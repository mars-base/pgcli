package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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

Infra addons (shared, not tied to one instance):
  pg addon install etcd
          Stored under top-level addons.etcd in config.
  pg addon install pgdog
          Stored under top-level addons.pgdog in config.

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
  etcd        standalone key-value store (HA cluster DCS)
  pgdog       Postgres proxy (pooling, load balancing, sharding)

Two modes (pgbouncer):
  Local:  pg addon install pgbouncer -i <instance>
  Remote: pg addon install pgbouncer --dsn <dsn> --pg-name <name>

Infra addon (etcd — shared, not tied to an instance):
  pg addon install etcd [--name ha] [--client-port N] [--peer-port N]
                        [--image ...] [--data-dir ...]

Infra addon (pgdog — shared Postgres proxy):
  pg addon install pgdog [--name proxy] [--backend NAME=HOST:PORT:DB[:SHARD[:ROLE]]]
                         [--user NAME:PASSWORD[:DBNAME]] [--sharded-table DB:TABLE:COLUMN:TYPE]
                         [--port N] [--pool-mode ...] [--workers N] [--default-pool-size N]

Re-running install is idempotent — it re-syncs all users and passwords from
pg_shadow, regenerates config files and restarts the container.

Examples:
  pg addon install pgbouncer -i proj01
  pg addon install pgbouncer --dsn "postgres://admin:pass@host:35432/proj01_db" --pg-name remote-proj01
  pg addon install etcd
  pg addon install etcd --name ha --client-port 2379 --peer-port 2380
  pg addon install pgdog --backend app=127.0.0.1:5432:appdb
  pg addon install pgdog --backend app=127.0.0.1:5432:shard0:0 --backend app=127.0.0.1:5433:shard1:1 --user alice:s3cret:app`,
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

	// etcd flags (top-level shared-infrastructure addon)
	addonInstallCmd.Flags().String("name", "", "addon key/name for the etcd member or pgdog proxy (default \"etcd\"/\"pgdog\")")
	addonInstallCmd.Flags().Int("client-port", 0, "etcd client host port (0=auto-assign from etcd_start_port)")
	addonInstallCmd.Flags().Int("peer-port", 0, "etcd peer host port (0=auto-assign, next free port after client)")
	addonInstallCmd.Flags().String("image", "", "etcd image tag (default quay.io/coreos/etcd:v3.5.30)")
	addonInstallCmd.Flags().String("cluster", "", "etcd cluster name (--initial-cluster-token, default \"pgcli-etcd\")")
	addonInstallCmd.Flags().String("data-dir", "", "etcd data dir root, absolute or relative to base_dir (default <base_dir>/addon/etcd); each member uses <root>/<name>/data")
	addonInstallCmd.Flags().String("advertise-host", "", "host advertised in this member's peer/client URLs (empty=127.0.0.1 for single-host; set a LAN IP or FQDN for cross-host clusters)")
	addonInstallCmd.Flags().String("join", "", "client endpoint of an existing cluster member to join cross-host, e.g. http://10.241.20.147:2379 (implies --initial-cluster-state existing; requires --advertise-host)")
	addonRemoveCmd.Flags().String("name", "", "name of the etcd member or pgdog proxy to remove (default \"etcd\"/\"pgdog\")")

	// pgdog flags (top-level shared Postgres proxy addon)
	addonInstallCmd.Flags().Int("port", 0, "PgDog client host port (0=auto-assign from pgdog_start_port; openmetrics takes the next free port)")
	addonInstallCmd.Flags().String("host", "", "PgDog listen address (default 127.0.0.1)")
	addonInstallCmd.Flags().String("pool-mode", "", "PgDog pooler mode: transaction (default) or session")
	addonInstallCmd.Flags().Int("workers", 0, "PgDog worker threads (default 2)")
	addonInstallCmd.Flags().StringArray("backend", nil, "backend database NAME=HOST:PORT:DBNAME[:SHARD[:ROLE]] (repeatable)")
	addonInstallCmd.Flags().StringArray("user", nil, "proxy user NAME:PASSWORD[:DBNAME] (repeatable; DBNAME defaults to the first backend's name)")
	addonInstallCmd.Flags().StringArray("sharded-table", nil, "sharded table DBNAME:TABLE:COLUMN:DATA_TYPE (repeatable)")
}

// ---------------------------------------------------------------------------
// install logic
// ---------------------------------------------------------------------------

func runAddonInstall(addonName string, cmd *cobra.Command) error {
	switch addonName {
	case "etcd":
		return runAddonInstallEtcd(cmd)
	case "pgdog":
		return runAddonInstallPgDog(cmd)
	case "pgbouncer":
		// falls through to the PgBouncer flow below
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog)", addonName)
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
	// macOS: bring up the podman machine and the pgcli-net bridge that the
	// pgbouncer container joins (no-ops on Linux).
	if err := pm.EnsureMachine(); err != nil {
		return err
	}
	if err := pm.EnsureNetwork(); err != nil {
		return err
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

	// 5. Get or allocate PgBouncer config (needed before SetupAuth for auth user name)
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
				DSN:             dsn,
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

	// Set BackendHost from DSN
	if host, port, _, _, _, err := podman.ParseDSN(dsn); err == nil {
		if dsn != "" && pgName != "" {
			pbConf := cfg.Addons.PgBouncer[pgName]
			pbConf.BackendHost = fmt.Sprintf("%s:%d", host, port)
			cfg.Addons.PgBouncer[pgName] = pbConf
		} else {
			inst := cfg.Instances[instName]
			inst.Addons.PgBouncer.BackendHost = fmt.Sprintf("%s:%d", host, port)
			cfg.Instances[instName] = inst
		}
	}

	// Let config auto-assign port if zero
	cfg.ApplyDefaults()

	// Re-fetch pointer after ApplyDefaults (map values are copied)
	if dsn != "" && pgName != "" {
		pbConf = cfg.Addons.PgBouncer[pgName]
	} else {
		pbConf = *cfg.Instances[instName].Addons.PgBouncer
	}

	// 6. Setup auth_query user and function on PG (needs pbConf for auth user name)
	fmt.Println("-> Setting up PgBouncer auth_query...")
	authUser, err := pbMgr.SetupAuth(dsn, instName)
	if err != nil {
		return err
	}
	fmt.Printf("  [OK] auth user %q configured\n", authUser.User)

	// 7. Generate config files
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
// install logic — etcd
// ---------------------------------------------------------------------------

// runAddonInstallEtcd installs a standalone single-member etcd container as a
// top-level (shared infrastructure) addon. Unlike pgbouncer it is not tied to
// a specific instance and needs no DSN/auth setup.
func runAddonInstallEtcd(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "etcd"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	clientPort, _ := cmd.Flags().GetInt("client-port")
	peerPort, _ := cmd.Flags().GetInt("peer-port")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	cluster, _ := cmd.Flags().GetString("cluster")
	advertiseHost, _ := cmd.Flags().GetString("advertise-host")
	joinEndpoint, _ := cmd.Flags().GetString("join")

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

	if joinEndpoint != "" {
		if advertiseHost == "" {
			return fmt.Errorf("--join requires --advertise-host so existing members can reach this new member over the network")
		}
		if cluster == "" {
			return fmt.Errorf("--join requires --cluster (must match the remote cluster's --initial-cluster-token)")
		}
	}

	if cfg.Addons.Etcd == nil {
		cfg.Addons.Etcd = make(map[string]config.EtcdConfig)
	}
	existing, ok := cfg.Addons.Etcd[name]
	if !ok {
		existing = config.EtcdConfig{
			ContainerName: "pgcli-etcd" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if clientPort != 0 {
		existing.ClientPort = clientPort
	}
	if peerPort != 0 {
		existing.PeerPort = peerPort
	}
	if dataDir != "" {
		base := cfg.BaseDir
		if base == "" {
			base = platform.DefaultConfigDir()
		}
		existing.DataDir = resolveUnderBase(base, dataDir)
	}
	if existing.Name == "" {
		existing.Name = name
	}
	if cluster != "" {
		existing.ClusterName = cluster
	}
	if advertiseHost != "" {
		existing.AdvertiseHost = advertiseHost
	}
	cfg.Addons.Etcd[name] = existing

	// Fill defaults and auto-assign any unset ports.
	cfg.ApplyDefaults()
	ec := cfg.Addons.Etcd[name]

	em, err := podman.NewEtcdManager(cfg)
	if err != nil {
		return fmt.Errorf("etcd manager: %w", err)
	}

	peerURL := ec.PeerURL()

	switch {
	case joinEndpoint != "":
		// Cross-host join: no coordinator container on this machine. Use
		// etcdctl from a short-lived container to register with the remote
		// coordinator, then start with the authoritative initial-cluster it
		// prints (already reflects every existing member's advertised URL,
		// plus this new one).
		initialCluster, err := joinViaEndpoint(em, ec, joinEndpoint, peerURL)
		if err != nil {
			return err
		}
		if err := em.EnsureContainer(&ec, initialCluster, "existing"); err != nil {
			return err
		}

	default:
		// Local-cluster path: peers are pgcli-managed members in the same
		// config. First member bootstraps; later ones join via a running
		// peer container.
		var peers []config.EtcdConfig
		for k, v := range cfg.Addons.Etcd {
			if k != name && v.ClusterName == ec.ClusterName {
				peers = append(peers, v)
			}
		}
		sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })

		initialCluster, state := bootstrapCluster(ec, peers)

		if len(peers) == 0 {
			fmt.Println("-> Bootstrapping etcd cluster...")
		} else {
			coord, ok := pickCoordinator(em, ec, peers)
			if !ok {
				return fmt.Errorf("cluster %q has no running member to join; start one of the existing etcd members first", ec.ClusterName)
			}
			registered, err := em.MemberExists(coord.ContainerName, coord.ClientPort, ec.Name)
			if err != nil {
				return fmt.Errorf("querying etcd members: %w", err)
			}
			if !registered {
				fmt.Println("-> Registering new member with the cluster...")
				if err := em.MemberAdd(coord.ContainerName, coord.ClientPort, ec.Name, peerURL); err != nil {
					return err
				}
			}
			fmt.Println("-> Starting etcd container (joining cluster)...")
		}

		if err := em.EnsureContainer(&ec, initialCluster, state); err != nil {
			return err
		}
	}

	cfg.Addons.Etcd[name] = ec
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ etcd installed: %q\n", name)
	fmt.Printf("  Container:    %s\n", ec.ContainerName)
	fmt.Printf("  Cluster:      %s\n", ec.ClusterName)
	fmt.Printf("  Data dir:     %s\n", em.DataDir(&ec))
	fmt.Printf("  Client port:  %d\n", ec.ClientPort)
	fmt.Printf("  Peer port:    %d\n", ec.PeerPort)
	fmt.Printf("  Advertise:    %s\n", ec.AdvertiseAddr())
	fmt.Println()
	fmt.Printf("  Client URL: %s\n", ec.ClientURL())
	fmt.Printf("  Connect (etcdctl): ETCDCTL_ENDPOINTS=%s\n", ec.ClientURL())
	return nil
}

// joinViaEndpoint registers a new member with a coordinator reachable at
// coordinatorEndpoint (cross-host or a non-pgcli-managed peer) and returns the
// authoritative ETCD_INITIAL_CLUSTER from etcdctl's response, to be passed to
// the joining container. Idempotent in the other direction: it errors when the
// member name is already registered, since re-adding would fail and the caller
// should remove the stale member first.
func joinViaEndpoint(em *podman.EtcdManager, ec config.EtcdConfig, coordinatorEndpoint, peerURL string) (string, error) {
	registered, err := em.MemberExistsAt(ec.ImageTag, coordinatorEndpoint, ec.Name)
	if err != nil {
		return "", err
	}
	if registered {
		return "", fmt.Errorf("member %q is already registered with the cluster at %s — remove it first with `pg etcdctl member remove` against that endpoint", ec.Name, coordinatorEndpoint)
	}
	fmt.Printf("-> Registering member with the cluster at %s...\n", coordinatorEndpoint)
	out, err := em.MemberAddAt(ec.ImageTag, coordinatorEndpoint, ec.Name, peerURL)
	if err != nil {
		return "", err
	}
	initialCluster, ok := parseInitialCluster(out)
	if !ok {
		return "", fmt.Errorf("coordinator response did not contain ETCD_INITIAL_CLUSTER:\n%s", out)
	}
	fmt.Println("-> Starting etcd container (joining cluster)...")
	return initialCluster, nil
}

// parseInitialCluster extracts the ETCD_INITIAL_CLUSTER value from the
// `etcdctl member add` response, e.g.:
//
//	ETCD_INITIAL_CLUSTER="m1=http://10.0.0.1:2380,m2=http://10.0.0.2:2380"
func parseInitialCluster(out string) (string, bool) {
	const key = "ETCD_INITIAL_CLUSTER=\""
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, key); i >= 0 {
			rest := line[i+len(key):]
			if j := strings.IndexByte(rest, '"'); j >= 0 {
				return rest[:j], true
			}
		}
	}
	return "", false
}

// resolveUnderBase turns a user-supplied data dir into an absolute path:
// absolute values are returned cleaned as-is, relative values are joined onto
// base (the config's base_dir). This is what lets `--data-dir ./etcd-data`
// land under the pg config file's base directory.
func resolveUnderBase(base, dir string) string {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(base, dir)
}

// bootstrapCluster returns the etcd --initial-cluster value and its state for
// a member, given the other members already in the same cluster. The first
// member of a cluster bootstraps with state "new" (single-member list);
// subsequent members join with state "existing" and the full peer list. Each
// entry uses that member's advertised peer URL, so a mixed host/network
// layout is represented correctly.
func bootstrapCluster(ec config.EtcdConfig, peers []config.EtcdConfig) (initialCluster, state string) {
	if len(peers) == 0 {
		return ec.Name + "=" + ec.PeerURL(), "new"
	}
	parts := make([]string, 0, len(peers)+1)
	for _, p := range peers {
		parts = append(parts, p.Name+"="+p.PeerURL())
	}
	parts = append(parts, ec.Name+"="+ec.PeerURL())
	return strings.Join(parts, ","), "existing"
}

// pickCoordinator returns a running member of the cluster to use for member
// registration, preferring the member's own already-running container (so a
// restart doesn't need a re-add), then any running peer.
func pickCoordinator(em *podman.EtcdManager, ec config.EtcdConfig, peers []config.EtcdConfig) (config.EtcdConfig, bool) {
	if running, err := em.ContainerRunning(ec.ContainerName); err == nil && running {
		return ec, true
	}
	for _, p := range peers {
		if running, err := em.ContainerRunning(p.ContainerName); err == nil && running {
			return p, true
		}
	}
	return config.EtcdConfig{}, false
}

// ---------------------------------------------------------------------------
// install logic — pgdog
// ---------------------------------------------------------------------------

// parsePgDogBackend parses a --backend value of the form
// NAME=HOST:PORT:DBNAME[:SHARD[:ROLE]]. NAME is the logical database name
// clients connect to; SHARD defaults to 0 and ROLE to empty (pgdog's default,
// primary). Used to build one [[databases]] entry in pgdog.toml.
func parsePgDogBackend(spec string) (config.PgDogBackend, error) {
	var b config.PgDogBackend
	name, rest, ok := strings.Cut(spec, "=")
	if !ok || name == "" {
		return b, fmt.Errorf("backend %q must be NAME=HOST:PORT:DBNAME[:SHARD[:ROLE]]", spec)
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 3 || len(parts) > 5 {
		return b, fmt.Errorf("backend %q: expected HOST:PORT:DBNAME[:SHARD[:ROLE]] after the name", spec)
	}
	b.Name = name
	b.Host = parts[0]
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return b, fmt.Errorf("backend %q: invalid port %q", spec, parts[1])
	}
	b.Port = port
	b.DatabaseName = parts[2]
	if len(parts) >= 4 {
		shard, err := strconv.Atoi(parts[3])
		if err != nil {
			return b, fmt.Errorf("backend %q: invalid shard %q", spec, parts[3])
		}
		b.Shard = shard
	}
	if len(parts) == 5 {
		b.Role = parts[4]
	}
	if b.Host == "" || b.DatabaseName == "" {
		return b, fmt.Errorf("backend %q: host and database name must not be empty", spec)
	}
	return b, nil
}

// parsePgDogUser parses a --user value of the form NAME:PASSWORD[:DBNAME].
// TODO: also support the backend role decoupling pgdog offers — server_user /
// server_password per [[users]] (and user/password per [[databases]]) — so the
// role PgDog uses to reach the backends need not equal the client --user name.
// DBNAME (the logical database the user may reach) defaults to defaultDB, the
// first backend's name. Passwords may contain ':' — only the first two fields
// are split off, the remainder is the password+dbname tail; since DBNAME is
// optional we take the last ':'-segment as the database only when the spec has
// at least three fields. Returns the parsed user.
func parsePgDogUser(spec, defaultDB string) (config.PgDogUser, error) {
	var u config.PgDogUser
	name, rest, ok := strings.Cut(spec, ":")
	if !ok || name == "" {
		return u, fmt.Errorf("user %q must be NAME:PASSWORD[:DBNAME]", spec)
	}
	// The password is everything up to the final ':' segment when a DBNAME is
	// present, or the whole remainder otherwise. A DBNAME never contains ':',
	// so the last segment is unambiguously the database when there are >=2
	// remaining ':'-parts.
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		u.Password = rest[:i]
		u.Database = rest[i+1:]
	} else {
		u.Password = rest
		u.Database = defaultDB
	}
	if u.Password == "" {
		return u, fmt.Errorf("user %q: password must not be empty", spec)
	}
	if u.Database == "" {
		u.Database = defaultDB
	}
	u.Name = name
	return u, nil
}

// parsePgDogShardedTable parses a --sharded-table value of the form
// DBNAME:TABLE:COLUMN:DATA_TYPE into one [[sharded_tables]] entry.
func parsePgDogShardedTable(spec string) (config.PgDogShardedTable, error) {
	var t config.PgDogShardedTable
	parts := strings.Split(spec, ":")
	if len(parts) != 4 {
		return t, fmt.Errorf("sharded-table %q must be DBNAME:TABLE:COLUMN:DATA_TYPE", spec)
	}
	t.Database, t.Name, t.Column, t.DataType = parts[0], parts[1], parts[2], parts[3]
	for _, f := range []*string{&t.Database, &t.Name, &t.Column, &t.DataType} {
		if *f == "" {
			return t, fmt.Errorf("sharded-table %q: all fields must be non-empty", spec)
		}
	}
	return t, nil
}

// runAddonInstallPgDog installs a standalone PgDog proxy container as a
// top-level (shared infrastructure) addon. Its pgdog.toml/users.toml are
// generated entirely from the flags below — there is no config file editing.
// At least one --backend is required; --user entries authenticate clients.
func runAddonInstallPgDog(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgdog"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")
	poolMode, _ := cmd.Flags().GetString("pool-mode")
	workers, _ := cmd.Flags().GetInt("workers")
	defaultPoolSize, _ := cmd.Flags().GetInt("default-pool-size")
	backendSpecs, _ := cmd.Flags().GetStringArray("backend")
	userSpecs, _ := cmd.Flags().GetStringArray("user")
	shardSpecs, _ := cmd.Flags().GetStringArray("sharded-table")

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

	if len(backendSpecs) == 0 {
		return fmt.Errorf("--backend is required (at least one NAME=HOST:PORT:DBNAME)")
	}
	if len(userSpecs) == 0 {
		return fmt.Errorf("--user is required (at least one NAME:PASSWORD[:DBNAME])")
	}

	backends := make([]config.PgDogBackend, 0, len(backendSpecs))
	for _, s := range backendSpecs {
		b, err := parsePgDogBackend(s)
		if err != nil {
			return err
		}
		backends = append(backends, b)
	}
	// --user DBNAME defaults to the first backend's logical database name.
	users := make([]config.PgDogUser, 0, len(userSpecs))
	for _, s := range userSpecs {
		u, err := parsePgDogUser(s, backends[0].Name)
		if err != nil {
			return err
		}
		// Every referenced database name must exist among the backends.
		found := false
		for _, b := range backends {
			if b.Name == u.Database {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("user %q references database %q, which no --backend defines", u.Name, u.Database)
		}
		users = append(users, u)
	}
	sharded := make([]config.PgDogShardedTable, 0, len(shardSpecs))
	for _, s := range shardSpecs {
		t, err := parsePgDogShardedTable(s)
		if err != nil {
			return err
		}
		sharded = append(sharded, t)
	}

	if cfg.Addons.PgDog == nil {
		cfg.Addons.PgDog = make(map[string]config.PgDogConfig)
	}
	existing, ok := cfg.Addons.PgDog[name]
	if !ok {
		existing = config.PgDogConfig{
			ContainerName: "pgcli-pgdog" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if imageTag != "" {
		existing.ImageTag = imageTag
	}
	if port != 0 {
		existing.HostPort = port
	}
	if host != "" {
		existing.Host = host
	}
	if poolMode != "" {
		existing.PoolerMode = poolMode
	}
	if workers != 0 {
		existing.Workers = workers
	}
	if defaultPoolSize != 0 {
		existing.DefaultPoolSize = defaultPoolSize
	}
	// Lists are fully replaced by the flags: pgdog.toml is generated solely
	// from this command's inputs, so re-running install is a clean re-render.
	existing.Backends = backends
	existing.Users = users
	existing.ShardedTables = sharded
	if existing.Name == "" {
		existing.Name = name
	}
	cfg.Addons.PgDog[name] = existing

	cfg.ApplyDefaults()
	pd := cfg.Addons.PgDog[name]

	dm, err := podman.NewPgDogManager(cfg)
	if err != nil {
		return fmt.Errorf("pgdog manager: %w", err)
	}

	tomlPath, usersPath, err := dm.WriteConfigs(&pd)
	if err != nil {
		return err
	}

	// macOS: bring up the podman machine and the pgcli-net bridge the container
	// joins (no-ops on Linux).
	pm, err := podman.New(cfg)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	if err := pm.EnsureMachine(); err != nil {
		return err
	}
	if err := pm.EnsureNetwork(); err != nil {
		return err
	}

	fmt.Println("-> Starting PgDog container...")
	if err := dm.EnsureContainer(&pd); err != nil {
		return err
	}

	cfg.Addons.PgDog[name] = pd
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ pgdog installed: %q\n", name)
	fmt.Printf("  Container:    %s\n", pd.ContainerName)
	fmt.Printf("  Image:        %s\n", pd.ImageTag)
	fmt.Printf("  Pooler mode:  %s\n", pd.PoolerMode)
	fmt.Printf("  Listen:       %s\n", pd.ClientAddr())
	fmt.Printf("  Metrics:      http://%s:%d/metrics\n", pd.Host, pd.OpenmetricsPort)
	fmt.Printf("  Backends:     %d\n", len(pd.Backends))
	fmt.Printf("  Users:        %d\n", len(pd.Users))
	fmt.Printf("  Config:       %s\n", tomlPath)
	fmt.Printf("  Users file:   %s (mode 0600)\n", usersPath)
	fmt.Println()
	fmt.Printf("  Connect via PgDog:\n")
	fmt.Printf("    postgres://%s@%s/%s\n", pd.Users[0].Name, pd.ClientAddr(), pd.Users[0].Database)
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
	em, _ := podman.NewEtcdManager(cfg)
	dm, _ := podman.NewPgDogManager(cfg)

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
		fmt.Printf("    Status:      %s\n", status)
		fmt.Printf("    Host:        %s:%d\n", inst.Postgres.Host, pb.HostPort)
		fmt.Printf("    Backend:     %s\n", pb.BackendHost)
		fmt.Printf("    Port:        %d\n", pb.HostPort)
		fmt.Printf("    Pool mode:   %s\n", pb.PoolMode)
		fmt.Printf("    Container:   %s\n", pb.ContainerName)
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
			fmt.Printf("    Status:      %s\n", status)
			fmt.Printf("    Host:        127.0.0.1:%d\n", pb.HostPort)
			fmt.Printf("    Backend:     %s\n", pb.BackendHost)
			fmt.Printf("    Port:        %d\n", pb.HostPort)
			fmt.Printf("    Pool mode:   %s\n", pb.PoolMode)
			fmt.Printf("    Container:   %s\n", pb.ContainerName)
		}
	}
	if !hasRemote {
		fmt.Println("  (none)")
	}

	// etcd (shared infrastructure, top-level addon)
	fmt.Println()
	fmt.Println("Infra add-ons (etcd):")
	hasEtcd := false
	for name, ec := range cfg.Addons.Etcd {
		hasEtcd = true
		status := "stopped"
		if em != nil {
			if running, err := em.ContainerRunning(ec.ContainerName); err == nil && running {
				status = "running"
			}
		}
		fmt.Printf("  %s (name: %s)\n", "etcd", name)
		fmt.Printf("    Status:      %s\n", status)
		fmt.Printf("    Cluster:     %s\n", ec.ClusterName)
		fmt.Printf("    Client URL:  http://127.0.0.1:%d\n", ec.ClientPort)
		fmt.Printf("    Client port: %d\n", ec.ClientPort)
		fmt.Printf("    Peer port:   %d\n", ec.PeerPort)
		fmt.Printf("    Image:       %s\n", ec.ImageTag)
		fmt.Printf("    Container:   %s\n", ec.ContainerName)
	}
	if !hasEtcd {
		fmt.Println("  (none)")
	}

	// pgdog (shared infrastructure proxy, top-level addon)
	fmt.Println()
	fmt.Println("Infra add-ons (pgdog):")
	hasPgDog := false
	for name, pd := range cfg.Addons.PgDog {
		hasPgDog = true
		status := "stopped"
		if dm != nil {
			if running, err := dm.ContainerRunning(pd.ContainerName); err == nil && running {
				status = "running"
			}
		}
		fmt.Printf("  %s (name: %s)\n", "pgdog", name)
		fmt.Printf("    Status:      %s\n", status)
		fmt.Printf("    Listen:      %s\n", pd.ClientAddr())
		fmt.Printf("    Client port: %d\n", pd.HostPort)
		fmt.Printf("    Metrics:     http://%s:%d/metrics\n", pd.Host, pd.OpenmetricsPort)
		fmt.Printf("    Pool mode:   %s\n", pd.PoolerMode)
		fmt.Printf("    Backends:    %d\n", len(pd.Backends))
		fmt.Printf("    Users:       %d\n", len(pd.Users))
		fmt.Printf("    Image:       %s\n", pd.ImageTag)
		fmt.Printf("    Container:   %s\n", pd.ContainerName)
	}
	if !hasPgDog {
		fmt.Println("  (none)")
	}

	return nil
}

// ---------------------------------------------------------------------------
// remove logic
// ---------------------------------------------------------------------------

func runAddonRemove(addonName string, cmd *cobra.Command) error {
	switch addonName {
	case "etcd":
		return runAddonRemoveEtcd(cmd)
	case "pgdog":
		return runAddonRemovePgDog(cmd)
	case "pgbouncer":
		// falls through to the PgBouncer flow below
	default:
		return fmt.Errorf("unknown addon: %s (available: pgbouncer, etcd, pgdog)", addonName)
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
// remove logic — etcd
// ---------------------------------------------------------------------------

func runAddonRemoveEtcd(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "etcd"
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

	if cfg.Addons.Etcd == nil {
		return fmt.Errorf("no etcd add-ons configured")
	}
	ec, ok := cfg.Addons.Etcd[name]
	if !ok {
		return fmt.Errorf("etcd %q not found", name)
	}

	em, err := podman.NewEtcdManager(cfg)
	if err != nil {
		return fmt.Errorf("etcd manager: %w", err)
	}

	// If other members of the same cluster are running, deregister this one
	// from the cluster first (via a running peer) so the quorum does not keep
	// a stale entry. Best-effort: skip when no peer is available (e.g. last
	// member, or cluster already down).
	for k, p := range cfg.Addons.Etcd {
		if k == name || p.ClusterName != ec.ClusterName {
			continue
		}
		if running, err := em.ContainerRunning(p.ContainerName); err == nil && running {
			fmt.Println("-> Deregistering member from the cluster...")
			if err := em.MemberRemove(p.ContainerName, p.ClientPort, ec.Name); err != nil {
				return err
			}
			break
		}
	}

	fmt.Printf("-> Removing etcd %q...\n", name)
	if err := em.Remove(&ec); err != nil {
		return err
	}

	delete(cfg.Addons.Etcd, name)
	if len(cfg.Addons.Etcd) == 0 {
		cfg.Addons.Etcd = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ etcd %q removed\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// remove logic — pgdog
// ---------------------------------------------------------------------------

func runAddonRemovePgDog(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "pgdog"
	}

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

	if cfg.Addons.PgDog == nil {
		return fmt.Errorf("no pgdog add-ons configured")
	}
	pd, ok := cfg.Addons.PgDog[name]
	if !ok {
		return fmt.Errorf("pgdog %q not found", name)
	}

	dm, err := podman.NewPgDogManager(cfg)
	if err != nil {
		return fmt.Errorf("pgdog manager: %w", err)
	}

	fmt.Printf("-> Removing pgdog %q...\n", name)
	if err := dm.Remove(&pd); err != nil {
		return err
	}

	delete(cfg.Addons.PgDog, name)
	if len(cfg.Addons.PgDog) == 0 {
		cfg.Addons.PgDog = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ pgdog %q removed\n", name)
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
