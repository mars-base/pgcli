package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/pitr"
	"github.com/mars-base/pgcli/internal/podman"
)

// pg ha snapshot wraps pgBackRest backup/info/expire for a Patroni CLUSTER
// (scope). Unlike `pg snapshot` (single-instance, cfg.PITR stanza), the target
// here is the cluster-wide stanza pgcli_<nsScope>: it lists every member as a
// pg*-host so pgBackRest locates the primary itself and survives failovers —
// so these commands never resolve a leader, they just name the stanza and run
// inside the shared backup container (which SSHes to the primary).
//
// Preflight mirrors what a cluster backup needs but stays cheap: it re-renders
// the backup-container pgbackrest.conf from the current topology (bind-mounted,
// so no container recreate). Cross-host SSH trust + repo CA are owned by
// `pg backup setup` / `pg ha remote`, not re-derived here.

func init() {
	haCmd.AddCommand(haSnapshotCmd)
	haSnapshotCmd.AddCommand(haSnapshotCreateCmd)
	haSnapshotCmd.AddCommand(haSnapshotListCmd)
	haSnapshotCmd.AddCommand(haSnapshotDeleteCmd)

	haSnapshotCreateCmd.Flags().StringVar(&haSnapType, "type", "full", "Backup type: full, incr, diff")
	haSnapshotCreateCmd.Flags().BoolVar(&haSnapTailLogs, "tail-logs", false, "Stream backup container logs to stdout during snapshot")
	haSnapshotListCmd.Flags().IntVar(&haSnapLimit, "limit", 0, "Limit display count (0=all)")
	haSnapshotDeleteCmd.Flags().BoolVar(&haSnapForce, "force", false, "Skip the interactive confirmation")
}

var haSnapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "pgBackRest snapshots for a Patroni cluster (create/list/delete)",
	Long: `Manage pgBackRest backups of a Patroni cluster (scope), run from the shared
backup container against the current primary.

The target is the cluster-wide stanza (pgcli_<scope>), so a backup follows the
leader across failovers with no config change. Run ` + "`pg backup setup`" + ` once
first to provision the backup container, stanza, and cross-host trust.

Examples:
  pg ha snapshot create app                     # full backup
  pg ha snapshot create app --type incr --tail-logs
  pg ha snapshot list app
  pg ha snapshot delete app 20260614-143005F`,
}

var (
	haSnapType     string
	haSnapTailLogs bool
	haSnapLimit    int
	haSnapForce    bool
)

// resolveScopeStanza validates the scope, returns its cluster-wide stanza, and
// best-effort refreshes the backup-container pgbackrest.conf so the stanza
// reflects the current member topology. An empty stanza means the scope has no
// members yet (nothing to back up).
func resolveScopeStanza(scope string) (bm *podman.BackupManager, stanza string, err error) {
	if err := loadConfigForDSN(); err != nil {
		return nil, "", err
	}
	cluster, err := lookupHACluster(scope)
	if err != nil {
		return nil, "", err
	}
	bm, err = newBackupManager()
	if err != nil {
		return nil, "", err
	}
	if len(cluster.Members) == 0 {
		return bm, "", nil
	}
	stanza = podman.PatroniStanzaForScope(cfg, scope)
	if _, err := bm.WritePgbackrestConf(); err != nil {
		fmt.Printf("  [!] could not refresh pgbackrest.conf (using existing): %v\n", err)
	}
	return bm, stanza, nil
}

var haSnapshotCreateCmd = &cobra.Command{
	Use:   "create <scope>",
	Short: "Create a snapshot of a Patroni cluster",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bm, stanza, err := resolveScopeStanza(args[0])
		if err != nil {
			return err
		}
		if stanza == "" {
			return fmt.Errorf("scope %q has no members to back up", args[0])
		}

		fmt.Println("-> Note: database backups may take a long time, do not interrupt the task")
		fmt.Printf("-> Creating %s backup of cluster %q...\n", haSnapType, args[0])
		out, err := bm.BackupExec(haSnapTailLogs,
			"pgbackrest", "--stanza="+stanza, "backup",
			"--type="+haSnapType, "--log-level-console=info")
		if err != nil {
			_ = bm.EnsureRepoReadable()
			return fmt.Errorf("creating backup: %w\n%s", err, out)
		}

		label := pitr.ExtractLabel(out)
		fmt.Printf("\nOK Snapshot created successfully\n")
		fmt.Printf("  Cluster: %s\n", args[0])
		fmt.Printf("  Name:    %s\n", label)
		fmt.Printf("  Type:    %s\n", haSnapType)
		fmt.Printf("  Time:    %s\n", time.Now().Format("2006-01-02 15:04:05"))
		return nil
	},
}

var haSnapshotListCmd = &cobra.Command{
	Use:     "list <scope>",
	Aliases: []string{"ls"},
	Short:   "List snapshots of a Patroni cluster",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bm, stanza, err := resolveScopeStanza(args[0])
		if err != nil {
			return err
		}
		if stanza == "" {
			fmt.Println("(no snapshots)")
			return nil
		}

		out, err := bm.BackupExec(false,
			"pgbackrest", "--stanza="+stanza, "info", "--log-level-console=info")
		if err != nil {
			return fmt.Errorf("listing backups: %w\n%s", err, out)
		}

		snapshots := pitr.ParseInfoOutput(out)
		if haSnapLimit > 0 && haSnapLimit < len(snapshots) {
			snapshots = snapshots[:haSnapLimit]
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
				s.Timestamp.Format("2006-01-02 15:04:05"), stopStr, s.Name, s.Type)
		}
		return nil
	},
}

var haSnapshotDeleteCmd = &cobra.Command{
	Use:     "delete <scope> <snapshot-name>",
	Aliases: []string{"rm"},
	Short:   "Delete a snapshot of a Patroni cluster",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, name := args[0], args[1]
		bm, stanza, err := resolveScopeStanza(scope)
		if err != nil {
			return err
		}
		if stanza == "" {
			return fmt.Errorf("scope %q has no members", scope)
		}

		out, err := bm.BackupExec(false,
			"pgbackrest", "--stanza="+stanza, "info", "--log-level-console=info")
		if err != nil {
			return fmt.Errorf("checking snapshots: %w\n%s", err, out)
		}
		snapshots := pitr.ParseInfoOutput(out)
		var target *pitr.Snapshot
		fullCount := 0
		for i := range snapshots {
			if snapshots[i].Type == "full" {
				fullCount++
			}
			if snapshots[i].Name == name {
				target = &snapshots[i]
			}
		}
		if target == nil {
			return fmt.Errorf("snapshot %s not found in cluster %q", name, scope)
		}
		if target.Type == "full" && fullCount == 1 {
			return fmt.Errorf("cannot delete %s: it is the only full backup; create another full backup first", name)
		}

		if !haSnapForce && !confirmPrompt(fmt.Sprintf("Delete snapshot %s from cluster %q? [y/N] ", name, scope)) {
			fmt.Println("Aborted.")
			return nil
		}

		out, err = bm.BackupExec(false,
			"pgbackrest", "--stanza="+stanza, "expire",
			"--set="+name, "--log-level-console=info")
		if err != nil {
			return fmt.Errorf("deleting backup %s: %w\n%s", name, err, out)
		}
		fmt.Printf("[OK] Snapshot %s deleted from cluster %q\n", name, scope)
		return nil
	},
}
