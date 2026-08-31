# PostgreSQL Extensions

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

Extensions requiring `shared_preload_libraries` (e.g., `pg_stat_statements`, `pg_cron`) trigger an automatic PostgreSQL restart.

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
