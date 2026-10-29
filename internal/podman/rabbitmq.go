package podman

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/rabbitmq"
)

// RabbitMQManager manages single-node RabbitMQ containers. The official image
// runs as uid 999 (rabbitmq); its entrypoint chowns the bind-mounted data dir
// on boot, so only the TLS private key is chowned here.
type RabbitMQManager struct {
	cfg     *config.Config
	podman  string
	dataDir string
}

// NewRabbitMQManager creates a RabbitMQManager.
func NewRabbitMQManager(cfg *config.Config) (*RabbitMQManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := ""
	if cfg != nil {
		dataDir = cfg.BaseDir
	}
	ensurePodmanStateReady(path)
	return &RabbitMQManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// EnsureImage pulls the pinned upstream RabbitMQ image when missing.
func (m *RabbitMQManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling RabbitMQ image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer recreates the container (install/config-change semantics).
// Data and config are never deleted here.
func (m *RabbitMQManager) EnsureContainer(k *config.RabbitMQConfig) error {
	if err := m.ensureFiles(k); err != nil {
		return err
	}
	running, err := m.containerRunning(k.ContainerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> RabbitMQ container already running, recreating to apply updated config...")
	}
	exists, err := m.containerExists(k.ContainerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("rm", "-f", k.ContainerName); err != nil {
			return fmt.Errorf("removing RabbitMQ container: %w", err)
		}
	}
	if err := m.EnsureImage(k.ImageTag); err != nil {
		return err
	}
	if err := m.createContainer(k); err != nil {
		return err
	}
	if err := m.waitReady(k); err != nil {
		return err
	}
	return m.EnsureAdmin(k)
}

// StartContainer starts without recreating (start/autostart path): running is
// a no-op; stopped is started; missing is created. The first return value
// reports that it was already running.
func (m *RabbitMQManager) StartContainer(k *config.RabbitMQConfig) (bool, error) {
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
			running, checkErr := m.containerRunning(k.ContainerName)
			if checkErr != nil {
				return false, fmt.Errorf("verifying RabbitMQ container %s after start: %w", k.ContainerName, checkErr)
			}
			// `podman start` exits 0 on podman's stale "running" record even
			// though nothing was started, so re-check the process liveness.
			if running {
				fmt.Printf("  [OK] RabbitMQ container %s started\n", k.ContainerName)
				if err := m.waitReady(k); err != nil {
					return false, err
				}
				return false, m.EnsureAdmin(k)
			}
		}
		fmt.Printf("  [!] RabbitMQ container %s in improper state, recreating...\n", k.ContainerName)
		if _, err := m.run("rm", "-f", k.ContainerName); err != nil {
			return false, fmt.Errorf("removing improper RabbitMQ container: %w", err)
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
	return false, m.EnsureAdmin(k)
}

func (m *RabbitMQManager) ensureFiles(k *config.RabbitMQConfig) error {
	if err := os.MkdirAll(k.DataDir, 0o755); err != nil {
		return fmt.Errorf("creating data dir %s: %w", k.DataDir, err)
	}
	if k.TLS {
		if rabbitmq.BYOTLS(*k) {
			ci, err := ValidateBYOCert(k.CertFile, k.KeyFile)
			if err != nil {
				return fmt.Errorf("bring-your-own TLS certificate: %w", err)
			}
			if ip := net.ParseIP(k.Listen); ip != nil && !ip.IsUnspecified() && !CertCoversHost(ci, k.Listen) {
				return fmt.Errorf("TLS certificate %s does not cover the listen address %s; add it as a SAN or use --listen 0.0.0.0", k.CertFile, k.Listen)
			}
		}
		if err := rabbitmq.EnsureTLSCerts(*k); err != nil {
			return err
		}
		if err := m.ensureTLSKeyOwner(*k); err != nil {
			return err
		}
	}
	if err := rabbitmq.WritePlugins(*k); err != nil {
		return err
	}
	if err := rabbitmq.WriteConfig(*k); err != nil {
		return err
	}
	if k.Cluster == "" {
		return nil
	}
	if err := rabbitmq.WriteErlangCookie(*k); err != nil {
		return err
	}
	return m.ensureRabbitMQFileOwner(rabbitmq.CookiePath(*k), "Erlang cookie")
}

// ensureTLSKeyOwner hands the private key to the image's rabbitmq user, which
// is the only file in the read-only TLS mount that needs it.
func (m *RabbitMQManager) ensureTLSKeyOwner(k config.RabbitMQConfig) error {
	return m.ensureRabbitMQFileOwner(filepath.Join(rabbitmq.TLSDir(k), "private.key"), "TLS key")
}

// ensureRabbitMQFileOwner chowns a host file to the image's rabbitmq user so
// the unprivileged server process can read it. Rootless podman cannot chown to
// a container uid directly; `podman unshare` maps it into the user namespace.
func (m *RabbitMQManager) ensureRabbitMQFileOwner(path, what string) error {
	if err := podmanUnshareChown(m.podman, rabbitmq.ContainerUID, rabbitmq.ContainerGID, path); err == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(path, rabbitmq.ContainerUID, rabbitmq.ContainerGID); err == nil {
			return nil
		}
	}
	return fmt.Errorf("chowning RabbitMQ %s %s to %d:%d (the image's rabbitmq user): rootless podman does this through `podman unshare`; on a rootful host run pgcli as root, or pre-chown the file manually",
		what, path, rabbitmq.ContainerUID, rabbitmq.ContainerGID)
}

func (m *RabbitMQManager) createContainer(k *config.RabbitMQConfig) error {
	if _, err := m.run(rabbitmqCreateArgs(*k)...); err != nil {
		return fmt.Errorf("creating RabbitMQ container: %w", err)
	}
	fmt.Printf("  [OK] RabbitMQ container %s created\n", k.ContainerName)
	return nil
}

// rabbitmqCreateArgs builds the `podman run` argv. Pure so tests can pin the
// mounts and environment without podman.
func rabbitmqCreateArgs(k config.RabbitMQConfig) []string {
	args := []string{
		"run", "-d",
		"--name", k.ContainerName,
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(k.DataDir), rabbitmq.ContainerDataDir),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(k.ConfigPath), rabbitmq.ContainerConfigFile),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(rabbitmq.PluginsFile(k)), rabbitmq.ContainerPluginsFile),
	}
	if k.TLS {
		args = append(args, "-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(rabbitmq.TLSDir(k)), rabbitmq.ContainerTLSDir))
	}
	if k.Cluster != "" {
		// The peer-visible identity is the Erlang node name. Long names are
		// required for IPs/FQDNs; short hostnames must stay short or the
		// distribution handshake uses a different name than the registered one.
		longName := true
		if _, host, err := config.ParseRabbitMQNodeName(k.NodeName); err == nil {
			longName = config.RabbitMQNodeUsesLongName(host)
		}
		args = append(args,
			"-e", "RABBITMQ_NODENAME="+k.NodeName,
			"-e", "RABBITMQ_USE_LONGNAME="+strconv.FormatBool(longName),
			"-e", "ERL_EPMD_ADDRESS="+k.Listen,
			"-e", fmt.Sprintf("ERL_EPMD_PORT=%d", k.EPMDPort),
			"-e", fmt.Sprintf("RABBITMQ_DIST_PORT=%d", k.DistPort),
			"-e", "RABBITMQ_DEFAULT_USER="+k.AdminUser,
			"-e", "RABBITMQ_DEFAULT_PASS="+k.AdminPassword,
			k.ImageTag,
		)
		return args
	}
	args = append(args,
		"-e", "RABBITMQ_NODENAME=rabbit@localhost",
		"-e", "ERL_EPMD_ADDRESS=127.0.0.1",
		"-e", fmt.Sprintf("ERL_EPMD_PORT=%d", k.EPMDPort),
		"-e", fmt.Sprintf("RABBITMQ_DIST_PORT=%d", k.DistPort),
		"-e", "RABBITMQ_DEFAULT_USER="+k.AdminUser,
		"-e", "RABBITMQ_DEFAULT_PASS="+k.AdminPassword,
		k.ImageTag,
	)
	return args
}

// waitReady polls the broker diagnostics until the node answers. A cluster
// member additionally waits for the peer discovery step to finish. await_startup
// itself fails fast while the node is still registering with EPMD, so it is
// retried until the overall deadline.
func (m *RabbitMQManager) waitReady(k *config.RabbitMQConfig) error {
	if k.Cluster != "" {
		deadline := time.Now().Add(180 * time.Second)
		var lastErr error
		var lastOut string
		for {
			attempt := time.Until(deadline)
			if attempt <= 0 {
				break
			}
			if attempt > 10*time.Second {
				attempt = 10 * time.Second
			}
			out, err := execIdleKilled(m.podman, attempt, defaultIdleKill,
				"exec", k.ContainerName, "rabbitmqctl", "-q", "await_startup")
			if err == nil {
				return nil
			}
			// With peers present, a peer can dial back into the CLI's ephemeral
			// RPC endpoint; await_startup then succeeds silently but the CLI
			// never exits, so the idle watchdog reports ErrCLIHung. The AMQP
			// listener opens at the end of boot, so an open port means ready.
			if errors.Is(err, ErrCLIHung) && rabbitMQAMQPPortOpen(k) {
				return nil
			}
			lastErr, lastOut = err, out
			time.Sleep(500 * time.Millisecond)
		}
		detail := strings.TrimSpace(lastOut)
		if detail != "" {
			return fmt.Errorf("RabbitMQ cluster node %s did not start (container %s): %w (%s)", k.NodeName, k.ContainerName, lastErr, detail)
		}
		return fmt.Errorf("RabbitMQ cluster node %s did not start (container %s): %w", k.NodeName, k.ContainerName, lastErr)
	}
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	var lastOut string
	for {
		attempt := time.Until(deadline)
		if attempt <= 0 {
			break
		}
		if attempt > 30*time.Second {
			attempt = 30 * time.Second
		}
		// ping answers as soon as the Erlang node is up, which can be before
		// the rabbit app starts; await_startup waits for the app itself.
		out, err := execIdleKilled(m.podman, attempt, defaultIdleKill,
			"exec", k.ContainerName, "rabbitmqctl", "-q", "await_startup")
		if err == nil {
			return nil
		}
		lastErr, lastOut = err, out
		time.Sleep(500 * time.Millisecond)
	}
	detail := strings.TrimSpace(lastOut)
	if detail != "" {
		return fmt.Errorf("RabbitMQ did not become ready (container %s): %w (%s)", k.ContainerName, lastErr, detail)
	}
	return fmt.Errorf("RabbitMQ did not become ready (container %s): %w", k.ContainerName, lastErr)
}

// rabbitMQAMQPPortOpen reports whether the broker's AMQP listener accepts
// connections, which happens at the end of the boot sequence.
func rabbitMQAMQPPortOpen(k *config.RabbitMQConfig) bool {
	conn, err := net.DialTimeout("tcp", rabbitmq.AMQPAddr(*k), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// EnsureAdmin reconciles the stored administrator credentials with the running
// node. RABBITMQ_DEFAULT_* only seeds an empty mnesia, so an existing data dir
// needs an explicit password/tags/permissions update after every start.
func (m *RabbitMQManager) EnsureAdmin(k *config.RabbitMQConfig) error {
	// No -q: with cluster peers the rabbitmqctl CLI is known to linger after a
	// successful RPC, and execIdleKilled only ends that wedge once output has
	// been seen. Silent success would otherwise burn the hard cap and report a
	// false failure.
	ctl := func(args ...string) (string, error) {
		execArgs := []string{"exec", k.ContainerName, "rabbitmqctl"}
		execArgs = append(execArgs, args...)
		return execIdleKilled(m.podman, 120*time.Second, defaultIdleKill, execArgs...)
	}
	if out, err := ctl("change_password", k.AdminUser, k.AdminPassword); err != nil {
		if out2, err2 := ctl("add_user", k.AdminUser, k.AdminPassword); err2 != nil {
			return fmt.Errorf("ensuring RabbitMQ admin user %s: %w (%s; %s)", k.AdminUser, err2, strings.TrimSpace(out), strings.TrimSpace(out2))
		}
	}
	if out, err := ctl("set_user_tags", k.AdminUser, "administrator"); err != nil {
		return fmt.Errorf("setting RabbitMQ admin tags: %w (%s)", err, strings.TrimSpace(out))
	}
	if out, err := ctl("set_permissions", "-p", "/", k.AdminUser, ".*", ".*", ".*"); err != nil {
		return fmt.Errorf("setting RabbitMQ admin permissions: %w (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// Stop stops the container and reports whether it was running.
func (m *RabbitMQManager) Stop(name string) (bool, error) {
	running, err := m.containerRunning(name)
	if err != nil || !running {
		return false, err
	}
	if _, err := m.run("stop", "-t", "30", name); err != nil {
		return true, fmt.Errorf("stopping RabbitMQ container: %w", err)
	}
	return true, nil
}

// RemoveContainer stops and removes the container, keeping data unless
// CleanData is called separately.
func (m *RabbitMQManager) RemoveContainer(k *config.RabbitMQConfig) error {
	exists, err := m.containerExists(k.ContainerName)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("  [OK] RabbitMQ container %s already absent\n", k.ContainerName)
		return nil
	}
	if _, err := m.run("rm", "-f", k.ContainerName); err != nil {
		return fmt.Errorf("removing RabbitMQ container: %w", err)
	}
	fmt.Printf("  [OK] RabbitMQ container %s removed\n", k.ContainerName)
	return nil
}

// CleanData deletes the instance directory (config + plugins + tls + default
// data dir) plus a distinct custom data dir.
func (m *RabbitMQManager) CleanData(k *config.RabbitMQConfig) error {
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
func (m *RabbitMQManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether the instance container exists.
func (m *RabbitMQManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

func rabbitMQExecArgs(container string, interactive, tty bool, args ...string) []string {
	execArgs := []string{"exec"}
	if interactive {
		if tty {
			execArgs = append(execArgs, "-it")
		} else {
			execArgs = append(execArgs, "-i")
		}
	}
	execArgs = append(execArgs, container)
	return append(execArgs, args...)
}

// Exec runs a command inside the container, sharing the host's terminal when
// interactive. With no args it opens bash.
func (m *RabbitMQManager) Exec(k *config.RabbitMQConfig, interactive bool, args ...string) error {
	if len(args) == 0 {
		args = []string{"/bin/bash"}
	}
	return m.runInteractive(rabbitMQExecArgs(k.ContainerName, interactive, isTerminal(os.Stdin), args...)...)
}

// ExecOutput runs a command inside the container and captures its output.
func (m *RabbitMQManager) ExecOutput(k *config.RabbitMQConfig, args ...string) (string, error) {
	execArgs := []string{"exec", k.ContainerName}
	execArgs = append(execArgs, args...)
	return execIdleKilled(m.podman, 30*time.Second, defaultIdleKill, execArgs...)
}

// QueueCtlArgs builds the rabbitmqctl argv for a queue command on a vhost.
// Pure so tests can pin the option order.
func QueueCtlArgs(vhost, command string, args ...string) []string {
	out := []string{"-q", "--vhost", vhost, command}
	return append(out, args...)
}

// Queue runs a rabbitmqctl queue subcommand against this node.
func (m *RabbitMQManager) Queue(k *config.RabbitMQConfig, vhost string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("queue command is required")
	}
	execArgs := []string{"exec", k.ContainerName, "rabbitmqctl"}
	execArgs = append(execArgs, QueueCtlArgs(vhost, args[0], args[1:]...)...)
	return execIdleKilled(m.podman, 30*time.Second, defaultIdleKill, execArgs...)
}

// ClusterStatus returns `rabbitmqctl cluster_status` output: running and
// stopped nodes, partitions and the listener endpoints.
func (m *RabbitMQManager) ClusterStatus(k *config.RabbitMQConfig) (string, error) {
	return m.ExecOutput(k, "rabbitmqctl", "-q", "cluster_status")
}

// ForgetClusterNode removes an offline node from the cluster metadata. Run it
// from a surviving member after the target node has been stopped.
func (m *RabbitMQManager) ForgetClusterNode(k *config.RabbitMQConfig, node string) (string, error) {
	return m.ExecOutput(k, "rabbitmqctl", "-q", "forget_cluster_node", node)
}

// LogsTail prints the last lines of the container log to stderr.
func (m *RabbitMQManager) LogsTail(name string, lines int) {
	cmd := podmanCommand(m.podman, "logs", "--tail", fmt.Sprint(lines), name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// --- internal helpers ---------------------------------------------------

func (m *RabbitMQManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *RabbitMQManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *RabbitMQManager) imageExists(tag string) (bool, error) {
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

func (m *RabbitMQManager) containerExists(name string) (bool, error) {
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

func (m *RabbitMQManager) containerRunning(name string) (bool, error) {
	out, err := m.run("ps", "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == name {
			return containerProcessAlive(m.podman, name), nil
		}
	}
	return false, nil
}
