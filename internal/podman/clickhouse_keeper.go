package podman

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/clickhousekeeper"
	"github.com/mars-base/pgcli/internal/config"
)

// ClickHouseKeeperManager manages standalone clickhouse-keeper addon
// containers. Keeper runs in its own container with separate config/data/log
// mounts, even when it is co-located with a ClickHouse server.
type ClickHouseKeeperManager struct {
	cfg    *config.Config
	podman string
}

// NewClickHouseKeeperManager creates a ClickHouseKeeperManager.
func NewClickHouseKeeperManager(cfg *config.Config) (*ClickHouseKeeperManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	ensurePodmanStateReady(path)
	return &ClickHouseKeeperManager{cfg: cfg, podman: path}, nil
}

// EnsureImage pulls the pinned ClickHouse Keeper image when missing.
func (m *ClickHouseKeeperManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling ClickHouse Keeper image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the Keeper container. Data, logs and
// config are never deleted here.
func (m *ClickHouseKeeperManager) EnsureContainer(kc *config.ClickHouseKeeperConfig) error {
	if err := m.ensureFiles(kc); err != nil {
		return err
	}
	running, err := m.containerRunning(kc.ContainerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> ClickHouse Keeper container already running, recreating to apply updated config...")
	}
	exists, err := m.containerExists(kc.ContainerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("rm", "-f", kc.ContainerName); err != nil {
			return fmt.Errorf("removing ClickHouse Keeper container: %w", err)
		}
	}
	if err := m.EnsureImage(kc.ImageTag); err != nil {
		return err
	}
	if err := m.createContainer(kc); err != nil {
		return err
	}
	return m.waitReady(kc)
}

// StartContainer starts without recreating (start/autostart path): running →
// no-op; stopped → podman start; missing → create. It waits for ruok.
// The first return value reports that it was already running.
func (m *ClickHouseKeeperManager) StartContainer(kc *config.ClickHouseKeeperConfig) (bool, error) {
	if err := m.ensureFiles(kc); err != nil {
		return false, err
	}
	running, err := m.containerRunning(kc.ContainerName)
	if err != nil {
		return false, err
	}
	if running {
		return true, nil
	}
	exists, err := m.containerExists(kc.ContainerName)
	if err != nil {
		return false, err
	}
	if exists {
		if _, err := m.run("start", kc.ContainerName); err == nil {
			fmt.Printf("  [OK] ClickHouse Keeper container %s started\n", kc.ContainerName)
			if err := m.waitReady(kc); err != nil {
				return false, err
			}
			return false, nil
		}
		fmt.Printf("  [!] ClickHouse Keeper container %s in improper state, recreating...\n", kc.ContainerName)
		if _, err := m.run("rm", "-f", kc.ContainerName); err != nil {
			return false, fmt.Errorf("removing improper ClickHouse Keeper container: %w", err)
		}
	}
	if err := m.EnsureImage(kc.ImageTag); err != nil {
		return false, err
	}
	if err := m.createContainer(kc); err != nil {
		return false, err
	}
	if err := m.waitReady(kc); err != nil {
		return false, err
	}
	return false, nil
}

func (m *ClickHouseKeeperManager) ensureFiles(kc *config.ClickHouseKeeperConfig) error {
	for _, dir := range []string{kc.DataDir, kc.LogDir, filepath.Dir(kc.ConfigPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return clickhousekeeper.WriteConfig(*kc)
}

func (m *ClickHouseKeeperManager) createContainer(kc *config.ClickHouseKeeperConfig) error {
	if _, err := m.run(clickHouseKeeperCreateArgs(*kc)...); err != nil {
		return fmt.Errorf("creating ClickHouse Keeper container: %w", err)
	}
	fmt.Printf("  [OK] ClickHouse Keeper container %s created\n", kc.ContainerName)
	return nil
}

// clickHouseKeeperCreateArgs builds the `podman run` argv. The entrypoint reads
// KEEPER_CONFIG and creates/chowns the data and log directories named by
// CLICKHOUSE_DATA_DIR and LOG_DIR. The tmpfs hides the image's anonymous
// /var/lib/clickhouse volume; no credentials are involved.
func clickHouseKeeperCreateArgs(kc config.ClickHouseKeeperConfig) []string {
	return []string{
		"run", "-d",
		"--name", kc.ContainerName,
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"--ulimit", "nofile=262144:262144",
		"--tmpfs", "/var/lib/clickhouse:rw",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(kc.DataDir), clickhousekeeper.ContainerDataDir),
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(kc.LogDir), clickhousekeeper.ContainerLogDir),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(filepath.Dir(kc.ConfigPath)), clickhousekeeper.ContainerConfigDir),
		"-e", "KEEPER_CONFIG=" + clickhousekeeper.ContainerConfigPath,
		"-e", "CLICKHOUSE_DATA_DIR=" + clickhousekeeper.ContainerDataDir,
		"-e", "LOG_DIR=" + clickhousekeeper.ContainerLogDir,
		kc.ImageTag,
	}
}

func (m *ClickHouseKeeperManager) waitReady(kc *config.ClickHouseKeeperConfig) error {
	host := clickhousekeeper.LocalProbeHost(kc.Listen)
	address := net.JoinHostPort(host, strconv.Itoa(kc.ClientPort))
	voters, err := config.ClickHouseKeeperVoters(*kc)
	if err != nil {
		return fmt.Errorf("resolving ClickHouse Keeper membership: %w", err)
	}
	// A single member serves clients immediately; a quorum member does not
	// listen on the client port until Raft reaches quorum. Wait briefly and
	// treat a pending quorum as a soft success so members can be installed
	// and started one host at a time.
	clustered := len(voters) > 1
	timeout := 60 * time.Second
	if clustered {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := clickhousekeeper.Ruok(host, kc.ClientPort, 2*time.Second); err == nil {
			break
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if clustered {
				fmt.Printf("  [!] ClickHouse Keeper member %d is running, but the quorum is not ready yet (%v) — install the remaining members with matching --peer lists, then run `pg addon clickhouse-keeper status --name %s`\n", kc.ServerID, lastErr, kc.Name)
				return nil
			}
			return fmt.Errorf("ClickHouse Keeper did not become ready at %s: %w", address, lastErr)
		}
		time.Sleep(250 * time.Millisecond)
	}

	status, err := clickhousekeeper.KeeperClusterStatus(*kc, 2*time.Second)
	if err != nil {
		fmt.Printf("  [!] Could not check Keeper membership: %v\n", err)
	} else if status.ReachableQuorum {
		fmt.Printf("  [OK] Keeper members reachable: %d/%d (quorum %d)\n", status.Reachable, len(status.Members), status.Quorum)
	} else {
		fmt.Printf("  [!] Keeper client ports reachable at %d/%d member(s); quorum is %d. Install the remaining members before ClickHouse.\n", status.Reachable, len(status.Members), status.Quorum)
	}
	if status.LocalState != "" {
		fmt.Printf("  [OK] Local Keeper state: %s\n", status.LocalState)
	}
	return nil
}

// Stop stops the container and reports whether it was running.
func (m *ClickHouseKeeperManager) Stop(name string) (bool, error) {
	running, err := m.containerRunning(name)
	if err != nil || !running {
		return false, err
	}
	if _, err := m.run("stop", "-t", "30", name); err != nil {
		return true, fmt.Errorf("stopping ClickHouse Keeper container: %w", err)
	}
	return true, nil
}

// RemoveContainer stops and removes the container, keeping data unless
// CleanData is called separately.
func (m *ClickHouseKeeperManager) RemoveContainer(kc *config.ClickHouseKeeperConfig) error {
	exists, err := m.containerExists(kc.ContainerName)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("  [OK] ClickHouse Keeper container %s already absent\n", kc.ContainerName)
		return nil
	}
	if _, err := m.run("rm", "-f", kc.ContainerName); err != nil {
		return fmt.Errorf("removing ClickHouse Keeper container: %w", err)
	}
	fmt.Printf("  [OK] ClickHouse Keeper container %s removed\n", kc.ContainerName)
	return nil
}

// CleanData deletes the Keeper instance directory plus any data/log dirs
// configured outside it. A custom config directory outside the instance dir
// is left in place with a hint.
func (m *ClickHouseKeeperManager) CleanData(kc *config.ClickHouseKeeperConfig) error {
	for _, dir := range clickHouseKeeperCleanDirs(*kc) {
		if err := removeHostDir(m.podman, kc.ImageTag, dir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cleaning %s: %w", dir, err)
		}
		fmt.Printf("  [OK] cleaned %s\n", dir)
	}
	configDir := filepath.Dir(kc.ConfigPath)
	if kc.InstanceDir != "" && !pathWithin(configDir, kc.InstanceDir) {
		fmt.Printf("  [!] Kept custom config dir %s; remove it manually if it is no longer needed\n", configDir)
	}
	return nil
}

// clickHouseKeeperCleanDirs returns the managed directories to delete for
// --clean-data: the instance root plus any data/log path outside it.
func clickHouseKeeperCleanDirs(kc config.ClickHouseKeeperConfig) []string {
	instanceDir := kc.InstanceDir
	if instanceDir == "" {
		instanceDir = filepath.Dir(filepath.Dir(kc.ConfigPath))
	}
	seen := map[string]bool{}
	dirs := make([]string, 0, 3)
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	add(instanceDir)
	for _, dir := range []string{kc.DataDir, kc.LogDir} {
		if !pathWithin(dir, instanceDir) {
			add(dir)
		}
	}
	return dirs
}

// ContainerRunning reports whether the Keeper container is running.
func (m *ClickHouseKeeperManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether the Keeper container exists.
func (m *ClickHouseKeeperManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// ExecShell opens a shell in the running Keeper container.
func (m *ClickHouseKeeperManager) ExecShell(kc *config.ClickHouseKeeperConfig) error {
	cmd := podmanCommand(m.podman, "exec", "-it", kc.ContainerName, "bash")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// LogsTail prints the last lines of the container log to stderr.
func (m *ClickHouseKeeperManager) LogsTail(name string, lines int) {
	cmd := podmanCommand(m.podman, "logs", "--tail", fmt.Sprint(lines), name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// --- internal helpers ---------------------------------------------------

func (m *ClickHouseKeeperManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *ClickHouseKeeperManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *ClickHouseKeeperManager) imageExists(tag string) (bool, error) {
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

func (m *ClickHouseKeeperManager) containerExists(name string) (bool, error) {
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

func (m *ClickHouseKeeperManager) containerRunning(name string) (bool, error) {
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
