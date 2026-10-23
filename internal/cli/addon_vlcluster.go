package cli

import (
	"fmt"
	"net"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// runAddonInstallVLCluster installs a VictoriaLogs cluster — vlinsert, vlselect,
// and vlstorage all running from the SAME image and differing only by flags.
// Like vmcluster, it supports cross-host deployment via --roles and
// --storage-node, enabling horizontal scaling (sharding, no replication).
//
// Each host runs its own `pg addon install vlcluster` with --roles selecting
// which components THIS host runs, and --storage-node listing ALL vlstorage
// addresses (host:storagePort). pgcli never SSHes.
func runAddonInstallVLCluster(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vlc"
	}
	roles, _ := cmd.Flags().GetString("roles")
	storageNodes, _ := cmd.Flags().GetStringArray("storage-node")
	insertPort, _ := cmd.Flags().GetInt("insert-port")
	selectPort, _ := cmd.Flags().GetInt("select-port")
	storagePort, _ := cmd.Flags().GetInt("storage-port")
	image, _ := cmd.Flags().GetString("image")
	listenAddr, _ := cmd.Flags().GetString("listen")
	retention, _ := cmd.Flags().GetString("retention")
	insertExtraArgs, _ := cmd.Flags().GetStringArray("insert-extra-arg")
	selectExtraArgs, _ := cmd.Flags().GetStringArray("select-extra-arg")
	storageExtraArgs, _ := cmd.Flags().GetStringArray("storage-extra-arg")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	force, _ := cmd.Flags().GetBool("force")

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Addons.VLCluster == nil {
		cfg.Addons.VLCluster = make(map[string]config.VLClusterConfig)
	}
	existing, ok := cfg.Addons.VLCluster[name]
	if !ok {
		existing = config.VLClusterConfig{
			ContainerPrefix: "pgcli-vlcluster" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:            name,
		}
	}

	// Snapshot before applying flags: the no-op gate below compares it with the
	// post-defaults config to detect any real change.
	previous := existing

	// Parse roles (comma-separated → slice).
	if cmd.Flags().Changed("roles") {
		parsed, err := parseVLClusterRoles(roles)
		if err != nil {
			return err
		}
		if parsed != nil {
			existing.Roles = parsed
		}
	}

	// Parse storage nodes (format: host:storagePort).
	if cmd.Flags().Changed("storage-node") {
		parsed, err := parseVLClusterStorageNodes(storageNodes)
		if err != nil {
			return err
		}
		existing.StorageNodes = parsed
	}

	// Apply flag overrides.
	if cmd.Flags().Changed("insert-port") {
		existing.InsertPort = insertPort
	}
	if cmd.Flags().Changed("select-port") {
		existing.SelectPort = selectPort
	}
	if cmd.Flags().Changed("storage-port") {
		existing.StoragePort = storagePort
	}
	if image != "" {
		existing.ImageTag = image
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if cmd.Flags().Changed("retention") {
		existing.Retention = retention
	}
	if cmd.Flags().Changed("insert-extra-arg") {
		existing.InsertExtraArgs = insertExtraArgs
	}
	if cmd.Flags().Changed("select-extra-arg") {
		existing.SelectExtraArgs = selectExtraArgs
	}
	if cmd.Flags().Changed("storage-extra-arg") {
		existing.StorageExtraArgs = storageExtraArgs
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}

	cfg.Addons.VLCluster[name] = existing
	cfg.ApplyDefaults()
	vc := cfg.Addons.VLCluster[name]

	if err := validateVLClusterConfig(&vc); err != nil {
		return err
	}

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	vcm, err := podman.NewVLClusterManager(cfg)
	if err != nil {
		return fmt.Errorf("VLCluster manager: %w", err)
	}

	// No-op when neither config nor containers changed. The container check
	// covers both missing roles (e.g. a previous partial install) and stale
	// role containers left behind by a manual config edit.
	if !force && reflect.DeepEqual(previous, vc) {
		inSync, err := vlClusterContainersInSync(vcm, &vc)
		if err != nil {
			return err
		}
		if inSync {
			fmt.Println("  [OK] VLCluster running, config unchanged")
			cfg.Addons.VLCluster[name] = vc
			if err := cfg.Save(path); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}
			printVLClusterSummary(&vc, vcm, name, false, false)
			return nil
		}
	}

	fmt.Printf("-> Preparing VLCluster image (roles: %s)...\n", strings.Join(vc.RolesInOrder(), ", "))
	if err := vcm.EnsureImage(&vc); err != nil {
		return err
	}

	fmt.Println("-> Starting VLCluster containers...")
	if err := vcm.EnsureCluster(&vc); err != nil {
		return err
	}

	cfg.Addons.VLCluster[name] = vc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printVLClusterSummary(&vc, vcm, name, true, force)
	return nil
}

// parseVLClusterRoles parses a comma-separated role list. An empty string
// yields nil (keep the existing roles). Unknown roles are rejected.
func parseVLClusterRoles(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		role := strings.TrimSpace(part)
		if role == "" {
			continue
		}
		switch role {
		case config.VLClusterRoleInsert, config.VLClusterRoleSelect, config.VLClusterRoleStorage:
		default:
			return nil, fmt.Errorf("invalid role %q in --roles: valid roles are insert, select, storage", role)
		}
		if !slices.Contains(out, role) {
			out = append(out, role)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--roles must not be empty: valid roles are insert, select, storage")
	}
	return out, nil
}

// parseVLClusterStorageNodes parses repeatable --storage-node values in
// host:storagePort form. net.SplitHostPort also rejects vmcluster's
// three-part host:insertPort:selectPort format. Entries are normalized through
// net.JoinHostPort (IPv6 brackets) and deduped in order.
func parseVLClusterStorageNodes(raw []string) ([]string, error) {
	var out []string
	for _, sn := range raw {
		host, portStr, err := net.SplitHostPort(strings.TrimSpace(sn))
		if err != nil {
			return nil, fmt.Errorf("invalid --storage-node %q: expected host:storagePort (e.g. 10.0.0.1:9432): %w", sn, err)
		}
		if host == "" {
			return nil, fmt.Errorf("invalid --storage-node %q: host must not be empty", sn)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid --storage-node %q: port must be 1-65535", sn)
		}
		normalized := net.JoinHostPort(host, strconv.Itoa(port))
		if !slices.Contains(out, normalized) {
			out = append(out, normalized)
		}
	}
	return out, nil
}

// validateVLClusterConfig rejects configurations that would silently produce a
// broken cluster: missing ports, duplicate ports, or access-only hosts with no
// storage nodes to dial (those would fall back to loopback and join nothing).
func validateVLClusterConfig(vc *config.VLClusterConfig) error {
	roles := vc.RolesInOrder()
	if len(roles) == 0 {
		return fmt.Errorf("no valid VLCluster roles configured: valid roles are insert, select, storage")
	}
	ports := []struct {
		name string
		port int
	}{
		{"insert", vc.InsertPort},
		{"select", vc.SelectPort},
		{"storage", vc.StoragePort},
	}
	seen := map[int]string{}
	for _, p := range ports {
		if p.port == 0 {
			return fmt.Errorf("VLCluster %s port not assigned (set --%s-port or vlcluster_start_port)", p.name, p.name)
		}
		if other, dup := seen[p.port]; dup {
			return fmt.Errorf("VLCluster ports must differ: %s and %s both use %d", other, p.name, p.port)
		}
		seen[p.port] = p.name
	}
	access := vc.RunsRole(config.VLClusterRoleInsert) || vc.RunsRole(config.VLClusterRoleSelect)
	if access && !vc.RunsRole(config.VLClusterRoleStorage) && len(vc.StorageNodes) == 0 {
		return fmt.Errorf("this host runs insert/select without storage — pass --storage-node host:storagePort for every vlstorage (repeatable)")
	}
	return nil
}

// vlClusterContainersInSync reports whether this host's containers exactly
// match the configured roles: every configured role has a container and every
// non-configured role has none.
func vlClusterContainersInSync(vcm *podman.VLClusterManager, vc *config.VLClusterConfig) (bool, error) {
	allRoles := []string{config.VLClusterRoleStorage, config.VLClusterRoleInsert, config.VLClusterRoleSelect}
	for _, role := range allRoles {
		exists, err := vcm.ContainerExists(vc.ContainerName(role))
		if err != nil {
			return false, err
		}
		if exists != vc.RunsRole(role) {
			return false, nil
		}
	}
	return true, nil
}

func printVLClusterSummary(vc *config.VLClusterConfig, vcm *podman.VLClusterManager, name string, installed, force bool) {
	fmt.Println()
	switch {
	case installed && force:
		fmt.Printf("✓ VLCluster %q recreated (--force)\n", name)
	case installed:
		fmt.Printf("✓ VLCluster installed: %q\n", name)
	default:
		fmt.Printf("✓ VLCluster %q (unchanged)\n", name)
	}

	roles := vc.RolesInOrder()
	fmt.Printf("  Roles:       %s", strings.Join(roles, ", "))
	if len(roles) == 3 {
		fmt.Println(" (converged on this host)")
	} else {
		fmt.Println(" (on this host)")
	}

	fmt.Print("  Containers:  ")
	for i, role := range roles {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Print(vc.ContainerName(role))
	}
	fmt.Println()

	fmt.Printf("  Image:       %s\n", vc.ImageTag)

	if vc.RunsRole(config.VLClusterRoleStorage) {
		fmt.Printf("  Data:        %s\n", vcm.DataDir(vc))
		if vc.Retention != "" {
			fmt.Printf("  Retention:   %s\n", vc.Retention)
		} else {
			fmt.Println("  Retention:   binary default (--retention to change)")
		}
	}

	if vc.RunsRole(config.VLClusterRoleInsert) {
		fmt.Printf("  Insert URL:  http://%s:%d/ (vlinsert — ingestion)\n", vc.Listen, vc.InsertPort)
	}
	if vc.RunsRole(config.VLClusterRoleSelect) {
		fmt.Printf("  Select URL:  http://%s:%d/ (vlselect — LogsQL queries)\n", vc.Listen, vc.SelectPort)
	}
	if vc.RunsRole(config.VLClusterRoleStorage) {
		fmt.Printf("  Storage URL: http://%s:%d/ (vlstorage — internal APIs + admin)\n", vc.Listen, vc.StoragePort)
	}

	nodes := vc.EffectiveStorageNodes()
	if len(nodes) > 0 {
		fmt.Println()
		fmt.Println("  Cluster topology:")
		fmt.Printf("    Storage nodes: %s\n", strings.Join(nodes, ", "))

		if len(roles) == 1 && roles[0] == config.VLClusterRoleStorage {
			fmt.Println()
			fmt.Println("  To add vlinsert/vlselect on another host:")
			fmt.Printf("    pg addon install vlcluster --name %s --roles insert,select \\\n", name)
			for i, node := range nodes {
				cont := ""
				if i < len(nodes)-1 {
					cont = " \\"
				}
				fmt.Printf("      --storage-node %s%s\n", node, cont)
			}
		}
	}

	fmt.Println()
	if vc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface. VLCluster has NO authentication —")
		fmt.Println("        its HTTP API ingests and queries arbitrary logs, so these endpoints")
		fmt.Println("        are guarded only by your firewall. Use --listen 127.0.0.1 for")
		fmt.Println("        loopback-only.")
	}
}

func runAddonRemoveVLCluster(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vlc"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Addons.VLCluster == nil {
		return fmt.Errorf("no VLCluster add-ons configured")
	}
	vc, ok := cfg.Addons.VLCluster[name]
	if !ok {
		return fmt.Errorf("VLCluster %q not found", name)
	}

	vcm, err := podman.NewVLClusterManager(cfg)
	if err != nil {
		return fmt.Errorf("VLCluster manager: %w", err)
	}

	fmt.Printf("-> Removing VLCluster %q...\n", name)
	if err := vcm.RemoveCluster(&vc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.VLCluster, name)
	if len(cfg.Addons.VLCluster) == 0 {
		cfg.Addons.VLCluster = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ VLCluster %q removed\n", name)
	if !cleanData && vc.RunsRole(config.VLClusterRoleStorage) {
		fmt.Printf("  (data dir kept — reinstall the same name revives stored logs; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartVLCluster(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vlc"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.VLCluster == nil {
		return fmt.Errorf("no VLCluster add-ons configured (run 'pg addon install vlcluster')")
	}
	vc, ok := cfg.Addons.VLCluster[name]
	if !ok {
		return fmt.Errorf("VLCluster %q not found (run 'pg addon install vlcluster --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	vcm, err := podman.NewVLClusterManager(cfg)
	if err != nil {
		return fmt.Errorf("VLCluster manager: %w", err)
	}
	fmt.Printf("-> Starting VLCluster %q...\n", name)
	return vcm.StartCluster(&vc)
}

func runAddonStopVLCluster(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vlc"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	vc, ok := cfg.Addons.VLCluster[name]
	if !ok {
		return fmt.Errorf("VLCluster %q not found", name)
	}
	vcm, err := podman.NewVLClusterManager(cfg)
	if err != nil {
		return fmt.Errorf("VLCluster manager: %w", err)
	}
	fmt.Printf("-> Stopping VLCluster %q...\n", name)
	if err := vcm.StopCluster(&vc); err != nil {
		return err
	}
	fmt.Printf("✓ VLCluster %q stopped\n", name)
	return nil
}
