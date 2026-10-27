---
title: "TimescaleDB"
description: "在 PostgreSQL 内使用 TimescaleDB hypertable 和 time_bucket 处理时序数据"
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

TimescaleDB 是面向时序负载的 PostgreSQL 扩展。它把普通表变成 **hypertable**：
数据按时间自动切分成 chunk，应用仍然使用普通 SQL、索引和事务。

> pgcli 安装的是 Pigsty 的 **TSL 构建**（TimescaleDB 2.31.0）。Hypertable、
> `time_bucket`、chunk 管理、列存/压缩、连续聚合和保留策略均可使用。
> TSL 允许免费内部使用，但禁止对外提供时序数据库服务。

## 使用场景

TimescaleDB 适合以时间为主要访问维度、以追加写入为主的数据：

- **IoT 与设备遥测** — 按设备采集读数、查询最新值和按时间窗口聚合。
- **基础设施与应用监控** — CPU、内存、延迟、请求量等指标，用于看板和告警。
- **工业与能源计量** — 高频传感器数据流，保留周期长。
- **金融行情数据** — tick、报价和成交；用 `time_bucket` 和 `first`/`last`
  生成 OHLC 类聚合。
- **产品与事件分析** — 用户事件、会话和 API 调用，按最近时间窗口统计。
- **智能家居与楼宇自动化** — 传感器历史数据和状态变化。

典型形态是 `(entity_id, time)` 的追加型数据：查询过滤最近一段时间，过期数据按
整个 chunk 删除。

不适合没有时间维度的 OLTP、键值/文档存储和大对象存储。TSL 构建包含压缩、
连续聚合和保留策略，但 TSL 不允许对外提供时序数据库服务。

## 安装

```bash
pg extension install timescaledb -i pg01 --auto-restart
```

pgcli 会使用 Pigsty 的 **TSL 包**构建扩展镜像，把 `timescaledb` 放到
`shared_preload_libraries` 的首位，重启 PostgreSQL，并在实例的默认数据库执行
`CREATE EXTENSION IF NOT EXISTS timescaledb`。

同一实例的其他数据库需要单独创建扩展：

```bash
pg psql -i pg01 -- -d appdb
CREATE EXTENSION IF NOT EXISTS timescaledb;
```

查看版本和许可证：

```bash
pg exec -i pg01 "SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'"
pg exec -i pg01 "SHOW timescaledb.license"
```

Pigsty 包返回 `timescale`。

## 快速开始

创建普通表并转换为 hypertable：

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision
);

SELECT create_hypertable('sensor', by_range('time'));
```

较新版本也支持一步创建：

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision
) WITH (tsdb.hypertable);
```

默认 chunk 间隔为 7 天；不满足查询或保留窗口时可以显式指定：

```sql
SELECT create_hypertable('sensor', by_range('time', INTERVAL '1 day'));
-- 后续调整；已有 chunk 保持原间隔
SELECT set_chunk_time_interval('sensor', INTERVAL '1 day');
```

写入样例数据并按时间分桶聚合：

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

索引建在 hypertable 上，会自动下推到各个 chunk：

```sql
CREATE INDEX ON sensor (sensor_id, time DESC);
```

## 常用查询

每条序列的首末值：

```sql
SELECT sensor_id,
       first(value, time) AS first_value,
       last(value, time) AS last_value
FROM sensor
GROUP BY sensor_id;
```

每条序列的最新一行：

```sql
SELECT DISTINCT ON (sensor_id) sensor_id, time, value
FROM sensor
ORDER BY sensor_id, time DESC;
```

按指定时区做天级分桶：

```sql
SELECT time_bucket('1 day', time, 'Asia/Shanghai') AS day,
       count(*)
FROM sensor
GROUP BY day
ORDER BY day DESC;
```

补齐时间桶序列中的空档：

```sql
SELECT time_bucket_gapfill('1 hour', time) AS bucket,
       avg(value)
FROM sensor
WHERE time BETWEEN now() - INTERVAL '6 hours' AND now()
GROUP BY bucket
ORDER BY bucket;
```

`time_bucket_gapfill` 需要明确的起点和终点：可以作为第二、三个参数传入，或在
`WHERE` 中给出时间范围。

## 压缩与列存

较早的 chunk 可以转入列存（columnstore），压缩数据并加速分析查询：

```sql
ALTER TABLE sensor SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'sensor_id',
    timescaledb.compress_orderby = 'time DESC'
);

-- 压缩 7 天前的 chunk，由后台任务执行
CALL add_columnstore_policy('sensor', INTERVAL '7 days');
```

查看压缩状态和空间占用：

```sql
SELECT * FROM hypertable_columnstore_stats('sensor');
SELECT * FROM chunk_columnstore_stats('sensor');
```

## 保留策略

超出保留窗口的 chunk 会自动删除：

```sql
SELECT add_retention_policy('sensor', INTERVAL '365 days');

-- 查看或移除策略
SELECT * FROM timescaledb_information.jobs
WHERE proc_name = 'policy_retention';

SELECT remove_retention_policy('sensor');
```

## 连续聚合

预先聚合最近的数据，并由后台任务持续刷新：

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

像普通表一样查询聚合结果：

```sql
SELECT * FROM sensor_hourly
ORDER BY bucket DESC
LIMIT 10;
```

策略只自动刷新 `start_offset` 到 `end_offset` 之间的近期窗口；回填更早的数据后
需要手动刷新。

## 管理 chunk

查看 chunk 及时间范围：

```sql
SELECT show_chunks('sensor');

SELECT chunk_name, range_start, range_end
FROM timescaledb_information.chunks
ORDER BY range_start DESC
LIMIT 10;
```

按整块删除过期数据：

```sql
SELECT count(*) FROM drop_chunks('sensor', older_than => INTERVAL '90 days');

SELECT pg_size_pretty(hypertable_size('sensor'));
```

`drop_chunks` 直接删除文件，不产生 dead tuple。不要用大范围 `DELETE` 清理
历史数据。自动过期优先使用保留策略；只有需要自定义调度时才安装 `pg_cron`：

```bash
pg extension install pg_cron -i pg01 --auto-restart
```

```sql
SELECT cron.schedule('drop-sensor-chunks', '0 3 * * *',
  $$SELECT drop_chunks('sensor', older_than => INTERVAL '90 days')$$);
```

## 元数据视图

```sql
SELECT * FROM timescaledb_information.hypertables;
SELECT * FROM timescaledb_information.dimensions;
SELECT * FROM timescaledb_information.chunks;
SELECT * FROM timescaledb_information.jobs;
```

`jobs` 中可以看到上面创建的列存、保留策略和连续聚合任务，以及内部的 job 历史
任务。

## 功能可用性

| 功能 | 状态 |
|---|---|
| Hypertable、`time_bucket`、`first`/`last`、`show_chunks`、`drop_chunks`、`hypertable_size`、元数据视图 | ✅ Apache |
| 用 `add_dimension` / `by_hash` 做空间分区 | ✅ Apache |
| 列存与压缩（`ALTER TABLE ... SET (timescaledb.compress)`、`CALL add_columnstore_policy`） | ✅ TSL |
| 连续聚合（`CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)`） | ✅ TSL |
| 保留策略（`add_retention_policy`） | ✅ TSL |
| `time_bucket_gapfill` | ✅ TSL |

标记为 ✅ Apache 的功能遵循 Apache-2.0；标记为 ✅ TSL 的功能来自 Timescale
License，允许免费内部使用，但禁止对外提供时序数据库服务。

## 备份与恢复

逻辑备份和 `pg export`/`pg import` 正常工作。物理恢复（`pg backup`/`pg restore`
使用的 pgBackRest）还需要 TimescaleDB 的恢复钩子：

```sql
SELECT timescaledb_pre_restore();   -- 替换数据文件之前，条件允许时执行
-- 恢复实例
SELECT timescaledb_post_restore();  -- 恢复后的实例启动之后执行
```

`pg restore` 不会自动调用这两个函数，恢复完成后需要手动执行
`timescaledb_post_restore()`。正式依赖前请先演练一次。

## 权限

`CREATE EXTENSION` 需要超级用户；`pg exec` 以实例管理员身份连接。表的所有者
可以对自己的表执行 `create_hypertable` 并管理 chunk。应用角色只需要普通的表
权限。

## 故障排查

**`extension "timescaledb" is not available`。** 实例镜像里没有扩展包，或目标
数据库没有创建扩展。执行 `pg extension install timescaledb -i <instance>`，并在
该数据库中创建扩展。

**`timescaledb is not preloaded`。** 扩展已在 `shared_preload_libraries` 中，但
PostgreSQL 没有重启。`pg extension install` 会自动处理；可用
`SHOW shared_preload_libraries;` 检查并重启实例。

**函数报许可证错误。** 实例镜像中还是 Apache 构建（旧版 pgcli 或手动安装的
PGDG 包）。执行 `pg extension install timescaledb -i <instance> --auto-restart`
切换到 Pigsty TSL 包。

**`cannot create a unique index without the column "time"`。** hypertable 上的
唯一索引和主键必须包含分区列：

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision,
    PRIMARY KEY (sensor_id, time)
);
```

**chunk 数量过多。** 用 `set_chunk_time_interval` 调大间隔；已有 chunk 不会
重写。

**旧数据没有被清理。** 用 `add_retention_policy` 添加保留策略，或手动执行
`drop_chunks`；可在 `timescaledb_information.jobs` 中检查策略任务。

## 函数参考

| 函数 | 用途 | 状态 |
|---|---|---|
| `create_hypertable('t', by_range('time'))` | 把表转换为 hypertable | ✅ |
| `by_range('time', INTERVAL '1 day')` | 指定时间分区和 chunk 间隔 | ✅ |
| `set_chunk_time_interval('t', INTERVAL ...)` | 调整新 chunk 的间隔 | ✅ |
| `time_bucket('1 hour', time [, timezone])` | 时间分桶聚合 | ✅ |
| `first(value, time)` / `last(value, time)` | 分组内首末值 | ✅ |
| `show_chunks('t')` / `drop_chunks('t', older_than => ...)` | 查看和删除 chunk | ✅ |
| `hypertable_size('t')` | 查看 hypertable 总大小 | ✅ |
| `add_dimension('t', by_hash('col', N))` | 添加空间分区 | ✅ |
| `add_retention_policy(...)` / `remove_retention_policy(...)` | 自动过期清理 | ✅ TSL |
| `CALL add_columnstore_policy(...)` | 列存/压缩策略 | ✅ TSL |
| `hypertable_columnstore_stats('t')` / `chunk_columnstore_stats('t')` | 列存状态与空间统计 | ✅ TSL |
| `CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)` | 连续聚合 | ✅ TSL |
| `add_continuous_aggregate_policy(...)` | 连续聚合的自动刷新策略 | ✅ TSL |
| `time_bucket_gapfill(...)` | 补齐缺失的时间桶 | ✅ TSL |
