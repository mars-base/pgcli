// // Package podman manages the Podman container lifecycle:
// machine management, image building, directory creation, container start/stop, command execution.
package podman

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	res "github.com/mars-base/pgcli/embed"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// Manager encapsulates Podman operations, bound to a configuration.
type Manager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // pg data directory (~/.pgcli)
}

var (
	cachedPodmanPath string
)

// findPodman returns the path to the podman binary.
// On Linux it prefers ~/.local/bin/podman (installed by install.sh via
// podman-launcher), falling back to PATH. On macOS it uses exec.LookPath.
func findPodman() (string, error) {
	if cachedPodmanPath != "" {
		return cachedPodmanPath, nil
	}
	// Prefer the static podman-launcher wrapper bundled by install.sh.
	if runtime.GOOS == "linux" {
		home, err := os.UserHomeDir()
		if err == nil {
			p := filepath.Join(home, ".local", "bin", "podman")
			if fi, e := os.Stat(p); e == nil && !fi.IsDir() {
				cachedPodmanPath = p
				return p, nil
			}
		}
	}
	p, err := exec.LookPath("podman")
	if err != nil {
		return "", err
	}
	cachedPodmanPath = p
	return p, nil
}

// podmanCommand creates an *exec.Cmd for the podman binary with the given
// arguments. On Linux it sets XDG_RUNTIME_DIR in the environment so rootless
// podman can reach the user API socket. Use this instead of exec.Command
// directly for all podman invocations.
func podmanCommand(podmanPath string, args ...string) *exec.Cmd {
	cmd := exec.Command(podmanPath, args...)
	if runtime.GOOS == "linux" {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		if os.Getenv("XDG_RUNTIME_DIR") == "" {
			cmd.Env = append(cmd.Env, "XDG_RUNTIME_DIR=/run/user/"+fmt.Sprint(os.Getuid()))
		}
	}
	return cmd
}

// New creates a Podman manager.
func New(cfg *config.Config) (*Manager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}

	return &Manager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
	}, nil
}

// --- Machine management -----------------------------------------

// EnsureMachine ensures the runtime is ready for podman containers.
// On macOS this initializes/starts the podman machine; on Linux it is a no-op.
func (m *Manager) EnsureMachine() error {
	if !platform.NeedsPodmanMachine() {
		return nil // Linux: no machine needed
	}

	// Check if machine exists
	out, err := m.run("machine", "list", "--format", "{{.Name}}")
	if err != nil {
		return fmt.Errorf("checking podman machine list: %w", err)
	}

	hasMachine := false
	machineRunning := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		hasMachine = true
		// Check if running
		statusOut, _ := m.run("machine", "list", "--format", "{{.LastUp}}")
		if strings.TrimSpace(statusOut) != "" {
			machineRunning = true
		}
		break
	}

	if !hasMachine {
		fmt.Println("-> Initializing podman machine (first use, may take a few minutes)...")
		if err := m.runInteractive("machine", "init"); err != nil {
			return fmt.Errorf("podman machine init: %w", err)
		}
	}

	if !machineRunning {
		fmt.Println("-> Starting podman machine...")
		if err := m.runInteractive("machine", "start"); err != nil {
			return fmt.Errorf("podman machine start: %w", err)
		}
	}

	return nil
}

// --- Image management ---------------------------------------------

// EnsureImage ensures the PostgreSQL + pgBackRest image is available.
// Tries podman pull first (for pre-built registry images), falls back to local build.
func (m *Manager) EnsureImage() error {
	tag := m.cfg.Podman.ImageTag

	exists, err := m.imageExists(tag)
	if err != nil {
		return err
	}
	if exists {
		fmt.Printf("-> Image %s already exists, skipping pull/build\n", tag)
		return nil
	}

	// Try pull first
	fmt.Printf("-> Pulling image %s...\n", tag)
	if _, err := m.run("pull", tag); err == nil {
		fmt.Println("  [OK] Image pulled from registry")
		return nil
	}
	fmt.Printf("  Pull failed, falling back to local build...\n")

	// Fallback: build from embed Containerfile
	return m.buildImage(tag)
}

// buildImage builds the PG image from embedded Containerfile and init.sh.
func (m *Manager) buildImage(tag string) error {
	fmt.Println("-> Building PostgreSQL + pgBackRest image...")

	buildDir := filepath.Join(m.dataDir, "build")
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return fmt.Errorf("creating build directory: %w", err)
	}

	containerfile := filepath.Join(buildDir, "Containerfile")
	if err := os.WriteFile(containerfile, []byte(res.Containerfile), 0644); err != nil {
		return fmt.Errorf("writing Containerfile: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "init.sh"), []byte(res.InitShell), 0644); err != nil {
		return fmt.Errorf("writing init.sh: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "pg-entrypoint.sh"), []byte(res.PGEntrypointShell), 0644); err != nil {
		return fmt.Errorf("writing pg-entrypoint.sh: %w", err)
	}

	if err := m.runInteractive("build", "-t", tag, "-f", containerfile, buildDir); err != nil {
		return fmt.Errorf("podman build: %w", err)
	}

	return nil
}

// --- Network management ------------------------------------------

// EnsureNetwork creates a bridge network on macOS so containers can
// communicate via DNS-resolved container names.  Linux continues to
// use --network host (zero overhead).
func (m *Manager) EnsureNetwork() error {
	if platform.Detect() != platform.MacOS {
		return nil
	}
	netName := m.cfg.Podman.Network
	exists, err := m.networkExists(netName)
	if err != nil {
		return fmt.Errorf("checking network %s: %w", netName, err)
	}
	if exists {
		return nil
	}
	if _, err := m.run("network", "create", netName); err != nil {
		return fmt.Errorf("creating network %s: %w", netName, err)
	}
	fmt.Println("  [OK] Bridge network created:", netName)
	return nil
}

// networkExists returns true if a Podman network with the given name exists.
func (m *Manager) networkExists(name string) (bool, error) {
	out, err := m.run("network", "ls", "--format", "{{.Name}}")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// EnsureDirs creates required data directories on the host.
func (m *Manager) EnsureDirs() error {
	dirs := []string{
		m.cfg.Podman.DataDir,
	}
	// Ensure backup dirs exist (shared, host directories)
	for _, dir := range []string{m.cfg.Backup.DataDir, m.cfg.Backup.LogDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating backup directory %s: %w", dir, err)
		}
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating data directory %s: %w", dir, err)
		}
		fmt.Printf("-> Data directory ensured: %s\n", dir)
	}

	return nil
}

// DataDir returns the host path for the actual PGDATA directory
// (<DataDir>/data, since <DataDir> is mounted at /var/lib/postgresql).
func (m *Manager) PGHostDataDir() string {
	return filepath.Join(m.cfg.Podman.DataDir, "data")
}

// --- Container management ---------------------------------------------

// ContainerStatus represents the running status of a container.
type ContainerStatus struct {
	Name    string
	Running bool
	Status  string
	Ports   string
}

// EnsureContainer ensures the PostgreSQL container is created and running.
// Creates the container if it does not exist, starts it if stopped.
// Caller is responsible for calling EnsureNetwork() first.
func (m *Manager) EnsureContainer() error {
	exists, err := m.containerExists(m.cfg.Podman.ContainerName)
	if err != nil {
		return err
	}

	if !exists {
		fmt.Println("-> Creating and starting PostgreSQL container...")
		return m.createContainer()
	}

	running, err := m.containerRunning(m.cfg.Podman.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		fmt.Println("-> Starting PostgreSQL container...")
		return m.StartContainer()
	}

	fmt.Printf("-> Container %s is already running\n", m.cfg.Podman.ContainerName)
	return nil
}

// StartContainer starts an existing container.
func (m *Manager) StartContainer() error {
	if _, err := m.run("start", m.cfg.Podman.ContainerName); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}
	fmt.Println("  [OK] Container started (check readiness with: pg status)")
	return nil
}

// StopContainer stops the container.
func (m *Manager) StopContainer() error {
	if _, err := m.run("stop", m.cfg.Podman.ContainerName); err != nil {
		return fmt.Errorf("stopping container: %w", err)
	}
	return nil
}

// Status returns detailed container status.
func (m *Manager) Status() (*ContainerStatus, error) {
	out, err := m.run("ps", "-a",
		"--filter", "name="+m.cfg.Podman.ContainerName,
		"--format", "{{.Names}}\t{{.Status}}\t{{.Ports}}",
	)
	if err != nil {
		return nil, fmt.Errorf("querying container status: %w", err)
	}

	out = strings.TrimSpace(out)
	if out == "" {
		return &ContainerStatus{Name: m.cfg.Podman.ContainerName, Status: "not created"}, nil
	}

	parts := strings.SplitN(out, "\t", 3)
	cs := &ContainerStatus{Name: m.cfg.Podman.ContainerName}
	if len(parts) >= 2 {
		cs.Status = parts[1]
		cs.Running = strings.HasPrefix(strings.ToLower(parts[1]), "up")
	}
	if len(parts) >= 3 {
		cs.Ports = parts[2]
	}
	return cs, nil
}

// Exec runs a command inside the container, returns stdout.
func (m *Manager) Exec(args ...string) (string, error) {
	podmanArgs := append([]string{"exec", "-i=false", m.cfg.Podman.ContainerName}, args...)
	return execWithTimeout(m.podman, podmanArgs, 30*time.Second)
}

// ExecInteractive runs a command inside the container with stdin/stdout/stderr
// attached (TTY). Used for interactive psql or shell sessions.
// Detects whether stdin is a terminal to decide if -t (TTY) flag is needed.
func (m *Manager) ExecInteractive(args ...string) error {
	running, err := m.containerRunning(m.cfg.Podman.ContainerName)
	if err != nil {
		return fmt.Errorf("checking container status: %w", err)
	}
	if !running {
		exists, _ := m.containerExists(m.cfg.Podman.ContainerName)
		if !exists {
			return fmt.Errorf("container '%s' not found. Run 'pg start -i %s' to create and start it", m.cfg.Podman.ContainerName, m.cfg.Instance)
		}
		return fmt.Errorf("container '%s' is stopped. Run 'pg start -i %s' to start it", m.cfg.Podman.ContainerName, m.cfg.Instance)
	}

	// Use -it for interactive terminal, -i only for piped input
	execFlags := "-i"
	if isTerminal(os.Stdin) {
		execFlags = "-it"
	}
	podmanArgs := append([]string{"exec", execFlags, m.cfg.Podman.ContainerName}, args...)
	return m.runInteractive(podmanArgs...)
}

// isTerminal checks if a file is a terminal (TTY).
func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// Destroy removes the container. Data directories on the host are preserved.
func (m *Manager) Destroy() error {
	return m.DestroyWithData(false)
}

// DestroyWithData removes the container. If cleanData is true, the host data
// and WAL directories are also removed, along with the instance's pgBackRest
// stanza directories in the shared backup repo.
func (m *Manager) DestroyWithData(cleanData bool) error {
	if cleanData {
		fmt.Println("!  Removing container and all host data")
	} else {
		fmt.Println("!  Removing container (host data directories are preserved)")
	}

	// Stopping and removing container
	m.run("stop", m.cfg.Podman.ContainerName)
	m.run("rm", "-f", m.cfg.Podman.ContainerName)

	if !cleanData {
		fmt.Printf("  Data preserved at: %s\n", m.cfg.Podman.DataDir)
		return nil
	}

	// Remove host data directories. In rootless mode some files are owned by
	// subordinate UIDs, so fall back to a container-based deletion if needed.
	for _, desc := range []struct {
		name string
		path string
	}{
		{"data", m.cfg.Podman.DataDir},
	} {
		if desc.path == "" {
			continue
		}
		if err := removeHostDir(m.podman, desc.path); err != nil {
			return fmt.Errorf("removing %s directory %s: %w", desc.name, desc.path, err)
		}
		fmt.Printf("  [OK] %s directory removed: %s\n", desc.name, desc.path)
	}

	// Remove pgBackRest stanza directories from the shared repo.
	if m.cfg.PITR.Enabled && m.cfg.Backup.DataDir != "" {
		stanza := m.cfg.PITR.PgBackRestStanza
		repo := m.cfg.Backup.DataDir
		for _, sub := range []string{"backup", "archive"} {
			p := filepath.Join(repo, sub, stanza)
			if err := removeHostDir(m.podman, p); err != nil {
				return fmt.Errorf("removing repo %s directory %s: %w", sub, p, err)
			}
		}
		fmt.Printf("  [OK] backup stanza removed: %s\n", stanza)
	}

	return nil
}

// removeHostDir deletes a host directory, handling rootless podman ownership
// by falling back to a temporary container with the parent directory mounted.
func removeHostDir(podmanPath, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}

	if err := os.RemoveAll(dir); err == nil {
		return nil
	}

	// Fallback: delete from inside a container running as root within the
	// user namespace so subordinate-UID files can be removed.
	parent := filepath.Dir(dir)
	base := filepath.Base(dir)
	if parent == dir || parent == "" {
		return fmt.Errorf("cannot determine parent of %s", dir)
	}

	cmd := podmanCommand(podmanPath, "run", "--rm",
		"-v", fmt.Sprintf("%s:/target:z", hostMountPath(parent)),
		"alpine:3.20", "sh", "-c", fmt.Sprintf("rm -rf /target/%s", base),
	)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// PGIsReady checks if PostgreSQL is accepting connections by running pg_isready
// inside the container. This works on all platforms including those where the
// host pg_isready binary may not be available.
func (m *Manager) PGIsReady() (bool, error) {
	return m.pgIsReadyContainer()
}

// PGIsPausedInRecovery returns true when PostgreSQL is in a paused recovery
// state (recovery_target_action=pause), meaning WAL replay has been suspended
// at the target time and the cluster is read-only. In this state a simple
// pg_wal_replay_resume() is enough to promote — a full re-restore is wasteful.
func (m *Manager) PGIsPausedInRecovery() (bool, error) {
	out, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-tAc", "SELECT pg_is_wal_replay_paused()")
	if err != nil {
		return false, nil // PG may be down or not in recovery
	}
	return strings.TrimSpace(out) == "t", nil
}

// PGPromoteAfterRecovery resumes WAL replay from a paused recovery state,
// promoting the cluster to a new timeline and making it read-write.
// Equivalent to pg_wal_replay_resume() in psql.
func (m *Manager) PGPromoteAfterRecovery() (string, error) {
	return m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-tAc", "SELECT pg_wal_replay_resume()")
}

// PGLastXactReplayTimestamp returns the commit timestamp of the last
// transaction replayed during recovery, or a zero time if no transaction
// has been replayed yet.  Only meaningful when pg_is_in_recovery() = true.
func (m *Manager) PGLastXactReplayTimestamp() (time.Time, error) {
	out, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-tAc", "SELECT pg_last_xact_replay_timestamp()")
	if err != nil {
		return time.Time{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02 15:04:05.999999-07", out)
	if err != nil {
		return time.Time{}, nil // treat unparseable as unknown
	}
	return t, nil
}

// PGLastArchivedTime returns the timestamp of the most recently archived WAL
// segment (pg_stat_archiver.last_archived_time), or a zero time if nothing
// has been archived yet or the value is null. Requires the container to be
// running and accepting connections; callers should treat an error as
// "unknown" and skip any check that depends on it, consistent with the
// latest-backup-stop-time check in cli/restore.go.
func (m *Manager) PGLastArchivedTime() (time.Time, error) {
	out, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-tAc", "SELECT last_archived_time FROM pg_stat_archiver")
	if err != nil {
		return time.Time{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02 15:04:05.999999-07", out)
	if err != nil {
		return time.Time{}, nil // treat unparseable as unknown
	}
	return t, nil
}

// pgIsReadyContainer checks PG readiness via podman exec inside the container.
func (m *Manager) pgIsReadyContainer() (bool, error) {
	args := []string{"exec", m.cfg.Podman.ContainerName, "pg_isready", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database}
	out, err := m.run(args...)
	if err != nil {
		return false, nil
	}
	return strings.Contains(out, "accepting connections"), nil
}

// ContainerIP returns the IP address of the managed container on the configured network.
func (m *Manager) ContainerIP() (string, error) {
	out, err := m.run("inspect", m.cfg.Podman.ContainerName, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	if err != nil {
		return "", fmt.Errorf("inspecting container IP: %w", err)
	}
	ip := strings.TrimSpace(out)
	if ip == "" {
		return "", fmt.Errorf("container %s has no IP address", m.cfg.Podman.ContainerName)
	}
	return ip, nil
}

// RunRestoreContainer runs pgBackRest restore in a temporary container.
// The PG container must be stopped first. The temporary container mounts the
// same data directory and backup repo, using the per-instance pgbackrest.conf
// (which has no pg1-host, so restore runs locally on the data directory).
// If tailLogs is true, the container's stdout/stderr is also streamed to os stdout/stderr.
//
// When promote is false (default), --target-action=pause is used: PostgreSQL
// starts up, replays WAL up to the target time, then PAUSES recovery in a
// read-only state. This lets the user inspect the data at that point in time
// and, if unsatisfied, restore again to a different target without polluting
// the WAL archive -- no timeline switch happens, so the archive chain stays
// intact and repeated PITR "time travel" remains possible.
//
// When promote is true, --target-action=promote is used: recovery completes
// and the cluster is promoted to a new timeline, becoming read-write. This
// switches the timeline and writes new WAL, so further PITR to points after
// the backup requires a fresh snapshot first. Use promote only when the
// restored state is confirmed correct.
//
// The temporary container is given a deterministic --name with a PID suffix
// so concurrent pg processes (e.g. parallel e2e tests) won't collide, while
// still allowing cleanup of orphaned containers from prior runs via prefix
// matching. A deferred "podman rm -f" acts as a safety net beyond the --rm flag.
// No timeout is enforced -- restore duration depends on database size and may
// take hours for large datasets.
func (m *Manager) RunRestoreContainer(stanza, target string, promote, tailLogs bool) (string, error) {
	confPath, err := m.writeInstancePgbackrestConf()
	if err != nil {
		return "", fmt.Errorf("generating pgbackrest.conf: %w", err)
	}

	// Unique-per-run container name: PID suffix prevents collisions between
	// concurrent runs (e.g. parallel e2e tests sharing the same instance name).
	restoreName := fmt.Sprintf("pgcli-restore-%s-%d", m.cfg.Podman.ContainerName, os.Getpid())

	// Clean up any orphaned restore containers from previous runs (any PID).
	// We use the name prefix to match containers that belong to this instance
	// but were left behind by a killed/crashed process.
	orphanPrefix := "pgcli-restore-" + m.cfg.Podman.ContainerName + "-"
	rmOrphans := podmanCommand(m.podman, "ps", "-a", "--filter", "name="+orphanPrefix, "-q")
	if out, err := rmOrphans.Output(); err == nil {
		for _, id := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			id = strings.TrimSpace(id)
			if id != "" && id != restoreName {
				m.run("rm", "-f", id)
			}
		}
	}

	// Ensure cleanup on all exit paths (belt-and-suspenders with --rm).
	defer func() {
		m.run("rm", "-f", restoreName)
	}()

	// Wipe the PG data directory before restore. pgBackRest --delta (incremental
	// restore) is unsafe when the cluster has already been promoted to a new
	// timeline by a prior restore -- replaying WAL onto a promoted data directory
	// causes PostgreSQL to exit(1) during recovery with a timeline mismatch. A
	// clean full restore every time is correct and predictable; PITR "time
	// travel" is not meant to reuse a previously-promoted cluster state.
	dataVol := hostMountPath(m.cfg.Podman.DataDir)
	// Remove the entire PGDATA directory and recreate it empty. This is more
	// reliable than `rm -rf data/*` which can leave hidden files, fail on
	// glob mismatches, or skip files held open. pgBackRest restore (without
	// --delta) requires an empty target directory; deleting and recreating it
	// guarantees a clean slate regardless of prior promote/timeline state.
	wipe := podmanCommand(m.podman, "run", "--rm",
		"-u", "root",
		"--name", restoreName+"-wipe",
		"--network", "host",
		"-v", fmt.Sprintf("%s:/var/lib/postgresql:z", dataVol),
		m.cfg.Podman.ImageTag,
		"sh", "-c", "rm -rf /var/lib/postgresql/data && mkdir -p /var/lib/postgresql/data && chmod 700 /var/lib/postgresql/data",
	)
	if err := wipe.Run(); err != nil {
		return "", fmt.Errorf("wiping data directory before restore: %w", err)
	}

	// Determine the recovery target action. pause (default) keeps the cluster
	// read-only at the target time so the user can verify the data and restore
	// again to a different point without switching timelines; promote switches
	// to a new timeline and makes the cluster read-write.
	targetAction := "pause"
	if promote {
		targetAction = "promote"
	}

	args := []string{
		"run", "--rm",
		"--name", restoreName,
		"--network", "host",
		"-v", fmt.Sprintf("%s:/var/lib/postgresql:z", dataVol),
		"-v", fmt.Sprintf("%s:/var/lib/pgbackrest:z", hostMountPath(m.cfg.Backup.DataDir)),
		"-v", fmt.Sprintf("%s:/etc/pgbackrest/pgbackrest.conf:ro,z", hostMountPath(confPath)),
		m.cfg.Podman.ImageTag,
		"pgbackrest", "--stanza=" + stanza, "restore",
		"--type=time", "--target=" + target,
		"--target-action=" + targetAction,
		"--log-level-console=info",
	}

	// Run without timeout -- restore can take a long time for large databases.
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)

	var stdoutBuf, stderrBuf strings.Builder
	if tailLogs {
		cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	} else {
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	}

	err = cmd.Run()
	out := stdoutBuf.String()

	// Podman may report a non-zero exit after `podman run --rm` because it
	// tries to forward a terminal signal (e.g. SIGWINCH) to a container
	// that has already been removed. If the actual command succeeded,
	// treat this as success.
	if err != nil && isPodmanCleanupNoise(stderrBuf.String()) && strings.Contains(out, "completed successfully") {
		return out, nil
	}

	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			errMsg := stderrBuf.String()
			if errMsg == "" {
				errMsg = out
			}
			return "", fmt.Errorf("podman %s: %s", strings.Join(args, " "), errMsg)
		}
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// RunRestoreContainerToWriter is like RunRestoreContainer but streams all
// stdout/stderr output to w in addition to capturing it internally.
func (m *Manager) RunRestoreContainerToWriter(w io.Writer, stanza, target string, promote bool) (string, error) {
	confPath, err := m.writeInstancePgbackrestConf()
	if err != nil {
		return "", fmt.Errorf("generating pgbackrest.conf: %w", err)
	}

	restoreName := fmt.Sprintf("pgcli-restore-%s-%d", m.cfg.Podman.ContainerName, os.Getpid())
	orphanPrefix := "pgcli-restore-" + m.cfg.Podman.ContainerName + "-"
	rmOrphans := podmanCommand(m.podman, "ps", "-a", "--filter", "name="+orphanPrefix, "-q")
	if out, err := rmOrphans.Output(); err == nil {
		for _, id := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			id = strings.TrimSpace(id)
			if id != "" && id != restoreName {
				m.run("rm", "-f", id)
			}
		}
	}
	defer func() { m.run("rm", "-f", restoreName) }()

	dataVol := hostMountPath(m.cfg.Podman.DataDir)
	wipe := podmanCommand(m.podman, "run", "--rm",
		"-u", "root",
		"--name", restoreName+"-wipe",
		"--network", "host",
		"-v", fmt.Sprintf("%s:/var/lib/postgresql:z", dataVol),
		m.cfg.Podman.ImageTag,
		"sh", "-c", "rm -rf /var/lib/postgresql/data && mkdir -p /var/lib/postgresql/data && chmod 700 /var/lib/postgresql/data",
	)
	if err := wipe.Run(); err != nil {
		return "", fmt.Errorf("wiping data directory before restore: %w", err)
	}

	targetAction := "pause"
	if promote {
		targetAction = "promote"
	}

	args := []string{
		"run", "--rm",
		"--name", restoreName,
		"--network", "host",
		"-v", fmt.Sprintf("%s:/var/lib/postgresql:z", dataVol),
		"-v", fmt.Sprintf("%s:/var/lib/pgbackrest:z", hostMountPath(m.cfg.Backup.DataDir)),
		"-v", fmt.Sprintf("%s:/etc/pgbackrest/pgbackrest.conf:ro,z", hostMountPath(confPath)),
		m.cfg.Podman.ImageTag,
		"pgbackrest", "--stanza=" + stanza, "restore",
		"--type=time", "--target=" + target,
		"--target-action=" + targetAction,
		"--log-level-console=info",
	}

	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = io.MultiWriter(w, &stdoutBuf)
	cmd.Stderr = io.MultiWriter(w, &stderrBuf)

	err = cmd.Run()
	out := stdoutBuf.String()

	if err != nil && isPodmanCleanupNoise(stderrBuf.String()) && strings.Contains(out, "completed successfully") {
		return out, nil
	}
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			errMsg := stderrBuf.String()
			if errMsg == "" {
				errMsg = out
			}
			return "", fmt.Errorf("podman %s: %s", strings.Join(args, " "), errMsg)
		}
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}


func (m *Manager) run(args ...string) (string, error) {
	slog.Debug("podman", "args", args)
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

func (m *Manager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *Manager) imageExists(tag string) (bool, error) {
	out, err := m.run("images", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == tag {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) containerExists(name string) (bool, error) {
	out, err := m.run("ps", "-a", "--filter", "name="+name, "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) containerRunning(name string) (bool, error) {
	out, err := m.run("ps", "--filter", "name="+name, "--filter", "status=running", "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == name, nil
}

func (m *Manager) createContainer() error {
	// Generate per-instance pgbackrest.conf
	confPath, err := m.writeInstancePgbackrestConf()
	if err != nil {
		return fmt.Errorf("generating pgbackrest.conf: %w", err)
	}

	// Use the shared backup data volume so pgbackrest archive-push writes
	// to the same repo the backup container manages.
	backupVol := m.cfg.Backup.DataDir
	hostPort := m.cfg.Podman.HostPort

	// macOS: use bridge network so containers can resolve each other by
	// name and the Mac host can reach published ports via gvproxy.
	// Linux: keep host networking for zero-overhead.
	networkMode := "host"
	if platform.Detect() == platform.MacOS {
		networkMode = m.cfg.Podman.Network
	}

	args := []string{
		"run", "-d",
		"--name", m.cfg.Podman.ContainerName,
		"--network", networkMode,
		// Restart automatically if the container exits (e.g. podman service
		// interruption when the launching terminal closes).
		// "unless-stopped" preserves an explicit `pg stop`.
		"--restart", "unless-stopped",
	}

	// Each PG instance gets a unique port via PGPORT / PGCLI_SSH_PORT env vars.
	args = append(args,
		"-e", fmt.Sprintf("PGPORT=%d", m.cfg.Podman.HostPort),
		"-e", fmt.Sprintf("PGCLI_SSH_PORT=%d", m.cfg.Podman.SSHPort),
	)

	// macOS + bridge: publish PG port so gvproxy forwards it to the Mac host.
	if platform.Detect() == platform.MacOS {
		args = append(args, "-p", fmt.Sprintf("%d:%d", hostPort, hostPort))
	}

	args = append(args,
		"-v", fmt.Sprintf("%s:/var/lib/postgresql:z", hostMountPath(m.cfg.Podman.DataDir)),
		"-v", fmt.Sprintf("%s:/var/lib/pgbackrest:z", hostMountPath(backupVol)),
		"-v", fmt.Sprintf("%s:/etc/pgbackrest/pgbackrest.conf:ro,z", hostMountPath(confPath)),
		"-e", fmt.Sprintf("POSTGRES_DB=%s", m.cfg.Postgres.Database),
		"-e", fmt.Sprintf("POSTGRES_USER=%s", m.cfg.Postgres.User),
		"-e", fmt.Sprintf("POSTGRES_PASSWORD=%s", m.cfg.Postgres.Password),
		"-e", fmt.Sprintf("PGBACKREST_STANZA=%s", m.cfg.PITR.PgBackRestStanza),
		"-e", "PGDATA=/var/lib/postgresql/data",
	)

	// Mount the backup container's public key so the PG container entrypoint
	// can install it as authorized_keys for postgres on every startup. This
	// makes the key survive PG container recreation without explicit re-auth.
	if m.cfg.PITR.Enabled {
		bm, err := NewBackupManager(m.cfg)
		if err != nil {
			return fmt.Errorf("creating backup manager: %w", err)
		}
		keys, err := bm.EnsureSSHKey()
		if err != nil {
			return fmt.Errorf("ensuring backup ssh key: %w", err)
		}
		args = append(args,
			"-v", fmt.Sprintf("%s:/run/pgcli/backup_id_rsa.pub:ro,z", hostMountPath(keys.Public)),
		)
	}

	args = append(args, m.cfg.Podman.ImageTag)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating container: %w", err)
	}
	fmt.Println("  [OK] Container created, PostgreSQL is initializing (check with: pg status)")
	return nil
}

// writeInstancePgbackrestConf writes a per-instance pgbackrest.conf for the PG container.
// Returns the path to the config file to be mounted.
func (m *Manager) writeInstancePgbackrestConf() (string, error) {
	stanza := m.cfg.PITR.PgBackRestStanza

	// All platforms use host networking now: each PG instance listens on a
	// unique port (PGPORT env var / pg1-port below).  Without pg1-port, the
	// remote pgbackrest process (over SSH) defaults to 5432 -- but sshd does
	// not forward PGPORT, so instances with custom ports fail stanza-create.
	content := fmt.Sprintf(`[%s]
pg1-path=/var/lib/postgresql/data
pg1-user=%s
pg1-port=%d

[global]
repo1-path=/var/lib/pgbackrest
repo1-retention-full=%d
repo1-retention-archive-type=full
repo1-retention-archive=%d
log-level-console=info
`, stanza, m.cfg.Postgres.User, m.cfg.Podman.HostPort, m.cfg.Backup.RetentionFull, m.cfg.Backup.RetentionFull)

	confPath := filepath.Join(m.dataDir, fmt.Sprintf("pgbackrest-%s.conf", m.cfg.Podman.ContainerName))

	if err := os.WriteFile(confPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing %s: %w", confPath, err)
	}
	return confPath, nil
}
