//go:build !windows

package podman

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecWithTimeoutCleanupNoise(t *testing.T) {
	noise := `time="2026-11-05T16:49:45+08:00" level=error msg="forwarding signal 28 to container abc: no container with ID abc found in database: no such container"` + "\n" +
		`time="2026-11-05T16:49:45+08:00" level=error msg="Waiting for container abc: container has already been removed"`

	t.Run("cleanup noise maps to sentinel", func(t *testing.T) {
		_, err := execWithTimeoutCleanupNoise("sh",
			[]string{"-c", `printf '%s\n' "$1" >&2; exit 127`, "sh", noise}, 5*time.Second)
		if !errors.Is(err, errPodmanCleanupNoise) {
			t.Fatalf("expected errPodmanCleanupNoise, got %v", err)
		}
		if !strings.Contains(err.Error(), "forwarding signal") {
			t.Fatalf("sentinel should keep the stderr text, got %v", err)
		}
	})

	t.Run("real stderr stays an error", func(t *testing.T) {
		_, err := execWithTimeoutCleanupNoise("sh",
			[]string{"-c", `printf 'psql: error: connection refused\n' >&2; exit 2`}, 5*time.Second)
		if err == nil || errors.Is(err, errPodmanCleanupNoise) {
			t.Fatalf("expected a real error, got %v", err)
		}
	})

	t.Run("success returns stdout", func(t *testing.T) {
		out, err := execWithTimeoutCleanupNoise("sh",
			[]string{"-c", `echo slot-1`}, 5*time.Second)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.TrimSpace(out) != "slot-1" {
			t.Fatalf("unexpected output %q", out)
		}
	})

	t.Run("timeout kills the process group", func(t *testing.T) {
		start := time.Now()
		_, err := execWithTimeoutCleanupNoise("sh",
			[]string{"-c", `sleep 30`}, 200*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("expected timeout error, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("timeout took too long: %v", elapsed)
		}
	})
}
