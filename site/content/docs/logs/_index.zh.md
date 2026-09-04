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

`pg logs` 命令用于查看 PostgreSQL 实例和插件组件（如 PgBouncer 连接池）的控制台输出日志。

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

## 选项说明

| 选项 | 简写 | 描述 |
|------|------|------|
| `--follow` | `-f` | 跟踪日志输出（类似 `tail -f`） |
| `--tail N` | `-n N` | 显示最后 N 行（默认：50，0 表示全部） |
| `--instance NAME` | `-i NAME` | 实例名称（默认：`default`） |
| `--pg-name NAME` | | 远程插件名称（用于远程 PgBouncer） |

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
```

## 注意事项

- PostgreSQL 日志包括查询执行、连接事件和系统消息
- 插件日志显示连接池活动（连接、断开、池统计）
- 跟踪模式（`-f`）会保持连接直到按 Ctrl+C 中断
- 远程插件使用 `--pg-name` 而不是 `-i` 来标识目标连接池
