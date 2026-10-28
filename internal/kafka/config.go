// Package kafka renders the managed KRaft server.properties and handles the
// cluster-id / meta.properties bookkeeping shared by the CLI and podman
// manager.
package kafka

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/tlsca"
)

// Container paths are fixed by the official apache/kafka image. The image's
// entrypoint merges every file in /mnt/shared/config into server.properties
// and formats /var/lib/kafka/data on the first boot.
const (
	ContainerConfigDir = "/mnt/shared/config"
	ContainerDataDir   = "/var/lib/kafka/data"

	// ContainerUID/ContainerGID are the image's appuser. The bind-mounted data
	// dir must be writable by that id.
	ContainerUID = 1000
	ContainerGID = 1000

	// TLSDirName sits under the mounted config dir; ServerPEMFile is the
	// combined key+cert file Kafka's PEM keystore expects.
	TLSDirName      = "tls"
	ServerPEMFile   = "server.pem"
	ContainerTLSDir = ContainerConfigDir + "/" + TLSDirName
)

// GenerateClusterID returns a random 22-character Kafka KRaft cluster id (16
// random bytes, unpadded base64url), matching `kafka-storage.sh random-uuid`.
func GenerateClusterID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating Kafka cluster id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// RenderProperties returns the complete server.properties for one combined
// KRaft node. Every value that affects the topology is rendered by pgcli;
// Settings are appended afterwards and may only contain other properties.
func RenderProperties(k config.KafkaConfig) (string, error) {
	if err := config.ValidateKafkaSettings(k.Settings); err != nil {
		return "", err
	}
	voters, err := config.KafkaVoters(k)
	if err != nil {
		return "", err
	}
	if len(voters) == 0 {
		return "", fmt.Errorf("Kafka requires at least one controller voter")
	}
	if !config.ValidKafkaClusterID(k.ClusterID) {
		return "", fmt.Errorf("invalid Kafka cluster id %q", k.ClusterID)
	}
	if k.Clustered() {
		if want := config.DeriveKafkaClusterID(k.Cluster); k.ClusterID != want {
			return "", fmt.Errorf("kafka cluster id %q does not match cluster %q (expected %q)", k.ClusterID, k.Cluster, want)
		}
	}

	rf := config.KafkaReplicationFactor(len(voters))
	minISR := config.KafkaMinISR(rf)

	voterStrings := make([]string, 0, len(voters))
	for _, voter := range voters {
		voterStrings = append(voterStrings, voter.String())
	}

	var b strings.Builder
	writeProperty(&b, "process.roles", "broker,controller")
	writeProperty(&b, "node.id", strconv.Itoa(k.NodeID))
	plainListener := "PLAINTEXT://" + net.JoinHostPort(k.Listen, strconv.Itoa(k.BrokerPort))
	plainAdvertised := "PLAINTEXT://" + net.JoinHostPort(k.AdvertiseHost, strconv.Itoa(k.BrokerPort))
	listeners := plainListener
	advertised := plainAdvertised
	protocolMap := "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT"
	if k.TLS {
		listeners += ",SSL://" + net.JoinHostPort(k.Listen, strconv.Itoa(k.TLSPort))
		advertised += ",SSL://" + net.JoinHostPort(k.AdvertiseHost, strconv.Itoa(k.TLSPort))
		protocolMap += ",SSL:SSL"
	}
	listeners += ",CONTROLLER://" + net.JoinHostPort(k.Listen, strconv.Itoa(k.ControllerPort))
	writeProperty(&b, "listeners", listeners)
	writeProperty(&b, "advertised.listeners", advertised)
	writeProperty(&b, "inter.broker.listener.name", "PLAINTEXT")
	writeProperty(&b, "controller.listener.names", "CONTROLLER")
	writeProperty(&b, "listener.security.protocol.map", protocolMap)
	writeProperty(&b, "controller.quorum.voters", strings.Join(voterStrings, ","))
	writeProperty(&b, "log.dirs", ContainerDataDir)
	if k.TLS {
		writeProperty(&b, "ssl.keystore.type", "PEM")
		writeProperty(&b, "ssl.keystore.location", ContainerTLSDir+"/"+ServerPEMFile)
		writeProperty(&b, "ssl.client.auth", "none")
	}
	b.WriteString("\n")
	writeProperty(&b, "offsets.topic.replication.factor", strconv.Itoa(rf))
	writeProperty(&b, "transaction.state.log.replication.factor", strconv.Itoa(rf))
	writeProperty(&b, "transaction.state.log.min.isr", strconv.Itoa(minISR))
	writeProperty(&b, "share.coordinator.state.topic.replication.factor", strconv.Itoa(rf))
	writeProperty(&b, "share.coordinator.state.topic.min.isr", strconv.Itoa(minISR))
	writeProperty(&b, "default.replication.factor", strconv.Itoa(rf))
	writeProperty(&b, "min.insync.replicas", strconv.Itoa(minISR))

	if len(k.Settings) > 0 {
		b.WriteString("\n")
		keys := make([]string, 0, len(k.Settings))
		for key := range k.Settings {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			writeProperty(&b, key, k.Settings[key])
		}
	}
	return b.String(), nil
}

func writeProperty(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(value)
	b.WriteByte('\n')
}

// WriteProperties atomically writes the generated server.properties.
func WriteProperties(k config.KafkaConfig) error {
	content, err := RenderProperties(k)
	if err != nil {
		return err
	}
	dir := filepath.Dir(k.ConfigPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".server.properties-*")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("writing config: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("setting config mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing config: %w", err)
	}
	if err := os.Rename(name, k.ConfigPath); err != nil {
		return fmt.Errorf("installing %s: %w", k.ConfigPath, err)
	}
	return nil
}

// TLSDir returns the host directory holding this instance's TLS material.
func TLSDir(k config.KafkaConfig) string {
	return filepath.Join(filepath.Dir(k.ConfigPath), TLSDirName)
}

// TLSCAPath is the CA certificate clients use to verify the broker.
func TLSCAPath(k config.KafkaConfig) string {
	return filepath.Join(TLSDir(k), tlsca.CACertFile)
}

// CertInfo summarizes the leaf certificate in a bring-your-own TLS PEM.
type CertInfo struct {
	Subject    string
	Issuer     string
	DNSNames   []string
	IPs        []string
	NotBefore  time.Time
	NotAfter   time.Time
	SelfSigned bool
}

// CoversHost reports whether host (an IP or DNS name) is present in the
// certificate's SANs. Kafka's AdvertiseHost is validated as an IP.
func (ci *CertInfo) CoversHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		for _, s := range ci.IPs {
			if s == ip.String() {
				return true
			}
		}
		return false
	}
	for _, name := range ci.DNSNames {
		if strings.EqualFold(name, host) {
			return true
		}
	}
	return ci.Subject != "" && strings.EqualFold(ci.Subject, host)
}

// ValidateTLSPEM parses a combined certificate-chain + private-key PEM and
// checks that it can actually serve Kafka's SSL listener. It is called at
// install time so a bad file fails before the container is recreated.
func ValidateTLSPEM(path string) (*CertInfo, error) {
	_, _, info, err := readTLSPEM(path)
	return info, err
}

// EnsureTLSCerts creates or renews the broker certificate and writes the
// combined key+cert PEM file Kafka's PEM keystore reads. With TLSPEM set the
// user-supplied file is validated and installed instead; otherwise the CA is
// pgcli's private root (reused across renewals) and is what clients trust.
func EnsureTLSCerts(k config.KafkaConfig) (string, error) {
	if !k.TLS {
		return "", nil
	}
	dir := TLSDir(k)
	// The config dir is mounted read-only into the container. Kafka runs as
	// uid 1000, so both it and the TLS dir must be traversable and ca.crt
	// readable; the private key file itself stays host-only. tlsca.Generate
	// creates a missing parent with 0700, so widen it explicitly. The combined
	// server.pem is chowned to the container user by the podman manager.
	configDir := filepath.Dir(dir)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", fmt.Errorf("creating config dir %s: %w", configDir, err)
	}
	if err := os.Chmod(configDir, 0o755); err != nil {
		return "", fmt.Errorf("setting config dir mode: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating TLS dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", fmt.Errorf("setting TLS dir mode: %w", err)
	}
	if k.TLSPEM != "" {
		if err := installBroughtOwnPEM(dir, k.TLSPEM); err != nil {
			return "", fmt.Errorf("installing Kafka TLS PEM %s: %w", k.TLSPEM, err)
		}
		return "", nil
	}

	hosts := []string{k.AdvertiseHost}
	if ip := net.ParseIP(k.Listen); ip != nil && !ip.IsUnspecified() {
		hosts = append(hosts, k.Listen)
	}
	caPath, err := tlsca.Generate(dir, tlsca.LocalHosts(hosts...), 30*24*time.Hour)
	if err != nil {
		return "", fmt.Errorf("generating Kafka TLS certificates: %w", err)
	}
	if err := os.Chmod(caPath, 0o644); err != nil {
		return "", fmt.Errorf("setting CA cert mode: %w", err)
	}
	if err := writeServerPEM(dir); err != nil {
		return "", err
	}
	return caPath, nil
}

// installBroughtOwnPEM validates a combined PEM and writes a normalized copy
// as the Kafka keystore: all certificate blocks in their original order
// followed by the private key converted to PKCS#8.
func installBroughtOwnPEM(dir, path string) error {
	chain, key, _, err := readTLSPEM(path)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	b.Write(chain)
	b.Write(key)
	return writeFileAtomic(filepath.Join(dir, ServerPEMFile), b.Bytes(), 0o600)
}

// readTLSPEM splits a combined PEM into a certificate chain and a PKCS#8
// private key, then proves the pair works for server authentication.
func readTLSPEM(path string) (chainPEM, keyPEM []byte, info *CertInfo, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var certs []*pem.Block
	var keyBlock *pem.Block
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case block.Type == "CERTIFICATE":
			certs = append(certs, block)
		case strings.HasSuffix(block.Type, "PRIVATE KEY"):
			if keyBlock != nil {
				return nil, nil, nil, fmt.Errorf("%s contains more than one private key", path)
			}
			keyBlock = block
		}
	}
	if len(certs) == 0 {
		return nil, nil, nil, fmt.Errorf("%s has no CERTIFICATE block", path)
	}
	if keyBlock == nil {
		return nil, nil, nil, fmt.Errorf("%s has no private key block", path)
	}
	var chain bytes.Buffer
	for _, cert := range certs {
		if err := pem.Encode(&chain, cert); err != nil {
			return nil, nil, nil, fmt.Errorf("encoding certificate chain: %w", err)
		}
	}
	keyPEM, err = normalizeKeyPEM(keyBlock)
	if err != nil {
		return nil, nil, nil, err
	}
	pair, err := tls.X509KeyPair(chain.Bytes(), keyPEM)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("certificate chain and private key do not pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parsing leaf certificate: %w", err)
	}
	if leaf.IsCA {
		return nil, nil, nil, fmt.Errorf("%s is a CA certificate; use the server leaf whose private key you provided", path)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) {
		return nil, nil, nil, fmt.Errorf("certificate is not valid yet (starts %s)", leaf.NotBefore.Format(time.RFC3339))
	}
	if now.After(leaf.NotAfter) {
		return nil, nil, nil, fmt.Errorf("certificate expired %s", leaf.NotAfter.Format(time.RFC3339))
	}
	if len(leaf.ExtKeyUsage) > 0 {
		ok := false
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny {
				ok = true
				break
			}
		}
		if !ok {
			return nil, nil, nil, fmt.Errorf("certificate is not valid for server authentication (ExtKeyUsage %v)", leaf.ExtKeyUsage)
		}
	}
	ips := make([]string, 0, len(leaf.IPAddresses))
	for _, ip := range leaf.IPAddresses {
		ips = append(ips, ip.String())
	}
	info = &CertInfo{
		Subject:    leaf.Subject.CommonName,
		Issuer:     leaf.Issuer.CommonName,
		DNSNames:   leaf.DNSNames,
		IPs:        ips,
		NotBefore:  leaf.NotBefore,
		NotAfter:   leaf.NotAfter,
		SelfSigned: string(leaf.RawSubject) == string(leaf.RawIssuer),
	}
	return chain.Bytes(), keyPEM, info, nil
}

// writeServerPEM concatenates the leaf+CA chain and the private key into the
// single PEM file the Kafka PEM keystore expects. Kafka only accepts PKCS#8
// private keys, while tlsca writes PKCS#1 ("RSA PRIVATE KEY"), so the key is
// re-encoded here.
func writeServerPEM(dir string) error {
	cert, err := os.ReadFile(filepath.Join(dir, tlsca.ServerCert))
	if err != nil {
		return fmt.Errorf("reading Kafka server certificate: %w", err)
	}
	key, err := os.ReadFile(filepath.Join(dir, tlsca.ServerKey))
	if err != nil {
		return fmt.Errorf("reading Kafka server key: %w", err)
	}
	keyPEM, err := pkcs8PEM(key)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	b.Write(cert)
	if !bytes.HasSuffix(cert, []byte("\n")) {
		b.WriteByte('\n')
	}
	b.Write(keyPEM)
	return writeFileAtomic(filepath.Join(dir, ServerPEMFile), b.Bytes(), 0o600)
}

// pkcs8PEM converts a private key PEM to PKCS#8.
func pkcs8PEM(keyPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("decoding Kafka server key: no PEM block found")
	}
	return normalizeKeyPEM(block)
}

// normalizeKeyPEM converts PKCS#1/EC private keys to PKCS#8 and rejects key
// types or encodings Kafka's PEM keystore cannot load.
func normalizeKeyPEM(block *pem.Block) ([]byte, error) {
	switch block.Type {
	case "ENCRYPTED PRIVATE KEY":
		return nil, fmt.Errorf("encrypted private keys are not supported; decrypt the key first")
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing PKCS#8 private key: %w", err)
		}
		if err := checkKafkaKeyType(key); err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(block), nil
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing RSA private key: %w", err)
		}
		return marshalPKCS8(key)
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing EC private key: %w", err)
		}
		return marshalPKCS8(key)
	default:
		return nil, fmt.Errorf("unsupported private key PEM type %q", block.Type)
	}
}

func checkKafkaKeyType(key any) error {
	switch key.(type) {
	case *rsa.PrivateKey, *ecdsa.PrivateKey:
		return nil
	default:
		return fmt.Errorf("Kafka's PEM keystore supports RSA and EC private keys, got %T", key)
	}
}

func marshalPKCS8(key any) ([]byte, error) {
	if err := checkKafkaKeyType(key); err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encoding private key as PKCS#8: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("setting mode on %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("installing %s: %w", path, err)
	}
	return nil
}

// MetaProperties is the subset of a Kafka meta.properties file pgcli uses to
// guard against pointing a configuration at another cluster's data dir.
type MetaProperties struct {
	ClusterID   string
	NodeID      int
	HasNodeID   bool
	DirectoryID string
	Version     string
}

// ReadMetaProperties parses <dataDir>/meta.properties. It returns (nil, nil)
// when the data dir has not been formatted yet.
func ReadMetaProperties(dataDir string) (*MetaProperties, error) {
	path := filepath.Join(dataDir, "meta.properties")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	meta := &MetaProperties{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "cluster.id":
			meta.ClusterID = value
		case "node.id":
			id, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("parsing node.id in %s: %w", path, err)
			}
			meta.NodeID = id
			meta.HasNodeID = true
		case "directory.id":
			meta.DirectoryID = value
		case "version":
			meta.Version = value
		}
	}
	if meta.ClusterID == "" {
		return nil, fmt.Errorf("%s has no cluster.id", path)
	}
	return meta, nil
}

// AdoptMetaData fills unset cluster-id/node-id fields from an existing
// meta.properties and rejects values that contradict it. It lets a reinstall
// after `remove` (without --clean-data) reuse the formatted storage instead of
// silently creating a second cluster id.
func AdoptMetaData(k *config.KafkaConfig) error {
	meta, err := ReadMetaProperties(k.DataDir)
	if err != nil {
		return err
	}
	if meta == nil {
		return nil
	}
	if k.ClusterID != "" && k.ClusterID != meta.ClusterID {
		return fmt.Errorf("data dir %s belongs to Kafka cluster %s, not %s — use the original --cluster-id or pick an empty --data-dir", k.DataDir, meta.ClusterID, k.ClusterID)
	}
	k.ClusterID = meta.ClusterID
	if meta.HasNodeID {
		if k.NodeID != 0 && k.NodeID != meta.NodeID {
			return fmt.Errorf("data dir %s belongs to Kafka node %d, not %d", k.DataDir, meta.NodeID, k.NodeID)
		}
		k.NodeID = meta.NodeID
	}
	return nil
}

// CheckMetaData verifies that an already-decided configuration matches the
// formatted data dir, if any, before writing files or starting the container.
func CheckMetaData(k config.KafkaConfig) error {
	meta, err := ReadMetaProperties(k.DataDir)
	if err != nil {
		return err
	}
	if meta == nil {
		return nil
	}
	if meta.ClusterID != k.ClusterID {
		return fmt.Errorf("data dir %s belongs to Kafka cluster %s, but this instance is configured for %s", k.DataDir, meta.ClusterID, k.ClusterID)
	}
	if meta.HasNodeID && meta.NodeID != k.NodeID {
		return fmt.Errorf("data dir %s belongs to Kafka node %d, but this instance is configured as node %d", k.DataDir, meta.NodeID, k.NodeID)
	}
	return nil
}

// LocalBrokerEndpoint is the broker address pgcli uses inside the host (health
// checks, topic commands). A wildcard bind is reached through loopback.
func LocalBrokerEndpoint(k config.KafkaConfig) string {
	return net.JoinHostPort(localHost(k.Listen), strconv.Itoa(k.BrokerPort))
}

// LocalControllerEndpoint is the controller address used by
// kafka-metadata-quorum.sh.
func LocalControllerEndpoint(k config.KafkaConfig) string {
	return net.JoinHostPort(localHost(k.Listen), strconv.Itoa(k.ControllerPort))
}

// AdvertisedBrokerEndpoint is the endpoint clients should use, as rendered in
// advertised.listeners.
func AdvertisedBrokerEndpoint(k config.KafkaConfig) string {
	return net.JoinHostPort(k.AdvertiseHost, strconv.Itoa(k.BrokerPort))
}

// AdvertisedTLSEndpoint is the SSL endpoint clients should use, as rendered in
// advertised.listeners.
func AdvertisedTLSEndpoint(k config.KafkaConfig) string {
	if k.TLSPort == 0 {
		return ""
	}
	return net.JoinHostPort(k.AdvertiseHost, strconv.Itoa(k.TLSPort))
}

func localHost(listen string) string {
	ip := net.ParseIP(listen)
	if ip == nil || !ip.IsUnspecified() {
		return listen
	}
	if ip.To4() == nil {
		return "::1"
	}
	return "127.0.0.1"
}
