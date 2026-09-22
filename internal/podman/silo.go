package podman

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/tlsca"
)

// SiloManager manages standalone silo containers — silo being Pigsty's fork of
// MinIO (https://silo.pgsty.com). It is the field-for-field twin of
// MinioManager because silo keeps MinIO's whole surface: the S3 API, the
// MINIO_* env contract, the `server /data --address --console-address` command
// line, the --certs-dir public.crt/private.key layout, and the distributed EC
// endpoints mode. The image (docker.io/pgsty/silo) ships the web console and
// its mcli client in one multi-arch build, so unlike pgcli-minio it is pulled
// straight from docker.io (pull-only, like etcd — pgcli never builds it). The
// only container-run difference is an explicit `--entrypoint silo`: the image
// defaults to a docker-entrypoint.sh wrapper, and pgcli drives the binary
// directly the same way it does for MinIO.
type SiloManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports (host networking binds the VM loopback, invisible to the Mac)
}

// NewSiloManager creates a SiloManager. It works on both platforms: Linux
// serves over host networking, macOS over the pgcli-net bridge with each port
// published (the same path minio uses), so the Mac's 127.0.0.1:<port> reaches
// it. The public image is dual-arch (amd64+arm64), so any host architecture
// works.
func NewSiloManager(cfg *config.Config) (*SiloManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &SiloManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/silo/<name>/data. It holds the
// object storage itself and outlives the container — `pg addon remove` keeps
// it unless --clean-data.
func (m *SiloManager) resolveDataDir(sc *config.SiloConfig) string {
	if sc.DataDir != "" {
		return sc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "silo", sc.Name, "data")
}

// resolveDriveDirs returns the host directories backing this instance's silo
// drives. Whenever sc.Drives is non-empty it returns the configured drives
// verbatim, in order: drive N is mounted at /dataN and silo erasure-codes
// across them — single-node (SNMD) or as one node of a distributed set (MNMD,
// where the endpoints carry the full host×drive matrix). Otherwise it collapses
// to the single resolveDataDir, so SNSD/MNSD stay the one-drive case.
func (m *SiloManager) resolveDriveDirs(sc *config.SiloConfig) []string {
	if len(sc.Drives) > 0 {
		return sc.Drives
	}
	return []string{m.resolveDataDir(sc)}
}

// Drives returns the resolved host drive dirs (for display).
func (m *SiloManager) Drives(sc *config.SiloConfig) []string {
	return m.resolveDriveDirs(sc)
}

// DataDir returns the resolved host data directory (for display).
func (m *SiloManager) DataDir(sc *config.SiloConfig) string {
	return m.resolveDataDir(sc)
}

// TLSDir is the host dir holding this addon's self-signed CA + leaf cert when
// sc.TLS is on. silo's --certs-dir wants public.crt/private.key there (the
// same contract as MinIO); the CA pair sits alongside for distribution
// (pgBackRest's repo*-s3-ca-file).
func (m *SiloManager) TLSDir(sc *config.SiloConfig) string {
	return filepath.Join(m.dataDir, "tls", "silo", sc.Name)
}

// EnsureTLS (re)generates the addon's cert material when needed and returns
// the CA cert path. Called before container creation whenever sc.TLS is set.
// SANs cover every address a client can dial: loopback names, this host's NIC
// IPs (members/backup containers run on host networking), the configured
// Listen bind, and the host MINIO_SERVER_URL advertises. In BYO mode it never
// generates — it validates the user's pair instead and returns an empty CA
// path (the cert bundle itself is the client trust anchor).
func (m *SiloManager) EnsureTLS(sc *config.SiloConfig) (string, error) {
	if sc.TLS && (sc.CertFile != "") != (sc.KeyFile != "") {
		fmt.Printf("  [!] silo %s has only one of cert_file/key_file set — ignoring the pair and using generated certs\n", sc.ContainerName)
	}
	if BYOTLS(sc.TLS, sc.CertFile, sc.KeyFile) {
		if _, err := ValidateBYOCert(sc.CertFile, sc.KeyFile); err != nil {
			return "", err
		}
		return "", nil
	}
	hosts := tlsca.LocalHosts(sc.Listen)
	if u := m.serverURL(sc); u != "" {
		if h, _, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")); err == nil {
			hosts = append(hosts, h)
		}
	}
	// 30 days: renew well ahead of expiry when NIC IPs changed.
	return tlsca.Generate(m.TLSDir(sc), hosts, 30*24*time.Hour)
}

// DataDirSharesRootDevice reports whether the resolved data dir sits on the same
// block device as the host root filesystem. silo's distributed-cluster startup
// (like MinIO) rejects a drive that is "part of root drive, will not be used",
// so a cluster's data_dir must live on a separately-mounted disk. Single-node
// mode has no such requirement, so this only matters when Endpoints is
// non-empty. It is advisory, never an error.
func (m *SiloManager) DataDirSharesRootDevice(sc *config.SiloConfig) (bool, error) {
	return sharesRootDevice(m.resolveDataDir(sc))
}

// DrivesSharingRootDevice returns the subset of this instance's drive dirs that
// sit on the host root device — the drives silo/MinIO refuse at startup in
// either multi-drive mode (MNSD's single export path or SNMD's per-drive list).
// SNSD has no such requirement, so callers gate the warning on
// len(Endpoints)>0 || len(Drives)>0. Advisory, never fatal.
func (m *SiloManager) DrivesSharingRootDevice(sc *config.SiloConfig) []string {
	var bad []string
	for _, d := range m.resolveDriveDirs(sc) {
		if shared, err := sharesRootDevice(d); err == nil && shared {
			bad = append(bad, d)
		}
	}
	return bad
}

// EnsureImage pulls the silo image if it is not present locally (pull-only: the
// image is published to the public docker.io/pgsty repo; pgcli never builds it).
func (m *SiloManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling silo image %s (is docker.io/pgsty reachable?): %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the silo container (install semantics:
// stop/rm/recreate so updated ports/credentials take effect). The data dir is
// created if missing; it is never deleted here.
func (m *SiloManager) EnsureContainer(sc *config.SiloConfig) error {
	containerName := sc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> silo container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping silo container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing silo container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale silo container: %w", err)
			}
		}
	}

	if err := m.createContainer(sc); err != nil {
		return err
	}
	fmt.Println("  [OK] silo container started")
	return nil
}

// StartContainer starts the silo container without touching config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh.
func (m *SiloManager) StartContainer(sc *config.SiloConfig) error {
	containerName := sc.ContainerName

	// TLS: run the cert check even against a live container. Generated mode
	// refreshes the leaf here — silo watches its cert files and hot-reloads,
	// which is how a deployment picks up a re-signed leaf+CA chain (the
	// `pg backup fetch-ca` anchor) without a --force recreate. BYO mode has
	// nothing to refresh (pgcli never re-signs the operator's files), so this
	// only re-validates the pair. Best-effort either way: a failure must not
	// block the start — the mounted cert is very likely still valid.
	if sc.TLS {
		if _, err := m.EnsureTLS(sc); err != nil {
			if BYOTLS(sc.TLS, sc.CertFile, sc.KeyFile) {
				fmt.Printf("  [!] silo %s BYO certificate problem: %v\n", containerName, err)
				fmt.Printf("      fix the files, or switch cert material with: pg addon install silo --name %s --tls-cert ... --tls-key ... --force\n", sc.Name)
			} else {
				fmt.Printf("  [!] silo %s TLS cert refresh skipped: %v\n", containerName, err)
			}
		}
	}

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] silo %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] silo %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] silo %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper silo container: %w", err)
		}
	}

	if err := m.createContainer(sc); err != nil {
		return err
	}
	fmt.Printf("  [OK] silo %s started\n", containerName)
	return nil
}

// createContainer runs silo: a single-node server over the bind-mounted data
// dir when neither sc.Endpoints nor sc.Drives is set, SNMD (`server /data1
// ..dataN`) when Drives is set, and a distributed cluster when sc.Endpoints is
// non-empty — with Drives also set that is MNMD: this node's drives mount at
// their own /dataN slots while the argv carries the shared host×drive endpoint
// matrix (see config.SiloConfig.Endpoints).
// Linux: host networking, bound to sc.Listen:APIPort (S3 API) and
// sc.Listen:ConsolePort (web console). macOS: the same ports on the pgcli-net
// bridge, published -p N:N so the Mac reaches them on 127.0.0.1. Credentials
// are passed as MINIO_ROOT_USER/PASSWORD env — the contract silo inherits from
// MinIO. The explicit --entrypoint silo bypasses the image's
// docker-entrypoint.sh so pgcli drives the binary directly, exactly as it does
// for MinIO.
func (m *SiloManager) createContainer(sc *config.SiloConfig) error {
	driveDirs := m.resolveDriveDirs(sc)
	for _, d := range driveDirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating silo data dir %s: %w", d, err)
		}
	}

	bind := proxyBindHost(m.bridge, sc.Listen)

	// TLS: silo serves HTTPS natively from --certs-dir (public.crt +
	// private.key) — the same contract as MinIO. pgBackRest refuses plaintext
	// HTTP for S3 repos, so a store feeding Patroni archive-push must be TLS;
	// pgcli's own CA (tlsca) covers every address clients dial and is
	// distributed as repo*-s3-ca-file. BYO mode (cert_file/key_file) validates
	// the operator's pair and mounts it at the two file names silo wants,
	// skipping tlsca entirely.
	if sc.TLS {
		caPath, err := m.EnsureTLS(sc)
		if err != nil {
			return err
		}
		if BYOTLS(sc.TLS, sc.CertFile, sc.KeyFile) {
			ci, err := ValidateBYOCert(sc.CertFile, sc.KeyFile)
			if err != nil {
				return err
			}
			fmt.Printf("  [OK] TLS certs (BYO: %s, valid %s → %s)\n", sc.CertFile,
				ci.NotBefore.Format("2006-01-02"), ci.NotAfter.Format("2006-01-02"))
			if !ci.SelfSigned {
				fmt.Printf("         issued by %q — clients chained to that CA need no --s3-ca-file\n", ci.Issuer)
			} else {
				fmt.Printf("         self-signed: clients pin the issuing CA (pg backup setup --s3-ca-file <ca.pem>)\n")
			}
		} else {
			fmt.Printf("  [OK] TLS certs (CA: %s)\n", caPath)
			fmt.Printf("         as pgBackRest repo CA: pg backup setup --s3-endpoint <host:port> --s3-ca-file %s\n", caPath)
			fmt.Printf("         from another host, pull it over TLS: pg backup fetch-ca <this-host>:%d\n", sc.APIPort)
		}
	}

	// silo inherits MinIO's deployment guidance: nofile=1048576. The podman
	// machine VM caps RLIMIT_NOFILE at its host's hard limit, and crun refuses
	// soft > hard outright, so macOS raises a lower, comfortably-allowed value:
	// plenty for single-host dev/test. The Linux path keeps the recommendation.
	nofile := "1048576"
	if m.bridge {
		nofile = "65536"
	}

	// --http-proxy=false: podman would otherwise inject the host's HTTP(S)_PROXY
	// and the process could honor it for outbound connections. Image pulls run
	// client-side and keep the proxy.
	args := []string{
		"run", "-d",
		"--name", sc.ContainerName,
	}
	args = append(args, netFlags(m.bridge, m.cfg.Podman.Network, sc.APIPort, sc.ConsolePort)...)
	// Data layout + server argv follow the one mode rule (endpoints+drives=MNMD,
	// endpoints=MNSD, drives=SNMD, else SNSD), shared verbatim with minio via
	// storeMountsAndServerArgv.
	driveMounts, serverArgs := storeMountsAndServerArgv(driveDirs, sc.Endpoints, len(sc.Drives) > 0)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--ulimit", "nofile="+nofile+":"+nofile,
		"--stop-timeout", "60",
		"--entrypoint", "silo",
	)
	args = append(args, driveMounts...)
	args = append(args,
		"-e", "MINIO_ROOT_USER="+sc.RootUser,
		"-e", "MINIO_ROOT_PASSWORD="+sc.RootPassword,
	)
	if sc.TLS {
		args = append(args, tlsMountFlags(sc.TLS, sc.CertFile, sc.KeyFile, m.TLSDir(sc), siloCertsDir)...)
	}
	// MINIO_SERVER_URL must be byte-identical on every node or silo refuses to
	// form the cluster (the same Mismatching-environment check MinIO runs).
	// Distributed nodes each advertise their own reachable IP, so there is no
	// single value to pin — omit it and let the endpoint list define addresses.
	// Single-node keeps pointing clients at the local URL.
	if len(sc.Endpoints) == 0 {
		args = append(args, "-e", "MINIO_SERVER_URL="+m.serverURL(sc))
	}
	args = append(args, sc.ImageTag)
	args = append(args, serverArgs...)
	args = append(args,
		"--address", fmt.Sprintf("%s:%d", bind, sc.APIPort),
		"--console-address", fmt.Sprintf("%s:%d", bind, sc.ConsolePort),
	)
	if sc.TLS {
		args = append(args, "--certs-dir", siloCertsDir)
	}

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating silo container: %w", err)
	}
	return nil
}

// serverURL returns the MINIO_SERVER_URL — the address S3 clients are told to
// use. On Linux that is the configured listen + API port. On macOS the store is
// published on the bridge and the Mac reaches it on its own loopback, so the
// URL must say 127.0.0.1, not the container's internal bind.
func (m *SiloManager) serverURL(sc *config.SiloConfig) string {
	host := sc.Listen
	if m.bridge {
		host = "127.0.0.1"
	}
	scheme := "http"
	if sc.TLS {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, sc.APIPort)
}

// Remove stops and removes the silo container. The data dirs are kept by
// default (they are the backup repository); cleanData also deletes them — each
// SNMD drive dir in turn, refusing any that is still mounted (removing through
// a live mount point would destroy data on the underlying device).
func (m *SiloManager) Remove(sc *config.SiloConfig, cleanData bool) error {
	containerName := sc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing silo container: %w", err)
	}
	fmt.Println("  [OK] silo container removed")

	driveDirs := m.resolveDriveDirs(sc)
	if !cleanData {
		for _, d := range driveDirs {
			fmt.Printf("  [OK] Data directory kept: %s\n", d)
		}
		return nil
	}
	singleDefault := sc.DataDir == "" && len(sc.Drives) == 0
	for _, d := range driveDirs {
		if isMountpoint(d) {
			fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", d)
			continue
		}
		if err := os.RemoveAll(d); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] Warning: removing data dir %s: %v\n", d, err)
		} else {
			fmt.Printf("  [OK] Data directory removed: %s\n", d)
		}
	}
	if singleDefault {
		// Default single-dir layout: also prune the <name> dir when it became
		// empty. With an override (or SNMD drives) the parent belongs to the
		// user — never touch it.
		os.Remove(filepath.Dir(driveDirs[0])) // ignore error — non-empty dir won't be removed
	}

	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *SiloManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists
// (running or not) — the install path uses it to skip recreation of a live
// instance.
func (m *SiloManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a silo container.
func (m *SiloManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *SiloManager) run(args ...string) (string, error) {
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

func (m *SiloManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// imageExists reports whether the given image tag is present locally.
func (m *SiloManager) imageExists(tag string) (bool, error) {
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

func (m *SiloManager) containerExists(name string) (bool, error) {
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

func (m *SiloManager) containerRunning(name string) (bool, error) {
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
