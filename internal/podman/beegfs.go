package podman

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// BeeGFSManager manages one BeeGFS filesystem — pgcli's FIRST multi-container
// addon. A single config.BeeGFSConfig entry describes a whole filesystem, and
// this manager brings up, tears down, and reports on three ordered containers
// that together form a converged (single-host) BeeGFS cluster:
//
//	pgcli-beegfs<ns>-<name>-mgmtd   → management daemon (node/target registry)
//	pgcli-beegfs<ns>-<name>-meta    → metadata service
//	pgcli-beegfs<ns>-<name>-storage → storage service
//
// The three official images (ghcr.io/thinkparq/beegfs-{mgmtd,meta,storage},
// config.BeeGFSImageRepo + DefaultBeeGFSVersion) share one mechanism: their
// baked entrypoint (start.sh → init.py) reads lowercase `beegfs_setup_N`
// env vars and runs each as a `beegfs-setup-<service>` command on FIRST boot
// only (idempotency marker <target>/alreadyInitByContainer in the bind mount),
// then execs the daemon with the remaining container args. init.py's invariant
// "number of setup commands == number of targets" is satisfied by construction
// here — every service is given exactly one target dir and one setup command.
//
// Three load-bearing divergences from every single-container addon:
//
//   - Host networking only. BeeGFS speaks its own TCP/UDP protocol, not HTTP;
//     the daemons bind their ports on the host directly. There is no bridge
//     fallback: on macOS (no host networking) installation is refused. The
//     ports themselves are the stock defaults (mgmtd 8008/8010, meta 8005,
//     storage 8003) — verified on vm01, 8.4's meta and storage daemons accept
//     NO listen-port override (connPortsList/connTcpPort are rejected as
//     invalid, and connMetaPort is accepted but breaks registration), so a
//     second filesystem on the same host could never exist regardless of what
//     pgcli allocated. EnsureCluster enforces that one-filesystem rule.
//   - No privileged mode, no capability additions, no chown. Pure
//     `--network host` + `:z` bind mounts, verified end-to-end on vm01
//     (no RDMA in v1). The images run as root and own their data dirs —
//     unlike Gitea's uid-1000 or Grafana's uid-472 pre-chown dance.
//   - Readiness is log-probed, not exec-probed. None of the three images
//     ships beegfs-ctl or check-servers (only the new `beegfs` gRPC CLI, and
//     only mgmtd's image can meaningfully run it against itself). meta and
//     storage each log "Registration ... complete" once they have reached
//     mgmtd — that line doubles as "mgmtd is up AND this service joined the
//     cluster", which is exactly the ordering gate EnsureCluster needs.
//
// TLS and licensing: the mgmtd daemon enforces gRPC TLS by default; v1 passes
// --tls-disable true (operator's explicit choice — the cluster is only usable
// with a matching --tls-disable on the client side too). A BeeGFS license is a
// system-specific registration the user must obtain; pgcli never generates or
// injects one. An unlicensed 8.4 mgmtd logs a quota warning but the
// filesystem still functions for community-scale use.
type BeeGFSManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

// BeeGFS service roles. The constants are also the container-name suffixes and
// the --service values of `pg logs addon beegfs`.
const (
	BeeGFSServiceMgmtd   = "mgmtd"
	BeeGFSServiceMeta    = "meta"
	BeeGFSServiceStorage = "storage"
)

const (
	// beegfsContainerDataRoot is where each service's single host data dir is
	// bind-mounted, one subdir per role: /data/mgmtd, /data/meta,
	// /data/storage.
	beegfsContainerDataRoot = "/data"
	// beegfsMgmtdDBFile is the mgmtd SQLite path INSIDE its container — the
	// parent dir is the setup target init.py counts against beegfs_setup_N.
	beegfsMgmtdDBFile = beegfsContainerDataRoot + "/" + BeeGFSServiceMgmtd + "/mgmtd.sqlite"

	// Ready-probe markers, verified in vm01 container logs:
	// mgmtd reaches its serve loop with "Waiting for shutdown signal";
	// meta/storage complete registration with "Registration and management
	// info download complete." (that ALSO proves mgmtd was up — the gate that
	// makes per-role mgmtd probes unnecessary).
	beegfsMgmtdReadyLog     = "Waiting for shutdown signal"
	beegfsRegisteredLogMark = "Registration and management info download complete"

	// beegfsSetupTimeout bounds one service's registration wait inside
	// EnsureCluster. Registration needs only seconds; the ceiling exists so a
	// mgmtd that never comes up fails the install instead of hanging it.
	beegfsSetupTimeout = 60 * time.Second
)

// NewBeeGFSManager creates a BeeGFSManager. BeeGFS requires host networking,
// which podman provides on Linux only — on macOS the manager is constructed
// normally but every cluster operation refuses with a clear error.
func NewBeeGFSManager(cfg *config.Config) (*BeeGFSManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &BeeGFSManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
	}, nil
}

// hostNetUnsupported is the single refusal point for non-Linux platforms.
func hostNetUnsupported() error {
	return errors.New("BeeGFS requires host networking, which podman only provides on Linux")
}

// beegfsImageRef builds one service image ref from the stored version string:
// ghcr.io/thinkparq/beegfs-<role>:<version>.
func beegfsImageRef(role, version string) string {
	return config.BeeGFSImageRepo + "-" + role + ":" + version
}

// mgmtdHost is the address meta/storage (and clients) dial for mgmtd. Under
// host networking the loopback is correct for every container on this host; a
// wildcard Listen is printed as localhost but referenced as 127.0.0.1 inside
// the cluster.
func mgmtdHost(listen string) string {
	if listen == "" || listen == "0.0.0.0" || listen == "::" {
		return "127.0.0.1"
	}
	return listen
}

// --- Pure argv builders ---------------------------------------------------
//
// All three share the same skeleton: run detached with a role-suffixed name,
// host network, two -e flags (the setup command and the shared connection-auth
// secret), one :z bind mount of the service data dir, --restart
// unless-stopped, then the image ref followed by the daemon args — exactly the
// vm01-proven invocation (podman stops flag parsing at the image, so the
// mgmtd `--db-file ...` and meta/storage `key=value ...` tokens become the
// container command with no separator).

// beegfsMgmtdArgsAndEnv builds the `podman run` argv for the management
// daemon. bc.ImageTag carries the version; the baked CONN_AUTH_FILE_DATA env is
// written by init.py to /etc/beegfs/conn.auth and wired in by start.sh as
// --auth-file. --tls-disable true is v1's explicit decision. Non-default ports
// are passed to the daemon as --beemsg-port/--grpc-port (both ARE supported —
// the only movable ports in the whole system).
func beegfsMgmtdArgsAndEnv(bc *config.BeeGFSConfig, dataDir string) []string {
	setup := fmt.Sprintf("beegfs-mgmtd --init --db-file %s --log-target stderr", beegfsMgmtdDBFile)
	daemonArgs := []string{"--db-file", beegfsMgmtdDBFile, "--log-target", "stderr", "--tls-disable", "true"}
	if bc.MgmtdPort != 0 && bc.MgmtdPort != config.DefaultBeeGFSMgmtdPort {
		daemonArgs = append(daemonArgs, "--beemsg-port", strconv.Itoa(bc.MgmtdPort))
	}
	if bc.MgmtdGRPCPort != 0 && bc.MgmtdGRPCPort != config.DefaultBeeGFSMgmtdGRPCPort {
		daemonArgs = append(daemonArgs, "--grpc-port", strconv.Itoa(bc.MgmtdGRPCPort))
	}
	return beegfsRunArgs(bc, BeeGFSServiceMgmtd, dataDir, setup, daemonArgs)
}

// beegfsMetaArgsAndEnv builds the meta service argv. NodeIDs are a per
// service-type namespace (verified on vm01: meta -s 1 and storage -s 1 coexist
// as node_meta_1 and node_storage_1), so both use 1. The daemon args speak
// key=value config overrides: sysMgmtdHost points at mgmtd, and a non-default
// beemsg port is followed by connMgmtdPort — verified on vm01 to actually
// re-target registration.
func beegfsMetaArgsAndEnv(bc *config.BeeGFSConfig, dataDir string) []string {
	const tgt = beegfsContainerDataRoot + "/" + BeeGFSServiceMeta
	setup := fmt.Sprintf("beegfs-setup-meta -C -p %s -s 1", tgt)
	daemonArgs := []string{
		"sysMgmtdHost=" + mgmtdHost(bc.Listen),
		"storeAllowFirstRunInit=false",
		"storeMetaDirectory=" + tgt,
	}
	if bc.MgmtdPort != 0 && bc.MgmtdPort != config.DefaultBeeGFSMgmtdPort {
		daemonArgs = append(daemonArgs, "connMgmtdPort="+strconv.Itoa(bc.MgmtdPort))
	}
	return beegfsRunArgs(bc, BeeGFSServiceMeta, dataDir, setup, daemonArgs)
}

// beegfsStorageArgsAndEnv builds the storage service argv — same shape as
// meta with a target id (-i 101; target numbering is independent of node ids,
// a fixed 101 keeps it distinguishable in `beegfs` output).
func beegfsStorageArgsAndEnv(bc *config.BeeGFSConfig, dataDir string) []string {
	const tgt = beegfsContainerDataRoot + "/" + BeeGFSServiceStorage
	setup := fmt.Sprintf("beegfs-setup-storage -C -p %s -s 1 -i 101", tgt)
	daemonArgs := []string{
		"sysMgmtdHost=" + mgmtdHost(bc.Listen),
		"storeAllowFirstRunInit=false",
		"storeStorageDirectory=" + tgt,
	}
	if bc.MgmtdPort != 0 && bc.MgmtdPort != config.DefaultBeeGFSMgmtdPort {
		daemonArgs = append(daemonArgs, "connMgmtdPort="+strconv.Itoa(bc.MgmtdPort))
	}
	return beegfsRunArgs(bc, BeeGFSServiceStorage, dataDir, setup, daemonArgs)
}

// beegfsRunArgs is the shared argv skeleton the three builders feed.
func beegfsRunArgs(bc *config.BeeGFSConfig, role, dataDir, setup string, daemonArgs []string) []string {
	args := []string{
		"run", "-d",
		"--name", bc.ContainerName(role),
		"--network", "host",
		"-e", "beegfs_setup_1=" + setup,
		"-e", "CONN_AUTH_FILE_DATA=" + bc.ConnAuth,
		"-v", fmt.Sprintf("%s:%s/%s:z", hostMountPath(dataDir), beegfsContainerDataRoot, role),
		"--http-proxy=false",
		"--restart", "unless-stopped",
		beegfsImageRef(role, bc.ImageTag),
	}
	return append(args, daemonArgs...)
}

// --- Data dir resolution --------------------------------------------------

func (m *BeeGFSManager) resolveDataDir(bc *config.BeeGFSConfig, role string) string {
	base := filepath.Join(m.dataDir, "addon", "beegfs", bc.Name)
	switch role {
	case BeeGFSServiceMgmtd:
		if bc.MgmtdDataDir != "" {
			return bc.MgmtdDataDir
		}
		return filepath.Join(base, BeeGFSServiceMgmtd)
	case BeeGFSServiceMeta:
		if bc.MetaDataDir != "" {
			return bc.MetaDataDir
		}
		return filepath.Join(base, BeeGFSServiceMeta)
	default:
		if bc.StorageDataDir != "" {
			return bc.StorageDataDir
		}
		return filepath.Join(base, BeeGFSServiceStorage)
	}
}

// DataDir returns one service's resolved host data directory (for display).
func (m *BeeGFSManager) DataDir(bc *config.BeeGFSConfig, role string) string {
	return m.resolveDataDir(bc, role)
}

// MgmtdStoreExists reports whether a mgmtd data dir already holds the SQLite
// database — the cheap host-side signal that install revives an existing
// filesystem store rather than initialising a fresh cluster.
func MgmtdStoreExists(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dataDir, "mgmtd.sqlite"))
	return err == nil
}

// --- Cluster lifecycle ----------------------------------------------------

// EnsureImage pulls the three service images if missing (pull-only public
// ghcr images). All three are pulled up front so a mid-cluster network
// failure never leaves a half-registered filesystem.
func (m *BeeGFSManager) EnsureImage(bc *config.BeeGFSConfig) error {
	for _, role := range []string{BeeGFSServiceMgmtd, BeeGFSServiceMeta, BeeGFSServiceStorage} {
		ref := beegfsImageRef(role, bc.ImageTag)
		exists, err := m.imageExists(ref)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		fmt.Printf("-> Pulling image %s...\n", ref)
		if err := m.runInteractive("pull", ref); err != nil {
			return fmt.Errorf("pulling BeeGFS image %s (is ghcr.io reachable?): %w", ref, err)
		}
		fmt.Println("  [OK] Image pulled")
	}
	return nil
}

// EnsureCluster brings the three services up in dependency order:
// mgmtd → meta (waits for its registration log line, which also proves mgmtd
// is serving) → storage (same gate). The entry-name guard enforces v1's
// one-filesystem-per-host rule before touching anything.
func (m *BeeGFSManager) EnsureCluster(bc *config.BeeGFSConfig) error {
	if platform.Detect() != platform.Linux {
		return hostNetUnsupported()
	}
	if err := m.guardSingleFilesystem(bc); err != nil {
		return err
	}

	fmt.Println("-> Starting BeeGFS management service (mgmtd)...")
	if err := m.createService(bc, BeeGFSServiceMgmtd); err != nil {
		return err
	}
	if err := m.waitForLog(bc, BeeGFSServiceMgmtd, beegfsMgmtdReadyLog); err != nil {
		return err
	}

	fmt.Println("-> Starting BeeGFS metadata service (meta)...")
	if err := m.createService(bc, BeeGFSServiceMeta); err != nil {
		return err
	}
	if err := m.waitForLog(bc, BeeGFSServiceMeta, beegfsRegisteredLogMark); err != nil {
		return err
	}

	fmt.Println("-> Starting BeeGFS storage service (storage)...")
	if err := m.createService(bc, BeeGFSServiceStorage); err != nil {
		return err
	}
	if err := m.waitForLog(bc, BeeGFSServiceStorage, beegfsRegisteredLogMark); err != nil {
		return err
	}

	fmt.Println("  [OK] BeeGFS cluster up: mgmtd + meta + storage registered")
	return nil
}

// guardSingleFilesystem rejects a second filesystem on this host: meta and
// storage daemons have no listen-port override (verified on vm01), so a second
// one would collide on the fixed 8005/8003 and crash-loop. The guard compares
// the persisted config map so a stale config cannot sneak a second entry in;
// re-ensuring the SAME filesystem name (recreate/revive) is allowed.
func (m *BeeGFSManager) guardSingleFilesystem(bc *config.BeeGFSConfig) error {
	for name, other := range m.cfg.Addons.BeeGFS {
		if name == bc.Name || other.Name == bc.Name {
			continue
		}
		return fmt.Errorf("a BeeGFS filesystem already exists on this host (%q): BeeGFS 8.4's meta and storage daemons bind fixed ports 8005/8003 with no supported override, so pgcli supports exactly one filesystem per host (multiple ones need separate hosts or containers with host-port collisions)", name)
	}
	return nil
}

// createService (re)creates one service container: an existing container is
// stopped and removed first so updated config always takes effect (install
// semantics). The data dir is created if missing, never deleted here.
func (m *BeeGFSManager) createService(bc *config.BeeGFSConfig, role string) error {
	name := bc.ContainerName(role)
	if exists, err := m.containerExists(name); err != nil {
		return err
	} else if exists {
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing stale BeeGFS container %s: %w", name, err)
		}
	}
	dataDir := m.resolveDataDir(bc, role)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating BeeGFS data dir %s: %w", dataDir, err)
	}
	var args []string
	switch role {
	case BeeGFSServiceMgmtd:
		args = beegfsMgmtdArgsAndEnv(bc, dataDir)
	case BeeGFSServiceMeta:
		args = beegfsMetaArgsAndEnv(bc, dataDir)
	case BeeGFSServiceStorage:
		args = beegfsStorageArgsAndEnv(bc, dataDir)
	default:
		return fmt.Errorf("unknown BeeGFS service %q", role)
	}
	slog.Debug("podman beegfs run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating BeeGFS %s container: %w", role, err)
	}
	return nil
}

// startService starts one existing container (autostart/`pg addon start`):
// running → no-op; stopped → start; improper → recreate from config; missing
// → create fresh.
func (m *BeeGFSManager) startService(bc *config.BeeGFSConfig, role string) error {
	name := bc.ContainerName(role)
	running, err := m.containerRunning(name)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] BeeGFS %s already running\n", name)
		return nil
	}
	if exists, err := m.containerExists(name); err != nil {
		return err
	} else if exists {
		if _, err := m.run("start", name); err == nil {
			fmt.Printf("  [OK] BeeGFS %s started\n", name)
			return nil
		}
		fmt.Printf("  [!] BeeGFS %s in improper state, recreating...\n", name)
	}
	return m.createService(bc, role)
}

// StartCluster starts the three containers in dependency order
// (mgmtd → meta → storage). Unlike EnsureCluster it does not recreate running
// services or block on registration logs — autostart must be quick, and the
// services' own retries cover an mgmtd that takes a moment longer.
func (m *BeeGFSManager) StartCluster(bc *config.BeeGFSConfig) error {
	if platform.Detect() != platform.Linux {
		return hostNetUnsupported()
	}
	for _, role := range []string{BeeGFSServiceMgmtd, BeeGFSServiceMeta, BeeGFSServiceStorage} {
		if err := m.startService(bc, role); err != nil {
			return err
		}
	}
	return nil
}

// StopCluster stops the three containers in reverse dependency order
// (storage → meta → mgmtd): the data services are the ones with in-flight
// traffic, and a graceful shutdown keeps their targets cleanly GOOD.
func (m *BeeGFSManager) StopCluster(bc *config.BeeGFSConfig) error {
	var firstErr error
	for _, role := range []string{BeeGFSServiceStorage, BeeGFSServiceMeta, BeeGFSServiceMgmtd} {
		name := bc.ContainerName(role)
		if _, err := m.run("stop", name); err != nil {
			slog.Warn("beegfs: stop service failed", "service", role, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("stopping BeeGFS %s: %w", role, err)
			}
		}
	}
	return firstErr
}

// RemoveCluster removes the three containers in reverse order. Data dirs are
// kept by default (the SQLite DB and both targets outlive the containers — a
// reinstall under the same name revives the filesystem); cleanData also deletes
// all three, refusing any that is still a mount point.
func (m *BeeGFSManager) RemoveCluster(bc *config.BeeGFSConfig, cleanData bool) error {
	for _, role := range []string{BeeGFSServiceStorage, BeeGFSServiceMeta, BeeGFSServiceMgmtd} {
		name := bc.ContainerName(role)
		_, _ = m.run("stop", name)
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing BeeGFS container %s: %w", name, err)
		}
		fmt.Printf("  [OK] Removed BeeGFS container %s\n", name)
	}
	for _, role := range []string{BeeGFSServiceMgmtd, BeeGFSServiceMeta, BeeGFSServiceStorage} {
		dataDir := m.resolveDataDir(bc, role)
		if !cleanData {
			fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
			continue
		}
		if isMountpoint(dataDir) {
			fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
			continue
		}
		// The images run as root, so under rootful podman a plain os.RemoveAll
		// clears these; under rootless the dirs carry subordinate-uid files and
		// removeHostDir falls back to `podman unshare rm` (its last-resort temp
		// container is never reached here, same as every other root-image addon).
		img := beegfsImageRef(BeeGFSServiceMgmtd, bc.ImageTag)
		if err := removeHostDir(m.podman, img, dataDir); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
		} else {
			fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
		}
	}
	if cleanData {
		base := filepath.Join(m.dataDir, "addon", "beegfs", bc.Name)
		_, _ = removeHostDirIfEmpty(m.podman, beegfsImageRef(BeeGFSServiceMgmtd, bc.ImageTag), base)
	}
	return nil
}

// --- Status / check -------------------------------------------------------

// ClusterState is the aggregate view of one filesystem's three containers.
type ClusterState struct {
	MgmtdUp   bool
	MetaUp    bool
	StorageUp bool
}

// String renders the aggregate the list/summary layers print: all up →
// "running", none → "stopped", anything between → "partial (mgmtd,meta)".
func (s ClusterState) String() string {
	switch {
	case s.AllUp():
		return "running"
	case s.AllDown():
		return "stopped"
	}
	var up []string
	if s.MgmtdUp {
		up = append(up, BeeGFSServiceMgmtd)
	}
	if s.MetaUp {
		up = append(up, BeeGFSServiceMeta)
	}
	if s.StorageUp {
		up = append(up, BeeGFSServiceStorage)
	}
	return "partial (" + strings.Join(up, ",") + ")"
}

func (s ClusterState) AllUp() bool   { return s.MgmtdUp && s.MetaUp && s.StorageUp }
func (s ClusterState) AllDown() bool { return !s.MgmtdUp && !s.MetaUp && !s.StorageUp }

// ContainerExists reports whether one of the filesystem's containers is
// present (running or not) — the CLI's "complete cluster → start, incomplete →
// (re)create" gate.
func (m *BeeGFSManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// ClusterStatus reports which of the three containers are running.
func (m *BeeGFSManager) ClusterStatus(bc *config.BeeGFSConfig) (ClusterState, error) {
	var st ClusterState
	var err error
	if st.MgmtdUp, err = m.containerRunning(bc.ContainerName(BeeGFSServiceMgmtd)); err != nil {
		return st, err
	}
	if st.MetaUp, err = m.containerRunning(bc.ContainerName(BeeGFSServiceMeta)); err != nil {
		return st, err
	}
	st.StorageUp, err = m.containerRunning(bc.ContainerName(BeeGFSServiceStorage))
	return st, err
}

// CheckServers reports cluster health without any external tooling. None of the
// three images ships beegfs-ctl/check-servers, and the modern `beegfs` gRPC CLI
// lives only in the host's beegfs-tools package — which pgcli never installs
// (that would drag a kernel-module package onto the host). So the authoritative
// signal is what the daemons already log: mgmtd's target-registration line and
// meta/storage's "Registration ... complete" line. Each is read from that
// service's container logs, so check works the instant the containers are up
// and needs nothing beyond podman.
func (m *BeeGFSManager) CheckServers(bc *config.BeeGFSConfig) (string, error) {
	st, err := m.ClusterStatus(bc)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "BeeGFS %s: %s\n", bc.Name, st)
	for _, role := range []string{BeeGFSServiceMgmtd, BeeGFSServiceMeta, BeeGFSServiceStorage} {
		up, _ := m.containerRunning(bc.ContainerName(role))
		reg := "no"
		// podman's --tail takes an INT only (docker's `all` is invalid syntax
		// here), and there is no "everything" sentinel — a plain `logs` dump of
		// a just-started daemon is exactly the bounded output we probe.
		logs, lerr := m.runCombined("logs", bc.ContainerName(role))
		if lerr == nil {
			switch role {
			case BeeGFSServiceMgmtd:
				if strings.Contains(logs, beegfsMgmtdReadyLog) {
					reg = "serving"
				}
			default:
				if strings.Contains(logs, beegfsRegisteredLogMark) {
					reg = "registered"
				}
			}
		}
		state := "down"
		if up {
			state = "up"
		}
		fmt.Fprintf(&b, "  %-8s %-4s %s\n", role, state, reg)
	}
	return b.String(), nil
}

// --- Internal helpers -----------------------------------------------------

// waitForLog polls one service container's logs for a readiness marker,
// failing fast if the container dies (a crashed daemon cannot become ready).
func (m *BeeGFSManager) waitForLog(bc *config.BeeGFSConfig, role, marker string) error {
	name := bc.ContainerName(role)
	deadline := time.Now().Add(beegfsSetupTimeout)
	for {
		// Re-read the log each tick — via runCombined (the daemons' output is
		// on container stderr, which podman forwards to host stderr, so an
		// Output()-only capture sees nothing) and WITHOUT `-f`: a follow stream
		// never reaches EOF, so a capturing call would block instead of
		// sampling.
		logs, err := m.runCombined("logs", name)
		if err == nil && strings.Contains(logs, marker) {
			return nil
		}
		if running, rerr := m.containerRunning(name); rerr == nil && !running {
			tail := logs
			if len(tail) > 400 {
				tail = tail[len(tail)-400:]
			}
			return fmt.Errorf("BeeGFS %s exited before becoming ready; last logs:\n%s", role, tail)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for BeeGFS %s log %q (timeout %s)", role, marker, beegfsSetupTimeout)
		}
		time.Sleep(2 * time.Second)
	}
}

func (m *BeeGFSManager) run(args ...string) (string, error) {
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

// runCombined captures stdout AND stderr merged — the one form `podman logs`
// needs. The daemons log to stderr (--log-target stderr), and podman forwards a
// container's stderr to the host's stderr, so run()'s cmd.Output() (stdout
// only) would return an EMPTY string for every daemon log line and make
// readiness un-probeable. Errors carry no separate-stderr extraction here: the
// combined stream already holds whatever diagnostic exists.
func (m *BeeGFSManager) runCombined(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (m *BeeGFSManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image ref is present locally.
func (m *BeeGFSManager) imageExists(ref string) (bool, error) {
	out, err := m.run("images", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == ref {
			return true, nil
		}
	}
	return false, nil
}

func (m *BeeGFSManager) containerExists(name string) (bool, error) {
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

func (m *BeeGFSManager) containerRunning(name string) (bool, error) {
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
