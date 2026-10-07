package podman

import (
	"fmt"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
)

// redisContainerDataDir is the container-side path the host data dir is bind-
// mounted to, and the value passed to redis-server --dir. RDB snapshots
// (dump.rdb) land there, so `pg addon remove` without --clean-data survives a
// reinstall of the same addon name.
const redisContainerDataDir = "/data"

// redisServerArgs builds the redis-server argv (everything after the image
// tag). bindHost is the already-resolved --bind value: createContainer passes
// proxyBindHost(m.bridge, rc.Listen), so the bridge widening (loopback →
// 0.0.0.0 for macOS -p mapping) happens at the caller and this stays a pure
// function testable without podman.
//
// --requirepass is always emitted: rc.Password is generated at first install
// (config.ApplyDefaults never fills it), and Redis has no auth otherwise — a
// Listen default of 0.0.0.0 would be a bare open port without it. --dir pins
// the persistence files' location. Every knob beyond port/requirepass/bind/dir
// is opt-in, and empty means "leave Redis's own default alone": --maxmemory
// pairs with allkeys-lru unless MaxMemoryPolicy says otherwise, --appendonly
// comes with an optional --appendfsync strength, and --save overrides (or, as
// "no", disables) the RDB snapshot schedule.
//
// The flag values are validated by the CLI layer, not here — see
// validateRedisKnobs in internal/cli/addon.go.
func redisServerArgs(rc *config.RedisConfig, bindHost string) []string {
	args := []string{
		"redis-server",
		"--port", fmt.Sprintf("%d", rc.Port),
		"--requirepass", rc.Password,
		"--bind", bindHost,
		"--dir", redisContainerDataDir,
	}
	if rc.MaxMemory != "" {
		policy := rc.MaxMemoryPolicy
		if policy == "" {
			policy = "allkeys-lru"
		}
		args = append(args, "--maxmemory", rc.MaxMemory, "--maxmemory-policy", policy)
	}
	if rc.AOF {
		args = append(args, "--appendonly", "yes")
		if rc.AppendFsync != "" {
			args = append(args, "--appendfsync", rc.AppendFsync)
		}
	}
	if rc.SaveSchedule != "" {
		// The whole schedule is one argv element: redis-server's own
		// command-line syntax is --save "900 1 300 10", and splitting it
		// would make the trailing numbers look like config files. pgcli's
		// "no" token maps to the native disable value, the empty string.
		val := strings.Join(strings.Fields(rc.SaveSchedule), " ")
		if val == "no" {
			val = ""
		}
		args = append(args, "--save", val)
	}
	return args
}
