//go:build unix

package hostbin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Spec describes a detached process launch.
type Spec struct {
	Bin     string
	Args    []string
	Env     []string
	Dir     string
	LogFile string
	PIDFile string
	// MatchArgs are extra argv fields recorded in the pid file and required by
	// Alive, so a recycled pid running an unrelated instance of the same binary
	// is not mistaken for this process.
	MatchArgs []string
}

// StartDetached launches bin with args in a new session (setsid) so it
// survives pgcli exiting, with all stdio appended to logFile, and records the
// pid in pidFile.
func StartDetached(bin string, args []string, logFile, pidFile string) error {
	return StartDetachedSpec(Spec{Bin: bin, Args: args, LogFile: logFile, PIDFile: pidFile})
}

// StartDetachedEnv is StartDetached with extra environment entries. Values in
// extraEnv override the inherited environment (e.g. SSL_CERT_FILE).
func StartDetachedEnv(bin string, args, extraEnv []string, logFile, pidFile string) error {
	return StartDetachedSpec(Spec{Bin: bin, Args: args, Env: extraEnv, LogFile: logFile, PIDFile: pidFile})
}

// StartDetachedSpec launches a detached process with an optional working
// directory and environment.
func StartDetachedSpec(s Spec) error {
	if err := os.MkdirAll(filepath.Dir(s.LogFile), 0o755); err != nil {
		return fmt.Errorf("hostbin: log dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.PIDFile), 0o755); err != nil {
		return fmt.Errorf("hostbin: pid dir: %w", err)
	}
	lf, err := os.OpenFile(s.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("hostbin: opening log %s: %w", s.LogFile, err)
	}
	defer lf.Close()

	cmd := exec.Command(s.Bin, s.Args...)
	cmd.Dir = s.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Env = mergeEnv(os.Environ(), append([]string{"NO_COLOR=1"}, s.Env...))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("hostbin: starting %s: %w", s.Bin, err)
	}
	pid := cmd.Process.Pid
	// Detach: the child outlives us; no Wait, no zombie concern past exit.
	_ = cmd.Process.Release()

	if err := writePID(s.PIDFile, s.Bin, pid, s.MatchArgs); err != nil {
		return err
	}
	return nil
}

// mergeEnv overlays override onto base: keys set in override replace the
// inherited value and new keys are appended, so the caller always wins.
func mergeEnv(base, override []string) []string {
	keys := make(map[string]bool, len(override))
	for _, kv := range override {
		if i := strings.IndexByte(kv, '='); i > 0 {
			keys[kv[:i]] = true
		}
	}
	merged := make([]string, 0, len(base)+len(override))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 && keys[kv[:i]] {
			continue
		}
		merged = append(merged, kv)
	}
	return append(merged, override...)
}

// Stop signals the recorded process (via its process group, so a daemon that
// forked a worker like juicefs mount -d does goes down too): TERM, wait up
// to grace, then KILL. The pid file is removed either way.
func Stop(pidFile string, grace time.Duration) error {
	pid, _, _, err := readPID(pidFile)
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
		if err := p.Signal(syscall.SIGTERM); err != nil && !processGone(err) {
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
	pid, bin, matchArgs, err := readPID(pidFile)
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
	// exec.Command launches the binary by its absolute path, so an exact argv
	// match is the strong signal. Scripts appear as an interpreter followed by
	// the script path (fields[1] for #!/bin/sh, later for #!/usr/bin/env), so
	// compare every field against the recorded path. The argv[0] basename
	// fallback keeps compiled binaries working when they rewrite argv[0].
	base := filepath.Base(bin)
	matchedBin := false
	for i, field := range fields {
		if field == bin || (i == 0 && filepath.Base(field) == base) {
			matchedBin = true
			break
		}
	}
	if !matchedBin || !containsAll(fields, matchArgs) {
		return false
	}
	return true
}

// containsAll reports whether every want appears as an argv field.
func containsAll(fields, want []string) bool {
	for _, w := range want {
		if !slices.Contains(fields, w) {
			return false
		}
	}
	return true
}

func writePID(pidFile, bin string, pid int, matchArgs []string) error {
	var b strings.Builder
	b.WriteString(strconv.Itoa(pid))
	b.WriteString("\n")
	b.WriteString(bin)
	b.WriteString("\n")
	for _, arg := range matchArgs {
		b.WriteString(arg)
		b.WriteString("\n")
	}
	tmp := pidFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("hostbin: writing %s: %w", pidFile, err)
	}
	return os.Rename(tmp, pidFile)
}

// readPID parses "<pid>\n<binary>\n[<match-arg>\n...]".
func readPID(pidFile string) (int, string, []string, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, "", nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return 0, "", nil, fmt.Errorf("hostbin: malformed pid file %s", pidFile)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, "", nil, fmt.Errorf("hostbin: pid file %s: %w", pidFile, err)
	}
	matchArgs := make([]string, 0, len(lines)-2)
	for _, line := range lines[2:] {
		if line = strings.TrimSpace(line); line != "" {
			matchArgs = append(matchArgs, line)
		}
	}
	return pid, lines[1], matchArgs, nil
}

// processGone reports whether err means the target no longer exists:
// kill(2)'s ESRCH, or os.ErrProcessDone from os.Process.Signal.
func processGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}
