package cli

import (
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// Gitea's entry in the password index is its WEB login password — the one
// pgcli created the admin account with at install (same shape as Grafana's,
// and with the same caveat: informational on a revived store whose admin
// pre-dates the config entry). `pg addon password gitea` must resolve the
// "<addon>:<name>" key the CLI builds from the positional arg + --name.
func TestAddonPasswordsIncludesGitea(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.Gitea = map[string]config.GiteaConfig{
		"git1": {Name: "git1", Password: "pw-gitea"},
	}
	all := addonPasswords(cfg)
	if got, ok := all["gitea:git1"]; !ok {
		t.Fatalf("missing key gitea:git1 (have %v)", keysOf(all))
	} else if got != "pw-gitea" {
		t.Errorf("gitea:git1 = %q, want pw-gitea", got)
	}
}
