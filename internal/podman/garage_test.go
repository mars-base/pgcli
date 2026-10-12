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

func TestValidateGarageSingle(t *testing.T) {
	if err := ValidateGarageSingle(0); err != nil {
		t.Errorf("unset factor: %v", err)
	}
	if err := ValidateGarageSingle(1); err != nil {
		t.Errorf("factor 1: %v", err)
	}
	if err := ValidateGarageSingle(3); err == nil {
		t.Error("factor 3 must be rejected in v1 (single-node only)")
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
