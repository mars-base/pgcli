package config

import (
	"path/filepath"
	"testing"
)

func TestPredixyDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.Predixy = map[string]PredixyConfig{
		"proxy": {},
	}

	cfg.ApplyDefaults()

	pc, ok := cfg.Addons.Predixy["proxy"]
	if !ok {
		t.Fatal("predixy addon missing after ApplyDefaults")
	}
	if pc.Name != "proxy" {
		t.Errorf("name = %q, want proxy", pc.Name)
	}
	if pc.ContainerName != "pgcli-predixy-proxy" {
		t.Errorf("container name = %q, want pgcli-predixy-proxy", pc.ContainerName)
	}
	if pc.ImageTag != DefaultPredixyImageTag {
		t.Errorf("image tag = %q, want %q", pc.ImageTag, DefaultPredixyImageTag)
	}
	if pc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0 (the proxy's only access gate is AUTH)", pc.Listen)
	}
	if pc.Workers != DefaultPredixyWorkers {
		t.Errorf("workers = %d, want the factory default %d", pc.Workers, DefaultPredixyWorkers)
	}
	// Password and Backend are deliberately not defaulted: a generated proxy
	// password would not authenticate against the cluster it fronts, and an
	// auto-derived backend list would guess at the operator's cluster topology.
	if pc.Password != "" {
		t.Errorf("password = %q, want empty (operator-supplied)", pc.Password)
	}
	if len(pc.Backend) != 0 {
		t.Errorf("backend = %v, want empty (operator-supplied)", pc.Backend)
	}
	if pc.Port < cfg.PredixyStartPort {
		t.Errorf("port %d below base %d", pc.Port, cfg.PredixyStartPort)
	}
}

// Predixy owns its port pool (predixy_start_port), separate from redis's. A
// high unused base keeps the live-listener scan out of the exact expectations,
// same convention as TestRedisPortAssignment.
func TestPredixyPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.PredixyStartPort = 37617
	cfg.Addons.Predixy = map[string]PredixyConfig{
		"b": {},
		"a": {Port: 38116}, // explicit, above the base
		"c": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.Predixy["a"]; got.Port != 38116 {
		t.Errorf("explicit port changed: %d", got.Port)
	}
	if got := cfg.Addons.Predixy["b"]; got.Port != 38117 {
		t.Errorf("b port = %d, want 38117 (cursor must skip the reserved port)", got.Port)
	}
	if got := cfg.Addons.Predixy["c"]; got.Port != 38118 {
		t.Errorf("c port = %d, want 38118", got.Port)
	}

	// The predixy pool must not collide with the redis pool on the same host.
	cfg.Addons.Redis = map[string]RedisConfig{"cache": {}}
	cfg.RedisStartPort = 36379
	cfg.ApplyDefaults()
	if r := cfg.Addons.Redis["cache"]; r.Port == cfg.Addons.Predixy["c"].Port {
		t.Errorf("redis port %d collided with predixy port", r.Port)
	}
}

func TestPredixySaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Addons.Predixy = map[string]PredixyConfig{
		"proxy": {
			ContainerName: "pgcli-predixy-proxy",
			Name:          "proxy",
			ImageTag:      DefaultPredixyImageTag,
			Listen:        "127.0.0.1",
			Port:          37617,
			Workers:       4,
			Backend:       []string{"127.0.0.1:6379", "127.0.0.1:6380", "10.0.0.12:6379"},
			Password:      "cluster-shared-pw",
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
	pc, ok := got.Addons.Predixy["proxy"]
	if !ok {
		t.Fatal("predixy addon not persisted")
	}
	if pc.Listen != "127.0.0.1" {
		t.Errorf("listen not persisted: %q", pc.Listen)
	}
	if pc.Port != 37617 {
		t.Errorf("port not persisted: %d", pc.Port)
	}
	if pc.Workers != 4 {
		t.Errorf("workers not persisted: %d", pc.Workers)
	}
	if len(pc.Backend) != 3 || pc.Backend[2] != "10.0.0.12:6379" {
		t.Errorf("backend not persisted: %v", pc.Backend)
	}
	if pc.Password != "cluster-shared-pw" {
		t.Errorf("password not persisted: %q", pc.Password)
	}
	if !pc.Autostart {
		t.Error("autostart not persisted")
	}
	if pc.ImageTag != DefaultPredixyImageTag {
		t.Errorf("image tag not persisted: %q", pc.ImageTag)
	}

	// A second ApplyDefaults must not move the assigned port, drop a backend,
	// rewrite workers, or touch the stored password.
	got.ApplyDefaults()
	again := got.Addons.Predixy["proxy"]
	if again.Port != 37617 || again.Workers != 4 || again.Password != "cluster-shared-pw" {
		t.Errorf("ApplyDefaults mutated stored values: port=%d workers=%d", again.Port, again.Workers)
	}
	if len(again.Backend) != 3 {
		t.Errorf("ApplyDefaults dropped backends: %v", again.Backend)
	}
}
