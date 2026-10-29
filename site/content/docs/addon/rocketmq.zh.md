---
title: "RocketMQ"
description: "以托管插件方式运行 Apache RocketMQ 5.x——三容器（NameServer + broker + proxy），支持单机或跨主机 controller 模式集群与 broker 自动主备切换，含 topic/消息命令、渲染配置与 host 网络"
weight: 67
---

`pg addon install rocketmq` 以托管插件方式运行 **Apache RocketMQ 5.x**，使用官方
`docker.io/apache/rocketmq` 镜像。和 Kafka、ClickHouse、RabbitMQ 一样，它是**顶层插件**，
不代理某个 PostgreSQL 实例。

每个实例都是 **host 网络上的三个容器**：**NameServer**、**broker** 和 **proxy**。单机与
集群使用相同的形态，因此端口、命令、日志和生命周期行为保持一致：

- **单机**——一个不带 controller 的 NameServer、一个无副本的 async master broker，以及
  proxy。这是最快能收发消息的模式。**不使用** 4.x 的 master/slave 或 DLedger broker
  部署模式。
- **跨主机集群**——每个 NameServer 内嵌 **DLedger（Raft）controller**，broker 以
  **controller 模式**运行：一个 broker 被选为 master，故障时另一个自动接管；proxy 作为
  客户端入口。这是 RocketMQ 5.x 的自动切换部署模式。

> **没有认证，也没有 TLS。** RocketMQ 使用 host 网络监听，集群端口固定。任何能连到
> NameServer、broker 或 proxy 端口的人都可以读写所有 topic。只应在可信内网或防火墙之后
> 运行，并限制 `9876`、`9877`、`10911`、`10912`、`8080`、`8081`。本插件也没有 Web UI
> 和 ACL。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `rocketmq` | 实例名（配置 map 的 key，也是子命令的默认实例名） |
| `--version` | `5.5.0` | 上游镜像 tag，镜像为 `docker.io/apache/rocketmq:<version>`；只接受 5.x |
| `--data-dir` | `<base_dir>/addon/rocketmq/<name>/data` | 实例数据根目录：broker store 为 `<dir>/broker`，集群模式的 controller store 为 `<dir>/controller`；除相对 `base_dir` 外必须是绝对路径 |
| `--advertise-host` | 单机 `127.0.0.1`；集群必填 | 向客户端和对端通告的地址。集群模式必须是非 loopback 的具体 **IPv4** 地址；主机名、IPv6 和 `0.0.0.0` 都会被拒绝。单机若需远程客户端访问，应设置为宿主机的局域网 IP |
| `--cluster NAME` | — | 开启跨主机集群模式。每台主机必须使用相同名称；它同时作为 DLedger controller group 和 broker cluster/group 名。集群身份安装后不可变 |
| `--controller-id ID` | — | 本节点的 controller 成员 id（`n0`、`n1`……）。集群首次安装必填，组内唯一，匹配 `[A-Za-z][A-Za-z0-9]{0,31}`；单机模式拒绝 |
| `--peer ID@HOST` | — | 另一个 controller 成员。按**其他**节点逐条重复，host 为裸的非 loopback IPv4、不带端口（controller 端口固定 `9877`）。每台主机都要传入其他成员的完整列表；本地成员会自动补全。单机模式拒绝 |
| `--namesrv-port` | 单机自动（`9876`） | NameServer 主机端口；`0` = 从 RocketMQ 端口池自动分配。仅单机可用，集群成员固定 `9876` |
| `--broker-port` | 单机自动（`10911`） | broker 监听端口；HA 端口为 `broker_port + 1`，VIP 端口为 `broker_port - 2`。仅单机可用，集群成员固定 `10911` |
| `--proxy-port` | 单机自动（`8080`） | proxy remoting 端口。仅单机可用，集群成员固定 `8080` |
| `--grpc-port` | 单机自动（`8081`） | proxy gRPC 端口（5.x SDK）。仅单机可用，集群成员固定 `8081` |
| `--controller-port` | — | 内嵌 controller 端口。单机模式不接受；集群成员固定 `9877` |
| `--heap` | `512m` | broker JVM 堆，写入 `JAVA_OPT_EXT=-Xms<size> -Xmx<size>` |
| `--namesrv-heap` | `256m` | NameServer JVM 堆 |
| `--proxy-heap` | `256m` | proxy JVM 堆 |
| `--config-option K=V` | — | 追加到 `broker.conf` 的设置（`flushDiskType=SYNC_FLUSH`、`brokerPermission=4`）；可重复，且**替换**已存储的覆盖列表。pgcli 托管的键会被拒绝（身份、监听、store 路径、controller 设置、复制设置、`autoCreateTopicEnable`） |
| `--force` | false | 重新渲染三个配置并重建三个容器，保留 broker store 和 controller store。集群身份和固定端口即使加 `--force` 也不能修改 |

`--image`、`--listen`、`--tls`、`--tls-pem`、`--tls-cert`、`--tls-key`、`--node-id`、
`--cluster-id`、`--join` 均不适用于 RocketMQ，会被拒绝。选择镜像请使用 `--version`。

## 安装单机实例

```bash
pg addon install rocketmq --name rmq
```

安装会按需拉取镜像、渲染配置、创建目录，按 `namesrv → broker → proxy` 启动，并等待
NameServer、broker 注册和两个 proxy 监听端口就绪：

```text
✓ RocketMQ "rmq" installed
  Version:     5.5.0
  Image:       docker.io/apache/rocketmq:5.5.0
  Containers:  pgcli-rocketmq-rmq-namesrv, pgcli-rocketmq-rmq-broker, pgcli-rocketmq-rmq-proxy
  Namesrv:     127.0.0.1:9876
  Broker:      127.0.0.1:10911 (HA 10912, VIP 10909)
  Proxy:       127.0.0.1:8080 (remoting), 127.0.0.1:8081 (gRPC)
  Mode:        single-node (no controller, no replicas)
  JVM heap:    broker 512m, namesrv 256m, proxy 256m
  Data dir:    /home/you/.pgcli/addon/rocketmq/rmq/data
  Config:      /home/you/.pgcli/addon/rocketmq/rmq/config
```

若需要其他主机上的客户端访问，应通告宿主机的局域网地址（proxy 配置和 broker 都使用它）：

```bash
pg addon install rocketmq --name rmq --advertise-host 10.0.0.11
```

第二个单机实例会从各端口池取下一个空闲端口，不会复用第一个实例的端口：

```bash
pg addon install rocketmq --name rmq2 --data-dir /srv/rocketmq-data
```

在内存较小的 VM 上，降低三个 JVM 的堆，避免三个进程超出内存：

```bash
pg addon install rocketmq --name rmq \
  --heap 384m --namesrv-heap 192m --proxy-heap 192m
```

不带 `--force` 重复执行 `install` 是空操作，只报告当前状态。带 `--force` 时，pgcli 会重写
`namesrv.conf`、`broker.conf` 和 `rmq-proxy.json`，重建三个容器，并保留 broker store 与
controller store。

## 多机集群

集群模式使用 RocketMQ 5.x 的 **controller 模式**：controller 以 DLedger（Raft）group 的
形式内嵌在每个 NameServer 中，controller 模式的 broker 选举 master 并在故障时自动切换。
每台主机各自执行一次 `pg addon install rocketmq`；pgcli 不会 SSH 到其他主机。

要求：

- 每台主机的 `--cluster` 名称相同。
- 每台主机使用唯一的 `--controller-id`（`n0`、`n1`、`n2`……）。
- 本机的 `--advertise-host` 是具体的非 loopback IPv4 地址。
- `--peer ID@HOST` 列出**其他** controller 成员；每台主机都要传完整对端集合，本地成员会
  自动补全。
- 每台主机上的固定集群端口空闲：`9876`、`9877`、`10911`、`10912`、`8080`、`8081`。
  一台主机只能运行一个集群 RocketMQ 实例。
- **至少 3 个成员才有容错。** DLedger 的 quorum 是多数派，2 个成员无法容忍任何故障；
  pgcli 会为 2 成员集群打印 warning。

逐台安装。以 `10.0.0.11`、`10.0.0.12`、`10.0.0.13` 三台主机为例：

```bash
# 在 10.0.0.11 上（controller n0）
pg addon install rocketmq --name rmq --cluster prod --controller-id n0 \
  --advertise-host 10.0.0.11 \
  --peer n1@10.0.0.12 --peer n2@10.0.0.13

# 在 10.0.0.12 上（controller n1）
pg addon install rocketmq --name rmq --cluster prod --controller-id n1 \
  --advertise-host 10.0.0.12 \
  --peer n0@10.0.0.11 --peer n2@10.0.0.13

# 在 10.0.0.13 上（controller n2）
pg addon install rocketmq --name rmq --cluster prod --controller-id n2 \
  --advertise-host 10.0.0.13 \
  --peer n0@10.0.0.11 --peer n1@10.0.0.12
```

前两台安装预计会以 warning 结束：

```text
[!] RocketMQ broker is running, but it has not registered with the controller yet
(broker not listed in clusterList) — install the remaining nodes with the same
--cluster and --peer values ...
```

这是正常现象：只有多数 NameServer 起来后 controller quorum 才会形成。安装仍会成功返回，
容器保持运行。第三台安装完成后，controller 选出 broker master，broker 完成注册。

逐台检查：

```bash
pg addon rocketmq status --name rmq
pg addon rocketmq cluster status --name rmq
```

`status` 显示三个容器状态、controller id、完整 peer 列表，以及集群可达时已注册的 broker
数和 master 数。`cluster status` 原样输出 `clusterList` 表格；controller 分配的
`BID` 列（旧版客户端里叫 `BrokerId`）中，当前 master 为 `0`，副本为非 0：

```text
rocketmq "rmq" — cluster prod: 3 broker(s), 1 master(s)
#Cluster Name  #Broker Name  #BID  #Addr             #Version  #InTPS(LOAD)  #OutTPS(LOAD)
prod           prod          0     10.0.0.13:10911   V5_5_0    0.00(0,0ms)   0.00(0,0ms)
prod           prod          1     10.0.0.11:10911   V5_5_0    0.00(0,0ms)   0.00(0,0ms)
prod           prod          2     10.0.0.12:10911   V5_5_0    0.00(0,0ms)   0.00(0,0ms)
```

列布局和行顺序由 `mqadmin` 产生，不同 5.x 版本可能略有差异；pgcli 只依赖 cluster 名和
broker id 列。

### 主备切换

master broker 停止后，controller 在心跳超时后从 in-sync 副本中选出新 master。通过 proxy
或 NameServer 连接的客户端会自动重试。观察一次切换：

```bash
# 在 cluster status 中找到 BID 0 所在主机，然后在该主机上：
pg addon stop rocketmq --name rmq

# 在任一成员上轮询，直到出现新的 BID 0：
pg addon rocketmq cluster status --name rmq

# 重新启动旧 broker；它会以副本身份重新加入：
pg addon start rocketmq --name rmq
```

配置中渲染了 `enableElectUncleanMaster=false`，不在 in-sync state set 中的副本不会被选为
master。如果 controller quorum 本身失去多数派（例如三台 NameServer 中两台停止），就无法
选出 master，写入会一直阻塞到 quorum 恢复。停止或移除一个成员**不会**改写其他主机上的
peer 列表；pgcli 不会联系其他节点。

## 端口

| 端口 | 角色 | 单机 | 集群 |
|---|---|---|---|
| `9876` | NameServer 客户端/RPC | 自动，基准 `9876` | 固定 |
| `9877` | 内嵌 DLedger controller | 不使用 | 固定 |
| `10911` | broker 监听 | 自动，基准 `10911` | 固定 |
| `10912` | broker HA 复制 | `broker_port + 1` | 固定 |
| `10909` | broker VIP | `broker_port - 2` | 固定 |
| `8080` | proxy remoting | 自动，基准 `8080` | 固定 |
| `8081` | proxy gRPC | 自动，基准 `8081` | 固定 |

单机实例可以从端口池分配端口并连同派生的 HA/VIP 端口一起保留，因此一台主机上可共存多个
实例。集群模式所有端口固定，因为 controller 成员关系和 broker 地址会写入每个节点的配置。

## 客户端

两种模式下 proxy 都是客户端入口，并以 `CLUSTER` 模式运行：

- **5.x gRPC SDK**（推荐的 5.x 客户端）：连接 `<advertise-host>:8081`。
- **Remoting SDK**（4.x 风格客户端）：连接 `<advertise-host>:8080`，或直接连接 broker
  `<advertise-host>:10911`。
- **NameServer 地址**（`9876`、controller `9877`）是集群内部通信地址，不要暴露给远程客户端。

单机默认 `127.0.0.1`，其他主机上的客户端需要使用设置了 `--advertise-host` 的安装。集群模式
每台成员都要求非 loopback 的 IPv4 `--advertise-host`，各主机上的 proxy 通告本机地址。

## Topic 与消息

为兼容启用了 topic 自动创建（`autoCreateTopicEnable=true`），但建议显式创建，以保证队列数
可预期：

```bash
pg addon rocketmq topic create surge --queues 3 --name rmq
pg addon rocketmq topic list --name rmq
pg addon rocketmq topic describe surge --name rmq
```

`topic create` 在整个集群上创建 topic（`-c <cluster-or-name>`），`--queues` 同时设置读写
队列数（默认 3）。`topic list` 默认隐藏 RocketMQ 内部 topic（`%RETRY%…`、`%DLQ%…`、
`TBW102`、`SELF_TEST_TOPIC`、事务/调度/trace topic、5.x 的 `rmq_sys_*` / `RMQ_SYS_*`、
`*_REPLY_TOPIC` 以及以 cluster 名命名的内部 topic）；加 `--system` 显示全部。
`topic describe` 输出 `topicStatus` 的按 broker 队列和 offset 表。

发送一条消息并按 key 查询：

```bash
pg addon rocketmq send surge "hello" --key k1 --tags demo --name rmq

pg addon rocketmq shell --service namesrv --name rmq -- \
  sh mqadmin queryMsgByKey -t surge -k k1 -n 127.0.0.1:9876
```

`send` 会打印 `sendMessage` 结果，包括 message id、发送状态和队列。用带边界的一次性调试消费者把消息拉回来：

```bash
pg addon rocketmq consume surge --count 10 --timeout 30s --name rmq

# 只读一个队列，从起始 offset 开始
pg addon rocketmq consume surge --queue 0 --offset 0 --count 50 --name rmq

# 按时间窗口读取（epoch 毫秒或 yyyy-MM-dd#HH:mm:ss:SSS）
pg addon rocketmq consume surge --begin 1793264528000 --end 1793264535000 \
  --count 0 --name rmq
```

`consume` 在一次性容器里运行 `mqadmin consumeMessage`，把消息打印到终端。它把 topic 各队列当前的消息拉完就退出；`--timeout`（默认 `30s`）是容器内的硬性安全上限，Ctrl-C 可提前结束。它只做拉取和打印，不删除消息，也不提交消费位点。

`--count` 是 mqadmin 的消息预算（默认 `10`）；mqadmin 按队列计算，实际打印的条数可能略少于请求值，`--count 0` 表示不设预算。`--queue`/`--offset` 只读一个队列，broker 名默认取实例的单 broker 名（`--broker` 可覆盖）。`--begin`/`--end` 接受 epoch 毫秒或 `yyyy-MM-dd#HH:mm:ss:SSS`；mqadmin 会把时间边界换算成队列 offset，因此结束边界可能包含恰好在其后的一条消息。

删除 topic 会删掉它的所有消息，除非加 `--yes`，否则会先要求确认：

```bash
pg addon rocketmq topic delete surge --name rmq --yes
```

## 投递与持久性

**单机。** broker 是唯一节点（`brokerId=0`、`brokerRole=ASYNC_MASTER`），没有副本。消息写入
`data/broker` 下的 broker store，因此优雅停止/启动不会丢失。渲染的刷盘策略是 RocketMQ 默认的
**ASYNC_FLUSH**，宿主机掉电可能丢失最近的写入。需要更强持久性时渲染 `SYNC_FLUSH`（会降低
吞吐）：

```bash
pg addon install rocketmq --name rmq --force \
  --config-option flushDiskType=SYNC_FLUSH
```

**集群。** broker 以 controller 模式复制。pgcli 渲染 `allAckInSyncStateSet=true`，3 个及以上
成员时 `minInSyncReplicas=2`（2 成员时为 `1`），因此只有所有 in-sync 副本都接受消息后
produce 才会成功。刷盘默认仍是 ASYNC；如果要求所有副本同时掉电也不丢已确认写入，请使用
`flushDiskType=SYNC_FLUSH`。

复制保护的是 controller quorum 健康时的 **broker** 故障，并不意味着 controller store 可以
随意删除：`data/controller` 保存 DLedger 状态和 epoch 文件。删除它、清空成员，或用
`--clean-data` 重装，都会破坏该成员的 controller 状态，并可能破坏 quorum。被移除后重新加入
的成员必须以空数据目录启动，并使用相同的 cluster/controller/peer 身份。

`pg addon remove rocketmq --clean-data` 会删除实例目录（配置、broker store、controller store
和日志）以及独立的自定义数据目录。不带 `--clean-data` 时会保留数据和配置目录。

## 配置、升级与不可变性

- **加 `--force` 可修改：** 版本、JVM 堆、broker.conf 覆盖项，以及单机实例的监听端口。
  `--config-option` 会替换已存储的覆盖 map，强制重装时需重新传入全部覆盖项。
- **即使加 `--force` 也不可修改：** 集群模式的 `--cluster`、`--controller-id`、
  `--advertise-host`、`--peer` 和固定端口。这些值已写入每个对端的配置，原地修改会被拒绝。
- **拓扑变更必须重建：** 单机 ↔ 集群不是原地变更。使用 `--clean-data` 删除实例后，按目标
  模式重新安装。
- **自定义数据目录：** 把 `--data-dir` 指向新路径会使用新的空 store；pgcli 不会迁移已有数据。

pgcli 托管的 `broker.conf` 键（在 `--config-option` 中会被拒绝）包括
`brokerClusterName`、`brokerName`、`brokerId`、`brokerRole`、`brokerIP1`/`brokerIP2`、
`listenPort`、`haListenPort`、`namesrvAddr`、`enableControllerMode`、`controllerAddr`、
所有 `controller*` 键、`storePathRootDir`、`storePathCommitLog`、`storePathEpochFile`、
`autoCreateTopicEnable`、`allAckInSyncStateSet`、`minInSyncReplicas`、
`enableControllerInNamesrv`、`enableElectUncleanMaster`、`notifyBrokerRoleChanged`。

## 生命周期、日志与开机自启

```bash
pg addon start rocketmq --name rmq
pg addon stop rocketmq --name rmq
pg addon remove rocketmq --name rmq --clean-data

pg logs addon rocketmq --name rmq --service broker -f
pg logs addon rocketmq --name rmq --service namesrv
pg logs addon rocketmq --name rmq --service proxy
```

停止按逆序（`proxy → broker → namesrv`），启动按正序（`namesrv → broker → proxy`）。集群模式
的启动不等待 controller quorum：多数派尚未可达时，broker 会保持等待状态，命令仍会成功返回。
其他成员起来后，用 `pg addon rocketmq status` 和 `cluster status` 确认。

开机自启与其他顶层插件一致：

```bash
pg autostart enable --rocketmq --name rmq
pg autostart status
```

它会重新渲染三个配置并按顺序启动容器。对于集群成员，自启不等待 controller quorum，因此各
成员可以任意顺序启动。`pg addon start`（全局自启启动器）行为相同。

## 限制

- **没有认证、TLS 或 ACL。** 请限制网络；proxy 和 NameServer 对任何能连到端口的人开放。
- **没有 Web UI。** 使用 `pg addon rocketmq status`、`cluster status`、topic 命令和日志。
- **不支持在线扩缩容或滚动升级。** 集群成员关系和固定端口不可变；成员变更需要有计划地重建。
  成员仍在 quorum 中时不能删除其 controller store。
- **2 成员集群没有容错能力。** 生产环境请使用 3 个及以上成员。
- **仅 Linux。** 插件依赖 host 网络和官方镜像的 `uid/gid 3000`。
- **不做消息迁移、consumer group 管理或 broker/storage 重平衡。** 可通过
  `pg addon rocketmq shell` 使用标准 RocketMQ 管理工具。
