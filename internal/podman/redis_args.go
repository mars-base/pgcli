package podman

import (
	"fmt"

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
// the RDB location. --maxmemory is opt-in; when set, allkeys-lru makes it a
// real cache (evicts any key) rather than just a hard write ceiling.
func redisServerArgs(rc *config.RedisConfig, bindHost string) []string {
	args := []string{
		"redis-server",
		"--port", fmt.Sprintf("%d", rc.Port),
		"--requirepass", rc.Password,
		"--bind", bindHost,
		"--dir", redisContainerDataDir,
	}
	if rc.MaxMemory != "" {
		args = append(args, "--maxmemory", rc.MaxMemory, "--maxmemory-policy", "allkeys-lru")
	}
	return args
}
