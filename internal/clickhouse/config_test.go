package clickhouse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

func testConfig(t *testing.T) config.ClickHouseConfig {
	t.Helper()
	dir := t.TempDir()
	return config.ClickHouseConfig{
		ContainerName: "pgcli-clickhouse-test",
		Name:          "test",
		Version:       config.DefaultClickHouseVersion,
		ImageTag:      config.DefaultClickHouseImageTag,
		ConfigPath:    filepath.Join(dir, "config.d", "zz-pgcli.xml"),
		DataDir:       filepath.Join(dir, "data"),
		LogDir:        filepath.Join(dir, "logs"),
		BackupDir:     filepath.Join(dir, "backups"),
		SecretPath:    filepath.Join(dir, "secrets", "admin_password"),
		Listen:        "127.0.0.1",
		HTTPPort:      18123,
		TCPPort:       19000,
		AdminUser:     "admin",
		AdminPassword: "s3cret",
	}
}

func TestRenderConfig(t *testing.T) {
	cc := testConfig(t)
	out, err := RenderConfig(cc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	for _, want := range []string{
		"<?xml version=\"1.0\"?>",
		"<clickhouse>",
		"<listen_host>127.0.0.1</listen_host>",
		"<listen_try>1</listen_try>",
		"<http_port>18123</http_port>",
		"<tcp_port>19000</tcp_port>",
		"<level>information</level>",
		"<log>/var/log/clickhouse-server/clickhouse-server.log</log>",
		"<errorlog>/var/log/clickhouse-server/clickhouse-server.err.log</errorlog>",
		"<console>1</console>",
		"<backups>",
		"<allowed_disk>backups</allowed_disk>",
		"<allowed_path>/var/lib/clickhouse-backups/</allowed_path>",
		"<storage_configuration>",
		"<disks>",
		"<backups>",
		"<type>local</type>",
		"<path>/var/lib/clickhouse-backups/</path>",
		"</clickhouse>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, cc.AdminPassword) {
		t.Error("rendered config must not contain the admin password")
	}
}

func TestRenderConfigListenHosts(t *testing.T) {
	tests := []struct {
		listen string
		want   []string
	}{
		{"127.0.0.1", []string{"127.0.0.1"}},
		{"", []string{"127.0.0.1"}},
		{"0.0.0.0", []string{"0.0.0.0"}},
		{"::", []string{"::", "0.0.0.0"}},
		{"[::]", []string{"::", "0.0.0.0"}},
		{"10.20.30.40", []string{"10.20.30.40", "127.0.0.1"}},
		{"::1", []string{"::1"}},
	}
	for _, tt := range tests {
		cc := testConfig(t)
		cc.Listen = tt.listen
		out, err := RenderConfig(cc)
		if err != nil {
			t.Fatalf("RenderConfig(%q): %v", tt.listen, err)
		}
		for _, host := range tt.want {
			if !strings.Contains(out, "<listen_host>"+host+"</listen_host>") {
				t.Errorf("listen %q: missing listen_host %s", tt.listen, host)
			}
		}
		if got := strings.Count(out, "<listen_host>"); got != len(tt.want) {
			t.Errorf("listen %q: %d listen_host elements, want %d:\n%s", tt.listen, got, len(tt.want), out)
		}
	}
}

func TestRenderConfigSettings(t *testing.T) {
	cc := testConfig(t)
	cc.Settings = map[string]string{
		"logger.level":                          "warning",
		"max_server_memory_usage":               "2000000000",
		"merge_tree.max_suspicious":             "0",
		"query_log.flush_interval_milliseconds": "5000",
	}
	out, err := RenderConfig(cc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if strings.Count(out, "<level>") != 1 {
		t.Errorf("logger.level should be folded into the managed logger block:\n%s", out)
	}
	if !strings.Contains(out, "<level>warning</level>") {
		t.Errorf("logger.level override missing:\n%s", out)
	}
	for _, want := range []string{
		"<max_server_memory_usage>2000000000</max_server_memory_usage>",
		"<merge_tree>",
		"<max_suspicious>0</max_suspicious>",
		"<query_log>",
		"<flush_interval_milliseconds>5000</flush_interval_milliseconds>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q:\n%s", want, out)
		}
	}
}

func TestRenderConfigEscapesValues(t *testing.T) {
	cc := testConfig(t)
	cc.Settings = map[string]string{"custom.value": `a<b & "c"`}
	out, err := RenderConfig(cc)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(out, "<value>a&lt;b &amp; &#34;c&#34;</value>") {
		t.Errorf("XML escaping missing:\n%s", out)
	}
}

func TestRenderConfigRejectsDuplicateManagedKey(t *testing.T) {
	cc := testConfig(t)
	cc.Settings = map[string]string{"http_port": "9999"}
	if _, err := RenderConfig(cc); err == nil {
		t.Fatal("RenderConfig should reject a duplicate managed key")
	}
}

func TestWriteConfigAndSecret(t *testing.T) {
	cc := testConfig(t)
	if err := WriteConfig(cc); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	info, err := os.Stat(cc.ConfigPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("config mode = %o, want 644", got)
	}
	content, err := os.ReadFile(cc.ConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(content), "<http_port>18123</http_port>") {
		t.Errorf("config content:\n%s", content)
	}

	if err := WriteSecret(cc); err != nil {
		t.Fatalf("WriteSecret: %v", err)
	}
	secretInfo, err := os.Stat(cc.SecretPath)
	if err != nil {
		t.Fatalf("stat secret: %v", err)
	}
	if got := secretInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("secret mode = %o, want 600", got)
	}
	secret, _ := os.ReadFile(cc.SecretPath)
	if string(secret) != cc.AdminPassword {
		t.Errorf("secret = %q, want %q", secret, cc.AdminPassword)
	}

	// Rewriting the same password must not recreate the file (the entrypoint
	// reads it at start; keep mtime stable).
	before := secretInfo.ModTime()
	time.Sleep(10 * time.Millisecond)
	if err := WriteSecret(cc); err != nil {
		t.Fatalf("WriteSecret again: %v", err)
	}
	again, _ := os.Stat(cc.SecretPath)
	if !again.ModTime().Equal(before) {
		t.Error("WriteSecret rewrote an unchanged secret")
	}

	cc.AdminPassword = "new-pass"
	if err := WriteSecret(cc); err != nil {
		t.Fatalf("WriteSecret change: %v", err)
	}
	secret, _ = os.ReadFile(cc.SecretPath)
	if string(secret) != "new-pass" {
		t.Errorf("secret = %q, want new-pass", secret)
	}
}

func TestConnectHostAndEndpoints(t *testing.T) {
	for listen, want := range map[string]string{
		"":          "127.0.0.1",
		"0.0.0.0":   "127.0.0.1",
		"::":        "127.0.0.1",
		"[::]":      "127.0.0.1",
		"127.0.0.1": "127.0.0.1",
		"10.0.0.5":  "10.0.0.5",
	} {
		if got := ConnectHost(listen); got != want {
			t.Errorf("ConnectHost(%q) = %q, want %q", listen, got, want)
		}
	}

	cc := testConfig(t)
	cc.Listen = "0.0.0.0"
	if got, want := HTTPURL(cc), "http://127.0.0.1:18123"; got != want {
		t.Errorf("HTTPURL = %q, want %q", got, want)
	}
	if got, want := TCPAddr(cc), "127.0.0.1:19000"; got != want {
		t.Errorf("TCPAddr = %q, want %q", got, want)
	}

	args := ClientArgs(cc, "")
	if strings.Join(args, " ") != "--host 127.0.0.1 --port 19000" {
		t.Errorf("ClientArgs = %v", args)
	}
	args = ClientArgs(cc, "demo")
	if strings.Join(args, " ") != "--host 127.0.0.1 --port 19000 --database demo" {
		t.Errorf("ClientArgs(demo) = %v", args)
	}
}

func TestSQLBuilders(t *testing.T) {
	if got, want := Ident("odd`name"), "`odd\\`name`"; got != want {
		t.Errorf("Ident = %q, want %q", got, want)
	}
	if got, want := Ident(`a\b`), "`a\\\\b`"; got != want {
		t.Errorf("Ident = %q, want %q", got, want)
	}
	if got, want := StringLiteral(`it's \ ok`), `'it\'s \\ ok'`; got != want {
		t.Errorf("StringLiteral = %q, want %q", got, want)
	}

	tests := []struct {
		name  string
		build func() (string, error)
		want  string
	}{
		{
			name:  "create database",
			build: func() (string, error) { return CreateDatabaseSQL("demo", "", "", false) },
			want:  "CREATE DATABASE `demo` ENGINE = Atomic",
		},
		{
			name:  "create database full",
			build: func() (string, error) { return CreateDatabaseSQL("demo", "Replicated", "it's mine", true) },
			want:  "CREATE DATABASE IF NOT EXISTS `demo` ENGINE = Replicated COMMENT 'it\\'s mine'",
		},
		{
			name:  "drop database",
			build: func() (string, error) { return DropDatabaseSQL("demo", true) },
			want:  "DROP DATABASE IF EXISTS `demo`",
		},
		{
			name: "create user",
			build: func() (string, error) {
				return CreateUserSQL("app", "p'w", "demo",
					[]string{"ANY", "IP '10.0.0.0/8'"}, true, true)
			},
			want: "CREATE USER IF NOT EXISTS `app` IDENTIFIED WITH sha256_password BY 'p\\'w' HOST ANY, IP '10.0.0.0/8' DEFAULT DATABASE `demo` SETTINGS readonly = 1",
		},
		{
			name:  "create user default host",
			build: func() (string, error) { return CreateUserSQL("app", "pw", "", nil, false, false) },
			want:  "CREATE USER `app` IDENTIFIED WITH sha256_password BY 'pw' HOST ANY",
		},
		{
			name:  "drop user",
			build: func() (string, error) { return DropUserSQL("app", false) },
			want:  "DROP USER `app`",
		},
		{
			name:  "show grants",
			build: func() (string, error) { return ShowGrantsSQL("app") },
			want:  "SHOW GRANTS FOR `app`",
		},
		{
			name:  "grant all",
			build: func() (string, error) { return GrantSQL("app", []string{"select", "insert"}, "", "", false, false) },
			want:  "GRANT SELECT, INSERT ON * TO `app`",
		},
		{
			name:  "grant table with option",
			build: func() (string, error) { return GrantSQL("app", []string{"SELECT"}, "demo", "events", false, true) },
			want:  "GRANT SELECT ON `demo`.`events` TO `app` WITH GRANT OPTION",
		},
		{
			name:  "revoke database",
			build: func() (string, error) { return GrantSQL("app", []string{"INSERT"}, "demo", "", true, true) },
			want:  "REVOKE INSERT ON `demo`.* FROM `app`",
		},
		{
			name:  "backup",
			build: func() (string, error) { return BackupDatabaseSQL("demo", "demo-1.zip") },
			want:  "BACKUP DATABASE `demo` TO Disk('backups', 'demo-1.zip')",
		},
		{
			name:  "restore as",
			build: func() (string, error) { return RestoreDatabaseSQL("demo", "demo_copy", "demo-1.zip") },
			want:  "RESTORE DATABASE `demo` AS `demo_copy` FROM Disk('backups', 'demo-1.zip')",
		},
	}
	for _, tt := range tests {
		got, err := tt.build()
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestSQLBuilderValidation(t *testing.T) {
	if _, err := CreateDatabaseSQL("bad name", "", "", false); err == nil {
		t.Error("CreateDatabaseSQL should reject a space in the name")
	}
	if _, err := CreateDatabaseSQL("demo", "MergeTree()", "", false); err == nil {
		t.Error("CreateDatabaseSQL should reject a parameterized engine")
	}
	if _, err := CreateUserSQL("app", "a\nb", "", nil, false, false); err == nil {
		t.Error("CreateUserSQL should reject a password with control characters")
	}
	if _, err := CreateUserSQL("app", "pw", "bad name", nil, false, false); err == nil {
		t.Error("CreateUserSQL should reject an invalid default database")
	}
	if _, err := NormalizePrivileges("SELECT, bad priv"); err == nil {
		t.Error("NormalizePrivileges should reject an invalid privilege")
	}
	if _, err := NormalizePrivileges(" , "); err == nil {
		t.Error("NormalizePrivileges should require at least one privilege")
	}
	got, err := NormalizePrivileges("select, insert")
	if err != nil {
		t.Fatalf("NormalizePrivileges: %v", err)
	}
	if strings.Join(got, ",") != "SELECT,INSERT" {
		t.Errorf("NormalizePrivileges = %v", got)
	}
	if _, err := GrantSQL("app", []string{"SELECT"}, "demo", "", false, false); err != nil {
		t.Errorf("GrantSQL database scope: %v", err)
	}
	if _, err := GrantSQL("app", []string{"SELECT"}, "", "events", false, false); err == nil {
		t.Error("GrantSQL should require a database for a table")
	}
}

func TestHostClauses(t *testing.T) {
	if got, err := HostIPClause("10.0.0.1"); err != nil || got != "IP '10.0.0.1'" {
		t.Errorf("HostIPClause(ip) = %q, %v", got, err)
	}
	if got, err := HostIPClause("10.0.0.0/8"); err != nil || got != "IP '10.0.0.0/8'" {
		t.Errorf("HostIPClause(cidr) = %q, %v", got, err)
	}
	if _, err := HostIPClause("not-an-ip"); err == nil {
		t.Error("HostIPClause should reject garbage")
	}
	if got, err := HostNameClause("app.internal"); err != nil || got != "NAME 'app.internal'" {
		t.Errorf("HostNameClause = %q, %v", got, err)
	}
	if _, err := HostNameClause("-bad-"); err == nil {
		t.Error("HostNameClause should reject a leading dash")
	}
}

func TestBackupTarget(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 19, 12, 34, 56, 0, time.UTC)

	disk, host, err := BackupTarget(dir, "demo", "", now)
	if err != nil {
		t.Fatalf("BackupTarget default: %v", err)
	}
	if disk != "demo-20261019-123456.zip" {
		t.Errorf("diskPath = %q", disk)
	}
	if host != filepath.Join(dir, disk) {
		t.Errorf("hostPath = %q", host)
	}

	disk, host, err = BackupTarget(dir, "demo", "nightly", now)
	if err != nil {
		t.Fatalf("BackupTarget relative: %v", err)
	}
	if disk != "nightly.zip" || host != filepath.Join(dir, "nightly.zip") {
		t.Errorf("BackupTarget(nightly) = %q, %q", disk, host)
	}

	abs := filepath.Join(dir, "custom.zip")
	disk, host, err = BackupTarget(dir, "demo", abs, now)
	if err != nil || disk != "custom.zip" || host != abs {
		t.Errorf("BackupTarget(absolute) = %q, %q, %v", disk, host, err)
	}

	if _, _, err := BackupTarget(dir, "demo", "../escape.zip", now); err == nil {
		t.Error("BackupTarget should reject a path outside the backup dir")
	}
	if _, _, err := BackupTarget(dir, "demo", "dump.tar", now); err == nil {
		t.Error("BackupTarget should reject a non-.zip extension")
	}
}

func TestRestoreSource(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "demo.zip")
	if err := os.WriteFile(good, []byte("zip"), 0o644); err != nil {
		t.Fatal(err)
	}

	disk, host, err := RestoreSource(dir, "demo.zip")
	if err != nil {
		t.Fatalf("RestoreSource: %v", err)
	}
	if disk != "demo.zip" || host != good {
		t.Errorf("RestoreSource = %q, %q", disk, host)
	}

	tests := []struct {
		file    string
		wantSub string
	}{
		{"", "required"},
		{"missing.zip", "no such file"},
		{"../escape.zip", "must be inside"},
		{"demo.tar", ".zip archive"},
	}
	for _, tt := range tests {
		if _, _, err := RestoreSource(dir, tt.file); err == nil || !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("RestoreSource(%q) = %v, want %q", tt.file, err, tt.wantSub)
		}
	}

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "x.zip"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RestoreSource(dir, "sub/x.zip"); err != nil {
		t.Errorf("RestoreSource nested: %v", err)
	}
	if _, _, err := RestoreSource(dir, sub); err == nil || !strings.Contains(err.Error(), "must be a .zip") {
		t.Errorf("RestoreSource(directory) = %v, want extension rejection", err)
	}
}

func TestListBackupFiles(t *testing.T) {
	dir := t.TempDir()
	if got, err := ListBackupFiles(dir); err != nil || len(got) != 0 {
		t.Errorf("ListBackupFiles(empty) = %v, %v", got, err)
	}
	if got, err := ListBackupFiles(filepath.Join(dir, "missing")); err != nil || len(got) != 0 {
		t.Errorf("ListBackupFiles(missing) = %v, %v", got, err)
	}

	old := filepath.Join(dir, "old.zip")
	newer := filepath.Join(dir, "newer.zip")
	notZip := filepath.Join(dir, "notes.txt")
	for _, f := range []string{old, newer, notZip} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	got, err := ListBackupFiles(dir)
	if err != nil {
		t.Fatalf("ListBackupFiles: %v", err)
	}
	if len(got) != 2 || got[0] != "newer.zip" || got[1] != "old.zip" {
		t.Errorf("ListBackupFiles = %v, want [newer.zip old.zip]", got)
	}
}
