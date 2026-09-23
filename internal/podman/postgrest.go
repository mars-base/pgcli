package podman

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// PostgrestManager manages PostgREST containers. PostgREST is stateless:
// every setting is a PGRST_* env var and there is no data dir or config file,
// so the container is fully described by its config and always recreated
// from it — there is nothing on the host to write or clean up.
type PostgrestManager struct {
	cfg    *config.Config
	podman string // podman binary path
	bridge bool   // macOS: run on the pgcli-net bridge instead of host networking
}

// NewPostgrestManager creates a PostgrestManager.
func NewPostgrestManager(cfg *config.Config) (*PostgrestManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	ensurePodmanStateReady(path)
	return &PostgrestManager{
		cfg:    cfg,
		podman: path,
		bridge: platform.Detect() == platform.MacOS,
	}, nil
}

// postgrestArgsAndEnv builds the full `podman run` argv for a PostgREST
// instance. Pure function (no podman calls) so both networking shapes and
// the env mapping are unit-testable:
//   - PGRST_DB_URI receives the DSN verbatim — it is never re-serialized,
//     so sslmode/options and every other URI parameter survive intact.
//   - PGRST_DB_POOL / PGRST_DB_SCHEMAS are only emitted when set; unset
//     defers to PostgREST's own defaults (10 / public).
//   - The server always binds pc.HostPort (on macOS the bridge publishes
//     the same port via `-p P:P`); the bind address goes through
//     proxyBindHost so loopback is widened to 0.0.0.0 under bridge.
func postgrestArgsAndEnv(pc *config.PostgrestConfig, bridge bool, network string) []string {
	args := []string{"run", "-d", "--name", pc.ContainerName}
	args = append(args, netFlags(bridge, network, pc.HostPort)...)
	args = append(args,
		"--restart", "unless-stopped",
		"-e", "PGRST_DB_URI="+pc.DSN,
		"-e", "PGRST_SERVER_HOST="+proxyBindHost(bridge, pc.Listen),
		"-e", "PGRST_SERVER_PORT="+strconv.Itoa(pc.HostPort),
	)
	if pc.DbPool > 0 {
		args = append(args, "-e", "PGRST_DB_POOL="+strconv.Itoa(pc.DbPool))
	}
	if pc.Schemas != "" {
		args = append(args, "-e", "PGRST_DB_SCHEMAS="+pc.Schemas)
	}
	if pc.AnonRole != "" {
		args = append(args, "-e", "PGRST_DB_ANON_ROLE="+pc.AnonRole)
	}
	if pc.JwtSecret != "" {
		args = append(args, "-e", "PGRST_JWT_SECRET="+pc.JwtSecret)
	}
	args = append(args, pc.ImageTag)
	return args
}

// EnsureImage pulls the PostgREST image if it is not present locally
// (pull-only: the image is the public official tag; pgcli never builds it).
func (m *PostgrestManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling postgrest image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the PostgREST container (install
// semantics: stop/rm/recreate so updated env/ports take effect).
func (m *PostgrestManager) EnsureContainer(pc *config.PostgrestConfig) error {
	containerName := pc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> PostgREST container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping PostgREST container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing PostgREST container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale PostgREST container: %w", err)
			}
		}
	}

	if err := m.createContainer(pc); err != nil {
		return err
	}
	fmt.Println("  [OK] PostgREST container started")
	return nil
}

// StartContainer starts the PostgREST container (autostart-on-boot path):
// running → no-op; exists but stopped → podman start, recreating on improper
// state; missing → create fresh (the config holds everything — PostgREST is
// stateless, unlike pgbouncer's on-disk ini/userlist).
func (m *PostgrestManager) StartContainer(pc *config.PostgrestConfig) error {
	containerName := pc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] PostgREST %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] PostgREST %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] PostgREST %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper PostgREST container: %w", err)
		}
	}

	if err := m.createContainer(pc); err != nil {
		return err
	}
	fmt.Printf("  [OK] PostgREST %s started\n", containerName)
	return nil
}

func (m *PostgrestManager) createContainer(pc *config.PostgrestConfig) error {
	if _, err := m.run(postgrestArgsAndEnv(pc, m.bridge, m.cfg.Podman.Network)...); err != nil {
		return fmt.Errorf("creating PostgREST container: %w", err)
	}
	return nil
}

// Remove stops and removes the PostgREST container. There is no host-side
// data or config directory to clean up.
func (m *PostgrestManager) Remove(pc *config.PostgrestConfig) error {
	containerName := pc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing PostgREST container: %w", err)
	}
	fmt.Println("  [OK] PostgREST container removed")
	return nil
}

// ContainerRunning reports whether the named container is currently running.
// Exported so the CLI can display status.
func (m *PostgrestManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not). Exported so install can reuse an existing container
// instead of always recreating it (MinIO's idempotency contract).
func (m *PostgrestManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a PostgREST container.
func (m *PostgrestManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ------------------------------------------------

func (m *PostgrestManager) run(args ...string) (string, error) {
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

func (m *PostgrestManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *PostgrestManager) imageExists(tag string) (bool, error) {
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

func (m *PostgrestManager) containerExists(name string) (bool, error) {
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

func (m *PostgrestManager) containerRunning(name string) (bool, error) {
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
