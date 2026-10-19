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
// this manager brings up, tears down, and reports on the ordered containers
// THIS host runs (bc.Roles — all three on a v1 converged host, a subset on a
// v2 cross-host member):
//
//	pgcli-beegfs<ns>-<name>-mgmtd   → management daemon (node/target registry)
//	pgcli-beegfs<ns>-<name>-meta    → metadata service
//	pgcli-beegfs<ns>-<name>-storage → storage service (one or more targets)
//
// v2 cross-host: pgcli NEVER SSHes anywhere. Each host runs its own local
// `pg addon install beegfs --roles <subset> --mgmtd-host <ip>` and the daemons
// self-register into mgmtd over the network (mgmtd IS the shared registry).
// A host may give its storage container several target dirs (one disk each).
//
// The three official images (ghcr.io/thinkparq/beegfs-{mgmtd,meta,storage},
// config.BeeGFSImageRepo + DefaultBeeGFSVersion) share one mechanism: their
// baked entrypoint (start.sh → init.py) reads lowercase `beegfs_setup_N`
// env vars and runs each as a `beegfs-setup-<service>` command on FIRST boot
// only (idempotency marker <target>/alreadyInitByContainer in the bind mount),
// then execs the daemon with the remaining container args. init.py's invariant
// "number of setup commands == number of targets" is satisfied by construction
// here — the storage builder emits exactly one setup env and one mount per
// target dir.
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
//     pgcli allocated. EnsureCluster enforces that one-filesystem rule; a
//     single filesystem spanning hosts is fine because each host binds its own
//     copies of the fixed ports.
//   - No privileged mode, no capability additions, no chown. Pure
//     `--network host` + `:z` bind mounts, verified end-to-end on vm01
//     (no RDMA in v1). The images run as root and own their data dirs —
//     unlike Gitea's uid-1000 or Grafana's uid-472 pre-chown dance.
//   - Readiness is log-probed, not exec-probed. None of the three images
//     ships beegfs-ctl or check-servers (only the new `beegfs` gRPC CLI, and
//     only mgmtd's image can meaningfully run it against itself). meta and
//     storage each log "Registration ... complete" once they have reached
//     mgmtd — that line doubles as "mgmtd is up AND this service joined the
//     cluster", which is exactly the ordering gate EnsureCluster needs. A
//     join rejected for a wrong secret shows up as a hard disconnect during
//     its Register* handshake (the "wrong secret" line itself is only in the
//     mgmtd's log), which waitForLog turns into a fast, actionable error.
//
// TLS and licensing: the mgmtd daemon enforces gRPC TLS by default; v1 passes
// --tls-disable true (operator's explicit choice — the cluster is only usable
// with a matching --tls-disable on the client side too). A BeeGFS license is a
// system-specific registration the user obtains from ThinkParQ; pgcli never
// generates one but carries the user's PEM (BeeGFSConfig.License) to the mgmtd
// container as BEEGFS_LICENSE_FILE_DATA — mgmtd alone ships
// libbeegfs_license.so. An unlicensed 8.4 mgmtd warns on every command and
// caps client mounts at five.
type BeeGFSManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

// BeeGFS service roles. The constants are also the container-name suffixes and
// the --service values of `pg logs addon beegfs`. The canonical definitions
// live in config (the Roles field validates against them); these aliases keep
// the podman-side call sites unchanged.
const (
	BeeGFSServiceMgmtd   = config.BeeGFSServiceMgmtd
	BeeGFSServiceMeta    = config.BeeGFSServiceMeta
	BeeGFSServiceStorage = config.BeeGFSServiceStorage
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

	// Join-rejection signature, verified on a real cross-host wrong-secret join
	// (vm02 -> vm01): the JOINING daemon logs a hard disconnect (connection
	// reset by mgmtd) while its Register/RegisterTarget message is in flight —
	// the "tried to authenticate stream with wrong secret" line itself appears
	// only in the *mgmtd's* log, which a join host cannot read. waitForLog
	// greps the joiner's own log for the two markers so a wrong secret fails in
	// seconds with the fix, instead of the 60 s timeout.
	beegfsJoinRejectedDisc = "Hard disconnect"
	beegfsJoinRejectedMsg  = "Message type: Regis"

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
// host network, the setup env var(s) plus the shared connection-auth secret,
// one :z bind mount per target dir, --restart unless-stopped, then the image
// ref followed by the daemon args — exactly the vm01-proven invocation (podman
// stops flag parsing at the image, so the mgmtd `--db-file ...` and
// meta/storage `key=value ...` tokens become the container command with no
// separator).

// beegfsNodeID is the effective -s node id (0 normalises to 1, the v1 value).
func beegfsNodeID(bc *config.BeeGFSConfig) int {
	if bc.NodeID == 0 {
		return 1
	}
	return bc.NodeID
}

// beegfsTargetID derives the i-th (1-based) storage target id from the node
// id: nodeID*100 + i. v1's fixed 101 is exactly node 1 target 1, and BeeGFS
// target ids are max 65535, which config.Validate bounds nodeID 1..655 for.
func beegfsTargetID(nodeID, i int) int {
	return nodeID*100 + i
}

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
	return beegfsRunArgs(bc, BeeGFSServiceMgmtd, []string{dataDir}, []string{beegfsContainerDataRoot + "/" + BeeGFSServiceMgmtd}, []string{setup}, daemonArgs)
}

// beegfsMetaArgsAndEnv builds the meta service argv. NodeIDs are a per
// service-type namespace (verified on vm01: meta -s 1 and storage -s 1 coexist
// as node_meta_1 and node_storage_1), so a single-host v1 uses 1 and v2
// defaults there too; bc.NodeID overrides per host. The daemon args speak
// key=value config overrides: sysMgmtdHost points at mgmtd — MgmtdHost when the
// daemon lives on another host (v2 join), else this host's own Listen — and a
// non-default beemsg port is followed by connMgmtdPort, which matters even
// MORE cross-host (the join host must dial the moved port on the remote).
func beegfsMetaArgsAndEnv(bc *config.BeeGFSConfig, dataDir string) []string {
	const tgt = beegfsContainerDataRoot + "/" + BeeGFSServiceMeta
	nodeID := beegfsNodeID(bc)
	setup := fmt.Sprintf("beegfs-setup-meta -C -p %s -s %d", tgt, nodeID)
	daemonArgs := []string{
		"sysMgmtdHost=" + beegfsMgmtdTarget(bc),
		"storeAllowFirstRunInit=false",
		"storeMetaDirectory=" + tgt,
	}
	if bc.MgmtdPort != 0 && bc.MgmtdPort != config.DefaultBeeGFSMgmtdPort {
		daemonArgs = append(daemonArgs, "connMgmtdPort="+strconv.Itoa(bc.MgmtdPort))
	}
	return beegfsRunArgs(bc, BeeGFSServiceMeta, []string{dataDir}, []string{tgt}, []string{setup}, daemonArgs)
}

// beegfsStorageArgsAndEnv builds the storage service argv — one container,
// one setup command PER target (init.py's invariant is setups == targets, and
// the official docs show exactly this beegfs_setup_1/-2 shape). hostDirs is
// the resolved per-target host dir list (length 1 = the v1 single-target
// shape). Container paths: a single target keeps /data/storage EXACTLY as v1
// mounted it — the target dir records its own path internally, so a revived
// v1 store must see the same mount point — while a multi-target host mounts
// each at /data/storage/<targetID> (only ever seen on v2-created stores).
// Target ids derive as nodeID*100+ordinal, so v1's single target is still 101.
func beegfsStorageArgsAndEnv(bc *config.BeeGFSConfig, hostDirs []string) []string {
	nodeID := beegfsNodeID(bc)
	var setups, contDirs []string
	for i := range hostDirs {
		cont := beegfsContainerDataRoot + "/" + BeeGFSServiceStorage
		if len(hostDirs) > 1 {
			cont = fmt.Sprintf("%s/%d", cont, beegfsTargetID(nodeID, i+1))
		}
		contDirs = append(contDirs, cont)
		setups = append(setups, fmt.Sprintf("beegfs-setup-storage -C -p %s -s %d -i %d", cont, nodeID, beegfsTargetID(nodeID, i+1)))
	}
	daemonArgs := []string{
		"sysMgmtdHost=" + beegfsMgmtdTarget(bc),
		"storeAllowFirstRunInit=false",
		"storeStorageDirectory=" + strings.Join(contDirs, ","),
	}
	if bc.MgmtdPort != 0 && bc.MgmtdPort != config.DefaultBeeGFSMgmtdPort {
		daemonArgs = append(daemonArgs, "connMgmtdPort="+strconv.Itoa(bc.MgmtdPort))
	}
	return beegfsRunArgs(bc, BeeGFSServiceStorage, hostDirs, contDirs, setups, daemonArgs)
}

// beegfsRunArgs is the shared argv skeleton the three builders feed.
// hostDirs/contDirs/setupCmds are parallel lists (length 1 for mgmtd/meta, N
// for a multi-target storage host): one -v and one beegfs_setup_<i> each,
// numbered from 1 exactly like the official env-var scheme.
func beegfsRunArgs(bc *config.BeeGFSConfig, role string, hostDirs, contDirs, setupCmds, daemonArgs []string) []string {
	args := []string{
		"run", "-d",
		"--name", bc.ContainerName(role),
		"--network", "host",
	}
	for i, setup := range setupCmds {
		args = append(args, "-e", fmt.Sprintf("beegfs_setup_%d=%s", i+1, setup))
	}
	args = append(args,
		"-e", "CONN_AUTH_FILE_DATA="+bc.ConnAuth,
	)
	// The license rides only to mgmtd: its image is the only one shipping
	// libbeegfs_license.so, and its start.sh wires BEEGFS_LICENSE_FILE_DATA →
	// /etc/beegfs/license.pem → --license-cert-file for beegfs-mgmtd alone.
	if role == BeeGFSServiceMgmtd && bc.License != "" {
		args = append(args, "-e", "BEEGFS_LICENSE_FILE_DATA="+bc.License)
	}
	for i := range hostDirs {
		args = append(args, "-v", hostMountPath(hostDirs[i])+":"+contDirs[i]+":z")
	}
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		beegfsImageRef(role, bc.ImageTag),
	)
	return append(args, daemonArgs...)
}

// beegfsMgmtdTarget is the sysMgmtdHost value meta/storage dial: the stored
// MgmtdHost for a v2 join host, otherwise this host's self-reference (loopback
// for a wildcard Listen — unchanged v1 behavior when mgmtd runs here).
func beegfsMgmtdTarget(bc *config.BeeGFSConfig) string {
	if bc.MgmtdHost != "" {
		return bc.MgmtdHost
	}
	return mgmtdHost(bc.Listen)
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
		return m.storageTargetHostDirs(bc)[0]
	}
}

// storageTargetHostDirs resolves the host dir of every storage target on this
// host: bc.StorageTargetDirs with empty entries defaulted (the single-target
// default is the v1 base/storage dir exactly; extra targets get base/storage-N
// — a list with multiple entries can only come from v2 flags anyway). Empty
// list defaults to one dir, keeping the v1 shape.
func (m *BeeGFSManager) storageTargetHostDirs(bc *config.BeeGFSConfig) []string {
	base := filepath.Join(m.dataDir, "addon", "beegfs", bc.Name)
	dirs := bc.StorageTargetDirs
	if len(dirs) == 0 {
		dirs = []string{bc.StorageDataDir}
	}
	out := make([]string, 0, len(dirs))
	for i, d := range dirs {
		if d != "" {
			out = append(out, d)
			continue
		}
		if i == 0 {
			out = append(out, filepath.Join(base, BeeGFSServiceStorage))
		} else {
			out = append(out, filepath.Join(base, fmt.Sprintf("%s-%d", BeeGFSServiceStorage, i+1)))
		}
	}
	return out
}

// DataDir returns one service's resolved host data directory (for display).
func (m *BeeGFSManager) DataDir(bc *config.BeeGFSConfig, role string) string {
	return m.resolveDataDir(bc, role)
}

// DataDirs returns every host data directory of one service — one for
// mgmtd/meta, N for a multi-target storage host (display + removal).
func (m *BeeGFSManager) DataDirs(bc *config.BeeGFSConfig, role string) []string {
	if role == BeeGFSServiceStorage {
		return m.storageTargetHostDirs(bc)
	}
	return []string{m.resolveDataDir(bc, role)}
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

// EnsureImage pulls this host's role images if missing (pull-only public ghcr
// images). All needed images are pulled up front so a mid-cluster network
// failure never leaves a half-registered filesystem. A join host only pulls the
// roles it runs, not mgmtd.
func (m *BeeGFSManager) EnsureImage(bc *config.BeeGFSConfig) error {
	for _, role := range bc.RolesInOrder() {
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

	// Bring up exactly the roles THIS host runs (v2): the full set is the v1
	// mgmtd → meta → storage sequence; a join host skips the earlier stages
	// and just registers its own services against the remote mgmtd. Each
	// meta/storage gate (the registration log line) doubles as "mgmtd is
	// reachable AND this service joined", so no separate mgmtd probe is needed.
	for _, role := range bc.RolesInOrder() {
		fmt.Printf("-> Starting BeeGFS %s service...\n", role)
		if err := m.createService(bc, role); err != nil {
			return err
		}
		marker := beegfsRegisteredLogMark
		if role == BeeGFSServiceMgmtd {
			marker = beegfsMgmtdReadyLog
		}
		if err := m.waitForLog(bc, role, marker); err != nil {
			return err
		}
	}

	fmt.Printf("  [OK] BeeGFS %s up on this host (roles: %s)\n", bc.Name, strings.Join(bc.RolesInOrder(), ","))
	return nil
}

// guardSingleFilesystem rejects a second filesystem on this host: meta and
// storage daemons have no listen-port override (verified on vm01), so a second
// one would collide on the fixed 8005/8003 and crash-loop. Cross-host scaling is
// the answer — spread roles across hosts with --roles, not a second filesystem
// here. The guard compares the persisted config map so a stale config cannot
// sneak a second entry in; re-ensuring the SAME filesystem name is allowed.
func (m *BeeGFSManager) guardSingleFilesystem(bc *config.BeeGFSConfig) error {
	for name, other := range m.cfg.Addons.BeeGFS {
		if name == bc.Name || other.Name == bc.Name {
			continue
		}
		return fmt.Errorf("a BeeGFS filesystem already exists on this host (%q): BeeGFS 8.4's meta and storage daemons bind fixed ports 8005/8003 with no supported override, so pgcli supports exactly one filesystem per host — to grow, add roles on ANOTHER host (pg addon install beegfs --name <fs> --roles storage --mgmtd-host <ip> --node-id <n> --password <conn-auth>), or remove the first here", name)
	}
	return nil
}

// createService (re)creates one service container: an existing container is
// stopped and removed first so updated config always takes effect (install
// semantics). Every target dir it will mount is created if missing, never
// deleted here.
func (m *BeeGFSManager) createService(bc *config.BeeGFSConfig, role string) error {
	name := bc.ContainerName(role)
	if exists, err := m.containerExists(name); err != nil {
		return err
	} else if exists {
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing stale BeeGFS container %s: %w", name, err)
		}
	}
	var args []string
	switch role {
	case BeeGFSServiceMgmtd:
		dir := m.resolveDataDir(bc, role)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating BeeGFS data dir %s: %w", dir, err)
		}
		args = beegfsMgmtdArgsAndEnv(bc, dir)
	case BeeGFSServiceMeta:
		dir := m.resolveDataDir(bc, role)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating BeeGFS data dir %s: %w", dir, err)
		}
		args = beegfsMetaArgsAndEnv(bc, dir)
	case BeeGFSServiceStorage:
		dirs := m.storageTargetHostDirs(bc)
		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("creating BeeGFS data dir %s: %w", dir, err)
			}
		}
		args = beegfsStorageArgsAndEnv(bc, dirs)
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

// RestartRole recreates ONE role's container from the CURRENT config — the
// license hot-swap path: the env only lands on a container at creation, so
// swapping the certificate means rebuilding mgmtd. The data dirs and the
// idempotent setup markers make a recreate cheap, and every other daemon
// re-registers on its own (BeeGFS services retry mgmtd contact indefinitely),
// so one role can be bounced without touching the rest of the cluster.
func (m *BeeGFSManager) RestartRole(bc *config.BeeGFSConfig, role string) error {
	if err := m.createService(bc, role); err != nil {
		return err
	}
	marker := beegfsRegisteredLogMark
	if role == BeeGFSServiceMgmtd {
		marker = beegfsMgmtdReadyLog
	}
	return m.waitForLog(bc, role, marker)
}

// StartCluster starts this host's role containers in dependency order
// (mgmtd → meta → storage). Unlike EnsureCluster it does not recreate running
// services or block on registration logs — autostart must be quick, and the
// services' own retries cover an mgmtd that takes a moment longer (including a
// REMOTE one on a v2 join host, which is exactly why registration waiting is
// skipped here).
func (m *BeeGFSManager) StartCluster(bc *config.BeeGFSConfig) error {
	if platform.Detect() != platform.Linux {
		return hostNetUnsupported()
	}
	for _, role := range bc.RolesInOrder() {
		if err := m.startService(bc, role); err != nil {
			return err
		}
	}
	return nil
}

// StopCluster stops this host's services in reverse dependency order
// (storage → meta → mgmtd): the data services are the ones with in-flight
// traffic, and a graceful shutdown keeps their targets cleanly GOOD.
func (m *BeeGFSManager) StopCluster(bc *config.BeeGFSConfig) error {
	var firstErr error
	roles := bc.RolesInOrder()
	for i := len(roles) - 1; i >= 0; i-- {
		role := roles[i]
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

// RemoveCluster removes this host's containers in reverse order. Data dirs are
// kept by default (the SQLite DB and every target outlive the containers — a
// reinstall under the same name revives the filesystem); cleanData also deletes
// them all, refusing any that is still a mount point.
func (m *BeeGFSManager) RemoveCluster(bc *config.BeeGFSConfig, cleanData bool) error {
	roles := bc.RolesInOrder()
	for i := len(roles) - 1; i >= 0; i-- {
		role := roles[i]
		name := bc.ContainerName(role)
		_, _ = m.run("stop", name)
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing BeeGFS container %s: %w", name, err)
		}
		fmt.Printf("  [OK] Removed BeeGFS container %s\n", name)
	}
	for _, role := range roles {
		for _, dataDir := range m.DataDirs(bc, role) {
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
	}
	if cleanData {
		base := filepath.Join(m.dataDir, "addon", "beegfs", bc.Name)
		_, _ = removeHostDirIfEmpty(m.podman, beegfsImageRef(BeeGFSServiceMgmtd, bc.ImageTag), base)
	}
	return nil
}

// --- Status / check -------------------------------------------------------

// ClusterState is the aggregate view of one filesystem's containers PRESENT ON
// THIS HOST (v2: a join host only has its own roles; a v1 host has all three).
// The bc pointer carries which roles those are; a zero/nil-bc state (hand-built
// in tests or legacy callers) covers the full trio.
type ClusterState struct {
	MgmtdUp   bool
	MetaUp    bool
	StorageUp bool
	bc        *config.BeeGFSConfig
}

// present returns the roles this state covers.
func (s ClusterState) present() []string {
	if s.bc != nil {
		return s.bc.RolesInOrder()
	}
	return []string{BeeGFSServiceMgmtd, BeeGFSServiceMeta, BeeGFSServiceStorage}
}

// Up reports whether the given present role's container is running.
func (s ClusterState) Up(role string) bool {
	switch role {
	case BeeGFSServiceMgmtd:
		return s.MgmtdUp
	case BeeGFSServiceMeta:
		return s.MetaUp
	default:
		return s.StorageUp
	}
}

// upOrder lists the running services in canonical order.
func (s ClusterState) upOrder() []string {
	var up []string
	for _, role := range s.present() {
		if s.Up(role) {
			up = append(up, role)
		}
	}
	return up
}

// String renders the aggregate the list/summary layers print, counting only
// the roles present on this host: all up → "running", none → "stopped",
// anything between → "partial (mgmtd,meta)".
func (s ClusterState) String() string {
	up := s.upOrder()
	switch {
	case len(up) == 0:
		return "stopped"
	case len(up) == len(s.present()):
		return "running"
	}
	return "partial (" + strings.Join(up, ",") + ")"
}

// AllUp/AllDown compare against the roles this host runs — on a v1 host that is
// the full trio, so v1 semantics are unchanged; a join host is "running" when
// exactly its own services are up.
func (s ClusterState) AllUp() bool {
	return len(s.upOrder()) == len(s.present()) && len(s.present()) > 0
}
func (s ClusterState) AllDown() bool { return len(s.upOrder()) == 0 }

// ContainerExists reports whether one of the filesystem's containers is
// present (running or not) — the CLI's "complete cluster → start, incomplete →
// (re)create" gate.
func (m *BeeGFSManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// ClusterStatus reports which of THIS host's role containers are running.
func (m *BeeGFSManager) ClusterStatus(bc *config.BeeGFSConfig) (ClusterState, error) {
	st := ClusterState{bc: bc}
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
	for _, role := range bc.RolesInOrder() {
		up := st.Up(role)
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

// RoleLogs returns one role container's combined logs (bounded by podman's
// default tail) — what `pg addon beegfs license` greps for the certificate
// verification lines mgmtd logs at startup.
func (m *BeeGFSManager) RoleLogs(bc *config.BeeGFSConfig, role string) (string, error) {
	return m.runCombined("logs", bc.ContainerName(role))
}

// --- Internal helpers -----------------------------------------------------

// waitForLog polls one service container's logs for a readiness marker, failing
// fast if the container dies (a crashed daemon cannot become ready) or logs a
// conn-auth mismatch (a join host whose --password differs from the mgmtd's is
// a config mistake, not something to wait out — report it in seconds with the
// fix, since the daemon retries silently and the 60 s timeout would otherwise
// be the only signal).
func (m *BeeGFSManager) waitForLog(bc *config.BeeGFSConfig, role, marker string) error {
	name := bc.ContainerName(role)
	watchJoin := marker == beegfsRegisteredLogMark
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
		if watchJoin && err == nil && strings.Contains(logs, beegfsJoinRejectedDisc) &&
			strings.Contains(logs, beegfsJoinRejectedMsg) {
			return fmt.Errorf("BeeGFS %s was rejected by mgmtd: it connected and its registration was reset, which means the connection-auth secret does not match — retrieve the cluster's value on the mgmtd host with `pg addon password beegfs --name %s` and reinstall with --password", role, bc.Name)
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
