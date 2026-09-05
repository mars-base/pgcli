---
title: "扩展目录与使用指南"
description: "内置和常用 PostgreSQL 扩展参考及使用示例"
weight: 10
---

本页列出基础镜像中所有内置扩展，并提供最常用扩展的使用示例。

## 内置扩展（45 个）

这些扩展已包含在 PostgreSQL 基础镜像中，无需镜像构建或 `pg extension install`。直接运行 `CREATE EXTENSION` 即可启用。

### 需要 shared_preload_libraries

这些扩展启用后需要重启 PostgreSQL。通过 `pg extension install` 安装时，pgcli 会自动处理重启。

| 扩展 | 描述 |
|------|------|
| `pg_stat_statements` | SQL 性能分析——跟踪查询执行次数、时间和资源使用 |
| `pg_cron` | 数据库内置 Cron 定时任务调度器 |
| `pg_hint_plan` | 查询提示，影响查询计划器 |
| `pg_stat_monitor` | 高级性能监控，包含查询计划 |
| `pg_qualstats` | 查询谓词统计，用于索引推荐 |
| `pg_stat_kcache` | 内核级 CPU/IO 性能统计 |
| `pg_wait_sampling` | 等待事件采样，用于性能分析 |
| `pg_track_settings` | 跟踪配置变更历史 |
| `timescaledb` | 时序数据库扩展，支持超级表 |
| `pg_prewarm` | 重启后将表数据预加载到共享缓冲区 |

### 无需重启

这些扩展可以通过 `CREATE EXTENSION` 立即启用，无需重启。

| 扩展 | 描述 |
|------|------|
| `uuid-ossp` | UUID 生成函数（v1/v3/v4/v5） |
| `hstore` | 单列键值对存储 |
| `pgcrypto` | 哈希（bcrypt/sha256）、加密/解密、随机数生成 |
| `pg_trgm` | 三元组相似性匹配，用于模糊搜索 |
| `unaccent` | 去除字符重音，用于国际化文本搜索 |
| `fuzzystrmatch` | 模糊字符串匹配（Levenshtein、Soundex、Metaphone） |
| `intarray` | 整数数组操作（去重、排序、交集） |
| `isn` | ISBN/ISSN/EAN 标准数字类型及验证 |
| `pg_repack` | 在线表重组，无需排他锁 |
| `pg_squeeze` | 表空间回收 |
| `pg_partman` | 自动分区管理 |
| `pgvector` (Pigsty) | 向量相似性搜索，用于 AI 应用 |
| `postgis` (Pigsty) | 地理空间数据支持 |
| `pgmq` (Pigsty) | 轻量级消息队列 |
| `tablefunc` | 交叉表/透视表函数 |
| `btree_gist` | GiST 索引的 B-tree 支持 |
| `btree_gin` | GIN 索引的 B-tree 支持 |
| `citext` | 不区分大小写的文本类型 |
| `cube` | 多维立方体数据类型 |
| `ltree` | 层次化标签树数据类型 |
| `seg` | 浮点区间数据类型 |
| `earthdistance` | 地球表面大圆距离计算 |
| `postgres_fdw` | 将外部 PostgreSQL 服务器作为本地表查询 |
| `file_fdw` | 将服务器文件作为外部表读取 |
| `dblink` | 连接其他 PostgreSQL 数据库 |
| `amcheck` | 验证 B-tree 索引完整性 |
| `pageinspect` | 底层页面检查，用于调试 |
| `pg_buffercache` | 查看共享缓冲区内容 |
| `pg_freespacemap` | 查看表的空闲空间映射 |
| `pg_visibility` | 查看表的可见性映射 |
| `pg_walinspect` | 检查 WAL 记录 |
| `pgstattuple` | 表级元组统计 |
| `pgrowlocks` | 显示行级锁信息 |
| `pg_surgery` | 底层堆元组操作 |
| `xml2` | XML 解析和 XPath 函数 |
| `dict_int` | 整数文本搜索字典 |
| `dict_xsyn` | 扩展同义词文本搜索字典 |
| `bloom` | 布隆过滤器索引访问方法 |
| `autoinc` | 自增触发器函数 |
| `insert_username` | 插入用户名触发器函数 |
| `moddatetime` | 自动更新时间戳触发器 |
| `refint` | 引用完整性触发器函数 |
| `tcn` | 触发器变更通知 |
| `sslinfo` | SSL 连接信息函数 |
| `lo` | 大对象类型支持 |
| `intagg` | 整数数组聚合/枚举 |
| `tsm_system_rows` | 按行数进行表采样 |
| `tsm_system_time` | 按时间进行表采样 |
| `pg_logicalinspect` | 逻辑复制槽检查 |

## 常用扩展

以下扩展是 PostgreSQL 中最常用的，已在 **PG 18** + pgcli 环境下测试通过。

### 性能与监控

| 扩展 | 安装 | 描述 |
|------|------|------|
| `pg_stat_statements` | `pg extension install pg_stat_statements` | SQL 性能分析。**生产环境必备。** |
| `pg_repack` | `pg extension install pg_repack` | 在线表重组，回收膨胀空间。 |
| `pg_prewarm` | `pg extension install pg_prewarm` | 重启后预加载表数据。 |

```bash
# 一次性安装所有扩展
pg extension install pg_stat_statements pg_repack pg_prewarm --auto-restart

# 查找 top 5 慢查询
pg exec "SELECT query, calls, total_exec_time::numeric(10,2) AS ms
         FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 5;"

# 重组膨胀表
pg exec -- pg_repack -U admin -d default_db -t my_table --no-superuser-check

# 预热表
pg exec "SELECT pg_prewarm('my_table');"
```

### 数据类型与标识符

| 扩展 | 安装 | 描述 |
|------|------|------|
| `uuid-ossp` | 内置 | UUID 生成（v1/v3/v4/v5）。PG 18 已内置 `gen_random_uuid()` 和 `uuidv7()`。 |
| `hstore` | 内置 | 键值对存储。 |
| `citext` | 内置 | 不区分大小写的文本。`Foo` = `foo`。 |

```bash
# UUID 生成
pg exec "SELECT uuid_generate_v4();"        # 随机 UUID
pg exec "SELECT uuidv7();"                  # 时间有序 UUID（PG 18 内置）

# 键值存储
pg exec "SELECT 'theme => dark, lang => en'::hstore -> 'theme';"
# 返回: dark

# 不区分大小写匹配
pg exec "CREATE TABLE users (email citext);
         INSERT INTO users VALUES ('Alice@Example.COM');
         SELECT * FROM users WHERE email = 'alice@example.com';"
```

### 搜索与文本

| 扩展 | 安装 | 描述 |
|------|------|------|
| `pg_trgm` | 内置 | 三元组相似性，用于模糊搜索和自动补全。 |
| `unaccent` | 内置 | 去除重音，用于灵活的国际化文本搜索。 |

```bash
# 模糊搜索
pg exec "SELECT name, similarity(name, 'John') FROM users
         ORDER BY similarity(name, 'John') DESC LIMIT 5;"

# 创建三元组索引加速模糊搜索
pg exec "CREATE INDEX users_name_trgm_idx ON users USING gin (name gin_trgm_ops);"

# 去除重音
pg exec "SELECT unaccent('Crème Brûlée');"  -- 返回 'Creme Brulee'

# 组合使用实现不区分重音的模糊搜索
pg exec "SELECT name FROM users WHERE name % unaccent('Creme Brulee');"
```

### 安全与加密

| 扩展 | 安装 | 描述 |
|------|------|------|
| `pgcrypto` | 内置 | 哈希、加密/解密、随机数生成。 |
| `pgaudit` | `pg extension install pgaudit` | 审计日志。合规（GDPR、HIPAA、SOX）必需。 |

```bash
# 使用 bcrypt 哈希密码
pg exec "SELECT crypt('my_password', gen_salt('bf'));"

# 验证密码
pg exec "SELECT (crypt('my_password', stored_hash) = stored_hash);"

# 对称加密/解密
pg exec "SELECT pgp_sym_decrypt(pgp_sym_encrypt('secret', 'key'), 'key');"

# 启用特定操作的审计日志
pg exec "SET pgaudit.log = 'read, ddl';"
```

### 地理空间

| 扩展 | 安装 | 描述 |
|------|------|------|
| `postgis` | `pg extension install postgis` | 完整空间数据库：几何类型、空间索引、距离/面积计算。 |

```bash
pg extension install postgis --auto-restart

# 创建包含空间数据的表
pg exec "CREATE TABLE places (id serial, name text, geom geometry(Point, 4326));"
pg exec "INSERT INTO places (name, geom) VALUES
         ('San Francisco', ST_MakePoint(-122.4, 37.8)),
         ('London', ST_MakePoint(-0.1276, 51.5074));"

# 查找 5km 范围内的地点
pg exec "SELECT name FROM places
         WHERE ST_DWithin(geom, ST_MakePoint(-122.4, 37.8)::geography, 5000);"

# 计算两个城市间的距离（米）
pg exec "SELECT ST_Distance(
           ST_MakePoint(-74.006, 40.7128)::geography,
           ST_MakePoint(-0.1276, 51.5074)::geography);"
```

### AI 与向量搜索

| 扩展 | 安装 | 描述 |
|------|------|------|
| `pgvector` | `pg extension install vector` | 存储和搜索嵌入向量。AI 应用标准。 |

```bash
pg extension install vector --auto-restart

# 创建带向量列的表
pg exec "CREATE TABLE items (id serial PRIMARY KEY, embedding vector(3));"
pg exec "INSERT INTO items (embedding) VALUES ('[1,2,3]'), ('[4,5,6]'), ('[7,8,9]');"

# 查找最近邻（欧几里得距离）
pg exec "SELECT id FROM items ORDER BY embedding <-> '[3,1,2]' LIMIT 5;"

# 创建 IVFFlat 索引加速近似搜索
pg exec "CREATE INDEX items_embedding_idx ON items USING ivfflat (embedding vector_l2_ops);"
```

### 时序数据

| 扩展 | 安装 | 描述 |
|------|------|------|
| `timescaledb` | `pg extension install timescaledb` | 优化的时序存储，支持超级表。 |

```bash
pg extension install timescaledb --auto-restart

# 创建超级表
pg exec "CREATE TABLE sensor (time timestamptz NOT NULL, value float);
         SELECT create_hypertable('sensor', 'time');"

# 时间桶聚合
pg exec "SELECT time_bucket('1 hour', time) AS bucket, avg(value)
         FROM sensor GROUP BY bucket ORDER BY bucket;"
```

### 定时任务与分布式

| 扩展 | 安装 | 描述 |
|------|------|------|
| `pg_cron` | `pg extension install pg_cron` | Cron 定时任务调度器。pgcli 自动配置。 |
| `citus` | `pg extension install citus` | 分布式 PostgreSQL，支持分片。 |

```bash
pg extension install pg_cron --auto-restart

# 调度每日清理任务（午夜运行）
pg exec "SELECT cron.schedule('cleanup', '0 0 * * *',
         'DELETE FROM logs WHERE created_at < now() - interval ''30 days''');"

# 列出已调度的任务
pg exec "SELECT jobid, schedule, command FROM cron.job;"

# 取消调度
pg exec "SELECT cron.unschedule('cleanup');"
```

### 外部数据

| 扩展 | 安装 | 描述 |
|------|------|------|
| `postgres_fdw` | 内置 | 将外部 PostgreSQL 服务器作为本地表查询。 |

```bash
# 创建外部服务器
pg exec "CREATE SERVER remote FOREIGN DATA WRAPPER postgres_fdw
         OPTIONS (host '10.0.0.2', port '5432', dbname 'analytics');"

# 创建用户映射
pg exec "CREATE USER MAPPING FOR current_user SERVER remote
         OPTIONS (user 'reader', password 'secret');"

# 导入远程 schema 作为外部表
pg exec "IMPORT FOREIGN SCHEMA public FROM SERVER remote INTO remote_schema;"

# 像查询本地表一样查询远程数据
pg exec "SELECT * FROM remote_schema.events LIMIT 10;"
```

### 兼容性说明

| 扩展 | PG 18 状态 | 备注 |
|------|-----------|------|
| `pgml` | 不可用 | 仅支持 PG 14-17（无 PG 18 包） |
| `uuid-ossp` | 部分需要 | PG 18 已内置 `gen_random_uuid()` (v4) 和 `uuidv7()` (时间有序) |
