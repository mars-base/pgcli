---
title: Exec、psql 和 shell
description: pg exec、psql 和 shell 指南
weight: 20
icon: fa-solid fa-book
cascade:
  type: docs
  footer_style: slim
---


两种对实例运行 SQL 的方式：`pg exec` 用于一次性 SQL 或容器命令，`pg psql` 用于交互式会话，`pg shell` 用于在容器内打开交互式 bash shell。

## pg exec

### SQL 模式（默认）

没有 `--` 的参数作为 SQL 通过 psql 执行，使用实例配置的用户和数据库。

```bash
pg exec "SELECT version()"
pg exec -i proj01 "SELECT count(*) FROM users"
pg exec "CREATE TABLE test (id serial PRIMARY KEY, msg text)"
```

### 容器命令模式（-- 之后）

`--` 之后的参数直接在容器内运行（以 root 身份）。

```bash
pg exec -- pg_isready
pg exec -- ls -la /var/lib/postgresql/data
pg exec -- bash -c "cat /var/lib/postgresql/data/postgresql.conf"
pg exec -- tail -f /var/log/postgresql/postgresql-*.log
```

### 远程数据库（--dsn）

通过连接字符串对任何可达数据库执行 SQL，使用临时容器。`--dsn` 仅支持 SQL 模式；容器命令需要本地实例。

```bash
pg exec --dsn postgres://user:pass@host:5432/db "SELECT count(*) FROM users"
```

## pg psql

### 交互式会话

```bash
pg psql                          # 默认实例
pg psql -i proj01                # 特定实例
```

在 shell 内你获得完整的 psql 功能：带历史记录和 tab 补全的 SQL、元命令（`\dt`、`\du`、`\l`），以及 `\q` 退出。

### 非交互式（脚本）

```bash
echo "SELECT version();" | pg psql        # 来自 stdin 的 SQL
pg psql -- -c "SHOW work_mem"             # 单条命令
pg psql -- -d other_db                    # 连接到不同数据库
pg psql -- -U other_user                  # 以不同用户连接
```

### 切换到 postgres 超级用户

某些管理任务（例如创建某些扩展、修改系统级设置）需要 postgres 超级用户权限。使用 `--` 传递 psql 参数并切换用户：

```bash
pg psql -i pg01 -- -U postgres -d postgres
```

**推荐方法**：日常操作使用实例默认用户（admin），仅在需要超级用户权限时切换到 postgres。这比修改配置文件或重启容器更安全更方便。

示例场景：
```bash
# 创建需要超级用户权限的扩展
pg psql -i pg01 -- -U postgres -d postgres -c "CREATE EXTENSION pg_cron"

# 配置 cron.database_name（pg_cron 特定参数）
pg psql -i pg01 -- -U postgres -d postgres -c "ALTER SYSTEM SET cron.database_name = 'pg01_db'"

# 查看系统级配置
pg psql -i pg01 -- -U postgres -d postgres -c "SHOW shared_preload_libraries"
```

### 远程数据库（--dsn）

```bash
pg psql --dsn postgres://user:pass@host:5432/db
```

## pg shell

在 PostgreSQL 容器内打开交互式 bash shell。

```bash
pg shell                             # 交互式 bash
pg shell -i proj01                   # 特定实例
pg shell -- -c "ls -la /var/lib/postgresql/data"
pg shell -- -c "cat /etc/postgresql/postgresql.conf"
```

使用场景：
- 检查 PostgreSQL 数据目录和日志文件
- 运行容器内部命令（如 `psql`、`pg_isready`、`pg_repack`）
- 调试扩展安装和配置文件

## 规则

- `--dsn` 和 `--instance` 互斥：连接字符串确定主机、端口和数据库，因此 `-i` 被拒绝以避免静默误用。
- 使用 `--dsn` 时，数据库是 URL 的路径部分：`postgres://user:pass@host:5432/mydb` 连接到 `mydb`。要使用另一个数据库，更改路径。
- 使用本地实例时，`--` 传递原始 psql 参数（包括 `-d`/`-U`），覆盖实例默认值。
