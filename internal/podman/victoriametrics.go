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

// VictoriaMetricsManager manages standalone VictoriaMetrics containers — a fast,
// Prometheus-compatible time-series database (https://victoriametrics.com/).
// Like Prometheus it is a top-level, persistent addon on one host port with a
// host data dir that outlives the container; the two are natural partners
// (VictoriaMetrics can serve as long-term storage for Prometheus).
//
// It is SIMPLER than Prometheus in three ways:
//   - No rendered config file: everything is passed as CLI flags after the image
//     name (-storageDataPath / -httpListenAddr / -retentionPeriod / ExtraArgs).
//   - No SIGHUP reload: VictoriaMetrics does not support hot config reload, so
//     changes require container recreation (install path always recreates).
//   - No pre-chown: the image runs as root, so the data dir needs no special
//     ownership setup (unlike Prometheus's uid 65534).
//
// There is NO login/password: VictoriaMetrics ships no authentication by default.
// Its HTTP API can ingest and query arbitrary metrics via PromQL/MetricsQL, so a
// remote-exposed instance is guarded only by the network boundary (firewall /
// --listen 127.0.0.1), same as Prometheus.
type VictoriaMetricsManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

const (
	// vmContainerDataDir is the data location VictoriaMetrics uses internally;
	// pgcli bind-mounts the host data dir here.
	vmContainerDataDir = "/victoria-metrics-data"
)

// NewVictoriaMetricsManager creates a VictoriaMetricsManager. It works on both
// platforms: Linux serves over host networking, macOS over the pgcli-net bridge
// with the port published (proxyBindHost widening makes it reachable through
// the VM), exactly like Prometheus. The upstream image is multi-arch.
func NewVictoriaMetricsManager(cfg *config.Config) (*VictoriaMetricsManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &VictoriaMetricsManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/victoriametrics/<name>/data. It
// holds the time-series data and outlives the container — `pg addon remove`
// keeps it unless --clean-data, so reinstalling under the same name revives
// history.
func (m *VictoriaMetricsManager) resolveDataDir(vc *config.VictoriaMetricsConfig) string {
	if vc.DataDir != "" {
		return vc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "victoriametrics", vc.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *VictoriaMetricsManager) DataDir(vc *config.VictoriaMetricsConfig) string {
	return m.resolveDataDir(vc)
}

// victoriaMetricsArgsAndEnv builds the full `podman run` argv for a
// VictoriaMetrics instance. Pure function (no podman calls) so the mount +
// trailing-flag shape is unit-testable:
//   - NO --user flag: the image runs as root, and the data dir arrives with
//     default ownership (no pre-chown needed).
//   - One mount: the writable data dir at /victoria-metrics-data (:z for
//     SELinux relabeling under rootless podman).
//   - Everything after ImageTag is the `victoria-metrics` binary's OWN flags —
//     podman stops parsing container flags at the image name, so these reach
//     the process. `-storageDataPath` names the in-container mount path;
//     `-httpListenAddr` pins the listener to the assigned host port (bind
//     widened through proxyBindHost for the macOS bridge); `-retentionPeriod`
//     is appended only when set. ExtraArgs come LAST so they can override any
//     earlier flag (last-wins semantics).
//
// dataDir is the caller's already-resolved host path, so this stays a pure
// function of its arguments.
func victoriaMetricsArgsAndEnv(vc *config.VictoriaMetricsConfig, bridge bool, network, dataDir string) []string {
	args := []string{"run", "-d", "--name", vc.ContainerName}
	args = append(args, netFlags(bridge, network, vc.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), vmContainerDataDir),
	)
	args = append(args, vc.ImageTag)
	args = append(args,
		"-storageDataPath="+vmContainerDataDir,
		fmt.Sprintf("-httpListenAddr=%s:%d", proxyBindHost(bridge, vc.Listen), vc.HostPort),
	)
	if vc.Retention != "" {
		args = append(args, "-retentionPeriod="+vc.Retention)
	}
	// ExtraArgs LAST — last-wins semantics for VictoriaMetrics CLI flags.
	args = append(args, vc.ExtraArgs...)
	return args
}

// createContainer runs a VictoriaMetrics instance. It creates the data dir
// (no chown needed — the image runs as root), then starts the container.
func (m *VictoriaMetricsManager) createContainer(vc *config.VictoriaMetricsConfig) error {
	dataDir := m.resolveDataDir(vc)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating VictoriaMetrics data dir %s: %w", dataDir, err)
	}

	args := victoriaMetricsArgsAndEnv(vc, m.bridge, m.cfg.Podman.Network, dataDir)
	slog.Debug("podman victoriametrics run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating VictoriaMetrics container: %w", err)
	}
	return nil
}

// EnsureImage pulls the VictoriaMetrics image if it is not present locally
// (pull-only: public docker.io/victoriametrics tags; pgcli never builds them).
// Offline hosts pre-seed them with `podman load` like the core images.
func (m *VictoriaMetricsManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling VictoriaMetrics image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the VictoriaMetrics container (install
// semantics: stop/rm/recreate so changed ports/listen/extra-args take effect).
// The data dir is created; never deleted here.
func (m *VictoriaMetricsManager) EnsureContainer(vc *config.VictoriaMetricsConfig) error {
	containerName := vc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> VictoriaMetrics container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping VictoriaMetrics container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing VictoriaMetrics container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale VictoriaMetrics container: %w", err)
			}
		}
	}

	if err := m.createContainer(vc); err != nil {
		return err
	}
	fmt.Println("  [OK] VictoriaMetrics container started")
	return nil
}

// StartContainer starts the VictoriaMetrics container without regenerating
// config (autostart-on-boot path): running → no-op; exists but stopped →
// podman start, recreating on improper state; missing → create. There is no
// config file to check (unlike Prometheus), so a missing container is created
// from the stored config.
func (m *VictoriaMetricsManager) StartContainer(vc *config.VictoriaMetricsConfig) error {
	containerName := vc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] VictoriaMetrics %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] VictoriaMetrics %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] VictoriaMetrics %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper VictoriaMetrics container: %w", err)
		}
	}

	if err := m.createContainer(vc); err != nil {
		return err
	}
	fmt.Printf("  [OK] VictoriaMetrics %s started\n", containerName)
	return nil
}

// Remove stops and removes the VictoriaMetrics container. The data dir is kept
// by default (so reinstalling under the same name revives history) unless
// cleanData. Under rootless podman the mapped-uid data dir is reclaimed through
// removeHostDir's `podman unshare rm` fallback.
func (m *VictoriaMetricsManager) Remove(vc *config.VictoriaMetricsConfig, cleanData bool) error {
	containerName := vc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing VictoriaMetrics container: %w", err)
	}
	fmt.Println("  [OK] VictoriaMetrics container removed")

	dataDir := m.resolveDataDir(vc)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if isMountpoint(dataDir) {
		fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
		return nil
	}
	if err := removeHostDir(m.podman, vc.ImageTag, dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if vc.DataDir == "" {
		// Default layout: also prune the <name> dir once data/ is gone. With an
		// override the parent belongs to the user — never touch it.
		_, _ = removeHostDirIfEmpty(m.podman, vc.ImageTag, filepath.Dir(dataDir))
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *VictoriaMetricsManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *VictoriaMetricsManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a VictoriaMetrics container.
func (m *VictoriaMetricsManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *VictoriaMetricsManager) run(args ...string) (string, error) {
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

func (m *VictoriaMetricsManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *VictoriaMetricsManager) imageExists(tag string) (bool, error) {
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

func (m *VictoriaMetricsManager) containerExists(name string) (bool, error) {
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

func (m *VictoriaMetricsManager) containerRunning(name string) (bool, error) {
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
