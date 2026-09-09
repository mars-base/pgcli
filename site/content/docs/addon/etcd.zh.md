---
title: "etcd"
description: "以 pgcli addon 形式运行独立的 etcd 集群，用于 HA / DCS 场景"
weight: 30
---

etcd 是一个分布式键值存储。pgcli 可以将一个或多个 etcd 成员作为**独立的顶层
addon** 运行 —— 它是共享基础设施，而非绑定某个实例的 sidecar。常见用途是为
PostgreSQL 高可用栈充当 DCS（Distributed Concurrent Store），或作为通用的配置 /
锁服务。

> **安全提示：** pgcli 管理的 etcd 成员目前**不提供** TLS/CA 证书，也**没有**
> 认证 / RBAC —— 任何能连到 client 端口的客户端都拥有完整读写权限。部署时请依靠
> 网络隔离（默认仅绑定回环；广播端口务必放在受信网络内）。CA/TLS 与认证/RBAC
> 支持在后续版本中规划开发。

成员通过一次次 `pg addon install etcd` 管理：第一个成员引导（bootstrap）集群，
后续成员动态加入同名的集群 —— 可以在同一台机器，也可以跨机器。

## 工作原理

- **共享基础设施：** etcd 存放在 `pg.yaml` 顶层的 `addons.etcd` map 中，以成员名
  为 key，不隶属于任何单个实例。
- **host 网络 + 可配置监听：** 每个成员以 `--network host` 运行。默认情况下
  client/peer URL 绑定到 `127.0.0.1` —— etcd 本身不带认证，单机集群仅回环暴露是
  预期的安全姿态。跨机集群中每个成员通过 `--advertise-host` 广播一个对端可达的
  地址，同时**仍然**监听回环，本机 etcdctl 与远端 peer 都能访问。
- **动态成员管理：** 第一个成员以 `--initial-cluster-state new` 启动；每个后续成员
  先对一个运行中的对等成员执行 `etcdctl member add` 注册，再以
  `--initial-cluster-state existing` 启动。pgcli 自动完成这些步骤 —— 同机走成员
  容器，跨机走临时 etcdctl 容器。
- **集群身份：** `--cluster` 值相同的成员加入同一个 etcd 集群（对应 etcd 的
  `--initial-cluster-token`）。默认值为 `pgcli-etcd`。
- **内置调优：** 每个成员启动时都带周期性压缩（`--auto-compaction-mode periodic`、
  `--auto-compaction-retention 24h`）以及 8 GiB 后端配额
  （`--quota-backend-bytes 8589934592`）—— 对于小型 HA 元数据存储是合理的默认值。

## 部署形态

pgcli 支持两种集群形态，使用的安装命令完全相同 —— 区别只在于成员是否共用一台
主机。

### 单机 —— 所有成员在同一台机器（测试 / 开发）

开发机或 CI 的经典布局：每个成员用不同的自动分配端口监听回环。无需
`--advertise-host`（默认 `127.0.0.1`），无需改防火墙，主机之外什么都访问不到。

```bash
pg addon install etcd --name m1              # 127.0.0.1:2379/2380
pg addon install etcd --name m2              # 127.0.0.1:2381/2382
pg addon install etcd --name m3              # 127.0.0.1:2383/2384
```

三个成员同属 `pgcli-etcd` 集群；m1 引导，m2/m3 动态加入。这能体验真正的 3 节点
raft 语义，但没有任何主机隔离 —— 整集群随一台机器一起消失，正因如此它只是
*测试*形态。

### 跨主机 —— 每台机器一个成员（生产 HA）

生产形态：把成员分散到不同机器（最好 3 或 5 个奇数个 —— 见拓扑与 Quorum）。
每个成员广播自己主机的 LAN 地址；第一个成员 bootstrap 时**必须**带
`--advertise-host`，远端 peer 才拨得通。端口可以在各主机上重复，因为每台只绑
自己的网卡。

```bash
# 主机 A (10.0.0.1) — 引导
pg addon install etcd --name m1 --cluster prod \
  --advertise-host 10.0.0.1 --client-port 2379 --peer-port 2380

# 主机 B (10.0.0.2) — 加入
pg addon install etcd --name m2 --cluster prod \
  --advertise-host 10.0.0.2 --client-port 2379 --peer-port 2380 \
  --join http://10.0.0.1:2379

# 主机 C (10.0.0.3) — 加入
pg addon install etcd --name m3 --cluster prod \
  --advertise-host 10.0.0.3 --client-port 2379 --peer-port 2380 \
  --join http://10.0.0.1:2379
```

要求：每个成员的 `--advertise-host` 互相可达（同一 LAN），每台主机的防火墙为
**所有成员的 client 与 peer 端口**向其它成员放行（peer 是双向通信 —— 例如 4 成员
默认端口集群放行 2379-2386/tcp），且所有成员使用相同的 `--cluster` token。任意
一台机器宕机后其余主机照常工作；集群只需要保住*机器*的多数派。

| | 单机 | 跨主机 |
|---|---|---|
| 适用 | 开发、CI、功能测试 | 生产 HA |
| `--advertise-host` | 省略（回环默认） | 必填，且每个成员都要 |
| `--join` | 不用 | 除第一个外每个成员都要 |
| 端口 | 同主机内每个成员互不相同 | 可重复，各主机绑各自网卡 |
| 防火墙 | 无需（仅回环） | 每个成员的 client+peer 端口，双向放行 |
| 能否扛单机故障 | 否 | 能（quorum 成立时） |

## 命令

### 创建第一个成员

```bash
pg addon install etcd --name m1
```

```
-> Bootstrapping etcd cluster...
  [OK] etcd container started

✓ etcd installed: "m1"
  Container:    pgcli-etcd-m1
  Cluster:      pgcli-etcd
  Data dir:     ~/.pgcli/addon/etcd/m1/data
  Client port:  2379
  Peer port:    2380
  Advertise:    127.0.0.1

  Client URL: http://127.0.0.1:2379
  Connect (etcdctl): ETCDCTL_ENDPOINTS=http://127.0.0.1:2379
```

client 端口从 **2379** 起分配，peer 端口取下一个空闲端口（**2380**），由
`etcd_start_port` 自动分配。

### 往同一集群添加成员

在 `m1` 运行时，安装相同 `--cluster` 的其它成员即加入该集群：

```bash
pg addon install etcd --name m2
pg addon install etcd --name m3
```

pgcli 会找到一个运行中的对等成员作为协调者，通过 `etcdctl member add` 注册
`m2`/`m3`，再以 `--initial-cluster-state existing` 启动它们。端口继续无冲突地自动
分配（`m2` → 2381/2382，`m3` → 2383/2384）。刚扩容的集群在 leader 重选期间会短暂
失去 quorum；pgcli 会自动重试成员操作，无需在安装之间手动等待。

使用不同的 `--cluster` 可让成员归属另一个独立的 etcd 集群。

### 跨机器添加成员

不同主机上的成员也能组成同一个集群。每个跨机成员都必须广播一个**对端可达**的
地址，用 `--advertise-host` 指定。在已有成员的主机上，bootstrap 时传入本机 LAN
地址：

```bash
# 主机 A (10.0.0.1)
pg addon install etcd --name m1 --cluster prod \
  --advertise-host 10.0.0.1 --client-port 2379 --peer-port 2380
```

然后在新主机上，把 `--join` 指向任一现有成员的 client endpoint。pgcli 通过该
endpoint 完成注册（借助临时 etcdctl 容器 —— 本机无需已有成员容器或镜像，镜像会
按需拉取），取响应中的权威 `ETCD_INITIAL_CLUSTER`，再启动成员：

```bash
# 主机 B (10.0.0.2)
pg addon install etcd --name m2 --cluster prod \
  --advertise-host 10.0.0.2 --client-port 2379 --peer-port 2380 \
  --join http://10.0.0.1:2379
```

```
-> Registering member with the cluster at http://10.0.0.1:2379...
-> Starting etcd container (joining cluster)...
  [OK] etcd container started
```

跨机集群注意事项：

- **每个**成员都需要可达的 `--advertise-host`，第一个也不例外。它广播的 peer
  URL 会进入集群成员列表，用默认 `127.0.0.1` 启动的首个成员永远无法被其它主机
  加入。计划跨机组网时，bootstrap 时就要传 LAN 地址。
- `--join` 必须搭配 `--cluster`，且取值要与远端集群的 token 一致 —— 成员注册
  由远端集群校验，而非本地配置。
- `--join` 必须搭配 `--advertise-host`；否则其它成员会注册一个拨不通的 peer URL。
- 端口可以跨主机重复（两台机器都用 `2379/2380`），因为各主机只绑定自己的网卡。
- 成员间 client/peer 端口必须互通 —— **为每个成员的端口、双向放行防火墙**。peer
  之间是双向互连（都拨对方的 peer 端口），client（以及 `--join`/`pg etcdctl`）需要
  能访问 client 端口。自动分配的端口每个成员不同，从安装输出或 `pg addon list` 里
  读取实际端口（或用 `--client-port`/`--peer-port` 固定），按范围放行 —— 例如 4
  成员默认端口集群放行 `2379-2386/tcp`。漏配防火墙正是成员长时间
  `etcdserver: no leader`、或 `--join` 超时的常见原因。
- 对已注册的名字重复执行相同的 `--join` 安装会直接报错；先用
  `pg etcdctl member remove <hex-id>` 从集群注销。

### 用 `pg etcdctl` 查看

`pg etcdctl` 从短生命周期容器里运行 etcd 官方客户端 —— 无需钻进
成员容器（本机一个成员都没有时也能用，镜像按需拉取）。目标端点取自
`ETCDCTL_ENDPOINTS`，未设置时回退到配置中的第一个成员：

```bash
export ETCDCTL_ENDPOINTS=http://10.0.0.2:2379
pg etcdctl member list
pg etcdctl endpoint health
pg etcdctl endpoint status -- -w table
```

pg 自身参数解析器不接受的 etcdctl 参数（`-w table`、`--hex` 等）放在 `--` 之后。

### 查看

```bash
pg addon list
```

etcd 成员出现在 **Infra add-ons (etcd)** 段：

```
Infra add-ons (etcd):
  etcd (name: m1)
    Status:      running
    Cluster:     pgcli-etcd
    Client URL:  http://127.0.0.1:2379
    Client port: 2379
    Peer port:   2380
    Image:       quay.io/coreos/etcd:v3.5.30
    Container:   pgcli-etcd-m1
```

### 移除成员

```bash
pg addon remove etcd --name m2
```

移除时会先从集群中**注销**该成员（先解析其十六进制 member ID —— etcd v3.5 的
`member remove` 接受 ID 而非名字），再删除容器和该成员的数据目录。只要 quorum
仍成立，其余成员保持健康。

**跨主机成员需要单独注销。** 上面这一步的自动注销，只在本地 `pg.yaml` 里存在
同一集群的另一个运行中成员时才会生效 —— pgcli 看不到住在其他主机上的成员。因
此对于跨主机集群，在运行该成员的那台主机上执行移除，只会删掉容器和数据，**集
群的成员列表里仍会留下它**（一个"残留"成员）—— 存活的成员会一直尝试和已经删
除的这台主机建立 peer 连接。

正确的做法是分两步：

```bash
# 1. 在运行该成员的主机上 —— 停止并清理本地状态
pg addon remove etcd --name m4

# 2. 从任意一台仍存活的成员所在主机，把它从集群里注销
export ETCDCTL_ENDPOINTS=http://10.0.0.1:2379   # 指向一个存活成员的 client URL
pg etcdctl member list                            # 找到 m4 的十六进制 ID
pg etcdctl member remove <hex-id>                 # 如 5c7048c8f7521ec7
```

`pg etcdctl` 需要一个可达的 peer 来通信 —— 把 `ETCDCTL_ENDPOINTS` 指向该集群
中**仍在运行**的成员（在仍有本机运行成员的主机上执行时会自动回退到它；否则就
显式设置），再按 ID 移除（见[用 `pg etcdctl` 查看](#用-pg-etcdctl-查看)）。
事后用 `pg etcdctl member list` 复核：被移除的名字应该已经不在了。

## 参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--name` | 成员名，同时作为 `pg.yaml` 里的配置 key | `etcd` |
| `--cluster` | 集群身份（`--initial-cluster-token`），取值相同即同集群 | `pgcli-etcd` |
| `--client-port` | client 主机端口（0 = 从 `etcd_start_port` 自动分配） | auto |
| `--peer-port` | peer 主机端口（0 = 自动分配，取下一个空闲端口） | auto |
| `--image` | etcd 镜像 tag | `quay.io/coreos/etcd:v3.5.30` |
| `--data-dir` | 数据目录**根**——绝对路径，或相对 `base_dir`；每个成员用 `<root>/<name>/data` | `<base_dir>/addon/etcd` |
| `--advertise-host` | 本成员 peer/client URL 中广播的主机（留空 = `127.0.0.1` 单机；跨机填 LAN IP 或 FQDN） | `127.0.0.1` |
| `--join` | 要跨机加入的现有成员 client endpoint，如 `http://10.0.0.1:2379`（隐含 `--initial-cluster-state existing`；需搭配 `--advertise-host` 与 `--cluster`） | — |

容器名遵循命名空间约定：`pgcli-etcd-<namespace>-<name>`（未设 namespace 时省略）。

## 数据目录布局

数据始终按 `<root>/<name>/data` 布局，因此同机多成员绝不共用目录：

```
<base_dir>/addon/etcd/
├── m1/data/     # 成员 m1
├── m2/data/     # 成员 m2
└── m3/data/     # 成员 m3
```

`--data-dir` 指定根目录。相对值按配置的 `base_dir` 解析；解析后的根会被持久化，
保证配置文件可移植：

```bash
# base_dir: /data/pgcli  →  根为 /data/pgcli/etcd，成员 d1 → /data/pgcli/etcd/d1/data
pg addon install etcd --name d1 --data-dir ./etcd

# 绝对路径根
pg addon install etcd --name d2 --data-dir /mnt/ssd/etcd
```

`pg addon remove` 只删除该成员的 `<name>/data` 目录，保留共享根。

## 连接

将 `etcdctl`（或任意 v3 客户端）指向某成员的 client URL：

```bash
export ETCDCTL_ENDPOINTS=http://127.0.0.1:2379
etcdctl member list
etcdctl endpoint status -w table
etcdctl endpoint health
etcdctl put foo bar
etcdctl get foo
```

## 拓扑与 Quorum

etcd 需要成员多数派（quorum）才能接受写入：

| 成员数 | Quorum | 可容忍故障数 |
|--------|--------|--------------|
| 1 | 1 | 0 |
| 3 | 2 | 1 |
| 5 | 3 | 2 |

- 使用**奇数**个成员 —— 生产 HA 推荐 3 或 5。
- 移除成员可能让集群跌破 quorum（例如 3 台只剩 1 台存活），此时无法提交写入。请始终保持多数在线。

## 配置

安装后，`pg.yaml` 在顶层 `addons.etcd` 下记录每个成员：

```yaml
addons:
  etcd:
    m1:
      container_name: pgcli-etcd-m1
      name: m1
      cluster_name: pgcli-etcd
      image_tag: quay.io/coreos/etcd:v3.5.30
      data_dir: /home/user/.pgcli/addon/etcd
      client_port: 2379
      peer_port: 2380
      autostart: true
    m2:
      container_name: pgcli-etcd-m2
      name: m2
      cluster_name: pgcli-etcd
      client_port: 2381
      peer_port: 2382
```

端口起始值可通过顶层 `etcd_start_port` 配置（默认 2379）。

## 开机自启

容器还带有 `--restart unless-stopped` 策略，由每容器的 conmon 监控进程执行
（无需 podman 守护进程）——它能应对进程崩溃，但**不覆盖**主机重启。要让成员
在重启后自动拉起，按成员启用 autostart：

```bash
pg autostart enable --etcd --name m1
pg autostart enable --etcd --name m2
pg autostart enable --etcd --name m3
```

这会把成员的 `autostart: true` 写入 `pg.yaml`，并安装/刷新开机服务（见
[开机自启](/docs/autostart/)）。开机时**只做启动**：拉起成员已存在的容器，
绝不重新执行 `member add`——已初始化的成员从磁盘加载集群状态即可重新加入。
跨主机集群时，在各自主机上为该主机的成员执行命令。`pg autostart status`
可查看所有成员的状态。

## 说明

- **单成员 vs 集群：** 仅 `--name m1` 得到单成员集群；用相同 `--cluster` 再装更多
  成员即可扩容。
- **生产可用：** 每个成员默认已带周期性压缩与 8 GiB 后端配额等调优参数（见上文
  「工作原理」），可直接用于生产环境的 DCS 场景。
- **重复安装是幂等的：** 对运行中的成员再次执行 `pg addon install etcd --name <m>`
  会以更新后的参数重建其容器；已在集群中注册的成员不会被重复 add。
