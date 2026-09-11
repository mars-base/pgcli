package config

import (
	"path/filepath"
	"testing"
)

func TestMinioDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.Addons.Minio = map[string]MinioConfig{
		"store": {},
	}

	cfg.ApplyDefaults()

	mc, ok := cfg.Addons.Minio["store"]
	if !ok {
		t.Fatal("minio addon missing after ApplyDefaults")
	}
	if mc.Name != "store" {
		t.Errorf("name = %q, want store", mc.Name)
	}
	if mc.ContainerName != "pgcli-minio-store" {
		t.Errorf("container name = %q, want pgcli-minio-store", mc.ContainerName)
	}
	if mc.ImageTag != DefaultMinioImageTag {
		t.Errorf("image tag = %q, want %q", mc.ImageTag, DefaultMinioImageTag)
	}
	if mc.Listen != "127.0.0.1" {
		t.Errorf("listen = %q, want 127.0.0.1", mc.Listen)
	}
	if mc.RootUser != "admin" {
		t.Errorf("root user = %q, want admin", mc.RootUser)
	}
	// DataDir is resolved by the manager at container-creation time, not here.
	if mc.DataDir != "" {
		t.Errorf("data dir = %q, want empty (manager-resolved)", mc.DataDir)
	}
	if mc.APIPort < cfg.MinioStartPort {
		t.Errorf("api port %d below base %d", mc.APIPort, cfg.MinioStartPort)
	}
	if mc.ConsolePort <= mc.APIPort {
		t.Errorf("console port %d must be above api port %d", mc.ConsolePort, mc.APIPort)
	}
}

// Manual ports push the shared cursor: the auto-assigned instance lands on the
// two ports right above the explicit pair, keeping API/console consecutive.
func TestMinioManualPortCursor(t *testing.T) {
	cfg := Default()
	cfg.Addons.Minio = map[string]MinioConfig{
		"a": {APIPort: 19876, ConsolePort: 19877},
		"b": {},
	}

	cfg.ApplyDefaults()

	a := cfg.Addons.Minio["a"]
	if a.APIPort != 19876 || a.ConsolePort != 19877 {
		t.Errorf("explicit ports changed: api=%d console=%d", a.APIPort, a.ConsolePort)
	}
	b := cfg.Addons.Minio["b"]
	if b.APIPort != 19878 || b.ConsolePort != 19879 {
		t.Errorf("auto ports = %d/%d, want 19878/19879", b.APIPort, b.ConsolePort)
	}
}

func TestMinioSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.Minio = map[string]MinioConfig{
		"store": {
			ContainerName: "pgcli-minio-store",
			Name:          "store",
			ImageTag:      DefaultMinioImageTag,
			DataDir:       "/srv/minio",
			Listen:        "127.0.0.1",
			APIPort:       9000,
			ConsolePort:   9001,
			RootUser:      "admin",
			RootPassword:  "s3cret",
			Autostart:     true,
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	mc, ok := got.Addons.Minio["store"]
	if !ok {
		t.Fatal("minio addon not persisted")
	}
	if mc.APIPort != 9000 || mc.ConsolePort != 9001 {
		t.Errorf("ports not persisted: api=%d console=%d", mc.APIPort, mc.ConsolePort)
	}
	if mc.DataDir != "/srv/minio" {
		t.Errorf("data dir not persisted: %q", mc.DataDir)
	}
	if mc.RootUser != "admin" || mc.RootPassword != "s3cret" {
		t.Errorf("credentials not persisted: user=%q", mc.RootUser)
	}
	if !mc.Autostart {
		t.Error("autostart not persisted")
	}

	// A second ApplyDefaults must not move already-assigned ports.
	got.ApplyDefaults()
	again := got.Addons.Minio["store"]
	if again.APIPort != 9000 || again.ConsolePort != 9001 {
		t.Errorf("ApplyDefaults re-assigned ports: %d/%d", again.APIPort, again.ConsolePort)
	}
}
