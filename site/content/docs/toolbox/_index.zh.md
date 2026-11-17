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
- [Daemon](./daemon/) — 在宿主机后台运行 webhook 接收端等长期命令
- [TimescaleDB](./timescaledb/) — 在 PostgreSQL 上使用时序 hypertable 和 time_bucket 分析
- [HTTP 文件服务器](./http/) — 通过 HTTP(S) 共享目录，提供 autoindex 列表和每文件复制按钮
- [随机字符串](./rand/) — 生成密码和 token 等安全随机字符串，支持自定义长度和字符集
- [压测](./bench/) — 在一次性容器中运行 pgbench，对本地或远程 PostgreSQL 做负载测试
{.cards}
