package podman

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// MinioManager manages standalone single-node MinIO containers: S3-compatible
// object storage as shared infrastructure (top-level addons.minio.<name>),
// the intended use being a pgBackRest repository every host of a Patroni
// cluster can reach. The image is the public pre-built ghcr tag (upstream
// .deb binary on Alpine — MinIO's official image dropped the web console);
// runtime image handling is pull-only, like etcd.
type MinioManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

// NewMinioManager creates a MinioManager. The addon is Linux/amd64-only for
// now: the public image is built from the upstream amd64 .deb, and the macOS
// podman machine does not expose host networking to containers.
func NewMinioManager(cfg *config.Config) (*MinioManager, error) {
	if platform.Detect() == platform.MacOS {
		return nil, fmt.Errorf("the minio addon is not supported on macOS yet: it serves over host networking, which the podman machine does not provide")
	}
	if runtime.GOARCH != "amd64" {
		return nil, fmt.Errorf("the minio addon supports amd64 hosts only: the public image is built from the upstream amd64 binary")
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
	return &MinioManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/minio/<name>/data. It holds the
// object storage itself and outlives the container — `pg addon remove` keeps
// it unless --clean-data.
func (m *MinioManager) resolveDataDir(mc *config.MinioConfig) string {
	if mc.DataDir != "" {
		return mc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "minio", mc.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *MinioManager) DataDir(mc *config.MinioConfig) string {
	return m.resolveDataDir(mc)
}

// EnsureImage pulls the MinIO image if it is not present locally (pull-only:
// the image is published to the public ghcr repo; pgcli never builds it).
func (m *MinioManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling minio image %s (is ghcr.io/mars-base/pgcli reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the MinIO container (install semantics:
// stop/rm/recreate so updated ports/credentials take effect). The data dir is
// created if missing; it is never deleted here.
func (m *MinioManager) EnsureContainer(mc *config.MinioConfig) error {
	containerName := mc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> MinIO container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping MinIO container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing MinIO container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale MinIO container: %w", err)
			}
		}
	}

	if err := m.createContainer(mc); err != nil {
		return err
	}
	fmt.Println("  [OK] MinIO container started")
	return nil
}

// StartContainer starts the MinIO container without touching config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh (the data dir
// and all settings live in the config, unlike haproxy's on-disk cfg file).
func (m *MinioManager) StartContainer(mc *config.MinioConfig) error {
	containerName := mc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] MinIO %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] MinIO %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] MinIO %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper MinIO container: %w", err)
		}
	}

	if err := m.createContainer(mc); err != nil {
		return err
	}
	fmt.Printf("  [OK] MinIO %s started\n", containerName)
	return nil
}

// createContainer runs a single-node MinIO serving /data (the bind-mounted
// data dir) on host networking, bound to mc.Listen:APIPort (S3 API) and
// mc.Listen:ConsolePort (web console). Credentials are passed as env, which
// makes them visible in `podman inspect` — the same exposure as a hand-run
// container with -e, acceptable for a rootless single-host deployment.
func (m *MinioManager) createContainer(mc *config.MinioConfig) error {
	dataDir := m.resolveDataDir(mc)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating MinIO data dir: %w", err)
	}

	// --http-proxy=false: podman would otherwise inject the host's HTTP(S)_PROXY
	// and the process could honor it for outbound connections. Image pulls run
	// client-side and keep the proxy.
	args := []string{
		"run", "-d",
		"--name", mc.ContainerName,
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--ulimit", "nofile=1048576:1048576",
		"--stop-timeout", "60",
		"-v", fmt.Sprintf("%s:/data:z", hostMountPath(dataDir)),
		"-e", "MINIO_ROOT_USER=" + mc.RootUser,
		"-e", "MINIO_ROOT_PASSWORD=" + mc.RootPassword,
		"-e", fmt.Sprintf("MINIO_SERVER_URL=http://%s:%d", mc.Listen, mc.APIPort),
		mc.ImageTag,
		"server", "/data",
		"--address", fmt.Sprintf("%s:%d", mc.Listen, mc.APIPort),
		"--console-address", fmt.Sprintf("%s:%d", mc.Listen, mc.ConsolePort),
	}

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating MinIO container: %w", err)
	}
	return nil
}

// Remove stops and removes the MinIO container. The data dir is kept by
// default (it is the backup repository); cleanData also deletes it.
func (m *MinioManager) Remove(mc *config.MinioConfig, cleanData bool) error {
	containerName := mc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing MinIO container: %w", err)
	}
	fmt.Println("  [OK] MinIO container removed")

	dataDir := m.resolveDataDir(mc)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if err := os.RemoveAll(dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if mc.DataDir == "" {
		// Default layout: also prune the <name> dir when it became empty. With
		// an override the parent belongs to the user — never touch it.
		os.Remove(filepath.Dir(dataDir)) // ignore error — non-empty dir won't be removed
	}

	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *MinioManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// Stop stops a MinIO container.
func (m *MinioManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *MinioManager) run(args ...string) (string, error) {
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

func (m *MinioManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *MinioManager) imageExists(tag string) (bool, error) {
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

func (m *MinioManager) containerExists(name string) (bool, error) {
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

func (m *MinioManager) containerRunning(name string) (bool, error) {
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
