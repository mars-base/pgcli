package config

import (
	"path/filepath"
	"testing"
)

func TestGarageDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.Addons.Garage = map[string]GarageConfig{
		"g1": {},
	}

	cfg.ApplyDefaults()

	gc, ok := cfg.Addons.Garage["g1"]
	if !ok {
		t.Fatal("garage addon missing after ApplyDefaults")
	}
	if gc.Name != "g1" {
		t.Errorf("name = %q, want g1", gc.Name)
	}
	if gc.ContainerName != "pgcli-garage-g1" {
		t.Errorf("container name = %q, want pgcli-garage-g1", gc.ContainerName)
	}
	if gc.ImageTag != DefaultGarageImageTag {
		t.Errorf("image tag = %q, want %q", gc.ImageTag, DefaultGarageImageTag)
	}
	if gc.Listen != "127.0.0.1" {
		t.Errorf("listen = %q, want 127.0.0.1", gc.Listen)
	}
	if gc.Region != "garage" {
		t.Errorf("region = %q, want garage", gc.Region)
	}
	if gc.ReplicationFactor != 1 {
		t.Errorf("replication factor = %d, want 1", gc.ReplicationFactor)
	}
	// Credentials are the install handler's job — ApplyDefaults must NOT invent
	// a GK-prefixed key or a secret on its own.
	if gc.RootUser != "" || gc.RootPassword != "" || gc.RPCSecret != "" {
		t.Errorf("secrets must stay empty through ApplyDefaults: %q %q %q", gc.RootUser, gc.RootPassword, gc.RPCSecret)
	}
	// Four consecutive ports from Garage's own pool (default base 3900).
	if gc.APIPort < cfg.GarageStartPort {
		t.Errorf("api port %d below base %d", gc.APIPort, cfg.GarageStartPort)
	}
	if gc.RPCPort != gc.APIPort+1 || gc.WebPort != gc.APIPort+2 || gc.AdminPort != gc.APIPort+3 {
		t.Errorf("ports not consecutive from api %d: rpc=%d web=%d admin=%d", gc.APIPort, gc.RPCPort, gc.WebPort, gc.AdminPort)
	}
}

func TestGarageTwoInstancesDoNotOverlap(t *testing.T) {
	cfg := Default()
	cfg.Addons.Garage = map[string]GarageConfig{
		"g1": {},
		"g2": {},
	}

	cfg.ApplyDefaults()

	g1 := cfg.Addons.Garage["g1"]
	g2 := cfg.Addons.Garage["g2"]
	set := map[int]string{}
	for _, p := range []int{g1.APIPort, g1.RPCPort, g1.WebPort, g1.AdminPort} {
		set[p] = "g1"
	}
	for _, p := range []int{g2.APIPort, g2.RPCPort, g2.WebPort, g2.AdminPort} {
		if owner, dup := set[p]; dup {
			t.Fatalf("port %d assigned to both %s and g2", p, owner)
		}
	}
	if g2.APIPort != g1.AdminPort+1 {
		t.Errorf("g2 api %d should continue after g1 admin %d", g2.APIPort, g1.AdminPort)
	}
}

// Garage's pool must not collide with the shared minio/silo/rustfs cursor or
// the nginx pool when all coexist at defaults.
func TestGaragePoolIndependentOfOtherStores(t *testing.T) {
	cfg := Default()
	cfg.Addons.Garage = map[string]GarageConfig{"g1": {}}
	cfg.Addons.Rustfs = map[string]RustfsConfig{"r1": {}}
	cfg.Addons.Nginx = map[string]NginxConfig{"web": {}}

	cfg.ApplyDefaults()

	g := cfg.Addons.Garage["g1"]
	r := cfg.Addons.Rustfs["r1"]
	n := cfg.Addons.Nginx["web"]
	if g.APIPort == r.APIPort || g.APIPort == r.ConsolePort {
		t.Errorf("garage api %d collides with rustfs %d/%d", g.APIPort, r.APIPort, r.ConsolePort)
	}
	if n.HTTPPort == g.APIPort {
		t.Errorf("nginx http port %d collides with garage api %d", n.HTTPPort, g.APIPort)
	}
}

func TestGarageSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.Garage = map[string]GarageConfig{
		"g1": {
			ContainerName: "pgcli-garage-g1",
			Name:          "g1",
			ImageTag:      DefaultGarageImageTag,
			DataDir:       "/srv/garage/data",
			// Drives is set here only to prove the list persists; the two are
			// mutually exclusive at install (CLI-enforced), not at the config
			// layer, so a round-trip can still carry both.
			Drives:            []string{"/mnt/garage/d1", "/mnt/garage/d2"},
			Listen:            "127.0.0.1",
			APIPort:           3900,
			RPCPort:           3901,
			WebPort:           3902,
			AdminPort:         3903,
			RootUser:          "GKabcdef",
			RootPassword:      "s3cret",
			RPCSecret:         "rpcsecret",
			Region:            "garage",
			ReplicationFactor: 1,
			Cluster:           "c1",
			AdvertiseHost:     "10.0.0.11",
			Zone:              "z1",
			Capacity:          15,
			Peers:             []string{"abcd@10.0.0.12:3901", "ef01@10.0.0.14:3901"},
			NodeID:            "39b9@10.0.0.11:3901",
			Autostart:         true,
		},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	gc, ok := got.Addons.Garage["g1"]
	if !ok {
		t.Fatal("garage addon not persisted")
	}
	if gc.RootUser != "GKabcdef" || gc.RootPassword != "s3cret" || gc.RPCSecret != "rpcsecret" {
		t.Errorf("credentials lost in round-trip: %q %q %q", gc.RootUser, gc.RootPassword, gc.RPCSecret)
	}
	if !gc.Autostart {
		t.Error("autostart lost in round-trip")
	}
	// Multi-node (v2) fields.
	if !gc.Clustered() || gc.Cluster != "c1" || gc.AdvertiseHost != "10.0.0.11" {
		t.Errorf("cluster fields lost: %+v", gc)
	}
	if gc.Zone != "z1" || gc.Capacity != 15 {
		t.Errorf("zone/capacity lost: %q/%d", gc.Zone, gc.Capacity)
	}
	if gc.DataDir != "/srv/garage/data" {
		t.Errorf("data dir lost: %q", gc.DataDir)
	}
	if d := gc.Drives; len(d) != 2 || d[0] != "/mnt/garage/d1" || d[1] != "/mnt/garage/d2" {
		t.Errorf("drives lost in round-trip: %v", gc.Drives)
	}
	if len(gc.Peers) != 2 || gc.Peers[0] != "abcd@10.0.0.12:3901" {
		t.Errorf("peers lost: %v", gc.Peers)
	}
	if gc.NodeID != "39b9@10.0.0.11:3901" {
		t.Errorf("node id lost: %q", gc.NodeID)
	}
	// rpc_public_addr brackets IPv6 and combines with the RPC port.
	gc.RPCPort = 3901
	if got := gc.RPCPublicAddr(); got != "10.0.0.11:3901" {
		t.Errorf("RPCPublicAddr = %q", got)
	}
	gc6 := gc
	gc6.AdvertiseHost = "fd00::1"
	if got := gc6.RPCPublicAddr(); got != "[fd00::1]:3901" {
		t.Errorf("IPv6 RPCPublicAddr = %q", got)
	}
	gc1 := gc
	gc1.AdvertiseHost = ""
	if got := gc1.RPCPublicAddr(); got != "" {
		t.Errorf("single-node RPCPublicAddr must be empty, got %q", got)
	}
	if got.GarageStartPort != 3900 {
		t.Errorf("garage_start_port = %d, want 3900", got.GarageStartPort)
	}
}
