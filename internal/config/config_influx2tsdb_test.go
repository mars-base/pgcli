package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInflux2TSDBApplyDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.Influx2TSDB = map[string]Influx2TSDBConfig{
		"proxy": {},
	}
	cfg.ApplyDefaults()

	ic := cfg.Addons.Influx2TSDB["proxy"]
	if ic.Name != "proxy" {
		t.Errorf("Name = %q, want proxy", ic.Name)
	}
	if ic.Version != DefaultInflux2TSDBVersion {
		t.Errorf("Version = %q, want %q", ic.Version, DefaultInflux2TSDBVersion)
	}
	if ic.ImageTag != DefaultInflux2TSDBImageTag {
		t.Errorf("ImageTag = %q, want %q", ic.ImageTag, DefaultInflux2TSDBImageTag)
	}
	if ic.ContainerName != "pgcli-influx2tsdb-proxy" {
		t.Errorf("ContainerName = %q, want pgcli-influx2tsdb-proxy", ic.ContainerName)
	}
	if ic.PoolSize != 10 {
		t.Errorf("PoolSize = %d, want 10", ic.PoolSize)
	}
	if ic.Port < cfg.Influx2TSDBStartPort {
		t.Errorf("Port = %d, want >= %d", ic.Port, cfg.Influx2TSDBStartPort)
	}
}

func TestInflux2TSDBContainerNameFollowsNamespace(t *testing.T) {
	cfg := Default()
	cfg.Namespace = "dev"
	cfg.Addons.Influx2TSDB = map[string]Influx2TSDBConfig{"proxy": {}}
	cfg.ApplyDefaults()

	if got := cfg.Addons.Influx2TSDB["proxy"].ContainerName; got != "pgcli-influx2tsdb-dev-proxy" {
		t.Errorf("ContainerName = %q, want pgcli-influx2tsdb-dev-proxy", got)
	}
}

func TestInflux2TSDBImageTagForVersion(t *testing.T) {
	if tag, ok := Influx2TSDBImageTagForVersion(DefaultInflux2TSDBVersion); !ok || tag != DefaultInflux2TSDBImageTag {
		t.Errorf("pinned version -> %q, %v", tag, ok)
	}
	if _, ok := Influx2TSDBImageTagForVersion("9.9.9"); ok {
		t.Error("unknown version accepted")
	}
}

func TestInflux2TSDBPortPoolSkipsInfluxDBPorts(t *testing.T) {
	cfg := Default()
	cfg.Influx2TSDBStartPort = 9100
	cfg.Addons.InfluxDB = map[string]InfluxDBConfig{
		"native": {HostPort: 9100},
	}
	cfg.Addons.Influx2TSDB = map[string]Influx2TSDBConfig{
		"b": {},
		"a": {},
	}
	cfg.ApplyDefaults()

	a := cfg.Addons.Influx2TSDB["a"]
	b := cfg.Addons.Influx2TSDB["b"]
	if a.Port == 9100 || b.Port == 9100 {
		t.Errorf("proxy ports %d/%d collide with InfluxDB's 9100", a.Port, b.Port)
	}
	if a.Port == b.Port {
		t.Errorf("both proxies got port %d", a.Port)
	}
	// Deterministic: names sorted, so "a" gets the first free port.
	if a.Port != 9101 {
		t.Errorf("a.Port = %d, want 9101", a.Port)
	}
}

func TestInflux2TSDBSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Influx2TSDBStartPort = 9187
	cfg.Addons.Influx2TSDB = map[string]Influx2TSDBConfig{
		"proxy": {
			TargetInstance: "tsdb",
			Database:       "metrics",
			PoolSize:       20,
			Verbose:        true,
			Autostart:      true,
		},
	}
	cfg.ApplyDefaults()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Influx2TSDBStartPort != 9187 {
		t.Errorf("start port = %d, want 9187", got.Influx2TSDBStartPort)
	}
	ic, ok := got.Addons.Influx2TSDB["proxy"]
	if !ok {
		t.Fatal("proxy missing after round-trip")
	}
	if ic.TargetInstance != "tsdb" || ic.Database != "metrics" || ic.PoolSize != 20 || !ic.Verbose || !ic.Autostart {
		t.Errorf("round-trip lost fields: %+v", ic)
	}
	if ic.Port != cfg.Addons.Influx2TSDB["proxy"].Port {
		t.Errorf("Port = %d, want %d", ic.Port, cfg.Addons.Influx2TSDB["proxy"].Port)
	}
}

func TestInflux2TSDBValidate(t *testing.T) {
	base := func() *Config {
		cfg := Default()
		cfg.ApplyDefaults()
		cfg.Addons.Influx2TSDB = map[string]Influx2TSDBConfig{
			"proxy": {
				DSN: "postgres://u:p@db:5432/metrics?sslmode=disable",
			},
			"other": {
				DSN: "postgres://u:p@db:5432/other?sslmode=disable",
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
		{name: "valid with dsn"},
		{
			name: "valid with local target",
			mutate: func(c *Config) {
				c.Instances = map[string]InstanceConfig{"tsdb": {}}
				c.ApplyDefaults()
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.TargetInstance, ic.DSN = "tsdb", ""
				c.Addons.Influx2TSDB["proxy"] = ic
			},
		},
		{
			name: "invalid name",
			mutate: func(c *Config) {
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.Name = "../evil"
				c.Addons.Influx2TSDB["proxy"] = ic
			},
			wantSub: "must match",
		},
		{
			name: "unsupported version",
			mutate: func(c *Config) {
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.Version, ic.ImageTag = "9.9.9", "ghcr.io/mars-base/pgcli/pgcli-influx2tsdb:9.9.9"
				c.Addons.Influx2TSDB["proxy"] = ic
			},
			wantSub: "not supported",
		},
		{
			name: "image tag mismatch",
			mutate: func(c *Config) {
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.ImageTag = "ghcr.io/mars-base/pgcli/pgcli-influx2tsdb:latest"
				c.Addons.Influx2TSDB["proxy"] = ic
			},
			wantSub: "does not match version",
		},
		{
			name: "no target",
			mutate: func(c *Config) {
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.DSN = ""
				c.Addons.Influx2TSDB["proxy"] = ic
			},
			wantSub: "set target_instance or dsn",
		},
		{
			name: "both targets",
			mutate: func(c *Config) {
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.TargetInstance = "tsdb"
				c.Addons.Influx2TSDB["proxy"] = ic
			},
			wantSub: "mutually exclusive",
		},
		{
			name: "unknown target instance",
			mutate: func(c *Config) {
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.TargetInstance, ic.DSN = "nope", ""
				c.Addons.Influx2TSDB["proxy"] = ic
			},
			wantSub: "not found in instances",
		},
		{
			name: "non-postgres dsn",
			mutate: func(c *Config) {
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.DSN = "mysql://u:p@db:3306/metrics"
				c.Addons.Influx2TSDB["proxy"] = ic
			},
			wantSub: "postgres://",
		},
		{
			name: "port out of range",
			mutate: func(c *Config) {
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.Port = 70000
				c.Addons.Influx2TSDB["proxy"] = ic
			},
			wantSub: "between 1 and 65535",
		},
		{
			name: "duplicate port",
			mutate: func(c *Config) {
				proxy := c.Addons.Influx2TSDB["proxy"]
				other := c.Addons.Influx2TSDB["other"]
				other.Port = proxy.Port
				c.Addons.Influx2TSDB["other"] = other
			},
			wantSub: "already used by",
		},
		{
			name: "pool size too large",
			mutate: func(c *Config) {
				ic := c.Addons.Influx2TSDB["proxy"]
				ic.PoolSize = 1001
				c.Addons.Influx2TSDB["proxy"] = ic
			},
			wantSub: "pool_size must be between 1 and 1000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
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
