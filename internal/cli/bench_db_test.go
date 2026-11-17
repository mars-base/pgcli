package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/podman"
)

type fakeBenchSQLRunner struct {
	outputs []string
	errs    []error
	calls   []podman.BenchContainerOptions
}

func (f *fakeBenchSQLRunner) QueryPsql(o podman.BenchContainerOptions) (string, error) {
	f.calls = append(f.calls, o)
	i := len(f.calls) - 1
	var out string
	var err error
	if i < len(f.outputs) {
		out = f.outputs[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return out, err
}

func testBenchTarget() benchTarget {
	return benchTarget{
		Host:     "127.0.0.1",
		Port:     5432,
		User:     "admin",
		Password: "secret",
		Database: "appdb",
	}
}

func TestParseBenchDBProbe(t *testing.T) {
	tests := []struct {
		in        string
		canCreate bool
		exists    bool
		wantErr   bool
	}{
		{"t|t\n", true, true, false},
		{"t|f\n", true, false, false},
		{"f|t\n", false, true, false},
		{"f|f\n", false, false, false},
		{" true | false \n", true, false, false},
		{"t|t", true, true, false},
		{"", false, false, true},
		{"x|t", false, false, true},
		{"t", false, false, true},
		{"t|f|x", false, false, true},
	}
	for _, tt := range tests {
		got, err := parseBenchDBProbe(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseBenchDBProbe(%q) accepted", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseBenchDBProbe(%q): %v", tt.in, err)
			continue
		}
		if got.CanCreate != tt.canCreate || got.Exists != tt.exists {
			t.Errorf("parseBenchDBProbe(%q) = %+v, want canCreate=%v exists=%v", tt.in, got, tt.canCreate, tt.exists)
		}
	}
}

func TestBenchDBQueries(t *testing.T) {
	probe := benchDBProbeQuery("bench_db")
	if !strings.Contains(probe, "rolsuper OR rolcreatedb") || !strings.Contains(probe, "datname = 'bench_db'") {
		t.Errorf("probe query = %q", probe)
	}
	if got := benchDBProbeQuery("a'b"); !strings.Contains(got, "'a''b'") {
		t.Errorf("probe query does not escape quotes: %q", got)
	}
	if got, want := createBenchDBQuery("bench_db"), `CREATE DATABASE "bench_db"`; got != want {
		t.Errorf("create query = %q, want %q", got, want)
	}
	if got, want := quoteBenchIdent(`a"b`), `"a""b"`; got != want {
		t.Errorf("quote ident = %q, want %q", got, want)
	}
}

func TestValidateBenchDBName(t *testing.T) {
	valid := []string{"bench_db", "a", "A1", "bench$1", strings.Repeat("a", 63)}
	for _, name := range valid {
		if err := validateBenchDBName(name); err != nil {
			t.Errorf("validateBenchDBName(%q) = %v", name, err)
		}
	}
	invalid := []string{"", "1bench", "-bench", "bench-db", "bench db", "bench;db", `bench"db`, strings.Repeat("a", 64), "template0", "template1"}
	for _, name := range invalid {
		if err := validateBenchDBName(name); err == nil {
			t.Errorf("validateBenchDBName(%q) accepted", name)
		}
	}
}

func TestResolveBenchDatabaseCannotCreate(t *testing.T) {
	pm := &fakeBenchSQLRunner{outputs: []string{"f|f"}}
	result, err := resolveBenchDatabase(pm, testBenchTarget(), "postgres", "img", "bench_db")
	if err != nil {
		t.Fatal(err)
	}
	if result.CanCreate || result.AutoSelected || result.Created {
		t.Errorf("result = %+v", result)
	}
	if result.Target.Database != "appdb" {
		t.Errorf("database = %q, want appdb", result.Target.Database)
	}
	if len(pm.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(pm.calls))
	}
	assertBenchCall(t, pm.calls[0], "postgres", "img")
}

func TestResolveBenchDatabaseExisting(t *testing.T) {
	pm := &fakeBenchSQLRunner{outputs: []string{"t|t"}}
	result, err := resolveBenchDatabase(pm, testBenchTarget(), "postgres", "img", "bench_db")
	if err != nil {
		t.Fatal(err)
	}
	if !result.CanCreate || !result.AutoSelected || result.Created {
		t.Errorf("result = %+v", result)
	}
	if result.Target.Database != "bench_db" {
		t.Errorf("database = %q, want bench_db", result.Target.Database)
	}
	if len(pm.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(pm.calls))
	}
}

func TestResolveBenchDatabaseCreates(t *testing.T) {
	pm := &fakeBenchSQLRunner{outputs: []string{"t|f", ""}}
	result, err := resolveBenchDatabase(pm, testBenchTarget(), "postgres", "img", "bench_db")
	if err != nil {
		t.Fatal(err)
	}
	if !result.AutoSelected || !result.Created {
		t.Errorf("result = %+v", result)
	}
	if result.Target.Database != "bench_db" {
		t.Errorf("database = %q", result.Target.Database)
	}
	if len(pm.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(pm.calls))
	}
	assertBenchCall(t, pm.calls[0], "postgres", "img")
	assertBenchCall(t, pm.calls[1], "postgres", "img")
	if !strings.Contains(pm.calls[1].Args[len(pm.calls[1].Args)-1], `CREATE DATABASE "bench_db"`) {
		t.Errorf("create args = %q", pm.calls[1].Args)
	}
}

func TestResolveBenchDatabaseCreateNoiseWithExistingDB(t *testing.T) {
	pm := &fakeBenchSQLRunner{
		outputs: []string{"t|f", "", "t|t"},
		errs:    []error{nil, errors.New("container cleanup failed"), nil},
	}
	result, err := resolveBenchDatabase(pm, testBenchTarget(), "postgres", "img", "bench_db")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Target.Database != "bench_db" {
		t.Errorf("result = %+v", result)
	}
	if len(pm.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(pm.calls))
	}
}

func TestResolveBenchDatabaseCreateFailure(t *testing.T) {
	pm := &fakeBenchSQLRunner{
		outputs: []string{"t|f", "", "t|f"},
		errs:    []error{nil, errors.New("permission denied"), nil},
	}
	_, err := resolveBenchDatabase(pm, testBenchTarget(), "postgres", "img", "bench_db")
	if err == nil || !strings.Contains(err.Error(), "creating benchmark database") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveBenchDatabaseRecheckFailure(t *testing.T) {
	pm := &fakeBenchSQLRunner{
		outputs: []string{"t|f", "", ""},
		errs:    []error{nil, errors.New("create failed"), errors.New("recheck failed")},
	}
	_, err := resolveBenchDatabase(pm, testBenchTarget(), "postgres", "img", "bench_db")
	if err == nil || !strings.Contains(err.Error(), "creating benchmark database") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveBenchDatabaseProbeError(t *testing.T) {
	pm := &fakeBenchSQLRunner{
		outputs: []string{""},
		errs:    []error{errors.New("connection refused")},
	}
	_, err := resolveBenchDatabase(pm, testBenchTarget(), "postgres", "img", "bench_db")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveBenchDatabaseMalformedProbe(t *testing.T) {
	pm := &fakeBenchSQLRunner{outputs: []string{"weird"}}
	_, err := resolveBenchDatabase(pm, testBenchTarget(), "postgres", "img", "bench_db")
	if err == nil || !strings.Contains(err.Error(), "unexpected permission probe") {
		t.Fatalf("error = %v", err)
	}
}

func TestCleanupBenchTables(t *testing.T) {
	pm := &fakeBenchSQLRunner{outputs: []string{""}}
	if err := cleanupBenchTables(pm, testBenchTarget(), "img"); err != nil {
		t.Fatal(err)
	}
	if len(pm.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(pm.calls))
	}
	assertBenchCall(t, pm.calls[0], "appdb", "img")
	sql := pm.calls[0].Args[len(pm.calls[0].Args)-1]
	for _, table := range []string{"pgbench_accounts", "pgbench_branches", "pgbench_history", "pgbench_tellers"} {
		if !strings.Contains(sql, table) {
			t.Errorf("cleanup SQL does not drop %s: %q", table, sql)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "CASCADE") {
		t.Errorf("cleanup SQL uses CASCADE: %q", sql)
	}
	if strings.Contains(strings.ToUpper(sql), "DROP DATABASE") {
		t.Errorf("cleanup SQL drops the database: %q", sql)
	}
}

func TestCleanupBenchTablesError(t *testing.T) {
	pm := &fakeBenchSQLRunner{
		outputs: []string{""},
		errs:    []error{errors.New("permission denied")},
	}
	err := cleanupBenchTables(pm, testBenchTarget(), "img")
	if err == nil || !strings.Contains(err.Error(), "dropping pgbench tables") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error = %v", err)
	}
}

func TestBenchCleanupHint(t *testing.T) {
	hint := benchCleanupHint("bench_db", "-i pg01")
	for _, want := range []string{
		"bench_db",
		"pg exec -i pg01 --database bench_db '" + cleanupBenchTablesQuery() + ";'",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint does not contain %q: %q", want, hint)
		}
	}
	if strings.Contains(hint, "--bench-db") || strings.Contains(hint, "--no-auto-db") || strings.Contains(hint, "--image") {
		t.Errorf("hint contains bench-only flags unusable with pg exec: %q", hint)
	}
}

func TestBenchInitHint(t *testing.T) {
	hint := benchInitHint("bench_db", "-i pg01")
	for _, want := range []string{"bench_db", "pg bench -i pg01 --init && pg bench -i pg01", "pg bench -i pg01 --cleanup"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint does not contain %q: %q", want, hint)
		}
	}
}

func TestDetectBenchScale(t *testing.T) {
	tests := []struct {
		out     string
		err     error
		want    int
		wantErr string
	}{
		{out: "100000\n", want: 1},
		{out: "2500000\n", want: 25},
		{out: " 300000 \n", want: 3},
		{out: "99999\n", wantErr: "smaller than 100000"},
		{out: "\n", wantErr: "no rows"},
		{out: "0\n", wantErr: "no rows"},
		{out: "many\n", wantErr: "unexpected"},
		{out: "", err: errors.New("relation does not exist"), wantErr: "relation does not exist"},
	}
	for _, tt := range tests {
		pm := &fakeBenchSQLRunner{outputs: []string{tt.out}, errs: []error{tt.err}}
		got, err := detectBenchScale(pm, testBenchTarget(), "img")
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("detectBenchScale(%q) error = %v, want containing %q", tt.out, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("detectBenchScale(%q): %v", tt.out, err)
			continue
		}
		if got != tt.want {
			t.Errorf("detectBenchScale(%q) = %d, want %d", tt.out, got, tt.want)
		}
		assertBenchCall(t, pm.calls[0], "appdb", "img")
	}
}

func TestBenchTablesExist(t *testing.T) {
	query := benchTablesExistQuery()
	for _, table := range []string{"pgbench_accounts", "pgbench_branches", "pgbench_history", "pgbench_tellers"} {
		if !strings.Contains(query, table) {
			t.Errorf("existence query does not check %s: %q", table, query)
		}
	}

	tests := []struct {
		out     string
		err     error
		exists  bool
		wantErr bool
	}{
		{"t\n", nil, true, false},
		{"f\n", nil, false, false},
		{"", errors.New("connection refused"), false, true},
		{"unexpected", nil, false, true},
	}
	for _, tt := range tests {
		pm := &fakeBenchSQLRunner{outputs: []string{tt.out}, errs: []error{tt.err}}
		exists, err := benchTablesExist(pm, testBenchTarget(), "img")
		if tt.wantErr {
			if err == nil {
				t.Errorf("benchTablesExist(%q) accepted", tt.out)
			}
			continue
		}
		if err != nil {
			t.Errorf("benchTablesExist(%q): %v", tt.out, err)
			continue
		}
		if exists != tt.exists {
			t.Errorf("benchTablesExist(%q) = %v, want %v", tt.out, exists, tt.exists)
		}
		assertBenchCall(t, pm.calls[0], "appdb", "img")
	}
}

func assertBenchCall(t *testing.T, call podman.BenchContainerOptions, database, image string) {
	t.Helper()
	if call.Entrypoint != "psql" {
		t.Errorf("entrypoint = %q", call.Entrypoint)
	}
	if call.Image != image {
		t.Errorf("image = %q", call.Image)
	}
	joined := strings.Join(call.Env, "\n")
	if !strings.Contains(joined, "PGDATABASE="+database) {
		t.Errorf("env = %q, want PGDATABASE=%s", call.Env, database)
	}
	if !strings.Contains(joined, "PGPASSWORD=secret") {
		t.Errorf("env does not include password: %q", call.Env)
	}
	for _, arg := range call.Args {
		if strings.Contains(arg, "secret") {
			t.Errorf("args leak the password: %q", call.Args)
		}
	}
}
