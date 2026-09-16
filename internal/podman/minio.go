package podman

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/tlsca"
)

// MinioManager manages standalone single-node MinIO containers: S3-compatible
// object storage as shared infrastructure (top-level addons.minio.<name>),
// the intended use being a pgBackRest repository every host of a Patroni
// cluster can reach. The image is the public pre-built ghcr tag (dual-arch:
// upstream static binaries on Alpine — MinIO's official image dropped the web
// console); runtime image handling is pull-only, like etcd.
type MinioManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports (host networking binds the VM loopback, invisible to the Mac)
}

// NewMinioManager creates a MinioManager. It works on both platforms: Linux
// serves over host networking, macOS over the pgcli-net bridge with each port
// published (the same path the instance and the proxy addons use), so the
// Mac's 127.0.0.1:<port> reaches it. The public image is dual-arch
// (amd64+arm64), so any host architecture works.
func NewMinioManager(cfg *config.Config) (*MinioManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &MinioManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/minio/<name>/data. It holds the
// object storage itself and outlives the container — `pg addon remove` keeps
// it unless --clean-data.
func (m *MinioManager) resolveDataDir(mc *config.MinioConfig) string {
	if mc.DataDir != "" {
		return mc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "minio", mc.Name, "data")
}

// DataDir returns the resolved host data directory (for display).
func (m *MinioManager) DataDir(mc *config.MinioConfig) string {
	return m.resolveDataDir(mc)
}

// TLSDir is the host dir holding this addon's self-signed CA + leaf cert when
// mc.TLS is on. MinIO's --certs-dir wants public.crt/private.key there; the CA
// pair sits alongside for distribution (pgBackRest's repo*-s3-ca-file).
func (m *MinioManager) TLSDir(mc *config.MinioConfig) string {
	return filepath.Join(m.dataDir, "tls", "minio", mc.Name)
}

// EnsureTLS (re)generates the addon's cert material when needed and returns
// the CA cert path. Called before container creation whenever mc.TLS is set.
// SANs cover every address a client can dial: loopback names, this host's NIC
// IPs (members/backup containers run on host networking), the configured
// Listen bind, and the host MINIO_SERVER_URL advertises.
func (m *MinioManager) EnsureTLS(mc *config.MinioConfig) (string, error) {
	hosts := tlsca.LocalHosts(mc.Listen)
	if u := m.serverURL(mc); u != "" {
		if h, _, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")); err == nil {
			hosts = append(hosts, h)
		}
	}
	// 30 days: renew well before the 825-day expiry if NIC IPs changed.
	return tlsca.Generate(m.TLSDir(mc), hosts, 30*24*time.Hour)
}

// DataDirSharesRootDevice reports whether the resolved data dir sits on the same
// block device as the host root filesystem. MinIO's distributed-cluster startup
// rejects a drive that is "part of root drive, will not be used" (a safety check
// against accidentally using the OS disk as an EC volume), so a cluster's
// data_dir must live on a separately-mounted disk. Single-node mode has no such
// requirement, so this only matters when Endpoints is non-empty. Callers compare
// it to decide whether to warn at install time; it is advisory, never an error.
//
// The dir may not exist yet at install time (createContainer creates it), so we
// stat the nearest existing ancestor — the device id is inherited until an actual
// mount point, which is exactly what the check needs to see.
func (m *MinioManager) DataDirSharesRootDevice(mc *config.MinioConfig) (bool, error) {
	root, err := os.Stat("/")
	if err != nil {
		return false, fmt.Errorf("stat root fs: %w", err)
	}
	data, err := statNearestExisting(m.resolveDataDir(mc))
	if err != nil {
		return false, fmt.Errorf("stat data dir: %w", err)
	}
	rd, ok1 := root.Sys().(*syscall.Stat_t)
	dd, ok2 := data.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false, nil // can't compare device ids on this platform; assume not-shared
	}
	return rd.Dev == dd.Dev, nil
}

// statNearestExisting stats path, walking up to its parent until a path exists.
// Returns the FileInfo of the first existing path found.
func statNearestExisting(path string) (os.FileInfo, error) {
	p := filepath.Clean(path)
	for {
		if fi, err := os.Stat(p); err == nil {
			return fi, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil, fmt.Errorf("no existing ancestor of %s", path)
		}
		p = parent
	}
}

// EnsureImage pulls the MinIO image if it is not present locally (pull-only:
// the image is published to the public ghcr repo; pgcli never builds it).
func (m *MinioManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling minio image %s (is ghcr.io/mars-base/pgcli reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the MinIO container (install semantics:
// stop/rm/recreate so updated ports/credentials take effect). The data dir is
// created if missing; it is never deleted here.
func (m *MinioManager) EnsureContainer(mc *config.MinioConfig) error {
	containerName := mc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> MinIO container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping MinIO container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing MinIO container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale MinIO container: %w", err)
			}
		}
	}

	if err := m.createContainer(mc); err != nil {
		return err
	}
	fmt.Println("  [OK] MinIO container started")
	return nil
}

// StartContainer starts the MinIO container without touching config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh (the data dir
// and all settings live in the config, unlike haproxy's on-disk cfg file).
func (m *MinioManager) StartContainer(mc *config.MinioConfig) error {
	containerName := mc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] MinIO %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] MinIO %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] MinIO %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper MinIO container: %w", err)
		}
	}

	if err := m.createContainer(mc); err != nil {
		return err
	}
	fmt.Printf("  [OK] MinIO %s started\n", containerName)
	return nil
}

// createContainer runs MinIO: single-node serving /data (the bind-mounted
// data dir) when mc.Endpoints is empty, or a distributed cluster with mc.Endpoints
// as the shared endpoint list when non-empty (see config.MinioConfig.Endpoints).
// Linux: host networking, bound to mc.Listen:APIPort (S3 API) and
// mc.Listen:ConsolePort (web console). macOS: the same ports on the pgcli-net
// bridge, published -p N:N so the Mac reaches them on 127.0.0.1 — which needs
// the server bound to 0.0.0.0 inside the container (proxyBindHost) and
// MINIO_SERVER_URL pointing at the address clients use. Credentials are
// passed as env, which makes them visible in `podman inspect` — the same
// exposure as a hand-run container with -e, acceptable for a rootless
// single-host deployment.
func (m *MinioManager) createContainer(mc *config.MinioConfig) error {
	dataDir := m.resolveDataDir(mc)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating MinIO data dir: %w", err)
	}

	bind := proxyBindHost(m.bridge, mc.Listen)

	// TLS: MinIO serves HTTPS natively from --certs-dir (public.crt +
	// private.key). pgBackRest refuses plaintext HTTP for S3 repos, so a MinIO
	// feeding Patroni archive-push must be TLS; pgcli's own CA (tlsca) covers
	// every address clients dial and is distributed as repo*-s3-ca-file.
	var tlsDir string
	if mc.TLS {
		caPath, err := m.EnsureTLS(mc)
		if err != nil {
			return err
		}
		tlsDir = m.TLSDir(mc)
		fmt.Printf("  [OK] TLS certs (CA: %s)\n", caPath)
	}

	// MinIO's deployment docs recommend nofile=1048576. The podman machine VM
	// caps RLIMIT_NOFILE at its host's hard limit (524288 on the current
	// applehv image), and crun refuses to set soft > hard outright, so macOS
	// raises a lower, comfortably-allowed value: plenty for single-host
	// dev/test. The Linux path keeps the upstream recommendation.
	nofile := "1048576"
	if m.bridge {
		nofile = "65536"
	}

	// --http-proxy=false: podman would otherwise inject the host's HTTP(S)_PROXY
	// and the process could honor it for outbound connections. Image pulls run
	// client-side and keep the proxy.
	args := []string{
		"run", "-d",
		"--name", mc.ContainerName,
	}
	args = append(args, netFlags(m.bridge, m.cfg.Podman.Network, mc.APIPort, mc.ConsolePort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--ulimit", "nofile="+nofile+":"+nofile,
		"--stop-timeout", "60",
		"-v", fmt.Sprintf("%s:/data:z", hostMountPath(dataDir)),
		"-e", "MINIO_ROOT_USER="+mc.RootUser,
		"-e", "MINIO_ROOT_PASSWORD="+mc.RootPassword,
	)
	if tlsDir != "" {
		args = append(args,
			"-v", fmt.Sprintf("%s:/opt/minio/certs:ro,z", hostMountPath(tlsDir)),
		)
	}
	// MINIO_SERVER_URL must be byte-identical on every node or MinIO refuses to
	// form the cluster ("Mismatching environment values: [MINIO_SERVER_URL]",
	// each node then loops on "Waiting for at least 1 remote servers with valid
	// configuration"). Distributed nodes each advertise their own reachable IP,
	// so there is no single value to pin — omit it and let the endpoint list
	// define addresses. Single-node keeps pointing clients at the local URL.
	if len(mc.Endpoints) == 0 {
		args = append(args, "-e", "MINIO_SERVER_URL="+m.serverURL(mc))
	}
	args = append(args, mc.ImageTag)
	// Distributed mode: Endpoints is a cluster-wide list every node carries
	// verbatim (e.g. http://10.0.0.1:9000/data). The container bind-mounts
	// mc.DataDir at /data unconditionally, so "/data" is the correct export
	// path for every endpoint in that list. Single-node mode keeps serving
	// just /data — same as before this field existed.
	if len(mc.Endpoints) > 0 {
		args = append(args, "server")
		args = append(args, mc.Endpoints...)
	} else {
		args = append(args, "server", "/data")
	}
	args = append(args,
		"--address", fmt.Sprintf("%s:%d", bind, mc.APIPort),
		"--console-address", fmt.Sprintf("%s:%d", bind, mc.ConsolePort),
	)
	if tlsDir != "" {
		args = append(args, "--certs-dir", "/opt/minio/certs")
	}

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating MinIO container: %w", err)
	}
	return nil
}

// serverURL returns the MINIO_SERVER_URL — the address S3 clients are told to
// use. On Linux that is the configured listen + API port. On macOS the store
// is published on the bridge and the Mac reaches it on its own loopback, so
// the URL must say 127.0.0.1, not the container's internal bind.
func (m *MinioManager) serverURL(mc *config.MinioConfig) string {
	host := mc.Listen
	if m.bridge {
		host = "127.0.0.1"
	}
	scheme := "http"
	if mc.TLS {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, mc.APIPort)
}

// Remove stops and removes the MinIO container. The data dir is kept by
// default (it is the backup repository); cleanData also deletes it.
func (m *MinioManager) Remove(mc *config.MinioConfig, cleanData bool) error {
	containerName := mc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing MinIO container: %w", err)
	}
	fmt.Println("  [OK] MinIO container removed")

	dataDir := m.resolveDataDir(mc)
	if !cleanData {
		fmt.Printf("  [OK] Data directory kept: %s\n", dataDir)
		return nil
	}
	if err := os.RemoveAll(dataDir); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  [!] Warning: removing data dir %s: %v\n", dataDir, err)
	} else {
		fmt.Printf("  [OK] Data directory removed: %s\n", dataDir)
	}
	if mc.DataDir == "" {
		// Default layout: also prune the <name> dir when it became empty. With
		// an override the parent belongs to the user — never touch it.
		os.Remove(filepath.Dir(dataDir)) // ignore error — non-empty dir won't be removed
	}

	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *MinioManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *MinioManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a MinIO container.
func (m *MinioManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *MinioManager) run(args ...string) (string, error) {
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

func (m *MinioManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *MinioManager) imageExists(tag string) (bool, error) {
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

func (m *MinioManager) containerExists(name string) (bool, error) {
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

func (m *MinioManager) containerRunning(name string) (bool, error) {
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
