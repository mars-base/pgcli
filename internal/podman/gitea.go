package podman

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// GiteaManager manages standalone Gitea containers — the self-hosted Git
// service (https://about.gitea.com/). It is a top-level, persistent web-UI
// addon in the Grafana shape: one host port, configuration entirely through
// environment variables (GITEA__<section>__<KEY>, which the image's
// environment-to-ini folds into app.ini at every boot), an auto-generated
// admin login, and a host /data dir outliving the container so
// remove+reinstall revives repos and settings.
//
// The image is docker.io/gitea/gitea (config.DefaultGiteaImageTag — note the
// version line moved from 1.2x to 28.x in late 2026). Multi-arch, pull-only,
// so pgcli never builds it. pgcli pins the ROOTFUL build; a -rootless variant
// exists but the two data layouts are mutually incompatible, so the flavor
// must never flip under a surviving data dir.
//
// Three load-bearing divergences from the Grafana template:
//
//   - Ownership: the rootful image starts as root and su-execs the `git`
//     user, default uid/gid 1000 (overridable via USER_UID/USER_GID, which
//     pgcli passes explicitly so the env and the host-side pre-chown can
//     never drift apart). createContainer pre-chowns the data dir to
//     1000:1000 like Grafana pre-chowns to 472:0.
//   - No crypto secrets in env. SECRET_KEY / INTERNAL_TOKEN are deliberately
//     NEVER injected: environment-to-ini re-applies GITEA__* into the
//     PERSISTED /data/gitea/conf/app.ini on every boot, so a per-install
//     regenerated secret would silently re-key a surviving database whose 2FA
//     seeds and OAuth client secrets were encrypted under the old one —
//     unrecoverable, with no error line anywhere. Gitea generates both itself
//     on first boot into that file; the volume owns crypto state, pgcli owns
//     only the admin web login.
//   - The admin account is not an env var. INSTALL_LOCK=true skips the web
//     setup wizard (an empty instance boots straight to the login page —
//     Gitea has no "first user is admin" assertion), and CreateAdmin below
//     execs `gitea admin user create` inside the running container once the
//     instance is ready. That also means pgcli's Password field means
//     "the password to create the admin with", honoured only while that
//     account does not exist; an instance whose admin already exists keeps
//     its own login (rotate it with `gitea admin user change-password`
//     in-container, same spirit as grafana-cli reset-admin-password).
type GiteaManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

// giteaContainerDataDir is the image's single state volume: repos,
// /data/gitea/conf/app.ini, the default SQLite database at
// /data/gitea/gitea.db, ssh host keys, and logs all hang off it; pgcli
// bind-mounts the host data dir here.
const (
	giteaContainerDataDir = "/data"
	// giteaContainerUID/GID are the rootful image's baked git ids
	// (Dockerfile USER_UID/USER_GID defaults, re-passed as env so the
	// container and the host-side pre-chown always agree).
	giteaContainerUID = 1000
	giteaContainerGID = 1000
)

// NewGiteaManager creates a GiteaManager. It works on both platforms: Linux
// serves over host networking, macOS over the pgcli-net bridge with the web
// port published (and the SSH port when enabled), exactly like Grafana. The
// upstream image is multi-arch.
func NewGiteaManager(cfg *config.Config) (*GiteaManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &GiteaManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/gitea/<name>/data. It holds repos,
// app.ini, the default SQLite DB and ssh host keys, and outlives the
// container — `pg addon remove` keeps it unless --clean-data, so reinstalling
// under the same name revives everything.
func (m *GiteaManager) resolveDataDir(gc *config.GiteaConfig) string {
	if gc.DataDir != "" {
		return gc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "gitea", gc.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *GiteaManager) DataDir(gc *config.GiteaConfig) string {
	return m.resolveDataDir(gc)
}

// GiteaStoreExists reports whether a data dir already holds Gitea's default
// SQLite database — the cheap host-side signal that install is reviving a
// kept store rather than initialising a fresh one. It is SQLite-only by
// construction: with --dsn the metadata lives in the external Postgres, so
// the revive check falls back to asking the instance itself (AdminExists).
func GiteaStoreExists(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dataDir, "gitea", "gitea.db"))
	return err == nil
}

// EnsureImage pulls the Gitea image if it is not present locally (pull-only:
// public docker.io/gitea tags; pgcli never builds them). Offline hosts
// pre-seed it with `podman load` like the core images.
func (m *GiteaManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling Gitea image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the Gitea container (install semantics:
// stop/rm/recreate so updated ports/credentials take effect). The data dir is
// created if missing; it is never deleted here.
func (m *GiteaManager) EnsureContainer(gc *config.GiteaConfig) error {
	containerName := gc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> Gitea container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping Gitea container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing Gitea container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale Gitea container: %w", err)
			}
		}
	}

	if err := m.createContainer(gc); err != nil {
		return err
	}
	fmt.Println("  [OK] Gitea container started")
	return nil
}

// StartContainer starts the Gitea container without touching config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh.
func (m *GiteaManager) StartContainer(gc *config.GiteaConfig) error {
	containerName := gc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] Gitea %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] Gitea %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] Gitea %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper Gitea container: %w", err)
		}
	}

	if err := m.createContainer(gc); err != nil {
		return err
	}
	fmt.Printf("  [OK] Gitea %s started\n", containerName)
	return nil
}

// GiteaDisplayHost picks the host name Gitea renders into clone URLs and
// webhook payloads. A wildcard bind (0.0.0.0/::, the default) is not a
// connectable address — echoing it into ROOT_URL would print literally
// `http://0.0.0.0:3080/...` as a clone URL — so the wildcard collapses to
// `localhost` (correct for the overwhelmingly common local use), and anyone
// deploying for remote clones passes `--listen <ip-or-host>`, which becomes
// both the bind hint and the displayed host.
func GiteaDisplayHost(listen string) string {
	if listen == "" || listen == "0.0.0.0" || listen == "::" {
		return "localhost"
	}
	return listen
}

// giteaArgsAndEnv builds the full `podman run` argv for a Gitea instance.
// Pure function (no podman calls) so the env mapping and the networking shape
// are unit-testable:
//   - NO --user flag: the image's own entrypoint drops to the baked git uid
//     via su-exec; forcing uid 0 would run the daemon as root and write
//     root-owned files into the data dir (the pgAdmin trick is as wrong here
//     as it is for Grafana). USER_UID/USER_GID pin the uid the data dir is
//     pre-chowned to, so the two can never drift.
//   - INSTALL_LOCK=true replaces the web setup wizard; SECRET_KEY and
//     INTERNAL_TOKEN are deliberately absent — Gitea generates them into the
//     persisted app.ini on first boot (see the GiteaManager doc for the
//     re-keying hazard of injecting them).
//   - GITEA__server__HTTP_PORT pins the web listener to the assigned host port
//     (the image defaults to 3000, which two host-network instances would
//     fight over); HTTP_ADDR goes through proxyBindHost so the published port
//     is reachable under the macOS bridge; ROOT_URL makes displayed clone URLs
//     match reality instead of the wildcard bind.
//   - SSH. Every instance carries a pool-allocated SSHPort that goes into the
//     *bare* env SSH_LISTEN_PORT/SSH_PORT, because that — not the GITEA__server__
//     copy — is what the image's *always-running* s6 sshd binds (its run script
//     execs sshd unconditionally; the GITEA__server__* keys drive only Gitea's own
//     SSH logic and clone URLs, not that process). Parking it off 22 is what keeps
//     a host-network instance from racing the host sshd forever.
//   - gc.SSH=false: DISABLE_SSH=true so Gitea neither writes authorized_keys
//     nor advertises ssh:// clone URLs. The s6 sshd is still up — parked on
//     the pool port, idle, and impossible to turn off through env.
//   - gc.SSH=true: git-over-ssh rides that same s6 sshd (the image's native
//     route): DISABLE_SSH=false lets Gitea populate the git user's
//     authorized_keys, START_SSH_LISTENER=false keeps Gitea's separate built-in
//     Go listener from double-binding the one netns, and
//     SSH_PORT/SSH_LISTEN_PORT/SSH_DOMAIN render clone URLs as ssh://host:port.
//   - With Dsn set, the metadata database moves to the external Postgres
//     (GITEA__database__*); the database itself must already exist (same
//     contract as PostgREST --dsn: pgcli does not create it, Gitea creates
//     its schema on first boot).
//   - Self-registration is closed — the generated admin password becomes the
//     only gate on a 0.0.0.0 default listen (Grafana's GF_USERS_ALLOW_SIGN_UP
//     analogue).
//
// dataDir is the caller's already-resolved host data dir (the manager resolves
// it from base+name+override), so this stays a pure function of its arguments.
func giteaArgsAndEnv(gc *config.GiteaConfig, bridge bool, network, dataDir string) []string {
	args := []string{"run", "-d", "--name", gc.ContainerName}
	args = append(args, netFlags(bridge, network, gc.HostPort)...)
	env := []string{
		"USER_UID=" + strconv.Itoa(giteaContainerUID),
		"USER_GID=" + strconv.Itoa(giteaContainerGID),
		"GITEA__security__INSTALL_LOCK=true",
		"GITEA__server__HTTP_PORT=" + strconv.Itoa(gc.HostPort),
		"GITEA__server__HTTP_ADDR=" + proxyBindHost(bridge, gc.Listen),
		"GITEA__server__ROOT_URL=" + fmt.Sprintf("http://%s:%d/", GiteaDisplayHost(gc.Listen), gc.HostPort),
		"GITEA__service__DISABLE_REGISTRATION=true",
	}
	if gc.SSHPort != 0 {
		// The image's s6 sshd is always started and reads its bind port from
		// the BARE SSH_LISTEN_PORT/SSH_PORT env, not the GITEA__server__ copy.
		// Parking it on the pool port is the whole reason a host-network gitea
		// does not fight the host sshd for 22 — so this pair is emitted in both
		// SSH modes, regardless of gc.SSH.
		env = append(env,
			"SSH_LISTEN_PORT="+strconv.Itoa(gc.SSHPort),
			"SSH_PORT="+strconv.Itoa(gc.SSHPort),
		)
	}
	if bridge && gc.SSH && gc.SSHPort != 0 {
		// Only an *enabled* listener is worth publishing on the bridge net
		// (netFlags only knows the web port). An SSH-off instance's parked
		// sshd is unreachable-by-design, so no -p for it.
		args = append(args, "-p", fmt.Sprintf("%d:%d", gc.SSHPort, gc.SSHPort))
	}
	if gc.SSH {
		domain := GiteaDisplayHost(gc.Listen)
		env = append(env,
			// git-over-ssh uses the image's s6 sshd (native route), so DISABLE_SSH
			// must be false — that is the flag gating whether Gitea writes the
			// git user's authorized_keys that sshd consults. Gitea's own built-in
			// Go listener stays off: under host networking both would bind the
			// same port in the same netns.
			"GITEA__server__DISABLE_SSH=false",
			"GITEA__server__START_SSH_LISTENER=false",
			"GITEA__server__SSH_LISTEN_PORT="+strconv.Itoa(gc.SSHPort),
			"GITEA__server__SSH_PORT="+strconv.Itoa(gc.SSHPort),
			"GITEA__server__SSH_DOMAIN="+domain,
		)
	} else {
		env = append(env, "GITEA__server__DISABLE_SSH=true")
	}
	if gc.Dsn != "" {
		host, port, user, password, database, err := ParseDSN(gc.Dsn)
		if err != nil {
			// Defensive: the CLI validates before writing, so an unparseable
			// DSN cannot reach here through the install path. A wrong env
			// would just boot a broken instance; fail loudly instead.
			slog.Error("gitea: unparseable dsn in args builder, falling back to SQLite", "dsn", gc.Dsn, "err", err)
		} else {
			env = append(env,
				"GITEA__database__DB_TYPE=postgres",
				"GITEA__database__HOST="+net.JoinHostPort(host, strconv.Itoa(port)),
				"GITEA__database__NAME="+database,
				"GITEA__database__USER="+user,
				"GITEA__database__PASSWD="+password,
			)
		}
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args,
		"--http-proxy=false",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), giteaContainerDataDir),
		"--restart", "unless-stopped",
	)
	args = append(args, gc.ImageTag)
	return args
}

// createContainer runs a Gitea instance. The data dir is created on the host,
// then handed to the image's git uid 1000 (ensureDataDirOwner) — the
// entrypoint chowns only what it creates itself, and an unwritable bind mount
// fails the container at startup. Failure aborts the install with a
// diagnostic instead of leaving a crash-looping container.
func (m *GiteaManager) createContainer(gc *config.GiteaConfig) error {
	dataDir := m.resolveDataDir(gc)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating Gitea data dir %s: %w", dataDir, err)
	}
	if err := m.ensureDataDirOwner(dataDir); err != nil {
		return err
	}

	args := giteaArgsAndEnv(gc, m.bridge, m.cfg.Podman.Network, dataDir)
	slog.Debug("podman gitea run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating Gitea container: %w", err)
	}
	return nil
}

// ensureDataDirOwner chowns the host data dir to the image's git uid/gid
// 1000:1000.
//
// Rootless podman: the bind mount must carry the SUBORDINATE host uid that
// podman maps container-uid 1000 to, so the chown goes through
// `podman unshare chown` — a plain host `os.Chown(1000, 1000)` would set
// literal host uid 1000, which the container does NOT map to its own 1000.
//
// Rootful podman — only reachable by running pgcli itself as root, since the
// manager shells out to the plain `podman` CLI, which under a non-root user is
// always that user's own rootless namespace. Under root, podman maps container
// uid 1000 to host uid 1000 directly and `podman unshare` refuses to run at
// all, so retry with a direct recursive chown. Without this fallback a rootful
// host would hit the failure on EVERY install. (Grafana's 472:0 has the exact
// same contract — only the ids differ.)
func (m *GiteaManager) ensureDataDirOwner(dataDir string) error {
	err := podmanUnshareChown(m.podman, giteaContainerUID, giteaContainerGID, dataDir)
	if err == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		if chownErr := chownTree(giteaContainerUID, giteaContainerGID, dataDir); chownErr == nil {
			return nil
		}
	}
	return fmt.Errorf("chowning Gitea data dir %s to %d:%d (Gitea's container git user): %w — pgcli chowns this automatically under rootless podman, and directly when it runs as root on a rootful host; a manual `sudo chown -R %d:%d %s` alone does not clear this error, since pgcli re-checks ownership on every install attempt — run pgcli as root, or switch to rootless podman",
		dataDir, giteaContainerUID, giteaContainerGID, err, giteaContainerUID, giteaContainerGID, dataDir)
}

// execGitea runs `/usr/local/bin/gitea <args...>` inside the container with a
// bounded capture (execIdleKilled: an idle kill, so a crash-looping or wedged
// container cannot hang the installer). Returns combined output.
//
// The `-u <uid>` is required, not cosmetic: the gitea image's baked USER is
// root (only its entrypoint su-execs the daemon down to git), so a bare
// `podman exec` runs as root — and the gitea CLI then aborts immediately with
// mustNotRunAsRoot. Pinning the exec to the same uid the daemon runs as (the
// git uid we pre-chown and pin via USER_UID) is what makes the CLI usable.
func (m *GiteaManager) execGitea(gc *config.GiteaConfig, d time.Duration, args ...string) (string, error) {
	execArgs := []string{"exec", "-u", strconv.Itoa(giteaContainerUID), gc.ContainerName, "/usr/local/bin/gitea"}
	execArgs = append(execArgs, args...)
	return execIdleKilled(m.podman, d, defaultIdleKill, execArgs...)
}

// AdminExists reports whether the admin web account is already present in the
// instance's database (`gitea admin user list`). Used as the install-time
// idempotency gate — in SQLite mode it is equivalent to GiteaStoreExists plus
// a successful bootstrap, but it is also the only correct check with --dsn,
// where the metadata lives outside the bind mount.
func (m *GiteaManager) AdminExists(gc *config.GiteaConfig) (bool, error) {
	out, err := m.execGitea(gc, 20*time.Second, "admin", "user", "list")
	if err != nil {
		return false, err
	}
	// Output is a table; usernames start after the leading ID column. Match
	// the field rather than a bare substring so `gitea` does not collide with
	// e.g. `gitea-backup-bot`.
	for line := range strings.SplitSeq(out, "\n") {
		if slices.Contains(strings.Fields(line), gc.AdminUser) {
			return true, nil
		}
	}
	return false, nil
}

// WaitForReady polls `gitea admin user list` until the instance answers —
// first boot runs migrations (creating app.ini keys, the SQLite DB or the
// Postgres schema) that can take a few seconds. The poll doubles as the
// ready-gate for CreateAdmin and the error it returns is Gitea's own.
func (m *GiteaManager) WaitForReady(gc *config.GiteaConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if _, err := m.execGitea(gc, 15*time.Second, "admin", "user", "list"); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for Gitea %s to initialize: %w", gc.ContainerName, lastErr)
		}
		time.Sleep(2 * time.Second)
	}
}

// CreateAdmin creates the instance's admin web account via `gitea admin
// user create` inside the running container. It is idempotent by contract:
// the caller gates on AdminExists (the create path is a hard error if the
// user exists — Gitea itself refuses the duplicate), and the email is
// deterministic so a recreate never adds a second address.
// --must-change-password=false matters: without it Gitea marks the account
// forced-change and the password pgcli printed becomes single-use.
func (m *GiteaManager) CreateAdmin(gc *config.GiteaConfig) error {
	_, err := m.execGitea(gc, 30*time.Second,
		"admin", "user", "create",
		"--admin",
		"--username", gc.AdminUser,
		"--password", gc.Password,
		"--email", gc.AdminUser+"@localhost",
		"--must-change-password=false",
	)
	if err != nil {
		return fmt.Errorf("creating Gitea admin user %q: %w", gc.AdminUser, err)
	}
	return nil
}

// Remove stops and removes the Gitea container. The data dir is kept by
// default (it holds repos, app.ini, the crypto secrets Gitea generated and
// the SQLite DB — remove+reinstall revives the instance and its login);
// cleanData also deletes it, refusing when it is still a mount point.
func (m *GiteaManager) Remove(gc *config.GiteaConfig, cleanData bool) error {
	containerName := gc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing Gitea container: %w", err)
	}
	fmt.Println("  [OK] Gitea container removed")

	dataDir := m.resolveDataDir(gc)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if isMountpoint(dataDir) {
		fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
		return nil
	}
	// Gitea writes as the container's mapped 1000 uid, which os.RemoveAll
	// cannot delete from the host under rootless — removeHostDir falls back to
	// `podman unshare rm` (the same path Grafana/pgAdmin/Redis need for their
	// mapped-uid files). With --dsn only this volume's repos disappear; the
	// external database is not pgcli's to touch.
	if err := removeHostDir(m.podman, gc.ImageTag, dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if gc.DataDir == "" {
		// Default layout: also prune the <name> dir when it became empty. With
		// an override the parent belongs to the user — never touch it.
		_, _ = removeHostDirIfEmpty(m.podman, gc.ImageTag, filepath.Dir(dataDir))
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *GiteaManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *GiteaManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a Gitea container.
func (m *GiteaManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *GiteaManager) run(args ...string) (string, error) {
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

func (m *GiteaManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *GiteaManager) imageExists(tag string) (bool, error) {
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

func (m *GiteaManager) containerExists(name string) (bool, error) {
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

func (m *GiteaManager) containerRunning(name string) (bool, error) {
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
