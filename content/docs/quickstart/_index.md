---
title: Quick Start
description: Install pgcli and create your first PostgreSQL instance.
weight: 10
icon: fa-solid fa-rocket
menus:
  main:
    identifier: docs-quickstart
    parent: docs
    weight: 10
    params:
      icon: fa-solid fa-rocket
---

Get pgcli up and running in minutes.

## Installation

Install pgcli with a single command:

```bash
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash
```

This script will:
- Download the latest pgcli binary for your platform
- Install it to `/usr/local/bin` (or `~/.local/bin` if no sudo)
- Add pgcli to your PATH

## Initialize Configuration

```bash
# Initialize config with a default instance
pg config init --add default --base-dir /data/pg
```

This creates `~/.pgcli/pg.yaml` with sensible defaults including:
- A default instance named `default`
- Data directory at `/data/pg/default`
- Auto-assigned ports starting from 35432

## Start Instance

```bash
# Start the default instance
pg start

# Check status and connection info
pg status
```

The output shows the connection URL, admin password, and backup status.

## Connect to Your Database

```bash
# Using pgcli's built-in psql wrapper
pg psql

# Or connect directly with the connection string shown in pg status
psql postgres://admin:<password>@localhost:35432/admin_db

# Execute SQL directly
pg exec "SELECT version()"
```

## Basic Operations

```bash
# List all instances
pg list

# Stop an instance
pg stop

# Start an instance
pg start

# View instance status
pg status

# Execute SQL
pg exec "SELECT version();"
```

## Multi-Instance

```bash
# Create additional instances
pg create -i proj01 --base-dir /data/pg
pg create -i proj02 --base-dir /data/pg

# List all instances
pg list

# Start all instances
pg start --all
```

## Multiple Config Files (Isolation)

For isolated testing environments or one config per project on the same host,
generate a separate config file per environment with a distinct `--namespace`
and **disjoint port ranges**.

```bash
# Environment "t1": containers prefixed pgcli-pg-t1-*, PG ports from 38000
pg config init -o ~/.pgcli-t1/pg.yaml --namespace t1 --pg-start-port 38000 --pg-ssh-port 43000 --add proj1

# Environment "t2": different namespace and disjoint ports
pg config init -o ~/.pgcli-t2/pg.yaml --namespace t2 --pg-start-port 38100 --pg-ssh-port 43100 --add proj2

# Manage each environment with -c
pg -c ~/.pgcli-t1/pg.yaml start -i proj1
pg -c ~/.pgcli-t2/pg.yaml list
```

| Parameter | Default | Meaning |
|-----------|---------|---------|
| `--namespace` | `default` | Container name prefix: `pgcli-pg-<namespace>-<instance>` |
| `--pg-start-port` | `35432` | First PG host port; instances get sequential ports |
| `--pg-ssh-port` | `42201` | First SSH host port; sequential from here |

Planning tips:

- **Always pass an explicit `--namespace`** to avoid container name clashes.
- **Port ranges must not overlap** between configs on one host.
- **The namespace is baked into container names at creation time.** Changing it later requires `pg destroy` and re-init.

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

# Connect to a remote database via connection string
pg psql --dsn postgres://user:pass@host:5432/db
```

Inside the interactive psql shell you get:
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

# View logs
pg shell -- -c "tail -f /var/log/postgresql/postgresql-*.log"
```

The shell runs as `root` inside the container, giving full access to:
- PostgreSQL data directory (`/var/lib/postgresql/data`)
- Configuration files
- Log files (`/var/log/postgresql/`)
- All system tools and utilities

## Next Steps

- Learn about [backup](../backup/) and [restore](../restore/)
- Set up [replication](../replica/) for high availability
- Explore [extensions](../extensions/) management
- Understand [administration](../administration/)
