package podman

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// PgAdminManager manages standalone pgAdmin 4 containers — the official web
// administration UI for PostgreSQL. Like Redis it is a top-level, persistent
// addon (it owns a host data dir for its session/config database), and like
// PostgREST it is configured entirely through environment variables with no
// config file to render. The upstream image (docker.io/dpage/pgadmin4) is
// multi-arch and pull-only, so pgcli never builds it.
//
// Ownership is the one runtime subtlety, and pgAdmin handles it in-image rather
// than requiring a wrapper (the rustfs contrast): its default USER is 5050, but
// its /entrypoint.sh has a root branch — when the container is launched as root
// it `chown -R`s /var/lib/pgadmin to pgadmin (PUID, default 5050) and then
// su-execs gunicorn down to that user. pgcli therefore runs it with `--user 0`
// so a fresh host bind mount (owned by whoever ran `pg`) is made writable by
// the container itself, with no host-side chown and no wrapper image. Under
// rootless podman `--user 0` is still just the user's mapped root, so this
// stays unprivileged on the host.
//
// The corollary for teardown: the persisted files land owned by the mapped
// 5050 uid, which the host user cannot `rm` directly — so Remove deletes the
// data dir through removeHostDir, whose `podman unshare rm` fallback reclaims
// them (the same path Redis uses).
type PgAdminManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

// pgadmin container-side paths, fixed by the image contract.
const (
	pgAdminContainerDataDir    = "/var/lib/pgadmin" // session/config db; entrypoint chowns to 5050
	pgAdminContainerServersDir = "/pgadmin4"        // entrypoint loads servers.json from here on first launch
)

// NewPgAdminManager creates a PgAdminManager. It works on both platforms: Linux
// serves over host networking, macOS over the pgcli-net bridge with the port
// published, so the Mac's 127.0.0.1:<port> reaches it. The upstream image is
// multi-arch, so any host architecture works.
func NewPgAdminManager(cfg *config.Config) (*PgAdminManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &PgAdminManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/pgadmin/<name>/data. It holds the
// pgAdmin config/session DB and outlives the container — `pg addon remove`
// keeps it unless --clean-data, so reinstalling under the same name revives the
// saved servers and settings.
func (m *PgAdminManager) resolveDataDir(ac *config.PgAdminConfig) string {
	if ac.DataDir != "" {
		return ac.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "pgadmin", ac.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *PgAdminManager) DataDir(ac *config.PgAdminConfig) string {
	return m.resolveDataDir(ac)
}

// serversJSONPath returns the host path pgcli writes an optional pre-loaded
// servers.json to (beside the data dir, under the instance's addon dir).
func (m *PgAdminManager) serversJSONPath(ac *config.PgAdminConfig) string {
	return filepath.Join(m.dataDir, "addon", "pgadmin", ac.Name, "servers.json")
}

// pgPassPath returns the host path of the optional seed passfile, beside
// servers.json. The file holds the seeded server's PostgreSQL password in
// plaintext (libpq pgpass format) and is removed on every reinstall/remove.
func (m *PgAdminManager) pgPassPath(ac *config.PgAdminConfig) string {
	return filepath.Join(m.dataDir, "addon", "pgadmin", ac.Name, "pgpass")
}

// EnsureImage pulls the pgAdmin image if it is not present locally (pull-only:
// the tags are public docker.io/dpage images; pgcli never builds them). Offline
// hosts pre-seed them with `podman load` like the core images.
func (m *PgAdminManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling pgAdmin image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the pgAdmin container (install semantics:
// stop/rm/recreate so updated ports/credentials take effect). The data dir is
// created if missing; it is never deleted here.
func (m *PgAdminManager) EnsureContainer(ac *config.PgAdminConfig) error {
	containerName := ac.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> pgAdmin container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping pgAdmin container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing pgAdmin container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale pgAdmin container: %w", err)
			}
		}
	}

	if err := m.createContainer(ac); err != nil {
		return err
	}
	fmt.Println("  [OK] pgAdmin container started")
	return nil
}

// StartContainer starts the pgAdmin container without touching config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh.
func (m *PgAdminManager) StartContainer(ac *config.PgAdminConfig) error {
	containerName := ac.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] pgAdmin %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] pgAdmin %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] pgAdmin %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper pgAdmin container: %w", err)
		}
	}

	if err := m.createContainer(ac); err != nil {
		return err
	}
	fmt.Printf("  [OK] pgAdmin %s started\n", containerName)
	return nil
}

// pgadminArgsAndEnv builds the full `podman run` argv for a pgAdmin instance.
// Pure function (no podman calls) so the env mapping, the ownership-triggering
// `--user 0`, the networking shape and the optional servers.json mount are all
// unit-testable:
//   - PGADMIN_DEFAULT_EMAIL / PGADMIN_DEFAULT_PASSWORD are pgAdmin's WEB LOGIN
//     credentials (required at launch); they are emitted verbatim.
//   - PGADMIN_LISTEN_PORT pins gunicorn's port to the assigned host port (the
//     image defaults to 80, and the entrypoint's privileged-bind fallback picks
//     8080 in a restricted context — pinning sidesteps both).
//   - --user 0 makes the entrypoint take its root branch: chown /var/lib/pgadmin
//     to pgadmin (5050) then su-exec down, so a fresh host dir needs no host
//     chown (see the PgAdminManager doc).
//   - PGADMIN_DISABLE_POSTFIX is set to skip the container's local Postfix
//     (pgcli never wires up password-reset email).
//   - serversJSONHostPath non-empty bind-mounts a pre-loaded servers.json at the
//     image's default /pgadmin4/servers.json and sets
//     PGADMIN_REPLACE_SERVERS_ON_STARTUP=True so a re-rendered file takes effect
//     on every start (declarative), not just first launch.
//   - pgPassHostPath non-empty bind-mounts the seed passfile at
//     /var/lib/pgadmin/pgpass (the path servers.json's passfile points at, so
//     the seeded server connects without prompting). Deliberately NOT :ro: the
//     entrypoint's chown -R must be able to fix its owner to 5050 on every
//     start (libpq refuses a passfile not owned by the connecting uid), and
//     chown on a read-only mount fails with EROFS.
//   - The bind address goes through proxyBindHost so a loopback listen is
//     widened to 0.0.0.0 under bridge (the published port must be reachable).
//
// dataDir is the caller's already-resolved host data dir (the manager resolves
// it from base+name+override), so this stays a pure function of its arguments.
func pgadminArgsAndEnv(ac *config.PgAdminConfig, bridge bool, network, dataDir, serversJSONHostPath, pgPassHostPath string) []string {
	args := []string{"run", "-d", "--name", ac.ContainerName}
	args = append(args, netFlags(bridge, network, ac.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--user", "0", // trigger the entrypoint's chown-and-drop-to-5050 branch
		"-e", "PGADMIN_DEFAULT_EMAIL="+ac.Email,
		"-e", "PGADMIN_DEFAULT_PASSWORD="+ac.Password,
		"-e", "PGADMIN_LISTEN_PORT="+strconv.Itoa(ac.HostPort),
		"-e", "PGADMIN_LISTEN_ADDRESS="+proxyBindHost(bridge, ac.Listen),
		"-e", "PGADMIN_DISABLE_POSTFIX=1",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), pgAdminContainerDataDir),
	)
	if serversJSONHostPath != "" {
		args = append(args,
			"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(serversJSONHostPath), filepath.Join(pgAdminContainerServersDir, "servers.json")),
			"-e", "PGADMIN_REPLACE_SERVERS_ON_STARTUP=True",
		)
	}
	if pgPassHostPath != "" {
		// No :ro here — see the pgPassHostPath note in the doc comment.
		args = append(args,
			"-v", fmt.Sprintf("%s:%s:z", hostMountPath(pgPassHostPath), pgAdminContainerPgPass),
		)
	}
	args = append(args, ac.ImageTag)
	return args
}

// createContainer runs a pgAdmin instance. The data dir is created on the host
// (owned by the current user); the container's own entrypoint — entered via
// `--user 0` — chowns it to uid 5050 before dropping privileges, so pgcli
// never chowns. When the config carries a DSN, an optional servers.json is
// rendered beside the data dir and mounted so the UI opens with that server
// pre-registered; when that DSN also carries a password, a libpq pgpass file is
// rendered alongside and referenced from servers.json, so the seeded server
// connects without a password prompt (a password-less DSN keeps the old
// prompt-on-first-connect behaviour).
func (m *PgAdminManager) createContainer(ac *config.PgAdminConfig) error {
	dataDir := m.resolveDataDir(ac)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating pgAdmin data dir %s: %w", dataDir, err)
	}

	var serversJSON, pgPass string
	if ac.DSN != "" {
		pgPassContainer := ""
		if err := WritePgPass(m.pgPassPath(ac), ac.DSN); err != nil {
			if !errors.Is(err, errNoPgPassPassword) {
				return fmt.Errorf("rendering pgAdmin pgpass: %w", err)
			}
		} else {
			pgPass = m.pgPassPath(ac)
			pgPassContainer = pgAdminContainerPgPass
		}
		path := m.serversJSONPath(ac)
		if err := WriteServersJSON(path, ac.DSN, ac.ServerName, pgPassContainer); err != nil {
			return fmt.Errorf("rendering pgAdmin servers.json: %w", err)
		}
		serversJSON = path
	}

	args := pgadminArgsAndEnv(ac, m.bridge, m.cfg.Podman.Network, dataDir, serversJSON, pgPass)
	slog.Debug("podman pgadmin run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating pgAdmin container: %w", err)
	}
	return nil
}

// Remove stops and removes the pgAdmin container. The data dir is kept by
// default (it holds the config/session DB — remove+reinstall revives the saved
// servers); cleanData also deletes it, refusing when it is still a mount point.
func (m *PgAdminManager) Remove(ac *config.PgAdminConfig, cleanData bool) error {
	containerName := ac.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing pgAdmin container: %w", err)
	}
	fmt.Println("  [OK] pgAdmin container removed")

	// The optional servers.json and seed pgpass are pgcli-authored (host-owned),
	// so a plain Remove suffices — delete them regardless of cleanData so a
	// reinstall without --dsn/--pg-name doesn't silently re-mount a stale file,
	// and so the plaintext seed password does not outlive its instance.
	for _, p := range []string{m.serversJSONPath(ac), m.pgPassPath(ac)} {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] Warning: removing %s: %v\n", p, err)
		}
	}

	dataDir := m.resolveDataDir(ac)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if isMountpoint(dataDir) {
		fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
		return nil
	}
	// pgAdmin writes as the container's mapped 5050 uid, which os.RemoveAll
	// cannot delete from the host — removeHostDir falls back to `podman unshare
	// rm` (same path redis/rustfs need for their mapped-uid files).
	if err := removeHostDir(m.podman, ac.ImageTag, dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if ac.DataDir == "" {
		// Default layout: also prune the <name> dir when it became empty (it now
		// holds data/ + servers.json; servers.json is gone, data/ just removed).
		// With an override the parent belongs to the user — never touch it.
		_, _ = removeHostDirIfEmpty(m.podman, ac.ImageTag, filepath.Dir(dataDir))
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *PgAdminManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *PgAdminManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a pgAdmin container.
func (m *PgAdminManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *PgAdminManager) run(args ...string) (string, error) {
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

func (m *PgAdminManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *PgAdminManager) imageExists(tag string) (bool, error) {
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

func (m *PgAdminManager) containerExists(name string) (bool, error) {
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

func (m *PgAdminManager) containerRunning(name string) (bool, error) {
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
