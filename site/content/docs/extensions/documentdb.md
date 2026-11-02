---
title: "DocumentDB"
description: "Install the official DocumentDB PostgreSQL 18 extension with pgcli, including preload libraries, background workers and the shared Unix socket used by the gateway."
weight: 25
---

[DocumentDB](https://documentdb.io/) is Microsoft's open-source document database layer for PostgreSQL. It is split into two pieces:

- **PostgreSQL extension** (`documentdb`, `documentdb_core`, `documentdb_extended_rum`) — stores BSON documents, indexes and queries inside PostgreSQL. This page installs it with pgcli.
- **MongoDB wire-protocol gateway** — a standalone Rust service that accepts MongoDB clients. Install it separately with [`pg addon install documentdb`](../../addon/documentdb/).

This page covers the extension. The gateway requires the extension, so install it first.

> **PostgreSQL 18 only.** The official DocumentDB v1.0 vendor package is built for PostgreSQL 18. pgcli rejects the install on other majors. Patroni/HA instances are not supported because DocumentDB's settings are written to `postgresql.conf`, which Patroni's DCS would overwrite.

## Install

```bash
pg extension install documentdb -i <instance> --auto-restart
```

`--auto-restart` lets pgcli restart PostgreSQL immediately: the DocumentDB libraries are postmaster-level (`shared_preload_libraries`), so a plain reload is not enough.

What the command does:

1. Downloads the official vendor DEB `ubuntu24.04-postgresql-18-documentdb_1.0-0_<arch>.deb` (amd64/arm64) from GitHub releases and verifies its SHA-256.
2. Downloads the Ubuntu 24.04 ICU 74 runtime (`libicu74`). The vendor `pg_documentdb_core.so` references ICU 74 symbols; Debian 13 ships ICU 76. During the image build pgcli installs `libicu74` and adds a `DT_NEEDED` entry for `libicui18n.so.74` with `patchelf`, so the library loads on the Debian-based PG 18 image.
3. Builds the shared `-ext` image with the remaining packages (`postgresql-18-cron`, `postgis`, `pgvector`, …).
4. Recreates the instance container from the new image and adds the shared Unix socket volume (`pgcli-pg-sock-<container>` mounted at `/var/run/postgresql`). The gateway add-on later reads that socket.
5. Writes the managed `postgresql.conf` block and creates the SQL extensions in the instance's configured database.

## Configuration Written by pgcli

`shared_preload_libraries` (order matters):

```
shared_preload_libraries = 'pg_cron,pg_documentdb_core,pg_documentdb,pg_documentdb_extended_rum'
```

Managed settings:

| Setting | Value | Purpose |
|---|---|---|
| `cron.database_name` | instance database | database where `pg_cron` runs |
| `cron.use_background_workers` | `on` | use background workers instead of the launcher |
| `documentdb.enableBackgroundWorker` | `true` | DocumentDB background worker |
| `documentdb.enableBackgroundWorkerJobs` | `true` | scheduled index/cleanup jobs |
| `documentdb.indexBuildsScheduledOnBgWorker` | `false` | index builds stay in the foreground |
| `documentdb.localhost_connection_string` | `host=/var/run/postgresql port=<instance port>` | how the extension's loader connects back |
| `default_toast_compression` | `lz4` | compress large document values |
| `documentdb.rum_library_load_option` | `require_documentdb_extended_rum` | require the RUM companion |
| `documentdb.alternate_index_handler_name` | `extended_rum` | route DocumentDB indexes through RUM |

The SQL side runs, in order:

```sql
CREATE EXTENSION IF NOT EXISTS pg_cron;
CREATE EXTENSION IF NOT EXISTS "documentdb" CASCADE;
CREATE EXTENSION IF NOT EXISTS "documentdb_extended_rum";
```

`CREATE EXTENSION documentdb CASCADE` creates `documentdb_core`, but **not** `documentdb_extended_rum`; pgcli creates it explicitly. After the batch it verifies all four extensions exist and aborts the install if any is missing.

## Verify

```bash
pg exec -i <instance> "SHOW shared_preload_libraries"
pg exec -i <instance> "SHOW documentdb.localhost_connection_string"
pg exec -i <instance> "SELECT extname FROM pg_extension ORDER BY 1"
```

Expected extensions include `documentdb`, `documentdb_core`, `documentdb_extended_rum` and `pg_cron`. The instance must be recreated with the socket mount before the gateway can be installed; check with:

```bash
podman inspect <pg container> --format '{{range .Mounts}}{{.Destination}}{{"\n"}}{{end}}'
```

`/var/run/postgresql` must be listed.

## Remove

```bash
pg extension remove documentdb -i <instance> --auto-restart
```

Removal is deliberately **not** `CASCADE`: DocumentDB collections are ordinary PostgreSQL objects, and dropping the extension with data still present fails. Delete the document data first, then remove the extension. pgcli drops in dependency order:

```sql
DROP EXTENSION IF EXISTS "documentdb_extended_rum";
DROP EXTENSION IF EXISTS "documentdb";
DROP EXTENSION IF EXISTS "documentdb_core";
```

`pg_cron` is dropped only when nothing else in `extensions:` needs it **and** `cron.job` is empty; otherwise it is kept with a warning. A failed DocumentDB drop aborts before the config is saved, so the managed extension list stays intact.

The extension packages stay in the shared `-ext` image after removal (pgcli never uninstalls packages from an image). Removing the extension from one instance does not affect another instance using the same `-ext` image.

## Notes

- **Vendor package, not Pigsty**: DocumentDB is not in the Pigsty DEB mirror. pgcli downloads and checksums the official GitHub release directly.
- **Image build needs network access** for the DEB downloads; the checksum pins the exact build.
- **MACVLAN/multi-instance**: every instance gets its own socket volume; the gateway add-on selects the instance with `--pg-name`.
- **Telemetry**: the extension itself does not ship telemetry; the gateway add-on renders its configuration with metrics and tracing explicitly disabled.
