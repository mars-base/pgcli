package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func newTestPgBouncerManager(t *testing.T, bridge bool, instances map[string]config.InstanceConfig) *PgBouncerManager {
	t.Helper()
	cfg := config.Default()
	cfg.Instances = instances
	return &PgBouncerManager{cfg: cfg, podman: "podman", dataDir: t.TempDir(), bridge: bridge}
}

// localInst returns an Instances map keyed "app" whose PG container is named
// pgcli-pg-app, reachable on the bridge at that name.
func localInst() map[string]config.InstanceConfig {
	return map[string]config.InstanceConfig{
		"app": {Podman: config.PodmanConfig{ContainerName: "pgcli-pg-app", HostPort: 5432}},
	}
}

// TestPgBouncerWriteConfigsBackendHost pins the pgbouncer.ini backend host
// rewrite: on the macOS bridge, a local-mode loopback DSN becomes the
// instance's container name; on Linux (bridge=false) it stays loopback.
func TestPgBouncerWriteConfigsBackendHost(t *testing.T) {
	auth := &AuthUser{User: "pgb_default_app", Passwd: "x"}

	cases := []struct {
		name    string
		bridge  bool
		dsn     string
		wantIni string // expected "* = host=... port=..." line
	}{
		{"linux local keeps loopback", false, "postgres://u:p@127.0.0.1:5432/db", "* = host=127.0.0.1 port=5432"},
		{"macOS bridge local rewrites to container name", true, "postgres://u:p@127.0.0.1:5432/db", "* = host=pgcli-pg-app port=5432"},
		{"macOS bridge remote host untouched", true, "postgres://u:p@10.0.0.5:5432/db", "* = host=10.0.0.5 port=5432"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestPgBouncerManager(t, tc.bridge, localInst())
			pb := &config.PgBouncerConfig{HostPort: 6432, PoolMode: "transaction"}
			iniPath, _, err := m.WriteConfigs(pb, auth, tc.dsn, "app")
			if err != nil {
				t.Fatalf("WriteConfigs: %v", err)
			}
			data, err := os.ReadFile(filepath.Clean(iniPath))
			if err != nil {
				t.Fatalf("read ini: %v", err)
			}
			if !strings.Contains(string(data), tc.wantIni) {
				t.Errorf("pgbouncer.ini missing %q:\n%s", tc.wantIni, data)
			}
		})
	}
}
