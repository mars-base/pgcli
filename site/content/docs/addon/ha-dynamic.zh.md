---
title: "Patroni 动态配置"
description: "存储在 DCS 中的所有可动态配置参数参考"
weight: 46
---

这些参数存储在 DCS（etcd）的 `/service/<scope>/config` 下，应用于集群的**所有**成员。使用 `pg ha edit-config` 修改：

```bash
pg ha edit-config app -- -s loop_wait=5
pg ha edit-config app -- -s 'postgresql.parameters.max_connections=200'
pg ha edit-config app --show          # 查看当前配置
```

> 参考：[Patroni 官方文档](https://patroni.readthedocs.io/en/latest/dynamic_configuration.html)、
> [Pigsty 动态配置](https://pigsty.cc/docs/patroni/config/dynamic/)。

> **`bootstrap.dcs` 是一次性的。** `patroni.yml` 里的 `bootstrap.dcs` 块只在某
> scope 的**第一个**成员 bootstrap 集群时生效。一旦 Patroni 把配置写入 DCS，之后
> 对 YAML 文件中 `bootstrap.dcs` 的任何修改都会被**完全忽略** —— 即使重新执行
> `pg ha create`（重装）也一样。重新跑 `pg ha create` **不会**重新读取
> `bootstrap.dcs` —— Patroni 看到 DCS 里已有 `config` 键，就直接使用它。

| 方式 | 说明 |
|------|------|
| `pg ha edit-config app -- -s key=value` | **推荐**，pgcli 的标准方式 |
| `pg ha ctl app -- edit-config` | 透传到 patronictl，效果一样 |
| Patroni REST API（`PATCH /config`） | 需要能访问到某个成员的 REST API 端口 |

## 核心时序参数

| 参数 | 默认值 | 最小值 | 说明 |
|------|--------|--------|------|
| `loop_wait` | `10` | 1 | 主循环每轮休眠的秒数（锁续租、DCS 更新、状态刷新） |
| `ttl` | `30` | 20 | leader 锁的 TTL（秒）。实际上是自动故障切换触发前的等待时间 |
| `retry_timeout` | `10` | 3 | DCS 和 PostgreSQL 操作的重试超时。如果 DCS 或网络中断时间短于此值，Patroni **不会**对主库执行降级 |

> **修改 `loop_wait`、`retry_timeout` 或 `ttl` 时的约束：**
> ```
> loop_wait + 2 * retry_timeout <= ttl
> ```

## 故障切换参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `maximum_lag_on_failover` | `1048576` | 副本有资格参与 leader 竞选的最大复制延迟（字节） |
| `maximum_lag_on_syncnode` | `-1` | 同步 standby 被健康的异步副本替换前允许的最大延迟（字节）。≤ 0 时 Patroni 不主动替换不健康的同步 standby。设得足够高以避免高事务量期间频繁替换 |
| `max_timelines_history` | `0` | DCS 中保留的时间线历史条目最大数量。0 = 保留全部 |
| `primary_start_timeout` | `300` | leader 从故障恢复的秒数，超时则触发故障切换。0 = 检测到崩溃立即切换（异步复制下可能丢失事务）。最大切换时间 = `loop_wait + primary_start_timeout + loop_wait`；设为 0 时仅为 `loop_wait` |
| `primary_stop_timeout` | — | 停止 PostgreSQL 时的等待秒数（仅在 `synchronous_mode` 启用时生效）。如果停止操作超过此超时，Patroni 向 postmaster 发送 SIGKILL。≤ 0 或未设置 = 无效果 |
| `failover_timeout` | `0` | leader 丢失后触发故障切换前的等待秒数。0 = 立即 |

## 复制模式

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `synchronous_mode` | `false` | 启用同步复制（`off` / `on` / `quorum`）。leader 管理 `synchronous_standby_names`；只有最后已知的 leader 或同步 standby 可以竞选 leader。保证零数据丢失，代价是无法保证持久性时写入不可用 |
| `synchronous_mode_strict` | `false` | 没有可用的同步 standby 时，拒绝禁用同步复制——阻塞客户端对 leader 的所有写入 |
| `synchronous_node_count` | `1` | 同步 standby 节点数。成员加入/离开时动态调整。自动限制为符合条件的节点数 |
| `failsafe_mode` | `false` | 启用 [DCS 故障安全模式](https://patroni.readthedocs.io/en/latest/dcs_failsafe_mode.html)：DCS 不可达时，leader 保持运行而不自我降级 |

## PostgreSQL 设置

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `postgresql.use_pg_rewind` | `false` | 使用 `pg_rewind` 让失败的 leader 重新加入（比全量 `pg_basebackup` 快）。需要数据页校验和（initdb 时 `--data-checksums`）或 `wal_log_hints=on` |
| `postgresql.use_slots` | `true` | 使用复制槽（副本断开时防止 WAL 丢失）。PostgreSQL 9.4+ 默认启用 |
| `postgresql.pg_hba` | — | 生成 `pg_hba.conf` 的规则。如果 PostgreSQL 的 `hba_file` 参数设为非默认值则忽略 |
| `postgresql.pg_ident` | — | 生成 `pg_ident.conf` 的规则。如果 `ident_file` 为非默认值则忽略 |
| `postgresql.parameters` | — | PostgreSQL GUC 的键值对，例如 `{max_connections: 100, wal_level: "replica", wal_log_hints: "on"}`。许多是复制正常工作所必需的 |
| `postgresql.recovery_conf` | — | standby 配置的附加 `recovery.conf` 条目（PG12+ 透明处理） |

通过 `postgresql.parameters` 键设置 PostgreSQL 参数：

```bash
pg ha edit-config app -- -s 'postgresql.parameters.max_connections=200'
pg ha edit-config app -- -s 'postgresql.parameters.work_mem=64MB'
pg ha edit-config app -- -s 'postgresql.parameters.wal_log_hints=on'
```

## 备用集群

如果定义了此节，集群将作为**备用集群**引导，从远端主库流式复制。

| 参数 | 说明 |
|------|------|
| `standby_cluster.host` | 远端主库地址 |
| `standby_cluster.port` | 远端主库端口 |
| `standby_cluster.primary_slot_name` | 复制用的槽名（可选，默认为成员名） |
| `standby_cluster.create_replica_methods` | 从远端主库引导备用 leader 的方法有序列表 |
| `standby_cluster.restore_command` | WAL 恢复命令 |
| `standby_cluster.archive_cleanup_command` | 备用 leader 的归档清理命令 |
| `standby_cluster.recovery_min_apply_delay` | 应用 WAL 记录前的延迟 |

## 复制槽

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `member_slots_ttl` | `30min` | 副本关闭后其物理复制槽的保留时间。0 = 成员键从 DCS 过期后立即删除。仅在 PostgreSQL 11+ 生效 |
| `slots` | — | 永久复制槽（哈希映射）。在 switchover/failover 期间保留。PG11+ 的物理槽在所有节点上创建，每 `loop_wait` 秒推进一次。逻辑槽通过重启从主库复制到副本，然后每 `loop_wait` 秒推进一次。需要 `use_slots: true` |
| `ignore_slots` | — | 由外部管理、Patroni 不应触碰的槽（属性集列表）。任何子集匹配都会导致该槽被忽略 |

### 永久槽示例

```yaml
slots:
  permanent_physical_slot:
    type: physical
  permanent_logical_slot:
    type: logical
    database: my_db
    plugin: pgoutput

ignore_slots:
  - name: externally_managed_slot
    type: physical
```

### 节点固定的物理槽

对于固定的集群拓扑，为每个节点定义永久物理槽，防止临时中断期间槽被删除：

```yaml
slots:
  node1:
    type: physical
  node2:
    type: physical
  node3:
    type: physical
```

> **警告：** 永久复制槽仅从 **primary**/**standby_leader** 同步到副本。应用程序应在 leader 节点上使用它们。在副本上使用永久槽会导致整个集群的 `pg_wal` 无限增长。例外：与 Patroni 成员名匹配的物理槽（由 Patroni 创建和维护）在所有节点间同步，用于节点间复制。

## 查看当前配置

```bash
pg ha edit-config app --show
```

或直接从 etcd 读取：

```bash
ETCDCTL_ENDPOINTS=http://10.0.0.1:2379 pg etcdctl get /service/app-default/config -- --prefix
```
