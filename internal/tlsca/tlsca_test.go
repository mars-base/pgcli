package tlsca

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// readChain returns every CERTIFICATE block in the server cert file, in order.
func readChain(t *testing.T, dir string) []*pem.Block {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ServerCert))
	if err != nil {
		t.Fatalf("read server cert: %v", err)
	}
	var blocks []*pem.Block
	for rest := data; ; {
		blk, remainder := pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type == "CERTIFICATE" {
			blocks = append(blocks, blk)
		}
		rest = remainder
	}
	return blocks
}

// The server cert is served as a leaf+CA chain so a remote `pg backup fetch-ca`
// can pull the signing root out of the TLS handshake.
func TestGenerateWritesLeafPlusCAChain(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, []string{"localhost", "127.0.0.1"}, time.Hour); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	chain := readChain(t, dir)
	if len(chain) != 2 {
		t.Fatalf("server cert has %d CERTIFICATE blocks, want 2 (leaf+CA)", len(chain))
	}
	leaf, err := x509.ParseCertificate(chain[0].Bytes)
	if err != nil {
		t.Fatalf("parse leaf block: %v", err)
	}
	ca, err := x509.ParseCertificate(chain[1].Bytes)
	if err != nil {
		t.Fatalf("parse CA block: %v", err)
	}
	if !ca.IsCA {
		t.Fatal("second block is not the CA")
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("leaf is not signed by the chained CA: %v", err)
	}
}

// A single-cert public.crt from before chain distribution gets the CA appended
// on the next EnsureTLS without re-signing the leaf (MinIO hot-reloads the
// file; a re-sign would be churn and would not be needed).
func TestEnsureChainUpgradesLegacyLeafOnlyFile(t *testing.T) {
	dir := t.TempDir()
	hosts := []string{"localhost", "127.0.0.1"}
	if _, err := Generate(dir, hosts, time.Hour); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Simulate a legacy deploy: strip the CA block back off.
	legacy := readChain(t, dir)[0]
	leafPath := filepath.Join(dir, ServerCert)
	f, err := os.OpenFile(leafPath, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(f, legacy); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := Generate(dir, hosts, time.Hour); err != nil {
		t.Fatalf("Generate (upgrade pass): %v", err)
	}
	chain := readChain(t, dir)
	if len(chain) != 2 {
		t.Fatalf("legacy file not upgraded: %d blocks, want 2", len(chain))
	}
	upgradedLeaf, err := x509.ParseCertificate(chain[0].Bytes)
	if err != nil {
		t.Fatal(err)
	}
	oldLeaf, err := x509.ParseCertificate(legacy.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !upgradedLeaf.Equal(oldLeaf) {
		t.Fatal("leaf was re-signed during the chain upgrade — should have been kept")
	}

	// Running the upgrade again must not append a second CA block.
	if err := ensureChain(leafPath, filepath.Join(dir, CACertFile)); err != nil {
		t.Fatalf("ensureChain repeat: %v", err)
	}
	if got := len(readChain(t, dir)); got != 2 {
		t.Fatalf("ensureChain is not idempotent: %d blocks after a repeat call", got)
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

func TestGenerateMutualLeafHasClientAuth(t *testing.T) {
	dir := t.TempDir()
	if _, err := GenerateMutual(dir, []string{"localhost", "127.0.0.1"}, time.Hour); err != nil {
		t.Fatalf("GenerateMutual: %v", err)
	}
	leaf := readLeaf(t, dir)
	for _, want := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if !slices.Contains(leaf.ExtKeyUsage, want) {
			t.Fatalf("leaf EKUs = %v, missing %v", leaf.ExtKeyUsage, want)
		}
	}

	caPEM, _ := os.ReadFile(filepath.Join(dir, CACertFile))
	blk, _ := pem.Decode(caPEM)
	ca, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("leaf does not verify as a client cert: %v", err)
	}
}

// writeECDSAKeyCA plants a `pg cert --ca`-style CA pair (ECDSA P-256, PKCS#8
// key) in dir, restricted to ekus when non-nil.
func writeECDSAKeyCA(t *testing.T, dir string, ekus []x509.ExtKeyUsage) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: "pgcli cert", Organization: []string{"pgcli cert"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           ekus,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, CACertFile), caCertPEM, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, CAKeyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return caCertPEM
}

// `pg cert --ca` writes an ECDSA P-256 key in PKCS#8; GenerateMutual must
// adopt such a CA pair in place instead of erroring or minting a new one.
func TestGenerateMutualAdoptsECDSAKeyCA(t *testing.T) {
	dir := t.TempDir()
	caCertPEM := writeECDSAKeyCA(t, dir, nil)

	if _, err := GenerateMutual(dir, []string{"localhost", "127.0.0.1"}, time.Hour); err != nil {
		t.Fatalf("GenerateMutual with ECDSA CA: %v", err)
	}
	gotCA, _ := os.ReadFile(filepath.Join(dir, CACertFile))
	if !bytes.Equal(gotCA, caCertPEM) {
		t.Fatal("pre-placed ECDSA CA was replaced")
	}
	caBlk, _ := pem.Decode(caCertPEM)
	caCert, err := x509.ParseCertificate(caBlk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leaf := readLeaf(t, dir)
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("leaf not signed by the pre-placed CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	for _, eku := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{eku}}); err != nil {
			t.Errorf("leaf does not verify for %v: %v", eku, err)
		}
	}
}

// CAs minted by older `pg cert --ca` releases carry ServerAuth EKU, which Go's
// verifier applies to client-auth chains too. Fail with the fix instead of
// signing a leaf that etcd peers will reject at handshake time.
func TestGenerateMutualRejectsServerOnlyCA(t *testing.T) {
	dir := t.TempDir()
	writeECDSAKeyCA(t, dir, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	_, err := GenerateMutual(dir, []string{"localhost"}, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "restricts extended key usage") {
		t.Fatalf("want EKU-restriction error, got %v", err)
	}
}

func TestGenerateMutualRenewsServerOnlyLeaf(t *testing.T) {
	dir := t.TempDir()
	hosts := []string{"localhost", "127.0.0.1"}
	if _, err := Generate(dir, hosts, time.Hour); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	caBefore, _ := os.ReadFile(filepath.Join(dir, CACertFile))

	if _, err := GenerateMutual(dir, hosts, time.Hour); err != nil {
		t.Fatalf("GenerateMutual: %v", err)
	}
	leaf := readLeaf(t, dir)
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Fatalf("server-only leaf was not renewed with ClientAuth: %v", leaf.ExtKeyUsage)
	}
	caAfter, _ := os.ReadFile(filepath.Join(dir, CACertFile))
	if string(caBefore) != string(caAfter) {
		t.Fatal("CA must be reused when upgrading the leaf EKU")
	}
}
