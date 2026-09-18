//go:build unix

package cli

import (
	"os"
	"syscall"
)

// stopSignal is the termination signal paired with SIGINT for killing the log
// tail before the process dies.
var stopSignal = syscall.SIGTERM

// killSelf re-raises sig with the default disposition after the signal handler
// was detached, so the process exits with the status the shell expects (e.g.
// 130 for SIGINT) instead of a clean exit that would mask the interrupt.
func killSelf(sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		_ = syscall.Kill(syscall.Getpid(), s)
	}
}
