---
title: "Logs"
description: "View PostgreSQL and addon console output logs"
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

The `pg logs` command displays console output logs from PostgreSQL instances and addon components (such as PgBouncer connection poolers and etcd members).

## PostgreSQL Instance Logs

### View recent logs

```bash
pg logs                              # Last 50 lines (default instance)
pg logs -i proj01                    # Specific instance
pg logs -n 200                       # Last 200 lines
```

### Follow logs in real-time

```bash
pg logs -f                           # Follow mode (Ctrl+C to exit)
pg logs -i proj01 -f                 # Follow specific instance
pg logs -n 100 -f                    # Start from last 100 lines, then follow
```

### Show all available logs

```bash
pg logs -n 0                         # All logs (no line limit)
```

**Default behavior**: Without `-i`, logs show the `default` instance.

## Addon Logs

Addon logs (e.g., PgBouncer connection poolers) use the `addon` subcommand.

### Local addons

View logs for addons attached to a local PostgreSQL instance:

```bash
pg logs addon pgbouncer -i proj01    # PgBouncer logs for proj01
pg logs addon pgbouncer -i proj01 -f # Follow PgBouncer logs
pg logs addon pgbouncer -i proj01 -n 100  # Last 100 lines
```

### Remote addons

View logs for standalone PgBouncer instances targeting remote databases:

```bash
pg logs addon pgbouncer --pg-name my-pool
pg logs addon pgbouncer --pg-name my-pool -f
pg logs addon pgbouncer --pg-name my-pool -n 200
```

### etcd members

etcd is a top-level infra addon — select the member with `--name` (defaults
to `etcd`):

```bash
pg logs addon etcd --name m1           # Member m1 logs
pg logs addon etcd --name m1 -f        # Follow m1
pg logs addon etcd -n 200              # Member "etcd", last 200 lines
```

## Options

| Option | Short | Description |
|--------|-------|-------------|
| `--follow` | `-f` | Follow log output (like `tail -f`) |
| `--tail N` | `-n N` | Show last N lines (default: 50, use 0 for all) |
| `--instance NAME` | `-i NAME` | Instance name (default: `default`) |
| `--pg-name NAME` | | Remote addon name (for remote PgBouncer) |
| `--name NAME` | | etcd member name (default: `etcd`) |

## Examples

```bash
# Check recent errors
pg logs -n 100 | grep ERROR

# Monitor database activity
pg logs -i prod-db -f

# Debug PgBouncer connection issues
pg logs addon pgbouncer -i myapp -f

# View remote pooler logs
pg logs addon pgbouncer --pg-name analytics-pool -n 50

# Watch an etcd member for leader elections / peer issues
pg logs addon etcd --name m1 -f
```

## Notes

- PostgreSQL logs include query execution, connection events, and system messages
- Addon logs show connection pooler activity (connections, disconnections, pool stats)
- etcd member logs are JSON-formatted raft/leader/peer events — useful for debugging quorum issues
- Follow mode (`-f`) keeps the connection open until interrupted with Ctrl+C
- Remote addons use `--pg-name` instead of `-i` to identify the target pooler; etcd members use `--name`
