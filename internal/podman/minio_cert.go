package podman

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// Bring-your-own certificates: instead of the pgcli-generated self-signed pair
// (internal/tlsca), the operator can point an addon at an existing cert/key —
// a public-CA domain cert, an internal-CA one, whatever. MinIO wants exactly
// public.crt + private.key inside --certs-dir, so the two user files are
// bind-mounted read-only at those names (the repo's mount-in-place pattern,
// like the backup container's ca.crt) and tlsca is skipped entirely — no
// copying a private key into the base dir, no regeneration over it.

// BYOTLS reports whether mc is configured to serve a user-provided cert pair.
// A half-configured pair (cert without key) is not BYO: callers fall back to
// generated certs and say so.
func BYOTLS(mc *config.MinioConfig) bool {
	return mc.TLS && mc.CertFile != "" && mc.KeyFile != ""
}

// tlsMountFlags returns the podman volume args that deliver cert material to
// /opt/minio/certs: the two user files mounted at the names MinIO requires in
// BYO mode, else the generated TLSDir as a whole.
func tlsMountFlags(mc *config.MinioConfig, tlsDir string) []string {
	if BYOTLS(mc) {
		return []string{
			"-v", fmt.Sprintf("%s:/opt/minio/certs/public.crt:ro,z", hostMountPath(mc.CertFile)),
			"-v", fmt.Sprintf("%s:/opt/minio/certs/private.key:ro,z", hostMountPath(mc.KeyFile)),
		}
	}
	if tlsDir == "" {
		return nil
	}
	return []string{"-v", fmt.Sprintf("%s:/opt/minio/certs:ro,z", hostMountPath(tlsDir))}
}

// CertInfo is the operator-facing summary of a BYO certificate.
type CertInfo struct {
	Subject    string
	Issuer     string
	DNSNames   []string
	IPs        []string
	NotBefore  time.Time
	NotAfter   time.Time
	SelfSigned bool
}

// ValidateBYOCert checks that certPath/keyPath exist, parse, pair, and could
// actually serve TLS: a leaf (not a CA), unexpired at this instant, and — when
// it pins extended key usage — allowed for server auth. It pairs the files
// through tls.LoadX509KeyPair so a mismatched key is caught here, at install,
// rather than by a container that would crash-loop on a handshake failure.
func ValidateBYOCert(certPath, keyPath string) (*CertInfo, error) {
	if _, err := os.Stat(certPath); err != nil {
		return nil, fmt.Errorf("certificate file: %w", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("cert/key pair unusable: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parsing leaf certificate: %w", err)
	}
	if leaf.IsCA {
		return nil, fmt.Errorf("%s is a CA certificate; MinIO needs the server leaf (its private key cannot sign anything)", certPath)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) {
		return nil, fmt.Errorf("certificate not yet valid (starts %s)", leaf.NotBefore.Format(time.RFC3339))
	}
	if now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("certificate expired %s", leaf.NotAfter.Format(time.RFC3339))
	}
	if len(leaf.ExtKeyUsage) > 0 {
		ok := false
		for _, ku := range leaf.ExtKeyUsage {
			if ku == x509.ExtKeyUsageServerAuth || ku == x509.ExtKeyUsageAny {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("certificate is not valid for server authentication (ExtKeyUsage %v)", leaf.ExtKeyUsage)
		}
	}
	ips := make([]string, 0, len(leaf.IPAddresses))
	for _, ip := range leaf.IPAddresses {
		ips = append(ips, ip.String())
	}
	return &CertInfo{
		Subject:    leaf.Subject.CommonName,
		Issuer:     leaf.Issuer.CommonName,
		DNSNames:   leaf.DNSNames,
		IPs:        ips,
		NotBefore:  leaf.NotBefore,
		NotAfter:   leaf.NotAfter,
		SelfSigned: string(leaf.RawSubject) == string(leaf.RawIssuer),
	}, nil
}

// CertCoversHost reports whether host (bare, or host:port, IP or DNS name)
// matches the certificate's SANs (falling back to CN only when no DNS SANs
// exist, which is what strict verifiers do anyway). A "*.foo" wildcard matches
// exactly one label, never "a.b.foo".
func CertCoversHost(ci *CertInfo, host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimSuffix(host, "."), ":")
	if ip := net.ParseIP(host); ip != nil {
		for _, s := range ci.IPs {
			if s == ip.String() {
				return true
			}
		}
		return false
	}
	names := ci.DNSNames
	if len(names) == 0 && ci.Subject != "" {
		names = []string{ci.Subject}
	}
	for _, n := range names {
		if matchDNSName(strings.ToLower(n), strings.ToLower(host)) {
			return true
		}
	}
	return false
}

func matchDNSName(pattern, host string) bool {
	if pattern == host {
		return true
	}
	if prefix, ok := strings.CutPrefix(pattern, "*."); ok && strings.HasSuffix(host, "."+prefix) {
		rest := strings.TrimSuffix(host, "."+prefix)
		return rest != "" && !strings.Contains(rest, ".")
	}
	return false
}
