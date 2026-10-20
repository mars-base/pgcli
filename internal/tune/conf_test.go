package tune

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseConf(t *testing.T) {
	src := `# comment
shared_buffers = 4GB
  work_mem = '16MB'    # inline comment

max_connections=100
jit = on
work_mem = '32MB'
`
	kvs, err := ParseConf(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []KV{
		{"shared_buffers", "4GB"},
		{"work_mem", "32MB"}, // duplicate: last value wins, first position kept
		{"max_connections", "100"},
		{"jit", "on"},
	}
	if fmt.Sprint(kvs) != fmt.Sprint(want) {
		t.Errorf("ParseConf = %v, want %v", kvs, want)
	}

	for _, bad := range []string{"nonsense line\n", "= 5\n", "name =\n"} {
		if _, err := ParseConf(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseConf(%q) should error", bad)
		}
	}
}

func TestFormatConfRoundTrip(t *testing.T) {
	in := baseInputs()
	ps, err := Recommend(in)
	if err != nil {
		t.Fatal(err)
	}
	kvs := ParamsToKV(ps)
	var sb strings.Builder
	if err := FormatConf(&sb, in, kvs); err != nil {
		t.Fatal(err)
	}
	back, err := ParseConf(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatalf("re-parse generated conf: %v\n%s", err, sb.String())
	}
	if fmt.Sprint(back) != fmt.Sprint(kvs) {
		t.Errorf("round-trip mismatch:\n%v\n%v", back, kvs)
	}
}

// TestOutputOrder pins le0pard view.jsx's parameter sequence.
func TestOutputOrder(t *testing.T) {
	ps, _ := Recommend(baseInputs())
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	want := []string{
		"max_connections", "shared_buffers", "effective_cache_size", "maintenance_work_mem",
		"checkpoint_completion_target", "wal_buffers", "default_statistics_target",
		"random_page_cost", "effective_io_concurrency", "work_mem", "huge_pages", "jit",
		"wal_compression", "io_method", "min_wal_size", "max_wal_size", "max_worker_processes",
		"max_parallel_workers_per_gather", "max_parallel_workers", "max_parallel_maintenance_workers",
	} // 8 CPUs → no autovacuum_max_workers; 1GB mwm → no autovacuum_work_mem
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order mismatch:\n got %v\nwant %v", names, want)
	}
}

// Golden fixtures for the --web-tune JS self-check are produced with web.go's
// GoldenFixture helper (see web_test.go).
