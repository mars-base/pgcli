package podman

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// AlertmanagerManager manages standalone Alertmanager containers — the alert
// dedup/grouping/routing frontend of the Prometheus stack
// (https://prometheus.io/docs/alerting/latest/alertmanager/). It is the
// Prometheus addon's twin: a top-level, persistent web-UI addon on one host
// port with a host data dir that outlives the container, configured by a
// rendered YAML file plus CLI flags after the image name, reloaded via SIGHUP.
//
// What it does NOT do (v1): wire itself into a Prometheus instance. No
// `alerting:` section is injected into any prometheus.yml — the two addons
// coexist without knowing about each other. Point Prometheus at it (or use a
// real notification tree via --conf-file) later.
//
// Ownership matches Prometheus and reuses its helpers: the image is USER nobody
// (uid/gid 65534) and its entrypoint does NO chown, so pgcli pre-chowns the
// host data dir to 65534:65534 via ensureDataDirOwner (`podman unshare chown`
// under rootless; the shared chownTree on a rootful host when pgcli runs as
// root). Remove reclaims the mapped-uid files through removeHostDir.
//
// Security posture is SHARPER than Prometheus's: Alertmanager ships no
// authentication, and its API lets anyone who can reach the port create
// SILENCES — muting the on-call. And unlike Prometheus (where pgcli never
// enables the HTTP reload endpoint via --web.enable-lifecycle), Alertmanager's
// POST /-/reload CANNOT be disabled. pgcli still reloads via SIGHUP rather than
// that endpoint — one uniform mechanism, no dependency on an unauthenticated
// mutating route — but the docs must not claim the route is closed, only that
// it is not needed.
type AlertmanagerManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

const (
	// alertmanagerContainerDataDir is where the image keeps silences and
	// notification state (--storage.path); pgcli bind-mounts the host data dir here.
	alertmanagerContainerDataDir = "/alertmanager"
	// alertmanagerContainerConfigFile is where alertmanager.yml is mounted
	// read-only; --config.file points at this in-container path (the image's
	// own default, so the flag is belt-and-braces explicitness).
	alertmanagerContainerConfigFile = "/etc/alertmanager/alertmanager.yml"
	// alertmanagerContainerUID/GID are the image's baked nobody ids (65534) that
	// ensureDataDirOwner chowns the host data dir to.
	alertmanagerContainerUID = 65534
	alertmanagerContainerGID = 65534
)

// NewAlertmanagerManager creates an AlertmanagerManager. It works on both
// platforms: Linux serves over host networking, macOS over the pgcli-net
// bridge with the port published, exactly like Prometheus. The upstream image
// is multi-arch.
func NewAlertmanagerManager(cfg *config.Config) (*AlertmanagerManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &AlertmanagerManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host state dir: the explicit DataDir
// override when set, otherwise <base>/addon/alertmanager/<name>/data. It holds
// silences and notification state and outlives the container — `pg addon
// remove` keeps it unless --clean-data.
func (m *AlertmanagerManager) resolveDataDir(ac *config.AlertmanagerConfig) string {
	if ac.DataDir != "" {
		return ac.DataDir
	}
	return filepath.Join(alertmanagerConfigDir(m.dataDir, ac.Name), "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *AlertmanagerManager) DataDir(ac *config.AlertmanagerConfig) string {
	return m.resolveDataDir(ac)
}

// alertmanagerConfigDir is the host dir holding an instance's alertmanager.yml
// (and, by default, its data/ subdir).
func alertmanagerConfigDir(baseDir, name string) string {
	return filepath.Join(baseDir, "addon", "alertmanager", name)
}

// ConfigPath returns the path of the alertmanager.yml copy pgcli mounts for
// instance ac (rendered or copied from the operator's file — same path either
// way). Callers snapshot it before WriteConfig overwrites it.
func (m *AlertmanagerManager) ConfigPath(ac *config.AlertmanagerConfig) string {
	return filepath.Join(alertmanagerConfigDir(m.dataDir, ac.Name), "alertmanager.yml")
}

// ValidateWebhooks checks every --webhook-url parses as an absolute http(s)
// URL BEFORE anything is written — the "early detection" contract: a bad URL
// fails the install with no partial state (no config rewrite, no reload, no
// pg.yaml change). The CLI install path calls it before saving config, and
// renderAlertmanagerConfig calls it again as the renderer's own guard. Unlike
// Prometheus scrape targets these are NOT connectivity-probed: a webhook is
// POSTed at alert time, so reachability at install time says nothing (and an
// unreachable one must not block an install).
func ValidateWebhooks(webhooks []string) error {
	for _, w := range webhooks {
		u, err := url.Parse(w)
		if err != nil {
			return fmt.Errorf("webhook URL %q: %w", w, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("webhook URL %q: want an http(s) URL", w)
		}
		if u.Host == "" {
			return fmt.Errorf("webhook URL %q: missing host", w)
		}
	}
	return nil
}

// renderAlertmanagerConfig builds the alertmanager.yml text for a
// rendered-mode instance. Pure function of the config so it can be
// unit-tested without podman. It emits one minimal route (group by alertname,
// sensible default timings) pointing at a single receiver, which is either
// "web.hook" — carrying one webhook_configs entry per --webhook-url, so several
// URLs fan out as notifications from the SAME receiver — or "no-op" (a
// name-only receiver, which Alertmanager's own config validation accepts), so
// a plain install starts a fully functional UI/API silencer with nothing ever
// sent. Retention and the listener bind are NOT rendered here — they are CLI
// flags, see alertmanagerArgsAndEnv.
func renderAlertmanagerConfig(ac *config.AlertmanagerConfig) (string, error) {
	if err := ValidateWebhooks(ac.Webhook); err != nil {
		return "", err
	}
	receiver := "no-op"
	if len(ac.Webhook) > 0 {
		receiver = "web.hook"
	}

	var b strings.Builder
	b.WriteString("# Generated by pgcli for the alertmanager addon — do not edit in place.\n")
	b.WriteString("# Change the pg.yaml alertmanager entry and re-run\n")
	b.WriteString("# `pg addon install alertmanager` instead; this file is rewritten.\n")
	b.WriteString("\n")
	b.WriteString("global:\n")
	b.WriteString("  resolve_timeout: 5m\n")
	b.WriteString("\n")
	b.WriteString("route:\n")
	fmt.Fprintf(&b, "  receiver: %q\n", receiver)
	b.WriteString("  group_by: ['alertname']\n")
	b.WriteString("  group_wait: 30s\n")
	b.WriteString("  group_interval: 5m\n")
	b.WriteString("  repeat_interval: 4h\n")
	b.WriteString("\n")
	b.WriteString("receivers:\n")
	fmt.Fprintf(&b, "  - name: %q\n", receiver)
	if len(ac.Webhook) > 0 {
		b.WriteString("    webhook_configs:\n")
	}
	for _, w := range ac.Webhook {
		fmt.Fprintf(&b, "      - url: %q\n", w)
	}
	return b.String(), nil
}

// WriteConfig renders alertmanager.yml (or copies the operator's --conf-file)
// into <base>/addon/alertmanager/<name>/. In conf-file mode the user's file is
// copied verbatim into the managed path — pgcli never rewrites it, and it is
// the COPY (not the original) that gets mounted, because a single-file bind
// mount pins the source inode so an in-place edit of the original would not
// propagate. The rendered file carries no secrets (webhook URLs may carry
// tokens in their query string, but that is the operator's choice, same
// contract as Prometheus's config), so 0644 — matching nginx/haproxy and
// keeping it readable by the container's nobody uid under rootless podman's
// mapping.
func (m *AlertmanagerManager) WriteConfig(ac *config.AlertmanagerConfig) (string, error) {
	dir := alertmanagerConfigDir(m.dataDir, ac.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating alertmanager config dir: %w", err)
	}
	path := filepath.Join(dir, "alertmanager.yml")

	if ac.ConfFile != "" {
		data, err := os.ReadFile(ac.ConfFile)
		if err != nil {
			return "", fmt.Errorf("reading user conf file %s: %w", ac.ConfFile, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("user conf file %s is empty", ac.ConfFile)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return "", fmt.Errorf("writing alertmanager.yml: %w", err)
		}
		return path, nil
	}

	cfg, err := renderAlertmanagerConfig(ac)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(cfg), 0644); err != nil {
		return "", fmt.Errorf("writing alertmanager.yml: %w", err)
	}
	return path, nil
}

// EnsureImage pulls the Alertmanager image if it is not present locally
// (pull-only: public docker.io/prom tags; pgcli never builds them). Offline
// hosts pre-seed them with `podman load` like the core images.
func (m *AlertmanagerManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling Alertmanager image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the Alertmanager container (install
// semantics: stop/rm/recreate so a re-rendered config or changed port takes
// effect). The data and config dirs are created; neither is deleted here.
func (m *AlertmanagerManager) EnsureContainer(ac *config.AlertmanagerConfig) error {
	containerName := ac.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> Alertmanager container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping Alertmanager container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing Alertmanager container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale Alertmanager container: %w", err)
			}
		}
	}

	if err := m.createContainer(ac); err != nil {
		return err
	}
	fmt.Println("  [OK] Alertmanager container started")
	return nil
}

// StartContainer starts the Alertmanager container without regenerating config
// (autostart-on-boot path): running → no-op; exists but stopped → podman start,
// recreating on improper state; missing → create. Like nginx/haproxy it refuses
// to create from scratch when the config file is absent (install writes it).
func (m *AlertmanagerManager) StartContainer(ac *config.AlertmanagerConfig) error {
	containerName := ac.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] Alertmanager %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] Alertmanager %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] Alertmanager %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper Alertmanager container: %w", err)
		}
	}

	if _, err := os.Stat(m.ConfigPath(ac)); err != nil {
		return fmt.Errorf("alertmanager config not found -- run 'pg addon install alertmanager' first")
	}

	if err := m.createContainer(ac); err != nil {
		return err
	}
	fmt.Printf("  [OK] Alertmanager %s started\n", containerName)
	return nil
}

// alertmanagerArgsAndEnv builds the full `podman run` argv for an Alertmanager
// instance. Pure function (no podman calls) so the mount + trailing-flag shape
// is unit-testable:
//   - NO --user flag: the image's baked nobody (65534) is what must run — the
//     data dir arrives pre-chowned by createContainer (see AlertmanagerManager).
//   - Two mounts: the writable state dir at /alertmanager (:z) and the
//     rendered/copied config file read-only at
//     /etc/alertmanager/alertmanager.yml (:ro,z).
//   - Everything after ImageTag is the `alertmanager` binary's OWN flags.
//     `--config.file` names the in-container mount path; `--web.listen-address`
//     pins the listener to the assigned host port (bind widened through
//     proxyBindHost for the macOS bridge); `--storage.path` points at the data
//     mount; and `--cluster.listen-address=` with an EMPTY VALUE explicitly
//     disables the HA gossip cluster (TCP+UDP 9094) — a single instance has no
//     peers to talk to, and leaving it on would both squat a port and emit
//     continuous bootstrap-peer failure logs.
//
// dataDir and configPath are the caller's already-resolved host paths, so this
// stays a pure function of its arguments.
func alertmanagerArgsAndEnv(ac *config.AlertmanagerConfig, bridge bool, network, dataDir, configPath string) []string {
	args := []string{"run", "-d", "--name", ac.ContainerName}
	args = append(args, netFlags(bridge, network, ac.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(dataDir), alertmanagerContainerDataDir),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(configPath), alertmanagerContainerConfigFile),
	)
	args = append(args, ac.ImageTag)
	args = append(args,
		"--config.file="+alertmanagerContainerConfigFile,
		fmt.Sprintf("--web.listen-address=%s:%d", proxyBindHost(bridge, ac.Listen), ac.HostPort),
		"--storage.path="+alertmanagerContainerDataDir,
		"--cluster.listen-address=",
	)
	return args
}

// createContainer runs an Alertmanager instance. It creates the data dir,
// writes (renders or copies) the config, chowns the data dir to the image's
// 65534, then starts the container. The config file is host-owned and mounted
// read-only, so it needs no chown; only the writable state dir does.
func (m *AlertmanagerManager) createContainer(ac *config.AlertmanagerConfig) error {
	dataDir := m.resolveDataDir(ac)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating Alertmanager data dir %s: %w", dataDir, err)
	}
	configPath, err := m.WriteConfig(ac)
	if err != nil {
		return err
	}
	if err := m.ensureDataDirOwner(dataDir); err != nil {
		return err
	}

	args := alertmanagerArgsAndEnv(ac, m.bridge, m.cfg.Podman.Network, dataDir, configPath)
	slog.Debug("podman alertmanager run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating Alertmanager container: %w", err)
	}
	return nil
}

// ensureDataDirOwner chowns the host data dir to the image's baked 65534:65534.
// Rootless podman maps container uid 65534 to a subordinate host uid, so the
// chown goes through `podman unshare chown`; a plain host os.Chown would set the
// wrong id. Rootful podman (only reachable by running pgcli as root, since the
// manager shells out to the plain `podman` CLI) maps 65534 directly and refuses
// `podman unshare`, so retry with the shared chownTree. Same contract as
// PrometheusManager.ensureDataDirOwner — only the names differ.
func (m *AlertmanagerManager) ensureDataDirOwner(dataDir string) error {
	err := podmanUnshareChown(m.podman, alertmanagerContainerUID, alertmanagerContainerGID, dataDir)
	if err == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		if chownErr := chownTree(alertmanagerContainerUID, alertmanagerContainerGID, dataDir); chownErr == nil {
			return nil
		}
	}
	return fmt.Errorf("chowning Alertmanager data dir %s to %d:%d (Alertmanager's container user): %w — pgcli chowns this automatically under rootless podman, and directly when it runs as root on a rootful host; a manual `sudo chown -R %d:%d %s` alone does not clear this error, since pgcli re-checks ownership on every install attempt — run pgcli as root, or switch to rootless podman",
		dataDir, alertmanagerContainerUID, alertmanagerContainerGID, err, alertmanagerContainerUID, alertmanagerContainerGID, dataDir)
}

// Reload applies config changes to a running instance without recreating it.
// Alertmanager reloads alertmanager.yml on SIGHUP, so pgcli rewrites the
// mounted copy (re-render in rendered mode, re-copy the operator's file in
// conf-file mode) and signals the container. It uses podman signalling rather
// than the HTTP POST /-/reload endpoint for uniformity with the other addons —
// note that, unlike Prometheus, Alertmanager's reload endpoint CANNOT be
// disabled, so the docs promise "pgcli does not depend on it", never "it is
// closed".
func (m *AlertmanagerManager) Reload(ac *config.AlertmanagerConfig) error {
	running, err := m.containerRunning(ac.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("alertmanager container %s is not running — run 'pg addon start alertmanager --name %s' first", ac.ContainerName, ac.Name)
	}

	if _, err := m.WriteConfig(ac); err != nil {
		return err
	}
	return m.SignalReload(ac)
}

// SignalReload sends SIGHUP to a running instance, which makes Alertmanager
// reread the alertmanager.yml already on disk. Assumes the file is in place;
// Reload and the install path both write it first. Refuses if the container
// isn't running.
func (m *AlertmanagerManager) SignalReload(ac *config.AlertmanagerConfig) error {
	running, err := m.containerRunning(ac.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("alertmanager container %s is not running — cannot hot-reload (a fresh instance starts normally)", ac.ContainerName)
	}
	fmt.Println("-> Reloading Alertmanager (SIGHUP)...")
	if _, err := m.run("kill", "--signal", "HUP", ac.ContainerName); err != nil {
		return fmt.Errorf("reloading Alertmanager: %w", err)
	}
	fmt.Println("  [OK] Alertmanager reloaded — reread alertmanager.yml")
	return nil
}

// Remove stops and removes the Alertmanager container and deletes the generated
// config file. The state data dir is kept by default (silences and notification
// state survive a reinstall) unless cleanData; --conf-file's original lives
// wherever the operator put it and is never touched — only pgcli's copy goes
// away. Under rootless podman the mapped-uid data dir is reclaimed through
// removeHostDir's `podman unshare rm` fallback.
func (m *AlertmanagerManager) Remove(ac *config.AlertmanagerConfig, cleanData bool) error {
	containerName := ac.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing Alertmanager container: %w", err)
	}
	fmt.Println("  [OK] Alertmanager container removed")

	// pgcli's alertmanager.yml copy is a generated artifact — safe to delete.
	if cfgPath := m.ConfigPath(ac); cfgPath != "" {
		if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] Warning: removing config file %s: %v\n", cfgPath, err)
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
	if err := removeHostDir(m.podman, ac.ImageTag, dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if ac.DataDir == "" {
		// Default layout: also prune the <name> dir once data/ is gone. With an
		// override the parent belongs to the user — never touch it.
		_, _ = removeHostDirIfEmpty(m.podman, ac.ImageTag, alertmanagerConfigDir(m.dataDir, ac.Name))
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *AlertmanagerManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not).
func (m *AlertmanagerManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops an Alertmanager container.
func (m *AlertmanagerManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *AlertmanagerManager) run(args ...string) (string, error) {
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

func (m *AlertmanagerManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *AlertmanagerManager) imageExists(tag string) (bool, error) {
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

func (m *AlertmanagerManager) containerExists(name string) (bool, error) {
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

func (m *AlertmanagerManager) containerRunning(name string) (bool, error) {
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
