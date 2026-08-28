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

Commands:
  pg replica create <name> -i <primary>   create a read-only replica
  pg replica list                          list replicas and replication lag`,
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

Examples:
  pg replica create ro1 -i proj01
  pg replica create ro2                # replicate the default instance`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReplicaCreate(args[0])
	},
}

var replicaListCmd = &cobra.Command{
	Use:   "list",
	Short: "List replica instances and replication lag",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReplicaList()
	},
}

func init() {
	rootCmd.AddCommand(replicaCmd)
	replicaCmd.AddCommand(replicaCreateCmd, replicaListCmd)
}

func runReplicaCreate(newName string) error {
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

	// Primary must exist in config.
	primary := cfgInstance
	if _, ok := cfg.Instances[primary]; !ok {
		return fmt.Errorf("primary instance %q not found in config", primary)
	}

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
	inst := cfg.InstanceDefaults(newName)
	inst.Postgres.Database = pc.Postgres.Database
	inst.Postgres.Password = pc.Postgres.Password
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
