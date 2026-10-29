---
title: "RabbitMQ"
description: "以托管插件方式运行 RabbitMQ broker——单节点，或通过 etcd 自动发现组建多机集群；AMQP 与 management Web UI/API、可选 TLS（自动生成 CA 或自带证书）、以及队列管理（list/declare/info、publish/get、bind/unbind、purge/delete）"
weight: 66
---

`pg addon install rabbitmq` 以托管容器方式运行 **RabbitMQ 4.4.0**，直接使用官方
`docker.io/library/rabbitmq` 的 `-management` 镜像（内含 Web UI 与 HTTP API）。和
Kafka、ClickHouse 一样，RabbitMQ 是**顶层插件**：pgcli 负责渲染 `rabbitmq.conf`、管理
管理员账号、绑定挂载数据目录，并以 host 网络启动容器。

插件支持**单节点，或作为多机集群的一个成员**。集群使用 **etcd 自动发现**
（`rabbitmq_peer_discovery_etcd`）：每台主机以相同的 `--cluster` 名和 etcd endpoint 列表安装
一个成员，首次启动时成员自动注册并互相发现——不需要静态 peer 列表，也不需要手动
`join_cluster`。不配置 federation、shovel、MQTT 或 STOMP；vhost、用户、权限、策略、
exchange 等高级管理通过 management UI 或 `pg addon rabbitmq shell` 里的 `rabbitmqctl`
完成。常用队列操作已封装为 `pg addon rabbitmq queue` 子命令（见[队列与消息](#队列与消息)）。
集群模式下 `declare` 默认创建 **quorum** 队列（见[集群](#集群)）。

> **认证始终开启。** AMQP 与 management UI 都需要 pgcli 创建的管理员账号（默认
> `admin`，20 位自动生成密码）。`--listen` 默认 `127.0.0.1`。对外暴露时，密码就是安全
> 边界：TLS 只加密传输，**不认证客户端**（`verify_none`、`fail_if_no_peer_cert = false`）。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `rabbitmq` | 实例名（map key，也是子命令默认名字） |
| `--version` | `4.4.0` | 上游镜像 tag，实际拉取 `docker.io/library/rabbitmq:<version>-management` |
| `--admin-user` | `admin` | 管理员账号，首启创建，每次启动对账 |
| `--admin-password` | 自动生成（20 位） | 管理员密码；同时存入 `pg.yaml` 与 `0600` secret 文件。集群模式拒绝明文，请用 `--secrets-from-etcd` 或 `--admin-password-file` |
| `--admin-password-file FILE` | — | 从 `FILE` 读取管理员密码（单行）。集群成员首次安装必填（使用 `--secrets-from-etcd` 时除外）；单机实例也可使用 |
| `--data-dir` | `<base_dir>/addon/rabbitmq/<name>/data` | 数据目录，挂载到 `/var/lib/rabbitmq`；必须是绝对路径 |
| `--listen` | 单机 `127.0.0.1`，集群 `0.0.0.0` | AMQP、AMQPS（启用 `--tls` 时）与 management 监听共用的绑定地址。`0.0.0.0` 会暴露所有已启用监听；集群模式不能是 loopback |
| `--cluster NAME` | — | 加入/创建名为 `NAME` 的 RabbitMQ 集群。空 = 单机；所有成员必须相同 |
| `--node-name NODE@HOST` | — | 本成员的 Erlang 节点名（如 `rabbit@192.0.2.11`）。集群成员首次安装必填；必须唯一。host 为 IP 时无需名称解析；为主机名时必须在每个成员上可解析 |
| `--etcd m1,m2` | — | 用本地 etcd addon 成员作为发现 endpoint；与 `--etcd-endpoints` 二选一（后者优先） |
| `--etcd-endpoints host:port,...` | — | 外部 etcd 发现 endpoint；可重复或逗号分隔。仅支持明文 HTTP，可带 `http://` 前缀（会被去掉） |
| `--erlang-cookie-file FILE` | — | 从 `FILE` 读取共享的 Erlang cookie。集群成员首次安装必填（使用 `--secrets-from-etcd` 时除外）；值保存后不可变 |
| `--secrets-from-etcd` | false | 从 `pg addon rabbitmq secret generate` 写入的 etcd key 读取两个共享密钥（推荐，无需在主机间复制文件）。不能与 `--erlang-cookie-file` 或 `--admin-password-file` 同时使用 |
| `--amqp-port` | 自动（基准 `5672`） | 明文 AMQP 端口；`0` = 从 `rabbitmq_start_port` 自动分配 |
| `--management-port` | 自动（基准 `15672`） | Management HTTP UI/API 端口；`0` = 从 `rabbitmq_management_start_port` 自动分配 |
| `--tls` | false | 在明文监听之外增加 AMQPS 与 management HTTPS，使用 pgcli 自签 CA 签发的证书 |
| `--tls-cert FILE` | — | 自带服务端证书（PEM 链）；必须与 `--tls-key` 同时传入；隐含 `--tls`；`--listen` 不是 `0.0.0.0` 时必须覆盖该地址 |
| `--tls-key FILE` | — | 自带私钥（PKCS#8/PKCS#1/SEC1），与 `--tls-cert` 配对 |
| `--tls-port` | 自动（基准 `5671`） | AMQPS 端口；`0` = 从 `rabbitmq_tls_start_port` 自动分配；需要 `--tls` |
| `--config-option K=V` | — | 额外的 `rabbitmq.conf` 配置（`heartbeat=60`、`log.file.level=debug`）；可重复；**替换**已存储的覆盖表 |
| `--force` | false | 重新渲染配置并重建容器；保留数据与已存储的密码。集群模式下 `--cluster`、`--node-name` 与 Erlang cookie 不可变，`--etcd-endpoints` 可以更新 |

RabbitMQ 不使用 `--image`，用 `--version`。`--version` 接受任意合法的上游 tag，但 tag 必须
真实存在，否则 pull 失败；镜像引用始终是 `docker.io/library/rabbitmq:<version>-management`。

## 安装

```bash
pg addon install rabbitmq --name rmq
```

```
✓ RabbitMQ "rmq" installed
  Version:    4.4.0
  Image:      docker.io/library/rabbitmq:4.4.0-management
  Container:  pgcli-rabbitmq-default-rmq
  AMQP:       amqp://127.0.0.1:5672
  Management: http://127.0.0.1:15672
  Admin:      admin
  Password:   <generated>
  Data dir:   /home/you/.pgcli/addon/rabbitmq/rmq/data
  Config:     /home/you/.pgcli/addon/rabbitmq/rmq/config/rabbitmq.conf
```

⚠️ 单机版创建的是持久化 **classic** 队列，但**消息默认不持久化**。classic 的消息持久化是
逐条消息的属性，**必须由发布端在每次 publish 时设置**（`pg addon rabbitmq queue publish`
加 `--property delivery_mode=2`，应用客户端设置 `delivery_mode=2`），不存在队列参数或
policy 级别的开关。未设置时 broker 重启后队列仍在、消息丢失。集群模式使用 quorum 队列，
会自动持久化所有消息。

第二个实例会从各自的端口池取下一个空闲端口（例如 AMQP `5673`、management `15673`、
EPMD `4370`、distribution `25673`）：

```bash
pg addon install rabbitmq --name rmq2 --data-dir /srv/rabbitmq-data --listen 0.0.0.0
```

不带 `--force` 重复 `install` 是幂等的，只打印当前状态。带 `--force` 时，pgcli 会重写
`rabbitmq.conf`、重写 `enabled_plugins`、重建容器并重新对账管理员账号；数据目录与密码保留。

密码只在安装时打印一次，之后可随时取回：

```bash
pg addon password rabbitmq --name rmq
```

## Management UI 与 API

Management Web UI 的地址：

```text
单机: http://127.0.0.1:15672        （--listen 默认 127.0.0.1）
集群: http://<成员IP>:15672          （任意成员，如 http://192.0.2.11:15672）
```

执行 `pg addon rabbitmq status --name rmq` 可打印本实例准确的 `Management:` 地址（随
`--listen` 与 `--management-port` 变化）。浏览器打开该地址，用管理员账号登录，密码通过
`pg addon password rabbitmq --name rmq` 获取。集群模式下任意成员都提供相同的 UI，
**Overview → Nodes** 可看到整个集群。

同一端口也提供 HTTP API：

```bash
pw="$(pg addon password rabbitmq --name rmq)"
curl -fsS -u "admin:$pw" http://127.0.0.1:15672/api/overview
```

CLI 未封装的能力——vhost、用户、权限、策略与 exchange 定义——都通过 UI 完成。
常用队列操作已提供 `pg addon rabbitmq queue ...` 命令（见[队列与消息](#队列与消息)）。

## 端口

| 端点 | 默认 | 端口池 |
|---|---|---|
| AMQP（明文） | `5672` | `rabbitmq_start_port`，默认 `5672` |
| AMQPS（TLS） | `5671`（启用 `--tls` 时） | `rabbitmq_tls_start_port`，默认 `5671` |
| Management HTTP UI/API | `15672` | `rabbitmq_management_start_port`，默认 `15672` |
| Management HTTPS | `15671`（启用 `--tls` 时） | `rabbitmq_management_tls_start_port`，默认 `15671` |
| Erlang EPMD | `4369` | `rabbitmq_epmd_start_port`，默认 `4369` |
| Erlang distribution | `25672` | `rabbitmq_dist_start_port`，默认 `25672` |

每个池独立分配：实例按名字排序，显式端口不会被覆盖，宿主已监听的端口会被跳过。所有
RabbitMQ 实例的端口必须互不重复。

EPMD 与 distribution 是基础设施端口，不是客户端端点。单机实例以
`RABBITMQ_NODENAME=rabbit@localhost`、`ERL_EPMD_ADDRESS=127.0.0.1` 和独立的
EPMD/distribution 端口运行，`rabbitmq.conf` 里固定
`distribution.listener.interface = 127.0.0.1`——Erlang distribution 永不对外暴露，多个单机
实例可在同一宿主共存，也不会与宿主的 Erlang 安装冲突。

**集群成员不同**：每个成员固定使用 EPMD `4369` 与 distribution `25672`，distribution 监听
`--listen`（默认 `0.0.0.0`）。因此**一台主机最多运行一个集群 RabbitMQ 实例**，且 `4369`、
`25672` 必须能被其他成员访问（见[集群](#集群)）。

**不启用 `rabbitmq_prometheus` 插件。** pgcli 挂载自己的 `enabled_plugins`（单机只有
`rabbitmq_management`，集群模式为 `rabbitmq_management` 与 `rabbitmq_peer_discovery_etcd`），
保证每个实例行为一致，也不会多开一个无认证的固定 metrics 监听。

## 集群

集群通过 **etcd 自动发现**组建，不使用静态 peer 列表。每台主机以相同的 `--cluster` 名和
etcd endpoint 列表运行一个成员；首次启动时成员在 etcd 中注册自己，并加入已经注册的节点。

前置条件：

- **etcd 3.4+**，每个成员都能以明文 HTTP 访问。测试时单成员 etcd 即可，但生产环境的发现
  存储本身应当冗余。本版本 pgcli 不支持该 etcd 连接的 TLS 或认证，请放在可信网络中。可以
  用 pgcli 的 etcd addon（`pg addon install etcd ...`），也可以用 `--etcd-endpoints` 指向
  外部 etcd。
- **节点名唯一。** `--node-name rabbit@host` 在集群内必须唯一。host 部分可以是主机名或
  IPv4；拒绝 loopback 与 IPv6。使用 **IP** 时成员直接互连，完全不需要名称解析——没有内网
  DNS 时推荐这种方式。使用**主机名**时，每个成员都必须能解析它（DNS 或 `/etc/hosts`）：
  etcd 发现只注册 `rabbit@host`，不提供地址。FQDN、IP 会让 RabbitMQ 自动使用 Erlang
  long names。
- **共享密钥。** 所有成员必须使用**相同的 Erlang cookie** 和**相同的管理员密码**。先在管理机
  执行一次 `pg addon rabbitmq secret generate`：它生成本地 `0600` 文件并把内容写入 etcd；
  各成员安装时用 `--secrets-from-etcd` 读取，无需在主机间复制文件。pgcli 会把两个值保存到
  `pg.yaml` 和 `0600` secret 文件，因此 `start` 与 autostart 不再依赖 etcd 或原始文件。
  仍支持手动分发文件时使用 `--erlang-cookie-file` / `--admin-password-file`。这两个都是
  **集群级**密钥，AMQP 客户端都不需要。
- **版本一致。** 每个成员的 `--version` 相同。
- **集群名一致。** 不同的 `--cluster` 会在同一个 etcd 中形成另一套集群；pgcli 通过
  `cluster_formation.etcd.cluster_name` 隔离。

在管理机上生成并发布一次共享密钥：

```bash
pg addon rabbitmq secret generate --cluster pgcli-lab \
  --etcd-endpoints 192.0.2.10:2379
```

命令不会打印密钥值。它会写入 `/tmp/rmq.cookie` 与 `/tmp/rmq.admin`（权限 `0600`），并把
相同内容写入以下 etcd key（不是文件）：

```text
/pgcli/rabbitmq/clusters/pgcli-lab/secrets/erlang_cookie
/pgcli/rabbitmq/clusters/pgcli-lab/secrets/admin_password
```

这两个本地文件各有明确用途：

| 文件 | 用途 |
|---|---|
| `/tmp/rmq.cookie` | 共享 **Erlang cookie**。仅用于集群成员之间的 Erlang distribution 握手（EPMD `4369`、distribution `25672`），AMQP 与管理客户端从不使用。 |
| `/tmp/rmq.admin` | 共享的**broker 管理员密码**，用于创建/对账 `admin` 账号。它是安装输入，不是客户端凭据文件：应用应使用自己创建的 RabbitMQ 用户连接。需要以管理员身份登录时，用 `pg addon password rabbitmq --name mq` 取回已保存的密码即可，不必保留该文件。 |

这两个文件都是**安装引导/分发用的中间产物**。安装时每个成员都会把值保存到 `pg.yaml` 和
自己的 `0600` secret 文件，`start`/autostart 不会读取 `/tmp` 下的文件，因此所有成员安装
完成后即可删除。若不用 `--secrets-from-etcd` 而选择手动分发，则把这两个文件复制到各主机
（内容必须一致），并通过 `--erlang-cookie-file` / `--admin-password-file` 传入；各主机的
路径可以不同。成员安装完成后**绝不要重新生成 Erlang cookie**——它不可变，cookie 不一致的
节点无法加入集群。

在各成员主机上安装（只有 `--node-name` 不同；用各主机 IP 可免去 `/etc/hosts`/DNS）：

```bash
# 192.0.2.11
pg addon install rabbitmq --name mq --cluster pgcli-lab \
  --node-name rabbit@192.0.2.11 \
  --etcd-endpoints 192.0.2.10:2379 \
  --secrets-from-etcd

# 192.0.2.12
pg addon install rabbitmq --name mq --cluster pgcli-lab \
  --node-name rabbit@192.0.2.12 \
  --etcd-endpoints 192.0.2.10:2379 \
  --secrets-from-etcd

# 192.0.2.13
pg addon install rabbitmq --name mq --cluster pgcli-lab \
  --node-name rabbit@192.0.2.13 \
  --etcd-endpoints 192.0.2.10:2379 \
  --secrets-from-etcd
```

每个成员都读取相同的值，不需要手动复制文件。若倾向手动分发，可跳过 `secret generate`，
改用两个 `--*-file` 参数。

etcd 中这些值是**明文**的。任何能访问 etcd client 端口的人都能读到集群凭据，因此请把
`2379` 限制在成员主机与管理机之间；不要把密钥放进其他租户可读的共享 etcd。

第一个成员会启动一个只有自己的集群并打印提示；后续成员通过 etcd 发现并自动加入。发现只在
Mnesia 为空时执行——已加入的成员重启时联系 last-seen peers。

发现 key 位于

```text
/rabbitmq/discovery/rabbitmq/clusters/<cluster-name>/nodes/<node-name>
```

因此只要 `--cluster` 名不同，多套集群可以共用一个 etcd。

### 队列语义

- 集群模式下 `pg addon rabbitmq queue declare` 默认 `--type quorum`。Quorum 队列会在所有成员
  上复制（默认 quorum size 为 3，以成员数为上限），是 RabbitMQ 4.x 唯一支持复制的队列类型。
- `--type classic` 仍可使用，但 **classic 队列不复制**：它只存在于声明它的节点上，该节点
  宕机时队列不可用。仅建议用于非关键或可重建的数据。
- `--durable=false`、quorum + `--auto-delete` 等限制不变（见[队列与消息](#队列与消息)）。
- 每个节点都能服务客户端，management API 也会展示整个集群，因此客户端可以连接任意成员。
  生产环境请配置多个 endpoint，或前置 TCP 负载均衡。

### 查看与下线成员

```bash
pg addon rabbitmq cluster status --name mq
pg addon rabbitmq cluster forget rabbit@192.0.2.13 --name mq --yes
```

`cluster status` 输出 `rabbitmqctl cluster_status`，包含所有 running nodes。`cluster forget`
用于把**已停止、永久下线**的成员从集群元数据中移除；它拒绝 forget 本机节点。移除成员后，
重新平衡 quorum 队列的副本分布：

```bash
pg addon rabbitmq shell --name mq
rabbitmq-queues rebalance quorum
```

只是临时停机的成员不需要任何操作：重启后会通过 last-seen peers 自动重新加入。不要在节点
仍计划恢复时执行 `cluster forget`——旧节点将无法重新加入，只能清空数据重建（或在对端 forget
后用空数据目录重新添加）。

### 集群约束

- `--cluster`、`--node-name` 与 Erlang cookie **不可变**。用不同的值执行 `install --force`
  会失败；`--etcd-endpoints` 可以更新。
- 单机实例不能转换为集群成员（反之亦然）：用 `--clean-data` 删除后重新安装。集群首次安装
  也要求数据目录为空。
- `--advertise-host`、`--node-id`、`--peer`、`--join` 对 RabbitMQ 不适用；节点身份由
  `--node-name` 唯一决定。集群模式拒绝明文 `--admin-password`。
- 管理员密码是集群级凭据，所有成员必须保存相同的值。轮换时先在管理机执行
  `pg addon rabbitmq secret generate --force --keep-cookie ...` 发布新密码（或改用
  `--admin-password-file ...`），然后在**每个**成员上重新执行
  `install --force --secrets-from-etcd`（或对应的 `--admin-password-file ...`）。
  成员安装后**绝不能重新生成 Erlang cookie**：它不可变，变化会让 `install --force` 失败。
- etcd 中 `/pgcli/rabbitmq/clusters/<name>/secrets/` 下的密钥是**明文**。请把 etcd client
  端口限制在成员主机与管理机之间。这两个密钥只属于集群：AMQP 客户端都不需要 cookie 或
  管理员密码，业务应用应创建独立的 RabbitMQ 用户。
- 集群互联流量（`25672` 上的 Erlang distribution）**不加密**；`--tls` 只保护客户端监听。
  请把 `4369` 与 `25672` 限制在私有网络，且只对成员开放。

## TLS

TLS 是**增量**的：`--tls` 保留明文 AMQP 与 management 监听，另外增加 AMQPS 与
management HTTPS。

```bash
pg addon install rabbitmq --name rmq --tls
pg addon install rabbitmq --name rmq --tls --tls-port 5671
```

`install` 与 `status` 会同时打印两套端点和 CA 路径：

```
  AMQP:       amqp://127.0.0.1:5672
  Management: http://127.0.0.1:15672
  AMQPS:      amqps://127.0.0.1:5671
  Mgmt TLS:   https://127.0.0.1:15671
  CA cert:    /home/you/.pgcli/addon/rabbitmq/rmq/config/tls/ca.crt
```

自动生成的证书由 pgcli 自签 CA（与 MinIO/silo/rustfs、Kafka 共用的 helper）签发，覆盖
`localhost`、`127.0.0.1`、`::1`、宿主所有网卡地址以及具体的 `--listen` 地址。客户端必须
信任 `config/tls/ca.crt`：

```bash
openssl s_client -connect 127.0.0.1:5671 \
  -CAfile "$HOME/.pgcli/addon/rabbitmq/rmq/config/tls/ca.crt" \
  -verify_ip 127.0.0.1 -verify_return_error </dev/null

pw="$(pg addon password rabbitmq --name rmq)"
curl -fsS --cacert "$HOME/.pgcli/addon/rabbitmq/rmq/config/tls/ca.crt" \
  -u "admin:$pw" https://127.0.0.1:15671/api/overview
```

启用 TLS 后的 AMQP URL（例如 Python/pika 或 Go 客户端）：

```text
amqps://admin:<password>@127.0.0.1:5671/
```

- TLS 只负责**传输加密**。`ssl_options.verify = verify_none` 与
  `fail_if_no_peer_cert = false` 表示客户端不提供证书；任何能连到已启用端口且知道密码的人
  仍然可以访问。监听地址与密码才是安全边界。
- 叶子证书在到期前 30 天内（安装、启动或 `--force` 时）重新签发，CA 保留，因此客户端
  不需要换信任文件。该行为只适用于 pgcli 生成的证书。
- `--tls-port` 没配 `--tls` 会被拒绝。用 `--tls=false --force` 关闭 TLS；生成的文件保留在
  `config/tls/`，重新启用时复用。

### 自带证书（`pg cert`）

```bash
pg cert --host localhost,127.0.0.1,192.0.2.10 \
  --cert-file rmq.crt --key-file rmq.key

pg addon install rabbitmq --name rmq \
  --tls-cert rmq.crt --tls-key rmq.key
```

`--tls-cert` 与 `--tls-key` 必须成对传入，且隐含 `--tls`。在任何镜像拉取或容器操作之前，
pgcli 会校验：

- 证书可解析且当前有效；
- 允许 server authentication，且不是 CA 证书；
- 私钥与证书匹配且未加密；
- 证书覆盖具体的 `--listen` 地址（不覆盖是硬错误；可改用 `--listen 0.0.0.0` 或把该地址
  加入 SAN）。

文件会复制到实例的 `config/tls/`：证书为 `public.crt`、私钥为 `private.key`，证书副本同时
作为 `ca.crt`。`pg cert` 生成的自签叶子证书本身就是信任锚，客户端可直接信任 `rmq.crt`。
如果证书由其他 CA 签发，请改为信任该 CA 链：`ca.crt` 只包含你提供的文件，可能不包含根证书。

使用自带证书时，源文件不会被改动，pgcli 也不会自动续期——替换源文件后重新执行
`install --force`（或 `--tls=false --force` 回到明文）。

## 队列与消息

`queue list`、`purge`、`delete` 走容器内的 `rabbitmqctl`，不依赖 management HTTP 密码。
其余队列命令——`declare`、`info`、`publish`、`get`、`bind`、`unbind`、`bindings`——通过
management HTTP API 使用已保存的管理员凭据，要求实例正在运行且宿主能访问 management 端口。

```bash
# 声明与查看
pg addon rabbitmq queue list --name rmq
pg addon rabbitmq queue declare orders --name rmq
pg addon rabbitmq queue info orders --name rmq
pg addon rabbitmq queue info orders --name rmq --json

# 发布与消费
pg addon rabbitmq queue publish orders --payload '{"id":1}' --name rmq
echo '{"id":2}' | pg addon rabbitmq queue publish orders --payload-file - --name rmq
# 单机 classic 队列：让消息在 broker 重启后仍然保留
pg addon rabbitmq queue publish orders --payload '{"id":3}' --property delivery_mode=2 --name rmq
pg addon rabbitmq queue get orders --count 10             # peek：消息保留在队列
pg addon rabbitmq queue get orders --count 10 --consume   # ack：取出并删除
pg addon rabbitmq queue get orders --json

# 绑定（必须是真实 exchange；默认 exchange 不能显式绑定）
pg addon rabbitmq queue bind orders --exchange amq.topic --routing-key 'order.*' --name rmq
pg addon rabbitmq queue bindings orders --name rmq
pg addon rabbitmq queue unbind orders --exchange amq.topic --routing-key 'order.*' --name rmq

# 清理
pg addon rabbitmq queue purge orders --name rmq --yes
pg addon rabbitmq queue delete orders --name rmq --yes
pg addon rabbitmq queue delete orders --name rmq --if-empty --if-unused
```

- `list` 打印 `name`、`type`、`messages`、`ready`、`unacked`、`consumers`；空 vhost 打印
  `no queues`。
- `declare` 在单机实例上默认创建**持久化 classic** 队列，在集群成员上默认创建
  **持久化 quorum** 队列（见[集群](#集群)）。RabbitMQ 4.x 已移除 transient（非持久化且
  非 exclusive）队列，因此 `--durable=false` 会在发出请求前直接报错。`--type classic`
  或 `--type quorum` 可显式覆盖默认类型；quorum 队列必须持久化，且不能 auto-delete。
  可重复的 `--argument KEY=VALUE` 设置队列参数；能按 JSON 解析的值保留 JSON 类型（如
  `--argument x-max-priority=10`）。
- **消息保留时间。** `--message-ttl 30m`（或毫秒数）设置 `x-message-ttl`：在队列中等待超过
  该时间的消息会被丢弃；若队列配置了 `x-dead-letter-exchange` 则转死信。单条消息也可用
  `--property 'expiration="60000"'` 设置自己的 TTL。
- **自动删除。** `--auto-delete` 在最后一个消费者断开后删除队列。`--expires 24h` 设置
  `x-expires`：队列连续闲置该时长后，**队列本身连同其中剩余消息**一起删除。只有存在
  消费者、被重新声明、或通过 `basic.get` 访问才算“使用过”；**单纯发布消息不会重置
  计时器**。该值必须为正数（`0` 会被 RabbitMQ 拒绝）。两者也可通过 `--argument` 设置。
- `info` 显示 type、state、durable、auto-delete、消息计数、consumers、memory、policy、
  arguments 与 node；`--json` 输出 management API 原始文档。计数由 RabbitMQ 统计模块异步
  刷新，可能比 publish/get 滞后几秒。
- `publish` 发送一条消息。`--payload` 与 `--payload-file` 必须二选一（`-` 表示 stdin）。
  `--exchange` 默认 `amq.default`（默认 exchange，按 routing key 直接路由到同名队列）；
  `--routing-key` 默认队列名。可重复的 `--property KEY=VALUE` 设置消息属性，例如
  `--property delivery_mode=2 --property 'content_type="application/json"'`。消息未被路由时
  只打印警告，命令仍成功。
- **单机持久化。** 单机实例默认声明**持久化 classic** 队列（不复制），但 durable 只作用于
  队列本身，不作用于消息。**classic 的消息持久化是每条消息的属性，必须由发布端在每次
  publish 时设置**：命令行加 `--property delivery_mode=2`，应用客户端则在 AMQP 发布代码中
  设置 `delivery_mode=2`。队列参数或 policy 无法开启该行为（`--argument
  delivery_mode=2` 不生效）。未设置时消息是 transient，broker 重启（`stop`/`start`、
  容器重建或宿主机重启）后队列还在、消息丢失。设置后只要实例数据目录保留（不要
  `remove --clean-data`），消息即可恢复；磁盘损坏则无法恢复。集群的 quorum 队列不依赖
  delivery mode，所有消息都会持久化并复制。
- `get` 默认 peek（`ack_requeue_true`）；`--consume` 确认并删除取出的消息
  （`ack_requeue_false`）。`--count` 默认 1；`--json` 输出 API 原始响应。
- `bind` / `unbind` 必填 `--exchange`，并拒绝 `amq.default`（默认 exchange 是隐式绑定）。
  `--routing-key` 默认队列名。可重复的 `--argument` 支持基于参数（headers）的绑定。
  `unbind` 不带 `--argument` 时删除该 exchange 下所有同 routing key 的绑定；带
  `--argument` 时只删除参数完全一致的那条。
- `bindings` 列出队列的绑定及其参数（`--json` 输出原始文档）。
- `purge` 清空队列中的**消息**但保留队列；`delete` 删除队列本身。两者默认要求确认，
  `--yes` 可跳过。`--if-empty` / `--if-unused`（仅 delete）透传 RabbitMQ 的条件检查：队列
  非空或有消费者时删除失败，而不是直接删掉。
- 所有队列命令都接受 `--name` 与 `--vhost`（默认 `/`），并要求实例正在运行。

### 限制保留时间样例

不带保留参数的队列会**无限期保留消息**：默认没有 TTL。消息只会因为被消费、purge、
进入死信，或队列本身被删除而离开。常见限制方式：

```bash
# 队列级 TTL：发布后 24 小时过期
pg addon rabbitmq queue declare orders --message-ttl 24h --name rmq

# 单条消息 TTL：这条消息 60 秒后过期
# （expiration 是毫秒字符串属性）
pg addon rabbitmq queue publish orders --payload '{"id":1}' \
  --property 'expiration="60000"' --name rmq

# 队列闲置删除（不是消息 TTL）：连续 1 小时无消费者、未被重新声明、
# 未被 get 访问后，队列本身及其中消息一起删除
pg addon rabbitmq queue declare scratch --expires 1h --name rmq

# 按容量而非时间限制：最多保留 10000 条消息
pg addon rabbitmq queue declare backlog --argument x-max-length=10000 --name rmq

# 过期消息转入死信交换机，而不是直接丢弃
pg addon rabbitmq queue declare dlq-demo --message-ttl 24h \
  --argument x-dead-letter-exchange=dlx --name rmq
```

`--message-ttl` 与 `--expires` 只在队列首次声明时生效。要修改已存在队列的保留策略而不
删除队列，使用 policy：

```bash
pg addon rabbitmq shell --name rmq
rabbitmqctl set_policy orders-ttl '^orders$' \
  '{"message-ttl":86400000,"max-length":10000}' --apply-to queues
rabbitmqctl list_policies
```

policy 按队列名正则匹配，对新建和已有队列都生效，并优先于同名的 `x-*` 队列参数。也可以
在 management UI 的 **Admin → Policies** 中创建。

同样的保留限制也可以不重新声明队列，直接通过 RabbitMQ **policy**（`message-ttl`、
`expires`、`max-length`、`max-length-bytes`）施加；pgcli 暂未封装 policy，请使用
management UI 或 `rabbitmqctl set_policy`。

`pg addon rabbitmq shell` 打开容器内的 `/bin/bash`；`rabbitmqctl` 与
`rabbitmq-diagnostics` 都在里面，可处理 CLI 和 API 未覆盖的操作。

## 状态与生命周期

```bash
pg addon rabbitmq status --name rmq       # 状态、端点、TLS、管理员、EPMD/dist、路径
pg addon start rabbitmq --name rmq        # 启动（缺失则创建），等待就绪
pg addon stop rabbitmq --name rmq
pg addon remove rabbitmq --name rmq       # 删除容器与 pg.yaml 条目，保留数据/配置/secret
pg addon remove rabbitmq --name rmq --clean-data   # 同时删除实例目录与自定义 data dir
pg logs addon rabbitmq --name rmq -f
```

`install`、`start` 与 `--force` 在单机实例上最多等待 **90 秒**，轮询
`rabbitmqctl -q await_startup`；集群成员最多等待 **180 秒**，覆盖首次 peer discovery
与 Mnesia 引导。集群模式下每次尝试上限 10 秒——有 peer 时该命令可能已执行成功却静默
不退出；若被空闲 watchdog 终止，pgcli 改为探测 AMQP 端口，监听器在启动末尾打开，
端口可连即视为就绪。就绪后 pgcli 用
`rabbitmqctl change_password` / `add_user` / `set_user_tags administrator` /
`set_permissions` 对账管理员账号。

集群模式下 `status` 还会打印集群名、Erlang 节点名与 etcd endpoint；
`pg addon rabbitmq cluster status` 展示 `rabbitmqctl cluster_status` 的实时视图。

不带 `--clean-data` 的 `remove` 会保留数据目录、生成的配置和 `0600` 密码 secret。同名重装会
复用该密码，因此 remove/reinstall 之后客户端配置无需修改。

开机自启：

```bash
pg autostart enable --rabbitmq --name rmq
pg autostart status
pg autostart disable --rabbitmq --name rmq
```

启动时会重写 `rabbitmq.conf`、启动容器并重新对账管理员账号。`pg start` 也会带上已启用
autostart 的 RabbitMQ 实例。

## pgcli 创建的内容

单个实例的全部文件位于 `<base_dir>/addon/rabbitmq/<name>/`：

| 宿主路径 | 容器路径 | 说明 |
|---|---|---|
| `config/rabbitmq.conf` | `/etc/rabbitmq/rabbitmq.conf`（只读） | 渲染后的监听、management、管理员 tags/权限与 distribution 配置 |
| `config/enabled_plugins` | `/etc/rabbitmq/enabled_plugins`（只读） | 单机 `[rabbitmq_management].`；集群 `[rabbitmq_management,rabbitmq_peer_discovery_etcd].` |
| `config/tls/` | `/etc/rabbitmq/tls`（只读，仅 `--tls`） | `ca.crt`（0644）、`public.crt`（含链，0644）、`private.key`（0600，chown 给 uid 999）；`ca.key` 留在宿主侧且不挂载 |
| `data/` | `/var/lib/rabbitmq` | Mnesia、消息存储、definitions 与日志；集群模式额外包含 `.erlang.cookie`（0400，chown 给 uid 999） |
| `secrets/admin_password` | 不挂载 | `0600`，重装时复用 |

明文实例生成的 `rabbitmq.conf`：

```ini
listeners.tcp.default = 127.0.0.1:5672

management.tcp.ip = 127.0.0.1
management.tcp.port = 15672

default_user_tags.administrator = true
default_permissions.configure = .*
default_permissions.read = .*
default_permissions.write = .*

distribution.listener.interface = 127.0.0.1
distribution.listener.port_range.min = 25672
distribution.listener.port_range.max = 25672
```

`--tls` 时会额外写入 `listeners.ssl.default`、`ssl_options.*` 与 `management.ssl.*`。
集群成员改为绑定 `0.0.0.0`（或显式 `--listen` 地址），并追加发现配置：

```ini
cluster_formation.peer_discovery_backend = etcd
cluster_formation.etcd.endpoints.1 = 192.0.2.10:2379
cluster_formation.etcd.cluster_name = pgcli-lab
```

任何 secret 都不会写进 `rabbitmq.conf`。管理员密码通过容器环境变量传入
（`RABBITMQ_DEFAULT_USER` / `RABBITMQ_DEFAULT_PASS` 只用于播种空的 Mnesia，之后 pgcli
每次启动用 `rabbitmqctl` 对账），`RABBITMQ_NODENAME` 与 `RABBITMQ_USE_LONGNAME` 同样走
环境变量。集群模式下 pgcli 还会在容器启动**之前**把共享 Erlang cookie 写入
`data/.erlang.cookie`（权限 `0400`，属主 uid 999）：官方 4.4 镜像忽略
`RABBITMQ_ERLANG_COOKIE`，任其自行生成随机 cookie 的节点无法与 peer 完成 distribution
握手。

容器以 `--network host`、`--restart unless-stopped` 和 30 秒 stop timeout 运行，身份是镜像的
**uid/gid 999（`rabbitmq`）**；pgcli 只把 TLS 私钥与集群 cookie 文件交给该 uid（rootless 下
通过 `podman unshare`，root 下用 `os.Chown`）。数据目录由镜像入口脚本自行 chown。

## 配置覆盖

安装时可追加 `rabbitmq.conf` 条目：

```bash
pg addon install rabbitmq --name rmq --force \
  --config-option heartbeat=60 \
  --config-option log.file.level=debug
```

`--config-option` **替换**已存储的覆盖表（不合并）。覆盖项保存在 `pg.yaml` 的
`addons.rabbitmq.<name>.settings` 下，每次启动重新渲染。键可包含字母、数字、点、下划线和
连字符；值不能包含 NUL、CR 或 LF。

pgcli 托管的键会被拒绝而不是被静默覆盖：`listeners.*`、`ssl_options.*`、`management.*`、
`prometheus.*`、`cluster_formation.*`、`default_user`、`default_pass`、
`default_user_tags.*`、`default_permissions.*`、`loopback_users.*`、
`distribution.listener.*`。

## 密码与账号语义

- `RABBITMQ_DEFAULT_USER` / `RABBITMQ_DEFAULT_PASS` 只在首次启动、Mnesia 为空时**播种**。
  因此 pgcli 每次启动都用 `rabbitmqctl change_password` 对账（用户不存在时退回
  `add_user`），再设置 `administrator` tag 以及 vhost `/` 上的 `.* / .* / .*` 权限。
- 在已有实例上修改 `--admin-user` 会创建/对账新账号，但**不会删除旧账号**。请通过
  management UI 或 `rabbitmqctl delete_user` 清理。
- 修改 `--admin-password`（或复用已保存的 secret）会在下一次 `install --force` 或 `start`
  时生效。集群中的密码是共享的：所有成员必须保存相同的值，轮换时要在所有成员上同步操作。
- 集群模式的共享密钥来自 `--secrets-from-etcd`（推荐，key 由
  `pg addon rabbitmq secret generate` 写入），或来自 `--admin-password-file` 与
  `--erlang-cookie-file`。明文 `--admin-password` 被拒绝。两个值随后都会持久化到
  `pg.yaml` 与 `0600` secret 文件。
- 密码同时保存在 `pg.yaml` 与 `0600` secret 文件中；
  `pg addon password rabbitmq --name NAME` 打印存储值。

## 生产注意事项

- **单节点或一个集群成员。** 单机实例没有 HA：容器停止即 broker 不可用。集群模式的可用性
  来自跨成员复制的 **quorum 队列**；classic 队列不复制，会随所在节点一起不可用。不配置
  federation、shovel、MQTT 或 STOMP。
- **集群网络。** `4369`（EPMD）与 `25672`（Erlang distribution）只在私有网络中、仅对成员
  开放。即使启用 `--tls`，distribution 流量也不加密；TLS 只覆盖客户端监听。一台主机只能有
  一个集群实例。
- **发现链路是明文的。** etcd endpoint 通过明文 HTTP 访问，没有认证。任何能访问 etcd 的人
  都能往集群注册恶意节点，或读取 `pg addon rabbitmq secret generate` 写入的共享密钥；
  请把 etcd 隔离在可信网络中。本版本不支持 etcd TLS/认证。
- **不启用 Prometheus 插件。** `enabled_plugins` 固定为 management（集群模式另加 etcd
  发现插件）。需要 metrics 时可通过 management API 采集，或自行在容器内启用插件——但要
  注意多个实例会争抢同一个默认 metrics 端口。
- **TLS 不是客户端认证。** 请使用 `--listen 127.0.0.1`，或在 broker 前加私有网络/防火墙。
  Management UI 是完整的管理面，不要随意暴露。
- **TLS 是增量监听。** 启用 `--tls` 后明文 `amqp://` 与 `http://` 仍然可用；需要加密的
  客户端必须显式选择 `amqps://` / `https://` 端点。
- **升级。** `--force --version <new>` 会拉取新 tag 并在原数据目录上重建容器。RabbitMQ 不保证
  能被更新版本写入的数据可降级——升级前请先做离线拷贝。`--version` 必须是真实存在的上游 tag。
- **备份。** 停止容器后复制数据目录，或用 management API 导出 users/vhosts/policies 的
  definitions。本版本没有 `pg addon rabbitmq backup` 命令。
- **数据属主。** 数据文件属 uid/gid 999。请用 `--clean-data` 删除，而不是手工 `rm`；pgcli
  会通过 `podman unshare` 处理权限回退。
- **平台。** Linux（host 网络）；官方镜像为多架构（amd64/arm64）。rootful 宿主上需以 root
  运行 pgcli 才能把 TLS 私钥交给 uid 999；rootless 下走 `podman unshare`。

## CLI 参数参考

### `pg addon install rabbitmq`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `rabbitmq` | 实例名；`[A-Za-z0-9][A-Za-z0-9_-]*`，最长 48 字符 |
| `--version TAG` | `4.4.0` | 上游 `docker.io/library/rabbitmq` tag；自动追加 `-management` 后缀 |
| `--admin-user USER` | `admin` | 管理员账号；`[A-Za-z0-9_.-]{1,64}` |
| `--admin-password PASS` | 自动生成 | 管理员密码（不能含 NUL/CR/LF）；集群模式拒绝 |
| `--admin-password-file FILE` | — | 从文件读取密码；集群成员首次安装必填（使用 `--secrets-from-etcd` 时除外） |
| `--data-dir DIR` | `<base_dir>/addon/rabbitmq/<name>/data` | 绝对宿主路径，挂载到 `/var/lib/rabbitmq` |
| `--listen ADDR` | `127.0.0.1`（集群 `0.0.0.0`） | AMQP、AMQPS、management HTTP 与 management HTTPS 的绑定地址；集群模式不能是 loopback |
| `--cluster NAME` | — | 集群名；空 = 单机 |
| `--node-name NODE@HOST` | — | 本成员的 Erlang 节点名；首次安装必填 |
| `--etcd M1,M2` | — | 用本地 etcd addon 成员做发现 |
| `--etcd-endpoints HOST:PORT,...` | — | 外部 etcd 发现 endpoint；优先于 `--etcd` |
| `--erlang-cookie-file FILE` | — | 从文件读取共享 Erlang cookie；集群成员首次安装必填（使用 `--secrets-from-etcd` 时除外） |
| `--secrets-from-etcd` | false | 从 `secret generate` 写入的 etcd key 读取两个共享密钥；不能与两个 `--*-file` 同时使用 |
| `--amqp-port N` | 自动（基准 `5672`） | 明文 AMQP 端口；`0` = 自动分配 |
| `--management-port N` | 自动（基准 `15672`） | Management HTTP 端口；`0` = 自动分配 |
| `--tls` | false | 增加 AMQPS 与 management HTTPS；明文监听保留 |
| `--tls-cert FILE` | — | 自带证书链（PEM）；必须与 `--tls-key` 配对；隐含 `--tls` |
| `--tls-key FILE` | — | 自带私钥；必须与 `--tls-cert` 配对 |
| `--tls-port N` | 自动（基准 `5671`） | AMQPS 端口；`0` = 自动分配；需要 `--tls` |
| `--config-option K=V` | — | 额外的 `rabbitmq.conf` 配置；可重复；替换已存储覆盖表 |
| `--force` | false | 重写配置并重建容器，保留数据与密码；cluster/node/cookie 不可变 |

### `pg addon rabbitmq status` / `shell` / `cluster ...`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `status --name NAME` | `rabbitmq` | 显示状态、端点、TLS、管理员、EPMD/dist、路径；集群模式附集群名/节点/etcd |
| `shell --name NAME` | `rabbitmq` | 打开容器内 `/bin/bash` |
| `cluster status --name NAME` | `rabbitmq` | 输出 `rabbitmqctl cluster_status` |
| `cluster forget NODE --name NAME [--yes]` | — | forget 一个已停止并下线的成员；拒绝本机节点，除非 `--yes` 否则要求确认 |

### `pg addon rabbitmq secret generate`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--cluster NAME` | — | 要发布密钥的集群名（必填） |
| `--etcd M1,M2` | — | 保存密钥的本地 etcd addon 成员 |
| `--etcd-endpoints HOST:PORT,...` | — | 保存密钥的外部 etcd endpoint；优先于 `--etcd` |
| `--cookie-file FILE` | `/tmp/rmq.cookie` | Erlang cookie 的本地副本（`0600`）；仅集群节点使用，客户端不需要 |
| `--admin-password-file FILE` | `/tmp/rmq.admin` | 管理员密码的本地副本（`0600`）；安装输入，不是客户端凭据 |
| `--force` | false | 覆盖已存在的文件与 etcd key |
| `--keep-cookie` | false | 与 `--force` 连用：保留现有 Erlang cookie，只轮换管理员密码（成员已安装后使用） |

### `pg addon rabbitmq queue ...`

所有队列子命令都接受 `--name NAME`（默认 `rabbitmq`）与 `--vhost VHOST`（默认 `/`）。

| 命令 | 参数 | 默认值 | 说明 |
|---|---|---|---|
| `list` | — | — | 列出队列及消息/消费者计数 |
| `declare QUEUE` | `--durable` | true | 声明持久化队列（RabbitMQ 4.x 拒绝 `false`） |
| | `--auto-delete` | false | 最后一个消费者断开后删除队列 |
| | `--type TYPE` | 单机 `classic`，集群 `quorum` | `classic` 或 `quorum` |
| | `--message-ttl DURATION` | — | 设置 `x-message-ttl`（如 `30m`、`24h` 或毫秒） |
| | `--expires DURATION` | — | 设置 `x-expires`：队列闲置该时长后删除（必须 > 0） |
| | `--argument K=V` | — | 额外队列参数；可重复；JSON 值保留类型 |
| `info QUEUE` | `--json` | false | 输出 management API 原始文档 |
| `publish QUEUE` | `--payload TEXT` | — | 消息体（与 `--payload-file` 互斥） |
| | `--payload-file FILE` | — | 从文件读取消息体；`-` 表示 stdin |
| | `--exchange NAME` | `amq.default` | 目标 exchange |
| | `--routing-key KEY` | 队列名 | routing key |
| | `--property K=V` | — | 消息属性；可重复；单机 classic 消息必须在每次 publish 时设置 `delivery_mode=2` 才能跨 broker 重启保留（不存在队列级开关）；JSON 值保留类型 |
| `get QUEUE` | `--count N` | 1 | 最多取出的消息数 |
| | `--consume` | false | ack 并删除消息（默认 peek 后重新入队） |
| | `--json` | false | 输出 management API 原始响应 |
| `bind QUEUE` | `--exchange NAME` | — | 源 exchange（必填；拒绝 `amq.default`） |
| | `--routing-key KEY` | 队列名 | routing key |
| | `--argument K=V` | — | 绑定参数；可重复 |
| `unbind QUEUE` | `--exchange NAME` | — | 源 exchange（必填） |
| | `--routing-key KEY` | 队列名 | routing key |
| | `--argument K=V` | — | 仅删除参数完全一致的绑定；省略则删除所有匹配项 |
| `bindings QUEUE` | `--json` | false | 输出 management API 原始文档 |
| `purge QUEUE` | `--yes` | false | 跳过确认提示 |
| `delete QUEUE` | `--yes` | false | 跳过确认提示 |
| | `--if-empty` | false | 队列非空时失败而不是删除 |
| | `--if-unused` | false | 队列有消费者时失败而不是删除 |

### 生命周期、日志、autostart

| 命令 | 参数 | 说明 |
|---|---|---|
| `pg addon start rabbitmq` | `--name NAME` | 启动（缺失则创建），等待就绪并对账管理员 |
| `pg addon stop rabbitmq` | `--name NAME` | 停止容器，保留数据 |
| `pg addon remove rabbitmq` | `--name NAME`、`--clean-data` | 删除容器与配置条目；`--clean-data` 同时删除实例目录与自定义 data dir |
| `pg logs addon rabbitmq` | `--name NAME`、`-n N`（默认 50）、`-f` | 容器日志 |
| `pg autostart enable\|disable` | `--rabbitmq`、`--name NAME` | 切换单个实例的开机自启 |
| `pg addon password rabbitmq` | `--name NAME` | 打印存储的管理员密码 |

### 全局参数

| 参数 | 说明 |
|---|---|
| `-c, --config FILE` | 配置文件（默认 `~/.pgcli/pg.yaml`） |
| `-o, --output FILE` | 输出配置路径（默认与输入相同） |
| `-i, --instance NAME` | PostgreSQL 实例选择器——RabbitMQ 不使用，请用 `--name` |
