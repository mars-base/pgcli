package podman

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// EtcdManager manages standalone etcd containers. Like PgBouncerManager it
// operates over the top-level (cross-instance) addon configs, since etcd is
// shared infrastructure for a HA cluster rather than a per-instance sidecar.
type EtcdManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

// NewEtcdManager creates an EtcdManager.
func NewEtcdManager(cfg *config.Config) (*EtcdManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &EtcdManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
	}, nil
}

// resolveDataDir returns the member's data dir. Data is always laid out as
// <root>/<name>/data so multiple members on one host never share a directory:
// the root is the explicit --data-dir override when set, otherwise the default
// base (<base>/addon/etcd).
func (m *EtcdManager) resolveDataDir(ec *config.EtcdConfig) string {
	root := ec.DataDir
	if root == "" {
		root = filepath.Join(m.dataDir, "addon", "etcd")
	}
	return filepath.Join(root, ec.Name, "data")
}

// DataDir returns the member's resolved host data directory (for display).
func (m *EtcdManager) DataDir(ec *config.EtcdConfig) string {
	return m.resolveDataDir(ec)
}

// EnsureContainer creates or restarts the etcd container for the given
// member. initialCluster is the etcd --initial-cluster value and state is
// "new" (bootstrapping the first member) or "existing" (joining a running
// cluster). Idempotent: a running member is recreated to pick up updated
// flags; a stale (stopped) container is removed first.
func (m *EtcdManager) EnsureContainer(ec *config.EtcdConfig, initialCluster, state string) error {
	containerName := ec.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> etcd container already running, restarting to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping etcd container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing etcd container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale etcd container: %w", err)
			}
		}
	}

	if err := m.createContainer(ec, initialCluster, state); err != nil {
		return err
	}
	fmt.Println("  [OK] etcd container started")
	return nil
}

// MemberAdd registers a new member with a running cluster by executing
// `etcdctl member add` inside the coordinator container (an existing member),
// pointed at that coordinator's own client endpoint. Must be called before the
// new member is started; the member then joins with --initial-cluster-state
// existing.
//
// A just-joined cluster briefly reports "unhealthy cluster" while it elects a
// leader, so the call is retried with a short backoff to absorb that transient.
func (m *EtcdManager) MemberAdd(coordinatorContainer string, coordinatorClientPort int, newMemberName, peerURL string) error {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", coordinatorClientPort)
	return m.retryMemberQuorum(func() error {
		_, err := m.run("exec", coordinatorContainer, "etcdctl",
			"--endpoints="+endpoint, "member", "add", newMemberName, "--peer-urls="+peerURL)
		if err != nil {
			return fmt.Errorf("registering etcd member %q with the cluster: %w", newMemberName, err)
		}
		return nil
	})
}

// MemberRemove deregisters a member from a running cluster by name, resolving
// its member ID first (etcd v3.5 `member remove` takes a hex ID, not a name).
// No-op when the member is not currently registered. The remove itself is
// retried for the same post-join quorum transient as MemberAdd.
func (m *EtcdManager) MemberRemove(coordinatorContainer string, coordinatorClientPort int, name string) error {
	id, found, err := m.memberIDByName(coordinatorContainer, coordinatorClientPort, name)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", coordinatorClientPort)
	return m.retryMemberQuorum(func() error {
		if _, err := m.run("exec", coordinatorContainer, "etcdctl",
			"--endpoints="+endpoint, "member", "remove", id); err != nil {
			return fmt.Errorf("deregistering etcd member %q from the cluster: %w", name, err)
		}
		return nil
	})
}

// MemberExists reports whether a member with the given name is already part of
// the cluster, by listing members from the coordinator container. Used to keep
// installs idempotent: a member already registered is not re-added.
func (m *EtcdManager) MemberExists(coordinatorContainer string, coordinatorClientPort int, name string) (bool, error) {
	_, found, err := m.memberIDByName(coordinatorContainer, coordinatorClientPort, name)
	return found, err
}

// memberIDByName lists members from the coordinator container and returns the
// hex member ID registered under `name`, or ("", false, nil) when no member
// has that name. Retries the same post-join quorum transient as MemberAdd.
// etcdctl -w simple lines are:
//
//	<id>, <status>, <name>, <peerURLs>, <clientURLs>, <isLearner>
func (m *EtcdManager) memberIDByName(coordinatorContainer string, coordinatorClientPort int, name string) (string, bool, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", coordinatorClientPort)
	var out string
	err := m.retryMemberQuorum(func() error {
		o, err := m.run("exec", coordinatorContainer, "etcdctl",
			"--endpoints="+endpoint, "member", "list", "-w", "simple")
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		return "", false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Split(line, ",")
		if len(fields) < 3 {
			continue
		}
		if strings.TrimSpace(fields[2]) == name {
			return strings.TrimSpace(fields[0]), true, nil
		}
	}
	return "", false, nil
}

// retryMemberQuorum runs op, retrying while it fails with an etcd
// "unhealthy cluster" / "no leader" / timeout error — the window right after a
// member joins or a leader is re-elected, when the cluster briefly lacks
// quorum. Gives up after a short bounded backoff so a genuinely dead cluster
// still fails fast.
func (m *EtcdManager) retryMemberQuorum(op func() error) error {
	const attempts = 6
	transient := []string{"unhealthy cluster", "no leader", "leader changed", "deadline exceeded"}
	var err error
	for i := 0; i < attempts; i++ {
		err = op()
		if err == nil {
			return nil
		}
		msg := strings.ToLower(err.Error())
		isTransient := false
		for _, marker := range transient {
			if strings.Contains(msg, marker) {
				isTransient = true
				break
			}
		}
		if !isTransient {
			return err
		}
		time.Sleep(time.Duration(2+i) * time.Second)
	}
	return err
}

// clientURL/peerURL helpers: a member advertises on its AdvertiseHost
// (LAN address for cross-host clusters, loopback otherwise) but always also
// listens on loopback, so local etcdctl and cross-host peers both work.
func etcdListenURLs(host string, port int) string {
	url := fmt.Sprintf("http://%s:%d", host, port)
	if host != "127.0.0.1" {
		url = fmt.Sprintf("http://127.0.0.1:%d,%s", port, url)
	}
	return url
}

// createContainer runs an etcd member on host networking with the member's
// client and peer ports, advertising on its AdvertiseHost.
func (m *EtcdManager) createContainer(ec *config.EtcdConfig, initialCluster, state string) error {
	dataDir := m.resolveDataDir(ec)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating etcd data dir: %w", err)
	}

	clientURL := ec.ClientURL()
	peerURL := ec.PeerURL()
	listenClient := etcdListenURLs(ec.AdvertiseAddr(), ec.ClientPort)
	listenPeer := etcdListenURLs(ec.AdvertiseAddr(), ec.PeerPort)

	// The etcd image ships no ENTRYPOINT — its CMD is the full path to the
	// binary — so the executable must be passed explicitly before the flags.
	// --http-proxy=false stops podman from injecting the host's HTTP(S)_PROXY
	// into the container: etcd's raft transport honors those vars, so a proxy
	// would hijack peer traffic to the advertised (LAN) URLs and break
	// cross-address clustering. Image pulls run client-side and keep the proxy.
	args := []string{
		"run", "-d",
		"--name", ec.ContainerName,
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:/etcd-data:z", hostMountPath(dataDir)),
		ec.ImageTag,
		"/usr/local/bin/etcd",
		"--name", ec.Name,
		"--data-dir", "/etcd-data",
		"--initial-advertise-peer-urls", peerURL,
		"--listen-peer-urls", listenPeer,
		"--advertise-client-urls", clientURL,
		"--listen-client-urls", listenClient,
		"--initial-cluster", initialCluster,
		"--initial-cluster-state", state,
		"--initial-cluster-token", ec.ClusterName,
		// Tuning defaults for a HA-cluster DCS: periodic compaction keeps
		// the history window bounded (Patroni relies on it), and an 8 GiB
		// backend quota is well above what a small metadata store needs.
		"--auto-compaction-mode", "periodic",
		"--auto-compaction-retention", "24h",
		"--quota-backend-bytes", "8589934592",
	}

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating etcd container: %w", err)
	}
	return nil
}

// Remove stops and removes the etcd container, then cleans up its data
// directory on the host.
func (m *EtcdManager) Remove(ec *config.EtcdConfig) error {
	containerName := ec.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing etcd container: %w", err)
	}
	fmt.Println("  [OK] etcd container removed")

	dir := m.resolveDataDir(ec)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dir)
	}

	// Data lives at <root>/<name>/data; drop the now-empty <name> dir too
	// (leaving the shared root in place). Ignore the error when it's not empty.
	os.Remove(filepath.Dir(dir))

	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *EtcdManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// MemberAddAt registers a new member with a cluster coordinator reachable at
// coordinatorEndpoint (any client URL — a local member or a cross-host peer).
// It uses a short-lived etcdctl container (host network), so the coordinator
// does not have to be a local pgcli-managed container. Returns the raw
// etcdctl output so callers can parse the authoritative ETCD_INITIAL_CLUSTER
// it prints for the joining member's --initial-cluster value.
func (m *EtcdManager) MemberAddAt(imageTag, coordinatorEndpoint, newMemberName, peerURL string) (string, error) {
	var out string
	err := m.retryMemberQuorum(func() error {
		o, err := m.runEtcdctl(imageTag, coordinatorEndpoint, false,
			"member", "add", newMemberName, "--peer-urls="+peerURL)
		if err != nil {
			return fmt.Errorf("registering etcd member %q with the cluster at %s: %w", newMemberName, coordinatorEndpoint, err)
		}
		out = o
		return nil
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

// ListMembersAt returns the raw output of `etcdctl member list -w simple`
// against a coordinator endpoint. Same transport as MemberAddAt — usable to
// discover an existing cluster's membership before starting a new cross-host
// member.
func (m *EtcdManager) ListMembersAt(imageTag, coordinatorEndpoint string) (string, error) {
	var out string
	err := m.retryMemberQuorum(func() error {
		o, err := m.runEtcdctl(imageTag, coordinatorEndpoint, false, "member", "list", "-w", "simple")
		if err != nil {
			return fmt.Errorf("listing etcd members via %s: %w", coordinatorEndpoint, err)
		}
		out = o
		return nil
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

// MemberExistsAt reports whether a member named `name` is registered in the
// cluster reachable at coordinatorEndpoint (cross-host capable). Parses the
// same `<id>, <status>, <name>, ...` lines as memberIDByName.
func (m *EtcdManager) MemberExistsAt(imageTag, coordinatorEndpoint, name string) (bool, error) {
	out, err := m.ListMembersAt(imageTag, coordinatorEndpoint)
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Split(line, ",")
		if len(fields) >= 3 && strings.TrimSpace(fields[2]) == name {
			return true, nil
		}
	}
	return false, nil
}

// EnsureImage pulls the etcd image if it is not present locally, so etcdctl
// works on a host that has no etcd member installed yet (or none running).
func (m *EtcdManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling etcd image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// Etcdctl runs an etcdctl subcommand from a short-lived container. etcd
// containers are per-member, so instead of exec'ing into one we launch
// `podman run --rm` on the member's image over the host network and hand the
// args to etcdctl. The client endpoint comes from ETCDCTL_ENDPOINTS (a native
// etcdctl env var) — inherited from the caller's environment when set, or from
// defaultEndpoint otherwise — so no member container needs to be picked. The
// image is pulled first if the host does not already have it.
func (m *EtcdManager) Etcdctl(imageTag, defaultEndpoint string, args []string) error {
	endpoint := os.Getenv("ETCDCTL_ENDPOINTS")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	_, err := m.runEtcdctl(imageTag, endpoint, true, args...)
	return err
}

// runEtcdctl is the internal base for invoking etcdctl from a short-lived
// container on the host network. It ensures the image, wires up
// ETCDCTL_ENDPOINTS, and either attaches stdout for the user (`interactive`)
// or captures it for parsing. Cross-host member-add/list flows use the
// captured mode; the `pg etcdctl` command uses the attached mode.
func (m *EtcdManager) runEtcdctl(imageTag, endpoint string, interactive bool, args ...string) (string, error) {
	if err := m.EnsureImage(imageTag); err != nil {
		return "", err
	}

	runArgs := []string{"run", "--rm"}
	if interactive && isTerminal(os.Stdin) {
		runArgs = append(runArgs, "-it")
	} else {
		runArgs = append(runArgs, "-i=false")
	}
	runArgs = append(runArgs,
		"--network", "host",
		// Same reason as createContainer: etcdctl's gRPC transport honors
		// HTTP(S)_PROXY, so podman's proxy injection would hijack requests to
		// advertised (LAN) endpoints.
		"--http-proxy=false",
		"-e", "ETCDCTL_ENDPOINTS="+endpoint,
		imageTag,
		"etcdctl",
	)
	runArgs = append(runArgs, args...)

	slog.Debug("podman etcdctl", "endpoint", endpoint, "args", args)
	cmd := podmanCommand(m.podman, runArgs...)
	cmd.Stdin = os.Stdin
	if interactive {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("running etcdctl: %w", err)
		}
		return "", nil
	}

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("etcdctl %s: %s", strings.Join(args, " "), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("etcdctl %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// StartContainer brings an existing etcd member container up for the boot
// service — the start-only counterpart of EnsureContainer (which performs
// cluster registration and is install semantics). It never re-runs member
// add: a data-dir-initialized member reloads its cluster state from disk, so
// plain start (or a config-only recreate when the container is gone or stuck
// in an improper state) is enough to rejoin.
//
// An already-initialized member ignores the --initial-cluster flags on
// restart, so the recreate path passes a placeholder self entry.
func (m *EtcdManager) StartContainer(ec *config.EtcdConfig) error {
	containerName := ec.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] etcd %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] etcd %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] etcd %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper etcd container: %w", err)
		}
	}
	if err := m.createContainer(ec, ec.Name+"="+ec.PeerURL(), "existing"); err != nil {
		return err
	}
	fmt.Printf("  [OK] etcd %s started\n", containerName)
	return nil
}

// Stop stops an etcd container.
func (m *EtcdManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ------------------------------------------------

// runInteractive runs a podman command with attached stdin/stdout/stderr,
// used for image pulls so the download progress is visible to the user.
func (m *EtcdManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *EtcdManager) imageExists(tag string) (bool, error) {
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

func (m *EtcdManager) run(args ...string) (string, error) {
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

func (m *EtcdManager) containerExists(name string) (bool, error) {
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

func (m *EtcdManager) containerRunning(name string) (bool, error) {
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
