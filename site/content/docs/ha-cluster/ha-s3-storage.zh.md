---
title: "S3 存储高可用方案"
description: "让 pgBackRest 背后的 MinIO/silo 对象存储也高可用：pgcli 暴露的两种部署形态及其原因，以及用 ZFS 补上磁盘级冗余的灵活方案——单机 raidz 池、分布式集群每节点各自建池、异构节点皆可"
weight: 55
---

Patroni 集群能扛住一个节点失联；它的 WAL 与备份流向的那个 S3 仓库却必须同
样扛得住，否则"高可用"在备份这条路上悄无声息地就断了。store 是一个
[MinIO](../addon/minio/) 或 [silo](../addon/silo/) 插件（两者可互换——silo 是
Pigsty 的 MinIO 分支，功能面完全一致）。这里真正要紧的是两个**互相正交**的
故障域：

| 故障 | 由谁吸收 |
|------|----------|
| 一个节点内的一块**磁盘**坏了 | MinIO 数据目录底下的存储层（ZFS） |
| 一整个**节点**下线 | MinIO 自己的跨主机纠删码（EC） |

本页记录 pgcli 支持如何把两者组合起来，以及暴露出来的形态为什么止于两种。

## 两种部署形态

MinIO 按节点数与每节点盘数给自己的布局分类
（[SNSD / SNMD / MNSD](../addon/minio/#部署形态)）。pgcli 暴露其中两种：

| 形态 | 结构 | 适用场景 |
|------|------|----------|
| **SNSD**（单机单盘） | 单节点、单个数据目录——默认 | 开发、测试、演示——以及配上下一节的 ZFS 后，任何需要磁盘冗余的单机部署 |
| **MNSD**（多机单盘） | ≥ 4 节点、每节点一个数据目录 | 紧凑的高可用部署 |

### 为什么只有两种

MinIO 与 silo 本身支持的形态比这两种多——**SNMD**（单机*多*盘）以及"多机且
每机多盘"都是真实存在的模式，走的是 MinIO 的 path-style endpoint 语法（单节
点挂 `/data{1...4}`，或一台主机×多盘的矩阵）。pgcli 只是**没有把这两种部署
形态做进插件里**：`--endpoint` 每个节点收一个可路由主机、`--data-dir` 是单个
目录，所以上面两种形态就是当前 CLI 能表达的全部。

这是有意收的范围，不是我们觉得该补上的缺口。磁盘冗余本该是*文件系统*层的
事，而 ZFS 比 MinIO 自带的多盘模式做得更好、更灵活：

- **一套机制同时服务两种形态。** SNSD 底下的 `raidz1` 保护单主机 store；同
  一套配方垫在 MNSD 集群的每个节点下，就保护了一个分布式 store。而 MinIO 的
  SNMD/MNMD 布局只作用于声明它的那个节点。
- **底层布局可以随时改。** 两块盘换成镜像、扩容、迁到 `raidz2`——
  `--data-dir` 始终不变，MinIO 毫无察觉。MinIO 自己的盘集合在 install 时就定
  死了。
- **节点可以异构。** 2 盘节点与 6 盘节点在 MinIO 眼里一模一样（各一个
  endpoint）。换成 MinIO 级的多盘，每个成员都得向集群描述自己的盘。
- **quorum 算术保持简单。** 写需要 ⌈N/2⌉+1 个*节点*。一旦节点内部的盘也算作
  故障成员，"这集群能扛住什么"就不再一目了然了。

所以分工是：**MinIO 负责节点级 EC，ZFS 负责磁盘级冗余**——每一层都保持简单。

## ZFS：更灵活的磁盘层

两种形态用的是同一套配方：用主机上的数据盘建一个 zpool，为 store 开一个
dataset，把 `--data-dir` 指到它的挂载点。MinIO 只看到"一块又大有可靠的盘"，
永远不知道底下有几块物理盘。

```bash
# 用主机上的空闲盘建池（挂载点默认就是 /minio-pool）
sudo zpool create minio-pool raidz1 /dev/sdb /dev/sdc /dev/sdd

# 为 store 开一个 dataset：recordsize 1M 适配对象大文件，关掉 atime 写放大
sudo zfs create -o recordsize=1M -o atime=off minio-pool/store

pg addon install minio --name store --data-dir /minio-pool/store   # 或 silo
```

池必须落在与根文件系统不同的设备上——这恰好也是
[MinIO 的盘检查](../addon/minio/#分布式--集群模式)与 pgcli install 时那条提示
性警告要的东西（`drive is part of root drive, will not be used`）。建在自己
盘上的 ZFS 挂载点天然满足。

### 按盘数选布局

| 数据盘数 | SNSD（ZFS 是唯一防线） | MNSD 节点（上层 EC 兜住节点损失） |
|----------|------------------------|--------------------------------------|
| 1 | 无冗余——dev/test 可以，坏一块盘就等于重建 store | 每节点一块大盘就是原生 MNSD 形态，无需 ZFS |
| 2 | `mirror` | `mirror` |
| 3 | `raidz1`（可用 2D） | `raidz1` |
| 4 | 机械盘用 `raidz2`；SSD 重建快、容量金贵时用 `raidz1`；写密集用 2 × `mirror` | `raidz1`——一格校验就足以把坏盘挡在 MinIO 视野外 |
| 5–8 | `raidz2`（可用 (N−2)D） | `raidz1`，大盘为 `raidz2` |
| > 8 | 拆成两个较小的 `raidz2`/`raidz1` vdev 条带组池——跨 12+ 块盘的 resilver 暴露窗口太长 | 同理：单个 vdev 控制在 ~8 块盘以内 |

两列的差别只在一层推理：SNSD 下 ZFS 既要扛住坏盘、又要扛住重建期间再出的
事，校验深度直接买安全；MNSD 下 ZFS 只需要阻止"坏盘升级成丢节点"，一格校验
加本地快速 resilver 就是它的全部职责——再往上，宁可用更多节点换更深的本地
RAID。

4 盘主机（SNSD）——单机最常见的问题——三种形状细看：

| 布局 | 可用容量 | 可容忍 | 特点 |
|------|----------|--------|------|
| `raidz2`（类 RAID6） | 2 × 盘 | 2 块盘 | 机械盘的稳妥默认：大盘 resilver 很长，raidz1 的单校验撑不住重建期间再丢一块 |
| `raidz1`（类 RAID5） | 3 × 盘 | 1 块盘 | SSD / 小盘的容量优先之选：重建以分钟计，而非小时 |
| 2 × `mirror`（条带化） | 2 × 盘 | 每镜像 1 块盘（分属不同镜像则可容忍 2 块） | 小写性能最好——宽条带 raidz 恰恰是这方面最差的形状 |

**SNSD** 下 ZFS 是 store 唯一的防线，所以默认往保守了选：除非盘足够快、重建
以分钟计、且那格容量真值那点风险——否则用 `raidz2`。

MinIO 那句"EC 模式底下不要做 RAID"的建议，针对的是 RAID 与跨主机 EC **双重
冗余**的浪费。在 **SNSD** 下根本没有 EC——ZFS 是数据唯一的保护——那条建议并
不适用；照字面执行（"单盘、不上 ZFS"）在 4 盘主机上意味着一块盘报废就丢掉
整个 store。

> **与 PostgreSQL 同机时：** ZFS 缓存很激进（ARC 默认最多吃掉一半内存）。数据
> 库同机时请设上限——例如 `/etc/modprobe.d/zfs.conf` 里
> `options zfs:zfs_arc_max=8589934592`——免得两个负载抢内存。

## 4 主机、每主机多块盘的高可用

正是上表指向的那个混合形态：**主机之间走 MNSD，每个节点的目录底下各自
ZFS**。每个节点只暴露*一个* endpoint（它 ZFS 支撑的 `/data`）：磁盘故障由本
地池自愈，根本不上报到 MinIO；节点故障由 EC quorum 吸收。

```bash
# 4 个节点上各自执行：建本地池（布局可按节点不同——见下）
sudo zpool create minio-pool raidz1 /dev/sdb /dev/sdc /dev/sdd
sudo zfs create -o recordsize=1M -o atime=off minio-pool/store

# 节点 1（10.0.0.11）：
pg addon install minio --name store \
  --listen 10.0.0.11 \
  --data-dir /minio-pool/store \
  --root-password '<共享密码>' --tls \
  --endpoint http://10.0.0.11:9000/data \
  --endpoint http://10.0.0.12:9000/data \
  --endpoint http://10.0.0.20:9000/data \
  --endpoint http://10.0.0.21:9000/data

# 节点 2-4：同样的命令、各自的 --listen、完全一致的 endpoint 列表与密码
```

4 节点集群上，两层各自能容忍什么：

| 事件 | 由谁处理 | 后果 |
|------|----------|------|
| 某节点 raidz 池里坏 1 块盘 | ZFS（resilver） | MinIO 无感知，不涉及 quorum |
| 1 个节点下线 | EC（写需要 ⌈4/2⌉+1 = 4 台里的 3 台在线） | 读写照常 |
| 2 个节点下线 | 只剩 EC 读（4 台里的 ⌈4/2⌉ = 2 台） | 可读、拒写，直到有节点回归 |
| 一块盘与它所在的节点一起故障 | EC（3/4） | 依然安全——节点回归后存活池继续自愈 |

让这套方案真正灵活的，是两条性质：

- **endpoint 数 = 节点数，而不是盘数。** 2 盘节点与 6 盘节点在 MinIO 眼里毫
  无区别。4 台主机完全可以跑不同布局——这里 `raidz1` ×4 盘、那里 2 盘
  `mirror`、另一台 `raidz2` ×6 盘——异构不损失任何东西。
- **池容量应大致对齐。** EC 把整个集群的可用容量定在*最弱成员*的空闲空间上
  （3 × 12T 池 + 1 × 4T 池 → 你拿到的是按 4T 条纹化，而不是 40T）。盘确实不
  一致时，精简置备（稀疏 `zvol` dataset，或直接 `zfs set refquota`）对这层错
  配对 MinIO 保密——用 quota 把大池压到小池的尺寸，不多占一分重建的力气。

**为什么不用条带化把每节点的池拿回容量。** MinIO"EC 底下别做 RAID"的建议讲
的是容量，而且这笔账是真的：4 节点 MNSD 的 EC 已经把原始总容量砍掉约一半，
所以每节点用无冗余的纯条带池（所有盘 stripe、零本地冗余）确实比 `raidz1`
多榨出约三分之一。但 EC 是按*节点*数故障成员，而条带池会把"坏 1 块盘"放大
成"整台节点 offline"——一次普通的坏盘，就烧掉了 EC 为"丢一整机"预留的预算
格，逼出跨网络重建整池，且在重建完成前把余量清零。那点容量不是白捡的，是你
悄悄把磁盘容错降级成了节点容错换来的。每节点 `raidz1` 正是两层不再互相掏空间
的临界点：池吸收坏盘、对 MinIO 不可见，EC 的预算留给真正的节点故障。（如果
"榨干一切"的答案确实合适，那个形态是每节点一块大盘的纯 MNSD、根本不上
ZFS——而不是一个把同样单点风险藏低一层的条带池。）

## 怎么选

| 场景 | 形态 |
|------|------|
| 笔记本 / 演示 / CI | SNSD，默认数据目录——无需决策 |
| 一台主机、N ≥ 4 块数据盘，备份要扛得住坏盘 | SNSD + 机械盘用 `raidz2`，快 SSD 用 `raidz1` |
| 几台主机、每台一块数据盘，要扛得住丢主机 | 纯 MNSD |
| 每台主机多块数据盘，盘和主机都要扛得住 | MNSD + 每节点 ZFS（各节点布局可不同；容量对齐） |

store 的 TLS 故事与拓扑无关：无论选哪种形态，`--tls`（或 `pg cert` 签发的自
带证书）行为一致——见[生成证书](../ha-cert/)。

## 相关

- [插件 → MinIO](../addon/minio/) —— 安装、`--tls`、分布式模式的约束（本页
  赖以展开的那三条硬性规则）
- [插件 → Silo](../addon/silo/) —— 可互换的分支；形态完全相同
- [集群备份](../ha-backup/) —— 仓库如何接进 Patroni（`pg backup setup`）
- [示例：HA 集群 + 自签 CA 的 MinIO](../ha-example-minio/) —— 单机形态的端到
  端实测流程
