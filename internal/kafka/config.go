// Package kafka renders the managed KRaft server.properties and handles the
// cluster-id / meta.properties bookkeeping shared by the CLI and podman
// manager.
package kafka

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
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

	rf := config.KafkaReplicationFactor(len(voters))
	minISR := config.KafkaMinISR(rf)

	voterStrings := make([]string, 0, len(voters))
	for _, voter := range voters {
		voterStrings = append(voterStrings, voter.String())
	}

	var b strings.Builder
	writeProperty(&b, "process.roles", "broker,controller")
	writeProperty(&b, "node.id", strconv.Itoa(k.NodeID))
	writeProperty(&b, "listeners",
		"PLAINTEXT://"+net.JoinHostPort(k.Listen, strconv.Itoa(k.BrokerPort))+","+
			"CONTROLLER://"+net.JoinHostPort(k.Listen, strconv.Itoa(k.ControllerPort)))
	writeProperty(&b, "advertised.listeners",
		"PLAINTEXT://"+net.JoinHostPort(k.AdvertiseHost, strconv.Itoa(k.BrokerPort)))
	writeProperty(&b, "inter.broker.listener.name", "PLAINTEXT")
	writeProperty(&b, "controller.listener.names", "CONTROLLER")
	writeProperty(&b, "listener.security.protocol.map", "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT")
	writeProperty(&b, "controller.quorum.voters", strings.Join(voterStrings, ","))
	writeProperty(&b, "log.dirs", ContainerDataDir)
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
