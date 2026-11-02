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
  Endpoint:   mongodb://docdb_admin@127.0.0.1:10260/?directConnection=true
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

uri = f"mongodb://{user}:{password}@{host}:{port}/?directConnection=true"
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

Run it on the host where the add-on listens; from another machine set `MONGO_HOST` to the gateway host. For TLS-only mode add `&tls=true&tlsAllowInvalidCertificates=true` to the URI (the certificate is self-signed).

## TLS

The daemon always generates a self-signed certificate on first start and persists it in the named volume `pgcli-documentdb-tls-<name>`, so the certificate survives container recreation.

- `--tls-mode allow` (default): the same port accepts plaintext and TLS.
- `--tls-mode require`: `EnforceTls=true` in `SetupConfiguration.json`; plaintext handshakes are rejected.

Switch modes with a recreate:

```bash
pg addon install documentdb --pg-name demo --tls-mode require --force
```

With `require`, use `tls=true&tlsAllowInvalidCertificates=true` (or import the generated CA) on the client side.

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

- Local PG 18 instances only; standalone (not Patroni/HA); `--dsn` is unsupported.
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
