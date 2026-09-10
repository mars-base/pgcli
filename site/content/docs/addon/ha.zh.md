---
title: "Patroni 高可用"
description: "以 pgcli HA 模式运行 Patroni 版 PostgreSQL 高可用——自动故障切换、switchover，基于 DCS 的集群"
weight: 45
---

[Patroni](https://patroni.readthedocs.io) 是 PostgreSQL 高可用的事实标准：它
管理每个 postmaster 的生命周期、在成员间流式复制，并在 leader 失联时执行
**自动故障切换**。pgcli 将 Patroni 作为独立的模式暴露 —— `pg ha` —— 而不是
把它揉进普通的 `pg` 实例管理路径。

> **这是独立模式，不是 addon 子命令。** 拥有 PostgreSQL 进程的是 Patroni
> （不是 pgcli）。`pg ha` 成员**不会**出现在 `cfg.Instances` 里，不复用实例
> 生命周期代码，也不是 `pg create` 创建的。它唯一借鉴 addon 体系的地方，是
> 用 [etcd](./etcd/) 做 DCS。

**仅支持 Linux**，与 [etcd](./etcd/) addon 一样：Patroni 成员依赖 rootless
podman 的 host 网络。macOS 上这些命令会快速失败并给出清晰提示。

## 所有权边界

最需要先理解的一件事，是谁拥有什么：

| 职责 | 归属 |
|------|------|
| Patroni 容器（run/start/stop/rm）、镜像、`patroni.yml`、端口分配、密码、DCS 接线 | **pgcli** |
| postmaster 生命周期、`initdb`、PostgreSQL 配置渲染、复制槽、**故障切换** | **Patroni** |
| switchover / failover / pause / edit-config | pgcli 在临时容器里包装 **patronictl** |

pgcli 从不直接编辑运行中 PostgreSQL 的配置，也从不跑 `initdb` —— 这两件事都
是 Patroni 做的。这也是为什么普通 PG 镜像的 `docker-entrypoint-initdb.d`
约定（`admin` 角色 / 默认库）在这里**不适用**：Patroni 自己 bootstrap 集群，
角色体系是 `postgres`（superuser）、`replicator`、`rewind_user`。

### 容器生命周期 ≠ 安全的 PG 重启

因为 Patroni 是容器里的 PID 1：

- **`pg ha create`** 是*重装*语义（stop + 重建容器）。重建 leader 会让该节点
  完全离线并**触发故障切换**。改成员配置靠重跑 `create` 是安全的；动态参数
  请优先用 `pg ha edit-config`，它绝不碰容器。
- **`pg ha start` / `pg ha stop`** 是裸的容器 start/stop。停掉 leader 容器和
  该节点宕机一样具有破坏性 —— Patroni 会切到副本。*计划内*运维请先
  `pg ha pause`（关掉自动 failover），再停容器。
- **paused** 的集群在 resume 之前**没有自动 failover**。

## 工作原理

`pg ha` 每个成员跑一个 Patroni 容器。同一 scope 的所有成员指向同一个
**DCS**（一个 [etcd](./etcd/) 集群），DCS 保存集群的动态配置和 leader 锁：

1. **某个 scope 的第一次 `pg ha create`** 完成 bootstrap：Patroni 跑
   `initdb`，抢下 leader，成为 leader。
2. **之后的每个成员**自动从当前 leader `pg_basebackup` 并开始流复制 —— 没有
   flag 区分"添加"还是"加入"。
3. `pg ha` 的控制命令（`switchover`、`pause` 等）在临时容器里对 DCS 跑
   `patronictl`，所以即使本机没有成员容器在跑也能工作。

DCS 是唯一真相来源：Patroni 每个周期都从 DCS 重新渲染每个成员的
`postgresql.conf`/`pg_hba.conf`，手工改盘上文件会丢 —— 请用
`pg ha edit-config`。

## Scope 与 DCS 存储路径

`pg ha create <scope>` 里的 `<scope>` **就是 Patroni 集群名** —— 一个 HA 集群
的身份标识。所有接收 scope 的命令（`status`、`switchover`、`failover`、
`pause`、`ctl` 等）指的都是同一个集群；某 scope 的第一次 `create` 完成
bootstrap，之后同 scope 的 `create` 是加成员。（`--member` 是集群**内部**逐主
机的节点名 —— 每台机器一个成员。）

`pg.yaml` 中集群以该 scope 为 key 存放在 `addons.patroni.<scope>` 下。

### etcd 里实际存了什么

Patroni 把一切存在固定的 etcd namespace + scope 之下：

- **namespace：** `/service/` —— Patroni 的顶层键，pgcli 不改动。
- **scope：** pgcli 写入 `patroni.yml` 的是 `PatroniScope(scope)`，即你起的名字
  **加上 pgcli 的 namespace 后缀**。Patroni 自己没有 namespace 概念（其 etcd
  前缀就是裸 scope），pgcli 把后缀烘进 scope，防止两个 pgcli namespace 共用一
  套 etcd 时串群。

因此一个集群在 etcd 里的完整前缀是：

```
/service/<scope>[-<namespace>]/          # 例：/service/app/（无 namespace）
                                         #     /service/app-prod/（namespace: prod）
├── initialize       # bootstrap 标记（只写一次）
├── leader           # 当前主；值 = 成员名
├── members/<member> # 每个成员的注册信息（conn_url、api_url、状态）
├── status           # 集群 LSN / 状态
├── config           # 动态配置（pause 标记也在这里）
├── history          # 配置修订历史
├── failover         # 手动 failover 请求
└── sync             # 同步复制状态
```

前缀以下的键属于 Patroni 自己的 DCS 布局。想查看，把 `pg etcdctl` 指向同机
器/同配置里一个运行中的 etcd 成员：

```bash
ETCDCTL_ENDPOINTS=http://127.0.0.1:2379 pg etcdctl get /service/ -- --prefix --keys-only
```

注意：即使建集群时用的是裸名字，这里显示的 scope 也会带着 namespace 后缀。

## 安装

前置条件：一个 DCS。要么复用本地 etcd addon 成员，要么指向外部 etcd。

```bash
# 1. 一个 DCS —— 这里用 etcd addon（多节点配置见 etcd 页面）
pg addon install etcd --name m1

# 2. 第一个成员 bootstrap 集群（成为 leader）
pg ha create app --member node1 --etcd m1

# 3. 后续成员自动以副本身份加入
pg ha create app --member node2 --etcd m1
pg ha create app --member node3 --etcd m1

# 4. 观察
pg ha status app
```

`pg ha status app` 渲染 `patronictl list` —— 恰好一行 `Leader`，其余是
`Replica … streaming`：

```
+ Cluster: app (7683433951661608987) +-----------+----+-------------+-----+------------+-----+
| Member | Host            | Role    | State     | TL | Receive LSN | Lag | Replay LSN | Lag |
+--------+-----------------+---------+-----------+----+-------------+-----+------------+-----+
| node1  | 127.0.0.1:5432  | Leader  | running   |  1 |             |     |            |     |
| node2  | 127.0.0.1:5433  | Replica | streaming |  1 |   0/3000060 |   0 |  0/3000060 |   0 |
| node3  | 127.0.0.1:5434  | Replica | streaming |  1 |   0/3000060 |   0 |  0/3000060 |   0 |
+--------+-----------------+---------+-----------+----+-------------+-----+------------+-----+
```

不带参数时，`pg ha status` 汇总每个集群：容器状态、成员数、每个成员的
`pg=`/`rest=` 端口。

## DCS 选型

Patroni 只需要一个可达的 etcd 集群，因此有两种布局：

- **同机部署** —— 把 etcd 成员作为 addon 跑在和 Patroni 成员相同的主机上，用
  `--etcd m1,m2,m3` 指定。最省事：一台机器同时扮演两个角色，不多占主机。适合
  起步的 3 节点 HA 集群。
- **专用 DCS 主机** —— 把 etcd 集群跑在**单独的（虚拟）机器**上，再用
  `--etcd-endpoints` 让每个 Patroni 成员指向它。下面每一行都在不同主机上执行
  （pgcli 是按主机管理的）：

  ```bash
  # 主机 E1 (10.0.0.20) —— bootstrap etcd 集群
  pg addon install etcd --name e1 --cluster prod \
      --advertise-host 10.0.0.20 --client-port 2379 --peer-port 2380

  # 主机 E2 (10.0.0.21) —— 加入
  pg addon install etcd --name e2 --cluster prod \
      --advertise-host 10.0.0.21 --client-port 2379 --peer-port 2380 \
      --join http://10.0.0.20:2379

  # 主机 E3 (10.0.0.22) —— 加入
  pg addon install etcd --name e3 --cluster prod \
      --advertise-host 10.0.0.22 --client-port 2379 --peer-port 2380 \
      --join http://10.0.0.20:2379

  # 主机 A / B / C —— 每台一个 Patroni 成员，endpoint 列表完全相同
  pg ha create app --member node1 --advertise-host 10.0.0.11 \
      --etcd-endpoints 10.0.0.20:2379,10.0.0.21:2379,10.0.0.22:2379
  ```

  这是可用性更高的拓扑：etcd 的 quorum 不会随某台 PG 主机一起消失，重装数据库
  机器也不会顺带带走 DCS。PG 主机只是**访问** DCS；`--etcd-endpoints` 列出全部
  成员的 client URL，因此某一台 etcd 主机宕机时 Patroni 会自动顺延到下一个
  endpoint（写入仍然需要 etcd 自身的 quorum —— 3 台中要活 2 台）。每台 PG 主机
  上的 endpoint 列表要保持一致。

  只写一个 endpoint（`--etcd-endpoints 10.0.0.20:2379`）**也能用** —— 那一台
  etcd 成员会服务整个集群，另外两台被它挡在后面。但这又把单点引回来了：E1 一
  宕，Patroni 就够不到 DCS，**哪怕 etcd 的 quorum 是健康的**；leader 续不上锁
  就会自我降级。你实际有几个成员，就把它们全部列出来。

生产环境推荐独立的奇数（3 或 5）成员 etcd 集群；同机部署用于开发和小型部署即
可。

## 命令

| 命令 | 作用 |
|------|------|
| `pg ha create <scope> --member <m> …` | 登记 + （重）装一个成员 —— **重建 = 节点离线** |
| `pg ha status [scope]` | 所有集群，或单个集群的 `patronictl list` |
| `pg ha switchover <scope>` | 计划内切换 leader（patronictl 会二次确认；`--yes` 可脚本化） |
| `pg ha failover <scope>` | 立即提升一个副本 |
| `pg ha pause` / `resume <scope>` | 关闭 / 重新开启自动 failover |
| `pg ha edit-config <scope> -- …` | 查看或修改 DCS 里的动态配置（绝不重建容器） |
| `pg ha start` / `stop <scope> --member m \| --all` | 裸容器 start/stop（见生命周期警告） |
| `pg ha remove <scope> --member m \| --scope-all [--clean-data] [--force]` | 移除成员；`--scope-all` 同时清 DCS |
| `pg ha passwords <scope> [--file F]` | 导出存储的密码集（`--passwords-file` 的格式，供其它主机使用） |
| `pg ha ctl <scope> -- <patronictl 参数…>` | 透传任意 `patronictl` 命令 |

`--` 之后的 flag 原样到达 `patronictl`（cobra 会剥掉 `--`），所以
`pg ha ctl app -- show-config`、`pg ha ctl app -- topology`、
`pg ha edit-config app -- -s synchronous_mode=true --force` 都能用。
`edit-config --show` 是 `ctl … -- show-config` 的便捷别名。

## 跨主机成员

每台主机各自跑自己的 pgcli，**只管本机的**成员；集群通过共享 DCS 汇合，所以
两台主机的 `pg.yaml` 各自持有同一个 `scope` 的一部分视图。

```bash
# host A (10.0.0.11) —— bootstrap（自带 etcd，或外部 DCS）
pg ha create app --member node1 --advertise-host 10.0.0.11 \
    --etcd-endpoints 10.0.0.9:2379,10.0.0.10:2379

# host A —— 导出第一台生成的密码，供其他主机使用
pg ha passwords app --file app-passwd.yml

# host B (10.0.0.12) —— 加入，共用同一个 DCS 与同一套密码
pg ha create app --member node2 --advertise-host 10.0.0.12 \
    --etcd-endpoints 10.0.0.9:2379,10.0.0.10:2379 \
    --passwords-file app-passwd.yml
```

跨主机清单（以下每一项都必须在每台主机上对齐）：

1. **端口** —— 自动分配是按主机的，但 `connect_address` 存在 DCS 里全集群共
   享。请在**每台主机上把 `--host-port` / `--restapi-port` 设成相同值**，否则
   副本之间连不通。
2. **密码** —— Patroni 的复制 / rewind / REST-API 鉴权是全集群一致的。用
   `pg ha passwords app --file app-passwd.yml` 导出第一台生成的那套，对每台其
   他 `pg ha create` 传 `--passwords-file app-passwd.yml`。
3. **`--advertise-host`** —— 跨主机成员必传；它把监听地址翻成 `0.0.0.0` 并把
   可达 IP 写进 `connect_address`。留空则该成员只走回环。**第一个（bootstrap）
   成员也不例外**：它就是 leader，其 `connect_address` 会写进 DCS，之后每台主
   机的 `pg_basebackup` 都拨这个地址 —— 用默认值引导的集群永远无法再加跨机成
   员。只要以后可能在别的主机加成员，第一次 `create` 就该传本机 LAN IPv4。
4. **防火墙** —— 在成员之间两两放行两个端口（PG + REST API）。

pgcli 不校验对端配置；`pg ha status` 会显示每个成员的 `connect_address`，便于
自查可达性。

## 密码

某个 scope 的第一次 `pg ha create` 生成四份凭据 —— `superuser`、
`replication`、`rewind`，以及 `restapi` basic-auth 对（`restapi_user` /
`restapi_password`）—— 存在 `pg.yaml` 的对应集群下。命令行上永远不用传密码：
第一个成员自动生成，`pg ha passwords` 把存储的这套导出成 `--passwords-file`
的格式，供其它主机复用：

```bash
pg ha passwords app --file app-passwd.yml   # 即 --passwords-file 的格式，写入权限 0600
```

（不加 `--file` 则输出到 stdout —— 建议用 `--file`，免得密码落进 shell 历史和
滚屏。）

需要完全自己掌控（或想手写这份文件）时，用同样格式配 `--passwords-file`：

```yaml
# app-passwd.yml
superuser: <postgres superuser 密码>
replication: <replicator 密码>
rewind: <rewind_user 密码>
restapi_user: postgres          # 可选，默认 postgres
restapi_password: <REST API basic-auth 密码>
```

```bash
pg ha create app --member node1 --etcd m1 --passwords-file app-passwd.yml
```

`pg ha create` 会打印每份密码的来源（生成并存盘 vs. 来自文件路径）。渲染出的
`patroni.yml` 以 `0600` 权限写入，因为它内嵌了所有这些密码。

## 开机自启

和其他基础 addon 一样，Patroni 成员的容器可以在主机重启后被拉起 —— 但它只
**启动已存在的容器**，读取盘上已有的 `patroni.yml`；绝不重新渲染配置或重建
数据。

```bash
pg autostart enable --ha --scope app --name node1
```

成员一次切一个（`--ha --scope <scope> --name <member>`）。相对 DCS 的启动顺序
无所谓：Patroni 会重试直到 etcd 应答，然后正常重选。启动服务的机制见
[autostart](/docs/autostart/) 页面。

## 日志

```bash
pg logs addon patroni --scope app --name node1          # 最近 50 行
pg logs addon patroni --scope app --name node1 -f       # 跟踪
```

容器名为 `pgcli-patroni-<scope>-<member>`（设置了 `namespace` 时带前缀）。

## 连接

客户端连的是 **leader** 的 PostgreSQL 端口。用 `pg ha status app` 找 leader
（`Leader` 行的 `Host` 就是它的 `connect_address`），再把 psql/pgcli 指过去。
单机默认（仅回环）成员下，就是 `127.0.0.1:<host_port>`。

```bash
psql "host=127.0.0.1 port=<leader_port> user=postgres dbname=postgres"
```

failover 后 leader 会变，所以固定连接串应当避免 —— 除非前面有连接池或 Patroni
REST API 的 leader 重定向兜着。

## pg ha vs. pg replica —— 怎么选

pgcli 有两种拿到副本的方式：

| | [`pg replica`](/docs/replica/) + [`pg failover`](/docs/failover/) | `pg ha`（Patroni） |
|---|---|---|
| 模型 | 手动：`pg replica` 建副本，`pg failover` 按需提升 | 自动：Patroni 保持 N 个成员同步并自愈 |
| Failover | 人工跑 `pg failover`；提升前主库一直宕着 | Patroni 检测到失联，约 30s 内提升副本 |
| 所有权 | pgcli 驱动 postmaster（和普通实例一样） | Patroni 驱动 postmaster；pgcli 只拥有容器 |
| 适合 | 简单读扩展、单次计划内提升、沿用 `pg` 实例工具链 | 零 RTO 可用性要求、无人值守 failover |

需要靠手工控制的副本，用 `pg replica`。需要集群在节点宕机时无人介入也能活，
用 `pg ha`。

## 配置

每个成员渲染的 `patroni.yml` 完全由 `pg.yaml` 里 `addons.patroni.<scope>` 派
生。一条典型记录：

```yaml
addons:
  patroni:
    app:
      name: app
      etcd_members: [m1]                # 或 etcd_endpoints 指向外部 DCS
      passwords:
        superuser: <superuser-password>
        replication: <replication-password>
        rewind: <rewind-password>
        restapi_user: postgres
        restapi_password: <restapi-password>
      members:
        node1:
          container_name: pgcli-patroni-app-node1
          image_tag: ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5
          host_port: 35590
          restapi_port: 39090
          data_dir: /home/you/.pgcli/addon/patroni/app/node1
          autostart: false
```

端口来自两个独立池 —— `patroni_start_port`（默认 35532）给 PostgreSQL，
`patroni_restapi_start_port`（默认 39060）给 REST API —— 所以 Patroni 成员永远
不会和普通实例或 addon 端口相撞。

DCS 的 scope 键 = scope 加命名空间后缀（Patroni 自己没有 namespace 概念，所以
把前缀烙进 scope，好让两个 pgcli namespace 共用一个 etcd 时不串台）。

## 动态配置

Patroni 的动态配置存在 DCS（etcd）的 `/service/<scope>/config` 下。每个成员
在每个循环（每 `loop_wait` 秒）都读取并应用这些配置 —— 所以 `pg ha edit-config`
是调优运行时参数的正确方式，无需重启容器。

**持久化：** 改动直接写入 etcd，不是容器里的文件。容器重启、`pg ha start`/`stop`，
甚至 `pg ha create`（重装）都不会丢失这些设置 —— 新成员会自动从 DCS 拿到最新配置。

### 查看当前配置

```bash
pg ha edit-config app --show
```

这是 `pg ha ctl app -- show-config` 的便捷别名。输出是存在 `/service/<scope>/config`
里的完整 JSON。

### 修改参数

```bash
# 设置单个参数
pg ha edit-config app -- -s loop_wait=5

# 设置多个参数
pg ha edit-config app -- -s loop_wait=5 -s retry_timeout=3

# 不确认直接应用（脚本场景）
pg ha edit-config app -- -s loop_wait=5 --force

# 交互式编辑（用 $EDITOR 打开当前配置）
pg ha edit-config app
```

改动后，所有成员在下一个循环（`loop_wait` 秒内）应用。无需重启。

### 常用可调参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `loop_wait` | `10` | leader 循环间隔（秒），用于锁续租、DCS 更新 |
| `ttl` | `30` | leader 锁的 TTL。leader 在此窗口内未续租，副本触发 failover |
| `retry_timeout` | `10` | DCS/PostgreSQL 操作超时。必须 `< ttl - loop_wait`，给 leader 至少一次重试机会 |
| `maximum_lag_on_failover` | `1048576` | 副本可被提升的最大复制延迟（字节），默认 1 MB |
| `synchronous_mode` | `false` | 启用同步复制（零数据丢失，更高延迟） |
| `synchronous_node_count` | `1` | 同步 standby 节点数（`synchronous_mode=true` 时） |
| `use_pg_rewind` | `true` | 用 `pg_rewind` 让失败的 leader 重新加入（比全量 `pg_basebackup` 快） |
| `use_slots` | `true` | 启用复制槽（副本断开时防止 WAL 丢失） |
| `failover_timeout` | `0` | failover 前等待时间（0 = leader 丢失时立即切换） |

PostgreSQL 运行时参数也可以在 `postgresql.parameters` 下设置：

```bash
pg ha edit-config app -- -s 'postgresql.parameters.max_connections=200'
pg ha edit-config app -- -s 'postgresql.parameters.work_mem=64MB'
```

这会触发 PostgreSQL `reload`（或重启，取决于参数的 context）。查阅 `pg_hba.conf`
和 `postgresql.conf` 参数文档，了解哪些设置需要重启。

### 不要直接编辑的内容

- **不要直接编辑磁盘上的 `patroni.yml`** —— 每次 `pg ha create` 都会从 `pg.yaml`
  重新生成，且 Patroni 的动态配置从 DCS 读取。
- **不要直接编辑 `postgresql.conf`** —— Patroni 每个循环都会从 DCS 配置覆盖它。
- **不要改 `scope` 或 `namespace`** —— 这些在 bootstrap 时烙进 DCS 键，无法修改，
  只能重建集群。

## 注意

- **仅 Linux / rootless。** 成员通过 `--userns=keep-id` 以宿主用户身份运行，
  这样 rootless 容器才能读 `0600` 的配置、写自己的数据目录。
- **`pg_hba.conf` 有意保持宽松**（`host all all all scram-sha-256` + 一行
  `replication`）。rootless podman 的 pasta 会改写回环源地址，收紧成固定白名
  单是后续待做的改进 —— 现阶段别把这些端口暴露给不受信网络。
- **DCS（etcd）本身也有安全注意事项** —— 见 [etcd](./etcd/) 页面：pgcli 管理
  的 etcd 不启用 TLS 或鉴权。
- **没有 `init.sh` / `docker-entrypoint-initdb.d`。** Patroni 自己 bootstrap
  集群，所以普通实例的 `admin`/默认库约定在这里不存在；连 `postgres`
  （superuser）来建角色。
- **`use_slots` / `use_pg_rewind`** 已启用：Patroni 接管复制槽，并能用
  `pg_rewind` 让宕过的 leader 重新加入，而不必全量 rebase。
