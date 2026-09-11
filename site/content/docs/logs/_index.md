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

### PgDog proxies

PgDog is also a top-level infra addon — select the proxy with `--name`
(defaults to `pgdog`):

```bash
pg logs addon pgdog --name pgdog       # Proxy logs
pg logs addon pgdog --name pgdog -f    # Follow
```

PgDog logs connection-pool events (new server connections, auth, client
connect/disconnect) as structured INFO lines.

### HAProxy instances

HAProxy is also a top-level infra addon — select the instance with `--name`
(defaults to `haproxy`):

```bash
pg logs addon haproxy --name lb        # Instance logs
pg logs addon haproxy --name lb -f     # Follow
```

HAProxy logs health-check transitions (`Server lb_rw/node2 is UP/DOWN, reason:
...`) and connection events — useful for watching failovers live.

### MinIO instances

MinIO is also a top-level infra addon — select the instance with `--name`
(defaults to `minio`):

```bash
pg logs addon minio --name store       # Instance logs
pg logs addon minio --name store -f    # Follow
```

MinIO logs to stdout: startup lines (the `API:` / `Console:` addresses it is
serving) and request errors.

## Options

| Option | Short | Description |
|--------|-------|-------------|
| `--follow` | `-f` | Follow log output (like `tail -f`) |
| `--tail N` | `-n N` | Show last N lines (default: 50, use 0 for all) |
| `--instance NAME` | `-i NAME` | Instance name (default: `default`) |
| `--pg-name NAME` | | Remote addon name (for remote PgBouncer) |
| `--name NAME` | | etcd member / PgDog proxy / HAProxy / MinIO instance name (default: `etcd` / `pgdog` / `haproxy` / `minio`) |

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

# Watch PgDog pool activity
pg logs addon pgdog --name pgdog -f

# Watch HAProxy health-check / failover transitions
pg logs addon haproxy --name lb -f

# Watch MinIO request errors
pg logs addon minio --name store -f
```

## Notes

- PostgreSQL logs include query execution, connection events, and system messages
- Addon logs show connection pooler activity (connections, disconnections, pool stats)
- etcd member logs are JSON-formatted raft/leader/peer events — useful for debugging quorum issues
- PgDog proxy logs are structured INFO lines for connection-pool events (server connections, auth, client connect/disconnect)
- HAProxy logs health-check transitions and connection events — watch a failover happen in real time
- MinIO logs startup lines and request errors to stdout
- Follow mode (`-f`) keeps the connection open until interrupted with Ctrl+C
- Remote addons use `--pg-name` instead of `-i` to identify the target pooler; etcd members, PgDog proxies, HAProxy instances, and MinIO instances use `--name`
