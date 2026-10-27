---
title: "ClickHouse"
description: "以受管容器方式运行单机 ClickHouse 列式 OLAP 数据库——内置认证、SQL 辅助命令、用户/授权、配置覆盖与 BACKUP/RESTORE"
weight: 64
---

`pg addon install clickhouse` 以受管容器方式运行 **ClickHouse 26.8 LTS**——单机列式 OLAP 数据库——直接跑官方 `docker.io/clickhouse/clickhouse-server` 多架构镜像。与 VictoriaMetrics、InfluxDB 一样，它是**顶层插件**，不前置任何 PostgreSQL 实例。

ClickHouse 用两个端口承载两种协议：**HTTP**（curl、JDBC/ODBC 以及大量驱动）和**原生 TCP 协议**（`clickhouse-client`）。pgcli 以 host 网络运行它，因此两者都是直接的宿主端口。**认证始终开启**——安装时创建 admin 账号，密码存入 `pg.yaml`。

> **admin 密码在安装时生成，可随时用
> `pg addon password clickhouse [--name NAME]` 取回。** HTTP 与原生监听对每个请求
> 都要求认证，不存在匿名访问。`--listen` 默认 `127.0.0.1`——需要其他主机连接时
> 显式传 `0.0.0.0`，并用 [nginx](./nginx/) 前置做 TLS。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `clickhouse` | 实例名（map 键；也是各子命令的默认名） |
| `--version` | `26.8.36.3` | 上游镜像 tag，直接从 `docker.io/clickhouse/clickhouse-server` 拉取 |
| `--data-dir` | `<base_dir>/addon/clickhouse/<name>/data` | 数据目录，绑定挂载到 `/var/lib/clickhouse`；必须是绝对路径 |
| `--listen` | `127.0.0.1` | HTTP 与原生监听的绑定地址。`0.0.0.0` 会把两者暴露到网络 |
| `--http-port` | 自动（基址 `8123`） | HTTP 宿主端口；`0` = 自 `clickhouse_http_start_port` 自动分配 |
| `--tcp-port` | 自动（基址 `9000`） | 原生协议宿主端口；`0` = 自 `clickhouse_tcp_start_port` 自动分配（与 S3 API 池共享区间） |
| `--admin-user` | `admin` | 由镜像 entrypoint 创建的 admin 账号 |
| `--admin-password` | 自动生成 | admin 密码。`--force` 时默认保留已存密码，除非显式指定 |
| `--config-option K=V` | — | 额外服务端设置，点分 XML 路径（`logger.level=debug`、`max_server_memory_usage=2147483648`）；可重复；替换已存覆盖 map |
| `--force` | false | 重新渲染配置与 secret 并重建容器；保留数据、已存密码与已存覆盖项 |

ClickHouse 不适用 `--image`——请用 `--version`。

生成的配置只固定监听、端口、日志与备份盘；其余服务端设置全部沿用上游默认值。覆盖项存在 `pg.yaml` 的 `addons.clickhouse.<name>.settings` 下并渲染进生成的 XML，因此重启与容器重建都不会丢失。

## 安装

```bash
pg addon install clickhouse --name ch1
```

```
✓ ClickHouse "ch1" installed
  Version:   26.8.36.3
  Image:     docker.io/clickhouse/clickhouse-server:26.8.36.3
  Container: pgcli-clickhouse-ch1
  HTTP:      http://127.0.0.1:8123
  TCP:       127.0.0.1:9000
  Admin:     admin
  Password:  <自动生成>
  Data dir:  /home/you/.pgcli/addon/clickhouse/ch1/data
  Backups:   /home/you/.pgcli/addon/clickhouse/ch1/backups
  Config:    /home/you/.pgcli/addon/clickhouse/ch1/config.d/zz-pgcli.xml
```

第二个实例会自动从各自端口池取下一个空闲 HTTP/TCP 端口（例如 `8124`/`9001`），并可使用自定义数据目录：

```bash
pg addon install clickhouse --name analytics --data-dir /srv/clickhouse-data --listen 0.0.0.0
```

不带 `--force` 重复 `install` 是幂等空操作，只报告当前状态。`--force` 会重新渲染配置、重建容器，并**保留数据目录、已存 admin 密码与已存设置**；只有想替换覆盖 map 时才需要重新传 `--config-option`。

镜像直接从 Docker Hub 拉取，不涉及 pgcli 自建镜像。`--version` 接受任意合法的上游 tag，但该 tag 必须存在于 registry，否则拉取失败。pgcli 默认固定当前 LTS 线（`26.8.36.3`）。

## 端口

| 端点 | 默认 | 协议 | 端口池 |
|---|---|---|---|
| HTTP | `8123` | HTTP（查询、`/ping`、大量驱动） | `clickhouse_http_start_port`，默认 `8123` |
| Native | `9000` | ClickHouse 原生 TCP（`clickhouse-client`） | `clickhouse_tcp_start_port`，默认 `9000`，**与 S3 API 池共享** |

两个池共享同一个“已分配”集合，因此一个池中的显式端口不会与另一个池自动分配的端口相撞。`--http-port` 与 `--tcp-port` 必须不同，且所有 ClickHouse 实例的 HTTP/TCP 端口必须全局唯一。自动分配会跳过宿主上已在监听的端口。

当 `--listen` 为通配地址（`0.0.0.0`、`::`）时，pgcli 自己的客户端命令会拨 `127.0.0.1`；打印的端点同样遵循此规则。LAN `--listen` 绑定在容器内仍会保留 loopback，保证容器内的 `clickhouse-client` 始终可用。

## pgcli 创建的内容

单个实例的全部状态位于 `<base_dir>/addon/clickhouse/<name>/`：

| 宿主路径 | 容器路径 | 说明 |
|---|---|---|
| `config.d/` | `/etc/clickhouse-server/config.d` | 只读；替换镜像自带的 `config.d`（否则会监听所有网卡）。`zz-pgcli.xml` 是生成的覆盖文件 |
| `data/` | `/var/lib/clickhouse` | 数据库数据与系统表 |
| `logs/` | `/var/log/clickhouse-server` | `clickhouse-server.log` 与 `clickhouse-server.err.log`（也可经 `podman logs` 查看） |
| `backups/` | `/var/lib/clickhouse-backups` | `backup`/`restore` 使用的受管 `backups` 盘 |
| `secrets/admin_password` | `/run/secrets/admin_password` | 权限 `0600`，entrypoint 经 `CLICKHOUSE_PASSWORD_FILE` 读取 |

生成的 `zz-pgcli.xml` 设置：

- `listen_host`（完全等于 `--listen` 的选择）与 `listen_try`
- `http_port` / `tcp_port`
- `logger.level`、`logger.log`、`logger.errorlog`、`logger.console`、日志轮转（`size`、`count`）
- 位于 `/var/lib/clickhouse-backups` 的本地盘 `backups`，并允许其用于 `BACKUP`/`RESTORE`

容器以 `--network host`、`--restart unless-stopped`、30 秒停止超时和 `nofile=262144` 运行。数据在容器内的属主是 uid/gid 101（镜像 entrypoint 以 root 完成挂载目录 chown 后再降权）。

## 凭据

```bash
pg addon password clickhouse --name ch1   # 打印 admin 密码
pg addon list                             # 查看 Analytics add-ons 区块
```

镜像 entrypoint 会创建 `admin`（或 `--admin-user` 指定的名字）并赋予全部权限，同时开启访问管理（`CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1`），因此本 CLI 发出的 `CREATE USER` / `GRANT` 可以生效。密码存入 `pg.yaml`（默认权限 `0644`——请保护该文件），并同步写入挂载进容器的 `0600` secret 文件。

所有与 ClickHouse 通信的 pgcli 命令都通过环境变量传递凭据、在容器内展开；密码绝不出现在 `podman` 命令行或宿主进程列表中。SQL 经 stdin 传入。

## 连接

用打印出的端点即可从任意宿主工具连接：

```bash
# HTTP 接口（Basic 认证）
curl -sS 'http://127.0.0.1:8123/?query=SELECT%20version()' \
  --user "admin:$(pg addon password clickhouse --name ch1)"

# 原生协议
clickhouse-client --host 127.0.0.1 --port 9000 --user admin --password
```

pgcli 不会在宿主上安装 `clickhouse-client`——请用下面的 `query`/`shell` 子命令，或让外部客户端连原生端口。`/ping` 无需认证并返回 `Ok.`，是 pgcli 启动时轮询的健康检查。

## Shell 与查询

```bash
pg addon clickhouse shell --name ch1                  # 进入容器内 bash
pg addon clickhouse query --name ch1 'SELECT version()'
pg addon clickhouse query --name ch1 --database demo 'SELECT count() FROM events'
pg addon clickhouse query --name ch1 --file schema.sql
echo "SELECT 1" | pg addon clickhouse query --name ch1 -
```

`query` 以 admin 用户运行 `clickhouse-client`，结果写到 stdout，可直接管道或重定向。语句由位置参数拼接；`--file` 读取宿主文件（宿主读取后经 stdin 传入，无需挂载）；`-` 表示从 stdin 读取。客户端调用时未加 `--multiquery`，因此**每次调用只传一条语句**。

`query`、`database`、`user`、`grant`、`backup`、`restore` 都要求实例正在运行；`shell` 在容器停止时仍会尝试打开，但会打印警告。

```sql
CREATE TABLE demo.events
(
    id   UInt64,
    ts   DateTime,
    name String
)
ENGINE = MergeTree
ORDER BY id;
```

```bash
pg addon clickhouse query --name ch1 --database demo --file events.sql
echo "INSERT INTO demo.events VALUES (1, now(), 'boot')" | \
  pg addon clickhouse query --name ch1 --database demo -
pg addon clickhouse query --name ch1 --database demo 'SELECT * FROM demo.events ORDER BY id'
```

## 数据库

```bash
pg addon clickhouse database create demo --name ch1
pg addon clickhouse database create logs --engine Atomic --comment 'application logs' --if-not-exists --name ch1
pg addon clickhouse database list --name ch1
pg addon clickhouse database drop demo --name ch1 --yes
```

`create` 接受裸 engine 名（默认 `Atomic`；不暴露 engine 参数）。`drop` 是破坏性操作，除非传 `--yes`，否则会要求确认。`database list` 展示 `system.databases` 中的 `name`、`engine` 与 `data_path`。`db` 是 `database` 的别名。

## 用户与权限

```bash
# 显式密码与默认数据库
pg addon clickhouse user create app --password 'change-me' --default-database demo --name ch1

# 省略密码：自动生成并打印一次
pg addon clickhouse user create reader --default-database demo --readonly --name ch1

# 限制来源地址 / 主机名（可重复；默认 HOST ANY）
pg addon clickhouse user create etl --host-ip 10.0.0.0/8 --host-ip 192.168.1.10 --name ch1
pg addon clickhouse user create svc --host-name app.internal --name ch1
pg addon clickhouse user create localonly --host-local --name ch1

pg addon clickhouse user list --name ch1
pg addon clickhouse user drop app --name ch1 --yes
```

用户以 `IDENTIFIED WITH sha256_password` 创建。未给任何 host 参数时默认 `--host-any`；`--host-local`、`--host-ip`（IP 或 CIDR）与 `--host-name` 会追加 `HOST` 子句。`--readonly` 追加 `SETTINGS readonly = 1`。自动生成的密码只打印一次且**不会**存入 `pg.yaml`——请自行保存。`--if-not-exists` 让 `user create` 幂等。

```bash
# 单库读写
pg addon clickhouse grant app --read --write --database demo --name ch1

# DDL 关键字 = CREATE, ALTER, DROP, TRUNCATE, OPTIMIZE
pg addon clickhouse grant app --ddl --database demo --name ch1

# 表级范围（必须显式指定 database）
pg addon clickhouse grant reader --read --database demo --table events --name ch1

# 显式列表、通配范围、grant option
pg addon clickhouse grant app --privileges 'SELECT,INSERT' --name ch1
pg addon clickhouse grant app --all --with-grant-option --name ch1

pg addon clickhouse grant list app --name ch1
pg addon clickhouse grant app --read --database demo --revoke --name ch1
```

`--read` 即 `SELECT`，`--write` 即 `INSERT`，`--ddl` 展开为 `CREATE, ALTER, DROP, TRUNCATE, OPTIMIZE`，`--all` 授予 `ALL`，`--privileges` 接受逗号分隔列表（会转大写并与 flag 选择去重）。不给 `--database` 时目标是 `*`（所有数据库）；`--table` 需要显式 database。`--revoke` 发出 `REVOKE ... FROM`。`grant list` 运行 `SHOW GRANTS FOR`。

这些命令都以实例的 admin 账号执行；CLI 从不保存每个用户的密码或凭据。

## 配置

服务端设置用**点分 XML 路径**寻址；叶子值就是生成 XML 中该元素的文本。

```bash
# 安装时
pg addon install clickhouse --name ch1 \
  --config-option max_server_memory_usage=2147483648 \
  --config-option logger.level=warning

# 运行中的实例
pg addon clickhouse config list --name ch1
pg addon clickhouse config get max_server_memory_usage --name ch1
pg addon clickhouse config set max_server_memory_usage=2147483648 --name ch1
pg addon clickhouse config set logger.level=debug query_log.flush_interval_milliseconds=1000 --name ch1
pg addon clickhouse config unset logger.level --name ch1
pg addon clickhouse config show --name ch1        # 完整生成的 XML
```

- `config set KEY=VALUE ...` 接受一个或多个键值对，校验后重写 XML 与 `pg.yaml`，然后**重启运行中的容器**使改动立即生效。传 `--no-restart` 可推迟到下次启动。
- `config unset KEY ...` 删除覆盖项（一个或多个），重启规则相同。unset 不存在的键会报错。
- `config get` 打印自定义覆盖或受管设置的有效值；`config list` 先列受管设置，再按 key 排序列出自定义覆盖。
- `config show` 渲染与写入 `config.d/zz-pgcli.xml` 相同的 XML；不触碰文件或容器。
- 安装时的 `--config-option` 会**替换**整个覆盖 map；不传则保留已存内容。

`logger.level` 特殊处理：它折叠进受管的 `<logger>` 块，而不是产生第二个 `<level>` 元素。合法级别为 `trace`、`debug`、`information`、`warning`、`error`、`fatal` 和 `none`。

pgcli 受管的键会被拒绝，因为覆盖它们要么产生重复 XML，要么与已存路径/端口静默冲突：`listen_host`、`listen_try`、`http_port`、`https_port`、`tcp_port`、`interserver_http_port`、`interserver_http_host`、`mysql_port`、`postgresql_port`、`path`、`tmp_path`、`user_files_path`、`format_schema_path`、`access_control_path`、`backups.*`、`user_directories`、`profiles`、`users`、`storage_configuration.disks`、`storage_configuration.policies`，以及除 `logger.level` 外的所有 `logger.*` 键。含空格的键或含控制字符的值同样会被拒绝。

常用设置：

```bash
pg addon clickhouse config set max_server_memory_usage=2147483648 --name ch1
pg addon clickhouse config set background_pool_size=8 max_concurrent_queries=100 --name ch1
pg addon clickhouse config set query_log.flush_interval_milliseconds=1000 --name ch1
pg addon clickhouse config set merge_tree.max_suspicious=0 --name ch1
```

本 CLI 不支持服务端设置的热加载；每次有效变更都会重启或重建容器（ClickHouse 在启动时读取该生成文件）。

pgcli 只校验 key 形式与受管键规则——**值**由 ClickHouse 在启动时校验。被它拒绝的值（例如低于 merge/mutation 健全性检查要求的 `background_pool_size`）会中止启动，而容器的 `unless-stopped` 策略会把它变成重启循环。按下面步骤恢复，不会丢数据：

```bash
pg addon stop clickhouse --name ch1
pg addon clickhouse config unset background_pool_size --no-restart --name ch1
pg addon start clickhouse --name ch1
```

## 备份与恢复

备份使用 ClickHouse 的 SQL `BACKUP` 命令写入本地 `backups` 盘（`/var/lib/clickhouse-backups`）。备份是**在线**的，实例必须正在运行。这里只暴露受管的本地盘——没有配置 S3 目标。

```bash
pg addon clickhouse backup --name ch1 --database demo
# ✓ backup written: <instance dir>/backups/demo-20261027-120000.zip (… bytes)

# 显式文件名或备份目录内的嵌套路径
pg addon clickhouse backup --name ch1 --database demo --out nightly.zip
pg addon clickhouse backup --name ch1 --database demo --out archive/2026-q4.zip

pg addon clickhouse backups --name ch1
pg addon clickhouse restore demo-20261027-120000.zip --name ch1 --database demo
pg addon clickhouse restore demo-20261027-120000.zip --name ch1 --database demo --as demo_restored
```

- 两个命令都必须给 `--database`：它表示备份中记录的数据库名（v1 没有“全部数据库”形式）。
- `--out` 在备份目录内时按相对路径解析，否则按绝对路径；绝对路径必须位于该目录内，且文件必须以 `.zip` 结尾。默认名为 `<database>-YYYYMMDD-HHMMSS.zip`（UTC）。
- `restore` 接受**备份目录内**的文件，外加记录中的数据库名。恢复到已存在的数据库会失败（ClickHouse 拒绝覆盖）；用 `--as NEW` 并存恢复。
- restore 本身是在线的，但需要足够空间与运行中的实例；`backups` 按最新优先列出 `.zip` 文件。

备份目录就是普通宿主目录——真正的灾备请把 `.zip` 复制到别的机器。没有定时备份；需要时把 `pg addon clickhouse backup` 接进自己的定时任务。

```bash
# 异地复制
rsync -a /home/you/.pgcli/addon/clickhouse/ch1/backups/ backup-host:/backups/clickhouse-ch1/
```

## 生命周期

```bash
pg addon start clickhouse --name ch1      # 不存在则创建，然后等待 /ping
pg addon stop clickhouse --name ch1
pg addon clickhouse status --name ch1     # 状态、ping、端点、版本、路径、覆盖项数量
pg logs addon clickhouse --name ch1 -f    # 容器日志（stdout/stderr）
pg addon remove clickhouse --name ch1     # 移除容器与 pg.yaml 条目，保留数据
pg addon remove clickhouse --name ch1 --clean-data   # 同时删除实例目录与自定义数据目录
```

`start` 在启动前会从 `pg.yaml` 重新渲染生成的配置与密码文件，因此停止中的实例会拾取 `config set --no-restart` 的改动与变更后的 admin 密码。HTTP `/ping` 可应答时，`status` 报告 `running (ping ok)`。

不带 `--clean-data` 时，`remove` 会在磁盘上保留 `<实例目录>`（数据、配置、日志、备份、secret）与自定义 `--data-dir`；同名重装即可复活数据，并复用保留的 admin 密码（用 `--admin-password` 可覆盖）。`--clean-data` 会同时删除实例目录与自定义数据目录（uid 101 属主的文件经 `podman unshare` 删除），但从不删除已拉取的镜像。

重启后自动启动：

```bash
pg autostart enable --clickhouse --name ch1
pg autostart status
pg autostart disable --clickhouse --name ch1
```

开机启动同样会先重渲染配置与 secret，再启动现有容器。`pg start` 会在 nginx 之前启动所有启用 autostart 的 ClickHouse 实例，因此指向 ClickHouse HTTP 端口的 nginx 后端在代理启动时已经就绪。

## 生产注意事项

- **仅单机。** 本插件没有复制、分片与 HA。ReplicatedMergeTree/Keeper 拓扑不在范围内；备份是灾备路径。
- **无 TLS 监听。** HTTP 与原生端口都是明文。需要带 TLS 在网络中暴露 ClickHouse 时，用 [nginx 插件](./nginx/) 前置 HTTP 端口（或使用外部代理）；不要把原生端口直接暴露给不可信网络。
- **默认仅回环。** `--listen` 默认 `127.0.0.1`。认证虽然始终开启，但网络绑定仍会扩大攻击面；只在可信网络中使用 `0.0.0.0`。
- **密码存储。** admin 密码位于 `pg.yaml`（默认 `0644`）；请保护该文件，并优先用 `pg addon password clickhouse` 取用，而不是四处复制。
- **资源占用。** pgcli 不限制容器的 CPU/内存。ClickHouse 会按宿主内存推导预算；共享宿主上请显式设置 `max_server_memory_usage`（并考虑 `max_concurrent_queries`、`background_pool_size`），用 `config set` 完成。
- **升级。** `--force --version <新版本>` 会拉取新 tag 并在同一数据目录上重建容器。ClickHouse 只支持向前升级——绝不要让旧版本服务端启动在新版本写过的数据上。升级前先备份。
- **数据属主。** 数据文件属主为 uid 101。请用 `--clean-data` 而不是手工删目录；pgcli 会处理权限回退。
- **平台。** 仅 Linux（host 网络）；官方镜像为多架构（amd64/arm64）。

## CLI 参数速查

### `pg addon install clickhouse`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `clickhouse` | 实例名（map 键）；`[A-Za-z0-9][A-Za-z0-9_-]*`，最长 48 字符 |
| `--version TAG` | `26.8.36.3` | `docker.io/clickhouse/clickhouse-server` 的上游镜像 tag |
| `--data-dir DIR` | `<base_dir>/addon/clickhouse/<name>/data` | 绝对宿主数据目录，挂载到 `/var/lib/clickhouse` |
| `--listen ADDR` | `127.0.0.1` | 两个监听的绑定地址 |
| `--http-port N` | 自动（基址 `8123`） | HTTP 宿主端口；`0` = 自动分配 |
| `--tcp-port N` | 自动（基址 `9000`） | 原生宿主端口；`0` = 自动分配；必须与 `--http-port` 不同 |
| `--admin-user USER` | `admin` | admin 账号名；`[A-Za-z0-9_.-]{1,64}` |
| `--admin-password PW` | 自动生成 | admin 密码；`--force` 时默认保留已存值，除非显式指定 |
| `--config-option K=V` | — | 点分 XML 路径设置；可重复；替换已存覆盖 map |
| `--force` | false | 重渲染配置/secret 并重建容器，保留数据、密码与设置 |

### `pg addon clickhouse status` / `shell` / `query [SQL...]`

| 参数/flag | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `clickhouse` | 目标实例 |
| `SQL...` | — | 仅 `query`：由位置参数拼接的 SQL |
| `--database DB` | — | 仅 `query`：追加 `--database DB` |
| `--file FILE` | — | 仅 `query`：从宿主文件读取 SQL（`-` = stdin） |
| `-`（位置参数） | — | 仅 `query`：从 stdin 读取 SQL |

### `pg addon clickhouse database create|list|drop`

| 参数/flag | 默认值 | 说明 |
|---|---|---|
| `NAME` | — | 数据库名（`create`/`drop`，位置参数） |
| `--engine NAME` | `Atomic` | 裸 engine 名（不含参数） |
| `--comment TEXT` | — | 数据库注释 |
| `--if-not-exists` | false | 仅 `create` |
| `--if-exists` | false | 仅 `drop` |
| `--yes` | false | 仅 `drop`：跳过确认 |

`db` 可作为 `database` 的别名。

### `pg addon clickhouse user create|list|drop`

| 参数/flag | 默认值 | 说明 |
|---|---|---|
| `NAME` | — | 用户名（`create`/`drop`，位置参数） |
| `--password PW` | 自动生成 | 密码；省略时生成并打印一次 |
| `--default-database DB` | — | `DEFAULT DATABASE` 子句 |
| `--host-any` | 隐含 | 允许任意主机（没有任何 host flag 时生效） |
| `--host-local` | false | 仅 `LOCAL`（回环） |
| `--host-ip IP\|CIDR` | — | 可重复的 `IP` host 子句 |
| `--host-name NAME` | — | 可重复的 `NAME` host 子句 |
| `--readonly` | false | 追加 `SETTINGS readonly = 1` |
| `--if-not-exists` | false | 仅 `create` |
| `--if-exists` | false | 仅 `drop` |
| `--yes` | false | 仅 `drop`：跳过确认 |

### `pg addon clickhouse grant USER` / `grant list USER`

| 参数/flag | 默认值 | 说明 |
|---|---|---|
| `USER` | — | 目标用户（位置参数） |
| `--read` | false | 授予 `SELECT` |
| `--write` | false | 授予 `INSERT` |
| `--ddl` | false | 授予 `CREATE, ALTER, DROP, TRUNCATE, OPTIMIZE` |
| `--all` | false | 授予 `ALL` |
| `--privileges LIST` | — | 额外逗号分隔权限，与上述 flag 合并 |
| `--database DB` | 全部（`*`） | 限定到某个数据库 |
| `--table TABLE` | 全部表 | 限定到某张表；需要 `--database` |
| `--revoke` | false | 发出 `REVOKE ... FROM` 而非 `GRANT ... TO` |
| `--with-grant-option` | false | 追加 `WITH GRANT OPTION`（仅授予时） |

### `pg addon clickhouse backup` / `restore FILE` / `backups`

| 参数/flag | 默认值 | 说明 |
|---|---|---|
| `FILE` | — | 仅 `restore`：备份目录内的 `.zip` 文件 |
| `--database DB` | 必需 | 要备份的数据库 / 备份中记录的数据库名 |
| `--out NAME` | `<db>-<UTC 时间戳>.zip` | 仅 `backup`：备份目录内的输出路径 |
| `--as NEW` | — | 仅 `restore`：恢复到新数据库名 |

### `pg addon clickhouse config list|get|set|unset|show`

| 参数/flag | 默认值 | 说明 |
|---|---|---|
| `KEY=VALUE...` | — | 仅 `set`：一个或多个点分 XML 设置 |
| `KEY...` | — | `get`/`unset`：一个设置键（`get` 只接受一个） |
| `--name NAME` | `clickhouse` | 目标实例 |
| `--no-restart` | false | 仅 `set`/`unset`：下次启动生效，而不是立即重启 |

### 生命周期、凭据、日志、autostart

| 命令 | 参数 | 说明 |
|---|---|---|
| `pg addon start clickhouse` | `--name NAME` | 启动（不存在则创建），等待 `/ping` |
| `pg addon stop clickhouse` | `--name NAME` | 停止容器，保留数据 |
| `pg addon remove clickhouse` | `--name NAME`、`--clean-data` | 移除容器与配置条目；`--clean-data` 同时删除实例目录与自定义数据目录 |
| `pg addon password clickhouse` | `--name NAME` | 打印 admin 密码 |
| `pg logs addon clickhouse` | `--name NAME`、`-n N`（默认 50）、`-f` | 容器日志 |
| `pg autostart enable\|disable` | `--clickhouse`、`--name NAME` | 切换单个实例的开机启动 |

### 全局参数

| 参数 | 说明 |
|---|---|
| `-c, --config FILE` | 配置文件（默认 `~/.pgcli/pg.yaml`） |
| `-o, --output FILE` | 输出配置路径（默认同一文件） |
| `-i, --instance NAME` | PostgreSQL 实例选择器——ClickHouse 不使用；请改用 `--name` |
