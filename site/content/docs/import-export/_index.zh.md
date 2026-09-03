---
title: 数据导入/导出
description: pgcli 数据导入/导出指南
weight: 20
icon: fa-solid fa-book
cascade:
  type: docs
  footer_style: slim
---


导出和导入数据库到转储文件。支持自定义格式（推荐）和纯 SQL，带有自动 gzip 压缩。还支持在实例间管道传输。

```bash
# 导出为自定义格式（推荐，最快恢复）
pg export -i proj01 -o backup.dump

# 导出为 SQL 格式（人类可读）
pg export -i proj01 -o backup.sql

# 导出时使用 gzip 压缩（从 .gz 扩展名自动检测）
pg export -i proj01 -o backup.dump.gz
pg export -i proj01 -o backup.sql.gz

# 导出特定数据库
pg export -i proj01 -d mydb -o backup.dump

# 导出时指定压缩级别（0-9）
pg export -i proj01 -o backup.sql.gz --compress=9

# 导出时显示详细输出（显示进度）
pg export -i proj01 -o backup.dump -v

# 从自定义格式导入
pg import -i proj02 backup.dump

# 从 SQL 格式导入
pg import -i proj02 backup.sql

# 导入压缩文件（从 .gz 扩展名自动检测）
pg import -i proj02 backup.dump.gz

# 导入到特定数据库
pg import -i proj02 -d mydb backup.dump

# 导入时清理（恢复前删除现有对象）
pg import -i proj02 --clean backup.dump

# 导入时显示详细输出
pg import -i proj02 backup.dump -v

# 在实例间管道传输（无临时文件）
pg export -i proj01 | pg import -i proj02
pg export -i proj01 -d mydb | pg import -i proj02 -d mydb --clean

# 通过 SSH 跨主机管道传输
pg export -i proj01 | ssh user@remote "pg import -i proj02"
ssh user@remote "pg export -i proj01" | pg import -i proj02
ssh user@host1 "pg export -i proj01" | ssh user@host2 "pg import -i proj02"

# 通过连接字符串使用远程数据库（--dsn）
pg export --dsn postgres://user:pass@host:5432/mydb -o backup.dump
pg import --dsn postgres://user:pass@host:5432/mydb backup.dump --clean
pg export -i proj01 | pg import --dsn postgres://user:pass@host:5432/mydb
pg export --dsn postgres://user:pass@host1:5432/db1 | pg import --dsn postgres://user:pass@host2:5432/db2

# DSN 也可用于本地实例（当端口与默认值不同时很有用）
pg export --dsn postgres://admin:pass@127.0.0.1:35432/mydb | pg import --dsn postgres://admin:pass@127.0.0.1:35433/mydb --clean
```

**格式比较：**

| 特性 | 自定义（`.dump`） | SQL（`.sql`） |
|------|------------------|--------------|
| 导入速度 | 更快（二进制 COPY，并行恢复） | 更慢（文本 INSERT） |
| 文件大小 | 更小（压缩） | 更大（纯文本） |
| 人类可读 | 否 | 是 |
| 选择性恢复 | 是（特定表） | 否 |
| 最适合 | 迁移、备份、大型数据库 | 版本控制、CI 种子数据、手动编辑 |

**格式检测：** 使用魔数（基于内容）配合扩展名回退。
- 以 `PGDMP` 开头的文件 → 自定义格式（使用 `pg_restore`）
- `.sql` 或 `.sql.gz` → 纯 SQL 格式（使用 `psql`）
- `.gz` 扩展名或 gzip 魔数（`0x1f 0x8b`） → 自动解压
- 如果内容检测失败则使用扩展名作为回退

**远程数据库（--dsn）：** 使用连接字符串连接到任何 PostgreSQL 实例。
- 使用 pgcli 容器镜像中的 `pg_dump` 和 `pg_restore`（无需本地 PostgreSQL 安装）
- 适用于本地到远程、远程到本地和远程到远程迁移
- 支持与本地实例相同的所有标志（`-o`、`-d`、`--clean`、`-v`、`--compress`）

**关于现有数据的说明：** 导入到包含现有表的数据库会失败，除非你使用 `--clean` 标志，它会在恢复前删除对象。导入到已包含数据的数据库时使用 `--clean`。

**用例：**
- 在实例间迁移数据：`pg export -i proj01 | pg import -i proj02`
- 跨主机迁移：`pg export -i proj01 | pg import --dsn postgres://user:pass@remote:5432/db`
- 与团队共享数据库：`pg export -i proj01 -o dump.dump.gz`（压缩，更小文件）
- 重大更改前备份：`pg export -i proj01 -o pre-migration.sql.gz`
- CI/CD 管道：导出测试数据，导入到新的测试数据库
