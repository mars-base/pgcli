package cli

import (
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/kafka"
	"github.com/mars-base/pgcli/internal/podman"
)

// resolveKafkaInstance loads the config, applies defaults and returns the
// named Kafka node.
func resolveKafkaInstance(name string) (*config.Config, string, config.KafkaConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.KafkaConfig{}, err
	}
	c.ApplyDefaults()
	kc, ok := c.Addons.Kafka[name]
	if !ok {
		return nil, "", config.KafkaConfig{}, fmt.Errorf("kafka %q not found (see 'pg addon list')", name)
	}
	return c, path, kc, nil
}

func kafkaFlagName(cmd *cobra.Command) string {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "kafka"
	}
	return name
}

// kafkaTLSSource describes where the SSL listener's key material came from.
func kafkaTLSSource(kc config.KafkaConfig) string {
	if kc.TLSPEM != "" {
		return "cert " + kc.TLSPEM
	}
	return "CA " + kafka.TLSCAPath(kc)
}

// parseKafkaConfigOptions parses repeatable KEY=VALUE overrides for
// server.properties.
func parseKafkaConfigOptions(options []string) (map[string]string, error) {
	settings := make(map[string]string, len(options))
	for _, opt := range options {
		key, value, ok := strings.Cut(opt, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --config-option %q: use KEY=VALUE, e.g. num.partitions=3", opt)
		}
		settings[key] = value
	}
	if err := config.ValidateKafkaSettings(settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// --- install / lifecycle ----------------------------------------------------

// kafkaInstallOptions captures the cluster-related flags and whether they were
// explicitly set, so re-installs can distinguish "keep the stored value" from
// "the operator is trying to change cluster identity".
type kafkaInstallOptions struct {
	cluster           string
	peers             []string
	nodeID            int
	clusterID         string
	advertiseHost     string
	controllerPort    int
	clusterSet        bool
	peersSet          bool
	nodeIDSet         bool
	clusterIDSet      bool
	advertiseHostSet  bool
	controllerPortSet bool
}

func readKafkaInstallOptions(fs *pflag.FlagSet) kafkaInstallOptions {
	opts := kafkaInstallOptions{
		clusterSet:        fs.Changed("cluster"),
		peersSet:          fs.Changed("peer"),
		nodeIDSet:         fs.Changed("node-id"),
		clusterIDSet:      fs.Changed("cluster-id"),
		advertiseHostSet:  fs.Changed("advertise-host"),
		controllerPortSet: fs.Changed("controller-port"),
	}
	opts.cluster, _ = fs.GetString("cluster")
	opts.peers, _ = fs.GetStringArray("peer")
	opts.nodeID, _ = fs.GetInt("node-id")
	opts.clusterID, _ = fs.GetString("cluster-id")
	opts.advertiseHost, _ = fs.GetString("advertise-host")
	opts.controllerPort, _ = fs.GetInt("controller-port")
	return opts
}

func applyKafkaInstallFlags(kc *config.KafkaConfig, fs *pflag.FlagSet, opts kafkaInstallOptions) error {
	if fs.Changed("version") {
		kc.Version, _ = fs.GetString("version")
	}
	if kc.Version == "" {
		kc.Version = config.DefaultKafkaVersion
	}
	imageTag, ok := config.KafkaImageTagForVersion(kc.Version)
	if !ok {
		return fmt.Errorf("Kafka version %q is not a valid image tag (default %s; pass the exact upstream tag, e.g. 4.4.0)", kc.Version, config.DefaultKafkaVersion)
	}
	kc.ImageTag = imageTag
	if v, _ := fs.GetString("listen"); v != "" {
		kc.Listen = v
	}
	if v, _ := fs.GetString("advertise-host"); v != "" {
		kc.AdvertiseHost = v
	}
	if fs.Changed("broker-port") {
		kc.BrokerPort, _ = fs.GetInt("broker-port")
	}
	if fs.Changed("controller-port") {
		kc.ControllerPort, _ = fs.GetInt("controller-port")
	}
	if fs.Changed("tls") {
		kc.TLS, _ = fs.GetBool("tls")
	}
	if fs.Changed("tls-pem") {
		pemPath, _ := fs.GetString("tls-pem")
		if pemPath == "" {
			kc.TLSPEM = ""
		} else {
			if fs.Changed("tls") && !kc.TLS {
				return fmt.Errorf("--tls-pem cannot be combined with --tls=false")
			}
			abs, err := filepath.Abs(pemPath)
			if err != nil {
				return fmt.Errorf("resolving --tls-pem: %w", err)
			}
			kc.TLSPEM = abs
			kc.TLS = true
		}
	}
	if !kc.TLS {
		kc.TLSPEM = ""
	}
	if fs.Changed("tls-port") {
		kc.TLSPort, _ = fs.GetInt("tls-port")
	}
	if fs.Changed("tls-port") && !kc.TLS {
		return fmt.Errorf("--tls-port requires --tls")
	}
	if v, _ := fs.GetString("data-dir"); v != "" {
		kc.DataDir = v
	}
	if fs.Changed("cluster-id") {
		kc.ClusterID, _ = fs.GetString("cluster-id")
	}
	if fs.Changed("heap") {
		value, _ := fs.GetString("heap")
		heap, err := config.ParseKafkaHeap(value)
		if err != nil {
			return err
		}
		kc.Heap = heap
	}
	if fs.Changed("config-option") {
		options, _ := fs.GetStringArray("config-option")
		settings, err := parseKafkaConfigOptions(options)
		if err != nil {
			return err
		}
		kc.Settings = settings
	}
	if opts.clusterSet {
		kc.Cluster = opts.cluster
	}
	if opts.peersSet {
		kc.Peers = append([]string(nil), opts.peers...)
	}
	if opts.nodeIDSet {
		kc.NodeID = opts.nodeID
	}
	return nil
}

// checkKafkaNoOpFlags rejects attempts to change cluster identity on an
// instance that is already installed without --force. Mutable flags are still
// ignored as before.
func checkKafkaNoOpFlags(stored config.KafkaConfig, opts kafkaInstallOptions) error {
	if stored.Cluster == "" {
		if opts.peersSet {
			return fmt.Errorf("--peer requires --cluster (pass the same cluster name on every host)")
		}
		if opts.nodeIDSet {
			return fmt.Errorf("--node-id requires --cluster (a single-node Kafka instance is always node 1)")
		}
	}
	if opts.clusterSet && opts.cluster != stored.Cluster {
		if stored.Cluster == "" {
			return fmt.Errorf("kafka %q is installed as a single node; cluster membership is not an in-place change (remove it first or use a new --name)", stored.Name)
		}
		return fmt.Errorf("kafka %q is installed in cluster %q; changing --cluster is not supported", stored.Name, stored.Cluster)
	}
	if opts.nodeIDSet && opts.nodeID != stored.NodeID {
		return fmt.Errorf("kafka %q is installed as node %d; changing --node-id is not supported", stored.Name, stored.NodeID)
	}
	if opts.clusterIDSet && opts.clusterID != "" && opts.clusterID != stored.ClusterID {
		return fmt.Errorf("kafka %q is installed with cluster id %s; changing --cluster-id is not supported", stored.Name, stored.ClusterID)
	}
	if opts.peersSet && !kafkaPeerSetsEqual(opts.peers, stored.Peers) {
		return fmt.Errorf("kafka %q is installed with static voters %s; changing --peer is not supported (static KRaft quorum membership cannot be edited in place)", stored.Name, strings.Join(stored.Peers, ", "))
	}
	if stored.Clustered() {
		if opts.advertiseHostSet && opts.advertiseHost != stored.AdvertiseHost {
			return fmt.Errorf("kafka %q advertises %s; changing --advertise-host would invalidate every peer's --peer list", stored.Name, stored.AdvertiseHost)
		}
		if opts.controllerPortSet && opts.controllerPort != stored.ControllerPort {
			return fmt.Errorf("kafka %q is installed with controller port %d; changing --controller-port would invalidate every peer's --peer list", stored.Name, stored.ControllerPort)
		}
	}
	return nil
}

// checkKafkaTopologyImmutable rejects changing a cluster node's identity even
// with --force. Static KRaft quorum membership cannot be edited in place.
func checkKafkaTopologyImmutable(stored, next config.KafkaConfig) error {
	if next.Cluster != stored.Cluster {
		if stored.Cluster == "" {
			return fmt.Errorf("kafka %q is installed as a single node; cluster membership is not an in-place change (remove it first or use a new --name)", next.Name)
		}
		return fmt.Errorf("kafka %q is installed in cluster %q; changing --cluster is not supported", next.Name, stored.Cluster)
	}
	if next.NodeID != stored.NodeID {
		if stored.Cluster == "" {
			return fmt.Errorf("--node-id requires --cluster (a single-node Kafka instance is always node 1)")
		}
		return fmt.Errorf("kafka %q is installed as node %d; changing --node-id is not supported", next.Name, stored.NodeID)
	}
	if !stored.Clustered() {
		if len(next.Peers) > 0 {
			return fmt.Errorf("--peer requires --cluster (pass the same cluster name on every host)")
		}
		return nil
	}
	if next.ClusterID != stored.ClusterID {
		return fmt.Errorf("kafka %q is installed with cluster id %s; changing --cluster-id is not supported", next.Name, stored.ClusterID)
	}
	if next.AdvertiseHost != stored.AdvertiseHost {
		return fmt.Errorf("kafka %q advertises %s; changing --advertise-host would invalidate every peer's --peer list", next.Name, stored.AdvertiseHost)
	}
	if next.ControllerPort != stored.ControllerPort {
		return fmt.Errorf("kafka %q is installed with controller port %d; changing --controller-port would invalidate every peer's --peer list", next.Name, stored.ControllerPort)
	}
	if !kafkaPeerSetsEqual(next.Peers, stored.Peers) {
		return fmt.Errorf("kafka %q is installed with static voters %s; changing --peer is not supported (static KRaft quorum membership cannot be edited in place)", next.Name, strings.Join(stored.Peers, ", "))
	}
	return nil
}

func validateKafkaClusterInstall(kc config.KafkaConfig, opts kafkaInstallOptions, present bool) error {
	if !kc.Clustered() {
		if opts.peersSet || len(kc.Peers) > 0 {
			return fmt.Errorf("--peer requires --cluster (pass the same cluster name on every host)")
		}
		if opts.nodeIDSet {
			return fmt.Errorf("--node-id requires --cluster (a single-node Kafka instance is always node 1)")
		}
		return nil
	}
	if len(kc.Peers) == 0 {
		return fmt.Errorf("--cluster requires at least one --peer ID@HOST:PORT listing the other controllers (this node is added automatically)")
	}
	if !present {
		if !opts.advertiseHostSet {
			return fmt.Errorf("--cluster requires an explicit --advertise-host <this host's peer-reachable IP>")
		}
		if !opts.controllerPortSet {
			return fmt.Errorf("--cluster requires an explicit --controller-port on a fresh install: it is embedded in every peer's --peer list, and per-host auto-assignment may choose a different port on another host")
		}
	}
	adv := net.ParseIP(kc.AdvertiseHost)
	if adv == nil || adv.IsUnspecified() || adv.IsLoopback() {
		return fmt.Errorf("--cluster requires an explicit --advertise-host <this host's peer-reachable IP>")
	}
	listen := net.ParseIP(kc.Listen)
	if listen == nil || listen.IsLoopback() {
		return fmt.Errorf("--cluster requires --listen 0.0.0.0 (or this node's advertised address); %s is not reachable by peers or clients", kc.Listen)
	}
	if want := config.DeriveKafkaClusterID(kc.Cluster); kc.ClusterID != "" && kc.ClusterID != want {
		return fmt.Errorf("--cluster-id %s does not match the id derived from --cluster %q (%s); omit --cluster-id or use the derived value on every host", kc.ClusterID, kc.Cluster, want)
	}
	return nil
}

// resolveKafkaIdentity reconciles cluster/node ids with the data dir. A fresh
// single-node install keeps the old clear-and-regenerate behavior; a fresh
// cluster keeps the deterministic id derived from the cluster name.
func resolveKafkaIdentity(kc *config.KafkaConfig, opts kafkaInstallOptions, present bool) error {
	if !present && !kc.Clustered() {
		if !opts.clusterIDSet {
			kc.ClusterID = ""
		}
		if !opts.nodeIDSet {
			kc.NodeID = 0
		}
	}
	meta, err := kafka.ReadMetaProperties(kc.DataDir)
	if err != nil {
		return err
	}
	if !present && !opts.clusterSet && meta != nil && meta.HasNodeID && meta.NodeID != 1 {
		return fmt.Errorf("data dir %s belongs to Kafka cluster node %d; reinstall it with the original --cluster and --node-id", kc.DataDir, meta.NodeID)
	}
	if err := kafka.AdoptMetaData(kc); err != nil {
		return err
	}
	if !kc.Clustered() {
		if kc.NodeID == 0 {
			kc.NodeID = 1
		}
		if kc.ClusterID == "" {
			id, err := kafka.GenerateClusterID()
			if err != nil {
				return err
			}
			kc.ClusterID = id
		}
		return nil
	}
	if kc.NodeID == 0 {
		return fmt.Errorf("cannot determine this node's Kafka cluster node id: pass --node-id (the data dir has no meta.properties yet)")
	}
	if kc.ClusterID == "" {
		kc.ClusterID = config.DeriveKafkaClusterID(kc.Cluster)
	}
	return nil
}

func kafkaPeerSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	byID := make(map[int]config.KafkaPeer, len(a))
	for _, raw := range a {
		peer, err := config.ParseKafkaPeer(raw)
		if err != nil {
			return false
		}
		if _, dup := byID[peer.ID]; dup {
			return false
		}
		byID[peer.ID] = peer
	}
	for _, raw := range b {
		peer, err := config.ParseKafkaPeer(raw)
		if err != nil {
			return false
		}
		prev, ok := byID[peer.ID]
		if !ok || prev.Port != peer.Port || !kafkaHostEqual(prev.Host, peer.Host) {
			return false
		}
	}
	return true
}

func kafkaHostEqual(a, b string) bool {
	ai, bi := net.ParseIP(a), net.ParseIP(b)
	if ai != nil && bi != nil {
		return ai.Equal(bi)
	}
	return a == b
}

func kafkaVoterStrings(voters []config.KafkaPeer) string {
	parts := make([]string, 0, len(voters))
	for _, voter := range voters {
		parts = append(parts, voter.String())
	}
	return strings.Join(parts, ",")
}

func kafkaLocalVoterString(voters []config.KafkaPeer, id int) string {
	for _, voter := range voters {
		if voter.ID == id {
			return voter.String()
		}
	}
	return "(unknown)"
}

func runAddonInstallKafka(cmd *cobra.Command) error {
	name := kafkaFlagName(cmd)
	if !config.ValidKafkaName(name) {
		return fmt.Errorf("invalid Kafka instance name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	fmt.Printf("=== pg addon install kafka (%s) ===\n", name)

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if c.Addons.Kafka == nil {
		c.Addons.Kafka = make(map[string]config.KafkaConfig)
	}
	opts := readKafkaInstallOptions(cmd.Flags())
	force, _ := cmd.Flags().GetBool("force")
	c.ApplyDefaults()
	stored, present := c.Addons.Kafka[name]

	if present && !force {
		if err := checkKafkaNoOpFlags(stored, opts); err != nil {
			return err
		}
		state := "stopped"
		if m, err := podman.NewKafkaManager(c); err == nil {
			if running, _ := m.ContainerRunning(stored.ContainerName); running {
				state = "running"
			}
		}
		tlsInfo := ""
		if stored.TLS {
			tlsInfo = fmt.Sprintf(", tls %s", kafka.AdvertisedTLSEndpoint(stored))
		}
		fmt.Printf("✓ kafka %q already installed (%s, broker %s%s, controller %s, node %d) — re-install with --force to rewrite the config and recreate the container\n",
			name, state, kafka.AdvertisedBrokerEndpoint(stored), tlsInfo, kafka.LocalControllerEndpoint(stored), stored.NodeID)
		if stored.Clustered() {
			if voters, err := config.KafkaVoters(stored); err == nil {
				rf := config.KafkaReplicationFactor(len(voters))
				fmt.Printf("  Cluster:     %s (cluster id %s)\n", stored.Cluster, stored.ClusterID)
				fmt.Printf("  Node:        %d of %d voters\n", stored.NodeID, len(voters))
				fmt.Printf("  Voter:       %s\n", kafkaLocalVoterString(voters, stored.NodeID))
				fmt.Printf("  Voters:      %s\n", kafkaVoterStrings(voters))
				fmt.Printf("  Replication: RF %d / minISR %d (topic default)\n", rf, config.KafkaMinISR(rf))
			}
		}
		return nil
	}

	kc := config.KafkaConfig{Name: name}
	if present {
		kc = stored
	}
	kc.Name = name
	if err := applyKafkaInstallFlags(&kc, cmd.Flags(), opts); err != nil {
		return err
	}
	c.Addons.Kafka[name] = kc
	c.ApplyDefaults()
	kc = c.Addons.Kafka[name]

	if present {
		if err := checkKafkaTopologyImmutable(stored, kc); err != nil {
			return err
		}
	}
	if err := validateKafkaClusterInstall(kc, opts, present); err != nil {
		return err
	}
	if err := resolveKafkaIdentity(&kc, opts, present); err != nil {
		return err
	}
	c.Addons.Kafka[name] = kc
	c.ApplyDefaults()
	kc = c.Addons.Kafka[name]

	if kc.TLS && kc.TLSPEM != "" {
		info, err := kafka.ValidateTLSPEM(kc.TLSPEM)
		if err != nil {
			return fmt.Errorf("--tls-pem: %w", err)
		}
		if !info.CoversHost(kc.AdvertiseHost) {
			if kc.Clustered() {
				return fmt.Errorf("--tls-pem certificate does not cover advertise host %q; cluster peers and clients connect to that address, so the certificate must include it as an IP or DNS SAN", kc.AdvertiseHost)
			}
			fmt.Printf("  [!] TLS certificate does not cover advertise host %q; clients verifying the hostname will fail\n", kc.AdvertiseHost)
		}
	}
	if err := c.Validate(); err != nil {
		return err
	}

	var voters []config.KafkaPeer
	if kc.Clustered() {
		voters, err = config.KafkaVoters(kc)
		if err != nil {
			return err
		}
		if len(voters) == 2 {
			fmt.Printf("  [!] Kafka cluster %q has 2 voters: quorum is 2, so stopping either node makes the metadata quorum unavailable. Use 3 or more voters (odd is best) for fault tolerance.\n", kc.Cluster)
		}
	}

	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	if err := m.EnsureImage(kc.ImageTag); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] image: %s\n", kc.ImageTag)

	// Persist before starting so a failed first boot remains inspectable and
	// the generated cluster id is not lost.
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}
	if err := m.EnsureContainer(&kc); err != nil {
		m.LogsTail(kc.ContainerName, 20)
		return fmt.Errorf("  [X] %v", err)
	}

	fmt.Println()
	fmt.Printf("✓ Kafka %q installed\n", name)
	fmt.Printf("  Version:     %s\n", kc.Version)
	fmt.Printf("  Image:       %s\n", kc.ImageTag)
	fmt.Printf("  Container:   %s\n", kc.ContainerName)
	fmt.Printf("  Broker:      %s\n", kafka.AdvertisedBrokerEndpoint(kc))
	if kc.TLS {
		fmt.Printf("  TLS:         %s (%s)\n", kafka.AdvertisedTLSEndpoint(kc), kafkaTLSSource(kc))
	}
	fmt.Printf("  Controller:  %s\n", kafka.LocalControllerEndpoint(kc))
	fmt.Printf("  Node ID:     %d\n", kc.NodeID)
	fmt.Printf("  Cluster ID:  %s\n", kc.ClusterID)
	if kc.Heap != "" {
		fmt.Printf("  JVM heap:    %s (KAFKA_HEAP_OPTS=-Xms%s -Xmx%s)\n", kc.Heap, kc.Heap, kc.Heap)
	}
	if kc.Clustered() {
		rf := config.KafkaReplicationFactor(len(voters))
		fmt.Printf("  Cluster:     %s\n", kc.Cluster)
		fmt.Printf("  Node:        %d of %d voters\n", kc.NodeID, len(voters))
		fmt.Printf("  Voter:       %s\n", kafkaLocalVoterString(voters, kc.NodeID))
		fmt.Printf("  Voters:      %s\n", kafkaVoterStrings(voters))
		fmt.Printf("  Replication: RF %d / minISR %d (topic default)\n", rf, config.KafkaMinISR(rf))
	}
	fmt.Printf("  Data dir:    %s\n", kc.DataDir)
	fmt.Printf("  Config:      %s\n", kc.ConfigPath)
	fmt.Println()
	fmt.Printf("  Status:      pg addon kafka status --name %s\n", name)
	fmt.Printf("  Create topic: pg addon kafka topic create demo --partitions 3 --name %s\n", name)
	fmt.Printf("  Shell:       pg addon kafka shell --name %s\n", name)
	fmt.Printf("  Logs:        pg logs addon kafka --name %s\n", name)
	return nil
}

func runAddonRemoveKafka(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name := kafkaFlagName(cmd)
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	c.ApplyDefaults()
	kc, ok := c.Addons.Kafka[name]
	if !ok {
		return fmt.Errorf("kafka %q not found (see 'pg addon list')", name)
	}
	fmt.Printf("=== pg addon remove kafka (%s) ===\n", name)

	if kc.Clustered() {
		if voters, err := config.KafkaVoters(kc); err == nil {
			n := len(voters)
			fmt.Printf("  [!] Kafka cluster %q: this removes node %d on THIS host only. pgcli never contacts the other nodes; their static voter lists still reference this node.\n", kc.Cluster, kc.NodeID)
			fmt.Printf("  [!] %d voters tolerate %d stopped node(s) (quorum = %d).\n", n, (n-1)/2, n/2+1)
			if cleanData {
				fmt.Printf("  [!] --clean-data deletes this node's formatted storage; rejoining requires a fresh data dir with cluster id %s and node id %d, and the same --peer list.\n", kc.ClusterID, kc.NodeID)
			}
		}
	}

	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainer(&kc); err != nil {
		return err
	}
	delete(c.Addons.Kafka, name)
	if len(c.Addons.Kafka) == 0 {
		c.Addons.Kafka = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	if cleanData {
		if err := m.CleanData(&kc); err != nil {
			return err
		}
	} else {
		fmt.Printf("  [i] kept %s (data + config) — pass --clean-data to drop\n", filepath.Dir(filepath.Dir(kc.ConfigPath)))
	}
	fmt.Printf("✓ Kafka %q removed\n", name)
	return nil
}

func runAddonStartKafka(cmd *cobra.Command) error {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	already, err := m.StartContainer(&kc)
	if err != nil {
		m.LogsTail(kc.ContainerName, 20)
		return err
	}
	if already {
		fmt.Printf("✓ kafka %q already running (broker %s)\n", name, kafka.AdvertisedBrokerEndpoint(kc))
		return nil
	}
	fmt.Printf("✓ kafka %q started (broker %s)\n", name, kafka.AdvertisedBrokerEndpoint(kc))
	if kc.TLS {
		fmt.Printf("  TLS: %s (%s)\n", kafka.AdvertisedTLSEndpoint(kc), kafkaTLSSource(kc))
	}
	return nil
}

func runAddonStopKafka(cmd *cobra.Command) error {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	wasRunning, err := m.Stop(kc.ContainerName)
	if err != nil {
		return err
	}
	if !wasRunning {
		fmt.Printf("✓ kafka %q already stopped\n", name)
		return nil
	}
	fmt.Printf("✓ kafka %q stopped\n", name)
	return nil
}

// --- status / shell / topic / cluster -------------------------------------

func runAddonKafkaStatus(cmd *cobra.Command) error {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	state := "stopped"
	if running, _ := m.ContainerRunning(kc.ContainerName); running {
		state = "running"
	}
	topology := "single-node combined KRaft"
	if kc.Clustered() {
		topology = fmt.Sprintf("cluster %q, node %d", kc.Cluster, kc.NodeID)
	}
	fmt.Printf("kafka %q — %s (%s)\n", name, state, topology)
	fmt.Printf("  Version:     %s\n", kc.Version)
	fmt.Printf("  Image:       %s\n", kc.ImageTag)
	fmt.Printf("  Container:   %s\n", kc.ContainerName)
	fmt.Printf("  Broker:      %s\n", kafka.AdvertisedBrokerEndpoint(kc))
	if kc.TLS {
		fmt.Printf("  TLS:         %s\n", kafka.AdvertisedTLSEndpoint(kc))
		if kc.TLSPEM != "" {
			fmt.Printf("  TLS cert:    %s (bring your own)\n", kc.TLSPEM)
		} else {
			fmt.Printf("  TLS CA:      %s\n", kafka.TLSCAPath(kc))
		}
	}
	fmt.Printf("  Controller:  %s\n", kafka.LocalControllerEndpoint(kc))
	fmt.Printf("  Listen:      %s\n", kc.Listen)
	fmt.Printf("  Node ID:     %d\n", kc.NodeID)
	fmt.Printf("  Cluster ID:  %s\n", kc.ClusterID)
	if kc.Heap != "" {
		fmt.Printf("  JVM heap:    %s\n", kc.Heap)
	}
	if kc.Clustered() {
		if voters, err := config.KafkaVoters(kc); err == nil {
			rf := config.KafkaReplicationFactor(len(voters))
			fmt.Printf("  Cluster:     %s\n", kc.Cluster)
			fmt.Printf("  Node:        %d of %d voters\n", kc.NodeID, len(voters))
			fmt.Printf("  Voter:       %s\n", kafkaLocalVoterString(voters, kc.NodeID))
			fmt.Printf("  Voters:      %s\n", kafkaVoterStrings(voters))
			fmt.Printf("  Replication: RF %d / minISR %d (topic default)\n", rf, config.KafkaMinISR(rf))
		} else {
			fmt.Printf("  [!] voter list is invalid: %v\n", err)
		}
	}
	fmt.Printf("  Data dir:    %s\n", kc.DataDir)
	fmt.Printf("  Config:      %s\n", kc.ConfigPath)
	fmt.Printf("  Settings:    %d override(s)\n", len(kc.Settings))
	fmt.Printf("  Logs:        pg logs addon kafka --name %s\n", name)
	return nil
}

func runAddonKafkaShell(cmd *cobra.Command, args []string) error {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return err
	}
	if running, _ := m.ContainerRunning(kc.ContainerName); !running {
		fmt.Fprintf(os.Stderr, "[!] kafka %q is not running — start it with: pg addon start kafka --name %s\n", name, name)
	}
	return m.Exec(&kc, true, args...)
}

func requireKafkaRunning(cmd *cobra.Command) (*podman.KafkaManager, config.KafkaConfig, error) {
	name := kafkaFlagName(cmd)
	c, _, kc, err := resolveKafkaInstance(name)
	if err != nil {
		return nil, kc, err
	}
	m, err := podman.NewKafkaManager(c)
	if err != nil {
		return nil, kc, err
	}
	if running, _ := m.ContainerRunning(kc.ContainerName); !running {
		return nil, kc, fmt.Errorf("kafka %q is not running — 'pg addon start kafka --name %s' first", name, name)
	}
	return m, kc, nil
}

var kafkaTopicNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,249}$`)

var kafkaRetentionRe = regexp.MustCompile(`^([0-9]+)(ms|s|m|h|d|w)?$`)

// parseKafkaRetention converts a human duration to milliseconds for
// retention.ms. Plain numbers are milliseconds, matching Kafka; -1,
// "infinite" and "forever" keep messages indefinitely.
func parseKafkaRetention(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "-1", "infinite", "forever":
		return -1, nil
	}
	m := kafkaRetentionRe.FindStringSubmatch(trimmed)
	if m == nil {
		return 0, fmt.Errorf("invalid retention %q: use ms/s/m/h/d/w (e.g. 30m, 12h, 7d, 4w), plain milliseconds, or -1 for infinite", value)
	}
	multiplier := int64(1)
	switch m[2] {
	case "", "ms":
	case "s":
		multiplier = 1000
	case "m":
		multiplier = 60 * 1000
	case "h":
		multiplier = 60 * 60 * 1000
	case "d":
		multiplier = 24 * 60 * 60 * 1000
	case "w":
		multiplier = 7 * 24 * 60 * 60 * 1000
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("retention %q is too large (max %d ms)", value, int64(math.MaxInt64))
	}
	return n * multiplier, nil
}

func validKafkaTopicName(topic string) bool {
	return kafkaTopicNameRe.MatchString(topic) && topic != "." && topic != ".."
}

func runAddonKafkaTopicCreate(cmd *cobra.Command, args []string) error {
	topic := args[0]
	if !validKafkaTopicName(topic) {
		return fmt.Errorf("invalid topic name %q: use letters, digits, '.', '_' and '-' (max 249 chars)", topic)
	}
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	createArgs := []string{"--create", "--topic", topic}
	if cmd.Flags().Changed("partitions") {
		partitions, _ := cmd.Flags().GetInt("partitions")
		if partitions < 1 {
			return fmt.Errorf("--partitions must be >= 1")
		}
		createArgs = append(createArgs, "--partitions", strconv.Itoa(partitions))
	}
	replicationFactor, _ := cmd.Flags().GetInt("replication-factor")
	if replicationFactor < 0 {
		return fmt.Errorf("--replication-factor must be >= 0 (0 = this instance's default)")
	}
	if replicationFactor == 0 {
		voters, err := config.KafkaVoters(kc)
		if err != nil {
			return err
		}
		replicationFactor = config.KafkaReplicationFactor(len(voters))
	}
	createArgs = append(createArgs, "--replication-factor", strconv.Itoa(replicationFactor))
	configs, _ := cmd.Flags().GetStringArray("config")
	for _, entry := range configs {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" || strings.ContainsAny(entry, "\x00\r\n") {
			return fmt.Errorf("invalid --config %q: use KEY=VALUE", entry)
		}
		createArgs = append(createArgs, "--config", entry)
	}
	if out, err := m.Topic(&kc, createArgs...); err != nil {
		return fmt.Errorf("creating topic %q: %w (%s)", topic, err, strings.TrimSpace(out))
	}
	fmt.Printf("✓ topic %q created on kafka %q (replication factor %d)\n", topic, kc.Name, replicationFactor)
	return nil
}

func runAddonKafkaTopicList(cmd *cobra.Command, _ []string) error {
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	listArgs := []string{"--list"}
	if exclude, _ := cmd.Flags().GetBool("exclude-internal"); exclude {
		listArgs = append(listArgs, "--exclude-internal")
	}
	out, err := m.Topic(&kc, listArgs...)
	if err != nil {
		return fmt.Errorf("listing topics: %w (%s)", err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	return nil
}

func runAddonKafkaTopicDescribe(cmd *cobra.Command, args []string) error {
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.Topic(&kc, "--describe", "--topic", args[0])
	if err != nil {
		return fmt.Errorf("describing topic %q: %w (%s)", args[0], err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	return nil
}

func runAddonKafkaTopicDelete(cmd *cobra.Command, args []string) error {
	topic := args[0]
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Delete topic %q on kafka %q? All of its messages are deleted. [y/N] ", topic, kc.Name)) {
		fmt.Println("aborted")
		return nil
	}
	out, err := m.Topic(&kc, "--delete", "--topic", topic)
	if err != nil {
		return fmt.Errorf("deleting topic %q: %w (%s)", topic, err, strings.TrimSpace(out))
	}
	fmt.Printf("✓ topic %q deleted\n", topic)
	return nil
}

func runAddonKafkaTopicRetention(cmd *cobra.Command, args []string) error {
	topic := args[0]
	if !validKafkaTopicName(topic) {
		return fmt.Errorf("invalid topic name %q: use letters, digits, '.', '_' and '-' (max 249 chars)", topic)
	}
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		out, err := m.TopicConfigs(&kc, "--describe", "--entity-type", "topics", "--entity-name", topic)
		if err != nil {
			return fmt.Errorf("reading retention for topic %q: %w (%s)", topic, err, strings.TrimSpace(out))
		}
		fmt.Print(out)
		return nil
	}
	retention, err := parseKafkaRetention(args[1])
	if err != nil {
		return err
	}
	out, err := m.TopicConfigs(&kc,
		"--alter", "--entity-type", "topics", "--entity-name", topic,
		"--add-config", "retention.ms="+strconv.FormatInt(retention, 10))
	if err != nil {
		return fmt.Errorf("setting retention for topic %q: %w (%s)", topic, err, strings.TrimSpace(out))
	}
	if retention == -1 {
		fmt.Printf("✓ topic %q retention set to infinite (retention.ms=-1)\n", topic)
		return nil
	}
	fmt.Printf("✓ topic %q retention set to %s (retention.ms=%d)\n", topic, args[1], retention)
	return nil
}

func runAddonKafkaGroupList(cmd *cobra.Command, _ []string) error {
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.ConsumerGroups(&kc, "--list")
	if err != nil {
		return fmt.Errorf("listing consumer groups: %w (%s)", err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	return nil
}

func runAddonKafkaGroupDescribe(cmd *cobra.Command, args []string) error {
	group := args[0]
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.ConsumerGroups(&kc, "--describe", "--group", group)
	if err != nil {
		return fmt.Errorf("describing consumer group %q: %w (%s)", group, err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	return nil
}

func runAddonKafkaClusterStatus(cmd *cobra.Command) error {
	m, kc, err := requireKafkaRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.ClusterStatus(&kc)
	if err != nil {
		return fmt.Errorf("reading metadata quorum: %w (%s)", err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	return nil
}

// --- command tree ----------------------------------------------------------

var addonKafkaCmd = &cobra.Command{
	Use:   "kafka",
	Short: "Manage Kafka (KRaft) container instances",
	Long: `Manage Kafka add-on instances (official apache/kafka image).

Kafka 4.x is KRaft-only: the broker and metadata controller run in one process
(combined mode) and ZooKeeper is not used. pgcli pins a cluster id, renders
server.properties, bind-mounts the data dir and starts the container on the
host network. Single-node installs listen on 127.0.0.1 by default and have no
authentication — keep them on a trusted network or behind a firewall.

Multi-host clusters use a static KRaft quorum: run "pg addon install kafka" on
every host with the same --cluster name, a unique 1-based --node-id, that
host's --advertise-host, an explicit --controller-port, and --peer entries for
all other controllers as ID@HOST:PORT. The cluster id is derived from the
cluster name, so every host computes the same value. Membership is immutable
after install; there is no online controller add/remove.

TLS is opt-in: --tls adds an SSL listener (default port 9093) next to the
plaintext broker and generates a certificate from pgcli's self-signed CA.
--tls-pem PATH installs your own combined PEM instead: all certificate blocks
(leaf first) followed by the matching private key in PKCS#8, PKCS#1 or SEC1
form. It implies --tls. For example, using a certificate from pg cert:
  pg cert --host 127.0.0.1,10.0.0.5 --cert-file kafka.crt --key-file kafka.key
  cat kafka.crt kafka.key > kafka-server.pem
  pg addon install kafka --tls-pem kafka-server.pem
A self-signed certificate is only trusted by clients that are configured with
the same certificate as their CA; include the advertise host in --host. Each
cluster node uses its own CA: concatenate the nodes' ca.crt files into one
bundle for clients. CONTROLLER and inter-broker traffic stays PLAINTEXT; --tls
only encrypts client listeners.
The plaintext listener stays enabled for compatibility, so a wildcard --listen
still exposes it; internal management commands keep using loopback PLAINTEXT.

Subcommands:
  status    Show container state, endpoints, node/cluster ids and paths
  shell     Open bash in the container
  topic     Create, list, describe or delete topics; show or set retention
  group     List consumer groups and show their offsets and lag
  cluster   Show the KRaft metadata quorum status

Examples:
  pg addon install kafka
  pg addon install kafka --name k1 --broker-port 9092 --controller-port 19093
  pg addon install kafka --name k1 --tls --tls-port 9093
  pg addon install kafka --name k1 --tls-pem kafka-server.pem
  # node 1 on 10.0.0.11, node 2 on 10.0.0.12, node 3 on 10.0.0.13
  pg addon install kafka --name prod --cluster prod --node-id 1 --listen 0.0.0.0 \
    --advertise-host 10.0.0.11 --controller-port 19093 \
    --peer 2@10.0.0.12:19093 --peer 3@10.0.0.13:19093
  pg addon install kafka --name prod --cluster prod --node-id 2 --listen 0.0.0.0 \
    --advertise-host 10.0.0.12 --controller-port 19093 \
    --peer 1@10.0.0.11:19093 --peer 3@10.0.0.13:19093
  pg addon install kafka --name prod --cluster prod --node-id 3 --listen 0.0.0.0 \
    --advertise-host 10.0.0.13 --controller-port 19093 \
    --peer 1@10.0.0.11:19093 --peer 2@10.0.0.12:19093
  pg addon kafka topic create demo --partitions 3 --name k1
  pg addon kafka topic list --exclude-internal --name k1
  pg addon kafka topic describe demo --name k1
  pg addon kafka topic delete demo --name k1 --yes
  pg addon kafka topic retention demo 7d --name k1
  pg addon kafka group list --name k1
  pg addon kafka group describe demo-group --name k1
  pg addon kafka cluster status --name k1
  pg addon kafka shell --name k1
  pg logs addon kafka --name k1 -f
  pg addon remove kafka --name k1 --clean-data`,
}

var addonKafkaStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Kafka container state, endpoints and paths",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonKafkaStatus(cmd) },
}

var addonKafkaShellCmd = &cobra.Command{
	Use:   "shell [-- COMMAND...]",
	Short: "Open bash (or run a command) in the Kafka container",
	Args:  cobra.ArbitraryArgs,
	RunE:  runAddonKafkaShell,
}

var addonKafkaTopicCmd = &cobra.Command{
	Use:   "topic",
	Short: "Create, list, describe and delete topics",
}

var addonKafkaTopicCreateCmd = &cobra.Command{
	Use:   "create NAME",
	Short: "Create a topic",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonKafkaTopicCreate,
}

var addonKafkaTopicListCmd = &cobra.Command{
	Use:   "list",
	Short: "List topics",
	RunE:  runAddonKafkaTopicList,
}

var addonKafkaTopicDescribeCmd = &cobra.Command{
	Use:   "describe NAME",
	Short: "Describe a topic's partitions and replicas",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonKafkaTopicDescribe,
}

var addonKafkaTopicDeleteCmd = &cobra.Command{
	Use:   "delete NAME",
	Short: "Delete a topic and all of its messages",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonKafkaTopicDelete,
}

var addonKafkaTopicRetentionCmd = &cobra.Command{
	Use:   "retention NAME [DURATION]",
	Short: "Show or set a topic's message retention time",
	Long: `Show or set retention.ms for a topic.

With DURATION, the value accepts ms/s/m/h/d/w suffixes (30m, 12h, 7d, 4w),
plain milliseconds, or -1 / "infinite" / "forever" to keep messages
indefinitely. Without DURATION, the topic's dynamic config overrides are
printed; an empty result means the broker default applies.

Retention is time-based: segments older than retention.ms are deleted even
if they were never consumed.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runAddonKafkaTopicRetention,
}

var addonKafkaGroupCmd = &cobra.Command{
	Use:   "group",
	Short: "List and describe consumer groups",
}

var addonKafkaGroupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List consumer groups",
	RunE:  runAddonKafkaGroupList,
}

var addonKafkaGroupDescribeCmd = &cobra.Command{
	Use:   "describe GROUP",
	Short: "Show a group's members, offsets and lag",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonKafkaGroupDescribe,
}

var addonKafkaClusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Inspect the KRaft metadata quorum",
}

var addonKafkaClusterStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the metadata quorum leader and voters",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonKafkaClusterStatus(cmd) },
}

func init() {
	addonCmd.AddCommand(addonKafkaCmd)
	addonKafkaCmd.AddCommand(addonKafkaStatusCmd, addonKafkaShellCmd, addonKafkaTopicCmd, addonKafkaGroupCmd, addonKafkaClusterCmd)
	addonKafkaTopicCmd.AddCommand(addonKafkaTopicCreateCmd, addonKafkaTopicListCmd, addonKafkaTopicDescribeCmd, addonKafkaTopicDeleteCmd, addonKafkaTopicRetentionCmd)
	addonKafkaGroupCmd.AddCommand(addonKafkaGroupListCmd, addonKafkaGroupDescribeCmd)
	addonKafkaClusterCmd.AddCommand(addonKafkaClusterStatusCmd)

	for _, sub := range []*cobra.Command{
		addonKafkaStatusCmd, addonKafkaShellCmd,
		addonKafkaTopicCreateCmd, addonKafkaTopicListCmd, addonKafkaTopicDescribeCmd, addonKafkaTopicDeleteCmd,
		addonKafkaTopicRetentionCmd,
		addonKafkaGroupListCmd, addonKafkaGroupDescribeCmd,
		addonKafkaClusterStatusCmd,
	} {
		sub.Flags().String("name", "", "kafka instance name (default \"kafka\")")
	}

	addonKafkaTopicCreateCmd.Flags().Int("partitions", 0, "number of partitions (default: broker num.partitions, 1)")
	addonKafkaTopicCreateCmd.Flags().Int("replication-factor", 0, "replication factor (0 = this instance's default: 1 single-node, min(3, voters) in a cluster)")
	addonKafkaTopicCreateCmd.Flags().StringArray("config", nil, "topic config KEY=VALUE (repeatable)")
	addonKafkaTopicListCmd.Flags().Bool("exclude-internal", false, "hide Kafka's internal topics")
	addonKafkaTopicDeleteCmd.Flags().Bool("yes", false, "skip the confirmation prompt")

	addonInstallCmd.Flags().Int("broker-port", 0, "Kafka broker host port (0=auto-assign from kafka_broker_start_port, default 9092)")
	addonInstallCmd.Flags().Int("controller-port", 0, "Kafka KRaft controller host port (0=auto-assign from kafka_controller_start_port, default 19093; required explicitly in --cluster mode because peers embed it)")
	addonInstallCmd.Flags().Int("tls-port", 0, "Kafka SSL (TLS) listener host port (0=auto-assign from kafka_tls_start_port, default 9093; requires --tls)")
	addonInstallCmd.Flags().String("tls-pem", "", "combined PEM (certificate chain + private key) for the Kafka TLS listener; implies --tls and replaces the generated certificate (pass an empty value with --tls to fall back to the generated certificate)")
	addonInstallCmd.Flags().String("heap", "", "Kafka JVM heap size (e.g. 512m, 2g, 4g), applied as KAFKA_HEAP_OPTS=-Xms<size> -Xmx<size>; empty keeps the image default (-Xms1G -Xmx1G); changing it needs --force")
	addonInstallCmd.Flags().String("cluster-id", "", "Kafka KRaft cluster id (in --cluster mode it must equal the id derived from the cluster name; otherwise: adopt meta.properties, else generate a random 22-char id)")
}
