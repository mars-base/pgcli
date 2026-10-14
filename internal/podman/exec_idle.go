package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrCLIHung marks the hard wall-clock cap firing in execIdleKilled (as
// opposed to the command failing on its own).
var ErrCLIHung = errors.New("CLI wedged without producing output (re-run the same command)")

// defaultIdleKill is how long execIdleKilled waits after the LAST byte of
// output before deciding the command is on its never-exit path. Healthy CLIs
// exit under a second after printing, so this only ever elapses for the wedge.
const defaultIdleKill = 3 * time.Second

// execIdleKilled runs `podman <args...>` capturing stdout+stderr (merged —
// useful messages live on both), capped by d.
//
// cmd.Output() cannot be used: once a cluster has peers, a peer dials back into
// the CLI's ephemeral RPC endpoint and the process never exits after printing
// (verified on garage v2.4.1: `timeout 20` still had to kill a fully printed
// `node id`). The exec session holds the pipe open, so Output() would burn the
// whole cap on a complete answer. Instead: watch the pipe, and when output has
// arrived and the stream then stays quiet for idleKill, SIGTERM the process
// group. podman forwards it into the container, the session ends, the pipe
// closes, and Wait returns — output preserved, error dropped because the kill
// was ours. Commands that FAIL print and exit non-zero on their own (verified:
// both `node connect` refused and a bad layout apply returned within seconds
// with rc≠0), so they are never idle-killed and their ExitError still reaches
// callers.
//
// The whole process group is the signal target everywhere here: podman
// re-execs itself, and killing only the direct child leaves an orphaned podman
// holding the pipe and squatting the container's exec session — the state-rot
// pattern seen on the VMs. cmd.WaitDelay escalates survivors to group SIGKILL.
//
// Extracted verbatim from GarageManager.garageExecOutput so SeaweedFS's exec
// path shares the same wedge protection; behaviour is unchanged for garage.
func execIdleKilled(podman string, d, idleKill time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := podmanCommandContext(ctx, podman, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second

	pr, pw, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	pw.Close() // our copy; the child holds its own until it dies

	var mu sync.Mutex
	var buf strings.Builder
	drained := make(chan struct{})
	chunk := make(chan struct{}, 1)
	go func() {
		defer close(drained)
		b := make([]byte, 4096)
		for {
			n, rerr := pr.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
				select {
				case chunk <- struct{}{}:
				default:
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	hard := time.NewTimer(d)
	defer hard.Stop()
	var idle *time.Timer
	var idleC <-chan time.Time
	killedEarly := false
	for {
		select {
		case werr := <-waited:
			pr.Close()
			<-drained
			mu.Lock()
			out := buf.String()
			mu.Unlock()
			switch {
			case werr == nil, killedEarly:
				return out, nil
			case ctx.Err() == context.DeadlineExceeded:
				return out, fmt.Errorf("podman %s: %w after %s", strings.Join(args, " "), ErrCLIHung, d)
			}
			if exitErr, ok := werr.(*exec.ExitError); ok {
				// run() surfaces merged output on failure; ExecCapture parses it.
				msg := string(exitErr.Stderr)
				if msg == "" {
					msg = out
				}
				return out, fmt.Errorf("podman %s: %s", strings.Join(args, " "), msg)
			}
			return out, fmt.Errorf("podman %s: %w", strings.Join(args, " "), werr)
		case <-hard.C:
			// Never went quiet even once: real wedge, end-of-story error.
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-chunk:
			if idle == nil {
				idle = time.NewTimer(idleKill)
				idleC = idle.C
			} else {
				idle.Reset(idleKill)
			}
		case <-idleC:
			killedEarly = true
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	}
}
