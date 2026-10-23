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

// VLClusterManager manages a VictoriaLogs cluster addon — vlinsert, vlselect,
// and vlstorage, all running from ONE image (docker.io/victoriametrics/victoria-logs)
// and differing only by flags. Unlike the single-node VictoriaLogsManager
// (one container, one port), this manages up to three containers and three
// HTTP ports per host.
//
// The design mirrors VMCluster's cross-host model:
//   - Roles selects which components THIS host runs (insert, select, storage).
//   - StorageNodes lists ALL vlstorage addresses (host:storagePort) for
//     sharding. vlinsert/vlselect dial the SAME port on each storage node —
//     the /internal/insert and /internal/select/* endpoints share it.
//   - pgcli never SSHes — each host runs its own `pg addon install vlcluster`.
//
// Key differences from VMCluster:
//   - One image for all roles instead of three per-role images.
//   - No internal TCP ports: storage nodes expose one HTTP port.
//   - vlinsert shards but does NOT replicate. There is no replication factor;
//     losing any vlstorage makes queries return 502.
//   - Only vlstorage has persistent data; vlinsert and vlselect are stateless.
//   - Host networking only on Linux (refused on macOS, same as VMCluster).
type VLClusterManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

// NewVLClusterManager creates a VLClusterManager. VLCluster requires host
// networking (Linux only) — on macOS the manager constructs normally but every
// cluster operation refuses with a clear error.
func NewVLClusterManager(cfg *config.Config) (*VLClusterManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &VLClusterManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
	}, nil
}

// vlclusterHostNetUnsupported is the single refusal point for non-Linux
// platforms — VLCluster requires host networking for the ports that
// vlinsert/vlselect dial on each vlstorage node.
func vlclusterHostNetUnsupported() error {
	return errors.New("VLCluster requires host networking, which podman only provides on Linux")
}

// --- Data dir resolution --------------------------------------------------

// resolveDataDir returns the vlstorage host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/vlcluster/<name>/data.
// Only vlstorage has persistent data; vlinsert and vlselect are stateless.
func (m *VLClusterManager) resolveDataDir(vc *config.VLClusterConfig) string {
	if vc.DataDir != "" {
		return vc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "vlcluster", vc.Name, "data")
}

// DataDir returns the resolved vlstorage host data directory (for display).
func (m *VLClusterManager) DataDir(vc *config.VLClusterConfig) string {
	return m.resolveDataDir(vc)
}

// --- Pure argv builders ---------------------------------------------------
//
// All three share the same skeleton: run detached with a role-suffixed name,
// host network, --restart unless-stopped, then the image ref followed by the
// daemon CLI flags. Only vlstorage has a bind mount (data dir).

// vlstorageArgs builds the `podman run` argv for vlstorage. It binds the host
// data dir at vlContainerDataDir and passes -storageDataPath and
// -httpListenAddr. Both /internal/insert and /internal/select/* are served on
// that same HTTP port, so there are no extra internal ports to publish.
// Retention and StorageExtraArgs follow.
func vlstorageArgs(vc *config.VLClusterConfig, dataDir string) []string {
	args := []string{
		"run", "-d",
		"--name", vc.ContainerName(config.VLClusterRoleStorage),
		"--network", "host",
	}
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), vlContainerDataDir),
	)
	args = append(args, vc.ImageTag)
	args = append(args,
		"-storageDataPath="+vlContainerDataDir,
		fmt.Sprintf("-httpListenAddr=%s:%d", vc.Listen, vc.StoragePort),
	)
	if vc.Retention != "" {
		args = append(args, "-retentionPeriod="+vc.Retention)
	}
	// StorageExtraArgs LAST — last-wins semantics.
	args = append(args, vc.StorageExtraArgs...)
	return args
}

// vlinsertArgs builds the `podman run` argv for vlinsert — stateless, so no
// bind mount. It passes -httpListenAddr, one -storageNode per vlstorage (the
// sharding core), and -select.disable so the process only ingests.
func vlinsertArgs(vc *config.VLClusterConfig) []string {
	args := []string{
		"run", "-d",
		"--name", vc.ContainerName(config.VLClusterRoleInsert),
		"--network", "host",
	}
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
	)
	args = append(args, vc.ImageTag)
	args = append(args,
		fmt.Sprintf("-httpListenAddr=%s:%d", vc.Listen, vc.InsertPort),
	)
	// One -storageNode per vlstorage — the sharding mechanism.
	for _, sn := range vc.EffectiveStorageNodes() {
		args = append(args, "-storageNode="+sn)
	}
	args = append(args, "-select.disable")
	// InsertExtraArgs LAST — last-wins semantics.
	args = append(args, vc.InsertExtraArgs...)
	return args
}

// vlselectArgs builds the `podman run` argv for vlselect — stateless, no bind
// mount. It passes -httpListenAddr, the same -storageNode list as vlinsert
// (storage addresses are identical; there is only one storage port), and
// -insert.disable so the process only queries.
func vlselectArgs(vc *config.VLClusterConfig) []string {
	args := []string{
		"run", "-d",
		"--name", vc.ContainerName(config.VLClusterRoleSelect),
		"--network", "host",
	}
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
	)
	args = append(args, vc.ImageTag)
	args = append(args,
		fmt.Sprintf("-httpListenAddr=%s:%d", vc.Listen, vc.SelectPort),
	)
	for _, sn := range vc.EffectiveStorageNodes() {
		args = append(args, "-storageNode="+sn)
	}
	args = append(args, "-insert.disable")
	// SelectExtraArgs LAST — last-wins semantics.
	args = append(args, vc.SelectExtraArgs...)
	return args
}

// --- Image management -----------------------------------------------------

// EnsureImage pulls the single shared image if it is absent locally. All three
// roles use the same ref, so there is nothing role-specific to pull.
func (m *VLClusterManager) EnsureImage(vc *config.VLClusterConfig) error {
	if platform.Detect() != platform.Linux {
		return vlclusterHostNetUnsupported()
	}
	exists, err := m.imageExists(vc.ImageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", vc.ImageTag)
	if err := m.runInteractive("pull", vc.ImageTag); err != nil {
		return fmt.Errorf("pulling VLCluster image %s (is docker.io reachable?): %w", vc.ImageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// --- Cluster lifecycle ----------------------------------------------------

// EnsureCluster creates (or recreates) the containers for the roles THIS host
// runs, in dependency order: storage → insert → select. A container for a role
// that is no longer in Roles (e.g. after --roles shrank) is removed first, so
// shrinking roles never leaves an orphan process behind.
func (m *VLClusterManager) EnsureCluster(vc *config.VLClusterConfig) error {
	if platform.Detect() != platform.Linux {
		return vlclusterHostNetUnsupported()
	}

	allRoles := []string{config.VLClusterRoleStorage, config.VLClusterRoleInsert, config.VLClusterRoleSelect}
	for i := len(allRoles) - 1; i >= 0; i-- {
		role := allRoles[i]
		if vc.RunsRole(role) {
			continue
		}
		name := vc.ContainerName(role)
		exists, err := m.containerExists(name)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", name); err != nil {
				return fmt.Errorf("removing stale VLCluster container %s: %w", name, err)
			}
			fmt.Printf("  [OK] Removed stale VLCluster container %s\n", name)
		}
	}

	for _, role := range vc.RolesInOrder() {
		fmt.Printf("-> Starting VLCluster %s service...\n", role)
		if err := m.createService(vc, role); err != nil {
			return err
		}
	}

	fmt.Printf("  [OK] VLCluster %s up on this host (roles: %s)\n", vc.Name, strings.Join(vc.RolesInOrder(), ", "))
	return nil
}

// createService (re)creates one role's container: an existing container is
// stopped and removed first so updated config always takes effect (install
// semantics). The data dir is created for vlstorage only.
func (m *VLClusterManager) createService(vc *config.VLClusterConfig, role string) error {
	name := vc.ContainerName(role)
	if exists, err := m.containerExists(name); err != nil {
		return err
	} else if exists {
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing stale VLCluster container %s: %w", name, err)
		}
	}

	var args []string
	switch role {
	case config.VLClusterRoleStorage:
		dir := m.resolveDataDir(vc)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating VLCluster data dir %s: %w", dir, err)
		}
		args = vlstorageArgs(vc, dir)
	case config.VLClusterRoleInsert:
		args = vlinsertArgs(vc)
	case config.VLClusterRoleSelect:
		args = vlselectArgs(vc)
	default:
		return fmt.Errorf("unknown VLCluster role %q", role)
	}

	slog.Debug("podman vlcluster run", "role", role, "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating VLCluster %s container: %w", role, err)
	}
	return nil
}

// startService starts one existing container (autostart/`pg addon start`):
// running → no-op; stopped → start; improper → recreate from config; missing
// → create fresh.
func (m *VLClusterManager) startService(vc *config.VLClusterConfig, role string) error {
	name := vc.ContainerName(role)
	running, err := m.containerRunning(name)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] VLCluster %s already running\n", name)
		return nil
	}
	if exists, err := m.containerExists(name); err != nil {
		return err
	} else if exists {
		if _, err := m.run("start", name); err == nil {
			fmt.Printf("  [OK] VLCluster %s started\n", name)
			return nil
		}
		fmt.Printf("  [!] VLCluster %s in improper state, recreating...\n", name)
	}
	return m.createService(vc, role)
}

// StartCluster starts this host's role containers in dependency order
// (storage → insert → select). Unlike EnsureCluster it does not recreate
// running services — autostart must be quick.
func (m *VLClusterManager) StartCluster(vc *config.VLClusterConfig) error {
	if platform.Detect() != platform.Linux {
		return vlclusterHostNetUnsupported()
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
// so in-flight traffic drains before vlstorage goes down.
func (m *VLClusterManager) StopCluster(vc *config.VLClusterConfig) error {
	var firstErr error
	roles := vc.RolesInOrder()
	for i := len(roles) - 1; i >= 0; i-- {
		role := roles[i]
		name := vc.ContainerName(role)
		if _, err := m.run("stop", name); err != nil {
			slog.Warn("vlcluster: stop service failed", "service", role, "err", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("stopping VLCluster %s: %w", role, err)
			}
		}
	}
	return firstErr
}

// RemoveCluster removes the containers for ALL THREE roles in reverse order,
// regardless of the current Roles — that cleans up orphans left behind when
// --roles shrank after a previous install. Data dirs are kept by default
// (vlstorage data outlives the containers — a reinstall under the same name
// revives the stored logs); cleanData also deletes them.
func (m *VLClusterManager) RemoveCluster(vc *config.VLClusterConfig, cleanData bool) error {
	allRoles := []string{config.VLClusterRoleStorage, config.VLClusterRoleInsert, config.VLClusterRoleSelect}
	for i := len(allRoles) - 1; i >= 0; i-- {
		role := allRoles[i]
		name := vc.ContainerName(role)
		_, _ = m.run("stop", name)
		if _, err := m.run("rm", "-f", name); err != nil {
			return fmt.Errorf("removing VLCluster container %s: %w", name, err)
		}
		fmt.Printf("  [OK] Removed VLCluster container %s\n", name)
	}

	// Only vlstorage has persistent data, but the dir may still exist after
	// the roles shrank — cleanData removes it either way.
	dataDir := m.resolveDataDir(vc)
	if !cleanData {
		if vc.RunsRole(config.VLClusterRoleStorage) {
			fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		}
		return nil
	}
	if isMountpoint(dataDir) {
		fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
	} else if err := removeHostDir(m.podman, vc.ImageTag, dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if vc.DataDir == "" {
		// Default layout: also prune the <name> dir once data/ is gone.
		_, _ = removeHostDirIfEmpty(m.podman, vc.ImageTag, filepath.Dir(dataDir))
	}
	return nil
}

// --- Status ---------------------------------------------------------------

// VLClusterState is the aggregate view of one cluster's containers PRESENT ON
// THIS HOST (a cross-host member only has its own roles).
type VLClusterState struct {
	InsertUp  bool
	SelectUp  bool
	StorageUp bool
	vc        *config.VLClusterConfig
}

// present returns the roles this state covers.
func (s VLClusterState) present() []string {
	if s.vc != nil {
		return s.vc.RolesInOrder()
	}
	return []string{config.VLClusterRoleStorage, config.VLClusterRoleInsert, config.VLClusterRoleSelect}
}

// Up reports whether the given role's container is running.
func (s VLClusterState) Up(role string) bool {
	switch role {
	case config.VLClusterRoleInsert:
		return s.InsertUp
	case config.VLClusterRoleSelect:
		return s.SelectUp
	default:
		return s.StorageUp
	}
}

// upOrder lists the running services in canonical order.
func (s VLClusterState) upOrder() []string {
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
func (s VLClusterState) String() string {
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
func (s VLClusterState) AllUp() bool {
	return len(s.upOrder()) == len(s.present()) && len(s.present()) > 0
}

// AllDown reports whether no present roles are running.
func (s VLClusterState) AllDown() bool { return len(s.upOrder()) == 0 }

// ContainerExists reports whether a named container is present (running or
// not) — the CLI's "complete cluster → start, incomplete → (re)create" gate.
func (m *VLClusterManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// ClusterStatus reports which of THIS host's role containers are running.
func (m *VLClusterManager) ClusterStatus(vc *config.VLClusterConfig) (VLClusterState, error) {
	st := VLClusterState{vc: vc}
	var err error
	if vc.RunsRole(config.VLClusterRoleStorage) {
		if st.StorageUp, err = m.containerRunning(vc.ContainerName(config.VLClusterRoleStorage)); err != nil {
			return st, err
		}
	}
	if vc.RunsRole(config.VLClusterRoleInsert) {
		if st.InsertUp, err = m.containerRunning(vc.ContainerName(config.VLClusterRoleInsert)); err != nil {
			return st, err
		}
	}
	if vc.RunsRole(config.VLClusterRoleSelect) {
		if st.SelectUp, err = m.containerRunning(vc.ContainerName(config.VLClusterRoleSelect)); err != nil {
			return st, err
		}
	}
	return st, nil
}

// --- Internal helpers -----------------------------------------------------

func (m *VLClusterManager) run(args ...string) (string, error) {
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

func (m *VLClusterManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *VLClusterManager) imageExists(tag string) (bool, error) {
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

func (m *VLClusterManager) containerExists(name string) (bool, error) {
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

func (m *VLClusterManager) containerRunning(name string) (bool, error) {
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
