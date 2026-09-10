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

pgcli 支持通过插件系统扩展 PostgreSQL 功能。插件是独立的容器，为 PostgreSQL
实例提供额外能力，无需修改数据库本身。

## 支持的插件

目前支持以下插件：

| 插件 | 说明 |
|------|------|
| [`pgbouncer`](./pgbouncer/) | 连接池管理器，提供事务级连接池化 |
| [`etcd`](./etcd/) | 分布式键值存储——独立运行、可组集群，用于 HA / DCS |
| [`pgdog`](./pgdog/) | Postgres 代理——连接池化、负载均衡与分片 |
| [`ha`](./ha/) | Patroni 高可用——自动故障切换。**不通过 `pg addon` 安装**：它是独立的顶层命令 `pg ha` |

每个插件都有独立页面，包含命令、参数与故障排除说明。

## 工作原理

插件作为独立容器运行，通过 `pg.yaml` 管理：

1. **`pg addon install`** 生成配置并启动插件容器
2. 配置与数据存放在 `<base-dir>/addon/<addon-name>/`
3. 插件容器通过主机网络通信
4. 配置更新时容器自动重启

**命名空间隔离：** 插件遵循配置的 `namespace` 设置 —— 容器名包含命名空间前缀，
因此不同配置文件可以管理互不冲突的独立插件。

## 常用命令

```bash
pg addon install <addon> [flags]   # 安装 / 重新配置（幂等）
pg addon list                       # 查看所有已安装插件及状态
pg addon remove <addon> [flags]     # 移除插件、容器与数据
```

各插件的具体参数与示例见其独立页面：
[Pgbouncer](./pgbouncer/) · [etcd](./etcd/) · [PgDog](./pgdog/) · [Patroni 高可用](./ha/) · [Patroni 动态配置](./ha-dynamic/)
