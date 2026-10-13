package podman

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
//  2. Two dirs, not one: metadata_dir (LMDB) and data_dir (blocks), each its
//     own bind mount at the container paths in garage_paths.go.
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
// override when set, otherwise <base>/addon/garage/<name>/data.
func (m *GarageManager) resolveDataDir(gc *config.GarageConfig) string {
	if gc.DataDir != "" {
		return gc.DataDir
	}
	return filepath.Join(m.dataDir, "addon", "garage", gc.Name, "data")
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
	if err := os.WriteFile(path, []byte(renderGarageTOML(gc, bind)), 0600); err != nil {
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
	dataDir := m.resolveDataDir(gc)
	for _, d := range []string{metaDir, dataDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating garage dir %s: %w", d, err)
		}
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
	args = append(args, garageMountFlags(metaDir, dataDir, configPath)...)

	args = append(args, gc.ImageTag)
	if gc.Clustered() {
		// Cluster member: start the daemon bare. Peering (node connect) and the
		// layout (assign/apply) are driven afterwards via ExecCapture.
		args = append(args, "/garage", "server")
	} else {
		// Single-node first-boot seeding (Garage >= v2.0): create this access
		// key and this bucket, owned by that key, if they do not exist yet.
		// The key/bucket land in metadata_dir, so they survive recreation.
		args = append(args,
			"-e", "GARAGE_DEFAULT_ACCESS_KEY="+gc.RootUser,
			"-e", "GARAGE_DEFAULT_SECRET_KEY="+gc.RootPassword,
			"-e", "GARAGE_DEFAULT_BUCKET="+gc.Name,
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

// Remove stops and removes the garage container. The two data dirs are kept by
// default; cleanData also deletes them — each refusing any still mounted.
// Garage writes as root and pgcli runs podman as root on the managed hosts, so
// plain removal suffices — no rustfs-style `podman unshare` fallback.
func (m *GarageManager) Remove(gc *config.GarageConfig, cleanData bool) error {
	containerName := gc.ContainerName

	m.run("stop", containerName)
	if _, err := m.run("rm", "-f", containerName); err != nil {
		return fmt.Errorf("removing garage container: %w", err)
	}
	fmt.Println("  [OK] garage container removed")

	dirs := []string{m.resolveMetadataDir(gc), m.resolveDataDir(gc)}
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

// ErrCLIHung marks the hard wall-clock cap firing in garageExecOutput (as
// opposed to the command failing on its own).
var ErrCLIHung = errors.New("garage CLI wedged without producing output (re-run the same command)")

// garageIdleKill is how long garageExecOutput waits after the LAST byte of
// output before deciding the CLI is on its never-exit path. Healthy garage
// CLIs exit under a second after printing, so this only ever elapses for the
// wedge.
const garageIdleKill = 3 * time.Second

// garageExecOutput runs `podman exec <args...>` capturing stdout+stderr
// (merged — useful garage messages live on both), capped by d.
//
// cmd.Output() cannot be used: once a cluster has peers, a peer dials back
// into the CLI's ephemeral RPC endpoint and the process never exits after
// printing (verified on v2.4.1: `timeout 20` still had to kill a fully
// printed `node id`). The exec session holds the pipe open, so Output()
// would burn the whole cap on a complete answer. Instead: watch the pipe, and
// when output has arrived and the stream then stays quiet for garageIdleKill,
// SIGTERM the process group. podman forwards it into the container, the
// session ends, the pipe closes, and Wait returns — output preserved, error
// dropped because the kill was ours. Commands that FAIL print and exit
// non-zero on their own (verified: both `node connect` refused and a bad
// layout apply returned within seconds with rc≠0), so they are never idle-
// killed and their ExitError still reaches callers.
//
// The whole process group is the signal target everywhere here: podman
// re-execs itself, and killing only the direct child leaves an orphaned podman
// holding the pipe and squatting the container's exec session — the state-rot
// pattern seen on the VMs. cmd.WaitDelay escalates survivors to group SIGKILL.
func (m *GarageManager) garageExecOutput(d time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := podmanCommandContext(ctx, m.podman, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second

	pr, pw, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	pw.Close() // our copy; the child holds its own until it dies

	var mu sync.Mutex
	var buf strings.Builder
	drained := make(chan struct{})
	chunk := make(chan struct{}, 1)
	go func() {
		defer close(drained)
		b := make([]byte, 4096)
		for {
			n, rerr := pr.Read(b)
			if n > 0 {
				mu.Lock()
				buf.Write(b[:n])
				mu.Unlock()
				select {
				case chunk <- struct{}{}:
				default:
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	hard := time.NewTimer(d)
	defer hard.Stop()
	var idle *time.Timer
	var idleC <-chan time.Time
	killedEarly := false
	for {
		select {
		case werr := <-waited:
			pr.Close()
			<-drained
			mu.Lock()
			out := buf.String()
			mu.Unlock()
			switch {
			case werr == nil, killedEarly:
				return out, nil
			case ctx.Err() == context.DeadlineExceeded:
				return out, fmt.Errorf("podman %s: %w after %s", strings.Join(args, " "), ErrCLIHung, d)
			}
			if exitErr, ok := werr.(*exec.ExitError); ok {
				// run() surfaces merged output on failure; ExecCapture parses it.
				msg := string(exitErr.Stderr)
				if msg == "" {
					msg = out
				}
				return out, fmt.Errorf("podman %s: %s", strings.Join(args, " "), msg)
			}
			return out, fmt.Errorf("podman %s: %w", strings.Join(args, " "), werr)
		case <-hard.C:
			// Never went quiet even once: real wedge, end-of-story error.
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-chunk:
			if idle == nil {
				idle = time.NewTimer(garageIdleKill)
				idleC = idle.C
			} else {
				idle.Reset(garageIdleKill)
			}
		case <-idleC:
			killedEarly = true
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	}
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
