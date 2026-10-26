package sqlite

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/hostbin"
)

// litestreamFile mirrors the v0.5 configuration schema. Field order drives
// yaml.Marshal's output order.
type litestreamFile struct {
	Snapshot *litestreamSnapshot `yaml:"snapshot,omitempty"`
	DBs      []litestreamDB      `yaml:"dbs"`
}

type litestreamSnapshot struct {
	Interval  string `yaml:"interval,omitempty"`
	Retention string `yaml:"retention,omitempty"`
}

type litestreamDB struct {
	Path     string             `yaml:"path"`
	MetaPath string             `yaml:"meta-path,omitempty"`
	Replica  *litestreamReplica `yaml:"replica,omitempty"`
}

type litestreamReplica struct {
	Type            string `yaml:"type"`
	SyncInterval    string `yaml:"sync-interval,omitempty"`
	Endpoint        string `yaml:"endpoint,omitempty"`
	Bucket          string `yaml:"bucket"`
	Path            string `yaml:"path,omitempty"`
	Region          string `yaml:"region,omitempty"`
	AccessKeyID     string `yaml:"access-key-id,omitempty"`
	SecretAccessKey string `yaml:"secret-access-key,omitempty"`
	ForcePathStyle  bool   `yaml:"force-path-style,omitempty"`
	SkipVerify      bool   `yaml:"skip-verify,omitempty"`
}

// RenderLitestreamConfig renders the per-database Litestream YAML. Credentials
// are supplied separately because they are never persisted to pg.yaml.
func RenderLitestreamConfig(dbPath, metaPath string, repl config.SQLiteReplicationConfig, accessKey, secretKey string) ([]byte, error) {
	if repl.S3 == nil {
		return nil, fmt.Errorf("replication.s3 is required")
	}
	s3 := repl.S3
	if s3.Bucket == "" {
		return nil, fmt.Errorf("replication.s3.bucket is required")
	}
	if strings.HasPrefix(s3.Path, "/") {
		return nil, fmt.Errorf("replication.s3.path %q must not start with '/'", s3.Path)
	}
	file := litestreamFile{
		Snapshot: &litestreamSnapshot{
			Interval:  repl.SnapshotInterval,
			Retention: repl.Retention,
		},
		DBs: []litestreamDB{{
			Path:     dbPath,
			MetaPath: metaPath,
			Replica: &litestreamReplica{
				Type:            "s3",
				SyncInterval:    repl.SyncInterval,
				Endpoint:        s3.Endpoint,
				Bucket:          s3.Bucket,
				Path:            s3.Path,
				Region:          s3.Region,
				AccessKeyID:     accessKey,
				SecretAccessKey: secretKey,
				ForcePathStyle:  s3.URIStyle == "path",
				SkipVerify:      s3.SkipVerify,
			},
		}},
	}
	return yaml.Marshal(file)
}

// WriteConfigFile writes data to path atomically with mode 0600.
func WriteConfigFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".litestream-*.yml")
	if err != nil {
		return fmt.Errorf("creating temp config in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("installing %s: %w", path, err)
	}
	return nil
}

// StartupGrace is how long StartReplication waits before declaring the
// process healthy. Litestream exits immediately on bad credentials or config.
const StartupGrace = 2 * time.Second

// StartReplication launches `litestream replicate -config`.
func StartReplication(bin, configPath, logPath, pidPath, caFile string) error {
	var extraEnv []string
	if caFile != "" {
		extraEnv = append(extraEnv, "SSL_CERT_FILE="+caFile)
	}
	return hostbin.StartDetachedEnv(bin, []string{"replicate", "-config", configPath}, extraEnv, logPath, pidPath)
}

// WaitAlive verifies the detached process survived startup.
func WaitAlive(pidPath string, grace time.Duration) error {
	time.Sleep(grace)
	if !hostbin.Alive(pidPath) {
		return fmt.Errorf("litestream exited during startup")
	}
	return nil
}

// StopReplication sends SIGTERM and lets Litestream run its final sync.
func StopReplication(pidPath string) error {
	return hostbin.Stop(pidPath, 30*time.Second)
}

// ReplicationRunning reports whether the recorded process is alive.
func ReplicationRunning(pidPath string) bool {
	return hostbin.Alive(pidPath)
}

// ReplicationPID returns the recorded pid when the process is alive.
func ReplicationPID(pidPath string) (int, bool) {
	if !hostbin.Alive(pidPath) {
		return 0, false
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, false
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return 0, false
	}
	return pid, true
}

// LitestreamStatus mirrors `litestream status -json` (v0.5 fields are strings).
type LitestreamStatus struct {
	Database  string `json:"database"`
	Status    string `json:"status"`
	LocalTXID string `json:"local_txid"`
	WALSize   string `json:"wal_size"`
}

// QueryStatus reads local replication state for one database.
func QueryStatus(bin, configPath, dbPath string) (LitestreamStatus, error) {
	out, err := exec.Command(bin, "status", "-config", configPath, "-json", dbPath).Output()
	if err != nil {
		return LitestreamStatus{}, fmt.Errorf("litestream status: %w", err)
	}
	var statuses []LitestreamStatus
	if err := json.Unmarshal(out, &statuses); err != nil {
		return LitestreamStatus{}, fmt.Errorf("parsing litestream status: %w", err)
	}
	for _, status := range statuses {
		if status.Database == dbPath {
			return status, nil
		}
	}
	if len(statuses) == 0 {
		return LitestreamStatus{}, fmt.Errorf("litestream status returned no database entry")
	}
	return statuses[0], nil
}

// ResetLocalState clears the local LTX state. Litestream exits successfully
// when there is nothing to reset, so this is always safe to call.
func ResetLocalState(bin, configPath, dbPath string) error {
	out, err := exec.Command(bin, "reset", "-config", configPath, dbPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("litestream reset: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// NormalizeTXID accepts a hex transaction id with an optional 0x prefix and
// returns the fixed-width 16-character form Litestream's -txid flag requires.
func NormalizeTXID(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return "", fmt.Errorf("empty transaction id")
	}
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return "", fmt.Errorf("invalid transaction id %q: expected hexadecimal", s)
	}
	return fmt.Sprintf("%016x", v), nil
}

// RestoreOptions describes one `litestream restore` invocation.
type RestoreOptions struct {
	ConfigPath string
	DBPath     string
	OutputPath string // empty = in-place (or dry run with no output)
	Timestamp  string
	TXID       string
	Integrity  string // none | quick | full
	DryRun     bool
	Force      bool
}

// RestoreArgs builds the restore argv and validates time/txid selection.
func RestoreArgs(o RestoreOptions) ([]string, error) {
	if o.Timestamp != "" && o.TXID != "" {
		return nil, fmt.Errorf("--timestamp and --txid are mutually exclusive")
	}
	if o.Timestamp != "" {
		if _, err := time.Parse(time.RFC3339, o.Timestamp); err != nil {
			return nil, fmt.Errorf("invalid --timestamp %q: expected RFC3339 (e.g. 2026-01-01T12:00:00Z)", o.Timestamp)
		}
	}
	txid := ""
	if o.TXID != "" {
		var err error
		if txid, err = NormalizeTXID(o.TXID); err != nil {
			return nil, err
		}
	}
	switch o.Integrity {
	case "", "none", "quick", "full":
	default:
		return nil, fmt.Errorf("invalid --integrity %q: must be none, quick or full", o.Integrity)
	}

	args := []string{"restore", "-config", o.ConfigPath}
	if o.Timestamp != "" {
		args = append(args, "-timestamp", o.Timestamp)
	}
	if txid != "" {
		args = append(args, "-txid", txid)
	}
	if o.DryRun {
		args = append(args, "-dry-run")
	}
	if o.OutputPath != "" {
		args = append(args, "-o", o.OutputPath)
	}
	if o.Force && !o.DryRun {
		args = append(args, "-force")
	}
	if o.Integrity == "quick" || o.Integrity == "full" {
		args = append(args, "-integrity-check", o.Integrity)
	}
	return append(args, o.DBPath), nil
}

// RunRestore executes `litestream restore`, streaming its output.
func RunRestore(bin string, o RestoreOptions) error {
	args, err := RestoreArgs(o)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("litestream restore: %w", err)
	}
	return nil
}

// UniqueBackupDir creates `<dbPath>.pre-restore-<UTC>` (or a numbered variant).
func UniqueBackupDir(dbPath string, now time.Time) (string, error) {
	base := dbPath + ".pre-restore-" + now.UTC().Format("20060102T150405Z")
	for i := range 1000 {
		dir := base
		if i > 0 {
			dir = fmt.Sprintf("%s-%d", base, i)
		}
		if err := os.Mkdir(dir, 0o700); err == nil {
			return dir, nil
		} else if !os.IsExist(err) {
			return "", fmt.Errorf("creating backup dir %s: %w", dir, err)
		}
	}
	return "", fmt.Errorf("could not allocate a unique backup dir for %s", dbPath)
}

// MoveDBSidecars moves the database and any -wal/-shm files into destDir.
func MoveDBSidecars(dbPath, destDir string) ([]string, error) {
	base := filepath.Base(dbPath)
	var moved []string
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := dbPath + suffix
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return moved, err
		}
		dst := filepath.Join(destDir, base+suffix)
		if err := os.Rename(src, dst); err != nil {
			return moved, fmt.Errorf("moving %s to %s: %w", src, dst, err)
		}
		moved = append(moved, dst)
	}
	return moved, nil
}

// MoveRestoredDB moves a restored database and sidecars from srcDir to destDB.
func MoveRestoredDB(srcDir, destDB string) error {
	base := filepath.Base(destDB)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := filepath.Join(srcDir, base+suffix)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := os.Rename(src, destDB+suffix); err != nil {
			return fmt.Errorf("moving %s to %s: %w", src, destDB+suffix, err)
		}
	}
	return nil
}

// RecentLogError returns the last error-ish line of a log file, or "".
func RecentLogError(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	const window = 64 * 1024
	if info, err := f.Stat(); err == nil && info.Size() > window {
		if _, err := f.Seek(-window, io.SeekEnd); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for _, line := range slices.Backward(lines) {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "level=error") || strings.Contains(lower, "level=fatal") ||
			strings.Contains(lower, "error:") || strings.HasPrefix(lower, "error") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// LastLogLine returns the final non-empty log line, or "".
func LastLogLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for _, line := range slices.Backward(lines) {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// SameReplication reports whether two non-secret replication settings and
// binary choice are identical (used for idempotent enable).
func SameReplication(a, b *config.SQLiteReplicationConfig) bool {
	if a == nil || b == nil || a.S3 == nil || b.S3 == nil {
		return a == nil && b == nil
	}
	as, bs := a.S3, b.S3
	return a.SyncInterval == b.SyncInterval &&
		a.SnapshotInterval == b.SnapshotInterval &&
		a.Retention == b.Retention &&
		as.Endpoint == bs.Endpoint &&
		as.Bucket == bs.Bucket &&
		as.Path == bs.Path &&
		as.Region == bs.Region &&
		as.URIStyle == bs.URIStyle &&
		as.CAFile == bs.CAFile &&
		as.SkipVerify == bs.SkipVerify
}
