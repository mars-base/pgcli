//go:build unix

package hostbin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// StartDetached launches bin with args in a new session (setsid) so it
// survives pgcli exiting, with all stdio appended to logFile, and records the
// pid in pidFile.
func StartDetached(bin string, args []string, logFile, pidFile string) error {
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return fmt.Errorf("hostbin: log dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		return fmt.Errorf("hostbin: pid dir: %w", err)
	}
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("hostbin: opening log %s: %w", logFile, err)
	}
	defer lf.Close()

	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("hostbin: starting %s: %w", bin, err)
	}
	pid := cmd.Process.Pid
	// Detach: the child outlives us; no Wait, no zombie concern past exit.
	_ = cmd.Process.Release()

	if err := writePID(pidFile, bin, pid); err != nil {
		return err
	}
	return nil
}

// Stop signals the recorded process (via its process group, so a daemon that
// forked a worker like juicefs mount -d does goes down too): TERM, wait up
// to grace, then KILL. The pid file is removed either way.
func Stop(pidFile string, grace time.Duration) error {
	pid, _, err := readPID(pidFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		os.Remove(pidFile)
		return nil
	}
	// Negative pid signals the whole group; fall back to the bare pid if the
	// group is already gone.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if err := p.Signal(syscall.SIGTERM); err != nil && !isESRCH(err) {
			return fmt.Errorf("hostbin: signaling %d: %w", pid, err)
		}
	}
	deadline := time.Now().Add(grace)
	for Alive(pidFile) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if Alive(pidFile) {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = p.Kill()
	}
	os.Remove(pidFile)
	return nil
}

// Alive reports whether the pid recorded in pidFile still runs the recorded
// binary (cmdline match guards against pid reuse).
func Alive(pidFile string) bool {
	pid, bin, err := readPID(pidFile)
	if err != nil {
		return false
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
		// Non-Linux unix (or dead pid): fall back to signal 0.
		p, err := os.FindProcess(pid)
		if err != nil {
			return false
		}
		if err := p.Signal(syscall.Signal(0)); err != nil {
			return false
		}
		return true
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	fields := strings.FieldsFunc(string(cmdline), func(r rune) bool { return r == 0 })
	if len(fields) == 0 {
		return false
	}
	return filepath.Base(fields[0]) == filepath.Base(bin)
}

func writePID(pidFile, bin string, pid int) error {
	tmp := pidFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"+bin+"\n"), 0o644); err != nil {
		return fmt.Errorf("hostbin: writing %s: %w", pidFile, err)
	}
	return os.Rename(tmp, pidFile)
}

// readPID parses "<pid>\n<binary>\n".
func readPID(pidFile string) (int, string, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, "", err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return 0, "", fmt.Errorf("hostbin: malformed pid file %s", pidFile)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, "", fmt.Errorf("hostbin: pid file %s: %w", pidFile, err)
	}
	return pid, lines[1], nil
}

func isESRCH(err error) bool { return err == syscall.ESRCH }
