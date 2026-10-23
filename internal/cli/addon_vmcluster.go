package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// runAddonInstallVMCluster installs a VictoriaMetrics cluster — three components
// (vminsert, vmselect, vmstorage) each running as a separate container. Unlike
// the single-node victoriametrics addon, this supports cross-host deployment
// via --roles and --storage-node, enabling horizontal scaling and HA.
//
// The design mirrors BeeGFS v2: each host runs its own `pg addon install vmcluster`
// with --roles selecting which components THIS host runs, and --storage-node
// listing ALL vmstorage addresses for sharding and replication. pgcli never SSHes.
func runAddonInstallVMCluster(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vmc"
	}
	roles, _ := cmd.Flags().GetString("roles")
	storageNodes, _ := cmd.Flags().GetStringArray("storage-node")
	insertImage, _ := cmd.Flags().GetString("insert-image")
	selectImage, _ := cmd.Flags().GetString("select-image")
	storageImage, _ := cmd.Flags().GetString("storage-image")
	insertPort, _ := cmd.Flags().GetInt("insert-port")
	selectPort, _ := cmd.Flags().GetInt("select-port")
	storagePort, _ := cmd.Flags().GetInt("storage-port")
	internalInsertPort, _ := cmd.Flags().GetInt("internal-insert-port")
	internalSelectPort, _ := cmd.Flags().GetInt("internal-select-port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	retentionMonths, _ := cmd.Flags().GetString("retention-months")
	replicationFactor, _ := cmd.Flags().GetInt("replication-factor")
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

	if cfg.Addons.VMCluster == nil {
		cfg.Addons.VMCluster = make(map[string]config.VMClusterConfig)
	}
	existing, ok := cfg.Addons.VMCluster[name]
	if !ok {
		existing = config.VMClusterConfig{
			ContainerPrefix: "pgcli-vmcluster" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:            name,
		}
	}

	// Parse roles (comma-separated → slice).
	if roles != "" {
		existing.Roles = strings.Split(roles, ",")
		for i := range existing.Roles {
			existing.Roles[i] = strings.TrimSpace(existing.Roles[i])
		}
	}

	// Parse storage nodes (format: host:insertPort:selectPort).
	if cmd.Flags().Changed("storage-node") {
		existing.StorageNodes = nil
		for _, sn := range storageNodes {
			parts := strings.Split(sn, ":")
			if len(parts) != 3 {
				return fmt.Errorf("invalid --storage-node format %q: expected host:insertPort:selectPort (e.g. 10.0.0.1:8483:8484)", sn)
			}
			var insertP, selectP int
			if _, err := fmt.Sscanf(parts[1], "%d", &insertP); err != nil {
				return fmt.Errorf("invalid insert port in --storage-node %q: %w", sn, err)
			}
			if _, err := fmt.Sscanf(parts[2], "%d", &selectP); err != nil {
				return fmt.Errorf("invalid select port in --storage-node %q: %w", sn, err)
			}
			existing.StorageNodes = append(existing.StorageNodes, config.VMClusterStorageNode{
				Address:    fmt.Sprintf("%s:%d", parts[0], insertP),
				SelectPort: selectP,
			})
		}
	}

	// Apply flag overrides.
	if insertImage != "" {
		existing.InsertImageTag = insertImage
	}
	if selectImage != "" {
		existing.SelectImageTag = selectImage
	}
	if storageImage != "" {
		existing.StorageImageTag = storageImage
	}
	if cmd.Flags().Changed("insert-port") {
		existing.InsertPort = insertPort
	}
	if cmd.Flags().Changed("select-port") {
		existing.SelectPort = selectPort
	}
	if cmd.Flags().Changed("storage-port") {
		existing.StoragePort = storagePort
	}
	if cmd.Flags().Changed("internal-insert-port") {
		existing.InternalInsertPort = internalInsertPort
	}
	if cmd.Flags().Changed("internal-select-port") {
		existing.InternalSelectPort = internalSelectPort
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if retentionMonths != "" {
		existing.Retention = retentionMonths
	}
	if cmd.Flags().Changed("replication-factor") {
		existing.ReplicationFactor = replicationFactor
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
		existing.StorageDataDir = dataDir
	}

	cfg.Addons.VMCluster[name] = existing
	cfg.ApplyDefaults()
	vc := cfg.Addons.VMCluster[name]

	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	vmc, err := podman.NewVMClusterManager(cfg)
	if err != nil {
		return fmt.Errorf("VMCluster manager: %w", err)
	}

	// Skip recreate when already running and --force is not set.
	running, err := vmc.ContainerExists(vc.ContainerName(config.VMClusterRoleStorage))
	if err != nil {
		return err
	}
	if running && !force && roles == "" {
		fmt.Println("  [OK] VMCluster running, config unchanged")
		cfg.Addons.VMCluster[name] = vc
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		printVMClusterSummary(&vc, vmc, name, false, false)
		return nil
	}

	fmt.Printf("-> Preparing VMCluster images (roles: %s)...\n", strings.Join(vc.RolesInOrder(), ", "))
	if err := vmc.EnsureImages(&vc); err != nil {
		return err
	}

	fmt.Println("-> Starting VMCluster containers...")
	if err := vmc.EnsureCluster(&vc); err != nil {
		return err
	}

	cfg.Addons.VMCluster[name] = vc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	printVMClusterSummary(&vc, vmc, name, true, force)
	return nil
}

func printVMClusterSummary(vc *config.VMClusterConfig, vmc *podman.VMClusterManager, name string, installed, force bool) {
	fmt.Println()
	switch {
	case installed && force:
		fmt.Printf("✓ VMCluster %q recreated (--force)\n", name)
	case installed:
		fmt.Printf("✓ VMCluster installed: %q\n", name)
	default:
		fmt.Printf("✓ VMCluster %q (unchanged)\n", name)
	}

	roles := vc.RolesInOrder()
	fmt.Printf("  Roles:       %s", strings.Join(roles, ", "))
	if len(roles) == 3 {
		fmt.Println(" (converged on this host)")
	} else {
		fmt.Println(" (on this host)")
	}

	// Containers.
	fmt.Print("  Containers:  ")
	for i, role := range roles {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Print(vc.ContainerName(role))
	}
	fmt.Println()

	// Images.
	fmt.Print("  Images:      ")
	for i, role := range roles {
		if i > 0 {
			fmt.Print(", ")
		}
		switch role {
		case config.VMClusterRoleInsert:
			fmt.Print(vc.InsertImageTag)
		case config.VMClusterRoleSelect:
			fmt.Print(vc.SelectImageTag)
		case config.VMClusterRoleStorage:
			fmt.Print(vc.StorageImageTag)
		}
	}
	fmt.Println()

	// Data dir (only for storage role).
	if vc.RunsRole(config.VMClusterRoleStorage) {
		fmt.Printf("  Data:        %s\n", vmc.DataDir(vc))
		if vc.Retention != "" {
			fmt.Printf("  Retention:   %s month(s)\n", vc.Retention)
		}
	}

	// Replication factor.
	if vc.ReplicationFactor > 1 {
		fmt.Printf("  Replication: %d\n", vc.ReplicationFactor)
	}

	// URLs.
	if vc.RunsRole(config.VMClusterRoleInsert) {
		fmt.Printf("  Insert URL:  http://%s:%d/ (vminsert — ingestion)\n", vc.Listen, vc.InsertPort)
	}
	if vc.RunsRole(config.VMClusterRoleSelect) {
		fmt.Printf("  Select URL:  http://%s:%d/ (vmselect — queries)\n", vc.Listen, vc.SelectPort)
	}
	if vc.RunsRole(config.VMClusterRoleStorage) {
		fmt.Printf("  Storage URL: http://%s:%d/ (vmstorage — admin/metrics)\n", vc.Listen, vc.StoragePort)
		fmt.Printf("  Internal:    TCP %d (vminsert), %d (vmselect)\n", vc.InternalInsertPort, vc.InternalSelectPort)
	}

	// Cluster topology hint.
	nodes := vc.EffectiveStorageNodes()
	if len(nodes) > 0 {
		fmt.Println()
		fmt.Println("  Cluster topology:")
		fmt.Print("    Storage nodes: ")
		for i, sn := range nodes {
			if i > 0 {
				fmt.Print(", ")
			}
			host := strings.Split(sn.Address, ":")[0]
			insertPort := strings.Split(sn.Address, ":")[1]
			fmt.Printf("%s:%s:%d", host, insertPort, sn.SelectPort)
		}
		fmt.Println()

		// Hint for adding insert/select on another host.
		if len(roles) == 1 && roles[0] == config.VMClusterRoleStorage {
			fmt.Println()
			fmt.Println("  To add vminsert/vmselect on another host:")
			fmt.Printf("    pg addon install vmcluster --name %s --roles insert,select \\\n", name)
			fmt.Print("      --storage-node ")
			for i, sn := range nodes {
				if i > 0 {
					fmt.Print(" \\\n      --storage-node ")
				}
				host := strings.Split(sn.Address, ":")[0]
				insertPort := strings.Split(sn.Address, ":")[1]
				fmt.Printf("%s:%s:%d", host, insertPort, sn.SelectPort)
			}
			fmt.Println()
		}
	}

	fmt.Println()
	if vc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface. VMCluster has NO authentication —")
		fmt.Println("        its HTTP API runs arbitrary MetricsQL, so these endpoints are guarded")
		fmt.Println("        only by your firewall. Use --listen 127.0.0.1 for loopback-only.")
	}
}

func runAddonRemoveVMCluster(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vmc"
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

	if cfg.Addons.VMCluster == nil {
		return fmt.Errorf("no VMCluster add-ons configured")
	}
	vc, ok := cfg.Addons.VMCluster[name]
	if !ok {
		return fmt.Errorf("VMCluster %q not found", name)
	}

	vmc, err := podman.NewVMClusterManager(cfg)
	if err != nil {
		return fmt.Errorf("VMCluster manager: %w", err)
	}

	fmt.Printf("-> Removing VMCluster %q...\n", name)
	if err := vmc.RemoveCluster(&vc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.VMCluster, name)
	if len(cfg.Addons.VMCluster) == 0 {
		cfg.Addons.VMCluster = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ VMCluster %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives stored time series; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartVMCluster(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vmc"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.VMCluster == nil {
		return fmt.Errorf("no VMCluster add-ons configured (run 'pg addon install vmcluster')")
	}
	vc, ok := cfg.Addons.VMCluster[name]
	if !ok {
		return fmt.Errorf("VMCluster %q not found (run 'pg addon install vmcluster --name %s')", name, name)
	}
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}
	vmc, err := podman.NewVMClusterManager(cfg)
	if err != nil {
		return fmt.Errorf("VMCluster manager: %w", err)
	}
	fmt.Printf("-> Starting VMCluster %q...\n", name)
	return vmc.StartCluster(&vc)
}

func runAddonStopVMCluster(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "vmc"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	vc, ok := cfg.Addons.VMCluster[name]
	if !ok {
		return fmt.Errorf("VMCluster %q not found", name)
	}
	vmc, err := podman.NewVMClusterManager(cfg)
	if err != nil {
		return fmt.Errorf("VMCluster manager: %w", err)
	}
	fmt.Printf("-> Stopping VMCluster %q...\n", name)
	if err := vmc.StopCluster(&vc); err != nil {
		return err
	}
	fmt.Printf("✓ VMCluster %q stopped\n", name)
	return nil
}
