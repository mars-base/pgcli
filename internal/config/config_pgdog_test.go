package config

import (
	"path/filepath"
	"testing"
)

func TestPgDogDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.Addons.PgDog = map[string]PgDogConfig{
		"proxy": {
			Backends: []PgDogBackend{{Name: "app", Host: "127.0.0.1", Port: 5432, DatabaseName: "appdb"}},
			Users:    []PgDogUser{{Name: "alice", Password: "s3cret", Database: "app"}},
		},
	}

	cfg.ApplyDefaults()

	pd, ok := cfg.Addons.PgDog["proxy"]
	if !ok {
		t.Fatal("pgdog addon missing after ApplyDefaults")
	}
	if pd.Name != "proxy" {
		t.Errorf("name = %q, want proxy", pd.Name)
	}
	if pd.ContainerName != "pgcli-pgdog-proxy" {
		t.Errorf("container name = %q, want pgcli-pgdog-proxy", pd.ContainerName)
	}
	if pd.ImageTag != "ghcr.io/pgdogdev/pgdog:v0.1.57" {
		t.Errorf("image tag = %q", pd.ImageTag)
	}
	if pd.Host != "127.0.0.1" {
		t.Errorf("host = %q, want 127.0.0.1", pd.Host)
	}
	if pd.PoolerMode != "transaction" {
		t.Errorf("pooler mode = %q, want transaction", pd.PoolerMode)
	}
	if pd.Workers != 2 {
		t.Errorf("workers = %d, want 2", pd.Workers)
	}
	if pd.DefaultPoolSize != 10 {
		t.Errorf("default pool size = %d, want 10", pd.DefaultPoolSize)
	}
	if pd.HostPort < cfg.PgDogStartPort {
		t.Errorf("host port %d below base %d", pd.HostPort, cfg.PgDogStartPort)
	}
	if pd.OpenmetricsPort <= pd.HostPort {
		t.Errorf("openmetrics port %d must be above host port %d", pd.OpenmetricsPort, pd.HostPort)
	}
}

func TestPgDogSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.PgDog = map[string]PgDogConfig{
		"proxy": {
			HostPort:        7432,
			OpenmetricsPort: 7433,
			Host:            "127.0.0.1",
			PoolerMode:      "session",
			Workers:         4,
			DefaultPoolSize: 50,
			Autostart:       true,
			Backends: []PgDogBackend{
				{Name: "app", Host: "10.0.0.1", Port: 5432, DatabaseName: "shard0", Shard: 0, Role: "primary"},
				{Name: "app", Host: "10.0.0.2", Port: 5432, DatabaseName: "shard1", Shard: 1, Role: "primary"},
			},
			Users:         []PgDogUser{{Name: "alice", Password: "s3cret", Database: "app"}},
			ShardedTables: []PgDogShardedTable{{Database: "app", Name: "users", Column: "id", DataType: "bigint"}},
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pd, ok := got.Addons.PgDog["proxy"]
	if !ok {
		t.Fatal("pgdog addon not persisted")
	}
	if pd.HostPort != 7432 || pd.OpenmetricsPort != 7433 {
		t.Errorf("ports not persisted: host=%d openmetrics=%d", pd.HostPort, pd.OpenmetricsPort)
	}
	if len(pd.Backends) != 2 || pd.Backends[1].Shard != 1 || pd.Backends[1].Role != "primary" {
		t.Errorf("backends not persisted correctly: %+v", pd.Backends)
	}
	if len(pd.Users) != 1 || pd.Users[0].Password != "s3cret" {
		t.Errorf("users not persisted correctly: %+v", pd.Users)
	}
	if len(pd.ShardedTables) != 1 || pd.ShardedTables[0].Column != "id" {
		t.Errorf("sharded_tables not persisted correctly: %+v", pd.ShardedTables)
	}
	if !pd.Autostart {
		t.Error("autostart not persisted")
	}
}
