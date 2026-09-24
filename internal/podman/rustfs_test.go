package podman

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// rustfs's data dir and TLS dir resolve under the rustfs-namespaced layout, not
// minio's/silo's — the one place a copy-paste between the three managers would
// go silently wrong (a rustfs instance writing to addons.minio). Pin it.
func TestRustfsPathResolution(t *testing.T) {
	m := &RustfsManager{dataDir: "/base"}
	rc := &config.RustfsConfig{Name: "r1"}

	if got := m.resolveDataDir(rc); got != "/base/addon/rustfs/r1/data" {
		t.Errorf("data dir = %q, want .../addon/rustfs/r1/data", got)
	}
	if got := m.TLSDir(rc); got != "/base/tls/rustfs/r1" {
		t.Errorf("tls dir = %q, want .../tls/rustfs/r1", got)
	}
	// Explicit DataDir override wins, same as minio/silo.
	rc2 := &config.RustfsConfig{Name: "r1", DataDir: "/srv/rustfs"}
	if got := m.resolveDataDir(rc2); got != "/srv/rustfs" {
		t.Errorf("override data dir = %q, want /srv/rustfs", got)
	}
}

// serverURL follows TLS for the scheme and the listen for the host, like the
// other stores. The rustfs health endpoint is /health (not MinIO's
// /minio/health/live), but that is a URL path appended at the call site, not
// part of serverURL — this pins only the base.
func TestRustfsServerURL(t *testing.T) {
	m := &RustfsManager{dataDir: "/base", bridge: false}
	plain := m.serverURL(&config.RustfsConfig{Listen: "10.0.0.9", APIPort: 9000})
	if want := "http://10.0.0.9:9000"; plain != want {
		t.Errorf("serverURL = %q, want %q", plain, want)
	}
	tls := m.serverURL(&config.RustfsConfig{Listen: "10.0.0.9", APIPort: 9000, TLS: true})
	if want := "https://10.0.0.9:9000"; tls != want {
		t.Errorf("TLS serverURL = %q, want %q", tls, want)
	}
}

func TestRustfsContainerPaths(t *testing.T) {
	if got := rustfsContainerPaths(0); len(got) != 0 {
		t.Errorf("0 drives = %v, want empty", got)
	}
	if got := rustfsContainerPaths(4); !reflect.DeepEqual(got,
		[]string{"/data/rustfs0", "/data/rustfs1", "/data/rustfs2", "/data/rustfs3"}) {
		t.Errorf("4 drives = %v, want 0-indexed /data/rustfs0..3", got)
	}
}

func TestRustfsVolumeRange(t *testing.T) {
	// One drive has no meaningful range — a literal slot, no braces.
	if got := rustfsVolumeRange(1); got != "/data/rustfs0" {
		t.Errorf("1 drive = %q, want /data/rustfs0", got)
	}
	if got := rustfsVolumeRange(4); got != "/data/rustfs{0...3}" {
		t.Errorf("4 drives = %q, want /data/rustfs{0...3}", got)
	}
}

func TestRustfsVolumesEnv(t *testing.T) {
	tests := []struct {
		name     string
		ep       []string
		drives   bool
		driveCnt int
		want     string
	}{
		{"SNSD", nil, false, 1, "/data"},
		{"SNMD single drive", nil, true, 1, "/data/rustfs0"},
		{"SNMD four drives", nil, true, 4, "/data/rustfs{0...3}"},
		{"MNMD two nodes × two drives",
			[]string{"http://10.0.0.1:9000", "http://10.0.0.2:9000"}, true, 2,
			// Space-joined, NOT comma: rustfs's binary mis-splits a comma-joined
			// URL list (collapsing http:// to http:/ and aborting VolumeNotFound).
			"http://10.0.0.1:9000/data/rustfs{0...1} http://10.0.0.2:9000/data/rustfs{0...1}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rustfsVolumesEnv(tc.ep, tc.drives, tc.driveCnt); got != tc.want {
				t.Errorf("rustfsVolumesEnv = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRustfsMountFlags(t *testing.T) {
	// SNSD: the single data dir at /data. hostMountPath makes each source
	// absolute, so relative inputs resolve against the test working dir — build
	// expectations the same way.
	single := []string{"/base/store/data"}
	if got, want := rustfsMountFlags(single, nil, false),
		[]string{"-v", hostMountPath("/base/store/data") + ":/data:z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SNSD mounts = %v, want %v", got, want)
	}
	// SNMD/MNMD: each drive at its 0-indexed /data/rustfsN slot.
	drives := []string{"/mnt/d1", "/mnt/d2"}
	if got, want := rustfsMountFlags(drives, nil, true),
		[]string{"-v", "/mnt/d1:/data/rustfs0:z", "-v", "/mnt/d2:/data/rustfs1:z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SNMD mounts = %v, want %v", got, want)
	}
}

func TestRustfsTLSMountFlags(t *testing.T) {
	// BYO: the two operator files mounted read-only at the wrapper's SOURCE dir
	// (rustfsTLSSrcDir), at rustfs's required names — the entrypoint copies them
	// into the live rustfsCertsDir from there. The operator's files are never
	// written to, and pgcli passes them 0644-readable via the docs, not via chown.
	got, want := rustfsTLSMountFlags("/etc/ssl/c.crt", "/etc/ssl/k.key", "/irrelevant"),
		[]string{
			"-v", hostMountPath("/etc/ssl/c.crt") + ":" + rustfsTLSSrcDir + "/rustfs_cert.pem:ro,z",
			"-v", hostMountPath("/etc/ssl/k.key") + ":" + rustfsTLSSrcDir + "/rustfs_key.pem:ro,z",
		}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BYO TLS mounts = %v, want %v", got, want)
	}
	// Generated: the whole tlsDir read-only at rustfsTLSSrcDir. rustfsSyncGeneratedCerts
	// placed the rustfs-named pair there already; the entrypoint copies to the live
	// dir. Never mounted rw — pgcli owns tlsDir and its 0700 perms must not be
	// changed by anything the container does.
	got, want = rustfsTLSMountFlags("", "", "/base/tls/rustfs/r1"),
		[]string{"-v", hostMountPath("/base/tls/rustfs/r1") + ":" + rustfsTLSSrcDir + ":ro,z"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("generated TLS mounts = %v, want %v", got, want)
	}
	// TLS on with no dir and no BYO pair: nothing to mount (not fatal).
	if got := rustfsTLSMountFlags("", "", ""); got != nil {
		t.Errorf("empty generated TLS = %v, want nil", got)
	}
}

// rustfs has no multi-node single-drive mode: endpoints require drives. That is
// the only structural rule pgcli can enforce cheaply at install.
func TestValidateRustfsVolumes(t *testing.T) {
	// MNSD (endpoints, zero drives) rejected.
	err := ValidateRustfsVolumes([]string{"http://10.0.0.1:9000", "http://10.0.0.2:9000"}, 0)
	if err == nil || !strings.Contains(err.Error(), "multi-node single-drive") {
		t.Errorf("MNSD must be rejected, got %v", err)
	}
	// SNSD (no endpoints, no drives) is fine.
	if err := ValidateRustfsVolumes(nil, 0); err != nil {
		t.Errorf("SNSD rejected: %v", err)
	}
	// SNMD (drives, no endpoints) is fine regardless of count.
	if err := ValidateRustfsVolumes(nil, 4); err != nil {
		t.Errorf("SNMD rejected: %v", err)
	}
	// MNMD (endpoints + drives) is fine — count/device checks left to runtime.
	if err := ValidateRustfsVolumes([]string{"http://10.0.0.1:9000", "http://10.0.0.2:9000"}, 2); err != nil {
		t.Errorf("MNMD rejected: %v", err)
	}
}
