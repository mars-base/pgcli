---
title: "Redis"
description: "Run Redis as a pgcli addon — a standalone key-value store for cache, session, ranking and counter data, with selectable major versions 7 and 8"
weight: 50
---

[Redis](https://redis.io/) is the in-memory key-value store most applications
reach for when they need a cache, a session backend, leaderboards (sorted sets)
or atomic counters. pgcli runs it as a **standalone, top-level addon** with the
same CLI surface as the object stores (install / start / stop / logs /
autostart / remove), managed entirely through `pg.yaml`.

It is deliberately the simplest addon in the fleet:

- **One container, one port, one password.** `requirepass` is generated on the
  first install and stored in `pg.yaml` — an unauthenticated Redis is not a
  reachable state.
- **Version selection.** It is the first version-selectable addon:
  `--version 7|8` resolves through a built-in table to a pinned
  `docker.io/library/redis` tag (7 → `7.4.11`, 8 → `8.10.2`, the default).
  `--image` still bypasses the table with any tag you like.
- **RDB snapshot persistence.** The dataset lives in a bind-mounted host
  directory as a plain `dump.rdb` — it survives restarts, and survives
  `pg addon remove` (without `--clean-data`) so reinstalling the same name
  revives the data.

Pick Redis for cache/session/ranking/counter workloads next to your PostgreSQL
instances; it is independent of the PG stack, coexists with any other addon,
and several instances (even across majors) can run on one host.

> **Out of scope:** standalone only. Sentinel (HA) and Redis Cluster are not
> managed by pgcli — see [Known limitations](#known-limitations).

## How It Works

One container per instance, from the plain upstream image — pgcli builds **no
wrapper for Redis** (it starts as container root and needs no uid gymnastics,
unlike [rustfs](../rustfs/)). pgcli drives `redis-server` directly with argv
overrides through the image's own entrypoint:

```
redis-server --port <port> --requirepass <password> --bind <listen> --dir /data
```

plus `--maxmemory <cap> --maxmemory-policy allkeys-lru` when `--maxmemory` is
set. The host data directory (default
`<base-dir>/addon/redis/<name>/data`) is bind-mounted at `/data`, so the RDB
snapshot lands on the host. `--stop-timeout 30` gives Redis time to write its
final snapshot on `pg stop`.

Credentials and version are handled like the other infra addons:

- `password` (→ `requirepass`) is **generated on first install** (20 chars, or
  pin it with `--password`), stored in `pg.yaml`
  (`addons.redis.<name>.password`) and **printed once** in the install summary;
- `version` + `image_tag` are resolved together at install time (see
  [Version selection](#version-selection));
- `port` is auto-assigned from Redis's own pool (`redis_start_port`, default
  **6379**) — separate from the object stores' shared pool.

Persistence is **RDB snapshots only** (Redis's own default): `save` runs on
shutdown and on demand, and the file replays at start. AOF is deliberately not
turned on — for cache/session data the RDB granularity is the right trade; if
you need write-ahead durability, Redis replication or AOF is a topology
decision beyond this addon.

## Version selection

`--version` picks a major; the table maps each major to a pinned patch tag:

| Major | Default image tag | Select with |
|-------|-------------------|-------------|
| 7 | `docker.io/library/redis:7.4.11` | `pg addon install redis --name cache --version 7` |
| 8 | `docker.io/library/redis:8.10.2` | `pg addon install redis --name cache` (8 is the default) |

Resolution rules:

- `--version 7` (or `8`) → the table's tag is stored in `image_tag`, the major
  in `version`;
- `--image <tag>` → your tag wins outright; the major is reverse-parsed from
  it for display (`redis:7.2.4` → `7`), or left empty if unparseable (the tag
  is then shown verbatim);
- neither → major `8`;
- an unknown `--version` → error listing the available majors.

Both majors can run side by side on one host — different tags, different ports
from the same pool. The tags track the latest patch of each major and are
bumped together with the `docs/images.md` rows.

## Install

```bash
# default: major 8, instance name "redis", port auto-assigned from 6379
pg addon install redis

# a named cache instance on major 7, with a memory cap (evicts at the cap)
pg addon install redis --name legacy --version 7 --maxmemory 256mb

# loopback-only instead of the all-interfaces default
pg addon install redis --name cache --listen 127.0.0.1

# pin the password (stable across reinstalls) and a fixed port
pg addon install redis --name sessions --password 'S3ssions!' --port 6379
```

The install summary prints everything a client needs:

```
✓ redis installed: "cache"
  Container:  pgcli-redis-default-cache
  Version:    8
  Image:      docker.io/library/redis:8.10.2
  Data:       ~/pg/addon/redis/cache/data
  Address:    0.0.0.0:6379

  Password:    <generated>

  Client:      pg redis-cli ping
  Raw DSN:     redis://:<password>@0.0.0.0:6379/0
  NOTE: listening on every interface (the Pigsty default); the password above is the only gate.
        loopback-only: pg addon install redis --name cache --listen 127.0.0.1 --force
```

> **Security model.** Following the Pigsty convention, `listen` defaults to
> **`0.0.0.0`** — on Linux (host networking) the port is published on every
> interface, and `requirepass` is the only gate. That is convenient for
> app-on-another-host setups but means the password is load-bearing: pass
> `--listen 127.0.0.1` to keep an instance loopback-only. `pg addon list` and
> the logs never print the password; read it from `pg.yaml`.

Re-running `install` is idempotent: a running container is left alone, a
stopped one is started. To apply changed ports/listen/password/image, add
`--force` (the container is recreated from the config; the data dir is
untouched).

## Using Redis

### `pg redis-cli`

`pg redis-cli` runs Redis's own client from a throwaway container of the
addon's image — no host install, no password typing:

```bash
pg redis-cli ping                              # PONG
pg redis-cli set session:42 '{"user":"alice"}' # ok — auth is injected
pg redis-cli get session:42
pg redis-cli lrange queue 0 -1                 # negative indexes pass through
pg redis-cli --name legacy zrevrange leaderboard 0 9
```

The target is `--name <addon>` or, when omitted, the first redis addon by
name (with several installed, the chosen target is announced on stderr).
pgcli's own flags are parsed only before the command word; everything after is
forwarded to `redis-cli` verbatim — including its `-h`/`-p`, so
`pg redis-cli info -h 10.0.0.7 -p 6380` talks to a foreign endpoint.
`REDISCLI_AUTH` (redis-cli's native env var), if set, overrides the stored
password:

```bash
REDISCLI_AUTH=otherpass pg redis-cli dbsize
```

### Application connections

Any Redis client works; the DSN printed at install has the shape
`redis://:<password>@<host>:<port>/0`. For TLS you would need a stunnel or
redis' own TLS build in front — see [Known limitations](#known-limitations).

## Ports

Each instance takes **one port** from Redis's own pool, `redis_start_port`
(default **6379**) — a separate cursor from the object stores' shared
`minio_start_port` pool, so a redis, a minio and a rustfs instance never
collide:

```bash
pg addon install redis  --name cache    # 6379
pg addon install redis  --name sessions # 6380
pg addon install minio  --name store    # 9000 / 9001 (different pool)
```

`--port` fixes an instance to a specific port; auto-assignment skips anything
already explicit or live on the host.

## Configuration

Instances live under the top-level `addons.redis` map in `pg.yaml`:

```yaml
namespace: default
redis_start_port: 6379     # Redis's own pool, separate from minio_start_port
addons:
  redis:
    cache:
      container_name: pgcli-redis-default-cache
      name: cache
      version: "8"                       # major as selected/stored
      image_tag: docker.io/library/redis:8.10.2
      # data_dir: /srv/redis-cache       # omit for <base-dir>/addon/redis/cache/data
      listen: 0.0.0.0                    # Pigsty default; 127.0.0.1 to tighten
      port: 6379
      password: <generated>              # written on first install
      # maxmemory: 256mb                 # optional cap → allkeys-lru eviction
      autostart: false                   # pg autostart enable --redis --name cache
```

Edits to `listen`, `port`, `password`, `maxmemory`, `image_tag`/`version`, or
`data_dir` take effect after the next `pg addon install redis --name cache
--force` (or via the matching flags).

### List

```bash
pg addon list
```

```
Infra add-ons (redis):
  redis (name: cache)
    Status:      running
    Version:     8
    Address:     0.0.0.0:6379
    Auth:        on (requirepass)
    Maxmemory:   256mb (allkeys-lru)
    Data:        ~/pg/addon/redis/cache/data
    Image:       docker.io/library/redis:8.10.2
    Container:   pgcli-redis-default-cache
    Client:      pg redis-cli --name cache ping
```

`pg addon list` never prints the password.

## Start and stop

```bash
pg addon start redis --name cache
pg addon stop  redis --name cache
```

`stop` sends a graceful SIGTERM — with `--stop-timeout 30` Redis writes its
final RDB before exiting. `start` only starts an existing container (and
self-heals an improper state by recreating it from the config).

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring instances up after a host reboot:

```bash
pg autostart enable --redis --name cache
```

Redis autostart is **start-only** (install the instance first) and replays the
RDB snapshot from its data dir, so the dataset survives the reboot. Redis is
independent of the PostgreSQL stack, so boot-time `pg start --autostart` starts
it last.

## Remove

```bash
pg addon remove redis --name cache            # container + config entry; data kept
pg addon remove redis --name cache --clean-data  # also delete the data dir
```

Without `--clean-data` the host data dir (and its `dump.rdb`) stays in place —
reinstalling the same name revives the dataset. `--clean-data` deletes it,
falling back through `podman unshare` when rootless podman's subordinate uid
makes Redis's files undeletable by the host user. An empty per-instance parent
directory is pruned; a custom `data_dir` parent is never touched.

## Logs

```bash
pg logs addon redis --name cache        # last 50 lines
pg logs addon redis --name cache -f     # follow
```

## Troubleshooting

- **`NOAUTH Authentication required` from a raw client** — expected: every
  instance has a `requirepass`. Read it from `pg.yaml`
  (`addons.redis.<name>.password`) or use `pg redis-cli`, which injects it.
- **`maxmemory` evicting more than you wanted** — `--maxmemory` implies
  `allkeys-lru` (evict any key to stay under the cap: a cache, not a hard
  store). For a strict no-eviction store, omit `--maxmemory` and size the host
  instead.
- **Image not found at install** — both majors are plain
  `docker.io/library/redis` images pulled on demand; on an air-gapped host,
  `podman load` the tar first (the catalog and export steps live in
  `docs/images.md` in the repo) and the pull is skipped.
- **A hand-edited `version` that is not in the table** — `install` errors and
  suggests `--image` rather than silently rewriting your value.
- **Port already live** — auto-assignment scans host listeners and skips
  them; pin `--port` when you need a stable number.

## Known limitations

- **Standalone only** — Sentinel (HA failover) and Redis Cluster (sharding) are
  out of scope, and so is replication (a redis replica of another host). A
  single instance is a single point: durable workloads want persistence
  semantics (AOF) or replicas, which this addon does not manage.
- **No TLS** — Redis 7's native TLS exists in some builds but is not wired
  here; treat the network path as trusted and rely on `requirepass`, or keep
  the instance loopback-only (`--listen 127.0.0.1`).
- **macOS is code-complete, untested** — the bridge path (published ports,
  `host.containers.internal` for `pg redis-cli`) mirrors the object stores; it
  has not yet been exercised on a Mac.
- **RDB granularity** — a crash loses writes since the last snapshot; accept
  it for cache/session data or move durable data to PostgreSQL.
