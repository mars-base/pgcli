package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
	yaml "gopkg.in/yaml.v3"
)

func testPatroniCluster() *config.PatroniClusterConfig {
	return &config.PatroniClusterConfig{
		Name: "app",
		Passwords: config.PatroniPasswords{
			Superuser: "su-pass", Replication: "re-pass", Rewind: "rw-pass",
			RestapiUser: "restuser", RestapiPasswd: "restpass",
		},
		Members: map[string]config.PatroniMemberConfig{
			"node1": {HostPort: 5432, RestapiPort: 8008, Autostart: false},
		},
	}
}

func TestPatroniRenderScopeIsNamespaced(t *testing.T) {
	cfg := config.Default()
	cfg.Namespace = "prod"
	m := &PatroniManager{cfg: cfg, dataDir: t.TempDir()}
	cluster := testPatroniCluster()
	cluster.EtcdEndpoints = []string{"10.0.0.9:2379"}

	path, err := m.WriteMemberConfig(cluster, "node1")
	if err != nil {
		t.Fatalf("WriteMemberConfig: %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantScope := cfg.PatroniScope("app") // "app-prod"
	if doc["scope"] != wantScope {
		t.Errorf("scope = %v, want %q (namespace must be baked in; Patroni has no other namespace concept)", doc["scope"], wantScope)
	}
	if doc["name"] != "node1" {
		t.Errorf("name = %v, want node1 (member key, no nsSuffix)", doc["name"])
	}
}

func TestPatroniWriteMemberConfigPermissions(t *testing.T) {
	cfg := config.Default()
	m := &PatroniManager{cfg: cfg, dataDir: t.TempDir()}
	cluster := testPatroniCluster()
	cluster.EtcdEndpoints = []string{"127.0.0.1:2379"}

	path, err := m.WriteMemberConfig(cluster, "node1")
	if err != nil {
		t.Fatalf("WriteMemberConfig: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// patroni.yml embeds the superuser/replication/rewind/restapi passwords.
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("patroni.yml mode = %o, want 600 (it contains cluster passwords)", perm)
	}
}

func TestPatroniDcsEndpointsPrecedence(t *testing.T) {
	cfg := config.Default()
	cfg.Addons.Etcd = map[string]config.EtcdConfig{
		"m1": {Name: "m1", ClientPort: 2379},
	}
	m := &PatroniManager{cfg: cfg, dataDir: t.TempDir()}

	cluster := testPatroniCluster()
	cluster.EtcdMembers = []string{"m1"}
	cluster.EtcdEndpoints = []string{"10.0.0.9:2379", "10.0.0.10:2379"}

	got, err := m.dcsEndpoints(cluster)
	if err != nil {
		t.Fatalf("dcsEndpoints: %v", err)
	}
	// EtcdEndpoints (explicit, for cross-host / external DCS) must win over
	// EtcdMembers (local addon lookup).
	if got != "10.0.0.9:2379,10.0.0.10:2379" {
		t.Errorf("explicit EtcdEndpoints ignored: got %q", got)
	}

	// With no explicit endpoints, fall back to resolving local etcd members.
	cluster.EtcdEndpoints = nil
	got, err = m.dcsEndpoints(cluster)
	if err != nil {
		t.Fatalf("dcsEndpoints (members): %v", err)
	}
	if got != "127.0.0.1:2379" {
		t.Errorf("resolved from EtcdMembers: got %q, want 127.0.0.1:2379", got)
	}

	// Neither set → clear error naming both flags.
	cluster.EtcdMembers = nil
	_, err = m.dcsEndpoints(cluster)
	if err == nil {
		t.Fatal("expected error when cluster has no DCS configured at all")
	}
	if !strings.Contains(err.Error(), "--etcd") || !strings.Contains(err.Error(), "--etcd-endpoints") {
		t.Errorf("error must name both flags, got: %v", err)
	}
}

func TestPatroniRenderPgHbaAllowsPastaSourceAddress(t *testing.T) {
	cfg := config.Default()
	m := &PatroniManager{cfg: cfg, dataDir: t.TempDir()}
	cluster := testPatroniCluster()
	cluster.EtcdEndpoints = []string{"127.0.0.1:2379"}

	path, err := m.WriteMemberConfig(cluster, "node1")
	if err != nil {
		t.Fatalf("WriteMemberConfig: %v", err)
	}
	out, _ := os.ReadFile(path)
	var doc map[string]any
	yaml.Unmarshal(out, &doc)

	boot := doc["bootstrap"].(map[string]any)
	pgHBA, ok := boot["pg_hba"].([]any)
	if !ok || len(pgHBA) == 0 {
		t.Fatalf("bootstrap.dcs must include pg_hba, got %v", boot["pg_hba"])
	}
	// Rootless podman's pasta rewrites loopback connections to arrive from
	// 192.168.10.1, so a 127.0.0.1/32-only rule would lock Patroni out of its
	// own postmaster. Every line must use "all" as the address, per the
	// user-approved permissive (scram) pg_hba strategy.
	for _, line := range pgHBA {
		s, _ := line.(string)
		if !strings.Contains(s, " all ") {
			t.Errorf("pg_hba line must allow any address (pasta rewrites loopback source IP), got %q", s)
		}
	}
}

func TestPatroniListenFlipsWithAdvertiseHost(t *testing.T) {
	cfg := config.Default()
	m := &PatroniManager{cfg: cfg, dataDir: t.TempDir()}

	cluster := testPatroniCluster()
	cluster.EtcdEndpoints = []string{"127.0.0.1:2379"}

	// No AdvertiseHost (local-only member): everything stays on loopback.
	path, err := m.WriteMemberConfig(cluster, "node1")
	if err != nil {
		t.Fatalf("WriteMemberConfig: %v", err)
	}
	out, _ := os.ReadFile(path)
	var doc map[string]any
	yaml.Unmarshal(out, &doc)
	pg := doc["postgresql"].(map[string]any)
	if listen := pg["listen"]; !strings.HasPrefix(listen.(string), "127.0.0.1:") {
		t.Errorf("local member must listen on loopback, got listen=%v", listen)
	}
	if connect := pg["connect_address"]; !strings.HasPrefix(connect.(string), "127.0.0.1:") {
		t.Errorf("local member connect_address must be loopback, got %v", connect)
	}

	// With AdvertiseHost (cross-host member): listen opens to 0.0.0.0 and
	// connect_address carries the real, reachable host — this is what other
	// members and clients read from the DCS to reach this node.
	m2 := cluster.Members["node1"]
	m2.AdvertiseHost = "10.0.0.11"
	cluster.Members["node1"] = m2
	path, err = m.WriteMemberConfig(cluster, "node1")
	if err != nil {
		t.Fatalf("WriteMemberConfig (advertise): %v", err)
	}
	out, _ = os.ReadFile(path)
	doc = map[string]any{}
	yaml.Unmarshal(out, &doc)
	pg = doc["postgresql"].(map[string]any)
	if listen := pg["listen"]; !strings.HasPrefix(listen.(string), "0.0.0.0:") {
		t.Errorf("AdvertiseHost member must listen on 0.0.0.0, got listen=%v", listen)
	}
	if connect := pg["connect_address"]; connect != "10.0.0.11:5432" {
		t.Errorf("AdvertiseHost member connect_address must be the advertised host:port, got %v", connect)
	}
	restapi := doc["restapi"].(map[string]any)
	if connect := restapi["connect_address"]; connect != "10.0.0.11:8008" {
		t.Errorf("AdvertiseHost member restapi connect_address must follow the same host, got %v", connect)
	}
}

// The config embeds in-container paths: Patroni resolves data_dir/pgpass inside
// the member container (member dir mounted at /patroni, data at
// /var/lib/postgresql), so a host path would not exist at runtime.
func TestPatroniRenderUsesContainerPaths(t *testing.T) {
	cfg := config.Default()
	hostBase := t.TempDir() // a real, writable host dir (would be used if any host path leaked)
	m := &PatroniManager{cfg: cfg, dataDir: hostBase}
	cluster := testPatroniCluster()
	cluster.EtcdEndpoints = []string{"127.0.0.1:2379"}

	path, err := m.WriteMemberConfig(cluster, "node1")
	if err != nil {
		t.Fatalf("WriteMemberConfig: %v", err)
	}
	out, _ := os.ReadFile(path)
	var doc map[string]any
	yaml.Unmarshal(out, &doc)
	pg := doc["postgresql"].(map[string]any)

	if pg["data_dir"] != "/var/lib/postgresql/data" {
		t.Errorf("data_dir must be the in-container PGDATA, got %v", pg["data_dir"])
	}
	pgpass, _ := pg["pgpass"].(string)
	if pgpass != "/patroni/.pgpass" {
		t.Errorf("pgpass must be an in-container path (/patroni/.pgpass), got %q", pgpass)
	}
	// The host base dir must never leak into the config Patroni reads at runtime.
	if strings.Contains(string(out), hostBase) {
		t.Errorf("rendered config must not contain host paths:\n%s", out)
	}
}

func TestPatroniPickMemberYMLPrefersOnDisk(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	m := &PatroniManager{cfg: cfg, dataDir: dir}

	cluster := testPatroniCluster()
	cluster.Members["node2"] = config.PatroniMemberConfig{HostPort: 5433, RestapiPort: 8009}
	cluster.EtcdEndpoints = []string{"127.0.0.1:2379"}

	// Before any member is created (no yml on disk), pickMemberYML must still
	// produce a usable, ephemeral config pointing at loopback so `pg ha status`
	// works pre-install.
	ymlPath, cleanup, tag, err := m.pickMemberYML(cluster)
	if err != nil {
		t.Fatalf("pickMemberYML with no on-disk config: %v", err)
	}
	defer cleanup()
	if ymlPath == "" || tag == "" {
		t.Fatalf("expected a real path and image tag, got path=%q tag=%q", ymlPath, tag)
	}
	out, _ := os.ReadFile(ymlPath)
	if !strings.Contains(string(out), "127.0.0.1:8008") {
		t.Errorf("ephemeral config should force loopback connect_address, got:\n%s", out)
	}
	if _, statErr := os.Stat(ymlPath); statErr != nil {
		t.Fatalf("ephemeral config should exist before cleanup: %v", statErr)
	}
	cleanup()
	if _, statErr := os.Stat(ymlPath); !os.IsNotExist(statErr) {
		t.Errorf("cleanup() must remove the ephemeral config, but %s still exists", ymlPath)
	}

	// After node2 has a real on-disk config, pickMemberYML must prefer it over
	// the ephemeral render (it reflects how that member was actually created).
	if _, err := m.WriteMemberConfig(cluster, "node2"); err != nil {
		t.Fatalf("WriteMemberConfig node2: %v", err)
	}
	ymlPath, cleanup, _, err = m.pickMemberYML(cluster)
	if err != nil {
		t.Fatalf("pickMemberYML after create: %v", err)
	}
	defer cleanup()
	want := filepath.Join(m.memberConfigDir(cfg.PatroniScope("app"), "node2"), "patroni.yml")
	if ymlPath != want {
		t.Errorf("must prefer the on-disk member config %s, got %s", want, ymlPath)
	}
}
