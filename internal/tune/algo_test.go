package tune

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func baseInputs() Inputs {
	return Inputs{
		DBVersion: 180000, OS: OSLinux, Type: TypeMixed,
		RAMBytes: 16 * gb, CPUs: 8, Disk: HDSSD, Size: SizeMid,
	}
}

// find returns the Param with the given name, or the zero value.
func find(t *testing.T, ps []Param, name string) Param {
	t.Helper()
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	return Param{}
}

func TestRecommend16GBMixedSSD(t *testing.T) {
	ps, err := Recommend(baseInputs())
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{
		"max_connections":                  "100", // mixed preset
		"shared_buffers":                   "4GB",
		"effective_cache_size":             "12GB",
		"maintenance_work_mem":             "1GB",
		"checkpoint_completion_target":     "0.9",
		"wal_buffers":                      "16MB", // 3% × 4GB, capped
		"default_statistics_target":        "100",
		"random_page_cost":                 "1.1",
		"effective_io_concurrency":         "200",
		"huge_pages":                       "try", // shared_buffers ≥ 2GB
		"jit":                              "off",
		"wal_compression":                  "lz4",
		"max_parallel_maintenance_workers": "4",
		"min_wal_size":                     "1GB",
		"max_wal_size":                     "4GB",
	}
	for name, want := range wants {
		if got := find(t, ps, name).Value; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// work_mem = floor((16777216−4194304)/((100+8)×3) / 2) = 19418 kB
	if got := find(t, ps, "work_mem").Value; got != "19418kB" {
		t.Errorf("work_mem = %q, want 19418kB", got)
	}
	// mwm 1GB < 2GB threshold → autovacuum_work_mem omitted entirely
	if p := find(t, ps, "autovacuum_work_mem"); p.Name != "" {
		t.Errorf("autovacuum_work_mem must be absent at 16GB, got %q", p.Value)
	}
	in := baseInputs()
	in.RAMBytes = 64 * gb // mwm 4GB ≥ 2GB → capped at 2GB
	ps, _ = Recommend(in)
	if got := find(t, ps, "autovacuum_work_mem").Value; got != "2GB" {
		t.Errorf("64GB autovacuum_work_mem = %q, want 2GB", got)
	}
	if got := find(t, ps, "io_method").Value; got != "worker" {
		t.Errorf("io_method = %q, want worker (io_uring is deliberately not recommended)", got)
	}
	if p := find(t, ps, "io_workers"); p.Value != "" {
		t.Errorf("io_workers must be absent at 8 cpus, got %q", p.Value)
	}
	if p := find(t, ps, "wal_compression"); !strings.Contains(p.Warning, "--with-lz4") {
		t.Errorf("wal_compression warning missing: %q", p.Warning)
	}
}

func TestRecommendDWMatrix(t *testing.T) {
	in := baseInputs()
	in.Type = TypeDW
	in.RAMBytes = 8 * gb
	in.CPUs = 2
	in.Disk = HDNVME
	ps, err := Recommend(in)
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{
		"max_connections":           "40",
		"shared_buffers":            "2GB",
		"effective_cache_size":      "6GB",
		"maintenance_work_mem":      "1GB", // dw: RAM/8
		"random_page_cost":          "4",
		"effective_io_concurrency":  "1000",
		"default_statistics_target": "500",
		"work_mem":                  "21845kB", // floor((8388608−2097152)/((40+1)×3) / 2)
		"min_wal_size":              "4GB",
		"max_wal_size":              "16GB",
	}
	for name, want := range wants {
		if got := find(t, ps, name).Value; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// cpu < 4 → no parallel block at all
	for _, name := range []string{"max_worker_processes", "max_parallel_workers",
		"max_parallel_workers_per_gather", "max_parallel_maintenance_workers"} {
		if p := find(t, ps, name); p.Name != "" {
			t.Errorf("%s must be absent with 2 CPUs", name)
		}
	}
	// dw → jit omitted
	if p := find(t, ps, "jit"); p.Name != "" {
		t.Errorf("jit must be absent for dw, got %q", p.Value)
	}
	// 2GB shared_buffers boundary → try
	if got := find(t, ps, "huge_pages").Value; got != "try" {
		t.Errorf("huge_pages = %q, want try (2GB is ≥ threshold)", got)
	}
	if p := find(t, ps, "random_page_cost"); !strings.Contains(p.Warning, "Data Warehouse") {
		t.Errorf("DW-on-NVMe warning missing: %q", p.Warning)
	}
}

func TestRecommendDesktop(t *testing.T) {
	in := baseInputs()
	in.Type = TypeDesktop
	in.RAMBytes = 4 * gb
	in.CPUs = 8
	ps, err := Recommend(in)
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{
		"max_connections":      "20",
		"shared_buffers":       "256MB",  // RAM/16
		"effective_cache_size": "1GB",    // RAM/4
		"work_mem":             "7801kB", // floor((4194304−262144)/((20+4)×3) / 6)
		"min_wal_size":         "100MB",
		"max_wal_size":         "2GB",
		"wal_level":            "minimal",
		"max_wal_senders":      "0",
		"huge_pages":           "off",
	}
	for name, want := range wants {
		if got := find(t, ps, name).Value; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestRecommendWebOLTPScale(t *testing.T) {
	in := baseInputs()
	in.Type = TypeWeb
	in.RAMBytes = 4 * gb
	in.CPUs = 16
	ps, err := Recommend(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, ps, "max_connections").Value; got != "200" {
		t.Errorf("web max_connections = %q, want 200", got)
	}
	// (4194304−1048576)/((200+16)×3) = 4854 kB, no dw/mixed halving
	if got := find(t, ps, "work_mem").Value; got != "4854kB" {
		t.Errorf("web work_mem = %q, want 4854kB", got)
	}
	// 16 cores → autovacuum workers 4
	if got := find(t, ps, "autovacuum_max_workers").Value; got != "4" {
		t.Errorf("autovacuum_max_workers = %q, want 4", got)
	}
}

func TestRecommendDBSizeAndConnsOverrides(t *testing.T) {
	in := baseInputs()
	in.Size = SizeLess
	in.MaxConns = 24
	ps, err := Recommend(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, ps, "max_connections").Value; got != "24" {
		t.Errorf("explicit max_connections = %q, want 24", got)
	}
	// less_ram: rpc 1.1 wins over dw/ssp rules; work_mem ×1.3
	// floor(floor((16777216−4194304)/((24+8)×3) / 2) × 1.3) = 85196 kB
	if got := find(t, ps, "work_mem").Value; got != "85196kB" {
		t.Errorf("less_ram work_mem = %q, want 85196kB", got)
	}
	if p := find(t, ps, "random_page_cost"); p.Value != "1.1" || p.Warning != "" {
		t.Errorf("less_ram rpc = %q warn %q, want 1.1/no warning", p.Value, p.Warning)
	}

	in.Size = SizeGreater
	ps2, err := Recommend(in)
	if err != nil {
		t.Fatal(err)
	}
	// greater_ram ×0.9: floor(floor(131072/2)×0.9) = 58982 kB
	if got := find(t, ps2, "work_mem").Value; got != "58982kB" {
		t.Errorf("greater_ram work_mem = %q, want 58982kB", got)
	}
}

func TestRecommendVersionGates(t *testing.T) {
	for _, ver := range []int{100000, 130004, 150000, 180000} {
		in := baseInputs()
		in.DBVersion = ver
		ps, err := Recommend(in)
		if err != nil {
			t.Fatal(err)
		}
		major := ver / 10000
		if p := find(t, ps, "io_method"); (major >= 18) != (p.Name != "") {
			t.Errorf("PG%d io_method present=%v, want %v", major, p.Name != "", major >= 18)
		}
		if p := find(t, ps, "jit"); (major >= 12) != (p.Name != "") {
			t.Errorf("PG%d jit present=%v, want %v", major, p.Name != "", major >= 12)
		}
		wc := find(t, ps, "wal_compression")
		want := "on"
		if major >= 15 {
			want = "lz4"
		}
		if wc.Value != want {
			t.Errorf("PG%d wal_compression = %q, want %q", major, wc.Value, want)
		}
	}
}

func TestRecommendOSVariants(t *testing.T) {
	// macOS: no effective_io_concurrency, io_method=worker (PG18), huge_pages off
	in := baseInputs()
	in.OS = OSMac
	ps, _ := Recommend(in)
	if p := find(t, ps, "effective_io_concurrency"); p.Name != "" {
		t.Errorf("mac must omit effective_io_concurrency")
	}
	if got := find(t, ps, "io_method").Value; got != "worker" {
		t.Errorf("mac io_method = %q, want worker", got)
	}
	// 8 cpus → io_workers min(32,max(3,2))=3 → not >3 → omitted
	if p := find(t, ps, "io_workers"); p.Name != "" {
		t.Errorf("io_workers = %q, want omitted at 3 (default is 3)", p.Value)
	}
	in.CPUs = 32
	ps, _ = Recommend(in)
	if got := find(t, ps, "io_workers").Value; got != "8" {
		t.Errorf("32-cpu io_workers = %q, want 8", got)
	}
	if got := find(t, ps, "huge_pages").Value; got != "off" {
		t.Errorf("mac huge_pages = %q, want off", got)
	}

	// Windows + PG17: 2GB−1MB caps on maintenance_work_mem / work_mem
	in = baseInputs()
	in.OS = OSWindows
	in.DBVersion = 170000
	in.RAMBytes = 128 * gb
	ps, _ = Recommend(in)
	if got := find(t, ps, "maintenance_work_mem").Value; got != "2047MB" {
		t.Errorf("win PG17 mwm = %q, want 2047MB", got)
	}
	// work_mem = floor((134217728−33554432)/((100+8)×3) / 2) = 155344 kB < 2047MB cap
	if got := find(t, ps, "work_mem").Value; got != "155344kB" {
		t.Errorf("win work_mem = %q, want 155344kB", got)
	}
	// Windows PG18 lifts the cap: 8GB
	in.DBVersion = 180000
	ps, _ = Recommend(in)
	if got := find(t, ps, "maintenance_work_mem").Value; got != "8GB" {
		t.Errorf("win PG18 mwm = %q, want 8GB", got)
	}
}

func TestRecommendWorkMemFloorAndDefaults(t *testing.T) {
	// tiny spec: work_mem must not dip below 4MB
	in := Inputs{DBVersion: 130000, OS: OSLinux, Type: TypeOLTP, RAMBytes: 1 * gb,
		CPUs: 1, Disk: HDHDD, Size: SizeMid}
	ps, _ := Recommend(in)
	if got := find(t, ps, "work_mem").Value; got != "4MB" {
		t.Errorf("floored work_mem = %q, want 4MB", got)
	}
	if got := find(t, ps, "wal_buffers").Value; got != "7864kB" {
		t.Errorf("wal_buffers = %q, want 7864kB (3%% of 256MB)", got)
	}
	// CPUs omitted → le0pard default 8 workers in the work_mem denominator
	base := (1048576 - 262144) / float64((300+8)*3) // 849
	if got := find(t, ps, "work_mem").Value; got != "4MB" && got != formatValue(int(base)) {
		t.Errorf("work_mem = %q", got)
	}
	if got := find(t, ps, "effective_io_concurrency").Value; got != "2" {
		t.Errorf("hdd io_concurrency = %q, want 2", got)
	}
	if got := find(t, ps, "random_page_cost").Value; got != "4" {
		t.Errorf("hdd rpc = %q, want 4", got)
	}
	if got := find(t, ps, "wal_compression").Value; got != "on" {
		t.Errorf("PG13 wal_compression = %q, want on", got)
	}
}

func TestRecommendRequiresRestart(t *testing.T) {
	ps, _ := Recommend(baseInputs())
	restart := map[string]bool{
		"max_connections": true, "shared_buffers": true, "wal_buffers": true,
		"huge_pages": true, "max_worker_processes": true, "max_parallel_workers": true,
	}
	for _, p := range ps {
		want := restart[p.Name]
		if p.RequiresRestart != want {
			t.Errorf("%s RequiresRestart = %v, want %v", p.Name, p.RequiresRestart, want)
		}
	}
}

func TestRecommendNormalizeDefaults(t *testing.T) {
	// blank enums fall back to linux/mixed/ssd/mid + PG18
	in := Inputs{RAMBytes: 16 * gb, CPUs: 8}
	ps, err := Recommend(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, ps, "effective_io_concurrency").Value; got != "200" {
		t.Errorf("default disk io_concurrency = %q, want 200 (ssd)", got)
	}
	if got := find(t, ps, "io_method").Value; got != "worker" {
		t.Errorf("default version io_method = %q, want worker (PG18)", got)
	}
	if got := find(t, ps, "max_connections").Value; got != "100" {
		t.Errorf("default type max_connections = %q, want 100 (mixed)", got)
	}
}

func TestGlobalWarnings(t *testing.T) {
	in := baseInputs()
	if w := GlobalWarnings(in); len(w) != 0 {
		t.Errorf("16GB should warn nothing, got %v", w)
	}
	in.RAMBytes = 128 * gb
	if w := GlobalWarnings(in); len(w) != 1 || !strings.Contains(w[0], "100GB") {
		t.Errorf("128GB high-RAM warning missing: %v", w)
	}
	in.RAMBytes = 128 * mb
	if w := GlobalWarnings(in); len(w) != 1 || !strings.Contains(w[0], "256MB") {
		t.Errorf("128MB low-RAM warning missing: %v", w)
	}
}

func TestRecommendInvalidInputs(t *testing.T) {
	in := baseInputs()
	in.RAMBytes = 0
	if _, err := Recommend(in); err == nil || !strings.Contains(err.Error(), "RAM") {
		t.Errorf("zero RAM error = %v", err)
	}
	in = baseInputs()
	in.Type = "quantum"
	if _, err := Recommend(in); err == nil {
		t.Error("unknown workload type must error")
	}
	in = baseInputs()
	in.Disk = "ramdisk"
	if _, err := Recommend(in); err == nil {
		t.Error("unknown disk must error")
	}
}

func TestValidateAgainstSettings(t *testing.T) {
	settings := map[string]SettingRow{
		"shared_buffers":           {Name: "shared_buffers", Vartype: "integer", Unit: "8kB", MinVal: "128", MaxVal: "1073741823"},
		"work_mem":                 {Name: "work_mem", Vartype: "integer", Unit: "kB", MinVal: "64", MaxVal: "2147483647"},
		"huge_pages":               {Name: "huge_pages", Vartype: "enum", EnumVals: "{on,try,off}"},
		"jit":                      {Name: "jit", Vartype: "bool"},
		"effective_io_concurrency": {Name: "effective_io_concurrency", Vartype: "integer", MinVal: "0", MaxVal: "1000"},
	}
	fetch := func(_ context.Context, names []string) (map[string]SettingRow, error) {
		out := map[string]SettingRow{}
		for _, n := range names {
			if s, ok := settings[n]; ok {
				out[n] = s
			}
		}
		return out, nil
	}

	ok := []KV{
		{"shared_buffers", "4GB"}, {"work_mem", "38964kB"}, {"huge_pages", "try"},
		{"jit", "off"}, {"effective_io_concurrency", "200"},
	}
	if errs := Validate(context.Background(), ok, fetch); len(errs) != 0 {
		t.Errorf("valid set rejected: %v", errs)
	}

	bad := []KV{
		{"io_method", "io_uring"},            // absent on the server (PG17)
		{"jit", "maybe"},                     // not a bool
		{"huge_pages", "definitely"},         // not in enum
		{"effective_io_concurrency", "2000"}, // above max
		{"work_mem", "banana"},               // not numeric at all
	}
	errs := Validate(context.Background(), bad, fetch)
	if len(errs) != 5 {
		t.Fatalf("got %d errors, want 5: %v", len(errs), errs)
	}
	for _, e := range errs {
		t.Log("expected:", e)
	}
	if !strings.Contains(errs[0].Error(), "not a parameter") {
		t.Errorf("unknown-param message wrong: %v", errs[0])
	}
	if !strings.Contains(errs[3].Error(), "above maximum") {
		t.Errorf("range message wrong: %v", errs[3])
	}

	// fetcher failure aborts everything in one error
	boom := func(context.Context, []string) (map[string]SettingRow, error) {
		return nil, errors.New("connection refused")
	}
	errs = Validate(context.Background(), ok, boom)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "connection refused") {
		t.Errorf("fetch error passthrough wrong: %v", errs)
	}
}

func TestParseWithUnit(t *testing.T) {
	cases := []struct {
		in    string
		want  float64
		unit  string
		isErr bool
	}{
		{"128MB", 131072, "MB", false},
		{"5min", 300000, "min", false},
		{"4GB", 4194304, "GB", false},
		{"64", 64, "", false},
		{"7f", 0, "", true},
		{"12parsec", 0, "", true},
	}
	for _, c := range cases {
		v, u, err := ParseWithUnit(c.in)
		if c.isErr {
			if err == nil {
				t.Errorf("ParseWithUnit(%q) expected error", c.in)
			}
			continue
		}
		if err != nil || v != c.want || u != c.unit {
			t.Errorf("ParseWithUnit(%q) = %v,%q,%v want %v,%q", c.in, v, u, err, c.want, c.unit)
		}
	}
}
