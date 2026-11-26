---
title: "Valkey"
description: "把 Valkey 作为 pgcli addon 运行——Linux Foundation 托管的 Redis BSD-3 分支，自有大版本线 7/8/9，支持 RDB/AOF 持久化、读副本与原生集群分片"
weight: 50
---

[Valkey](https://valkey.io/) 是 Linux Foundation 托管的 **Redis 7.2.4 的 BSD-3
分支**——同样的线上协议、同样的 `--requirepass`、同样的 RDB/AOF 文件、同样的
复制与原生集群协议。pgcli 把它作为**独立的顶层 add-on 类型**提供，而不是“换个镜像
的 redis”：`pg addon install valkey` 拥有独立的配置命名空间（`addons.valkey`）、
独立的镜像表（`docker.io/valkey/valkey`）、独立的容器前缀（`pgcli-valkey…`）、
独立的数据目录（`addon/valkey/<name>/data`）、独立的端口池（`valkey_start_port`，
起点 **6380**）以及独立的命令透传（`pg valkey-cli`）。

与 [redis](../redis/) add-on 的功能对齐是刻意的：单机主节点、读副本
（`--replica-of`）、原生集群成员（`--cluster`）、RDB/AOF 持久化、自动生成的
`requirepass`、list/logs/password、autostart，以及同样的 Ansible 单机 + 集群
布局。差别只在标签和二进制名（`valkey-server` / `valkey-cli`）。

> **Valkey 的大版本是独立版本线。** Valkey 从 Redis 7.2.4 分叉，但独立发版：
> 大版本 **7、8、9**（默认 **9**）与 Redis 大版本**没有对应关系**。这里从不查
> redis add-on 的版本表——redis 与 valkey 是两个独立命名空间，甚至可以使用同名，
> 端口池也做了错开播种，不会自动撞号。

> **不在范围内：** Sentinel（自动故障转移）不由 pgcli 管理，与 redis 一致。
> 支持**原生集群**——`--cluster` 把实例变成 cluster-enabled 成员，由你自己用
> `pg valkey-cli -- --cluster create` 组建一次；也支持读副本（`--replica-of`）。

## 参数

每个旋钮一个 flag，逐一映射到原生 `valkey-server` 选项——不生成配置文件，每个实例
一个容器跑纯上游镜像：

| Flag / `pg.yaml` 键 | 默认值 | Valkey 选项 | 含义 |
|----------------------|---------|---------------|---------|
| `--version` / `version` | `9` | —（决定镜像 tag） | 要安装的大版本（见[版本选择](#版本选择)） |
| `--image` / `image_tag` | 由版本表解析 | — | 原样覆盖 tag |
| `--port` / `port` | 从 `valkey_start_port` 自动分配（起点 **6380**） | `--port` | 宿主端口 |
| `--listen` / `listen` | `0.0.0.0` | `--bind` | 绑定地址（收紧可设 `127.0.0.1`） |
| `--password` / `password` | 自动生成，20 字符 | `--requirepass` | 认证；始终必需（副本必须与主节点一致） |
| `--maxmemory` / `maxmemory` | 未设置（无上限） | `--maxmemory` | 数据集上限，开启淘汰 |
| `--maxmemory-policy` / `maxmemory_policy` | `allkeys-lru` | `--maxmemory-policy` | 淘汰策略，仅在有上限时生效 |
| `--aof` / `aof` | 关闭 | `--appendonly yes` | 在快照之上叠加预写日志 |
| `--appendfsync` / `appendfsync` | `everysec`（Valkey 自身默认） | `--appendfsync` | AOF 刷盘强度，仅配合 `--aof` |
| `--save` / `save` | Valkey 默认计划 | `--save` | RDB 快照计划；`no` 关闭快照 |
| `--io-threads` / `io_threads` | 未设置（服务端默认 `1`） | `--io-threads` | 网络 I/O 线程数（1-128）；命令执行仍是单线程——**不要超过 4**，再多收益很小 |
| `--replica-of` / `replica_host`+`replica_port` | 未设置（主节点） | `--replicaof` + `--masterauth` | 本地 valkey 主 add-on 的只读副本（host/port/password 从配置解析） |
| `--replica-of-host` / `replica_host` | — | `--replicaof` | 跨主机副本的主节点**地址**（搭配 `--replica-of-port` + `--password`） |
| `--replica-of-port` / `replica_port` | — | `--replicaof` | 主节点端口，与 `--replica-of-host` 配对 |
| `--cluster` / `cluster` | 未设置（单机） | `--cluster-enabled yes` + `--masterauth` | 原生集群成员：非空即组名——相同值即同一集群，同组共享第一个成员的口令。与 `--replica-of`/`--replica-of-host` 互斥。集群由你自己组建——见[原生集群](#原生集群) |
| `--cluster-replicas` / `cluster_replicas` | `0`（仅主节点） | —（仅回显进建议的 `--cluster create`） | 组装时每个主节点的跟随者数量；与口令一样是从第一个成员继承的组属性 |
| `--advertise-host` / `advertise_host` | 未设置（`127.0.0.1`，单机） | `--cluster-announce-ip`/`-port`/`-bus-port` | 本成员向对端宣告的地址——跨主机集群必需（集群总线不支持 NAT/端口重映射） |
| `--data-dir` / `data_dir` | `<base-dir>/addon/valkey/<name>/data` | `--dir /data`（宿主目录 bind 到 `/data`） | 数据位置 |
| — | `autostart: false` | — | 开机自启（`pg autostart enable --valkey`） |

旋钮组合规则与 redis 完全相同，由同一套共享校验器执行：

- **`requirepass` 始终开启**——未认证的 Valkey 不是可达状态。
- **持久化默认 RDB 快照**——Valkey 自身的 `save` 计划，停机时再写一次最终快照
  （stop 超时给它落盘时间），启动时回放 `dump.rdb`。
- **`--aof` 叠加预写日志**（`--appendonly yes`）；`--appendfsync` 调节刷盘
  （`always` / `everysec` / `no`）。两者同时开启时先回放 AOF 再回放快照。
- **`--save no` 关闭快照**——与 `--aof` 搭配得到纯 AOF 存储；单独使用则是纯内存
  实例，重启即丢数据集（安装时会警告）。
- **`--maxmemory` 隐含淘汰**，默认 `allkeys-lru`；要硬上限就传
  `--maxmemory-policy noeviction`。`--appendfsync` 依赖 `--aof`；非法组合在拉取
  之前就被拒绝。
- **`--io-threads N` 只并行网络 I/O**——Valkey 的命令执行仍在主线程，多出的线程
  只负责套接字读写。收益很快变平：2-4 是有效区间，**文档建议不要超过 4**。服务端
  接受 1-128；`0`（默认）表示保留服务端自己的 1。
- **`--replica-of` / `--cluster` 互斥**——实例只能是其中一种角色。同组成员共享
  第一个成员的口令与 `--cluster-replicas`；显式不一致会在开始前被拒绝。

## 版本选择

`--version` 选择大版本；Valkey 表把每个大版本映射到 `docker.io/valkey/valkey`
的固定补丁 tag：

| 大版本 | 默认镜像 tag | 选择方式 |
|-------|-------------------|-------------|
| 7 | `docker.io/valkey/valkey:7.2.16` | `pg addon install valkey --name vcache --version 7` |
| 8 | `docker.io/valkey/valkey:8.1.12` | `pg addon install valkey --name vcache --version 8` |
| 9 | `docker.io/valkey/valkey:9.2.0` | `pg addon install valkey --name vcache`（9 是默认） |

解析规则：

- `--version 7|8|9` → 表中的 tag 写入 `image_tag`，大版本写入 `version`；
- `--image <tag>` → 你的 tag 完全优先；若是 valkey tag 会反解大版本用于展示，
  否则留空（原样展示 tag）；
- 都不给 → 大版本 `9`；
- 未知 `--version` → 报错并列出可用的 valkey 大版本。

所有大版本可以在同一主机并存——不同 tag、同一 `valkey_start_port` 池里的不同端口。
**不要**把 Redis 大版本映射到 Valkey：`redis` 的 8 与 `valkey` 的 8 是互不相关的
版本线。

## 安装

```bash
# 默认：大版本 9，实例名 "valkey"，端口从 6380 自动分配
pg addon install valkey

# 命名为 vcache 的缓存实例，大版本 7，带内存上限
pg addon install valkey --name vcache --version 7 --maxmemory 256mb

# 只监听 loopback，而非默认的全接口
pg addon install valkey --name vcache --listen 127.0.0.1

# 固定口令（重装不变）与固定端口
pg addon install valkey --name sessions --password 'S3ssions!' --port 6380

# 本地 "sessions" 实例的只读副本
pg addon install valkey --name sessions-ro --replica-of sessions
```

安装摘要打印客户端需要的一切：

```
✓ valkey installed: "vcache"
  Container:  pgcli-valkey-default-vcache
  Version:    9
  Image:      docker.io/valkey/valkey:9.2.0
  Data:       ~/pg/addon/valkey/vcache/data
  Address:    0.0.0.0:6380
  Role:       master
  Persistence: rdb snapshots (default save schedule)

  Password:    <generated>

  Client:      pg valkey-cli ping
  Raw DSN:     redis://:<password>@0.0.0.0:6380/0
  NOTE: listening on every interface; the password above is the only gate.
        loopback-only: pg addon install valkey --name vcache --listen 127.0.0.1 --force
```

DSN 保持 `redis://` scheme，因为所有 Redis 协议客户端都识别它——Valkey 说的是
同一套线上协议。

> **安全模型。** `listen` 默认 **`0.0.0.0`**；在 Linux（host 网络）上端口发布到
> 所有接口，`requirepass` 是唯一的门槛。需要仅本机可访问时传
> `--listen 127.0.0.1`。`pg addon list` 与日志从不打印口令；用
> `pg addon password valkey --name vcache` 或 `pg addon list --show-password`
> 显式取回。

重复执行 `install` 幂等：运行中的容器保持不动，已停止的会被启动。要应用变更后的
端口/监听/口令/镜像/持久化，加 `--force`（容器按配置重建；数据目录不动）。

## 使用 Valkey

### `pg valkey-cli`

`pg valkey-cli` 用 add-on 镜像的临时容器运行 Valkey 自己的客户端——无需安装、
无需手输口令：

```bash
pg valkey-cli ping                               # PONG
pg valkey-cli set session:42 '{"user":"alice"}'  # ok——认证已注入
pg valkey-cli get session:42
pg valkey-cli --name vcache lrange queue 0 -1
```

本地目标是 `--name <addon>`；省略时取按名称排序的第一个 valkey add-on（装了多个
时会在 stderr 通告选中项）。pgcli 自己的 flag 只在命令词之前解析；其后全部原样
转发给 `valkey-cli`。

**连接远端**——`--host`/`--port` 覆盖客户端连接目标，无需注册 add-on：

```bash
pg valkey-cli --host 10.0.0.7 ping
pg valkey-cli --host 10.0.0.7 --port 6381 --name vcache info

# 本地完全没有 add-on：默认大版本镜像，口令走 REDISCLI_AUTH
REDISCLI_AUTH=s3cret pg valkey-cli --host 10.0.0.7 dbsize
```

请用这些而不是客户端自己的 `-h`/`-p`：后者位于命令词*之后*，会被当作命令参数，
而 pgcli 自己注入连接 flag。`REDISCLI_AUTH`——valkey-cli 从 redis-cli 继承的
环境变量——若设置，在所有模式下覆盖存储的口令：

```bash
REDISCLI_AUTH=otherpass pg valkey-cli dbsize
```

### 应用连接

任何 Redis 协议客户端都可用；安装时打印的 DSN 形如
`redis://:<password>@<host>:<port>/0`。

## 读副本

`--replica-of` 把新实例变成既有 valkey 主 add-on 的**只读副本**：持续接收主节点
写入并提供读，写入会被拒绝（`READONLY`）。这是读扩展与数据冗余，**不是**高可用
——没有 Sentinel，主节点挂掉不会自动提升。

```bash
pg addon install valkey --name vcache
pg addon install valkey --name vcache-ro --replica-of vcache
```

本地 `--replica-of` 从 `addons.valkey` 解析一切：副本采用主节点的大版本（不支持
跨大版本复制——不匹配会立即报错）、口令，以及 `127.0.0.1:<master-port>` 端点。
副本只能跟随另一个 **valkey** add-on——redis 与 valkey 命名空间独立，
`--replica-of` 不会跨类型。跨主机主节点需显式给出地址与主节点口令：

```bash
pg addon install valkey --name vcache-r2 \
    --replica-of-host 10.0.0.7 --replica-of-port 6380 --password <master-password>
```

`pg addon list` 与安装摘要会显示 `Role:` 行（`master` 或
`replica of <host>:<port> (read-only)`），首次 RDB 同步完成后
`pg valkey-cli --name vcache-ro info replication` 报告 `role:replica` 与
`master_link_status:up`。

手工提升副本（pgcli 刻意留给你的手动故障转移）：

```bash
pg valkey-cli --name vcache-ro replicaof no one   # 停止跟随主节点
pg addon remove valkey --name vcache-ro          # 保留数据，忘掉角色
pg addon install valkey --name vcache-ro         # 作为普通主节点重装
```

## 原生集群

`--cluster` 把实例变成 **Valkey 原生集群**成员——按 slot（16384 个）分片，而
非某个主节点的读副本。流程与 redis 完全一致：**pgcli 只安装 cluster-enabled
节点，不负责组装。**

```bash
# 1. 安装成员——最少 3 个主节点；相同 token 即同一集群
pg addon install valkey --name n1 --cluster app --maxmemory 10mb
pg addon install valkey --name n2 --cluster app --maxmemory 10mb
pg addon install valkey --name n3 --cluster app --maxmemory 10mb

# 2. 组装一次，通过 pg valkey-cli 的透传（pgcli 从不执行）
pg valkey-cli --name n1 -- --cluster create 127.0.0.1:6380 127.0.0.1:6381 127.0.0.1:6382 --cluster-replicas 0
```

同组成员自动共享第一个成员的口令（后装成员给了不同 `--password` 会在开始前被
拒绝），每个节点的 argv 都带 `--masterauth`，这样 Valkey 在组装时把任何节点提升
为跟随者后都能向主节点认证。`--cluster` 不能与 `--replica-of`/`--replica-of-host`
同时使用。

安装摘要会按组内当前成员打印精确的 `--cluster create` 命令，可直接复制：

```
-> NOTE: native-cluster member of "app" — it comes up cluster-enabled but INCOMPLETE
   until you assemble the group once (see the "Cluster:" line below). pgcli does not
   run --cluster create for you.
...
  Cluster:     "app" — 3 masters configured. Assemble once (pgcli does not run this):
               pg valkey-cli --name n3 -- --cluster create 127.0.0.1:6380 127.0.0.1:6381 127.0.0.1:6382 --cluster-replicas 0
               (bus port = client+10000; for cross-host peers open it on the firewall, and give each member --advertise-host)
```

要带跟随者，就安装 `3*(1+N)` 个成员，并在第一个上给 `--cluster-replicas N`
（其余继承）；建议命令以 `--cluster-replicas N` 结尾，哪些成员成为跟随者由
Valkey 在 `--cluster create` 时决定。

### 组装之后

指向本地集群成员的 `pg valkey-cli` 自动是**集群感知客户端**——pgcli 注入
valkey-cli 自己的 `-c` flag，`get`/`set` 落到“错误”节点时会跟随 `MOVED` 重定向
到真正持有 slot 的节点：

```bash
pg valkey-cli --name n1 set foo bar      # 可能经 MOVED 由 n3 服务
pg valkey-cli --name n2 get foo          # 同一 key，再次跟随重定向
pg valkey-cli --name n1 -- --cluster check 127.0.0.1:6380   # 管理子命令，不走 -c
```

最后一行是例外：命令是 valkey-cli 自己的 `--cluster <subcommand>` 时，pgcli 抑制
`-c` 注入。

用 `--host`/`--port` **远程**访问集群且本地没有该 add-on 时，pgcli 无从得知端点
是集群节点，不会注入任何东西；自行在 `--` 透传之后加 `-c`：

```bash
export REDISCLI_AUTH=<the group password>
pg valkey-cli --host 10.10.0.158 --port 6380 -- -c get foo
pg valkey-cli --host 10.10.0.158 --port 6380 -- -a <the group password> -c set foo bar
```

如果你的客户端完全无法集群感知，在前面放一个 [Predixy 代理](../predixy/)：它说
同一协议，自行跟随重定向并暴露一个普通 `redis://` 端点。

集群总线是**每个成员的第二个端口**，恒为 `client port + 10000`（如 `6380`→
`16380`）——派生而来，不存储，也不从端口池取。拓扑自愈：每个节点的 `nodes.conf`
位于自己的 bind 挂载里，`pg addon stop`/`start` 即可恢复集群，无需重新组建。

```bash
# 通过透传做健康检查
pg valkey-cli --host <ip> --port <port> -- -c cluster info
pg valkey-cli -- --cluster check <ip>:<port>
```

### 跨主机成员：`--advertise-host`

集群总线**不支持** NAT 或端口重映射，跨主机成员必须宣告对端可达的地址：

```bash
# 在 10.10.0.158——第一个成员决定整组唯一的口令；读回来：
pg addon install valkey --name n1 --cluster app --advertise-host 10.10.0.158
PW=$(pg addon password valkey --name n1)
# 在 10.10.0.159 与 10.10.0.160——把同一口令带过去：
pg addon install valkey --name n2 --cluster app --advertise-host 10.10.0.159 --password "$PW"
pg addon install valkey --name n3 --cluster app --advertise-host 10.10.0.160 --password "$PW"
# 从任意能连通三台的主机组装一次：
pg valkey-cli --name n1 -- --cluster create \
    10.10.0.158:6380 10.10.0.159:6380 10.10.0.160:6380 --cluster-replicas 0
```

在主机间防火墙放行每个成员的**总线端口（client+10000——默认 `6380` 时为
`16380`）**，并给组内每个成员都加该 flag。口令继承只在同一个 `pg.yaml` 内有效；
跨主机必须在每台主机上显式固定同一口令，如上所示。

> **状态：** 单主机集群流程有自动化 e2e（`test/addon/test_valkey_cluster.sh`）；
> 跨主机流程复用 redis 实现，与 redis 一样为手工验证。

## 端口

每个实例从 Valkey 自己的端口池 `valkey_start_port`（默认 **6380**）取**一个端口**
——与 redis 池（`redis_start_port`，6379）及所有其它 add-on 池都独立。分配时还会
把 `addons.redis` 已占用的端口计入，因此 valkey 不会自动分到同一配置中 redis 已
持有的端口：

```bash
pg addon install valkey --name vcache    # 6380
pg addon install valkey --name sessions  # 6381
```

`--port` 把实例固定在指定端口；自动分配会跳过任何已显式指定或宿主上已在监听的
端口。集群成员并无不同——总线端口恒为 `client+10000`。

## 配置

实例位于 `pg.yaml` 顶层的 `addons.valkey` map：

```yaml
namespace: default
valkey_start_port: 6380    # Valkey 自己的端口池，独立于 redis_start_port
addons:
  valkey:
    vcache:
      container_name: pgcli-valkey-default-vcache
      name: vcache
      version: "9"                       # 选择/存储的大版本
      image_tag: docker.io/valkey/valkey:9.2.0
      # data_dir: /srv/valkey-vcache      # 省略则为 <base-dir>/addon/valkey/vcache/data
      listen: 0.0.0.0                    # 默认（全接口）；收紧可设 127.0.0.1
      port: 6380
      password: <generated>              # 首次安装写入
      # maxmemory: 256mb                 # 可选上限
      # maxmemory_policy: noeviction     # 仅配合上限；默认 allkeys-lru
      # aof: true                        # 在快照之上叠加预写日志
      # appendfsync: always              # 仅配合 aof；默认 everysec
      # save: "900 1 300 10"             # RDB 计划；"no" 关闭快照
      # io_threads: 4                    # 网络 I/O 线程数（1-128；建议 ≤ 4）
      # replica_host: 127.0.0.1          # 由 --replica-of 写入：这是读副本
      # replica_port: 6380               # 主节点端口
      # cluster: app                     # 由 --cluster 写入：这是原生集群成员
      # cluster_replicas: 1              # --cluster create 时每个主节点的跟随者
      # advertise_host: 10.10.0.158      # 对端可见地址（仅跨主机）
      autostart: false                   # pg autostart enable --valkey --name vcache
```

修改 `listen`、`port`、`password`、
`maxmemory`/`maxmemory_policy`、`aof`/`appendfsync`/`save`、`io_threads`、
`replica_host`/`replica_port`、`cluster`/`cluster_replicas`/`advertise_host`、
`image_tag`/`version` 或 `data_dir` 后，需执行
`pg addon install valkey --name vcache --force`（或使用对应 flag）生效。

### 列表

```bash
pg addon list
```

```
Infra add-ons (valkey):
  valkey (name: vcache)
    Status:      running
    Version:     9
    Address:     0.0.0.0:6380
    Role:        master
    Auth:        on (requirepass)
    Maxmemory:   256mb (allkeys-lru)
    Persistence: rdb + aof (default save schedule, appendonly yes)
    Data:        ~/pg/addon/valkey/vcache/data
    Image:       docker.io/valkey/valkey:9.2.0
    Container:   pgcli-valkey-default-vcache
    Client:      pg valkey-cli --name vcache ping
```

`pg addon list` 从不打印口令。加 `--show-password` 显式显示，或把单个实例的口令
裸打到 stdout 供脚本使用：

```bash
pg addon password valkey --name vcache
export REDISCLI_AUTH="$(pg addon password valkey --name vcache)"
```

`pg addon password` 使用同样的 `--name`（默认 "valkey"）以及 `--file`——把值以
0600 权限写入文件而不是终端。

## 启停

```bash
pg addon start valkey --name vcache
pg addon stop  valkey --name vcache
```

`stop` 发送优雅 SIGTERM——Valkey 在 stop 超时内写出最终 RDB 后退出。`start`
只启动既有容器（状态异常时按配置自愈重建）。

## 开机自启

```bash
pg autostart enable --valkey --name vcache
```

Valkey autostart 是**只启动**（先安装实例），并回放数据目录中的 RDB/AOF。开机时
`pg start --autostart` 在 redis 之后启动 valkey，早于代理它们的 Predixy 等服务。

## 卸载

```bash
pg addon remove valkey --name vcache            # 容器 + 配置条目；数据保留
pg addon remove valkey --name vcache --clean-data  # 同时删除数据目录
```

不带 `--clean-data` 时宿主数据目录（及其 `dump.rdb`）保留——重装同名实例即可复活
数据。`--clean-data` 会删除它，当 rootless podman 的从属 uid 让宿主用户无法删除
文件时回退到 `podman unshare`。

## 日志

```bash
pg logs addon valkey --name vcache        # 最后 50 行
pg logs addon valkey --name vcache -f     # 跟随
```

## 故障排查

- **裸客户端报 `NOAUTH Authentication required`**——预期行为：每个实例都有
  `requirepass`。用 `pg addon password valkey --name vcache` 取回，或直接用
  `pg valkey-cli`（自动注入）。
- **安装时报镜像不存在**——三个大版本都是按需拉取的
  `docker.io/valkey/valkey` 纯上游镜像；离线主机先 `podman load` 对应 tar
  （`valkey_7.2.16.tar` / `valkey_8.1.12.tar` / `valkey_9.2.0.tar`，见
  `docs/images.md`），拉取会被跳过。
- **手改的 `version` 不在表中**——`install` 报错并建议 `--image`，而不是悄悄
  改写你的值。
- **端口已被占用**——自动分配会扫描宿主监听并跳过；需要稳定端口就固定
  `--port`。
- **副本拒绝写入（`READONLY`）**——那是角色而非故障：把写发送到主节点（见
  [读副本](#读副本)）。
- **`--replica-of` 找不到主节点**——该值是 `addons.valkey` 中的名字，而不是
  `addons.redis` 中的；valkey 副本只能跟随 valkey 主节点。
- **`maxmemory` 淘汰得比预期多**——传 `--maxmemory-policy noeviction` 获得硬
  上限，或干脆省略 `--maxmemory`。
- **硬崩溃时 AOF 丢最新写入**——`everysec`（默认）最多丢一秒；用
  `--appendfsync always`。优雅的 `pg stop` 总会刷盘。

## 已知限制

- **没有自动故障转移**——支持读副本与原生集群分片，但没有 HA 控制面：
  **Sentinel** 不在范围，集群编排也不在——pgcli 安装集群成员，但从不负责组装、
  扩缩容或再均衡。副本提供读扩展与冗余副本，不是自愈对。
- **原生集群：组装与重塑仍是手工**——`--cluster` 安装 cluster-enabled 节点，
  `--cluster-replicas N` 让每个主节点可带跟随者，但
  `--cluster create`/`add-node`/reshard/`del-node` 由你执行。删除成员只是普通
  删除，不会做 slot 重新分配。
- **跨主机集群（`--advertise-host`）为手工流程，未进 e2e**——状态与 redis 相同：
  已实现、有单测、经手工验证；单主机流程有自动化 e2e。
- **没有 TLS**——把网络路径视为可信并依赖 `requirepass`，或让实例只监听
  loopback（`--listen 127.0.0.1`）。跨主机集群总线同样不加密。
- **macOS 代码完备、未实测**——桥接路径与对象存储一致，尚未在 Mac 上实际运行。
- **默认持久化是 RDB 粒度**——崩溃会丢最后一次快照之后的写入。在意时开启
  `--aof`。
- **是独立 add-on，不是 redis 迁移**——redis 与 valkey 实例是独立命名空间，各有
  独立端口池与命令；pgcli 不会把既有 redis add-on 迁移到 valkey。请安装 valkey
  add-on，把应用指过去，再择机下线 redis 实例。
