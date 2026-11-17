package cli

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func defaultBenchOptions() benchOptions {
	return benchOptions{
		BenchDB:  "bench_db",
		Scale:    1,
		Clients:  16,
		Threads:  4,
		Time:     60,
		Mode:     "prepared",
		Progress: 10,
		LogDir:   "./pgbench-logs",
	}
}

func TestBenchCommandRegistration(t *testing.T) {
	var bench *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "bench" {
			bench = c
			break
		}
	}
	if bench == nil {
		t.Fatal("bench command not registered on rootCmd")
	}
	for _, name := range []string{"init", "clients"} {
		f := bench.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("--%s not registered", name)
		}
		if f.Shorthand != "" {
			t.Errorf("--%s must be long-only, got shorthand -%s", name, f.Shorthand)
		}
	}
	for _, dup := range []string{"instance", "config", "output"} {
		if bench.Flags().Lookup(dup) != nil {
			t.Errorf("bench re-declares global flag --%s", dup)
		}
	}
	defaults := map[string]string{
		"clients": "16", "threads": "4", "time": "60", "mode": "prepared",
		"progress": "10", "scale": "1", "bench-db": "bench_db",
		"transactions": "0", "rate": "0",
	}
	for name, want := range defaults {
		f := bench.Flags().Lookup(name)
		if f == nil {
			t.Errorf("--%s not registered", name)
			continue
		}
		if f.DefValue != want {
			t.Errorf("--%s default = %q, want %q", name, f.DefValue, want)
		}
	}
}

func TestValidateBenchOptions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*benchOptions, map[string]bool)
		want   string
	}{
		{"defaults", func(*benchOptions, map[string]bool) {}, ""},
		{"init scale", func(o *benchOptions, c map[string]bool) {
			o.Init = true
			o.Scale = 3
			c["scale"] = true
		}, ""},
		{"init with run-only clients", func(o *benchOptions, c map[string]bool) {
			o.Init = true
			c["clients"] = true
		}, "run-only"},
		{"init with run-only log-dir", func(o *benchOptions, c map[string]bool) {
			o.Init = true
			c["log-dir"] = true
		}, "run-only"},
		{"run with init-only scale", func(o *benchOptions, c map[string]bool) {
			c["scale"] = true
		}, "requires --init"},
		{"run with init-only foreign-keys", func(o *benchOptions, c map[string]bool) {
			o.ForeignKeys = true
			c["foreign-keys"] = true
		}, "requires --init"},
		{"time and transactions", func(o *benchOptions, c map[string]bool) {
			c["time"] = true
			o.Transactions = 100
			c["transactions"] = true
		}, "mutually exclusive"},
		{"select-only and simple-update", func(o *benchOptions, c map[string]bool) {
			o.SelectOnly = true
			o.SimpleUpdate = true
		}, "mutually exclusive"},
		{"latency without rate", func(o *benchOptions, c map[string]bool) {
			o.LatencyLimit = 10
			c["latency-limit"] = true
		}, "requires --rate"},
		{"partition method without partitions", func(o *benchOptions, c map[string]bool) {
			o.Init = true
			o.PartitionMethod = "hash"
			c["partition-method"] = true
		}, "requires --partitions"},
		{"partition method with zero partitions", func(o *benchOptions, c map[string]bool) {
			o.Init = true
			o.PartitionMethod = "hash"
			o.Partitions = 0
			c["partition-method"] = true
			c["partitions"] = true
		}, "requires --partitions"},
		{"log-dir without log", func(o *benchOptions, c map[string]bool) {
			c["log-dir"] = true
		}, "requires --log"},
		{"scale zero", func(o *benchOptions, c map[string]bool) { o.Scale = 0 }, "--scale"},
		{"clients zero", func(o *benchOptions, c map[string]bool) { o.Clients = 0 }, "--clients"},
		{"threads zero", func(o *benchOptions, c map[string]bool) { o.Threads = 0 }, "--threads"},
		{"time zero", func(o *benchOptions, c map[string]bool) { o.Time = 0 }, "--time"},
		{"transactions negative", func(o *benchOptions, c map[string]bool) {
			o.Transactions = -1
			c["transactions"] = true
		}, "--transactions"},
		{"progress negative", func(o *benchOptions, c map[string]bool) { o.Progress = -1 }, "--progress"},
		{"latency negative", func(o *benchOptions, c map[string]bool) {
			o.Rate = 10
			o.LatencyLimit = -1
		}, "--latency-limit"},
		{"partitions negative", func(o *benchOptions, c map[string]bool) { o.Partitions = -1 }, "--partitions"},
		{"fillfactor low", func(o *benchOptions, c map[string]bool) {
			o.Init = true
			o.Fillfactor = 5
			c["fillfactor"] = true
		}, "--fillfactor"},
		{"fillfactor high", func(o *benchOptions, c map[string]bool) {
			o.Init = true
			o.Fillfactor = 101
			c["fillfactor"] = true
		}, "--fillfactor"},
		{"rate zero explicit", func(o *benchOptions, c map[string]bool) {
			c["rate"] = true
		}, "--rate"},
		{"bad mode", func(o *benchOptions, c map[string]bool) { o.Mode = "fast" }, "--mode"},
		{"bad partition method", func(o *benchOptions, c map[string]bool) {
			o.Init = true
			o.Partitions = 2
			o.PartitionMethod = "list"
			c["partitions"] = true
		}, "--partition-method"},
		{"bad bench db", func(o *benchOptions, c map[string]bool) { o.BenchDB = "bad name" }, "--bench-db"},
		{"template bench db", func(o *benchOptions, c map[string]bool) { o.BenchDB = "template1" }, "template"},
		{"bad bench db ignored with no-auto-db", func(o *benchOptions, c map[string]bool) {
			o.BenchDB = "bad name"
			o.NoAutoDB = true
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := defaultBenchOptions()
			changed := map[string]bool{}
			tt.mutate(&o, changed)
			err := validateBenchOptions(o, changed)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateBenchOptions: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateBenchOptions error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildBenchPlanDefaultRun(t *testing.T) {
	plan, err := buildBenchPlan(defaultBenchOptions())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c", "16", "-j", "4", "-M", "prepared", "-T", "60", "-P", "10"}
	if !reflect.DeepEqual(plan.Args, want) {
		t.Errorf("args = %q, want %q", plan.Args, want)
	}
	if len(plan.Mounts) != 0 || plan.WorkDir != "" {
		t.Errorf("unexpected mounts/workdir: %q %q", plan.Mounts, plan.WorkDir)
	}
}

func TestBuildBenchPlanInit(t *testing.T) {
	o := defaultBenchOptions()
	o.Init = true
	o.Scale = 3
	o.NoVacuum = true
	o.Partitions = 4
	o.PartitionMethod = "hash"
	o.Fillfactor = 80
	o.ForeignKeys = true
	plan, err := buildBenchPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-i", "-s", "3", "--no-vacuum", "--partitions", "4", "--partition-method", "hash", "--fillfactor", "80", "--foreign-keys"}
	if !reflect.DeepEqual(plan.Args, want) {
		t.Errorf("args = %q, want %q", plan.Args, want)
	}
}

func TestBuildBenchPlanTransactionsOverride(t *testing.T) {
	o := defaultBenchOptions()
	o.Transactions = 100
	plan, err := buildBenchPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Args, " ")
	if strings.Contains(joined, "-T") {
		t.Errorf("args contain -T when transactions are set: %q", plan.Args)
	}
	if !strings.Contains(joined, "-t 100") {
		t.Errorf("args do not contain -t 100: %q", plan.Args)
	}
}

func TestBuildBenchPlanRunFlags(t *testing.T) {
	o := defaultBenchOptions()
	o.Progress = 0
	o.SelectOnly = true
	o.Builtin = []string{"tpcb-like@2", "select-only"}
	o.Define = []string{"scale=5"}
	o.Connect = true
	o.Rate = 100
	o.LatencyLimit = 50
	o.PerCommand = true
	o.NoVacuum = true
	plan, err := buildBenchPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-c", "16", "-j", "4", "-M", "prepared", "-T", "60",
		"-S", "-b", "tpcb-like@2", "-b", "select-only", "-D", "scale=5",
		"-C", "-R", "100", "-L", "50", "-r", "-n",
	}
	if !reflect.DeepEqual(plan.Args, want) {
		t.Errorf("args = %q, want %q", plan.Args, want)
	}
}

func TestBuildBenchPlanSimpleUpdate(t *testing.T) {
	o := defaultBenchOptions()
	o.SimpleUpdate = true
	plan, err := buildBenchPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Args, " "), "-N") {
		t.Errorf("args do not contain -N: %q", plan.Args)
	}
}

func TestBuildBenchPlanLogs(t *testing.T) {
	o := defaultBenchOptions()
	o.Log = true
	o.LogDir = t.TempDir()
	plan, err := buildBenchPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Args, " "), "-l") {
		t.Errorf("args do not contain -l: %q", plan.Args)
	}
	if plan.WorkDir != "/pgcli-bench/log" {
		t.Errorf("workdir = %q", plan.WorkDir)
	}
	wantMount := plan.LogDir + ":/pgcli-bench/log:Z"
	if !strings.Contains(strings.Join(plan.Mounts, " "), wantMount) {
		t.Errorf("mounts = %q, want containing %q", plan.Mounts, wantMount)
	}
}

func TestBuildBenchPlanFiles(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bench.sql"
	if err := os.WriteFile(path, []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := defaultBenchOptions()
	o.Files = []string{path + "@2"}
	plan, err := buildBenchPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Args, " ")
	if !strings.Contains(joined, "-f /pgcli-bench/files/0-bench.sql@2") {
		t.Errorf("args = %q", plan.Args)
	}
	if len(plan.Mounts) != 1 {
		t.Fatalf("mounts = %q", plan.Mounts)
	}
	if !strings.HasPrefix(plan.Mounts[0], path+":/pgcli-bench/files/0-bench.sql:ro,Z") {
		// path may be symlink-resolved; only verify the container side and ro,Z.
		if !strings.Contains(plan.Mounts[0], ":/pgcli-bench/files/0-bench.sql:ro,Z") {
			t.Errorf("mount = %q", plan.Mounts[0])
		}
	}
}

func TestBuildBenchPlanFilesUniqueContainerPaths(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	path1 := dir1 + "/bench.sql"
	path2 := dir2 + "/bench.sql"
	if err := os.WriteFile(path1, []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path2, []byte("SELECT 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := defaultBenchOptions()
	o.Files = []string{path1, path2}
	plan, err := buildBenchPlan(o)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Args, " ")
	if !strings.Contains(joined, "/pgcli-bench/files/0-bench.sql") || !strings.Contains(joined, "/pgcli-bench/files/1-bench.sql") {
		t.Errorf("args = %q", plan.Args)
	}
}

func TestResolveBenchFilesRejectsBadInputs(t *testing.T) {
	o := defaultBenchOptions()
	o.Files = []string{"/definitely/not/a/real/file.sql"}
	if _, err := buildBenchPlan(o); err == nil {
		t.Error("missing file accepted")
	}

	dir := t.TempDir()
	o.Files = []string{dir}
	if _, err := buildBenchPlan(o); err == nil {
		t.Error("directory accepted as script")
	}

	o.Files = []string{"@2"}
	if _, err := buildBenchPlan(o); err == nil || !strings.Contains(err.Error(), "empty path") {
		t.Errorf("empty path error = %v", err)
	}
}

func TestSplitWeightedBenchSpec(t *testing.T) {
	tests := []struct {
		in, path, weight string
	}{
		{"a.sql", "a.sql", ""},
		{"a.sql@2", "a.sql", "2"},
		{"dir/a@b.sql", "dir/a@b.sql", ""},
		{"a.sql@0", "a.sql@0", ""},
		{"a.sql@x", "a.sql@x", ""},
		{"a.sql@", "a.sql@", ""},
	}
	for _, tt := range tests {
		path, weight := splitWeightedBenchSpec(tt.in)
		if path != tt.path || weight != tt.weight {
			t.Errorf("splitWeightedBenchSpec(%q) = (%q, %q), want (%q, %q)", tt.in, path, weight, tt.path, tt.weight)
		}
	}
}

func TestValidateBenchPassthroughAllowed(t *testing.T) {
	allowed := [][]string{
		{"--sampling-rate", "0.1"},
		{"-T", "10", "-n"},
		{"--protocol=prepared", "-C"},
		{"-c", "100", "-M", "prepared"},
		{"-D", "x=-h"},
		{"--help"},
		{"--version"},
	}
	for _, args := range allowed {
		if err := validateBenchPassthrough(args); err != nil {
			t.Errorf("validateBenchPassthrough(%q) = %v", args, err)
		}
	}
}

func TestValidateBenchPassthroughRejected(t *testing.T) {
	tests := [][]string{
		{"-h", "evil"},
		{"--host=evil"},
		{"--hos", "evil"},
		{"-p", "1"},
		{"-p1"},
		{"-U", "evil"},
		{"--username", "evil"},
		{"-d", "evil"},
		{"--dbname=evil"},
		{"-i"},
		{"--init"},
		{"--initialize"},
		{"-f", "evil.sql"},
		{"--file=evil.sql"},
		{"-l"},
		{"--log"},
		{"-z"},
		{"-Ch"},
	}
	for _, args := range tests {
		if err := validateBenchPassthrough(args); err == nil {
			t.Errorf("validateBenchPassthrough(%q) accepted", args)
		}
	}
}

func TestValidateBenchPassthroughStopsAtDoubleDash(t *testing.T) {
	if err := validateBenchPassthrough([]string{"--", "-h", "evil"}); err != nil {
		t.Errorf("second -- should stop scanning: %v", err)
	}
}
