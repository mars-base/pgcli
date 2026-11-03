package podman

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func testClickHouseConfig(t *testing.T) config.ClickHouseConfig {
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

func TestClickHouseCreateArgs(t *testing.T) {
	cc := testClickHouseConfig(t)
	got := clickHouseCreateArgs(cc)
	want := []string{
		"run", "-d",
		"--name", "pgcli-clickhouse-test",
		"--network", "host",
		"--http-proxy=false",
		"--restart", "unless-stopped",
		"--stop-timeout", "30",
		"--ulimit", "nofile=262144:262144",
		"--entrypoint", "/bin/sh",
		"-v", cc.DataDir + ":/var/lib/clickhouse:z",
		"-v", cc.LogDir + ":/var/log/clickhouse-server:z",
		"-v", filepath.Dir(cc.ConfigPath) + ":/etc/clickhouse-server/config.d:ro,z",
		"-v", cc.BackupDir + ":/var/lib/clickhouse-backups:z",
		"-v", cc.SecretPath + ":/run/secrets/admin_password:ro,z",
		"-e", "CLICKHOUSE_USER=admin",
		"-e", "CLICKHOUSE_PASSWORD_FILE=/run/secrets/admin_password",
		"-e", "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1",
		cc.ImageTag,
		"-c", `export PGCLI_CLICKHOUSE_ADMIN_PASSWORD="$(cat /run/secrets/admin_password)"; exec /entrypoint.sh`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("clickHouseCreateArgs:\n got %q\nwant %q", got, want)
	}
	for _, arg := range got {
		if strings.Contains(arg, cc.AdminPassword) {
			t.Fatalf("container argv leaks the admin password: %q", arg)
		}
	}
}

func TestClickHouseExecArgs(t *testing.T) {
	env := []string{"CLICKHOUSE_USER=admin", "CLICKHOUSE_PASSWORD=s3cret"}
	got := clickHouseExecArgs(ClickHouseExecSpec{
		Command:    "client",
		Env:        env,
		ClientArgs: []string{"--host", "127.0.0.1", "--port", "19000"},
	}, "pgcli-clickhouse-test")
	want := []string{
		"exec",
		"-e", "CLICKHOUSE_USER", "-e", "CLICKHOUSE_PASSWORD",
		"pgcli-clickhouse-test", "sh", "-c",
		`exec clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" "$@"`,
		"clickhouse-client",
		"--host", "127.0.0.1", "--port", "19000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("clickHouseExecArgs:\n got %q\nwant %q", got, want)
	}
	for _, arg := range got {
		if strings.Contains(arg, "s3cret") {
			t.Fatalf("exec argv leaks the password: %q", arg)
		}
		if arg == "-i" || arg == "-it" {
			t.Fatalf("non-interactive exec must not attach a TTY/stdin: %q", got)
		}
	}

	interactive := clickHouseExecArgs(ClickHouseExecSpec{
		Command:     "client",
		Env:         env,
		Interactive: true,
	}, "c")
	if interactive[1] != "-it" {
		t.Errorf("interactive exec flag = %q, want -it", interactive[1])
	}
	if strings.Contains(strings.Join(interactive, " "), "s3cret") {
		t.Error("interactive exec argv leaks the password")
	}

	shell := clickHouseExecArgs(ClickHouseExecSpec{Command: "shell", Interactive: true}, "c")
	if want := []string{"exec", "-it", "c", "bash"}; !reflect.DeepEqual(shell, want) {
		t.Errorf("shell args = %q, want %q", shell, want)
	}
}

func TestClickHouseHTTPQuery(t *testing.T) {
	var gotQuery, gotDatabase, gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll: %v", err)
		}
		gotQuery = string(body)
		gotDatabase = r.URL.Query().Get("database")
		gotUser, gotPass, _ = r.BasicAuth()
		io.WriteString(w, "26.8.36.3\n")
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	cc := &config.ClickHouseConfig{
		Listen:        "127.0.0.1",
		HTTPPort:      port,
		AdminUser:     "admin",
		AdminPassword: "s3cret",
	}
	out, err := clickHouseHTTPQuery(cc, []string{"--host", "127.0.0.1", "--port", "9000", "--database", "demo"}, "SELECT version()")
	if err != nil {
		t.Fatalf("clickHouseHTTPQuery: %v", err)
	}
	if string(out) != "26.8.36.3\n" {
		t.Errorf("output = %q", out)
	}
	if gotQuery != "SELECT version()" || gotDatabase != "demo" {
		t.Errorf("query=%q database=%q", gotQuery, gotDatabase)
	}
	if gotUser != "admin" || gotPass != "s3cret" {
		t.Errorf("basic auth = %q/%q", gotUser, gotPass)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Code: 62. DB::Exception: Syntax error", http.StatusBadRequest)
	}))
	defer errSrv.Close()
	eu, _ := url.Parse(errSrv.URL)
	ep, _ := strconv.Atoi(eu.Port())
	errCC := *cc
	errCC.HTTPPort = ep
	_, err = clickHouseHTTPQuery(&errCC, nil, "not sql")
	if err == nil || !strings.Contains(err.Error(), "Syntax error") {
		t.Errorf("error = %v, want the server error text", err)
	}
}

func TestClickHousePathWithin(t *testing.T) {
	tests := []struct {
		path, dir string
		want      bool
	}{
		{"/srv/addon/clickhouse/a/data", "/srv/addon/clickhouse/a", true},
		{"/srv/addon/clickhouse/a", "/srv/addon/clickhouse/a", true},
		{"/srv/addon/clickhouse/ab/data", "/srv/addon/clickhouse/a", false},
		{"/srv/other", "/srv/addon/clickhouse/a", false},
	}
	for _, tt := range tests {
		if got := pathWithin(tt.path, tt.dir); got != tt.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v", tt.path, tt.dir, got, tt.want)
		}
	}
}
