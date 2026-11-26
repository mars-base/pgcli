package podman

import (
	"github.com/mars-base/pgcli/internal/config"
)

// NewValkeyManager creates a manager for Valkey addons (docker.io/valkey/valkey).
// Valkey is the Redis 7.2.4 fork: the lifecycle, data layout and client
// precedence are identical, so this shares RedisManager and only switches the
// kind (data dir addon/valkey/<name>/data, message label) and the server argv
// binary (valkey-server).
func NewValkeyManager(cfg *config.Config) (*RedisManager, error) {
	return newKVManager(cfg, "valkey", valkeyServerArgs)
}

// ValkeyCLI runs valkey-cli from a short-lived container. VALKEYCLI_AUTH takes
// precedence when set and is also mirrored to REDISCLI_AUTH for older clients;
// REDISCLI_AUTH remains supported for compatibility. Networking and cluster
// routing follow RedisCLI.
func (m *RedisManager) ValkeyCLI(imageTag, password, host string, port int, cluster bool, args []string) error {
	return m.kvCLI("valkey-cli", imageTag, password, host, port, cluster, args)
}
