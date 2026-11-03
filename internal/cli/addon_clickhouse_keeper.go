package cli

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mars-base/pgcli/internal/clickhousekeeper"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

func clickHouseKeeperFlagName(cmd *cobra.Command) string {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "clickhouse-keeper"
	}
	return name
}

func resolveClickHouseKeeperInstance(name string) (*config.Config, string, config.ClickHouseKeeperConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.ClickHouseKeeperConfig{}, err
	}
	c.ApplyDefaults()
	kc, ok := c.Addons.ClickHouseKeeper[name]
	if !ok {
		return nil, "", config.ClickHouseKeeperConfig{}, fmt.Errorf("clickhouse-keeper %q not found (see 'pg addon list')", name)
	}
	return c, path, kc, nil
}

// clickHouseKeeperInstallOptions captures the Keeper install flags and which
// were explicitly set. Membership identity is immutable after install.
type clickHouseKeeperInstallOptions struct {
	serverID         int
	advertiseHost    string
	peers            []string
	clientPort       int
	raftPort         int
	listen           string
	dataDir          string
	logDir           string
	configDir        string
	version          string
	serverIDSet      bool
	advertiseHostSet bool
	peersSet         bool
	clientPortSet    bool
	raftPortSet      bool
	listenSet        bool
	dataDirSet       bool
	logDirSet        bool
	configDirSet     bool
	versionSet       bool
	clusterSet       bool
}

func readClickHouseKeeperInstallOptions(fs *pflag.FlagSet) clickHouseKeeperInstallOptions {
	opts := clickHouseKeeperInstallOptions{
		serverIDSet:      fs.Changed("server-id"),
		advertiseHostSet: fs.Changed("advertise-host"),
		peersSet:         fs.Changed("peer"),
		clientPortSet:    fs.Changed("client-port"),
		raftPortSet:      fs.Changed("raft-port"),
		listenSet:        fs.Changed("listen"),
		dataDirSet:       fs.Changed("data-dir"),
		logDirSet:        fs.Changed("log-dir"),
		configDirSet:     fs.Changed("config-dir"),
		versionSet:       fs.Changed("version"),
		clusterSet:       fs.Changed("cluster"),
	}
	opts.serverID, _ = fs.GetInt("server-id")
	opts.advertiseHost, _ = fs.GetString("advertise-host")
	opts.peers, _ = fs.GetStringArray("peer")
	opts.clientPort, _ = fs.GetInt("client-port")
	opts.raftPort, _ = fs.GetInt("raft-port")
	opts.listen, _ = fs.GetString("listen")
	opts.dataDir, _ = fs.GetString("data-dir")
	opts.logDir, _ = fs.GetString("log-dir")
	opts.configDir, _ = fs.GetString("config-dir")
	opts.version, _ = fs.GetString("version")
	return opts
}

func applyClickHouseKeeperInstallFlags(kc *config.ClickHouseKeeperConfig, opts clickHouseKeeperInstallOptions) error {
	if opts.versionSet {
		kc.Version = opts.version
	}
	if kc.Version == "" {
		kc.Version = config.DefaultClickHouseKeeperVersion
	}
	imageTag, ok := config.ClickHouseKeeperImageTagForVersion(kc.Version)
	if !ok {
		return fmt.Errorf("ClickHouse Keeper %q is not available (supported: %s; pass the exact upstream tag)", kc.Version, config.DefaultClickHouseKeeperVersion)
	}
	kc.ImageTag = imageTag
	if opts.serverIDSet {
		kc.ServerID = opts.serverID
	}
	if opts.advertiseHostSet {
		kc.AdvertiseHost = opts.advertiseHost
	}
	if opts.peersSet {
		kc.Peers = append([]string(nil), opts.peers...)
	}
	if opts.clientPortSet {
		kc.ClientPort = opts.clientPort
	}
	if opts.raftPortSet {
		kc.RaftPort = opts.raftPort
	}
	if opts.listenSet && opts.listen != "" {
		kc.Listen = opts.listen
	}
	if opts.dataDirSet && opts.dataDir != "" {
		kc.DataDir = opts.dataDir
	}
	if opts.logDirSet && opts.logDir != "" {
		kc.LogDir = opts.logDir
	}
	if opts.configDirSet && opts.configDir != "" {
		kc.ConfigPath = filepath.Join(opts.configDir, "keeper_config.xml")
	}
	return nil
}

func checkClickHouseKeeperNoOpFlags(stored config.ClickHouseKeeperConfig, opts clickHouseKeeperInstallOptions) error {
	type mismatch struct{ flag string }
	var found []mismatch
	if opts.serverIDSet && opts.serverID != stored.ServerID {
		found = append(found, mismatch{"--server-id"})
	}
	if opts.advertiseHostSet && opts.advertiseHost != stored.AdvertiseHost {
		found = append(found, mismatch{"--advertise-host"})
	}
	if opts.peersSet && !clickHouseKeeperPeerSetsEqual(opts.peers, stored.Peers) {
		found = append(found, mismatch{"--peer"})
	}
	if opts.clientPortSet && opts.clientPort != stored.ClientPort {
		found = append(found, mismatch{"--client-port"})
	}
	if opts.raftPortSet && opts.raftPort != stored.RaftPort {
		found = append(found, mismatch{"--raft-port"})
	}
	if opts.listenSet && opts.listen != stored.Listen {
		found = append(found, mismatch{"--listen"})
	}
	if opts.dataDirSet && opts.dataDir != stored.DataDir {
		found = append(found, mismatch{"--data-dir"})
	}
	if opts.logDirSet && opts.logDir != stored.LogDir {
		found = append(found, mismatch{"--log-dir"})
	}
	if opts.configDirSet && filepath.Join(opts.configDir, "keeper_config.xml") != stored.ConfigPath {
		found = append(found, mismatch{"--config-dir"})
	}
	if opts.versionSet && opts.version != stored.Version {
		found = append(found, mismatch{"--version"})
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf("clickhouse-keeper %q is already installed; %s differs from the stored value (run with --force to apply)", stored.Name, found[0].flag)
}

func checkClickHouseKeeperIdentityImmutable(stored, next config.ClickHouseKeeperConfig) error {
	if next.ServerID != stored.ServerID {
		return fmt.Errorf("clickhouse-keeper %q is installed as server id %d; changing --server-id is not supported", next.Name, stored.ServerID)
	}
	if next.AdvertiseHost != stored.AdvertiseHost {
		return fmt.Errorf("clickhouse-keeper %q advertises %s; changing --advertise-host would invalidate every peer's --peer list", next.Name, stored.AdvertiseHost)
	}
	if next.ClientPort != stored.ClientPort {
		return fmt.Errorf("clickhouse-keeper %q serves clients on port %d; changing --client-port is not supported", next.Name, stored.ClientPort)
	}
	if next.RaftPort != stored.RaftPort {
		return fmt.Errorf("clickhouse-keeper %q uses Raft port %d; changing --raft-port is not supported", next.Name, stored.RaftPort)
	}
	if !clickHouseKeeperPeerSetsEqual(next.Peers, stored.Peers) {
		return fmt.Errorf("clickhouse-keeper %q is installed with peers %s; changing --peer is not supported (Raft membership cannot be edited in place)", next.Name, strings.Join(stored.Peers, ", "))
	}
	return nil
}

func validateClickHouseKeeperInstall(kc config.ClickHouseKeeperConfig, opts clickHouseKeeperInstallOptions, present bool) error {
	if opts.clusterSet {
		return fmt.Errorf("--cluster is not used by clickhouse-keeper; membership is defined by --peer <id>@<host>[:<raft_port>]")
	}
	if !present {
		var missing []string
		if !opts.serverIDSet {
			missing = append(missing, "--server-id")
		}
		if !opts.advertiseHostSet {
			missing = append(missing, "--advertise-host")
		}
		if len(missing) > 0 {
			return fmt.Errorf("a fresh ClickHouse Keeper install requires explicit %s", strings.Join(missing, ", "))
		}
	}
	if len(kc.Peers) > 0 && net.ParseIP(kc.Listen) != nil && net.ParseIP(kc.Listen).IsLoopback() {
		return fmt.Errorf("a multi-member Keeper requires --listen 0.0.0.0 (or a non-loopback address); peers cannot reach %s", kc.Listen)
	}
	return nil
}

func clickHouseKeeperPeerSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, raw := range a {
		peer, err := config.ParseClickHouseKeeperPeer(raw, config.DefaultClickHouseKeeperRaftPort)
		if err != nil {
			return false
		}
		seen[peer.String()] = true
	}
	if len(seen) != len(a) {
		return false
	}
	for _, raw := range b {
		peer, err := config.ParseClickHouseKeeperPeer(raw, config.DefaultClickHouseKeeperRaftPort)
		if err != nil || !seen[peer.String()] {
			return false
		}
		delete(seen, peer.String())
	}
	return len(seen) == 0
}

func normalizeClickHouseKeeperPeers(kc *config.ClickHouseKeeperConfig) {
	if len(kc.Peers) == 0 {
		return
	}
	peers := make([]config.ClickHouseKeeperPeer, 0, len(kc.Peers))
	for _, raw := range kc.Peers {
		peer, err := config.ParseClickHouseKeeperPeer(raw, kc.RaftPort)
		if err != nil {
			return
		}
		peers = append(peers, peer)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
	out := make([]string, 0, len(peers))
	for _, peer := range peers {
		out = append(out, peer.String())
	}
	kc.Peers = out
}

// --- install / lifecycle ----------------------------------------------------

func runAddonInstallClickHouseKeeper(cmd *cobra.Command) error {
	name := clickHouseKeeperFlagName(cmd)
	if !config.ValidClickHouseName(name) {
		return fmt.Errorf("invalid ClickHouse Keeper instance name %q: use letters, digits, '_' and '-', starting with a letter or digit (max 48 chars)", name)
	}
	fmt.Printf("=== pg addon install clickhouse-keeper (%s) ===\n", name)

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if c.Addons.ClickHouseKeeper == nil {
		c.Addons.ClickHouseKeeper = make(map[string]config.ClickHouseKeeperConfig)
	}
	fs := cmd.Flags()
	opts := readClickHouseKeeperInstallOptions(fs)
	force, _ := fs.GetBool("force")
	c.ApplyDefaults()
	stored, present := c.Addons.ClickHouseKeeper[name]

	if present && !force {
		if err := checkClickHouseKeeperNoOpFlags(stored, opts); err != nil {
			return err
		}
		state := "stopped"
		if m, err := podman.NewClickHouseKeeperManager(c); err == nil {
			if running, _ := m.ContainerRunning(stored.ContainerName); running {
				state = "running"
			}
		}
		fmt.Printf("✓ clickhouse-keeper %q already installed (%s, server id %d, client port %d, raft port %d) — re-install with --force to rewrite the config and recreate the container\n",
			name, state, stored.ServerID, stored.ClientPort, stored.RaftPort)
		if voters, err := config.ClickHouseKeeperVoters(stored); err == nil {
			fmt.Printf("  Members:   %s\n", clickHouseKeeperVoterStrings(voters))
		}
		return nil
	}

	kc := config.ClickHouseKeeperConfig{Name: name}
	if present {
		kc = stored
	}
	kc.Name = name
	if err := applyClickHouseKeeperInstallFlags(&kc, opts); err != nil {
		return err
	}
	c.Addons.ClickHouseKeeper[name] = kc
	c.ApplyDefaults()
	kc = c.Addons.ClickHouseKeeper[name]

	if present {
		if err := checkClickHouseKeeperIdentityImmutable(stored, kc); err != nil {
			return err
		}
	}
	if err := validateClickHouseKeeperInstall(kc, opts, present); err != nil {
		return err
	}
	normalizeClickHouseKeeperPeers(&kc)
	c.Addons.ClickHouseKeeper[name] = kc
	if err := c.Validate(); err != nil {
		return err
	}

	voters, err := config.ClickHouseKeeperVoters(kc)
	if err != nil {
		return err
	}
	if len(voters) < 3 {
		fmt.Printf("  [!] Keeper cluster has %d member(s); use 3 or 5 so one member can fail while a quorum remains\n", len(voters))
	}

	m, err := podman.NewClickHouseKeeperManager(c)
	if err != nil {
		return err
	}
	if err := m.EnsureImage(kc.ImageTag); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] image: %s\n", kc.ImageTag)
	if err := clickhousekeeper.WriteConfig(kc); err != nil {
		return fmt.Errorf("  [X] writing config: %w", err)
	}
	fmt.Printf("  [OK] config: %s\n", kc.ConfigPath)
	fmt.Printf("  [OK] data:   %s\n", kc.DataDir)
	fmt.Printf("  [OK] logs:   %s\n", kc.LogDir)

	// Persist before starting so a failed boot remains inspectable.
	c.Addons.ClickHouseKeeper[name] = kc
	if err := c.Save(path); err != nil {
		return fmt.Errorf("  [X] saving config: %w", err)
	}

	if err := m.EnsureContainer(&kc); err != nil {
		m.LogsTail(kc.ContainerName, 20)
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] container: %s\n", kc.ContainerName)

	fmt.Println()
	fmt.Printf("✓ ClickHouse Keeper %q installed\n", name)
	fmt.Printf("  Version:     %s\n", kc.Version)
	fmt.Printf("  Image:       %s\n", kc.ImageTag)
	fmt.Printf("  Container:   %s\n", kc.ContainerName)
	fmt.Printf("  Server ID:   %d\n", kc.ServerID)
	fmt.Printf("  Client:      %s:%d\n", kc.Listen, kc.ClientPort)
	fmt.Printf("  Raft:        %s:%d\n", kc.AdvertiseHost, kc.RaftPort)
	fmt.Printf("  Members:     %s\n", clickHouseKeeperVoterStrings(voters))
	fmt.Printf("  Data dir:    %s\n", kc.DataDir)
	fmt.Printf("  Logs dir:    %s\n", kc.LogDir)
	fmt.Printf("  Config:      %s\n", kc.ConfigPath)
	fmt.Println()
	fmt.Printf("  Status:   pg addon clickhouse-keeper status --name %s\n", name)
	fmt.Printf("  Shell:    pg addon clickhouse-keeper shell --name %s\n", name)
	fmt.Printf("  Logs:     pg logs addon clickhouse-keeper --name %s\n", name)
	return nil
}

func runAddonRemoveClickHouseKeeper(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name := clickHouseKeeperFlagName(cmd)
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	c.ApplyDefaults()
	kc, ok := c.Addons.ClickHouseKeeper[name]
	if !ok {
		return fmt.Errorf("clickhouse-keeper %q not found (see 'pg addon list')", name)
	}
	fmt.Printf("=== pg addon remove clickhouse-keeper (%s) ===\n", name)

	if voters, err := config.ClickHouseKeeperVoters(kc); err == nil && len(voters) > 1 {
		remaining := len(voters) - 1
		quorum := len(voters)/2 + 1
		if remaining < quorum {
			fmt.Printf("  [!] removing server id %d leaves %d member(s); a %d-member cluster needs %d for quorum. Update the --peer lists on the remaining members.\n",
				kc.ServerID, remaining, len(voters), quorum)
		} else {
			fmt.Printf("  [i] %d member(s) remain; the other members still reference this server id in their raft_configuration — update their --peer lists and recreate them.\n", remaining)
		}
	}

	m, err := podman.NewClickHouseKeeperManager(c)
	if err != nil {
		return err
	}
	if err := m.RemoveContainer(&kc); err != nil {
		return err
	}
	delete(c.Addons.ClickHouseKeeper, name)
	if len(c.Addons.ClickHouseKeeper) == 0 {
		c.Addons.ClickHouseKeeper = nil
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
		fmt.Printf("  [i] kept %s (config, data, logs) — pass --clean-data to drop\n", kc.InstanceDir)
	}
	fmt.Printf("✓ ClickHouse Keeper %q removed\n", name)
	return nil
}

func runAddonStartClickHouseKeeper(cmd *cobra.Command) error {
	name := clickHouseKeeperFlagName(cmd)
	c, _, kc, err := resolveClickHouseKeeperInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseKeeperManager(c)
	if err != nil {
		return err
	}
	already, err := m.StartContainer(&kc)
	if err != nil {
		m.LogsTail(kc.ContainerName, 20)
		return err
	}
	if already {
		fmt.Printf("✓ clickhouse-keeper %q already running (client %s:%d)\n", name, kc.Listen, kc.ClientPort)
		return nil
	}
	fmt.Printf("✓ clickhouse-keeper %q started (client %s:%d, raft %s:%d)\n", name, kc.Listen, kc.ClientPort, kc.AdvertiseHost, kc.RaftPort)
	return nil
}

func runAddonStopClickHouseKeeper(cmd *cobra.Command) error {
	name := clickHouseKeeperFlagName(cmd)
	c, _, kc, err := resolveClickHouseKeeperInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseKeeperManager(c)
	if err != nil {
		return err
	}
	wasRunning, err := m.Stop(kc.ContainerName)
	if err != nil {
		return err
	}
	if !wasRunning {
		fmt.Printf("✓ clickhouse-keeper %q already stopped\n", name)
		return nil
	}
	fmt.Printf("✓ clickhouse-keeper %q stopped\n", name)
	return nil
}

func runAddonClickHouseKeeperStatus(cmd *cobra.Command) error {
	name := clickHouseKeeperFlagName(cmd)
	c, _, kc, err := resolveClickHouseKeeperInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseKeeperManager(c)
	if err != nil {
		return err
	}
	voters, err := config.ClickHouseKeeperVoters(kc)
	if err != nil {
		return err
	}

	state := "stopped"
	running, _ := m.ContainerRunning(kc.ContainerName)
	if running {
		if err := clickhousekeeper.Ruok(clickhousekeeper.LocalProbeHost(kc.Listen), kc.ClientPort, 2*time.Second); err != nil {
			state = fmt.Sprintf("running (ruok failed: %v)", err)
		} else {
			state = "running (ruok ok)"
		}
	}
	fmt.Printf("clickhouse-keeper %q — %s\n", name, state)
	fmt.Printf("  Version:    %s\n", kc.Version)
	fmt.Printf("  Image:      %s\n", kc.ImageTag)
	fmt.Printf("  Container:  %s\n", kc.ContainerName)
	fmt.Printf("  Server ID:  %d\n", kc.ServerID)
	fmt.Printf("  Client:     %s:%d\n", kc.Listen, kc.ClientPort)
	fmt.Printf("  Raft:       %s:%d\n", kc.AdvertiseHost, kc.RaftPort)
	fmt.Printf("  Members:    %s\n", clickHouseKeeperVoterStrings(voters))
	fmt.Printf("  Data dir:   %s\n", kc.DataDir)
	fmt.Printf("  Logs dir:   %s\n", kc.LogDir)
	fmt.Printf("  Config:     %s\n", kc.ConfigPath)
	fmt.Printf("  Logs:       pg logs addon clickhouse-keeper --name %s\n", name)

	if !running {
		return nil
	}
	status, err := clickhousekeeper.KeeperClusterStatus(kc, 2*time.Second)
	if err != nil {
		fmt.Printf("  [!] membership probe failed: %v\n", err)
		return nil
	}
	fmt.Printf("  Local state: %s\n", status.LocalState)
	fmt.Printf("  Reachability:\n")
	for _, member := range status.Members {
		mark := "unreachable"
		if member.Reachable {
			mark = "reachable"
		}
		role := ""
		if member.State != "" {
			role = " (" + member.State + ")"
		}
		fmt.Printf("    server %d  %s:%d  %s%s\n", member.ServerID, member.Host, member.ClientPort, mark, role)
	}
	if status.ReachableQuorum {
		fmt.Printf("  [OK] %d/%d client endpoints reachable (quorum %d; client reachability is not a Raft quorum proof)\n",
			status.Reachable, len(status.Members), status.Quorum)
	} else {
		fmt.Printf("  [!] %d/%d client endpoints reachable (quorum %d) — the cluster has no quorum until more members return\n",
			status.Reachable, len(status.Members), status.Quorum)
	}
	return nil
}

func runAddonClickHouseKeeperShell(cmd *cobra.Command) error {
	name := clickHouseKeeperFlagName(cmd)
	c, _, kc, err := resolveClickHouseKeeperInstance(name)
	if err != nil {
		return err
	}
	m, err := podman.NewClickHouseKeeperManager(c)
	if err != nil {
		return err
	}
	if running, _ := m.ContainerRunning(kc.ContainerName); !running {
		fmt.Fprintf(os.Stderr, "[!] clickhouse-keeper %q is not running — start it with: pg addon start clickhouse-keeper --name %s\n", name, name)
	}
	return m.ExecShell(&kc)
}

func runAddonClickHouseKeeperConfigShow(cmd *cobra.Command) error {
	name := clickHouseKeeperFlagName(cmd)
	_, _, kc, err := resolveClickHouseKeeperInstance(name)
	if err != nil {
		return err
	}
	content, err := clickhousekeeper.RenderConfig(kc)
	if err != nil {
		return err
	}
	fmt.Print(content)
	return nil
}

func clickHouseKeeperVoterStrings(voters []config.ClickHouseKeeperPeer) string {
	parts := make([]string, 0, len(voters))
	for _, voter := range voters {
		parts = append(parts, voter.String())
	}
	return strings.Join(parts, ", ")
}

// --- command tree ----------------------------------------------------------

var addonClickHouseKeeperCmd = &cobra.Command{
	Use:   "clickhouse-keeper",
	Short: "Manage standalone ClickHouse Keeper (ZooKeeper-compatible) members",
	Long: `Manage ClickHouse Keeper add-on instances (official clickhouse-keeper image).

Keeper is an independent top-level addon: it has its own container, config and
data/log paths, even when it runs on the same host as a ClickHouse server.
ClickHouse replicated tables and ON CLUSTER DDL use Keeper as their
ZooKeeper-compatible coordination service.

A 3-member Keeper cluster tolerates one member failing. Use 3 or 5 members;
an even number does not add fault tolerance. Keeper has no authentication or
TLS, so run it only on a trusted private network.

Subcommands:
  status    Show container state, Raft role, member reachability and paths
  shell     Open a bash shell in the container
  config    Show the generated keeper_config.xml

Examples:
  pg addon install clickhouse-keeper --name keeper --server-id 1 \
    --advertise-host 10.0.0.11 --listen 0.0.0.0 \
    --peer 2@10.0.0.12:9234 --peer 3@10.0.0.13:9234
  pg addon clickhouse-keeper status --name keeper
  pg addon clickhouse-keeper config show --name keeper
  pg addon remove clickhouse-keeper --name keeper --clean-data`,
}

var addonClickHouseKeeperStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Keeper state, Raft role and member reachability",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseKeeperStatus(cmd) },
}

var addonClickHouseKeeperShellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Open a bash shell in the Keeper container",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseKeeperShell(cmd) },
}

var addonClickHouseKeeperConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect the generated Keeper configuration",
}

var addonClickHouseKeeperConfigShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the generated keeper_config.xml",
	RunE:  func(cmd *cobra.Command, _ []string) error { return runAddonClickHouseKeeperConfigShow(cmd) },
}

func init() {
	addonCmd.AddCommand(addonClickHouseKeeperCmd)
	addonClickHouseKeeperCmd.AddCommand(
		addonClickHouseKeeperStatusCmd, addonClickHouseKeeperShellCmd, addonClickHouseKeeperConfigCmd,
	)
	addonClickHouseKeeperConfigCmd.AddCommand(addonClickHouseKeeperConfigShowCmd)

	for _, sub := range []*cobra.Command{addonClickHouseKeeperStatusCmd, addonClickHouseKeeperShellCmd, addonClickHouseKeeperConfigShowCmd} {
		sub.Flags().String("name", "", "clickhouse-keeper instance name (default \"clickhouse-keeper\")")
	}

	addonInstallCmd.Flags().Int("server-id", 0, "clickhouse-keeper: this member's 1-based Raft server id (unique within the cluster; immutable after install)")
	addonInstallCmd.Flags().Int("raft-port", 0, "clickhouse-keeper Raft peer port (0=default 9234; immutable after install)")
	addonInstallCmd.Flags().String("log-dir", "", "clickhouse-keeper log directory (default <base_dir>/addon/clickhouse-keeper/<name>/logs)")
	addonInstallCmd.Flags().String("config-dir", "", "clickhouse-keeper config directory; keeper_config.xml is written there (default <base_dir>/addon/clickhouse-keeper/<name>/config)")
}
