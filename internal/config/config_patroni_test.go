package config

import (
	"path/filepath"
	"testing"
)

func TestPatroniDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.Addons.Patroni = map[string]PatroniClusterConfig{
		"app": {
			EtcdMembers: []string{"m1"},
			Passwords:   PatroniPasswords{Superuser: "su", Replication: "re", Rewind: "rw", RestapiUser: "pg", RestapiPasswd: "rp"},
			Members: map[string]PatroniMemberConfig{
				"node1": {},
				"node2": {},
			},
		},
	}

	cfg.ApplyDefaults()

	cl, ok := cfg.Addons.Patroni["app"]
	if !ok {
		t.Fatal("patroni cluster missing after ApplyDefaults")
	}
	if cl.Name != "app" {
		t.Errorf("cluster name = %q, want app", cl.Name)
	}

	n1 := cl.Members["node1"]
	if n1.ContainerName != "pgcli-patroni-app-node1" {
		t.Errorf("node1 container name = %q, want pgcli-patroni-app-node1", n1.ContainerName)
	}
	if n1.ImageTag != "ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5" {
		t.Errorf("node1 image tag = %q", n1.ImageTag)
	}
	if n1.DataDir == "" {
		t.Error("node1 data dir empty")
	}

	// Members are allocated in name order; each gets one PG port from the
	// 35532 pool and one REST port from the 8008 pool, independent sequences.
	if n1.HostPort < cfg.PatroniStartPort {
		t.Errorf("node1 host port %d below base %d", n1.HostPort, cfg.PatroniStartPort)
	}
	if n1.RestapiPort < cfg.PatroniRestapiStartPort {
		t.Errorf("node1 restapi port %d below base %d", n1.RestapiPort, cfg.PatroniRestapiStartPort)
	}
	n2 := cl.Members["node2"]
	if n2.HostPort <= n1.HostPort {
		t.Errorf("node2 host port %d must exceed node1 %d", n2.HostPort, n1.HostPort)
	}
	if n2.RestapiPort <= n1.RestapiPort {
		t.Errorf("node2 restapi port %d must exceed node1 %d", n2.RestapiPort, n1.RestapiPort)
	}
	// The two pools must never collide with each other.
	if n1.HostPort == n1.RestapiPort {
		t.Errorf("a member's PG and REST ports collide: %d", n1.HostPort)
	}
}

// The two Patroni pools must not steal an instance's or pgdog's ports.
func TestPatroniPortsDoNotCollideWithInstances(t *testing.T) {
	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{
		"default": {},
		"proj01":  {},
	}
	cfg.Addons.Patroni = map[string]PatroniClusterConfig{
		"app": {Members: map[string]PatroniMemberConfig{"node1": {}}},
	}

	cfg.ApplyDefaults()

	hostPorts := map[int]string{}
	for name, inst := range cfg.Instances {
		hostPorts[inst.Podman.HostPort] = "instance:" + name
	}
	for name, pd := range cfg.Addons.PgDog {
		hostPorts[pd.HostPort] = "pgdog:" + name
		hostPorts[pd.OpenmetricsPort] = "pgdog-metrics:" + name
	}
	for scope, cl := range cfg.Addons.Patroni {
		for m, mb := range cl.Members {
			if owner, dup := hostPorts[mb.HostPort]; dup {
				t.Errorf("patroni %s/%s PG port %d collides with %s", scope, m, mb.HostPort, owner)
			}
			hostPorts[mb.HostPort] = "patroni:" + scope + "/" + m
		}
	}
}

// An explicitly-set member port must be respected, not overwritten.
func TestPatroniExplicitPortsRespected(t *testing.T) {
	cfg := Default()
	cfg.Addons.Patroni = map[string]PatroniClusterConfig{
		"app": {Members: map[string]PatroniMemberConfig{
			"node1": {HostPort: 5432, RestapiPort: 8010},
			"node2": {},
		}},
	}

	cfg.ApplyDefaults()

	n1 := cfg.Addons.Patroni["app"].Members["node1"]
	if n1.HostPort != 5432 {
		t.Errorf("explicit host port overwritten to %d", n1.HostPort)
	}
	if n1.RestapiPort != 8010 {
		t.Errorf("explicit restapi port overwritten to %d", n1.RestapiPort)
	}
}

func TestPatroniNamespaceContainerName(t *testing.T) {
	cfg := Default()
	cfg.Namespace = "prod"
	cfg.Addons.Patroni = map[string]PatroniClusterConfig{
		"app": {Members: map[string]PatroniMemberConfig{"node1": {}}},
	}

	cfg.ApplyDefaults()

	n1 := cfg.Addons.Patroni["app"].Members["node1"]
	if n1.ContainerName != "pgcli-patroni-prod-app-node1" {
		t.Errorf("namespaced container name = %q, want pgcli-patroni-prod-app-node1", n1.ContainerName)
	}
}

func TestPatroniSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.PatroniStartPort = 35540
	cfg.PatroniRestapiStartPort = 8020
	cfg.Addons.Patroni = map[string]PatroniClusterConfig{
		"app": {
			EtcdEndpoints: []string{"10.0.0.9:2379", "10.0.0.10:2379"},
			Passwords:     PatroniPasswords{Superuser: "sup", Replication: "rep", Rewind: "rew", RestapiUser: "pg", RestapiPasswd: "rpw"},
			Members: map[string]PatroniMemberConfig{
				"node1": {AdvertiseHost: "10.0.0.11", HostPort: 5432, RestapiPort: 8008, Autostart: true},
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
	// The two new start-port bases must survive serialization (Display() gap).
	if got.PatroniStartPort != 35540 {
		t.Errorf("patroni_start_port not persisted: %d", got.PatroniStartPort)
	}
	if got.PatroniRestapiStartPort != 8020 {
		t.Errorf("patroni_restapi_start_port not persisted: %d", got.PatroniRestapiStartPort)
	}
	cl, ok := got.Addons.Patroni["app"]
	if !ok {
		t.Fatal("patroni cluster not persisted")
	}
	if len(cl.EtcdEndpoints) != 2 || cl.EtcdEndpoints[0] != "10.0.0.9:2379" {
		t.Errorf("etcd_endpoints not persisted: %+v", cl.EtcdEndpoints)
	}
	if cl.Passwords.Superuser != "sup" || cl.Passwords.RestapiPasswd != "rpw" {
		t.Errorf("passwords not persisted: %+v", cl.Passwords)
	}
	n1, ok := cl.Members["node1"]
	if !ok {
		t.Fatal("member node1 not persisted")
	}
	if n1.AdvertiseHost != "10.0.0.11" || n1.HostPort != 5432 || n1.RestapiPort != 8008 {
		t.Errorf("member not persisted correctly: %+v", n1)
	}
	if !n1.Autostart {
		t.Error("member autostart not persisted")
	}
}
