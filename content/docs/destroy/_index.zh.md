---
title: "销毁"
description: "销毁实例并移除其配置"
weight: 95
icon: fa-solid fa-trash
menus:
  main:
    identifier: docs-destroy
    parent: docs
    weight: 95
    params:
      icon: fa-solid fa-trash
---

destroy 会停止并移除容器，然后从配置文件中删除该实例。默认情况下，主机的数据目录会被保留。使用 `--clean-data` 可以同时移除数据、WAL 归档和 pgBackRest 仓库的 stanza。

## 基本用法

```bash
# 销毁实例（保留数据目录）
pg destroy -i proj01

# 无需确认直接销毁
pg destroy -i proj01 --force

# 销毁并清理数据（全新开始）
pg destroy -i proj01 --clean-data

# 跳过确认并清理所有数据
pg destroy -i proj01 --clean-data --force
```

## 执行过程

运行 `pg destroy` 时：

1. **停止容器** — PostgreSQL 优雅关闭
2. **移除容器** — 删除 Podman 容器
3. **移除配置** — 从 `~/.pgcli/pg.yaml` 中删除该实例条目
4. **保留数据**（默认）— 主机数据目录 `--base-dir/<instance>` 保留

使用 `--clean-data` 时：

1. 以上所有步骤，加上：
2. **移除主机数据** — 删除数据目录
3. **移除 WAL 归档** — 删除主机上的所有 WAL 文件
4. **移除备份 stanza** — 删除该实例的 pgBackRest 仓库 stanza

## 重建实例

销毁后，可以重新创建实例以获得全新开始：

```bash
# 销毁并清理所有数据
pg destroy -i proj01 --clean-data

# 使用相同名称重新创建
pg create -i proj01 --base-dir /data/pg

# 启动新实例
pg start -i proj01
```

**重要：** 不使用 `--clean-data` 时，旧的数据目录会被保留。当你重新创建实例时，PostgreSQL 会使用现有数据，而 `init.sh`（创建用户和设置管理员密码）**不会**再次运行。这可能导致以下问题：

- 数据是用不同的用户或密码创建的
- 你更改了配置中的默认用户
- 你想要完全全新开始

需要彻底清理时使用 `--clean-data`。

## 确认提示

默认情况下，`pg destroy` 会在执行前询问确认：

```bash
$ pg destroy -i proj01
!  This will destroy instance "proj01":
   - Container: pgcli-pg-default-proj01
   - Data dir: /data/pg/proj01 (preserved)

Continue? [y/N]:
```

使用 `--force` 跳过提示：

```bash
pg destroy -i proj01 --force
```

## 使用场景

### 1. 配置更改后的干净重启

如果你更改了配置中的默认用户、密码或 PostgreSQL 版本，销毁并重建：

```bash
# 更新配置
# 编辑 ~/.pgcli/pg.yaml 更改 postgres.user 或 image_tag

# 销毁并清理数据
pg destroy -i proj01 --clean-data --force

# 使用新设置重新创建
pg create -i proj01 --base-dir /data/pg
pg start -i proj01
```

### 2. 移除测试实例

测试完成后，移除不再需要的实例：

```bash
pg destroy -i test-instance --force
```

### 3. 修复损坏的状态

如果实例处于异常状态（例如启动失败、数据损坏），销毁并重建：

```bash
pg destroy -i broken-instance --clean-data --force
pg create -i broken-instance --base-dir /data/pg
pg start -i broken-instance
```

### 4. 释放资源

销毁不再活跃使用的实例以释放：

- Podman 容器（CPU 和内存）
- 主机磁盘空间（使用 `--clean-data`）
- 配置文件中的条目

## 与副本的关系

销毁**副本**时，主实例上的复制槽**不会**自动移除。你需要单独清理：

```bash
# 步骤 1：销毁副本
pg destroy -i ro1 --force

# 步骤 2：在主实例上删除复制槽
pg replica drop ro1 -i primary-instance
```

这种两步流程确保如果你计划稍后重新创建副本，不会意外丢失复制槽。

## 安全注意事项

- **数据丢失**：`--clean-data` 会永久删除该实例的所有数据、WAL 和备份。谨慎使用。
- **无法撤销**：销毁后，除非有外部备份，否则实例无法恢复。
- **配置丢失**：实例条目会从配置文件中移除。如果以后需要该配置，请先备份。

## 标志

| 标志 | 默认值 | 描述 |
|------|--------|------|
| `--force` | `false` | 跳过确认提示 |
| `--clean-data` | `false` | 同时移除主机数据、WAL 归档和备份 stanza |
| `-i`, `--instance` | `default` | 要销毁的实例名称 |

## 相关命令

- `pg create` — 创建新实例
- `pg start` — 启动实例
- `pg stop` — 停止实例（容器保留）
- `pg replica drop` — 从主实例移除复制槽
