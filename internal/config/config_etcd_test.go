package config

import (
	"path/filepath"
	"testing"
)

func TestEtcdDefaultsAndPortAssignment(t *testing.T) {
	cfg := Default()
	cfg.Addons.Etcd = map[string]EtcdConfig{
		"ha": {Name: "ha"},
	}

	cfg.ApplyDefaults()

	ec, ok := cfg.Addons.Etcd["ha"]
	if !ok {
		t.Fatal("etcd addon missing after ApplyDefaults")
	}
	if ec.ContainerName != "pgcli-etcd-ha" {
		t.Errorf("container name = %q, want pgcli-etcd-ha", ec.ContainerName)
	}
	if ec.ImageTag != "quay.io/coreos/etcd:v3.5.30" {
		t.Errorf("image tag = %q, want quay.io/coreos/etcd:v3.5.30", ec.ImageTag)
	}
	if ec.ClientPort < cfg.EtcdStartPort {
		t.Errorf("client port %d below base %d", ec.ClientPort, cfg.EtcdStartPort)
	}
	if ec.PeerPort <= ec.ClientPort {
		t.Errorf("peer port %d must be above client port %d", ec.PeerPort, ec.ClientPort)
	}
}

func TestEtcdSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{"a": {}}
	cfg.Addons.Etcd = map[string]EtcdConfig{
		"ha": {ClientPort: 2379, PeerPort: 2380},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ec, ok := got.Addons.Etcd["ha"]
	if !ok {
		t.Fatal("etcd addon not persisted")
	}
	if ec.ClientPort != 2379 || ec.PeerPort != 2380 {
		t.Errorf("ports not persisted: client=%d peer=%d", ec.ClientPort, ec.PeerPort)
	}
}
