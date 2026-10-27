---
title: "TimescaleDB"
description: "Use TimescaleDB hypertables and time_bucket analytics for time-series data inside PostgreSQL"
weight: 40
icon: fa-solid fa-chart-line
aliases: ["/toolbox/timescaledb/"]
menus:
  main:
    identifier: docs-toolbox-timescaledb
    parent: docs-toolbox
    weight: 40
    params:
      icon: fa-solid fa-chart-line
cascade:
  type: docs
  footer_style: slim
---

TimescaleDB is a PostgreSQL extension for time-series workloads. It turns a
regular table into a **hypertable**: rows are partitioned into time-based
chunks automatically, while applications keep using ordinary SQL, indexes and
transactions.

> pgcli installs the Pigsty **TSL build** of TimescaleDB 2.31.0. Hypertables,
> `time_bucket`, chunk management, columnstore/compression, continuous
> aggregates and retention policies are available. The TSL permits free
> internal use but prohibits offering a time-series database as a service.

## Use cases

TimescaleDB fits append-heavy data where time is the main access dimension:

- **IoT and device telemetry** — per-device readings, latest values and
  time-window aggregates.
- **Infrastructure and application monitoring** — CPU, memory, latency and
  request metrics for dashboards and alerting.
- **Industrial and energy metering** — high-frequency sensor streams with a
  long retention window.
- **Financial market data** — ticks, quotes and trades; OHLC-style rollups with
  `time_bucket` and `first`/`last`.
- **Product and event analytics** — user events, sessions and API calls
  aggregated over recent time windows.
- **Smart home and building automation** — sensor history and state changes.

The typical shape is append-only rows keyed by `(entity_id, time)`: queries
filter a recent time range, and old data is expired by dropping whole chunks.

It is a poor fit for OLTP workloads without a time dimension, key-value or
document storage, and blob storage. The TSL build includes compression,
continuous aggregates and retention policies, but the TSL does not allow
offering a time-series database as a service.

## Install

```bash
pg extension install timescaledb -i pg01 --auto-restart
```

pgcli builds an extension image with the Pigsty **TSL package**, puts
`timescaledb` first in `shared_preload_libraries`, restarts PostgreSQL and runs
`CREATE EXTENSION IF NOT EXISTS timescaledb` in the instance's default
database.

To use it from another database in the same instance:

```bash
pg psql -i pg01 -- -d appdb
CREATE EXTENSION IF NOT EXISTS timescaledb;
```

Check the installed version and license:

```bash
pg exec -i pg01 "SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'"
pg exec -i pg01 "SHOW timescaledb.license"
```

The Pigsty package reports `timescale`.

## Quick start

### 1. Create an instance

```bash
pg create -i tsdb
pg start -i tsdb
```

`pg create` writes the configuration and prints the admin password; `pg start`
brings up PostgreSQL and the backup container. The default database is
`tsdb_db`.

### 2. Install the extension

```bash
pg extension install timescaledb -i tsdb --auto-restart
```

pgcli builds the extension image, preloads `timescaledb`, restarts PostgreSQL
and runs `CREATE EXTENSION IF NOT EXISTS timescaledb` in `tsdb_db`.

### 3. Create a hypertable

Connect to the instance:

```bash
pg psql -i tsdb
```

Create a table and convert it to a hypertable:

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision
);

SELECT create_hypertable('sensor', by_range('time', INTERVAL '1 day'));
CREATE INDEX ON sensor (sensor_id, time DESC);
```

Or create it in one step; the default chunk interval applies and can be changed
afterwards:

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision
) WITH (tsdb.hypertable);

SELECT set_chunk_time_interval('sensor', INTERVAL '1 day');
```

Choose the chunk interval to fit the query and retention window. Existing
chunks keep their interval when you change it later.

### 4. Write data

```sql
INSERT INTO sensor
SELECT now() - (i * INTERVAL '1 minute'),
       'sensor_' || (i % 3 + 1),
       random() * 100
FROM generate_series(1, 1440) AS i;

SELECT time_bucket('1 hour', time) AS bucket,
       sensor_id,
       round(avg(value)::numeric, 2) AS avg_value
FROM sensor
WHERE time > now() - INTERVAL '24 hours'
GROUP BY bucket, sensor_id
ORDER BY bucket DESC, sensor_id;
```

### 5. Daily operations

Append new rows, inspect chunks and jobs, and enable automatic maintenance:

```sql
INSERT INTO sensor VALUES (now(), 'sensor_1', 42.0);

SELECT show_chunks('sensor');
SELECT * FROM timescaledb_information.jobs;

ALTER TABLE sensor SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'sensor_id',
    timescaledb.compress_orderby = 'time DESC'
);
CALL add_columnstore_policy('sensor', INTERVAL '7 days');
SELECT add_retention_policy('sensor', INTERVAL '365 days');
```

Operational commands:

```bash
pg status -i tsdb               # instance and backup status
pg logs -i tsdb -n 100          # server log tail
pg export -i tsdb -o sensor.dump
```

See [Compression and columnstore](#compression-and-columnstore), [Retention
policies](#retention-policies), [Continuous aggregates](#continuous-aggregates)
and [Managing chunks](#managing-chunks) for the full set.

## InfluxDB-compatible API: the influx2tsdb addon

If existing tooling speaks InfluxDB rather than SQL, the
[influx2tsdb addon](../../addon/influx2tsdb/) runs a managed, stateless proxy that
serves the InfluxDB 1.x HTTP API (Line Protocol writes and InfluxQL queries) on top
of a TimescaleDB database:

- each InfluxDB **database** becomes a PostgreSQL **schema**, and each
  **measurement** becomes a **hypertable** with `time` as the partition column;
- `CREATE / ALTER / DROP RETENTION POLICY` is translated into TimescaleDB's own
  `add_retention_policy` / `add_compression_policy` jobs — no `pg_cron` is needed,
  and chunk cleanup continues while the proxy is stopped;
- the proxy holds no data, so backup, PITR, replication and HA remain ordinary
  PostgreSQL operations.

```bash
pg addon install influx2tsdb --pg-name tsdb
```

The trade-off is throughput: every request is translated to SQL, so pure
time-series ingest and query load is slower than native InfluxDB. The proxy has no
authentication or TLS; front it with [nginx](../../addon/nginx/) or isolate the
port. See the [addon page](../../addon/influx2tsdb/) for flags, the data model,
retention semantics and the test scripts.

## Query patterns

First and last value per series:

```sql
SELECT sensor_id,
       first(value, time) AS first_value,
       last(value, time) AS last_value
FROM sensor
GROUP BY sensor_id;
```

Latest row per series:

```sql
SELECT DISTINCT ON (sensor_id) sensor_id, time, value
FROM sensor
ORDER BY sensor_id, time DESC;
```

Daily buckets in a specific timezone:

```sql
SELECT time_bucket('1 day', time, 'Asia/Shanghai') AS day,
       count(*)
FROM sensor
GROUP BY day
ORDER BY day DESC;
```

Fill gaps in a bucket sequence:

```sql
SELECT time_bucket_gapfill('1 hour', time) AS bucket,
       avg(value)
FROM sensor
WHERE time BETWEEN now() - INTERVAL '6 hours' AND now()
GROUP BY bucket
ORDER BY bucket;
```

`time_bucket_gapfill` needs an explicit start and finish, either as the second
and third arguments or in the `WHERE` clause.

## Compression and columnstore

Older chunks can be moved to the columnstore, which compresses them and speeds
up analytical scans:

```sql
ALTER TABLE sensor SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'sensor_id',
    timescaledb.compress_orderby = 'time DESC'
);

-- Compress chunks older than 7 days; runs as a background job
CALL add_columnstore_policy('sensor', INTERVAL '7 days');
```

Check the compression status and the space used:

```sql
SELECT * FROM hypertable_columnstore_stats('sensor');
SELECT * FROM chunk_columnstore_stats('sensor');
```

## Retention policies

Expire chunks automatically once they fall outside the retention window:

```sql
SELECT add_retention_policy('sensor', INTERVAL '365 days');

-- Inspect or remove the policy
SELECT * FROM timescaledb_information.jobs
WHERE proc_name = 'policy_retention';

SELECT remove_retention_policy('sensor');
```

These jobs run in TimescaleDB's own background scheduler; `pg_cron` is not required.
The [influx2tsdb addon](../../addon/influx2tsdb/) uses the same mechanism: its
`CREATE / ALTER / DROP RETENTION POLICY` statements are translated into these jobs
automatically, and cleanup continues while the proxy is stopped.

## Continuous aggregates

Pre-aggregate recent data and keep the rollup refreshed in the background:

```sql
CREATE MATERIALIZED VIEW sensor_hourly
WITH (timescaledb.continuous) AS
SELECT time_bucket('1 hour', time) AS bucket,
       sensor_id,
       avg(value) AS avg_value
FROM sensor
GROUP BY bucket, sensor_id;

SELECT add_continuous_aggregate_policy('sensor_hourly',
    start_offset      => INTERVAL '3 hours',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '1 hour');
```

Query the rollup like a normal table:

```sql
SELECT * FROM sensor_hourly
ORDER BY bucket DESC
LIMIT 10;
```

The policy refreshes only the recent window between `start_offset` and
`end_offset`; refresh older ranges manually after a backfill.

## Managing chunks

### Chunk intervals

Set the interval at creation, or change it later for new chunks:

```sql
SELECT create_hypertable('sensor', by_range('time', INTERVAL '1 day'));

-- Existing chunks keep their ranges; new chunks use 6 hours
SELECT set_chunk_time_interval('sensor', INTERVAL '6 hours');

SELECT time_interval
FROM timescaledb_information.dimensions
WHERE hypertable_name = 'sensor';
```

For `timestamptz` columns, chunk boundaries align to UTC. Choose an interval
large enough that a chunk fits comfortably in memory; a very small interval
creates many chunks and adds planning overhead.

### List chunks

```sql
SELECT show_chunks('sensor');
SELECT show_chunks('sensor', older_than => INTERVAL '30 days');
SELECT show_chunks('sensor', newer_than => INTERVAL '1 day');

SELECT chunk_name, range_start, range_end
FROM timescaledb_information.chunks
WHERE hypertable_name = 'sensor'
ORDER BY range_start DESC
LIMIT 10;
```

`older_than` and `newer_than` filter by the chunk's time range, not by its
creation time.

### Chunk sizes

```sql
SELECT chunk_name,
       pg_size_pretty(pg_total_relation_size(
           format('%I.%I', chunk_schema, chunk_name)::regclass)) AS total_size
FROM timescaledb_information.chunks
WHERE hypertable_name = 'sensor'
ORDER BY range_start DESC;

SELECT pg_size_pretty(hypertable_size('sensor'));
SELECT * FROM hypertable_detailed_size('sensor');
```

### Split a chunk

Split one chunk at a timestamp inside its range; take the chunk name from
`show_chunks('sensor')`:

```sql
CALL split_chunk('_timescaledb_internal._hyper_1_2_chunk',
                 '2026-10-26 12:00:00+00'::timestamptz);
```

`split_chunk` is a procedure; use `CALL`, not `SELECT`.

### Remove chunks

```sql
-- Chunks entirely older than 90 days, or newer than tomorrow
SELECT count(*) FROM drop_chunks('sensor', older_than => INTERVAL '90 days');
SELECT count(*) FROM drop_chunks('sensor', newer_than => now() + INTERVAL '1 day');
```

`drop_chunks` removes files instead of producing dead tuples. Avoid deleting
large time ranges with `DELETE`. For automatic expiration prefer a retention
policy; `pg_cron` is only needed for a custom schedule:

```bash
pg extension install pg_cron -i pg01 --auto-restart
```

```sql
SELECT cron.schedule('drop-sensor-chunks', '0 3 * * *',
  $$SELECT drop_chunks('sensor', older_than => INTERVAL '90 days')$$);
```

### Manual columnstore conversion

With compression settings configured (see [Compression and
columnstore](#compression-and-columnstore)), convert a single chunk without
waiting for the policy:

```sql
CALL convert_to_columnstore('_timescaledb_internal._hyper_1_1_chunk');
SELECT chunk_name, is_compressed
FROM timescaledb_information.chunks
WHERE hypertable_name = 'sensor'
ORDER BY range_start DESC;

CALL convert_to_rowstore('_timescaledb_internal._hyper_1_1_chunk');
```

Both procedures take a chunk name, not the hypertable name.

## Metadata views

```sql
SELECT * FROM timescaledb_information.hypertables;
SELECT * FROM timescaledb_information.dimensions;
SELECT * FROM timescaledb_information.chunks;
SELECT * FROM timescaledb_information.jobs;
```

`jobs` also lists the columnstore, retention and continuous-aggregate policies
created above, alongside the internal job-history task.

## Feature availability

| Feature | Status |
|---|---|
| Hypertables, `time_bucket`, `first`/`last`, `show_chunks`, `drop_chunks`, `hypertable_size`, metadata views | ✅ Apache |
| Space partitioning with `add_dimension` / `by_hash` | ✅ Apache |
| Columnstore and compression (`ALTER TABLE ... SET (timescaledb.compress)`, `CALL add_columnstore_policy`) | ✅ TSL |
| Continuous aggregates (`CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)`) | ✅ TSL |
| Retention policies (`add_retention_policy`) | ✅ TSL |
| `time_bucket_gapfill` | ✅ TSL |

Rows marked ✅ Apache follow Apache-2.0. Rows marked ✅ TSL come from the
Timescale License: free for internal use, but offering a time-series database
as a service is not permitted.

## Backup and restore

Logical backups and `pg export`/`pg import` work as usual. A physical restore
(pgBackRest, used by `pg backup`/`pg restore`) also needs TimescaleDB's restore
hooks:

```sql
SELECT timescaledb_pre_restore();   -- before replacing the data files, when possible
-- restore the instance
SELECT timescaledb_post_restore();  -- after the restored instance starts
```

`pg restore` does not run these automatically, so run `timescaledb_post_restore()`
after the restore completes. Test the procedure before relying on it.

## Permissions

`CREATE EXTENSION` requires a superuser; `pg exec` connects as the instance
admin. The owner of a table can convert it with `create_hypertable` and manage
its chunks. Application roles need only ordinary table privileges.

## Troubleshooting

**`extension "timescaledb" is not available`.** The package is missing from the
instance image, or the extension was not created in the target database. Run
`pg extension install timescaledb -i <instance>` and create it in that database.

**`timescaledb is not preloaded`.** The extension is in
`shared_preload_libraries` but PostgreSQL was not restarted. `pg extension
install` handles this; verify with `SHOW shared_preload_libraries;` and restart
the instance.

**License errors for a function.** The instance image still contains the
Apache build (an older pgcli release or a manually installed PGDG package).
Reinstall with `pg extension install timescaledb -i <instance> --auto-restart`
to switch to the Pigsty TSL package.

**`cannot create a unique index without the column "time"`.** Every unique
index and primary key on a hypertable must include the partitioning column:

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision,
    PRIMARY KEY (sensor_id, time)
);
```

**Too many chunks.** Lower the chunk interval with
`set_chunk_time_interval`; existing chunks are not rewritten.

**Old data does not disappear.** Add a retention policy with
`add_retention_policy`, or run `drop_chunks` manually. Check
`timescaledb_information.jobs` for the policy job.

## Function reference

| Function | Purpose | Status |
|---|---|---|
| `create_hypertable('t', by_range('time'))` | Convert a table to a hypertable | ✅ |
| `by_range('time', INTERVAL '1 day')` | Time partitioning with a chunk interval | ✅ |
| `set_chunk_time_interval('t', INTERVAL ...)` | Change the interval for new chunks | ✅ |
| `time_bucket('1 hour', time [, timezone])` | Time-based aggregation buckets | ✅ |
| `first(value, time)` / `last(value, time)` | First/last value in a group | ✅ |
| `show_chunks('t' [, older_than/newer_than => ...])` / `drop_chunks('t', ...)` | Inspect, filter and drop chunks | ✅ |
| `hypertable_size('t')` / `hypertable_detailed_size('t')` | Hypertable size and breakdown | ✅ |
| `add_dimension('t', by_hash('col', N))` | Add space partitioning | ✅ |
| `add_retention_policy(...)` / `remove_retention_policy(...)` | Automatic chunk expiration | ✅ TSL |
| `CALL add_columnstore_policy(...)` | Columnstore/compression policy | ✅ TSL |
| `CALL convert_to_columnstore(chunk)` / `CALL convert_to_rowstore(chunk)` | Manual chunk storage conversion | ✅ TSL |
| `hypertable_columnstore_stats('t')` / `chunk_columnstore_stats('t')` | Columnstore statistics | ✅ TSL |
| `CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)` | Continuous aggregates | ✅ TSL |
| `add_continuous_aggregate_policy(...)` | Continuous aggregate refresh policy | ✅ TSL |
| `time_bucket_gapfill(...)` | Fill missing buckets | ✅ TSL |
