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

// PgDogManager manages standalone PgDog containers. Like EtcdManager it
// operates over the top-level (cross-instance) addon configs: PgDog is shared
// proxy infrastructure (pooling / load balancing / sharding in front of one or
// more PostgreSQL backends), not a per-instance sidecar.
type PgDogManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: run on the pgcli-net bridge instead of host networking
}

// NewPgDogManager creates a PgDogManager.
func NewPgDogManager(cfg *config.Config) (*PgDogManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &PgDogManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// configDir returns the host directory holding a proxy's pgdog.toml and
// users.toml.
func pgDogConfigDir(baseDir, name string) string {
	return filepath.Join(baseDir, "addon", "pgdog", name)
}

// tomlQuote escapes a string for use inside a double-quoted TOML basic string.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// WriteConfigs generates pgdog.toml and users.toml for the given proxy into
// <baseDir>/addon/pgdog/<name>/. The content mirrors the ansible-pg pgdog
// role's templates: a [general] block, one [[databases]] entry per backend,
// optional [[sharded_tables]] entries, and a users.toml of [[users]] entries.
// users.toml holds plaintext passwords, so it is written mode 0600.
func (m *PgDogManager) WriteConfigs(pd *config.PgDogConfig) (tomlPath, usersPath string, err error) {
	dir := pgDogConfigDir(m.dataDir, pd.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("creating pgdog config dir: %w", err)
	}

	var toml strings.Builder
	toml.WriteString("[general]\n")
	// macOS bridge: a published port can't reach a loopback-only bind inside
	// the container, so the default (127.0.0.1) renders as 0.0.0.0 — without
	// touching pd.Host itself, which ClientAddr()/status output still display.
	fmt.Fprintf(&toml, "host = %s\n", tomlQuote(proxyBindHost(m.bridge, pd.Host)))
	fmt.Fprintf(&toml, "port = %d\n", pd.HostPort)
	fmt.Fprintf(&toml, "workers = %d\n", pd.Workers)
	fmt.Fprintf(&toml, "default_pool_size = %d\n", pd.DefaultPoolSize)
	fmt.Fprintf(&toml, "pooler_mode = %s\n", tomlQuote(pd.PoolerMode))
	fmt.Fprintf(&toml, "openmetrics_port = %d\n", pd.OpenmetricsPort)
	for _, db := range pd.Backends {
		toml.WriteString("\n[[databases]]\n")
		fmt.Fprintf(&toml, "name = %s\n", tomlQuote(db.Name))
		fmt.Fprintf(&toml, "host = %s\n", tomlQuote(db.Host))
		fmt.Fprintf(&toml, "port = %d\n", db.Port)
		fmt.Fprintf(&toml, "database_name = %s\n", tomlQuote(db.DatabaseName))
		fmt.Fprintf(&toml, "shard = %d\n", db.Shard)
		if db.Role != "" {
			fmt.Fprintf(&toml, "role = %s\n", tomlQuote(db.Role))
		}
	}
	for _, t := range pd.ShardedTables {
		toml.WriteString("\n[[sharded_tables]]\n")
		fmt.Fprintf(&toml, "database = %s\n", tomlQuote(t.Database))
		fmt.Fprintf(&toml, "name = %s\n", tomlQuote(t.Name))
		fmt.Fprintf(&toml, "column = %s\n", tomlQuote(t.Column))
		fmt.Fprintf(&toml, "data_type = %s\n", tomlQuote(t.DataType))
	}

	tomlPath = filepath.Join(dir, "pgdog.toml")
	if err := os.WriteFile(tomlPath, []byte(toml.String()), 0644); err != nil {
		return "", "", fmt.Errorf("writing pgdog.toml: %w", err)
	}

	var users strings.Builder
	for _, u := range pd.Users {
		users.WriteString("[[users]]\n")
		fmt.Fprintf(&users, "name = %s\n", tomlQuote(u.Name))
		fmt.Fprintf(&users, "password = %s\n", tomlQuote(u.Password))
		fmt.Fprintf(&users, "database = %s\n", tomlQuote(u.Database))
		if u.ReplicationMode {
			fmt.Fprintf(&users, "replication_mode = %t\n", u.ReplicationMode)
		}
		users.WriteString("\n")
	}

	usersPath = filepath.Join(dir, "users.toml")
	if err := os.WriteFile(usersPath, []byte(users.String()), 0600); err != nil {
		return "", "", fmt.Errorf("writing users.toml: %w", err)
	}

	return tomlPath, usersPath, nil
}

// EnsureContainer creates or restarts the PgDog container for the given
// configuration. Config files are bind-mounted from the host, so updating them
// and recreating the container is sufficient for idempotent installs.
func (m *PgDogManager) EnsureContainer(pd *config.PgDogConfig) error {
	containerName := pd.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> PgDog container already running, restarting to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping PgDog container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing PgDog container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale PgDog container: %w", err)
			}
		}
	}

	if err := m.createContainer(pd); err != nil {
		return err
	}
	fmt.Println("  [OK] PgDog container started")
	return nil
}

// StartContainer starts the PgDog container without regenerating config
// (autostart-on-boot path). Unlike EnsureContainer (install semantics:
// stop/rm/recreate), this only brings the container up: running → no-op;
// exists but stopped → podman start, recreating on improper state; missing →
// create from the existing config files on disk.
func (m *PgDogManager) StartContainer(pd *config.PgDogConfig) error {
	containerName := pd.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] PgDog %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] PgDog %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] PgDog %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper PgDog container: %w", err)
		}
	}

	tomlPath := filepath.Join(pgDogConfigDir(m.dataDir, pd.Name), "pgdog.toml")
	if _, err := os.Stat(tomlPath); err != nil {
		return fmt.Errorf("pgdog config %s not found -- run 'pg addon install pgdog' first", tomlPath)
	}

	if err := m.createContainer(pd); err != nil {
		return err
	}
	fmt.Printf("  [OK] PgDog %s started\n", containerName)
	return nil
}

// createContainer runs a PgDog proxy, bind-mounting its config directory at
// /pgdog (matching the image's expected layout) and pointing the binary at the
// two TOML files. Linux uses host networking; macOS joins the pgcli-net bridge
// and publishes its ports (see netFlags).
func (m *PgDogManager) createContainer(pd *config.PgDogConfig) error {
	dir := pgDogConfigDir(m.dataDir, pd.Name)

	// The role mounts /pgdog read-write (pgdog may write there), so do the
	// same rather than :ro.
	// --http-proxy=false: podman would otherwise inject the host's HTTP(S)_PROXY
	// and the process could honor it for outbound connections. Image pulls run
	// client-side and keep the proxy.
	args := []string{"run", "-d", "--name", pd.ContainerName}
	args = append(args, netFlags(m.bridge, m.cfg.Podman.Network, pd.HostPort, pd.OpenmetricsPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:/pgdog:z", hostMountPath(dir)),
		pd.ImageTag,
		"pgdog",
		"-c", "/pgdog/pgdog.toml",
		"-u", "/pgdog/users.toml",
	)

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating PgDog container: %w", err)
	}
	return nil
}

// Remove stops and removes the PgDog container, then cleans up the config
// directory on the host.
func (m *PgDogManager) Remove(pd *config.PgDogConfig) error {
	containerName := pd.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing PgDog container: %w", err)
	}
	fmt.Println("  [OK] PgDog container removed")

	dir := pgDogConfigDir(m.dataDir, pd.Name)
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
func (m *PgDogManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// Stop stops a PgDog container.
func (m *PgDogManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ------------------------------------------------

func (m *PgDogManager) run(args ...string) (string, error) {
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

func (m *PgDogManager) containerExists(name string) (bool, error) {
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

func (m *PgDogManager) containerRunning(name string) (bool, error) {
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
