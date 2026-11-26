package podman

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// RedisManager manages standalone Redis and Valkey addon containers — a KV
// store for cache, session, ranking and atomic-counter data. Valkey is the
// Redis 7.2.4 fork: same server flags, same persistence files, same cluster
// protocol, so one manager serves both; the kind field picks the container
// prefix (data dir subdirectory) and the server binary. Unlike the object
// stores it runs the plain upstream image (docker.io/library/redis or
// docker.io/valkey/valkey): both start as container root and need no uid-drop
// wrapper (the rustfs reason for building pgcli-rustfs), so the images are
// pull-only and pgcli never builds them.
//
// The one runtime contract pgcli relies on is the `<server> --flag value` argv
// (see redisServerArgs/valkeyServerArgs), identical across the majors the
// version tables offer; the image's docker-entrypoint.sh passes it through
// untouched because the first argument is not the client binary.
type RedisManager struct {
	cfg        *config.Config
	podman     string // podman binary path
	dataDir    string // base data directory (e.g. ~/.pgcli/)
	bridge     bool   // macOS: serve on the pgcli-net bridge with published ports (host networking binds the VM loopback, invisible to the Mac)
	kind       string // "redis" | "valkey" — container/data-dir prefix and message label
	serverArgs func(*config.RedisConfig, string) []string
}

// NewRedisManager creates a RedisManager for Redis addons. It works on both
// platforms: Linux serves over host networking, macOS over the pgcli-net
// bridge with the port published (the same path minio/silo/rustfs use), so the
// Mac's 127.0.0.1:<port> reaches it. The upstream image is multi-arch, so any
// host architecture works.
func NewRedisManager(cfg *config.Config) (*RedisManager, error) {
	return newKVManager(cfg, "redis", redisServerArgs)
}

// newKVManager is the shared constructor: kind selects the data-dir subdir and
// message label, serverArgs the server argv builder (redisServerArgs or
// valkeyServerArgs).
func newKVManager(cfg *config.Config, kind string, serverArgs func(*config.RedisConfig, string) []string) (*RedisManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &RedisManager{
		cfg:        cfg,
		podman:     path,
		dataDir:    dataDir,
		bridge:     platform.Detect() == platform.MacOS,
		kind:       kind,
		serverArgs: serverArgs,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/<kind>/<name>/data. It holds the
// RDB snapshot / AOF files and outlives the container — `pg addon remove`
// keeps it unless --clean-data, and reinstalling under the same name loads the
// old dump back.
func (m *RedisManager) resolveDataDir(rc *config.RedisConfig) string {
	if rc.DataDir != "" {
		return rc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", m.kind, rc.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *RedisManager) DataDir(rc *config.RedisConfig) string {
	return m.resolveDataDir(rc)
}

// EnsureImage pulls the addon image if it is not present locally (pull-only:
// the tags are public docker.io images; pgcli never builds them). Offline
// hosts pre-seed them with `podman load` like the core images.
func (m *RedisManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling %s image %s (is docker.io reachable?): %w", m.kind, imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the addon container (install semantics:
// stop/rm/recreate so updated ports/credentials take effect). The data dir is
// created if missing; it is never deleted here.
func (m *RedisManager) EnsureContainer(rc *config.RedisConfig) error {
	containerName := rc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("-> %s container already running, recreating to apply updated config...\n", m.kind)
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping %s container: %w", m.kind, err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing %s container: %w", m.kind, err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale %s container: %w", m.kind, err)
			}
		}
	}

	if err := m.createContainer(rc); err != nil {
		return err
	}
	fmt.Printf("  [OK] %s container started\n", m.kind)
	return nil
}

// StartContainer starts the addon container without touching config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh.
func (m *RedisManager) StartContainer(rc *config.RedisConfig) error {
	containerName := rc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] %s %s already running\n", m.kind, containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] %s %s started\n", m.kind, containerName)
			return nil
		}
		fmt.Printf("  [!] %s %s in improper state, recreating...\n", m.kind, containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper %s container: %w", m.kind, err)
		}
	}

	if err := m.createContainer(rc); err != nil {
		return err
	}
	fmt.Printf("  [OK] %s %s started\n", m.kind, containerName)
	return nil
}

// createContainer runs the server with the argv from the manager's serverArgs
// builder (pure, unit-tested there). Linux: host networking — the published
// port is simply rc.Port on sc.Listen's bind, default 0.0.0.0 (requirepass is
// on, so publishing the port is safe).
// macOS: the same port on the pgcli-net bridge, published -p N:N, with the
// bind widened to 0.0.0.0 via proxyBindHost so gvproxy can reach it.
// --stop-timeout 30 lets the server answer SIGTERM with its usual final RDB
// save instead of being killed after podman's 10s default.
func (m *RedisManager) createContainer(rc *config.RedisConfig) error {
	if err := os.MkdirAll(m.resolveDataDir(rc), 0755); err != nil {
		return fmt.Errorf("creating %s data dir %s: %w", m.kind, m.resolveDataDir(rc), err)
	}

	bind := proxyBindHost(m.bridge, rc.Listen)

	// A bridge container cannot reach a same-host published port via
	// 127.0.0.1 — that is the container's own loopback — so the master's
	// stored loopback address becomes podman's container→host alias here, at
	// the same layer that widens the bind above. redisServerArgs stays a pure
	// function of the config it is handed.
	if m.bridge && (rc.ReplicaHost == "127.0.0.1" || rc.ReplicaHost == "localhost") {
		rcCopy := *rc
		rcCopy.ReplicaHost = "host.containers.internal"
		rc = &rcCopy
	}

	// --http-proxy=false: podman would otherwise inject the host's HTTP(S)_PROXY
	// and the process could honor it for outbound connections. Image pulls run
	// client-side and keep the proxy.
	args := []string{
		"run", "-d",
		"--name", rc.ContainerName,
	}
	args = append(args, netFlags(m.bridge, m.cfg.Podman.Network, rc.Port)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(m.resolveDataDir(rc)), redisContainerDataDir),
		rc.ImageTag,
	)
	args = append(args, m.serverArgs(rc, bind)...)

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating %s container: %w", m.kind, err)
	}
	return nil
}

// Remove stops and removes the addon container. The data dir is kept by
// default (it holds the RDB/AOF — remove+reinstall revives the dataset);
// cleanData also deletes it, refusing when it is still a mount point.
func (m *RedisManager) Remove(rc *config.RedisConfig, cleanData bool) error {
	containerName := rc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing %s container: %w", m.kind, err)
	}
	fmt.Printf("  [OK] %s container removed\n", m.kind)

	dataDir := m.resolveDataDir(rc)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if isMountpoint(dataDir) {
		fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
		return nil
	}
	// The server writes dump.rdb as rootless-podman's mapped uid, which
	// os.RemoveAll cannot delete from the host — removeHostDir falls back to
	// `podman unshare rm` (same path rustfs needs for its uid-10001 files).
	if err := removeHostDir(m.podman, rc.ImageTag, dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if rc.DataDir == "" {
		// Default layout: also prune the <name> dir when it became empty. With
		// an override the parent belongs to the user — never touch it.
		_, _ = removeHostDirIfEmpty(m.podman, rc.ImageTag, filepath.Dir(dataDir))
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *RedisManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *RedisManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a redis container.
func (m *RedisManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// RedisCLI runs redis-cli from a short-lived container against the addon's
// published port; ValkeyCLI (valkey.go) does the same with valkey-cli. Both
// share kvCLI below. Caller-provided auth variables are forwarded; when none
// is set, the addon's config password is injected. Networking mirrors MCRunner —
// Linux uses host networking to reach the port on the host loopback, macOS the
// default bridge with host.containers.internal as the target (host networking
// there would only see the VM's loopback). An explicit host replaces that local
// target, so a remote endpoint is reachable from either platform. The image is
// pulled first if the host does not already have it.
func (m *RedisManager) RedisCLI(imageTag, password, host string, port int, cluster bool, args []string) error {
	return m.kvCLI("redis-cli", imageTag, password, host, port, cluster, args)
}

// kvCLI is the shared client runner: client is the binary inside the image
// ("redis-cli" or "valkey-cli"). cluster turns on the client's -c flag so
// MOVED redirects follow themselves; it is gated off when args start with
// `--cluster` (that subcommand does its own dialling and -c is redundant,
// though per the VM01 dry-check a stray -c is harmless either way — we still
// emit a clean command).
func (m *RedisManager) kvCLI(client, imageTag, password, host string, port int, cluster bool, args []string) error {
	if err := m.EnsureImage(imageTag); err != nil {
		return err
	}

	if host == "" {
		// Local addon target. Linux: host networking reaches the port on the
		// host's bind (default 0.0.0.0, at worst loopback) directly. macOS: the
		// default bridge and host.containers.internal — podman's documented
		// container→host address; host networking there would only see the VM's
		// loopback, not the published port.
		if m.bridge {
			host = "host.containers.internal"
		} else {
			host = "127.0.0.1"
		}
	}
	runArgs := []string{"run", "--rm"}
	if isTerminal(os.Stdin) {
		runArgs = append(runArgs, "-it")
	} else {
		runArgs = append(runArgs, "-i=false")
	}
	// An explicit remote --host needs no host networking; the default bridge
	// routes outbound on both platforms. Linux keeps host networking either way.
	if !m.bridge {
		runArgs = append(runArgs, "--network", "host")
	}
	// Same reason as createContainer: proxy injection would risk hijacking the
	// client connection.
	runArgs = append(runArgs, "--http-proxy=false")
	if client == "valkey-cli" {
		if auth, ok := os.LookupEnv("VALKEYCLI_AUTH"); ok {
			runArgs = append(runArgs,
				"-e", "VALKEYCLI_AUTH="+auth,
				"-e", "REDISCLI_AUTH="+auth,
			)
		} else if auth, ok := os.LookupEnv("REDISCLI_AUTH"); ok {
			runArgs = append(runArgs, "-e", "REDISCLI_AUTH="+auth)
		} else if password != "" {
			runArgs = append(runArgs, "-e", "REDISCLI_AUTH="+password)
		}
	} else if auth, ok := os.LookupEnv("REDISCLI_AUTH"); ok {
		runArgs = append(runArgs, "-e", "REDISCLI_AUTH="+auth)
	} else if password != "" {
		runArgs = append(runArgs, "-e", "REDISCLI_AUTH="+password)
	}
	runArgs = append(runArgs,
		imageTag,
		client,
	)
	if cluster && !startsClusterSubcmd(args) {
		runArgs = append(runArgs, "-c")
	}
	runArgs = append(runArgs,
		"-h", host,
		"-p", fmt.Sprintf("%d", port),
	)
	runArgs = append(runArgs, args...)

	slog.Debug("podman "+client, "image", imageTag, "host", host, "port", port, "args", args)
	cmd := podmanCommand(m.podman, runArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %s: %w", client, err)
	}
	return nil
}

// startsClusterSubcmd reports whether forwarded args open with redis-cli's
// `--cluster` admin subcommand (create/check/add-node/…), for which the
// cluster-aware -c flag is redundant.
func startsClusterSubcmd(args []string) bool {
	return len(args) > 0 && args[0] == "--cluster"
}

// --- Internal helpers ---------------------------------------------------

func (m *RedisManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("podman %s: %s", strings.Join(args, " "), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *RedisManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *RedisManager) imageExists(tag string) (bool, error) {
	out, err := m.run("images", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == tag {
			return true, nil
		}
	}
	return false, nil
}

func (m *RedisManager) containerExists(name string) (bool, error) {
	out, err := m.run("ps", "-a", "--filter", "name="+name, "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

func (m *RedisManager) containerRunning(name string) (bool, error) {
	out, err := m.run("ps", "--filter", "name="+name, "--filter", "status=running", "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}
