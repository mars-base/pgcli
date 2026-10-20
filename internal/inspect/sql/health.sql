-- pg_stat_database for the current database (adapted from pgbot, Apache-2.0).
-- Counter fields are sampled twice ~interval apart by the runner; the delta
-- over the window is the rate. numbackends is read from the second sample as
-- the current gauge; stats_reset becomes the age of the statistics window in
-- seconds (a number — psql's timestamp text format is not portable).
SELECT json_agg(t) FROM (
SELECT numbackends,
       xact_commit, xact_rollback,
       blks_read, blks_hit,
       tup_returned, tup_fetched, tup_inserted, tup_updated, tup_deleted,
       deadlocks, temp_bytes,
       coalesce(extract(epoch FROM now() - stats_reset), 0)::bigint AS stats_age_s
FROM pg_stat_database
WHERE datname = current_database()
) t;
