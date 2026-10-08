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
// "no", disables) the RDB snapshot schedule. A non-empty ReplicaHost turns the
// instance into a read replica: --replicaof <host> <port> plus --masterauth
// (which equals requirepass, since resolveRedisReplica pins a replica's
// password to the master's). A non-empty Cluster instead makes it a native-
// cluster member: --cluster-enabled yes with its nodes.conf under --dir (so it
// self-heals across restarts), --masterauth=rc.Password (so any member Redis
// later promotes to follower can authenticate to its master — the group shares
// one password), and, when AdvertiseHost is set, --cluster-announce-* so
// cross-host peers can reach it; Redis opens the +10000 bus port itself.
// Cluster and ReplicaHost are mutually exclusive, enforced by the CLI layer.
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
	if rc.Cluster != "" {
		// Native-cluster member. The bus port (client + 10000) is opened by
		// Redis itself; nodes.conf lands in --dir /data (this instance's bind
		// mount), so a restart/rebuild re-joins the cluster from it without any
		// pgcli re-add. --cluster-announce-* only when a peer-visible address is
		// set: a single-host member is reached at 127.0.0.1 and needs none.
		//
		// --masterauth is emitted unconditionally here, NOT gated on
		// ClusterReplicas: which members end up masters vs followers is decided
		// by Redis at --cluster create time, not at install, and every member of
		// a group shares one password (rc.Password == requirepass), so
		// --masterauth=rc.Password lets any member authenticate to whichever
		// master it is later assigned. Without it a follower could never auth to
		// its master and spins in a reconnect storm (found assembling a 3-master
		// x 1-replica cluster). Harmless on a member that stays a master.
		args = append(args,
			"--cluster-enabled", "yes",
			"--cluster-config-file", "nodes.conf",
			"--cluster-node-timeout", "5000",
			"--masterauth", rc.Password)
		if rc.AdvertiseHost != "" {
			args = append(args,
				"--cluster-announce-ip", rc.AdvertiseHost,
				"--cluster-announce-port", fmt.Sprintf("%d", rc.Port),
				"--cluster-announce-bus-port", fmt.Sprintf("%d", rc.ClusterBusPort()))
		}
	}
	if rc.ReplicaHost != "" {
		// Read replica: --replicaof points at the master, and --masterauth is
		// the password used to authenticate TO it. resolveRedisReplica has
		// already enforced rc.Password == the master's password, so the same
		// value serves both --requirepass (above) and --masterauth here — one
		// stored password, no separate masterauth field.
		args = append(args, "--masterauth", rc.Password,
			"--replicaof", rc.ReplicaHost, fmt.Sprintf("%d", rc.ReplicaPort))
	}
	return args
}
