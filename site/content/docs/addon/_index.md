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

| Addon | Description | Container Image |
|-------|-------------|-----------------|
| `pgbouncer` | Connection pool manager with transaction-level pooling | `edoburu/pgbouncer:latest` |

## How It Works

Addons run as sidecar containers, coordinating with PostgreSQL instances through mounted configuration files:

1. **`pg addon install`** generates configuration files and starts the addon container
2. Configuration files are mounted to the `<base-dir>/addon/<addon-name>/` directory
3. Addon containers communicate with PostgreSQL instances via host network
4. Containers automatically restart when configuration files are updated

Benefits:
- Addons are decoupled from PostgreSQL instances and can be managed independently
- Configuration files are centrally stored in `<base-dir>/addon/` directory
- Parameters can be adjusted dynamically without rebuilding containers
- Passwords are dynamically resolved via auth_query — no static password sync needed

## Commands

### Install Addon

```bash
# Install PgBouncer connection pooler
pg addon install pgbouncer -i mypg

# Specify connection pool parameters
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

**Parameters:**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `--max-client-conn` | Maximum client connections | 100 |
| `--default-pool-size` | Default pool size | 20 |
| `--min-pool-size` | Minimum pool size (warmup) | 0 |
| `--reserve-pool-size` | Reserve pool size (burst) | 0 |
| `--max-db-connections` | Max connections per database | 50 |
| `--max-user-connections` | Max connections per user | 0 (unlimited) |
| `--server-idle-timeout` | Idle server connection timeout (seconds) | 600 |
| `--server-lifetime` | Max server connection lifetime (seconds) | 3600 |
| `--server-connect-timeout` | PostgreSQL connection timeout (seconds) | 15 |
| `--query-timeout` | Query timeout (seconds) | 0 (unlimited) |
| `--query-wait-timeout` | Wait for connection timeout (seconds) | 120 |
| `--idle-transaction-timeout` | Idle transaction timeout (seconds) | 0 |
| `--transaction-timeout` | Transaction timeout (seconds) | 0 |
| `--admin-users` | Admin users list | (empty) |
| `--stats-users` | Read-only stats users list | (empty) |
| `--log-connections` | Log connections | 0 |
| `--log-disconnections` | Log disconnections | 0 |

### List Installed Addons

```bash
pg addon list
```

Example output:
```
Add-ons:
  pgbouncer (container: pgcli-addon-pgbouncer-mypg)
    Status: running
    Port: 6432
    Pool mode: transaction
    Config: /data/addon/pgbouncer/pgbouncer.ini
```

### Remove Addon

```bash
# Remove PgBouncer
pg addon remove pgbouncer -i mypg
```

Workflow:
1. Stop and remove the addon container
2. Delete the `<base-dir>/addon/<addon-name>/` directory and configuration files
3. Remove addon configuration from `pg.yaml`

## Configuration

After installing an addon, `pg.yaml` is updated with addon configuration:

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

Configuration files are stored in `<base-dir>/addon/pgbouncer/`:

```
/data/addon/pgbouncer/
├── pgbouncer.ini    # PgBouncer main configuration
├── userlist.txt     # pgbouncer_auth credentials (auto-regenerated)
└── stats/           # Statistics directory (optional)
```

## Authentication

PgBouncer uses the `auth_query` method for authentication:

1. A `pgbouncer_auth` user is created on PostgreSQL with a random password
2. A `SECURITY DEFINER` function `pgbouncer_lookup()` is installed to query `pg_authid`
3. When a client connects, PgBouncer uses `pgbouncer_auth` to run the auth_query and fetch the real user's password hash
4. The password is cached in PgBouncer's memory for subsequent connections

The `userlist.txt` only contains `pgbouncer_auth` (plaintext password). All other users are authenticated dynamically via auth_query — no password sync needed.

**After changing a PostgreSQL user's password**, re-run `pg addon install pgbouncer` to reset the auth cache, or connect to the PgBouncer admin console and run `RECONNECT`.

## Connection Methods

After installing PgBouncer, clients connect through the addon port:

```bash
# Direct connection to PostgreSQL (port 5432)
pg exec -i mypg "SELECT version()"

# Connection through PgBouncer (port 6432)
pg exec --dsn "postgres://user:pass@127.0.0.1:6432/mypg" "SELECT version()"
```

**Port Allocation:** PgBouncer defaults to port 6432. If the port is occupied, pgcli automatically allocates the next available port.

View current port:
```bash
pg addon list
```

## Use Cases

### High Concurrency

```bash
pg addon install pgbouncer -i mypg \
  --max-client-conn 1000 \
  --default-pool-size 50 \
  --reserve-pool-size 20 \
  --max-db-connections 100
```

### Short-Lived Connections

```bash
pg addon install pgbouncer -i mypg \
  --pool-mode transaction \
  --server-idle-timeout 60 \
  --server-lifetime 600
```

### Long-Lived Connections

```bash
pg addon install pgbouncer -i mypg \
  --pool-mode session \
  --server-lifetime 86400
```

### Read Replicas

```bash
pg addon install pgbouncer -i mypg-replica \
  --pool-mode transaction \
  --max-db-connections 30 \
  --query-timeout 30
```

## Monitoring

PgBouncer provides an admin console for monitoring connection pools and runtime status.

### Connecting to Admin Console

Connect to the `pgbouncer` virtual database using an admin user:

```bash
pg exec --dsn "postgres://<admin-user>:<password>@127.0.0.1:<pgbouncer-port>/pgbouncer" "SHOW pools"
```

Example:
```bash
pg exec --dsn "postgres://admin:secret@127.0.0.1:6432/pgbouncer" "SHOW pools"
```

**Note:** Only users listed in `admin_users` can access the admin console.

### Common SHOW Commands

| Command | Description |
|---------|-------------|
| `SHOW pools` | Connection pool status (active/waiting client and server connections) |
| `SHOW clients` | All current client connections with details |
| `SHOW servers` | All current server (PostgreSQL) connections with details |
| `SHOW databases` | Configured databases and their connection parameters |
| `SHOW stats` | Traffic statistics (transactions, queries, bytes received/sent) |
| `SHOW config` | All runtime configuration parameters |
| `SHOW sockets` | Low-level TCP socket information |
| `SHOW active_sockets` | Active TCP sockets |
| `SHOW mem` | Memory usage statistics |
| `SHOW lists` | Summary of various object counts |

### Examples

```bash
# Check pool status
pg exec --dsn "postgres://admin:secret@127.0.0.1:6432/pgbouncer" "SHOW pools"

# View active client connections
pg exec --dsn "postgres://admin:secret@127.0.0.1:6432/pgbouncer" "SHOW clients"

# View PostgreSQL backend connections
pg exec --dsn "postgres://admin:secret@127.0.0.1:6432/pgbouncer" "SHOW servers"

# Check current configuration
pg exec --dsn "postgres://admin:secret@127.0.0.1:6432/pgbouncer" "SHOW config"

# View traffic statistics
pg exec --dsn "postgres://admin:secret@127.0.0.1:6432/pgbouncer" "SHOW stats"
```

### Other Admin Commands

| Command | Description |
|---------|-------------|
| `RELOAD` | Reload configuration file |
| `PAUSE` | Pause connection pool (wait for transactions to complete) |
| `RESUME` | Resume connection pool |
| `RECONNECT` | Force reconnect all server connections |
| `SHUTDOWN` | Shutdown PgBouncer |

## Troubleshooting

### Connection Pool Full

```
ERROR: no more connections allowed
```

Cause: Reached `max_client_conn` limit.

Solution:
```bash
# Increase maximum connections
pg addon install pgbouncer -i mypg --max-client-conn 500

# Or reduce pool size
pg addon install pgbouncer -i mypg --default-pool-size 10
```

### Authentication Failure

```
FATAL: password authentication failed
```

Cause: Passwords in `userlist.txt` are inconsistent with PostgreSQL.

Solution:
```bash
# Re-sync user passwords
pg addon install pgbouncer -i mypg
```

### Addon Container Won't Start

```bash
# View container logs
podman logs pgcli-addon-pgbouncer-mypg

# Check configuration file
cat /data/addon/pgbouncer/pgbouncer.ini
```

Common causes:
- Configuration file syntax error
- Incorrect user list format
- Port already in use

### Query Timeout

```
ERROR: query timeout
```

Cause: Query execution time exceeded `query_timeout`.

Solution:
```bash
# Increase query timeout or disable
pg addon install pgbouncer -i mypg --query-timeout 300
# Or
pg addon install pgbouncer -i mypg --query-timeout 0
```

## Notes

- **Addon Port:** PgBouncer defaults to port 6432; ensure firewall rules allow access
- **User Passwords:** After modifying PostgreSQL user passwords, re-run `pg addon install` to reset the auth cache
- **Configuration Files:** After manually editing configuration files, restart the container to apply changes:
  ```bash
  podman restart pgcli-addon-pgbouncer-mypg
  ```
- **Monitoring:** PgBouncer provides an admin console accessible via `admin_users`
- **Performance:** Connection pooling introduces minimal latency (typically < 1ms) but significantly improves concurrency
- **Transaction Mode:** `transaction` mode does not support session-level features (like temporary tables); use `session` mode instead
