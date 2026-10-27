---
title: "InfluxDB"
description: "以受管容器方式运行 InfluxDB 1.x——内置认证、配置覆盖、备份恢复，以及面向本地/远程实例的 influx CLI 透传命令"
weight: 62
---

`pg addon install influxdb` 以受管容器方式运行 **InfluxDB 1.13.1 OSS**——单节点时序数据库，TSM 存储、InfluxQL 查询——直接跑 pgcli 自建的 `ghcr.io/mars-base/pgcli/pgcli-influxdb` 镜像（官方 linux/amd64 发行版二进制 + Alpine）。与 VictoriaMetrics 一样，它是**顶层插件**，不前置任何 PostgreSQL 实例。

本插件**仅支持 Linux/amd64**（镜像为单架构）。InfluxDB 1.x **没有集群/HA**、**没有内置 Web UI**，且上游已把 v1 置于维护模式；pgcli 固定最后一个 1.13.1 发行版。新部署若需要仍在维护的引擎，可考虑 [VictoriaMetrics](./victoriametrics/) 或 [VictoriaLogs](./victorialogs/)；InfluxDB 1.x 适合需要与既有工具保持 InfluxQL 兼容的场景。

> **认证始终开启。** 安装时生成 admin 密码，存入 `pg.yaml`，可随时用
> `pg addon password influxdb [--name NAME]` 取回。HTTP API 不允许匿名访问。
> 备份/恢复 RPC 端口**设计上仅绑回环**——上游对该协议没有认证，所以无论
> `--listen` 如何设置，pgcli 都不会把它暴露到网络上。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `influxdb` | 实例名（map 键；各子命令的默认名同名） |
| `--version` | `1.13.1` | 固定的镜像发行版（决定使用哪个已发布的 `pgcli-influxdb` tag） |
| `--data-dir` | `<base_dir>/addon/influxdb/<name>/data` | 数据目录，绑定挂载到容器内 `/var/lib/influxdb`；绝对路径或相对 `base_dir` |
| `--listen` | `0.0.0.0` | HTTP 绑定地址。RPC 端点始终只绑 `127.0.0.1` |
| `--http-port` | 自动（基址 8086） | HTTP 宿主端口；自 `influxdb_start_port` 自动分配 |
| `--rpc-port` | 自动（基址 8088） | 备份/恢复 RPC 端口，仅回环；每个实例分配唯一端口 |
| `--admin-user` | `admin` | InfluxQL 管理员用户名（无 Web UI） |
| `--admin-password` | 自动生成 | 管理员密码。`--force` 时默认保留已存密码，除非显式指定 |
| `--config-option` | — | 额外 `influxdb.conf` 设置，格式 `KEY=VALUE`（顶层）或 `SECTION.KEY=VALUE`（表），值为原始 TOML（可重复）。替换已存覆盖列表并持久化 |
| `--force` | false | 重写生成的配置并重建容器，保留数据与已存管理员密码 |

生成的配置只覆盖路径、监听和认证，其余全部沿用上游 1.13.1 默认值。渲染后的文件位于 `<base_dir>/addon/influxdb/<name>/influxdb.conf`，只读挂载进容器。覆盖项**同时**存入 `pg.yaml` 和该文件，因此重启与重新渲染都不会丢失。

## 安装

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
  Password:  <自动生成>
  Data dir:  /home/you/.pgcli/addon/influxdb/metrics/data
  Config:    /home/you/.pgcli/addon/influxdb/metrics/influxdb.conf
```

第二个及之后的实例会自动从各自端口池取下一个空闲 HTTP/RPC 端口（例如 `8087`/`8089`），并可用 `--data-dir` 指向任意目录：

```bash
pg addon install influxdb --name metrics2 --data-dir /srv/influx-data --listen 127.0.0.1
```

不带 `--force` 重复 `install` 是幂等空操作，只报告当前状态。`--force` 会重写配置并重建容器，但**保留数据目录与已存管理员密码**（否则既有 admin 用户将无法登录）；由于 `--config-option` 会替换已存列表，需要保留覆盖项时必须重新传入。

## 凭据

```bash
pg addon password influxdb --name metrics   # 打印管理员密码
pg addon list                               # 显示所有实例及状态
```

密码保存在 `pg.yaml`（默认权限 `0644`——请保护该文件）。所有需要凭据的命令都通过环境变量把凭据传给容器，绝不放进 `podman` 命令行。

## 配置覆盖

```bash
# 安装时持久化覆盖项
pg addon install influxdb --name metrics --config-option 'data.index-version="tsi1"'

# 在线修改并立即生效
pg addon influxdb config set --name metrics 'data.cache-max-memory-size=1000000000' --restart
pg addon influxdb config list --name metrics
pg addon influxdb config unset --name metrics data.cache-max-memory-size
```

`config set/unset` 同时改写生成的配置文件与 `pg.yaml`；`--restart` 会重建容器使改动立即生效，不带 `--restart` 则下次启动生效。值按原始 TOML 处理：字符串要带引号（`'data.index-version="tsi1"'`），数字直接写。

上游默认索引是 `inmem`。高基数序列请设置 `data.index-version="tsi1"`——该设置只能在全新数据目录上修改，务必在写入数据前决定。

## `pg influx` CLI

`pg influx` 从同一镜像启动一个一次性容器运行 `influx` 客户端——宿主机不安装任何东西，命令结束后容器自动删除。pg 自身解析器不认识的 flag 放在 `--` 之后；参数按原样、按顺序透传。

### 本地实例

```bash
pg influx --name metrics -- -execute "SHOW DATABASES"
pg influx --name metrics --database demo -- -execute "SELECT * FROM cpu LIMIT 5"
pg influx --name metrics                      # 交互式 shell
```

主机、端口和凭据会自动注入。

### 远程 InfluxDB 1.x

```bash
# host/port flags
pg influx --host db.example.com --username admin --ssl -- -execute "SHOW DATABASES"

# DSN（userinfo、数据库与 TLS 查询参数）
pg influx --dsn 'influxdb://admin:secret@db.example.com:8086/demo' -- -execute "SHOW MEASUREMENTS"
pg influx --dsn 'https://admin:secret@db.example.com:8086/demo?unsafe_ssl=true' -- -execute "SHOW DATABASES"
```

两种模式都支持从 `INFLUX_USERNAME` / `INFLUX_PASSWORD` 回退取凭据。优先用环境变量而非 `--password`，后者会出现在进程列表中。`--ssl` 启用 HTTPS；`--unsafe-ssl` 跳过证书校验，必须与 `--ssl` 同用。`--dsn` 已携带全部连接信息，不能与 `--host/--port/--ssl/...` 组合；`--name` 不能与任何远程 flag 组合。

### 远程模式下的 import 文件

```bash
pg influx --host db.example.com --database demo -- -import -path /tmp/demo.txt
```

`-path` 指定的文件会以原绝对路径绑定挂载进一次性容器，所以 `pg addon influxdb export`（或 `influx_inspect export`）产出的文件也能导入远程服务器。

`# DML` 段和 `# CONTEXT-DATABASE:` 标记是必需的，`# DDL` 段可选。目标库可能不存在时再带上 DDL——库已存在时 `CREATE DATABASE` 是空操作。导入目标由 context 标记决定，**不是** `--database`；目标库不存在又没有 DDL 时会报 `database not found`。没有 `# DML` 头的文件会按 InfluxQL 解析并报语法错误。

`/tmp/demo.txt` 是运行 `pg` 的机器上的文件（它被挂载进容器），不是 `db.example.com` 上的文件。最简样例：

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

`# DDL` 段按 InfluxQL 在数据之前执行（`CREATE DATABASE` 对已存在的库是空操作）。`# DML` 之后全部是 line protocol；时间戳可省略（默认纳秒，`-precision` 可改变解释方式）。可用 `-pps N` 限速；文件是 gzip 压缩时加 `-compressed`。远程服务器开启认证时，用 `--username`/`--password` 或环境变量 `INFLUX_USERNAME`/`INFLUX_PASSWORD` 传凭据。

### 跨主机操作

`pg influx` 的远程模式完全在客户端机器上运行——只需要 podman 和 `pgcli-influxdb` 镜像，不需要本机安装 InfluxDB，也不需要本地实例。

```bash
# 在任何能访问 db.example.com 的机器上
export INFLUX_USERNAME=admin
export INFLUX_PASSWORD=secret

pg influx --host db.example.com --database demo -- -execute "SHOW MEASUREMENTS"
pg influx --host db.example.com --database demo -- -execute "SELECT * FROM cpu LIMIT 10"
pg influx --host db.example.com -- -execute "CREATE DATABASE demo"
pg influx --host db.example.com -- -execute "CREATE RETENTION POLICY rp_30d ON demo DURATION 30d REPLICATION 1 DEFAULT"
pg influx --host db.example.com --database demo          # 交互式 shell

# HTTPS，以及使用自签名证书的 HTTPS
pg influx --host influx.example.com --ssl --database demo -- -execute "SHOW MEASUREMENTS"
pg influx --host influx.example.com --ssl --unsafe-ssl --database demo -- -execute "SHOW MEASUREMENTS"
```

addon 的生命周期和备份命令只操作**本机**的 podman 实例，无法直接跨主机：`pg addon influxdb status|backup|restore|export|import`、`pg addon start|stop|remove` 和 `pg logs addon influxdb` 都必须在 InfluxDB 所在主机上执行——登录到该主机后直接运行。在其他机器上请使用 `pg influx --host/--dsn` 或下文介绍的 HTTP API（`/query`、`/write`）。

跨主机迁移数据用 export + import：

```bash
# 源主机：离线导出（导出期间容器会停止）
pg addon influxdb export --name metrics --database demo
# 把命令输出的文件（默认位于 <instance dir>/exports/ 下）传到目标主机，再在目标主机上导入

# 登录到目标主机：导入到本地实例
pg influx --name metrics --database demo -- -import -path /tmp/demo-export.txt

# 或者在任何持有该文件的客户端：导入远程服务器（文件会在本地被挂载进容器）
pg influx --host target.example.com --database demo -- -import -path /tmp/demo-export.txt
```

恢复使用仅回环的 RPC 端点，因此备份目录必须在目标主机上，并在**目标主机**上执行恢复：

```bash
# 目标实例是全新的、没有 demo 库：保留原库名
pg addon influxdb restore /tmp/influx-backup --name metrics --database demo

# 目标实例已存在 demo 库时，influxd restore 拒绝覆盖，改用新库名恢复
pg addon influxdb restore /tmp/influx-backup --name metrics --database demo --newdb demo_copy
```

## 常用操作

### 数据库

```bash
# pgcli 封装命令
pg addon influxdb database create demo --name metrics
pg addon influxdb database create demo --retention 30d --shard-duration 1d --rp rp_30d --name metrics
pg addon influxdb database list --name metrics
pg addon influxdb database drop demo --name metrics --yes   # --yes 跳过确认

# 等价的 InfluxQL
pg influx --name metrics -- -execute "CREATE DATABASE demo"
pg influx --name metrics -- -execute "SHOW DATABASES"
pg influx --name metrics -- -execute "DROP DATABASE demo"
```

### 保留策略（retention policy）

每个数据库至少有一个保留策略；默认的 `autogen` 永不过期。保留策略决定数据保留多久、按什么粒度分片，过期分片会被自动删除。

```bash
# pgcli 封装命令
pg addon influxdb retention list   --name metrics --database demo
pg addon influxdb retention create --name metrics --database demo --rp rp_7d --duration 7d --default
pg addon influxdb retention alter  --name metrics --database demo --rp rp_7d --duration 14d --default
pg addon influxdb retention drop   --name metrics --database demo --rp rp_7d --yes
```

等价的 InfluxQL：

```bash
# 创建保留 30 天的策略（OSS 版 REPLICATION 必须为 1）
pg influx --name metrics -- \
  -execute "CREATE RETENTION POLICY rp_30d ON demo DURATION 30d REPLICATION 1"

# 同上，并设为默认策略：写入/查询未指定 RP 时使用它
pg influx --name metrics -- \
  -execute "CREATE RETENTION POLICY rp_30d ON demo DURATION 30d REPLICATION 1 DEFAULT"

# 建库时直接指定有界默认 RP
pg influx --name metrics -- \
  -execute "CREATE DATABASE demo WITH DURATION 30d REPLICATION 1 SHARD DURATION 1d NAME rp_30d"

# 之后修改保留时长 / 默认策略
pg influx --name metrics -- \
  -execute "ALTER RETENTION POLICY rp_30d ON demo DURATION 90d DEFAULT"

# 查看与删除
pg influx --name metrics -- -execute "SHOW RETENTION POLICIES ON demo"
pg influx --name metrics -- -execute "DROP RETENTION POLICY rp_30d ON demo"

# 显式写入/查询非默认策略
pg influx --name metrics -- -execute "INSERT INTO demo.rp_30d cpu,host=server01 value=0.64"
pg influx --name metrics -- -execute "SELECT * FROM demo.rp_30d.cpu LIMIT 10"
```

- `DURATION` 支持 `h`、`d`、`w` 和 `INF`，最小值为 `1h`；`SHARD DURATION` 可选，默认由 `DURATION` 推导。
- 只能有一个 `DEFAULT` 策略；把另一个设为默认会自动降级原来的默认策略。不带 RP 前缀的查询使用默认策略。
- `DROP RETENTION POLICY` 会删除该策略的数据。过期分片由后台保留检查清理（默认每 30 分钟一次），因此磁盘占用会在下一次检查后下降。

### 交互式 shell

```bash
pg influx --name metrics --database demo
```

直接输入 InfluxQL（`SHOW MEASUREMENTS`、`SELECT ...`、`use otherdb` 等）；`exit` 或 Ctrl-D 退出。`--database` 指定初始数据库上下文。

### 查看 schema

```bash
# 所有 measurement
pg influx --name metrics --database demo -- -execute "SHOW MEASUREMENTS"
# 用正则匹配 measurement
pg influx --name metrics --database demo -- -execute "SHOW MEASUREMENTS WITH MEASUREMENT =~ /cpu.*/"
# field key 及其类型
pg influx --name metrics --database demo -- -execute "SHOW FIELD KEYS FROM cpu"
# tag key
pg influx --name metrics --database demo -- -execute "SHOW TAG KEYS FROM cpu"
# 某个 tag 的所有取值
pg influx --name metrics --database demo -- -execute 'SHOW TAG VALUES FROM cpu WITH KEY = "host"'
# 所有 series（measurement + tag 组合）
pg influx --name metrics --database demo -- -execute "SHOW SERIES FROM cpu"
```

InfluxQL 引号规则：**双引号**用于标识符（`"host"`、`"cpu"`），**单引号**用于字符串值（`'server01'`）。查询本身含双引号时，像 `SHOW TAG VALUES` 示例那样用单引号包住整个 `-execute` 参数。

### 查询数据

```bash
# 最新 10 条，时间戳可读
pg influx --name metrics --database demo -- -precision rfc3339 \
  -execute "SELECT * FROM cpu ORDER BY time DESC LIMIT 10"

# 最近一小时
pg influx --name metrics --database demo -- \
  -execute "SELECT * FROM cpu WHERE time > now() - 1h"

# 按 host 做 5 分钟平均
pg influx --name metrics --database demo -- \
  -execute "SELECT mean(value) FROM cpu WHERE time > now() - 1h GROUP BY time(5m), host"

# 行数
pg influx --name metrics --database demo -- -execute "SELECT COUNT(value) FROM cpu"

# JSON 输出，便于脚本处理
pg influx --name metrics --database demo -- -format json -pretty \
  -execute "SELECT * FROM cpu LIMIT 5"
```

### 写入与删除

```bash
# 写入一个点（line protocol；时间戳可省略，默认取服务器时间）
pg influx --name metrics --database demo -- \
  -execute "INSERT cpu,host=server01,region=us-west value=0.64"

# 删除 30 天前的数据（支持 now()）
pg influx --name metrics --database demo -- \
  -execute "DELETE FROM cpu WHERE time < now() - 30d"

# 不带 RP 前缀的 DELETE 作用于所有保留策略；加前缀可限定范围
pg influx --name metrics --database demo -- \
  -execute "DELETE FROM autogen.cpu WHERE time < now() - 30d"

# 删除整个 measurement / 指定 series
pg influx --name metrics --database demo -- -execute "DROP MEASUREMENT cpu"
pg influx --name metrics --database demo -- \
  -execute "DROP SERIES FROM cpu WHERE host = 'server01'"
```

批量写入用 `pg influx ... -- -import -path FILE`，见[导出与导入](#导出与导入)。

### 用户与权限

```bash
pg influx --name metrics -- -execute "SHOW USERS"
pg influx --name metrics -- -execute 'SHOW GRANTS FOR "admin"'
pg influx --name metrics -- -execute 'CREATE USER "readonly" WITH PASSWORD '\''change-me'\'''
pg influx --name metrics -- -execute 'GRANT READ ON demo TO "readonly"'
pg influx --name metrics -- -execute 'REVOKE ALL PRIVILEGES ON demo FROM "readonly"'
pg influx --name metrics -- -execute 'DROP USER "readonly"'
```

不要把管理员密码放进 argv（`ps` 可见）；用交互式 shell、`INFLUX_USERNAME`/`INFLUX_PASSWORD`，或 `--password ''` 交互输入。

### 日志与状态

```bash
pg addon influxdb status --name metrics        # 运行状态、端点、版本、路径
pg logs addon influxdb --name metrics          # 最近 50 行容器日志
pg logs addon influxdb --name metrics -n 200   # 最近 200 行
pg logs addon influxdb --name metrics -f       # 持续跟踪
```

## HTTP API（URL 操作）

插件在 `<listen>:<http-port>` 上提供标准 InfluxDB 1.x HTTP API，认证始终开启（HTTP Basic）。同样的调用既适用于本机（`http://127.0.0.1:8086`），也适用于远程（`http://db.example.com:8086`，或由 [nginx](./nginx/) 终止 TLS 后的 `https://influx.example.com`）。

```bash
URL=http://db.example.com:8086
AUTH='admin:secret'     # 建议用 --netrc-file：写在 -u 里的密码会出现在 ps 中

# 健康检查 —— 204，无需认证
curl -i "$URL/ping"

# 执行 InfluxQL（GET /query）
curl -s -G "$URL/query" \
  --data-urlencode 'db=demo' \
  --data-urlencode 'q=SHOW MEASUREMENTS' \
  -u "$AUTH"

# 创建数据库和保留策略
curl -s -XPOST "$URL/query" -u "$AUTH" \
  --data-urlencode 'q=CREATE DATABASE demo'
curl -s -XPOST "$URL/query" -u "$AUTH" \
  --data-urlencode 'q=CREATE RETENTION POLICY rp_30d ON demo DURATION 30d REPLICATION 1 DEFAULT'

# 写入一个点（POST /write）—— 成功返回 204
curl -s -o /dev/null -w '%{http_code}\n' -XPOST "$URL/write?db=demo&precision=s" -u "$AUTH" \
  --data-binary 'cpu,host=web01,region=us-east value=0.42 1793069000'

# 从 line protocol 文件批量写入（每行一个点）
curl -s -XPOST "$URL/write?db=demo" -u "$AUTH" --data-binary @points.txt

# 查询并美化 JSON（不带 epoch 时时间戳为 RFC3339）
curl -s -G "$URL/query" -u "$AUTH" \
  --data-urlencode 'db=demo' \
  --data-urlencode 'q=SELECT * FROM cpu ORDER BY time DESC LIMIT 10' \
  --data-urlencode 'pretty=true'

# 用 Unix 秒时间戳
curl -s -G "$URL/query" -u "$AUTH" \
  --data-urlencode 'db=demo' --data-urlencode 'epoch=s' \
  --data-urlencode 'q=SELECT * FROM cpu LIMIT 10'

# 删除数据库
curl -s -XPOST "$URL/query" -u "$AUTH" --data-urlencode 'q=DROP DATABASE demo'
```

| 参数 | 端点 | 含义 |
|---|---|---|
| `db` | `/query`、`/write` | 目标数据库（多数语句必需） |
| `rp` | `/query`、`/write` | 目标保留策略（默认用数据库的默认策略） |
| `q` | `/query` | InfluxQL 语句；需要 URL 编码（`--data-urlencode` 会处理） |
| `pretty=true` | `/query` | 缩进 JSON 响应 |
| `epoch` | `/query` | `ns`、`u`、`ms`、`s`、`m`、`h`；省略则返回 RFC3339 |
| `precision` | `/write` | 写入行协议里的时间戳精度（默认 `ns`） |

说明：

- 未认证请求返回 `401`。写入成功返回 `204`；非法数据点返回 `400` 和 `partial write` 信息，指出被丢弃的点。
- `curl -u user:pass` 会让密码出现在进程列表中。建议使用权限 0600 的 netrc 文件：
  `machine db.example.com login admin password secret`，然后 `curl --netrc-file ~/.netrc-influx ...`。
- 本机日常操作优先用 `pg influx`（自动通过环境变量传凭据）；HTTP API 适合从其他机器、脚本和应用集成。

## 备份与恢复

portable 备份是**在线**的——实例保持运行：

```bash
pg addon influxdb backup --name metrics                        # 整个实例
pg addon influxdb backup --name metrics --database demo
pg addon influxdb backup --name metrics --since 2026-10-01T00:00:00Z
pg addon influxdb backup --name metrics --start 2026-10-01T00:00:00Z --end 2026-10-02T00:00:00Z
```

默认输出目录为 `<实例目录>/backups/<UTC 时间戳>`；`--out DIR` 可覆盖。备份目录包含 `.manifest`、`.meta` 和按 shard 的 `.tar.gz` 文件。

恢复同样是**在线**的（辅助容器经回环 RPC 与运行中的服务通信；实例已停止时会先启动）。单个数据库完整的"备份 → 删除 → 恢复"流程：

```bash
# 1. 替换前先备份
pg addon influxdb backup --name metrics --database demo --out /path/to/backup-demo

# 2. 删除老库
pg addon influxdb database drop demo --name metrics --yes

# 3. 以原库名恢复
pg addon influxdb restore /path/to/backup-demo --name metrics --database demo
```

`influxd restore` 拒绝覆盖已存在的数据库。如果想在保留老库的同时另存一份，用新库名恢复——`--newdb` 必须与 `--database` 同用：

```bash
pg addon influxdb restore /path/to/backup-demo --name metrics --database demo --newdb demo_copy
```

恢复完成后实例保持运行。

## 导出与导入

`export` 是**离线**的：pgcli 先停止容器，让 `influx_inspect` 读取一致的 TSM 文件，结束后自动重启（`--no-start` 可保持停止）。

```bash
pg addon influxdb export --name metrics --database demo
pg addon influxdb export --name metrics --out /tmp/all.txt --compress --no-start
pg addon influxdb export --name metrics --start 2026-10-01T00:00:00Z --end 2026-10-02T00:00:00Z
```

默认文件为 `<实例目录>/exports/<database|all>-<UTC 时间戳>.txt`（`--compress` 时加 `.gz`）。

`import` 需要实例运行中，并接受 export 格式文件：

```bash
pg addon influxdb import /tmp/demo.txt --name metrics --database demo_copy
pg addon influxdb import /tmp/demo.txt --name metrics --precision ms
```

带 `--database` 时，pgcli 会把文件复制到实例目录下并改写 `# CONTEXT-DATABASE:` 行到目标数据库，原导出文件不受影响。导入远程服务器用 `pg influx --host ... -- -import -path FILE`（见上文）。

## 生命周期

```bash
pg addon start influxdb --name metrics     # 启动容器
pg addon stop influxdb --name metrics
pg addon influxdb status --name metrics    # 状态、ping、端点、版本
pg logs addon influxdb --name metrics -f   # 容器日志
pg addon remove influxdb --name metrics    # 删除容器与 pg.yaml 条目，保留数据
pg addon remove influxdb --name metrics --clean-data   # 连同配置/备份与数据目录一起删除
```

不带 `--clean-data` 时，数据与生成的配置在 `remove` 后仍保留，同名重装即可复活已存序列。`--clean-data` 也会删除自定义的 `--data-dir`。

开机自启：

```bash
pg autostart enable --influxdb --name metrics
pg autostart status
pg autostart disable --influxdb --name metrics
```

开机时 pgcli 只启动既有容器，不会重新执行认证 bootstrap。

## 生产注意事项

- **没有 HTTPS 监听。** InfluxDB 1.x 只提供明文 HTTP。需要 TLS 时用 [nginx 插件](./nginx/) 前置，或 `--listen 127.0.0.1` 保持本地访问。
- **RPC 仅回环。** 备份/恢复 RPC 协议没有认证；pgcli 把它固定在 `127.0.0.1`，每个实例分配唯一端口。不要对外暴露。
- **没有 HA。** InfluxDB 1.x OSS 是单节点。备份（`backup` / `export`）是唯一的灾备手段；pgcli 不做定时备份，需要时把 `pg addon influxdb backup` 接入你自己的定时任务。
- **v1 处于维护模式。** 版本固定 1.13.1；升级意味着发布新镜像 tag 并重新测试，且不存在从 v1 到 v2/v3 的原地升级路径。
- **高基数：** 在写入数据前切换到 `data.index-version="tsi1"`（见上文）。
- **平台：** 仅 Linux/amd64——镜像打包的是官方 amd64 压缩包。

## 完整 `influxdb.conf` 参数参考

pgcli 生成的配置是**最小化**的：只写入下表中的托管项，其余全部沿用 InfluxDB 1.13.1 的默认值。下表默认值来自 1.13.1 的 `influxd config` 实际输出。

| pgcli 托管键 | 渲染值 | 说明 |
|---|---|---|
| `reporting-disabled` | `true` | 关闭上游使用统计上报 |
| `bind-address`（顶层） | `"127.0.0.1:<rpc-port>"` | 备份/恢复 RPC；上游对此协议无认证，因此永不对外监听 |
| `[meta] dir` | `/var/lib/influxdb/meta` | 位于 data-dir 挂载内 |
| `[data] dir` | `/var/lib/influxdb/data` | 位于 data-dir 挂载内 |
| `[data] wal-dir` | `/var/lib/influxdb/wal` | 位于 data-dir 挂载内 |
| `[http] bind-address` | `<listen>:<http-port>` | 容器使用 host 网络，这就是宿主机实际监听地址 |
| `[http] auth-enabled` | `true` | 认证始终开启 |

这七个键会被 `--config-option` / `config set` 拒绝——请通过 `pg addon install` / `config` 的对应参数修改。其余键均可覆盖。

### 覆盖方式

```bash
# 表内键用 SECTION.KEY=VALUE，顶层键用 KEY=VALUE
pg addon influxdb config set --name metrics 'data.index-version="tsi1"' --restart
pg addon influxdb config set --name metrics 'http.max-row-limit=50000' --restart
pg addon influxdb config unset --name metrics data.index-version
```

值按**原始 TOML** 写入——字符串、时长、大小要加引号（`'cache-max-memory-size="2g"'`），数字和布尔值不加引号。限制：

- 只支持单表 `[section]`。重复表 `[[graphite]]`、`[[collectd]]`、`[[opentsdb]]`、`[[udp]]` 无法表达；嵌套键（如 `https-client-ca.paths`、`http.headers`）同样无法表达。
- 这类设置需要直接编辑 `<base_dir>/addon/influxdb/<name>/influxdb.conf` 并重启容器（`pg addon stop|start influxdb`）。普通启停不会改动文件；`install --force` 以及任何 `config set/unset` 都会重新渲染配置，手工修改会丢失。
- `--config-option` 会整体替换已保存的覆盖列表；改其中一项时要重新传全部条目。

### 顶层

| 键 | 默认值 | 说明 |
|---|---|---|
| `reporting-disabled` | `false` | pgcli 渲染为 `true`（托管） |
| `bind-address` | `"127.0.0.1:8088"` | 备份/恢复 RPC 监听地址（托管） |
| `hardening-enabled` | `false` | 限制 Flux HTTP 请求只能访问公网地址（SSRF 加固） |

### `[meta]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `dir` | `~/.influxdb/meta`（容器内 `/var/lib/influxdb/meta`） | 元数据/Raft 数据库目录（托管） |
| `retention-autocreate` | `true` | `CREATE DATABASE` 时自动创建 `autogen` 保留策略 |
| `logging-enabled` | `true` | 输出 meta 服务日志 |

### `[data]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `dir` | `~/.influxdb/data` | TSM 存储目录（托管） |
| `wal-dir` | `~/.influxdb/wal` | WAL 目录（托管） |
| `wal-fsync-delay` | `"0s"` | fsync 前的批量窗口；`0s` 每次写入都 fsync，0–100ms 对非 SSD 磁盘有帮助 |
| `index-version` | `"inmem"` | 分片索引：`inmem`（默认）或 `tsi1`（高基数；只能在全新数据目录上设置） |
| `validate-keys` | `false` | 拒绝非法 UTF-8/unicode 的 key（少量写入开销） |
| `skip-field-size-validation` | `false` | 跳过写入时的字段大小上限检查 |
| `strict-error-handling` | `false` | 返回错误而不是静默丢弃非法点（如 `SELECT INTO` 中的 ±Inf） |
| `query-log-enabled` | `true` | 执行前记录查询日志（可能包含敏感数据） |
| `cache-max-memory-size` | `"1g"` | 单分片 cache 上限，超出后拒绝写入 |
| `cache-snapshot-memory-size` | `"25m"` | 达到该大小即把 cache 快照为 TSM |
| `cache-snapshot-write-cold-duration` | `"10m"` | 多久无写入/删除后快照 cache |
| `compact-full-write-cold-duration` | `"4h"` | 多久无写入/删除后做 full compaction |
| `compact-throughput` | `"48m"` | compaction 写入限速（字节/秒） |
| `compact-throughput-burst` | `"48m"` | 允许的短时突发上限 |
| `aggressive-points-per-block` | `10000` | 激进 compaction 的每块点数 |
| `ingress-metric-by-measurement-enabled` | `false` | 按 measurement 统计写入指标 |
| `ingress-metric-by-login-enabled` | `false` | 按登录用户统计写入指标 |
| `max-series-per-database` | `1000000` | 每库 series 上限，超出后丢弃写入（`0` = 不限） |
| `max-values-per-tag` | `100000` | 每 tag 的 value 上限（`0` = 不限） |
| `max-concurrent-compactions` | `0` | 并发 full/level compaction 数；`0` = GOMAXPROCS 的 50% |
| `max-concurrent-optimized-compactions` | `0` | 并发 optimized compaction 数（`0` = 默认） |
| `max-index-log-file-size` | `"1m"` | TSI 索引 WAL 压缩进索引文件的阈值 |
| `max-concurrent-deletes` | `1` | 每分片同时执行的 `DELETE` 数 |
| `series-id-set-cache-size` | `100` | TSI series-id-set 缓存条目数（`0` 关闭） |
| `series-id-set-cache-max-size` | `0` | 自适应扩容上限（`0` = 关闭自适应） |
| `series-id-set-cache-target-hit-rate` | `0.0` | 自适应缩容的目标命中率（`0.0` = 关闭，`1.0` 非法） |
| `series-id-set-cache-shrink-conservatism` | `2.5` | 缩容策略的标准差余量 |
| `series-file-max-concurrent-snapshot-compactions` | `0` | 并发 series-file 快照压缩数（`0` = 默认） |
| `trace-logging-enabled` | `false` | TSM 引擎详细 trace 日志 |
| `tsm-use-madv-willneed` | `false` | 对 TSM 文件使用内核 `MADV_WILLNEED` 建议 |
| `tar-stream-buffer-size` | `"1m"` | 备份/改名/复制时 tar 流缓冲区大小 |

### `[coordinator]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `write-timeout` | `"10s"` | 写入等待超时 |
| `max-concurrent-queries` | `0` | 并发查询上限（`0` = 不限） |
| `query-timeout` | `"0s"` | 查询超时后被终止（`0s` = 不限） |
| `log-queries-after` | `"0s"` | 慢查询日志阈值（`0s` = 关闭） |
| `log-timedout-queries` | `false` | 记录被 `query-timeout` 杀掉的查询 |
| `max-select-point` | `0` | 单 `SELECT` 最大处理点数（`0` = 不限） |
| `max-select-series` | `0` | 单 `SELECT` 最大 series 数（`0` = 不限） |
| `max-select-buckets` | `0` | `GROUP BY time()` 的最大桶数（`0` = 不限） |
| `termination-query-log` | `false` | 收到 `SIGTERM` 时打印正在执行的查询 |

### `[flux-controller]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `query-concurrency` | `0` | 并发 Flux 查询数（`0` = 默认） |
| `query-initial-memory-bytes` | `"0"` | 每个 Flux 查询的初始内存预算 |
| `query-max-memory-bytes` | `"0"` | 每个 Flux 查询的内存上限 |
| `total-max-memory-bytes` | `"0"` | 所有 Flux 查询的总内存上限 |
| `query-queue-size` | `0` | Flux 查询队列长度 |

### `[retention]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `enabled` | `true` | 启用保留策略执行 |
| `check-interval` | `"30m"` | 保留策略检查间隔 |

### `[shard-precreation]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `enabled` | `true` | 数据到达前预创建分片 |
| `check-interval` | `"10m"` | 预创建检查间隔 |
| `advance-period` | `"30m"` | 在分片组结束前多久创建下一组 |

### `[monitor]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `store-enabled` | `true` | 记录内部统计 |
| `store-database` | `"_internal"` | 目标数据库 |
| `store-interval` | `"10s"` | 记录间隔 |

### `[http]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `enabled` | `true` | 启用 HTTP API |
| `bind-address` | `":8086"` | HTTP 监听地址（托管：`<listen>:<http-port>`） |
| `auth-enabled` | `false` | 要求认证（托管：`true`） |
| `realm` | `"InfluxDB"` | Basic-auth 质询 realm |
| `log-enabled` | `true` | HTTP 请求日志 |
| `suppress-write-log` | `false` | 从 HTTP 日志中隐藏写请求 |
| `access-log-path` | `""` | 访问日志文件；空则写 stderr |
| `access-log-status-filters` | `[]` | 状态码过滤（`NNN`、`NNX`、`NXX`）；空则全部记录 |
| `write-tracing` | `false` | 详细写日志 |
| `flux-enabled` | `false` | 启用 Flux 查询端点 |
| `flux-log-enabled` | `false` | 记录 Flux 查询 |
| `flux-testing` | `false` | 暴露 Flux 测试端点 |
| `pprof-enabled` | `true` | 启用 `/debug/pprof` |
| `pprof-auth-enabled` | `false` | pprof 要求管理员认证 |
| `debug-pprof-enabled` | `false` | 启动时额外在 `localhost:6060` 绑定 pprof |
| `ping-auth-enabled` | `false` | `/ping`、`/metrics`、`/status` 要求认证 |
| `prom-read-auth-enabled` | `false` | Prometheus remote-read API 要求认证 |
| `https-enabled` | `false` | 提供 HTTPS |
| `https-certificate` | `"/etc/ssl/influxdb.pem"` | TLS 证书（PEM） |
| `https-private-key` | `""` | TLS 私钥；空表示与证书合并存放 |
| `https-insecure-certificate` | `false` | 允许不安全的证书文件权限 |
| `https-ignore-sanity-checks` | `false` | 加载未通过服务端认证检查的证书 |
| `https-client-auth-type` | 未设置 | 双向 TLS 客户端认证模式（`NoClientCert`、`RequestClientCert`、`RequireAnyClientCert`、`VerifyClientCertIfGiven`、`RequireAndVerifyClientCert`） |
| `https-client-ca` | 未设置 | 客户端 CA：`{ paths = [...], include-system = false }` |
| `shared-secret` | `""` | JWT 校验密钥 |
| `max-row-limit` | `0` | 分块结果行数上限（`0` = 不限） |
| `max-connection-limit` | `0` | 并发 HTTP 连接上限（`0` = 不限） |
| `unix-socket-enabled` | `false` | 通过 Unix socket 提供 HTTP |
| `unix-socket-permissions` | `"0777"` | Unix socket 权限 |
| `bind-socket` | `"/var/run/influxdb.sock"` | Unix socket 路径 |
| `max-body-size` | `25000000` | 请求体最大字节数（`0` = 不限） |
| `max-concurrent-write-limit` | `0` | 并发写入上限（`0` = 不限） |
| `max-enqueued-write-limit` | `0` | 排队写入上限（`0` = 不限） |
| `enqueued-write-timeout` | `"30s"` | 写入最长排队等待时间 |
| `user-query-bytes-enabled` | `false` | 按用户统计查询响应字节数 |
| `user-write-bytes-enabled` | `false` | 按用户统计写入请求字节数 |
| `[http.headers]` | 未设置 | 任意响应头，如 `X-Header-1 = "value"` |

### `[logging]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `format` | `"auto"` | 日志编码：`auto`、`logfmt`、`json` |
| `level` | `"info"` | 最低级别：`error`、`warn`、`info`、`debug` |
| `suppress-logo` | `false` | 不打印启动 logo |

### `[subscriber]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `enabled` | `true` | 启用订阅（把每次写入复制到指定端点） |
| `http-timeout` | `"30s"` | 订阅写入超时 |
| `insecure-skip-verify` | `false` | 跳过订阅端点的 TLS 校验 |
| `ca-certs` | `""` | PEM CA 包路径；空则使用系统根证书 |
| `root-ca` | 未设置 | 额外的订阅 CA：`{ paths = [...], include-system = false }` |
| `certificate` | `""` | 向订阅端点出示的客户端证书（双向 TLS） |
| `private-key` | `""` | `certificate` 对应的私钥 |
| `insecure-certificate` | `false` | 允许不安全的证书/私钥文件权限 |
| `ignore-cert-sanity-checks` | `false` | 加载未通过服务端认证检查的证书 |
| `write-concurrency` | `40` | 写协程数 |
| `write-buffer-size` | `1000` | channel 中缓冲的写入数 |
| `total-buffer-bytes` | `"0"` | 总缓冲预算（`0` = 不限） |

### 重复监听表

这些是**数组表**（`[[graphite]]`、`[[collectd]]`、`[[opentsdb]]`、`[[udp]]`），无法用 `--config-option` 设置；需要时直接编辑生成的文件。每张表默认关闭。

| 段 | 键 | 默认值 | 说明 |
|---|---|---|---|
| `[[graphite]]` | `enabled` | `false` | 启用 Graphite 监听 |
| | `bind-address` | `":2003"` | 监听地址 |
| | `database` | `"graphite"` | 目标数据库 |
| | `retention-policy` | `""` | 目标保留策略 |
| | `protocol` | `"tcp"` | `tcp` 或 `udp` |
| | `consistency-level` | `"one"` | 写入一致性级别 |
| | `batch-size` | `5000` | 刷盘前缓冲点数 |
| | `batch-pending` | `10` | 挂起批次数 |
| | `batch-timeout` | `"1s"` | 最长刷盘等待 |
| | `udp-read-buffer` | `0` | UDP 读缓冲（`0` = 系统默认） |
| | `separator` | `"."` | 模板 measurement 各部分的连接符 |
| | `tags` | `[]` | 默认 tag，如 `["region=us-east"]` |
| | `templates` | `[]` | 模板规则（默认一条） |
| `[[collectd]]` | `enabled` | `false` | 启用 collectd 监听 |
| | `bind-address` | `":25826"` | 监听地址 |
| | `database` | `"collectd"` | 目标数据库 |
| | `retention-policy` | `""` | 目标保留策略 |
| | `typesdb` | `"/usr/share/collectd/types.db"` | types DB 文件或目录 |
| | `security-level` | `"none"` | `none`、`sign`、`encrypt` |
| | `auth-file` | `"/etc/collectd/auth_file"` | 签名/加密流量的认证文件 |
| | `batch-size` | `5000` | 刷盘前缓冲点数 |
| | `batch-pending` | `10` | 挂起批次数 |
| | `batch-timeout` | `"10s"` | 最长刷盘等待 |
| | `read-buffer` | `0` | UDP 读缓冲（`0` = 系统默认） |
| | `parse-multivalue-plugin` | `"split"` | 多值插件按 `split` 或 `join` 解析 |
| `[[opentsdb]]` | `enabled` | `false` | 启用 OpenTSDB 监听 |
| | `bind-address` | `":4242"` | 监听地址 |
| | `database` | `"opentsdb"` | 目标数据库 |
| | `retention-policy` | `""` | 目标保留策略 |
| | `consistency-level` | `"one"` | 写入一致性级别 |
| | `tls-enabled` | `false` | 提供 TLS |
| | `certificate` | `"/etc/ssl/influxdb.pem"` | TLS 证书 |
| | `private-key` | `""` | TLS 私钥；空表示与证书合并存放 |
| | `insecure-certificate` | `false` | 允许不安全的证书文件权限 |
| | `ignore-cert-sanity-checks` | `false` | 加载未通过检查的证书 |
| | `client-auth-type` | 未设置 | 双向 TLS 客户端认证模式 |
| | `client-ca` | 未设置 | 客户端 CA：`{ paths = [...], include-system = false }` |
| | `log-point-errors` | `true` | 记录每个格式错误的点 |
| | `batch-size` | `1000` | 刷盘前缓冲点数（仅 telnet） |
| | `batch-pending` | `5` | 挂起批次数 |
| | `batch-timeout` | `"1s"` | 最长刷盘等待 |
| `[[udp]]` | `enabled` | `false` | 启用 line protocol UDP 监听 |
| | `bind-address` | `":8089"` | 监听地址 |
| | `database` | `"udp"` | 目标数据库 |
| | `retention-policy` | `""` | 目标保留策略 |
| | `precision` | `"n"` | 输入精度：`""`、`n`、`u`、`ms`、`s`、`m`、`h` |
| | `batch-size` | `5000` | 刷盘前缓冲点数 |
| | `batch-pending` | `10` | 挂起批次数 |
| | `batch-timeout` | `"1s"` | 最长刷盘等待 |
| | `read-buffer` | `0` | UDP 读缓冲（`0` = 系统默认） |
| | `writers` | `1` | 并行写协程数 |

### `[continuous_queries]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `enabled` | `true` | 执行连续查询 |
| `log-enabled` | `true` | 记录 CQ 执行日志 |
| `query-stats-enabled` | `false` | 把 CQ 统计写入监控库 |
| `run-interval` | `"1s"` | CQ 检查间隔 |

### `[tls]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `ciphers` | Go 默认 | 允许的加密套件（Go 名称） |
| `min-version` | Go 默认 | 最低 TLS 版本，如 `"tls1.2"` |
| `max-version` | Go 默认 | 最高 TLS 版本，如 `"tls1.3"` |

> **插件里的 HTTPS：** pgcli 从不开启 `[http] https-enabled`。需要 TLS 时使用
> [nginx 插件](./nginx/) 做终止，或只监听回环地址。`[tls]` 段只调整已经启用
> TLS 的监听器的加密套件/版本协商。
