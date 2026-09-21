package config

import (
	"path/filepath"
	"testing"
)

func TestSiloDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.Addons.Silo = map[string]SiloConfig{
		"s1": {},
	}

	cfg.ApplyDefaults()

	sc, ok := cfg.Addons.Silo["s1"]
	if !ok {
		t.Fatal("silo addon missing after ApplyDefaults")
	}
	if sc.Name != "s1" {
		t.Errorf("name = %q, want s1", sc.Name)
	}
	if sc.ContainerName != "pgcli-silo-s1" {
		t.Errorf("container name = %q, want pgcli-silo-s1", sc.ContainerName)
	}
	if sc.ImageTag != DefaultSiloImageTag {
		t.Errorf("image tag = %q, want %q", sc.ImageTag, DefaultSiloImageTag)
	}
	if sc.Listen != "127.0.0.1" {
		t.Errorf("listen = %q, want 127.0.0.1", sc.Listen)
	}
	if sc.RootUser != "admin" {
		t.Errorf("root user = %q, want admin", sc.RootUser)
	}
	// DataDir is resolved by the manager at container-creation time, not here.
	if sc.DataDir != "" {
		t.Errorf("data dir = %q, want empty (manager-resolved)", sc.DataDir)
	}
	if sc.APIPort < cfg.MinioStartPort {
		t.Errorf("api port %d below base %d", sc.APIPort, cfg.MinioStartPort)
	}
	if sc.ConsolePort <= sc.APIPort {
		t.Errorf("console port %d must be above api port %d", sc.ConsolePort, sc.APIPort)
	}
}

func TestSiloSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.Silo = map[string]SiloConfig{
		"s1": {
			ContainerName: "pgcli-silo-s1",
			Name:          "s1",
			ImageTag:      DefaultSiloImageTag,
			DataDir:       "/srv/silo",
			Listen:        "127.0.0.1",
			APIPort:       9100,
			ConsolePort:   9101,
			RootUser:      "admin",
			RootPassword:  "s3cret",
			TLS:           true,
			CertFile:      "/etc/ssl/silo.crt",
			KeyFile:       "/etc/ssl/silo.key",
			Endpoints:     []string{"http://10.0.0.1:9100/data", "http://10.0.0.2:9100/data"},
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
	sc, ok := got.Addons.Silo["s1"]
	if !ok {
		t.Fatal("silo addon not persisted")
	}
	if sc.APIPort != 9100 || sc.ConsolePort != 9101 {
		t.Errorf("ports not persisted: api=%d console=%d", sc.APIPort, sc.ConsolePort)
	}
	if sc.DataDir != "/srv/silo" {
		t.Errorf("data dir not persisted: %q", sc.DataDir)
	}
	if sc.RootUser != "admin" || sc.RootPassword != "s3cret" {
		t.Errorf("credentials not persisted: user=%q", sc.RootUser)
	}
	if !sc.TLS || sc.CertFile != "/etc/ssl/silo.crt" || sc.KeyFile != "/etc/ssl/silo.key" {
		t.Errorf("TLS fields not persisted: tls=%v cert=%q key=%q", sc.TLS, sc.CertFile, sc.KeyFile)
	}
	if len(sc.Endpoints) != 2 {
		t.Errorf("endpoints not persisted: %v", sc.Endpoints)
	}
	if !sc.Autostart {
		t.Error("autostart not persisted")
	}

	// A second ApplyDefaults must not move already-assigned ports.
	got.ApplyDefaults()
	again := got.Addons.Silo["s1"]
	if again.APIPort != 9100 || again.ConsolePort != 9101 {
		t.Errorf("ApplyDefaults re-assigned ports: %d/%d", again.APIPort, again.ConsolePort)
	}
}

// The silo twin of the minio drives round-trip: order preserved, key omitted
// when empty.
func TestSiloDrivesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := Default()
	cfg.Addons.Silo = map[string]SiloConfig{
		"s1": {ContainerName: "pgcli-silo-s1", Name: "s1",
			Drives: []string{"/mnt/d1", "/mnt/d2"}},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if d := got.Addons.Silo["s1"].Drives; len(d) != 2 || d[0] != "/mnt/d1" || d[1] != "/mnt/d2" {
		t.Errorf("silo drives round-trip = %v, want [/mnt/d1 /mnt/d2]", d)
	}
}

// The silo twin of the minio MNMD round-trip: drives and endpoints coexist and
// both persist in order.
func TestSiloMNMDRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")
	cfg := Default()
	cfg.Addons.Silo = map[string]SiloConfig{
		"s1": {
			ContainerName: "pgcli-silo-s1",
			Name:          "s1",
			Drives:        []string{"/mnt/d1", "/mnt/d2"},
			Endpoints: []string{
				"http://10.0.0.1:9000/data1", "http://10.0.0.1:9000/data2",
				"http://10.0.0.2:9000/data1", "http://10.0.0.2:9000/data2",
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
	sc := got.Addons.Silo["s1"]
	if len(sc.Drives) != 2 || sc.Drives[0] != "/mnt/d1" {
		t.Errorf("silo MNMD drives round-trip = %v", sc.Drives)
	}
	if len(sc.Endpoints) != 4 || sc.Endpoints[3] != "http://10.0.0.2:9000/data2" {
		t.Errorf("silo MNMD endpoints round-trip = %v", sc.Endpoints)
	}
}

// The shared-pool contract: minio and silo instances on one host auto-assign
// from the same cursor, so their port pairs never overlap. Names sort within
// each table; the minio table is assigned before silo. The base is a high
// unused port so the live-listener scan cannot perturb exact expectations
// (same convention as TestMinioManualPortCursor's explicit pair).
func TestMinioSiloSharedPortPool(t *testing.T) {
	cfg := Default()
	cfg.MinioStartPort = 18800
	cfg.Addons.Minio = map[string]MinioConfig{
		"store": {},
	}
	cfg.Addons.Silo = map[string]SiloConfig{
		"s1": {},
		"s2": {APIPort: 19876, ConsolePort: 19877},
		"s3": {},
	}

	cfg.ApplyDefaults()

	m := cfg.Addons.Minio["store"]
	s1 := cfg.Addons.Silo["s1"]
	s2 := cfg.Addons.Silo["s2"]
	s3 := cfg.Addons.Silo["s3"]

	if m.APIPort != 18800 || m.ConsolePort != 18801 {
		t.Errorf("minio store = %d/%d, want 18800/18801", m.APIPort, m.ConsolePort)
	}
	if s1.APIPort != 18802 || s1.ConsolePort != 18803 {
		t.Errorf("silo s1 = %d/%d, want 18802/18803", s1.APIPort, s1.ConsolePort)
	}
	if s2.APIPort != 19876 || s2.ConsolePort != 19877 {
		t.Errorf("explicit s2 ports changed: %d/%d", s2.APIPort, s2.ConsolePort)
	}
	// s2's explicit pair pushes the cursor above it before s3 draws — the same
	// within-table semantics minio has always had.
	if s3.APIPort != 19878 || s3.ConsolePort != 19879 {
		t.Errorf("silo s3 = %d/%d, want 19878/19879", s3.APIPort, s3.ConsolePort)
	}

	// No two of the four instances may share either port.
	seen := map[int]string{}
	for name, pair := range map[string][2]int{
		"minio/store": {m.APIPort, m.ConsolePort},
		"silo/s1":     {s1.APIPort, s1.ConsolePort},
		"silo/s2":     {s2.APIPort, s2.ConsolePort},
		"silo/s3":     {s3.APIPort, s3.ConsolePort},
	} {
		for _, p := range pair {
			if other, dup := seen[p]; dup {
				t.Errorf("port %d shared by %s and %s", p, other, name)
			}
			seen[p] = name
		}
	}
}

// An explicit silo port in the pool range must push auto-assignments past it —
// silo reserves feed the same `assigned` set minio draws from.
func TestSiloExplicitPortReservesForMinio(t *testing.T) {
	cfg := Default()
	cfg.MinioStartPort = 18800
	base := cfg.MinioStartPort
	cfg.Addons.Silo = map[string]SiloConfig{
		"s1": {APIPort: base, ConsolePort: base + 1},
	}
	cfg.Addons.Minio = map[string]MinioConfig{
		"store": {},
	}

	cfg.ApplyDefaults()

	if got := cfg.Addons.Silo["s1"]; got.APIPort != base || got.ConsolePort != base+1 {
		t.Fatalf("explicit silo ports changed: %d/%d", got.APIPort, got.ConsolePort)
	}
	m := cfg.Addons.Minio["store"]
	// minio is assigned first, but silo's explicit pair sits in the reserved set
	// the cursor skips — so minio starts drawing above it.
	if m.APIPort != base+2 || m.ConsolePort != base+3 {
		t.Errorf("minio = %d/%d, want %d/%d (cursor must skip silo's reserved pair)",
			m.APIPort, m.ConsolePort, base+2, base+3)
	}
}
