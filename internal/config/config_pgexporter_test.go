package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPGExporterDefaults(t *testing.T) {
	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"demo": {}}
	cfg.Addons.PGExporter = map[string]PGExporterConfig{
		"pe1": {TargetInstance: "demo"},
	}

	cfg.ApplyDefaults()

	pc, ok := cfg.Addons.PGExporter["pe1"]
	if !ok {
		t.Fatal("pgexporter addon missing after ApplyDefaults")
	}
	if pc.Name != "pe1" {
		t.Errorf("name = %q, want pe1", pc.Name)
	}
	if pc.ContainerName != "pgcli-pgexporter-pe1" {
		t.Errorf("container name = %q, want pgcli-pgexporter-pe1", pc.ContainerName)
	}
	if pc.ImageTag != DefaultPGExporterImageTag {
		t.Errorf("image tag = %q, want %q", pc.ImageTag, DefaultPGExporterImageTag)
	}
	// Same cross-host-Prometheus posture as node_exporter: default wide open.
	if pc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0", pc.Listen)
	}
	if pc.MonitorUser != "pgexporter" {
		t.Errorf("monitor user = %q, want pgexporter", pc.MonitorUser)
	}
	// Secrets are NEVER defaulted.
	if pc.MonitorPassword != "" || pc.MonitorDSN != "" {
		t.Errorf("ApplyDefaults must not invent credentials: pass=%q dsn=%q", pc.MonitorPassword, pc.MonitorDSN)
	}
	if pc.HostPort != DefaultPGExporterPort {
		t.Errorf("port = %d, want %d (pg_exporter's conventional port)", pc.HostPort, DefaultPGExporterPort)
	}
}

// pg_exporter owns its port pool (pgexporter_start_port, base 9630). Multiple
// instances count up; an explicit port is preserved and the cursor skips past.
func TestPGExporterPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.PGExporterStartPort = 39630 // high unused base keeps the live-listener scan out of expectations
	cfg.Instances = map[string]InstanceConfig{"demo": {}}
	cfg.Addons.PGExporter = map[string]PGExporterConfig{
		"b": {TargetInstance: "demo"},
		"a": {TargetInstance: "demo", HostPort: 39635}, // explicit, above the base
		"c": {TargetInstance: "demo"},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.PGExporter["a"]; got.HostPort != 39635 {
		t.Errorf("explicit port changed: %d", got.HostPort)
	}
	b := cfg.Addons.PGExporter["b"].HostPort
	c := cfg.Addons.PGExporter["c"].HostPort
	if b == c {
		t.Errorf("two instances share port %d", b)
	}
	for _, p := range []int{b, c} {
		if p == 39635 {
			t.Errorf("auto-assigned port collided with the explicit 39635")
		}
		if p < 39630 {
			t.Errorf("port %d below base 39630", p)
		}
	}
}

// The three-mode target contract: exactly ONE of target_instance / dsn /
// admin_dsn, the instance must exist, and both DSNs must be postgres URLs.
func TestPGExporterValidateTarget(t *testing.T) {
	newCfg := func(pc PGExporterConfig) *Config {
		cfg := Default()
		cfg.Instances = map[string]InstanceConfig{"demo": {}}
		cfg.Addons.PGExporter = map[string]PGExporterConfig{"pe1": pc}
		cfg.ApplyDefaults()
		return cfg
	}

	if err := newCfg(PGExporterConfig{}).Validate(); err == nil || !strings.Contains(err.Error(), "set exactly one of target_instance, dsn, or admin_dsn") {
		t.Errorf("missing target: want 'set exactly one of ...', got %v", err)
	}
	both := newCfg(PGExporterConfig{TargetInstance: "demo", DSN: "postgres://u:p@h:5432/db"})
	if err := both.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("instance+dsn: want mutual-exclusion error, got %v", err)
	}
	dsnAdmin := newCfg(PGExporterConfig{DSN: "postgres://u:p@h:5432/db", AdminDSN: "postgres://a:b@h:5432/db"})
	if err := dsnAdmin.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("dsn+admin_dsn: want mutual-exclusion error, got %v", err)
	}
	missing := newCfg(PGExporterConfig{TargetInstance: "ghost"})
	if err := missing.Validate(); err == nil || !strings.Contains(err.Error(), `not found in instances`) {
		t.Errorf("unknown instance: want not-found error, got %v", err)
	}
	badScheme := newCfg(PGExporterConfig{DSN: "mysql://u:p@h:3306/db"})
	if err := badScheme.Validate(); err == nil || !strings.Contains(err.Error(), "postgres://") {
		t.Errorf("bad scheme: want postgres-scheme error, got %v", err)
	}
	badAdminScheme := newCfg(PGExporterConfig{AdminDSN: "mysql://u:p@h:3306/db"})
	if err := badAdminScheme.Validate(); err == nil || !strings.Contains(err.Error(), "admin_dsn") {
		t.Errorf("bad admin_dsn scheme: want admin_dsn postgres-scheme error, got %v", err)
	}
	good := newCfg(PGExporterConfig{DSN: "postgresql://u:p@h:5432/db"})
	if err := good.Validate(); err != nil {
		t.Errorf("valid remote dsn rejected: %v", err)
	}
	goodAdmin := newCfg(PGExporterConfig{AdminDSN: "postgresql://h:5432/db"})
	if err := goodAdmin.Validate(); err != nil {
		t.Errorf("valid admin_dsn rejected: %v", err)
	}
}

func TestPGExporterSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"demo": {}}
	cfg.Addons.PGExporter = map[string]PGExporterConfig{
		"pe1": {
			ContainerName:   "pgcli-pgexporter-pe1",
			Name:            "pe1",
			ImageTag:        DefaultPGExporterImageTag,
			HostPort:        39630,
			Listen:          "10.0.0.5",
			TargetInstance:  "demo",
			MonitorDSN:      "postgres://pgexporter:pw@127.0.0.1:5432/demo?sslmode=disable",
			MonitorPassword: "pw",
			MonitorUser:     "pgexporter",
			ConfigFile:      "/etc/mycollectors.yml",
			ExtraArgs:       []string{"--auto-discovery=false", "--tag=env=prod"},
			Autostart:       true,
		},
		// A second instance in admin_dsn mode: the credential-FREE target address
		// must survive the round-trip so starts can re-substitute monitor creds.
		"pe2": {
			Name:     "pe2",
			HostPort: 39631,
			AdminDSN: "postgres://db.example.com:5432/app?sslmode=require",
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pc, ok := got.Addons.PGExporter["pe1"]
	if !ok {
		t.Fatal("pgexporter addon not persisted")
	}
	if pc.Listen != "10.0.0.5" {
		t.Errorf("listen not persisted: %q", pc.Listen)
	}
	if pc.HostPort != 39630 {
		t.Errorf("port not persisted: %d", pc.HostPort)
	}
	if pc.TargetInstance != "demo" {
		t.Errorf("target instance not persisted: %q", pc.TargetInstance)
	}
	if pc.MonitorDSN == "" || pc.MonitorPassword != "pw" {
		t.Errorf("monitor credentials not persisted: dsn=%q pass=%q", pc.MonitorDSN, pc.MonitorPassword)
	}
	if pc.ConfigFile != "/etc/mycollectors.yml" {
		t.Errorf("config file not persisted: %q", pc.ConfigFile)
	}
	if len(pc.ExtraArgs) != 2 || pc.ExtraArgs[0] != "--auto-discovery=false" {
		t.Errorf("extra args not persisted correctly: %v", pc.ExtraArgs)
	}
	if !pc.Autostart {
		t.Error("autostart not persisted")
	}

	pe2, ok := got.Addons.PGExporter["pe2"]
	if !ok {
		t.Fatal("admin_dsn addon not persisted")
	}
	if pe2.AdminDSN != "postgres://db.example.com:5432/app?sslmode=require" {
		t.Errorf("admin_dsn (credential-free target) not persisted verbatim: %q", pe2.AdminDSN)
	}

	// A second ApplyDefaults must not move the assigned port, override explicit
	// values, or invent credentials.
	got.Instances = map[string]InstanceConfig{"demo": {}}
	got.ApplyDefaults()
	again := got.Addons.PGExporter["pe1"]
	if again.HostPort != 39630 {
		t.Errorf("ApplyDefaults moved stored port: %d", again.HostPort)
	}
	if again.Listen != "10.0.0.5" {
		t.Errorf("ApplyDefaults overrode explicit listen: %q", again.Listen)
	}
	if again.MonitorPassword != "pw" {
		t.Errorf("ApplyDefaults changed the stored secret: %q", again.MonitorPassword)
	}
	if again.MonitorDSN == "" || !strings.Contains(again.MonitorDSN, "pgexporter") {
		t.Errorf("ApplyDefaults clobbered MonitorDSN: %q", again.MonitorDSN)
	}
}
