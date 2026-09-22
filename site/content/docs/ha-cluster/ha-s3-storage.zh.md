---
title: "S3 存储高可用方案"
description: "让 pgBackRest 背后的 MinIO/silo 对象存储也高可用：pgcli 暴露的四种部署形态（SNSD/SNMD/MNSD/MNMD）、原生多盘与 ZFS 磁盘冗余层的取舍，以及「每台主机多块盘的分布式集群」的两条路"
weight: 55
---

Patroni 集群能扛住一个节点失联；它的 WAL 与备份流向的那个 S3 仓库却必须同
样扛得住，否则"高可用"在备份这条路上悄无声息地就断了。store 是一个
[MinIO](../addon/minio/) 或 [silo](../addon/silo/) 插件（两者可互换——silo 是
Pigsty 的 MinIO 分支，功能面完全一致）。这里真正要紧的是两个**互相正交**的
故障域：

| 故障 | 由谁吸收 |
|------|----------|
| 一个节点内的一块**磁盘**坏了 | SNMD/MNMD 下的盘级 EC，或 MinIO 数据目录底下的存储层（ZFS） |
| 一整个**节点**下线 | MinIO 自己的跨主机纠删码（EC） |

本页记录 pgcli 支持如何把两者组合起来，以及如何在其中取舍。

## 部署形态

MinIO 按节点数与每节点盘数给自己的布局分类
（[SNSD / SNMD / MNSD / MNMD](../addon/minio/#部署形态)）。pgcli 四种全部暴
露：

| 形态 | 结构 | 适用场景 |
|------|------|----------|
| **SNSD**（单机单盘） | 单节点、单个数据目录——默认 | 开发、测试、演示——以及配上下一节的 ZFS 后，任何需要磁盘冗余的单机部署 |
| **SNMD**（单机*多*盘） | 单节点、多块盘——每个 `--drive` 传一块盘 | 单机、N ≥ 4 块数据盘、要扛住坏盘又不想额外搭文件系统层 |
| **MNSD**（多机单盘） | ≥ 4 节点、每节点一个数据目录 | 紧凑的高可用部署 |
| **MNMD**（多机*多*盘） | ≥ 2 节点、每节点多块盘——`--drive` 加上完整 `--endpoint` 矩阵 | 不搭文件系统层就要同时扛住坏盘与坏节点 |

SNMD 原生支持：`pg addon install minio --drive /mnt/disk1 --drive ...`
（每盘一个 flag）会拉起一个单进程 MinIO，在盘之间做纠删码。它能换来什么（在
活的 4 盘集上实测）：默认 2 片校验，可容忍 2 块盘故障；坏 1 块盘时读写都照常，
坏 2 块盘时读仍成功、写被拒（quorum 边界）；可用容量约为原始总量的一半；回来
的盘由 MinIO 自己 heal。同样的 `--drive` 在 [silo](../addon/silo/) 上也可用。

MNMD 同样是原生支持：保留 `--drive` 传本节点的盘，再用 `--endpoint` 补上整
个集群的 host×drive 端点矩阵——每台、每盘各一条 URL，各自指名该节点的
`/data1../dataN` 槽。在活体的 4 节点 × 4 盘集上实测（minio 与 silo 皆然）：
16 盘在线报**单个 stripe 大小 16 的纠删码集合、EC:4**；损失一整个节点
（12/16）读写照常；再损失第二个节点（8/16）写被拒、读也失败，节点重启后自
愈回 16/16。完整步骤见
[插件 → MinIO → 多机多盘（MNMD）](../addon/minio/#多机多盘mnmd)。

### SNMD 与 ZFS：扛住坏盘的两条路

两者都保护单主机数据抵御坏盘，区别在冗余放在哪一层、各自代价是什么。

| | 原生 **SNMD/MNMD**（`--drive`） | `--data-dir` 底下的 **ZFS** 池 |
|---|---|---|
| quorum 单位 | *盘*——MinIO 把盘计作故障成员 | 对 MinIO 不可见——一块大盘，池吸收坏盘 |
| 布局日后可否改 | install 时定死（盘集合就是 EC 集合） | 随时可换——换盘、扩容、`raidz1`→`raidz2`，`--data-dir` 始终不变 |
| 4 盘可用容量 | 约一半（2 数据 + 2 校验） | 看 vdev：`raidz2` = 2×盘，`raidz1` = 3×盘（更省） |
| 重建 | MinIO heal 回来的盘 | ZFS 本地 resilver |
| 服务哪些形态 | SNMD（单机）与 MNMD（跨主机） | SNSD *与* MNSD（同一配方垫在每个节点下） |
| 异构节点 | 每个节点必须贡献相同盘数 | 2 盘节点与 6 盘节点看起来一样（各一个 endpoint） |

坦白的取舍是：**原生多盘更简单**——一条命令、不用置备文件系统、零 ZFS 配置就
拿到磁盘冗余。**ZFS 更灵活**——底层布局随时可换、同盘下省出更多可用容量
（`raidz1` 4 盘留 3 份，而 SNMD 的 EC:2 只留 2 份），而且它能容纳彼此并不一致的
节点。于是：

- 单机、要磁盘冗余又不想碰 ZFS → **SNMD**
- 单机、要更多可用容量 / 日后还想换布局 → **SNSD + ZFS**
- 分布式集群、盘和节点都要扛 → 两条路现在都是一等的：**原生 MNMD**，或
  **MNSD + 每节点 ZFS**——见下文"4 主机、每主机多块盘的高可用"

单机上两条路都不算错——SNMD 那 50% 容量是不用管理池子的代价，ZFS 的灵活是置
备池子的代价。下文继续讲 ZFS 这条路，以及多机场景下的 MNMD 替代方案。

## ZFS：更灵活的磁盘层

两种形态（SNSD 与 MNSD）用的是同一套配方：用主机上的数据盘建一个 zpool，为
store 开一个 dataset，把 `--data-dir` 指到它的挂载点。MinIO 只看到"一块又大
有可靠的盘"，永远不知道底下有几块物理盘。

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

正是上表指向的那个混合形态，而且有两条一等的路：**原生 MNMD**——MinIO 在每
台节点的每块盘之间做纠删码；或**MNSD + 每个节点数据目录底下的 ZFS**——
MinIO 每节点只见一块盘，坏盘由本地池吸收。两条路的失败方式不同，所以这是个
真实的取舍。

### 原生 MNMD

一条矩阵、没有文件系统层：每个节点用 `--drive` 传自己的盘，每个节点携带完全
相同的 host×drive 端点列表。TLS 方面，签一张共享叶子、每个节点装同一对文件
（见下文实测之后的 TLS 说明）。

```bash
# 任意一台上执行一次——一张 SAN 覆盖所有节点地址的自签叶子：
pg cert --host 10.0.0.11,10.0.0.12,10.0.0.20,10.0.0.21 \
        --cert-file grid.crt --key-file grid.key
# 把 grid.crt + grid.key 复制到每个节点——各处都是逐字节相同的文件

# 节点 1（10.0.0.11），四块数据盘已挂载；节点 2-4：同样的命令、同一份
# grid.crt/grid.key、各自的 --drive 路径、完全相同的 16 条端点矩阵、完全相同
# 的 --root-password：
pg addon install minio --name store \
  --tls-cert grid.crt --tls-key grid.key \
  --listen 0.0.0.0 \
  --drive /mnt/minio/disk1 --drive /mnt/minio/disk2 \
  --drive /mnt/minio/disk3 --drive /mnt/minio/disk4 \
  --root-password '<共享密码>' \
  --endpoint https://10.0.0.11:9000/data1 --endpoint https://10.0.0.11:9000/data2 \
  --endpoint https://10.0.0.11:9000/data3 --endpoint https://10.0.0.11:9000/data4 \
  --endpoint https://10.0.0.12:9000/data1 --endpoint https://10.0.0.12:9000/data2 \
  --endpoint https://10.0.0.12:9000/data3 --endpoint https://10.0.0.12:9000/data4 \
  --endpoint https://10.0.0.20:9000/data1 --endpoint https://10.0.0.20:9000/data2 \
  --endpoint https://10.0.0.20:9000/data3 --endpoint https://10.0.0.20:9000/data4 \
  --endpoint https://10.0.0.21:9000/data1 --endpoint https://10.0.0.21:9000/data2 \
  --endpoint https://10.0.0.21:9000/data3 --endpoint https://10.0.0.21:9000/data4
```

在活体的 4 节点 × 4 盘集上实测（minio 与 silo 结果一致）：16 盘集合报**单个
stripe 大小 16 的纠删码集、EC:4**——16 块盘里坏 4 块仍然健康。

| 事件 | 在线 | 效果（实测） |
|------|------|--------------|
| 坏 1–4 块盘 | ≥ 12/16 | 读写照常；回来的盘由 MinIO heal |
| 1 个节点下线（它的 4 块盘） | 12/16 | 读*和*写照常——64 MiB 往返逐字节一致 |
| 2 个节点下线 | 8/16 | 写被拒（`Resource requested is unwritable`）、读也失败 |
| 节点重启 | 16/16 | 自愈 |

可用容量随校验比例而定：16 盘 EC:4 保留原始字节的 **12/16**。代价是：布局在
install 时定死（矩阵就是 EC 集合）、每个节点必须贡献相同盘数，而 pgcli 查不
出*另一台*节点上写歪的矩阵——保持 N 份 `pg.yaml` 一致是运维的职责。跨 grid 的
TLS，**服务一份共享证书**：用 `pg cert --host <所有节点地址，逗号分隔>` 签一
张自签叶子，再用 `--tls-cert`/`--tls-key` 装到每个节点（同一对文件各处逐字节
相同，端点矩阵全部 `https://`）。grid 随即成环，没有任何逐节点 CA 需要对齐
——在活体的 4 节点集上验证过：环以 `Network: 4/4 OK` 起来、`CAs/` 目录为空，
因为那张共享叶子自身就是信任锚，而每个节点本来就持有它。退路才是生成模式：
若让每个节点各跑裸 `--tls`，每台主机会签出自己的*私有* CA，第一次跨节点握手
死在 `x509: certificate signed by unknown authority`，直到你手工把某一份共享
CA 种进每个节点的证书目录——而共享 `pg cert` 这条路把这个活儿整个消掉了。

### MNSD + 每节点 ZFS

**主机之间走 MNSD，每个节点的目录底下各自 ZFS。** 每个节点只暴露*一个*
endpoint（它 ZFS 支撑的 `/data`）：磁盘故障由本地池自愈，根本不上报到
MinIO；节点故障由 EC quorum 吸收。

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

### 两条路怎么选

| | 原生 **MNMD** | **MNSD + 每节点 ZFS** |
|---|---|---|
| 置备 | 每节点一条矩阵命令，不用置备文件系统 | 每节点先 zpool + dataset，再每节点一个 endpoint |
| EC 数什么 | 盘——丢一台节点只是 16 个成员里少了 4 个 | 节点——坏盘根本到不了 MinIO 面前 |
| 实测故障余量 | 4 节点 × 4 盘集：到 12/16 盘（一整个节点）都正常，8/16 失效 | 4 节点：读到 2/4，写到 3/4 台节点 |
| 可用容量 | 该集合上是原始的 12/16（EC:4）——校验片数是 MinIO 为这个 stripe 选的 | 本地 `raidz1` 每池留 3/4，再经跨节点 EC 减半 |
| 日后可否改布局 | install 时定死——矩阵就是 EC 集合 | 可改——换盘、vdev、布局都不碰 MinIO |
| 异构节点 | 不可能：每个节点必须贡献相同盘数 | 天然支持——每节点只是一个 endpoint，本地布局随意 |
| TLS | 一张共享证书服务整个 grid：`pg cert` 签一张覆盖所有节点地址的叶子，`--tls-cert`/`--tls-key` 各处一致地装上（没有 CA 要对齐） | 同理——节点之间仍是 TLS grid，同一套共享叶子配法照用 |

节点彼此一致、盘的规划已经定死、只靠 MinIO 就要同时拿到盘与节点的冗余、底下
什么都不想置备——选 **MNMD**。各节点的盘数/盘大小不一、布局日后可能变、或者习
惯按"整台节点"而不是"逐块盘"来推理故障——选 **MNSD + ZFS**。

## 怎么选

| 场景 | 形态 |
|------|------|
| 笔记本 / 演示 / CI | SNSD，默认数据目录——无需决策 |
| 一台主机、N ≥ 4 块数据盘，备份要扛得住坏盘 | SNMD（`--drive` × N）——零文件系统配置；或 SNSD + ZFS（机械盘 `raidz2`、快 SSD `raidz1`）换更多可用容量与可改布局 |
| 几台主机、每台一块数据盘，要扛得住丢主机 | 纯 MNSD |
| 彼此一致的主机、每台多块数据盘，盘和主机都要扛得住 | MNMD（`--drive` + 完整 `--endpoint` 矩阵）——无文件系统层；或 MNSD + 每节点 ZFS，见上文"两条路怎么选" |
| 各主机盘数/盘大小不一致 | MNSD + 每节点 ZFS（各节点布局可不同；容量对齐）——MNMD 要求各节点盘数相同 |

store 的 TLS 故事与拓扑无关：无论选哪种形态，`--tls`（或 `pg cert` 签发的自
带证书）行为一致——见[生成证书](../ha-cert/)。

## 相关

- [插件 → MinIO](../addon/minio/) —— 安装、`--tls`、分布式模式的约束（本页
  赖以展开的硬性规则），以及 [MNMD 完整流程](../addon/minio/#多机多盘mnmd)
- [插件 → Silo](../addon/silo/) —— 可互换的分支；形态完全相同
- [集群备份](../ha-backup/) —— 仓库如何接进 Patroni（`pg backup setup`）
- [示例：HA 集群 + 自签 CA 的 MinIO](../ha-example-minio/) —— 单机形态的端到
  端实测流程
