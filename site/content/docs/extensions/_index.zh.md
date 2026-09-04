---
title: PostgreSQL 扩展
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

## 内置扩展目录

### 需要 shared_preload_libraries（安装时重启）

| 扩展 | 描述 |
|------|------|
| `pg_stat_statements` | SQL 性能分析 |
| `pg_cron` | 定时任务执行 |
| `pg_hint_plan` | 查询提示 |
| `pg_stat_monitor` | 高级性能监控 |
| `pg_qualstats` | 查询谓词统计 |
| `pg_stat_kcache` | 内核级性能统计 |
| `pg_wait_sampling` | 等待事件采样 |
| `pg_track_settings` | 配置更改跟踪 |
| `timescaledb` | 时序数据库扩展 |

### 不需要 shared_preload_libraries（无需重启）

| 扩展 | 描述 |
|------|------|
| `uuid-ossp` | UUID 生成函数 |
| `pgmq` | 轻量级消息队列 |
| `hstore` | 键值对存储 |
| `pgcrypto` | 加密函数 |
| `tablefunc` | 交叉表函数 |
| `btree_gist` | B-tree GiST 索引支持 |
| `btree_gin` | B-tree GIN 索引支持 |
| `pg_trgm` | 三元组相似性匹配 |
| `unaccent` | 去重音函数 |
| `fuzzystrmatch` | 模糊字符串匹配 |
| `intarray` | 整数数组操作 |
| `isn` | ISBN/ISSN/EAN 标准数字类型 |
| `pg_repack` | 在线表重组 |
| `pg_squeeze` | 表空间回收 |
| `pg_partman` | 分区管理 |
| `pgvector` | 向量相似性搜索 |
| `postgis` | 地理空间数据支持 |

## 常用扩展

以下 20 个 PostgreSQL 最常用的扩展已在 **PG 18** + pgcli 环境下验证通过。

### 性能与监控

| 扩展 | 安装命令 | 说明 |
|------|---------|------|
| `pg_stat_statements` | `pg extension install pg_stat_statements` | SQL 性能分析，跟踪查询执行统计。**生产环境必备。** |
| `pg_repack` | `pg extension install pg_repack` | 在线表重组，无需排他锁。回收频繁更新表的膨胀空间。 |
| `pg_prewarm` | `pg extension install pg_prewarm` | 重启后将表数据预加载到共享缓冲区。 |

```bash
# 一次性安装性能扩展
pg extension install pg_stat_statements pg_repack pg_prewarm --auto-restart

# 查找 top 5 慢查询
pg exec "SELECT query, calls, total_exec_time::numeric(10,2) AS ms FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 5;"

# 重组膨胀表
pg exec -- pg_repack -U admin -d default_db -t my_table --no-superuser-check
```

### 数据类型与标识符

| 扩展 | 安装命令 | 说明 |
|------|---------|------|
| `uuid-ossp` | builtin | UUID 生成（v1/v3/v4/v5）。PG 18 已内置 `gen_random_uuid()` 和 `uuidv7()`。 |
| `hstore` | builtin | 单列键值对存储。 |
| `citext` | builtin | 不区分大小写的文本类型。`Foo` = `foo`。 |

```bash
# 生成 UUID
pg exec "SELECT uuid_generate_v4();"          # 随机 UUID
pg exec "SELECT uuidv7();"                    # 时间有序 UUID（PG 18 内置）

# 键值存储
pg exec "SELECT 'theme => dark, lang => en'::hstore -> 'theme';"  -- 返回 'dark'

# 不区分大小写匹配
pg exec "CREATE TABLE users (email citext); INSERT INTO users VALUES ('Alice@Example.COM'); SELECT * FROM users WHERE email = 'alice@example.com';"
```

### 搜索与文本

| 扩展 | 安装命令 | 说明 |
|------|---------|------|
| `pg_trgm` | builtin | 三元组相似性匹配，用于模糊搜索和自动补全。 |
| `unaccent` | builtin | 去除字符重音，实现灵活的国际化文本搜索。 |

```bash
# 模糊搜索：查找与 "John" 相似的名称
pg exec "SELECT name, similarity(name, 'John') FROM users ORDER BY similarity(name, 'John') DESC LIMIT 5;"

# 去除重音
pg exec "SELECT unaccent('Crème Brûlée');"  -- 返回 'Creme Brulee'
```

### 安全与加密

| 扩展 | 安装命令 | 说明 |
|------|---------|------|
| `pgcrypto` | builtin | 哈希（bcrypt/sha256）、加密/解密、随机值生成。 |
| `pgaudit` | `pg extension install pgaudit` | 会话和对象审计日志。合规（GDPR、HIPAA、SOX）必需。 |

```bash
# bcrypt 密码哈希
pg exec "SELECT crypt('my_password', gen_salt('bf'));"

# 加密和解密
pg exec "SELECT pgp_sym_decrypt(pgp_sym_encrypt('secret', 'key'), 'key');"  -- 返回 'secret'

# 启用审计日志
pg exec "SET pgaudit.log = 'read, ddl';"
```

### 地理空间

| 扩展 | 安装命令 | 说明 |
|------|---------|------|
| `postgis` | `pg extension install postgis` | 全功能空间数据库：几何类型、空间索引、距离/面积计算。 |

```bash
pg extension install postgis --auto-restart

# 查找 5km 范围内的地点
pg exec "SELECT name FROM places WHERE ST_DWithin(geom, ST_MakePoint(-122.4, 37.8)::geography, 5000);"

# 计算两点间距离（米）
pg exec "SELECT ST_Distance(a::geography, b::geography) FROM (SELECT ST_MakePoint(-74.006, 40.7128) AS a, ST_MakePoint(-0.1276, 51.5074) AS b) t;"
```

### AI 与向量搜索

| 扩展 | 安装命令 | 说明 |
|------|---------|------|
| `pgvector` | `pg extension install vector` | 存储和搜索嵌入向量。AI 应用的事实标准。 |

```bash
pg extension install vector --auto-restart

# 创建带向量列的表
pg exec "CREATE TABLE items (id serial PRIMARY KEY, embedding vector(3));"
pg exec "INSERT INTO items (embedding) VALUES ('[1,2,3]'), ('[4,5,6]'), ('[7,8,9]');"

# 查找最近邻
pg exec "SELECT id FROM items ORDER BY embedding <-> '[3,1,2]' LIMIT 5;"
```

### 时序数据

| 扩展 | 安装命令 | 说明 |
|------|---------|------|
| `timescaledb` | `pg extension install timescaledb` | 优化的时序存储，支持超级表和时间桶聚合。 |

```bash
pg extension install timescaledb --auto-restart

# 创建超级表
pg exec "CREATE TABLE sensor (time timestamptz NOT NULL, value float); SELECT create_hypertable('sensor', 'time');"

# 时间桶聚合
pg exec "SELECT time_bucket('1 hour', time) AS bucket, avg(value) FROM sensor GROUP BY bucket ORDER BY bucket;"
```

### 定时任务与分布式

| 扩展 | 安装命令 | 说明 |
|------|---------|------|
| `pg_cron` | `pg extension install pg_cron` | 数据库内 Cron 定时任务调度器。pgcli 自动配置。 |
| `citus` | `pg extension install citus` | 分布式 PostgreSQL，支持分片和并行查询。 |

```bash
# 调度每日清理任务（午夜运行）
pg exec "SELECT cron.schedule('cleanup', '0 0 * * *', 'DELETE FROM logs WHERE created_at < now() - interval ''30 days''');"

# 列出已调度任务
pg exec "SELECT jobid, schedule, command FROM cron.job;"

# 取消调度
pg exec "SELECT cron.unschedule('cleanup');"

# 创建分布式表（citus）
pg exec "SELECT create_distributed_table('events', 'tenant_id');"
```

### 外部数据

| 扩展 | 安装命令 | 说明 |
|------|---------|------|
| `postgres_fdw` | builtin | 将外部 PostgreSQL 服务器作为本地表查询。 |

```bash
# 创建外部服务器并查询远程数据
pg exec "CREATE SERVER remote FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host '10.0.0.2', port '5432', dbname 'analytics');"
pg exec "CREATE USER MAPPING FOR current_user SERVER remote OPTIONS (user 'reader', password 'secret');"
pg exec "IMPORT FOREIGN SCHEMA public FROM SERVER remote INTO remote_schema;"
```

### 兼容性说明

| 扩展 | PG 18 状态 | 备注 |
|------|-----------|------|
| `pgml` | 不可用 | 仅支持 PG 14-17（无 PG 18 包） |
| `uuid-ossp` | 部分需要 | PG 18 已内置 `gen_random_uuid()`（v4）和 `uuidv7()`（时间有序） |

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
