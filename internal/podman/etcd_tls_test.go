package podman

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func testEtcdManager(t *testing.T) *EtcdManager {
	t.Helper()
	return &EtcdManager{dataDir: t.TempDir()}
}

func TestEnsureMemberTLSCreatesSharedCA(t *testing.T) {
	m := testEtcdManager(t)
	m1 := &config.EtcdConfig{
		Name: "m1", ClusterName: "c1", TLS: true,
		AdvertiseHost: "10.0.0.1", ClientPort: 2379, PeerPort: 2380,
	}
	tls1, err := m.EnsureMemberTLS(m1, true)
	if err != nil {
		t.Fatalf("first member: %v", err)
	}
	clusterCA := filepath.Join(m.ClusterTLSDir(m1), "ca.crt")
	if _, err := os.Stat(clusterCA); err != nil {
		t.Fatalf("cluster CA not promoted: %v", err)
	}
	ca1, err := os.ReadFile(clusterCA)
	if err != nil {
		t.Fatal(err)
	}

	m2 := &config.EtcdConfig{
		Name: "m2", ClusterName: "c1", TLS: true,
		AdvertiseHost: "10.0.0.2", ClientPort: 2381, PeerPort: 2382,
	}
	tls2, err := m.EnsureMemberTLS(m2, true)
	if err != nil {
		t.Fatalf("second member: %v", err)
	}
	ca2, err := os.ReadFile(clusterCA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca1, ca2) {
		t.Error("second member did not reuse the cluster CA")
	}
	if !m.AutoPublishesCA(m1) {
		t.Error("a pgcli-minted CA must be marked for publication into etcd")
	}
	if tls1.CAFile == "" || tls2.CAFile == "" {
		t.Error("CAFile should be set for generated TLS material")
	}

	// Peer mTLS requires the ClientAuth EKU on every member leaf.
	for _, dir := range []string{tls1.MemberDir, tls2.MemberDir} {
		leaf := readLeaf(t, filepath.Join(dir, "public.crt"))
		if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) ||
			!slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
			t.Errorf("%s EKUs = %v, want ServerAuth+ClientAuth", dir, leaf.ExtKeyUsage)
		}
	}
	leaf2 := readLeaf(t, filepath.Join(tls2.MemberDir, "public.crt"))
	if err := leaf2.VerifyHostname("10.0.0.2"); err != nil {
		t.Errorf("leaf SAN does not cover advertise host: %v", err)
	}
	if err := leaf2.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("leaf SAN does not cover loopback: %v", err)
	}
}

func TestEnsureMemberTLSReusesManuallyPlacedCA(t *testing.T) {
	m := testEtcdManager(t)
	ec := &config.EtcdConfig{Name: "m1", ClusterName: "c1", TLS: true, ClientPort: 2379, PeerPort: 2380}
	clusterDir := m.ClusterTLSDir(ec)
	if err := os.MkdirAll(clusterDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Bootstrap a throwaway member to mint a CA pair, then plant it as if an
	// operator had distributed it with `pg cert --ca`.
	seeded := &config.EtcdConfig{Name: "seed", ClusterName: "seed", TLS: true}
	if _, err := m.EnsureMemberTLS(seeded, true); err != nil {
		t.Fatal(err)
	}
	seedDir := filepath.Join(m.ClusterTLSDir(seeded))
	for _, f := range []string{"ca.crt", "ca.key"} {
		data, err := os.ReadFile(filepath.Join(seedDir, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(clusterDir, f), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, _ := os.ReadFile(filepath.Join(clusterDir, "ca.crt"))

	if _, err := m.EnsureMemberTLS(ec, false); err != nil {
		t.Fatalf("reusing manual CA: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(clusterDir, "ca.crt"))
	if !bytes.Equal(want, got) {
		t.Error("manually placed cluster CA was replaced")
	}
	if m.AutoPublishesCA(ec) {
		t.Error("a manually placed CA must not be published into etcd")
	}
	// PublishClusterCA must be a local no-op: no etcd is running here, and a
	// reachable endpoint is 10.0.0.1:2379 only in cloud docs.
	if err := m.PublishClusterCA(ec, false); err != nil {
		t.Fatalf("manual CA publish: %v", err)
	}
}

func TestEnsureMemberTLSRequiresCAWhenNotBootstrapping(t *testing.T) {
	m := testEtcdManager(t)
	ec := &config.EtcdConfig{Name: "m1", ClusterName: "c1", TLS: true, ClientPort: 2379, PeerPort: 2380}
	if _, err := m.EnsureMemberTLS(ec, false); err == nil {
		t.Fatal("expected an error without a CA when allowGenerateCA is false")
	}
}

func TestEtcdRunArgsTLS(t *testing.T) {
	ec := &config.EtcdConfig{
		Name: "m1", ClusterName: "c1",
		ContainerName: "pgcli-etcd-m1", ImageTag: "etcd:test",
		AdvertiseHost: "10.0.0.1", ClientPort: 2379, PeerPort: 2380, TLS: true,
	}
	mounts := []string{"-v", "/tls/m1:/etcd-certs:ro,z"}
	args := etcdRunArgs(ec, "/tmp/data", "m1=https://10.0.0.1:2380", "new", mounts)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"--initial-advertise-peer-urls https://10.0.0.1:2380",
		"--listen-peer-urls https://127.0.0.1:2380,https://10.0.0.1:2380",
		"--advertise-client-urls https://10.0.0.1:2379",
		"--listen-client-urls https://127.0.0.1:2379,https://10.0.0.1:2379",
		"--client-cert-auth=false",
		"--peer-client-cert-auth",
		"--peer-trusted-ca-file /etcd-certs/ca.crt",
		"--experimental-peer-skip-client-san-verification",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q\nargv: %s", want, joined)
		}
	}
	// etcd requires client certs whenever --trusted-ca-file is set, so the
	// client listener must not carry it (auth is username/password).
	if strings.Contains(joined, "--trusted-ca-file") {
		t.Errorf("client listener must not set --trusted-ca-file: %s", joined)
	}

	// Mounts are podman flags; after the image they would be argv for etcd.
	image := slices.Index(args, "etcd:test")
	mount := slices.Index(args, "/tls/m1:/etcd-certs:ro,z")
	if image < 0 || mount < 0 || mount > image {
		t.Errorf("TLS mount must precede the image (image=%d mount=%d): %s", image, mount, joined)
	}
}

func TestEtcdRunArgsPlaintext(t *testing.T) {
	ec := &config.EtcdConfig{
		Name: "m1", ClusterName: "c1",
		ContainerName: "pgcli-etcd-m1", ImageTag: "etcd:test",
		AdvertiseHost: "10.0.0.1", ClientPort: 2379, PeerPort: 2380,
	}
	joined := strings.Join(etcdRunArgs(ec, "/tmp/data", "m1=http://10.0.0.1:2380", "new", nil), " ")
	if strings.Contains(joined, "https://") {
		t.Errorf("plaintext argv contains https URLs: %s", joined)
	}
	if strings.Contains(joined, "--peer-client-cert-auth") {
		t.Errorf("plaintext argv contains TLS flags: %s", joined)
	}
}

func TestEtcdExecCtlArgsCarryCredentialsThroughEnv(t *testing.T) {
	ec := &config.EtcdConfig{
		ContainerName: "pgcli-etcd-m1", ClientPort: 2379, TLS: true,
		Auth: true, AuthUser: "root", AuthPassword: "secret",
	}
	joined := strings.Join(etcdExecCtlArgs(ec), " ")
	if !strings.Contains(joined, "-e ETCDCTL_USER=root:secret") {
		t.Errorf("missing credential env: %s", joined)
	}
	if strings.Contains(joined, "--user") {
		t.Errorf("credentials leaked into etcdctl argv: %s", joined)
	}
	if !strings.Contains(joined, "--cacert=/etcd-certs/ca.crt") {
		t.Errorf("missing CA flag: %s", joined)
	}
}

func readLeaf(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		t.Fatalf("%s: no PEM block", path)
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return cert
}
