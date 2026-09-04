package podman

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// PgBouncerManager manages PgBouncer sidecar containers.
// Like BackupManager, it operates across all instances in the config.
type PgBouncerManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
}

// NewPgBouncerManager creates a PgBouncerManager.
func NewPgBouncerManager(cfg *config.Config) (*PgBouncerManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	return &PgBouncerManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
	}, nil
}

// UserEntry holds a PostgreSQL user name and its scram-sha-256 password hash.
type UserEntry struct {
	User   string
	Passwd string // SCRAM-SHA-256$4096:…$…:…
}

// pgBouncerConfigDir returns the host directory for a given instance's
// PgBouncer configuration files.
func pgBouncerConfigDir(baseDir, instName string) string {
	return filepath.Join(baseDir, "addon", "pgbouncer", instName)
}

// SyncUsers queries pg_shadow on the target PG instance (via DSN) and returns
// all users with non-null password hashes.
func (m *PgBouncerManager) SyncUsers(dsn string) ([]UserEntry, error) {
	pm, err := New(m.cfg)
	if err != nil {
		return nil, err
	}
	out, err := pm.ExecDSNQuery(dsn,
		"SELECT usename, passwd FROM pg_shadow WHERE passwd IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("querying pg_shadow: %w", err)
	}

	var users []UserEntry
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		users = append(users, UserEntry{
			User:   strings.TrimSpace(parts[0]),
			Passwd: strings.TrimSpace(parts[1]),
		})
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("no users with passwords found in pg_shadow")
	}
	return users, nil
}

// WriteConfigs generates pgbouncer.ini and userlist.txt for the given instance
// into <baseDir>/addon/pgbouncer/<instName>/.
func (m *PgBouncerManager) WriteConfigs(pbConf *config.PgBouncerConfig, users []UserEntry, dsn, instName string) (iniPath, userListPath string, err error) {
	host, port, _, _, _, perr := ParseDSN(dsn)
	if perr != nil {
		return "", "", perr
	}

	dir := pgBouncerConfigDir(m.dataDir, instName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("creating pgbouncer config dir: %w", err)
	}

	// pgbouncer.ini
	ini := fmt.Sprintf(`[databases]
* = host=%s port=%d

[pgbouncer]
listen_addr = 0.0.0.0
listen_port = %d
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/userlist.txt
pool_mode = %s
max_client_conn = %d
default_pool_size = %d
`, host, port, pbConf.HostPort, pbConf.PoolMode, pbConf.MaxClientConn, pbConf.DefaultPoolSize)

	iniPath = filepath.Join(dir, "pgbouncer.ini")
	if err := os.WriteFile(iniPath, []byte(ini), 0644); err != nil {
		return "", "", fmt.Errorf("writing pgbouncer.ini: %w", err)
	}

	// userlist.txt
	var sb strings.Builder
	for _, u := range users {
		fmt.Fprintf(&sb, "%q %q\n", u.User, u.Passwd)
	}
	userListPath = filepath.Join(dir, "userlist.txt")
	if err := os.WriteFile(userListPath, []byte(sb.String()), 0644); err != nil {
		return "", "", fmt.Errorf("writing userlist.txt: %w", err)
	}

	return iniPath, userListPath, nil
}

// EnsureContainer creates or restarts the PgBouncer container for the given
// configuration. Config files are bind-mounted from the host, so updating
// them and restarting the container is sufficient for idempotent installs.
func (m *PgBouncerManager) EnsureContainer(iniPath, userListPath string, pbConf *config.PgBouncerConfig) error {
	containerName := pbConf.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> PgBouncer container already running, restarting to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping PgBouncer container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing PgBouncer container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale PgBouncer container: %w", err)
			}
		}
	}

	args := []string{
		"run", "-d",
		"--name", containerName,
		"--network", "host",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:/etc/pgbouncer/pgbouncer.ini:ro,z", hostMountPath(iniPath)),
		"-v", fmt.Sprintf("%s:/etc/pgbouncer/userlist.txt:ro,z", hostMountPath(userListPath)),
		pbConf.ImageTag,
	}

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating PgBouncer container: %w", err)
	}

	fmt.Println("  [OK] PgBouncer container started")
	return nil
}

// Remove stops and removes the PgBouncer container, then cleans up the
// config directory on the host.
func (m *PgBouncerManager) Remove(pbConf *config.PgBouncerConfig, instName string) error {
	containerName := pbConf.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing PgBouncer container: %w", err)
	}
	fmt.Println("  [OK] PgBouncer container removed")

	// Remove config directory
	dir := pgBouncerConfigDir(m.dataDir, instName)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing config dir %s: %v\n", dir, err)
	} else {
		fmt.Printf("  [OK] Config directory removed: %s\n", dir)
	}

	// Remove empty parent if it's now empty
	parent := filepath.Dir(dir)
	os.Remove(parent) // ignore error — non-empty dir won't be removed

	return nil
}


// ContainerRunning reports whether the named container is currently running.
// Exported so the CLI can display status.
func (m *PgBouncerManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// --- Internal helpers ------------------------------------------------

func (m *PgBouncerManager) run(args ...string) (string, error) {
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

func (m *PgBouncerManager) containerExists(name string) (bool, error) {
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

func (m *PgBouncerManager) containerRunning(name string) (bool, error) {
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
