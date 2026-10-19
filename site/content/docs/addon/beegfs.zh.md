---
title: "BeeGFS"
description: "以 pgcli 插件方式运行 BeeGFS 并行文件系统。"
weight: 58
---

[BeeGFS](https://www.beegfs.io/) 是一套走「易用」路线的**并行文件系统**：2004
年生于德国弗劳恩霍夫工业数学研究所（ITWM）——当年一个课题组要用 Lustre，嫌它
部署运维太重，干脆自己写了一个，目标从一开始就定死：不求规模最大，但求部署和
维护最省人力。2014 年核心成员从研究所出来成立商业公司 ThinkParQ，产品改名
BeeGFS，一路从研究所内部工具长成欧洲高校与区域超算里常见的共享存储。

它解决的是「多台机器共享一份高吞吐数据」的问题：本地文件系统只活在一台机器
上，NFS 这类共享存储在多客户端并发读写大文件时很快见顶。BeeGFS 和 Lustre 同
属并行文件系统——元数据与数据分离、文件切块摊到多台存储服务器、客户端拿到位置
信息后直连数据节点读写，聚合带宽随服务器数线性增长——但在每一个可以简化的地方
都选了简单：服务角色可以混布在同一台机器上、守护进程启动即自动注册组网（不需要
手工分发配置）、扩容就是加机器、普通以太网就能跑而不强制 InfiniBand。代价是不
追求极限规模与最细的调优自由度，换来的是**没有专职存储工程师的课题组、部门级集
群也能一两天用起来**。

典型场景：几台到两三百台机器的 HPC 集群、**AI 训练的数据供给层**（7.3 起支持
GPUDirect Storage，存储与 GPU 显存直通，大规模训练/推理直接读共享数据集）、
**影视行业后期编辑的素材数据**（多人并发剪辑大码流素材不抢带宽）、海量小文件
或大文件顺序读写的部门级共享盘。一句话选型：国家级超算、上千节点、有专职存储
团队——Lustre；「当天用上、不想养存储团队」——BeeGFS。

> **平台**：仅 Linux。BeeGFS 守护进程跑 **host 网络**（BeeGFS 说的是自家
> TCP/UDP 协议而非 HTTP，不存在可分配的容器端口映射）。macOS 的 podman 没有
> host 网络，`pg addon install beegfs` 在 Mac 上会带明确报错直接拒绝。

> **客户端刻意不在管辖范围内。** 挂载 BeeGFS 需要 `beegfs-client` **内核模块**
> （DKMS）。内核模块无法容器化，所以 pgcli 既不安装也不挂载它——只管*服务端*
> 服务（收敛宿主上是三个容器，跨主机成员上是本机承担的角色子集）。
> `pg addon beegfs mount-hint` 会打印装了 `beegfs-client` +
> `beegfs-utils` 包的主机所需的逐字配置。见「挂载客户端」一节。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `beegfs` | 文件系统名（map 键）；容器名为 `<前缀>-<角色>`，按本机承担的角色各建一个 |
| `--image` | `8.4.0` | 版本后缀——manager 据此拼出 `ghcr.io/thinkparq/beegfs-<角色>:<version>`（各角色共用同一 tag） |
| `--data-dir` | `<base>/addon/beegfs/<name>` | **一个**目录管整个文件系统；三个服务用 `<dir>/{mgmtd,meta,storage}` |
| `--listen` | `0.0.0.0` | 客户端自引用 mgmtd 的地址（`sysMgmtdHost`）；host 网络下守护进程实际绑所有网卡 |
| `--password` | 自动生成（仅含 mgmtd 的宿主） | 连接认证密钥（`ConnAuth`），全部守护进程与所有客户端共享；首装生成**仅限本机跑 mgmtd**——join 宿主必须显式传集群既有值 |
| `--roles` | 全部三角色 | v2：本机承担哪些服务——`mgmtd,meta,storage` 的逗号列表；不传即收敛单机文件系统 |
| `--mgmtd-host` | — | v2：本机 meta/storage 注册指向的 mgmtd 地址（`--roles` 不含 mgmtd 时必填） |
| `--node-id` | `1` | v2：本机 meta/storage 的 BeeGFS 节点号（1..655）；storage target ID 由它派生（`2` → `201`、`202`…）。跨主机须按服务类型唯一 |
| `--storage-target` | 单目录 | v2：一个宿主目录一个 storage target——多块盘就重复传（每盘一个 target） |
| `--license-file` | — | ThinkParQ license `.pem` 的路径——搬运进 mgmtd 容器（内容存 `pg.yaml`，注入为 `BEEGFS_LICENSE_FILE_DATA`）。**仅限 mgmtd 宿主**——pgcli 从不生成 license，之后用 `pg addon beegfs license` 查看/热换 |
| `--beegfs-mgmtd-port` | `8008` | 移动 mgmtd 的 BeeMsg 口（meta/storage 注册指向的*唯一*可移动口）；`0` = 用默认 |
| `--beegfs-mgmtd-grpc-port` | `8010` | 移动 mgmtd 的 gRPC 口（`beegfs` CLI 拨的口）；`0` = 用默认 |
| `--force` | — | 即使本机容器已在跑也重建 |

meta/storage **刻意没有 `--port`**：见「端口」一节。

## 安装

```bash
pg addon install beegfs                    # 默认名 "beegfs" 的文件系统
pg addon install beegfs --name fs1
pg addon install beegfs --listen 10.0.0.5  # 对客户端宣告可达的 mgmtd 地址
```

全程非交互。先一次性拉齐本机角色所需的镜像（避免网络故障把集群卡在"半注册"
状态），然后**按依赖序**拉起：先 mgmtd，再 meta，再 storage——join 宿主（如
`--roles storage`）只跑自己那一步。meta/storage 会阻塞等它们打出
`Registration and management info download complete`——这一行同时证明
"mgmtd 可达"与"该服务已入集群"。每个容器首启执行一步
`beegfs-setup-<service>`（由镜像 entrypoint 消费的 `beegfs_setup_N` 环境变量
驱动）初始化自己的 target 目录；幂等 marker 保证对幸存数据目录重建容器时直接
复用既有 target。

重复执行 `install` 是安全的，且以**集群为粒度**（按本机角色计）：

- 本机角色全在跑 → 跳过（要重建用 `--force`，要看健康用 `pg addon beegfs check`）；
- 本机角色全停且存在 → 直接启动存好的集群（不 pull、不重建）；
- 其余情况（partial、或全新安装）→ `EnsureCluster` 按序重建整套。

安装摘要打印本机角色与对应容器名、镜像、mgmtd 地址与两个口（join 宿主标
remote）、节点号与数据目录、conn-auth 状态，以及指向
`pg addon beegfs mount-hint` 的一行；跑 mgmtd 的宿主还会提示
`pg addon beegfs join-hint`。`--listen` 是通配 `0.0.0.0` 时附
暴露 NOTE（关掉 TLS 的并行文件系统，对任何握着 conn-auth 密钥的机器都是一扇
门）。

## 跨主机集群（v2）

BeeGFS 的扩展轴是**加机器**，不是手工同步配置：每个服务守护进程启动时只需
`sysMgmtdHost=<mgmtd地址>` 加共享 conn-auth 密钥，就会**自行注册进集群——
mgmtd 本身就是注册中心**。pgcli 完全照搬这个模型：**没有中心化部署命令，pgcli
从不 SSH**。每台宿主各自跑一条 `pg addon install beegfs --roles <子集>`，用自己
本地的 `pg.yaml` 描述本机承担的角色，剩下的组网全是 BeeGFS 自己完成——与
Patroni 跨主机成员同一套声明式逐机模型（那边聚合拓扑靠 etcd，这边靠 mgmtd 的
SQLite 注册表）。

三台机器一个文件系统：

```bash
# vm01 —— 本宿主跑管理 + 元数据；conn-auth 密钥在这台生成：
pg addon install beegfs --name fs1 --roles mgmtd,meta --listen 10.241.20.147

# 在 vm01 上渲染逐字 join 命令（含密钥）：
pg addon beegfs join-hint --name fs1

# vm02 —— 两块数据盘，一块盘一个 storage target：
pg addon install beegfs --name fs1 --roles storage --mgmtd-host 10.241.20.147 \
    --node-id 2 --password <conn-auth> \
    --storage-target /mnt/d1 --storage-target /mnt/d2

# vm03 —— 再来一个 storage 节点（或 --roles meta 把元数据挪离 vm01）：
pg addon install beegfs --name fs1 --roles storage --mgmtd-host 10.241.20.147 \
    --node-id 3 --password <conn-auth> --storage-target /mnt/d3
```

客户端照旧从 vm01 的地址挂载——跨主机对内核模块客户端完全透明。

规则与边界，如实说明：

- **`--roles` 不含 mgmtd 时 `--mgmtd-host` 必填**（没有 localhost 可回落），
  且 join **必须带集群既有的 conn-auth**——pgcli 绝不替既有集群的成员生成密
  钥。密码传错几秒内报错并指路 `pg addon password beegfs`，而不是等注册超时。
- **`--node-id` 在跨主机间必须按服务类型唯一**（node ID 是按服务类型分的命名
  空间：`meta_1` 与 `storage_1` 共存）。它派生 storage target ID：节点 2 的
  target 是 201、202…（`node_id*100 + 序号`，上限 node_id 655）。
- 每台宿主的 `pg.yaml` 条目**只存自己承担的角色**；`check`、
  `start`/`stop`、`list`、autostart 全是本宿视角。集群完整成员表在 mgmtd 里
  （`beegfs ... node list`）。
- **mgmtd 仍是单点**：它挂掉后已挂载客户端的 IO 照常（客户端直连 meta/storage），
  但注册与管理操作停摆。管理节点双活是企业 license 功能，不归 pgcli 接线。
  **license 同样只装在 mgmtd 宿主**——join 宿主既不存也不需要 PEM
  （`join-hint` 会说明这一点），证书由 mgmtd 为整个文件系统校验。
- 仍然**一宿主一文件系统**——扩展轴是跨主机，不是同宿主第二个文件系统
  （固定口 8005/8003 必撞）。

## 数据目录与持久化

收敛宿主拥有**三个**宿主目录，各自 bind 挂载进自己容器的 `/data/<role>`：

```
<base>/addon/beegfs/<name>/mgmtd     mgmtd.sqlite——节点/target 注册表
<base>/addon/beegfs/<name>/meta      元数据 target
<base>/addon/beegfs/<name>/storage   数据 target（真正的文件内容）
```

`--data-dir <dir>` 一个 flag 排出全部三个目录（`<dir>/{mgmtd,meta,storage}`）
——使用者从不按角色分别寻址。跨主机成员只用自己角色的那个目录。**多 storage
target**（重复 `--storage-target`）时，每个宿主目录挂进同一个 storage 容器的
`/data/storage/<targetID>`；单 target 保持朴素的 `/data/storage` 路径，v1 的
存量 store 因此原样复活（target 目录内部记录自身路径，复活必须挂同一路径）。
三个镜像以 **root** 运行并自属数据目录，所以与
Gitea（uid 1000）、Grafana（uid 472）不同，**没有预 `chown` 环节**——宿主目录
直接可写。

数据全部跨容器删除存活：`pg addon remove beegfs` 不带 `--clean-data` 保留目录，
同名重装即**复活**文件系统（检测到既有 `mgmtd.sqlite` 时安装会打印
NOTE）。复活沿用的是存储里原有的注册表与连接认证——新传的 `--password` 只作用
于全新初始化的守护进程，改不动幸存的 SQLite 注册表。

## 故障处理：跨节点条带做性能，ZFS 做可靠性

BeeGFS 的跨节点条带是**性能**机制，不是冗余机制：默认 RAID0 布局下，丢任意一
个 storage target 就丢它承载的 chunk——条带化的文件即随之损坏。节点级数据冗
余是 buddy mirroring，而那是企业 license 功能。所以社区版把两件事干净地分层，
而存储侧需要一个**本地故障的底层方案**：

- **跨节点：条带做吞吐。** 每个文件的 chunk 摊到多台机器的多个 target 上，聚
  合带宽随服务器数线性增长——`--num-targets`/`--chunk-size` 调的就是这一层。
  它买的是速度，不是安全。
- **每台 storage 服务器内部：target 底下垫一个有弹性的卷。** 推荐底座是
  **ZFS**——因为 BeeGFS 的 storage target 就是"一个目录"，ZFS 免费把这个目录
  变成自愈存储：checksum 防静默损坏、copy-on-write、快照、压缩。守护进程对此
  毫无感知，所以这一层**完全不需要 pgcli 或 BeeGFS 参与**：建池、每个 target
  一个 dataset、把 `--storage-target` 指过去即可。

    ```bash
    # 两块盘 → mirror vdev（ZFS 的 RAID1：容量=单盘大小，任坏一块数据仍在）。
    # raidz1/2 需要 3/4 块盘起。
    zpool create bgtank mirror /dev/disk1 /dev/disk2
    zfs create -o recordsize=1M -o compression=lz4 bgtank/t1
    # ……然后：  pg addon install beegfs --roles storage ... \
    #              --storage-target /bgtank/t1
    ```

  （mdadm RAID1/5/6 也能用，但 ZFS 的 checksum 能抓住静默位翻转——否则会被
  原样喂给客户端。）一个 target 一个 dataset，可让磁盘故障根本不进 BeeGFS 的
  target 路径。
- **整机故障仍然救不回来**（社区版）：一台服务器倒下，它的 storage target 随
  之消失——本地 ZFS/RAID 扛得住坏*盘*，扛不住烧掉的*机器*。无 license 的缓解
  手段：把 `meta`/`mgmtd` 服务在多台上做重复（免费——这是**服务冗余**，文件
  系统仍能挂载、管理操作仍可运转）、对元数据做快照、并在负载允许时把 storage
  数据当作"可重灌"处理（训练集可以重新生成——这是社区版诚实的取舍）。想要真
  正的跨节点镜像，只能 temporary 全功能试用证（免费、限期、一系统一张）或付
  费 enterprise license。
- **元数据同样值得照顾**：meta target 丢失则整棵树读不出来（与数据是否冗余无
  关）——它的目录也应放本地镜像（体量很小）。pgcli 不做 BeeGFS 备份：保护
  mgmtd 注册表与 meta target 靠的是宿主级快照——对承载角色目录的卷做 ZFS 快
  照，或在守护进程停住时做一次静默文件拷贝。

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
`pg.yaml` 里手改的非默认 `meta_port`/`storage_port`。跨主机则各绑各的、互不冲
突——这正是 v2 用 `--roles` 扩角色的原因（见「跨主机集群（v2）」）。因此**没有
`beegfs_start_port` 端口池**——与其他插件不同，BeeGFS 的端口是固定的，不是分
配的。

## 连接认证与 TLS

文件系统里的每个守护进程与所有客户端共享一个**连接认证密钥**（`ConnAuth`，经
`CONN_AUTH_FILE_DATA` 环境变量交给镜像 init 写成 `/etc/beegfs/conn.auth`）。
首装自动生成（20 位随机）**仅限跑 mgmtd 的宿主**、打印一次、存入 `pg.yaml`，
之后用 `pg addon password beegfs` 取回；跨主机 join 宿主必须把这个既有值经
`--password` 带过去（见「跨主机集群（v2）」），pgcli 绝不替既有集群的成员生成
密钥。与其他 pgcli secret 一样，`ApplyDefaults`
绝不把它默认成字面量。

**mgmtd 的 TLS 是关闭的**（`--tls-disable true`，v1 的明确决策）。这意味着只
有同样带 `--tls-disable` 的客户端能入集群，且这是真实的暴露考量：通配
`--listen 0.0.0.0` + TLS 关闭 = 网络上任何握着 conn-auth 密钥的机器都能触达
8008/8010/8005/8003。非私有网络请绑回环（`--listen 127.0.0.1`）或对这四个口
做防火墙。

**License**：自 BeeGFS 8 起每个部署都应有 license。**pgcli 从不生成
license**（与 Gitea 加密 secret 同一原则），但**可搬运**你申领的那张：PEM
**内容存入 `pg.yaml`**（随配置备份一起走），注入 mgmtd 容器为
`BEEGFS_LICENSE_FILE_DATA`——官方镜像的 init 把它写成
`/etc/beegfs/license.pem`，mgmtd 的 start.sh 再转成 `--license-cert-file`。
只有 mgmtd 宿主能带 license：它的镜像是唯一自带 license 库的，证书由它的
守护进程为整个文件系统校验（join 宿主传 `--license-file` 会被安装直接拒绝）。

license 三档（全部走 ThinkParQ 自助门户，社区版一分钟可领）：

| 档位 | 价格 | 作用 |
|---|---|---|
| **community** | 免费 | 解除无 license 的限制（8.4 的五个客户端挂载封顶、quota 告警）；有容量上限，**不**解锁企业功能 |
| **temporary** | 免费试用 | 限期全功能；8.4 起一个系统限持一张 |
| **enterprise** | 付费 | storage pools、quota、mirroring（伙伴镜像）、管理节点 HA 双活 |

> **重点：mirroring（伙伴镜像）是 ENTERPRISE 功能——必须付费 license。**
> 无 license 时 mgmtd 拒绝一切镜像请求（`beegfs mirror create` →
> "no license loaded"，实机核验）。免费的 community license **不解锁**它，
> 只有付费 enterprise（或限期全功能的 temporary 试用证）才行。社区版的
> 数据冗余止步于多跑几个 meta/mgmtd daemon——那是**服务冗余，不是数据镜像**。

申领在体系外完成：`beegfs license --get` 打印**系统绑定的申领 URL** → 浏览器
打开 → 下载 `.pem`。然后交给 pgcli——安装时 `--license-file license.pem`，
或事后 `pg addon beegfs license --set`（见「License 状态与热换」一节）。

**无 license** 跑 8.4 的现实是：mgmtd 打 quota 告警、每条 `beegfs` 命令都
带 warning、客户端挂载**上限五个**（第六个被拒）、企业功能直接拒绝（如
`beegfs mirror create` 报 "no license loaded"）。装上任意有效 license——包
括免费的社区版——即解除告警与挂载上限。

## 挂载客户端

客户端是宿主**内核模块**——pgcli 不装。要在 Debian/Ubuntu 主机上挂载文件系统，
需自行添加 ThinkParQ apt 仓库并安装客户端包。8.4 + Debian 12（bookworm）的完整
命令（已在测试机实测）：

```bash
# 1. BeeGFS 签名 key（指纹 0683 AF2B 9AC5 BC7D 59B2 D623 40CE 4E24 3CD5 0804）
wget https://www.beegfs.io/release/beegfs_8.4/gpg/GPG-KEY-beegfs -O /etc/apt/trusted.gpg.d/beegfs.asc
# 2. repo 文件（bookworm = Debian 12；Debian 13 用 beegfs-trixie.list，
#    Ubuntu 用 beegfs-jammy/noble.list，……）
wget https://www.beegfs.io/release/beegfs_8.4/dists/beegfs-bookworm.list -O /etc/apt/sources.list.d/beegfs.list
apt update
# 3. 客户端 + 工具；内核模块由 DKMS 编译
apt install beegfs-client beegfs-utils beegfs-tools linux-headers-$(uname -r)
```

RHEL 系主机上同一仓库以 `.repo` 文件发布于
`https://www.beegfs.io/release/beegfs_8.4/dists/` 下（`rpm --import
…/GPG-KEY-beegfs` + `wget …/beegfs-rhel9.repo -O
/etc/yum.repos.d/beegfs.repo`）。URL 一律以 [ThinkParQ 下载
页](https://www.beegfs.io/download/) 上*你的* BeeGFS 版本 + 发行版对应的为准——
服务端 pin 在 8.4，客户端就应挂对应 8.4 的仓库。

`beegfs-client` 依赖 `build-essential`、推荐内核头文件——DKMS 在安装时对着
`linux-headers-$(uname -r)` 编译 `beegfs` 模块，所以运行中内核的头文件必须就位，
且每次内核升级都会重新编译。

然后照抄 `pg addon beegfs mount-hint` 的输出——它逐字渲染三个文件：

```bash
pg addon beegfs mount-hint --name fs1
```

```
1. /etc/beegfs/conn.auth（0600）——与服务端完全相同的密钥
2. /etc/beegfs/beegfs-client.conf——main 段设置：
     sysMgmtdHost=<mgmtd-host>      # 仅当 mgmtd BeeMsg 口被移动过时才有 connMgmtdPort=<port>
3. /etc/beegfs/beegfs-mounts.conf（一行一个挂载，字段：
   <挂载点> <client-conf> <fstype>）：
     /mnt/beegfs /etc/beegfs/beegfs-client.conf beegfs
```

> **conn.auth 的坑：** 客户端的连接认证文件必须以换行符结尾。
> `echo <secret> > /etc/beegfs/conn.auth` 可以；`printf <secret> > …`（无行尾换
> 行）会被拒为 `tried to authenticate stream with wrong secret`——尾部那个字节
> 是两侧比对内容的一部分。

挂载与验证：

```bash
systemctl restart beegfs-client            # 读 beegfs-mounts.conf 并挂载
df -h /mnt/beegfs
echo hi > /mnt/beegfs/t && cat /mnt/beegfs/t   # 端到端写读
```

由于集群关着 TLS，每个 `beegfs` CLI 调用都要带关 TLS 的形态、指向 gRPC 口；
`node list` 会列出全部四类节点（management、meta、storage，以及挂载成功后的本客
户端）：

```bash
beegfs --tls-disable --mgmtd-addr=<mgmtd-host>:8010 node list
```

常用客户端操作（全部经 8.4 实测——`beegfs` CLI 是 8.x 的重写版，7.x 那套
`beegfs-*-ctl` 用法不适用）：

```bash
B="beegfs --tls-disable --mgmtd-addr=<mgmtd-host>:8010"

$B node list                          # 所有节点：mg / meta / storage / client
$B target list                        # storage target、所属节点与状态
$B entry info /mnt/beegfs/big.bin     # 单文件的条带模式，以及各 chunk 落在哪些 target
$B entry set /mnt/beegfs/data --num-targets 3 --chunk-size 4Mi
                                      # data/ 下新建文件条带到 3 个 target、4 MiB 块
                                      #（目录设的模式作用于其后创建的内容）
$B entry set /mnt/beegfs/data --num-targets 0   # 恢复集群默认
$B health check                       # 全集群健康报告
$B stats server                       # 各服务器实时 IO 计数（8.x 的 stats）
```

`mount-hint` 是纯本地渲染——从不碰 podman，客户端本就不归 pgcli 管。mgmtd 地
址按配置条目渲染：跨主机成员上是存好的 `--mgmtd-host`；本机 `--listen` 为通配
时打印占位符 `<mgmtd-host-ip>`（0.0.0.0 不可连），而不是客户端连不上的值。

## 健康检查

```bash
pg addon beegfs check             # 默认名
pg addon beegfs check --name fs1
```

报告**本机角色**的聚合状态（`running` / `stopped` / `partial (mgmtd,meta)`），
并按服务给出容器是否在跑、是否打出就绪日志行——mgmtd 是
`Waiting for shutdown signal`（在服务），meta/storage 是
`Registration and management info download complete`（已入集群）。镜像都不
含 `beegfs-ctl`/`check-servers`，所以守护进程自己的日志就是权威的*容器内*信
号；集群完整成员表在 mgmtd 里——看全局用上面的 `beegfs` gRPC CLI
（`node list`）。`check` 对停着的集群同样可用（它报状态，不要求运行中）。

## 取回连接认证密钥

```bash
pg addon password beegfs             # "beegfs" 文件系统
pg addon password beegfs --name fs1
```

`pg addon list` 只显示 presence 标记（`Auth: none` / `set (connection-auth)`），
只有加 `--show-password` 才打印密钥本身——随手 list 绝不泄密。

## License 状态与热换

```bash
pg addon beegfs license                     # mgmtd 怎么看这张证书？
pg addon beegfs license --name fs1 --set /path/license.pem   # 装载/更换，重启 mgmtd
pg addon beegfs license --name fs1 --clear                    # 摘掉回到无 license
```

在 **mgmtd 宿主**上跑（license 只活在那台——见「连接认证与 TLS」）。不带参数
时报告两侧：`pg.yaml` 里有没有 PEM，以及 mgmtd 加载它时自己打了什么——有效
证书是 `Successfully loaded license certificate`，坏/过期证书则是 mgmtd 的
校验错误行。pgcli 只做 PEM 形状检查（BEGIN/END 块），真正的校验永远是
mgmtd 的事。

`--set <pem>` 读文件、把内容写进 `pg.yaml`，然后**重建 mgmtd 容器**让新 env
落地（podman 的 env 只在创建时存在）。meta/storage 和全部客户端会自行重注册
——换证只需几秒且不碰数据。`--clear` 抹掉 PEM 同样重启，回到无 license 态。
`--set` 与 `--clear` 互斥；安装时带 `--license-file` 是同一件事的首次装载
（容器本来就在那时创建，无需重建）。

## List

```bash
pg addon list
```

BeeGFS 区块（"Infra add-ons (beegfs)"）每个文件系统一条：按本机角色计的聚合
状态、角色列表、mgmtd 地址与两个口（join 宿主标注 remote）、固定的 meta/storage
口、conn-auth presence、镜像、本机角色对应的数据目录（每个 storage target 一
行）、容器名。podman 不可用时仍然列出配置条目（状态显示 `n/a`）。

## 启停

容器带 podman 的 `unless-stopped` 重启策略，宿主重启后随 podman 服务自动回
来。`start`/`stop` 按依赖顺序显式管理**本机承担的角色**：

```bash
pg addon stop beegfs     # storage → meta → mgmtd（反序，target 保持 GOOD）
pg addon start beegfs    # mgmtd → meta → storage
```

跨主机成员只动自己那一个子集——`--roles storage` 的宿主 start/stop 只有
storage。

## 开机自启

```bash
pg autostart enable --beegfs --name fs1
```

BeeGFS 是**唯一的集群粒度 autostart**：`pg start --autostart` 拉起本宿主所跑的
文件系统部分（按 mgmtd → meta → storage 顺序，或配置的角色子集），而不是单个
容器。因为镜像以 root 运行并
自属目录，autostart 除 podman 外不需要任何宿主权限（没有 Gitea 那样的 chown
环节）。每台挂载主机上的客户端经由自己的 `beegfs-mounts.conf` 自行重挂——
pgcli 从不管理那一步。完整机制、状态与关闭方式见
[Autostart]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove beegfs                # 保留数据目录——同名重装复活文件系统
pg addon remove beegfs --clean-data   # 连目录一起删（数据没了）
```

移除按反序拆**本机角色**（storage → meta → mgmtd）。`--clean-data` 拒删仍是
挂载点的目录。
**指向本宿主的客户端挂载点无论如何都得先卸载**——pgcli 够不着客户端（那不归
它管），请先去客户端主机上卸载。

## 日志

一个文件系统最多横跨三个容器，所以 `pg logs addon beegfs` 用 `--service` 选一
个（默认 `mgmtd`）：

```bash
pg logs addon beegfs                       # mgmtd（默认）
pg logs addon beegfs --service meta -f     # 跟踪元数据守护进程
pg logs addon beegfs --service storage -n 200
```

`--service` 只对 `beegfs` 有效（配其他插件类型时报错）。

## 排障

- **`install beegfs --name fs2` 报 "a BeeGFS filesystem already exists on this
  host"**——这是一宿主一文件系统规则，不是 bug：8.4 里 meta（8005）与
  storage（8003）的口动不了，同宿主第二个文件系统永远绑不上。扩容请在**另一台
  宿主**上加角色（`pg addon beegfs join-hint` 渲染逐字命令），或先 `pg addon
  remove` 掉第一个。
- **`check` 显示 meta/storage 一直 not registered**——mgmtd 必须先在服务；
  mgmtd 容器若退出，两者会阻塞到 `beegfsSetupTimeout`（60 秒）让安装失败而非
  挂死。看 `pg logs addon beegfs --service mgmtd` 找 mgmtd 退出原因（宿主上
  端口被占、或数据目录不可写）。
- **`install` 在 `meta_port`/`storage_port` 配置校验处拒绝**——`pg.yaml` 被手
  改出了非默认值。守护进程根本绑不了那些口，删掉钉死的值回落 8005/8003。
- **客户端挂不上 / `beegfs` CLI 报 TLS 错误**——集群跑的是 mgmtd TLS 关闭；所
  有客户端与 `beegfs` CLI 调用都必须同样带 `--tls-disable`，且
  `/etc/beegfs/conn.auth` 必须与 `pg addon password beegfs` 的值**逐字相同**。
- **join 宿主秒报 "wrong secret"**——它的 install 传入的 `--password` 不是集群
  既有的连接认证值（BeeGFS 日志打 `tried to authenticate stream with wrong
  secret`）。安装几秒内中止而不是等注册超时；到 mgmtd 宿主用
  `pg addon password beegfs` 取回正确值后重装。
- **`pg addon beegfs license` 打出 mgmtd 的校验错误**——存进去的 PEM 对
  *这台* 系统无效（证书是系统绑定的——申领要在 mgmtd 宿主跑
  `beegfs license --get`）或已过期。容器照常跑、社区功能不受影响，mgmtd
  只是表现为无 license 态；`--clear`（或用对的 PEM `--set`）修复配置侧。
- **别的机器对着 `localhost` 挂载失败**——通配 `--listen 0.0.0.0` 时
  `mount-hint` 把 `sysMgmtdHost` 渲染成占位符；用 `--listen <可达IP>`（配
  `--force`）重装，客户端才能拿到真实 mgmtd 地址。

## 已知限制

- **仅 Linux。** host 网络是硬性要求，podman 只在 Linux 提供；macOS 上安装被
  拒。不存在 bridge 兜底——BeeGFS 不是 HTTP。
- **一宿主一文件系统、端口固定。** pgcli 钉死原生口
  （8008/8010/8005/8003）；8.4 里 meta/storage 的监听口不可移动，单宿主只容得
  下一个文件系统。向外扩展是跨主机（`--roles`），不是同宿主第二个文件系统。
- **pgcli 从不安装、从不挂载客户端。** 客户端是内核模块（DKMS），无法容器
  化；pgcli 只管服务端容器，挂载靠 `mount-hint` *输出文档*。
- **TLS 关闭；license 可搬运、不代发。** mgmtd 以 `--tls-disable true` 运行，
  `beegfs` gRPC CLI 与客户端必须同样带 `--tls-disable`。pgcli 从不生成
  license、也不管续费——它只把你线下申领（`beegfs license --get` 打印系统
  绑定 URL）的 ThinkParQ PEM 搬进 mgmtd 容器，`pg addon beegfs license`
  查看与热换。mirroring、pools、quota 仍是需要购买的 enterprise license
  功能；无 license 的 8.4 客户端挂载上限为五个。
- **跨主机部署是逐机手动的（v2 设计如此）。** 每台宿主各自跑自己的
  `pg addon install beegfs --roles ...`；pgcli 从不 SSH，也没有"从一台机器部
  署整个集群"的命令。扩容 = 在新宿主上跑一条命令（conn-auth 密钥靠运维手工
  带过去——见 `join-hint`），每台宿主的 `pg.yaml` 只描述自己那一片。
- **mgmtd 是单点。** 管理守护进程就是集群的注册表；它停摆后，新注册与管理操作
  停摆（已挂载的客户端照常对 meta/storage 做 IO）。管理节点双活是 BeeGFS
  *企业 license* 功能，不在 pgcli 的 v1/v2 接线范围内。
