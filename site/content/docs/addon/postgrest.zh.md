---
title: "PostgREST"
description: "通过 PostgREST 插件将 PostgreSQL schema 暴露为 REST API"
weight: 46
---

PostgREST 是一个单进程、无状态的 Web 服务器，把一个 PostgreSQL schema 直接变成
RESTful API。作为 pgcli 插件，它是贴在任意 PG 端点前面的一个容器——没有数据目录、
没有渲染出的配置文件，全部通过 `PGRST_*` 环境变量配置。

PostgREST 是和 [PgBouncer](../pgbouncer/)、[PgDog](../pgdog/) 同类的**代理型**
插件：自身不保存状态。**两种部署模式**与 PgBouncer 对称：

- **本地模式：** `pg addon install postgrest -i <instance>`——作为 sidecar 存在
  `instances.<name>.addons.postgrest` 下，DSN 由该实例自动拼出。
- **远程模式：** `pg addon install postgrest --dsn <dsn> --pg-name <name>`——存在
  顶层 `addons.postgrest` 下。

两种模式里的 `--dsn` 都原样透传进容器的 `PGRST_DB_URI`，因此可以指向**任意**
PG 端点：一个直连的托管实例、一个 [PgBouncer](../pgbouncer/) 连接池，或一个 Patroni
集群前面的 [HAProxy](../haproxy/) 监听口。

## 平台支持

PostgREST 在 **Linux**（host 网络，`--network host`）和 **macOS**（容器加入
`pgcli-net` bridge 并发布端口，与 podman machine 下的 PG 实例同形）均可用。

- macOS 上**本地模式**开箱可用——容器经 bridge 访问托管实例。
- macOS 上**远程模式**：`--dsn` 必须是**从 Mac 本身**可达的地址，而不是 podman
  machine VM 的地址。指向一个 Mac 能解析的真实主机 IP（或指向 VM 上服务的
  `host.containers.internal`）。
- 客户端始终访问 `127.0.0.1:<port>`（或 `--listen`）；gvproxy 把发布端口转发到
  Mac 的 loopback。

## 工作原理

1. **`pg addon install postgrest`** 拉取镜像（若缺）并启动一个完全由 `PGRST_*`
   env 装配的容器。
2. 容器经 host 网络（Linux）或 `pgcli-net` bridge（macOS）访问后端 PG。
3. PostgREST 启动时内省暴露的 schema，对外提供 REST 服务，并在 `pgrst` 通道上
   LISTEN 等待 schema 缓存重载信号。

PostgREST 在主机上**不留任何数据**——`pg addon remove postgrest` 只停并删容器。

**容器名：** `pgcli-postgrest<ns>-<name>`（本地模式用实例名作为 `<name>`；远程模式
用 `--pg-name`）。namespace 隔离生效：两个不同 namespace 的配置可以各跑各的 API，
互不冲突。

## 命令

### 安装

```bash
# 本地模式：暴露一个托管实例的 schema
pg addon install postgrest -i mypg --schema api --anon-role web_anon

# 远程模式：任意 PG 端点（直连实例、pgbouncer、或 haproxy LB）
pg addon install postgrest \
  --dsn "postgres://api:pass@127.0.0.1:5000/appdb" \
  --pg-name app-api --schema api --anon-role web_anon

# 调整 PostgREST 面向后端的连接池大小
pg addon install postgrest -i mypg --db-pool 20

# 启用 JWT 认证（未认证请求仍回落到 --anon-role）
pg addon install postgrest -i mypg --schema api --anon-role web_anon --jwt-secret "$(openssl rand -hex 32)"
```

重复安装是幂等的：已存在的容器会被复用（停着的会被启动）。改过端口、监听地址、
DSN、db-pool、schema、anon-role 或 jwt-secret 后，加 `--force` 重建容器以生效。

**参数：**

| 参数 | 说明 | 默认 |
|------|------|------|
| `-i`, `--instance` | 托管实例（本地模式） | `default` |
| `--dsn` | 后端 PG URI（远程模式）；原样进 `PGRST_DB_URI` | — |
| `--pg-name` | 标识一个远程 PostgREST 的名字（配 `--dsn` 必填） | — |
| `--schema` | 暴露的 schema（可逗号分隔多个）；→ `PGRST_DB_SCHEMAS` | PostgREST 默认（`public`） |
| `--db-pool` | PostgREST 面向后端的连接池大小；→ `PGRST_DB_POOL` | PostgREST 默认（`10`） |
| `--anon-role` | 未认证请求所切换到的角色；→ `PGRST_DB_ANON_ROLE` | —（匿名访问关闭） |
| `--jwt-secret` | 验证 `Authorization: Bearer` JWT 所用的密钥；→ `PGRST_JWT_SECRET` | —（JWT 认证关闭） |
| `--port` | HTTP 主机端口 | 自动，取自 `postgrest_start_port`（基 3500） |
| `--listen` | 绑定地址 | `127.0.0.1` |
| `--image` | 容器镜像 | `docker.io/postgrest/postgrest:v16.3` |
| `--force` | 重建已存在的容器以应用改过的参数 | 关 |

> **`--db-pool` 与后端的 `max_connections`。** 一套 PostgREST 打开的 PG 连接总数
> 约为 *PostgREST 实例数 × `--db-pool`*。若在同一端点后跑多个副本，按此预算
> `max_connections`。

### 列表

```bash
pg addon list
```

PostgREST 出现在**本地插件**/**远程插件**段下，含 Status、REST URL、Backend、
Schema、DB pool、Anon role、JWT auth、Container。

### 启动 / 停止 / 移除 / 日志

```bash
pg addon start postgrest -i mypg
pg addon stop postgrest --pg-name app-api
pg addon remove postgrest -i mypg          # 无状态：只删容器
pg addon remove postgrest --pg-name app-api
pg logs addon postgrest -i mypg            # 本地容器日志
pg logs addon postgrest --pg-name app-api  # 远程容器日志
```

### 开机自启

```bash
pg autostart enable --postgrest -i mypg
pg autostart enable --postgrest --pg-name app-api
pg autostart status
```

自启是「只启动」语义：开机时按配置里已有的 `PGRST_*` env 拉起容器（若容器被删则
从配置重建）。请先安装插件。

## 配置

PostgREST 的配置在 `addons.postgrest`（远程）或 `instances.<name>.addons.postgrest`
sidecar（本地）下：

```yaml
postgrest_start_port: 3500      # HTTP 端口池基址

addons:
  postgrest:
    app-api:
      container_name: pgcli-postgrest-default-app-api
      name: app-api
      image_tag: docker.io/postgrest/postgrest:v16.3
      host_port: 3501
      listen: 127.0.0.1
      dsn: postgres://api:pass@10.0.0.20:35432/appdb
      backend_host: 10.0.0.20:35432
      db_pool: 4
      schemas: api
      anon_role: web_anon
      jwt_secret: <HS256共享密钥>   # 仅在给了 --jwt-secret 时才会出现
      autostart: false
```

省略 `--port` 时端口从 `postgrest_start_port` 端口池（基 3500）自动分配；该池独立
于 PgBouncer / etcd / pgdog / minio 的池。

## 连接 Patroni 集群

把 `--dsn` 指向集群的 **HAProxy 监听口**，而不是某个成员直连的 PG 端口。

```bash
# 正确：写请求经 LB 的读写口跟随 leader
pg addon install postgrest --dsn "postgres://api:pass@<lb-host>:5000/appdb" \
  --pg-name app-api --schema api --anon-role web_anon
```

成员直连口在 failover 后会失去写入（旧 leader 不再接受写，但 DSN 仍指着它）。
pgcli 在安装时会检测并给出警告，建议改指 HAProxy 监听口。

- **failover 自愈。** 后端 leader 变更后，PostgREST 的重连 + 重载 schema 缓存
  循环会经 LB 重新提供服务，无需 pgcli 干预。
- **横向扩容。** 在一个负载均衡器后面跑多个 PostgREST 副本；每个副本各自贡献
  `--db-pool` 份后端连接。

## 库侧准备（pgcli 不代管）

pgcli 只负责安装并运行 PostgREST 容器，**不碰你的数据库**。API 要能读到东西，
数据库侧需要先备好未认证请求所 `SET ROLE` 到的角色，以及对暴露 schema 的授权
——通常归你的 migration 管，不归 pgcli。以下这段可重复执行：

```sql
-- 暴露的 schema（即 --schema 的值；若用 public 可省略，它本就存在）：
CREATE SCHEMA IF NOT EXISTS api;

-- 未认证请求所切换到的 NOINHERIT 角色（即 --anon-role 的值）。
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'web_anon') THEN
    CREATE ROLE web_anon NOINHERIT NOLOGIN;
  END IF;
END $$;
GRANT USAGE ON SCHEMA api TO web_anon;
GRANT SELECT ON ALL TABLES IN SCHEMA api TO web_anon;
ALTER DEFAULT PRIVILEGES IN SCHEMA api GRANT SELECT ON TABLES TO web_anon;

-- SET ROLE 要求登录用户是目标角色的成员（DSN 用户是 superuser 时可省略）：
GRANT web_anon TO <dsn-user>;
```

> 角色视图的列名是 `rolname`，不是 `rolename`——写错会让整批语句回滚。
> `ALTER DEFAULT PRIVILEGES` 只对其**之后**新建的表生效；已存在的表由上面那条
> `GRANT ... ON ALL TABLES` 覆盖。

PostgREST 以 DSN 用户连接、每个请求再 `SET ROLE` 到 `web_anon`，因此 **API 携带的
就是这个角色的权限**：`web_anon` 只有 SELECT 就只能读，想开写权限就对该表
`GRANT INSERT/UPDATE/DELETE`。连接用的登录角色本身只需是请求所扮演角色的成员，
不需要别的权限。然后用 `--anon-role web_anon` 安装。认证请求的两种方式——匿名
（`--anon-role`）与 JWT（`--jwt-secret`）——见下文；两者都不设时，每个请求都会被
拒绝（401）。

PostgREST 会缓存它内省到的 schema。schema 变更后需要重载缓存：

```sql
NOTIFY pgrst, 'reload schema';
```

> **`LISTEN` 与事务池化。** PostgREST 依赖一条持久的 `LISTEN pgrst` 会话接收重载
> 通知。若后端跑在**事务**池化模式（PgBouncer 默认）下，这条会话级 LISTEN 会被打断
> ——通知送不到，经池化连接发出的 `NOTIFY pgrst` 同样送不到。**实测行为**：经
> 事务池化的 PgBouncer 发出的 reload 并**不能**刷新 PostgREST 的缓存（新表仍 404），
> 而且即便把 `NOTIFY` 直发到后端也会丢，因为 PostgREST 自己的 LISTEN 会话没有稳定
> 连接。要么让 PostgREST 连到**会话**池化或直连端点，要么**重启 PostgREST 容器**
> （启动时会重新内省）以拾取 schema 变更。

## 验证 API

安装输出会打印 REST URL（`http://127.0.0.1:<port>`）。先在暴露的 schema 里建一张
表，再访问 API 确认服务已就绪并能返回它的行：

```sql
CREATE TABLE api.widgets (id integer PRIMARY KEY, name text);
INSERT INTO api.widgets VALUES (1, 'bolt'), (2, 'nut');
GRANT SELECT ON api.widgets TO web_anon;
NOTIFY pgrst, 'reload schema';   -- 否则新表仍会 404
```

```bash
# OpenAPI 根——PostgREST 连上后端后即应答。
curl -s http://127.0.0.1:3500/ | head -c 120

# 暴露了哪些表/关系（取自 OpenAPI paths）：
curl -s http://127.0.0.1:3500/ | grep -o '"/[a-z_]*"'

# 读取暴露 schema 下某张表的行。路径里 schema 是隐含的——是 /widgets，
# 不是 /api.widgets：
curl -s "http://127.0.0.1:3500/widgets"
curl -s "http://127.0.0.1:3500/widgets?id=eq.1"   # 带过滤条件
```

全新安装时几点预期：

- 根路径可能需要一点时间才应答——schema 内省在启动后才跑，最初的请求可能返回
  `503`，直到 PostgREST 连上后端。
- 刚建的表返回 `404`、未认证请求返回 `401`，说明 schema 缓存过期或未设
  `--anon-role`——修法见下方**排障**表。

## JWT 认证

`--anon-role` 是最简单的模型：每个未认证请求都固定以某个角色运行。
`--jwt-secret` 则打开按请求划分的身份。安装时传入它（HS256 共享密钥，或用于
RS256 的 JSON Web Key）；pgcli 会作为 `PGRST_JWT_SECRET` 传进容器。

```bash
pg addon install postgrest -i mypg --schema api --anon-role web_anon \
  --jwt-secret "$(openssl rand -hex 32)"
```

> 若像上面那样内联命令替换，密钥**不会**进你的 shell 历史。但它会落到
> `pg.yaml` 里（pgcli 要靠它重建容器）——请把这个文件当作含密文件对待，改过
> 密钥后要加 `--force` 重新安装。

> **长度：** HS256 的密钥**至少 32 个字符**（256-bit 密钥材料；HS384 需 48、
> HS512 需 64）。更短的密钥会让 PostgREST **拒绝启动**——`openssl rand -hex 32`
> （64 字符）是稳妥的默认值。JWK（用于 RS256/ECDSA）没有这个下限，强度由 JWK
> 自身决定。

设了密钥后，任何带 `Authorization: Bearer <token>` 的请求，PostgREST 都会先验签，
再以 token 里 **`role`** claim 所指名的角色运行：

- token 必须用同一密钥签名；被篡改的一律 401。
- `role` claim 必须指到一个在暴露 schema 上有授权的角色（像上面的 `web_anon`
  那样建，`NOINHERIT`）。
- **DSN 里的登录角色必须是该角色的成员**，因为服务请求的本质就是 `SET ROLE`
  到它。所以对 `web_user` 这类 JWT 角色，还需要 `GRANT web_user TO <dsn-user>;`
  ——与上面匿名角色的要求相同，DSN 用户是 superuser 时同样可省略。缺这个成员
  关系，请求会失败并报 `permission denied to set role`。
- 若你的签发方用别的字段名，可用 `PGRST_JWT_ROLE_CLAIM_KEY` 改 claim 键——pgcli
  没有暴露该 flag，需要时直接在容器上设置。

`--anon-role` 与 `--jwt-secret` 可并存：**带**合法 JWT 的请求按其 `role` claim
运行，**不带**的请求回落到 `--anon-role`。两者都不设时，每个请求都被拒绝（401）。
常见组合是：公开读用匿名，认证写用 JWT 角色。

生产环境由你应用的认证服务签发 token。手搓一个用同一 HS256 密钥的测试 token：

```bash
b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
SECRET='<--jwt-secret 的值>'
HEADER=$(printf '{"alg":"HS256","typ":"JWT"}' | b64url)
PAYLOAD=$(printf '{"role":"web_anon","exp":%d}' $(( $(date +%s) + 3600 )) | b64url)
SIG=$(printf '%s.%s' "$HEADER" "$PAYLOAD" | openssl dgst -sha256 -hmac "$SECRET" -binary | b64url)
curl -s -H "Authorization: Bearer $HEADER.$PAYLOAD.$SIG" http://127.0.0.1:3500/widgets
```

## 排障

| 现象 | 可能原因 / 处理 |
|------|-----------------|
| 安装报 `cannot connect to source database` | 容器访问不到 DSN 的 host:port（macOS 上远程 `127.0.0.1` 指向 Mac 而非 VM）。用 `pg exec --dsn <dsn> "SELECT 1"` 验证。 |
| 所有请求返回 HTTP 401 `Anonymous access is disabled` | 未给 `--anon-role` 且请求没带合法 JWT——补 `--anon-role`，或用 `--jwt-secret` 安装并发一个签名 token。若已设 `--jwt-secret` 仍 401，说明签名或 `role` claim 不对。 |
| 写请求返回 HTTP 401，但 JSON 响应体里是 `42501` / `permission denied for table` | 请求所扮演角色（匿名角色或 JWT 的 `role` claim）缺该权限——PostgREST 在 HTTP 层把权限不足报成 401，真正的 SQLSTATE 只在响应体里。给请求实际使用的角色补授权。 |
| 新建的表/关系在 `NOTIFY pgrst` 后仍 404 / `PGRST205` | reload 没送达 PostgREST（见上文**事务池化**）。重启容器，或改用会话/直连。 |
| Patroni failover 后写请求立刻失败 | DSN 指到了成员直连口而非 HAProxy 读写口——安装时已警告。把 DSN 改指 LB。 |
| `--db-pool` 改动没生效 | 已存在的容器会被复用；加 `--force` 重装以重建。 |

## 相关

- [PgBouncer](../pgbouncer/)——连接池（把 PostgREST 的后端放它前面时，注意上面的事务池化坑）。
- [PgDog](../pgdog/)——池化 / 分片代理，同样是双模形态。
- [HAProxy](../haproxy/)——Patroni 集群下 PostgREST 的 DSN 应指向的监听口。
- [HA 集群](../../ha-cluster/)——`--dsn` 所指向的 Patroni 拓扑。
