package podman

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/tlsca"
)

// etcd TLS material layout:
//
//	<base>/tls/etcd/<cluster>/ca.crt|ca.key        cluster CA, shared by every member
//	<base>/tls/etcd/<cluster>/<member>/...         this member's leaf (tlsca layout)
//
// The cluster dir is the cross-host distribution point: the first member
// publishes the CA pair into etcd under the keys below, and joining hosts pull
// it back before generating their own leaf. Operators can also drop a CA pair
// there by hand (e.g. `pg cert --ca`) and it is reused as-is.
const (
	etcdCertsDir  = "/etcd-certs"
	etcdctlCAPath = "/etcdctl-ca/ca.crt"

	etcdCAPublishCertKey = "/_pgcli/pki/etcd/ca.crt"
	etcdCAPublishKeyKey  = "/_pgcli/pki/etcd/ca.key"

	// generatedCAMarker sits next to the cluster CA when pgcli minted it.
	// Only then is the CA pair published into etcd: a manually distributed
	// pair (`pg cert --ca`) stays out of the cluster.
	generatedCAMarker = ".pgcli-generated"
)

// EtcdTLS is the resolved TLS material for one member. Mounts are podman -v
// args exposing cert/key/CA at the paths etcd and etcdctl expect inside the
// container; CAFile is the host path clients (etcdctl containers) trust.
type EtcdTLS struct {
	Mounts    []string
	CAFile    string
	MemberDir string // generated mode only
}

// ClusterTLSDir is the host directory holding the cluster CA.
func (m *EtcdManager) ClusterTLSDir(ec *config.EtcdConfig) string {
	name := ec.ClusterName
	if name == "" {
		name = "default"
	}
	return filepath.Join(m.dataDir, "tls", "etcd", etcdPathComponent(name))
}

func (m *EtcdManager) memberTLSDir(ec *config.EtcdConfig) string {
	return filepath.Join(m.ClusterTLSDir(ec), etcdPathComponent(ec.Name))
}

// etcdPathComponent keeps cluster/member names from escaping the tls tree.
func etcdPathComponent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "default"
	}
	return out
}

// EnsureMemberTLS resolves (and, in generated mode, creates) this member's TLS
// material. Generated mode shares one CA per cluster: a pair in the cluster dir
// wins, a pair left in the member dir is adopted into it, and a fresh CA is
// only minted when allowGenerateCA is true (bootstrapping a brand-new member).
func (m *EtcdManager) EnsureMemberTLS(ec *config.EtcdConfig, allowGenerateCA bool) (*EtcdTLS, error) {
	if !ec.TLS {
		return nil, nil
	}
	if BYOTLS(true, ec.TLSCertFile, ec.TLSKeyFile) {
		if err := validateEtcdBYOCert(ec.TLSCertFile, ec.TLSKeyFile); err != nil {
			return nil, err
		}
		ca := ec.TLSCAFile
		if ca == "" {
			// A self-signed leaf is its own trust anchor; etcd accepts the
			// server cert as --trusted-ca-file in that case.
			ca = ec.TLSCertFile
		}
		if _, err := os.Stat(ca); err != nil {
			return nil, fmt.Errorf("etcd TLS CA: %w", err)
		}
		return &EtcdTLS{
			Mounts: []string{
				"-v", fmt.Sprintf("%s:%s/public.crt:ro,z", hostMountPath(ec.TLSCertFile), etcdCertsDir),
				"-v", fmt.Sprintf("%s:%s/private.key:ro,z", hostMountPath(ec.TLSKeyFile), etcdCertsDir),
				"-v", fmt.Sprintf("%s:%s/ca.crt:ro,z", hostMountPath(ca), etcdCertsDir),
			},
			CAFile: hostMountPath(ca),
		}, nil
	}
	if (ec.TLSCertFile == "") != (ec.TLSKeyFile == "") {
		return nil, fmt.Errorf("etcd %s: tls_cert_file and tls_key_file must be set together", ec.Name)
	}

	clusterDir := m.ClusterTLSDir(ec)
	memberDir := m.memberTLSDir(ec)
	for _, dir := range []string{clusterDir, memberDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("creating etcd TLS dir %s: %w", dir, err)
		}
	}
	clusterCA := filepath.Join(clusterDir, tlsca.CACertFile)
	clusterKey := filepath.Join(clusterDir, tlsca.CAKeyFile)
	memberCA := filepath.Join(memberDir, tlsca.CACertFile)
	memberKey := filepath.Join(memberDir, tlsca.CAKeyFile)
	minted := !fileExists(clusterCA) && !fileExists(memberCA)

	switch {
	case fileExists(clusterCA) && fileExists(clusterKey):
		if err := copyCertFile(clusterCA, memberCA, 0644); err != nil {
			return nil, err
		}
		if err := copyCertFile(clusterKey, memberKey, 0600); err != nil {
			return nil, err
		}
	case fileExists(memberCA) && fileExists(memberKey):
		if err := copyCertFile(memberCA, clusterCA, 0644); err != nil {
			return nil, err
		}
		if err := copyCertFile(memberKey, clusterKey, 0600); err != nil {
			return nil, err
		}
	case !allowGenerateCA:
		return nil, fmt.Errorf("etcd TLS CA missing for cluster %q (expected %s) — copy ca.crt/ca.key there, join with --join to fetch it from the cluster, or bootstrap a new cluster", ec.ClusterName, clusterCA)
	}

	// Mutually authenticated leaf: raft peers verify each other's certificates
	// as client certs, which requires the ClientAuth EKU.
	if _, err := tlsca.GenerateMutual(memberDir, tlsca.LocalHosts(ec.AdvertiseAddr()), 30*24*time.Hour); err != nil {
		return nil, fmt.Errorf("generating etcd member certificate: %w", err)
	}
	// A freshly generated CA lives in memberDir; promote it to the cluster dir
	// so sibling members (and the next join) reuse it.
	if err := copyCertFile(memberCA, clusterCA, 0644); err != nil {
		return nil, err
	}
	if err := copyCertFile(memberKey, clusterKey, 0600); err != nil {
		return nil, err
	}
	if minted {
		if err := os.WriteFile(filepath.Join(clusterDir, generatedCAMarker), []byte("generated by pgcli; delete before placing a manually distributed CA here\n"), 0600); err != nil {
			return nil, fmt.Errorf("marking generated etcd CA: %w", err)
		}
	}
	return &EtcdTLS{
		Mounts:    []string{"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(memberDir), etcdCertsDir)},
		CAFile:    clusterCA,
		MemberDir: memberDir,
	}, nil
}

// ClientCAFile returns the host CA path a client (etcdctl) should trust for
// this member. Empty when TLS is off.
func (m *EtcdManager) ClientCAFile(ec *config.EtcdConfig) (string, error) {
	if !ec.TLS {
		return "", nil
	}
	if BYOTLS(true, ec.TLSCertFile, ec.TLSKeyFile) {
		ca := ec.TLSCAFile
		if ca == "" {
			ca = ec.TLSCertFile
		}
		return hostMountPath(ca), nil
	}
	ca := filepath.Join(m.ClusterTLSDir(ec), tlsca.CACertFile)
	if !fileExists(ca) {
		return "", fmt.Errorf("etcd TLS CA missing at %s — install/join the member first", ca)
	}
	return ca, nil
}

// PublishClusterCA writes the generated cluster CA pair into etcd so joining
// hosts can fetch it. No-op for plaintext and BYO setups (BYO CAs are
// distributed manually) and for a manually placed CA — only a pair pgcli
// minted carries the marker and gets published. authEnabled says whether the
// cluster's authentication is already on: only then are the member's
// credentials sent.
func (m *EtcdManager) PublishClusterCA(ec *config.EtcdConfig, authEnabled bool) error {
	if !ec.TLS || BYOTLS(true, ec.TLSCertFile, ec.TLSKeyFile) {
		return nil
	}
	if !m.AutoPublishesCA(ec) {
		return nil
	}
	caFile, err := m.ClientCAFile(ec)
	if err != nil {
		return err
	}
	memberDir := m.memberTLSDir(ec)
	cert, err := os.ReadFile(filepath.Join(memberDir, tlsca.CACertFile))
	if err != nil {
		return fmt.Errorf("reading cluster CA: %w", err)
	}
	key, err := os.ReadFile(filepath.Join(memberDir, tlsca.CAKeyFile))
	if err != nil {
		return fmt.Errorf("reading cluster CA key: %w", err)
	}
	opts := EtcdClientOpts{CAFile: caFile}
	if authEnabled && ec.Auth {
		opts.Username = ec.AuthUser
		opts.Password = ec.AuthPassword
	}
	if err := EtcdPutValuesWith([]string{ec.ClientURL()}, map[string]string{
		etcdCAPublishCertKey: string(cert),
		etcdCAPublishKeyKey:  string(key),
	}, opts); err != nil {
		return fmt.Errorf("publishing etcd cluster CA: %w", err)
	}
	return nil
}

// AutoPublishesCA reports whether this member's cluster CA was minted by
// pgcli (and is therefore published into etcd for joining hosts). False for a
// manually distributed CA and for BYO certificates.
func (m *EtcdManager) AutoPublishesCA(ec *config.EtcdConfig) bool {
	if !ec.TLS || BYOTLS(true, ec.TLSCertFile, ec.TLSKeyFile) {
		return false
	}
	return fileExists(filepath.Join(m.ClusterTLSDir(ec), generatedCAMarker))
}

// FetchClusterCA pulls the published CA pair from a TLS cluster endpoint into
// the local cluster dir (TOFU: the endpoint certificate is not trusted yet, so
// verification is skipped for this one exchange). Credentials are required
// once the remote cluster has auth enabled.
func (m *EtcdManager) FetchClusterCA(ec *config.EtcdConfig, endpoint, username, password string) error {
	if !ec.TLS {
		return nil
	}
	opts := EtcdClientOpts{
		InsecureSkipVerify: true,
		Username:           username,
		Password:           password,
	}
	cert, found, err := EtcdGetValueWith([]string{endpoint}, etcdCAPublishCertKey, opts)
	if err != nil {
		return fmt.Errorf("fetching cluster CA from %s: %w", endpoint, err)
	}
	if !found {
		return fmt.Errorf("%s has no published etcd CA at %s — was the cluster bootstrapped with --tls?", endpoint, etcdCAPublishCertKey)
	}
	key, found, err := EtcdGetValueWith([]string{endpoint}, etcdCAPublishKeyKey, opts)
	if err != nil {
		return fmt.Errorf("fetching cluster CA key from %s: %w", endpoint, err)
	}
	if !found {
		return fmt.Errorf("%s has no published etcd CA key at %s", endpoint, etcdCAPublishKeyKey)
	}
	if err := validateCAPair([]byte(cert), []byte(key)); err != nil {
		return fmt.Errorf("published etcd CA is unusable: %w", err)
	}
	dir := m.ClusterTLSDir(ec)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating etcd TLS dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, tlsca.CACertFile), []byte(cert), 0644); err != nil {
		return fmt.Errorf("writing cluster CA: %w", err)
	}
	keyPath := filepath.Join(dir, tlsca.CAKeyFile)
	if err := os.WriteFile(keyPath, []byte(key), 0600); err != nil {
		return fmt.Errorf("writing cluster CA key: %w", err)
	}
	return os.Chmod(keyPath, 0600)
}

// validateEtcdBYOCert applies the shared BYO checks and additionally requires
// client-auth usage when the certificate pins EKUs — the same pair serves raft
// peer mTLS.
func validateEtcdBYOCert(certPath, keyPath string) error {
	if _, err := ValidateBYOCert(certPath, keyPath); err != nil {
		return err
	}
	data, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		return fmt.Errorf("%s contains no PEM certificate", certPath)
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", certPath, err)
	}
	if len(leaf.ExtKeyUsage) > 0 {
		ok := false
		for _, ku := range leaf.ExtKeyUsage {
			if ku == x509.ExtKeyUsageClientAuth || ku == x509.ExtKeyUsageAny {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%s is not valid for client authentication (ExtKeyUsage %v), which etcd raft peer mTLS requires", certPath, leaf.ExtKeyUsage)
		}
	}
	return nil
}

// validateCAPair checks that certPEM is a CA certificate and keyPEM is its
// matching private key.
func validateCAPair(certPEM, keyPEM []byte) error {
	cb, _ := pem.Decode(certPEM)
	if cb == nil || cb.Type != "CERTIFICATE" {
		return fmt.Errorf("CA cert is not a PEM CERTIFICATE")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return fmt.Errorf("parsing CA cert: %w", err)
	}
	if !cert.IsCA {
		return fmt.Errorf("certificate is not a CA")
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return fmt.Errorf("CA key is not a PEM private key")
	}
	var key crypto.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(kb.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParsePKCS8PrivateKey(kb.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParseECPrivateKey(kb.Bytes); err == nil {
		key = k
	} else {
		return fmt.Errorf("CA key is not a supported private key")
	}
	var pub crypto.PublicKey
	switch k := key.(type) {
	case *rsa.PrivateKey:
		pub = k.Public()
	case *ecdsa.PrivateKey:
		pub = k.Public()
	case ed25519.PrivateKey:
		pub = k.Public()
	default:
		return fmt.Errorf("unsupported CA key type %T", key)
	}
	type equaler interface{ Equal(crypto.PublicKey) bool }
	eq, ok := cert.PublicKey.(equaler)
	if !ok {
		return fmt.Errorf("cannot compare CA cert and key")
	}
	if !eq.Equal(pub) {
		return fmt.Errorf("CA key does not match CA cert")
	}
	return nil
}

// fileExists reports whether path exists as a regular file.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// copyFile copies src to dst (mode enforced) unless the contents already match.
func copyCertFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return os.Chmod(dst, mode)
}

// etcdServerTLSFlags returns the etcd argv for TLS on both listeners. Client
// connections verify the server (no client certs — auth is username/password),
// raft peers additionally require a client certificate from the shared CA.
//
// --trusted-ca-file is deliberately absent on the client listener: etcd's
// ServerConfig() sets RequireAndVerifyClientCert whenever it is non-empty,
// regardless of --client-cert-auth=false, which would reject every plain
// etcdctl/HTTP client.
//
// etcd also checks that the peer client cert's SANs match the connection's
// source IP. That breaks any host behind NAT/SNAT (common between private
// networks), so the experimental switch disables just that check: peers still
// must present a certificate signed by the shared CA.
func etcdServerTLSFlags() []string {
	return []string{
		"--cert-file", etcdCertsDir + "/public.crt",
		"--key-file", etcdCertsDir + "/private.key",
		"--client-cert-auth=false",
		"--peer-cert-file", etcdCertsDir + "/public.crt",
		"--peer-key-file", etcdCertsDir + "/private.key",
		"--peer-trusted-ca-file", etcdCertsDir + "/ca.crt",
		"--peer-client-cert-auth",
		"--experimental-peer-skip-client-san-verification",
	}
}
