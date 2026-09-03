---
title: 快速开始
description: 安装 pgcli 并创建你的第一个 PostgreSQL 实例
weight: 10
icon: fa-solid fa-rocket
menus:
  main:
    identifier: docs-quickstart
    parent: docs
    weight: 10
    params:
      icon: fa-solid fa-rocket
---

几分钟内让 pgcli 运行起来。

## 安装

使用单条命令安装 pgcli：

```bash
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash
```

此脚本将：
- 为你的平台下载最新的 pgcli 二进制文件
- 安装到 `/usr/local/bin`（如果没有 sudo 则安装到 `~/.local/bin`）
- 将 pgcli 添加到你的 PATH

## 初始化配置

```bash
# 初始化配置并创建默认实例
pg config init --add default --base-dir /data/pg
```

这会在 `~/.pgcli/pg.yaml` 创建带有合理默认值的配置，包括：
- 名为 `default` 的默认实例
- 数据目录 `/data/pg/default`
- 从 35432 开始自动分配的端口

## 启动实例

```bash
# 启动默认实例
pg start

# 查看状态和连接信息
pg status
```

输出会显示连接 URL、管理员密码和备份状态。

## 连接到数据库

```bash
# 使用 pgcli 内置的 psql 封装
pg psql

# 或使用 pg status 中显示的连接字符串直接连接
psql postgres://admin:<password>@localhost:35432/admin_db

# 直接执行 SQL
pg exec "SELECT version()"
```

## 基本操作

```bash
# 列出所有实例
pg list

# 停止实例
pg stop

# 启动实例
pg start

# 查看实例状态
pg status

# 执行 SQL
pg exec "SELECT version();"
```

## 多实例管理

```bash
# 创建额外实例
pg create -i proj01 --base-dir /data/pg
pg create -i proj02 --base-dir /data/pg

# 列出所有实例
pg list

# 启动所有实例
pg start --all
```

## 多配置文件（隔离环境）

为同一主机上的隔离测试环境或每个项目一个配置，
使用不同的 `--namespace` 和**不重叠的端口范围**生成独立的配置文件。

```bash
# 环境 "t1"：容器前缀 pgcli-pg-t1-*，PG 端口从 38000 开始
pg config init -o ~/.pgcli-t1/pg.yaml --namespace t1 --pg-start-port 38000 --pg-ssh-port 43000 --add proj1

# 环境 "t2"：不同的命名空间和端口
pg config init -o ~/.pgcli-t2/pg.yaml --namespace t2 --pg-start-port 38100 --pg-ssh-port 43100 --add proj2

# 使用 -c 管理各环境
pg -c ~/.pgcli-t1/pg.yaml start -i proj1
pg -c ~/.pgcli-t2/pg.yaml list
```

| 参数 | 默认值 | 含义 |
|------|--------|------|
| `--namespace` | `default` | 容器名称前缀：`pgcli-pg-<namespace>-<instance>` |
| `--pg-start-port` | `35432` | 第一个 PG 主机端口；实例按顺序分配 |
| `--pg-ssh-port` | `42201` | 第一个 SSH 主机端口；按顺序分配 |

规划建议：

- **始终使用显式的 `--namespace`** 以避免容器名称冲突。
- **同一主机上的端口范围不能重叠。**
- **命名空间在创建时固化到容器名称中。** 后期更改需要 `pg destroy` 后重新初始化。

## 交互式 psql 会话

```bash
# 打开交互式 psql（默认实例）
pg psql

# 为指定实例打开 psql
pg psql -i proj01

# 从 stdin 读取 SQL（非交互，用于脚本）
echo "SELECT version();" | pg psql

# 执行单条 SQL 命令
pg psql -- -c "SHOW work_mem"

# 连接到不同数据库
pg psql -- -d postgres

# 使用 psql 元命令
pg psql -- -c "\dt"     # 列出表
pg psql -- -c "\du"     # 列出用户
pg psql -- -c "\l"      # 列出数据库

# 通过连接字符串连接远程数据库
pg psql --dsn postgres://user:pass@host:5432/db
```

在交互式 psql 中你可以使用：
- 带历史记录和 tab 补全的 SQL 查询
- psql 元命令（`\dt`、`\du`、`\l` 等）
- `\q` 退出

## 容器 Shell

```bash
# 在容器中打开 bash（默认实例）
pg shell

# 为指定实例打开 shell
pg shell -i proj01

# 直接运行命令
pg shell -- -c "ls -la /var/lib/postgresql/data"

# 查看日志
pg shell -- -c "tail -f /var/log/postgresql/postgresql-*.log"
```

Shell 以 `root` 身份在容器内运行，可以完全访问：
- PostgreSQL 数据目录（`/var/lib/postgresql/data`）
- 配置文件
- 日志文件（`/var/log/postgresql/`）
- 所有系统工具和实用程序

## 下一步

- 了解[备份](../backup/)与[恢复](../restore/)
- 设置[复制](../replica/)以实现高可用
- 探索[扩展](../extensions/)管理
- 了解[管理](../administration/)
