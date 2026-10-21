package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestCheckMountPoint(t *testing.T) {
	dir := t.TempDir()
	if err := checkMountPoint(dir); err != nil {
		t.Fatalf("a fresh writable tempdir should pass, got: %v", err)
	}
	// No probe file may survive the check.
	if _, err := os.Stat(filepath.Join(dir, ".pgcli-mount-probe")); !os.IsNotExist(err) {
		t.Fatal("the write probe was not cleaned up")
	}
}

func TestCheckMountPointErrors(t *testing.T) {
	t.Run("relative", func(t *testing.T) {
		err := checkMountPoint("mnt/relative")
		if err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("want absolute-path rejection, got %v", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		err := checkMountPoint(filepath.Join(t.TempDir(), "nope"))
		if err == nil || !strings.Contains(err.Error(), "mkdir -p") {
			t.Fatalf("want the mkdir -p remedy, got %v", err)
		}
	})
	t.Run("not-a-dir", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		err := checkMountPoint(f)
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("want not-a-directory rejection, got %v", err)
		}
	})
	t.Run("unwritable", func(t *testing.T) {
		// Skipped for root, which ignores mode bits.
		if os.Geteuid() == 0 {
			t.Skip("running as root — permission bits do not apply")
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		err := checkMountPoint(dir)
		if err == nil || !strings.Contains(err.Error(), "not writable") {
			t.Fatalf("want not-writable rejection, got %v", err)
		}
	})
}

func TestHostAddonDir(t *testing.T) {
	t.Run("explicit-base", func(t *testing.T) {
		got := hostAddonDir(&config.Config{BaseDir: "/srv/pgcli"}, "juicefs", "data")
		if want := filepath.Join("/srv/pgcli", "addon", "juicefs", "data"); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}
