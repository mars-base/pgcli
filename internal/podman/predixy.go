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

// PredixyManager manages standalone Predixy containers — a Redis protocol
// proxy that re-exposes a native cluster as one ordinary redis:// endpoint, so
// clients need no cluster awareness. It is the haproxy shape again (shared
// routing infrastructure driven by a generated config file, host networking,
// top-level addon config), with two manager-level differences:
//
//   - the image is pgcli's own ghcr build (config.DefaultPredixyImageTag) and
//     pull-only with local-exists-then-pull fallback, so an offline host that
//     pre-seeded it with `podman load` uses it directly;
//   - the rendered predixy.conf is bind-mounted FILE-LEVEL over the image's
//     baked /usr/local/predixy/conf/predixy.conf. Predixy resolves
//     `Include license.conf` relative to the config file's own directory, and
//     the signed free-tier license.conf is a sibling baked into the image — a
//     directory-level mount would hide it and the binary refuses to start
//     without a valid license. pgcli never ships or manages the license; the
//     image build rotates it.
type PredixyManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

// NewPredixyManager creates a PredixyManager. Like haproxy the addon is
// Linux-only for now: it proxies cluster nodes reached over host networking,
// which the macOS podman machine does not expose to containers.
func NewPredixyManager(cfg *config.Config) (*PredixyManager, error) {
	if platform.Detect() == platform.MacOS {
		return nil, fmt.Errorf("the predixy addon is not supported on macOS yet: it proxies Redis nodes over host networking, which the podman machine does not provide")
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
	return &PredixyManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// predixyConfigDir returns the host directory holding an instance's predixy.conf.
func predixyConfigDir(baseDir, name string) string {
	return filepath.Join(baseDir, "addon", "predixy", name)
}

// predixyContainerConfPath is the path the image's ENTRYPOINT reads, and the
// mount target of the rendered file.
const predixyContainerConfPath = "/usr/local/predixy/conf/predixy.conf"

// RenderPredixyCfg builds the predixy.conf text for one instance. Pure function
// of the config so it can be unit-tested without podman.
//
// Syntax notes (verified against 7.0.1): every block's opening brace must sit
// on the block-name line — a brace on its own line is a parse error ("unmatched
// end scope"). Passwords are always the quoted form, which tolerates the
// specials the redis addon's generator produces (upper/lower/digit only today,
// but --password accepts anything); the CLI rejects a password containing a
// double quote or newline before this is ever called.
func RenderPredixyCfg(p *config.PredixyConfig) (string, error) {
	if len(p.Backend) == 0 {
		return "", fmt.Errorf("predixy %q has no backend nodes — pass --backend host:port[,host:port...]", p.Name)
	}
	if p.Password == "" {
		return "", fmt.Errorf("predixy %q has no password — pass --password (the proxied cluster's requirepass)", p.Name)
	}
	listen := p.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	workers := p.Workers
	if workers <= 0 {
		workers = config.DefaultPredixyWorkers
	}

	var b strings.Builder
	name := p.ContainerName
	if name == "" {
		name = "pgcli-predixy"
	}
	fmt.Fprintf(&b, "Name %s\n", name)
	fmt.Fprintf(&b, "Bind %s:%d\n", listen, p.Port)
	fmt.Fprintf(&b, "WorkerThreads %d\n", workers)
	// Relative to this file's directory inside the container — resolves to the
	// image's baked, signed license.conf sibling (see PredixyManager doc).
	b.WriteString("Include license.conf\n")
	b.WriteString("\n")
	b.WriteString("Authority {\n")
	fmt.Fprintf(&b, "    Auth %q {\n", p.Password)
	b.WriteString("        Mode admin\n")
	b.WriteString("    }\n")
	b.WriteString("}\n")
	b.WriteString("\n")
	b.WriteString("ClusterServerPool {\n")
	fmt.Fprintf(&b, "    Password %q\n", p.Password)
	b.WriteString("    Servers {\n")
	for _, backend := range p.Backend {
		fmt.Fprintf(&b, "        + %s\n", backend)
	}
	b.WriteString("    }\n")
	b.WriteString("}\n")
	return b.String(), nil
}

// WriteConfigs renders predixy.conf for the given instance into
// <baseDir>/addon/predixy/<name>/ and returns its host path. The file embeds
// the proxy password, but so does pg.yaml (every pgcli secret's storage
// posture); 0644 matches the haproxy precedent and keeps the file readable by
// the container process under rootless podman's uid mapping.
func (m *PredixyManager) WriteConfigs(p *config.PredixyConfig) (string, error) {
	cfg, err := RenderPredixyCfg(p)
	if err != nil {
		return "", err
	}
	dir := predixyConfigDir(m.dataDir, p.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating predixy config dir: %w", err)
	}
	path := filepath.Join(dir, "predixy.conf")
	if err := os.WriteFile(path, []byte(cfg), 0644); err != nil {
		return "", fmt.Errorf("writing predixy.conf: %w", err)
	}
	return path, nil
}

// ConfigDir returns the host directory holding an instance's rendered
// predixy.conf (for display in `pg addon list`).
func (m *PredixyManager) ConfigDir(p *config.PredixyConfig) string {
	return predixyConfigDir(m.dataDir, p.Name)
}

// EnsureImage pulls the predixy image if it is not present locally (pull-only:
// pgcli never builds during install; the ghcr wrapper is built by
// `make container-build-predixy`). Offline hosts pre-seed it with
// `podman load` like the core images — the local check finds those.
func (m *PredixyManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling predixy image %s (is ghcr.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the Predixy container (install
// semantics: stop/rm/recreate so a re-rendered predixy.conf takes effect).
func (m *PredixyManager) EnsureContainer(p *config.PredixyConfig) error {
	containerName := p.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> Predixy container already running, restarting to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping Predixy container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing Predixy container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale Predixy container: %w", err)
			}
		}
	}

	if err := m.createContainer(p); err != nil {
		return err
	}
	fmt.Println("  [OK] Predixy container started")
	return nil
}

// StartContainer starts the Predixy container without regenerating config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create from the predixy.conf
// already on disk.
func (m *PredixyManager) StartContainer(p *config.PredixyConfig) error {
	containerName := p.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] Predixy %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] Predixy %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] Predixy %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper Predixy container: %w", err)
		}
	}

	cfgPath := filepath.Join(predixyConfigDir(m.dataDir, p.Name), "predixy.conf")
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("predixy config %s not found -- run 'pg addon install predixy' first", cfgPath)
	}

	if err := m.createContainer(p); err != nil {
		return err
	}
	fmt.Printf("  [OK] Predixy %s started\n", containerName)
	return nil
}

// createContainer runs a Predixy instance with the rendered predixy.conf
// mounted read-only FILE-LEVEL at the ENTRYPOINT's path — see the
// PredixyManager doc for why not the whole conf dir. Host networking: the
// backends are Redis nodes' host ports and the listener must be reachable on
// the host's port directly. The image's ENTRYPOINT takes the config path, so
// no command args are appended.
func (m *PredixyManager) createContainer(p *config.PredixyConfig) error {
	hostConf := filepath.Join(predixyConfigDir(m.dataDir, p.Name), "predixy.conf")

	// --http-proxy=false: podman would otherwise inject the host's HTTP(S)_PROXY
	// and the process could honor it for outbound connections. Image pulls run
	// client-side and keep the proxy.
	args := []string{
		"run", "-d",
		"--name", p.ContainerName,
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(hostConf), predixyContainerConfPath),
		p.ImageTag,
	}

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating Predixy container: %w", err)
	}
	return nil
}

// Remove stops and removes the Predixy container, then deletes its config
// directory on the host. There is no data dir — the proxy is stateless.
func (m *PredixyManager) Remove(p *config.PredixyConfig) error {
	containerName := p.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing Predixy container: %w", err)
	}
	fmt.Println("  [OK] Predixy container removed")

	dir := predixyConfigDir(m.dataDir, p.Name)
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
func (m *PredixyManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it for the reuse/no-force check.
func (m *PredixyManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a Predixy container.
func (m *PredixyManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *PredixyManager) run(args ...string) (string, error) {
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

func (m *PredixyManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *PredixyManager) imageExists(tag string) (bool, error) {
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

func (m *PredixyManager) containerExists(name string) (bool, error) {
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

func (m *PredixyManager) containerRunning(name string) (bool, error) {
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
