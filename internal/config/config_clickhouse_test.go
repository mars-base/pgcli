package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClickHouseApplyDefaults(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.ClickHouse = map[string]ClickHouseConfig{
		"analytics": {},
	}
	cfg.ApplyDefaults()

	cc := cfg.Addons.ClickHouse["analytics"]
	if cc.Name != "analytics" {
		t.Errorf("Name = %q, want analytics", cc.Name)
	}
	if cc.Version != DefaultClickHouseVersion {
		t.Errorf("Version = %q, want %q", cc.Version, DefaultClickHouseVersion)
	}
	if cc.ImageTag != DefaultClickHouseImageTag {
		t.Errorf("ImageTag = %q, want %q", cc.ImageTag, DefaultClickHouseImageTag)
	}
	if cc.Listen != "127.0.0.1" {
		t.Errorf("Listen = %q, want 127.0.0.1", cc.Listen)
	}
	if cc.AdminUser != "admin" {
		t.Errorf("AdminUser = %q, want admin", cc.AdminUser)
	}
	if cc.ContainerName != "pgcli-clickhouse-analytics" {
		t.Errorf("ContainerName = %q, want pgcli-clickhouse-analytics", cc.ContainerName)
	}
	dir := filepath.Join("/srv/pg", "addon", "clickhouse", "analytics")
	for field, got := range map[string]string{
		"ConfigPath": cc.ConfigPath,
		"DataDir":    cc.DataDir,
		"LogDir":     cc.LogDir,
		"BackupDir":  cc.BackupDir,
		"SecretPath": cc.SecretPath,
	} {
		if !strings.HasPrefix(got, dir+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a path below %q", field, got, dir)
		}
	}
	if cc.ConfigPath != filepath.Join(dir, "config.d", "zz-pgcli.xml") {
		t.Errorf("ConfigPath = %q", cc.ConfigPath)
	}
	if cc.SecretPath != filepath.Join(dir, "secrets", "admin_password") {
		t.Errorf("SecretPath = %q", cc.SecretPath)
	}
	if cc.HTTPPort == 0 || cc.TCPPort == 0 {
		t.Errorf("ports = %d/%d, want auto-assigned non-zero ports", cc.HTTPPort, cc.TCPPort)
	}
	if cc.HTTPPort == cc.TCPPort {
		t.Errorf("http_port and tcp_port both %d, want different ports", cc.HTTPPort)
	}
}

func TestClickHouseDefaultPathsFollowBaseDir(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/data/pgcli"
	cfg.Addons.ClickHouse = map[string]ClickHouseConfig{"a": {}}
	cfg.ApplyDefaults()

	got := cfg.Addons.ClickHouse["a"].DataDir
	want := filepath.Join("/data/pgcli", "addon", "clickhouse", "a", "data")
	if got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
}

func TestClickHousePortPoolsAreSeparate(t *testing.T) {
	cfg := Default()
	cfg.Addons.ClickHouse = map[string]ClickHouseConfig{
		"b": {},
		"a": {},
	}
	cfg.ApplyDefaults()

	// Ports already listening on the test host are skipped, so only the
	// pool separation and uniqueness are fixed, not the exact numbers.
	a := cfg.Addons.ClickHouse["a"]
	b := cfg.Addons.ClickHouse["b"]
	if a.HTTPPort < cfg.ClickHouseHTTPStartPort || b.HTTPPort < cfg.ClickHouseHTTPStartPort {
		t.Errorf("HTTP ports = %d/%d, want >= %d", a.HTTPPort, b.HTTPPort, cfg.ClickHouseHTTPStartPort)
	}
	if a.TCPPort < cfg.ClickHouseTCPStartPort || b.TCPPort < cfg.ClickHouseTCPStartPort {
		t.Errorf("TCP ports = %d/%d, want >= %d", a.TCPPort, b.TCPPort, cfg.ClickHouseTCPStartPort)
	}
	seen := map[int]bool{}
	for _, p := range []int{a.HTTPPort, a.TCPPort, b.HTTPPort, b.TCPPort} {
		if seen[p] {
			t.Errorf("port %d assigned twice", p)
		}
		seen[p] = true
	}
}

func TestClickHousePortPoolsShareAssignedSet(t *testing.T) {
	cfg := Default()
	cfg.ClickHouseHTTPStartPort = 19000
	cfg.ClickHouseTCPStartPort = 19005
	cfg.Addons.ClickHouse = map[string]ClickHouseConfig{
		// Explicit TCP port sits on the HTTP pool's first port; the HTTP
		// allocation must skip it.
		"a": {TCPPort: 19000},
		"b": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.ClickHouse["a"]
	b := cfg.Addons.ClickHouse["b"]
	if a.HTTPPort == 19000 {
		t.Errorf("a.HTTPPort = 19000, collides with a.TCPPort")
	}
	if b.TCPPort < 19005 {
		t.Errorf("b.TCPPort = %d, want >= 19005", b.TCPPort)
	}
	seen := map[int]bool{}
	for _, cc := range cfg.Addons.ClickHouse {
		for _, p := range []int{cc.HTTPPort, cc.TCPPort} {
			if seen[p] {
				t.Errorf("port %d assigned twice", p)
			}
			seen[p] = true
		}
	}
}

func TestClickHouseSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.ClickHouseHTTPStartPort = 18123
	cfg.ClickHouseTCPStartPort = 19090
	cfg.Addons.ClickHouse = map[string]ClickHouseConfig{
		"analytics": {
			AdminPassword: "secret",
			Settings:      map[string]string{"logger.level": "warning", "max_server_memory_usage": "1000000000"},
			Autostart:     true,
		},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ClickHouseHTTPStartPort != 18123 || got.ClickHouseTCPStartPort != 19090 {
		t.Errorf("start ports = %d/%d, want 18123/19090",
			got.ClickHouseHTTPStartPort, got.ClickHouseTCPStartPort)
	}
	cc, ok := got.Addons.ClickHouse["analytics"]
	if !ok {
		t.Fatal("analytics missing after round-trip")
	}
	if cc.AdminPassword != "secret" || !cc.Autostart || cc.Listen != "127.0.0.1" {
		t.Errorf("round-trip lost fields: %+v", cc)
	}
	if cc.Settings["logger.level"] != "warning" || cc.Settings["max_server_memory_usage"] != "1000000000" {
		t.Errorf("Settings = %v", cc.Settings)
	}
	want := cfg.Addons.ClickHouse["analytics"]
	if cc.HTTPPort != want.HTTPPort || cc.TCPPort != want.TCPPort {
		t.Errorf("ports = %d/%d, want %d/%d", cc.HTTPPort, cc.TCPPort, want.HTTPPort, want.TCPPort)
	}
}

func TestClickHouseValidate(t *testing.T) {
	valid := func() *Config {
		cfg := Default()
		cfg.Addons.ClickHouse = map[string]ClickHouseConfig{
			"analytics": {
				AdminUser:     "admin",
				AdminPassword: "secret",
			},
		}
		cfg.ApplyDefaults()
		return cfg
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{
			name: "valid",
		},
		{
			name: "invalid name",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.Name = "../evil"
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "must match",
		},
		{
			name: "relative data dir",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.DataDir = "data"
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "absolute path",
		},
		{
			name: "invalid listen",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.Listen = "localhost"
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "must be an IP address",
		},
		{
			name: "invalid admin user",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.AdminUser = "bad user"
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "admin_user",
		},
		{
			name: "empty password",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.AdminPassword = ""
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "admin_password must not be empty",
		},
		{
			name: "password with newline",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.AdminPassword = "a\nb"
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "control characters",
		},
		{
			name: "same http and tcp port",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.HTTPPort = 8123
				cc.TCPPort = 8123
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "must differ",
		},
		{
			name: "duplicate port across instances",
			mutate: func(c *Config) {
				first := c.Addons.ClickHouse["analytics"]
				second := first
				second.Name = "second"
				second.ContainerName = "pgcli-clickhouse-second"
				second.HTTPPort = first.HTTPPort
				c.Addons.ClickHouse["second"] = second
			},
			wantSub: "already used",
		},
		{
			name: "port out of range",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.HTTPPort = 70000
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "between 1 and 65535",
		},
		{
			name: "managed setting",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.Settings = map[string]string{"http_port": "8124"}
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "managed by pgcli",
		},
		{
			name: "image tag mismatch",
			mutate: func(c *Config) {
				cc := c.Addons.ClickHouse["analytics"]
				cc.ImageTag = "docker.io/clickhouse/clickhouse-server:latest"
				c.Addons.ClickHouse["analytics"] = cc
			},
			wantSub: "does not match version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			err := cfg.Validate()
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("Validate error = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestValidateClickHouseSettings(t *testing.T) {
	valid := []map[string]string{
		nil,
		{"max_server_memory_usage": "2000000000"},
		{"logger.level": "warning"},
		{"query_log.database": "system"},
		{"merge_tree.max_suspicious": "0"},
	}
	for _, settings := range valid {
		if err := ValidateClickHouseSettings(settings); err != nil {
			t.Errorf("ValidateClickHouseSettings(%v): %v", settings, err)
		}
	}

	bad := []struct {
		settings map[string]string
		wantSub  string
	}{
		{map[string]string{"not a key": "1"}, "dotted XML path"},
		{map[string]string{"http_port": "8124"}, "managed by pgcli"},
		{map[string]string{"listen_host": "0.0.0.0"}, "managed by pgcli"},
		{map[string]string{"storage_configuration.disks.backups.path": "/tmp"}, "managed by pgcli"},
		{map[string]string{"backups.allowed_path": "/"}, "managed by pgcli"},
		{map[string]string{"logger.log": "/tmp/x"}, "managed by pgcli"},
		{map[string]string{"user_directories.users_xml.path": "/x"}, "managed by pgcli"},
		{map[string]string{"logger.level": "verbose"}, "not a ClickHouse log level"},
		{map[string]string{"logger.level": "warning\n"}, "control characters"},
	}
	for _, tt := range bad {
		err := ValidateClickHouseSettings(tt.settings)
		if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("ValidateClickHouseSettings(%v) = %v, want %q", tt.settings, err, tt.wantSub)
		}
	}
}
