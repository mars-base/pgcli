package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
