package config

import (
	"os"
	"path/filepath"
	"strings"
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
	// v2: the zero-value entry normalizes to the full v1 converged role set
	// and node id 1 — behaviour stored configs from the v1 era keep unchanged.
	if got := bc.RolesInOrder(); len(got) != 3 || got[0] != BeeGFSServiceMgmtd || got[2] != BeeGFSServiceStorage {
		t.Errorf("empty roles must normalize to mgmtd,meta,storage, got %v", got)
	}
	if bc.NodeID != 1 {
		t.Errorf("node id = %d, want 1 (v1 compat)", bc.NodeID)
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

// --- v2 cross-host fields -------------------------------------------------

func TestBeeGFSRolesNormalize(t *testing.T) {
	cfg := Default()
	cfg.Addons.BeeGFS = map[string]BeeGFSConfig{
		// out of order and duplicated on purpose
		"join": {Roles: []string{"storage", "mgmtd", "storage"}},
	}
	cfg.ApplyDefaults()

	got := cfg.Addons.BeeGFS["join"].Roles
	if len(got) != 2 || got[0] != BeeGFSServiceMgmtd || got[1] != BeeGFSServiceStorage {
		t.Errorf("roles must dedupe into canonical order (mgmtd,storage), got %v", got)
	}
}

func TestBeeGFSV2Validate(t *testing.T) {
	cases := []struct {
		name    string
		bc      BeeGFSConfig
		wantErr bool
	}{
		{"v1 zero entry", BeeGFSConfig{}, false},
		{"full roles", BeeGFSConfig{Roles: []string{"mgmtd", "meta", "storage"}}, false},
		{"converged with mgmtd needs no mgmtd_host", BeeGFSConfig{Roles: []string{"mgmtd", "meta"}}, false},
		{"join needs mgmtd_host", BeeGFSConfig{Roles: []string{"storage"}}, true},
		{"join with mgmtd_host ok", BeeGFSConfig{Roles: []string{"storage"}, MgmtdHost: "10.0.0.5"}, false},
		{"unknown role", BeeGFSConfig{Roles: []string{"metad"}}, true},
		{"node id 655 still fits target-id space", BeeGFSConfig{NodeID: 655}, false},
		{"node id 656 overflows", BeeGFSConfig{NodeID: 656}, true},
		{"negative node id", BeeGFSConfig{NodeID: -1}, true},
	}
	for _, c := range cases {
		cfg := Default()
		bc := c.bc
		bc.Name = "fs1"
		bc.ContainerPrefix = "pgcli-beegfs-fs1"
		cfg.Addons.BeeGFS = map[string]BeeGFSConfig{"fs1": bc}
		err := cfg.Validate()
		if c.wantErr && err == nil {
			t.Errorf("%s: want Validate error, got nil", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: want Validate to pass, got %v", c.name, err)
		}
	}
}

func TestBeeGFSV2SaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.BeeGFS = map[string]BeeGFSConfig{
		"fs1": {
			Name:              "fs1",
			ContainerPrefix:   "pgcli-beegfs-fs1",
			ImageTag:          "8.4.0",
			ConnAuth:          "shared-auth-secret",
			Roles:             []string{"storage"},
			MgmtdHost:         "10.241.20.147",
			NodeID:            2,
			StorageTargetDirs: []string{"/mnt/d1", "/mnt/d2"},
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	bc := got.Addons.BeeGFS["fs1"]
	if len(bc.Roles) != 1 || bc.Roles[0] != "storage" {
		t.Errorf("roles lost: %v", bc.Roles)
	}
	if bc.MgmtdHost != "10.241.20.147" || bc.NodeID != 2 {
		t.Errorf("join fields lost: %q %d", bc.MgmtdHost, bc.NodeID)
	}
	if len(bc.StorageTargetDirs) != 2 || bc.StorageTargetDirs[1] != "/mnt/d2" {
		t.Errorf("storage target dirs lost: %v", bc.StorageTargetDirs)
	}
	// A join host must also survive Validate as-is (what `pg config` checks).
	if err := got.Validate(); err != nil {
		t.Errorf("loaded join entry must validate: %v", err)
	}
}

// --- license wiring -------------------------------------------------------

// The license is a multi-line PEM; yaml.v3 must round-trip it byte-identically
// through Save/Load (the value lands in the mgmtd container env verbatim).
const beegfsTestPEM = "-----BEGIN CERTIFICATE-----\nMIIBfake\nlicensePEM==\n-----END CERTIFICATE-----\n"

func TestBeeGFSLicenseRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.BeeGFS = map[string]BeeGFSConfig{
		"fs1": {Name: "fs1", ContainerPrefix: "pgcli-beegfs-fs1", License: beegfsTestPEM},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if again := got.Addons.BeeGFS["fs1"].License; again != beegfsTestPEM {
		t.Errorf("license PEM not byte-identical after round trip:\n%q", again)
	}
	// An unlicensed entry must not grow an empty-but-present key. Check a
	// config whose ONLY filesystem is unlicensed, so fs1's key can't false-hit.
	cfg2 := Default()
	cfg2.Instances = map[string]InstanceConfig{"a": {}}
	cfg2.Addons.BeeGFS = map[string]BeeGFSConfig{
		"fs2": {Name: "fs2", ContainerPrefix: "pgcli-beegfs-fs2"},
	}
	if err := cfg2.Save(path); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "license:") {
		t.Error("omitempty broken: a license-less config must not carry the key")
	}
}

func TestBeeGFSLicenseValidate(t *testing.T) {
	cases := []struct {
		name    string
		bc      BeeGFSConfig
		wantErr bool
	}{
		{"no license is the status quo", BeeGFSConfig{}, false},
		{"mgmtd host, PEM shape", BeeGFSConfig{License: beegfsTestPEM}, false},
		{"join host carrying a license", BeeGFSConfig{Roles: []string{"storage"}, MgmtdHost: "10.0.0.5", License: beegfsTestPEM}, true},
		{"shapeless value (wrong file)", BeeGFSConfig{License: "not a certificate at all"}, true},
	}
	for _, c := range cases {
		cfg := Default()
		bc := c.bc
		bc.Name = "fs1"
		bc.ContainerPrefix = "pgcli-beegfs-fs1"
		cfg.Addons.BeeGFS = map[string]BeeGFSConfig{"fs1": bc}
		err := cfg.Validate()
		if c.wantErr && err == nil {
			t.Errorf("%s: want Validate error, got nil", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: want Validate to pass, got %v", c.name, err)
		}
	}
}

// ApplyDefaults must never invent a license (same rule as ConnAuth): a zero
// entry stays unlicensed.
func TestBeeGFSLicenseNotGenerated(t *testing.T) {
	cfg := Default()
	cfg.Addons.BeeGFS = map[string]BeeGFSConfig{"fs1": {}}
	cfg.ApplyDefaults()
	if bc := cfg.Addons.BeeGFS["fs1"]; bc.License != "" {
		t.Errorf("ApplyDefaults invented a license: %q", bc.License)
	}
}
