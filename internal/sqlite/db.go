// Package sqlite implements host-local SQLite database management and
// Litestream replication. It shells out to the sqlite3 and litestream
// binaries; no cgo or embedded database driver is involved.
package sqlite

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/hostbin"
	"github.com/mars-base/pgcli/internal/platform"
)

// timeoutCmd keeps sqlite3 from failing immediately when another process
// holds a write lock; it is connection-scoped, not a persistent setting.
const timeoutCmd = ".timeout 5000"

// ValidateName rejects names that cannot safely be used as directory keys.
func ValidateName(name string) error {
	if !config.ValidSQLiteName(name) {
		return fmt.Errorf("invalid SQLite database name %q: must match [A-Za-z0-9][A-Za-z0-9_-]{0,47}", name)
	}
	return nil
}

// BaseDir resolves where pgcli keeps SQLite state.
func BaseDir(c *config.Config) string {
	if c.BaseDir != "" {
		return c.BaseDir
	}
	return platform.DefaultConfigDir()
}

// DefaultDBPath is <base>/sqlite/<name>/<name>.db.
func DefaultDBPath(baseDir, name string) string {
	return filepath.Join(baseDir, "sqlite", name, name+".db")
}

// Layout holds every path owned by one managed database. The database file
// may live elsewhere via --path, but config/log/pid/meta stay under the
// per-name state directory so cleanup is uniform.
type Layout struct {
	DB         string
	StateDir   string
	ConfigFile string
	LogFile    string
	PIDFile    string
	MetaDir    string
}

// NewLayout derives the state layout for db.
func NewLayout(baseDir string, db config.SQLiteConfig) Layout {
	stateDir := filepath.Join(baseDir, "sqlite", db.Name)
	return Layout{
		DB:         db.Path,
		StateDir:   stateDir,
		ConfigFile: filepath.Join(stateDir, "litestream.yml"),
		LogFile:    filepath.Join(stateDir, "litestream.log"),
		PIDFile:    filepath.Join(stateDir, "litestream.pid"),
		MetaDir:    filepath.Join(stateDir, "."+filepath.Base(db.Path)+"-litestream"),
	}
}

// Runner executes the sqlite3 CLI.
type Runner struct {
	Bin string
}

// NewRunner finds sqlite3 on PATH.
func NewRunner() (*Runner, error) {
	p, ok := hostbin.Detect("sqlite3")
	if !ok {
		return nil, fmt.Errorf("sqlite3 not found on PATH — run \"pg sqlite install\" first")
	}
	return &Runner{Bin: p}, nil
}

// SQLiteArgs builds argv for a one-shot statement. Options precede FILENAME,
// and the statement is a single argv element (never passed through a shell).
func SQLiteArgs(dbPath string, readOnly bool, sql string) []string {
	args := []string{"-cmd", timeoutCmd}
	if readOnly {
		args = append(args, "-readonly")
	}
	args = append(args, dbPath)
	if sql != "" {
		args = append(args, sql)
	}
	return args
}

// SQLiteShellArgs builds argv for an interactive shell: caller arguments
// (e.g. -header) go before FILENAME so sqlite3 parses them as options.
func SQLiteShellArgs(dbPath string, extra []string) []string {
	args := []string{"-cmd", timeoutCmd}
	args = append(args, extra...)
	return append(args, dbPath)
}

// Query runs sql and captures stdout+stderr.
func (r *Runner) Query(dbPath, sql string, readOnly bool) (string, error) {
	out, err := exec.Command(r.Bin, SQLiteArgs(dbPath, readOnly, sql)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sqlite3: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Exec runs sql with inherited stdio; stdin may be nil.
func (r *Runner) Exec(dbPath, sql string, stdin io.Reader) error {
	cmd := exec.Command(r.Bin, SQLiteArgs(dbPath, false, sql)...)
	cmd.Stdin = stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sqlite3: %w", err)
	}
	return nil
}

// Shell runs an interactive sqlite3 session.
func (r *Runner) Shell(dbPath string, extra []string) error {
	cmd := exec.Command(r.Bin, SQLiteShellArgs(dbPath, extra)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sqlite3: %w", err)
	}
	return nil
}

// EnsureWAL enables WAL and verifies the persistent journal mode.
func (r *Runner) EnsureWAL(dbPath string) error {
	out, err := r.Query(dbPath, "PRAGMA journal_mode=WAL;", false)
	if err != nil {
		return err
	}
	if mode := ParseJournalMode(out); mode != "wal" {
		return fmt.Errorf("could not enable WAL on %s (journal_mode=%q)", dbPath, mode)
	}
	return nil
}

// CheckIntegrity runs quick_check or integrity_check. "none" is a no-op.
func (r *Runner) CheckIntegrity(dbPath, level string) error {
	var pragma string
	switch level {
	case "", "none":
		return nil
	case "quick":
		pragma = "PRAGMA quick_check;"
	case "full":
		pragma = "PRAGMA integrity_check;"
	default:
		return fmt.Errorf("invalid integrity level %q: must be none, quick or full", level)
	}
	out, err := r.Query(dbPath, pragma, true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "ok" {
		return fmt.Errorf("integrity check (%s) failed for %s:\n%s", level, dbPath, strings.TrimSpace(out))
	}
	return nil
}

// Info is the subset of PRAGMA state shown by `pg sqlite info`.
type Info struct {
	JournalMode   string
	PageSize      int64
	PageCount     int64
	FreelistCount int64
	SizeBytes     int64
	WALBytes      int64
}

// Inspect reads database pragmas without modifying the file.
func (r *Runner) Inspect(dbPath string) (Info, error) {
	var info Info
	var err error
	if info.JournalMode, err = r.pragmaValue(dbPath, "PRAGMA journal_mode;"); err != nil {
		return Info{}, err
	}
	if info.PageSize, err = r.pragmaInt(dbPath, "PRAGMA page_size;"); err != nil {
		return Info{}, err
	}
	if info.PageCount, err = r.pragmaInt(dbPath, "PRAGMA page_count;"); err != nil {
		return Info{}, err
	}
	if info.FreelistCount, err = r.pragmaInt(dbPath, "PRAGMA freelist_count;"); err != nil {
		return Info{}, err
	}
	info.SizeBytes = FileSize(dbPath)
	info.WALBytes = FileSize(dbPath + "-wal")
	return info, nil
}

func (r *Runner) pragmaValue(dbPath, pragma string) (string, error) {
	out, err := r.Query(dbPath, pragma, true)
	if err != nil {
		return "", err
	}
	return FirstLine(out), nil
}

func (r *Runner) pragmaInt(dbPath, pragma string) (int64, error) {
	value, err := r.pragmaValue(dbPath, pragma)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s output %q: %w", pragma, value, err)
	}
	return n, nil
}

// FirstLine returns the first non-empty output line, trimmed.
func FirstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// ParseJournalMode normalizes sqlite3's journal_mode output.
func ParseJournalMode(out string) string {
	return strings.ToLower(FirstLine(out))
}

// FileSize returns the file size, or 0 when it does not exist.
func FileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// DBSidecars lists the database file and its WAL sidecars.
func DBSidecars(dbPath string) []string {
	return []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
}
