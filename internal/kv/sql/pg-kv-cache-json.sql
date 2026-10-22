-- PostgreSQL JSONB cache
-- Lightweight Redis alternative for JSON KV caching (low-frequency reads/writes)
--
-- JSONB type + GIN index: supports @>, ->, ->> and rich queries
-- Function API uses primary key lookups then in-memory operations; GIN index
-- only serves user-written WHERE value @> ... full-table queries
--
-- Also based on UNLOGGED TABLE (data lost on crash, cache is rebuildable)

CREATE UNLOGGED TABLE IF NOT EXISTS kv_cache_json (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_kv_cache_json_expires ON kv_cache_json (expires_at);
CREATE INDEX IF NOT EXISTS idx_kv_cache_json_gin ON kv_cache_json USING GIN (value);


-- json_set: write JSON cache, UPSERT atomic operation, default 300s TTL
CREATE OR REPLACE FUNCTION json_set(
    p_key TEXT,
    p_value JSONB,
    p_ttl_seconds INT DEFAULT 300
) RETURNS VOID AS $$
BEGIN
    INSERT INTO kv_cache_json (key, value, expires_at)
    VALUES (p_key, p_value, NOW() + INTERVAL '1 second' * p_ttl_seconds)
    ON CONFLICT (key) DO UPDATE
    SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at, created_at = NOW();
END;
$$ LANGUAGE plpgsql;

-- json_set_with_result: same as above, returns BOOLEAN
CREATE OR REPLACE FUNCTION json_set_with_result(
    p_key TEXT,
    p_value JSONB,
    p_ttl_seconds INT DEFAULT 300
) RETURNS BOOLEAN AS $$
BEGIN
    INSERT INTO kv_cache_json (key, value, expires_at)
    VALUES (p_key, p_value, NOW() + INTERVAL '1 second' * p_ttl_seconds)
    ON CONFLICT (key) DO UPDATE
    SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at, created_at = NOW();
    RETURN TRUE;
EXCEPTION
    WHEN OTHERS THEN
        RETURN FALSE;
END;
$$ LANGUAGE plpgsql;

-- json_get: read entire JSON object
CREATE OR REPLACE FUNCTION json_get(p_key TEXT)
RETURNS JSONB AS $$
DECLARE
    result JSONB;
BEGIN
    SELECT value INTO result
    FROM kv_cache_json
    WHERE key = p_key AND expires_at > NOW();
    RETURN result;
END;
$$ LANGUAGE plpgsql;

-- json_get_field: read specified field as text (->>)
CREATE OR REPLACE FUNCTION json_get_field(p_key TEXT, p_field TEXT)
RETURNS TEXT AS $$
DECLARE
    result JSONB;
BEGIN
    SELECT value INTO result
    FROM kv_cache_json
    WHERE key = p_key AND expires_at > NOW();

    IF result IS NOT NULL THEN
        RETURN result->>p_field;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- json_get_field_json: read specified field as JSONB (->)
CREATE OR REPLACE FUNCTION json_get_field_json(p_key TEXT, p_field TEXT)
RETURNS JSONB AS $$
DECLARE
    result JSONB;
BEGIN
    SELECT value INTO result
    FROM kv_cache_json
    WHERE key = p_key AND expires_at > NOW();

    IF result IS NOT NULL THEN
        RETURN result->p_field;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- json_contains: check if cache contains specified JSON (@> operator)
CREATE OR REPLACE FUNCTION json_contains(p_key TEXT, p_query JSONB)
RETURNS BOOLEAN AS $$
DECLARE
    result JSONB;
BEGIN
    SELECT value INTO result
    FROM kv_cache_json
    WHERE key = p_key AND expires_at > NOW();

    IF result IS NOT NULL THEN
        RETURN result @> p_query;
    END IF;
    RETURN FALSE;
END;
$$ LANGUAGE plpgsql;

-- json_update_field: partial update of specified field
--   Smart type conversion (numbers/booleans/arrays/objects/strings)
--   Auto-creates field if it does not exist
--   p_ttl_seconds NULL preserves original expiration; non-NULL resets it
--   p_new_value SQL NULL returns FALSE (prevents accidental field deletion)
CREATE OR REPLACE FUNCTION json_update_field(
    p_key TEXT,
    p_field TEXT,
    p_new_value TEXT,
    p_ttl_seconds INT DEFAULT NULL
) RETURNS BOOLEAN AS $$
DECLARE
    current_value JSONB;
    new_json_value JSONB;
BEGIN
    -- NULL input triggers jsonb_set field deletion semantics, reject it
    IF p_new_value IS NULL THEN
        RETURN FALSE;
    END IF;

    SELECT value INTO current_value
    FROM kv_cache_json
    WHERE key = p_key AND expires_at > NOW();

    IF current_value IS NULL THEN
        RETURN FALSE;
    END IF;

    -- Smart conversion: try parsing as JSON, fallback to string
    BEGIN
        new_json_value := p_new_value::jsonb;
    EXCEPTION
        WHEN invalid_text_representation THEN
            new_json_value := to_jsonb(p_new_value);
    END;

    UPDATE kv_cache_json
    SET value = jsonb_set(current_value, ARRAY[p_field], new_json_value),
        expires_at = CASE
            WHEN p_ttl_seconds IS NULL THEN kv_cache_json.expires_at
            ELSE NOW() + INTERVAL '1 second' * p_ttl_seconds
        END
    WHERE key = p_key;

    RETURN TRUE;
END;
$$ LANGUAGE plpgsql;


-- json_del: delete specified JSON cache entry
CREATE OR REPLACE FUNCTION json_del(p_key TEXT)
RETURNS VOID AS $$
BEGIN
    DELETE FROM kv_cache_json WHERE key = p_key;
END;
$$ LANGUAGE plpgsql;

-- json_cleanup_batch: batch cleanup of expired data
CREATE OR REPLACE FUNCTION json_cleanup_batch(max_rows INT DEFAULT 1000)
RETURNS INT AS $$
DECLARE
    deleted_count INT;
BEGIN
    max_rows := COALESCE(max_rows, 1000);
    WITH expired AS (
        SELECT key
        FROM kv_cache_json
        WHERE expires_at < NOW()
        ORDER BY expires_at
        LIMIT max_rows
    )
    DELETE FROM kv_cache_json
    WHERE key IN (SELECT key FROM expired);

    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN deleted_count;
END;
$$ LANGUAGE plpgsql;
