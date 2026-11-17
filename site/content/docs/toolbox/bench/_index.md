---
title: "Benchmarking"
description: "Run pgbench load tests in a one-shot container against local pgcli instances or remote PostgreSQL"
weight: 65
icon: fa-solid fa-gauge-high
aliases: ["/toolbox/bench/"]
menus:
  main:
    identifier: docs-toolbox-bench
    parent: docs-toolbox
    weight: 65
    params:
      icon: fa-solid fa-gauge-high
cascade:
  type: docs
  footer_style: slim
---

`pg bench` runs [pgbench](https://www.postgresql.org/docs/current/pgbench.html)
inside a disposable container. You do not need pgbench or a PostgreSQL client
installed on the host, and no benchmark service stays running afterwards.

The target can be a local pgcli instance (global `-i/--instance`) or any
reachable database (`--dsn`). PostgreSQL's client tools are included in the
official `postgres` image and in pgcli's own `pgcli-pg` image, so the default
image already has everything needed.

## Quick start

For a one-off test, `--cleanup` runs the whole lifecycle in one command: it
initializes the pgbench tables, runs the benchmark, then drops those tables.

```bash
# Local pgcli instance
pg bench -i proj01 --cleanup

# Remote database
pg bench --dsn 'postgres://user:pass@10.0.0.8:5432/appdb' --cleanup
```

The database itself is kept; only pgbench's own tables are dropped. To keep the
benchmark data between runs, initialize once and then run without `--cleanup`:

```bash
pg bench -i proj01 --init --scale 10
pg bench -i proj01
```

When `--cleanup` is not used, the command prints the `DROP TABLE` statement for
the pgbench tables after the run, so the data can be removed later.

Defaults are 16 clients, 4 threads, 60 seconds, the `prepared` protocol and a
progress report every 10 seconds.

`--init` runs `pgbench -i`, which drops and recreates pgbench's own
`pgbench_accounts`, `pgbench_branches`, `pgbench_history` and
`pgbench_tellers` tables in the benchmark database. It does not touch other
tables and never drops the database. `--cleanup` performs the same
initialization and drops those four tables after the test.

## Dedicated benchmark database

Before running, pgcli checks whether the supplied account is a superuser or has
`CREATEDB`:

- **Allowed to create databases** — use `bench_db` (configurable with
  `--bench-db`). If it does not exist, pgcli creates it; if it exists, it is
  reused. The database is never dropped automatically. With `--cleanup`, the
  pgbench tables are dropped at the end, but the database remains.
- **Not allowed** — stay on the database from the instance configuration or
  the DSN, and print why.

The permission probe runs in a one-shot `psql` container. Local instances probe
through the `postgres` database; DSN targets probe through the database named in
the DSN. Credentials are passed as `PG*` environment variables, not command-line
arguments.

When a benchmark database is newly created in run mode (without `--init` or
`--cleanup`), `pg bench` stops and asks you to initialize it first:

```text
benchmark database "bench_db" was created but not initialized; run "pg bench --init" first
```

`--cleanup` combines initialization, the test and table cleanup. It accepts
both init and run flags, and may be combined with `--init`; initialization
happens either way. Cleanup is attempted even when pgbench fails, and only the
four `pgbench_*` tables are dropped.

Use `--no-auto-db` to skip the probe and stay on the target database, and
`--bench-db NAME` to choose a different dedicated database:

```bash
pg bench -i proj01 --no-auto-db
pg bench -i proj01 --bench-db loadtest
```

## Connection targets

### Local pgcli instance

```bash
pg bench -i proj01 --init
pg bench -i proj01 -T 30
```

The command refuses to run when the instance container is stopped.

### Remote database with `--dsn`

```bash
pg bench --dsn 'postgres://user:pass@db.example.com:5432/appdb' --init
pg bench --dsn 'postgres://user:pass@db.example.com:5432/appdb' --clients 32 -T 120
```

The DSN must use the `postgres://` or `postgresql://` scheme and include a
host, user and database; the port defaults to 5432 and the password may be
omitted. Multiple hosts, fragments and opaque DSNs are not supported. Use
percent-encoding for special characters in the password:

```bash
pg bench --dsn 'postgres://user:p%40ssword@db.example.com:5432/appdb'
```

`--dsn` and an explicitly supplied `-i/--instance` are mutually exclusive.

#### Supported query parameters

Only the following DSN query parameters are accepted. They are mapped to libpq
environment variables and passed to both the control `psql` and pgbench:

| DSN parameter | Environment variable |
|---------------|----------------------|
| `sslmode` | `PGSSLMODE` |
| `sslrootcert` | `PGSSLROOTCERT` |
| `sslcert` | `PGSSLCERT` |
| `sslkey` | `PGSSLKEY` |
| `sslcrl` | `PGSSLCRL` |
| `sslcrldir` | `PGSSLCRLDIR` |
| `sslpassword` | `PGSSLPASSWORD` |
| `connect_timeout` | `PGCONNECT_TIMEOUT` |
| `application_name` | `PGAPPNAME` |
| `fallback_application_name` | `PGFALLBACKAPPNAME` |
| `options` | `PGOPTIONS` |
| `target_session_attrs` | `PGTARGETSESSIONATTRS` |
| `channel_binding` | `PGCHANNELBINDING` |
| `client_encoding` | `PGCLIENTENCODING` |
| `gssencmode` | `PGGSSENCMODE` |
| `sslnegotiation` | `PGSSLNEGOTIATION` |
| `ssl_min_protocol_version` | `PGSSLMINPROTOCOLVERSION` |
| `ssl_max_protocol_version` | `PGSSLMAXPROTOCOLVERSION` |

Connection fields (`host`, `port`, `user`, `password`, `dbname`, `service`,
`passfile`, `replication`) must be expressed in the URL itself, not in the
query string.

TLS certificate paths are resolved to absolute host paths, mounted read-only
under `/pgcli-bench/tls/`, and the corresponding environment variable points to
the container path. `sslrootcert=system` is passed through unchanged.

### Client image version

The default client image comes from `image_tag` in the pgcli configuration
(`pgcli-pg`, based on PostgreSQL 18). For a remote server with a different major
version, select a matching image explicitly:

```bash
pg bench --dsn 'postgres://user:pass@db:5432/appdb' --image docker.io/library/postgres:17 -T 60
```

pgbench itself usually works across minor versions, but matching the major
version avoids protocol and catalog surprises.

## Run modes and scripts

By default pgbench runs the TPC-B-like built-in script with the extended
protocol in prepared mode:

```bash
pg bench -i proj01
```

Common variations:

```bash
# Read-only workload
pg bench -i proj01 -S -T 60

# Simple-update workload (no pgbench_accounts updates)
pg bench -i proj01 --simple-update

# Fixed number of transactions per client instead of a duration
pg bench -i proj01 --clients 8 --transactions 1000

# Rate-limited run with a latency limit
pg bench -i proj01 --rate 500 --latency-limit 20 -T 60

# Weighted mix of built-in scripts
pg bench -i proj01 -b select-only@3 -b tpcb-like@1 -T 60
```

`--time` and `--transactions` are mutually exclusive. `--select-only` and
`--simple-update` are also mutually exclusive. `--latency-limit` requires
`--rate`.

### Custom scripts

Custom scripts live on the host and are mounted read-only into the container:

```bash
pg bench -i proj01 -f ./bench.sql -T 60
pg bench -i proj01 -f ./bench.sql@2 -f ./other.sql@1 -T 60
```

The optional `@weight` suffix follows pgbench semantics. Script variables use
`-D`:

```bash
pg bench -i proj01 -f ./bench.sql -D scale=5 -D clients=16 -T 60
```

`-D` values are placed in the container command line; do not use them for
passwords or other secrets.

## Logs

pgbench per-transaction logs are written to a host directory:

```bash
pg bench -i proj01 --log --log-dir ./pgbench-logs -T 30
```

The directory is created if needed and mounted read-write at
`/pgcli-bench/log`. pgbench writes `pgbench_log.<pid>` (one file) or
`pgbench_log.<pid>.<thread>` (multiple threads) inside it. After pgbench exits,
the command prints the host directory and each log file written by that run.
The files are owned by root with rootful Podman or by your user with rootless
Podman.

## Passing raw pgbench options

Options after `--` are appended unchanged to the pgbench command:

```bash
pg bench -i proj01 -- --sampling-rate 0.1
```

The following raw options are rejected because they would bypass pgcli's
target, script or log handling:

| Rejected option | Use instead |
|-----------------|-------------|
| `-h`, `--host` | `--dsn` or `-i` |
| `-p`, `--port` | `--dsn` or `-i` |
| `-U`, `--username` | `--dsn` or `-i` |
| `-d`, `--dbname` | `--dsn` or `-i` |
| `-i`, `--initialize` | `--init` |
| `-f`, `--file` | `--file` |
| `-l`, `--log` | `--log` |

`--host=x`, abbreviated long options such as `--hos`, and clustered short
options are checked as well. Positional arguments before `--` are rejected;
instance selection must use `-i/--instance`.

## Security notes

- Passwords are passed through the `PGPASSWORD` environment variable. They do
  not appear in the `podman run` arguments, so they are not visible in `ps`.
  They are visible in the container's environment via `podman inspect`, as with
  any container environment variable.
- The `--dsn` value you type is part of the `pg bench` process arguments and is
  therefore visible to `ps`. The podman client containers still receive only
  the `PG*` environment variables, not the DSN.
- TLS certificate and key files are mounted read-only into the container.
- Credentials and sensitive values passed with `-D` appear in the container
  command line. Avoid `-D` for secrets.
- The benchmark tables inherit the target database's permissions. Use a
  dedicated account when running against shared infrastructure.

## Limitations

- The client container uses `--network host`, so the host must be Linux. On
  macOS, host networking in Podman does not reach the host network namespace in
  the same way.
- When the client and server run on the same machine, they compete for CPU,
  memory and disk I/O. Results reflect that contention. For clean numbers, run
  `pg bench` from a separate machine against the database host.
- The image must contain `pgbench`; the command reports exit code 126/127 and
  asks for `--image` when it does not.
- `--init` is required once per benchmark database and scale. Re-running it
  recreates only pgbench's own tables.

## Flags

| Flag | Default | Mode | Description |
|------|---------|------|-------------|
| `--dsn URL` | empty | all | Remote target; mutually exclusive with an explicit `-i` |
| `-i, --instance NAME` | `default` | all | Local pgcli instance (global flag) |
| `--image IMAGE` | config `image_tag` | all | Client image containing pgbench |
| `--bench-db NAME` | `bench_db` | all | Dedicated benchmark database name |
| `--no-auto-db` | false | all | Do not probe/create a dedicated database |
| `--init` | false | mode | Initialize pgbench tables |
| `--cleanup` | false | mode | Initialize, run, then drop the pgbench tables; keeps the database |
| `--scale N` | 1 | init | pgbench scale factor |
| `--partitions N` | 0 | init | Number of partitions |
| `--partition-method METHOD` | empty | init | `range` or `hash` |
| `--fillfactor N` | 0 | init | Table fillfactor (10–100) |
| `--foreign-keys` | false | init | Add foreign keys |
| `--no-vacuum` | false | all | Skip vacuum on init; pass `-n` on run |
| `--clients N` | 16 | run | Concurrent clients |
| `-j, --threads N` | 4 | run | Worker threads |
| `-T, --time SECONDS` | 60 | run | Run duration |
| `-t, --transactions N` | 0 | run | Transactions per client |
| `-M, --mode MODE` | `prepared` | run | `simple`, `extended` or `prepared` |
| `-S, --select-only` | false | run | SELECT-only workload |
| `-N, --simple-update` | false | run | Simple-update workload |
| `-b, --builtin SPEC` | empty | run | Built-in script `name[@weight]`, repeatable |
| `-f, --file SPEC` | empty | run | Host script `path[@weight]`, repeatable |
| `-D, --define K=V` | empty | run | Script variable, repeatable |
| `-C, --connect` | false | run | Reconnect per transaction |
| `-R, --rate N` | 0 | run | Target transactions per second |
| `-L, --latency-limit MS` | 0 | run | Latency limit; requires `--rate` |
| `-P, --progress SECONDS` | 10 | run | Progress interval; 0 disables |
| `-r, --per-command-report` | false | run | Per-command latency report |
| `-l, --log` | false | run | Write per-transaction logs |
| `--log-dir DIR` | `./pgbench-logs` | run | Host log directory; requires `--log` |
