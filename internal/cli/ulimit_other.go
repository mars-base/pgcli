//go:build !unix

package cli

// raiseOpenFileLimit is a no-op on platforms without RLIMIT_NOFILE; the host
// FUSE addons (juicefs, zerofs) are Linux-only, so this never runs there.
func raiseOpenFileLimit() (int, bool) { return 0, false }
