package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/kafka"
)

// kafkaBinDir holds the CLI tools in the official image; it is not on PATH.
const kafkaBinDir = "/opt/kafka/bin"

// KafkaManager manages standalone and clustered Kafka nodes. The official
// apache/kafka image runs as uid 1000 (appuser), so the bind-mounted data dir
// is chowned once via `podman unshare` before the first boot formats it.
type KafkaManager struct {
	cfg     *config.Config
	podman  string
	dataDir string
}

// NewKafkaManager creates a KafkaManager.
func NewKafkaManager(cfg *config.Config) (*KafkaManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := ""
	if cfg != nil {
		dataDir = cfg.BaseDir
	}
	ensurePodmanStateReady(path)
	return &KafkaManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// EnsureImage pulls the pinned upstream Kafka image when missing.
func (m *KafkaManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling Kafka image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer recreates the container (install/config-change semantics).
// Data and config are never deleted here.
func (m *KafkaManager) EnsureContainer(k *config.KafkaConfig) error {
	if err := m.ensureFiles(k); err != nil {
		return err
	}
	running, err := m.containerRunning(k.ContainerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> Kafka container already running, recreating to apply updated config...")
	}
	exists, err := m.containerExists(k.ContainerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("rm", "-f", k.ContainerName); err != nil {
			return fmt.Errorf("removing Kafka container: %w", err)
		}
	}
	if err := m.EnsureImage(k.ImageTag); err != nil {
		return err
	}
	if err := m.createContainer(k); err != nil {
		return err
	}
	return m.waitReady(k)
}

// StartContainer starts without recreating (start/autostart path): running is
// a no-op; stopped is started; missing is created. The first return value
// reports that it was already running.
func (m *KafkaManager) StartContainer(k *config.KafkaConfig) (bool, error) {
	if err := m.ensureFiles(k); err != nil {
		return false, err
	}
	running, err := m.containerRunning(k.ContainerName)
	if err != nil {
		return false, err
	}
	if running {
		return true, nil
	}
	exists, err := m.containerExists(k.ContainerName)
	if err != nil {
		return false, err
	}
	if exists {
		if _, err := m.run("start", k.ContainerName); err == nil {
			fmt.Printf("  [OK] Kafka container %s started\n", k.ContainerName)
			if err := m.waitReady(k); err != nil {
				return false, err
			}
			return false, nil
		}
		fmt.Printf("  [!] Kafka container %s in improper state, recreating...\n", k.ContainerName)
		if _, err := m.run("rm", "-f", k.ContainerName); err != nil {
			return false, fmt.Errorf("removing improper Kafka container: %w", err)
		}
	}
	if err := m.EnsureImage(k.ImageTag); err != nil {
		return false, err
	}
	if err := m.createContainer(k); err != nil {
		return false, err
	}
	if err := m.waitReady(k); err != nil {
		return false, err
	}
	return false, nil
}

func (m *KafkaManager) ensureFiles(k *config.KafkaConfig) error {
	if err := kafka.CheckMetaData(*k); err != nil {
		return err
	}
	if err := os.MkdirAll(k.DataDir, 0o755); err != nil {
		return fmt.Errorf("creating data dir %s: %w", k.DataDir, err)
	}
	if err := m.ensureDataDirOwner(k); err != nil {
		return err
	}
	if k.TLS {
		if _, err := kafka.EnsureTLSCerts(*k); err != nil {
			return err
		}
		if err := m.ensureTLSFileOwner(*k); err != nil {
			return err
		}
	}
	return kafka.WriteProperties(*k)
}

// ensureTLSFileOwner hands the combined key+cert PEM to the image's appuser.
// Only that file is chowned: it is the one the container reads, while the raw
// private key and CA stay host-owned. The TLS dir remains traversable so the
// read-only mount works for uid 1000.
func (m *KafkaManager) ensureTLSFileOwner(k config.KafkaConfig) error {
	path := filepath.Join(kafka.TLSDir(k), kafka.ServerPEMFile)
	if err := podmanUnshareChown(m.podman, kafka.ContainerUID, kafka.ContainerGID, path); err == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(path, kafka.ContainerUID, kafka.ContainerGID); err == nil {
			return nil
		}
	}
	return fmt.Errorf("chowning Kafka TLS key %s to %d:%d (the image's appuser): rootless podman does this through `podman unshare`; on a rootful host run pgcli as root, or pre-chown the file manually",
		path, kafka.ContainerUID, kafka.ContainerGID)
}

// ensureDataDirOwner hands a fresh data dir to the image's appuser. The
// recursive chown only runs before the first format: once meta.properties
// exists the storage is already owned by uid 1000 and a re-chown of a large
// log tree would be pure cost.
func (m *KafkaManager) ensureDataDirOwner(k *config.KafkaConfig) error {
	meta, err := kafka.ReadMetaProperties(k.DataDir)
	if err != nil {
		return err
	}
	if meta != nil {
		return nil
	}
	dir := hostMountPath(k.DataDir)
	if err := podmanUnshareRun(m.podman, "chown", "-R",
		fmt.Sprintf("%d:%d", kafka.ContainerUID, kafka.ContainerGID), dir); err == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		if chownErr := chownTree(kafka.ContainerUID, kafka.ContainerGID, dir); chownErr == nil {
			return nil
		}
	}
	return fmt.Errorf("chowning Kafka data dir %s to %d:%d (the image's appuser): rootless podman does this through `podman unshare`; on a rootful host run pgcli as root, or pre-chown the directory manually",
		k.DataDir, kafka.ContainerUID, kafka.ContainerGID)
}

func (m *KafkaManager) createContainer(k *config.KafkaConfig) error {
	if _, err := m.run(kafkaCreateArgs(*k)...); err != nil {
		return fmt.Errorf("creating Kafka container: %w", err)
	}
	fmt.Printf("  [OK] Kafka container %s created\n", k.ContainerName)
	return nil
}

// kafkaCreateArgs builds the `podman run` argv. Pure so tests can pin the
// mounts, cluster id and host networking without podman.
func kafkaCreateArgs(k config.KafkaConfig) []string {
	return []string{
		"run", "-d",
		"--name", k.ContainerName,
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(k.DataDir), kafka.ContainerDataDir),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(filepath.Dir(k.ConfigPath)), kafka.ContainerConfigDir),
		"-e", "CLUSTER_ID=" + k.ClusterID,
		k.ImageTag,
	}
}

// waitReady runs the broker API check until it succeeds. A single node must
// become ready (fatal on timeout); a cluster node may legitimately be waiting
// for the static voter majority, so a short timeout only warns.
func (m *KafkaManager) waitReady(k *config.KafkaConfig) error {
	timeout := 90 * time.Second
	if k.Clustered() {
		timeout = 15 * time.Second
	}
	endpoint := kafka.LocalBrokerEndpoint(*k)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		out, err := execIdleKilled(m.podman, 10*time.Second, defaultIdleKill,
			"exec", k.ContainerName, kafkaBinDir+"/kafka-broker-api-versions.sh", "--bootstrap-server", endpoint)
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if k.Clustered() {
				fmt.Printf("  [!] Kafka node %d is running but the controller quorum is not ready yet (%v) — install the remaining peers, then check `pg addon kafka cluster status`\n", k.NodeID, lastErr)
				return nil
			}
			detail := strings.TrimSpace(out)
			if detail != "" {
				return fmt.Errorf("Kafka did not become ready at %s: %w (%s)", endpoint, lastErr, detail)
			}
			return fmt.Errorf("Kafka did not become ready at %s: %w", endpoint, lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Stop stops the container and reports whether it was running.
func (m *KafkaManager) Stop(name string) (bool, error) {
	running, err := m.containerRunning(name)
	if err != nil || !running {
		return false, err
	}
	if _, err := m.run("stop", "-t", "30", name); err != nil {
		return true, fmt.Errorf("stopping Kafka container: %w", err)
	}
	return true, nil
}

// RemoveContainer stops and removes the container, keeping data unless
// CleanData is called separately.
func (m *KafkaManager) RemoveContainer(k *config.KafkaConfig) error {
	exists, err := m.containerExists(k.ContainerName)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("  [OK] Kafka container %s already absent\n", k.ContainerName)
		return nil
	}
	if _, err := m.run("rm", "-f", k.ContainerName); err != nil {
		return fmt.Errorf("removing Kafka container: %w", err)
	}
	fmt.Printf("  [OK] Kafka container %s removed\n", k.ContainerName)
	return nil
}

// CleanData deletes the instance directory (config + default data dir) plus a
// distinct custom data dir. Files owned by the image's uid are removed through
// the existing podman-unshare path.
func (m *KafkaManager) CleanData(k *config.KafkaConfig) error {
	instanceDir := filepath.Dir(filepath.Dir(k.ConfigPath))
	dirs := []string{instanceDir}
	if k.DataDir != "" && !pathWithin(k.DataDir, instanceDir) {
		dirs = append(dirs, k.DataDir)
	}
	for _, dir := range dirs {
		if err := removeHostDir(m.podman, k.ImageTag, dir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cleaning %s: %w", dir, err)
		}
		fmt.Printf("  [OK] cleaned %s\n", dir)
	}
	return nil
}

// ContainerRunning reports whether the instance container is running.
func (m *KafkaManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether the instance container exists.
func (m *KafkaManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Exec runs a command inside the container, sharing the host's terminal when
// interactive. With no args it opens bash.
func (m *KafkaManager) Exec(k *config.KafkaConfig, interactive bool, args ...string) error {
	if len(args) == 0 {
		args = []string{"/bin/bash"}
	}
	execArgs := []string{"exec"}
	if interactive {
		execArgs = append(execArgs, "-it")
	}
	execArgs = append(execArgs, k.ContainerName)
	execArgs = append(execArgs, args...)
	return m.runInteractive(execArgs...)
}

// ExecOutput runs a command inside the container and captures its output.
func (m *KafkaManager) ExecOutput(k *config.KafkaConfig, args ...string) (string, error) {
	execArgs := []string{"exec", k.ContainerName}
	execArgs = append(execArgs, args...)
	return execIdleKilled(m.podman, 30*time.Second, defaultIdleKill, execArgs...)
}

// Topic runs kafka-topics.sh against this node's local broker endpoint.
func (m *KafkaManager) Topic(k *config.KafkaConfig, args ...string) (string, error) {
	execArgs := []string{
		"exec", k.ContainerName,
		kafkaBinDir + "/kafka-topics.sh",
		"--bootstrap-server", kafka.LocalBrokerEndpoint(*k),
	}
	execArgs = append(execArgs, args...)
	return execIdleKilled(m.podman, 30*time.Second, defaultIdleKill, execArgs...)
}

// ClusterStatus describes the local metadata quorum.
func (m *KafkaManager) ClusterStatus(k *config.KafkaConfig) (string, error) {
	return execIdleKilled(m.podman, 30*time.Second, defaultIdleKill,
		"exec", k.ContainerName,
		kafkaBinDir+"/kafka-metadata-quorum.sh",
		"--bootstrap-controller", kafka.LocalControllerEndpoint(*k),
		"describe", "--status")
}

// LogsTail prints the last lines of the container log to stderr.
func (m *KafkaManager) LogsTail(name string, lines int) {
	cmd := podmanCommand(m.podman, "logs", "--tail", fmt.Sprint(lines), name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// --- internal helpers ---------------------------------------------------

func (m *KafkaManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *KafkaManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *KafkaManager) imageExists(tag string) (bool, error) {
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

func (m *KafkaManager) containerExists(name string) (bool, error) {
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

func (m *KafkaManager) containerRunning(name string) (bool, error) {
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
