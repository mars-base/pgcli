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
}

var stopAll bool

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop pg services",
	Long: `stop terminates the PostgreSQL container and associated services.

By default, stops only the current instance (specified by -i).
Use --all to stop all instances, the backup container, and PgBouncer services.

Note: This does not affect auto-start configuration. Instances with
autostart enabled will still start automatically at boot.`,
	RunE: func(cmd *cobra.Command, args []string) error {
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

	pm, err := newPodman()
	if err != nil {
		return err
	}

	fmt.Println("-> Stopping pg services...")
	if err := pm.StopContainer(); err != nil {
		return err
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

		if err := pm.StopContainer(); err != nil {
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
