package config

import (
	"path/filepath"
	"testing"
)

func TestSeaweedfsDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.Addons.Seaweedfs = map[string]SeaweedfsConfig{
		"s1": {},
	}

	cfg.ApplyDefaults()

	sc, ok := cfg.Addons.Seaweedfs["s1"]
	if !ok {
		t.Fatal("seaweedfs addon missing after ApplyDefaults")
	}
	if sc.Name != "s1" {
		t.Errorf("name = %q, want s1", sc.Name)
	}
	if sc.ContainerName != "pgcli-seaweedfs-s1" {
		t.Errorf("container name = %q, want pgcli-seaweedfs-s1", sc.ContainerName)
	}
	if sc.ImageTag != DefaultSeaweedfsImageTag {
		t.Errorf("image tag = %q, want %q", sc.ImageTag, DefaultSeaweedfsImageTag)
	}
	if sc.Listen != "127.0.0.1" {
		t.Errorf("listen = %q, want 127.0.0.1", sc.Listen)
	}
	if sc.Replication != "000" {
		t.Errorf("replication = %q, want 000", sc.Replication)
	}
	if sc.VolumeSizeMB != 1024 {
		t.Errorf("volume size = %d, want 1024", sc.VolumeSizeMB)
	}
	// The bucket defaults to the instance name (after addon.Name is filled).
	if sc.Bucket != "s1" {
		t.Errorf("bucket = %q, want s1", sc.Bucket)
	}
	// Credentials and the filer DSN are the install handler's job — ApplyDefaults
	// must NOT invent an access/secret pair or resolve a DSN on its own.
	if sc.RootUser != "" || sc.RootPassword != "" || sc.FilerDSN != "" {
		t.Errorf("secrets/DSN must stay empty through ApplyDefaults: %q %q %q", sc.RootUser, sc.RootPassword, sc.FilerDSN)
	}
	// Four consecutive ports from SeaweedFS's own pool (default base 8400).
	if sc.Port < cfg.SeaweedfsStartPort {
		t.Errorf("master port %d below base %d", sc.Port, cfg.SeaweedfsStartPort)
	}
	if sc.VolumePort != sc.Port+1 || sc.FilerPort != sc.Port+2 || sc.S3Port != sc.Port+3 {
		t.Errorf("ports not consecutive from master %d: vol=%d filer=%d s3=%d", sc.Port, sc.VolumePort, sc.FilerPort, sc.S3Port)
	}
	if sc.Clustered() {
		t.Error("empty Cluster must not report clustered")
	}
	if sc.TLSOn() {
		t.Error("no TLS flags must not report TLSOn")
	}
}

func TestSeaweedfsTwoInstancesDoNotOverlap(t *testing.T) {
	cfg := Default()
	cfg.Addons.Seaweedfs = map[string]SeaweedfsConfig{
		"s1": {},
		"s2": {},
	}

	cfg.ApplyDefaults()

	s1 := cfg.Addons.Seaweedfs["s1"]
	s2 := cfg.Addons.Seaweedfs["s2"]
	set := map[int]string{}
	for _, p := range []int{s1.Port, s1.VolumePort, s1.FilerPort, s1.S3Port} {
		set[p] = "s1"
	}
	for _, p := range []int{s2.Port, s2.VolumePort, s2.FilerPort, s2.S3Port} {
		if owner, dup := set[p]; dup {
			t.Fatalf("port %d assigned to both %s and s2", p, owner)
		}
	}
	if s2.Port != s1.S3Port+1 {
		t.Errorf("s2 master %d should continue after s1 s3 %d", s2.Port, s1.S3Port)
	}
}

// Every SeaweedFS role also opens a gRPC port at http+10000. The pool never
// assigns those, but must record them as taken so a LATER instance's four http
// ports never walk into an EARLIER instance's gRPC range. Crafted by pinning
// one instance so its gRPC twins land exactly on the shared base, forcing the
// second instance to skip them.
func TestSeaweedfsReservesGRPCtwins(t *testing.T) {
	cfg := Default()
	cfg.SeaweedfsStartPort = 18400
	cfg.Addons.Seaweedfs = map[string]SeaweedfsConfig{
		// "a" is sorted first and pinned to 8400..8403, whose gRPC twins are
		// exactly 18400..18403 — the base the auto pool would otherwise start at.
		"a": {Port: 8400, VolumePort: 8401, FilerPort: 8402, S3Port: 8403},
		"b": {},
	}

	cfg.ApplyDefaults()

	b := cfg.Addons.Seaweedfs["b"]
	for _, p := range []int{b.Port, b.VolumePort, b.FilerPort, b.S3Port} {
		if p >= 18400 && p <= 18403 {
			t.Errorf("instance b landed on a's gRPC range: port %d (a's twins are 18400-18403)", p)
		}
	}
	if b.Port != 18404 {
		t.Errorf("b master = %d, want 18404 (first port past a's gRPC twins)", b.Port)
	}
}

// SeaweedFS's pool must not collide with the shared minio/silo/rustfs cursor,
// the Garage pool, or the nginx pool when all coexist at defaults.
func TestSeaweedfsPoolIndependentOfOtherStores(t *testing.T) {
	cfg := Default()
	cfg.Addons.Seaweedfs = map[string]SeaweedfsConfig{"s1": {}}
	cfg.Addons.Garage = map[string]GarageConfig{"g1": {}}
	cfg.Addons.Rustfs = map[string]RustfsConfig{"r1": {}}
	cfg.Addons.Nginx = map[string]NginxConfig{"web": {}}

	cfg.ApplyDefaults()

	s := cfg.Addons.Seaweedfs["s1"]
	g := cfg.Addons.Garage["g1"]
	r := cfg.Addons.Rustfs["r1"]
	n := cfg.Addons.Nginx["web"]
	for _, p := range []int{s.Port, s.VolumePort, s.FilerPort, s.S3Port} {
		if p == g.APIPort || p == r.APIPort || p == r.ConsolePort || p == n.HTTPPort {
			t.Errorf("seaweedfs port %d collides (garage=%d rustfs=%d/%d nginx=%d)",
				p, g.APIPort, r.APIPort, r.ConsolePort, n.HTTPPort)
		}
	}
}

func TestSeaweedfsSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.Seaweedfs = map[string]SeaweedfsConfig{
		"s1": {
			ContainerName: "pgcli-seaweedfs-s1",
			Name:          "s1",
			ImageTag:      DefaultSeaweedfsImageTag,
			DataDir:       "/srv/sw/data",
			Listen:        "127.0.0.1",
			Port:          8400,
			VolumePort:    8401,
			FilerPort:     8402,
			S3Port:        8403,
			RootUser:      "SWabcdef",
			RootPassword:  "s3cret",
			Bucket:        "databucket",
			Replication:   "001",
			Cluster:       "c1",
			AdvertiseHost: "10.0.0.11",
			Peers:         []string{"10.0.0.11:8400", "10.0.0.12:8400"},
			FilerDSN:      "postgres://admin:pw@127.0.0.1:35432/seaweed_s1",
			TLS:           true,
			CertFile:      "/srv/sw/cert.pem",
			KeyFile:       "/srv/sw/key.pem",
			VolumeSizeMB:  1024,
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
	sc, ok := got.Addons.Seaweedfs["s1"]
	if !ok {
		t.Fatal("seaweedfs addon not persisted")
	}
	if sc.RootUser != "SWabcdef" || sc.RootPassword != "s3cret" {
		t.Errorf("credentials lost in round-trip: %q %q", sc.RootUser, sc.RootPassword)
	}
	if !sc.Clustered() || sc.Cluster != "c1" || sc.AdvertiseHost != "10.0.0.11" {
		t.Errorf("cluster fields lost: %+v", sc)
	}
	if sc.Replication != "001" || sc.Bucket != "databucket" {
		t.Errorf("replication/bucket lost: %q/%q", sc.Replication, sc.Bucket)
	}
	if len(sc.Peers) != 2 || sc.Peers[1] != "10.0.0.12:8400" {
		t.Errorf("peers lost: %v", sc.Peers)
	}
	if sc.FilerDSN == "" || !sc.TLS || !sc.TLSOn() {
		t.Errorf("filer dsn/tls lost: dsn=%q tls=%v tlson=%v", sc.FilerDSN, sc.TLS, sc.TLSOn())
	}
	if sc.CertFile != "/srv/sw/cert.pem" || sc.KeyFile != "/srv/sw/key.pem" {
		t.Errorf("byo cert/key lost: %q %q", sc.CertFile, sc.KeyFile)
	}
	if sc.VolumeSizeMB != 1024 || !sc.Autostart {
		t.Errorf("volume size/autostart lost: %d %v", sc.VolumeSizeMB, sc.Autostart)
	}
	if got.SeaweedfsStartPort != 8400 {
		t.Errorf("seaweedfs_start_port = %d, want 8400", got.SeaweedfsStartPort)
	}
}

// TLSOn is true for either the generated (--tls) or bring-your-own cert path.
// A lone cert/key is treated as "intends TLS" too — pairing is enforced by
// Validate at install, not here.
func TestSeaweedfsTLSOn(t *testing.T) {
	if !(SeaweedfsConfig{TLS: true}).TLSOn() {
		t.Error("--tls must report TLSOn")
	}
	if !(SeaweedfsConfig{CertFile: "c.pem", KeyFile: "k.pem"}).TLSOn() {
		t.Error("BYO cert/key must report TLSOn")
	}
	if !(SeaweedfsConfig{CertFile: "c.pem"}).TLSOn() {
		t.Error("a lone cert file still intends TLS (Validate catches the missing key)")
	}
	if (SeaweedfsConfig{}).TLSOn() {
		t.Error("no TLS flags must not report TLSOn")
	}
}

func TestSeaweedfsGRPCPort(t *testing.T) {
	if got := SeaweedfsGRPCPort(8400); got != 18400 {
		t.Errorf("grpc twin of 8400 = %d, want 18400", got)
	}
}
