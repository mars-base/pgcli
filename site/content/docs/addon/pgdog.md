---
title: "PgDog"
description: "Run PgDog — a Postgres proxy for connection pooling, load balancing and sharding — as a pgcli addon"
weight: 40
---

[PgDog](https://pgdog.dev) is a high-performance Postgres proxy written in Rust
(the successor to PgCat). It provides connection pooling, read/write load
balancing across replicas, and horizontal sharding — in front of one or more
PostgreSQL backends. pgcli can run PgDog as a **standalone, top-level addon**:
shared infrastructure rather than a per-instance sidecar.

> **Security caveat:** PgDog authenticates clients against **plaintext**
> passwords stored in `users.toml`. pgcli writes that file mode `0600` and
> mounts it read-only into the container, but the passwords still sit in clear
> text on disk and in `pg.yaml`. Keep the listen port on loopback (the default)
> or inside a trusted network, and treat the config file as a secret.

PgDog is configured with two TOML files — `pgdog.toml` (the proxy, its
backends, and sharding rules) and `users.toml` (the client credentials).
pgcli **generates both entirely from the install flags**: there is no config
file to hand-edit, so `pg addon install pgdog` is the source of truth and is
idempotent — re-running it re-renders the files from the flags given.

## Platform support

PgDog runs on **Linux** (host networking) and on **macOS** for **single-host
dev/test**, joining the same `pgcli-net` bridge a PG instance uses under podman
machine and publishing its client + openmetrics ports. On macOS:

- The listen bind is widened to `0.0.0.0` inside the container so the published
  port is reachable (a loopback-only bind can't be forwarded to); the client
  address you connect to is still `127.0.0.1:<port>`.
- **Backends must be reachable from the bridge.** PgDog has no automatic
  name-resolution for `--backend`, so a `127.0.0.1` backend would point at the
  Mac, not the podman machine VM. Give each `--backend` either the target
  instance's **container name** (run `pg status -i <instance>` and read the
  `Container:` line, e.g. `app=pgcli-pg-mypg:5432:...`) or an address the Mac
  can route to — never `127.0.0.1` for a managed instance.

## How It Works

- **Shared infrastructure:** PgDog lives in the top-level `addons.pgdog` map in
  `pg.yaml`, keyed by proxy name — not under any single instance.
- **Host network (Linux) / bridge (macOS):** the proxy listens on its client
  port (auto-assigned from `pgdog_start_port`, default 7432) plus a
  Prometheus-style `openmetrics` port on the next free number. On Linux it runs
  with `--network host`; on macOS it joins `pgcli-net` and publishes both ports.
- **Generated config:** `pgdog.toml` + `users.toml` are written under
  `<base-dir>/addon/pgdog/<name>/` and bind-mounted into the container at
  `/pgdog`.
- **Image pinned:** `ghcr.io/pgdogdev/pgdog:v0.1.57` by default; override with
  `--image`.

## Install

A minimal single-backend proxy (pooling only — see the note below):

```bash
pg addon install pgdog \
  --backend "app=127.0.0.1:35432:default_db" \
  --user "appuser:secret:app" \
  --sharded-table "app:users:id:bigint"
```

> Declare a `--sharded-table` so the example is a complete, self-consistent
> config (it mirrors the sharding example below). Be aware that with a **single**
> `--backend` there is nothing to shard across — every row lands on that one
> backend regardless of the declaration, so this minimal example only
> demonstrates connection pooling. For real sharding you need two or more
> `--backend`s with distinct shard numbers (see
> [Sharding and replicas](#sharding-and-replicas)).

- `--backend NAME=HOST:PORT:DBNAME[:SHARD[:ROLE]]` — one `[[databases]]` entry.
  `NAME` is the logical database clients connect to; `DBNAME` is the real
  database on the backend. `SHARD` defaults to 0; `ROLE` to PgDog's default
  (`primary`, or set `replica` for read routing). Repeatable.
- `--user NAME:PASSWORD[:DBNAME]` — one `[[users]]` entry. The `DBNAME` is the
  logical database (a `--backend` name) the user may reach; it defaults to the
  first backend's name. Repeatable.
- At least one `--backend` and one `--user` are required.

Other flags:

| Flag | Meaning | Default |
|------|---------|---------|
| `--name` | proxy / addon key | `pgdog` |
| `--port` | client host port (openmetrics takes the next free) | auto-assign |
| `--host` | listen address | `127.0.0.1` |
| `--pool-mode` | `transaction` or `session` | `transaction` |
| `--workers` | worker threads | `2` |
| `--default-pool-size` | server connections per user/db pair | `10` |
| `--image` | PgDog image tag | `ghcr.io/pgdogdev/pgdog:v0.1.57` |

### Sharding and replicas

Repeat `--backend` across shards and roles, and declare the shard key with
`--sharded-table DBNAME:TABLE:COLUMN:DATA_TYPE`:

```bash
pg addon install pgdog \
  --backend "app=10.0.0.1:5432:shard0:0" \
  --backend "app=10.0.0.2:5432:shard1:1" \
  --backend "app=10.0.0.1:5433:shard0:0:replica" \
  --backend "app=10.0.0.2:5433:shard1:1:replica" \
  --user "appuser:secret:app" \
  --sharded-table "app:users:id:bigint"
```

This puts two shards (each with a primary + replica) behind the logical
database `app` and routes the `users` table by its `id` column.

### Provisioning the backends

PgDog routes to databases, tables, and roles that must already exist on the
backends — installing the proxy does not create them. For a local pgcli
instance, use `pg exec` to create each shard database, its sharded table, and
the login role. With the default instance on a single host (shards as separate
databases on one port), the example below provisions the backends (steps 1–4),
installs the proxy (step 5), and writes through it to show the routing (steps
6–7):

```bash
# 1. create the shard databases
pg exec -i default -- psql -U admin -d default_db -c "CREATE DATABASE shard0;"
pg exec -i default -- psql -U admin -d default_db -c "CREATE DATABASE shard1;"

# 2. create the sharded table (same schema) in each shard
for db in shard0 shard1; do
  pg exec -i default -- psql -U admin -d "$db" \
    -c "CREATE TABLE users (id bigint PRIMARY KEY, name text, email text);"
done

# 3. create the login role PgDog uses to reach the backends — its name must
#    match a --user, and its password must match that user's users.toml entry
pg exec -i default -- psql -U admin -d default_db \
  -c "CREATE ROLE appuser LOGIN PASSWORD 'secret';"

# 4. grant that role access to the sharded table in every shard
for db in shard0 shard1; do
  pg exec -i default -- psql -U admin -d "$db" \
    -c "GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE users TO appuser;"
done

# 5. install the proxy over both shards (see "Sharding and replicas" above)
pg addon install pgdog \
  --backend "app=127.0.0.1:35432:shard0:0" \
  --backend "app=127.0.0.1:35432:shard1:1" \
  --user "appuser:secret:app" \
  --sharded-table "app:users:id:bigint"

# 6. write rows through the proxy with pgcli (pg exec --dsn), ONE row per
#    statement so PgDog routes each by its id (a multi-row INSERT is broadcast
#    to every shard instead)
for id in 1 2 3 4 5 6; do
  pg exec --dsn "postgres://appuser:secret@127.0.0.1:7432/app" \
    "INSERT INTO users (id, name, email) VALUES ($id, 'n$id', 'e$id@x');"
done

# 7. read each shard directly to confirm the rows landed where PgDog routed
#    them — pg psql -- -d <db> targets one database
pg psql -i default -- -d shard0 -tAc "SELECT id, name FROM users ORDER BY id;"
pg psql -i default -- -d shard1 -tAc "SELECT id, name FROM users ORDER BY id;"
```

`pg exec -i default -- psql -d <db>` runs a command against a specific database
in the named instance's container. The `id bigint` column matches the
`--sharded-table "app:users:id:bigint"` declaration above, so PgDog can route
`users` rows by `id`. Steps 6–7 make that routing visible: the two shard queries
return **different, non-overlapping** subsets of the ids you inserted (the exact
split depends on PgDog's hash), and together they account for every row — no row
lands in both shards. For remote backends, run the equivalent SQL against each
host directly.

**The backend role is not optional.** By default PgDog authenticates to the
backends using the *client's* `--user` name and its `users.toml` password. So
every backend must have a login role named `appuser` (etc.), or connections fail
with `password for user "..." is wrong, or the database does not exist`, even on
a loopback `trust` setup where the password is ignored but the *role* must still
exist. Under password auth (the default for non-loopback `scram-sha-256`), the
`users.toml` password must match the role's.

> **Decoupling client and backend users.** PgDog itself supports connecting to
> the backends with a *different* user than the client uses: a `[[users]]` entry
> can carry `server_user` / `server_password`, and a `[[databases]]` entry can
> carry `user` / `password` (which take priority). **pgcli's install flags do not
> currently emit these fields**, so through `pg addon install pgdog` the backend
> role name is always the `--user` name. If you need a shared backend role (a
> common `postgres` superuser, or a name that differs from every client user),
> hand-edit the generated `users.toml`/`pgdog.toml` and restart the container —
> but note a re-install regenerates both files from the flags and will discard
> those edits.

## Connecting

The install summary prints the endpoint. Clients connect through the proxy's
client port, using the logical database name and a `--user` credential — with
pgcli, that's a `--dsn` pointing at the proxy:

```bash
# interactive psql through the proxy
pg psql --dsn "postgres://appuser:secret@127.0.0.1:7432/app"

# one-shot SQL
pg exec --dsn "postgres://appuser:secret@127.0.0.1:7432/app" "SELECT version();"
```

Prometheus metrics are served on the openmetrics port:

```bash
curl -s http://127.0.0.1:7433/metrics
```

## Writing to shards

How a write lands on the shards depends on the *form* of the statement.
Observed with PgDog v0.1.57:

**Single-row `INSERT` — routed by the shard key.** One `VALUES` tuple per
statement; PgDog hashes the `id` and sends that row to exactly one shard:

```bash
pg exec --dsn "postgres://appuser:secret@127.0.0.1:7432/app" \
  "INSERT INTO users (id, name, email) VALUES (4, 'n4', 'e4@x');"
```

Inserting ids 1–6 as six single-row statements split them across the two
shards (in one run: shard0 got `1,2`, shard1 got `3,4,5,6` — the exact split
is PgDog's hash, not something to rely on). No row lands in both shards.

**Multi-row `INSERT` — broadcast, not split.** A single statement carrying
several `VALUES` tuples is sent **verbatim to every shard**, so each shard
ends up with *every* row:

```bash
pg exec --dsn "postgres://appuser:secret@127.0.0.1:7432/app" \
  "INSERT INTO users (id, name, email) VALUES (1,'a','a@x'),(2,'b','b@x'),(3,'c','c@x'),(4,'d','d@x');"
```

This reported `INSERT 0 8` — 4 rows in *each* shard. The per-shard `PRIMARY
KEY` still holds (the duplicate ids live in different shards, never colliding),
and there is **no error or warning**: every shard silently grows a full copy of
the batch. Reads through the proxy will then see each id multiple times.

**Bulk-loading correctly:** loop one statement per row through the proxy (each
routed individually), or skip the proxy and run the load against each shard
directly (`COPY` via `pg exec -i <inst> -- psql -d <shard>`).

## Configuration

After install, `pg.yaml` records each proxy under the top-level `addons.pgdog`:

```yaml
addons:
  pgdog:
    pgdog:
      container_name: pgcli-pgdog-pgdog
      name: pgdog
      image_tag: ghcr.io/pgdogdev/pgdog:v0.1.57
      host: 127.0.0.1
      host_port: 7432
      openmetrics_port: 7433
      pooler_mode: transaction
      workers: 2
      default_pool_size: 10
      backends:
        - name: app
          host: 127.0.0.1
          port: 35432
          database_name: default_db
          shard: 0
      users:
        - name: appuser
          password: secret
          database: app
      autostart: false
```

Port base is configurable via the top-level `pgdog_start_port` (default 7432);
the openmetrics port is always allocated just above it.

### List

```bash
pg addon list
```

Every PgDog proxy appears under the **Infra add-ons (pgdog)** section with its
runtime status, ports, backend/user counts, image, and container name:

```
Infra add-ons (pgdog):
  pgdog (name: pgdog)
    Status:      running
    Listen:      127.0.0.1:7432
    Client port: 7432
    Metrics:     http://127.0.0.1:7433/metrics
    Pool mode:   transaction
    Backends:    2
    Users:       1
    Image:       ghcr.io/pgdogdev/pgdog:v0.1.57
    Container:   pgcli-pgdog-pgdog
```

`Backends` / `Users` are the counts from `pgdog.toml` / `users.toml`; `Status`
reflects the live container (a stopped proxy shows `stopped`).

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring a proxy up after a host reboot:

```bash
pg autostart enable --pgdog --name pgdog
```

This sets `autostart: true` and installs/refreshes the boot service (see
[Auto-start on Boot](/docs/autostart/)). Boot is **start-only**: it starts the
existing container reading the `pgdog.toml`/`users.toml` already on disk, and
never re-generates config or auth. Install the proxy first. `pg autostart
status` lists every target's state.

## Logs

```bash
pg logs addon pgdog --name pgdog       # last 50 lines
pg logs addon pgdog --name pgdog -f    # follow
```

PgDog logs connection-pool events (new server connections, auth, client
connect/disconnect) as structured INFO lines. A repeatedly failing pool with
`password for user "..." is wrong, or the database does not exist` means step 3
or 4 above was skipped — the backend has no such login role, or its password
does not match `users.toml`.

## Notes

- **Reinstall is idempotent:** re-running `pg addon install pgdog --name <p>`
  re-renders the config from the given flags and recreates the container. The
  backend/user/shard lists are **fully replaced** by this command's flags — an
  omitted `--backend` is removed, not kept.
- **Plaintext credentials:** passwords live in `users.toml` and `pg.yaml`.
  Prefer a dedicated low-privilege role for proxy users and restrict network
  access to the client port.
- **Write forms differ:** single-row `INSERT` is routed by the shard key; a
  multi-row `INSERT VALUES` is **broadcast** to every shard. See
  [Writing to shards](#writing-to-shards).
- **Not a per-instance sidecar:** unlike PgBouncer, PgDog frontends backends by
  address in `--backend` — it does not have to point at a pgcli-managed
  instance, and one proxy can span many databases/shards.
