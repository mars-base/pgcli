---
title: "插件 (Addons)"
description: "pgcli 插件管理指南"
weight: 70
icon: fa-solid fa-puzzle-piece
menus:
  main:
    identifier: docs-addon
    parent: docs
    weight: 70
    params:
      icon: fa-solid fa-puzzle-piece
cascade:
  type: docs
  footer_style: slim
---

pgcli 支持通过插件系统扩展 PostgreSQL 功能。插件是独立的容器，为 PostgreSQL 实例提供额外能力，无需修改数据库本身。

## 支持的插件

目前支持以下插件：

| 插件 | 说明 | 容器镜像 |
|------|------|----------|
| `pgbouncer` | 连接池管理器，提供事务级连接池化 | `edoburu/pgbouncer:latest` |

## 工作原理

插件作为 sidecar 容器运行，通过挂载配置文件与 PostgreSQL 实例协同工作：

1. **`pg addon install`** 生成配置文件并启动插件容器
2. 配置文件挂载到 `<base-dir>/addon/<addon-name>/` 目录
3. 插件容器通过主机网络与 PostgreSQL 实例通信
4. 配置文件更新时自动重启容器应用变更

优势：
- 插件与 PostgreSQL 实例解耦，可独立管理
- 配置文件集中存储在 `<base-dir>/addon/` 目录
- 支持动态调整参数，无需重建容器
- 用户密码自动从 `pg_shadow` 同步

## 命令

### 安装插件

```bash
# 安装 PgBouncer 连接池
pg addon install pgbouncer -i mypg

# 指定连接池参数
pg addon install pgbouncer -i mypg \
  --max-client-conn 200 \
  --default-pool-size 30 \
  --min-pool-size 5 \
  --reserve-pool-size 10 \
  --max-db-connections 50 \
  --query-timeout 60 \
  --admin-users admin \
  --log-connections 1
```

**参数说明：**

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--max-client-conn` | 最大客户端连接数 | 100 |
| `--default-pool-size` | 默认连接池大小 | 20 |
| `--min-pool-size` | 最小连接池大小（预热） | 0 |
| `--reserve-pool-size` | 预留连接池大小（突发） | 0 |
| `--max-db-connections` | 每数据库最大连接数 | 50 |
| `--max-user-connections` | 每用户最大连接数 | 0（无限制） |
| `--server-idle-timeout` | 空闲服务端连接超时（秒） | 600 |
| `--server-lifetime` | 服务端连接最大生命周期（秒） | 3600 |
| `--server-connect-timeout` | 连接 PostgreSQL 超时（秒） | 15 |
| `--query-timeout` | 查询超时（秒） | 0（无限制） |
| `--query-wait-timeout` | 等待连接超时（秒） | 120 |
| `--idle-transaction-timeout` | 空闲事务超时（秒） | 0 |
| `--transaction-timeout` | 事务超时（秒） | 0 |
| `--admin-users` | 管理员用户列表 | （空） |
| `--stats-users` | 只读统计用户列表 | （空） |
| `--log-connections` | 记录连接日志 | 0 |
| `--log-disconnections` | 记录断开日志 | 0 |

### 列出已安装插件

```bash
pg addon list
```

示例输出：
```
Add-ons:
  pgbouncer (container: pgcli-addon-pgbouncer-mypg)
    Status: running
    Port: 6432
    Pool mode: transaction
    Config: /data/addon/pgbouncer/pgbouncer.ini
```

### 卸载插件

```bash
# 卸载 PgBouncer
pg addon remove pgbouncer -i mypg
```

工作流：
1. 停止并删除插件容器
2. 删除 `<base-dir>/addon/<addon-name>/` 目录及配置文件
3. 从 `pg.yaml` 中移除插件配置

## 配置文件

安装插件后，`pg.yaml` 会添加插件配置：

```yaml
instances:
  mypg:
    addons:
      pgbouncer:
        enabled: true
        max_client_conn: 200
        default_pool_size: 30
        min_pool_size: 5
        reserve_pool_size: 10
        max_db_connections: 50
        query_timeout: 60
        admin_users: admin
        log_connections: 1
```

配置文件存储在 `<base-dir>/addon/pgbouncer/`：

```
/data/addon/pgbouncer/
├── pgbouncer.ini    # PgBouncer 主配置
├── userlist.txt     # 用户密码列表（自动同步）
└── stats/           # 统计目录（可选）
```

## 用户密码同步

PgBouncer 插件会自动从 `pg_shadow` 同步用户密码到 `userlist.txt`：

```bash
# 查看用户列表
cat /data/addon/pgbouncer/userlist.txt
```

输出示例：
```
"admin" "SCRAM-SHA-256$4096:xxx:yyy"
"app_user" "SCRAM-SHA-256$4096:xxx:yyy"
```

**自动同步：** 每次运行 `pg addon install pgbouncer` 时，都会重新查询 `pg_shadow` 并更新 `userlist.txt`，确保密码与 PostgreSQL 保持一致。

## 连接方式

安装 PgBouncer 后，客户端通过插件端口连接：

```bash
# 直接连接 PostgreSQL（端口 5432）
psql "postgres://user:pass@localhost:5432/mypg"

# 通过 PgBouncer 连接（端口 6432）
psql "postgres://user:pass@localhost:6432/mypg"
```

**端口分配：** PgBouncer 默认使用端口 6432。如果端口被占用，pgcli 会自动分配下一个可用端口。

查看当前端口：
```bash
pg addon list
```

## 使用场景

### 高并发场景

```bash
pg addon install pgbouncer -i mypg \
  --max-client-conn 1000 \
  --default-pool-size 50 \
  --reserve-pool-size 20 \
  --max-db-connections 100
```

### 短连接应用

```bash
pg addon install pgbouncer -i mypg \
  --pool-mode transaction \
  --server-idle-timeout 60 \
  --server-lifetime 600
```

### 长连接应用

```bash
pg addon install pgbouncer -i mypg \
  --pool-mode session \
  --server-lifetime 86400
```

### 只读副本

```bash
pg addon install pgbouncer -i mypg-replica \
  --pool-mode transaction \
  --max-db-connections 30 \
  --query-timeout 30
```

## 监控

PgBouncer 提供管理控制台用于监控连接池和运行状态。

### 连接管理控制台

使用管理员用户连接到 `pgbouncer` 虚拟数据库：

```bash
psql "postgres://<管理员用户>:<密码>@127.0.0.1:<pgbouncer端口>/pgbouncer"
```

示例：
```bash
psql "postgres://admin:secret@127.0.0.1:6432/pgbouncer"
```

**注意：** 只有在 `admin_users` 中列出的用户才能访问管理控制台。

### 常用 SHOW 命令

| 命令 | 说明 |
|------|------|
| `SHOW pools` | 连接池状态（活跃/等待的客户端和服务端连接数） |
| `SHOW clients` | 所有当前客户端连接详情 |
| `SHOW servers` | 所有当前服务端（PostgreSQL）连接详情 |
| `SHOW databases` | 已配置的数据库及其连接参数 |
| `SHOW stats` | 流量统计（事务数、查询数、收发字节数） |
| `SHOW config` | 所有运行时的配置参数 |
| `SHOW sockets` | 底层 TCP 套接字信息 |
| `SHOW active_sockets` | 活跃的 TCP 套接字 |
| `SHOW mem` | 内存使用统计 |
| `SHOW lists` | 各类对象数量汇总 |

### 示例

```sql
-- 检查连接池状态
SHOW pools;

-- 查看活跃的客户端连接
SHOW clients;

-- 查看 PostgreSQL 后端连接
SHOW servers;

-- 查看当前配置
SHOW config;

-- 查看流量统计
SHOW stats;
```

### 其他管理命令

| 命令 | 说明 |
|------|------|
| `RELOAD` | 重新加载配置文件 |
| `PAUSE` | 暂停连接池（等待事务完成） |
| `RESUME` | 恢复连接池 |
| `RECONNECT` | 强制重新连接所有服务端连接 |
| `SHUTDOWN` | 关闭 PgBouncer |

## 故障排除

### 连接池满

```
ERROR: no more connections allowed
```

原因：达到 `max_client_conn` 限制。

解决方法：
```bash
# 增加最大连接数
pg addon install pgbouncer -i mypg --max-client-conn 500

# 或减少连接池大小
pg addon install pgbouncer -i mypg --default-pool-size 10
```

### 用户认证失败

```
FATAL: password authentication failed
```

原因：`userlist.txt` 中的密码与 PostgreSQL 不一致。

解决方法：
```bash
# 重新同步用户密码
pg addon install pgbouncer -i mypg
```

### 插件容器无法启动

```bash
# 查看容器日志
podman logs pgcli-addon-pgbouncer-mypg

# 检查配置文件
cat /data/addon/pgbouncer/pgbouncer.ini
```

常见原因：
- 配置文件语法错误
- 用户列表格式不正确
- 端口被占用

### 查询超时

```
ERROR: query timeout
```

原因：查询执行时间超过 `query_timeout`。

解决方法：
```bash
# 增加查询超时或禁用
pg addon install pgbouncer -i mypg --query-timeout 300
# 或
pg addon install pgbouncer -i mypg --query-timeout 0
```

## 注意事项

- **插件端口：** PgBouncer 默认使用 6432 端口，确保防火墙规则允许访问
- **用户密码：** 修改 PostgreSQL 用户密码后，需重新运行 `pg addon install` 同步
- **配置文件：** 手动编辑配置文件后，重启容器应用变更：
  ```bash
  podman restart pgcli-addon-pgbouncer-mypg
  ```
- **监控：** PgBouncer 提供管理控制台，可通过 `admin_users` 访问
- **性能：** 连接池会引入少量延迟（通常 < 1ms），但能显著提升并发能力
- **事务模式：** `transaction` 模式不支持会话级功能（如临时表），需使用 `session` 模式
