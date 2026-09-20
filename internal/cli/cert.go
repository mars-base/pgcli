package cli

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/certgen"
)

func init() {
	certCmd.Flags().StringSliceVar(&certHost, "host", []string{"127.0.0.1"}, "comma-separated hostnames and IPs for the certificate SAN (IPs are detected automatically); repeatable")
	certCmd.Flags().StringVar(&certCertFile, "cert-file", "cert.pem", "path to write the PEM certificate")
	certCmd.Flags().StringVar(&certKeyFile, "key-file", "key.pem", "path to write the PEM private key (PKCS8, mode 0600)")
	certCmd.Flags().DurationVar(&certValid, "valid-duration", 825*24*time.Hour, "how long the certificate stays valid (e.g. 720h)")
	certCmd.Flags().BoolVar(&certCA, "ca", false, "issue a self-signed CA (CA:TRUE, keyCertSign) instead of a server leaf — do not feed this to a MinIO/silo --tls-cert")
	certCmd.Flags().IntVar(&certRSA, "rsa", 0, "RSA key size in bits (0 = use ECDSA)")
	certCmd.Flags().StringVar(&certECDSA, "ecdsa", "P-256", "ECDSA curve (P-224, P-256, P-384, P-521)")
	rootCmd.AddCommand(certCmd)
}

var (
	certHost     []string
	certCertFile string
	certKeyFile  string
	certValid    time.Duration
	certCA       bool
	certRSA      int
	certECDSA    string
)

var certCmd = &cobra.Command{
	Use:   "cert",
	Short: "Generate a self-signed certificate (domain + IP SANs)",
	Long: `cert mints a self-signed certificate whose SubjectAltNames cover any mix of
DNS names and IPs you give it — for anywhere you need TLS without a CA: a dev
or test server, an internal endpoint, a service whose clients you control. It
is also the pair MinIO's (and silo's) bring-your-own-TLS mode consumes directly:

  pg cert --host minio.test,127.0.0.1,10.0.0.9 --cert-file minio.crt --key-file minio.key
  pg addon install minio --name store --tls-cert minio.crt --tls-key minio.key
  pg addon install silo --name store --tls-cert minio.crt --tls-key minio.key

The result is a single self-signed LEAF (CA:FALSE, serverAuth EKU) that is its
own trust anchor: hand the same .crt to any TLS client that can point at a CA
file. For pgBackRest's S3 path that means
backup.repo.s3.ca_file / pg backup setup --s3-ca-file minio.crt.

Nothing is registered with pgcli: this just writes the two files you name and
prints the SANs. It does not touch pg.yaml or start a container.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if certRSA <= 0 && certECDSA == "" {
			return fmt.Errorf("no key requested: pass --rsa <bits> or --ecdsa <curve>")
		}

		res, err := certgen.Generate(certgen.Options{
			Hosts:      certHost,
			Valid:      certValid,
			CA:         certCA,
			RSABits:    certRSA,
			ECDSACurve: certECDSA,
		})
		if err != nil {
			return err
		}

		if err := os.WriteFile(certCertFile, res.CertPEM, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", certCertFile, err)
		}
		if err := os.WriteFile(certKeyFile, res.KeyPEM, 0600); err != nil {
			return fmt.Errorf("writing %s: %w", certKeyFile, err)
		}

		fmt.Printf("[OK] Wrote %s and %s\n", certCertFile, certKeyFile)
		fmt.Printf("  Valid until: %s\n", res.NotAfter.Format(time.RFC3339))
		if certCA {
			fmt.Println("  Type: self-signed CA (not for --tls-cert)")
		} else {
			fmt.Println("  Type: self-signed leaf (usable as --tls-cert and as the client ca_file)")
		}
		if len(res.DNSNames) > 0 {
			fmt.Printf("  DNS SAN:  %s\n", strings.Join(res.DNSNames, ", "))
		}
		if len(res.IPs) > 0 {
			fmt.Printf("  IP SAN:   %s\n", joinIPs(res.IPs))
		}
		return nil
	},
}

func joinIPs(ips []net.IP) string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return strings.Join(out, ", ")
}
