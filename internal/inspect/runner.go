package inspect

import (
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

//go:embed sql
var sqlFS embed.FS

// The .sql files are plain diagnostic queries adapted from pgbot (Apache-2.0,
// https://github.com/pgrundev/pgbot), each wrapped in
//
//	SELECT json_agg(t) FROM ( … ) t
//
// so one psql -tA invocation returns the whole result set as a single JSON
// array keyed by the column aliases — which is exactly what the row structs'
// json tags in model.go decode. pgcli never holds a database connection
// itself; the Querier below is satisfied by psql-over-podman, keeping this
// package driver-free and fakeable in tests.
//
// queries.sql carries three format verbs (%s total-time column, %s relation,
// %d limit) substituted by the runner from fixed or server-derived values —
// never user input.

func loadSQL(name string) (string, error) {
	b, err := sqlFS.ReadFile("sql/" + name)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Querier runs one read-only SQL statement and returns psql -tA output.
type Querier interface {
	Query(sql string) (string, error)
}

// Options tunes a run.
type Options struct {
	Interval time.Duration // gap between the two counter samples (default 1s)
	Limit    int           // rows kept from pg_stat_statements (default 20)
}

// note is a degradation line (a domain that could not be sampled); it renders
// as a NOTE finding and explains an empty section.
type note struct {
	Section string
	Reason  string
}

// Run samples every section once (health twice, for rates) and assembles the
// report Context. A failure to reach the server at all is fatal; a single
// domain failing (missing extension, one view unreadable) degrades to an empty
// section plus a note — a report on a partially-visible database beats none.
func Run(q Querier, target string, opts Options) (*Context, error) {
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}

	c := &Context{Target: target, Interval: opts.Interval.Seconds()}
	var notes []note

	ver, err := queryScalar(q, "SELECT current_setting('server_version_num')::int")
	if err != nil {
		return nil, fmt.Errorf("cannot reach the database: %w", err)
	}
	n, err := strconv.Atoi(ver)
	if err != nil || n == 0 {
		return nil, fmt.Errorf("unexpected server_version_num %q", ver)
	}
	c.Version = n
	c.Server = fmt.Sprintf("postgres %d.%d", n/10000, n%100) // PG10+: MAJOR*10000+MINOR

	// pg_stat_statements is an extension and may live outside public (Supabase
	// installs it in "extensions"); the schema comes from the catalog.
	pgss := ""
	if ns, err := queryScalar(q, `SELECT coalesce(n.nspname, '')
FROM pg_extension e LEFT JOIN pg_namespace n ON n.oid = e.extnamespace
WHERE e.extname = 'pg_stat_statements'`); err == nil && ns != "" {
		pgss = "pg_stat_statements"
		if ns != "public" && ns != "pg_catalog" {
			pgss = strconv.Quote(ns) + ".pg_stat_statements"
		}
	}

	totalCol := "total_exec_time"
	if n < 130000 {
		totalCol = "total_time" // renamed in PG 13
	}

	// health: two samples bracketing the interval; the delta is the rate.
	hsql, err := loadSQL("health.sql")
	if err != nil {
		return nil, err
	}
	h0, errH0 := queryRows[HealthRow](q, hsql)
	time.Sleep(opts.Interval)
	h1, errH1 := queryRows[HealthRow](q, hsql)
	if errH0 == nil && errH1 == nil && len(h0) == 1 && len(h1) == 1 {
		c.Health = &h1[0]
		c.Rates = computeRates(h0[0], h1[0], opts.Interval)
	} else {
		notes = append(notes, note{"health", "pg_stat_database could not be sampled"})
	}

	// limits: a single rollup row.
	{
		var rows []LimitsRow
		runFile(&notes, "connections/limits", "limits.sql", func(sql string) error {
			return queryInto(q, sql, &rows)
		})
		if len(rows) == 1 {
			c.Limits = &rows[0]
		}
	}

	if pgss == "" {
		notes = append(notes, note{"queries", "the pg_stat_statements extension is not installed"})
	} else if qs, err := loadSQL("queries.sql"); err != nil {
		notes = append(notes, note{"queries", err.Error()})
	} else {
		sql := fmt.Sprintf(qs, totalCol, pgss, opts.Limit)
		runSection(&notes, "queries", func() error { return queryInto(q, sql, &c.Queries) })
	}

	runFile(&notes, "indexes", "indexes.sql", func(sql string) error { return queryInto(q, sql, &c.Indexes) })
	runFile(&notes, "tables", "tables.sql", func(sql string) error { return queryInto(q, sql, &c.Tables) })
	runFile(&notes, "settings", "settings.sql", func(sql string) error { return queryInto(q, sql, &c.Settings) })
	runFile(&notes, "sessions", "activity.sql", func(sql string) error {
		var rows []ActivityRow
		if err := queryInto(q, sql, &rows); err != nil {
			return err
		}
		c.Activity = rollActivity(rows)
		return nil
	})

	c.Findings = Analyze(c, notes)
	return c, nil
}

// queryScalar runs a single-value query (no json_agg wrapper) and trims it.
func queryScalar(q Querier, sql string) (string, error) {
	out, err := q.Query(sql)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// queryRows runs one query and decodes its json_agg array into row structs. An
// empty result set (psql prints a bare newline for NULL) is a valid empty
// slice, not an error.
func queryRows[T any](q Querier, sql string) ([]T, error) {
	out, err := q.Query(sql)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return nil, nil
	}
	var rows []T
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, fmt.Errorf("decode: %w (output %.120q)", err, out)
	}
	return rows, nil
}

// queryInto is queryRows for callers that already hold the destination slice
// (so the runner can populate Context fields in place).
func queryInto[T any](q Querier, sql string, dst *[]T) error {
	rows, err := queryRows[T](q, sql)
	if err != nil {
		return err
	}
	*dst = rows
	return nil
}

// runSection notes a section's failure. An empty result is a valid empty
// section, not a failure — so the closure only returns an error for real errors.
func runSection(notes *[]note, section string, run func() error) {
	if err := run(); err != nil {
		*notes = append(*notes, note{section, err.Error()})
	}
}

// runFile loads one .sql file, hands it to run, and notes any failure.
func runFile(notes *[]note, section, file string, run func(sql string) error) {
	sql, err := loadSQL(file)
	if err != nil {
		*notes = append(*notes, note{section, err.Error()})
		return
	}
	runSection(notes, section, func() error { return run(sql) })
}

// computeRates turns two cumulative pg_stat_database samples into per-second
// rates and window ratios. Cache-hit and rollback ratios stay nil when the
// window sampled too little traffic to mean anything (pgbot's floors: 10k
// blocks, 20 transactions).
func computeRates(a, b HealthRow, window time.Duration) *Rates {
	secs := window.Seconds()
	if secs <= 0 {
		secs = 1
	}
	r := &Rates{
		WindowSec:       secs,
		CommitsPerSec:   float64(b.XactCommit-a.XactCommit) / secs,
		RollbacksPerSec: float64(b.XactRollback-a.XactRollback) / secs,
	}
	if d := b.Deadlocks - a.Deadlocks; d > 0 {
		r.DeadlocksDelta = d
	}
	if d := b.TempBytes - a.TempBytes; d > 0 {
		r.TempBytesDelta = d
	}
	hit := b.BlksHit - a.BlksHit
	read := b.BlksRead - a.BlksRead
	if blocks := hit + read; blocks >= int64(cacheHitMinBlocks) {
		r.CacheBlocks = blocks
		ratio := float64(hit) / float64(blocks)
		r.CacheHitRatio = &ratio
	}
	if txns := (b.XactCommit - a.XactCommit) + (b.XactRollback - a.XactRollback); txns >= int64(rollbackMinTxns) {
		ratio := float64(b.XactRollback-a.XactRollback) / float64(txns)
		r.RollbackRatio = &ratio
	}
	return r
}

// rollActivity aggregates the by-state pg_stat_activity rows into the
// point-in-time summary the rules read.
func rollActivity(rows []ActivityRow) *Activity {
	a := &Activity{}
	for _, r := range rows {
		a.Total += r.N
		switch {
		case r.State == "active":
			a.Active += r.N
		case strings.HasPrefix(r.State, "idle in transaction"):
			a.IdleInTransaction += r.N
		case r.State == "idle":
			a.Idle += r.N
		}
		if r.MaxXactAgeS > a.LongestXactSec {
			a.LongestXactSec = r.MaxXactAgeS
		}
		if r.MaxActiveAgeS > a.LongestActiveSec {
			a.LongestActiveSec = r.MaxActiveAgeS
		}
	}
	return a
}
