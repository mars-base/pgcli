-- Top of pg_stat_statements by total execution time (adapted from pgbot,
-- Apache-2.0). The runner substitutes the indexed verbs below: argument 1 is
-- the version-appropriate total-time column (total_exec_time / total_time,
-- chosen from server_version_num), argument 2 the schema-quoted relation
-- (pg_stat_statements can live outside public — Supabase installs it in
-- "extensions"), argument 3 the LIMIT row count. All three come from fixed
-- values or server-controlled catalog lookups in Go, never user input.
-- Transaction-control and session-GUC statements are dropped: they dominate a
-- quiet database's counters but are never "the query eating your database".
-- Cumulative since stats reset.
SELECT json_agg(t) FROM (
SELECT queryid,
       left(query, 400)         AS query,
       calls,
       %[1]s                     AS total_ms,
       %[1]s / nullif(calls, 0)  AS mean_ms,
       rows,
       shared_blks_hit,
       shared_blks_read,
       sum(%[1]s) OVER ()        AS total_exec_all
FROM %[2]s
WHERE queryid IS NOT NULL AND calls > 0
  AND query NOT ILIKE '%%pg_stat_statements%%'
  AND query !~* '^\s*(set|show|reset|begin|commit|rollback|discard|deallocate|start)\M'
ORDER BY %[1]s DESC
LIMIT %[3]d
) t;
