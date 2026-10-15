package config

import (
	"path/filepath"
	"testing"
)

func TestAlertmanagerDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.Alertmanager = map[string]AlertmanagerConfig{
		"am1": {},
	}

	cfg.ApplyDefaults()

	ac, ok := cfg.Addons.Alertmanager["am1"]
	if !ok {
		t.Fatal("alertmanager addon missing after ApplyDefaults")
	}
	if ac.Name != "am1" {
		t.Errorf("name = %q, want am1", ac.Name)
	}
	if ac.ContainerName != "pgcli-alertmanager-am1" {
		t.Errorf("container name = %q, want pgcli-alertmanager-am1", ac.ContainerName)
	}
	if ac.ImageTag != DefaultAlertmanagerImageTag {
		t.Errorf("image tag = %q, want %q", ac.ImageTag, DefaultAlertmanagerImageTag)
	}
	if ac.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0 (an alerting endpoint is queried/POSTed to remotely, same rationale as Prometheus — but with a sharper edge: no password, and its API can create silences, see the addon docs)", ac.Listen)
	}
	// Webhook is deliberately NOT defaulted: an empty list still renders a
	// legal, fully-functional config — a single route pointing at a name-only
	// "no-op" receiver (see renderAlertmanagerConfig).
	if len(ac.Webhook) != 0 {
		t.Errorf("webhook = %v, want empty (no-op receiver is the default rendered shape)", ac.Webhook)
	}
	// ConfFile is never defaulted — its presence is what switches into
	// operator-file mode and skips rendering entirely.
	if ac.ConfFile != "" {
		t.Errorf("conf file = %q, want empty (opt-in via --conf-file)", ac.ConfFile)
	}
	if ac.HostPort < cfg.AlertmanagerStartPort {
		t.Errorf("port %d below base %d", ac.HostPort, cfg.AlertmanagerStartPort)
	}
}

// Alertmanager owns its port pool (alertmanager_start_port). A high unused base
// keeps the live-listener scan out of the exact expectations, same convention
// as the prometheus test. Cross-pool non-collision is also asserted: the base
// defaults (9093 for alertmanager vs 9090 for prometheus, 3000 for grafana,
// 5050 for pgAdmin, 8080 for nginx) are chosen to be disjoint — and Alertmanager's
// own 9094 cluster port never enters the pool at all, since a single instance
// disables it via a CLI flag (see alertmanagerArgsAndEnv), not via port
// allocation.
func TestAlertmanagerPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.AlertmanagerStartPort = 39093
	cfg.Addons.Alertmanager = map[string]AlertmanagerConfig{
		"b": {},
		"a": {HostPort: 39100}, // explicit, above the base
		"c": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.Alertmanager["a"]; got.HostPort != 39100 {
		t.Errorf("explicit port changed: %d", got.HostPort)
	}
	if got := cfg.Addons.Alertmanager["b"]; got.HostPort != 39101 {
		t.Errorf("b port = %d, want 39101 (cursor must skip the reserved port)", got.HostPort)
	}
	if got := cfg.Addons.Alertmanager["c"]; got.HostPort != 39102 {
		t.Errorf("c port = %d, want 39102", got.HostPort)
	}

	// The alertmanager pool must not collide with the prometheus pool on the
	// same host. Each pool's cursor is independent (built only from its own
	// addons' assigned ports), so isolation rests on the default bases being
	// disjoint (9093 vs 9090) — this pins that the two pools, given distinct
	// bases, land on distinct ports even for the first instance each.
	cfg.Addons.Prometheus = map[string]PrometheusConfig{"p1": {}}
	cfg.PrometheusStartPort = 39200
	cfg.ApplyDefaults()
	if p := cfg.Addons.Prometheus["p1"]; p.HostPort == cfg.Addons.Alertmanager["c"].HostPort {
		t.Errorf("prometheus port %d collided with alertmanager port", p.HostPort)
	}
}

func TestAlertmanagerSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Addons.Alertmanager = map[string]AlertmanagerConfig{
		"am1": {
			ContainerName: "pgcli-alertmanager-am1",
			Name:          "am1",
			ImageTag:      DefaultAlertmanagerImageTag,
			HostPort:      39093,
			Listen:        "127.0.0.1",
			DataDir:       "/srv/alertmanager-data",
			Webhook:       []string{"http://127.0.0.1:5001/", "https://hooks.example.com/alert?token=abc"},
			ConfFile:      "/etc/alertmanager/my.yml",
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
	ac, ok := got.Addons.Alertmanager["am1"]
	if !ok {
		t.Fatal("alertmanager addon not persisted")
	}
	if ac.Listen != "127.0.0.1" {
		t.Errorf("listen not persisted: %q", ac.Listen)
	}
	if ac.HostPort != 39093 {
		t.Errorf("port not persisted: %d", ac.HostPort)
	}
	if ac.DataDir != "/srv/alertmanager-data" {
		t.Errorf("data dir not persisted: %q", ac.DataDir)
	}
	if len(ac.Webhook) != 2 || ac.Webhook[0] != "http://127.0.0.1:5001/" {
		t.Errorf("webhook URLs not persisted correctly: %v", ac.Webhook)
	}
	if ac.ConfFile != "/etc/alertmanager/my.yml" {
		t.Errorf("conf file not persisted: %q", ac.ConfFile)
	}
	if !ac.Autostart {
		t.Error("autostart not persisted")
	}
	if ac.ImageTag != DefaultAlertmanagerImageTag {
		t.Errorf("image tag not persisted: %q", ac.ImageTag)
	}

	// A second ApplyDefaults must not move the assigned port or touch any
	// explicitly-set field — every one is already populated above.
	got.ApplyDefaults()
	again := got.Addons.Alertmanager["am1"]
	if again.HostPort != 39093 {
		t.Errorf("ApplyDefaults moved stored port: %d", again.HostPort)
	}
	if again.Listen != "127.0.0.1" {
		t.Errorf("ApplyDefaults overrode explicit listen: %q", again.Listen)
	}
	if again.DataDir != "/srv/alertmanager-data" {
		t.Errorf("ApplyDefaults dropped explicit data dir: %q", again.DataDir)
	}
	if len(again.Webhook) != 2 {
		t.Errorf("ApplyDefaults dropped explicit webhook list: %v", again.Webhook)
	}
	if again.ConfFile != "/etc/alertmanager/my.yml" {
		t.Errorf("ApplyDefaults dropped explicit conf file: %q", again.ConfFile)
	}
}
