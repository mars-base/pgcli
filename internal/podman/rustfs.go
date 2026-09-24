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

// rustfsTLSSrcDir is the read-only bind-mount point pgcli uses to expose cert
// material (BYO files or the generated tlsca dir) to the wrapper entrypoint.
// The wrapper copies what it needs from here into rustfsCertsDir (a fresh
// container-local dir it owns) — it never reads or mutates rustfsTLSSrcDir as
// rustfs itself.
const rustfsTLSSrcDir = "/opt/rustfs/certs-src"

// rustfsCertsDir is where rustfs actually looks for TLS material, set via
// RUSTFS_TLS_PATH. The wrapper entrypoint (see embed/rustfs-entrypoint.sh)
// creates and owns this dir (uid 10001) and copies rustfs_cert.pem / rustfs_key.pem
// into it from rustfsTLSSrcDir before su-dropping. Keeping it separate from the
// source mount means pgcli can mount the source read-only and never has to
// re-own any host file — its cert dir stays pgcli-owned and traversable.
const rustfsCertsDir = "/opt/rustfs/certs"

// RustfsManager manages standalone rustfs containers (https://rustfs.com) — a
// Rust object store that is S3-compatible on the wire but shares NONE of
// MinIO's runtime contract, which is why it is a sibling manager rather than a
// flag on MinioManager/SiloManager. Its config shape is a field-for-field twin,
// but three runtime facts diverge:
//
//  1. Fixed non-root container user — solved inside the image. Upstream rustfs
//     runs as uid/gid 10001 (the `rustfs` account), not configurable. MinIO/silo
//     enter as root and drop privileges themselves, so pgcli never chowns their
//     data dirs; making a bind-mounted dir owned by a fixed foreign uid from the
//     host is exactly what rootless podman can't do cleanly. pgcli instead runs
//     its own wrapper image (config.DefaultRustfsImageTag) whose entrypoint
//     starts as container-root, chowns the bind-mounted dirs to 10001, then
//     `su`-drops to rustfs and execs the unmodified upstream /entrypoint.sh. So
//     pgcli passes no --user flag and does NO host-side ownership work — the
//     container owns its dirs.
//
//  2. Config is env-only, argv is derived by the image. rustfs is driven
//     entirely through RUSTFS_* env — there is NO `server /data ...` argv from
//     pgcli. Critically, pgcli does NOT pass `--entrypoint rustfs` (as silo
//     does): the image's /entrypoint.sh is what expands the RUSTFS_VOLUMES brace
//     ranges (e.g. /data/rustfs{0...3}), mkdirs each drive, and appends the
//     resulting local paths as the real argv. Bypassing it would drop the
//     single-node multi-drive layout entirely.
//
//  3. Only three topologies, and no multi-node single-drive (MNSD). A node
//     contributes either one /data (SNSD) or N /data/rustfsN drives (SNMD);
//     a cluster (MNMD) is one shared node×drive matrix. rustfs additionally
//     refuses, at startup, drives that share a physical device — a hard FATAL,
//     not the advisory root-device warning MinIO/silo emit.
//
// pgcli ships its own thin wrapper image over upstream rustfs (see §1) that
// solves the uid-10001 ownership problem in-container. Like silo/etcd the
// runtime path is pull-only — pgcli never builds during install; the wrapper is
// published to ghcr.io/mars-base/pgcli by `make container-build-rustfs`/
// `container-push-rustfs` and referenced by config.DefaultRustfsImageTag.
type RustfsManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

// NewRustfsManager creates a RustfsManager. It mirrors NewSiloManager: Linux
// serves over host networking, macOS over the pgcli-net bridge with each port
// published. The rustfs image is multi-arch, so any host architecture works.
func NewRustfsManager(cfg *config.Config) (*RustfsManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &RustfsManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/rustfs/<name>/data. It outlives the
// container — `pg addon remove` keeps it unless --clean-data.
func (m *RustfsManager) resolveDataDir(rc *config.RustfsConfig) string {
	if rc.DataDir != "" {
		return rc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "rustfs", rc.Name, "data")
}

// resolveDriveDirs returns the host dirs backing this instance's rustfs drives:
// the configured Drives verbatim when non-empty (SNMD, or one node of an MNMD
// cluster where the endpoints carry the shared node×drive matrix), otherwise the
// single resolveDataDir (SNSD). There is no MNSD for rustfs, so an empty Drives
// list always collapses to the one-dir case.
func (m *RustfsManager) resolveDriveDirs(rc *config.RustfsConfig) []string {
	if len(rc.Drives) > 0 {
		return rc.Drives
	}
	return []string{m.resolveDataDir(rc)}
}

// Drives returns the resolved host drive dirs (for display).
func (m *RustfsManager) Drives(rc *config.RustfsConfig) []string {
	return m.resolveDriveDirs(rc)
}

// DataDir returns the resolved host data directory (for display).
func (m *RustfsManager) DataDir(rc *config.RustfsConfig) string {
	return m.resolveDataDir(rc)
}

// TLSDir is the host dir holding this addon's self-signed CA + leaf cert when
// rc.TLS is on. tlsca writes MinIO's names (public.crt/private.key) there;
// rustfsTLSMountFlags additionally bind those at the names rustfs requires
// (rustfs_cert.pem/rustfs_key.pem) inside the container. The CA pair sits
// alongside for distribution (pgBackRest's repo*-s3-ca-file).
func (m *RustfsManager) TLSDir(rc *config.RustfsConfig) string {
	return filepath.Join(m.dataDir, "tls", "rustfs", rc.Name)
}

// EnsureTLS (re)generates the addon's cert material when needed and returns the
// CA cert path. Mirrors SiloManager.EnsureTLS. In BYO mode it never generates —
// it validates the user's pair instead and returns an empty CA path.
func (m *RustfsManager) EnsureTLS(rc *config.RustfsConfig) (string, error) {
	if rc.TLS && (rc.CertFile != "") != (rc.KeyFile != "") {
		fmt.Printf("  [!] rustfs %s has only one of cert_file/key_file set — ignoring the pair and using generated certs\n", rc.ContainerName)
	}
	if BYOTLS(rc.TLS, rc.CertFile, rc.KeyFile) {
		if _, err := ValidateBYOCert(rc.CertFile, rc.KeyFile); err != nil {
			return "", err
		}
		return "", nil
	}
	hosts := tlsca.LocalHosts(rc.Listen)
	if u := m.serverURL(rc); u != "" {
		if h, _, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")); err == nil {
			hosts = append(hosts, h)
		}
	}
	caPath, err := tlsca.Generate(m.TLSDir(rc), hosts, 30*24*time.Hour)
	if err != nil {
		return "", err
	}
	// rustfs reads rustfs_cert.pem/rustfs_key.pem, not tlsca's public.crt/
	// private.key. Place rustfs-named copies beside them so the single dir
	// mount satisfies rustfs while the canonical names stay for clients and
	// `pg cert`/`pg backup fetch-ca`. Re-run on every generate so a re-signed
	// leaf propagates to the rustfs pair.
	if err := rustfsSyncGeneratedCerts(m.TLSDir(rc)); err != nil {
		return "", err
	}
	return caPath, nil
}

// DataDirSharesRootDevice reports whether the resolved data dir sits on the same
// block device as the host root filesystem. Advisory only for rustfs: unlike
// MinIO's "part of root drive" refusal it does not gate the single-node case,
// and the multi-drive case is caught far more precisely at container startup
// (rustfs refuses drives sharing a physical device). Kept for symmetry with the
// other stores so the CLI can print the same hint.
func (m *RustfsManager) DataDirSharesRootDevice(rc *config.RustfsConfig) (bool, error) {
	return sharesRootDevice(m.resolveDataDir(rc))
}

// DrivesSharingRootDevice returns the subset of drive dirs on the host root
// device. Advisory (see DataDirSharesRootDevice); rustfs's own distinct-device
// check is the authoritative gate and runs at container start.
func (m *RustfsManager) DrivesSharingRootDevice(rc *config.RustfsConfig) []string {
	var bad []string
	for _, d := range m.resolveDriveDirs(rc) {
		if shared, err := sharesRootDevice(d); err == nil && shared {
			bad = append(bad, d)
		}
	}
	return bad
}

// EnsureImage pulls the rustfs image if it is not present locally (pull-only).
func (m *RustfsManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling rustfs image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the rustfs container (install semantics:
// stop/rm/recreate so updated ports/credentials take effect). Data dirs are
// created if missing; never deleted here.
func (m *RustfsManager) EnsureContainer(rc *config.RustfsConfig) error {
	containerName := rc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> rustfs container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping rustfs container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing rustfs container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale rustfs container: %w", err)
			}
		}
	}

	if err := m.createContainer(rc); err != nil {
		return err
	}
	fmt.Println("  [OK] rustfs container started")
	return nil
}

// StartContainer starts the rustfs container without touching config
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh. Best-effort TLS
// cert refresh, exactly as silo.
func (m *RustfsManager) StartContainer(rc *config.RustfsConfig) error {
	containerName := rc.ContainerName

	if rc.TLS {
		if _, err := m.EnsureTLS(rc); err != nil {
			if BYOTLS(rc.TLS, rc.CertFile, rc.KeyFile) {
				fmt.Printf("  [!] rustfs %s BYO certificate problem: %v\n", containerName, err)
				fmt.Printf("      fix the files, or switch cert material with: pg addon install rustfs --name %s --tls-cert ... --tls-key ... --force\n", rc.Name)
			} else {
				fmt.Printf("  [!] rustfs %s TLS cert refresh skipped: %v\n", containerName, err)
			}
		}
	}

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] rustfs %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] rustfs %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] rustfs %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper rustfs container: %w", err)
		}
	}

	if err := m.createContainer(rc); err != nil {
		return err
	}
	fmt.Printf("  [OK] rustfs %s started\n", containerName)
	return nil
}

// createContainer runs rustfs. The data layout follows one mode rule (see the
// type doc): SNSD mounts the single data dir at /data, SNMD/MNMD mount each
// drive at /data/rustfsN; in every case the volume set is expressed as a single
// RUSTFS_VOLUMES env and the argv is left to the image's /entrypoint.sh, which
// expands brace ranges, mkdirs drives, and appends the local paths. No
// --entrypoint override: rustfs needs its own wrapper, unlike silo.
//
// Credentials become RUSTFS_ACCESS_KEY/RUSTFS_SECRET_KEY; bind/ports become
// RUSTFS_ADDRESS/RUSTFS_CONSOLE_ADDRESS (bind:port, not MinIO's --address
// flag). TLS uses RUSTFS_TLS_PATH (env), not MinIO's --certs-dir flag, with the
// cert files named rustfs_cert.pem/rustfs_key.pem. Ownership of the bind-mounted
// dirs is NOT handled here: pgcli's wrapper image (see §1) chowns them to uid
// 10001 from inside the container before dropping to rustfs, so there is no
// host-side chown and no --user flag.
func (m *RustfsManager) createContainer(rc *config.RustfsConfig) error {
	driveDirs := m.resolveDriveDirs(rc)
	for _, d := range driveDirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating rustfs data dir %s: %w", d, err)
		}
	}

	bind := proxyBindHost(m.bridge, rc.Listen)

	// TLS: source material is mounted read-only at rustfsTLSSrcDir; the wrapper
	// entrypoint copies it into RUSTFS_TLS_PATH (a container-local 10001-owned
	// dir) before su-dropping to rustfs. pgcli's cert dir / operator's BYO files
	// are never re-owned or written to by the container.
	var tlsEnv []string
	if rc.TLS {
		caPath, err := m.EnsureTLS(rc)
		if err != nil {
			return err
		}
		tlsEnv = []string{"-e", "RUSTFS_TLS_PATH=" + rustfsCertsDir}
		if BYOTLS(rc.TLS, rc.CertFile, rc.KeyFile) {
			ci, err := ValidateBYOCert(rc.CertFile, rc.KeyFile)
			if err != nil {
				return err
			}
			fmt.Printf("  [OK] TLS certs (BYO: %s, valid %s → %s)\n", rc.CertFile,
				ci.NotBefore.Format("2006-01-02"), ci.NotAfter.Format("2006-01-02"))
			if !ci.SelfSigned {
				fmt.Printf("         issued by %q — clients chained to that CA need no --s3-ca-file\n", ci.Issuer)
			} else {
				fmt.Printf("         self-signed: clients pin the issuing CA (pg backup setup --s3-ca-file <ca.pem>)\n")
			}
		} else {
			fmt.Printf("  [OK] TLS certs (CA: %s)\n", caPath)
			fmt.Printf("         as pgBackRest repo CA: pg backup setup --s3-endpoint <host:port> --s3-ca-file %s\n", caPath)
			fmt.Printf("         from another host, pull it over TLS: pg backup fetch-ca <this-host>:%d\n", rc.APIPort)
		}
	}

	// nofile: rustfs inherits MinIO's deployment guidance. macOS raises a lower
	// value because the podman-machine VM caps RLIMIT_NOFILE at the host hard
	// limit and crun refuses soft > hard.
	nofile := "1048576"
	if m.bridge {
		nofile = "65536"
	}

	args := []string{
		"run", "-d",
		"--name", rc.ContainerName,
	}
	args = append(args, netFlags(m.bridge, m.cfg.Podman.Network, rc.APIPort, rc.ConsolePort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--ulimit", "nofile="+nofile+":"+nofile,
		"--stop-timeout", "60",
	)
	args = append(args, rustfsMountFlags(driveDirs, rc.Endpoints, len(rc.Drives) > 0)...)
	args = append(args,
		"-e", "RUSTFS_ACCESS_KEY="+rc.RootUser,
		"-e", "RUSTFS_SECRET_KEY="+rc.RootPassword,
		"-e", "RUSTFS_ADDRESS="+fmt.Sprintf("%s:%d", bind, rc.APIPort),
		"-e", "RUSTFS_CONSOLE_ADDRESS="+fmt.Sprintf("%s:%d", bind, rc.ConsolePort),
		"-e", "RUSTFS_CONSOLE_ENABLE=true",
		// Empty log directory forces rustfs to log to stdout (the image default
		// /logs would write files to the container fs, invisible to
		// `pg logs addon rustfs`). This matches how minio/silo surface logs.
		"-e", "RUSTFS_OBS_LOG_DIRECTORY=",
		"-e", "RUSTFS_VOLUMES="+rustfsVolumesEnv(rc.Endpoints, len(rc.Drives) > 0, len(driveDirs)),
	)
	args = append(args, tlsEnv...)
	if rc.TLS {
		args = append(args, rustfsTLSMountFlags(rc.CertFile, rc.KeyFile, m.TLSDir(rc))...)
	}
	// No positional argv and no --entrypoint override: /entrypoint.sh derives
	// both from RUSTFS_VOLUMES. The image tag is the final argument.
	args = append(args, rc.ImageTag)

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating rustfs container: %w", err)
	}
	return nil
}

// serverURL returns the address S3 clients dial — listen + API port on Linux,
// 127.0.0.1 on macOS (the bridge publishes to the Mac's loopback). rustfs has no
// MINIO_SERVER_URL analog to advertise; this URL is for display and TLS SANs.
func (m *RustfsManager) serverURL(rc *config.RustfsConfig) string {
	host := rc.Listen
	if m.bridge {
		host = "127.0.0.1"
	}
	scheme := "http"
	if rc.TLS {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, rc.APIPort)
}

// Remove stops and removes the rustfs container. The data dirs are kept by
// default; cleanData also deletes them — each drive dir in turn, refusing any
// still mounted. rustfs writes as uid 10001, so on a rootless host the leftover
// files are not deletable by os.RemoveAll: Remove routes through removeHostDir,
// whose `podman unshare rm` fallback reclaims them (verified: plain rm hits
// EACCES, unshare rm succeeds).
func (m *RustfsManager) Remove(rc *config.RustfsConfig, cleanData bool) error {
	containerName := rc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing rustfs container: %w", err)
	}
	fmt.Println("  [OK] rustfs container removed")

	driveDirs := m.resolveDriveDirs(rc)
	if !cleanData {
		for _, d := range driveDirs {
			fmt.Printf("  [OK] Data directory kept: %s\n", d)
		}
		return nil
	}
	singleDefault := rc.DataDir == "" && len(rc.Drives) == 0
	for _, d := range driveDirs {
		if isMountpoint(d) {
			fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", d)
			continue
		}
		if err := removeHostDir(m.podman, rc.ImageTag, d); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] Warning: removing data dir %s: %v\n", d, err)
		} else {
			fmt.Printf("  [OK] Data directory removed: %s\n", d)
		}
	}
	if singleDefault {
		// Default single-dir layout: also prune the <name> dir when empty. With
		// an override (or drives) the parent belongs to the user — never touch.
		_, _ = removeHostDirIfEmpty(m.podman, rc.ImageTag, filepath.Dir(driveDirs[0]))
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *RustfsManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists.
func (m *RustfsManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a rustfs container.
func (m *RustfsManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *RustfsManager) run(args ...string) (string, error) {
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

func (m *RustfsManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *RustfsManager) imageExists(tag string) (bool, error) {
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

func (m *RustfsManager) containerExists(name string) (bool, error) {
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

func (m *RustfsManager) containerRunning(name string) (bool, error) {
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
