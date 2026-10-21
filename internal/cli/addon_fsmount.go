package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// hostAddonDir rebuilds a host-binary addon's bookkeeping dir
// (<base>/addon/<family>/<name>: pid files, logs, cache, config.toml). It is
// derived from the base dir, never from the mount point — the mount point is
// operator-chosen and `mount --mount-point` can move it anywhere, so the
// pgcli-owned paths would be mis-parented by filepath.Dir(mount-point).
// Mirrors the defaulting in config.ApplyDefaults.
func hostAddonDir(c *config.Config, family, name string) string {
	base := c.BaseDir
	if base == "" {
		base = platform.DefaultConfigDir()
	}
	return filepath.Join(base, "addon", family, name)
}

// checkMountPoint enforces the mount-point contract of the filesystem addons:
// the operator creates the directory themselves (mkdir -p) at a path the user
// running pg can read, write and traverse — pgcli never creates or chowns it.
// A missing or unwritable target fails here, before any format or daemon
// starts, with the exact remedy.
func checkMountPoint(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("mount point %q is not an absolute path", dir)
	}
	st, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return fmt.Errorf("mount point %s does not exist — create it first: mkdir -p %s (owned by, and writable for, the user running pg)", dir, dir)
	case err != nil:
		return fmt.Errorf("mount point %s: %w", dir, err)
	case !st.IsDir():
		return fmt.Errorf("mount point %s exists but is not a directory", dir)
	}
	// The real permission check: the FUSE daemon must be able to create
	// entries here — a mode/ownership failure must surface as a clear pgcli
	// error, not a cryptic mount exit.
	probe := filepath.Join(dir, ".pgcli-mount-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("mount point %s is not writable by the user running pg (mode %v) — fix ownership/permissions or pick another path: %w", dir, st.Mode().Perm(), err)
	}
	f.Close()
	os.Remove(probe)
	return nil
}
