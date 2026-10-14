package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestGarageBindAddr(t *testing.T) {
	if got := garageBindAddr("127.0.0.1", 3900); got != "127.0.0.1:3900" {
		t.Errorf("IPv4 = %q, want 127.0.0.1:3900", got)
	}
	// Garage parses TOML bind_addr with net::ToSocketAddrs: an IPv6 literal or
	// the dual-stack wildcard must be bracketed.
	if got := garageBindAddr("::", 3900); got != "[::]:3900" {
		t.Errorf("wildcard = %q, want [::]:3900", got)
	}
	if got := garageBindAddr("::1", 3900); got != "[::1]:3900" {
		t.Errorf("loopback6 = %q, want [::1]:3900", got)
	}
}

func TestRenderGarageTOML(t *testing.T) {
	gc := &config.GarageConfig{
		Name:              "store",
		ReplicationFactor: 1,
		RPCSecret:         "deadbeef",
		Region:            "garage",
		RPCPort:           3901,
		APIPort:           3900,
		WebPort:           3902,
		AdminPort:         3903,
	}
	out := renderGarageTOML(gc, "127.0.0.1", nil)

	wants := []string{
		`metadata_dir = "/var/lib/garage/meta"`,
		`data_dir = "/var/lib/garage/data"`,
		`db_engine = "lmdb"`,
		`replication_factor = 1`,
		`rpc_bind_addr = "127.0.0.1:3901"`,
		`rpc_secret = "deadbeef"`,
		"[s3_api]",
		`s3_region = "garage"`,
		`api_bind_addr = "127.0.0.1:3900"`,
		"[s3_web]",
		`bind_addr = "127.0.0.1:3902"`,
		// v2.4 refuses to boot without this required [s3_web] field.
		`root_domain = "garage.localhost"`,
		"[admin]",
		`api_bind_addr = "127.0.0.1:3903"`,
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("rendered TOML missing %q:\n%s", w, out)
		}
	}
	// The file carries rpc_secret, so the two api_bind_addr lines must both be
	// present (s3_api and admin) — a mis-nested table would still "contain"
	// them, so pin ordering too: [s3_web] after [s3_api], [admin] after that.
	iAPI := strings.Index(out, "[s3_api]")
	iWeb := strings.Index(out, "[s3_web]")
	iAdmin := strings.Index(out, "[admin]")
	if !(iAPI < iWeb && iWeb < iAdmin) {
		t.Errorf("table order wrong: s3_api=%d s3_web=%d admin=%d", iAPI, iWeb, iAdmin)
	}

	// A bridge/IPv6 bind reaches every line bracketed consistently.
	out6 := renderGarageTOML(gc, "::", nil)
	for _, w := range []string{
		`rpc_bind_addr = "[::]:3901"`,
		`api_bind_addr = "[::]:3900"`,
		`bind_addr = "[::]:3902"`,
	} {
		if !strings.Contains(out6, w) {
			t.Errorf("IPv6 render missing %q:\n%s", w, out6)
		}
	}
}

func TestRenderGarageTOMLCluster(t *testing.T) {
	gc := &config.GarageConfig{
		Name: "store", Cluster: "c1", AdvertiseHost: "10.0.0.11",
		ReplicationFactor: 3, RPCSecret: "aa", Region: "garage",
		APIPort: 3900, RPCPort: 3901, WebPort: 3902, AdminPort: 3903,
	}
	out := renderGarageTOML(gc, "0.0.0.0", nil)
	// rpc_public_addr is the peer-dialable address — between bind and secret.
	if !strings.Contains(out, `rpc_public_addr = "10.0.0.11:3901"`) {
		t.Errorf("cluster render missing rpc_public_addr:\n%s", out)
	}
	if !strings.Contains(out, "replication_factor = 3") {
		t.Errorf("cluster render missing rf 3:\n%s", out)
	}
	// An IPv6 advertise host is bracketed (same rule as the bind addrs).
	gc6 := *gc
	gc6.AdvertiseHost = "fd00::1"
	if !strings.Contains(renderGarageTOML(&gc6, "::", nil), `rpc_public_addr = "[fd00::1]:3901"`) {
		t.Error("IPv6 advertise host must be bracketed")
	}
	// Single-node (AdvertiseHost empty): no rpc_public_addr line at all.
	gc1 := *gc
	gc1.AdvertiseHost = ""
	if strings.Contains(renderGarageTOML(&gc1, "127.0.0.1", nil), "rpc_public_addr") {
		t.Error("single-node render must not carry rpc_public_addr")
	}
}

func TestGarageMountFlags(t *testing.T) {
	flags := garageMountFlags("/base/meta", []string{"/base/data"}, "/base/garage.toml")
	want := []string{
		"-v", "/base/meta:/var/lib/garage/meta:z",
		"-v", "/base/data:/var/lib/garage/data:z",
		"-v", "/base/garage.toml:/etc/garage.toml:ro,z",
	}
	if strings.Join(flags, " ") != strings.Join(want, " ") {
		t.Errorf("mount flags =\n%v\nwant\n%v", flags, want)
	}
	// Multi-disk: drive i lands at its own numbered slot, meta and config stay
	// one apiece (metadata_dir is never part of the drive set).
	multi := garageMountFlags("/base/meta", []string{"/mnt/d1", "/mnt/d2", "/mnt/d3"}, "/base/garage.toml")
	wantMulti := []string{
		"-v", "/base/meta:/var/lib/garage/meta:z",
		"-v", "/mnt/d1:/var/lib/garage/data1:z",
		"-v", "/mnt/d2:/var/lib/garage/data2:z",
		"-v", "/mnt/d3:/var/lib/garage/data3:z",
		"-v", "/base/garage.toml:/etc/garage.toml:ro,z",
	}
	if strings.Join(multi, " ") != strings.Join(wantMulti, " ") {
		t.Errorf("multi-drive mount flags =\n%v\nwant\n%v", multi, wantMulti)
	}
}

// containerGarageDataDirs is the slot contract shared by the TOML render and
// the mount loop: count 0/1 keeps the bare constant, N>1 numbers each slot.
func TestContainerGarageDataDirs(t *testing.T) {
	if got := containerGarageDataDirs(0); len(got) != 1 || got[0] != "/var/lib/garage/data" {
		t.Errorf("count 0 = %v, want [/var/lib/garage/data]", got)
	}
	if got := containerGarageDataDirs(1); len(got) != 1 || got[0] != "/var/lib/garage/data" {
		t.Errorf("count 1 = %v, want [/var/lib/garage/data]", got)
	}
	got := containerGarageDataDirs(3)
	want := []string{"/var/lib/garage/data1", "/var/lib/garage/data2", "/var/lib/garage/data3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("count 3 = %v, want %v", got, want)
	}
}

// Multi-disk renders data_dir as Garage's array-of-tables form (native since
// v0.9.0), with a REQUIRED per-entry capacity — v2.4 refuses an entry without
// one (verified on vm01). pgcli charges each drive its whole filesystem. A
// single-dir config must keep rendering the exact same single-line string as
// before (byte-compatibility).
func TestRenderGarageTOMLMultiDrive(t *testing.T) {
	gc := &config.GarageConfig{
		Name: "store", ReplicationFactor: 1, RPCSecret: "aa", Region: "garage",
		APIPort: 3900, RPCPort: 3901, WebPort: 3902, AdminPort: 3903,
		Drives: []string{"/mnt/garage/d1", "/mnt/garage/d2"},
	}
	out := renderGarageTOML(gc, "127.0.0.1", []uint64{4, 8})
	for _, want := range []string{
		"data_dir = [",
		`{ path = "/var/lib/garage/data1", capacity = "4G" },`,
		`{ path = "/var/lib/garage/data2", capacity = "8G" },`,
		`metadata_dir = "/var/lib/garage/meta"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("multi-drive render missing %q:\n%s", want, out)
		}
	}
	// Never the old single-line form once drives are set.
	if strings.Contains(out, `data_dir = "/var/lib/garage/data"`) {
		t.Errorf("single-dir data_dir leaked into a multi-drive render:\n%s", out)
	}
	// The array must sit between the two scalar lines it belongs beside, and
	// metadata_dir must stay a plain string (never part of the array).
	iMeta := strings.Index(out, `metadata_dir = "/var/lib/garage/meta"`)
	iArr := strings.Index(out, "data_dir = [")
	iDB := strings.Index(out, `db_engine`)
	if !(iMeta < iArr && iArr < iDB) {
		t.Errorf("dir lines out of order: meta=%d data_dir=%d db_engine=%d", iMeta, iArr, iDB)
	}

	// Single-dir (no drives) is byte-identical to the legacy rendering: no
	// capacity appears (the scalar form takes none).
	single := *gc
	single.Drives = nil
	outSingle := renderGarageTOML(&single, "127.0.0.1", nil)
	if !strings.Contains(outSingle, "\ndata_dir = \"/var/lib/garage/data\"\n") {
		t.Error("single-dir render must keep the plain string form")
	}
	if strings.Contains(outSingle, "capacity") {
		t.Errorf("single-dir render must not carry a capacity:\n%s", outSingle)
	}
	// Exactly one drive is still the single-dir case, not a one-element array.
	one := *gc
	one.Drives = []string{"/mnt/garage/d1"}
	outOne := renderGarageTOML(&one, "127.0.0.1", []uint64{4})
	if strings.Contains(outOne, "data_dir = [") {
		t.Errorf("one drive must render as a string, not an array:\n%s", outOne)
	}
}

// driveCapacityGiB sizes a drive's filesystem in GiB (floor) and falls back to
// the nearest existing parent for a not-yet-created dir, so the render never
// statfs-fails on a dir createContainer is about to mkdir.
func TestDriveCapacityGiB(t *testing.T) {
	dir := t.TempDir()
	gib, err := driveCapacityGiB(dir)
	if err != nil {
		t.Fatalf("statfs tempdir: %v", err)
	}
	if gib < 1 {
		t.Errorf("capacity of %q = %d, want >= 1", dir, gib)
	}
	// A deep nonexistent path under an existing fs resolves to that fs's size.
	deeper, err := driveCapacityGiB(filepath.Join(dir, "a", "b", "c"))
	if err != nil {
		t.Fatalf("statfs nonexistent-under-existing: %v", err)
	}
	if deeper != gib {
		t.Errorf("missing-dir capacity = %d, want the parent fs size %d", deeper, gib)
	}
}

func TestValidateGarageCluster(t *testing.T) {
	// Single-node: replication_factor > 1 rejected (v1 contract).
	if err := ValidateGarageCluster(&config.GarageConfig{}); err != nil {
		t.Errorf("unset: %v", err)
	}
	if err := ValidateGarageCluster(&config.GarageConfig{ReplicationFactor: 1}); err != nil {
		t.Errorf("rf 1: %v", err)
	}
	if err := ValidateGarageCluster(&config.GarageConfig{ReplicationFactor: 3}); err == nil {
		t.Error("rf 3 without cluster must be rejected")
	}
	// Cluster: advertise-host required; rpc_secret required only of a joiner
	// (Peers non-empty) — the bootstrap generates it during install.
	if err := ValidateGarageCluster(&config.GarageConfig{Cluster: "c1", RPCSecret: "aa", ReplicationFactor: 3}); err == nil {
		t.Error("cluster without advertise-host must be rejected")
	}
	if err := ValidateGarageCluster(&config.GarageConfig{Cluster: "c1", AdvertiseHost: "10.0.0.1", ReplicationFactor: 3}); err != nil {
		t.Errorf("bootstrap (no peers, no secret yet) must pass: %v", err)
	}
	if err := ValidateGarageCluster(&config.GarageConfig{Cluster: "c1", AdvertiseHost: "10.0.0.1", ReplicationFactor: 3, Peers: []string{"abcd1234@10.0.0.1:3901"}}); err == nil {
		t.Error("joiner without rpc_secret must be rejected")
	}
	if err := ValidateGarageCluster(&config.GarageConfig{Cluster: "c1", AdvertiseHost: "10.0.0.1", RPCSecret: "aa", ReplicationFactor: 3, Capacity: 10}); err == nil {
		t.Error("capacity without zone must be rejected")
	}
	if err := ValidateGarageCluster(&config.GarageConfig{Cluster: "c1", AdvertiseHost: "10.0.0.1", RPCSecret: "aa", ReplicationFactor: 3, Capacity: 10, Zone: "z1"}); err != nil {
		t.Errorf("valid cluster config rejected: %v", err)
	}
	// Single-node supplied credentials: Garage's first-boot floors (id ≥ 8,
	// secret ≥ 16), checked up front because the detached container would only
	// crash-loop later. Boundary values pass; empty means "generate" and must
	// never be gated.
	if err := ValidateGarageCluster(&config.GarageConfig{RootUser: "ABCDEFGH", RootPassword: "0123456789abcdef"}); err != nil {
		t.Errorf("boundary ak 8 / sk 16 must pass: %v", err)
	}
	if err := ValidateGarageCluster(&config.GarageConfig{RootUser: "abcdefg"}); err == nil {
		t.Error("access key of 7 chars must be rejected")
	}
	if err := ValidateGarageCluster(&config.GarageConfig{RootPassword: "0123456789abcde"}); err == nil {
		t.Error("secret key of 15 chars must be rejected")
	}
	// The cluster path ignores RootUser/RootPassword entirely (key create seeds
	// there), so short supplied values must not trip it.
	if err := ValidateGarageCluster(&config.GarageConfig{Cluster: "c1", AdvertiseHost: "10.0.0.1", RPCSecret: "aa", ReplicationFactor: 3, Capacity: 10, Zone: "z1", RootUser: "ab", RootPassword: "x"}); err != nil {
		t.Errorf("cluster member must not be gated on credentials: %v", err)
	}
}

// The three host paths resolve under the garage-namespaced layout, with the
// meta/config dirs siblings of data — the place a rustfs copy-paste would go
// silently wrong (a garage instance writing under addon/rustfs).
func TestGaragePathResolution(t *testing.T) {
	m := &GarageManager{dataDir: "/base"}
	gc := &config.GarageConfig{Name: "g1"}

	if got := m.resolveDataDir(gc); got != "/base/addon/garage/g1/data" {
		t.Errorf("data dir = %q, want .../addon/garage/g1/data", got)
	}
	if got := m.resolveMetadataDir(gc); got != "/base/addon/garage/g1/meta" {
		t.Errorf("meta dir = %q, want .../addon/garage/g1/meta", got)
	}
	if got := m.resolveConfigFile(gc); got != "/base/addon/garage/g1/garage.toml" {
		t.Errorf("config file = %q, want .../addon/garage/g1/garage.toml", got)
	}
	// Explicit DataDir override relocates all three (meta/toml stay siblings).
	rc := &config.GarageConfig{Name: "g1", DataDir: "/srv/garage"}
	if got := m.resolveDataDir(rc); got != "/srv/garage" {
		t.Errorf("override data dir = %q, want /srv/garage", got)
	}
	if got := m.resolveMetadataDir(rc); got != "/srv/meta" {
		t.Errorf("override meta dir = %q, want /srv/meta", got)
	}
	// ConfigFile override wins outright and is never rewritten (WriteConfig
	// stats it instead).
	sc := &config.GarageConfig{Name: "g1", ConfigFile: "/etc/garage.toml"}
	if got := m.resolveConfigFile(sc); got != "/etc/garage.toml" {
		t.Errorf("override config file = %q, want /etc/garage.toml", got)
	}
}

func TestGarageWriteConfig(t *testing.T) {
	dir := t.TempDir()
	m := &GarageManager{dataDir: dir}
	gc := &config.GarageConfig{
		Name: "g1", Region: "garage", ReplicationFactor: 1,
		RPCSecret: "aa", Listen: "127.0.0.1",
		APIPort: 3900, RPCPort: 3901, WebPort: 3902, AdminPort: 3903,
	}
	path, err := m.WriteConfig(gc)
	if err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if want := dir + "/addon/garage/g1/garage.toml"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	// The file embeds rpc_secret — it must not be world-readable.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}
