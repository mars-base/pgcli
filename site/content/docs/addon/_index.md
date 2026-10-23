---
title: "Addons"
description: "pgcli addon management guide"
weight: 70
icon: fa-solid fa-puzzle-piece
menus:
  main:
    identifier: docs-addon
    parent: docs
    weight: 70
    params:
      icon: fa-solid fa-puzzle-piece
cascade:
  type: docs
  footer_style: slim
---

pgcli supports extending PostgreSQL capabilities through an addon system. Addons are standalone containers that provide additional functionality for PostgreSQL instances without modifying the database itself.

## Supported Addons

The following addons are currently supported:

| Addon | Description |
|-------|-------------|
| [`pgbouncer`](./pgbouncer/) | Connection pool manager with transaction-level pooling |
| [`etcd`](./etcd/) | Distributed key-value store — standalone, clusterable, for HA / DCS use |
| [`pgdog`](./pgdog/) | Postgres proxy — connection pooling, load balancing and sharding |
| [`postgrest`](./postgrest/) | Expose a PostgreSQL schema as a REST API — single stateless container, local or remote (any PG endpoint) |
| [`haproxy`](./haproxy/) | TCP load balancer in front of a Patroni cluster — unified or read/write split, or any native proxying via your own `haproxy.cfg` (Linux only) |
| [`minio`](./minio/) | S3-compatible object storage with web console — standalone or distributed erasure-coded cluster across hosts (Linux and macOS) |
| [`silo`](./silo/) | S3-compatible object storage (Pigsty's MinIO fork) — same feature surface as the minio addon, shares one port pool; ships its own `mcli` client (Linux and macOS) |
| [`rustfs`](./rustfs/) | S3-compatible object storage (Rust reimplementation) — SNSD/SNMD/MNMD erasure-coded layouts, fixed container uid handled inside pgcli's wrapper image, shares the one port pool (Linux only) |
| [`garage`](./garage/) | S3-compatible object storage (Deuxfleurs' Rust store) — single-node v1, rendered `garage.toml`, two data dirs, four ports from its own pool, plaintext HTTP only (front with nginx for TLS) (Linux; macOS code-complete, untested) |
| [`seaweedfs`](./seaweedfs/) | S3-compatible object storage (Go, volume-managed disk) — single-node `weed mini` or cross-host `weed server` cluster with filer metadata in a shared PostgreSQL, one credential pair shared by the whole cluster, native S3 TLS; own port pool (Linux; macOS code-complete, untested) |
| [`redis`](./redis/) | Standalone key-value store for cache, session, ranking and counter data — selectable majors 7/8, auto-generated `requirepass`, RDB persistence, read replicas, native cluster sharding via `--cluster`, own port pool (Linux; macOS code-complete, untested) |
| [`predixy`](./predixy/) | Redis protocol proxy — turns a native cluster into one plain `redis://` endpoint, so clients need no cluster awareness at all; own port pool (Linux only) |
| [`pgadmin`](./pgadmin/) | pgAdmin 4 — the official PostgreSQL web administration UI, one container from the upstream image; web login auto-generated, optional one-time server seed via `--dsn`/`--pg-name`, persistent session store; own port pool (Linux; macOS code-complete, untested) |
| [`nginx`](./nginx/) | HTTP reverse proxy fronting multiple web services — path-based routing to declared backends, optional TLS termination, rendered `nginx.conf` or your own `--conf-file`, `pg addon nginx reload` on config change; own port pool (Linux and macOS) |
| [`grafana`](./grafana/) | Grafana — the open-source observability dashboard, one container from the official upstream image; web login auto-generated, `Listen` defaults to `0.0.0.0` (signup closed), env-only (`GF_*`) config with no provisioning in v1; own port pool (Linux; macOS code-complete, untested) |
| [`prometheus`](./prometheus/) | Prometheus — the metrics collection backend Grafana reads from, one container from the official `docker.io/prom/prometheus` image; rendered `prometheus.yml` (self job + one job per `--scrape JOB`, folding that job's targets; `--scrape-append` merges incrementally; `--alertmanager-url` forwards fired alerts and adds the matching scrape job; `--rule-file` copies and loads alerting rules) or your own `--conf-file`, SIGHUP reload via `pg addon prometheus reload`; **no web login** — `Listen` defaults to `0.0.0.0` guarded only by the network (use `--listen 127.0.0.1` for loopback); own port pool (Linux; macOS code-complete, untested) |
| [`alertmanager`](./alertmanager/) | Alertmanager — the alert dedup/grouping/routing frontend of the Prometheus stack, one container from the official `docker.io/prom/alertmanager` image; rendered `alertmanager.yml` (a minimal route to a no-op or `--webhook-url` `web.hook` receiver) or your own `--conf-file`, SIGHUP reload via `pg addon alertmanager reload`; HA cluster gossip disabled (`9094` never opens); **no web login** — `Listen` defaults to `0.0.0.0` guarded only by the network, and reaching it means anyone can create silences (mute on-call), so use `--listen 127.0.0.1` for loopback; wired to Prometheus from the Prometheus side via `pg addon install prometheus --alertmanager-url`; own port pool (Linux; macOS code-complete, untested) |
| [`victoriametrics`](./victoriametrics/) | VictoriaMetrics — fast, Prometheus-compatible time-series database, one container from the official `docker.io/victoriametrics/victoria-metrics` image; **no config file** (everything is CLI flags: `-storageDataPath` / `-httpListenAddr` / `-retentionPeriod` / `--extra-arg`), no hot reload (changes recreate the container), image runs as root (no pre-chown); **no web login** — `Listen` defaults to `0.0.0.0` guarded only by the network (use `--listen 127.0.0.1` for loopback); own port pool (Linux; macOS code-complete, untested) |
| [`victorialogs`](./victorialogs/) | VictoriaLogs — the log database of the VictoriaMetrics family, one container from the official `docker.io/victoriametrics/victoria-logs` image; single HTTP port serves ingestion (JSON Lines / Elasticsearch bulk / syslog / OTLP), LogsQL queries and the built-in UI; **no config file** (everything is CLI flags: `-storageDataPath` / `-httpListenAddr` / `-retentionPeriod` / `--extra-arg`), `--retention` takes a duration (`30d`, `3months`, ...) and is omitted by default so the binary's own default stands; **no web login** — `Listen` defaults to `0.0.0.0` guarded only by the network (use `--listen 127.0.0.1` for loopback); own port pool, default 9428 (Linux; macOS code-complete, untested) |
| [`vlagent`](./vlagent/) | vlagent — the official VictoriaLogs log agent, one container from the official `docker.io/victoriametrics/vlagent` image; tracks host log files (each `--log-path` mounted read-only at the same absolute path) selected by vlagent's native globs (`--include`/`--exclude`; **no regex**), and forwards to VictoriaLogs via one or more `--remote-write-url` values ending in `/insert/native`; relay-only when no paths are given; **no config file, no auth**; own port pool, default 9429 (Linux; macOS code-complete, untested) |
| [`vmcluster`](./vmcluster/) | VMCluster — the VictoriaMetrics cluster edition, three components (`vminsert` + `vmselect` + `vmstorage`) each in its own container from the official `docker.io/victoriametrics/*:*-cluster` images; `--roles` selects which components THIS host runs (default all three = converged single host) and `--storage-node` lists every vmstorage address for cross-host sharding/replication; only vmstorage has persistent data; **no config file, no auth**; 5 ports per instance from `vmcluster_start_port` (default 8480-8484) (Linux only) |
| [`vlcluster`](./vlcluster/) | VLCluster — the VictoriaLogs cluster edition, three components (`vlinsert` + `vlselect` + `vlstorage`) all from the **same** `docker.io/victoriametrics/victoria-logs` image, distinguished only by flags; `--roles` selects which components THIS host runs (default all three = converged single host) and `--storage-node host:storagePort` lists every vlstorage address for cross-host sharding (**no replication** — a down vlstorage makes queries return 502); only vlstorage has persistent data; **no config file, no auth**; 3 ports per instance from `vlcluster_start_port` (default 9430-9432) (Linux only) |
| [`gitea`](./gitea/) | Gitea — self-hosted Git service, one container from the official `docker.io/gitea/gitea` image (rootful build, runs as uid 1000); no-wizard install (`INSTALL_LOCK`) plus an auto-created admin whose web login is generated, printed once, and retrievable via `pg addon password gitea`; embedded SQLite by default or an external PostgreSQL via `--dsn` (Gitea creates its schema, pgcli never creates or drops the database); optional `ssh://` clones via `--ssh-port` (own second port pool; off by default — HTTP clone only, since the image's OpenSSH sshd runs unconditionally on every instance and `--ssh-port` is what promotes it from a parked listener to the live clone endpoint); crypto secrets (`SECRET_KEY`/`INTERNAL_TOKEN`) deliberately never injected — they belong to the persisted volume so reinstalls cannot silently re-key a surviving database; `Listen` defaults to `0.0.0.0` (signup closed by default, open it with `--allow-registration`; accounts via `pg addon gitea user create`), env-only (`GITEA__*`) config; own port pool (Linux; macOS code-complete, untested) |
| [`beegfs`](./beegfs/) | BeeGFS — parallel filesystem, pgcli's **first multi-container addon**: one config entry = one converged single-host filesystem = three containers (`mgmtd` + `meta` + `storage`) from the official `ghcr.io/thinkparq/beegfs-*` images, started in dependency order and managed as a unit (cluster-granular install reuse, stop/start, autostart, `pg logs addon beegfs --service`); **host networking only** — Linux only, no port pool: the protocol ports are stock/fixed (8008/8010 movable via the two `--beegfs-mgmtd-*` flags, meta 8005 + storage 8003 have no 8.4 override), which is exactly why v1 is **one filesystem per host** (guarded); shared connection-auth secret generated on install (`pg addon password beegfs`), mgmtd TLS **disabled** (`--tls-disable`, so clients must match); the **client is a kernel module — pgcli never installs or mounts it**, `pg addon beegfs mount-hint` prints the exact client config; licenses never injected; `pg addon beegfs check` reads daemon log lines for health; v1 single-host, v2 = cross-host cluster |
| [`juicefs`](./juicefs/) | JuiceFS — S3-backed POSIX filesystem, pgcli's first **host-binary addon** (no container): the binary is downloaded from GitHub releases with a pinned sha256 (a `juicefs` on `PATH` is reused), both sides fully explicit with **no same-host auto-detection** — the object store is an EXTERNAL S3 service (`--s3-endpoint http(s)://host[:port]` + `--access-key`/`--secret-key`, probed before format) and the metadata engine is an explicit `--dsn` URL (`postgres://` / `redis://` / `sqlite3://`, which pgcli never creates or drops); buckets stay manual `pg mc` work — pgcli prints the exact command. Mounts a plain **host** directory (no propagation into PG containers); `pg addon juicefs mount|umount|status`, top-level `pg addon start|stop juicefs` aliases, `pg logs addon juicefs -f`, `pg addon password juicefs` = the S3 secret; `remove` never deletes bucket data — Linux only |
| [`zerofs`](./zerofs/) | ZeroFS — encrypted, self-maintaining S3 filesystem (its own LSM on the bucket, **no external metadata engine**) as a host-binary addon: pgcli writes `config.toml` and runs two host processes — a **loopback-only** 9P server (9P has no auth; non-loopback `--listen` is rejected, one port per instance) plus the FUSE client; the object store is an EXTERNAL S3 service given explicitly (`--s3-endpoint` + `--access-key`/`--secret-key`, optional `--s3-region` — no same-host addon lookup); a generated encryption password **wraps the bucket's data key** (`pg addon password zerofs`; after `remove` keeps the bucket, re-attach with `--encryption-password '<original>'` or the server fails `Invalid password or corrupted key`); fencing needs conditional put — `--conditional-put auto` (MinIO 2022-08+ / real S3) or `redis` via an explicit `--redis-dsn` for stores lacking it, `none` single-writer only; `pg addon zerofs start|stop|mount|umount|status`, `pg logs addon zerofs --service server|mount` — Linux only |
| [`duckdb`](./duckdb/) | DuckDB — embedded OLAP database ("SQLite for analytics") as a **host-binary** addon (no container, no daemon): C++ columnar engine with vectorised execution, queries Parquet/CSV/JSON/Arrow directly; downloaded from GitHub releases with a pinned sha256 (a `duckdb` on `PATH` is reused); common extensions (`httpfs`, `parquet`, `json`) auto-installed; `--version` pins the release (`1.5.6` LTS default, `2.0.0` supported); `pg addon duckdb status|shell`; Linux only, amd64 |

Each addon has its own page with commands, parameters, and troubleshooting.

> **Patroni HA (`pg ha`) is not on this list** — it is not installed via
> `pg addon` but is its own top-level command, and its documentation now lives in
> a dedicated **[HA Cluster](../ha-cluster/)** section. [etcd](./etcd/) and
> [HAProxy](./haproxy/) here are addons a `pg ha` cluster can use.

## How It Works

Addons run as standalone containers managed through `pg.yaml`:

1. **`pg addon install`** generates configuration and starts the addon container
2. Config and data live under `<base-dir>/addon/<addon-name>/`
3. Addon containers communicate over the host network
4. Containers automatically restart when configuration is updated

**Namespace isolation:** Addons respect the config's `namespace` setting — container names include the namespace prefix, so different config files can manage independent addons without conflicting.

## Common Commands

```bash
pg addon install <addon> [flags]   # install / reconfigure (idempotent)
pg addon list                       # show all installed addons and status
pg addon remove <addon> [flags]     # remove the addon, container, and data
```

See each addon's page for its specific flags and examples:
[Pgbouncer](./pgbouncer/) · [etcd](./etcd/) · [PgDog](./pgdog/) · [PostgREST](./postgrest/) · [HAProxy](./haproxy/) · [MinIO](./minio/) · [Silo](./silo/) · [rustfs](./rustfs/) · [Garage](./garage/) · [SeaweedFS](./seaweedfs/) · [Redis](./redis/) · [Predixy](./predixy/) · [pgAdmin 4](./pgadmin/) · [nginx](./nginx/) · [Grafana](./grafana/) · [Prometheus](./prometheus/) · [Alertmanager](./alertmanager/) · [VictoriaMetrics](./victoriametrics/) · [VictoriaLogs](./victorialogs/) · [vlagent](./vlagent/) · [VMCluster](./vmcluster/) · [VLCluster](./vlcluster/) · [Gitea](./gitea/) · [BeeGFS](./beegfs/) · [JuiceFS](./juicefs/) · [ZeroFS](./zerofs/) · [DuckDB](./duckdb/). Patroni pages live in [HA Cluster](../ha-cluster/).
