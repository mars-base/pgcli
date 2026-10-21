package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/hostbin"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/podman"
)

// ---------------------------------------------------------------------------
// juicefs — pgcli's first HOST-BINARY addon (FUSE filesystem, not a container)
// ---------------------------------------------------------------------------
//
// One config entry (addons.juicefs.<name>) is a host juicefs client mount: a
// POSIX directory backed by a pgcli S3 addon (minio/silo) with metadata in one
// of four engines (sqlite3 file, a pgcli redis addon, a pgcli PG instance, or
// a caller --dsn URL). Unlike every container addon, the "service" is a process
// on this host: the binary is downloaded (GitHub release + pinned sha256) or
// reused from PATH, `format` initialises the volume once (it stores the S3
// credentials inside the metadata, so later mounts need only the meta URL), and
// `mount -d` self-daemonises. pgcli never touches the S3 bucket itself —
// provisioning it is manual `pg mc` work, so the install only reads the S3
// addon's stored credentials and, on a failure that smells like a missing
// bucket, prints the exact `pg mc mb` to run.
//
// FUSE needs /dev/fuse + fusermount3 on the host, so this is Linux-only (same
// refusal as BeeGFS); pgcli detects the prerequisite and tells you, it never
// installs system packages.

// validJuicefsVolumeName mirrors juicefs's own volume-name rule (see the
// format-time error): letters, digits and hyphens, 3-63 characters.
func validJuicefsVolumeName(s string) bool {
	if len(s) < 3 || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') {
			continue
		}
		return false
	}
	return true
}

// juicefsDownload resolves the release asset for the running linux arch.
func juicefsDownload(version string) (hostbin.Download, error) {
	if runtime.GOOS != "linux" {
		return hostbin.Download{}, fmt.Errorf("juicefs supports Linux only (host FUSE)")
	}
	arch := runtime.GOARCH
	sha := config.JuicefsSHA256(arch)
	if sha == "" {
		return hostbin.Download{}, fmt.Errorf("no pinned juicefs build for arch %s", arch)
	}
	v := strings.TrimPrefix(version, "v")
	return hostbin.Download{
		Binary: "juicefs",
		URL: fmt.Sprintf("https://github.com/juicedata/juicefs/releases/download/v%s/juicefs-%s-linux-%s.tar.gz",
			v, v, arch),
		Member: "juicefs",
		SHA256: sha,
	}, nil
}

// ensureJuicefsBinary locates or installs the host binary. It returns the path
// plus a user-facing note (empty unless the installer had to fall back off
// /usr/local/bin).
func ensureJuicefsBinary(version, installDir string) (string, string, error) {
	if p, ok := hostbin.Detect("juicefs"); ok {
		return p, "", nil
	}
	dl, err := juicefsDownload(version)
	if err != nil {
		return "", "", err
	}
	dir, note, err := hostbin.InstallDir(installDir)
	if err != nil {
		return "", "", err
	}
	fmt.Printf("-> Downloading juicefs %s (linux/%s)...\n", version, runtime.GOARCH)
	path, err := hostbin.Ensure(dl, dir)
	if err != nil {
		return "", "", err
	}
	return path, note, nil
}

// preflightFUSE fails fast when the host cannot mount. pgcli never installs the
// fuse3 package itself — it reports what is missing.
func preflightFUSE() error {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		return fmt.Errorf("/dev/fuse not present — FUSE kernel support is required to mount")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		return fmt.Errorf("fusermount3 not found on PATH — install the fuse3 package (e.g. apt install fuse3)")
	}
	return nil
}

// resolveJuicefsMeta turns the meta-engine selection into the concrete juicefs
// metadata URL, performing the engine's setup side effects (create the PG
// metadata database, create the sqlite3 parent directory). The result is stored
// in JuicefsConfig.MetaDSN for every engine — mount/umount/status need only it.
func resolveJuicefsMeta(cfg *config.Config, jc *config.JuicefsConfig) (string, error) {
	switch jc.MetaEngine {
	case config.JuicefsMetaSQLite3:
		if err := os.MkdirAll(filepath.Dir(jc.MetaDBPath), 0o755); err != nil {
			return "", fmt.Errorf("creating meta dir: %w", err)
		}
		return "sqlite3://" + jc.MetaDBPath, nil
	case config.JuicefsMetaRedis:
		return redisMetaURL(cfg, jc.RedisName)
	case config.JuicefsMetaPG:
		return pgMetaURL(cfg, jc)
	case config.JuicefsMetaDSN:
		if jc.MetaDSN == "" {
			return "", fmt.Errorf("meta engine dsn needs a --dsn URL")
		}
		return jc.MetaDSN, nil
	}
	return "", fmt.Errorf("unknown meta engine %q", jc.MetaEngine)
}

// redisMetaURL points juicefs at a pgcli redis addon. pgcli does NOT auto-install
// redis: the addon must already exist, so creating it stays an explicit choice.
func redisMetaURL(cfg *config.Config, redisName string) (string, error) {
	rc, ok := cfg.Addons.Redis[redisName]
	if !ok {
		return "", fmt.Errorf("redis addon %q not installed — run `pg addon install redis --name %s` first", redisName, redisName)
	}
	if rc.Password == "" {
		return "", fmt.Errorf("redis addon %q has no password in config — check pg.yaml", redisName)
	}
	host := rc.Listen
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("redis://:%s@%s:%d/0", rc.Password, host, rc.Port), nil
}

// pgMetaURL resolves a pgcli PG instance, creates the metadata database if
// missing (juicefs format refuses a nonexistent db), and returns the URL
// pointed at that db.
func pgMetaURL(cfg *config.Config, jc *config.JuicefsConfig) (string, error) {
	pc := *cfg
	if err := pc.SetInstance(jc.PgName); err != nil {
		return "", fmt.Errorf("resolving PG instance %q: %w", jc.PgName, err)
	}
	pm, err := podman.New(&pc)
	if err != nil {
		return "", err
	}
	if err := pm.CheckContainerRunning(); err != nil {
		return "", fmt.Errorf("PG instance %q is not running — start it: pg start %s", jc.PgName, jc.PgName)
	}
	base := podman.MetaEngineURL(pc.GetPostgresURL())
	if err := pm.EnsureMetaDB(base, jc.PgDBName); err != nil {
		return "", err
	}
	return swapDSNDatabase(base, jc.PgDBName), nil
}

// swapDSNDatabase replaces the path (database) component of a postgres:// URL,
// preserving query params like sslmode.
func swapDSNDatabase(dsn, dbname string) string {
	base, q := dsn, ""
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		base, q = dsn[:i], dsn[i:]
	}
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[:i+1]
	} else {
		base += "/"
	}
	return base + dbname + q
}

// runAddonInstallJuicefs implements `pg addon install juicefs` per the plan:
// binary → FUSE → S3 target → meta engine → save config → format → mount.
// The config entry is saved before any external command runs, so a crash
// mid-install leaves a re-runnable state (format skips when the volume
// already exists).
func runAddonInstallJuicefs(cmd *cobra.Command) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("the juicefs add-on runs a host FUSE mount and is Linux-only (like beegfs)")
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "juicefs"
	}
	// juicefs itself rejects names outside ^[a-z0-9-]{3,63}$ at format time;
	// failing here says so before anything is downloaded or written.
	if !validJuicefsVolumeName(name) {
		return fmt.Errorf("volume name %q is invalid for juicefs: 3-63 lowercase letters, digits and hyphens", name)
	}
	fmt.Printf("=== pg addon install juicefs (%s) ===\n", name)

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
	if c.Addons.Juicefs == nil {
		c.Addons.Juicefs = make(map[string]config.JuicefsConfig)
	}
	jc, present := c.Addons.Juicefs[name]
	if present {
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			fmt.Printf("✓ juicefs %q already installed (re-install with --force to rebuild the entry from flags; mount state: pg addon juicefs status --name %s)\n", name, name)
			return nil
		}
		jc = config.JuicefsConfig{} // --force rebuilds from flags, not stale secrets
	}
	jc.Name = name

	// --- flags over entry -------------------------------------------------
	fs := cmd.Flags()
	if fs.Changed("version") {
		jc.Version, _ = fs.GetString("version")
	}
	if jc.Version == "" {
		jc.Version = config.DefaultJuicefsVersion
	}
	metaEngine, _ := fs.GetString("meta")
	dsnFlag, _ := fs.GetString("dsn")
	metaDSN, _ := fs.GetString("meta-dsn")
	redisDSN, _ := fs.GetString("redis-dsn")
	if v, _ := fs.GetString("redis-name"); v != "" {
		jc.RedisName = v
	}
	if v, _ := fs.GetString("pg-name"); v != "" {
		jc.PgName = v
	}
	if v, _ := fs.GetString("s3"); v != "" {
		jc.S3Addon = v
	}
	if jc.S3Addon == "" {
		jc.S3Addon = "minio"
	}
	if v, _ := fs.GetString("s3-name"); v != "" {
		jc.S3Name = v
	}
	for _, s := range []struct {
		flag string
		dst  *string
	}{
		{"bucket", &jc.Bucket}, {"access-key", &jc.AccessKey}, {"secret-key", &jc.SecretKey},
		{"mount-point", &jc.MountPoint}, {"cache-dir", &jc.CacheDir},
	} {
		if v, _ := fs.GetString(s.flag); v != "" {
			*s.dst = v
		}
	}

	// Engine: an explicit --dsn forces the dsn engine (it is the user-visible
	// "point me at any URL" knob); --meta-dsn is its alias. --redis-dsn pins
	// the redis-engine URL instead of resolving --redis-name.
	switch {
	case dsnFlag != "" && metaDSN != "" && dsnFlag != metaDSN:
		return fmt.Errorf("--dsn and --meta-dsn disagree; pass one")
	case dsnFlag != "":
		metaEngine = config.JuicefsMetaDSN
		jc.MetaDSN = dsnFlag
	case metaDSN != "":
		metaEngine = config.JuicefsMetaDSN
		jc.MetaDSN = metaDSN
	}
	if redisDSN != "" && metaEngine != "" && metaEngine != config.JuicefsMetaRedis {
		return fmt.Errorf("--redis-dsn is only valid with --meta redis")
	}
	if metaEngine == "" {
		metaEngine = config.JuicefsMetaSQLite3
	}
	if !config.ValidJuicefsMetaEngine(metaEngine) {
		return fmt.Errorf("--meta %q is not one of sqlite3, redis, pg, dsn", metaEngine)
	}
	jc.MetaEngine = metaEngine

	// Normalise host paths/defaults, then re-read the merged entry (defaults
	// are applied to the map, so the draft must be stored first).
	c.Addons.Juicefs[name] = jc
	c.ApplyDefaults()
	jc = c.Addons.Juicefs[name]
	if redisDSN != "" {
		jc.MetaDSN = redisDSN // --meta redis + explicit URL skips addon lookup
	}

	installDir, _ := fs.GetString("install-dir")
	noMount, _ := fs.GetBool("no-mount")

	// --- 1. binary ---------------------------------------------------------
	bin, note, err := ensureJuicefsBinary(jc.Version, installDir)
	if err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	jc.BinaryPath = bin
	fmt.Printf("  [OK] juicefs binary: %s\n", bin)
	if note != "" {
		fmt.Printf("  [!] %s\n", note)
	}

	// --- 2. FUSE preflight -------------------------------------------------
	if err := preflightFUSE(); err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Println("  [OK] FUSE available (/dev/fuse, fusermount3)")

	// --- 3. S3 backend (read-only: pgcli never runs mc) -------------------
	tgt, err := resolveS3Target(c, jc.S3Addon, jc.S3Name)
	if err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] S3 backend: %s addon %q at %s\n", tgt.Kind, tgt.Name, tgt.BaseURL)
	// Credentials: an explicit pair is honoured verbatim (an operator-scoped
	// service account made with `pg mc`); otherwise the S3 addon's own root
	// creds are stored. Keyed on Changed(), not emptiness — ApplyDefaults has
	// already filled the bucket-name access-key default.
	akSet, skSet := fs.Changed("access-key"), fs.Changed("secret-key")
	switch {
	case akSet != skSet:
		return fmt.Errorf("  [X] --access-key and --secret-key must be given together (or neither, to use the %s root credentials)", tgt.Kind)
	case akSet:
		fmt.Printf("  [OK] credentials: explicit access-key %q\n", jc.AccessKey)
	default:
		jc.AccessKey, jc.SecretKey = tgt.User, tgt.Password
		fmt.Printf("  [OK] credentials: %s addon root (%s)\n", tgt.Kind, tgt.User)
	}

	// --- 4. metadata engine ------------------------------------------------
	metaURL, err := resolveJuicefsMeta(c, &jc)
	if err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	jc.MetaDSN = metaURL
	fmt.Printf("  [OK] metadata engine (%s): %s\n", jc.MetaEngine, dsnDisplay(metaURL))

	// --- 5. persist BEFORE anything external --------------------------------
	c.Addons.Juicefs[name] = jc
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	// --- 6. format (skip when the volume already exists) --------------------
	formatArgs := []string{"format", "--storage", "s3",
		"--bucket", fmt.Sprintf("%s/%s", tgt.BaseURL, jc.Bucket),
		"--access-key", jc.AccessKey, "--secret-key", jc.SecretKey,
		metaURL, name}
	formatted, err := juicefsVolumeExists(bin, metaURL)
	if err != nil {
		fmt.Printf("  [!] existing-volume probe inconclusive (%v) — attempting format anyway\n", err)
	}
	if formatted {
		fmt.Println("  [OK] volume already formatted — skipping format")
	} else if err := runJuicefsQuiet(bin, formatArgs...); err != nil {
		fmt.Printf("  [X] format failed: %v\n", err)
		printBucketHint(tgt, jc.Bucket)
		return fmt.Errorf("format volume %q", name)
	} else {
		fmt.Printf("  [OK] formatted volume %q (bucket %s)\n", name, jc.Bucket)
	}

	// --- 7. mount ----------------------------------------------------------
	if noMount {
		fmt.Printf("  [i] --no-mount: not mounting. Mount later: pg addon juicefs mount --name %s\n", name)
	} else {
		if err := juicefsMount(bin, &jc); err != nil {
			fmt.Printf("  [X] mount failed: %v\n", err)
			printBucketHint(tgt, jc.Bucket)
			return fmt.Errorf("mount %q", name)
		}
		fmt.Printf("  [OK] mounted at %s\n", jc.MountPoint)
	}

	fmt.Println()
	fmt.Printf("✓ juicefs installed: %q\n", name)
	fmt.Printf("  Volume:      %s (engine %s)\n", name, jc.MetaEngine)
	fmt.Printf("  Metadata:    %s\n", dsnDisplay(jc.MetaDSN))
	fmt.Printf("  S3:          %s addon %q, bucket %s\n", tgt.Kind, tgt.Name, jc.Bucket)
	fmt.Printf("  Mount point: %s\n", jc.MountPoint)
	fmt.Printf("  Cache:       %s\n", jc.CacheDir)
	fmt.Println()
	fmt.Printf("  pgcli does not create buckets. If the volume is not yet usable,\n")
	fmt.Printf("  provision the bucket manually (once):\n")
	printBucketHint(tgt, jc.Bucket)
	fmt.Println()
	fmt.Printf("  Manage: pg addon juicefs {status|umount|mount} --name %s\n", name)
	fmt.Printf("  Remove: pg addon remove juicefs --name %s\n", name)
	return nil
}

// printBucketHint renders the exact manual `pg mc` commands to provision the
// bucket — pgcli itself never runs them.
func printBucketHint(tgt *s3Target, bucket string) {
	alias := "pgcli-" + tgt.Kind
	fmt.Printf("      pg mc alias set %s %s %s '***'\n", alias, tgt.BaseURL, tgt.User)
	fmt.Printf("      pg mc mb %s/%s\n", alias, bucket)
}

// juicefsMount creates the mount/cache dirs and self-daemonises a client.
func juicefsMount(bin string, jc *config.JuicefsConfig) error {
	if err := os.MkdirAll(jc.MountPoint, 0o755); err != nil {
		return fmt.Errorf("creating mount point %s: %w", jc.MountPoint, err)
	}
	if err := os.MkdirAll(jc.CacheDir, 0o755); err != nil {
		return fmt.Errorf("creating cache dir %s: %w", jc.CacheDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(jc.MountLogFile), 0o755); err != nil {
		return fmt.Errorf("creating log dir: %w", err)
	}
	return runJuicefsQuiet(bin, "mount", jc.MetaDSN, jc.MountPoint, "-d",
		"--log", jc.MountLogFile, "--cache-dir", jc.CacheDir)
}

// juicefsVolumeExists asks the binary whether the metadata already describes a
// formatted volume. `juicefs config <meta>` exits 0 only for a readable
// volume; an empty meta engine (fresh sqlite3 file / empty db) exits nonzero.
func juicefsVolumeExists(bin, metaURL string) (bool, error) {
	out, err := exec.Command(bin, "config", metaURL).CombinedOutput()
	if err == nil {
		return true, nil
	}
	low := strings.ToLower(string(out))
	// Definitive "no volume here" answers — anything else is ambiguous and the
	// caller should try format (it is the real failure surface, with output).
	for _, sig := range []string{"database is not formatted", "not a valid", "no such", "does not exist", "use `juicefs format`"} {
		if strings.Contains(low, sig) {
			return false, nil
		}
	}
	return false, fmt.Errorf("%v: %s", err, strings.TrimSpace(low))
}

// runJuicefsQuiet runs the binary capturing combined output; errors surface the
// output so callers print one authoritative failure block.
func runJuicefsQuiet(bin string, args ...string) error {
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%v: %s", err, truncateRunes(msg, 600))
		}
		return err
	}
	return nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// --- shared host-addon plumbing ---------------------------------------------

// resolveHostAddon loads config and the named juicefs entry (default "juicefs"),
// with the same find-or-complain shape as resolveBeeGFSConfig.
func resolveHostAddon(cmd *cobra.Command) (*config.Config, string, config.JuicefsConfig, error) {
	c, _, err := loadAddonCfg()
	if err != nil {
		return nil, "", config.JuicefsConfig{}, err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "juicefs"
	}
	if c.Addons.Juicefs == nil {
		return nil, "", config.JuicefsConfig{}, fmt.Errorf("no juicefs add-ons configured (run 'pg addon install juicefs')")
	}
	if _, ok := c.Addons.Juicefs[name]; !ok {
		return nil, "", config.JuicefsConfig{}, fmt.Errorf("juicefs volume %q not found (see 'pg addon list')", name)
	}
	c.ApplyDefaults()
	return c, name, c.Addons.Juicefs[name], nil
}

// isMountPoint checks /proc/mounts for an exact mount-point match.
func isMountPoint(dir string) bool {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) >= 2 && fields[1] == dir {
			return true
		}
	}
	return false
}

func runJuicefsMount(cmd *cobra.Command, _ []string) error {
	_, name, jc, err := resolveHostAddon(cmd)
	if err != nil {
		return err
	}
	if isMountPoint(jc.MountPoint) {
		fmt.Printf("✓ juicefs %q already mounted at %s\n", name, jc.MountPoint)
		return nil
	}
	if err := juicefsMount(jc.BinaryPath, &jc); err != nil {
		return fmt.Errorf("mounting %q: %w", name, err)
	}
	fmt.Printf("✓ juicefs %q mounted at %s\n", name, jc.MountPoint)
	return nil
}

func runJuicefsUmount(cmd *cobra.Command, _ []string) error {
	_, name, jc, err := resolveHostAddon(cmd)
	if err != nil {
		return err
	}
	if !isMountPoint(jc.MountPoint) {
		fmt.Printf("juicefs %q is not mounted\n", name)
		return nil
	}
	if err := runJuicefsQuiet(jc.BinaryPath, "umount", jc.MountPoint); err != nil {
		return fmt.Errorf("unmounting %q: %w", name, err)
	}
	fmt.Printf("✓ juicefs %q unmounted from %s\n", name, jc.MountPoint)
	return nil
}

func runJuicefsStatus(cmd *cobra.Command, _ []string) error {
	_, name, jc, err := resolveHostAddon(cmd)
	if err != nil {
		return err
	}
	state := "not mounted"
	if isMountPoint(jc.MountPoint) {
		state = "mounted"
	}
	fmt.Printf("juicefs %q — %s\n", name, state)
	fmt.Printf("  Metadata:  %s (%s engine)\n", dsnDisplay(jc.MetaDSN), jc.MetaEngine)
	fmt.Printf("  S3:        %s addon %q, bucket %s\n", jc.S3Addon, jc.S3Name, jc.Bucket)
	fmt.Printf("  Binary:    %s\n", jc.BinaryPath)
	fmt.Printf("  Mount:     %s\n", jc.MountPoint)
	fmt.Printf("  Cache:     %s\n", jc.CacheDir)
	fmt.Printf("  Log:       %s\n", jc.MountLogFile)
	return nil
}

// runAddonRemoveJuicefs tears down the host mount and forgets the entry. Like
// every other addon it deletes NO data: bucket, metadata store, cache and the
// binary all survive; --clean-data additionally removes the per-instance
// <base>/addon/juicefs/<name> dir (sqlite meta + cache + logs) — still never
// the S3 bucket and never a PG metadata db, which pgcli refuses to drop
// speculatively (it prints the statement instead).
func runAddonRemoveJuicefs(cmd *cobra.Command) error {
	c, path, err := loadAddonCfg()
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "juicefs"
	}
	cleanData, _ := cmd.Flags().GetBool("clean-data")
	if _, ok := c.Addons.Juicefs[name]; !ok {
		return fmt.Errorf("juicefs volume %q not found (see 'pg addon list')", name)
	}
	c.ApplyDefaults()
	jc := c.Addons.Juicefs[name]
	fmt.Printf("=== pg addon remove juicefs (%s) ===\n", name)

	if isMountPoint(jc.MountPoint) {
		if err := runJuicefsQuiet(jc.BinaryPath, "umount", jc.MountPoint); err != nil {
			return fmt.Errorf("  [X] refusing to forget a still-mounted volume: %v", err)
		}
		fmt.Printf("  [OK] unmounted %s\n", jc.MountPoint)
	}

	delete(c.Addons.Juicefs, name)
	if len(c.Addons.Juicefs) == 0 {
		c.Addons.Juicefs = nil
	}
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [OK] config entry removed (%s)\n", path)

	// The per-instance dir is the parent of the defaulted absolute mount point
	// (c.BaseDir may be unset; ApplyDefaults already resolved paths against the
	// platform default).
	addonDir := filepath.Dir(jc.MountPoint)
	if cleanData {
		if err := os.RemoveAll(addonDir); err != nil {
			return fmt.Errorf("cleaning %s: %w", addonDir, err)
		}
		fmt.Printf("  [OK] cleaned %s (cache, logs, sqlite meta)\n", addonDir)
	} else {
		fmt.Printf("  [i] kept %s (cache/meta/logs) and the %s bucket — pass --clean-data to drop the host dir; the S3 data and any PG metadata db are never deleted automatically\n", addonDir, jc.Bucket)
	}
	if jc.MetaEngine == config.JuicefsMetaPG {
		fmt.Printf("  [i] metadata db %q on instance %q was left in place — drop it manually if you are sure:\n      psql ... -c 'DROP DATABASE %s'\n", jc.PgDBName, jc.PgName, jc.PgDBName)
	}
	fmt.Printf("✓ juicefs %q removed\n", name)
	return nil
}

// --- nested `pg addon juicefs` tree + registration ---------------------------

var addonJuicefsCmd = &cobra.Command{
	Use:   "juicefs",
	Short: "Manage juicefs host-mount add-on instances",
	Long: `Manage juicefs add-on instances: host FUSE mounts backed by a pgcli S3 addon.

Subcommands:
  mount    Mount a formatted juicefs volume on this host
  umount   Unmount it (the client daemon exits; volume, bucket and metadata stay)
  status   Show backend, metadata engine, binary and mount state

Examples:
  pg addon install juicefs --name data --s3 minio --s3-name ms3 --meta pg --pg-name default
  pg addon juicefs status --name data
  pg addon juicefs umount --name data`,
}

var addonJuicefsMountCmd = &cobra.Command{
	Use:   "mount",
	Short: "Mount an installed juicefs volume",
	RunE:  runJuicefsMount,
}

var addonJuicefsUmountCmd = &cobra.Command{
	Use:   "umount",
	Short: "Unmount an installed juicefs volume",
	RunE:  runJuicefsUmount,
}

var addonJuicefsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show juicefs volume status",
	RunE:  runJuicefsStatus,
}

// runAddonStartJuicefs / runAddonStopJuicefs wire the top-level
// `pg addon start|stop juicefs` to the nested mount/umount implementations so
// both spellings behave identically (container addons start by name; this
// host addon mounts by name).
func runAddonStartJuicefs(cmd *cobra.Command) error { return runJuicefsMount(cmd, nil) }
func runAddonStopJuicefs(cmd *cobra.Command) error  { return runJuicefsUmount(cmd, nil) }

func init() {
	addonCmd.AddCommand(addonJuicefsCmd)
	addonJuicefsCmd.AddCommand(addonJuicefsMountCmd, addonJuicefsUmountCmd, addonJuicefsStatusCmd)
	for _, c := range []*cobra.Command{addonJuicefsMountCmd, addonJuicefsUmountCmd, addonJuicefsStatusCmd} {
		c.Flags().String("name", "", "juicefs volume name (default \"juicefs\")")
	}

	// Install-time knobs shared with zerofs (see addon_zerofs.go). --clean-data
	// for remove is already registered on addonRemoveCmd in addon.go.
	addonInstallCmd.Flags().String("meta", "", "juicefs: metadata engine — sqlite3 (default), redis, pg, or dsn (--dsn/--meta-dsn implies dsn)")
	addonInstallCmd.Flags().String("meta-dsn", "", "juicefs --meta dsn: the metadata URL itself (postgres://…, redis://…, sqlite3://…); alias of --dsn for this addon")
	addonInstallCmd.Flags().String("redis-name", "", "juicefs --meta redis / zerofs conditional-put redis: pgcli redis addon name (default \"redis\"; must already be installed)")
	addonInstallCmd.Flags().String("redis-dsn", "", "explicit redis:// URL instead of resolving --redis-name (juicefs meta / zerofs conditional-put backend)")
	addonInstallCmd.Flags().String("s3", "minio", "juicefs/zerofs: S3 backend kind — minio (default) or silo")
	addonInstallCmd.Flags().String("s3-name", "", "juicefs/zerofs: minio/silo addon instance backing the store (default = the --s3 kind name)")
	addonInstallCmd.Flags().String("access-key", "", "juicefs/zerofs: S3 access key (default: the backend addon's root user)")
	addonInstallCmd.Flags().String("secret-key", "", "juicefs/zerofs: S3 secret key (default: the backend addon's root password)")
	addonInstallCmd.Flags().String("mount-point", "", "juicefs/zerofs: host mount directory (default <base_dir>/addon/<family>/<name>/mnt)")
	addonInstallCmd.Flags().String("cache-dir", "", "juicefs/zerofs: local cache directory (default <base_dir>/addon/<family>/<name>/cache)")
	addonInstallCmd.Flags().String("install-dir", "", "juicefs/zerofs: where to place the host binary (default /usr/local/bin, else ~/.local/bin)")
	addonInstallCmd.Flags().Bool("no-mount", false, "juicefs/zerofs: set everything up but leave the filesystem unmounted (mount later with `pg addon juicefs mount`)")
}
