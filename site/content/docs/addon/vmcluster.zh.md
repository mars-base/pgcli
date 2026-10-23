---
title: "VMCluster"
description: "以 pgcli 插件方式运行 VictoriaMetrics 集群版——3 个组件（vminsert + vmselect + vmstorage）各跑一个容器，支持跨主机部署和水平扩展"
weight: 58
---

`pg addon install vmcluster` 以受管容器方式运行 [VictoriaMetrics 集群版](https://docs.victoriametrics.com/cluster-victoriametrics/)——3 个组件各跑一个容器，使用官方 `docker.io/victoriametrics/{vminsert,vmselect,vmstorage}:v1.154.1-cluster` 镜像。与单机版 `victoriametrics` addon（1 容器 1 端口）不同，它管理最多 3 个容器、3 个镜像、5 个端口，支持跨主机部署和水平扩展。

**架构**：

| 组件 | 职责 | HTTP 端口 | 内部 TCP |
|------|------|-----------|----------|
| vminsert | 摄入端，按 metric hash 分片写入所有 vmstorage | 8480 | 拨 vmstorage:InternalInsertPort |
| vmselect | 查询端，从所有 vmstorage 合并结果 | 8481 | 拨 vmstorage:InternalSelectPort |
| vmstorage | 数据存储（可多节点分片+副本） | 8482 | 监听 InternalInsertPort + InternalSelectPort |

> **没有 Web 登录/密码。** 与单机版 VictoriaMetrics 一样，集群版**完全不带鉴权**。
> 它的 HTTP API 可执行任意 MetricsQL（PromQL 的超集），昂贵查询也能把自身打满，
> 所以远程暴露的实例**只靠网络边界**保护——防火墙，或 `--listen 127.0.0.1` 收紧
> 为仅回环。请把它当作一个无鉴权的读/写控制台来对待。

> **平台**：仅 Linux。VMCluster 需要 host networking 来承载组件间的内部 TCP 端口。
> macOS 上安装会被拒绝并给出明确错误提示。

## 部署形态

### 形态 1 — 单机收敛（默认）

3 个组件全在一台主机上，类似 BeeGFS v1：

```bash
pg addon install vmcluster --name vmc
# 1 host, 3 containers (vminsert + vmselect + vmstorage)
# --storage-node 自动推导为 127.0.0.1:<internalPorts>
```

### 形态 2 — 跨主机分布

```bash
# Host A — 存储节点 1
pg addon install vmcluster --name vmc --roles storage --listen 10.0.0.1

# Host B — 存储节点 2
pg addon install vmcluster --name vmc --roles storage --listen 10.0.0.2

# Host C — 接入层（vminsert + vmselect）
pg addon install vmcluster --name vmc --roles insert,select \
  --storage-node 10.0.0.1:8483:8484 \
  --storage-node 10.0.0.2:8483:8484
```

### 形态 3 — 水平扩展 + 副本

```bash
# replicationFactor=2 → 每条数据写 2 个 vmstorage
pg addon install vmcluster --name vmc --roles insert,select \
  --replication-factor 2 \
  --storage-node 10.0.0.1:8483:8484 \
  --storage-node 10.0.0.2:8483:8484
```

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `vmc` | 实例名（map 键） |
| `--roles` | `insert,select,storage` | 本主机运行的组件子集（逗号分隔，跨主机时选子集） |
| `--storage-node` | 自动推导 | vmstorage 地址（可重复；格式 `host:insertPort:selectPort`），列出集群中所有 vmstorage 节点。单机收敛时省略即可 |
| `--insert-port` | 自动（基址 8480） | vminsert HTTP 端口 |
| `--select-port` | 自动（基址 8481） | vmselect HTTP 端口 |
| `--storage-port` | 自动（基址 8482） | vmstorage HTTP 端口 |
| `--internal-insert-port` | 自动（基址 8483） | vmstorage TCP 端口（vminsert 协议） |
| `--internal-select-port` | 自动（基址 8484） | vmstorage TCP 端口（vmselect 协议） |
| `--listen` | `0.0.0.0` | 绑定地址——默认开放全部网卡；`127.0.0.1` 收紧为仅回环 |
| `--retention-months` | `1` | 数据保留月数（仅 vmstorage 角色） |
| `--replication-factor` | `1` | 副本因子（每条数据写几个 vmstorage 节点） |
| `--insert-image` | `docker.io/victoriametrics/vminsert:v1.154.1-cluster` | vminsert 镜像覆盖 |
| `--select-image` | `docker.io/victoriametrics/vmselect:v1.154.1-cluster` | vmselect 镜像覆盖 |
| `--storage-image` | `docker.io/victoriametrics/vmstorage:v1.154.1-cluster` | vmstorage 镜像覆盖 |
| `--insert-extra-arg` | — | vminsert 的额外 CLI flag（可重复） |
| `--select-extra-arg` | — | vmselect 的额外 CLI flag（可重复） |
| `--storage-extra-arg` | — | vmstorage 的额外 CLI flag（可重复） |
| `--data-dir` | 自动 | vmstorage 数据目录覆盖 |
| `--force` | false | 即使正在运行也重建 |

## 安装

```bash
pg addon install vmcluster --name vmc
```

命令会按需拉取 3 个镜像、按依赖序创建容器（storage → insert → select），并打印摘要：

```
✓ VMCluster installed: "vmc"
  Roles:       storage, insert, select (converged on this host)
  Containers:  pgcli-vmcluster-default-vmc-storage, pgcli-vmcluster-default-vmc-insert, pgcli-vmcluster-default-vmc-select
  Images:      docker.io/victoriametrics/vmstorage:v1.154.1-cluster, docker.io/victoriametrics/vminsert:v1.154.1-cluster, docker.io/victoriametrics/vmselect:v1.154.1-cluster
  Data:        /home/you/.pgcli/addon/vmcluster/vmc/data
  Retention:   1 month(s)
  Insert URL:  http://0.0.0.0:8480/ (vminsert — ingestion)
  Select URL:  http://0.0.0.0:8481/ (vmselect — queries)
  Storage URL: http://0.0.0.0:8482/ (vmstorage — admin/metrics)
  Internal:    TCP 8483 (vminsert), 8484 (vmselect)
```

VMCluster 立即可摄入和查询。

## 集群版 URL 格式

**重要**：集群版的 URL 路径与单机版 VictoriaMetrics 不同。所有 API 端点都需要在路径中包含 `<accountID>`（使用 `0` 表示默认账户）：

**通过 vminsert 摄入数据**：
```bash
# Prometheus remote_write
curl -X POST -d 'test_metric{job="pgcli"} 42' \
  http://127.0.0.1:8480/insert/0/prometheus/api/v1/import/prometheus

# InfluxDB line protocol
curl -X POST -d 'cpu,host=vm01 value=42' \
  http://127.0.0.1:8480/insert/0/influx/write

# Graphite plaintext
echo "test.metric 42 $(date +%s)" | nc 127.0.0.1 8480
```

**通过 vmselect 查询数据**：
```bash
# Prometheus query API
curl 'http://127.0.0.1:8481/select/0/prometheus/api/v1/query?query=test_metric'

# Range query
curl 'http://127.0.0.1:8481/select/0/prometheus/api/v1/query_range?query=test_metric&start=2024-01-01T00:00:00Z&end=2024-01-02T00:00:00Z&step=1h'

# Export
curl 'http://127.0.0.1:8481/select/0/prometheus/api/v1/export?match[]={__name__!=""}'
```

**Grafana 数据源**：指向 vmselect，URL 为 `http://<host>:8481/select/0/prometheus/`

**Prometheus remote_write**：配置 `remote_write` 到 `http://<host>:8480/insert/0/prometheus/api/v1/write`

## 端口

每个 VMCluster 实例占有 **5 个连续端口**，取自 `vmcluster_start_port` 端口池（默认 8480）：

- `+0` InsertPort（vminsert HTTP — 摄入端点）
- `+1` SelectPort（vmselect HTTP — 查询端点）
- `+2` StoragePort（vmstorage HTTP — 管理/指标端点）
- `+3` InternalInsertPort（vmstorage TCP — vminsert 拨此端口）
- `+4` InternalSelectPort（vmstorage TCP — vmselect 拨此端口）

连续分配是因为 `--storage-node` 显式引用内部 TCP 端口，整块必须保持连续。`8480` 与 VictoriaMetrics（`8428`）、Prometheus（`9090`）、Grafana（`3000`）等都不重号。

## 配置

```yaml
addons:
  vmcluster:
    vmc:
      name: vmc
      container_prefix: pgcli-vmcluster-default-vmc
      insert_image_tag: docker.io/victoriametrics/vminsert:v1.154.1-cluster
      select_image_tag: docker.io/victoriametrics/vmselect:v1.154.1-cluster
      storage_image_tag: docker.io/victoriametrics/vmstorage:v1.154.1-cluster
      listen: 0.0.0.0
      insert_port: 8480
      select_port: 8481
      storage_port: 8482
      internal_insert_port: 8483
      internal_select_port: 8484
      roles:
        - storage
        - insert
        - select
      storage_nodes:
        - address: "10.0.0.1:8483"
          select_port: 8484
        - address: "10.0.0.2:8483"
          select_port: 8484
      replication_factor: 1
      retention: "1"
      autostart: false
```

**没有配置文件**——一切都作为 CLI flags 传给 3 个二进制。`storage_nodes` 是跨主机拓扑；空即自动推导单节点。

## 动态添加存储节点

VictoriaMetrics 集群版没有动态发现机制——`-storageNode` 列表在 vminsert/vmselect 容器启动时写入。添加 vmstorage 节点需要一个**短暂的维护窗口**来重建接入层容器（不是热加载）。现有 vmstorage 里的历史数据不受影响。

> **维护影响**：重建 vminsert/vmselect 会导致**几秒写入中断**。如果写入连续性很重要，请在低峰期操作。vmstorage 里的历史数据在整个过程中始终可用——只有摄入会短暂中断。

### 步骤

1. **在新主机上创建 vmstorage**：

```bash
# Host C — 新存储节点
pg addon install vmcluster --name vmc --roles storage --listen 10.0.0.3
```

2. **在接入层主机上重建 vminsert + vmselect**，追加新的 `--storage-node`：

```bash
pg addon install vmcluster --name vmc --roles insert,select --force \
  --storage-node 10.0.0.1:8483:8484 \
  --storage-node 10.0.0.2:8483:8484 \
  --storage-node 10.0.0.3:8483:8484
```

`--force` 会删掉并重建 vminsert/vmselect 容器，中间有**几秒写入中断**。已有的 vmstorage 数据完全不受影响（只有接入层容器被替换）。

### 添加节点后的数据分布

- **旧数据**保留在原来的 vmstorage 节点上（不会自动 rebalance）。
- **新写入**按 metric hash 在所有节点（旧 + 新）之间重新分片。
- vmselect 从所有节点合并结果，查询端无缝看到新旧数据。

VictoriaMetrics 集群版**不会**把已有数据迁移到新加入的节点。对大多数监控场景来说这不是问题——时间序列是持续写入的，几小时后新节点上就有足够的数据让查询均衡。如果需要完全均匀分布，只能导出再重新导入（`/api/v1/export` → `/api/v1/import`）。

## 移除存储节点

反向操作：先重建 vminsert/vmselect 时**去掉**要下线节点的 `--storage-node`，再在目标主机上执行 `pg addon remove vmcluster`。被移除的 vmstorage 上的数据将不再对查询可见——如果需要保留，请提前导出。

## 启停

```bash
pg addon stop vmcluster
pg addon start vmcluster
```

容器以 podman 的 `unless-stopped` 重启策略运行，宿主重启后随 podman 服务自动回来。

### 开机自启

```bash
pg autostart enable --vmcluster --name vmc
```

开机启动以集群为单位：每台主机只拉自己的角色容器，按依赖序（storage → insert → select），已有容器直接启动、仅缺失/异常的重建，不会重建正在运行的容器。跨主机部署时要在每台主机上分别执行。完整机制见 [开机自启]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove vmcluster              # 保留 vmstorage 数据——重装即复活时序数据
pg addon remove vmcluster --clean-data # 连 vmstorage 数据一起删
```

只有 vmstorage 有持久数据；vminsert 和 vmselect 是 stateless，删除后不留任何东西。

## 日志

```bash
pg logs addon vmcluster                    # vmstorage 日志（默认 service）
pg logs addon vmcluster --service insert   # vminsert 日志
pg logs addon vmcluster --service select   # vmselect 日志
```

## 已知限制

- **无鉴权。** 集群版的 API 执行任意 MetricsQL；`0.0.0.0` 绑定的唯一保护是网络边界。
- **仅 Linux。** 组件间的内部 TCP 端口需要 host networking。
- **无自动负载均衡。** 多 vminsert/vmselect 实例需在前面手动放 HAProxy 或 nginx。
- **无 TLS。** 需要 TLS 终止请用 nginx addon 前置。
- pgcli 不校验 `--extra-arg` 值。有错的 flag 只会在启动时从容器日志里暴露。
