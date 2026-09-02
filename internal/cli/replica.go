package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

var replicaCmd = &cobra.Command{
	Use:   "replica",
	Short: "Manage read-only physical replica instances",
	Long: `Manage read-only physical replicas.

A replica continuously streams WAL from its primary instance and serves
read-only queries. The replica shares the primary's data — including the
admin password — and is intended for read/write split and standby purposes.

Same-host replicas (default):
  pg replica create <name> -i <primary>   create a read-only replica

Cross-network replicas (two hosts, run one command on each side):
  pg replica create <name> -i <primary> --replica-host <ip|hostname>   on the primary host
  pg replica create <name> -i <primary> --primary-dsn <dsn>            on the replica host
  pg replica drop <name> -i <primary>     remove the slot on the primary host

Failover (3-step, run each on the respective host):
  pg replica promote <name>               promote a replica to primary
  pg replica drop <name> -i <old-primary> clean up old primary (step 2)
  pg replica repoint <other> --primary-dsn <dsn> --primary-name <name>
                                           re-point other replicas (step 3)

Commands:
  pg replica create <name> -i <primary>   create a read-only replica
  pg replica promote <name>               promote a replica to primary
  pg replica repoint <name> --primary-dsn <dsn> --primary-name <name>
                                           re-point replica to new primary
  pg replica list                          list replicas and replication lag
  pg replica drop <name> -i <primary>     drop a replica's slot on the primary`,
}

var replicaCreateCmd = &cobra.Command{
	Use:   "create <replica-name>",
	Short: "Create a read-only physical replica of an instance",
	Long: `Create a new read-only replica of the instance given with -i.
The replica is initialized with pg_basebackup and then continuously streams
WAL from the primary (physical replication, second-level lag).

The primary instance must be running. The replica gets its own container,
data directory and port, but shares the primary's admin password (the data
is a byte-for-byte copy).

For replicas on another host, run one command on each side:
  --replica-host <ip>  on the primary host: allows the replica host through
                       pg_hba and reserves the replication slot (run first)
  --primary-dsn <dsn>  on the replica host: copies the primary data and
                       starts the replica (run after the primary side)
  --primary-name <name>  primary instance name recorded as replica_of in
                       --primary-dsn mode (the -i instance is a local one
                       and is not used for the remote primary)

Examples:
  pg replica create ro1 -i proj01
  pg replica create ro2                # replicate the default instance
  pg replica create ro1 -i proj01 --replica-host 10.241.20.100        # primary host
  pg replica create ro1 --primary-dsn postgres://admin:pw@10.241.20.50:35432/proj01_db --primary-name proj01  # replica host`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		primaryDSN, _ := cmd.Flags().GetString("primary-dsn")
		replicaHost, _ := cmd.Flags().GetString("replica-host")
		primaryName, _ := cmd.Flags().GetString("primary-name")
		return runReplicaCreate(args[0], primaryDSN, replicaHost, primaryName, cmd.Flags().Changed("instance"))
	},
}

var replicaListCmd = &cobra.Command{
	Use:   "list",
	Short: "List replica instances and replication lag",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReplicaList()
	},
}

var replicaPromoteCmd = &cobra.Command{
	Use:   "promote <replica-name>",
	Short: "Promote a replica to a standalone read-write primary",
	Long: `Promote a physical standby replica to a read-write primary.

The replica is promoted in-place using pg_promote() (PostgreSQL 12+).
No container restart is needed — the instance exits recovery and becomes
read-write immediately.

After promotion:
  - primary_conninfo is removed from postgresql.auto.conf
  - The instance config is updated (ReplicaOf/PrimaryDSN cleared, PITR re-enabled)

This is step 1 of a 3-step failover. After promoting, run the remaining
steps on their respective hosts:
  2. pg replica drop <name> -i <old-primary>       (on the old primary host)
  3. pg replica repoint <other> --primary-dsn <dsn> --primary-name <name>
                                                    (on each other replica host)

Run "pg start -i <name>" afterward to initialize the backup stanza and
apply archive_mode (requires a restart).

Examples:
  pg replica promote ro1
  pg replica promote ro1 -i ro1`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReplicaPromote(args[0])
	},
}

var replicaDropCmd = &cobra.Command{
	Use:   "drop <replica-name>",
	Short: "Drop a replica's replication slot on the primary",
	Long: `Remove the physical replication slot reserved for a cross-network
replica from the primary instance (run on the primary host, after the
replica was destroyed on its own host). No-op when the slot does not exist.

The pg_hba entry for the replica host is kept, matching same-host destroy
behavior.

Examples:
  pg replica drop ro1 -i proj01`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReplicaDrop(args[0])
	},
}

var replicaRepointCmd = &cobra.Command{
	Use:   "repoint <replica-name>",
	Short: "Re-point a replica to a different primary",
	Long: `Re-point a running replica to stream from a different primary instance.

Run this on each remaining replica after a failover (step 3):
  1. pg replica promote <new-primary>                (on the promoted replica host)
  2. pg replica drop <new-primary> -i <old-primary>  (on the old primary host)
  3. pg replica repoint <replica> --primary-dsn <dsn> --primary-name <name>
                                                      (on this replica host)

The command creates a replication slot on the new primary (via DSN), updates
primary_conninfo on this replica, restarts the container, and verifies the
replica is streaming from the new primary.

--primary-dsn is the connection string of the new (promoted) primary.
--primary-name is the name to record as replica_of in the local config.

Examples:
  pg replica repoint ro2 --primary-dsn "postgres://admin:pw@10.241.20.50:35432/ro1_db" --primary-name ro1`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		primaryDSN, _ := cmd.Flags().GetString("primary-dsn")
		primaryName, _ := cmd.Flags().GetString("primary-name")
		return runReplicaRepoint(args[0], primaryDSN, primaryName)
	},
}

func init() {
	replicaCreateCmd.Flags().String("primary-dsn", "", "primary connection string; run on the replica host (cross-network)")
	replicaCreateCmd.Flags().String("replica-host", "", "replica host IP or hostname; run on the primary host (cross-network)")
	replicaCreateCmd.Flags().String("primary-name", "", "primary instance name for --primary-dsn mode, recorded as replica_of")
	replicaCreateCmd.MarkFlagsMutuallyExclusive("primary-dsn", "replica-host")
	replicaCreateCmd.MarkFlagsMutuallyExclusive("replica-host", "primary-name")
	replicaRepointCmd.Flags().String("primary-dsn", "", "connection string of the new primary (required)")
	replicaRepointCmd.Flags().String("primary-name", "", "name of the new primary, recorded as replica_of (required)")
	_ = replicaRepointCmd.MarkFlagRequired("primary-dsn")
	_ = replicaRepointCmd.MarkFlagRequired("primary-name")
	rootCmd.AddCommand(replicaCmd)
	replicaCmd.AddCommand(replicaCreateCmd, replicaListCmd, replicaDropCmd, replicaPromoteCmd, replicaRepointCmd)
}

func runReplicaCreate(newName, primaryDSN, replicaHost, primaryName string, iFlagSet bool) error {
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

	// Target must not exist yet.
	if _, ok := cfg.Instances[newName]; ok {
		return fmt.Errorf("instance %q already exists in config", newName)
	}

	switch {
	case replicaHost != "":
		// Primary-side preparation: -i names a local instance that must exist.
		if _, ok := cfg.Instances[cfgInstance]; !ok {
			return fmt.Errorf("primary instance %q not found in config", cfgInstance)
		}
		return runReplicaCreatePrimary(cfg, path, newName, cfgInstance, replicaHost)

	case primaryDSN != "":
		// Replica-side creation: the primary lives on another host, its name
		// comes from --primary-name. -i keeps its strict meaning: if given,
		// it must reference a real local instance.
		if primaryName == "" {
			return fmt.Errorf("--primary-name is required with --primary-dsn")
		}
		if iFlagSet {
			if _, ok := cfg.Instances[cfgInstance]; !ok {
				return fmt.Errorf("instance %q not found in config", cfgInstance)
			}
		}
		return runReplicaCreateRemote(cfg, path, newName, primaryName, primaryDSN)

	default:
		// Same-host replica: -i must exist.
		if primaryName != "" {
			return fmt.Errorf("--primary-name requires --primary-dsn")
		}
		if _, ok := cfg.Instances[cfgInstance]; !ok {
			return fmt.Errorf("primary instance %q not found in config", cfgInstance)
		}
		return runReplicaCreateSameHost(cfg, path, newName, cfgInstance)
	}
}

// runReplicaCreateSameHost registers and boots a replica on the same host as
// its primary (original behavior).
func runReplicaCreateSameHost(cfg *config.Config, path, newName, primary string) error {
	// Pre-check primary connectivity BEFORE creating anything, so a stopped
	// primary leaves no side effects behind.
	pc := *cfg
	if err := pc.SetInstance(primary); err != nil {
		return fmt.Errorf("loading primary instance: %w", err)
	}
	primaryPM, err := podman.New(&pc)
	if err != nil {
		return err
	}
	fmt.Println("Checking primary connectivity...")
	if err := primaryPM.CheckContainerRunning(); err != nil {
		return err
	}

	// Register the replica. The password is copied from the primary: physical
	// replication copies pg_authid, so both sides authenticate identically.
	// The database name is inherited too — a physical replica serves the
	// primary's databases as-is, it does not create its own. PITR is disabled
	// (a standby archives nothing and is not registered with the pgBackRest
	// backup container; backups run on the primary).
	// The image tag and extensions list are inherited so the replica container
	// has the same shared libraries referenced in postgresql.auto.conf.
	inst := cfg.InstanceDefaults(newName)
	inst.Postgres.Database = pc.Postgres.Database
	inst.Postgres.Password = pc.Postgres.Password
	inst.Podman.ImageTag = pc.Podman.ImageTag
	inst.Extensions = append([]string(nil), pc.Instances[primary].Extensions...)
	inst.PITR.Enabled = false
	inst.ReplicaOf = primary
	cfg.Instances[newName] = *inst
	cfg.ApplyDefaults()
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("Created replica %q (primary: %s)\n", newName, primary)

	// startSingle/doStart persist config via the global cfgPath.
	cfgPath = path
	if err := startSingle(cfg, newName); err != nil {
		return fmt.Errorf("starting replica %q: %w", newName, err)
	}

	rc := *cfg
	if err := rc.SetInstance(newName); err != nil {
		return err
	}

	fmt.Printf("✓ Replica %q created: postgres://%s:%s@localhost:%d/%s\n",
		newName, rc.Postgres.User, rc.Postgres.Password, rc.Podman.HostPort, rc.Postgres.Database)
	fmt.Println("  Read-only; verify with: pg exec -i " + newName + " \"SELECT pg_is_in_recovery()\"")
	return nil
}

// runReplicaCreatePrimary runs on the primary host: allows the replica host
// through pg_hba and reserves the replication slot. It creates nothing
// locally — the replica is created on its own host with --primary-dsn.
func runReplicaCreatePrimary(cfg *config.Config, path, newName, primary, replicaHost string) error {
	pc := *cfg
	if err := pc.SetInstance(primary); err != nil {
		return fmt.Errorf("loading primary instance: %w", err)
	}
	primaryPM, err := podman.New(&pc)
	if err != nil {
		return err
	}
	fmt.Println("Checking primary connectivity...")
	if err := primaryPM.CheckContainerRunning(); err != nil {
		return err
	}

	if err := primaryPM.EnsureReplicationHBA(replicaHost); err != nil {
		return fmt.Errorf("configuring replication on primary: %w", err)
	}
	slot := podman.ReplicaSlotName(newName)
	if err := primaryPM.EnsureReplicationSlot(slot); err != nil {
		return fmt.Errorf("creating replication slot on primary: %w", err)
	}

	fmt.Printf("✓ Primary prepared for replica %q\n", newName)
	fmt.Printf("  slot:      %s\n", slot)
	fmt.Printf("  hba:       host replication all %s\n", replicaHost)
	fmt.Println("  Next, on the replica host run:")
	fmt.Printf("    pg replica create %s -i %s --primary-dsn \"postgres://%s:<primary password>@<primary ip>:<port>/%s\"\n",
		newName, primary, pc.Postgres.User, pc.Postgres.Database)
	fmt.Println("  Port and password are the primary instance's PG port and admin password.")
	return nil
}

// runReplicaCreateRemote runs on the replica host: registers the replica and
// boots it, copying the primary data via pg_basebackup. The primary side must
// already have been prepared with --replica-host.
func runReplicaCreateRemote(cfg *config.Config, path, newName, primary, dsn string) error {
	_, _, user, password, database, err := podman.ParseDSN(dsn)
	if err != nil {
		return err
	}

	// Pre-check the primary side BEFORE registering anything, so a missing or
	// unprepared primary leaves no side effects (no config entry, no data).
	pm, err := podman.New(cfg)
	if err != nil {
		return err
	}
	fmt.Println("Checking primary replication slot...")
	if err := pm.CheckPrimarySlotReady(dsn, newName, primary); err != nil {
		return err
	}

	// Physical replication copies pg_authid: the replica's local password must
	// match the primary's, so user/database/password are taken from the DSN
	// (there is no local primary config to copy from).
	inst := cfg.InstanceDefaults(newName)
	inst.Postgres.User = user
	inst.Postgres.Database = database
	inst.Postgres.Password = password
	inst.PITR.Enabled = false
	inst.ReplicaOf = primary
	inst.PrimaryDSN = dsn
	cfg.Instances[newName] = *inst
	cfg.ApplyDefaults()
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("Created replica %q (primary: %s)\n", newName, primary)

	// startSingle/doStart persist config via the global cfgPath.
	cfgPath = path
	if err := startSingle(cfg, newName); err != nil {
		return fmt.Errorf("starting replica %q: %w", newName, err)
	}

	rc := *cfg
	if err := rc.SetInstance(newName); err != nil {
		return err
	}

	fmt.Printf("✓ Replica %q created: postgres://%s:%s@localhost:%d/%s\n",
		newName, rc.Postgres.User, rc.Postgres.Password, rc.Podman.HostPort, rc.Postgres.Database)
	fmt.Println("  Read-only; verify with: pg exec -i " + newName + " \"SELECT pg_is_in_recovery()\"")
	fmt.Printf("  To destroy: pg destroy -i %s; then on the primary host: pg replica drop %s -i %s\n",
		newName, newName, primary)
	return nil
}

// runReplicaDrop removes the replication slot for a cross-network replica
// from the primary (run on the primary host).
func runReplicaDrop(replicaName string) error {
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

	primary := cfgInstance
	if _, ok := cfg.Instances[primary]; !ok {
		return fmt.Errorf("primary instance %q not found in config", primary)
	}

	pc := *cfg
	if err := pc.SetInstance(primary); err != nil {
		return fmt.Errorf("loading primary instance: %w", err)
	}
	primaryPM, err := podman.New(&pc)
	if err != nil {
		return err
	}
	if err := primaryPM.CheckContainerRunning(); err != nil {
		fmt.Printf("  [!]  Warning: primary %q is not running; slot %q left for manual cleanup\n",
			primary, podman.ReplicaSlotName(replicaName))
		return nil
	}

	slot := podman.ReplicaSlotName(replicaName)
	if err := primaryPM.DropReplicationSlot(slot); err != nil {
		return fmt.Errorf("dropping replication slot: %w", err)
	}
	fmt.Printf("  [OK] replication slot %q removed from primary %q\n", slot, primary)
	return nil
}

func runReplicaList() error {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	var replicas []string
	for name, inst := range cfg.Instances {
		if inst.ReplicaOf != "" {
			replicas = append(replicas, name)
		}
	}
	sort.Strings(replicas)

	if len(replicas) == 0 {
		fmt.Println("No replica instances configured")
		return nil
	}

	fmt.Printf("%-18s %-16s %-10s %s\n", "NAME", "PRIMARY", "STATUS", "LAG")
	for _, name := range replicas {
		primary := cfg.Instances[name].ReplicaOf
		sc := *cfg
		if err := sc.SetInstance(name); err != nil {
			fmt.Printf("%-18s %-16s %-10s %s\n", name, primary, "error", "-")
			continue
		}
		pm, err := podman.New(&sc)
		if err != nil {
			continue
		}
		cs, err := pm.Status()
		if err != nil || !cs.Running {
			status := "not running"
			if cs != nil && cs.Status != "" {
				status = cs.Status
			}
			fmt.Printf("%-18s %-16s %-10s %s\n", name, primary, status, "-")
			continue
		}

		// Query from the standby itself: is it in recovery, and how far
		// behind is replay.
		out, err := pm.Exec("psql", "-U", sc.Postgres.User, "-d", sc.Postgres.Database,
			"-t", "-A", "-F", "|", "-c",
			"SELECT pg_is_in_recovery(), COALESCE(now() - pg_last_xact_replay_timestamp(), interval '0')")
		if err != nil {
			fmt.Printf("%-18s %-16s %-10s %s\n", name, primary, "running", "-")
			continue
		}
		parts := strings.Split(strings.TrimSpace(out), "|")
		status := "standby"
		if len(parts) > 0 && strings.TrimSpace(parts[0]) == "f" {
			status = "primary (promoted)"
		}
		lag := "-"
		if len(parts) > 1 {
			l := strings.TrimSpace(parts[1])
			if l != "" && l != "00:00:00" {
				lag = l
			}
		}
		fmt.Printf("%-18s %-16s %-10s %s\n", name, primary, status, lag)
	}
	return nil
}

// runReplicaPromote promotes a replica to a standalone read-write primary.
// This is step 1 of a 3-step failover — it only handles the promoted replica
// itself. Slot cleanup on the old primary (step 2) and re-pointing other
// replicas (step 3) are separate commands run on their respective hosts.
func runReplicaPromote(replicaName string) error {
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

	inst, ok := cfg.Instances[replicaName]
	if !ok {
		return fmt.Errorf("instance %q not found in config", replicaName)
	}
	if inst.ReplicaOf == "" {
		return fmt.Errorf("instance %q is not a replica (no replica_of set)", replicaName)
	}
	oldPrimary := inst.ReplicaOf

	rc := *cfg
	if err := rc.SetInstance(replicaName); err != nil {
		return fmt.Errorf("loading replica instance config: %w", err)
	}
	pm, err := podman.New(&rc)
	if err != nil {
		return err
	}

	fmt.Println("Checking replica status...")
	if err := pm.CheckContainerRunning(); err != nil {
		return err
	}

	// Idempotent: if already promoted (pg_is_in_recovery=false) but config
	// still says replica, skip pg_promote() and proceed to config cleanup.
	recOut, recErr := pm.Exec("psql", "-U", rc.Postgres.User, "-d", rc.Postgres.Database,
		"-tAc", "SELECT pg_is_in_recovery()")
	alreadyPromoted := recErr == nil && strings.TrimSpace(recOut) == "f"

	if !alreadyPromoted {
		fmt.Printf("Promoting replica %q to primary...\n", replicaName)
		if err := pm.Promote(); err != nil {
			return fmt.Errorf("promote failed: %w", err)
		}
	} else {
		fmt.Println("Instance is already promoted (completing config cleanup)...")
	}

	// Update config: clear ReplicaOf/PrimaryDSN, re-enable PITR
	inst.ReplicaOf = ""
	inst.PrimaryDSN = ""
	inst.PITR.Enabled = true
	if inst.PITR.PgBackRestStanza == "" {
		inst.PITR.PgBackRestStanza = "pgcli_" + replicaName
	}
	cfg.Instances[replicaName] = inst
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("promote succeeded but failed to save config: %w -- manually clear replica_of and primary_dsn for instance %q", err, replicaName)
	}
	fmt.Println("  [OK] config updated (ReplicaOf/PrimaryDSN cleared, PITR re-enabled)")

	// Auto-initialize PITR: create stanza, enable archive_mode, restart container
	fmt.Println("-> Initializing PITR (backup stanza + archive_mode)...")
	cfgPath = path
	if err := startSingle(cfg, replicaName); err != nil {
		fmt.Printf("  [!] Warning: PITR initialization failed: %v\n", err)
		fmt.Printf("      Run manually: pg start -i %s\n", replicaName)
	}

	fmt.Println()
	fmt.Printf("✓ Replica %q promoted to primary\n", replicaName)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Printf("  1. pg replica drop %s -i %s\n", replicaName, oldPrimary)
	fmt.Println("     Clean up the replication slot on the old primary host.")
	fmt.Println()
	fmt.Println("  2. pg replica repoint <other-replica> --primary-dsn <dsn> --primary-name " + replicaName)
	fmt.Println("     Re-point each remaining replica on its own host.")

	return nil
}

// runReplicaRepoint re-points a replica to stream from a new primary.
// This is step 3 of a 3-step failover — run on each remaining replica host.
//
// After a promotion, other replicas may have diverged timelines and cannot
// simply be re-pointed by changing primary_conninfo. The safest approach is
// to destroy the old data and re-initialize via pg_basebackup from the new
// primary. The replica's config entry (port, password, etc.) is preserved.
func runReplicaRepoint(replicaName, primaryDSN, primaryName string) error {
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

	inst, ok := cfg.Instances[replicaName]
	if !ok {
		return fmt.Errorf("instance %q not found in config", replicaName)
	}
	if inst.ReplicaOf == "" {
		return fmt.Errorf("instance %q is not a replica (no replica_of set)", replicaName)
	}

	_, _, user, password, _, err := podman.ParseDSN(primaryDSN)
	if err != nil {
		return fmt.Errorf("invalid --primary-dsn: %w", err)
	}

	rc := *cfg
	if err := rc.SetInstance(replicaName); err != nil {
		return fmt.Errorf("loading replica instance config: %w", err)
	}
	pm, err := podman.New(&rc)
	if err != nil {
		return err
	}

	// Stop and remove the old replica container + data
	fmt.Printf("-> Stopping replica %q...\n", replicaName)
	_ = pm.StopContainer()
	fmt.Println("-> Destroying old replica data (will re-initialize from new primary)...")
	if err := pm.DestroyWithData(true); err != nil {
		return fmt.Errorf("destroying old replica data: %w", err)
	}

	// Create replication slot on the new primary (via DSN)
	slot := podman.ReplicaSlotName(replicaName)
	esc := strings.ReplaceAll(slot, "'", "''")
	_, slotErr := pm.ExecDSNQuery(primaryDSN,
		fmt.Sprintf("SELECT pg_create_physical_replication_slot('%s')", esc))
	if slotErr != nil {
		if !strings.Contains(slotErr.Error(), "already exists") {
			return fmt.Errorf("creating replication slot on new primary: %w", slotErr)
		}
		fmt.Printf("  [OK] replication slot %q already exists on new primary\n", slot)
	} else {
		fmt.Printf("  [OK] replication slot %q created on new primary\n", slot)
	}

	// Update config: point to new primary
	inst.ReplicaOf = primaryName
	inst.PrimaryDSN = primaryDSN
	inst.Postgres.User = user
	inst.Postgres.Password = password
	inst.PITR.Enabled = false
	cfg.Instances[replicaName] = inst
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("  [OK] config updated (ReplicaOf = %q)\n", primaryName)

	// Re-initialize via startSingle → EnsureReplica → pg_basebackup
	fmt.Printf("-> Re-initializing replica %q from new primary...\n", replicaName)
	cfgPath = path
	if err := startSingle(cfg, replicaName); err != nil {
		return fmt.Errorf("re-initializing replica: %w", err)
	}

	fmt.Println()
	fmt.Printf("✓ Replica %q re-pointed to %q\n", replicaName, primaryName)
	return nil
}
