package config

import (
	"path/filepath"
	"testing"
)

func TestGrafanaDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.Grafana = map[string]GrafanaConfig{
		"ui": {},
	}

	cfg.ApplyDefaults()

	gc, ok := cfg.Addons.Grafana["ui"]
	if !ok {
		t.Fatal("grafana addon missing after ApplyDefaults")
	}
	if gc.Name != "ui" {
		t.Errorf("name = %q, want ui", gc.Name)
	}
	if gc.ContainerName != "pgcli-grafana-ui" {
		t.Errorf("container name = %q, want pgcli-grafana-ui", gc.ContainerName)
	}
	if gc.ImageTag != DefaultGrafanaImageTag {
		t.Errorf("image tag = %q, want %q", gc.ImageTag, DefaultGrafanaImageTag)
	}
	if gc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0 (a dashboard is meant to be viewed from other machines; containers run on the host network)", gc.Listen)
	}
	if gc.AdminUser != "admin" {
		t.Errorf("admin user = %q, want admin", gc.AdminUser)
	}
	// Password is deliberately NOT defaulted: ApplyDefaults must stay
	// deterministic and a login password must never be a hardcoded literal.
	// The install handler generates one (same rule as pgAdmin).
	if gc.Password != "" {
		t.Errorf("password = %q, want empty (install-time generated)", gc.Password)
	}
	if gc.HostPort < cfg.GrafanaStartPort {
		t.Errorf("port %d below base %d", gc.HostPort, cfg.GrafanaStartPort)
	}
}

// Grafana owns its port pool (grafana_start_port). A high unused base keeps
// the live-listener scan out of the exact expectations, same convention as the
// predixy test. Cross-pool non-collision is also asserted: the base defaults
// (3000 vs pgAdmin's 5050) are chosen to be disjoint.
func TestGrafanaPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.GrafanaStartPort = 33000
	cfg.Addons.Grafana = map[string]GrafanaConfig{
		"b": {},
		"a": {HostPort: 33100}, // explicit, above the base
		"c": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.Grafana["a"]; got.HostPort != 33100 {
		t.Errorf("explicit port changed: %d", got.HostPort)
	}
	if got := cfg.Addons.Grafana["b"]; got.HostPort != 33101 {
		t.Errorf("b port = %d, want 33101 (cursor must skip the reserved port)", got.HostPort)
	}
	if got := cfg.Addons.Grafana["c"]; got.HostPort != 33102 {
		t.Errorf("c port = %d, want 33102", got.HostPort)
	}

	// The grafana pool must not collide with the pgAdmin pool on the same host.
	cfg.Addons.PgAdmin = map[string]PgAdminConfig{"ui": {}}
	cfg.PgAdminStartPort = 35050
	cfg.ApplyDefaults()
	if p := cfg.Addons.PgAdmin["ui"]; p.HostPort == cfg.Addons.Grafana["c"].HostPort {
		t.Errorf("pgadmin port %d collided with grafana port", p.HostPort)
	}
}

func TestGrafanaSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Addons.Grafana = map[string]GrafanaConfig{
		"ui": {
			ContainerName: "pgcli-grafana-ui",
			Name:          "ui",
			ImageTag:      DefaultGrafanaImageTag,
			HostPort:      33000,
			Listen:        "127.0.0.1",
			AdminUser:     "ops",
			Password:      "web-login-pw",
			DataDir:       "/srv/grafana-data",
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
	gc, ok := got.Addons.Grafana["ui"]
	if !ok {
		t.Fatal("grafana addon not persisted")
	}
	if gc.Listen != "127.0.0.1" {
		t.Errorf("listen not persisted: %q", gc.Listen)
	}
	if gc.HostPort != 33000 {
		t.Errorf("port not persisted: %d", gc.HostPort)
	}
	if gc.AdminUser != "ops" {
		t.Errorf("admin user not persisted: %q", gc.AdminUser)
	}
	if gc.Password != "web-login-pw" {
		t.Errorf("password not persisted: %q", gc.Password)
	}
	if gc.DataDir != "/srv/grafana-data" {
		t.Errorf("data dir not persisted: %q", gc.DataDir)
	}
	if !gc.Autostart {
		t.Error("autostart not persisted")
	}
	if gc.ImageTag != DefaultGrafanaImageTag {
		t.Errorf("image tag not persisted: %q", gc.ImageTag)
	}

	// A second ApplyDefaults must not move the assigned port, rewrite the
	// admin user, or touch the stored password — every field is already set.
	got.ApplyDefaults()
	again := got.Addons.Grafana["ui"]
	if again.HostPort != 33000 || again.AdminUser != "ops" || again.Password != "web-login-pw" {
		t.Errorf("ApplyDefaults mutated stored values: port=%d admin=%q", again.HostPort, again.AdminUser)
	}
	if again.Listen != "127.0.0.1" {
		t.Errorf("ApplyDefaults overrode explicit listen: %q", again.Listen)
	}
	if again.DataDir != "/srv/grafana-data" {
		t.Errorf("ApplyDefaults dropped explicit data dir: %q", again.DataDir)
	}
}
