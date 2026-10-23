---
title: "VLCluster"
description: "以 pgcli 插件方式运行 VictoriaLogs 集群版——vlinsert + vlselect + vlstorage 共用同一镜像，支持跨主机分片部署"
weight: 60
---

`pg addon install vlcluster` 以受管容器方式运行 [VictoriaLogs 集群版](https://docs.victoriametrics.com/victorialogs/cluster/)——可水平扩展的日志数据库。与单机版 `victorialogs` addon（1 容器 1 端口）不同，它管理最多 3 个容器，但三个角色**共用同一个镜像**（`docker.io/victoriametrics/victoria-logs:v1.53.0`），角色完全由启动参数区分；每台主机占用 3 个 HTTP 端口。

**架构**：

| 组件 | 职责 | HTTP 端口 | 通信对象 |
|------|------|-----------|----------|
| vlinsert | 摄入端，按 stream hash 分片写入所有 vlstorage | 9430 | 拨每个 `vlstorage:StoragePort` |
| vlselect | 查询端，并行查询所有 vlstorage 并合并结果 | 9431 | 拨每个 `vlstorage:StoragePort` |
| vlstorage | 数据存储（分片，**不复制**） | 9432 | 在该端口上提供 `/internal/insert` + `/internal/select/*` |

vlinsert 和 vlselect 都是无状态的，且拨向每个 vlstorage 的**同一个端口**——不像 VMCluster 需要 `host:insertPort:selectPort` 两个内部 TCP 端口。因此存储节点地址就是 `host:storagePort`。

> **没有 Web 登录/密码。** 与单机版 VictoriaLogs 一样，集群版**完全不带鉴权**。
> 它的 HTTP API 可以摄入、查询和删除任意日志，所以远程暴露的实例**只靠网络边界**
> 保护——防火墙，或 `--listen 127.0.0.1` 收紧为仅回环。请把它当作一个无鉴权的
> 读/写控制台来对待。

> **平台**：仅 Linux。VLCluster 需要 host networking 来承载 vlinsert/vlselect
> 拨向各 vlstorage 的端口。macOS 上安装会被拒绝并给出明确错误提示。

> **没有副本。** vlinsert 只做分片、不做复制：每条 stream 只存在于一个 vlstorage
> 节点上。任一 vlstorage 下线，查询会返回 HTTP 502，直到它恢复（摄入仍在继续，
> 可由 vlagent 或客户端缓冲）。这是上游的设计属性，不是 pgcli 的限制——容量规划
> 和备份请按此考虑。

## 部署形态

### 形态 1 — 单机收敛（默认）

3 个组件全在一台主机上：

```bash
pg addon install vlcluster --name vlc --listen 127.0.0.1
# 1 host, 3 containers (vlstorage + vlinsert + vlselect)
# --storage-node 自动推导为 127.0.0.1:<StoragePort>
```

### 形态 2 — 跨主机分片

与 VMCluster 相同的 `--roles` + `--storage-node` 模式：每台主机各跑自己的 `pg addon install vlcluster`，pgcli 从不 SSH。

```bash
# Host A — 存储节点 1
pg addon install vlcluster --name vlc --roles storage --listen 10.0.0.1

# Host B — 存储节点 2
pg addon install vlcluster --name vlc --roles storage --listen 10.0.0.2

# Host C — 摄入 + 查询层
pg addon install vlcluster --name vlc --roles insert,select --listen 10.0.0.3 \
  --storage-node 10.0.0.1:9432 \
  --storage-node 10.0.0.2:9432
```

仅运行 access 角色（`--roles insert` 和/或 `select`）的主机**必须**传 `--storage-node`，否则安装直接报错——自动推导的 loopback 地址会造出一个谁都不认识的孤立节点。

所有主机必须为同名实例分配相同的端口块，地址才能对上。默认基址是 9430（见 [端口](#端口)）；如果某台主机修改了 `vlcluster_start_port`，其他主机也要改成一样的值。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `vlc` | 实例名（map key） |
| `--roles` | `insert,select,storage` | 本机运行的角色，逗号分隔（跨主机时取子集） |
| `--storage-node` | 自动推导 | vlstorage 地址（可重复；格式 `host:storagePort`，如 `10.0.0.1:9432`）。列出集群中**所有** vlstorage 节点以分片。单机收敛时省略 |
| `--insert-port` | 自动（基址 9430） | vlinsert HTTP 端口 |
| `--select-port` | 自动（基址 9431） | vlselect HTTP 端口 |
| `--storage-port` | 自动（基址 9432） | vlstorage HTTP 端口 |
| `--listen` | `0.0.0.0` | 绑定地址——`127.0.0.1` 表示仅回环 |
| `--retention` | 二进制默认 | vlstorage 保留期，如 `30d`（仅在本机运行 storage 角色时生效） |
| `--image` | `docker.io/victoriametrics/victoria-logs:v1.53.0` | 镜像覆盖（三个角色共用一个镜像） |
| `--insert-extra-arg` | — | vlinsert 额外 CLI 参数（可重复，追加在最后） |
| `--select-extra-arg` | — | vlselect 额外 CLI 参数（可重复，追加在最后） |
| `--storage-extra-arg` | — | vlstorage 额外 CLI 参数（可重复，追加在最后） |
| `--data-dir` | 自动 | vlstorage 数据目录覆盖 |
| `--force` | false | 即使运行中且配置未变也重建 |

`--retention` 刻意不设默认值：留空表示使用 victoria-logs 二进制自己的默认保留期。与 vmcluster 的 `--retention-months` 不同，VictoriaLogs 解析的是 `30d`、`3months`、`1y` 这类时长。

## 安装

```bash
pg addon install vlcluster --name vlc --listen 127.0.0.1
```

命令会按需拉取镜像，按依赖顺序（storage → insert → select）创建容器，并打印摘要：

```
✓ VLCluster installed: "vlc"
  Roles:       storage, insert, select (converged on this host)
  Containers:  pgcli-vlcluster-default-vlc-storage, ...
  Image:       docker.io/victoriametrics/victoria-logs:v1.53.0
  Data:        /home/you/.pgcli/addon/vlcluster/vlc/data
  Retention:   binary default (--retention to change)
  Insert URL:  http://127.0.0.1:9430/ (vlinsert — ingestion)
  Select URL:  http://127.0.0.1:9431/ (vlselect — LogsQL queries)
  Storage URL: http://127.0.0.1:9432/ (vlstorage — internal APIs + admin)
```

用相同参数重装是幂等的（显示 `unchanged`）；参数变化时只重建需要的部分；从 `--roles` 中移除的角色，其残留容器会被删除。

## URL 格式

集群版与单机版 VictoriaLogs 使用**完全相同的 API 路径**——没有 account ID，也没有额外前缀：

**经 vlinsert 摄入**：
```bash
curl -X POST http://127.0.0.1:9430/insert/jsonline \
  -H 'Content-Type: application/json' \
  -d '{"_msg":"hello from vlcluster","app":"demo"}'
```

**经 vlselect 查询**：
```bash
curl 'http://127.0.0.1:9431/select/logsql/query?query=app:demo&limit=10'
```

vlselect 还在 `http://<host>:9431/select/vmui/` 提供内置 UI。vlagent 的转发目标为 `http://<vlinsert>:9430/insert/native`。

vlstorage 端口（9432）的 `/internal/insert` 与 `/internal/select/*` 供另外两个角色调用；它自己的 LogsQL 端点只查询**本节点本地数据**，适合单节点排障，但不是集群级查询。

## 端口

每个 VLCluster 实例从 `vlcluster_start_port` 池（默认 9430）占用 **3 个连续端口**：

- `+0` InsertPort（vlinsert HTTP——摄入端点）
- `+1` SelectPort（vlselect HTTP——查询端点）
- `+2` StoragePort（vlstorage HTTP——内部摄入/查询 API）

即使某台主机只运行部分角色，三个端口也都会分配，这样存储节点地址在各主机间可预测。基址紧挨单机版 VictoriaLogs（9428）和 vlagent（9429）；三个端口池互相感知，不会发放同一个端口。

## 配置

```yaml
addons:
  vlcluster:
    vlc:
      name: vlc
      container_prefix: pgcli-vlcluster-default-vlc
      image_tag: docker.io/victoriametrics/victoria-logs:v1.53.0
      listen: 0.0.0.0
      insert_port: 9430
      select_port: 9431
      storage_port: 9432
      roles:
        - storage
        - insert
        - select
      storage_nodes:
        - "10.0.0.1:9432"
        - "10.0.0.2:9432"
      retention: 30d
      autostart: false
```

容器内**没有配置文件**——一切都以 CLI 参数传入。`storage_nodes` 是跨主机拓扑；留空表示自动推导的单机模式。

## 增加存储节点

vlinsert/vlselect 在启动时固化 `-storageNode` 列表，因此增加节点需要重建 access 层容器（摄入会短暂中断，已有数据不受影响）：

```bash
# 新存储主机
pg addon install vlcluster --name vlc --roles storage --listen 10.0.0.3

# access 层主机：带完整节点列表重装
pg addon install vlcluster --name vlc --roles insert,select --listen 10.0.0.3 \
  --storage-node 10.0.0.1:9432 \
  --storage-node 10.0.0.2:9432 \
  --storage-node 10.0.0.3:9432
```

**没有自动 rebalance**：旧数据留在原节点，只有新写入会分布到扩容后的节点集合；vlselect 会合并所有节点，所以查询能看到新旧全部数据。如需重新均衡旧数据，可等 retention 自然过期，或手工搬运按天分区（上游没有 rebalance 工具）。

## 启停

```bash
pg addon stop vlcluster
pg addon start vlcluster
```

容器以 podman 的 `unless-stopped` 策略运行，主机重启后会自动恢复。停止顺序与依赖相反（select → insert → storage）。

### 开机自启

```bash
pg autostart enable --vlcluster --name vlc
```

启动是集群粒度的：每台主机只拉起自己的角色，按依赖顺序（storage → insert → select），已存在的容器直接 start，缺失的才重建。跨主机部署需在每台主机上执行。完整机制见 [Autostart]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove vlcluster              # 保留 vlstorage 数据——同名重装可复活已有日志
pg addon remove vlcluster --clean-data # 连同数据目录一起删除
```

移除会删除本机全部三个角色容器，包括角色变更后遗留的孤儿容器。在 access-only 主机上执行不会影响远端存储节点。

## 日志

```bash
pg logs addon vlcluster                # vlstorage 日志（默认 service）
pg logs addon vlcluster --service insert   # vlinsert 日志
pg logs addon vlcluster --service select   # vlselect 日志
```

本机未运行的角色会被明确拒绝。

## 已知限制

- **没有副本。** 每条 stream 只有一份；任一 vlstorage 宕机，查询返回 502。摄入会继续，可由上游（如 vlagent 磁盘队列）缓冲。跨集群高可用需要多个 vlagent `--remote-write-url` 目标加 vmauth 之类路由层——不在本 addon 范围内。
- **没有自动 rebalance。** 增加存储节点只影响新写入的数据。
- **没有鉴权。** HTTP API 可摄入和查询任意日志；`0.0.0.0` 绑定只能靠网络边界保护。
- **仅 Linux。** 需要 host networking。
- **没有 TLS。** 在前面用 nginx 终止 TLS，或在私有网络内使用。
- **状态是本机视角。** `pg addon list` 只报告当前主机配置的角色，不会探测远端存储节点。
- pgcli 不校验 `--*-extra-arg` 的取值，错误参数只会在容器启动日志中暴露。
