// Package tlsca generates self-signed CA + leaf certificates for pgcli's
// TLS-terminated addons (MinIO today). pgBackRest forces HTTPS for S3
// repositories and there is no public CA for a private MinIO, so pgcli plays
// CA itself: the leaf ships SANs for every address the MinIO is reachable at,
// and consumers (pgBackRest via repo*-s3-ca-file) get the ca.crt to verify
// against — certificate verification stays ON.
//
// The server cert (public.crt) is written as a leaf+CA chain so the TLS server
// presents the root in its handshake: `pg backup fetch-ca` on a remote host
// dials the endpoint and pulls the CA back out of the peer chain, closing the
// last gap where getting ca.crt off the storage host used to be a manual scp.
package tlsca

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// File names under the generated dir. ServerCert/ServerKey match what MinIO's
// --certs-dir expects; the CA pair is what consumers are pointed at.
const (
	CACertFile = "ca.crt"
	CAKeyFile  = "ca.key"
	ServerCert = "public.crt"
	ServerKey  = "private.key"

	// Same 100 years as the CA: the CA/Browser Forum's 825-day cap on leaf
	// validity is a policy for publicly-trusted CAs, not a limit Go's x509
	// verifier enforces — a pgcli-private cert is never seen by a browser that
	// would apply it. Longevity costs nothing here: renewing for a changed SAN
	// (hostSANsMissing) is what absorbs address drift, independent of expiry.
	// A short leaf would only add a silent-failure mode — a container left
	// untouched past its expiry starts failing archive-push with no pgcli run
	// to renew it.
	leafValidity = 100 * 365 * 24 * time.Hour
	// 100 years: the CA is pgcli's private root for a loopback-adjacent
	// service, not a public PKI artifact — a short CA would force every client
	// that received ca.crt to re-distribute it long before the deployment
	// retires. Leaf rotation (leafValidity + auto-renew) is where address
	// changes are absorbed, so CA longevity costs nothing operationally.
	caValidity = 100 * 365 * 24 * time.Hour
)

// Generate creates a self-signed CA (when absent) and a leaf cert for the
// given hosts (DNS names and/or IPs), writing them into dir. An existing leaf
// is renewed only when missing, unparsable, within renewBefore of expiry, or
// lacking a now-required SAN. Returns the CA cert path.
func Generate(dir string, hosts []string, renewBefore time.Duration) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("creating cert dir: %w", err)
	}
	caCertPath := filepath.Join(dir, CACertFile)
	leafPath := filepath.Join(dir, ServerCert)

	need, err := leafNeedsRenewal(leafPath, hosts, renewBefore)
	if err != nil {
		return "", err
	}
	if !need {
		// Existing leaf (possibly from before the chain was served): add the CA
		// block in place. MinIO watches the cert files and hot-reloads, so no
		// restart is needed and the leaf does not have to be re-signed.
		if err := ensureChain(leafPath, caCertPath); err != nil {
			return "", err
		}
		return caCertPath, nil
	}

	caKey, caCert, err := loadOrCreateCA(filepath.Join(dir, CAKeyFile), caCertPath)
	if err != nil {
		return "", err
	}
	if err := writeLeaf(dir, caKey, caCert, hosts); err != nil {
		return "", err
	}
	return caCertPath, nil
}

// leafNeedsRenewal reports whether the leaf at leafPath must be rewritten.
func leafNeedsRenewal(leafPath string, hosts []string, renewBefore time.Duration) (bool, error) {
	data, err := os.ReadFile(leafPath)
	if os.IsNotExist(err) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("reading leaf cert: %w", err)
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		return true, nil
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return true, nil
	}
	if time.Until(cert.NotAfter) < renewBefore {
		return true, nil
	}
	return hostSANsMissing(cert, hosts), nil
}

// hostSANsMissing returns true when some requested host is absent from the
// cert's IP or DNS SANs.
func hostSANsMissing(cert *x509.Certificate, hosts []string) bool {
	wantIP := map[string]bool{}
	for _, ip := range cert.IPAddresses {
		wantIP[ip.String()] = true
	}
	wantDNS := map[string]bool{}
	for _, d := range cert.DNSNames {
		wantDNS[d] = true
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			if !wantIP[ip.String()] {
				return true
			}
		} else if !wantDNS[h] {
			return true
		}
	}
	return false
}

// loadOrCreateCA reads the CA material from disk, generating and persisting a
// fresh 4096-bit CA when either file is absent.
func loadOrCreateCA(keyPath, certPath string) (*rsa.PrivateKey, *x509.Certificate, error) {
	keyPEM, kerr := os.ReadFile(keyPath)
	certPEM, cerr := os.ReadFile(certPath)
	if kerr == nil && cerr == nil {
		kb, _ := pem.Decode(keyPEM)
		cb, _ := pem.Decode(certPEM)
		if kb != nil && cb != nil {
			key, err := x509.ParsePKCS1PrivateKey(kb.Bytes)
			if err == nil {
				cert, err := x509.ParseCertificate(cb.Bytes)
				if err == nil {
					return key, cert, nil
				}
			}
		}
		return nil, nil, fmt.Errorf("CA files exist but are unparsable: %s / %s", keyPath, certPath)
	}
	if !os.IsNotExist(kerr) && kerr != nil {
		return nil, nil, fmt.Errorf("reading CA key: %w", kerr)
	}
	if !os.IsNotExist(cerr) && cerr != nil {
		return nil, nil, fmt.Errorf("reading CA cert: %w", cerr)
	}

	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, nil, fmt.Errorf("generating CA key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "pgcli self-signed CA", Organization: []string{"pgcli"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("self-signing CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	if err := writePEM(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), 0600); err != nil {
		return nil, nil, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0644); err != nil {
		return nil, nil, err
	}
	return key, cert, nil
}

// writeLeaf issues a server cert for hosts signed by the CA into dir.
func writeLeaf(dir string, caKey *rsa.PrivateKey, caCert *x509.Certificate, hosts []string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generating leaf key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: firstHost(hosts), Organization: []string{"pgcli"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("signing leaf cert: %w", err)
	}
	if err := writePEM(filepath.Join(dir, ServerKey), "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), 0600); err != nil {
		return err
	}
	if err := writePEM(filepath.Join(dir, ServerCert), "CERTIFICATE", der, 0644); err != nil {
		return err
	}
	// Chain order leaf-first (RFC 4346 "certificate_sequence"): the TLS stack
	// sends every block, so a remote `pg backup fetch-ca` sees the signing root.
	return appendCAChain(filepath.Join(dir, ServerCert), filepath.Join(dir, CACertFile))
}

// ensureChain appends the CA block to the leaf file when it is not already
// present, upgrading a single-cert file from before chains were served. It is
// a no-op when the CA is already chained or the CA file is absent (a leaf
// without a ca.crt alongside is not this package's doing).
func ensureChain(leafPath, caCertPath string) error {
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil // no CA on disk: nothing to append
	}
	ca, _ := pem.Decode(caPEM)
	if ca == nil || ca.Type != "CERTIFICATE" {
		return fmt.Errorf("CA cert %s is not a valid PEM certificate", caCertPath)
	}
	data, err := os.ReadFile(leafPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Generate regenerates from scratch; nothing to upgrade
		}
		return fmt.Errorf("reading leaf cert: %w", err)
	}
	for rest := data; ; {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type == "CERTIFICATE" && bytes.Equal(blk.Bytes, ca.Bytes) {
			return nil // already chained
		}
	}
	return appendCAChain(leafPath, caCertPath)
}

// appendCAChain appends ca.crt's PEM block to leafPath (creating no file when
// the CA is missing). Idempotency is the caller's: see ensureChain.
func appendCAChain(leafPath, caCertPath string) error {
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading CA cert: %w", err)
	}
	blk, _ := pem.Decode(caPEM)
	if blk == nil {
		return fmt.Errorf("CA cert %s is not valid PEM", caCertPath)
	}
	f, err := os.OpenFile(leafPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("opening leaf cert: %w", err)
	}
	defer f.Close()
	if err := pem.Encode(f, blk); err != nil {
		return fmt.Errorf("appending CA to %s: %w", leafPath, err)
	}
	return nil
}

// LocalHosts returns the SAN set a loopback/host-networked service on this
// machine is reachable at: loopback names plus every NIC's IP addresses
// (containers run with host networking, so peers dial the NIC IP). extra
// (e.g. a configured bind address or advertise host) is appended deduped.
func LocalHosts(extra ...string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if ifaddrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifaddrs {
			if n, ok := a.(*net.IPNet); ok {
				hosts = append(hosts, n.IP.String())
			}
		}
	}
	hosts = append(hosts, extra...)
	seen := map[string]bool{}
	out := hosts[:0]
	for _, h := range hosts {
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: typ, Bytes: der})
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	return n
}

func firstHost(hosts []string) string {
	if len(hosts) > 0 {
		return hosts[0]
	}
	return "localhost"
}
