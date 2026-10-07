package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// replicaCfg mirrors a host that already has a master installed: the local
// --replica-of path reads the master's Port/Password/Version from the config,
// so the fixture must look like a real post-install pg.yaml.
func replicaCfg() *config.Config {
	cfg := config.Default()
	cfg.Addons.Redis = map[string]config.RedisConfig{
		"cache": {
			Name:     "cache",
			Version:  "8",
			ImageTag: "docker.io/library/redis:8.10.2",
			Password: "pw-cache",
			Port:     6379,
		},
		"legacy": {
			Name:     "legacy",
			Version:  "7",
			ImageTag: "docker.io/library/redis:7.4.11",
			Password: "pw-legacy",
			Port:     6380,
		},
	}
	return cfg
}

func TestResolveRedisReplicaLocal(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "cache-r"}
	if err := resolveRedisReplica(cfg, &rc, "cache-r", "cache", "", "", 0, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The local master is reachable on the loopback the replica shares with it
	// (Linux host networking; the macOS bridge rewrite happens at the container
	// layer, not in the stored config).
	if rc.ReplicaHost != "127.0.0.1" || rc.ReplicaPort != 6379 {
		t.Errorf("target = %q:%d, want 127.0.0.1:6379", rc.ReplicaHost, rc.ReplicaPort)
	}
	if rc.Password != "pw-cache" {
		t.Errorf("password = %q, want the master's", rc.Password)
	}
}

// No --version: the replica adopts the master's major so the later version
// resolve re-derives a matching image tag.
func TestResolveRedisReplicaAdoptsMasterMajor(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "cache-r", Version: ""}
	if err := resolveRedisReplica(cfg, &rc, "cache-r", "cache", "", "", 0, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Version != "8" {
		t.Errorf("version = %q, want the master's 8", rc.Version)
	}
}

// A previously stored major that disagrees with the master is as much an error
// as an explicit --version mismatch: Redis cannot replicate across majors.
func TestResolveRedisReplicaStoredMajorMismatch(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "cache-r", Version: "7"}
	err := resolveRedisReplica(cfg, &rc, "cache-r", "cache", "", "", 0, false)
	if err == nil {
		t.Fatal("stale stored major must be rejected")
	}
	if !strings.Contains(err.Error(), "major") {
		t.Errorf("error should name the major: %v", err)
	}
}

func TestResolveRedisReplicaExplicitMajorMismatch(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "cache-r"}
	err := resolveRedisReplica(cfg, &rc, "cache-r", "cache", "", "7", 0, false)
	if err == nil {
		t.Fatal("--version 7 against an 8 master must be rejected")
	}
}

// --password is allowed only when it equals the master's — one password feeds
// both requirepass and masterauth.
func TestResolveRedisReplicaPasswordMustMatch(t *testing.T) {
	cfg := replicaCfg()

	rc := config.RedisConfig{Name: "cache-r", Password: "pw-cache"}
	if err := resolveRedisReplica(cfg, &rc, "cache-r", "cache", "", "", 0, true); err != nil {
		t.Fatalf("an equal explicit password should pass: %v", err)
	}

	rc = config.RedisConfig{Name: "cache-r", Password: "someone-elses"}
	err := resolveRedisReplica(cfg, &rc, "cache-r", "cache", "", "", 0, true)
	if err == nil {
		t.Fatal("a differing --password must be rejected")
	}
	if !strings.Contains(err.Error(), "must equal") {
		t.Errorf("unexpected error: %v", err)
	}
}

// A reinstall that passes no replica flags must not clear a stored role — that
// would silently drop --replicaof from the argv and promote the replica.
func TestResolveRedisReplicaPlainReinstallKeepsRole(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "cache-r", ReplicaHost: "127.0.0.1", ReplicaPort: 6379, Password: "pw-cache"}
	if err := resolveRedisReplica(cfg, &rc, "cache-r", "", "", "", 0, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ReplicaHost != "127.0.0.1" || rc.ReplicaPort != 6379 {
		t.Errorf("stored role was cleared: %q:%d", rc.ReplicaHost, rc.ReplicaPort)
	}
}

func TestResolveRedisReplicaMasterStaysMaster(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "cache", ReplicaHost: "", Password: "pw-cache"}
	if err := resolveRedisReplica(cfg, &rc, "cache", "", "", "8", 0, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ReplicaHost != "" {
		t.Errorf("a no-flag install must not gain a replica role: %q", rc.ReplicaHost)
	}
}

func TestResolveRedisReplicaUnknownMaster(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "cache-r"}
	err := resolveRedisReplica(cfg, &rc, "cache-r", "ghost", "", "", 0, false)
	if err == nil {
		t.Fatal("an uninstalled master name must error")
	}
	// The error should help by naming what is installed.
	for _, want := range []string{"cache", "legacy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list available addons, got: %v", err)
			break
		}
	}
}

func TestResolveRedisReplicaSelfReference(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "cache"}
	err := resolveRedisReplica(cfg, &rc, "cache", "cache", "", "", 0, false)
	if err == nil {
		t.Fatal("an instance cannot replicate itself")
	}
}

func TestResolveRedisReplicaBothFlags(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "cache-r", Password: "pw"}
	err := resolveRedisReplica(cfg, &rc, "cache-r", "cache", "10.0.0.9", "", 6379, true)
	if err == nil {
		t.Fatal("--replica-of and --replica-of-host together must be rejected")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Remote masters are not in this config, so the operator must supply both the
// port and the password; nothing is inherited.
func TestResolveRedisReplicaRemote(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "remote-r", Version: "8", Password: "pw-remote"}
	if err := resolveRedisReplica(cfg, &rc, "remote-r", "", "10.0.0.9", "", 6379, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ReplicaHost != "10.0.0.9" || rc.ReplicaPort != 6379 {
		t.Errorf("target = %q:%d, want 10.0.0.9:6379", rc.ReplicaHost, rc.ReplicaPort)
	}
	if rc.Password != "pw-remote" {
		t.Errorf("remote password was overwritten: %q", rc.Password)
	}
	// No cross-host major check: the stored version stands.
	if rc.Version != "8" {
		t.Errorf("remote replica version changed to %q", rc.Version)
	}
}

func TestResolveRedisReplicaRemoteNeedsPort(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "remote-r", Password: "pw"}
	err := resolveRedisReplica(cfg, &rc, "remote-r", "", "10.0.0.9", "", 0, true)
	if err == nil {
		t.Fatal("--replica-of-host without --replica-of-port must error")
	}
	if !strings.Contains(err.Error(), "--replica-of-port") {
		t.Errorf("error should name the missing flag: %v", err)
	}
}

func TestResolveRedisReplicaRemoteNeedsPassword(t *testing.T) {
	cfg := replicaCfg()
	rc := config.RedisConfig{Name: "remote-r"}
	err := resolveRedisReplica(cfg, &rc, "remote-r", "", "10.0.0.9", "", 6379, false)
	if err == nil {
		t.Fatal("a remote replica without --password must error")
	}
	if !strings.Contains(err.Error(), "--password") {
		t.Errorf("error should name the missing flag: %v", err)
	}
}

// validateRedisKnobs carries the one replica invariant checkable without the
// whole config: a target host with no port would emit `--replicaof <host> 0`.
func TestValidateRedisKnobsReplicaPortRequired(t *testing.T) {
	err := validateRedisKnobs(config.RedisConfig{Name: "cache-r", ReplicaHost: "127.0.0.1"})
	if err == nil {
		t.Fatal("ReplicaHost with ReplicaPort 0 must error")
	}
	if !strings.Contains(err.Error(), "--replica-of") {
		t.Errorf("error should point at the fix: %v", err)
	}
	if err := validateRedisKnobs(config.RedisConfig{Name: "cache-r", ReplicaHost: "127.0.0.1", ReplicaPort: 6379}); err != nil {
		t.Errorf("a complete replica target should validate: %v", err)
	}
}
