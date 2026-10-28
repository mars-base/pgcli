---
title: "Kafka"
description: "以托管插件方式运行单机 Apache Kafka 4.x（KRaft 模式，无需 ZooKeeper），支持 topic 管理、渲染 server.properties 与 host 网络"
weight: 65
---

`pg addon install kafka` 以托管容器方式运行 **Apache Kafka 4.4.0**，直接使用官方
`docker.io/apache/kafka` 多架构镜像。Kafka 4.x 是 **KRaft-only**：元数据 controller
内建在 broker 进程里（`process.roles=broker,controller`，combined 模式），**不安装也不依赖
ZooKeeper**。

和 ClickHouse 一样，Kafka 是**顶层插件**，不代理某个 PostgreSQL 实例。pgcli 负责渲染
`server.properties`、固定 KRaft cluster id、绑定挂载数据目录，并以 host 网络启动容器。
当前版本只支持**单机 combined 节点**；多机集群参数已预留，但会被明确拒绝（见
[生产注意事项](#生产注意事项)）。

> **没有认证，也没有 TLS。** Kafka 仅使用 PLAINTEXT，`--listen` 默认 `127.0.0.1`。
> 只有在可信内网或防火墙/代理之后才对外暴露。任何能连到 broker 端口的人都可以读写
> 所有 topic。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `kafka` | 实例名（map key，也是子命令默认名字） |
| `--version` | `4.4.0` | 上游镜像 tag，直接从 `docker.io/apache/kafka` 拉取 |
| `--data-dir` | `<base_dir>/addon/kafka/<name>/data` | 数据目录，挂载到 `/var/lib/kafka/data`；必须是绝对路径 |
| `--listen` | `127.0.0.1` | PLAINTEXT 与 CONTROLLER 监听地址。`0.0.0.0` 会同时暴露两个端口 |
| `--advertise-host` | `127.0.0.1` | 告知客户端使用的地址，必须是具体 IP（不能是 `0.0.0.0`）；单机模式下同时作为 controller voter 地址 |
| `--broker-port` | 自动（基准 `9092`） | broker 宿主机端口；`0` = 从 `kafka_broker_start_port` 自动分配 |
| `--controller-port` | 自动（基准 `19093`） | KRaft controller 宿主机端口；`0` = 从 `kafka_controller_start_port` 自动分配 |
| `--cluster-id` | 自动生成 | 采用显式 22 字符 KRaft cluster id（否则生成，或从 `meta.properties` 采纳）；`remove` 后保留数据重装时也用它 |
| `--config-option K=V` | — | 额外的 `server.properties` 配置（`num.partitions=3`、`log.retention.hours=168`）；可重复；**替换**已存储的覆盖表 |
| `--force` | false | 重新渲染 `server.properties` 并重建容器；保留数据、已存储的 cluster id 与 node id |

Kafka 不使用 `--image`，用 `--version`。`--cluster`、`--peer`、`--node-id` 尚未实现，
传入会直接报"not implemented yet"错误。

controller 默认端口是 **19093** 而不是 9093——9093 属于 Alertmanager 的默认端口池。
拓扑相关键由 pgcli 自己渲染，`--config-option` 只能添加非托管配置：`process.roles`、
`node.id`、`listeners`、`advertised.listeners`、协议映射、controller 监听/voters、
`log.dirs`、内部 topic 的 replication factor、`default.replication.factor`、
`min.insync.replicas` 等都会被拒绝。

## 安装

```bash
pg addon install kafka --name k1
```

```
✓ Kafka "k1" installed
  Version:     4.4.0
  Image:       docker.io/apache/kafka:4.4.0
  Container:   pgcli-kafka-k1
  Broker:      127.0.0.1:9092
  Controller:  127.0.0.1:19093
  Node ID:     1
  Cluster ID:  <22-character id>
  Data dir:    /home/you/.pgcli/addon/kafka/k1/data
  Config:      /home/you/.pgcli/addon/kafka/k1/config/server.properties
```

第二个实例会从各自的端口池取下一个空闲端口（例如端口未被占用时的 `9093`/`19094`）：

```bash
pg addon install kafka --name k2 --data-dir /srv/kafka-data --listen 0.0.0.0 --advertise-host 192.0.2.10
```

不带 `--force` 重复 `install` 是幂等的，只打印当前状态。`--force` 会重写配置并重建容器，
保留数据目录、cluster id 与 node id。对已格式化的数据目录改 `--cluster-id` 会被拒绝——
数据属于原来的 cluster id。

镜像直接从 Docker Hub 拉取，不涉及 pgcli 自建镜像。`--version` 接受任意合法的上游 tag，
但 tag 必须真实存在，否则 pull 失败。

## 端口

| 端点 | 默认值 | 协议 | 端口池 |
|---|---|---|---|
| Broker | `9092` | Kafka 客户端协议（PLAINTEXT） | `kafka_broker_start_port`，默认 `9092` |
| Controller | `19093` | KRaft 元数据 quorum（PLAINTEXT） | `kafka_controller_start_port`，默认 `19093` |

两个池共享同一个"已分配"集合，并预占其他流式插件（Prometheus、ClickHouse、InfluxDB、
influx2tsdb 和对象存储）的显式端口，避免互相抢占。broker 与 controller 端口必须不同，
且所有 Kafka 实例的端口必须全局唯一。自动分配会跳过宿主机上已在监听的端口。

`--listen` 为通配地址时，pgcli 自己的 topic/健康检查命令走 `127.0.0.1`，打印的本地
controller 端点同理。单机模式的静态 voter 是 `AdvertiseHost:ControllerPort`，所以如果
把 `--advertise-host` 设成内网地址，必须同时把 `--listen` 设为 `0.0.0.0`（或同一个
内网地址），否则 controller 连不上自己。

## pgcli 创建的内容

单个实例的全部状态位于 `<base_dir>/addon/kafka/<name>/`：

| 宿主机路径 | 容器路径 | 说明 |
|---|---|---|
| `config/server.properties` | `/mnt/shared/config`（目录，只读） | 生成的拓扑配置；镜像入口会把它合并进运行时配置 |
| `data/` | `/var/lib/kafka/data` | 日志段、`meta.properties` 与 KRaft 元数据 |

生成的 `server.properties` 关键内容：

```properties
process.roles=broker,controller
node.id=1
listeners=PLAINTEXT://127.0.0.1:9092,CONTROLLER://127.0.0.1:19093
advertised.listeners=PLAINTEXT://127.0.0.1:9092
inter.broker.listener.name=PLAINTEXT
controller.listener.names=CONTROLLER
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
controller.quorum.voters=1@127.0.0.1:19093
log.dirs=/var/lib/kafka/data
```

再加上内部 topic 与自动创建 topic 的副本配置：单 voter 时
`offsets.topic.replication.factor=1`、`transaction.state.log.replication.factor=1`、
`transaction.state.log.min.isr=1`、share coordinator 的同名项、
`default.replication.factor=1`、`min.insync.replicas=1`。`--config-option` 覆盖项按
key 排序追加在托管配置之后。

容器以 `--network host`、`--restart unless-stopped`、30 秒停止超时运行，并显式传入
`-e CLUSTER_ID=<id>`，确保镜像用存储的 id 格式化数据目录（否则镜像会注入自己的固定
默认值）。

官方镜像以 **uid/gid 1000（`appuser`）** 运行。首次启动前，pgcli 用一次性的
`podman unshare chown -R 1000:1000` 把数据目录交给该用户。一旦 `meta.properties`
存在，说明目录属主已正确，后续 start 和 `--force` 重建都不会再递归遍历日志目录。

## Topic

```bash
pg addon kafka topic create demo --name k1
pg addon kafka topic create events --partitions 3 --replication-factor 1 --name k1
pg addon kafka topic create logs --config retention.ms=604800000 --name k1
pg addon kafka topic list --name k1
pg addon kafka topic list --exclude-internal --name k1
pg addon kafka topic describe demo --name k1
pg addon kafka topic delete demo --name k1 --yes
```

- `--partitions` 默认使用 broker 的 `num.partitions`（上游默认 `1`）。
  `--replication-factor 0`（默认值）表示"本实例的 RF"——单机为 `1`。
- `--config KEY=VALUE` 可重复，直接透传给 `kafka-topics.sh`。
- `delete` 默认要求确认，`--yes` 跳过；删除 topic 会删除其全部消息。
- 所有 topic 命令都要求实例正在运行。

用容器内的 Kafka 脚本写入和读取（脚本位于 `/opt/kafka/bin`，不在 `PATH` 中）：

```bash
pg addon kafka shell --name k1 -- bash -lc \
  'printf "hello\nworld\n" | /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server 127.0.0.1:9092 --topic demo'

pg addon kafka shell --name k1 -- bash -lc \
  '/opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server 127.0.0.1:9092 --topic demo --from-beginning --max-messages 2'
```

外部客户端使用打印出的 broker 端点作为 `--bootstrap-server`。对默认 loopback 监听，
宿主机客户端直接用 `127.0.0.1:9092` 即可。

## 状态与 quorum

```bash
pg addon kafka status --name k1          # 容器状态、端点、id、路径
pg addon kafka cluster status --name k1  # KRaft 元数据 quorum 的 leader 与 voters
pg logs addon kafka --name k1 -f         # broker 日志（stdout/stderr）
```

`cluster status` 执行 `kafka-metadata-quorum.sh --bootstrap-controller <本地 controller>
describe --status`；健康的单机会打印 cluster id、leader 和一个 voter。

`install` 和 `start` 最多等待 90 秒，直到 `kafka-broker-api-versions.sh` 在本地 broker
端点返回结果。一直不就绪会让命令失败并打印最后几行日志；数据会保留。

## 配置

安装时可通过 `server.properties` 覆盖项补充配置：

```bash
pg addon install kafka --name k1 \
  --config-option num.partitions=3 \
  --config-option log.retention.hours=168
```

`--config-option` **替换**已存储的覆盖表（不是合并）。覆盖项保存在 `pg.yaml` 的
`addons.kafka.<name>.settings` 下，每次启动都会重新渲染，因此重启和重建容器都不会丢失。
本版本没有单独的 `config set` 命令；修改覆盖项请带 `--force` 重新安装，并传入完整的
覆盖表。

key 允许字母、数字、点、下划线和连字符；value 不允许 NUL、CR、LF。pgcli 托管的 key
会被拒绝，而不是被静默覆盖。

## 生命周期

```bash
pg addon start kafka --name k1            # 启动，不存在则创建，并等待就绪
pg addon stop kafka --name k1
pg addon remove kafka --name k1           # 删除容器 + pg.yaml 条目，保留数据/配置
pg addon remove kafka --name k1 --clean-data   # 同时删除实例目录和自定义数据目录
```

不带 `--clean-data` 时，`remove` 会留下 `<实例目录>`（配置 + 数据）以及自定义
`--data-dir`。同名重装会**采纳已格式化的存储**：自动复用 `meta.properties` 里的
`cluster.id` 与 `node.id`，而不是生成新集群——因此重装可以恢复原有日志。

`--clean-data` 删除实例目录以及（若不同）自定义数据目录（uid 1000 的文件通过
`podman unshare` 删除），但不会删除已拉取的镜像。如果把实例指向属于其他 cluster id
或 node id 的数据目录，会在创建容器前被拒绝。

开机自启：

```bash
pg autostart enable --kafka --name k1
pg autostart status
pg autostart disable --kafka --name k1
```

开机启动会先重新渲染 `server.properties`，并且是幂等的（`pg start` 也会启动
autostart 启用的 Kafka 实例）。

## 生产注意事项

- **本版本仅支持单机。** 没有副本，也没有 HA：broker 停止后无法读写。
  `--cluster`/`--peer`/`--node-id` 预留给未来的多机拓扑，目前会明确报错。
- **KRaft combined 模式。** broker 和 controller 共用一个进程，适合开发和小规模部署；
  上游建议大型关键集群使用独立的 controller/broker 角色，本插件暂不提供。
- **无认证、无 TLS。** 唯一的安全边界是监听地址。保持默认 loopback，或只在可信内网
  暴露 `0.0.0.0`。如需 TLS/认证，请自行在 Kafka 前放置代理/网关。
- **没有 ZooKeeper。** Kafka 4.x 是 KRaft-only，不存在 ZooKeeper 容器；
  `zookeeper.connect` 作为托管 key 会被拒绝。
- **副本不是备份。** topic 副本（单机没有）保障的是可用性，不能防误删或逻辑损坏。
  离线备份请停止实例后复制数据目录；本版本没有 backup/restore 命令。
- **升级。** `--force --version <新版本>` 会拉取新 tag 并在同一数据目录上重建容器。
  Kafka 不保证新版本写入的数据能回退到旧版本——升级前请做离线拷贝。
  `--version` 必须是真实存在的上游 tag。
- **数据属主。** 数据文件属主为 uid/gid 1000。清理请用 `--clean-data`，不要手工删除；
  pgcli 会处理权限回退。
- **端口。** broker 默认 `9092`；controller 默认 `19093`，避开 Alertmanager 默认的
  `9093`。host 网络下它们就是普通宿主机端口。
- **平台。** Linux（host 网络）；官方镜像多架构（amd64/arm64）。

## 命令行参数参考

### `pg addon install kafka`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `kafka` | 实例名；`[A-Za-z0-9][A-Za-z0-9_-]*`，最多 48 字符 |
| `--version TAG` | `4.4.0` | 上游 `docker.io/apache/kafka` tag |
| `--data-dir DIR` | `<base_dir>/addon/kafka/<name>/data` | 绝对路径宿主机数据目录，挂载到 `/var/lib/kafka/data` |
| `--listen ADDR` | `127.0.0.1` | broker 与 controller 的监听地址 |
| `--advertise-host IP` | `127.0.0.1` | 对外广播的 broker 地址（单机也是 controller voter 地址）；不能是通配地址 |
| `--broker-port N` | 自动（基准 `9092`） | broker 宿主机端口；`0` = 自动分配 |
| `--controller-port N` | 自动（基准 `19093`） | controller 宿主机端口；`0` = 自动分配；必须与 broker 端口不同 |
| `--cluster-id ID` | 生成 / 采纳 | 显式 22 字符 base64url KRaft cluster id |
| `--config-option K=V` | — | 额外 `server.properties` 配置；可重复；替换已存储的覆盖表 |
| `--force` | false | 重新渲染配置并重建容器，保留数据和 id |

### `pg addon kafka status` / `shell`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `kafka` | 目标实例 |
| `-- COMMAND...` | `/bin/bash` | 仅 `shell`：执行命令而不是打开交互式 shell |

### `pg addon kafka topic create|list|describe|delete`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `NAME` | — | topic 名（`create`/`describe`/`delete` 的位置参数） |
| `--partitions N` | broker 默认（`1`） | 仅 `create`：分区数 |
| `--replication-factor N` | `0` = 实例 RF（`1`） | 仅 `create`：副本数 |
| `--config K=V` | — | 仅 `create`：topic 配置；可重复 |
| `--exclude-internal` | false | 仅 `list`：隐藏 Kafka 内部 topic |
| `--yes` | false | 仅 `delete`：跳过确认 |

### `pg addon kafka cluster status`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `kafka` | 查询哪个实例的本地 controller |

### 生命周期、日志与开机自启

| 命令 | 参数 | 说明 |
|---|---|---|
| `pg addon start kafka` | `--name NAME` | 启动（不存在则创建），等待就绪 |
| `pg addon stop kafka` | `--name NAME` | 停止容器，保留数据 |
| `pg addon remove kafka` | `--name NAME`、`--clean-data` | 删除容器与配置条目；`--clean-data` 同时删除实例目录和自定义数据目录 |
| `pg logs addon kafka` | `--name NAME`、`-n N`（默认 50）、`-f` | 容器日志 |
| `pg autostart enable\|disable` | `--kafka`、`--name NAME` | 开关单个实例的 boot 启动 |

### 全局参数

| 参数 | 说明 |
|---|---|
| `-c, --config FILE` | 配置文件（默认 `~/.pgcli/pg.yaml`） |
| `-o, --output FILE` | 输出配置路径（默认与输入相同） |
| `-i, --instance NAME` | PostgreSQL 实例选择器——Kafka 不使用，请用 `--name` |
