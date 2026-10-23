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

// VictoriaLogsManager manages standalone VictoriaLogs containers — the log
// database of the VictoriaMetrics family (https://docs.victoriametrics.com/victorialogs/).
// Like VictoriaMetrics it is a top-level, persistent addon on one host port
// with a host data dir that outlives the container; the two are natural
// partners (VictoriaMetrics keeps metrics, VictoriaLogs keeps logs).
//
// It is the exact mirror of VictoriaMetricsManager in shape:
//   - No rendered config file: everything is passed as CLI flags after the
//     image name (-storageDataPath / -httpListenAddr / -retentionPeriod /
//     ExtraArgs).
//   - No SIGHUP reload: VictoriaLogs has no config to reload, so changes
//     require container recreation (install path always recreates).
//   - No pre-chown: the image runs as root, so the data dir needs no special
//     ownership setup (unlike Prometheus's uid 65534).
//
// There is NO login/password: VictoriaLogs ships no authentication by default.
// Its HTTP API can ingest and query arbitrary logs via LogsQL, so a
// remote-exposed instance is guarded only by the network boundary (firewall /
// --listen 127.0.0.1), same as VictoriaMetrics.
type VictoriaLogsManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

const (
	// vlContainerDataDir is the data location VictoriaLogs uses internally;
	// pgcli bind-mounts the host data dir here.
	vlContainerDataDir = "/victoria-logs-data"
)

// NewVictoriaLogsManager creates a VictoriaLogsManager. It works on both
// platforms: Linux serves over host networking, macOS over the pgcli-net bridge
// with the port published (proxyBindHost widening makes it reachable through
// the VM), exactly like VictoriaMetrics. The upstream image is multi-arch.
func NewVictoriaLogsManager(cfg *config.Config) (*VictoriaLogsManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &VictoriaLogsManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/victorialogs/<name>/data. It holds
// the log data and outlives the container — `pg addon remove` keeps it unless
// --clean-data, so reinstalling under the same name revives history.
func (m *VictoriaLogsManager) resolveDataDir(lc *config.VictoriaLogsConfig) string {
	if lc.DataDir != "" {
		return lc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "victorialogs", lc.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *VictoriaLogsManager) DataDir(lc *config.VictoriaLogsConfig) string {
	return m.resolveDataDir(lc)
}

// victoriaLogsArgsAndEnv builds the full `podman run` argv for a VictoriaLogs
// instance. Pure function (no podman calls) so the mount + trailing-flag shape
// is unit-testable — it is the exact mirror of victoriaMetricsArgsAndEnv:
//   - NO --user flag: the image runs as root, and the data dir arrives with
//     default ownership (no pre-chown needed).
//   - One mount: the writable data dir at /victoria-logs-data (:z for SELinux
//     relabeling under rootless podman).
//   - Everything after ImageTag is the `victoria-logs` binary's OWN flags —
//     podman stops parsing container flags at the image name, so these reach
//     the process. `-storageDataPath` names the in-container mount path;
//     `-httpListenAddr` pins the listener to the assigned host port (bind
//     widened through proxyBindHost for the macOS bridge); `-retentionPeriod`
//     is appended only when set (empty lets the binary's own default stand).
//     ExtraArgs come LAST so they can override any earlier flag (last-wins
//     semantics).
//
// dataDir is the caller's already-resolved host path, so this stays a pure
// function of its arguments.
func victoriaLogsArgsAndEnv(lc *config.VictoriaLogsConfig, bridge bool, network, dataDir string) []string {
	args := []string{"run", "-d", "--name", lc.ContainerName}
	args = append(args, netFlags(bridge, network, lc.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), vlContainerDataDir),
	)
	args = append(args, lc.ImageTag)
	args = append(args,
		"-storageDataPath="+vlContainerDataDir,
		fmt.Sprintf("-httpListenAddr=%s:%d", proxyBindHost(bridge, lc.Listen), lc.HostPort),
	)
	if lc.Retention != "" {
		args = append(args, "-retentionPeriod="+lc.Retention)
	}
	// ExtraArgs LAST — last-wins semantics for VictoriaLogs CLI flags.
	args = append(args, lc.ExtraArgs...)
	return args
}

// createContainer runs a VictoriaLogs instance. It creates the data dir (no
// chown needed — the image runs as root), then starts the container.
func (m *VictoriaLogsManager) createContainer(lc *config.VictoriaLogsConfig) error {
	dataDir := m.resolveDataDir(lc)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating VictoriaLogs data dir %s: %w", dataDir, err)
	}

	args := victoriaLogsArgsAndEnv(lc, m.bridge, m.cfg.Podman.Network, dataDir)
	slog.Debug("podman victorialogs run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating VictoriaLogs container: %w", err)
	}
	return nil
}

// EnsureImage pulls the VictoriaLogs image if it is not present locally
// (pull-only: public docker.io/victoriametrics tags; pgcli never builds them).
// Offline hosts pre-seed them with `podman load` like the core images.
func (m *VictoriaLogsManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling VictoriaLogs image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the VictoriaLogs container (install
// semantics: stop/rm/recreate so changed ports/listen/extra-args take effect).
// The data dir is created; never deleted here.
func (m *VictoriaLogsManager) EnsureContainer(lc *config.VictoriaLogsConfig) error {
	containerName := lc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> VictoriaLogs container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping VictoriaLogs container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing VictoriaLogs container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale VictoriaLogs container: %w", err)
			}
		}
	}

	if err := m.createContainer(lc); err != nil {
		return err
	}
	fmt.Println("  [OK] VictoriaLogs container started")
	return nil
}

// StartContainer starts the VictoriaLogs container without regenerating
// config (autostart-on-boot path): running → no-op; exists but stopped →
// podman start, recreating on improper state; missing → create. There is no
// config file to check (unlike Prometheus), so a missing container is created
// from the stored config.
func (m *VictoriaLogsManager) StartContainer(lc *config.VictoriaLogsConfig) error {
	containerName := lc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] VictoriaLogs %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] VictoriaLogs %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] VictoriaLogs %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper VictoriaLogs container: %w", err)
		}
	}

	if err := m.createContainer(lc); err != nil {
		return err
	}
	fmt.Printf("  [OK] VictoriaLogs %s started\n", containerName)
	return nil
}

// Remove stops and removes the VictoriaLogs container. The data dir is kept by
// default (so reinstalling under the same name revives stored logs) unless
// cleanData. Under rootless podman the mapped-uid data dir is reclaimed through
// removeHostDir's `podman unshare rm` fallback.
func (m *VictoriaLogsManager) Remove(lc *config.VictoriaLogsConfig, cleanData bool) error {
	containerName := lc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing VictoriaLogs container: %w", err)
	}
	fmt.Println("  [OK] VictoriaLogs container removed")

	dataDir := m.resolveDataDir(lc)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if isMountpoint(dataDir) {
		fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
		return nil
	}
	if err := removeHostDir(m.podman, lc.ImageTag, dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if lc.DataDir == "" {
		// Default layout: also prune the <name> dir once data/ is gone. With an
		// override the parent belongs to the user — never touch it.
		_, _ = removeHostDirIfEmpty(m.podman, lc.ImageTag, filepath.Dir(dataDir))
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *VictoriaLogsManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *VictoriaLogsManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a VictoriaLogs container.
func (m *VictoriaLogsManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *VictoriaLogsManager) run(args ...string) (string, error) {
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

func (m *VictoriaLogsManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *VictoriaLogsManager) imageExists(tag string) (bool, error) {
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

func (m *VictoriaLogsManager) containerExists(name string) (bool, error) {
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

func (m *VictoriaLogsManager) containerRunning(name string) (bool, error) {
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
