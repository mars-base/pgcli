package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func valkeyClusterCfg(members ...config.RedisConfig) *config.Config {
	cfg := config.Default()
	cfg.Addons.Valkey = map[string]config.RedisConfig{}
	for _, m := range members {
		cfg.Addons.Valkey[m.Name] = m
	}
	return cfg
}

// resolveValkeyCluster shares resolveKVCluster's invariants with redis: one
// password per valkey cluster group, first member untouched (the install path
// generates it), later members inherit, explicit disagreements rejected.
func TestResolveValkeyClusterFirstMemberKeepsPassword(t *testing.T) {
	cfg := valkeyClusterCfg()
	rc := &config.RedisConfig{Name: "n1"}
	if err := resolveValkeyCluster(cfg, rc, "n1", "app", "", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Cluster != "app" {
		t.Errorf("Cluster = %q, want %q", rc.Cluster, "app")
	}
	if rc.Password != "" {
		t.Errorf("first member Password = %q, want untouched-empty so the install path generates it", rc.Password)
	}
}

func TestResolveValkeyClusterLaterMemberInherits(t *testing.T) {
	cfg := valkeyClusterCfg(config.RedisConfig{Name: "n1", Cluster: "app", Password: "gen-shared"})
	rc := &config.RedisConfig{Name: "n2"}
	if err := resolveValkeyCluster(cfg, rc, "n2", "app", "", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Password != "gen-shared" {
		t.Errorf("Password = %q, want %q inherited from the group's first member", rc.Password, "gen-shared")
	}
}

// Redis and valkey --cluster groups are separate namespaces: a redis addon in
// the same-named cluster must never donate its password, port set or replica
// intent to a valkey member.
func TestResolveValkeyClusterIgnoresRedisNamespace(t *testing.T) {
	cfg := valkeyClusterCfg()
	cfg.Addons.Redis = map[string]config.RedisConfig{
		"r1": {Name: "r1", Cluster: "app", Password: "pw-redis", ClusterReplicas: 2, Port: 6379},
	}
	rc := &config.RedisConfig{Name: "n1"}
	if err := resolveValkeyCluster(cfg, rc, "n1", "app", "", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Password != "" {
		t.Errorf("valkey member adopted the redis addon's password: %q", rc.Password)
	}
	if rc.ClusterReplicas != 0 {
		t.Errorf("ClusterReplicas = %d, want 0 (a redis group's value must not leak)", rc.ClusterReplicas)
	}
	peers := valkeyClusterPeerAddrs(cfg, *rc)
	if len(peers) != 0 {
		t.Errorf("peer operands leaked redis members: %v", peers)
	}
}

func TestResolveValkeyClusterMismatchedPasswordErrors(t *testing.T) {
	cfg := valkeyClusterCfg(config.RedisConfig{Name: "n1", Cluster: "app", Password: "pw-group"})
	rc := &config.RedisConfig{Name: "n2", Password: "pw-other"}
	err := resolveValkeyCluster(cfg, rc, "n2", "app", "", "", "", 0, false, true)
	if err == nil {
		t.Fatal("mismatched explicit --password should be rejected (would break --cluster create auth)")
	}
	if !strings.Contains(err.Error(), "share the group's password") {
		t.Errorf("unexpected error text: %v", err)
	}
}

func TestResolveValkeyClusterReplicaMutex(t *testing.T) {
	cfg := valkeyClusterCfg()
	rc := &config.RedisConfig{Name: "n1", ReplicaHost: "10.0.0.5", ReplicaPort: 6380}
	if err := resolveValkeyCluster(cfg, rc, "n1", "app", "", "", "", 0, false, false); err == nil {
		t.Fatal("cluster + a stored ReplicaHost must be rejected — one instance, one role")
	}
	rc2 := &config.RedisConfig{Name: "n1"}
	if err := resolveValkeyCluster(cfg, rc2, "n1", "app", "", "master", "", 0, false, false); err == nil {
		t.Fatal("cluster + --replica-of must be rejected on the same install")
	}
}

func TestResolveValkeyClusterReplicasInheritAndMismatch(t *testing.T) {
	cfg := valkeyClusterCfg(config.RedisConfig{Name: "n1", Cluster: "app", Password: "pw", ClusterReplicas: 1})
	rc := &config.RedisConfig{Name: "n2"}
	if err := resolveValkeyCluster(cfg, rc, "n2", "app", "", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ClusterReplicas != 1 {
		t.Errorf("ClusterReplicas = %d, want 1 inherited from the group", rc.ClusterReplicas)
	}
	rc2 := &config.RedisConfig{Name: "n3"}
	if err := resolveValkeyCluster(cfg, rc2, "n3", "app", "", "", "", 2, true, false); err == nil {
		t.Fatal("explicit --cluster-replicas disagreeing with the group must be rejected")
	}
}

func TestValkeyClusterPeerAddrs(t *testing.T) {
	cfg := valkeyClusterCfg(
		config.RedisConfig{Name: "n3", Cluster: "app", Port: 6382, AdvertiseHost: "10.0.0.9"},
		config.RedisConfig{Name: "n1", Cluster: "app", Port: 6380},
		config.RedisConfig{Name: "n2", Cluster: "app", Port: 6381},
		config.RedisConfig{Name: "other", Cluster: "reporting", Port: 7000},
		config.RedisConfig{Name: "standalone", Port: 8000},
	)
	got := valkeyClusterPeerAddrs(cfg, config.RedisConfig{Cluster: "app"})
	want := []string{"127.0.0.1:6380", "127.0.0.1:6381", "10.0.0.9:6382"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("peer operands = %v, want %v (name-ordered, group-only, advertise honoured)", got, want)
	}
}

// The suggested assemble command must name pg valkey-cli — a redis-cli line
// here would point the operator at the wrong client binary.
func TestValkeyClusterNextSteps(t *testing.T) {
	cfg := valkeyClusterCfg(
		config.RedisConfig{Name: "n1", Cluster: "app", Port: 6380},
		config.RedisConfig{Name: "n2", Cluster: "app", Port: 6381},
	)
	// A redis member in the same-named cluster must not appear in the operands.
	cfg.Addons.Redis = map[string]config.RedisConfig{
		"r1": {Name: "r1", Cluster: "app", Port: 6379},
	}
	out := valkeyClusterNextSteps(cfg, "n2", cfg.Addons.Valkey["n2"])
	if !strings.Contains(out, "2/3") || !strings.Contains(out, "more") {
		t.Errorf("below-min summary should show configured-vs-required masters: %q", out)
	}
	if !strings.Contains(out, "pg valkey-cli --name n2 -- --cluster create 127.0.0.1:6380 127.0.0.1:6381 --cluster-replicas 0") {
		t.Errorf("summary missing the valkey-cli assemble command: %q", out)
	}
	if strings.Contains(out, "redis-cli") || strings.Contains(out, "6379") {
		t.Errorf("summary leaked the redis client or a redis member: %q", out)
	}

	cfg.Addons.Valkey["n3"] = config.RedisConfig{Name: "n3", Cluster: "app", Port: 6382}
	out = valkeyClusterNextSteps(cfg, "n3", cfg.Addons.Valkey["n3"])
	if !strings.Contains(out, "3 masters configured") {
		t.Errorf("at-minimum summary should say so: %q", out)
	}
	if strings.Contains(out, "Install") {
		t.Errorf("at-minimum summary should not ask for more installs: %q", out)
	}
}
