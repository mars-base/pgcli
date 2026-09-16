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
	"sort"
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
	ensurePodmanStateReady(path)
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

// patroniBackupTarget is one Patroni member normalized for the backup
// generators: the same value feeds both the SSH `Host` alias / `HostName` and
// the pgBackRest `pg1-host` / `pg1-port`, so the two files can never disagree.
type patroniBackupTarget struct {
	scope    string // Patroni cluster map key (for the stanza name)
	member   string // member name (for the stanza name)
	alias    string // SSH Host alias == pgBackRest pg1-host
	hostName string // SSH HostName (127.0.0.1 / container name / remote IP)
	hostPort int    // PostgreSQL port == pg1-port
	sshPort  int    // sshd port on hostName
}

// patroniBackupTargets resolves the full backup target set for every Patroni
// cluster. It prefers the DCS-merged view (DiscoverAllMembers), which includes
// cross-host members this host's pg.yaml never saw, and folds in each member's
// SSH port from the pgcli member registry. When the DCS is unreachable it
// falls back to this host's local config (which may still carry `pg ha remote`
// registrations). Callers get a best-effort list; no error is surfaced because
// a missing remote member must not block local backups.
func (m *BackupManager) patroniBackupTargets() []patroniBackupTarget {
	pm, pmErr := NewPatroniManager(m.cfg)
	isMac := platform.Detect() == platform.MacOS
	defaultSSH := m.cfg.PatroniSSHStartPort
	if defaultSSH == 0 {
		defaultSSH = 42301
	}

	var out []patroniBackupTarget
	for scope, cluster := range m.cfg.Addons.Patroni {
		if pmErr == nil {
			if members, err := pm.DiscoverAllMembers(&cluster); err == nil {
				for _, cm := range members {
					t := patroniBackupTarget{scope: scope, member: cm.Name, hostPort: cm.HostPort}
					t.sshPort = cm.SSHPort
					if t.sshPort == 0 {
						t.sshPort = defaultSSH
					}
					if cm.Local {
						// Same host: reach via the container-name alias.
						t.alias = cluster.Members[cm.Name].ContainerName
						if t.alias == "" {
							t.alias = cm.Host // no local container name; use DCS host
						}
						t.hostName = "127.0.0.1"
						if isMac {
							t.hostName = t.alias
						}
					} else {
						t.alias = cm.Host
						t.hostName = cm.Host
					}
					out = append(out, t)
				}
				continue
			}
		}
		// DCS unreachable: local config only.
		for member, mb := range cluster.Members {
			t := patroniBackupTarget{scope: scope, member: member, hostPort: mb.HostPort}
			t.sshPort = mb.SSHPort
			if t.sshPort == 0 {
				t.sshPort = defaultSSH
			}
			if mb.RemoteHost != "" {
				t.alias = mb.RemoteHost
				t.hostName = mb.RemoteHost
			} else if mb.ContainerName != "" {
				t.alias = mb.ContainerName
				t.hostName = "127.0.0.1"
				if isMac {
					t.hostName = mb.ContainerName
				}
			} else {
				continue // local member not yet created — nothing to back up
			}
			out = append(out, t)
		}
	}
	return out
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

	// Patroni cluster members: same SSH scheme but with their own port range.
	// The alias (== pg1-host in the stanza) is the container name for local
	// members (→ 127.0.0.1) and the host IP for cross-host members.
	for _, t := range m.patroniBackupTargets() {
		fmt.Fprintf(&sb, "Host %s\n", t.alias)
		fmt.Fprintf(&sb, "    HostName %s\n", t.hostName)
		sb.WriteString("    StrictHostKeyChecking no\n")
		sb.WriteString("    UserKnownHostsFile /dev/null\n")
		sb.WriteString("    IdentityFile /home/postgres/.ssh/id_rsa\n")
		sb.WriteString("    User postgres\n")
		fmt.Fprintf(&sb, "    Port %d\n\n", t.sshPort)
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

// AuthorizeKeyOnContainer installs the backup public key into a PG container
// and patches PAM so that SSH command execution works inside unprivileged
// containers.
//
// Containers lack CAP_AUDIT_WRITE, which pam_loginuid.so needs to write
// /proc/self/loginuid.  When pam_loginuid is "required" (Debian default),
// sshd accepts the connection but kills the session before the command runs —
// exit status 254 with only the MOTD banner.  pgBackRest SSH connections then
// receive "uname" output instead of JSON protocol responses.
//
// Making pam_loginuid "optional" is safe: loginuid auditing is irrelevant
// inside a container and the rest of the PAM stack remains unchanged.
func (m *BackupManager) AuthorizeKeyOnContainer(containerName string) error {
	keys := m.SSHKeyPaths()

	pub, err := os.ReadFile(keys.Public)
	if err != nil {
		return fmt.Errorf("reading public key: %w", err)
	}

	// 1. Write authorized_keys file, ensure correct ownership/permissions.
	// 2. Patch pam_loginuid.so from "required" to "optional" so SSH commands
	//    execute inside containers that lack CAP_AUDIT_WRITE.
	cmd := fmt.Sprintf(
		"mkdir -p /etc/ssh/authorized_keys && "+
			"echo '%s' > /etc/ssh/authorized_keys/postgres && "+
			"chown postgres:postgres /etc/ssh/authorized_keys/postgres && "+
			"chmod 600 /etc/ssh/authorized_keys/postgres && "+
			"sed -i 's/^session  *required  *pam_loginuid/session    optional     pam_loginuid/' /etc/pam.d/sshd",
		strings.TrimSpace(string(pub)))
	podmanArgs := []string{"exec", "-u", "root", containerName, "sh", "-c", cmd}
	if _, err := execWithTimeout(m.podman, podmanArgs, 30*time.Second); err != nil {
		return fmt.Errorf("configuring SSH on %s: %w", containerName, err)
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

// patroniStanzaName is the single source of truth for a Patroni cluster's
// pgBackRest stanza name. It is CLUSTER-wide, not per-member: all members are
// one logical database with one timeline, so they share a stanza (identical
// WAL pushes from a demoted old leader simply dedup). The backup-side stanza
// lists one pg*-host per member so pgBackRest locates the primary on its own
// and keeps working across failovers — stanza-create and full backups must
// connect to the primary ([056]), which a per-member stanza cannot do. Both
// the config generator and the injected archive_command must agree here.
//
// nsScope MUST be the DCS-qualified name (cfg.PatroniScope(scope)): namespaces
// are how two pgcli configs on one host — or sharing one etcd — stay apart,
// and the local repo1-path plus a shared S3 bucket give no other isolation.
// A bare scope would let namespace "a" and "b" collide on pgcli_app in the
// object store.
func patroniStanzaName(nsScope string) string {
	return fmt.Sprintf("pgcli_%s", nsScope)
}

// s3StanzaLines returns the repo1 S3 override block emitted *inside* each
// Patroni stanza, or "" when no S3 repo is configured. Deliberately per-stanza
// rather than in [global]: the shared [global] repo1-path stays local, so the
// pgcli-managed regular-instance backups are completely unaffected by S3.
// Only Patroni cluster stanzas push to the object store.
func (m *BackupManager) s3StanzaLines() string {
	s3 := m.cfg.Backup.Repo.S3
	if s3 == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# S3 repo (overrides [global] repo1-path) — pgBackRest forces HTTPS for S3.\n")
	sb.WriteString("repo1-type=s3\n")
	fmt.Fprintf(&sb, "repo1-s3-endpoint=%s\n", s3.Endpoint)
	fmt.Fprintf(&sb, "repo1-s3-bucket=%s\n", s3.Bucket)
	fmt.Fprintf(&sb, "repo1-s3-region=%s\n", s3.Region)
	fmt.Fprintf(&sb, "repo1-s3-uri-style=%s\n", s3.URIStyle)
	fmt.Fprintf(&sb, "repo1-path=%s\n", s3.Path)
	fmt.Fprintf(&sb, "repo1-s3-key=%s\n", s3.AccessKey)
	fmt.Fprintf(&sb, "repo1-s3-key-secret=%s\n", s3.SecretKey)
	verify := "y"
	if s3.VerifyTLS != nil && !*s3.VerifyTLS {
		verify = "n"
	}
	fmt.Fprintf(&sb, "repo1-s3-verify-tls=%s\n", verify)
	if s3.CAFile != "" {
		sb.WriteString("repo1-s3-ca-file=/etc/pgbackrest/ca.crt\n")
	}
	return sb.String()
}

// WritePgbackrestConf generates pgbackrest.conf with all instance stanzas.
// Returns the path to the generated config file.
func (m *BackupManager) WritePgbackrestConf() (string, error) {
	var sb strings.Builder
	stanzaCount := 0

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
		stanzaCount++
	}

	// One cluster-wide stanza per Patroni scope, listing every member as an
	// additional pg*-host (cross-host members included automatically via the
	// DCS + registry — see patroniBackupTargets). pgBackRest probes the hosts,
	// finds the primary itself, and survives failovers with no config change.
	// Patroni members share the standalone PG data_dir layout
	// (PGDATA=/var/lib/postgresql/data). The S3 repo (when configured) is
	// scoped to these stanzas only.
	patroniConf, patroniStanzas := m.patroniBackupStanzas(m.patroniBackupTargets())
	sb.WriteString(patroniConf)
	stanzaCount += patroniStanzas

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

	fmt.Printf("-> pgbackrest.conf generated: %s (%d stanzas)\n", confPath, stanzaCount)
	return confPath, nil
}

// patroniBackupStanzas renders the backup-container view: one stanza per
// Patroni scope containing every member as pgN-host (pgBackRest probes them,
// finds the primary itself, and keeps working across failovers). Returns the
// text and the stanza count.
func (m *BackupManager) patroniBackupStanzas(targets []patroniBackupTarget) (string, int) {
	var sb strings.Builder
	s3 := m.s3StanzaLines()
	count := 0
	for _, scope := range scopesInOrder(targets) {
		sb.WriteString("[" + patroniStanzaName(m.cfg.PatroniScope(scope)) + "]\n")
		i := 0
		for _, t := range targets {
			if t.scope != scope {
				continue
			}
			i++
			fmt.Fprintf(&sb, "pg%d-host=%s\n", i, t.alias)
			fmt.Fprintf(&sb, "pg%d-path=/var/lib/postgresql/data\n", i)
			fmt.Fprintf(&sb, "pg%d-port=%d\n", i, t.hostPort)
			fmt.Fprintf(&sb, "pg%d-socket-path=/var/lib/postgresql\n", i)
			fmt.Fprintf(&sb, "pg%d-user=postgres\n", i)
		}
		sb.WriteString(s3)
		sb.WriteString("\n")
		count++
	}
	return sb.String(), count
}

// patroniArchiveStanzas renders the member-local view: one stanza per scope,
// no pg*-host and no ports (archive-push never connects to PostgreSQL — it
// only reads WAL files under pg1-path — and a stanza naming any pg1-host
// makes archive-push abort with [072] "must be run on the PostgreSQL host").
// Every member mounts this identical text and pushes to the identical stanza.
func (m *BackupManager) patroniArchiveStanzas(targets []patroniBackupTarget) (string, int) {
	var sb strings.Builder
	s3 := m.s3StanzaLines()
	count := 0
	for _, scope := range scopesInOrder(targets) {
		sb.WriteString("[" + patroniStanzaName(m.cfg.PatroniScope(scope)) + "]\n")
		sb.WriteString("pg1-path=/var/lib/postgresql/data\n")
		sb.WriteString("pg1-user=postgres\n")
		sb.WriteString(s3)
		sb.WriteString("\n")
		count++
	}
	return sb.String(), count
}

// scopesInOrder lists the distinct scopes in sorted order — stanza text must
// not shuffle between runs (Go map iteration is randomized).
func scopesInOrder(targets []patroniBackupTarget) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range targets {
		if !seen[t.scope] {
			seen[t.scope] = true
			out = append(out, t.scope)
		}
	}
	sort.Strings(out)
	return out
}

// WritePgbackrestArchiveConf generates the *member-local* pgbackrest.conf used
// for archive-push, mounted read-only into every Patroni member container at
// /etc/pgbackrest.conf. See patroniArchiveStanzas for why the stanza carries no
// pg*-host. The backup container keeps using WritePgbackrestConf (with
// pg*-hosts) for backup/restore, so this is a second, distinct file.
func (m *BackupManager) WritePgbackrestArchiveConf() (string, error) {
	var sb strings.Builder
	archiveConf, stanzaCount := m.patroniArchiveStanzas(m.patroniBackupTargets())
	sb.WriteString(archiveConf)

	sb.WriteString("[global]\n")
	sb.WriteString("repo1-path=/var/lib/pgbackrest\n")
	fmt.Fprintf(&sb, "repo1-retention-full=%d\n", m.cfg.Backup.RetentionFull)
	sb.WriteString("log-level-console=info\n")
	sb.WriteString("start-fast=y\n")
	sb.WriteString("compress-type=zst\n")

	confPath := filepath.Join(m.dataDir, "pgbackrest-archive.conf")
	if err := os.WriteFile(confPath, []byte(sb.String()), 0644); err != nil {
		return "", fmt.Errorf("writing pgbackrest-archive.conf: %w", err)
	}
	fmt.Printf("-> pgbackrest-archive.conf generated: %s (%d cluster stanzas, local view)\n", confPath, stanzaCount)
	return confPath, nil
}

// backupContainerDrift returns a short reason when the running backup
// container's mounts no longer match the desired config — today only the S3 CA
// file (a mount a pre-S3 container never had). "" means it's current.
func (m *BackupManager) backupContainerDrift() (string, error) {
	s3 := m.cfg.Backup.Repo.S3
	if s3 == nil || s3.CAFile == "" {
		return "", nil // nothing to drift on (local repo, or system CAs)
	}
	out, err := m.run("inspect", "--format", "{{range .Mounts}}{{.Destination}}\n{{end}}", m.cfg.Backup.ContainerName)
	if err != nil {
		return "", fmt.Errorf("inspecting backup container mounts: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "/etc/pgbackrest/ca.crt" {
			return "", nil
		}
	}
	return "missing the S3 CA mount", nil
}

// --- Container management ---------------------------------------------

// EnsureBackupContainer creates and starts the backup container if needed.
// SSH config and pgbackrest.conf are bind-mounted at fixed paths, so host file
// updates are reflected inside the container without recreation. But a newly
// configured CA mount (/etc/pgbackrest/ca.crt) can only arrive via recreate —
// a running container predating it is silently missing the cert and every S3
// op fails with [095]. So: (re)create when absent, not running, or drifting.
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
		if drift, err := m.backupContainerDrift(); err != nil {
			return err
		} else if drift != "" {
			fmt.Printf("-> Backup container is %s; recreating...\n", drift)
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing backup container: %w", err)
			}
			return m.createBackupContainer(confPath)
		}
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

// StanzaCreatePatroni runs `pgbackrest stanza-create` + `check` for every
// Patroni cluster stanza (the ones the S3 repo covers). Existing valid
// stanzas are tolerated, so this is safe to re-run on every `pg backup setup`.
// stanza-create failures are fatal (a repository that is unreachable or a
// missing leader means backups silently would not work); check failures are
// advisory, since a fresh stanza has no archived WAL yet.
func (m *BackupManager) StanzaCreatePatroni() error {
	targets := m.PatroniStanzaNames()
	if len(targets) == 0 {
		return nil
	}
	for _, t := range targets {
		out, err := m.BackupExec(false, "pgbackrest", "--stanza="+t.Stanza, "stanza-create")
		if err != nil {
			if strings.Contains(out, "already exists and is valid") || strings.Contains(out, "already valid") {
				fmt.Printf("  [OK] stanza %s (already exists)\n", t.Stanza)
				continue
			}
			return fmt.Errorf("stanza-create %s: %w\n%s", t.Stanza, err, strings.TrimSpace(out))
		}
		fmt.Printf("  [OK] stanza %s created\n", t.Stanza)
	}
	for _, t := range targets {
		out, err := m.BackupExec(false, "pgbackrest", "--stanza="+t.Stanza, "check")
		if err != nil {
			// A brand-new stanza has no archived WAL yet, so check legitimately
			// fails until the first segment is pushed. Advisory, not fatal.
			fmt.Printf("  [!] check %s: %s\n      (expected on a fresh stanza until the first WAL segment archives; force one with SELECT pg_switch_wal())\n",
				t.Stanza, strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
			continue
		}
		fmt.Printf("  [OK] check %s\n", t.Stanza)
	}
	return nil
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
	)
	if ca := m.cfg.Backup.Repo.S3; ca != nil && ca.CAFile != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/etc/pgbackrest/ca.crt:ro,z", hostMountPath(ca.CAFile)))
	}
	args = append(args, m.cfg.Backup.ImageTag)

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
