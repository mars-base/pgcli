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
| [`gitea`](./gitea/) | Gitea——自托管 Git 服务，单容器直接跑官方 `docker.io/gitea/gitea` 镜像（rootful 构建，以 uid 1000 运行）；免向导安装（`INSTALL_LOCK`）+ 自动创建的 admin，其 Web 登录自动生成、打印一次、可用 `pg addon password gitea` 取回；默认内嵌 SQLite，或用 `--dsn` 接外置 PostgreSQL（Gitea 自建 schema，pgcli 从不建库/删库）；可选经 `--ssh-port` 启用 `ssh://` 克隆（独立第二端口池；默认关闭——只走 HTTP clone。镜像的 OpenSSH sshd 在每个实例上都无条件在跑，`--ssh-port` 的作用是把 parked sshd 提升为活的克隆端点）；加密 secret（`SECRET_KEY`/`INTERNAL_TOKEN`）刻意永不注入——它们归持久 volume 所有，以免重装静默重设密钥锁死幸存数据库；`Listen` 默认 `0.0.0.0`（注册默认关闭，`--allow-registration` 打开；建号走 `pg addon gitea user create`），配置只经环境变量（`GITEA__*`）交付；独立端口池（Linux；macOS 代码完备、未实测） |
| [`beegfs`](./beegfs/) | BeeGFS——并行文件系统，pgcli 的**首个多容器插件**：一条配置 = 一个单机收敛文件系统 = 三个容器（`mgmtd` + `meta` + `storage`，官方 `ghcr.io/thinkparq/beegfs-*` 镜像），按依赖顺序启动、以集群为单位管理（集群粒度的安装复用、启停、autostart、`pg logs addon beegfs --service`）；**仅 host 网络**——只有 Linux、没有端口池：协议口即原生固定口（8008/8010 可用两枚 `--beegfs-mgmtd-*` flag 移动，meta 8005 + storage 8003 在 8.4 无任何覆盖手段），这正是 v1 **每宿主仅一个文件系统**（有守卫）的原因；连接认证密钥首装自动生成（`pg addon password beegfs`），mgmtd TLS **关闭**（`--tls-disable`，客户端必须同样关闭）；**客户端是内核模块——pgcli 从不安装、从不挂载**，`pg addon beegfs mount-hint` 输出逐字的客户端配置；license 永不注入；`pg addon beegfs check` 靠守护进程日志行判健康；v1 单机，v2 = 跨主机集群 |
| [`juicefs`](./juicefs/) | JuiceFS——S3 后端的 POSIX 文件系统，pgcli 的**首个宿主二进制插件**（无容器）：二进制从 GitHub releases 下载、sha256 校验（`PATH` 上已有则复用）；两端全部显式、**无同主机自动识别**——对象存储是**外部 S3 服务**（`--s3-endpoint http(s)://host[:port]` + `--access-key`/`--secret-key`，format 前先探活），元数据引擎是一个显式 `--dsn` URL（`postgres://`/`redis://`/`sqlite3://`，pgcli 从不创建或删除该库）；建桶仍是手动 `pg mc` 的活——pgcli 把逐字命令打印给你；挂载为普通**宿主**目录（不进 PG 容器）；`pg addon juicefs mount|umount|status`、顶层 `pg addon start|stop juicefs` 别名、`pg logs addon juicefs -f`、`pg addon password juicefs` 取 S3 secret；`remove` 永不删桶数据——仅 Linux |
| [`zerofs`](./zerofs/) | ZeroFS——加密、自维护的 S3 文件系统（自有 LSM 跑在桶上，**无外部元数据引擎**）的宿主二进制形态：pgcli 写 `config.toml` 并起两个宿主进程——**仅 loopback** 的 9P server（9P 无认证，非 loopback `--listen` 直接拒绝；每实例一个端口）+ FUSE client；对象存储是**外部 S3 服务**、全显式给出（`--s3-endpoint` + `--access-key`/`--secret-key`，可选 `--s3-region`——无同主机 addon 自动解析）；生成的加密口令**包裹桶的数据密钥**（`pg addon password zerofs`；`remove` 保留桶后要用原口令 `--encryption-password` 重接，否则 server 报 `Invalid password or corrupted key`）；fencing 需 conditional put——`--conditional-put auto`（MinIO 2022-08+/真 S3）或给缺此能力的存储走 `redis`（必须显式 `--redis-dsn`），`none` 仅限单写者；`pg addon zerofs start|stop|mount|umount|status`、`pg logs addon zerofs --service server|mount`——仅 Linux |
| [`duckdb`](./duckdb/) | DuckDB——嵌入式 OLAP 分析数据库（"SQLite for analytics"）的**宿主二进制**形态（无容器、无守护进程）：C++ 列式引擎，向量化执行，直接查询 Parquet/CSV/JSON/Arrow；从 GitHub releases 下载、sha256 校验（`PATH` 上已有 `duckdb` 则直接复用）；常用扩展（`httpfs`、`parquet`、`json`）自动安装；`--version` 指定版本（默认 `1.5.6` LTS，也支持 `2.0.0`）；`pg addon duckdb status|shell`；仅 Linux、仅 amd64 |

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
[Pgbouncer](./pgbouncer/) · [etcd](./etcd/) · [PgDog](./pgdog/) · [PostgREST](./postgrest/) · [HAProxy](./haproxy/) · [MinIO](./minio/) · [Silo](./silo/) · [rustfs](./rustfs/) · [Garage](./garage/) · [SeaweedFS](./seaweedfs/) · [Redis](./redis/) · [Predixy](./predixy/) · [pgAdmin 4](./pgadmin/) · [nginx](./nginx/) · [Grafana](./grafana/) · [Prometheus](./prometheus/) · [Alertmanager](./alertmanager/) · [Gitea](./gitea/) · [BeeGFS](./beegfs/) · [JuiceFS](./juicefs/) · [ZeroFS](./zerofs/) · [DuckDB](./duckdb/)。Patroni 相关页面见 [HA 集群](../ha-cluster/)。
