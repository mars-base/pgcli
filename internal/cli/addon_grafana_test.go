package cli

import (
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// Grafana's entry in the password index is its WEB login password
// (GF_SECURITY_ADMIN_PASSWORD), the same shape as pgAdmin's — pgcli-generated
// and the addon's only credential, so `pg addon password grafana` must resolve
// the "<addon>:<name>" key the CLI builds from the positional arg + --name.
func TestAddonPasswordsIncludesGrafana(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.Grafana = map[string]config.GrafanaConfig{
		"ui": {Name: "ui", Password: "pw-grafana"},
	}
	all := addonPasswords(cfg)
	if got, ok := all["grafana:ui"]; !ok {
		t.Fatalf("missing key grafana:ui (have %v)", keysOf(all))
	} else if got != "pw-grafana" {
		t.Errorf("grafana:ui = %q, want pw-grafana", got)
	}
}
