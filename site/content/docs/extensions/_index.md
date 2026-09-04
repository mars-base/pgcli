---
title: "PostgreSQL Extensions"
description: "PostgreSQL Extensions guide for pgcli"
weight: 60
icon: fa-solid fa-puzzle-piece
menus:
  main:
    identifier: docs-extensions
    parent: docs
    weight: 60
    params:
      icon: fa-solid fa-puzzle-piece
cascade:
  type: docs
  footer_style: slim
---


pgcli supports installing and managing PostgreSQL extensions from the [Pigsty DEB repository](https://pigsty.io/ext/).

## How It Works

Extensions are baked into a derived container image:

1. **`pg extension install`** builds a new image (based on the current image + Pigsty repo + extension packages)
2. Stops and removes the old container
3. Recreates the container from the new image (host data volumes are preserved)
4. Updates `image_tag` in the config file

Benefits of this approach:
- Extensions survive container rebuilds (baked into the image layer)
- `pg start` does not need to run `apt-get install` on every boot
- Extension files are persistent and decoupled from the container lifecycle

## Commands

### Install Extensions

```bash
# Install a single extension
pg extension install pg_stat_statements

# Install multiple extensions (single image build)
pg extension install pgmq uuid-ossp pg_stat_statements

# Target a specific instance
pg extension install pg_stat_statements -i pg01
```

**Restart confirmation:** Extensions requiring `shared_preload_libraries` (e.g., `pg_stat_statements`, `pg_cron`) need a PostgreSQL restart. By default, you will be prompted for confirmation:

```bash
# Interactive confirmation (default)
pg extension install pg_stat_statements
# Output:
# Installing extensions that require shared_preload_libraries will cause a PostgreSQL restart.
# Extensions to be installed: [pg_stat_statements]
# This will cause a brief interruption to database connections.
# Restart PostgreSQL now? [y/N]:

# Skip confirmation and restart automatically
pg extension install pg_stat_statements --auto-restart
```

If you decline the restart, you can apply the changes later:
```bash
pg stop -i pg01
pg start -i pg01
```

### List Installed Extensions

```bash
pg extension list -i pg01
```

Example output:
```
Installed extensions in "pg01":
  pg_stat_statements (managed)
  uuid-ossp (managed)
  plpgsql (unmanaged)
```

- **managed**: tracked by pgcli (recorded in config, included in image)
- **unmanaged**: manually installed extensions (not tracked in config)

### Remove Extensions

```bash
pg extension remove pgmq -i pg01
```

Workflow:
1. `DROP EXTENSION IF EXISTS pgmq`
2. Update config and `shared_preload_libraries`
3. **No image rebuild** — the `-ext` image is shared across instances and packages are never uninstalled

**Restart confirmation:** If removing extensions that require `shared_preload_libraries` (e.g., `pg_stat_statements`, `pg_cron`), you will be prompted for confirmation before restarting:

```bash
# Interactive confirmation (default)
pg extension remove pg_stat_statements -i pg01
# Output:
# Removing extensions that require shared_preload_libraries will cause a PostgreSQL restart.
# Extensions to be removed: [pg_stat_statements]
# This will cause a brief interruption to database connections.
# Restart PostgreSQL now? [y/N]:

# Skip confirmation and restart automatically
pg extension remove pg_stat_statements -i pg01 --auto-restart
```

If you decline the restart, you can apply the changes later:
```bash
pg stop -i pg01
pg start -i pg01
```

### View Available Extensions

```bash
pg extension available
```

Lists all 440 known extensions:
- **45 builtin** (contrib, already in the base image — no image build needed)
- **395 Pigsty catalog** (from Pigsty DEB repo, requires image build)

## Built-in Extension Catalog

### Requires shared_preload_libraries (restart on install)

| Extension | Description |
|-----------|-------------|
| `pg_stat_statements` | SQL performance analysis |
| `pg_cron` | Scheduled job execution |
| `pg_hint_plan` | Query hints |
| `pg_stat_monitor` | Advanced performance monitoring |
| `pg_qualstats` | Query predicate statistics |
| `pg_stat_kcache` | Kernel-level performance stats |
| `pg_wait_sampling` | Wait event sampling |
| `pg_track_settings` | Configuration change tracking |
| `timescaledb` | Time-series database extension |

### No shared_preload_libraries (no restart needed)

| Extension | Description |
|-----------|-------------|
| `uuid-ossp` | UUID generation functions |
| `pgmq` | Lightweight message queue |
| `hstore` | Key-value pair storage |
| `pgcrypto` | Cryptographic functions |
| `tablefunc` | Crosstab functions |
| `btree_gist` | B-tree GiST index support |
| `btree_gin` | B-tree GIN index support |
| `pg_trgm` | Trigram similarity matching |
| `unaccent` | Accent removal functions |
| `fuzzystrmatch` | Fuzzy string matching |
| `intarray` | Integer array operations |
| `isn` | ISBN/ISSN/EAN standard number types |
| `pg_repack` | Online table reorganization |
| `pg_squeeze` | Table space reclamation |
| `pg_partman` | Partition management |
| `pgvector` | Vector similarity search |
| `postgis` | Geospatial data support |

## Popular Extensions

The following 20 extensions are the most commonly used in PostgreSQL, tested on **PG 18** with pgcli.

### Performance & Monitoring

| Extension | Install | Description |
|-----------|---------|-------------|
| `pg_stat_statements` | `pg extension install pg_stat_statements` | SQL performance analysis, tracks query execution stats. **Must-have for production.** |
| `pg_repack` | `pg extension install pg_repack` | Online table reorganization without exclusive locks. Reclaims bloat from update-heavy tables. |
| `pg_prewarm` | `pg extension install pg_prewarm` | Preload table data into shared buffers after restart. |

```bash
# Install performance extensions at once
pg extension install pg_stat_statements pg_repack pg_prewarm --auto-restart

# Find top 5 slow queries
pg exec "SELECT query, calls, total_exec_time::numeric(10,2) AS ms FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 5;"

# Repack a bloated table
pg exec -- pg_repack -U admin -d default_db -t my_table --no-superuser-check
```

### Data Types & Identifiers

| Extension | Install | Description |
|-----------|---------|-------------|
| `uuid-ossp` | builtin | UUID generation (v1/v3/v4/v5). PG 18 also has built-in `gen_random_uuid()` and `uuidv7()`. |
| `hstore` | builtin | Key-value pair storage in a single column. |
| `citext` | builtin | Case-insensitive text type. `Foo` = `foo`. |

```bash
# Generate UUIDs
pg exec "SELECT uuid_generate_v4();"          # random UUID
pg exec "SELECT uuidv7();"                    # time-ordered UUID (PG 18 builtin)

# Key-value storage
pg exec "SELECT 'theme => dark, lang => en'::hstore -> 'theme';"  -- returns 'dark'

# Case-insensitive matching
pg exec "CREATE TABLE users (email citext); INSERT INTO users VALUES ('Alice@Example.COM'); SELECT * FROM users WHERE email = 'alice@example.com';"
```

### Search & Text

| Extension | Install | Description |
|-----------|---------|-------------|
| `pg_trgm` | builtin | Trigram similarity matching for fuzzy search and autocomplete. |
| `unaccent` | builtin | Strip accents from characters for flexible international text search. |

```bash
# Fuzzy search: find names similar to "John"
pg exec "SELECT name, similarity(name, 'John') FROM users ORDER BY similarity(name, 'John') DESC LIMIT 5;"

# Remove accents
pg exec "SELECT unaccent('Crème Brûlée');"  -- returns 'Creme Brulee'
```

### Security & Encryption

| Extension | Install | Description |
|-----------|---------|-------------|
| `pgcrypto` | builtin | Hashing (bcrypt/sha256), encryption/decryption, random value generation. |
| `pgaudit` | `pg extension install pgaudit` | Audit logging for sessions and objects. Required for compliance (GDPR, HIPAA, SOX). |

```bash
# Hash a password with bcrypt
pg exec "SELECT crypt('my_password', gen_salt('bf'));"

# Encrypt and decrypt
pg exec "SELECT pgp_sym_decrypt(pgp_sym_encrypt('secret', 'key'), 'key');"  -- returns 'secret'

# Enable audit logging
pg exec "SET pgaudit.log = 'read, ddl';"
```

### Geospatial

| Extension | Install | Description |
|-----------|---------|-------------|
| `postgis` | `pg extension install postgis` | Full-featured spatial database: geometry types, spatial indexes, distance/area calculations. |

```bash
pg extension install postgis --auto-restart

# Find locations within 5km
pg exec "SELECT name FROM places WHERE ST_DWithin(geom, ST_MakePoint(-122.4, 37.8)::geography, 5000);"

# Calculate distance between two points (in meters)
pg exec "SELECT ST_Distance(a::geography, b::geography) FROM (SELECT ST_MakePoint(-74.006, 40.7128) AS a, ST_MakePoint(-0.1276, 51.5074) AS b) t;"
```

### AI & Vector Search

| Extension | Install | Description |
|-----------|---------|-------------|
| `pgvector` | `pg extension install vector` | Store and search embedding vectors. De facto standard for AI applications. |

```bash
pg extension install vector --auto-restart

# Create a table with vector column
pg exec "CREATE TABLE items (id serial PRIMARY KEY, embedding vector(3));"
pg exec "INSERT INTO items (embedding) VALUES ('[1,2,3]'), ('[4,5,6]'), ('[7,8,9]');"

# Find nearest neighbors
pg exec "SELECT id FROM items ORDER BY embedding <-> '[3,1,2]' LIMIT 5;"
```

### Time Series

| Extension | Install | Description |
|-----------|---------|-------------|
| `timescaledb` | `pg extension install timescaledb` | Optimized time-series storage with hypertables and time_bucket aggregation. |

```bash
pg extension install timescaledb --auto-restart

# Create a hypertable
pg exec "CREATE TABLE sensor (time timestamptz NOT NULL, value float); SELECT create_hypertable('sensor', 'time');"

# Time-bucket aggregation
pg exec "SELECT time_bucket('1 hour', time) AS bucket, avg(value) FROM sensor GROUP BY bucket ORDER BY bucket;"
```

### Scheduling & Distributed

| Extension | Install | Description |
|-----------|---------|-------------|
| `pg_cron` | `pg extension install pg_cron` | Cron-based job scheduler inside the database. Auto-configured by pgcli. |
| `citus` | `pg extension install citus` | Distributed PostgreSQL with sharding and parallel queries. |

```bash
# Schedule a daily cleanup job (runs at midnight)
pg exec "SELECT cron.schedule('cleanup', '0 0 * * *', 'DELETE FROM logs WHERE created_at < now() - interval ''30 days''');"

# List scheduled jobs
pg exec "SELECT jobid, schedule, command FROM cron.job;"

# Unschedule a job
pg exec "SELECT cron.unschedule('cleanup');"

# Create a distributed table (citus)
pg exec "SELECT create_distributed_table('events', 'tenant_id');"
```

### Foreign Data

| Extension | Install | Description |
|-----------|---------|-------------|
| `postgres_fdw` | builtin | Query external PostgreSQL servers as local tables. |

```bash
# Create a foreign server and query remote data
pg exec "CREATE SERVER remote FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host '10.0.0.2', port '5432', dbname 'analytics');"
pg exec "CREATE USER MAPPING FOR current_user SERVER remote OPTIONS (user 'reader', password 'secret');"
pg exec "IMPORT FOREIGN SCHEMA public FROM SERVER remote INTO remote_schema;"
```

### Compatibility Notes

| Extension | PG 18 Status | Notes |
|-----------|-------------|-------|
| `pgml` | Not available | Only supports PG 14-17 (no PG 18 package) |
| `uuid-ossp` | Partially needed | PG 18 has built-in `gen_random_uuid()` (v4) and `uuidv7()` (time-ordered) |

## Extensions Outside the Catalog

Only extensions in the catalog (builtin + Pigsty) can be installed via `pg extension install`. Unknown extension names are rejected before the build starts:

```
  [X] Unknown extension(s): [nonexistent_ext]

      These extensions are not in the Pigsty catalog or builtin contrib list.
      Check available extensions: pg extension available
      Full Pigsty catalog: https://pigsty.cc/ext/list/
```

Full catalog: https://pigsty.cc/ext/list/

## Configuration

After installing extensions, the config is updated:

```yaml
instances:
  pg01:
    extensions:
      - pg_stat_statements
      - uuid-ossp
      - pgmq
    podman:
      image_tag: ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-ext
```

The `image_tag` points to the derived image containing all installed extensions.

## Shared Preload Libraries

Extensions requiring `shared_preload_libraries` are automatically configured in `postgresql.conf`:

```
# === pgcli extensions (managed — do not edit) ===
shared_preload_libraries = 'pg_stat_statements,pg_cron'
# === end pgcli extensions ===
```

This is a postmaster-level parameter; PostgreSQL must be restarted after changes.

## Troubleshooting

### Extension Install Failure

```
  [X] Unknown extension(s): [nonexistent_ext]
```

Cause: Extension name is not in the builtin contrib list or Pigsty catalog.

Resolution:
- Verify the extension name: `pg extension available`
- Check the Pigsty catalog: https://pigsty.cc/ext/list/
- Note the exact SQL extension name (e.g., `vector` not `pgvector`)

### CREATE EXTENSION Failure

```
ERROR: extension "pgmq" already exists
```

The extension is installed but not tracked in config. You can safely ignore this, or manually add it to the config:

```yaml
extensions:
  - pgmq
```

### Shared Preload Library Conflict

If `shared_preload_libraries` was manually edited in `postgresql.conf`, pgcli's sentinel block will overwrite it.

Resolution: Remove the manual configuration and let pgcli manage it.

## Notes

- **Extension count**: 45 builtin (contrib) + 395 Pigsty catalog = 440 total known extensions
- **Image size**: Each extension adds 10-50MB to the image, but Pigsty packages are optimized
- **Build time**: First extension install takes 1-3 minutes (download + build); subsequent installs are faster (cache hits)
- **Replica behavior**: Replicas can install extensions, but `CREATE EXTENSION` will be rejected (read-only). Install on the primary; replicas sync via physical replication
- **Extension upgrades**: `ALTER EXTENSION ... UPDATE TO ...` is not yet supported; run manually via `pg exec`
