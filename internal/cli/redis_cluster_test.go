package cli

import (
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func clusterCfg(members ...config.RedisConfig) *config.Config {
	cfg := config.Default()
	cfg.Addons.Redis = map[string]config.RedisConfig{}
	for _, m := range members {
		cfg.Addons.Redis[m.Name] = m
	}
	return cfg
}

// resolveRedisCluster is what enforces the load-bearing "one password per
// cluster" invariant for the passthrough model: pgcli never runs --cluster
// create itself, so all a --cluster create command needs to work is that every
// member carries the same requirepass.
func TestResolveRedisClusterFirstMemberKeepsPassword(t *testing.T) {
	cfg := clusterCfg() // empty group
	rc := &config.RedisConfig{Name: "n1"}
	if err := resolveRedisCluster(cfg, rc, "n1", "app", "", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Cluster != "app" {
		t.Errorf("Cluster = %q, want %q", rc.Cluster, "app")
	}
	if rc.AdvertiseHost != "" {
		t.Errorf("AdvertiseHost = %q, want empty (single-host default)", rc.AdvertiseHost)
	}
	// First member: password untouched here — the install path generates it
	// after resolve, so a subsequent member inherits whatever it settles on.
	if rc.Password != "" {
		t.Errorf("first member Password = %q, want untouched-empty so the install path generates it", rc.Password)
	}
}

// An explicit --password on the first member is legitimate: it becomes the
// group's shared secret that every later member inherits.
func TestResolveRedisClusterFirstMemberHonoursExplicitPassword(t *testing.T) {
	cfg := clusterCfg()
	rc := &config.RedisConfig{Name: "n1", Password: "pinned"}
	if err := resolveRedisCluster(cfg, rc, "n1", "app", "", "", "", 0, false, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Password != "pinned" {
		t.Errorf("Password = %q, want the explicit %q kept for later members", rc.Password, "pinned")
	}
}

func TestResolveRedisClusterLaterMemberInherits(t *testing.T) {
	cfg := clusterCfg(config.RedisConfig{Name: "n1", Cluster: "app", Password: "gen-shared"})
	rc := &config.RedisConfig{Name: "n2"}
	if err := resolveRedisCluster(cfg, rc, "n2", "app", "", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Password != "gen-shared" {
		t.Errorf("Password = %q, want %q inherited from the group's first member", rc.Password, "gen-shared")
	}
}

// Even without --password given, a re-install of an existing member must not
// clobber the group's shared password (the flag's own merge in
// runAddonInstallRedis only overwrites rc.Password when non-empty, so this
// resolve step is what actually keeps the group consistent when a fresh member
// is added).
// Group membership is decided by the token, not by name order: a member of
// ANOTHER --cluster group sorting alphabetically first must not become this
// group's password base.
func TestResolveRedisClusterDifferentTokenIndependent(t *testing.T) {
	cfg := clusterCfg(
		config.RedisConfig{Name: "aaa", Cluster: "reporting", Password: "pw-reporting"},
		config.RedisConfig{Name: "zzz", Cluster: "app", Password: "pw-app"},
	)
	rc := &config.RedisConfig{Name: "n2"}
	if err := resolveRedisCluster(cfg, rc, "n2", "app", "", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// If base selection ignored the token and took the first sorted member,
	// this would be pw-reporting.
	if rc.Password != "pw-app" {
		t.Errorf("Password = %q, want the %q group's shared value (not the alphabetically-first other-group's)", rc.Password, "app")
	}
	if rc.Cluster != "app" {
		t.Errorf("Cluster = %q, want %q (the unrelated group must not interfere)", rc.Cluster, "app")
	}
}

func TestResolveRedisClusterMismatchedPasswordErrors(t *testing.T) {
	cfg := clusterCfg(config.RedisConfig{Name: "n1", Cluster: "app", Password: "pw-group"})
	rc := &config.RedisConfig{Name: "n2", Password: "pw-other"}
	err := resolveRedisCluster(cfg, rc, "n2", "app", "", "", "", 0, false, true)
	if err == nil {
		t.Fatalf("mismatched explicit --password should be rejected (would break --cluster create auth)")
	}
	if !strings.Contains(err.Error(), "share the group's password") {
		t.Errorf("unexpected error text: %v", err)
	}
}

// Explicit --password equal to the group's shared value must NOT error — an
// operator re-pinning the same secret (e.g. on a re-install) is legitimate.
func TestResolveRedisClusterExplicitMatchingPasswordAllowed(t *testing.T) {
	cfg := clusterCfg(config.RedisConfig{Name: "n1", Cluster: "app", Password: "pw-group"})
	rc := &config.RedisConfig{Name: "n2", Password: "pw-group"}
	if err := resolveRedisCluster(cfg, rc, "n2", "app", "", "", "", 0, false, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Password != "pw-group" {
		t.Errorf("Password = %q, want the same %q", rc.Password, "pw-group")
	}
}

// Stored replica state is checked too: a hand-rolled pg.yaml can put a
// replica_host on what is now being installed as a cluster member.
func TestResolveRedisClusterReplicaStoredMutex(t *testing.T) {
	cfg := clusterCfg()
	rc := &config.RedisConfig{Name: "n1", ReplicaHost: "10.0.0.5", ReplicaPort: 6379}
	err := resolveRedisCluster(cfg, rc, "n1", "app", "", "", "", 0, false, false)
	if err == nil {
		t.Fatal("cluster + a stored ReplicaHost must be rejected — one instance, one role")
	}
}

func TestResolveRedisClusterReplicaFlagsMutex(t *testing.T) {
	cfg := clusterCfg()
	rc := &config.RedisConfig{Name: "n1"}
	// --replica-of is handled later (resolveRedisReplica) but the operator
	// could still pass both on one install — must fail up front.
	err := resolveRedisCluster(cfg, rc, "n1", "app", "", "master", "", 0, false, false)
	if err == nil {
		t.Fatal("cluster + --replica-of must be rejected on the same install")
	}
	err = resolveRedisCluster(cfg, rc, "n1", "app", "", "", "10.0.0.5", 0, false, false)
	if err == nil {
		t.Fatal("cluster + --replica-of-host must be rejected on the same install")
	}
}

// cluster="" (no --cluster flag) is a no-op — a plain reinstall must not wipe
// a stored cluster/advertise-host just because the flag is absent this time.
func TestResolveRedisClusterEmptyNoOp(t *testing.T) {
	cfg := clusterCfg()
	rc := &config.RedisConfig{Name: "n1", Cluster: "app", AdvertiseHost: "10.0.0.5", Password: "keepme"}
	if err := resolveRedisCluster(cfg, rc, "n1", "", "", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.Cluster != "app" || rc.AdvertiseHost != "10.0.0.5" || rc.Password != "keepme" {
		t.Errorf("empty --cluster clobbered stored state: %+v", rc)
	}
}

// --advertise-host sets the peer-visible address (--cluster-announce-ip).
// Only meaningful with --cluster set, which the empty-cluster no-op above
// already enforces by not touching it otherwise.
func TestResolveRedisClusterSetsAdvertiseHost(t *testing.T) {
	cfg := clusterCfg()
	rc := &config.RedisConfig{Name: "n1"}
	if err := resolveRedisCluster(cfg, rc, "n1", "app", "10.0.0.7", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.AdvertiseHost != "10.0.0.7" {
		t.Errorf("AdvertiseHost = %q, want %q", rc.AdvertiseHost, "10.0.0.7")
	}
}

// --cluster-replicas is a GROUP property like the password: the first member
// sets it (only when the flag was given), later members inherit it, and an
// explicit disagreement is rejected so the suggested create command and the
// node-count guidance stay consistent. It never touches redis-server argv —
// who becomes a follower is Redis's call at create time.
func TestResolveRedisClusterFirstMemberSetsReplicas(t *testing.T) {
	cfg := clusterCfg()
	rc := &config.RedisConfig{Name: "n1"}
	if err := resolveRedisCluster(cfg, rc, "n1", "app", "", "", "", 2, true, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ClusterReplicas != 2 {
		t.Errorf("ClusterReplicas = %d, want the explicit 2 stored as the group's value", rc.ClusterReplicas)
	}
}

func TestResolveRedisClusterLaterMemberInheritsReplicas(t *testing.T) {
	cfg := clusterCfg(config.RedisConfig{Name: "n1", Cluster: "app", Password: "pw", ClusterReplicas: 1})
	rc := &config.RedisConfig{Name: "n2"}
	if err := resolveRedisCluster(cfg, rc, "n2", "app", "", "", "", 0, false, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rc.ClusterReplicas != 1 {
		t.Errorf("ClusterReplicas = %d, want 1 inherited from the group (a later install omits the flag)", rc.ClusterReplicas)
	}
}

func TestResolveRedisClusterReplicasMismatchErrors(t *testing.T) {
	cfg := clusterCfg(config.RedisConfig{Name: "n1", Cluster: "app", Password: "pw", ClusterReplicas: 1})
	rc := &config.RedisConfig{Name: "n2"}
	err := resolveRedisCluster(cfg, rc, "n2", "app", "", "", "", 2, true, false)
	if err == nil {
		t.Fatal("explicit --cluster-replicas disagreeing with the group must be rejected")
	}
}

func TestResolveRedisClusterNegativeReplicasRejected(t *testing.T) {
	cfg := clusterCfg()
	rc := &config.RedisConfig{Name: "n1"}
	if err := resolveRedisCluster(cfg, rc, "n1", "app", "", "", "", -1, true, false); err == nil {
		t.Fatal("negative --cluster-replicas must be rejected")
	}
}

func TestRedisRoleSummaryCluster(t *testing.T) {
	rc := config.RedisConfig{Cluster: "app", Port: 6379}
	if got := redisRoleSummary(rc); got != `cluster member of "app" (127.0.0.1:6379)` {
		t.Errorf("single-host role line = %q", got)
	}
	rc.AdvertiseHost = "10.0.0.7"
	if got := redisRoleSummary(rc); !strings.Contains(got, "10.0.0.7:6379") {
		t.Errorf("cross-host role line should carry the advertise address: %q", got)
	}
}

func TestRedisClusterPeerAddrs(t *testing.T) {
	cfg := clusterCfg(
		config.RedisConfig{Name: "n3", Cluster: "app", Port: 6381, AdvertiseHost: "10.0.0.9"},
		config.RedisConfig{Name: "n1", Cluster: "app", Port: 6379},
		config.RedisConfig{Name: "n2", Cluster: "app", Port: 6380},
		config.RedisConfig{Name: "other", Cluster: "reporting", Port: 7000},
		config.RedisConfig{Name: "standalone", Port: 8000},
	)
	got := redisClusterPeerAddrs(cfg, config.RedisConfig{Cluster: "app"})
	want := []string{"127.0.0.1:6379", "127.0.0.1:6380", "10.0.0.9:6381"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("peer operands = %v, want %v (name-ordered, group-only, advertise honoured)", got, want)
	}
}

// Below the minimum masters, the summary tells the operator how many more to
// install rather than handing them a --cluster create that would fail; at the
// minimum it returns the exact command. Both variants carry the same
// "pgcli does not assemble" note.
func TestRedisClusterNextSteps(t *testing.T) {
	cfg := clusterCfg(
		config.RedisConfig{Name: "n1", Cluster: "app", Port: 6379},
		config.RedisConfig{Name: "n2", Cluster: "app", Port: 6380},
	)
	out := redisClusterNextSteps(cfg, "n2", cfg.Addons.Redis["n2"])
	if !strings.Contains(out, "1/3") && !strings.Contains(out, "2/3") {
		t.Fatalf("below-min summary should show configured-vs-required masters: %q", out)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("below-min summary should say how many more to install: %q", out)
	}
	// The exact create command is still shown (copy-pasteable ahead of time is
	// friendlier than hiding it): 2 operands so far.
	if !strings.Contains(out, "--cluster create 127.0.0.1:6379 127.0.0.1:6380") {
		t.Errorf("summary missing the operands-so-far command: %q", out)
	}

	cfg.Addons.Redis["n3"] = config.RedisConfig{Name: "n3", Cluster: "app", Port: 6381}
	out = redisClusterNextSteps(cfg, "n3", cfg.Addons.Redis["n3"])
	if !strings.Contains(out, "3 masters configured") {
		t.Errorf("at-minimum summary should say so: %q", out)
	}
	if !strings.Contains(out, "--cluster create 127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381") {
		t.Errorf("at-minimum summary missing the full command: %q", out)
	}
	if strings.Contains(out, "Install") {
		t.Errorf("at-minimum summary should not ask for more installs: %q", out)
	}
}

// With --cluster-replicas N the summary counts NODES (not masters), needs
// 3*(1+N) of them, and echoes the operator's replica intent into the suggested
// create command — so "6 masters configured" can never mislabel followers.
func TestRedisClusterNextStepsWithReplicas(t *testing.T) {
	mk := func(name string, port int) config.RedisConfig {
		return config.RedisConfig{Name: name, Cluster: "app", Port: port, ClusterReplicas: 1}
	}
	cfg := clusterCfg(mk("n1", 6379), mk("n2", 6380), mk("n3", 6381)) // 3 of 6 nodes so far
	out := redisClusterNextSteps(cfg, "n3", cfg.Addons.Redis["n3"])
	if !strings.Contains(out, "3/6") {
		t.Fatalf("below-min (replicas=1) should count 3 configured vs 6 needed nodes: %q", out)
	}
	if !strings.Contains(out, "nodes configured") {
		t.Errorf("with followers the summary should say NODES, not masters: %q", out)
	}
	if strings.Contains(out, "masters configured") {
		t.Errorf("replicas>0 summary must not call every node a master: %q", out)
	}
	if !strings.Contains(out, "--cluster-replicas 1") {
		t.Errorf("summary should echo the group's --cluster-replicas into the create cmd: %q", out)
	}

	cfg.Addons.Redis["n4"] = mk("n4", 6382)
	cfg.Addons.Redis["n5"] = mk("n5", 6383)
	cfg.Addons.Redis["n6"] = mk("n6", 6384) // now 6 nodes = 3 masters x (1+1)
	out = redisClusterNextSteps(cfg, "n6", cfg.Addons.Redis["n6"])
	if !strings.Contains(out, "6 nodes configured") {
		t.Errorf("at-minimum (replicas=1) summary should count all 6 nodes: %q", out)
	}
	if strings.Contains(out, "Install") {
		t.Errorf("at-minimum summary should stop asking for installs: %q", out)
	}
}
