---
title: "PgDog"
description: "将 PgDog（Postgres 代理：连接池、负载均衡、分片）作为 pgcli 插件运行"
weight: 40
---

[PgDog](https://pgdog.dev) 是一个用 Rust 编写的高性能 Postgres 代理（PgCat
的继任者）。它提供连接池化、跨副本的读写负载均衡，以及水平分片，位于一个或多
个 PostgreSQL 后端之前。pgcli 可将 PgDog 作为**独立的顶层插件**运行：它是共享
基础设施，而非依附某个实例的 sidecar。

> **安全提示：** PgDog 使用 `users.toml` 中的**明文**密码对客户端做认证。
> pgcli 以 `0600` 权限写出该文件并只读挂载进容器，但密码仍以明文存在于磁盘和
> `pg.yaml` 中。请将监听端口保持在回环地址（默认）或可信网络内，并把配置文件
> 当作机密对待。

PgDog 由两个 TOML 文件配置——`pgdog.toml`（代理本身、其后端、以及分片规则）
和 `users.toml`（客户端凭据）。pgcli **完全根据安装参数生成这两个文件**：没有
需要手工编辑的配置文件，因此 `pg addon install pgdog` 就是唯一事实来源，且具
有幂等性——重新执行会根据所给参数重新渲染文件。

## 平台支持

PgDog 支持 **Linux**（host 网络）与 **macOS 单主机 dev/test**：macOS 下容器加
入 PG 实例在 podman machine 里使用的同一张 `pgcli-net` bridge 网络，并发布客户
端与 openmetrics 两个端口。macOS 上：

- 容器内的监听地址会被自动放宽为 `0.0.0.0`，否则发布端口转发不到只绑回环的进
  程；对外连接的地址仍然是 `127.0.0.1:<port>`。
- **后端必须是从 bridge 可达的地址。** PgDog 不会自动解析 `--backend` 里的名字，
  所以 `127.0.0.1` 指向的是 Mac 本机，而不是 podman machine 虚拟机。请给每个
  `--backend` 填目标实例的**容器名**（用 `pg status -i <instance>` 查看
  `Container:` 一行，例如 `app=pgcli-pg-mypg:5432:...`），或 Mac 能路由到的地
  址 —— 对受管实例绝对不要填 `127.0.0.1`。

## 工作原理

- **共享基础设施：** PgDog 存放在 `pg.yaml` 顶层的 `addons.pgdog` map 中，按
  代理名索引——不隶属于任何单个实例。
- **主机网络（Linux）/ bridge 网络（macOS）：** 代理监听其客户端端口（从
  `pgdog_start_port` 自动分配，默认 7432），并在下一个空闲端口提供
  Prometheus 风格的 openmetrics 端口。Linux 上以 `--network host` 运行；macOS
  上加入 `pgcli-net` 并发布两个端口。
- **生成的配置：** `pgdog.toml` + `users.toml` 写在
  `<base-dir>/addon/pgdog/<name>/` 下，并绑定挂载进容器的 `/pgdog`。
- **锁定镜像：** 默认 `ghcr.io/pgdogdev/pgdog:v0.1.57`，可用 `--image` 覆盖。

## 安装

最小的单后端代理（仅演示连接池化，见下方说明）：

```bash
pg addon install pgdog \
  --backend "app=127.0.0.1:35432:default_db" \
  --user "appuser:secret:app" \
  --sharded-table "app:users:id:bigint"
```

> 加 `--sharded-table` 是为了让这份示例成为完整、自洽的配置（与下方"分片与副
> 本"示例对齐）。注意：**单个** `--backend` 无从分片——不管是否声明，所有数据最
> 终都会落到那唯一后端，所以这份最小示例只体现连接池化。真正分片需要至少两个不
> 同 `shard` 编号的 `--backend`（见[分片与副本](#分片与副本)）。

- `--backend NAME=HOST:PORT:DBNAME[:SHARD[:ROLE]]` —— 一个 `[[databases]]`
  条目。`NAME` 是客户端连接时使用的逻辑数据库名；`DBNAME` 是后端上真实的数据
  库名。`SHARD` 默认为 0；`ROLE` 默认为 PgDog 默认值（`primary`，读路由可设
  `replica`）。可重复。
- `--user NAME:PASSWORD[:DBNAME]` —— 一个 `[[users]]` 条目。`DBNAME` 是该用户
  可访问的逻辑数据库（某个 `--backend` 的名字），缺省时取第一个后端的名字。可
  重复。
- 至少需要一个 `--backend` 和一个 `--user`。

其他参数：

| 参数 | 含义 | 默认值 |
|------|------|--------|
| `--name` | 代理 / 插件键名 | `pgdog` |
| `--port` | 客户端主机端口（openmetrics 取下一个空闲端口） | 自动分配 |
| `--host` | 监听地址 | `127.0.0.1` |
| `--pool-mode` | `transaction` 或 `session` | `transaction` |
| `--workers` | 工作线程数 | `2` |
| `--default-pool-size` | 每个 user/db 对的服务器连接数 | `10` |
| `--image` | PgDog 镜像 tag | `ghcr.io/pgdogdev/pgdog:v0.1.57` |

### 分片与副本

对每个分片 / 角色重复 `--backend`，并用
`--sharded-table DBNAME:TABLE:COLUMN:DATA_TYPE` 声明分片键：

```bash
pg addon install pgdog \
  --backend "app=10.0.0.1:5432:shard0:0" \
  --backend "app=10.0.0.2:5432:shard1:1" \
  --backend "app=10.0.0.1:5433:shard0:0:replica" \
  --backend "app=10.0.0.2:5433:shard1:1:replica" \
  --user "appuser:secret:app" \
  --sharded-table "app:users:id:bigint"
```

这会把两个分片（各含一主一副本）置于逻辑数据库 `app` 之后，并按 `id` 列路由
`users` 表。

### 准备后端

PgDog 路由到的数据库、表与角色必须已存在于后端——安装代理并不会创建它们。对于
本地 pgcli 实例，用 `pg exec` 创建每个分片库、其分片表，以及登录角色。以单机
default 实例为例（分片即同一端口上的不同数据库），下面的示例先准备后端（第 1–4
步）、安装代理（第 5 步），再写入数据以体现路由效果（第 6–7 步）：

```bash
# 1. 创建分片数据库
pg exec -i default -- psql -U admin -d default_db -c "CREATE DATABASE shard0;"
pg exec -i default -- psql -U admin -d default_db -c "CREATE DATABASE shard1;"

# 2. 在每个分片中创建相同结构的分片表
for db in shard0 shard1; do
  pg exec -i default -- psql -U admin -d "$db" \
    -c "CREATE TABLE users (id bigint PRIMARY KEY, name text, email text);"
done

# 3. 创建 PgDog 连后端所用的登录角色——名字必须与某个 --user 相同，
#    密码必须与该用户在 users.toml 中的条目一致
pg exec -i default -- psql -U admin -d default_db \
  -c "CREATE ROLE appuser LOGIN PASSWORD 'secret';"

# 4. 为该角色授予每个分片中分片表的访问权限
for db in shard0 shard1; do
  pg exec -i default -- psql -U admin -d "$db" \
    -c "GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE users TO appuser;"
done

# 5. 在两个分片之上安装代理（参见上文「分片与副本」）
pg addon install pgdog \
  --backend "app=127.0.0.1:35432:shard0:0" \
  --backend "app=127.0.0.1:35432:shard1:1" \
  --user "appuser:secret:app" \
  --sharded-table "app:users:id:bigint"

# 6. 用 pgcli（pg exec --dsn）通过代理写入数据，一条语句只写一行，
#    PgDog 才能按 id 路由（多行 INSERT 会被整体广播到每个分片）
for id in 1 2 3 4 5 6; do
  pg exec --dsn "postgres://appuser:secret@127.0.0.1:7432/app" \
    "INSERT INTO users (id, name, email) VALUES ($id, 'n$id', 'e$id@x');"
done

# 7. 直接查询每个分片，确认数据落在 PgDog 路由到的位置
#    pg psql -- -d <db> 针对单个数据库
pg psql -i default -- -d shard0 -tAc "SELECT id, name FROM users ORDER BY id;"
pg psql -i default -- -d shard1 -tAc "SELECT id, name FROM users ORDER BY id;"
```

`pg exec -i default -- psql -d <db>` 在指定实例的容器内针对某个数据库执行命令。
上面的 `id bigint` 列与 `--sharded-table "app:users:id:bigint"` 的声明一致，因
此 PgDog 能按 `id` 路由 `users` 的行。第 6、7 步让这层路由可见：两个分片的查询
返回**互不重叠**的 id 子集（具体划分取决于 PgDog 的哈希），两者合起来正好覆盖所
有写入行——没有一行会同时落到两个分片。远程后端则直接对各主机执行等价 SQL。

**后端角色不可省略。** 默认情况下 PgDog 用*客户端*的 `--user` 名字及其
`users.toml` 密码去连后端。所以每个后端都必须存在名为 `appuser`（等）的登录角
色，否则连接会失败并报 `password for user "..." is wrong, or the database does
not exist`——即使在回环 `trust` 配置下密码被忽略，*角色*仍必须存在。若走密码认证
（非回环 `scram-sha-256` 的默认行为），`users.toml` 里的密码必须与角色一致。

> **解耦客户端与后端用户。** PgDog 本身支持用*不同于*客户端的用户去连后端：一个
> `[[users]]` 条目可带 `server_user` / `server_password`，一个 `[[databases]]`
> 条目可带 `user` / `password`（后者优先级更高）。**但 pgcli 的安装参数目前不会
> 生成这些字段**，因此经 `pg addon install pgdog` 安装时，后端角色名始终等于
> `--user` 的名字。若你需要一个共享的后端角色（例如统一的 `postgres` 超级用户，
> 或与所有客户端用户名都不同的名字），只能手工编辑生成的
> `users.toml`/`pgdog.toml` 并重启容器——但请注意：重新执行 install 会按参数重新
> 生成这两个文件，覆盖掉你的手工改动。

## 连接

安装摘要会打印接入端点。客户端通过代理的客户端端口连接，使用逻辑数据库名和某个
`--user` 凭据——在 pgcli 里，就是一个指向代理的 `--dsn`：

```bash
# 通过代理进入交互式 psql
pg psql --dsn "postgres://appuser:secret@127.0.0.1:7432/app"

# 一次性执行 SQL
pg exec --dsn "postgres://appuser:secret@127.0.0.1:7432/app" "SELECT version();"
```

Prometheus 指标在 openmetrics 端口提供：

```bash
curl -s http://127.0.0.1:7433/metrics
```

## 向分片写入

写入如何落到各分片，取决于语句的*形式*。以下是在 PgDog v0.1.57 上的实测。

**单行 `INSERT` —— 按分片键路由。** 一条语句只带一个 `VALUES` 元组时，
PgDog 对 `id` 做哈希，把该行送到唯一一个分片：

```bash
pg exec --dsn "postgres://appuser:secret@127.0.0.1:7432/app" \
  "INSERT INTO users (id, name, email) VALUES (4, 'n4', 'e4@x');"
```

以六条单行语句分别插入 id 1–6 时，两分片各得一部分（一次实测：shard0 收到
`1,2`，shard1 收到 `3,4,5,6` —— 具体划分取决于 PgDog 的哈希，不应当作可依赖
的结果）。没有任何一行同时落入两个分片。

**多行 `INSERT` —— 广播而非拆分。** 一条语句带多个 `VALUES` 元组时，会被原
样送到**每一个**分片，于是每个分片都拿到*全部*行：

```bash
pg exec --dsn "postgres://appuser:secret@127.0.0.1:7432/app" \
  "INSERT INTO users (id, name, email) VALUES (1,'a','a@x'),(2,'b','b@x'),(3,'c','c@x'),(4,'d','d@x');"
```

这条语句报告 `INSERT 0 8` —— *每个*分片各 4 行。分片内 `PRIMARY KEY` 仍然
有效（重复 id 分别落在不同分片，永不冲突），且**没有任何错误或告警**：每个分
片静默地多出一份完整副本。随后经代理读取就会看到每个 id 都成对重复出现。

**正确的批量导入方式：** 一条语句一行、逐行经代理插入（各行单独路由）；或者
绕开代理，直接对各分片做导入（例如 `pg exec -i <inst> -- psql -d <shard>` 跑
`COPY`）。

## 配置

安装后，`pg.yaml` 在顶层 `addons.pgdog` 下记录每个代理：

```yaml
addons:
  pgdog:
    pgdog:
      container_name: pgcli-pgdog-pgdog
      name: pgdog
      image_tag: ghcr.io/pgdogdev/pgdog:v0.1.57
      host: 127.0.0.1
      host_port: 7432
      openmetrics_port: 7433
      pooler_mode: transaction
      workers: 2
      default_pool_size: 10
      backends:
        - name: app
          host: 127.0.0.1
          port: 35432
          database_name: default_db
          shard: 0
      users:
        - name: appuser
          password: secret
          database: app
      autostart: false
```

端口基址可通过顶层 `pgdog_start_port`（默认 7432）配置；openmetrics 端口总是紧接
其上方分配。

### 查看列表

```bash
pg addon list
```

每个 PgDog 代理都会出现在 **Infra add-ons (pgdog)** 小节下，附带运行时状态、
端口、后端 / 用户数量、镜像和容器名：

```
Infra add-ons (pgdog):
  pgdog (name: pgdog)
    Status:      running
    Listen:      127.0.0.1:7432
    Client port: 7432
    Metrics:     http://127.0.0.1:7433/metrics
    Pool mode:   transaction
    Backends:    2
    Users:       1
    Image:       ghcr.io/pgdogdev/pgdog:v0.1.57
    Container:   pgcli-pgdog-pgdog
```

`Backends` / `Users` 取自 `pgdog.toml` / `users.toml` 中的条目数；`Status`
反映容器的实时状态（已停止的代理显示为 `stopped`）。

## 启动与停止

宿主机重启或手动停掉后,无需重跑 install 即可拉起代理(配置与 users 不动):

```bash
pg addon start pgdog            # 默认名 "pgdog"
pg addon start pgdog --name proxy
pg addon stop pgdog --name proxy
```

`start` 幂等 —— 已在运行则不动;容器被删掉的代理会从其配置目录重建。

## 开机自启

容器带有 `--restart unless-stopped` 策略（应对崩溃，而非重启）。要在主机重启后
拉起代理：

```bash
pg autostart enable --pgdog --name pgdog
```

这会设置 `autostart: true` 并安装 / 刷新开机服务（见
[开机自启](/docs/autostart/)）。开机时是**仅启动**语义：它启动已存在的容器、读
取磁盘上已有的 `pgdog.toml`/`users.toml`，不会重新生成配置或认证。请先安装代理。
`pg autostart status` 会列出每个目标的状态。

## 日志

```bash
pg logs addon pgdog --name pgdog       # 最后 50 行
pg logs addon pgdog --name pgdog -f    # 持续跟踪
```

PgDog 以结构化 INFO 行记录连接池事件（新建服务器连接、认证、客户端连接 / 断
开）。若某个连接池不断重试并报 `password for user "..." is wrong, or the
database does not exist`，说明上面「准备后端」的第 3 或第 4 步被跳过——后端没有
该登录角色，或其密码与 `users.toml` 不一致。

## 注意事项

- **重装幂等：** 重新执行 `pg addon install pgdog --name <p>` 会根据所给参数重新
  渲染配置并重建容器。后端 / 用户 / 分片列表被本命令的参数**完全替换**——省略某
  个 `--backend` 是移除它，而非保留。
- **明文凭据：** 密码存放在 `users.toml` 和 `pg.yaml` 中。建议为代理用户单独使
  用一个低权限角色，并限制对客户端端口的网络访问。
- **写入形式差异：** 单行 `INSERT` 按分片键路由；多行 `INSERT VALUES` 会被**广
  播**到每个分片。详见[向分片写入](#向分片写入)。
- **非实例级 sidecar：** 与 PgBouncer 不同，PgDog 通过 `--backend` 里的地址指向
  后端——它不必指向某个 pgcli 管理的实例，一个代理可横跨多个数据库 / 分片。
