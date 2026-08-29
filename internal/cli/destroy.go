package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

var (
	destroyForce     bool
	destroyCleanData bool
)

func init() {
	rootCmd.AddCommand(destroyCmd)
	destroyCmd.Flags().BoolVar(&destroyForce, "force", false, "Skip confirmation prompt")
	destroyCmd.Flags().BoolVar(&destroyCleanData, "clean-data", false, "Also remove host data, WAL and backup repo stanza")
}

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Destroy an instance and remove its configuration",
	Long: `destroy stops and removes the container, then removes the
instance's configuration entry.

By default host data directories are preserved. Use --clean-data to also
delete the data directory and the instance's pgBackRest stanza from the
shared backup repo.

Use --force to skip the confirmation prompt.

Examples:
  pg destroy -i proj01
  pg destroy -i proj01 --force
  pg destroy -i proj01 --clean-data --force`,
	RunE: func(cmd *cobra.Command, args []string) error {
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

		inst, ok := cfg.Instances[cfgInstance]
		if !ok {
			return fmt.Errorf("instance %q not found in config", cfgInstance)
		}

		// Merge instance config for container operations
		if err := cfg.SetInstance(cfgInstance); err != nil {
			return fmt.Errorf("loading instance config: %w", err)
		}

		pm, err := podman.New(cfg)
		if err != nil {
			return fmt.Errorf("podman: %w", err)
		}

		if !destroyForce {
			fmt.Printf("!  This will destroy instance %q:\n", cfgInstance)
			fmt.Printf("  - Stop and remove container: %s\n", inst.Podman.ContainerName)
			fmt.Printf("  - Remove config entry\n")
			if destroyCleanData {
				fmt.Printf("\n  [!]  Host data will be PERMANENTLY deleted:\n")
				fmt.Printf("    data:       %s\n", inst.Podman.DataDir)
				if inst.PITR.Enabled {
					fmt.Printf("    backup:     %s/backup/%s\n", cfg.Backup.DataDir, inst.PITR.PgBackRestStanza)
					fmt.Printf("    archive:    %s/archive/%s\n", cfg.Backup.DataDir, inst.PITR.PgBackRestStanza)
				}
			} else {
				fmt.Printf("\n  Data directories preserved on host:\n")
				fmt.Printf("    data: %s\n", inst.Podman.DataDir)
			}
			fmt.Println()

			if !confirmPrompt("Confirm? [y/N]: ") {
				fmt.Println("Cancelled")
				return nil
			}
		}

		// 1. Destroy container (and data if requested). Must happen BEFORE the
		// slot drop: a live replica holds an active slot, and
		// pg_drop_replication_slot refuses active slots. Stopping the
		// container first closes the streaming connection so the drop succeeds.
		fmt.Printf("-> Stopping and removing container %s...\n", inst.Podman.ContainerName)
		if err := pm.DestroyWithData(destroyCleanData); err != nil {
			fmt.Printf("  [!]  Warning: failed to destroy container: %v\n", err)
		}

		// 2. Replicas: drop the physical replication slot on the primary so
		// WAL is not held forever on its behalf (best-effort — a stopped
		// primary just leaves the slot for manual cleanup).
		if primary := cfg.ReplicaOf(cfgInstance); primary != "" {
			pc := *cfg
			if err := pc.SetInstance(primary); err == nil {
				ppm, perr := podman.New(&pc)
				if perr == nil && ppm.CheckContainerRunning() == nil {
					if err := ppm.DropReplicaSlot(cfgInstance); err != nil {
						fmt.Printf("  [!]  Warning: dropping replication slot on primary %q: %v\n", primary, err)
					} else {
						fmt.Printf("  [OK] replication slot for replica %q removed from primary %q\n", cfgInstance, primary)
					}
				}
			}
		}

		// 3. Remove config entry
		delete(cfg.Instances, cfgInstance)
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		// 4. Rebuild or remove backup container depending on remaining instances.
		if cfg.PITR.Enabled {
			bm, err := podman.NewBackupManager(cfg)
			if err != nil {
				fmt.Printf("  [!]  Warning: cannot rebuild backup container: %v\n", err)
			} else if len(cfg.Instances) == 0 {
				// No instances left — stop and remove the shared backup
				// container.
				if err := bm.Destroy(); err != nil {
					fmt.Printf("  [!]  Warning: failed to remove backup container: %v\n", err)
				} else {
					fmt.Println("  [OK] backup container removed (no instances remaining)")
				}
				// With --clean-data remove every file the container left
				// behind (repo, logs, credentials). Without it keep the
				// backup data so a rebuild can restore from the existing
				// repo; only drop the config and credential files.
				if destroyCleanData {
					if err := bm.RemoveHostData(); err != nil {
						fmt.Printf("  [!]  Warning: cleaning up backup host data: %v\n", err)
					}
				} else if err := bm.RemoveHostConfig(); err != nil {
					fmt.Printf("  [!]  Warning: cleaning up backup host config: %v\n", err)
				}
			} else {
				// Ensure SSH key exists before rebuilding backup container
				if _, err := bm.EnsureSSHKey(); err != nil {
					fmt.Printf("  [!]  Warning: cannot ensure backup SSH key: %v\n", err)
				} else if err := bm.EnsureBackupInfra(); err != nil {
					fmt.Printf("  [!]  Warning: failed to update backup container: %v\n", err)
				}
			}
		}

		fmt.Printf("[OK] instance %q destroyed\n", cfgInstance)
		return nil
	},
}
