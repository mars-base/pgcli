package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// The negative counterpart to TestAddonPasswordsIncludesGrafana: Prometheus
// ships NO authentication, so it must never enter the password index (and by
// extension `pg addon password prometheus` resolves nothing). A stray Password
// field or an addonPasswords arm added later would break this and silently
// advertise a credential that does not exist.
func TestAddonPasswordsExcludesPrometheus(t *testing.T) {
	cfg := config.Default()
	cfg.ApplyDefaults()
	cfg.Addons.Prometheus = map[string]config.PrometheusConfig{
		"p1": {Name: "p1"},
	}
	cfg.ApplyDefaults() // must not conjure a password for prometheus either

	all := addonPasswords(cfg)
	for key := range all {
		if strings.HasPrefix(key, "prometheus:") {
			t.Errorf("prometheus must not appear in the password index, found key %q", key)
		}
	}
	if _, ok := all["prometheus:p1"]; ok {
		t.Error("prometheus:p1 unexpectedly present in addonPasswords")
	}
}
