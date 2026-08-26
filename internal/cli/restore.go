package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/pitr"
)

var (
	restoreTime     string
	restoreDryRun   bool
	restoreForce    bool
	restorePromote  bool
	restoreTailLogs bool
)

func init() {
	rootCmd.AddCommand(restoreCmd)
	restoreCmd.Flags().StringVar(&restoreTime, "time", "", "Restore to specified time (e.g. '2026-06-14 15:04:05+00' or '2026-06-14 15:04:05')")
	restoreCmd.Flags().BoolVar(&restoreDryRun, "dry-run", false, "Only show what would be done, do not execute")
	restoreCmd.Flags().BoolVar(&restoreForce, "force", false, "Skip confirmation prompt")
	restoreCmd.Flags().BoolVar(&restorePromote, "promote", false, "Promote the cluster to read-write after recovery (switches timeline). By default the cluster is left paused at the target time in read-only state so you can inspect the data and restore again to a different point.")
	restoreCmd.Flags().BoolVar(&restoreTailLogs, "tail-logs", false, "Stream restore container logs to stdout during recovery")
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "PITR point-in-time recovery",
	Long: `restore rolls back the PostgreSQL database to a specified point in time (PITR).

WARNING: All changes after the target time will be permanently lost!

By default the cluster is recovered to the target time and then PAUSED in a
read-only state (recovery_target_action=pause). This lets you inspect the data
at that point in time. If it is not what you expect, run restore again with a
different --time -- no timeline switch happens, so the WAL archive stays intact
and repeated time-travel remains possible.

Once the restored state is confirmed correct, use --promote to make the cluster
read-write. Promote switches the cluster to a new timeline; further PITR to
points after the last backup then requires a new snapshot first.

Process:
  1. Stop PostgreSQL
  2. pgBackRest restore --type=time --target=<time> --target-action=pause|promote
  3. Start PostgreSQL

Examples:
  pg restore --time "2026-06-14 15:04:05+00"
  pg restore -i myinst --time "2026-06-14 15:04:05+00"
  pg restore --time "2026-06-14 15:04:05+00" --dry-run
  pg restore --time "2026-06-14 15:04:05+00" --tail-logs
  pg restore --time "2026-06-14 15:04:05+00" --promote
  pg restore --time "2026-06-14 15:04:05+00" --force`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := loadConfig(); err != nil {
			return err
		}

		if restoreTime == "" {
			return fmt.Errorf("Please specify restore time: --time \"2026-06-14 15:04:05\"")
		}

		var targetTime time.Time
		var err error
		for _, layout := range []string{
			"2006-01-02 15:04:05-07:00",
			"2006-01-02 15:04:05-0700",
			"2006-01-02 15:04:05-07",
			"2006-01-02 15:04:05Z07:00",
			"2006-01-02 15:04:05Z0700",
			"2006-01-02 15:04:05",
		} {
			targetTime, err = time.Parse(layout, restoreTime)
			if err == nil {
				break
			}
		}
		if err != nil {
			return fmt.Errorf("Invalid time format: %w (use: YYYY-MM-DD HH:MM:SS+00 or YYYY-MM-DD HH:MM:SS)", err)
		}

		// If no timezone was parsed, treat as UTC.
		if targetTime.Location() == time.UTC && !strings.Contains(restoreTime, "+") && !strings.Contains(restoreTime, "Z") {
			targetTime = time.Date(targetTime.Year(), targetTime.Month(), targetTime.Day(),
				targetTime.Hour(), targetTime.Minute(), targetTime.Second(), 0, time.UTC)
		}

		// Validate target time against the latest backup stop time.
		// pgBackRest requires target >= stop time; if earlier, it cannot find a
		// usable backup set and will fail with error [075].
		{
			bm0, err0 := newBackupManager()
			if err0 == nil {
				pm0, err0 := newPodman()
				if err0 == nil {
					pt0 := pitr.New(cfg, pm0, bm0)
					if snaps, err0 := pt0.ListSnapshots(1); err0 == nil && len(snaps) > 0 {
						latest := snaps[0]
						if !latest.StopTime.IsZero() && targetTime.Before(latest.StopTime) {
							return fmt.Errorf(
								"target time %s is before the latest backup stop time %s\n"+
									"  The earliest usable restore point is: %s\n"+
									"  Hint: use --time \"%s\" or later",
								targetTime.UTC().Format("2006-01-02 15:04:05"),
								latest.StopTime.UTC().Format("2006-01-02 15:04:05"),
								latest.StopTime.UTC().Format("2006-01-02 15:04:05"),
								latest.StopTime.UTC().Format("2006-01-02 15:04:05"),
							)
						}
					}
				}
			}
		}

		// Validate target time against WAL archive progress. If the target is
		// ahead of the last successfully archived WAL segment, pgBackRest
		// restore may "succeed" (it only copies the base backup + writes
		// recovery config) while PostgreSQL then fails to replay to the
		// requested point and crash-loops under the container's --restart
		// policy. Catch this before touching any container state.
		{
			pm1, err1 := newPodman()
			if err1 == nil {
				if lastArchived, err1 := pm1.PGLastArchivedTime(); err1 == nil && !lastArchived.IsZero() {
					if targetTime.After(lastArchived) {
						return fmt.Errorf(
							"target time %s is beyond the last archived WAL (%s)\n"+
								"  PostgreSQL cannot replay past this point yet -- restoring now would\n"+
								"  fail during recovery and the container may repeatedly restart.\n"+
								"  Hint: use --time \"%s\" or earlier, or wait for WAL archiving to catch up",
							targetTime.UTC().Format("2006-01-02 15:04:05"),
							lastArchived.UTC().Format("2006-01-02 15:04:05"),
							lastArchived.UTC().Format("2006-01-02 15:04:05"),
						)
					}
				}
			}
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

		// dry-run mode
		if restoreDryRun {
			return pt.Restore(targetTime, restorePromote, true, restoreTailLogs)
		}

		// Confirmation (unless --force)
		if !restoreForce {
			fmt.Printf("!  Confirm restore operation\n")
			fmt.Printf("  Instance:    %s\n", cfg.Instance)
			fmt.Printf("  Target time: %s\n", targetTime.Format("2006-01-02 15:04:05"))
			if restorePromote {
				fmt.Printf("  Action:       promote (cluster becomes read-write, timeline switches)\n")
			} else {
				fmt.Printf("  Action:       pause (cluster stays read-only at target time; restore again to adjust)\n")
			}
			fmt.Printf("  This will restore the database to that time point. All changes after it will be permanently lost!\n")
			fmt.Println()

			if !confirmPrompt("Confirm? [y/N]: ") {
				fmt.Println("Cancelled")
				return nil
			}
		}

		return pt.Restore(targetTime, restorePromote, false, restoreTailLogs)
	},
}
