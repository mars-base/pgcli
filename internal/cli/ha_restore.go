package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/pitr"
	"github.com/mars-base/pgcli/internal/podman"
)

// pg ha restore performs a Patroni CLUSTER point-in-time recovery, the
// cluster-side counterpart of the single-instance `pg restore`. It follows the
// Patroni custom-bootstrap recipe: clear the cluster's DCS identity, then have
// one LOCAL member re-bootstrap its (emptied) data dir from the pgBackRest repo
// at the target time. That member comes up on a new timeline and promotes
// itself to the writable leader; the other members rejoin it from the DCS.
//
// Two deliberate differences from single-instance restore (see the docs):
//   - it ALWAYS promotes (bootstrap recovery ends in a writable leader — there
//     is no read-only "pause, inspect, retry another time" two-step), and
//   - it must run on a host that owns at least one member of the scope, since
//     only a local member's patroni.yml + data dir + container can be rebuilt.
//
// The target is the cluster-wide stanza pgcli_<scope>, so recovery reaches S3
// from any member; a `pg backup setup` must have provisioned the repo + trust.

var (
	haRestoreTime     string
	haRestoreMember   string
	haRestoreDryRun   bool
	haRestoreForce    bool
	haRestoreTailLogs bool
)

func init() {
	haCmd.AddCommand(haRestoreCmd)
	haRestoreCmd.Flags().StringVar(&haRestoreTime, "time", "", `Restore to a point in time, in UTC (e.g. "2026-08-26 15:30:00+00"; a time without a timezone is assumed UTC, e.g. "2026-08-26 15:30:00")`)
	haRestoreCmd.Flags().StringVar(&haRestoreMember, "member", "", "local member to carry the bootstrap (default: first local member)")
	haRestoreCmd.Flags().BoolVar(&haRestoreDryRun, "dry-run", false, "Only show what would be done, do not execute")
	haRestoreCmd.Flags().BoolVar(&haRestoreForce, "force", false, "Skip the confirmation prompt")
	haRestoreCmd.Flags().BoolVar(&haRestoreTailLogs, "tail-logs", false, "Stream the bootstrap member's logs to stdout during recovery")
}

var haRestoreCmd = &cobra.Command{
	Use:   "restore <scope>",
	Short: "PITR: rebuild a Patroni cluster to a point in time",
	Long: `restore rebuilds a Patroni cluster (scope) to a point in time (PITR) from the
pgBackRest repository, mirroring the single-instance ` + "`pg restore`" + ` targets.

The cluster's DCS identity is removed and one local member re-bootstraps its data
directory from a pgBackRest restore at the target time. That member starts on a
NEW timeline and is promoted to the writable leader; the remaining members — this
host's and cross-host ones alike — rejoin it through the DCS (reinit, if a replica
does not come back on its own).

WARNING: this is destructive and irreversible:
  - the target time is a hard cutoff — all commits after it are permanently lost;
  - every local member's data directory is wiped and rebuilt;
  - unlike single-instance restore, the cluster comes up READ-WRITE immediately
    (bootstrap recovery always promotes). There is no read-only "pause, inspect,
    retry another time" step — dry-run the target first if unsure.

Run this on a host that owns at least one member of the scope. Requires a stanza
provisioned with WAL archiving (` + "`pg backup setup`" + ` with an S3 repo).

After the restore, take a fresh full snapshot
(` + "`pg ha snapshot create <scope> --type full`" + `) to re-baseline the new timeline.

Examples:
  pg ha restore app --time "2026-08-26 15:30:00+00" --dry-run
  pg ha restore app --time "2026-08-26 15:30:00+00" --tail-logs
  pg ha restore app --time "2026-08-26 15:30:00+00" --member node1 --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		scope := args[0]
		if haRestoreTime == "" {
			return fmt.Errorf(`specify a restore time: --time "2026-08-26 15:30:00+00"`)
		}
		targetTime, err := parseRestoreTime(haRestoreTime)
		if err != nil {
			return err
		}

		// Resolve scope → cluster-wide stanza + backup manager, and re-render the
		// backup-container pgbackrest.conf so it reflects current topology.
		bm, stanza, err := resolveScopeStanza(scope)
		if err != nil {
			return err
		}
		if stanza == "" {
			return fmt.Errorf("scope %q has no members — nothing to restore", scope)
		}

		cluster, err := lookupHACluster(scope)
		if err != nil {
			return err
		}

		// Pick the local member that carries the bootstrap. Remote members have no
		// local container / data dir to rebuild, so the pool is locals only.
		locals := localMembers(cluster)
		if len(locals) == 0 {
			return fmt.Errorf("scope %q has no local member on this host — run pg ha restore on a host that owns a member (this host's view: %s)",
				scope, patroniMemberNames(*cluster))
		}
		boot, err := chooseRestoreMember(locals, haRestoreMember, scope)
		if err != nil {
			return err
		}
		others := append([]string{}, locals...)
		others = removeMember(others, boot)

		pm, err := podman.NewPatroniManager(cfg)
		if err != nil {
			return err
		}

		// Pre-flight: the destructive path clears the DCS and makes a LOCAL member
		// re-bootstrap. If the CURRENT leader is not controllable from this host,
		// its Patroni daemon keeps running and would race the bootstrap to
		// re-claim the DCS on the OLD timeline after the key is removed. A leader
		// is controllable only if it is one of this host's local members — a
		// cross-host member may or may not appear in the local config view at all,
		// so membership in `locals` (not the RemoteHost flag) is the test. Refuse
		// the real run (and warn in --dry-run) until leadership is here.
		leaderName := currentLeaderName(pm, cluster)
		leaderLocal := leaderName == "" // unreadable DCS → no hard refusal
		for _, l := range locals {
			if l == leaderName {
				leaderLocal = true
				break
			}
		}

		// Pre-validation: the target must not predate the latest backup stop time,
		// or pgBackRest has no usable base to restore from (error [075]). Mirrors
		// single-instance restore, but sourced from the cluster stanza's info.
		if err := validateRestoreStopTime(bm, stanza, targetTime); err != nil {
			return err
		}

		restoreCmd := buildRestoreCmd(stanza, targetTime)

		if haRestoreDryRun {
			printRestorePlan(scope, cluster, stanza, boot, others, targetTime, restoreCmd, leaderName, !leaderLocal)
			return nil
		}

		if !leaderLocal {
			return fmt.Errorf(
				"current leader %q is not a local member of scope %q — this host cannot stop it, so it would\n"+
					"  race the local bootstrap to re-claim the DCS on the old timeline.\n"+
					"  Move leadership here first (pg ha switchover %s or pg ha failover %s to a local member: %v),\n"+
					"  or run `pg ha restore` on the leader's host",
				leaderName, scope, scope, scope, locals)
		}

		if !haRestoreForce {
			fmt.Printf("!  Confirm Patroni cluster restore\n")
			fmt.Printf("  Scope:        %s\n", scope)
			fmt.Printf("  Stanza:       %s\n", stanza)
			fmt.Printf("  Target time:  %s\n", targetTime.Format("2006-01-02 15:04:05"))
			fmt.Printf("  Bootstrap on: %s (local member)\n", boot)
			if len(others) > 0 {
				fmt.Printf("  Rejoin local: %v\n", others)
			}
			fmt.Printf("  Action:       restore + auto-promote to a NEW writable timeline\n")
			fmt.Printf("  This wipes every local member's data dir; all changes after the target are PERMANENTLY LOST.\n")
			fmt.Println()
			if !confirmPrompt("Confirm? [y/N]: ") {
				fmt.Println("Cancelled")
				return nil
			}
		}

		return runClusterRestore(pm, cluster, stanza, boot, others, targetTime, restoreCmd)
	},
}

// localMembers returns the names of the scope's members that live on this host
// (RemoteHost empty), sorted for a deterministic default pick.
func localMembers(cluster *config.PatroniClusterConfig) []string {
	var out []string
	for name, mb := range cluster.Members {
		if mb.RemoteHost == "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// chooseRestoreMember resolves --member against the local pool, defaulting to
// the first local member. A member that is remote or not in the scope is an
// error with a clear hint.
func chooseRestoreMember(locals []string, want, scope string) (string, error) {
	if want == "" {
		return locals[0], nil
	}
	for _, l := range locals {
		if l == want {
			return want, nil
		}
	}
	return "", fmt.Errorf("member %q cannot carry the restore (not a local member of scope %q) — local members: %v", want, scope, locals)
}

func removeMember(list []string, name string) []string {
	out := list[:0]
	for _, m := range list {
		if m != name {
			out = append(out, m)
		}
	}
	return out
}

// validateRestoreStopTime rejects a target earlier than the newest backup stop
// time — pgBackRest cannot restore before its base backup completes ([075]).
func validateRestoreStopTime(bm *podman.BackupManager, stanza string, target time.Time) error {
	out, err := bm.BackupExec(false, "pgbackrest", "--stanza="+stanza, "info", "--log-level-console=info")
	if err != nil {
		// info failing here (no backups yet, repo unreachable) should not block
		// the restore path — the actual bootstrap will surface the real error.
		return nil
	}
	var latest time.Time
	for _, s := range pitr.ParseInfoOutput(out) {
		if !s.StopTime.IsZero() && s.StopTime.After(latest) {
			latest = s.StopTime
		}
	}
	if latest.IsZero() {
		return nil
	}
	if target.Before(latest) {
		return fmt.Errorf(
			"target time %s is before the latest backup stop time %s\n"+
				"  The earliest usable restore point is: %s\n"+
				"  Hint: use --time \"%s\" or later",
			target.UTC().Format("2006-01-02 15:04:05"),
			latest.UTC().Format("2006-01-02 15:04:05"),
			latest.UTC().Format("2006-01-02 15:04:05"),
			latest.UTC().Format("2006-01-02 15:04:05"))
	}
	return nil
}

// buildRestoreCmd renders the pgbackrest restore Patroni runs as the bootstrap
// method. --type=time --target pins the point; --target-action=promote makes the
// bootstrapped member the writable leader on a new timeline; --delta lets it
// land into the emptied dir. The target is quoted so its space survives
// Patroni's shlex-style command splitting.
func buildRestoreCmd(stanza string, target time.Time) string {
	return fmt.Sprintf(
		`pgbackrest --stanza=%s --type=time --target="%s" --target-action=promote --delta restore`,
		stanza, target.Format("2006-01-02 15:04:05-07"))
}

// runClusterRestore is the destructive execution path. See the plan's steps 1-8.
func runClusterRestore(pm *podman.PatroniManager, cluster *config.PatroniClusterConfig, stanza, boot string, others []string, targetTime time.Time, restoreCmd string) error {
	// 2. Stop all local members so none keeps writing the old timeline or races
	//    the bootstrap. Remote members are left running — they lose the leader /
	//    DCS key and wait; the caller is told to reinit them afterward.
	fmt.Println("-> Stopping local member containers...")
	for name, mb := range cluster.Members {
		if mb.RemoteHost != "" {
			continue
		}
		if _, err := pm.StopMemberContainer(mb.ContainerName); err != nil {
			fmt.Printf("  [!] %s: stop: %v\n", name, err)
		}
	}

	// 3. Clear the cluster identity from the DCS so bootstrap.dcs will run.
	fmt.Println("-> Removing the cluster from the DCS (patronictl remove)...")
	if err := pm.RemoveScopeFromDCS(cluster); err != nil {
		return fmt.Errorf("removing scope from DCS: %w", err)
	}
	fmt.Println("  [OK] DCS entry removed")

	// 4-5. Wipe the bootstrap member's data dir, render its restore-flavoured
	//      patroni.yml, recreate + start the container so Patroni runs the
	//      pgbackrest bootstrap.
	if err := bootstrapMember(pm, cluster, boot, stanza, restoreCmd); err != nil {
		return err
	}

	// Optionally stream the member's logs while it recovers.
	var stopTail func()
	if haRestoreTailLogs {
		stopTail = startMemberLogTail(cluster.Members[boot].ContainerName)
		defer stopTail()
	}

	// 6. Wait for the new leader to appear in the DCS.
	fmt.Println("-> Waiting for the bootstrap member to recover and promote...")
	leader, err := waitForLeader(pm, cluster, 15*time.Minute)
	if err != nil {
		return fmt.Errorf("restore bootstrap did not yield a leader: %w", err)
	}
	fmt.Printf("  [OK] new leader: %s\n", leader)

	// 7. Rejoin the remaining local members on standard bootstrap: wipe, write
	//    the normal patroni.yml (no restore method), recreate. As replicas they
	//    basebackup the new leader.
	for _, name := range others {
		mb := cluster.Members[name]
		if _, err := pm.WriteMemberConfig(cluster, name); err != nil {
			fmt.Printf("  [!] %s: write config: %v\n", name, err)
			continue
		}
		if err := wipeMemberDataDir(mb); err != nil {
			fmt.Printf("  [!] %s: wipe data dir: %v\n", name, err)
			continue
		}
		if err := pm.EnsureMemberContainer(cluster, name); err != nil {
			fmt.Printf("  [!] %s: recreate: %v\n", name, err)
			continue
		}
		fmt.Printf("  [OK] %s rejoining as replica\n", name)
	}

	// 8. Report + next steps.
	fmt.Println()
	fmt.Println("OK Cluster restore complete")
	fmt.Printf("  Scope:        %s\n", cluster.Name)
	fmt.Printf("  New leader:   %s (timeline switched)\n", leader)
	fmt.Printf("  Restored to:  %s\n", targetTime.Format("2006-01-02 15:04:05"))
	fmt.Println("  Next steps:")
	fmt.Printf("    - re-baseline: pg ha snapshot create %s --type full\n", cluster.Name)
	fmt.Println("    - a data-dir rebuild changes the system-id, so the first snapshot may")
	fmt.Printf("      fail with [051]; fix with: pg backup stanza-upgrade %s\n", stanza)
	if hasRemoteMembers(cluster) {
		fmt.Println("    - remote members rebuild themselves from the new leader; if one stays")
		fmt.Printf("      down: pg ha ctl %s -- reinit %s <member> --force\n", cluster.Name, cfg.PatroniScope(cluster.Name))
	}
	return nil
}

// bootstrapMember wipes a member's data dir, writes the restore patroni.yml, and
// recreates the container so Patroni runs the pgbackrest bootstrap method.
func bootstrapMember(pm *podman.PatroniManager, cluster *config.PatroniClusterConfig, boot, stanza, restoreCmd string) error {
	mb := cluster.Members[boot]
	if _, err := pm.WriteMemberRestoreConfig(cluster, boot, podman.PatroniRestoreBootstrap{RestoreCmd: restoreCmd}); err != nil {
		return fmt.Errorf("writing restore config for %s: %w", boot, err)
	}
	if err := wipeMemberDataDir(mb); err != nil {
		return fmt.Errorf("wiping data dir for %s: %w", boot, err)
	}
	fmt.Printf("-> Bootstrapping %s from the repo (stanza %s)...\n", boot, stanza)
	if err := pm.EnsureMemberContainer(cluster, boot); err != nil {
		return fmt.Errorf("starting bootstrap member %s: %w", boot, err)
	}
	return nil
}

// wipeMemberDataDir removes the member's PostgreSQL data subdirectory so Patroni
// sees an empty data dir and runs bootstrap. The member dir doubles as the host
// bind mount at /var/lib/postgresql, with PGDATA at ./data; patroni.yml, .pgpass
// and the SSH passwd/group live alongside and are preserved (rewritten by the
// config write + container recreate).
func wipeMemberDataDir(mb config.PatroniMemberConfig) error {
	if mb.DataDir == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(mb.DataDir, "data"))
}

func startMemberLogTail(container string) func() {
	cmd := exec.Command("podman", "logs", "-f", container)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Printf("  [!] could not stream logs: %v\n", err)
		return func() {}
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return func() {
		_ = cmd.Process.Kill()
		<-done
	}
}

// waitForLeader polls the DCS (via a captured patronictl list) until a leader is
// registered or the timeout elapses. A blank leader name means no member has
// published itself yet — the bootstrap is still recovering.
func waitForLeader(pm *podman.PatroniManager, cluster *config.PatroniClusterConfig, timeout time.Duration) (string, error) {
	nsScope := cfg.PatroniScope(cluster.Name)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if out, err := pm.PatronictlCapture(cluster, "list", nsScope, "-f", "json"); err == nil {
			if leader := podman.PatroniLeaderFromListJSON(out); leader != "" {
				return leader, nil
			}
		}
		time.Sleep(5 * time.Second)
	}
	return "", fmt.Errorf("no leader registered in the DCS within %s", timeout)
}

func hasRemoteMembers(cluster *config.PatroniClusterConfig) bool {
	for _, mb := range cluster.Members {
		if mb.RemoteHost != "" {
			return true
		}
	}
	return false
}

// currentLeaderName reads the leader member name from the DCS once (empty if no
// leader is published yet or the read fails).
func currentLeaderName(pm *podman.PatroniManager, cluster *config.PatroniClusterConfig) string {
	nsScope := cfg.PatroniScope(cluster.Name)
	out, err := pm.PatronictlCapture(cluster, "list", nsScope, "-f", "json")
	if err != nil {
		return ""
	}
	return podman.PatroniLeaderFromListJSON(out)
}

// printRestorePlan renders the destructive sequence without touching anything.
func printRestorePlan(scope string, cluster *config.PatroniClusterConfig, stanza, boot string, others []string, target time.Time, restoreCmd, leaderName string, leaderRemote bool) {
	fmt.Println("[DRY RUN] Would restore the Patroni cluster with a custom-bootstrap PITR:")
	fmt.Printf("  Scope:        %s\n", scope)
	fmt.Printf("  Stanza:       %s\n", stanza)
	fmt.Printf("  Target time:  %s\n", target.Format("2006-01-02 15:04:05"))
	if leaderName != "" {
		fmt.Printf("  Current leader: %s\n", leaderName)
	}
	if leaderRemote {
		fmt.Printf("  [!!] current leader %q is NOT a local member — the real run REFUSES until leadership\n", leaderName)
		fmt.Printf("       is on this host (switchover/failover to a local member, or restore on the leader's host).\n")
	}
	fmt.Println("  Steps:")
	fmt.Printf("    1. stop local members (%v)\n", localMembers(cluster))
	fmt.Printf("    2. patronictl remove %s (clear DCS identity)\n", cfg.PatroniScope(scope))
	fmt.Printf("    3. wipe %s data dir; write patroni.yml with bootstrap method:\n        %s\n", boot, restoreCmd)
	fmt.Printf("    4. start %s — Patroni recovers to the target and promotes to leader on a NEW timeline\n", boot)
	if len(others) > 0 {
		fmt.Printf("    5. wipe + restart %v as standard replicas; they basebackup the new leader\n", others)
	}
	if hasRemoteMembers(cluster) {
		fmt.Println("    6. remote members rejoin via the DCS (reinit if they do not return)")
	}
	fmt.Println("  No changes made. Re-run without --dry-run to execute.")
}
