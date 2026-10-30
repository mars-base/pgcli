package podman

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// PGExporterManager manages pg_exporter containers — Pigsty's declarative
// PostgreSQL metrics exporter (https://pigsty.cc/docs/pg_exporter/). Like
// node_exporter it is a top-level, single stateless container on one host port
// with no data dir and nothing rendered to disk, but it exports a DATABASE's
// metrics rather than the host's, so it must be handed a connection URL.
//
// The whole default collector set (460-odd queries) is baked into the binary and
// planned at runtime by server version / role / extensions, so pgcli needs NO
// config file to get useful metrics. The target is provided via the
// PG_EXPORTER_URL env, and pg_exporter's own flag surface (--auto-discovery,
// --exclude-database, --tag, ...) is reachable through ExtraArgs.
//
// The exporter connects as a low-privilege MONITOR role (pg_monitor member),
// never the target's admin: SetupMonitorRole creates it idempotently on the
// target using an admin DSN that the caller holds only transiently. That monitor
// URL — not the admin URL — is what lands in pg.yaml and in the container env.
//
// There is NO authentication or TLS on the exporter endpoint, and it also serves
// admin endpoints (/reload, /explain, /stat), so it defaults to 0.0.0.0 with the
// same eyes-open posture as the other metrics addons — guarded by the network
// boundary. SIGHUP hot-reloads the COLLECTOR config only; URL/port/listen changes
// still require recreation.
type PGExporterManager struct {
	cfg    *config.Config
	podman string // podman binary path
	bridge bool   // macOS: serve on the pgcli-net bridge with published ports
}

// pgExporterConfigMount is where an optional user collector YAML lands inside the
// container; PG_EXPORTER_CONFIG points the binary at it. pg_exporter's own
// default config path is /etc/pg_exporter.yml, so the mount doubles as the
// natural location.
const pgExporterConfigMount = "/etc/pg_exporter.yml"

// NewPGExporterManager creates a PGExporterManager. Linux serves over host
// networking (so the exporter binds the host port directly and, crucially, can
// dial a local instance's 127.0.0.1 target DSN); macOS runs on the pgcli-net
// bridge with the port published. The upstream image is multi-arch. macOS local-
// instance mode cannot reach the host's loopback PG and is not usable there —
// the CLI layer documents that rather than half-solving it.
func NewPGExporterManager(cfg *config.Config) (*PGExporterManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	ensurePodmanStateReady(path)
	return &PGExporterManager{
		cfg:    cfg,
		podman: path,
		bridge: platform.Detect() == platform.MacOS,
	}, nil
}

// pgExporterArgs builds the full `podman run` argv for a pg_exporter instance.
// Pure function (no podman calls) so the env-injection and flag shape is unit-
// testable — the load-bearing contract is that the CONTAINER gets the monitor
// DSN (dsn, supplied by the caller) via PG_EXPORTER_URL, never an admin URL:
//   - NO --user flag and NO data mount: pg_exporter is stateless and its image
//     runs unprivileged.
//   - netFlags: --network host on Linux (exporter binds the host port and can
//     reach a local 127.0.0.1 target), bridge + -p on macOS.
//   - ConfigFile, when set, is mounted read-only (:ro,z — a user file the
//     container reads, the managed-user-file convention) at /etc/pg_exporter.yml
//     and selected via PG_EXPORTER_CONFIG.
//   - After ImageTag, pg_exporter's OWN flags: --web.listen-address=<bind>:<port>
//     (widened via proxyBindHost on macOS). ExtraArgs come LAST so a user can
//     override anything (last-wins), e.g. --auto-discovery=false.
func pgExporterArgs(pc *config.PGExporterConfig, dsn string, bridge bool, network string) []string {
	args := []string{"run", "-d", "--name", pc.ContainerName}
	args = append(args, netFlags(bridge, network, pc.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-e", "PG_EXPORTER_URL="+dsn,
	)
	if pc.ConfigFile != "" {
		args = append(args,
			"-v", fmt.Sprintf("%s:%s:ro,z", pc.ConfigFile, pgExporterConfigMount),
			"-e", "PG_EXPORTER_CONFIG="+pgExporterConfigMount,
		)
	}
	args = append(args, pc.ImageTag)
	args = append(args,
		fmt.Sprintf("--web.listen-address=%s:%d", proxyBindHost(bridge, pc.Listen), pc.HostPort),
	)
	// ExtraArgs LAST — last-wins semantics for pg_exporter CLI flags.
	args = append(args, pc.ExtraArgs...)
	return args
}

// createContainer runs a pg_exporter instance with the given (monitor) DSN.
// There is no data dir to create and no chown — the exporter is stateless.
func (m *PGExporterManager) createContainer(pc *config.PGExporterConfig, dsn string) error {
	args := pgExporterArgs(pc, dsn, m.bridge, m.cfg.Podman.Network)
	slog.Debug("podman pgexporter run", "args", redactExporterArgs(args, dsn))
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating pg_exporter container: %w", err)
	}
	return nil
}

// redactExporterArgs replaces the PG_EXPORTER_URL value in a logged argv so a
// debug trace never leaks the monitor password (the DSN is otherwise visible).
func redactExporterArgs(args []string, dsn string) []string {
	if dsn == "" {
		return args
	}
	out := make([]string, len(args))
	copy(out, args)
	want := "PG_EXPORTER_URL=" + dsn
	for i, a := range out {
		if a == want {
			out[i] = "PG_EXPORTER_URL=<redacted>"
		}
	}
	return out
}

// SetupMonitorRole creates (or resets) the low-privilege role pg_exporter
// connects as, and returns its generated plaintext password. It mirrors
// PgBouncerManager.SetupAuth: crypto/rand password, idempotent DO-block
// (CREATE ROLE ... ELSE ALTER ROLE ... PASSWORD), run through a throwaway psql
// via ExecDSNQuery against the ADMIN dsn the caller supplies. Nothing about the
// admin DSN is retained — only the returned user/password are, by the caller.
//
// Beyond LOGIN + CONNECTION LIMIT 5, the role gets pg_monitor membership (the
// documented requirement for the full collector set), plus the PG10-13-only
// SELECT on pg_subscription.oid that pg_exporter needs for its subscription
// queries (harmless on newer servers where the grant is unnecessary).
//
// monitorUser is embedded in SQL, so it must be a plain identifier — validated
// here, not trusted from the flag.
func (m *PGExporterManager) SetupMonitorRole(adminDSN, monitorUser string) (string, error) {
	if !ValidMonitorUser(monitorUser) {
		return "", fmt.Errorf("invalid monitor user name %q: use letters, digits and '_', starting with a letter (max 63 chars)", monitorUser)
	}
	pm, err := New(m.cfg)
	if err != nil {
		return "", err
	}

	pwBytes := make([]byte, 16)
	if _, err := rand.Read(pwBytes); err != nil {
		return "", fmt.Errorf("generating random password: %w", err)
	}
	password := "pgx_" + hex.EncodeToString(pwBytes)

	// Idempotent: create the role or rotate its password if it exists, then
	// always re-grant pg_monitor. pg_catalog-qualified so it works regardless of
	// the target database's search_path.
	setupSQL := fmt.Sprintf(`
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN
    CREATE ROLE "%[1]s" LOGIN CONNECTION LIMIT 5 PASSWORD '%[2]s';
  ELSE
    ALTER ROLE "%[1]s" WITH LOGIN CONNECTION LIMIT 5 PASSWORD '%[2]s';
  END IF;
END $$;

DO $$ BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_auth_members m
    JOIN pg_roles r ON r.oid = m.roleid
    WHERE m.member = (SELECT oid FROM pg_roles WHERE rolname = '%[1]s')
      AND r.rolname = 'pg_monitor'
  ) THEN
    GRANT pg_monitor TO "%[1]s";
  END IF;
END $$;

GRANT SELECT (oid) ON pg_catalog.pg_subscription TO "%[1]s";
`, monitorUser, password)

	if _, err := pm.ExecDSNQuery(adminDSN, setupSQL); err != nil {
		return "", fmt.Errorf("setting up monitor role %q: %w", monitorUser, err)
	}
	return password, nil
}

// ValidMonitorUser reports whether name is safe to embed as a quoted role name:
// a plain identifier (no quotes, semicolons, whitespace, or dots that could
// smuggle SQL), starting with a letter or underscore, max 63 chars (PG's
// NAMEDATALEN-1). Anything else can never be a valid role name anyway.
func ValidMonitorUser(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	if name[0] != '_' && (name[0] < 'a' || name[0] > 'z') && (name[0] < 'A' || name[0] > 'Z') {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || c == '$' || c == '-' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

// BuildMonitorDSN rewrites targetDSN to connect as monitorUser with the given
// password, keeping host/port/database and any query params. This is the URL
// handed to the container and stored in pg.yaml — the admin credentials are
// discarded. The target may carry NO user/password (an --admin-dsn is stored
// credential-free), so it is parsed with net/url directly rather than ParseDSN,
// which demands a password.
func BuildMonitorDSN(targetDSN, monitorUser, monitorPassword string) (string, error) {
	u, err := url.Parse(targetDSN)
	if err != nil {
		return "", fmt.Errorf("parsing target DSN: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("target DSN must be a postgres:// or postgresql:// URL")
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid DSN: missing host")
	}
	host := u.Hostname()
	port := 5432
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil {
			return "", fmt.Errorf("invalid DSN: bad port %q", p)
		}
	}
	database := strings.TrimPrefix(u.Path, "/")
	out := url.URL{
		Scheme:   u.Scheme,
		User:     url.UserPassword(monitorUser, monitorPassword),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		RawQuery: u.RawQuery,
	}
	if database != "" {
		out.Path = "/" + database
	}
	return out.String(), nil
}

// EnsureImage pulls the pg_exporter image if it is not present locally
// (pull-only: public docker.io/pgsty tags; pgcli never builds them). Offline
// hosts pre-seed it with `podman load` like the core images.
func (m *PGExporterManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling pg_exporter image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the pg_exporter container with the given
// monitor DSN (install semantics: stop/rm/recreate so changed port/listen/URL/
// extra-args take effect).
func (m *PGExporterManager) EnsureContainer(pc *config.PGExporterConfig, dsn string) error {
	containerName := pc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> pg_exporter container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping pg_exporter container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing pg_exporter container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale pg_exporter container: %w", err)
			}
		}
	}

	if err := m.createContainer(pc, dsn); err != nil {
		return err
	}
	fmt.Println("  [OK] pg_exporter container started")
	return nil
}

// StartContainer starts the pg_exporter container with the given monitor DSN
// (autostart-on-boot and `pg addon start` paths): running → no-op; exists but
// stopped → podman start, recreating on improper state; missing → create fresh.
// The caller re-derives dsn first so a rotated instance password is picked up.
func (m *PGExporterManager) StartContainer(pc *config.PGExporterConfig, dsn string) error {
	containerName := pc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] pg_exporter %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] pg_exporter %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] pg_exporter %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper pg_exporter container: %w", err)
		}
	}

	if err := m.createContainer(pc, dsn); err != nil {
		return err
	}
	fmt.Printf("  [OK] pg_exporter %s started\n", containerName)
	return nil
}

// Reload sends SIGHUP to hot-reload the collector config. pg_exporter only
// re-reads its YAML on SIGHUP, so with pgcli's baked-in-default posture (no
// --config-file) this is a no-op unless the user mounted one; the CLI layer says
// so. URL/port/listen changes still need recreation.
func (m *PGExporterManager) Reload(containerName string) error {
	if _, err := m.run("kill", "--signal", "HUP", containerName); err != nil {
		return fmt.Errorf("reloading pg_exporter %s: %w", containerName, err)
	}
	return nil
}

// Remove stops and removes the pg_exporter container. There is no data dir to
// keep or clean — the exporter is stateless — so cleanData only controls the
// "nothing to delete" notice. Reinstalling always starts fresh.
func (m *PGExporterManager) Remove(pc *config.PGExporterConfig, cleanData bool) error {
	containerName := pc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing pg_exporter container: %w", err)
	}
	fmt.Println("  [OK] pg_exporter container removed")
	if cleanData {
		fmt.Println("  [OK] No data to clean (pg_exporter is stateless)")
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *PGExporterManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not).
func (m *PGExporterManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a pg_exporter container.
func (m *PGExporterManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *PGExporterManager) run(args ...string) (string, error) {
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

func (m *PGExporterManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *PGExporterManager) imageExists(tag string) (bool, error) {
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

func (m *PGExporterManager) containerExists(name string) (bool, error) {
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

func (m *PGExporterManager) containerRunning(name string) (bool, error) {
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
