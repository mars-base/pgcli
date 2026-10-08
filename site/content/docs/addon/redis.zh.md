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
- **可调的持久化。** 默认数据集是落在 bind 挂载主机目录里的 RDB 快照——重启
  后仍在，`pg addon remove`（不带 `--clean-data`）后也仍在，重装同名实例即可
  复活数据。`--aof` 在其上叠加写前日志，用于崩溃安全、不驱逐的存储；
  `--save no` 可彻底关闭快照（见[参数设置](#参数设置)）。

在 PostgreSQL 实例旁边做缓存/会话/排行榜/计数就选 Redis；它与 PG 栈无关，可
与任何插件并存，同一主机也能跑多个实例（甚至跨大版本）。

> **范围之外：** Sentinel（自动故障转移）不由 pgcli 管理。Redis 的*原生集
> 群*是支持的——`--cluster` 把实例变成 cluster-enabled 成员，组建由你自己跑
> 一次 `pg redis-cli -- --cluster create`（默认纯主分片，用 `--cluster-replicas
> N` 可给每个主加从属，见[原生集
> 群](#原生集群)）。读副本**也**支持——`--replica-of` 把一个实例变成对另一个
> 主节点的只读跟随（见[读副本](#读副本)与[已知限制](#已知限制)）。

## 参数设置

pgcli 刻意把 Redis 的参数面做得很小：一个可调项对应一个 flag，最终都落到
`redis-server` 的原生选项上——经镜像自带 entrypoint 用 argv 覆盖，不生成
`redis.conf`；一个实例一个容器，直接用纯上游镜像（pgcli 不为 Redis 构建
wrapper，这点与 [rustfs](../rustfs/) 不同）。下表列出每个可调项及其默认值：

| Flag / `pg.yaml` 键 | 默认值 | Redis 选项 | 含义 |
|---|---|---|---|
| `--version` / `version` | `8` | —（决定镜像 tag） | 安装的大版本（见[版本选择](#版本选择)） |
| `--image` / `image_tag` | 由版本映射表解析 | — | 直接指定任意 tag |
| `--port` / `port` | 从 `redis_start_port` 自动分配（基 **6379**） | `--port` | 宿主端口 |
| `--listen` / `listen` | `0.0.0.0` | `--bind` | 监听地址（`127.0.0.1` 收紧） |
| `--password` / `password` | 自动生成 20 字符 | `--requirepass` | 认证；始终必须存在（副本的必须与主节点相同） |
| `--maxmemory` / `maxmemory` | 不设（无上限） | `--maxmemory` | 数据集上限，开启驱逐 |
| `--maxmemory-policy` / `maxmemory_policy` | `allkeys-lru` | `--maxmemory-policy` | 驱逐策略，仅在设了上限时有意义 |
| `--aof` / `aof` | 关 | `--appendonly yes` | 在快照之上叠加写前日志 |
| `--appendfsync` / `appendfsync` | `everysec`（Redis 自身默认） | `--appendfsync` | AOF 刷盘强度，仅在开 `--aof` 时有意义 |
| `--save` / `save` | Redis 默认计划 | `--save` | RDB 快照计划；`no` 关闭快照 |
| `--replica-of` / `replica_host`+`replica_port` | 不设（主节点） | `--replicaof` + `--masterauth` | 成为*本机*主实例的只读副本（host/port/密码都从配置里取） |
| `--replica-of-host` / `replica_host` | — | `--replicaof` | 跨机副本的**主节点地址**（与 `--replica-of-port` + `--password` 搭配） |
| `--replica-of-port` / `replica_port` | — | `--replicaof` | 主节点端口，与 `--replica-of-host` 搭配 |
| `--cluster` / `cluster` | 不设（独立实例） | `--cluster-enabled yes` + `--masterauth` | 原生集群成员：非空值即组名——同名归同一集群，且同组成员共用**首成员**的密码（`--cluster create` 用一个密码认证全部操作数节点，与"副本密码必须等于主节点"同理）。每个成员还会带上 `--masterauth`，以便 Redis 在组建时被提升为从属的节点能向主节点认证。与 `--replica-of`/`--replica-of-host` 互斥——一个实例要么是集群成员，要么是读副本。pgcli 只装节点，组建由你自己做——见[原生集群](#原生集群) |
| `--cluster-replicas` / `cluster_replicas` | `0`（纯主分片） | —（仅回显进建议的 `--cluster create`） | 组建步骤里每个主的从属数：摘要按 `3*(1+N)` 个**节点**计数，打印的命令以 `--cluster-replicas N` 结尾。哪些成员成为从属是 Redis 在 `--cluster create` 时的决定，而非安装时。它是与密码同类的组属性——首成员设定，后续成员继承，显式不一致会被拒绝 |
| `--advertise-host` / `advertise_host` | 不设（`127.0.0.1`，单机） | `--cluster-announce-ip`/`-port`/`-bus-port` | 本成员向对等节点宣告的地址——填局域网 IP，跨机集群必备（Redis 集群总线不支持 NAT/端口重映射） |
| `--data-dir` / `data_dir` | `<base-dir>/addon/redis/<name>/data` | `--dir /data`（主机目录 bind 挂载到 `/data`） | 数据落盘位置 |
| — | `autostart: false` | — | 开机自启（`pg autostart enable --redis`） |

各旋钮的组合方式：

- **`requirepass` 始终开启**——即使手工往 `pg.yaml` 里留空 `password`，也不
  存在"未认证的 Redis"这种可达状态。
- **持久化默认只有 RDB 快照**——沿用 Redis 自身的 `save` 计划，外加停机时补
  一份最终快照（`--stop-timeout 30` 给足时间）与启动时从 `dump.rdb` 回放。
- **`--aof` 在快照之上叠加写前日志**（`--appendonly yes`）——耐久、不驱逐的
  存储形态。`--appendfsync` 调节刷盘强度：`always`（耐久）/ `everysec`（默
  认）/ `no`（吞吐）。AOF 文件在数据目录的 `appendonlydir/` 下，启动时先于
  快照回放，两者同开时以 AOF 为准。
- **`--save no` 关闭快照**——与 `--aof` 搭配即 AOF-only（安装摘要显示
  `aof only`）；单独使用则是纯内存实例，重启丢数据（install 会警告）。
- **`--maxmemory` 隐含驱逐**，默认 `allkeys-lru`——上限的语义是"缓存，超了
  就驱逐"。传 `--maxmemory-policy noeviction` 变成硬顶（超上限写失败而非驱逐
  键），或干脆不设 `--maxmemory` 表示无上限。policy 依赖 `--maxmemory`、
  `--appendfsync` 依赖 `--aof`——CLI 在拉镜像/建容器之前就拒绝这类组合。
- **`--replica-of` 造只读副本**——见[读副本](#读副本)。持久化/驱逐旋钮与角色
  正交：副本也能设 `--maxmemory` 或开 `--aof`，跟主节点一模一样。
- **`--cluster` 造集群成员，与 `--replica-of`/`--replica-of-host` 互斥**——
  一个实例只有一个角色。同 `--cluster` 组成员自动共用首成员的密码（后续成员
  带**不同**的 `--password` 会在拉镜像/建容器之前被拒绝）；每主从属数由
  `--cluster-replicas N` 同样按组继承。见[原生集群](#原生集群)。

Redis/Valkey 的选项空间远比这张表大，pgcli 有意不包装多节点**控制面**
（Sentinel、自动故障转移），集群**编排**也一样：pgcli 装 cluster-enabled 节
点，但从不替你跑 `--cluster create`/`add-node`/resharding/缩容——见[原生集
群](#原生集群)与[已知限制](#已知限制)。读副本是支持的（[读副本](#读副本)），
pgcli 留下的只是*编排*那一层：主节点挂了不自动提升副本。引擎本身倒是可以用
`--image` 变通：`pg addon install redis --image docker.io/valkey/valkey:8` 跑的
是 [Valkey](https://valkey.io/)（Redis 的分支），大版本反解析为 `8`。

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

# 本机 "sessions" 的只读副本（借用它的大版本、密码与端点）
pg addon install redis --name sessions-ro --replica-of sessions
```

安装摘要打印客户端所需的一切：

```
✓ redis installed: "cache"
  Container:  pgcli-redis-default-cache
  Version:    8
  Image:      docker.io/library/redis:8.10.2
  Data:       ~/pg/addon/redis/cache/data
  Address:    0.0.0.0:6379
  Role:       master
  Persistence: rdb snapshots (default save schedule)

  Password:    <generated>

  Client:      pg redis-cli ping
  Raw DSN:     redis://:<password>@0.0.0.0:6379/0
  NOTE: listening on every interface; the password above is the only gate.
        loopback-only: pg addon install redis --name cache --listen 127.0.0.1 --force
```

> **安全模型。** `listen` 默认 **`0.0.0.0`**——Linux（host 网络）下端口发布在
> 所有网卡上，`requirepass` 是唯一的闸门。这对"应用跑在另一台机器"很方便，但
> 也意味着密码是分量的：用 `--listen 127.0.0.1` 可把实例收紧为仅回环。
> `pg addon list` 与日志从不打印密码；需要时显式揭示——`pg addon password redis
> --name cache` 或 `pg addon list --show-password`（也可直接读 `pg.yaml`）。

重复执行 `install` 是幂等的：运行中的容器原样不动，停止的容器会被拉起。要让
改动的端口/监听/密码/镜像生效，加 `--force`（容器按配置重建；数据目录不受影
响）。持久化类旋钮（`--aof`/`--appendfsync`/`--save`/`--maxmemory-policy`）同
样靠 `--force` 生效。

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

本地目标是 `--name <实例>`，省略时取按名字排序的第一个 redis 实例（装了多个时，
实际选中的目标会提示在 stderr）。pgcli 自己的 flag 只在命令词之前解析；命令
词之后一切原样转发给 `redis-cli`——负数索引、经 `--` 的选项 flag 都靠这个。

**连接远程端点** —— `--host`/`--port` 覆盖客户端连去哪里，无需把远程注册成
插件：

```bash
# 复用本地实例的镜像 + 密码，但连到远程主机（如副本）
pg redis-cli --host 10.0.0.7 ping
pg redis-cli --host 10.0.0.7 --port 6380 --name cache info

# 本机没有任何实例：用默认大版本镜像，密码走 REDISCLI_AUTH
REDISCLI_AUTH=s3cret pg redis-cli --host 10.0.0.7 dbsize
```

要连远程用这两个，而不是 redis-cli 自己的 `-h`/`-p`：后者落在命令词之后，会
被当成命令参数（`ping -h 10.0.0.7` 报错），而连接用的 `-h`/`-p` 由 pgcli 负
责注入。`REDISCLI_AUTH`（redis-cli 原生环境变量）若已设置，任何模式下都覆盖
存储的密码：

```bash
REDISCLI_AUTH=otherpass pg redis-cli dbsize
```

### 应用连接

任何 Redis 客户端都可用；安装时打印的 DSN 形如
`redis://:<password>@<host>:<port>/0`。需要 TLS 则要在前面架 stunnel 之类
——见[已知限制](#已知限制)。

## 读副本

`--replica-of` 把新实例变成已有主实例的**只读副本**：它持续拉取主节点的写入
并提供读，对它的写会被 `READONLY` 拒绝。这是读扩展与数据冗余，**不是**高可
用——pgcli 不跑 Sentinel，主节点挂掉不会自动提升副本（见[已知限制](#已知限
制)）。

```bash
# 主节点（可写）
pg addon install redis --name cache

# 同机副本：借用主节点的大版本、密码与端点
pg addon install redis --name cache-ro --replica-of cache
```

本机的 `--replica-of` 一切从配置解析：副本采用主节点的 version（跨大版本复制
不受支持——不一致会立刻报错）、它的密码，以及端点 `127.0.0.1:<主节点端口>`。
这**一个**密码随后同时服务 `requirepass`（副本自己的客户端用它认证）与
`--masterauth`（副本向主节点认证），所以 `--password`/`--maxmemory`/`--aof`
等与主节点完全一致。存储下来的 `replica_host`/`replica_port` 意味着即使主节点
已从配置中删除，`pg addon start` 也能重建出同样的 `--replicaof` argv。

**跨机主节点**用显式形式——pgcli 查不到远程主节点的密码，得由你给（必须与主
节点相同）：

```bash
pg addon install redis --name cache-r2 \
    --replica-of-host 10.0.0.7 --replica-of-port 6379 --password <master-password>
```

`pg addon list` 与安装摘要都会打印 `Role:` 行——`master`，或
`replica of <host>:<port> (read-only)`；`pg redis-cli --name cache-ro info
replication` 在（携带 RDB 的）首轮同步完成后显示 `role:replica` 且
`master_link_status:up`。读流量指向副本、写流量指向主节点，例如用各自的 DSN：

```bash
# 读流量 -> 副本
redis://:<password>@127.0.0.1:<副本端口>/0
```

要在运行时把一个副本提升为独立主节点（这类手工故障转移正是 pgcli 有意留给你的
部分），先解除复制，再——若希望重启后依然成立——把角色从配置里去掉：

```bash
pg redis-cli --name cache-ro replicaof no one   # 停止跟随主节点
pg addon remove redis --name cache-ro          # 保留数据，忘掉角色
pg addon install redis --name cache-ro         # 作为普通主节点重装
```

## 原生集群

`--cluster` 把实例变成 **Redis 原生集群** 的成员——按 16384 个哈希槽分片，而
非对某个主节点做只读复制。这是 pgcli 唯一支持的多节点拓扑；[Sentinel](#已知
限制) 不支持。

pgcli 在这件事上的角色刻意很窄：**装 cluster-enabled 的节点，但从不替你组
建**。这本来就是 Redis 自己的两步流程，不是 pgcli 发明的花活——集群 = 若干
`cluster-enabled yes` 的服务器 + 一条对它们整体跑一次的 `redis-cli --cluster
create`。把这两步拆开，pgcli 就不必去猜"这是不是我等着的最后一个成员、现在
该不该把集群拉起来"（那种 etcd 式的自动 bootstrap 状态机）；组建永远是**你
的决定**，跑一次，跟你对着裸 `redis-server` 做的事一模一样。

```bash
# 1. 装成员——一个 --cluster <组名> 把每台变成 cluster-enabled 节点；同名意味着
#    "同一个集群"。最少 3 个主节点（Redis 自己的法定人数规则）。纯主分片是默认
#    （装 3 个，--cluster-replicas 0）；要每台主带一个从属，就装 3*(1+N) 个成员，
#    在第一个安装时传 --cluster-replicas N（其余自动继承）——见第 2 步。
pg addon install redis --name n1 --cluster app --maxmemory 10mb
pg addon install redis --name n2 --cluster app --maxmemory 10mb
pg addon install redis --name n3 --cluster app --maxmemory 10mb
```

同组成员**自动共用首成员的密码**：`--cluster create` 用一个 `-a`（这里是
`pg redis-cli` 注入的那一个 `REDISCLI_AUTH`）认证操作数里的每个节点，所以
必须一个密码打通全组——和"[读副本](#读副本)的密码必须等于主节点"是同一条
规则。后续成员带**不同**的 `--password` 会在拉镜像/建容器之前被拒绝；不带
`--password` 则自动继承组内的值。每个成员还会带上 `--masterauth`（与
requirepass 相同的共享密码）：`--cluster create` 时 Redis 可以把任意节点提升
为从属，从属需要 `--masterauth` 才能向**主节点**认证——没有它节点就会陷入永久
的重连风暴（`master_link_status:down`，CPU 满载）。`--cluster` 与
`--replica-of`/`--replica-of-host` 在一次安装里互斥——一个实例要么是集群成员，
要么是读副本，绝不兼得。

```bash
# 2. 一次组建，走 pg redis-cli 的透传（pgcli 从不替你跑这一步）
pg redis-cli --name n1 -- --cluster create 127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381 --cluster-replicas 0
```

三个 `addr:port` 操作数就是每个成员的 `PeerAddr:port`——单机集群里是
`127.0.0.1:<端口>`，跨机集群里是每个成员的 `--advertise-host:<端口>`（见下
文）。安装摘要已经会打印出这条命令，按当下配置好的 `--cluster app` 成员现
场拼装（比如装到 n3 时看到 `pg redis-cli --name n3 -- --cluster create
127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381 --cluster-replicas 0`），复制
粘贴即可用，不需要手工拼：

```
-> NOTE: native-cluster member of "app" — it comes up cluster-enabled but INCOMPLETE
   until you assemble the group once (see the "Cluster:" line below). pgcli does not
   run --cluster create for you.
...
  Cluster:     "app" — 3 masters configured. Assemble once (pgcli does not run this):
               pg redis-cli --name n3 -- --cluster create 127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381 --cluster-replicas 0
               (bus port = client+10000; for cross-host peers open it on the firewall, and give each member --advertise-host)
```

（安装摘要/列表的输出与英文文档格式一致——`pg` CLI 的输出本身是英文，两种
语言下字节相同。）

`--cluster-replicas 0`（默认）就是"纯主分片"——每个成员各管一段槽，且都没有
从属。要让每台主带从属（这样某台主挂掉时它的从属能自动顶上，槽区间不至于随
之失联），在第一个安装时传 `--cluster-replicas N` 并装够成员数：每台主需要它
自己加它的 `N` 个从属，所以总数是 `3*(1+N)`——N=1 时是 6 个成员：

```bash
pg addon install redis --name n1 --cluster app --cluster-replicas 1 --maxmemory 10mb
pg addon install redis --name n2 --cluster app --maxmemory 10mb   # 继承 replicas=1
# …装到 6 个成员，然后：
pg redis-cli --name n1 -- --cluster create \
    127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381 \
    127.0.0.1:6382 127.0.0.1:6383 127.0.0.1:6384 --cluster-replicas 1
```

哪些成员被 Redis 选为主、哪些选为从，是它在 `--cluster create` 时的决定（能
跨主机均衡时它会尽量均衡），所以 pgcli 的实例命名刻意是角色中立的；从属重启
后会从自己的 `nodes.conf` 重新接上主——角色与 masterauth 配对都能跨重启保留，
无需重新 create。上面的安装摘要在引入从属后会从"3 masters configured"切换为
按**节点**计数（"6 nodes configured"），并把组内的 `--cluster-replicas N` 回显
进建议命令；它绝不会把从属所在组的节点叫成 master。

### 组建之后

连到 cluster 成员的 `pg redis-cli` 自动是**集群感知客户端**：pgcli 会注入
redis-cli 自己的 `-c`，`get`/`set` 落到"错的那个节点"时会跟着 `MOVED` 重定
向跑到真正持槽的那台——不用你记 `-c`，随便指哪个 `--name` 都一样：

```bash
pg redis-cli --name n1 set foo bar      # 可能实际由 n3 应答（MOVED 跟随）
pg redis-cli --name n2 get foo          # 同一个 key，再跟一次重定向
pg redis-cli --name n1 -- --cluster check 127.0.0.1:6379   # 管理型子命令，不归 -c 管
```

最后一行是例外，不是规则：命令是 redis-cli 自己的 `--cluster <子命令>` 时
（这类子命令会按操作数自己拨到目标节点），pgcli 会**抑制** `-c` 注入——它是
组建/管理面，不是数据命令。

集群总线是**每成员的第二个端口**：永远是 `client + 10000`（Redis 的固定
规则，比如 `6379`→`16379`）——不从 `redis_start_port` 池分配、不写入
`pg.yaml`；`pg addon list` 会在组名旁把它一并显示出来：

```
    Role:        cluster member of "app" (127.0.0.1:6381)
    Cluster:     app (bus 16381)
```

拓扑会自愈：每个节点的 `--cluster-config-file nodes.conf` 落在自己
`--dir /data` 的 bind 挂载里，所以 `pg addon stop`/`start`（或主机重启）后
整个集群原样回来，不需要再 create——与本仓库 autostart 的"只启动"模型一致。

### 跨机成员：`--advertise-host`

单机集群默认用 `127.0.0.1` 拨对端，不需要额外 flag。Redis 集群总线**不支
持** NAT 或端口重映射，所以跨机成员必须显式宣告"对端实际能到达"的地址：

```bash
# 在 10.10.0.158 上——首成员决定全组的那一个密码，装完读出来：
pg addon install redis --name n1 --cluster app --advertise-host 10.10.0.158
PW=$(pg addon password redis --name n1)
# 在 10.10.0.159、10.10.0.160 上——把同一个密码带过去：
pg addon install redis --name n2 --cluster app --advertise-host 10.10.0.159 --password "$PW"
pg addon install redis --name n3 --cluster app --advertise-host 10.10.0.160 --password "$PW"
# 一次组建（从能连通三台的任意一台执行）——操作数是各成员的宣告地址+各自端口
# （这里每台一个成员，各主机自己的端口池都从 6379 起，所以三台都是 :6379）：
pg redis-cli --name n1 -- --cluster create \
    10.10.0.158:6379 10.10.0.159:6379 10.10.0.160:6379 --cluster-replicas 0
```

`--advertise-host` 会往节点 argv 里加 `--cluster-announce-ip/-port/-bus-port`，
并且把 `PeerAddr`（就是上面拼 `--cluster create` 操作数用的地址）从
`127.0.0.1` 换成这个地址。你自己还得做对两件事：主机间防火墙**放行每个成员
的总线端口（client+10000——默认 `6379` 时即 `16379`）**——不只是客户端端口
——并且组内**每个**成员都要带这个 flag，不能只给新加入的补。

后续每台主机上的 `--password "$PW"` 不是摆设：共享密码的继承只在**同一份
`pg.yaml` 内**生效——单机上，后续成员自动采用组内密码；跨机上每台主机有自己
独立的配置文件，各自生成各自的秘密。因此跨机集群必须**在每台主机上显式钉同
一个密码**，密码从首成员用 `pg addon password` 读出来。

（`--cluster create` 用这一个密码认证操作数里的每一个节点，不一致会以逐节点
认证错误的形式暴露出来。）

> **状态：** 跨机集群已**在三台主机上端到端验证**（`--advertise-host` →
> `--cluster-announce-*`、跨机 `--cluster create`、跨机 MOVED 重定向，以及各
> 自主机 stop/start 后的自愈）。单机流程另有自动化 e2e
> （`test/addon/test_redis_cluster.sh`），在其中覆盖两种形态：纯三主组，以及
> 3 主 × 1 从组（缺失 `--masterauth` 的重连风暴正是这样被抓出来的）。**多机场
> 景目前还是手工流程**，尚未进测试套件。

## 端口

每个实例从 Redis 自己的池 `redis_start_port`（默认 **6379**）取**一个**端口
——与对象存储共用的 `minio_start_port` 池是独立游标，所以 redis、minio、
rustfs 实例互不冲突：

```bash
pg addon install redis  --name cache      # 6379
pg addon install redis  --name sessions   # 6380
```

`--port` 可钉死某个端口；自动分配会跳过显式占用的与主机上已监听的。[原生集
群](#原生集群)成员也一样——集群总线端口永远是 Redis 自己的 `client+10000`
（由客户端端口推导出来），不占这个池。

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
      listen: 0.0.0.0                    # 默认（全网卡）；127.0.0.1 收紧
      port: 6379
      password: <generated>              # 首次安装写入
      # maxmemory: 256mb                 # 可选上限
      # maxmemory_policy: noeviction     # 仅在设了上限时有效；默认 allkeys-lru
      # aof: true                        # 在快照之上叠加写前日志
      # appendfsync: always              # 仅在开 aof 时有效；默认 everysec
      # save: "900 1 300 10"             # RDB 计划；"no" 关闭快照
      # replica_host: 127.0.0.1          # 由 --replica-of 写入：本实例是读副本
      # replica_port: 6379               # 主节点端口，与 replica_host 搭配
      # cluster: app                     # 由 --cluster 写入：本实例是原生集群成员
      # cluster_replicas: 1              # 由 --cluster-replicas 写入：每个主的从属数，供 --cluster create 用（0=纯主分片；全组共用）
      # advertise_host: 10.10.0.158      # 由 --advertise-host 写入：对等节点可见地址（仅跨机）
      autostart: false                   # pg autostart enable --redis --name cache
```

改 `listen`、`port`、`password`、`maxmemory`/`maxmemory_policy`、
`aof`/`appendfsync`/`save`、`replica_host`/`replica_port`、
`cluster`/`cluster_replicas`/`advertise_host`、`image_tag`/`version` 或 `data_dir` 后，
下一次 `pg addon install redis --name cache --force`（或用对应 flag）生效。副本角色
与集群角色通常分别由 `--replica-of*` / `--cluster`（含 `--cluster-replicas`）/
`--advertise-host` 这组 flag 设定，而非手改——见[读副本](#读副本)与[原生集群](#原生集群)。

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
    Role:        master
    Auth:        on (requirepass)
    Maxmemory:   256mb (allkeys-lru)
    Persistence: rdb + aof (default save schedule, appendonly yes)
    Data:        ~/pg/addon/redis/cache/data
    Image:       docker.io/library/redis:8.10.2
    Container:   pgcli-redis-default-cache
    Client:      pg redis-cli --name cache ping
```

`pg addon list` 从不打印密码。要有意揭示它，加 `--show-password`（会给每个 Redis
实例追加 `Password:` 与一条可直接粘贴的 `Raw DSN:` 行——对象存储则追加
`Root password:` 行），或只把某一个实例的值原样打到 stdout 供脚本使用：

```bash
pg addon password redis --name cache
export REDISCLI_AUTH="$(pg addon password redis --name cache)"
```

`pg addon password` 与其它子命令一样接受 `--name`（缺省即插件自身名字），并支持
`--file`——把值以 0600 权限写入文件而非终端，避免落入 shell 历史与回滚缓冲。

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
  `requirepass`。用 `pg addon password redis --name cache` 取，或直接用会注入密
  码的 `pg redis-cli`。
- **`maxmemory` 驱逐超出预期** —— 设了 `--maxmemory` 上限后默认策略是
  `allkeys-lru`（到上限就驱逐任何 key：是缓存，不是硬存储）。要把上限变成
  硬天花板就传 `--maxmemory-policy noeviction`（超限后写入报错而非驱逐），或
  干脆不设 `--maxmemory`（无上限）。
- **AOF 未回放最新写入** —— `--appendfsync everysec`（默认）在硬崩溃时可能
  丢最后一秒的写入；承受不起就用 `--appendfsync always`。优雅 `pg stop` 总会
  刷盘，所以只有非正常 kill 才会踩到。
- **安装时报镜像不存在** —— 两个大版本都是按需拉取的纯
  `docker.io/library/redis` 镜像；隔离网络的主机先 `podman load` tar 包
  （目录与导出步骤见仓库内 `docs/images.md`），之后会跳过拉取。
- **手工改过、不在表中的 `version`** —— `install` 报错并提示用 `--image`，
  不会静默改写你的值。
- **端口已被监听** —— 自动分配会扫描主机监听并跳过；要稳定端口号就用
  `--port` 钉死。
- **副本拒绝写入（`READONLY`）** —— 这是角色本身，不是故障：写请发往主节点。
  要让它成为独立主节点，见[读副本](#读副本)。
- **刚建好的副本短暂显示 `master_link_status:down`** —— 首轮（携带 RDB 的）
  全量同步期间正常；redis 8 经 rdbchannel 传快照时还会再重连一次链路。看
  `info replication`，最终会稳定为 `up` 且 `role:replica`。

## 已知限制

- **无自动故障转移** —— 读副本是支持的（见[读副本](#读副本)），原生集群分片
  也支持（见[原生集群](#原生集群)），但 HA *控制面*不支持：**Sentinel** 不在
  范围内，集群**编排**也一样——pgcli 装 cluster 成员，但从不替你组建、扩容、
  再平衡。副本给你的是读扩展与一份冗余数据，不是一对能自愈的节点——主节点挂
  掉时由你自己提升副本（`replicaof no one`，见上）。没有副本的单实例是单点：
  可以用 `--aof` 让它在磁盘上*耐久*，但无法故障转移到其它节点。
- **原生集群的组建与再整形仍为手工** —— `--cluster` 装 cluster-enabled 节点，
  `--cluster-replicas N`（回显进建议命令）可让每台主带从属，某台主挂掉时它的从
  属能自动顶上。pgcli 不替你做的事是：跑 `--cluster create`/`add-node`/
  resharding/`del-node`；`pg addon remove` 删成员就是纯删除，不会搬槽——要缩
  容或改拓扑请先在 Redis 层把它的槽挪空、把节点忘记，再删。另注意：纯主分片
  （`--cluster-replicas 0`）没有可提升的从属——一台主挂掉，它负责的那段槽就随
  之不可用，直到你介入。
- **跨机集群（`--advertise-host`）为手工流程、未进 e2e 套件** —— flag、它产生
  的 `--cluster-announce-*` argv、防火墙/总线端口规则都已实现、单元测过，并
  且已**在三台主机上手工验证**（组建、跨机 MOVED 重定向、自愈均通过）。单机
  流程（纯主分片与 3 主 × 1 从）有自动 e2e；跨机带从属尚无自动 e2e，但它依赖
  的正是让单机从属连上的那同一份逐成员 `--masterauth`。见[原生集群](#原生集
  群)里的状态说明。
- **无 TLS** —— Redis 7 的部分构建支持原生 TLS，但这里未接入；按可信网络对
  待链路并依赖 `requirepass`，或干脆 `--listen 127.0.0.1` 仅回环。跨机集群意
  味着成员间的总线流量同样是明文。
- **macOS 代码完备、未实测** —— bridge 路径（发布端口、`pg redis-cli` 走
  `host.containers.internal`）与对象存储同构；尚未在 Mac 上跑过。
- **默认持久化是 RDB 粒度** —— 崩溃会丢失上次快照之后的写入。这个缺口要紧时
  启用 `--aof`（`everysec` 下最多仍丢一秒；`always` 可封死）；缓存/会话数据
  接受仅快照即可，耐久数据请放 PostgreSQL。
