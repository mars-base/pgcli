---
title: "ClickHouse"
description: "Run a single-node ClickHouse columnar OLAP database as a managed addon container, with authentication, SQL helpers, users/grants, config overrides and BACKUP/RESTORE"
weight: 64
---

`pg addon install clickhouse` runs **ClickHouse 26.8 LTS** — a single-node columnar OLAP database — as a managed container from the official `docker.io/clickhouse/clickhouse-server` multi-arch image. Like VictoriaMetrics and InfluxDB, it is a **top-level addon**: it does not front a PostgreSQL instance.

ClickHouse speaks two protocols on two ports: **HTTP** (curl, JDBC/ODBC, many drivers) and the **native TCP protocol** (`clickhouse-client`). pgcli runs it with host networking, so both are plain host ports. **Authentication is always enabled** — the install creates an admin account and stores its password in `pg.yaml`.

> **The admin password is generated at install time and can be read back at any
> time with `pg addon password clickhouse [--name NAME]`.** The HTTP and native
> listeners require authentication on every request; there is no anonymous
> access. `--listen` defaults to `127.0.0.1` — pass `0.0.0.0` explicitly when
> other hosts must connect, and front it with [nginx](./nginx/) for TLS.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `clickhouse` | Instance name (map key; also the default subcommand name) |
| `--version` | `26.8.36.3` | Upstream image tag, pulled directly from `docker.io/clickhouse/clickhouse-server` |
| `--data-dir` | `<base_dir>/addon/clickhouse/<name>/data` | Data directory bind-mounted at `/var/lib/clickhouse`; must be an absolute path |
| `--listen` | `127.0.0.1` | Bind address for the HTTP and native listeners. `0.0.0.0` exposes both on the network |
| `--http-port` | auto (base `8123`) | Host HTTP port; `0` = auto-assign from `clickhouse_http_start_port` |
| `--tcp-port` | auto (base `9000`) | Host native-protocol port; `0` = auto-assign from `clickhouse_tcp_start_port` (shared with the S3 API pool) |
| `--admin-user` | `admin` | Admin account created by the image entrypoint |
| `--admin-password` | generated | Admin password. On `--force` the stored password is kept unless this is set |
| `--config-option K=V` | — | Extra server setting as a dotted XML path (`logger.level=debug`, `max_server_memory_usage=2147483648`); repeatable; replaces the stored override map |
| `--force` | false | Re-render the config and secret, then recreate the container; keeps data, the stored password and the stored overrides |

`--image` does not apply to ClickHouse — use `--version`.

The generated config only pins listeners, ports, logging and the backup disk; every other server setting keeps the upstream default. Overrides are stored in `pg.yaml` under `addons.clickhouse.<name>.settings` and rendered into the generated XML, so they survive restarts and container recreation.

## Install

```bash
pg addon install clickhouse --name ch1
```

```
✓ ClickHouse "ch1" installed
  Version:   26.8.36.3
  Image:     docker.io/clickhouse/clickhouse-server:26.8.36.3
  Container: pgcli-clickhouse-ch1
  HTTP:      http://127.0.0.1:8123
  TCP:       127.0.0.1:9000
  Admin:     admin
  Password:  <generated>
  Data dir:  /home/you/.pgcli/addon/clickhouse/ch1/data
  Backups:   /home/you/.pgcli/addon/clickhouse/ch1/backups
  Config:    /home/you/.pgcli/addon/clickhouse/ch1/config.d/zz-pgcli.xml
```

A second instance takes the next free HTTP/TCP ports from its pools (for example `8124`/`9001`) and can use a custom data dir:

```bash
pg addon install clickhouse --name analytics --data-dir /srv/clickhouse-data --listen 0.0.0.0
```

Re-running `install` without `--force` is a no-op that reports the current state. `--force` re-renders the config, recreates the container and **keeps the data dir, the stored admin password and the stored settings**; pass `--config-option` only when you want to replace the override map.

The image is pulled directly from Docker Hub; no pgcli-built image is involved. `--version` accepts any safe upstream tag, but the tag must exist in the registry — the pull fails otherwise. pgcli pins the current LTS line (`26.8.36.3`) by default.

## Ports

| Endpoint | Default | Protocol | Pool |
|---|---|---|---|
| HTTP | `8123` | HTTP (queries, `/ping`, many drivers) | `clickhouse_http_start_port`, default `8123` |
| Native | `9000` | ClickHouse native TCP (`clickhouse-client`) | `clickhouse_tcp_start_port`, default `9000`, **shared with the S3 API pool** |

Both pools share one "already assigned" set, so an explicit port in one pool cannot collide with an auto-assigned port in the other. `--http-port` and `--tcp-port` must differ, and every HTTP/TCP port must be unique across all ClickHouse instances. Ports already listening on the host are skipped during auto-assignment.

When `--listen` is a wildcard (`0.0.0.0`, `::`), pgcli's own client commands dial `127.0.0.1`; the printed endpoints follow the same rule. A LAN `--listen` bind also keeps loopback enabled inside the container so the in-container `clickhouse-client` always works.

## What pgcli creates

Everything for one instance lives under `<base_dir>/addon/clickhouse/<name>/`:

| Host path | Container path | Notes |
|---|---|---|
| `config.d/` | `/etc/clickhouse-server/config.d` | Read-only; replaces the image's stock `config.d` (which would otherwise bind every interface). `zz-pgcli.xml` is the generated override |
| `data/` | `/var/lib/clickhouse` | Database data and system tables |
| `logs/` | `/var/log/clickhouse-server` | `clickhouse-server.log` and `clickhouse-server.err.log` (also visible via `podman logs`) |
| `backups/` | `/var/lib/clickhouse-backups` | The managed `backups` disk used by `backup`/`restore` |
| `secrets/admin_password` | `/run/secrets/admin_password` | Mode `0600`, read by the entrypoint via `CLICKHOUSE_PASSWORD_FILE` |

The generated `zz-pgcli.xml` sets:

- `listen_host` (exactly what `--listen` chose) plus `listen_try`
- `http_port` / `tcp_port`
- `logger.level`, `logger.log`, `logger.errorlog`, `logger.console`, rotation (`size`, `count`)
- a local disk named `backups` at `/var/lib/clickhouse-backups`, allowed for `BACKUP`/`RESTORE`

The container runs with `--network host`, `--restart unless-stopped`, a 30-second stop timeout and `nofile=262144`. Data ownership inside the container is uid/gid 101 (the image drops privileges after its root entrypoint chowns the mounts).

## Credentials

```bash
pg addon password clickhouse --name ch1   # print the admin password
pg addon list                             # shows the Analytics add-ons section
```

The image entrypoint creates `admin` (or the name given with `--admin-user`) with full privileges and access management enabled (`CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1`), so `CREATE USER` / `GRANT` issued by this CLI work. The password is stored in `pg.yaml` (mode `0644` by default — protect that file) and duplicated into the `0600` secret file mounted into the container.

Every pgcli command that talks to ClickHouse passes credentials as environment variables expanded inside the container; the password never appears on the `podman` command line or the host process list. SQL is supplied on stdin.

## Connecting

Use the printed endpoints from any host tool:

```bash
# HTTP interface (Basic auth)
curl -sS 'http://127.0.0.1:8123/?query=SELECT%20version()' \
  --user "admin:$(pg addon password clickhouse --name ch1)"

# Native protocol
clickhouse-client --host 127.0.0.1 --port 9000 --user admin --password
```

`clickhouse-client` is not installed on the host by pgcli — use the addon's own `query`/`shell` commands below, or point an external client at the native port. `/ping` answers `Ok.` without authentication and is what pgcli polls during start.

## Shell and query

```bash
pg addon clickhouse shell --name ch1                  # bash inside the container
pg addon clickhouse query --name ch1 'SELECT version()'
pg addon clickhouse query --name ch1 --database demo 'SELECT count() FROM events'
pg addon clickhouse query --name ch1 --file schema.sql
echo "SELECT 1" | pg addon clickhouse query --name ch1 -
```

`query` runs `clickhouse-client` as the admin user and writes the result to stdout, so it can be piped or redirected. The statements are joined from the positional arguments; `--file` reads a file (mount-free: the file is read by the host and piped on stdin), and `-` reads from stdin. The client is invoked without `--multiquery`, so pass **one statement per call**.

The instance must be running for `query`, `database`, `user`, `grant`, `backup` and `restore`; `shell` opens regardless but prints a warning when the container is stopped.

```sql
CREATE TABLE demo.events
(
    id   UInt64,
    ts   DateTime,
    name String
)
ENGINE = MergeTree
ORDER BY id;
```

```bash
pg addon clickhouse query --name ch1 --database demo --file events.sql
echo "INSERT INTO demo.events VALUES (1, now(), 'boot')" | \
  pg addon clickhouse query --name ch1 --database demo -
pg addon clickhouse query --name ch1 --database demo 'SELECT * FROM demo.events ORDER BY id'
```

## Databases

```bash
pg addon clickhouse database create demo --name ch1
pg addon clickhouse database create logs --engine Atomic --comment 'application logs' --if-not-exists --name ch1
pg addon clickhouse database list --name ch1
pg addon clickhouse database drop demo --name ch1 --yes
```

`create` takes a bare engine name (default `Atomic`; engine parameters are not exposed). `drop` is destructive — it asks for confirmation unless `--yes` is given. `database list` shows `name`, `engine` and `data_path` from `system.databases`. `db` is an alias for `database`.

## Users and privileges

```bash
# User with an explicit password and default database
pg addon clickhouse user create app --password 'change-me' --default-database demo --name ch1

# Password omitted: generated and printed once
pg addon clickhouse user create reader --default-database demo --readonly --name ch1

# Restrict by source address / host name (repeatable; default is HOST ANY)
pg addon clickhouse user create etl --host-ip 10.0.0.0/8 --host-ip 192.168.1.10 --name ch1
pg addon clickhouse user create svc --host-name app.internal --name ch1
pg addon clickhouse user create localonly --host-local --name ch1

pg addon clickhouse user list --name ch1
pg addon clickhouse user drop app --name ch1 --yes
```

Users are created with `IDENTIFIED WITH sha256_password`. `--host-any` is the default when no host flag is given; `--host-local`, `--host-ip` (IP or CIDR) and `--host-name` add `HOST` clauses. `--readonly` appends `SETTINGS readonly = 1`. A generated password is printed exactly once and is **not** stored in `pg.yaml` — save it. `--if-not-exists` makes `user create` idempotent.

```bash
# Read + write on one database
pg addon clickhouse grant app --read --write --database demo --name ch1

# DDL keywords = CREATE, ALTER, DROP, TRUNCATE, OPTIMIZE
pg addon clickhouse grant app --ddl --database demo --name ch1

# Table scope (requires an explicit database)
pg addon clickhouse grant reader --read --database demo --table events --name ch1

# Explicit list, wildcard scope, grant option
pg addon clickhouse grant app --privileges 'SELECT,INSERT' --name ch1
pg addon clickhouse grant app --all --with-grant-option --name ch1

pg addon clickhouse grant list app --name ch1
pg addon clickhouse grant app --read --database demo --revoke --name ch1
```

`--read` is `SELECT`, `--write` is `INSERT`, `--ddl` expands to `CREATE, ALTER, DROP, TRUNCATE, OPTIMIZE`, `--all` grants `ALL`, and `--privileges` takes a comma-separated list (uppercased and de-duplicated with the flag selections). Without `--database` the grant targets `*` (all databases); `--table` requires an explicit database. `--revoke` emits `REVOKE ... FROM`. `grant list` runs `SHOW GRANTS FOR`.

All of these run as the instance's admin account; the CLI never stores per-user passwords or credentials.

## Configuration

Server settings are addressed by **dotted XML paths**; the leaf value is the text of that element in the generated `zz-pgcli.xml`.

```bash
# At install time
pg addon install clickhouse --name ch1 \
  --config-option max_server_memory_usage=2147483648 \
  --config-option logger.level=warning

# On a live instance
pg addon clickhouse config list --name ch1
pg addon clickhouse config get max_server_memory_usage --name ch1
pg addon clickhouse config set max_server_memory_usage=2147483648 --name ch1
pg addon clickhouse config set logger.level=debug query_log.flush_interval_milliseconds=1000 --name ch1
pg addon clickhouse config unset logger.level --name ch1
pg addon clickhouse config show --name ch1        # full generated XML
```

- `config set KEY=VALUE ...` accepts one or more pairs, validates them, rewrites the XML and `pg.yaml`, then **restarts a running container** so the change takes effect. Pass `--no-restart` to defer it to the next start.
- `config unset KEY ...` removes overrides (one or more) and follows the same restart rule. Unsetting an unknown key is an error.
- `config get` prints the effective value of a custom override or a managed setting; `config list` shows managed settings first, then the custom overrides sorted by key.
- `config show` renders the same XML that is written to `config.d/zz-pgcli.xml`; it does not touch the file or the container.
- `--config-option` at install time **replaces** the whole override map; omit it to keep the stored one.

`logger.level` is special-cased: it folds into the managed `<logger>` block instead of producing a second `<level>` element. Valid levels are `trace`, `debug`, `information`, `warning`, `error`, `fatal` and `none`.

pgcli-managed keys are rejected, because overriding them would duplicate generated XML or silently contradict the stored paths/ports: `listen_host`, `listen_try`, `http_port`, `https_port`, `tcp_port`, `interserver_http_port`, `interserver_http_host`, `mysql_port`, `postgresql_port`, `path`, `tmp_path`, `user_files_path`, `format_schema_path`, `access_control_path`, `backups.*`, `user_directories`, `profiles`, `users`, `storage_configuration.disks`, `storage_configuration.policies`, and every `logger.*` key except `logger.level`. Keys containing spaces or values containing control characters are rejected too.

Typical settings:

```bash
pg addon clickhouse config set max_server_memory_usage=2147483648 --name ch1
pg addon clickhouse config set background_pool_size=8 max_concurrent_queries=100 --name ch1
pg addon clickhouse config set query_log.flush_interval_milliseconds=1000 --name ch1
pg addon clickhouse config set merge_tree.max_suspicious=0 --name ch1
```

Server settings cannot be changed with a hot reload by this CLI; every effective change recreates or restarts the container (ClickHouse reads this generated file at startup).

pgcli validates only the key shape and the managed-key rules — the **value** is checked by ClickHouse itself at startup. A value it rejects (for example a `background_pool_size` below what the merge/mutation sanity check allows) aborts startup, and the container's `unless-stopped` policy turns that into a restart loop. Recover without losing data:

```bash
pg addon stop clickhouse --name ch1
pg addon clickhouse config unset background_pool_size --no-restart --name ch1
pg addon start clickhouse --name ch1
```

## Backup and restore

Backups use ClickHouse's SQL `BACKUP` command against the local `backups` disk (`/var/lib/clickhouse-backups`). They run **online** and the instance must be running. Only the managed local disk is exposed — S3 destinations are not configured.

```bash
pg addon clickhouse backup --name ch1 --database demo
# ✓ backup written: <instance dir>/backups/demo-20261027-120000.zip (… bytes)

# Explicit name or nested path inside the backups dir
pg addon clickhouse backup --name ch1 --database demo --out nightly.zip
pg addon clickhouse backup --name ch1 --database demo --out archive/2026-q4.zip

pg addon clickhouse backups --name ch1
pg addon clickhouse restore demo-20261027-120000.zip --name ch1 --database demo
pg addon clickhouse restore demo-20261027-120000.zip --name ch1 --database demo --as demo_restored
```

- `--database` is required for both commands: it names the database recorded in the backup (there is no "all databases" form in v1).
- `--out` is relative to the backups dir unless absolute; absolute paths must stay inside that dir, and the file must end in `.zip`. The default name is `<database>-YYYYMMDD-HHMMSS.zip` (UTC).
- `restore` takes a file **inside the backups dir**, plus the recorded database name. Restoring into a database that already exists fails (ClickHouse refuses to overwrite); use `--as NEW` to restore alongside it.
- Restore itself is online, but it needs free space and a running instance; `backups` lists `.zip` files sorted newest first.

The backups directory is a normal host directory — copy the `.zip` files off-host for real disaster recovery. There is no scheduled backup; wire `pg addon clickhouse backup` into your own timer if you need one.

```bash
# Off-host copy
rsync -a /home/you/.pgcli/addon/clickhouse/ch1/backups/ backup-host:/backups/clickhouse-ch1/
```

## Lifecycle

```bash
pg addon start clickhouse --name ch1      # create if missing, then wait for /ping
pg addon stop clickhouse --name ch1
pg addon clickhouse status --name ch1     # state, ping, endpoints, version, paths, override count
pg logs addon clickhouse --name ch1 -f    # container logs (stdout/stderr)
pg addon remove clickhouse --name ch1     # remove container + pg.yaml entry, keep data
pg addon remove clickhouse --name ch1 --clean-data   # also delete the instance dir and a custom data dir
```

`start` re-renders the generated config and password file from `pg.yaml` before starting, so a stopped instance picks up `config set --no-restart` changes and a changed admin password. `status` reports `running (ping ok)` when the HTTP `/ping` endpoint answers.

Without `--clean-data`, `remove` leaves `<instance dir>` (data, config, logs, backups, secret) and a custom `--data-dir` on disk; reinstalling under the same name revives the data and reuses the kept admin password (pass `--admin-password` to override). `--clean-data` deletes both the instance dir and a custom data dir (files owned by uid 101 are removed through `podman unshare`), but never removes the pulled image.

Auto-start after reboot:

```bash
pg autostart enable --clickhouse --name ch1
pg autostart status
pg autostart disable --clickhouse --name ch1
```

Boot start also re-renders the config and secret first, then starts the existing container. `pg start` starts every autostart-enabled ClickHouse instance before nginx, so an nginx backend pointing at ClickHouse's HTTP port is up when the proxy starts.

## Production notes

- **Single node only.** This addon has no replication, no sharding and no HA. ClickHouse ReplicatedMergeTree/Keeper topologies are out of scope; backups are the disaster-recovery path.
- **No TLS listener.** The HTTP and native ports are plaintext. To expose ClickHouse over the network with TLS, put the [nginx addon](./nginx/) in front of the HTTP port (or use an external proxy); do not expose the native port directly to untrusted networks.
- **Loopback by default.** `--listen` defaults to `127.0.0.1`. Authentication is always on, but a network bind still widens the attack surface; use `0.0.0.0` only on trusted networks.
- **Password storage.** The admin password lives in `pg.yaml` (`0644` by default); protect that file and prefer `pg addon password clickhouse` over copying it around.
- **Resource use.** The container has no CPU/memory limit from pgcli. ClickHouse derives its memory budget from the host; on a shared host set `max_server_memory_usage` explicitly (and consider `max_concurrent_queries`, `background_pool_size`) via `config set`.
- **Upgrades.** `--force --version <new>` pulls a new tag and recreates the container on the same data dir. ClickHouse supports forward upgrades only — never start an older server against data written by a newer version. Take a backup before upgrading.
- **Data ownership.** Data files are owned by uid 101. Use `--clean-data` rather than deleting the directory by hand; pgcli handles the permission fallback.
- **Platform.** Linux only (host networking); the official image is multi-arch (amd64/arm64).

## CLI flag reference

### `pg addon install clickhouse`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `clickhouse` | Instance name (map key); `[A-Za-z0-9][A-Za-z0-9_-]*`, max 48 chars |
| `--version TAG` | `26.8.36.3` | Upstream image tag from `docker.io/clickhouse/clickhouse-server` |
| `--data-dir DIR` | `<base_dir>/addon/clickhouse/<name>/data` | Absolute host data dir bind-mounted at `/var/lib/clickhouse` |
| `--listen ADDR` | `127.0.0.1` | Bind address for both listeners |
| `--http-port N` | auto (base `8123`) | HTTP host port; `0` = auto-assign |
| `--tcp-port N` | auto (base `9000`) | Native host port; `0` = auto-assign; must differ from `--http-port` |
| `--admin-user USER` | `admin` | Admin account name; `[A-Za-z0-9_.-]{1,64}` |
| `--admin-password PW` | generated | Admin password; on `--force` the stored value is kept unless supplied |
| `--config-option K=V` | — | Dotted XML path setting; repeatable; replaces the stored override map |
| `--force` | false | Re-render config/secret and recreate the container, keeping data, password and settings |

### `pg addon clickhouse status` / `shell` / `query [SQL...]`

| Argument/flag | Default | Description |
|---|---|---|
| `--name NAME` | `clickhouse` | Instance to operate on |
| `SQL...` | — | `query` only: SQL joined from positional arguments |
| `--database DB` | — | `query` only: adds `--database DB` |
| `--file FILE` | — | `query` only: read SQL from a host file (`-` = stdin) |
| `-` (argument) | — | `query` only: read SQL from stdin |

### `pg addon clickhouse database create|list|drop`

| Argument/flag | Default | Description |
|---|---|---|
| `NAME` | — | Database name (`create`/`drop`, positional) |
| `--engine NAME` | `Atomic` | Bare engine name (no parameters) |
| `--comment TEXT` | — | Database comment |
| `--if-not-exists` | false | `create` only |
| `--if-exists` | false | `drop` only |
| `--yes` | false | `drop` only: skip the confirmation prompt |

`db` is an accepted alias for `database`.

### `pg addon clickhouse user create|list|drop`

| Argument/flag | Default | Description |
|---|---|---|
| `NAME` | — | User name (`create`/`drop`, positional) |
| `--password PW` | generated | Password; generated and printed once when omitted |
| `--default-database DB` | — | `DEFAULT DATABASE` clause |
| `--host-any` | implied | Allow any host (applied when no host flag is given) |
| `--host-local` | false | `LOCAL` (loopback) only |
| `--host-ip IP\|CIDR` | — | Repeatable `IP` host clause |
| `--host-name NAME` | — | Repeatable `NAME` host clause |
| `--readonly` | false | Add `SETTINGS readonly = 1` |
| `--if-not-exists` | false | `create` only |
| `--if-exists` | false | `drop` only |
| `--yes` | false | `drop` only: skip the confirmation prompt |

### `pg addon clickhouse grant USER` / `grant list USER`

| Argument/flag | Default | Description |
|---|---|---|
| `USER` | — | Target user (positional) |
| `--read` | false | Grant `SELECT` |
| `--write` | false | Grant `INSERT` |
| `--ddl` | false | Grant `CREATE, ALTER, DROP, TRUNCATE, OPTIMIZE` |
| `--all` | false | Grant `ALL` |
| `--privileges LIST` | — | Extra comma-separated privileges, merged with the flags above |
| `--database DB` | all (`*`) | Scope to a database |
| `--table TABLE` | all tables | Scope to a table; requires `--database` |
| `--revoke` | false | Emit `REVOKE ... FROM` instead of `GRANT ... TO` |
| `--with-grant-option` | false | Append `WITH GRANT OPTION` (grant only) |

### `pg addon clickhouse backup` / `restore FILE` / `backups`

| Argument/flag | Default | Description |
|---|---|---|
| `FILE` | — | `restore` only: `.zip` file inside the backups dir |
| `--database DB` | required | Database to back up / the database name recorded in the backup |
| `--out NAME` | `<db>-<UTC timestamp>.zip` | `backup` only: output path inside the backups dir |
| `--as NEW` | — | `restore` only: restore under a new database name |

### `pg addon clickhouse config list|get|set|unset|show`

| Argument/flag | Default | Description |
|---|---|---|
| `KEY=VALUE...` | — | `set` only: one or more dotted XML settings |
| `KEY...` | — | `get`/`unset`: one setting key (`get` takes exactly one) |
| `--name NAME` | `clickhouse` | Instance to inspect or modify |
| `--no-restart` | false | `set`/`unset` only: apply on the next start instead of restarting now |

### Lifecycle, credentials, logs, autostart

| Command | Flags | Description |
|---|---|---|
| `pg addon start clickhouse` | `--name NAME` | Start (create if missing), wait for `/ping` |
| `pg addon stop clickhouse` | `--name NAME` | Stop the container, keep data |
| `pg addon remove clickhouse` | `--name NAME`, `--clean-data` | Remove container and config entry; `--clean-data` also deletes the instance dir and a custom data dir |
| `pg addon password clickhouse` | `--name NAME` | Print the admin password |
| `pg logs addon clickhouse` | `--name NAME`, `-n N` (default 50), `-f` | Container logs |
| `pg autostart enable\|disable` | `--clickhouse`, `--name NAME` | Toggle boot start for one instance |

### Global flags

| Flag | Description |
|---|---|
| `-c, --config FILE` | Config file (default `~/.pgcli/pg.yaml`) |
| `-o, --output FILE` | Output config path (default the same file) |
| `-i, --instance NAME` | PostgreSQL instance selector — not used by ClickHouse; use `--name` instead |
