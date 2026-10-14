package podman

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

// GarageManager manages standalone Garage containers
// (https://garagehq.deuxfleurs.fr) — a Rust, AGPLv3 S3-compatible object store
// built for small self-hosted and geo-distributed clusters, and the successor
// route pgcli recommends now that the open-source MinIO repo is archived. It is
// a sibling of RustfsManager rather than a flag on the MinIO-shaped managers
// because four runtime facts diverge (see config.GarageConfig):
//
//  1. Config is a rendered TOML file. pgcli writes garage.toml under the
//     instance dir and bind-mounts it at /etc/garage.toml (read-only — Garage
//     never rewrites its own config). MinIO/silo/rustfs pass argv or env.
//  2. Two kinds of dir: metadata_dir (LMDB, always one) and data_dir (blocks),
//     each its own bind mount at the container paths in garage_paths.go. Since
//     Garage v0.9.0 data_dir also accepts an array of { path = … } tables — one
//     per disk — so a node can spread its blocks across several drives
//     (Drives); the mount and render adapt, metadata_dir never joins the set.
//  3. Four ports from their own pool (garage_start_port, default 3900): S3 API,
//     RPC, s3_web, admin/metrics — published together per instance.
//  4. No native TLS — Garage serves plaintext HTTP only. pgBackRest's S3 repo
//     requires HTTPS, so the docs front Garage with the nginx addon; pgcli does
//     NOT couple the two (installing garage with --tls is rejected at the CLI).
//
// v1 is single-node: the container runs `/garage server --single-node
// --default-bucket`, and a default access key + bucket are seeded from the
// GARAGE_DEFAULT_ACCESS_KEY / GARAGE_DEFAULT_SECRET_KEY / GARAGE_DEFAULT_BUCKET
// env (Garage ≥ v2.0 creates both on first boot). Unlike rustfs, Garage runs as
// root inside the container and upstream ships no uid-drop, so pgcli uses the
// upstream image directly — no wrapper image, pull-only. The image is FROM
// scratch: `podman exec ... sh` fails, but /garage itself is exec-able, so
// Exec runs control commands (bucket/key/layout) against a running node.
type GarageManager struct {
	cfg     *config.Config
	podman  string // podman binary path
	dataDir string // base data directory (e.g. ~/.pgcli/)
	bridge  bool   // macOS: serve on the pgcli-net bridge with published ports
}

// NewGarageManager creates a GarageManager. It mirrors NewRustfsManager: Linux
// serves over host networking, macOS over the pgcli-net bridge with each of the
// four ports published. The Garage image is multi-arch, so any host works.
func NewGarageManager(cfg *config.Config) (*GarageManager, error) {
	path, err := findPodman()
	if err != nil {
		return nil, fmt.Errorf("podman is not installed: %w", err)
	}
	dataDir := cfg.BaseDir
	if dataDir == "" {
		dataDir = platform.DefaultConfigDir()
	}
	ensurePodmanStateReady(path)
	return &GarageManager{
		cfg:     cfg,
		podman:  path,
		dataDir: dataDir,
		bridge:  platform.Detect() == platform.MacOS,
	}, nil
}

// resolveDataDir returns the instance's host data dir: the explicit DataDir
// override when set, otherwise <base>/addon/garage/<name>/data. In multi-drive
// mode Drives replaces it entirely (resolveDriveDirs); this function still
// answers for the metadata dir's parent anchor.
func (m *GarageManager) resolveDataDir(gc *config.GarageConfig) string {
	if gc.DataDir != "" {
		return gc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "garage", gc.Name, "data")
}

// resolveDriveDirs returns the host dirs backing this node's data_dir: the
// explicit Drives list when set (multi-disk — one dir per disk), else the
// single resolved DataDir. Mirrors SeaweedfsManager.resolveDriveDirs.
func (m *GarageManager) resolveDriveDirs(gc *config.GarageConfig) []string {
	if len(gc.Drives) > 0 {
		return gc.Drives
	}
	return []string{m.resolveDataDir(gc)}
}

// DrivesSharingRootDevice returns the subset of this instance's drive dirs that
// sit on the host's root device — advisory, same posture as seaweedfs: Garage
// happily uses such a dir, but multi-disk only pays off on separately-mounted
// disks, so the install warns per drive and never refuses.
func (m *GarageManager) DrivesSharingRootDevice(gc *config.GarageConfig) []string {
	var bad []string
	for _, d := range m.resolveDriveDirs(gc) {
		if shared, err := sharesRootDevice(d); err == nil && shared {
			bad = append(bad, d)
		}
	}
	return bad
}

// resolveMetadataDir returns the host dir backing metadata_dir:
// <base>/addon/garage/<name>/meta. There is no metadata_dir override field —
// one --data-dir knob keeps the CLI surface minimal, and the meta dir is small
// enough that splitting it onto separate media is a hand-edit concern, not a v1
// flag.
func (m *GarageManager) resolveMetadataDir(gc *config.GarageConfig) string {
	return filepath.Join(filepath.Dir(m.resolveDataDir(gc)), "meta")
}

// resolveConfigFile returns the host path of the rendered garage.toml:
// <base>/addon/garage/<name>/garage.toml (ConfigFile overrides for a hand-
// managed file, which then is not rewritten by WriteConfig).
func (m *GarageManager) resolveConfigFile(gc *config.GarageConfig) string {
	if gc.ConfigFile != "" {
		return gc.ConfigFile
	}
	return filepath.Join(filepath.Dir(m.resolveDataDir(gc)), "garage.toml")
}

// DataDir returns the resolved host data directory (for display).
func (m *GarageManager) DataDir(gc *config.GarageConfig) string {
	return m.resolveDataDir(gc)
}

// Drives returns the resolved host drive dirs (for display; includes the
// single-data-dir case, so it is the general accessor callers print).
func (m *GarageManager) Drives(gc *config.GarageConfig) []string {
	return m.resolveDriveDirs(gc)
}

// MetadataDir returns the resolved host metadata directory (for display).
func (m *GarageManager) MetadataDir(gc *config.GarageConfig) string {
	return m.resolveMetadataDir(gc)
}

// WriteConfig renders garage.toml for the instance (with the bind address
// Garage should see from inside its own network namespace, per proxyBindHost)
// and writes it 0600 — the file embeds rpc_secret, and Garage refuses a
// world-readable config that carries secrets. It runs before createContainer in
// both the install and the start-recreate paths so the mounted file always
// matches the config pgcli holds. Returns the host path written.
func (m *GarageManager) WriteConfig(gc *config.GarageConfig) (string, error) {
	path := m.resolveConfigFile(gc)
	if gc.ConfigFile != "" {
		// Operator-managed file: mount it verbatim, never rewrite it.
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("garage config_file %s: %w", path, err)
		}
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("creating garage config dir: %w", err)
	}
	bind := proxyBindHost(m.bridge, gc.Listen)
	var capacities []uint64
	if len(gc.Drives) > 1 {
		// Garage requires a capacity per data_dir entry; each drive gets its
		// whole filesystem (see driveCapacityGiB). createContainer mkdirs the
		// dirs before calling here, so the statfs resolves the real target.
		for _, d := range m.resolveDriveDirs(gc) {
			c, err := driveCapacityGiB(d)
			if err != nil {
				return "", err
			}
			capacities = append(capacities, c)
		}
	}
	if err := os.WriteFile(path, []byte(renderGarageTOML(gc, bind, capacities)), 0600); err != nil {
		return "", fmt.Errorf("writing garage.toml: %w", err)
	}
	return path, nil
}

// EnsureImage pulls the Garage image if it is not present locally (pull-only —
// unlike rustfs there is no wrapper image to build; the upstream dxflrs/garage
// image runs as root and needs no host-side ownership dance).
func (m *GarageManager) EnsureImage(imageTag string) error {
	exists, err := m.imageExists(imageTag)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	fmt.Printf("-> Pulling image %s...\n", imageTag)
	if err := m.runInteractive("pull", imageTag); err != nil {
		return fmt.Errorf("pulling garage image %s: %w", imageTag, err)
	}
	fmt.Println("  [OK] Image pulled")
	return nil
}

// EnsureContainer creates or restarts the Garage container (install semantics:
// stop/rm/recreate so updated ports/credentials take effect). The config is
// re-rendered first; data dirs are created if missing and never deleted here.
func (m *GarageManager) EnsureContainer(gc *config.GarageConfig) error {
	containerName := gc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Println("-> garage container already running, recreating to apply updated config...")
		if _, err := m.run("stop", containerName); err != nil {
			return fmt.Errorf("stopping garage container: %w", err)
		}
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing garage container: %w", err)
		}
	} else {
		exists, err := m.containerExists(containerName)
		if err != nil {
			return err
		}
		if exists {
			if _, err := m.run("rm", "-f", containerName); err != nil {
				return fmt.Errorf("removing stale garage container: %w", err)
			}
		}
	}

	if err := m.createContainer(gc); err != nil {
		return err
	}
	fmt.Println("  [OK] garage container started")
	return nil
}

// StartContainer starts the Garage container without recreating it
// (autostart-on-boot path): running → no-op; exists but stopped → podman
// start, recreating on improper state; missing → create fresh. The rendered
// garage.toml is refreshed either way (createContainer) or trusted as on-disk
// (plain start), matching how rustfs treats a stopped-but-present container.
func (m *GarageManager) StartContainer(gc *config.GarageConfig) error {
	containerName := gc.ContainerName

	running, err := m.containerRunning(containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("  [OK] garage %s already running\n", containerName)
		return nil
	}

	exists, err := m.containerExists(containerName)
	if err != nil {
		return err
	}
	if exists {
		if _, err := m.run("start", containerName); err == nil {
			fmt.Printf("  [OK] garage %s started\n", containerName)
			return nil
		}
		fmt.Printf("  [!] garage %s in improper state, recreating...\n", containerName)
		if _, err := m.run("rm", "-f", containerName); err != nil {
			return fmt.Errorf("removing improper garage container: %w", err)
		}
	}

	if err := m.createContainer(gc); err != nil {
		return err
	}
	fmt.Printf("  [OK] garage %s started\n", containerName)
	return nil
}

// createContainer runs Garage. Unlike rustfs (env-only, wrapper entrypoint
// derives argv) or silo (--entrypoint override), Garage takes its config from
// the mounted /etc/garage.toml, and pgcli passes the sub-command explicitly
// (`garage server ...`) rather than trusting the image CMD. The container runs
// as root (the image has no other user), so there is no --user flag and no
// host-side chown; the bind dirs just need to exist.
//
// The two topologies differ only in the trailing argv/env:
//   - single-node: `server --single-node --default-bucket` plus the three
//     GARAGE_DEFAULT_* env, which seed one access key + one bucket on first
//     boot (Garage ≥ v2.0). This path is byte-identical to v1.
//   - cluster (gc.Clustered()): bare `server` with NO seed env — the
//     --default-bucket/GARAGE_DEFAULT_* path is single-node-only, and in a
//     cluster the key + bucket are created explicitly by the CLI once quorum
//     forms (see internal/cli.runAddonInstallGarage).
func (m *GarageManager) createContainer(gc *config.GarageConfig) error {
	metaDir := m.resolveMetadataDir(gc)
	driveDirs := m.resolveDriveDirs(gc)
	for _, d := range driveDirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating garage dir %s: %w", d, err)
		}
	}
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		return fmt.Errorf("creating garage dir %s: %w", metaDir, err)
	}

	configPath, err := m.WriteConfig(gc)
	if err != nil {
		return err
	}

	args := []string{
		"run", "-d",
		"--name", gc.ContainerName,
	}
	args = append(args, netFlags(m.bridge, m.cfg.Podman.Network, gc.APIPort, gc.RPCPort, gc.WebPort, gc.AdminPort)...)
	args = append(args,
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "60",
	)
	args = append(args, garageMountFlags(metaDir, driveDirs, configPath)...)

	if gc.Clustered() {
		// Cluster member: start the daemon bare. Peering (node connect) and the
		// layout (assign/apply) are driven afterwards via ExecCapture.
		args = append(args, gc.ImageTag, "/garage", "server")
	} else {
		// Single-node first-boot seeding (Garage >= v2.0): create this access
		// key and this bucket, owned by that key, if they do not exist yet.
		// The key/bucket land in metadata_dir, so they survive recreation.
		// `-e` must precede the image: podman stops parsing container flags at
		// the image name and would otherwise treat "-e" as the command.
		args = append(args,
			"-e", "GARAGE_DEFAULT_ACCESS_KEY="+gc.RootUser,
			"-e", "GARAGE_DEFAULT_SECRET_KEY="+gc.RootPassword,
			"-e", "GARAGE_DEFAULT_BUCKET="+gc.Name,
			gc.ImageTag,
			"/garage", "server", "--single-node", "--default-bucket",
		)
	}

	if _, err := m.run(args...); err != nil {
		return fmt.Errorf("creating garage container: %w", err)
	}
	return nil
}

// Exec runs a Garage control command (`/garage bucket list`, `/garage key
// create`, ...) inside the running container, with stdio passthrough. The image
// has no shell, so the /garage binary itself is the command — same shape as
// NginxManager.Exec running `nginx -s reload`. Callers pass the args after
// /garage (this method prefixes it).
func (m *GarageManager) Exec(gc *config.GarageConfig, args []string) error {
	running, err := m.containerRunning(gc.ContainerName)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("garage container %s is not running — run 'pg addon start garage --name %s' first", gc.ContainerName, gc.Name)
	}
	execArgs := []string{"exec", "-i", gc.ContainerName, "/garage"}
	execArgs = append(execArgs, args...)
	return m.runInteractive(execArgs...)
}

// Remove stops and removes the garage container. The metadata dir and every
// data drive are kept by default; cleanData also deletes them — each refusing
// any still mounted.
// Garage writes as root and pgcli runs podman as root on the managed hosts, so
// plain removal suffices — no rustfs-style `podman unshare` fallback.
func (m *GarageManager) Remove(gc *config.GarageConfig, cleanData bool) error {
	containerName := gc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing garage container: %w", err)
	}
	fmt.Println("  [OK] garage container removed")

	dirs := []string{m.resolveMetadataDir(gc)}
	dirs = append(dirs, m.resolveDriveDirs(gc)...)
	if cfgFile := gc.ConfigFile; cfgFile == "" {
		// pgcli-rendered config: it lives beside the dirs and goes with them.
		dirs = append(dirs, m.resolveConfigFile(gc))
	}
	if !cleanData {
		for _, d := range dirs {
			fmt.Printf("  [OK] Garage directory kept: %s\n", d)
		}
		return nil
	}
	for _, d := range dirs {
		if isMountpoint(d) {
			fmt.Printf("  [!] Refusing to delete %s: it is still a mount point — unmount it first if the data below is really disposable\n", d)
			continue
		}
		if err := os.RemoveAll(d); err != nil && !os.IsNotExist(err) {
			fmt.Printf("  [!] Warning: removing garage dir %s: %v\n", d, err)
		} else if fi, serr := os.Stat(d); serr == nil && !fi.IsDir() {
			fmt.Printf("  [OK] Garage file removed: %s\n", d)
		} else {
			fmt.Printf("  [OK] Garage directory removed: %s\n", d)
		}
	}
	if gc.DataDir == "" {
		// Default layout: also prune the <name> dir when empty. With a
		// DataDir override the parent belongs to the user — never touch.
		parent := filepath.Dir(m.resolveDataDir(gc))
		if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
			_ = os.Remove(parent)
		}
	}
	return nil
}

// ContainerRunning reports whether the named container is currently running.
func (m *GarageManager) ContainerRunning(name string) (bool, error) {
	return m.containerRunning(name)
}

// ContainerExists reports whether a container with the given name exists.
func (m *GarageManager) ContainerExists(name string) (bool, error) {
	return m.containerExists(name)
}

// Stop stops a garage container.
func (m *GarageManager) Stop(name string) (string, error) {
	return m.run("stop", name)
}

// --- Internal helpers ---------------------------------------------------

func (m *GarageManager) run(args ...string) (string, error) {
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

// garageExecOutput runs `podman exec <args...>` with the shared idle-kill
// capture pipeline (see execIdleKilled for why cmd.Output() is wrong here).
func (m *GarageManager) garageExecOutput(d time.Duration, args ...string) (string, error) {
	return execIdleKilled(m.podman, d, defaultIdleKill, args...)
}

func (m *GarageManager) runInteractive(args ...string) error {
	slog.Debug("podman", "args", args)
	cmd := podmanCommand(m.podman, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (m *GarageManager) imageExists(tag string) (bool, error) {
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

func (m *GarageManager) containerExists(name string) (bool, error) {
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

func (m *GarageManager) containerRunning(name string) (bool, error) {
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
