package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
)

// pgInstallCmd builds a bare command carrying exactly the flags
// runAddonInstallPgAdmin reads, so the validation paths can be exercised
// without touching cobra's wiring or podman.
func pgInstallCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "install"}
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("image", "", "")
	cmd.Flags().String("data-dir", "", "")
	cmd.Flags().Int("port", 0, "")
	cmd.Flags().String("listen", "", "")
	cmd.Flags().String("email", "", "")
	cmd.Flags().String("password", "", "")
	cmd.Flags().String("dsn", "", "")
	cmd.Flags().String("pg-name", "", "")
	cmd.Flags().Bool("force", false, "")
	return cmd
}

func TestPgAdminSeededDisplay(t *testing.T) {
	// --pg-name seed: the instance name is the display name...
	got := pgadminSeededDisplay(config.PgAdminConfig{
		DSN:        "postgres://app:secret@127.0.0.1:5432/appdb",
		ServerName: "app",
	})
	if got != "app" {
		t.Errorf("display = %q, want app", got)
	}
	// ...and --dsn seed shows a generic label. Either way the DSN (which carries
	// a password) must never be echoed into the install summary.
	got = pgadminSeededDisplay(config.PgAdminConfig{DSN: "postgres://app:secret@127.0.0.1:5432/appdb"})
	if !strings.Contains(got, "dsn") {
		t.Errorf("display = %q, want a from-dsn label", got)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "postgres://") {
		t.Errorf("display leaked the DSN: %q", got)
	}
}

func TestAddonPasswordsIncludesPgAdmin(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.PgAdmin = map[string]config.PgAdminConfig{
		"ui": {Name: "ui", Password: "pw-pgadmin"},
	}
	all := addonPasswords(cfg)
	if got, ok := all["pgadmin:ui"]; !ok {
		t.Fatalf("missing key pgadmin:ui (have %v)", keysOf(all))
	} else if got != "pw-pgadmin" {
		t.Errorf("pgadmin:ui = %q, want pw-pgadmin", got)
	}
}

// The seed-source validation must fire before any image pull or container work:
// --dsn and --pg-name are mutually exclusive for pgadmin (--pg-name is a DSN
// *source* here, not the map key as it is for pgbouncer/postgrest), and an
// unknown --pg-name is a plain config error.
func TestPgAdminInstallSeedValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := config.Default()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	oldPath := cfgPath
	cfgPath = path
	t.Cleanup(func() { cfgPath = oldPath })

	t.Run("--dsn with --pg-name rejected", func(t *testing.T) {
		cmd := pgInstallCmd()
		cmd.Flags().Set("dsn", "postgres://app@remote:5432/appdb")
		cmd.Flags().Set("pg-name", "missing")
		err := runAddonInstallPgAdmin(cmd)
		if err == nil {
			t.Fatal("expected an error for --dsn together with --pg-name")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("error %q missing %q", err.Error(), "mutually exclusive")
		}
	})

	t.Run("unknown --pg-name rejected", func(t *testing.T) {
		cmd := pgInstallCmd()
		cmd.Flags().Set("pg-name", "nope")
		err := runAddonInstallPgAdmin(cmd)
		if err == nil {
			t.Fatal("expected an error for an unknown instance name")
		}
		if !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "not found") {
			t.Errorf("error %q should name the missing instance", err.Error())
		}
	})
}
