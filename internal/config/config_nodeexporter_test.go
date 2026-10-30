package config

import (
	"path/filepath"
	"testing"
)

func TestNodeExporterDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.NodeExporter = map[string]NodeExporterConfig{
		"ne1": {},
	}

	cfg.ApplyDefaults()

	nc, ok := cfg.Addons.NodeExporter["ne1"]
	if !ok {
		t.Fatal("nodeexporter addon missing after ApplyDefaults")
	}
	if nc.Name != "ne1" {
		t.Errorf("name = %q, want ne1", nc.Name)
	}
	if nc.ContainerName != "pgcli-nodeexporter-ne1" {
		t.Errorf("container name = %q, want pgcli-nodeexporter-ne1", nc.ContainerName)
	}
	if nc.ImageTag != DefaultNodeExporterImageTag {
		t.Errorf("image tag = %q, want %q", nc.ImageTag, DefaultNodeExporterImageTag)
	}
	// Deliberate deviation from most web addons: node_exporter defaults to
	// 0.0.0.0 because Prometheus scraping it is usually on a DIFFERENT host.
	if nc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0 (cross-host Prometheus is the common topology)", nc.Listen)
	}
	if nc.HostPort < cfg.NodeExporterStartPort {
		t.Errorf("port %d below base %d", nc.HostPort, cfg.NodeExporterStartPort)
	}
	if nc.HostPort != DefaultNodeExporterPort {
		t.Errorf("port = %d, want %d (the conventional node_exporter port)", nc.HostPort, DefaultNodeExporterPort)
	}
}

// node_exporter owns its port pool (nodeexporter_start_port). The default base
// 9100 collides with no other pool base. Multiple instances count up from the
// base; an explicit port is preserved and the cursor skips past it.
func TestNodeExporterPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.NodeExporterStartPort = 39100 // high unused base keeps the live-listener scan out of expectations
	cfg.Addons.NodeExporter = map[string]NodeExporterConfig{
		"b": {},
		"a": {HostPort: 39105}, // explicit, above the base
		"c": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.NodeExporter["a"]; got.HostPort != 39105 {
		t.Errorf("explicit port changed: %d", got.HostPort)
	}
	// b and c fill from the base cursor; neither may land on the reserved 39105.
	b := cfg.Addons.NodeExporter["b"].HostPort
	c := cfg.Addons.NodeExporter["c"].HostPort
	if b == c {
		t.Errorf("two instances share port %d", b)
	}
	for _, p := range []int{b, c} {
		if p == 39105 {
			t.Errorf("auto-assigned port collided with the explicit 39105")
		}
		if p < 39100 {
			t.Errorf("port %d below base 39100", p)
		}
	}
}

func TestNodeExporterSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Addons.NodeExporter = map[string]NodeExporterConfig{
		"ne1": {
			ContainerName: "pgcli-nodeexporter-ne1",
			Name:          "ne1",
			ImageTag:      DefaultNodeExporterImageTag,
			HostPort:      39100,
			Listen:        "10.0.0.5",
			ExtraArgs:     []string{"--no-collector.cpufreq", "--collector.netdev"},
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
	nc, ok := got.Addons.NodeExporter["ne1"]
	if !ok {
		t.Fatal("nodeexporter addon not persisted")
	}
	if nc.Listen != "10.0.0.5" {
		t.Errorf("listen not persisted: %q", nc.Listen)
	}
	if nc.HostPort != 39100 {
		t.Errorf("port not persisted: %d", nc.HostPort)
	}
	if len(nc.ExtraArgs) != 2 || nc.ExtraArgs[0] != "--no-collector.cpufreq" {
		t.Errorf("extra args not persisted correctly: %v", nc.ExtraArgs)
	}
	if !nc.Autostart {
		t.Error("autostart not persisted")
	}
	if nc.ImageTag != DefaultNodeExporterImageTag {
		t.Errorf("image tag not persisted: %q", nc.ImageTag)
	}

	// A second ApplyDefaults must not move the assigned port or override any
	// explicitly-set field.
	got.ApplyDefaults()
	again := got.Addons.NodeExporter["ne1"]
	if again.HostPort != 39100 {
		t.Errorf("ApplyDefaults moved stored port: %d", again.HostPort)
	}
	if again.Listen != "10.0.0.5" {
		t.Errorf("ApplyDefaults overrode explicit listen: %q", again.Listen)
	}
	if len(again.ExtraArgs) != 2 {
		t.Errorf("ApplyDefaults dropped explicit extra args: %v", again.ExtraArgs)
	}
}
