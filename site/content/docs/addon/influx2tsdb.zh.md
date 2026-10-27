---
title: "influx2tsdb"
description: "以受管容器方式运行 influx2tsdb 代理——在 PostgreSQL + TimescaleDB 之上提供 InfluxDB 1.x HTTP API（Line Protocol + InfluxQL），保留与压缩由 TimescaleDB 自动调度"
weight: 63
---

`pg addon install influx2tsdb` 以受管容器方式运行 **influx2tsdb-proxy 1.3.2**——
一个 InfluxDB 1.x HTTP API 适配器（Line Protocol 写入、InfluxQL 查询），数据落到
启用了 TimescaleDB 扩展的 PostgreSQL。镜像由 pgcli 自建：
`ghcr.io/mars-base/pgcli/pgcli-influx2tsdb`。与 [InfluxDB 插件](./influxdb/) 不同，
这里没有 TSM 引擎：每个数据点都存进 TimescaleDB hypertable，代理本身**无状态**。

本插件**仅支持 Linux/amd64**（镜像为单架构，基于上游发行版二进制 + `debian:12-slim`）。
代理容器可以随时重建，数据因存放在 PostgreSQL 中而不会丢失。

> **HTTP API 没有认证。** 上游代理只提供明文 HTTP、绑定所有网卡，且不校验
> InfluxDB 凭据。需要 TLS 和访问控制时用 [nginx 插件](./nginx/) 前置，或直接
> 防火墙隔离端口。不可在不可信网络上使用。

## 适用场景与取舍

原生 InfluxDB 1.x 使用自己的**内存索引 + TSM 文件**，直接服务 InfluxQL。
influx2tsdb 则把每个请求翻译成 SQL，把数据行存进 PostgreSQL/TimescaleDB。这层
翻译加上关系型存储引擎会带来吞吐和延迟开销：**纯时序写入/查询负载下，原生
InfluxDB 比本代理更快**，差距在大范围聚合扫描和高基数场景下更明显。

换来的好处是时序数据变成了普通的数据库数据：

- **ACID 事务与一致性。** 数据点与关系数据处于同一个事务存储中；批量写入要么
  整体提交、要么失败，时序数据可以与业务表在一条 SQL 里 join。
- **SQL 生态。** TimescaleDB 把 hypertable 暴露成普通表：join、视图、CTE、窗口
  函数、ORM 和 BI 工具都能用。InfluxQL 只是写入/查询入口，不是封闭的孤岛。
- **成熟的备份、PITR、复制与 HA。** `pg_dump`/pgBackRest、WAL 归档、流复制、
  Patroni 等 PostgreSQL 运维手段原样适用——不需要再维护第二套存储和灾备流程。
- **统一的运维、认证与审计。** 用户、角色、TLS、监控和合规控制都复用
  PostgreSQL，不必额外加固一个数据库。
- **灵活的数据生命周期。** 保留与压缩是 TimescaleDB 原生策略（见下文），同时
  仍可对任意行 `UPDATE`/`DELETE`、做 schema 迁移和表结构重塑。
- **复用现有 PostgreSQL 基础设施与 DBA 技能。** 如果 PostgreSQL 本来就有专业
  运维，新增一个时序负载的边际成本很低。

如果最看重 InfluxQL/TSM 性能和忠实还原 InfluxDB 1.x 行为，选
[InfluxDB 插件](./influxdb/)；如果首要目标是把一切留在 PostgreSQL 里，选
influx2tsdb。

## 前置条件

- 目标 PostgreSQL 数据库已安装 **TimescaleDB** 扩展。代理启动时会尝试
  `CREATE EXTENSION IF NOT EXISTS timescaledb`，但实例中必须有可安装的扩展包。
  pgcli 实例上执行：

  ```bash
  pg extension install timescaledb -i tsdb --auto-restart
  ```

- 目标必须在代理启动前处于运行状态。本地目标会预先检查
  （`pg start -i tsdb`）；PostgreSQL 不可达时代理会立即退出。

## 安装

本地目标——DSN 在每次启动时从 pgcli 实例重新推导，端口和密码变化会自动同步：

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

远程或非受管目标——传完整 DSN：

```bash
pg addon install influx2tsdb --dsn 'postgres://user:pass@db.example.com:5432/metrics?sslmode=disable' \
  --database metrics --port 8087
```

DSN（含密码）会存入 `pg.yaml`，同时出现在容器命令行里，可通过
`podman inspect` 看到；请保护配置文件，并使用只授权目标数据库的最小权限角色。

第二个实例会从 `influx2tsdb_start_port`（默认 `8087`）起自动分配下一个空闲端口：

```bash
pg addon install influx2tsdb --name metrics2 --pg-name tsdb
```

不带 `--force` 重复 `install` 是幂等空操作。`--force` 会重写目标/端口设置并重建
容器；代理无数据，因此不影响任何已存数据。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `influx2tsdb` | 实例名（配置 map 键） |
| `--pg-name` | — | 使用本地受管 pgcli 实例作为目标；与 `--dsn` 互斥 |
| `--dsn` | — | 远程/非受管 PostgreSQL/TimescaleDB 的完整 DSN |
| `--database` | 目标库名 | 默认 InfluxDB 数据库（代理 `-db`）；对应一个 PostgreSQL schema |
| `--port` | 自动（基址 8087） | InfluxDB 协议 HTTP 宿主端口；`0` = 自动分配 |
| `--pool-size` | `10` | PostgreSQL 连接池大小（`-pool`） |
| `--verbose` | false | 输出翻译后的 SQL 和查询细节（`-verbose`） |
| `--version` | `1.3.2` | 固定的镜像发行版 |
| `--force` | false | 重写设置并重建无状态容器 |

没有 `--data-dir` 和 `--listen`：代理不保留本地状态，使用 host 网络，且始终
绑定所有网卡。

## 数据模型

每个 InfluxDB **数据库**对应目标库中的一个 PostgreSQL **schema**：

- `CREATE DATABASE "demo"` → `CREATE SCHEMA "demo"`
- **measurement** 变成该 schema 下的 TimescaleDB **hypertable**
- **tag** 变成文本列；**field** 变成带类型的列；`time` 是 hypertable 的分区列
- 代理元数据位于目标库的 `public` schema：`_influx_databases`、`_influx_meta`、
  `_retention_policy`

数据就是普通 PostgreSQL 数据，可以直接用 `pg exec` 查看和 join：

```bash
pg exec -i tsdb 'SELECT * FROM "tsdb_db"."cpu" ORDER BY time DESC LIMIT 5'
pg exec -i tsdb "SELECT time_bucket('1 minute', time) AS m, avg(value) FROM \"tsdb_db\".\"cpu\" GROUP BY 1 ORDER BY 1 DESC LIMIT 5"
```

## 写入与查询

API 遵循 InfluxDB 1.x：写入成功返回 `204`，非法 line protocol 返回 `400`。

```bash
URL=http://127.0.0.1:8087
DB=tsdb_db

# 单点写入
curl -sS -o /dev/null -w '%{http_code}\n' -XPOST "$URL/write?db=$DB" \
  --data-binary 'cpu,host=web01 value=0.42 1700000000000000000'

# 批量写入
curl -sS -o /dev/null -w '%{http_code}\n' -XPOST "$URL/write?db=$DB" --data-binary '
cpu,host=a value=10.5,ok=true,state="up" 1700000000000000000
cpu,host=b value=20.5,ok=false,state="down" 1700000060000000000'

# InfluxQL
curl -sS -G "$URL/query" --data-urlencode "db=$DB" \
  --data-urlencode 'q=SELECT * FROM "cpu" ORDER BY time DESC LIMIT 5'
curl -sS -G "$URL/query" --data-urlencode "db=$DB" \
  --data-urlencode 'q=SELECT mean("value") FROM "cpu" WHERE time > now() - 1h GROUP BY time(5m), "host"'
```

当前版本支持的常用语句：

| 类别 | 语句 |
|---|---|
| 数据库 | `CREATE DATABASE`、`DROP DATABASE`、`SHOW DATABASES` |
| 保留策略 | `CREATE / ALTER / DROP / SHOW RETENTION POLICIES` |
| 写入/删除 | line protocol 写入、`DELETE FROM`、`DROP MEASUREMENT` |
| 元数据 | `SHOW MEASUREMENTS / TAG KEYS / TAG VALUES / FIELD KEYS`（后三者需带 `FROM <measurement>`） |
| 查询 | 带 tag/time 过滤的 `SELECT`、`mean/sum/min/max/count`、`first/last`、`GROUP BY time(...)` 与 tag、子查询 |

### 使用 InfluxDB 1.x 命令行客户端

官方 `influx` CLI（1.13.1）可以直接操作代理。pgcli 通过一次性容器运行它，
remote 模式指向代理即可（代理无认证，无需用户名密码）：

```bash
CLI="pg influx --host 127.0.0.1 --port 8087"
```

数据库与保留策略：

```bash
$CLI -- -execute "SHOW DATABASES"
$CLI -- -execute "CREATE DATABASE demo"
$CLI --database demo -- -execute \
  "CREATE RETENTION POLICY rp1 ON demo DURATION 1d REPLICATION 1"
$CLI --database demo -- -execute \
  "ALTER RETENTION POLICY rp1 ON demo DURATION 2d DEFAULT"
$CLI --database demo -- -execute "SHOW RETENTION POLICIES ON demo"
# 默认策略不能直接删除：先把另一个策略设为 DEFAULT 再删
$CLI --database demo -- -execute \
  "ALTER RETENTION POLICY autogen ON demo DURATION INF REPLICATION 1 DEFAULT"
$CLI --database demo -- -execute "DROP RETENTION POLICY rp1 ON demo"
$CLI -- -execute "DROP DATABASE demo"
```

写入（line protocol）：

```bash
$CLI --database tsdb_db -- -execute \
  'INSERT cpu,host=web01 value=42,ok=true 1700000000000000000'
$CLI --database tsdb_db -- -execute \
  'INSERT cpu,host=web02,region=us value=7.5 1700000060000000000'
```

查询与元数据：

```bash
$CLI --database tsdb_db -- -execute \
  "SELECT * FROM cpu WHERE host = 'web01' AND time >= '2023-11-14 22:13:20'"
$CLI --database tsdb_db -- -execute \
  'SELECT mean(value) FROM cpu GROUP BY time(1m),host'
$CLI --database tsdb_db -- -execute 'SHOW MEASUREMENTS'
$CLI --database tsdb_db -- -execute 'SHOW TAG KEYS FROM cpu'
$CLI --database tsdb_db -- -execute 'SHOW TAG VALUES FROM cpu WITH KEY = host'
$CLI --database tsdb_db -- -execute 'SHOW FIELD KEYS FROM cpu'
```

删除与输出格式：

```bash
$CLI --database tsdb_db -- -execute "DELETE FROM cpu WHERE host = 'web01'"
$CLI --database tsdb_db -- -execute 'DROP MEASUREMENT cpu'
$CLI --database tsdb_db -- -format json -execute 'SELECT * FROM cpu'
$CLI --database tsdb_db -- -format csv  -execute 'SELECT * FROM cpu'
$CLI --database tsdb_db -- -precision ms -execute 'SELECT * FROM cpu'
```

批量导入（influx 1.x import 格式）：

```bash
cat >/tmp/influx-import.txt <<'DATA'
# DDL
CREATE DATABASE demo

# DML
# CONTEXT-DATABASE: demo
# CONTEXT-RETENTION-POLICY: autogen
weather,location=us-midwest temperature=82 1700000000000000000
weather,location=us-midwest temperature=83 1700000060000000000
DATA

$CLI -- -import -path /tmp/influx-import.txt
```

实测可用：数据库与保留策略 DDL、`INSERT`（line protocol）、`SELECT`（含
`GROUP BY time(...)`、tag/time 过滤和 `-format column/csv/json`）、
`SHOW MEASUREMENTS`、带 `FROM` 的 `SHOW TAG KEYS / FIELD KEYS / TAG VALUES`、
`DELETE`、`DROP MEASUREMENT` 以及 `-import`。

与原生 InfluxDB 的差异：

- 元数据命令必须带 `FROM <measurement>`：`SHOW TAG KEYS`、`SHOW FIELD KEYS`
  和 `SHOW TAG VALUES` 不带 `FROM` 会返回 `missing FROM clause`（原生
  InfluxDB 允许省略）；`WITH KEY =~ /regex/` 也不支持。
- 不支持 `SHOW SERIES`（返回 `unsupported query: SHOW SERIES`）。
- 默认保留策略不能直接 `DROP`（与原生 InfluxDB 一致），先把另一个策略设为
  `DEFAULT`。
- `-precision` 的 `ns/u/ms/s/m/h` 都生效；`rfc3339` 不生效，仍输出纳秒整数。

### 时间精度注意事项

- **时间戳一律按纳秒解释。** 在 E2E 实测中，当前版本忽略 `/write` 的
  `precision=` 查询参数；请发送纳秒时间戳（InfluxDB 默认精度），避免数据时间
  偏移。
- **时间字面量两种写法都可用。** 实测中，`WHERE time ...` 既接受
  `'YYYY-MM-DD HH:MM:SS'`，也接受 RFC3339（如 `'2023-11-14T22:13:20Z'`，含
  毫秒或 `+08:00` 时区偏移）；Grafana 经 `$timeFilter` 插值出的 epoch 毫秒
  形式（如 `1793083539315ms`）同样可用：

  ```bash
  curl -sS -G "$URL/query" --data-urlencode "db=$DB" \
    --data-urlencode "q=SELECT * FROM \"cpu\" WHERE time >= '2023-11-14 22:13:20' AND time <= '2023-11-14 22:15:20'"
  ```

- `now() - 1d` 这类表达式可用。

### 已知上游问题：`DROP MEASUREMENT` 元数据缓存

执行 `DROP MEASUREMENT` 后，代理会删除 `_influx_meta` 中该 measurement 的行，
但保留内存中的 schema 缓存。之后用**相同名字**重新创建 measurement 时，元数据
不会被重新插入，`SHOW MEASUREMENTS` / `SHOW TAG KEYS` / `SHOW FIELD KEYS` 可能
为空，而 `SELECT` 仍然可用。自动化中请使用每次唯一的 measurement 名，或在删除
并重建后重启代理。仓库自带测试脚本采用前者。

## Grafana 集成

Grafana 内置的 **InfluxDB** 数据源可以直接接入本代理（已在 Grafana 13.2.4 上
端到端验证）：数据源类型选 **InfluxDB**，Query Language 选 **InfluxQL**
（不是 Flux），URL 指向代理，即可查询底层 TimescaleDB 中的 hypertable。

| 字段 | 值 |
|---|---|
| Type | `InfluxDB` |
| Query Language | `InfluxQL` |
| URL | `http://<proxy-host>:8087` |
| Database | `tsdb_db`（或你的默认库） |
| User / Password | 留空 |
| HTTP Method | `GET`（默认） |

数据源的 “Save & test” 健康检查可用；Grafana 查询编辑器和面板生成的请求
（`$timeFilter`、`epoch=ms`、`fill(null)`、`GROUP BY time`）已实测返回正确的
时间序列，`SHOW MEASUREMENTS / TAG KEYS / TAG VALUES / FIELD KEYS` 也受支持，
因此查询构建器里的 measurement/tag/field 下拉框可正常使用。

注意事项：

- 代理**无认证、无 TLS**；Grafana 与代理不在可信网络时用
  [nginx 插件](./nginx/) 前置。
- 只支持 InfluxQL 子集；不支持的表达式可能返回错误或空结果，也不支持 Flux
  （Flux 依赖 InfluxDB v2 API，本代理不提供）。
- 代理忽略写入的 `precision=` 参数；Grafana 用 `epoch=ms` 读取，展示不受
  影响。

### 通过 Grafana HTTP API 集成

自动化场景不必点 Web UI：Grafana 的 HTTP API 可以直接创建数据源，并验证
“代理写入 → Grafana API 读取”的完整链路（以下命令已在 Grafana 13.2.4 上
实测）。

```bash
GRAFANA_PW=$(pg addon password grafana)   # Grafana admin 密码
G=http://127.0.0.1:3000

# 1. 创建 InfluxDB/InfluxQL 数据源
curl -sS -u "admin:$GRAFANA_PW" \
  -H 'Content-Type: application/json' \
  -XPOST "$G/api/datasources" -d '{
    "name": "influx2tsdb",
    "type": "influxdb",
    "access": "proxy",
    "url": "http://127.0.0.1:8087",
    "database": "tsdb_db",
    "user": "",
    "password": "",
    "isDefault": true,
    "jsonData": {"httpMode": "GET", "version": "InfluxQL"}
  }'

# 按名字取回 uid（也可直接读上一步响应里的 datasource.uid）
UID=$(curl -sS -u "admin:$GRAFANA_PW" \
  "$G/api/datasources/name/influx2tsdb" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["uid"])')

# 2. 健康检查
curl -sS -u "admin:$GRAFANA_PW" \
  "$G/api/datasources/uid/$UID/health"
# {"message":"datasource is working. 0 measurements found","status":"OK"}

# 3. 通过代理写入一个点（纳秒时间戳）
curl -sS -o /dev/null -w '%{http_code}\n' \
  -XPOST "http://127.0.0.1:8087/write?db=tsdb_db" \
  --data-binary "cpu,host=api value=42 $(date +%s%N)"

# 4. 经 Grafana 的 datasource proxy 读取（InfluxQL）
curl -sS -u "admin:$GRAFANA_PW" -G \
  "$G/api/datasources/proxy/uid/$UID/query" \
  --data-urlencode 'db=tsdb_db' \
  --data-urlencode 'epoch=ms' \
  --data-urlencode 'q=SELECT last("value") FROM "cpu" WHERE time >= now() - 1h'
```

写入返回 `204`；第 4 步返回该 measurement 的 `last(value)`，`time` 列为
epoch 毫秒（与 `epoch=ms` 一致）。

要点：

- `access: "proxy"` 让 Grafana 服务端转发查询，浏览器不直连代理，也不涉及
  CORS。
- `jsonData` 中的 `version: "InfluxQL"` 和 `httpMode: "GET"` 与面板查询使用
  的模式一致，创建时显式给出。
- 删除数据源：
  `curl -u "admin:$GRAFANA_PW" -X DELETE "$G/api/datasources/uid/$UID"`
  （返回 `200`）。

## 保留策略、压缩与 TimescaleDB 任务

支持 InfluxDB 语义的 `CREATE / ALTER / DROP RETENTION POLICY`（按数据库隔离、
只有一个默认策略、最短 `15m`、`INF`/`0s` 表示无限）。在存储层，代理会立即对该
数据库 schema 中的每个 hypertable 调用 TimescaleDB 自带的策略函数：

```sql
SELECT add_retention_policy('"tsdb_db"."cpu"', INTERVAL '86400 seconds',
                            schedule_interval => INTERVAL '1 hour');
SELECT add_compression_policy('"tsdb_db"."cpu"', INTERVAL '6 hours',
                              schedule_interval => INTERVAL '1 hour');
SELECT set_chunk_time_interval('"tsdb_db"."cpu"', INTERVAL '1 hour');
```

**清理本身是自动的，不需要 `pg_cron`。** 任务运行在 TimescaleDB 自带的后台
job scheduler 中，可在 `timescaledb_information.jobs` 查看：

```bash
pg exec -i tsdb "
  SELECT job_id, application_name, schedule_interval, proc_name,
         hypertable_schema, hypertable_name, config
  FROM timescaledb_information.jobs
  WHERE hypertable_schema = 'tsdb_db' ORDER BY job_id"
```

一个 1 天保留策略会产生类似这样的任务：

```text
 job_id |  application_name        | schedule_interval |    proc_name     | config
--------+--------------------------+-------------------+------------------+-----------------------------------------
   1050 | Retention Policy [1050]  | 01:00:00          | policy_retention | {"drop_after": "24:00:00", ...}
   1051 | Columnstore Policy [1051]| 01:00:00          | policy_compression | {"compress_after": "06:00:00", ...}
```

运维要点：

- **调度器不是代理。** 真正执行任务的是 TimescaleDB；只要 PostgreSQL 在运行，
  停止或删除代理容器并不会停止数据过期清理。反之，代理停机期间新建的
  hypertable 会在下一次同步时补上策略。
- **同步节奏。** `CREATE / ALTER / DROP RETENTION POLICY` 会立即触发一次同步；
  另有后台循环每 **5 分钟**对所有 hypertable 重新下发策略（覆盖新表，以及因锁
  超时被跳过的 DDL）。
- **任务节奏。** 代理会按推导值设置调度间隔：保留策略小于 24h 时请求每 5 分钟、
  否则每小时；压缩小于 24h 时请求每分钟、否则每小时。但上游 1.3.2 存在已知
  quirk：选择间隔时会遍历**所有数据库**的策略，且结果受 map 遍历顺序影响，多库
  部署下实际间隔可能与推导值不同（实测 1h 保留策略的 retention 任务被排成
  1 小时）。需要精确节奏时以 `timescaledb_information.jobs.schedule_interval`
  为准；代理每次同步都会重新下发推导出的间隔。
- **只有默认策略会真正生效。** schema 内所有 hypertable 都使用该数据库默认策略
  的时长（若没有默认策略，则取第一个有限时长的策略）。非默认策略只是元数据；
  代理不会按策略拆分 schema，也不会按 measurement 单独保留。没有有限时长策略
  （或默认策略为 `INF`）时数据会持续增长。
- **删除以 chunk 为粒度。** TimescaleDB 整块删除 chunk，因此过期数据最多会多留
  一个 chunk 间隔，稳态数据量约为「保留时长 + 最多一个 chunk 间隔」，之后不再
  无限增长。chunk 间隔来自 `SHARD DURATION` 或按保留时长推导（最小 1h，
  retention/24，上限 1 天）。
- **把策略改成 `INF`** 会在下次同步时移除 TimescaleDB 保留任务，数据不再过期。
- **压缩自动启用。** tag 作为 `compress_segmentby`，时间倒序作为
  `compress_orderby`；`compress_after` 由保留时长推导（retention/4，上限 1 天，
  最小 `15m`）。

在 PostgreSQL 侧核查：

```bash
# 当前策略及最近一次执行
pg exec -i tsdb "SELECT application_name, schedule_interval, last_run_started_at, last_successful_finish
                 FROM timescaledb_information.job_stats s
                 JOIN timescaledb_information.jobs j USING (job_id)
                 WHERE proc_name IN ('policy_retention','policy_compression')"
```

## 安全与暴露面

- 代理**没有任何凭据**；能连到端口的人就能读写目标 DSN 暴露的所有数据库。
- 只绑可信网络，或用 [nginx 插件](./nginx/) 前置做 TLS 与 IP/Basic Auth 限制。
  代理端口本身没有 TLS。
- PostgreSQL 密码会出现在 `pg.yaml` 和容器 argv 中。请使用仅授权目标库的专用
  角色，并保护配置文件。

## 生命周期与开机自启

```bash
pg addon start influx2tsdb --name influx2tsdb
pg addon stop influx2tsdb --name influx2tsdb
pg addon influx2tsdb status --name influx2tsdb
pg logs addon influx2tsdb --name influx2tsdb -f
pg addon remove influx2tsdb --name influx2tsdb          # 容器 + pg.yaml 条目
```

`remove` 从不删除数据：数据在 PostgreSQL 里。`--clean-data` 为与其他插件保持
一致而接受，但没有可删除的内容。

开机自启会重建无状态代理。本地目标会先启动实例，再重新推导 DSN：

```bash
pg autostart enable --influx2tsdb --name influx2tsdb
pg autostart status
pg autostart disable --influx2tsdb --name influx2tsdb
```

## 备份、恢复与 HA

代理侧没有需要备份的内容，直接使用 PostgreSQL 的常规工具：

- `pg backup <instance>` / pgBackRest / `pg_dump`——所有 schema、hypertable 以及
  `public._influx_*` 元数据表都会包含在内。
- PITR / WAL 归档 / 流复制 / Patroni——原样适用。故障切换或迁移后，把代理指向
  新主库（`--dsn`）并重建；本地实例则在实例 DSN 变化后重启代理。

代理容器是一次性的：`pg addon install influx2tsdb --force` 用同一目标重建即可。

## 测试脚本

`test/addon/test_influx2tsdb.sh` 覆盖常用时序操作：ping、单点/批量写入（含 tag
和多种 field 类型）、count、时间范围与 tag 过滤、`mean/sum/min/max`、
`GROUP BY time()` 与 tag、`first/last`、`SHOW` 元数据、可选的 PostgreSQL
行数/hypertable 检查、`DELETE`、`DROP MEASUREMENT`、数据库生命周期、保留策略
CRUD，以及非法写入的处理。

```bash
# 本地目标；从 pg addon influx2tsdb status 自动发现 URL 和数据库
test/addon/test_influx2tsdb.sh --instance tsdb

# 显式端点
test/addon/test_influx2tsdb.sh --url http://127.0.0.1:8087 --db tsdb_db

# 保留测试 measurement/数据库以便检查
test/addon/test_influx2tsdb.sh --instance tsdb --keep
```

由于上述 `DROP MEASUREMENT` 元数据缓存问题，脚本使用每次运行唯一的 measurement
和数据库名。

`test/addon/test_influx2tsdb_retention.sh` 专门验证保留策略的自动化，覆盖：

- 自动创建 `policy_retention` / `policy_compression` 任务，`drop_after` /
  `compress_after` 与 RP 推导一致；
- `CALL run_job(<id>)` 真正删除过期 chunk，新数据保留；
- 停掉代理后直接向 PostgreSQL 插入过期行，任务仍能删除（证明调度不依赖代理）；
- 默认 RP 切到 `INF` 后保留任务消失、压缩任务保留并回退到 1 天；
- `DROP DATABASE` 后该 schema 的任务全部清理；
- 目标库未安装 `pg_cron`。

```bash
# 本地目标；默认从 pg addon influx2tsdb status 发现端点、数据库和实例
test/addon/test_influx2tsdb_retention.sh

# 显式指定
test/addon/test_influx2tsdb_retention.sh --instance tsdb \
  --url http://127.0.0.1:8087 --db tsdb_db

# 跳过停代理阶段（只验证任务与配置）
test/addon/test_influx2tsdb_retention.sh --skip-proxy-stop
```

脚本通过 `pg exec` 访问目标 PostgreSQL，因此需要 `--instance` 或 `--pg-dsn`；
它会在运行中停止并重启本地代理（除非 `--skip-proxy-stop`）。

## 限制

- **仅 Linux/amd64。**
- **代理无认证、无 TLS**；请用 nginx 前置或做网络隔离。
- **InfluxQL 子集。** 当前版本只实现上文列出的语句，不是完整 InfluxQL；例如
  `SHOW SERIES` 不支持，`SHOW TAG KEYS / FIELD KEYS / TAG VALUES` 必须带
  `FROM`。不支持的语法会返回错误或空结果。不支持 Flux。
- **时间戳按纳秒解释**；当前版本忽略写入的 `precision=` 参数。查询输出精度由
  `epoch=ms` 控制（Grafana 即使用该参数）。
- **仅支持按数据库的保留策略。** 一个默认策略作用于 schema 内所有 hypertable。
- **时序负载性能低于原生 InfluxDB**（见上文）。
- 代理的元数据表（`_influx_databases`、`_influx_meta`、`_retention_policy`）位于
  目标库的 `public` schema。

## CLI 参数参考

### `pg addon install influx2tsdb`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `influx2tsdb` | 实例名 |
| `--pg-name NAME` | — | 本地 pgcli 实例作为目标；与 `--dsn` 互斥 |
| `--dsn DSN` | — | `postgres://user:pass@host:port/db` 目标；与 `--pg-name` 互斥 |
| `--database DB` | 目标库名 | 默认 InfluxDB 数据库（`-db`） |
| `--port N` | 自动（基址 `8087`） | HTTP 宿主端口；`0` = 自动分配 |
| `--pool-size N` | `10` | PostgreSQL 连接池大小 |
| `--verbose` | false | 输出翻译后的 SQL |
| `--version V` | `1.3.2` | 固定的镜像发行版 |
| `--force` | false | 重写设置并重建容器 |

### `pg addon start|stop|remove influx2tsdb`

| 命令 | 参数 | 说明 |
|---|---|---|
| `pg addon start influx2tsdb` | `--name NAME` | 启动（重建）容器；本地目标会先做运行检查 |
| `pg addon stop influx2tsdb` | `--name NAME` | 停止容器 |
| `pg addon remove influx2tsdb` | `--name NAME`、`--clean-data` | 删除容器与配置条目；数据留在 PostgreSQL |

### `pg addon influx2tsdb status`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `influx2tsdb` | 要查看的实例（状态、ping、目标、数据库、端口） |

### `pg logs addon influx2tsdb`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `influx2tsdb` | 要查看容器日志的实例 |
| `-n N`、`--tail N` | `50` | 行数 |
| `-f`、`--follow` | false | 持续跟踪 |

### `pg autostart enable|disable`

| 参数 | 说明 |
|---|---|
| `--influx2tsdb` | 选择该代理插件 |
| `--name NAME` | 要切换的实例（默认 `influx2tsdb`） |

### 全局参数

| 参数 | 说明 |
|---|---|
| `-c, --config FILE` | 配置文件（默认 `~/.pgcli/pg.yaml`） |
| `-o, --output FILE` | 输出配置路径 |
| `-i, --instance NAME` | PostgreSQL 实例选择器——此处不使用；请用 `--name` |
