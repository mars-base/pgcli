# pgcli — PostgreSQL Database Instance Manager

A CLI tool for managing containerized PostgreSQL database instances using Podman and pgBackRest.

## About

Create, manage, and backup PostgreSQL databases with ease. pgcli provides a simple CLI to launch isolated PostgreSQL instances in containers — one command to spin up a database, automatic backups via pgBackRest, and point-in-time recovery when you need it.

Whether you're running multiple dev databases, managing staging environments, or need reliable backup strategies, pgcli handles the complexity so you can focus on your application.

**Key capabilities:**
- One-command instance creation with automatic port assignment
- Automated backup and PITR setup (no manual pgBackRest configuration)
- Multi-instance isolation with separate data directories
- Cross-platform support (Linux and macOS)

## Features

- **Containerized PostgreSQL** — Each instance runs in an isolated Podman container
- **PITR (Point-In-Time Recovery)** — Full backup and time-travel recovery via pgBackRest
- **Snapshot Management** — Create, list, and delete database snapshots
- **Multi-Instance Support** — Run multiple isolated PostgreSQL instances on different ports
- **Linux + macOS** — Native Podman on Linux, podman machine on macOS

## Quick Start

```bash
# Install
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash

# Initialize config (creates ~/.pgcli/pg.yaml)
pg config init --add default --base-dir /data/pg

# Start the instance
pg start

# Check status (shows port, connection info)
pg status

# Connect via psql (password shown during create/start)
psql postgres://admin:<password>@localhost:<port>/<instance>_db

# Or execute SQL directly (auto-connects with instance user/database)
pg exec "SELECT version()"
pg exec -i myinst "SELECT count(*) FROM users"

# Or run arbitrary commands inside the container
pg exec -- bash -c "cat /var/lib/postgresql/data/postgresql.conf"

# Stop
pg stop
```

### Multi-Instance

```bash
# Create additional instances
pg create -i proj01 --base-dir /data/pg
pg create -i proj02 --base-dir /data/pg

# Start all instances
pg start --all

# Or start individually
pg start -i proj01

# List all instances
pg list
```

## PostgreSQL Accounts

Each pgcli instance automatically creates two database roles:

### admin (Primary User)
- **Username**: `admin` (configurable in config)
- **Password**: Auto-generated 16-character random password (shown during `pg create` or `pg start`)
- **Database**: `<instance>_db` (e.g., `proj01_db`, `default_db`)
- **Permissions**: Superuser with full database access
- **Usage**: Application connections, development, and general database operations

Connect using the connection string shown in `pg status` output:
```bash
psql postgres://admin:<password>@localhost:35432/proj01_db
```

### postgres (System Role)
- **Username**: `postgres`
- **Password**: No password (peer authentication only)
- **Permissions**: Superuser
- **Purpose**: pgBackRest backup system requires this role for SSH-based backup connections
- **Access**: Only accessible from within the container via peer authentication

**Note**: The `postgres` role is created automatically and should not be used for application connections. Use the `admin` user for all database operations.

## Commands

| Command | Description |
|---------|-------------|
| `pg backup` | Manage the shared pgbackrest backup container |
| `pg config` | Configuration management |
| `pg create` | Create a new database instance |
| `pg destroy` | Destroy an instance and remove its configuration |
| `pg exec` | Execute SQL or a command inside the PostgreSQL container |
| `pg list` | List all configured instances |
| `pg restore` | PITR point-in-time recovery |
| `pg snapshot` | Snapshot management (create/list/delete) |
| `pg start` | Start PostgreSQL + pgBackRest services |
| `pg status` | Show pgcli running status and health check |
| `pg stop` | Stop pg services |

## Backup and Restore

### Snapshot (Backup)

pgcli uses a shared pgBackRest backup container. On first `pg start`, the backup infrastructure is set up automatically.

```bash
# Create a full backup snapshot
pg snapshot create -i proj01

# Create a differential backup (changes since last full backup) (recommended)
pg snapshot create --type diff -i proj01

# Create an incremental backup (changes since last backup)
pg snapshot create --type incr -i proj01

# List all snapshots
pg snapshot list -i proj01

# Delete a specific snapshot
pg snapshot delete 20260826-073712F -i proj01
```

Snapshot types:
- **full** — Complete database backup (default, largest but self-contained)
- **incr** — Only changes since the last backup of any type
- **diff** — Changes since the last full backup

### Point-in-Time Recovery (PITR) (UTC Time)

Restore the database to any point in time after the earliest backup, using WAL replay.

```bash
# Dry run — show what would be done without executing
pg restore --time "2026-08-26 15:30:00+00" --dry-run

# Restore to a specific time (default: pause in read-only mode)
pg restore --time "2026-08-26 15:30:00+00"

# Inspect the restored state, then restore again to a different time if needed
pg restore --time "2026-08-26 15:25:00+00"

# Once satisfied, promote to read-write (switches timeline)
pg restore --time "2026-08-26 15:30:00+00" --promote

# Skip confirmation prompt
pg restore --time "2026-08-26 15:30:00+00" --promote --force
```

Supported time formats:
- `2026-08-26 15:30:00+08:00` — with timezone offset (colons)
- `2026-08-26 15:30:00+0800` — with timezone offset (no colons)
- `2026-08-26 15:30:00+08` — with timezone hour only
- `2026-08-26 15:30:00Z` — UTC (Zulu)
- `2026-08-26 15:30:00` — without timezone (assumed as UTC)

Recovery workflow:
1. **Stop** — PostgreSQL container is stopped
2. **Restore** — pgBackRest restores base backup and WAL to the target time
3. **Start** — PostgreSQL restarts and replays WAL up to the target

By default the instance is left paused in read-only mode so you can inspect the restored data before committing. Use `--promote` when you are satisfied to switch to a new timeline and resume read-write operations.

**Note**: After a promote, you should create a new full snapshot before attempting further PITR.

## License
MIT
