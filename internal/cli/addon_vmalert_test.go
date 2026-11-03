package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
)

func TestVmalertCommandTree(t *testing.T) {
	var reload, status bool
	for _, c := range addonVmalertCmd.Commands() {
		switch c.Name() {
		case "reload":
			reload = true
		case "status":
			status = true
		}
	}
	if !reload || !status {
		t.Fatalf("vmalert subcommands: reload=%v status=%v, want both", reload, status)
	}
	if got := addonVmalertCmd.PersistentFlags().Lookup("name").DefValue; got != "vmalert" {
		t.Errorf("--name default = %q, want \"vmalert\"", got)
	}
}

func TestVmalertInstallFlagsRegistered(t *testing.T) {
	for _, name := range []string{
		"datasource-url", "notifier-url", "remote-write-url", "remote-read-url",
		"external-label", "external-url", "rules-dir", "templates-dir",
	} {
		if addonInstallCmd.Flags().Lookup(name) == nil {
			t.Errorf("install flag --%s is not registered", name)
		}
	}
}

// newVmalertFlagCmd mirrors the install flag set used by
// applyVmalertInstallFlags so Changed semantics can be exercised without a
// config path or podman.
func newVmalertFlagCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "install"}
	f := cmd.Flags()
	f.String("name", "", "")
	f.String("image", "", "")
	f.Int("port", 0, "")
	f.String("listen", "", "")
	f.String("datasource-url", "", "")
	f.StringArray("notifier-url", nil, "")
	f.String("rules-dir", "", "")
	f.String("templates-dir", "", "")
	f.StringArray("remote-write-url", nil, "")
	f.StringArray("remote-read-url", nil, "")
	f.StringArray("external-label", nil, "")
	f.String("external-url", "", "")
	f.StringArray("extra-arg", nil, "")
	return cmd
}

func TestApplyVmalertInstallFlagsChangedOnly(t *testing.T) {
	existing := config.VmalertConfig{
		Name:            "vm1",
		DatasourceURL:   "http://127.0.0.1:8428",
		NotifierURLs:    []string{"http://am-a:9093"},
		RemoteWriteURLs: []string{"http://rw-a/api/v1/write"},
		ExternalLabels:  []string{"replica=a"},
		ExtraArgs:       []string{"-dryRun"},
	}
	cmd := newVmalertFlagCmd()
	// Nothing changed: the stored config must survive untouched.
	got := applyVmalertInstallFlags(existing, cmd)
	if got.DatasourceURL != existing.DatasourceURL || len(got.NotifierURLs) != 1 || got.NotifierURLs[0] != "http://am-a:9093" {
		t.Fatalf("unchanged flags mutated config: %+v", got)
	}

	if err := cmd.Flags().Set("datasource-url", "http://127.0.0.1:9428"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("notifier-url", "http://am-b:9093"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("notifier-url", "http://am-c:9093"); err != nil {
		t.Fatal(err)
	}
	got = applyVmalertInstallFlags(existing, cmd)
	if got.DatasourceURL != "http://127.0.0.1:9428" {
		t.Errorf("datasource = %q", got.DatasourceURL)
	}
	// Repeatable flags replace the stored list wholesale.
	if len(got.NotifierURLs) != 2 || got.NotifierURLs[1] != "http://am-c:9093" {
		t.Errorf("notifiers = %#v, want the two new values", got.NotifierURLs)
	}
	// Unchanged fields keep their stored values.
	if len(got.RemoteWriteURLs) != 1 || got.RemoteWriteURLs[0] != "http://rw-a/api/v1/write" {
		t.Errorf("remote write = %#v", got.RemoteWriteURLs)
	}
	if len(got.ExtraArgs) != 1 || got.ExtraArgs[0] != "-dryRun" {
		t.Errorf("extra args = %#v", got.ExtraArgs)
	}
}

// vmalert has no authentication, so it must never enter the password index.
func TestAddonPasswordsExcludesVmalert(t *testing.T) {
	cfg := config.Default()
	cfg.ApplyDefaults()
	cfg.Addons.Vmalert = map[string]config.VmalertConfig{
		"vmalert": {Name: "vmalert"},
	}
	cfg.ApplyDefaults()

	all := addonPasswords(cfg)
	for key := range all {
		if strings.HasPrefix(key, "vmalert:") {
			t.Errorf("vmalert must not appear in the password index, found key %q", key)
		}
	}
	if _, ok := all["vmalert:vmalert"]; ok {
		t.Error("vmalert:vmalert unexpectedly present in addonPasswords")
	}
}
