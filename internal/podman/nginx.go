package podman

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/tlsca"
)

// DefaultNginxImageTag is the built-in nginx image when an instance does not
// override it. The tag is pinned to a recent stable alpine release; the
// official image's default CMD runs nginx in the foreground, and pgcli
// overrides it with `nginx -g 'daemon off;'` for clarity.
const DefaultNginxImageTag = "docker.io/library/nginx:1.27-alpine"

// NginxManager manages standalone nginx reverse proxy containers. Like
// HAProxyManager it operates over the top-level (cross-instance) addon
// configs: nginx is shared routing infrastructure (path-based HTTP proxy in
// front of pgAdmin, PostgREST, etc.), not a per-instance sidecar.
//
// Unlike HAProxy (TCP load balancer, Linux-only for host-networked Patroni
// backends), nginx is an HTTP reverse proxy that works on both Linux (host
// networking) and macOS (pgcli-net bridge with published ports). The config
// is file-driven: pgcli renders nginx.conf and bind-mounts it read-only.
//
// TLS is optional. When enabled without a BYO cert, pgcli generates a
// self-signed leaf via tlsca.Generate under the instance's addon directory,
// alongside the rendered config. The HTTPS listener runs alongside HTTP so
// existing plain-text clients keep working.
type NginxManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

// nginx container-side paths, fixed by the image contract.
const (
	nginxContainerConfDir = "/etc/nginx/conf.d"      // bind-mount point for rendered server blocks
	nginxContainerCertDir = "/etc/nginx/certs"        // bind-mount point for TLS cert+key (when TLS)
	nginxContainerLogDir  = "/var/log/nginx"          // bind-mount point for access/error logs
)

// NewNginxManager creates an NginxManager. It works on both platforms: Linux
// serves over host networking, macOS over the pgcli-net bridge with the ports
// published, so the Mac's 127.0.0.1:<port> reaches it. The upstream image is
// multi-arch, so any host architecture works.
func NewNginxManager(cfg *config.Config) (*NginxManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &NginxManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// configDir returns the host directory holding an instance's nginx.conf and
// optional TLS certificates.
func nginxConfigDir(baseDir, name string) string {
	return filepath.Join(baseDir, "addon", "nginx", name)
}

// tlsDir returns the host sub-directory for TLS certificates.
func nginxTLSDir(baseDir, name string) string {
	return filepath.Join(nginxConfigDir(baseDir, name), "tls")
}

// logDir returns the host sub-directory for nginx log files.
func nginxLogDir(baseDir, name string) string {
	return filepath.Join(nginxConfigDir(baseDir, name), "log")
}

// RenderNginxCfg builds the nginx.conf text for one instance. Pure function
// of the config so it can be unit-tested without podman. The output is a
// single-file nginx config (no conf.d includes) with upstream and server
// blocks for each backend.
//
// When TLS is enabled, certFile and keyFile are the container-side paths
// (e.g. /etc/nginx/certs/public.crt) — the caller is responsible for
// bind-mounting the actual files there.
func RenderNginxCfg(nc *config.NginxConfig) (string, error) {
	// ConfFile mode: user provides the full config, skip rendering.
	if nc.ConfFile != "" {
		return "", nil
	}
	if len(nc.Backends) == 0 {
		return "", fmt.Errorf("nginx %q has no backend targets — pass --upstream name=...,path=...,backend=... or --pgadmin-name/--postgrest-name", nc.Name)
	}

	listen := nc.Listen
	if listen == "" {
		listen = "127.0.0.1"
	}

	var b strings.Builder

	// Main context
	b.WriteString("worker_processes auto;\n")
	b.WriteString("pid /tmp/nginx.pid;\n")
	b.WriteString("\n")

	workerConns := nc.WorkerConnections
	if workerConns <= 0 {
		workerConns = 1024
	}

	b.WriteString("events {\n")
	fmt.Fprintf(&b, "    worker_connections %d;\n", workerConns)
	b.WriteString("}\n")
	b.WriteString("\n")

	b.WriteString("http {\n")
	b.WriteString("    access_log /var/log/nginx/access.log;\n")
	b.WriteString("    error_log  /var/log/nginx/error.log;\n")
	b.WriteString("\n")

	// Upstream blocks — one per backend
	for _, be := range nc.Backends {
		name := be.Name
		if name == "" {
			name = "upstream_" + sanitizeNginxName(be.Path)
		}
		fmt.Fprintf(&b, "    upstream %s {\n", name)
		fmt.Fprintf(&b, "        server %s;\n", be.Backend)
		b.WriteString("    }\n")
		b.WriteString("\n")
	}

	// HTTP server block
	b.WriteString("    server {\n")
	fmt.Fprintf(&b, "        listen %s:%d;\n", listen, nc.HTTPPort)
	b.WriteString("        access_log /var/log/nginx/access.log;\n")
	b.WriteString("        error_log  /var/log/nginx/error.log;\n")
	b.WriteString("\n")
	for _, be := range nc.Backends {
		name := be.Name
		if name == "" {
			name = "upstream_" + sanitizeNginxName(be.Path)
		}
		logName := sanitizeNginxName(name)
		path := be.Path
		if path == "" {
			path = "/"
		}
		fmt.Fprintf(&b, "        location %s {\n", path)
		fmt.Fprintf(&b, "            proxy_pass http://%s;\n", name)
		b.WriteString("            proxy_set_header Host $host;\n")
		b.WriteString("            proxy_set_header X-Real-IP $remote_addr;\n")
		b.WriteString("            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
		b.WriteString("            proxy_set_header X-Forwarded-Proto $scheme;\n")
		fmt.Fprintf(&b, "            access_log /var/log/nginx/%s.access.log;\n", logName)
		fmt.Fprintf(&b, "            error_log  /var/log/nginx/%s.error.log;\n", logName)
		b.WriteString("        }\n")
		b.WriteString("\n")
	}
	b.WriteString("    }\n")

	// HTTPS server block (TLS only)
	if nc.TLS && nc.HTTPSPort > 0 {
		certFile := filepath.Join(nginxContainerCertDir, tlsca.ServerCert)
		keyFile := filepath.Join(nginxContainerCertDir, tlsca.ServerKey)
		b.WriteString("\n")
		b.WriteString("    server {\n")
		fmt.Fprintf(&b, "        listen %s:%d ssl;\n", listen, nc.HTTPSPort)
		fmt.Fprintf(&b, "        ssl_certificate     %s;\n", certFile)
		fmt.Fprintf(&b, "        ssl_certificate_key %s;\n", keyFile)
		b.WriteString("        ssl_protocols TLSv1.2 TLSv1.3;\n")
		b.WriteString("        access_log /var/log/nginx/access.log;\n")
		b.WriteString("        error_log  /var/log/nginx/error.log;\n")
		b.WriteString("\n")
		for _, be := range nc.Backends {
			name := be.Name
			if name == "" {
				name = "upstream_" + sanitizeNginxName(be.Path)
			}
			logName := sanitizeNginxName(name)
			path := be.Path
			if path == "" {
				path = "/"
			}
			fmt.Fprintf(&b, "        location %s {\n", path)
			fmt.Fprintf(&b, "            proxy_pass http://%s;\n", name)
			b.WriteString("            proxy_set_header Host $host;\n")
			b.WriteString("            proxy_set_header X-Real-IP $remote_addr;\n")
			b.WriteString("            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
			b.WriteString("            proxy_set_header X-Forwarded-Proto $scheme;\n")
			fmt.Fprintf(&b, "            access_log /var/log/nginx/%s.access.log;\n", logName)
			fmt.Fprintf(&b, "            error_log  /var/log/nginx/%s.error.log;\n", logName)
			b.WriteString("        }\n")
			b.WriteString("\n")
		}
		b.WriteString("    }\n")
	}

	b.WriteString("}\n")
	return b.String(), nil
}

// sanitizeNginxName replaces characters that are invalid in an nginx upstream
// name with underscores.
func sanitizeNginxName(s string) string {
	s = strings.TrimPrefix(s, "/")
	if s == "" {
		return "root"
	}
	return strings.NewReplacer("/", "_", ".", "_", "-", "_", ":", "_").Replace(s)
}

// WriteConfigs renders nginx.conf for the given instance into
// <baseDir>/addon/nginx/<name>/. When ConfFile is set, the user's file is
// copied instead of rendering the template. When TLS is enabled and no BYO
// cert is provided, a self-signed cert is generated via tlsca.Generate. The
// file carries no secrets (unless TLS with BYO key), so it is world-readable.
func (m *NginxManager) WriteConfigs(nc *config.NginxConfig) (string, error) {
	dir := nginxConfigDir(m.dataDir, nc.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating nginx config dir: %w", err)
	}

	// Create log directory (always, for both template and ConfFile modes)
	logDir := nginxLogDir(m.dataDir, nc.Name)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return "", fmt.Errorf("creating nginx log dir: %w", err)
	}

	confPath := filepath.Join(dir, "nginx.conf")

	// ConfFile mode: copy the user's file instead of rendering.
	if nc.ConfFile != "" {
		data, err := os.ReadFile(nc.ConfFile)
		if err != nil {
			return "", fmt.Errorf("reading user conf file %s: %w", nc.ConfFile, err)
		}
		if len(data) == 0 {
			return "", fmt.Errorf("user conf file %s is empty", nc.ConfFile)
		}
		if err := os.WriteFile(confPath, data, 0644); err != nil {
			return "", fmt.Errorf("writing nginx.conf: %w", err)
		}
		return confPath, nil
	}

	// TLS: generate self-signed cert or validate BYO paths
	if nc.TLS && nc.TLSCert == "" {
		tlsDir := nginxTLSDir(m.dataDir, nc.Name)
		hosts := tlsca.LocalHosts(nc.Listen)
		if _, err := tlsca.Generate(tlsDir, hosts, 30*24*time.Hour); err != nil {
			return "", fmt.Errorf("generating TLS certificates: %w", err)
		}
	}

	cfg, err := RenderNginxCfg(nc)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(confPath, []byte(cfg), 0644); err != nil {
		return "", fmt.Errorf("writing nginx.conf: %w", err)
	}
	return confPath, nil
}

// EnsureContainer creates or restarts the nginx container (install semantics:
// stop/rm/recreate so a re-rendered nginx.conf takes effect).
func (m *NginxManager) EnsureContainer(nc *config.NginxConfig) error {
	containerName := nc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> nginx container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping nginx container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing nginx container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale nginx container: %w", err)
			}
		}
	}

	if err := m.createContainer(nc); err != nil {
		return err
	}
	fmt.Println("  [OK] nginx container started")
	return nil
}

// StartContainer starts the nginx container without regenerating config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create from the nginx.conf
// already on disk.
func (m *NginxManager) StartContainer(nc *config.NginxConfig) error {
	containerName := nc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] nginx %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] nginx %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] nginx %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper nginx container: %w", err)
		}
	}

	cfgPath := filepath.Join(nginxConfigDir(m.dataDir, nc.Name), "nginx.conf")
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("nginx config %s not found -- run 'pg addon install nginx' first", cfgPath)
	}

	if err := m.createContainer(nc); err != nil {
		return err
	}
	fmt.Printf("  [OK] nginx %s started\n", containerName)
	return nil
}

// nginxArgsAndEnv builds the full `podman run` argv for an nginx instance.
// Pure function (no podman calls) so the networking shape and mount set are
// unit-testable:
//   - netFlags: host networking on Linux, bridge with published ports on macOS.
//   - The rendered nginx.conf is bind-mounted at /etc/nginx/nginx.conf:ro.
//   - When TLS is enabled: the cert directory is bind-mounted at
//     /etc/nginx/certs:ro. With self-signed certs the host dir is
//     <base>/addon/nginx/<name>/tls/; with BYO certs the user's files are
//     copied there first (by the caller) and mounted from the same path.
//   - proxyBindHost widens a loopback listen to 0.0.0.0 under bridge.
func nginxArgsAndEnv(nc *config.NginxConfig, bridge bool, network, configHostDir string) []string {
	args := []string{"run", "-d", "--name", nc.ContainerName}

	// Publish the right ports under bridge; host networking needs none.
	ports := []int{nc.HTTPPort}
	if nc.TLS && nc.HTTPSPort > 0 {
		ports = append(ports, nc.HTTPSPort)
	}
	args = append(args, netFlags(bridge, network, ports...)...)

	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(filepath.Join(configHostDir, "nginx.conf")), "/etc/nginx/nginx.conf"),
		"-v", fmt.Sprintf("%s:%s:z", hostMountPath(filepath.Join(configHostDir, "log")), nginxContainerLogDir),
	)

	// TLS cert mount
	if nc.TLS {
		tlsHostDir := filepath.Join(configHostDir, "tls")
		args = append(args,
			"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(tlsHostDir), nginxContainerCertDir),
		)
	}

	args = append(args, nc.ImageTag)
	args = append(args, "nginx", "-g", "daemon off;")
	return args
}

// createContainer runs an nginx instance with its config file bind-mounted
// read-only. Host networking on Linux; bridge with published ports on macOS.
func (m *NginxManager) createContainer(nc *config.NginxConfig) error {
	dir := nginxConfigDir(m.dataDir, nc.Name)

	// For BYO TLS: copy the user's cert+key into the addon tls dir so the
	// bind mount is always from a single, predictable location.
	if nc.TLS && nc.TLSCert != "" && nc.TLSKey != "" {
		tlsDir := filepath.Join(dir, "tls")
		if err := os.MkdirAll(tlsDir, 0700); err != nil {
			return fmt.Errorf("creating nginx TLS dir: %w", err)
		}
		if err := copyFile(nc.TLSCert, filepath.Join(tlsDir, tlsca.ServerCert)); err != nil {
			return fmt.Errorf("copying TLS cert: %w", err)
		}
		if err := copyFile(nc.TLSKey, filepath.Join(tlsDir, tlsca.ServerKey)); err != nil {
			return fmt.Errorf("copying TLS key: %w", err)
		}
	}

	args := nginxArgsAndEnv(nc, m.bridge, m.cfg.Podman.Network, dir)
	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating nginx container: %w", err)
	}
	return nil
}

// copyFile copies a file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0600)
}

// Remove stops and removes the nginx container, then deletes its config
// directory on the host.
func (m *NginxManager) Remove(nc *config.NginxConfig) error {
	containerName := nc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing nginx container: %w", err)
	}
	fmt.Println("  [OK] nginx container removed")

	dir := nginxConfigDir(m.dataDir, nc.Name)
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing config dir %s: %v\n", dir, err)
	} else {
		fmt.Printf("  [OK] Config directory removed: %s\n", dir)
	}
	parent := filepath.Dir(dir)
	os.Remove(parent) // ignore error — non-empty dir won't be removed

	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *NginxManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// Stop stops an nginx container.
func (m *NginxManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// EnsureImage pulls the nginx image if it is not present locally (pull-only:
// the tags are public docker.io/library images; pgcli never builds them).
func (m *NginxManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling nginx image %s (is docker.io reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// --- Internal helpers ---------------------------------------------------

func (m *NginxManager) run(args ...string) (string, error) {
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

func (m *NginxManager) runInteractive(args ...string) error {
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *NginxManager) imageExists(tag string) (bool, error) {
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

func (m *NginxManager) containerExists(name string) (bool, error) {
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

func (m *NginxManager) containerRunning(name string) (bool, error) {
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

// Test validates nginx config syntax using a temporary (--rm) container.
// Does NOT require the nginx container to be running — it spins up a
// throwaway container with the same image + mounts, runs `nginx -t`, and
// exits. This lets users test config changes before reloading.
func (m *NginxManager) Test(nc *config.NginxConfig) error {
	dir := nginxConfigDir(m.dataDir, nc.Name)
	confPath := filepath.Join(dir, "nginx.conf")
	if _, err := os.Stat(confPath); err != nil {
		return fmt.Errorf("nginx config %s not found — run 'pg addon install nginx' first", confPath)
	}

	args := []string{
		"run", "--rm", "--name", nc.ContainerName + "-test",
		"-v", hostMountPath(confPath) + ":/etc/nginx/nginx.conf:ro,z",
		"-v", hostMountPath(filepath.Join(dir, "log")) + ":" + nginxContainerLogDir + ":z",
	}
	// Mount TLS certs if enabled (cert dir exists)
	if nc.TLS {
		tlsHostDir := nginxTLSDir(m.dataDir, nc.Name)
		args = append(args, "-v", hostMountPath(tlsHostDir)+":"+nginxContainerCertDir+":ro,z")
	}
	args = append(args, nc.ImageTag, "nginx", "-t")
	return m.runInteractive(args...)
}

// Reload sends a reload signal to nginx inside the running container (graceful restart).
func (m *NginxManager) Reload(nc *config.NginxConfig) error {
	running, err := m.containerRunning(nc.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("nginx container %s is not running — run 'pg addon start nginx --name %s' first", nc.ContainerName, nc.Name)
	}
	return m.runInteractive("exec", nc.ContainerName, "nginx", "-s", "reload")
}

// Exec runs an arbitrary command inside the nginx container, with stdio passthrough.
func (m *NginxManager) Exec(nc *config.NginxConfig, args []string) error {
	running, err := m.containerRunning(nc.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("nginx container %s is not running — run 'pg addon start nginx --name %s' first", nc.ContainerName, nc.Name)
	}
	execArgs := []string{"exec", "-i", nc.ContainerName}
	execArgs = append(execArgs, args...)
	return m.runInteractive(execArgs...)
}
