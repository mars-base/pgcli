# pgcli

**Easy-to-use PostgreSQL instance manager**

[![Release](https://img.shields.io/github/v/release/mars-base/pgcli)](https://github.com/mars-base/pgcli/releases)
[![License](https://img.shields.io/github/license/mars-base/pgcli)](https://github.com/mars-base/pgcli/blob/main/LICENSE)
[![Platform](https://img.shields.io/badge/platform-linux%20|%20macOS-blue)]()

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash
```

This installs `pg` to `~/.local/bin`.

## Quick Start

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

### Multi-Instance

```bash
# Create additional instances
pg create -i proj01 --base-dir /data/pg
pg create -i proj02 --base-dir /data/pg

# List all instances
pg list

# Start all
pg start --all
```

## Commands

| Command | Description |
|---------|-------------|
| `pg start` | Start instance + backup services |
| `pg stop` | Stop services |
| `pg status` | Show status and connection info |
| `pg list` | List all instances |
| `pg exec` | Execute SQL or shell commands |
| `pg snapshot` | Manage backups (create/list/delete) |
| `pg restore` | PITR point-in-time recovery |
| `pg create` | Create new instance |
| `pg destroy` | Destroy instance |
| `pg config` | Configuration management |

## Backup and Restore

### Snapshots

```bash
# Create snapshot (full backup)
pg snapshot create -i proj01

# Create differential backup (recommended)
pg snapshot create --type diff -i proj01

# List snapshots
pg snapshot list -i proj01

# Delete snapshot
pg snapshot delete 20260826-073712F -i proj01
```

**Snapshot types:**
- `full` — Complete backup (default, self-contained)
- `diff` — Changes since last full backup
- `incr` — Changes since last backup

### Point-in-Time Recovery (PITR)

Restore to any point in time after the first backup.

```bash
# Restore (read-only, inspect before committing)
pg restore --time "2026-08-26 15:30:00+00"

# Try different time if needed
pg restore --time "2026-08-26 15:25:00+00"

# Promote to read-write (switches timeline)
pg restore --time "2026-08-26 15:30:00+00" --promote

# Skip confirmation
pg restore --time "2026-08-26 15:30:00+00" --promote --force
```

**Time formats:**
- `2026-08-26 15:30:00+08:00` — with timezone offset
- `2026-08-26 15:30:00+08` — timezone hour only
- `2026-08-26 15:30:00Z` — UTC
- `2026-08-26 15:30:00` — assumed UTC

**Recovery workflow:** Stop → Restore → Start → WAL replay to target time

**Note:** After `--promote`, create a new full snapshot before further PITR.

## License

MIT
