---
title: "KV Cache"
description: "PostgreSQL KV cache functions for lightweight Redis replacement"
weight: 87
icon: fa-solid fa-database
menus:
  main:
    identifier: docs-kv
    parent: docs
    weight: 87
    params:
      icon: fa-solid fa-database
cascade:
  type: docs
  footer_style: slim
---

`pg kv` deploys a set of KV-cache functions to a PostgreSQL instance, turning it into a lightweight Redis alternative for pure key-value caching. It is not a full Redis replacement — no counters, no sorted sets, no pub/sub — just fast, atomic, TTL-based key-value storage with automatic expiration.

Two variants are available: **BYTEA** (binary values, generic caching) and **JSONB** (structured JSON with field-level updates and containment queries). Both can be deployed together or separately.

## When to use it

- **Session storage** — store user sessions, OAuth tokens, or API keys with automatic expiration. Simpler than a Redis sidecar when your app already talks to PostgreSQL.
- **Configuration cache** — cache application settings or feature flags that change infrequently. JSONB variant supports partial field updates without rewriting the whole object.
- **Rate limiting** — track request counts or timestamps per user/IP. UPSERT atomicity prevents race conditions under concurrent writes.
- **Temporary data** — job queues, lock tokens, one-time-use codes. UNLOGGED TABLE means no WAL overhead; crash data loss is acceptable for ephemeral cache.
- **Prototyping** — avoid spinning up Redis for a side project. One command (`pg kv -i myinst --database myapp`) and you have a working cache.

## When NOT to use it

- **High throughput** (>500 QPS sustained) — use Redis or Memcached. PostgreSQL is not optimized for millions of tiny KV operations per second.
- **Sorted sets, counters, pub/sub** — these are Redis features PostgreSQL does not replicate. Use Redis or application-level logic.
- **Large values** (>1 MB per key) — BYTEA storage works but is inefficient. Use object storage (S3/MinIO) or filesystem.
- **Persistence required** — UNLOGGED TABLE means data is lost on crash or unclean shutdown. If cache loss is unacceptable, use a regular table or Redis with AOF.

## Usage

```bash
# Deploy both BYTEA and JSONB variants (default)
pg kv -i myinst --database myapp

# Deploy only BYTEA (generic binary values)
pg kv -i myinst --database myapp --type bytea

# Deploy only JSONB (structured JSON with field updates)
pg kv -i myinst --database myapp --type json

# Target a remote database via DSN
pg kv --dsn postgres://user:pass@host:5432/otherdb --database myapp

# Preview SQL without executing
pg kv -i myinst --database myapp --dry-run
```

The `--database` flag is required — pgcli does not auto-create databases, so the target must already exist. The command is idempotent: running it multiple times is safe (functions use `CREATE OR REPLACE`, tables use `IF NOT EXISTS`).

## BYTEA variant

Generic binary key-value cache. Values are stored as `BYTEA` (arbitrary binary data). Use for strings, serialized objects, or raw bytes.

### Tables

```sql
CREATE UNLOGGED TABLE kv_cache (
    key TEXT PRIMARY KEY,
    value BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
```

### Functions

| Function | Signature | Returns | Description |
|----------|-----------|---------|-------------|
| `kv_set` | `(p_key TEXT, p_value BYTEA, p_ttl_seconds INT DEFAULT 300)` | `VOID` | Write cache with UPSERT atomicity. Default TTL 300s. |
| `kv_set_with_result` | `(p_key TEXT, p_value BYTEA, p_ttl_seconds INT DEFAULT 300)` | `BOOLEAN` | Same as `kv_set`, returns `TRUE` on success, `FALSE` on error. |
| `kv_get` | `(p_key TEXT)` | `BYTEA` | Read cache (auto-filters expired). Returns `NULL` if not found or expired. |
| `kv_get_text` | `(p_key TEXT)` | `TEXT` | Read cache and convert to text (`convert_from` UTF-8). Returns `NULL` if not found. |
| `kv_del` | `(p_key TEXT)` | `VOID` | Delete cache entry. |
| `kv_cleanup_batch` | `(max_rows INT DEFAULT 1000)` | `INT` | Delete up to `max_rows` expired entries. Returns actual count deleted. |

### Examples

```sql
-- Write a cache entry (text converted to bytea)
SELECT kv_set('session:abc123', 'user_id=42'::bytea, 3600);

-- Read as text
SELECT kv_get_text('session:abc123');
-- Returns: user_id=42

-- Read as raw bytea (hex encoding)
SELECT kv_get('session:abc123');
-- Returns: \x757365725f69643d3432

-- Check if key exists (returns NULL if expired or missing)
SELECT kv_get('session:xyz') IS NOT NULL;

-- Delete a key
SELECT kv_del('session:abc123');

-- Cleanup expired entries (run periodically via cron or application)
SELECT kv_cleanup_batch(1000);
```

## JSONB variant

Structured JSON cache with field-level updates and containment queries. Values are stored as `JSONB` with a GIN index for efficient `@>` queries.

### Tables

```sql
CREATE UNLOGGED TABLE kv_cache_json (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_kv_cache_json_gin ON kv_cache_json USING GIN (value);
```

The GIN index serves user-written ad-hoc queries (`WHERE value @> '{"status":"active"}'`). Function APIs use primary key lookups, so the index is not consulted for `json_get`/`json_set`.

### Functions

| Function | Signature | Returns | Description |
|----------|-----------|---------|-------------|
| `json_set` | `(p_key TEXT, p_value JSONB, p_ttl_seconds INT DEFAULT 300)` | `VOID` | Write JSON cache with UPSERT atomicity. Default TTL 300s. |
| `json_set_with_result` | `(p_key TEXT, p_value JSONB, p_ttl_seconds INT DEFAULT 300)` | `BOOLEAN` | Same as `json_set`, returns `TRUE` on success. |
| `json_get` | `(p_key TEXT)` | `JSONB` | Read entire JSON object. Returns `NULL` if not found or expired. |
| `json_get_field` | `(p_key TEXT, p_field TEXT)` | `TEXT` | Read specified field as text (`->>` operator). Returns `NULL` if key or field missing. |
| `json_get_field_json` | `(p_key TEXT, p_field TEXT)` | `JSONB` | Read specified field as JSONB (`->` operator). Returns `NULL` if key or field missing. |
| `json_contains` | `(p_key TEXT, p_query JSONB)` | `BOOLEAN` | Check if cached JSON contains the query subset (`@>` operator). Returns `FALSE` if key missing. |
| `json_update_field` | `(p_key TEXT, p_field TEXT, p_new_value TEXT, p_ttl_seconds INT DEFAULT NULL)` | `BOOLEAN` | Partial update of specified field. Smart type conversion (numbers/booleans/arrays/objects/strings). `p_ttl_seconds NULL` preserves original expiration; non-NULL resets it. `p_new_value SQL NULL` returns `FALSE` (prevents accidental field deletion). Returns `FALSE` if key missing. |
| `json_del` | `(p_key TEXT)` | `VOID` | Delete JSON cache entry. |
| `json_cleanup_batch` | `(max_rows INT DEFAULT 1000)` | `INT` | Delete up to `max_rows` expired entries. Returns actual count deleted. |

### Examples

```sql
-- Write a JSON object
SELECT json_set('user:42', '{"name":"Alice","age":30,"roles":["admin","user"]}'::jsonb, 3600);

-- Read a field as text
SELECT json_get_field('user:42', 'name');
-- Returns: Alice

-- Read a field as JSONB (preserves type)
SELECT json_get_field_json('user:42', 'age');
-- Returns: 30 (as integer)

-- Read the whole object
SELECT json_get('user:42');
-- Returns: {"age": 30, "name": "Alice", "roles": ["admin", "user"]}

-- Check if object contains a subset
SELECT json_contains('user:42', '{"age":30}'::jsonb);
-- Returns: t

-- Update a single field (preserves other fields, smart type conversion)
SELECT json_update_field('user:42', 'age', '31');
SELECT json_get_field('user:42', 'age');
-- Returns: 31 (still an integer, not a string)

-- Add a new field
SELECT json_update_field('user:42', 'email', 'alice@example.com');

-- Update field and reset TTL
SELECT json_update_field('user:42', 'age', '32', 7200);

-- Update field without changing expiration
SELECT json_update_field('user:42', 'age', '33', NULL);

-- Delete a key
SELECT json_del('user:42');

-- Cleanup expired entries
SELECT json_cleanup_batch(1000);
```

## Concurrency and atomicity

Both variants use `ON CONFLICT` (UPSERT) for atomic writes. Concurrent `kv_set` calls on the same key do not race — one wins, the other overwrites. No locks are held beyond the single row.

Read operations (`kv_get`, `json_get`) are side-effect free and never block. Expired entries are filtered by `WHERE expires_at > NOW()` — they are not physically deleted until `kv_cleanup_batch` runs.

## Expiration and cleanup

Entries expire based on TTL (default 300 seconds). Expired entries are **not automatically deleted** — they remain in the table until `kv_cleanup_batch` or `json_cleanup_batch` is called.

**Recommended cleanup strategies:**

1. **Application-level cron** — call `kv_cleanup_batch(1000)` every 5-10 minutes from your app or a cron job.
2. **pg_cron extension** — if installed, schedule `SELECT kv_cleanup_batch(1000)` via `cron.schedule`.
3. **Lazy cleanup** — call cleanup after every N writes (e.g., `if (rand() < 0.01) kv_cleanup_batch(1000)`).

The `max_rows` parameter bounds each cleanup to prevent long-running transactions. With 1000 rows per batch, cleanup takes <10ms even with millions of expired entries.

## Limitations

- **No persistence** — UNLOGGED TABLE means data is lost on crash. Acceptable for cache, unacceptable for primary data.
- **No replication** — UNLOGGED TABLEs are not replicated by logical replication (they are replicated by physical streaming replication, but only the table structure, not the data).
- **No partitioning** — all keys are in a single table. For very large caches (>10M keys), consider sharding by key prefix or using Redis.
- **No TTL indexing** — expired entries are not indexed separately. Cleanup scans `expires_at < NOW()` with a regular B-tree index.

## Verification

After deployment, verify the functions are present:

```bash
# Check function count (should be 15 for --type all)
pg exec -i myinst -c "SELECT COUNT(*) FROM pg_proc WHERE proname LIKE 'kv_%' OR proname LIKE 'json_%'"

# List all KV functions
pg exec -i myinst -c "SELECT proname FROM pg_proc WHERE proname LIKE 'kv_%' OR proname LIKE 'json_%' ORDER BY proname"
```

Expected output for `--type all`:

```
         proname
-------------------------
 json_cleanup_batch
 json_contains
 json_del
 json_get
 json_get_field
 json_get_field_json
 json_set
 json_set_with_result
 json_update_field
 kv_cleanup_batch
 kv_del
 kv_get
 kv_get_text
 kv_set
 kv_set_with_result
(15 rows)
```

## Testing

A test script is provided in `scripts/test-kv-cache.sh`. It covers all 15 functions (6 BYTEA + 9 JSONB), including edge cases (NULL handling, TTL preservation, expired entry cleanup).

```bash
# Run tests against a local instance
bash scripts/test-kv-cache.sh myinst

# Run tests against the default instance
bash scripts/test-kv-cache.sh
```

The script exits with code 0 if all tests pass, 1 if any fail. Use it in CI or after upgrades to confirm the functions work as expected.

## Uninstallation

There is no `pg kv remove` command. Functions and tables remain in the database until manually dropped:

```sql
-- Remove BYTEA variant
DROP TABLE IF EXISTS kv_cache CASCADE;

-- Remove JSONB variant
DROP TABLE IF EXISTS kv_cache_json CASCADE;
```

The `CASCADE` clause removes dependent functions automatically.
