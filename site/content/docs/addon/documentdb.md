---
title: "DocumentDB Gateway"
description: "Run the official DocumentDB MongoDB wire-protocol gateway as a managed pgcli addon, connected to a local PostgreSQL 18 instance over a shared Unix socket."
weight: 68
---

`pg addon install documentdb` runs the official **DocumentDB gateway** — the Rust service that speaks the MongoDB wire protocol in front of the [DocumentDB PostgreSQL extension](../../extensions/documentdb/) — as a managed container.

```
MongoDB clients (pymongo, mongosh, …)
        │  port 10260
        ▼
DocumentDB gateway container
        │  Unix socket (shared volume)
        ▼
PostgreSQL 18 container with documentdb / documentdb_core /
documentdb_extended_rum / pg_cron
```

> **Install the extension first.** The gateway refuses to start unless the target instance is running, exports `/var/run/postgresql`, and has all four extensions. See [DocumentDB extension](../../extensions/documentdb/).

> **Local instances only.** The official gateway `.deb` supports passwordless authentication over a Unix socket and refuses `PGPASSWORD`, passwords in the URL, and `PostgresDataUserPassword`. pgcli therefore connects the gateway through a shared socket volume and rejects `--dsn`/`--admin-dsn`. A remote PostgreSQL cannot be used as the target.

> **Access model.** The gateway connects to PostgreSQL as the dedicated role `docdb_gateway` (`LOGIN SUPERUSER`, no password) through `local … trust` in `pg_hba.conf`. MongoDB clients authenticate as a DocumentDB user (default `docdb_admin`) with the built-in roles `readWriteAnyDatabase` + `clusterAdmin` — broad by design. Use a distinct `--username` per application if you need finer separation later; v1 does not expose per-collection roles.

> **No telemetry.** pgcli does not use the upstream all-in-one image or its `usage_telemetry.sh`. It runs the vendor gateway binary directly and renders `SetupConfiguration.json` with `TelemetryOptions.Metrics.Enabled` and `Tracing.Enabled` set to `false`; no OTLP endpoint is injected.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `documentdb` | Add-on name (map key and container suffix) |
| `--pg-name` | — | **Required.** Target pgcli instance (must be PG 18, standalone, with the DocumentDB extension) |
| `--database` | target instance's database | Database containing the DocumentDB extension |
| `--username` | `docdb_admin` | MongoDB client user, created through `documentdb_api.create_user`. Prefixes `documentdb`, `citus`, `pg`, `internal_role` are rejected |
| `--password` | generated | Client password. Printed once when generated; retrieve later with `pg addon password documentdb` |
| `--tls-mode` | `allow` | `allow` = plaintext and TLS; `require` = TLS only |
| `--image` | `ghcr.io/mars-base/pgcli/pgcli-documentdb-gateway:1.0.0` | Gateway image override |
| `--port` | auto (base 10260) | MongoDB wire-protocol port |
| `--listen` | `0.0.0.0` | Bind address (`0.0.0.0`, `127.0.0.1` or `::1`) |
| `--force` | false | Recreate even when running, applying changed flags |

`--dsn` and `--admin-dsn` are rejected for this add-on.

## Install

```bash
pg extension install documentdb -i demo --auto-restart
pg addon install documentdb --pg-name demo
```

The install prints the one-time password and the endpoint:

```
✓ DocumentDB gateway installed: "documentdb"
  Container:  pgcli-documentdb-documentdb
  Image:      ghcr.io/mars-base/pgcli/pgcli-documentdb-gateway:1.0.0
  Endpoint:   mongodb://docdb_admin@127.0.0.1:10260/demo_db?directConnection=true
  Target:     instance demo (database demo_db)
  Client:     docdb_admin (DocumentDB native user)
  TLS mode:   allow

  Client password: n7Qv2Kd9Xa4Tb8Rm3Lc6Wf1Z
  (printed once — retrieve it later with `pg addon password documentdb --name documentdb`)
```

During install pgcli also:

1. Prepares the target — ensures `docdb_gateway` exists with `LOGIN SUPERUSER`, and adds a managed `local all docdb_gateway trust` rule at the top of `pg_hba.conf` **only if** no matching local trust rule is present (pgcli's default HBA already has one).
2. Creates or rotates the client user through `documentdb_api.create_user` / `update_user`. The JSON document travels over `psql` stdin — never argv or PostgreSQL logs — after `SET log_statement='none'`.
3. Renders `pg-url` and `SetupConfiguration.json` under `<base-dir>/addon/documentdb/<name>/` and mounts the target's socket volume read-only.
4. Starts the gateway container with `--restart unless-stopped`.

Re-running the same install while the container is running is a no-op unless `--force` is passed.

## Client Test with Python (`uv` + pymongo)

pgcli's repository uses [`uv`](https://docs.astral.sh/uv/) for ad-hoc Python dependencies. This is the end-to-end connection and CRUD test used during development:

```bash
export MONGO_PASSWORD="$(pg addon password documentdb --name documentdb)"

uv run --with pymongo python - <<'PY'
import os
from urllib.parse import quote
from pymongo import MongoClient

host = os.environ.get("MONGO_HOST", "127.0.0.1")
port = os.environ.get("MONGO_PORT", "10260")
user = os.environ.get("MONGO_USER", "docdb_admin")
password = quote(os.environ["MONGO_PASSWORD"], safe="")

# Set MONGO_TLS=true to connect over TLS.
use_tls = os.environ.get("MONGO_TLS", "").lower() in ("1", "true", "yes")
params = "directConnection=true"
if use_tls:
    params += "&tls=true&tlsAllowInvalidCertificates=true"

uri = f"mongodb://{user}:{password}@{host}:{port}/?{params}"
client = MongoClient(uri, serverSelectionTimeoutMS=10000)
print("ping:", client.admin.command("ping"))

col = client["pgcli_e2e"]["smoke"]
col.drop()
print("inserted:", col.insert_one({"name": "alpha", "value": 1}).inserted_id)
print("found:   ", col.find_one({"name": "alpha"}))

print("updated: ", col.update_one({"name": "alpha"}, {"$set": {"value": 2}}).modified_count)
print("index:   ", col.create_index([("value", 1)]))
print("count:   ", col.count_documents({}))
print("deleted: ", col.delete_one({"name": "alpha"}).deleted_count)

client.drop_database("pgcli_e2e")
client.close()
print("OK")
PY
```

Run it on the host where the add-on listens; from another machine set `MONGO_HOST` to the gateway host. TLS is off by default in the script: run with `MONGO_TLS=true` to enable it. This works in both `allow` and `require` modes (plaintext is rejected in `require` anyway). `tlsAllowInvalidCertificates` is needed only because the auto-generated certificate is self-signed; with a certificate you trust, use `tlsCAFile="/path/to/ca.pem"` instead.

## Management CLI (mongosh)

pgcli can connect to and inspect a managed or remote gateway with `mongosh` without installing it on the host and without adding a MongoDB driver to the pgcli binary. The client runs from a separate mongosh-only image — the official mongosh 2.14.1 binary on `debian:13-slim`, no MongoDB server — which is pulled on first use and runs as a throwaway container. The pgcli binary does not grow.

```bash
pg addon documentdb shell
pg addon documentdb shell --database app
pg addon documentdb shell -- --eval 'db.version()'

pg addon documentdb databases list
pg addon documentdb collections list --database app
pg addon documentdb indexes list --database app --collection orders
```

All subcommands accept the same persistent flags:

| Flag | Default | Description |
|---|---|---|
| `--name` | `documentdb` | Gateway add-on to connect to (explicit `--name` forces local mode and ignores `DOCDB_URI`) |
| `--dsn` | `$DOCDB_URI` | Full MongoDB connection string — recommended for a gateway on another host |
| `--uri` | — | Alias for `--dsn` |
| `--host` / `--port` | — / `10260` | Alternative to `--dsn` |
| `--username` / `--password` | `$DOCDB_USERNAME` / `$DOCDB_PASSWORD` | Credentials with `--host`; prefer the environment over `--password`, which is visible in `ps` |
| `--tls` | false | Force TLS. Enabled automatically when a managed gateway's `--tls-mode` is `require` |
| `--image` | `ghcr.io/mars-base/pgcli/pgcli-mongosh:2.14.1` | Client image override (private registry or offline) |

`shell` opens an interactive session; `--database` selects the initial database (the gateway's target database or the URI's database by default), and arguments after `--` are forwarded to mongosh verbatim. `databases list`, `collections list`, and `indexes list` run read-only queries and print one name (or one JSON index document) per line; `indexes list` requires `--collection`.

### Remote Gateways

No local add-on entry is needed to drive a gateway on another host. Export the connection string once so the commands stay short:

```bash
export DOCDB_PASSWORD='...'   # from the gateway host: pg addon password documentdb
export DOCDB_URI='mongodb://docdb_admin@10.0.0.5:10260/demo_db?authSource=admin&directConnection=true'

pg addon documentdb databases list
pg addon documentdb collections list --database demo_db
pg addon documentdb shell -- --eval 'db.version()'
```

Use `--dsn` (or its alias `--uri`) for one-off commands:

```bash
pg addon documentdb collections list \
  --dsn 'mongodb://docdb_admin@10.0.0.5:10260/demo_db'
```

Rules:

- A password missing from the URI is filled from `DOCDB_PASSWORD`; missing userinfo is filled from `DOCDB_USERNAME`. Prefer these environment variables over `--password`, whose value appears in `ps`.
- pgcli adds `authSource=admin`, `directConnection=true`, and `serverSelectionTimeoutMS=10000` when the URI omits them. Values already in the URI, including its database path, win.
- `--tls` forces TLS by adding `tls=true&tlsAllowInvalidCertificates=true`. Use it with a gateway in `require` mode, or put the TLS parameters in the URI yourself.
- `--dsn`/`--uri` cannot be combined with `--host/--port/--username/--password`, and `--name` cannot be combined with any remote flag. Explicit remote flags take precedence over `DOCDB_URI`.
- `--host HOST --port PORT --username USER` remains available as an alternative; the password comes from `DOCDB_PASSWORD`.
- The client host needs podman and the mongosh image, pulled from GHCR on first use (`--image` overrides it). The gateway must listen on an address reachable from the client: the default `--listen 0.0.0.0` works; `127.0.0.1` restricts it to the gateway host.

Remote mode only changes where the mongosh client connects — the gateway itself still targets a local PostgreSQL instance on its own host.

### Common mongosh Operations

`shell` forwards everything after `--` to mongosh. Everyday commands:

```javascript
// inventory
db                       // current database
show dbs                 // DocumentDB lists a database only after it has a collection
show collections
use demo_db              // switch (creates the database implicitly on first write)

// write and read
db.users.insertOne({name: "alice", age: 30})
db.users.insertMany([{name: "bob"}, {name: "carol", age: 41}])
db.users.findOne({name: "alice"})
db.users.find({age: {$gte: 30}}).forEach(printjson)
db.users.countDocuments({age: {$gte: 30}})

// update and delete
db.users.updateOne({name: "alice"}, {$set: {age: 31}})
db.users.updateMany({}, {$inc: {visits: 1}})
db.users.deleteOne({name: "bob"})
db.users.deleteMany({age: {$lt: 18}})

// indexes: 1 = ascending, -1 = descending
db.users.createIndex({name: 1})
db.users.createIndex({tenant: 1, createdAt: -1})   // compound: tenant asc, createdAt desc
db.users.find({tenant: "acme"}).sort({tenant: 1, createdAt: -1})  // matches the compound index
db.users.getIndexes()                              // key shows direction, e.g. {"name":1}
db.users.dropIndex("name_1")

// remove data
db.users.drop()
db.dropDatabase()
```

Run a single command without entering the shell:

```bash
pg addon documentdb shell -- --quiet --eval 'db.getSiblingDB("demo_db").users.countDocuments({})'
pg addon documentdb shell --dsn 'mongodb://docdb_admin@10.0.0.5:10260/demo_db' \
  -- --quiet --eval 'db.users.find({age: {$gte: 30}}).forEach(printjson)'
```

- `find()` shows the first 20 documents in an interactive session; type `it` for more, or `.forEach(printjson)` to print all.
- `_id` is unique per collection, indexed automatically, and generated when omitted. Queries on `_id` do not need another index.
- `drop()` and `dropDatabase()` delete data irreversibly. The read-only CLI helpers (`databases list`, `collections list`, `indexes list`) never write; use `shell` for document queries and writes.
- DocumentDB does not implement every MongoDB command or query operator; check the upstream compatibility notes when a feature is missing.

Notes:

- The connection string is built from the add-on's `pg.yaml` entry (client user, password, port, TLS mode) in local mode, or from `--dsn`/`DOCDB_URI` in remote mode. The password reaches the container through an environment variable passed **by name only**, so it does not appear in podman's argv (`podman inspect`'s `Config.Cmd`).
- DocumentDB reports only databases that contain collections. When a list is empty, pgcli prints an explanatory note on **stderr** and leaves stdout empty, so scripts can keep treating stdout as data.
- The inspection subcommands are intentionally read-only. Create/drop database, collection, index, and user operations are not exposed yet; use `shell` (or a MongoDB driver) for writes.
- The image sets mongosh's telemetry opt-out (`/home/mongosh/.mongodb/mongosh/config`, `{"enableTelemetry":false}`). mongosh 2.14.1 may still print its telemetry notice at startup.

## TLS

The daemon always generates a self-signed certificate on first start and persists it in the named volume `pgcli-documentdb-tls-<name>`, so the certificate survives container recreation.

- `--tls-mode allow` (default): the same port accepts plaintext and TLS.
- `--tls-mode require`: `EnforceTls=true` in `SetupConfiguration.json`; plaintext handshakes are rejected.

Switch modes with a recreate:

```bash
pg addon install documentdb --pg-name demo --tls-mode require --force
```

With `require`, enable TLS on the client as in the test code (`MONGO_TLS=true`), or use `tlsCAFile` when you want certificate validation.

## Lifecycle

```bash
pg addon list                    # DocumentDB section: status/endpoint/target/TLS
pg addon start documentdb --name documentdb
pg addon stop  documentdb --name documentdb
pg logs addon documentdb         # follows the gateway container logs
pg addon password documentdb --name documentdb
pg addon remove documentdb --name documentdb [--clean-data]
```

`remove` deletes the container; `--clean-data` also deletes the TLS state volume and the rendered config directory. **PostgreSQL-side state is kept**: DocumentDB data, the client user, the `docdb_gateway` role and the HBA rule survive removal. The command prints the `DROP ROLE` statements if you want to clean them up manually.

Autostart is supported; the gateway starts **after** the instances in `pg start --autostart` and verifies the target instance is up first:

```bash
pg autostart enable --documentdb --name documentdb
```

## Multiple Gateways

Each gateway gets its own name, container, port and TLS volume:

```bash
pg addon install documentdb --name analytics --pg-name demo --port 10261 --username docdb_analytics
```

> **Use a distinct `--username` when several gateways share the same target database.** DocumentDB users are PostgreSQL roles, so a second install with the same username rotates that role's password — invalidating the credential stored by the first gateway.

### Impact on the Shared PostgreSQL Instance

Multiple gateways may target one instance, but they are not isolated from it or from each other:

| Area | Impact |
|---|---|
| Connections | Each gateway holds at least one client backend as `docdb_gateway` (one idle connection per gateway was observed on a running install). Under load the gateway may open more. Count the total against `max_connections`; v1 has no pooling layer between the gateway and PostgreSQL. |
| CPU / memory / I/O / locks | Every gateway's queries, writes and foreground index builds (`indexBuildsScheduledOnBgWorker=false`) compete on the same instance. Adding gateways adds load, not capacity. |
| Data visibility | A distinct `--username` separates credentials, **not data**. Each DocumentDB user is created with `readWriteAnyDatabase` + `clusterAdmin` by default, so all gateways pointing at the same database read and write the same collections. Use separate databases or separate instances when applications need isolation; v1 exposes no per-collection roles. |
| Roles | PostgreSQL roles are cluster-wide. Client usernames must therefore be unique across gateways. `pg addon remove` keeps each gateway's client role (drop it manually), while the shared `docdb_gateway` role must remain for the surviving gateways. |
| Background workers / `pg_cron` | Configured once per instance, not per gateway. Extra gateways do not start duplicate workers or cron jobs. |

Monitor actual usage with:

```bash
pg exec -i demo "SELECT usename, state, count(*) FROM pg_stat_activity WHERE backend_type = 'client backend' GROUP BY 1, 2 ORDER BY 3 DESC"
```

## Limitations

- Local PG 18 instances only; standalone (not Patroni/HA); `--dsn` is unsupported. (The `pg addon documentdb --dsn` client flag is unrelated — it points mongosh at a gateway running elsewhere.)
- macOS is code-complete but untested.
- TLS uses the auto-generated self-signed certificate only; custom certificates are not exposed yet.
- `pg destroy` deletes the instance's socket volume. Remove the gateway **before** destroying its target instance.
- The gateway connects as a superuser over local trust; the security boundary is the host/container boundary. Keep `--listen 127.0.0.1` when clients are local.

## Troubleshooting

```
WARN … Host Config file not able to be loaded: No such file or directory
```

Expected on a standalone gateway: no dynamic host-configuration file is configured. It is logged once at startup and does not affect operation.

If the gateway cannot connect, verify the target mount and extensions:

```bash
podman inspect <pg container> --format '{{range .Mounts}}{{.Destination}}{{"\n"}}{{end}}'
pg exec -i demo "SELECT extname FROM pg_extension WHERE extname LIKE 'documentdb%' OR extname = 'pg_cron'"
```
