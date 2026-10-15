package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// Same negative assertion as TestAddonPasswordsExcludesPrometheus: Alertmanager
// ships no authentication at all, so it must never enter the password index —
// `pg addon password alertmanager` has to resolve nothing rather than advertise
// a credential that does not exist.
func TestAddonPasswordsExcludesAlertmanager(t *testing.T) {
	cfg := config.Default()
	cfg.ApplyDefaults()
	cfg.Addons.Alertmanager = map[string]config.AlertmanagerConfig{
		"am1": {Name: "am1"},
	}
	cfg.ApplyDefaults() // must not conjure a password for alertmanager either

	all := addonPasswords(cfg)
	for key := range all {
		if strings.HasPrefix(key, "alertmanager:") {
			t.Errorf("alertmanager must not appear in the password index, found key %q", key)
		}
	}
	if _, ok := all["alertmanager:am1"]; ok {
		t.Error("alertmanager:am1 unexpectedly present in addonPasswords")
	}
}
