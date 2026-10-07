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
- **Persistence you can dial in.** By default the dataset is an RDB snapshot in
  a bind-mounted host directory — it survives restarts, and survives
  `pg addon remove` (without `--clean-data`) so reinstalling the same name
  revives the data. `--aof` adds the write-ahead log on top for crash-safe,
  no-eviction stores, and `--save no` turns snapshots off entirely (see
  [Parameters](#parameters)).

Pick Redis for cache/session/ranking/counter workloads next to your PostgreSQL
instances; it is independent of the PG stack, coexists with any other addon,
and several instances (even across majors) can run on one host.

> **Out of scope:** Sentinel (automatic failover) and Redis Cluster are not
> managed by pgcli. Read replicas *are* — `--replica-of` turns an instance into
> a read-only follower of another; see [Read replicas](#read-replicas) and
> [Known limitations](#known-limitations).

## Parameters

pgcli keeps Redis's parameter surface deliberately small: one flag per knob,
each mapping to a native `redis-server` option — argv overrides through the
image's own entrypoint, no `redis.conf` file is generated, and one container
per instance runs the plain upstream image (no pgcli wrapper, unlike
[rustfs](../rustfs/)). The table lists every knob and its default:

| Flag / `pg.yaml` key | Default | Redis option | Meaning |
|----------------------|---------|--------------|---------|
| `--version` / `version` | `8` | — (picks the image tag) | major to install (see [Version selection](#version-selection)) |
| `--image` / `image_tag` | resolved from the version table | — | override the tag verbatim |
| `--port` / `port` | auto from `redis_start_port` (base **6379**) | `--port` | host port |
| `--listen` / `listen` | `0.0.0.0` | `--bind` | bind address (`127.0.0.1` to tighten) |
| `--password` / `password` | generated, 20 chars | `--requirepass` | auth; always required (a replica's must equal the master's) |
| `--maxmemory` / `maxmemory` | unset (no cap) | `--maxmemory` | dataset cap that turns eviction on |
| `--maxmemory-policy` / `maxmemory_policy` | `allkeys-lru` | `--maxmemory-policy` | eviction policy, only under a cap |
| `--aof` / `aof` | off | `--appendonly yes` | write-ahead log on top of the snapshot |
| `--appendfsync` / `appendfsync` | `everysec` (Redis's own) | `--appendfsync` | AOF flush strength, only with `--aof` |
| `--save` / `save` | Redis's default schedule | `--save` | RDB snapshot plan; `no` disables snapshots |
| `--replica-of` / `replica_host`+`replica_port` | unset (master) | `--replicaof` + `--masterauth` | read-only replica of a *local* master addon (its host/port/password are resolved from the config) |
| `--replica-of-host` / `replica_host` | — | `--replicaof` | master *address* for a cross-host replica (pair with `--replica-of-port` + `--password`) |
| `--replica-of-port` / `replica_port` | — | `--replicaof` | master port, paired with `--replica-of-host` |
| `--data-dir` / `data_dir` | `<base-dir>/addon/redis/<name>/data` | `--dir /data` (host dir bind-mounted at `/data`) | where the data lives |
| — | `autostart: false` | — | start on boot (`pg autostart enable --redis`) |

How the knobs combine:

- **`requirepass` is always on** — an unauthenticated Redis is not a reachable
  state, even if `--password` is left empty by hand in `pg.yaml`.
- **Persistence defaults to RDB snapshots** — Redis's own `save` schedule, plus
  a final snapshot on shutdown (`--stop-timeout 30` gives Redis the time to
  write it) and a replay from `dump.rdb` at start.
- **`--aof` adds the write-ahead log** (`--appendonly yes`) alongside the
  snapshot — the durable, no-eviction shape. `--appendfsync` tunes its flush:
  `always` (durability) / `everysec` (the default) / `no` (throughput). The AOF
  lives in an `appendonlydir/` under the data dir and is replayed before the
  snapshot, so it is the source of truth when both are on.
- **`--save no` disables snapshots** — pair it with `--aof` for an AOF-only
  store (the summary then reads `aof only`); on its own it means a purely
  in-memory instance that loses its dataset on restart (the install warns).
- **`--maxmemory` implies eviction**, defaulting to `allkeys-lru` — the cap
  means "cache, evict to stay under". Pass `--maxmemory-policy noeviction` for
  a hard ceiling (writes fail past the cap instead of evicting), or omit
  `--maxmemory` entirely for no cap. The policy flag requires `--maxmemory`;
  `--appendfsync` requires `--aof` — the CLI rejects those combinations before
  it pulls or creates anything.
- **`--replica-of` makes a read replica** — see [Read
  replicas](#read-replicas). The persistence/eviction knobs stay orthogonal: a
  replica may cap memory or enable AOF exactly like a master.

Not every Redis option is a flag, by design: the multi-node control plane
(Sentinel, Redis Cluster, automatic failover) is out of scope — see [Known
limitations](#known-limitations). Read replicas are supported
([Read replicas](#read-replicas)); it is the *orchestration* (promoting a
replica automatically when the master dies) that pgcli leaves to you.
And `--image` is the escape hatch for the
engine itself: `pg addon install redis --image docker.io/valkey/valkey:8`
runs [Valkey](https://valkey.io/) (the Redis fork), its major reverse-parsing
as `8`.

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

# a read-only replica of the local "sessions" instance (borrows its password,
# major and endpoint)
pg addon install redis --name sessions-ro --replica-of sessions
```

The install summary prints everything a client needs:

```
✓ redis installed: "cache"
  Container:  pgcli-redis-default-cache
  Version:    8
  Image:      docker.io/library/redis:8.10.2
  Data:       ~/pg/addon/redis/cache/data
  Address:    0.0.0.0:6379
  Role:       master
  Persistence: rdb snapshots (default save schedule)

  Password:    <generated>

  Client:      pg redis-cli ping
  Raw DSN:     redis://:<password>@0.0.0.0:6379/0
  NOTE: listening on every interface; the password above is the only gate.
        loopback-only: pg addon install redis --name cache --listen 127.0.0.1 --force
```

> **Security model.** `listen` defaults to **`0.0.0.0`** — on Linux (host
> networking) the port is published on every interface, and `requirepass` is
> the only gate. That is convenient for app-on-another-host setups but means
> the password is load-bearing: pass `--listen 127.0.0.1` to keep an instance
> loopback-only. `pg addon list` and the logs never print the password; read it
> from `pg.yaml`.

Re-running `install` is idempotent: a running container is left alone, a
stopped one is started. To apply changed ports/listen/password/image, add
`--force` (the container is recreated from the config; the data dir is
untouched). The persistence knobs (`--aof`/`--appendfsync`/`--save`/
`--maxmemory-policy`) take effect the same way.

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

The local target is `--name <addon>` or, when omitted, the first redis addon by
name (with several installed, the chosen target is announced on stderr).
pgcli's own flags are parsed only before the command word; everything after is
forwarded to `redis-cli` verbatim — negative indexes and option flags (via `--`)
work that way.

**Reaching a remote endpoint** — `--host`/`--port` override where the client
connects, without registering the remote as an addon:

```bash
# reuse a local addon's image + password, but talk to a remote host (e.g. a replica)
pg redis-cli --host 10.0.0.7 ping
pg redis-cli --host 10.0.0.7 --port 6380 --name cache info

# no local addon at all: default-major image, password via REDISCLI_AUTH
REDISCLI_AUTH=s3cret pg redis-cli --host 10.0.0.7 dbsize
```

Use these instead of redis-cli's own `-h`/`-p`: those sit *after* the command
word and would be read as command arguments (`ping -h 10.0.0.7` errors), while
pgcli injects the connection `-h`/`-p` itself. `REDISCLI_AUTH` (redis-cli's
native env var), if set, overrides the stored password in every mode:

```bash
REDISCLI_AUTH=otherpass pg redis-cli dbsize
```

### Application connections

Any Redis client works; the DSN printed at install has the shape
`redis://:<password>@<host>:<port>/0`. For TLS you would need a stunnel or
redis' own TLS build in front — see [Known limitations](#known-limitations).

## Read replicas

`--replica-of` turns a new instance into a **read-only replica** of an existing
master addon: it streams the master's writes continuously and serves reads,
while writes to it are rejected with `READONLY`. This is read scaling and data
redundancy, **not** high availability — pgcli does not run Sentinel, so a dead
master is not promoted automatically (see [Known
limitations](#known-limitations)).

```bash
# master (writable)
pg addon install redis --name cache

# same-host replica: it borrows the master's major, password and endpoint
pg addon install redis --name cache-ro --replica-of cache
```

A local `--replica-of` resolves everything from the config: the replica adopts
the master's version (cross-major replication is unsupported — a mismatch is a
fast error), its password, and the endpoint `127.0.0.1:<master-port>`. That one
password then serves both `requirepass` (the replica's own clients auth with it)
and `--masterauth` (the replica authenticating *to* the master), so
`--password`/`--maxmemory`/`--aof` etc. stay exactly as on a master. The stored
`replica_host`/`replica_port` mean `pg addon start` rebuilds the same
`--replicaof` argv even if the master is gone from the config.

**Cross-host masters** use the explicit form — pgcli cannot look up a remote
master's password, so you supply it (it must equal the master's):

```bash
pg addon install redis --name cache-r2 \
    --replica-of-host 10.0.0.7 --replica-of-port 6379 --password <master-password>
```

`pg addon list` and the install summary show a `Role:` line — `master`, or
`replica of <host>:<port> (read-only)` — and `pg redis-cli --name cache-ro
info replication` reports `role:replica` with `master_link_status:up` once the
(initial, RDB-carrying) sync completes. Send reads to the replica and writes to
the master, e.g. with the DSNs:

```bash
# read traffic -> the replica
redis://:<password>@127.0.0.1:<replica-port>/0
```

To promote a replica to an independent master at runtime (the manual failover
pgcli deliberately leaves to you), clear its replication and then, if you want
it to persist across restarts, drop its role from the config:

```bash
pg redis-cli --name cache-ro replicaof no one   # stop following the master
pg addon remove redis --name cache-ro          # keep the data, forget the role
pg addon install redis --name cache-ro         # reinstall as a plain master
```

## Ports

Each instance takes **one port** from Redis's own pool, `redis_start_port`
(default **6379**) — a separate cursor from the object stores' shared
`minio_start_port` pool, so a redis, a minio and a rustfs instance never
collide:

```bash
pg addon install redis  --name cache    # 6379
pg addon install redis  --name sessions # 6380
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
      listen: 0.0.0.0                    # default (all interfaces); 127.0.0.1 to tighten
      port: 6379
      password: <generated>              # written on first install
      # maxmemory: 256mb                 # optional cap
      # maxmemory_policy: noeviction     # only with a cap; default allkeys-lru
      # aof: true                        # write-ahead log on top of snapshots
      # appendfsync: always              # only with aof; default everysec
      # save: "900 1 300 10"             # RDB schedule; "no" disables snapshots
      # replica_host: 127.0.0.1          # set by --replica-of: this is a read replica
      # replica_port: 6379               # the master's port, paired with replica_host
      autostart: false                   # pg autostart enable --redis --name cache
```

Edits to `listen`, `port`, `password`, `maxmemory`/`maxmemory_policy`,
`aof`/`appendfsync`/`save`, `replica_host`/`replica_port`,
`image_tag`/`version`, or `data_dir` take effect after the next
`pg addon install redis --name cache --force` (or via the matching flags). The
replica role is normally set by the `--replica-of*` flags rather than hand-
edited — see [Read replicas](#read-replicas).

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
    Role:        master
    Auth:        on (requirepass)
    Maxmemory:   256mb (allkeys-lru)
    Persistence: rdb + aof (default save schedule, appendonly yes)
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
- **`maxmemory` evicting more than you wanted** — under a `--maxmemory` cap the
  default policy is `allkeys-lru` (evict any key to stay under: a cache, not a
  hard store). Pass `--maxmemory-policy noeviction` to make the cap a hard
  ceiling instead (writes fail past it rather than evicting), or omit
  `--maxmemory` for no cap at all.
- **AOF not replaying the newest writes** — `--appendfsync everysec` (the
  default) can lose the last second of writes on a hard crash; use
  `--appendfsync always` when you cannot afford that. A graceful `pg stop`
  always flushes, so this only bites on an unclean kill.
- **Image not found at install** — both majors are plain
  `docker.io/library/redis` images pulled on demand; on an air-gapped host,
  `podman load` the tar first (the catalog and export steps live in
  `docs/images.md` in the repo) and the pull is skipped.
- **A hand-edited `version` that is not in the table** — `install` errors and
  suggests `--image` rather than silently rewriting your value.
- **Port already live** — auto-assignment scans host listeners and skips
  them; pin `--port` when you need a stable number.
- **A replica rejects writes (`READONLY`)** — that is the role, not a fault:
  send writes to the master. To make it an independent master, see [Read
  replicas](#read-replicas).
- **A freshly-created replica briefly shows `master_link_status:down`** —
  normal during the first (RDB-carrying) full sync, and redis 8 reloads the
  link once more when the snapshot transfers over its rdbchannel. Watch
  `info replication`; it settles at `up` and `role:replica`.

## Known limitations

- **No automatic failover** — read replicas are supported
  ([Read replicas](#read-replicas)), but the HA control plane is not: Sentinel
  and Redis Cluster (sharding) are out of scope. A replica gives read scaling
  and a redundant copy of the data, not a self-healing pair — when the master
  dies you promote a replica yourself (`replicaof no one`, above). A single
  instance with no replica is a single point: it can be made *durable* on disk
  with `--aof`, but it cannot fail over to another node.
- **No TLS** — Redis 7's native TLS exists in some builds but is not wired
  here; treat the network path as trusted and rely on `requirepass`, or keep
  the instance loopback-only (`--listen 127.0.0.1`).
- **macOS is code-complete, untested** — the bridge path (published ports,
  `host.containers.internal` for `pg redis-cli`) mirrors the object stores; it
  has not yet been exercised on a Mac.
- **Default persistence is RDB granularity** — a crash loses writes since the
  last snapshot. Enable `--aof` when that gap matters (at `everysec` you still
  lose at most one second; `always` closes it); accept snapshots-only for
  cache/session data, or move durable data to PostgreSQL.
