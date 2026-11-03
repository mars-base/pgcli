package podman

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func testClickHouseKeeperConfig(t *testing.T) config.ClickHouseKeeperConfig {
	t.Helper()
	dir := t.TempDir()
	return config.ClickHouseKeeperConfig{
		ContainerName: "pgcli-clickhouse-keeper-test",
		Name:          "test",
		Version:       config.DefaultClickHouseKeeperVersion,
		ImageTag:      config.DefaultClickHouseKeeperImageTag,
		InstanceDir:   filepath.Join(dir, "keeper"),
		ConfigPath:    filepath.Join(dir, "keeper", "config", "keeper_config.xml"),
		DataDir:       filepath.Join(dir, "keeper", "data"),
		LogDir:        filepath.Join(dir, "keeper", "logs"),
		Listen:        "0.0.0.0",
		ServerID:      1,
		AdvertiseHost: "10.0.0.1",
		Peers:         []string{"2@10.0.0.2", "3@10.0.0.3"},
		ClientPort:    9181,
		RaftPort:      9234,
	}
}

func TestClickHouseKeeperCreateArgs(t *testing.T) {
	kc := testClickHouseKeeperConfig(t)
	got := clickHouseKeeperCreateArgs(kc)
	want := []string{
		"run", "-d",
		"--name", "pgcli-clickhouse-keeper-test",
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"--ulimit", "nofile=262144:262144",
		"--tmpfs", "/var/lib/clickhouse:rw",
		"-v", kc.DataDir + ":/var/lib/clickhouse-keeper:z",
		"-v", kc.LogDir + ":/var/log/clickhouse-keeper:z",
		"-v", filepath.Dir(kc.ConfigPath) + ":/etc/pgcli-clickhouse-keeper:ro,z",
		"-e", "KEEPER_CONFIG=/etc/pgcli-clickhouse-keeper/keeper_config.xml",
		"-e", "CLICKHOUSE_DATA_DIR=/var/lib/clickhouse-keeper",
		"-e", "LOG_DIR=/var/log/clickhouse-keeper",
		kc.ImageTag,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("clickHouseKeeperCreateArgs:\n got %q\nwant %q", got, want)
	}
}

func TestClickHouseKeeperEnsureFiles(t *testing.T) {
	kc := testClickHouseKeeperConfig(t)
	m := &ClickHouseKeeperManager{}
	if err := m.ensureFiles(&kc); err != nil {
		t.Fatalf("ensureFiles: %v", err)
	}
	for _, dir := range []string{kc.DataDir, kc.LogDir, filepath.Dir(kc.ConfigPath)} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("expected dir %s: %v", dir, err)
		}
	}
	body, err := os.ReadFile(kc.ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"<server_id>1</server_id>", "<tcp_port>9181</tcp_port>", "<hostname>10.0.0.2</hostname>"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("config missing %q:\n%s", want, body)
		}
	}
}

func TestClickHouseKeeperCleanDirs(t *testing.T) {
	kc := testClickHouseKeeperConfig(t)
	got := clickHouseKeeperCleanDirs(kc)
	want := []string{kc.InstanceDir}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("default clean dirs = %q, want %q", got, want)
	}

	external := filepath.Join(t.TempDir(), "elsewhere")
	kc.DataDir = external
	got = clickHouseKeeperCleanDirs(kc)
	want = []string{kc.InstanceDir, external}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("external data clean dirs = %q, want %q", got, want)
	}
}

func TestClickHouseEnsureFilesWritesClusterConfig(t *testing.T) {
	dir := t.TempDir()
	cc := config.ClickHouseConfig{
		ContainerName:   "pgcli-clickhouse-test",
		Name:            "test",
		Version:         config.DefaultClickHouseVersion,
		ImageTag:        config.DefaultClickHouseImageTag,
		ConfigPath:      filepath.Join(dir, "config.d", "zz-pgcli.xml"),
		DataDir:         filepath.Join(dir, "data"),
		LogDir:          filepath.Join(dir, "logs"),
		BackupDir:       filepath.Join(dir, "backups"),
		SecretPath:      filepath.Join(dir, "secrets", "admin_password"),
		Listen:          "0.0.0.0",
		Cluster:         "chcluster",
		Shard:           1,
		Replica:         1,
		AdvertiseHost:   "10.0.0.1",
		TCPPort:         9000,
		HTTPPort:        18123,
		InterserverPort: 9009,
		Peers:           []string{"1/2@10.0.0.2:9000"},
		Keepers:         []string{"10.0.0.3:9181"},
		AdminUser:       "admin",
		AdminPassword:   "s3cret",
	}
	m := &ClickHouseManager{}
	if err := m.ensureFiles(&cc); err != nil {
		t.Fatalf("ensureFiles: %v", err)
	}
	clusterPath := filepath.Join(filepath.Dir(cc.ConfigPath), "zz-pgcli-cluster.xml")
	body, err := os.ReadFile(clusterPath)
	if err != nil {
		t.Fatalf("cluster config missing: %v", err)
	}
	if !strings.Contains(string(body), "<internal_replication>true</internal_replication>") {
		t.Errorf("cluster config missing internal_replication:\n%s", body)
	}

	standalone := cc
	standalone.Cluster = ""
	standalone.Shard = 0
	standalone.Replica = 0
	standalone.AdvertiseHost = ""
	standalone.Peers = nil
	standalone.Keepers = nil
	standalone.InterserverPort = 0
	if err := m.ensureFiles(&standalone); err != nil {
		t.Fatalf("ensureFiles standalone: %v", err)
	}
	if _, err := os.Stat(clusterPath); !os.IsNotExist(err) {
		t.Errorf("stale cluster config was not removed: %v", err)
	}
}
