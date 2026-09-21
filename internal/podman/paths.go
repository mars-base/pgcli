package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// multiDriveMounts bind-mounts each host drive dir at its own in-container slot
// (/data1..dataN) — the layout both SNMD and MNMD need, since in either the
// server sees one export path per physical drive.
func multiDriveMounts(driveDirs []string) []string {
	cp := snmdContainerPaths(len(driveDirs))
	mounts := make([]string, 0, 2*len(driveDirs))
	for i, d := range driveDirs {
		mounts = append(mounts, "-v", fmt.Sprintf("%s:%s:z", hostMountPath(d), cp[i]))
	}
	return mounts
}

// storeMountsAndServerArgv is the single source of truth for how the minio and
// silo addons lay out their data mounts and `server ...` argv, so both stay in
// lockstep and the mode logic is testable without running podman. It takes the
// resolved host drive dirs (resolveDriveDirs output: the drive list whenever
// Drives is set, otherwise the one data dir), the endpoint list, and whether any
// drive was given. Four shapes:
//
//   - MNMD (endpoints and drives): this node's drives each mount at their own
//     slot /dataN, and `server <endpoint>...` — the endpoints are the whole
//     cluster's host×drive matrix, whose export paths name those very slots.
//   - MNSD (endpoints, no drives): one /data mount (this node's single export
//     path) and `server <endpoint>...` — one endpoint per node.
//   - SNMD (drives, no endpoints): one -v hostDirN:/dataN mount per drive and
//     `server /data1 /data2 ...` — MinIO erasure-codes across this node's disks.
//   - SNSD (neither): the plain `server /data` single mount.
func storeMountsAndServerArgv(driveDirs []string, endpoints []string, drivesProvided bool) (mounts, serverArgs []string) {
	switch {
	case len(endpoints) > 0 && drivesProvided:
		mounts = multiDriveMounts(driveDirs)
		serverArgs = append([]string{"server"}, endpoints...)
	case len(endpoints) > 0:
		mounts = []string{"-v", fmt.Sprintf("%s:/data:z", hostMountPath(driveDirs[0]))}
		serverArgs = append([]string{"server"}, endpoints...)
	case drivesProvided:
		mounts = multiDriveMounts(driveDirs)
		serverArgs = append([]string{"server"}, snmdContainerPaths(len(driveDirs))...)
	default:
		mounts = []string{"-v", fmt.Sprintf("%s:/data:z", hostMountPath(driveDirs[0]))}
		serverArgs = []string{"server", "/data"}
	}
	return mounts, serverArgs
}

// ValidateMNMDMatrix checks a distributed cluster's endpoint list against this
// node's drive count before the container starts. It is a no-op unless both are
// set (MNSD passes endpoints with no drives; SNMD/SNSD pass no endpoints). The
// rules mirror what MinIO itself refuses at runtime, surfaced as a clean CLI
// error instead of a crash-looping container:
//   - every endpoint's export path must be a /dataN slot, since that is where
//     each node's drives are mounted under MNMD;
//   - the list must divide evenly into per-node drive groups of len(drives) —
//     MinIO requires every node contribute the same number of drives;
//   - at least len(drives) endpoints (one node's worth) is the degenerate
//     single-host fold, which MinIO rejects as not-distributed;
//   - endpoint scheme must match the node's TLS mode: a --tls node serving
//     http:// endpoints (or a plaintext node serving https:// ones) hits
//     MinIO's "HTTP specified in endpoints, but the server ... is configured
//     with a TLS certificate" FATAL at startup.
// tls is this node's TLS flag. listen is this node's bind address; it is not
// enforced here (a node may bind 0.0.0.0 while advertising a LAN IP in the
// matrix).
func ValidateMNMDMatrix(endpoints []string, drives int, tls bool) error {
	if len(endpoints) == 0 || drives == 0 {
		return nil
	}
	if len(endpoints)%drives != 0 {
		return fmt.Errorf("MNMD needs one endpoint per drive on every node: %d endpoints is not a multiple of %d drives/node — each node must contribute the same number of drives", len(endpoints), drives)
	}
	if len(endpoints) <= drives {
		return fmt.Errorf("MNMD needs at least one more node than this host: %d endpoints ÷ %d drives/node = a single node — pass --endpoint for every node of the cluster (distributed mode is cross-host)", len(endpoints), drives)
	}
	for _, ep := range endpoints {
		path := endpointExportPath(ep)
		if !isDataSlot(path) {
			return fmt.Errorf("MNMD endpoint %q has export path %q, but multi-drive nodes mount their drives at /data1../data%d — each endpoint must address one of those slots", ep, path, drives)
		}
		if wantHTTPS := tls; strings.HasPrefix(ep, "https://") != wantHTTPS {
			if wantHTTPS {
				return fmt.Errorf("MNMD endpoint %q is plaintext http:// but this node serves TLS (--tls): MinIO refuses to start when its certificate and the endpoint scheme disagree — pass every endpoint as https://", ep)
			}
			return fmt.Errorf("MNMD endpoint %q is https:// but this node serves plaintext (no --tls): MinIO refuses to start when its endpoints and the local TLS mode disagree — drop --tls on every node or pass all endpoints as https://", ep)
		}
	}
	return nil
}

// endpointExportPath returns the URL path component of an endpoint like
// http://host:9000/data2 (empty if it has none). Scheme and host:port are split
// off by hand — the endpoint is not a real network URL pgcli ever dials, only
// its trailing path carries meaning (the in-container export dir).
func endpointExportPath(ep string) string {
	rest := ep
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[i:]
	}
	return ""
}

// isDataSlot reports whether an export path is exactly /dataN for N>=1 (the
// per-drive mount slots MNMD uses), not the bare /data of MNSD.
func isDataSlot(path string) bool {
	rest, ok := strings.CutPrefix(path, "/data")
	if !ok {
		return false
	}
	if rest == "" {
		return false // bare /data is MNSD, not an MNMD drive slot
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
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
