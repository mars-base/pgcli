# Quick Start

```bash
# Initialize config (creates ~/.pgcli/pg.yaml with default instance)
pg config init --add default --base-dir /data/pg

# Start instance
pg start

# Check status and connection info
pg status

# Connect via psql
psql postgres://admin:<password>@localhost:35432/admin_db

# Execute SQL directly
pg exec "SELECT version()"

# Stop
pg stop
```

## Multi-Instance

```bash
# Create additional instances
pg create -i proj01 --base-dir /data/pg
pg create -i proj02 --base-dir /data/pg

# List all instances
pg list

# Start all
pg start --all
```

## Multiple Config Files (Isolation)

For isolated testing environments or one config per project on the same host,
generate a separate config file per environment with a distinct `--namespace`
and **disjoint port ranges**. All three parameters are persisted in the config
file at init time (`namespace`, `pg_start_port`, `pg_ssh_port`).

```bash
# Environment "t1": containers prefixed pgcli-pg-t1-*, PG ports from 38000,
# SSH ports from 43000
pg config init -o ~/.pgcli-t1/pg.yaml --namespace t1 --pg-start-port 38000 --pg-ssh-port 43000 --add proj1

# Environment "t2": different namespace and disjoint ports, same host
pg config init -o ~/.pgcli-t2/pg.yaml --namespace t2 --pg-start-port 38100 --pg-ssh-port 43100 --add proj2

# Manage each environment with -c
pg -c ~/.pgcli-t1/pg.yaml start -i proj1
pg -c ~/.pgcli-t2/pg.yaml list
```

| Parameter | Default | Meaning |
|-----------|---------|---------|
| `--namespace` | `default` | Container name prefix: instance containers `pgcli-pg-<namespace>-<instance>`, shared backup container `pgcli-backup-<namespace>`. Pass `--namespace ""` to keep legacy names without a prefix |
| `--pg-start-port` | `35432` | First PG host port; instances get sequential ports from here |
| `--pg-ssh-port` | `42201` | First SSH host port (pgBackRest transport); sequential from here |

Planning tips:

- **Always pass an explicit `--namespace`.** The default is `default`, so two configs initialized without it would share the same container prefix and clash.
- **Port ranges must not overlap** between configs on one host. Each instance consumes one PG + one SSH port, so leave headroom for the instances you plan to add — e.g. split ranges as 35000/42000, 38000/43000, 41000/44000.
- **The namespace is baked into container names at creation time.** Decide it once at init; changing `namespace:` later would not match the already-created containers and the instance would fail to start. To rename, `pg destroy` the instances and re-init instead.
- Instances from different configs share the same podman daemon and network (`pgcli-net`); isolation is by container name and port allocation only.

## Interactive psql Session

```bash
# Open interactive psql shell (default instance)
pg psql

# Open psql for specific instance
pg psql -i proj01

# Run SQL from stdin (non-interactive, for scripts)
echo "SELECT version();" | pg psql

# Execute a single SQL command
pg psql -- -c "SHOW work_mem"

# Connect to a different database
pg psql -- -d postgres

# Use psql meta-commands
pg psql -- -c "\dt"     # list tables
pg psql -- -c "\du"     # list users
pg psql -- -c "\l"      # list databases

# Connect to a remote database via connection string (--dsn)
pg psql --dsn postgres://user:pass@host:5432/db
pg exec --dsn postgres://user:pass@host:5432/db "SELECT version()"
```

Inside the interactive psql shell, you have full access to:
- SQL queries with history and tab completion
- psql meta-commands (`\dt`, `\du`, `\l`, etc.)
- `\q` to quit

## Container Shell

```bash
# Open bash shell in container (default instance)
pg shell

# Open shell for specific instance
pg shell -i proj01

# Run a command directly
pg shell -- -c "ls -la /var/lib/postgresql/data"

# Check PostgreSQL configuration
pg shell -- -c "cat /etc/postgresql/postgresql.conf"

# View logs
pg shell -- -c "tail -f /var/log/postgresql/postgresql-*.log"
```

The shell runs as `root` user inside the container, giving you full access to:
- PostgreSQL data directory (`/var/lib/postgresql/data`)
- Configuration files (`/etc/postgresql/`)
- Log files (`/var/log/postgresql/`)
- All system tools and utilities
