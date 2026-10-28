---
title: "RabbitMQ"
description: "以托管插件方式运行单节点 RabbitMQ broker——AMQP 与 management Web UI/API、可选 TLS（自动生成 CA 或自带证书）、通过 rabbitmqctl 管理队列 list/purge/delete"
weight: 66
---

`pg addon install rabbitmq` 以托管容器方式运行 **RabbitMQ 4.4.0**，直接使用官方
`docker.io/library/rabbitmq` 的 `-management` 镜像（内含 Web UI 与 HTTP API）。和
Kafka、ClickHouse 一样，RabbitMQ 是**顶层插件**：pgcli 负责渲染 `rabbitmq.conf`、管理
管理员账号、绑定挂载数据目录，并以 host 网络启动容器。

本插件刻意只做**单节点**：不配置集群、federation、shovel、MQTT 或 STOMP；vhost、用户、
策略、exchange、binding 等高级管理通过 management UI 或 `pg addon rabbitmq shell` 里的
`rabbitmqctl` 完成。

> **认证始终开启。** AMQP 与 management UI 都需要 pgcli 创建的管理员账号（默认
> `admin`，20 位自动生成密码）。`--listen` 默认 `127.0.0.1`。对外暴露时，密码就是安全
> 边界：TLS 只加密传输，**不认证客户端**（`verify_none`、`fail_if_no_peer_cert = false`）。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `rabbitmq` | 实例名（map key，也是子命令默认名字） |
| `--version` | `4.4.0` | 上游镜像 tag，实际拉取 `docker.io/library/rabbitmq:<version>-management` |
| `--admin-user` | `admin` | 管理员账号，首启创建，每次启动对账 |
| `--admin-password` | 自动生成（20 位） | 管理员密码；同时存入 `pg.yaml` 与 `0600` secret 文件 |
| `--data-dir` | `<base_dir>/addon/rabbitmq/<name>/data` | 数据目录，挂载到 `/var/lib/rabbitmq`；必须是绝对路径 |
| `--listen` | `127.0.0.1` | AMQP、AMQPS（启用 `--tls` 时）与 management 监听共用的绑定地址。`0.0.0.0` 会暴露所有已启用监听 |
| `--amqp-port` | 自动（基准 `5672`） | 明文 AMQP 端口；`0` = 从 `rabbitmq_start_port` 自动分配 |
| `--management-port` | 自动（基准 `15672`） | Management HTTP UI/API 端口；`0` = 从 `rabbitmq_management_start_port` 自动分配 |
| `--tls` | false | 在明文监听之外增加 AMQPS 与 management HTTPS，使用 pgcli 自签 CA 签发的证书 |
| `--tls-cert FILE` | — | 自带服务端证书（PEM 链）；必须与 `--tls-key` 同时传入；隐含 `--tls`；`--listen` 不是 `0.0.0.0` 时必须覆盖该地址 |
| `--tls-key FILE` | — | 自带私钥（PKCS#8/PKCS#1/SEC1），与 `--tls-cert` 配对 |
| `--tls-port` | 自动（基准 `5671`） | AMQPS 端口；`0` = 从 `rabbitmq_tls_start_port` 自动分配；需要 `--tls` |
| `--config-option K=V` | — | 额外的 `rabbitmq.conf` 配置（`heartbeat=60`、`log.file.level=debug`）；可重复；**替换**已存储的覆盖表 |
| `--force` | false | 重新渲染配置并重建容器；保留数据与已存储的密码 |

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

浏览器打开 `http://<listen>:<management-port>`，用管理员账号登录。同一端口也提供 HTTP API：

```bash
pw="$(pg addon password rabbitmq --name rmq)"
curl -fsS -u "admin:$pw" http://127.0.0.1:15672/api/overview
```

CLI 未封装的能力——vhost、用户、权限、策略、exchange、binding 以及逐队列检查——都通过
UI 完成。

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

EPMD 与 distribution 是基础设施端口，不是客户端端点。每个实例都以
`RABBITMQ_NODENAME=rabbit@localhost`、`ERL_EPMD_ADDRESS=127.0.0.1` 和独立的
EPMD/distribution 端口运行，`rabbitmq.conf` 里固定
`distribution.listener.interface = 127.0.0.1`——Erlang distribution 永不对外暴露，多个实例
可在同一宿主共存，也不会与宿主的 Erlang 安装冲突。

**不启用 `rabbitmq_prometheus` 插件。** pgcli 挂载自己的 `enabled_plugins`（只有
`rabbitmq_management`），保证每个实例行为一致，也不会多开一个无认证的固定 metrics 监听。

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

## 队列

队列管理走容器内的 `rabbitmqctl`，不依赖 management HTTP 密码，即使 UI 端口被防火墙挡住
也能用。

```bash
pg addon rabbitmq queue list --name rmq
pg addon rabbitmq queue list --name rmq --vhost orders
pg addon rabbitmq queue purge orders --name rmq --vhost orders --yes
pg addon rabbitmq queue delete orders --name rmq --yes
pg addon rabbitmq queue delete orders --name rmq --if-empty --if-unused
```

- `list` 打印 `name`、`type`、`messages`、`ready`、`unacked`、`consumers`。空 vhost 打印
  `no queues`。
- `purge` 清空队列中的**消息**但保留队列；`delete` 删除队列本身。两者默认要求确认，`--yes`
  可跳过。
- `--if-empty` / `--if-unused`（仅 delete）透传 RabbitMQ 的条件检查：队列非空或有消费者时
  删除失败，而不是直接删掉。
- `--vhost` 默认 `/`。
- 所有队列命令都要求实例正在运行。

声明队列或发送测试消息可用 management UI（`Queues → Add a new queue`）或 HTTP API：

```bash
pw="$(pg addon password rabbitmq --name rmq)"
curl -fsS -u "admin:$pw" -X PUT http://127.0.0.1:15672/api/queues/%2F/demo \
  -H 'content-type: application/json' \
  -d '{"durable":true}'
curl -fsS -u "admin:$pw" -X POST http://127.0.0.1:15672/api/exchanges/%2F/amq.default/publish \
  -H 'content-type: application/json' \
  -d '{"properties":{},"routing_key":"demo","payload":"hello","payload_encoding":"string"}'
```

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

`install`、`start` 与 `--force` 最多等待 **90 秒**，轮询
`rabbitmq-diagnostics -q ping`。就绪后 pgcli 用 `rabbitmqctl change_password` /
`add_user` / `set_user_tags administrator` / `set_permissions` 对账管理员账号。

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
| `config/enabled_plugins` | `/etc/rabbitmq/enabled_plugins`（只读） | 固定为 `[rabbitmq_management].` |
| `config/tls/` | `/etc/rabbitmq/tls`（只读，仅 `--tls`） | `ca.crt`（0644）、`public.crt`（含链，0644）、`private.key`（0600，chown 给 uid 999）；`ca.key` 留在宿主侧且不挂载 |
| `data/` | `/var/lib/rabbitmq` | Mnesia、消息存储、definitions 与日志 |
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
管理员密码**不会**写进配置文件：容器通过 `RABBITMQ_DEFAULT_USER` /
`RABBITMQ_DEFAULT_PASS` 获取，并且每次启动由 pgcli 用 `rabbitmqctl` 对账。

容器以 `--network host`、`--restart unless-stopped` 和 30 秒 stop timeout 运行，身份是镜像的
**uid/gid 999（`rabbitmq`）**；pgcli 只把 TLS 私钥交给该 uid（rootless 下通过
`podman unshare`，root 下用 `os.Chown`）。数据目录由镜像入口脚本自行 chown。

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
`prometheus.*`、`default_user`、`default_pass`、`default_user_tags.*`、
`default_permissions.*`、`loopback_users.*`、`distribution.listener.*`。

## 密码与账号语义

- `RABBITMQ_DEFAULT_USER` / `RABBITMQ_DEFAULT_PASS` 只在首次启动、Mnesia 为空时**播种**。
  因此 pgcli 每次启动都用 `rabbitmqctl change_password` 对账（用户不存在时退回
  `add_user`），再设置 `administrator` tag 以及 vhost `/` 上的 `.* / .* / .*` 权限。
- 在已有实例上修改 `--admin-user` 会创建/对账新账号，但**不会删除旧账号**。请通过
  management UI 或 `rabbitmqctl delete_user` 清理。
- 修改 `--admin-password`（或复用已保存的 secret）会在下一次 `install --force` 或 `start`
  时生效。
- 密码同时保存在 `pg.yaml` 与 `0600` secret 文件中；
  `pg addon password rabbitmq --name NAME` 打印存储值。

## 生产注意事项

- **仅单节点。** 本插件不支持集群、镜像/经典队列 HA 复制、federation、shovel、MQTT 或
  STOMP。容器停止即 broker 不可用。
- **不启用 Prometheus 插件。** `enabled_plugins` 固定为 management。需要 metrics 时可通过
  management API 采集，或自行在容器内启用插件——但要注意多个实例会争抢同一个默认 metrics
  端口。
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
| `--admin-password PASS` | 自动生成 | 管理员密码（不能含 NUL/CR/LF） |
| `--data-dir DIR` | `<base_dir>/addon/rabbitmq/<name>/data` | 绝对宿主路径，挂载到 `/var/lib/rabbitmq` |
| `--listen ADDR` | `127.0.0.1` | AMQP、AMQPS、management HTTP 与 management HTTPS 的绑定地址 |
| `--amqp-port N` | 自动（基准 `5672`） | 明文 AMQP 端口；`0` = 自动分配 |
| `--management-port N` | 自动（基准 `15672`） | Management HTTP 端口；`0` = 自动分配 |
| `--tls` | false | 增加 AMQPS 与 management HTTPS；明文监听保留 |
| `--tls-cert FILE` | — | 自带证书链（PEM）；必须与 `--tls-key` 配对；隐含 `--tls` |
| `--tls-key FILE` | — | 自带私钥；必须与 `--tls-cert` 配对 |
| `--tls-port N` | 自动（基准 `5671`） | AMQPS 端口；`0` = 自动分配；需要 `--tls` |
| `--config-option K=V` | — | 额外的 `rabbitmq.conf` 配置；可重复；替换已存储覆盖表 |
| `--force` | false | 重写配置并重建容器，保留数据与密码 |

### `pg addon rabbitmq status` / `shell`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name NAME` | `rabbitmq` | 目标实例 |
| `-- COMMAND...` | `/bin/bash` | 仅 `shell`：执行命令而不是打开交互 shell |

### `pg addon rabbitmq queue list|purge|delete`

| 参数 | 默认值 | 说明 |
|---|---|---|
| `QUEUE` | — | 队列名（`purge`/`delete`，位置参数） |
| `--name NAME` | `rabbitmq` | 目标实例 |
| `--vhost VHOST` | `/` | 虚拟主机 |
| `--yes` | false | 跳过确认提示（`purge`/`delete`） |
| `--if-empty` | false | 仅 `delete`：队列非空时失败而不是删除 |
| `--if-unused` | false | 仅 `delete`：队列有消费者时失败而不是删除 |

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
