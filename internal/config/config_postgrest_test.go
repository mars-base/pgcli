package config

import (
	"path/filepath"
	"testing"
)

func TestPostgrestDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	if cfg.PostgrestStartPort != 3500 {
		t.Errorf("postgrest start port = %d, want 3500", cfg.PostgrestStartPort)
	}

	// Remote shape: top-level map keyed by --pg-name.
	cfg.Addons.Postgrest = map[string]PostgrestConfig{
		"api": {DSN: "postgres://auth:pw@10.0.0.1:5432/appdb"},
	}
	// Local shape: per-instance sidecar.
	cfg.Instances = map[string]InstanceConfig{
		"inst1": {Addons: AddonsConfig{
			Postgrest: &PostgrestConfig{DSN: "postgres://auth:pw@127.0.0.1:5432/appdb"},
		}},
	}

	cfg.ApplyDefaults()

	pr, ok := cfg.Addons.Postgrest["api"]
	if !ok {
		t.Fatal("postgrest addon missing after ApplyDefaults")
	}
	if pr.Name != "api" {
		t.Errorf("name = %q, want api", pr.Name)
	}
	if pr.ContainerName != "pgcli-postgrest-api" {
		t.Errorf("container name = %q, want pgcli-postgrest-api", pr.ContainerName)
	}
	if pr.ImageTag != DefaultPostgrestImageTag {
		t.Errorf("image tag = %q, want %q", pr.ImageTag, DefaultPostgrestImageTag)
	}
	if pr.Listen != "127.0.0.1" {
		t.Errorf("listen = %q, want 127.0.0.1", pr.Listen)
	}
	if pr.HostPort < cfg.PostgrestStartPort {
		t.Errorf("host port %d below base %d", pr.HostPort, cfg.PostgrestStartPort)
	}
	// Unset pool/schemas stay untouched: 0/"" means "PostgREST's own default".
	if pr.DbPool != 0 {
		t.Errorf("db pool = %d, want 0 (defer to PostgREST default)", pr.DbPool)
	}
	if pr.Schemas != "" {
		t.Errorf("schemas = %q, want empty (defer to PostgREST default)", pr.Schemas)
	}
	// The JWT secret is never defaulted — it only exists if --jwt-secret set it.
	if pr.JwtSecret != "" {
		t.Errorf("jwt secret = %q, want empty (never defaulted)", pr.JwtSecret)
	}

	local := cfg.Instances["inst1"].Addons.Postgrest
	if local == nil {
		t.Fatal("local postgrest sidecar missing after ApplyDefaults")
	}
	if local.ContainerName != "pgcli-postgrest-inst1" {
		t.Errorf("local container name = %q, want pgcli-postgrest-inst1", local.ContainerName)
	}
	if local.HostPort < cfg.PostgrestStartPort {
		t.Errorf("local host port %d below base %d", local.HostPort, cfg.PostgrestStartPort)
	}
	if local.HostPort == pr.HostPort {
		t.Errorf("local and remote postgrest share port %d", local.HostPort)
	}
}

// TestPostgrestPortRespectsExplicitPort pins the pool's collision rule within
// the config: an explicitly-set HostPort is respected verbatim, and auto-
// assignment never lands on a port another postgrest entry already claimed.
// (Live host-port avoidance goes through platform.GetUsedPorts, which scans the
// host and can't be injected here, so this only exercises the assignedPR set.)
func TestPostgrestPortRespectsExplicitPort(t *testing.T) {
	cfg := Default()
	cfg.Addons.Postgrest = map[string]PostgrestConfig{
		"fixed": {DSN: "d", HostPort: 3507},
		"auto":  {DSN: "d"},
	}

	cfg.ApplyDefaults()

	if pr := cfg.Addons.Postgrest["fixed"]; pr.HostPort != 3507 {
		t.Errorf("explicit port changed to %d, want 3507", pr.HostPort)
	}
	auto := cfg.Addons.Postgrest["auto"].HostPort
	if auto < cfg.PostgrestStartPort {
		t.Fatalf("auto port %d below base %d", auto, cfg.PostgrestStartPort)
	}
	if auto == 3507 {
		t.Errorf("auto port collided with explicit port 3507")
	}
}

func TestPostgrestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.Postgrest = map[string]PostgrestConfig{
		"api": {
			ContainerName: "pgcli-postgrest-api",
			Name:          "api",
			ImageTag:      DefaultPostgrestImageTag,
			HostPort:      3501,
			Listen:        "0.0.0.0",
			DSN:           "postgres://auth:pw@10.0.0.1:5432/appdb?sslmode=require",
			BackendHost:   "10.0.0.1:5432",
			DbPool:        3,
			Schemas:       "api",
			AnonRole:      "web_anon",
			JwtSecret:     "s3cr3t-jwt",
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
	pr, ok := got.Addons.Postgrest["api"]
	if !ok {
		t.Fatal("postgrest addon not persisted")
	}
	if pr.HostPort != 3501 || pr.Listen != "0.0.0.0" {
		t.Errorf("port/listen not persisted: %d %q", pr.HostPort, pr.Listen)
	}
	if pr.DSN != "postgres://auth:pw@10.0.0.1:5432/appdb?sslmode=require" {
		t.Errorf("dsn not persisted verbatim: %q", pr.DSN)
	}
	if pr.DbPool != 3 || pr.Schemas != "api" {
		t.Errorf("pool/schemas not persisted: %d %q", pr.DbPool, pr.Schemas)
	}
	if pr.AnonRole != "web_anon" {
		t.Errorf("anon role not persisted: %q", pr.AnonRole)
	}
	if pr.JwtSecret != "s3cr3t-jwt" {
		t.Errorf("jwt secret not persisted: %q", pr.JwtSecret)
	}
	if !pr.Autostart {
		t.Error("autostart not persisted")
	}
}
