---
title: "压测"
description: "在一次性容器中运行 pgbench，对本地 pgcli 实例或远程 PostgreSQL 做负载测试"
weight: 65
icon: fa-solid fa-gauge-high
aliases: ["/toolbox/bench/"]
menus:
  main:
    identifier: docs-toolbox-bench
    parent: docs-toolbox
    weight: 65
    params:
      icon: fa-solid fa-gauge-high
cascade:
  type: docs
  footer_style: slim
---

`pg bench` 在一次性容器中运行
[pgbench](https://www.postgresql.org/docs/current/pgbench.html)。宿主机无需安装
pgbench 或 PostgreSQL 客户端，压测结束后也不会留下常驻服务。

目标既可以是本地 pgcli 实例（全局 `-i/--instance`），也可以是任意可连接的
数据库（`--dsn`）。官方 `postgres` 镜像和 pgcli 自己的 `pgcli-pg` 镜像都自带
PostgreSQL 客户端工具，因此默认镜像即可直接使用。

## 快速开始

一次性压测可以用 `--cleanup` 一条命令完成整个流程：初始化 pgbench 表、运行
压测，结束后删除这些表。

```bash
# 本地 pgcli 实例
pg bench -i proj01 --cleanup

# 远程数据库
pg bench --dsn 'postgres://user:pass@10.0.0.8:5432/appdb' --cleanup
```

数据库本身会保留，只删除 pgbench 自己的表。如果希望在多次运行之间保留压测
数据，先初始化一次，之后不带 `--cleanup` 运行：

```bash
pg bench -i proj01 --init --scale 10
pg bench -i proj01
```

不使用 `--cleanup` 时，命令会在运行结束后打印一条可直接复制的 `pg exec`
命令，在压测库中删除 pgbench 表，方便之后手动清理。如果运行因为 pgbench
表不存在而失败，命令会检测到并提示使用 `--init` 或 `--cleanup`，而不是输出
删除命令。

默认 16 个客户端、4 个线程、运行 60 秒、使用 `prepared` 协议，每 10 秒输出
一次进度。

`--init` 执行 `pgbench -i`，会在压测库中删除并重建 pgbench 自己的
`pgbench_accounts`、`pgbench_branches`、`pgbench_history` 和
`pgbench_tellers` 表。它不会修改其他表，也绝不会删除数据库。`--cleanup`
会执行同样的初始化，并在压测结束后删除这四张表。

## 专用压测库

运行前，pgcli 会检查给定账号是否为超级用户或拥有 `CREATEDB`：

- **有创建数据库权限** — 使用 `bench_db`（可通过 `--bench-db` 修改）。库不存在
  时自动创建，已存在时直接复用；不会自动删除。加上 `--cleanup` 时会在结束后
  删除 pgbench 表，但数据库本身仍然保留。
- **没有该权限** — 继续使用实例配置或 DSN 指定的库，并打印回退原因。

权限探测在一次性 `psql` 容器中执行。本地实例通过 `postgres` 库探测；DSN
目标通过 DSN 中指定的库探测。凭据通过 `PG*` 环境变量传递，不出现在命令行
参数中。

如果压测库是在 run 模式（未加 `--init` 或 `--cleanup`）下刚创建的，`pg bench`
会直接报错，提示先初始化：

```text
benchmark database "bench_db" was created but not initialized; run "pg bench --init" first
```

`--cleanup` 把初始化、压测和清理表合并成一次执行。它同时接受 init 参数和 run
参数，也可以和 `--init` 一起使用；无论是否显式传 `--init` 都会先初始化。
即使 pgbench 运行失败，也会尝试清理；只删除四张 `pgbench_*` 表。

使用 `--no-auto-db` 可以跳过探测、始终使用原目标库；使用
`--bench-db NAME` 可以指定其他专用库名：

```bash
pg bench -i proj01 --no-auto-db
pg bench -i proj01 --bench-db loadtest
```

## scale（数据规模）

`--scale N`（对应 pgbench `-s`）是数据量倍数，不是并发数。`--init` 生成的
初始行数如下：

| 表 | 行数 |
|----|------|
| `pgbench_branches` | N |
| `pgbench_tellers` | 10 × N |
| `pgbench_accounts` | 100,000 × N |
| `pgbench_history` | 0（运行中每事务增加一行） |

scale=1 时只有 1 行 branch、10 行 teller 和 10 万行 account：并发客户端会
争用同一行 branch，而且数据大概率全部命中内存，测不出磁盘读压力。需要测
服务器上限时，应使用更大的 scale（如 10、50 或 100），让数据分布更均匀并
超出内存。scale 越大会线性增加初始化时间和磁盘占用。

pgbench 运行时用 `-s` 决定随机访问范围，默认值为 1；如果初始化用了更大的
scale 而运行时没有传入相同的值，内置脚本只会访问前 10 万行 accounts。
`pg bench` 会在运行前执行 `SELECT max(aid) FROM pgbench_accounts` 自动推断
scale 并传给 pgbench，因此单独执行 `pg bench -i proj01` 也能覆盖全量数据。
同一命令中的 `--init`/`--cleanup` 则直接使用 `--scale` 的值。表不存在时跳过
推断，交由缺失表提示处理。

注意 `-D scale=5` 是传给自定义脚本的变量，与 pgbench 的 `-s/--scale` 无关。

## 连接目标

### 本地 pgcli 实例

```bash
pg bench -i proj01 --init
pg bench -i proj01 -T 30
```

实例容器未运行时，命令会直接报错。

### 通过 `--dsn` 连接远程库

```bash
pg bench --dsn 'postgres://user:pass@db.example.com:5432/appdb' --init
pg bench --dsn 'postgres://user:pass@db.example.com:5432/appdb' --clients 32 -T 120
```

DSN 的 scheme 只能是 `postgres://` 或 `postgresql://`，且必须包含 host、user 和
database；端口默认 5432，密码可以省略。不支持多 host、fragment 和 opaque
DSN。密码中的特殊字符需要百分号编码：

```bash
pg bench --dsn 'postgres://user:p%40ssword@db.example.com:5432/appdb'
```

`--dsn` 与显式传入的 `-i/--instance` 互斥。

#### 支持的 query 参数

只接受以下 DSN query 参数。它们会映射为 libpq 环境变量，同时传给控制用的
`psql` 和 pgbench：

| DSN 参数 | 环境变量 |
|----------|----------|
| `sslmode` | `PGSSLMODE` |
| `sslrootcert` | `PGSSLROOTCERT` |
| `sslcert` | `PGSSLCERT` |
| `sslkey` | `PGSSLKEY` |
| `sslcrl` | `PGSSLCRL` |
| `sslcrldir` | `PGSSLCRLDIR` |
| `sslpassword` | `PGSSLPASSWORD` |
| `connect_timeout` | `PGCONNECT_TIMEOUT` |
| `application_name` | `PGAPPNAME` |
| `fallback_application_name` | `PGFALLBACKAPPNAME` |
| `options` | `PGOPTIONS` |
| `target_session_attrs` | `PGTARGETSESSIONATTRS` |
| `channel_binding` | `PGCHANNELBINDING` |
| `client_encoding` | `PGCLIENTENCODING` |
| `gssencmode` | `PGGSSENCMODE` |
| `sslnegotiation` | `PGSSLNEGOTIATION` |
| `ssl_min_protocol_version` | `PGSSLMINPROTOCOLVERSION` |
| `ssl_max_protocol_version` | `PGSSLMAXPROTOCOLVERSION` |

连接字段（`host`、`port`、`user`、`password`、`dbname`、`service`、
`passfile`、`replication`）必须写在 URL 本身，不能放在 query 中。

TLS 证书路径会转换为宿主机绝对路径，以只读方式挂载到 `/pgcli-bench/tls/`
下，对应的环境变量指向容器内路径。`sslrootcert=system` 原样传递。

### 客户端镜像版本

默认客户端镜像来自 pgcli 配置中的 `image_tag`（`pgcli-pg`，基于
PostgreSQL 18）。如果远程服务端大版本不同，可显式选择匹配的镜像：

```bash
pg bench --dsn 'postgres://user:pass@db:5432/appdb' --image docker.io/library/postgres:17 -T 60
```

pgbench 通常可以跨小版本工作，但匹配大版本可以避免协议和系统表层面的意外。

## 运行模式与脚本

默认使用 TPC-B-like 内置脚本、extended 协议和 prepared 模式：

```bash
pg bench -i proj01
```

常见变体：

```bash
# 只读负载
pg bench -i proj01 -S -T 60

# simple-update 负载（不更新 pgbench_accounts）
pg bench -i proj01 --simple-update

# 固定每个客户端的事务数，而不是按时间运行
pg bench -i proj01 --clients 8 --transactions 1000

# 限速并设置延迟上限
pg bench -i proj01 --rate 500 --latency-limit 20 -T 60

# 按权重混合内置脚本
pg bench -i proj01 -b select-only@3 -b tpcb-like@1 -T 60
```

`--time` 与 `--transactions` 互斥；`--select-only` 与 `--simple-update` 互斥；
`--latency-limit` 必须搭配 `--rate`。

### 自定义脚本

自定义脚本放在宿主机，以只读方式挂载进容器：

```bash
pg bench -i proj01 -f ./bench.sql -T 60
pg bench -i proj01 -f ./bench.sql@2 -f ./other.sql@1 -T 60
```

可选的 `@weight` 后缀遵循 pgbench 语义。脚本变量用 `-D` 传递：

```bash
pg bench -i proj01 -f ./bench.sql -D scale=5 -D clients=16 -T 60
```

`-D` 的值会出现在容器命令行中，不要用它传递密码等密钥。

## 日志

pgbench 的逐事务日志会写入宿主机目录：

```bash
pg bench -i proj01 --log --log-dir ./pgbench-logs -T 30
```

目录不存在时会自动创建，并以读写方式挂载到 `/pgcli-bench/log`。pgbench 会在
其中写入 `pgbench_log.<pid>`（单线程）或 `pgbench_log.<pid>.<thread>`（多
线程）。pgbench 退出后，命令会打印宿主机目录和本次运行写入的每个日志文件
路径；rootful Podman 下文件属 root，rootless Podman 下归属当前用户。

## 透传原生 pgbench 参数

`--` 之后的参数会原样追加到 pgbench 命令：

```bash
pg bench -i proj01 -- --sampling-rate 0.1
```

以下原生参数会被拒绝，因为它们会绕过 pgcli 对目标、脚本或日志的处理：

| 被拒绝的参数 | 改用 |
|--------------|------|
| `-h`, `--host` | `--dsn` 或 `-i` |
| `-p`, `--port` | `--dsn` 或 `-i` |
| `-U`, `--username` | `--dsn` 或 `-i` |
| `-d`, `--dbname` | `--dsn` 或 `-i` |
| `-i`, `--initialize` | `--init` |
| `-f`, `--file` | `--file` |
| `-l`, `--log` | `--log` |

`--host=x`、`--hos` 这类缩写长参数以及组合短参数同样会检查。`--` 之前的
位置参数会被拒绝；实例选择必须使用 `-i/--instance`。

## 安全说明

- 密码通过 `PGPASSWORD` 环境变量传递，不会出现在 `podman run` 的参数中，
  因此 `ps` 看不到。但与任何容器环境变量一样，拥有 podman 权限的人可以通过
  `podman inspect` 查看。
- 你输入的 `--dsn` 值属于 `pg bench` 进程参数，因此对 `ps` 可见。podman
  客户端容器仍然只接收 `PG*` 环境变量，不会接收 DSN。
- TLS 证书和私钥以只读方式挂载进容器。
- 通过 `-D` 传入的凭据和敏感值会出现在容器命令行中，避免用 `-D` 传递密钥。
- 压测表继承目标库的权限配置。对共享基础设施压测时，建议使用专用账号。

## 限制

- 客户端容器使用 `--network host`，因此宿主机必须是 Linux。macOS 上 Podman
  的 host networking 无法以同样方式访问宿主机网络命名空间。
- 客户端与服务端同机运行时，会争抢 CPU、内存和磁盘 I/O，结果反映的是这种
  资源竞争。需要干净数据时，应从独立机器对数据库主机运行 `pg bench`。
- 镜像中必须包含 `pgbench`；不含时会返回退出码 126/127，并提示通过
  `--image` 指定镜像。
- 每个压测库和 scale 只需执行一次 `--init`；重复执行只会重建 pgbench 自己的表。

## 参数

| 参数 | 默认值 | 模式 | 说明 |
|------|--------|------|------|
| `--dsn URL` | 空 | 通用 | 远程目标；与显式 `-i` 互斥 |
| `-i, --instance NAME` | `default` | 通用 | 本地 pgcli 实例（全局参数） |
| `--image IMAGE` | 配置 `image_tag` | 通用 | 包含 pgbench 的客户端镜像 |
| `--bench-db NAME` | `bench_db` | 通用 | 专用压测库名 |
| `--no-auto-db` | false | 通用 | 不探测/创建专用库 |
| `--init` | false | 模式 | 初始化 pgbench 表 |
| `--cleanup` | false | 模式 | 初始化并运行压测，结束后删除 pgbench 表；保留数据库 |
| `--scale N` | 1 | init | 数据量倍数；运行时自动从 `pgbench_accounts` 推断 |
| `--partitions N` | 0 | init | 分区数量 |
| `--partition-method METHOD` | 空 | init | `range` 或 `hash` |
| `--fillfactor N` | 0 | init | 表 fillfactor（10–100） |
| `--foreign-keys` | false | init | 添加外键 |
| `--no-vacuum` | false | 通用 | init 跳过 vacuum；run 传 `-n` |
| `--clients N` | 16 | run | 并发客户端数 |
| `-j, --threads N` | 4 | run | 工作线程数 |
| `-T, --time SECONDS` | 60 | run | 运行时长 |
| `-t, --transactions N` | 0 | run | 每个客户端的事务数 |
| `-M, --mode MODE` | `prepared` | run | `simple`、`extended` 或 `prepared` |
| `-S, --select-only` | false | run | 只读负载 |
| `-N, --simple-update` | false | run | simple-update 负载 |
| `-b, --builtin SPEC` | 空 | run | 内置脚本 `name[@weight]`，可重复 |
| `-f, --file SPEC` | 空 | run | 宿主机脚本 `path[@weight]`，可重复 |
| `-D, --define K=V` | 空 | run | 脚本变量，可重复 |
| `-C, --connect` | false | run | 每个事务新建连接 |
| `-R, --rate N` | 0 | run | 目标每秒事务数 |
| `-L, --latency-limit MS` | 0 | run | 延迟上限；需要 `--rate` |
| `-P, --progress SECONDS` | 10 | run | 进度间隔；0 表示关闭 |
| `-r, --per-command-report` | false | run | 分命令延迟报告 |
| `-l, --log` | false | run | 写逐事务日志 |
| `--log-dir DIR` | `./pgbench-logs` | run | 宿主机日志目录；需要 `--log` |
