package podman

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/clickhouse"
	"github.com/mars-base/pgcli/internal/config"
)

// ClickHouseManager manages standalone ClickHouse addon containers. The
// official clickhouse-server image runs its entrypoint as root (then drops to
// uid 101) so it can chown the bind-mounted data dir. Host networking makes
// the configured HTTP/TCP ports host ports directly.
type ClickHouseManager struct {
	cfg     *config.Config
	podman  string
	dataDir string
}

// NewClickHouseManager creates a ClickHouseManager.
func NewClickHouseManager(cfg *config.Config) (*ClickHouseManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := ""
	if cfg != nil {
		dataDir = cfg.BaseDir
	}
	ensurePodmanStateReady(path)
	return &ClickHouseManager{cfg: cfg, podman: path, dataDir: dataDir}, nil
}

// EnsureImage pulls the pinned ClickHouse server image when missing.
func (m *ClickHouseManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling ClickHouse image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or recreates the container (install/config-change
// semantics). Data, logs, config and backups are never deleted here.
func (m *ClickHouseManager) EnsureContainer(cc *config.ClickHouseConfig) error {
	if err := m.ensureFiles(cc); err != nil {
		return err
	}
	running, err := m.containerRunning(cc.ContainerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> ClickHouse container already running, recreating to apply updated config...")
	}
	exists, err := m.containerExists(cc.ContainerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("rm", "-f", cc.ContainerName); err != nil {
			return fmt.Errorf("removing ClickHouse container: %w", err)
		}
	}
	if err := m.EnsureImage(cc.ImageTag); err != nil {
		return err
	}
	if err := m.createContainer(cc); err != nil {
		return err
	}
	return m.waitReady(cc)
}

// StartContainer starts without recreating (start/autostart path): running →
// no-op; stopped → podman start; missing → create. It waits for /ping.
// The first return value reports that it was already running.
func (m *ClickHouseManager) StartContainer(cc *config.ClickHouseConfig) (bool, error) {
	if err := m.ensureFiles(cc); err != nil {
		return false, err
	}
	running, err := m.containerRunning(cc.ContainerName)
	if err != nil {
		return false, err
	}
	if running {
		return true, nil
	}
	exists, err := m.containerExists(cc.ContainerName)
	if err != nil {
		return false, err
	}
	if exists {
		if _, err := m.run("start", cc.ContainerName); err == nil {
			fmt.Printf("  [OK] ClickHouse container %s started\n", cc.ContainerName)
			if err := m.waitReady(cc); err != nil {
				return false, err
			}
			return false, nil
		}
		fmt.Printf("  [!] ClickHouse container %s in improper state, recreating...\n", cc.ContainerName)
		if _, err := m.run("rm", "-f", cc.ContainerName); err != nil {
			return false, fmt.Errorf("removing improper ClickHouse container: %w", err)
		}
	}
	if err := m.EnsureImage(cc.ImageTag); err != nil {
		return false, err
	}
	if err := m.createContainer(cc); err != nil {
		return false, err
	}
	if err := m.waitReady(cc); err != nil {
		return false, err
	}
	return false, nil
}

// ensureFiles (re)writes the generated config and password file. The config is
// always re-rendered so a start after an external edit still reflects pg.yaml;
// data/log/backup dirs are created for the bind mounts.
func (m *ClickHouseManager) ensureFiles(cc *config.ClickHouseConfig) error {
	for _, dir := range []string{cc.DataDir, cc.LogDir, cc.BackupDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	if err := clickhouse.WriteGeneratedConfig(*cc); err != nil {
		return err
	}
	return clickhouse.WriteSecret(*cc)
}

func (m *ClickHouseManager) createContainer(cc *config.ClickHouseConfig) error {
	if _, err := m.run(clickHouseCreateArgs(*cc)...); err != nil {
		return fmt.Errorf("creating ClickHouse container: %w", err)
	}
	fmt.Printf("  [OK] ClickHouse container %s created\n", cc.ContainerName)
	return nil
}

// clickHouseEntrypointWrapper exports the admin password from the mounted
// secret before handing control to the stock entrypoint. remote_servers reads
// it with `from_env`, so Distributed queries can authenticate without the
// password ever appearing in a host file, the podman argv or the container
// environment configuration.
const clickHouseEntrypointWrapper = `export ` + clickhouse.AdminPasswordEnvVar +
	`="$(cat ` + clickhouse.ContainerSecretPath + `)"; exec /entrypoint.sh`

// clickHouseCreateArgs builds the `podman run` argv. Pure so tests can pin the
// mounts, environment and host networking without podman. Migrations inside
// the container land in the mounted data dir; the server config is the mounted
// config.d directory so the stock wildcard listener is replaced.
func clickHouseCreateArgs(cc config.ClickHouseConfig) []string {
	return []string{
		"run", "-d",
		"--name", cc.ContainerName,
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"--ulimit", "nofile=262144:262144",
		"--entrypoint", "/bin/sh",
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(cc.DataDir), clickhouse.ContainerDataDir),
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(cc.LogDir), clickhouse.ContainerLogDir),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(filepath.Dir(cc.ConfigPath)), clickhouse.ContainerConfigDir),
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(cc.BackupDir), clickhouse.ContainerBackupDir),
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(cc.SecretPath), clickhouse.ContainerSecretPath),
		"-e", "CLICKHOUSE_USER=" + cc.AdminUser,
		"-e", "CLICKHOUSE_PASSWORD_FILE=" + clickhouse.ContainerSecretPath,
		"-e", "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1",
		cc.ImageTag,
		"-c", clickHouseEntrypointWrapper,
	}
}

func (m *ClickHouseManager) waitReady(cc *config.ClickHouseConfig) error {
	if err := clickhouse.WaitForPing(*cc, clickhouse.StartTimeout); err != nil {
		return fmt.Errorf("ClickHouse did not become ready at %s: %w", clickhouse.HTTPURL(*cc), err)
	}
	return nil
}

// Stop stops the container and reports whether it was running.
func (m *ClickHouseManager) Stop(name string) (bool, error) {
	running, err := m.containerRunning(name)
	if err != nil || !running {
		return false, err
	}
	if _, err := m.run("stop", "-t", "30", name); err != nil {
		return true, fmt.Errorf("stopping ClickHouse container: %w", err)
	}
	return true, nil
}

// RemoveContainer stops and removes the container, keeping data unless CleanData
// is called separately.
func (m *ClickHouseManager) RemoveContainer(cc *config.ClickHouseConfig) error {
	exists, err := m.containerExists(cc.ContainerName)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("  [OK] ClickHouse container %s already absent\n", cc.ContainerName)
		return nil
	}
	if _, err := m.run("rm", "-f", cc.ContainerName); err != nil {
		return fmt.Errorf("removing ClickHouse container: %w", err)
	}
	fmt.Printf("  [OK] ClickHouse container %s removed\n", cc.ContainerName)
	return nil
}

// CleanData deletes the instance directory (config, secrets, logs, backups,
// and the default data dir) plus a distinct custom data dir. Files written by
// the container's uid 101 fall back to `podman unshare rm`.
func (m *ClickHouseManager) CleanData(cc *config.ClickHouseConfig) error {
	instanceDir := filepath.Dir(filepath.Dir(cc.ConfigPath))
	dirs := []string{instanceDir}
	if cc.DataDir != "" && !pathWithin(cc.DataDir, instanceDir) {
		dirs = append(dirs, cc.DataDir)
	}
	for _, dir := range dirs {
		if err := removeHostDir(m.podman, cc.ImageTag, dir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cleaning %s: %w", dir, err)
		}
		fmt.Printf("  [OK] cleaned %s\n", dir)
	}
	return nil
}

// pathWithin reports whether path is inside dir.
func pathWithin(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ContainerRunning reports whether the instance container is running.
func (m *ClickHouseManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether the instance container exists.
func (m *ClickHouseManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// ClickHouseExecSpec describes one in-container client invocation. One-shot
// SQL does not use this path: podman-static on this platform can deadlock
// inside `podman exec` (with or without -i), so statements go over the HTTP
// interface instead.
type ClickHouseExecSpec struct {
	// Command selects "client" (default) or "shell".
	Command     string
	Env         []string // KEY=VALUE pairs
	ClientArgs  []string // connection args from clickhouse.ClientArgs
	Interactive bool     // allocate a TTY and share the host's stdin
}

// ExecClient runs one statement through the HTTP interface. Interactive mode
// (and an empty statement) falls back to clickhouse-client in the container.
func (m *ClickHouseManager) ExecClient(cc *config.ClickHouseConfig, args []string, sql string, interactive bool) error {
	if !interactive && sql != "" {
		out, err := clickHouseHTTPQuery(cc, args, sql)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(out)
		return err
	}
	return m.exec(cc, ClickHouseExecSpec{
		Command:     "client",
		Env:         clickhouseClientEnv(cc),
		ClientArgs:  args,
		Interactive: interactive,
	})
}

// ExecClientOutput runs one statement through the HTTP interface and returns
// its response body.
func (m *ClickHouseManager) ExecClientOutput(cc *config.ClickHouseConfig, args []string, sql string) ([]byte, error) {
	if sql != "" {
		return clickHouseHTTPQuery(cc, args, sql)
	}
	return m.execOutput(cc, ClickHouseExecSpec{
		Command:    "client",
		Env:        clickhouseClientEnv(cc),
		ClientArgs: args,
	})
}

// ExecShell opens a shell in the container.
func (m *ClickHouseManager) ExecShell(cc *config.ClickHouseConfig) error {
	return m.exec(cc, ClickHouseExecSpec{Command: "shell", Interactive: true})
}

func clickhouseClientEnv(cc *config.ClickHouseConfig) []string {
	return []string{
		"CLICKHOUSE_USER=" + cc.AdminUser,
		"CLICKHOUSE_PASSWORD=" + cc.AdminPassword,
	}
}

// clickHouseHTTPQuery sends one statement to the server's HTTP interface. The
// addon uses host networking, so the configured HTTP port is reachable on the
// loopback address. ClickHouse reads the POST body as the statement itself
// (form encoding is not parsed), so the SQL goes in the body and the optional
// database in the URL query string. The password rides in an Authorization
// header. None of them appear in the podman argv or the host process list.
func clickHouseHTTPQuery(cc *config.ClickHouseConfig, clientArgs []string, sql string) ([]byte, error) {
	database := ""
	for i := 0; i+1 < len(clientArgs); i++ {
		if clientArgs[i] == "--database" {
			database = clientArgs[i+1]
		}
	}
	endpoint := clickhouse.HTTPURL(*cc) + "/"
	if database != "" {
		params := url.Values{}
		params.Set("database", database)
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(sql))
	if err != nil {
		return nil, fmt.Errorf("building ClickHouse HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.SetBasicAuth(cc.AdminUser, cc.AdminPassword)
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   5 * time.Minute,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("running ClickHouse query: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading ClickHouse response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("ClickHouse query failed: %s", msg)
	}
	return body, nil
}

// clickHouseExecArgs builds `podman exec` argv for interactive clients and
// shells. The `sh -c` wrapper receives "clickhouse-client" as $0 (never passed
// onward) and the client connection args as "$@", so credentials are read from
// the environment rather than the argv. Pure for tests.
func clickHouseExecArgs(spec ClickHouseExecSpec, container string) []string {
	args := []string{"exec"}
	if spec.Interactive {
		args = append(args, "-it")
	}
	for _, kv := range spec.Env {
		if name, _, ok := strings.Cut(kv, "="); ok && name != "" {
			args = append(args, "-e", name)
		}
	}
	if spec.Command == "shell" {
		return append(args, container, "bash")
	}
	cmd := `exec clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" "$@"`
	args = append(args, container, "sh", "-c", cmd, "clickhouse-client")
	return append(args, spec.ClientArgs...)
}

func (m *ClickHouseManager) exec(cc *config.ClickHouseConfig, spec ClickHouseExecSpec) error {
	cmd := podmanCommand(m.podman, clickHouseExecArgs(spec, cc.ContainerName)...)
	if len(spec.Env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, spec.Env...)
	}
	if spec.Interactive {
		cmd.Stdin = os.Stdin
	} else {
		// Never let an empty stdin swallow the caller's input.
		cmd.Stdin = nil
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running clickhouse-client: %w", err)
	}
	return nil
}

func (m *ClickHouseManager) execOutput(cc *config.ClickHouseConfig, spec ClickHouseExecSpec) ([]byte, error) {
	cmd := podmanCommand(m.podman, clickHouseExecArgs(spec, cc.ContainerName)...)
	if len(spec.Env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, spec.Env...)
	}
	return cmd.CombinedOutput()
}

// LogsTail prints the last lines of the container log to stderr.
func (m *ClickHouseManager) LogsTail(name string, lines int) {
	cmd := podmanCommand(m.podman, "logs", "--tail", fmt.Sprint(lines), name)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// --- internal helpers ---------------------------------------------------

func (m *ClickHouseManager) run(args ...string) (string, error) {
	cmd := podmanCommand(m.podman, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (m *ClickHouseManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *ClickHouseManager) imageExists(tag string) (bool, error) {
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

func (m *ClickHouseManager) containerExists(name string) (bool, error) {
	out, err := m.run("ps", "-a", "--format", "{{.Names}}")
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

func (m *ClickHouseManager) containerRunning(name string) (bool, error) {
	out, err := m.run("ps", "--format", "{{.Names}}")
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
