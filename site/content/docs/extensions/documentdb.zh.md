---
title: "DocumentDB"
description: "用 pgcli 安装官方 DocumentDB PostgreSQL 18 扩展，包括预加载库、后台 worker 以及网关使用的共享 Unix socket。"
weight: 25
---

[DocumentDB](https://documentdb.io/) 是 Microsoft 开源的 PostgreSQL 文档数据库层。它分成两部分：

- **PostgreSQL 扩展**（`documentdb`、`documentdb_core`、`documentdb_extended_rum`）——在 PostgreSQL 内部存储 BSON 文档、索引与查询。本页用 pgcli 安装它。
- **MongoDB wire protocol 网关**——接受 MongoDB 客户端的独立 Rust 服务。用 [`pg addon install documentdb`](../../addon/documentdb/) 单独安装。

本页讲扩展。网关依赖扩展，所以先装扩展。

> **仅支持 PostgreSQL 18。** 官方 DocumentDB v1.0 vendor 包为 PostgreSQL 18 构建。其他大版本会被 pgcli 拒绝。不支持 Patroni/HA 实例，因为 DocumentDB 的设置写入 `postgresql.conf`，会被 Patroni 的 DCS 覆盖。

## 安装

```bash
pg extension install documentdb -i <instance> --auto-restart
```

`--auto-restart` 让 pgcli 立即重启 PostgreSQL：DocumentDB 的库是 postmaster 级别（`shared_preload_libraries`），仅 reload 不够。

命令做了什么：

1. 从 GitHub releases 下载官方 vendor DEB `ubuntu24.04-postgresql-18-documentdb_1.0-0_<arch>.deb`（amd64/arm64），并校验 SHA-256。
2. 下载 Ubuntu 24.04 的 ICU 74 运行时（`libicu74`）。vendor 的 `pg_documentdb_core.so` 引用 ICU 74 符号，而 Debian 13 自带 ICU 76。镜像构建期间 pgcli 安装 `libicu74`，并用 `patchelf` 给该库加上 `libicui18n.so.74` 的 `DT_NEEDED` 条目，使其能在 Debian 基础的 PG 18 镜像上加载。
3. 用其余包（`postgresql-18-cron`、`postgis`、`pgvector` 等）构建共享的 `-ext` 镜像。
4. 从新镜像重建实例容器，并添加共享 Unix socket volume（`pgcli-pg-sock-<container>` 挂载到 `/var/run/postgresql`）。网关 add-on 之后读取该 socket。
5. 写入受管的 `postgresql.conf` 块，并在实例配置的数据库中创建 SQL 扩展。

## pgcli 写入的配置

`shared_preload_libraries`（顺序重要）：

```
shared_preload_libraries = 'pg_cron,pg_documentdb_core,pg_documentdb,pg_documentdb_extended_rum'
```

受管设置：

| 设置 | 值 | 用途 |
|---|---|---|
| `cron.database_name` | 实例数据库 | `pg_cron` 运行所在的数据库 |
| `cron.use_background_workers` | `on` | 使用后台 worker 而不是 launcher |
| `documentdb.enableBackgroundWorker` | `true` | DocumentDB 后台 worker |
| `documentdb.enableBackgroundWorkerJobs` | `true` | 定时索引/清理任务 |
| `documentdb.indexBuildsScheduledOnBgWorker` | `false` | 索引构建留在前台 |
| `documentdb.localhost_connection_string` | `host=/var/run/postgresql port=<instance port>` | 扩展 loader 的回连方式 |
| `default_toast_compression` | `lz4` | 压缩大的文档值 |
| `documentdb.rum_library_load_option` | `require_documentdb_extended_rum` | 强制要求 RUM 配套扩展 |
| `documentdb.alternate_index_handler_name` | `extended_rum` | DocumentDB 索引走 RUM |

SQL 侧按顺序执行：

```sql
CREATE EXTENSION IF NOT EXISTS pg_cron;
CREATE EXTENSION IF NOT EXISTS "documentdb" CASCADE;
CREATE EXTENSION IF NOT EXISTS "documentdb_extended_rum";
```

`CREATE EXTENSION documentdb CASCADE` 会创建 `documentdb_core`，但**不会**创建 `documentdb_extended_rum`；pgcli 显式创建它。批量执行后验证四个扩展都存在，任一缺失则中止安装。

## 验证

```bash
pg exec -i <instance> "SHOW shared_preload_libraries"
pg exec -i <instance> "SHOW documentdb.localhost_connection_string"
pg exec -i <instance> "SELECT extname FROM pg_extension ORDER BY 1"
```

预期扩展包含 `documentdb`、`documentdb_core`、`documentdb_extended_rum` 与 `pg_cron`。实例必须先带 socket 挂载重建，网关才能安装；检查方式：

```bash
podman inspect <pg container> --format '{{range .Mounts}}{{.Destination}}{{"\n"}}{{end}}'
```

必须列出 `/var/run/postgresql`。

## 移除

```bash
pg extension remove documentdb -i <instance> --auto-restart
```

移除刻意**不**用 `CASCADE`：DocumentDB 集合是普通 PostgreSQL 对象，数据仍在时 DROP 扩展会失败。先删除文档数据，再移除扩展。pgcli 按依赖顺序 drop：

```sql
DROP EXTENSION IF EXISTS "documentdb_extended_rum";
DROP EXTENSION IF EXISTS "documentdb";
DROP EXTENSION IF EXISTS "documentdb_core";
```

仅当 `extensions:` 中没有其他扩展需要 `pg_cron`、**且** `cron.job` 为空时才 drop `pg_cron`；否则保留并给出警告。DocumentDB drop 失败会在保存配置前中止，受管扩展列表保持完整。

移除后扩展包仍留在共享的 `-ext` 镜像里（pgcli 从不从镜像卸载包）。从一个实例移除不影响使用同一 `-ext` 镜像的其他实例。

## 说明

- **Vendor 包，非 Pigsty**：DocumentDB 不在 Pigsty DEB 镜像中。pgcli 直接从官方 GitHub release 下载并校验和。
- **镜像构建需要网络**访问以下载 DEB；校验和固定了确切的构建产物。
- **MACVLAN/多实例**：每个实例有自己的 socket volume；网关 add-on 用 `--pg-name` 选择实例。
- **遥测**：扩展本身不带遥测；网关 add-on 渲染配置时显式关闭 metrics 与 tracing。
