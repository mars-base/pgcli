package podman

import (
	"encoding/json"
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

// SeaweedfsManager manages standalone SeaweedFS containers
// (https://github.com/seaweedfs/seaweedfs) — an Apache-2.0 object store whose
// volume-level disk management (O(1) small-file reads, placement codes instead
// of node replicas) is complementary to the existing garage/minio/silo/rustfs
// addons. It is a sibling manager because the runtime contract diverges on
// four points (all verified live on vm01 with the default 4.48_large_disk
// image):
//
//  1. One process, many roles. Single-node runs `weed mini` (master+volume+
//     filer+s3 in one process — the officially endorsed single-binary server);
//     a cluster member runs `weed server -filer -s3`. pgcli ALWAYS bypasses the
//     official entrypoint with --entrypoint /usr/bin/weed: entrypoint.sh chowns
//     -R /data and su-execs to uid 1000 — a silent-slow hazard on big dirs and
//     pointless for us (weed runs fine as the container root).
//  2. argv-only config. Roles take flags; the S3 credentials come from a
//     rendered s3.json (4.48 protojson schema: identities[].credentials is an
//     ARRAY, no buckets field, bare "Admin" action = full access), filer
//     metadata from a rendered filer.toml ([postgres2]) when an external
//     PostgreSQL is configured — per-field keys, there is NO dsn key.
//  3. Ports: four consecutive HTTP ports from their own pool
//     (seaweedfs_start_port, default 8400) = master/volume/filer/s3, plus an
//     IMPLICIT gRPC twin per role at +10000 (verified: 18400-18403 alongside
//     8400-8403). The pool reserves the twins so a second instance never
//     walks into them.
//  4. The -ip trap (measured, not folklore): passing only -ip.bind makes the
//     volume register its auto-detected host NIC IP with the master, and the
//     filer/S3 internal data path then dials that address — connections
//     REFUSED on uploads while `mc mb`/`mc ls` still work and the container
//     looks healthy. -ip MUST always be passed: single-node -ip=<Listen>,
//     cluster -ip=<AdvertiseHost>.
//
// Hygiene flags pgcli always appends (both topologies): -logtostderr=true (glog
// otherwise writes files inside the container and `pg logs` shows nothing),
// -master.telemetry=false, -s3.port.iceberg=0, -s3.port.lance=0,
// -webdav=false. Single-node (`mini`) additionally closes -admin.ui=false: that
// flag is mini-only (server rejects an undefined flag with exit 2) and squats
// the default admin port 23646.
//
// Native S3 TLS is supported (verified): --tls generates a self-signed pair via
// tlsca, --tls-cert/--tls-key bring your own (pg cert output drops in
// directly). The S3 port becomes HTTPS and the cleartext listener is turned
// OFF entirely (-s3.port=0, measured: connection refused). The cluster topology
// is minio-MNMD-shaped: the SAME install command copied to every node (only
// --advertise-host differs, the full -master.peers list lands on every member)
// — there is no bootstrap/join role distinction, and Raft quorum absence on
// the first nodes is normal waiting state, not failure.
type SeaweedfsManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

// NewSeaweedfsManager creates a SeaweedfsManager, mirroring
// NewGarageManager: Linux serves over host networking, macOS over the
// pgcli-net bridge with the four ports published. The image is multi-arch.
func NewSeaweedfsManager(cfg *config.Config) (*SeaweedfsManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &SeaweedfsManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// --- host paths -----------------------------------------------------------

// dir returns the instance dir <base>/addon/seaweedfs/<name> (or the parent
// of an explicit DataDir override, so rendered files sit beside it).
func (m *SeaweedfsManager) dir(sc *config.SeaweedfsConfig) string {
	if sc.DataDir != "" {
		return filepath.Dir(sc.DataDir)
	}
	return filepath.Join(m.dataDir, "addon", "seaweedfs", sc.Name)
}

// resolveDataDir returns the host dir behind the container's -dir=/data.
func (m *SeaweedfsManager) resolveDataDir(sc *config.SeaweedfsConfig) string {
	if sc.DataDir != "" {
		return sc.DataDir
	}
	return filepath.Join(m.dir(sc), "data")
}

// resolveDriveDirs returns the host dirs backing this instance's -dir: the
// explicit Drives list when set (multi-disk SNMD — one dir per disk), else the
// single resolved DataDir. Mirrors MinioManager.resolveDriveDirs.
func (m *SeaweedfsManager) resolveDriveDirs(sc *config.SeaweedfsConfig) []string {
	if len(sc.Drives) > 0 {
		return sc.Drives
	}
	return []string{m.resolveDataDir(sc)}
}

// containerDataDirs is the in-container path for each resolved drive: the
// single-dir case keeps the constant /data; the multi-drive case mounts drive i
// at /data(i+1), matching how -dir is comma-joined in buildWeedArgs. Depends
// only on the drive COUNT (from sc.Drives), not the host paths, so it stays a
// pure package-level helper like buildWeedArgs itself.
func containerDataDirs(sc *config.SeaweedfsConfig) []string {
	n := len(sc.Drives)
	if n == 0 {
		return []string{seaweedContainerDataDir}
	}
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", seaweedContainerDataDir, i+1)
	}
	return out
}

// DrivesSharingRootDevice returns the subset of this instance's drive dirs that
// sit on the host's root device — advisory, same posture as MinIO/Silo's SNMD.
// A multi-disk volume server is meant to spread data across SEPARATELY mounted
// disks; pointing a drive at a plain dir on / works but defeats that, so the
// install prints a per-drive warning. Unlike weed's own -ip check this is not
// a correctness issue (weed never refuses a same-device dir), hence never fatal.
func (m *SeaweedfsManager) DrivesSharingRootDevice(sc *config.SeaweedfsConfig) []string {
	var bad []string
	for _, d := range m.resolveDriveDirs(sc) {
		if shared, err := sharesRootDevice(d); err == nil && shared {
			bad = append(bad, d)
		}
	}
	return bad
}

// DataDir returns the resolved host data directory (for display).
func (m *SeaweedfsManager) DataDir(sc *config.SeaweedfsConfig) string {
	return m.resolveDataDir(sc)
}

// Drives returns the resolved host drive dirs (for display; includes the
// single-data-dir case, so it is the general accessor callers print).
func (m *SeaweedfsManager) Drives(sc *config.SeaweedfsConfig) []string {
	return m.resolveDriveDirs(sc)
}

func (m *SeaweedfsManager) s3ConfigPath(sc *config.SeaweedfsConfig) string {
	return filepath.Join(m.dir(sc), "s3.json")
}

func (m *SeaweedfsManager) filerConfigPath(sc *config.SeaweedfsConfig) string {
	return filepath.Join(m.dir(sc), "filer.toml")
}

// TLSDir is the host dir holding this addon's self-signed CA + leaf when TLS
// is on (tlsca's own names: public.crt/private.key/ca.crt, which `pg cert`
// and `pg backup fetch-ca` consumers reference). createContainer mounts the
// pair at the cert.pem/key.pem names the weed flags take.
func (m *SeaweedfsManager) TLSDir(sc *config.SeaweedfsConfig) string {
	return filepath.Join(m.dir(sc), "tls")
}

// --- rendered config files --------------------------------------------------

// WriteS3Config renders and writes s3.json 0600 (it embeds the secret key) and
// returns the host path. Written on both the install and the start-recreate
// path so the mounted file always matches the config pgcli holds.
func (m *SeaweedfsManager) WriteS3Config(sc *config.SeaweedfsConfig) (string, error) {
	content, err := renderSeaweedS3JSON(sc)
	if err != nil {
		return "", err
	}
	path := m.s3ConfigPath(sc)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("creating seaweedfs instance dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("writing seaweedfs s3.json: %w", err)
	}
	return path, nil
}

// WriteFilerConfig renders filer.toml ([postgres2] → the FilerDSN's
// PostgreSQL) 0600 and returns the host path. Called whenever FilerDSN is set;
// without it the filer keeps its metadata inside DataDir (leveldb2) and no
// file is rendered.
func (m *SeaweedfsManager) WriteFilerConfig(sc *config.SeaweedfsConfig) (string, error) {
	host, port, user, password, database, err := ParseDSN(sc.FilerDSN)
	if err != nil {
		return "", fmt.Errorf("seaweedfs filer dsn: %w", err)
	}
	path := m.filerConfigPath(sc)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("creating seaweedfs instance dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderSeaweedFilerTOML(host, port, user, password, database)), 0600); err != nil {
		return "", fmt.Errorf("writing seaweedfs filer.toml: %w", err)
	}
	return path, nil
}

// --- TLS ------------------------------------------------------------------

// hostnames returns every name/SAN the leaf cert must cover: the bind host,
// the advertise host (cluster), loopback pair, and the host part of the S3
// URL clients dial.
func (m *SeaweedfsManager) hostnames(sc *config.SeaweedfsConfig) []string {
	hosts := []string{sc.Listen}
	if sc.AdvertiseHost != "" {
		hosts = append(hosts, sc.AdvertiseHost)
	}
	if h, _, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(m.S3URL(sc), "https://"), "http://")); err == nil {
		hosts = append(hosts, h)
	}
	return hosts
}

// EnsureTLS resolves the leaf/key host paths the container will mount: BYO
// pair validated (never generated over), else tlsca.Generate into TLSDir
// (idempotent — renews only near expiry), returning tlsca's canonical names.
// Mirrors MinioManager/RustfsManager.EnsureTLS semantics.
func (m *SeaweedfsManager) EnsureTLS(sc *config.SeaweedfsConfig) (certPath, keyPath, caPath string, err error) {
	if (sc.CertFile != "") != (sc.KeyFile != "") {
		fmt.Printf("  [!] seaweedfs %s has only one of cert_file/key_file set — using generated certs\n", sc.ContainerName)
	}
	if sc.CertFile != "" && sc.KeyFile != "" {
		if _, err := ValidateBYOCert(sc.CertFile, sc.KeyFile); err != nil {
			return "", "", "", err
		}
		return sc.CertFile, sc.KeyFile, "", nil
	}
	caPath, err = tlsca.Generate(m.TLSDir(sc), tlsca.LocalHosts(m.hostnames(sc)...), 30*24*time.Hour)
	if err != nil {
		return "", "", "", err
	}
	return filepath.Join(m.TLSDir(sc), tlsca.ServerCert),
		filepath.Join(m.TLSDir(sc), tlsca.ServerKey), caPath, nil
}

// --- image / container ------------------------------------------------------

// EnsureImage pulls the SeaweedFS image if absent (pull-only; no wrapper
// build — weed runs as container root happily once the entrypoint is bypassed).
func (m *SeaweedfsManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling seaweedfs image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the container (install semantics:
// stop/rm/recreate so updated ports/credentials/flags take effect).
func (m *SeaweedfsManager) EnsureContainer(sc *config.SeaweedfsConfig) error {
	containerName := sc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> seaweedfs container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping seaweedfs container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing seaweedfs container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale seaweedfs container: %w", err)
			}
		}
	}

	if err := m.createContainer(sc); err != nil {
		return err
	}
	fmt.Println("  [OK] seaweedfs container started")
	return nil
}

// StartContainer starts the container without recreating it (autostart path):
// running → no-op; exists but stopped → podman start, recreating on improper
// state; missing → create fresh. Rendered config files refresh with every
// recreate; TLS is best-effort refreshed first (BYO problems are loud).
func (m *SeaweedfsManager) StartContainer(sc *config.SeaweedfsConfig) error {
	containerName := sc.ContainerName

	if sc.TLSOn() {
		if _, _, _, err := m.EnsureTLS(sc); err != nil {
			if sc.CertFile != "" && sc.KeyFile != "" {
				fmt.Printf("  [!] seaweedfs %s BYO certificate problem: %v\n", containerName, err)
				fmt.Printf("      fix the files, or switch cert material with: pg addon install seaweedfs --name %s --tls-cert ... --tls-key ... --force\n", sc.Name)
			} else {
				fmt.Printf("  [!] seaweedfs %s TLS cert refresh skipped: %v\n", containerName, err)
			}
		}
	}

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] seaweedfs %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] seaweedfs %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] seaweedfs %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper seaweedfs container: %w", err)
		}
	}

	if err := m.createContainer(sc); err != nil {
		return err
	}
	fmt.Printf("  [OK] seaweedfs %s started\n", containerName)
	return nil
}

// buildWeedArgs assembles the full weed argv for both topologies. ORDER
// MATTERS: weed uses Go's stdlib flag parser, where the first non-flag word is
// the subcommand and only the process-global logging flags may precede it.
// Every -dir/-ip/-ip.bind/-master.*/-volume.*/-filer.*/-s3.* flag is a
// SUBCOMMAND flag and must come after the mini/server word — putting them
// before it makes weed print its usage block and exit 2 (verified on vm01).
// So the shape is global, subcommand word, then role args:
//
//	single:  -logtostderr=true mini  <common> -bucket=<B>
//	cluster: -logtostderr=true server <common> -master.peers=<full list> -master.defaultReplication=<code> -filer -s3
//
// TLS swaps -s3.port=P for -s3.port=0 -s3.port.https=P plus the cert/key flags
// (0 = cleartext listener not bound at all, measured). certPath/keyPath are the
// CONTAINER paths (or empty when TLS is off).
func buildWeedArgs(sc *config.SeaweedfsConfig, bind, certPath, keyPath string) []string {
	advertise := sc.Listen
	if sc.Clustered() {
		advertise = sc.AdvertiseHost
	}

	// common = the role flags both topologies pass, placed after the
	// subcommand word. -dir is a comma list when multiple drives are mounted
	// (weed treats each entry as a separate disk — verified: the volume server
	// reports every dir in its own DiskStatuses entry).
	common := []string{
		"-dir=" + strings.Join(containerDataDirs(sc), ","),
		"-ip=" + advertise,
		"-ip.bind=" + bind,
		fmt.Sprintf("-master.port=%d", sc.Port),
		fmt.Sprintf("-volume.port=%d", sc.VolumePort),
		fmt.Sprintf("-filer.port=%d", sc.FilerPort),
		fmt.Sprintf("-master.volumeSizeLimitMB=%d", sc.VolumeSizeMB),
		"-s3.config=" + seaweedContainerS3Config,
		// hygiene: no telemetry, no extra listeners pgcli doesn't use
		"-master.telemetry=false",
		"-s3.port.iceberg=0",
		"-s3.port.lance=0",
		"-webdav=false",
	}
	if sc.TLSOn() && certPath != "" {
		common = append(common,
			"-s3.port=0",
			fmt.Sprintf("-s3.port.https=%d", sc.S3Port),
			"-s3.cert.file="+certPath,
			"-s3.key.file="+keyPath,
		)
	} else {
		common = append(common, fmt.Sprintf("-s3.port=%d", sc.S3Port))
	}

	args := []string{"-logtostderr=true"}
	if sc.Clustered() {
		args = append(args, "server")
		args = append(args, common...)
		return append(args,
			"-master.peers="+SeaweedfsPeersFlag(sc),
			"-master.defaultReplication="+sc.Replication,
			"-filer", "-s3",
		)
	}
	bucket := sc.Bucket
	if bucket == "" {
		bucket = sc.Name
	}
	args = append(args, "mini")
	args = append(args, common...)
	// -admin.ui is mini-only (the standalone admin UI takes its own port); the
	// server command has no such flag, so it lives here, not in common.
	args = append(args, "-admin.ui=false")
	return append(args, "-bucket="+bucket)
}

// createContainer runs the weed process under podman. Layout follows garage's
// shape (netFlags → static flags → mounts → -e before image → argv), but weed
// is argv-only — no env config — so there are no -e flags at all. Drives mount
// at /data1../dataN and -dir lists them comma-separated (single-dir keeps the
// bare /data).
func (m *SeaweedfsManager) createContainer(sc *config.SeaweedfsConfig) error {
	driveDirs := m.resolveDriveDirs(sc)
	for _, d := range append(append([]string{}, driveDirs...), m.dir(sc)) {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating seaweedfs dir %s: %w", d, err)
		}
	}

	s3Path, err := m.WriteS3Config(sc)
	if err != nil {
		return err
	}
	var filerPath string
	if sc.FilerDSN != "" {
		if filerPath, err = m.WriteFilerConfig(sc); err != nil {
			return err
		}
	}

	var hostCert, hostKey, certPath, keyPath string
	if sc.TLSOn() {
		hostCert, hostKey, _, err = m.EnsureTLS(sc)
		if err != nil {
			return err
		}
		certPath, keyPath = seaweedContainerCert, seaweedContainerKey
	}

	bind := proxyBindHost(m.bridge, sc.Listen)
	// A cluster member must reach its peers over the LAN (master gRPC at
	// -ip+10000 carries Raft), so it binds every interface: that covers the
	// AdvertiseHost the peers dial AND loopback, which the health gate, the mc
	// seed, and local S3 clients use. -ip (above) still fixes the REGISTERED
	// address, so this only widens what the socket listens on. Single-node
	// keeps the narrower Listen-derived bind — an unclustered store has no
	// peers to reach and should not silently expose the S3 port on the LAN.
	if sc.Clustered() {
		bind = "0.0.0.0"
	}

	args := []string{
		"run", "-d",
		"--name", sc.ContainerName,
	}
	args = append(args, netFlags(m.bridge, m.cfg.Podman.Network, sc.Port, sc.VolumePort, sc.FilerPort, sc.S3Port)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "60",
		"--entrypoint", "/usr/bin/weed",
	)
	args = append(args, "-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(s3Path), seaweedContainerS3Config))
	containerDirs := containerDataDirs(sc)
	for i, d := range driveDirs {
		args = append(args, "-v", fmt.Sprintf("%s:%s:z", hostMountPath(d), containerDirs[i]))
	}
	if filerPath != "" {
		args = append(args, "-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(filerPath), seaweedContainerFilerCfg))
	}
	if certPath != "" {
		args = append(args,
			"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(hostCert), certPath),
			"-v", fmt.Sprintf("%s:%s:ro,z", hostMountPath(hostKey), keyPath),
		)
	}
	args = append(args, sc.ImageTag)
	args = append(args, buildWeedArgs(sc, bind, certPath, keyPath)...)

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating seaweedfs container: %w", err)
	}
	return nil
}

// --- lifecycle --------------------------------------------------------------

// Stop stops a seaweedfs container.
func (m *SeaweedfsManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// Remove stops and removes the container. cleanData also deletes the data dir
// and every pgcli-rendered file (s3.json/filer.toml/TLS dir), refusing
// anything still mounted; the filer metadata DATABASE is never touched (the
// CLI prints the keep-the-DB notice).
func (m *SeaweedfsManager) Remove(sc *config.SeaweedfsConfig, cleanData bool) error {
	containerName := sc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing seaweedfs container: %w", err)
	}
	fmt.Println("  [OK] seaweedfs container removed")

	type entry struct {
		path  string
		label string
	}
	var entries []entry
	for _, d := range m.resolveDriveDirs(sc) {
		entries = append(entries, entry{d, "data directory"})
	}
	if sc.DataDir == "" {
		// pgcli-rendered files only live under the instance dir when no
		// DataDir override moved it elsewhere.
		entries = append(entries,
			entry{m.s3ConfigPath(sc), "s3.json"},
			entry{m.filerConfigPath(sc), "filer.toml"},
			entry{m.TLSDir(sc), "TLS dir"},
		)
	}
	if !cleanData {
		for _, e := range entries {
			fmt.Printf("  [OK] SeaweedFS %s kept: %s\n", e.label, e.path)
		}
		return nil
	}
	for _, e := range entries {
		if isMountpoint(e.path) {
			fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", e.path)
			continue
		}
		// filer.toml and the TLS dir only exist when that instance actually
		// uses them (a cluster filer store / TLS material) — claim removal
		// only for paths that were really there, never for absent optionals.
		if _, serr := os.Stat(e.path); serr != nil {
			continue
		}
		if err := os.RemoveAll(e.path); err != nil {
			fmt.Printf("  [!] Warning: removing seaweedfs %s %s: %v\n", e.label, e.path, err)
			continue
		}
		fmt.Printf("  [OK] SeaweedFS %s removed: %s\n", e.label, e.path)
	}
	if sc.DataDir == "" {
		// Default layout: also prune the <name> dir when empty. With a DataDir
		// override the parent belongs to the user — never touch.
		parent := m.dir(sc)
		if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
			_ = os.Remove(parent)
		}
	}
	return nil
}

// ContainerRunning / ContainerExists mirror the other addon managers.
func (m *SeaweedfsManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

func (m *SeaweedfsManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// --- exec surface -------------------------------------------------------------

// Exec runs `/usr/bin/weed <args...>` inside the container with stdio
// passthrough (the `pg weed` surface; e.g. `weed shell`).
func (m *SeaweedfsManager) Exec(sc *config.SeaweedfsConfig, args []string) error {
	running, err := m.containerRunning(sc.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("seaweedfs container %s is not running — run 'pg addon start seaweedfs --name %s' first", sc.ContainerName, sc.Name)
	}
	execArgs := []string{"exec", "-i", sc.ContainerName, "/usr/bin/weed"}
	execArgs = append(execArgs, args...)
	return m.runInteractive(execArgs...)
}

// ExecShellTimeout runs an `sh -c` pipeline inside a running member with the
// shared idle-kill capture pipeline — `weed shell` fed via stdin prints its
// results and can then linger on open gRPC channels, exactly like the plain
// weed CLIs, so the same wedge protection applies.
func (m *SeaweedfsManager) ExecShellTimeout(sc *config.SeaweedfsConfig, d time.Duration, script string) (string, error) {
	running, err := m.containerRunning(sc.ContainerName)
	if err != nil {
		return "", err
	}
	if !running {
		return "", fmt.Errorf("seaweedfs container %s is not running — run 'pg addon start seaweedfs --name %s' first", sc.ContainerName, sc.Name)
	}
	execArgs := []string{"exec", sc.ContainerName, "sh", "-c", script}
	return execIdleKilled(m.podman, d, defaultIdleKill, execArgs...)
}

// ExecCaptureTimeout runs `/usr/bin/weed <args...>` inside a running member,
// capturing merged stdout+stderr with the shared idle-kill pipeline — weed
// CLIs that handshake with the cluster can print and then never exit, exactly
// like garage's, so the same wedge protection applies.
func (m *SeaweedfsManager) ExecCaptureTimeout(sc *config.SeaweedfsConfig, d time.Duration, args ...string) (string, error) {
	running, err := m.containerRunning(sc.ContainerName)
	if err != nil {
		return "", err
	}
	if !running {
		return "", fmt.Errorf("seaweedfs container %s is not running — run 'pg addon start seaweedfs --name %s' first", sc.ContainerName, sc.Name)
	}
	execArgs := []string{"exec", sc.ContainerName, "/usr/bin/weed"}
	execArgs = append(execArgs, args...)
	return execIdleKilled(m.podman, d, defaultIdleKill, execArgs...)
}

// --- health ---------------------------------------------------------------

// SeaweedfsClusterStatus is the master's /cluster/status JSON (fixture pinned
// in tests): {"IsLeader":bool,"Leader":"ip:httpport.grpcport"}.
type SeaweedfsClusterStatus struct {
	IsLeader bool   `json:"IsLeader"`
	Leader   string `json:"Leader"`
}

// ParseSeaweedfsClusterStatus decodes the /cluster/status body.
func ParseSeaweedfsClusterStatus(body string) (SeaweedfsClusterStatus, error) {
	var st SeaweedfsClusterStatus
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &st); err != nil {
		return st, fmt.Errorf("parsing seaweedfs cluster status: %w", err)
	}
	return st, nil
}

// clusterStatusExecCap bounds one /cluster/status poll. The container can be in
// a crash-loop (restart policy respawning a flag-rejected weed) where `podman
// exec` attaches but the exec never produces output — an unbounded exec would
// then hang the whole health gate past its own deadline (vm02 caught this: one
// poll wedged 5 minutes). Kept short so the gate still makes its ~30s of polls.
const clusterStatusExecCap = 5 * time.Second

// ClusterStatus polls the master's JSON status endpoint from INSIDE the
// container over loopback (no credentials needed). The health gate treats a
// non-empty Leader as green.
func (m *SeaweedfsManager) ClusterStatus(sc *config.SeaweedfsConfig) (SeaweedfsClusterStatus, error) {
	script := fmt.Sprintf("wget -qO- http://127.0.0.1:%d/cluster/status", sc.Port)
	out, err := execIdleKilled(m.podman, clusterStatusExecCap, defaultIdleKill,
		"exec", sc.ContainerName, "sh", "-c", script)
	if err != nil {
		return SeaweedfsClusterStatus{}, err
	}
	return ParseSeaweedfsClusterStatus(out)
}

// S3URL returns the URL S3 clients dial for display and seeding.
func (m *SeaweedfsManager) S3URL(sc *config.SeaweedfsConfig) string {
	host := sc.Listen
	if m.bridge {
		host = "127.0.0.1"
	}
	scheme := "http"
	if sc.TLSOn() {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, sc.S3Port)
}

// --- internal helpers -------------------------------------------------------

func (m *SeaweedfsManager) run(args ...string) (string, error) {
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

func (m *SeaweedfsManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *SeaweedfsManager) imageExists(tag string) (bool, error) {
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

func (m *SeaweedfsManager) containerExists(name string) (bool, error) {
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

func (m *SeaweedfsManager) containerRunning(name string) (bool, error) {
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
