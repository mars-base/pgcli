package podman

import (
	"crypto/rand"
	"encoding/hex"
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

// AuthUser holds the pgbouncer_auth user credentials for auth_query.
type AuthUser struct {
	User   string
	Passwd string // plaintext password for userlist.txt
}

// pgBouncerConfigDir returns the host directory for a given instance's
// PgBouncer configuration files.
func pgBouncerConfigDir(baseDir, instName string) string {
	return filepath.Join(baseDir, "addon", "pgbouncer", instName)
}

// SetupAuth creates the pgbouncer_auth user and lookup function on the
// target PostgreSQL instance, then returns the plaintext password.
// The plaintext password is stored in userlist.txt so PgBouncer can
// authenticate to PostgreSQL to run the auth_query.
func (m *PgBouncerManager) SetupAuth(dsn string) (*AuthUser, error) {
	pm, err := New(m.cfg)
	if err != nil {
		return nil, err
	}

	// Generate a random password for pgbouncer_auth
	pwBytes := make([]byte, 16)
	if _, err := rand.Read(pwBytes); err != nil {
		return nil, fmt.Errorf("generating random password: %w", err)
	}
	authPassword := "pgb_" + hex.EncodeToString(pwBytes)

	// Create user and lookup function (idempotent via IF NOT EXISTS / CREATE OR REPLACE)
	setupSQL := fmt.Sprintf(`
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgbouncer_auth') THEN
    CREATE ROLE pgbouncer_auth LOGIN PASSWORD '%s';
  ELSE
    ALTER ROLE pgbouncer_auth WITH PASSWORD '%s';
  END IF;
END $$;

CREATE OR REPLACE FUNCTION pgbouncer_lookup(username text)
RETURNS TABLE(usename name, passwd text) SECURITY DEFINER AS
'SELECT rolname, rolpassword FROM pg_authid WHERE rolname = $1 AND rolcanlogin'
LANGUAGE sql;

REVOKE ALL ON FUNCTION pgbouncer_lookup(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pgbouncer_lookup(text) TO pgbouncer_auth;
`, authPassword, authPassword)

	if _, err := pm.ExecDSNQuery(dsn, setupSQL); err != nil {
		return nil, fmt.Errorf("setting up pgbouncer_auth: %w", err)
	}

	return &AuthUser{User: "pgbouncer_auth", Passwd: authPassword}, nil
}

// WriteConfigs generates pgbouncer.ini and userlist.txt for the given instance
// into <baseDir>/addon/pgbouncer/<instName>/.
// Uses auth_query for dynamic password lookup from PostgreSQL.
func (m *PgBouncerManager) WriteConfigs(pbConf *config.PgBouncerConfig, authUser *AuthUser, dsn, instName string) (iniPath, userListPath string, err error) {
	host, port, _, _, database, perr := ParseDSN(dsn)
	if perr != nil {
		return "", "", perr
	}

	dir := pgBouncerConfigDir(m.dataDir, instName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("creating pgbouncer config dir: %w", err)
	}

	// pgbouncer.ini
	var ini strings.Builder
	ini.WriteString("[databases]\n")
	fmt.Fprintf(&ini, "* = host=%s port=%d\n\n", host, port)
	ini.WriteString("[pgbouncer]\n")
	ini.WriteString("listen_addr = 0.0.0.0\n")
	fmt.Fprintf(&ini, "listen_port = %d\n", pbConf.HostPort)
	ini.WriteString("auth_type = scram-sha-256\n")
	ini.WriteString("auth_file = /etc/pgbouncer/userlist.txt\n")
	ini.WriteString("auth_user = pgbouncer_auth\n")
	fmt.Fprintf(&ini, "auth_dbname = %s\n", database)
	ini.WriteString("auth_query = SELECT usename, passwd FROM pgbouncer_lookup($1)\n")
	fmt.Fprintf(&ini, "pool_mode = %s\n", pbConf.PoolMode)
	fmt.Fprintf(&ini, "max_client_conn = %d\n", pbConf.MaxClientConn)
	fmt.Fprintf(&ini, "default_pool_size = %d\n", pbConf.DefaultPoolSize)

	// Optional pool sizing (0 = PgBouncer default)
	if pbConf.MinPoolSize > 0 {
		fmt.Fprintf(&ini, "min_pool_size = %d\n", pbConf.MinPoolSize)
	}
	if pbConf.ReservePoolSize > 0 {
		fmt.Fprintf(&ini, "reserve_pool_size = %d\n", pbConf.ReservePoolSize)
	}
	if pbConf.MaxDBConnections > 0 {
		fmt.Fprintf(&ini, "max_db_connections = %d\n", pbConf.MaxDBConnections)
	}
	if pbConf.MaxUserConnections > 0 {
		fmt.Fprintf(&ini, "max_user_connections = %d\n", pbConf.MaxUserConnections)
	}

	// Optional timeouts (0 = PgBouncer default)
	if pbConf.ServerIdleTimeout > 0 {
		fmt.Fprintf(&ini, "server_idle_timeout = %d\n", pbConf.ServerIdleTimeout)
	}
	if pbConf.ServerLifetime > 0 {
		fmt.Fprintf(&ini, "server_lifetime = %d\n", pbConf.ServerLifetime)
	}
	if pbConf.ServerConnectTimeout > 0 {
		fmt.Fprintf(&ini, "server_connect_timeout = %d\n", pbConf.ServerConnectTimeout)
	}
	if pbConf.QueryTimeout > 0 {
		fmt.Fprintf(&ini, "query_timeout = %d\n", pbConf.QueryTimeout)
	}
	if pbConf.QueryWaitTimeout > 0 {
		fmt.Fprintf(&ini, "query_wait_timeout = %d\n", pbConf.QueryWaitTimeout)
	}
	if pbConf.IdleTransactionTimeout > 0 {
		fmt.Fprintf(&ini, "idle_transaction_timeout = %d\n", pbConf.IdleTransactionTimeout)
	}
	if pbConf.TransactionTimeout > 0 {
		fmt.Fprintf(&ini, "transaction_timeout = %d\n", pbConf.TransactionTimeout)
	}

	// Admin access
	if pbConf.AdminUsers != "" {
		fmt.Fprintf(&ini, "admin_users = %s\n", pbConf.AdminUsers)
	}
	if pbConf.StatsUsers != "" {
		fmt.Fprintf(&ini, "stats_users = %s\n", pbConf.StatsUsers)
	}

	// Logging (default 1 = enabled)
	if pbConf.LogConnections != 0 {
		fmt.Fprintf(&ini, "log_connections = %d\n", pbConf.LogConnections)
	}
	if pbConf.LogDisconnections != 0 {
		fmt.Fprintf(&ini, "log_disconnections = %d\n", pbConf.LogDisconnections)
	}

	iniPath = filepath.Join(dir, "pgbouncer.ini")
	if err := os.WriteFile(iniPath, []byte(ini.String()), 0644); err != nil {
		return "", "", fmt.Errorf("writing pgbouncer.ini: %w", err)
	}

	// userlist.txt — only pgbouncer_auth for auth_query
	var sb strings.Builder
	fmt.Fprintf(&sb, "%q %q\n", authUser.User, authUser.Passwd)
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
