package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentDBDefaults(t *testing.T) {
	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"demo": {}}
	cfg.Addons.DocumentDB = map[string]DocumentDBConfig{
		"ddb": {TargetInstance: "demo", Database: "postgres"},
	}

	cfg.ApplyDefaults()

	dc, ok := cfg.Addons.DocumentDB["ddb"]
	if !ok {
		t.Fatal("documentdb addon missing after ApplyDefaults")
	}
	if dc.Name != "ddb" {
		t.Errorf("name = %q, want ddb", dc.Name)
	}
	if dc.ContainerName != "pgcli-documentdb-ddb" {
		t.Errorf("container name = %q, want pgcli-documentdb-ddb", dc.ContainerName)
	}
	if dc.ImageTag != DefaultDocumentDBImageTag {
		t.Errorf("image tag = %q, want %q", dc.ImageTag, DefaultDocumentDBImageTag)
	}
	if dc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0", dc.Listen)
	}
	if dc.ClientUser != "docdb_admin" {
		t.Errorf("client user = %q, want docdb_admin", dc.ClientUser)
	}
	if dc.TLSMode != "allow" {
		t.Errorf("tls mode = %q, want allow", dc.TLSMode)
	}
	if dc.HostPort != DefaultDocumentDBPort {
		t.Errorf("port = %d, want %d", dc.HostPort, DefaultDocumentDBPort)
	}
	// Secrets and the target database are NEVER defaulted.
	if dc.ClientPassword != "" {
		t.Errorf("ApplyDefaults must not invent a client password, got %q", dc.ClientPassword)
	}
}

func TestDocumentDBPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.DocumentDBStartPort = 39610 // high unused base keeps the live-listener scan out of expectations
	cfg.Instances = map[string]InstanceConfig{"demo": {}}
	cfg.Addons.DocumentDB = map[string]DocumentDBConfig{
		"b": {TargetInstance: "demo", Database: "postgres"},
		"a": {TargetInstance: "demo", Database: "postgres", HostPort: 39615},
		"c": {TargetInstance: "demo", Database: "postgres"},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.DocumentDB["a"].HostPort; got != 39615 {
		t.Errorf("explicit port changed: %d", got)
	}
	b := cfg.Addons.DocumentDB["b"].HostPort
	c := cfg.Addons.DocumentDB["c"].HostPort
	if b == c {
		t.Errorf("two instances share port %d", b)
	}
	for _, p := range []int{b, c} {
		if p == 39615 {
			t.Errorf("auto-assigned port collided with the explicit 39615")
		}
		if p < 39610 {
			t.Errorf("port %d below base 39610", p)
		}
	}
}

func TestDocumentDBValidate(t *testing.T) {
	newCfg := func(dc DocumentDBConfig) *Config {
		cfg := Default()
		cfg.Instances = map[string]InstanceConfig{"demo": {}}
		cfg.Addons.DocumentDB = map[string]DocumentDBConfig{"ddb": dc}
		cfg.ApplyDefaults()
		return cfg
	}

	good := DocumentDBConfig{TargetInstance: "demo", Database: "postgres"}
	if err := newCfg(good).Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	if err := newCfg(DocumentDBConfig{Database: "postgres"}).Validate(); err == nil || !strings.Contains(err.Error(), "target_instance is required") {
		t.Errorf("missing target: want required error, got %v", err)
	}
	if err := newCfg(DocumentDBConfig{TargetInstance: "ghost", Database: "postgres"}).Validate(); err == nil || !strings.Contains(err.Error(), "not found in instances") {
		t.Errorf("unknown instance: want not-found error, got %v", err)
	}
	if err := newCfg(DocumentDBConfig{TargetInstance: "demo"}).Validate(); err == nil || !strings.Contains(err.Error(), "database is required") {
		t.Errorf("missing database: want required error, got %v", err)
	}
	if err := newCfg(DocumentDBConfig{TargetInstance: "demo", Database: "postgres", HostPort: 70000}).Validate(); err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
		t.Errorf("bad port: want range error, got %v", err)
	}
	if err := newCfg(DocumentDBConfig{TargetInstance: "demo", Database: "postgres", Listen: "10.0.0.1"}).Validate(); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("bad listen: want listen error, got %v", err)
	}
	if err := newCfg(DocumentDBConfig{TargetInstance: "demo", Database: "postgres", TLSMode: "strict"}).Validate(); err == nil || !strings.Contains(err.Error(), "tls_mode") {
		t.Errorf("bad tls mode: want tls error, got %v", err)
	}
	for _, user := range []string{"documentdb_admin", "citus_user", "pgadmin", "internal_role_x", "has space", "quote\"x", "semi;colon"} {
		if err := newCfg(DocumentDBConfig{TargetInstance: "demo", Database: "postgres", ClientUser: user}).Validate(); err == nil || !strings.Contains(err.Error(), "client_user") {
			t.Errorf("client user %q: want client_user error, got %v", user, err)
		}
	}
	for _, user := range []string{"docdb_admin", "app_user-1", "Alice"} {
		if err := newCfg(DocumentDBConfig{TargetInstance: "demo", Database: "postgres", ClientUser: user}).Validate(); err != nil {
			t.Errorf("client user %q rejected: %v", user, err)
		}
	}

	dup := Default()
	dup.Instances = map[string]InstanceConfig{"demo": {}}
	dup.Addons.DocumentDB = map[string]DocumentDBConfig{
		"a": {TargetInstance: "demo", Database: "postgres", HostPort: 39620},
		"b": {TargetInstance: "demo", Database: "postgres", HostPort: 39620},
	}
	dup.ApplyDefaults()
	if err := dup.Validate(); err == nil || !strings.Contains(err.Error(), "already used by") {
		t.Errorf("duplicate port: want conflict error, got %v", err)
	}
}

func TestDocumentDBRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"demo": {}}
	cfg.Addons.DocumentDB = map[string]DocumentDBConfig{
		"ddb": {
			TargetInstance: "demo",
			Database:       "appdb",
			ClientUser:     "docdb_admin",
			ClientPassword: "secret-value",
			TLSMode:        "require",
			Autostart:      true,
		},
	}
	cfg.ApplyDefaults()

	path := filepath.Join(t.TempDir(), "pg.yaml")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	dc, ok := loaded.Addons.DocumentDB["ddb"]
	if !ok {
		t.Fatal("documentdb addon lost in round-trip")
	}
	if dc.TargetInstance != "demo" || dc.Database != "appdb" || dc.ClientUser != "docdb_admin" {
		t.Errorf("round-trip target/db/user = %q/%q/%q", dc.TargetInstance, dc.Database, dc.ClientUser)
	}
	if dc.ClientPassword != "secret-value" || dc.TLSMode != "require" || !dc.Autostart {
		t.Errorf("round-trip secret/tls/autostart = %q/%q/%v", dc.ClientPassword, dc.TLSMode, dc.Autostart)
	}
}

func TestValidDocumentDBClientUser(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"docdb_admin", true},
		{"app_user-1", true},
		{"Alice", true},
		{"", false},
		{"documentdb_admin", false},
		{"DocumentDB_admin", false},
		{"citus", false},
		{"pgadmin", false},
		{"PGadmin", false},
		{"internal_role", false},
		{"a b", false},
		{"a;b", false},
		{"_leading", false},
		{strings.Repeat("a", 64), false},
	}
	for _, tt := range tests {
		if got := ValidDocumentDBClientUser(tt.name); got != tt.want {
			t.Errorf("ValidDocumentDBClientUser(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
