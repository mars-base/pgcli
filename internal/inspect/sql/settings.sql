-- Key tuning parameters, always present, with display values, so the rules can
-- name the current setting (adapted from pgbot, Apache-2.0). One UNION arm for
-- the tuning whitelist; the other for parameters a human moved off their
-- compiled-in default (source tells where).
SELECT json_agg(t) FROM (
SELECT name, setting AS value, unit, 'tuning' AS kind, source
FROM pg_settings
WHERE name IN (
  'work_mem', 'maintenance_work_mem', 'max_wal_size', 'max_connections',
  'shared_buffers', 'effective_cache_size', 'autovacuum_max_workers',
  'autovacuum_vacuum_scale_factor', 'autovacuum_vacuum_threshold',
  'autovacuum_analyze_scale_factor', 'autovacuum_analyze_threshold',
  'random_page_cost', 'track_io_timing',
  'fsync', 'full_page_writes', 'synchronous_commit', 'statement_timeout',
  'archive_mode', 'wal_level', 'data_checksums', 'ignore_checksum_failure',
  'log_statement', 'default_statistics_target'
)
UNION ALL
SELECT name, setting AS value, unit, 'override' AS kind, source
FROM pg_settings
WHERE setting IS DISTINCT FROM boot_val
  AND source NOT IN ('session', 'client')
  AND name NOT IN (
    'work_mem', 'maintenance_work_mem', 'max_wal_size', 'max_connections',
    'shared_buffers', 'effective_cache_size', 'autovacuum_max_workers',
    'autovacuum_vacuum_scale_factor', 'autovacuum_vacuum_threshold',
    'autovacuum_analyze_scale_factor', 'autovacuum_analyze_threshold',
    'random_page_cost', 'track_io_timing',
    'fsync', 'full_page_writes', 'synchronous_commit', 'statement_timeout',
    'archive_mode', 'wal_level', 'data_checksums', 'ignore_checksum_failure',
    'log_statement', 'default_statistics_target'
  )
ORDER BY 1
) t;
