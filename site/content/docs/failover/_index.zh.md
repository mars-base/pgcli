---
title: 故障转移：副本提升
description: pgcli 故障转移：副本提升指南
weight: 20
icon: fa-solid fa-book
cascade:
  type: docs
  footer_style: slim
---


当当前主实例失败时，将副本提升为新的主实例。pgcli 提供 3 步手动故障转移工作流——每个步骤在其各自的主机上运行，不自动检测同主机与跨主机拓扑。

## 概述

```
                    ┌─────────────┐
                    │   primary   │  ← 崩溃 / 变得不可用
                    │  (pg01)     │
                    └──────┬──────┘
                           │ WAL 流式传输
               ┌───────────┼───────────┐
               ▼           ▼           ▼
          ┌─────────┐ ┌─────────┐ ┌─────────┐
          │  ro1    │ │  ro2    │ │  ro3    │
          │ replica │ │ replica │ │ replica │
          └─────────┘ └─────────┘ └─────────┘
```

故障转移后（提升 ro1 → 新主实例）：

```
                    ┌─────────────┐
                    │   ro1       │  ← 新主实例（已提升）
                    │  (primary)  │
                    └──────┬──────┘
                           │ WAL 流式传输
               ┌───────────┼───────────┐
               ▼           ▼           ▼
          ┌─────────┐ ┌─────────┐ ┌─────────┐
          │  ro2    │ │  ro3    │ │  pg01   │
          │ replica │ │ replica │ │ replica │  ← 降级的旧主实例
          └─────────┘ └─────────┘ └─────────┘
```

## 3 步故障转移

每个步骤都是独立的命令。按**顺序**在各自的主机上运行它们。

```bash
# 步骤 1：在被提升的副本上
pg replica promote ro1

# 步骤 2：在旧主实例主机上（当它恢复时）
pg replica drop ro1 -i pg01

# 步骤 3：在每个剩余副本主机上
pg replica repoint ro2 --primary-dsn "postgres://admin:<pw>@<new-primary-ip>:<port>/<db>" --primary-name ro1
pg replica repoint ro3 --primary-dsn "postgres://admin:<pw>@<new-primary-ip>:<port>/<db>" --primary-name ro1
```

### 步骤 1：`pg replica promote <name>`

在被提升为主实例的副本主机上运行。

```bash
pg replica promote ro1
```

**发生了什么：**

1. 验证实例是副本（`ReplicaOf` 已设置）且容器正在运行
2. 调用 `pg_promote()`（PostgreSQL 12+ 原生提升——无需容器重启）
3. 等待恢复结束（通常亚秒级）
4. 通过 `ALTER SYSTEM RESET` 从 `postgresql.auto.conf` 清理 `primary_conninfo`
5. 更新配置：清除 `ReplicaOf` 和 `PrimaryDSN`，启用 `PITR`
6. 自动初始化 PITR：
   - pgBackRest stanza 创建
   - `archive_mode` / `archive_command` 配置
   - PostgreSQL 重启以应用 postmaster 级别参数
7. 打印下一步指令

**幂等：** 如果副本已被提升（例如通过手动 `pg_ctl promote`），命令会跳到配置更新。

### 步骤 2：`pg replica drop <name> -i <old-primary>`

在**旧主实例主机**上运行以清理复制槽。此步骤仅在特定场景中需要。

```bash
pg replica drop ro1 -i pg01
```

这会在旧主实例上删除物理复制槽 `pgcli_r_ro1`。没有清理，槽会无限期保留 WAL，直到主实例磁盘空间耗尽。

**何时运行：**

| 场景 | 操作 | 原因 |
|------|------|------|
| 旧主实例永久丢失 | **跳过** | 槽随服务器一起消失 |
| 计划将旧主实例降级为副本 | **跳过** | `repoint` 销毁数据目录（包括 `pg_replslot/`），所有槽被隐式移除 |
| 旧主实例已恢复，继续作为独立主实例运行 | **必须运行** | 槽无限期保留 WAL；没有清理磁盘最终会填满 |
| 旧主实例已恢复但将被关闭 | **可选** | 如果实例不再运行，跳过无害 |

> **保持旧主实例不变？** 如果你想保留旧主实例及其原始数据（例如用于取证分析或作为只读归档），你可以简单地不理会它——不要在其上运行 `drop` 或 `repoint`。旧主实例继续作为具有陈旧数据的独立实例运行。只是要注意被提升副本的复制槽仍然存在并会累积 WAL；你可能想要仅删除那个特定的槽（`pg replica drop ro1 -i pg01`）同时保持其他一切不变。

### 步骤 3：`pg replica repoint <name> --primary-dsn <dsn> --primary-name <name>`

在**每个剩余副本主机**上运行以将其重新指向新主实例。

```bash
pg replica repoint ro2 \
  --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@10.241.21.97:35439/pg01_db" \
  --primary-name ro1
```

**发生了什么：**

1. 通过 DSN 查询新主实例的扩展（`pg_extension` 目录）
2. 如果存在非内置扩展（例如 pg_cron、timescaledb），构建本地 `-ext` 镜像并匹配包
3. 停止旧副本容器并销毁其数据目录
4. 通过 DSN 在新主实例上创建复制槽
5. 更新配置：`ReplicaOf`、`PrimaryDSN`、`ImageTag`、`Extensions`，禁用 `PITR`
6. 通过 `pg_basebackup -R` 从新主实例重新初始化
7. 以备用模式启动副本容器

**为什么销毁 + 重建而不是 ALTER SYSTEM SET？**

提升后，新主实例进入新时间线。旧时间线上的其他副本不能简单地更改 `primary_conninfo`——PostgreSQL 会拒绝连接：

```
FATAL: requested starting point on timeline 1 is not in this server's history
```

唯一安全的方法是从新主实例进行完整的 `pg_basebackup`。

### 获取主实例 DSN

从被提升副本主机上的 `pg status` 获取新主实例的连接字符串：

```bash
pg status -i ro1
# Connection: postgres://admin:fbcQx9uIzvTO6dVJ@127.0.0.1:35439/pg01_db
```

将 `127.0.0.1` 替换为从副本主机可达的新主实例主机 IP（例如 `10.241.21.97`）。

## 降级旧主实例

当旧主实例恢复时，你可以使用相同的 `repoint` 命令将其作为新主实例的副本重新加入：

```bash
# 在旧主实例主机上
pg replica repoint pg01 \
  --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@10.241.21.97:35439/pg01_db" \
  --primary-name ro1
```

即使 `pg01` 曾是主实例（未设置 `ReplicaOf`）这也有效。该命令：

1. 停止 pg01 并销毁其数据（包括旧的 PITR stanza）
2. 在新主实例上为 pg01 创建复制槽
3. 设置 `ReplicaOf = "ro1"`，`PITR.Enabled = false`
4. 通过 `pg_basebackup` 从新主实例重新初始化

重新指向后，pg01 作为只读副本从新主实例流式传输 WAL——没有 WAL 归档，没有备份。

## 扩展同步

当副本被重新指向新主实例时，pgcli 会自动同步扩展：

1. **查询** — 通过 DSN 连接到新主实例并查询 `pg_extension` 获取已安装的扩展
2. **过滤** — 识别非内置扩展（需要外部包的扩展，例如 pg_cron、pgmq、timescaledb）
3. **构建** — 如果存在非内置扩展，构建本地 `-ext` 镜像：
   - 如果本地 `-ext` 镜像已存在，在其上安装缺失的包（重用 Pigsty 仓库——快速）
   - 如果没有 `-ext` 镜像，从基础镜像构建并设置 Pigsty 仓库
   - `apt-get install` 是幂等的——安装已存在的包是无操作
4. **应用** — 副本启动时，`ApplyExtensions` 将 `shared_preload_libraries` 写入 `postgresql.conf`
5. **跳过 CREATE EXTENSION** — 副本是只读的；扩展通过 `pg_basebackup` + WAL 流式传输从主实例复制

这确保副本容器具有 `postgresql.auto.conf` 中引用的所需共享库（例如 `pg_cron`）。

## 同主机 vs 跨主机

pgcli **不**自动检测拓扑。你选择在每个命令运行的位置：

| 场景 | 步骤 1 | 步骤 2 | 步骤 3 |
|------|--------|--------|--------|
| 全部在同一主机 | `pg replica promote ro1` | `pg replica drop ro1 -i pg01` | `pg replica repoint ro2 --primary-dsn "postgres://...@127.0.0.1:..." --primary-name ro1` |
| 主实例 + 副本分散在多个主机 | 在副本主机 | 在旧主实例主机 | 在每个副本主机上使用新主实例的网络 IP |
| 混合 | 在各自的主机 | 在旧主实例主机 | 在每个副本主机 |

`--primary-dsn` 必须使用从 `repoint` 运行所在主机可达的 IP/主机名。

## 完整示例

```bash
# ── 初始设置：pg01（主实例）+ ro1, ro2（副本）在同一主机 ──

$ pg list
NAME    ROLE      PRIMARY   STATUS
pg01    primary   -         Up 2 hours
ro1     replica   pg01      Up 1 hour
ro2     replica   pg01      Up 1 hour

# ── pg01 崩溃 ──

# 步骤 1：提升 ro1
$ pg replica promote ro1
  [OK] pg_promote() signaled
  [OK] recovery ended, instance is now read-write
  [OK] primary_conninfo removed from postgresql.auto.conf
✓ Replica "ro1" promoted to primary

$ pg start -i ro1           # 启用 PITR + WAL 归档

# 步骤 2：在旧主实例上清理（如果 pg01 永久丢失则跳过）
$ pg replica drop ro1 -i pg01
  [OK] replication slot "pgcli_r_ro1" removed from primary "pg01"

# 步骤 3：将 ro2 重新指向新主实例
$ pg replica repoint ro2 \
    --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@127.0.0.1:35437/pg01_db" \
    --primary-name ro1
  [OK] extension image built with pg_cron, pgmq, timescaledb
  [OK] replication slot "pgcli_r_ro2" created on new primary
  [OK] config updated (ReplicaOf = "ro1")
✓ Replica "ro2" re-pointed to "ro1"

# ── pg01 恢复，降级为副本 ──
$ pg replica repoint pg01 \
    --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@127.0.0.1:35437/pg01_db" \
    --primary-name ro1
  [OK] backup stanza removed: pgcli_pg01
  [OK] replication slot "pgcli_r_pg01" created on new primary
  [OK] config updated (ReplicaOf = "ro1", PITR disabled)
✓ Replica "pg01" re-pointed to "ro1"

# ── 最终状态 ──
$ pg list
NAME    ROLE      PRIMARY   STATUS
pg01    replica   ro1       Up 30 seconds    # 已降级
ro1     primary   -         Up 10 minutes    # 新主实例
ro2     replica   ro1       Up 5 minutes
```

## 跨主机示例

```bash
# ── 设置：ra3（主实例，主机 A）+ ra2（副本，主机 A）+ ro2（副本，主机 B）──

# ra3 崩溃。在主机 A 上，提升 ra2：
$ pg replica promote ra2
  [OK] pg_promote() signaled
  [OK] recovery ended
  [OK] PITR initialized (stanza + archive_mode)
✓ Replica "ra2" promoted to primary

# 在主机 B（10.241.20.147）上，将 ro2 重新指向新主实例 ra2（主机 A = 10.241.21.97）：
$ pg replica repoint ro2 \
    --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@10.241.21.97:35438/pg01_db" \
    --primary-name ra2
-> New primary has 3 non-builtin extension(s): pg_cron, pgmq, timescaledb
-> Extension image already has all required packages
  [OK] replication slot "pgcli_r_ro2" created on new primary
  [OK] config updated (ReplicaOf = "ra2", image = ...-ext)
✓ Replica "ro2" re-pointed to "ra2"

# 验证跨主机复制：
$ pg exec -i ra2 "INSERT INTO test(msg) VALUES ('after failover')"
$ pg exec -i ro2 "SELECT * FROM test ORDER BY id DESC LIMIT 1"
   msg: after failover
```

## 级联复制

副本本身可以作为下游副本的主实例，形成级联链。这减少主实例的负载并启用分层拓扑。

```
primary (ra3)
  ├─→ replica (ra2) ← ra2_ro1 的上游
  │     └─→ replica (ra2_ro1)
  └─→ replica (pg01)
```

### 工作原理

1. **创建副本的副本**：使用副本作为 `-i` 目标
   ```bash
   # ra2 是 ra3 的副本，创建 ra2_ro1 作为 ra2 的副本
   pg replica create ra2_ro1 -i ra2
   ```

2. **WAL 传播**： 
   - ra2 从 ra3 流式传输 WAL
   - ra2_ro1 从 ra2 流式传输 WAL
   - 数据流：ra3 → ra2 → ra2_ro1

3. **复制槽**：每个链接维护自己的槽
   - ra3 有槽 `pgcli_r_ra2`
   - ra2 有槽 `pgcli_r_ra2_ro1`

### 优势

- **减少主实例负载**：只有直接副本连接到主实例
- **地理分布**：主实例 → 区域副本 → 本地副本
- **网络效率**：本地副本可以共享区域上游

### 限制

- **增加延迟**：每一跳增加复制延迟
- **级联故障**：如果 ra2 失败，ra2_ro1 失去其上游
- **提升复杂性**：提升 ra2_ro1 需要将其重新指向新主实例

### 验证级联

```bash
# 检查 ra2 的下游副本
pg exec -i ra2 "SELECT client_addr, state FROM pg_stat_replication"

# 检查 ra2_ro1 的上游
pg exec -i ra2_ro1 "SELECT conninfo FROM pg_stat_wal_receiver"
```

### 级联故障转移

如果 ra2（中间节点）失败：

```bash
# 选项 1：将 ra2_ro1 直接重新指向 ra3
pg replica repoint ra2_ro1 \
  --primary-dsn "postgres://admin:password@ra3-host:5432/pg01_db" \
  --primary-name ra3

# 选项 2：等待 ra2 恢复（一旦 ra2 重新连接到 ra3 自动进行）
```

如果 ra3（主实例）失败且 ra2 被提升：

```bash
# 步骤 1：提升 ra2
pg replica promote ra2

# 步骤 2：ra2_ro1 自动跟随（它已经从 ra2 复制）
# ra2_ro1 无需操作

# 步骤 3：将其他副本重新指向新主实例
pg replica repoint pg01 \
  --primary-dsn "postgres://admin:password@ra2-host:5432/pg01_db" \
  --primary-name ra2
```

## 注释

- **pg_promote()** — PostgreSQL 12+ 原生函数，无需容器重启。实例就地退出恢复并立即变为可读写
- **时间线分歧** — 提升后，新主实例在新时间线上。其他副本不能用 `ALTER SYSTEM SET primary_conninfo` 重新指向——它们必须通过 `pg_basebackup` 重建
- **被提升副本上的 PITR** — 提升后，运行 `pg start` 创建 pgBackRest stanza 并启用 WAL 归档。被提升的副本没有先前的备份历史
- **复制槽** — 旧主实例为被提升副本的槽在提升后变得陈旧。`pg replica drop` 清理它。如果旧主实例被降级为副本，`repoint` 销毁旧数据且陈旧的槽不再被引用
- **扩展** — 副本容器通过 `postgresql.auto.conf` 从主实例继承 `shared_preload_libraries`。repoint 命令确保本地镜像在重建副本之前具有所需的扩展包
- **跳过 CREATE EXTENSION** — 副本是只读的；`pg_basebackup` 从主实例复制扩展元数据，因此不需要 `CREATE EXTENSION`（且会失败 "cannot execute CREATE EXTENSION in a read-only transaction"）
