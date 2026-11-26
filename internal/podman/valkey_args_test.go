package podman

import (
	"slices"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestValkeyServerArgs(t *testing.T) {
	rc := &config.RedisConfig{
		Port:     36380,
		Password: "aX9kQ2mZx7Lp4RtVbNcE",
		Listen:   "0.0.0.0",
	}

	args := valkeyServerArgs(rc, "0.0.0.0")

	if len(args) == 0 || args[0] != "valkey-server" {
		t.Fatalf("args must lead with valkey-server, got %v", args)
	}
	flagPair(t, args, "--port", "36380")
	flagPair(t, args, "--requirepass", "aX9kQ2mZx7Lp4RtVbNcE")
	flagPair(t, args, "--bind", "0.0.0.0")
	flagPair(t, args, "--dir", redisContainerDataDir)

	if slices.Contains(args, "--maxmemory") || slices.Contains(args, "--maxmemory-policy") {
		t.Errorf("maxmemory flags present without MaxMemory set: %v", args)
	}
}

// Valkey accepts the Redis argv verbatim (same flags, same persistence files,
// same cluster protocol). This pins that parity: for every config shape the
// two builders emit the same argv apart from the binary name, so a future
// Redis-only flag addition can never silently skip Valkey.
func TestValkeyServerArgsMirrorsRedisArgs(t *testing.T) {
	cases := []struct {
		name string
		rc   config.RedisConfig
	}{
		{
			name: "knobs",
			rc: config.RedisConfig{
				Port: 6380, Password: "p",
				MaxMemory: "256mb", MaxMemoryPolicy: "noeviction",
				AOF: true, AppendFsync: "always", SaveSchedule: "900 1 300 10",
				IoThreads: 4,
			},
		},
		{
			name: "replica",
			rc: config.RedisConfig{
				Port: 6381, Password: "sharedPw",
				ReplicaHost: "10.10.0.20", ReplicaPort: 6379,
				MaxMemory: "1gb", AOF: true,
			},
		},
		{
			name: "cluster",
			rc: config.RedisConfig{
				Port: 6382, Password: "sharedPw",
				Cluster: "app", AdvertiseHost: "10.10.0.158", ClusterReplicas: 1,
				SaveSchedule: "no",
			},
		},
		{
			name: "cluster-single-host",
			rc:   config.RedisConfig{Port: 6383, Password: "p", Cluster: "app"},
		},
		{
			name: "defaults",
			rc:   config.RedisConfig{Port: 6380, Password: "p"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			redisArgs := redisServerArgs(&tc.rc, "0.0.0.0")
			valkeyArgs := valkeyServerArgs(&tc.rc, "0.0.0.0")
			if len(redisArgs) != len(valkeyArgs) {
				t.Fatalf("argv length differs: redis %v, valkey %v", redisArgs, valkeyArgs)
			}
			for i := range redisArgs {
				want := redisArgs[i]
				if i == 0 {
					want = "valkey-server"
				}
				if valkeyArgs[i] != want {
					t.Errorf("argv[%d] = %q, want %q (redis %v, valkey %v)", i, valkeyArgs[i], want, redisArgs, valkeyArgs)
				}
			}
		})
	}
}

// The cluster announce block and bus-port derivation are Redis's own rules and
// must come through unchanged for Valkey.
func TestValkeyServerArgsClusterAdvertise(t *testing.T) {
	rc := &config.RedisConfig{Port: 6381, Password: "p", Cluster: "app", AdvertiseHost: "10.10.0.158"}
	args := valkeyServerArgs(rc, "0.0.0.0")
	flagPair(t, args, "--cluster-enabled", "yes")
	flagPair(t, args, "--cluster-config-file", "nodes.conf")
	flagPair(t, args, "--cluster-node-timeout", "5000")
	flagPair(t, args, "--masterauth", "p")
	flagPair(t, args, "--cluster-announce-ip", "10.10.0.158")
	flagPair(t, args, "--cluster-announce-port", "6381")
	flagPair(t, args, "--cluster-announce-bus-port", "16381")
}
