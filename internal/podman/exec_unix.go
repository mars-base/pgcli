//go:build !windows

package podman

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// execWithTimeoutWriter runs podman streaming stdout/stderr to both w and
// internal buffers.  If w is nil only the internal buffers are used.
// When args[0] is "exec" and the command fails with a cgroup permission error,
// automatically retries via systemd-run --user --scope.
func execWithTimeoutWriter(podmanPath string, args []string, timeout time.Duration, w io.Writer) (string, error) {
	slog.Debug("execWithTimeoutWriter", "args", args)
	out, err := execWithTimeoutWriterRaw(podmanPath, args, timeout, w)
	if err != nil && len(args) > 0 && args[0] == "exec" && isCgroupPermErr(err) {
		slog.Debug("cgroup fallback via systemd-run (execWithTimeoutWriter)")
		out, err = execWithTimeoutWriterRaw("systemd-run", append([]string{"--user", "--scope", "--quiet", podmanPath}, args...), timeout, w)
	}
	return out, err
}

func execWithTimeoutWriterRaw(binary string, args []string, timeout time.Duration, w io.Writer) (string, error) {
	cmd := podmanCommand(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdoutBuf, stderrBuf strings.Builder
	if w != nil {
		cmd.Stdout = io.MultiWriter(w, &stdoutBuf)
		cmd.Stderr = io.MultiWriter(w, &stderrBuf)
	} else {
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	}

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)

	go func() {
		err := cmd.Run()
		out := stdoutBuf.String()
		if err != nil {
			if isPodmanCleanupNoise(stderrBuf.String()) && strings.Contains(out, "completed successfully") {
				done <- result{out, nil}
				return
			}
			if _, ok := err.(*exec.ExitError); ok {
				errMsg := stderrBuf.String()
				if errMsg == "" {
					errMsg = out
				}
				done <- result{"", fmt.Errorf("podman %s: %s", strings.Join(args, " "), errMsg)}
				return
			}
			done <- result{"", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)}
			return
		}
		done <- result{out, nil}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return "", r.err
		}
		return r.out, nil
	case <-time.After(timeout):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return "", fmt.Errorf("podman %s timed out after %v", strings.Join(args, " "), timeout)
	}
}

// execWithTimeoutStreaming runs podman while also copying stdout/stderr to the
// process stdout/stderr so logs are visible in real time. A copy of the output
// is still returned for error reporting.
// When args[0] is "exec" and the command fails with a cgroup permission error,
// automatically retries via systemd-run --user --scope.
func execWithTimeoutStreaming(podmanPath string, args []string, timeout time.Duration) (string, error) {
	slog.Debug("execWithTimeoutStreaming", "args", args)
	out, err := execWithTimeoutStreamingRaw(podmanPath, args, timeout)
	if err != nil && len(args) > 0 && args[0] == "exec" && isCgroupPermErr(err) {
		slog.Debug("cgroup fallback via systemd-run (execWithTimeoutStreaming)")
		out, err = execWithTimeoutStreamingRaw("systemd-run", append([]string{"--user", "--scope", "--quiet", podmanPath}, args...), timeout)
	}
	return out, err
}

func execWithTimeoutStreamingRaw(binary string, args []string, timeout time.Duration) (string, error) {
	cmd := podmanCommand(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)

	go func() {
		err := cmd.Run()
		out := stdoutBuf.String()
		if err != nil {
			// Podman may report a non-zero exit after `podman run --rm` because it
			// tries to forward a terminal signal (e.g. SIGWINCH) to a container
			// that has already been removed. If the actual command succeeded,
			// treat this as success.
			if isPodmanCleanupNoise(stderrBuf.String()) && strings.Contains(out, "completed successfully") {
				done <- result{out, nil}
				return
			}
			if _, ok := err.(*exec.ExitError); ok {
				errMsg := stderrBuf.String()
				if errMsg == "" {
					errMsg = out
				}
				done <- result{"", fmt.Errorf("podman %s: %s", strings.Join(args, " "), errMsg)}
				return
			}
			done <- result{"", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)}
			return
		}
		done <- result{out, nil}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return "", r.err
		}
		return r.out, nil
	case <-time.After(timeout):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return "", fmt.Errorf("podman %s timed out after %v", strings.Join(args, " "), timeout)
	}
}

// execWithTimeout runs podman with a goroutine+channel timeout.
// Uses cmd.Run() with explicit buffers instead of cmd.Output() because
// execWithTimeout runs a podman command with a timeout.
// When args[0] is "exec" and the command fails with a cgroup permission error,
// automatically retries via systemd-run --user --scope.
func execWithTimeout(podmanPath string, args []string, timeout time.Duration) (string, error) {
	slog.Debug("execWithTimeout", "args", args)
	out, err := execWithTimeoutRaw(podmanPath, args, timeout)
	if err != nil && len(args) > 0 && args[0] == "exec" && isCgroupPermErr(err) {
		slog.Debug("cgroup fallback via systemd-run (execWithTimeout)")
		out, err = execWithTimeoutRaw("systemd-run", append([]string{"--user", "--scope", "--quiet", podmanPath}, args...), timeout)
	}
	return out, err
}

func execWithTimeoutRaw(binary string, args []string, timeout time.Duration) (string, error) {
	cmd := podmanCommand(binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)

	go func() {
		err := cmd.Run()
		out := stdout.String()
		if err != nil {
			if _, ok := err.(*exec.ExitError); ok {
				errMsg := stderr.String()
				if errMsg == "" {
					errMsg = out
				}
				done <- result{"", fmt.Errorf("podman %s: %s", strings.Join(args, " "), errMsg)}
				return
			}
			done <- result{"", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)}
			return
		}
		done <- result{out, nil}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return "", r.err
		}
		return r.out, nil
	case <-time.After(timeout):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return "", fmt.Errorf("podman %s timed out after %v", strings.Join(args, " "), timeout)
	}
}

// isCgroupPermErr checks if an error message contains cgroup.procs permission denied.
func isCgroupPermErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "cgroup.procs") && strings.Contains(msg, "Permission denied")
}
