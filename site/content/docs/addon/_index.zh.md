---
title: "插件 (Addons)"
description: "pgcli 插件管理指南"
weight: 70
icon: fa-solid fa-puzzle-piece
menus:
  main:
    identifier: docs-addon
    parent: docs
    weight: 70
    params:
      icon: fa-solid fa-puzzle-piece
cascade:
  type: docs
  footer_style: slim
---

pgcli 支持通过插件系统扩展 PostgreSQL 功能。插件是独立的容器，为 PostgreSQL
实例提供额外能力，无需修改数据库本身。

## 支持的插件

目前支持以下插件：

| 插件 | 说明 |
|------|------|
| [`pgbouncer`](./pgbouncer/) | 连接池管理器，提供事务级连接池化 |
| [`etcd`](./etcd/) | 分布式键值存储——独立运行、可组集群，用于 HA / DCS |
| [`pgdog`](./pgdog/) | Postgres 代理——连接池化、负载均衡与分片 |
| [`postgrest`](./postgrest/) | 将 PostgreSQL schema 暴露为 REST API——单容器无状态，本地或远程（任意 PG 端点） |
| [`haproxy`](./haproxy/) | Patroni 集群前的 TCP 负载均衡——读写一体或读写分离，也可用自己的 `haproxy.cfg` 做任意原生代理（仅 Linux） |
| [`minio`](./minio/) | S3 兼容对象存储，含 Web 控制台——单机或跨主机分布式纠删码集群（Linux 与 macOS） |
| [`silo`](./silo/) | S3 兼容对象存储（Pigsty 的 MinIO 分支）——功能面与 minio 插件一致，共用同一端口池；自带 `mcli` 客户端（Linux 与 macOS） |
| [`rustfs`](./rustfs/) | S3 兼容对象存储（Rust 重新实现）——SNSD/SNMD/MNMD 纠删码布局，固定的容器 uid 由 pgcli 定制镜像在内部消化，共用同一端口池（仅 Linux） |
| [`garage`](./garage/) | S3 兼容对象存储（Deuxfleurs 的 Rust 实现）——v1 单节点，渲染 `garage.toml`，两个数据目录，四个端口取自自己的池，仅明文 HTTP（TLS 需 nginx 前置）（Linux；macOS 代码完备、未实测） |
| [`seaweedfs`](./seaweedfs/) | S3 兼容对象存储（Go 实现，以 volume 粒度管盘）——单节点 `weed mini`，或跨主机 `weed server` 集群（filer 元数据放共享 PostgreSQL），全集群共用同一对凭据，原生 S3 TLS；独立端口池（Linux；macOS 代码完备、未实测） |
| [`redis`](./redis/) | 独立键值存储，用于缓存/会话/排行榜/原子计数——大版本 7/8 可选，自动生成 `requirepass`，RDB 持久化，读副本，`--cluster` 原生集群分片，独立的端口池（Linux；macOS 代码完备、未实测） |
| [`predixy`](./predixy/) | Redis 协议代理——把原生集群变成一个普通的 `redis://` 端点，客户端完全无需感知集群；独立端口池（仅 Linux） |
| [`pgadmin`](./pgadmin/) | pgAdmin 4——PostgreSQL 官方 Web 管理界面，单容器直接跑上游镜像；Web 登录自动生成，可选经 `--dsn`/`--pg-name` 一次性预置服务器，持久化会话存储；独立端口池（Linux；macOS 代码完备、未实测） |
| [`nginx`](./nginx/) | HTTP 反向代理，前置多个 Web 服务——按声明的后端做基于路径的路由，可选 TLS 终止，渲染 `nginx.conf` 或用自己的 `--conf-file`，配置变更后 `pg addon nginx reload`；独立端口池（Linux 与 macOS） |
| [`grafana`](./grafana/) | Grafana——开源可观测性面板，单容器直接跑官方上游镜像；Web 登录自动生成，`Listen` 默认 `0.0.0.0`（关闭自助注册），v1 只经环境变量（`GF_*`）配置、无 provisioning；独立端口池（Linux；macOS 代码完备、未实测） |
| [`prometheus`](./prometheus/) | Prometheus——Grafana 读取的指标采集后端，单容器直接跑官方 `docker.io/prom/prometheus` 镜像；渲染 `prometheus.yml`（自带自监控 job + 每个 `--scrape` JOB 一个 job，该 job 声明的 target 全部并入其中；`--scrape-append` 增量合并；`--alertmanager-url` 转发已触发告警并附带对应的抓取 job；`--rule-file` 拷贝并加载告警规则）或用自己的 `--conf-file`，`pg addon prometheus reload` 经 SIGHUP 热加载；**无 Web 登录**——`Listen` 默认 `0.0.0.0` 仅靠网络边界保护（仅本机访问用 `--listen 127.0.0.1`）；独立端口池（Linux；macOS 代码完备、未实测） |
| [`alertmanager`](./alertmanager/) | Alertmanager——Prometheus 体系的告警去重/分组/路由端，单容器直接跑官方 `docker.io/prom/alertmanager` 镜像；渲染 `alertmanager.yml`（一条最小路由指向 no-op 或 `--webhook-url` 的 `web.hook` receiver）或用自己的 `--conf-file`，`pg addon alertmanager reload` 经 SIGHUP 热加载；HA 集群 gossip 已禁用（`9094` 永不开）；**无 Web 登录**——`Listen` 默认 `0.0.0.0` 仅靠网络边界保护，而可触达即意味着任何人能创建 silence（静音 on-call），仅本机访问用 `--listen 127.0.0.1`；与 Prometheus 的衔接在 Prometheus 侧完成（`pg addon install prometheus --alertmanager-url`）；独立端口池（Linux；macOS 代码完备、未实测） |
| [`victoriametrics`](./victoriametrics/) | VictoriaMetrics——高性能、兼容 Prometheus 的时序数据库，单容器直接跑官方 `docker.io/victoriametrics/victoria-metrics` 镜像；**无需配置文件**（一切经 CLI flag：`-storageDataPath` / `-httpListenAddr` / `-retentionPeriod` / `--extra-arg`），无热重载（变更需重建容器），镜像以 root 运行（无需预 chown）；**无 Web 登录**——`Listen` 默认 `0.0.0.0` 仅靠网络边界保护（仅本机访问用 `--listen 127.0.0.1`）；独立端口池（Linux；macOS 代码完备、未实测） |
| [`victorialogs`](./victorialogs/) | VictoriaLogs——VictoriaMetrics 家族的日志数据库，单容器直接跑官方 `docker.io/victoriametrics/victoria-logs` 镜像；单 HTTP 端口同时提供摄入（JSON Lines / Elasticsearch bulk / syslog / OTLP）、LogsQL 查询与内置 UI；**无需配置文件**（一切经 CLI flag：`-storageDataPath` / `-httpListenAddr` / `-retentionPeriod` / `--extra-arg`），`--retention` 接受时长（`30d`、`3months` 等），默认省略以保持二进制自身默认；**无 Web 登录**——`Listen` 默认 `0.0.0.0` 仅靠网络边界保护（仅本机访问用 `--listen 127.0.0.1`）；独立端口池，默认 9428（Linux；macOS 代码完备、未实测） |
| [`influxdb`](./influxdb/) | InfluxDB——经典 1.x 时序数据库（TSM 存储、InfluxQL），单容器直接跑 pgcli 自建的 `ghcr.io/mars-base/pgcli/pgcli-influxdb` 镜像（官方 1.13.1 linux/amd64 发行版二进制 + Alpine）；**认证始终开启**（安装时生成 admin 密码，`pg addon password influxdb` 取回），最小化渲染的 `influxdb.conf` 只覆盖路径/监听/认证，其余全部沿用上游默认值，任意持久化覆盖经 `--config-option` 或 `pg addon influxdb config set/unset`；一个容器加每个实例唯一的**仅回环**备份/恢复 RPC 端口（上游 RPC 无认证）；在线 portable `backup`/`restore`（`--newdb` 可并存恢复）、离线 `export`（`influx_inspect`，会先停容器，除非 `--no-start` 否则结束后重启）、`import` 自动改写 `# CONTEXT-DATABASE:`；顶层 **`pg influx` 透传**用一次性容器运行 `influx` CLI，可操作本地 addon 或任意**远程** InfluxDB 1.x（`--host`/`--dsn`、TLS flags、`INFLUX_USERNAME`/`INFLUX_PASSWORD`，`-import -path FILE` 会挂载进容器）；没有 HTTPS 监听（用 nginx 前置）、无 HA、v1 处于维护模式；独立端口池（仅 Linux/amd64） |
| [`influx2tsdb`](./influx2tsdb/) | influx2tsdb——在 PostgreSQL + TimescaleDB 之上提供 InfluxDB 1.x HTTP API（Line Protocol + InfluxQL）的协议适配器，单容器、**无状态**，镜像为 pgcli 自建的 `ghcr.io/mars-base/pgcli/pgcli-influx2tsdb`（上游 1.3.2 + Debian）；每个 InfluxDB 数据库对应一个 PostgreSQL schema，每个 measurement 对应一个 hypertable；用 `--pg-name` 指向本地实例（每次启动重新推导 DSN），或用 `--dsn` 接任意**远程** PostgreSQL/TimescaleDB；**无认证、无 TLS**——需用 nginx 前置或隔离端口；保留与压缩以 **TimescaleDB 后台任务**形式下发（`add_retention_policy` / `add_compression_policy`，每 5 分钟重新同步）——**不需要 pg_cron，且代理停机期间清理照常进行**；数据全在 PostgreSQL，`remove` 从不删数据，备份/PITR/复制/HA 都走 PostgreSQL；InfluxQL 子集、纳秒时间戳、性能低于原生 InfluxDB；独立端口池（仅 Linux/amd64） |
| [`vlagent`](./vlagent/) | vlagent——VictoriaLogs 官方日志 agent，单容器直接跑官方 `docker.io/victoriametrics/vlagent` 镜像；跟踪宿主日志文件（每个 `--log-path` 以只读方式挂载到容器内相同绝对路径），用 vlagent 原生 glob（`--include`/`--exclude`，**不是正则**）选择文件，并通过一个或多个以 `/insert/native` 结尾的 `--remote-write-url` 转发给 VictoriaLogs；不给路径时仅做 HTTP 转发；**无配置文件、无鉴权**；独立端口池，默认 9429（Linux；macOS 代码完备、未实测） |
| [`vmcluster`](./vmcluster/) | VMCluster——VictoriaMetrics 集群版，三个组件（`vminsert` + `vmselect` + `vmstorage`）各跑一个容器，镜像为官方 `docker.io/victoriametrics/*:*-cluster`；`--roles` 选择本机运行哪些组件（默认全部 = 单机收敛），`--storage-node` 列出所有 vmstorage 地址以支持跨主机分片/副本；只有 vmstorage 有持久数据；**无配置文件、无鉴权**；每实例 5 个端口，基址 `vmcluster_start_port`（默认 8480-8484）（仅 Linux） |
| [`vlcluster`](./vlcluster/) | VLCluster——VictoriaLogs 集群版，三个组件（`vlinsert` + `vlselect` + `vlstorage`）**共用同一个**官方 `docker.io/victoriametrics/victoria-logs` 镜像，仅靠 flag 区分角色；`--roles` 选择本机运行哪些组件（默认全部 = 单机收敛），`--storage-node host:storagePort` 列出所有 vlstorage 地址以支持跨主机分片（**无副本**——任一 vlstorage 下线时查询返回 502）；只有 vlstorage 有持久数据；**无配置文件、无鉴权**；每实例 3 个端口，基址 `vlcluster_start_port`（默认 9430-9432）（仅 Linux） |
| [`gitea`](./gitea/) | Gitea——自托管 Git 服务，单容器直接跑官方 `docker.io/gitea/gitea` 镜像（rootful 构建，以 uid 1000 运行）；免向导安装（`INSTALL_LOCK`）+ 自动创建的 admin，其 Web 登录自动生成、打印一次、可用 `pg addon password gitea` 取回；默认内嵌 SQLite，或用 `--dsn` 接外置 PostgreSQL（Gitea 自建 schema，pgcli 从不建库/删库）；可选经 `--ssh-port` 启用 `ssh://` 克隆（独立第二端口池；默认关闭——只走 HTTP clone。镜像的 OpenSSH sshd 在每个实例上都无条件在跑，`--ssh-port` 的作用是把 parked sshd 提升为活的克隆端点）；加密 secret（`SECRET_KEY`/`INTERNAL_TOKEN`）刻意永不注入——它们归持久 volume 所有，以免重装静默重设密钥锁死幸存数据库；`Listen` 默认 `0.0.0.0`（注册默认关闭，`--allow-registration` 打开；建号走 `pg addon gitea user create`），配置只经环境变量（`GITEA__*`）交付；独立端口池（Linux；macOS 代码完备、未实测） |
| [`beegfs`](./beegfs/) | BeeGFS——并行文件系统，pgcli 的**首个多容器插件**：一条配置 = 一个单机收敛文件系统 = 三个容器（`mgmtd` + `meta` + `storage`，官方 `ghcr.io/thinkparq/beegfs-*` 镜像），按依赖顺序启动、以集群为单位管理（集群粒度的安装复用、启停、autostart、`pg logs addon beegfs --service`）；**仅 host 网络**——只有 Linux、没有端口池：协议口即原生固定口（8008/8010 可用两枚 `--beegfs-mgmtd-*` flag 移动，meta 8005 + storage 8003 在 8.4 无任何覆盖手段），这正是 v1 **每宿主仅一个文件系统**（有守卫）的原因；连接认证密钥首装自动生成（`pg addon password beegfs`），mgmtd TLS **关闭**（`--tls-disable`，客户端必须同样关闭）；**客户端是内核模块——pgcli 从不安装、从不挂载**，`pg addon beegfs mount-hint` 输出逐字的客户端配置；license 永不注入；`pg addon beegfs check` 靠守护进程日志行判健康；v1 单机，v2 = 跨主机集群 |
| [`juicefs`](./juicefs/) | JuiceFS——S3 后端的 POSIX 文件系统，pgcli 的**首个宿主二进制插件**（无容器）：二进制从 GitHub releases 下载、sha256 校验（`PATH` 上已有则复用）；两端全部显式、**无同主机自动识别**——对象存储是**外部 S3 服务**（`--s3-endpoint http(s)://host[:port]` + `--access-key`/`--secret-key`，format 前先探活），元数据引擎是一个显式 `--dsn` URL（`postgres://`/`redis://`/`sqlite3://`，pgcli 从不创建或删除该库）；建桶仍是手动 `pg mc` 的活——pgcli 把逐字命令打印给你；挂载为普通**宿主**目录（不进 PG 容器）；`pg addon juicefs mount|umount|status`、顶层 `pg addon start|stop juicefs` 别名、`pg logs addon juicefs -f`、`pg addon password juicefs` 取 S3 secret；`remove` 永不删桶数据——仅 Linux |
| [`zerofs`](./zerofs/) | ZeroFS——加密、自维护的 S3 文件系统（自有 LSM 跑在桶上，**无外部元数据引擎**）的宿主二进制形态：pgcli 写 `config.toml` 并起两个宿主进程——**仅 loopback** 的 9P server（9P 无认证，非 loopback `--listen` 直接拒绝；每实例一个端口）+ FUSE client；对象存储是**外部 S3 服务**、全显式给出（`--s3-endpoint` + `--access-key`/`--secret-key`，可选 `--s3-region`——无同主机 addon 自动解析）；生成的加密口令**包裹桶的数据密钥**（`pg addon password zerofs`；`remove` 保留桶后要用原口令 `--encryption-password` 重接，否则 server 报 `Invalid password or corrupted key`）；fencing 需 conditional put——`--conditional-put auto`（MinIO 2022-08+/真 S3）或给缺此能力的存储走 `redis`（必须显式 `--redis-dsn`），`none` 仅限单写者；`pg addon zerofs start|stop|mount|umount|status`、`pg logs addon zerofs --service server|mount`——仅 Linux |
| [`duckdb`](./duckdb/) | DuckDB——嵌入式 OLAP 分析数据库（"SQLite for analytics"）的**宿主二进制**形态（无容器、无守护进程）：C++ 列式引擎，向量化执行，直接查询 Parquet/CSV/JSON/Arrow；从 GitHub releases 下载、sha256 校验（`PATH` 上已有 `duckdb` 则直接复用）；常用扩展（`httpfs`、`parquet`、`json`）自动安装；`--version` 指定版本（默认 `1.5.6` LTS，也支持 `2.0.0`）；`pg addon duckdb status|shell`；仅 Linux、仅 amd64 |
| [`clickhouse`](./clickhouse/) | ClickHouse——单机列式 OLAP 数据库，直接跑官方 `docker.io/clickhouse/clickhouse-server` 镜像（默认固定 26.8 LTS `26.8.36.3`，amd64/arm64；host 网络，数据/日志/配置/备份目录均绑定挂载）；**认证始终开启**，安装时生成 admin 账号（`pg addon password clickhouse` 取回密码）；默认 HTTP `8123` + 原生 TCP `9000`，取自两个独立端口池（TCP 与 S3 API 池共享）；`Listen` 默认 `127.0.0.1`；**无 TLS 监听**（用 nginx 前置），单节点、无复制/HA；`pg addon clickhouse status|shell|query|database|user|grant|backup|restore|backups|config`，配置经点分 XML 路径的 `config set` 修改；仅 Linux |
| [`kafka`](./kafka/) | Kafka——**KRaft** broker（无需 ZooKeeper），官方 `docker.io/apache/kafka` 镜像（固定 `4.4.0`，amd64/arm64；host 网络、渲染 `server.properties`、数据目录绑定挂载）；单机 broker+controller combined 模式，**或跨主机静态 controller quorum 中的一个节点**（每台主机用 `--cluster`/`--node-id`/`--peer` 各装一次；推导 cluster id，成员不可在线变更）；默认 broker `9092`、controller `19093`（各自端口池，controller 避开 Alertmanager 的 `9093`）；`Listen` 默认 `127.0.0.1`（集群要求 `0.0.0.0`）；**默认 PLAINTEXT，可选 TLS 监听（`--tls`，SSL 端口取自 `9093` 池；仅加密，不认证客户端；集群客户端需拼接 CA bundle）**；RF=`min(3, voters)` / minISR=`min(2, RF)`；`pg addon kafka status\|shell\|topic create/list/describe/delete/retention\|group list/describe\|cluster status`；`--config-option` 覆盖项持久化在 `pg.yaml`；仅 Linux |
| [`rabbitmq`](./rabbitmq/) | RabbitMQ——单节点消息 broker，官方 `docker.io/library/rabbitmq` management 镜像（固定 `4.4.0`，amd64/arm64；host 网络、渲染 `rabbitmq.conf`、数据目录绑定挂载、`enabled_plugins` 固定为仅 management）；默认 AMQP `5672` 与 management Web UI/API `15672`，另有仅 loopback 的 EPMD `4369` 与 Erlang distribution `25672`，各自独立端口池；`Listen` 默认 `127.0.0.1`；**认证始终开启**（自动生成管理员密码，`pg addon password rabbitmq` 取回，每次启动对账）；可选**增量** TLS（`--tls`：AMQPS `5671` + management HTTPS `15671`，自签 CA 或自带 `--tls-cert`/`--tls-key`；仅加密，不认证客户端）；`pg addon rabbitmq status\|shell\|queue ...`——declare/info/publish/get/bind/unbind/bindings 走 management API，list/purge/delete 走 `rabbitmqctl`；单节点，无集群、不启用 Prometheus 插件；仅 Linux |

每个插件都有独立页面，包含命令、参数与故障排除说明。

> **Patroni 高可用（`pg ha`）不在此列**——它不通过 `pg addon` 安装，而是独立的顶层命令，
> 文档也已从本节拆出，见 **[HA 集群](../ha-cluster/)**。本节的 [etcd](./etcd/) 与
> [HAProxy](./haproxy/) 仍是可被 `pg ha` 集群使用的插件。

## 工作原理

插件作为独立容器运行，通过 `pg.yaml` 管理：

1. **`pg addon install`** 生成配置并启动插件容器
2. 配置与数据存放在 `<base-dir>/addon/<addon-name>/`
3. 插件容器通过主机网络通信
4. 配置更新时容器自动重启

**命名空间隔离：** 插件遵循配置的 `namespace` 设置 —— 容器名包含命名空间前缀，
因此不同配置文件可以管理互不冲突的独立插件。

## 常用命令

```bash
pg addon install <addon> [flags]   # 安装 / 重新配置（幂等）
pg addon list                       # 查看所有已安装插件及状态
pg addon remove <addon> [flags]     # 移除插件、容器与数据
```

各插件的具体参数与示例见其独立页面：
[Pgbouncer](./pgbouncer/) · [etcd](./etcd/) · [PgDog](./pgdog/) · [PostgREST](./postgrest/) · [HAProxy](./haproxy/) · [MinIO](./minio/) · [Silo](./silo/) · [rustfs](./rustfs/) · [Garage](./garage/) · [SeaweedFS](./seaweedfs/) · [Redis](./redis/) · [Predixy](./predixy/) · [pgAdmin 4](./pgadmin/) · [nginx](./nginx/) · [Grafana](./grafana/) · [Prometheus](./prometheus/) · [Alertmanager](./alertmanager/) · [VictoriaMetrics](./victoriametrics/) · [VictoriaLogs](./victorialogs/) · [vlagent](./vlagent/) · [InfluxDB](./influxdb/) · [influx2tsdb](./influx2tsdb/) · [ClickHouse](./clickhouse/) · [Kafka](./kafka/) · [RabbitMQ](./rabbitmq/) · [VMCluster](./vmcluster/) · [VLCluster](./vlcluster/) · [Gitea](./gitea/) · [BeeGFS](./beegfs/) · [JuiceFS](./juicefs/) · [ZeroFS](./zerofs/) · [DuckDB](./duckdb/)。Patroni 相关页面见 [HA 集群](../ha-cluster/)。
