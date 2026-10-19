---
title: "BeeGFS"
description: "以 pgcli 插件方式运行 BeeGFS 并行文件系统——首个多容器插件（mgmtd + meta + storage 三容器收敛集群）"
weight: 58
---

`pg addon install beegfs` 以受管插件方式运行 [BeeGFS](https://www.beegfs.io/)——scale-out 并行文件系统。与仓库里其他所有插件（各一个容器）不同，一个 BeeGFS *文件系统* 是**三个容器**构成的三件套，按依赖顺序拉起：

```
pgcli-beegfs-<name>-mgmtd     管理守护进程——节点/target 注册表
pgcli-beegfs-<name>-meta      元数据服务   ——一个 meta target
pgcli-beegfs-<name>-storage   数据服务     ——一个 storage target
```

这是 v1：**单机收敛集群**——三个服务全部在一台宿主上，一条配置条目
（`addons.beegfs.<name>`）、一个共享连接认证密钥、以集群为单位整体创建与拆除。
它是真实可挂载的 BeeGFS 后端；还不是跨主机的 HPC 分布式集群（那是 v2，见
「已知限制」）。

> **平台**：仅 Linux。BeeGFS 守护进程跑 **host 网络**（BeeGFS 说的是自家
> TCP/UDP 协议而非 HTTP，不存在可分配的容器端口映射）。macOS 的 podman 没有
> host 网络，`pg addon install beegfs` 在 Mac 上会带明确报错直接拒绝。

> **客户端刻意不在管辖范围内。** 挂载 BeeGFS 需要 `beegfs-client` **内核模块**
> （DKMS）。内核模块无法容器化，所以 pgcli 既不安装也不挂载它——只管三个*服务
> 端*容器。`pg addon beegfs mount-hint` 会打印装了 `beegfs-client` +
> `beegfs-utils` 包的主机所需的逐字配置。见「挂载客户端」一节。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `beegfs` | 文件系统名（map 键）；三容器名为 `<前缀>-{mgmtd,meta,storage}` |
| `--image` | `8.4.0` | 版本后缀——manager 据此拼出 `ghcr.io/thinkparq/beegfs-{mgmtd,meta,storage}:<version>`（三容器共用同一 tag） |
| `--data-dir` | `<base>/addon/beegfs/<name>` | **一个**目录管整个文件系统；三个服务用 `<dir>/{mgmtd,meta,storage}` |
| `--listen` | `0.0.0.0` | 客户端自引用 mgmtd 的地址（`sysMgmtdHost`）；host 网络下守护进程实际绑所有网卡 |
| `--password` | 自动生成 | 连接认证密钥（`ConnAuth`），三个守护进程与所有客户端共享；未传时首装自动生成 |
| `--beegfs-mgmtd-port` | `8008` | 移动 mgmtd 的 BeeMsg 口（meta/storage 注册指向的*唯一*可移动口）；`0` = 用默认 |
| `--beegfs-mgmtd-grpc-port` | `8010` | 移动 mgmtd 的 gRPC 口（`beegfs` CLI 拨的口）；`0` = 用默认 |
| `--force` | — | 即使集群已在运行也重建三个容器 |

meta/storage **刻意没有 `--port`**：见「端口」一节。

## 安装

```bash
pg addon install beegfs                    # 默认名 "beegfs" 的文件系统
pg addon install beegfs --name fs1
pg addon install beegfs --listen 10.0.0.5  # 对客户端宣告可达的 mgmtd 地址
```

全程非交互。先一次性拉齐三个镜像（避免网络故障把集群卡在"半注册"状态），然后
**按序**拉起：先 mgmtd，再 meta（阻塞等它打出
`Registration and management info download complete`——这一行同时证明
"mgmtd 在服务"与"该服务已入集群"），storage 同样。每个容器首启执行一步
`beegfs-setup-<service>`（由镜像 entrypoint 消费的 `beegfs_setup_1` 环境变量
驱动）初始化自己的 target 目录；幂等 marker 保证对幸存数据目录重建容器时直接
复用既有 target。

重复执行 `install` 是安全的，且以**集群为粒度**：

- 三容器全在跑 → 跳过（要重建用 `--force`，要看健康用 `pg addon beegfs check`）；
- 三容器全停且存在 → 直接启动存好的集群（不 pull、不重建）；
- 其余情况（partial、或全新安装）→ `EnsureCluster` 按序重建整套。

安装摘要打印三个容器名、镜像、mgmtd 两个口、三个数据目录、conn-auth 状态，以及
指向 `pg addon beegfs mount-hint` 的一行。`--listen` 是通配 `0.0.0.0` 时附
暴露 NOTE（关掉 TLS 的并行文件系统，对任何握着 conn-auth 密钥的机器都是一扇
门）。

## 数据目录与持久化

一个文件系统拥有**三个**宿主目录，各自 bind 挂载进自己容器的 `/data/<role>`：

```
<base>/addon/beegfs/<name>/mgmtd     mgmtd.sqlite——节点/target 注册表
<base>/addon/beegfs/<name>/meta      元数据 target
<base>/addon/beegfs/<name>/storage   数据 target（真正的文件内容）
```

`--data-dir <dir>` 一个 flag 排出全部三个目录（`<dir>/{mgmtd,meta,storage}`）
——使用者从不按角色分别寻址。三个镜像以 **root** 运行并自属数据目录，所以与
Gitea（uid 1000）、Grafana（uid 472）不同，**没有预 `chown` 环节**——宿主目录
直接可写。

数据全部跨容器删除存活：`pg addon remove beegfs` 不带 `--clean-data` 保留三个
目录，同名重装即**复活**文件系统（检测到既有 `mgmtd.sqlite` 时安装会打印
NOTE）。复活沿用的是存储里原有的注册表与连接认证——新传的 `--password` 只作用
于全新初始化的守护进程，改不动幸存的 SQLite 注册表。

## 端口

BeeGFS 的四个监听口是**协议原生固定口**，不是端口池：

```
mgmtd   8008  BeeMsg——meta/storage 在此注册   （可移动：--beemsg-port）
mgmtd   8010  gRPC  ——`beegfs` CLI 拨此口     （可移动：--grpc-port）
meta    8005  元数据服务                       （固定）
storage 8003  数据服务                         （固定）
```

实机核验：BeeGFS 8.4 的 **meta 与 storage 守护进程不接受任何监听口覆盖**
（`connPortsList`/`connTcpPort` 被拒为非法参数，`connMetaPort` 收下但注册就
断）。只有 mgmtd 的两个口能动；移动 mgmtd 的 BeeMsg 口后，meta/storage 的注册
会自动重定向（manager 给两者注入 `connMgmtdPort=<port>`）。直接推论：**每台宿
主只能有一个 BeeGFS 文件系统**——第二个无论如何绑不了 8005/8003，与 pgcli 分
配什么无关，所以 `EnsureCluster` 显式挡掉第二个，CLI 也会带真实理由拒绝
`pg.yaml` 里手改的非默认 `meta_port`/`storage_port`。因此**没有
`beegfs_start_port` 端口池**——与其他插件不同，BeeGFS 的端口是固定的，不是分
配的。

## 连接认证与 TLS

三个守护进程与所有客户端共享一个**连接认证密钥**（`ConnAuth`，经
`CONN_AUTH_FILE_DATA` 环境变量交给镜像 init 写成 `/etc/beegfs/conn.auth`）。
首装自动生成（20 位随机）、打印一次、存入 `pg.yaml`，之后用
`pg addon password beegfs` 取回。与其他 pgcli secret 一样，`ApplyDefaults`
绝不把它默认成字面量。

**mgmtd 的 TLS 是关闭的**（`--tls-disable true`，v1 的明确决策）。这意味着只
有同样带 `--tls-disable` 的客户端能入集群，且这是真实的暴露考量：通配
`--listen 0.0.0.0` + TLS 关闭 = 网络上任何握着 conn-auth 密钥的机器都能触达
8008/8010/8005/8003。非私有网络请绑回环（`--listen 127.0.0.1`）或对这四个口
做防火墙。

**License**：BeeGFS license 是向 ThinkParQ 申请的系统绑定注册——**pgcli 从不
生成或注入**（与 Gitea 加密 secret 同一原则）。无 license 的 8.4 管理守护进程
在注册时打一条 quota 告警，文件系统对社区规模照常可用。mirroring、pools、
quota 属企业功能，需要 license，也不在 v1 的接线范围内。

## 挂载客户端

客户端是宿主**内核模块**——pgcli 不装。在 Linux 主机上添加 ThinkParQ apt 仓
库、安装 `beegfs-client beegfs-utils`（DKMS 会对着 `linux-headers-$(uname -r)`
编译模块），然后照抄 `pg addon beegfs mount-hint` 的输出。它逐字渲染三个文件：

```bash
pg addon beegfs mount-hint --name fs1
```

```
1. /etc/beegfs/conn.auth（0600）——与服务端完全相同的密钥
2. /etc/beegfs/beegfs-client.conf——main 段设置：
     sysMgmtdHost=<mgmtd-host>      # 仅当 mgmtd BeeMsg 口被移动过时才有 connMgmtdPort=<port>
3. /etc/beegfs/beegfs-mounts.conf（一行一个挂载）：
     /mnt/beegfs fs1@<mgmtd-host>:/ _sysName=<client-name>
```

然后 `systemctl restart beegfs-client` 触发挂载，用 `df -h /mnt/beegfs` 加一
次写读往返验证。由于集群关着 TLS，每个 `beegfs` CLI 调用都要带关 TLS 的形态、
指向 gRPC 口：

```bash
beegfs --tls-disable --mgmtd-addr=<mgmtd-host>:8010 node list --type client
```

`mount-hint` 是纯本地渲染——从不碰 podman，客户端本就不归 pgcli 管。
`--listen` 为通配时打印占位符 `<mgmtd-host-ip>`（0.0.0.0 不可连），而不是客户
端连不上的值。

## 健康检查

```bash
pg addon beegfs check             # 默认名
pg addon beegfs check --name fs1
```

报告集群聚合状态（`running` / `stopped` / `partial (mgmtd,meta)`），并按服务
给出容器是否在跑、是否打出就绪日志行——mgmtd 是
`Waiting for shutdown signal`（在服务），meta/storage 是
`Registration and management info download complete`（已入集群）。三个镜像都
不含 `beegfs-ctl`/`check-servers`，所以守护进程自己的日志就是权威的*容器内*信
号；要拿已挂载客户端的视角，用上面的 `beegfs` gRPC CLI。`check` 对停着的集群
同样可用（它报状态，不要求运行中）。

## 取回连接认证密钥

```bash
pg addon password beegfs             # "beegfs" 文件系统
pg addon password beegfs --name fs1
```

`pg addon list` 只显示 presence 标记（`Auth: none` / `set (connection-auth)`），
只有加 `--show-password` 才打印密钥本身——随手 list 绝不泄密。

## List

```bash
pg addon list
```

BeeGFS 区块（"Infra add-ons (beegfs)"）每个文件系统一条：聚合集群状态、mgmtd
两个口、固定的 meta/storage 口、conn-auth presence、镜像、三个数据目录、三个
容器名。podman 不可用时仍然列出配置条目（状态显示 `n/a`）。

## 启停

容器带 podman 的 `unless-stopped` 重启策略，宿主重启后随 podman 服务自动回
来。`start`/`stop` 按依赖顺序显式管理整个集群：

```bash
pg addon stop beegfs     # storage → meta → mgmtd（反序，target 保持 GOOD）
pg addon start beegfs    # mgmtd → meta → storage
```

## 开机自启

```bash
pg autostart enable --beegfs --name fs1
```

BeeGFS 是**唯一的集群粒度 autostart**：`pg start --autostart` 拉起整个文件系
统（按 mgmtd → meta → storage 顺序），而不是单个容器。因为镜像以 root 运行并
自属目录，autostart 除 podman 外不需要任何宿主权限（没有 Gitea 那样的 chown
环节）。每台挂载主机上的客户端经由自己的 `beegfs-mounts.conf` 自行重挂——
pgcli 从不管理那一步。完整机制、状态与关闭方式见
[Autostart]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove beegfs                # 保留三个数据目录——同名重装复活文件系统
pg addon remove beegfs --clean-data   # 连目录一起删（数据没了）
```

移除按反序拆（storage → meta → mgmtd）。`--clean-data` 拒删仍是挂载点的目录。
**指向本宿主的客户端挂载点无论如何都得先卸载**——pgcli 够不着客户端（那不归
它管），请先去客户端主机上卸载。

## 日志

三个容器，所以 `pg logs addon beegfs` 用 `--service` 选一个（默认 `mgmtd`）：

```bash
pg logs addon beegfs                       # mgmtd（默认）
pg logs addon beegfs --service meta -f     # 跟踪元数据守护进程
pg logs addon beegfs --service storage -n 200
```

`--service` 只对 `beegfs` 有效（配其他插件类型时报错）。

## 排障

- **`install beegfs --name fs2` 报 "a BeeGFS filesystem already exists on this
  host"**——这是 v1 的一宿主一文件系统规则，不是 bug：8.4 里 meta（8005）与
  storage（8003）的口动不了，同宿主第二个文件系统永远绑不上。换宿主（这正是
  v2 的跨主机方向），或先 `pg addon remove` 掉第一个。
- **`check` 显示 meta/storage 一直 not registered**——mgmtd 必须先在服务；
  mgmtd 容器若退出，两者会阻塞到 `beegfsSetupTimeout`（60 秒）让安装失败而非
  挂死。看 `pg logs addon beegfs --service mgmtd` 找 mgmtd 退出原因（宿主上
  端口被占、或数据目录不可写）。
- **`install` 在 `meta_port`/`storage_port` 配置校验处拒绝**——`pg.yaml` 被手
  改出了非默认值。守护进程根本绑不了那些口，删掉钉死的值回落 8005/8003。
- **客户端挂不上 / `beegfs` CLI 报 TLS 错误**——集群跑的是 mgmtd TLS 关闭；所
  有客户端与 `beegfs` CLI 调用都必须同样带 `--tls-disable`，且
  `/etc/beegfs/conn.auth` 必须与 `pg addon password beegfs` 的值**逐字相同**。
- **别的机器对着 `localhost` 挂载失败**——通配 `--listen 0.0.0.0` 时
  `mount-hint` 把 `sysMgmtdHost` 渲染成占位符；用 `--listen <可达IP>`（配
  `--force`）重装，客户端才能拿到真实 mgmtd 地址。

## 已知限制

- **仅 Linux。** host 网络是硬性要求，podman 只在 Linux 提供；macOS 上安装被
  拒。不存在 bridge 兜底——BeeGFS 不是 HTTP。
- **一宿主一文件系统、端口固定。** v1 钉死原生口（8008/8010/8005/8003）；
  8.4 里 meta/storage 的监听口不可移动，单宿主只容得下一个文件系统。
- **pgcli 从不安装、从不挂载客户端。** 客户端是内核模块（DKMS），无法容器
  化；pgcli 只管三个服务端容器，挂载靠 `mount-hint` *输出文档*。
- **TLS 关闭、license 不接线。** mgmtd 以 `--tls-disable true` 运行；`beegfs`
  gRPC CLI 与客户端必须匹配。license、mirroring、pools、quota 不归 pgcli 管——
  企业功能需要线下从 ThinkParQ 取得注册。
- **v1 是单机（收敛）集群。** 真正的跨主机 HPC 集群——meta/storage 分布到多
  台宿主、复用 Patroni 那套 SSH 编排——是计划中的 **v2**，此处未实现。
