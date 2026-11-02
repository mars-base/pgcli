package podman

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/embed"
	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// DocumentDBBackendUser is the dedicated PostgreSQL role the gateway connects
// as over the shared Unix socket. The official .deb only supports passwordless
// local auth (peer/trust), so the role is created LOGIN SUPERUSER by the
// install path and the URL carries no password.
const DocumentDBBackendUser = "docdb_gateway"

// DocumentDB container-side paths, fixed by the image + upstream daemon.
const (
	documentdbPgURLMount    = "/etc/documentdb/gateway/pg-url"
	documentdbSetupCfgMount = "/etc/documentdb/gateway/SetupConfiguration.json"
	documentdbTLSStateMount = "/var/lib/documentdb-gateway"
	documentdbSocketMount   = "/var/run/postgresql"
)

// DocumentDBManager manages the DocumentDB gateway add-on — the official Rust
// service that exposes the MongoDB wire protocol on top of the DocumentDB
// PostgreSQL extension. It is local-instance-only: the official .deb rejects
// password authentication and connects over a Unix socket shared from the
// target PG container (SocketVolumeName). Clients authenticate as DocumentDB
// API users, while the gateway itself connects to PostgreSQL as
// DocumentDBBackendUser.
type DocumentDBManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

// NewDocumentDBManager creates a DocumentDBManager. Linux serves over host
// networking; macOS uses the pgcli-net bridge with the port published. The
// Unix-socket connection to the target instance needs the shared socket
// volume, which is provided by `pg extension install documentdb`.
func NewDocumentDBManager(cfg *config.Config) (*DocumentDBManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &DocumentDBManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// DocumentDBConfigDir returns the host directory holding a gateway's rendered
// pg-url and SetupConfiguration.json.
func DocumentDBConfigDir(baseDir, name string) string {
	return filepath.Join(baseDir, "addon", "documentdb", name)
}

// DocumentDBTLSVolumeName returns the named volume that persists the
// auto-generated self-signed TLS certificate across container recreation.
func DocumentDBTLSVolumeName(name string) string {
	return "pgcli-documentdb-tls-" + name
}

// documentdbListenAddr maps the configured bind address to the daemon's
// DOCUMENTDB_LISTEN_ADDR format. The daemon rejects a literal "0.0.0.0"
// ("[::]" is the all-addresses form), so the wildcard is rendered as ":port".
func documentdbListenAddr(listen string, port int, bridge bool) string {
	if bridge {
		return ":" + strconv.Itoa(port)
	}
	switch listen {
	case "", "0.0.0.0":
		return ":" + strconv.Itoa(port)
	case "127.0.0.1":
		return "127.0.0.1:" + strconv.Itoa(port)
	case "::1":
		return "[::1]:" + strconv.Itoa(port)
	default:
		return net.JoinHostPort(listen, strconv.Itoa(port))
	}
}

// documentdbGatewayArgs builds the full `podman run` argv for a gateway.
// Pure function (no podman calls) so the mount/env contract is unit-testable:
//   - The target PG socket volume is mounted read-only; the gateway never
//     writes to the socket directory.
//   - pg-url and SetupConfiguration.json are managed host files, mounted
//     read-only with :ro,z.
//   - The TLS state volume is writable and :U-chowned to the image's
//     documentdb-gateway user, so the daemon can persist cert.pem/pkey.pem.
//   - netFlags: --network host on Linux, bridge + -p on macOS.
func documentdbGatewayArgs(dc *config.DocumentDBConfig, socketVol, urlFile, configFile, tlsVol string, bridge bool, network string) []string {
	args := []string{"run", "-d", "--name", dc.ContainerName}
	args = append(args, netFlags(bridge, network, dc.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:ro,z", socketVol, documentdbSocketMount),
		"-v", fmt.Sprintf("%s:%s:ro,U,z", urlFile, documentdbPgURLMount),
		"-v", fmt.Sprintf("%s:%s:ro,U,z", configFile, documentdbSetupCfgMount),
		"-v", fmt.Sprintf("%s:%s:U,z", tlsVol, documentdbTLSStateMount),
		"-e", "DOCUMENTDB_PG_URL_FILE="+documentdbPgURLMount,
		"-e", "DOCUMENTDB_LISTEN_ADDR="+documentdbListenAddr(dc.Listen, dc.HostPort, bridge),
		"-e", "DOCUMENTDB_TLS_AUTO_GENERATE=true",
		"-e", "DOCUMENTDB_TLS_STATE_DIR="+documentdbTLSStateMount+"/tls",
		"-e", "DOCUMENTDB_LOG_LEVEL=info",
		dc.ImageTag,
		"run",
	)
	return args
}

// documentdbPgURL renders the passwordless libpq URL the gateway reads from
// its pg-url file. pgPort is the PostgreSQL server port of the target
// instance (the same port PGPORT is set to inside the target container), so
// the socket file is /var/run/postgresql/.s.PGSQL.<pgPort>.
func documentdbPgURL(pgPort int, database string) string {
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.User(DocumentDBBackendUser),
		Path:     "/" + database,
		RawQuery: "host=/var/run/postgresql&port=" + strconv.Itoa(pgPort),
	}
	return u.String()
}

// renderDocumentDBSetupConfiguration renders the gateway config from the
// embedded upstream default. Telemetry stays explicitly disabled, and
// EnforceTls follows TLSMode (require → true).
func renderDocumentDBSetupConfiguration(tlsMode string) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(embed.DocumentDBGatewaySetupConfigurationJSON), &doc); err != nil {
		return "", fmt.Errorf("parsing embedded DocumentDB gateway config: %w", err)
	}
	doc["EnforceTls"] = tlsMode == "require"
	if telemetry, ok := doc["TelemetryOptions"].(map[string]any); ok {
		for _, section := range []string{"Metrics", "Tracing"} {
			if opts, ok := telemetry[section].(map[string]any); ok {
				opts["Enabled"] = false
			}
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding DocumentDB gateway config: %w", err)
	}
	return string(out) + "\n", nil
}

// WriteFiles renders pg-url and SetupConfiguration.json under the gateway's
// data dir and returns their host paths. Both are mode 0640 — they contain no
// secrets (the backend URL is passwordless and the client password lives only
// in pg.yaml) but the upstream daemon warns above 0640. The mounts carry :U,
// so podman rewrites their group to the container's gateway user on start.
func (m *DocumentDBManager) WriteFiles(dc *config.DocumentDBConfig, pgPort int) (urlFile, configFile string, err error) {
	dir := DocumentDBConfigDir(m.dataDir, dc.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("creating DocumentDB gateway config dir: %w", err)
	}
	urlFile = filepath.Join(dir, "pg-url")
	configFile = filepath.Join(dir, "SetupConfiguration.json")
	if err := os.WriteFile(urlFile, []byte(documentdbPgURL(pgPort, dc.Database)+"\n"), 0640); err != nil {
		return "", "", fmt.Errorf("writing pg-url: %w", err)
	}
	if err := os.Chmod(urlFile, 0640); err != nil {
		return "", "", fmt.Errorf("setting pg-url mode: %w", err)
	}
	cfg, err := renderDocumentDBSetupConfiguration(dc.TLSMode)
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(configFile, []byte(cfg), 0640); err != nil {
		return "", "", fmt.Errorf("writing SetupConfiguration.json: %w", err)
	}
	if err := os.Chmod(configFile, 0640); err != nil {
		return "", "", fmt.Errorf("setting SetupConfiguration.json mode: %w", err)
	}
	return urlFile, configFile, nil
}

// EnsureImage pulls the gateway image if it is not present locally (pull-only;
// pgcli never builds it at install time). Offline hosts pre-seed it with
// `podman load`.
func (m *DocumentDBManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling DocumentDB gateway image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the gateway container (install
// semantics: stop/rm/recreate so changed port/listen/TLS/config take effect).
func (m *DocumentDBManager) EnsureContainer(dc *config.DocumentDBConfig, urlFile, configFile string) error {
	containerName := dc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> DocumentDB gateway container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping DocumentDB gateway container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing DocumentDB gateway container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale DocumentDB gateway container: %w", err)
			}
		}
	}

	if err := m.createContainer(dc, urlFile, configFile); err != nil {
		return err
	}
	fmt.Println("  [OK] DocumentDB gateway container started")
	return nil
}

func (m *DocumentDBManager) createContainer(dc *config.DocumentDBConfig, urlFile, configFile string) error {
	target := m.targetContainerName(dc)
	if target == "" {
		return fmt.Errorf("target instance %q not found", dc.TargetInstance)
	}
	tlsVol := DocumentDBTLSVolumeName(dc.Name)
	args := documentdbGatewayArgs(dc, SocketVolumeName(target), urlFile, configFile, tlsVol, m.bridge, m.cfg.Podman.Network)
	slog.Debug("podman documentdb run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating DocumentDB gateway container: %w", err)
	}
	return nil
}

// targetContainerName resolves the PG container whose socket volume the
// gateway shares.
func (m *DocumentDBManager) targetContainerName(dc *config.DocumentDBConfig) string {
	if inst, ok := m.cfg.Instances[dc.TargetInstance]; ok {
		return inst.Podman.ContainerName
	}
	return ""
}

// StartContainer starts the gateway container (autostart and `pg addon start`
// paths): running → no-op; exists but stopped → podman start, recreating on
// improper state; missing → create fresh.
func (m *DocumentDBManager) StartContainer(dc *config.DocumentDBConfig, urlFile, configFile string) error {
	containerName := dc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] DocumentDB gateway %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] DocumentDB gateway %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] DocumentDB gateway %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper DocumentDB gateway container: %w", err)
		}
	}

	if err := m.createContainer(dc, urlFile, configFile); err != nil {
		return err
	}
	fmt.Printf("  [OK] DocumentDB gateway %s started\n", containerName)
	return nil
}

// Remove stops and removes the gateway container. With cleanData it also
// deletes the persisted TLS state volume and the rendered config directory.
// PostgreSQL-side roles and DocumentDB data are NOT touched.
func (m *DocumentDBManager) Remove(dc *config.DocumentDBConfig, cleanData bool) error {
	containerName := dc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing DocumentDB gateway container: %w", err)
	}
	fmt.Println("  [OK] DocumentDB gateway container removed")
	if cleanData {
		if _, err := m.run("volume", "rm", "-f", DocumentDBTLSVolumeName(dc.Name)); err != nil {
			fmt.Printf("  [!] Could not remove TLS volume: %v\n", err)
		} else {
			fmt.Println("  [OK] TLS state volume removed")
		}
		dir := DocumentDBConfigDir(m.dataDir, dc.Name)
		if err := os.RemoveAll(dir); err != nil {
			fmt.Printf("  [!] Could not remove config dir %s: %v\n", dir, err)
		} else {
			fmt.Printf("  [OK] Config dir removed: %s\n", dir)
		}
	}
	return nil
}

// EnsureDocumentDBLocalTrust guarantees role can authenticate over the local
// Unix socket without a password. pgcli's default pg_hba.conf already carries
// `local all all trust`; a custom file gets one managed rule prepended, since
// pg_hba matching stops at the first hit.
func (m *Manager) EnsureDocumentDBLocalTrust(role string) error {
	const hbaPath = "/var/lib/postgresql/data/pg_hba.conf"
	escaped := strings.ReplaceAll(role, "'", "''")
	out, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-t", "-A", "-c",
		fmt.Sprintf("SELECT count(*) FROM pg_hba_file_rules WHERE type = 'local' AND auth_method = 'trust' AND ('all' = ANY(user_name) OR '%s' = ANY(user_name))", escaped))
	if err != nil {
		return fmt.Errorf("checking pg_hba rules: %w", err)
	}
	if strings.TrimSpace(out) != "0" {
		return nil
	}

	content, err := m.Exec("cat", hbaPath)
	if err != nil {
		return fmt.Errorf("reading pg_hba.conf: %w", err)
	}
	block := "# === pgcli documentdb gateway (managed - do not edit) ===\n" +
		"local all " + role + " trust\n" + content
	tmp, err := os.CreateTemp("", "pgcli-docdb-hba-*.conf")
	if err != nil {
		return fmt.Errorf("creating temp pg_hba.conf: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(block); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp pg_hba.conf: %w", err)
	}
	tmp.Close()

	if _, err := m.run("cp", tmpPath, m.cfg.Podman.ContainerName+":"+hbaPath); err != nil {
		return fmt.Errorf("podman cp pg_hba.conf: %w", err)
	}
	if err := m.chownDataFile(m.cfg.Podman.ContainerName, hbaPath); err != nil {
		return fmt.Errorf("chown pg_hba.conf: %w", err)
	}
	if _, err := m.Exec("psql", "-U", m.cfg.Postgres.User, "-d", m.cfg.Postgres.Database,
		"-c", "SELECT pg_reload_conf()"); err != nil {
		return fmt.Errorf("reloading pg_hba.conf: %w", err)
	}
	fmt.Printf("  [OK] local trust rule added for role %s\n", role)
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *DocumentDBManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists.
func (m *DocumentDBManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a gateway container.
func (m *DocumentDBManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *DocumentDBManager) run(args ...string) (string, error) {
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

func (m *DocumentDBManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *DocumentDBManager) imageExists(tag string) (bool, error) {
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

func (m *DocumentDBManager) containerExists(name string) (bool, error) {
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

func (m *DocumentDBManager) containerRunning(name string) (bool, error) {
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
