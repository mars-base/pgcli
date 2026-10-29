package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

// resolveRocketMQInstance loads the config, applies defaults and returns the
// named RocketMQ instance.
func resolveRocketMQInstance(name string) (*config.Config, string, config.RocketMQConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.RocketMQConfig{}, err
	}
	c.ApplyDefaults()
	rc, ok := c.Addons.RocketMQ[name]
	if !ok {
		return nil, "", config.RocketMQConfig{}, fmt.Errorf("rocketmq %q not found (see 'pg addon list')", name)
	}
	return c, path, rc, nil
}

func rocketMQFlagName(cmd *cobra.Command) string {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "rocketmq"
	}
	return name
}

func rocketMQService(cmd *cobra.Command) string {
	service, _ := cmd.Flags().GetString("service")
	if service == "" {
		service = podman.RocketMQRoleBroker
	}
	return service
}

func validRocketMQService(service string) bool {
	switch service {
	case podman.RocketMQRoleNamesrv, podman.RocketMQRoleBroker, podman.RocketMQRoleProxy:
		return true
	}
	return false
}

func parseRocketMQConfigOptions(options []string) (map[string]string, error) {
	settings := make(map[string]string, len(options))
	for _, opt := range options {
		key, value, ok := strings.Cut(opt, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --config-option %q: use KEY=VALUE, e.g. flushDiskType=SYNC_FLUSH", opt)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("invalid --config-option %q: value may not contain control characters", opt)
		}
		settings[key] = value
	}
	if err := config.ValidateRocketMQSettings(settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// --- install / lifecycle ----------------------------------------------------

type rocketMQInstallOptions struct {
	cluster           string
	peers             []string
	advertiseHost     string
	controllerID      string
	namesrvPort       int
	brokerPort        int
	proxyPort         int
	grpcPort          int
	controllerPort    int
	clusterSet        bool
	peersSet          bool
	advertiseHostSet  bool
	controllerIDSet   bool
	namesrvPortSet    bool
	brokerPortSet     bool
	proxyPortSet      bool
	grpcPortSet       bool
	controllerPortSet bool
}

func readRocketMQInstallOptions(fs *pflag.FlagSet) rocketMQInstallOptions {
	opts := rocketMQInstallOptions{
		clusterSet:        fs.Changed("cluster"),
		peersSet:          fs.Changed("peer"),
		advertiseHostSet:  fs.Changed("advertise-host"),
		controllerIDSet:   fs.Changed("controller-id"),
		namesrvPortSet:    fs.Changed("namesrv-port"),
		brokerPortSet:     fs.Changed("broker-port"),
		proxyPortSet:      fs.Changed("proxy-port"),
		grpcPortSet:       fs.Changed("grpc-port"),
		controllerPortSet: fs.Changed("controller-port"),
	}
	opts.cluster, _ = fs.GetString("cluster")
	opts.peers, _ = fs.GetStringArray("peer")
	opts.advertiseHost, _ = fs.GetString("advertise-host")
	opts.controllerID, _ = fs.GetString("controller-id")
	opts.namesrvPort, _ = fs.GetInt("namesrv-port")
	opts.brokerPort, _ = fs.GetInt("broker-port")
	opts.proxyPort, _ = fs.GetInt("proxy-port")
	opts.grpcPort, _ = fs.GetInt("grpc-port")
	opts.controllerPort, _ = fs.GetInt("controller-port")
	return opts
}

// rejectRocketMQFlags refuses flags that belong to other addons: RocketMQ
// always uses host networking, has no TLS and no separate node/cluster ids.
func rejectRocketMQFlags(fs *pflag.FlagSet) error {
	for _, name := range []string{
		"listen", "tls", "tls-pem", "tls-cert", "tls-key",
		"node-id", "cluster-id", "join", "image",
	} {
		if fs.Changed(name) {
			return fmt.Errorf("--%s is not supported for RocketMQ", name)
		}
	}
	return nil
}

func applyRocketMQInstallFlags(rc *config.RocketMQConfig, fs *pflag.FlagSet, opts rocketMQInstallOptions) error {
	if fs.Changed("version") {
		rc.Version, _ = fs.GetString("version")
	}
	if rc.Version == "" {
		rc.Version = config.DefaultRocketMQVersion
	}
	imageTag, ok := config.RocketMQImageTagForVersion(rc.Version)
	if !ok {
		return fmt.Errorf("RocketMQ version %q is not supported: use a 5.x release tag, e.g. %s", rc.Version, config.DefaultRocketMQVersion)
	}
	rc.ImageTag = imageTag
	if v, _ := fs.GetString("advertise-host"); v != "" {
		rc.AdvertiseHost = v
	}
	if fs.Changed("namesrv-port") {
		rc.NamesrvPort, _ = fs.GetInt("namesrv-port")
	}
	if fs.Changed("broker-port") {
		rc.BrokerPort, _ = fs.GetInt("broker-port")
	}
	if fs.Changed("proxy-port") {
		rc.ProxyPort, _ = fs.GetInt("proxy-port")
	}
	if fs.Changed("grpc-port") {
		rc.GRPCPort, _ = fs.GetInt("grpc-port")
	}
	if fs.Changed("controller-port") {
		rc.ControllerPort, _ = fs.GetInt("controller-port")
	}
	if v, _ := fs.GetString("data-dir"); v != "" {
		rc.DataDir = v
	}
	if fs.Changed("heap") {
		value, _ := fs.GetString("heap")
		heap, err := config.ParseRocketMQHeap(value)
		if err != nil {
			return err
		}
		rc.Heap = heap
	}
	if fs.Changed("namesrv-heap") {
		value, _ := fs.GetString("namesrv-heap")
		heap, err := config.ParseRocketMQHeap(value)
		if err != nil {
			return err
		}
		rc.NamesrvHeap = heap
	}
	if fs.Changed("proxy-heap") {
		value, _ := fs.GetString("proxy-heap")
		heap, err := config.ParseRocketMQHeap(value)
		if err != nil {
			return err
		}
		rc.ProxyHeap = heap
	}
	if fs.Changed("config-option") {
		options, _ := fs.GetStringArray("config-option")
		settings, err := parseRocketMQConfigOptions(options)
		if err != nil {
			return err
		}
		rc.Settings = settings
	}
	if opts.clusterSet {
		rc.Cluster = opts.cluster
	}
	if opts.peersSet {
		rc.Peers = append([]string(nil), opts.peers...)
	}
	if opts.controllerIDSet {
		rc.ControllerID = opts.controllerID
	}
	return nil
}

// rocketMQPeersEqual compares peer sets by controller id and host, ignoring
// order and an optional local peer.
func rocketMQPeersEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	byID := make(map[string]string, len(a))
	for _, raw := range a {
		peer, err := config.ParseRocketMQPeer(raw)
		if err != nil {
			return false
		}
		if _, dup := byID[peer.ID]; dup {
			return false
		}
		byID[peer.ID] = peer.Host
	}
	for _, raw := range b {
		peer, err := config.ParseRocketMQPeer(raw)
		if err != nil {
			return false
		}
		host, ok := byID[peer.ID]
		if !ok || host != peer.Host {
			return false
		}
	}
	return true
}

func rocketMQPeerStrings(peers []config.RocketMQPeer) string {
	parts := make([]string, 0, len(peers))
	for _, peer := range peers {
		parts = append(parts, peer.String())
	}
	return strings.Join(parts, ", ")
}

// checkRocketMQNoOpFlags rejects identity changes on an installed instance
// without --force. Mutable flags are ignored with a hint to use --force.
func checkRocketMQNoOpFlags(stored config.RocketMQConfig, opts rocketMQInstallOptions) error {
	if !stored.Clustered() {
		if opts.peersSet {
			return fmt.Errorf("--peer requires --cluster (pass the same cluster name on every host)")
		}
		if opts.controllerIDSet {
			return fmt.Errorf("--controller-id requires --cluster")
		}
		if opts.controllerPortSet {
			return fmt.Errorf("--controller-port requires --cluster")
		}
	}
	if opts.clusterSet && opts.cluster != stored.Cluster {
		if stored.Cluster == "" {
			return fmt.Errorf("rocketmq %q is installed as a single node; cluster membership is not an in-place change (remove it with --clean-data first or use a new --name)", stored.Name)
		}
		return fmt.Errorf("rocketmq %q is installed in cluster %q; changing --cluster is not supported", stored.Name, stored.Cluster)
	}
	if opts.controllerIDSet && opts.controllerID != stored.ControllerID {
		return fmt.Errorf("rocketmq %q is installed as controller %s; changing --controller-id is not supported", stored.Name, stored.ControllerID)
	}
	if stored.Clustered() {
		if opts.advertiseHostSet && opts.advertiseHost != stored.AdvertiseHost {
			return fmt.Errorf("rocketmq %q advertises %s; changing --advertise-host would invalidate every peer's --peer list", stored.Name, stored.AdvertiseHost)
		}
		for _, change := range []struct {
			set      bool
			value    int
			expected int
			flag     string
		}{
			{opts.namesrvPortSet, opts.namesrvPort, config.DefaultRocketMQNamesrvPort, "namesrv-port"},
			{opts.brokerPortSet, opts.brokerPort, config.DefaultRocketMQBrokerPort, "broker-port"},
			{opts.proxyPortSet, opts.proxyPort, config.DefaultRocketMQProxyPort, "proxy-port"},
			{opts.grpcPortSet, opts.grpcPort, config.DefaultRocketMQGRPCPort, "grpc-port"},
			{opts.controllerPortSet, opts.controllerPort, config.DefaultRocketMQControllerPort, "controller-port"},
		} {
			if change.set && change.value != change.expected {
				return fmt.Errorf("rocketmq cluster mode fixes --%s at %d; ports cannot be changed on a cluster member", change.flag, change.expected)
			}
		}
	}
	if opts.peersSet {
		normalized, err := config.NormalizeRocketMQPeers(opts.peers, stored.ControllerID, stored.AdvertiseHost)
		if err != nil {
			return err
		}
		if !rocketMQPeersEqual(normalized, stored.Peers) {
			return fmt.Errorf("rocketmq %q is installed with controller peers %s; changing --peer is not supported (the DLedger quorum membership cannot be edited in place)", stored.Name, strings.Join(stored.Peers, ", "))
		}
	}
	return nil
}

// checkRocketMQTopologyImmutable rejects changing cluster identity even with
// --force.
func checkRocketMQTopologyImmutable(stored, next config.RocketMQConfig) error {
	if next.Cluster != stored.Cluster {
		if stored.Cluster == "" {
			return fmt.Errorf("rocketmq %q is installed as a single node; cluster membership is not an in-place change (remove it with --clean-data first or use a new --name)", next.Name)
		}
		return fmt.Errorf("rocketmq %q is installed in cluster %q; changing --cluster is not supported", next.Name, stored.Cluster)
	}
	if !stored.Clustered() {
		return nil
	}
	if next.ControllerID != stored.ControllerID {
		return fmt.Errorf("rocketmq %q is installed as controller %s; changing --controller-id is not supported", next.Name, stored.ControllerID)
	}
	if next.AdvertiseHost != stored.AdvertiseHost {
		return fmt.Errorf("rocketmq %q advertises %s; changing --advertise-host would invalidate every peer's --peer list", next.Name, stored.AdvertiseHost)
	}
	if !rocketMQPeersEqual(next.Peers, stored.Peers) {
		return fmt.Errorf("rocketmq %q is installed with controller peers %s; changing --peer is not supported (the DLedger quorum membership cannot be edited in place)", next.Name, strings.Join(stored.Peers, ", "))
	}
	for _, change := range []struct {
		next, stored int
		flag         string
	}{
		{next.NamesrvPort, stored.NamesrvPort, "namesrv-port"},
		{next.BrokerPort, stored.BrokerPort, "broker-port"},
		{next.ProxyPort, stored.ProxyPort, "proxy-port"},
		{next.GRPCPort, stored.GRPCPort, "grpc-port"},
		{next.ControllerPort, stored.ControllerPort, "controller-port"},
	} {
		if change.next != change.stored {
			return fmt.Errorf("rocketmq cluster mode fixes --%s at %d; ports cannot be changed on a cluster member", change.flag, change.stored)
		}
	}
	return nil
}

func validateRocketMQInstall(rc config.RocketMQConfig, opts rocketMQInstallOptions, present bool) error {
	if !rc.Clustered() {
		if opts.peersSet || len(rc.Peers) > 0 {
			return fmt.Errorf("--peer requires --cluster (pass the same cluster name on every host)")
		}
		if opts.controllerIDSet || rc.ControllerID != "" {
			return fmt.Errorf("--controller-id requires --cluster")
		}
		if opts.controllerPortSet || rc.ControllerPort != 0 {
			return fmt.Errorf("--controller-port requires --cluster (the controller is embedded in the NameServer)")
		}
		return nil
	}
	remotePeers := 0
	for _, raw := range rc.Peers {
		peer, err := config.ParseRocketMQPeer(raw)
		if err != nil {
			return err
		}
		if peer.ID != rc.ControllerID {
			remotePeers++
		}
	}
	if remotePeers == 0 {
		return fmt.Errorf("--cluster requires at least one --peer ID@HOST listing the other controllers (the local peer is added automatically)")
	}
	for _, change := range []struct {
		set      bool
		value    int
		expected int
		flag     string
	}{
		{opts.namesrvPortSet, opts.namesrvPort, config.DefaultRocketMQNamesrvPort, "namesrv-port"},
		{opts.brokerPortSet, opts.brokerPort, config.DefaultRocketMQBrokerPort, "broker-port"},
		{opts.proxyPortSet, opts.proxyPort, config.DefaultRocketMQProxyPort, "proxy-port"},
		{opts.grpcPortSet, opts.grpcPort, config.DefaultRocketMQGRPCPort, "grpc-port"},
		{opts.controllerPortSet, opts.controllerPort, config.DefaultRocketMQControllerPort, "controller-port"},
	} {
		if change.set && change.value != change.expected {
			return fmt.Errorf("rocketmq cluster mode fixes --%s at %d; ports cannot be changed on a cluster member", change.flag, change.expected)
		}
	}
	if !present {
		if !opts.advertiseHostSet {
			return fmt.Errorf("--cluster requires an explicit --advertise-host <this host's peer-reachable IPv4 address>")
		}
		if !opts.controllerIDSet {
			return fmt.Errorf("--cluster requires an explicit --controller-id (e.g. n0) — it must be unique in the DLedger group")
		}
	}
	return nil
}

func runAddonInstallRocketMQ(cmd *cobra.Command) error {
	name := rocketMQFlagName(cmd)
	if !config.ValidRocketMQName(name) {
		return fmt.Errorf("invalid RocketMQ instance name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	fmt.Printf("=== pg addon install rocketmq (%s) ===\n", name)

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if c.Addons.RocketMQ == nil {
		c.Addons.RocketMQ = make(map[string]config.RocketMQConfig)
	}
	fs := cmd.Flags()
	if err := rejectRocketMQFlags(fs); err != nil {
		return err
	}
	opts := readRocketMQInstallOptions(fs)
	force, _ := fs.GetBool("force")
	c.ApplyDefaults()
	stored, present := c.Addons.RocketMQ[name]

	if present && !force {
		if err := checkRocketMQNoOpFlags(stored, opts); err != nil {
			return err
		}
		state := "stopped"
		if m, err := podman.NewRocketMQManager(c); err == nil {
			if running, _, _ := m.ContainerStatus(&stored, podman.RocketMQRoleBroker); running {
				state = "running"
			}
		}
		mode := "single-node"
		if stored.Clustered() {
			mode = fmt.Sprintf("cluster %q, controller %s", stored.Cluster, stored.ControllerID)
		}
		fmt.Printf("✓ rocketmq %q already installed (%s, %s, proxy %s:%d) — re-install with --force to rewrite the config and recreate the containers\n",
			name, state, mode, stored.AdvertiseHost, stored.ProxyPort)
		fmt.Printf("  Namesrv:     %s\n", strings.Join(mustRocketMQNamesrvAddrs(stored), ";"))
		fmt.Printf("  Broker:      %s:%d\n", stored.AdvertiseHost, stored.BrokerPort)
		fmt.Printf("  Proxy gRPC:  %s:%d\n", stored.AdvertiseHost, stored.GRPCPort)
		return nil
	}

	rc := config.RocketMQConfig{Name: name}
	if present {
		rc = stored
	}
	rc.Name = name
	if err := applyRocketMQInstallFlags(&rc, fs, opts); err != nil {
		return err
	}
	c.Addons.RocketMQ[name] = rc
	c.ApplyDefaults()
	rc = c.Addons.RocketMQ[name]

	if present {
		if err := checkRocketMQTopologyImmutable(stored, rc); err != nil {
			return err
		}
	}
	if err := validateRocketMQInstall(rc, opts, present); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}

	var peers []config.RocketMQPeer
	if rc.Clustered() {
		peers, err = config.RocketMQPeers(rc)
		if err != nil {
			return err
		}
		if len(peers) == 2 {
			fmt.Printf("  [!] RocketMQ cluster %q has 2 controller members: DLedger quorum is 2, so stopping either node makes the controller unavailable. Use 3 or more members for fault tolerance.\n", rc.Cluster)
		}
	}

	m, err := podman.NewRocketMQManager(c)
	if err != nil {
		return err
	}
	if err := m.EnsureImage(rc.ImageTag); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] image: %s\n", rc.ImageTag)

	// Persist before starting so a failed first boot remains inspectable.
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}
	if err := m.EnsureContainers(&rc); err != nil {
		m.LogsTail(m.ContainerName(&rc, podman.RocketMQRoleNamesrv), 20)
		return fmt.Errorf("  [X] %v", err)
	}
	if err := m.WaitReady(&rc); err != nil {
		m.LogsTail(m.ContainerName(&rc, podman.RocketMQRoleBroker), 20)
		return fmt.Errorf("  [X] %v", err)
	}

	namesrvAddrs := mustRocketMQNamesrvAddrs(rc)
	fmt.Println()
	fmt.Printf("✓ RocketMQ %q installed\n", name)
	fmt.Printf("  Version:     %s\n", rc.Version)
	fmt.Printf("  Image:       %s\n", rc.ImageTag)
	fmt.Printf("  Containers:  %s, %s, %s\n",
		m.ContainerName(&rc, podman.RocketMQRoleNamesrv),
		m.ContainerName(&rc, podman.RocketMQRoleBroker),
		m.ContainerName(&rc, podman.RocketMQRoleProxy))
	fmt.Printf("  Namesrv:     %s\n", strings.Join(namesrvAddrs, ";"))
	fmt.Printf("  Broker:      %s:%d (HA %d, VIP %d)\n", rc.AdvertiseHost, rc.BrokerPort, config.RocketMQHAPort(rc), config.RocketMQVIPPort(rc))
	fmt.Printf("  Proxy:       %s:%d (remoting), %s:%d (gRPC)\n", rc.AdvertiseHost, rc.ProxyPort, rc.AdvertiseHost, rc.GRPCPort)
	if rc.Clustered() {
		fmt.Printf("  Cluster:     %s\n", rc.Cluster)
		fmt.Printf("  Controller:  %s (%s)\n", rc.ControllerID, strings.Join(mustRocketMQControllerAddrs(rc), ";"))
		fmt.Printf("  Peers:       %s\n", rocketMQPeerStrings(peers))
		fmt.Printf("  Replication: allAckInSyncStateSet=true, minInSyncReplicas=%d\n", config.RocketMQMinInSyncReplicas(rc))
	} else {
		fmt.Printf("  Mode:        single-node (no controller, no replicas)\n")
	}
	fmt.Printf("  JVM heap:    broker %s, namesrv %s, proxy %s\n", rc.Heap, rc.NamesrvHeap, rc.ProxyHeap)
	fmt.Printf("  Data dir:    %s\n", rc.DataDir)
	fmt.Printf("  Config:      %s\n", rc.ConfigDir)
	fmt.Println()
	if rc.Clustered() {
		fmt.Printf("  Install the remaining nodes with the same --cluster and their own --controller-id/--advertise-host/--peer values.\n")
	}
	fmt.Printf("  Status:      pg addon rocketmq status --name %s\n", name)
	fmt.Printf("  Create topic: pg addon rocketmq topic create demo --name %s\n", name)
	fmt.Printf("  Send:        pg addon rocketmq send demo \"hello\" --name %s\n", name)
	fmt.Printf("  Logs:        pg logs addon rocketmq --name %s\n", name)
	return nil
}

// mustRocketMQNamesrvAddrs is only called for validated configs.
func mustRocketMQNamesrvAddrs(rc config.RocketMQConfig) []string {
	addrs, err := config.RocketMQNamesrvAddrs(rc)
	if err != nil {
		return nil
	}
	return addrs
}

func mustRocketMQControllerAddrs(rc config.RocketMQConfig) []string {
	addrs, err := config.RocketMQControllerAddrs(rc)
	if err != nil {
		return nil
	}
	return addrs
}

func runAddonRemoveRocketMQ(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name := rocketMQFlagName(cmd)
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	c.ApplyDefaults()
	rc, ok := c.Addons.RocketMQ[name]
	if !ok {
		return fmt.Errorf("rocketmq %q not found (see 'pg addon list')", name)
	}
	fmt.Printf("=== pg addon remove rocketmq (%s) ===\n", name)
	if rc.Clustered() {
		peers, _ := config.RocketMQPeers(rc)
		n := len(peers)
		fmt.Printf("  [!] RocketMQ cluster %q: this removes controller %s on THIS host only. pgcli never contacts the other nodes; their DLedger peer lists still reference this member.\n", rc.Cluster, rc.ControllerID)
		fmt.Printf("  [!] %d controller members tolerate %d stopped member(s) (quorum = %d).\n", n, (n-1)/2, n/2+1)
		if cleanData {
			fmt.Printf("  [!] --clean-data deletes this member's controller store and broker data; rejoining requires a fresh data dir and the same --cluster/--controller-id/--peer values.\n")
		}
	}

	m, err := podman.NewRocketMQManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainers(&rc); err != nil {
		return err
	}
	delete(c.Addons.RocketMQ, name)
	if len(c.Addons.RocketMQ) == 0 {
		c.Addons.RocketMQ = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	if cleanData {
		if err := m.CleanData(&rc); err != nil {
			return err
		}
	} else {
		fmt.Printf("  [i] kept %s (data + config) — pass --clean-data to drop\n", filepath.Dir(filepath.Dir(rc.ConfigDir)))
	}
	fmt.Printf("✓ RocketMQ %q removed\n", name)
	return nil
}

func runAddonStartRocketMQ(cmd *cobra.Command) error {
	name := rocketMQFlagName(cmd)
	c, _, rc, err := resolveRocketMQInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewRocketMQManager(c)
	if err != nil {
		return err
	}
	already, err := m.StartContainers(&rc)
	if err != nil {
		m.LogsTail(m.ContainerName(&rc, podman.RocketMQRoleBroker), 20)
		return err
	}
	if already {
		fmt.Printf("✓ rocketmq %q already running (proxy %s:%d)\n", name, rc.AdvertiseHost, rc.ProxyPort)
		return nil
	}
	if err := m.WaitReady(&rc); err != nil {
		return err
	}
	fmt.Printf("✓ rocketmq %q started (namesrv %s, proxy %s:%d)\n", name,
		strings.Join(mustRocketMQNamesrvAddrs(rc), ";"), rc.AdvertiseHost, rc.ProxyPort)
	return nil
}

func runAddonStopRocketMQ(cmd *cobra.Command) error {
	name := rocketMQFlagName(cmd)
	c, _, rc, err := resolveRocketMQInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewRocketMQManager(c)
	if err != nil {
		return err
	}
	if err := m.StopContainers(&rc); err != nil {
		return err
	}
	fmt.Printf("✓ rocketmq %q stopped\n", name)
	return nil
}

// --- status / shell / topic / send / cluster -------------------------------

func runAddonRocketMQStatus(cmd *cobra.Command) error {
	name := rocketMQFlagName(cmd)
	c, _, rc, err := resolveRocketMQInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewRocketMQManager(c)
	if err != nil {
		return err
	}
	states := make([]string, 0, 3)
	active := 0
	for _, role := range []string{podman.RocketMQRoleNamesrv, podman.RocketMQRoleBroker, podman.RocketMQRoleProxy} {
		running, exists, err := m.ContainerStatus(&rc, role)
		if err != nil {
			return err
		}
		switch {
		case running:
			states = append(states, role+" running")
			active++
		case exists:
			states = append(states, role+" stopped")
		default:
			states = append(states, role+" absent")
		}
	}
	topology := "single-node"
	if rc.Clustered() {
		topology = fmt.Sprintf("cluster %q, controller %s", rc.Cluster, rc.ControllerID)
	}
	fmt.Printf("rocketmq %q — %s (%s)\n", name, strings.Join(states, ", "), topology)
	fmt.Printf("  Version:     %s\n", rc.Version)
	fmt.Printf("  Image:       %s\n", rc.ImageTag)
	fmt.Printf("  Namesrv:     %s\n", strings.Join(mustRocketMQNamesrvAddrs(rc), ";"))
	fmt.Printf("  Broker:      %s:%d (HA %d, VIP %d)\n", rc.AdvertiseHost, rc.BrokerPort, config.RocketMQHAPort(rc), config.RocketMQVIPPort(rc))
	fmt.Printf("  Proxy:       %s:%d (remoting), %s:%d (gRPC)\n", rc.AdvertiseHost, rc.ProxyPort, rc.AdvertiseHost, rc.GRPCPort)
	if rc.Clustered() {
		peers, err := config.RocketMQPeers(rc)
		if err != nil {
			fmt.Printf("  [!] peer list is invalid: %v\n", err)
		} else {
			fmt.Printf("  Controller:  %s (%s)\n", rc.ControllerID, strings.Join(mustRocketMQControllerAddrs(rc), ";"))
			fmt.Printf("  Peers:       %s\n", rocketMQPeerStrings(peers))
		}
		fmt.Printf("  Replication: minInSyncReplicas=%d\n", config.RocketMQMinInSyncReplicas(rc))
	}
	fmt.Printf("  JVM heap:    broker %s, namesrv %s, proxy %s\n", rc.Heap, rc.NamesrvHeap, rc.ProxyHeap)
	fmt.Printf("  Data dir:    %s\n", rc.DataDir)
	fmt.Printf("  Config:      %s\n", rc.ConfigDir)
	fmt.Printf("  Settings:    %d override(s)\n", len(rc.Settings))
	fmt.Printf("  Logs:        pg logs addon rocketmq --name %s\n", name)

	if rc.Clustered() && active > 0 {
		out, err := m.ClusterList(&rc, false)
		fmt.Println()
		if err != nil {
			fmt.Printf("  [!] controller quorum not ready (no active controller reachable): %v\n", err)
		} else {
			brokers, masters := rocketMQClusterSummary(out, rc.ClusterName())
			if brokers == 0 {
				fmt.Printf("  [!] no broker registered yet — the controller quorum may still be forming; install the remaining members or check `pg logs addon rocketmq --name %s --service broker`\n", name)
			} else {
				fmt.Printf("  Brokers:     %d registered, %d master(s)\n", brokers, masters)
				fmt.Print(out)
				if !strings.HasSuffix(out, "\n") {
					fmt.Println()
				}
			}
		}
	}
	return nil
}

// rocketMQClusterSummary counts broker rows and masters (BID 0) for this
// cluster in clusterList output. The BID column is the third whitespace field
// in both the 5.x (#Cluster Name / #Broker Name / #BID) and 4.x layouts, so it
// cannot be located by splitting the header, whose names contain spaces.
func rocketMQClusterSummary(output, cluster string) (brokers, masters int) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != cluster {
			continue
		}
		brokers++
		if fields[2] == "0" {
			masters++
		}
	}
	return brokers, masters
}

func runAddonRocketMQShell(cmd *cobra.Command, args []string) error {
	name := rocketMQFlagName(cmd)
	service := rocketMQService(cmd)
	if !validRocketMQService(service) {
		return fmt.Errorf("invalid --service %q: use namesrv, broker or proxy", service)
	}
	c, _, rc, err := resolveRocketMQInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewRocketMQManager(c)
	if err != nil {
		return err
	}
	if running, _, _ := m.ContainerStatus(&rc, service); !running {
		fmt.Fprintf(os.Stderr, "[!] rocketmq %q %s container is not running — start it with: pg addon start rocketmq --name %s\n", name, service, name)
	}
	return m.Exec(&rc, service, true, args...)
}

func requireRocketMQRunning(cmd *cobra.Command) (*podman.RocketMQManager, config.RocketMQConfig, error) {
	name := rocketMQFlagName(cmd)
	c, _, rc, err := resolveRocketMQInstance(name)
	if err != nil {
		return nil, rc, err
	}
	m, err := podman.NewRocketMQManager(c)
	if err != nil {
		return nil, rc, err
	}
	if running, _, _ := m.ContainerStatus(&rc, podman.RocketMQRoleNamesrv); !running {
		return nil, rc, fmt.Errorf("rocketmq %q is not running — 'pg addon start rocketmq --name %s' first", name, name)
	}
	return m, rc, nil
}

var rocketMQTopicNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,127}$`)

func validRocketMQTopicName(topic string) bool {
	return rocketMQTopicNameRe.MatchString(topic)
}

var rocketMQSystemTopics = map[string]bool{
	"TBW102":                      true,
	"SELF_TEST_TOPIC":             true,
	"OFFSET_MOVED_EVENT":          true,
	"SCHEDULE_TOPIC_XXXX":         true,
	"TRANS_CHECK_MAX_TIME_TOPIC":  true,
	"DefaultHeartBeatSyncerTopic": true,
}

func rocketMQSystemTopic(topic string) bool {
	return strings.HasPrefix(topic, "%RETRY%") ||
		strings.HasPrefix(topic, "%DLQ%") ||
		strings.HasPrefix(topic, "rmq_sys_") ||
		strings.HasPrefix(topic, "RMQ_SYS_") ||
		strings.HasSuffix(topic, "_REPLY_TOPIC") ||
		rocketMQSystemTopics[topic]
}

func runAddonRocketMQTopicCreate(cmd *cobra.Command, args []string) error {
	topic := args[0]
	if !validRocketMQTopicName(topic) {
		return fmt.Errorf("invalid topic name %q: use letters, digits, '_' and '-' (max 127 chars)", topic)
	}
	queues, _ := cmd.Flags().GetInt("queues")
	if queues < 1 {
		return fmt.Errorf("--queues must be >= 1")
	}
	m, rc, err := requireRocketMQRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.MQAdmin(&rc, "updateTopic",
		"-t", topic,
		"-r", strconv.Itoa(queues),
		"-w", strconv.Itoa(queues),
		"-c", rc.ClusterName())
	if err != nil {
		return fmt.Errorf("creating topic %q: %w (%s)", topic, err, strings.TrimSpace(out))
	}
	fmt.Printf("✓ topic %q created on rocketmq %q (%d read/write queues, cluster %s)\n", topic, rc.Name, queues, rc.ClusterName())
	return nil
}

func runAddonRocketMQTopicList(cmd *cobra.Command, _ []string) error {
	m, rc, err := requireRocketMQRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.MQAdmin(&rc, "topicList")
	if err != nil {
		return fmt.Errorf("listing topics: %w (%s)", err, strings.TrimSpace(out))
	}
	showSystem, _ := cmd.Flags().GetBool("system")
	for line := range strings.SplitSeq(out, "\n") {
		topic := strings.TrimSpace(line)
		if topic == "" {
			continue
		}
		if !showSystem && (rocketMQSystemTopic(topic) || topic == rc.ClusterName()) {
			continue
		}
		fmt.Println(topic)
	}
	return nil
}

func runAddonRocketMQTopicDescribe(cmd *cobra.Command, args []string) error {
	m, rc, err := requireRocketMQRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.MQAdmin(&rc, "topicStatus", "-t", args[0])
	if err != nil {
		return fmt.Errorf("describing topic %q: %w (%s)", args[0], err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	if !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	return nil
}

func runAddonRocketMQTopicDelete(cmd *cobra.Command, args []string) error {
	topic := args[0]
	m, rc, err := requireRocketMQRunning(cmd)
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Delete topic %q on rocketmq %q? All of its messages are deleted. [y/N] ", topic, rc.Name)) {
		fmt.Println("aborted")
		return nil
	}
	out, err := m.MQAdmin(&rc, "deleteTopic", "-t", topic, "-c", rc.ClusterName())
	if err != nil {
		return fmt.Errorf("deleting topic %q: %w (%s)", topic, err, strings.TrimSpace(out))
	}
	fmt.Printf("✓ topic %q deleted\n", topic)
	return nil
}

func runAddonRocketMQSend(cmd *cobra.Command, args []string) error {
	topic, body := args[0], args[1]
	m, rc, err := requireRocketMQRunning(cmd)
	if err != nil {
		return err
	}
	sendArgs := []string{"sendMessage", "-t", topic, "-p", body}
	if key, _ := cmd.Flags().GetString("key"); key != "" {
		sendArgs = append(sendArgs, "-k", key)
	}
	if tags, _ := cmd.Flags().GetString("tags"); tags != "" {
		sendArgs = append(sendArgs, "-c", tags)
	}
	out, err := m.MQAdmin(&rc, sendArgs...)
	if err != nil {
		return fmt.Errorf("sending to topic %q: %w (%s)", topic, err, strings.TrimSpace(out))
	}
	fmt.Print(out)
	if !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	return nil
}

func runAddonRocketMQClusterStatus(cmd *cobra.Command, _ []string) error {
	m, rc, err := requireRocketMQRunning(cmd)
	if err != nil {
		return err
	}
	more, _ := cmd.Flags().GetBool("more")
	out, err := m.ClusterList(&rc, more)
	if err != nil {
		return fmt.Errorf("reading cluster list (controller quorum may not be ready): %w (%s)", err, strings.TrimSpace(out))
	}
	brokers, masters := rocketMQClusterSummary(out, rc.ClusterName())
	fmt.Printf("rocketmq %q — cluster %s: %d broker(s), %d master(s)\n", rc.Name, rc.ClusterName(), brokers, masters)
	fmt.Print(out)
	if !strings.HasSuffix(out, "\n") {
		fmt.Println()
	}
	return nil
}

// --- command tree ----------------------------------------------------------

var addonRocketMQCmd = &cobra.Command{
	Use:   "rocketmq",
	Short: "Manage RocketMQ 5.x container instances",
	Long: `Manage RocketMQ 5.x add-on instances (official apache/rocketmq image).

Every instance runs three containers on the host network: a NameServer, a
broker and a proxy. Standalone mode starts one async master with no replicas
and no controller; the proxy is the 5.x client entry point (gRPC 8081 and
remoting 8080). Data and rendered config files are bind-mounted; pgcli manages
JVM heaps and the container lifecycle. There is NO authentication and NO TLS —
keep it on a trusted private network or behind a firewall.

Multi-host clusters use RocketMQ's auto-failover mode: the controller is
embedded in every NameServer as a DLedger (Raft) group, and brokers run in
controller mode, so one broker is elected master and another takes over when it
fails. Run "pg addon install rocketmq" on every host with the same --cluster
name, a unique --controller-id (n0, n1, ...), that host's --advertise-host as a
concrete non-loopback IPv4 address, and --peer entries for the other controller
members as ID@HOST (no port; the controller port is fixed at 9877). The local
peer is added automatically. Install 3 members for fault tolerance: the DLedger
quorum is the majority, so 2 members tolerate no failure. Cluster ports are
fixed (9876/9877/10911/10912/8080/8081) and cluster identity is immutable after
install; switching between standalone and cluster requires --clean-data.

Broker messages are replicated with allAckInSyncStateSet=true and
minInSyncReplicas=2 on 3+ members (1 on 2 members). Disks still use the
upstream default ASYNC_FLUSH; pass --config-option flushDiskType=SYNC_FLUSH for
a sync flush (slower, stronger power-loss durability).

Subcommands:
  status    Show container states, endpoints, controller/peers and paths
  shell     Open bash in one role container (--service namesrv|broker|proxy)
  topic     Create, list, describe or delete topics
  send      Send one message to a topic
  cluster   Show the clusterList table (registered brokers and roles)

Examples:
  pg addon install rocketmq
  pg addon install rocketmq --name rmq --heap 512m
  pg addon install rocketmq --name rmq --advertise-host 10.0.0.11
  # node n0 on 10.0.0.11, n1 on 10.0.0.12, n2 on 10.0.0.13
  pg addon install rocketmq --name rmq --cluster prod --controller-id n0 \
    --advertise-host 10.0.0.11 --peer n1@10.0.0.12 --peer n2@10.0.0.13
  pg addon install rocketmq --name rmq --cluster prod --controller-id n1 \
    --advertise-host 10.0.0.12 --peer n0@10.0.0.11 --peer n2@10.0.0.13
  pg addon install rocketmq --name rmq --cluster prod --controller-id n2 \
    --advertise-host 10.0.0.13 --peer n0@10.0.0.11 --peer n1@10.0.0.12
  pg addon rocketmq topic create demo --queues 3 --name rmq
  pg addon rocketmq topic list --name rmq
  pg addon rocketmq topic describe demo --name rmq
  pg addon rocketmq send demo "hello" --key k1 --tags demo --name rmq
  pg addon rocketmq cluster status --name rmq
  pg addon rocketmq shell --service namesrv --name rmq
  pg logs addon rocketmq --name rmq --service broker -f
  pg addon remove rocketmq --name rmq --clean-data`,
}

var addonRocketMQStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show RocketMQ container states, endpoints and paths",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonRocketMQStatus(cmd) },
}

var addonRocketMQShellCmd = &cobra.Command{
	Use:   "shell [-- COMMAND...]",
	Short: "Open bash (or run a command) in a RocketMQ role container",
	Long: `Open bash in the broker container by default. Use --service to pick the
NameServer or the proxy. The command runs inside the selected container; for
mqadmin use "pg addon rocketmq cluster status" or run it explicitly:
  pg addon rocketmq shell --service namesrv -- sh mqadmin clusterList`,
	Args: cobra.ArbitraryArgs,
	RunE: runAddonRocketMQShell,
}

var addonRocketMQTopicCmd = &cobra.Command{
	Use:   "topic",
	Short: "Create, list, describe and delete topics",
}

var addonRocketMQTopicCreateCmd = &cobra.Command{
	Use:   "create NAME",
	Short: "Create a topic with read and write queues",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRocketMQTopicCreate,
}

var addonRocketMQTopicListCmd = &cobra.Command{
	Use:   "list",
	Short: "List topics (system topics hidden unless --system)",
	RunE:  runAddonRocketMQTopicList,
}

var addonRocketMQTopicDescribeCmd = &cobra.Command{
	Use:   "describe NAME",
	Short: "Show a topic's queues and broker offsets",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRocketMQTopicDescribe,
}

var addonRocketMQTopicDeleteCmd = &cobra.Command{
	Use:   "delete NAME",
	Short: "Delete a topic and all of its messages",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonRocketMQTopicDelete,
}

var addonRocketMQSendCmd = &cobra.Command{
	Use:   "send TOPIC BODY",
	Short: "Send one message to a topic",
	Args:  cobra.ExactArgs(2),
	RunE:  runAddonRocketMQSend,
}

var addonRocketMQClusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Inspect registered brokers and controller-assigned roles",
}

var addonRocketMQClusterStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the clusterList table (brokers, ids, addresses)",
	RunE:  runAddonRocketMQClusterStatus,
}

func init() {
	addonCmd.AddCommand(addonRocketMQCmd)
	addonRocketMQCmd.AddCommand(addonRocketMQStatusCmd, addonRocketMQShellCmd, addonRocketMQTopicCmd, addonRocketMQSendCmd, addonRocketMQClusterCmd)
	addonRocketMQTopicCmd.AddCommand(addonRocketMQTopicCreateCmd, addonRocketMQTopicListCmd, addonRocketMQTopicDescribeCmd, addonRocketMQTopicDeleteCmd)
	addonRocketMQClusterCmd.AddCommand(addonRocketMQClusterStatusCmd)

	for _, sub := range []*cobra.Command{
		addonRocketMQStatusCmd, addonRocketMQShellCmd,
		addonRocketMQTopicCreateCmd, addonRocketMQTopicListCmd, addonRocketMQTopicDescribeCmd, addonRocketMQTopicDeleteCmd,
		addonRocketMQSendCmd, addonRocketMQClusterStatusCmd,
	} {
		sub.Flags().String("name", "", "rocketmq instance name (default \"rocketmq\")")
	}
	addonRocketMQShellCmd.Flags().String("service", "broker", "container role: namesrv, broker or proxy")
	addonRocketMQTopicCreateCmd.Flags().Int("queues", 4, "read and write queue count for the topic")
	addonRocketMQTopicListCmd.Flags().Bool("system", false, "include RocketMQ system topics")
	addonRocketMQTopicDeleteCmd.Flags().Bool("yes", false, "skip the confirmation prompt")
	addonRocketMQSendCmd.Flags().String("key", "", "message key (searchable with mqadmin queryMsgByKey)")
	addonRocketMQSendCmd.Flags().String("tags", "", "message tag")
	addonRocketMQClusterStatusCmd.Flags().Bool("more", false, "include per-broker runtime details (-m)")

	addonInstallCmd.Flags().Int("namesrv-port", 0, "RocketMQ NameServer host port (0=auto-assign from 9876; standalone only — cluster members use the fixed 9876)")
	addonInstallCmd.Flags().Int("proxy-port", 0, "RocketMQ proxy remoting host port (0=auto-assign from 8080; standalone only — cluster members use the fixed 8080)")
	addonInstallCmd.Flags().Int("grpc-port", 0, "RocketMQ proxy gRPC host port for 5.x clients (0=auto-assign from 8081; standalone only — cluster members use the fixed 8081)")
	addonInstallCmd.Flags().String("controller-id", "", "RocketMQ cluster only: this node's embedded-controller DLedger id (required, unique in the group, e.g. n0)")
	addonInstallCmd.Flags().String("namesrv-heap", "", "RocketMQ NameServer JVM heap size (e.g. 256m), applied as JAVA_OPT_EXT=-Xms<size> -Xmx<size>")
	addonInstallCmd.Flags().String("proxy-heap", "", "RocketMQ proxy JVM heap size (e.g. 256m), applied as JAVA_OPT_EXT=-Xms<size> -Xmx<size>")
}
