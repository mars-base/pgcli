---
title: "工具箱"
description: "扩展 pgcli 能力的独立工具，无需 PostgreSQL 实例"
weight: 89
icon: fa-solid fa-toolbox
aliases: ["/toolbox/"]
menus:
  main:
    identifier: docs-toolbox
    parent: docs
    weight: 89
    params:
      icon: fa-solid fa-toolbox
cascade:
  type: docs
  footer_style: slim
---

在核心实例管理之外扩展 pgcli 能力的工具与使用指南。

- [ZFS 存储](./zfs/) — 安装 ZFS 并管理 pool、dataset 和快照
- [PGMQ 消息队列](./pgmq/) — 在 PostgreSQL 内部实现持久化消息队列
- [SQLite + Litestream](./sqlite/) — 管理宿主机 SQLite 数据库并持续备份到 S3
- [TimescaleDB](./timescaledb/) — 在 PostgreSQL 上使用时序 hypertable 和 time_bucket 分析
{.cards}
