//go:build unix

package cli

import "syscall"

// raiseOpenFileLimit lifts this process's RLIMIT_NOFILE soft limit towards its
// hard cap (targeting 65536). ZeroFS's on-disk cache engine opens thousands of
// files at startup; the customary 1024 soft limit makes the server die with
// "No file descriptors available" (seen live on vm01). Returns the new soft
// limit and whether the target was reached; best-effort either way — zerofs
// reports its own failure.
//
// The Setrlimit call is unconditional on purpose: Go's syscall init raises the
// limit to hard-1 for itself but caches the original (e.g. 1024) and restores
// it in every exec'd child unless the program calls Setrlimit itself — so a
// detached zerofs would come up at 1024 even though our own /proc/self/limits
// already reads hard-1.
func raiseOpenFileLimit() (int, bool) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return 0, false
	}
	const target uint64 = 65536
	want := target
	if want > lim.Max {
		want = lim.Max
	}
	if lim.Cur < want {
		lim.Cur = want
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return int(lim.Cur), false
	}
	return int(lim.Cur), lim.Cur >= target
}
