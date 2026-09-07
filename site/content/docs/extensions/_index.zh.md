---
title: 扩展
description: pgcli PostgreSQL 扩展指南
weight: 20
icon: fa-solid fa-book
cascade:
  type: docs
  footer_style: slim
---


pgcli 支持从 [Pigsty DEB 仓库](https://pigsty.io/ext/)安装和管理 PostgreSQL 扩展。

## 工作原理

扩展被烘焙到派生容器镜像中：

1. **`pg extension install`** 构建新镜像（基于当前镜像 + Pigsty 仓库 + 扩展包）
2. 停止并移除旧容器
3. 从新镜像重新创建容器（主机数据卷被保留）
4. 更新配置文件中的 `image_tag`

这种方法的优势：
- 扩展在容器重建后仍然存在（烘焙到镜像层）
- `pg start` 不需要在每次启动时运行 `apt-get install`
- 扩展文件是持久的并与容器生命周期解耦

## 命令

### 安装扩展

```bash
# 安装单个扩展
pg extension install pg_stat_statements

# 安装多个扩展（单次镜像构建）
pg extension install pgmq uuid-ossp pg_stat_statements

# 针对特定实例
pg extension install pg_stat_statements -i pg01
```

**重启确认：** 需要 `shared_preload_libraries` 的扩展（例如 `pg_stat_statements`、`pg_cron`）需要 PostgreSQL 重启。默认情况下，会提示你确认：

```bash
# 交互式确认（默认）
pg extension install pg_stat_statements
# 输出：
# Installing extensions that require shared_preload_libraries will cause a PostgreSQL restart.
# Extensions to be installed: [pg_stat_statements]
# This will cause a brief interruption to database connections.
# Restart PostgreSQL now? [y/N]:

# 跳过确认并自动重启
pg extension install pg_stat_statements --auto-restart
```

如果你拒绝重启，可以稍后应用更改：
```bash
pg stop -i pg01
pg start -i pg01
```

### 列出已安装扩展

```bash
pg extension list -i pg01
```

示例输出：
```
Installed extensions in "pg01":
  pg_stat_statements (managed)
  uuid-ossp (managed)
  plpgsql (unmanaged)
```

- **managed**：由 pgcli 跟踪（记录在配置中，包含在镜像中）
- **unmanaged**：手动安装的扩展（不在配置中跟踪）

### 移除扩展

```bash
pg extension remove pgmq -i pg01
```

工作流：
1. `DROP EXTENSION IF EXISTS pgmq`
2. 更新配置和 `shared_preload_libraries`
3. **无镜像重建** — `-ext` 镜像在实例间共享，包永远不会被卸载

**重启确认：** 如果移除需要 `shared_preload_libraries` 的扩展（例如 `pg_stat_statements`、`pg_cron`），会在重启前提示你确认：

```bash
# 交互式确认（默认）
pg extension remove pg_stat_statements -i pg01
# 输出：
# Removing extensions that require shared_preload_libraries will cause a PostgreSQL restart.
# Extensions to be removed: [pg_stat_statements]
# This will cause a brief interruption to database connections.
# Restart PostgreSQL now? [y/N]:

# 跳过确认并自动重启
pg extension remove pg_stat_statements -i pg01 --auto-restart
```

如果你拒绝重启，可以稍后应用更改：
```bash
pg stop -i pg01
pg start -i pg01
```

### 查看可用扩展

```bash
pg extension available
```

列出所有 440 个已知扩展：
- **45 个内置**（contrib，已在基础镜像中——无需镜像构建）
- **395 个 Pigsty 目录**（来自 Pigsty DEB 仓库，需要镜像构建）

## 扩展目录

参见[扩展目录](./catalog/)获取 45 个内置扩展完整列表（无需安装）。

参见[常用扩展](./popular/)获取常用扩展详细使用示例。

## 目录外的扩展

只有目录中的扩展（builtin + Pigsty）可以通过 `pg extension install` 安装。未知扩展名称会在构建开始前被拒绝：

```
  [X] Unknown extension(s): [nonexistent_ext]

      These extensions are not in the Pigsty catalog or builtin contrib list.
      Check available extensions: pg extension available
      Full Pigsty catalog: https://pigsty.cc/ext/list/
```

完整目录：https://pigsty.cc/ext/list/

## 配置

安装扩展后，配置会被更新：

```yaml
instances:
  pg01:
    extensions:
      - pg_stat_statements
      - uuid-ossp
      - pgmq
    podman:
      image_tag: ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0-ext
```

`image_tag` 指向包含所有已安装扩展的派生镜像。

## 共享预加载库

需要 `shared_preload_libraries` 的扩展会在 `postgresql.conf` 中自动配置：

```
# === pgcli extensions (managed — do not edit) ===
shared_preload_libraries = 'pg_stat_statements,pg_cron'
# === end pgcli extensions ===
```

这是 postmaster 级别参数；更改后必须重启 PostgreSQL。

## 故障排除

### 扩展安装失败

```
  [X] Unknown extension(s): [nonexistent_ext]
```

原因：扩展名称不在内置 contrib 列表或 Pigsty 目录中。

解决方法：
- 验证扩展名称：`pg extension available`
- 检查 Pigsty 目录：https://pigsty.cc/ext/list/
- 注意确切的 SQL 扩展名称（例如 `vector` 而不是 `pgvector`）

### CREATE EXTENSION 失败

```
ERROR: extension "pgmq" already exists
```

扩展已安装但未在配置中跟踪。你可以安全地忽略这个，或手动将其添加到配置：

```yaml
extensions:
  - pgmq
```

### 共享预加载库冲突

如果 `shared_preload_libraries` 在 `postgresql.conf` 中被手动编辑，pgcli 的标记块会覆盖它。

解决方法：删除手动配置并让 pgcli 管理它。

## 注释

- **扩展数量**：45 个内置（contrib）+ 395 个 Pigsty 目录 = 440 个已知扩展
- **镜像大小**：每个扩展增加 10-50MB 到镜像，但 Pigsty 包已优化
- **构建时间**：首次扩展安装需要 1-3 分钟（下载 + 构建）；后续安装更快（缓存命中）
- **副本行为**：副本可以安装扩展，但 `CREATE EXTENSION` 会被拒绝（只读）。在主实例上安装；副本通过物理复制同步
- **扩展升级**：`ALTER EXTENSION ... UPDATE TO ...` 尚不支持；通过 `pg exec` 手动运行
