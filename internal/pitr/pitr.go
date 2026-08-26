// Package pitr implements core PITR (Point-In-Time Recovery) features:
// snapshot create/list/delete, point-in-time restore, branch clone.
// pgBackRest operations are executed inside the shared backup container via BackupExec.
package pitr

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/podman"
)

// Snapshot represents a pgBackRest backup snapshot.
type Snapshot struct {
	Name      string    `json:"name"`      // Backup label
	Timestamp time.Time `json:"timestamp"` // Backup start time
	StopTime  time.Time `json:"stop_time"` // Backup stop time (earliest usable restore point)
	Type      string    `json:"type"`      // full / incr / diff
	Size      int64     `json:"size"`      // Backup size (bytes)
}

// Manager encapsulates PITR operations.
// pgBackRest commands run in the shared backup container (via BackupManager),
// while PG container lifecycle (stop/start) uses the podman Manager.
type Manager struct {
	cfg    *config.Config
	podman *podman.Manager       // PG container lifecycle
	backup *podman.BackupManager // pgBackRest operations (in backup container)
}

// New creates a PITR manager.
func New(cfg *config.Config, pm *podman.Manager, bm *podman.BackupManager) *Manager {
	return &Manager{cfg: cfg, podman: pm, backup: bm}
}

// --- Stanza management ------------------------------------------

// EnsureStanza ensures the pgBackRest stanza is created.
func (m *Manager) EnsureStanza() error {
	stanza := m.cfg.PITR.PgBackRestStanza

	// pgBackRest may connect before PostgreSQL has fully finished crash recovery
	// or initdb, resulting in "database system is starting up". Retry with
	// backoff instead of failing the whole start flow.
	var out string
	var err error
	for i := 0; i < 15; i++ {
		out, err = m.pgbackrest(false, "--stanza="+stanza, "stanza-create", "--log-level-console=info")
		if err == nil {
			fmt.Println("-> pgBackRest stanza created")
			break
		}
		// stanza-create errors if already exists, ignore
		if strings.Contains(err.Error(), "already exists") || strings.Contains(out, "already exists") {
			err = nil
			break
		}
		// Retry on transient startup races.
		msg := err.Error() + out
		if strings.Contains(msg, "database system is starting up") ||
			strings.Contains(msg, "unable to check pg1") {
			time.Sleep(2 * time.Second)
			continue
		}
		_ = m.backup.EnsureRepoReadable()
		return fmt.Errorf("creating pgBackRest stanza: %w\n%s", err, out)
	}
	if err != nil {
		_ = m.backup.EnsureRepoReadable()
		return fmt.Errorf("creating pgBackRest stanza: %w\n%s", err, out)
	}

	// Make sure repo files are readable by the postgres user during archive-get.
	if err := m.backup.EnsureRepoReadable(); err != nil {
		fmt.Printf("  [!] repo readability warning: %v\n", err)
	}
	return nil
}

// CheckStanza verifies the stanza configuration.
func (m *Manager) CheckStanza() error {
	stanza := m.cfg.PITR.PgBackRestStanza
	out, err := m.pgbackrest(false, "--stanza="+stanza, "check", "--log-level-console=info")
	if err != nil {
		return fmt.Errorf("pgBackRest stanza check failed: %w\n%s", err, out)
	}
	return nil
}

// --- Snapshot management ---------------------------------------------

// CreateSnapshot creates a backup snapshot.
// backupType: "full" (default), "incr", "diff"
// tailLogs streams the backup container output to stdout during the backup.
func (m *Manager) CreateSnapshot(backupType string, tailLogs bool) (*Snapshot, error) {
	if backupType == "" {
		backupType = "full"
	}

	stanza := m.cfg.PITR.PgBackRestStanza
	args := []string{
		"--stanza=" + stanza,
		"backup",
		"--type=" + backupType,
		"--log-level-console=info",
	}

	fmt.Printf("-> Creating %s backup...\n", backupType)
	out, err := m.pgbackrest(tailLogs, args...)
	if err != nil {
		_ = m.backup.EnsureRepoReadable()
		return nil, fmt.Errorf("creating backup: %w\n%s", err, out)
	}

	// Make sure repo files are readable by the postgres user during archive-get/recovery.
	if err := m.backup.EnsureRepoReadable(); err != nil {
		fmt.Printf("  [!] repo readability warning: %v\n", err)
	}

	// Parse backup label
	label := extractLabel(out)
	snap := &Snapshot{
		Name:      label,
		Timestamp: time.Now(),
		Type:      backupType,
	}
	fmt.Printf("  [OK] Snapshot created: %s (%s)\n", label, backupType)
	return snap, nil
}

// CreateSnapshotToWriter creates a backup snapshot, streaming all pgBackRest
// output line-by-line to w.  It does not write to os.Stdout/os.Stderr, making
// it suitable for GUI use where output must be forwarded via events.
func (m *Manager) CreateSnapshotToWriter(w io.Writer, backupType string) (*Snapshot, error) {
	if backupType == "" {
		backupType = "full"
	}

	stanza := m.cfg.PITR.PgBackRestStanza
	args := []string{"pgbackrest",
		"--stanza=" + stanza,
		"backup",
		"--type=" + backupType,
		"--log-level-console=info",
	}

	fmt.Fprintf(w, "-> Creating %s backup...\n", backupType)
	out, err := m.backup.BackupExecToWriter(w, args...)
	if err != nil {
		_ = m.backup.EnsureRepoReadable()
		return nil, fmt.Errorf("creating backup: %w\n%s", err, out)
	}

	if err := m.backup.EnsureRepoReadable(); err != nil {
		fmt.Fprintf(w, "  [!] repo readability warning: %v\n", err)
	}

	label := extractLabel(out)
	snap := &Snapshot{
		Name:      label,
		Timestamp: time.Now(),
		Type:      backupType,
	}
	fmt.Fprintf(w, "  [OK] Snapshot created: %s (%s)\n", label, backupType)
	return snap, nil
}

// ListSnapshots lists all backups.
func (m *Manager) ListSnapshots(limit int) ([]Snapshot, error) {
	stanza := m.cfg.PITR.PgBackRestStanza
	args := []string{
		"--stanza=" + stanza,
		"info",
		"--log-level-console=info",
	}

	out, err := m.pgbackrest(false, args...)
	if err != nil {
		return nil, fmt.Errorf("listing backups: %w\n%s", err, out)
	}

	snapshots := parseInfoOutput(out)
	if limit > 0 && limit < len(snapshots) {
		snapshots = snapshots[:limit]
	}
	return snapshots, nil
}

// DeleteSnapshot deletes a specific backup.
func (m *Manager) DeleteSnapshot(name string) error {
	stanza := m.cfg.PITR.PgBackRestStanza
	args := []string{
		"--stanza=" + stanza,
		"expire",
		"--set=" + name,
		"--log-level-console=info",
	}

	out, err := m.pgbackrest(false, args...)
	if err != nil {
		return fmt.Errorf("deleting backup %s: %w\n%s", name, err, out)
	}
	fmt.Printf("-> Snapshot %s deleted\n", name)
	return nil
}

// DeleteBefore deletes backups older than a specified time.
func (m *Manager) DeleteBefore(before time.Time) error {
	stanza := m.cfg.PITR.PgBackRestStanza
	args := []string{
		"--stanza=" + stanza,
		"expire",
		"--log-level-console=info",
	}

	// pgBackRest expire uses retention config to auto-handle old backups
	out, err := m.pgbackrest(false, args...)
	if err != nil {
		return fmt.Errorf("cleaning old backups: %w\n%s", err, out)
	}
	fmt.Println("-> Old backups cleaned")
	return nil
}

// --- PITR restore --------------------------------------------

// Restore performs point-in-time recovery.
// targetTime specifies the recovery target time.
// promote controls the recovery target action: when false (default) the
// cluster is left paused at the target time in read-only state so the data
// can be inspected and Restore called again with a different target; when
// true the cluster is promoted to a new timeline and becomes read-write.
// dryRun only prints the plan. tailLogs streams the restore container output to stdout.
// Restore process:
// 1. Stop PostgreSQL container
// 2. pgBackRest restore in a temporary container mounting the same data directory
// 3. Start PostgreSQL container
func (m *Manager) Restore(targetTime time.Time, promote, dryRun, tailLogs bool) error {
	stanza := m.cfg.PITR.PgBackRestStanza
	targetStr := targetTime.Format("2006-01-02 15:04:05-07")

	if dryRun {
		fmt.Printf("-> [DRY RUN] Would restore to: %s (target-action=%s)\n", targetStr, targetActionName(promote))
		return nil
	}

	fmt.Printf("!  Restoring PostgreSQL to %s (target-action=%s)\n", targetStr, targetActionName(promote))

	// Fast path: if the cluster is already paused in recovery and the
	// user only wants to promote, check whether the paused cluster is at
	// (or very near) the requested target time.  pg_last_xact_replay_timestamp
	// returns the last committed transaction, which is always slightly before
	// the recovery target — a 2 s tolerance handles the gap.
	//
	// When the timestamps match: skip the full re-restore and simply resume
	// WAL replay (pg_wal_replay_resume).  When they don't (e.g. user chose a
	// different --time), fall through to the full wipe + restore + replay path.
	if promote {
		if paused, _ := m.podman.PGIsPausedInRecovery(); paused {
			replayed, err := m.podman.PGLastXactReplayTimestamp()
			if err == nil && !replayed.IsZero() {
				diff := targetTime.Sub(replayed)
				if diff < 0 {
					diff = -diff
				}
				if diff < 2*time.Second {
					fmt.Println("  Instance already paused at target time, promoting directly (skip re-restore)...")
					out, err := m.podman.PGPromoteAfterRecovery()
					if err != nil {
						return fmt.Errorf("promoting after recovery: %w\n%s", err, out)
					}
					fmt.Println("[OK] Instance promoted to read-write")
					return nil
				}
				fmt.Printf("  Paused at %s, target is %s — full restore required\n",
					replayed.Truncate(time.Second).Format("15:04:05"),
					targetTime.Truncate(time.Second).Format("15:04:05"))
			}
		}
	}

	// Ensure repo is readable by postgres user during recovery archive-get.
	if err := m.backup.EnsureRepoReadable(); err != nil {
		fmt.Printf("  [!] repo readability warning: %v\n", err)
	}

	fmt.Println("  1/3 Stopping PostgreSQL...")
	if err := m.podman.StopContainer(); err != nil {
		return fmt.Errorf("stopping container: %w", err)
	}

	fmt.Println("  2/3 Running pgBackRest restore...")
	out, err := m.podman.RunRestoreContainer(stanza, targetStr, promote, tailLogs)
	if err != nil {
		return fmt.Errorf("pgBackRest restore: %w\n%s", err, out)
	}

	// Restore runs pgbackrest as the postgres user, so any repo files it
	// touches are already postgres-owned; no readability fixup is needed.
	_ = m.backup.EnsureRepoReadable()

	fmt.Println("  3/3 Starting PostgreSQL...")
	if err := m.podman.StartContainer(); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}

	// Re-authorize the backup SSH key on the PG container now that it is
	// running. The authorized_keys file lives inside the container and is lost
	// when the container is recreated; restore does not need SSH itself (it
	// uses a temporary container that mounts the data dir locally), but the
	// key must be installed before the next backup. We do this after start so
	// it also works when PG was down (e.g. recovering from a failed restore).
	if err := m.backup.AuthorizeKeyOnInstance(); err != nil {
		fmt.Printf("  [!] backup key authorization warning: %v\n", err)
	}

	// PG container IP may have changed; refresh backup container /etc/hosts so
	// subsequent backups can reach it.
	if err := m.backup.EnsureBackupInfra(); err != nil {
		fmt.Printf("  [!] backup infra refresh warning: %v\n", err)
	}

	fmt.Println("[OK] Restore complete")
	return nil
}

// RestoreToWriter performs PITR like Restore but streams all progress output
// to w so callers (e.g. the GUI) can display real-time logs.
func (m *Manager) RestoreToWriter(w io.Writer, targetTime time.Time, promote bool) error {
	stanza := m.cfg.PITR.PgBackRestStanza
	targetStr := targetTime.Format("2006-01-02 15:04:05-07")

	fmt.Fprintf(w, "!  Restoring PostgreSQL to %s (target-action=%s)\n", targetStr, targetActionName(promote))

	// Fast path: already paused at target time → just promote.
	if promote {
		if paused, _ := m.podman.PGIsPausedInRecovery(); paused {
			replayed, err := m.podman.PGLastXactReplayTimestamp()
			if err == nil && !replayed.IsZero() {
				diff := targetTime.Sub(replayed)
				if diff < 0 {
					diff = -diff
				}
				if diff < 2*time.Second {
					fmt.Fprintln(w, "  Instance already paused at target time, promoting directly...")
					out, err := m.podman.PGPromoteAfterRecovery()
					if err != nil {
						return fmt.Errorf("promoting after recovery: %w\n%s", err, out)
					}
					fmt.Fprintln(w, "[OK] Instance promoted to read-write")
					return nil
				}
				fmt.Fprintf(w, "  Paused at %s, target is %s — full restore required\n",
					replayed.Truncate(time.Second).Format("15:04:05"),
					targetTime.Truncate(time.Second).Format("15:04:05"))
			}
		}
	}

	if err := m.backup.EnsureRepoReadable(); err != nil {
		fmt.Fprintf(w, "  [!] repo readability warning: %v\n", err)
	}

	fmt.Fprintln(w, "  1/3 Stopping PostgreSQL...")
	if err := m.podman.StopContainer(); err != nil {
		return fmt.Errorf("stopping container: %w", err)
	}

	fmt.Fprintln(w, "  2/3 Running pgBackRest restore...")
	_, err := m.podman.RunRestoreContainerToWriter(w, stanza, targetStr, promote)
	if err != nil {
		return fmt.Errorf("pgBackRest restore: %w", err)
	}

	_ = m.backup.EnsureRepoReadable()

	fmt.Fprintln(w, "  3/3 Starting PostgreSQL...")
	if err := m.podman.StartContainer(); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}

	if err := m.backup.AuthorizeKeyOnInstance(); err != nil {
		fmt.Fprintf(w, "  [!] backup key authorization warning: %v\n", err)
	}
	if err := m.backup.EnsureBackupInfra(); err != nil {
		fmt.Fprintf(w, "  [!] backup infra refresh warning: %v\n", err)
	}

	fmt.Fprintln(w, "[OK] Restore complete")
	return nil
}


func (m *Manager) pgbackrest(tailLogs bool, args ...string) (string, error) {
	slog.Debug("pgbackrest", "args", args)
	return m.backup.BackupExec(tailLogs, append([]string{"pgbackrest"}, args...)...)
}

// targetActionName returns the human-readable recovery target action.
func targetActionName(promote bool) string {
	if promote {
		return "promote"
	}
	return "pause"
}

// extractLabel extracts the backup label from pgBackRest output.
func extractLabel(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"full backup:", "incr backup:", "diff backup:"} {
			if name, ok := strings.CutPrefix(line, prefix); ok {
				return strings.TrimSpace(name)
			}
		}
		if _, after, ok := strings.Cut(line, "new backup label ="); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// parseInfoOutput parses pgbackrest info output into a Snapshot list.
func parseInfoOutput(out string) []Snapshot {
	var snapshots []Snapshot

	// pgbackrest info output format example:
	// full backup: 20260614-143005F
	//     timestamp start/stop: 2026-06-14 14:30:05+00 / 2026-06-14 14:30:10+00
	//     database size: 30MB, database backup size: 30MB
	//
	// incr backup: 20260614-150010I
	//     timestamp start/stop: 2026-06-14 15:00:10+00 / 2026-06-14 15:00:15+00
	//     ...
	var current *Snapshot

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)

		// Detect backup line
		for _, bt := range []string{"full backup:", "incr backup:", "diff backup:"} {
			if name, ok := strings.CutPrefix(line, bt); ok {
				name = strings.TrimSpace(name)
				current = &Snapshot{
					Name: name,
					Type: strings.TrimSuffix(bt, " backup:"),
				}
				snapshots = append(snapshots, *current)
				continue
			}
		}

		if current == nil {
			continue
		}

		// Parse timestamp. pgbackrest info uses "timestamp start/stop: <start> / <stop>"
		if ts, ok := strings.CutPrefix(line, "timestamp start/stop:"); ok {
			// ts is " <start>+00 / <stop>+00"
			parts := strings.SplitN(ts, " / ", 2)
			if len(parts) > 0 {
				start := strings.TrimSpace(parts[0])
				if t, err := time.Parse("2006-01-02 15:04:05Z07", start); err == nil {
					current.Timestamp = t
					snapshots[len(snapshots)-1].Timestamp = t
				}
			}
			if len(parts) > 1 {
				stop := strings.TrimSpace(parts[1])
				if t, err := time.Parse("2006-01-02 15:04:05Z07", stop); err == nil {
					current.StopTime = t
					snapshots[len(snapshots)-1].StopTime = t
				}
			}
		}
	}

	return snapshots
}
