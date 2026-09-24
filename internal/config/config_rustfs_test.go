package config

import (
	"path/filepath"
	"testing"
)

func TestRustfsDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.Addons.Rustfs = map[string]RustfsConfig{
		"r1": {},
	}

	cfg.ApplyDefaults()

	rc, ok := cfg.Addons.Rustfs["r1"]
	if !ok {
		t.Fatal("rustfs addon missing after ApplyDefaults")
	}
	if rc.Name != "r1" {
		t.Errorf("name = %q, want r1", rc.Name)
	}
	if rc.ContainerName != "pgcli-rustfs-r1" {
		t.Errorf("container name = %q, want pgcli-rustfs-r1", rc.ContainerName)
	}
	if rc.ImageTag != DefaultRustfsImageTag {
		t.Errorf("image tag = %q, want %q", rc.ImageTag, DefaultRustfsImageTag)
	}
	if rc.Listen != "127.0.0.1" {
		t.Errorf("listen = %q, want 127.0.0.1", rc.Listen)
	}
	if rc.RootUser != "admin" {
		t.Errorf("root user = %q, want admin", rc.RootUser)
	}
	// DataDir is resolved by the manager at container-creation time, not here.
	if rc.DataDir != "" {
		t.Errorf("data dir = %q, want empty (manager-resolved)", rc.DataDir)
	}
	if rc.APIPort < cfg.MinioStartPort {
		t.Errorf("api port %d below base %d", rc.APIPort, cfg.MinioStartPort)
	}
	if rc.ConsolePort <= rc.APIPort {
		t.Errorf("console port %d must be above api port %d", rc.ConsolePort, rc.APIPort)
	}
}

func TestRustfsSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.Rustfs = map[string]RustfsConfig{
		"r1": {
			ContainerName: "pgcli-rustfs-r1",
			Name:          "r1",
			ImageTag:      DefaultRustfsImageTag,
			DataDir:       "/srv/rustfs",
			Listen:        "127.0.0.1",
			APIPort:       9100,
			ConsolePort:   9101,
			RootUser:      "admin",
			RootPassword:  "s3cret",
			TLS:           true,
			CertFile:      "/etc/ssl/rustfs.crt",
			KeyFile:       "/etc/ssl/rustfs.key",
			Endpoints:     []string{"http://10.0.0.1:9100", "http://10.0.0.2:9100"},
			Autostart:     true,
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rc, ok := got.Addons.Rustfs["r1"]
	if !ok {
		t.Fatal("rustfs addon not persisted")
	}
	if rc.APIPort != 9100 || rc.ConsolePort != 9101 {
		t.Errorf("ports not persisted: api=%d console=%d", rc.APIPort, rc.ConsolePort)
	}
	if rc.DataDir != "/srv/rustfs" {
		t.Errorf("data dir not persisted: %q", rc.DataDir)
	}
	if rc.RootUser != "admin" || rc.RootPassword != "s3cret" {
		t.Errorf("credentials not persisted: user=%q", rc.RootUser)
	}
	if !rc.TLS || rc.CertFile != "/etc/ssl/rustfs.crt" || rc.KeyFile != "/etc/ssl/rustfs.key" {
		t.Errorf("TLS fields not persisted: tls=%v cert=%q key=%q", rc.TLS, rc.CertFile, rc.KeyFile)
	}
	if len(rc.Endpoints) != 2 {
		t.Errorf("endpoints not persisted: %v", rc.Endpoints)
	}
	if !rc.Autostart {
		t.Error("autostart not persisted")
	}

	// A second ApplyDefaults must not move already-assigned ports.
	got.ApplyDefaults()
	again := got.Addons.Rustfs["r1"]
	if again.APIPort != 9100 || again.ConsolePort != 9101 {
		t.Errorf("ApplyDefaults re-assigned ports: %d/%d", again.APIPort, again.ConsolePort)
	}
}

// The rustfs twin of the minio/silo drives round-trip: order preserved, key
// omitted when empty.
func TestRustfsDrivesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := Default()
	cfg.Addons.Rustfs = map[string]RustfsConfig{
		"r1": {ContainerName: "pgcli-rustfs-r1", Name: "r1",
			Drives: []string{"/mnt/d1", "/mnt/d2"}},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if d := got.Addons.Rustfs["r1"].Drives; len(d) != 2 || d[0] != "/mnt/d1" || d[1] != "/mnt/d2" {
		t.Errorf("rustfs drives round-trip = %v, want [/mnt/d1 /mnt/d2]", d)
	}
}

// rustfs MNMD endpoints carry no per-drive path (the drive suffix comes from
// the shared RUSTFS_VOLUMES brace range, assembled by the runtime, not stored
// per-endpoint), unlike MinIO/silo whose endpoint list enumerates every drive
// slot. This test pins that difference survives round-trip.
func TestRustfsMNMDRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := Default()
	cfg.Addons.Rustfs = map[string]RustfsConfig{
		"r1": {
			ContainerName: "pgcli-rustfs-r1",
			Name:          "r1",
			Drives:        []string{"/mnt/d1", "/mnt/d2"},
			Endpoints: []string{
				"http://10.0.0.1:9000", "http://10.0.0.2:9000",
			},
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rc := got.Addons.Rustfs["r1"]
	if len(rc.Drives) != 2 || rc.Drives[0] != "/mnt/d1" {
		t.Errorf("rustfs MNMD drives round-trip = %v", rc.Drives)
	}
	if len(rc.Endpoints) != 2 || rc.Endpoints[1] != "http://10.0.0.2:9000" {
		t.Errorf("rustfs MNMD endpoints round-trip = %v", rc.Endpoints)
	}
}

// The shared-pool contract extended to three stores: minio, silo, and rustfs
// instances on one host auto-assign from the same cursor in table order
// (minio → silo → rustfs), so their port pairs never overlap. A high unused
// base keeps the live-listener scan from perturbing exact expectations
// (same convention as TestMinioSiloSharedPortPool).
func TestMinioSiloRustfsSharedPortPool(t *testing.T) {
	cfg := Default()
	cfg.MinioStartPort = 18800
	cfg.Addons.Minio = map[string]MinioConfig{
		"store": {},
	}
	cfg.Addons.Silo = map[string]SiloConfig{
		"s1": {},
	}
	cfg.Addons.Rustfs = map[string]RustfsConfig{
		"r1": {},
		"r2": {APIPort: 19876, ConsolePort: 19877},
		"r3": {},
	}

	cfg.ApplyDefaults()

	m := cfg.Addons.Minio["store"]
	s1 := cfg.Addons.Silo["s1"]
	r1 := cfg.Addons.Rustfs["r1"]
	r2 := cfg.Addons.Rustfs["r2"]
	r3 := cfg.Addons.Rustfs["r3"]

	if m.APIPort != 18800 || m.ConsolePort != 18801 {
		t.Errorf("minio store = %d/%d, want 18800/18801", m.APIPort, m.ConsolePort)
	}
	if s1.APIPort != 18802 || s1.ConsolePort != 18803 {
		t.Errorf("silo s1 = %d/%d, want 18802/18803", s1.APIPort, s1.ConsolePort)
	}
	if r1.APIPort != 18804 || r1.ConsolePort != 18805 {
		t.Errorf("rustfs r1 = %d/%d, want 18804/18805", r1.APIPort, r1.ConsolePort)
	}
	if r2.APIPort != 19876 || r2.ConsolePort != 19877 {
		t.Errorf("explicit r2 ports changed: %d/%d", r2.APIPort, r2.ConsolePort)
	}
	// r2's explicit pair pushes the cursor above it before r3 draws — the same
	// within-table semantics minio/silo have always had.
	if r3.APIPort != 19878 || r3.ConsolePort != 19879 {
		t.Errorf("rustfs r3 = %d/%d, want 19878/19879", r3.APIPort, r3.ConsolePort)
	}

	// No two of the five instances may share either port.
	seen := map[int]string{}
	for name, pair := range map[string][2]int{
		"minio/store": {m.APIPort, m.ConsolePort},
		"silo/s1":     {s1.APIPort, s1.ConsolePort},
		"rustfs/r1":   {r1.APIPort, r1.ConsolePort},
		"rustfs/r2":   {r2.APIPort, r2.ConsolePort},
		"rustfs/r3":   {r3.APIPort, r3.ConsolePort},
	} {
		for _, p := range pair {
			if other, dup := seen[p]; dup {
				t.Errorf("port %d shared by %s and %s", p, other, name)
			}
			seen[p] = name
		}
	}
}

// An explicit rustfs port in the pool range must push auto-assignments past it
// — rustfs reserves feed the same `assigned` set minio and silo draw from.
func TestRustfsExplicitPortReservesForMinio(t *testing.T) {
	cfg := Default()
	cfg.MinioStartPort = 18800
	base := cfg.MinioStartPort
	cfg.Addons.Rustfs = map[string]RustfsConfig{
		"r1": {APIPort: base, ConsolePort: base + 1},
	}
	cfg.Addons.Minio = map[string]MinioConfig{
		"store": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.Rustfs["r1"]; got.APIPort != base || got.ConsolePort != base+1 {
		t.Fatalf("explicit rustfs ports changed: %d/%d", got.APIPort, got.ConsolePort)
	}
	m := cfg.Addons.Minio["store"]
	// minio is assigned first, but rustfs's explicit pair sits in the reserved
	// set the cursor skips — so minio starts drawing above it.
	if m.APIPort != base+2 || m.ConsolePort != base+3 {
		t.Errorf("minio = %d/%d, want %d/%d (cursor must skip rustfs's reserved pair)",
			m.APIPort, m.ConsolePort, base+2, base+3)
	}
}
