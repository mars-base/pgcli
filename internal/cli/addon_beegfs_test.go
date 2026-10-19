package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
)

// BeeGFS's entry in the password index is the connection-auth secret the three
// daemons (and every client's /etc/beegfs/conn.auth) share. `pg addon password
// beegfs --name <fs>` must resolve the "<addon>:<name>" key the CLI builds
// from the positional arg + --name. An empty ConnAuth (a revived store whose
// secret lives in the daemons, not the config) must still produce the key —
// presence/absence is rendered by the caller, not by dropping the entry.
func TestAddonPasswordsIncludesBeeGFS(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.BeeGFS = map[string]config.BeeGFSConfig{
		"fs1": {Name: "fs1", ConnAuth: "secret-beegfs"},
		"fs2": {Name: "fs2"},
	}
	all := addonPasswords(cfg)
	if got, ok := all["beegfs:fs1"]; !ok {
		t.Fatalf("missing key beegfs:fs1 (have %v)", keysOf(all))
	} else if got != "secret-beegfs" {
		t.Errorf("beegfs:fs1 = %q, want secret-beegfs", got)
	}
	if got, ok := all["beegfs:fs2"]; !ok {
		t.Fatalf("beegfs:fs2 must key even with no secret (have %v)", keysOf(all))
	} else if got != "" {
		t.Errorf("beegfs:fs2 = %q, want empty", got)
	}
}

// The two mgmtd port overrides are the ONLY movable BeeGFS ports (meta 8005
// and storage 8003 cannot be relocated at all — verified on vm01), and they
// must be plain Ints: Changed-gating distinguishes "not passed" from "passed
// as 0", which a default-equal-to-0 scheme cannot.
func TestBeeGFSWiringFlagsRegistered(t *testing.T) {
	for _, name := range []string{"beegfs-mgmtd-port", "beegfs-mgmtd-grpc-port"} {
		f := addonInstallCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("--%s is not registered", name)
		}
		if f.Value.Type() != "int" {
			t.Errorf("--%s is %s, want int", name, f.Value.Type())
		}
	}
}

// v2 cross-host flags + the license carrier: the CSV roles, the join target
// address, the int node id (Changed-gated like the ports), the repeatable
// per-disk storage target, and the --license-file PEM path (string, not a
// bool — the gate distinguishes "not passed" from "passed an empty path").
func TestBeeGFSV2FlagsRegistered(t *testing.T) {
	want := map[string]string{
		"roles":          "string",
		"mgmtd-host":     "string",
		"node-id":        "int",
		"storage-target": "stringArray",
		"license-file":   "string",
	}
	for name, typ := range want {
		f := addonInstallCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("--%s is not registered", name)
		}
		if f.Value.Type() != typ {
			t.Errorf("--%s is %s, want %s", name, f.Value.Type(), typ)
		}
	}
}

// The license subcommand must carry the status/--set/--clear shape.
func TestBeeGFSLicenseSubcommandFlags(t *testing.T) {
	for name, typ := range map[string]string{"name": "string", "set": "string", "clear": "bool"} {
		f := addonBeeGFSLicenseCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("license --%s is not registered", name)
		}
		if f.Value.Type() != typ {
			t.Errorf("license --%s is %s, want %s", name, f.Value.Type(), typ)
		}
	}
}

// beegfsInstallCmd mirrors the real install flag set for the join-gate tests.
func beegfsInstallCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "install"}
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("image", "", "")
	cmd.Flags().String("data-dir", "", "")
	cmd.Flags().String("listen", "", "")
	cmd.Flags().String("password", "", "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().Int("beegfs-mgmtd-port", 0, "")
	cmd.Flags().Int("beegfs-mgmtd-grpc-port", 0, "")
	cmd.Flags().String("roles", "", "")
	cmd.Flags().String("mgmtd-host", "", "")
	cmd.Flags().Int("node-id", 0, "")
	cmd.Flags().StringArray("storage-target", nil, "")
	cmd.Flags().String("license-file", "", "")
	return cmd
}

// The join gate must fire BEFORE any podman work (it does not depend on the
// cluster's container state): a host whose roles exclude mgmtd must be given
// the cluster's existing conn-auth secret — pgcli never generates a secret for
// a member of an existing cluster, and without mgmtd there is nothing local to
// generate one from.
func TestBeeGFSJoinInstallGates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := config.Default()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })

	t.Run("roles without mgmtd needs mgmtd-host", func(t *testing.T) {
		cmd := beegfsInstallCmd()
		cmd.Flags().Set("roles", "storage")
		cmd.Flags().Set("password", "some-secret")
		err := runAddonInstallBeeGFS(cmd)
		if err == nil || !strings.Contains(err.Error(), "--mgmtd-host is required") {
			t.Fatalf("want mgmtd-host-required error, got %v", err)
		}
	})

	t.Run("join without secret is refused, never generated", func(t *testing.T) {
		cmd := beegfsInstallCmd()
		cmd.Flags().Set("roles", "meta,storage")
		cmd.Flags().Set("mgmtd-host", "10.0.0.5")
		err := runAddonInstallBeeGFS(cmd)
		if err == nil {
			t.Fatal("want conn-auth error, got nil")
		}
		for _, want := range []string{"connection-auth secret", "pg addon password beegfs", "join-hint"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing pointer %q", err.Error(), want)
			}
		}
	})

	t.Run("unknown role is refused", func(t *testing.T) {
		cmd := beegfsInstallCmd()
		cmd.Flags().Set("roles", "metad")
		err := runAddonInstallBeeGFS(cmd)
		if err == nil || !strings.Contains(err.Error(), "unknown service") {
			t.Fatalf("want unknown-service error, got %v", err)
		}
	})

	t.Run("join host carrying a license is refused", func(t *testing.T) {
		pem := filepath.Join(t.TempDir(), "license.pem")
		if err := os.WriteFile(pem, []byte("-----BEGIN CERTIFICATE-----\nMIIBfake\n-----END CERTIFICATE-----\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := beegfsInstallCmd()
		cmd.Flags().Set("roles", "storage")
		cmd.Flags().Set("mgmtd-host", "10.0.0.5")
		cmd.Flags().Set("password", "some-secret")
		cmd.Flags().Set("license-file", pem)
		err := runAddonInstallBeeGFS(cmd)
		if err == nil || !strings.Contains(err.Error(), "only valid on the host running mgmtd") {
			t.Fatalf("want mgmtd-only license error, got %v", err)
		}
	})
}

// readBeeGFSLicense is the CLI's shape gate before a PEM reaches the config:
// the file must exist, be non-empty, and look like a PEM block. Crypto
// validity is mgmtd's job, never pgcli's.
func TestReadBeeGFSLicense(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "license.pem")
	if err := os.WriteFile(good, []byte("-----BEGIN CERTIFICATE-----\nMIIBfake\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if pem, err := readBeeGFSLicense(good); err != nil {
		t.Fatalf("valid PEM rejected: %v", err)
	} else if !strings.Contains(pem, "BEGIN CERTIFICATE") {
		t.Errorf("PEM body lost: %q", pem)
	}

	if _, err := readBeeGFSLicense(filepath.Join(dir, "missing.pem")); err == nil {
		t.Error("missing file must error")
	}
	for name, content := range map[string]string{"empty": "", "not a cert": "just some text"} {
		bad := filepath.Join(dir, name)
		if err := os.WriteFile(bad, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBeeGFSLicense(bad); err == nil {
			t.Errorf("%s: want rejection, got nil", name)
		}
	}
}

// join-hint renders the verbatim per-host commands. Run on the host holding the
// mgmtd entry, the secret and (if moved) the ports must all show up; the
// wildcard Listen becomes a placeholder since 0.0.0.0 cannot be dialed from
// another machine.
func TestBeeGFSJoinHintRenders(t *testing.T) {
	write := func(t *testing.T, fs map[string]config.BeeGFSConfig) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "pg.yaml")
		cfg := config.Default()
		cfg.Addons.BeeGFS = fs
		if err := cfg.Save(path); err != nil {
			t.Fatalf("save: %v", err)
		}
		oldPath := cfgPath
		cfgPath = path
		t.Cleanup(func() { cfgPath = oldPath })
	}

	write(t, map[string]config.BeeGFSConfig{
		"fs1": {Name: "fs1", ContainerPrefix: "pgcli-beegfs-fs1", ImageTag: "8.4.0",
			Listen: "0.0.0.0", MgmtdPort: config.DefaultBeeGFSMgmtdPort,
			MgmtdGRPCPort: config.DefaultBeeGFSMgmtdGRPCPort, ConnAuth: "shared-auth"},
	})
	cmd := beegfsSubCmd("fs1")
	out := captureStdout(t, func() {
		if err := runAddonBeeGFSJoinHint(cmd); err != nil {
			t.Fatalf("join-hint: %v", err)
		}
	})
	for _, want := range []string{
		"--roles storage --mgmtd-host <mgmtd-host-ip>",
		"--node-id <n> --password shared-auth",
		"--storage-target <mnt-disk-1>",
		"--roles meta --mgmtd-host <mgmtd-host-ip>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered hint missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--beemsg-port") || strings.Contains(out, "connMgmtdPort") {
		t.Errorf("stock ports must not appear in the hint:\n%s", out)
	}

	t.Run("explicit listen and moved ports", func(t *testing.T) {
		write(t, map[string]config.BeeGFSConfig{
			"fs1": {Name: "fs1", ContainerPrefix: "pgcli-beegfs-fs1", ImageTag: "8.4.0",
				Listen: "10.0.0.5", MgmtdPort: 18008, MgmtdGRPCPort: 18010,
				ConnAuth: "shared-auth"},
		})
		out := captureStdout(t, func() {
			if err := runAddonBeeGFSJoinHint(beegfsSubCmd("fs1")); err != nil {
				t.Fatalf("join-hint: %v", err)
			}
		})
		for _, want := range []string{"--mgmtd-host 10.0.0.5", "--beegfs-mgmtd-port 18008", "--beegfs-mgmtd-grpc-port 18010", "10.0.0.5:18010"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q:\n%s", want, out)
			}
		}
		// No license on this entry → no license note.
		if strings.Contains(out, "carries a license") {
			t.Errorf("unlicensed filesystem must not print the license note:\n%s", out)
		}
	})

	t.Run("licensed mgmtd host notes joiners need nothing", func(t *testing.T) {
		write(t, map[string]config.BeeGFSConfig{
			"fs1": {Name: "fs1", ContainerPrefix: "pgcli-beegfs-fs1", ImageTag: "8.4.0",
				Listen: "10.0.0.5", MgmtdPort: config.DefaultBeeGFSMgmtdPort,
				MgmtdGRPCPort: config.DefaultBeeGFSMgmtdGRPCPort, ConnAuth: "shared-auth",
				License: "-----BEGIN CERTIFICATE-----\nMIIBfake\n-----END CERTIFICATE-----"},
		})
		out := captureStdout(t, func() {
			if err := runAddonBeeGFSJoinHint(beegfsSubCmd("fs1")); err != nil {
				t.Fatalf("join-hint: %v", err)
			}
		})
		if !strings.Contains(out, "carries a license") {
			t.Errorf("licensed host must note that joiners need nothing:\n%s", out)
		}
		// The license must NOT leak into the rendered join command itself.
		if strings.Contains(out, "--license-file") || strings.Contains(out, "BEGIN CERTIFICATE") {
			t.Errorf("join commands must not carry the license:\n%s", out)
		}
	})
}

func beegfsSubCmd(name string) *cobra.Command {
	cmd := &cobra.Command{Use: "join-hint"}
	cmd.Flags().String("name", "", "")
	cmd.Flags().Set("name", name)
	return cmd
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// was written — the hint renderers print, they do not return.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	out := <-done
	_ = r.Close()
	return out
}
