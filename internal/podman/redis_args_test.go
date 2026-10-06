package podman

import (
	"slices"
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
