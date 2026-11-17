package cli

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseBenchDSNFull(t *testing.T) {
	target, err := parseBenchDSN("postgres://user:p%40ss@db.example.com:6432/appdb?sslmode=require&application_name=bench")
	if err != nil {
		t.Fatal(err)
	}
	want := benchTarget{
		Host:     "db.example.com",
		Port:     6432,
		User:     "user",
		Password: "p@ss",
		Database: "appdb",
		ExtraEnv: []string{"PGAPPNAME=bench", "PGSSLMODE=require"},
	}
	if !reflect.DeepEqual(target, want) {
		t.Errorf("target = %+v, want %+v", target, want)
	}
}

func TestParseBenchDSNDefaults(t *testing.T) {
	target, err := parseBenchDSN("postgresql://user@db.example.com/appdb")
	if err != nil {
		t.Fatal(err)
	}
	if target.Port != 5432 {
		t.Errorf("port = %d, want 5432", target.Port)
	}
	if target.Password != "" {
		t.Errorf("password = %q, want empty", target.Password)
	}
	if target.Database != "appdb" {
		t.Errorf("database = %q", target.Database)
	}
}

func TestParseBenchDSNIPv6(t *testing.T) {
	target, err := parseBenchDSN("postgres://user@[::1]/appdb")
	if err != nil {
		t.Fatal(err)
	}
	if target.Host != "::1" {
		t.Errorf("host = %q, want ::1", target.Host)
	}
	if target.Port != 5432 {
		t.Errorf("port = %d", target.Port)
	}
}

func TestParseBenchDSNSSLSystem(t *testing.T) {
	target, err := parseBenchDSN("postgres://user@db/appdb?sslrootcert=system")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(target.ExtraEnv, []string{"PGSSLROOTCERT=system"}) {
		t.Errorf("extra env = %q", target.ExtraEnv)
	}
	if len(target.Mounts) != 0 {
		t.Errorf("mounts = %q", target.Mounts)
	}
}

func TestParseBenchDSNTLSMount(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "root cert.pem")
	if err := os.WriteFile(cert, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := "postgres://user@db/appdb?sslrootcert=" + url.QueryEscape(cert)
	target, err := parseBenchDSN(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(target.Mounts) != 1 {
		t.Fatalf("mounts = %q", target.Mounts)
	}
	mount := target.Mounts[0]
	if !strings.Contains(mount, ":/pgcli-bench/tls/0-root_cert.pem:ro,Z") {
		t.Errorf("mount = %q", mount)
	}
	if !reflect.DeepEqual(target.ExtraEnv, []string{"PGSSLROOTCERT=/pgcli-bench/tls/0-root_cert.pem"}) {
		t.Errorf("extra env = %q", target.ExtraEnv)
	}
}

func TestParseBenchDSNErrors(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"bad scheme", "mysql://user@db/appdb", "scheme"},
		{"opaque", "postgres:user@db/appdb", "expected postgres://"},
		{"fragment", "postgres://user@db/appdb#frag", "fragments"},
		{"multi host", "postgres://user@h1,h2/appdb", "multiple hosts"},
		{"missing host", "postgres:///appdb", "missing host"},
		{"missing user", "postgres://db/appdb", "missing user"},
		{"empty user", "postgres://:pass@db/appdb", "missing user"},
		{"missing db", "postgres://user@db", "missing database"},
		{"db slash", "postgres://user@db/a/b", "must not contain"},
		{"bad port", "postgres://user@db:99999/appdb", "bad port"},
		{"unknown param", "postgres://user@db/appdb?foo=bar", "unsupported parameter"},
		{"duplicate param", "postgres://user@db/appdb?sslmode=require&sslmode=disable", "specified once"},
		{"forbidden param", "postgres://user@db/appdb?host=evil", "not in the query string"},
		{"empty TLS path", "postgres://user@db/appdb?sslrootcert=", "must not be empty"},
		{"missing TLS file", "postgres://user@db/appdb?sslrootcert=" + url.QueryEscape(dir+"/missing.pem"), "TLS path"},
		{"TLS dir as file", "postgres://user@db/appdb?sslrootcert=" + url.QueryEscape(dir), "regular file"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseBenchDSN(tt.dsn)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBenchTargetEnv(t *testing.T) {
	target := benchTarget{
		Host:     "db",
		Port:     5432,
		User:     "user",
		Password: "secret",
		Database: "appdb",
		ExtraEnv: []string{"PGSSLMODE=require"},
	}
	env := target.env()
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"PGHOST=db", "PGPORT=5432", "PGUSER=user", "PGDATABASE=appdb",
		"PGPASSWORD=secret", "PGSSLMODE=require", "PGAPPNAME=pgcli-bench",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q: %q", want, env)
		}
	}
}

func TestBenchTargetEnvNoPassword(t *testing.T) {
	target := benchTarget{Host: "db", Port: 5432, User: "user", Database: "appdb"}
	for _, kv := range target.env() {
		if strings.HasPrefix(kv, "PGPASSWORD=") {
			t.Errorf("unexpected PGPASSWORD: %q", kv)
		}
	}
}

func TestBenchTargetEnvKeepsDSNAppName(t *testing.T) {
	target := benchTarget{
		Host: "db", Port: 5432, User: "user", Database: "appdb",
		ExtraEnv: []string{"PGAPPNAME=bench"},
	}
	count := 0
	for _, kv := range target.env() {
		if strings.HasPrefix(kv, "PGAPPNAME=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("PGAPPNAME entries = %d, env = %q", count, target.env())
	}
}

func TestBenchTargetDisplay(t *testing.T) {
	target := benchTarget{Host: "::1", Port: 5432, User: "user", Database: "db"}
	if got, want := target.display(), "user@[::1]:5432/db"; got != want {
		t.Errorf("display = %q, want %q", got, want)
	}
}
