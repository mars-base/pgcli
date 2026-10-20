-- Connection breakdown by backend state (adapted from pgbot, Apache-2.0).
-- Grouped maxes let the runner derive the global longest transaction / active
-- query by taking max across rows in Go; the idle-in-transaction count is the
-- sum of n over the idle states. Client backends only, our own backend excluded
-- so the monitor doesn't inflate the saturation figure it reports.
SELECT json_agg(t) FROM (
SELECT coalesce(state, 'unknown') AS state,
       count(*)                   AS n,
       coalesce(max(extract(epoch FROM now() - xact_start)), 0)::float8  AS max_xact_age_s,
       coalesce(max(extract(epoch FROM now() - query_start))
                FILTER (WHERE state = 'active'), 0)::float8              AS max_active_age_s
FROM pg_stat_activity
WHERE pid <> pg_backend_pid()
  AND backend_type = 'client backend'
GROUP BY 1
) t;
