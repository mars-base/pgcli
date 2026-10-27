---
title: "InfluxDB"
description: "Run InfluxDB 1.x as a managed addon container, with authentication, config overrides, backups and a passthrough influx CLI for local or remote servers"
weight: 62
---

`pg addon install influxdb` runs **InfluxDB 1.13.1 OSS** — a single-node time-series database with TSM storage and InfluxQL — as a managed container from pgcli's own `ghcr.io/mars-base/pgcli/pgcli-influxdb` image (the official linux/amd64 release binaries on Alpine). Like VictoriaMetrics, it is a top-level addon: it does not front a PostgreSQL instance.

The addon is **Linux/amd64 only** (the image is single-arch). InfluxDB 1.x has **no clustering/HA**, **no built-in web UI**, and upstream has put v1 into maintenance mode; pgcli pins the final 1.13.1 release. For new deployments consider [VictoriaMetrics](./victoriametrics/) or [VictoriaLogs](./victorialogs/) if you need a maintained, auth-free-but-modern engine; InfluxDB 1.x remains a good fit when you need InfluxQL compatibility with existing tooling.

> **Authentication is always enabled.** The install generates an admin password,
> stores it in `pg.yaml`, and `pg addon password influxdb [--name NAME]` prints it
> again. The HTTP API cannot be used anonymously. The backup/restore RPC port is
> **loopback-only by design** — upstream has no authentication for it, so pgcli
> never exposes it on the network regardless of `--listen`.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `influxdb` | Instance name (map key; default subcommand name is the same) |
| `--version` | `1.13.1` | Pinned image release (selects the published `pgcli-influxdb` tag) |
| `--data-dir` | `<base_dir>/addon/influxdb/<name>/data` | Data directory bind-mounted at `/var/lib/influxdb`; absolute or relative to `base_dir` |
| `--listen` | `0.0.0.0` | HTTP bind address. The RPC endpoint always stays on `127.0.0.1` |
| `--http-port` | auto (base 8086) | Host HTTP port; auto-allocated from `influxdb_start_port` |
| `--rpc-port` | auto (base 8088) | Backup/restore RPC port, loopback only; each instance gets a unique port |
| `--admin-user` | `admin` | InfluxQL admin username (no web UI) |
| `--admin-password` | generated | Admin password. On `--force` the stored password is kept unless this is set |
| `--config-option` | — | Extra `influxdb.conf` setting as `KEY=VALUE` (top-level) or `SECTION.KEY=VALUE` (table), raw TOML value (repeatable). Replaces the stored override list and is persisted |
| `--force` | false | Rewrite the generated config and recreate the container, keeping data and the stored admin password |

The generated config only overrides paths, listeners and auth; every other value is the upstream 1.13.1 default. The rendered file lives at `<base_dir>/addon/influxdb/<name>/influxdb.conf` and is mounted read-only. Overrides are stored **both** in `pg.yaml` and in that file so they survive restarts and a re-render.

## Install

```bash
pg addon install influxdb --name metrics
```

```
✓ InfluxDB "metrics" installed
  Version:   1.13.1
  Image:     ghcr.io/mars-base/pgcli/pgcli-influxdb:1.13.1
  Container: pgcli-influxdb-metrics
  HTTP:      http://0.0.0.0:8086 (auth enabled)
  RPC:       127.0.0.1:8088 (backup/restore, loopback only)
  Admin:     admin
  Password:  <generated>
  Data dir:  /home/you/.pgcli/addon/influxdb/metrics/data
  Config:    /home/you/.pgcli/addon/influxdb/metrics/influxdb.conf
```

Second and later instances automatically take the next free HTTP/RPC ports from their pools (for example `8087`/`8089`) and can point `--data-dir` anywhere:

```bash
pg addon install influxdb --name metrics2 --data-dir /srv/influx-data --listen 127.0.0.1
```

Re-running `install` without `--force` is a no-op that reports the current state. `--force` rewrites the config and recreates the container, but **keeps the data dir and the stored admin password** (the existing admin user would otherwise become unreachable); pass `--config-option` again to retain your overrides, because the flag replaces the stored list.

## Credentials

```bash
pg addon password influxdb --name metrics   # print the admin password
pg addon list                               # shows both instances and state
```

The password is stored in `pg.yaml` (mode `0644` by default — protect that file). Commands that need credentials pass them to containers through the environment, never on the `podman` command line.

## Config overrides

```bash
# Persist an override at install time
pg addon install influxdb --name metrics --config-option 'data.index-version="tsi1"'

# Change it on a live instance, applying immediately
pg addon influxdb config set --name metrics 'data.cache-max-memory-size=1000000000' --restart
pg addon influxdb config list --name metrics
pg addon influxdb config unset --name metrics data.cache-max-memory-size
```

`config set/unset` rewrites both the generated file and `pg.yaml`; `--restart` recreates the container so the change takes effect. Without `--restart` the new value applies at the next start. Values are raw TOML: quote strings (`'data.index-version="tsi1"'`), leave numbers bare.

The upstream default index is `inmem`. For high-cardinality series set `data.index-version="tsi1"` — it can only be changed on a fresh data dir, so decide before loading data.

## The `pg influx` CLI

`pg influx` runs the `influx` client from the same image in a throwaway container — nothing is installed on the host and the container is removed after each command. Any flag pg's own parser rejects goes after `--`; arguments are forwarded verbatim.

### Local instance

```bash
pg influx --name metrics -- -execute "SHOW DATABASES"
pg influx --name metrics --database demo -- -execute "SELECT * FROM cpu LIMIT 5"
pg influx --name metrics                      # interactive shell
```

The host, port and credentials are injected automatically.

### Remote InfluxDB 1.x

```bash
# host/port flags
pg influx --host db.example.com --username admin --ssl -- -execute "SHOW DATABASES"

# DSN (userinfo, database and TLS query options)
pg influx --dsn 'influxdb://admin:secret@db.example.com:8086/demo' -- -execute "SHOW MEASUREMENTS"
pg influx --dsn 'https://admin:secret@db.example.com:8086/demo?unsafe_ssl=true' -- -execute "SHOW DATABASES"
```

Credentials fall back to `INFLUX_USERNAME` / `INFLUX_PASSWORD` in both modes. Prefer the environment variables over `--password`, whose value is visible in the process list. `--ssl` enables HTTPS; `--unsafe-ssl` skips certificate verification and requires `--ssl`. `--dsn` already carries the connection settings and cannot be combined with `--host/--port/--ssl/...`; `--name` cannot be combined with any remote flag.

### Import files in remote mode

```bash
pg influx --host db.example.com --database demo -- -import -path /tmp/demo.txt
```

The file named by `-path` is bind-mounted into the throwaway container at its absolute path, so exports produced by `pg addon influxdb export` (or `influx_inspect export`) import into remote servers too.

A `# DML` section with a `# CONTEXT-DATABASE:` marker is required; the `# DDL` section is optional. Include it when the target database may not exist — `CREATE DATABASE` is a no-op when it already does. The import target comes from the context marker, **not** from `--database`; importing into a missing database fails with `database not found`. A file without a `# DML` header is parsed as InfluxQL and fails with a syntax error.

`/tmp/demo.txt` is read on the machine running `pg` (it is bind-mounted into the container), not on `db.example.com`. A minimal file:

```text
# DDL
CREATE DATABASE demo

# DML
# CONTEXT-DATABASE: demo
# CONTEXT-RETENTION-POLICY: autogen
cpu,host=server01,region=us-west value=0.64 1434055562000000000
cpu,host=server02,region=us-west value=0.55 1434055562000000001
mem,host=server01 used_percent=42.1,free=123456789i 1434055563000000000
```

The `# DDL` block runs as InfluxQL before the data (`CREATE DATABASE` is a no-op if the database exists). Everything after `# DML` is line protocol; timestamps are optional (default nanoseconds, `-precision` changes the interpretation). Add `-pps N` to throttle and `-compressed` if the file is gzip-compressed. When the remote server has authentication enabled, pass `--username`/`--password` or export `INFLUX_USERNAME`/`INFLUX_PASSWORD`.

### Cross-host workflows

`pg influx` remote mode runs entirely from the client machine — it needs podman and the `pgcli-influxdb` image, but no local InfluxDB and no add-on instance.

```bash
# on any machine that can reach db.example.com
export INFLUX_USERNAME=admin
export INFLUX_PASSWORD=secret

pg influx --host db.example.com --database demo -- -execute "SHOW MEASUREMENTS"
pg influx --host db.example.com --database demo -- -execute "SELECT * FROM cpu LIMIT 10"
pg influx --host db.example.com -- -execute "CREATE DATABASE demo"
pg influx --host db.example.com -- -execute "CREATE RETENTION POLICY rp_30d ON demo DURATION 30d REPLICATION 1 DEFAULT"
pg influx --host db.example.com --database demo          # interactive shell

# HTTPS, and HTTPS with a self-signed certificate
pg influx --host influx.example.com --ssl --database demo -- -execute "SHOW MEASUREMENTS"
pg influx --host influx.example.com --ssl --unsafe-ssl --database demo -- -execute "SHOW MEASUREMENTS"
```

Add-on lifecycle and backup commands always act on the **local** podman instance, so they cannot reach a server on another host: `pg addon influxdb status|backup|restore|export|import`, `pg addon start|stop|remove` and `pg logs addon influxdb` must be run on the InfluxDB host itself — log in to that host and run them there. From other machines use `pg influx --host/--dsn` or the HTTP API (`/query`, `/write`) described below.

Move data between hosts with export + import:

```bash
# on the source host: offline export (the container is stopped for the duration)
pg addon influxdb export --name metrics --database demo
# transfer the file printed by the command (default under <instance dir>/exports/)
# to the target, then import it there

# logged in to the target host: import into the local instance
pg influx --name metrics --database demo -- -import -path /tmp/demo-export.txt

# or from any client that has the file: import into the remote server
pg influx --host target.example.com --database demo -- -import -path /tmp/demo-export.txt
```

Restores use the loopback-only RPC endpoint, so the backup directory must be present on the target host and `restore` run **there**:

```bash
# fresh target without a `demo` database: keep the original name
pg addon influxdb restore /tmp/influx-backup --name metrics --database demo

# if the target already has a `demo` database, `influxd restore` refuses to overwrite it;
# restore it under a new name instead
pg addon influxdb restore /tmp/influx-backup --name metrics --database demo --newdb demo_copy
```

## Common operations

### Databases

```bash
# pgcli convenience wrappers
pg addon influxdb database create demo --name metrics
pg addon influxdb database create demo --retention 30d --shard-duration 1d --rp rp_30d --name metrics
pg addon influxdb database list --name metrics
pg addon influxdb database drop demo --name metrics --yes   # --yes skips the confirmation

# the same via InfluxQL
pg influx --name metrics -- -execute "CREATE DATABASE demo"
pg influx --name metrics -- -execute "SHOW DATABASES"
pg influx --name metrics -- -execute "DROP DATABASE demo"
```

### Retention policies

Every database has at least one retention policy — the default `autogen` never expires data. A retention policy controls how long data is kept and how it is sharded; expired shards are dropped automatically.

```bash
# pgcli convenience wrappers
pg addon influxdb retention list   --name metrics --database demo
pg addon influxdb retention create --name metrics --database demo --rp rp_7d --duration 7d --default
pg addon influxdb retention alter  --name metrics --database demo --rp rp_7d --duration 14d --default
pg addon influxdb retention drop   --name metrics --database demo --rp rp_7d --yes
```

Equivalent InfluxQL:

```bash
# create a 30-day policy (REPLICATION must be 1 in OSS)
pg influx --name metrics -- \
  -execute "CREATE RETENTION POLICY rp_30d ON demo DURATION 30d REPLICATION 1"

# same, and make it the default for writes/queries without an explicit RP
pg influx --name metrics -- \
  -execute "CREATE RETENTION POLICY rp_30d ON demo DURATION 30d REPLICATION 1 DEFAULT"

# create a new database with a bounded default RP in one statement
pg influx --name metrics -- \
  -execute "CREATE DATABASE demo WITH DURATION 30d REPLICATION 1 SHARD DURATION 1d NAME rp_30d"

# change duration / default later
pg influx --name metrics -- \
  -execute "ALTER RETENTION POLICY rp_30d ON demo DURATION 90d DEFAULT"

# list and drop
pg influx --name metrics -- -execute "SHOW RETENTION POLICIES ON demo"
pg influx --name metrics -- -execute "DROP RETENTION POLICY rp_30d ON demo"

# write to / query a non-default policy explicitly
pg influx --name metrics -- -execute "INSERT INTO demo.rp_30d cpu,host=server01 value=0.64"
pg influx --name metrics -- -execute "SELECT * FROM demo.rp_30d.cpu LIMIT 10"
```

- `DURATION` accepts `h`, `d`, `w` and `INF`; the minimum is `1h`. `SHARD DURATION` is optional — by default it is derived from `DURATION`.
- Only one policy can be `DEFAULT`; making another one default demotes the previous one. Queries without an RP prefix use the default policy.
- `DROP RETENTION POLICY` deletes its data. Expired shards are removed by the background retention check (every 30 minutes by default), so disk usage drops after the next check.

### Interactive shell

```bash
pg influx --name metrics --database demo
```

Type InfluxQL directly (`SHOW MEASUREMENTS`, `SELECT ...`, `use otherdb`, …); `exit` or Ctrl-D leaves the shell. `--database` selects the initial database context.

### Inspect the schema

```bash
# all measurements
pg influx --name metrics --database demo -- -execute "SHOW MEASUREMENTS"
# measurements matching a regex
pg influx --name metrics --database demo -- -execute "SHOW MEASUREMENTS WITH MEASUREMENT =~ /cpu.*/"
# field keys and their types
pg influx --name metrics --database demo -- -execute "SHOW FIELD KEYS FROM cpu"
# tag keys
pg influx --name metrics --database demo -- -execute "SHOW TAG KEYS FROM cpu"
# values of one tag
pg influx --name metrics --database demo -- -execute 'SHOW TAG VALUES FROM cpu WITH KEY = "host"'
# every series (measurement + tag set)
pg influx --name metrics --database demo -- -execute "SHOW SERIES FROM cpu"
```

InfluxQL quoting: **double quotes** delimit identifiers (`"host"`, `"cpu"`), **single quotes** delimit string values (`'server01'`). If the query itself contains double quotes, wrap the whole `-execute` argument in single quotes as in the `SHOW TAG VALUES` example.

### Query data

```bash
# newest 10 points, human-readable timestamps
pg influx --name metrics --database demo -- -precision rfc3339 \
  -execute "SELECT * FROM cpu ORDER BY time DESC LIMIT 10"

# last hour
pg influx --name metrics --database demo -- \
  -execute "SELECT * FROM cpu WHERE time > now() - 1h"

# 5-minute averages per host
pg influx --name metrics --database demo -- \
  -execute "SELECT mean(value) FROM cpu WHERE time > now() - 1h GROUP BY time(5m), host"

# row count
pg influx --name metrics --database demo -- -execute "SELECT COUNT(value) FROM cpu"

# JSON output for scripting
pg influx --name metrics --database demo -- -format json -pretty \
  -execute "SELECT * FROM cpu LIMIT 5"
```

### Write and delete

```bash
# write a point (line protocol; timestamp optional — server time is used)
pg influx --name metrics --database demo -- \
  -execute "INSERT cpu,host=server01,region=us-west value=0.64"

# delete points older than 30 days (now() is supported)
pg influx --name metrics --database demo -- \
  -execute "DELETE FROM cpu WHERE time < now() - 30d"

# an unqualified DELETE affects every retention policy; prefix the policy to scope it
pg influx --name metrics --database demo -- \
  -execute "DELETE FROM autogen.cpu WHERE time < now() - 30d"

# drop a whole measurement / selected series
pg influx --name metrics --database demo -- -execute "DROP MEASUREMENT cpu"
pg influx --name metrics --database demo -- \
  -execute "DROP SERIES FROM cpu WHERE host = 'server01'"
```

For bulk writes use `pg influx ... -- -import -path FILE` — see [Export and import](#export-and-import).

### Users and permissions

```bash
pg influx --name metrics -- -execute "SHOW USERS"
pg influx --name metrics -- -execute 'SHOW GRANTS FOR "admin"'
pg influx --name metrics -- -execute 'CREATE USER "readonly" WITH PASSWORD '\''change-me'\'''
pg influx --name metrics -- -execute 'GRANT READ ON demo TO "readonly"'
pg influx --name metrics -- -execute 'REVOKE ALL PRIVILEGES ON demo FROM "readonly"'
pg influx --name metrics -- -execute 'DROP USER "readonly"'
```

Avoid putting the admin password in argv (`ps` shows it); use the interactive shell, `INFLUX_USERNAME`/`INFLUX_PASSWORD`, or `--password ''` to be prompted.

### Logs and status

```bash
pg addon influxdb status --name metrics        # state, endpoints, version, paths
pg logs addon influxdb --name metrics          # last 50 container log lines
pg logs addon influxdb --name metrics -n 200   # last 200 lines
pg logs addon influxdb --name metrics -f       # follow
```

## HTTP API (URL operations)

The addon serves the stock InfluxDB 1.x HTTP API on `<listen>:<http-port>` with authentication always enabled (HTTP Basic). The same calls work locally (`http://127.0.0.1:8086`) and remotely (`http://db.example.com:8086`, or `https://influx.example.com` when [nginx](./nginx/) terminates TLS).

```bash
URL=http://db.example.com:8086
AUTH='admin:secret'     # prefer --netrc-file: a literal -u value is visible in ps

# health check — 204, no auth required
curl -i "$URL/ping"

# run InfluxQL (GET /query)
curl -s -G "$URL/query" \
  --data-urlencode 'db=demo' \
  --data-urlencode 'q=SHOW MEASUREMENTS' \
  -u "$AUTH"

# create a database and a retention policy
curl -s -XPOST "$URL/query" -u "$AUTH" \
  --data-urlencode 'q=CREATE DATABASE demo'
curl -s -XPOST "$URL/query" -u "$AUTH" \
  --data-urlencode 'q=CREATE RETENTION POLICY rp_30d ON demo DURATION 30d REPLICATION 1 DEFAULT'

# write one point (POST /write) — 204 on success
curl -s -o /dev/null -w '%{http_code}\n' -XPOST "$URL/write?db=demo&precision=s" -u "$AUTH" \
  --data-binary 'cpu,host=web01,region=us-east value=0.42 1793069000'

# write many points from a line-protocol file (one point per line)
curl -s -XPOST "$URL/write?db=demo" -u "$AUTH" --data-binary @points.txt

# query with pretty JSON (timestamps stay RFC3339 unless epoch= is given)
curl -s -G "$URL/query" -u "$AUTH" \
  --data-urlencode 'db=demo' \
  --data-urlencode 'q=SELECT * FROM cpu ORDER BY time DESC LIMIT 10' \
  --data-urlencode 'pretty=true'

# the same with Unix-second timestamps
curl -s -G "$URL/query" -u "$AUTH" \
  --data-urlencode 'db=demo' --data-urlencode 'epoch=s' \
  --data-urlencode 'q=SELECT * FROM cpu LIMIT 10'

# drop a database
curl -s -XPOST "$URL/query" -u "$AUTH" --data-urlencode 'q=DROP DATABASE demo'
```

| Parameter | Endpoint | Meaning |
|---|---|---|
| `db` | `/query`, `/write` | Target database (required for most statements) |
| `rp` | `/query`, `/write` | Target retention policy (defaults to the database default) |
| `q` | `/query` | InfluxQL statement; URL-encode it (`--data-urlencode` does this) |
| `pretty=true` | `/query` | Indent the JSON response |
| `epoch` | `/query` | `ns`, `u`, `ms`, `s`, `m`, `h`; omit for RFC3339 timestamps |
| `precision` | `/write` | Timestamp precision of the supplied line protocol (`ns` by default) |

Notes:

- Unauthenticated requests return `401`. A successful write returns `204`; malformed points return `400` with a `partial write` message that names the dropped points.
- `curl -u user:pass` exposes the password in the process list. Prefer a mode-0600 netrc file:
  `machine db.example.com login admin password secret`, then `curl --netrc-file ~/.netrc-influx ...`.
- For day-to-day local work `pg influx` is simpler and injects credentials through the environment; the HTTP API is the way to integrate from other machines, scripts and applications.

## Backup and restore

Portable backups are **online** — the instance keeps running:

```bash
pg addon influxdb backup --name metrics                        # whole instance
pg addon influxdb backup --name metrics --database demo
pg addon influxdb backup --name metrics --since 2026-10-01T00:00:00Z
pg addon influxdb backup --name metrics --start 2026-10-01T00:00:00Z --end 2026-10-02T00:00:00Z
```

The default output dir is `<instance dir>/backups/<UTC timestamp>`; `--out DIR` overrides it. A backup directory contains `.manifest`, `.meta` and per-shard `.tar.gz` files.

> ⚠️ **Store backups on remote S3.** The backup is written on the InfluxDB host, so losing that host would lose the backup too. Upload each backup directory with any S3 client — for example the `pg mc` add-on:

```bash
pg mc alias set store https://s3.example.com ACCESS_KEY SECRET_KEY   # once
pg mc mb store/influxdb-backups
pg mc cp /path/to/backup-demo store/influxdb-backups/ -- --recursive
```

A restore reads the directory through the loopback RPC, so download it back to the target host before running `pg addon influxdb restore`.

Restores are **online** too (the helper talks to the running server over the loopback RPC; a stopped instance is started first). A complete backup → drop → restore cycle for one database:

```bash
# 1. back up the database before replacing it
pg addon influxdb backup --name metrics --database demo --out /path/to/backup-demo

# 2. drop the old database
pg addon influxdb database drop demo --name metrics --yes

# 3. restore it under the original name
pg addon influxdb restore /path/to/backup-demo --name metrics --database demo
```

`influxd restore` refuses to overwrite an existing database. To restore alongside the old one instead of replacing it, use a new name — `--newdb` requires `--database`:

```bash
pg addon influxdb restore /path/to/backup-demo --name metrics --database demo --newdb demo_copy
```

The instance is left running after the restore.

> ⚠️ **A restore is not a complete identity/metadata copy.** Users, privileges and continuous queries are not restored — recreate them manually after a restore. Retention policies and their durations are restored.

## Export and import

`export` is **offline**: pgcli stops the container so `influx_inspect` reads consistent TSM files, then restarts it unless `--no-start` is given.

```bash
pg addon influxdb export --name metrics --database demo
pg addon influxdb export --name metrics --out /tmp/all.txt --compress --no-start
pg addon influxdb export --name metrics --start 2026-10-01T00:00:00Z --end 2026-10-02T00:00:00Z
```

The default file is `<instance dir>/exports/<database|all>-<UTC timestamp>.txt` (`.gz` with `--compress`).

> ⚠️ **After an import you must recreate users, privileges and continuous queries, and recreate non-default retention policies with their real `DURATION`/`SHARD DURATION`.** The export DDL emits RPs as `DURATION 0s` and omits the shard duration, so a 30d RP would become infinite unless fixed.

An export file is a **logical data copy**, not a full instance snapshot: it contains every data point (TSM plus WAL) with per-database/retention-policy context markers, plus `CREATE DATABASE` / `CREATE RETENTION POLICY` DDL. Without `--database`, all databases including `_internal` are exported. Use `backup`/`restore` for disaster recovery and `export`/`import` to move points between servers.

`import` needs a running instance and takes an export-format file:

```bash
pg addon influxdb database create demo_copy --name metrics
pg addon influxdb import /tmp/demo.txt --name metrics --database demo_copy
pg addon influxdb import /tmp/demo.txt --name metrics --precision ms
```

With `--database`, pgcli copies the file under the instance directory and rewrites the `# CONTEXT-DATABASE:` lines to the target database, so the original export is not modified. Importing into a remote server is done with `pg influx --host ... -- -import -path FILE` (above).

Unlike `restore`, `import` has no new-name restriction: it writes line-protocol points into the target and merges them with existing data (a point with the same series and timestamp overwrites the old one). The `demo_copy` above is only an example of importing into another database — importing into the original `demo` is fine. The target database must already exist (the file's `# DDL` can create it) unless `--database` names a database you created first.

## Lifecycle

```bash
pg addon start influxdb --name metrics     # starts the container
pg addon stop influxdb --name metrics
pg addon influxdb status --name metrics    # state, ping, endpoints, version
pg logs addon influxdb --name metrics -f   # container logs
pg addon remove influxdb --name metrics    # removes the container and pg.yaml entry, keeps data
pg addon remove influxdb --name metrics --clean-data   # also deletes the config/backups and data dir
```

Data and the generated config survive `remove` without `--clean-data`, so reinstalling under the same name revives the stored series. `--clean-data` also removes a custom `--data-dir`.

Auto-start after reboot:

```bash
pg autostart enable --influxdb --name metrics
pg autostart status
pg autostart disable --influxdb --name metrics
```

At boot pgcli starts the existing container; it does not re-run authentication bootstrap.

## Production notes

- **No HTTPS listener.** InfluxDB 1.x serves plain HTTP. Front it with the [nginx addon](./nginx/) if you need TLS, or bind `--listen 127.0.0.1` and keep it local.
- **RPC is loopback-only.** The backup/restore RPC protocol has no authentication; pgcli pins it to `127.0.0.1` and allocates one unique port per instance. Do not expose it.
- **No HA.** InfluxDB 1.x OSS is single-node. Backups (`backup` / `export`) are the disaster-recovery path; there is no scheduled backup — wire `pg addon influxdb backup` into your own timer if needed.
- **v1 is in maintenance mode.** The version is pinned to 1.13.1; upgrading means publishing a new image tag and re-testing. There is no in-place major upgrade path from v1 to v2/v3.
- **High cardinality:** switch to `data.index-version="tsi1"` before loading data (above).
- **Platform:** Linux/amd64 only — the image packages the official amd64 tarball.

## CLI flag reference

A one-page lookup of every pgcli flag that applies to InfluxDB. InfluxDB's own
CLI flags go after `--` and are forwarded verbatim (see `influx -help`).

### `pg addon install influxdb`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `influxdb` | Instance name (map key); `[A-Za-z0-9][A-Za-z0-9_-]*`, max 48 chars |
| `--version V` | `1.13.1` | Pinned image release; only versions shipped as a pgcli image are accepted |
| `--data-dir DIR` | `<base_dir>/addon/influxdb/<name>/data` | Host dir bind-mounted at `/var/lib/influxdb`; absolute or relative to `base_dir` |
| `--listen ADDR` | `0.0.0.0` | HTTP bind address only; the RPC listener is always `127.0.0.1` |
| `--http-port N` | auto (base `8086`) | HTTP host port; `0` = auto-assign from `influxdb_start_port` |
| `--rpc-port N` | auto (base `8088`) | Backup/restore RPC port, loopback only; `0` = auto-assign; must differ from `--http-port` |
| `--admin-user USER` | `admin` | InfluxQL admin username |
| `--admin-password PW` | generated | Admin password; on `--force` the stored value is kept unless supplied |
| `--config-option K=V` | — | Extra `influxdb.conf` entry: `KEY=VALUE` (top-level) or `SECTION.KEY=VALUE` (table), raw TOML; repeatable; replaces the stored override list |
| `--force` | false | Rewrite the generated config and recreate the container; keeps data and the stored password |

`--image` does not apply to InfluxDB — use `--version`.

### `pg addon influxdb status`

| Argument/flag | Default | Description |
|---|---|---|
| `--name NAME` | `influxdb` | Instance to inspect |

### `pg addon influxdb shell [-- influx args...]`

| Argument/flag | Default | Description |
|---|---|---|
| `--name NAME` | `influxdb` | Instance to connect to |
| `--database DB` | — | Adds `-database DB` |
| args after `--` | — | Forwarded verbatim to the `influx` client (`-execute`, `-format`, ...) |

### `pg addon influxdb database create NAME` / `database list`

| Argument/flag | Default | Description |
|---|---|---|
| `NAME` | — | Database to create (positional, `create` only) |
| `--name NAME` | `influxdb` | Instance to operate on |

### `pg addon influxdb backup`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `influxdb` | Instance to back up (must be running) |
| `--out DIR` | `<instance dir>/backups/<UTC timestamp>` | Backup output directory |
| `--database DB` | all | Back up one database (`-db`) |
| `--since RFC3339` | — | Back up data since this instant; cannot be combined with `--start/--end` |
| `--start RFC3339` | — | Range start; requires `--end` |
| `--end RFC3339` | — | Range end; requires `--start` |

### `pg addon influxdb restore BACKUP_DIR`

| Argument/flag | Default | Description |
|---|---|---|
| `BACKUP_DIR` | — | Backup directory to restore (positional, required) |
| `--name NAME` | `influxdb` | Target instance (started first if stopped) |
| `--database DB` | all | Restore one database (`-db`) |
| `--newdb NAME` | — | Restore under a new database name (`-newdb`); requires `--database` |

Restoring a database that already exists fails; use `--newdb` to restore alongside it.

### `pg addon influxdb export`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `influxdb` | Instance to export (stopped for the duration) |
| `--database DB` | all | Export one database (`-database`) |
| `--out FILE` | `<instance dir>/exports/<database\|all>-<UTC timestamp>.txt[.gz]` | Output file |
| `--compress` | false | Gzip the output (`-compress`) |
| `--start RFC3339` | — | Range start; requires `--end` |
| `--end RFC3339` | — | Range end; requires `--start` |
| `--no-start` | false | Leave the instance stopped afterwards |

### `pg addon influxdb import FILE`

| Argument/flag | Default | Description |
|---|---|---|
| `FILE` | — | Export-format file to import (positional, required) |
| `--name NAME` | `influxdb` | Target instance (must be running) |
| `--database DB` | file's context | Target database (`-database`); rewrites `# CONTEXT-DATABASE:` lines |
| `--precision P` | — | Timestamp precision (`-precision`): `ns`, `u`, `ms`, `s`, `m`, `h` |

### `pg addon influxdb config list` / `set` / `unset`

| Argument/flag | Default | Description |
|---|---|---|
| `KEY=VALUE...` | — | Entries to set (positional, `set` only; one or more) |
| `SECTION.KEY...` | — | Entries to remove (positional, `unset` only; one or more) |
| `--name NAME` | `influxdb` | Instance to modify |
| `--restart` | false | Restart the container after rewriting the config (`set`/`unset` only) |

### `pg influx`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `influxdb` | Managed addon instance; conflicts with every remote flag |
| `--database DB` | — | Adds `-database DB` |
| `--host HOST` | — | Remote host; enables remote mode |
| `--port N` | `8086` | Remote HTTP port |
| `--dsn URL` | — | `influxdb://user:pass@host:port/db` or `http(s)://...`; exclusive with the other connection flags |
| `--username USER` | `$INFLUX_USERNAME` | Remote username |
| `--password PW` | `$INFLUX_PASSWORD` | Remote password (prefer the env var: `ps` shows argv) |
| `--ssl` | false | Use HTTPS (`-ssl`) |
| `--unsafe-ssl` | false | Skip TLS verification (`-unsafeSsl`); requires `--ssl` |
| args after `--` | — | Forwarded verbatim to the `influx` client |

### Lifecycle, credentials, logs, autostart

| Command | Flags | Description |
|---|---|---|
| `pg addon start influxdb` | `--name NAME` (default `influxdb`) | Start (create if missing) the container |
| `pg addon stop influxdb` | `--name NAME` (default `influxdb`) | Stop the container |
| `pg addon remove influxdb` | `--name NAME` (default `influxdb`), `--clean-data` | Remove the container and config entry; `--clean-data` also deletes the instance dir, backups and data dir |
| `pg addon password influxdb` | `--name NAME` (default `influxdb`) | Print the admin password |
| `pg logs addon influxdb` | `--name NAME` (default `influxdb`), `-n N` (`--tail`, default 50), `-f` (`--follow`) | Container logs |
| `pg autostart enable\|disable` | `--influxdb`, `--name NAME` (default `influxdb`) | Toggle boot start for one instance |

### Global flags

| Flag | Description |
|---|---|
| `-c, --config FILE` | Config file (default `~/.pgcli/pg.yaml`) |
| `-o, --output FILE` | Output config path (default the same file) |
| `-i, --instance NAME` | PostgreSQL instance selector — not used by InfluxDB; use `--name` instead |

## Full `influxdb.conf` parameter reference

The generated config is **minimal**: pgcli renders only the keys below and leaves every other InfluxDB 1.13.1 default in place. The defaults in the tables are the effective values printed by `influxd config` (1.13.1).

| pgcli-managed key | Rendered value | Why |
|---|---|---|
| `reporting-disabled` | `true` | Disables upstream usage reporting |
| `bind-address` (top level) | `"127.0.0.1:<rpc-port>"` | Backup/restore RPC; upstream has no auth for it, so it is never exposed |
| `[meta] dir` | `/var/lib/influxdb/meta` | Inside the data-dir bind mount |
| `[data] dir` | `/var/lib/influxdb/data` | Inside the data-dir bind mount |
| `[data] wal-dir` | `/var/lib/influxdb/wal` | Inside the data-dir bind mount |
| `[http] bind-address` | `<listen>:<http-port>` | Host networking: this is the actual host listener |
| `[http] auth-enabled` | `true` | Authentication is always on |

These seven keys are rejected by `--config-option` / `config set` — change them through `pg addon install` / `config` flags instead. Everything else can be overridden.

### How to override

```bash
# SECTION.KEY=VALUE for a table entry; KEY=VALUE for a top-level entry
pg addon influxdb config set --name metrics 'data.index-version="tsi1"' --restart
pg addon influxdb config set --name metrics 'http.max-row-limit=50000' --restart
pg addon influxdb config unset --name metrics data.index-version
```

Values are **raw TOML** — quote strings, durations and sizes (`'cache-max-memory-size="2g"'`), leave numbers/bools bare. Limitations:

- Only single `[section]` tables are supported. The repeated `[[graphite]]`, `[[collectd]]`, `[[opentsdb]]` and `[[udp]]` listener tables cannot be expressed; neither can nested keys such as `https-client-ca.paths` or `http.headers`.
- Such settings require editing `<base_dir>/addon/influxdb/<name>/influxdb.conf` directly and restarting the container (`pg addon stop|start influxdb`). Ordinary start/stop keeps the file; `install --force` and any `config set/unset` re-render it and will drop hand edits.
- `--config-option` replaces the whole stored override list; re-pass all entries when changing one.

### Top level

| Key | Default | Description |
|---|---|---|
| `reporting-disabled` | `false` | pgcli renders `true` (managed) |
| `bind-address` | `"127.0.0.1:8088"` | RPC backup/restore listener (managed) |
| `hardening-enabled` | `false` | Restrict Flux HTTP requests to public addresses (SSRF hardening) |

### `[meta]`

| Key | Default | Description |
|---|---|---|
| `dir` | `~/.influxdb/meta` (`/var/lib/influxdb/meta` in the container) | Metadata/Raft database directory (managed) |
| `retention-autocreate` | `true` | Auto-create the `autogen` retention policy on `CREATE DATABASE` |
| `logging-enabled` | `true` | Log meta-service messages |

### `[data]`

| Key | Default | Description |
|---|---|---|
| `dir` | `~/.influxdb/data` | TSM storage directory (managed) |
| `wal-dir` | `~/.influxdb/wal` | WAL directory (managed) |
| `wal-fsync-delay` | `"0s"` | Batch window before fsync; `0s` fsyncs every write; 0–100ms helps non-SSD disks |
| `index-version` | `"inmem"` | Shard index: `inmem` (default) or `tsi1` (high cardinality; only settable on a fresh data dir) |
| `validate-keys` | `false` | Reject keys with invalid UTF-8/unicode (small write overhead) |
| `skip-field-size-validation` | `false` | Skip the maximum field-size check on writes |
| `strict-error-handling` | `false` | Return errors instead of silently dropping invalid points (e.g. ±Inf in `SELECT INTO`) |
| `query-log-enabled` | `true` | Log queries before execution (may log sensitive data) |
| `cache-max-memory-size` | `"1g"` | Per-shard cache limit before writes are rejected |
| `cache-snapshot-memory-size` | `"25m"` | Cache size that triggers a snapshot to TSM |
| `cache-snapshot-write-cold-duration` | `"10m"` | Snapshot the cache after this long without writes/deletes |
| `compact-full-write-cold-duration` | `"4h"` | Full compaction after this long without writes/deletes |
| `compact-throughput` | `"48m"` | Compaction write rate limit (bytes/s) |
| `compact-throughput-burst` | `"48m"` | Allowed short burst above the compaction rate limit |
| `aggressive-points-per-block` | `10000` | Points per block for aggressive compaction |
| `ingress-metric-by-measurement-enabled` | `false` | Collect per-measurement write statistics |
| `ingress-metric-by-login-enabled` | `false` | Collect per-login write statistics |
| `max-series-per-database` | `1000000` | Series limit per database before writes are dropped (`0` = unlimited) |
| `max-values-per-tag` | `100000` | Tag-value limit per tag (`0` = unlimited) |
| `max-concurrent-compactions` | `0` | Concurrent full/level compactions; `0` = 50% of GOMAXPROCS |
| `max-concurrent-optimized-compactions` | `0` | Concurrent optimized compactions (`0` = default) |
| `max-index-log-file-size` | `"1m"` | TSI index WAL threshold before compaction into an index file |
| `max-concurrent-deletes` | `1` | Simultaneous `DELETE` calls per shard |
| `series-id-set-cache-size` | `100` | TSI series-id-set cache entries (`0` disables) |
| `series-id-set-cache-max-size` | `0` | Upper bound for adaptive cache growth (`0` = adaptive sizing off) |
| `series-id-set-cache-target-hit-rate` | `0.0` | Target hit rate for adaptive sizing (`0.0` = off, `1.0` invalid) |
| `series-id-set-cache-shrink-conservatism` | `2.5` | Shrink-policy margin in standard deviations |
| `series-file-max-concurrent-snapshot-compactions` | `0` | Concurrent series-file snapshot compactions (`0` = default) |
| `trace-logging-enabled` | `false` | Verbose TSM engine trace logging |
| `tsm-use-madv-willneed` | `false` | Advise the kernel with `MADV_WILLNEED` for TSM files |
| `tar-stream-buffer-size` | `"1m"` | Buffer size for tar streaming during backup/rename/copy |

### `[coordinator]`

| Key | Default | Description |
|---|---|---|
| `write-timeout` | `"10s"` | How long a write waits before returning a timeout |
| `max-concurrent-queries` | `0` | Concurrent query limit (`0` = unlimited) |
| `query-timeout` | `"0s"` | Kill queries after this long (`0s` = no limit) |
| `log-queries-after` | `"0s"` | Slow-query logging threshold (`0s` = off) |
| `log-timedout-queries` | `false` | Log queries killed by `query-timeout` |
| `max-select-point` | `0` | Maximum points a `SELECT` may process (`0` = unlimited) |
| `max-select-series` | `0` | Maximum series a `SELECT` may touch (`0` = unlimited) |
| `max-select-buckets` | `0` | Maximum `GROUP BY time()` buckets (`0` = unlimited) |
| `termination-query-log` | `false` | Print running queries on `SIGTERM` |

### `[flux-controller]`

| Key | Default | Description |
|---|---|---|
| `query-concurrency` | `0` | Concurrent Flux queries (`0` = default) |
| `query-initial-memory-bytes` | `"0"` | Initial memory budget per Flux query |
| `query-max-memory-bytes` | `"0"` | Maximum memory per Flux query |
| `total-max-memory-bytes` | `"0"` | Total memory for all Flux queries |
| `query-queue-size` | `0` | Flux query queue length |

### `[retention]`

| Key | Default | Description |
|---|---|---|
| `enabled` | `true` | Enforce retention policies |
| `check-interval` | `"30m"` | How often retention enforcement runs |

### `[shard-precreation]`

| Key | Default | Description |
|---|---|---|
| `enabled` | `true` | Pre-create shards before data arrives |
| `check-interval` | `"10m"` | How often pre-creation runs |
| `advance-period` | `"30m"` | How far ahead of a shard group's end its successor is created |

### `[monitor]`

| Key | Default | Description |
|---|---|---|
| `store-enabled` | `true` | Record internal statistics |
| `store-database` | `"_internal"` | Destination database |
| `store-interval` | `"10s"` | Recording interval |

### `[http]`

| Key | Default | Description |
|---|---|---|
| `enabled` | `true` | Enable the HTTP API |
| `bind-address` | `":8086"` | HTTP listener (managed: `<listen>:<http-port>`) |
| `auth-enabled` | `false` | Require authentication (managed: `true`) |
| `realm` | `"InfluxDB"` | Basic-auth challenge realm |
| `log-enabled` | `true` | HTTP request logging |
| `suppress-write-log` | `false` | Hide write requests from the HTTP log |
| `access-log-path` | `""` | Access-log file; empty writes to stderr |
| `access-log-status-filters` | `[]` | Status filters (`NNN`, `NNX`, `NXX`); empty logs everything |
| `write-tracing` | `false` | Detailed write logging |
| `flux-enabled` | `false` | Enable the Flux query endpoint |
| `flux-log-enabled` | `false` | Log Flux queries |
| `flux-testing` | `false` | Expose Flux testing endpoints |
| `pprof-enabled` | `true` | Enable `/debug/pprof` |
| `pprof-auth-enabled` | `false` | Require admin auth for pprof |
| `debug-pprof-enabled` | `false` | Bind an extra pprof endpoint on `localhost:6060` at startup |
| `ping-auth-enabled` | `false` | Require auth on `/ping`, `/metrics`, `/status` |
| `prom-read-auth-enabled` | `false` | Require auth on the Prometheus remote-read API |
| `https-enabled` | `false` | Serve HTTPS |
| `https-certificate` | `"/etc/ssl/influxdb.pem"` | TLS certificate (PEM) |
| `https-private-key` | `""` | TLS private key; empty = combined with the certificate |
| `https-insecure-certificate` | `false` | Allow insecure certificate file permissions |
| `https-ignore-sanity-checks` | `false` | Load certificates that fail server-auth sanity checks |
| `https-client-auth-type` | unset | Mutual TLS client-auth mode (`NoClientCert`, `RequestClientCert`, `RequireAnyClientCert`, `VerifyClientCertIfGiven`, `RequireAndVerifyClientCert`) |
| `https-client-ca` | unset | Client CA bundle: `{ paths = [...], include-system = false }` |
| `shared-secret` | `""` | JWT validation secret |
| `max-row-limit` | `0` | Chunked-result row limit (`0` = unlimited) |
| `max-connection-limit` | `0` | Concurrent HTTP connections (`0` = unlimited) |
| `unix-socket-enabled` | `false` | Serve HTTP over a Unix socket |
| `unix-socket-permissions` | `"0777"` | Unix socket permissions |
| `bind-socket` | `"/var/run/influxdb.sock"` | Unix socket path |
| `max-body-size` | `25000000` | Maximum request body in bytes (`0` = unlimited) |
| `max-concurrent-write-limit` | `0` | Concurrent writes (`0` = unlimited) |
| `max-enqueued-write-limit` | `0` | Queued writes (`0` = unlimited) |
| `enqueued-write-timeout` | `"30s"` | Maximum queue wait for a write |
| `user-query-bytes-enabled` | `false` | Track query response bytes per user |
| `user-write-bytes-enabled` | `false` | Track write request bytes per user |
| `[http.headers]` | unset | Arbitrary response headers, e.g. `X-Header-1 = "value"` |

### `[logging]`

| Key | Default | Description |
|---|---|---|
| `format` | `"auto"` | Log encoder: `auto`, `logfmt` or `json` |
| `level` | `"info"` | Minimum level: `error`, `warn`, `info`, `debug` |
| `suppress-logo` | `false` | Suppress the startup logo |

### `[subscriber]`

| Key | Default | Description |
|---|---|---|
| `enabled` | `true` | Enable subscriptions (fork every write to an endpoint) |
| `http-timeout` | `"30s"` | Subscriber write timeout |
| `insecure-skip-verify` | `false` | Skip TLS verification for subscribers |
| `ca-certs` | `""` | PEM CA bundle path; empty uses the system roots |
| `root-ca` | unset | Extra subscriber CAs: `{ paths = [...], include-system = false }` |
| `certificate` | `""` | Client certificate presented to subscribers (mutual TLS) |
| `private-key` | `""` | Key for `certificate` |
| `insecure-certificate` | `false` | Allow insecure certificate/key file permissions |
| `ignore-cert-sanity-checks` | `false` | Load certificates failing server-auth sanity checks |
| `write-concurrency` | `40` | Writer goroutines |
| `write-buffer-size` | `1000` | In-flight writes buffered in the channel |
| `total-buffer-bytes` | `"0"` | Total buffer budget (`0` = unlimited) |

### Repeated listener tables

These are **array-of-tables** (`[[graphite]]`, `[[collectd]]`, `[[opentsdb]]`, `[[udp]]`) and cannot be set with `--config-option`; edit the generated file directly if you need them. Each table starts disabled.

| Section | Key | Default | Description |
|---|---|---|---|
| `[[graphite]]` | `enabled` | `false` | Enable the Graphite listener |
| | `bind-address` | `":2003"` | Listener address |
| | `database` | `"graphite"` | Target database |
| | `retention-policy` | `""` | Target retention policy |
| | `protocol` | `"tcp"` | `tcp` or `udp` |
| | `consistency-level` | `"one"` | Write consistency level |
| | `batch-size` | `5000` | Points buffered before flush |
| | `batch-pending` | `10` | Pending batches |
| | `batch-timeout` | `"1s"` | Maximum time before a flush |
| | `udp-read-buffer` | `0` | UDP read buffer (`0` = OS default) |
| | `separator` | `"."` | Joins template measurement parts |
| | `tags` | `[]` | Default tags, e.g. `["region=us-east"]` |
| | `templates` | `[]` | Template rules (one default) |
| `[[collectd]]` | `enabled` | `false` | Enable the collectd listener |
| | `bind-address` | `":25826"` | Listener address |
| | `database` | `"collectd"` | Target database |
| | `retention-policy` | `""` | Target retention policy |
| | `typesdb` | `"/usr/share/collectd/types.db"` | Types DB file or directory |
| | `security-level` | `"none"` | `none`, `sign` or `encrypt` |
| | `auth-file` | `"/etc/collectd/auth_file"` | Auth file for signed/encrypted traffic |
| | `batch-size` | `5000` | Points buffered before flush |
| | `batch-pending` | `10` | Pending batches |
| | `batch-timeout` | `"10s"` | Maximum time before a flush |
| | `read-buffer` | `0` | UDP read buffer (`0` = OS default) |
| | `parse-multivalue-plugin` | `"split"` | `split` or `join` multi-value plugins |
| `[[opentsdb]]` | `enabled` | `false` | Enable the OpenTSDB listener |
| | `bind-address` | `":4242"` | Listener address |
| | `database` | `"opentsdb"` | Target database |
| | `retention-policy` | `""` | Target retention policy |
| | `consistency-level` | `"one"` | Write consistency level |
| | `tls-enabled` | `false` | Serve TLS |
| | `certificate` | `"/etc/ssl/influxdb.pem"` | TLS certificate |
| | `private-key` | `""` | TLS key; empty = combined with the certificate |
| | `insecure-certificate` | `false` | Allow insecure certificate file permissions |
| | `ignore-cert-sanity-checks` | `false` | Load certificates failing sanity checks |
| | `client-auth-type` | unset | Mutual TLS client-auth mode |
| | `client-ca` | unset | Client CA bundle: `{ paths = [...], include-system = false }` |
| | `log-point-errors` | `true` | Log every malformed point |
| | `batch-size` | `1000` | Points buffered before flush (telnet only) |
| | `batch-pending` | `5` | Pending batches |
| | `batch-timeout` | `"1s"` | Maximum time before a flush |
| `[[udp]]` | `enabled` | `false` | Enable the line-protocol UDP listener |
| | `bind-address` | `":8089"` | Listener address |
| | `database` | `"udp"` | Target database |
| | `retention-policy` | `""` | Target retention policy |
| | `precision` | `"n"` | Input precision: `""`, `n`, `u`, `ms`, `s`, `m`, `h` |
| | `batch-size` | `5000` | Points buffered before flush |
| | `batch-pending` | `10` | Pending batches |
| | `batch-timeout` | `"1s"` | Maximum time before a flush |
| | `read-buffer` | `0` | UDP read buffer (`0` = OS default) |
| | `writers` | `1` | Parallel writers |

### `[continuous_queries]`

| Key | Default | Description |
|---|---|---|
| `enabled` | `true` | Run continuous queries |
| `log-enabled` | `true` | Log CQ executions |
| `query-stats-enabled` | `false` | Write CQ stats to the monitoring store |
| `run-interval` | `"1s"` | How often CQs are checked |

### `[tls]`

| Key | Default | Description |
|---|---|---|
| `ciphers` | Go defaults | Allowed cipher suites (Go names) |
| `min-version` | Go default | Minimum TLS version, e.g. `"tls1.2"` |
| `max-version` | Go default | Maximum TLS version, e.g. `"tls1.3"` |

> **HTTPS on the addon:** pgcli never enables `[http] https-enabled`. Use the
> [nginx addon](./nginx/) for TLS termination, or expose only loopback. The
> `[tls]` section only tunes cipher/version negotiation for listeners that are
> already TLS-enabled.


