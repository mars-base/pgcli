package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/pitr"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.Flags().BoolVar(&startAll, "all", false, "start all configured instances")
	startCmd.Flags().BoolVar(&startAutostartFlag, "autostart", false, "start only instances/addons with autostart enabled (used by the boot service)")
	startCmd.MarkFlagsMutuallyExclusive("all", "autostart")
}

var (
	startAll           bool
	startAutostartFlag bool
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start pg services (PostgreSQL + pgBackRest)",
	Long: `start initializes the runtime environment and launches the PostgreSQL container.

Steps:
  1. Check dependencies
  2. podman machine init/start (macOS only)
  3. Build PostgreSQL + pgBackRest image (if missing)
  4. Create data directories (if missing)
  5. Start PostgreSQL container
  6. Initialize pgBackRest stanza (if PITR enabled)

Use --all to start all instances configured in the current config file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if startAutostartFlag {
			return startAutostart()
		}
		if startAll {
			return startAllInstances()
		}
		return startInstance()
	},
}

// startAutostart starts only the instances and addons that have autostart
// enabled in the config. Invoked by the boot service (systemd unit or
// launchd agent) after a host reboot. Returns nil when nothing is enabled
// so the boot unit always succeeds.
func startAutostart() error {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cfgPath = path

	// Instances with autostart enabled, sorted for deterministic order.
	var names []string
	for name, inst := range c.Instances {
		if inst.Autostart {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	if len(names) == 0 && !c.Backup.Autostart && !hasAutostartPgbouncer(c) && !hasAutostartEtcd(c) {
		fmt.Println("-> No autostart targets configured (pg autostart enable -i <name> / --backup / --pgbouncer / --etcd)")
		return nil
	}

	var firstErr error
	ok := 0
	for _, name := range names {
		fmt.Printf("\n>>> starting instance %q (autostart) <<<\n", name)
		if err := startSingle(c, name); err != nil {
			fmt.Printf("  [X] %s: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok++
	}
	if len(names) > 0 {
		fmt.Printf("\n>>> started %d/%d autostart instances <<<\n", ok, len(names))
	}

	// Shared backup container. When PITR is enabled, doStart already ensured
	// it per instance; the explicit call covers configs without PITR.
	if c.Backup.Autostart {
		bm, err := podman.NewBackupManager(c)
		if err != nil {
			fmt.Printf("  [X] backup autostart: %v\n", err)
			if firstErr == nil {
				firstErr = err
			}
		} else if err := bm.EnsureBackupInfra(); err != nil {
			fmt.Printf("  [X] backup autostart: %v\n", err)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			fmt.Println("  [OK] backup container running (autostart)")
		}
	}

	// PgBouncer containers (local per-instance + remote).
	if err := startAutostartPgbouncers(c); err != nil && firstErr == nil {
		firstErr = err
	}

	// etcd members (top-level infra addon).
	if err := startAutostartEtcds(c); err != nil && firstErr == nil {
		firstErr = err
	}

	return firstErr
}

func hasAutostartPgbouncer(c *config.Config) bool {
	for _, inst := range c.Instances {
		if inst.Addons.PgBouncer != nil && inst.Addons.PgBouncer.Autostart {
			return true
		}
	}
	for _, pb := range c.Addons.PgBouncer {
		if pb.Autostart {
			return true
		}
	}
	return false
}

func startAutostartPgbouncers(c *config.Config) error {
	pbMgr, err := podman.NewPgBouncerManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for name, inst := range c.Instances {
		if inst.Addons.PgBouncer != nil && inst.Addons.PgBouncer.Autostart {
			if err := pbMgr.StartContainer(inst.Addons.PgBouncer, name); err != nil {
				fmt.Printf("  [X] pgbouncer autostart (%s): %v\n", name, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	for name, pb := range c.Addons.PgBouncer {
		if pb.Autostart {
			if err := pbMgr.StartContainer(&pb, name); err != nil {
				fmt.Printf("  [X] pgbouncer autostart (%s): %v\n", name, err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func hasAutostartEtcd(c *config.Config) bool {
	for _, ec := range c.Addons.Etcd {
		if ec.Autostart {
			return true
		}
	}
	return false
}

// startAutostartEtcds starts each autostart-enabled etcd member. Order is
// sorted by member name for deterministic, readable boot logs.
func startAutostartEtcds(c *config.Config) error {
	var names []string
	for name, ec := range c.Addons.Etcd {
		if ec.Autostart {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	em, err := podman.NewEtcdManager(c)
	if err != nil {
		return err
	}
	var firstErr error
	for _, name := range names {
		ec := c.Addons.Etcd[name]
		if err := em.StartContainer(&ec); err != nil {
			fmt.Printf("  [X] etcd autostart (%s): %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// startAllInstances starts every instance listed in the config file.
func startAllInstances() error {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if len(c.Instances) == 0 {
		return fmt.Errorf("no instances configured in %s", path)
	}
	cfgPath = path

	var firstErr error
	ok := 0
	for name := range c.Instances {
		fmt.Printf("\n>>> starting instance %q <<<\n", name)
		if err := startSingle(c, name); err != nil {
			fmt.Printf("  [X] %s: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok++
	}
	fmt.Printf("\n>>> started %d/%d instances <<<\n", ok, len(c.Instances))
	return firstErr
}

// startInstance starts the instance specified by -i (default "default").
func startInstance() error {
	if err := loadConfig(); err != nil {
		return err
	}
	return doStart(cfg)
}

// startSingle starts a specific instance from a shared config.
func startSingle(c *config.Config, name string) error {
	cfgInstance = name
	if err := c.SetInstance(name); err != nil {
		return err
	}
	return doStart(c)
}

// doStart performs the actual start workflow.
func doStart(c *config.Config) error {
	fmt.Println("=== pg start ===")
	fmt.Printf("Platform: %s\n", platform.Detect())

	// 1. Check dependencies
	fmt.Println("\n-> Checking dependencies...")
	missing := platform.MissingPrereqs()
	if len(missing) > 0 {
		for _, d := range missing {
			fmt.Printf("  [X] %s: %s\n", d.Name, d.Hint)
		}
		return fmt.Errorf("missing dependencies, please install them first")
	}
	fmt.Println("  [OK] podman available")

	// 2. Initialize podman manager
	pm, err := podman.New(c)
	if err != nil {
		return err
	}

	// 3. podman machine (macOS, no-op on Linux)
	if err := pm.EnsureMachine(); err != nil {
		return err
	}

	// 4. Build image (if missing)
	if err := pm.EnsureImage(); err != nil {
		return err
	}

	// 5. Create directories (if missing)
	if err := pm.EnsureDirs(); err != nil {
		return err
	}

	// 6. Ensure shared network
	if err := pm.EnsureNetwork(); err != nil {
		return err
	}

	// 7. Replica instances: initialize data via pg_basebackup before the
	// container is created (skips initdb). No-op for primaries and on
	// subsequent starts.
	if err := pm.EnsureReplica(); err != nil {
		return err
	}

	// 8. Create and start container
	if err := pm.EnsureContainer(); err != nil {
		return err
	}

	// 8a. EnsurePGPortProxy is a no-op on Unix platforms.
	pm.EnsurePGPortProxy()

	// Wait for PostgreSQL to finish initialization (initdb + init scripts).
	fmt.Println("-> Waiting for PostgreSQL to be ready...")
	for i := 0; i < 60; i++ {
		if ready, _ := pm.PGIsReady(); ready {
			break
		}
		if cs, _ := pm.Status(); cs != nil && !cs.Running && strings.HasPrefix(strings.ToLower(cs.Status), "exited") {
			return fmt.Errorf("PostgreSQL container exited during startup (status: %s); data directory may be corrupted -- run 'pg restore -i %s --time \"...\"' to recover", cs.Status, c.Instance)
		}
		time.Sleep(time.Second)
	}

	// 7a. Ensure .pgpass exists for the postgres OS user inside the container.
	// This is needed by pg_cron and other background workers that connect via
	// TCP as localhost. The file is idempotent — overwrites with current creds.
	if err := pm.EnsurePgpass(); err != nil {
		fmt.Printf("  [!] .pgpass warning: %v\n", err)
	}

	// 7b. Apply performance tuning to postgresql.conf inside the running
	// container.  Done here (after PG is ready) so podman exec works.
	// If restart-required params changed, restart the container once more and
	// wait for PG to come back up.
	if needsRestart, err := pm.ApplyPGTuning(); err != nil {
		fmt.Printf("  [!] pg_tuning warning: %v\n", err)
	} else if needsRestart {
		fmt.Println("-> Restarting PostgreSQL to apply shared_buffers / wal_buffers changes...")
		if err := pm.StopContainer(); err != nil {
			fmt.Printf("  [!] stop container for restart: %v\n", err)
		} else if err := pm.StartContainer(); err != nil {
			fmt.Printf("  [!] start container after restart: %v\n", err)
		} else {
			fmt.Println("-> Waiting for PostgreSQL to be ready (after restart)...")
			for i := 0; i < 60; i++ {
				if ready, _ := pm.PGIsReady(); ready {
					fmt.Println("  [OK] PostgreSQL ready")
					break
				}
				time.Sleep(time.Second)
			}
		}
	}

	// 7c. Apply extensions: install packages, manage shared_preload_libraries,
	// CREATE EXTENSION if needed. If shared_preload_libraries changed, restart
	// the container and wait for PG to come back up.
	if len(c.Instances[c.Instance].Extensions) > 0 {
		if err := applyExtensions(pm, c); err != nil {
			fmt.Printf("  [!] extensions warning: %v\n", err)
		}
	}

	// 8. Initialize pgBackRest stanza (via backup container)
	if c.PITR.Enabled {
		bm, err := podman.NewBackupManager(c)
		if err != nil {
			return fmt.Errorf("creating backup manager: %w", err)
		}

		if _, err := bm.EnsureSSHKey(); err != nil {
			return fmt.Errorf("backup ssh key: %w", err)
		}

		fmt.Println("-> Authorizing backup key on PostgreSQL container...")
		if err := bm.AuthorizeKeyOnInstance(); err != nil {
			fmt.Printf("  [!] backup key authorization warning: %v\n", err)
		}

		fmt.Println("-> Ensuring backup infrastructure is ready...")
		if err := bm.EnsureBackupInfra(); err != nil {
			return fmt.Errorf("backup infrastructure: %w", err)
		}

		pt := pitr.New(c, pm, bm)
		if err := pt.EnsureStanza(); err != nil {
			fmt.Printf("  [!] stanza create warning: %v\n", err)
		}

		stanza := c.PITR.PgBackRestStanza
		archiveCmd := fmt.Sprintf("pgbackrest --stanza=%s archive-push %%p", stanza)

		// archive_mode is a postmaster-level parameter — needs restart.
		// Set it together with archive_command, then restart if either changed.
		if _, err := pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-c",
			"ALTER SYSTEM SET archive_mode = on"); err != nil {
			fmt.Printf("  [!] setting archive_mode: %v\n", err)
		}
		if _, err := pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-c",
			fmt.Sprintf("ALTER SYSTEM SET archive_command TO '%s'", archiveCmd)); err != nil {
			fmt.Printf("  [!] setting archive_command: %v\n", err)
		} else {
			fmt.Println("-> archive_command configured")
		}

		// archive_mode requires a restart (not just reload). Check if it's
		// already active; if not, restart the container.
		modeOut, _ := pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database,
			"-t", "-A", "-c", "SHOW archive_mode")
		if strings.TrimSpace(modeOut) != "on" {
			fmt.Println("-> Restarting PostgreSQL to enable archive_mode...")
			pm.StopContainer()
			pm.StartContainer()
			for i := 0; i < 60; i++ {
				if ready, _ := pm.PGIsReady(); ready {
					break
				}
				time.Sleep(time.Second)
			}
		} else {
			pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-c", "SELECT pg_reload_conf()")
		}
		pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-c", "SELECT pg_switch_wal()")

		if err := pt.CheckStanza(); err != nil {
			fmt.Printf("  [!] stanza check warning: %v\n", err)
		}

		fmt.Println("-> Waiting for WAL archiver to catch up...")
		for i := 0; i < 30; i++ {
			time.Sleep(2 * time.Second)
			out, err := pm.Exec("psql", "-U", c.Postgres.User, "-d", c.Postgres.Database, "-t", "-c",
				"SELECT count(*) FROM pg_ls_dir('pg_wal/archive_status') AS f WHERE f LIKE '%.ready'")
			if err == nil && strings.TrimSpace(out) == "0" {
				fmt.Println("  [OK] WAL archiver caught up")
				break
			}
		}
	}

	if err := c.Save(cfgPath); err != nil {
		fmt.Printf("  [!] failed to save config: %v\n", err)
	}

	fmt.Println("\nOK started")
	fmt.Printf("  PostgreSQL: postgres://%s:%s@localhost:%d/%s\n",
		c.Postgres.User, c.Postgres.Password,
		c.Postgres.Port, c.Postgres.Database)
	return nil
}

// applyExtensions manages shared_preload_libraries and runs CREATE EXTENSION
// for managed extensions. Extension packages are baked into the image by
// `pg extension install` (BuildExtensionImage), so start only needs to keep
// postgresql.conf and pg_extension in sync.
func applyExtensions(pm *podman.Manager, c *config.Config) error {
	inst := c.Instances[c.Instance]
	if len(inst.Extensions) == 0 {
		return nil
	}

	// Apply shared_preload_libraries (idempotent: no-op if already set)
	needsRestart, err := pm.ApplyExtensions(inst.Extensions)
	if err != nil {
		return fmt.Errorf("apply extensions: %w", err)
	}

	if needsRestart {
		fmt.Println("-> Restarting PostgreSQL to apply shared_preload_libraries changes...")
		if err := pm.StopContainer(); err != nil {
			return fmt.Errorf("stop container for restart: %w", err)
		}
		if err := pm.StartContainer(); err != nil {
			return fmt.Errorf("start container after restart: %w", err)
		}
		fmt.Println("-> Waiting for PostgreSQL to be ready (after restart)...")
		for i := 0; i < 60; i++ {
			if ready, _ := pm.PGIsReady(); ready {
				fmt.Println("  [OK] PostgreSQL ready")
				break
			}
			time.Sleep(time.Second)
		}
	}

	// CREATE EXTENSION IF NOT EXISTS for each managed extension.
	// Skip for replica instances: they are read-only and extensions are
	// replicated from the primary via pg_basebackup + WAL streaming.
	if !pm.IsReplica() {
		if err := pm.RunCreateExtensions(inst.Extensions); err != nil {
			return fmt.Errorf("create extensions: %w", err)
		}
	}

	return nil
}
