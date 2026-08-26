package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/pitr"
)

func init() {
	rootCmd.AddCommand(snapshotCmd)
	snapshotCmd.AddCommand(snapshotCreateCmd)
	snapshotCmd.AddCommand(snapshotListCmd)
	snapshotCmd.AddCommand(snapshotDeleteCmd)
}

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Snapshot management (create/list/delete)",
	Long:  `snapshot manages pgBackRest backup snapshots. Supports full, incr, and diff backup types.`,
}

var (
	snapType     string
	snapLimit    int
	snapTailLogs bool
)

var snapshotCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new snapshot",
	Long: `Create a PostgreSQL backup snapshot.

Backup types:
  full  - Full backup (default)
  incr  - Incremental backup (changes since last backup)
  diff  - Differential backup (changes since last full backup)`,
	Example: `  pg snapshot create
  pg snapshot create --type incr`,
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

		// Ensure backup container host entries are up to date (PG container IPs may
		// have changed after stop/start/restore).
		if err := bm.EnsureBackupInfra(); err != nil {
			return err
		}

		// Re-authorize the backup SSH key on the PG container. The authorized_keys
		// file lives inside the container and is lost if the container is recreated,
		// so we ensure it is installed before every backup operation.
		if err := bm.AuthorizeKeyOnInstance(); err != nil {
			return fmt.Errorf("authorizing backup key: %w", err)
		}

		pt := pitr.New(cfg, pm, bm)

		fmt.Println("-> Note: database backups may take a long time, do not interrupt the task")
		snap, err := pt.CreateSnapshot(snapType, snapTailLogs)
		if err != nil {
			return err
		}

		fmt.Printf("\nOK Snapshot created successfully\n")
		fmt.Printf("  Name: %s\n", snap.Name)
		fmt.Printf("  Type: %s\n", snap.Type)
		fmt.Printf("  Time: %s\n", snap.Timestamp.Format("2006-01-02 15:04:05"))
		return nil
	},
}

var snapshotListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all snapshots",
	Example: `  pg snapshot list
  pg snapshot list --limit 10`,
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

		pt := pitr.New(cfg, pm, bm)

		snapshots, err := pt.ListSnapshots(snapLimit)
		if err != nil {
			return err
		}

		if len(snapshots) == 0 {
			fmt.Println("(no snapshots)")
			return nil
		}

		fmt.Printf("%-25s  %-25s  %-30s  %-10s\n", "Start Time", "Stop Time", "Name", "Type")
		fmt.Println("-----------------------------------------------------------------------------------------")
		for _, s := range snapshots {
			stopStr := ""
			if !s.StopTime.IsZero() {
				stopStr = s.StopTime.Format("2006-01-02 15:04:05")
			}
			fmt.Printf("%-25s  %-25s  %-30s  %-10s\n",
				s.Timestamp.Format("2006-01-02 15:04:05"),
				stopStr,
				s.Name, s.Type)
		}
		return nil
	},
}

var snapshotDeleteCmd = &cobra.Command{
	Use:     "delete <snapshot-name>",
	Aliases: []string{"rm"},
	Short:   "Delete a specific snapshot",
	Example: `  pg snapshot delete 20260614-143005F`,
	Args:    cobra.ExactArgs(1),
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

		pt := pitr.New(cfg, pm, bm)

		if err := pt.DeleteSnapshot(args[0]); err != nil {
			return err
		}

		fmt.Println("[OK] Snapshot deleted")
		return nil
	},
}

func init() {
	snapshotCreateCmd.Flags().StringVar(&snapType, "type", "full", "Backup type: full, incr, diff")
	snapshotCreateCmd.Flags().BoolVar(&snapTailLogs, "tail-logs", false, "Stream backup container logs to stdout during snapshot")

	snapshotListCmd.Flags().IntVar(&snapLimit, "limit", 0, "Limit display count (0=all)")
}
