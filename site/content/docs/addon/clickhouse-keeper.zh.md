---
title: "ClickHouse Keeper"
description: "以独立顶层插件运行 ClickHouse Keeper quorum——ReplicatedMergeTree 与 ON CLUSTER DDL 背后的 ZooKeeper 兼容协调服务"
weight: 66
---

`pg addon install clickhouse-keeper` 以受管容器方式运行 **ClickHouse Keeper**，直接使用官方 `docker.io/clickhouse/clickhouse-keeper` 镜像。Keeper 是 ClickHouse [复制集群](./clickhouse-cluster/) 使用的 ZooKeeper 兼容协调服务：保存复制表元数据、复制日志与 `ON CLUSTER` DDL 任务队列。

Keeper 是**独立顶层插件**，绝不是 ClickHouse 容器内的一个进程：

- 它的配置、数据与日志目录独立于所有 ClickHouse 实例（pgcli 会拒绝路径重叠）。
- 它有独立生命周期：install、start、stop、remove、autostart 都以 `pg addon ... clickhouse-keeper` 寻址。
- `pg start` 会**先**启动启用 autostart 的 Keeper 成员，再启动 ClickHouse，避免 ClickHouse 在 Keeper quorum 仍不可用时启动。

本插件只在 ClickHouse 集群模式下有意义。单机 ClickHouse 不需要 Keeper。

> **正式环境把 Keeper 跑在专用服务器上。** 下文示例为节省测试机器而把 Keeper
> 与 ClickHouse 混部在 VM 上。正式环境应使用 3（或 5）台独立服务器，配独立
> 磁盘与故障域——见[生产注意事项](#生产注意事项)。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `clickhouse-keeper` | 实例名（map 键；也是各子命令的默认名） |
| `--version` | `26.8.36.3` | 上游镜像 tag，来自 `docker.io/clickhouse/clickhouse-keeper` |
| `--server-id` | —（首次安装必需） | 本成员的 1-based Raft server id；quorum 内唯一 |
| `--advertise-host` | —（首次安装必需） | Raft peer 访问本成员所用的具体非 loopback 地址 |
| `--peer ID@HOST[:RAFT_PORT]` | — | 另一个成员；可重复。每台主机只列**其他**成员；本机自动加入。默认 Raft 端口 `9234` |
| `--client-port` | `9181` | ZooKeeper 协议客户端端口（ClickHouse 连这里） |
| `--raft-port` | `9234` | Keeper 成员之间的 Raft peer 端口 |
| `--listen` | `127.0.0.1` | 绑定地址。多成员安装要求非 loopback 值（`0.0.0.0` 或 advertise 地址） |
| `--data-dir` | `<base_dir>/addon/clickhouse-keeper/<name>/data` | Raft 日志与快照存储；绝对路径 |
| `--log-dir` | `<base_dir>/addon/clickhouse-keeper/<name>/logs` | `clickhouse-keeper.log` 与 `.err.log`；绝对路径 |
| `--config-dir` | `<base_dir>/addon/clickhouse-keeper/<name>/config` | 写入 `keeper_config.xml` 的目录；绝对路径 |
| `--force` | false | 重写配置并重建容器，保留数据与身份 |
| `--clean-data` | false（`remove`） | 同时删除实例目录及自定义 data/log 目录 |

`--cluster` 会被拒绝：Keeper 成员关系完全由 `--server-id`、`--advertise-host` 与 `--peer` 表达。也不适用 `--image`——请用 `--version`。

## 安装三成员 quorum

在每台主机上安装一个成员。只有本机身份与省略的 peer 不同：

```bash
# host01
pg addon install clickhouse-keeper --name keeper \
  --server-id 1 --advertise-host 192.0.2.11 --listen 0.0.0.0 \
  --peer 2@192.0.2.12 --peer 3@192.0.2.13

# host02
pg addon install clickhouse-keeper --name keeper \
  --server-id 2 --advertise-host 192.0.2.12 --listen 0.0.0.0 \
  --peer 1@192.0.2.11 --peer 3@192.0.2.13

# host03
pg addon install clickhouse-keeper --name keeper \
  --server-id 3 --advertise-host 192.0.2.13 --listen 0.0.0.0 \
  --peer 1@192.0.2.11 --peer 2@192.0.2.12
```

每台主机渲染的配置都包含**完整**的 Raft 成员列表（两个 peer 加上由自身身份折叠进来的本机），按 server id 排序。安装顺序无关紧要：peer 尚未启动的成员会先启动、暂不监听客户端端口，并以**警告**而不是安装失败的形式提示无 quorum；继续安装其余成员，quorum 一旦形成该成员会自动加入。尚未达到 quorum 时，`pg addon clickhouse-keeper status` 会警告但不会报错。

首次安装必须显式提供 `--server-id` 与 `--advertise-host`——Keeper 无法安全地推导二者。多成员安装还要求非 loopback `--listen`，因为 peer 无法访问 `127.0.0.1`。少于三个成员的 quorum 仅供开发使用，会有警告；正式环境请用 **3 或 5** 个成员。

安装摘要：

```
✓ ClickHouse Keeper "keeper" installed
  Version:     26.8.36.3
  Image:       docker.io/clickhouse/clickhouse-keeper:26.8.36.3
  Container:   pgcli-clickhouse-keeper-keeper
  Server ID:   1
  Client:      0.0.0.0:9181
  Raft:        192.0.2.11:9234
  Members:     1@192.0.2.11:9234, 2@192.0.2.12:9234, 3@192.0.2.13:9234
  Data dir:    /home/you/.pgcli/addon/clickhouse-keeper/keeper/data
  Logs dir:    /home/you/.pgcli/addon/clickhouse-keeper/keeper/logs
  Config:      /home/you/.pgcli/addon/clickhouse-keeper/keeper/config/keeper_config.xml
```

## Quorum

Keeper 通过 Raft 复制状态。quorum 为 `floor(n/2)+1` 个成员：

| 成员数 | Quorum | 可容忍故障 |
|---|---|---|
| 1 | 1 | 0（仅开发） |
| 2 | 2 | 0——任一成员丢失即无 quorum |
| 3 | 2 | **1** |
| 5 | 3 | 2 |

三成员是能扛住一台主机故障的最小配置。偶数成员没有意义：四个成员仍需要三个才能形成 quorum。请用 3 或 5。

- **三成员中一个下线。** ClickHouse 仍可创建复制表、执行 `ON CLUSTER` DDL。
- **两个下线。** 无 quorum。复制停止，新建复制表与 DDL 失败，现有复制表在会话超时后转为只读。写入前先恢复成员。
- **整个 quorum 丢失。** ClickHouse 磁盘上的表数据不会删除，但复制与 DDL 状态无法自动重建。请把 Keeper 数据目录与 ClickHouse 数据一起备份。

与 ZooKeeper 一样，Keeper 客户端端口刻意不做认证。请把 `9181` 与 `9234` 限制在可信网络内。

## 状态

```bash
pg addon clickhouse-keeper status --name keeper
```

命令报告容器状态、渲染的成员列表与本地 `mntr` 探测：

```
clickhouse-keeper "keeper" — running (ruok ok)
  Version:    26.8.36.3
  Image:      docker.io/clickhouse/clickhouse-keeper:26.8.36.3
  Container:  pgcli-clickhouse-keeper-keeper
  Server ID:  1
  Client:     0.0.0.0:9181
  Raft:       192.0.2.11:9234
  Members:    1@192.0.2.11:9234, 2@192.0.2.12:9234, 3@192.0.2.13:9234
  Local state: leader
  Reachability:
    server 1  127.0.0.1:9181  reachable (leader)
    server 2  192.0.2.12:9181  reachable
    server 3  192.0.2.13:9181  reachable
  [OK] 3/3 client endpoints reachable (quorum 2; client reachability is not a Raft quorum proof)
```

本机成员通过 loopback 探测，因此显示 `127.0.0.1`；远端成员使用其 advertise 地址。只有本机成员的 Raft 角色可知（来自 `mntr`），所以远端成员不显示角色后缀。可达性探测用 `ruok` 四字命令访问每个成员的**客户端**端口；本机另外用 `mntr` 获得 `zk_server_state`（`leader` / `follower` / `standalone`）。客户端端口可达并不等于 quorum 正常——输出会明确说明这一点。请结合 `mntr` 状态与可达数量判断健康度。

## pgcli 创建的内容

单个实例的全部状态位于 `<base_dir>/addon/clickhouse-keeper/<name>/`：

| 宿主路径 | 容器路径 | 说明 |
|---|---|---|
| `config/keeper_config.xml` | `/etc/pgcli-clickhouse-keeper/keeper_config.xml` | 只读的受管配置 |
| `data/` | `/var/lib/clickhouse-keeper` | Raft 日志在 `coordination/log`，快照在 `coordination/snapshots` |
| `logs/` | `/var/log/clickhouse-keeper` | `clickhouse-keeper.log` / `.err.log`，按 100 MB × 3 轮转 |

三个路径都可独立覆盖（`--config-dir` 控制存放 `keeper_config.xml` 的目录）。pgcli 在安装时校验 Keeper 的 config/data/log 路径彼此不重叠，也不与任何 ClickHouse 的 config/data/log/backup/secret 路径重叠。

生成的 XML 设置：

```xml
<?xml version="1.0"?>
<clickhouse>
    <logger>
        <level>information</level>
        <log>/var/log/clickhouse-keeper/clickhouse-keeper.log</log>
        <errorlog>/var/log/clickhouse-keeper/clickhouse-keeper.err.log</errorlog>
        <size>100M</size>
        <count>3</count>
        <console>1</console>
    </logger>
    <listen_host>0.0.0.0</listen_host>
    <keeper_server>
        <tcp_port>9181</tcp_port>
        <server_id>1</server_id>
        <log_storage_path>/var/lib/clickhouse-keeper/coordination/log</log_storage_path>
        <snapshot_storage_path>/var/lib/clickhouse-keeper/coordination/snapshots</snapshot_storage_path>
        <coordination_settings>
            <operation_timeout_ms>10000</operation_timeout_ms>
            <session_timeout_ms>30000</session_timeout_ms>
            <raft_logs_level>information</raft_logs_level>
        </coordination_settings>
        <raft_configuration>
            <server><id>1</id><hostname>192.0.2.11</hostname><port>9234</port></server>
            <server><id>2</id><hostname>192.0.2.12</hostname><port>9234</port></server>
            <server><id>3</id><hostname>192.0.2.13</hostname><port>9234</port></server>
        </raft_configuration>
    </keeper_server>
</clickhouse>
```

容器以 `--network host`、`--restart unless-stopped`、30 秒停止超时和 `nofile=262144` 运行。官方 entrypoint 在降权前会 chown 数据与日志挂载。没有 secret：Keeper 配置不含凭据。

## 生命周期

```bash
pg addon start clickhouse-keeper --name keeper      # 不存在则创建；短暂等待本地 ruok，quorum 未达只警告
pg addon stop clickhouse-keeper --name keeper
pg addon clickhouse-keeper status --name keeper
pg addon clickhouse-keeper shell --name keeper      # 进入容器内 bash
pg addon clickhouse-keeper config show --name keeper
pg logs addon clickhouse-keeper --name keeper -f
pg addon remove clickhouse-keeper --name keeper     # 移除容器与 pg.yaml 条目，保留数据
pg addon remove clickhouse-keeper --name keeper --clean-data
```

`remove` 会打印 quorum 警告，并提醒剩余成员的 `raft_configuration` 仍引用被移除的 server id。v1 没有在线成员变更：替换成员意味着用更新后的 peer 列表重装受影响的成员。`--clean-data` 只删除 Keeper 实例目录及位于其外的自定义 `--data-dir`/`--log-dir`；绝不动 ClickHouse 数据。

`config show` 打印渲染后的 XML；v1 的 Keeper 没有 `config set`，也没有 password 子命令。

Autostart：

```bash
pg autostart enable --clickhouse-keeper --name keeper
pg autostart status
pg autostart disable --clickhouse-keeper --name keeper
```

`pg start` 先启动启用 autostart 的 Keeper 成员，再启动 ClickHouse 成员，最后 nginx。

## 生产注意事项

- **专用服务器。** 在 3（或 5）台不运行 ClickHouse 的服务器上运行 quorum。磁盘与故障域比 CPU 更重要：Keeper quorum 一失，整个 ClickHouse 集群转只读，混部会把无关故障耦合在一起。
- **仅在可信网络。** 无认证、无 TLS。任何能访问 `9181` 的客户端都能读写协调状态。
- **备份数据目录。** Keeper 数据量小但变更频繁；对任一健康成员的 `data/` 做文件系统快照即可用于恢复，但在 quorum 停止或逐成员停止时备份一致性更好。
- **每台主机一个 Keeper。** v1 强制每台主机一个 Keeper 成员。
- **静态成员关系。** 无在线增删/替换；请提前规划 quorum 规模。
- **资源占用。** Keeper 很轻（几百 MiB），但在 2 GiB 测试 VM 上并非免费；请给 ClickHouse 的 `max_server_memory_usage` 留出余量。

## 限制

- 无认证、无 TLS。
- 无在线成员变更；`--server-id`、`--advertise-host`、`--peer`、`--client-port`、`--raft-port` 安装后不可变，即使带 `--force`。
- 无 `config set`；只能通过完整重装修改生成的 XML。
- 仅 IP 地址（不支持 DNS 名）。
- 每台主机一个 Keeper。
- 每个配置条目管理一个容器；多成员部署即每台主机一个配置条目。

## CLI 参数速查

### `pg addon install clickhouse-keeper`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `clickhouse-keeper` | 实例名；`[A-Za-z0-9][A-Za-z0-9_-]*`，最长 48 字符 |
| `--version TAG` | `26.8.36.3` | `docker.io/clickhouse/clickhouse-keeper` 的上游镜像 tag |
| `--server-id N` | 首次安装必需 | 1-based Raft server id，quorum 内唯一 |
| `--advertise-host IP` | 首次安装必需 | peer 拨号所用的具体非 loopback 地址 |
| `--peer ID@HOST[:RAFT_PORT]` | — | 可重复；每台主机列出所有其他成员 |
| `--client-port N` | `9181` | ZooKeeper 协议客户端端口 |
| `--raft-port N` | `9234` | Raft peer 端口 |
| `--listen ADDR` | `127.0.0.1` | 绑定地址；有 peers 时必须非 loopback |
| `--data-dir DIR` | `<instance>/data` | 绝对路径；挂载到 `/var/lib/clickhouse-keeper` |
| `--log-dir DIR` | `<instance>/logs` | 绝对路径；挂载到 `/var/log/clickhouse-keeper` |
| `--config-dir DIR` | `<instance>/config` | 绝对路径；写入 `keeper_config.xml` |
| `--force` | false | 重写配置并重建容器；身份 flag 仍不可变 |

不带 `--force` 重跑 `install` 会把每个显式 flag 与已存值比对，发现漂移就报错而不应用。

### `pg addon clickhouse-keeper status` / `shell` / `config show`

| 参数/flag | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `clickhouse-keeper` | 目标实例 |

### 生命周期、日志、autostart

| 命令 | 参数 | 说明 |
|---|---|---|
| `pg addon start clickhouse-keeper` | `--name NAME` | 启动（不存在则创建）；短暂等待本地 `ruok`，quorum 未达时只警告 |
| `pg addon stop clickhouse-keeper` | `--name NAME` | 停止容器，保留数据 |
| `pg addon remove clickhouse-keeper` | `--name NAME`、`--clean-data` | 移除容器与配置条目；`--clean-data` 删除 Keeper 实例/data/log 路径 |
| `pg logs addon clickhouse-keeper` | `--name NAME`、`-n N`（默认 50）、`-f` | 容器日志 |
| `pg autostart enable\|disable` | `--clickhouse-keeper`、`--name NAME` | 切换单个成员的开机启动 |

### 全局参数

| 参数 | 说明 |
|---|---|
| `-c, --config FILE` | 配置文件（默认 `~/.pgcli/pg.yaml`） |
| `-o, --output FILE` | 输出配置路径（默认同一文件） |
| `-i, --instance NAME` | PostgreSQL 实例选择器——Keeper 不使用；请改用 `--name` |

## 验证记录

2026-11-03 使用 `clickhouse/clickhouse-keeper:26.8.36.3` 验证：`test/addon/test_clickhouse_cluster.sh` 中的 Keeper 断言在 3 成员 quorum 上全部通过，覆盖 leader/follower 状态、单成员停止时 2/3 quorum 仍可用、以及降级期间 `ON CLUSTER` DDL 可用。
