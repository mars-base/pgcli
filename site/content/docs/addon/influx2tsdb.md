---
title: "influx2tsdb"
description: "Run the influx2tsdb proxy as a managed container — InfluxDB 1.x HTTP API (Line Protocol + InfluxQL) on top of PostgreSQL + TimescaleDB, with automatic retention and compression jobs"
weight: 63
---

`pg addon install influx2tsdb` runs **influx2tsdb-proxy 1.3.2** — an InfluxDB 1.x
HTTP API adapter (Line Protocol writes and InfluxQL queries) that stores data in
PostgreSQL with the TimescaleDB extension — as a managed container from pgcli's own
`ghcr.io/mars-base/pgcli/pgcli-influx2tsdb` image. Unlike the [InfluxDB addon](./influxdb/),
there is no TSM engine: every point lives in a TimescaleDB hypertable, and the proxy
itself is **stateless**.

The add-on is **Linux/amd64 only** (the image is single-arch, built from the upstream
release binary on `debian:12-slim`). The proxy container can be recreated at any time;
the data survives because PostgreSQL holds it.

> **The HTTP API has no authentication.** The upstream proxy speaks plain HTTP, binds
> all interfaces, and does not check InfluxDB credentials. Use the
> [nginx addon](./nginx/) for TLS and access control, or firewall the port. The
> endpoint cannot be used safely on an untrusted network.

## When to use it — and what you give up

Native InfluxDB 1.x keeps time-series data in its own **in-memory index + TSM files**
and serves InfluxQL directly. influx2tsdb instead translates every request into SQL
and stores rows in PostgreSQL/TimescaleDB. That translation and a relational storage
engine cost throughput and latency: **for pure time-series ingest and query load,
native InfluxDB is faster than this proxy**. The gap grows on large aggregate scans
and high-cardinality workloads.

The trade-off is that time-series data becomes ordinary database data:

- **ACID transactions and consistency.** Points land in the same transactional store
  as relational data; a write batch commits or fails atomically, and time-series can
  be joined with business tables in one query.
- **SQL ecosystem.** TimescaleDB exposes hypertables as tables: joins, views, CTEs,
  window functions, ORMs and BI tools all work. InfluxQL is only the ingestion/query
  front end, not a walled garden.
- **Mature backup, PITR, replication and HA.** `pg_dump`/pgBackRest, WAL archiving,
  streaming replicas, Patroni and the rest of PostgreSQL operations apply unchanged —
  you do not run a second storage system and a second DR process.
- **One set of operations, auth and audit.** Users, roles, TLS, monitoring and
  compliance controls are PostgreSQL's; there is no second database to harden.
- **Flexible lifecycle.** Retention and compression are native TimescaleDB policies
  (below), and you can still `UPDATE`/`DELETE` arbitrary rows, migrate schemas and
  reshape tables like any SQL data.
- **Reuse of existing PostgreSQL infrastructure and DBA skills.** If PostgreSQL is
  already operated professionally, the marginal cost of a time-series workload is
  low.

Pick the [InfluxDB addon](./influxdb/) when InfluxQL/TSM performance and a faithful
InfluxDB 1.x behavior model matter most; pick influx2tsdb when keeping everything in
PostgreSQL is the priority.

## Prerequisites

- A PostgreSQL database with the **TimescaleDB** extension. The proxy tries
  `CREATE EXTENSION IF NOT EXISTS timescaledb` on startup, but the extension package
  must be installed in the instance. On a pgcli instance:

  ```bash
  pg extension install timescaledb -i tsdb --auto-restart
  ```

- The target must be running before the proxy starts. A local target is checked up
  front (`pg start -i tsdb`); the proxy exits immediately if PostgreSQL is
  unreachable.

## Install

Local target — the DSN is derived from the pgcli instance on every start, so a
changed port or password stays in sync:

```bash
pg addon install influx2tsdb --pg-name tsdb
```

```
✓ influx2tsdb "influx2tsdb" installed and running
  Version:   1.3.2
  Image:     ghcr.io/mars-base/pgcli/pgcli-influx2tsdb:1.3.2
  Container: pgcli-influx2tsdb-influx2tsdb
  Target:    instance tsdb
  InfluxDB database: tsdb_db
  Endpoint:  http://<host>:8087 (InfluxDB 1.x HTTP API; binds all interfaces, no auth)
  Pool:      10 connections
```

Remote or unmanaged target — pass the full DSN:

```bash
pg addon install influx2tsdb --dsn 'postgres://user:pass@db.example.com:5432/metrics?sslmode=disable' \
  --database metrics --port 8087
```

The DSN (including its password) is stored in `pg.yaml` and is part of the container
command line, so it is visible via `podman inspect`; protect the config file and
prefer a dedicated least-privilege PostgreSQL role.

Second instances take the next free port from `influx2tsdb_start_port` (default
`8087`):

```bash
pg addon install influx2tsdb --name metrics2 --pg-name tsdb2
```

Re-running `install` without `--force` is a no-op. `--force` rewrites the target/port
settings and recreates the container; data is untouched because the proxy has none.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `influx2tsdb` | Instance name (config map key) |
| `--pg-name` | — | Resolve the target from a locally managed pgcli instance; mutually exclusive with `--dsn` |
| `--dsn` | — | Explicit PostgreSQL/TimescaleDB DSN for a remote or unmanaged server |
| `--database` | target DB name | Default InfluxDB database (the proxy's `-db`); becomes a PostgreSQL schema |
| `--port` | auto (base 8087) | InfluxDB-protocol HTTP host port; `0` = auto-assign |
| `--pool-size` | `10` | PostgreSQL connection pool size (`-pool`) |
| `--verbose` | false | Log translated SQL and query details (`-verbose`) |
| `--version` | `1.3.2` | Pinned image release |
| `--force` | false | Rewrite the settings and recreate the stateless container |

There is no `--data-dir` and no `--listen`: the proxy keeps no local state, uses host
networking, and always binds all interfaces.

## Data model

Each InfluxDB **database** maps to a PostgreSQL **schema** inside the target database:

- `CREATE DATABASE "demo"` → `CREATE SCHEMA "demo"`
- a **measurement** becomes a TimescaleDB **hypertable** in that schema
- **tags** become text columns; **fields** become typed columns; `time` is the
  hypertable's partition column
- proxy metadata lives in the target's `public` schema: `_influx_databases`,
  `_influx_meta`, `_retention_policy`

Because the data is plain PostgreSQL, you can inspect and join it with `pg exec`:

```bash
pg exec -i tsdb 'SELECT * FROM "tsdb_db"."cpu" ORDER BY time DESC LIMIT 5'
pg exec -i tsdb "SELECT time_bucket('1 minute', time) AS m, avg(value) FROM \"tsdb_db\".\"cpu\" GROUP BY 1 ORDER BY 1 DESC LIMIT 5"
```

## Write and query

The API follows InfluxDB 1.x. Writes return `204`; malformed line protocol returns
`400`.

```bash
URL=http://127.0.0.1:8087
DB=tsdb_db

# single point
curl -sS -o /dev/null -w '%{http_code}\n' -XPOST "$URL/write?db=$DB" \
  --data-binary 'cpu,host=web01 value=0.42 1700000000000000000'

# batch
curl -sS -o /dev/null -w '%{http_code}\n' -XPOST "$URL/write?db=$DB" --data-binary '
cpu,host=a value=10.5,ok=true,state="up" 1700000000000000000
cpu,host=b value=20.5,ok=false,state="down" 1700000060000000000'

# InfluxQL
curl -sS -G "$URL/query" --data-urlencode "db=$DB" \
  --data-urlencode 'q=SELECT * FROM "cpu" ORDER BY time DESC LIMIT 5'
curl -sS -G "$URL/query" --data-urlencode "db=$DB" \
  --data-urlencode 'q=SELECT mean("value") FROM "cpu" WHERE time > now() - 1h GROUP BY time(5m), "host"'
```

Common statements supported by the current release:

| Category | Statements |
|---|---|
| Database | `CREATE DATABASE`, `DROP DATABASE`, `SHOW DATABASES` |
| Retention policy | `CREATE / ALTER / DROP / SHOW RETENTION POLICIES` |
| Write / delete | line protocol writes, `DELETE FROM`, `DROP MEASUREMENT` |
| Metadata | `SHOW MEASUREMENTS / TAG KEYS / TAG VALUES / FIELD KEYS` |
| Query | `SELECT` with tag/time filters, `mean/sum/min/max/count`, `first/last`, `GROUP BY time(...)` and tags, subqueries |

### Time precision caveats

- **Timestamps are always nanoseconds.** In E2E testing the current release ignored
  the `precision=` query parameter on `/write`; send nanosecond timestamps (the
  InfluxDB default) to avoid shifted data.
- **Use `'YYYY-MM-DD HH:MM:SS'` time literals.** In E2E testing, RFC3339 literals such
  as `'2023-11-14T22:13:20Z'` in `WHERE time ...` silently returned empty results;
  the space-separated form worked:

  ```bash
  curl -sS -G "$URL/query" --data-urlencode "db=$DB" \
    --data-urlencode "q=SELECT * FROM \"cpu\" WHERE time >= '2023-11-14 22:13:20' AND time <= '2023-11-14 22:15:20'"
  ```

- `now() - 1d`-style expressions work.

### Known upstream issue: `DROP MEASUREMENT` metadata cache

After `DROP MEASUREMENT`, the proxy deletes the measurement's rows in `_influx_meta`
but keeps its in-memory schema cache. Re-creating the **same** measurement name then
does not reinsert metadata, so `SHOW MEASUREMENTS` / `SHOW TAG KEYS` /
`SHOW FIELD KEYS` can go empty for it while `SELECT` still works. Use unique
measurement names in automation, or restart the proxy after dropping and recreating a
measurement. The bundled test script does the former.

## Retention policies, compression and TimescaleDB jobs

`CREATE / ALTER / DROP RETENTION POLICY` is supported with InfluxDB semantics
(per-database, one default policy, minimum duration `15m`, `INF`/`0s` = infinite).
At the storage layer the proxy immediately calls TimescaleDB's own policy functions
for every hypertable in that database's schema:

```sql
SELECT add_retention_policy('"tsdb_db"."cpu"', INTERVAL '86400 seconds',
                            schedule_interval => INTERVAL '1 hour');
SELECT add_compression_policy('"tsdb_db"."cpu"', INTERVAL '6 hours',
                              schedule_interval => INTERVAL '1 hour');
SELECT set_chunk_time_interval('"tsdb_db"."cpu"', INTERVAL '1 hour');
```

**The cleanup itself is automatic and needs no `pg_cron`.** The jobs run inside
TimescaleDB's own background-job scheduler and are visible in
`timescaledb_information.jobs`:

```bash
pg exec -i tsdb "
  SELECT job_id, application_name, schedule_interval, proc_name,
         hypertable_schema, hypertable_name, config
  FROM timescaledb_information.jobs
  WHERE hypertable_schema = 'tsdb_db' ORDER BY job_id"
```

A 1-day policy produces a job like:

```text
 job_id |  application_name        | schedule_interval |    proc_name     | config
--------+--------------------------+-------------------+------------------+-----------------------------------------
   1050 | Retention Policy [1050]  | 01:00:00          | policy_retention | {"drop_after": "24:00:00", ...}
   1051 | Columnstore Policy [1051]| 01:00:00          | policy_compression | {"compress_after": "06:00:00", ...}
```

Operational details:

- **The proxy is not the scheduler.** TimescaleDB executes the jobs; stopping or
  removing the proxy container does not stop retention while PostgreSQL is running.
  Conversely, if the proxy is down when a *new* hypertable is created, its policies
  are applied on the next sync.
- **Sync cadence.** `CREATE / ALTER / DROP RETENTION POLICY` triggers an immediate
  sync; a background loop re-applies policies to all hypertables every **5 minutes**
  (for new tables and for DDL skipped on a lock timeout).
- **Job cadence.** The proxy requests a schedule interval: retention every 5 minutes
  when the duration is under 24h, otherwise hourly; compression every minute under
  24h, otherwise hourly. Note the upstream 1.3.2 quirk: the interval is chosen by
  scanning policies across **all databases in map order**, so with multiple databases
  the effective interval can differ from the derivation (a 1h retention policy was
  observed scheduled hourly). Read `schedule_interval` from
  `timescaledb_information.jobs` when exact cadence matters; every sync re-applies
  the derived interval.
- **Only the default policy is enforced.** All hypertables in the schema use the
  database's default retention duration (or the first finite policy if none is
  default). Non-default policies are metadata; the proxy does not split data into
  per-policy schemas or apply measurement-specific retention. With no finite policy
  (or an `INF` default) data grows without bound.
- **Deletion is chunk-granular.** TimescaleDB drops whole chunks, so expired data may
  linger for up to one chunk interval; steady-state size is roughly "retention
  duration + at most one chunk interval" and stops growing after that. The proxy
  derives the chunk interval from `SHARD DURATION` or from the retention duration
  (1h minimum, retention/24, capped at 1 day).
- **Changing a policy to `INF`** removes the TimescaleDB retention job at the next
  sync; data then stops expiring.
- **Compression is applied automatically.** Tags become `compress_segmentby`, time
  descending becomes `compress_orderby`, and `compress_after` is derived from the
  retention duration (retention/4, capped at 1 day; `15m` minimum).

Verify from PostgreSQL:

```bash
# active policies and last run
pg exec -i tsdb "SELECT application_name, schedule_interval, last_run_started_at, last_successful_finish
                 FROM timescaledb_information.job_stats s
                 JOIN timescaledb_information.jobs j USING (job_id)
                 WHERE proc_name IN ('policy_retention','policy_compression')"
```

## Security and exposure

- The proxy has **no credentials**; anyone who can reach the port can read and write
  every database exposed by the target DSN.
- Bind it to a trusted network only, or put the [nginx addon](./nginx/) in front for
  TLS and IP/basic-auth restrictions. The proxy port itself has no TLS.
- The PostgreSQL password appears in `pg.yaml` and in the container's argv. Use a
  dedicated role limited to the target database, and protect the config file.

## Lifecycle and autostart

```bash
pg addon start influx2tsdb --name influx2tsdb
pg addon stop influx2tsdb --name influx2tsdb
pg addon influx2tsdb status --name influx2tsdb
pg logs addon influx2tsdb --name influx2tsdb -f
pg addon remove influx2tsdb --name influx2tsdb          # container + pg.yaml entry
```

`remove` never deletes data: it lives in PostgreSQL. `--clean-data` is accepted for
consistency but has nothing to delete.

Autostart recreates the stateless proxy at boot. For a local target, pgcli starts the
instance first and then re-derives the DSN:

```bash
pg autostart enable --influx2tsdb --name influx2tsdb
pg autostart status
pg autostart disable --influx2tsdb --name influx2tsdb
```

## Backup, restore and HA

There is nothing to back up on the proxy side. Use PostgreSQL's normal tooling:

- `pg backup <instance>` / pgBackRest / `pg_dump` — all schemas, hypertables and the
  `public._influx_*` metadata tables are included.
- PITR / WAL archiving / streaming replicas / Patroni — work unchanged. On a failover
  or move, point the proxy at the new primary (`--dsn`) and recreate it, or, for a
  local instance, restart it after the instance's DSN changes.

The proxy container is disposable: `pg addon install influx2tsdb --force` rebuilds it
from the same target.

## Test script

`test/addon/test_influx2tsdb.sh` exercises the common time-series operations: ping,
single/batch writes with tags and field types, count, time-range and tag filters,
`mean/sum/min/max`, `GROUP BY time()` and tags, `first/last`, `SHOW` metadata,
optional PostgreSQL row-count/hypertable checks, `DELETE`, `DROP MEASUREMENT`,
database lifecycle, retention-policy CRUD, and malformed-write handling.

```bash
# local target; discovers URL and database from pg addon influx2tsdb status
test/addon/test_influx2tsdb.sh --instance tsdb

# explicit endpoint
test/addon/test_influx2tsdb.sh --url http://127.0.0.1:8087 --db tsdb_db

# keep the test measurements/database for inspection
test/addon/test_influx2tsdb.sh --instance tsdb --keep
```

The script uses unique per-run measurement and database names because of the
`DROP MEASUREMENT` metadata-cache issue described above.

`test/addon/test_influx2tsdb_retention.sh` focuses on retention automation and
covers:

- the automatic `policy_retention` / `policy_compression` jobs, with `drop_after` /
  `compress_after` matching the derived values;
- `CALL run_job(<id>)` actually dropping expired chunks while recent data survives;
- expired rows inserted directly into PostgreSQL while the proxy is stopped still
  being deleted (proving the schedule does not depend on the proxy);
- switching the default policy to `INF` removing the retention job while the
  compression job stays and falls back to 1 day;
- `DROP DATABASE` cleaning up all jobs for that schema;
- asserting that `pg_cron` is not installed.

```bash
# local target; discovers endpoint, database and instance from
# pg addon influx2tsdb status
test/addon/test_influx2tsdb_retention.sh

# explicit endpoint
test/addon/test_influx2tsdb_retention.sh --instance tsdb \
  --url http://127.0.0.1:8087 --db tsdb_db

# skip the proxy-stop phase (jobs/config only)
test/addon/test_influx2tsdb_retention.sh --skip-proxy-stop
```

The script reaches PostgreSQL through `pg exec`, so it needs `--instance` or
`--pg-dsn`, and it stops and restarts the local proxy during the run (unless
`--skip-proxy-stop`).

## Limitations

- **Linux/amd64 only.**
- **No authentication and no TLS** in the proxy; front it with nginx or isolate it.
- **InfluxQL subset.** The release implements the statements listed above, not the
  full InfluxQL language; unsupported constructs return an error or an empty result.
  Flux is not available.
- **Timestamps in nanoseconds**; the `precision=` write parameter is ignored by the
  current release, and RFC3339 time literals in queries returned empty in E2E testing.
- **Per-database retention only.** One default policy applies to every hypertable in
  the schema.
- **Performance is below native InfluxDB** for time-series workloads (see above).
- The proxy stores its metadata tables (`_influx_databases`, `_influx_meta`,
  `_retention_policy`) in the target database's `public` schema.

## CLI flag reference

### `pg addon install influx2tsdb`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `influx2tsdb` | Instance name |
| `--pg-name NAME` | — | Local pgcli instance as the target; exclusive with `--dsn` |
| `--dsn DSN` | — | `postgres://user:pass@host:port/db` target; exclusive with `--pg-name` |
| `--database DB` | target DB name | Default InfluxDB database (`-db`) |
| `--port N` | auto (base `8087`) | HTTP host port; `0` = auto-assign |
| `--pool-size N` | `10` | PostgreSQL pool size |
| `--verbose` | false | Log translated SQL |
| `--version V` | `1.3.2` | Pinned image release |
| `--force` | false | Rewrite settings and recreate the container |

### `pg addon start|stop|remove influx2tsdb`

| Command | Flags | Description |
|---|---|---|
| `pg addon start influx2tsdb` | `--name NAME` | Start (recreate) the container; checks a local target first |
| `pg addon stop influx2tsdb` | `--name NAME` | Stop the container |
| `pg addon remove influx2tsdb` | `--name NAME`, `--clean-data` | Remove container and config entry; data stays in PostgreSQL |

### `pg addon influx2tsdb status`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `influx2tsdb` | Instance to inspect (state, ping, target, database, port) |

### `pg logs addon influx2tsdb`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `influx2tsdb` | Instance whose container logs to show |
| `-n N`, `--tail N` | `50` | Number of lines |
| `-f`, `--follow` | false | Follow |

### `pg autostart enable|disable`

| Flag | Description |
|---|---|
| `--influx2tsdb` | Select the proxy add-on |
| `--name NAME` | Instance to toggle (default `influx2tsdb`) |

### Global flags

| Flag | Description |
|---|---|
| `-c, --config FILE` | Config file (default `~/.pgcli/pg.yaml`) |
| `-o, --output FILE` | Output config path |
| `-i, --instance NAME` | PostgreSQL instance selector — not used here; use `--name` |
