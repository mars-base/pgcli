package kafka

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// testPEM returns a combined certificate+key PEM. With pkcs1 the key is
// written as "RSA PRIVATE KEY" (the format tlsca uses), otherwise as an EC
// PKCS#8 "PRIVATE KEY" (the format pg cert uses).
func testPEM(t *testing.T, notBefore, notAfter time.Time, isCA, pkcs1 bool) []byte {
	t.Helper()
	var priv any
	var pub any
	if pkcs1 {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		priv, pub = key, &key.PublicKey
	} else {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		priv, pub = key, &key.PublicKey
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kafka.test"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		DNSNames:              []string{"kafka.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	if pkcs1 {
		if err := pem.Encode(&b, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv.(*rsa.PrivateKey))}); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(&b, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writePEM(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

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
	kc.ClusterID = config.DeriveKafkaClusterID(kc.Cluster)
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

func TestRenderPropertiesTwoVoterCluster(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.Cluster = "pair"
	kc.ClusterID = config.DeriveKafkaClusterID(kc.Cluster)
	kc.Listen = "0.0.0.0"
	kc.AdvertiseHost = "10.0.0.1"
	kc.Peers = []string{"2@10.0.0.2:19093"}

	got, err := RenderProperties(kc)
	if err != nil {
		t.Fatalf("RenderProperties: %v", err)
	}
	for _, want := range []string{
		"controller.quorum.voters=1@10.0.0.1:19093,2@10.0.0.2:19093\n",
		"offsets.topic.replication.factor=2\n",
		"transaction.state.log.min.isr=2\n",
		"default.replication.factor=2\n",
		"min.insync.replicas=2\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderProperties missing %q:\n%s", want, got)
		}
	}
}

func TestRenderPropertiesClusterTLS(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.Cluster = "prod"
	kc.ClusterID = config.DeriveKafkaClusterID(kc.Cluster)
	kc.Listen = "0.0.0.0"
	kc.AdvertiseHost = "10.241.20.147"
	kc.Peers = []string{"2@10.241.20.148:19093", "3@10.241.20.153:19093"}
	kc.TLS = true
	kc.TLSPort = 9093

	got, err := RenderProperties(kc)
	if err != nil {
		t.Fatalf("RenderProperties: %v", err)
	}
	for _, want := range []string{
		"listeners=PLAINTEXT://0.0.0.0:9092,SSL://0.0.0.0:9093,CONTROLLER://0.0.0.0:19093\n",
		"advertised.listeners=PLAINTEXT://10.241.20.147:9092,SSL://10.241.20.147:9093\n",
		"listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,SSL:SSL\n",
		"ssl.keystore.location=/mnt/shared/config/tls/server.pem\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderProperties missing %q:\n%s", want, got)
		}
	}

	// Internal traffic is not TLS even when the client listener is.
	for _, want := range []string{
		"inter.broker.listener.name=PLAINTEXT\n",
		"controller.listener.names=CONTROLLER\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderProperties missing %q:\n%s", want, got)
		}
	}
}

func TestRenderPropertiesRejectsMismatchedClusterID(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.Cluster = "prod"
	kc.ClusterID = config.DeriveKafkaClusterID("other")
	kc.Listen = "0.0.0.0"
	kc.AdvertiseHost = "10.0.0.1"
	kc.Peers = []string{"2@10.0.0.2:19093"}

	if _, err := RenderProperties(kc); err == nil || !strings.Contains(err.Error(), "does not match cluster") {
		t.Fatalf("mismatched cluster id error = %v", err)
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

func TestRenderPropertiesTLS(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.TLS = true
	kc.TLSPort = 9093

	got, err := RenderProperties(kc)
	if err != nil {
		t.Fatalf("RenderProperties: %v", err)
	}
	for _, want := range []string{
		"listeners=PLAINTEXT://127.0.0.1:9092,SSL://127.0.0.1:9093,CONTROLLER://127.0.0.1:19093\n",
		"advertised.listeners=PLAINTEXT://127.0.0.1:9092,SSL://127.0.0.1:9093\n",
		"listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,SSL:SSL\n",
		"ssl.keystore.type=PEM\n",
		"ssl.keystore.location=/mnt/shared/config/tls/server.pem\n",
		"ssl.client.auth=none\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderProperties missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"ssl.keystore.password", "ssl.key.password", "SASL"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("TLS properties unexpectedly contain %q:\n%s", unwanted, got)
		}
	}
}

func TestEnsureTLSCerts(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.TLS = true
	kc.TLSPort = 9093

	caPath, err := EnsureTLSCerts(kc)
	if err != nil {
		t.Fatalf("EnsureTLSCerts: %v", err)
	}
	if caPath != TLSCAPath(kc) {
		t.Errorf("CA path = %q, want %q", caPath, TLSCAPath(kc))
	}
	dir := TLSDir(kc)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat TLS dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o755 {
		t.Errorf("TLS dir mode = %o, want 755", perm)
	}
	configInfo, err := os.Stat(filepath.Dir(dir))
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := configInfo.Mode().Perm(); perm != 0o755 {
		t.Errorf("config dir mode = %o, want 755", perm)
	}
	caInfo, err := os.Stat(caPath)
	if err != nil {
		t.Fatalf("stat CA: %v", err)
	}
	if perm := caInfo.Mode().Perm(); perm != 0o644 {
		t.Errorf("CA mode = %o, want 644", perm)
	}
	serverPEM := filepath.Join(dir, ServerPEMFile)
	pemInfo, err := os.Stat(serverPEM)
	if err != nil {
		t.Fatalf("stat server.pem: %v", err)
	}
	if perm := pemInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("server.pem mode = %o, want 600", perm)
	}
	body, err := os.ReadFile(serverPEM)
	if err != nil {
		t.Fatalf("read server.pem: %v", err)
	}
	if !strings.Contains(string(body), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("server.pem is missing the certificate:\n%s", body)
	}
	if strings.Contains(string(body), "BEGIN RSA PRIVATE KEY") {
		t.Errorf("server.pem must carry a PKCS#8 key, got PKCS#1:\n%s", body)
	}
	// Kafka's PEM keystore rejects PKCS#1 with "algid parse error, not a
	// sequence"; make sure the embedded key parses as PKCS#8.
	var keyBlock *pem.Block
	for rest := body; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			keyBlock = block
		}
	}
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		t.Fatalf("server.pem has no PKCS#8 private key block:\n%s", body)
	}
	if _, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err != nil {
		t.Errorf("embedded key is not valid PKCS#8: %v", err)
	}

	if _, err := EnsureTLSCerts(kc); err != nil {
		t.Errorf("EnsureTLSCerts is not idempotent: %v", err)
	}
}

func TestEnsureTLSCertsClusterSAN(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.Cluster = "prod"
	kc.ClusterID = config.DeriveKafkaClusterID(kc.Cluster)
	kc.NodeID = 2
	kc.Listen = "0.0.0.0"
	kc.AdvertiseHost = "10.241.20.148"
	kc.Peers = []string{"1@10.241.20.147:19093", "3@10.241.20.153:19093"}
	kc.TLS = true
	kc.TLSPort = 9093

	if _, err := EnsureTLSCerts(kc); err != nil {
		t.Fatalf("EnsureTLSCerts: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(TLSDir(kc), ServerPEMFile))
	if err != nil {
		t.Fatalf("read server.pem: %v", err)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		t.Fatal("server.pem has no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse server certificate: %v", err)
	}
	want := net.ParseIP(kc.AdvertiseHost)
	for _, ip := range cert.IPAddresses {
		if ip.Equal(want) {
			return
		}
	}
	t.Errorf("cluster certificate SANs %v do not include the advertise host %s", cert.IPAddresses, kc.AdvertiseHost)
}

func TestEnsureTLSCertsDisabled(t *testing.T) {
	kc := testKafkaConfig(t)
	caPath, err := EnsureTLSCerts(kc)
	if err != nil || caPath != "" {
		t.Errorf("EnsureTLSCerts without TLS = (%q, %v), want (\"\", nil)", caPath, err)
	}
	if _, err := os.Stat(TLSDir(kc)); !os.IsNotExist(err) {
		t.Errorf("TLS dir should not exist when TLS is off: %v", err)
	}
}

func TestAdvertisedTLSEndpoint(t *testing.T) {
	kc := testKafkaConfig(t)
	if got := AdvertisedTLSEndpoint(kc); got != "" {
		t.Errorf("AdvertisedTLSEndpoint without TLS = %q, want empty", got)
	}
	kc.TLS = true
	kc.TLSPort = 9093
	if got := AdvertisedTLSEndpoint(kc); got != "127.0.0.1:9093" {
		t.Errorf("AdvertisedTLSEndpoint = %q", got)
	}
	kc.AdvertiseHost = "fd00::5"
	if got := AdvertisedTLSEndpoint(kc); got != "[fd00::5]:9093" {
		t.Errorf("IPv6 AdvertisedTLSEndpoint = %q", got)
	}
}

func TestEnsureTLSCertsBroughtOwnPEM(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.TLS = true
	kc.TLSPort = 9093
	now := time.Now()
	body := testPEM(t, now.Add(-time.Hour), now.Add(24*time.Hour), false, false)
	kc.TLSPEM = writePEM(t, t.TempDir(), "kafka-server.pem", body)

	caPath, err := EnsureTLSCerts(kc)
	if err != nil {
		t.Fatalf("EnsureTLSCerts: %v", err)
	}
	if caPath != "" {
		t.Errorf("BYO TLS returned CA path %q, want empty", caPath)
	}
	serverPEM := filepath.Join(TLSDir(kc), ServerPEMFile)
	got, err := os.ReadFile(serverPEM)
	if err != nil {
		t.Fatalf("read server.pem: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("server.pem differs from the supplied PEM:\n%s", got)
	}
	info, err := os.Stat(serverPEM)
	if err != nil {
		t.Fatalf("stat server.pem: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("server.pem mode = %o, want 600", perm)
	}
	if _, err := os.Stat(TLSCAPath(kc)); !os.IsNotExist(err) {
		t.Errorf("BYO TLS should not generate ca.crt: %v", err)
	}

	ci, err := ValidateTLSPEM(kc.TLSPEM)
	if err != nil {
		t.Fatalf("ValidateTLSPEM: %v", err)
	}
	for _, host := range []string{"127.0.0.1", "::1", "kafka.test"} {
		if !ci.CoversHost(host) {
			t.Errorf("CoversHost(%q) = false", host)
		}
	}
	if ci.CoversHost("10.0.0.9") {
		t.Error("CoversHost accepted an address missing from the SANs")
	}
	if !ci.SelfSigned {
		t.Error("self-signed test certificate reported as issued by another CA")
	}
}

func TestEnsureTLSCertsBroughtOwnPKCS1(t *testing.T) {
	kc := testKafkaConfig(t)
	kc.TLS = true
	kc.TLSPort = 9093
	now := time.Now()
	kc.TLSPEM = writePEM(t, t.TempDir(), "kafka-server.pem",
		testPEM(t, now.Add(-time.Hour), now.Add(24*time.Hour), false, true))

	if _, err := EnsureTLSCerts(kc); err != nil {
		t.Fatalf("EnsureTLSCerts with PKCS#1 key: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(TLSDir(kc), ServerPEMFile))
	if err != nil {
		t.Fatalf("read server.pem: %v", err)
	}
	if strings.Contains(string(got), "BEGIN RSA PRIVATE KEY") {
		t.Errorf("server.pem kept the PKCS#1 key:\n%s", got)
	}
	block, _ := pem.Decode(got)
	if block == nil {
		t.Fatal("server.pem has no PEM block")
	}
	// Find the key block (after the certificate) and prove it is PKCS#8.
	_, rest := pem.Decode(got)
	keyBlock, _ := pem.Decode(rest)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		t.Fatalf("expected a PKCS#8 PRIVATE KEY block, got %+v", keyBlock)
	}
	if _, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err != nil {
		t.Errorf("normalized key is not PKCS#8: %v", err)
	}
}

func TestValidateTLSPEMErrors(t *testing.T) {
	now := time.Now()
	good := testPEM(t, now.Add(-time.Hour), now.Add(24*time.Hour), false, false)
	other := testPEM(t, now.Add(-time.Hour), now.Add(24*time.Hour), false, false)

	certBlock, rest := pem.Decode(good)
	otherCert, _ := pem.Decode(other)
	onlyCert := pem.EncodeToMemory(certBlock)
	mismatched := append(pem.EncodeToMemory(otherCert), rest...)

	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"no key", onlyCert, "no private key"},
		{"no cert", rest, "no CERTIFICATE"},
		{"mismatched key", mismatched, "do not pair"},
		{"expired", testPEM(t, now.Add(-48*time.Hour), now.Add(-24*time.Hour), false, false), "expired"},
		{"not yet valid", testPEM(t, now.Add(24*time.Hour), now.Add(48*time.Hour), false, false), "not valid yet"},
		{"CA certificate", testPEM(t, now.Add(-time.Hour), now.Add(24*time.Hour), true, false), "CA certificate"},
		{"encrypted key", append(append([]byte{}, onlyCert...), pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("x")})...), "encrypted private keys"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writePEM(t, t.TempDir(), "bad.pem", tc.body)
			if _, err := ValidateTLSPEM(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateTLSPEM error = %v, want substring %q", err, tc.want)
			}
		})
	}
}
