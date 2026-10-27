package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInfluxDBApplyDefaults(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.InfluxDB = map[string]InfluxDBConfig{
		"metrics": {},
	}
	cfg.ApplyDefaults()

	ic := cfg.Addons.InfluxDB["metrics"]
	if ic.Name != "metrics" {
		t.Errorf("Name = %q, want metrics", ic.Name)
	}
	if ic.Version != DefaultInfluxDBVersion {
		t.Errorf("Version = %q, want %q", ic.Version, DefaultInfluxDBVersion)
	}
	if ic.Listen != "0.0.0.0" {
		t.Errorf("Listen = %q, want 0.0.0.0", ic.Listen)
	}
	if ic.AdminUser != "admin" {
		t.Errorf("AdminUser = %q, want admin", ic.AdminUser)
	}
	if ic.ContainerName != "pgcli-influxdb-metrics" {
		t.Errorf("ContainerName = %q, want pgcli-influxdb-metrics", ic.ContainerName)
	}
	if ic.ImageTag != DefaultInfluxDBImageTag {
		t.Errorf("ImageTag = %q, want %q", ic.ImageTag, DefaultInfluxDBImageTag)
	}
	dir := filepath.Join("/srv/pg", "addon", "influxdb", "metrics")
	for field, got := range map[string]string{
		"ConfigPath": ic.ConfigPath,
		"DataDir":    ic.DataDir,
	} {
		if !strings.HasPrefix(got, dir+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a path below %q", field, got, dir)
		}
	}
	if ic.HostPort == 0 || ic.RPCPort == 0 {
		t.Errorf("ports = %d/%d, want auto-assigned non-zero ports", ic.HostPort, ic.RPCPort)
	}
	if ic.HostPort == ic.RPCPort {
		t.Errorf("host_port and rpc_port both %d, want different ports", ic.HostPort)
	}
}

func TestInfluxDBDefaultPathsFollowBaseDir(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/data/pgcli"
	cfg.Addons.InfluxDB = map[string]InfluxDBConfig{"a": {}}
	cfg.ApplyDefaults()

	got := cfg.Addons.InfluxDB["a"].DataDir
	want := filepath.Join("/data/pgcli", "addon", "influxdb", "a", "data")
	if got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
}

func TestInfluxDBPortPoolsAreSeparate(t *testing.T) {
	cfg := Default()
	cfg.Addons.InfluxDB = map[string]InfluxDBConfig{
		"b": {},
		"a": {},
	}
	cfg.ApplyDefaults()

	// Ports already listening on the test host are skipped, so only the
	// pool separation and uniqueness are fixed, not the exact numbers.
	a := cfg.Addons.InfluxDB["a"]
	b := cfg.Addons.InfluxDB["b"]
	if a.HostPort < cfg.InfluxDBStartPort || b.HostPort < cfg.InfluxDBStartPort {
		t.Errorf("HTTP ports = %d/%d, want >= %d", a.HostPort, b.HostPort, cfg.InfluxDBStartPort)
	}
	if a.RPCPort < 8088 || b.RPCPort < 8088 {
		t.Errorf("RPC ports = %d/%d, want >= 8088", a.RPCPort, b.RPCPort)
	}
	seen := map[int]bool{}
	for _, p := range []int{a.HostPort, a.RPCPort, b.HostPort, b.RPCPort} {
		if seen[p] {
			t.Errorf("port %d assigned twice", p)
		}
		seen[p] = true
	}
}

func TestInfluxDBPortPoolsShareAssignedSet(t *testing.T) {
	cfg := Default()
	cfg.InfluxDBStartPort = 9000
	cfg.Addons.InfluxDB = map[string]InfluxDBConfig{
		// Explicit RPC port sits on the HTTP pool's first port; the HTTP
		// allocation must skip it.
		"a": {RPCPort: 9000},
		"b": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.InfluxDB["a"]
	b := cfg.Addons.InfluxDB["b"]
	if a.HostPort == 9000 {
		t.Errorf("a.HostPort = 9000, collides with a.RPCPort")
	}
	if b.RPCPort == a.HostPort || b.RPCPort == a.RPCPort {
		t.Errorf("b.RPCPort %d collides with a (%d/%d)", b.RPCPort, a.HostPort, a.RPCPort)
	}
	seen := map[int]bool{}
	for _, ic := range cfg.Addons.InfluxDB {
		for _, p := range []int{ic.HostPort, ic.RPCPort} {
			if seen[p] {
				t.Errorf("port %d assigned twice", p)
			}
			seen[p] = true
		}
	}
}

func TestInfluxDBSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.InfluxDBStartPort = 9086
	cfg.Addons.InfluxDB = map[string]InfluxDBConfig{
		"metrics": {
			AdminPassword: "secret",
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
	if got.InfluxDBStartPort != 9086 {
		t.Errorf("start port = %d, want 9086", got.InfluxDBStartPort)
	}
	ic, ok := got.Addons.InfluxDB["metrics"]
	if !ok {
		t.Fatal("metrics missing after round-trip")
	}
	if ic.AdminPassword != "secret" || !ic.Autostart || ic.Listen != "0.0.0.0" {
		t.Errorf("round-trip lost fields: %+v", ic)
	}
	if ic.HostPort != cfg.Addons.InfluxDB["metrics"].HostPort || ic.RPCPort != cfg.Addons.InfluxDB["metrics"].RPCPort {
		t.Errorf("ports = %d/%d, want %d/%d", ic.HostPort, ic.RPCPort,
			cfg.Addons.InfluxDB["metrics"].HostPort, cfg.Addons.InfluxDB["metrics"].RPCPort)
	}
}

func TestInfluxDBValidate(t *testing.T) {
	valid := func() *Config {
		cfg := Default()
		cfg.Addons.InfluxDB = map[string]InfluxDBConfig{
			"metrics": {
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
				ic := c.Addons.InfluxDB["metrics"]
				ic.Name = "../evil"
				c.Addons.InfluxDB["metrics"] = ic
			},
			wantSub: "must match",
		},
		{
			name: "relative data dir",
			mutate: func(c *Config) {
				ic := c.Addons.InfluxDB["metrics"]
				ic.DataDir = "data"
				c.Addons.InfluxDB["metrics"] = ic
			},
			wantSub: "absolute path",
		},
		{
			name: "empty admin user",
			mutate: func(c *Config) {
				ic := c.Addons.InfluxDB["metrics"]
				ic.AdminUser = ""
				c.Addons.InfluxDB["metrics"] = ic
			},
			wantSub: "admin_user must not be empty",
		},
		{
			name: "invalid admin user",
			mutate: func(c *Config) {
				ic := c.Addons.InfluxDB["metrics"]
				ic.AdminUser = "bad user"
				c.Addons.InfluxDB["metrics"] = ic
			},
			wantSub: "admin_user",
		},
		{
			name: "empty password",
			mutate: func(c *Config) {
				ic := c.Addons.InfluxDB["metrics"]
				ic.AdminPassword = ""
				c.Addons.InfluxDB["metrics"] = ic
			},
			wantSub: "admin_password must not be empty",
		},
		{
			name: "same http and rpc port",
			mutate: func(c *Config) {
				ic := c.Addons.InfluxDB["metrics"]
				ic.HostPort = 8086
				ic.RPCPort = 8086
				c.Addons.InfluxDB["metrics"] = ic
			},
			wantSub: "must differ",
		},
		{
			name: "duplicate port across instances",
			mutate: func(c *Config) {
				first := c.Addons.InfluxDB["metrics"]
				c.Addons.InfluxDB["second"] = InfluxDBConfig{
					Name: "second", Version: DefaultInfluxDBVersion, ImageTag: DefaultInfluxDBImageTag,
					AdminUser: "admin", AdminPassword: "secret",
					ConfigPath: first.ConfigPath, DataDir: first.DataDir,
					HostPort: first.HostPort, RPCPort: first.RPCPort + 1,
				}
			},
			wantSub: "already used",
		},
		{
			name: "port out of range",
			mutate: func(c *Config) {
				ic := c.Addons.InfluxDB["metrics"]
				ic.HostPort = 70000
				c.Addons.InfluxDB["metrics"] = ic
			},
			wantSub: "between 1 and 65535",
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

func TestInfluxDBExtraConfig(t *testing.T) {
	valid := []string{
		"query-log-enabled=true",
		`data.index-version="tsi1"`,
		"data.cache-max-memory-size=1000000000",
		"[logging] level=warn",
	}
	// The bracket form above is NOT supported — it must be rejected cleanly.
	if err := ValidateInfluxDBExtraConfig(valid[:3]); err != nil {
		t.Fatalf("ValidateInfluxDBExtraConfig: %v", err)
	}
	if err := ValidateInfluxDBExtraConfig(valid[3:]); err == nil {
		t.Fatal("bracket section syntax should be rejected")
	}

	bad := []struct {
		entries []string
		wantSub string
	}{
		{[]string{"not-an-assignment"}, "must be KEY=VALUE"},
		{[]string{"data.index-version="}, "empty value"},
		{[]string{"=true"}, "empty key"},
		{[]string{"a.b.c=1"}, "too many dots"},
		{[]string{"bad section.k=v"}, "invalid section"},
		{[]string{"data.bad key=v"}, "invalid key"},
		{[]string{`http.auth-enabled=false`}, "pgcli-managed"},
		{[]string{`data.dir="/tmp/x"`}, "pgcli-managed"},
		{[]string{"data.index-version=1", `data.index-version="tsi1"`}, "more than once"},
	}
	for _, tt := range bad {
		err := ValidateInfluxDBExtraConfig(tt.entries)
		if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("ValidateInfluxDBExtraConfig(%v) = %v, want %q", tt.entries, err, tt.wantSub)
		}
	}
}

func TestInfluxDBExtraConfigRoundTrip(t *testing.T) {
	cfg := Default()
	if cfg.Addons.InfluxDB == nil {
		cfg.Addons.InfluxDB = make(map[string]InfluxDBConfig)
	}
	cfg.Addons.InfluxDB["metrics"] = InfluxDBConfig{
		Name: "metrics", AdminUser: "admin", AdminPassword: "secret",
		Listen: "127.0.0.1", HostPort: 18086, RPCPort: 18088,
		ExtraConfig: []string{`data.index-version="tsi1"`},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := loaded.Addons.InfluxDB["metrics"].ExtraConfig
	if len(got) != 1 || got[0] != `data.index-version="tsi1"` {
		t.Fatalf("ExtraConfig = %v", got)
	}
}
