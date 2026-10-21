package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/hostbin"
	"github.com/mars-base/pgcli/internal/platform"
)

// ---------------------------------------------------------------------------
// zerofs — the second HOST-BINARY addon: an LSM-on-S3 filesystem
// ---------------------------------------------------------------------------
//
// ZeroFS differs structurally from JuiceFS: it carries NO external metadata
// engine. Each volume is a self-contained log-structured store whose metadata
// (an LSM tree) lives in the object store itself, so one config entry is really
// TWO host processes: a server (`zerofs run -c config.toml`) that speaks 9P on
// a loopback port and owns the S3 + encryption side, and a client
// (`zerofs mount <9p> <dir>`) exposing the FUSE directory. pgcli manages both
// with the hostbin process primitives.
//
// ZeroFS requires conditional writes (put-if-not-exists) for writer fencing.
// MinIO (RELEASE.2022-08+) and real S3 support this natively, so pgcli's
// default backend does; a store that doesn't (untested: silo) needs
// --conditional-put pointing the primitive at a redis addon. The 9P listener has
// no authentication, so it is forced to loopback (config Validate enforces it;
// install pre-checks with a clearer message).
//
// The config.toml is pgcli-rendered (never `zerofs init`), so a golden test
// pins its shape and re-installs stay byte-stable. Secrets (encryption
// password + S3 keys) live only in that 0600 file.

// zerofsDownload resolves the multiplatform release tarball and the member to
// extract for the running arch.
func zerofsDownload(version string) (hostbin.Download, error) {
	if runtime.GOOS != "linux" {
		return hostbin.Download{}, fmt.Errorf("zerofs supports Linux only (host FUSE)")
	}
	v := strings.TrimPrefix(version, "v")
	return hostbin.Download{
		Binary: "zerofs",
		URL:    fmt.Sprintf("https://github.com/Barre/ZeroFS/releases/download/v%s/zerofs-pgo-multiplatform.tar.gz", v),
		Member: fmt.Sprintf("zerofs-linux-%s-pgo", runtime.GOARCH),
		SHA256: config.ZerofsTarballSHA256(),
	}, nil
}

// ensureZerofsBinary locates or installs the host zerofs binary.
func ensureZerofsBinary(version, installDir string) (string, string, error) {
	if p, ok := hostbin.Detect("zerofs"); ok {
		return p, "", nil
	}
	dl, err := zerofsDownload(version)
	if err != nil {
		return "", "", err
	}
	dir, note, err := hostbin.InstallDir(installDir)
	if err != nil {
		return "", "", err
	}
	fmt.Printf("-> Downloading zerofs %s (linux/%s)...\n", version, runtime.GOARCH)
	path, err := hostbin.Ensure(dl, dir)
	if err != nil {
		return "", "", err
	}
	return path, note, nil
}

// renderZerofsConfig produces the server config in ZeroFS's documented
// [cache]/[storage]/[aws]/[servers.ninep] shape. The golden test
// (TestRenderZerofsConfig) pins exact output. conditional_put is emitted only
// in redis mode; `none` and `auto` both rely on native fencing (omitted).
func renderZerofsConfig(zc *config.ZerofsConfig, tgt *s3Target) string {
	var b strings.Builder
	b.WriteString("# pgcli-managed ZeroFS server config — do not edit by hand.\n")
	b.WriteString("# Regenerate with: pg addon install zerofs --name " + zc.Name + " --force\n\n")
	fmt.Fprintf(&b, "[cache]\ndir = %q\ndisk_size_gb = %d.0\n\n", zc.CacheDir, zc.ServerCacheGB)
	fmt.Fprintf(&b, "[storage]\nurl = %q\nencryption_password = %q\n\n", "s3://"+zc.Bucket, zc.EncryptionPassword)
	b.WriteString("[aws]\n")
	fmt.Fprintf(&b, "access_key_id = %q\nsecret_access_key = %q\n", zc.AccessKey, zc.SecretKey)
	fmt.Fprintf(&b, "endpoint = %q\n", tgt.BaseURL)
	if !tgt.TLS {
		// ZeroFS's parser wants allow_http as a STRING, not a TOML bool.
		b.WriteString("allow_http = \"true\"\n")
	}
	if zc.ConditionalPut == config.ZerofsConditionalPutRedis && zc.ConditionalPutURL != "" {
		fmt.Fprintf(&b, "conditional_put = %q\n", zc.ConditionalPutURL)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "[servers.ninep]\naddresses = [%q]\n", net.JoinHostPort(zc.Listen, fmt.Sprint(zc.ListenPort)))
	return b.String()
}

// zerofsMountLog is the client-side log, a sibling of the server log.
func zerofsMountLog(zc *config.ZerofsConfig) string {
	return strings.TrimSuffix(zc.LogFile, ".log") + ".mount.log"
}

// waitForTCPPort polls a loopback TCP port until it accepts or the timeout hits.
func waitForTCPPort(host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func isLoopbackBind(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// runAddonInstallZerofs implements `pg addon install zerofs`.
func runAddonInstallZerofs(cmd *cobra.Command) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("the zerofs add-on runs a host FUSE mount and is Linux-only (like beegfs/juicefs)")
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "zerofs"
	}
	// zerofs has no name embedded in config.toml (the bucket carries the
	// identity), so only path-safety matters — no juicefs-style 3-63 rule.
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\ "`) {
		return fmt.Errorf("volume name %q is invalid: no path separators or spaces", name)
	}
	fmt.Printf("=== pg addon install zerofs (%s) ===\n", name)
	// ZeroFS's cache engine needs far more fds than the usual 1024 soft limit.
	cur, raised := raiseOpenFileLimit()
	fmt.Printf("  [i] open-file soft limit: %d%s\n", cur, map[bool]string{true: "", false: " (below 65536 — if the server dies with \"No file descriptors available\", raise it: ulimit -n)"}[raised])

	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s -- run \"pg config init\" first", path)
	}
	c, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if c.Addons.Zerofs == nil {
		c.Addons.Zerofs = make(map[string]config.ZerofsConfig)
	}
	zc, present := c.Addons.Zerofs[name]
	force, _ := cmd.Flags().GetBool("force")
	if present && !force {
		fmt.Printf("✓ zerofs %q already installed (start/stop/mount via `pg addon zerofs`; --force regenerates config.toml and re-formats)\n", name)
		return nil
	}
	if !present {
		zc = config.ZerofsConfig{Name: name}
	}
	zc.Name = name

	fs := cmd.Flags()
	if fs.Changed("version") {
		zc.Version, _ = fs.GetString("version")
	}
	if zc.Version == "" {
		zc.Version = config.DefaultZerofsVersion
	}
	if v, _ := fs.GetString("s3"); v != "" {
		zc.S3Addon = v
	}
	if zc.S3Addon == "" {
		zc.S3Addon = "minio"
	}
	if v, _ := fs.GetString("s3-name"); v != "" {
		zc.S3Name = v
	}
	if zc.S3Name == "" {
		zc.S3Name = zc.S3Addon
	}
	if v, _ := fs.GetString("bucket"); v != "" {
		zc.Bucket = v
	}
	if v, _ := fs.GetString("mount-point"); v != "" {
		zc.MountPoint = v
	}
	if v, _ := fs.GetString("cache-dir"); v != "" {
		zc.CacheDir = v
	}
	if fs.Changed("listen") {
		zc.Listen, _ = fs.GetString("listen")
	}
	if fs.Changed("port") {
		zc.ListenPort, _ = fs.GetInt("port")
	}
	if fs.Changed("server-cache-gb") {
		zc.ServerCacheGB, _ = fs.GetInt("server-cache-gb")
	}
	if v, _ := fs.GetString("conditional-put"); v != "" {
		zc.ConditionalPut = v
	}
	if zc.ConditionalPut == "" {
		zc.ConditionalPut = config.ZerofsConditionalPutAuto
	}
	if v, _ := fs.GetString("redis-name"); v != "" {
		zc.RedisName = v
	}
	if v, _ := fs.GetString("encryption-password"); v != "" {
		zc.EncryptionPassword = v
	}
	if !config.ValidZerofsConditionalPut(zc.ConditionalPut) {
		return fmt.Errorf("--conditional-put %q is not one of auto, redis, none", zc.ConditionalPut)
	}

	// Persist the draft so ApplyDefaults can resolve host paths from it, then
	// re-read. (Defaults apply to the map, so store first.)
	c.Addons.Zerofs[name] = zc
	c.ApplyDefaults()
	zc = c.Addons.Zerofs[name]

	if !isLoopbackBind(zc.Listen) {
		return fmt.Errorf("  [X] zerofs 9P listens without authentication — --listen must be loopback (got %q)", zc.Listen)
	}

	installDir, _ := fs.GetString("install-dir")
	noMount, _ := fs.GetBool("no-mount")

	// 1. Binary.
	bin, note, err := ensureZerofsBinary(zc.Version, installDir)
	if err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	zc.BinaryPath = bin
	fmt.Printf("  [OK] zerofs binary: %s\n", bin)
	if note != "" {
		fmt.Printf("  [!] %s\n", note)
	}

	// 2. FUSE.
	if err := preflightFUSE(); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Println("  [OK] FUSE available (/dev/fuse, fusermount3)")

	// 3. S3 backend + credentials.
	tgt, err := resolveS3Target(c, zc.S3Addon, zc.S3Name)
	if err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] S3 backend: %s addon %q at %s\n", tgt.Kind, tgt.Name, tgt.BaseURL)
	akSet, skSet := fs.Changed("access-key"), fs.Changed("secret-key")
	switch {
	case akSet != skSet:
		return fmt.Errorf("  [X] --access-key and --secret-key must be given together (or neither, to use the %s root credentials)", tgt.Kind)
	case akSet:
		fmt.Printf("  [OK] credentials: explicit access-key %q\n", zc.AccessKey)
	default:
		zc.AccessKey, zc.SecretKey = tgt.User, tgt.Password
		fmt.Printf("  [OK] credentials: %s addon root (%s)\n", tgt.Kind, tgt.User)
	}

	// 4. Conditional-put fencing backend.
	redisDSN, _ := fs.GetString("redis-dsn")
	switch zc.ConditionalPut {
	case config.ZerofsConditionalPutRedis:
		rurl := redisDSN
		if rurl == "" {
			rurl, err = redisMetaURL(c, zc.RedisName)
			if err != nil {
				return fmt.Errorf("  [X] --conditional-put redis: %v", err)
			}
		}
		zc.ConditionalPutURL = rurl
		fmt.Printf("  [OK] conditional-put: redis backend (%s)\n", dsnDisplay(rurl))
	case config.ZerofsConditionalPutNone:
		fmt.Println("  [!] conditional-put none — no fencing; safe ONLY if nothing else writes this bucket")
	default: // auto
		fmt.Printf("  [OK] conditional-put: native If-None-Match (%s)\n", tgt.Kind)
	}

	// 5. Encryption password — generated once. --force regenerates the
	// config.toml but KEEPS the stored password: the bucket's data-encryption
	// key is wrapped by it, so a fresh password would orphan existing data
	// ("Invalid password or corrupted key"). True reset = remove + fresh bucket.
	if zc.EncryptionPassword == "" {
		pw, gerr := generatePassword(24)
		if gerr != nil {
			return fmt.Errorf("generating encryption password: %w", gerr)
		}
		zc.EncryptionPassword = pw
		fmt.Println("  [OK] encryption password generated")
	} else {
		fmt.Println("  [OK] encryption password reused (the bucket's DEK is wrapped by it)")
	}

	// Persist before writing files / spawning processes.
	c.Addons.Zerofs[name] = zc
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	// 6. config.toml.
	if err := os.MkdirAll(filepath.Dir(zc.ConfigPath), 0o755); err != nil {
		return fmt.Errorf("creating zerofs dir: %w", err)
	}
	if err := os.WriteFile(zc.ConfigPath, []byte(renderZerofsConfig(&zc, tgt)), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", zc.ConfigPath, err)
	}
	fmt.Printf("  [OK] config: %s\n", zc.ConfigPath)

	// 7. Server (detached), wait for the 9P port.
	if hostbin.Alive(zc.ServerPidFile) {
		fmt.Println("  [OK] server already running")
	} else {
		if err := hostbin.StartDetached(bin, []string{"run", "-c", zc.ConfigPath}, zc.LogFile, zc.ServerPidFile); err != nil {
			return fmt.Errorf("  [X] starting server: %v", err)
		}
		if werr := waitForTCPPort(zc.Listen, zc.ListenPort, 10*time.Second); werr != nil {
			fmt.Printf("  [X] server did not listen on %s:%d — check %s\n", zc.Listen, zc.ListenPort, zc.LogFile)
			_ = hostbin.Stop(zc.ServerPidFile, 3*time.Second)
			tail, _ := lastLines(zc.LogFile, 3)
			switch {
			case strings.Contains(tail, "Invalid password or corrupted key"):
				// The bucket's DEK is wrapped by a password pgcli no longer
				// stores (a plain remove deletes the entry but keeps the
				// bucket) — or the wrong one was passed.
				fmt.Println("      (encryption password does not match this bucket's data — pass the original: --encryption-password ... ; to start over, empty the bucket or use a new --bucket)")
			case strings.Contains(tail, "conditional") || strings.Contains(tail, "If-None-Match"):
				fmt.Println("      (a store without conditional-put support fails here — retry with --conditional-put redis)")
			default:
				fmt.Println("      (last log lines:)")
				for _, l := range strings.Split(strings.TrimSpace(tail), "\n") {
					fmt.Println("        " + l)
				}
			}
			return fmt.Errorf("zerofs server failed to start")
		}
		fmt.Printf("  [OK] server listening on %s:%d\n", zc.Listen, zc.ListenPort)
	}

	// 8. Client mount unless --no-mount.
	if noMount {
		fmt.Printf("  [i] --no-mount: not mounting. Mount later: pg addon zerofs mount --name %s\n", name)
	} else {
		if err := zerofsMount(bin, &zc); err != nil {
			fmt.Printf("  [X] mount failed: %v\n", err)
			printBucketHint(tgt, zc.Bucket)
			return fmt.Errorf("mount %q", name)
		}
		fmt.Printf("  [OK] mounted at %s\n", zc.MountPoint)
	}

	fmt.Println()
	fmt.Printf("✓ zerofs installed: %q\n", name)
	fmt.Printf("  Volume:      %s (bucket %s on %s %q)\n", name, zc.Bucket, tgt.Kind, tgt.Name)
	fmt.Printf("  9P endpoint: %s:%d (loopback, unauthenticated)\n", zc.Listen, zc.ListenPort)
	fmt.Printf("  Mount point: %s\n", zc.MountPoint)
	fmt.Printf("  Config:      %s (0600 — holds the encryption key; pg addon password zerofs to view)\n", zc.ConfigPath)
	fmt.Println()
	fmt.Printf("  pgcli does not create buckets. If the mount fails, provision once:\n")
	printBucketHint(tgt, zc.Bucket)
	fmt.Println()
	fmt.Printf("  Manage: pg addon zerofs {status|stop|start|umount|mount} --name %s\n", name)
	fmt.Printf("  Remove: pg addon remove zerofs --name %s\n", name)
	return nil
}

// lastLines returns the final n lines of a (small, daemon-written) log file.
func lastLines(path string, n int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// zerofsMount starts the FUSE client detached against the local 9P server.
func zerofsMount(bin string, zc *config.ZerofsConfig) error {
	if err := os.MkdirAll(zc.MountPoint, 0o755); err != nil {
		return fmt.Errorf("creating mount point: %w", err)
	}
	if hostbin.Alive(zc.MountPidFile) && isMountPoint(zc.MountPoint) {
		return nil
	}
	endpoint := net.JoinHostPort(zc.Listen, fmt.Sprint(zc.ListenPort))
	if err := hostbin.StartDetached(bin, []string{"mount", endpoint, zc.MountPoint}, zerofsMountLog(zc), zc.MountPidFile); err != nil {
		return err
	}
	// The client mounts asynchronously: /proc/mounts lags StartDetached by a
	// beat, and answering "mounted" before it lands makes the very next
	// isMountPoint check (umount/status) lie. We deliberately do NOT poll
	// hostbin.Alive here — between fork and exec the child's cmdline is empty,
	// so Alive false-alarms "exited" for a healthy process; the timeout plus
	// the mount log covers genuine failures.
	deadline := time.Now().Add(10 * time.Second)
	for !isMountPoint(zc.MountPoint) {
		if time.Now().After(deadline) {
			return fmt.Errorf("client did not mount %s — check %s", zc.MountPoint, zerofsMountLog(zc))
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// --- `pg addon zerofs` lifecycle --------------------------------------------

func resolveZerofs(cmd *cobra.Command) (*config.Config, string, config.ZerofsConfig, error) {
	c, _, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.ZerofsConfig{}, err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "zerofs"
	}
	if c.Addons.Zerofs == nil {
		return nil, "", config.ZerofsConfig{}, fmt.Errorf("no zerofs add-ons configured (run 'pg addon install zerofs')")
	}
	if _, ok := c.Addons.Zerofs[name]; !ok {
		return nil, "", config.ZerofsConfig{}, fmt.Errorf("zerofs volume %q not found (see 'pg addon list')", name)
	}
	c.ApplyDefaults()
	return c, name, c.Addons.Zerofs[name], nil
}

func runZerofsStart(cmd *cobra.Command, _ []string) error {
	_, name, zc, err := resolveZerofs(cmd)
	if err != nil {
		return err
	}
	if hostbin.Alive(zc.ServerPidFile) {
		fmt.Printf("✓ zerofs %q server already running\n", name)
		return nil
	}
	raiseOpenFileLimit()
	if err := hostbin.StartDetached(zc.BinaryPath, []string{"run", "-c", zc.ConfigPath}, zc.LogFile, zc.ServerPidFile); err != nil {
		return fmt.Errorf("starting server: %w", err)
	}
	if err := waitForTCPPort(zc.Listen, zc.ListenPort, 10*time.Second); err != nil {
		return fmt.Errorf("server did not come up — check %s", zc.LogFile)
	}
	fmt.Printf("✓ zerofs %q server started on %s:%d\n", name, zc.Listen, zc.ListenPort)
	return nil
}

func runZerofsStop(cmd *cobra.Command, _ []string) error {
	_, name, zc, err := resolveZerofs(cmd)
	if err != nil {
		return err
	}
	zerofsStopBoth(&zc)
	fmt.Printf("✓ zerofs %q stopped (client unmounted, server killed)\n", name)
	return nil
}

// zerofsStopBoth tears the client down before the server (umount needs the 9P
// endpoint alive), then the server, and force-releases the mount point if the
// client didn't.
func zerofsStopBoth(zc *config.ZerofsConfig) {
	_ = hostbin.Stop(zc.MountPidFile, 5*time.Second)
	if isMountPoint(zc.MountPoint) {
		_ = exec.Command("fusermount3", "-u", zc.MountPoint).Run()
	}
	_ = hostbin.Stop(zc.ServerPidFile, 5*time.Second)
}

func runZerofsMount(cmd *cobra.Command, _ []string) error {
	_, name, zc, err := resolveZerofs(cmd)
	if err != nil {
		return err
	}
	if isMountPoint(zc.MountPoint) {
		fmt.Printf("✓ zerofs %q already mounted at %s\n", name, zc.MountPoint)
		return nil
	}
	if !hostbin.Alive(zc.ServerPidFile) {
		return fmt.Errorf("server not running — start it: pg addon zerofs start --name %s", name)
	}
	if err := zerofsMount(zc.BinaryPath, &zc); err != nil {
		return fmt.Errorf("mounting: %w", err)
	}
	fmt.Printf("✓ zerofs %q mounted at %s\n", name, zc.MountPoint)
	return nil
}

func runZerofsUmount(cmd *cobra.Command, _ []string) error {
	_, name, zc, err := resolveZerofs(cmd)
	if err != nil {
		return err
	}
	if !isMountPoint(zc.MountPoint) {
		fmt.Printf("zerofs %q is not mounted\n", name)
		return nil
	}
	_ = hostbin.Stop(zc.MountPidFile, 5*time.Second)
	if isMountPoint(zc.MountPoint) {
		_ = exec.Command("fusermount3", "-u", zc.MountPoint).Run()
	}
	if isMountPoint(zc.MountPoint) {
		return fmt.Errorf("failed to unmount %s", zc.MountPoint)
	}
	fmt.Printf("✓ zerofs %q unmounted\n", name)
	return nil
}

func runZerofsStatus(cmd *cobra.Command, _ []string) error {
	_, name, zc, err := resolveZerofs(cmd)
	if err != nil {
		return err
	}
	srv := "down"
	if hostbin.Alive(zc.ServerPidFile) {
		srv = "running"
	}
	mnt := "not mounted"
	if isMountPoint(zc.MountPoint) {
		mnt = "mounted at " + zc.MountPoint
	}
	fmt.Printf("zerofs %q\n", name)
	fmt.Printf("  Server:    %s (%s:%d)\n", srv, zc.Listen, zc.ListenPort)
	fmt.Printf("  Mount:     %s\n", mnt)
	fmt.Printf("  S3:        %s addon %q, bucket %s\n", zc.S3Addon, zc.S3Name, zc.Bucket)
	fmt.Printf("  Config:    %s\n", zc.ConfigPath)
	fmt.Printf("  Log:       %s\n", zc.LogFile)
	return nil
}

func runAddonRemoveZerofs(cmd *cobra.Command) error {
	_, name, zc, err := resolveZerofs(cmd)
	if err != nil {
		return err
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	fmt.Printf("=== pg addon remove zerofs (%s) ===\n", name)
	zerofsStopBoth(&zc)
	fmt.Printf("  [OK] stopped (client + server)\n")

	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	delete(c.Addons.Zerofs, name)
	if len(c.Addons.Zerofs) == 0 {
		c.Addons.Zerofs = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)
	addonDir := filepath.Dir(zc.MountPoint)
	if cleanData {
		if err := os.RemoveAll(addonDir); err != nil {
			return fmt.Errorf("cleaning %s: %w", addonDir, err)
		}
		fmt.Printf("  [OK] cleaned %s (config.toml, cache, logs)\n", addonDir)
	} else {
		fmt.Printf("  [i] kept %s and the %s bucket; the encrypted LSM data in object storage is never deleted automatically\n", addonDir, zc.Bucket)
	}
	fmt.Printf("✓ zerofs %q removed\n", name)
	return nil
}

// --- registration -----------------------------------------------------------

var addonZerofsCmd = &cobra.Command{
	Use:   "zerofs",
	Short: "Manage zerofs host-mount add-on instances",
	Long: `Manage zerofs add-on instances: host FUSE mounts backed by a pgcli S3 addon.

A zerofs volume is two host processes: a server (zerofs run) speaking 9P on a
loopback port that owns the S3 + encryption side, and a client (zerofs mount)
exposing the FUSE directory.

Subcommands:
  start    Bring up the 9P server
  stop     Unmount the client (if mounted) and kill the server
  mount    Attach the FUSE client to a running server
  umount   Detach the FUSE client
  status   Show server, mount, backend and file locations

Examples:
  pg addon install zerofs --name data --s3 minio --s3-name ms3 --bucket zerofs-data
  pg addon zerofs status --name data
  pg addon zerofs stop --name data`,
}

var addonZerofsStartCmd = &cobra.Command{Use: "start", Short: "Start the zerofs 9P server", RunE: runZerofsStart}
var addonZerofsStopCmd = &cobra.Command{Use: "stop", Short: "Stop the zerofs server (unmounting first)", RunE: runZerofsStop}
var addonZerofsMountCmd = &cobra.Command{Use: "mount", Short: "Mount the zerofs FUSE client", RunE: runZerofsMount}
var addonZerofsUmountCmd = &cobra.Command{Use: "umount", Short: "Unmount the zerofs FUSE client", RunE: runZerofsUmount}
var addonZerofsStatusCmd = &cobra.Command{Use: "status", Short: "Show zerofs volume status", RunE: runZerofsStatus}

// runAddonStartZerofs / runAddonStopZerofs wire the top-level `pg addon
// start|stop zerofs` to the server start / full stop.
func runAddonStartZerofs(cmd *cobra.Command) error { return runZerofsStart(cmd, nil) }
func runAddonStopZerofs(cmd *cobra.Command) error  { return runZerofsStop(cmd, nil) }

func init() {
	addonCmd.AddCommand(addonZerofsCmd)
	addonZerofsCmd.AddCommand(addonZerofsStartCmd, addonZerofsStopCmd, addonZerofsMountCmd, addonZerofsUmountCmd, addonZerofsStatusCmd)
	for _, c := range []*cobra.Command{addonZerofsStartCmd, addonZerofsStopCmd, addonZerofsMountCmd, addonZerofsUmountCmd, addonZerofsStatusCmd} {
		c.Flags().String("name", "", "zerofs volume name (default \"zerofs\")")
	}

	addonInstallCmd.Flags().String("conditional-put", "auto", "zerofs: fencing backend — auto (native If-None-Match, needs MinIO 2022-08+ / real S3; default), redis (fence via a pgcli redis addon --redis-name / --redis-dsn; for stores lacking conditional-put, e.g. silo), or none (no fencing — single writer only, unsafe if anything else touches the bucket)")
	addonInstallCmd.Flags().Int("server-cache-gb", 0, "zerofs: server-side disk cache size in GiB (default 100)")
	addonInstallCmd.Flags().String("encryption-password", "", "zerofs: encryption password (default: generated). Required to re-attach a bucket whose DEK was wrapped by a previously generated password — pg addon password zerofs recovers it")
}
