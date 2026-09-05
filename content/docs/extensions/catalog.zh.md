---
title: "扩展目录"
description: "基础镜像中内置的 PostgreSQL 扩展列表"
weight: 10
---

本页列出基础镜像中所有内置扩展。使用示例请参见[常用扩展](./popular/)。

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
