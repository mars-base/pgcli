package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/influxdb"
)

const influxDBEntrypointDir = "/usr/local/bin"

// InfluxDBManager manages standalone InfluxDB 1.x addon containers. The image
// is the pgcli-built pgcli-influxdb (the official linux/amd64 tarball on
// Alpine), so the addon is Linux-only and pull-only. influxd uses host
// networking: the authenticated HTTP API listens on Listen:HostPort, and the
// unauthenticated backup/restore RPC stays on the host's loopback (8088) where
// a one-shot container on the same network namespace can reach it.
type InfluxDBManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

// NewInfluxDBManager creates an InfluxDBManager.
func NewInfluxDBManager(cfg *config.Config) (*InfluxDBManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := ""
	if cfg != nil {
		dataDir = cfg.BaseDir
	}
	ensurePodmanStateReady(path)
	return &InfluxDBManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// EnsureImage pulls the influxdb image if it is not present locally (pull-only:
// the tag is published to the ghcr repo; pgcli never builds it here).
func (m *InfluxDBManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling influxdb image %s (is ghcr.io/mars-base/pgcli reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the container (install semantics:
// stop/rm/recreate so changed ports or a regenerated config take effect). The
// data dir and generated config are ensured first; neither is ever deleted
// here. It waits until /ping answers.
func (m *InfluxDBManager) EnsureContainer(ic *config.InfluxDBConfig) error {
	if err := m.ensureFiles(ic); err != nil {
		return err
	}
	running, err := m.containerRunning(ic.ContainerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> influxdb container already running, recreating to apply updated config...")
	}
	exists, err := m.containerExists(ic.ContainerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("rm", "-f", ic.ContainerName); err != nil {
			return fmt.Errorf("removing influxdb container: %w", err)
		}
	}
	if err := m.createContainer(ic); err != nil {
		return err
	}
	return m.waitReady(ic)
}

// StartContainer starts the container without recreating config (start and
// autostart path): running → no-op; exists but stopped → podman start,
// recreating on improper state; missing → create fresh. It waits for /ping.
// The first return value reports that it was already running.
func (m *InfluxDBManager) StartContainer(ic *config.InfluxDBConfig) (bool, error) {
	if err := m.ensureFiles(ic); err != nil {
		return false, err
	}
	running, err := m.containerRunning(ic.ContainerName)
	if err != nil {
		return false, err
	}
	if running {
		return true, nil
	}

	exists, err := m.containerExists(ic.ContainerName)
	if err != nil {
		return false, err
	}
	if exists {
		if _, err := m.run("start", ic.ContainerName); err == nil {
			fmt.Printf("  [OK] influxdb container %s started\n", ic.ContainerName)
			if err := m.waitReady(ic); err != nil {
				return false, err
			}
			return false, nil
		}
		fmt.Printf("  [!] influxdb container %s in improper state, recreating...\n", ic.ContainerName)
		if _, err := m.run("rm", "-f", ic.ContainerName); err != nil {
			return false, fmt.Errorf("removing improper influxdb container: %w", err)
		}
	}
	if err := m.createContainer(ic); err != nil {
		return false, err
	}
	if err := m.waitReady(ic); err != nil {
		return false, err
	}
	return false, nil
}

// ensureFiles creates the host data dir and re-renders the config when it is
// missing, so a start after a manual cleanup does not fail on an empty mount.
func (m *InfluxDBManager) ensureFiles(ic *config.InfluxDBConfig) error {
	if err := os.MkdirAll(ic.DataDir, 0o755); err != nil {
		return fmt.Errorf("creating data dir %s: %w", ic.DataDir, err)
	}
	if _, err := os.Stat(ic.ConfigPath); os.IsNotExist(err) {
		if err := influxdb.WriteConfig(*ic); err != nil {
			return err
		}
	}
	return nil
}

func (m *InfluxDBManager) createContainer(ic *config.InfluxDBConfig) error {
	args := influxDBCreateArgs(*ic)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating influxdb container: %w", err)
	}
	fmt.Printf("  [OK] influxdb container %s created\n", ic.ContainerName)
	return nil
}

// influxDBCreateArgs builds the `podman run` argv for influxd. Pure so tests
// can pin the mounts and host networking without podman.
func influxDBCreateArgs(ic config.InfluxDBConfig) []string {
	return []string{
		"run", "-d",
		"--name", ic.ContainerName,
		// Host networking: the port in the generated config is the host port,
		// and the loopback-only RPC stays reachable by one-shot helper
		// containers on the same network namespace.
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(ic.DataDir), influxdb.ContainerDataDir),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(ic.ConfigPath), influxdb.ContainerConfigPath),
		ic.ImageTag,
		"-config", influxdb.ContainerConfigPath,
	}
}

func (m *InfluxDBManager) waitReady(ic *config.InfluxDBConfig) error {
	if err := influxdb.WaitForPing(*ic, influxdb.StartTimeout); err != nil {
		return fmt.Errorf("influxdb did not become ready at %s: %w", influxdb.PingURL(*ic), err)
	}
	return nil
}

// Stop stops the container and reports whether it was running.
func (m *InfluxDBManager) Stop(name string) (bool, error) {
	running, err := m.containerRunning(name)
	if err != nil || !running {
		return false, err
	}
	if _, err := m.run("stop", "-t", "30", name); err != nil {
		return true, fmt.Errorf("stopping influxdb container: %w", err)
	}
	return true, nil
}

// RemoveContainer stops and removes the container. Data and the generated
// config are kept; CleanData deletes them.
func (m *InfluxDBManager) RemoveContainer(ic *config.InfluxDBConfig) error {
	exists, err := m.containerExists(ic.ContainerName)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("  [OK] influxdb container %s already absent\n", ic.ContainerName)
		return nil
	}
	if _, err := m.run("rm", "-f", ic.ContainerName); err != nil {
		return fmt.Errorf("removing influxdb container: %w", err)
	}
	fmt.Printf("  [OK] influxdb container %s removed\n", ic.ContainerName)
	return nil
}

// CleanData deletes the instance directory (generated config, local backups)
// and a distinct custom data dir. Files may be written as a rootless-podman
// mapped uid, so removal falls back to `podman unshare rm`.
func (m *InfluxDBManager) CleanData(ic *config.InfluxDBConfig) error {
	dirs := []string{filepath.Dir(ic.ConfigPath)}
	if ic.DataDir != "" && filepath.Clean(ic.DataDir) != filepath.Clean(dirs[0]) {
		dirs = append(dirs, ic.DataDir)
	}
	for _, dir := range dirs {
		if err := removeHostDir(m.podman, ic.ImageTag, dir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cleaning %s: %w", dir, err)
		}
		fmt.Printf("  [OK] cleaned %s\n", dir)
	}
	return nil
}

// ContainerRunning reports whether the instance container is running.
func (m *InfluxDBManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether the instance container exists.
func (m *InfluxDBManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// InfluxDBRunSpec describes one short-lived helper container on the host
// network. Mounts are host:container[:opts] strings; Env entries are
// KEY=VALUE and passed to podman by NAME only, so credentials never appear in
// the podman argv.
type InfluxDBRunSpec struct {
	Entrypoint  string
	Mounts      []string
	Env         []string
	Args        []string
	Interactive bool
}

// Run executes a helper container and passes stdio through. The image is
// pulled first when missing.
func (m *InfluxDBManager) Run(ic *config.InfluxDBConfig, spec InfluxDBRunSpec) error {
	if err := m.EnsureImage(ic.ImageTag); err != nil {
		return err
	}
	cmd := podmanCommand(m.podman, influxDBRunArgs(ic.ImageTag, spec)...)
	if len(spec.Env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, spec.Env...)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running influxdb helper container: %w", err)
	}
	return nil
}

// Output executes a non-interactive helper container and returns its combined
// output (used for version probes and the auth bootstrap).
func (m *InfluxDBManager) Output(ic *config.InfluxDBConfig, spec InfluxDBRunSpec) ([]byte, error) {
	if err := m.EnsureImage(ic.ImageTag); err != nil {
		return nil, err
	}
	spec.Interactive = false
	cmd := podmanCommand(m.podman, influxDBRunArgs(ic.ImageTag, spec)...)
	if len(spec.Env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, spec.Env...)
	}
	return cmd.CombinedOutput()
}

// RunCLI runs the influx client against this instance. Credentials travel via
// the environment (CLIEnv) and are referenced by name on the command line.
func (m *InfluxDBManager) RunCLI(ic *config.InfluxDBConfig, env, args []string, interactive bool) error {
	all := append(influxdb.CLIArgs(*ic), args...)
	return m.Run(ic, InfluxDBRunSpec{
		Entrypoint:  influxDBEntrypointDir + "/influx",
		Env:         env,
		Args:        all,
		Interactive: interactive,
	})
}

// CLIOutput runs the influx client and returns its combined output.
func (m *InfluxDBManager) CLIOutput(ic *config.InfluxDBConfig, env, args []string) ([]byte, error) {
	all := append(influxdb.CLIArgs(*ic), args...)
	return m.Output(ic, InfluxDBRunSpec{
		Entrypoint: influxDBEntrypointDir + "/influx",
		Env:        env,
		Args:       all,
	})
}

// LogsTail prints the last lines of the container log to stderr (error context
// after a failed start).
func (m *InfluxDBManager) LogsTail(name string, lines int) {
	cmd := podmanCommand(m.podman, "logs", "--tail", fmt.Sprint(lines), name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// influxDBRunArgs builds the `podman run` argv for a one-shot helper. Host
// networking lets backup/restore reach the server's loopback-only RPC port.
// --http-proxy=false keeps a host HTTP(S)_PROXY from hijacking loopback
// requests. Pure so tests can pin the flag order without podman.
func influxDBRunArgs(imageTag string, spec InfluxDBRunSpec) []string {
	runArgs := []string{"run", "--rm"}
	if spec.Interactive {
		runArgs = append(runArgs, "-it")
	} else {
		runArgs = append(runArgs, "-i=false")
	}
	runArgs = append(runArgs, "--network", "host", "--http-proxy=false", "--entrypoint", spec.Entrypoint)
	for _, mount := range spec.Mounts {
		runArgs = append(runArgs, "-v", mount)
	}
	for _, kv := range spec.Env {
		if name, _, ok := strings.Cut(kv, "="); ok && name != "" {
			runArgs = append(runArgs, "-e", name)
		}
	}
	runArgs = append(runArgs, imageTag)
	return append(runArgs, spec.Args...)
}

// --- internal helpers ---------------------------------------------------

func (m *InfluxDBManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *InfluxDBManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *InfluxDBManager) imageExists(tag string) (bool, error) {
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

func (m *InfluxDBManager) containerExists(name string) (bool, error) {
	out, err := m.run("ps", "-a", "--format", "{{.Names}}")
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

func (m *InfluxDBManager) containerRunning(name string) (bool, error) {
	out, err := m.run("ps", "--format", "{{.Names}}")
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
