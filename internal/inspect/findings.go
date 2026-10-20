package inspect

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Deterministic rule engine — the analysis half of pgbot's design philosophy:
// every judgement is a fixed threshold over sampled numbers, so the report is
// reproducible and explainable (no LLM in the loop). Thresholds are pgbot's
// (Apache-2.0); a rule either fires or it doesn't, and severity drives the
// health-score weight below.

const (
	xidWrapWarn         = 1_000_000_000 // age(datfrozenxid): autovacuum's freeze trigger is ~200M
	xidWrapCrit         = 1_800_000_000 // past this, time-to-wraparound is days, not months
	xidWrapWall         = 2_147_483_647 // hard ceiling: writes halt
	connSatWarn         = 0.85
	connSatCrit         = 0.95
	cacheHitWarn        = 0.90 // below this, with >= cacheHitMinBlocks sampled…
	cacheHitMinBlocks   = 10_000
	deadRatioWarn       = 0.20 // dead/(dead+live) on a table this big…
	deadRatioMinRows    = 10_000
	unusedIndexMinBytes = 1 << 20 // >1 MiB and zero scans → a reclaim candidate
	longXactWarnSec     = 300
	idleTxnWarnSec      = 60   // idle-in-transaction this long is a NOTE…
	idleTnxCritSec      = 3600 // …this long blocks vacuum like a stuck xact
	rollbackRatioWarn   = 0.10 // needs >= rollbackMinTxns in the window to mean anything
	rollbackMinTxns     = 20
	seqScanMinRows      = 50_000         // seq-scan-heavy on a big table…
	seqScanMinScans     = 100            // …with enough samples to compare against…
	seqScanRatio        = 2              // …index scans: seq > 2×index.
	hotUpdateWarnRatio  = 0.50           // below 50% HOT = bloat-prone updates…
	hotUpdateMinVolume  = 10_000         // …on tables with enough updates to judge
	staleStatsWarnSec   = 30 * 24 * 3600 // last analyze older than a month
)

// weights turn findings into the health score.
const (
	weightCrit   = 10.0
	weightWarn   = 3.0
	weightInfo   = 1.0
	scoreFloor   = 0.0
	scoreCeiling = 100.0
)

// Analyze runs every rule over the sampled Context and returns findings sorted
// most-actionable first (severity, then impact). Notes degrade silently: an
// un-sampleable domain becomes a NOTE so an empty section is never mysterious.
func Analyze(c *Context, notes []note) []Finding {
	var out []Finding
	out = append(out, ruleWraparound(c)...)
	out = append(out, ruleConnections(c)...)
	out = append(out, ruleCache(c)...)
	out = append(out, ruleRollback(c)...)
	out = append(out, ruleDeadlocks(c)...)
	out = append(out, ruleUnusedIndexes(c)...)
	out = append(out, ruleBloat(c)...)
	out = append(out, ruleSeqScan(c)...)
	out = append(out, ruleHotUpdates(c)...)
	out = append(out, ruleStaleStats(c)...)
	out = append(out, ruleVacuumHygiene(c)...)
	out = append(out, ruleSessions(c)...)
	out = append(out, ruleSettings(c)...)
	out = append(out, ruleGood(c)...)
	for _, n := range notes {
		fix := n.Fix
		if fix == "" {
			fix = "Grant the monitoring role access to the underlying stats view (pg_monitor helps)."
		}
		out = append(out, Finding{
			ID:          "section_" + slug(n.Section),
			Severity:    SeverityInfo,
			Title:       fmt.Sprintf("%s not sampled", n.Section),
			Detail:      n.Reason,
			Remediation: fix,
			ImpactScore: 1,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i].Severity), rank(out[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return out[i].ImpactScore > out[j].ImpactScore
	})
	return out
}

// HealthScore = 100 − Σ(weight · count), floored at 0. GOOD and NOTE findings
// barely move it; CRITICALs dominate. This is a triage signal, not a grade.
func HealthScore(findings []Finding) float64 {
	score := scoreCeiling
	for _, f := range findings {
		switch f.Severity {
		case SeverityCritical:
			score -= weightCrit
		case SeverityWarn:
			score -= weightWarn
		case SeverityInfo:
			score -= weightInfo
		}
	}
	return clamp(score, scoreFloor, scoreCeiling)
}

func rank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityWarn:
		return 1
	case SeverityInfo:
		return 2
	default: // GOOD
		return 3
	}
}

// settingValue looks up a whitelisted pg_settings value, or "" if absent.
func settingValue(c *Context, name string) (string, bool) {
	for _, s := range c.Settings {
		if s.Name == name {
			return s.Value, true
		}
	}
	return "", false
}

// analyzeAge returns the age (seconds) of the most recent ANALYZE across the
// manual/auto timestamps, where -1 means "never". It returns -1 when neither
// has run — that table is reported by table_never_vacuumed, not here. Otherwise
// the smaller (more recent) age wins, so autoanalyzing a table clears it.
func analyzeAge(a, b int64) int64 {
	switch {
	case a < 0 && b < 0:
		return -1 // never analyzed — left to table_never_vacuumed
	case a < 0:
		return b
	case b < 0:
		return a
	default:
		return min(a, b)
	}
}

func slug(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer(" ", "_", "/", "_").Replace(s)
}

func ruleWraparound(c *Context) []Finding {
	if c.Limits == nil {
		return nil
	}
	var out []Finding
	for _, g := range []struct {
		name string
		age  int64
	}{
		{"transaction ID (xid)", c.Limits.MaxXidAge},
		{"multi-transaction ID (mxid)", c.Limits.MaxMxidAge},
	} {
		switch {
		case g.age >= xidWrapCrit:
			out = append(out, Finding{
				ID:          "xid_wraparound_" + strings.Fields(g.name)[0],
				Severity:    SeverityCritical,
				Title:       fmt.Sprintf("%s age %s — wraparound imminent", g.name, humanInt(g.age)),
				Detail:      fmt.Sprintf("Oldest datfrozenxid age is %d of the %d ceiling; at this rate autovacuum cannot keep up and Postgres will halt writes.", g.age, xidWrapWall),
				Remediation: "Run VACUUM (FREEZE) on the oldest relations now, then raise autovacuum aggressiveness (lower autovacuum_vacuum_scale_factor / insert thresholds) so it keeps pace.",
				ImpactScore: 100 * float64(g.age) / xidWrapWall,
			})
		case g.age >= xidWrapWarn:
			out = append(out, Finding{
				ID:          "xid_wraparound_" + strings.Fields(g.name)[0],
				Severity:    SeverityWarn,
				Title:       fmt.Sprintf("%s age %s is climbing toward wraparound", g.name, humanInt(g.age)),
				Detail:      fmt.Sprintf("Oldest age is %d; the freeze wall is %d. Far enough that it is not urgent, close enough to watch.", g.age, xidWrapWall),
				Remediation: "Confirm autovacuum is enabled and keeping up; consider lowering vacuum thresholds on the largest, coldest tables.",
				ImpactScore: 100 * float64(g.age) / xidWrapWall,
			})
		}
	}
	return out
}

func ruleConnections(c *Context) []Finding {
	if c.Limits == nil || c.Limits.ConnMax <= 0 {
		return nil
	}
	ratio := float64(c.Limits.ConnUsed) / float64(c.Limits.ConnMax)
	sev := SeverityGood
	switch {
	case ratio >= connSatCrit:
		sev = SeverityCritical
	case ratio >= connSatWarn:
		sev = SeverityWarn
	default:
		return nil
	}
	return []Finding{{
		ID:          "connection_saturation",
		Severity:    sev,
		Title:       fmt.Sprintf("%d%% of max_connections in use", int(ratio*100)),
		Detail:      fmt.Sprintf("%d of %d client backends; leaving no headroom means new connections are refused.", c.Limits.ConnUsed, c.Limits.ConnMax),
		Remediation: "Put an application connection pool (PgBouncer, pgbouncer-in-front) in place; audit for leaked or per-request connections.",
		ImpactScore: 100 * ratio,
	}}
}

func ruleCache(c *Context) []Finding {
	if c.Rates == nil || c.Rates.CacheHitRatio == nil {
		return nil
	}
	ratio := *c.Rates.CacheHitRatio
	if ratio >= cacheHitWarn {
		return nil
	}
	return []Finding{{
		ID:          "low_cache_hit",
		Severity:    SeverityWarn,
		Title:       fmt.Sprintf("buffer cache hit ratio %.1f%%", ratio*100),
		Detail:      fmt.Sprintf("%.1f%% of %s block reads hit the shared buffers over the %.0fs window; the rest come off disk.", ratio*100, humanInt(c.Rates.CacheBlocks), c.Rates.WindowSec),
		Remediation: "Working set exceeds shared_buffers. Raise shared_buffers (to ~25% RAM) and effective_cache_size, or reduce contention / add cache.",
		ImpactScore: (1 - ratio) * 100,
	}}
}

func ruleRollback(c *Context) []Finding {
	if c.Rates == nil || c.Rates.RollbackRatio == nil {
		return nil
	}
	ratio := *c.Rates.RollbackRatio
	if ratio < rollbackRatioWarn {
		return nil
	}
	return []Finding{{
		ID:          "high_rollback_ratio",
		Severity:    SeverityWarn,
		Title:       fmt.Sprintf("%.1f%% of transactions rolled back", ratio*100),
		Detail:      fmt.Sprintf("%.1f%% of ~%s transactions in the %.0fs window were rollbacks — errors, conflicts, or aborted work.", ratio*100, humanInt(txnsOf(c.Rates)), c.Rates.WindowSec),
		Remediation: "Look for exceptions surfacing as rolled-back transactions in the application; a high rate often hides recurring errors.",
		ImpactScore: ratio * 100,
	}}
}

func txnsOf(r *Rates) int64 {
	// Recover the transaction total from rollback-per-sec ÷ rollback-ratio; the
	// result only feeds an evidence line, so rounding is acceptable.
	if r.RollbackRatio == nil || *r.RollbackRatio <= 0 || r.RollbacksPerSec <= 0 {
		return 0
	}
	rollbacks := r.RollbacksPerSec * r.WindowSec
	return int64(rollbacks / *r.RollbackRatio)
}

func ruleDeadlocks(c *Context) []Finding {
	if c.Rates == nil || c.Rates.DeadlocksDelta <= 0 {
		return nil
	}
	return []Finding{{
		ID:          "deadlocks",
		Severity:    SeverityWarn,
		Title:       fmt.Sprintf("%d deadlock(s) detected in the sample window", c.Rates.DeadlocksDelta),
		Detail:      "Postgres killed a backend to break each cycle; the victims' work was rolled back.",
		Remediation: "Inspect deadlock_detail in the log; acquire locks in a consistent order across transactions.",
		ImpactScore: 60,
	}}
}

func ruleUnusedIndexes(c *Context) []Finding {
	var out []Finding
	var reclaimed int64
	var evidence []string
	var caveats []string
	for _, ix := range c.Indexes {
		if ix.Scans != 0 || ix.Bytes < unusedIndexMinBytes {
			continue
		}
		if ix.IsPrimary || ix.BacksConstraint || ix.IsReplIdent {
			continue // dropping these breaks a guarantee or replication
		}
		reclaimed += ix.Bytes
		evidence = append(evidence, fmt.Sprintf("%s.%s (%s, %s)", ix.Schema, ix.Index, ix.Table, humanBytes(ix.Bytes)))
		if ix.IsUnique {
			caveats = append(caveats, "some entries are UNIQUE — dropping changes enforced constraints")
		}
		if ix.IsPartial || ix.IsExpression {
			caveats = append(caveats, "partial/expression indexes can be invisible to the scan counters yet still required")
		}
	}
	if len(evidence) == 0 {
		return nil
	}
	caveats = dedupe(caveats)
	return append(out, Finding{
		ID:          "unused_indexes",
		Severity:    SeverityInfo,
		Title:       fmt.Sprintf("%d index(es) never scanned, %s reclaimable", len(evidence), humanBytes(reclaimed)),
		Detail:      "idx_scan = 0 since the last stats reset and size over 1 MiB. Zero scans can also mean a read-replica or a rare code path uses them.",
		Remediation: "Confirm against the whole cluster's stats and any query patterns, then DROP INDEX one at a time (or set them INVALID first).",
		Evidence:    evidence,
		Caveats:     caveats,
		ImpactScore: sizeScore(reclaimed),
	})
}

func ruleBloat(c *Context) []Finding {
	var out []Finding
	var evidence []string
	for _, t := range c.Tables {
		total := t.LiveTuples + t.DeadTuples
		if total < deadRatioMinRows {
			continue
		}
		ratio := float64(t.DeadTuples) / float64(total)
		if ratio < deadRatioWarn {
			continue
		}
		evidence = append(evidence, fmt.Sprintf("%s.%s — %s dead of %s (%.0f%%)", t.Schema, t.Table, humanInt(t.DeadTuples), humanInt(total), ratio*100))
	}
	if len(evidence) == 0 {
		return nil
	}
	sortStrings(evidence)
	return append(out, Finding{
		ID:          "table_bloat",
		Severity:    SeverityWarn,
		Title:       fmt.Sprintf("%d table(s) over %d%% dead tuples", len(evidence), int(deadRatioWarn*100)),
		Detail:      "High dead-tuple share wastes space and drags seq-scans; autovacuum is not keeping up with the churn.",
		Remediation: "Lower autovacuum_vacuum_scale_factor (or set it per-table) on the offenders; VACUUM (ANALYZE) the biggest now.",
		Evidence:    evidence,
		ImpactScore: 70,
	})
}

func ruleSeqScan(c *Context) []Finding {
	var evidence []string
	for _, t := range c.Tables {
		if t.LiveTuples < seqScanMinRows {
			continue
		}
		if t.SeqScans+t.IndexScans < seqScanMinScans {
			continue
		}
		if t.SeqScans <= t.IndexScans*seqScanRatio {
			continue
		}
		evidence = append(evidence, fmt.Sprintf("%s.%s — %s seq vs %s index scans (%s rows)", t.Schema, t.Table, humanInt(t.SeqScans), humanInt(t.IndexScans), humanInt(t.LiveTuples)))
	}
	if len(evidence) == 0 {
		return nil
	}
	sortStrings(evidence)
	return []Finding{{
		ID:          "seq_scan_heavy",
		Severity:    SeverityInfo,
		Title:       fmt.Sprintf("%d large table(s) scanned mostly by full scan", len(evidence)),
		Detail:      "Sequential scans dominate index scans on big, hot tables — often a missing index, or a filter that cannot use one.",
		Remediation: "Check the queries hitting these tables (see the queries section) for predicates that could be indexed; a full scan on a small lookup table is fine.",
		Evidence:    evidence,
		ImpactScore: 40,
	}}
}

func ruleHotUpdates(c *Context) []Finding {
	var evidence []string
	for _, t := range c.Tables {
		if t.Updates < hotUpdateMinVolume {
			continue
		}
		ratio := float64(t.HotUpdates) / float64(t.Updates)
		if ratio >= hotUpdateWarnRatio {
			continue
		}
		evidence = append(evidence, fmt.Sprintf("%s.%s — %.0f%% HOT of %s updates", t.Schema, t.Table, ratio*100, humanInt(t.Updates)))
	}
	if len(evidence) == 0 {
		return nil
	}
	sortStrings(evidence)
	return []Finding{{
		ID:          "cold_hot_updates",
		Severity:    SeverityInfo,
		Title:       fmt.Sprintf("%d table(s) with few HOT updates (bloat-prone writes)", len(evidence)),
		Detail:      "Non-HOT updates rewrite index tuples too, accelerating bloat and vacuum load.",
		Remediation: "Avoid updating indexed columns on the hot path, or trim indexes on these tables so more updates qualify as HOT.",
		Evidence:    evidence,
		ImpactScore: 30,
	}}
}

func ruleStaleStats(c *Context) []Finding {
	var evidence []string
	for _, t := range c.Tables {
		age := analyzeAge(t.LastAnalyzeAgeS, t.LastAutoanalyzeAgeS)
		if age < staleStatsWarnSec {
			continue
		}
		evidence = append(evidence, fmt.Sprintf("%s.%s — last analyzed %s ago, %s mods since", t.Schema, t.Table, humanSec(float64(age)), humanInt(t.ModsSinceAnalyze)))
	}
	if len(evidence) == 0 {
		return nil
	}
	sortStrings(evidence)
	return []Finding{{
		ID:          "stale_statistics",
		Severity:    SeverityWarn,
		Title:       fmt.Sprintf("%d table(s) not analyzed in over 30 days", len(evidence)),
		Detail:      "Planner statistics older than a month, with pending modifications — plans drift and can regress sharply.",
		Remediation: "ANALYZE the listed tables; if it recurs, lower autovacuum_analyze_scale_factor so autoanalyze fires sooner.",
		Evidence:    evidence,
		ImpactScore: 50,
	}}
}

func ruleVacuumHygiene(c *Context) []Finding {
	var disabled, neverVac []string
	for _, t := range c.Tables {
		if t.RelAutovacuumEnabled != nil && *t.RelAutovacuumEnabled == "false" {
			disabled = append(disabled, fmt.Sprintf("%s.%s (%s)", t.Schema, t.Table, humanBytes(t.TotalBytes)))
		}
		if t.LastVacuumAgeS < 0 && t.LastAutovacuumAgeS < 0 && t.AutovacuumCount == 0 && t.TotalBytes > unusedIndexMinBytes {
			neverVac = append(neverVac, fmt.Sprintf("%s.%s (%s)", t.Schema, t.Table, humanBytes(t.TotalBytes)))
		}
	}
	var out []Finding
	if len(disabled) > 0 {
		out = append(out, Finding{
			ID:          "autovacuum_disabled_on_table",
			Severity:    SeverityCritical,
			Title:       fmt.Sprintf("%d table(s) with autovacuum disabled", len(disabled)),
			Detail:      "Per-table autovacuum_enabled=false means dead tuples and xid age accumulate here unless a job vacuums it explicitly.",
			Remediation: "Re-enable autovacuum, or guarantee a scheduled VACUUM for each listed table.",
			Evidence:    disabled,
			ImpactScore: 90,
		})
	}
	if len(neverVac) > 0 {
		out = append(out, Finding{
			ID:          "table_never_vacuumed",
			Severity:    SeverityWarn,
			Title:       fmt.Sprintf("%d sizeable table(s) never vacuumed", len(neverVac)),
			Detail:      "No manual or automatic vacuum has ever run on these tables; if they are updated/deleted on, bloat and xid age grow unchecked.",
			Remediation: "Check autovacuum is enabled for them (see autovacuum_disabled_on_table) and run an initial VACUUM (ANALYZE).",
			Evidence:    neverVac,
			ImpactScore: 45,
		})
	}
	return out
}

func ruleSessions(c *Context) []Finding {
	if c.Activity == nil {
		return nil
	}
	var out []Finding
	a := c.Activity
	if a.LongestXactSec >= longXactWarnSec {
		out = append(out, Finding{
			ID:          "long_running_transaction",
			Severity:    SeverityWarn,
			Title:       fmt.Sprintf("a transaction has been open for %s", shortDur(sec(a.LongestXactSec))),
			Detail:      "A long-open transaction pins the xmin horizon: vacuum cannot reclaim anything newer, and it may hold locks.",
			Remediation: "Find the backend (pg_stat_activity, xact_start) and either let it finish or pg_terminate_backend it.",
			ImpactScore: clamp(a.LongestXactSec/3600*40, 30, 100),
		})
	}
	if a.IdleInTransaction > 0 && a.LongestXactSec >= idleTxnWarnSec {
		sev := SeverityInfo
		if a.LongestXactSec >= idleTnxCritSec {
			sev = SeverityWarn
		}
		out = append(out, Finding{
			ID:          "idle_in_transaction",
			Severity:    sev,
			Title:       fmt.Sprintf("%d idle-in-transaction session(s), longest %s", a.IdleInTransaction, shortDur(sec(a.LongestXactSec))),
			Detail:      "Sessions holding an open transaction while idle block cleanup and can hold row locks indefinitely.",
			Remediation: "Set idle_in_transaction_session_timeout; fix the app path that opens a transaction and pauses without committing.",
			ImpactScore: clamp(a.LongestXactSec/3600*30, 20, 90),
		})
	}
	return out
}

func ruleSettings(c *Context) []Finding {
	var out []Finding
	durability := func(id, name, bad, detail, rem string) {
		if v, ok := settingValue(c, name); ok && strings.EqualFold(v, bad) {
			out = append(out, Finding{
				ID: id, Severity: SeverityCritical,
				Title:  fmt.Sprintf("%s = %s", name, v),
				Detail: detail, Remediation: rem, ImpactScore: 95,
			})
		}
	}
	durability("fsync_off", "fsync", "off",
		"fsync is off: a crash or power loss can leave the data directory corrupt. Not a performance knob.",
		"Set fsync = on (it should only ever be off in a throwaway restore test).")
	durability("full_page_writes_off", "full_page_writes", "off",
		"full_page_writes off risks torn pages surviving crash recovery.",
		"Set full_page_writes = on unless you fully understand the consequences.")
	if v, ok := settingValue(c, "data_checksums"); ok && v == "off" {
		out = append(out, Finding{
			ID: "no_data_checksums", Severity: SeverityInfo,
			Title:       "data checksums disabled",
			Detail:      "data_checksums=off means silent storage corruption cannot be detected at read time. It is set at initdb and cannot be changed without a rewrite.",
			Remediation: "Consider initdb with --data-checksums for future clusters; the cost is usually negligible.",
			ImpactScore: 15,
		})
	}
	return out
}

// ruleGood emits a short list of positives so a clean database reads as
// "checked and healthy", not "nothing ran".
func ruleGood(c *Context) []Finding {
	var out []Finding
	if c.Health == nil {
		return out
	}
	if c.Health.StatsAgeS < shortStatsWindow {
		out = append(out, good("stats_recent", "statistics window covers the sample interval",
			fmt.Sprintf("counters were reset %s ago — deltas and ratios reflect a live window.", humanSec(float64(c.Health.StatsAgeS)))))
	} else {
		out = append(out, Finding{
			ID: "short_stats_window", Severity: SeverityInfo,
			Title:       fmt.Sprintf("statistics were reset %s ago", humanSec(float64(c.Health.StatsAgeS))),
			Detail:      "Counters have only been accumulating since the last pg_stat_reset; cumulative views (per-table scans, statement totals) describe a short window.",
			ImpactScore: 1,
		})
	}
	if c.Rates != nil && c.Rates.CacheHitRatio != nil && *c.Rates.CacheHitRatio >= cacheHitWarn {
		out = append(out, good("cache_hit_good", fmt.Sprintf("buffer cache hit ratio %.1f%%", *c.Rates.CacheHitRatio*100),
			"workload is served almost entirely from shared buffers over the sampled window."))
	}
	if c.Limits != nil && c.Limits.MaxXidAge < xidWrapWarn {
		out = append(out, good("xid_age_healthy", "transaction-ID age well below the freeze wall",
			fmt.Sprintf("oldest xid age %s; wraparound is not a near-term concern.", humanInt(c.Limits.MaxXidAge))))
	}
	return out
}

const shortStatsWindow = 7 * 24 * 3600 // a stats window under a week makes cumulative rules soft

func good(id, title, detail string) Finding {
	return Finding{ID: id, Severity: SeverityGood, Title: title, Detail: detail}
}

// helpers -------------------------------------------------------------------

func sec(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func sortStrings(s []string) { sort.Strings(s) }

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
