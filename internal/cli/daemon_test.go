package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/daemon"
)

func executePG(t *testing.T, args ...string) error {
	t.Helper()
	rootCmd.SetArgs(args)
	return rootCmd.Execute()
}

func TestDaemonAddRequiresDoubleDash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	err := executePG(t, "-c", path, "daemon", "add", "hook", "/bin/true")
	if err == nil || !strings.Contains(err.Error(), `"--"`) {
		t.Fatalf("error = %v, want a -- separator hint", err)
	}
}

func TestDaemonAddValidatesName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	err := executePG(t, "-c", path, "daemon", "add", "bad.name", "--", "/bin/true")
	if err == nil || !strings.Contains(err.Error(), "invalid daemon name") {
		t.Fatalf("error = %v, want invalid-name rejection", err)
	}
}

func TestDaemonAddPersistsAndForcePreservesAutostart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	if err := executePG(t, "-c", path, "daemon", "add", "hook", "--dir", dir, "--", "/bin/true", "arg1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	dc, ok := loaded.Daemons["hook"]
	if !ok {
		t.Fatalf("daemon not persisted: %+v", loaded.Daemons)
	}
	if dc.Command[0] != "/bin/true" || len(dc.Command) != 2 || dc.Command[1] != "arg1" {
		t.Errorf("Command = %v, want absolute binary and preserved args", dc.Command)
	}
	if dc.Dir != dir {
		t.Errorf("Dir = %q, want %q", dc.Dir, dir)
	}

	err = executePG(t, "-c", path, "daemon", "add", "hook", "--dir", dir, "--force=false", "--", "/bin/true")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate add error = %v", err)
	}

	loaded.Daemons["hook"] = config.DaemonConfig{
		Command: dc.Command, Dir: dc.Dir, Autostart: true,
	}
	if err := loaded.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := executePG(t, "-c", path, "daemon", "add", "hook", "--dir", dir, "--force", "--", "/bin/echo"); err != nil {
		t.Fatalf("force add: %v", err)
	}
	loaded, err = config.Load(path)
	if err != nil {
		t.Fatalf("Load after force: %v", err)
	}
	dc = loaded.Daemons["hook"]
	if dc.Command[0] != "/bin/echo" {
		t.Errorf("force did not replace the command: %v", dc.Command)
	}
	if !dc.Autostart {
		t.Error("force replaced the daemon but dropped Autostart")
	}
}

func TestDaemonAddForceRejectsRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")
	c := config.Default()
	c.BaseDir = dir
	c.Daemons = map[string]config.DaemonConfig{
		"sleeper": {Command: []string{"/bin/sh", "-c", "sleep 30"}, Dir: dir},
	}
	if err := c.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := daemon.Start(c, "", "sleeper"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Stop(c, "sleeper", 5*time.Second) })

	err := executePG(t, "-c", path, "daemon", "add", "sleeper", "--dir", dir, "--force", "--", "/bin/true")
	if err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("error = %v, want running-daemon rejection", err)
	}
}

func TestDaemonAddLogFileRelativeToDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	if err := executePG(t, "-c", path, "daemon", "add", "hook",
		"--dir", dir, "--log-file", "logs/hook.log", "--", "/bin/true"); err != nil {
		t.Fatalf("add: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "logs", "hook.log")
	if got := loaded.Daemons["hook"].LogFile; got != want {
		t.Errorf("LogFile = %q, want %q resolved against --dir", got, want)
	}
}

// resetDaemonAddFlags clears values and pflag Changed marks that otherwise
// leak across executePG calls in this process.
func resetDaemonAddFlags() {
	daemonAddDir = ""
	daemonAddLogFile = ""
	daemonAddLogMaxSize = ""
	daemonAddLogDaily = false
	daemonAddLogKeep = 7
	daemonAddForce = false
	for _, name := range []string{"log-max-size", "log-rotate-daily", "log-keep"} {
		if f := daemonAddCmd.Flags().Lookup(name); f != nil {
			f.Changed = false
		}
	}
}

func TestDaemonAddPersistsLogRotate(t *testing.T) {
	resetDaemonAddFlags()
	t.Cleanup(resetDaemonAddFlags)
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	if err := executePG(t, "-c", path, "daemon", "add", "hook", "--dir", dir,
		"--log-max-size", "10M", "--log-rotate-daily", "--log-keep", "3",
		"--", "/bin/true"); err != nil {
		t.Fatalf("add: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := loaded.Daemons["hook"].LogRotate
	if r == nil || r.MaxSize != 10<<20 || !r.Daily || r.Keep != 3 {
		t.Fatalf("LogRotate = %+v, want size 10M + daily, keep 3", r)
	}
}

func TestDaemonAddWithoutRotationOmitsLogRotate(t *testing.T) {
	resetDaemonAddFlags()
	t.Cleanup(resetDaemonAddFlags)
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	if err := executePG(t, "-c", path, "daemon", "add", "hook", "--dir", dir, "--", "/bin/true"); err != nil {
		t.Fatalf("add: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r := loaded.Daemons["hook"].LogRotate; r != nil {
		t.Fatalf("LogRotate = %+v, want nil without rotation flags", r)
	}
}

func TestDaemonAddLogRotateValidation(t *testing.T) {
	resetDaemonAddFlags()
	t.Cleanup(resetDaemonAddFlags)
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--log-max-size", "banana"}, "invalid size"},
		{[]string{"--log-max-size", "1M", "--log-keep", "-1"}, "must be >= 0"},
		{[]string{"--log-keep", "3"}, "requires --log-max-size or --log-rotate-daily"},
	}
	for _, tc := range cases {
		resetDaemonAddFlags()
		args := append([]string{"-c", path, "daemon", "add", "hook", "--dir", dir}, tc.args...)
		args = append(args, "--", "/bin/true")
		err := executePG(t, args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("args %v: error = %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestAutostartDaemonRequiresName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	err := executePG(t, "-c", path, "autostart", "enable", "--daemon")
	if err == nil || !strings.Contains(err.Error(), "--daemon requires --name") {
		t.Fatalf("error = %v, want name requirement", err)
	}
}

func TestCountAutostartTargetsIncludesDaemons(t *testing.T) {
	c := config.Default()
	c.Backup.Autostart = false // Default() enables it; isolate the daemon count.
	c.Daemons = map[string]config.DaemonConfig{
		"hook": {Autostart: true},
		"off":  {},
	}
	if n := countAutostartTargets(c); n != 1 {
		t.Fatalf("countAutostartTargets = %d, want 1", n)
	}
}
