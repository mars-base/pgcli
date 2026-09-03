---
title: "Data Import/Export"
description: "Data Import/Export guide for pgcli"
weight: 80
icon: fa-solid fa-file-arrow-down
menus:
  main:
    identifier: docs-import-export
    parent: docs
    weight: 80
    params:
      icon: fa-solid fa-file-arrow-down
cascade:
  type: docs
  footer_style: slim
---


Export and import databases to dump files. Supports custom format (recommended) and plain SQL, with automatic gzip compression. Also supports piping between instances.

```bash
# Export to custom format (recommended, fastest restore)
pg export -i proj01 -o backup.dump

# Export to SQL format (human-readable)
pg export -i proj01 -o backup.sql

# Export with gzip compression (auto-detected from .gz extension)
pg export -i proj01 -o backup.dump.gz
pg export -i proj01 -o backup.sql.gz

# Export specific database
pg export -i proj01 -d mydb -o backup.dump

# Export with custom compression level (0-9)
pg export -i proj01 -o backup.sql.gz --compress=9

# Export with verbose output (show progress)
pg export -i proj01 -o backup.dump -v

# Import from custom format
pg import -i proj02 backup.dump

# Import from SQL format
pg import -i proj02 backup.sql

# Import compressed file (auto-detected from .gz extension)
pg import -i proj02 backup.dump.gz

# Import to specific database
pg import -i proj02 -d mydb backup.dump

# Import with cleanup (drop existing objects before restore)
pg import -i proj02 --clean backup.dump

# Import with verbose output
pg import -i proj02 backup.dump -v

# Pipe between instances (no temp file)
pg export -i proj01 | pg import -i proj02
pg export -i proj01 -d mydb | pg import -i proj02 -d mydb --clean

# Pipe across hosts via SSH
pg export -i proj01 | ssh user@remote "pg import -i proj02"
ssh user@remote "pg export -i proj01" | pg import -i proj02
ssh user@host1 "pg export -i proj01" | ssh user@host2 "pg import -i proj02"

# Work with remote databases via connection string (--dsn)
pg export --dsn postgres://user:pass@host:5432/mydb -o backup.dump
pg import --dsn postgres://user:pass@host:5432/mydb backup.dump --clean
pg export -i proj01 | pg import --dsn postgres://user:pass@host:5432/mydb
pg export --dsn postgres://user:pass@host1:5432/db1 | pg import --dsn postgres://user:pass@host2:5432/db2

# DSN can also be used for local instances (useful when ports differ from defaults)
pg export --dsn postgres://admin:pass@127.0.0.1:35432/mydb | pg import --dsn postgres://admin:pass@127.0.0.1:35433/mydb --clean
```

**Format comparison:**

| Feature | Custom (`.dump`) | SQL (`.sql`) |
|---------|------------------|--------------|
| Import speed | Faster (binary COPY, parallel restore) | Slower (text INSERT) |
| File size | Smaller (compressed) | Larger (plain text) |
| Human-readable | No | Yes |
| Selective restore | Yes (specific tables) | No |
| Best for | Migration, backup, large databases | Version control, CI seed data, manual editing |

**Format detection:** Uses magic bytes (content-based) with extension fallback.
- Files starting with `PGDMP` → custom format (uses `pg_restore`)
- `.sql` or `.sql.gz` → plain SQL format (uses `psql`)
- `.gz` extension or gzip magic bytes (`0x1f 0x8b`) → automatic decompression
- Extension is used as fallback if content detection fails

**Remote databases (--dsn):** Connect to any PostgreSQL instance using a connection string.
- Uses `pg_dump` and `pg_restore` from the pgcli container image (no local PostgreSQL installation needed)
- Works with local-to-remote, remote-to-local, and remote-to-remote migrations
- Supports all the same flags as local instances (`-o`, `-d`, `--clean`, `-v`, `--compress`)

**Note on existing data:** Importing into a database with existing tables will fail unless you use the `--clean` flag, which drops objects before restoring. Use `--clean` when importing into a database that already contains data.

**Use cases:**
- Migrate data between instances: `pg export -i proj01 | pg import -i proj02`
- Cross-host migration: `pg export -i proj01 | pg import --dsn postgres://user:pass@remote:5432/db`
- Share database with team: `pg export -i proj01 -o dump.dump.gz` (compressed, smaller file)
- Backup before major changes: `pg export -i proj01 -o pre-migration.sql.gz`
- CI/CD pipelines: export test data, import into fresh test databases
