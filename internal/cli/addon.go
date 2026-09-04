package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// ---------------------------------------------------------------------------
// Parent command
// ---------------------------------------------------------------------------

var addonCmd = &cobra.Command{
	Use:   "addon",
	Short: "Manage add-on components",
	Long: `Manage add-on components (connection poolers, etc.).

Add-ons are sidecar containers that provide additional capabilities
for PostgreSQL instances without modifying the database itself.

Two modes:
  Local:  pg addon install pgbouncer -i <instance>
          Stored under instances.<name>.addons in config.

  Remote: pg addon install pgbouncer --dsn <dsn> --pg-name <name>
          Stored under top-level addons in config.
          --pg-name is required to identify this remote pooler.

Commands:
  pg addon install <addon>   install an add-on
  pg addon list              list all installed add-ons
  pg addon remove <addon>    remove an add-on`,
}

// ---------------------------------------------------------------------------
// install
// ---------------------------------------------------------------------------

var addonInstallCmd = &cobra.Command{
	Use:   "install <addon>",
	Short: "Install an add-on for a local or remote PostgreSQL instance",
	Long: `Install an add-on sidecar container for a PostgreSQL instance.

Currently supported add-ons:
  pgbouncer   connection pooler (transaction mode)

Two modes:
  Local:  pg addon install pgbouncer -i <instance>
  Remote: pg addon install pgbouncer --dsn <dsn> --pg-name <name>

Re-running install is idempotent — it re-syncs all users and passwords from
pg_shadow, regenerates config files and restarts the container.

Examples:
  pg addon install pgbouncer -i proj01
  pg addon install pgbouncer --dsn "postgres://admin:pass@host:35432/proj01_db" --pg-name remote-proj01`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonInstall(args[0], cmd)
	},
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

var addonListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all installed add-ons (local and remote)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonList()
	},
}

// ---------------------------------------------------------------------------
// remove
// ---------------------------------------------------------------------------

var addonRemoveCmd = &cobra.Command{
	Use:   "remove <addon>",
	Short: "Remove an add-on",
	Long: `Remove an add-on sidecar container and its configuration.

For local add-ons, use -i to specify the instance.
For remote add-ons, use --pg-name to specify the pooler name.

Examples:
  pg addon remove pgbouncer -i proj01
  pg addon remove pgbouncer --pg-name remote-proj01`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonRemove(args[0], cmd)
	},
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	rootCmd.AddCommand(addonCmd)
	addonCmd.AddCommand(addonInstallCmd, addonListCmd, addonRemoveCmd)

	// Basic flags
	addonInstallCmd.Flags().String("dsn", "", "PG instance connection string for remote mode (postgres://user:pass@host:port/db)")
	addonInstallCmd.Flags().String("pg-name", "", "name to identify a remote PgBouncer (required with --dsn)")
	addonInstallCmd.Flags().Int("max-client-conn", 0, "maximum number of client connections allowed (default 100)")
	addonInstallCmd.Flags().Int("default-pool-size", 0, "number of server connections per user/database pair (default 20)")

	// Pool sizing
	addonInstallCmd.Flags().Int("min-pool-size", 0, "minimum number of server connections to maintain (default 0)")
	addonInstallCmd.Flags().Int("reserve-pool-size", 0, "extra connections for burst traffic (default 0)")
	addonInstallCmd.Flags().Int("reserve-pool-timeout", 0, "seconds before using reserve pool (default 5)")
	addonInstallCmd.Flags().Int("max-db-connections", 0, "max server connections per database (0=unlimited)")
	addonInstallCmd.Flags().Int("max-user-connections", 0, "max server connections per user (0=unlimited)")

	// Timeouts (in seconds)
	addonInstallCmd.Flags().Int("server-idle-timeout", 0, "close idle server connections after N seconds (default 600)")
	addonInstallCmd.Flags().Int("server-lifetime", 0, "max lifetime for server connections in seconds (default 3600)")
	addonInstallCmd.Flags().Int("server-connect-timeout", 0, "timeout for connecting to PG in seconds (default 15)")
	addonInstallCmd.Flags().Int("query-timeout", 0, "max query execution time in seconds (default 0=disabled)")
	addonInstallCmd.Flags().Int("query-wait-timeout", 0, "max time to wait for server connection in seconds (default 120)")
	addonInstallCmd.Flags().Int("idle-transaction-timeout", 0, "close idle transactions after N seconds (default 0=disabled)")
	addonInstallCmd.Flags().Int("transaction-timeout", 0, "max transaction duration in seconds (default 0=disabled)")

	// Admin access
	addonInstallCmd.Flags().String("admin-users", "", "comma-separated list of admin users for console access")
	addonInstallCmd.Flags().String("stats-users", "", "comma-separated list of users for read-only stats access")

	// Logging
	addonInstallCmd.Flags().Int("log-connections", 0, "log client connections (default 1=enabled)")
	addonInstallCmd.Flags().Int("log-disconnections", 0, "log client disconnections (default 1=enabled)")

	addonRemoveCmd.Flags().String("pg-name", "", "name of a remote PgBouncer to remove")
}

// ---------------------------------------------------------------------------
// install logic
// ---------------------------------------------------------------------------

func runAddonInstall(addonName string, cmd *cobra.Command) error {
	if addonName != "pgbouncer" {
		return fmt.Errorf("unknown addon: %s (available: pgbouncer)", addonName)
	}

	dsn, _ := cmd.Flags().GetString("dsn")
	pgName, _ := cmd.Flags().GetString("pg-name")

	// Validate mutual exclusivity: --dsn/--pg-name vs -i
	if dsn != "" || pgName != "" {
		if dsn == "" {
			return fmt.Errorf("--pg-name requires --dsn")
		}
		if pgName == "" {
			return fmt.Errorf("--dsn requires --pg-name to identify this remote pooler")
		}
		if err := checkDSNInstanceConflict(cmd); err != nil {
			return err
		}
	}

	// 1. Load config
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

	// 2. Determine DSN and storage key
	var instName string // name used for config storage and config file directory
	if dsn != "" {
		// Remote mode: --dsn + --pg-name
		instName = pgName
	} else {
		// Local mode: -i
		if _, ok := cfg.Instances[cfgInstance]; !ok {
			return fmt.Errorf("instance %q not found in config", cfgInstance)
		}
		cfg.SetInstance(cfgInstance)
		dsn = cfg.GetPostgresURL()
		instName = cfgInstance
	}

	// 3. Verify connectivity
	pm, err := podman.New(cfg)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	fmt.Println("-> Checking PG connectivity...")
	if err := pm.CheckDSNReachable(dsn); err != nil {
		return err
	}

	// 4. Setup auth_query user and function on PG
	pbMgr, err := podman.NewPgBouncerManager(cfg)
	if err != nil {
		return fmt.Errorf("pgbouncer manager: %w", err)
	}
	fmt.Println("-> Setting up PgBouncer auth_query...")
	authUser, err := pbMgr.SetupAuth(dsn)
	if err != nil {
		return err
	}
	fmt.Printf("  [OK] auth user %q configured\n", authUser.User)

	// 5. Get or allocate PgBouncer config
	// Read optional override flags
	maxClientConn, _ := cmd.Flags().GetInt("max-client-conn")
	defaultPoolSize, _ := cmd.Flags().GetInt("default-pool-size")
	maxClientConnChanged := cmd.Flags().Changed("max-client-conn")
	defaultPoolSizeChanged := cmd.Flags().Changed("default-pool-size")

	// Pool sizing flags
	minPoolSize, _ := cmd.Flags().GetInt("min-pool-size")
	reservePoolSize, _ := cmd.Flags().GetInt("reserve-pool-size")
	maxDBConnections, _ := cmd.Flags().GetInt("max-db-connections")
	maxUserConnections, _ := cmd.Flags().GetInt("max-user-connections")

	// Timeout flags
	serverIdleTimeout, _ := cmd.Flags().GetInt("server-idle-timeout")
	serverLifetime, _ := cmd.Flags().GetInt("server-lifetime")
	serverConnectTimeout, _ := cmd.Flags().GetInt("server-connect-timeout")
	queryTimeout, _ := cmd.Flags().GetInt("query-timeout")
	queryWaitTimeout, _ := cmd.Flags().GetInt("query-wait-timeout")
	idleTransactionTimeout, _ := cmd.Flags().GetInt("idle-transaction-timeout")
	transactionTimeout, _ := cmd.Flags().GetInt("transaction-timeout")

	// Admin and logging flags
	adminUsers, _ := cmd.Flags().GetString("admin-users")
	statsUsers, _ := cmd.Flags().GetString("stats-users")
	logConnections, _ := cmd.Flags().GetInt("log-connections")
	logDisconnections, _ := cmd.Flags().GetInt("log-disconnections")

	var pbConf config.PgBouncerConfig
	if dsn != "" && pgName != "" {
		// Remote mode: store in top-level addons.pgbouncer.<pgName>
		if cfg.Addons.PgBouncer == nil {
			cfg.Addons.PgBouncer = make(map[string]config.PgBouncerConfig)
		}
		existing, ok := cfg.Addons.PgBouncer[pgName]
		if ok {
			pbConf = existing
		} else {
			pbConf = config.PgBouncerConfig{
				ContainerName:   "pgcli-pgbouncer" + nsSuffixCLI(cfg.Namespace) + "-" + pgName,
				ImageTag:        "edoburu/pgbouncer:latest",
				PoolMode:        "transaction",
				MaxClientConn:   100,
				DefaultPoolSize: 20,
			}
		}
		// Apply overrides if flags were explicitly set
		if maxClientConnChanged {
			pbConf.MaxClientConn = maxClientConn
		}
		if defaultPoolSizeChanged {
			pbConf.DefaultPoolSize = defaultPoolSize
		}
		// Pool sizing
		if cmd.Flags().Changed("min-pool-size") {
			pbConf.MinPoolSize = minPoolSize
		}
		if cmd.Flags().Changed("reserve-pool-size") {
			pbConf.ReservePoolSize = reservePoolSize
		}
		if cmd.Flags().Changed("max-db-connections") {
			pbConf.MaxDBConnections = maxDBConnections
		}
		if cmd.Flags().Changed("max-user-connections") {
			pbConf.MaxUserConnections = maxUserConnections
		}
		// Timeouts
		if cmd.Flags().Changed("server-idle-timeout") {
			pbConf.ServerIdleTimeout = serverIdleTimeout
		}
		if cmd.Flags().Changed("server-lifetime") {
			pbConf.ServerLifetime = serverLifetime
		}
		if cmd.Flags().Changed("server-connect-timeout") {
			pbConf.ServerConnectTimeout = serverConnectTimeout
		}
		if cmd.Flags().Changed("query-timeout") {
			pbConf.QueryTimeout = queryTimeout
		}
		if cmd.Flags().Changed("query-wait-timeout") {
			pbConf.QueryWaitTimeout = queryWaitTimeout
		}
		if cmd.Flags().Changed("idle-transaction-timeout") {
			pbConf.IdleTransactionTimeout = idleTransactionTimeout
		}
		if cmd.Flags().Changed("transaction-timeout") {
			pbConf.TransactionTimeout = transactionTimeout
		}
		// Admin and logging
		if cmd.Flags().Changed("admin-users") {
			pbConf.AdminUsers = adminUsers
		}
		if cmd.Flags().Changed("stats-users") {
			pbConf.StatsUsers = statsUsers
		}
		if cmd.Flags().Changed("log-connections") {
			pbConf.LogConnections = logConnections
		}
		if cmd.Flags().Changed("log-disconnections") {
			pbConf.LogDisconnections = logDisconnections
		}
		cfg.Addons.PgBouncer[pgName] = pbConf
	} else {
		// Local mode: store in instances.<name>.addons.pgbouncer
		inst := cfg.Instances[instName]
		if inst.Addons.PgBouncer == nil {
			inst.Addons.PgBouncer = &config.PgBouncerConfig{
				ContainerName:   "pgcli-pgbouncer" + nsSuffixCLI(cfg.Namespace) + "-" + instName,
				ImageTag:        "edoburu/pgbouncer:latest",
				PoolMode:        "transaction",
				MaxClientConn:   100,
				DefaultPoolSize: 20,
			}
		}
		// Apply overrides if flags were explicitly set
		if maxClientConnChanged {
			inst.Addons.PgBouncer.MaxClientConn = maxClientConn
		}
		if defaultPoolSizeChanged {
			inst.Addons.PgBouncer.DefaultPoolSize = defaultPoolSize
		}
		// Pool sizing
		if cmd.Flags().Changed("min-pool-size") {
			inst.Addons.PgBouncer.MinPoolSize = minPoolSize
		}
		if cmd.Flags().Changed("reserve-pool-size") {
			inst.Addons.PgBouncer.ReservePoolSize = reservePoolSize
		}
		if cmd.Flags().Changed("max-db-connections") {
			inst.Addons.PgBouncer.MaxDBConnections = maxDBConnections
		}
		if cmd.Flags().Changed("max-user-connections") {
			inst.Addons.PgBouncer.MaxUserConnections = maxUserConnections
		}
		// Timeouts
		if cmd.Flags().Changed("server-idle-timeout") {
			inst.Addons.PgBouncer.ServerIdleTimeout = serverIdleTimeout
		}
		if cmd.Flags().Changed("server-lifetime") {
			inst.Addons.PgBouncer.ServerLifetime = serverLifetime
		}
		if cmd.Flags().Changed("server-connect-timeout") {
			inst.Addons.PgBouncer.ServerConnectTimeout = serverConnectTimeout
		}
		if cmd.Flags().Changed("query-timeout") {
			inst.Addons.PgBouncer.QueryTimeout = queryTimeout
		}
		if cmd.Flags().Changed("query-wait-timeout") {
			inst.Addons.PgBouncer.QueryWaitTimeout = queryWaitTimeout
		}
		if cmd.Flags().Changed("idle-transaction-timeout") {
			inst.Addons.PgBouncer.IdleTransactionTimeout = idleTransactionTimeout
		}
		if cmd.Flags().Changed("transaction-timeout") {
			inst.Addons.PgBouncer.TransactionTimeout = transactionTimeout
		}
		// Admin and logging
		if cmd.Flags().Changed("admin-users") {
			inst.Addons.PgBouncer.AdminUsers = adminUsers
		}
		if cmd.Flags().Changed("stats-users") {
			inst.Addons.PgBouncer.StatsUsers = statsUsers
		}
		if cmd.Flags().Changed("log-connections") {
			inst.Addons.PgBouncer.LogConnections = logConnections
		}
		if cmd.Flags().Changed("log-disconnections") {
			inst.Addons.PgBouncer.LogDisconnections = logDisconnections
		}
		cfg.Instances[instName] = inst
	}

	// Let config auto-assign port if zero
	cfg.ApplyDefaults()

	// Re-fetch pointer after ApplyDefaults (map values are copied)
	if dsn != "" && pgName != "" {
		pbConf = cfg.Addons.PgBouncer[pgName]
	} else {
		pbConf = *cfg.Instances[instName].Addons.PgBouncer
	}

	// 6. Generate config files
	fmt.Println("-> Generating PgBouncer configuration...")
	iniPath, userListPath, err := pbMgr.WriteConfigs(&pbConf, authUser, dsn, instName)
	if err != nil {
		return err
	}
	fmt.Printf("  [OK] pgbouncer.ini: %s\n", iniPath)
	fmt.Printf("  [OK] userlist.txt:  %s\n", userListPath)

	// 7. Ensure container is running (create or restart)
	fmt.Println("-> Starting PgBouncer container...")
	if err := pbMgr.EnsureContainer(iniPath, userListPath, &pbConf); err != nil {
		return err
	}

	// 8. Save config
	if dsn != "" && pgName != "" {
		cfg.Addons.PgBouncer[pgName] = pbConf
	} else {
		inst := cfg.Instances[instName]
		inst.Addons.PgBouncer = &pbConf
		cfg.Instances[instName] = inst
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// 9. Print result
	fmt.Println()
	fmt.Printf("✓ PgBouncer installed for %q\n", instName)
	fmt.Printf("  PgBouncer port: %d\n", pbConf.HostPort)
	fmt.Printf("  Pool mode:      %s\n", pbConf.PoolMode)
	fmt.Printf("  Max clients:    %d\n", pbConf.MaxClientConn)
	fmt.Printf("  Pool size:      %d\n", pbConf.DefaultPoolSize)
	fmt.Println()
	fmt.Printf("  Connect via PgBouncer:\n")
	fmt.Printf("    postgres://user:pass@127.0.0.1:%d/<database>\n", pbConf.HostPort)
	return nil
}

// ---------------------------------------------------------------------------
// list logic
// ---------------------------------------------------------------------------

func runAddonList() error {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	pbMgr, _ := podman.NewPgBouncerManager(cfg)

	// Local add-ons (from instances)
	fmt.Println("Local add-ons:")
	hasLocal := false
	for name, inst := range cfg.Instances {
		if inst.Addons.PgBouncer == nil {
			continue
		}
		hasLocal = true
		pb := inst.Addons.PgBouncer
		status := "stopped"
		if pbMgr != nil {
			if running, err := pbMgr.ContainerRunning(pb.ContainerName); err == nil && running {
				status = "running"
			}
		}
		fmt.Printf("  %s (instance: %s)\n", "pgbouncer", name)
		fmt.Printf("    Status:    %s\n", status)
		fmt.Printf("    Port:      %d\n", pb.HostPort)
		fmt.Printf("    Pool mode: %s\n", pb.PoolMode)
		fmt.Printf("    Container: %s\n", pb.ContainerName)
	}
	if !hasLocal {
		fmt.Println("  (none)")
	}

	// Remote add-ons (from top-level addons)
	fmt.Println()
	fmt.Println("Remote add-ons:")
	hasRemote := false
	if cfg.Addons.PgBouncer != nil {
		for name, pb := range cfg.Addons.PgBouncer {
			hasRemote = true
			status := "stopped"
			if pbMgr != nil {
				if running, err := pbMgr.ContainerRunning(pb.ContainerName); err == nil && running {
					status = "running"
				}
			}
			fmt.Printf("  %s (pg-name: %s)\n", "pgbouncer", name)
			fmt.Printf("    Status:    %s\n", status)
			fmt.Printf("    Port:      %d\n", pb.HostPort)
			fmt.Printf("    Pool mode: %s\n", pb.PoolMode)
			fmt.Printf("    Container: %s\n", pb.ContainerName)
		}
	}
	if !hasRemote {
		fmt.Println("  (none)")
	}

	return nil
}

// ---------------------------------------------------------------------------
// remove logic
// ---------------------------------------------------------------------------

func runAddonRemove(addonName string, cmd *cobra.Command) error {
	if addonName != "pgbouncer" {
		return fmt.Errorf("unknown addon: %s (available: pgbouncer)", addonName)
	}

	pgName, _ := cmd.Flags().GetString("pg-name")

	// Determine mode: --pg-name → remote, otherwise → local (-i)
	isRemote := pgName != ""
	if !isRemote && cmd.Flags().Changed("instance") && cfgInstance != "default" {
		// -i explicitly set, use local mode
	} else if !isRemote {
		// Use the default -i value (could be "default" or user-set)
	}

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	pbMgr, err := podman.NewPgBouncerManager(cfg)
	if err != nil {
		return fmt.Errorf("pgbouncer manager: %w", err)
	}

	if isRemote {
		// Remove remote PgBouncer
		if cfg.Addons.PgBouncer == nil {
			return fmt.Errorf("no remote PgBouncer add-ons configured")
		}
		pb, ok := cfg.Addons.PgBouncer[pgName]
		if !ok {
			return fmt.Errorf("remote PgBouncer %q not found", pgName)
		}

		fmt.Printf("-> Removing remote PgBouncer %q...\n", pgName)
		if err := pbMgr.Remove(&pb, pgName); err != nil {
			return err
		}

		delete(cfg.Addons.PgBouncer, pgName)
		if len(cfg.Addons.PgBouncer) == 0 {
			cfg.Addons.PgBouncer = nil
		}
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Printf("✓ Remote PgBouncer %q removed\n", pgName)
		return nil
	}

	// Remove local PgBouncer
	if _, ok := cfg.Instances[cfgInstance]; !ok {
		return fmt.Errorf("instance %q not found in config", cfgInstance)
	}

	inst := cfg.Instances[cfgInstance]
	if inst.Addons.PgBouncer == nil {
		fmt.Printf("PgBouncer is not installed for instance %q\n", cfgInstance)
		return nil
	}

	fmt.Printf("-> Removing PgBouncer from instance %q...\n", cfgInstance)
	if err := pbMgr.Remove(inst.Addons.PgBouncer, cfgInstance); err != nil {
		return err
	}

	inst.Addons.PgBouncer = nil
	cfg.Instances[cfgInstance] = inst
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ PgBouncer removed from instance %q\n", cfgInstance)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// nsSuffixCLI returns "-<namespace>" or "" — a CLI-level helper mirroring
// config.nsSuffix which is unexported.
func nsSuffixCLI(namespace string) string {
	if namespace == "" {
		return ""
	}
	return "-" + namespace
}
