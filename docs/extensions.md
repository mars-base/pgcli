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
2. Build a new image without the removed extension
3. Replace the container (data preserved)
4. Update config

### View Available Extensions

```bash
pg extension available
```

Lists the 26 common extensions in the pgcli catalog.

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

Extensions not in the catalog can be installed by name directly:

```bash
pg extension install <name> -i pg01
```

pgcli will attempt to install the `postgresql-18-<name>` package. If the package is not available in the Pigsty repo, an error is returned.

Full catalog: https://pigsty.io/ext/

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
install pgmq: install postgresql-18-pgmq: exit status 100
```

Cause: Package not found in the Pigsty repository.

Resolution:
- Verify the extension name: `pg extension available`
- Check the Pigsty catalog: https://pigsty.io/ext/
- Install manually: `pg exec bash -c "apt-get install -y postgresql-18-<pkg>"`

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

- **Image size**: Each extension adds 10-50MB to the image, but Pigsty packages are optimized
- **Build time**: First extension install takes 1-3 minutes (download + build); subsequent installs are near-instant (cache hits)
- **Replica behavior**: Replicas can install extensions, but `CREATE EXTENSION` will be rejected (read-only). Install on the primary; replicas sync via physical replication
- **Extension upgrades**: `ALTER EXTENSION ... UPDATE TO ...` is not yet supported; run manually via `pg exec`
