---
title: "Patroni 集群恢复"
description: "用 pg ha restore 为 Patroni HA 集群做时间点恢复（PITR）：自定义 bootstrap 机制、leader 本机性预检、端到端操作流程，以及恢复之后的重新基线步骤"
weight: 51
---

本页是 **[Patroni 集群备份](../ha-backup/)** 的恢复侧姊妹页。集群一旦有了带 WAL
归档的 stanza（`pg backup setup` + `pg ha snapshot`），`pg ha restore` 就能把整个集群
回到某个时间点——它是单实例 [`pg restore`](../../restore/) 的 Patroni 集群对应物。本页
讲清机制（为什么集群 PITR 不是简单跑一次 `pgbackrest restore`）、关于 leader 所在主机
的安全性预检、端到端操作流程，以及恢复之后必须做的重新基线步骤。命令 / flag 细节同时见
[恢复 → Patroni 集群](../../restore/#patroni-集群)；本页把它们串成一条可照着跑的流程。

## 前提：本机要有带 WAL 归档的 stanza

PITR 只能重放到已经归档的部分。恢复前，集群必须已经具备：

- 针对 S3 仓库预置好的集群 stanza——`pg backup setup --s3-endpoint ...`
  （见 [备份](../../backup/) 与 [HA 备份](../ha-backup/)）；
- 至少一个**全量**快照，加上覆盖目标时间点的 WAL 段
  （用 `pg ha snapshot create <scope> --type full` 产生）。

目标时间点被这段历史框住：早于最新全量备份的 stop 时间、或晚于最后归档的 WAL，恢复
都落不到那里。

**`pg backup setup` 必须在*本机*跑过，且 backup 容器要在运行。** 对跨主机集群而言，
这条比听起来更严格：

- 承载 bootstrap 的成员从 S3 恢复，靠的是挂进它容器里的仓库配置——
  `pgbackrest-archive.conf` 挂到 `/etc/pgbackrest.conf`，外加 S3 CA 挂到
  `/etc/pgbackrest/ca.crt`。两者都由 `pg backup setup` 生成，且容器只在文件存在时才挂载。
  从没跑过 setup 的主机没有仓库配置可挂，`pgbackrest restore` 的 bootstrap 就连不上 S3、
  起不来。
- leader 本机性与 stop 时间的预检，都是在 **backup 容器内**跑 `pgbackrest ... info`。
  `pg ha restore` 只会 best-effort 刷新共享的 `pgbackrest.conf`，**不会**生成成员的
  archive 配置，也**不会**启动 backup 容器——所以要先把容器拉起来（`pg backup setup`，
  或用 `pg backup status` 确认它是 `Up`）。backup 容器没起时，预检会被跳过，真正的失败
  要拖到 bootstrap 阶段才暴露；而恢复之后的 `stanza-upgrade` 与新快照本来就依赖它在运行。

一句话：在**已用 `pg backup setup` 预置好 S3 仓库、且 backup 容器为 `Up`** 的主机上
运行 `pg ha restore`。

## 机制：自定义 bootstrap PITR，而非裸跑 `pgbackrest restore`

Patroni 集群不能在存活的 postmaster 下直接 `pgbackrest restore` 来做 PITR——数据目录、
时间线、DCS 身份都归 Patroni 管。`pg ha restore` 走的是 Patroni 的**自定义 bootstrap**
配方：

1. **清除集群的 DCS 身份**（`patronictl remove`）。Patroni 的 `bootstrap.dcs` 只在 DCS
   没有 config key 时才执行，所以移除它正是重新武装 bootstrap 路径的动作。
2. **选一个本机成员**，改写它的 `patroni.yml`，把默认的 initdb bootstrap 替换成一条指向
   目标时间点的 `pgbackrest restore` method：

   ```yaml
   bootstrap:
     method: pgbackrest
     pgbackrest:
       command: 'pgbackrest --stanza=pgcli_<scope> --type=time --target="<time>" --target-action=promote --delta restore'
       no_params: true
       keep_existing_recovery_conf: true
   ```

   `no_params: true` 阻止 Patroni 追加 `--scope`/`--datadir`（`pgbackrest` 会以
   `[031] invalid option` 拒绝它们）；`keep_existing_recovery_conf: true` 保留 `pgbackrest
   restore` 自己写下的 `recovery.signal` + `restore_command` + `recovery_target_*`。
   `method`/`pgbackrest` 这两个键是 `bootstrap.dcs` 的**兄弟**，绝不嵌进它里面——恢复相关的
   GUC 若泄漏进 DCS 配置，会在 promote 后的 leader 上作为运行时参数生效。
3. **清空该成员的 PGDATA** 并重建其容器。Patroni 面对空数据目录 + 无 DCS 配置，跑该
   method，在**新时间线**上恢复到目标点，并借 `--target-action=promote` 把自己 promote
   成可写 leader。
4. **其余成员重新加入**新 leader：本机其它成员被清空并作为标准副本重启（对新 leader 做
   `pg_basebackup`）；跨主机成员经 DCS 重新加入（或被 reinit）。

## leader 本机性预检

**除非当前 leader 是本机的一个成员，否则 `pg ha restore` 拒绝执行。** 动手之前它先从 DCS
读 leader，判断它是不是本机可控成员之一。

**原因。** 第 1 步清掉 DCS、第 3 步让一个本机成员在新时间线上重新 bootstrap。活在另一台
主机上的 leader 其 Patroni daemon 一直在跑——本机停不掉它——所以 DCS key 一旦被移除，那个
远端 leader 会**抢着在旧时间线上重新夺回 leadership**，与本机 bootstrap 相撞。要求 leadership
在本机（从而可被停止）就消除了这个竞态。

**你会看到什么。**

```bash
# dry-run：一条醒目告警，不拒绝——你仍可检视计划
pg ha restore app --time "2026-08-26 15:30:00+00" --dry-run
#   [!!] current leader "node3" is NOT a local member — the real run REFUSES until
#        leadership is on this host...

# 在 leader 为远端的主机上真实执行：硬报错，什么都不碰
pg ha restore app --time "2026-08-26 15:30:00+00"
#   current leader "node3" is not a local member of scope "app" ...
#   Move leadership here first (pg ha switchover app ...) or run on the leader's host
```

**怎么办。** 把 leadership 挪到你正站着的这台主机的某个成员上，再重试：

```bash
pg ha switchover app    # 或： pg ha failover app ...
pg ha status app        # 确认 leader 现在是本机成员
```

或者改到 leader 自己所在的主机上跑 `pg ha restore`。leader **为空 / 读不到**时（例如最早
的 bootstrap、还没有任何成员 publish 自己）**不**阻断执行。

## 与单实例 `pg restore` 的差异

- **总是 promote。** 没有"只读 pause、检查、再换个时间重试"的两步——bootstrap 恢复以一个
  新时间线上的可写 leader 收场。执行前先用 `--dry-run` 确认目标时间。
- **没有 `--promote` flag**——promote 是自动的（它烧进了 bootstrap 命令里）。
- **必须在拥有成员的主机上运行**，且 leader 必须是本机的（见上文预检）。

## 操作流程：把集群恢复到某个时间点

完整 flag 集：`--time`（必填，格式见[恢复](../../restore/)）、
`--member`、`--dry-run`、`--tail-logs`、`--force`。

**1. 用 dry-run 确认目标安全。** 什么都不碰；你能看到 stanza、选中的 bootstrap 成员、确切的
`pgbackrest` 命令，以及（若相关）leader 本机性告警。

```bash
pg ha restore app --time "2026-08-26 15:30:00+00" --dry-run
```

**2. 确保 leader 在本机**（仅当 dry-run 告警时）。switchover，然后复查状态。

```bash
pg ha switchover app
pg ha status app
```

**3. 恢复。** 默认会弹确认提示，列出 scope、stanza、目标时间、bootstrap 成员，以及
"PERMANENTLY LOST" 警告。用 `--tail-logs` 流式输出恢复日志；用 `--force` 跳过提示以便
自动化。

```bash
pg ha restore app --time "2026-08-26 15:30:00+00" --tail-logs
# 或者显式指定承载 bootstrap 的成员：
pg ha restore app --time "2026-08-26 15:30:00+00" --member node1 --force
```

**4. 确认新集群起来。** `waitForLeader` 轮询 DCS 直到出现 leader（上限 15 分钟），随后
重新加入的成员作为副本启动。

```bash
pg ha status app                     # 新 leader 在已切换的时间线上，副本在 streaming
pg exec --dsn "postgres://<user>@<leader_host>:<port>/postgres" \
  "SELECT ... FROM your_table"       # 数据停在目标时间；之后的提交都没了
```

## 恢复之后：给新时间线重新基线

恢复不是最后一步。数据目录被重建过，所以在集群重新具备备份能力之前，有两件收尾必须做：

1. **建一个新的全量快照**，让后续 PITR 在新时间线上有基点：

   ```bash
   pg ha snapshot create app --type full
   ```

   重建数据目录会改变 PostgreSQL 的 **system-id**，所以这个首快照可能报
   `[051] system-id ... do not match stanza`。用 stanza 升级**非破坏性**地修复，然后重试
   快照：

   ```bash
   pg backup stanza-upgrade pgcli_app-<ns>
   pg ha snapshot create app --type full
   ```

2. **拉回任何没有自动重新加入的成员**——通常是丢了旧 leader 的跨主机成员。从新 leader
   重新初始化它（只破坏该副本的数据目录）：

   ```bash
   pg ha ctl app -- reinit app-<ns> <member> --force
   ```

   （完整细节见 [HA 备份](../ha-backup/) 的"卡住的副本 → reinit"一节。）

## 故障排查

- **"no local member on this host"**——你在一台不拥有该 scope 任何成员的主机上运行。
  `pg ha restore` 只能重建本机的数据目录 + 容器。到拥有成员的主机、或 leader 所在主机上运行。
- **"target time is before the latest backup stop time"**——最早可用点是最新全量备份的 stop
  时间；报错会打印它并给出建议的 `--time`。若需要更晚的基点，先做一个更新的全量快照。
- **恢复超出最后归档的 WAL**——不能恢复到没归档过的时间点。调早目标时间，或先确保
  `archive_command` 在正常投递（`pg backup status`）再重试。
- **恢复后首个快照报 `[051]`**——数据目录重建后属预期；跑 `pg backup stanza-upgrade`（见上），
  它是非破坏性的。
