package inspect

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeQuerier answers each diagnostic query with canned psql -tA output
// (a json_agg array). It is stateful only for the health double-sample, so the
// second read returns bumped counters and the runner can compute a rate.
type fakeQuerier struct {
	healthCalls int
	pgss        string // "" → pretend pg_stat_statements is absent
}

func (f *fakeQuerier) Query(sql string) (string, error) {
	lower := strings.ToLower(sql)
	switch {
	case strings.Contains(lower, "current_setting('server_version_num')"):
		return "160003\n", nil
	case strings.Contains(lower, "pg_extension"):
		if f.pgss == "" {
			return "\n", nil
		}
		return f.pgss + "\n", nil
	case strings.Contains(lower, "pg_stat_database"):
		f.healthCalls++
		if f.healthCalls == 1 {
			return `[{"numbackends":5,"xact_commit":100,"xact_rollback":5,"blks_read":10,"blks_hit":90,"deadlocks":0,"temp_bytes":0,"stats_age_s":3600,"tup_returned":0,"tup_fetched":0,"tup_inserted":0,"tup_updated":0,"tup_deleted":0}]`, nil
		}
		// 1s later: +60 commits, +10000 blocks mostly hit, one deadlock.
		return `[{"numbackends":5,"xact_commit":160,"xact_rollback":7,"blks_read":2010,"blks_hit":11090,"deadlocks":1,"temp_bytes":4096,"stats_age_s":3601,"tup_returned":0,"tup_fetched":0,"tup_inserted":0,"tup_updated":0,"tup_deleted":0}]`, nil
	case strings.Contains(lower, "datfrozenxid"):
		return `[{"conn_used":90,"conn_max":100,"max_xid_age":1500000000,"max_mxid_age":1000}]`, nil
	case strings.Contains(lower, "pg_stat_statements"):
		return `[{"queryid":123,"query":"SELECT $1 FROM t","calls":10,"total_ms":500,"mean_ms":50,"rows":100,"shared_blks_hit":200,"shared_blks_read":5,"total_exec_all":600}]`, nil
	case strings.Contains(lower, "pg_stat_user_indexes"):
		return `[{"schema":"public","table":"t","index":"junk_idx","scans":0,"bytes":8388608,"method":"btree","is_primary":false,"is_unique":false,"is_replident":false,"is_partial":false,"is_expression":false,"backs_constraint":false,"definition":"CREATE INDEX junk_idx ON t (x)"}]`, nil
	case strings.Contains(lower, "pg_stat_user_tables"):
		return `[{"schema":"public","table":"big","total_bytes":52428800,"live_tuples":100000,"dead_tuples":40000,"seq_scans":900,"index_scans":10,"mods_since_analyze":1000,"updates":50000,"hot_updates":1000,"last_analyze_age_s":2678400,"last_autoanalyze_age_s":-1,"last_vacuum_age_s":-1,"last_autovacuum_age_s":-1,"autovacuum_count":0,"rel_autovacuum_enabled":null}]`, nil
	case strings.Contains(lower, "pg_settings"):
		return `[{"name":"fsync","value":"on","unit":"","kind":"tuning","source":"default"},{"name":"max_connections","value":"100","unit":"","kind":"tuning","source":"config file"},{"name":"work_mem","value":"4","unit":"MB","kind":"tuning","source":"default"}]`, nil
	case strings.Contains(lower, "pg_stat_activity"):
		return `[{"state":"active","n":1,"max_xact_age_s":400,"max_active_age_s":10},{"state":"idle in transaction","n":2,"max_xact_age_s":5000,"max_active_age_s":0},{"state":"idle","n":3,"max_xact_age_s":0,"max_active_age_s":0}]`, nil
	default:
		return "", fmt.Errorf("fakeQuerier: unexpected query %.60s", sql)
	}
}

func TestRunEndToEnd(t *testing.T) {
	f := &fakeQuerier{pgss: "public"}
	c, err := Run(f, "test target", Options{Interval: time.Second, Limit: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.Server != "postgres 16.3" || c.Version != 160003 {
		t.Errorf("version probe wrong: %+v", c)
	}
	if c.Health == nil || c.Health.XactCommit != 160 {
		t.Errorf("health should be the second sample, got %+v", c.Health)
	}
	if c.Rates == nil || c.Rates.CommitsPerSec != 60 {
		t.Errorf("commits/sec should be 60 over 1s, got %+v", c.Rates)
	}
	if c.Rates.DeadlocksDelta != 1 {
		t.Errorf("expected one deadlock delta, got %+v", c.Rates)
	}
	if len(c.Queries) != 1 || len(c.Tables) != 1 || len(c.Indexes) != 1 || len(c.Settings) != 3 {
		for _, f := range c.Findings {
			if f.ID == "section_queries" {
				t.Logf("section_queries note: %s", f.Detail)
			}
		}
		t.Errorf("section row counts wrong: q=%d t=%d i=%d s=%d", len(c.Queries), len(c.Tables), len(c.Indexes), len(c.Settings))
	}
	if c.Activity == nil || c.Activity.IdleInTransaction != 2 {
		t.Errorf("activity rollup wrong: %+v", c.Activity)
	}
	// Rules should have fired against the deliberately-awful fixture.
	want := []string{"connection_saturation", "xid_wraparound_transaction", "table_bloat", "unused_indexes", "long_running_transaction"}
	for _, id := range want {
		if !hasID(c.Findings, id) {
			t.Errorf("expected finding %q, got ids: %s", id, idList(c.Findings))
		}
	}
}

// TestRunDegradesGracefully: pg_stat_statements absent → queries section empty
// but a NOTE finding appears and Run still succeeds (a partial report beats none).
func TestRunDegradesGracefully(t *testing.T) {
	f := &fakeQuerier{pgss: ""}
	c, err := Run(f, "target", Options{Interval: 500 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(c.Queries) != 0 {
		t.Errorf("queries should be empty without the extension, got %d", len(c.Queries))
	}
	if !hasFinding(c.Findings, "section_queries", SeverityInfo) {
		t.Errorf("missing a NOTE about the absent queries section: %s", idList(c.Findings))
	}
	// The absent-extension NOTE must recommend installing it via pgcli.
	for _, f := range c.Findings {
		if f.ID == "section_queries" {
			if !strings.Contains(f.Remediation, "pg extension install pg_stat_statements") {
				t.Errorf("remediation should suggest the install command, got %q", f.Remediation)
			}
		}
	}
}

func idList(fs []Finding) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, string(f.Severity)+":"+f.ID)
	}
	return strings.Join(parts, ", ")
}
