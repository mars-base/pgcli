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
)

// ---------------------------------------------------------------------------
// juicefs — pgcli's first HOST-BINARY addon (FUSE filesystem, not a container)
// ---------------------------------------------------------------------------
//
// One config entry (addons.juicefs.<name>) is a host juicefs client mount: a
// POSIX directory backed by an EXTERNAL S3 service (cloud or remote-host —
// the normal case is that the S3 provider and this client are different
// machines), with metadata in an explicitly-supplied engine URL (--dsn:
// sqlite3://, redis://, postgres://…). There is deliberately no same-host
// addon auto-detection: endpoint, credentials and metadata URL all come from
// the command line and are stored as given. The "service" is a process on this
// host: the binary is downloaded (GitHub release + pinned sha256) or reused
// from PATH, `format` initialises the volume once (it stores the S3
// credentials inside the metadata, so later mounts need only the meta URL),
// and `mount -d` self-daemonises. pgcli never touches the S3 bucket itself —
// provisioning it is manual `pg mc` work, so the install only probes the
// endpoint and, on a failure that smells like a missing bucket, prints the
// exact `pg mc mb` to run.
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

// runAddonInstallJuicefs implements `pg addon install juicefs`:
// binary → FUSE → external S3 endpoint → explicit meta DSN → save config →
// format → mount. The config entry is saved before any external command runs,
// so a crash mid-install leaves a re-runnable state (format skips when the
// volume already exists).
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
	// Everything that identifies the volume is explicit — there is no
	// same-host fallback for the S3 endpoint, the credentials, or the
	// metadata URL. --dsn is the metadata URL; --meta-dsn is its alias.
	if v, _ := fs.GetString("s3-endpoint"); v != "" {
		jc.S3Endpoint = v
	}
	dsnFlag, _ := fs.GetString("dsn")
	metaDSN, _ := fs.GetString("meta-dsn")
	switch {
	case dsnFlag != "" && metaDSN != "" && dsnFlag != metaDSN:
		return fmt.Errorf("--dsn and --meta-dsn disagree; pass one")
	case dsnFlag != "":
		jc.MetaDSN = dsnFlag
	case metaDSN != "":
		jc.MetaDSN = metaDSN
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
	akSet, skSet := fs.Changed("access-key"), fs.Changed("secret-key")
	if akSet != skSet {
		return fmt.Errorf("--access-key and --secret-key must be given together — an external S3 service has no local addon root credentials to inherit")
	}
	var missing []string
	if jc.S3Endpoint == "" {
		missing = append(missing, "--s3-endpoint http(s)://host[:port]")
	}
	if jc.AccessKey == "" || jc.SecretKey == "" {
		missing = append(missing, "--access-key and --secret-key")
	}
	if jc.MetaDSN == "" {
		missing = append(missing, "--dsn <metadata URL: sqlite3://…, redis://…, postgres://…>")
	}
	if len(missing) > 0 {
		return fmt.Errorf("juicefs mounts an EXTERNAL S3 service with an EXPLICIT metadata engine — missing %s (see pg addon install juicefs -h)", strings.Join(missing, ", "))
	}
	if err := config.ValidateJuicefsDSNScheme(jc.MetaDSN); err != nil {
		return fmt.Errorf("%w", err)
	}

	// Normalise host paths/defaults, then re-read the merged entry (defaults
	// are applied to the map, so the draft must be stored first).
	c.Addons.Juicefs[name] = jc
	c.ApplyDefaults()
	jc = c.Addons.Juicefs[name]

	// The mount-point contract, checked before any download or format: the
	// operator creates the directory (mkdir -p) with read-write access for the
	// user running pg — pgcli never creates or chowns it.
	if err := checkMountPoint(jc.MountPoint); err != nil {
		return fmt.Errorf("  [X] %v", err)
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

	// --- 3. external S3 endpoint (read-only: pgcli never runs mc) ---------
	tgt, err := externalS3Target(jc.S3Endpoint)
	if err != nil {
		return fmt.Errorf("  [X] %v", err)
	}
	fmt.Printf("  [OK] S3 endpoint: %s (TLS: %v)\n", tgt.BaseURL, tgt.TLS)
	fmt.Printf("  [OK] credentials: explicit access-key %q\n", jc.AccessKey)

	// --- 4. metadata engine (explicit URL, verified reachable by format) ---
	fmt.Printf("  [OK] metadata engine: %s\n", dsnDisplay(jc.MetaDSN))

	// --- 5. persist BEFORE anything external --------------------------------
	c.Addons.Juicefs[name] = jc
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	// --- 6. format (skip when the volume already exists) --------------------
	formatArgs := []string{"format", "--storage", "s3",
		"--bucket", fmt.Sprintf("%s/%s", tgt.BaseURL, jc.Bucket),
		"--access-key", jc.AccessKey, "--secret-key", jc.SecretKey,
		jc.MetaDSN, name}
	formatted, err := juicefsVolumeExists(bin, jc.MetaDSN)
	if err != nil {
		fmt.Printf("  [!] existing-volume probe inconclusive (%v) — attempting format anyway\n", err)
	}
	if formatted {
		fmt.Println("  [OK] volume already formatted — skipping format")
	} else if err := runJuicefsQuiet(bin, formatArgs...); err != nil {
		fmt.Printf("  [X] format failed: %v\n", err)
		printBucketHint(tgt, jc.AccessKey, jc.Bucket)
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
			printBucketHint(tgt, jc.AccessKey, jc.Bucket)
			return fmt.Errorf("mount %q", name)
		}
		fmt.Printf("  [OK] mounted at %s\n", jc.MountPoint)
	}

	fmt.Println()
	fmt.Printf("✓ juicefs installed: %q\n", name)
	fmt.Printf("  Volume:      %s\n", name)
	fmt.Printf("  Metadata:    %s\n", dsnDisplay(jc.MetaDSN))
	fmt.Printf("  S3:          %s, bucket %s\n", tgt.BaseURL, jc.Bucket)
	fmt.Printf("  Mount point: %s\n", jc.MountPoint)
	fmt.Printf("  Cache:       %s\n", jc.CacheDir)
	fmt.Println()
	fmt.Printf("  pgcli does not create buckets. If the volume is not yet usable,\n")
	fmt.Printf("  provision the bucket manually (once):\n")
	printBucketHint(tgt, jc.AccessKey, jc.Bucket)
	fmt.Println()
	fmt.Printf("  Manage: pg addon juicefs {status|umount|mount} --name %s\n", name)
	fmt.Printf("  Remove: pg addon remove juicefs --name %s\n", name)
	return nil
}

// printBucketHint renders the exact manual `pg mc` commands to provision the
// bucket — pgcli itself never runs them. The alias is generic ("externals3")
// because the store is not a pgcli addon; the secret stays behind '***'.
func printBucketHint(tgt *s3Target, accessKey, bucket string) {
	fmt.Printf("      pg mc alias set externals3 %s %s '***'\n", tgt.BaseURL, accessKey)
	fmt.Printf("      pg mc mb externals3/%s\n", bucket)
}

// juicefsMount self-daemonises a client on the (operator-created) mount point.
// The cache and log dirs are pgcli's own bookkeeping under the addon dir, so
// pgcli creates those — but never the mount point itself: every call path
// (mount command, install, autostart) enforces the contract here, so a
// directory removed since install fails with the mkdir -p remedy, not a
// cryptic juicefs exit.
func juicefsMount(bin string, jc *config.JuicefsConfig) error {
	if err := checkMountPoint(jc.MountPoint); err != nil {
		return err
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
// with the same find-or-complain shape as resolveBeeGFSConfig. It also returns
// the config file path so `mount --mount-point` can persist a relocated path.
func resolveHostAddon(cmd *cobra.Command) (*config.Config, string, string, config.JuicefsConfig, error) {
	c, path, err := loadAddonCfg()
	if err != nil {
		return nil, "", "", config.JuicefsConfig{}, err
	}
	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		name = "juicefs"
	}
	if c.Addons.Juicefs == nil {
		return nil, "", "", config.JuicefsConfig{}, fmt.Errorf("no juicefs add-ons configured (run 'pg addon install juicefs')")
	}
	if _, ok := c.Addons.Juicefs[name]; !ok {
		return nil, "", "", config.JuicefsConfig{}, fmt.Errorf("juicefs volume %q not found (see 'pg addon list')", name)
	}
	c.ApplyDefaults()
	return c, path, name, c.Addons.Juicefs[name], nil
}

// mountOverrideJuicefs applies a `mount --mount-point` flag: it relocates the
// volume's mount path (persisted to pg.yaml) after refusing to move a volume
// that is still mounted at its old path. The flag is only registered on the
// nested mount command, so the top-level `pg addon start juicefs` alias (which
// shares this RunE via addonStartCmd) passes the Lookup guard with an empty
// flag and is a no-op. Returns the effective mount point.
func mountOverrideJuicefs(c *config.Config, path, name string, jc *config.JuicefsConfig, cmd *cobra.Command) error {
	if f := cmd.Flags().Lookup("mount-point"); f == nil {
		return nil // top-level start alias: no flag, keep the stored path
	}
	want, _ := cmd.Flags().GetString("mount-point")
	if want == "" || want == jc.MountPoint {
		return nil
	}
	if isMountPoint(jc.MountPoint) {
		return fmt.Errorf("juicefs %q is still mounted at %s — umount first: pg addon juicefs umount --name %s", name, jc.MountPoint, name)
	}
	if err := checkMountPoint(want); err != nil {
		return err
	}
	jc.MountPoint = want
	c.Addons.Juicefs[name] = *jc
	if err := c.Save(path); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Printf("  [i] mount point moved to %s (saved)\n", want)
	return nil
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
	c, path, name, jc, err := resolveHostAddon(cmd)
	if err != nil {
		return err
	}
	if err := mountOverrideJuicefs(c, path, name, &jc, cmd); err != nil {
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
	_, _, name, jc, err := resolveHostAddon(cmd)
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
	_, _, name, jc, err := resolveHostAddon(cmd)
	if err != nil {
		return err
	}
	state := "not mounted"
	if isMountPoint(jc.MountPoint) {
		state = "mounted"
	}
	fmt.Printf("juicefs %q — %s\n", name, state)
	fmt.Printf("  Metadata:  %s\n", dsnDisplay(jc.MetaDSN))
	fmt.Printf("  S3:        %s, bucket %s\n", jc.S3Endpoint, jc.Bucket)
	fmt.Printf("  Binary:    %s\n", jc.BinaryPath)
	fmt.Printf("  Mount:     %s\n", jc.MountPoint)
	fmt.Printf("  Cache:     %s\n", jc.CacheDir)
	fmt.Printf("  Log:       %s\n", jc.MountLogFile)
	return nil
}

// runAddonRemoveJuicefs tears down the host mount and forgets the entry. Like
// every other addon it deletes NO data: bucket, metadata store, cache and the
// binary all survive; --clean-data additionally removes the per-instance
// <base>/addon/juicefs/<name> dir (cache + logs) — still never the S3 bucket,
// which pgcli refuses to touch speculatively.
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

	// The per-instance dir lives at <base>/addon/juicefs/<name> regardless of
	// where the (possibly relocated or custom) mount point points.
	addonDir := hostAddonDir(c, "juicefs", name)
	if cleanData {
		if err := os.RemoveAll(addonDir); err != nil {
			return fmt.Errorf("cleaning %s: %w", addonDir, err)
		}
		fmt.Printf("  [OK] cleaned %s (cache, logs)\n", addonDir)
	} else {
		fmt.Printf("  [i] kept %s (cache/logs), the metadata store and the %s bucket — pass --clean-data to drop the host dir; object storage and the metadata engine are never deleted automatically\n", addonDir, jc.Bucket)
	}
	fmt.Printf("✓ juicefs %q removed\n", name)
	return nil
}

// --- nested `pg addon juicefs` tree + registration ---------------------------

var addonJuicefsCmd = &cobra.Command{
	Use:   "juicefs",
	Short: "Manage juicefs host-mount add-on instances",
	Long: `Manage juicefs add-on instances: host FUSE mounts backed by an external S3 service.

Subcommands:
  mount    Mount a formatted juicefs volume on this host (--mount-point to
           relocate it first — after umount, to a pre-created directory)
  umount   Unmount it (the client daemon exits; volume, bucket and metadata stay)
  status   Show backend, metadata URL, binary and mount state

Examples:
  pg addon install juicefs --name data --s3-endpoint https://s3.example.com --access-key AK --secret-key SK --bucket my-bucket --dsn "postgres://u:p@meta-host:5432/jfs?sslmode=disable"
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
	addonJuicefsMountCmd.Flags().String("mount-point", "", "mount at this path instead of the stored one (the directory must already exist: mkdir -p, writable for the user running pg) — persisted; refuses while still mounted at the old path")

	// Install-time knobs shared with zerofs (see addon_zerofs.go). --clean-data
	// for remove is already registered on addonRemoveCmd in addon.go.
	// --dsn (the metadata URL) is the shared addon-install flag (addon.go).
	addonInstallCmd.Flags().String("meta-dsn", "", "juicefs: the metadata-engine URL itself (postgres://…, redis://…, sqlite3://…) — required, there is no same-host engine auto-detection; alias of --dsn for this addon")
	addonInstallCmd.Flags().String("redis-dsn", "", "zerofs --conditional-put redis: the fencing backend redis:// URL (explicit — no same-host addon lookup)")
	addonInstallCmd.Flags().String("s3-endpoint", "", "juicefs/zerofs: EXTERNAL S3 service as http(s)://host[:port] (no bucket path) — required; the client and the object store normally live on different machines, and pgcli never auto-detects a same-host addon")
	addonInstallCmd.Flags().String("s3-region", "", "zerofs: SigV4 region for the external endpoint (default: zerofs's own us-east-1). JuiceFS needs none — it probes the endpoint at format")
	addonInstallCmd.Flags().String("access-key", "", "juicefs/zerofs: S3 access key — REQUIRED with --s3-endpoint (an external store has no local addon root to inherit)")
	addonInstallCmd.Flags().String("secret-key", "", "juicefs/zerofs: S3 secret key — REQUIRED with --s3-endpoint")
	addonInstallCmd.Flags().String("mount-point", "", "juicefs/zerofs: host mount directory (default <base_dir>/addon/<family>/<name>/mnt) — pgcli never creates it: mkdir -p it first, owned and writable by the user running pg; `pg addon juicefs|zerofs mount --mount-point` relocates a volume")
	addonInstallCmd.Flags().String("cache-dir", "", "juicefs/zerofs: local cache directory (default <base_dir>/addon/<family>/<name>/cache)")
	addonInstallCmd.Flags().String("install-dir", "", "juicefs/zerofs: where to place the host binary (default /usr/local/bin, else ~/.local/bin)")
	addonInstallCmd.Flags().Bool("no-mount", false, "juicefs/zerofs: set everything up but leave the filesystem unmounted (mount later with `pg addon juicefs mount`)")
}
