# pgcli

**Easy-to-use PostgreSQL instance manager**

[![Release](https://img.shields.io/github/v/release/mars-base/pgcli)](https://github.com/mars-base/pgcli/releases)
[![Docs](https://img.shields.io/badge/docs-pgcli.pages.dev-blue)](https://pgcli.pages.dev)
[![License](https://img.shields.io/github/license/mars-base/pgcli)](https://github.com/mars-base/pgcli/blob/main/LICENSE)
[![Platform](https://img.shields.io/badge/platform-linux%20|%20macOS-blue)]()

Create, manage, and backup PostgreSQL databases with ease. pgcli provides a simple CLI to launch isolated PostgreSQL instances in containers — one command to spin up a database, automatic backups, and point-in-time recovery when you need it.

Whether you're running multiple dev databases, managing staging environments, or need reliable backup strategies, pgcli handles the complexity so you can focus on your application.

**Key capabilities:**
- One-command to manage multiple database instances
- Advanced features such as: replica/backup/restore/extensions/psql/shell/import-export/clone/dsn-connection
- Cross-platform support (Linux and macOS)

## Documentation

Full documentation site: **[https://pgcli.pages.dev](https://pgcli.pages.dev)** (EN / 中文)

| Document | Description |
|----------|-------------|
| [Quick Start](docs/quickstart.md) | Install, init config, start/stop instances, multi-instance, multi-config isolation (namespace + port ranges), interactive psql session, container shell, remote connection via `--dsn` |
| [exec and psql](docs/exec-psql.md) | One-shot SQL vs interactive sessions, container command mode, stdin scripts, psql passthrough args, remote `--dsn` mode and its rules |
| [Data Import/Export](docs/import-export.md) | Export/import in custom or SQL format, gzip compression, specific database, stream piping between instances, across hosts via SSH, and to remote databases via `--dsn` |
| [Clone](docs/clone.md) | Copy an instance into a new one via logical dump pipe, remote `--dsn` source, pre-flight connectivity check, live progress |
| [Backup and Restore](docs/backup-restore.md) | Full/differential snapshots, snapshot list/delete, point-in-time recovery (PITR) with read-only inspection before promoting |
| [Replica](docs/replica.md) | Read-only physical standby of an instance: create/list, live WAL streaming, lag, slot lifecycle |
| [Failover](docs/failover.md) | 3-step manual failover: promote replica to primary, re-point other replicas, demote old primary |
| [Extensions](docs/extensions.md) | Install PostgreSQL extensions from Pigsty DEB repo, baked into container image, shared_preload_libraries management |
| [Addons](https://pgcli.pages.dev/docs/addon/) | Standalone companions via `pg addon`: PgBouncer (pooling), etcd (DCS for HA), PgDog (pooling/load-balancing/sharding), PostgREST (schema-as-REST-API), HAProxy (TCP load balancer for Patroni), MinIO / silo / rustfs (S3 object storage, standalone or distributed), Redis (key-value store for cache/session/ranking, majors 7/8, native cluster via `--cluster`), Predixy (Redis cluster proxy — one plain `redis://` endpoint, Linux only), InfluxDB (1.x time-series database with InfluxQL — auth always on, online backup/restore, offline export/import, plus top-level `pg influx` CLI passthrough for local and remote servers, Linux/amd64 only), ClickHouse (single-node columnar OLAP database — pinned 26.8 LTS image, auth always on, generated admin password, SQL helpers for databases/users/grants, online BACKUP/RESTORE, dotted-XML config overrides, Linux only), Kafka (KRaft broker — no ZooKeeper, official `apache/kafka` image, single-node or a multi-host static controller quorum via `--cluster`/`--node-id`/`--peer`, rendered `server.properties`, topic create/list/describe/delete/retention and consumer-group list/describe with RF=`min(3, voters)`, PLAINTEXT by default plus an optional TLS listener via `--tls` with no client authentication, Linux only), RabbitMQ (message broker — official `library/rabbitmq` management image with AMQP and the management web UI/API, generated admin password reconciled on every start, optional additive TLS via `--tls` or `--tls-cert`/`--tls-key`, queue management: list/declare/info, publish/get, bind/unbind/bindings, purge/delete, with message TTL (`--message-ttl`), idle queue expiry (`--expires`) and auto-delete queues; single node, or a multi-host cluster formed by etcd peer discovery with `--cluster`/`--node-name`/`--etcd-endpoints` and shared secrets from `pg addon rabbitmq secret generate` + `--secrets-from-etcd` (or `--erlang-cookie-file`/`--admin-password-file`), quorum queues by default in cluster mode, `pg addon rabbitmq cluster status|forget`, Linux only), RocketMQ (Apache RocketMQ 5.x messaging — three containers per instance: NameServer + broker + proxy; standalone starts one async master with no replicas, or a multi-host controller-mode cluster uses an embedded DLedger controller in every NameServer and fails brokers over automatically via `--cluster`/`--controller-id`/`--advertise-host`/`--peer ID@HOST` with fixed ports 9876/9877/10911/10912/8080/8081; topic create/list/describe/delete, send with key/tags and cluster status; rendered `namesrv.conf`/`broker.conf`/`rmq-proxy.json`; no auth, no TLS, no web UI; Linux only), JuiceFS / ZeroFS (S3-backed POSIX filesystems — host-binary addons, mount the bucket as a local directory, Linux only) |
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
- **Failover** — 3-step manual failover: promote replica to primary, re-point remaining replicas, demote old primary back to replica. Same-host and cross-host supported
- **Multi-Instance Support** — Run multiple isolated PostgreSQL instances on different ports with independent configurations
- **Multi-Config Isolation** — Multiple config files on one host with namespaced containers and disjoint port ranges (use pg config init -h for details)
- **Data Import/Export** — Export/import in custom or SQL format with gzip compression, stream piping between instances, and cross-host support via SSH or `--dsn`
- **Instance Cloning** — Copy an instance into a new one via logical dump pipe with live progress and pre-flight connectivity checks
- **Interactive Shell** — Open psql or bash sessions directly in containers, or connect to remote databases via `--dsn`
- **Addons** — Standalone companions managed via `pg addon install/start/stop/remove`: PgBouncer (connection pooling), etcd (DCS for HA), PgDog (pooling/load-balancing/sharding), PostgREST (schema-as-REST-API), HAProxy (TCP load balancer for Patroni clusters), MinIO / silo / rustfs (S3-compatible object storage, standalone or distributed erasure-coded), Redis (key-value store for cache/session/ranking/counter data, majors 7/8 selectable, native cluster sharding via `--cluster`), Predixy (Redis protocol proxy — fronts a native cluster as one plain `redis://` endpoint for cluster-unaware clients, Linux only), InfluxDB (1.x time-series database with InfluxQL — authentication always on, online portable backup/restore, offline export/import, and a top-level `pg influx` CLI passthrough for local addon or remote servers via `--host`/`--dsn`, Linux/amd64 only), ClickHouse (single-node columnar OLAP database from the official image — pinned 26.8 LTS, authentication always on with a generated admin password, HTTP + native ports from separate pools, `database`/`user`/`grant` helpers, online `BACKUP`/`RESTORE` to a managed disk, persistent dotted-XML config overrides, Linux only), Kafka (KRaft broker — KRaft-only, no ZooKeeper; official `docker.io/apache/kafka` image with a pinned/derived cluster id; a single node or one node of a multi-host static controller quorum installed with `--cluster`/`--node-id`/`--peer` on every host; broker/controller/TLS ports from their own pools; topic `create`/`list`/`describe`/`delete` with RF=`min(3, voters)`; persistent `server.properties` overrides; PLAINTEXT by default plus an optional TLS listener (`--tls`) that encrypts traffic but does not authenticate clients; Linux only), RabbitMQ (message broker from the official `docker.io/library/rabbitmq` management image — AMQP plus the management web UI/API, a generated admin password reconciled with `rabbitmqctl` on every start, optional additive TLS (`--tls`, or bring your own `--tls-cert`/`--tls-key`; encryption only, no client authentication), and queue management (`list`/`declare`/`info`, `publish`/`get`, `bind`/`unbind`/`bindings`, `purge`/`delete`) including message TTL (`--message-ttl`), idle queue expiry (`--expires`) and auto-delete queues; a single node, or one member of a multi-host cluster formed by etcd peer discovery with `--cluster`/`--node-name`/`--etcd-endpoints` (or `--etcd` for local members) and shared secrets from `pg addon rabbitmq secret generate` + `--secrets-from-etcd` (or `--erlang-cookie-file`/`--admin-password-file`); clustered `declare` defaults to quorum queues and `pg addon rabbitmq cluster status|forget NODE` inspects/retires members; no Prometheus plugin; Linux only), RocketMQ (Apache RocketMQ 5.x messaging from the official `docker.io/apache/rocketmq` image — three containers per instance: a NameServer, a broker and a proxy; standalone runs one async master with no replicas, while a multi-host cluster embeds a DLedger controller in every NameServer and runs controller-mode brokers that fail over automatically, installed one member per host with `--cluster`/`--controller-id`/`--advertise-host`/`--peer ID@HOST` and fixed ports 9876/9877/10911/10912/8080/8081; topic `create`/`list`/`describe`/`delete`, `send` with key/tags and `cluster status`; rendered `namesrv.conf`/`broker.conf`/`rmq-proxy.json` with persistent `--config-option` overrides; no authentication, no TLS and no web UI; controller/broker stores are stateful and cluster identity is immutable after install; Linux only), JuiceFS / ZeroFS (S3-backed POSIX filesystems — the first **host-binary** addons: the binary is downloaded + sha256-verified and mounted on the host, no container; Linux only)
- **ZFS Storage** — Install ZFS on Debian/Ubuntu/Fedora/Rocky and manage pools, datasets and snapshots with `pg zfs`: automatic stripe/mirror/raidz layouts, multi-vdev pools, SLOG/special/cache/spare devices and strict whole-disk safety checks
- **SQLite + Litestream** — Manage host-local SQLite databases with `pg sqlite` (WAL, shell/exec, info/integrity) and replicate WAL changes continuously to S3-compatible storage via Litestream 0.5, including point-in-time restore by timestamp or transaction id; Linux only, S3 credentials stay in a 0600 config file rather than `pg.yaml`
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

## License

Apache 2.0
