package tlsca

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readLeaf(t *testing.T, dir string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ServerCert))
	if err != nil {
		t.Fatalf("read leaf: %v", err)
	}
	blk, _ := pem.Decode(data)
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return cert
}

func TestGenerateSignsVerifiableChain(t *testing.T) {
	dir := t.TempDir()
	hosts := []string{"localhost", "127.0.0.1", "10.0.0.5", "192.168.10.1"}
	caPath, err := Generate(dir, hosts, time.Hour)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if caPath != filepath.Join(dir, CACertFile) {
		t.Fatalf("ca path = %q", caPath)
	}

	caPEM, _ := os.ReadFile(caPath)
	caBlk, _ := pem.Decode(caPEM)
	pool := x509.NewCertPool()
	caCert, err := x509.ParseCertificate(caBlk.Bytes)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	pool.AddCert(caCert)

	leaf := readLeaf(t, dir)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: pool, DNSName: "localhost", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("leaf does not verify against generated CA: %v", err)
	}
	// Go's x509.Verify does not match SANs (crypto/tls does); check SANs
	// directly: DNS for the name, exact IPs for the addresses.
	var dnsOK, ipOK bool
	for _, d := range leaf.DNSNames {
		if d == "localhost" {
			dnsOK = true
		}
	}
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("10.0.0.5")) {
			ipOK = true
		}
	}
	if !dnsOK || !ipOK {
		t.Fatalf("leaf SANs = dns:%v ip:%v, want localhost + 10.0.0.5", leaf.DNSNames, leaf.IPAddresses)
	}
}

func TestGenerateIdempotentAndSANRenewal(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, []string{"localhost", "127.0.0.1"}, time.Hour); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, ServerCert))

	// Same hosts, still valid -> file untouched.
	if _, err := Generate(dir, []string{"localhost", "127.0.0.1"}, time.Hour); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, ServerCert))
	if string(first) != string(second) {
		t.Fatal("leaf rewritten although nothing changed")
	}

	// A new IP appears (host got a new NIC) -> leaf renewed, CA reused.
	caBefore, _ := os.ReadFile(filepath.Join(dir, CACertFile))
	if _, err := Generate(dir, []string{"localhost", "127.0.0.1", "10.9.8.7"}, time.Hour); err != nil {
		t.Fatalf("third Generate: %v", err)
	}
	leaf := readLeaf(t, dir)
	found := false
	for _, ip := range leaf.IPAddresses {
		if ip.String() == "10.9.8.7" {
			found = true
		}
	}
	if !found {
		t.Fatal("renewed leaf lacks the new IP SAN")
	}
	caAfter, _ := os.ReadFile(filepath.Join(dir, CACertFile))
	if string(caBefore) != string(caAfter) {
		t.Fatal("CA was regenerated — re-trusting it on every consumer would be required")
	}
}

func TestLocalHostsDedup(t *testing.T) {
	hosts := LocalHosts("127.0.0.1", "", "example.local")
	seen := map[string]bool{}
	for _, h := range hosts {
		if seen[h] {
			t.Fatalf("duplicate entry %q in %v", h, hosts)
		}
		seen[h] = true
	}
	for _, want := range []string{"localhost", "127.0.0.1", "::1", "example.local"} {
		if !seen[want] {
			t.Errorf("missing %q in %v", want, hosts)
		}
	}
}
