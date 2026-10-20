-- User indexes with scan counts + size + the safety flags the unused-index rule
-- needs to avoid recommending an outage (adapted from pgbot, Apache-2.0):
--   backs_constraint — enforces a PK/UNIQUE/EXCLUSION constraint; cannot drop.
--   is_replident     — the table's REPLICA IDENTITY index; never "unused".
--   is_partial / is_expression / method — shapes zero-scan can't rule out.
SELECT json_agg(t) FROM (
SELECT s.schemaname            AS schema,
       s.relname               AS "table",
       s.indexrelname          AS "index",
       coalesce(s.idx_scan, 0) AS scans,
       pg_relation_size(s.indexrelid) AS bytes,
       am.amname               AS method,
       ix.indisprimary         AS is_primary,
       ix.indisunique          AS is_unique,
       ix.indisreplident       AS is_replident,
       (ix.indpred IS NOT NULL)  AS is_partial,
       (ix.indexprs IS NOT NULL) AS is_expression,
       EXISTS (SELECT 1 FROM pg_constraint co WHERE co.conindid = s.indexrelid) AS backs_constraint,
       coalesce(i.indexdef, '') AS definition
FROM pg_stat_user_indexes s
JOIN pg_index ix ON ix.indexrelid = s.indexrelid
JOIN pg_class ci ON ci.oid = s.indexrelid
JOIN pg_am am ON am.oid = ci.relam
LEFT JOIN pg_indexes i
  ON i.schemaname = s.schemaname AND i.tablename = s.relname AND i.indexname = s.indexrelname
ORDER BY bytes DESC
LIMIT 200
) t;
