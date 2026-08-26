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
func execWithTimeoutWriter(podmanPath string, args []string, timeout time.Duration, w io.Writer) (string, error) {
	slog.Debug("execWithTimeoutWriter", "args", args)
	cmd := podmanCommand(podmanPath, args...)
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
func execWithTimeoutStreaming(podmanPath string, args []string, timeout time.Duration) (string, error) {
	slog.Debug("execWithTimeoutStreaming", "args", args)
	cmd := podmanCommand(podmanPath, args...)
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
// cmd.Output()'s prefixSuffixSaver can cause pipe hangs with podman exec.
// Explicitly kills the process on timeout.
func execWithTimeout(podmanPath string, args []string, timeout time.Duration) (string, error) {
	slog.Debug("execWithTimeout", "args", args)
	cmd := podmanCommand(podmanPath, args...)
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
