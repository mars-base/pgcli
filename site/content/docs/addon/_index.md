---
title: "Addons"
description: "pgcli addon management guide"
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

pgcli supports extending PostgreSQL capabilities through an addon system. Addons are standalone containers that provide additional functionality for PostgreSQL instances without modifying the database itself.

## Supported Addons

The following addons are currently supported:

| Addon | Description |
|-------|-------------|
| [`pgbouncer`](./pgbouncer/) | Connection pool manager with transaction-level pooling |
| [`etcd`](./etcd/) | Distributed key-value store — standalone, clusterable, for HA / DCS use |
| [`pgdog`](./pgdog/) | Postgres proxy — connection pooling, load balancing and sharding |

Each addon has its own page with commands, parameters, and troubleshooting.

## How It Works

Addons run as standalone containers managed through `pg.yaml`:

1. **`pg addon install`** generates configuration and starts the addon container
2. Config and data live under `<base-dir>/addon/<addon-name>/`
3. Addon containers communicate over the host network
4. Containers automatically restart when configuration is updated

**Namespace isolation:** Addons respect the config's `namespace` setting — container names include the namespace prefix, so different config files can manage independent addons without conflicting.

## Common Commands

```bash
pg addon install <addon> [flags]   # install / reconfigure (idempotent)
pg addon list                       # show all installed addons and status
pg addon remove <addon> [flags]     # remove the addon, container, and data
```

See each addon's page for its specific flags and examples:
[Pgbouncer](./pgbouncer/) · [etcd](./etcd/) · [PgDog](./pgdog/)
