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
  Endpoint:   mongodb://docdb_admin@127.0.0.1:10260/?directConnection=true
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

- 仅本地 PG 18 实例；standalone（不支持 Patroni/HA）；不支持 `--dsn`。
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
