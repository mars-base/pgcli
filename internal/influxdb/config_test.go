package influxdb

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func testInstance(t *testing.T) config.InfluxDBConfig {
	t.Helper()
	ic := config.InfluxDBConfig{
		Name:          "metrics",
		ConfigPath:    filepath.Join(t.TempDir(), "influxdb.conf"),
		DataDir:       filepath.Join(t.TempDir(), "data"),
		Listen:        "0.0.0.0",
		HostPort:      8086,
		RPCPort:       8088,
		AdminUser:     "admin",
		AdminPassword: "secret",
	}
	return ic
}

func TestRenderConfig(t *testing.T) {
	ic := testInstance(t)
	ic.Listen = "0.0.0.0"
	ic.HostPort = 8086
	ic.RPCPort = 8088

	want := `reporting-disabled = true
bind-address = "127.0.0.1:8088"

[meta]
  dir = "/var/lib/influxdb/meta"

[data]
  dir = "/var/lib/influxdb/data"
  wal-dir = "/var/lib/influxdb/wal"

[http]
  bind-address = "0.0.0.0:8086"
  auth-enabled = true
`
	got, err := RenderConfig(ic)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if got != want {
		t.Errorf("RenderConfig:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "secret") {
		t.Error("rendered config must not contain the admin password")
	}
}

func TestRenderConfigExtraOptions(t *testing.T) {
	ic := testInstance(t)
	ic.DataDir = "/srv/pg/influx"
	ic.ExtraConfig = []string{
		`data.index-version="tsi1"`,
		"data.cache-max-memory-size=1000000000",
		"http.max-row-limit=0",
		`logging.level="warn"`,
		"query-log-enabled=true",
	}
	got, err := RenderConfig(ic)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	for _, want := range []string{
		"  index-version = \"tsi1\"\n",
		"  cache-max-memory-size = 1000000000\n",
		"  max-row-limit = 0\n",
		"\n[logging]\n  level = \"warn\"\n",
		"query-log-enabled = true\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderConfig missing %q:\n%s", want, got)
		}
	}
	// Overrides live inside their table: [data] must not be reopened.
	if strings.Count(got, "[data]") != 1 || strings.Count(got, "[http]") != 1 {
		t.Errorf("tables reopened in:\n%s", got)
	}
}

func TestRenderConfigRejectsBadOption(t *testing.T) {
	ic := testInstance(t)
	ic.ExtraConfig = []string{"not-an-assignment"}
	if _, err := RenderConfig(ic); err == nil {
		t.Fatal("RenderConfig accepted a malformed --config-option")
	}
}

func TestRenderConfigIPv6Listen(t *testing.T) {
	ic := testInstance(t)
	ic.Listen = "::1"
	ic.HostPort = 18086

	got, err := RenderConfig(ic)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if !strings.Contains(got, `bind-address = "[::1]:18086"`) {
		t.Errorf("RenderConfig with IPv6 listen:\n%s", got)
	}
}

func TestWriteConfig(t *testing.T) {
	ic := testInstance(t)
	if err := WriteConfig(ic); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	data, err := os.ReadFile(ic.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := RenderConfig(ic)
	if err != nil {
		t.Fatalf("RenderConfig: %v", err)
	}
	if string(data) != want {
		t.Errorf("config content = %q", data)
	}
	info, err := os.Stat(ic.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", info.Mode().Perm())
	}
}

func TestConnectHostAndPingURL(t *testing.T) {
	tests := []struct {
		listen   string
		wantHost string
		wantURL  string
	}{
		{"0.0.0.0", "127.0.0.1", "http://127.0.0.1:8086/ping"},
		{"", "127.0.0.1", "http://127.0.0.1:8086/ping"},
		{"::", "127.0.0.1", "http://127.0.0.1:8086/ping"},
		{"127.0.0.1", "127.0.0.1", "http://127.0.0.1:8086/ping"},
		{"10.0.0.5", "10.0.0.5", "http://10.0.0.5:8086/ping"},
		{"::1", "::1", "http://[::1]:8086/ping"},
	}
	for _, tt := range tests {
		if got := ConnectHost(tt.listen); got != tt.wantHost {
			t.Errorf("ConnectHost(%q) = %q, want %q", tt.listen, got, tt.wantHost)
		}
		ic := config.InfluxDBConfig{Listen: tt.listen, HostPort: 8086}
		if got := PingURL(ic); got != tt.wantURL {
			t.Errorf("PingURL(%q) = %q, want %q", tt.listen, got, tt.wantURL)
		}
	}
}

func TestInfluxQLEscaping(t *testing.T) {
	if got, want := InfluxQLString(`pa'ss\word`), `'pa\'ss\\word'`; got != want {
		t.Errorf("InfluxQLString = %q, want %q", got, want)
	}
	if got, want := InfluxQLIdent(`ad"min`), `"ad\"min"`; got != want {
		t.Errorf("InfluxQLIdent = %q, want %q", got, want)
	}
}

func TestBackupArgs(t *testing.T) {
	ic := testInstance(t)
	ic.RPCPort = 8088

	tests := []struct {
		name    string
		out     string
		opts    BackupOptions
		want    []string
		wantErr string
	}{
		{
			name: "database only",
			out:  "/tmp/backup",
			opts: BackupOptions{Database: "demo"},
			want: []string{"backup", "-portable", "-host", "127.0.0.1:8088", "-db", "demo", "/tmp/backup"},
		},
		{
			name: "since",
			out:  "/tmp/backup",
			opts: BackupOptions{Since: "2026-01-01T00:00:00Z"},
			want: []string{"backup", "-portable", "-host", "127.0.0.1:8088", "-since", "2026-01-01T00:00:00Z", "/tmp/backup"},
		},
		{
			name: "start and end",
			out:  "/tmp/backup",
			opts: BackupOptions{Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z"},
			want: []string{"backup", "-portable", "-host", "127.0.0.1:8088", "-start", "2026-01-01T00:00:00Z", "-end", "2026-01-02T00:00:00Z", "/tmp/backup"},
		},
		{
			name:    "since and start",
			out:     "/tmp/backup",
			opts:    BackupOptions{Since: "2026-01-01T00:00:00Z", Start: "2026-01-02T00:00:00Z"},
			wantErr: "cannot be combined",
		},
		{
			name:    "start without end",
			out:     "/tmp/backup",
			opts:    BackupOptions{Start: "2026-01-01T00:00:00Z"},
			wantErr: "must be given together",
		},
		{
			name:    "empty output",
			opts:    BackupOptions{},
			wantErr: "must not be empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BackupArgs(ic, tt.out, tt.opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("BackupArgs: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("args = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRestoreArgs(t *testing.T) {
	tests := []struct {
		name    string
		backup  string
		db      string
		newdb   string
		want    []string
		wantErr string
	}{
		{
			name:   "all",
			backup: "/backup",
			want:   []string{"restore", "-portable", "-host", "127.0.0.1:8088", "/backup"},
		},
		{
			name:   "database into new name",
			backup: "/backup",
			db:     "demo",
			newdb:  "demo_restored",
			want:   []string{"restore", "-portable", "-host", "127.0.0.1:8088", "-db", "demo", "-newdb", "demo_restored", "/backup"},
		},
		{
			name:    "newdb without database",
			backup:  "/backup",
			newdb:   "demo_restored",
			wantErr: "requires --database",
		},
		{
			name:    "empty backup",
			db:      "demo",
			wantErr: "must not be empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RestoreArgs(testInstance(t), tt.backup, tt.db, tt.newdb)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RestoreArgs: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("args = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCLIEnvDoesNotLeakIntoArgs(t *testing.T) {
	ic := testInstance(t)
	if env := CLIEnv(ic, false); env != nil {
		t.Errorf("CLIEnv(unauthenticated) = %v, want nil", env)
	}
	env := CLIEnv(ic, true)
	want := map[string]string{
		"INFLUX_USERNAME=admin":  "admin",
		"INFLUX_PASSWORD=secret": "secret",
	}
	if len(env) != len(want) {
		t.Fatalf("CLIEnv = %v", env)
	}
	for _, kv := range env {
		if want[kv] == "" {
			t.Errorf("unexpected env entry %q", kv)
		}
	}
	for _, arg := range CLIArgs(ic) {
		if strings.Contains(arg, "secret") {
			t.Errorf("password leaked into CLI args: %q", arg)
		}
	}
}
