package podman

import (
	"os"
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
	out := renderGarageTOML(gc, "127.0.0.1")

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
	out6 := renderGarageTOML(gc, "::")
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
	out := renderGarageTOML(gc, "0.0.0.0")
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
	if !strings.Contains(renderGarageTOML(&gc6, "::"), `rpc_public_addr = "[fd00::1]:3901"`) {
		t.Error("IPv6 advertise host must be bracketed")
	}
	// Single-node (AdvertiseHost empty): no rpc_public_addr line at all.
	gc1 := *gc
	gc1.AdvertiseHost = ""
	if strings.Contains(renderGarageTOML(&gc1, "127.0.0.1"), "rpc_public_addr") {
		t.Error("single-node render must not carry rpc_public_addr")
	}
}

func TestGarageMountFlags(t *testing.T) {
	flags := garageMountFlags("/base/meta", "/base/data", "/base/garage.toml")
	want := []string{
		"-v", "/base/meta:/var/lib/garage/meta:z",
		"-v", "/base/data:/var/lib/garage/data:z",
		"-v", "/base/garage.toml:/etc/garage.toml:ro,z",
	}
	if strings.Join(flags, " ") != strings.Join(want, " ") {
		t.Errorf("mount flags =\n%v\nwant\n%v", flags, want)
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
