package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutostartFieldsSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{
		"a": {Autostart: true},
		"b": {},
	}
	cfg.Backup.Autostart = true
	cfg.Instances["a"] = func() InstanceConfig {
		inst := cfg.Instances["a"]
		inst.Addons.PgBouncer = &PgBouncerConfig{ContainerName: "pgcli-pgbouncer-a", Autostart: true}
		return inst
	}()

	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.Instances["a"].Autostart {
		t.Error("instance autostart not persisted")
	}
	if got.Instances["b"].Autostart {
		t.Error("unset instance autostart should be false")
	}
	if !got.Backup.Autostart {
		t.Error("backup autostart not persisted")
	}
	if got.Instances["a"].Addons.PgBouncer == nil || !got.Instances["a"].Addons.PgBouncer.Autostart {
		t.Errorf("pgbouncer autostart not persisted, yaml:\n%s", data)
	}
}
