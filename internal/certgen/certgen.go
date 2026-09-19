// Package certgen mints self-signed certificates whose SubjectAltNames cover a
// mix of DNS names and IPs — the cert/key pair pgcli's MinIO BYO mode
// (`pg addon install minio --tls-cert ... --tls-key ...`) consumes, and any
// spot where a quick, purpose-built test cert is wanted.
//
// It is adapted from the Go standard library's crypto/tls/generate_cert.go:
// same PEM output, with an IP-address-aware SAN split so Hosts mixes DNS names
// and IPs (e.g. "minio.test", "127.0.0.1", "::1"). The result is a single
// self-signed LEAF (IsCA false, serverAuth EKU) that is its own trust anchor;
// set CA:true only when a private root is wanted instead.
package certgen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

// Options configures a single Generate call. Zero values take the defaults
// noted on each field.
type Options struct {
	Hosts      []string      // DNS names and/or IPs for the SAN; defaults to 127.0.0.1 when empty
	Valid      time.Duration // validity window from now; defaults to 825 days
	CA         bool          // issue a self-signed CA (CA:TRUE, keyCertSign) instead of a leaf
	RSABits    int           // when > 0, generate an RSA key of this size instead of ECDSA
	ECDSACurve string        // P-224|P-256|P-384|P-521; defaults to P-256 when RSABits == 0
}

// Result is the freshly minted pair, plus the SAN split so callers can report
// exactly what the certificate covers.
type Result struct {
	CertPEM  []byte
	KeyPEM   []byte
	DNSNames []string
	IPs      []net.IP
	NotAfter time.Time
}

// Generate builds a key and a self-signed certificate per opts, returning both
// as PEM. At least one of RSABits / ECDSACurve must effectively select a key
// (the defaults satisfy this).
func Generate(opts Options) (*Result, error) {
	priv, err := newKey(opts)
	if err != nil {
		return nil, err
	}

	ips, dns := splitHosts(opts.Hosts)

	valid := opts.Valid
	if valid <= 0 {
		valid = 825 * 24 * time.Hour
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"pgcli cert"},
		},
		NotBefore:             time.Now().Add(-time.Hour), // absorb mild clock skew
		NotAfter:              time.Now().Add(valid),
		KeyUsage:              keyUsage(opts.CA),
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  opts.CA,
		IPAddresses:           ips,
		DNSNames:              dns,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey(priv), priv)
	if err != nil {
		return nil, fmt.Errorf("creating certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshaling private key: %w", err)
	}

	return &Result{
		CertPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}),
		KeyPEM:   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		DNSNames: dns,
		IPs:      ips,
		NotAfter: template.NotAfter,
	}, nil
}

// newKey builds the requested private key: RSA when RSABits > 0, otherwise the
// named ECDSA curve (default P-256).
func newKey(opts Options) (any, error) {
	if opts.RSABits > 0 {
		return rsa.GenerateKey(rand.Reader, opts.RSABits)
	}
	var curve elliptic.Curve
	switch strings.ToUpper(opts.ECDSACurve) {
	case "P-224":
		curve = elliptic.P224()
	case "P-256", "":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported ECDSA curve %q (want P-224, P-256, P-384, P-521)", opts.ECDSACurve)
	}
	return ecdsa.GenerateKey(curve, rand.Reader)
}

func publicKey(priv any) any {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	default:
		return nil
	}
}

func keyUsage(isCA bool) x509.KeyUsage {
	ku := x509.KeyUsageDigitalSignature
	if isCA {
		return ku | x509.KeyUsageCertSign
	}
	return ku | x509.KeyUsageKeyEncipherment
}

// splitHosts partitions hosts into IPs and DNS names. Empty entries are
// dropped; a nil/empty input yields the 127.0.0.1 default so a bare
// `pg cert`/`gencert` still produces something usable.
func splitHosts(hosts []string) ([]net.IP, []string) {
	var ips []net.IP
	var dns []string
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1"}
	}
	for _, h := range hosts {
		for part := range strings.SplitSeq(h, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if ip := net.ParseIP(part); ip != nil {
				ips = append(ips, ip)
				continue
			}
			dns = append(dns, part)
		}
	}
	if len(ips) == 0 && len(dns) == 0 {
		ips = append(ips, net.ParseIP("127.0.0.1"))
	}
	return ips, dns
}
