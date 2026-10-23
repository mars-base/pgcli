package podman

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// VMClusterManager manages a VictoriaMetrics cluster addon — three components
// (vminsert, vmselect, vmstorage) each running as a separate container. Unlike
// the single-node VictoriaMetricsManager (one container, one image), this
// manages up to three containers with three separate images and five ports.
//
// The design mirrors BeeGFS's cross-host model:
//   - Roles selects which components THIS host runs (insert, select, storage).
//   - StorageNodes lists ALL vmstorage addresses for sharding and replication.
//   - pgcli never SSHes — each host runs its own `pg addon install vmcluster`.
//
// Key differences from BeeGFS:
//   - No log-based readiness probe: VictoriaMetrics exposes /-/healthy HTTP
//     endpoints; for v1 we just start the containers and trust they come up.
//   - Only vmstorage has persistent data; vminsert and vmselect are stateless.
//   - No conn-auth / license / setup env vars — pure CLI flags after the image.
//   - Host networking only on Linux (refused on macOS, same as BeeGFS).
//
// The images run as root, so no pre-chown of the data dir is needed (same as
// the single-node VictoriaMetrics).
type VMClusterManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

const (
	// vmstorageContainerDataDir is the data location vmstorage uses internally;
	// pgcli bind-mounts the host data dir here.
	vmstorageContainerDataDir = "/storage"
)

// NewVMClusterManager creates a VMClusterManager. VMCluster requires host
// networking (Linux only) — on macOS the manager constructs normally but every
// cluster operation refuses with a clear error.
func NewVMClusterManager(cfg *config.Config) (*VMClusterManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &VMClusterManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
	}, nil
}

// vmclusterHostNetUnsupported is the single refusal point for non-Linux
// platforms — VMCluster requires host networking for the internal TCP ports
// between vminsert/vmselect and vmstorage.
func vmclusterHostNetUnsupported() error {
	return errors.New("VMCluster requires host networking, which podman only provides on Linux")
}

// --- Data dir resolution --------------------------------------------------

// resolveDataDir returns the vmstorage host data dir: the explicit
// StorageDataDir override when set, otherwise <base>/addon/vmcluster/<name>/data.
// Only vmstorage has persistent data; vminsert and vmselect are stateless.
func (m *VMClusterManager) resolveDataDir(vc *config.VMClusterConfig) string {
	if vc.StorageDataDir != "" {
		return vc.StorageDataDir
	}
	return filepath.Join(m.dataDir, "addon", "vmcluster", vc.Name, "data")
}

// DataDir returns the resolved vmstorage host data directory (for display).
func (m *VMClusterManager) DataDir(vc *config.VMClusterConfig) string {
	return m.resolveDataDir(vc)
}

// --- Pure argv builders ---------------------------------------------------
//
// All three share the same skeleton: run detached with a role-suffixed name,
// host network, --restart unless-stopped, then the image ref followed by the
// daemon CLI flags. Only vmstorage has a bind mount (data dir).

// vmstorageArgs builds the `podman run` argv for vmstorage. It binds the host
// data dir at /storage and passes -storageDataPath, -httpListenAddr,
// -vminsertAddr, -vmselectAddr as CLI flags after the image. Retention and
// StorageExtraArgs follow.
func vmstorageArgs(vc *config.VMClusterConfig, dataDir string) []string {
	args := []string{
		"run", "-d",
		"--name", vc.ContainerName(config.VMClusterRoleStorage),
		"--network", "host",
	}
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), vmstorageContainerDataDir),
	)
	args = append(args, vc.StorageImageTag)
	args = append(args,
		"-storageDataPath="+vmstorageContainerDataDir,
		fmt.Sprintf("-httpListenAddr=%s:%d", vc.Listen, vc.StoragePort),
		fmt.Sprintf("-vminsertAddr=:%d", vc.InternalInsertPort),
		fmt.Sprintf("-vmselectAddr=:%d", vc.InternalSelectPort),
	)
	if vc.Retention != "" {
		args = append(args, "-retentionPeriod="+vc.Retention)
	}
	// StorageExtraArgs LAST — last-wins semantics.
	args = append(args, vc.StorageExtraArgs...)
	return args
}

// vminsertArgs builds the `podman run` argv for vminsert — stateless, so no
// bind mount. It passes -httpListenAddr and one -storageNode per vmstorage
// (the horizontal-scaling core). ReplicationFactor is passed when > 1.
func vminsertArgs(vc *config.VMClusterConfig) []string {
	args := []string{
		"run", "-d",
		"--name", vc.ContainerName(config.VMClusterRoleInsert),
		"--network", "host",
	}
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
	)
	args = append(args, vc.InsertImageTag)
	args = append(args,
		fmt.Sprintf("-httpListenAddr=%s:%d", vc.Listen, vc.InsertPort),
	)
	// One -storageNode per vmstorage — the sharding/replication mechanism.
	for _, sn := range vc.EffectiveStorageNodes() {
		args = append(args, "-storageNode="+sn.Address)
	}
	if vc.ReplicationFactor > 1 {
		args = append(args, fmt.Sprintf("-replicationFactor=%d", vc.ReplicationFactor))
	}
	// InsertExtraArgs LAST — last-wins semantics.
	args = append(args, vc.InsertExtraArgs...)
	return args
}

// vmselectArgs builds the `podman run` argv for vmselect — stateless, no bind
// mount. It passes -httpListenAddr and one -storageNode per vmstorage. Unlike
// vminsert (which uses Address directly = host:InternalInsertPort), vmselect
// needs host:SelectPort (the select protocol port on the same vmstorage host).
func vmselectArgs(vc *config.VMClusterConfig) []string {
	args := []string{
		"run", "-d",
		"--name", vc.ContainerName(config.VMClusterRoleSelect),
		"--network", "host",
	}
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
	)
	args = append(args, vc.SelectImageTag)
	args = append(args,
		fmt.Sprintf("-httpListenAddr=%s:%d", vc.Listen, vc.SelectPort),
	)
	// vmselect dials vmstorage on host:SelectPort (select protocol).
	for _, sn := range vc.EffectiveStorageNodes() {
		host := vmclusterHostFromAddress(sn.Address)
		args = append(args, fmt.Sprintf("-storageNode=%s:%d", host, sn.SelectPort))
	}
	// SelectExtraArgs LAST — last-wins semantics.
	args = append(args, vc.SelectExtraArgs...)
	return args
}

// vmclusterHostFromAddress extracts the host part from a VMClusterStorageNode
// Address (format "host:port"). Uses LastIndex to handle IPv6 bracket notation
// if it ever appears, though v1 uses plain IPv4.
func vmclusterHostFromAddress(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[:i]
	}
	return addr
}

// --- Image management -----------------------------------------------------

// EnsureImages pulls the images for the roles THIS host runs (not all three —
// a storage-only host does not need vminsert/vmselect images).
func (m *VMClusterManager) EnsureImages(vc *config.VMClusterConfig) error {
	images := m.roleImages(vc)
	for _, img := range images {
		exists, err := m.imageExists(img)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		fmt.Printf("-> Pulling image %s...\n", img)
		if err := m.runInteractive("pull", img); err != nil {
			return fmt.Errorf("pulling VMCluster image %s (is docker.io reachable?): %w", img, err)
		}
		fmt.Println("  [OK] Image pulled")
	}
	return nil
}

// roleImages returns the image tags for the roles this host runs.
func (m *VMClusterManager) roleImages(vc *config.VMClusterConfig) []string {
	var imgs []string
	for _, role := range vc.RolesInOrder() {
		switch role {
		case config.VMClusterRoleInsert:
			imgs = append(imgs, vc.InsertImageTag)
		case config.VMClusterRoleSelect:
			imgs = append(imgs, vc.SelectImageTag)
		case config.VMClusterRoleStorage:
			imgs = append(imgs, vc.StorageImageTag)
		}
	}
	return imgs
}

// --- Cluster lifecycle ----------------------------------------------------

// EnsureCluster creates (or recreates) the containers for the roles THIS host
// runs, in dependency order: storage → insert → select. vmstorage is created
// first (and its data dir MkdirAll'd) so vminsert/vmselect have something to
// dial. No readiness probe — VictoriaMetrics components start fast and the
// health endpoints are available within seconds.
func (m *VMClusterManager) EnsureCluster(vc *config.VMClusterConfig) error {
	if platform.Detect() != platform.Linux {
		return vmclusterHostNetUnsupported()
	}

	for _, role := range vc.RolesInOrder() {
		fmt.Printf("-> Starting VMCluster %s service...\n", role)
		if err := m.createService(vc, role); err != nil {
			return err
		}
	}

	fmt.Printf("  [OK] VMCluster %s up on this host (roles: %s)\n", vc.Name, strings.Join(vc.RolesInOrder(), ", "))
	return nil
}

// createService (re)creates one role's container: an existing container is
// stopped and removed first so updated config always takes effect (install
// semantics). The data dir is created for vmstorage only.
func (m *VMClusterManager) createService(vc *config.VMClusterConfig, role string) error {
	name := vc.ContainerName(role)
	if exists, err := m.containerExists(name); err != nil {
		return err
	} else if exists {
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing stale VMCluster container %s: %w", name, err)
		}
	}

	var args []string
	switch role {
	case config.VMClusterRoleStorage:
		dir := m.resolveDataDir(vc)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating VMCluster data dir %s: %w", dir, err)
		}
		args = vmstorageArgs(vc, dir)
	case config.VMClusterRoleInsert:
		args = vminsertArgs(vc)
	case config.VMClusterRoleSelect:
		args = vmselectArgs(vc)
	default:
		return fmt.Errorf("unknown VMCluster role %q", role)
	}

	slog.Debug("podman vmcluster run", "role", role, "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating VMCluster %s container: %w", role, err)
	}
	return nil
}

// startService starts one existing container (autostart/`pg addon start`):
// running → no-op; stopped → start; improper → recreate from config; missing
// → create fresh.
func (m *VMClusterManager) startService(vc *config.VMClusterConfig, role string) error {
	name := vc.ContainerName(role)
	running, err := m.containerRunning(name)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] VMCluster %s already running\n", name)
		return nil
	}
	if exists, err := m.containerExists(name); err != nil {
		return err
	} else if exists {
		if _, err := m.run("start", name); err == nil {
			fmt.Printf("  [OK] VMCluster %s started\n", name)
			return nil
		}
		fmt.Printf("  [!] VMCluster %s in improper state, recreating...\n", name)
	}
	return m.createService(vc, role)
}

// StartCluster starts this host's role containers in dependency order
// (storage → insert → select). Unlike EnsureCluster it does not recreate
// running services — autostart must be quick.
func (m *VMClusterManager) StartCluster(vc *config.VMClusterConfig) error {
	if platform.Detect() != platform.Linux {
		return vmclusterHostNetUnsupported()
	}
	for _, role := range vc.RolesInOrder() {
		if err := m.startService(vc, role); err != nil {
			return err
		}
	}
	return nil
}

// StopCluster stops this host's services in reverse dependency order
// (select → insert → storage): the query/ingestion services are stopped first
// so in-flight traffic drains before vmstorage goes down.
func (m *VMClusterManager) StopCluster(vc *config.VMClusterConfig) error {
	var firstErr error
	roles := vc.RolesInOrder()
	for i := len(roles) - 1; i >= 0; i-- {
		role := roles[i]
		name := vc.ContainerName(role)
		if _, err := m.run("stop", name); err != nil {
			slog.Warn("vmcluster: stop service failed", "service", role, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("stopping VMCluster %s: %w", role, err)
			}
		}
	}
	return firstErr
}

// RemoveCluster removes this host's containers in reverse order. Data dirs are
// kept by default (vmstorage data outlives the containers — a reinstall under
// the same name revives the stored time series); cleanData also deletes them.
func (m *VMClusterManager) RemoveCluster(vc *config.VMClusterConfig, cleanData bool) error {
	roles := vc.RolesInOrder()
	for i := len(roles) - 1; i >= 0; i-- {
		role := roles[i]
		name := vc.ContainerName(role)
		_, _ = m.run("stop", name)
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing VMCluster container %s: %w", name, err)
		}
		fmt.Printf("  [OK] Removed VMCluster container %s\n", name)
	}

	// Only vmstorage has persistent data.
	dataDir := m.resolveDataDir(vc)
	if vc.RunsRole(config.VMClusterRoleStorage) {
		if !cleanData {
			fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		} else {
			if isMountpoint(dataDir) {
				fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
			} else {
				// The image runs as root, so under rootless podman the mapped-uid
				// data dir is reclaimed through removeHostDir's `podman unshare rm`
				// fallback.
				if err := removeHostDir(m.podman, vc.StorageImageTag, dataDir); err != nil && !os.IsNotExist(err) {
					fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
				} else {
					fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
				}
			}
			if vc.StorageDataDir == "" {
				// Default layout: also prune the <name> dir once data/ is gone.
				_, _ = removeHostDirIfEmpty(m.podman, vc.StorageImageTag, filepath.Dir(dataDir))
			}
		}
	}
	return nil
}

// --- Status ---------------------------------------------------------------

// VMClusterState is the aggregate view of one cluster's containers PRESENT ON
// THIS HOST (a cross-host member only has its own roles).
type VMClusterState struct {
	InsertUp  bool
	SelectUp  bool
	StorageUp bool
	vc        *config.VMClusterConfig
}

// present returns the roles this state covers.
func (s VMClusterState) present() []string {
	if s.vc != nil {
		return s.vc.RolesInOrder()
	}
	return []string{config.VMClusterRoleStorage, config.VMClusterRoleInsert, config.VMClusterRoleSelect}
}

// Up reports whether the given role's container is running.
func (s VMClusterState) Up(role string) bool {
	switch role {
	case config.VMClusterRoleInsert:
		return s.InsertUp
	case config.VMClusterRoleSelect:
		return s.SelectUp
	default:
		return s.StorageUp
	}
}

// upOrder lists the running services in canonical order.
func (s VMClusterState) upOrder() []string {
	var up []string
	for _, role := range s.present() {
		if s.Up(role) {
			up = append(up, role)
		}
	}
	return up
}

// String renders the aggregate: all up → "running", none → "stopped",
// anything between → "partial (storage)".
func (s VMClusterState) String() string {
	up := s.upOrder()
	switch {
	case len(up) == 0:
		return "stopped"
	case len(up) == len(s.present()):
		return "running"
	}
	return "partial (" + strings.Join(up, ", ") + ")"
}

// AllUp reports whether all present roles are running.
func (s VMClusterState) AllUp() bool {
	return len(s.upOrder()) == len(s.present()) && len(s.present()) > 0
}

// AllDown reports whether no present roles are running.
func (s VMClusterState) AllDown() bool { return len(s.upOrder()) == 0 }

// ContainerExists reports whether a named container is present (running or
// not) — the CLI's "complete cluster → start, incomplete → (re)create" gate.
func (m *VMClusterManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// ClusterStatus reports which of THIS host's role containers are running.
func (m *VMClusterManager) ClusterStatus(vc *config.VMClusterConfig) (VMClusterState, error) {
	st := VMClusterState{vc: vc}
	var err error
	if vc.RunsRole(config.VMClusterRoleStorage) {
		if st.StorageUp, err = m.containerRunning(vc.ContainerName(config.VMClusterRoleStorage)); err != nil {
			return st, err
		}
	}
	if vc.RunsRole(config.VMClusterRoleInsert) {
		if st.InsertUp, err = m.containerRunning(vc.ContainerName(config.VMClusterRoleInsert)); err != nil {
			return st, err
		}
	}
	if vc.RunsRole(config.VMClusterRoleSelect) {
		if st.SelectUp, err = m.containerRunning(vc.ContainerName(config.VMClusterRoleSelect)); err != nil {
			return st, err
		}
	}
	return st, nil
}

// --- Internal helpers -----------------------------------------------------

func (m *VMClusterManager) run(args ...string) (string, error) {
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

func (m *VMClusterManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *VMClusterManager) imageExists(tag string) (bool, error) {
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

func (m *VMClusterManager) containerExists(name string) (bool, error) {
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

func (m *VMClusterManager) containerRunning(name string) (bool, error) {
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
