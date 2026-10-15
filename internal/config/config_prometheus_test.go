package config

import (
	"path/filepath"
	"testing"
)

func TestPrometheusDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.Prometheus = map[string]PrometheusConfig{
		"p1": {},
	}

	cfg.ApplyDefaults()

	pc, ok := cfg.Addons.Prometheus["p1"]
	if !ok {
		t.Fatal("prometheus addon missing after ApplyDefaults")
	}
	if pc.Name != "p1" {
		t.Errorf("name = %q, want p1", pc.Name)
	}
	if pc.ContainerName != "pgcli-prometheus-p1" {
		t.Errorf("container name = %q, want pgcli-prometheus-p1", pc.ContainerName)
	}
	if pc.ImageTag != DefaultPrometheusImageTag {
		t.Errorf("image tag = %q, want %q", pc.ImageTag, DefaultPrometheusImageTag)
	}
	if pc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0 (a metrics backend is scraped/queried remotely, same rationale as Grafana — but with no password guarding it, see the addon docs)", pc.Listen)
	}
	if pc.Retention != "15d" {
		t.Errorf("retention = %q, want 15d", pc.Retention)
	}
	if pc.ScrapeInterval != "15s" {
		t.Errorf("scrape interval = %q, want 15s", pc.ScrapeInterval)
	}
	// Scrape is deliberately NOT defaulted: an empty list still renders the
	// always-present self-monitoring job (see renderPrometheusConfig).
	if len(pc.Scrape) != 0 {
		t.Errorf("scrape = %v, want empty (self job is always rendered regardless)", pc.Scrape)
	}
	// ConfFile is never defaulted — its presence is what switches into
	// operator-file mode and skips rendering entirely.
	if pc.ConfFile != "" {
		t.Errorf("conf file = %q, want empty (opt-in via --conf-file)", pc.ConfFile)
	}
	if pc.HostPort < cfg.PrometheusStartPort {
		t.Errorf("port %d below base %d", pc.HostPort, cfg.PrometheusStartPort)
	}
}

// Prometheus owns its port pool (prometheus_start_port). A high unused base
// keeps the live-listener scan out of the exact expectations, same convention
// as the grafana test. Cross-pool non-collision is also asserted: the base
// defaults (9090 for prometheus vs 3000 for grafana, 5050 for pgAdmin, 8080
// for nginx) are chosen to be disjoint.
func TestPrometheusPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.PrometheusStartPort = 39090
	cfg.Addons.Prometheus = map[string]PrometheusConfig{
		"b": {},
		"a": {HostPort: 39100}, // explicit, above the base
		"c": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.Prometheus["a"]; got.HostPort != 39100 {
		t.Errorf("explicit port changed: %d", got.HostPort)
	}
	if got := cfg.Addons.Prometheus["b"]; got.HostPort != 39101 {
		t.Errorf("b port = %d, want 39101 (cursor must skip the reserved port)", got.HostPort)
	}
	if got := cfg.Addons.Prometheus["c"]; got.HostPort != 39102 {
		t.Errorf("c port = %d, want 39102", got.HostPort)
	}

	// The prometheus pool must not collide with the grafana pool on the same host.
	cfg.Addons.Grafana = map[string]GrafanaConfig{"ui": {}}
	cfg.GrafanaStartPort = 39100
	cfg.ApplyDefaults()
	if p := cfg.Addons.Grafana["ui"]; p.HostPort == cfg.Addons.Prometheus["c"].HostPort {
		t.Errorf("grafana port %d collided with prometheus port", p.HostPort)
	}
}

func TestPrometheusSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Addons.Prometheus = map[string]PrometheusConfig{
		"p1": {
			ContainerName:  "pgcli-prometheus-p1",
			Name:           "p1",
			ImageTag:       DefaultPrometheusImageTag,
			HostPort:       39090,
			Listen:         "127.0.0.1",
			DataDir:        "/srv/prometheus-data",
			Retention:      "30d",
			Scrape:         []string{"postgres-exporter=127.0.0.1:9187", "node-exporter=10.0.0.5:9100,10.0.0.6:9100"},
			ScrapeInterval: "30s",
			ConfFile:       "/etc/prometheus/my.yml",
			Autostart:      true,
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pc, ok := got.Addons.Prometheus["p1"]
	if !ok {
		t.Fatal("prometheus addon not persisted")
	}
	if pc.Listen != "127.0.0.1" {
		t.Errorf("listen not persisted: %q", pc.Listen)
	}
	if pc.HostPort != 39090 {
		t.Errorf("port not persisted: %d", pc.HostPort)
	}
	if pc.DataDir != "/srv/prometheus-data" {
		t.Errorf("data dir not persisted: %q", pc.DataDir)
	}
	if pc.Retention != "30d" {
		t.Errorf("retention not persisted: %q", pc.Retention)
	}
	if len(pc.Scrape) != 2 || pc.Scrape[0] != "postgres-exporter=127.0.0.1:9187" {
		t.Errorf("scrape targets not persisted correctly: %v", pc.Scrape)
	}
	if pc.ScrapeInterval != "30s" {
		t.Errorf("scrape interval not persisted: %q", pc.ScrapeInterval)
	}
	if pc.ConfFile != "/etc/prometheus/my.yml" {
		t.Errorf("conf file not persisted: %q", pc.ConfFile)
	}
	if !pc.Autostart {
		t.Error("autostart not persisted")
	}
	if pc.ImageTag != DefaultPrometheusImageTag {
		t.Errorf("image tag not persisted: %q", pc.ImageTag)
	}

	// A second ApplyDefaults must not move the assigned port or touch any
	// explicitly-set field — every one is already populated above.
	got.ApplyDefaults()
	again := got.Addons.Prometheus["p1"]
	if again.HostPort != 39090 {
		t.Errorf("ApplyDefaults moved stored port: %d", again.HostPort)
	}
	if again.Listen != "127.0.0.1" {
		t.Errorf("ApplyDefaults overrode explicit listen: %q", again.Listen)
	}
	if again.DataDir != "/srv/prometheus-data" {
		t.Errorf("ApplyDefaults dropped explicit data dir: %q", again.DataDir)
	}
	if again.Retention != "30d" {
		t.Errorf("ApplyDefaults overrode explicit retention: %q", again.Retention)
	}
	if again.ScrapeInterval != "30s" {
		t.Errorf("ApplyDefaults overrode explicit scrape interval: %q", again.ScrapeInterval)
	}
	if again.ConfFile != "/etc/prometheus/my.yml" {
		t.Errorf("ApplyDefaults dropped explicit conf file: %q", again.ConfFile)
	}
}
