package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/zfs"
)

var zfsSnapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Create and manage ZFS snapshots",
	Long:  `Create, list, roll back and destroy ZFS snapshots.`,
}

var zfsSnapshotCreateCmd = &cobra.Command{
	Use:   "create DATASET@SNAPSHOT",
	Short: "Create a snapshot",
	Long: `Create a snapshot of a dataset. Snapshots are cheap and should be created
before risky changes.

Examples:
  pg zfs snapshot create tank/data@before-upgrade
  pg zfs snapshot create tank/data@daily --recursive`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, _, err := zfs.SplitSnapshot(args[0]); err != nil {
			return err
		}
		recursive, _ := cmd.Flags().GetBool("recursive")
		ctx, r, zfsBin, err := zfsUserlandTools(cmd)
		if err != nil {
			return err
		}
		if err := r.RunPrivileged(ctx, zfsBin, zfs.SnapshotCreateArgs(args[0], recursive)...); err != nil {
			return err
		}
		fmt.Printf("Created snapshot %q.\n", args[0])
		return nil
	},
}

var zfsSnapshotListCmd = &cobra.Command{
	Use:   "list [DATASET]",
	Short: "List snapshots",
	Long: `List snapshots, optionally recursively below a dataset.

Examples:
  pg zfs snapshot list
  pg zfs snapshot list tank/data`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		parent := ""
		if len(args) == 1 {
			if err := zfs.ValidateDatasetName(args[0]); err != nil {
				return err
			}
			if strings.Contains(args[0], "@") {
				return fmt.Errorf("%q is a snapshot, not a dataset", args[0])
			}
			parent = args[0]
		}
		ctx, r, _, err := zfsUserlandTools(cmd)
		if err != nil {
			return err
		}
		rows, err := zfs.ListSnapshots(ctx, r, parent)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("No snapshots.")
			return nil
		}
		writer := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
		fmt.Fprintln(writer, "SNAPSHOT\tUSED\tREFER\tCREATED")
		for _, row := range rows {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
				row.Name,
				zfs.FormatBytes(row.Used),
				zfs.FormatBytes(row.Refer),
				time.Unix(row.Creation, 0).Local().Format("2006-01-02 15:04:05"),
			)
		}
		return writer.Flush()
	},
}

var zfsSnapshotRollbackCmd = &cobra.Command{
	Use:   "rollback DATASET@SNAPSHOT",
	Short: "Roll a dataset back to a snapshot",
	Long: `Discard all changes made after the snapshot. If clones depend on the
snapshot, ZFS refuses the rollback; remove the clones first (or use raw for -R).

Examples:
  pg zfs snapshot rollback tank/data@before-upgrade
  pg zfs snapshot rollback tank/data@before-upgrade --recursive --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, _, err := zfs.SplitSnapshot(args[0]); err != nil {
			return err
		}
		recursive, _ := cmd.Flags().GetBool("recursive")
		ctx, r, zfsBin, err := zfsUserlandTools(cmd)
		if err != nil {
			return err
		}
		fmt.Printf("Snapshot:    %s\n", args[0])
		if recursive {
			fmt.Println("Mode:        recursive")
		}
		fmt.Println("  [!] All changes after this snapshot are discarded.")
		if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Roll back to %q? [y/N] ", args[0])) {
			printZFSCancelled()
			return nil
		}
		if err := r.RunPrivileged(ctx, zfsBin, zfs.SnapshotRollbackArgs(args[0], recursive)...); err != nil {
			return err
		}
		fmt.Printf("Rolled back to snapshot %q.\n", args[0])
		return nil
	},
}

var zfsSnapshotDestroyCmd = &cobra.Command{
	Use:   "destroy DATASET@SNAPSHOT",
	Short: "Destroy a snapshot",
	Long: `Destroy a snapshot. This is permanent and requires confirmation; --force only
skips the prompt.

Examples:
  pg zfs snapshot destroy tank/data@before-upgrade
  pg zfs snapshot destroy tank/data@before-upgrade --recursive --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, _, err := zfs.SplitSnapshot(args[0]); err != nil {
			return err
		}
		recursive, _ := cmd.Flags().GetBool("recursive")
		ctx, r, zfsBin, err := zfsUserlandTools(cmd)
		if err != nil {
			return err
		}
		fmt.Printf("Snapshot:    %s\n", args[0])
		if recursive {
			fmt.Println("Mode:        recursive")
		}
		if !confirmZFS(zfsForce(cmd), fmt.Sprintf("Destroy snapshot %q? [y/N] ", args[0])) {
			printZFSCancelled()
			return nil
		}
		if err := r.RunPrivileged(ctx, zfsBin, zfs.SnapshotDestroyArgs(args[0], recursive)...); err != nil {
			return err
		}
		fmt.Printf("Destroyed snapshot %q.\n", args[0])
		return nil
	},
}

func init() {
	zfsSnapshotCreateCmd.Flags().BoolP("recursive", "r", false, "snapshot child datasets too")

	zfsSnapshotRollbackCmd.Flags().BoolP("recursive", "r", false, "roll back child datasets too")
	addZFSForceFlag(zfsSnapshotRollbackCmd)

	zfsSnapshotDestroyCmd.Flags().BoolP("recursive", "r", false, "destroy snapshots of child datasets too")
	addZFSForceFlag(zfsSnapshotDestroyCmd)

	zfsSnapshotCmd.AddCommand(
		zfsSnapshotCreateCmd,
		zfsSnapshotListCmd,
		zfsSnapshotRollbackCmd,
		zfsSnapshotDestroyCmd,
	)
	zfsCmd.AddCommand(zfsSnapshotCmd)
}
