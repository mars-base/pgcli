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

// privoxyContainerConfigPath is where the rendered (or copied) config is
// bind-mounted. Overriding the image entrypoint is required because the
// shipped entrypoint rewrites this file in place, which fails on a read-only
// bind mount.
const privoxyContainerConfigPath = "/etc/privoxy/config"

// PrivoxyManager manages standalone Privoxy forward-proxy containers. Like
// NginxManager it operates over top-level (cross-instance) addon configs:
// Privoxy is shared egress infrastructure, not a per-instance sidecar.
//
// Privoxy forwards HTTP directly and tunnels HTTPS with CONNECT — it never
// intercepts TLS, so pgcli manages no CA or certificates. The configuration
// is rendered by pgcli (or copied verbatim from --conf-file) and bind-mounted
// read-only; logs go to the container's stdout.
type PrivoxyManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

// NewPrivoxyManager creates a PrivoxyManager. Linux serves over host
// networking; macOS uses the pgcli-net bridge with the port published. The
// image is multi-arch, so any host architecture works.
func NewPrivoxyManager(cfg *config.Config) (*PrivoxyManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &PrivoxyManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// privoxyConfigDir returns the host directory holding an instance's config.
func privoxyConfigDir(baseDir, name string) string {
	return filepath.Join(baseDir, "addon", "privoxy", name)
}

// RenderPrivoxyConfig builds the Privoxy config text for one instance. Pure
// function of the config so it can be unit-tested without podman.
//
// ACL semantics (Privoxy's own): once any permit-access/deny-access line
// exists, clients are denied by default and the LAST matching line wins.
// Therefore permits always render before denies. A deny-only configuration
// needs an explicit catch-all permit first, otherwise every client would be
// refused. "<spec>" is a native Privoxy ACL value (host, addr[/mask][:port],
// optionally followed by a destination address).
func RenderPrivoxyConfig(pc *config.PrivoxyConfig) (string, error) {
	if pc.Port <= 0 {
		return "", fmt.Errorf("privoxy %q has no listener port (set --port or privoxy_start_port)", pc.Name)
	}
	listen := pc.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	allow := config.NormalizeACLValues(pc.AllowNetworks)
	deny := config.NormalizeACLValues(pc.DenyNetworks)
	for _, spec := range append(append([]string{}, allow...), deny...) {
		if strings.ContainsAny(spec, "\r\n") {
			return "", fmt.Errorf("privoxy %q: ACL entry %q must be a single line", pc.Name, spec)
		}
	}

	var b strings.Builder
	b.WriteString("# Managed by pgcli -- changes are overwritten on install.\n")
	b.WriteString("#\n")
	b.WriteString("# Forward proxy: HTTP is forwarded directly, HTTPS uses CONNECT\n")
	b.WriteString("# tunneling. TLS is never intercepted.\n")
	b.WriteString("\n")
	b.WriteString("confdir /etc/privoxy\n")
	b.WriteString("logdir /var/log/privoxy\n")
	b.WriteString("logfile /dev/stdout\n")
	fmt.Fprintf(&b, "listen-address %s:%d\n", listen, pc.Port)
	b.WriteString("\n")
	b.WriteString("toggle 1\n")
	b.WriteString("enable-remote-toggle 0\n")
	b.WriteString("enable-remote-http-toggle 0\n")
	b.WriteString("enable-edit-actions 0\n")
	b.WriteString("enforce-blocks 0\n")
	b.WriteString("buffer-limit 4096\n")
	b.WriteString("enable-proxy-authentication-forwarding 0\n")
	b.WriteString("forwarded-connect-retries 0\n")
	b.WriteString("accept-intercepted-requests 0\n")
	b.WriteString("allow-cgi-request-crunching 0\n")
	b.WriteString("split-large-forms 0\n")
	b.WriteString("keep-alive-timeout 5\n")
	b.WriteString("tolerate-pipelining 1\n")
	b.WriteString("socket-timeout 300\n")

	if acl := privoxyACLLines(allow, deny, listen); len(acl) > 0 {
		b.WriteString("\n")
		b.WriteString("# Client ACLs: last match wins; any ACL line makes the default deny.\n")
		for _, line := range acl {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// privoxyACLLines renders permit-access/deny-access lines in match order.
func privoxyACLLines(allow, deny []string, listen string) []string {
	if len(allow) == 0 && len(deny) == 0 {
		return nil
	}
	lines := make([]string, 0, len(allow)+len(deny)+1)
	if len(allow) == 0 {
		// Blacklist mode: everything is permitted, then the denies below
		// carve out exceptions. Privoxy would otherwise deny every client.
		if strings.Contains(listen, ":") {
			lines = append(lines, "permit-access [::]/0")
		} else {
			lines = append(lines, "permit-access 0.0.0.0/0")
		}
	}
	for _, net := range allow {
		lines = append(lines, "permit-access "+net)
	}
	for _, net := range deny {
		lines = append(lines, "deny-access "+net)
	}
	return lines
}

// WriteConfig renders the instance config into
// <baseDir>/addon/privoxy/<name>/config. In --conf-file mode the user's file
// is copied instead. The file carries no secrets, so it is world-readable.
func (m *PrivoxyManager) WriteConfig(pc *config.PrivoxyConfig) (string, error) {
	dir := privoxyConfigDir(m.dataDir, pc.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating privoxy config dir: %w", err)
	}
	confPath := filepath.Join(dir, "config")

	if pc.ConfFile != "" {
		data, err := os.ReadFile(pc.ConfFile)
		if err != nil {
			return "", fmt.Errorf("reading user conf file %s: %w", pc.ConfFile, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("user conf file %s is empty", pc.ConfFile)
		}
		if err := os.WriteFile(confPath, data, 0644); err != nil {
			return "", fmt.Errorf("writing privoxy config: %w", err)
		}
		return confPath, nil
	}

	rendered := *pc
	rendered.Listen = proxyBindHost(m.bridge, pc.Listen)
	cfg, err := RenderPrivoxyConfig(&rendered)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(confPath, []byte(cfg), 0644); err != nil {
		return "", fmt.Errorf("writing privoxy config: %w", err)
	}
	return confPath, nil
}

// EnsureContainer creates or recreates the Privoxy container (install
// semantics: stop/rm/recreate so a re-rendered config takes effect).
func (m *PrivoxyManager) EnsureContainer(pc *config.PrivoxyConfig) error {
	containerName := pc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> privoxy container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping privoxy container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing privoxy container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale privoxy container: %w", err)
			}
		}
	}

	if err := m.createContainer(pc); err != nil {
		return err
	}
	fmt.Println("  [OK] privoxy container started")
	return nil
}

// StartContainer starts the container without re-rendering config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create from the config
// already on disk.
func (m *PrivoxyManager) StartContainer(pc *config.PrivoxyConfig) error {
	containerName := pc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] privoxy %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] privoxy %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] privoxy %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper privoxy container: %w", err)
		}
	}

	cfgPath := filepath.Join(privoxyConfigDir(m.dataDir, pc.Name), "config")
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("privoxy config %s not found -- run 'pg addon install privoxy' first", cfgPath)
	}

	if err := m.createContainer(pc); err != nil {
		return err
	}
	fmt.Printf("  [OK] privoxy %s started\n", containerName)
	return nil
}

// privoxyArgsAndEnv builds the full `podman run` argv for a Privoxy instance.
// Pure function (no podman calls) so the networking shape and mount set are
// unit-testable:
//   - netFlags: host networking on Linux, bridge with the port published on macOS.
//   - The config is bind-mounted read-only at /etc/privoxy/config.
//   - --entrypoint replaces the image script (which would rewrite the mounted
//     config); the proxy still starts as root and drops to the privoxy user.
func privoxyArgsAndEnv(pc *config.PrivoxyConfig, bridge bool, network, configHostDir string) []string {
	args := []string{"run", "-d", "--name", pc.ContainerName}
	args = append(args, netFlags(bridge, network, pc.Port)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--entrypoint", "/usr/sbin/privoxy",
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(filepath.Join(configHostDir, "config")), privoxyContainerConfigPath),
		pc.ImageTag,
		"--no-daemon", "--user", "privoxy", privoxyContainerConfigPath,
	)
	return args
}

// createContainer runs a Privoxy instance with its config bind-mounted
// read-only. Host networking on Linux; bridge with published port on macOS.
func (m *PrivoxyManager) createContainer(pc *config.PrivoxyConfig) error {
	dir := privoxyConfigDir(m.dataDir, pc.Name)
	args := privoxyArgsAndEnv(pc, m.bridge, m.cfg.Podman.Network, dir)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating privoxy container: %w", err)
	}
	return nil
}

// Remove deletes the container and the addon's config directory. Privoxy
// keeps no data of its own.
func (m *PrivoxyManager) Remove(pc *config.PrivoxyConfig) error {
	containerName := pc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing privoxy container: %w", err)
	}
	fmt.Println("  [OK] privoxy container removed")

	dir := privoxyConfigDir(m.dataDir, pc.Name)
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
func (m *PrivoxyManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// Stop stops a Privoxy container.
func (m *PrivoxyManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// EnsureImage pulls the Privoxy image if it is not present locally (pull-only:
// pgcli never builds during install).
func (m *PrivoxyManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling privoxy image %s (is ghcr.io reachable and logged in?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// --- Internal helpers ---------------------------------------------------

func (m *PrivoxyManager) run(args ...string) (string, error) {
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

func (m *PrivoxyManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *PrivoxyManager) imageExists(tag string) (bool, error) {
	out, err := m.run("image", "exists", tag)
	_ = out
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return false, nil
		}
		// podman exits non-zero when the image is absent; treat any run error
		// as "missing" and let the pull surface the real problem.
		return false, nil
	}
	return true, nil
}

func (m *PrivoxyManager) containerExists(name string) (bool, error) {
	if _, err := m.run("container", "exists", name); err != nil {
		return false, nil
	}
	return true, nil
}

func (m *PrivoxyManager) containerRunning(name string) (bool, error) {
	out, err := m.run("inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		return false, nil // not found or inspect failure → not running
	}
	return strings.TrimSpace(out) == "true", nil
}
