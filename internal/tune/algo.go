package tune

import (
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Recommend computes the le0pard/pgtune parameter set for the given inputs,
// in le0pard's output order, each carrying a one-line reason. Inputs with a
// non-positive RAM are an error; enum fields left blank take the safe default.
func Recommend(in Inputs) ([]Param, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}

	maxConns := in.MaxConns
	if maxConns == 0 {
		maxConns = MaxConnectionsPreset(in.Type)
	}

	sharedBuffers := in.sharedBuffersKB()
	parallel := in.parallelSettings() // empty when CPUs < 4
	workMem := in.workMemKB(sharedBuffers, maxConns, parallel)
	maintWM := in.maintenanceWorkMemKB()
	walBuffers := in.walBuffersKB(sharedBuffers)

	// postmaster-level knobs that reload cannot change.
	restart := map[string]bool{
		"max_connections": true, "shared_buffers": true, "wal_buffers": true,
		"huge_pages": true, "max_worker_processes": true, "max_parallel_workers": true,
		"wal_level": true, "max_wal_senders": true,
	}

	out := []Param{
		{Name: "max_connections", Value: fmt.Sprint(maxConns),
			Reason: "connection budget for the workload class"},
		{Name: "shared_buffers", Value: formatValue(sharedBuffers),
			Reason: "buffer cache; 1/4 RAM (1/16 on desktop)"},
		{Name: "effective_cache_size", Value: formatValue(in.effectiveCacheKB()),
			Reason: "planner hint for OS + buffer cache"},
		{Name: "maintenance_work_mem", Value: formatValue(maintWM),
			Reason: "vacuum/index-build memory"},
		{Name: "checkpoint_completion_target", Value: "0.9",
			Reason: "spread checkpoint writes over the interval"},
		{Name: "wal_buffers", Value: formatValue(walBuffers),
			Reason: "3% of shared_buffers, capped at 16MB"},
		{Name: "default_statistics_target", Value: fmt.Sprint(in.statisticsTarget()),
			Reason: "ANALYZE sample multiplier (500 for DW)"},
		{Name: "random_page_cost", Value: in.randomPageCost(),
			Reason:  "seek vs sequential cost ratio",
			Warning: in.randomPageCostWarning()},
	}
	if io := in.effectiveIOConcurrency(); io != "" {
		out = append(out, Param{Name: "effective_io_concurrency", Value: io,
			Reason: "prefetch depth for the storage class"})
	}
	out = append(out,
		Param{Name: "work_mem", Value: formatValue(workMem),
			Reason: "per-node sort/hash memory"},
		Param{Name: "huge_pages", Value: in.hugePages(sharedBuffers),
			Reason: "reduce TLB pressure for large shared_buffers"},
	)
	if jit := in.jit(); jit != "" {
		out = append(out, Param{Name: "jit", Value: jit,
			Reason: "disable JIT for short OLTP/web queries"})
	}
	if wc := in.walCompression(); wc != "" {
		out = append(out, Param{Name: "wal_compression", Value: wc,
			Reason:  "compress WAL to cut I/O",
			Warning: in.walCompressionWarning()})
	}
	if aw := in.autovacuumMaxWorkers(); aw != "" {
		out = append(out, Param{Name: "autovacuum_max_workers", Value: aw,
			Reason: "more vacuum workers on high-core hosts"})
	}
	if awm := in.autovacuumWorkMemKB(maintWM); awm != "" {
		out = append(out, Param{Name: "autovacuum_work_mem", Value: awm,
			Reason: "cap per-worker vacuum memory below maintenance_work_mem"})
	}
	if im := in.ioMethod(); im != "" {
		out = append(out, Param{Name: "io_method", Value: im,
			Reason: "asynchronous I/O method (PG18+)", Warning: in.ioMethodWarning()})
	}
	if iw := in.ioWorkers(); iw != "" {
		out = append(out, Param{Name: "io_workers", Value: iw,
			Reason: "AIO worker processes (PG18+)"})
	}
	for _, kv := range in.walSizes() {
		out = append(out, Param{Name: kv[0], Value: kv[1],
			Reason: "WAL checkpoint sizing for the workload class"})
	}
	for _, p := range parallel {
		out = append(out, Param{Name: p.Name, Value: p.Value, Reason: p.Reason})
	}
	if in.Type == TypeDesktop {
		out = append(out,
			Param{Name: "wal_level", Value: "minimal",
				Reason: "desktop: no WAL archiving/replication needed"},
			Param{Name: "max_wal_senders", Value: "0",
				Reason: "required 0 when wal_level=minimal"})
	}

	for i := range out {
		out[i].RequiresRestart = restart[out[i].Name]
	}
	return out, nil
}

func (in Inputs) sharedBuffersKB() int {
	div := 4.0
	if in.Type == TypeDesktop {
		div = 16
	}
	v := math.Floor(in.ramKB() / div)
	if in.major() < 10 && in.OS == OSWindows { // legacy 512MB Windows cap
		if cap := math.Floor(512 * mb / kb); v > cap {
			v = cap
		}
	}
	return floorKb(v)
}

func (in Inputs) effectiveCacheKB() int {
	if in.Type == TypeDesktop {
		return floorKb(in.ramKB() / 4)
	}
	return floorKb(in.ramKB() * 3 / 4)
}

func (in Inputs) maintenanceWorkMemKB() int {
	v := math.Floor(in.ramKB() / 16)
	if in.Type == TypeDW {
		v = math.Floor(in.ramKB() / 8)
	}
	limit := 8.0 * gb / kb
	if in.OS == OSWindows && in.major() <= 17 {
		limit = 2*gb/kb - 1*mb/kb // Windows ≤PG17 can't allocate 2GB; use 2047MB
	}
	if v >= limit {
		v = limit
		if in.OS == OSWindows && in.major() <= 17 {
			v = 2*gb/kb - 1*mb/kb
		}
	}
	return floorKb(v)
}

func (in Inputs) walBuffersKB(sharedBuffers int) int {
	v := math.Floor(float64(sharedBuffers) * 3 / 100)
	maxBuf := 16 * mb / kb
	near := 14 * mb / kb
	if v > maxBuf {
		v = maxBuf
	}
	if v > near && v < maxBuf { // round 14-16MB up to an even 16MB
		v = maxBuf
	}
	if v < 32 {
		v = 32
	}
	return floorKb(v)
}

func (in Inputs) workMemKB(sharedBuffers, maxConns int, parallel []Param) int {
	workers := 1.0
	for _, p := range parallel {
		if p.Name == "max_worker_processes" {
			workers = math.Max(workers, float64(toInt(p.Value)))
		}
	}
	if len(parallel) == 0 {
		workers = 8 // le0pard's DEFAULT_DB_SETTINGS.max_worker_processes fallback
	}
	base := (in.ramKB() - float64(sharedBuffers)) / (float64(maxConns) + workers) / 3
	switch in.Type {
	case TypeDW, TypeMixed:
		base = math.Floor(base / 2)
	case TypeDesktop:
		base = math.Floor(base / 6)
	}
	switch in.Size {
	case SizeLess:
		base = math.Floor(base * 1.3)
	case SizeGreater:
		base = math.Floor(base * 0.9)
	}
	if base < 4*mb/kb {
		base = 4 * mb / kb
	}
	if in.OS == OSWindows && in.major() <= 17 {
		if lim := 2*gb/kb - 1*mb/kb; base > lim {
			base = lim
		}
	}
	return floorKb(base)
}

func (in Inputs) parallelSettings() []Param {
	if in.CPUs < 4 {
		return nil
	}
	perGather := int(math.Ceil(float64(in.CPUs) / 2))
	if in.Type != TypeDW && perGather > 4 {
		perGather = 4
	}
	out := []Param{
		{Name: "max_worker_processes", Value: fmt.Sprint(in.CPUs),
			Reason: "one background worker slot per core"},
		{Name: "max_parallel_workers_per_gather", Value: fmt.Sprint(perGather),
			Reason: "parallel scan workers per query (capped at 4 off DW)"},
	}
	if in.major() >= 10 {
		out = append(out, Param{Name: "max_parallel_workers", Value: fmt.Sprint(in.CPUs),
			Reason: "cluster-wide parallel worker budget"})
	}
	if in.major() >= 11 {
		mw := min(4, int(math.Ceil(float64(in.CPUs)/2)))
		out = append(out, Param{Name: "max_parallel_maintenance_workers", Value: fmt.Sprint(mw),
			Reason: "parallel index build/vacuum workers"})
	}
	return out
}

func (in Inputs) statisticsTarget() int {
	if in.Type == TypeDW {
		return 500
	}
	return 100
}

func (in Inputs) randomPageCost() string {
	if in.Size == SizeLess { // whole DB in RAM: seeks are free
		return "1.1"
	}
	if in.Disk == HDHDD {
		return "4"
	}
	if in.Type == TypeDW { // DW on SSD: low rpc picks catastrophic index scans
		return "4"
	}
	return "1.1"
}

func (in Inputs) randomPageCostWarning() string {
	if in.Type == TypeDW && in.Disk != HDHDD && in.Size != SizeLess {
		return "Cost parameters for Data Warehouses on SSDs/NVMe drives/SAN storage " +
			"are left at defaults to avoid catastrophic index scan selections"
	}
	return ""
}

func (in Inputs) effectiveIOConcurrency() string {
	if in.OS != OSLinux {
		return ""
	}
	switch in.Disk {
	case HDHDD:
		return "2"
	case HDSSD:
		return "200"
	case HDSAN:
		return "300"
	case HDNVME:
		return "1000"
	}
	return ""
}

func (in Inputs) hugePages(sharedBuffers int) string {
	if in.OS == OSMac {
		return "off"
	}
	if float64(sharedBuffers) >= 2*gb/kb {
		return "try"
	}
	return "off"
}

func (in Inputs) jit() string {
	if in.major() >= 12 && (in.Type == TypeWeb || in.Type == TypeOLTP || in.Type == TypeMixed) {
		return "off"
	}
	return ""
}

func (in Inputs) walCompression() string {
	switch {
	case in.major() >= 15:
		return "lz4"
	case in.major() >= 10:
		return "on"
	default:
		return ""
	}
}

func (in Inputs) walCompressionWarning() string {
	if in.walCompression() == "lz4" {
		return "wal_compression = lz4 requires PostgreSQL built --with-lz4"
	}
	return ""
}

func (in Inputs) autovacuumMaxWorkers() string {
	switch {
	case in.CPUs >= 32:
		return "5"
	case in.CPUs >= 16:
		return "4"
	default:
		return ""
	}
}

func (in Inputs) autovacuumWorkMemKB(maintWM int) string {
	threshold := 2.0 * gb / kb
	v := 0.0
	if float64(maintWM) >= threshold {
		v = threshold
	}
	if in.OS == OSWindows && in.major() <= 17 {
		if lim := 2*gb/kb - 1*mb/kb; v > lim {
			v = lim
		}
	}
	if v == 0 {
		return ""
	}
	return formatValue(floorKb(v))
}

func (in Inputs) ioMethod() string {
	if in.major() < 18 {
		return ""
	}
	if in.OS == OSLinux {
		return "io_uring"
	}
	return "worker"
}

func (in Inputs) ioMethodWarning() string {
	if in.ioMethod() == "io_uring" {
		return "io_method = io_uring requires PostgreSQL built --with-liburing"
	}
	return ""
}

func (in Inputs) ioWorkers() string {
	if in.major() < 18 || in.ioMethod() == "io_uring" {
		return ""
	}
	v := min(32.0, max(3.0, math.Floor(float64(in.CPUs)/4)))
	if v > 3 {
		return strconv.Itoa(int(v))
	}
	return ""
}

func (in Inputs) walSizes() [][2]string {
	min := map[Workload]int{TypeWeb: 1024, TypeOLTP: 2048, TypeDW: 4096, TypeDesktop: 100, TypeMixed: 1024}
	max := map[Workload]int{TypeWeb: 4096, TypeOLTP: 8192, TypeDW: 16384, TypeDesktop: 2048, TypeMixed: 4096}
	mkMB := func(n int) string { return formatValue(n * 1024) }
	return [][2]string{
		{"min_wal_size", mkMB(min[in.Type])},
		{"max_wal_size", mkMB(max[in.Type])},
	}
}

func toInt(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

// GlobalWarnings returns le0pard's machine-wide caveats (absurdly low or high
// RAM). Per-parameter caveats like the DW-on-SSD one ride on Param.Warning.
func GlobalWarnings(in Inputs) []string {
	var out []string
	switch ram := float64(in.RAMBytes); {
	case ram > 100*gb:
		out = append(out, "RAM is over 100GB — these heuristics are not tuned for very "+
			"large machines; review the values before applying")
	case ram > 0 && ram < 256*mb:
		out = append(out, "RAM is under 256MB — PostgreSQL will be far from optimal at this size")
	}
	return out
}

// ParamsByRestart returns a copy ordered so postmaster-level (restart-required)
// parameters come first — the render and apply paths use this to warn about
// restarts up front.
func ParamsByRestart(ps []Param) []Param {
	out := append([]Param(nil), ps...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].RequiresRestart && !out[j].RequiresRestart
	})
	return out
}
