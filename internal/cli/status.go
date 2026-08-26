package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/pitr"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show pgcli running status",
	Long:  `status shows PostgreSQL container status, PG health check, and recent backup info.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfig(); err != nil {
			return err
		}

		pm, err := newPodman()
		if err != nil {
			return err
		}
		bm, err := newBackupManager()
		if err != nil {
			return err
		}

		fmt.Println("=== pgcli status ===")
		fmt.Printf("Instance: %s\n", cfg.Instance)

		// Container status
		cs, err := pm.Status()
		if err != nil {
			return err
		}
		fmt.Printf("\nContainer: %s\n", cs.Name)
		fmt.Printf("  Status: %s\n", cs.Status)
		if cs.Ports != "" {
			fmt.Printf("  Ports: %s\n", cs.Ports)
		}

		// PG health check
		if cs.Running {
			ready, _ := pm.PGIsReady()
			if ready {
				fmt.Println("\nPostgreSQL: [OK] accepting connections")
				fmt.Printf("  Connection: %s\n", cfg.GetPostgresURL())
			} else {
				fmt.Println("\nPostgreSQL: [X] not accepting connections")
			}
		}

		// Backup info (when PITR enabled)
		if cfg.PITR.Enabled && cs.Running {
			pt := pitr.New(cfg, pm, bm)

			type backupResult struct {
				snapshots []pitr.Snapshot
				err       error
			}
			ch := make(chan backupResult, 1)
			go func() {
				snapshots, err := pt.ListSnapshots(5)
				ch <- backupResult{snapshots, err}
			}()

			select {
			case r := <-ch:
				if r.err == nil && len(r.snapshots) > 0 {
					fmt.Println("\nRecent backups(UTC):")
					for _, s := range r.snapshots {
						stopStr := ""
						if !s.StopTime.IsZero() {
							stopStr = " → " + s.StopTime.Format("2006-01-02 15:04:05")
						}
						fmt.Printf("  %s%s  %s  %s\n",
							s.Timestamp.Format("2006-01-02 15:04:05"),
							stopStr,
							s.Name, s.Type)
					}
				} else {
					fmt.Println("\nRecent backups: (none)")
				}
			case <-time.After(5 * time.Second):
				fmt.Println("\nRecent backups: (pgbackrest not responding)")
			}
		}

		fmt.Println()
		return nil
	},
}
