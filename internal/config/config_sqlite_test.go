package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteApplyDefaults(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = "/srv/pg"
	cfg.Addons.SQLite = map[string]SQLiteConfig{
		"appdb": {},
	}
	cfg.ApplyDefaults()

	db := cfg.Addons.SQLite["appdb"]
	if db.Name != "appdb" {
		t.Errorf("Name = %q, want appdb", db.Name)
	}
	wantPath := filepath.Join("/srv/pg", "sqlite", "appdb", "appdb.db")
	if db.Path != wantPath {
		t.Errorf("Path = %q, want %q", db.Path, wantPath)
	}
	if db.Replication != nil {
		t.Errorf("Replication = %+v, want nil", db.Replication)
	}
}

func TestSQLiteApplyDefaultsReplication(t *testing.T) {
	cfg := Default()
	cfg.Addons.SQLite = map[string]SQLiteConfig{
		"appdb": {
			Replication: &SQLiteReplicationConfig{
				S3: &SQLiteS3Config{Bucket: "backups"},
			},
		},
	}
	cfg.ApplyDefaults()

	r := cfg.Addons.SQLite["appdb"].Replication
	if r.SyncInterval != "1s" || r.SnapshotInterval != "24h" || r.Retention != "24h" {
		t.Errorf("durations = %q/%q/%q, want 1s/24h/24h", r.SyncInterval, r.SnapshotInterval, r.Retention)
	}
	if r.S3.Path != "sqlite/appdb" {
		t.Errorf("s3.path = %q, want sqlite/appdb", r.S3.Path)
	}
	if r.S3.Region != "us-east-1" {
		t.Errorf("s3.region = %q, want us-east-1", r.S3.Region)
	}
	if r.S3.URIStyle != "path" {
		t.Errorf("s3.uri_style = %q, want path", r.S3.URIStyle)
	}
}

func TestSQLiteSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Addons.SQLite = map[string]SQLiteConfig{
		"appdb": {
			// Autostart is meaningless without Litestream, so the round-trip
			// case keeps replication configured.
			Autostart: true,
			Replication: &SQLiteReplicationConfig{
				S3: &SQLiteS3Config{
					Endpoint: "http://127.0.0.1:9000",
					Bucket:   "backups",
				},
			},
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
	db, ok := got.Addons.SQLite["appdb"]
	if !ok {
		t.Fatal("appdb missing after round-trip")
	}
	if db.Path != cfg.Addons.SQLite["appdb"].Path {
		t.Errorf("Path = %q, want %q", db.Path, cfg.Addons.SQLite["appdb"].Path)
	}
	if db.Replication == nil || db.Replication.S3 == nil {
		t.Fatal("replication lost after round-trip")
	}
	if db.Replication.S3.Bucket != "backups" || db.Replication.S3.Path != "sqlite/appdb" {
		t.Errorf("s3 = %+v", db.Replication.S3)
	}
	if !db.Autostart {
		t.Error("autostart lost after round-trip")
	}
}

func TestSQLiteConfigHasNoCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Addons.SQLite = map[string]SQLiteConfig{
		"appdb": {
			Replication: &SQLiteReplicationConfig{
				S3: &SQLiteS3Config{Bucket: "backups", Endpoint: "http://127.0.0.1:9000"},
			},
		},
	}
	cfg.ApplyDefaults()
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"access-key", "secret-access", "secret_key", "ACCESS_KEY"} {
		if strings.Contains(string(data), needle) {
			t.Errorf("pg.yaml unexpectedly contains %q:\n%s", needle, data)
		}
	}
}

func TestSQLiteValidate(t *testing.T) {
	valid := func() *Config {
		cfg := Default()
		cfg.Addons.SQLite = map[string]SQLiteConfig{
			"appdb": {
				Replication: &SQLiteReplicationConfig{
					S3: &SQLiteS3Config{
						Endpoint: "https://s3.example.com:9000",
						Bucket:   "backups",
					},
				},
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
				db := c.Addons.SQLite["appdb"]
				db.Name = "../evil"
				c.Addons.SQLite["appdb"] = db
			},
			wantSub: "must match",
		},
		{
			name: "relative path",
			mutate: func(c *Config) {
				db := c.Addons.SQLite["appdb"]
				db.Path = "relative.db"
				c.Addons.SQLite["appdb"] = db
			},
			wantSub: "absolute path",
		},
		{
			name: "duplicate path",
			mutate: func(c *Config) {
				c.Addons.SQLite["second"] = SQLiteConfig{Name: "second", Path: c.Addons.SQLite["appdb"].Path}
			},
			wantSub: "already used",
		},
		{
			name: "missing bucket",
			mutate: func(c *Config) {
				c.Addons.SQLite["appdb"].Replication.S3.Bucket = ""
			},
			wantSub: "bucket must not be empty",
		},
		{
			name: "leading slash in s3 path",
			mutate: func(c *Config) {
				c.Addons.SQLite["appdb"].Replication.S3.Path = "/sqlite/appdb"
			},
			wantSub: "must not start with",
		},
		{
			name: "bad endpoint",
			mutate: func(c *Config) {
				c.Addons.SQLite["appdb"].Replication.S3.Endpoint = "s3.example.com"
			},
			wantSub: "not a bare",
		},
		{
			name: "bad uri style",
			mutate: func(c *Config) {
				c.Addons.SQLite["appdb"].Replication.S3.URIStyle = "dns"
			},
			wantSub: "uri_style",
		},
		{
			name: "bad duration",
			mutate: func(c *Config) {
				c.Addons.SQLite["appdb"].Replication.SyncInterval = "fast"
			},
			wantSub: "positive duration",
		},
		{
			name: "zero retention",
			mutate: func(c *Config) {
				c.Addons.SQLite["appdb"].Replication.Retention = "0s"
			},
			wantSub: "positive duration",
		},
		{
			name: "autostart without replication",
			mutate: func(c *Config) {
				db := c.Addons.SQLite["appdb"]
				db.Autostart = true
				db.Replication = nil
				c.Addons.SQLite["appdb"] = db
			},
			wantSub: "requires replication",
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
