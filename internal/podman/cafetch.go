package podman

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"net"
	"strings"
	"time"
)

// caFetchDialTimeout bounds the whole dial+handshake: an unreachable storage
// host should fail fast, not hang a setup run.
const caFetchDialTimeout = 5 * time.Second

// FetchRepoCA dials a TLS S3 endpoint and returns the self-signed CA that
// signed its leaf, PEM-encoded, taken from the server's certificate chain.
//
// This closes the first hop of cross-host backup trust: pgcli's MinIO addon
// serves a leaf+CA chain (internal/tlsca), so a Patroni host that has never
// seen ca.crt can pull it with one command instead of an scp from the storage
// host. Everything after that is already automated — `pg backup setup`
// publishes the pulled CA to the cluster's etcd registry for the remaining
// hosts.
//
// The connection is trust-on-first-use: the CA we are fetching is the trust
// anchor, so it cannot also be the thing we verify against. The command-side
// output prints a fingerprint so an operator can cross-check it against the
// storage host (sha256sum ca.crt) the way one checks an SSH host key.
func FetchRepoCA(endpoint string) (string, error) {
	hostport := NormalizeS3Endpoint(endpoint)
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: caFetchDialTimeout},
		"tcp", hostport,
		&tls.Config{InsecureSkipVerify: true}, //nolint:gosec // TOFU: fetching the anchor to verify against
	)
	if err != nil {
		return "", fmt.Errorf("connecting to %s over TLS: %w (an S3 endpoint must serve HTTPS — pgBackRest refuses plaintext S3; for pgcli's MinIO that is `pg addon install minio --tls`)", hostport, err)
	}
	defer conn.Close()

	chain := conn.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return "", fmt.Errorf("%s presented no certificate", hostport)
	}
	leaf := chain[0]
	for _, cert := range chain[1:] {
		if !cert.IsCA || !bytes.Equal(cert.RawSubject, cert.RawIssuer) {
			continue // not self-signed: an intermediate, or not ours
		}
		if err := leaf.CheckSignatureFrom(cert); err != nil {
			continue // self-signed but did not sign the leaf: not its anchor
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})), nil
	}
	return "", fmt.Errorf("%s did not present its CA in the TLS chain (only %d certificate(s)) — the storage host runs a pgcli from before CA distribution; re-run `pg addon install minio --tls` there (which hot-reloads the chain) or copy its tls/minio/<name>/ca.crt over manually", hostport, len(chain))
}

// NormalizeS3Endpoint turns a repo endpoint (config convention: "host:port",
// no scheme, but a scheme is tolerated) into a dialable "host:port",
// defaulting to :443 when the port is omitted.
func NormalizeS3Endpoint(endpoint string) string {
	hostport := strings.TrimSpace(endpoint)
	hostport = strings.TrimPrefix(hostport, "https://")
	hostport = strings.TrimPrefix(hostport, "http://")
	hostport = strings.TrimSuffix(hostport, "/")
	if hostport == "" {
		return hostport
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// Missing port (SplitHostPort errs) — default to the HTTPS port. A
		// bracketed IPv6 with no port lands here too and stays valid.
		return net.JoinHostPort(hostport, "443")
	}
	if port == "" {
		return net.JoinHostPort(host, "443")
	}
	return hostport
}
