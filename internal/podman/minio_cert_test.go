package podman

import (
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
	"github.com/mars-base/pgcli/internal/tlsca"
)

// genSelfSignedPair writes a self-signed cert/key into dir and returns their
// paths. notAfter/notBefore let a test build an expired or not-yet-valid leaf;
// dnsNames/IPs set the SANs; withCA flips the IsCA bit; eku pins ExtKeyUsage
// (empty = none, matching what browsers/Go default to server-auth for).
func genSelfSignedPair(t *testing.T, dir string, cn string, dnsNames []string, ips []string, notBefore, notAfter time.Time, withCA bool, eku []x509.ExtKeyUsage) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
		IsCA:                  withCA,
		DNSNames:              dnsNames,
	}
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath := filepath.Join(dir, cn+".crt")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyPath := filepath.Join(dir, cn+".key")
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func TestTLSMountFlags(t *testing.T) {
	t.Run("BYO mounts two files, not the dir", func(t *testing.T) {
		got := tlsMountFlags(true, "/etc/certs/pub.crt", "/etc/certs/priv.key", "/base/tls/minio/x", minioCertsDir)
		want := []string{
			"-v", "/etc/certs/pub.crt:/opt/minio/certs/public.crt:ro,z",
			"-v", "/etc/certs/priv.key:/opt/minio/certs/private.key:ro,z",
		}
		if len(got) != len(want) {
			t.Fatalf("got %d args %q, want %d %q", len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("arg %d = %q, want %q", i, got[i], want[i])
			}
		}
		for _, a := range got {
			if strings.Contains(a, "/base/tls/minio/x") {
				t.Fatalf("BYO mode must not mount the generated TLSDir: %q", a)
			}
		}
	})
	t.Run("silo uses its own certs dir", func(t *testing.T) {
		got := tlsMountFlags(true, "/etc/certs/pub.crt", "/etc/certs/priv.key", "/base/tls/silo/x", siloCertsDir)
		want := []string{
			"-v", "/etc/certs/pub.crt:/opt/silo/certs/public.crt:ro,z",
			"-v", "/etc/certs/priv.key:/opt/silo/certs/private.key:ro,z",
		}
		if len(got) != len(want) {
			t.Fatalf("got %q, want %q", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("arg %d = %q, want %q", i, got[i], want[i])
			}
		}
	})
	t.Run("generated mode mounts the dir", func(t *testing.T) {
		got := tlsMountFlags(true, "", "", "/base/tls/minio/x", minioCertsDir)
		want := []string{"-v", "/base/tls/minio/x:/opt/minio/certs:ro,z"}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("empty tlsDir with no TLS means no flags", func(t *testing.T) {
		if got := tlsMountFlags(false, "", "", "", minioCertsDir); got != nil {
			t.Fatalf("got %q, want nil", got)
		}
	})
}

func TestValidateBYOCert(t *testing.T) {
	// tlsca produces a self-signed CA + leaf (public.crt/private.key). The leaf
	// is the valid BYO pair; the CA is the "cert is a CA" rejection case.
	dir := t.TempDir()
	caPath, err := tlsca.Generate(dir, []string{"minio.test", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatalf("tlsca.Generate: %v", err)
	}
	leafCert := filepath.Join(dir, tlsca.ServerCert)
	leafKey := filepath.Join(dir, tlsca.ServerKey)
	_ = caPath

	t.Run("valid leaf pair", func(t *testing.T) {
		ci, err := ValidateBYOCert(leafCert, leafKey)
		if err != nil {
			t.Fatalf("ValidateBYOCert: %v", err)
		}
		if ci.SelfSigned {
			t.Fatalf("tlsca leaf is issued by a private CA, not self-signed")
		}
	})

	t.Run("rejects mismatched key", func(t *testing.T) {
		// Pair the leaf cert with a fresh, unrelated key.
		_, otherKey := genSelfSignedPair(t, dir, "other", nil, nil,
			time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false,
			[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
		if _, err := ValidateBYOCert(leafCert, otherKey); err == nil {
			t.Fatalf("expected mismatched-key error, got nil")
		}
	})

	t.Run("rejects CA cert", func(t *testing.T) {
		caKey := filepath.Join(dir, tlsca.CAKeyFile)
		if _, err := ValidateBYOCert(filepath.Join(dir, tlsca.CACertFile), caKey); err == nil {
			t.Fatalf("expected IsCA rejection, got nil")
		}
	})

	t.Run("rejects expired", func(t *testing.T) {
		c, k := genSelfSignedPair(t, dir, "expired", []string{"expired.test"}, nil,
			time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour), false,
			[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
		if _, err := ValidateBYOCert(c, k); err == nil {
			t.Fatalf("expected expired error, got nil")
		}
	})

	t.Run("rejects non-server-auth EKU", func(t *testing.T) {
		c, k := genSelfSignedPair(t, dir, "clientonly", []string{"clientonly.test"}, nil,
			time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), false,
			[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
		if _, err := ValidateBYOCert(c, k); err == nil {
			t.Fatalf("expected EKU rejection, got nil")
		}
	})
}

func TestCertCoversHost(t *testing.T) {
	ci := &CertInfo{
		DNSNames: []string{"minio.test", "*.hi.163.com"},
		IPs:      []string{"127.0.0.1", "10.241.21.97"},
	}
	cases := []struct {
		host string
		want bool
	}{
		{"minio.test", true},
		{"MINIO.test", true},        // case-insensitive
		{"minio.test:9000", true},   // host:port stripped
		{"other.test", false},       // not a SAN
		{"a.hi.163.com", true},      // single-label wildcard
		{"a.b.hi.163.com", false},   // wildcard is one label only
		{"hi.163.com", false},       // bare parent doesn't match *.child
		{"127.0.0.1", true},         // IP SAN
		{"10.241.21.97:9000", true}, // IP SAN with port
		{"8.8.8.8", false},          // IP not in SANs
	}
	for _, tc := range cases {
		if got := CertCoversHost(ci, tc.host); got != tc.want {
			t.Errorf("CertCoversHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}

	t.Run("CN fallback when no DNS SANs", func(t *testing.T) {
		cnOnly := &CertInfo{Subject: "cn.only"}
		if !CertCoversHost(cnOnly, "cn.only") {
			t.Fatalf("expected CN fallback to match cn.only")
		}
		if CertCoversHost(cnOnly, "other") {
			t.Fatalf("CN fallback should not match unrelated host")
		}
	})
}

func TestEnsureTLSBYONeverTouchesTLSDir(t *testing.T) {
	dir := t.TempDir()
	// A real self-signed leaf pair to point at.
	certPath, keyPath := genSelfSignedPair(t, dir, "byo", []string{"minio.test"}, []string{"127.0.0.1"},
		time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), false,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})

	m := &MinioManager{dataDir: dir, podman: "/bin/true"}
	mc := &config.MinioConfig{
		Name: "byo", ContainerName: "pgcli-minio-byo",
		TLS: true, CertFile: certPath, KeyFile: keyPath, Listen: "0.0.0.0", APIPort: 9000,
	}
	caPath, err := m.EnsureTLS(mc)
	if err != nil {
		t.Fatalf("EnsureTLS: %v", err)
	}
	if caPath != "" {
		t.Fatalf("BYO must return an empty CA path, got %q", caPath)
	}
	if _, err := os.Stat(m.TLSDir(mc)); !os.IsNotExist(err) {
		t.Fatalf("BYO mode must not create the generated TLSDir %s (stat err=%v)", m.TLSDir(mc), err)
	}
}

func TestBYOCertConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := &config.Config{BaseDir: t.TempDir()}
	cfg.Addons.Minio = map[string]config.MinioConfig{
		"byo": {Name: "byo", TLS: true, CertFile: "/etc/certs/pub.crt", KeyFile: "/etc/certs/priv.key"},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := got.Addons.Minio["byo"]
	if m.CertFile != "/etc/certs/pub.crt" || m.KeyFile != "/etc/certs/priv.key" {
		t.Fatalf("round-trip lost fields: cert=%q key=%q", m.CertFile, m.KeyFile)
	}

	// Clearing the pair drops both keys (omitempty) and they read back empty.
	cfg.Addons.Minio["byo"] = config.MinioConfig{Name: "byo", TLS: true}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save cleared: %v", err)
	}
	got2, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load cleared: %v", err)
	}
	m2 := got2.Addons.Minio["byo"]
	if m2.CertFile != "" || m2.KeyFile != "" {
		t.Fatalf("cleared pair should read empty: cert=%q key=%q", m2.CertFile, m2.KeyFile)
	}
}
