package podman

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	res "github.com/mars-base/pgcli/embed"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"golang.org/x/crypto/ssh"
)

// BackupManager manages the shared pgbackrest backup container.
// Unlike Manager (which is bound to a single PG instance after SetInstance),
// BackupManager operates on all instances configured in the config file.
type BackupManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // pg data directory (~/.pg)
}

// NewBackupManager creates a BackupManager.
func NewBackupManager(cfg *config.Config) (*BackupManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	return &BackupManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
	}, nil
}

// SSHKeyPair is the on-disk path pair for the backup container SSH key.
type SSHKeyPair struct {
	Private string
	Public  string
}

// SSHKeyPaths returns the host paths to the backup SSH key pair.
func (m *BackupManager) SSHKeyPaths() SSHKeyPair {
	return SSHKeyPair{
		Private: filepath.Join(m.dataDir, "backup", "id_rsa"),
		Public:  filepath.Join(m.dataDir, "backup", "id_rsa.pub"),
	}
}

// SSHConfigPath returns the host path to the backup container's SSH client config.
func (m *BackupManager) SSHConfigPath() string {
	return filepath.Join(m.dataDir, "backup", "ssh_config")
}

// WriteSSHConfig writes an SSH client config that disables host key checking
// for PG containers. All platforms use host networking -- per-instance Host
// aliases map container names to 127.0.0.1 with unique SSH ports.
func (m *BackupManager) WriteSSHConfig() (string, error) {
	// Per-instance Host aliases: each PG container name maps to
	// 127.0.0.1 with its unique SSH port (host networking).
	var sb strings.Builder
	for _, inst := range m.cfg.Instances {
		if !inst.PITR.Enabled || inst.Podman.ContainerName == "" {
			continue
		}
		sshPort := inst.Podman.SSHPort
		if sshPort == 0 {
			sshPort = 42201
		}
		fmt.Fprintf(&sb, "Host %s\n", inst.Podman.ContainerName)
		// macOS + bridge: containers resolve each other via Podman DNS.
		// Linux + host: all containers share the host network stack.
		hostName := "127.0.0.1"
		if platform.Detect() == platform.MacOS {
			hostName = inst.Podman.ContainerName
		}
		fmt.Fprintf(&sb, "    HostName %s\n", hostName)
		sb.WriteString("    StrictHostKeyChecking no\n")
		sb.WriteString("    UserKnownHostsFile /dev/null\n")
		sb.WriteString("    IdentityFile /home/postgres/.ssh/id_rsa\n")
		sb.WriteString("    User postgres\n")
		fmt.Fprintf(&sb, "    Port %d\n\n", sshPort)
	}
	conf := sb.String()

	path := m.SSHConfigPath()

	if err := os.WriteFile(path, []byte(conf), 0644); err != nil {
		return "", fmt.Errorf("writing ssh config: %w", err)
	}
	return path, nil
}

func (m *BackupManager) EnsureSSHKey() (*SSHKeyPair, error) {
	keys := m.SSHKeyPaths()
	if err := os.MkdirAll(filepath.Dir(keys.Private), 0700); err != nil {
		return nil, fmt.Errorf("creating backup ssh directory: %w", err)
	}

	// Check if key already exists.
	if _, err := os.Stat(keys.Private); err == nil {
		return &keys, nil
	}

	fmt.Println("-> Generating backup container SSH key pair...")
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating rsa key: %w", err)
	}

	privPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}
	privFile, err := os.OpenFile(keys.Private, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("creating private key file: %w", err)
	}
	defer privFile.Close()
	if err := pem.Encode(privFile, privPEM); err != nil {
		return nil, fmt.Errorf("writing private key: %w", err)
	}

	pub, err := sshPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("formatting public key: %w", err)
	}
	if err := os.WriteFile(keys.Public, []byte(pub), 0644); err != nil {
		return nil, fmt.Errorf("writing public key: %w", err)
	}

	fmt.Println("  [OK] SSH key pair generated")
	return &keys, nil
}

// sshPublicKey returns an authorized_keys line for the given RSA public key.
func sshPublicKey(pub *rsa.PublicKey) (string, error) {
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), nil
}

// AuthorizeKeyOnContainer installs the backup public key into a PG container.
func (m *BackupManager) AuthorizeKeyOnContainer(containerName string) error {
	keys := m.SSHKeyPaths()

	pub, err := os.ReadFile(keys.Public)
	if err != nil {
		return fmt.Errorf("reading public key: %w", err)
	}

	// Write authorized_keys file via exec, then ensure correct ownership/permissions.
	cmd := fmt.Sprintf("mkdir -p /etc/ssh/authorized_keys && echo '%s' > /etc/ssh/authorized_keys/postgres && chown postgres:postgres /etc/ssh/authorized_keys/postgres && chmod 600 /etc/ssh/authorized_keys/postgres", strings.TrimSpace(string(pub)))
	podmanArgs := []string{"exec", "-u", "root", containerName, "sh", "-c", cmd}
	if _, err := execWithTimeout(m.podman, podmanArgs, 30*time.Second); err != nil {
		return fmt.Errorf("installing authorized_keys on %s: %w", containerName, err)
	}
	return nil
}

// AuthorizeKeyOnInstance is a convenience wrapper for the currently selected instance.
func (m *BackupManager) AuthorizeKeyOnInstance() error {
	return m.AuthorizeKeyOnContainer(m.cfg.Podman.ContainerName)
}

// --- Image management ---------------------------------------------

// EnsureBackupImage ensures the shared pgbackrest backup image is available.
// Tries podman pull first (for pre-built registry images), falls back to local build.
func (m *BackupManager) EnsureBackupImage() error {
	tag := m.cfg.Backup.ImageTag

	exists, err := m.imageExists(tag)
	if err != nil {
		return err
	}
	if exists {
		fmt.Printf("-> Backup image %s already exists, skipping pull/build\n", tag)
		return nil
	}

	// Try pull first
	fmt.Printf("-> Pulling backup image %s...\n", tag)
	if _, err := m.run("pull", tag); err == nil {
		fmt.Println("  [OK] Backup image pulled from registry")
		return nil
	}
	fmt.Printf("  Pull failed, falling back to local build...\n")

	// Fallback: build from embed backup.Containerfile
	return m.buildBackupImage(tag)
}

// buildBackupImage builds the backup image from embedded backup.Containerfile.
func (m *BackupManager) buildBackupImage(tag string) error {
	fmt.Println("-> Building pgbackrest backup image...")

	buildDir := filepath.Join(m.dataDir, "backup-build")
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return fmt.Errorf("creating backup build directory: %w", err)
	}

	containerfile := filepath.Join(buildDir, "Containerfile")
	if err := os.WriteFile(containerfile, []byte(res.BackupContainerfile), 0644); err != nil {
		return fmt.Errorf("writing backup Containerfile: %w", err)
	}

	if err := m.runInteractive("build", "-t", tag, "-f", containerfile, buildDir); err != nil {
		return fmt.Errorf("podman build backup image: %w", err)
	}

	fmt.Println("  [OK] Backup image built:", tag)
	return nil
}

// --- Network management ------------------------------------------

// EnsureNetwork creates a bridge network on macOS so containers can
// communicate via DNS-resolved container names.  Linux uses host networking.
func (m *BackupManager) EnsureNetwork() error {
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
func (m *BackupManager) networkExists(name string) (bool, error) {
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

// --- Directory management --------------------------------------

// EnsureBackupDirs creates the backup data and log directories on the host.
func (m *BackupManager) EnsureBackupDirs() error {
	dirs := []string{
		m.cfg.Backup.DataDir,
		m.cfg.Backup.LogDir,
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating backup directory %s: %w", dir, err)
		}
		fmt.Printf("-> Backup directory ensured: %s\n", dir)
	}
	return nil
}

// EnsureBackupInfra prepares the shared backup infrastructure:
// network, image, directories, config, and container with current PG container IPs.
func (m *BackupManager) EnsureBackupInfra() error {
	if err := m.EnsureNetwork(); err != nil {
		return err
	}
	if err := m.EnsureBackupImage(); err != nil {
		return err
	}
	if err := m.EnsureBackupDirs(); err != nil {
		return err
	}
	confPath, err := m.WritePgbackrestConf()
	if err != nil {
		return err
	}
	return m.EnsureBackupContainer(confPath)
}

// --- pgbackrest.conf generation ----------------------------------

// WritePgbackrestConf generates pgbackrest.conf with all instance stanzas.
// Returns the path to the generated config file.
func (m *BackupManager) WritePgbackrestConf() (string, error) {
	var sb strings.Builder

	// Build stanza for each instance with PITR enabled
	for name, inst := range m.cfg.Instances {
		if !inst.PITR.Enabled {
			continue
		}
		stanza := inst.PITR.PgBackRestStanza
		if stanza == "" {
			stanza = "pgcli_" + name
		}

		// pgbackrest runs from the backup container and connects to each PG
		// container via SSH (pg1-host). The PG container runs sshd as root
		// and accepts the backup container's public key for the postgres user.
		// SSH Host alias (in ssh_config) resolves container name
		// to 127.0.0.1 with the correct per-instance SSH port.
		host := inst.Podman.ContainerName
		sb.WriteString("[")
		sb.WriteString(stanza)
		sb.WriteString("]\n")
		fmt.Fprintf(&sb, "pg1-host=%s\n", host)
		fmt.Fprintf(&sb, "pg1-path=/var/lib/postgresql/data\n")
		fmt.Fprintf(&sb, "pg1-user=postgres\n\n")
	}

	// Global section
	sb.WriteString("[global]\n")
	sb.WriteString("repo1-path=/var/lib/pgbackrest\n")
	fmt.Fprintf(&sb, "repo1-retention-full=%d\n", m.cfg.Backup.RetentionFull)
	sb.WriteString("log-level-console=info\n")
	sb.WriteString("start-fast=y\n")
	sb.WriteString("compress-type=zst\n")

	confPath := filepath.Join(m.dataDir, "pgbackrest.conf")
	if err := os.WriteFile(confPath, []byte(sb.String()), 0644); err != nil {
		return "", fmt.Errorf("writing pgbackrest.conf: %w", err)
	}

	fmt.Printf("-> pgbackrest.conf generated: %s (%d stanzas)\n", confPath, len(m.cfg.Instances))
	return confPath, nil
}

// --- Container management ---------------------------------------------

// EnsureBackupContainer creates and starts the backup container if needed.
// SSH config and pgbackrest.conf are bind-mounted, so host file updates are
// reflected inside the container without recreation. Only (re)create if the
// container does not exist or is not running.
func (m *BackupManager) EnsureBackupContainer(confPath string) error {
	containerName := m.cfg.Backup.ContainerName

	// Always refresh SSH config — it's bind-mounted, so host-side updates
	// are immediately visible inside the container. New instances may have
	// been added since the container was last created.
	if _, err := m.WriteSSHConfig(); err != nil {
		fmt.Printf("  [!] ssh config update warning: %v\n", err)
	}

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> Backup container already running")
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		// Container exists but not running — start it
		fmt.Println("-> Starting backup container...")
		return m.StartBackupContainer()
	}

	fmt.Println("-> Creating and starting backup container...")
	return m.createBackupContainer(confPath)
}

// StartBackupContainer starts the backup container. If the container is in an
// improper state (e.g. left "Stopping" after a host reboot), `podman start`
// fails; the container is then removed and recreated automatically.
func (m *BackupManager) StartBackupContainer() error {
	if _, err := m.run("start", m.cfg.Backup.ContainerName); err != nil {
		fmt.Printf("  [!] Starting backup container failed: %v\n", err)
		return m.recreateBackupContainer()
	}
	return nil
}

// recreateBackupContainer removes the current backup container and creates a
// fresh one. Bind-mounted backup data on the host is preserved.
func (m *BackupManager) recreateBackupContainer() error {
	confPath, err := m.WritePgbackrestConf()
	if err != nil {
		return fmt.Errorf("generating pgbackrest.conf: %w", err)
	}
	fmt.Println("  -> Removing and recreating backup container...")
	if _, err := m.run("rm", "-f", m.cfg.Backup.ContainerName); err != nil {
		return fmt.Errorf("removing stale backup container: %w", err)
	}
	return m.createBackupContainer(confPath)
}

// StopBackupContainer stops the backup container.
func (m *BackupManager) StopBackupContainer() error {
	if _, err := m.run("stop", m.cfg.Backup.ContainerName); err != nil {
		return fmt.Errorf("stopping backup container: %w", err)
	}
	return nil
}

// Destroy stops and removes the shared backup container. Used when the last
// instance is destroyed so no orphaned backup container is left behind.
func (m *BackupManager) Destroy() error {
	m.run("stop", m.cfg.Backup.ContainerName)
	if _, err := m.run("rm", "-f", m.cfg.Backup.ContainerName); err != nil {
		return fmt.Errorf("removing backup container: %w", err)
	}
	return nil
}

// RemoveHostConfig deletes the backup infrastructure's config and credential
// files (pgbackrest.conf, SSH config and keys) while leaving backup data on
// disk. Used when the last instance is destroyed without --clean-data so the
// data stays available for a rebuild.
func (m *BackupManager) RemoveHostConfig() error {
	for _, p := range []string{
		filepath.Join(m.dataDir, "pgbackrest.conf"),
		filepath.Join(m.dataDir, "backup", "ssh_config"),
		filepath.Join(m.dataDir, "backup", "id_rsa"),
		filepath.Join(m.dataDir, "backup", "id_rsa.pub"),
	} {
		if err := os.Remove(p); err == nil {
			fmt.Printf("  [OK] removed: %s\n", p)
		} else if !os.IsNotExist(err) {
			fmt.Printf("  [!]  Warning: removing %s: %v\n", p, err)
		}
	}
	return nil
}

// RemoveHostData deletes everything the shared backup infrastructure left on
// the host: the backup repo (stanza dirs are already removed per-instance),
// the pgBackRest logs, the SSH credentials and the shared pgbackrest.conf.
// Call after Destroy() when no instances remain and --clean-data is used. The
// dbdata directory is only removed when empty (other instances may still use
// it).
func (m *BackupManager) RemoveHostData() error {
	m.RemoveHostConfig()

	baseDir := m.dataDir
	for _, p := range []string{
		filepath.Join(baseDir, "backup", "log"),
		filepath.Join(baseDir, "backup", "data"),
		filepath.Join(baseDir, "backup"),
	} {
		if err := os.Remove(p); err == nil {
			fmt.Printf("  [OK] removed: %s\n", p)
			continue
		} else if os.IsNotExist(err) {
			continue
		}
		// Directory owned by container sub-UIDs (or a non-empty dir) — remove
		// recursively through the podman user namespace.
		if err := removeHostDir(m.podman, m.cfg.Backup.ImageTag, p); err == nil {
			fmt.Printf("  [OK] removed: %s\n", p)
		} else if !os.IsNotExist(err) {
			fmt.Printf("  [!]  Warning: removing %s: %v\n", p, err)
		}
	}

	if removed, err := removeHostDirIfEmpty(m.podman, m.cfg.Backup.ImageTag, filepath.Join(baseDir, "dbdata")); err != nil {
		fmt.Printf("  [!]  Warning: removing empty dbdata directory: %v\n", err)
	} else if removed {
		fmt.Printf("  [OK] removed: %s\n", filepath.Join(baseDir, "dbdata"))
	}
	return nil
}

// BackupContainerStatus returns the backup container status.
func (m *BackupManager) BackupContainerStatus() (*ContainerStatus, error) {
	out, err := m.run("ps", "-a",
		"--filter", "name="+m.cfg.Backup.ContainerName,
		"--format", "{{.Names}}\t{{.Status}}\t{{.Ports}}",
	)
	if err != nil {
		return nil, fmt.Errorf("querying backup container status: %w", err)
	}

	cs := &ContainerStatus{Name: m.cfg.Backup.ContainerName}
	// podman's name filter is a substring match — pick the exact line.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, m.cfg.Backup.ContainerName+"\t") {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) >= 2 {
			cs.Status = parts[1]
			cs.Running = strings.HasPrefix(strings.ToLower(parts[1]), "up")
		}
		if len(parts) >= 3 {
			cs.Ports = parts[2]
		}
		break
	}
	if cs.Status == "" {
		cs.Status = "not created"
	}
	return cs, nil
}

// CheckContainerRunning checks if a container with the given name is running.
func (m *BackupManager) CheckContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// BackupExec runs a command inside the backup container.
// If tailLogs is true, the container's stdout/stderr is also streamed to the
// process stdout/stderr.
func (m *BackupManager) BackupExec(tailLogs bool, args ...string) (string, error) {
	podmanArgs := append([]string{"exec", "-i=false", m.cfg.Backup.ContainerName}, args...)
	if tailLogs {
		return execWithTimeoutStreaming(m.podman, podmanArgs, 10*time.Minute)
	}
	return execWithTimeout(m.podman, podmanArgs, 10*time.Minute)
}

// BackupExecToWriter runs a command inside the backup container, streaming
// stdout/stderr to w in addition to collecting them for error reporting.
// w may be nil (falls back to os.Stdout/os.Stderr only).
func (m *BackupManager) BackupExecToWriter(w io.Writer, args ...string) (string, error) {
	podmanArgs := append([]string{"exec", "-i=false", m.cfg.Backup.ContainerName}, args...)
	return execWithTimeoutWriter(m.podman, podmanArgs, 10*time.Minute, w)
}

// --- Internal methods ---------------------------------------------

func (m *BackupManager) run(args ...string) (string, error) {
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

func (m *BackupManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *BackupManager) imageExists(tag string) (bool, error) {
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

func (m *BackupManager) containerExists(name string) (bool, error) {
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

func (m *BackupManager) containerRunning(name string) (bool, error) {
	out, err := m.run("ps", "--filter", "name="+name, "--filter", "status=running", "--format", "{{.Names}}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == name, nil
}

func (m *BackupManager) createBackupContainer(confPath string) error {
	keys := m.SSHKeyPaths()

	// Ensure SSH config is written before mounting.
	sshConfPath, err := m.WriteSSHConfig()
	if err != nil {
		return err
	}

	// macOS: use bridge network so containers resolve each other by name.
	// Linux: keep host networking for zero-overhead.
	networkMode := "host"
	if platform.Detect() == platform.MacOS {
		networkMode = m.cfg.Podman.Network
	}

	args := []string{
		"run", "-d",
		"--name", m.cfg.Backup.ContainerName,
		"--network", networkMode,
		// Restart automatically if it exits (terminal close, service restart).
		"--restart", "unless-stopped",
	}

	args = append(args,
		"-v", fmt.Sprintf("%s:/var/lib/pgbackrest:z", hostMountPath(m.cfg.Backup.DataDir)),
		"-v", fmt.Sprintf("%s:/var/log/pgbackrest:z", hostMountPath(m.cfg.Backup.LogDir)),
		"-v", fmt.Sprintf("%s:/etc/pgbackrest/pgbackrest.conf:ro,z", hostMountPath(confPath)),
		// SSH keys are mounted writable (not :ro) so the ownership-migration
		// step below can chown them to postgres; the container runs as postgres
		// and needs to own its private key for ssh to accept it.
		"-v", fmt.Sprintf("%s:/home/postgres/.ssh/id_rsa:z", hostMountPath(keys.Private)),
		"-v", fmt.Sprintf("%s:/home/postgres/.ssh/id_rsa.pub:z", hostMountPath(keys.Public)),
		"-v", fmt.Sprintf("%s:/home/postgres/.ssh/config:z", hostMountPath(sshConfPath)),
		m.cfg.Backup.ImageTag,
	)

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating backup container: %w", err)
	}

	// Migrate repo/key ownership to the postgres user. Existing repo files were
	// written by root (container root -> host uid 1000); the container now runs
	// as postgres (uid 999 -> host 100998 via rootless podman subuid), which is
	// a different host uid and cannot write 1000-owned files. The mounted SSH
	// private key is likewise owned by 1000, which postgres cannot read.
	// Running chown as root inside the container (host uid 1000, owner of the
	// files) fixes both: repo and keys become postgres-owned. Idempotent and
	// cheap, so it is safe to run on every container (re)creation.
	chownArgs := []string{"exec", "-u", "root", m.cfg.Backup.ContainerName,
		"sh", "-c",
		"chown -R postgres:postgres /var/lib/pgbackrest /var/log/pgbackrest " +
				"/home/postgres/.ssh/id_rsa /home/postgres/.ssh/id_rsa.pub"}
	if _, err := m.run(chownArgs...); err != nil {
		fmt.Printf("  [!] ownership migration to postgres: %v\n", err)
	}

	fmt.Println("  [OK] Backup container started")
	return nil
}
