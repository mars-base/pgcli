package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/podman"
)

func init() {
	rootCmd.AddCommand(stopCmd)
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "stop all configured instances, backup container, and pgbouncer")
	stopCmd.Flags().BoolVarP(&stopForce, "force", "f", false, "force stop: SIGKILL without a graceful drain, and clear a container wedged in a stale stopping state")
}

var (
	stopAll   bool
	stopForce bool
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Args:  cobra.MaximumNArgs(1),
	Short: "Stop pg services",
	Long: `stop terminates the PostgreSQL container and associated services.

By default, stops only the current instance (specified by -i).
Use --all to stop all instances, the backup container, and PgBouncer services.
Use -f/--force to SIGKILL immediately without a graceful drain, and to clear a
container stuck in a stale "stopping" state.

Note: This does not affect auto-start configuration. Instances with
autostart enabled will still start automatically at boot.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 && args[0] != "default" {
			return fmt.Errorf("unexpected argument %q: to stop a named instance use 'pg stop -i %s'", args[0], args[0])
		}
		if stopAll {
			return stopAllInstances()
		}
		return stopInstance()
	},
}

func stopInstance() error {
	if err := loadConfig(); err != nil {
		return err
	}

	// NewForStop (not New): stopping must never trigger the post-reboot
	// container recovery, which would otherwise resurrect other stopped
	// containers the user did not ask to start.
	pm, err := podman.NewForStop(cfg)
	if err != nil {
		return err
	}

	if stopForce {
		fmt.Println("-> Force-stopping pg services...")
		if err := pm.KillContainer(); err != nil {
			return err
		}
	} else {
		fmt.Println("-> Stopping pg services...")
		if err := pm.StopContainer(); err != nil {
			return err
		}
	}

	fmt.Println("[OK] pg stopped")
	return nil
}

func stopAllInstances() error {
	if err := loadConfig(); err != nil {
		return err
	}

	if len(cfg.Instances) == 0 {
		return fmt.Errorf("no instances configured")
	}

	var firstErr error
	ok := 0

	// Collect instance names (sorted for deterministic order).
	names := make([]string, 0, len(cfg.Instances))
	for name := range cfg.Instances {
		names = append(names, name)
	}
	sort.Strings(names)

	// Stop all instances
	for _, name := range names {
		fmt.Printf("\n>>> stopping instance %q <<<\n", name)
		cfgInstance = name
		if err := cfg.SetInstance(name); err != nil {
			fmt.Printf("  [X] %s: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		pm, err := podman.NewForStop(cfg)
		if err != nil {
			fmt.Printf("  [X] %s: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		stopFn := pm.StopContainer
		if stopForce {
			stopFn = pm.KillContainer
		}
		if err := stopFn(); err != nil {
			if strings.Contains(err.Error(), "no such container") {
				fmt.Printf("  [OK] %s: container not running (does not exist)\n", name)
				ok++
				continue
			}
			fmt.Printf("  [X] %s: %v\n", name, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		fmt.Printf("  [OK] %s stopped\n", name)
		ok++
	}
	fmt.Printf("\n>>> stopped %d/%d instances <<<\n", ok, len(cfg.Instances))

	// Stop backup container if running
	fmt.Println("\n-> Stopping backup container...")
	if bm, err := newBackupManager(); err == nil {
		if err := bm.StopBackupContainer(); err != nil {
			fmt.Printf("  [!] backup container: %v\n", err)
		} else {
			fmt.Println("  [OK] backup container stopped")
		}
	}

	// Stop PgBouncer containers
	fmt.Println("\n-> Stopping PgBouncer containers...")
	pgbStopped := 0
	if pbm, err := podman.NewPgBouncerManager(cfg); err == nil {
		for _, name := range names {
			inst := cfg.Instances[name]
			if inst.Addons.PgBouncer != nil {
				containerName := inst.Addons.PgBouncer.ContainerName
				if running, _ := pbm.ContainerRunning(containerName); running {
					if _, err := pbm.Stop(containerName); err != nil {
						fmt.Printf("  [!] pgbouncer %s: %v\n", name, err)
					} else {
						fmt.Printf("  [OK] pgbouncer %s stopped\n", name)
						pgbStopped++
					}
				}
			}
		}
		for name, pb := range cfg.Addons.PgBouncer {
			containerName := pb.ContainerName
			if running, _ := pbm.ContainerRunning(containerName); running {
				if _, err := pbm.Stop(containerName); err != nil {
					fmt.Printf("  [!] pgbouncer %s (remote): %v\n", name, err)
				} else {
					fmt.Printf("  [OK] pgbouncer %s (remote) stopped\n", name)
					pgbStopped++
				}
			}
		}
	}
	if pgbStopped == 0 {
		fmt.Println("  (no running PgBouncer containers)")
	}

	return firstErr
}
