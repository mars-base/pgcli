# pgcli

**Easy-to-use PostgreSQL instance manager**

[![Release](https://img.shields.io/github/v/release/mars-base/pgcli)](https://github.com/mars-base/pgcli/releases)
[![License](https://img.shields.io/github/license/mars-base/pgcli)](https://github.com/mars-base/pgcli/blob/main/LICENSE)
[![Platform](https://img.shields.io/badge/platform-linux%20|%20macOS-blue)]()

Create, manage, and backup PostgreSQL databases with ease. pgcli provides a simple CLI to launch isolated PostgreSQL instances in containers — one command to spin up a database, automatic backups, and point-in-time recovery when you need it.

Whether you're running multiple dev databases, managing staging environments, or need reliable backup strategies, pgcli handles the complexity so you can focus on your application.

**Key capabilities:**
- One-command to manage multiple database instances
- Advanced features such as: replica/backup/restore/extensions/psql/shell/import-export/clone/dsn-connection
- Cross-platform support (Linux and macOS)

## Documentation

| Document | Description |
|----------|-------------|
| [Quick Start](docs/quickstart.md) | Install, init config, start/stop instances, multi-instance, multi-config isolation (namespace + port ranges), interactive psql session, container shell, remote connection via `--dsn` |
| [exec and psql](docs/exec-psql.md) | One-shot SQL vs interactive sessions, container command mode, stdin scripts, psql passthrough args, remote `--dsn` mode and its rules |
| [Data Import/Export](docs/import-export.md) | Export/import in custom or SQL format, gzip compression, specific database, stream piping between instances, across hosts via SSH, and to remote databases via `--dsn` |
| [Clone](docs/clone.md) | Copy an instance into a new one via logical dump pipe, remote `--dsn` source, pre-flight connectivity check, live progress |
| [Backup and Restore](docs/backup-restore.md) | Full/differential snapshots, snapshot list/delete, point-in-time recovery (PITR) with read-only inspection before promoting |
| [Replica](docs/replica.md) | Read-only physical standby of an instance: create/list, live WAL streaming, lag, slot lifecycle |
| [Extensions](docs/extensions.md) | Install PostgreSQL extensions from Pigsty DEB repo, baked into container image, shared_preload_libraries management |
| [Administration](docs/administration.md) | Shell completion (bash/zsh/fish/PowerShell), PostgreSQL parameter tuning, instance destroy/rebuild |
| [Test Report](docs/full-test-step.md) | Core functionality test report: DSN piping, PITR verification, known issues, install script test |

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
- **Rocky Linux 10** - SELinux auto-disabled by installer
- Other distributions with podman 4.0+ may work

### SELinux (RHEL-family systems)

On RHEL/CentOS/Rocky/AlmaLinux with SELinux **Enforcing**, rootless podman-static cannot run containers. The installer automatically detects SELinux status and:

1. Disables it via `sudo setenforce 0` and persists the change in `/etc/selinux/config`
2. Errors out with clear instructions if sudo permissions are insufficient:

### Installation Privileges

**Sudo privileges are recommended** for the best experience:

| Privilege Level | Installation Path | Auto-setup |
|----------------|-------------------|------------|
| **With sudo** (recommended) | `/usr/local/bin` | ✅ Automatically installs dependencies, creates policy.json |
| Without sudo | `~/.local/bin` | ⚠️ Requires manual PATH setup, dependency installation |

The installer automatically detects available privileges and adapts accordingly.

## Features

- **Containerized PostgreSQL** — Each instance runs in an isolated Podman container with separate data directories
- **Extension Management** — Install, remove, and manage 440+ PostgreSQL extensions from the [Pigsty DEB repository](https://pigsty.cc/ext/)
  - Smart image ID-based change detection for container replacement (detects content changes even when tags stay the same)
  - Automatic extension configuration with restart confirmation prompts
  - Extensions baked into container images for persistence across rebuilds
- **PITR (Point-In-Time Recovery)** — Full backup and time-travel recovery via pgBackRest with differential/incremental snapshots
- **Snapshot Management** — Create, list, and delete database snapshots for quick rollback
- **Read-only Replicas** — Physical standby instances streaming WAL from a primary, supporting read/write split and failover scenarios
- **Multi-Instance Support** — Run multiple isolated PostgreSQL instances on different ports with independent configurations
- **Multi-Config Isolation** — Multiple config files on one host with namespaced containers and disjoint port ranges (use pg config init -h for details)
- **Data Import/Export** — Export/import in custom or SQL format with gzip compression, stream piping between instances, and cross-host support via SSH or `--dsn`
- **Instance Cloning** — Copy an instance into a new one via logical dump pipe with live progress and pre-flight connectivity checks
- **Interactive Shell** — Open psql or bash sessions directly in containers, or connect to remote databases via `--dsn`
- **Linux + macOS** — Native Podman on Linux, podman machine on macOS with automatic setup

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash
```

The installer auto-detects sudo privileges and installs to `/usr/local/bin` (with sudo) or `~/.local/bin` (without sudo). PATH is configured automatically.

## Quick Start

```bash
# Initialize config (creates ~/.pgcli/pg.yaml with default instance)
pg config init --add default --base-dir /data/pg

# Alternatively, Isolated environments on one host: distinct namespace + disjoint port ranges (use pg config init -h for details)
pg config init -o ~/.pgcli-t1/pg.yaml --namespace t1 --pg-start-port 38000 --pg-ssh-port 43000 --add proj1
pg -c ~/.pgcli-t1/pg.yaml start

# Start instance
pg start

# Check status and connection info
pg status

# Connect via psql
psql postgres://admin:<password>@localhost:35432/default_db

# Execute SQL directly
pg exec "SELECT version()"

# Stop
pg stop
```

See the full [Quick Start](docs/quickstart.md) guide for multi-instance setup, interactive psql sessions, and container shell usage.

## Commands

| Command | Description |
|---------|-------------|
| `pg config` | Configuration management |
| `pg create` | Create new instance |
| `pg clone` | Clone an instance into a new one via logical dump |
| `pg start` | Start instance + backup services |
| `pg stop` | Stop services |
| `pg status` | Show status and connection info |
| `pg list` | List all instances |
| `pg psql` | Open interactive psql session (also via `--dsn` for remote DBs) |
| `pg shell` | Open interactive bash shell in container |
| `pg exec` | Execute SQL or shell commands (also via `--dsn` for remote DBs) |
| `pg export` | Export database to dump file |
| `pg import` | Import database from dump file |
| `pg snapshot` | Manage backups (create/list/delete) |
| `pg restore` | PITR point-in-time recovery |
| `pg replica` | Create/list read-only physical replicas (standbys) of an instance |
| `pg destroy` | Destroy instance |
| `pg completion` | Generate shell completion scripts |

## License

Apache 2.0
