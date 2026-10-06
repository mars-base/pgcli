---
title: "Redis"
description: "以 pgcli 插件方式运行 Redis——用于缓存/会话/排行榜/原子计数的独立键值存储，大版本 7 / 8 可选"
weight: 50
---

[Redis](https://redis.io/) 是应用需要缓存、会话后端、排行榜（有序集合）或
原子计数时最常选的内存键值存储。pgcli 将其作为**独立的顶层插件**运行，CLI
表面与对象存储一致（install / start / stop / logs / autostart / remove），
全部通过 `pg.yaml` 管理。

它是整个插件族里刻意做得最简单的一个：

- **一个容器、一个端口、一个密码。** `requirepass` 在首次安装时自动生成并
  存入 `pg.yaml`——不存在"未认证的 Redis"这种可达状态。
- **版本选择。** 首个支持版本选择的插件：`--version 7|8` 通过内置映射表解析
  到固定的 `docker.io/library/redis` 标签（7 → `7.4.11`，8 → `8.10.2`，默认
  8）。`--image` 仍可绕过映射表直接指定任意标签。
- **RDB 快照持久化。** 数据集以 `dump.rdb` 形式落在 bind 挂载的主机目录
  ——重启后仍在，`pg addon remove`（不带 `--clean-data`）后也仍在，重装同名
  实例即可复活数据。

在 PostgreSQL 实例旁边做缓存/会话/排行榜/计数就选 Redis；它与 PG 栈无关，可
与任何插件并存，同一主机也能跑多个实例（甚至跨大版本）。

> **范围之外：** 仅单机。Sentinel（高可用）与 Redis Cluster 不由 pgcli 管理
> ——见[已知限制](#已知限制)。

## 参数设置

pgcli 刻意把 Redis 的参数面做得很小：一个可调项对应一个 flag，最终都落到
`redis-server` 的原生选项上——经镜像自带 entrypoint 用 argv 覆盖，不生成
`redis.conf`；一个实例一个容器，直接用纯上游镜像（pgcli 不为 Redis 构建
wrapper，这点与 [rustfs](../rustfs/) 不同）。下表列出每个可调项、默认值，以及
Pigsty [REDIS 模块参数](https://pigsty.cc/docs/redis/param/) 里的对应项供参照：

| Flag / `pg.yaml` 键 | 默认值 | Redis 选项 | Pigsty 对应参数 |
|---|---|---|---|
| `--version` / `version` | `8` | —（决定镜像 tag） | `redis_type`（引擎选择，最接近的对应） |
| `--image` / `image_tag` | 由版本映射表解析 | — | 版本/包选择 |
| `--port` / `port` | 从 `redis_start_port` 自动分配（基 **6379**） | `--port` | `redis_instances` 的实例键 |
| `--listen` / `listen` | `0.0.0.0` | `--bind` | `redis_bind_address` |
| `--password` / `password` | 自动生成 20 字符 | `--requirepass` | `redis_password`——Pigsty 默认留空即禁用密码，pgcli 始终要求一个 |
| `--maxmemory` / `maxmemory` | 不设（无上限） | `--maxmemory` + `--maxmemory-policy allkeys-lru` | `redis_max_memory` + `redis_mem_policy`（pgcli 把策略钉死为 `allkeys-lru`） |
| `--data-dir` / `data_dir` | `<base-dir>/addon/redis/<name>/data` | `--dir /data`（主机目录 bind 挂载到 `/data`） | `redis_fs_main` |
| — | `autostart: false` | — | —（pgcli 层：`pg autostart enable --redis`） |

固定、不对外暴露为 flag 的策略：

- **`requirepass` 始终开启**——即使手工往 `pg.yaml` 里塞空 `password`，也不存在
  "未认证的 Redis" 这种可达状态。
- **持久化仅 RDB 快照**——沿用 Redis 自身的默认 `save` 计划，外加停机时补一份
  最终快照（`--stop-timeout 30` 给足时间）与启动时从 `dump.rdb` 回放。这与
  Pigsty `redis_rdb_save: ['1200 1']` 的意图一致；AOF（`redis_aof_enabled:
  false`）两边都刻意关着。
- **设了 `--maxmemory` 就把 `--maxmemory-policy` 钉为 `allkeys-lru`**：上限的
  语义是"缓存，超了就驱逐"，不是"硬存储"。要严格不驱逐就别设 `--maxmemory`，
  改为按主机容量规划。

Pigsty 参数中 pgcli 不管理的那些：多节点拓扑相关的一切（`redis_mode:
sentinel|cluster`、`redis_cluster_replicas`、`redis_sentinel_monitor`、实例级
`replica_of`）、配置文件模板（`redis_conf`）、危险命令重命名
（`redis_rename_commands`）、监控（`redis_exporter_*`——需要的话自行跑一个
`redis_exporter` 容器）。`REDIS_REMOVE` 只部分对应：`--clean-data` 即
pgcli 的 `redis_rm_data`；`redis_safeguard` 与 `redis_rm_pkg` 在"主机上没装任何
东西"的容器模型下没有意义。唯一能靠 `--image` 变通的是 `redis_type: valkey`——
它不过是另一个 tag：`pg addon install redis --image
docker.io/valkey/valkey:8` 可用，大版本反解析为 `8`。

## 版本选择

`--version` 选大版本；映射表把每个大版本钉到一个固定的补丁标签：

| 大版本 | 默认镜像标签 | 选择方式 |
|--------|--------------|----------|
| 7 | `docker.io/library/redis:7.4.11` | `pg addon install redis --name cache --version 7` |
| 8 | `docker.io/library/redis:8.10.2` | `pg addon install redis --name cache`（默认 8） |

解析规则：

- `--version 7`（或 `8`）→ 表中 tag 存入 `image_tag`，大版本存入 `version`；
- `--image <tag>` → 以你的 tag 为准；大版本从 tag 反解析出来仅供展示
  （`redis:7.2.4` → `7`），解析不出则留空（展示时原样显示 tag）；
- 两者都不给 → 大版本 `8`；
- 未知的 `--version` → 报错并列出可用大版本。

两个大版本可在同一主机并存——tag 不同、端口各自从同一池分配。表中 tag 跟随
各大版本的最新补丁，与 `docs/images.md` 的条目一起升级。

## 安装

```bash
# 默认：大版本 8，实例名 "redis"，端口从 6379 自动分配
pg addon install redis

# 命名的大版本 7 缓存实例，带内存上限（到上限即驱逐）
pg addon install redis --name legacy --version 7 --maxmemory 256mb

# 收紧为仅回环监听（默认是全网卡）
pg addon install redis --name cache --listen 127.0.0.1

# 固定密码（跨重装保持稳定）与固定端口
pg addon install redis --name sessions --password 'S3ssions!' --port 6379
```

安装摘要打印客户端所需的一切：

```
✓ redis installed: "cache"
  Container:  pgcli-redis-default-cache
  Version:    8
  Image:      docker.io/library/redis:8.10.2
  Data:       ~/pg/addon/redis/cache/data
  Address:    0.0.0.0:6379

  Password:    <generated>

  Client:      pg redis-cli ping
  Raw DSN:     redis://:<password>@0.0.0.0:6379/0
  NOTE: listening on every interface (the Pigsty default); the password above is the only gate.
        loopback-only: pg addon install redis --name cache --listen 127.0.0.1 --force
```

> **安全模型。** 按 Pigsty 惯例，`listen` 默认 **`0.0.0.0`**——Linux（host
> 网络）下端口发布在所有网卡上，`requirepass` 是唯一的闸门。这对"应用跑在另
> 一台机器"很方便，但也意味着密码是分量的：用 `--listen 127.0.0.1` 可把实例
> 收紧为仅回环。`pg addon list` 与日志从不打印密码；需要时从 `pg.yaml` 读。

重复执行 `install` 是幂等的：运行中的容器原样不动，停止的容器会被拉起。要让
改动的端口/监听/密码/镜像生效，加 `--force`（容器按配置重建；数据目录不受
影响）。

## 使用 Redis

### `pg redis-cli`

`pg redis-cli` 用该实例镜像起一个一次性容器跑 Redis 官方客户端——宿主机不
用装任何东西，也不用输密码：

```bash
pg redis-cli ping                              # PONG
pg redis-cli set session:42 '{"user":"alice"}' # 认证自动注入
pg redis-cli get session:42
pg redis-cli lrange queue 0 -1                 # 负数索引原样透传
pg redis-cli --name legacy zrevrange leaderboard 0 9
```

目标是 `--name <实例>`，省略时取按名字排序的第一个 redis 实例（装了多个时，
实际选中的目标会提示在 stderr）。pgcli 自己的 flag 只在命令词之前解析；命令
词之后一切原样转发给 `redis-cli`——包括它的 `-h`/`-p`，所以
`pg redis-cli info -h 10.0.0.7 -p 6380` 可以打到外部端点。`REDISCLI_AUTH`
（redis-cli 原生环境变量）若已设置，覆盖存储的密码：

```bash
REDISCLI_AUTH=otherpass pg redis-cli dbsize
```

### 应用连接

任何 Redis 客户端都可用；安装时打印的 DSN 形如
`redis://:<password>@<host>:<port>/0`。需要 TLS 则要在前面架 stunnel 之类
——见[已知限制](#已知限制)。

## 端口

每个实例从 Redis 自己的池 `redis_start_port`（默认 **6379**）取**一个**端口
——与对象存储共用的 `minio_start_port` 池是独立游标，所以 redis、minio、
rustfs 实例互不冲突：

```bash
pg addon install redis  --name cache      # 6379
pg addon install redis  --name sessions   # 6380
pg addon install minio  --name store      # 9000 / 9001（另一个池）
```

`--port` 可钉死某个端口；自动分配会跳过显式占用的与主机上已监听的。

## 配置

实例位于 `pg.yaml` 顶层 `addons.redis` 映射下：

```yaml
namespace: default
redis_start_port: 6379     # Redis 自己的池，与 minio_start_port 相互独立
addons:
  redis:
    cache:
      container_name: pgcli-redis-default-cache
      name: cache
      version: "8"                       # 记录/展示用的大版本
      image_tag: docker.io/library/redis:8.10.2
      # data_dir: /srv/redis-cache       # 省略则为 <base-dir>/addon/redis/cache/data
      listen: 0.0.0.0                    # Pigsty 默认值；127.0.0.1 收紧
      port: 6379
      password: <generated>              # 首次安装写入
      # maxmemory: 256mb                 # 可选上限 → allkeys-lru 驱逐
      autostart: false                   # pg autostart enable --redis --name cache
```

改 `listen`、`port`、`password`、`maxmemory`、`image_tag`/`version` 或
`data_dir` 后，下一次 `pg addon install redis --name cache --force`（或用对
应 flag）生效。

### 列表

```bash
pg addon list
```

```
Infra add-ons (redis):
  redis (name: cache)
    Status:      running
    Version:     8
    Address:     0.0.0.0:6379
    Auth:        on (requirepass)
    Maxmemory:   256mb (allkeys-lru)
    Data:        ~/pg/addon/redis/cache/data
    Image:       docker.io/library/redis:8.10.2
    Container:   pgcli-redis-default-cache
    Client:      pg redis-cli --name cache ping
```

`pg addon list` 从不打印密码。

## 启停

```bash
pg addon start redis --name cache
pg addon stop  redis --name cache
```

`stop` 发优雅 SIGTERM——配合 `--stop-timeout 30`，Redis 退出前会写完最后一
份 RDB。`start` 只拉起已有容器（状态不当时会按配置自愈重建）。

## 开机自启

容器带 `--restart unless-stopped` 策略（管崩溃，不管重启）。主机重启后拉起
实例：

```bash
pg autostart enable --redis --name cache
```

Redis 自启是**只启动**（先 install），并从数据目录回放 RDB 快照，数据集跨重
启保留。Redis 与 PostgreSQL 栈无关，开机 `pg start --autostart` 把它放在最
后启动。

## 移除

```bash
pg addon remove redis --name cache               # 容器 + 配置项；数据保留
pg addon remove redis --name cache --clean-data  # 同时删除数据目录
```

不带 `--clean-data` 时主机数据目录（含 `dump.rdb`）原地保留——重装同名实例
数据集复活。`--clean-data` 删除它，并在 rootless podman 的从属 uid 让 Redis
写的文件对宿主用户不可删时，经 `podman unshare` 兜底。清空的每实例父目录会
顺手剪掉；自定义 `data_dir` 的父目录绝不动。

## 日志

```bash
pg logs addon redis --name cache        # 最近 50 行
pg logs addon redis --name cache -f     # 跟随
```

## 故障排查

- **裸客户端报 `NOAUTH Authentication required`** ——预期行为：每个实例都有
  `requirepass`。从 `pg.yaml`（`addons.redis.<name>.password`）读，或直接用
  会注入密码的 `pg redis-cli`。
- **`maxmemory` 驱逐超出预期** —— `--maxmemory` 隐含 `allkeys-lru`（到上限
  就驱逐任何 key：是缓存，不是硬存储）。要严格不驱逐的存储就别设
  `--maxmemory`，改为按主机容量规划。
- **安装时报镜像不存在** —— 两个大版本都是按需拉取的纯
  `docker.io/library/redis` 镜像；隔离网络的主机先 `podman load` tar 包
  （目录与导出步骤见仓库内 `docs/images.md`），之后会跳过拉取。
- **手工改过、不在表中的 `version`** —— `install` 报错并提示用 `--image`，
  不会静默改写你的值。
- **端口已被监听** —— 自动分配会扫描主机监听并跳过；要稳定端口号就用
  `--port` 钉死。

## 已知限制

- **仅单机** —— Sentinel（HA 故障转移）与 Redis Cluster（分片）不在范围内，
  复制（作为他机 redis 的副本）同样不做。单实例是单点：耐久数据需要持久化
  语义（AOF）或副本，这些不由本插件管理。
- **无 TLS** —— Redis 7 的部分构建支持原生 TLS，但这里未接入；按可信网络对
  待链路并依赖 `requirepass`，或干脆 `--listen 127.0.0.1` 仅回环。
- **macOS 代码完备、未实测** —— bridge 路径（发布端口、`pg redis-cli` 走
  `host.containers.internal`）与对象存储同构；尚未在 Mac 上跑过。
- **RDB 粒度** —— 崩溃会丢失上次快照之后的写入；缓存/会话数据可接受，耐久
  数据请放 PostgreSQL。
