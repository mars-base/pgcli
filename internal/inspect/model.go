// The health report's data shapes. Rows mirror the SQL aliases exactly (psql
// returns json_agg output keyed by those aliases); derived values (rates,
// cache-hit ratio, longest-xact ages) live in separate structs the runner
// computes, so the JSON decode and the analysis never tangle.
package inspect

import "time"

// Severity grades a Finding. The four levels are the report's spine: the
// summary groups by them and the health score weights by them.
type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityWarn     Severity = "WARNING"
	SeverityInfo     Severity = "NOTE"
	SeverityGood     Severity = "GOOD"
)

// Finding is one deterministic diagnosis. ID is a stable slug (unused_indexes,
// txid_wraparound); Evidence carries one line per offending object for aggregate
// findings; ImpactScore (0-100) sorts within a severity; Caveats are the
// inline "but…" clauses that stop a confident wrong recommendation.
type Finding struct {
	ID          string   `json:"id"`
	Severity    Severity `json:"severity"`
	Title       string   `json:"title"`
	Detail      string   `json:"detail,omitempty"`
	Remediation string   `json:"remediation,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	ImpactScore float64  `json:"impact_score"`
	Caveats     []string `json:"caveats,omitempty"`
}

// HealthRow is one pg_stat_database sample (counters cumulative since stats
// reset).
type HealthRow struct {
	NumBackends  int32 `json:"numbackends"`
	XactCommit   int64 `json:"xact_commit"`
	XactRollback int64 `json:"xact_rollback"`
	BlksRead     int64 `json:"blks_read"`
	BlksHit      int64 `json:"blks_hit"`
	TupReturned  int64 `json:"tup_returned"`
	TupFetched   int64 `json:"tup_fetched"`
	TupInserted  int64 `json:"tup_inserted"`
	TupUpdated   int64 `json:"tup_updated"`
	TupDeleted   int64 `json:"tup_deleted"`
	Deadlocks    int64 `json:"deadlocks"`
	TempBytes    int64 `json:"temp_bytes"`
	StatsAgeS    int64 `json:"stats_age_s"`
}

// Rates are the per-second deltas between two HealthRow samples. CacheHitRatio
// is nil when the window sampled too few blocks to mean anything.
type Rates struct {
	CommitsPerSec   float64   `json:"commits_per_sec"`
	RollbacksPerSec float64   `json:"rollbacks_per_sec"`
	DeadlocksDelta  int64     `json:"deadlocks_delta"`
	TempBytesDelta  int64     `json:"temp_bytes_delta"`
	CacheHitRatio   *float64  `json:"cache_hit_ratio,omitempty"`
	CacheBlocks     int64     `json:"cache_blocks"`
	RollbackRatio   *float64  `json:"rollback_ratio,omitempty"`
	WindowSec       float64   `json:"window_sec"`
	StatsReset      time.Time `json:"-"`
}

// LimitsRow is the cluster-wide saturation/wraparound gauges.
type LimitsRow struct {
	ConnUsed   int   `json:"conn_used"`
	ConnMax    int   `json:"conn_max"`
	MaxXidAge  int64 `json:"max_xid_age"`
	MaxMxidAge int64 `json:"max_mxid_age"`
}

// QueryRow is one pg_stat_statements entry. Query text is normalized ($1) by
// the server; TotalMs/Share come from cumulative counters.
type QueryRow struct {
	Queryid      int64   `json:"queryid"`
	Query        string  `json:"query"`
	Calls        int64   `json:"calls"`
	TotalMs      float64 `json:"total_ms"`
	MeanMs       float64 `json:"mean_ms"`
	Rows         int64   `json:"rows"`
	SharedHit    int64   `json:"shared_blks_hit"`
	SharedRead   int64   `json:"shared_blks_read"`
	TotalExecAll float64 `json:"total_exec_all"`
}

// Share is this query's fraction of all statement time (0..1); nil when the
// denominator is unusable.
func (q *QueryRow) Share() *float64 {
	if q.TotalExecAll <= 0 {
		return nil
	}
	s := q.TotalMs / q.TotalExecAll
	return &s
}

// IndexRow is one user index with the safety flags the unused-index rule needs
// to avoid recommending an outage.
type IndexRow struct {
	Schema          string `json:"schema"`
	Table           string `json:"table"`
	Index           string `json:"index"`
	Scans           int64  `json:"scans"`
	Bytes           int64  `json:"bytes"`
	Method          string `json:"method"`
	IsPrimary       bool   `json:"is_primary"`
	IsUnique        bool   `json:"is_unique"`
	IsReplIdent     bool   `json:"is_replident"`
	IsPartial       bool   `json:"is_partial"`
	IsExpression    bool   `json:"is_expression"`
	BacksConstraint bool   `json:"backs_constraint"`
	Definition      string `json:"definition"`
}

// TableRow is one pg_stat_user_tables row; it feeds both the tables and the
// vacuum sections. The *_age_s columns are -1 when never (NULL in the view).
type TableRow struct {
	Schema               string  `json:"schema"`
	Table                string  `json:"table"`
	TotalBytes           int64   `json:"total_bytes"`
	LiveTuples           int64   `json:"live_tuples"`
	DeadTuples           int64   `json:"dead_tuples"`
	SeqScans             int64   `json:"seq_scans"`
	IndexScans           int64   `json:"index_scans"`
	ModsSinceAnalyze     int64   `json:"mods_since_analyze"`
	Updates              int64   `json:"updates"`
	HotUpdates           int64   `json:"hot_updates"`
	LastAnalyzeAgeS      int64   `json:"last_analyze_age_s"`
	LastAutoanalyzeAgeS  int64   `json:"last_autoanalyze_age_s"`
	LastVacuumAgeS       int64   `json:"last_vacuum_age_s"`
	LastAutovacuumAgeS   int64   `json:"last_autovacuum_age_s"`
	AutovacuumCount      int64   `json:"autovacuum_count"`
	RelAutovacuumEnabled *string `json:"rel_autovacuum_enabled"`
}

// SettingRow is one pg_settings row; Kind is "tuning" (the whitelist, always)
// or "override" (moved off its compiled-in default by configuration).
type SettingRow struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Unit   string `json:"unit"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

// ActivityRow is one pg_stat_activity state group; ages are seconds.
type ActivityRow struct {
	State         string  `json:"state"`
	N             int     `json:"n"`
	MaxXactAgeS   float64 `json:"max_xact_age_s"`
	MaxActiveAgeS float64 `json:"max_active_age_s"`
}

// Activity is the point-in-time rollup the rules read.
type Activity struct {
	Total             int
	Active            int
	Idle              int
	IdleInTransaction int
	LongestXactSec    float64
	LongestActiveSec  float64
}

// Context is one single-shot inspection: the sampled sections, the rollups,
// and the findings computed over them. The tags define the --json contract,
// matching the snake_case style of the row structs below.
type Context struct {
	Server   string  `json:"server"`     // e.g. "postgres 16.3"
	Version  int     `json:"version"`    // server_version_num
	Target   string  `json:"target"`     // redacted connection summary
	Interval float64 `json:"interval_s"` // counter sample window, seconds

	Health   *HealthRow   `json:"health,omitempty"`
	Limits   *LimitsRow   `json:"limits,omitempty"`
	Rates    *Rates       `json:"rates,omitempty"`
	Queries  []QueryRow   `json:"queries,omitempty"`
	Indexes  []IndexRow   `json:"indexes,omitempty"`
	Tables   []TableRow   `json:"tables,omitempty"`
	Settings []SettingRow `json:"settings,omitempty"`
	Activity *Activity    `json:"activity,omitempty"`

	Findings []Finding `json:"findings"`
}
