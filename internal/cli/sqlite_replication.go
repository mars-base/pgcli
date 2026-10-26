package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/sqlite"
)

var (
	sqliteEnableS3Bucket      string
	sqliteEnableS3Endpoint    string
	sqliteEnableS3Path        string
	sqliteEnableS3Region      string
	sqliteEnableS3AccessKey   string
	sqliteEnableS3SecretKey   string
	sqliteEnableS3URIStyle    string
	sqliteEnableS3CAFile      string
	sqliteEnableS3SkipVerify  bool
	sqliteEnableSyncInterval  string
	sqliteEnableSnapshotEvery string
	sqliteEnableRetention     string
	sqliteEnableBinary        string
	sqliteEnableNoStart       bool
	sqliteEnableForce         bool

	sqliteRestoreTarget    string
	sqliteRestoreTimestamp string
	sqliteRestoreTXID      string
	sqliteRestoreIntegrity string
	sqliteRestoreForce     bool
	sqliteRestoreDryRun    bool
	sqliteRestoreRestart   bool

	sqliteLogsFollow bool
	sqliteLogsTail   int
)

var sqliteReplicationCmd = &cobra.Command{
	Use:   "replication",
	Short: "Configure and operate Litestream WAL replication",
	Long: `Configure Litestream continuous replication for one SQLite database.

Each database gets its own Litestream process and its own 0600
litestream.yml. S3 credentials are accepted only as flags on "enable" and
are never written to pg.yaml or printed by any command.

The bucket must already exist; Litestream does not create it.`,
}

var sqliteReplicationEnableCmd = &cobra.Command{
	Use:   "enable <name>",
	Short: "Configure S3 replication and start Litestream",
	Args:  cobra.ExactArgs(1),
	RunE:  runSQLiteReplicationEnable,
}

var sqliteReplicationDisableCmd = &cobra.Command{
	Use:   "disable <name>",
	Short: "Stop replication and remove the local Litestream config",
	Args:  cobra.ExactArgs(1),
	RunE:  runSQLiteReplicationDisable,
}

var sqliteReplicationStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start Litestream for an enabled database",
	Args:  cobra.ExactArgs(1),
	RunE:  runSQLiteReplicationStart,
}

var sqliteReplicationStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop Litestream (runs a final sync)",
	Args:  cobra.ExactArgs(1),
	RunE:  runSQLiteReplicationStop,
}

var sqliteReplicationStatusCmd = &cobra.Command{
	Use:   "status [name]",
	Short: "Show replication process and local transaction state",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runSQLiteReplicationStatus,
}

var sqliteReplicationLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show the Litestream log",
	Args:  cobra.ExactArgs(1),
	RunE:  runSQLiteReplicationLogs,
}

var sqliteReplicationRestoreCmd = &cobra.Command{
	Use:   "restore <name>",
	Short: "Restore from S3 to a new file or in place",
	Long: `Restore a database from Litestream backups.

Without --target the database is restored in place: replication is stopped,
the existing database (if any) is preserved under "<db>.pre-restore-<UTC>/",
and replication stays stopped afterwards unless --restart is given.

Restarting replication against the same S3 path after a restore can confuse
the remote timeline; prefer a new --s3-path for the re-enabled job.`,
	Args: cobra.ExactArgs(1),
	RunE: runSQLiteReplicationRestore,
}

func init() {
	sqliteCmd.AddCommand(sqliteReplicationCmd)
	sqliteReplicationCmd.AddCommand(sqliteReplicationEnableCmd, sqliteReplicationDisableCmd,
		sqliteReplicationStartCmd, sqliteReplicationStopCmd, sqliteReplicationStatusCmd,
		sqliteReplicationLogsCmd, sqliteReplicationRestoreCmd)

	f := sqliteReplicationEnableCmd.Flags()
	f.StringVar(&sqliteEnableS3Bucket, "s3-bucket", "", "S3 bucket (required; must already exist)")
	f.StringVar(&sqliteEnableS3Endpoint, "s3-endpoint", "", "S3 endpoint URL, e.g. http://127.0.0.1:9000 (omit for AWS)")
	f.StringVar(&sqliteEnableS3Path, "s3-path", "", "object key prefix (default sqlite/<name>, no leading /)")
	f.StringVar(&sqliteEnableS3Region, "s3-region", "", "S3 region (default us-east-1)")
	f.StringVar(&sqliteEnableS3AccessKey, "s3-access-key", "", "S3 access key id (omit with --s3-secret-key to use the default credential chain)")
	f.StringVar(&sqliteEnableS3SecretKey, "s3-secret-key", "", "S3 secret access key (never stored in pg.yaml)")
	f.StringVar(&sqliteEnableS3URIStyle, "s3-uri-style", "path", "S3 URI style: path or virtual-host")
	f.StringVar(&sqliteEnableS3CAFile, "s3-ca-file", "", "CA bundle passed to Litestream via SSL_CERT_FILE")
	f.BoolVar(&sqliteEnableS3SkipVerify, "s3-skip-verify", false, "skip S3 TLS certificate verification")
	f.StringVar(&sqliteEnableSyncInterval, "sync-interval", "1s", "WAL sync interval")
	f.StringVar(&sqliteEnableSnapshotEvery, "snapshot-interval", "24h", "full snapshot interval")
	f.StringVar(&sqliteEnableRetention, "retention", "24h", "snapshot retention")
	f.StringVar(&sqliteEnableBinary, "litestream-binary", "", "explicit Litestream 0.5.x binary to use and record")
	f.BoolVar(&sqliteEnableNoStart, "no-start", false, "write the configuration but do not start Litestream")
	f.BoolVar(&sqliteEnableForce, "force", false, "replace an existing replication configuration")

	sqliteReplicationLogsCmd.Flags().BoolVarP(&sqliteLogsFollow, "follow", "f", false, "follow the log")
	sqliteReplicationLogsCmd.Flags().IntVarP(&sqliteLogsTail, "tail", "n", 50, "number of lines to show")

	rf := sqliteReplicationRestoreCmd.Flags()
	rf.StringVar(&sqliteRestoreTarget, "target", "", "restore to this path instead of in place")
	rf.StringVar(&sqliteRestoreTimestamp, "timestamp", "", "restore to this RFC3339 time in UTC with a Z suffix (e.g. 2026-01-01T12:00:00Z)")
	rf.StringVar(&sqliteRestoreTXID, "txid", "", "restore to this transaction id (hex)")
	rf.StringVar(&sqliteRestoreIntegrity, "integrity", "quick", "integrity check: none, quick or full")
	rf.BoolVar(&sqliteRestoreForce, "force", false, "skip the in-place replacement confirmation")
	rf.BoolVar(&sqliteRestoreDryRun, "dry-run", false, "show what would be restored without writing")
	rf.BoolVar(&sqliteRestoreRestart, "restart", false, "start replication after an in-place restore")
}

func validatePositiveDuration(flagName, value string) error {
	d, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", flagName, value, err)
	}
	if d <= 0 {
		return fmt.Errorf("%s must be greater than zero", flagName)
	}
	return nil
}

// sqliteS3Prefix normalizes --s3-path: Litestream concatenates the prefix
// directly into object keys, so a leading slash would produce an empty key
// segment. An empty flag defaults to sqlite/<name>.
func sqliteS3Prefix(flagValue, name string) string {
	prefix := strings.TrimLeft(strings.TrimSpace(flagValue), "/")
	if prefix == "" {
		return "sqlite/" + name
	}
	return prefix
}

func runSQLiteReplicationEnable(cmd *cobra.Command, args []string) error {
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
	if err := sqliteMustExist(db.Path); err != nil {
		return err
	}

	if sqliteEnableS3Bucket == "" {
		return fmt.Errorf("--s3-bucket is required")
	}
	if sqliteEnableS3Endpoint != "" {
		if err := config.ValidateS3Endpoint("replication", sqliteEnableS3Endpoint); err != nil {
			return err
		}
	}
	if sqliteEnableS3URIStyle != "path" && sqliteEnableS3URIStyle != "virtual-host" {
		return fmt.Errorf("invalid --s3-uri-style %q: must be path or virtual-host", sqliteEnableS3URIStyle)
	}
	if (sqliteEnableS3AccessKey == "") != (sqliteEnableS3SecretKey == "") {
		return fmt.Errorf("--s3-access-key and --s3-secret-key must be provided together")
	}
	if sqliteEnableS3CAFile != "" {
		info, err := os.Stat(sqliteEnableS3CAFile)
		if err != nil {
			return fmt.Errorf("--s3-ca-file: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("--s3-ca-file %s is a directory", sqliteEnableS3CAFile)
		}
	}
	for _, flag := range []struct{ name, value string }{
		{"--sync-interval", sqliteEnableSyncInterval},
		{"--snapshot-interval", sqliteEnableSnapshotEvery},
		{"--retention", sqliteEnableRetention},
	} {
		if err := validatePositiveDuration(flag.name, flag.value); err != nil {
			return err
		}
	}
	s3Path := sqliteS3Prefix(sqliteEnableS3Path, name)

	runner, err := sqlite.NewRunner()
	if err != nil {
		return err
	}
	out, err := runner.Query(db.Path, "PRAGMA journal_mode;", true)
	if err != nil {
		return err
	}
	if mode := sqlite.ParseJournalMode(out); mode != "wal" {
		return fmt.Errorf("database %s uses journal_mode=%s, Litestream requires WAL — run \"pg sqlite create %s\" (or PRAGMA journal_mode=WAL) first", db.Path, mode, name)
	}

	bin, version, err := sqlite.ResolveLitestreamBinary(sqliteEnableBinary, db.LitestreamBinary)
	if err != nil {
		return err
	}

	newDB := db
	newDB.LitestreamBinary = bin
	newDB.LitestreamVersion = version
	newDB.Replication = &config.SQLiteReplicationConfig{
		SyncInterval:     sqliteEnableSyncInterval,
		SnapshotInterval: sqliteEnableSnapshotEvery,
		Retention:        sqliteEnableRetention,
		S3: &config.SQLiteS3Config{
			Endpoint:   sqliteEnableS3Endpoint,
			Bucket:     sqliteEnableS3Bucket,
			Path:       s3Path,
			Region:     sqliteEnableS3Region,
			URIStyle:   sqliteEnableS3URIStyle,
			CAFile:     sqliteEnableS3CAFile,
			SkipVerify: sqliteEnableS3SkipVerify,
		},
	}
	c.Addons.SQLite[name] = newDB
	c.ApplyDefaults()
	newDB = c.Addons.SQLite[name]

	_, statErr := os.Stat(layout.ConfigFile)
	configExists := statErr == nil
	running := sqlite.ReplicationRunning(layout.PIDFile)

	if db.Replication != nil && !sqliteEnableForce {
		same := sqlite.SameReplication(db.Replication, newDB.Replication) &&
			db.LitestreamBinary == newDB.LitestreamBinary
		if same && running && configExists {
			fmt.Printf("  [OK] replication for %q is already configured and running (pid %d)\n", name, mustSQLitePID(layout))
			return nil
		}
		return fmt.Errorf("replication is already configured for %q — use --force to rewrite it and restart Litestream", name)
	}
	if db.Replication == nil && running && !sqliteEnableForce {
		return fmt.Errorf("a Litestream process is already running for %q but replication is not configured in pg.yaml — use --force to replace it", name)
	}
	if running {
		fmt.Printf("-> stopping the existing Litestream process for %q...\n", name)
		if err := sqlite.StopReplication(layout.PIDFile); err != nil {
			return fmt.Errorf("stopping existing replication: %w", err)
		}
	}

	data, err := sqlite.RenderLitestreamConfig(db.Path, layout.MetaDir, *newDB.Replication,
		sqliteEnableS3AccessKey, sqliteEnableS3SecretKey)
	if err != nil {
		return err
	}
	if err := sqlite.WriteConfigFile(layout.ConfigFile, data); err != nil {
		return err
	}
	if err := saveSQLiteConfig(c, path); err != nil {
		os.Remove(layout.ConfigFile)
		return err
	}
	fmt.Printf("  [OK] wrote %s (0600)\n", layout.ConfigFile)
	fmt.Printf("  [OK] replication metadata saved to %s (no credentials)\n", path)

	if err := sqlite.ResetLocalState(bin, layout.ConfigFile, db.Path); err != nil {
		return err
	}
	if sqliteEnableNoStart {
		fmt.Printf("  [OK] replication configured for %q; Litestream is not running (--no-start)\n", name)
		return nil
	}
	return startSQLiteReplication(layout, newDB)
}

func mustSQLitePID(layout sqlite.Layout) int {
	pid, _ := sqlite.ReplicationPID(layout.PIDFile)
	return pid
}

func startSQLiteReplication(layout sqlite.Layout, db config.SQLiteConfig) error {
	if pid, ok := sqlite.ReplicationPID(layout.PIDFile); ok {
		fmt.Printf("  [OK] Litestream is already running (pid %d)\n", pid)
		return nil
	}
	if _, err := os.Stat(layout.ConfigFile); err != nil {
		return fmt.Errorf("litestream.yml not found at %s — run \"pg sqlite replication enable %s\" again", layout.ConfigFile, db.Name)
	}
	bin, _, err := sqlite.ResolveLitestreamBinary("", db.LitestreamBinary)
	if err != nil {
		return err
	}
	caFile := ""
	if db.Replication != nil && db.Replication.S3 != nil {
		caFile = db.Replication.S3.CAFile
	}
	if err := sqlite.StartReplication(bin, layout.ConfigFile, layout.LogFile, layout.PIDFile, caFile); err != nil {
		return err
	}
	if err := sqlite.WaitAlive(layout.PIDFile, sqlite.StartupGrace); err != nil {
		os.Remove(layout.PIDFile)
		msg := err.Error()
		if tail := sqlite.LastLogLine(layout.LogFile); tail != "" {
			msg += " — last log line: " + tail
		}
		return fmt.Errorf("%s (log: %s)", msg, layout.LogFile)
	}
	fmt.Printf("  [OK] Litestream running for %q (pid %d, log %s)\n", db.Name, mustSQLitePID(layout), layout.LogFile)
	return nil
}

func runSQLiteReplicationDisable(cmd *cobra.Command, args []string) error {
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
	hadReplication := db.Replication != nil
	hadAutostart := db.Autostart

	if err := stopSQLiteReplication(layout); err != nil {
		return err
	}
	db.Replication = nil
	db.Autostart = false
	c.Addons.SQLite[name] = db
	if err := saveSQLiteConfig(c, path); err != nil {
		return err
	}
	os.Remove(layout.ConfigFile)
	os.Remove(layout.PIDFile)

	if hadReplication {
		fmt.Printf("  [OK] replication disabled for %q (local Litestream state kept at %s)\n", name, layout.MetaDir)
		fmt.Println("  [!] remote S3 backups were not deleted.")
	} else {
		fmt.Printf("  [OK] replication is not configured for %q; removed any leftover local config\n", name)
	}
	if hadAutostart {
		fmt.Printf("  [!] auto-start also disabled for %q (replication is off)\n", name)
	}
	return nil
}

// stopSQLiteReplication only signals the recorded pid when the process still
// matches the recorded command, to avoid touching an unrelated reused pid.
func stopSQLiteReplication(layout sqlite.Layout) error {
	if sqlite.ReplicationRunning(layout.PIDFile) {
		return sqlite.StopReplication(layout.PIDFile)
	}
	os.Remove(layout.PIDFile)
	return nil
}

func runSQLiteReplicationStart(cmd *cobra.Command, args []string) error {
	name := args[0]
	c, _, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	db, err := sqliteEntry(c, name)
	if err != nil {
		return err
	}
	if db.Replication == nil {
		return fmt.Errorf("replication is not enabled for %q — run \"pg sqlite replication enable %s\" first", name, name)
	}
	return startSQLiteReplication(sqliteLayout(c, db), db)
}

func runSQLiteReplicationStop(cmd *cobra.Command, args []string) error {
	name := args[0]
	c, _, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	db, err := sqliteEntry(c, name)
	if err != nil {
		return err
	}
	layout := sqliteLayout(c, db)
	if !sqlite.ReplicationRunning(layout.PIDFile) {
		os.Remove(layout.PIDFile)
		fmt.Printf("  [OK] Litestream is not running for %q\n", name)
		return nil
	}
	fmt.Printf("-> stopping Litestream for %q (final sync)...\n", name)
	if err := sqlite.StopReplication(layout.PIDFile); err != nil {
		return err
	}
	fmt.Printf("  [OK] Litestream stopped for %q\n", name)
	return nil
}

func runSQLiteReplicationStatus(cmd *cobra.Command, args []string) error {
	c, _, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	var names []string
	if len(args) == 1 {
		if _, err := sqliteEntry(c, args[0]); err != nil {
			return err
		}
		names = []string{args[0]}
	} else {
		names = sortedSQLiteNames(c.Addons.SQLite)
		if len(names) == 0 {
			fmt.Println("No SQLite databases registered.")
			return nil
		}
	}
	verbose := len(args) == 1
	for _, name := range names {
		printSQLiteReplicationStatus(c, name, c.Addons.SQLite[name], verbose)
	}
	return nil
}

func printSQLiteReplicationStatus(c *config.Config, name string, db config.SQLiteConfig, verbose bool) {
	layout := sqliteLayout(c, db)
	if !verbose {
		fmt.Printf("%s\n", name)
	}
	if db.Replication == nil {
		fmt.Printf("  replication: off\n")
		return
	}
	pid, running := sqlite.ReplicationPID(layout.PIDFile)
	if running {
		fmt.Printf("  process:     running (pid %d)\n", pid)
	} else {
		fmt.Printf("  process:     stopped\n")
	}
	s3 := db.Replication.S3
	if s3 != nil {
		endpoint := s3.Endpoint
		if endpoint == "" {
			endpoint = "AWS default"
		}
		fmt.Printf("  s3:          %s bucket=%s path=%s region=%s\n", endpoint, s3.Bucket, s3.Path, s3.Region)
	}
	if _, err := os.Stat(layout.ConfigFile); err != nil {
		fmt.Printf("  [!] litestream.yml missing at %s — run \"pg sqlite replication enable %s\" again\n", layout.ConfigFile, name)
		return
	}
	bin, _, err := sqlite.ResolveLitestreamBinary("", db.LitestreamBinary)
	if err != nil {
		fmt.Printf("  [!] %v\n", err)
		return
	}
	status, err := sqlite.QueryStatus(bin, layout.ConfigFile, db.Path)
	if err != nil {
		fmt.Printf("  [!] status unavailable: %v\n", err)
	} else {
		fmt.Printf("  local:       state=%s txid=%s wal=%s\n", status.Status, status.LocalTXID, status.WALSize)
	}
	if errLine := sqlite.RecentLogError(layout.LogFile); errLine != "" {
		fmt.Printf("  last error:  %s\n", errLine)
	}
}

func runSQLiteReplicationLogs(cmd *cobra.Command, args []string) error {
	c, _, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	db, err := sqliteEntry(c, args[0])
	if err != nil {
		return err
	}
	return tailHostLog(sqliteLayout(c, db).LogFile, sqliteLogsTail, sqliteLogsFollow)
}

func runSQLiteReplicationRestore(cmd *cobra.Command, args []string) error {
	name := args[0]
	c, _, err := loadSQLiteConfig()
	if err != nil {
		return err
	}
	db, err := sqliteEntry(c, name)
	if err != nil {
		return err
	}
	if db.Replication == nil {
		return fmt.Errorf("replication is not enabled for %q — run \"pg sqlite replication enable %s\" first", name, name)
	}
	layout := sqliteLayout(c, db)
	if _, err := os.Stat(layout.ConfigFile); err != nil {
		return fmt.Errorf("litestream.yml not found at %s — run \"pg sqlite replication enable %s\" again", layout.ConfigFile, name)
	}
	if sqliteRestoreTarget != "" && sqliteRestoreRestart {
		return fmt.Errorf("--target and --restart cannot be combined")
	}
	if sqliteRestoreDryRun && sqliteRestoreRestart {
		return fmt.Errorf("--dry-run and --restart cannot be combined")
	}
	if sqliteRestoreTarget != "" {
		targetAbs, err := filepath.Abs(sqliteRestoreTarget)
		if err != nil {
			return err
		}
		dbAbs, err := filepath.Abs(db.Path)
		if err != nil {
			return err
		}
		if filepath.Clean(targetAbs) == filepath.Clean(dbAbs) {
			return fmt.Errorf("--target must differ from the configured database path %s — omit --target for an in-place restore", db.Path)
		}
	}

	baseOpts := sqlite.RestoreOptions{
		ConfigPath: layout.ConfigFile,
		DBPath:     db.Path,
		Timestamp:  sqliteRestoreTimestamp,
		TXID:       sqliteRestoreTXID,
		Integrity:  sqliteRestoreIntegrity,
		DryRun:     sqliteRestoreDryRun,
	}
	// Validate flag combinations and values before touching any state.
	if _, err := sqlite.RestoreArgs(baseOpts); err != nil {
		return err
	}
	bin, _, err := sqlite.ResolveLitestreamBinary("", db.LitestreamBinary)
	if err != nil {
		return err
	}

	if sqliteRestoreTarget != "" {
		if sqliteRestoreDryRun {
			opts := baseOpts
			opts.OutputPath = sqliteRestoreTarget
			return sqlite.RunRestore(bin, opts)
		}
		return restoreSQLiteToTarget(bin, baseOpts, sqliteRestoreTarget)
	}

	if sqliteRestoreDryRun {
		return sqlite.RunRestore(bin, baseOpts)
	}
	return restoreSQLiteInPlace(bin, c, db, layout, baseOpts)
}

func restoreSQLiteToTarget(bin string, baseOpts sqlite.RestoreOptions, target string) error {
	info, err := os.Stat(target)
	switch {
	case err == nil && info.IsDir():
		return fmt.Errorf("--target %s is a directory", target)
	case err == nil:
		if !sqliteRestoreForce {
			return fmt.Errorf("target %s already exists — pass --force to replace it", target)
		}
		backup, err := sqlite.UniqueBackupDir(target, time.Now())
		if err != nil {
			return err
		}
		if _, err := sqlite.MoveDBSidecars(target, backup); err != nil {
			return err
		}
		fmt.Printf("  [OK] existing target preserved at %s\n", backup)
	case !os.IsNotExist(err):
		return err
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(target), err)
	}
	opts := baseOpts
	opts.OutputPath = target
	opts.Force = false
	fmt.Printf("-> restoring to %s...\n", target)
	if err := sqlite.RunRestore(bin, opts); err != nil {
		return err
	}

	if runner, err := sqlite.NewRunner(); err != nil {
		fmt.Printf("  [!] %v; skipping journal_mode check\n", err)
	} else if out, err := runner.Query(target, "PRAGMA journal_mode;", true); err != nil {
		fmt.Printf("  [!] could not check journal_mode on %s: %v\n", target, err)
	} else if mode := sqlite.ParseJournalMode(out); mode != "wal" {
		fmt.Printf("  [!] restored database has journal_mode=%s; enable WAL before using it with Litestream\n", mode)
	}
	fmt.Printf("  [OK] restored %s (replication was not stopped)\n", target)
	return nil
}

func restoreSQLiteInPlace(bin string, c *config.Config, db config.SQLiteConfig, layout sqlite.Layout, baseOpts sqlite.RestoreOptions) error {
	dbInfo, statErr := os.Stat(db.Path)
	dbExists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if dbExists && dbInfo.IsDir() {
		return fmt.Errorf("database path %s is a directory", db.Path)
	}

	hasSidecars := dbExists
	if !hasSidecars {
		for _, p := range sqlite.DBSidecars(db.Path) {
			if _, err := os.Stat(p); err == nil {
				hasSidecars = true
				break
			}
		}
	}

	if hasSidecars && !sqliteRestoreForce {
		prompt := fmt.Sprintf("Restore will stop replication and replace %s. The current files are preserved on disk. Continue? [y/N] ", db.Path)
		if !confirmPrompt(prompt) {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	fmt.Println("  [!] stop every application that has this database open before continuing.")
	fmt.Printf("-> stopping Litestream for %q...\n", db.Name)
	if err := stopSQLiteReplication(layout); err != nil {
		return fmt.Errorf("stopping replication: %w", err)
	}

	backup := ""
	if hasSidecars {
		var err error
		backup, err = sqlite.UniqueBackupDir(db.Path, time.Now())
		if err != nil {
			return err
		}
		if _, err := sqlite.MoveDBSidecars(db.Path, backup); err != nil {
			return err
		}
		fmt.Printf("  [OK] previous database preserved at %s\n", backup)
	}

	dbDir := filepath.Dir(db.Path)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dbDir, err)
	}
	tmpDir, err := os.MkdirTemp(dbDir, ".pg-sqlite-restore-")
	if err != nil {
		return fmt.Errorf("creating restore temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpDB := filepath.Join(tmpDir, filepath.Base(db.Path))
	opts := baseOpts
	opts.OutputPath = tmpDB
	opts.Force = false
	fmt.Printf("-> restoring %s...\n", db.Path)
	if err := sqlite.RunRestore(bin, opts); err != nil {
		if backup != "" {
			fmt.Printf("  [!] previous database is still preserved at %s\n", backup)
		}
		return err
	}

	runner, err := sqlite.NewRunner()
	if err != nil {
		return err
	}
	out, err := runner.Query(tmpDB, "PRAGMA journal_mode;", true)
	if err != nil {
		return err
	}
	if mode := sqlite.ParseJournalMode(out); mode != "wal" {
		if err := runner.EnsureWAL(tmpDB); err != nil {
			return err
		}
		fmt.Printf("  [!] restored database was journal_mode=%s; enabled WAL\n", mode)
	}

	if err := sqlite.MoveRestoredDB(tmpDir, db.Path); err != nil {
		return err
	}
	if err := sqlite.ResetLocalState(bin, layout.ConfigFile, db.Path); err != nil {
		fmt.Printf("  [!] %v\n", err)
		if rmErr := os.RemoveAll(layout.MetaDir); rmErr != nil {
			fmt.Printf("  [!] could not remove stale local state %s: %v\n", layout.MetaDir, rmErr)
		} else {
			fmt.Printf("  [!] removed stale local Litestream state %s\n", layout.MetaDir)
		}
	}

	fmt.Printf("  [OK] restored %s in place\n", db.Path)
	fmt.Println("  [!] replication remains stopped. Restarting it against the same S3 path may mix the restored")
	fmt.Println("      timeline with the old remote data; consider enabling with a new --s3-path first.")
	if sqliteRestoreRestart {
		if err := startSQLiteReplication(layout, db); err != nil {
			return err
		}
	}
	return nil
}
