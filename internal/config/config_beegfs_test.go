package config

import (
	"path/filepath"
	"testing"
)

func TestBeeGFSDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.BeeGFS = map[string]BeeGFSConfig{
		"fs1": {},
	}

	cfg.ApplyDefaults()

	bc, ok := cfg.Addons.BeeGFS["fs1"]
	if !ok {
		t.Fatal("beegfs addon missing after ApplyDefaults")
	}
	if bc.Name != "fs1" {
		t.Errorf("name = %q, want fs1", bc.Name)
	}
	if bc.ContainerPrefix != "pgcli-beegfs-fs1" {
		t.Errorf("container prefix = %q, want pgcli-beegfs-fs1", bc.ContainerPrefix)
	}
	// ImageTag stores only the VERSION, not a full ref: the manager builds
	// three refs (mgmtd/meta/storage) from BeeGFSImageRepo + role + version.
	if bc.ImageTag != DefaultBeeGFSVersion {
		t.Errorf("image tag = %q, want %q", bc.ImageTag, DefaultBeeGFSVersion)
	}
	if bc.Listen != "0.0.0.0" {
		t.Errorf("listen = %q, want 0.0.0.0 (a parallel filesystem is mounted from other hosts)", bc.Listen)
	}
	// ConnAuth is a shared secret — ApplyDefaults must never invent one; the
	// install handler generates it (same rule as Gitea.Password).
	if bc.ConnAuth != "" {
		t.Errorf("conn auth must stay empty through ApplyDefaults, got %q", bc.ConnAuth)
	}
	// Data dirs are never defaulted here — the manager resolves them under
	// base_dir (same contract as Gitea.DataDir).
	if bc.MgmtdDataDir != "" || bc.MetaDataDir != "" || bc.StorageDataDir != "" {
		t.Errorf("data dirs must stay empty through ApplyDefaults: %q %q %q", bc.MgmtdDataDir, bc.MetaDataDir, bc.StorageDataDir)
	}
	// The four ports default to the STOCK BeeGFS conventions, NOT a
	// consecutive pool: meta and storage have no supported listen-port
	// override in 8.4, so v1 pins the fixed defaults (the install handler
	// rejects any attempt to move meta/storage off them).
	if bc.MgmtdPort != DefaultBeeGFSMgmtdPort {
		t.Errorf("mgmtd port = %d, want %d", bc.MgmtdPort, DefaultBeeGFSMgmtdPort)
	}
	if bc.MgmtdGRPCPort != DefaultBeeGFSMgmtdGRPCPort {
		t.Errorf("mgmtd grpc port = %d, want %d", bc.MgmtdGRPCPort, DefaultBeeGFSMgmtdGRPCPort)
	}
	if bc.MetaPort != DefaultBeeGFSMetaPort {
		t.Errorf("meta port = %d, want %d", bc.MetaPort, DefaultBeeGFSMetaPort)
	}
	if bc.StoragePort != DefaultBeeGFSStoragePort {
		t.Errorf("storage port = %d, want %d", bc.StoragePort, DefaultBeeGFSStoragePort)
	}
}

// A user-pinned mgmtd port is kept as-is (the manager emits --beemsg-port /
// --grpc-port for it); meta/storage ports are likewise stored but the CLI is
// what rejects moving them. This test just pins the "don't clobber non-zero"
// contract ApplyDefaults owes every addon field.
func TestBeeGFSPinnedMgmtdPortsSurviveDefaults(t *testing.T) {
	cfg := Default()
	cfg.Addons.BeeGFS = map[string]BeeGFSConfig{
		"fs1": {MgmtdPort: 9008, MgmtdGRPCPort: 9010},
	}

	cfg.ApplyDefaults()

	bc := cfg.Addons.BeeGFS["fs1"]
	if bc.MgmtdPort != 9008 || bc.MgmtdGRPCPort != 9010 {
		t.Errorf("pinned mgmtd ports clobbered: %d/%d", bc.MgmtdPort, bc.MgmtdGRPCPort)
	}
	// meta/storage unset → filled with the fixed defaults.
	if bc.MetaPort != DefaultBeeGFSMetaPort || bc.StoragePort != DefaultBeeGFSStoragePort {
		t.Errorf("meta/storage not defaulted: %d/%d", bc.MetaPort, bc.StoragePort)
	}
}

// The fixed BeeGFS ports must not collide with any other addon's default pool;
// they sit in the 800x band that no web addon claims.
func TestBeeGFSFixedPortsDontCollide(t *testing.T) {
	cfg := Default()
	cfg.Addons.BeeGFS = map[string]BeeGFSConfig{"fs1": {}}
	cfg.Addons.Gitea = map[string]GiteaConfig{"git1": {}}
	cfg.Addons.Grafana = map[string]GrafanaConfig{"dash": {}}
	cfg.Addons.Prometheus = map[string]PrometheusConfig{"p1": {}}
	cfg.Addons.Seaweedfs = map[string]SeaweedfsConfig{"s1": {}}

	cfg.ApplyDefaults()

	b := cfg.Addons.BeeGFS["fs1"]
	g := cfg.Addons.Gitea["git1"]
	d := cfg.Addons.Grafana["dash"]
	p := cfg.Addons.Prometheus["p1"]
	s := cfg.Addons.Seaweedfs["s1"]
	others := []int{g.HostPort, g.SSHPort, d.HostPort, p.HostPort, s.Port, s.VolumePort, s.FilerPort, s.S3Port}
	for _, port := range []int{b.MgmtdPort, b.MgmtdGRPCPort, b.MetaPort, b.StoragePort} {
		for _, o := range others {
			if port == o {
				t.Errorf("beegfs port %d collides with another addon (%d)", port, o)
			}
		}
	}
}

func TestBeeGFSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.BeeGFS = map[string]BeeGFSConfig{
		"fs1": {
			Name:            "fs1",
			ContainerPrefix: "pgcli-beegfs-fs1",
			ImageTag:        "8.4.0",
			Listen:          "0.0.0.0",
			MgmtdPort:       8008,
			MgmtdGRPCPort:   8010,
			MetaPort:        8005,
			StoragePort:     8003,
			ConnAuth:        "shared-auth-secret",
			MgmtdDataDir:    "/srv/bfs/mgmtd",
			MetaDataDir:     "/srv/bfs/meta",
			StorageDataDir:  "/srv/bfs/storage",
			Autostart:       true,
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	bc, ok := got.Addons.BeeGFS["fs1"]
	if !ok {
		t.Fatal("beegfs addon not persisted")
	}
	if bc.ContainerPrefix != "pgcli-beegfs-fs1" || bc.ImageTag != "8.4.0" || bc.Listen != "0.0.0.0" {
		t.Errorf("identity fields lost: %+v", bc)
	}
	if bc.MgmtdPort != 8008 || bc.MgmtdGRPCPort != 8010 || bc.MetaPort != 8005 || bc.StoragePort != 8003 {
		t.Errorf("ports lost: %d %d %d %d", bc.MgmtdPort, bc.MgmtdGRPCPort, bc.MetaPort, bc.StoragePort)
	}
	if bc.ConnAuth != "shared-auth-secret" {
		t.Errorf("conn auth lost: %q", bc.ConnAuth)
	}
	if bc.MgmtdDataDir != "/srv/bfs/mgmtd" || bc.MetaDataDir != "/srv/bfs/meta" || bc.StorageDataDir != "/srv/bfs/storage" {
		t.Errorf("data dirs lost: %q %q %q", bc.MgmtdDataDir, bc.MetaDataDir, bc.StorageDataDir)
	}
	if !bc.Autostart {
		t.Error("autostart lost")
	}

	// Re-applying defaults over a loaded config must be idempotent: stored
	// ports stay, no secret or dir is invented.
	got.ApplyDefaults()
	again := got.Addons.BeeGFS["fs1"]
	if again.MgmtdPort != 8008 || again.MgmtdGRPCPort != 8010 || again.MetaPort != 8005 || again.StoragePort != 8003 {
		t.Errorf("ApplyDefaults re-assigned explicit ports: %d %d %d %d",
			again.MgmtdPort, again.MgmtdGRPCPort, again.MetaPort, again.StoragePort)
	}
	if again.ConnAuth != "shared-auth-secret" {
		t.Errorf("ApplyDefaults changed the stored conn auth: %q", again.ConnAuth)
	}
}
