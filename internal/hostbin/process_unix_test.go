//go:build unix

package hostbin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartStopAlive(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "sleep.pid")
	logFile := filepath.Join(dir, "sleep.log")

	if Alive(pidFile) {
		t.Fatal("Alive on missing pid file should be false")
	}
	if err := Stop(pidFile, time.Second); err != nil {
		t.Fatalf("Stop on missing pid file: %v", err)
	}

	// sleep ignores TERM quickly; give grace enough to observe the kill path.
	if err := StartDetached("/bin/sleep", []string{"30"}, logFile, pidFile); err != nil {
		t.Fatal(err)
	}
	if !Alive(pidFile) {
		data, _ := os.ReadFile(pidFile)
		t.Fatalf("not alive right after start, pid file %q", data)
	}
	// Double-start guard is the caller's job, but Stop must be idempotent.
	if err := Stop(pidFile, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for Alive(pidFile) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if Alive(pidFile) {
		t.Fatal("process still alive after Stop")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("pid file should be removed by Stop")
	}
}

func TestAliveGuardsPIDReuse(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "x.pid")
	// Record our own pid but a binary we are not running.
	if err := writePID(pidFile, "/definitely-not-me", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if Alive(pidFile) {
		t.Error("cmdline mismatch should report not-alive")
	}
	if err := writePID(pidFile, os.Args[0], os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if !Alive(pidFile) {
		t.Error("self should be alive under its own argv[0]")
	}
}

func TestMergeEnv(t *testing.T) {
	base := []string{"PATH=/bin", "FOO=base", "BAR=keep"}
	override := []string{"FOO=override", "NEW=1"}
	got := mergeEnv(base, override)
	want := []string{"PATH=/bin", "BAR=keep", "FOO=override", "NEW=1"}
	if len(got) != len(want) {
		t.Fatalf("mergeEnv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mergeEnv = %v, want %v", got, want)
		}
	}
}

func TestStartDetachedEnvOverridesEnvironment(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "env.pid")
	logFile := filepath.Join(dir, "env.log")
	t.Setenv("PGCLI_HOSTBIN_TEST", "base")

	if err := StartDetachedEnv("/bin/sh", []string{"-c", `printf '%s' "$PGCLI_HOSTBIN_TEST"`},
		[]string{"PGCLI_HOSTBIN_TEST=override"}, logFile, pidFile); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logFile)
		if err == nil && len(data) > 0 {
			if string(data) != "override" {
				t.Fatalf("child observed %q, want override", data)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child produced no output")
}
