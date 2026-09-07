---
title: "etcd"
description: "以 pgcli addon 形式运行独立的 etcd 集群，用于 HA / DCS 场景"
weight: 30
---

etcd 是一个分布式键值存储。pgcli 可以将一个或多个 etcd 成员作为**独立的顶层
addon** 运行 —— 它是共享基础设施，而非绑定某个实例的 sidecar。常见用途是为
PostgreSQL 高可用栈充当 DCS（Distributed Concurrent Store），或作为通用的配置 /
锁服务。

成员通过一次次 `pg addon install etcd` 管理：第一个成员引导（bootstrap）集群，
后续成员动态加入同名的集群。

## 工作原理

- **共享基础设施：** etcd 存放在 `pg.yaml` 顶层的 `addons.etcd` map 中，以成员名
  为 key，不隶属于任何单个实例。
- **host 网络 + 仅监听回环：** 每个成员以 `--network host` 运行，client/peer URL
  绑定到 `127.0.0.1`。etcd 本身不带认证，因此仅回环暴露是预期的安全姿态。
- **动态成员管理：** 第一个成员以 `--initial-cluster-state new` 启动；每个后续成员
  先对一个运行中的对等成员执行 `etcdctl member add` 注册，再以
  `--initial-cluster-state existing` 启动。pgcli 自动完成这些步骤。
- **集群身份：** `--cluster` 值相同的成员加入同一个 etcd 集群（对应 etcd 的
  `--initial-cluster-token`）。默认值为 `pgcli-etcd`。
- **内置调优：** 每个成员启动时都带周期性压缩（`--auto-compaction-mode periodic`、
  `--auto-compaction-retention 24h`）以及 8 GiB 后端配额
  （`--quota-backend-bytes 8589934592`）—— 对于小型 HA 元数据存储是合理的默认值。

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

## 参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--name` | 成员名，同时作为 `pg.yaml` 里的配置 key | `etcd` |
| `--cluster` | 集群身份（`--initial-cluster-token`），取值相同即同集群 | `pgcli-etcd` |
| `--client-port` | client 主机端口（0 = 从 `etcd_start_port` 自动分配） | auto |
| `--peer-port` | peer 主机端口（0 = 自动分配，取下一个空闲端口） | auto |
| `--image` | etcd 镜像 tag | `quay.io/coreos/etcd:v3.5.30` |
| `--data-dir` | 数据目录**根**——绝对路径，或相对 `base_dir`；每个成员用 `<root>/<name>/data` | `<base_dir>/addon/etcd` |

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

成员容器内已自带这些工具：

```bash
podman exec pgcli-etcd-m1 etcdctl member list
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
    m2:
      container_name: pgcli-etcd-m2
      name: m2
      cluster_name: pgcli-etcd
      client_port: 2381
      peer_port: 2382
```

端口起始值可通过顶层 `etcd_start_port` 配置（默认 2379）。

## 说明

- **单成员 vs 集群：** 仅 `--name m1` 得到单成员集群；用相同 `--cluster` 再装更多
  成员即可扩容。
- **无认证：** 这些成员不启用认证且仅绑定回环。未加认证前，不要把 client 端口暴露到主机之外。
- **重复安装是幂等的：** 对运行中的成员再次执行 `pg addon install etcd --name <m>`
  会以更新后的参数重建其容器；已在集群中注册的成员不会被重复 add。
