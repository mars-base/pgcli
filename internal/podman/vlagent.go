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

// VlagentManager manages standalone vlagent containers — the official log
// agent of the VictoriaLogs family
// (https://docs.victoriametrics.com/victorialogs/vlagent/). Like VictoriaLogs
// it is a top-level, persistent addon on one host port with a host data dir
// that outlives the container; the two are natural partners (VictoriaLogs
// stores logs, vlagent collects and forwards them).
//
// Shape mirrors VictoriaLogsManager:
//   - No rendered config file: everything is passed as CLI flags after the
//     image name (-tmpDataPath / -httpListenAddr / -remoteWrite.url /
//     -fileCollector.* / ExtraArgs).
//   - No SIGHUP reload: changes require container recreation.
//   - No pre-chown: the image runs as root.
//
// The distinguishing feature is the file collector. vlagent only sees paths
// inside its container, so every host LogPath is bind-mounted read-only at the
// SAME absolute path; Includes/Excludes then name container paths. Excludes are
// positionally paired with Includes, exactly like vlagent's own
// -fileCollector.glob / -fileCollector.excludeGlob flags.
type VlagentManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

const (
	// vlagentContainerDataDir is the data location vlagent uses internally for
	// -tmpDataPath (buffers + file-collector checkpoints); pgcli bind-mounts
	// the host data dir here.
	vlagentContainerDataDir = "/var/lib/vlagent"
)

// NewVlagentManager creates a VlagentManager. It works on both platforms: Linux
// serves over host networking, macOS over the pgcli-net bridge with the port
// published, exactly like VictoriaLogs. The upstream image is multi-arch.
func NewVlagentManager(cfg *config.Config) (*VlagentManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &VlagentManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/vlagent/<name>/data. It holds the
// on-disk buffers and file-collector checkpoints and outlives the container —
// `pg addon remove` keeps it unless --clean-data, so reinstalling under the
// same name resumes reading where it stopped.
func (m *VlagentManager) resolveDataDir(vc *config.VlagentConfig) string {
	if vc.DataDir != "" {
		return vc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "vlagent", vc.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *VlagentManager) DataDir(vc *config.VlagentConfig) string {
	return m.resolveDataDir(vc)
}

// vlagentArgsAndEnv builds the full `podman run` argv for a vlagent instance.
// Pure function (no podman calls) so the mount + trailing-flag shape is
// unit-testable:
//   - NO --user flag: the image runs as root.
//   - One writable mount: the data dir at /var/lib/vlagent (:z for SELinux
//     relabeling under rootless podman).
//   - One read-only mount per LogPath, host path and container path IDENTICAL:
//     globs are matched inside the container, so keeping the host path makes
//     --include/--exclude values unambiguous. :ro only — :z would relabel
//     host log files, which is unacceptable for /var/log.
//   - Everything after ImageTag is the vlagent binary's OWN flags:
//     -tmpDataPath names the data mount (buffers + checkpoints),
//     -httpListenAddr pins the ingest listener to the assigned host port
//     (widened through proxyBindHost for the macOS bridge), one
//     -remoteWrite.url per URL (repeating it replicates to every target),
//     one -fileCollector.glob per include, and -fileCollector.excludeGlob
//     entries positionally paired with those globs — an empty entry is a
//     placeholder for "no exclude at this position", so pgcli emits every
//     entry up to the last non-empty one.
//   - ExtraArgs come LAST so they can override any earlier flag (last-wins
//     semantics).
//
// dataDir is the caller's already-resolved host path, so this stays a pure
// function of its arguments.
func vlagentArgsAndEnv(vc *config.VlagentConfig, bridge bool, network, dataDir string) []string {
	args := []string{"run", "-d", "--name", vc.ContainerName}
	args = append(args, netFlags(bridge, network, vc.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), vlagentContainerDataDir),
	)
	for _, p := range vc.LogPaths {
		args = append(args, "-v", fmt.Sprintf("%s:%s:ro", hostMountPath(p), p))
	}
	args = append(args, vc.ImageTag)
	args = append(args,
		"-tmpDataPath="+vlagentContainerDataDir,
		fmt.Sprintf("-httpListenAddr=%s:%d", proxyBindHost(bridge, vc.Listen), vc.HostPort),
	)
	for _, u := range vc.RemoteWriteURLs {
		args = append(args, "-remoteWrite.url="+u)
	}
	for _, g := range vc.Includes {
		args = append(args, "-fileCollector.glob="+g)
	}
	lastExclude := -1
	for i, e := range vc.Excludes {
		if e != "" {
			lastExclude = i
		}
	}
	for i := 0; i <= lastExclude; i++ {
		args = append(args, "-fileCollector.excludeGlob="+vc.Excludes[i])
	}
	// ExtraArgs LAST — last-wins semantics for vlagent CLI flags.
	args = append(args, vc.ExtraArgs...)
	return args
}

// createContainer runs a vlagent instance. It creates the data dir (no chown
// needed — the image runs as root), then starts the container.
func (m *VlagentManager) createContainer(vc *config.VlagentConfig) error {
	dataDir := m.resolveDataDir(vc)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating vlagent data dir %s: %w", dataDir, err)
	}

	args := vlagentArgsAndEnv(vc, m.bridge, m.cfg.Podman.Network, dataDir)
	slog.Debug("podman vlagent run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating vlagent container: %w", err)
	}
	return nil
}

// EnsureImage pulls the vlagent image if it is not present locally (pull-only:
// public docker.io/victoriametrics tags; pgcli never builds them). Offline
// hosts pre-seed them with `podman load` like the core images.
func (m *VlagentManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling vlagent image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the vlagent container (install
// semantics: stop/rm/recreate so changed ports/listen/URLs/globs/extra-args
// take effect). The data dir is created; never deleted here.
func (m *VlagentManager) EnsureContainer(vc *config.VlagentConfig) error {
	containerName := vc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> vlagent container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping vlagent container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing vlagent container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale vlagent container: %w", err)
			}
		}
	}

	if err := m.createContainer(vc); err != nil {
		return err
	}
	fmt.Println("  [OK] vlagent container started")
	return nil
}

// StartContainer starts the vlagent container without regenerating config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create. There is no config
// file to check, so a missing container is created from the stored config.
func (m *VlagentManager) StartContainer(vc *config.VlagentConfig) error {
	containerName := vc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] vlagent %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] vlagent %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] vlagent %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper vlagent container: %w", err)
		}
	}

	if err := m.createContainer(vc); err != nil {
		return err
	}
	fmt.Printf("  [OK] vlagent %s started\n", containerName)
	return nil
}

// Remove stops and removes the vlagent container. The data dir is kept by
// default (so reinstalling under the same name resumes buffered logs and file
// checkpoints) unless cleanData. Under rootless podman the mapped-uid data dir
// is reclaimed through removeHostDir's `podman unshare rm` fallback.
func (m *VlagentManager) Remove(vc *config.VlagentConfig, cleanData bool) error {
	containerName := vc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing vlagent container: %w", err)
	}
	fmt.Println("  [OK] vlagent container removed")

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
func (m *VlagentManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *VlagentManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a vlagent container.
func (m *VlagentManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *VlagentManager) run(args ...string) (string, error) {
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

func (m *VlagentManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *VlagentManager) imageExists(tag string) (bool, error) {
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

func (m *VlagentManager) containerExists(name string) (bool, error) {
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

func (m *VlagentManager) containerRunning(name string) (bool, error) {
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
