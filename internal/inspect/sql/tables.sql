-- Largest user tables with vacuum/scan/bloat signals (adapted from pgbot,
-- Apache-2.0). Sizes and dead-tuple counts are gauges; scan counts are
-- cumulative. Last-vacuum/analyze timestamps become ages in seconds (a NULL
-- age means never). Per-table reloptions override the global autovacuum knobs
-- — a frequent source of surprise. This one row set feeds both the vacuum and
-- tables sections.
SELECT json_agg(t) FROM (
SELECT s.schemaname                       AS schema,
       s.relname                          AS "table",
       pg_total_relation_size(s.relid)    AS total_bytes,
       s.n_live_tup                       AS live_tuples,
       s.n_dead_tup                       AS dead_tuples,
       s.seq_scan                         AS seq_scans,
       coalesce(s.idx_scan, 0)            AS index_scans,
       s.n_mod_since_analyze              AS mods_since_analyze,
       s.n_tup_upd                        AS updates,
       s.n_tup_hot_upd                    AS hot_updates,
       coalesce(extract(epoch FROM now() - s.last_analyze),     -1)::bigint AS last_analyze_age_s,
       coalesce(extract(epoch FROM now() - s.last_autoanalyze), -1)::bigint AS last_autoanalyze_age_s,
       coalesce(extract(epoch FROM now() - s.last_vacuum),      -1)::bigint AS last_vacuum_age_s,
       coalesce(extract(epoch FROM now() - s.last_autovacuum),  -1)::bigint AS last_autovacuum_age_s,
       s.autovacuum_count                 AS autovacuum_count,
       (SELECT option_value FROM pg_options_to_table(c.reloptions)
          WHERE option_name = 'autovacuum_enabled')  AS rel_autovacuum_enabled
FROM pg_stat_user_tables s
JOIN pg_class c ON c.oid = s.relid
ORDER BY total_bytes DESC
LIMIT 30
) t;
