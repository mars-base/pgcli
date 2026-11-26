package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func valkeyReplicaCfg() *config.Config {
	cfg := config.Default()
	cfg.Addons.Valkey = map[string]config.ValkeyConfig{
		"vcache": {
			Name:     "vcache",
			Version:  "9",
			ImageTag: "docker.io/valkey/valkey:9.2.0",
			Password: "pw-vcache",
			Port:     6380,
		},
	}
	return cfg
}

func TestResolveValkeyReplicaLocal(t *testing.T) {
	cfg := valkeyReplicaCfg()
	rc := config.RedisConfig{Name: "vcache-r"}
	if err := resolveValkeyReplica(cfg, &rc, "vcache-r", "vcache", "", "", 0, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ReplicaHost != "127.0.0.1" || rc.ReplicaPort != 6380 {
		t.Errorf("target = %q:%d, want 127.0.0.1:6380", rc.ReplicaHost, rc.ReplicaPort)
	}
	if rc.Password != "pw-vcache" {
		t.Errorf("password = %q, want the master's", rc.Password)
	}
	if rc.Version != "9" {
		t.Errorf("version = %q, want the master's 9", rc.Version)
	}
}

// A local --replica-of may only resolve against the valkey map — a redis
// addon of the same name must not serve as the master.
func TestResolveValkeyReplicaIgnoresRedisNamespace(t *testing.T) {
	cfg := valkeyReplicaCfg()
	cfg.Addons.Redis = map[string]config.RedisConfig{
		"ronly": {Name: "ronly", Version: "8", Password: "pw-redis", Port: 6379},
	}
	rc := config.RedisConfig{Name: "vr"}
	err := resolveValkeyReplica(cfg, &rc, "vr", "ronly", "", "", 0, false)
	if err == nil {
		t.Fatal("a redis addon must not be accepted as a valkey replica master")
	}
	if !strings.Contains(err.Error(), "valkey") {
		t.Errorf("error should name the valkey addon type: %v", err)
	}
	if !strings.Contains(err.Error(), "vcache") {
		t.Errorf("error should list installed valkey addons: %v", err)
	}
}

func TestResolveValkeyReplicaMajorMismatch(t *testing.T) {
	cfg := valkeyReplicaCfg()
	rc := config.RedisConfig{Name: "vcache-r"}
	err := resolveValkeyReplica(cfg, &rc, "vcache-r", "vcache", "", "7", 0, false)
	if err == nil {
		t.Fatal("--version 7 against a 9 master must be rejected")
	}
	if !strings.Contains(err.Error(), "Valkey") {
		t.Errorf("error should name Valkey as the product: %v", err)
	}
}

func TestResolveValkeyReplicaPasswordMustMatch(t *testing.T) {
	cfg := valkeyReplicaCfg()
	rc := config.RedisConfig{Name: "vcache-r", Password: "someone-elses"}
	err := resolveValkeyReplica(cfg, &rc, "vcache-r", "vcache", "", "", 0, true)
	if err == nil {
		t.Fatal("a differing --password must be rejected")
	}
	if !strings.Contains(err.Error(), "must equal") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveValkeyReplicaRemote(t *testing.T) {
	cfg := valkeyReplicaCfg()
	rc := config.RedisConfig{Name: "remote-r", Version: "8", Password: "pw-remote"}
	if err := resolveValkeyReplica(cfg, &rc, "remote-r", "", "10.0.0.9", "", 6380, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ReplicaHost != "10.0.0.9" || rc.ReplicaPort != 6380 || rc.Password != "pw-remote" {
		t.Errorf("remote replica resolved wrongly: %+v", rc)
	}
}

func TestResolveValkeyReplicaSelfReference(t *testing.T) {
	cfg := valkeyReplicaCfg()
	rc := config.RedisConfig{Name: "vcache"}
	if err := resolveValkeyReplica(cfg, &rc, "vcache", "vcache", "", "", 0, false); err == nil {
		t.Fatal("an instance cannot replicate itself")
	}
}

func TestResolveValkeyReplicaBothFlags(t *testing.T) {
	cfg := valkeyReplicaCfg()
	rc := config.RedisConfig{Name: "vcache-r", Password: "pw"}
	err := resolveValkeyReplica(cfg, &rc, "vcache-r", "vcache", "10.0.0.9", "", 6380, true)
	if err == nil {
		t.Fatal("--replica-of and --replica-of-host together must be rejected")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveValkeyReplicaRemoteNeedsPortAndPassword(t *testing.T) {
	cfg := valkeyReplicaCfg()
	rc := config.RedisConfig{Name: "remote-r", Password: "pw"}
	if err := resolveValkeyReplica(cfg, &rc, "remote-r", "", "10.0.0.9", "", 0, true); err == nil {
		t.Fatal("--replica-of-host without --replica-of-port must error")
	}
	rc2 := config.RedisConfig{Name: "remote-r"}
	if err := resolveValkeyReplica(cfg, &rc2, "remote-r", "", "10.0.0.9", "", 6380, false); err == nil {
		t.Fatal("a remote replica without --password must error")
	}
}
