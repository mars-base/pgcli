package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

// flagPair checks that args contains flag immediately followed by value.
func flagPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	i := slices.Index(args, flag)
	if i < 0 {
		t.Fatalf("args missing %q: %v", flag, args)
	}
	if i+1 >= len(args) || args[i+1] != value {
		t.Errorf("%s = %q, want %q (args: %v)", flag, args[i+1:], value, args)
	}
}

func TestRedisServerArgs(t *testing.T) {
	rc := &config.RedisConfig{
		Port:     36379,
		Password: "aX9kQ2mZx7Lp4RtVbNcE",
		Listen:   "0.0.0.0",
	}

	args := redisServerArgs(rc, "0.0.0.0")

	if len(args) == 0 || args[0] != "redis-server" {
		t.Fatalf("args must lead with redis-server, got %v", args)
	}
	flagPair(t, args, "--port", "36379")
	flagPair(t, args, "--requirepass", "aX9kQ2mZx7Lp4RtVbNcE")
	flagPair(t, args, "--bind", "0.0.0.0")
	flagPair(t, args, "--dir", redisContainerDataDir)

	// Without MaxMemory neither eviction flag may appear — an orphan
	// --maxmemory-policy would change default behavior on its own.
	if slices.Contains(args, "--maxmemory") || slices.Contains(args, "--maxmemory-policy") {
		t.Errorf("maxmemory flags present without MaxMemory set: %v", args)
	}
}

// The bind host is passed through verbatim: createContainer resolves it via
// proxyBindHost (macOS bridge widens a loopback Listen to 0.0.0.0), and this
// function must not second-guess that.
func TestRedisServerArgsBindPassthrough(t *testing.T) {
	rc := &config.RedisConfig{Port: 6379, Password: "p", Listen: "127.0.0.1"}
	args := redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--bind", "0.0.0.0")
}

func TestRedisServerArgsMaxMemory(t *testing.T) {
	rc := &config.RedisConfig{
		Port:      6380,
		Password:  "p",
		Listen:    "0.0.0.0",
		MaxMemory: "256mb",
	}

	args := redisServerArgs(rc, "0.0.0.0")

	flagPair(t, args, "--maxmemory", "256mb")
	flagPair(t, args, "--maxmemory-policy", "allkeys-lru")
}

// An explicit policy replaces the allkeys-lru default — that is the escape hatch
// for a hard-ceiling store instead of an evicting cache.
func TestRedisServerArgsMaxMemoryPolicyOverride(t *testing.T) {
	rc := &config.RedisConfig{
		Port:            6380,
		Password:        "p",
		MaxMemory:       "1gb",
		MaxMemoryPolicy: "noeviction",
	}

	args := redisServerArgs(rc, "0.0.0.0")

	flagPair(t, args, "--maxmemory", "1gb")
	flagPair(t, args, "--maxmemory-policy", "noeviction")
	if slices.Contains(args, "allkeys-lru") {
		t.Errorf("default policy leaked through an override: %v", args)
	}
}

func TestRedisServerArgsAOF(t *testing.T) {
	rc := &config.RedisConfig{Port: 6379, Password: "p", AOF: true}
	args := redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--appendonly", "yes")
	// Without an explicit strength, Redis's own everysec default applies —
	// emitting nothing is the point.
	if slices.Contains(args, "--appendfsync") {
		t.Errorf("--appendfsync emitted without AppendFsync set: %v", args)
	}

	rc.AppendFsync = "always"
	args = redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--appendfsync", "always")
}

// An AOF-off instance must not emit any appendonly machinery.
func TestRedisServerArgsAOFOff(t *testing.T) {
	rc := &config.RedisConfig{Port: 6379, Password: "p", AppendFsync: "always"}
	args := redisServerArgs(rc, "0.0.0.0")
	if slices.Contains(args, "--appendonly") || slices.Contains(args, "--appendfsync") {
		t.Errorf("aof flags present with AOF off: %v", args)
	}
}

// The whole schedule is one argv element: splitting "900 1 300 10" would make
// the trailing numbers parse as config-file arguments.
func TestRedisServerArgsSaveSchedule(t *testing.T) {
	rc := &config.RedisConfig{Port: 6379, Password: "p", SaveSchedule: "900 1 300 10"}
	args := redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--save", "900 1 300 10")

	rc.SaveSchedule = "  3600  1  "
	args = redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--save", "3600 1")
}

// "no" is pgcli's disable token and must arrive as Redis's own empty value.
func TestRedisServerArgsSaveDisabled(t *testing.T) {
	rc := &config.RedisConfig{Port: 6379, Password: "p", SaveSchedule: "no"}
	args := redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--save", "")
}

func TestRedisServerArgsSaveDefaultUntouched(t *testing.T) {
	rc := &config.RedisConfig{Port: 6379, Password: "p"}
	args := redisServerArgs(rc, "0.0.0.0")
	if slices.Contains(args, "--save") {
		t.Errorf("--save emitted without SaveSchedule set: %v", args)
	}
}

// A replica emits --replicaof <host> <port> — three argv slots, so the
// two-slot flagPair helper does not fit — plus --masterauth carrying the same
// password as --requirepass (resolveRedisReplica pins them equal).
func TestRedisServerArgsReplica(t *testing.T) {
	rc := &config.RedisConfig{
		Port:        6380,
		Password:    "sharedPw",
		ReplicaHost: "127.0.0.1",
		ReplicaPort: 6379,
	}
	args := redisServerArgs(rc, "0.0.0.0")

	i := slices.Index(args, "--replicaof")
	if i < 0 {
		t.Fatalf("args missing --replicaof: %v", args)
	}
	if i+2 >= len(args) || args[i+1] != "127.0.0.1" || args[i+2] != "6379" {
		t.Errorf("--replicaof = %v, want [127.0.0.1 6379]", args[i+1:])
	}
	flagPair(t, args, "--masterauth", "sharedPw")
	flagPair(t, args, "--requirepass", "sharedPw")

	// The knobs are orthogonal to the role: a replica may cap memory and enable
	// AOF exactly like a master.
	rc.MaxMemory = "256mb"
	rc.AOF = true
	args = redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--maxmemory", "256mb")
	flagPair(t, args, "--appendonly", "yes")
	flagPair(t, args, "--masterauth", "sharedPw")
	if slices.Index(args, "--replicaof") < 0 {
		t.Errorf("--replicaof lost when other knobs are set: %v", args)
	}
}

// A master must emit neither replica flag — an orphan --masterauth would be
// harmless but --replicaof would silently demote it.
func TestRedisServerArgsMasterHasNoReplicaFlags(t *testing.T) {
	rc := &config.RedisConfig{Port: 6379, Password: "p"}
	args := redisServerArgs(rc, "0.0.0.0")
	if slices.Contains(args, "--replicaof") || slices.Contains(args, "--masterauth") {
		t.Errorf("replica flags present without ReplicaHost: %v", args)
	}
}

// A single-host cluster member gets the three cluster flags and no announce
// block — nodes.conf is relative to --dir /data so it lands in the instance's
// own bind mount and self-heals across a rebuild.
func TestRedisServerArgsClusterSingleHost(t *testing.T) {
	rc := &config.RedisConfig{Port: 6379, Password: "p", Cluster: "app"}
	args := redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--cluster-enabled", "yes")
	flagPair(t, args, "--cluster-config-file", "nodes.conf")
	flagPair(t, args, "--cluster-node-timeout", "5000")
	// --masterauth=rc.Password unconditionally: any member Redis promotes to a
	// follower at create time must be able to auth to its master, else it spins
	// in a reconnect storm. The group shares one password, so masterauth ==
	// requirepass. (Regression: 3-master x 1-replica cluster left followers
	// master_link_status:down with no masterauth.)
	flagPair(t, args, "--masterauth", "p")
	if slices.Contains(args, "--cluster-announce-ip") {
		t.Errorf("announce flags present without AdvertiseHost (single-host is reached at its bind): %v", args)
	}
	// Orthogonal to the cluster role: the memory cap still applies.
	rc.MaxMemory = "10mb"
	args = redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--maxmemory", "10mb")
	flagPair(t, args, "--cluster-enabled", "yes")
}

// A cross-host member announces the peer-visible address plus the exact
// client/bus ports so peers don't dial the container's own view of itself. The
// bus port is always client+10000 (Redis's own rule), which pgcli mirrors here
// rather than storing.
func TestRedisServerArgsClusterAdvertise(t *testing.T) {
	rc := &config.RedisConfig{Port: 6381, Password: "p", Cluster: "app", AdvertiseHost: "10.10.0.158"}
	args := redisServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--cluster-announce-ip", "10.10.0.158")
	flagPair(t, args, "--cluster-announce-port", "6381")
	flagPair(t, args, "--cluster-announce-bus-port", "16381")
	flagPair(t, args, "--cluster-enabled", "yes")
}

// A standalone instance (no Cluster) must not emit any cluster flag — the
// argv is otherwise byte-identical to before the feature.
func TestRedisServerArgsNoClusterFlagsByDefault(t *testing.T) {
	rc := &config.RedisConfig{Port: 6379, Password: "p", AOF: true}
	args := redisServerArgs(rc, "0.0.0.0")
	for _, a := range args {
		if strings.HasPrefix(a, "--cluster") {
			t.Errorf("cluster flag %q present without Cluster set: %v", a, args)
		}
	}
}

// RedisCLI gates the cluster-aware -c flag off when the forwarded command is
// redis-cli's own --cluster admin subcommand (which dials its operand nodes
// itself). The args reach this predicate already past cobra's `--`, so the
// subcommand is literally args[0].
func TestStartsClusterSubcmd(t *testing.T) {
	if !startsClusterSubcmd([]string{"--cluster", "create", "127.0.0.1:6379"}) {
		t.Error("--cluster create must suppress -c")
	}
	if startsClusterSubcmd([]string{"get", "k"}) {
		t.Error("an ordinary command must keep -c")
	}
	if startsClusterSubcmd([]string{"info", "--", "--cluster"}) {
		t.Error("only a leading --cluster is the subcommand; a trailing one is an arg")
	}
	if startsClusterSubcmd(nil) {
		t.Error("empty args must not match")
	}
}
