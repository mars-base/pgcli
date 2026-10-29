package podman

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/rocketmq"
)

// RocketMQ runs as three containers per instance: a NameServer (with an
// embedded controller when clustered), a broker and a proxy. The official
// image's user is uid/gid 3000.
const (
	RocketMQRoleNamesrv = "namesrv"
	RocketMQRoleBroker  = "broker"
	RocketMQRoleProxy   = "proxy"

	RocketMQContainerUID = 3000
	RocketMQContainerGID = 3000
)

// RocketMQManager manages the RocketMQ container set.
type RocketMQManager struct {
	cfg     *config.Config
	podman  string
	dataDir string
}

// NewRocketMQManager creates a RocketMQManager.
func NewRocketMQManager(cfg *config.Config) (*RocketMQManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := ""
	if cfg != nil {
		dataDir = cfg.BaseDir
	}
	ensurePodmanStateReady(path)
	return &RocketMQManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// EnsureImage pulls the pinned upstream RocketMQ image when missing.
func (m *RocketMQManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling RocketMQ image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainers recreates the whole container set (install/config-change
// semantics). Data and config are never deleted here.
func (m *RocketMQManager) EnsureContainers(k *config.RocketMQConfig) error {
	if err := m.ensureFiles(k); err != nil {
		return err
	}
	if err := m.EnsureImage(k.ImageTag); err != nil {
		return err
	}
	for _, role := range rocketMQRoles() {
		name := config.RocketMQContainerName(*k, role)
		running, err := m.containerRunning(name)
		if err != nil {
			return err
		}
		if running {
			fmt.Printf("-> RocketMQ %s container already running, recreating to apply updated config...\n", role)
		}
		exists, err := m.containerExists(name)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", name); err != nil {
				return fmt.Errorf("removing RocketMQ %s container: %w", role, err)
			}
		}
		if err := m.createContainer(k, role); err != nil {
			return err
		}
	}
	return nil
}

// StartContainers starts the set in namesrv -> broker -> proxy order without
// recreating existing containers (start/autostart path). Missing containers
// are created. The first return value reports that every container was
// already running.
func (m *RocketMQManager) StartContainers(k *config.RocketMQConfig) (bool, error) {
	if err := m.ensureFiles(k); err != nil {
		return false, err
	}
	allRunning := true
	for _, role := range rocketMQRoles() {
		name := config.RocketMQContainerName(*k, role)
		running, err := m.containerRunning(name)
		if err != nil {
			return false, err
		}
		if running {
			fmt.Printf("  [OK] RocketMQ %s container %s already running\n", role, name)
			continue
		}
		allRunning = false
		exists, err := m.containerExists(name)
		if err != nil {
			return false, err
		}
		if exists {
			if _, err := m.run("start", name); err == nil {
				running, checkErr := m.containerRunning(name)
				if checkErr != nil {
					return false, fmt.Errorf("verifying RocketMQ %s container %s after start: %w", role, name, checkErr)
				}
				// `podman start` exits 0 on a stale "running" record even
				// though nothing started, so re-check process liveness.
				if running {
					fmt.Printf("  [OK] RocketMQ %s container %s started\n", role, name)
					continue
				}
			}
			fmt.Printf("  [!] RocketMQ %s container %s in improper state, recreating...\n", role, name)
			if _, err := m.run("rm", "-f", name); err != nil {
				return false, fmt.Errorf("removing improper RocketMQ %s container: %w", role, err)
			}
		}
		if err := m.EnsureImage(k.ImageTag); err != nil {
			return false, err
		}
		if err := m.createContainer(k, role); err != nil {
			return false, err
		}
	}
	return allRunning, nil
}

// StopContainers stops the set in reverse order. Containers that are already
// stopped or absent are reported and skipped.
func (m *RocketMQManager) StopContainers(k *config.RocketMQConfig) error {
	for _, role := range slices.Backward(rocketMQRoles()) {
		name := config.RocketMQContainerName(*k, role)
		running, err := m.containerRunning(name)
		if err != nil {
			return err
		}
		if !running {
			fmt.Printf("  [OK] RocketMQ %s container %s already stopped\n", role, name)
			continue
		}
		if _, err := m.run("stop", "-t", "30", name); err != nil {
			return fmt.Errorf("stopping RocketMQ %s container: %w", role, err)
		}
		fmt.Printf("  [OK] RocketMQ %s container %s stopped\n", role, name)
	}
	return nil
}

// RemoveContainers removes the set in reverse order, keeping data unless
// CleanData is called separately.
func (m *RocketMQManager) RemoveContainers(k *config.RocketMQConfig) error {
	for _, role := range slices.Backward(rocketMQRoles()) {
		name := config.RocketMQContainerName(*k, role)
		exists, err := m.containerExists(name)
		if err != nil {
			return err
		}
		if !exists {
			fmt.Printf("  [OK] RocketMQ %s container %s already absent\n", role, name)
			continue
		}
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing RocketMQ %s container: %w", role, err)
		}
		fmt.Printf("  [OK] RocketMQ %s container %s removed\n", role, name)
	}
	return nil
}

// CleanData deletes the instance directory (config, data and logs) plus a
// distinct custom data dir.
func (m *RocketMQManager) CleanData(k *config.RocketMQConfig) error {
	instanceDir := filepath.Dir(filepath.Dir(k.ConfigDir))
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

// ContainerStatus reports whether one role container is running and whether
// it exists at all.
func (m *RocketMQManager) ContainerStatus(k *config.RocketMQConfig, role string) (bool, bool, error) {
	name := config.RocketMQContainerName(*k, role)
	exists, err := m.containerExists(name)
	if err != nil {
		return false, false, err
	}
	if !exists {
		return false, false, nil
	}
	running, err := m.containerRunning(name)
	if err != nil {
		return false, true, err
	}
	return running, true, nil
}

// ContainerName returns the container name for one role.
func (m *RocketMQManager) ContainerName(k *config.RocketMQConfig, role string) string {
	return config.RocketMQContainerName(*k, role)
}

// ensureFiles renders the configs, creates the data/log directories and hands
// the directories the container user writes to (uid/gid 3000) over to it.
func (m *RocketMQManager) ensureFiles(k *config.RocketMQConfig) error {
	if err := rocketmq.WriteConfigs(*k); err != nil {
		return err
	}
	dirs := []string{rocketmq.BrokerDataDir(*k)}
	if k.Clustered() {
		dirs = append(dirs, rocketmq.ControllerDataDir(*k))
	}
	for _, role := range rocketMQRoles() {
		dirs = append(dirs, rocketmq.LogDir(*k, role))
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating RocketMQ directory %s: %w", dir, err)
		}
		if err := m.ensureDirOwner(dir); err != nil {
			return err
		}
	}
	return nil
}

// ensureDirOwner chowns one (empty or existing) directory to the image user.
// The container creates its own files below it, so a non-recursive chown is
// enough and avoids walking a large store on every start.
func (m *RocketMQManager) ensureDirOwner(dir string) error {
	path := hostMountPath(dir)
	if err := podmanUnshareChown(m.podman, RocketMQContainerUID, RocketMQContainerGID, path); err == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(path, RocketMQContainerUID, RocketMQContainerGID); err == nil {
			return nil
		}
	}
	return fmt.Errorf("chowning RocketMQ directory %s to %d:%d (the image's appuser): rootless podman does this through `podman unshare`; on a rootful host run pgcli as root, or pre-chown the directory manually",
		dir, RocketMQContainerUID, RocketMQContainerGID)
}

func (m *RocketMQManager) createContainer(k *config.RocketMQConfig, role string) error {
	args, err := rocketMQCreateArgs(*k, role)
	if err != nil {
		return err
	}
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating RocketMQ %s container: %w", role, err)
	}
	fmt.Printf("  [OK] RocketMQ %s container %s created\n", role, config.RocketMQContainerName(*k, role))
	return nil
}

// rocketMQCreateArgs builds the `podman run` argv for one role. Pure so tests
// can pin mounts, heaps and the startup command without podman.
func rocketMQCreateArgs(k config.RocketMQConfig, role string) ([]string, error) {
	var heap, script string
	switch role {
	case RocketMQRoleNamesrv:
		heap = k.NamesrvHeap
		script = "mqnamesrv -c " + filepath.Join(rocketmq.ContainerConfigDir, rocketmq.NamesrvConfigFile)
	case RocketMQRoleBroker:
		heap = k.Heap
		script = "mqbroker -c " + filepath.Join(rocketmq.ContainerConfigDir, rocketmq.BrokerConfigFile)
	case RocketMQRoleProxy:
		heap = k.ProxyHeap
		script = "mqproxy -pc " + filepath.Join(rocketmq.ContainerConfigDir, rocketmq.ProxyConfigFile) + " -pm cluster"
	default:
		return nil, fmt.Errorf("unknown RocketMQ role %q", role)
	}
	if heap == "" {
		heap = k.Heap
	}
	args := []string{
		"run", "-d",
		"--name", config.RocketMQContainerName(k, role),
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(k.ConfigDir), rocketmq.ContainerConfigDir),
	}
	switch role {
	case RocketMQRoleBroker:
		args = append(args, "-v", fmt.Sprintf("%s:%s:z", hostMountPath(rocketmq.BrokerDataDir(k)), rocketmq.ContainerDataDir))
	case RocketMQRoleNamesrv:
		if k.Clustered() {
			args = append(args, "-v", fmt.Sprintf("%s:%s:z", hostMountPath(rocketmq.ControllerDataDir(k)), rocketmq.ContainerControllerDir))
		}
	}
	args = append(args, "-v", fmt.Sprintf("%s:%s:z", hostMountPath(rocketmq.LogDir(k, role)), rocketmq.ContainerLogDir))
	if heap != "" {
		args = append(args, "-e", "JAVA_OPT_EXT=-Xms"+heap+" -Xmx"+heap)
	}
	if role == RocketMQRoleProxy {
		namesrv, err := config.RocketMQNamesrvAddrs(k)
		if err != nil {
			return nil, err
		}
		args = append(args, "-e", "NAMESRV_ADDR="+strings.Join(namesrv, ";"))
	}
	args = append(args, k.ImageTag, "sh")
	return append(args, strings.Fields(script)...), nil
}

// rocketMQRoles is the startup order; stop/remove walk it in reverse.
func rocketMQRoles() []string {
	return []string{RocketMQRoleNamesrv, RocketMQRoleBroker, RocketMQRoleProxy}
}

// WaitReady waits for the nameserver, then broker registration, then the
// proxy listeners. In cluster mode broker registration and proxy listeners are
// expected to lag until the controller quorum forms, so those timeouts only
// warn; a standalone instance must become fully ready.
func (m *RocketMQManager) WaitReady(k *config.RocketMQConfig) error {
	namesrvAddr := fmt.Sprintf("127.0.0.1:%d", k.NamesrvPort)
	if err := waitTCPPorts([]string{namesrvAddr}, 30*time.Second); err != nil {
		return fmt.Errorf("RocketMQ nameserver did not become ready: %w", err)
	}
	if err := m.waitBrokerReady(k); err != nil {
		return err
	}
	return m.waitProxyReady(k)
}

func (m *RocketMQManager) waitBrokerReady(k *config.RocketMQConfig) error {
	timeout := 90 * time.Second
	if k.Clustered() {
		timeout = 20 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	lastDetail := ""
	brokerName := config.RocketMQContainerName(*k, RocketMQRoleBroker)
	for {
		out, err := m.MQAdmin(k, "clusterList")
		if err == nil {
			if rocketMQBrokerRegistered(out, k.ClusterName(), k.BrokerPort) {
				return nil
			}
			lastDetail = "broker not listed in clusterList"
		} else {
			lastErr = err
			lastDetail = strings.TrimSpace(out)
		}
		if time.Now().After(deadline) {
			if k.Clustered() {
				detail := lastDetail
				if detail == "" && lastErr != nil {
					detail = lastErr.Error()
				}
				fmt.Printf("  [!] RocketMQ broker is running, but it has not registered with the controller yet (%s) — install the remaining nodes with the same --cluster and --peer values, then run `pg addon rocketmq cluster status --name %s`\n", detail, k.Name)
				return nil
			}
			if lastErr != nil {
				if lastDetail != "" {
					return fmt.Errorf("RocketMQ broker did not register with the nameserver: %w (%s)", lastErr, lastDetail)
				}
				return fmt.Errorf("RocketMQ broker did not register with the nameserver: %w", lastErr)
			}
			return fmt.Errorf("RocketMQ broker %s did not register with the nameserver within %s", brokerName, timeout)
		}
		time.Sleep(time.Second)
	}
}

func (m *RocketMQManager) waitProxyReady(k *config.RocketMQConfig) error {
	timeout := 60 * time.Second
	if k.Clustered() {
		timeout = 20 * time.Second
	}
	ports := []string{
		fmt.Sprintf("127.0.0.1:%d", k.ProxyPort),
		fmt.Sprintf("127.0.0.1:%d", k.GRPCPort),
	}
	if err := waitTCPPorts(ports, timeout); err != nil {
		if k.Clustered() {
			fmt.Printf("  [!] RocketMQ proxy listeners are not ready yet (%v) — the broker may still be waiting for the controller quorum; check again with `pg addon rocketmq status --name %s`\n", err, k.Name)
			return nil
		}
		return fmt.Errorf("RocketMQ proxy did not become ready: %w", err)
	}
	return nil
}

func waitTCPPorts(addrs []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	lastAddr := ""
	for {
		pending := false
		for _, addr := range addrs {
			conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				pending = true
				lastErr = err
				lastAddr = addr
				break
			}
			conn.Close()
		}
		if !pending {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s: %w", lastAddr, lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// rocketMQBrokerRegistered reports whether clusterList output contains a
// registered broker for cluster on the given broker port. Header lines start
// with '#'; the Address column carries host:brokerPort while the neighbouring
// HA address column carries brokerPort+1, so match the Address column when the
// header names it.
func rocketMQBrokerRegistered(output, cluster string, brokerPort int) bool {
	suffix := ":" + strconv.Itoa(brokerPort)
	lines := strings.Split(output, "\n")
	addressCol := -1
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "#") {
			continue
		}
		for i, field := range fields {
			if field == "Address" {
				addressCol = i
				break
			}
		}
		break
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != cluster {
			continue
		}
		if addressCol >= 0 {
			if addressCol < len(fields) && strings.HasSuffix(fields[addressCol], suffix) {
				return true
			}
			continue
		}
		for _, field := range fields {
			if strings.HasSuffix(field, suffix) {
				return true
			}
		}
	}
	return false
}

// MQAdmin runs mqadmin from a transient container against the local
// NameServer. It deliberately does not use `podman exec`: on some podman
// builds (4.9.3 static) the exec attach path deadlocks after the command has
// already printed its output, holding a libpod shm lock that in turn wedges
// later podman calls. A one-shot container needs no exec session and exits
// cleanly.
func (m *RocketMQManager) MQAdmin(k *config.RocketMQConfig, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := podmanCommandContext(ctx, m.podman, rocketMQAdminArgs(*k, args...)...)
	// podman run can leave a conmon/container child holding the stdout pipe
	// after the client is killed; without WaitDelay CombinedOutput would block
	// well past the context deadline on that open pipe.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return string(out), fmt.Errorf("podman run mqadmin timed out after 90s")
		}
		return string(out), fmt.Errorf("podman run mqadmin: %w", err)
	}
	return string(out), nil
}

// rocketMQAdminArgs builds the mqadmin argv and supplies the local NameServer
// address unless the caller already passed -n.
func rocketMQAdminArgs(k config.RocketMQConfig, args ...string) []string {
	runArgs := []string{
		"run", "--rm", "--network", "host", "--http-proxy=false",
		"-e", "JAVA_OPT_EXT=-Xms192m -Xmx192m",
		k.ImageTag, "sh", "mqadmin",
	}
	runArgs = append(runArgs, args...)
	if !slices.Contains(args, "-n") {
		runArgs = append(runArgs, "-n", fmt.Sprintf("127.0.0.1:%d", k.NamesrvPort))
	}
	return runArgs
}

// ClusterList returns the raw clusterList table for this instance.
func (m *RocketMQManager) ClusterList(k *config.RocketMQConfig, more bool) (string, error) {
	args := []string{"clusterList"}
	if more {
		args = append(args, "-m")
	}
	return m.MQAdmin(k, args...)
}

// Exec runs a command inside one role container, sharing the host terminal
// when interactive. With no args it opens bash.
func (m *RocketMQManager) Exec(k *config.RocketMQConfig, role string, interactive bool, args ...string) error {
	if !validRocketMQRole(role) {
		return fmt.Errorf("unknown RocketMQ service %q", role)
	}
	if len(args) == 0 {
		args = []string{"/bin/bash"}
	}
	execArgs := rocketMQExecArgs(config.RocketMQContainerName(*k, role), interactive, isTerminal(os.Stdin), args...)
	return m.runInteractive(execArgs...)
}

// ExecOutput runs a command inside one role container and captures output.
func (m *RocketMQManager) ExecOutput(k *config.RocketMQConfig, role string, args ...string) (string, error) {
	if !validRocketMQRole(role) {
		return "", fmt.Errorf("unknown RocketMQ service %q", role)
	}
	execArgs := []string{"exec", config.RocketMQContainerName(*k, role)}
	execArgs = append(execArgs, args...)
	return execIdleKilled(m.podman, 30*time.Second, defaultIdleKill, execArgs...)
}

func validRocketMQRole(role string) bool {
	return slices.Contains(rocketMQRoles(), role)
}

// rocketMQExecArgs builds the podman exec argv. -t requires a real terminal:
// over SSH or with piped stdin it hangs without starting the command, so use
// -i alone to keep piped stdin working.
func rocketMQExecArgs(container string, interactive, tty bool, args ...string) []string {
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

// LogsTail prints the last lines of a role container's log to stderr.
func (m *RocketMQManager) LogsTail(container string, lines int) {
	cmd := podmanCommand(m.podman, "logs", "--tail", fmt.Sprint(lines), container)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// --- internal helpers ---------------------------------------------------

func (m *RocketMQManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *RocketMQManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *RocketMQManager) imageExists(tag string) (bool, error) {
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

func (m *RocketMQManager) containerExists(name string) (bool, error) {
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

func (m *RocketMQManager) containerRunning(name string) (bool, error) {
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
