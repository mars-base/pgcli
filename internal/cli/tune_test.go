package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mars-base/pgcli/internal/tune"
)

func findTune(t *testing.T) *cobra.Command {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() == "tune" {
			return c
		}
	}
	t.Fatal("tune command not registered on rootCmd")
	return nil
}

func TestTuneFlagsRegistered(t *testing.T) {
	c := findTune(t)
	want := map[string]string{
		"type": "string", "hd": "string", "ram": "string", "cpus": "int",
		"conns": "int", "db-size": "string", "apply": "bool", "revert": "bool",
		"apply-conf": "string", "dsn": "string", "ha": "string",
		"auto-restart": "bool", "web-tune": "string", "open": "bool",
	}
	for name, typ := range want {
		f := c.Flags().Lookup(name)
		if f == nil {
			t.Errorf("flag --%s not registered", name)
			continue
		}
		if f.Value.Type() != typ {
			t.Errorf("flag --%s type = %s, want %s", name, f.Value.Type(), typ)
		}
	}
	// -i/-c/-o are global persistent flags; tune must not re-declare them.
	for _, dup := range []string{"instance", "config", "output"} {
		if c.Flags().Lookup(dup) != nil {
			t.Errorf("tune re-declares global flag --%s", dup)
		}
	}
}

// tuneProbeCmd returns a command carrying an "instance" flag so checkTuneTargets
// can observe Changed("instance") the way it does under rootCmd.
func tuneProbeCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "tune", RunE: func(*cobra.Command, []string) error { return nil }}
	c.PersistentFlags().String("instance", "default", "")
	if err := c.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	return c
}

func TestCheckTuneTargets(t *testing.T) {
	cases := []struct {
		name string
		args []string
		dsn  string
		ha   string
		err  string
	}{
		{"ha+dsn", []string{}, "postgres://h/db", "app", "--ha and --dsn are mutually exclusive"},
		{"ha+instance", []string{"--instance", "x"}, "", "app", "--ha and --instance are mutually exclusive"},
		{"dsn+instance", []string{"--instance", "x"}, "postgres://h/db", "", "--dsn and --instance are mutually exclusive"},
		{"ha alone ok", []string{}, "", "app", ""},
		{"dsn alone ok", []string{}, "postgres://h/db", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tuneProbeCmd(t, tc.args...)
			err := checkTuneTargets(cmd, tc.dsn, tc.ha)
			if tc.err == "" {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error %q, got nil", tc.err)
			}
			if !strings.HasPrefix(err.Error(), tc.err) {
				t.Errorf("error = %q, want prefix %q", err.Error(), tc.err)
			}
		})
	}
}

func TestParseRAM(t *testing.T) {
	cases := map[string]int64{
		"16G": 16 << 30, "16GB": 16 << 30, "512M": 512 << 20,
		"2TB": 2 << 40, "1024KB": 1024 << 10, "4096": 4096, "8b": 8,
		" 16G ": 16 << 30, // surrounding spaces are trimmed
	}
	for in, want := range cases {
		got, err := parseRAM(in)
		if err != nil {
			t.Errorf("parseRAM(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseRAM(%q) = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "abc", "0", "-5G", "G", "16X"} {
		if _, err := parseRAM(bad); err == nil {
			t.Errorf("parseRAM(%q) should error", bad)
		}
	}
}

func TestDBSizeClass(t *testing.T) {
	ram := int64(16 << 30) // 16G
	cases := []struct {
		db   int64
		want tune.DBSize
	}{
		{8 << 30, tune.SizeLess},
		{16 << 30, tune.SizeMid}, // == ram → not less, ≤4× → mid
		{64 << 30, tune.SizeMid}, // == 4× ram → mid
		{65 << 30, tune.SizeGreater},
	}
	for _, tc := range cases {
		if got := dbSizeClass(tc.db, ram); got != tc.want {
			t.Errorf("dbSizeClass(%d, 16G) = %s, want %s", tc.db, got, tc.want)
		}
	}
}

func TestSQLQuote(t *testing.T) {
	if got := sqlQuote("it's"); got != "it''s" {
		t.Errorf("sqlQuote = %q", got)
	}
	if got := sqlQuote("'; DROP TABLE x; --"); got != "''; DROP TABLE x; --" {
		t.Errorf("sqlQuote injection = %q", got)
	}
}

func TestParsePipeRows(t *testing.T) {
	got := parsePipeRows("shared_buffers|4GB\nwork_mem|19418kB\n\nmalformed-no-pipe\n")
	if got["shared_buffers"] != "4GB" || got["work_mem"] != "19418kB" {
		t.Errorf("parsePipeRows = %v", got)
	}
	if _, ok := got["malformed-no-pipe"]; ok {
		t.Errorf("lines without a pipe must be skipped: %v", got)
	}
}

// fakeQuerier answers fixed SQL so buildInputs/currentValues can run offline.
type fakeQuerier struct{ resp map[string]string }

func (f *fakeQuerier) Query(sql string) (string, error) {
	return f.resp[sql], nil
}

func TestServerVersion(t *testing.T) {
	tt := &tuneTarget{q: &fakeQuerier{resp: map[string]string{
		"SELECT current_setting('server_version_num')": "180006\n",
	}}}
	v, err := tt.serverVersion()
	if err != nil || v != 180006 {
		t.Fatalf("serverVersion = %d, %v", v, err)
	}
	bad := &tuneTarget{q: &fakeQuerier{resp: map[string]string{
		"SELECT current_setting('server_version_num')": "not-a-number\n",
	}}}
	if _, err := bad.serverVersion(); err == nil {
		t.Error("want error on non-numeric version")
	}
}

func TestPresetOr(t *testing.T) {
	if got := presetOr(0, tune.TypeWeb); got != 200 {
		t.Errorf("preset web = %d, want 200", got)
	}
	if got := presetOr(50, tune.TypeWeb); got != 50 {
		t.Errorf("override = %d, want 50", got)
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{
		16 << 30: "16GB", 512 << 20: "512MB", 100: "≈0MB",
	}
	for in, want := range cases {
		if got := formatSize(in); got != want {
			t.Errorf("formatSize(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestRunWebTuneWritesSite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tune.html")
	var buf bytes.Buffer
	if err := runWebTune(path, false, &buf); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<!DOCTYPE html>") {
		t.Errorf("no HTML written:\n%s", data)
	}
	if !strings.Contains(buf.String(), path) {
		t.Errorf("output should name the file: %q", buf.String())
	}
}

func TestInputsToStringsRoundTrip(t *testing.T) {
	in := tune.Inputs{DBVersion: 180006, Type: tune.TypeDW, RAMBytes: 8 << 30,
		CPUs: 4, Disk: tune.HDNVME, Size: tune.SizeGreater, MaxConns: 40}
	m := inputsToStrings(in)
	if m["type"] != "dw" || m["hd"] != "nvme" || m["ram"] != "8GB" || m["cpus"] != "4" {
		t.Errorf("inputsToStrings = %v", m)
	}
	if m["version"] != "180006" || m["conns"] != "40" {
		t.Errorf("inputsToStrings version/conns = %v", m)
	}
}
