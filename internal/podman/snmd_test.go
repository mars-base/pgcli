package podman

import (
	"reflect"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// storeMountsAndServerArgv is the whole SNMD/MNSD/MNMD/SNSD data-layout decision —
// pin every shape so minio and silo cannot drift apart and the container gets
// exactly the mount list and argv the erasure-coding mode needs.
func TestStoreMountsAndServerArgv(t *testing.T) {
	drives := []string{"/mnt/d1", "/mnt/d2", "/mnt/d3", "/mnt/d4"}
	eps := []string{"http://10.0.0.1:9000/data", "http://10.0.0.2:9000/data"}
	single := []string{"/base/store/data"}
	singleMount := []string{"-v", hostMountPath("/base/store/data") + ":/data:z"}

	tests := []struct {
		name       string
		dirs       []string
		endpoints  []string
		snmd       bool
		wantMounts []string
		wantServer []string
	}{
		{
			name:       "SNSD",
			dirs:       single,
			wantMounts: singleMount,
			wantServer: []string{"server", "/data"},
		},
		{
			name:       "MNSD: endpoints only, single /data mount",
			dirs:       single,
			endpoints:  eps,
			wantMounts: singleMount,
			wantServer: append([]string{"server"}, eps...),
		},
		{
			name:       "MNMD: endpoints + drives, per-drive mounts, full matrix argv",
			dirs:       []string{"/mnt/d1", "/mnt/d2"},
			endpoints:  []string{"http://10.0.0.1:9000/data1", "http://10.0.0.1:9000/data2", "http://10.0.0.2:9000/data1", "http://10.0.0.2:9000/data2"},
			snmd:       true,
			wantMounts: []string{"-v", "/mnt/d1:/data1:z", "-v", "/mnt/d2:/data2:z"},
			wantServer: []string{"server", "http://10.0.0.1:9000/data1", "http://10.0.0.1:9000/data2", "http://10.0.0.2:9000/data1", "http://10.0.0.2:9000/data2"},
		},
		{
			name:       "SNMD mounts every drive at its literal slot",
			dirs:       drives,
			snmd:       true,
			wantMounts: []string{"-v", "/mnt/d1:/data1:z", "-v", "/mnt/d2:/data2:z", "-v", "/mnt/d3:/data3:z", "-v", "/mnt/d4:/data4:z"},
			wantServer: []string{"server", "/data1", "/data2", "/data3", "/data4"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mounts, server := storeMountsAndServerArgv(tc.dirs, tc.endpoints, tc.snmd)
			if !reflect.DeepEqual(mounts, tc.wantMounts) {
				t.Errorf("mounts =\n %q\nwant\n %q", mounts, tc.wantMounts)
			}
			if !reflect.DeepEqual(server, tc.wantServer) {
				t.Errorf("server argv = %q, want %q", server, tc.wantServer)
			}
		})
	}
}

// The mode resolution rule — a non-empty Drives list is this node's drive set
// whether or not endpoints name a whole cluster (SNMD vs MNMD) — must hold
// identically in both managers' resolveDriveDirs.
func TestResolveDriveDirsModeRule(t *testing.T) {
	mm := &MinioManager{dataDir: "/base"}
	ms := &SiloManager{dataDir: "/base"}
	drives := []string{"/mnt/d1", "/mnt/d2"}

	// Drives without endpoints => the drives verbatim, in order.
	mcSNMD := &config.MinioConfig{Name: "n", Drives: drives}
	if got := mm.resolveDriveDirs(mcSNMD); !reflect.DeepEqual(got, drives) {
		t.Errorf("minio SNMD dirs = %q, want %q", got, drives)
	}
	// Endpoints present with drives => MNMD: this node's drives still win,
	// verbatim (the matrix rides in the endpoints, not the drive list).
	mcMNMD := &config.MinioConfig{Name: "n", Drives: drives, Endpoints: []string{"http://10.0.0.1:9000/data1", "http://10.0.0.1:9000/data2", "http://10.0.0.2:9000/data1", "http://10.0.0.2:9000/data2"}}
	if got := mm.resolveDriveDirs(mcMNMD); !reflect.DeepEqual(got, drives) {
		t.Errorf("minio MNMD dirs = %q, want %q", got, drives)
	}
	// Endpoints with no drives => MNSD stays the single data dir.
	mcMNSD := &config.MinioConfig{Name: "n", Endpoints: []string{"http://10.0.0.1:9000/data"}}
	if got, want := mm.resolveDriveDirs(mcMNSD), []string{"/base/addon/minio/n/data"}; !reflect.DeepEqual(got, want) {
		t.Errorf("minio MNSD dirs = %q, want %q", got, want)
	}
	// No drives => the single resolved data dir, override respected.
	if got, want := mm.resolveDriveDirs(&config.MinioConfig{Name: "n", DataDir: "/srv/m"}), []string{"/srv/m"}; !reflect.DeepEqual(got, want) {
		t.Errorf("minio SNSD override dirs = %q, want %q", got, want)
	}

	scSNMD := &config.SiloConfig{Name: "n", Drives: drives}
	if got := ms.resolveDriveDirs(scSNMD); !reflect.DeepEqual(got, drives) {
		t.Errorf("silo SNMD dirs = %q, want %q", got, drives)
	}
	scPlain := &config.SiloConfig{Name: "n"}
	if got, want := ms.resolveDriveDirs(scPlain), []string{"/base/addon/silo/n/data"}; !reflect.DeepEqual(got, want) {
		t.Errorf("silo SNSD dirs = %q, want %q", got, want)
	}
}

// The root-device reporter walks every resolved drive: "/" is trivially on the
// root device, /proc (its own mount on every Linux) is not. This is what the
// install-time warning gate reads per drive.
func TestDrivesSharingRootDevice(t *testing.T) {
	mm := &MinioManager{dataDir: t.TempDir()}
	bad := mm.DrivesSharingRootDevice(&config.MinioConfig{Name: "n", Drives: []string{"/", "/proc"}})
	if !reflect.DeepEqual(bad, []string{"/"}) {
		t.Errorf("bad drives = %q, want exactly [/]", bad)
	}
	ms := &SiloManager{dataDir: t.TempDir()}
	bad = ms.DrivesSharingRootDevice(&config.SiloConfig{Name: "n", Drives: []string{"/", "/proc"}})
	if !reflect.DeepEqual(bad, []string{"/"}) {
		t.Errorf("silo bad drives = %q, want exactly [/]", bad)
	}
}

// isMountpoint guards --clean-data: os.RemoveAll must never walk through a live
// mount. /proc proves the true case on any Linux; a fresh dir the false case.
func TestIsMountpoint(t *testing.T) {
	if !isMountpoint("/proc") {
		t.Fatalf("/proc must read as a mount point (guard is useless otherwise)")
	}
	if isMountpoint(t.TempDir()) {
		t.Fatalf("a plain temp dir must not read as a mount point")
	}
	if isMountpoint("/proc/self/nope-yes-a-subdir-is-not-checked") {
		t.Fatalf("nonexistent path must not read as a mount point")
	}
}

func TestSnmdContainerPaths(t *testing.T) {
	if got, want := snmdContainerPaths(3), []string{"/data1", "/data2", "/data3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("snmdContainerPaths(3) = %q, want %q", got, want)
	}
	if got := snmdContainerPaths(0); len(got) != 0 {
		t.Errorf("snmdContainerPaths(0) = %q, want empty", got)
	}
}
