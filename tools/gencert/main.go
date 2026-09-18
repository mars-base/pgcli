// Command gencert creates a self-signed certificate with SubjectAltNames for
// domains and/or IPs — the cert/key pair pgcli's MinIO BYO mode
// (`pg addon install minio --tls-cert ... --tls-key ...`) consumes, and any
// other spot where a quick, purpose-built test cert is wanted.
//
// It is a trimmed adaptation of the Go standard library's
// crypto/tls/generate_cert.go: same flag surface and PEM output, minus the
// retired ed25519 path, with an IP-address-aware SAN split so `-host` accepts
// a comma-separated mix of DNS names and IPs (e.g. "minio.test,127.0.0.1,::1").
//
// Build:  make gencert   →  bin/gencert
// Usage:  ./bin/gencert -host minio.test,10.0.0.1 -cert-file minio.crt -key-file minio.key
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		host          = flag.String("host", "127.0.0.1", "comma-separated hostnames and IPs to encode in the certificate (SAN); IPs are detected automatically")
		certFile      = flag.String("cert-file", "cert.pem", "filename of the PEM certificate to write")
		keyFile       = flag.String("key-file", "key.pem", "filename of the PEM private key to write")
		validDuration = flag.Duration("valid-duration", 825*24*time.Hour, "duration for which the certificate is valid (e.g. 720h, 87600h)")
		isCA          = flag.Bool("ca", false, "whether this cert should be its own Certificate Authority")
		rsaBits       = flag.Int("rsa", 0, "size in bits of RSA key to generate (e.g. 2048, 4096); 0 disables RSA")
		ecdsaCurve    = flag.String("ecdsa", "P-256", "ECDSA curve to use (P-224, P-256, P-384, P-521); empty disables ECDSA")
	)
	flag.Parse()

	if *rsaBits <= 0 && *ecdsaCurve == "" {
		log.Fatalf("no key requested: set -rsa <bits> or -ecdsa <curve>")
	}

	priv, err := newKey(*rsaBits, *ecdsaCurve)
	if err != nil {
		log.Fatalf("generating key: %v", err)
	}

	ips, dns := splitHosts(*host)

	notAfter := time.Now().Add(*validDuration)
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		log.Fatalf("generating serial number: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"pgcli gencert"},
		},
		NotBefore:             time.Now(),
		NotAfter:              notAfter,
		KeyUsage:              keyUsage(*isCA),
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
		DNSNames:              dns,
	}
	if *isCA {
		template.IsCA = true
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey(priv), priv)
	if err != nil {
		log.Fatalf("creating certificate: %v", err)
	}

	if err := writePEM(*certFile, "CERTIFICATE", derBytes, 0644); err != nil {
		log.Fatalf("writing %s: %v", *certFile, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		log.Fatalf("marshaling private key: %v", err)
	}
	if err := writePEM(*keyFile, "PRIVATE KEY", keyDER, 0600); err != nil {
		log.Fatalf("writing %s: %v", *keyFile, err)
	}

	log.Printf("wrote %s and %s (valid until %s)", *certFile, *keyFile, notAfter.UTC().Format(time.RFC3339))
	if len(dns) > 0 {
		log.Printf("  DNS SAN: %s", strings.Join(dns, ", "))
	}
	if len(ips) > 0 {
		log.Printf("  IP SAN:  %s", joinIPs(ips))
	}
}

// newKey builds the requested private key: RSA when -rsa is set, otherwise the
// named ECDSA curve (the default path).
func newKey(rsaBits int, curveName string) (any, error) {
	if rsaBits > 0 {
		return rsa.GenerateKey(rand.Reader, rsaBits)
	}
	var curve elliptic.Curve
	switch strings.ToUpper(curveName) {
	case "P-224":
		curve = elliptic.P224()
	case "P-256", "":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported ECDSA curve %q (want P-224, P-256, P-384, P-521)", curveName)
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

// splitHosts partitions a comma-separated -host value into IPs and DNS names.
// Empty entries are dropped; a bare hostname still lands in DNS names.
func splitHosts(host string) ([]net.IP, []string) {
	var ips []net.IP
	var dns []string
	for h := range strings.SplitSeq(host, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
			continue
		}
		dns = append(dns, h)
	}
	return ips, dns
}

func joinIPs(ips []net.IP) string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return strings.Join(out, ", ")
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: typ, Bytes: der})
}
