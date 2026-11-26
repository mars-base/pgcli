package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// Valkey is the Redis 7.2.4 BSD-3 fork (Linux Foundation): same server flags,
// same RDB/AOF persistence files, same replication and native-cluster
// protocols, so this add-on type mirrors the redis add-on one-for-one — only
// the image table (valkeyMajorImages), the container/data-dir prefix
// (pgcli-valkey, addon/valkey/<name>/data), the binaries (valkey-server /
// valkey-cli) and the user-facing labels differ. The shared bodies live in
// addon.go (resolveKVReplica / resolveKVCluster / kvClusterPeerAddrs /
// kvClusterNextSteps) and internal/podman (RedisManager kind="valkey").

// resolveValkeyReplica is the valkey wrapper over the shared replica resolver.
func resolveValkeyReplica(cfg *config.Config, rc *config.RedisConfig, selfName, replicaOf, replicaOfHost, version string, replicaOfPort int, passwordGiven bool) error {
	return resolveKVReplica(cfg.Addons.Valkey, "valkey", "Valkey", rc, selfName, replicaOf, replicaOfHost, version, replicaOfPort, passwordGiven)
}

// resolveValkeyCluster is the valkey wrapper over the shared cluster resolver.
func resolveValkeyCluster(cfg *config.Config, rc *config.RedisConfig, selfName, cluster, advertiseHost, replicaOf, replicaOfHost string, replicas int, replicasGiven, passwordGiven bool) error {
	return resolveKVCluster(cfg.Addons.Valkey, rc, selfName, cluster, advertiseHost, replicaOf, replicaOfHost, replicas, replicasGiven, passwordGiven)
}

// valkeyClusterPeerAddrs lists the "addr:port" operands for a valkey cluster
// group (the --cluster create node list).
func valkeyClusterPeerAddrs(cfg *config.Config, self config.RedisConfig) []string {
	return kvClusterPeerAddrs(cfg.Addons.Valkey, self)
}

// valkeyClusterNextSteps suggests the `pg valkey-cli -- --cluster create`
// assemble command for a native-cluster member.
func valkeyClusterNextSteps(cfg *config.Config, selfName string, self config.RedisConfig) string {
	return kvClusterNextSteps(cfg.Addons.Valkey, "valkey-cli", selfName, self)
}

// resolveValkeyVersion applies the version-selection rules for the valkey addon
// and writes the outcome into rc (Version / ImageTag). Same precedence as
// resolveRedisVersion: an explicit --image wins outright and --version is
// demoted to a display label (reverse-parsed from the tag when it was not
// given); a bare --version looks the tag up in the Valkey table; neither falls
// back to DefaultValkeyMajor. Valkey majors do NOT correspond to Redis majors
// (Valkey is the 7.2.4 fork but versions its own line), so the Redis table is
// never consulted here.
func resolveValkeyVersion(rc *config.RedisConfig, version, imageTag string) error {
	if version != "" {
		if _, ok := config.ValkeyImageTagForMajor(version); !ok {
			return fmt.Errorf("unknown valkey --version %q (available: %s)", version, strings.Join(config.ValkeyMajors(), ", "))
		}
		rc.Version = version
	}
	if imageTag != "" {
		rc.ImageTag = imageTag
		if version == "" {
			// --image bypasses the table: recover the major for display, or
			// leave it empty and show the tag verbatim.
			rc.Version = config.ValkeyMajorForImageTag(imageTag)
		}
		return nil
	}
	if rc.Version == "" {
		rc.Version = config.DefaultValkeyMajor
	}
	tag, ok := config.ValkeyImageTagForMajor(rc.Version)
	if !ok {
		// Reachable only with a hand-edited version and no --image; ApplyDefaults
		// deliberately leaves such an ImageTag empty rather than rewriting the
		// operator's value, so surface it here instead of a confusing pull error.
		return fmt.Errorf("unknown valkey version %q for addon %q (available: %s, or pass --image)", rc.Version, rc.Name, strings.Join(config.ValkeyMajors(), ", "))
	}
	// A --version given now (or a previously-resolved major) always re-derives
	// the tag; a stale stored tag from an older pgcli must not outrank it.
	rc.ImageTag = tag
	return nil
}

// runAddonInstallValkey installs a Valkey container — the BSD-3 Redis fork,
// feature-for-feature identical to the redis add-on (standalone master, read
// replica, native-cluster member; RDB/AOF persistence; requirepass). Flags,
// merge semantics and summary shape mirror runAddonInstallRedis; only the
// version table and the labels/binary names differ.
func runAddonInstallValkey(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "valkey"
	}
	imageTag, _ := cmd.Flags().GetString("image")
	version, _ := cmd.Flags().GetString("version")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	port, _ := cmd.Flags().GetInt("port")
	listenAddr, _ := cmd.Flags().GetString("listen")
	password, _ := cmd.Flags().GetString("password")
	maxMemory, _ := cmd.Flags().GetString("maxmemory")
	maxMemoryPolicy, _ := cmd.Flags().GetString("maxmemory-policy")
	aof, _ := cmd.Flags().GetBool("aof")
	aofSet := cmd.Flags().Changed("aof")
	appendfsync, _ := cmd.Flags().GetString("appendfsync")
	saveSchedule, _ := cmd.Flags().GetString("save")
	replicaOf, _ := cmd.Flags().GetString("replica-of")
	replicaOfHost, _ := cmd.Flags().GetString("replica-of-host")
	replicaOfPort, _ := cmd.Flags().GetInt("replica-of-port")
	clusterToken, _ := cmd.Flags().GetString("cluster")
	clusterReplicas, _ := cmd.Flags().GetInt("cluster-replicas")
	advertiseHost, _ := cmd.Flags().GetString("advertise-host")
	force, _ := cmd.Flags().GetBool("force")

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Addons.Valkey == nil {
		cfg.Addons.Valkey = make(map[string]config.ValkeyConfig)
	}
	existing, ok := cfg.Addons.Valkey[name]
	if !ok {
		existing = config.ValkeyConfig{
			ContainerName: "pgcli-valkey" + nsSuffixCLI(cfg.Namespace) + "-" + name,
			Name:          name,
		}
	}
	if dataDir != "" {
		existing.DataDir = dataDir
	}
	if port != 0 {
		existing.Port = port
	}
	if listenAddr != "" {
		existing.Listen = listenAddr
	}
	if password != "" {
		existing.Password = password
	}
	if maxMemory != "" {
		existing.MaxMemory = maxMemory
	}
	if maxMemoryPolicy != "" {
		existing.MaxMemoryPolicy = maxMemoryPolicy
	}
	if aofSet {
		existing.AOF = aof
	}
	if appendfsync != "" {
		existing.AppendFsync = appendfsync
	}
	if saveSchedule != "" {
		existing.SaveSchedule = saveSchedule
	}
	// Resolve the replica role (if requested) before validating and before the
	// version step: a local --replica-of adopts the master's major and password,
	// and a bad topology fails here rather than at pull time.
	passwordGiven := cmd.Flags().Changed("password")
	replicasGiven := cmd.Flags().Changed("cluster-replicas")
	// Cluster membership first: it decides the shared password (inherited from
	// the group's first member) and owns the cluster↔replica exclusion.
	if err := resolveValkeyCluster(cfg, &existing, name, clusterToken, advertiseHost, replicaOf, replicaOfHost, clusterReplicas, replicasGiven, passwordGiven); err != nil {
		return err
	}
	if err := resolveValkeyReplica(cfg, &existing, name, replicaOf, replicaOfHost, version, replicaOfPort, passwordGiven); err != nil {
		return err
	}
	// Validate the effective (merged) values: stored flags count too.
	if err := validateRedisKnobs(existing); err != nil {
		return err
	}
	if existing.Name == "" {
		existing.Name = name
	}
	if err := resolveValkeyVersion(&existing, version, imageTag); err != nil {
		return err
	}
	cfg.Addons.Valkey[name] = existing

	// ApplyDefaults fills ContainerName/Listen and assigns the port from the
	// valkey_start_port pool. Password is NOT managed there.
	cfg.ApplyDefaults()
	rc := cfg.Addons.Valkey[name]

	// macOS: valkey serves on the pgcli-net bridge with the port published, so
	// bring up the machine and the bridge first (no-op on Linux).
	if err := ensureProxyBridge(cfg); err != nil {
		return err
	}

	rm, err := podman.NewValkeyManager(cfg)
	if err != nil {
		return fmt.Errorf("valkey manager: %w", err)
	}

	// Reuse semantics identical to redis: a live container is left alone
	// (reinstall is a no-op), a stopped one is started, and only --force
	// recreates it so changed port/listen/password take effect.
	exists, err := rm.ContainerExists(rc.ContainerName)
	if err != nil {
		return err
	}
	skipped := false
	if exists && !force {
		skipped = true
		if running, _ := rm.ContainerRunning(rc.ContainerName); running {
			fmt.Printf("-> valkey container %q already running; skipping creation\n", rc.ContainerName)
		} else {
			fmt.Printf("-> valkey container %q exists but is stopped; starting it\n", rc.ContainerName)
			if err := rm.StartContainer(&rc); err != nil {
				return err
			}
		}
	} else {
		// Fresh install (or the config survived a `remove` that kept it):
		// generate the password once so a reinstall of the same name can still
		// read the RDB it left behind.
		if rc.Password == "" {
			pw, err := generatePassword(20)
			if err != nil {
				return fmt.Errorf("generating valkey password: %w", err)
			}
			rc.Password = pw
		}
		fmt.Printf("-> Preparing valkey image %s...\n", rc.ImageTag)
		if err := rm.EnsureImage(rc.ImageTag); err != nil {
			return err
		}
		if rc.MaxMemory != "" {
			if redisEffectivePolicy(rc) == "noeviction" {
				fmt.Println("-> NOTE: --maxmemory with noeviction — writes fail past the cap instead of evicting (a hard ceiling).")
			} else {
				fmt.Printf("-> NOTE: --maxmemory with %s — Valkey evicts keys to stay under the cap (a cache, not a hard store).\n", redisEffectivePolicy(rc))
			}
		}
		if rc.SaveSchedule == "no" && !rc.AOF {
			fmt.Println("-> NOTE: snapshots are disabled and AOF is off — this instance loses its whole dataset on restart. Pair --save no with --aof for a durable store.")
		}
		if rc.ReplicaHost != "" {
			fmt.Printf("-> NOTE: read replica — it starts an initial full sync from the master at %s:%d into its own data dir, and rejects writes (READONLY). Send writes to the master.\n", rc.ReplicaHost, rc.ReplicaPort)
		}
		if rc.ClusterEnabled() {
			fmt.Printf("-> NOTE: native-cluster member of %q — it comes up cluster-enabled but INCOMPLETE until you assemble the group once (see the \"Cluster:\" line below). pgcli does not run --cluster create for you.\n", rc.Cluster)
		}
		fmt.Println("-> Starting valkey container...")
		if err := rm.EnsureContainer(&rc); err != nil {
			return err
		}
	}

	cfg.Addons.Valkey[name] = rc
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	if skipped {
		fmt.Println()
		fmt.Printf("✓ valkey already present: %q\n", name)
	} else {
		fmt.Println()
		fmt.Printf("✓ valkey installed: %q\n", name)
	}
	fmt.Printf("  Container:  %s\n", rc.ContainerName)
	fmt.Printf("  Version:    %s\n", rc.Version)
	fmt.Printf("  Image:      %s\n", rc.ImageTag)
	fmt.Printf("  Data:       %s\n", rm.DataDir(&rc))
	fmt.Printf("  Address:    %s:%d\n", rc.Listen, rc.Port)
	fmt.Printf("  Role:       %s\n", redisRoleSummary(rc))
	if rc.MaxMemory != "" {
		fmt.Printf("  Maxmemory:   %s (%s)\n", rc.MaxMemory, redisEffectivePolicy(rc))
	}
	fmt.Printf("  Persistence: %s\n", redisPersistenceSummary(rc))
	fmt.Println()
	fmt.Printf("  Password:    %s\n", rc.Password)
	fmt.Println()
	fmt.Printf("  Client:      pg valkey-cli ping\n")
	fmt.Printf("  Raw DSN:     redis://:%s@%s:%d/0\n", rc.Password, rc.Listen, rc.Port)
	if rc.Listen == "0.0.0.0" {
		fmt.Println("  NOTE: listening on every interface; the password above is the only gate.")
		fmt.Printf("        loopback-only: pg addon install valkey --name %s --listen 127.0.0.1 --force\n", name)
	}
	if rc.ClusterEnabled() {
		fmt.Print(valkeyClusterNextSteps(cfg, name, rc))
	}
	return nil
}

func runAddonRemoveValkey(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "valkey"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Addons.Valkey == nil {
		return fmt.Errorf("no valkey add-ons configured")
	}
	rc, ok := cfg.Addons.Valkey[name]
	if !ok {
		return fmt.Errorf("valkey %q not found", name)
	}

	rm, err := podman.NewValkeyManager(cfg)
	if err != nil {
		return fmt.Errorf("valkey manager: %w", err)
	}

	fmt.Printf("-> Removing valkey %q...\n", name)
	if err := rm.Remove(&rc, cleanData); err != nil {
		return err
	}

	delete(cfg.Addons.Valkey, name)
	if len(cfg.Addons.Valkey) == 0 {
		cfg.Addons.Valkey = nil
	}
	if err := cfg.Save(path); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✓ valkey %q removed\n", name)
	if !cleanData {
		fmt.Printf("  (data dir kept — reinstall the same name revives the RDB; --clean-data to delete it)\n")
	}
	return nil
}

func runAddonStartValkey(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "valkey"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	if cfg.Addons.Valkey == nil {
		return fmt.Errorf("no valkey add-ons configured (run 'pg addon install valkey')")
	}
	rc, ok := cfg.Addons.Valkey[name]
	if !ok {
		return fmt.Errorf("valkey %q not found (run 'pg addon install valkey --name %s')", name, name)
	}
	rm, err := podman.NewValkeyManager(cfg)
	if err != nil {
		return fmt.Errorf("valkey manager: %w", err)
	}
	fmt.Printf("-> Starting valkey %q...\n", name)
	return rm.StartContainer(&rc)
}

func runAddonStopValkey(cmd *cobra.Command) error {
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "valkey"
	}
	cfg, _, err := loadAddonCfg()
	if err != nil {
		return err
	}
	rc, ok := cfg.Addons.Valkey[name]
	if !ok {
		return fmt.Errorf("valkey %q not found", name)
	}
	rm, err := podman.NewValkeyManager(cfg)
	if err != nil {
		return fmt.Errorf("valkey manager: %w", err)
	}
	running, _ := rm.ContainerRunning(rc.ContainerName)
	if !running {
		fmt.Printf("valkey %q is not running\n", name)
		return nil
	}
	fmt.Printf("-> Stopping valkey %q...\n", name)
	if _, err := rm.Stop(rc.ContainerName); err != nil {
		return err
	}
	fmt.Printf("✓ valkey %q stopped\n", name)
	return nil
}
