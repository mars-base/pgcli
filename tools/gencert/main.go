// Command gencert creates a self-signed certificate with SubjectAltNames for
// domains and/or IPs — the thin CLI front for internal/certgen (the same logic
// `pg cert` uses). See that package's doc for the certificate's properties.
//
// Build:  make gencert   →  bin/gencert
// Usage:  ./bin/gencert -host minio.test,10.0.0.1 -cert-file minio.crt -key-file minio.key
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/certgen"
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

	res, err := certgen.Generate(certgen.Options{
		Hosts:      []string{*host},
		Valid:      *validDuration,
		CA:         *isCA,
		RSABits:    *rsaBits,
		ECDSACurve: *ecdsaCurve,
	})
	if err != nil {
		log.Fatalf("gencert: %v", err)
	}

	if err := os.WriteFile(*certFile, res.CertPEM, 0644); err != nil {
		log.Fatalf("writing %s: %v", *certFile, err)
	}
	if err := os.WriteFile(*keyFile, res.KeyPEM, 0600); err != nil {
		log.Fatalf("writing %s: %v", *keyFile, err)
	}

	log.Printf("wrote %s and %s (valid until %s)", *certFile, *keyFile, res.NotAfter.UTC().Format(time.RFC3339))
	if len(res.DNSNames) > 0 {
		log.Printf("  DNS SAN: %s", strings.Join(res.DNSNames, ", "))
	}
	if len(res.IPs) > 0 {
		log.Printf("  IP SAN:  %s", joinIPs(res.IPs))
	}
}

func joinIPs(ips []net.IP) string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return strings.Join(out, ", ")
}
