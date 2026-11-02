---
title: "DocumentDB 网关"
description: "以 pgcli 插件方式运行官方 DocumentDB MongoDB wire protocol 网关，通过共享 Unix socket 连接本地 PostgreSQL 18 实例。"
weight: 68
---

`pg addon install documentdb` 把官方 **DocumentDB 网关**——运行在 [DocumentDB PostgreSQL 扩展](../../extensions/documentdb/) 之前、对外讲 MongoDB wire protocol 的 Rust 服务——作为受管容器运行。

```
MongoDB 客户端（pymongo、mongosh……）
        │  端口 10260
        ▼
DocumentDB 网关容器
        │  Unix socket（共享 volume）
        ▼
带 documentdb / documentdb_core /
documentdb_extended_rum / pg_cron 的 PostgreSQL 18 容器
```

> **先装扩展。** 目标实例未运行、没有导出 `/var/run/postgresql`、或四个扩展不齐时，网关会拒绝启动。见 [DocumentDB 扩展](../../extensions/documentdb/)。

> **仅支持本地实例。** 官方网关 `.deb` 只支持经 Unix socket 的无密码认证，拒绝 `PGPASSWORD`、URL 内密码和 `PostgresDataUserPassword`。因此 pgcli 通过共享 socket volume 连接网关，并拒绝 `--dsn`/`--admin-dsn`。远程 PostgreSQL 不能作为目标。

> **访问模型。** 网关以专用角色 `docdb_gateway`（`LOGIN SUPERUSER`、无密码）通过 `pg_hba.conf` 的 `local … trust` 连接 PostgreSQL。MongoDB 客户端以 DocumentDB 用户（默认 `docdb_admin`）认证，内置角色为 `readWriteAnyDatabase` + `clusterAdmin`——权限本来就宽。需要更细隔离时，可为每个应用使用不同的 `--username`；v1 暂不提供按集合授权的角色。

> **无遥测。** pgcli 不使用上游 all-in-one 镜像及其 `usage_telemetry.sh`，而是直接运行 vendor 网关二进制，并在渲染 `SetupConfiguration.json` 时把 `TelemetryOptions.Metrics.Enabled` 与 `Tracing.Enabled` 置为 `false`，不注入任何 OTLP endpoint。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `documentdb` | 插件名（map 键与容器后缀） |
| `--pg-name` | — | **必填。** 目标 pgcli 实例（必须是 PG 18、standalone、已装 DocumentDB 扩展） |
| `--database` | 目标实例的数据库 | 含 DocumentDB 扩展的数据库 |
| `--username` | `docdb_admin` | MongoDB 客户端用户，经 `documentdb_api.create_user` 创建。前缀 `documentdb`、`citus`、`pg`、`internal_role` 会被拒绝 |
| `--password` | 自动生成 | 客户端密码。自动生成时只打印一次；之后用 `pg addon password documentdb` 取回 |
| `--tls-mode` | `allow` | `allow` = 明文与 TLS 均可；`require` = 仅 TLS |
| `--image` | `ghcr.io/mars-base/pgcli/pgcli-documentdb-gateway:1.0.0` | 覆盖网关镜像 |
| `--port` | 自动（基址 10260） | MongoDB wire protocol 端口 |
| `--listen` | `0.0.0.0` | 绑定地址（`0.0.0.0`、`127.0.0.1` 或 `::1`） |
| `--force` | false | 即使正在运行也重建，用于应用改动的参数 |

本插件拒绝 `--dsn` 与 `--admin-dsn`。

## 安装

```bash
pg extension install documentdb -i demo --auto-restart
pg addon install documentdb --pg-name demo
```

安装会打印一次性密码与端点：

```
✓ DocumentDB gateway installed: "documentdb"
  Container:  pgcli-documentdb-documentdb
  Image:      ghcr.io/mars-base/pgcli/pgcli-documentdb-gateway:1.0.0
  Endpoint:   mongodb://docdb_admin@127.0.0.1:10260/demo_db?directConnection=true
  Target:     instance demo (database demo_db)
  Client:     docdb_admin (DocumentDB native user)
  TLS mode:   allow

  Client password: n7Qv2Kd9Xa4Tb8Rm3Lc6Wf1Z
  (printed once — retrieve it later with `pg addon password documentdb --name documentdb`)
```

安装过程中 pgcli 还会：

1. 准备目标——确保 `docdb_gateway` 以 `LOGIN SUPERUSER` 存在；**仅当**没有匹配的本地 trust 规则时，才在 `pg_hba.conf` 顶部写入受管规则 `local all docdb_gateway trust`（pgcli 默认 HBA 已自带，故通常无需改动）。
2. 通过 `documentdb_api.create_user` / `update_user` 创建或轮换客户端用户。JSON 文档经 `psql` stdin 传入——不出现在 argv 或 PostgreSQL 日志——且先执行 `SET log_statement='none'`。
3. 在 `<base-dir>/addon/documentdb/<name>/` 下渲染 `pg-url` 与 `SetupConfiguration.json`，并以只读方式挂载目标的 socket volume。
4. 以 `--restart unless-stopped` 启动网关容器。

容器已在运行时重复执行同一条安装命令是 no-op，除非传 `--force`。

## 用 Python（`uv` + pymongo）做客户端测试

pgcli 仓库使用 [`uv`](https://docs.astral.sh/uv/) 管理临时 Python 依赖。下面是开发时实际跑通的连接与 CRUD 测试：

```bash
export MONGO_PASSWORD="$(pg addon password documentdb --name documentdb)"

uv run --with pymongo python - <<'PY'
import os
from urllib.parse import quote
from pymongo import MongoClient

host = os.environ.get("MONGO_HOST", "127.0.0.1")
port = os.environ.get("MONGO_PORT", "10260")
user = os.environ.get("MONGO_USER", "docdb_admin")
password = quote(os.environ["MONGO_PASSWORD"], safe="")

# 设置 MONGO_TLS=true 以启用 TLS。
use_tls = os.environ.get("MONGO_TLS", "").lower() in ("1", "true", "yes")
params = "directConnection=true"
if use_tls:
    params += "&tls=true&tlsAllowInvalidCertificates=true"

uri = f"mongodb://{user}:{password}@{host}:{port}/?{params}"
client = MongoClient(uri, serverSelectionTimeoutMS=10000)
print("ping:", client.admin.command("ping"))

col = client["pgcli_e2e"]["smoke"]
col.drop()
print("inserted:", col.insert_one({"name": "alpha", "value": 1}).inserted_id)
print("found:   ", col.find_one({"name": "alpha"}))

print("updated: ", col.update_one({"name": "alpha"}, {"$set": {"value": 2}}).modified_count)
print("index:   ", col.create_index([("value", 1)]))
print("count:   ", col.count_documents({}))
print("deleted: ", col.delete_one({"name": "alpha"}).deleted_count)

client.drop_database("pgcli_e2e")
client.close()
print("OK")
PY
```

脚本在插件监听的宿主上运行；从另一台机器运行则把 `MONGO_HOST` 设为网关宿主。脚本默认关闭 TLS：用 `MONGO_TLS=true` 启用。`allow` 与 `require` 模式都可用（`require` 下明文本来就会被拒绝）。只有在使用自动生成的自签证书时才需要 `tlsAllowInvalidCertificates`；如果你有可信证书，改用 `tlsCAFile="/path/to/ca.pem"`。

## 管理命令（mongosh）

pgcli 可以在不往宿主机安装 mongosh、也不给 pgcli 二进制增加 MongoDB driver 的前提下，连接并巡检受管网关或远程网关。客户端由独立的 mongosh-only 镜像提供——`debian:13-slim` 上的官方 mongosh 2.14.1 单文件二进制，不含 MongoDB server——首次使用时按需拉取，并以一次性容器运行。pgcli 二进制体积不增加。

```bash
pg addon documentdb shell
pg addon documentdb shell --database app
pg addon documentdb shell -- --eval 'db.version()'

pg addon documentdb databases list
pg addon documentdb collections list --database app
pg addon documentdb indexes list --database app --collection orders
```

所有子命令共用同一组持久参数：

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `documentdb` | 要连接的网关插件名（显式 `--name` 强制本地模式并忽略 `DOCDB_URI`） |
| `--dsn` | `$DOCDB_URI` | 完整 MongoDB 连接串——跨主机连接推荐使用 |
| `--uri` | — | `--dsn` 的别名 |
| `--host` / `--port` | — / `10260` | `--dsn` 的备选写法 |
| `--username` / `--password` | `$DOCDB_USERNAME` / `$DOCDB_PASSWORD` | 配合 `--host` 使用；优先用环境变量，`--password` 会出现在 `ps` 中 |
| `--tls` | false | 强制 TLS。受管网关的 `--tls-mode` 为 `require` 时自动启用 |
| `--image` | `ghcr.io/mars-base/pgcli/pgcli-mongosh:2.14.1` | 覆盖客户端镜像（私有仓库或离线场景） |

`shell` 打开交互会话；`--database` 指定初始数据库（默认是网关的目标数据库或 URI 中的数据库），`--` 之后的参数原样转交给 mongosh。`databases list`、`collections list`、`indexes list` 执行只读查询，每行输出一个名字（索引则输出一个 JSON 文档）；`indexes list` 必须提供 `--collection`。

### 跨主机连接

连接另一台主机上的网关不需要本地 add-on 配置。把连接串导出一次，后续命令就很短：

```bash
export DOCDB_PASSWORD='...'   # 在网关主机执行 pg addon password documentdb 获取
export DOCDB_URI='mongodb://docdb_admin@10.0.0.5:10260/demo_db?authSource=admin&directConnection=true'

pg addon documentdb databases list
pg addon documentdb collections list --database demo_db
pg addon documentdb shell -- --eval 'db.version()'
```

一次性命令直接用 `--dsn`（或别名 `--uri`）：

```bash
pg addon documentdb collections list \
  --dsn 'mongodb://docdb_admin@10.0.0.5:10260/demo_db'
```

规则：

- URI 中没有密码时用 `DOCDB_PASSWORD` 补全；没有 userinfo 时用 `DOCDB_USERNAME` 补全。优先用这些环境变量，`--password` 的值会出现在 `ps` 中。
- URI 缺少 `authSource=admin`、`directConnection=true`、`serverSelectionTimeoutMS=10000` 时 pgcli 会补上；URI 中已有的值和数据库路径优先。
- `--tls` 会加上 `tls=true&tlsAllowInvalidCertificates=true` 强制 TLS；连接 `require` 模式的网关时使用，或自行在 URI 中写 TLS 参数。
- `--dsn`/`--uri` 不能与 `--host/--port/--username/--password` 组合，`--name` 不能与任何远程参数组合。显式的远程参数优先于 `DOCDB_URI`。
- `--host HOST --port PORT --username USER` 作为备选写法保留；密码来自 `DOCDB_PASSWORD`。
- 客户端主机需要 podman 和 mongosh 镜像，首次使用时从 GHCR 拉取（可用 `--image` 覆盖）。网关必须监听客户端可达的地址：默认 `--listen 0.0.0.0` 可用，`127.0.0.1` 只允许网关主机自身访问。

远程模式只改变 mongosh 客户端连接的位置——网关本身仍然连接其所在主机上的本地 PostgreSQL 实例。

### 常用 mongosh 操作

`shell` 会把 `--` 之后的参数原样转交给 mongosh。日常操作示例：

```javascript
// 查看
db                       // 当前数据库
show dbs                 // DocumentDB 只列出已经有集合的数据库
show collections
use demo_db              // 切换数据库（首次写入时隐式创建）

// 写入与查询
db.users.insertOne({name: "alice", age: 30})
db.users.insertMany([{name: "bob"}, {name: "carol", age: 41}])
db.users.findOne({name: "alice"})
db.users.find({age: {$gte: 30}}).forEach(printjson)
db.users.countDocuments({age: {$gte: 30}})

// 更新与删除
db.users.updateOne({name: "alice"}, {$set: {age: 31}})
db.users.updateMany({}, {$inc: {visits: 1}})
db.users.deleteOne({name: "bob"})
db.users.deleteMany({age: {$lt: 18}})

// 索引：1 = 升序，-1 = 降序
db.users.createIndex({name: 1})
db.users.createIndex({tenant: 1, createdAt: -1})   // 复合索引：tenant 升序，createdAt 降序
db.users.find({tenant: "acme"}).sort({tenant: 1, createdAt: -1})  // 与该复合索引的方向匹配
db.users.getIndexes()                              // key 中显示方向，如 {"name":1}
db.users.dropIndex("name_1")

// 删除数据
db.users.drop()
db.dropDatabase()
```

不进交互 shell，直接执行单条命令：

```bash
pg addon documentdb shell -- --quiet --eval 'db.getSiblingDB("demo_db").users.countDocuments({})'
pg addon documentdb shell --dsn 'mongodb://docdb_admin@10.0.0.5:10260/demo_db' \
  -- --quiet --eval 'db.users.find({age: {$gte: 30}}).forEach(printjson)'
```

- 交互模式下 `find()` 先显示前 20 条，输入 `it` 继续；`.forEach(printjson)` 可一次打印全部。
- `_id` 在每个集合内唯一、自动建立索引，省略时自动生成；按 `_id` 查询不需要额外索引。
- `drop()` 与 `dropDatabase()` 会不可逆地删除数据。只读 CLI 命令（`databases list`、`collections list`、`indexes list`）绝不写入；文档查询和写操作统一走 `shell`。
- DocumentDB 并未实现 MongoDB 的全部命令和查询操作符；遇到不支持的特性时参考上游兼容性说明。

说明：

- 本地模式下连接串由插件的 `pg.yaml` 配置（客户端用户、密码、端口、TLS 模式）生成；远程模式下来自 `--dsn`/`DOCDB_URI`。密码通过**仅传变量名**的环境变量进入容器，因此不会出现在 podman 的 argv（`podman inspect` 的 `Config.Cmd`）里。
- DocumentDB 只报告包含集合的数据库。列表为空时 pgcli 会在 **stderr** 打印一行说明，stdout 保持为空，脚本仍可把 stdout 当作纯数据。
- 巡检子命令有意保持只读。建/删数据库、集合、索引和用户等写操作暂未提供；需要写入时请用 `shell`（或 MongoDB driver）。
- 镜像内置 mongosh 的遥测关闭配置（`/home/mongosh/.mongodb/mongosh/config`，`{"enableTelemetry":false}`）；mongosh 2.14.1 启动时仍可能打印遥测提示。

## TLS

daemon 首次启动时自动生成自签证书，并持久化到命名 volume `pgcli-documentdb-tls-<name>`，因此容器重建后证书不变。

- `--tls-mode allow`（默认）：同一端口同时接受明文与 TLS。
- `--tls-mode require`：`SetupConfiguration.json` 内 `EnforceTls=true`，明文握手被拒绝。

切换模式需重建：

```bash
pg addon install documentdb --pg-name demo --tls-mode require --force
```

`require` 时按测试代码启用客户端 TLS（`MONGO_TLS=true`）；需要校验证书时改用 `tlsCAFile`。

## 生命周期

```bash
pg addon list                    # DocumentDB 区块：状态/端点/目标/TLS
pg addon start documentdb --name documentdb
pg addon stop  documentdb --name documentdb
pg logs addon documentdb         # 跟踪网关容器日志
pg addon password documentdb --name documentdb
pg addon remove documentdb --name documentdb [--clean-data]
```

`remove` 删除容器；`--clean-data` 还会删除 TLS 状态 volume 和渲染出的配置目录。**PostgreSQL 侧状态保留**：DocumentDB 数据、客户端用户、`docdb_gateway` 角色与 HBA 规则都不随 remove 删除。命令会打印手工清理用的 `DROP ROLE` 语句。

支持 autostart；`pg start --autostart` 中网关在实例启动**之后**启动，并先校验目标实例已就绪：

```bash
pg autostart enable --documentdb --name documentdb
```

## 多网关

每个网关有自己的名字、容器、端口和 TLS volume：

```bash
pg addon install documentdb --name analytics --pg-name demo --port 10261 --username docdb_analytics
```

> **多个网关共用同一目标数据库时，必须使用不同的 `--username`。** DocumentDB 用户就是 PostgreSQL role，第二个网关用相同用户名安装会轮换该 role 的密码，从而使第一个网关保存的凭据失效。

### 对共享 PostgreSQL 实例的影响

多个网关可以指向同一实例，但它们既不与实例隔离，也不彼此隔离：

| 方面 | 影响 |
|---|---|
| 连接数 | 每个网关至少占用一条 `docdb_gateway` 的 client backend（实测运行中的网关空闲时为 1 条）。负载下网关可能开更多连接。总连接数需要按 `max_connections` 计算；v1 在网关与 PostgreSQL 之间没有连接池层。 |
| CPU / 内存 / IO / 锁 | 所有网关的查询、写入和前台索引构建（`indexBuildsScheduledOnBgWorker=false`）都在同一实例上竞争资源。增加网关增加的是负载，不是容量。 |
| 数据可见性 | 不同 `--username` 只隔离凭据，**不隔离数据**。每个 DocumentDB 用户默认带 `readWriteAnyDatabase` + `clusterAdmin`，所有指向同一 database 的网关读写的是同一份集合。应用需要隔离时应使用不同 database 或不同实例；v1 不提供按集合授权的角色。 |
| Role | PostgreSQL role 是集群级的，因此各网关的客户端用户名必须唯一。`pg addon remove` 会保留各网关的客户端 role（需手工删除），而共享的 `docdb_gateway` 角色必须保留给其余网关使用。 |
| 后台 worker / `pg_cron` | 每个实例只配置一份，不随网关数量增加。多装网关不会重复启动 worker 或 cron job。 |

查看实际连接占用：

```bash
pg exec -i demo "SELECT usename, state, count(*) FROM pg_stat_activity WHERE backend_type = 'client backend' GROUP BY 1, 2 ORDER BY 3 DESC"
```

## 限制

- 仅本地 PG 18 实例；standalone（不支持 Patroni/HA）；不支持 `--dsn`。（`pg addon documentdb --dsn` 客户端参数是另一回事——它让 mongosh 连接别处的网关。）
- macOS 代码完成但未实测。
- TLS 仅支持自动生成的自签证书；暂不支持自定义证书。
- `pg destroy` 会删除实例的 socket volume。销毁目标实例**之前**先移除网关。
- 网关以 superuser 经本地 trust 连接；安全边界是宿主/容器边界。客户端都在本机时保持 `--listen 127.0.0.1`。

## 排障

```
WARN … Host Config file not able to be loaded: No such file or directory
```

standalone 网关没有动态 host 配置文件，这条警告在启动时记录一次，不影响运行。

网关连不上时，检查目标挂载与扩展：

```bash
podman inspect <pg container> --format '{{range .Mounts}}{{.Destination}}{{"\n"}}{{end}}'
pg exec -i demo "SELECT extname FROM pg_extension WHERE extname LIKE 'documentdb%' OR extname = 'pg_cron'"
```
