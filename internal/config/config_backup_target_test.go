package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupTargetSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{
		"s3i": {PITR: PITRConfig{Enabled: true, PgBackRestStanza: "pgcli_s3i", BackupTarget: BackupTargetS3}},
		"loc": {PITR: PITRConfig{Enabled: true, PgBackRestStanza: "pgcli_loc"}},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Instances["s3i"].PITR.BackupTarget != BackupTargetS3 {
		t.Errorf("s3 target not persisted: %q", got.Instances["s3i"].PITR.BackupTarget)
	}
	if got.Instances["loc"].PITR.BackupTarget != "" {
		t.Errorf("default target must stay empty (omitempty): %q", got.Instances["loc"].PITR.BackupTarget)
	}
	// A local-target instance must not serialize the key at all.
	raw, _ := os.ReadFile(path)
	if strings.Count(string(raw), "backup_target") != 1 {
		t.Errorf("backup_target must appear only for the s3 instance:\n%s", raw)
	}
}

func TestInstanceDefaultsBackupTargetEmpty(t *testing.T) {
	cfg := Default()
	inst := cfg.InstanceDefaults("demo")
	if !inst.PITR.Enabled {
		t.Error("InstanceDefaults must keep PITR enabled for a fresh primary")
	}
	if inst.PITR.BackupTarget != "" {
		t.Errorf("InstanceDefaults backup_target = %q, want empty (local)", inst.PITR.BackupTarget)
	}
}

func TestSetInstanceMergesBackupTarget(t *testing.T) {
	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{
		"s3i": {PITR: PITRConfig{Enabled: true, PgBackRestStanza: "pgcli_s3i", BackupTarget: BackupTargetS3}},
		"loc": {PITR: PITRConfig{Enabled: true, PgBackRestStanza: "pgcli_loc"}},
	}
	if err := cfg.SetInstance("s3i"); err != nil {
		t.Fatalf("SetInstance: %v", err)
	}
	if cfg.PITR.BackupTarget != BackupTargetS3 {
		t.Errorf("merged backup_target = %q, want s3", cfg.PITR.BackupTarget)
	}
	// The previous instance's value must not leak into a local one.
	if err := cfg.SetInstance("loc"); err != nil {
		t.Fatalf("SetInstance: %v", err)
	}
	if cfg.PITR.BackupTarget != "" {
		t.Errorf("merged backup_target = %q after switching to a local instance, want empty", cfg.PITR.BackupTarget)
	}
}

func TestValidateBackupTarget(t *testing.T) {
	full := &BackupRepoS3{Endpoint: "10.0.0.9:9000", Bucket: "b", AccessKey: "k", SecretKey: "v"}

	if err := ValidateBackupTarget("", nil); err != nil {
		t.Errorf("empty target must be valid: %v", err)
	}
	if err := ValidateBackupTarget(BackupTargetLocal, nil); err != nil {
		t.Errorf("local target must be valid: %v", err)
	}
	if err := ValidateBackupTarget(BackupTargetS3, full); err != nil {
		t.Errorf("s3 with a complete repo must be valid: %v", err)
	}
	if err := ValidateBackupTarget("glacier", full); err == nil {
		t.Error("unknown target must be rejected")
	}
	if err := ValidateBackupTarget(BackupTargetS3, nil); err == nil {
		t.Error("s3 without a repo must be rejected")
	}
	for _, mutate := range []func(*BackupRepoS3){
		func(r *BackupRepoS3) { r.Endpoint = "" },
		func(r *BackupRepoS3) { r.Bucket = "" },
		func(r *BackupRepoS3) { r.AccessKey = "" },
		func(r *BackupRepoS3) { r.SecretKey = "" },
	} {
		r := *full
		mutate(&r)
		if err := ValidateBackupTarget(BackupTargetS3, &r); err == nil {
			t.Errorf("s3 with an incomplete repo (%+v) must be rejected", r)
		}
	}
}

func TestValidateRejectsBadInstanceBackupTarget(t *testing.T) {
	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{
		"bad": {PITR: PITRConfig{Enabled: true, PgBackRestStanza: "pgcli_bad", BackupTarget: "nfs"}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "backup_target") {
		t.Fatalf("Validate must reject an invalid per-instance backup_target, got %v", err)
	}

	bad := cfg.Instances["bad"]
	bad.PITR.BackupTarget = BackupTargetS3
	cfg.Instances["bad"] = bad
	if err := cfg.Validate(); err != nil {
		t.Fatalf("s3 is a valid shape at any host with/without the repo: %v", err)
	}
}
