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
	Long: `Manage add-on components attached to PostgreSQL instances.

Add-ons are sidecar containers that extend a PG instance with additional
capabilities (connection pooling, etc.) without modifying the database itself.

Commands:
  pg addon install <addon>   install an add-on for the current instance
  pg addon list              list installed add-ons
  pg addon remove <addon>    remove an add-on`,
}

// ---------------------------------------------------------------------------
// install
// ---------------------------------------------------------------------------

var addonInstallCmd = &cobra.Command{
	Use:   "install <addon>",
	Short: "Install an add-on for the current instance",
	Long: `Install an add-on sidecar container for the current (or --dsn) PostgreSQL instance.

Currently supported add-ons:
  pgbouncer   connection pooler (transaction mode)

The add-on connects to the PG instance via DSN. For local instances the DSN is
constructed automatically from config; for remote instances pass --dsn explicitly.

Re-running install is idempotent — it re-syncs all users and passwords from
pg_shadow, regenerates config files and restarts the container.

Examples:
  pg addon install pgbouncer -i proj01
  pg addon install pgbouncer --dsn "postgres://admin:pass@host:35432/proj01_db"`,
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
	Short: "List installed add-ons for the current instance",
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
	Short: "Remove an add-on from the current instance",
	Long: `Remove an add-on sidecar container and its configuration.

Examples:
  pg addon remove pgbouncer -i proj01`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAddonRemove(args[0])
	},
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	rootCmd.AddCommand(addonCmd)
	addonCmd.AddCommand(addonInstallCmd, addonListCmd, addonRemoveCmd)

	addonInstallCmd.Flags().String("dsn", "", "PG instance connection string (postgres://user:pass@host:port/db)")
}

// ---------------------------------------------------------------------------
// install logic
// ---------------------------------------------------------------------------

func runAddonInstall(addonName string, cmd *cobra.Command) error {
	if addonName != "pgbouncer" {
		return fmt.Errorf("unknown addon: %s (available: pgbouncer)", addonName)
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

	// 2. Determine DSN
	dsn, _ := cmd.Flags().GetString("dsn")
	var instName string
	if dsn != "" {
		if err := checkDSNInstanceConflict(cmd); err != nil {
			return err
		}
		// --dsn mode: derive an instance name from the database portion
		_, _, _, _, db, err := podman.ParseDSN(dsn)
		if err != nil {
			return err
		}
		instName = db // use database name as a best-effort key
	} else {
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

	// 4. Sync users from pg_shadow
	pbMgr, err := podman.NewPgBouncerManager(cfg)
	if err != nil {
		return fmt.Errorf("pgbouncer manager: %w", err)
	}
	fmt.Println("-> Syncing users from pg_shadow...")
	users, err := pbMgr.SyncUsers(dsn)
	if err != nil {
		return err
	}
	fmt.Printf("  Found %d user(s)\n", len(users))

	// 5. Get or allocate PgBouncer config
	inst, ok := cfg.Instances[instName]
	if !ok {
		// --dsn mode targeting an instance not in config — create a transient entry
		inst = *cfg.InstanceDefaults(instName)
	}
	if inst.Addons.PgBouncer == nil {
		inst.Addons.PgBouncer = &config.PgBouncerConfig{
			ContainerName:   "pgcli-pgbouncer" + nsSuffixCLI(cfg.Namespace) + "-" + instName,
			ImageTag:        "edoburu/pgbouncer:latest",
			PoolMode:        "transaction",
			MaxClientConn:   100,
			DefaultPoolSize: 20,
		}
	}
	pbConf := inst.Addons.PgBouncer
	pbConf.DSN = dsn

	// Let config auto-assign port if zero
	cfg.Instances[instName] = inst
	cfg.ApplyDefaults() // re-runs port assignment for the new PgBouncer entry
	pbConf = cfg.Instances[instName].Addons.PgBouncer

	// 6. Generate config files
	fmt.Println("-> Generating PgBouncer configuration...")
	iniPath, userListPath, err := pbMgr.WriteConfigs(pbConf, users, dsn)
	if err != nil {
		return err
	}
	fmt.Printf("  [OK] pgbouncer.ini: %s\n", iniPath)
	fmt.Printf("  [OK] userlist.txt:  %s\n", userListPath)

	// 7. Ensure container is running (create or restart)
	fmt.Println("-> Starting PgBouncer container...")
	if err := pbMgr.EnsureContainer(iniPath, userListPath, pbConf); err != nil {
		return err
	}

	// 8. Save config
	cfg.Instances[instName] = cfg.Instances[instName]
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// 9. Print result
	fmt.Println()
	fmt.Printf("✓ PgBouncer installed for instance %q\n", instName)
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
	if _, ok := cfg.Instances[cfgInstance]; !ok {
		return fmt.Errorf("instance %q not found in config", cfgInstance)
	}
	cfg.SetInstance(cfgInstance)

	inst := cfg.Instances[cfgInstance]
	fmt.Printf("Add-ons for instance %q:\n", cfgInstance)

	if inst.Addons.PgBouncer == nil {
		fmt.Println("  (none)")
		return nil
	}

	pb := inst.Addons.PgBouncer
	status := "stopped"
	// Check if container is running
	pbMgr, err := podman.NewPgBouncerManager(cfg)
	if err == nil {
		running, rerr := pbMgr.ContainerRunning(pb.ContainerName)
		if rerr == nil && running {
			status = "running"
		}
	}

	fmt.Printf("  pgbouncer\n")
	fmt.Printf("    Status:    %s\n", status)
	fmt.Printf("    Port:      %d\n", pb.HostPort)
	fmt.Printf("    Pool mode: %s\n", pb.PoolMode)
	fmt.Printf("    Container: %s\n", pb.ContainerName)
	if pb.DSN != "" {
		// Mask password in displayed DSN
		fmt.Printf("    DSN:       %s\n", maskDSNPassword(pb.DSN))
	}
	return nil
}

// ---------------------------------------------------------------------------
// remove logic
// ---------------------------------------------------------------------------

func runAddonRemove(addonName string) error {
	if addonName != "pgbouncer" {
		return fmt.Errorf("unknown addon: %s (available: pgbouncer)", addonName)
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
	if _, ok := cfg.Instances[cfgInstance]; !ok {
		return fmt.Errorf("instance %q not found in config", cfgInstance)
	}
	cfg.SetInstance(cfgInstance)

	inst := cfg.Instances[cfgInstance]
	if inst.Addons.PgBouncer == nil {
		fmt.Printf("PgBouncer is not installed for instance %q\n", cfgInstance)
		return nil
	}

	pbMgr, err := podman.NewPgBouncerManager(cfg)
	if err != nil {
		return fmt.Errorf("pgbouncer manager: %w", err)
	}

	fmt.Println("-> Removing PgBouncer...")
	if err := pbMgr.Remove(inst.Addons.PgBouncer, cfgInstance); err != nil {
		return err
	}

	// Clear from config
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

// maskDSNPassword replaces the password in a DSN with "***" for display.
func maskDSNPassword(dsn string) string {
	host, port, user, _, db, err := podman.ParseDSN(dsn)
	if err != nil {
		return dsn // if parsing fails, return as-is
	}
	return fmt.Sprintf("postgres://%s:***@%s:%d/%s", user, host, port, db)
}
