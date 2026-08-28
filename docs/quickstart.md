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
