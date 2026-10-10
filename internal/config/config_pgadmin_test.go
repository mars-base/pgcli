package config

import (
	"path/filepath"
	"testing"
)

func TestPgAdminDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.PgAdmin = map[string]PgAdminConfig{
		"ui": {},
	}

	cfg.ApplyDefaults()

	ac, ok := cfg.Addons.PgAdmin["ui"]
	if !ok {
		t.Fatal("pgadmin addon missing after ApplyDefaults")
	}
	if ac.Name != "ui" {
		t.Errorf("name = %q, want ui", ac.Name)
	}
	if ac.ContainerName != "pgcli-pgadmin-ui" {
		t.Errorf("container name = %q, want pgcli-pgadmin-ui", ac.ContainerName)
	}
	if ac.ImageTag != DefaultPgAdminImageTag {
		t.Errorf("image tag = %q, want %q", ac.ImageTag, DefaultPgAdminImageTag)
	}
	// Loopback by default: a web admin UI must not bind 0.0.0.0 unless asked
	// (the opposite default of predixy, whose only gate is AUTH).
	if ac.Listen != "127.0.0.1" {
		t.Errorf("listen = %q, want 127.0.0.1", ac.Listen)
	}
	if ac.Email != "admin@pgcli.lan" {
		t.Errorf("email = %q, want admin@pgcli.lan", ac.Email)
	}
	// Password, DSN and ServerName are deliberately not defaulted: a hardcoded
	// web login credential would be a shipped secret, and the CLI install
	// handler generates the password when empty.
	if ac.Password != "" {
		t.Errorf("password = %q, want empty (generated at install)", ac.Password)
	}
	if ac.DSN != "" {
		t.Errorf("dsn = %q, want empty (operator-supplied seed)", ac.DSN)
	}
	if ac.ServerName != "" {
		t.Errorf("server name = %q, want empty", ac.ServerName)
	}
	if ac.HostPort < cfg.PgAdminStartPort {
		t.Errorf("port %d below base %d", ac.HostPort, cfg.PgAdminStartPort)
	}
}

// pgAdmin owns its port pool (pgadmin_start_port). A high unused base keeps the
// live-listener scan out of the exact expectations, same convention as
// TestPredixyPortAssignment.
func TestPgAdminPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.PgAdminStartPort = 38501
	cfg.Addons.PgAdmin = map[string]PgAdminConfig{
		"b": {},
		"a": {HostPort: 39100}, // explicit, above the base
		"c": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.PgAdmin["a"]; got.HostPort != 39100 {
		t.Errorf("explicit port changed: %d", got.HostPort)
	}
	if got := cfg.Addons.PgAdmin["b"]; got.HostPort != 39101 {
		t.Errorf("b port = %d, want 39101 (cursor must skip the reserved port)", got.HostPort)
	}
	if got := cfg.Addons.PgAdmin["c"]; got.HostPort != 39102 {
		t.Errorf("c port = %d, want 39102", got.HostPort)
	}

	// The pgadmin pool must not collide with the predixy pool on the same host.
	cfg.Addons.Predixy = map[string]PredixyConfig{"proxy": {}}
	cfg.PredixyStartPort = 37617
	cfg.ApplyDefaults()
	if p := cfg.Addons.Predixy["proxy"]; p.Port == cfg.Addons.PgAdmin["c"].HostPort {
		t.Errorf("predixy port %d collided with pgadmin port", p.Port)
	}
}

func TestPgAdminSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Addons.PgAdmin = map[string]PgAdminConfig{
		"ui": {
			ContainerName: "pgcli-pgadmin-ui",
			Name:          "ui",
			ImageTag:      DefaultPgAdminImageTag,
			HostPort:      38501,
			Listen:        "0.0.0.0",
			Email:         "dba@example.com",
			Password:      "web-login-pw",
			DSN:           "postgres://app:pw@dbhost:5432/appdb",
			ServerName:    "app",
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
	ac, ok := got.Addons.PgAdmin["ui"]
	if !ok {
		t.Fatal("pgadmin addon not persisted")
	}
	if ac.Listen != "0.0.0.0" {
		t.Errorf("listen not persisted: %q", ac.Listen)
	}
	if ac.HostPort != 38501 {
		t.Errorf("port not persisted: %d", ac.HostPort)
	}
	if ac.Email != "dba@example.com" {
		t.Errorf("email not persisted: %q", ac.Email)
	}
	if ac.Password != "web-login-pw" {
		t.Errorf("password not persisted: %q", ac.Password)
	}
	if ac.DSN != "postgres://app:pw@dbhost:5432/appdb" {
		t.Errorf("dsn not persisted: %q", ac.DSN)
	}
	if ac.ServerName != "app" {
		t.Errorf("server name not persisted: %q", ac.ServerName)
	}
	if !ac.Autostart {
		t.Error("autostart not persisted")
	}
	if ac.ImageTag != DefaultPgAdminImageTag {
		t.Errorf("image tag not persisted: %q", ac.ImageTag)
	}

	// A second ApplyDefaults must not move the assigned port, rewrite the
	// login credentials, or drop the seed DSN.
	got.ApplyDefaults()
	again := got.Addons.PgAdmin["ui"]
	if again.HostPort != 38501 || again.Email != "dba@example.com" || again.Password != "web-login-pw" {
		t.Errorf("ApplyDefaults mutated stored values: port=%d email=%q", again.HostPort, again.Email)
	}
	if again.DSN == "" || again.ServerName != "app" {
		t.Errorf("ApplyDefaults dropped the seed: dsn=%q server=%q", again.DSN, again.ServerName)
	}
}
