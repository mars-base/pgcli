package kafka

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func testKafkaConfig(t *testing.T) config.KafkaConfig {
	t.Helper()
	dir := t.TempDir()
	return config.KafkaConfig{
		ContainerName:  "pgcli-kafka-test",
		Name:           "test",
		Version:        config.DefaultKafkaVersion,
		ImageTag:       config.DefaultKafkaImageTag,
		ConfigPath:     filepath.Join(dir, "config", "server.properties"),
		DataDir:        filepath.Join(dir, "data"),
		Listen:         "127.0.0.1",
		AdvertiseHost:  "127.0.0.1",
		BrokerPort:     9092,
		ControllerPort: 19093,
		NodeID:         1,
		ClusterID:      "aaaaaaaaaaaaaaaaaaaaaa",
	}
}

func TestGenerateClusterID(t *testing.T) {
	first, err := GenerateClusterID()
	if err != nil {
		t.Fatalf("GenerateClusterID: %v", err)
	}
	if len(first) != 22 || !config.ValidKafkaClusterID(first) {
		t.Errorf("cluster id %q is not a valid 22-char base64url id", first)
	}
	second, err := GenerateClusterID()
	if err != nil {
		t.Fatalf("GenerateClusterID: %v", err)
	}
	if first == second {
		t.Errorf("two generated cluster ids are identical: %q", first)
	}
}

func TestRenderPropertiesSingleNode(t *testing.T) {
	kc := testKafkaConfig(t)
	got, err := RenderProperties(kc)
	if err != nil {
		t.Fatalf("RenderProperties: %v", err)
	}
	want := `process.roles=broker,controller
node.id=1
listeners=PLAINTEXT://127.0.0.1:9092,CONTROLLER://127.0.0.1:19093
advertised.listeners=PLAINTEXT://127.0.0.1:9092
inter.broker.listener.name=PLAINTEXT
controller.listener.names=CONTROLLER
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
controller.quorum.voters=1@127.0.0.1:19093
log.dirs=/var/lib/kafka/data

offsets.topic.replication.factor=1
transaction.state.log.replication.factor=1
transaction.state.log.min.isr=1
share.coordinator.state.topic.replication.factor=1
share.coordinator.state.topic.min.isr=1
default.replication.factor=1
min.insync.replicas=1
`
	if got != want {
		t.Errorf("RenderProperties:\n got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "zookeeper.connect") {
		t.Error("KRaft properties must not contain zookeeper.connect")
	}
}

func TestRenderPropertiesCluster(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.Cluster = "prod"
	kc.NodeID = 2
	kc.Listen = "0.0.0.0"
	kc.AdvertiseHost = "10.241.20.148"
	kc.Peers = []string{"3@10.241.20.153:19093", "1@10.241.20.147:19093"}
	kc.Settings = map[string]string{"num.partitions": "3"}

	got, err := RenderProperties(kc)
	if err != nil {
		t.Fatalf("RenderProperties: %v", err)
	}
	for _, want := range []string{
		"node.id=2\n",
		"listeners=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:19093\n",
		"advertised.listeners=PLAINTEXT://10.241.20.148:9092\n",
		"controller.quorum.voters=1@10.241.20.147:19093,2@10.241.20.148:19093,3@10.241.20.153:19093\n",
		"offsets.topic.replication.factor=3\n",
		"transaction.state.log.min.isr=2\n",
		"default.replication.factor=3\n",
		"min.insync.replicas=2\n",
		"\nnum.partitions=3\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderProperties missing %q:\n%s", want, got)
		}
	}
}

func TestRenderPropertiesIPv6(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.Listen = "::"
	kc.AdvertiseHost = "fd00::1"

	got, err := RenderProperties(kc)
	if err != nil {
		t.Fatalf("RenderProperties: %v", err)
	}
	for _, want := range []string{
		"listeners=PLAINTEXT://[::]:9092,CONTROLLER://[::]:19093\n",
		"advertised.listeners=PLAINTEXT://[fd00::1]:9092\n",
		"controller.quorum.voters=1@[fd00::1]:19093\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderProperties missing %q:\n%s", want, got)
		}
	}
}

func TestRenderPropertiesRejectsBadInput(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.Settings = map[string]string{"listeners": "PLAINTEXT://0.0.0.0:9092"}
	if _, err := RenderProperties(kc); err == nil || !strings.Contains(err.Error(), "managed by pgcli") {
		t.Errorf("managed setting error = %v", err)
	}

	kc = testKafkaConfig(t)
	kc.Settings = map[string]string{"num.partitions": "3\nnode.id=9"}
	if _, err := RenderProperties(kc); err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Errorf("newline setting error = %v", err)
	}

	kc = testKafkaConfig(t)
	kc.ClusterID = "short"
	if _, err := RenderProperties(kc); err == nil || !strings.Contains(err.Error(), "invalid Kafka cluster id") {
		t.Errorf("bad cluster id error = %v", err)
	}

	kc = testKafkaConfig(t)
	kc.Cluster = "prod"
	kc.Peers = nil
	if _, err := RenderProperties(kc); err == nil || !strings.Contains(err.Error(), "at least one --peer") {
		t.Errorf("missing peers error = %v", err)
	}
}

func TestWriteProperties(t *testing.T) {
	kc := testKafkaConfig(t)
	if err := WriteProperties(kc); err != nil {
		t.Fatalf("WriteProperties: %v", err)
	}
	content, err := os.ReadFile(kc.ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	want, err := RenderProperties(kc)
	if err != nil {
		t.Fatalf("RenderProperties: %v", err)
	}
	if string(content) != want {
		t.Error("written server.properties does not match RenderProperties")
	}
	info, err := os.Stat(kc.ConfigPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("config mode = %o, want 644", perm)
	}

	kc.Settings = map[string]string{"log.dirs": "/tmp"}
	if err := WriteProperties(kc); err == nil {
		t.Error("WriteProperties accepted a managed setting")
	}
}

func writeMetaFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.properties"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadMetaProperties(t *testing.T) {
	dir := t.TempDir()
	meta, err := ReadMetaProperties(dir)
	if err != nil || meta != nil {
		t.Fatalf("missing meta.properties = (%v, %v), want (nil, nil)", meta, err)
	}

	writeMetaFile(t, dir, `# generated by Kafka
version=1
cluster.id=aaaaaaaaaaaaaaaaaaaaaa
node.id=7
directory.id=dir-1
`)
	meta, err = ReadMetaProperties(dir)
	if err != nil {
		t.Fatalf("ReadMetaProperties: %v", err)
	}
	if meta.ClusterID != "aaaaaaaaaaaaaaaaaaaaaa" || !meta.HasNodeID || meta.NodeID != 7 {
		t.Errorf("meta = %+v", meta)
	}
	if meta.DirectoryID != "dir-1" || meta.Version != "1" {
		t.Errorf("meta = %+v", meta)
	}

	badDir := t.TempDir()
	writeMetaFile(t, badDir, "node.id=1\n")
	if _, err := ReadMetaProperties(badDir); err == nil || !strings.Contains(err.Error(), "no cluster.id") {
		t.Errorf("missing cluster.id error = %v", err)
	}

	badNodeDir := t.TempDir()
	writeMetaFile(t, badNodeDir, "cluster.id=aaaaaaaaaaaaaaaaaaaaaa\nnode.id=abc\n")
	if _, err := ReadMetaProperties(badNodeDir); err == nil || !strings.Contains(err.Error(), "parsing node.id") {
		t.Errorf("bad node.id error = %v", err)
	}
}

func TestAdoptMetaData(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.ClusterID = ""
	kc.NodeID = 0
	writeMetaFile(t, kc.DataDir, "cluster.id=bbbbbbbbbbbbbbbbbbbbbb\nnode.id=9\n")
	if err := AdoptMetaData(&kc); err != nil {
		t.Fatalf("AdoptMetaData: %v", err)
	}
	if kc.ClusterID != "bbbbbbbbbbbbbbbbbbbbbb" || kc.NodeID != 9 {
		t.Errorf("adopted = %+v", kc)
	}

	kc = testKafkaConfig(t)
	writeMetaFile(t, kc.DataDir, "cluster.id=bbbbbbbbbbbbbbbbbbbbbb\nnode.id=9\n")
	if err := AdoptMetaData(&kc); err == nil || !strings.Contains(err.Error(), "belongs to Kafka cluster") {
		t.Errorf("cluster mismatch error = %v", err)
	}

	kc = testKafkaConfig(t)
	kc.NodeID = 3
	writeMetaFile(t, kc.DataDir, "cluster.id=aaaaaaaaaaaaaaaaaaaaaa\nnode.id=9\n")
	if err := AdoptMetaData(&kc); err == nil || !strings.Contains(err.Error(), "belongs to Kafka node") {
		t.Errorf("node mismatch error = %v", err)
	}

	kc = testKafkaConfig(t)
	kc.ClusterID = ""
	kc.NodeID = 0
	if err := AdoptMetaData(&kc); err != nil || kc.ClusterID != "" || kc.NodeID != 0 {
		t.Errorf("no meta file should be a no-op, got %+v (%v)", kc, err)
	}
}

func TestCheckMetaData(t *testing.T) {
	kc := testKafkaConfig(t)
	if err := CheckMetaData(kc); err != nil {
		t.Fatalf("missing meta should pass: %v", err)
	}

	writeMetaFile(t, kc.DataDir, "cluster.id=aaaaaaaaaaaaaaaaaaaaaa\nnode.id=1\n")
	if err := CheckMetaData(kc); err != nil {
		t.Fatalf("matching meta should pass: %v", err)
	}

	kc.ClusterID = "bbbbbbbbbbbbbbbbbbbbbb"
	if err := CheckMetaData(kc); err == nil || !strings.Contains(err.Error(), "belongs to Kafka cluster") {
		t.Errorf("cluster mismatch error = %v", err)
	}

	kc = testKafkaConfig(t)
	writeMetaFile(t, kc.DataDir, "cluster.id=aaaaaaaaaaaaaaaaaaaaaa\nnode.id=9\n")
	if err := CheckMetaData(kc); err == nil || !strings.Contains(err.Error(), "belongs to Kafka node") {
		t.Errorf("node mismatch error = %v", err)
	}
}

func TestEndpoints(t *testing.T) {
	kc := testKafkaConfig(t)
	if got := LocalBrokerEndpoint(kc); got != "127.0.0.1:9092" {
		t.Errorf("LocalBrokerEndpoint = %q", got)
	}
	if got := LocalControllerEndpoint(kc); got != "127.0.0.1:19093" {
		t.Errorf("LocalControllerEndpoint = %q", got)
	}
	if got := AdvertisedBrokerEndpoint(kc); got != "127.0.0.1:9092" {
		t.Errorf("AdvertisedBrokerEndpoint = %q", got)
	}

	kc.Listen = "0.0.0.0"
	if got := LocalBrokerEndpoint(kc); got != "127.0.0.1:9092" {
		t.Errorf("wildcard LocalBrokerEndpoint = %q", got)
	}
	kc.Listen = "::"
	if got := LocalBrokerEndpoint(kc); got != "[::1]:9092" {
		t.Errorf("IPv6 wildcard LocalBrokerEndpoint = %q", got)
	}
	kc.Listen = "fd00::1"
	if got := LocalBrokerEndpoint(kc); got != "[fd00::1]:9092" {
		t.Errorf("IPv6 LocalBrokerEndpoint = %q", got)
	}
	kc.AdvertiseHost = "fd00::5"
	if got := AdvertisedBrokerEndpoint(kc); got != "[fd00::5]:9092" {
		t.Errorf("IPv6 AdvertisedBrokerEndpoint = %q", got)
	}
}
