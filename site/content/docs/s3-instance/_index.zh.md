---
title: 使用 S3 的实例
description: 让普通 pgcli 实例直接把备份写入共享 S3 仓库 —— pg create --backup-target s3、不双写、提升时自动选目标
weight: 25
icon: fa-solid fa-cloud-arrow-up
cascade:
  type: docs
  footer_style: slim
---

普通实例（非 Patroni）的 WAL 归档与 pgBackRest 备份默认落在**本地磁盘**。本页
讲的是可选的另一条路：在 `pg create` 时就选定，把两者直接写到本环境共享的
**S3 仓库**（`backup.repo.s3` —— MinIO、silo、rustfs、garage、AWS S3……）。

HA 集群的行为不同，不在本页范围：Patroni stanza 只要检测到 S3 配置就自动使用
它——见[备份 → S3 对象存储仓库](../backup/#s3-对象存储仓库)与
[HA 集群 → S3 存储](../ha-cluster/ha-s3-storage/)。

## 前提：全局 S3 仓库

`backup-target: s3` 的实例不带自己的端点或凭据——它引用 `pg.yaml` 里那一份
主机级的 `backup.repo.s3`。S3 存储通常跑在**自己的机器**上（比如一台专职的
`pg addon install minio` 主机），与运行 pgcli 实例的主机分开——实例主机只需要
网络可达端点，外加 store 的 TLS 信任锚。设存储主机 `192.0.2.9`、实例主机
`192.0.2.10`：

```bash
# 在存储主机上：装一个提供 HTTPS 的 MinIO（pgBackRest 拒绝明文 S3）
pg addon install minio --tls
```

```bash
# 在实例主机上：用一次 TLS 握手取回信任锚——无需 scp
pg backup fetch-ca 192.0.2.9:9000      # 打印 CA 路径 + SHA-256 指纹
```

把指纹与存储主机对拍一次（这本质是"首次使用即信任"），然后登记仓库——完整的
块需要 endpoint + bucket + access_key + secret_key：

```bash
pg backup setup --s3-endpoint 192.0.2.9:9000 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-secret-key '<密码>' \
    --s3-ca-file <fetch-ca 打印的路径>
```

（shell 历史会外泄或日志共享的环境，可改为把 `backup.repo.s3.secret_key` 手编
进 `pg.yaml`，不用 flag。）其余机制与 Patroni 场景完全一致——
[备份 → S3 对象存储仓库](../backup/#s3-对象存储仓库)讲清了 HTTPS 硬性要求、
`verify_tls` 与公签端点（真实 AWS S3 直接省掉 `--s3-ca-file`）。这个 S3 块必须
**完整**——不完整的仓库会在 `pg create`、`pg start`、`pg backup setup` 三处被拒
绝，绝不静默退回本地。而且它是**按主机**的：多主机部署里，每个跑实例、或将提
升副本的主机都照同样方式各配各的 `backup.repo.s3`（跨机 HA 集群则是另一套——
CA 与 SSH 公钥经 etcd 分发，见备份文档"跨机 HA 免手工分发"一条）。

## 创建落在 S3 的实例

```bash
pg create -i proj01 --backup-target s3
```

`--backup-target` 接受 `local`（默认）与 `s3`。选择按实例记录
（`instances.<name>.pitr.backup_target: s3`），重载后仍在；local 实例干脆不写
这个键。

这个 flag 在底层改了什么：共享备份容器里该实例的 stanza 获得一段 per-stanza 的
`repo1-type=s3` 覆盖（端点、bucket、region、path、凭据、CA），PG 容器内
`archive-push` 所用的 hostless 配置同样如此。`[global]` 仓库保持本地，两类目标
因此互不干扰。

**不双写。** s3 目标的 stanza **只**写 S3 —— 备份与 WAL 都不再进本地仓库，两边
都不留副本。日后把实例指向另一个仓库时也不会迁移：stanza 从零开始一条新的备份
链，旧本地备份就此自然过期。

**目标在创建时定死。** 运行中的实例没有切换开关——要换，就用期望的目标新建
实例再照常迁移数据。

## local 与 s3 实例共存

一个环境、一个备份容器、混合目标：

```bash
pg create -i local01              # 本地仓库（默认）
pg create -i cloud01 --backup-target s3
```

```bash
pg backup list-stanza
# pgcli_local01   ← 走 [global] repo1-path，本地磁盘
# pgcli_cloud01   ← stanza 内的 repo1-type=s3 覆盖
```

保留策略、压缩、stanza 生命周期（`stanza-create`、`stanza-upgrade`、过期清理）
两者完全一致——只是字节的落点不同。配置了 S3 时，`pg backup setup` 的连通性
探测会优先挑一个真正用 S3 的 stanza。

## 副本：提升之前不配备份

副本——无论同主机还是跨主机——**不**获得任何备份配置：它跟随主实例流式复制
期间 PITR 保持禁用，归档始终是主实例的职责（副本的 WAL 经物理复制到达，主实例
上的 PITR 已经覆盖它）。

`pg replica promote <name>` 会自动决定新主实例的备份目标——而且是在调用
`pg_promote()` **之前**：

| 情形 | 结果 |
|------|------|
| 本机有完整的 `backup.repo.s3` | 提升，备份目标 = **s3** |
| 无 S3 配置（同主机或跨主机副本） | 提升，备份目标 = **local** |
| `backup.repo.s3` 存在但不完整 | 提升**中止**（补好这个块再重跑） |

所以没在本机配 S3 的跨主机副本，与同主机副本行为完全一致：提升为一个走 **local**
备份的主实例，在本机开一条新的备份链。要注意这条新链与旧主实例的仓库无关——提升
之前的 WAL 存在*旧*主机的仓库里，因此无法从本机 PITR 回到故障切换点之前。如果想
让提升后的实例继续用 S3，先在本机配好仓库（就是前提一节那对
`pg backup fetch-ca` + `pg backup setup --s3-*`——store 仍是那台远端机器），提升时
就会解析成 `s3`：

```bash
pg backup setup --s3-endpoint 192.0.2.9:9000 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-secret-key '<密码>' \
    --s3-ca-file <fetch-ca 打印的路径>
pg replica promote ra2
```

提升成功后，stanza 创建、`archive_command` 接线、重启 PostgreSQL 全都照
[故障切换](../failover/) 文档所述进行——用的就是上表定下的目标。

## 恢复与 PITR

无需任何改动：对 s3 目标实例执行 `pg restore --time ...`，pgBackRest 经由同一段
per-stanza 覆盖从 S3 读取备份与 WAL。配置了 `ca_file` 时，临时的恢复容器会自动
获得 CA 挂载。私有 CA 的 store 之所以能走通，是因为 CA 证书被以只读方式
bind-mount 到 `/etc/pgbackrest/ca.crt`，PG 容器（archive-push）与恢复容器都挂
—— `pg` 在创建容器时加上这个挂载，而 `pg start` 会**重建**早于该挂载存在的旧
PG 容器（数据在卷里，stop-rm-create 无损）。

## 另见

- [备份](../backup/) —— 共享备份容器、快照、S3 仓库配置块
- [恢复](../restore/) —— PITR 流程与时间格式
- [故障切换](../failover/) —— `pg replica promote` 逐步说明
- [HA 集群 → S3 存储](../ha-cluster/ha-s3-storage/) —— Patroni 侧的等价能力
