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

// RedisManager manages standalone Redis addon containers — a KV store for
// cache, session, ranking and atomic-counter data. Unlike the object stores it
// runs the plain upstream image (docker.io/library/redis): Redis starts as
// container root and needs no uid-drop wrapper (the rustfs reason for
// building pgcli-rustfs), so the image is pull-only and pgcli never builds it.
//
// The one runtime contract pgcli relies on is the `redis-server --flag value`
// argv (see redisServerArgs), identical across the majors the version table
// offers; the image's docker-entrypoint.sh passes it through untouched because
// the first argument is not "redis-cli".
type RedisManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports (host networking binds the VM loopback, invisible to the Mac)
}

// NewRedisManager creates a RedisManager. It works on both platforms: Linux
// serves over host networking, macOS over the pgcli-net bridge with the port
// published (the same path minio/silo/rustfs use), so the Mac's
// 127.0.0.1:<port> reaches it. The upstream image is multi-arch, so any host
// architecture works.
func NewRedisManager(cfg *config.Config) (*RedisManager, error) {
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
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/redis/<name>/data. It holds the
// RDB snapshot and outlives the container — `pg addon remove` keeps it unless
// --clean-data, and reinstalling under the same name loads the old dump back.
func (m *RedisManager) resolveDataDir(rc *config.RedisConfig) string {
	if rc.DataDir != "" {
		return rc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "redis", rc.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *RedisManager) DataDir(rc *config.RedisConfig) string {
	return m.resolveDataDir(rc)
}

// EnsureImage pulls the redis image if it is not present locally (pull-only:
// the tags are public docker.io/library images; pgcli never builds them).
// Offline hosts pre-seed them with `podman load` like the core images.
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
		return fmt.Errorf("pulling redis image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the redis container (install semantics:
// stop/rm/recreate so updated ports/credentials take effect). The data dir is
// created if missing; it is never deleted here.
func (m *RedisManager) EnsureContainer(rc *config.RedisConfig) error {
	containerName := rc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> redis container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping redis container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing redis container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale redis container: %w", err)
			}
		}
	}

	if err := m.createContainer(rc); err != nil {
		return err
	}
	fmt.Println("  [OK] redis container started")
	return nil
}

// StartContainer starts the redis container without touching config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh.
func (m *RedisManager) StartContainer(rc *config.RedisConfig) error {
	containerName := rc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] redis %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] redis %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] redis %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper redis container: %w", err)
		}
	}

	if err := m.createContainer(rc); err != nil {
		return err
	}
	fmt.Printf("  [OK] redis %s started\n", containerName)
	return nil
}

// createContainer runs redis-server with the argv from redisServerArgs
// (pure, unit-tested there). Linux: host networking — the published port is
// simply rc.Port on sc.Listen's bind, default 0.0.0.0 (requirepass is on, so
// publishing the port is safe).
// macOS: the same port on the pgcli-net bridge, published -p N:N, with the
// bind widened to 0.0.0.0 via proxyBindHost so gvproxy can reach it.
// --stop-timeout 30 lets Redis answer SIGTERM with its usual final RDB save
// instead of being killed after podman's 10s default.
func (m *RedisManager) createContainer(rc *config.RedisConfig) error {
	if err := os.MkdirAll(m.resolveDataDir(rc), 0755); err != nil {
		return fmt.Errorf("creating redis data dir %s: %w", m.resolveDataDir(rc), err)
	}

	bind := proxyBindHost(m.bridge, rc.Listen)

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
	args = append(args, redisServerArgs(rc, bind)...)

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating redis container: %w", err)
	}
	return nil
}

// Remove stops and removes the redis container. The data dir is kept by
// default (it holds the RDB — remove+reinstall revives the dataset); cleanData
// also deletes it, refusing when it is still a mount point.
func (m *RedisManager) Remove(rc *config.RedisConfig, cleanData bool) error {
	containerName := rc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing redis container: %w", err)
	}
	fmt.Println("  [OK] redis container removed")

	dataDir := m.resolveDataDir(rc)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if isMountpoint(dataDir) {
		fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
		return nil
	}
	// Redis writes dump.rdb as rootless-podman's mapped uid, which os.RemoveAll
	// cannot delete from the host — removeHostDir falls back to `podman unshare
	// rm` (same path rustfs needs for its uid-10001 files).
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
// published port. Auth follows redis-cli's native precedence: a REDISCLI_AUTH
// already present in the caller's environment is forwarded and wins, otherwise
// the addon's config password is injected. Networking mirrors MCRunner — Linux
// uses host networking to reach the port on the host loopback, macOS the
// default bridge with host.containers.internal as the target (host
// networking there would only see the VM's loopback). An explicit host (from
// pg redis-cli --host) replaces that local target, so a remote endpoint is
// reachable from either platform. The image is pulled first if the host does
// not already have it.
func (m *RedisManager) RedisCLI(imageTag, password, host string, port int, args []string) error {
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
	if auth, ok := os.LookupEnv("REDISCLI_AUTH"); ok {
		runArgs = append(runArgs, "-e", "REDISCLI_AUTH="+auth)
	} else if password != "" {
		runArgs = append(runArgs, "-e", "REDISCLI_AUTH="+password)
	}
	runArgs = append(runArgs,
		imageTag,
		"redis-cli",
		"-h", host,
		"-p", fmt.Sprintf("%d", port),
	)
	runArgs = append(runArgs, args...)

	slog.Debug("podman redis-cli", "image", imageTag, "host", host, "port", port, "args", args)
	cmd := podmanCommand(m.podman, runArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running redis-cli: %w", err)
	}
	return nil
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
