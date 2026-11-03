---
title: "ClickHouse 集群"
description: "跨主机运行复制的 ClickHouse 集群——2 shard × 2 replica、ReplicatedMergeTree、Distributed 表与 ON CLUSTER DDL，由独立 ClickHouse Keeper 插件协调"
weight: 65
---

`pg addon install clickhouse --cluster ...` 把 [ClickHouse 插件](./clickhouse/) 变成**跨主机静态集群中的一个成员**。每台主机各自运行 ClickHouse 容器、各自维护 `pg.yaml`；pgcli 不会通过 SSH 远程操作，也不会替你创建集群成员。你需要在每个成员上安装同一套集群配置，并显式传入共享拓扑。

复制协调由 **[ClickHouse Keeper](./clickhouse-keeper/)** 提供——它是独立顶层插件，必须**先于** ClickHouse 成员安装并处于健康状态。Keeper 不会被嵌进 ClickHouse 容器。

> **正式环境使用 3 台专用 Keeper 服务器。** 下文四台 VM 的布局是为节省机器
> 而把 Keeper 与 ClickHouse 混部的测试拓扑。正式环境应在自己的 3（或 5）台
> 服务器上运行 Keeper quorum，并使用独立磁盘与故障域——见[生产注意事项](#生产注意事项)。

## 拓扑

集群由名称加一组 `shard` / `replica` 位置定义。本文使用以下参考测试布局：

| 主机 | IP | Keeper | ClickHouse |
|---|---|---|---|
| host01 | `192.0.2.11` | server id 1 | shard 1 / replica 1 |
| host02 | `192.0.2.12` | server id 2 | shard 1 / replica 2 |
| host03 | `192.0.2.13` | server id 3 | shard 2 / replica 1 |
| host04 | `192.0.2.14` | — | shard 2 / replica 2 |

请替换成你自己的地址；下文 `--peer` 列表即由该表推导。

### Shard 与 replica

- **Shard** 是水平分区：不同 shard 存放不同的行。
- **Replica** 是 shard 的完整副本，通过 Keeper 保持同步。同一 shard 内的每个副本都可读写；复制是异步的。
- **2 shard × 2 replica** 下，每行数据存在两份，每个 shard 允许一台 ClickHouse 主机故障而不丢数据、不丢写能力。
- v1 要求所有 shard 的 **replica 数量一致**、编号为无空洞的 `1..N`，且每个成员都必须在所有主机上列出。成员关系是静态的：没有在线增删节点命令。

v1 只支持具体 IPv4/IPv6 地址（不支持 DNS 名）、每台主机一个 ClickHouse 实例、每台主机一个 Keeper。

## 开始之前

1. **一个可用的 Keeper quorum。** 按 [ClickHouse Keeper](./clickhouse-keeper/) 安装三个成员。`pg addon clickhouse-keeper status` 应显示 3/3 客户端端点可达。
2. **所有成员使用同一个 admin 密码。** 每个 ClickHouse 成员必须使用完全相同的 `--admin-password`；pgcli 无法跨主机读取或同步它。因此全新集群安装**强制显式提供该 flag**——集群模式禁用自动生成密码的路径。
3. **端口空闲。** HTTP `8123`、原生 TCP `9000`、interserver `9009`；Keeper 使用客户端 `9181` 与 Raft `9234`。
4. **每台主机都有 pgcli 二进制**与各自的配置文件（默认 `~/.pgcli/pg.yaml`）。所有主机使用相同的 `--name`；namespace 仍可用于隔离同一机器上的无关部署。

## 安装

参考 Keeper 命令（细节见 [Keeper 页面](./clickhouse-keeper/)）：

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

然后在每台主机上各安装一个 ClickHouse 成员。host01 的完整命令：

```bash
pg addon install clickhouse --name clickhouse \
  --cluster chcluster \
  --shard 1 --replica 1 \
  --advertise-host 192.0.2.11 --listen 0.0.0.0 \
  --admin-password "$CH_ADMIN_PASSWORD" \
  --peer 1/2@192.0.2.12:9000 \
  --peer 2/1@192.0.2.13:9000 \
  --peer 2/2@192.0.2.14:9000 \
  --keeper 192.0.2.11:9181 \
  --keeper 192.0.2.12:9181 \
  --keeper 192.0.2.13:9181 \
  --interserver-port 9009
```

其余三个成员只有本机身份与省略的 peer 不同：

| 主机 | `--shard` | `--replica` | `--advertise-host` | `--peer` 条目（另外三个成员） |
|---|---|---|---|---|
| host01 | 1 | 1 | `192.0.2.11` | `1/2@192.0.2.12:9000`、`2/1@192.0.2.13:9000`、`2/2@192.0.2.14:9000` |
| host02 | 1 | 2 | `192.0.2.12` | `1/1@192.0.2.11:9000`、`2/1@192.0.2.13:9000`、`2/2@192.0.2.14:9000` |
| host03 | 2 | 1 | `192.0.2.13` | `1/1@192.0.2.11:9000`、`1/2@192.0.2.12:9000`、`2/2@192.0.2.14:9000` |
| host04 | 2 | 2 | `192.0.2.14` | `1/1@192.0.2.11:9000`、`1/2@192.0.2.12:9000`、`2/1@192.0.2.13:9000` |

每台主机必须一致的部分是 `--cluster`、三个 `--keeper` 端点、`--interserver-port` 与 `--admin-password`。

`--peer` **只列其他成员**——本机由 `--shard`、`--replica`、`--advertise-host` 及其 TCP 端口自动折叠进来。相反，`--keeper` 在每台主机上都要给出**完整**的 Keeper 客户端端点列表（ClickHouse 的 Keeper 列表没有自重折叠）。Keeper 客户端默认端口是 `9181`，因此 `--keeper 192.0.2.11` 也可接受；非默认端口请显式写出。

全新集群安装必须显式提供 `--cluster`、`--shard`、`--replica`、`--advertise-host`、至少一个 `--peer`、至少一个 `--keeper`、`--listen`（非 loopback，`0.0.0.0` 或本机 advertise 地址）以及 `--admin-password`。缺项会以错误形式列出所有缺失 flag。

## 集群模式渲染的内容

常规服务端配置仍在 `config.d/zz-pgcli.xml` 中。集群模式额外生成一个受管文件 `config.d/zz-pgcli-cluster.xml`：

```xml
<?xml version="1.0"?>
<clickhouse>
    <remote_servers>
        <chcluster>
            <shard>
                <internal_replication>true</internal_replication>
                <replica>
                    <host>192.0.2.11</host>
                    <port>9000</port>
                    <user>admin</user>
                    <password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>
                </replica>
                <replica>
                    <host>192.0.2.12</host>
                    <port>9000</port>
                    <user>admin</user>
                    <password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>
                </replica>
            </shard>
            <shard>
                <internal_replication>true</internal_replication>
                <replica>
                    <host>192.0.2.13</host>
                    <port>9000</port>
                    <user>admin</user>
                    <password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>
                </replica>
                <replica>
                    <host>192.0.2.14</host>
                    <port>9000</port>
                    <user>admin</user>
                    <password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>
                </replica>
            </shard>
        </chcluster>
    </remote_servers>
    <macros>
        <cluster>chcluster</cluster>
        <shard>1</shard>
        <replica>1</replica>
    </macros>
    <zookeeper>
        <node>
            <host>192.0.2.11</host>
            <port>9181</port>
        </node>
        <node>
            <host>192.0.2.12</host>
            <port>9181</port>
        </node>
        <node>
            <host>192.0.2.13</host>
            <port>9181</port>
        </node>
        <session_timeout_ms>30000</session_timeout_ms>
        <operation_timeout_ms>10000</operation_timeout_ms>
    </zookeeper>
    <distributed_ddl>
        <path>/clickhouse/chcluster/task_queue/ddl</path>
    </distributed_ddl>
    <interserver_http_host>192.0.2.11</interserver_http_host>
    <interserver_http_port>9009</interserver_http_port>
    <default_replica_path>/clickhouse/chcluster/tables/{uuid}/{shard}</default_replica_path>
    <default_replica_name>{replica}</default_replica_name>
</clickhouse>
```

- `remote_servers` 以集群名声明完整拓扑；`internal_replication=true` 让写 replicated 表时通过 Keeper 复制，而不是由 Distributed 引擎自己向每个副本写。每个 replica 条目还带上 admin 用户名，使跨 shard 的 `Distributed` 查询能在远端成员上完成认证——镜像的 entrypoint 在配置了专用 admin 用户后会删除无密码的 `default` 用户。密码以 `from_env` 引用；容器入口包装脚本在启动时从挂载的 secret 文件导出该环境变量，因此 0644 的集群文件里只有变量名，绝不含明文密码。
- `macros` 在本节点填充 `ReplicatedMergeTree` 路径中的 `{shard}` 与 `{replica}`。
- `zookeeper` 指向 Keeper 客户端端点（Keeper 使用 ZooKeeper 协议）。
- `distributed_ddl` 把 `ON CLUSTER` DDL 任务放在集群专属路径下。
- `interserver_http_host` / `_port` 是副本之间追平数据的端点。
- `default_replica_path` / `default_replica_name` 让不带显式参数的 `ReplicatedMergeTree` 能从 macros 推导 Keeper 路径。

集群文件不包含监听、日志或备份设置——这些仍在基础文件中；唯一的用户条目是上面的 `from_env` 凭据引用。集群模式下，`config set` 会拒绝与受管拓扑冲突的键（`remote_servers`、`macros`、`zookeeper`、`distributed_ddl`、`default_replica_path`、`default_replica_name`）。单机实例仍可自行配置外部 `zookeeper`/`macros` 块。

如果实例被重装为单机（例如 `remove` 未加 `--clean-data` 后重装），集群文件会被自动删除，避免陈旧的 `remote_servers` 块泄漏进单机模式。

## 验证

```bash
pg addon clickhouse cluster status --name clickhouse
```

该命令在本地成员上查询 `system.clusters` 与 `system.replicas`，把拓扑与 `pg.yaml` 比对，并探测每个 Keeper 客户端端点：

```
clickhouse "clickhouse" — cluster "chcluster", this node shard 1 / replica 1
  Cluster config: /home/you/.pgcli/addon/clickhouse/clickhouse/config.d/zz-pgcli-cluster.xml
  system.clusters: 4 member(s), expected 4
    shard 1 replica 1  192.0.2.11:9000  local
    shard 1 replica 2  192.0.2.12:9000
    shard 2 replica 1  192.0.2.13:9000
    shard 2 replica 2  192.0.2.14:9000
  system.replicas: 1 replicated table(s)
    demo.events replica 1 — ok, delay 0s, queue 0
  [OK] Keeper client endpoints reachable: 3/3 (quorum 2; reachability is not a Raft quorum proof)
```

当 `system.clusters` 与存储的拓扑不一致，或某个副本只读时，命令以非零码退出。Keeper 可达性是单独报告的：TCP 能连上客户端端口**并不**证明 Raft quorum 正常。

等价的直接 SQL：

```bash
pg addon clickhouse query --name clickhouse \
  "SELECT cluster, shard_num, replica_num, host_name, port, is_local FROM system.clusters WHERE cluster = 'chcluster'"

pg addon clickhouse query --name clickhouse \
  "SELECT database, table, replica_name, is_readonly, absolute_delay, queue_size FROM system.replicas"

pg addon clickhouse query --name clickhouse \
  "SELECT count() FROM system.zookeeper WHERE path = '/clickhouse/chcluster'"
```

## 创建复制表

用 `ON CLUSTER` **一次**创建数据库与表；ClickHouse 会把语句展开到每个成员。由于渲染了 `default_replica_path` / `default_replica_name`，`ReplicatedMergeTree` 无需显式路径参数：

```sql
CREATE DATABASE IF NOT EXISTS demo ON CLUSTER chcluster;

CREATE TABLE demo.events ON CLUSTER chcluster
(
    id   UInt64,
    ts   DateTime,
    name String
)
ENGINE = ReplicatedMergeTree
ORDER BY id;

CREATE TABLE demo.events_dist ON CLUSTER chcluster
AS demo.events
ENGINE = Distributed(chcluster, demo, events, rand());
```

请通过 `pg addon clickhouse query` 逐条执行（未启用多语句模式），或用 `shell` 里的 `clickhouse-client --multiquery` 管道传入文件。

本地表（`demo.events`）与分布式表（`demo.events_dist`）职责不同：

- `demo.events` 持有本 shard 的行，并把它们复制到同 shard 的兄弟副本。
- `demo.events_dist` 是路由视图：把写扇出到各 shard，并把**所有** shard 的读结果合并。它本身不存数据。

本地插入并在兄弟副本上验证复制：

```bash
# 在 host01：写入 shard 1
pg addon clickhouse query --name clickhouse \
  "INSERT INTO demo.events VALUES (1, now(), 'host01')"

# 在 host02：同 shard 的兄弟副本能看到
pg addon clickhouse query --name clickhouse \
  "SELECT id, name FROM demo.events ORDER BY id"

# 经 Distributed 表读取两个 shard（可在任意成员执行）
pg addon clickhouse query --name clickhouse \
  "SELECT id, name FROM demo.events_dist ORDER BY id"
```

如果希望显式指定路径（例如让每张表有独立的 Keeper 布局），自行传入路径并保留 macros 占位符：

```sql
ENGINE = ReplicatedMergeTree('/clickhouse/chcluster/tables/{shard}/events', '{replica}')
```

`{shard}` 与 `{replica}` 来自渲染的 `macros`，因此表达式在每个成员上展开为不同值。

## 故障行为

- **一个 ClickHouse 副本停止。** 同 shard 的兄弟副本继续提供读写。它恢复后通过 Keeper 与 interserver 端点追平；观察 `system.replicas.queue_size` 直到排空。
- **某 shard 的两个副本都停止。** 该 shard 的数据不可用，但其他 shard 仍工作。Distributed 读会按引擎设置返回部分结果或报错；不要把此时视为集群可用。
- **三个 Keeper 中的一个停止。** 仍有 2/3 quorum；`ON CLUSTER` DDL 与复制继续。
- **三个 Keeper 中的两个停止。** 没有 quorum。新建复制表与 DDL 失败；现有复制表在 ClickHouse 到 Keeper 的会话过期后（渲染配置为 30 秒）转为只读。把 Keeper quorum 丧失视为故障，先恢复成员再写入。

Keeper 数据量小但关键：它保存复制日志与表元数据。整个 Keeper quorum 丢失**不会**丢失磁盘上的表数据，但复制状态无法自动重建。

## 运维

- **身份不可变。** 安装后，`--force` 会拒绝修改 `--cluster`、`--shard`、`--replica`、`--advertise-host`、`--peer`、`--keeper`、`--interserver-port`，以及集群模式下的 `--tcp-port`。其他所有成员的配置里都写死了本节点，修改一个节点的身份需要协同重建。
- **扩容是手动的。** 增加成员需要滚动重建，见[水平扩容](#水平扩容)。v1 不自动 rebalance 或拆分 shard。
- **`backup` / `restore` 是单机操作。** 它们只操作本地实例的本地表。备份一个副本不等于备份其他 shard。把复制表恢复到集群时需要考虑其 Keeper 路径。
- **`config set` 只影响本地成员。** 集群级修改必须在每台主机重复；CLI 不分发配置。
- **`user` / `grant` 默认也是单节点操作**，除非用 `ON CLUSTER` 下发。CLI 创建的用户与授权只作用于本地成员。
- **Autostart 顺序。** `pg start` 先启动启用 autostart 的 Keeper 成员，再启动 ClickHouse 成员，最后 nginx。分别用 `pg autostart enable --clickhouse-keeper --name keeper` 与 `pg autostart enable --clickhouse --name clickhouse` 开启。

## 水平扩容

v1 的成员关系是静态的：没有在线加节点命令。扩容 = 先安装新成员，再以滚动方式重写每个既有成员的 `remote_servers`。

- 每个 shard 的 replica 数必须一致，且编号从 `1` 连续。加一个 replica 就要给**每个** shard 都加。
- `--peer` 属于不可变身份，既有成员无法用 `--force` 原地修改。必须 `pg addon remove clickhouse --name clickhouse`（**不要**加 `--clean-data`）后再以同一 `--name` 和扩展后的 peer 列表重装。默认数据目录会被沿用，复制表会重新接回原有 Keeper 路径；若当初用了自定义 `--data-dir`，重装时必须继续传入同一路径。
- `--cluster`、`--admin-password`、`--interserver-port`、`--tcp-port` 保持不变；Keeper 成员关系不需要调整（除非另行扩容 Keeper）。
- 一次只动一个成员；同一 shard 的两个 replica 不能同时下线；等 `system.replicas` 显示 `is_readonly=0` 且队列清空后再继续；滚动期间暂停 `ON CLUSTER` DDL。

### 增加 replica

给每个 shard 各加一个 replica。2 shard × 2 replica → 2 × 3 需要新增两台主机：

| 主机 | `--shard` | `--replica` | `--advertise-host` |
|---|---|---|---|
| host05 | 1 | 3 | `192.0.2.15` |
| host06 | 2 | 3 | `192.0.2.16` |

每台新成员都用完整的六成员 peer 列表安装——只列其他成员，绝不包含自己：

```bash
# host05 — shard 1 / replica 3
pg addon install clickhouse --name clickhouse \
  --cluster chcluster --shard 1 --replica 3 \
  --advertise-host 192.0.2.15 --listen 0.0.0.0 \
  --admin-password "$CH_ADMIN_PASSWORD" \
  --peer 1/1@192.0.2.11:9000 --peer 1/2@192.0.2.12:9000 \
  --peer 2/1@192.0.2.13:9000 --peer 2/2@192.0.2.14:9000 \
  --peer 2/3@192.0.2.16:9000 \
  --keeper 192.0.2.11:9181 --keeper 192.0.2.12:9181 \
  --keeper 192.0.2.13:9181 \
  --interserver-port 9009
```

host06 使用 `--shard 2 --replica 3 --advertise-host 192.0.2.16`，peer 列表对应调整。

然后滚动改造四台既有成员。每台先 remove（不加 `--clean-data`），再以原身份和扩展后的 peer 列表重装：

```bash
# 在 host01；host02–host04 用各自身份重复
pg addon remove clickhouse --name clickhouse     # 保留 data、config 与 logs
pg addon install clickhouse --name clickhouse \
  --cluster chcluster --shard 1 --replica 1 \
  --advertise-host 192.0.2.11 --listen 0.0.0.0 \
  --admin-password "$CH_ADMIN_PASSWORD" \
  --peer 1/2@192.0.2.12:9000 --peer 2/1@192.0.2.13:9000 \
  --peer 2/2@192.0.2.14:9000 \
  --peer 1/3@192.0.2.15:9000 --peer 2/3@192.0.2.16:9000 \
  --keeper 192.0.2.11:9181 --keeper 192.0.2.12:9181 \
  --keeper 192.0.2.13:9181 \
  --interserver-port 9009
```

新 replica 会自动从同 shard 的兄弟副本拉取历史 parts，无需手工搬运。用 `pg addon clickhouse cluster status --name clickhouse` 验证：`system.clusters` 应为六成员（2 shard × 3 replica），`system.replicas` 健康。

### 增加 shard

新 shard 初始为空，且 replica 数必须与既有 shard 一致。2 × 2 → 3 × 2 需要新增两台主机：

| 主机 | `--shard` | `--replica` | `--advertise-host` |
|---|---|---|---|
| host05 | 3 | 1 | `192.0.2.15` |
| host06 | 3 | 2 | `192.0.2.16` |

安装方式相同；host05 的命令与上面的 replica 示例只差身份与 peer 列表：

```bash
# host05 — shard 3 / replica 1
pg addon install clickhouse --name clickhouse \
  --cluster chcluster --shard 3 --replica 1 \
  --advertise-host 192.0.2.15 --listen 0.0.0.0 \
  --admin-password "$CH_ADMIN_PASSWORD" \
  --peer 1/1@192.0.2.11:9000 --peer 1/2@192.0.2.12:9000 \
  --peer 2/1@192.0.2.13:9000 --peer 2/2@192.0.2.14:9000 \
  --peer 3/2@192.0.2.16:9000 \
  --keeper 192.0.2.11:9181 --keeper 192.0.2.12:9181 \
  --keeper 192.0.2.13:9181 \
  --interserver-port 9009
```

host06 使用 `--shard 3 --replica 2 --advertise-host 192.0.2.16`。随后按上面的流程滚动 host01–host04，把两个新 peer 换成 `3/1@192.0.2.15:9000` 和 `3/2@192.0.2.16:9000`。

历史数据**不会**重新分布：Distributed 表只把新写入路由到新 shard。若必须迁移存量数据，需要另行 reshard（例如 `clickhouse-copier`，或新建表并用 `INSERT … SELECT` / 双写迁移）；pgcli v1 不自动完成。验证 `system.clusters` 为六成员（3 shard × 2 replica）。

## 生产注意事项

- **Keeper 跑在专用服务器上。** 本文的混部布局只是为了塞进四台 VM 的测试环境。正式环境应使用 **3（或 5）台独立 Keeper 服务器**，配独立磁盘、电源与故障域。Keeper 与 ClickHouse 会争抢同一份内存和 I/O，混部会把 ClickHouse 故障升级为 quorum 风险。
- **仅在可信网络。** Keeper 无认证、无 TLS；ClickHouse interserver 流量同样是明文。不要把 Keeper 端口（`9181`、`9234`）或 interserver 端口暴露到可信网络之外。
- **无 TLS。** HTTP 与原生监听都是明文。远程客户端需要 TLS 时，用 [nginx 插件](./nginx/) 前置 HTTP 端口；绝不要把原生或 interserver 端口暴露给不可信网络。
- **异步复制。** 一个副本确认的写入尚未落到兄弟副本。不要把一次成功插入当作已在两个节点持久化；用 `system.replicas` 看健康度，测试需要等待追平时用 `SYSTEM SYNC REPLICA`。
- **共享 admin 密码。** 每个成员必须使用相同的 `--admin-password`；pgcli 无法跨主机强制校验。不一致表现为客户端访问结果不一致，而不是安装报错。
- **内存。** 小内存 VM 需要在每个 ClickHouse 成员上显式设置 `max_server_memory_usage`，Keeper 也需要余量。2 GiB 测试 VM 上每个 ClickHouse 容器使用几百 MiB；见 [内存限制](./clickhouse/#内存限制)。
- **升级。** 一次升级一个副本（停止、`install --force --version <新版本>`、启动），等复制追平后再处理兄弟副本。绝不要让旧版本服务端运行新版本写过的磁盘格式。

## 限制

- 静态成员关系：无在线增删、无自动 rebalance、无 shard 拆分。
- 仅支持均匀拓扑：所有 shard 的 replica 数必须一致。
- 仅具体 IP 地址；不支持 DNS 名。
- 每台主机一个 ClickHouse 实例、一个 Keeper。
- 无跨主机编排：每条命令只操作本机的 `pg.yaml`。
- 无 TLS、无 Keeper 认证、不保证混合版本兼容。
- 复制是异步的。

## CLI 参数速查

共享的 ClickHouse flag（`--name`、`--version`、`--data-dir`、端口、用户、配置、备份、生命周期）见 [ClickHouse 页面](./clickhouse/#cli-参数速查)。以下是集群专属部分：

### `pg addon install clickhouse --cluster`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--cluster NAME` | —（必需） | 集群名；同时作为 `remote_servers` 元素名、macros 条目与 DDL 路径组成部分。`[A-Za-z_][A-Za-z0-9_-]*`，最长 48 字符 |
| `--shard N` | —（必需） | 本成员的 1-based shard 编号 |
| `--replica N` | —（必需） | 本成员在 shard 内的 1-based replica 编号 |
| `--advertise-host IP` | —（必需） | peer 与客户端访问本成员所用的具体非 loopback 地址 |
| `--peer S/R@HOST:PORT` | —（必需，可重复） | 另一个成员，格式 `shard/replica@host:tcp_port`。每台主机只列**其他**成员；本机自动加入 |
| `--keeper HOST[:PORT]` | —（必需，可重复） | Keeper 客户端端点。每台主机都要传**完整**共享列表；默认端口 `9181` |
| `--interserver-port N` | `9009` | 副本追平端口；集群内必须一致，安装后不可变 |
| `--listen ADDR` | 集群模式必需 | 必须是 `0.0.0.0` 或本节点的 `--advertise-host`；拒绝 loopback |
| `--admin-password PW` | 集群模式必需 | 所有成员必须一致；禁用自动生成密码 |
| `--tcp-port N` | 自动（基址 `9000`） | 原生端口；会被其他成员的 `--peer` 引用，安装后不可变 |

集群身份 flag 即使带 `--force` 也不可变：`--cluster`、`--shard`、`--replica`、`--advertise-host`、`--peer`、`--keeper`、`--interserver-port`，以及集群模式下的 `--tcp-port`。不带 `--force` 重跑 `install` 会把每个显式 flag 与已存值比对，发现漂移就报错，而不是静默应用。

### `pg addon clickhouse cluster status`

| 参数/flag | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `clickhouse` | 要检查的集群成员；必须正在运行 |

单机实例、`system.clusters` 与存储拓扑不一致、或某个复制表只读时，命令失败。Keeper 可达性只是参考信息。

## 验证记录

2026-11-03 使用 `clickhouse/clickhouse-server:26.8.36.3` 在与上表一致的 4 主机拓扑上验证：`test/addon/test_clickhouse_cluster.sh` 的 103 项断言全部通过，覆盖复制写入、跨 shard `Distributed` 读写、Keeper 单成员故障、ClickHouse 副本故障与追平、身份不可变及路径隔离。
