package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	c := config.Default()
	c.BaseDir = t.TempDir()
	return c
}

func TestStartStopRunning(t *testing.T) {
	c := testConfig(t)
	c.Daemons = map[string]config.DaemonConfig{
		"sleep": {Command: []string{"/bin/sh", "-c", "sleep 30"}, Dir: t.TempDir()},
	}

	already, err := Start(c, "sleep")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if already {
		t.Fatal("first Start reported already running")
	}
	if !Running(c, "sleep") {
		t.Fatal("daemon should be running")
	}
	if pid := PID(c, "sleep"); pid <= 0 {
		t.Fatalf("PID = %d", pid)
	}

	already, err = Start(c, "sleep")
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if !already {
		t.Fatal("second Start should be idempotent")
	}

	if err := Stop(c, "sleep", 5*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if Running(c, "sleep") {
		t.Fatal("daemon should be stopped")
	}
}

func TestStartImmediateExitReportsLogTail(t *testing.T) {
	c := testConfig(t)
	c.Daemons = map[string]config.DaemonConfig{
		"boom": {Command: []string{"/bin/sh", "-c", "echo boom; exit 1"}},
	}

	_, err := Start(c, "boom")
	if err == nil {
		t.Fatal("Start should fail when the process exits immediately")
	}
	msg := err.Error()
	if !strings.Contains(msg, "exited immediately") || !strings.Contains(msg, "boom") {
		t.Fatalf("error = %q, want exit notice and log tail", msg)
	}
}

func TestStartRejectsEmptyCommand(t *testing.T) {
	c := testConfig(t)
	c.Daemons = map[string]config.DaemonConfig{"empty": {}}
	if _, err := Start(c, "empty"); err == nil {
		t.Fatal("Start should reject an empty command")
	}
	if _, err := Start(c, "missing"); err == nil {
		t.Fatal("Start should reject an unknown daemon")
	}
}

func TestLayoutAndCustomLogFile(t *testing.T) {
	c := testConfig(t)
	c.Daemons = map[string]config.DaemonConfig{"hook": {}}

	layout := NewLayout(c, "hook")
	wantDir := filepath.Join(c.BaseDir, "daemon", "hook")
	if layout.StateDir != wantDir {
		t.Errorf("StateDir = %q, want %q", layout.StateDir, wantDir)
	}
	if LogFile(c, "hook", c.Daemons["hook"]) != layout.LogFile {
		t.Error("default log file should come from the layout")
	}

	dc := config.DaemonConfig{LogFile: "/var/log/hook.log"}
	if got := LogFile(c, "hook", dc); got != "/var/log/hook.log" {
		t.Errorf("LogFile = %q, want custom path", got)
	}
	if PIDFile(c, "hook") != layout.PIDFile {
		t.Error("PIDFile should come from the layout")
	}
}

func TestCommandLine(t *testing.T) {
	dc := config.DaemonConfig{Command: []string{"/bin/uvicorn", "app:app", "--port", "18801"}}
	if got := CommandLine(dc); got != "/bin/uvicorn app:app --port 18801" {
		t.Errorf("CommandLine = %q", got)
	}
}

func TestTailLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("line\n")
	}
	b.WriteString("last line\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	got := tailLines(path, 2)
	if !strings.Contains(got, "last line") || strings.Count(got, "\n") != 2 {
		t.Errorf("tailLines = %q", got)
	}
	if got := tailLines(filepath.Join(t.TempDir(), "missing"), 5); got != "" {
		t.Errorf("missing file tail = %q, want empty", got)
	}
}
