---
title: "DuckDB"
description: "以 pgcli 插件方式安装嵌入式 OLAP 分析数据库（宿主二进制）。"
weight: 61
---

[DuckDB](https://duckdb.org/) 是嵌入式分析数据库——常被称作"SQLite for analytics"。
C++ 编写的列式引擎，向量化执行，可以直接查询 Parquet、CSV、JSON 和 Arrow 文件，
无需服务器进程。把它当成一个单二进制的 OLAP 引擎，指向你的数据即可使用：
加载 `httpfs` 扩展后，`SELECT * FROM read_parquet('s3://bucket/data.parquet')`
开箱即用。

作为 pgcli 插件，DuckDB 是**宿主二进制**形态：没有容器，没有守护进程。
`pg addon install duckdb` 从 GitHub releases 下载官方 CLI zip（sha256 校验；
`PATH` 上已有 `duckdb` 则直接复用），安装常用扩展（`httpfs`、`parquet`、
`json`），并在 `pg.yaml` 中记录条目。

> **平台**：仅 Linux，仅 amd64（macOS 及其他架构明确拒绝并给出错误提示）。
> DuckDB 是一个独立的 CLI 工具——不作为后台服务运行、不监听端口、不会传播进
> PG 实例容器。

## 为什么在 pgcli 旁边装 DuckDB

DuckDB 填补了 PostgreSQL 行存引擎不擅长的分析场景：对大量 Parquet/CSV 数据集的
即席查询、数据湖探索、加载进 PostgreSQL 之前的 ETL 预处理，或者对已经躺在 S3 里
的文件做快速聚合。pgcli 已经管理了多种 S3 存储（MinIO、silo、rustfs、garage、
seaweedfs）——DuckDB 通过 `httpfs` 直接查询其中的内容。

## 用法

```bash
# 默认：v1.5.6 LTS，安装 httpfs+parquet+json 扩展
pg addon install duckdb

# 指定版本
pg addon install duckdb --version 2.0.0

# 自定义名称和数据目录
pg addon install duckdb --name analytics --data-dir /srv/duckdb

# 跳过扩展安装
pg addon install duckdb --no-extensions

# 重新安装并更换参数（条目已存在时）
pg addon install duckdb --version 2.0.0 --force
```

安装完成后，启动交互式 shell 或执行一次性查询：

```bash
# 交互式 shell（数据目录作为工作目录）
pg addon duckdb shell

# 对本地 Parquet 文件的一次性查询
pg addon duckdb shell -- -c "SELECT * FROM read_parquet('/tmp/data.parquet')"

# 直接查询 S3（需要 httpfs 扩展，默认已安装）
duckdb -c "SELECT count(*) FROM read_parquet('s3://bucket/data/*.parquet')"

# 打开持久化数据库文件
duckdb ~/.pgcli/addon/duckdb/duckdb/analytics.duckdb
```

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--name` | `duckdb` | 实例名（配置 map 的键） |
| `--version` | `1.5.6` | 要下载的版本（sha256 已 pin 在 pgcli 源码里；支持 `1.5.6` LTS 和 `2.0.0`） |
| `--data-dir` | `<base>/addon/duckdb/<name>` | `.duckdb` 数据库文件的默认目录 |
| `--install-dir` | `/usr/local/bin` | 宿主二进制落点（不可写时退到 `~/.local/bin`——注意保持其在 `PATH` 上） |
| `--no-extensions` | 关 | 跳过安装常用扩展（`httpfs`、`parquet`、`json`） |
| `--force` | 关 | 按命令行参数重建配置条目 |

## 日常管理

```bash
pg addon duckdb status --name duckdb        # 二进制版本、路径、数据目录
pg addon duckdb shell --name duckdb         # 交互式 DuckDB 会话
pg addon duckdb shell -- -c "SELECT 1"      # 一次性查询（-- 后面的参数传给 duckdb）
pg addon list                               # duckdb 显示在"Analytics add-ons"下
pg addon start duckdb                       # 无操作（CLI 工具，非守护进程）
pg addon stop  duckdb                       # 无操作（CLI 工具，非守护进程）
pg addon remove duckdb --name duckdb        # 忘掉配置；二进制和数据目录保留
pg addon remove duckdb --name duckdb --clean-data   # 同时删除数据目录
```

`remove` 从不删除二进制——它可能被多个命名实例共享。加 `--clean-data` 删除
数据目录（数据库文件）；不加则保留目录并打印提示。

## 扩展

DuckDB 的扩展系统在首次使用时安装共享库。pgcli 在安装时预装三个常用扩展：

- **`httpfs`** —— 通过 HTTP/S3 读取文件（`read_parquet('s3://...')`）
- **`parquet`** —— Parquet 文件读写器
- **`json`** —— JSON 文件读取器（`read_json_auto(...)`）

后续可以从 DuckDB shell 里安装更多扩展：

```sql
INSTALL 'spatial';       -- 地理空间函数
INSTALL 'sqlite';        -- 读写 SQLite 数据库
INSTALL 'mysql';         -- 直接查询 MySQL
LOAD 'httpfs';           -- 在当前会话中加载已安装的扩展
```

如果希望手动管理扩展，或安装时主机无法上网，可在安装时加 `--no-extensions`。
