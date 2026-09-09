package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestNeedsMigrate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "logrus-style pause reset hint",
			in:   `time="2026-08-28T14:25:33+08:00" level=error msg="invalid internal status, try resetting the pause process with /home/diwen/.local/share/podman-static/bin/podman system migrate: could not find any running process: no such process"`,
			want: true,
		},
		{
			name: "short migrate hint",
			in:   "Error: need podman system migrate",
			want: true,
		},
		{
			name: "unrelated podman error",
			in:   "Error: no container with name or ID foo found",
			want: false,
		},
		{
			name: "empty output",
			in:   "",
			want: false,
		},
	}
	for _, c := range cases {
		if got := needsMigrate(c.in); got != c.want {
			t.Errorf("%s: needsMigrate(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// TestEnsurePodmanReadyRepairsStaleState exercises the full repair path with a
// fake podman binary: a stale probe must trigger `podman system migrate`, after
// which the same instance reports healthy.
func TestEnsurePodmanReadyRepairsStaleState(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "podman")
	state := filepath.Join(dir, "state")

	script := fmt.Sprintf(`#!/bin/sh
STATE=%q
case "$*" in
  *"system migrate"*)
    printf ok > "$STATE"
    echo "Migrated"
    exit 0
    ;;
  *)
    if [ "$(cat "$STATE" 2>/dev/null)" = "ok" ]; then
      echo "pgcli-pg-default"
      exit 0
    fi
    echo 'time="x" level=error msg="invalid internal status, try resetting the pause process with podman system migrate: could not find any running process"' >&2
    exit 125
    ;;
esac
`, state)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &Manager{podman: bin}
	m.ensurePodmanReady()

	if !m.repaired {
		t.Fatal("expected Manager.repaired=true after repairing stale state")
	}
	b, err := os.ReadFile(state)
	if err != nil || string(b) != "ok" {
		t.Fatalf("expected state file 'ok', got %q err=%v", b, err)
	}
}

// TestEnsurePodmanReadyRestartsOnlyAfterMigrate pins the reboot-recovery gate:
// stopped containers are restarted only when a stale rootless state actually
// required `podman system migrate` (i.e. right after a reboot). On a healthy
// podman, ensurePodmanReady must leave stopped containers alone — otherwise a
// read-only command like `pg list` would resurrect a manual `pg stop`.
func TestEnsurePodmanReadyRestartsOnlyAfterMigrate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only")
	}

	// Reset and restore the process-global one-shot guards so this test is
	// order-independent regardless of the repair test running first.
	savedGlobal := podmanStateRepaired
	defer func() { podmanStateRepaired = savedGlobal }()

	cases := []struct {
		name          string
		stale         bool
		wantRestarted bool
	}{
		{name: "stale state restarts stopped containers", stale: true, wantRestarted: true},
		{name: "healthy state leaves stopped containers alone", stale: false, wantRestarted: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			podmanStateRepaired = false
			dir := t.TempDir()
			bin := filepath.Join(dir, "podman")
			started := filepath.Join(dir, "started")

			// The fake podman inspects its arguments:
			//   probe (`ps -a --format {{.Names}}`, no --filter) → stale: the
			//   migrate error; healthy: a running name (exit 0).
			//   system migrate → record success.
			//   restart enumeration (`--filter name=pgcli-`) → one Exited row.
			//   start <name> → touch the marker the assertion checks.
			mode := "healthy"
			if tc.stale {
				mode = "stale"
			}
			script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"system migrate"*)
    echo migrated; exit 0 ;;
  *"--filter name=pgcli-"*)
    printf 'pgcli-pg-ra2\tExited (0) 2 days ago\n'; exit 0 ;;
  start\ *)
    printf started >> %q; exit 0 ;;
  *"ps -a --format"*)
    if [ %q = "stale" ]; then
      echo 'time="x" level=error msg="invalid internal status, try resetting the pause process with podman system migrate"' >&2
      exit 125
    fi
    echo pgcli-pg-ra2; exit 0 ;;
  *) exit 0 ;;
esac
`, started, mode)
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}

			m := &Manager{cfg: config.Default(), podman: bin}
			m.ensurePodmanReady()

			_, err := os.Stat(started)
			restarted := err == nil
			if restarted != tc.wantRestarted {
				t.Errorf("restart of stopped containers = %v, want %v (stale=%v)",
					restarted, tc.wantRestarted, tc.stale)
			}
		})
	}
}
