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

## Parameters

pgcli keeps Redis's parameter surface deliberately small: one flag per knob,
each mapping to a native `redis-server` option — argv overrides through the
image's own entrypoint, no `redis.conf` file is generated, and one container
per instance runs the plain upstream image (no pgcli wrapper, unlike
[rustfs](../rustfs/)). The table lists every knob with its default and the
equivalent from Pigsty's [REDIS module
parameters](https://pigsty.cc/docs/redis/param/) for reference:

| Flag / `pg.yaml` key | Default | Redis option | Pigsty equivalent |
|----------------------|---------|--------------|-------------------|
| `--version` / `version` | `8` | — (picks the image tag) | `redis_type` (engine choice, closest analogue) |
| `--image` / `image_tag` | resolved from the version table | — | package/version selection |
| `--port` / `port` | auto from `redis_start_port` (base **6379**) | `--port` | the instance key in `redis_instances` |
| `--listen` / `listen` | `0.0.0.0` | `--bind` | `redis_bind_address` |
| `--password` / `password` | generated, 20 chars | `--requirepass` | `redis_password` — Pigsty defaults to empty (no auth); pgcli always requires one |
| `--maxmemory` / `maxmemory` | unset (no cap) | `--maxmemory` + `--maxmemory-policy allkeys-lru` | `redis_max_memory` + `redis_mem_policy` (pgcli pins the policy to `allkeys-lru`) |
| `--data-dir` / `data_dir` | `<base-dir>/addon/redis/<name>/data` | `--dir /data` (host dir bind-mounted at `/data`) | `redis_fs_main` |
| — | `autostart: false` | — | — (pgcli-level: `pg autostart enable --redis`) |

Policy that is fixed, not exposed as flags:

- **`requirepass` is always on** — an unauthenticated Redis is not a reachable
  state, even if `--password` is passed empty by hand into `pg.yaml`.
- **Persistence is RDB snapshots only** — Redis's own default `save` schedule
  applies, plus a final snapshot on shutdown (`--stop-timeout 30` gives Redis
  the time to write it) and a replay from `dump.rdb` at start. This matches
  the intent of Pigsty's `redis_rdb_save: ['1200 1']`; AOF
  (`redis_aof_enabled: false`) is deliberately off for both.
- **`--maxmemory-policy` is pinned to `allkeys-lru`** whenever `--maxmemory` is
  set: the cap means "cache, evict to stay under", never "hard store". For a
  no-eviction store, omit `--maxmemory` and size the host instead.

Pigsty parameters pgcli does not manage: everything behind multi-node
topology (`redis_mode: sentinel|cluster`, `redis_cluster_replicas`,
`redis_sentinel_monitor`, per-instance `replica_of`), the config-file
template (`redis_conf`), dangerous-command renaming
(`redis_rename_commands`), and monitoring (`redis_exporter_*` — run a
`redis_exporter` container yourself if wanted). The `REDIS_REMOVE` knobs map
partially: `--clean-data` is pgcli's `redis_rm_data`; `redis_safeguard` and
`redis_rm_pkg` have no meaning when nothing is installed on the host. The one
knob you *can* bend through `--image`: `redis_type: valkey` is just another
tag — `pg addon install redis --image docker.io/valkey/valkey:8` works, and
the major reverse-parses as `8`.

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
