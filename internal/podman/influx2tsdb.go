package podman

import (
	"fmt"
	"os"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/influx2tsdb"
)

// Influx2TSDBManager manages influx2tsdb-proxy addon containers. The image is
// the pgcli-built pgcli-influx2tsdb (the upstream linux/amd64 static binary on
// Alpine). The proxy is stateless — PostgreSQL holds the data — so the
// container is recreated on every start; that also keeps a locally derived
// DSN in sync with the target instance's current port and password.
//
// The container uses host networking: the proxy listens on Port on all
// interfaces and reaches a local target at 127.0.0.1:<instance host port>.
// The upstream API has no authentication, so Port must be restricted at the
// network boundary or fronted with nginx.
type Influx2TSDBManager struct {
	podman string // podman binary path
}

// NewInflux2TSDBManager creates an Influx2TSDBManager.
func NewInflux2TSDBManager(cfg *config.Config) (*Influx2TSDBManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	ensurePodmanStateReady(path)
	return &Influx2TSDBManager{podman: path}, nil
}

// EnsureImage pulls the proxy image if it is not present locally.
func (m *Influx2TSDBManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling influx2tsdb image %s (is ghcr.io/mars-base/pgcli reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer (re)creates the container so changed target settings take
// effect, then waits for /ping.
func (m *Influx2TSDBManager) EnsureContainer(ic *config.Influx2TSDBConfig, dsn string) error {
	if err := m.removeExisting(ic.ContainerName); err != nil {
		return err
	}
	if err := m.createContainer(ic, dsn); err != nil {
		return err
	}
	return m.waitReady(ic)
}

// StartContainer starts the proxy without touching config. Because the addon
// is stateless, an existing stopped container is removed and recreated with
// the current DSN rather than started. The first return value reports that it
// was already running.
func (m *Influx2TSDBManager) StartContainer(ic *config.Influx2TSDBConfig, dsn string) (bool, error) {
	running, err := m.containerRunning(ic.ContainerName)
	if err != nil {
		return false, err
	}
	if running {
		return true, nil
	}
	if err := m.removeExisting(ic.ContainerName); err != nil {
		return false, err
	}
	if err := m.createContainer(ic, dsn); err != nil {
		return false, err
	}
	if err := m.waitReady(ic); err != nil {
		return false, err
	}
	return false, nil
}

func (m *Influx2TSDBManager) removeExisting(name string) error {
	exists, err := m.containerExists(name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := m.run("rm", "-f", name); err != nil {
		return fmt.Errorf("removing influx2tsdb container: %w", err)
	}
	return nil
}

func (m *Influx2TSDBManager) createContainer(ic *config.Influx2TSDBConfig, dsn string) error {
	args := influx2TSDBCreateArgs(*ic, dsn)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating influx2tsdb container: %w", err)
	}
	fmt.Printf("  [OK] influx2tsdb container %s created\n", ic.ContainerName)
	return nil
}

// influx2TSDBCreateArgs builds the `podman run` argv. Pure so tests can pin the
// host-network and proxy flags without podman.
func influx2TSDBCreateArgs(ic config.Influx2TSDBConfig, dsn string) []string {
	args := []string{
		"run", "-d",
		"--name", ic.ContainerName,
		// Host networking: -port binds the host port directly and a local
		// target is reachable at 127.0.0.1:<instance host port>.
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		ic.ImageTag,
	}
	return append(args, influx2tsdb.Args(ic, dsn)...)
}

func (m *Influx2TSDBManager) waitReady(ic *config.Influx2TSDBConfig) error {
	if err := influx2tsdb.WaitForPing(ic.Port, influx2tsdb.StartTimeout); err != nil {
		return fmt.Errorf("influx2tsdb did not answer on http://127.0.0.1:%d/ping: %w", ic.Port, err)
	}
	return nil
}

// Stop stops the container and reports whether it was running.
func (m *Influx2TSDBManager) Stop(name string) (bool, error) {
	running, err := m.containerRunning(name)
	if err != nil || !running {
		return false, err
	}
	if _, err := m.run("stop", "-t", "30", name); err != nil {
		return true, fmt.Errorf("stopping influx2tsdb container: %w", err)
	}
	return true, nil
}

// RemoveContainer removes the container; the addon keeps no local data.
func (m *Influx2TSDBManager) RemoveContainer(ic *config.Influx2TSDBConfig) error {
	exists, err := m.containerExists(ic.ContainerName)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("  [OK] influx2tsdb container %s already absent\n", ic.ContainerName)
		return nil
	}
	if _, err := m.run("rm", "-f", ic.ContainerName); err != nil {
		return fmt.Errorf("removing influx2tsdb container: %w", err)
	}
	fmt.Printf("  [OK] influx2tsdb container %s removed\n", ic.ContainerName)
	return nil
}

// ContainerRunning reports whether the proxy container is running.
func (m *Influx2TSDBManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether the proxy container exists.
func (m *Influx2TSDBManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// LogsTail prints the last lines of the container log to stderr.
func (m *Influx2TSDBManager) LogsTail(name string, lines int) {
	cmd := podmanCommand(m.podman, "logs", "--tail", fmt.Sprint(lines), name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// --- internal helpers ---------------------------------------------------

func (m *Influx2TSDBManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *Influx2TSDBManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *Influx2TSDBManager) imageExists(tag string) (bool, error) {
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

func (m *Influx2TSDBManager) containerExists(name string) (bool, error) {
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

func (m *Influx2TSDBManager) containerRunning(name string) (bool, error) {
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
