package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/config"
)

func TestSafelineFlagName(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("name", "", "")
	if got := safelineFlagName(cmd); got != "safeline" {
		t.Errorf("default name = %q, want safeline", got)
	}
	if err := cmd.Flags().Set("name", "waf1"); err != nil {
		t.Fatal(err)
	}
	if got := safelineFlagName(cmd); got != "waf1" {
		t.Errorf("name = %q, want waf1", got)
	}
}

func TestSafelineInstallFlagsRegistered(t *testing.T) {
	want := map[string]string{
		"name":             "string",
		"version":          "string",
		"postgres-version": "string",
		"subnet-prefix":    "string",
		"port":             "int",
		"listen":           "string",
		"data-dir":         "string",
		"force":            "bool",
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
	for _, name := range []string{"postgres-version", "subnet-prefix"} {
		if f := addonInstallCmd.Flags().Lookup(name); f != nil && !strings.Contains(f.Usage, "safeline") {
			t.Errorf("--%s help does not mention safeline: %q", name, f.Usage)
		}
	}
	if f := addonRemoveCmd.Flags().Lookup("clean-data"); f == nil || !strings.Contains(f.Usage, "safeline") {
		t.Errorf("--clean-data help does not mention safeline: %+v", f)
	}
}

func TestSafelineAutostartFlagRegistered(t *testing.T) {
	for _, cmd := range []*cobra.Command{autostartEnableCmd, autostartDisableCmd} {
		f := cmd.Flags().Lookup("safeline")
		if f == nil {
			t.Fatalf("%s: --safeline is not registered", cmd.Name())
		}
		if f.Value.Type() != "bool" {
			t.Errorf("%s: --safeline is %s, want bool", cmd.Name(), f.Value.Type())
		}
		if !strings.Contains(f.Usage, "SafeLine") {
			t.Errorf("%s: --safeline help does not mention SafeLine: %q", cmd.Name(), f.Usage)
		}
	}
}

func TestSafelineLogsServiceHelp(t *testing.T) {
	f := logsAddonCmd.Flags().Lookup("service")
	if f == nil {
		t.Fatal("logs addon: --service is not registered")
	}
	for _, role := range []string{"safeline", "mgt", "tengine"} {
		if !strings.Contains(f.Usage, role) {
			t.Errorf("--service help does not mention %q: %q", role, f.Usage)
		}
	}
}

// safelineFlagSet registers every flag rejectSafelineFlags/applySafelineInstallFlags
// read, then parses args. The rejected flags are present so tests can prove the
// rejection rather than a parse failure.
func safelineFlagSet(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("safeline", pflag.ContinueOnError)
	fs.String("version", "", "")
	fs.String("postgres-version", "", "")
	fs.String("subnet-prefix", "", "")
	fs.Int("port", 0, "")
	fs.String("listen", "", "")
	fs.String("data-dir", "", "")
	fs.Bool("force", false, "")
	for _, name := range []string{
		"image", "dsn", "pg-name", "password", "config-option", "config-file",
		"tls", "tls-pem", "tls-cert", "tls-key", "advertise-host", "cluster",
		"peer", "join",
	} {
		fs.String(name, "", "")
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return fs
}

func TestRejectSafelineFlags(t *testing.T) {
	if err := rejectSafelineFlags(safelineFlagSet(t)); err != nil {
		t.Fatalf("no flags rejected: %v", err)
	}
	for _, flag := range []string{
		"--image", "--dsn", "--pg-name", "--password", "--config-option",
		"--config-file", "--tls", "--tls-pem", "--tls-cert", "--tls-key",
		"--advertise-host", "--cluster", "--peer", "--join",
	} {
		err := rejectSafelineFlags(safelineFlagSet(t, flag, "x"))
		if err == nil || !strings.Contains(err.Error(), flag) {
			t.Errorf("rejectSafelineFlags(%s) = %v, want error mentioning %s", flag, err, flag)
		}
	}
}

func TestApplySafelineInstallFlags(t *testing.T) {
	baseDir := t.TempDir()
	sc := config.SafelineConfig{}
	fs := safelineFlagSet(t,
		"--version", "9.5.1",
		"--postgres-version", "15.18",
		"--subnet-prefix", "172.22.240",
		"--port", "12443",
		"--listen", "10.0.0.5",
		"--data-dir", "wafdata",
	)
	if err := applySafelineInstallFlags(&sc, fs, baseDir); err != nil {
		t.Fatalf("applySafelineInstallFlags: %v", err)
	}
	if sc.Version != "9.5.1" || sc.PostgresVersion != "15.18" {
		t.Errorf("versions = %q/%q", sc.Version, sc.PostgresVersion)
	}
	if sc.SubnetPrefix != "172.22.240" || sc.ConsolePort != 12443 || sc.Listen != "10.0.0.5" {
		t.Errorf("network = %q/%d/%q", sc.SubnetPrefix, sc.ConsolePort, sc.Listen)
	}
	if want := filepath.Join(baseDir, "wafdata"); sc.DataDir != want {
		t.Errorf("relative --data-dir resolved to %q, want %q", sc.DataDir, want)
	}

	abs := config.SafelineConfig{}
	if err := applySafelineInstallFlags(&abs, safelineFlagSet(t, "--data-dir", "/var/lib/wafdata"), baseDir); err != nil {
		t.Fatal(err)
	}
	if abs.DataDir != "/var/lib/wafdata" {
		t.Errorf("absolute --data-dir = %q", abs.DataDir)
	}

	for _, bad := range []struct {
		args    []string
		wantSub string
	}{
		{[]string{"--version", "9.4.7-rc1"}, "invalid --version"},
		{[]string{"--postgres-version", "15"}, "invalid --postgres-version"},
		{[]string{"--subnet-prefix", "172.22"}, "invalid --subnet-prefix"},
		{[]string{"--subnet-prefix", "172.22.999"}, "invalid --subnet-prefix"},
		{[]string{"--port", "70000"}, "--port must be between"},
		{[]string{"--port=-1"}, "--port must be between"},
	} {
		sc := config.SafelineConfig{}
		err := applySafelineInstallFlags(&sc, safelineFlagSet(t, bad.args...), baseDir)
		if err == nil || !strings.Contains(err.Error(), bad.wantSub) {
			t.Errorf("applySafelineInstallFlags(%v) = %v, want substring %q", bad.args, err, bad.wantSub)
		}
	}
}

func TestCheckSafelineNoOpFlags(t *testing.T) {
	stored := config.SafelineConfig{
		Version:         config.DefaultSafelineVersion,
		PostgresVersion: config.DefaultSafelinePostgresVersion,
		SubnetPrefix:    config.DefaultSafelineSubnetPrefix,
		ConsolePort:     config.DefaultSafelineConsolePort,
		Listen:          "0.0.0.0",
		DataDir:         "/srv/pg/addon/safeline/waf1",
	}
	baseDir := "/srv/pg"

	if err := checkSafelineNoOpFlags("waf1", stored, safelineFlagSet(t), baseDir); err != nil {
		t.Errorf("no flags should be a no-op: %v", err)
	}
	same := safelineFlagSet(t,
		"--version", config.DefaultSafelineVersion,
		"--port", "9443",
		"--listen", "0.0.0.0",
		"--data-dir", "/srv/pg/addon/safeline/waf1",
	)
	if err := checkSafelineNoOpFlags("waf1", stored, same, baseDir); err != nil {
		t.Errorf("explicitly restating stored values should be a no-op: %v", err)
	}

	for _, changed := range []struct {
		args     []string
		wantSubs []string
	}{
		{[]string{"--version", "9.5.0"}, []string{"--version", "--force"}},
		{[]string{"--postgres-version", "15.20"}, []string{"--postgres-version", "--force"}},
		{[]string{"--subnet-prefix", "172.22.240"}, []string{"--subnet-prefix", "--force"}},
		{[]string{"--port", "9444"}, []string{"--port", "--force"}},
		{[]string{"--listen", "10.0.0.5"}, []string{"--listen", "--force"}},
		{[]string{"--data-dir", "other"}, []string{"--data-dir", "--force"}},
	} {
		err := checkSafelineNoOpFlags("waf1", stored, safelineFlagSet(t, changed.args...), baseDir)
		if err == nil {
			t.Errorf("checkSafelineNoOpFlags(%v) accepted a drift", changed.args)
			continue
		}
		for _, sub := range changed.wantSubs {
			if !strings.Contains(err.Error(), sub) {
				t.Errorf("checkSafelineNoOpFlags(%v) = %v, want substring %q", changed.args, err, sub)
			}
		}
	}

	if err := checkSafelineNoOpFlags("waf1", stored, safelineFlagSet(t, "--version", "bad"), baseDir); err == nil ||
		!strings.Contains(err.Error(), "invalid --version") {
		t.Errorf("invalid flag on an installed stack = %v, want validation error", err)
	}
}

func TestAddonPasswordsIncludesSafeline(t *testing.T) {
	cfg := passwordCfg()
	cfg.Addons.Safeline = map[string]config.SafelineConfig{
		"waf1": {Name: "waf1", AdminPassword: "console-pw", PostgresPassword: "bundled-db-pw"},
	}
	all := addonPasswords(cfg)
	if got := all["safeline:waf1"]; got != "console-pw" {
		t.Errorf("safeline:waf1 = %q, want the console admin password", got)
	}
	// The bundled PostgreSQL password is an internal detail; exposing it would
	// put two secrets behind one key.
	for key, val := range all {
		if val == "bundled-db-pw" {
			t.Errorf("bundled postgres password exposed under %q", key)
		}
	}
}

func TestAddonPasswordResetRejectsOtherAddons(t *testing.T) {
	// The rejection must come before any config load, so this needs no pg.yaml.
	err := runAddonPassword("redis", "cache", "", true)
	if err == nil || !strings.Contains(err.Error(), "--reset is only supported for safeline") {
		t.Errorf("reset on redis = %v, want --reset rejection", err)
	}
}

func TestAddonPasswordResetFlagRegistered(t *testing.T) {
	f := addonPasswordCmd.Flags().Lookup("reset")
	if f == nil {
		t.Fatal("password --reset flag not registered")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--reset type = %q, want bool", f.Value.Type())
	}
}

func TestAddonReloadRejectsOtherAddons(t *testing.T) {
	// The rejection must come before any config load, so this needs no pg.yaml.
	err := runAddonReload("redis", "cache")
	if err == nil || !strings.Contains(err.Error(), "reload is only supported for safeline") {
		t.Errorf("reload on redis = %v, want reload rejection", err)
	}
}

func TestAddonReloadCommandRegistered(t *testing.T) {
	found := false
	for _, c := range addonCmd.Commands() {
		if c.Name() == "reload" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("pg addon reload is not registered")
	}
	f := addonReloadCmd.Flags().Lookup("name")
	if f == nil || f.Value.Type() != "string" {
		t.Errorf("reload --name flag = %+v, want a string flag", f)
	}
}
