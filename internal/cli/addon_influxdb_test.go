package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteImportContext(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "export.txt")
	dst := filepath.Join(dir, "rewritten.txt")
	body := "# DDL\nCREATE DATABASE demo WITH NAME autogen\n# DML\n" +
		"# CONTEXT-DATABASE:demo\n# CONTEXT-RETENTION-POLICY:autogen\n" +
		"cpu,host=srv1 value=0.64 1793066775681818870\n" +
		"# CONTEXT-DATABASE:_internal\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := rewriteImportContext(src, "demo_copy", dst); err != nil {
		t.Fatalf("rewriteImportContext: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "CONTEXT-DATABASE:demo\n") || strings.Contains(text, "CONTEXT-DATABASE:_internal") {
		t.Errorf("old context directives survived:\n%s", text)
	}
	if strings.Count(text, "CONTEXT-DATABASE:demo_copy") != 2 {
		t.Errorf("context directives = %d, want 2:\n%s", strings.Count(text, "CONTEXT-DATABASE:demo_copy"), text)
	}
	if !strings.Contains(text, "CREATE DATABASE demo WITH NAME autogen") {
		t.Errorf("DDL was modified:\n%s", text)
	}
	// The source file must be left untouched.
	orig, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != body {
		t.Error("source file was modified")
	}
}

func TestRewriteImportContextNoDirective(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "lines.txt")
	dst := filepath.Join(dir, "copy.txt")
	body := "cpu value=1 1\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteImportContext(src, "demo_copy", dst); err != nil {
		t.Fatalf("rewriteImportContext: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("copy = %q, want %q", got, body)
	}
}

func TestInfluxDBCreateDatabaseQuery(t *testing.T) {
	tests := []struct {
		name                                   string
		db, retention, shardDuration, rp, want string
	}{
		{"plain", "demo", "", "", "", `CREATE DATABASE "demo"`},
		{"retention", "demo", "30d", "", "", `CREATE DATABASE "demo" WITH DURATION 30d REPLICATION 1`},
		{"full", "demo", "30d", "1d", "rp_30d", `CREATE DATABASE "demo" WITH DURATION 30d REPLICATION 1 SHARD DURATION 1d NAME "rp_30d"`},
		{"quoted ident", `my"db`, "12h", "", "", `CREATE DATABASE "my\"db" WITH DURATION 12h REPLICATION 1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := influxDBCreateDatabaseQuery(tt.db, tt.retention, tt.shardDuration, tt.rp)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("query = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInfluxDBCreateDatabaseQueryErrors(t *testing.T) {
	tests := []struct {
		name                             string
		db, retention, shardDuration, rp string
	}{
		{"empty db", " ", "", "", ""},
		{"rp without retention", "demo", "", "", "rp"},
		{"shard without retention", "demo", "", "1d", ""},
		{"bad duration", "demo", "30days", "", ""},
		{"bad shard duration", "demo", "30d", "1d2x", ""},
		{"INF shard", "demo", "30d", "INF", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := influxDBCreateDatabaseQuery(tt.db, tt.retention, tt.shardDuration, tt.rp); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestInfluxDBRetentionQueries(t *testing.T) {
	got, err := influxDBRetentionCreateQuery("demo", "rp_30d", "30d", "1d", true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := `CREATE RETENTION POLICY "rp_30d" ON "demo" DURATION 30d REPLICATION 1 SHARD DURATION 1d DEFAULT`
	if got != want {
		t.Errorf("create query = %q, want %q", got, want)
	}

	got, err = influxDBRetentionAlterQuery("demo", "rp_30d", "90d", "", false)
	if err != nil {
		t.Fatalf("alter: %v", err)
	}
	want = `ALTER RETENTION POLICY "rp_30d" ON "demo" DURATION 90d`
	if got != want {
		t.Errorf("alter query = %q, want %q", got, want)
	}

	got, err = influxDBRetentionAlterQuery("demo", "rp_30d", "", "", true)
	if err != nil {
		t.Fatalf("alter default: %v", err)
	}
	want = `ALTER RETENTION POLICY "rp_30d" ON "demo" DEFAULT`
	if got != want {
		t.Errorf("alter default query = %q, want %q", got, want)
	}
}

func TestInfluxDBRetentionQueryErrors(t *testing.T) {
	if _, err := influxDBRetentionCreateQuery("", "rp", "30d", "", false); err == nil {
		t.Error("create without database: expected an error")
	}
	if _, err := influxDBRetentionCreateQuery("demo", "", "30d", "", false); err == nil {
		t.Error("create without rp: expected an error")
	}
	if _, err := influxDBRetentionCreateQuery("demo", "rp", "", "", false); err == nil {
		t.Error("create without duration: expected an error")
	}
	if _, err := influxDBRetentionAlterQuery("demo", "rp", "", "", false); err == nil {
		t.Error("alter without changes: expected an error")
	}
	if _, err := influxDBRetentionAlterQuery("demo", "rp", "30days", "", false); err == nil {
		t.Error("alter with bad duration: expected an error")
	}
}

func TestValidateInfluxDBDuration(t *testing.T) {
	for _, d := range []string{"1h", "12h", "30d", "1w", "1h30m", "500ms", "10u", "10µ", "INF"} {
		if err := validateInfluxDBDuration("duration", d); err != nil {
			t.Errorf("%q rejected: %v", d, err)
		}
	}
	for _, d := range []string{"", "0", "30 days", "1y", "-1h", "1.5h", "1h 30m"} {
		if err := validateInfluxDBDuration("duration", d); err == nil {
			t.Errorf("%q accepted, want error", d)
		}
	}
}
