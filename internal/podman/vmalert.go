package podman

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/vmalert"
)

const (
	vmalertContainerRulesDir     = "/etc/vmalert/rules"
	vmalertContainerTemplatesDir = "/etc/vmalert/templates"
)

// VmalertManager manages standalone vmalert containers — the rule-evaluation
// engine of the VictoriaMetrics family. One instance evaluates rules against
// exactly one datasource (VictoriaLogs or a metrics backend, never both) and
// forwards firing alerts to one or more Alertmanager-compatible notifiers.
//
// Unlike the database addons there is no writable data dir: rules and
// templates live in host directories bind-mounted read-only, so the operator
// edits YAML/templates directly and asks vmalert to reload. In-container state
// (alert state) is not persisted; use -remoteWrite.url/-remoteRead.url when
// alert-state restore after restart matters.
type VmalertManager struct {
	cfg     *config.Config
	podman  string
	dataDir string
	bridge  bool
}

// NewVmalertManager creates a VmalertManager.
func NewVmalertManager(cfg *config.Config) (*VmalertManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &VmalertManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// RulesDir returns the host directory mounted at /etc/vmalert/rules: the
// explicit override when set, otherwise <base>/addon/vmalert/<name>/rules.
func (m *VmalertManager) RulesDir(vc *config.VmalertConfig) string {
	if vc.RulesDir != "" {
		return vc.RulesDir
	}
	return filepath.Join(m.dataDir, "addon", "vmalert", vc.Name, "rules")
}

// TemplatesDir returns the host directory mounted at /etc/vmalert/templates.
func (m *VmalertManager) TemplatesDir(vc *config.VmalertConfig) string {
	if vc.TemplatesDir != "" {
		return vc.TemplatesDir
	}
	return filepath.Join(m.dataDir, "addon", "vmalert", vc.Name, "templates")
}

// Endpoint returns the host-side HTTP URL used by status/reload. A wildcard or
// empty listen address is reached through loopback.
func (m *VmalertManager) Endpoint(vc *config.VmalertConfig) string {
	host := vc.Listen
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, vc.HostPort)
}

// vmalertArgsAndEnv builds the full `podman run` argv for a vmalert instance.
// Pure function (no podman calls) so mounts and flag order are unit-testable:
//   - NO --user flag: the image runs as root.
//   - Two read-only mounts: rules and templates are operator-owned files that
//     the container must never modify (:ro,z for SELinux relabeling).
//   - Everything after ImageTag is vmalert's OWN flags. `-rule` and
//     `-rule.templates` use container-side globs so newly added *.yaml/*.tpl
//     files are picked up on reload without recreating the container.
//   - ExtraArgs come LAST so they can override any earlier flag (last-wins).
func vmalertArgsAndEnv(vc *config.VmalertConfig, bridge bool, network, rulesDir, templatesDir string) []string {
	args := []string{"run", "-d", "--name", vc.ContainerName}
	args = append(args, netFlags(bridge, network, vc.HostPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(rulesDir), vmalertContainerRulesDir),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(templatesDir), vmalertContainerTemplatesDir),
	)
	args = append(args, vc.ImageTag)
	args = append(args,
		"-datasource.url="+vc.DatasourceURL,
		"-rule="+vmalertContainerRulesDir+"/*.yaml",
		"-rule.templates="+vmalertContainerTemplatesDir+"/*.tpl",
	)
	for _, u := range vc.NotifierURLs {
		args = append(args, "-notifier.url="+u)
	}
	for _, u := range vc.RemoteWriteURLs {
		args = append(args, "-remoteWrite.url="+u)
	}
	for _, u := range vc.RemoteReadURLs {
		args = append(args, "-remoteRead.url="+u)
	}
	if vc.ExternalURL != "" {
		args = append(args, "-external.url="+vc.ExternalURL)
	}
	for _, label := range vc.ExternalLabels {
		args = append(args, "-external.label="+label)
	}
	args = append(args,
		fmt.Sprintf("-httpListenAddr=%s:%d", proxyBindHost(bridge, vc.Listen), vc.HostPort),
	)
	args = append(args, vc.ExtraArgs...)
	return args
}

// createContainer seeds the rules/templates dirs (never overwriting existing
// files) and starts the container.
func (m *VmalertManager) createContainer(vc *config.VmalertConfig) error {
	rulesDir := m.RulesDir(vc)
	templatesDir := m.TemplatesDir(vc)
	if err := vmalert.SeedRulesDir(rulesDir); err != nil {
		return err
	}
	if err := vmalert.SeedTemplatesDir(templatesDir); err != nil {
		return err
	}

	args := vmalertArgsAndEnv(vc, m.bridge, m.cfg.Podman.Network, rulesDir, templatesDir)
	slog.Debug("podman vmalert run", "args", args)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating vmalert container: %w", err)
	}
	return nil
}

// EnsureImage pulls the vmalert image if it is not present locally.
func (m *VmalertManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling vmalert image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the vmalert container (install
// semantics: stop/rm/recreate so changed ports/listen/args take effect).
// Rule and template files are never touched.
func (m *VmalertManager) EnsureContainer(vc *config.VmalertConfig) error {
	containerName := vc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> vmalert container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping vmalert container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing vmalert container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale vmalert container: %w", err)
			}
		}
	}

	if err := m.createContainer(vc); err != nil {
		return err
	}
	fmt.Println("  [OK] vmalert container started")
	return nil
}

// StartContainer starts the container without recreating it (autostart path):
// running → no-op; stopped → podman start with recreate fallback; missing →
// create from stored config. Dirs are seeded first so a missing default dir
// does not produce a container with no rules.
func (m *VmalertManager) StartContainer(vc *config.VmalertConfig) error {
	if err := vmalert.SeedRulesDir(m.RulesDir(vc)); err != nil {
		return err
	}
	if err := vmalert.SeedTemplatesDir(m.TemplatesDir(vc)); err != nil {
		return err
	}

	containerName := vc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] vmalert %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] vmalert %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] vmalert %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper vmalert container: %w", err)
		}
	}

	if err := m.createContainer(vc); err != nil {
		return err
	}
	fmt.Printf("  [OK] vmalert %s started\n", containerName)
	return nil
}

// Reload asks a running vmalert to re-read its rules and templates via
// GET /-/reload. The reload is asynchronous: a 2xx response only means the
// signal was accepted; parse errors surface in the container log while the
// previous rules keep running.
func (m *VmalertManager) Reload(vc *config.VmalertConfig) error {
	running, err := m.containerRunning(vc.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("vmalert container %s is not running — run 'pg addon start vmalert --name %s' first", vc.ContainerName, vc.Name)
	}
	fmt.Println("-> Requesting vmalert reload (asynchronous)...")
	if err := vmalert.NewClient(m.Endpoint(vc)).Reload(context.Background()); err != nil {
		return fmt.Errorf("reloading vmalert: %w", err)
	}
	fmt.Println("  [OK] Reload requested — check 'pg addon vmalert status' or container logs to confirm the new rules loaded")
	return nil
}

// vmalertDirsToClean returns the dirs --clean-data may delete: only those
// still using the default managed location. An explicit user directory is
// never returned.
func vmalertDirsToClean(vc *config.VmalertConfig, rulesDir, templatesDir string) []string {
	var dirs []string
	if vc.RulesDir == "" {
		dirs = append(dirs, rulesDir)
	}
	if vc.TemplatesDir == "" {
		dirs = append(dirs, templatesDir)
	}
	return dirs
}

// Remove stops and removes the vmalert container. Rules and templates are kept
// by default; --clean-data deletes only default-managed dirs, never explicit
// --rules-dir/--templates-dir paths.
func (m *VmalertManager) Remove(vc *config.VmalertConfig, cleanData bool) error {
	containerName := vc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing vmalert container: %w", err)
	}
	fmt.Println("  [OK] vmalert container removed")

	rulesDir := m.RulesDir(vc)
	templatesDir := m.TemplatesDir(vc)
	if !cleanData {
		fmt.Printf("  [OK] Rules directory kept: %s\n", rulesDir)
		fmt.Printf("  [OK] Templates directory kept: %s\n", templatesDir)
		return nil
	}

	for _, dir := range vmalertDirsToClean(vc, rulesDir, templatesDir) {
		if isMountpoint(dir) {
			fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the rules below are really disposable\n", dir)
			continue
		}
		if err := removeHostDir(m.podman, vc.ImageTag, dir); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] Warning: removing directory %s: %v\n", dir, err)
		} else {
			fmt.Printf("  [OK] Directory removed: %s\n", dir)
		}
	}
	if vc.RulesDir != "" {
		fmt.Printf("  [OK] Rules directory kept (user-managed): %s\n", rulesDir)
	}
	if vc.TemplatesDir != "" {
		fmt.Printf("  [OK] Templates directory kept (user-managed): %s\n", templatesDir)
	}
	if vc.RulesDir == "" && vc.TemplatesDir == "" {
		// Default layout: prune the <name> dir once rules/ and templates/ are
		// gone. With an override the parent belongs to the user.
		_, _ = removeHostDirIfEmpty(m.podman, vc.ImageTag, filepath.Join(m.dataDir, "addon", "vmalert", vc.Name))
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *VmalertManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists.
func (m *VmalertManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a vmalert container.
func (m *VmalertManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *VmalertManager) run(args ...string) (string, error) {
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

func (m *VmalertManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *VmalertManager) imageExists(tag string) (bool, error) {
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

func (m *VmalertManager) containerExists(name string) (bool, error) {
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

func (m *VmalertManager) containerRunning(name string) (bool, error) {
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
