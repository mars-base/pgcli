// Package daemon manages long-running host processes declared in pg.yaml.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/hostbin"
	"github.com/mars-base/pgcli/internal/platform"
)

// Layout is the per-daemon state directory and its default files.
type Layout struct {
	StateDir      string
	LogFile       string
	PIDFile       string
	SupervisorLog string
}

// BaseDir returns the pgcli base directory (config BaseDir, else the platform
// default) — the same resolution used by the SQLite addon.
func BaseDir(c *config.Config) string {
	if c.BaseDir != "" {
		return c.BaseDir
	}
	return platform.DefaultConfigDir()
}

// NewLayout returns the state paths for a daemon.
func NewLayout(c *config.Config, name string) Layout {
	state := filepath.Join(BaseDir(c), "daemon", name)
	return Layout{
		StateDir:      state,
		LogFile:       filepath.Join(state, "daemon.log"),
		PIDFile:       filepath.Join(state, "daemon.pid"),
		SupervisorLog: filepath.Join(state, "supervisor.log"),
	}
}

// LogFile returns the effective log path (custom --log-file or the default).
func LogFile(c *config.Config, name string, dc config.DaemonConfig) string {
	if dc.LogFile != "" {
		return dc.LogFile
	}
	return NewLayout(c, name).LogFile
}

// LogRotateSummary renders rotation settings for status output.
func LogRotateSummary(r *config.DaemonLogRotate) string {
	if !r.Enabled() {
		return "off"
	}
	var parts []string
	if r.MaxSize > 0 {
		parts = append(parts, "size "+humanMegabytes(r.MaxSize))
	}
	if r.Daily {
		parts = append(parts, "daily")
	}
	keep := "all"
	if r.Keep > 0 {
		keep = strconv.Itoa(r.Keep)
	}
	return fmt.Sprintf("%s (keep %s)", strings.Join(parts, " + "), keep)
}

// humanMegabytes rounds up like the lumberjack MaxSize conversion, so the
// display matches when rotation actually triggers.
func humanMegabytes(n int64) string {
	mb := (n + megabyte - 1) / megabyte
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

// PIDFile returns the pid file path for a daemon.
func PIDFile(c *config.Config, name string) string {
	return NewLayout(c, name).PIDFile
}

// Running reports whether the daemon's recorded process is alive.
func Running(c *config.Config, name string) bool {
	return hostbin.Alive(PIDFile(c, name))
}

// PID returns the recorded pid when the process is alive, else 0.
func PID(c *config.Config, name string) int {
	pidFile := PIDFile(c, name)
	if !hostbin.Alive(pidFile) {
		return 0
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	line, _, _ := strings.Cut(string(data), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return 0
	}
	return pid
}

// Start launches the daemon if it is not already running. already is true when
// the process was left untouched. cfgPath is the config file the supervisor
// re-reads; it is only required when log rotation is enabled.
func Start(c *config.Config, cfgPath, name string) (already bool, err error) {
	dc, ok := c.Daemons[name]
	if !ok {
		return false, fmt.Errorf("daemon %q not found in config", name)
	}
	if len(dc.Command) == 0 {
		return false, fmt.Errorf("daemon %q has no command", name)
	}
	pidFile := PIDFile(c, name)
	if hostbin.Alive(pidFile) {
		return true, nil
	}

	logFile := LogFile(c, name, dc)
	if dc.LogRotate.Enabled() {
		err = startSupervised(c, cfgPath, name)
	} else {
		err = hostbin.StartDetachedSpec(hostbin.Spec{
			Bin:     dc.Command[0],
			Args:    dc.Command[1:],
			Dir:     dc.Dir,
			LogFile: logFile,
			PIDFile: pidFile,
		})
	}
	if err != nil {
		return false, err
	}

	// Catch the common failure (bad interpreter, import error, port in use)
	// instead of reporting success for a process that is already gone.
	time.Sleep(500 * time.Millisecond)
	if !hostbin.Alive(pidFile) {
		msg := fmt.Sprintf("daemon %q exited immediately", name)
		if tail := tailLines(logFile, 20); tail != "" {
			msg += "\n" + tail
		}
		return false, fmt.Errorf("%s", msg)
	}
	return false, nil
}

// startSupervised detaches `pg daemon __supervise <name>` in place of the
// daemon command so rotation keeps working after pgcli exits. The pid file
// records the supervisor; MatchArgs keeps pid-reuse checks from accepting an
// unrelated pg process.
func startSupervised(c *config.Config, cfgPath, name string) error {
	if cfgPath == "" {
		return fmt.Errorf("daemon %q: config path required for log rotation", name)
	}
	absCfg, err := filepath.Abs(cfgPath)
	if err != nil {
		return fmt.Errorf("resolving config path: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating pg executable: %w", err)
	}
	layout := NewLayout(c, name)
	return hostbin.StartDetachedSpec(hostbin.Spec{
		Bin:       self,
		Args:      []string{"-c", absCfg, "daemon", "__supervise", name},
		LogFile:   layout.SupervisorLog,
		PIDFile:   layout.PIDFile,
		MatchArgs: []string{"daemon", "__supervise", name},
	})
}

// Stop terminates the daemon's process group, waiting up to grace before
// killing it. It is a no-op when no process is recorded.
func Stop(c *config.Config, name string, grace time.Duration) error {
	return hostbin.Stop(PIDFile(c, name), grace)
}

// CommandLine renders the configured command for display.
func CommandLine(dc config.DaemonConfig) string {
	return strings.Join(dc.Command, " ")
}

// tailLines returns the last n lines of path, prefixed for an error message.
// A missing or unreadable file yields an empty string.
func tailLines(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- log tail ---")
	for _, line := range lines {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	return b.String()
}
