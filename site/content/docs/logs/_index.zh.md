---
title: "日志"
description: "查看 PostgreSQL 和插件控制台输出日志"
weight: 95
icon: fa-solid fa-file-lines
menus:
  main:
    identifier: docs-logs
    parent: docs
    weight: 95
    params:
      icon: fa-solid fa-file-lines
cascade:
  type: docs
  footer_style: slim
---

`pg logs` 命令用于查看 PostgreSQL 实例和插件组件（如 PgBouncer 连接池、etcd 成员）的控制台输出日志。

## PostgreSQL 实例日志

### 查看最近的日志

```bash
pg logs                              # 最后 50 行（默认实例）
pg logs -i proj01                    # 指定实例
pg logs -n 200                       # 最后 200 行
```

### 实时跟踪日志

```bash
pg logs -f                           # 跟踪模式（Ctrl+C 退出）
pg logs -i proj01 -f                 # 跟踪指定实例
pg logs -n 100 -f                    # 从最后 100 行开始，然后持续跟踪
```

### 显示所有可用日志

```bash
pg logs -n 0                         # 所有日志（无行数限制）
```

**默认行为**：不指定 `-i` 时，显示 `default` 实例的日志。

## 插件日志

插件日志（如 PgBouncer 连接池）使用 `addon` 子命令。

### 本地插件

查看附加到本地 PostgreSQL 实例的插件日志：

```bash
pg logs addon pgbouncer -i proj01    # proj01 的 PgBouncer 日志
pg logs addon pgbouncer -i proj01 -f # 跟踪 PgBouncer 日志
pg logs addon pgbouncer -i proj01 -n 100  # 最后 100 行
```

### 远程插件

查看针对远程数据库的独立 PgBouncer 实例的日志：

```bash
pg logs addon pgbouncer --pg-name my-pool
pg logs addon pgbouncer --pg-name my-pool -f
pg logs addon pgbouncer --pg-name my-pool -n 200
```

### etcd 成员

etcd 属于顶层 infra 插件，用 `--name` 指定成员（省略时默认为 `etcd`）：

```bash
pg logs addon etcd --name m1           # 成员 m1 的日志
pg logs addon etcd --name m1 -f        # 持续跟踪 m1
pg logs addon etcd -n 200              # 成员 "etcd"，最后 200 行
```

### PgDog 代理

pgdog 同样属于顶层 infra 插件，用 `--name` 指定代理（省略时默认为 `pgdog`）：

```bash
pg logs addon pgdog --name pgdog       # 代理日志
pg logs addon pgdog --name pgdog -f    # 持续跟踪
```

PgDog 以结构化 INFO 行记录连接池事件（新建服务器连接、认证、客户端连接 / 断开）。

### HAProxy 实例

haproxy 同样属于顶层 infra 插件，用 `--name` 指定实例（省略时默认为 `haproxy`）：

```bash
pg logs addon haproxy --name lb        # 实例日志
pg logs addon haproxy --name lb -f     # 持续跟踪
```

HAProxy 会记录健康检查状态变化（`Server lb_rw/node2 is UP/DOWN, reason: ...`）
与连接事件，适合实时观察故障切换。

## 选项说明

| 选项 | 简写 | 描述 |
|------|------|------|
| `--follow` | `-f` | 跟踪日志输出（类似 `tail -f`） |
| `--tail N` | `-n N` | 显示最后 N 行（默认：50，0 表示全部） |
| `--instance NAME` | `-i NAME` | 实例名称（默认：`default`） |
| `--pg-name NAME` | | 远程插件名称（用于远程 PgBouncer） |
| `--name NAME` | | etcd 成员 / PgDog 代理 / HAProxy 实例名称（默认：`etcd` / `pgdog` / `haproxy`） |

## 示例

```bash
# 检查最近的错误
pg logs -n 100 | grep ERROR

# 监控数据库活动
pg logs -i prod-db -f

# 调试 PgBouncer 连接问题
pg logs addon pgbouncer -i myapp -f

# 查看远程连接池日志
pg logs addon pgbouncer --pg-name analytics-pool -n 50

# 观察 etcd 成员的选主 / peer 事件
pg logs addon etcd --name m1 -f

# 观察 PgDog 连接池活动
pg logs addon pgdog --name pgdog -f

# 观察 HAProxy 健康检查 / 故障切换状态变化
pg logs addon haproxy --name lb -f
```

## 注意事项

- PostgreSQL 日志包括查询执行、连接事件和系统消息
- 插件日志显示连接池活动（连接、断开、池统计）
- etcd 成员日志为 JSON 格式的 raft/选主/peer 事件，适合排查 quorum 问题
- PgDog 代理日志为结构化 INFO 行，记录连接池事件（服务器连接、认证、客户端连接 / 断开）
- HAProxy 日志记录健康检查状态变化与连接事件——可实时观察故障切换过程
- 跟踪模式（`-f`）会保持连接直到按 Ctrl+C 中断
- 远程插件使用 `--pg-name` 而不是 `-i` 来标识目标连接池；etcd 成员、PgDog 代理和 HAProxy 实例使用 `--name`
