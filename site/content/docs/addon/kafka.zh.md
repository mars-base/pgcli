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

> **没有认证。** Kafka 默认只使用 PLAINTEXT，`--listen` 默认 `127.0.0.1`。TLS 是可选功能
> （`--tls`），只加密传输，**不认证客户端**（`ssl.client.auth=none`），且 PLAINTEXT 监听
> 仍然保留以保持兼容。只有在可信内网或防火墙/代理之后才对外暴露。任何能连到任一监听端口
> 的人都可以读写所有 topic。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `kafka` | 实例名（map key，也是子命令默认名字） |
| `--version` | `4.4.0` | 上游镜像 tag，直接从 `docker.io/apache/kafka` 拉取 |
| `--data-dir` | `<base_dir>/addon/kafka/<name>/data` | 数据目录，挂载到 `/var/lib/kafka/data`；必须是绝对路径 |
| `--listen` | `127.0.0.1` | PLAINTEXT、SSL（启用 `--tls` 时）与 CONTROLLER 监听地址。`0.0.0.0` 会暴露所有已启用的监听端口 |
| `--advertise-host` | `127.0.0.1` | 告知客户端使用的地址，必须是具体 IP（不能是 `0.0.0.0`）；单机模式下同时作为 controller voter 地址 |
| `--broker-port` | 自动（基准 `9092`） | broker 宿主机端口；`0` = 从 `kafka_broker_start_port` 自动分配 |
| `--controller-port` | 自动（基准 `19093`） | KRaft controller 宿主机端口；`0` = 从 `kafka_controller_start_port` 自动分配 |
| `--tls` | false | 在明文 broker 之外增加 SSL 监听，使用 pgcli 自签 CA 签发的证书；PLAINTEXT 监听保留 |
| `--tls-pem` | — | 用这个合并后的 PEM（证书链 + 私钥）提供 TLS，替代自动生成的证书。隐含 `--tls`；文件会先校验再复制为 `config/tls/server.pem`。支持 PKCS#8、PKCS#1 与 SEC1 私钥 |
| `--tls-port` | 自动（基准 `9093`） | SSL 监听宿主机端口；`0` = 从 `kafka_tls_start_port` 自动分配；需要 `--tls` |
| `--cluster-id` | 自动生成 | 采用显式 22 字符 KRaft cluster id（否则生成，或从 `meta.properties` 采纳）；`remove` 后保留数据重装时也用它 |
| `--config-option K=V` | — | 额外的 `server.properties` 配置（`num.partitions=3`、`log.retention.hours=168`）；可重复；**替换**已存储的覆盖表 |
| `--force` | false | 重新渲染 `server.properties` 并重建容器；保留数据、已存储的 cluster id 与 node id；增删 TLS 必须带 `--force` |

Kafka 不使用 `--image`，用 `--version`。`--cluster`、`--peer`、`--node-id` 尚未实现，
传入会直接报"not implemented yet"错误。

controller 默认端口是 **19093** 而不是 9093——9093 属于 Alertmanager 的默认端口池（TLS 池
虽然从 9093 开始，但会跳过其他插件已占用的端口）。拓扑相关键由 pgcli 自己渲染，
`--config-option` 只能添加非托管配置：`process.roles`、`node.id`、`listeners`、
`advertised.listeners`、协议映射、controller 监听/voters、`log.dirs`、SSL keystore/
客户端认证键、内部 topic 的 replication factor、`default.replication.factor`、
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
| SSL（TLS） | `9093`（启用 `--tls` 时） | TLS 上的 Kafka 客户端协议 | `kafka_tls_start_port`，默认 `9093` |

三个池共享同一个"已分配"集合，并预占其他流式插件（Prometheus、ClickHouse、InfluxDB、
influx2tsdb 和对象存储）的显式端口，避免互相抢占。Kafka 端口分配还会显式避让
Alertmanager 已占端口（Alertmanager 池也反向预占 Kafka 端口），因为两者的默认池都在
`9093` 附近。broker、controller 与 TLS 端口必须互不相同，且所有 Kafka 实例的端口必须
全局唯一。自动分配会跳过宿主机上已在监听的端口。

`--listen` 为通配地址时，pgcli 自己的 topic/健康检查命令走 `127.0.0.1`，打印的本地
controller 端点同理。单机模式的静态 voter 是 `AdvertiseHost:ControllerPort`，所以如果
把 `--advertise-host` 设成内网地址，必须同时把 `--listen` 设为 `0.0.0.0`（或同一个
内网地址），否则 controller 连不上自己。

## TLS

用 `--tls` 启用 SSL 监听：

```bash
pg addon install kafka --name k1 --tls                 # SSL 使用 9093 起的首个空闲端口
pg addon install kafka --name k1 --tls --tls-port 9093
```

`install` 与 `status` 会同时打印两个端点和 CA 路径：

```
  Broker:      127.0.0.1:9092
  TLS:         127.0.0.1:9093 (CA .../addon/kafka/k1/config/tls/ca.crt)
  Controller:  127.0.0.1:19093
```

PLAINTEXT 监听保持开启，已有客户端不受影响。TLS 只在创建或 `--force` 时生效；
只传 `--tls-port` 而不传 `--tls` 会报错。关闭用 `--tls=false --force`——生成的文件仍留在
`config/tls/`，重新启用时复用。

证书由 pgcli 自签 CA（与 MinIO/silo/rustfs 相同的 helper）签发，SAN 覆盖 `localhost`、
宿主机所有网卡地址和 `--advertise-host`。客户端需要信任 `config/tls/ca.crt`。

### 使用自带证书（`pg cert`）

Kafka 的 PEM keystore 只读**一个**文件：先是证书链，随后是私钥。`pg cert` 会把证书和私钥
分别写出，因此安装前先手动合并：

```bash
# 1. 签发服务端证书。--host 要包含客户端实际拨号的所有地址，
#    尤其是 --advertise-host。
pg cert --host 127.0.0.1,10.241.20.147 \
  --cert-file kafka.crt --key-file kafka.key

# 2. 合并证书（链）与私钥为一个 PEM。
cat kafka.crt kafka.key > kafka-server.pem

# 3. 用合并后的文件安装 —— --tls 会被自动启用。
pg addon install kafka --name k1 --tls-pem ./kafka-server.pem
```

有中间证书时把叶子证书放在最前：`cat leaf.crt intermediate.crt key.key > kafka-server.pem`。

安装时 pgcli 会校验：文件包含证书且只有一个未加密私钥、两者匹配、证书当前有效、允许
服务端认证用途、且不是 CA 证书。若 `--advertise-host` 不在 SAN 中会打印警告——broker
仍会启动，但做主机名校验的客户端会连接失败。支持 PKCS#8（`BEGIN PRIVATE KEY`，即
`pg cert` 的输出）、PKCS#1（`BEGIN RSA PRIVATE KEY`）与 SEC1（`BEGIN EC PRIVATE KEY`）；
加密私钥会被拒绝。

pgcli 会把合并后的文件复制为 `config/tls/server.pem`（0600，属主 uid 1000），**不会**
生成 `ca.crt` 或 `public.crt`/`private.key`，源文件保持不变。自动续期不适用：更换源 PEM 后
需要带 `--force` 重新 install，才能把新证书装进新容器。

自签的 `pg cert` 证书需要客户端直接信任证书文件——把下文示例中的 `ca.crt` 换成
`kafka.crt`。如果证书由公共或私有 CA 签发，则让客户端信任该 CA 链，并把中间证书一并
放进 `kafka-server.pem`。

要从自带证书切回 pgcli 生成的证书，用 `--tls --tls-pem '' --force` 重新安装；
`--tls-pem` 传空值会清除已存储的路径。

Java 客户端（`client.properties`）：

```properties
security.protocol=SSL
ssl.truststore.type=PEM
ssl.truststore.location=/path/to/addon/kafka/k1/config/tls/ca.crt
```

librdkafka 系客户端：

```properties
security.protocol=SSL
ssl.ca.location=/path/to/addon/kafka/k1/config/tls/ca.crt
```

用 OpenSSL 验证监听：

```bash
openssl s_client -connect 127.0.0.1:9093 \
  -CAfile <base_dir>/addon/kafka/k1/config/tls/ca.crt -verify_return_error </dev/null
```

在容器内通过 TLS 读写（TLS 目录随 config 目录只读挂载）：

```bash
pg addon kafka shell --name k1 -- bash -lc \
  'printf "security.protocol=SSL\nssl.truststore.type=PEM\nssl.truststore.location=/mnt/shared/config/tls/ca.crt\n" > /tmp/client.properties
   printf "hello\n" | /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server 127.0.0.1:9093 --producer.config /tmp/client.properties --topic demo'

pg addon kafka shell --name k1 -- bash -lc \
  'printf "security.protocol=SSL\nssl.truststore.type=PEM\nssl.truststore.location=/mnt/shared/config/tls/ca.crt\n" > /tmp/client.properties
   /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server 127.0.0.1:9093 --consumer.config /tmp/client.properties --topic demo --from-beginning --max-messages 1'
```

- TLS 只负责加密：`ssl.client.auth=none`，客户端无需证书，能连到 SSL 端口的人依然可以连接。
  监听地址仍是唯一的安全边界。
- 请用 IP 连接（或证书中存在的名字）。不在宿主机网卡地址中的 DNS 名不在 SAN 列表里，
  校验会失败。
- 叶子证书在剩余有效期不足 30 天时自动重新签发（install、start 或 `--force` 时检查）。
  CA 保持不变，客户端无需更换信任文件。该行为只适用于 pgcli 生成的证书；使用
  `--tls-pem` 时续期由你负责。

## pgcli 创建的内容

单个实例的全部状态位于 `<base_dir>/addon/kafka/<name>/`：

| 宿主机路径 | 容器路径 | 说明 |
|---|---|---|
| `config/server.properties` | `/mnt/shared/config`（目录，只读） | 生成的拓扑配置；镜像入口会把它合并进运行时配置 |
| `config/tls/` | `/mnt/shared/config/tls`（只读） | 仅 `--tls`：`server.pem`（证书链 + 私钥，0600，属主 uid 1000）；由 pgcli 生成证书时还有 `ca.crt`（0644）、`public.crt` 与 `private.key`。使用 `--tls-pem` 时只写 `server.pem`——即你提供的合并 PEM 的规范化副本 |
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

启用 `--tls` 后，监听与安全配置变为：

```properties
listeners=PLAINTEXT://127.0.0.1:9092,SSL://127.0.0.1:9093,CONTROLLER://127.0.0.1:19093
advertised.listeners=PLAINTEXT://127.0.0.1:9092,SSL://127.0.0.1:9093
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,SSL:SSL
ssl.keystore.type=PEM
ssl.keystore.location=/mnt/shared/config/tls/server.pem
ssl.client.auth=none
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
启用 `--tls` 时只把合并后的 `config/tls/server.pem` 交给 uid/gid 1000（镜像以
`appuser` 读取 PEM keystore）；config 目录、CA 与原始私钥仍归宿主机所有，目录保持可遍历
以支持只读挂载。

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
- **无认证；TLS 只负责传输加密。** PLAINTEXT 监听始终开启；`--tls` 增加的加密监听因
  `ssl.client.auth=none` 而不认证客户端。能连到任一端口的人都可以读写所有 topic。
  保持默认 loopback，或只在可信内网暴露。如需要真正的认证（SASL/mTLS），请自行在
  Kafka 前放置代理/网关。
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
  `9093`；启用 TLS 时 SSL 从 `9093` 起分配并跳过已占端口。host 网络下它们就是普通
  宿主机端口。
- **平台。** Linux（host 网络）；官方镜像多架构（amd64/arm64）。

## 命令行参数参考

### `pg addon install kafka`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `kafka` | 实例名；`[A-Za-z0-9][A-Za-z0-9_-]*`，最多 48 字符 |
| `--version TAG` | `4.4.0` | 上游 `docker.io/apache/kafka` tag |
| `--data-dir DIR` | `<base_dir>/addon/kafka/<name>/data` | 绝对路径宿主机数据目录，挂载到 `/var/lib/kafka/data` |
| `--listen ADDR` | `127.0.0.1` | broker、SSL（启用时）与 controller 的监听地址 |
| `--advertise-host IP` | `127.0.0.1` | 对外广播的 broker 地址（单机也是 controller voter 地址）；不能是通配地址 |
| `--broker-port N` | 自动（基准 `9092`） | broker 宿主机端口；`0` = 自动分配 |
| `--controller-port N` | 自动（基准 `19093`） | controller 宿主机端口；`0` = 自动分配；必须与 broker 端口不同 |
| `--tls` | false | 增加 SSL 监听；PLAINTEXT 监听保留 |
| `--tls-pem FILE` | — | SSL 监听使用的合并 PEM（证书链 + 私钥）；隐含 `--tls`；路径可为相对或绝对 |
| `--tls-port N` | 自动（基准 `9093`） | SSL 监听宿主机端口；`0` = 自动分配；需要 `--tls` |
| `--cluster-id ID` | 生成 / 采纳 | 显式 22 字符 base64url KRaft cluster id |
| `--config-option K=V` | — | 额外 `server.properties` 配置；可重复；替换已存储的覆盖表 |
| `--force` | false | 重新渲染配置并重建容器，保留数据和 id；增删 TLS 必须使用 |

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
