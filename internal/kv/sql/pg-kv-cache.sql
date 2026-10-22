-- PostgreSQL KV cache (BYTEA variant)
-- Lightweight Redis alternative for pure KV caching (low-frequency reads/writes,
-- no counter/ranking/ZSET support)
--
-- Based on UNLOGGED TABLE: faster writes, data lost on crash (acceptable for cache)
-- Automatic expiration (TTL + batch cleanup)
-- Safe concurrency (UPSERT atomicity via ON CONFLICT; GET is side-effect free)
-- Resource control (each cleanup ≤1000 rows)
--
-- Notes:
-- 1. Avoid single records > 1MB
-- 2. UNLOGGED TABLE still needs VACUUM for space reclamation, but less frequently
-- 3. Suitable for < 500 QPS; use Redis for higher frequencies

CREATE UNLOGGED TABLE IF NOT EXISTS kv_cache (
    key TEXT PRIMARY KEY,
    value BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_kv_cache_expires ON kv_cache (expires_at);


-- kv_set: write cache, UPSERT atomic operation, default 300s TTL
CREATE OR REPLACE FUNCTION kv_set(
    p_key TEXT,
    p_value BYTEA,
    p_ttl_seconds INT DEFAULT 300
) RETURNS VOID AS $$
BEGIN
    INSERT INTO kv_cache (key, value, expires_at)
    VALUES (p_key, p_value, NOW() + INTERVAL '1 second' * p_ttl_seconds)
    ON CONFLICT (key) DO UPDATE
    SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at;
END;
$$ LANGUAGE plpgsql;

-- kv_set_with_result: same as above, returns BOOLEAN
CREATE OR REPLACE FUNCTION kv_set_with_result(
    p_key TEXT,
    p_value BYTEA,
    p_ttl_seconds INT DEFAULT 300
) RETURNS BOOLEAN AS $$
BEGIN
    INSERT INTO kv_cache (key, value, expires_at)
    VALUES (p_key, p_value, NOW() + INTERVAL '1 second' * p_ttl_seconds)
    ON CONFLICT (key) DO UPDATE
    SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at;
    RETURN TRUE;
EXCEPTION
    WHEN OTHERS THEN
        RETURN FALSE;
END;
$$ LANGUAGE plpgsql;


-- kv_get: read cache (auto-filters expired), returns NULL if not found or expired
CREATE OR REPLACE FUNCTION kv_get(p_key TEXT)
RETURNS BYTEA AS $$
DECLARE
    result BYTEA;
BEGIN
    SELECT value INTO result
    FROM kv_cache
    WHERE key = p_key AND expires_at > NOW();
    RETURN result;
END;
$$ LANGUAGE plpgsql;

-- kv_get_text: read cache and convert to text (convert_from UTF8)
CREATE OR REPLACE FUNCTION kv_get_text(p_key TEXT)
RETURNS TEXT AS $$
DECLARE
    result BYTEA;
BEGIN
    SELECT value INTO result
    FROM kv_cache
    WHERE key = p_key AND expires_at > NOW();

    IF result IS NOT NULL THEN
        RETURN convert_from(result, 'UTF8');
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- kv_del: delete specified cache entry
CREATE OR REPLACE FUNCTION kv_del(p_key TEXT)
RETURNS VOID AS $$
BEGIN
    DELETE FROM kv_cache WHERE key = p_key;
END;
$$ LANGUAGE plpgsql;


-- kv_cleanup_batch: batch cleanup of expired data (≤ max_rows), returns actual count deleted
CREATE OR REPLACE FUNCTION kv_cleanup_batch(max_rows INT DEFAULT 1000)
RETURNS INT AS $$
DECLARE
    deleted_count INT;
BEGIN
    max_rows := COALESCE(max_rows, 1000);
    WITH expired AS (
        SELECT key
        FROM kv_cache
        WHERE expires_at < NOW()
        ORDER BY expires_at
        LIMIT max_rows
    )
    DELETE FROM kv_cache
    WHERE key IN (SELECT key FROM expired);

    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN deleted_count;
END;
$$ LANGUAGE plpgsql;
