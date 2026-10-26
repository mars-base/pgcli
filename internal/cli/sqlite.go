package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/hostbin"
	"github.com/mars-base/pgcli/internal/platform"
	"github.com/mars-base/pgcli/internal/sqlite"
)

var (
	sqliteInstallDir      string
	sqliteForceLitestream bool
	sqliteCreatePath      string
	sqliteInfoIntegrity   string
	sqliteRemoveCleanData bool
	sqliteRemoveForce     bool
)

var sqliteCmd = &cobra.Command{
	Use:   "sqlite",
	Short: "Manage host-local SQLite databases and Litestream replication",
	Long: `Manage host-local SQLite databases with optional Litestream continuous
replication to S3-compatible object storage.

SQLite is Linux-only and runs directly on the host (not in a container).
All SQL goes through the sqlite3 CLI with WAL journaling enabled.

Common workflow:
  pg sqlite install
  pg sqlite create appdb
  pg sqlite exec appdb "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"
  pg sqlite replication enable appdb --s3-bucket backups ...
  pg sqlite replication status appdb
  pg sqlite replication restore appdb --target /tmp/appdb-restored.db

Databases live under <base>/sqlite/<name>/ by default; --path keeps the
database file elsewhere. S3 credentials are written only to a 0600
litestream.yml and are never stored in pg.yaml.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if platform.Detect() != platform.Linux {
			return fmt.Errorf("pg sqlite requires Linux")
		}
		if cmd.Root().PersistentFlags().Changed("output") {
			return fmt.Errorf("pg sqlite does not use --output: use --target for restore output paths")
		}
		return nil
	},
}

var sqliteInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install sqlite3 and Litestream 0.5.x host binaries",
	Args:  cobra.NoArgs,
	RunE:  runSQLiteInstall,
}

var sqliteCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Register a SQLite database and enable WAL",
	Args:  cobra.ExactArgs(1),
	RunE:  runSQLiteCreate,
}

var sqliteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List managed SQLite databases",
	Args:  cobra.NoArgs,
	RunE:  runSQLiteList,
}

var sqliteInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show database pragmas, size and replication state",
	Args:  cobra.ExactArgs(1),
	RunE:  runSQLiteInfo,
}

var sqliteShellCmd = &cobra.Command{
	Use:                "shell <name> [-- sqlite3 args...]",
	Short:              "Open an interactive sqlite3 shell",
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: true,
	RunE:               runSQLiteShell,
}

var sqliteExecCmd = &cobra.Command{
	Use:                "exec <name> <sql>",
	Short:              "Execute SQL (use \"-\" as SQL to read from stdin)",
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: true,
	RunE:               runSQLiteExec,
}

var sqliteRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Stop replication and unregister a database",
	Args:  cobra.ExactArgs(1),
	RunE:  runSQLiteRemove,
}

func init() {
	rootCmd.AddCommand(sqliteCmd)
	sqliteCmd.AddCommand(sqliteInstallCmd, sqliteCreateCmd, sqliteListCmd, sqliteInfoCmd,
		sqliteShellCmd, sqliteExecCmd, sqliteRemoveCmd)

	sqliteInstallCmd.Flags().StringVar(&sqliteInstallDir, "install-dir", "", "directory for the Litestream binary (default: auto)")
	sqliteInstallCmd.Flags().BoolVar(&sqliteForceLitestream, "force-litestream", false, "re-download Litestream even if a supported version exists")

	sqliteCreateCmd.Flags().StringVar(&sqliteCreatePath, "path", "", "absolute path for the database file (default <base>/sqlite/<name>/<name>.db)")

	sqliteInfoCmd.Flags().StringVar(&sqliteInfoIntegrity, "integrity", "quick", "integrity check: none, quick or full")

	sqliteRemoveCmd.Flags().BoolVar(&sqliteRemoveCleanData, "clean-data", false, "also delete the database file, WAL sidecars, local replication state and logs")
	sqliteRemoveCmd.Flags().BoolVar(&sqliteRemoveForce, "force", false, "skip the --clean-data confirmation")
}

// loadSQLiteConfig loads pg.yaml without selecting an instance; SQLite
// databases can be managed on a machine that has no PostgreSQL instances.
func loadSQLiteConfig() (*config.Config, string, error) {
	path := cfgPath
	if path == "" {
		path = platform.DefaultConfigPath()
	}
	cfgPath = path
	c, err := config.Load(path)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load config: %w", err)
	}
	return c, path, nil
}

func saveSQLiteConfig(c *config.Config, path string) error {
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return err
	}
	return c.Save(path)
}

func sqliteEntry(c *config.Config, name string) (config.SQLiteConfig, error) {
	if err := sqlite.ValidateName(name); err != nil {
		return config.SQLiteConfig{}, err
	}
	db, ok := c.Addons.SQLite[name]
	if !ok {
		return config.SQLiteConfig{}, fmt.Errorf("SQLite database %q is not registered — see \"pg sqlite list\"", name)
	}
	return db, nil
}

func sortedSQLiteNames(m map[string]config.SQLiteConfig) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sqliteLayout(c *config.Config, db config.SQLiteConfig) sqlite.Layout {
	return sqlite.NewLayout(sqlite.BaseDir(c), db)
}

// sqliteMustExist verifies that the database file is present before handing it
// to sqlite3 (sqlite3 would otherwise silently create an empty database).
func sqliteMustExist(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("database file %s does not exist — run \"pg sqlite create <name>\" or restore it", path)
		}
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("database path %s is a directory", path)
	}
	return nil
}

func runSQLiteInstall(cmd *cobra.Command, _ []string) error {
	fmt.Println("=== pg sqlite install ===")

	runner, err := sqlite.NewRunner()
	if err != nil {
		pm, ok := sqlite.DetectPackageManager()
		if !ok {
			return fmt.Errorf("sqlite3 is not installed and no supported package manager was found — install sqlite3 manually")
		}
		pkgCmd, _ := sqlite.SQLitePackageCommand(pm)
		prefix, err := sqlite.DetectPrivilege()
		if err != nil {
			return err
		}

		fmt.Printf("-> installing sqlite3 with %s...\n", pm)
		err = runSQLitePackageCommand(prefix, pkgCmd.Name, pkgCmd.Args)
		if err != nil && pm == "apt-get" {
			fmt.Println("  [!] install failed, refreshing package lists and retrying...")
			if updateErr := runSQLitePackageCommand(prefix, "apt-get", []string{"update"}); updateErr != nil {
				return fmt.Errorf("apt-get update: %w", updateErr)
			}
			err = runSQLitePackageCommand(prefix, pkgCmd.Name, pkgCmd.Args)
		}
		if err != nil {
			return fmt.Errorf("installing sqlite3: %w", err)
		}
		if runner, err = sqlite.NewRunner(); err != nil {
			return err
		}
	}
	versionOut, _ := exec.Command(runner.Bin, "--version").Output()
	fmt.Printf("  [OK] sqlite3 %s (%s)\n", strings.TrimSpace(string(versionOut)), runner.Bin)

	bin, version := "", ""
	if !sqliteForceLitestream {
		if p, v, err := sqlite.ResolveLitestreamBinary("", ""); err == nil {
			bin, version = p, v
		}
	}
	if bin == "" {
		download, err := sqlite.LitestreamDownload(runtime.GOARCH)
		if err != nil {
			return err
		}
		dir, note, err := hostbin.InstallDir(sqliteInstallDir)
		if err != nil {
			return err
		}
		if note != "" {
			fmt.Printf("  [!] %s\n", note)
		}
		fmt.Printf("-> downloading Litestream %s...\n", sqlite.LitestreamVersion)
		bin, err = hostbin.Ensure(download, dir)
		if err != nil {
			return err
		}
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return fmt.Errorf("running %s version: %w", bin, err)
	}
	version = sqlite.ParseVersionOutput(string(out))
	if !sqlite.SupportedLitestreamVersion(version) {
		return fmt.Errorf("installed Litestream at %s reports version %q, expected 0.5.x — run \"pg sqlite install --force-litestream\"", bin, version)
	}
	fmt.Printf("  [OK] litestream %s (%s)\n", version, bin)
	fmt.Println()
	fmt.Println("✓ SQLite tooling installed")
	return nil
}

func runSQLitePackageCommand(prefix []string, name string, args []string) error {
	argv := append(append([]string{}, prefix...), name)
	argv = append(argv, args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runSQLiteCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := sqlite.ValidateName(name); err != nil {
		return err
	}
	c, path, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	if _, exists := c.Addons.SQLite[name]; exists {
		return fmt.Errorf("SQLite database %q is already registered", name)
	}

	dbPath := sqliteCreatePath
	if dbPath == "" {
		dbPath = sqlite.DefaultDBPath(sqlite.BaseDir(c), name)
	}
	if !filepath.IsAbs(dbPath) {
		return fmt.Errorf("--path must be absolute, got %q", dbPath)
	}
	dbPath = filepath.Clean(dbPath)
	for _, other := range c.Addons.SQLite {
		if filepath.Clean(other.Path) == dbPath {
			return fmt.Errorf("path %s is already used by SQLite database %q", dbPath, other.Name)
		}
	}

	runner, err := sqlite.NewRunner()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dbPath), err)
	}
	_, statErr := os.Stat(dbPath)
	existed := statErr == nil
	if err := runner.EnsureWAL(dbPath); err != nil {
		if !existed {
			os.Remove(dbPath)
		}
		return err
	}
	if c.Addons.SQLite == nil {
		c.Addons.SQLite = make(map[string]config.SQLiteConfig)
	}
	c.Addons.SQLite[name] = config.SQLiteConfig{Name: name, Path: dbPath}
	if err := saveSQLiteConfig(c, path); err != nil {
		return err
	}

	if existed {
		fmt.Printf("  [OK] adopted existing database %s (WAL enabled, contents untouched)\n", dbPath)
	} else {
		fmt.Printf("  [OK] created database %s (journal_mode=wal)\n", dbPath)
	}
	fmt.Printf("  [OK] registered as %q in %s\n", name, path)
	return nil
}

func runSQLiteList(cmd *cobra.Command, _ []string) error {
	c, _, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	names := sortedSQLiteNames(c.Addons.SQLite)
	if len(names) == 0 {
		fmt.Println("No SQLite databases registered. Create one with \"pg sqlite create <name>\".")
		return nil
	}
	fmt.Printf("%-24s %-12s %-18s %s\n", "NAME", "SIZE", "REPLICATION", "PATH")
	for _, name := range names {
		db := c.Addons.SQLite[name]
		layout := sqliteLayout(c, db)
		repl := "off"
		if db.Replication != nil {
			repl = "stopped"
			if pid, ok := sqlite.ReplicationPID(layout.PIDFile); ok {
				repl = fmt.Sprintf("running pid %d", pid)
			}
		}
		fmt.Printf("%-24s %-12s %-18s %s\n", name, formatSQLiteBytes(sqlite.FileSize(db.Path)), repl, db.Path)
	}
	return nil
}

func runSQLiteInfo(cmd *cobra.Command, args []string) error {
	name := args[0]
	c, _, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	db, err := sqliteEntry(c, name)
	if err != nil {
		return err
	}
	if err := sqliteMustExist(db.Path); err != nil {
		return err
	}
	switch sqliteInfoIntegrity {
	case "none", "quick", "full":
	default:
		return fmt.Errorf("invalid --integrity %q: must be none, quick or full", sqliteInfoIntegrity)
	}

	runner, err := sqlite.NewRunner()
	if err != nil {
		return err
	}
	info, err := runner.Inspect(db.Path)
	if err != nil {
		return err
	}

	layout := sqliteLayout(c, db)
	fmt.Printf("Name:        %s\n", name)
	fmt.Printf("Path:        %s\n", db.Path)
	fmt.Printf("Size:        %s (WAL %s)\n", formatSQLiteBytes(info.SizeBytes), formatSQLiteBytes(info.WALBytes))
	fmt.Printf("Journal:     %s\n", info.JournalMode)
	fmt.Printf("Pages:       %d x %d bytes (freelist %d)\n", info.PageCount, info.PageSize, info.FreelistCount)
	printSQLiteReplicationSummary(layout, db)

	if sqliteInfoIntegrity != "none" {
		if err := runner.CheckIntegrity(db.Path, sqliteInfoIntegrity); err != nil {
			return err
		}
		fmt.Printf("Integrity:   ok (%s)\n", sqliteInfoIntegrity)
	}
	return nil
}

func printSQLiteReplicationSummary(layout sqlite.Layout, db config.SQLiteConfig) {
	if db.Replication == nil {
		fmt.Printf("Replication: off\n")
		return
	}
	if pid, ok := sqlite.ReplicationPID(layout.PIDFile); ok {
		fmt.Printf("Replication: running (pid %d)\n", pid)
	} else {
		fmt.Printf("Replication: configured, stopped\n")
	}
	s3 := db.Replication.S3
	if s3 != nil {
		endpoint := s3.Endpoint
		if endpoint == "" {
			endpoint = "AWS default"
		}
		fmt.Printf("S3:          %s bucket=%s path=%s region=%s\n", endpoint, s3.Bucket, s3.Path, s3.Region)
	}
	if errLine := sqlite.RecentLogError(layout.LogFile); errLine != "" {
		fmt.Printf("Last error:  %s\n", errLine)
	}
}

// sqliteNameAndArgs splits "<name> [--] [args...]". Cobra's DisableFlagParsing
// keeps the separator in args, and sqlite3 would treat it as an option.
func sqliteNameAndArgs(args []string) (string, []string, bool) {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return "", nil, false
	}
	name, rest := args[0], args[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	return name, rest, true
}

func runSQLiteShell(cmd *cobra.Command, args []string) error {
	name, rest, ok := sqliteNameAndArgs(args)
	if !ok {
		return fmt.Errorf("usage: pg sqlite shell <name> [-- sqlite3 args...]")
	}
	c, _, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	db, err := sqliteEntry(c, name)
	if err != nil {
		return err
	}
	if err := sqliteMustExist(db.Path); err != nil {
		return err
	}
	runner, err := sqlite.NewRunner()
	if err != nil {
		return err
	}
	return runner.Shell(db.Path, rest)
}

func runSQLiteExec(cmd *cobra.Command, args []string) error {
	name, rest, ok := sqliteNameAndArgs(args)
	if !ok {
		return fmt.Errorf("usage: pg sqlite exec <name> <sql> (use \"-\" to read SQL from stdin)")
	}
	c, _, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	db, err := sqliteEntry(c, name)
	if err != nil {
		return err
	}
	if err := sqliteMustExist(db.Path); err != nil {
		return err
	}
	runner, err := sqlite.NewRunner()
	if err != nil {
		return err
	}

	if len(rest) == 1 && rest[0] == "-" {
		return runner.Exec(db.Path, "", os.Stdin)
	}
	if len(rest) < 1 {
		return fmt.Errorf("missing SQL — usage: pg sqlite exec <name> <sql> (use \"-\" to read SQL from stdin)")
	}
	return runner.Exec(db.Path, strings.Join(rest, " "), nil)
}

func runSQLiteRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	c, path, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	db, err := sqliteEntry(c, name)
	if err != nil {
		return err
	}
	layout := sqliteLayout(c, db)

	if sqliteRemoveCleanData && !sqliteRemoveForce {
		prompt := fmt.Sprintf("This deletes %s and its WAL files permanently. Continue? [y/N] ", db.Path)
		if !confirmPrompt(prompt) {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	if sqlite.ReplicationRunning(layout.PIDFile) {
		fmt.Printf("-> stopping Litestream for %q...\n", name)
		if err := sqlite.StopReplication(layout.PIDFile); err != nil {
			return fmt.Errorf("stopping replication: %w", err)
		}
	}

	hadReplication := db.Replication != nil
	delete(c.Addons.SQLite, name)
	if err := saveSQLiteConfig(c, path); err != nil {
		return err
	}
	os.Remove(layout.ConfigFile)
	os.Remove(layout.PIDFile)

	if sqliteRemoveCleanData {
		for _, p := range sqlite.DBSidecars(db.Path) {
			os.Remove(p)
		}
		os.RemoveAll(layout.MetaDir)
		os.Remove(layout.LogFile)
		// Remove empty per-name state dir; harmless if it still has files.
		os.Remove(layout.StateDir)
		fmt.Printf("  [OK] deleted database data and local state for %q\n", name)
	} else {
		fmt.Printf("  [OK] unregistered %q; database file left at %s\n", name, db.Path)
	}
	if hadReplication {
		fmt.Println("  [!] remote S3 backups were not deleted.")
	}
	return nil
}

func formatSQLiteBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
