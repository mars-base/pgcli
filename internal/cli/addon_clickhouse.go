package cli

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/clickhouse"
	"github.com/mars-base/pgcli/internal/clickhousekeeper"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

// resolveClickHouseInstance loads the config, applies defaults and returns the
// named instance.
func resolveClickHouseInstance(name string) (*config.Config, string, config.ClickHouseConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.ClickHouseConfig{}, err
	}
	c.ApplyDefaults()
	cc, ok := c.Addons.ClickHouse[name]
	if !ok {
		return nil, "", config.ClickHouseConfig{}, fmt.Errorf("clickhouse %q not found (see 'pg addon list')", name)
	}
	return c, path, cc, nil
}

func clickHouseFlagName(cmd *cobra.Command) string {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "clickhouse"
	}
	return name
}

// clickHouseAdminPassword reuses a kept secret (same-name reinstall after
// `remove` without --clean-data), otherwise generates a fresh password.
func clickHouseAdminPassword(secretPath string) (string, error) {
	data, err := os.ReadFile(secretPath)
	switch {
	case err == nil:
		if pw := strings.TrimSpace(string(data)); pw != "" {
			return pw, nil
		}
	case !os.IsNotExist(err):
		return "", err
	}
	return generatePassword(20)
}

// clickHouseInstallOptions captures every install flag plus whether it was
// explicitly set. Stored values are kept on --force unless the operator passes
// the flag again; cluster identity flags are immutable after install.
type clickHouseInstallOptions struct {
	cluster          string
	peers            []string
	shard            int
	replica          int
	advertiseHost    string
	keepers          []string
	interserverPort  int
	tcpPort          int
	httpPort         int
	listen           string
	dataDir          string
	adminUser        string
	adminPassword    string
	version          string
	configOptions    []string
	clusterSet       bool
	peersSet         bool
	shardSet         bool
	replicaSet       bool
	advertiseHostSet bool
	keepersSet       bool
	serverPortSet    bool
	tcpPortSet       bool
	httpPortSet      bool
	listenSet        bool
	dataDirSet       bool
	adminUserSet     bool
	adminPasswordSet bool
	versionSet       bool
	configOptionSet  bool
}

func readClickHouseInstallOptions(fs *pflag.FlagSet) clickHouseInstallOptions {
	opts := clickHouseInstallOptions{
		clusterSet:       fs.Changed("cluster"),
		peersSet:         fs.Changed("peer"),
		shardSet:         fs.Changed("shard"),
		replicaSet:       fs.Changed("replica"),
		advertiseHostSet: fs.Changed("advertise-host"),
		keepersSet:       fs.Changed("keeper"),
		serverPortSet:    fs.Changed("interserver-port"),
		tcpPortSet:       fs.Changed("tcp-port"),
		httpPortSet:      fs.Changed("http-port"),
		listenSet:        fs.Changed("listen"),
		dataDirSet:       fs.Changed("data-dir"),
		adminUserSet:     fs.Changed("admin-user"),
		versionSet:       fs.Changed("version"),
		configOptionSet:  fs.Changed("config-option"),
	}
	opts.cluster, _ = fs.GetString("cluster")
	opts.peers, _ = fs.GetStringArray("peer")
	opts.shard, _ = fs.GetInt("shard")
	opts.replica, _ = fs.GetInt("replica")
	opts.advertiseHost, _ = fs.GetString("advertise-host")
	opts.keepers, _ = fs.GetStringArray("keeper")
	opts.interserverPort, _ = fs.GetInt("interserver-port")
	opts.tcpPort, _ = fs.GetInt("tcp-port")
	opts.httpPort, _ = fs.GetInt("http-port")
	opts.listen, _ = fs.GetString("listen")
	opts.dataDir, _ = fs.GetString("data-dir")
	opts.adminUser, _ = fs.GetString("admin-user")
	opts.version, _ = fs.GetString("version")
	opts.configOptions, _ = fs.GetStringArray("config-option")
	if v, _ := fs.GetString("admin-password"); fs.Changed("admin-password") && v != "" {
		opts.adminPassword = v
		opts.adminPasswordSet = true
	}
	return opts
}

func applyClickHouseInstallFlags(cc *config.ClickHouseConfig, opts clickHouseInstallOptions) error {
	if opts.versionSet {
		cc.Version = opts.version
	}
	if cc.Version == "" {
		cc.Version = config.DefaultClickHouseVersion
	}
	imageTag, ok := config.ClickHouseImageTagForVersion(cc.Version)
	if !ok {
		return fmt.Errorf("ClickHouse %q is not available (supported: %s; pass the exact upstream tag)", cc.Version, config.DefaultClickHouseVersion)
	}
	cc.ImageTag = imageTag
	if opts.adminPasswordSet {
		cc.AdminPassword = opts.adminPassword
	}
	if opts.adminUserSet && opts.adminUser != "" {
		cc.AdminUser = opts.adminUser
	}
	if opts.listenSet && opts.listen != "" {
		cc.Listen = opts.listen
	}
	if opts.httpPortSet {
		cc.HTTPPort = opts.httpPort
	}
	if opts.tcpPortSet {
		cc.TCPPort = opts.tcpPort
	}
	if opts.dataDirSet && opts.dataDir != "" {
		cc.DataDir = opts.dataDir
	}
	if opts.clusterSet {
		cc.Cluster = opts.cluster
	}
	if opts.shardSet {
		cc.Shard = opts.shard
	}
	if opts.replicaSet {
		cc.Replica = opts.replica
	}
	if opts.advertiseHostSet {
		cc.AdvertiseHost = opts.advertiseHost
	}
	if opts.peersSet {
		cc.Peers = append([]string(nil), opts.peers...)
	}
	if opts.keepersSet {
		cc.Keepers = append([]string(nil), opts.keepers...)
	}
	if opts.serverPortSet {
		cc.InterserverPort = opts.interserverPort
	}
	if opts.configOptionSet {
		settings := make(map[string]string, len(opts.configOptions))
		for _, opt := range opts.configOptions {
			key, value, ok := strings.Cut(opt, "=")
			if !ok || strings.TrimSpace(key) == "" {
				return fmt.Errorf("invalid --config-option %q: use KEY=VALUE with a dotted XML path, e.g. max_server_memory_usage=2147483648", opt)
			}
			settings[strings.TrimSpace(key)] = value
		}
		cc.Settings = settings
	}
	return nil
}

// checkClickHouseNoOpFlags rejects an explicit flag that would change an
// installed instance when --force is absent. The old behavior silently ignored
// every flag; surfacing the drift avoids "I passed --shard but nothing
// changed" surprises.
func checkClickHouseNoOpFlags(stored config.ClickHouseConfig, opts clickHouseInstallOptions) error {
	type mismatch struct{ flag, stored, requested string }
	var found []mismatch
	check := func(set bool, flag, oldValue, newValue string) {
		if set && oldValue != newValue {
			found = append(found, mismatch{flag, oldValue, newValue})
		}
	}
	check(opts.clusterSet, "--cluster", stored.Cluster, opts.cluster)
	check(opts.shardSet, "--shard", fmt.Sprint(stored.Shard), fmt.Sprint(opts.shard))
	check(opts.replicaSet, "--replica", fmt.Sprint(stored.Replica), fmt.Sprint(opts.replica))
	check(opts.advertiseHostSet, "--advertise-host", stored.AdvertiseHost, opts.advertiseHost)
	check(opts.serverPortSet, "--interserver-port", fmt.Sprint(stored.InterserverPort), fmt.Sprint(opts.interserverPort))
	check(opts.tcpPortSet, "--tcp-port", fmt.Sprint(stored.TCPPort), fmt.Sprint(opts.tcpPort))
	check(opts.httpPortSet, "--http-port", fmt.Sprint(stored.HTTPPort), fmt.Sprint(opts.httpPort))
	check(opts.listenSet, "--listen", stored.Listen, opts.listen)
	check(opts.dataDirSet, "--data-dir", stored.DataDir, opts.dataDir)
	check(opts.adminUserSet, "--admin-user", stored.AdminUser, opts.adminUser)
	if opts.adminPasswordSet && opts.adminPassword != stored.AdminPassword {
		found = append(found, mismatch{"--admin-password", "(stored)", "(different)"})
	}
	check(opts.versionSet, "--version", stored.Version, opts.version)
	if opts.peersSet && !clickHousePeerSetsEqual(opts.peers, stored.Peers) {
		found = append(found, mismatch{"--peer", strings.Join(stored.Peers, ","), strings.Join(opts.peers, ",")})
	}
	if opts.keepersSet && !clickHouseKeeperSetsEqual(opts.keepers, stored.Keepers) {
		found = append(found, mismatch{"--keeper", strings.Join(stored.Keepers, ","), strings.Join(opts.keepers, ",")})
	}
	if opts.configOptionSet {
		requested, err := parseClickHouseConfigOptions(opts.configOptions)
		if err != nil {
			return err
		}
		if !clickHouseSettingsEqual(requested, stored.Settings) {
			found = append(found, mismatch{"--config-option", "(stored)", "(different)"})
		}
	}
	if len(found) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "clickhouse %q is already installed; %s differs from the stored value (run with --force to apply)", stored.Name, found[0].flag)
	for _, m := range found[1:] {
		fmt.Fprintf(&b, ", %s differs", m.flag)
	}
	return fmt.Errorf("%s", b.String())
}

// checkClickHouseTopologyImmutable rejects changing cluster identity even with
// --force: every other member has this node baked into its peer/keeper list.
func checkClickHouseTopologyImmutable(stored, next config.ClickHouseConfig) error {
	if next.Cluster != stored.Cluster {
		if stored.Cluster == "" {
			return fmt.Errorf("clickhouse %q is installed as a single node; cluster membership is not an in-place change (remove it first or use a new --name)", next.Name)
		}
		return fmt.Errorf("clickhouse %q is installed in cluster %q; changing --cluster is not supported", next.Name, stored.Cluster)
	}
	if !stored.Clustered() {
		return nil
	}
	if next.Shard != stored.Shard {
		return fmt.Errorf("clickhouse %q is installed as shard %d; changing --shard is not supported", next.Name, stored.Shard)
	}
	if next.Replica != stored.Replica {
		return fmt.Errorf("clickhouse %q is installed as replica %d; changing --replica is not supported", next.Name, stored.Replica)
	}
	if next.AdvertiseHost != stored.AdvertiseHost {
		return fmt.Errorf("clickhouse %q advertises %s; changing --advertise-host would invalidate every peer's --peer list", next.Name, stored.AdvertiseHost)
	}
	if next.TCPPort != stored.TCPPort {
		return fmt.Errorf("clickhouse %q is installed with tcp_port %d; the cluster ports are immutable (other members use this endpoint in their --peer list)", next.Name, stored.TCPPort)
	}
	if next.InterserverPort != stored.InterserverPort {
		return fmt.Errorf("clickhouse %q is installed with interserver port %d; changing --interserver-port is not supported", next.Name, stored.InterserverPort)
	}
	if !clickHousePeerSetsEqual(next.Peers, stored.Peers) {
		return fmt.Errorf("clickhouse %q is installed with peers %s; changing --peer is not supported (static cluster membership cannot be edited in place)", next.Name, strings.Join(stored.Peers, ", "))
	}
	if !clickHouseKeeperSetsEqual(next.Keepers, stored.Keepers) {
		return fmt.Errorf("clickhouse %q is installed with Keeper endpoints %s; changing --keeper is not supported (edit the rendered cluster config and recreate the other members instead)", next.Name, strings.Join(stored.Keepers, ", "))
	}
	return nil
}

func validateClickHouseClusterInstall(cc config.ClickHouseConfig, opts clickHouseInstallOptions, present bool) error {
	if !cc.Clustered() {
		switch {
		case opts.peersSet || len(cc.Peers) > 0:
			return fmt.Errorf("--peer requires --cluster (pass the same cluster name on every host)")
		case opts.shardSet || opts.replicaSet || cc.Shard != 0 || cc.Replica != 0:
			return fmt.Errorf("--shard/--replica require --cluster (pass the same cluster name on every host)")
		case opts.keepersSet || len(cc.Keepers) > 0:
			return fmt.Errorf("--keeper requires --cluster")
		case opts.advertiseHostSet || cc.AdvertiseHost != "":
			return fmt.Errorf("--advertise-host requires --cluster for ClickHouse")
		case opts.serverPortSet || cc.InterserverPort != 0:
			return fmt.Errorf("--interserver-port requires --cluster")
		}
		return nil
	}
	if len(cc.Peers) == 0 {
		return fmt.Errorf("--cluster requires at least one --peer SHARD/REPLICA@HOST:PORT listing the other members (this node is added automatically)")
	}
	if len(cc.Keepers) == 0 {
		return fmt.Errorf("--cluster requires at least one --keeper HOST[:PORT] (Keeper serves replicated table metadata and ON CLUSTER DDL)")
	}
	if !present {
		var missing []string
		for _, required := range []struct {
			ok   bool
			flag string
		}{
			{opts.clusterSet, "--cluster"},
			{opts.shardSet, "--shard"},
			{opts.replicaSet, "--replica"},
			{opts.advertiseHostSet, "--advertise-host"},
			{opts.peersSet, "--peer"},
			{opts.keepersSet, "--keeper"},
			{opts.listenSet, "--listen"},
			{opts.adminPasswordSet, "--admin-password"},
		} {
			if !required.ok {
				missing = append(missing, required.flag)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("a fresh ClickHouse cluster install requires explicit %s (every member must share cluster/keeper/admin-password; the admin password cannot be generated because peers need the same one)", strings.Join(missing, ", "))
		}
	}
	if !opts.adminPasswordSet && cc.AdminPassword == "" {
		return fmt.Errorf("--cluster requires --admin-password; every member must use the same admin credential and pgcli cannot read it from another host")
	}
	listen := net.ParseIP(cc.Listen)
	if listen == nil || listen.IsLoopback() || (!listen.IsUnspecified() && !listen.Equal(net.ParseIP(cc.AdvertiseHost))) {
		return fmt.Errorf("--cluster requires --listen 0.0.0.0 (or this node's advertised address %s); %s is not reachable by peers or clients", cc.AdvertiseHost, cc.Listen)
	}
	return nil
}

// clickHousePeerSetsEqual compares parsed peers, ignoring list order.
func clickHousePeerSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, raw := range a {
		peer, err := config.ParseClickHousePeer(raw)
		if err != nil {
			return false
		}
		seen[peer.String()] = true
	}
	if len(seen) != len(a) {
		return false
	}
	for _, raw := range b {
		peer, err := config.ParseClickHousePeer(raw)
		if err != nil || !seen[peer.String()] {
			return false
		}
		delete(seen, peer.String())
	}
	return len(seen) == 0
}

// clickHouseKeeperSetsEqual compares parsed Keeper endpoints, applying the
// default client port to entries that omit it.
func clickHouseKeeperSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, raw := range a {
		endpoint, err := config.ParseClickHouseKeeperEndpoint(raw, config.DefaultClickHouseKeeperClientPort)
		if err != nil {
			return false
		}
		seen[endpoint.String()] = true
	}
	if len(seen) != len(a) {
		return false
	}
	for _, raw := range b {
		endpoint, err := config.ParseClickHouseKeeperEndpoint(raw, config.DefaultClickHouseKeeperClientPort)
		if err != nil || !seen[endpoint.String()] {
			return false
		}
		delete(seen, endpoint.String())
	}
	return len(seen) == 0
}

func parseClickHouseConfigOptions(options []string) (map[string]string, error) {
	settings := make(map[string]string, len(options))
	for _, opt := range options {
		key, value, ok := strings.Cut(opt, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid --config-option %q: use KEY=VALUE with a dotted XML path, e.g. max_server_memory_usage=2147483648", opt)
		}
		settings[strings.TrimSpace(key)] = value
	}
	return settings, nil
}

func clickHouseSettingsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// normalizeClickHouseClusterLists stores canonical peer/keeper strings so the
// rendered XML, status output and later re-install comparisons are stable.
func normalizeClickHouseClusterLists(cc *config.ClickHouseConfig) {
	if !cc.Clustered() {
		return
	}
	if len(cc.Peers) > 0 {
		peers := make([]config.ClickHousePeer, 0, len(cc.Peers))
		valid := true
		for _, raw := range cc.Peers {
			peer, err := config.ParseClickHousePeer(raw)
			if err != nil {
				valid = false
				break
			}
			peers = append(peers, peer)
		}
		if valid {
			sort.Slice(peers, func(i, j int) bool {
				if peers[i].Shard != peers[j].Shard {
					return peers[i].Shard < peers[j].Shard
				}
				return peers[i].Replica < peers[j].Replica
			})
			out := make([]string, 0, len(peers))
			for _, peer := range peers {
				out = append(out, peer.String())
			}
			cc.Peers = out
		}
	}
	if endpoints, err := config.ClickHouseKeeperEndpoints(*cc); err == nil {
		out := make([]string, 0, len(endpoints))
		for _, endpoint := range endpoints {
			out = append(out, endpoint.String())
		}
		cc.Keepers = out
	}
}

// --- install / lifecycle ----------------------------------------------------

func runAddonInstallClickHouse(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	if !config.ValidClickHouseName(name) {
		return fmt.Errorf("invalid ClickHouse instance name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	fmt.Printf("=== pg addon install clickhouse (%s) ===\n", name)

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if c.Addons.ClickHouse == nil {
		c.Addons.ClickHouse = make(map[string]config.ClickHouseConfig)
	}
	fs := cmd.Flags()
	opts := readClickHouseInstallOptions(fs)
	force, _ := fs.GetBool("force")
	c.ApplyDefaults()
	stored, present := c.Addons.ClickHouse[name]

	if present && !force {
		if err := checkClickHouseNoOpFlags(stored, opts); err != nil {
			return err
		}
		state := "stopped"
		if m, err := podman.NewClickHouseManager(c); err == nil {
			if running, _ := m.ContainerRunning(stored.ContainerName); running {
				state = "running"
			}
		}
		fmt.Printf("✓ clickhouse %q already installed (%s, HTTP %s:%d, TCP %s:%d, admin %s) — re-install with --force to rewrite the config and recreate the container\n",
			name, state, stored.Listen, stored.HTTPPort, stored.Listen, stored.TCPPort, stored.AdminUser)
		if stored.Clustered() {
			fmt.Printf("  Cluster:   %s (shard %d / replica %d, advertise %s)\n", stored.Cluster, stored.Shard, stored.Replica, stored.AdvertiseHost)
			fmt.Printf("  Peers:     %s\n", strings.Join(stored.Peers, ", "))
			fmt.Printf("  Keepers:   %s\n", strings.Join(stored.Keepers, ", "))
		}
		fmt.Printf("  Password: pg addon password clickhouse --name %s\n", name)
		return nil
	}

	cc := config.ClickHouseConfig{Name: name}
	if present {
		cc = stored
	}
	cc.Name = name
	if err := applyClickHouseInstallFlags(&cc, opts); err != nil {
		return err
	}
	c.Addons.ClickHouse[name] = cc
	c.ApplyDefaults()
	cc = c.Addons.ClickHouse[name]

	if present {
		if err := checkClickHouseTopologyImmutable(stored, cc); err != nil {
			return err
		}
	}
	if err := validateClickHouseClusterInstall(cc, opts, present); err != nil {
		return err
	}
	if cc.Clustered() && !opts.tcpPortSet {
		fmt.Printf("  [!] --tcp-port not set; auto-assigned %d. Every other member's --peer entry must use this port, so pass --tcp-port explicitly when the per-host pool may differ.\n", cc.TCPPort)
	}
	normalizeClickHouseClusterLists(&cc)
	c.Addons.ClickHouse[name] = cc

	if cc.AdminPassword == "" {
		pw, err := clickHouseAdminPassword(cc.SecretPath)
		if err != nil {
			return fmt.Errorf("resolving admin password: %w", err)
		}
		cc.AdminPassword = pw
		c.Addons.ClickHouse[name] = cc
	}
	if err := c.Validate(); err != nil {
		return err
	}

	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	if err := m.EnsureImage(cc.ImageTag); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] image: %s\n", cc.ImageTag)
	if err := clickhouse.WriteGeneratedConfig(cc); err != nil {
		return fmt.Errorf("  [X] writing config: %w", err)
	}
	if err := clickhouse.WriteSecret(cc); err != nil {
		return fmt.Errorf("  [X] writing password file: %w", err)
	}
	fmt.Printf("  [OK] config:  %s\n", cc.ConfigPath)
	if cc.Clustered() {
		fmt.Printf("  [OK] cluster: %s\n", clickhouse.ClusterConfigPath(cc))
	}
	fmt.Printf("  [OK] data:    %s\n", cc.DataDir)

	// Persist before starting so a failed boot is still inspectable and the
	// generated admin password is not lost.
	c.Addons.ClickHouse[name] = cc
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}

	if err := m.EnsureContainer(&cc); err != nil {
		m.LogsTail(cc.ContainerName, 20)
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] container: %s\n", cc.ContainerName)
	fmt.Printf("  [OK] HTTP:      %s\n", clickhouse.HTTPURL(cc))
	fmt.Printf("  [OK] TCP:       %s\n", clickhouse.TCPAddr(cc))
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), "SELECT version()", false); err != nil {
		m.LogsTail(cc.ContainerName, 20)
		return fmt.Errorf("  [X] admin login failed: %w", err)
	}
	if cc.Clustered() {
		endpoints, err := config.ClickHouseKeeperEndpoints(cc)
		if err != nil {
			return err
		}
		reachable := 0
		for _, endpoint := range endpoints {
			conn, err := net.DialTimeout("tcp", endpoint.String(), time.Second)
			if err == nil {
				reachable++
				conn.Close()
			}
		}
		if reachable < len(endpoints) {
			fmt.Printf("  [!] Keeper endpoints reachable: %d/%d; replicated tables and ON CLUSTER DDL stay unavailable until quorum is reachable\n", reachable, len(endpoints))
		} else {
			fmt.Printf("  [OK] Keeper endpoints reachable: %d/%d\n", reachable, len(endpoints))
		}
	}

	fmt.Println()
	fmt.Printf("✓ ClickHouse %q installed\n", name)
	fmt.Printf("  Version:   %s\n", cc.Version)
	fmt.Printf("  Image:     %s\n", cc.ImageTag)
	fmt.Printf("  Container: %s\n", cc.ContainerName)
	fmt.Printf("  HTTP:      %s\n", clickhouse.HTTPURL(cc))
	fmt.Printf("  TCP:       %s\n", clickhouse.TCPAddr(cc))
	if cc.Clustered() {
		fmt.Printf("  Cluster:   %s\n", cc.Cluster)
		fmt.Printf("  Node:      shard %d / replica %d (advertise %s:%d)\n", cc.Shard, cc.Replica, cc.AdvertiseHost, cc.TCPPort)
		fmt.Printf("  Peers:     %s\n", strings.Join(cc.Peers, ", "))
		fmt.Printf("  Keepers:   %s\n", strings.Join(cc.Keepers, ", "))
		fmt.Printf("  Interserver: %s:%d\n", cc.AdvertiseHost, cc.InterserverPort)
		fmt.Printf("  Cluster config: %s\n", clickhouse.ClusterConfigPath(cc))
	}
	fmt.Printf("  Admin:     %s\n", cc.AdminUser)
	fmt.Printf("  Password:  %s\n", cc.AdminPassword)
	fmt.Printf("  Data dir:  %s\n", cc.DataDir)
	fmt.Printf("  Backups:   %s\n", cc.BackupDir)
	fmt.Printf("  Config:    %s\n", cc.ConfigPath)
	fmt.Println()
	if cc.Clustered() {
		fmt.Printf("  Cluster status: pg addon clickhouse cluster status --name %s\n", name)
		fmt.Printf("  Repeat on every host with the same --cluster, --keeper and --admin-password, this host's --shard/--replica/--advertise-host, and --peer entries for the other members.\n")
	}
	fmt.Printf("  Password: pg addon password clickhouse --name %s\n", name)
	fmt.Printf("  Status:   pg addon clickhouse status --name %s\n", name)
	fmt.Printf("  Query:    pg addon clickhouse query --name %s 'SELECT 1'\n", name)
	fmt.Printf("  Shell:    pg addon clickhouse shell --name %s\n", name)
	return nil
}

func runAddonRemoveClickHouse(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name := clickHouseFlagName(cmd)
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	c.ApplyDefaults()
	cc, ok := c.Addons.ClickHouse[name]
	if !ok {
		return fmt.Errorf("clickhouse %q not found (see 'pg addon list')", name)
	}
	fmt.Printf("=== pg addon remove clickhouse (%s) ===\n", name)

	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainer(&cc); err != nil {
		return err
	}
	delete(c.Addons.ClickHouse, name)
	if len(c.Addons.ClickHouse) == 0 {
		c.Addons.ClickHouse = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	if cleanData {
		if err := m.CleanData(&cc); err != nil {
			return err
		}
	} else {
		fmt.Printf("  [i] kept %s (data, config, logs, backups) — pass --clean-data to drop\n", filepath.Dir(filepath.Dir(cc.ConfigPath)))
	}
	fmt.Printf("✓ ClickHouse %q removed\n", name)
	return nil
}

func runAddonStartClickHouse(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	already, err := m.StartContainer(&cc)
	if err != nil {
		m.LogsTail(cc.ContainerName, 20)
		return err
	}
	if already {
		fmt.Printf("✓ clickhouse %q already running (HTTP %s, TCP %s)\n", name, clickhouse.HTTPURL(cc), clickhouse.TCPAddr(cc))
		return nil
	}
	fmt.Printf("✓ clickhouse %q started (HTTP %s, TCP %s)\n", name, clickhouse.HTTPURL(cc), clickhouse.TCPAddr(cc))
	return nil
}

func runAddonStopClickHouse(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	wasRunning, err := m.Stop(cc.ContainerName)
	if err != nil {
		return err
	}
	if !wasRunning {
		fmt.Printf("✓ clickhouse %q already stopped\n", name)
		return nil
	}
	fmt.Printf("✓ clickhouse %q stopped\n", name)
	return nil
}

// requireClickHouseRunning returns a manager for a running instance.
func requireClickHouseRunning(cmd *cobra.Command) (*podman.ClickHouseManager, config.ClickHouseConfig, error) {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return nil, cc, err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return nil, cc, err
	}
	if running, _ := m.ContainerRunning(cc.ContainerName); !running {
		return nil, cc, fmt.Errorf("clickhouse %q is not running — 'pg addon start clickhouse --name %s' first", name, name)
	}
	return m, cc, nil
}

// --- status / shell / query ------------------------------------------------

func runAddonClickHouseStatus(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	state := "stopped"
	if running, _ := m.ContainerRunning(cc.ContainerName); running {
		if err := clickhouse.Ping(cc, 2*time.Second); err != nil {
			state = fmt.Sprintf("running (ping failed: %v)", err)
		} else {
			state = "running (ping ok)"
		}
	}
	fmt.Printf("clickhouse %q — %s\n", name, state)
	fmt.Printf("  Version:    %s\n", cc.Version)
	fmt.Printf("  Image:      %s\n", cc.ImageTag)
	fmt.Printf("  Container:  %s\n", cc.ContainerName)
	fmt.Printf("  HTTP:       %s\n", clickhouse.HTTPURL(cc))
	fmt.Printf("  TCP:        %s\n", clickhouse.TCPAddr(cc))
	fmt.Printf("  Listen:     %s\n", cc.Listen)
	fmt.Printf("  Admin user: %s\n", cc.AdminUser)
	if cc.Clustered() {
		fmt.Printf("  Cluster:    %s\n", cc.Cluster)
		fmt.Printf("  Node:       shard %d / replica %d\n", cc.Shard, cc.Replica)
		fmt.Printf("  Advertise:  %s (TCP %d, interserver %d)\n", cc.AdvertiseHost, cc.TCPPort, cc.InterserverPort)
		fmt.Printf("  Peers:      %s\n", strings.Join(cc.Peers, ", "))
		fmt.Printf("  Keepers:    %s\n", strings.Join(cc.Keepers, ", "))
		fmt.Printf("  Cluster cfg:%s\n", clickhouse.ClusterConfigPath(cc))
	}
	fmt.Printf("  Data dir:   %s\n", cc.DataDir)
	fmt.Printf("  Logs dir:   %s\n", cc.LogDir)
	fmt.Printf("  Backups:    %s\n", cc.BackupDir)
	fmt.Printf("  Config:     %s\n", cc.ConfigPath)
	fmt.Printf("  Settings:   %d override(s)\n", len(cc.Settings))
	fmt.Printf("  Logs:       pg logs addon clickhouse --name %s\n", name)
	return nil
}

// clickHouseClusterMember is one row of system.clusters.
type clickHouseClusterMember struct {
	Shard   int
	Replica int
	Host    string
	Port    int
	Local   bool
}

func parseClickHouseClusterMembers(tsv string) ([]clickHouseClusterMember, error) {
	var members []clickHouseClusterMember
	for _, line := range strings.Split(strings.TrimSpace(tsv), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("unexpected system.clusters row %q (want 5 tab-separated fields)", line)
		}
		shard, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("invalid shard in system.clusters row %q: %w", line, err)
		}
		replica, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("invalid replica in system.clusters row %q: %w", line, err)
		}
		port, err := strconv.Atoi(fields[3])
		if err != nil {
			return nil, fmt.Errorf("invalid port in system.clusters row %q: %w", line, err)
		}
		members = append(members, clickHouseClusterMember{
			Shard: shard, Replica: replica, Host: fields[2], Port: port, Local: fields[4] == "1",
		})
	}
	return members, nil
}

// clickHouseReplicaState is one row of system.replicas.
type clickHouseReplicaState struct {
	Database  string
	Table     string
	Replica   string
	Readonly  bool
	Delay     uint64
	QueueSize uint64
}

func parseClickHouseReplicaStates(tsv string) ([]clickHouseReplicaState, error) {
	var replicas []clickHouseReplicaState
	for _, line := range strings.Split(strings.TrimSpace(tsv), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 6 {
			return nil, fmt.Errorf("unexpected system.replicas row %q (want 6 tab-separated fields)", line)
		}
		delay, err := strconv.ParseUint(fields[4], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid absolute_delay in system.replicas row %q: %w", line, err)
		}
		queue, err := strconv.ParseUint(fields[5], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid queue_size in system.replicas row %q: %w", line, err)
		}
		replicas = append(replicas, clickHouseReplicaState{
			Database: fields[0], Table: fields[1], Replica: fields[2],
			Readonly: fields[3] == "1", Delay: delay, QueueSize: queue,
		})
	}
	return replicas, nil
}

func runAddonClickHouseClusterStatus(cmd *cobra.Command) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	if !cc.Clustered() {
		return fmt.Errorf("clickhouse %q is not a cluster member; install it with --cluster/--shard/--replica and the peer list", cc.Name)
	}
	topology, err := config.ClickHouseTopology(cc)
	if err != nil {
		return err
	}

	clusterSQL := fmt.Sprintf(
		"SELECT shard_num, replica_num, host_name, port, is_local FROM system.clusters WHERE cluster = '%s' ORDER BY shard_num, replica_num FORMAT TSV",
		cc.Cluster)
	out, err := m.ExecClientOutput(&cc, clickhouse.ClientArgs(cc, ""), clusterSQL)
	if err != nil {
		return fmt.Errorf("querying system.clusters: %w", err)
	}
	members, err := parseClickHouseClusterMembers(string(out))
	if err != nil {
		return err
	}

	replicaSQL := "SELECT database, table, replica_name, is_readonly, absolute_delay, queue_size FROM system.replicas ORDER BY database, table, replica_name FORMAT TSV"
	out, err = m.ExecClientOutput(&cc, clickhouse.ClientArgs(cc, ""), replicaSQL)
	if err != nil {
		return fmt.Errorf("querying system.replicas: %w", err)
	}
	replicas, err := parseClickHouseReplicaStates(string(out))
	if err != nil {
		return err
	}

	fmt.Printf("clickhouse %q — cluster %q, this node shard %d / replica %d\n", cc.Name, cc.Cluster, cc.Shard, cc.Replica)
	fmt.Printf("  Cluster config: %s\n", clickhouse.ClusterConfigPath(cc))
	fmt.Printf("  system.clusters: %d member(s), expected %d\n", len(members), len(topology))
	for _, member := range members {
		local := ""
		if member.Local {
			local = "  local"
		}
		fmt.Printf("    shard %d replica %d  %s:%d%s\n", member.Shard, member.Replica, member.Host, member.Port, local)
	}

	expected := map[string]bool{}
	for _, peer := range topology {
		expected[fmt.Sprintf("%d/%d@%s:%d", peer.Shard, peer.Replica, peer.Host, peer.Port)] = true
	}
	var missing []string
	for _, member := range members {
		delete(expected, fmt.Sprintf("%d/%d@%s:%d", member.Shard, member.Replica, member.Host, member.Port))
	}
	for member := range expected {
		missing = append(missing, member)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		fmt.Printf("  [!] missing from system.clusters: %s\n", strings.Join(missing, ", "))
	}

	fmt.Printf("  system.replicas: %d replicated table(s)\n", len(replicas))
	var unhealthy []string
	for _, replica := range replicas {
		state := "ok"
		if replica.Readonly {
			state = "READONLY"
			unhealthy = append(unhealthy, replica.Database+"."+replica.Table+"@"+replica.Replica)
		}
		fmt.Printf("    %s.%s replica %s — %s, delay %ds, queue %d\n",
			replica.Database, replica.Table, replica.Replica, state, replica.Delay, replica.QueueSize)
	}

	endpoints, err := config.ClickHouseKeeperEndpoints(cc)
	if err != nil {
		return err
	}
	reachable := 0
	for _, endpoint := range endpoints {
		if err := clickhousekeeper.Ruok(endpoint.Host, endpoint.Port, time.Second); err == nil {
			reachable++
		}
	}
	quorum := len(endpoints)/2 + 1
	if reachable < quorum {
		fmt.Printf("  [!] Keeper client endpoints reachable: %d/%d (quorum %d) — replicated tables and DDL cannot reach quorum\n", reachable, len(endpoints), quorum)
	} else {
		fmt.Printf("  [OK] Keeper client endpoints reachable: %d/%d (quorum %d; reachability is not a Raft quorum proof)\n", reachable, len(endpoints), quorum)
	}

	if len(members) != len(topology) {
		return fmt.Errorf("system.clusters reports %d member(s), expected %d from the local topology", len(members), len(topology))
	}
	if len(unhealthy) > 0 {
		return fmt.Errorf("replicas are read-only or detached: %s", strings.Join(unhealthy, ", "))
	}
	return nil
}

func runAddonClickHouseShell(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	c, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	if running, _ := m.ContainerRunning(cc.ContainerName); !running {
		fmt.Fprintf(os.Stderr, "[!] clickhouse %q is not running — start it with: pg addon start clickhouse --name %s\n", name, name)
	}
	return m.ExecShell(&cc)
}

func runAddonClickHouseQuery(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	db, _ := cmd.Flags().GetString("database")
	file, _ := cmd.Flags().GetString("file")

	var sql string
	switch {
	case file != "" || (len(args) == 1 && args[0] == "-"):
		if file == "" {
			file = "-"
		}
		if file == "-" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading SQL from stdin: %w", err)
			}
			sql = string(data)
		} else {
			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading SQL file %s: %w", file, err)
			}
			sql = string(data)
		}
	case len(args) > 0:
		sql = strings.Join(args, " ")
	default:
		return fmt.Errorf("pass SQL as arguments, with --file FILE, or pipe it on stdin (SQL... | ... -)")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("empty SQL")
	}
	return m.ExecClient(&cc, clickhouse.ClientArgs(cc, db), sql, false)
}

// --- database / user / grant ----------------------------------------------

func runAddonClickHouseDatabaseCreate(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	engine, _ := cmd.Flags().GetString("engine")
	comment, _ := cmd.Flags().GetString("comment")
	ifNotExists, _ := cmd.Flags().GetBool("if-not-exists")
	sql, err := clickhouse.CreateDatabaseSQL(args[0], engine, comment, ifNotExists)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	fmt.Printf("✓ database %q ready on clickhouse %q\n", args[0], cc.Name)
	return nil
}

func runAddonClickHouseDatabaseList(cmd *cobra.Command, _ []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.ExecClientOutput(&cc, clickhouse.ClientArgs(cc, ""), clickhouse.ListDatabasesSQL())
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

func runAddonClickHouseDatabaseDrop(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	ifExists, _ := cmd.Flags().GetBool("if-exists")
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Drop database %q on clickhouse %q? All of its data is deleted. [y/N] ", args[0], cc.Name)) {
		fmt.Println("aborted")
		return nil
	}
	sql, err := clickhouse.DropDatabaseSQL(args[0], ifExists)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	fmt.Printf("✓ database %q dropped\n", args[0])
	return nil
}

func clickHouseHostClauses(cmd *cobra.Command) ([]string, error) {
	anyHost, _ := cmd.Flags().GetBool("host-any")
	local, _ := cmd.Flags().GetBool("host-local")
	ips, _ := cmd.Flags().GetStringArray("host-ip")
	names, _ := cmd.Flags().GetStringArray("host-name")

	var clauses []string
	if anyHost || (len(ips) == 0 && len(names) == 0 && !local) {
		clauses = append(clauses, "ANY")
	}
	if local {
		clauses = append(clauses, "LOCAL")
	}
	for _, value := range ips {
		clause, err := clickhouse.HostIPClause(value)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, clause)
	}
	for _, value := range names {
		clause, err := clickhouse.HostNameClause(value)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, clause)
	}
	return clauses, nil
}

func runAddonClickHouseUserCreate(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	password, _ := cmd.Flags().GetString("password")
	generated := false
	if password == "" {
		password, err = generatePassword(20)
		if err != nil {
			return fmt.Errorf("generating password: %w", err)
		}
		generated = true
	}
	defaultDB, _ := cmd.Flags().GetString("default-database")
	readonly, _ := cmd.Flags().GetBool("readonly")
	ifNotExists, _ := cmd.Flags().GetBool("if-not-exists")
	clauses, err := clickHouseHostClauses(cmd)
	if err != nil {
		return err
	}
	sql, err := clickhouse.CreateUserSQL(args[0], password, defaultDB, clauses, readonly, ifNotExists)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	fmt.Printf("✓ user %q ready on clickhouse %q\n", args[0], cc.Name)
	if generated {
		fmt.Printf("  Password: %s\n", password)
	}
	return nil
}

func runAddonClickHouseUserList(cmd *cobra.Command, _ []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	out, err := m.ExecClientOutput(&cc, clickhouse.ClientArgs(cc, ""), clickhouse.ListUsersSQL())
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

func runAddonClickHouseUserDrop(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	ifExists, _ := cmd.Flags().GetBool("if-exists")
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes && !confirmPrompt(fmt.Sprintf("Drop user %q on clickhouse %q? [y/N] ", args[0], cc.Name)) {
		fmt.Println("aborted")
		return nil
	}
	sql, err := clickhouse.DropUserSQL(args[0], ifExists)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	fmt.Printf("✓ user %q dropped\n", args[0])
	return nil
}

func clickHouseGrantPrivileges(cmd *cobra.Command) ([]string, error) {
	var privileges []string
	add := func(p string) {
		for _, existing := range privileges {
			if existing == p {
				return
			}
		}
		privileges = append(privileges, p)
	}
	if read, _ := cmd.Flags().GetBool("read"); read {
		add("SELECT")
	}
	if write, _ := cmd.Flags().GetBool("write"); write {
		add("INSERT")
	}
	if ddl, _ := cmd.Flags().GetBool("ddl"); ddl {
		for _, p := range []string{"CREATE", "ALTER", "DROP", "TRUNCATE", "OPTIMIZE"} {
			add(p)
		}
	}
	if all, _ := cmd.Flags().GetBool("all"); all {
		add("ALL")
	}
	if raw, _ := cmd.Flags().GetString("privileges"); raw != "" {
		parsed, err := clickhouse.NormalizePrivileges(raw)
		if err != nil {
			return nil, err
		}
		for _, p := range parsed {
			add(p)
		}
	}
	if len(privileges) == 0 {
		return nil, fmt.Errorf("no privileges selected: pass --read, --write, --ddl, --all and/or --privileges")
	}
	return privileges, nil
}

func runAddonClickHouseGrant(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	privileges, err := clickHouseGrantPrivileges(cmd)
	if err != nil {
		return err
	}
	database, _ := cmd.Flags().GetString("database")
	table, _ := cmd.Flags().GetString("table")
	revoke, _ := cmd.Flags().GetBool("revoke")
	withGrantOption, _ := cmd.Flags().GetBool("with-grant-option")
	sql, err := clickhouse.GrantSQL(args[0], privileges, database, table, revoke, withGrantOption)
	if err != nil {
		return err
	}
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	verb := "granted to"
	if revoke {
		verb = "revoked from"
	}
	fmt.Printf("✓ %s %s user %q\n", strings.Join(privileges, ", "), verb, args[0])
	return nil
}

func runAddonClickHouseGrantList(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	sql, err := clickhouse.ShowGrantsSQL(args[0])
	if err != nil {
		return err
	}
	out, err := m.ExecClientOutput(&cc, clickhouse.ClientArgs(cc, ""), sql)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

// --- backup / restore ------------------------------------------------------

func runAddonClickHouseBackup(cmd *cobra.Command) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	database, _ := cmd.Flags().GetString("database")
	if database == "" {
		return fmt.Errorf("--database is required")
	}
	out, _ := cmd.Flags().GetString("out")
	diskPath, hostPath, err := clickhouse.BackupTarget(cc.BackupDir, database, out, time.Now())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cc.BackupDir, 0o755); err != nil {
		return fmt.Errorf("creating backup dir %s: %w", cc.BackupDir, err)
	}
	sql, err := clickhouse.BackupDatabaseSQL(database, diskPath)
	if err != nil {
		return err
	}
	fmt.Printf("-> Backing up database %q to %s...\n", database, hostPath)
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	if info, err := os.Stat(hostPath); err == nil {
		fmt.Printf("✓ backup written: %s (%d bytes)\n", hostPath, info.Size())
	} else {
		fmt.Printf("✓ backup reported complete: %s\n", hostPath)
	}
	return nil
}

func runAddonClickHouseRestore(cmd *cobra.Command, args []string) error {
	m, cc, err := requireClickHouseRunning(cmd)
	if err != nil {
		return err
	}
	database, _ := cmd.Flags().GetString("database")
	if database == "" {
		return fmt.Errorf("--database is required (the database name recorded in the backup)")
	}
	newName, _ := cmd.Flags().GetString("as")
	diskPath, hostPath, err := clickhouse.RestoreSource(cc.BackupDir, args[0])
	if err != nil {
		return err
	}
	sql, err := clickhouse.RestoreDatabaseSQL(database, newName, diskPath)
	if err != nil {
		return err
	}
	fmt.Printf("-> Restoring %s...\n", hostPath)
	if err := m.ExecClient(&cc, clickhouse.ClientArgs(cc, ""), sql, false); err != nil {
		return err
	}
	target := database
	if newName != "" {
		target = newName
	}
	fmt.Printf("✓ database %q restored on clickhouse %q\n", target, cc.Name)
	return nil
}

func runAddonClickHouseBackupList(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	_, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	files, err := clickhouse.ListBackupFiles(cc.BackupDir)
	if err != nil {
		return fmt.Errorf("listing %s: %w", cc.BackupDir, err)
	}
	if len(files) == 0 {
		fmt.Printf("no backups in %s\n", cc.BackupDir)
		return nil
	}
	for _, file := range files {
		info, err := os.Stat(filepath.Join(cc.BackupDir, file))
		if err != nil {
			fmt.Printf("  %s\n", file)
			continue
		}
		fmt.Printf("  %-48s %10d bytes  %s\n", file, info.Size(), info.ModTime().UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return nil
}

// --- config ----------------------------------------------------------------

func clickHouseManagedSetting(cc config.ClickHouseConfig, key string) (string, bool) {
	switch key {
	case "listen_host":
		return cc.Listen, true
	case "listen_try":
		return "1", true
	case "http_port":
		return fmt.Sprint(cc.HTTPPort), true
	case "tcp_port":
		return fmt.Sprint(cc.TCPPort), true
	case "logger.level":
		if v, ok := cc.Settings["logger.level"]; ok && v != "" {
			return v, true
		}
		return "information", true
	case "logger.log":
		return clickhouse.ContainerLogDir + "/clickhouse-server.log", true
	case "logger.errorlog":
		return clickhouse.ContainerLogDir + "/clickhouse-server.err.log", true
	case "backups.allowed_disk":
		return "backups", true
	case "backups.allowed_path":
		return clickhouse.ContainerBackupDir + "/", true
	}
	if !cc.Clustered() {
		return "", false
	}
	switch key {
	case "interserver_http_host":
		return cc.AdvertiseHost, true
	case "interserver_http_port":
		return fmt.Sprint(cc.InterserverPort), true
	case "macros.cluster":
		return cc.Cluster, true
	case "macros.shard":
		return fmt.Sprint(cc.Shard), true
	case "macros.replica":
		return fmt.Sprint(cc.Replica), true
	case "distributed_ddl.path":
		return "/clickhouse/" + cc.Cluster + "/task_queue/ddl", true
	case "default_replica_path":
		return "/clickhouse/" + cc.Cluster + "/tables/{uuid}/{shard}", true
	case "default_replica_name":
		return "{replica}", true
	}
	return "", false
}

func runAddonClickHouseConfigList(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	_, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	fmt.Printf("clickhouse %q effective managed settings:\n", name)
	managedKeys := []string{"listen_host", "http_port", "tcp_port", "logger.level", "logger.log", "logger.errorlog", "backups.allowed_disk", "backups.allowed_path"}
	if cc.Clustered() {
		managedKeys = append(managedKeys,
			"interserver_http_host", "interserver_http_port",
			"macros.cluster", "macros.shard", "macros.replica",
			"distributed_ddl.path", "default_replica_path", "default_replica_name")
	}
	for _, key := range managedKeys {
		if value, ok := clickHouseManagedSetting(cc, key); ok {
			fmt.Printf("  %-24s %s\n", key, value)
		}
	}
	if len(cc.Settings) == 0 {
		fmt.Println("custom overrides: none")
		return nil
	}
	fmt.Println("custom overrides:")
	keys := make([]string, 0, len(cc.Settings))
	for key := range cc.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("  %-24s %s\n", key, cc.Settings[key])
	}
	return nil
}

func runAddonClickHouseConfigGet(cmd *cobra.Command, args []string) error {
	name := clickHouseFlagName(cmd)
	_, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	key := args[0]
	if value, ok := cc.Settings[key]; ok {
		fmt.Println(value)
		return nil
	}
	if value, ok := clickHouseManagedSetting(cc, key); ok {
		fmt.Println(value)
		return nil
	}
	return fmt.Errorf("setting %q is not overridden (see 'pg addon clickhouse config list')", key)
}

// persistClickHouseConfig rewrites the generated XML, stores the entry and,
// when restart is true and the container is running, recreates it so the change
// takes effect immediately.
func persistClickHouseConfig(c *config.Config, path string, cc config.ClickHouseConfig, restart bool) error {
	if err := config.ValidateClickHouseSettings(cc.Settings); err != nil {
		return err
	}
	if cc.Clustered() {
		if err := config.ValidateClickHouseClusterSettings(cc.Settings); err != nil {
			return err
		}
	}
	if err := clickhouse.WriteGeneratedConfig(cc); err != nil {
		return err
	}
	c.Addons.ClickHouse[cc.Name] = cc
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.Save(path); err != nil {
		return err
	}
	fmt.Printf("  [OK] wrote %s\n", cc.ConfigPath)
	fmt.Printf("  [OK] stored in %s\n", path)

	m, err := podman.NewClickHouseManager(c)
	if err != nil {
		return err
	}
	running, _ := m.ContainerRunning(cc.ContainerName)
	if !restart {
		if running {
			fmt.Printf("  [i] takes effect on the next start: pg addon stop clickhouse --name %s && pg addon start clickhouse --name %s\n", cc.Name, cc.Name)
		}
		return nil
	}
	if !running {
		fmt.Printf("  [i] instance is stopped; the change applies on the next start\n")
		return nil
	}
	if _, err := m.Stop(cc.ContainerName); err != nil {
		return fmt.Errorf("stopping clickhouse %q: %w", cc.Name, err)
	}
	if _, err := m.StartContainer(&cc); err != nil {
		m.LogsTail(cc.ContainerName, 20)
		return err
	}
	fmt.Printf("✓ clickhouse %q restarted with the new config\n", cc.Name)
	return nil
}

func runAddonClickHouseConfigSet(cmd *cobra.Command, args []string) error {
	name := clickHouseFlagName(cmd)
	c, path, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	if cc.Settings == nil {
		cc.Settings = map[string]string{}
	}
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return fmt.Errorf("invalid setting %q: use KEY=VALUE with a dotted XML path, e.g. max_server_memory_usage=2147483648", arg)
		}
		cc.Settings[strings.TrimSpace(key)] = value
	}
	if err := config.ValidateClickHouseSettings(cc.Settings); err != nil {
		return err
	}
	return persistClickHouseConfig(c, path, cc, !noRestart)
}

func runAddonClickHouseConfigUnset(cmd *cobra.Command, args []string) error {
	name := clickHouseFlagName(cmd)
	c, path, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	noRestart, _ := cmd.Flags().GetBool("no-restart")
	for _, key := range args {
		if _, ok := cc.Settings[key]; !ok {
			return fmt.Errorf("setting %q is not overridden", key)
		}
		delete(cc.Settings, key)
	}
	if len(cc.Settings) == 0 {
		cc.Settings = nil
	}
	return persistClickHouseConfig(c, path, cc, !noRestart)
}

func clickHouseConfigShowContent(cc config.ClickHouseConfig) (string, error) {
	base, err := clickhouse.RenderConfig(cc)
	if err != nil {
		return "", err
	}
	if !cc.Clustered() {
		return base, nil
	}
	cluster, err := clickhouse.RenderClusterConfig(cc)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("== %s ==\n%s\n== %s ==\n%s", cc.ConfigPath, strings.TrimRight(base, "\n"), clickhouse.ClusterConfigPath(cc), cluster), nil
}

func runAddonClickHouseConfigShow(cmd *cobra.Command) error {
	name := clickHouseFlagName(cmd)
	_, _, cc, err := resolveClickHouseInstance(name)
	if err != nil {
		return err
	}
	content, err := clickHouseConfigShowContent(cc)
	if err != nil {
		return err
	}
	fmt.Print(content)
	return nil
}

// --- command tree ----------------------------------------------------------

var addonClickHouseCmd = &cobra.Command{
	Use:   "clickhouse",
	Short: "Manage ClickHouse container instances",
	Long: `Manage ClickHouse add-on instances (official clickhouse-server image).

pgcli runs ClickHouse with host networking and a generated config.d override
that pins the HTTP/native ports, loopback binding, the log files and a local
"backups" disk. The admin user is created by the image entrypoint from
CLICKHOUSE_USER and a password file; "pg addon password clickhouse" reads it
back.

Subcommands:
  status    Show container state, endpoints, version and paths
  cluster   Inspect a multi-host cluster (system.clusters/system.replicas)
  shell     Open a bash shell in the container
  query     Run SQL with clickhouse-client (args, --file or stdin)
  db        Create, list or drop databases
  user      Create, list or drop users
  grant     Grant/revoke privileges, or list a user's grants
  backup    BACKUP DATABASE to the managed backups disk
  restore   RESTORE DATABASE from a .zip in the backups disk
  backups   List local .zip backups
  config    Inspect, change and render server settings

Cluster mode is opt-in with --cluster. Each host installs one member with a
unique --shard/--replica, this host's --advertise-host, --peer entries for the
OTHER members and the shared --keeper endpoints. Every member must use the same
--admin-password; pgcli cannot synchronize it across hosts. Cluster identity
(cluster/shard/replica/advertise/peer/keeper/interserver and the cluster TCP
port) is immutable after install, even with --force.

Config settings use dotted XML paths, e.g.
  max_server_memory_usage=2147483648
  logger.level=debug
  query_log.flush_interval_milliseconds=1000
pgcli-managed keys (paths, listen/ports, auth, backups, user_directories) are
rejected. "config set" restarts the running container by default so the value
takes effect immediately; pass --no-restart to defer it.

Examples:
  pg addon install clickhouse --name ch1
  pg addon install clickhouse --listen 0.0.0.0 --http-port 8123 --tcp-port 9000
  pg addon install clickhouse --name clickhouse --cluster chcluster --shard 1 --replica 1 \
    --advertise-host 10.0.0.11 --listen 0.0.0.0 --tcp-port 9000 --interserver-port 9009 \
    --admin-password "$CH_ADMIN_PASSWORD" \
    --peer 1/2@10.0.0.12:9000 --peer 2/1@10.0.0.13:9000 --peer 2/2@10.0.0.14:9000 \
    --keeper 10.0.0.11:9181 --keeper 10.0.0.12:9181 --keeper 10.0.0.13:9181
  pg addon clickhouse cluster status --name clickhouse
  pg addon clickhouse query --name ch1 'SELECT version()'
  pg addon clickhouse db create demo --name ch1
  pg addon clickhouse user create app --password secret --default-database demo
  pg addon clickhouse grant app --read --write --database demo
  pg addon clickhouse grant list app --name ch1
  pg addon clickhouse backup --name ch1 --database demo
  pg addon clickhouse restore demo-20261027-120000.zip --name ch1 --database demo --as demo_restored
  pg addon clickhouse config set max_server_memory_usage=2147483648 --name ch1
  pg addon clickhouse config show --name ch1
  pg logs addon clickhouse --name ch1 -f
  pg addon remove clickhouse --name ch1 --clean-data`,
}

var addonClickHouseStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show ClickHouse container state, endpoints and paths",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseStatus(cmd) },
}

var addonClickHouseClusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Inspect a multi-host ClickHouse cluster",
}

var addonClickHouseClusterStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show system.clusters, system.replicas and Keeper reachability",
	Long: `Show this member's view of the configured cluster.

The command queries system.clusters (all configured members), system.replicas
(replication state of local ReplicatedMergeTree tables) and probes every
--keeper client endpoint. Keeper endpoint reachability is reported separately
from Raft quorum: a reachable client port does not prove that Keeper has a
quorum.`,
	RunE: func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseClusterStatus(cmd) },
}

var addonClickHouseShellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a bash shell in the ClickHouse container",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseShell(cmd) },
}

var addonClickHouseQueryCmd = &cobra.Command{
	Use:   "query [SQL...]",
	Short: "Run SQL with clickhouse-client (args, --file or stdin)",
	Long: `Run a SQL statement with clickhouse-client inside the container.

Pass the statement as arguments, read it from --file, or pipe it on stdin and
pass "-". Query output is written to stdout, so it can be redirected or piped.
The client authenticates as the admin user with a password read from the
environment, never from argv.`,
	Args: cobra.ArbitraryArgs,
	RunE: runAddonClickHouseQuery,
}

var addonClickHouseDatabaseCmd = &cobra.Command{
	Use:     "database",
	Aliases: []string{"db"},
	Short:   "Create, list and drop databases",
}

var addonClickHouseDatabaseCreateCmd = &cobra.Command{
	Use:   "create NAME",
	Short: "Create a database",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseDatabaseCreate,
}

var addonClickHouseDatabaseListCmd = &cobra.Command{
	Use:   "list",
	Short: "List databases",
	RunE:  runAddonClickHouseDatabaseList,
}

var addonClickHouseDatabaseDropCmd = &cobra.Command{
	Use:   "drop NAME",
	Short: "Drop a database and all of its data",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseDatabaseDrop,
}

var addonClickHouseUserCmd = &cobra.Command{
	Use:   "user",
	Short: "Create, list and drop users",
}

var addonClickHouseUserCreateCmd = &cobra.Command{
	Use:   "create NAME",
	Short: "Create a user (generates a password when --password is omitted)",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseUserCreate,
}

var addonClickHouseUserListCmd = &cobra.Command{
	Use:   "list",
	Short: "List users",
	RunE:  runAddonClickHouseUserList,
}

var addonClickHouseUserDropCmd = &cobra.Command{
	Use:   "drop NAME",
	Short: "Drop a user",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseUserDrop,
}

var addonClickHouseGrantCmd = &cobra.Command{
	Use:   "grant USER",
	Short: "Grant or revoke privileges on a database/table",
	Long: `Grant privileges to a user. Select privileges with --read (SELECT),
--write (INSERT), --ddl (CREATE/ALTER/DROP/TRUNCATE/OPTIMIZE), --all, or an
explicit comma-separated list with --privileges. The scope is
[database].[table]; with no --database the grant is global (*.*). --revoke
issues REVOKE instead of GRANT.`,
	Args: cobra.ExactArgs(1),
	RunE: runAddonClickHouseGrant,
}

var addonClickHouseGrantListCmd = &cobra.Command{
	Use:   "list USER",
	Short: "Show a user's grants",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseGrantList,
}

var addonClickHouseBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Back up a database to the managed backups disk",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseBackup(cmd) },
}

var addonClickHouseRestoreCmd = &cobra.Command{
	Use:   "restore FILE",
	Short: "Restore a .zip backup from the managed backups disk",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseRestore,
}

var addonClickHouseBackupsCmd = &cobra.Command{
	Use:   "backups",
	Short: "List .zip backups in the managed backups disk",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseBackupList(cmd) },
}

var addonClickHouseConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect, change and render server settings",
	Long: `Inspect and change ClickHouse server settings stored in pg.yaml and rendered
into the generated config.d XML. Keys are dotted XML paths whose text value is
the setting:

  pg addon clickhouse config set max_server_memory_usage=2147483648
  pg addon clickhouse config set logger.level=debug
  pg addon clickhouse config set query_log.flush_interval_milliseconds=1000

pgcli-managed keys (paths, listen/ports, backups, user_directories, profiles,
users, storage disks/policies) are rejected. "set"/"unset" restart a running
container by default; pass --no-restart to apply the change on the next start.
"show" prints the full generated XML.`,
}

var addonClickHouseConfigListCmd = &cobra.Command{
	Use:   "list",
	Short: "List managed and custom settings",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseConfigList(cmd) },
}

var addonClickHouseConfigGetCmd = &cobra.Command{
	Use:   "get KEY",
	Short: "Print one setting's effective value",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddonClickHouseConfigGet,
}

var addonClickHouseConfigSetCmd = &cobra.Command{
	Use:   "set KEY=VALUE...",
	Short: "Set settings and restart the container",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAddonClickHouseConfigSet,
}

var addonClickHouseConfigUnsetCmd = &cobra.Command{
	Use:   "unset KEY...",
	Short: "Remove settings and restart the container",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAddonClickHouseConfigUnset,
}

var addonClickHouseConfigShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the generated config.d XML",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseConfigShow(cmd) },
}

func init() {
	addonCmd.AddCommand(addonClickHouseCmd)
	addonClickHouseCmd.AddCommand(
		addonClickHouseStatusCmd, addonClickHouseClusterCmd, addonClickHouseShellCmd, addonClickHouseQueryCmd,
		addonClickHouseDatabaseCmd, addonClickHouseUserCmd, addonClickHouseGrantCmd,
		addonClickHouseBackupCmd, addonClickHouseRestoreCmd, addonClickHouseBackupsCmd,
		addonClickHouseConfigCmd,
	)
	addonClickHouseClusterCmd.AddCommand(addonClickHouseClusterStatusCmd)
	addonClickHouseDatabaseCmd.AddCommand(addonClickHouseDatabaseCreateCmd, addonClickHouseDatabaseListCmd, addonClickHouseDatabaseDropCmd)
	addonClickHouseUserCmd.AddCommand(addonClickHouseUserCreateCmd, addonClickHouseUserListCmd, addonClickHouseUserDropCmd)
	addonClickHouseGrantCmd.AddCommand(addonClickHouseGrantListCmd)
	addonClickHouseConfigCmd.AddCommand(addonClickHouseConfigListCmd, addonClickHouseConfigGetCmd, addonClickHouseConfigSetCmd, addonClickHouseConfigUnsetCmd, addonClickHouseConfigShowCmd)

	for _, sub := range []*cobra.Command{
		addonClickHouseStatusCmd, addonClickHouseClusterStatusCmd, addonClickHouseShellCmd, addonClickHouseQueryCmd,
		addonClickHouseDatabaseCreateCmd, addonClickHouseDatabaseListCmd, addonClickHouseDatabaseDropCmd,
		addonClickHouseUserCreateCmd, addonClickHouseUserListCmd, addonClickHouseUserDropCmd,
		addonClickHouseGrantCmd, addonClickHouseGrantListCmd,
		addonClickHouseBackupCmd, addonClickHouseRestoreCmd, addonClickHouseBackupsCmd,
		addonClickHouseConfigListCmd, addonClickHouseConfigGetCmd, addonClickHouseConfigSetCmd,
		addonClickHouseConfigUnsetCmd, addonClickHouseConfigShowCmd,
	} {
		sub.Flags().String("name", "", "clickhouse instance name (default \"clickhouse\")")
	}

	addonClickHouseQueryCmd.Flags().String("database", "", "default database (--database)")
	addonClickHouseQueryCmd.Flags().String("file", "", "read SQL from this file (\"-\" = stdin)")
	addonClickHouseDatabaseCreateCmd.Flags().String("engine", "", "database engine (default Atomic)")
	addonClickHouseDatabaseCreateCmd.Flags().String("comment", "", "database comment")
	addonClickHouseDatabaseCreateCmd.Flags().Bool("if-not-exists", false, "do nothing if the database already exists")
	addonClickHouseDatabaseDropCmd.Flags().Bool("if-exists", false, "do nothing if the database does not exist")
	addonClickHouseDatabaseDropCmd.Flags().Bool("yes", false, "skip the confirmation prompt")

	addonClickHouseUserCreateCmd.Flags().String("password", "", "user password (generated and printed when omitted)")
	addonClickHouseUserCreateCmd.Flags().String("default-database", "", "default database for the user")
	addonClickHouseUserCreateCmd.Flags().Bool("host-any", false, "allow connections from any host (default when no host flag is given)")
	addonClickHouseUserCreateCmd.Flags().Bool("host-local", false, "allow local (loopback) connections only")
	addonClickHouseUserCreateCmd.Flags().StringArray("host-ip", nil, "allow this IP or CIDR (repeatable)")
	addonClickHouseUserCreateCmd.Flags().StringArray("host-name", nil, "allow this host name (repeatable)")
	addonClickHouseUserCreateCmd.Flags().Bool("readonly", false, "create the user with SETTINGS readonly = 1")
	addonClickHouseUserCreateCmd.Flags().Bool("if-not-exists", false, "do nothing if the user already exists")
	addonClickHouseUserDropCmd.Flags().Bool("if-exists", false, "do nothing if the user does not exist")
	addonClickHouseUserDropCmd.Flags().Bool("yes", false, "skip the confirmation prompt")

	addonClickHouseGrantCmd.Flags().Bool("read", false, "grant SELECT")
	addonClickHouseGrantCmd.Flags().Bool("write", false, "grant INSERT")
	addonClickHouseGrantCmd.Flags().Bool("ddl", false, "grant CREATE, ALTER, DROP, TRUNCATE and OPTIMIZE")
	addonClickHouseGrantCmd.Flags().Bool("all", false, "grant ALL")
	addonClickHouseGrantCmd.Flags().String("privileges", "", "comma-separated explicit privilege list (e.g. SELECT,INSERT)")
	addonClickHouseGrantCmd.Flags().String("database", "", "database scope (default: all databases)")
	addonClickHouseGrantCmd.Flags().String("table", "", "table scope (requires --database)")
	addonClickHouseGrantCmd.Flags().Bool("revoke", false, "revoke instead of grant")
	addonClickHouseGrantCmd.Flags().Bool("with-grant-option", false, "add WITH GRANT OPTION")

	addonClickHouseBackupCmd.Flags().String("database", "", "database to back up (required)")
	addonClickHouseBackupCmd.Flags().String("out", "", "output file inside the backups dir (default <db>-<UTC timestamp>.zip)")
	addonClickHouseRestoreCmd.Flags().String("database", "", "database name recorded in the backup (required)")
	addonClickHouseRestoreCmd.Flags().String("as", "", "restore under a new database name")

	for _, sub := range []*cobra.Command{addonClickHouseConfigSetCmd, addonClickHouseConfigUnsetCmd} {
		sub.Flags().Bool("no-restart", false, "apply on the next start instead of restarting the container now")
	}

	// ClickHouse-specific install flags. --name/--data-dir/--listen/
	// --http-port/--admin-user/--admin-password/--version/--force/
	// --config-option/--cluster/--peer/--advertise-host are shared.
	addonInstallCmd.Flags().Int("tcp-port", 0, "clickhouse native-protocol host port (0=auto-assign from clickhouse_tcp_start_port, default 9000; the S3 API pool shares this range). In cluster mode it is this member's native endpoint and is immutable after install")
	addonInstallCmd.Flags().Int("shard", 0, "clickhouse cluster mode: this member's 1-based shard number (all shards must have the same replica count)")
	addonInstallCmd.Flags().Int("replica", 0, "clickhouse cluster mode: this member's 1-based replica number within the shard")
	addonInstallCmd.Flags().StringArray("keeper", nil, "clickhouse cluster mode: a ClickHouse Keeper client endpoint HOST[:PORT] (repeatable; pass the full shared list on every member, default port 9181)")
	addonInstallCmd.Flags().Int("interserver-port", 0, "clickhouse cluster mode: interserver HTTP replication port (default 9009, fixed)")
}
