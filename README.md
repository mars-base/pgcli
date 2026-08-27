# pgcli

**Easy-to-use PostgreSQL instance manager**

[![Release](https://img.shields.io/github/v/release/mars-base/pgcli)](https://github.com/mars-base/pgcli/releases)
[![License](https://img.shields.io/github/license/mars-base/pgcli)](https://github.com/mars-base/pgcli/blob/main/LICENSE)
[![Platform](https://img.shields.io/badge/platform-linux%20|%20macOS-blue)]()

Create, manage, and backup PostgreSQL databases with ease. pgcli provides a simple CLI to launch isolated PostgreSQL instances in containers — one command to spin up a database, automatic backups via pgBackRest, and point-in-time recovery when you need it.

Whether you're running multiple dev databases, managing staging environments, or need reliable backup strategies, pgcli handles the complexity so you can focus on your application.

**Key capabilities:**
- One-command instance creation with automatic port assignment
- Automated backup and PITR setup (no manual pgBackRest configuration)
- Multi-instance isolation with separate data directories
- Cross-platform support (Linux and macOS)

## System Requirements

### Supported Platforms

| Platform | Architecture | Status | Notes |
|----------|--------------|--------|-------|
| **Linux** | amd64, arm64 | ✅ **Recommended** | Native podman, best performance |
| macOS | amd64, arm64 | Supported | Requires podman machine (VM overhead) |
| Windows | - | ❌ Not supported | Use WSL2 with Linux |

### Linux Distributions

Tested and verified on:
- **Debian 12 (bookworm)** - LTS
- **Debian 13 (trixie)** - Current stable
- **Ubuntu 24.04 (noble)** - LTS
- **Ubuntu 26.04 (resolute)** - LTS
- **Fedora 44** - SELinux Enforcing mode supported
- RHEL/CentOS 8+
- Other distributions with podman 4.0+ should work

### Prerequisites

**Linux:**
- `podman` 4.0+ (rootless mode)
- `uidmap` package (for rootless container user mapping)
- `/etc/containers/policy.json` (container policy)
- Kernel user namespaces enabled (`/proc/sys/kernel/unprivileged_userns_clone`)

**macOS:**
- `podman` 4.0+ via Homebrew
- `podman machine` initialized and running

### Installation Privileges

**Sudo privileges are recommended** for the best experience:

| Privilege Level | Installation Path | Auto-setup |
|----------------|-------------------|------------|
| **With sudo** (recommended) | `/usr/local/bin` | ✅ Automatically installs dependencies, creates policy.json |
| Without sudo | `~/.local/bin` | ⚠️ Requires manual PATH setup, dependency installation |

The installer automatically detects available privileges and adapts accordingly.

## Features

- **Containerized PostgreSQL** — Each instance runs in an isolated Podman container
- **PITR (Point-In-Time Recovery)** — Full backup and time-travel recovery via pgBackRest
- **Snapshot Management** — Create, list, and delete database snapshots
- **Multi-Instance Support** — Run multiple isolated PostgreSQL instances on different ports
- **Linux + macOS** — Native Podman on Linux, podman machine on macOS

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash
```

The installer auto-detects sudo privileges and installs to `/usr/local/bin` (with sudo) or `~/.local/bin` (without sudo). PATH is configured automatically.

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

### Interactive psql Session

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
```

Inside the interactive psql shell, you have full access to:
- SQL queries with history and tab completion
- psql meta-commands (`\dt`, `\du`, `\l`, etc.)
- `\q` to quit

### Container Shell

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

### Data Import/Export

Export and import databases using `pg_dump` and `pg_restore`/`psql`. Supports custom format (recommended) and plain SQL, with automatic gzip compression.

```bash
# Export to custom format (recommended, fastest restore)
pg export -i proj01 -o backup.dump

# Export to SQL format (human-readable)
pg export -i proj01 -o backup.sql

# Export with gzip compression (auto-detected from .gz extension)
pg export -i proj01 -o backup.dump.gz
pg export -i proj01 -o backup.sql.gz

# Export specific database
pg export -i proj01 -d mydb -o backup.dump

# Export with custom compression level (0-9)
pg export -i proj01 -o backup.sql.gz --compress=9

# Import from custom format
pg import -i proj02 backup.dump

# Import from SQL format
pg import -i proj02 backup.sql

# Import compressed file (auto-detected from .gz extension)
pg import -i proj02 backup.dump.gz

# Import to specific database
pg import -i proj02 -d mydb backup.dump

# Import with cleanup (drop existing objects before restore)
pg import -i proj02 --clean backup.dump
```

**Format detection:** Uses magic bytes (content-based) with extension fallback.
- Files starting with `PGDMP` → custom format (uses `pg_restore`)
- `.sql` or `.sql.gz` → plain SQL format (uses `psql`)
- `.gz` extension or gzip magic bytes (`0x1f 0x8b`) → automatic decompression
- Extension is used as fallback if content detection fails

**Use cases:**
- Migrate data between instances: `pg export -i proj01 -o dump.dump && pg import -i proj02 dump.dump`
- Share database with team: `pg export -i proj01 -o dump.dump.gz` (compressed, smaller file)
- Backup before major changes: `pg export -i proj01 -o pre-migration.sql.gz`
- CI/CD pipelines: export test data, import into fresh test databases

## Commands

| Command | Description |
|---------|-------------|
| `pg start` | Start instance + backup services |
| `pg stop` | Stop services |
| `pg status` | Show status and connection info |
| `pg list` | List all instances |
| `pg psql` | Open interactive psql session |
| `pg shell` | Open interactive bash shell in container |
| `pg exec` | Execute SQL or shell commands |
| `pg export` | Export database to dump file |
| `pg import` | Import database from dump file |
| `pg snapshot` | Manage backups (create/list/delete) |
| `pg restore` | PITR point-in-time recovery |
| `pg create` | Create new instance |
| `pg destroy` | Destroy instance |
| `pg config` | Configuration management |
| `pg completion` | Generate shell completion scripts |

## Shell Completion

Enable tab completion for commands, flags, and instance names.

### Bash

```bash
# Linux
pg completion bash > /etc/bash_completion.d/pg

# macOS (with Homebrew bash-completion)
pg completion bash > $(brew --prefix)/etc/bash_completion.d/pg

# Or load in current session
source <(pg completion bash)
```

### Zsh

```bash
# Enable completion system (once)
echo "autoload -U compinit; compinit" >> ~/.zshrc

# Install completion
pg completion zsh > "${fpath[1]}/_pg"
```

### Fish

```bash
pg completion fish > ~/.config/fish/completions/pg.fish
```

### PowerShell

```powershell
pg completion powershell > pg.ps1
# Source from your PowerShell profile
```

## PostgreSQL Configuration

Modify PostgreSQL runtime parameters via `pg exec` with `ALTER SYSTEM`, then reload:

```bash
# Change a parameter
pg exec "ALTER SYSTEM SET work_mem = '256MB'"
pg exec "SELECT pg_reload_conf()"

# For a specific instance
pg exec -i proj01 "ALTER SYSTEM SET effective_cache_size = '4GB'"
pg exec -i proj01 "SELECT pg_reload_conf()"
```

**Note:** Some parameters (e.g. `shared_buffers`, `max_connections`) require a restart rather than reload. Use `pg stop && pg start` to apply those changes.

## Destroy and Rebuild

```bash
# Destroy instance (keeps data directory)
pg destroy -i proj01

# Destroy with data cleanup (fresh start)
pg destroy -i proj01 --clean-data

# Recreate and start
pg create -i proj01 --base-dir /data/pg
pg start -i proj01
```

**Important:** Without `--clean-data`, the data directory is preserved. When restarting the instance, PostgreSQL uses the existing data and `init.sh` (which creates users) does **not** run again. This can cause issues if the data was created with a different user or schema.

Use `--clean-data` when you need a completely fresh instance, or when changing the default user in configuration.

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
