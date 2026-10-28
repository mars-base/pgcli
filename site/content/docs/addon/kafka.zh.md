---
title: "Kafka"
description: "以托管插件方式运行 Apache Kafka 4.x（KRaft 模式，无需 ZooKeeper）——支持单机或跨主机静态 controller quorum，含 topic 管理、渲染 server.properties 与 host 网络"
weight: 65
---

`pg addon install kafka` 以托管容器方式运行 **Apache Kafka 4.4.0**，直接使用官方
`docker.io/apache/kafka` 多架构镜像。Kafka 4.x 是 **KRaft-only**：元数据 controller
内建在 broker 进程里（`process.roles=broker,controller`，combined 模式），**不安装也不依赖
ZooKeeper**。

和 ClickHouse 一样，Kafka 是**顶层插件**，不代理某个 PostgreSQL 实例。pgcli 负责渲染
`server.properties`、固定 KRaft cluster id、绑定挂载数据目录，并以 host 网络启动容器。
单条命令安装一个**单机 combined 节点**；加上 `--cluster`、`--node-id` 和 `--peer`
则安装**跨主机静态 controller quorum** 中的一个节点，每台主机各自执行一次
`pg addon install kafka`（pgcli 不会通过 SSH 远程操作）。见[多机集群](#多机集群)。

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
| `--listen` | `127.0.0.1` | PLAINTEXT、SSL（启用 `--tls` 时）与 CONTROLLER 监听地址。`0.0.0.0` 会暴露所有已启用的监听端口；集群模式下必须是 `0.0.0.0` 或本节点 advertise 地址 |
| `--advertise-host` | `127.0.0.1` | 告知客户端使用的地址，必须是具体 IP（不能是 `0.0.0.0`）；单机模式下同时作为 controller voter 地址，集群模式下是本节点的 voter 地址，因此必须是具体非 loopback IP，且全新集群安装必须显式传入 |
| `--broker-port` | 自动（基准 `9092`） | broker 宿主机端口；`0` = 从 `kafka_broker_start_port` 自动分配 |
| `--controller-port` | 自动（基准 `19093`） | KRaft controller 宿主机端口；`0` = 从 `kafka_controller_start_port` 自动分配。集群安装必须显式传入：所有 peer 都会把它写进 voter 列表 |
| `--cluster NAME` | — | 加入名为该值的多机集群；所有主机必须相同，KRaft cluster id 由它确定性推导 |
| `--node-id N` | 单机 `1` | 本节点 1-based KRaft node id；全新集群安装必填，集群内唯一；单机模式下会被拒绝 |
| `--peer ID@HOST:PORT` | — | 另一个 controller voter。每个**其他**节点各写一次（本节点自动折叠进去）；每台主机都要传完整 peer 列表。host 必须是 IP，port 必须与该节点的 `--controller-port` 一致 |
| `--tls` | false | 在明文 broker 之外增加 SSL 监听，使用 pgcli 自签 CA 签发的证书；PLAINTEXT 监听保留 |
| `--tls-pem` | — | 用这个合并后的 PEM（证书链 + 私钥）提供 TLS，替代自动生成的证书。隐含 `--tls`；文件会先校验再复制为 `config/tls/server.pem`。支持 PKCS#8、PKCS#1 与 SEC1 私钥。集群模式下证书必须覆盖本节点的 `--advertise-host` |
| `--tls-port` | 自动（基准 `9093`） | SSL 监听宿主机端口；`0` = 从 `kafka_tls_start_port` 自动分配；需要 `--tls` |
| `--cluster-id` | 集群推导 / 单机生成 | 集群模式下 id 由 `--cluster` 推导，显式值必须与之相等；单机模式可显式采用 22 字符 KRaft cluster id，否则采纳 `meta.properties` 或生成 |
| `--config-option K=V` | — | 额外的 `server.properties` 配置（`num.partitions=3`、`log.retention.hours=168`）；可重复；**替换**已存储的覆盖表 |
| `--force` | false | 重新渲染 `server.properties` 并重建容器；保留数据、已存储的 cluster id 与 node id；增删 TLS 必须带 `--force`。集群身份字段（cluster、node id、cluster id、peers、advertise host、controller port）即使带 `--force` 也不可修改 |

Kafka 不使用 `--image`，用 `--version`。

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

## 多机集群

集群是**静态 KRaft quorum**：每个节点都携带完整的 `controller.quorum.voters` 列表，
成员关系不能在线变更。每台主机用一个节点安装，使用相同的 `--cluster` 名字、集群内唯一的
`--node-id`、本机的 `--advertise-host`、显式的 `--controller-port`，并为每个**其他**
节点传一条 `--peer`。命令由你在每台主机上分别执行；pgcli 不会通过 SSH 批量操作。

以三台主机 `192.0.2.11`、`192.0.2.12`、`192.0.2.13` 为例：

```bash
# 节点 1，在 192.0.2.11 上
pg addon install kafka --name prod --cluster prod --node-id 1 \
  --listen 0.0.0.0 --advertise-host 192.0.2.11 \
  --broker-port 9092 --controller-port 19093 \
  --peer 2@192.0.2.12:19093 --peer 3@192.0.2.13:19093

# 节点 2，在 192.0.2.12 上
pg addon install kafka --name prod --cluster prod --node-id 2 \
  --listen 0.0.0.0 --advertise-host 192.0.2.12 \
  --broker-port 9092 --controller-port 19093 \
  --peer 1@192.0.2.11:19093 --peer 3@192.0.2.13:19093

# 节点 3，在 192.0.2.13 上
pg addon install kafka --name prod --cluster prod --node-id 3 \
  --listen 0.0.0.0 --advertise-host 192.0.2.13 \
  --broker-port 9092 --controller-port 19093 \
  --peer 1@192.0.2.11:19093 --peer 2@192.0.2.12:19093
```

身份信息是这样拼出来的：

- **Cluster ID。** 由 `--cluster` 确定性哈希推导，所有主机无需传 `--cluster-id` 也能得到
  相同值。若显式传入 `--cluster-id`，必须等于推导值。
- **Voters。** `ID@HOST:PORT` 按 node id 排序；本节点的条目来自 `--node-id`、
  `--advertise-host` 和 `--controller-port`，其余由 peer 提供。两个 voter 不允许共享
  同一个 controller endpoint。
- **Controller 端口。** 它出现在所有 peer 的 voter 列表里，所以全新集群安装必须显式
  传 `--controller-port`（默认且推荐值：`19093`）。自动分配可能在其他主机选到不同端口。
- **端口与地址。** 不同主机可以使用相同的 broker/controller 端口。`--listen` 必须是
  `0.0.0.0`（或本节点的 advertise 地址）；loopback 会被拒绝。

前两台安装会打印 controller quorum 尚未就绪的警告——在最后一个 voter 装好之前这是正常的。
集群模式下就绪等待是 15 秒而不是 90 秒。所有节点起来后：

```bash
pg addon kafka cluster status --name prod    # leader + 3 个 voter
pg addon kafka status --name prod            # cluster、node x of N、voter、voters、RF/minISR
pg addon list                                # 显示 "cluster prod" 和 voter 列表
```

Topic 默认副本数跟随 quorum 大小：`--replication-factor 0` 表示 `min(3, voters)`
（三节点即 3），`min.insync.replicas = min(2, RF)`。**允许两 voter 集群，但零容错**——
失去任一节点就失去元数据 quorum——因此 pgcli 会打印警告。HA 请用 3 或另一个奇数。

安装后集群身份不可变，即使带 `--force`：`--cluster`、`--node-id`、`--cluster-id`、
`--peer` 集合、`--advertise-host` 和 `--controller-port` 变化都会被拒绝。端口、TLS、
版本、`--data-dir` 与 `--config-option` 仍可通过 `--force` 修改。要改变拓扑，只能在本机
remove 后重装；本插件不提供在线增删 controller。

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

集群模式下 controller 端口不只是本地监听——它会被其他所有主机写进自己的
`controller.quorum.voters`。所有节点请使用相同的 `--controller-port`（除非你刻意让每个节点
用不同端口，此时每条 `--peer` 必须携带对应节点的正确端口），并确保该端口在所有主机上
都空闲。端口不一致意味着 voter 列表不同，quorum 永远无法形成。broker 端口是按节点广播的，
可以不同，但全部使用 `9092` 能让客户端 bootstrap 列表更简单。

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

### 集群 TLS

每个节点各自签发**独立的** CA 和叶子证书，因此可能连接任意 broker 的客户端需要把它们
放进同一个信任文件：

```bash
# 在各主机上取出该节点的 CA，然后在客户端上拼接。
for v in vm01 vm02 vm03; do
  ssh $v 'cat ~/.pgcli/addon/kafka/prod/config/tls/ca.crt'
done > kafka-ca-bundle.pem
```

SSL 监听是按节点开启的：需要统一的 SSL bootstrap 列表时请给**所有**节点启用 `--tls`。
如果只有部分节点启用了 TLS，这些节点会广播 `SSL://...` 端点，其余节点只广播
`PLAINTEXT://...`；使用 `security.protocol=SSL` 的客户端仍能连到启用了 TLS 的节点，
但纯明文节点保持明文。请逐台执行 `--tls --force`，保证三 voter 的 quorum 始终保留两个。

`--tls` 只增加面向客户端的 SSL 监听。**CONTROLLER** quorum 与 broker 间流量始终是
`PLAINTEXT`，所以增删 TLS 不会破坏 quorum。

### 使用自带证书（`pg cert`）

Kafka 的 PEM keystore 只读**一个**文件：先是证书链，随后是私钥。`pg cert` 会把证书和私钥
分别写出，因此安装前先手动合并：

```bash
# 1. 签发服务端证书。--host 要包含客户端实际拨号的所有地址，
#    尤其是 --advertise-host。
pg cert --host 127.0.0.1,192.0.2.11 \
  --cert-file kafka.crt --key-file kafka.key

# 2. 合并证书（链）与私钥为一个 PEM。
cat kafka.crt kafka.key > kafka-server.pem

# 3. 用合并后的文件安装 —— --tls 会被自动启用。
pg addon install kafka --name k1 --tls-pem ./kafka-server.pem
```

有中间证书时把叶子证书放在最前：`cat leaf.crt intermediate.crt key.key > kafka-server.pem`。

安装时 pgcli 会校验：文件包含证书且只有一个未加密私钥、两者匹配、证书当前有效、允许
服务端认证用途、且不是 CA 证书。还会检查 SAN 是否覆盖 `--advertise-host`：集群模式下
证书不覆盖本节点 advertise IP 是**硬错误**（其他 voter 和客户端会用该地址访问本节点）；
单机模式只打印警告。支持 PKCS#8（`BEGIN PRIVATE KEY`，即 `pg cert` 的输出）、
PKCS#1（`BEGIN RSA PRIVATE KEY`）与 SEC1（`BEGIN EC PRIVATE KEY`）；加密私钥会被拒绝。

pgcli 会把合并后的文件复制为 `config/tls/server.pem`（0600，属主 uid 1000），**不会**
生成 `ca.crt` 或 `public.crt`/`private.key`，源文件保持不变。自动续期不适用：更换源 PEM 后
需要带 `--force` 重新 install，才能把新证书装进新容器。

自签的 `pg cert` 证书需要客户端直接信任证书文件——把下文示例中的 `ca.crt` 换成
`kafka.crt`。如果证书由公共或私有 CA 签发，则让客户端信任该 CA 链，并把中间证书一并
放进 `kafka-server.pem`。

集群中请为**每个节点单独**签发覆盖其 advertise IP 的证书，并且只传给该节点的 install：

```bash
# 节点 1，其 --advertise-host 为 192.0.2.11：
pg cert --host 192.0.2.11 --cert-file kafka-node1.crt --key-file kafka-node1.key
cat kafka-node1.crt kafka-node1.key > kafka-node1.pem
pg addon install kafka --name prod --cluster prod --node-id 1 --force ... --tls-pem ./kafka-node1.pem

# 客户端需要信任所有节点：把各节点证书（或 CA）拼起来。
cat kafka-node1.crt kafka-node2.crt kafka-node3.crt > kafka-ca-bundle.pem
```

如果某节点的证书没有覆盖它的 `--advertise-host`，集群安装会在重建容器之前被拒绝。

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

三节点集群示例（展示 `node 2 of prod`；`--listen 0.0.0.0`）：

```properties
process.roles=broker,controller
node.id=2
listeners=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:19093
advertised.listeners=PLAINTEXT://192.0.2.12:9092
inter.broker.listener.name=PLAINTEXT
controller.listener.names=CONTROLLER
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
controller.quorum.voters=1@192.0.2.11:19093,2@192.0.2.12:19093,3@192.0.2.13:19093
log.dirs=/var/lib/kafka/data
offsets.topic.replication.factor=3
transaction.state.log.replication.factor=3
transaction.state.log.min.isr=2
default.replication.factor=3
min.insync.replicas=2
```

voter 列表在三台主机上完全相同并按 node id 排序；只有本节点的 `node.id` 一行不同。
副本数为 `min(3, voters)`，ISR 为 `min(2, RF)`。

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
pg addon kafka topic retention demo 7d --name k1   # 设置 retention.ms
pg addon kafka topic retention demo --name k1      # 查看当前覆盖项
```

- `--partitions` 默认使用 broker 的 `num.partitions`（上游默认 `1`）。
  `--replication-factor 0`（默认值）表示"本实例的 RF"——单机为 `1`，集群为
  `min(3, voters)`（三节点即 `3`）。
- `--config KEY=VALUE` 可重复，直接透传给 `kafka-topics.sh`。
- `delete` 默认要求确认，`--yes` 跳过；删除 topic 会删除其全部消息。
- `retention NAME [DURATION]` 查看或设置 `retention.ms`。时长支持
  `ms`/`s`/`m`/`h`/`d`/`w` 后缀（`30m`、`12h`、`7d`、`4w`）、纯数字毫秒，
  或 `-1` / `infinite` / `forever` 表示永久保留。不带时长时打印 topic 的
  动态覆盖项；结果为空表示使用 broker 默认值。保留时间按时间清理旧 segment，
  即使消息从未被消费也会被删除。
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

集群中每个 broker 广播自己的地址，客户端可以从任意节点 bootstrap，随后被重定向到分区
leader。生产者和消费者可以运行在不同主机上：

```bash
pg addon kafka topic create e2e --partitions 3 --replication-factor 3 --name prod

# 在节点 1 上通过本地 broker 写入：
pg addon kafka shell --name prod -- bash -lc \
  'printf "alpha\nbeta\n" | /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server 192.0.2.11:9092 --topic e2e'

# 在节点 3 上从节点 2 的 broker 读取：
pg addon kafka shell --name prod -- bash -lc \
  '/opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server 192.0.2.12:9092 --topic e2e --from-beginning --max-messages 2'
```

### 用 kcat 在宿主机测试

[kcat](https://github.com/edenhill/kcat) 是轻量的原生生产/消费工具，适合在容器外做
冒烟测试（Debian/Ubuntu `apt install kcat`，macOS `brew install kcat`）。从任意节点
bootstrap 即可，客户端会自动跟随 advertised leader。示例使用上文创建的 `e2e`
topic（3 分区、RF=3）：

```bash
# PLAINTEXT
printf 'alpha\nbeta\n' | kcat -P -b 192.0.2.11:9092 -t e2e
kcat -C -q -b 192.0.2.12:9092 -t e2e -o beginning -e -c 2   # 前两条
kcat -C -q -b 192.0.2.13:9092 -t e2e -o -1 -e -c 1         # 最后一条
```

TLS 场景下把 kcat 指向[集群 TLS](#集群-tls) 一节拼出的 CA bundle，并选择 SSL
（TLS 监听端口为 `9093`）：

```bash
printf 'gamma\n' | kcat -P -b 192.0.2.11:9093 -t e2e \
  -X security.protocol=SSL -X ssl.ca.location=kafka-ca-bundle.pem
kcat -C -q -b 192.0.2.12:9093 -t e2e -o -1 -e -c 1 \
  -X security.protocol=SSL -X ssl.ca.location=kafka-ca-bundle.pem
```

- `-o beginning` / `-o -1` 分别从第一条 / 最后一条开始；`-e` 读到 EOF 退出，
  `-c N` 消费 N 条后退出；`-q` 只输出消息体。
- `-b` 使用 advertised 地址即可，客户端会被重定向到分区 leader，无需逐分区配置。
- 这些示例**不加入消费组**：kcat 直接读分区，不提交 offset，因此
  `pg addon kafka group list` 里不会出现任何 group。要以消费组方式读取，加
  `-G NAME`（topic 改为位置参数），见[消费组](#消费组)。

## 消费组

`kafka-consumer-groups.sh` 的日常查询封装成了两个子命令：

```bash
pg addon kafka group list --name k1
pg addon kafka group describe demo-group --name k1
```

- `list` 列出在本集群提交过 offset 的 group id。
- `describe` 输出 group 状态、成员，以及每个分区的 `CURRENT-OFFSET` /
  `LOG-END-OFFSET` / `LAG` 和消费者 id、host。LAG 持续增长说明消费速度跟不上。
- group 在第一次提交 offset 后才会出现；`group.id` 由客户端设置，pgcli 不创建
  消费组。冒烟测试可以先带 `--group` 消费一次：

```bash
pg addon kafka shell --name prod -- bash -lc \
  '/opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server 192.0.2.11:9092 --topic e2e --group demo-group --from-beginning --max-messages 2'
pg addon kafka group list --name prod
pg addon kafka group describe demo-group --name prod
```

在宿主机上用 kcat 做同样的事则用 `-G`；组模式下 topic 是位置参数，不能用 `-t`：

```bash
kcat -C -q -G demo-group -b 192.0.2.11:9092 \
  -X auto.offset.reset=earliest -c 2 e2e
pg addon kafka group list --name prod
pg addon kafka group describe demo-group --name prod
```

上文各节的 `console-consumer` 和 kcat 示例刻意不带 group（读完即退出的冒烟
测试），因此不会创建消费组。

- 两个命令都要求实例正在运行。消费者停止且未提交 offset 时 group 会留在 `Empty`
  状态，offset 和 lag 仍可见，直到 offset 过期。

## 状态与 quorum

```bash
pg addon kafka status --name k1          # 容器状态、端点、id、路径
pg addon kafka cluster status --name k1  # KRaft 元数据 quorum 的 leader 与 voters
pg logs addon kafka --name k1 -f         # broker 日志（stdout/stderr）
```

`cluster status` 执行 `kafka-metadata-quorum.sh --bootstrap-controller <本地 controller>
describe --status`；健康的单机会打印 cluster id、leader 和一个 voter。集群中它会报告
leader 以及所有 voter 的日志延迟和是否已追上——在任意节点执行均可：

```
ClusterId:              <推导出的 id>
LeaderId:               1
CurrentVoters:          [{"id": 1, ...}, {"id": 2, ...}, {"id": 3, ...}]
```

`N` 个 voter 容忍 `(N-1)/2` 台节点停机——三节点容忍一台，两节点一台都不能停。停掉多数派
会失去元数据 quorum：broker 的现有分区只能按 `min.insync.replicas` 允许的范围继续读写，
在 quorum 恢复前无法进行 leader 选举或元数据变更。

`install` 和 `start` 最多等待 90 秒，直到 `kafka-broker-api-versions.sh` 在本地 broker
端点返回结果（集群模式为 15 秒，因为完整 quorum 要等最后一个 voter 装好才形成）。
一直不就绪会让命令失败并打印最后几行日志；数据会保留。

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

集群中 `remove` 只影响**本机**——pgcli 不会联系其他节点，它们的静态 voter 列表仍然引用
本节点。`pg addon remove` 会先打印 quorum 影响。三个 voter 时，移除（或停止）一个节点
集群仍可用；移除第二个会失去 quorum。之后重新加入必须使用**相同**的 `--cluster`、
`--node-id` 和 `--peer` 列表；`--clean-data` 后数据目录为空，broker 会用相同的推导
cluster id 重新格式化。

开机自启同样是按主机的——请在每个节点上启用：

```bash
pg autostart enable --kafka --name prod   # 在每台主机执行
pg autostart status
pg autostart disable --kafka --name prod
```

开机启动会先重新渲染 `server.properties`，并且是幂等的（`pg start` 也会启动
autostart 启用的 Kafka 实例）。

## 生产注意事项

- **静态 quorum，不能在线扩缩。** `--cluster` 安装的是固定 KRaft quorum 中的一个节点，
  没有在线增删 controller 的能力：扩容或缩容需要用新的 voter 列表重装节点。HA 请使用
  3（或其他 ≥3 的奇数）个 voter；允许两 voter，但零容错。
- **默认仍是单机。** 不带 `--cluster` 时没有副本也没有 HA：broker 停止后该实例无法读写。
- **KRaft combined 模式。** broker 和 controller 共用一个进程，适合开发和小规模部署；
  上游建议大型关键集群使用独立的 controller/broker 角色，本插件暂不提供。
- **无认证；TLS 只负责传输加密。** PLAINTEXT 监听始终开启；`--tls` 增加的加密监听因
  `ssl.client.auth=none` 而不认证客户端。能连到任一端口的人都可以读写所有 topic。
  集群模式下 CONTROLLER quorum 与 broker 间流量同样是 PLAINTEXT——`--tls` 只覆盖客户端
  连接。保持默认 loopback，或只在可信内网暴露。如需要真正的认证（SASL/mTLS），请自行在
  Kafka 前放置代理/网关。
- **没有 ZooKeeper。** Kafka 4.x 是 KRaft-only，不存在 ZooKeeper 容器；
  `zookeeper.connect` 作为托管 key 会被拒绝。
- **副本不是备份。** topic 副本保障的是可用性，不能防误删或逻辑损坏。
  离线备份请停止实例后复制数据目录；本版本没有 backup/restore 命令，且单个集群节点的
  离线拷贝并不是完整的集群备份。
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
| `--listen ADDR` | `127.0.0.1` | broker、SSL（启用时）与 controller 的监听地址；集群模式要求 `0.0.0.0` 或 advertise 地址 |
| `--advertise-host IP` | `127.0.0.1` | 对外广播的 broker 地址（也是本节点的 controller voter 地址）；不能是通配地址；全新集群安装必须显式传入 |
| `--broker-port N` | 自动（基准 `9092`） | broker 宿主机端口；`0` = 自动分配 |
| `--controller-port N` | 自动（基准 `19093`） | controller 宿主机端口；`0` = 自动分配；全新集群安装必须显式传入，因为每个 peer 都会内嵌它 |
| `--cluster NAME` | — | 多机集群名；所有主机相同；KRaft cluster id 由它推导 |
| `--node-id N` | 单机 `1` | 1-based node id，集群内唯一；全新集群安装必填；单机模式会被拒绝 |
| `--peer ID@HOST:PORT` | — | 另一个节点的 controller voter；每个其他节点各一条；所有主机传相同 peer 列表 |
| `--tls` | false | 增加 SSL 监听；PLAINTEXT 监听保留 |
| `--tls-pem FILE` | — | SSL 监听使用的合并 PEM（证书链 + 私钥）；隐含 `--tls`；路径可为相对或绝对；集群模式下必须覆盖 `--advertise-host` |
| `--tls-port N` | 自动（基准 `9093`） | SSL 监听宿主机端口；`0` = 自动分配；需要 `--tls` |
| `--cluster-id ID` | 集群推导 / 单机生成 | KRaft cluster id；集群模式下必须等于由 `--cluster` 推导出的值 |
| `--config-option K=V` | — | 额外 `server.properties` 配置；可重复；替换已存储的覆盖表 |
| `--force` | false | 重新渲染配置并重建容器，保留数据和 id；增删 TLS 必须使用；不能修改集群身份字段 |

### `pg addon kafka status` / `shell`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `kafka` | 目标实例 |
| `-- COMMAND...` | `/bin/bash` | 仅 `shell`：执行命令而不是打开交互式 shell |

### `pg addon kafka topic create|list|describe|delete|retention`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `NAME` | — | topic 名（`create`/`describe`/`delete`/`retention` 的位置参数） |
| `DURATION` | — | 仅 `retention`：新的 `retention.ms`（`30m`、`12h`、`7d`、`4w`、纯毫秒，或 `-1`/`infinite`）；省略则查看当前覆盖项 |
| `--partitions N` | broker 默认（`1`） | 仅 `create`：分区数 |
| `--replication-factor N` | `0` = 实例 RF（单机 `1`，集群 `min(3, voters)`） | 仅 `create`：副本数 |
| `--config K=V` | — | 仅 `create`：topic 配置；可重复 |
| `--exclude-internal` | false | 仅 `list`：隐藏 Kafka 内部 topic |
| `--yes` | false | 仅 `delete`：跳过确认 |

### `pg addon kafka group list|describe`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `GROUP` | — | 消费组 id（仅 `describe` 的位置参数） |
| `--name NAME` | `kafka` | 查询哪个实例的本地 broker |

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
