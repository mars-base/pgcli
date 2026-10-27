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

Create a normal table and convert it to a hypertable:

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision
);

SELECT create_hypertable('sensor', by_range('time'));
```

Recent versions also accept the one-step form:

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision
) WITH (tsdb.hypertable);
```

Choose an explicit chunk interval when the default (7 days) does not fit the
query or retention window:

```sql
SELECT create_hypertable('sensor', by_range('time', INTERVAL '1 day'));
-- Change it later; existing chunks keep their current interval
SELECT set_chunk_time_interval('sensor', INTERVAL '1 day');
```

Insert sample data and aggregate it into time buckets:

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

Indexes are declared on the hypertable and propagated to chunks:

```sql
CREATE INDEX ON sensor (sensor_id, time DESC);
```

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

List chunks and their time ranges:

```sql
SELECT show_chunks('sensor');

SELECT chunk_name, range_start, range_end
FROM timescaledb_information.chunks
ORDER BY range_start DESC
LIMIT 10;
```

Expire old data by dropping whole chunks:

```sql
SELECT count(*) FROM drop_chunks('sensor', older_than => INTERVAL '90 days');

SELECT pg_size_pretty(hypertable_size('sensor'));
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
| `show_chunks('t')` / `drop_chunks('t', older_than => ...)` | Inspect and drop chunks | ✅ |
| `hypertable_size('t')` | Total size of a hypertable | ✅ |
| `add_dimension('t', by_hash('col', N))` | Add space partitioning | ✅ |
| `add_retention_policy(...)` / `remove_retention_policy(...)` | Automatic chunk expiration | ✅ TSL |
| `CALL add_columnstore_policy(...)` | Columnstore/compression policy | ✅ TSL |
| `hypertable_columnstore_stats('t')` / `chunk_columnstore_stats('t')` | Columnstore statistics | ✅ TSL |
| `CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)` | Continuous aggregates | ✅ TSL |
| `add_continuous_aggregate_policy(...)` | Continuous aggregate refresh policy | ✅ TSL |
| `time_bucket_gapfill(...)` | Fill missing buckets | ✅ TSL |
