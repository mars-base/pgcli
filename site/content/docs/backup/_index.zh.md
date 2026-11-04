---
title: 备份
description: pgcli 备份指南
weight: 20
icon: fa-solid fa-clock-rotate-left
cascade:
  type: docs
  footer_style: slim
---


## 快照

```bash
# 创建快照（完整备份）
pg snapshot create -i proj01

# 创建差异备份（推荐）
pg snapshot create --type diff -i proj01

# 快照期间流式输出备份容器日志
pg snapshot create --tail-logs -i proj01

# 列出快照
pg snapshot list -i proj01

# 限制显示的快照数量
pg snapshot list --limit 5 -i proj01

# 删除快照
pg snapshot delete 20260826-073712F -i proj01
```

**快照类型：**
- `full` — 完整备份（默认，自包含）
- `diff` — 自上次完整备份以来的更改
- `incr` — 自上次备份以来的更改

## 共享备份容器

所有实例共享单个 pgbackrest 容器；每个实例在存储库中都有自己的 stanza。

> **一份配置文件一个 `[global]` 仓库。** 容器的 `[global]` 段只指定**一个**默认
> 仓库——`data_dir` 下的本地目录；选择 S3 的 stanza 用自己段内的
> `repo1-type=s3` 覆盖它。于是一个 pgcli 环境里 local 与 S3 的 stanza 可以并存：
> 配置了 `backup.repo.s3` 时 Patroni 集群 stanza 自动使用它，普通实例则只在
> `pg create --backup-target s3` 创建时才用
> （[使用 S3 的实例](../s3-instance/)），其余一律留在本地磁盘。选了 S3 的
> stanza **只**写 S3——不双写，也不迁移已有备份。若想把第二个 store（比如再
> `pg addon install minio` 一个）只喂给部分 stanza，就跑**第二份配置**
> （`pg config init --namespace …`）、配上它自己的备份容器——完整走法见
> [示例：HA 集群 + 自签 CA 的 MinIO](../ha-cluster/ha-example-minio/)。

```bash
# 初始化共享 pgbackrest 容器（构建镜像、创建目录、生成配置）
pg backup setup

# 使用自定义基础目录存储备份数据和日志
pg backup setup --base-dir /mnt/backup

# 启动 / 停止备份容器
pg backup start
pg backup stop

# 删除备份容器及其生成的配置（pgbackrest.conf、ssh_config）。备份数据与
# SSH 密钥对会保留，之后 `pg backup setup` 可直接重新接上同一个仓库；
# --clean-data 则连本地仓库、日志与凭据一起删除。
pg backup remove
pg backup remove --clean-data

# 显示备份容器状态
pg backup status

# 列出 backup 容器管理的全部 stanza（每个 PITR 实例一个、每个 Patroni 集群
# 一个）。下面的 stanza-upgrade 用的就是这些名字——它们是仓库里的备份对象
# 标识，不是容器名。
pg backup list-stanza

# 数据目录被重建后（restore、recreate、reinit）刷新 stanza 元数据。此后
# 备份会报 "[051] system-id ... do not match stanza"；stanza-upgrade 重新
# 同步它而不删除已有备份。必须显式给出 stanza 名（有意不提供全量升级）。
pg backup stanza-upgrade pgcli_default
pg backup stanza-upgrade pgcli_default pgcli_app-default
```

备份基础设施（网络、镜像、目录、配置、容器）在 `pg start` 时自动准备；手动运行 `pg backup setup` 重新初始化，例如更改基础目录之后。

## S3 对象存储仓库

备份与 WAL 归档可以推送到任意 S3 兼容存储（MinIO、silo、rustfs、garage、
AWS S3……）。Patroni 集群在配置后自动使用它；普通实例按实例用
`pg create --backup-target s3` 选择（[使用 S3 的实例](../s3-instance/)）。
配置写在 `pg.yaml` 的 `backup.repo.s3` 下：

```yaml
backup:
  repo:
    s3:
      endpoint: 10.0.0.9:9000     # host:port，不带 scheme
      bucket: pgbackrest
      access_key: admin
      secret_key: <密码>           # 与其它 pgcli 密码一样存于 pg.yaml
      ca_file: /home/you/.pgcli/tls/minio/store/ca.crt   # 私有 CA 时必填
```

然后（重）跑 setup，它会自动完成归档编排：

```bash
pg backup setup
```

- **HTTPS 是硬性要求。** pgBackRest 拒绝明文 S3（上游明确不实现），端点必须
  提供 TLS。pgcli 自带的 MinIO 用 `pg addon install minio --tls` 开启原生
  HTTPS —— pgcli 会生成自签 CA，并把 `ca.crt` 路径填进上面的 `ca_file`。
  外部端点（真实 AWS S3 等公签证书）留空 `ca_file` 即可；实在无法提供 CA 时
  可用 `verify_tls: false` 逃生（不做证书校验）。
- **远端存储主机取 CA——无需 scp。** MinIO 在另一台机器时，用一次 TLS 握手就
  能取回信任锚，不必拷贝文件：`pg backup fetch-ca <存储主机>:9002` 从端点下发
  的证书里取出信任锚，存到 `<base-dir>/backup/repo-ca/` 下并打印 SHA-256
  指纹——这本质是"首次使用即信任"（TOFU），所以信任前先把指纹与存储主机对拍
  一次，再把文件交给 `pg backup setup --s3-ca-file <路径>`。它覆盖 pgcli 会提供
  的两种 TLS 形态：开 `--tls` 的 MinIO（下发的是生成好的"叶+CA"链，取出的是
  签名根）；以及喂了 `pg cert` 自签叶证书的 MinIO（叶本身就是信任锚，原样保存
  即可）。公共 CA 签发的端点根本不需要 `ca_file`，此命令对它们没有意义。
- **跨机 HA 免手工分发。** Patroni 集群分散在多台主机时，`ca_file` 与备份 SSH
  公钥只需在*一台*主机配好：`pg backup setup` 把 CA 发布进集群的 etcd 注册表，
  `ca_file` 留空的加入者自动拉取；各主机的备份公钥在 `pg ha create` 时发布，
  由 `setup` 合并进每个成员的 authorized_keys。注册表里只放**公开材料**（CA
  证书、SSH 公钥），绝不含私钥或 S3 secret_key。详见 HA 文档"备份跨机
  leader"一节。
- **归档自动化。** setup 检测到 Patroni 成员的归档配置过期时，会 pause 集群、
  按"副本先、leader 后"重建成员容器（recreate 即重启，`archive_mode` 是
  postmaster 级参数）、resume。每个成员的 patroni.yml 会注入同一份
  `archive_command`（每个集群一个 stanza，成员互为 `pg*-host`，pgBackRest
  自动定位 primary），WAL 从此持续 push 到 S3。
- **作用范围。** 配置了 S3 仓库时，Patroni 集群的 stanza 自动使用它；普通实例
  只在用 `pg create --backup-target s3` 创建时才推送到它。其余 stanza 备份仍走
  本地仓库，互不影响——按实例的目标选择、local 与 S3 stanza 共存、以及
  `pg replica promote` 如何定目标，见[使用 S3 的实例](../s3-instance/)。

等价的一次性 flag 写法：

```bash
pg backup setup --s3-endpoint 192.0.2.9:9000 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-secret-key '<密码>' \
    --s3-ca-file ~/.pgcli/tls/minio/store/ca.crt
```

（shell 历史会外泄或日志共享的环境，可改为把 `backup.repo.s3.secret_key` 手编
进 `pg.yaml`，不用 flag。）

