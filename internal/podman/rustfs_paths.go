package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/tlsca"
)

// rustfsContainerPaths returns the in-container export paths rustfs is given for
// N drives: 0-indexed /data/rustfs0../data/rustfs(N-1). This is rustfs's own
// convention and deliberately differs from snmdContainerPaths (MinIO's
// 1-indexed /data1..dataN): rustfs's /entrypoint.sh mkdirs these exact paths
// from the RUSTFS_VOLUMES brace range, and the local disk root must exist or it
// aborts with VolumeNotFound.
func rustfsContainerPaths(n int) []string {
	paths := make([]string, n)
	for i := range paths {
		paths[i] = fmt.Sprintf("/data/rustfs%d", i)
	}
	return paths
}

// rustfsVolumeRange renders the drive portion of RUSTFS_VOLUMES for D drives:
// a single literal /data/rustfs0 when D==1, otherwise the brace range
// /data/rustfs{0...D-1} that rustfs's entrypoint expands. D must be >= 1.
func rustfsVolumeRange(drives int) string {
	if drives <= 1 {
		return "/data/rustfs0"
	}
	return fmt.Sprintf("/data/rustfs{0...%d}", drives-1)
}

// rustfsVolumesEnv builds the single RUSTFS_VOLUMES value for a node. There is
// no multi-node single-drive topology for rustfs, so endpoints (MNMD) always
// arrive with drivesProvided true — the CLI rejects the empty-drive case
// (ValidateRustfsVolumes). driveCount is len(driveDirs), which is >= 1 in every
// mode (SNSD resolves to the one data dir).
//
//   - SNSD (no endpoints, no drives): "/data" (the single data-dir mount).
//   - SNMD (no endpoints, drives):    "/data/rustfs{0...D-1}" (or /data/rustfs0).
//   - MNMD (endpoints, drives):       "<epN>/data/rustfs{0...D-1} <...>" — every
//     endpoint (each node's scheme://host:port) gets the shared per-drive suffix,
//     SPACE-joined. The separator MUST be a space, not a comma: the image's
//     /entrypoint.sh turns commas into spaces with `tr` before expanding the
//     brace ranges for its OWN argv, but the rustfs binary re-reads the raw
//     RUSTFS_VOLUMES value and mis-tokenizes a comma-joined URL list — the
//     "http://" prefix collapses to "http:/", so the node resolves the whole
//     comma-run as a single bogus local path and aborts with VolumeNotFound (or,
//     on the peers, degrades to "waiting for storage_quorum" with no writable
//     cluster). Space-separated literals parse identically to the documented
//     compact brace form (http://node{1...4}:9000/data/rustfs{0...3}) but let
//     pgcli emit the operator's literal, per-node IPs with no /etc/hosts setup.
func rustfsVolumesEnv(endpoints []string, drivesProvided bool, driveCount int) string {
	switch {
	case len(endpoints) > 0:
		suffix := rustfsVolumeRange(driveCount)
		parts := make([]string, 0, len(endpoints))
		for _, ep := range endpoints {
			parts = append(parts, ep+suffix)
		}
		return strings.Join(parts, " ")
	case drivesProvided:
		return rustfsVolumeRange(driveCount)
	default:
		return "/data"
	}
}

// rustfsMountFlags returns the podman -v args binding each host dir into the
// container. SNSD mounts the single data dir at /data; every other shape mounts
// drive N at its own /data/rustfsN slot (0-indexed), matching rustfsContainerPaths
// and the RUSTFS_VOLUMES range. driveDirs comes from resolveDriveDirs (>= 1).
func rustfsMountFlags(driveDirs []string, endpoints []string, drivesProvided bool) []string {
	if len(endpoints) == 0 && !drivesProvided {
		return []string{"-v", fmt.Sprintf("%s:/data:z", hostMountPath(driveDirs[0]))}
	}
	cp := rustfsContainerPaths(len(driveDirs))
	mounts := make([]string, 0, 2*len(driveDirs))
	for i, d := range driveDirs {
		mounts = append(mounts, "-v", fmt.Sprintf("%s:%s:z", hostMountPath(d), cp[i]))
	}
	return mounts
}

// rustfsTLSMountFlags delivers cert material to the wrapper's read-only SOURCE
// mount (rustfsTLSSrcDir), from which the entrypoint copies into rustfs's live
// TLS dir (rustfsCertsDir). rustfs reads exactly rustfs_cert.pem + rustfs_key.pem
// there (NOT MinIO/silo's public.crt/private.key), via RUSTFS_TLS_PATH env rather
// than a --certs-dir flag. The source is ALWAYS mounted read-only: pgcli's own
// cert dir (generated mode) or the operator's BYO files must never be mutated by
// the container, and the wrapper's copy-then-chown-destination design means
// nothing needs to be written back here anyway.
//
//   - BYO (cert+key set): bind the two operator files at rustfs's required names.
//   - Generated: mount the whole tlsDir — rustfsSyncGeneratedCerts has already
//     placed rustfs_cert.pem/rustfs_key.pem beside the CA material, so a single
//     dir mount carries exactly what the wrapper needs to copy.
func rustfsTLSMountFlags(certFile, keyFile, tlsDir string) []string {
	if BYOTLS(true, certFile, keyFile) {
		return []string{
			"-v", fmt.Sprintf("%s:%s/rustfs_cert.pem:ro,z", hostMountPath(certFile), rustfsTLSSrcDir),
			"-v", fmt.Sprintf("%s:%s/rustfs_key.pem:ro,z", hostMountPath(keyFile), rustfsTLSSrcDir),
		}
	}
	if tlsDir == "" {
		return nil
	}
	return []string{"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(tlsDir), rustfsTLSSrcDir)}
}

// Generated-mode TLS file names tlsca writes and rustfs needs, in the same dir.
const (
	rustfsCertName = "rustfs_cert.pem"
	rustfsKeyName  = "rustfs_key.pem"
)

// rustfsSyncGeneratedCerts copies the tlsca leaf (public.crt) and key
// (private.key) in the addon's generated TLS dir to the names rustfs reads
// (rustfs_cert.pem / rustfs_key.pem). The originals stay for clients and for
// `pg cert`/`pg backup fetch-ca`; rustfs just wants its own spellings. Copied
// in-place (same base dir) so no secret moves anywhere new. Re-run after every
// tlsca.Generate so a re-signed leaf propagates to the rustfs-named pair.
func rustfsSyncGeneratedCerts(tlsDir string) error {
	cop := []struct {
		src, dst string
		mode     os.FileMode
	}{
		{tlsca.ServerCert, rustfsCertName, 0644},
		{tlsca.ServerKey, rustfsKeyName, 0600},
	}
	for _, c := range cop {
		b, err := os.ReadFile(filepath.Join(tlsDir, c.src))
		if err != nil {
			return fmt.Errorf("reading generated %s for rustfs: %w", c.src, err)
		}
		if err := os.WriteFile(filepath.Join(tlsDir, c.dst), b, c.mode); err != nil {
			return fmt.Errorf("writing %s for rustfs: %w", c.dst, err)
		}
	}
	return nil
}

// ValidateRustfsVolumes gates a rustfs install before ApplyDefaults. rustfs has
// exactly three topologies and NO multi-node single-drive mode: endpoints
// (MNMD) require at least one drive, since each node must contribute drives at
// /data/rustfsN. That is the only structural rule pgcli can cheaply enforce at
// install. The minimum drive/node count and the "drives must be on distinct
// physical devices" rule are left to rustfs itself at startup (it FATALs
// otherwise) — no host-side stat-based pre-check, so an as-yet-unmounted drive
// path is not falsely rejected here.
func ValidateRustfsVolumes(endpoints []string, drives int) error {
	if len(endpoints) > 0 && drives == 0 {
		return fmt.Errorf("rustfs has no multi-node single-drive mode; pass --drive alongside --endpoint, or drop --endpoint for single-node")
	}
	return nil
}
