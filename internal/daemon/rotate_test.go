package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func backupNames(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "daemon-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestRotatingWriterMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	w := newRotatingWriter(path, &config.DaemonLogRotate{MaxSize: 1 << 20, Keep: 3},
		fixedClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)))
	defer w.Close()

	chunk := make([]byte, 600<<10)
	if _, err := w.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if backups := backupNames(t, dir); len(backups) != 0 {
		t.Fatalf("rotated before exceeding MaxSize: %v", backups)
	}
	if _, err := w.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if backups := backupNames(t, dir); len(backups) != 1 {
		t.Fatalf("size rotation produced %d backups, want 1", len(backups))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(chunk)) {
		t.Errorf("active log size = %d, want %d after rotation", info.Size(), len(chunk))
	}
}

func TestRotatingWriterDaily(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	now := time.Date(2026, 11, 2, 23, 59, 0, 0, time.Local)
	w := newRotatingWriter(path, &config.DaemonLogRotate{Daily: true, Keep: 7}, func() time.Time { return now })
	defer w.Close()

	if _, err := w.Write([]byte("day one\n")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute) // past midnight
	if _, err := w.Write([]byte("day two\n")); err != nil {
		t.Fatal(err)
	}
	backups := backupNames(t, dir)
	if len(backups) != 1 {
		t.Fatalf("daily rotation produced %d backups, want 1", len(backups))
	}
	data, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "day one\n" {
		t.Errorf("backup = %q, want yesterday's content", data)
	}
	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != "day two\n" {
		t.Errorf("active = %q, want today's content", active)
	}
}

func TestRotatingWriterStaleDayOnStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte("yesterday\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 11, 1, 23, 0, 0, 0, time.Local)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 2, 8, 0, 0, 0, time.Local)
	w := newRotatingWriter(path, &config.DaemonLogRotate{Daily: true, Keep: 7}, fixedClock(now))
	defer w.Close()

	if backups := backupNames(t, dir); len(backups) != 1 {
		t.Fatalf("stale log not rotated on start: %d backups", len(backups))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Errorf("new active log has %d bytes, want empty", info.Size())
	}
}

func TestRotatingWriterRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	w := newRotatingWriter(path, &config.DaemonLogRotate{MaxSize: 1 << 20, Keep: 2},
		fixedClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)))
	defer w.Close()

	chunk := make([]byte, 700<<10)
	for range 6 {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // lumberjack names backups per millisecond
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		backups := backupNames(t, dir)
		if len(backups) <= 2 {
			if len(backups) != 2 {
				t.Fatalf("retention kept %d backups, want 2", len(backups))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("retention did not prune: %d backups", len(backups))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRotatingWriterDailyOnlyIgnoresSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	w := newRotatingWriter(path, &config.DaemonLogRotate{Daily: true, Keep: 0},
		fixedClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)))
	defer w.Close()

	chunk := make([]byte, 1<<20)
	for range 3 {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if backups := backupNames(t, dir); len(backups) != 0 {
		t.Errorf("daily-only mode rotated by size: %v", backups)
	}
}
