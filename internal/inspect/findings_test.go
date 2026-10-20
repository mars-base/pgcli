package inspect

import (
	"testing"
	"time"
)

// ptr helpers for *float64 fields on Rates.
func f64(v float64) *float64 { return &v }

func hasFinding(fs []Finding, id string, sev Severity) bool {
	for _, f := range fs {
		if f.ID == id && f.Severity == sev {
			return true
		}
	}
	return false
}

func hasID(fs []Finding, id string) bool {
	for _, f := range fs {
		if f.ID == id {
			return true
		}
	}
	return false
}

func TestHealthScoreWeights(t *testing.T) {
	fs := []Finding{
		{Severity: SeverityCritical},
		{Severity: SeverityWarn},
		{Severity: SeverityWarn},
		{Severity: SeverityInfo},
		{Severity: SeverityGood}, // GOOD must never subtract
	}
	// 100 - 10 - 3 - 3 - 1 = 83
	if got := HealthScore(fs); got != 83 {
		t.Errorf("HealthScore = %v, want 83", got)
	}
}

func TestHealthScoreFloorsAtZero(t *testing.T) {
	var fs []Finding
	for i := 0; i < 12; i++ {
		fs = append(fs, Finding{Severity: SeverityCritical})
	}
	if got := HealthScore(fs); got != 0 {
		t.Errorf("HealthScore = %v, want 0 (floor)", got)
	}
}

func TestRuleWraparound(t *testing.T) {
	c := &Context{Limits: &LimitsRow{MaxXidAge: 1_900_000_000, MaxMxidAge: 1_100_000_000}}
	fs := ruleWraparound(c)
	if !hasFinding(fs, "xid_wraparound_transaction", SeverityCritical) {
		t.Error("xid age 1.9B should be CRITICAL")
	}
	if !hasFinding(fs, "xid_wraparound_multi-transaction", SeverityWarn) {
		t.Errorf("mxid age 1.1B should be WARNING, got %+v", fs)
	}

	clean := ruleWraparound(&Context{Limits: &LimitsRow{MaxXidAge: 100_000_000, MaxMxidAge: 100}})
	if len(clean) != 0 {
		t.Errorf("healthy ages should produce nothing, got %+v", clean)
	}
}

func TestRuleConnections(t *testing.T) {
	if !hasFinding(ruleConnections(&Context{Limits: &LimitsRow{ConnUsed: 96, ConnMax: 100}}),
		"connection_saturation", SeverityCritical) {
		t.Error("96% should be CRITICAL")
	}
	if !hasFinding(ruleConnections(&Context{Limits: &LimitsRow{ConnUsed: 90, ConnMax: 100}}),
		"connection_saturation", SeverityWarn) {
		t.Error("90% should be WARNING")
	}
	if len(ruleConnections(&Context{Limits: &LimitsRow{ConnUsed: 50, ConnMax: 100}})) != 0 {
		t.Error("50% should be clean")
	}
	if len(ruleConnections(&Context{Limits: &LimitsRow{ConnUsed: 5, ConnMax: 0}})) != 0 {
		t.Error("conn_max=0 must not divide")
	}
}

func TestRuleCacheNilRatioIgnored(t *testing.T) {
	if len(ruleCache(&Context{Rates: &Rates{}})) != 0 {
		t.Error("nil cache ratio (too little sampled traffic) must not fire")
	}
	if !hasFinding(ruleCache(&Context{Rates: &Rates{CacheHitRatio: f64(0.80), CacheBlocks: 12000}}),
		"low_cache_hit", SeverityWarn) {
		t.Error("80% hit ratio should fire")
	}
	if len(ruleCache(&Context{Rates: &Rates{CacheHitRatio: f64(0.995)}})) != 0 {
		t.Error("99.5% should be clean")
	}
}

func TestRuleUnusedIndexesSafetyFlags(t *testing.T) {
	c := &Context{Indexes: []IndexRow{
		{Schema: "public", Table: "t", Index: "t_unused", Scans: 0, Bytes: 5 << 20},
		{Schema: "public", Table: "t", Index: "t_pk", Scans: 0, Bytes: 5 << 20, IsPrimary: true, BacksConstraint: true},
		{Schema: "public", Table: "t", Index: "t_uk", Scans: 0, Bytes: 5 << 20, IsUnique: true, BacksConstraint: true},
		{Schema: "public", Table: "t", Index: "t_repl", Scans: 0, Bytes: 5 << 20, IsReplIdent: true},
		{Schema: "public", Table: "t", Index: "t_scanned", Scans: 7, Bytes: 5 << 20},
		{Schema: "public", Table: "t", Index: "t_tiny", Scans: 0, Bytes: 1024},
		{Schema: "public", Table: "t", Index: "t_expr", Scans: 0, Bytes: 5 << 20, IsExpression: true},
	}}
	fs := ruleUnusedIndexes(c)
	if len(fs) != 1 {
		t.Fatalf("want exactly one aggregate finding, got %+v", fs)
	}
	if len(fs[0].Evidence) != 2 { // t_unused + t_expr only
		t.Errorf("evidence should exclude pk/unique-backed/replident/scanned/tiny, got %v", fs[0].Evidence)
	}
	var exprCaveat bool
	for _, cv := range fs[0].Caveats {
		if indexOf(cv, "partial/expression") {
			exprCaveat = true
		}
	}
	if !exprCaveat {
		t.Error("expression index must add a caveat")
	}
}

func indexOf(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRuleBloat(t *testing.T) {
	c := &Context{Tables: []TableRow{
		{Schema: "s", Table: "bloated", LiveTuples: 30_000, DeadTuples: 20_000},
		{Schema: "s", Table: "small_but_dead", LiveTuples: 5, DeadTuples: 5_000},
		{Schema: "s", Table: "clean", LiveTuples: 90_000, DeadTuples: 1_000},
	}}
	fs := ruleBloat(c)
	if len(fs) != 1 {
		t.Fatalf("want one bloat finding, got %+v", fs)
	}
	if len(fs[0].Evidence) != 1 || !indexOf(fs[0].Evidence[0], "bloated") {
		t.Errorf("only the big 40%%-dead table should appear, got %v", fs[0].Evidence)
	}
}

func TestRuleSeqScan(t *testing.T) {
	c := &Context{Tables: []TableRow{
		{Table: "hot", LiveTuples: 100_000, SeqScans: 500, IndexScans: 10},      // fires
		{Table: "mixed", LiveTuples: 100_000, SeqScans: 50, IndexScans: 10},     // too few total scans
		{Table: "indexed", LiveTuples: 100_000, SeqScans: 500, IndexScans: 400}, // seq <= 2×index
		{Table: "small", LiveTuples: 100, SeqScans: 500, IndexScans: 0},         // below row floor
	}}
	fs := ruleSeqScan(c)
	if len(fs) != 1 || !indexOf(fs[0].Evidence[0], "hot") {
		t.Errorf("only 'hot' should fire, got %+v", fs)
	}
}

func TestRuleStaleStats(t *testing.T) {
	monthAgo := int64(31 * 24 * 3600)
	c := &Context{Tables: []TableRow{
		{Table: "manual_only", LastAnalyzeAgeS: monthAgo, LastAutoanalyzeAgeS: -1},        // stale, never autoanalyzed
		{Table: "fresh_auto", LastAnalyzeAgeS: monthAgo, LastAutoanalyzeAgeS: 60},         // recent autoanalyze rescues it
		{Table: "never", LastAnalyzeAgeS: -1, LastAutoanalyzeAgeS: -1},                    // never analyzed → other rule's job
		{Table: "old_both", LastAnalyzeAgeS: monthAgo * 2, LastAutoanalyzeAgeS: monthAgo}, // stale
	}}
	fs := ruleStaleStats(c)
	if len(fs) != 1 {
		t.Fatalf("want one stale finding, got %+v", fs)
	}
	if len(fs[0].Evidence) != 2 {
		t.Errorf("manual_only + old_both expected, got %v", fs[0].Evidence)
	}
	for _, e := range fs[0].Evidence {
		if indexOf(e, "never") || indexOf(e, "fresh_auto") {
			t.Errorf("stale rule wrongly included %q", e)
		}
	}
}

func TestRuleVacuumHygiene(t *testing.T) {
	off := "false"
	c := &Context{Tables: []TableRow{
		{Table: "noautovac", TotalBytes: 10 << 20, RelAutovacuumEnabled: &off},
		{Table: "nevervac", TotalBytes: 10 << 20, LastVacuumAgeS: -1, LastAutovacuumAgeS: -1},
		{Table: "tiny_never", TotalBytes: 2048, LastVacuumAgeS: -1, LastAutovacuumAgeS: -1}, // below size floor
		{Table: "fine", TotalBytes: 10 << 20, LastAutovacuumAgeS: 3600, AutovacuumCount: 4},
	}}
	fs := ruleVacuumHygiene(c)
	if !hasFinding(fs, "autovacuum_disabled_on_table", SeverityCritical) {
		t.Error("autovacuum_enabled=false must be CRITICAL")
	}
	if !hasFinding(fs, "table_never_vacuumed", SeverityWarn) {
		t.Error("big never-vacuumed table must WARN")
	}
	for _, f := range fs {
		for _, e := range f.Evidence {
			if indexOf(e, "tiny_never") || indexOf(e, "fine") {
				t.Errorf("unexpected table %q in %s", e, f.ID)
			}
		}
	}
}

func TestRuleSessions(t *testing.T) {
	c := &Context{Activity: &Activity{IdleInTransaction: 2, LongestXactSec: 400}}
	fs := ruleSessions(c)
	if !hasFinding(fs, "long_running_transaction", SeverityWarn) {
		t.Error("400s open transaction must WARN")
	}
	if !hasFinding(fs, "idle_in_transaction", SeverityInfo) {
		t.Error("idle-in-txn at 400s should be a NOTE")
	}

	// An hour-old idle transaction escalates.
	c.Activity.LongestXactSec = 3700
	fs = ruleSessions(c)
	if !hasFinding(fs, "idle_in_transaction", SeverityWarn) {
		t.Error("idle-in-txn past an hour should WARN")
	}

	clean := ruleSessions(&Context{Activity: &Activity{LongestXactSec: 10}})
	if len(clean) != 0 {
		t.Errorf("quiet sessions must be silent, got %+v", clean)
	}
}

func TestRuleSettings(t *testing.T) {
	c := &Context{Settings: []SettingRow{
		{Name: "fsync", Value: "off"},
		{Name: "full_page_writes", Value: "on"},
		{Name: "data_checksums", Value: "off"},
	}}
	fs := ruleSettings(c)
	if !hasFinding(fs, "fsync_off", SeverityCritical) {
		t.Error("fsync=off must be CRITICAL")
	}
	if hasID(fs, "full_page_writes_off") {
		t.Error("full_page_writes=on must not fire")
	}
	if !hasFinding(fs, "no_data_checksums", SeverityInfo) {
		t.Error("checksums off should be a NOTE")
	}
}

func TestAnalyzeSortsAndNotes(t *testing.T) {
	c := &Context{
		Limits: &LimitsRow{ConnUsed: 99, ConnMax: 100, MaxXidAge: 5000},
		Health: &HealthRow{StatsAgeS: 3600},
	}
	notes := []note{{Section: "queries", Reason: "extension missing"}}
	fs := Analyze(c, notes)
	if len(fs) < 2 {
		t.Fatalf("expected saturation + note + good findings, got %+v", fs)
	}
	if fs[0].Severity != SeverityCritical {
		t.Errorf("findings must sort by severity, first is %s", fs[0].Severity)
	}
	if !hasFinding(fs, "section_queries", SeverityInfo) {
		t.Error("notes must surface as NOTE findings")
	}
	if !hasID(fs, "xid_age_healthy") {
		t.Error("a healthy xid age should earn a GOOD finding")
	}
}

func TestComputeRates(t *testing.T) {
	a := HealthRow{XactCommit: 100, XactRollback: 10, BlksHit: 1_000, BlksRead: 100}
	b := HealthRow{XactCommit: 130, XactRollback: 12, BlksHit: 9_000, BlksRead: 2_600, Deadlocks: 2, TempBytes: 5_000}
	r := computeRates(a, b, time.Second)
	if r.CommitsPerSec != 30 {
		t.Errorf("CommitsPerSec = %v, want 30", r.CommitsPerSec)
	}
	if r.DeadlocksDelta != 2 || r.TempBytesDelta != 5_000 {
		t.Errorf("deltas wrong: %+v", r)
	}
	if r.CacheHitRatio == nil || *r.CacheHitRatio < 0.7 || *r.CacheHitRatio > 0.8 {
		t.Errorf("hit ratio = %v, want ~0.76", r.CacheHitRatio)
	}
	if r.RollbackRatio == nil || *r.RollbackRatio != 0.0625 {
		t.Errorf("rollback ratio = %v, want 0.0625", r.RollbackRatio)
	}

	// Too little traffic → ratios stay nil (no confident judgement).
	quiet := computeRates(HealthRow{}, HealthRow{BlksHit: 100, XactCommit: 5}, time.Second)
	if quiet.CacheHitRatio != nil || quiet.RollbackRatio != nil {
		t.Error("quiet windows must not produce ratios")
	}
}
