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

# Create and start an instance
pg create default --data-dir ~/.pgcli/instances/default
pg start

# Check status
pg status

# Connect via psql
psql postgres://pgcli_user:pgcli_pass@localhost:25432/pgcli_db

# Stop
pg stop
```

## Commands

| Command | Description |
|---------|-------------|
| `pg create` | Create a new database instance |
| `pg start` | Start PostgreSQL + pgBackRest services |
| `pg stop` | Stop services |
| `pg status` | Show running status and health check |
| `pg list` | List all configured instances |
| `pg destroy` | Remove an instance |
| `pg exec` | Execute a command inside the container |
| `pg snapshot create/list/delete` | Manage snapshots |
| `pg restore` | PITR point-in-time recovery |
| `pg backup` | Manage the shared backup container |
| `pg config` | Configuration management |

## Configuration

Default config file: `~/.pgcli/pg.yaml`

```yaml
postgres:
  user: pgcli_user
  password: pgcli_pass
  database: pgcli_db

podman:
  image_tag: "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0"
  network: pgcli-net

pitr:
  enabled: true
  pgbackrest_stanza: pgcli

backup:
  container_name: pgcli-backup
  retention_full: 2
```

## Multi-Instance

```bash
# Create multiple instances on different ports
pg create dev --data-dir /data/pg-dev
pg create staging --data-dir /data/pg-staging

# Start all instances
pg start --all

# Or start individually
pg start -i dev
pg start -i staging
```

## Building from Source

```bash
git clone https://github.com/mars-base/pgcli.git
cd pgcli
make build
./bin/pg --help
```

## License

MIT
