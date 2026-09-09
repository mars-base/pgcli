---
title: "PgBouncer"
description: "Connection pooling for PostgreSQL instances via the PgBouncer addon"
weight: 20
---

PgBouncer is a lightweight connection pooler for PostgreSQL. As a pgcli addon
it runs as a container in front of one or more PostgreSQL instances, reusing
server connections across many short-lived client sessions — transaction-level
pooling by default.

PgBouncer supports two deployment modes:

- **Local:** `pg addon install pgbouncer -i <instance>` — stored under
  `instances.<name>.addons`
- **Remote:** `pg addon install pgbouncer --dsn <dsn> --pg-name <name>` —
  stored in top-level `addons.pgbouncer`

## Platform support

PgBouncer works on **Linux** (host networking, including cross-host pools) and
on **macOS** for **single-host dev/test** (the container joins the `pgcli-net`
bridge and publishes its port, the same way a PG instance does under podman
machine). On macOS:

- **Local mode** just works: PgBouncer reaches the managed instance by its
  container name automatically — no address to configure.
- **Remote mode:** the `--dsn` host must be reachable **from the Mac** — do not
  point it at `127.0.0.1`, which is the Mac itself, not the podman machine VM.
- **Client connections** still target `127.0.0.1:<port>`; gvproxy forwards the
  published port to the Mac's loopback, exactly as for a PG instance.

## How It Works

1. **`pg addon install pgbouncer`** generates configuration files and starts the container
2. Config files live in `<base-dir>/addon/pgbouncer/<instance>/`
3. The container reaches PostgreSQL over the host network (Linux) or the
   `pgcli-net` bridge by container name (macOS)
4. Config file updates restart the container automatically

**Namespace isolation:** PgBouncer respects the config's `namespace` setting.
Container names and auth users include the namespace prefix (e.g.
`pgb_<namespace>_<instance>`), so different configs with different namespaces
can create independent poolers for the same PostgreSQL instance without
conflicting.

```bash
# Config with namespace "prod"
pg -c prod-pg.yaml addon install pgbouncer -i mypg
# → container: pgcli-pgbouncer-prod-mypg, auth user: pgb_prod_mypg

# Config with namespace "staging"
pg -c staging-pg.yaml addon install pgbouncer -i mypg
# → container: pgcli-pgbouncer-staging-mypg, auth user: pgb_staging_mypg
```

## Commands

### Install

```bash
# Local mode: pool a managed instance
pg addon install pgbouncer -i mypg

# Remote mode: pool a remote PG instance
pg addon install pgbouncer \
  --dsn "postgres://admin:pass@10.241.20.50:35432/mypg_db" \
  --pg-name my-remote-pool

# Tune pool parameters
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
| `--dsn` | PG instance connection string (remote mode) | — |
| `--pg-name` | Name to identify a remote PgBouncer (required with --dsn) | — |
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

### List

```bash
pg addon list
```

PgBouncer appears under **Local add-ons** / **Remote add-ons**:

```
Local add-ons:
  pgbouncer (instance: mypg)
    Status:    running
    Host:      127.0.0.1:56432
    Port:      56432
    Pool mode: transaction
    Container: pgcli-pgbouncer-default-mypg

Remote add-ons:
  pgbouncer (pg-name: my-remote-pool)
    Status:    running
    Host:      10.241.20.50:56433
    Port:      56433
    Pool mode: transaction
    Container: pgcli-pgbouncer-default-my-remote-pool
```

### Remove

```bash
# Remove local PgBouncer
pg addon remove pgbouncer -i mypg

# Remove remote PgBouncer
pg addon remove pgbouncer --pg-name my-remote-pool
```

Workflow:
1. Stop and remove the addon container
2. Delete the `<base-dir>/addon/pgbouncer/<instance>/` directory and files
3. Remove the addon entry from `pg.yaml`

## Configuration

**Local mode** (under `instances.<name>.addons`):
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

**Remote mode** (under top-level `addons`):
```yaml
addons:
  pgbouncer:
    my-remote-pool:
      container_name: pgcli-pgbouncer-default-my-remote-pool
      host_port: 56433
      pool_mode: transaction
      dsn: "postgres://admin:pass@10.241.20.50:35432/mypg_db"
      max_client_conn: 200
      default_pool_size: 30
```

Generated files, per instance, under `<base-dir>/addon/pgbouncer/`:

```
<base-dir>/addon/pgbouncer/
├── mypg/
│   ├── pgbouncer.ini    # PgBouncer main configuration
│   └── userlist.txt     # auth user credentials (auto-regenerated)
└── my-remote-pool/
    ├── pgbouncer.ini
    └── userlist.txt
```

## Authentication

PgBouncer uses the `auth_query` method for dynamic password lookup:

1. A per-pooler auth user is created on PostgreSQL with a random password, named `pgb_<namespace>_<instance>` (e.g. `pgb_default_mypg`, `pgb_test-ns_my-remote`)
2. A shared `SECURITY DEFINER` function `pgbouncer_lookup()` is installed to query `pg_authid`
3. When a client connects, PgBouncer uses its own auth user to run the auth_query and fetch the real user's password hash
4. The password is cached in PgBouncer's memory for subsequent connections

`userlist.txt` only contains the pooler's auth user (plaintext password). All other users are authenticated dynamically via auth_query — no password sync needed.

Each pooler gets its own PG auth user, so multiple poolers (local or cross-host) targeting the same PG instance do not conflict.

**After changing a PostgreSQL user's password**, re-run `pg addon install pgbouncer` to reset the auth cache, or connect to the admin console and run `RECONNECT`.

## Connecting

Clients connect through the addon port:

```bash
# Direct connection to PostgreSQL
pg exec -i mypg "SELECT version()"

# Connection through PgBouncer
pg exec --dsn "postgres://user:pass@127.0.0.1:56432/mypg_db" "SELECT version()"
```

**Port allocation:** PgBouncer defaults to port 56432; if occupied, pgcli
assigns the next free port. View the current port with `pg addon list`.

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

PgBouncer exposes an admin console for pool and runtime status.

### Connecting to the Admin Console

Connect to the `pgbouncer` virtual database as an admin user:

```bash
pg exec --dsn "postgres://<admin-user>:<password>@127.0.0.1:<pgbouncer-port>/pgbouncer" "SHOW pools"
```

Example:
```bash
pg exec --dsn "postgres://admin:secret@127.0.0.1:56432/pgbouncer" "SHOW pools"
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
pg exec --dsn "postgres://admin:secret@127.0.0.1:56432/pgbouncer" "SHOW pools"

# View active client connections
pg exec --dsn "postgres://admin:secret@127.0.0.1:56432/pgbouncer" "SHOW clients"

# View PostgreSQL backend connections
pg exec --dsn "postgres://admin:secret@127.0.0.1:56432/pgbouncer" "SHOW servers"

# Check current configuration
pg exec --dsn "postgres://admin:secret@127.0.0.1:56432/pgbouncer" "SHOW config"

# View traffic statistics
pg exec --dsn "postgres://admin:secret@127.0.0.1:56432/pgbouncer" "SHOW stats"
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

Cause: Auth cache contains a stale password hash.

```bash
# Re-run install to reset the auth cache
pg addon install pgbouncer -i mypg
```

### Container Won't Start

```bash
# View container logs
podman logs pgcli-pgbouncer-default-mypg

# Check the configuration file
cat <base-dir>/addon/pgbouncer/mypg/pgbouncer.ini
```

Common causes: config syntax error, malformed user list, or port already in use.

### Query Timeout

```
ERROR: query timeout
```

Cause: Query exceeded `query_timeout`.

```bash
# Increase the timeout, or disable it
pg addon install pgbouncer -i mypg --query-timeout 300
# or
pg addon install pgbouncer -i mypg --query-timeout 0
```

## Notes

- **Port:** PgBouncer defaults to 56432; ensure firewall rules allow access
- **Passwords:** After changing a PostgreSQL user password, re-run `pg addon install` to reset the auth cache
- **Config edits:** After editing config files manually, restart to apply:
  ```bash
  podman restart pgcli-pgbouncer-default-mypg
  ```
- **Transaction mode:** `transaction` mode does not support session-level features (e.g. temporary tables); use `session` mode instead
