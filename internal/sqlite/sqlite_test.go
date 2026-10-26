package sqlite

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

func TestValidateName(t *testing.T) {
	valid := []string{"appdb", "app_db-1", "A", strings.Repeat("a", 48)}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", ".", "..", "a/b", "-a", "a b", strings.Repeat("a", 49)}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", name)
		}
	}
}

func TestDefaultDBPathAndLayout(t *testing.T) {
	got := DefaultDBPath("/srv/pg", "appdb")
	want := filepath.Join("/srv/pg", "sqlite", "appdb", "appdb.db")
	if got != want {
		t.Errorf("DefaultDBPath = %q, want %q", got, want)
	}

	db := config.SQLiteConfig{Name: "appdb", Path: "/data/custom.db"}
	layout := NewLayout("/srv/pg", db)
	if layout.DB != "/data/custom.db" {
		t.Errorf("DB = %q", layout.DB)
	}
	if layout.ConfigFile != "/srv/pg/sqlite/appdb/litestream.yml" {
		t.Errorf("ConfigFile = %q", layout.ConfigFile)
	}
	if layout.LogFile != "/srv/pg/sqlite/appdb/litestream.log" {
		t.Errorf("LogFile = %q", layout.LogFile)
	}
	if layout.PIDFile != "/srv/pg/sqlite/appdb/litestream.pid" {
		t.Errorf("PIDFile = %q", layout.PIDFile)
	}
	if layout.MetaDir != "/srv/pg/sqlite/appdb/.custom.db-litestream" {
		t.Errorf("MetaDir = %q", layout.MetaDir)
	}
}

func TestSQLiteArgs(t *testing.T) {
	got := SQLiteArgs("/data/app.db", false, "SELECT 1")
	want := []string{"-cmd", ".timeout 5000", "/data/app.db", "SELECT 1"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("SQLiteArgs = %v, want %v", got, want)
	}
	for _, arg := range got {
		if arg == "/bin/sh" || arg == "sh" {
			t.Fatalf("argv must not invoke a shell: %v", got)
		}
	}

	got = SQLiteArgs("/data/app.db", true, "")
	want = []string{"-cmd", ".timeout 5000", "-readonly", "/data/app.db"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("SQLiteArgs read-only = %v, want %v", got, want)
	}

	got = SQLiteShellArgs("/data/app.db", []string{"-header", "-column"})
	want = []string{"-cmd", ".timeout 5000", "-header", "-column", "/data/app.db"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("SQLiteShellArgs = %v, want %v", got, want)
	}
}

func TestParseHelpers(t *testing.T) {
	if got := FirstLine("\n  wal  \nfoo\n"); got != "wal" {
		t.Errorf("FirstLine = %q", got)
	}
	if got := ParseJournalMode("\nWAL\n"); got != "wal" {
		t.Errorf("ParseJournalMode = %q", got)
	}
	if got := ParseVersionOutput("0.5.17\n"); got != "0.5.17" {
		t.Errorf("ParseVersionOutput = %q", got)
	}
	if got := ParseVersionOutput("litestream version v0.5.17"); got != "0.5.17" {
		t.Errorf("ParseVersionOutput = %q", got)
	}
	if !SupportedLitestreamVersion("0.5.17") || SupportedLitestreamVersion("0.3.13") {
		t.Error("SupportedLitestreamVersion misclassified")
	}
}

func TestNormalizeTXID(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"1", "0000000000000001", false},
		{"0x10", "0000000000000010", false},
		{"00000000000000ff", "00000000000000ff", false},
		{"FFFFFFFFFFFFFFFF", "ffffffffffffffff", false},
		{"", "", true},
		{"xyz", "", true},
		{"10000000000000000", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeTXID(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("NormalizeTXID(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("NormalizeTXID(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestRestoreArgs(t *testing.T) {
	base := RestoreOptions{ConfigPath: "/state/litestream.yml", DBPath: "/data/app.db"}

	args, err := RestoreArgs(base)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"restore", "-config", "/state/litestream.yml", "/data/app.db"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("RestoreArgs = %v, want %v", args, want)
	}

	o := base
	o.OutputPath = "/tmp/restored.db"
	o.TXID = "1"
	o.Integrity = "full"
	o.Force = true
	args, err = RestoreArgs(o)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"restore", "-config", "/state/litestream.yml", "-txid", "0000000000000001",
		"-o", "/tmp/restored.db", "-force", "-integrity-check", "full", "/data/app.db"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("RestoreArgs = %v, want %v", args, want)
	}

	o = base
	o.Timestamp = "2026-01-01T12:00:00Z"
	o.DryRun = true
	o.Force = true
	args, err = RestoreArgs(o)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"restore", "-config", "/state/litestream.yml", "-timestamp", "2026-01-01T12:00:00Z", "-dry-run", "/data/app.db"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("RestoreArgs = %v, want %v", args, want)
	}

	bad := []RestoreOptions{
		{ConfigPath: "c", DBPath: "d", Timestamp: "2026-01-01T12:00:00Z", TXID: "1"},
		{ConfigPath: "c", DBPath: "d", Timestamp: "yesterday"},
		{ConfigPath: "c", DBPath: "d", TXID: "zz"},
		{ConfigPath: "c", DBPath: "d", Integrity: "deep"},
	}
	for _, o := range bad {
		if _, err := RestoreArgs(o); err == nil {
			t.Errorf("RestoreArgs(%+v) = nil error, want error", o)
		}
	}
}

func TestRenderLitestreamConfig(t *testing.T) {
	repl := config.SQLiteReplicationConfig{
		SyncInterval:     "1s",
		SnapshotInterval: "24h",
		Retention:        "24h",
		S3: &config.SQLiteS3Config{
			Endpoint: "http://127.0.0.1:9000",
			Bucket:   "backups",
			Path:     "sqlite/appdb",
			Region:   "us-east-1",
			URIStyle: "path",
		},
	}
	data, err := RenderLitestreamConfig("/data/app.db", "/state/.app.db-litestream", repl, "AKIA", "SECRET")
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	want := `snapshot:
    interval: 24h
    retention: 24h
dbs:
    - path: /data/app.db
      meta-path: /state/.app.db-litestream
      replica:
        type: s3
        sync-interval: 1s
        endpoint: http://127.0.0.1:9000
        bucket: backups
        path: sqlite/appdb
        region: us-east-1
        access-key-id: AKIA
        secret-access-key: SECRET
        force-path-style: true
`
	if got != want {
		t.Errorf("rendered config mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if strings.Contains(got, "replicas:") {
		t.Error("config uses deprecated replicas key")
	}

	// virtual-host omits force-path-style; skip-verify and AWS default
	// endpoint are only emitted when set.
	repl.S3.URIStyle = "virtual-host"
	repl.S3.Endpoint = ""
	repl.S3.SkipVerify = true
	data, err = RenderLitestreamConfig("/data/app.db", "/state/meta", repl, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got = string(data)
	if strings.Contains(got, "force-path-style") {
		t.Error("virtual-host must not force path style")
	}
	if !strings.Contains(got, "skip-verify: true") {
		t.Error("skip-verify missing")
	}
	if strings.Contains(got, "endpoint:") {
		t.Error("empty endpoint should be omitted")
	}
	if strings.Contains(got, "access-key-id") || strings.Contains(got, "secret-access-key") {
		t.Error("empty credentials should be omitted")
	}
}

func TestWriteConfigFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "litestream.yml")
	if err := WriteConfigFile(path, []byte("a: b\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if string(data) != "a: b\n" {
		t.Errorf("content = %q", data)
	}
}

func TestLitestreamDownload(t *testing.T) {
	tests := []struct {
		arch     string
		wantArch string
		wantSHA  string
	}{
		{"amd64", "x86_64", "cfb371176d164437ae869f8351cfde49bd1804ae71c61923f75c9cba9c9c006d"},
		{"arm64", "arm64", "f8ca4a050095c1efbda2c4365172e61bf9d955ea0d9ac42f448b52e51819baa5"},
	}
	for _, tt := range tests {
		d, err := LitestreamDownload(tt.arch)
		if err != nil {
			t.Fatalf("%s: %v", tt.arch, err)
		}
		wantURL := "https://github.com/benbjohnson/litestream/releases/download/v0.5.17/litestream-0.5.17-linux-" + tt.wantArch + ".tar.gz"
		if d.URL != wantURL {
			t.Errorf("%s URL = %q, want %q", tt.arch, d.URL, wantURL)
		}
		if d.Member != "litestream" || d.Binary != "litestream" {
			t.Errorf("%s member = %q binary = %q", tt.arch, d.Member, d.Binary)
		}
		if d.SHA256 != tt.wantSHA {
			t.Errorf("%s SHA = %q", tt.arch, d.SHA256)
		}
	}
	if _, err := LitestreamDownload("386"); err == nil {
		t.Error("unsupported arch should error")
	}
}

func TestSQLitePackageCommand(t *testing.T) {
	tests := []struct {
		pm   string
		args string
	}{
		{"apt-get", "install -y --no-install-recommends sqlite3"},
		{"dnf", "install -y sqlite"},
		{"yum", "install -y sqlite"},
		{"pacman", "-S --noconfirm --needed sqlite"},
		{"zypper", "install -y sqlite3"},
		{"apk", "add sqlite"},
	}
	for _, tt := range tests {
		got, ok := SQLitePackageCommand(tt.pm)
		if !ok {
			t.Errorf("%s not recognized", tt.pm)
			continue
		}
		if got.Name != tt.pm || strings.Join(got.Args, " ") != tt.args {
			t.Errorf("%s = %s %s", tt.pm, got.Name, strings.Join(got.Args, " "))
		}
	}
	if _, ok := SQLitePackageCommand("brew"); ok {
		t.Error("brew must not be supported (Linux-only)")
	}
}

func TestPrivilegePrefixFor(t *testing.T) {
	if p, err := PrivilegePrefixFor(true, false, false); err != nil || len(p) != 0 {
		t.Errorf("root: %v %v", p, err)
	}
	if p, err := PrivilegePrefixFor(false, true, true); err != nil || strings.Join(p, " ") != "sudo -n" {
		t.Errorf("sudo: %v %v", p, err)
	}
	if _, err := PrivilegePrefixFor(false, false, false); err == nil {
		t.Error("no sudo should error")
	}
	if _, err := PrivilegePrefixFor(false, true, false); err == nil {
		t.Error("sudo without -n should error")
	}
}

func TestLitestreamCandidates(t *testing.T) {
	got := LitestreamCandidates("/opt/litestream", "/opt/litestream", "/usr/bin/litestream", "/home/u")
	want := []string{"/opt/litestream", "/usr/bin/litestream", "/usr/local/bin/litestream", "/home/u/.local/bin/litestream"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

func TestSameReplication(t *testing.T) {
	mk := func() *config.SQLiteReplicationConfig {
		return &config.SQLiteReplicationConfig{
			SyncInterval: "1s", SnapshotInterval: "24h", Retention: "24h",
			S3: &config.SQLiteS3Config{Endpoint: "http://x", Bucket: "b", Path: "p", Region: "r", URIStyle: "path"},
		}
	}
	if !SameReplication(mk(), mk()) {
		t.Error("identical configs reported different")
	}
	a, b := mk(), mk()
	b.S3.Bucket = "other"
	if SameReplication(a, b) {
		t.Error("different buckets reported identical")
	}
	if SameReplication(nil, mk()) || SameReplication(mk(), nil) || !SameReplication(nil, nil) {
		t.Error("nil handling wrong")
	}
}

func TestBackupAndMove(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "app.db")
	for _, p := range []string{db, db + "-wal", db + "-shm"} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	backup, err := UniqueBackupDir(db, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(backup) != "app.db.pre-restore-20260102T030405Z" {
		t.Errorf("backup dir = %q", backup)
	}
	moved, err := MoveDBSidecars(db, backup)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 3 {
		t.Errorf("moved %d files, want 3", len(moved))
	}
	for _, p := range DBSidecars(db) {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists", p)
		}
	}

	// Simulate a restore into a temp dir, then move it into place.
	restoreDir := t.TempDir()
	for _, suffix := range []string{"", "-wal"} {
		if err := os.WriteFile(filepath.Join(restoreDir, "app.db"+suffix), []byte("new"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := MoveRestoredDB(restoreDir, db); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{db, db + "-wal"} {
		data, err := os.ReadFile(p)
		if err != nil || string(data) != "new" {
			t.Errorf("%s = %q, %v", p, data, err)
		}
	}
	if _, err := os.Stat(db + "-shm"); !os.IsNotExist(err) {
		t.Error("-shm should not be created")
	}
}

func TestRecentLogError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "litestream.log")
	content := "level=INFO msg=starting\nlevel=ERROR msg=boom\nlevel=INFO msg=still running\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RecentLogError(path); !strings.Contains(got, "boom") {
		t.Errorf("RecentLogError = %q", got)
	}
	if got := LastLogLine(path); got != "level=INFO msg=still running" {
		t.Errorf("LastLogLine = %q", got)
	}
	empty := filepath.Join(dir, "empty.log")
	os.WriteFile(empty, []byte("level=INFO ok\n"), 0o644)
	if got := RecentLogError(empty); got != "" {
		t.Errorf("RecentLogError(clean) = %q", got)
	}
}
