---
title: "Patroni 集群备份"
description: "Patroni HA 集群的 pgBackRest 备份：backup setup、S3 仓库与 WAL 归档、pg ha snapshot 快照操作，以及副本 LSN 卡住时的 reinit 修复"
weight: 50
---

本页给出 **Patroni HA 集群备份的完整操作流程**：从 `pg backup setup` 打通备份
基础设施，到 `pg ha snapshot` 做全量/增量/差异备份，再到一个运行态坑——副本
`Replay LSN` 卡住导致 `pg ha status` 显示成员 LSN 不一致时，如何用
`patronictl reinit` 拉回。命令与前置机制的参考细节见
[Patroni HA](../ha/#备份跨主机-leader) 与
[备份](../../backup/#s3-对象存储仓库)；本页把它们串成一条能照着跑完的路径。

## 模型：集群一个 stanza，备份容器 SSH 找 primary

一个 Patroni 集群（scope）对应**一个** pgBackRest stanza：`pgcli_<scope>`
（与带 namespace 的 DCS scope 一致，两个 pgcli namespace 共用一个 S3 桶也撞不上）。
这个 stanza 是 **cluster-wide** 的——备份侧配置把每个成员列成一个 `pg*-host`，
pgBackRest 自己 SSH 探测、定位当前的 primary，并在 failover 后自动跟随，**无需改
任何配置**。因此：

- **不存在"per-member 备份"**。`full` / `incr` / `diff` 都是对整个集群这一个 stanza
  操作，pgBackRest 从当时的 leader 打快照。
- 备份从共享的 **backup 容器**里发起，通过 SSH 连到 primary。跨主机成员也照样可达
  （信任在 `setup` 时自动打通，见下）。
- `pg ha snapshot` 命令**不需要你指定 leader**——传对 scope 即可，primary 由
  pgBackRest 定位。

## 前置：`pg backup setup`

`pg backup setup` 一次跑完，把备份基础设施全部拉起并让集群具备归档能力：

```bash
# 最简（备份数据落在默认 base-dir 下）
pg backup setup

# S3 对象仓库（Patroni 集群归档到 MinIO / AWS S3 的前提）。
# 存储主机在别处、手头还没有 ca.crt 时，先用一次 TLS 握手把它拉下来——无需 scp：
pg backup fetch-ca 10.0.0.9:9000          # 打印 SHA-256 指纹供核对
pg backup setup --s3-endpoint 10.0.0.9:9000 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-ca-file ~/.pgcli/backup/repo-ca/ca-10.0.0.9-9000.crt
```

setup 做的事（配了 S3 仓库时，动手前先做一次 **endpoint 预检**：普通 TCP
拨号，打错主机名或存储没起会立刻失败退出，而不是拖到镜像拉取 / 容器启动之后才
把真正原因埋进噪声里）：

1. 构建/拉取 pgbackrest 镜像、建网络与目录、生成 backup 容器视图的
   `pgbackrest.conf` 与成员本地归档视图的 `pgbackrest-archive.conf`。
2. 拉起共享 backup 容器，随后**验证仓库连通性**：跑一次 `pgbackrest repo-ls`，
   验证整条链路（TLS、凭证、bucket），并把失败映射成可操作的提示——证书错误指向
   `pg backup fetch-ca`、access-denied 指向凭证、拒绝/超时指向 endpoint。
3. **打通跨主机备份互信**：各主机 `pg ha create` 时把本机备份**公钥**发布进集群的
   etcd 注册表，`setup` 把全集群成员公钥合并成一份每集群一份的 `authorized_keys`
   bind-mount 进成员容器——sshd 每次登录重读该文件，所以**后加入的主机无需重启就被
   信任**。S3 仓库 CA 走同一条路：配了 `ca_file` 的主机把证书发布进注册表，留空的
   加入者自动拉取。（详见 [HA → 备份跨主机 leader](../ha/#备份跨主机-leader)。）
4. **开启 WAL 归档**：检测到成员归档配置过期时，pause 集群、按"副本先、leader 后"
   重建过期成员容器（recreate 同时就是 `archive_mode` 所需的 postmaster 重启）、
   resume，并把同一份 `archive_command` 渲染进每个成员的 `patroni.yml`。
5. 对每个集群 stanza 执行 `stanza-create` + `check`。

> **HTTPS 是硬性要求。** pgBackRest 拒绝明文 S3。要接收集群归档的 MinIO 必须以
> TLS 提供服务——用 `pg addon install minio --tls`（pgcli 自签 CA，把 `ca.crt`
> 填进 `--s3-ca-file`）。MinIO 在另一台机器时，用 `pg backup fetch-ca <存储主机>:<端口>`
> 一次 TLS 握手取回 CA，免 scp（见 [MinIO → 远端主机取 CA](../minio/#tls--tls)）。

配好后确认：

```bash
pg backup status
```

backup 容器 `Up` 且 stanza 就绪，即可开始备份。

> **每个主机都要先 setup，再 create 成员。** 成员的归档能力在**创建时**就被冻结：
> 容器只有在 `pgbackrest-archive.conf` 已存在时才挂载它，渲染出的 `patroni.yml`
> 也只有在配置了 S3 仓库时才带归档 GUC。所以在某主机跑 `pg backup setup` *之前*
> `pg ha create` 出来的成员，天生**不带 WAL 归档**——`pg ha snapshot`/`pg ha
> restore` 覆盖不到它，要等之后的某次 `setup` 把它标记为过期、在 pause 窗口内
> 重建才能补上。`pg ha create` 会提前就此发出告警；每台主机先跑
> `pg backup setup`，创建出来即是可备份的。

## 快照操作：`pg ha snapshot`

作用在某个集群（scope）上，通过共享 backup 容器对当前 primary 执行 pgBackRest。
这是 `pg snapshot`（单实例）在 Patroni 集群侧的对应物。

```bash
# 全量备份（默认 --type full），--tail-logs 流式打印 pgBackRest 日志
pg ha snapshot create app --tail-logs

# 增量备份（自上次备份以来的变化）
pg ha snapshot create app --type incr

# 差异备份（自上次全量以来的变化）
pg ha snapshot create app --type diff

# 列出该集群的全部快照（表格：Start / Stop / Name / Type）
pg ha snapshot list app
pg ha snapshot list app --limit 5

# 删除指定快照（label 从 list 拿）——不带 --force 有交互确认
pg ha snapshot delete app 20260916-140131F_20260917-015154I
pg ha snapshot delete app <label> --force
```

说明：

- 每次命令前，`pg ha snapshot` 会**自动重渲染** backup 容器视图的
  `pgbackrest.conf`（从当前拓扑），使成员增删/端口变化即时反映到 stanza——bind-mount
  生效，不重建容器。
- **只删得掉非唯一的 full**：pgBackRest 至少保留一个 full，`delete` 命中唯一 full 会
  被拒（先建一个新 full 再删旧的）。
- create 期间 pgBackRest 连到当时的 leader；failover 后换 leader 再跑也成功——这正是
  cluster-wide stanza 的价值。

## 运行态坑：副本 LSN 卡住 → `patronictl reinit`

**症状。** `pg ha status app` 里某个副本的 `Replay LSN` 长期落后于自己的
`Receive LSN`（甚至与另一副本明显不一致），且**不随时间收敛**。注意 Patroni 的
`Lag` 列是相对 leader 的差值，leader LSN 一动全体数字就动；判断真卡住要看副本
**自身**的 recv vs replay，或隔一段时间看 Replay LSN 是否纹丝不动。

**确诊。** 三条证据指向同一结论——该副本的 WAL 回放断点、复制流实际已停：

```bash
# 1) leader 侧无复制客户端、槽 inactive
pg exec --dsn "postgres://<user>@<leader_host>:<port>/postgres" \
  "SELECT client_addr, state FROM pg_stat_replication"
pg exec --dsn "postgres://<user>@<leader_host>:<port>/postgres" \
  "SELECT slot_name, active, confirmed_flush_lsn FROM pg_replication_slots"

# 2) 卡住的副本上没有 walreceiver
pg exec --dsn "postgres://<user>@<replica_host>:<port>/postgres" \
  "SELECT status FROM pg_stat_wal_receiver"

# 3) 副本容器日志在反复刷同一条（pg logs ha 按 scope+成员名取，
#    -f 持续跟随，-n 取更多行）：
pg logs ha <scope> -m <member> -n 50 | grep -iE "prev-link|waiting for WAL"
#   LOG: record with incorrect prev-link ... at 0/20000060
#   LOG: waiting for WAL to become available at 0/20000078
```

`record with incorrect prev-link` + `waiting for WAL to become available` 反复出现，
就是 WAL 流在段边界出现空洞/乱序、startup 进程卡在边界等下一段。此时 Patroni 往往
仍每周期报"no action. I am ... a secondary, and following a leader"——它认为在跟随、
实际 postmaster 的 walreceiver 已停摆，**不会自愈**。这与备份无关，是复制运行态问题。

**修复：reinit 该副本。** 让 Patroni 从 leader 重新 `pg_basebackup` 一份、重开复制。
这是对**该副本数据目录的破坏性重建**（不影响 leader 与其他副本），所以 Patroni 要求
`--force`：

```bash
# CLUSTER_NAME 是带 namespace 的 scope（pg ha status 里 Cluster: 那行的名字）
pg ha ctl <scope> -- reinit <scope>-<ns-suffix> <member> --force
# 例（namespace=default → 后缀 -default）：
pg ha ctl app -- reinit app-default node1 --force
```

**注意 `-f` 无效**：`patronictl reinit` 只认全写 `--force`（不像 `switchover`/`failover`
的 `--yes`）。`Success: reinitialize for member node1` 即已下发。

**验证恢复。** reinit 会清空并重建副本数据目录，随后 basebackup + 回放。轮询确认回到
`recv == replay` 且 `status=streaming`：

```bash
pg exec --dsn "postgres://<user>@<replica_host>:<port>/postgres" \
  "SELECT pg_last_wal_receive_lsn() recv, pg_last_wal_replay_lsn() replay,
          (SELECT status FROM pg_stat_wal_receiver) wr"
#   期望：recv == replay，wr = streaming

pg ha status app    # 该副本 Replay LSN 追平、Lag 收敛
```

reinit 完成后，恢复 `pg ha snapshot create app --type full` 一次全量，把快照基线对齐到
健康的拓扑。
