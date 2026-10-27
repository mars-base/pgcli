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

### 1. 创建实例

```bash
pg create -i tsdb
pg start -i tsdb
```

`pg create` 写入配置并打印管理员密码；`pg start` 启动 PostgreSQL 和备份容器。
默认数据库为 `tsdb_db`。

### 2. 安装扩展

```bash
pg extension install timescaledb -i tsdb --auto-restart
```

pgcli 构建扩展镜像，预加载 `timescaledb`，重启 PostgreSQL，并在 `tsdb_db` 中
执行 `CREATE EXTENSION IF NOT EXISTS timescaledb`。

### 3. 创建 hypertable

连接实例：

```bash
pg psql -i tsdb
```

创建普通表并转换为 hypertable：

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision
);

SELECT create_hypertable('sensor', by_range('time', INTERVAL '1 day'));
CREATE INDEX ON sensor (sensor_id, time DESC);
```

也可以一步创建；此时使用默认 chunk 间隔，之后仍可调整：

```sql
CREATE TABLE sensor (
    time      timestamptz NOT NULL,
    sensor_id text NOT NULL,
    value     double precision
) WITH (tsdb.hypertable);

SELECT set_chunk_time_interval('sensor', INTERVAL '1 day');
```

chunk 间隔按查询和保留窗口选择；后续调整只影响新 chunk。

### 4. 写入数据

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

### 5. 日常运维

追加数据、查看 chunk 和后台任务、启用自动维护：

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

常用运维命令：

```bash
pg status -i tsdb               # 实例与备份状态
pg logs -i tsdb -n 100          # 查看日志尾部
pg export -i tsdb -o sensor.dump
```

压缩、保留策略、连续聚合和 chunk 的完整操作见下文对应章节。

## InfluxDB 兼容 API：influx2tsdb 插件

如果现有工具只支持 InfluxDB 协议，可用
[influx2tsdb 插件](../../addon/influx2tsdb/) 在 TimescaleDB 之上运行一个受管的
无状态代理，提供 InfluxDB 1.x HTTP API（Line Protocol 写入与 InfluxQL 查询）：

- 每个 InfluxDB **数据库**对应一个 PostgreSQL **schema**，每个 **measurement**
  对应一个 **hypertable**，`time` 为分区列；
- `CREATE / ALTER / DROP RETENTION POLICY` 会被翻译成 TimescaleDB 自带的
  `add_retention_policy` / `add_compression_policy` 任务——不需要 `pg_cron`，
  且代理停机期间 chunk 清理照常进行；
- 代理本身不存数据，备份、PITR、复制与 HA 仍然完全走 PostgreSQL。

```bash
pg addon install influx2tsdb --pg-name tsdb
```

代价是吞吐：请求需要翻译成 SQL，纯时序写入/查询性能低于原生 InfluxDB。代理
没有认证和 TLS，需要 [nginx](../../addon/nginx/) 前置或做网络隔离。参数、数据
模型、保留语义和测试脚本见 [插件文档](../../addon/influx2tsdb/)。

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

这些任务由 TimescaleDB 自带的后台调度器执行，不需要 `pg_cron`。
[influx2tsdb 插件](../../addon/influx2tsdb/) 使用同一机制：其
`CREATE / ALTER / DROP RETENTION POLICY` 会被自动翻译成这些任务，代理停机期间
清理照常进行。

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

### Chunk 间隔

创建时指定间隔，或之后调整（只影响新 chunk）：

```sql
SELECT create_hypertable('sensor', by_range('time', INTERVAL '1 day'));

-- 已有 chunk 保持原范围；新 chunk 使用 6 小时
SELECT set_chunk_time_interval('sensor', INTERVAL '6 hours');

SELECT time_interval
FROM timescaledb_information.dimensions
WHERE hypertable_name = 'sensor';
```

`timestamptz` 列的 chunk 边界按 UTC 对齐。间隔要保证单个 chunk 能较轻松地放进
内存；间隔过小会产生大量 chunk，并增加查询规划开销。

### 查看 chunk

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

`older_than` / `newer_than` 按 chunk 的时间范围过滤，而不是按创建时间。

### 查看 chunk 大小

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

### 拆分 chunk

在 chunk 的时间范围内选一个时间点进行拆分；chunk 名称来自
`show_chunks('sensor')`：

```sql
CALL split_chunk('_timescaledb_internal._hyper_1_2_chunk',
                 '2026-10-26 12:00:00+00'::timestamptz);
```

`split_chunk` 是 procedure，必须用 `CALL`，不能用 `SELECT`。

### 删除 chunk

```sql
-- 完全早于 90 天、或晚于明天的 chunk
SELECT count(*) FROM drop_chunks('sensor', older_than => INTERVAL '90 days');
SELECT count(*) FROM drop_chunks('sensor', newer_than => now() + INTERVAL '1 day');
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

### 手动转换列存

配置好压缩参数后（见上文「压缩与列存」），可以不等策略而手动转换单个 chunk：

```sql
CALL convert_to_columnstore('_timescaledb_internal._hyper_1_1_chunk');
SELECT chunk_name, is_compressed
FROM timescaledb_information.chunks
WHERE hypertable_name = 'sensor'
ORDER BY range_start DESC;

CALL convert_to_rowstore('_timescaledb_internal._hyper_1_1_chunk');
```

这两个 procedure 接收 chunk 名称，而不是 hypertable 名称。

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
| `show_chunks('t' [, older_than/newer_than => ...])` / `drop_chunks('t', ...)` | 查看、过滤和删除 chunk | ✅ |
| `hypertable_size('t')` / `hypertable_detailed_size('t')` | hypertable 总大小及明细 | ✅ |
| `add_dimension('t', by_hash('col', N))` | 添加空间分区 | ✅ |
| `add_retention_policy(...)` / `remove_retention_policy(...)` | 自动过期清理 | ✅ TSL |
| `CALL add_columnstore_policy(...)` | 列存/压缩策略 | ✅ TSL |
| `CALL convert_to_columnstore(chunk)` / `CALL convert_to_rowstore(chunk)` | 手动转换 chunk 存储格式 | ✅ TSL |
| `hypertable_columnstore_stats('t')` / `chunk_columnstore_stats('t')` | 列存状态与空间统计 | ✅ TSL |
| `CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous)` | 连续聚合 | ✅ TSL |
| `add_continuous_aggregate_policy(...)` | 连续聚合的自动刷新策略 | ✅ TSL |
| `time_bucket_gapfill(...)` | 补齐缺失的时间桶 | ✅ TSL |
