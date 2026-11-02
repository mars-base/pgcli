package daemon

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// Supervise runs the daemon command in the foreground with its combined
// stdout/stderr forwarded to a rotating log. It is spawned by Start as
// `pg daemon __supervise <name>` when log rotation is enabled, because a
// rotation decision needs a live process after pgcli exits.
//
// The child stays in the supervisor's process group (no Setsid): `pg daemon
// stop` signals the group recorded in the pid file, covering both processes.
func Supervise(c *config.Config, name string) error {
	dc, ok := c.Daemons[name]
	if !ok {
		return fmt.Errorf("daemon %q not found in config", name)
	}
	if len(dc.Command) == 0 {
		return fmt.Errorf("daemon %q has no command", name)
	}
	if !dc.LogRotate.Enabled() {
		return fmt.Errorf("daemon %q has no log rotation configured", name)
	}

	logPath := LogFile(c, name, dc)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("daemon: log dir: %w", err)
	}
	w := newRotatingWriter(logPath, dc.LogRotate, time.Now)
	defer w.Close()

	cmd := exec.Command(dc.Command[0], dc.Command[1:]...)
	cmd.Dir = dc.Dir
	cmd.Env = os.Environ()

	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("daemon: log pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		fmt.Fprintf(w, "pg daemon: starting %s: %v\n", dc.Command[0], err)
		return fmt.Errorf("daemon: starting %s: %w", dc.Command[0], err)
	}
	pw.Close()

	// Forward termination signals so a TERM aimed at the supervisor reaches
	// the child too. Never return early on a signal: Stop polls the supervisor
	// pid and only escalates to SIGKILL while it is still alive, so staying put
	// lets a TERM-ignoring child be reaped by that escalation.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigCh:
				_ = cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()

	_, copyErr := io.Copy(w, pr)
	pr.Close()
	waitErr := cmd.Wait()
	close(done)
	signal.Stop(sigCh)

	if copyErr != nil {
		return fmt.Errorf("daemon: copying %s output: %w", name, copyErr)
	}
	return waitErr
}
