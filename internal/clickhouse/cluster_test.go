package clickhouse

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func clusterTestConfig(t *testing.T) config.ClickHouseConfig {
	t.Helper()
	dir := t.TempDir()
	return config.ClickHouseConfig{
		Name:            "clickhouse",
		ConfigPath:      filepath.Join(dir, "config.d", "zz-pgcli.xml"),
		DataDir:         filepath.Join(dir, "data"),
		LogDir:          filepath.Join(dir, "logs"),
		BackupDir:       filepath.Join(dir, "backups"),
		SecretPath:      filepath.Join(dir, "secrets", "admin_password"),
		Listen:          "0.0.0.0",
		Cluster:         "chcluster",
		Shard:           1,
		Replica:         1,
		AdvertiseHost:   "10.241.20.147",
		TCPPort:         9000,
		HTTPPort:        18123,
		InterserverPort: 9009,
		Peers: []string{
			"1/2@10.241.20.148:9000",
			"2/1@10.241.20.153:9000",
			"2/2@10.241.20.154:9000",
		},
		Keepers: []string{
			"10.241.20.147:9181",
			"10.241.20.148:9181",
			"10.241.20.153:9181",
		},
		AdminUser:     "admin",
		AdminPassword: "s3cret",
	}
}

func TestRenderClusterConfig(t *testing.T) {
	cc := clusterTestConfig(t)
	out, err := RenderClusterConfig(cc)
	if err != nil {
		t.Fatalf("RenderClusterConfig: %v", err)
	}
	for _, want := range []string{
		"<?xml version=\"1.0\"?>",
		"<clickhouse>",
		"<remote_servers>",
		"<chcluster>",
		"<internal_replication>true</internal_replication>",
		"<host>10.241.20.147</host>",
		"<port>9000</port>",
		"<user>admin</user>",
		`<password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>`,
		"<macros>",
		"<cluster>chcluster</cluster>",
		"<shard>1</shard>",
		"<replica>1</replica>",
		"<zookeeper>",
		"<session_timeout_ms>30000</session_timeout_ms>",
		"<operation_timeout_ms>10000</operation_timeout_ms>",
		"<distributed_ddl>",
		"<path>/clickhouse/chcluster/task_queue/ddl</path>",
		"<interserver_http_host>10.241.20.147</interserver_http_host>",
		"<interserver_http_port>9009</interserver_http_port>",
		"<default_replica_path>/clickhouse/chcluster/tables/{uuid}/{shard}</default_replica_path>",
		"<default_replica_name>{replica}</default_replica_name>",
		"</clickhouse>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cluster config missing %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "\n            <shard>\n"); got != 2 {
		t.Errorf("shard blocks = %d, want 2:\n%s", got, out)
	}
	if got := strings.Count(out, "\n                <replica>\n"); got != 4 {
		t.Errorf("replica blocks = %d, want 4:\n%s", got, out)
	}
	if got := strings.Count(out, "<internal_replication>true</internal_replication>"); got != 2 {
		t.Errorf("internal_replication entries = %d, want 2", got)
	}
	if got := strings.Count(out, "\n        <node>\n"); got != 3 {
		t.Errorf("keeper nodes = %d, want 3", got)
	}
	if got := strings.Count(out, "<user>admin</user>"); got != 4 {
		t.Errorf("replica user entries = %d, want 4", got)
	}
	if got := strings.Count(out, `<password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>`); got != 4 {
		t.Errorf("replica password env references = %d, want 4", got)
	}
	if strings.Contains(out, cc.AdminPassword) {
		t.Errorf("cluster config leaks the admin password:\n%s", out)
	}

	var parsed struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("rendered cluster config is not well-formed XML: %v", err)
	}
	if parsed.XMLName.Local != "clickhouse" {
		t.Errorf("root element = %q, want clickhouse", parsed.XMLName.Local)
	}
}

func TestRenderClusterConfigRejectsStandalone(t *testing.T) {
	cc := clusterTestConfig(t)
	cc.Cluster = ""
	if _, err := RenderClusterConfig(cc); err == nil || !strings.Contains(err.Error(), "cluster mode") {
		t.Fatalf("error = %v, want cluster mode error", err)
	}
}

func TestRenderClusterConfigRejectsInvalidClusterName(t *testing.T) {
	cc := clusterTestConfig(t)
	cc.Cluster = "bad name"
	if _, err := RenderClusterConfig(cc); err == nil || !strings.Contains(err.Error(), "cluster name") {
		t.Fatalf("error = %v, want cluster name error", err)
	}
}

func TestWriteGeneratedConfigSwitchesModes(t *testing.T) {
	cc := clusterTestConfig(t)
	if err := WriteGeneratedConfig(cc); err != nil {
		t.Fatalf("WriteGeneratedConfig clustered: %v", err)
	}
	for _, path := range []string{cc.ConfigPath, ClusterConfigPath(cc)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
	}

	standalone := cc
	standalone.Cluster = ""
	standalone.Shard = 0
	standalone.Replica = 0
	standalone.AdvertiseHost = ""
	standalone.Peers = nil
	standalone.Keepers = nil
	standalone.InterserverPort = 0
	if err := WriteGeneratedConfig(standalone); err != nil {
		t.Fatalf("WriteGeneratedConfig standalone: %v", err)
	}
	if _, err := os.Stat(ClusterConfigPath(standalone)); !os.IsNotExist(err) {
		t.Errorf("stale cluster config was not removed: %v", err)
	}
	if _, err := os.Stat(standalone.ConfigPath); err != nil {
		t.Errorf("base config missing: %v", err)
	}
}

func TestRenderConfigGolden(t *testing.T) {
	cc := testConfig(t)
	out, err := RenderConfig(cc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	golden := filepath.Join("testdata", "standalone_config.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading golden file (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if out != string(want) {
		t.Errorf("standalone config changed:\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}
