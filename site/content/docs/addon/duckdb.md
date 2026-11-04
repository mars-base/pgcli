---
title: "DuckDB"
description: "Install an embedded OLAP database as a managed host-binary addon."
weight: 61
---

[DuckDB](https://duckdb.org/) is an embedded analytical database — often called
"SQLite for analytics". It is a C++ columnar engine with vectorised execution
that queries Parquet, CSV, JSON and Arrow files directly, without a server
process. Think of it as a single-binary OLAP engine you point at your data:
`SELECT * FROM read_parquet('s3://bucket/data.parquet')` works out of the box
once the `httpfs` extension is loaded.

As a pgcli addon, DuckDB is a **host-binary** addon: there is no container and
no daemon. `pg addon install duckdb` downloads the official CLI zip from GitHub
releases (sha256-verified; a `duckdb` already on `PATH` is reused), installs
the `httpfs` extension (parquet and json are bundled in the binary), and
records the entry in `pg.yaml`.

> **Platform**: Linux only, amd64 only (macOS and other architectures refuse
> with a clear error). DuckDB is a standalone CLI tool — it does not run as a
> background service, does not listen on a port, and does not propagate into
> running PG instance containers.

## Why DuckDB alongside pgcli

DuckDB fills the analytical gap that PostgreSQL's row-oriented engine does not
target: ad-hoc queries over large Parquet/CSV datasets, data-lake exploration,
ETL pre-processing before loading into PostgreSQL, or quick aggregations on
files that already live in S3. pgcli already manages S3 stores (MinIO, silo,
rustfs, garage, seaweedfs) — DuckDB queries their contents directly via
`httpfs`.

## Usage

```bash
# Default: v1.5.6 LTS, install httpfs extension (parquet/json are bundled)
pg addon install duckdb

# Pin a specific version
pg addon install duckdb --version 2.0.0

# Custom name and data directory
pg addon install duckdb --name analytics --data-dir /srv/duckdb

# Skip extension installation
pg addon install duckdb --no-extensions

# Re-install with different flags (entry already exists)
pg addon install duckdb --version 2.0.0 --force
```

Once installed, launch the interactive shell or run one-shot queries:

```bash
# Interactive shell (data dir is set as the working directory)
pg addon duckdb shell

# One-shot query against a local Parquet file
pg addon duckdb shell -- -c "SELECT * FROM read_parquet('/tmp/data.parquet')"

# Query S3 directly (needs the httpfs extension, installed by default)
duckdb -c "SELECT count(*) FROM read_parquet('s3://bucket/data/*.parquet')"

# Open a persistent database file
duckdb ~/.pgcli/addon/duckdb/duckdb/analytics.duckdb
```

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `duckdb` | Instance name (config map key) |
| `--version` | `1.5.6` | Release to download (its sha256 is pinned in pgcli; `1.5.6` LTS and `2.0.0` supported) |
| `--data-dir` | `<base>/addon/duckdb/<name>` | Default database directory for `.duckdb` files |
| `--install-dir` | `/usr/local/bin` | Where the host binary goes (falls back to `~/.local/bin` when not writable — keep it on `PATH`) |
| `--no-extensions` | off | Skip installing the `httpfs` extension (parquet and json are bundled in the binary) |
| `--force` | off | Rebuild the config entry from the flags |

## Daily operations

```bash
pg addon duckdb status --name duckdb        # binary version, path, data dir
pg addon duckdb shell --name duckdb         # interactive DuckDB session
pg addon duckdb shell -- -c "SELECT 1"      # one-shot query (args after --)
pg addon list                               # duckdb appears under "Analytics add-ons"
pg addon start duckdb                       # no-op (CLI tool, not a daemon)
pg addon stop  duckdb                       # no-op (CLI tool, not a daemon)
pg addon remove duckdb --name duckdb        # forget config; binary and data dir stay
pg addon remove duckdb --name duckdb --clean-data   # also deletes the data directory
```

`remove` never deletes the binary — it may be shared across multiple named
instances. Pass `--clean-data` to delete the data directory (database files);
without it, the directory is kept and reported.

## Server/Client mode (v2.0.0+)

DuckDB v2.0.0 introduced the **Quack protocol** for native server/client operation.
This allows DuckDB to serve its databases over HTTP, letting multiple clients
query the same instance.

### Starting a server

```bash
# Interactive server (stdin must stay open)
duckdb /path/to/db.duckdb -serve

# Background server (redirect stdin to keep alive)
nohup duckdb /path/to/db.duckdb -serve < /dev/zero > /tmp/duckdb-serve.log 2>&1 &

# Or use quack_serve() from SQL
duckdb -c "INSTALL quack; LOAD quack; CALL quack_serve(token='mytoken')"
```

The server listens on `localhost:9494` by default and prints an auto-generated
auth token. By default, it only accepts local connections — for remote access,
put it behind a TLS-terminating reverse proxy (see
[DuckDB docs](https://duckdb.org/docs/2.0/quack/setup/reverse_proxy.html)).

#### Cross-host access (0.0.0.0 binding)

To accept connections from any network interface, use `quack_serve()` with
explicit parameters instead of `-serve`:

```sql
-- Server: bind to all interfaces on port 9494
CALL quack_serve(
  listen_uri='quack:0.0.0.0:9494',
  allow_other_hostname=true,  -- required for non-localhost binding
  disable_ssl=true,            -- optional, default is HTTPS
  token='mytoken'
);
```

Three parameters are needed:
- `listen_uri='quack:0.0.0.0:9494'` — bind to all interfaces (not just localhost)
- `allow_other_hostname=true` — bypass the localhost-only restriction
- `disable_ssl=true` — use plain HTTP (default is HTTPS with auto-generated cert)

Clients connect using the server's IP address:

```bash
# Client connecting to remote server
duckdb -c "ATTACH 'quack:192.0.2.10:9494' AS qk (TOKEN 'mytoken', DISABLE_SSL true); CONNECT qk; SELECT * FROM t"
```

The client uses HTTP automatically only for `localhost`, `127.0.0.1` and `::1`;
any other host defaults to HTTPS. When the server was started with
`disable_ssl=true`, the client must pass `DISABLE_SSL true` as well (as above),
otherwise it fails with an SSL error.

> **Security warning**: Binding to `0.0.0.0` with `disable_ssl=true` exposes the
> database to the network without encryption. Use only on trusted networks or
> behind a firewall. For production, use a reverse proxy with TLS.

### Connecting to a server

```bash
# Using -connect flag (v2.0.0+)
duckdb -connect -c "SELECT * FROM t"

# Or via SQL (short form, localhost default)
duckdb -c "ATTACH 'quack:localhost' AS qk (TOKEN 'BAED...'); CONNECT qk; SELECT * FROM t"

# Explicit ip:port form (for remote or non-default port)
duckdb -c "ATTACH 'quack:127.0.0.1:9494' AS qk (TOKEN 'BAED...'); CONNECT qk; SELECT * FROM t"

# Plain-HTTP remote server
duckdb -c "ATTACH 'quack:192.0.2.10:9494' AS qk (TOKEN 'BAED...', DISABLE_SSL true); CONNECT qk; SELECT * FROM t"
```

The `-connect` flag auto-discovers the token from the server's output. For
manual connections, copy the token from the server's startup message.

While a `CONNECT` is active, unqualified SQL — including `read_csv` /
`read_parquet` with local paths — executes on the server, so files on the
client are not visible. Run `DISCONNECT` to return to the local database, or
skip `CONNECT` and use `qk.<table>` qualified names to mix local and remote
queries in one session.

### Serving S3/OSS data to remote clients

A common deployment keeps the data files in object storage (AWS S3, Alibaba
Cloud OSS, MinIO, …) and runs only the Quack server on the ECS instance. The
server reads the files through `httpfs`, so remote clients need neither S3
credentials nor a route to the bucket — they only query the server.

On the server, configure the S3 secret and expose a view, then start Quack in
the same session (the view resolves at query time):

```sql
CREATE SECRET (
  TYPE S3,
  ENDPOINT 'oss-cn-hangzhou.aliyuncs.com',  -- omit for AWS S3; use REGION instead
  KEY_ID '...',
  SECRET '...',
  USE_SSL true,
  URL_STYLE 'vhost'                         -- MinIO/silo/rustfs need 'path'
);

CREATE VIEW events AS
  SELECT * FROM read_parquet('s3://my-bucket/events/*.parquet');

CALL quack_serve(
  listen_uri = 'quack:0.0.0.0:9494',
  allow_other_hostname = true,
  token = 'my_token',
  ssl_cert_file = '/etc/duckdb/server.crt',  -- omit to use a generated cert
  ssl_key_file  = '/etc/duckdb/server.key'
);
```

If the server is launched non-interactively, keep stdin open:

```bash
( cat server.sql; tail -f /dev/null ) | duckdb
```

#### Managing the server with `pg daemon`

For a long-running server, let `pg daemon` own the process: it detaches it from
the terminal, redirects output to a log, and can start it at boot. DuckDB still
needs stdin to stay open, so wrap it in a small script that feeds the SQL and
then blocks:

```bash
cat > /srv/duckdb/quack-serve.sh <<'SH'
#!/bin/bash
{
  cat /srv/duckdb/server.sql
  exec tail -f /dev/null
} | duckdb
SH
chmod +x /srv/duckdb/quack-serve.sh

pg daemon add duckdb-serve --dir /srv/duckdb -- /bin/bash /srv/duckdb/quack-serve.sh
pg daemon start duckdb-serve
pg daemon status duckdb-serve
pg daemon logs duckdb-serve
pg daemon restart duckdb-serve
pg daemon stop duckdb-serve
```

`pg daemon` runs the wrapper in its own session/process group; `stop` signals
the whole group, so the wrapper, `tail` and `duckdb` terminate together. Logs
default to `<base>/daemon/duckdb-serve/daemon.log`.

To start the server at boot:

```bash
pg autostart enable --daemon --name duckdb-serve
```

Daemons start after instances and addons, so a same-host MinIO/silo/rustfs is
already listening when Quack starts. Use persistent paths for `server.sql` and
the wrapper (not `/tmp`) when enabling autostart.

Connect from the local machine. With TLS (recommended) no `DISABLE_SSL` is
needed:

```bash
duckdb -c "ATTACH 'quack:ecs.example.com:9494' AS qk (TOKEN 'my_token'); CONNECT qk; SELECT count(*) FROM events"
```

Without `CONNECT`, qualify the view (`SELECT count(*) FROM qk.events`); an
unqualified name is resolved in the local catalog and will not find the
server-side view. The `read_parquet` itself always runs on the server, and only
the server needs the S3 secret.

For a plain-HTTP test server add `DISABLE_SSL true` on the client, as shown
above, and add the server host to `NO_PROXY` when the client uses an HTTP
proxy.

The same pattern was tested end to end with MinIO as the object store: a
100,000-row Parquet file at `s3://ducktest/events/events.parquet` was exposed as
a server-side view, and a remote client returned the same row count and sum via
both `ATTACH … CONNECT` and `quack_query()`.

- The S3 secret stays in the serving session (or `~/.duckdbrc`); clients never
  need it.
- The token grants server-side SQL execution, not just access to the exposed
  view: a client can query any S3 object reachable with the server's
  credentials, and any local file the server process can read. Use a dedicated,
  least-privilege S3 key and keep the Quack port private.
- Keep the Quack port private (security group/firewall). Token auth does not
  replace network isolation.

#### Analyzing other files on the server

The view is only one entry point. Any SQL executed on the server can read other
objects with the server's S3 credentials, or files on the server's filesystem.
Use `quack_query` for one-off analysis without attaching:

```sql
-- Aggregate a CSV that is not part of any view
FROM quack_query(
  'quack:ecs.example.com:9494',
  'SELECT count(*) AS n, round(avg(score), 1) AS avg_score
     FROM read_csv_auto(''s3://my-bucket/csv/users.csv'', header = true)',
  token = 'my_token'
);

-- Join two object prefixes entirely on the server
FROM quack_query(
  'quack:ecs.example.com:9494',
  'SELECT count(*) FROM read_parquet(''s3://my-bucket/a/*.parquet'') a
     JOIN read_parquet(''s3://my-bucket/b/*.parquet'') b USING (id)',
  token = 'my_token'
);
```

With an attachment, route the session with `CONNECT` and run plain SQL, or stay
local and use the attached `query` macro:

```sql
ATTACH 'quack:ecs.example.com:9494' AS qk (TOKEN 'my_token');

CONNECT qk;
SELECT count(*) FROM read_parquet('s3://my-bucket/other/*.parquet');

-- Equivalent without CONNECT: the SQL string runs on the server
SELECT * FROM qk.query(
  'SELECT count(*) FROM read_parquet(''s3://my-bucket/other/*.parquet'')'
);
```

These examples assume HTTPS. For a plain-HTTP server add
`disable_ssl = true` to `quack_query` or `DISABLE_SSL true` to `ATTACH`.
Statements that execute locally use the client's own credentials — a local
`read_parquet` on `s3://...` fails without a client-side S3 secret.

### Calling over the HTTP API

Quack is an HTTP RPC protocol: clients `POST` to `/quack` with
`Authorization: Bearer <token>` and `Content-Type: application/vnd.duckdb`.
Request and response bodies use DuckDB's internal binary serialization (the
same format as the WAL), not JSON, so hand-written HTTP requests can check the
service but cannot easily run SQL.

```bash
# Protocol hint / liveness
curl http://192.0.2.10:9494/

# CORS preflight
curl -i -X OPTIONS http://192.0.2.10:9494/quack    # 204
```

There is no JSON/REST endpoint: a JSON body posted to `/quack` is treated as a
binary protocol frame and fails to deserialize. To run SQL over HTTP from
another language, use a DuckDB client library and `quack_query` (or
`quack_query_by_name` on an attached catalog), or put your own JSON service in
front of DuckDB.

For an API-style call without `ATTACH`, use the stateless `quack_query`
function — one request/response per query:

```sql
FROM quack_query(
  'quack:192.0.2.10:9494',
  'SELECT count(*) AS rows FROM events',
  token = 'mytoken',
  disable_ssl = true          -- omit for HTTPS servers
);
```

Any DuckDB client (CLI, Python, …) can execute this SQL. `quack_query` is the
practical HTTP API surface; for a long-lived session use `ATTACH` + `CONNECT`.

#### Python example

The Python client uses the same SQL surface; `duckdb` must be v2.0.0 or newer.
A 1.x package does not speak Quack. Install it with `uv`:

```bash
# Recommended: project virtualenv
uv venv .venv
source .venv/bin/activate
uv pip install 'duckdb>=2.0.0'

# Isolated target dir (does not touch the current environment)
uv pip install --target /tmp/duckpkg 'duckdb==2.0.0'
PYTHONPATH=/tmp/duckpkg python3 quack_demo.py

# One-off run without a persistent install
uv run --with 'duckdb>=2.0.0' python3 quack_demo.py
```

```python
import os
os.environ["NO_PROXY"] = "ecs.example.com"   # plain-HTTP server behind a proxy

import duckdb

con = duckdb.connect()

rows = con.execute("""
    FROM quack_query(
        'quack:ecs.example.com:9494',
        'SELECT count(*) AS rows, sum(amount)::BIGINT AS total FROM events',
        token = 'my_token',
        disable_ssl = true
    )
""").fetchall()
print(rows)

# Attach once, query the exposed view with a qualified name
con.execute("ATTACH 'quack:ecs.example.com:9494' AS qk (TOKEN 'my_token', DISABLE_SSL true)")
print(con.execute("SELECT count(*) FROM qk.events").fetchall())

# Stay local and run arbitrary SQL on the server via the query macro
print(con.execute("""
    SELECT * FROM qk.query(
        'SELECT count(*) FROM read_parquet(''s3://my-bucket/other/*.parquet'')'
    )
""").fetchall())
```

`fetchdf()` requires pandas; `fetchall()` works without it. Set `NO_PROXY`
before importing DuckDB when the server is plain HTTP and the environment has
an HTTP proxy configured.

### Key points

- **stdin must stay open**: the server exits when stdin closes (use `< /dev/zero` or `tail -f /dev/null |` for background operation, or manage it with `pg daemon`)
- **localhost by default**: bind to `0.0.0.0` with `allow_other_hostname=true` for remote access; prefer a TLS-terminating reverse proxy or `disable_ssl=false`
- **Auth token**: auto-generated, printed at startup, required for client connections
- **Not managed by pgcli**: `pg addon start/stop duckdb` remain no-ops — the Quack server is a standalone process you manage yourself
- **HTTP proxies**: the client honors `HTTP_PROXY`/`HTTPS_PROXY`. A plain-HTTP
  connection to a private host can be swallowed by a proxy and reported as
  `Unauthorized`; add the host to `NO_PROXY` (e.g. `NO_PROXY=192.0.2.10`)

## Extensions

DuckDB's extension system installs shared libraries on first use. pgcli
pre-installs `httpfs` at install time — it enables reading files over HTTP/S3.
`parquet` and `json` are statically linked into the DuckDB CLI binary, so they
are always available without a network download.

- **`httpfs`** — read files over HTTP/S3 (`read_parquet('s3://...')`)
- **`parquet`** — Parquet file reader/writer (bundled)
- **`json`** — JSON file reader (`read_json_auto(...)`) (bundled)

Additional extensions can be installed later from the DuckDB shell:

```sql
INSTALL 'spatial';       -- geospatial functions
INSTALL 'sqlite';        -- read/write SQLite databases
INSTALL 'mysql';         -- query MySQL directly
LOAD 'httpfs';           -- load an installed extension in the current session
```

Pass `--no-extensions` at install time if you prefer to manage extensions
manually, or if the host has no internet access at install time.

## Accessing S3, Parquet, JSON and CSV data

DuckDB is built for analytical queries over files — no server, no `COPY`, no
import step. Point it at your data.

### Parquet files

```sql
-- Single file
SELECT * FROM read_parquet('/tmp/events.parquet');

-- Wildcard: all parquet files in a directory
SELECT * FROM read_parquet('/data/logs/*.parquet');

-- Schema inference + count
SELECT count(*) FROM read_parquet('s3://bucket/year=2026/*.parquet');

-- Specific columns + filter
SELECT user_id, event, ts
FROM read_parquet('/data/events.parquet')
WHERE ts > '2026-01-01'
LIMIT 100;
```

### JSON files

```sql
-- Auto-detect schema (handles JSON lines and nested JSON)
SELECT * FROM read_json_auto('/tmp/users.json');

-- Explicit schema with read_json
SELECT * FROM read_json('/tmp/users.json',
    columns = {'id': 'INTEGER', 'name': 'VARCHAR', 'email': 'VARCHAR'},
    format = 'newline_delimited');

-- Nested objects: extract fields with ->>
SELECT
    data->>'user_id' AS user_id,
    data->>'event'   AS event
FROM read_json_auto('/tmp/events.jsonl');
```

### CSV files

```sql
-- Auto-detect header and types
SELECT * FROM read_csv_auto('/tmp/sales.csv');

-- Explicit column types (when auto-detection guesses wrong)
SELECT * FROM read_csv('/tmp/sales.csv',
    columns = {'id': 'INTEGER', 'amount': 'DOUBLE', 'date': 'DATE'},
    header = true);

-- TSV or custom delimiter
SELECT * FROM read_csv_auto('/tmp/data.tsv', delim = '\t');
```

### S3 access (httpfs extension)

The `httpfs` extension enables S3 access. Credentials and endpoint are configured
via `CREATE SECRET` (v2.0.0+) or legacy `SET` commands (v1.5.x).

#### DuckDB v2.0.0+ — `CREATE SECRET`

```sql
-- Configure S3 credentials (v2.0.0+ syntax)
CREATE SECRET (
  TYPE S3,
  ENDPOINT 'minio.example.com:9000',
  KEY_ID 'admin',
  SECRET 'your-secret-key',
  USE_SSL false,
  URL_STYLE 'path'   -- required for MinIO and compatible stores
);

-- Query Parquet directly from S3
SELECT count(*) FROM read_parquet('s3://my-bucket/events/2026/*.parquet');

-- Query CSV from S3
SELECT * FROM read_csv_auto('s3://my-bucket/users.csv') LIMIT 10;

-- Query JSON from S3
SELECT * FROM read_json_auto('s3://my-bucket/config.json');

-- Write Parquet back to S3 (needs write permissions on the bucket)
COPY (SELECT * FROM read_parquet('/tmp/*.parquet') WHERE year = 2026)
  TO 's3://output-bucket/filtered/2026.parquet' (FORMAT parquet);
```

#### DuckDB v1.5.x — legacy `SET` commands

```sql
-- Configure S3 credentials in the DuckDB session (v1.5.x only)
SET s3_endpoint = 'minio.example.com:9000';
SET s3_access_key_id = 'admin';
SET s3_secret_access_key = 'your-secret-key';
SET s3_use_ssl = false;   -- true for https endpoints
SET s3_region = 'us-east-1';   -- 'us-east-1' works for MinIO (region is ignored)
SET s3_url_style = 'path';   -- MinIO and compatible stores use path-style
```

#### Connecting to pgcli-managed S3 stores

pgcli manages several S3-compatible stores (MinIO, silo, rustfs, garage,
seaweedfs). DuckDB queries them directly — the `s3_endpoint` is the store's
HTTP address, and credentials come from `pg addon password`.

First, create an S3 store instance and a bucket. The setup is identical across
MinIO, silo (Pigsty's MinIO fork), and rustfs (Rust reimplementation) — they
all share the same `pg mc` wrapper and the same DuckDB `s3_url_style = 'path'`:

```bash
# MinIO
pg addon install minio --name ms3
S3_SECRET=$(pg addon password minio --name ms3)

# or silo (same API surface, shares the port pool with minio)
pg addon install silo --name ms3
S3_SECRET=$(pg addon password silo --name ms3)

# or rustfs (Rust S3-compatible store, Linux only)
pg addon install rustfs --name ms3
S3_SECRET=$(pg addon password rustfs --name ms3)

# Create a bucket via the pg mc wrapper
pg mc alias set ms3 http://127.0.0.1:9000 admin "${S3_SECRET}"
pg mc mb ms3/my-bucket

# Upload test data (optional)
pg mc cp /tmp/data.parquet ms3/my-bucket/data.parquet
```

Then query it from DuckDB — note `URL_STYLE 'path'` is required for
MinIO, silo, rustfs and compatible stores (they do not support virtual-host style):

```bash
# DuckDB v2.0.0+
duckdb -c "
  CREATE SECRET (
    TYPE S3,
    ENDPOINT '127.0.0.1:9000',
    KEY_ID 'admin',
    SECRET '${S3_SECRET}',
    USE_SSL false,
    URL_STYLE 'path'
  );
  SELECT count(*) FROM read_parquet('s3://my-bucket/data/*.parquet');
"

# DuckDB v1.5.x (legacy SET syntax)
duckdb -c "
  SET s3_endpoint = '127.0.0.1:9000';
  SET s3_access_key_id = 'admin';
  SET s3_secret_access_key = '${S3_SECRET}';
  SET s3_use_ssl = false;
  SET s3_url_style = 'path';
  SELECT count(*) FROM read_parquet('s3://my-bucket/data/*.parquet');
"
```

For a more permanent setup, create a DuckDB startup file (`~/.duckdbrc`) with
the `CREATE SECRET` (v2.0.0+) or `SET` (v1.5.x) commands — they run on every
session start:

```sql
-- ~/.duckdbrc (DuckDB v2.0.0+)
CREATE SECRET (
  TYPE S3,
  ENDPOINT 'minio.example.com:9000',
  KEY_ID 'admin',
  SECRET 'your-secret-key',
  USE_SSL false,
  URL_STYLE 'path'
);
```

### Environment variables for S3

DuckDB reads endpoint and SSL settings from `DUCKDB_S3_*` environment variables,
but credentials must use standard AWS variables (`AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`) — DuckDB's `enable_global_s3_configuration` auto-detects these.

> **v2.0.0 limitation**: `URL_STYLE` cannot be set via environment variables.
> It defaults to virtual-host, which breaks MinIO/silo/rustfs (they require path-style).
> For these stores, use `CREATE SECRET` with `URL_STYLE 'path'` instead of env vars.

```bash
# Endpoint and SSL via DUCKDB_S3_*
export DUCKDB_S3_ENDPOINT='127.0.0.1:9000'
export DUCKDB_S3_USE_SSL='false'

# Credentials via AWS_* (standard variables, auto-detected by DuckDB)
export AWS_ACCESS_KEY_ID='admin'
export AWS_SECRET_ACCESS_KEY='your-secret-key'

# URL style must still be set in the session or startup file (v1.5.x)
duckdb -c "SET s3_url_style = 'path'; SELECT * FROM read_parquet('s3://my-bucket/data.parquet');"

# For v2.0.0, URL_STYLE must be set via CREATE SECRET — env vars alone are insufficient
# for MinIO/silo/rustfs (virtual-host style is not supported by these stores)
duckdb -c "
  CREATE SECRET (TYPE S3, ENDPOINT '127.0.0.1:9000', KEY_ID 'admin',
    SECRET 'your-secret-key', USE_SSL false, URL_STYLE 'path');
  SELECT * FROM read_parquet('s3://my-bucket/data.parquet');
"
```

For MinIO/silo/rustfs, you still need `s3_url_style = 'path'` because these
stores don't support virtual-host style URLs.

