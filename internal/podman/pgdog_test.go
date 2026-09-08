package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func newTestPgDogManager(t *testing.T) *PgDogManager {
	t.Helper()
	base := t.TempDir()
	return &PgDogManager{cfg: config.Default(), podman: "podman", dataDir: base}
}

func TestPgDogWriteConfigs(t *testing.T) {
	m := newTestPgDogManager(t)
	pd := &config.PgDogConfig{
		Name:            "proxy",
		Host:            "127.0.0.1",
		HostPort:        7432,
		OpenmetricsPort: 7433,
		PoolerMode:      "transaction",
		Workers:         2,
		DefaultPoolSize: 10,
		Backends: []config.PgDogBackend{
			{Name: "app", Host: "10.0.0.1", Port: 5432, DatabaseName: "shard0", Shard: 0, Role: "primary"},
			{Name: "app", Host: "10.0.0.2", Port: 5432, DatabaseName: "shard1", Shard: 1},
		},
		Users:         []config.PgDogUser{{Name: "alice", Password: "p\"w", Database: "app"}},
		ShardedTables: []config.PgDogShardedTable{{Database: "app", Name: "users", Column: "id", DataType: "bigint"}},
	}

	tomlPath, usersPath, err := m.WriteConfigs(pd)
	if err != nil {
		t.Fatalf("WriteConfigs: %v", err)
	}

	tomlData, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read pgdog.toml: %v", err)
	}
	toml := string(tomlData)
	for _, want := range []string{
		"[general]",
		`host = "127.0.0.1"`,
		"port = 7432",
		"workers = 2",
		"default_pool_size = 10",
		`pooler_mode = "transaction"`,
		"openmetrics_port = 7433",
		"[[databases]]",
		`name = "app"`,
		`host = "10.0.0.1"`,
		"shard = 1",
		`role = "primary"`,
		"[[sharded_tables]]",
		`column = "id"`,
		`data_type = "bigint"`,
	} {
		if !strings.Contains(toml, want) {
			t.Errorf("pgdog.toml missing %q:\n%s", want, toml)
		}
	}
	// Second backend has no role — the line must be absent for that entry.
	if strings.Count(toml, "[[databases]]") != 2 {
		t.Errorf("expected 2 [[databases]] entries, got %d", strings.Count(toml, "[[databases]]"))
	}
	if strings.Count(toml, `role = "primary"`) != 1 {
		t.Errorf("expected exactly 1 role line, got %d", strings.Count(toml, `role = "primary"`))
	}

	usersData, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatalf("read users.toml: %v", err)
	}
	users := string(usersData)
	// The password contains a double quote — must be TOML-escaped.
	if !strings.Contains(users, `password = "p\"w"`) {
		t.Errorf("users.toml did not escape the password quote:\n%s", users)
	}
	if !strings.Contains(users, `name = "alice"`) || !strings.Contains(users, `database = "app"`) {
		t.Errorf("users.toml missing expected fields:\n%s", users)
	}

	// users.toml holds plaintext passwords: mode must be 0600.
	if info, err := os.Stat(usersPath); err != nil {
		t.Fatalf("stat users.toml: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("users.toml mode = %o, want 600", perm)
	}

	// Files land under the per-proxy config dir.
	if filepath.Base(filepath.Dir(tomlPath)) != "proxy" {
		t.Errorf("pgdog.toml dir = %q, want .../proxy", filepath.Dir(tomlPath))
	}
}

func TestPgDogReplicationModeLine(t *testing.T) {
	m := newTestPgDogManager(t)
	pd := &config.PgDogConfig{
		Name: "r", Host: "127.0.0.1", HostPort: 1, OpenmetricsPort: 2,
		PoolerMode: "transaction", Workers: 1, DefaultPoolSize: 1,
		Backends: []config.PgDogBackend{{Name: "app", Host: "127.0.0.1", Port: 5432, DatabaseName: "app"}},
		Users:    []config.PgDogUser{{Name: "repl", Password: "p", Database: "app", ReplicationMode: true}},
	}
	_, usersPath, err := m.WriteConfigs(pd)
	if err != nil {
		t.Fatalf("WriteConfigs: %v", err)
	}
	data, _ := os.ReadFile(usersPath)
	if !strings.Contains(string(data), "replication_mode = true") {
		t.Errorf("expected replication_mode line, got:\n%s", data)
	}
}
