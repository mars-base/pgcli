package podman

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// GrafanaManager manages standalone Grafana containers — the open-source
// observability dashboard (https://grafana.com/oss/grafana/). Like pgAdmin it
// is a top-level, persistent web-UI addon: one host port, configuration
// entirely through environment variables (GF_* — the grafana.ini inside the
// image cannot be edited), an auto-generated login, and a host data dir that
// outlives the container so remove+reinstall revives dashboards and settings.
// v1 seeds nothing (no datasource/dashboard provisioning mounts); the UI is
// configured through the browser.
//
// The image is docker.io/grafana/grafana (NOT grafana/grafana-oss: since
// 12.4.0 the -oss repo is frozen and grafana/grafana carries the OSS build —
// config.DefaultGrafanaImageTag). Multi-arch, pull-only, so pgcli never builds
// it.
//
// Ownership is where Grafana diverges sharply from pgAdmin. The image bakes
// `USER 472` (group root, gid 0) and its /run.sh performs NO ownership work:
// it only checks readability/writability (printing a warning), mkdirs the
// plugins dir, installs GF_PLUGINS_PREINSTALL, and execs `grafana server` as
// the same 472 it started as — no chown, no su-exec/gosu drop. So pgAdmin's
// `--user 0` trick (trigger the entrypoint's root branch to self-chown) has
// no analogue here: forcing uid 0 would just run Grafana as root and write
// root-owned files into the data dir. rustfs faced the same fixed-uid problem
// and solved it with a pgcli wrapper image that chowns in-container; Grafana
// ships none, so the ownership work happens on the host instead:
// createContainer pre-chowns the bind-mounted data dir to 472:0 through
// podmanUnshareChown, which maps 472 to the rootless user namespace's
// subordinate range exactly the way the container will see it.
//
// The corollary for teardown: persisted files land owned by the mapped uid
// (host subuid 100472), which the host user cannot os.RemoveAll — so Remove
// deletes the data dir through removeHostDir, whose `podman unshare rm`
// fallback reclaims them (the same path pgAdmin/Redis need).
type GrafanaManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

// grafanaContainerDataDir is where the image keeps its state (GF_PATHS_DATA) —
// the sqlite meta DB (grafana.db), plugins, and by default logs/plugins paths
// hang off it or beside it; pgcli bind-mounts the host data dir here.
const (
	grafanaContainerDataDir = "/var/lib/grafana"
	// grafanaContainerUID/GID are the image's baked ids (Dockerfile ARG
	// GF_UID="472", GF_GID="0") — the pair podmanUnshareChown targets.
	grafanaContainerUID = 472
	grafanaContainerGID = 0
)

// NewGrafanaManager creates a GrafanaManager. It works on both platforms:
// Linux serves over host networking, macOS over the pgcli-net bridge with the
// port published (the proxyBindHost widening makes it reachable through the
// VM), exactly like pgAdmin. The upstream image is multi-arch.
func NewGrafanaManager(cfg *config.Config) (*GrafanaManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &GrafanaManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/grafana/<name>/data. It holds the
// grafana.db meta DB and outlives the container — `pg addon remove` keeps it
// unless --clean-data, so reinstalling under the same name revives saved
// dashboards and logins.
func (m *GrafanaManager) resolveDataDir(gc *config.GrafanaConfig) string {
	if gc.DataDir != "" {
		return gc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "grafana", gc.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *GrafanaManager) DataDir(gc *config.GrafanaConfig) string {
	return m.resolveDataDir(gc)
}

// GrafanaStoreExists reports whether a data dir already holds Grafana's sqlite
// meta DB (grafana.db) — the signal that install is reviving a kept store
// rather than initialising a fresh one. Grafana only reads
// GF_SECURITY_ADMIN_USER/PASSWORD when that file is absent, so the login
// credentials an install prints are inert on a revived store (the working
// login is whatever was set on the store's FIRST startup;
// `grafana-cli admin reset-admin-password` inside the container changes it).
func GrafanaStoreExists(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dataDir, "grafana.db"))
	return err == nil
}

// EnsureImage pulls the Grafana image if it is not present locally (pull-only:
// public docker.io/grafana tags; pgcli never builds them). Offline hosts
// pre-seed them with `podman load` like the core images.
func (m *GrafanaManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling Grafana image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the Grafana container (install
// semantics: stop/rm/recreate so updated ports/credentials take effect). The
// data dir is created if missing; it is never deleted here.
func (m *GrafanaManager) EnsureContainer(gc *config.GrafanaConfig) error {
	containerName := gc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> Grafana container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping Grafana container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing Grafana container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale Grafana container: %w", err)
			}
		}
	}

	if err := m.createContainer(gc); err != nil {
		return err
	}
	fmt.Println("  [OK] Grafana container started")
	return nil
}

// StartContainer starts the Grafana container without touching config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh.
func (m *GrafanaManager) StartContainer(gc *config.GrafanaConfig) error {
	containerName := gc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] Grafana %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] Grafana %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] Grafana %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper Grafana container: %w", err)
		}
	}

	if err := m.createContainer(gc); err != nil {
		return err
	}
	fmt.Printf("  [OK] Grafana %s started\n", containerName)
	return nil
}

// grafanaArgsAndEnv builds the full `podman run` argv for a Grafana instance.
// Pure function (no podman calls) so the env mapping and the networking shape
// are unit-testable:
//   - NO --user flag: the image's baked USER 472 is what must run — see the
//     GrafanaManager doc for why pgAdmin's `--user 0` is actively wrong here
//     and why the data dir instead arrives pre-chowned by createContainer.
//   - GF_SECURITY_ADMIN_USER / GF_SECURITY_ADMIN_PASSWORD are Grafana's WEB
//     LOGIN credentials; they only take effect while grafana.db is absent
//     (GrafanaStoreExists). GF_SERVER_HTTP_PORT pins the web listener to the
//     assigned host port (the image otherwise defaults to 3000, which two
//     instances would fight over on host networking). The bind address goes
//     through proxyBindHost so the published port is reachable under the
//     macOS bridge.
//   - Analytics reporting/update-checks are off (a database appliance should
//     not phone home), and self-signup is closed — the generated admin
//     password becomes the only gate on a 0.0.0.0 default listen.
//   - No GF_SERVER_ROOT_URL: v1 serves at /; a reverse-proxy subpath is nginx
//     territory and would need an override knob this addon does not ship yet.
//
// dataDir is the caller's already-resolved host data dir (the manager resolves
// it from base+name+override), so this stays a pure function of its arguments.
func grafanaArgsAndEnv(gc *config.GrafanaConfig, bridge bool, network, dataDir string) []string {
	args := []string{"run", "-d", "--name", gc.ContainerName}
	args = append(args, netFlags(bridge, network, gc.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-e", "GF_SECURITY_ADMIN_USER="+gc.AdminUser,
		"-e", "GF_SECURITY_ADMIN_PASSWORD="+gc.Password,
		"-e", "GF_SERVER_HTTP_PORT="+strconv.Itoa(gc.HostPort),
		"-e", "GF_SERVER_HTTP_ADDR="+proxyBindHost(bridge, gc.Listen),
		"-e", "GF_ANALYTICS_REPORTING_ENABLED=false",
		"-e", "GF_ANALYTICS_CHECK_FOR_UPDATES=false",
		"-e", "GF_USERS_ALLOW_SIGN_UP=false",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), grafanaContainerDataDir),
	)
	args = append(args, gc.ImageTag)
	return args
}

// createContainer runs a Grafana instance. The data dir is created on the host,
// then handed to the image's fixed 472:0 (ensureDataDirOwner) — the image will
// not do this itself (no root branch in /run.sh), and an unwritable bind mount
// fails the container at startup with Grafana's own permission warning
// followed by a crash. Failure aborts the install with a diagnostic instead of
// leaving a crash-looping container.
func (m *GrafanaManager) createContainer(gc *config.GrafanaConfig) error {
	dataDir := m.resolveDataDir(gc)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating Grafana data dir %s: %w", dataDir, err)
	}
	if err := m.ensureDataDirOwner(dataDir); err != nil {
		return err
	}

	args := grafanaArgsAndEnv(gc, m.bridge, m.cfg.Podman.Network, dataDir)
	slog.Debug("podman grafana run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating Grafana container: %w", err)
	}
	return nil
}

// ensureDataDirOwner chowns the host data dir to the image's baked 472:0.
//
// Rootless podman: the bind mount must carry the SUBORDINATE host uid that
// podman maps container-uid 472 to, so the chown goes through
// `podman unshare chown` — a plain host `os.Chown(472, 0)` would set literal
// host uid 472, which the container does NOT map to its own 472.
//
// Rootful podman — only reachable by running pgcli itself as root, since the
// manager shells out to the plain `podman` CLI, which under a non-root user is
// always that user's own rootless namespace (there is no remote-URI podman
// config to point at someone else's rootful daemon). Under root, podman maps
// container uid 472 to host uid 472 directly and `podman unshare` refuses to
// run at all, so retry with a direct recursive chown. Without this fallback a
// rootful host would hit the failure on EVERY install.
func (m *GrafanaManager) ensureDataDirOwner(dataDir string) error {
	err := podmanUnshareChown(m.podman, grafanaContainerUID, grafanaContainerGID, dataDir)
	if err == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		if chownErr := chownTree(grafanaContainerUID, grafanaContainerGID, dataDir); chownErr == nil {
			return nil
		}
	}
	return fmt.Errorf("chowning Grafana data dir %s to %d:%d (Grafana's container user): %w — pgcli chowns this automatically under rootless podman, and directly when it runs as root on a rootful host; a manual `sudo chown -R %d:%d %s` alone does not clear this error, since pgcli re-checks ownership on every install attempt — run pgcli as root, or switch to rootless podman",
		dataDir, grafanaContainerUID, grafanaContainerGID, err, grafanaContainerUID, grafanaContainerGID, dataDir)
}

// chownTree recursively chowns root and everything under it (like
// `chown -R`), never following symlinks — WalkDir does not descend into them
// and Lchown acts on the link itself.
func chownTree(uid, gid int, root string) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}

// Remove stops and removes the Grafana container. The data dir is kept by
// default (it holds grafana.db — remove+reinstall revives dashboards and the
// original login); cleanData also deletes it, refusing when it is still a
// mount point.
func (m *GrafanaManager) Remove(gc *config.GrafanaConfig, cleanData bool) error {
	containerName := gc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing Grafana container: %w", err)
	}
	fmt.Println("  [OK] Grafana container removed")

	dataDir := m.resolveDataDir(gc)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if isMountpoint(dataDir) {
		fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", dataDir)
		return nil
	}
	// Grafana writes as the container's mapped 472 uid, which os.RemoveAll
	// cannot delete from the host — removeHostDir falls back to `podman
	// unshare rm` (the same path pgAdmin/Redis need for their mapped-uid files).
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
func (m *GrafanaManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *GrafanaManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a Grafana container.
func (m *GrafanaManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *GrafanaManager) run(args ...string) (string, error) {
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

func (m *GrafanaManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *GrafanaManager) imageExists(tag string) (bool, error) {
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

func (m *GrafanaManager) containerExists(name string) (bool, error) {
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

func (m *GrafanaManager) containerRunning(name string) (bool, error) {
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
