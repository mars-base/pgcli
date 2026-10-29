package rabbitmq

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func testRabbitMQ(t *testing.T) config.RabbitMQConfig {
	t.Helper()
	dir := t.TempDir()
	return config.RabbitMQConfig{
		ContainerName:     "pgcli-rabbitmq-test",
		Name:              "test",
		Version:           config.DefaultRabbitMQVersion,
		ImageTag:          config.DefaultRabbitMQImageTag,
		ConfigPath:        filepath.Join(dir, "config", "rabbitmq.conf"),
		DataDir:           filepath.Join(dir, "data"),
		SecretPath:        filepath.Join(dir, "secrets", "admin_password"),
		Listen:            "127.0.0.1",
		AMQPPort:          5672,
		ManagementPort:    15672,
		EPMDPort:          4369,
		DistPort:          25672,
		AdminUser:         "admin",
		AdminPassword:     "secret",
		AMQPTLSPort:       5671,
		ManagementTLSPort: 15671,
	}
}

func TestRenderConfigPlaintext(t *testing.T) {
	rc := testRabbitMQ(t)
	got, err := RenderConfig(rc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	for _, want := range []string{
		"listeners.tcp.default = 127.0.0.1:5672\n",
		"management.tcp.ip = 127.0.0.1\n",
		"management.tcp.port = 15672\n",
		"default_user_tags.administrator = true\n",
		"default_permissions.configure = .*\n",
		"distribution.listener.interface = 127.0.0.1\n",
		"distribution.listener.port_range.min = 25672\n",
		"distribution.listener.port_range.max = 25672\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderConfig missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"listeners.ssl.default",
		"management.ssl.port",
		"ssl_options.",
		"default_pass",
		"default_user =",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("RenderConfig unexpectedly contains %q:\n%s", unwanted, got)
		}
	}
}

func TestRenderConfigTLS(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.TLS = true
	got, err := RenderConfig(rc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	for _, want := range []string{
		"listeners.ssl.default = 127.0.0.1:5671\n",
		"ssl_options.cacertfile = /etc/rabbitmq/tls/ca.crt\n",
		"ssl_options.certfile = /etc/rabbitmq/tls/public.crt\n",
		"ssl_options.keyfile = /etc/rabbitmq/tls/private.key\n",
		"ssl_options.verify = verify_none\n",
		"ssl_options.fail_if_no_peer_cert = false\n",
		"management.ssl.port = 15671\n",
		"management.ssl.cacertfile = /etc/rabbitmq/tls/ca.crt\n",
		"management.ssl.verify = verify_none\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderConfig TLS missing %q:\n%s", want, got)
		}
	}
}

func TestRenderConfigSettingsSorted(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.Settings = map[string]string{
		"log.file.level":   "debug",
		"channel_max":      "2047",
		"consumer_timeout": "1800000",
		"heartbeat":        "60",
	}
	got, err := RenderConfig(rc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	order := []string{
		"channel_max = 2047\n",
		"consumer_timeout = 1800000\n",
		"heartbeat = 60\n",
		"log.file.level = debug\n",
	}
	last := -1
	for _, key := range order {
		idx := strings.Index(got, key)
		if idx < 0 {
			t.Fatalf("RenderConfig missing %q:\n%s", key, got)
		}
		if idx < last {
			t.Errorf("setting %q is not in sorted order:\n%s", key, got)
		}
		last = idx
	}
}

func TestRenderConfigRejectsManagedSetting(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.Settings = map[string]string{"management.tcp.port": "9999"}
	if _, err := RenderConfig(rc); err == nil || !strings.Contains(err.Error(), "managed by pgcli") {
		t.Fatalf("RenderConfig = %v, want managed-key error", err)
	}
}

func TestWritePlugins(t *testing.T) {
	rc := testRabbitMQ(t)
	if err := WritePlugins(rc); err != nil {
		t.Fatalf("WritePlugins: %v", err)
	}
	data, err := os.ReadFile(PluginsFile(rc))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[rabbitmq_management].\n" {
		t.Errorf("enabled_plugins = %q", data)
	}
}

func TestWriteSecret(t *testing.T) {
	rc := testRabbitMQ(t)
	if err := WriteSecret(rc); err != nil {
		t.Fatalf("WriteSecret: %v", err)
	}
	data, err := os.ReadFile(rc.SecretPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != rc.AdminPassword {
		t.Errorf("secret = %q, want %q", data, rc.AdminPassword)
	}
	info, err := os.Stat(rc.SecretPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("secret mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteErlangCookie(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.Cluster = "lab"
	rc.ErlangCookie = "shared-cookie"
	if err := WriteErlangCookie(rc); err != nil {
		t.Fatalf("WriteErlangCookie: %v", err)
	}
	data, err := os.ReadFile(CookiePath(rc))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shared-cookie\n" {
		t.Errorf("cookie = %q, want %q", data, "shared-cookie\n")
	}
	info, err := os.Stat(CookiePath(rc))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Errorf("cookie mode = %o, want 400", info.Mode().Perm())
	}

	// A rotated cookie rewrites the file even though the old one is 0400.
	rc.ErlangCookie = "rotated-cookie"
	if err := WriteErlangCookie(rc); err != nil {
		t.Fatalf("WriteErlangCookie (rotate): %v", err)
	}
	data, err = os.ReadFile(CookiePath(rc))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rotated-cookie\n" {
		t.Errorf("rotated cookie = %q", data)
	}
}

func TestWriteErlangCookieStandaloneNoop(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.ErlangCookie = "shared-cookie"
	if err := WriteErlangCookie(rc); err != nil {
		t.Fatalf("WriteErlangCookie: %v", err)
	}
	if _, err := os.Stat(CookiePath(rc)); !os.IsNotExist(err) {
		t.Errorf("standalone instance must not get a cookie file, stat err = %v", err)
	}
}

func TestEnsureTLSCertsGenerated(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.TLS = true
	if err := EnsureTLSCerts(rc); err != nil {
		t.Fatalf("EnsureTLSCerts: %v", err)
	}
	for _, name := range []string{"ca.crt", "ca.key", "public.crt", "private.key"} {
		if _, err := os.Stat(filepath.Join(TLSDir(rc), name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(TLSDir(rc), "public.crt"))
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		t.Fatal("public.crt is not PEM")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !certHasIP(cert, "127.0.0.1") || !certHasIP(cert, "::1") {
		t.Errorf("generated certificate SANs = IP %v, DNS %v; want loopback IPs", cert.IPAddresses, cert.DNSNames)
	}
	// The CA is chained after the leaf so clients can build the chain.
	rest := data[len(pem.EncodeToMemory(&pem.Block{Type: blk.Type, Bytes: blk.Bytes})):]
	if !strings.Contains(string(rest), "CERTIFICATE") {
		t.Errorf("public.crt does not include the CA chain")
	}
}

func TestEnsureTLSCertsBYO(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.TLS = true
	dir := t.TempDir()
	rc.CertFile = filepath.Join(dir, "mine.crt")
	rc.KeyFile = filepath.Join(dir, "mine.key")
	certPEM := []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")
	keyPEM := []byte("-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----\n")
	if err := os.WriteFile(rc.CertFile, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rc.KeyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTLSCerts(rc); err != nil {
		t.Fatalf("EnsureTLSCerts: %v", err)
	}
	gotCert, err := os.ReadFile(filepath.Join(TLSDir(rc), "public.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCert) != string(certPEM) {
		t.Errorf("public.crt = %q, want the supplied certificate", gotCert)
	}
	gotCA, err := os.ReadFile(filepath.Join(TLSDir(rc), "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCA) != string(certPEM) {
		t.Errorf("ca.crt = %q, want the supplied certificate", gotCA)
	}
	gotKey, err := os.ReadFile(filepath.Join(TLSDir(rc), "private.key"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotKey) != string(keyPEM) {
		t.Errorf("private.key = %q, want the supplied key", gotKey)
	}
	info, err := os.Stat(filepath.Join(TLSDir(rc), "private.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("private.key mode = %o, want 600", info.Mode().Perm())
	}
}

func TestEndpoints(t *testing.T) {
	rc := testRabbitMQ(t)
	if got := AMQPAddr(rc); got != "127.0.0.1:5672" {
		t.Errorf("AMQPAddr = %q", got)
	}
	if got := ManagementURL(rc); got != "http://127.0.0.1:15672" {
		t.Errorf("ManagementURL = %q", got)
	}
	if got := AMQPSAddr(rc); got != "" {
		t.Errorf("AMQPSAddr without TLS = %q, want empty", got)
	}
	if got := ManagementTLSURL(rc); got != "" {
		t.Errorf("ManagementTLSURL without TLS = %q, want empty", got)
	}
	rc.TLS = true
	if got := AMQPSAddr(rc); got != "127.0.0.1:5671" {
		t.Errorf("AMQPSAddr = %q", got)
	}
	if got := ManagementTLSURL(rc); got != "https://127.0.0.1:15671" {
		t.Errorf("ManagementTLSURL = %q", got)
	}
}

func TestRenderConfigCluster(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.Cluster = "lab"
	rc.NodeName = "rabbit@node1"
	rc.Listen = "0.0.0.0"
	rc.EtcdEndpoints = []string{"http://10.0.0.2:2379", "10.0.0.1:2379"}
	got, err := RenderConfig(rc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	for _, want := range []string{
		"listeners.tcp.default = 0.0.0.0:5672\n",
		"management.tcp.ip = 0.0.0.0\n",
		"distribution.listener.interface = 0.0.0.0\n",
		"cluster_formation.peer_discovery_backend = etcd\n",
		"cluster_formation.etcd.endpoints.1 = 10.0.0.1:2379\n",
		"cluster_formation.etcd.endpoints.2 = 10.0.0.2:2379\n",
		"cluster_formation.etcd.cluster_name = lab\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cluster RenderConfig missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"erlang_cookie", "default_pass", "secret"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("cluster RenderConfig leaked %q:\n%s", unwanted, got)
		}
	}
}

func TestRenderConfigStandaloneHasNoClusterFormation(t *testing.T) {
	rc := testRabbitMQ(t)
	got, err := RenderConfig(rc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if strings.Contains(got, "cluster_formation") {
		t.Errorf("standalone RenderConfig contains cluster_formation:\n%s", got)
	}
}

func TestWritePluginsCluster(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.Cluster = "lab"
	rc.NodeName = "rabbit@node1"
	rc.EtcdEndpoints = []string{"10.0.0.1:2379"}
	if err := WritePlugins(rc); err != nil {
		t.Fatalf("WritePlugins: %v", err)
	}
	data, err := os.ReadFile(PluginsFile(rc))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[rabbitmq_management,rabbitmq_peer_discovery_etcd].\n" {
		t.Errorf("enabled_plugins = %q", data)
	}
}

func TestEndpointsClusterUseNodeHost(t *testing.T) {
	rc := testRabbitMQ(t)
	rc.Cluster = "lab"
	rc.NodeName = "rabbit@10.241.20.147"
	rc.Listen = "0.0.0.0"
	rc.TLS = true
	if got := AMQPAddr(rc); got != "10.241.20.147:5672" {
		t.Errorf("AMQPAddr = %q, want the node host, not 0.0.0.0", got)
	}
	if got := AMQPSAddr(rc); got != "10.241.20.147:5671" {
		t.Errorf("AMQPSAddr = %q", got)
	}
	if got := ManagementURL(rc); got != "http://10.241.20.147:15672" {
		t.Errorf("ManagementURL = %q", got)
	}
	if got := ManagementTLSURL(rc); got != "https://10.241.20.147:15671" {
		t.Errorf("ManagementTLSURL = %q", got)
	}
	rc.NodeName = "rabbit@node1"
	if got := AdvertiseHost(rc); got != "node1" {
		t.Errorf("AdvertiseHost = %q, want node1", got)
	}
}

func certHasIP(cert *x509.Certificate, want string) bool {
	for _, ip := range cert.IPAddresses {
		if ip.Equal(net.ParseIP(want)) {
			return true
		}
	}
	return false
}
