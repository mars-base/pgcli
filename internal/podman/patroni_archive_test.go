package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func provisioningFixture(t *testing.T, s3 *config.BackupRepoS3) *PatroniManager {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.Backup.Repo.S3 = s3
	return &PatroniManager{cfg: cfg, dataDir: dataDir, podman: "podman"}
}

func TestBackupProvisioningWarningQuietWhenArchiveConfExists(t *testing.T) {
	m := provisioningFixture(t, testS3Repo())
	if err := os.WriteFile(filepath.Join(m.dataDir, "pgbackrest-archive.conf"), []byte("[global]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := m.BackupProvisioningWarning(); w != "" {
		t.Errorf("provisioned host must stay silent, got %q", w)
	}
}

func TestBackupProvisioningWarningNoRepo(t *testing.T) {
	m := provisioningFixture(t, nil)
	w := m.BackupProvisioningWarning()
	if !strings.Contains(w, "No S3 backup repo") || !strings.Contains(w, "pg backup setup") {
		t.Errorf("no-repo warning = %q, want the missing-repo text with the setup hint", w)
	}
}

func TestBackupProvisioningWarningRepoButSetupNotRun(t *testing.T) {
	m := provisioningFixture(t, testS3Repo())
	w := m.BackupProvisioningWarning()
	if !strings.Contains(w, "pg backup setup") || !strings.Contains(w, "has not run") {
		t.Errorf("repo-set-but-unprovisioned warning = %q, want the not-run text with the setup hint", w)
	}
}
