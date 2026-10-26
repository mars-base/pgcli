---
title: "Toolbox"
description: "Standalone tools that extend pgcli beyond PostgreSQL management"
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

Tools and guides that extend pgcli beyond core instance management.

- [ZFS Storage](./zfs/) — Install ZFS and manage pools, datasets and snapshots
- [PGMQ Queues](./pgmq/) — Build durable message queues inside PostgreSQL
- [SQLite + Litestream](./sqlite/) — Manage host-local SQLite databases with continuous S3 backups
{.cards}
