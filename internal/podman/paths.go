package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// hostMountPath returns the cleaned absolute host path for podman -v mounts.
func hostMountPath(hostPath string) string {
	abs, _ := filepath.Abs(hostPath)
	if abs == "" {
		return hostPath
	}
	return abs
}

// HostMountPath is the exported form, for callers that persist a user-supplied
// mount source already absolutized (the CLI records addon cert paths this way).
func HostMountPath(hostPath string) string {
	return hostMountPath(hostPath)
}

// snmdContainerPaths returns the literal in-container export paths MinIO/silo is
// handed in single-node multi-drive mode: one per drive, in order, /data1..dataN.
// They are passed as a plain argv slice (not a {1...N} brace range) because the
// run argv never goes through a shell, so braces would reach the server
// unexpanded; literal paths also make crash logs name the exact drive slot.
func snmdContainerPaths(n int) []string {
	paths := make([]string, n)
	for i := range paths {
		paths[i] = fmt.Sprintf("/data%d", i+1)
	}
	return paths
}

// storeMountsAndServerArgv is the single source of truth for how the minio and
// silo addons lay out their data mounts and `server ...` argv, so both stay in
// lockstep and the mode logic is testable without running podman. It takes the
// resolved host drive dirs (resolveDriveDirs output: the drive list in SNMD,
// otherwise the one data dir) plus the two mode flags. Three shapes:
//
//   - MNSD (endpoints non-empty): one /data mount (this node's single export
//     path) and `server <endpoint>...` — the endpoints name every node.
//   - SNMD (snmd true): one -v hostDirN:/dataN mount per drive and
//     `server /data1 /data2 ...` — MinIO erasure-codes across this node's disks.
//   - SNSD (neither): the plain `server /data` single mount.
func storeMountsAndServerArgv(driveDirs []string, endpoints []string, snmd bool) (mounts, serverArgs []string) {
	switch {
	case len(endpoints) > 0:
		mounts = []string{"-v", fmt.Sprintf("%s:/data:z", hostMountPath(driveDirs[0]))}
		serverArgs = append([]string{"server"}, endpoints...)
	case snmd:
		cp := snmdContainerPaths(len(driveDirs))
		for i, d := range driveDirs {
			mounts = append(mounts, "-v", fmt.Sprintf("%s:%s:z", hostMountPath(d), cp[i]))
		}
		serverArgs = append([]string{"server"}, cp...)
	default:
		mounts = []string{"-v", fmt.Sprintf("%s:/data:z", hostMountPath(driveDirs[0]))}
		serverArgs = []string{"server", "/data"}
	}
	return mounts, serverArgs
}

// sharesRootDevice reports whether path's nearest existing ancestor sits on the
// same block device as the host root filesystem. MinIO/silo reject a drive that
// is "part of root drive, will not be used" (an EC safety check against using the
// OS disk as a data volume). statNearestExisting walks up so a not-yet-created
// dir still resolves to the device an eventual mount would land on. Returns
// (false, nil) when device ids can't be compared on this platform.
func sharesRootDevice(path string) (bool, error) {
	root, err := os.Stat("/")
	if err != nil {
		return false, fmt.Errorf("stat root fs: %w", err)
	}
	data, err := statNearestExisting(path)
	if err != nil {
		return false, fmt.Errorf("stat dir %s: %w", path, err)
	}
	rd, ok1 := root.Sys().(*syscall.Stat_t)
	dd, ok2 := data.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false, nil
	}
	return rd.Dev == dd.Dev, nil
}

// isMountpoint reports whether path is itself a mount point, via the classic
// st_dev test: a directory is a mountpoint when its device id differs from its
// parent's (loop-mounted and real-disk mounts both change st_dev; a plain dir
// and a non-mount subdir do not). It guards --clean-data: os.RemoveAll walks
// *through* a live mount and would destroy data on the underlying disk, so a
// still-mounted drive must be refused, not deleted. Returns false when device
// ids can't be compared (non-Linux) so callers fail open to the delete.
func isMountpoint(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	parent, err := os.Stat(filepath.Dir(filepath.Clean(path)))
	if err != nil {
		return false
	}
	d, ok1 := fi.Sys().(*syscall.Stat_t)
	p, ok2 := parent.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false
	}
	return d.Dev != p.Dev
}
