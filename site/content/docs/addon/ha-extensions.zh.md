---
title: "HA 集群扩展安装"
description: "在 Patroni 管理的 HA 集群中安装和管理 PostgreSQL 扩展"
weight: 48
---

在 Patroni 管理的 HA 集群中安装 PostgreSQL 扩展，与单节点实例有本质区别。
本页解释原因，并介绍 `pg ha extension` 命令子树。

## 为什么不能用 `pg extension install`？

单节点流程（`pg extension install <instance> <ext>`）的工作方式是：

1. 用 Pigsty 包构建 `-ext` 派生镜像
2. 停止并用新镜像重建容器
3. 编辑 `postgresql.conf` 设置 `shared_preload_libraries`
4. 在容器内执行 `CREATE EXTENSION`

在 Patroni 集群中，**步骤 2-4 全部失效**：

- **Patroni 是 PID 1。** 重建容器 = 该节点离线。如果是 leader，Patroni 会触发
  故障切换 —— 集群在你安装到一半时重新洗牌。
- **Patroni 每个循环都从 DCS 重新生成 `postgresql.conf`。** 任何对文件的直接
  编辑都会在几秒内被覆盖。`shared_preload_libraries` 必须通过
  `patronictl edit-config`（写入 DCS）来设置。
- **`CREATE EXTENSION` 必须在 leader 上运行**，而 leader 可能在远程主机 ——
  不在任何本地容器内。

`pg ha extension` 正确编排了所有这些步骤。

## 工作原理

安装流程遵循特定顺序，避免上述陷阱：

```
1. 验证扩展名（IsExtensionKnown）
2. 用 Pigsty 包构建 -ext 镜像
3. patronictl pause <scope> --wait            ← 禁用自动故障切换
4. 逐个重建成员容器（每次一个，等待重新加入）
5. patronictl resume <scope> --wait           ← ⚠ 必须在 edit-config 之前
6. patronictl edit-config（设置 shared_preload_libraries）
   → Patroni 触发滚动重启（replica 先，leader 最后）
7. 等待滚动重启完成
8. 在 leader 上执行 CREATE EXTENSION
9. 将扩展列表保存到 pg.yaml
```

关键顺序是 **resume 必须在 edit-config 之前**：暂停状态的 Patroni 集群不会
应用 `edit-config` 变更。如果在暂停状态下 edit-config，`shared_preload_libraries`
的更新会被静默丢失。

### 纯内置扩展快速路径

如果所有请求的扩展都是内置的（contrib 扩展，如 `hstore`、`uuid-ossp`，随
PostgreSQL 一起发布），步骤 2-4 会完全跳过 —— 不构建镜像、不重建容器、不
暂停/恢复。流程直接走 `edit-config` + `CREATE EXTENSION`。

## 命令

### 安装

```bash
pg ha extension install <scope> <extension>[,<extension>...] [flags]
```

构建 `-ext` 镜像，暂停集群，重建本地成员容器。

- **单主机集群**（所有成员在本地）：自动执行 apply（resume + edit-config + CREATE EXTENSION）
- **跨主机集群**：只执行 pause + recreate，集群保持暂停。需要在每台主机上分别运行
  install，最后在任意主机上运行 `pg ha extension apply` 完成安装。

扩展以逗号分隔传入：

```bash
# 安装单个扩展
pg ha extension install app pg_stat_statements

# 安装多个扩展到指定数据库
pg ha extension install app pg_cron,pg_stat_statements --database mydb

# 跳过滚动重启确认提示
pg ha extension install app pgvector --auto-restart
```

**参数：**

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--database` | `postgres` | `CREATE EXTENSION` 的目标数据库 |
| `--auto-restart` | `false` | 跳过 `edit-config` 触发的滚动重启确认 |

### 卸载

```bash
pg ha extension remove <scope> <extension>[,<extension>...] [flags]
```

在 leader 上执行 `DROP EXTENSION`，然后通过 `patronictl edit-config` 更新
`shared_preload_libraries`（触发滚动重启）。**不会**重建镜像或重建容器 ——
`-ext` 镜像只增不减；磁盘回收很少见且需手动操作。

扩展以逗号分隔传入：

```bash
pg ha extension remove app pg_cron
pg ha extension remove app pg_stat_statements,pg_cron --auto-restart
```

### 列表

```bash
pg ha extension list <scope>
```

显示集群扩展的三个视图：

- **Config (pg.yaml)：** 存储在 `pg.yaml` 中的 `extensions` 列表
- **DCS (preload)：** 来自 `patronictl show-config` 的 `shared_preload_libraries` 值
- **Leader (installed)：** leader 上 `pg_extension` 中的实际扩展

```bash
$ pg ha extension list app
Cluster "app" extensions:

  Config (pg.yaml):  [pg_stat_statements pg_cron]
  DCS (preload):     shared_preload_libraries: pg_stat_statements,pg_cron
  Leader (installed): [pg_cron pg_stat_statements]
```

### 应用

```bash
pg ha extension apply <scope> [flags]
```

手动触发安装流程的后半段：恢复集群、执行 `patronictl edit-config`、在
leader 上执行 `CREATE EXTENSION`。

用于**跨主机**集群 —— 在每台主机上运行 `install`（各自构建镜像并重建本地
成员）后，在任意主机上运行一次 `apply` 完成 DCS 更新和扩展创建。

```bash
pg ha extension apply app
pg ha extension apply app --database mydb --auto-restart
```

## 跨主机工作流

在跨主机集群中，每台主机只管理自己的成员。扩展安装流程分为两个阶段，
**全局只暂停/恢复一次**，避免不必要的 leader 漂移：

**第一阶段 —— 每台主机依次运行 `install`：**

每台主机执行：
- 在本地构建 `-ext` 镜像（合并 DCS 已有的扩展列表，确保包含所有包）
- 暂停集群（幂等 —— 第二台主机不会因"已暂停"而报错）
- 用新镜像重建自己的本地成员（**同主机内 replica 先、leader 后**）
- 保存配置 —— **集群保持暂停状态**，不 resume

> **推荐顺序：从 replica 所在主机开始。** 如果 leader 所在主机先执行 install，
> leader 重建会触发故障切换（leader 漂移到其他主机）。从 replica 主机开始，
> leader 始终不动，直到最后才处理 leader 所在主机，最大限度减少 leader 漂移
> 带来的数据同步开销。

```bash
# 主机 A（只有 replica，先执行）—— 集群变为暂停状态
pg ha extension install app pg_stat_statements,pg_cron

# 主机 B（有 leader，后执行）—— 集群保持暂停
pg ha extension install app pg_stat_statements,pg_cron
```

**第二阶段 —— 一次，在任意主机运行 `apply`：**

- 恢复集群（幂等 —— tolerate "not paused"）
- 通过 `patronictl edit-config` 更新 `shared_preload_libraries`
- 等待滚动重启
- 在 leader 上执行 `CREATE EXTENSION`

```bash
pg ha extension apply app --auto-restart
```

> **注意：** `install` 后集群处于暂停状态。如果忘记执行 `apply`，集群会一直暂停
> （自动故障切换被禁用）。此时运行 `pg ha extension apply` 或手动
> `patronictl resume <scope>` 即可恢复。

对于单主机集群（所有成员都在本地），`install` 自动执行 `apply` 步骤 ——
不需要单独的命令。

> **关于 leader 漂移：** 目前无法完全避免 leader 漂移。当 leader 所在主机的
> 容器被重建时，Patroni 进程停止，DCS 中的 leader key 在 TTL 过期后失效，
> 即使集群已暂停（pause），其他 replica 仍会检测到 key 失效并触发新一轮选举。
> `replicas-first` 重建顺序能确保 leader 所在主机是最后一个执行 install 的，
> 但无法阻止 leader 漂移本身。这是 Patroni + 容器重建的固有限制。

## `extensions` 配置字段

扩展在 `pg.yaml` 的 Patroni 集群配置中以集群级别跟踪：

```yaml
addons:
  patroni:
    app:
      name: app
      extensions:
        - pg_stat_statements
        - pg_cron
      passwords:
        superuser: ...
      members:
        node1: { ... }
        node2: { ... }
```

这个列表是 `pg ha extension list` 报告的内容，也是 `apply` 安装的依据。
`install` 和 `remove` 都会自动更新它。

## `shared_preload_libraries` 排序

某些扩展必须出现在 `shared_preload_libraries` 的第 0 位（如果不在第一位，
PostgreSQL 会 FATAL）。pgcli 在扩展目录中以 `PreloadFirst` 属性跟踪此约束 ——
当前设置此标记的扩展：

- **`citus`** —— 分布式 PostgreSQL，必须最先加载
- **`timescaledb`** —— 时序引擎，同样的约束

部分扩展还需要额外的 DCS 参数：

- **`pg_cron`** 需要 `cron.database_name`

`pg ha extension` 自动处理所有情况：

- 带 `PreloadFirst` 的扩展始终放在 CSV 开头，不管输入顺序如何
- 当存在 `pg_cron` 时，`cron.database_name` 设为 `--database` 的值
  （默认 `postgres`）
- 当 `pg_cron` 被移除时，`cron.database_name` 被清除

## 示例

### 安装 pg_stat_statements 和 pg_cron

```bash
pg ha extension install app pg_stat_statements,pg_cron --database mydb --auto-restart
```

构建 `-ext` 镜像，重建所有成员（带 pause/resume），在 DCS 中设置
`shared_preload_libraries=pg_stat_statements,pg_cron` 和
`cron.database_name=mydb`，然后在 `mydb` 中创建两个扩展。

### 安装 Citus（必须排在 preload 第一位）

```bash
pg ha extension install app citus pg_stat_statements
# 或等价写法：
pg ha extension install app citus,pg_stat_statements
```

即使 `citus` 列在第二位，preload CSV 也会生成为
`citus,pg_stat_statements` —— 带 `PreloadFirst` 的扩展始终放在位置 0。

### 卸载扩展

```bash
pg ha extension remove app pg_cron
```

从 leader 删除 `pg_cron`，从 `shared_preload_libraries` 中移除，并清除
`cron.database_name`。触发滚动重启。

### 查看已安装的扩展

```bash
pg ha extension list app
```

## 限制

- **镜像重建是增量的。** 卸载扩展不会重建 `-ext` 镜像或缩小它。要回收磁盘，
  需用 `podman image prune` 手动清理旧镜像。
- **没有按成员的扩展列表。** 扩展是集群级别的 —— 所有成员共享相同的 `-ext`
  镜像和相同的 `shared_preload_libraries`。
- **`CREATE EXTENSION` 只针对一个数据库。** PostgreSQL 扩展是按数据库的。要在
  多个数据库中安装，需用 `--database` 分别指定每个数据库重新运行。
- **跨主机需要手动协调。** 每台主机必须先运行 `install`，然后才能运行一次
  `apply`。pgcli 不会 SSH 到远程主机。

## 参考

- [Patroni HA](./ha/) —— 集群搭建、命令和架构
- [扩展管理](/docs/extensions/) —— 单节点扩展管理和 Pigsty 目录
- [Patroni 动态配置](./ha-dynamic/) —— DCS 参数参考
