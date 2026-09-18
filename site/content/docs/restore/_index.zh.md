---
title: 恢复
description: pgcli 时间点恢复（PITR）指南
weight: 30
icon: fa-solid fa-clock-rotate-left
cascade:
  type: docs
  footer_style: slim
---


## 时间点恢复（PITR）

恢复到首次备份后的任意时间点。

```bash
# 恢复（只读，提交前检查）
pg restore --time "2026-08-26 15:30:00+00"

# 预览将要恢复的内容而不执行（试运行）
pg restore --time "2026-08-26 15:30:00+00" --dry-run

# 恢复期间流式输出恢复容器日志
pg restore --time "2026-08-26 15:30:00+00" --tail-logs

# 如果需要可以尝试不同的时间
pg restore --time "2026-08-26 15:25:00+00"

# 提升为读写（切换时间线）
pg restore --time "2026-08-26 15:30:00+00" --promote

# 跳过确认
pg restore --time "2026-08-26 15:30:00+00" --promote --force
```

**时间格式：**
- `2026-08-26 15:30:00+08:00` — 带时区偏移
- `2026-08-26 15:30:00+08` — 仅时区小时
- `2026-08-26 15:30:00Z` — UTC
- `2026-08-26 15:30:00` — 假定为 UTC

**恢复工作流：** 停止 → 恢复 → 启动 → WAL 重放到目标时间

**注意：** `--promote` 之后，在进行进一步的 PITR 之前需要创建新的完整快照。

## Patroni 集群

Patroni HA 集群用 `pg ha restore` 恢复，是上面单实例命令的集群版对应物。它接受
相同的 `--time`（全部格式），并支持 `--dry-run`、`--tail-logs`、`--force`。

```bash
# 预览恢复计划，不触碰集群
pg ha restore app --time "2026-08-26 15:30:00+00" --dry-run

# 恢复，同时流式输出承载 bootstrap 成员的恢复日志
pg ha restore app --time "2026-08-26 15:30:00+00" --tail-logs

# 指定由哪个本机成员承载 bootstrap（默认：第一个本机成员）
pg ha restore app --time "2026-08-26 15:30:00+00" --member node1

# 跳过确认提示
pg ha restore app --time "2026-08-26 15:30:00+00" --force
```

**原理：** 先 **pause 集群**（冻结 failover，免得本机停掉 leader 时远端副本被
promote 抢走 leadership），再移除集群的 DCS 身份，然后让一个**本机**成员通过
Patroni 的自定义 bootstrap 方法，从 pgBackRest 仓库把（已清空的）数据目录恢复到目标
时间点。该成员在**新时间线**上启动并自动 promote 成可写 leader；其余成员——本机的、
跨主机的——都经 DCS 重新加入它。前提是该 stanza 已配好 WAL 归档，由带 S3 仓库的
`pg backup setup` 预置（见 [HA 备份插件](../ha-cluster/ha-backup)）。

**与单实例恢复的差异：**

- **总是 promote。** 没有"挂起只读、检查、再换个时间重试"的两步——集群在目标点
  直接以可写状态起来。执行前用 `--dry-run` 确认目标时间。
- **必须在拥有成员的主机上运行，且动手前先检测 leader。** `pg ha restore` 在执行
  任何破坏性操作**之前**，会先从 DCS 读取当前 leader，判断它是否是本机的可控成员。
  真实执行时若 leader 不在本机（远端成员，或本机视图里根本没有的跨主机成员），会
  **直接拒绝**；`--dry-run` 则只是告警。原因：破坏性路径会清掉 DCS 身份并让本机
  成员重新 bootstrap，若本机停不掉当前 leader，它会在 **旧时间线** 上抢先重新抢回
  DCS，与 bootstrap 竞态。先把 leader 切到本机（`pg ha switchover`/`failover`），
  或到 leader 所在主机执行。leader 读不到/为空时不阻断（覆盖首次 bootstrap 的场景）。
- **没有 `--promote` flag** —— promote 是自动的。

**恢复工作流：** 预检（leader 必须是本机成员）→ pause 集群 → 停本机成员 → 清 DCS →
清空并用仓库 bootstrap 一个成员 → 它 promote 成新时间线的 leader → 其余成员作为副本
重新加入。

**恢复之后：** 建一个新的完整快照以给新时间线重新定位——
`pg ha snapshot create app --type full`。这步通常直接成功：同一 stanza 的
pgBackRest 恢复在时间线切换时保持 system-id 不变，所以首个快照不会报
`[051] system-id ... do not match stanza`，也不需要 `stanza-upgrade`。若有副本没有
自动重新加入，用 `pg ha ctl app -- reinit app-<ns> <member> --force` 重建它。
