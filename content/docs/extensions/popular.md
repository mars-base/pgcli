---
title: "Popular Extensions"
description: "Usage examples for the most commonly used PostgreSQL extensions"
weight: 20
---


Usage examples for the most commonly used PostgreSQL extensions, tested on **PG 18** with pgcli.

## Performance & Monitoring

| Extension | Install | Description |
|-----------|---------|-------------|
| `pg_stat_statements` | `pg extension install pg_stat_statements` | SQL performance analysis. **Must-have for production.** |
| `pg_repack` | `pg extension install pg_repack` | Online table reorganization. Reclaims bloat. |
| `pg_prewarm` | `pg extension install pg_prewarm` | Preload table data after restart. |

```bash
# Install all at once
pg extension install pg_stat_statements pg_repack pg_prewarm --auto-restart

# Find top 5 slow queries
pg exec "SELECT query, calls, total_exec_time::numeric(10,2) AS ms
         FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 5;"

# Repack a bloated table
pg exec -- pg_repack -U admin -d default_db -t my_table --no-superuser-check

# Prewarm a table
pg exec "SELECT pg_prewarm('my_table');"
```

## Data Types & Identifiers

| Extension | Install | Description |
|-----------|---------|-------------|
| `uuid-ossp` | builtin | UUID generation (v1/v3/v5). **PG 18 has built-in `gen_random_uuid()` (v4) and `uuidv7()` (v7) — no extension needed.** |
| `hstore` | builtin | Key-value pair storage. |
| `citext` | builtin | Case-insensitive text. `Foo` = `foo`. |

```bash
# UUID generation (PG 18 built-in — no extension needed)
pg exec "SELECT gen_random_uuid();"  # v4 random UUID (built-in since PG 13)
pg exec "SELECT uuidv7();"           # v7 time-ordered UUID (new in PG 18)

# uuid-ossp extension — only needed for v1/v3/v5 UUIDs
pg exec "CREATE EXTENSION IF NOT EXISTS \"uuid-ossp\";"
pg exec "SELECT uuid_generate_v1();"  # v1 timestamp + MAC address
pg exec "SELECT uuid_generate_v3(uuid_ns_url(), 'https://example.com');"  # v3 MD5-based
pg exec "SELECT uuid_generate_v5(uuid_ns_url(), 'https://example.com');"  # v5 SHA-1 based

# Key-value storage
pg exec "SELECT 'theme => dark, lang => en'::hstore -> 'theme';"
# Returns: dark

# Case-insensitive matching
pg exec "CREATE TABLE users (email citext);
         INSERT INTO users VALUES ('Alice@Example.COM');
         SELECT * FROM users WHERE email = 'alice@example.com';"
```

## Search & Text

| Extension | Install | Description |
|-----------|---------|-------------|
| `pg_trgm` | builtin | Trigram similarity for fuzzy search and autocomplete. |
| `unaccent` | builtin | Strip accents for flexible international text search. |

```bash
# Fuzzy search
pg exec "SELECT name, similarity(name, 'John') FROM users
         ORDER BY similarity(name, 'John') DESC LIMIT 5;"

# Create a trigram index for fast fuzzy search
pg exec "CREATE INDEX users_name_trgm_idx ON users USING gin (name gin_trgm_ops);"

# Remove accents
pg exec "SELECT unaccent('Crème Brûlée');"  -- returns 'Creme Brulee'

# Combine for accent-insensitive fuzzy search
pg exec "SELECT name FROM users WHERE name % unaccent('Creme Brulee');"
```

## Security & Encryption

| Extension | Install | Description |
|-----------|---------|-------------|
| `pgcrypto` | builtin | Hashing, encryption/decryption, random generation. |
| `pgaudit` | `pg extension install pgaudit` | Audit logging. Required for compliance (GDPR, HIPAA, SOX). |

```bash
# Hash a password with bcrypt
pg exec "SELECT crypt('my_password', gen_salt('bf'));"

# Verify a password
pg exec "SELECT (crypt('my_password', stored_hash) = stored_hash);"

# Symmetric encryption/decryption
pg exec "SELECT pgp_sym_decrypt(pgp_sym_encrypt('secret', 'key'), 'key');"

# Enable audit logging for specific operations
pg exec "SET pgaudit.log = 'read, ddl';"
```

## Geospatial

| Extension | Install | Description |
|-----------|---------|-------------|
| `postgis` | `pg extension install postgis` | Full spatial database: geometry, spatial indexes, distance/area. |

```bash
pg extension install postgis --auto-restart

# Create a table with spatial data
pg exec "CREATE TABLE places (id serial, name text, geom geometry(Point, 4326));"
pg exec "INSERT INTO places (name, geom) VALUES
         ('San Francisco', ST_MakePoint(-122.4, 37.8)),
         ('London', ST_MakePoint(-0.1276, 51.5074));"

# Find places within 5km of a point
pg exec "SELECT name FROM places
         WHERE ST_DWithin(geom, ST_MakePoint(-122.4, 37.8)::geography, 5000);"

# Calculate distance between two cities (in meters)
pg exec "SELECT ST_Distance(
           ST_MakePoint(-74.006, 40.7128)::geography,
           ST_MakePoint(-0.1276, 51.5074)::geography);"
```

## AI & Vector Search

| Extension | Install | Description |
|-----------|---------|-------------|
| `pgvector` | `pg extension install vector` | Store and search embedding vectors. Standard for AI apps. |

```bash
pg extension install vector --auto-restart

# Create a table with vector column
pg exec "CREATE TABLE items (id serial PRIMARY KEY, embedding vector(3));"
pg exec "INSERT INTO items (embedding) VALUES ('[1,2,3]'), ('[4,5,6]'), ('[7,8,9]');"

# Find nearest neighbors (Euclidean distance)
pg exec "SELECT id FROM items ORDER BY embedding <-> '[3,1,2]' LIMIT 5;"

# Create an IVFFlat index for fast approximate search
pg exec "CREATE INDEX items_embedding_idx ON items USING ivfflat (embedding vector_l2_ops);"
```

## Time Series

| Extension | Install | Description |
|-----------|---------|-------------|
| `timescaledb` | `pg extension install timescaledb` | Optimized time-series storage with hypertables. |

```bash
pg extension install timescaledb --auto-restart

# Create a hypertable
pg exec "CREATE TABLE sensor (time timestamptz NOT NULL, value float);
         SELECT create_hypertable('sensor', 'time');"

# Time-bucket aggregation
pg exec "SELECT time_bucket('1 hour', time) AS bucket, avg(value)
         FROM sensor GROUP BY bucket ORDER BY bucket;"
```

## Scheduling & Distributed

| Extension | Install | Description |
|-----------|---------|-------------|
| `pg_cron` | `pg extension install pg_cron` | Cron-based job scheduler. Auto-configured by pgcli. |
| `citus` | `pg extension install citus` | Distributed PostgreSQL with sharding. |

```bash
pg extension install pg_cron --auto-restart

# Schedule a daily cleanup job (runs at midnight)
pg exec "SELECT cron.schedule('cleanup', '0 0 * * *',
         'DELETE FROM logs WHERE created_at < now() - interval ''30 days''');"

# List scheduled jobs
pg exec "SELECT jobid, schedule, command FROM cron.job;"

# Unschedule a job
pg exec "SELECT cron.unschedule('cleanup');"
```

## Foreign Data

| Extension | Install | Description |
|-----------|---------|-------------|
| `postgres_fdw` | builtin | Query external PostgreSQL servers as local tables. |

```bash
# Create a foreign server
pg exec "CREATE SERVER remote FOREIGN DATA WRAPPER postgres_fdw
         OPTIONS (host '10.0.0.2', port '5432', dbname 'analytics');"

# Create user mapping
pg exec "CREATE USER MAPPING FOR current_user SERVER remote
         OPTIONS (user 'reader', password 'secret');"

# Import remote schema as foreign tables
pg exec "IMPORT FOREIGN SCHEMA public FROM SERVER remote INTO remote_schema;"

# Query remote data as if it were local
pg exec "SELECT * FROM remote_schema.events LIMIT 10;"
```

## Compatibility Notes

| Extension | PG 18 Status | Notes |
|-----------|-------------|-------|
| `pgml` | Not available | Only supports PG 14-17 (no PG 18 package) |
| `uuid-ossp` | Partially needed | PG 18 built-in: `gen_random_uuid()` (v4), `uuidv7()` (v7). Extension only needed for v1/v3/v5 |
