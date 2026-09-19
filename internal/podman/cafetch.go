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

// FetchRepoCA dials a TLS S3 endpoint and returns the CA that should be
// trusted for it, PEM-encoded, taken from the server's certificate chain.
//
// Two shapes resolve to an anchor: a self-signed leaf is returned as-is (it is
// its own trust anchor — `pg cert` mints this shape for MinIO's BYO mode), and
// otherwise the first self-signed CA in the chain that actually signed the leaf
// is returned (pgcli's generated `--tls` leaf+CA chain).
//
// This closes the first hop of cross-host backup trust: a Patroni host that has
// never seen the storage side's certificate can pull it with one command
// instead of an scp from the storage host. Everything after that is already
// automated — `pg backup setup` publishes the pulled CA to the cluster's etcd
// registry for the remaining hosts.
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
	// A self-signed leaf *is* its own anchor — there is no separate CA to
	// find, and none of the chain-scanning below applies (see `pg cert`,
	// which mints exactly this shape for MinIO's BYO mode).
	if bytes.Equal(leaf.RawSubject, leaf.RawIssuer) {
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})), nil
	}
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

// s3EndpointDialTimeout bounds the reachability probe: an unreachable storage
// host must fail a setup fast, at the network layer, before any image pull or
// container start spends minutes and buries the real cause.
const s3EndpointDialTimeout = 5 * time.Second

// CheckEndpointReachable dials the S3 endpoint's host:port over plain TCP and
// returns a normalized error if the socket cannot be established. This is a
// network-layer preflight only — it proves nothing about TLS, credentials, or
// the bucket (VerifyRepoConnectivity's repo-ls does that later); it exists so
// a mistyped host or a store that is simply down aborts `pg backup setup`
// immediately with a clear message rather than after the slow steps. The
// endpoint is normalized first, so a scheme or a missing :443 is tolerated the
// same way every other consumer reads it.
func CheckEndpointReachable(endpoint string) error {
	return checkEndpointReachable(endpoint, s3EndpointDialTimeout)
}

// checkEndpointReachable is the testable core: the timeout is a parameter so a
// test can force the unreachable path on a guaranteed-dead address quickly.
func checkEndpointReachable(endpoint string, timeout time.Duration) error {
	hostport := NormalizeS3Endpoint(endpoint)
	conn, err := net.DialTimeout("tcp", hostport, timeout)
	if err != nil {
		return fmt.Errorf("cannot reach S3 endpoint %s: %w (check backup.repo.s3.endpoint, that the store is running, and that this host has network route to it)", hostport, err)
	}
	_ = conn.Close()
	return nil
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
