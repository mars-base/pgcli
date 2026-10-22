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

DuckDB 的扩展系统在首次使用时安装共享库。pgcli 在安装时预装 `httpfs`——
它启用通过 HTTP/S3 读取文件的能力。`parquet` 和 `json` 是静态链接在 DuckDB CLI
二进制里的，始终可用，无需网络下载。

- **`httpfs`** —— 通过 HTTP/S3 读取文件（`read_parquet('s3://...')`）
- **`parquet`** —— Parquet 文件读写器（内置）
- **`json`** —— JSON 文件读取器（`read_json_auto(...)`）（内置）

后续可以从 DuckDB shell 里安装更多扩展：

```sql
INSTALL 'spatial';       -- 地理空间函数
INSTALL 'sqlite';        -- 读写 SQLite 数据库
INSTALL 'mysql';         -- 直接查询 MySQL
LOAD 'httpfs';           -- 在当前会话中加载已安装的扩展
```

如果希望手动管理扩展，或安装时主机无法上网，可在安装时加 `--no-extensions`。

## 访问 S3、Parquet、JSON 和 CSV 数据

DuckDB 天生为文件分析查询而生——无需服务器、无需 `COPY`、无需导入步骤。
直接指向你的数据即可。

### Parquet 文件

```sql
-- 单个文件
SELECT * FROM read_parquet('/tmp/events.parquet');

-- 通配符：目录下所有 parquet 文件
SELECT * FROM read_parquet('/data/logs/*.parquet');

-- schema 推断 + 计数
SELECT count(*) FROM read_parquet('s3://bucket/year=2026/*.parquet');

-- 指定列 + 过滤
SELECT user_id, event, ts
FROM read_parquet('/data/events.parquet')
WHERE ts > '2026-01-01'
LIMIT 100;
```

### JSON 文件

```sql
-- 自动推断 schema（支持 JSON lines 和嵌套 JSON）
SELECT * FROM read_json_auto('/tmp/users.json');

-- 显式 schema
SELECT * FROM read_json('/tmp/users.json',
    columns = {'id': 'INTEGER', 'name': 'VARCHAR', 'email': 'VARCHAR'},
    format = 'newline_delimited');

-- 嵌套对象：用 ->> 提取字段
SELECT
    data->>'user_id' AS user_id,
    data->>'event'   AS event
FROM read_json_auto('/tmp/events.jsonl');
```

### CSV 文件

```sql
-- 自动推断表头和类型
SELECT * FROM read_csv_auto('/tmp/sales.csv');

-- 显式列类型（自动推断不准时）
SELECT * FROM read_csv('/tmp/sales.csv',
    columns = {'id': 'INTEGER', 'amount': 'DOUBLE', 'date': 'DATE'},
    header = true);

-- TSV 或自定义分隔符
SELECT * FROM read_csv_auto('/tmp/data.tsv', delim = '\t');
```

### S3 访问（httpfs 扩展）

`httpfs` 扩展启用 S3 访问。凭据在每个会话里配置一次，或通过环境变量：

```sql
-- 在 DuckDB 会话中配置 S3 凭据
SET s3_endpoint = 'minio.example.com:9000';
SET s3_access_key_id = 'admin';
SET s3_secret_access_key = 'your-secret-key';
SET s3_use_ssl = false;   -- https 端点用 true
SET s3_region = 'us-east-1';   -- MinIO 写 'us-east-1' 即可（region 被忽略）

-- 从 S3 直接查询 Parquet
SELECT count(*) FROM read_parquet('s3://my-bucket/events/2026/*.parquet');

-- 从 S3 查询 CSV
SELECT * FROM read_csv_auto('s3://my-bucket/users.csv') LIMIT 10;

-- 从 S3 查询 JSON
SELECT * FROM read_json_auto('s3://my-bucket/config.json');

-- 写回 Parquet 到 S3（需要桶的写权限）
COPY (SELECT * FROM read_parquet('/tmp/*.parquet') WHERE year = 2026)
  TO 's3://output-bucket/filtered/2026.parquet' (FORMAT parquet);
```

#### 连接 pgcli 管理的 S3 存储

pgcli 管理了多种 S3 兼容存储（MinIO、silo、rustfs、garage、seaweedfs）。
DuckDB 直接查询它们——`s3_endpoint` 就是存储的 HTTP 地址，凭据来自
`pg addon password`。

先创建 S3 存储实例和桶。MinIO、silo（Pigsty 的 MinIO 分支）和 rustfs
（Rust 重新实现）的设置方式完全相同——它们共用 `pg mc` 包装命令，DuckDB
端也需要相同的 `s3_url_style = 'path'`：

```bash
# MinIO
pg addon install minio --name ms3
S3_SECRET=$(pg addon password minio --name ms3)

# 或 silo（功能面一致，与 minio 共用端口池）
pg addon install silo --name ms3
S3_SECRET=$(pg addon password silo --name ms3)

# 或 rustfs（Rust S3 兼容存储，仅 Linux）
pg addon install rustfs --name ms3
S3_SECRET=$(pg addon password rustfs --name ms3)

# 通过 pg mc 包装命令创建桶
pg mc alias set ms3 http://127.0.0.1:9000 admin "${S3_SECRET}"
pg mc mb ms3/my-bucket

# 上传测试数据（可选）
pg mc cp /tmp/data.parquet ms3/my-bucket/data.parquet
```

然后从 DuckDB 查询——MinIO、silo、rustfs 及兼容存储需要
`s3_url_style = 'path'`（不支持 virtual-host style）：

```bash
duckdb -c "
  SET s3_endpoint = '127.0.0.1:9000';
  SET s3_access_key_id = 'admin';
  SET s3_secret_access_key = '${S3_SECRET}';
  SET s3_use_ssl = false;
  SET s3_url_style = 'path';
  SELECT count(*) FROM read_parquet('s3://my-bucket/data/*.parquet');
"
```

如需更持久的配置，创建 DuckDB 启动文件（`~/.duckdbrc`），其中的 `SET` 命令
会在每次会话启动时执行：

```sql
-- ~/.duckdbrc
SET s3_endpoint = 'minio.example.com:9000';
SET s3_access_key_id = 'admin';
SET s3_secret_access_key = 'your-secret-key';
SET s3_use_ssl = false;
SET s3_url_style = 'path';
```

### S3 环境变量

DuckDB 也读取以下环境变量，免去在会话内 `SET`：

```bash
export DUCKDB_S3_ENDPOINT='minio.example.com:9000'
export DUCKDB_S3_ACCESS_KEY_ID='admin'
export DUCKDB_S3_SECRET_ACCESS_KEY='your-secret-key'
export DUCKDB_S3_USE_SSL='false'

duckdb -c "SELECT * FROM read_parquet('s3://my-bucket/data.parquet')"
```

