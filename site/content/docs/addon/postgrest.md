---
title: "PostgREST"
description: "Expose a PostgreSQL schema as a REST API via the PostgREST addon"
weight: 46
---

PostgREST is a single-process, stateless web server that turns a PostgreSQL
schema into a RESTful API. As a pgcli addon it runs as one container in front
of any PG endpoint — no data directory, no rendered config file, everything
configured through `PGRST_*` environment variables.

PostgREST is a **proxy-type** addon like [PgBouncer](../pgbouncer/) and
[PgDog](../pgdog/): it holds no state of its own. **Two deployment modes**
mirror PgBouncer's:

- **Local:** `pg addon install postgrest -i <instance>` — stored as a sidecar
  under `instances.<name>.addons.postgrest`, DSN built from the instance.
- **Remote:** `pg addon install postgrest --dsn <dsn> --pg-name <name>` —
  stored in top-level `addons.postgrest`.

The `--dsn` in either mode is passed verbatim into the container's
`PGRST_DB_URI`, so it works against **any** PG endpoint: a direct managed
instance, a [PgBouncer](../pgbouncer/) pool, or a Patroni cluster behind its
[HAProxy](../haproxy/) listener.

## Platform support

PostgREST works on **Linux** (host networking, `--network host`) and
**macOS** (container joins the `pgcli-net` bridge and publishes its port, the
same shape as a PG instance under podman machine).

- **Local mode** on macOS just works — the container reaches the managed
  instance over the bridge.
- **Remote mode** on macOS: `--dsn` must be reachable from the Mac itself,
  not from the podman machine VM. Point it at an address the Mac can resolve
  (a real host IP, or `host.containers.internal` for a service on the VM).
- Clients always reach the REST API at `127.0.0.1:<port>` (or `--listen`);
  gvproxy forwards the published port to the Mac's loopback.

## How it works

1. **`pg addon install postgrest`** pulls the image (if missing) and starts
   a container wired entirely from `PGRST_*` env vars.
2. The container reaches the backend PG over the host network (Linux) or the
   `pgcli-net` bridge (macOS).
3. PostgREST introspects the exposed schema on startup, serves REST requests,
   and listens on the `pgrst` channel for a schema-cache reload signal.

PostgREST holds **no data** on the host — `pg addon remove postgrest` only
stops and removes the container.

**Container name:** `pgcli-postgrest<ns>-<name>` (local mode uses the instance
name as `<name>`; remote mode uses `--pg-name`). Namespace isolation applies:
two configs with different namespaces can run independent APIs without
colliding.

## Commands

### Install

```bash
# Local mode: expose a managed instance's schema
pg addon install postgrest -i mypg --schema api --anon-role web_anon

# Remote mode: any PG endpoint (direct instance, pgbouncer, or haproxy LB)
pg addon install postgrest \
  --dsn "postgres://api:pass@127.0.0.1:5000/appdb" \
  --pg-name app-api --schema api --anon-role web_anon

# Tune the connection pool PostgREST keeps toward its backend
pg addon install postgrest -i mypg --db-pool 20

# Enable JWT auth (unauthenticated requests still fall back to --anon-role)
pg addon install postgrest -i mypg --schema api --anon-role web_anon --jwt-secret "$(openssl rand -hex 32)"
```

Re-running install is idempotent: an existing container is reused (a stopped
one is started). Pass `--force` to recreate it after changing ports, listen
address, DSN, db-pool, schema, anon-role, or jwt-secret.

**Parameters:**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `-i`, `--instance` | Managed instance (local mode) | `default` |
| `--dsn` | Backend PG URI (remote mode); → `PGRST_DB_URI` verbatim | — |
| `--pg-name` | Name to identify a remote PostgREST (required with `--dsn`) | — |
| `--schema` | Exposed schema(s), comma-separated; → `PGRST_DB_SCHEMAS` | PostgREST default (`public`) |
| `--db-pool` | Connections in PostgREST's pool toward the backend; → `PGRST_DB_POOL` | PostgREST default (`10`) |
| `--anon-role` | Role unauthenticated requests run as; → `PGRST_DB_ANON_ROLE` | — (anonymous access off) |
| `--jwt-secret` | Secret used to verify `Authorization: Bearer` JWTs; → `PGRST_JWT_SECRET` | — (JWT auth off) |
| `--port` | HTTP host port | auto, from `postgrest_start_port` (base 3500) |
| `--listen` | Bind address | `127.0.0.1` |
| `--image` | Container image | `docker.io/postgrest/postgrest:v16.3` |
| `--force` | Recreate an existing container to apply changed flags | off |

> **`--db-pool` and the backend's `max_connections`.** Total PG connections
> a PostgREST deployment opens is roughly *number of PostgREST instances ×
> `--db-pool`*. If you run several replicas behind the same endpoint, budget
> `max_connections` accordingly.

### List

```bash
pg addon list
```

PostgREST appears under **Local add-ons** / **Remote add-ons** with Status,
REST URL, Backend, Schema, DB pool, Anon role, JWT auth, and Container.

### Start / stop / remove / logs

```bash
pg addon start postgrest -i mypg
pg addon stop postgrest --pg-name app-api
pg addon remove postgrest -i mypg          # stateless: only the container goes away
pg addon remove postgrest --pg-name app-api
pg logs addon postgrest -i mypg            # local container logs
pg logs addon postgrest --pg-name app-api  # remote container logs
```

### Autostart

```bash
pg autostart enable --postgrest -i mypg
pg autostart enable --postgrest --pg-name app-api
pg autostart status
```

Autostart is start-only: at boot the container is brought up reading the
`PGRST_*` env already in the config (recreated from config if it was removed).
Install the addon first.

## Configuration

PostgREST settings live under `addons.postgrest` (remote) or as an
`instances.<name>.addons.postgrest` sidecar (local):

```yaml
postgrest_start_port: 3500      # base of the HTTP port pool

addons:
  postgrest:
    app-api:
      container_name: pgcli-postgrest-default-app-api
      name: app-api
      image_tag: docker.io/postgrest/postgrest:v16.3
      host_port: 3501
      listen: 127.0.0.1
      dsn: postgres://api:pass@10.0.0.20:35432/appdb
      backend_host: 10.0.0.20:35432
      db_pool: 4
      schemas: api
      anon_role: web_anon
      jwt_secret: <HS256-shared-secret>   # only present when --jwt-secret was given
      autostart: false
```

Ports are auto-assigned from the `postgrest_start_port` pool (base 3500) when
`--port` is omitted; the pool is independent of the PgBouncer / etcd / pgdog /
minio pools.

## Connecting to a Patroni cluster

Point `--dsn` at the cluster's **HAProxy listener**, not at an individual
member's direct PG port.

```bash
# Good: writes follow the leader through the LB's rw listener
pg addon install postgrest --dsn "postgres://api:pass@<lb-host>:5000/appdb" \
  --pg-name app-api --schema api --anon-role web_anon
```

A member's direct port loses writes after a failover (the old leader stops
accepting them, but the DSN keeps pointing there). pgcli detects this at
install time and warns, suggesting the HAProxy listener instead.

- **Failover self-heal.** When the backend leader changes, PostgREST's
  reconnect-and-reload cycle (it retries the connection and reloads the schema
  cache) re-establishes service through the LB without any pgcli action.
- **Scale-out.** Run several PostgREST replicas behind a load balancer; each
  adds its own `--db-pool` worth of backend connections.

## Database-side setup (not managed by pgcli)

pgcli installs and runs the PostgREST container; it **does not** touch your
database. Before the API serves anything, the database needs the unauthenticated
role PostgREST `SET ROLE`s to, plus grants on the exposed schema — usually owned
by your migrations, not pgcli. This block is re-runnable:

```sql
-- The exposed schema (--schema value; skip for public, which already exists):
CREATE SCHEMA IF NOT EXISTS api;

-- The NOINHERIT role unauthenticated requests run as (the --anon-role value).
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'web_anon') THEN
    CREATE ROLE web_anon NOINHERIT NOLOGIN;
  END IF;
END $$;
GRANT USAGE ON SCHEMA api TO web_anon;
GRANT SELECT ON ALL TABLES IN SCHEMA api TO web_anon;
ALTER DEFAULT PRIVILEGES IN SCHEMA api GRANT SELECT ON TABLES TO web_anon;
```

> The role view's column is `rolname`, not `rolename` — a typo fails the whole
> batch. `ALTER DEFAULT PRIVILEGES` covers only tables created **after** it
> runs; existing ones are handled by the `GRANT ... ON ALL TABLES` line.

PostgREST connects as the DSN user and `SET ROLE`s to `web_anon` per request, so
that role needs the read grants; the connecting login role itself can stay
narrow. Then install with `--anon-role web_anon`. The two ways to authenticate a
request — anonymous (`--anon-role`) and JWT (`--jwt-secret`) — are covered next;
without either, every request is refused with 401.

PostgREST caches the schema it introspected. After a schema change, reload the
cache:

```sql
NOTIFY pgrst, 'reload schema';
```

> **`LISTEN` and transaction pooling.** PostgREST relies on a persistent
> `LISTEN pgrst` session to receive reload notifications. Behind a pooler in
> **transaction** pooling mode (PgBouncer's default) that session-level LISTEN
> is broken — the notification is never delivered, and neither is
> `NOTIFY pgrst` reaching PostgREST via a pooled connection. Verified
> behaviour: a reload sent through a transaction-pooled PgBouncer does **not**
> refresh PostgREST's cache (a fresh table stays 404), and even a `NOTIFY`
> sent directly to the backend is lost because PostgREST's own listener has no
> stable connection. Either point PostgREST at a **session**-pooled or direct
> connection, or **restart the PostgREST container** (it re-introspects on
> boot) to pick up schema changes.

## Verifying the API

The install prints the REST URL (`http://127.0.0.1:<port>`). Create a table in
the exposed schema, then hit the API to confirm the server is live and serves
its rows:

```sql
CREATE TABLE api.widgets (id integer PRIMARY KEY, name text);
INSERT INTO api.widgets VALUES (1, 'bolt'), (2, 'nut');
GRANT SELECT ON api.widgets TO web_anon;
NOTIFY pgrst, 'reload schema';   -- otherwise the new table stays 404
```

```bash
# The OpenAPI root — answers as soon as PostgREST has connected to the backend.
curl -s http://127.0.0.1:3500/ | head -c 120

# Which tables/relations are exposed (from the OpenAPI paths):
curl -s http://127.0.0.1:3500/ | grep -o '"/[a-z_]*"'

# Read rows from the table. The schema is implicit in the path — it is
# /widgets, NOT /api.widgets:
curl -s "http://127.0.0.1:3500/widgets"
curl -s "http://127.0.0.1:3500/widgets?id=eq.1"   # a filter
```

A few things to expect on a fresh install:

- The root can take a moment to answer — schema introspection runs after boot,
  so the first requests may `503` until PostgREST has connected.
- A `404` on a just-created table or a `401` on unauthenticated requests means
  the schema cache is stale or `--anon-role` is unset — see the
  [Troubleshooting](#troubleshooting) table below for the fix.

## JWT authentication

`--anon-role` is the simplest model: every unauthenticated request runs as one
fixed role. `--jwt-secret` turns on per-request identity. Pass it at install
time (a HS256 shared secret, or a JSON Web Key for RS256); pgcli passes it to
the container as `PGRST_JWT_SECRET`.

```bash
pg addon install postgrest -i mypg --schema api --anon-role web_anon \
  --jwt-secret "$(openssl rand -hex 32)"
```

> The secret is **not** written to your shell history if you inline a command
> substitution as shown. It does land in `pg.yaml` (so pgcli can recreate the
> container) — treat that file as secret-bearing, and re-run install with
> `--force` after changing it.

> **Length:** for HS256 the secret must be **at least 32 characters** (256-bit
> key material; 48 for HS384, 64 for HS512). PostgREST refuses to start with a
> shorter one — `openssl rand -hex 32` (64 chars) is a safe default. A JWK (for
> RS256/ECDSA) has no such minimum; the key strength comes from the JWK itself.

With a secret set, PostgREST verifies any request that carries
`Authorization: Bearer <token>` and runs it as the role named in the token's
**`role`** claim:

- The token must be signed with the same secret; a tampered one is rejected 401.
- The `role` claim must name a database role with grants on the exposed schema
  (create it like `web_anon` above, `NOINHERIT`).
- Change the claim key from `role` via `PGRST_JWT_ROLE_CLAIM_KEY` if your issuer
  uses another field — pgcli does not surface that flag; set it on the container
  directly if needed.

`--anon-role` and `--jwt-secret` compose: requests **with** a valid JWT run as
their `role` claim, requests **without** one fall back to `--anon-role`. With
neither flag, every request is refused (401). A typical progression is anon for
public reads plus JWT roles for authenticated writes.

In production your application's auth service signs the tokens. To hand-craft a
test token with the same HS256 secret:

```bash
b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
SECRET='<the --jwt-secret value>'
HEADER=$(printf '{"alg":"HS256","typ":"JWT"}' | b64url)
PAYLOAD=$(printf '{"role":"web_user","exp":%d}' $(( $(date +%s) + 3600 )) | b64url)
SIG=$(printf '%s.%s' "$HEADER" "$PAYLOAD" | openssl dgst -sha256 -hmac "$SECRET" -binary | b64url)
curl -s -H "Authorization: Bearer $HEADER.$PAYLOAD.$SIG" http://127.0.0.1:3500/widgets
```

## Troubleshooting

| Symptom | Likely cause / fix |
|---------|--------------------|
| Install fails with `cannot connect to source database` | The DSN host:port is unreachable from the container (on macOS, remote `127.0.0.1` points at the Mac, not the VM). Verify with `pg exec --dsn <dsn> "SELECT 1"`. |
| Every request returns HTTP 401 `Anonymous access is disabled` | No `--anon-role` was given and the request carried no valid JWT — add `--anon-role`, or install with `--jwt-secret` and send a signed token. A JWT 401 with `--jwt-secret` set means the signature/`role` claim is wrong. |
| A brand-new table/relationship still returns 404/`PGRST205` after `NOTIFY pgrst` | Reload is not reaching PostgREST (see **transaction pooling** above). Restart the container, or use a session/direct connection. |
| Writes fail right after a Patroni failover | The DSN pointed at a member's direct port, not the HAProxy rw listener — install warned about this. Re-point the DSN at the LB. |
| `--db-pool` change didn't take effect | Existing containers are reused; re-run install with `--force` to recreate. |

## Related

- [PgBouncer](../pgbouncer/) — connection pooler (watch the transaction-pooling caveat above when pooling PostgREST's backend).
- [PgDog](../pgdog/) — pooling / sharding proxy, same dual-mode shape.
- [HAProxy](../haproxy/) — the listener PostgREST's DSN should target for a Patroni cluster.
- [HA Cluster](../../ha-cluster/) — Patroni topology that `--dsn` points at.
