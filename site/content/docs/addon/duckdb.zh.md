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
`PATH` 上已有 `duckdb` 则直接复用），安装 `httpfs` 扩展（parquet 和 json
已内置于二进制中），并在 `pg.yaml` 中记录条目。

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
# 默认：v1.5.6 LTS，安装 httpfs 扩展（parquet/json 已内置）
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
| `--no-extensions` | 关 | 跳过安装 `httpfs` 扩展（parquet 和 json 已内置于二进制中） |
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

## Server/Client 模式（v2.0.0+）

DuckDB v2.0.0 引入了 **Quack 协议**，支持原生 server/client 模式。
DuckDB 可以通过 HTTP 对外提供数据库服务，多个客户端可以同时查询同一个实例。

### 启动服务端

```bash
# 交互式 server（stdin 必须保持打开）
duckdb /path/to/db.duckdb -serve

# 后台 server（重定向 stdin 以保持存活）
nohup duckdb /path/to/db.duckdb -serve < /dev/zero > /tmp/duckdb-serve.log 2>&1 &

# 或在 SQL 中使用 quack_serve()
duckdb -c "INSTALL quack; LOAD quack; CALL quack_serve(token='mytoken')"
```

服务端默认监听 `localhost:9494`，启动时会打印自动生成的 auth token。
默认只接受本地连接——如需远程访问，需在前面放一个 TLS 终止反向代理
（参见 [DuckDB 文档](https://duckdb.org/docs/2.0/quack/setup/reverse_proxy.html)）。

#### 跨主机访问（绑定 0.0.0.0）

要接受来自任意网络接口的连接，使用 `quack_serve()` 并指定参数（而非 `-serve`）：

```sql
-- 服务端：绑定到所有接口的 9494 端口
CALL quack_serve(
  listen_uri='quack:0.0.0.0:9494',
  allow_other_hostname=true,  -- 必须，允许非 localhost 绑定
  disable_ssl=true,            -- 可选，默认是 HTTPS
  token='mytoken'
);
```

需要三个参数：
- `listen_uri='quack:0.0.0.0:9494'` — 绑定到所有接口（不只是 localhost）
- `allow_other_hostname=true` — 绕过仅限 localhost 的限制
- `disable_ssl=true` — 使用纯 HTTP（默认是 HTTPS 并自动生成证书）

客户端使用服务端 IP 地址连接：

```bash
# 客户端连接远程服务端
duckdb -c "ATTACH 'quack:192.0.2.10:9494' AS qk (TOKEN 'mytoken', DISABLE_SSL true); CONNECT qk; SELECT * FROM t"
```

客户端只对 `localhost`、`127.0.0.1` 和 `::1` 自动使用 HTTP，其它地址默认走
HTTPS。服务端以 `disable_ssl=true` 启动时，客户端也必须传 `DISABLE_SSL true`
（如上例），否则会报 SSL 连接错误。

> **安全警告**：绑定 `0.0.0.0` 并使用 `disable_ssl=true` 会将数据库以明文形式暴露到网络。
> 仅在可信网络或防火墙后面使用。生产环境请使用带 TLS 的反向代理。

### 客户端连接

```bash
# 使用 -connect 参数（v2.0.0+）
duckdb -connect -c "SELECT * FROM t"

# 或通过 SQL（简写形式，默认 localhost）
duckdb -c "ATTACH 'quack:localhost' AS qk (TOKEN 'BAED...'); CONNECT qk; SELECT * FROM t"

# 显式 ip:port 形式（远程连接或非默认端口时使用）
duckdb -c "ATTACH 'quack:127.0.0.1:9494' AS qk (TOKEN 'BAED...'); CONNECT qk; SELECT * FROM t"

# 明文 HTTP 远程服务端
duckdb -c "ATTACH 'quack:192.0.2.10:9494' AS qk (TOKEN 'BAED...', DISABLE_SSL true); CONNECT qk; SELECT * FROM t"
```

`-connect` 参数会自动发现服务端输出的 token。手动连接时，从服务端启动
信息中复制 token 即可。

`CONNECT` 激活期间，未限定的 SQL——包括使用本地路径的 `read_csv` /
`read_parquet`——都在服务端执行，客户端本地文件不可见。要回到本地数据库执行，
先 `DISCONNECT`；或者不使用 `CONNECT`，用 `qk.<table>` 限定名在同一个会话里
混合本地与远端查询。

### S3/OSS 数据 + 远端客户端

常见部署方式是把数据文件放在对象存储（AWS S3、阿里云 OSS、MinIO 等），
ECS 上只运行 Quack server。服务端通过 `httpfs` 读取文件，远端客户端既不需要
S3 凭据，也不需要能访问桶——只查询服务端即可。

在服务端配置 S3 secret、创建视图，并在同一个会话里启动 Quack（视图在查询时
解析）：

```sql
CREATE SECRET (
  TYPE S3,
  ENDPOINT 'oss-cn-hangzhou.aliyuncs.com',  -- AWS S3 省略此项，改用 REGION
  KEY_ID '...',
  SECRET '...',
  USE_SSL true,
  URL_STYLE 'vhost'                         -- MinIO/silo/rustfs 用 'path'
);

CREATE VIEW events AS
  SELECT * FROM read_parquet('s3://my-bucket/events/*.parquet');

CALL quack_serve(
  listen_uri = 'quack:0.0.0.0:9494',
  allow_other_hostname = true,
  token = 'my_token',
  ssl_cert_file = '/etc/duckdb/server.crt',  -- 省略则自动生成证书
  ssl_key_file  = '/etc/duckdb/server.key'
);
```

非交互方式启动时，需要保持 stdin 打开：

```bash
( cat server.sql; tail -f /dev/null ) | duckdb
```

本地连接。使用 TLS（推荐）时客户端不需要 `DISABLE_SSL`：

```bash
duckdb -c "ATTACH 'quack:ecs.example.com:9494' AS qk (TOKEN 'my_token'); CONNECT qk; SELECT count(*) FROM events"
```

不加 `CONNECT` 时要写限定名 `qk.events`；未限定的表名会在客户端本地目录解析，
找不到服务端视图。`read_parquet` 本身始终在服务端执行，S3 凭据只需放在服务端。

明文 HTTP 的测试服务端要在客户端加 `DISABLE_SSL true`（见上一节）；客户端
若使用 HTTP 代理，记得把服务端主机加入 `NO_PROXY`。

该模式已用 MinIO 模拟对象存储做过端到端验证：`s3://ducktest/events/events.parquet`
中的 10 万行 Parquet 被服务端暴露为视图，远端客户端通过 `ATTACH … CONNECT`
和 `quack_query()` 都得到相同的行数与求和结果。

- S3 凭据只保留在服务端会话（或 `~/.duckdbrc`）中，客户端不需要。
- token 授予的是服务端完整 SQL 执行能力，而不只是访问暴露的视图：客户端可以
  查询服务端凭据能访问的任意 S3 对象，以及服务端进程能读取的任意本地文件。
  请使用最小权限的专用 S3 key，并限制 Quack 端口的网络访问。
- Quack 端口应限制在安全组/防火墙内；token 认证不能替代网络隔离。

#### 分析服务端的其他文件

视图只是入口之一。服务端执行的任意 SQL 都能用服务端的 S3 凭据读取其他对象，
也能读取服务端本地文件。一次性分析可用 `quack_query`，无需 attach：

```sql
-- 聚合一个不在任何视图中的 S3 CSV
FROM quack_query(
  'quack:ecs.example.com:9494',
  'SELECT count(*) AS n, round(avg(score), 1) AS avg_score
     FROM read_csv_auto(''s3://my-bucket/csv/users.csv'', header = true)',
  token = 'my_token'
);

-- 两个对象前缀在服务端完成 join
FROM quack_query(
  'quack:ecs.example.com:9494',
  'SELECT count(*) FROM read_parquet(''s3://my-bucket/a/*.parquet'') a
     JOIN read_parquet(''s3://my-bucket/b/*.parquet'') b USING (id)',
  token = 'my_token'
);
```

使用 attach 时，可以 `CONNECT` 后直接写普通 SQL，或保持本地会话用 attach 的
`query` 宏：

```sql
ATTACH 'quack:ecs.example.com:9494' AS qk (TOKEN 'my_token');

CONNECT qk;
SELECT count(*) FROM read_parquet('s3://my-bucket/other/*.parquet');

-- 不 CONNECT 的等价写法：SQL 字符串在服务端执行
SELECT * FROM qk.query(
  'SELECT count(*) FROM read_parquet(''s3://my-bucket/other/*.parquet'')'
);
```

以上示例默认使用 HTTPS。明文 HTTP 服务端需给 `quack_query` 加
`disable_ssl = true`，或给 `ATTACH` 加 `DISABLE_SSL true`。在本地执行的语句
使用客户端自己的凭据——本地直接 `read_parquet('s3://...')` 在没有客户端
S3 secret 时会失败。

### 通过 HTTP API 调用

Quack 是 HTTP RPC 协议：客户端向 `/quack` 发送 `POST`，请求头带
`Authorization: Bearer <token>` 和 `Content-Type: application/vnd.duckdb`。
请求与响应用 DuckDB 内部二进制序列化（与 WAL 相同的格式），不是 JSON，
因此手写 HTTP 请求只适合探活，不适合直接执行 SQL。

```bash
# 协议提示 / 探活
curl http://192.0.2.10:9494/

# CORS 预检
curl -i -X OPTIONS http://192.0.2.10:9494/quack    # 204
```

没有 JSON/REST 接口：向 `/quack` POST 一个 JSON body 会被当成二进制协议帧，
反序列化失败。要从其他语言通过 HTTP 执行 SQL，应使用 DuckDB 客户端库调用
`quack_query`（或在已 attach 的 catalog 上用 `quack_query_by_name`），或者自己
在 DuckDB 前面包一层 JSON 服务。

需要 API 风格的无状态调用时，用 `quack_query`——每次查询一个请求/响应，
不需要 `ATTACH`：

```sql
FROM quack_query(
  'quack:192.0.2.10:9494',
  'SELECT count(*) AS rows FROM events',
  token = 'mytoken',
  disable_ssl = true          -- HTTPS 服务端省略
);
```

任何 DuckDB 客户端（CLI、Python 等）都能执行这段 SQL。`quack_query` 是实用的
HTTP API 入口；需要长会话时用 `ATTACH` + `CONNECT`。

#### Python 示例

Python 客户端使用同一套 SQL；`duckdb` 必须是 v2.0.0 或更高，1.x 不支持
Quack。用 `uv` 安装：

```bash
# 推荐：项目虚拟环境
uv venv .venv
source .venv/bin/activate
uv pip install 'duckdb>=2.0.0'

# 隔离到临时目录（不影响当前环境）
uv pip install --target /tmp/duckpkg 'duckdb==2.0.0'
PYTHONPATH=/tmp/duckpkg python3 quack_demo.py

# 一次性运行，不持久安装
uv run --with 'duckdb>=2.0.0' python3 quack_demo.py
```

```python
import os
os.environ["NO_PROXY"] = "ecs.example.com"   # 明文 HTTP + 本机有代理时

import duckdb

con = duckdb.connect()

rows = con.execute("""
    FROM quack_query(
        'quack:ecs.example.com:9494',
        'SELECT count(*) AS rows, sum(amount)::BIGINT AS total FROM events',
        token = 'my_token',
        disable_ssl = true
    )
""").fetchall()
print(rows)

# attach 一次，用限定名查询视图
con.execute("ATTACH 'quack:ecs.example.com:9494' AS qk (TOKEN 'my_token', DISABLE_SSL true)")
print(con.execute("SELECT count(*) FROM qk.events").fetchall())

# 保持本地会话，通过 query 宏在服务端执行任意 SQL
print(con.execute("""
    SELECT * FROM qk.query(
        'SELECT count(*) FROM read_parquet(''s3://my-bucket/other/*.parquet'')'
    )
""").fetchall())
```

`fetchdf()` 需要 pandas；`fetchall()` 不需要。服务端是明文 HTTP 且环境配置了
HTTP 代理时，要在 import duckdb 之前设置 `NO_PROXY`。

### 注意事项

- **stdin 必须保持打开**：服务端在 stdin 关闭后会退出（后台运行用 `< /dev/zero` 或 `tail -f /dev/null |`）
- **默认仅限本地**：远程访问需绑定 `0.0.0.0` 并设置 `allow_other_hostname=true`；建议配合 TLS 反向代理或保持 `disable_ssl=false`
- **Auth token**：自动生成，启动时打印，客户端连接时必须提供
- **pgcli 不管理 Quack server**：`pg addon start/stop duckdb` 仍然是无操作——Quack server 是一个独立进程，需要自行管理
- **HTTP 代理**：客户端遵循 `HTTP_PROXY`/`HTTPS_PROXY`。连接内网主机的明文 HTTP
  服务时，请求可能被代理截走并报 `Unauthorized`；把该主机加入 `NO_PROXY`
  （如 `NO_PROXY=192.0.2.10`）即可

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

`httpfs` 扩展启用 S3 访问。凭据和端点通过 `CREATE SECRET`（v2.0.0+）或传统
`SET` 命令（v1.5.x）配置。

#### DuckDB v2.0.0+ — `CREATE SECRET`

```sql
-- 配置 S3 凭据（v2.0.0+ 语法）
CREATE SECRET (
  TYPE S3,
  ENDPOINT 'minio.example.com:9000',
  KEY_ID 'admin',
  SECRET 'your-secret-key',
  USE_SSL false,
  URL_STYLE 'path'   -- MinIO 及兼容存储必须设置
);

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

#### DuckDB v1.5.x — 传统 `SET` 命令

```sql
-- 在 DuckDB 会话中配置 S3 凭据（仅 v1.5.x）
SET s3_endpoint = 'minio.example.com:9000';
SET s3_access_key_id = 'admin';
SET s3_secret_access_key = 'your-secret-key';
SET s3_use_ssl = false;   -- https 端点用 true
SET s3_region = 'us-east-1';   -- MinIO 写 'us-east-1' 即可（region 被忽略）
SET s3_url_style = 'path';   -- MinIO 及兼容存储必须设置
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
`URL_STYLE 'path'`（不支持 virtual-host style）：

```bash
# DuckDB v2.0.0+
duckdb -c "
  CREATE SECRET (
    TYPE S3,
    ENDPOINT '127.0.0.1:9000',
    KEY_ID 'admin',
    SECRET '${S3_SECRET}',
    USE_SSL false,
    URL_STYLE 'path'
  );
  SELECT count(*) FROM read_parquet('s3://my-bucket/data/*.parquet');
"

# DuckDB v1.5.x（传统 SET 语法）
duckdb -c "
  SET s3_endpoint = '127.0.0.1:9000';
  SET s3_access_key_id = 'admin';
  SET s3_secret_access_key = '${S3_SECRET}';
  SET s3_use_ssl = false;
  SET s3_url_style = 'path';
  SELECT count(*) FROM read_parquet('s3://my-bucket/data/*.parquet');
"
```

如需更持久的配置，创建 DuckDB 启动文件（`~/.duckdbrc`），其中的命令
会在每次会话启动时执行：

```sql
-- ~/.duckdbrc (DuckDB v2.0.0+)
CREATE SECRET (
  TYPE S3,
  ENDPOINT 'minio.example.com:9000',
  KEY_ID 'admin',
  SECRET 'your-secret-key',
  USE_SSL false,
  URL_STYLE 'path'
);
```

### S3 环境变量

DuckDB 从 `DUCKDB_S3_*` 环境变量读取端点和 SSL 设置，但凭据必须使用标准 AWS
变量（`AWS_ACCESS_KEY_ID`、`AWS_SECRET_ACCESS_KEY`）——DuckDB 的
`enable_global_s3_configuration` 会自动检测这些变量。

> **v2.0.0 限制**：`URL_STYLE` 无法通过环境变量设置。它默认为 virtual-host，
> 会导致 MinIO/silo/rustfs（需要 path-style）无法访问。对于这些存储，
> 应使用 `CREATE SECRET` 并指定 `URL_STYLE 'path'`。

```bash
# 端点和 SSL 通过 DUCKDB_S3_*
export DUCKDB_S3_ENDPOINT='127.0.0.1:9000'
export DUCKDB_S3_USE_SSL='false'

# 凭据通过 AWS_*（标准变量，DuckDB 自动检测）
export AWS_ACCESS_KEY_ID='admin'
export AWS_SECRET_ACCESS_KEY='your-secret-key'

# URL 样式仍需在会话或启动文件中设置（v1.5.x）
duckdb -c "SET s3_url_style = 'path'; SELECT * FROM read_parquet('s3://my-bucket/data.parquet');"

# v2.0.0 中 URL_STYLE 必须通过 CREATE SECRET 设置——环境变量无法覆盖
# MinIO/silo/rustfs 不支持 virtual-host style，必须显式指定 path
duckdb -c "
  CREATE SECRET (TYPE S3, ENDPOINT '127.0.0.1:9000', KEY_ID 'admin',
    SECRET 'your-secret-key', USE_SSL false, URL_STYLE 'path');
  SELECT * FROM read_parquet('s3://my-bucket/data.parquet');
"
```

对于 MinIO/silo/rustfs，仍需设置 `s3_url_style = 'path'`，因为这些存储
不支持虚拟主机样式 URL。

