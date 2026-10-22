---
title: "DuckDB"
description: "Install an embedded OLAP database as a managed host-binary addon."
weight: 61
---

[DuckDB](https://duckdb.org/) is an embedded analytical database — often called
"SQLite for analytics". It is a C++ columnar engine with vectorised execution
that queries Parquet, CSV, JSON and Arrow files directly, without a server
process. Think of it as a single-binary OLAP engine you point at your data:
`SELECT * FROM read_parquet('s3://bucket/data.parquet')` works out of the box
once the `httpfs` extension is loaded.

As a pgcli addon, DuckDB is a **host-binary** addon: there is no container and
no daemon. `pg addon install duckdb` downloads the official CLI zip from GitHub
releases (sha256-verified; a `duckdb` already on `PATH` is reused), installs
common extensions (`httpfs`, `parquet`, `json`), and records the entry in
`pg.yaml`.

> **Platform**: Linux only, amd64 only (macOS and other architectures refuse
> with a clear error). DuckDB is a standalone CLI tool — it does not run as a
> background service, does not listen on a port, and does not propagate into
> running PG instance containers.

## Why DuckDB alongside pgcli

DuckDB fills the analytical gap that PostgreSQL's row-oriented engine does not
target: ad-hoc queries over large Parquet/CSV datasets, data-lake exploration,
ETL pre-processing before loading into PostgreSQL, or quick aggregations on
files that already live in S3. pgcli already manages S3 stores (MinIO, silo,
rustfs, garage, seaweedfs) — DuckDB queries their contents directly via
`httpfs`.

## Usage

```bash
# Default: v1.5.6 LTS, install httpfs+parquet+json extensions
pg addon install duckdb

# Pin a specific version
pg addon install duckdb --version 2.0.0

# Custom name and data directory
pg addon install duckdb --name analytics --data-dir /srv/duckdb

# Skip extension installation
pg addon install duckdb --no-extensions

# Re-install with different flags (entry already exists)
pg addon install duckdb --version 2.0.0 --force
```

Once installed, launch the interactive shell or run one-shot queries:

```bash
# Interactive shell (data dir is set as the working directory)
pg addon duckdb shell

# One-shot query against a local Parquet file
pg addon duckdb shell -- -c "SELECT * FROM read_parquet('/tmp/data.parquet')"

# Query S3 directly (needs the httpfs extension, installed by default)
duckdb -c "SELECT count(*) FROM read_parquet('s3://bucket/data/*.parquet')"

# Open a persistent database file
duckdb ~/.pgcli/addon/duckdb/duckdb/analytics.duckdb
```

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `duckdb` | Instance name (config map key) |
| `--version` | `1.5.6` | Release to download (its sha256 is pinned in pgcli; `1.5.6` LTS and `2.0.0` supported) |
| `--data-dir` | `<base>/addon/duckdb/<name>` | Default database directory for `.duckdb` files |
| `--install-dir` | `/usr/local/bin` | Where the host binary goes (falls back to `~/.local/bin` when not writable — keep it on `PATH`) |
| `--no-extensions` | off | Skip installing common extensions (`httpfs`, `parquet`, `json`) |
| `--force` | off | Rebuild the config entry from the flags |

## Daily operations

```bash
pg addon duckdb status --name duckdb        # binary version, path, data dir
pg addon duckdb shell --name duckdb         # interactive DuckDB session
pg addon duckdb shell -- -c "SELECT 1"      # one-shot query (args after --)
pg addon list                               # duckdb appears under "Analytics add-ons"
pg addon start duckdb                       # no-op (CLI tool, not a daemon)
pg addon stop  duckdb                       # no-op (CLI tool, not a daemon)
pg addon remove duckdb --name duckdb        # forget config; binary and data dir stay
pg addon remove duckdb --name duckdb --clean-data   # also deletes the data directory
```

`remove` never deletes the binary — it may be shared across multiple named
instances. Pass `--clean-data` to delete the data directory (database files);
without it, the directory is kept and reported.

## Extensions

DuckDB's extension system installs shared libraries on first use. pgcli
pre-installs three common ones at install time:

- **`httpfs`** — read files over HTTP/S3 (`read_parquet('s3://...')`)
- **`parquet`** — Parquet file reader/writer
- **`json`** — JSON file reader (`read_json_auto(...)`)

Additional extensions can be installed later from the DuckDB shell:

```sql
INSTALL 'spatial';       -- geospatial functions
INSTALL 'sqlite';        -- read/write SQLite databases
INSTALL 'mysql';         -- query MySQL directly
LOAD 'httpfs';           -- load an installed extension in the current session
```

Pass `--no-extensions` at install time if you prefer to manage extensions
manually, or if the host has no internet access at install time.
