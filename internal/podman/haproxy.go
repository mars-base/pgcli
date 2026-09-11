package podman

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// DefaultHAProxyImageTag is the built-in HAProxy image when an instance does
// not override it. The tag is pinned to a recent stable release; the official
// image's default CMD already loads /usr/local/etc/haproxy/haproxy.cfg in the
// foreground, which is exactly where pgcli bind-mounts the rendered config.
const DefaultHAProxyImageTag = "docker.io/library/haproxy:3.2.23-alpine"

// HAProxyManager manages standalone HAProxy containers in front of Patroni
// clusters. Like EtcdManager/PgDogManager it operates over the top-level
// (cross-instance) addon configs: HAProxy is shared routing infrastructure,
// not a per-instance sidecar.
//
// Routing follows the official Patroni haproxy.cfg pattern: the backend TCP
// port is the member's PostgreSQL port, while the health check runs against
// the member's REST API port (GET / → 200 only on the leader, GET /replica →
// 200 only on replicas; those GETs are unauthenticated by design). Two modes:
//
//	unified — one listener sends all traffic to the current leader
//	split   — a rw listener to the leader plus a ro listener spread over
//	          replicas (read/write separation)
type HAProxyManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

// NewHAProxyManager creates an HAProxyManager. The addon is Linux-only for
// now: Patroni members are only reachable via host networking, which the macOS
// podman machine does not expose to containers.
func NewHAProxyManager(cfg *config.Config) (*HAProxyManager, error) {
	if platform.Detect() == platform.MacOS {
		return nil, fmt.Errorf("the haproxy addon is not supported on macOS yet: it frontends Patroni members over host networking, which the podman machine does not provide")
	}
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &HAProxyManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// configDir returns the host directory holding an instance's haproxy.cfg.
func haProxyConfigDir(baseDir, name string) string {
	return filepath.Join(baseDir, "addon", "haproxy", name)
}

// RenderHAProxyCfg builds the haproxy.cfg text for one instance. Pure function
// of the config so it can be unit-tested without podman.
func RenderHAProxyCfg(h *config.HAProxyConfig) (string, error) {
	if len(h.Targets) == 0 {
		return "", fmt.Errorf("haproxy %q has no backend targets — pass --node NAME=HOST:PGPORT:RESTPORT or --ha <scope>", h.Name)
	}
	listen := h.Listen
	if listen == "" {
		listen = "127.0.0.1"
	}

	var b strings.Builder
	b.WriteString("global\n")
	b.WriteString("    maxconn 4096\n")
	b.WriteString("    log stdout format raw local0 info\n")
	b.WriteString("\n")
	b.WriteString("defaults\n")
	b.WriteString("    log global\n")
	b.WriteString("    mode tcp\n")
	b.WriteString("    retries 2\n")
	b.WriteString("    timeout client 30m\n")
	b.WriteString("    timeout connect 4s\n")
	b.WriteString("    timeout server 30m\n")
	b.WriteString("    timeout check 5s\n")
	b.WriteString("\n")

	scope := h.Name
	if scope == "" {
		scope = "pg"
	}

	// Stats page (HTTP) so `pg haproxy` users can watch server up/down state.
	fmt.Fprintf(&b, "listen stats\n")
	fmt.Fprintf(&b, "    mode http\n")
	fmt.Fprintf(&b, "    bind %s:%d\n", listen, h.StatsPort)
	b.WriteString("    stats enable\n")
	b.WriteString("    stats uri /\n")
	b.WriteString("    stats refresh 10s\n\n")

	// server lines are identical across listeners; only the health-check URI
	// (and on-marked-down handling) differs.
	servers := func(buf *strings.Builder) {
		for _, t := range h.Targets {
			fmt.Fprintf(buf, "    server %s %s:%d maxconn 100 check port %d\n", t.Name, t.Host, t.PGPort, t.RestPort)
		}
	}

	switch h.EffectiveMode() {
	case "split":
		b.WriteString("listen " + scope + "_rw\n")
		fmt.Fprintf(&b, "    bind %s:%d\n", listen, h.WritePort)
		b.WriteString("    option httpchk GET /\n")
		b.WriteString("    http-check expect status 200\n")
		b.WriteString("    default-server inter 3s fall 3 rise 2 on-marked-down shutdown-sessions\n")
		servers(&b)
		b.WriteString("\n")

		roURI := "/replica"
		if h.ReplicaMaxLag != "" {
			roURI += "?lag=" + h.ReplicaMaxLag
		}
		b.WriteString("listen " + scope + "_ro\n")
		fmt.Fprintf(&b, "    bind %s:%d\n", listen, h.ReadPort)
		fmt.Fprintf(&b, "    option httpchk GET %s\n", roURI)
		b.WriteString("    http-check expect status 200\n")
		b.WriteString("    default-server inter 3s fall 3 rise 2\n")
		servers(&b)
	default: // unified
		b.WriteString("listen " + scope + "\n")
		fmt.Fprintf(&b, "    bind %s:%d\n", listen, h.WritePort)
		b.WriteString("    option httpchk GET /\n")
		b.WriteString("    http-check expect status 200\n")
		b.WriteString("    default-server inter 3s fall 3 rise 2 on-marked-down shutdown-sessions\n")
		servers(&b)
	}
	return b.String(), nil
}

// WriteConfigs renders haproxy.cfg for the given instance into
// <baseDir>/addon/haproxy/<name>/. The file carries no secrets (only hosts,
// ports and health-check URIs), so it is world-readable — required, in fact,
// because the official image drops to its non-root "haproxy" user.
func (m *HAProxyManager) WriteConfigs(h *config.HAProxyConfig) (string, error) {
	cfg, err := RenderHAProxyCfg(h)
	if err != nil {
		return "", err
	}
	dir := haProxyConfigDir(m.dataDir, h.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating haproxy config dir: %w", err)
	}
	path := filepath.Join(dir, "haproxy.cfg")
	if err := os.WriteFile(path, []byte(cfg), 0644); err != nil {
		return "", fmt.Errorf("writing haproxy.cfg: %w", err)
	}
	return path, nil
}

// EnsureContainer creates or restarts the HAProxy container (install
// semantics: stop/rm/recreate so a re-rendered haproxy.cfg takes effect).
func (m *HAProxyManager) EnsureContainer(h *config.HAProxyConfig) error {
	containerName := h.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> HAProxy container already running, restarting to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping HAProxy container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing HAProxy container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale HAProxy container: %w", err)
			}
		}
	}

	if err := m.createContainer(h); err != nil {
		return err
	}
	fmt.Println("  [OK] HAProxy container started")
	return nil
}

// StartContainer starts the HAProxy container without regenerating config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create from the haproxy.cfg
// already on disk.
func (m *HAProxyManager) StartContainer(h *config.HAProxyConfig) error {
	containerName := h.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] HAProxy %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] HAProxy %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] HAProxy %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper HAProxy container: %w", err)
		}
	}

	cfgPath := filepath.Join(haProxyConfigDir(m.dataDir, h.Name), "haproxy.cfg")
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("haproxy config %s not found -- run 'pg addon install haproxy' first", cfgPath)
	}

	if err := m.createContainer(h); err != nil {
		return err
	}
	fmt.Printf("  [OK] HAProxy %s started\n", containerName)
	return nil
}

// createContainer runs an HAProxy instance with its config directory mounted
// read-only at the image's config location. Host networking: the backends are
// Patroni members' host ports and the listeners must be reachable on the
// host's ports directly.
func (m *HAProxyManager) createContainer(h *config.HAProxyConfig) error {
	dir := haProxyConfigDir(m.dataDir, h.Name)

	// --http-proxy=false: podman would otherwise inject the host's HTTP(S)_PROXY
	// and the process could honor it for outbound connections. Image pulls run
	// client-side and keep the proxy.
	args := []string{
		"run", "-d",
		"--name", h.ContainerName,
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:/usr/local/etc/haproxy:ro,z", hostMountPath(dir)),
		h.ImageTag,
		"haproxy", "-f", "/usr/local/etc/haproxy/haproxy.cfg", "-db",
	}

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating HAProxy container: %w", err)
	}
	return nil
}

// Remove stops and removes the HAProxy container, then deletes its config
// directory on the host.
func (m *HAProxyManager) Remove(h *config.HAProxyConfig) error {
	containerName := h.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing HAProxy container: %w", err)
	}
	fmt.Println("  [OK] HAProxy container removed")

	dir := haProxyConfigDir(m.dataDir, h.Name)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing config dir %s: %v\n", dir, err)
	} else {
		fmt.Printf("  [OK] Config directory removed: %s\n", dir)
	}
	parent := filepath.Dir(dir)
	os.Remove(parent) // ignore error — non-empty dir won't be removed

	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *HAProxyManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// Stop stops an HAProxy container.
func (m *HAProxyManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *HAProxyManager) run(args ...string) (string, error) {
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

func (m *HAProxyManager) containerExists(name string) (bool, error) {
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

func (m *HAProxyManager) containerRunning(name string) (bool, error) {
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
