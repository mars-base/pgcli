package certgen

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSplitHosts(t *testing.T) {
	tests := []struct {
		name    string
		hosts   []string
		wantIPs []string
		wantDNS []string
	}{
		{"empty yields loopback default", nil, []string{"127.0.0.1"}, nil},
		{"mixed dns and v4/v6", []string{"minio.test,127.0.0.1,::1"}, []string{"127.0.0.1", "::1"}, []string{"minio.test"}},
		{"repeatable flags re-split on comma", []string{"a.test,b.test", "10.0.0.1"}, []string{"10.0.0.1"}, []string{"a.test", "b.test"}},
		{"whitespace and empty entries dropped", []string{" a.test , , 10.0.0.9 "}, []string{"10.0.0.9"}, []string{"a.test"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ips, dns := splitHosts(tc.hosts)
			if len(ips) != len(tc.wantIPs) {
				t.Fatalf("got %d ips, want %d: %v", len(ips), len(tc.wantIPs), ips)
			}
			for i, ip := range ips {
				if !ip.Equal(net.ParseIP(tc.wantIPs[i])) {
					t.Errorf("ip[%d] = %v, want %v", i, ip, tc.wantIPs[i])
				}
			}
			if len(dns) != len(tc.wantDNS) {
				t.Fatalf("got dns %v, want %v", dns, tc.wantDNS)
			}
			for i, d := range dns {
				if d != tc.wantDNS[i] {
					t.Errorf("dns[%d] = %q, want %q", i, d, tc.wantDNS[i])
				}
			}
		})
	}
}

func TestGenerateLeaf(t *testing.T) {
	res, err := Generate(Options{Hosts: []string{"minio.test,127.0.0.1,10.0.0.9"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.CertPEM) == 0 || len(res.KeyPEM) == 0 {
		t.Fatal("empty PEM output")
	}
	// Exactly one certificate block: this is a single leaf, not a chain.
	if n := countBlocks(res.CertPEM); n != 1 {
		t.Fatalf("cert has %d CERTIFICATE blocks, want 1 (self-signed leaf, no chain)", n)
	}

	der, _ := pem.Decode(res.CertPEM)
	leaf, err := x509.ParseCertificate(der.Bytes)
	if err != nil {
		t.Fatalf("parsing cert: %v", err)
	}
	if leaf.IsCA {
		t.Error("default leaf must not be a CA")
	}
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("leaf missing DigitalSignature usage")
	}
	if leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Error("leaf must not have CertSign (that would let it sign others)")
	}
	if !slices.Contains(leaf.DNSNames, "minio.test") {
		t.Errorf("DNS SAN missing minio.test: %v", leaf.DNSNames)
	}
	if !containsIP(leaf.IPAddresses, net.ParseIP("10.0.0.9")) {
		t.Errorf("IP SAN missing 10.0.0.9: %v", leaf.IPAddresses)
	}
	if res.NotAfter.Before(time.Now().Add(824 * 24 * time.Hour)) {
		t.Errorf("NotAfter %v is short of the 825-day default", res.NotAfter)
	}
}

func TestGenerateCA(t *testing.T) {
	res, err := Generate(Options{Hosts: []string{"myca.test"}, CA: true})
	if err != nil {
		t.Fatalf("Generate CA: %v", err)
	}
	der, _ := pem.Decode(res.CertPEM)
	crt, err := x509.ParseCertificate(der.Bytes)
	if err != nil {
		t.Fatalf("parsing CA cert: %v", err)
	}
	if !crt.IsCA {
		t.Error("CA:true must set the CA flag")
	}
	if crt.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA missing CertSign usage")
	}
}

func TestGenerateRSA(t *testing.T) {
	res, err := Generate(Options{Hosts: []string{"x.test"}, RSABits: 2048})
	if err != nil {
		t.Fatalf("Generate RSA: %v", err)
	}
	der, _ := pem.Decode(res.CertPEM)
	crt, _ := x509.ParseCertificate(der.Bytes)
	if crt.PublicKeyAlgorithm != x509.RSA {
		t.Errorf("--rsa must yield an RSA public key, got %v", crt.PublicKeyAlgorithm)
	}
}

// TestGenerateLoadableByTLSLoader mirrors what podman.ValidateBYOCert does:
// the emitted pair must load through crypto/tls as a matched cert+key, and the
// self-signed leaf must validate as its own trust anchor (the S3 ca_file use).
func TestGenerateLoadableByTLSLoader(t *testing.T) {
	res, err := Generate(Options{Hosts: []string{"minio.test,127.0.0.1"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "public.crt")
	keyPath := filepath.Join(dir, "private.key")
	if err := os.WriteFile(certPath, res.CertPEM, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, res.KeyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("pair must load via tls.LoadX509KeyPair: %v", err)
	}

	// Self-anchor: build a pool whose only root is this cert and verify it.
	der, _ := pem.Decode(res.CertPEM)
	leaf, _ := x509.ParseCertificate(der.Bytes)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   "minio.test",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("self-signed leaf must verify against itself as its root (the --s3-ca-file case): %v", err)
	}
}

func countBlocks(pemBytes []byte) int {
	n := 0
	for rest := pemBytes; ; {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type == "CERTIFICATE" {
			n++
		}
	}
	return n
}

func containsIP(ips []net.IP, want net.IP) bool {
	for _, ip := range ips {
		if ip.Equal(want) {
			return true
		}
	}
	return false
}
