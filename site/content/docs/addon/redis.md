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

> **Out of scope:** Sentinel (automatic failover) is not managed by pgcli.
> Redis's *native cluster* is supported — `--cluster` turns instances into
> cluster-enabled members that you assemble once yourself with
> `pg redis-cli -- --cluster create` (masters-only by default; add per-master
> followers with `--cluster-replicas N`; see [Native cluster](#native-cluster)).
> Read replicas are too — `--replica-of`
> turns an instance into a read-only follower of another (see
> [Read replicas](#read-replicas) and [Known limitations](#known-limitations)).

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
| `--cluster` / `cluster` | unset (standalone) | `--cluster-enabled yes` + `--masterauth` | native-cluster member: non-empty is the group name — same value means same cluster, and every member of a group shares the first member's password (`--cluster create` authenticates all operands with one password, exactly like a replica's password must equal its master's). Every member also carries `--masterauth` so any node Redis later promotes to a follower can authenticate to its master. Mutually exclusive with `--replica-of`/`--replica-of-host` — an instance is either a cluster member or a read replica, never both. pgcli installs the node; you assemble the cluster yourself — see [Native cluster](#native-cluster) |
| `--cluster-replicas` / `cluster_replicas` | `0` (masters-only) | — (echoed into the suggested `--cluster create` only) | per-master followers for the assemble step: the summary counts `3*(1+N)` nodes and the printed command ends with `--cluster-replicas N`. Which members become followers is Redis's decision at `--cluster create`, not at install. A group property like the password — the first member sets it, later members inherit it, an explicit mismatch is rejected |
| `--advertise-host` / `advertise_host` | unset (`127.0.0.1`, single-host) | `--cluster-announce-ip`/`-port`/`-bus-port` | the address this member announces to peers — a LAN IP, required for a cross-host cluster (Redis's cluster bus has no NAT/port remapping) |
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
- **`--cluster` makes a cluster member, and is mutually exclusive with
  `--replica-of`/`--replica-of-host`** — an instance is one role or the other,
  never both. Members of the same `--cluster` group share the first member's
  password automatically (a later `--cluster` member with a *different*
  `--password` is rejected before anything is pulled or created), and the
  per-master follower count set by `--cluster-replicas N` is inherited the same
  way. See [Native cluster](#native-cluster).

Not every Redis option is a flag, by design: the multi-node control plane
(Sentinel, automatic failover) is out of scope, and so is cluster *orchestration*
— pgcli installs cluster-enabled members but never runs `--cluster create`,
`--cluster add-node`, resharding, or scale-in for you; see [Native
cluster](#native-cluster) and [Known limitations](#known-limitations). Read
replicas are supported
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
> loopback-only. `pg addon list` and the logs never print the password; reveal it
> on demand with `pg addon password redis --name cache` or `pg addon list
> --show-password` (or read `pg.yaml`).

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

## Native cluster

`--cluster` turns instances into members of a **Redis native cluster** —
sharding by slot (16384 of them), not a read replica of one master. This is the
only multi-node topology pgcli supports; [Sentinel](#known-limitations) is not.

pgcli's role is deliberately narrow: **it installs cluster-enabled nodes, it does
not assemble them.** That is Redis's own two-step flow, not a pgcli invention —
a cluster is `cluster-enabled yes` servers plus one `redis-cli --cluster create`
command run against all of them at once. Splitting those two steps means pgcli
never has to guess "is this the last member I'm waiting for, should I form the
group now" (an etcd-style auto-bootstrap state machine); assembling is always
your decision, made once, the same way you'd do it against bare `redis-server`.

```bash
# 1. install the members — one --cluster <token> turns each into a cluster-enabled
#    node; the same token means "same cluster". Minimum 3 masters (Redis's own
#    quorum rule). masters-only is the default (3 installs, --cluster-replicas 0);
#    to give each master a follower, install 3*(1+N) members and pass
#    --cluster-replicas N to the first one (the rest inherit it) — see step 2.
pg addon install redis --name n1 --cluster app --maxmemory 10mb
pg addon install redis --name n2 --cluster app --maxmemory 10mb
pg addon install redis --name n3 --cluster app --maxmemory 10mb
```

Members of one group **share the first member's password** automatically:
`--cluster create` authenticates every operand node with a single `-a` (here,
via the one `REDISCLI_AUTH` pg redis-cli injects), so one password has to reach
the whole group — the same rule that makes a [read
replica](#read-replicas)'s password equal its master's. A later `--cluster`
member installed with a *different* `--password` is rejected up front, before
anything is pulled or created; omit `--password` and the group's own value is
inherited. Every member also emits `--masterauth` (the same shared password) in
its argv: at `--cluster create` time Redis may promote any node to a follower,
and a follower needs `--masterauth` to authenticate *to* its master — without it
it spins forever in a reconnect storm (`master_link_status:down`, CPU pinned).
`--cluster` and `--replica-of`/`--replica-of-host` are mutually exclusive on one
install — an instance is either a cluster member or a read replica, never both.

```bash
# 2. assemble once, through pg redis-cli's passthrough (pgcli never runs this)
pg redis-cli --name n1 -- --cluster create 127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381 --cluster-replicas 0
```

The three `addr:port` operands are every member's `PeerAddr:port` — that's
`127.0.0.1:<port>` for a single-host cluster, or each member's
`--advertise-host:<port>` for a cross-host one (see below). The install summary
already prints this exact command, built from whatever `--cluster app` members
are configured at that moment (`pg redis-cli --name n3 -- --cluster create
127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381 --cluster-replicas 0`), so it is
copy-pasteable rather than something to reconstruct by hand:

```
-> NOTE: native-cluster member of "app" — it comes up cluster-enabled but INCOMPLETE
   until you assemble the group once (see the "Cluster:" line below). pgcli does not
   run --cluster create for you.
...
  Cluster:     "app" — 3 masters configured. Assemble once (pgcli does not run this):
               pg redis-cli --name n3 -- --cluster create 127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381 --cluster-replicas 0
               (bus port = client+10000; for cross-host peers open it on the firewall, and give each member --advertise-host)
```

`--cluster-replicas 0` is masters-only — every member holds its own slice of
the slots and none of them has a follower. To give each master a follower (so
one master's slot range survives that node dying), pass `--cluster-replicas N`
on the first install and add enough members: each master needs itself plus its
`N` followers, so the total is `3*(1+N)` — for N=1 that's 6 members:

```bash
pg addon install redis --name n1 --cluster app --cluster-replicas 1 --maxmemory 10mb
pg addon install redis --name n2 --cluster app --maxmemory 10mb   # inherits replicas=1
# …install to 6 members total, then:
pg redis-cli --name n1 -- --cluster create \
    127.0.0.1:6379 127.0.0.1:6380 127.0.0.1:6381 \
    127.0.0.1:6382 127.0.0.1:6383 127.0.0.1:6384 --cluster-replicas 1
```

Which members Redis elects as masters vs followers is its decision at
`--cluster create` (it balances followers across hosts where it can), so the
pgcli instance names are deliberately role-neutral, and a follower re-links
from its own `nodes.conf` after a restart — role and masterauth pairing
survive, no re-create. The install summary switches from "3 masters
configured" to counting **nodes** ("6 nodes configured") once followers are in
play, and echoes the group's `--cluster-replicas N` into the suggested command;
it never calls a follower-set node a master.

### After assembling

A `pg redis-cli` pointed at a cluster member is automatically a
**cluster-aware client**: pgcli injects redis-cli's own `-c` flag so a `get`/`set`
that lands on the "wrong" node follows its `MOVED` redirect to the node that
actually owns the slot — no `-c` to remember, and it doesn't matter which
`--name` you happen to point at:

```bash
pg redis-cli --name n1 set foo bar      # might be served by n3 via MOVED
pg redis-cli --name n2 get foo          # same key, followed the redirect again
pg redis-cli --name n1 -- --cluster check 127.0.0.1:6379   # admin subcommands, not -c's job
```

The last line is the exception, not a rule to remember: when the command is
redis-cli's own `--cluster <subcommand>` (which dials the nodes named in its
operands itself), pgcli suppresses the `-c` injection — it's the assembly/admin
surface from step 2, not a data command.

The auto-`-c` above depends on pgcli reading a **local** cluster member from
`pg.yaml` (the `--name` path). When you reach the cluster **remotely** with
`--host`/`--port` from a machine that has no such addon configured, pgcli has no
way to know the endpoint is a cluster node, so it injects nothing — and an
unredirected `get`/`set` that lands on the wrong node just returns `MOVED ...`.
Forward redis-cli's `-c` yourself (it is redis-cli's own flag, so it goes after
the `--` passthrough, before the command word):

```bash
export REDISCLI_AUTH=<the group password>
pg redis-cli --host 10.10.0.158 --port 6379 -- -c get foo   # follows MOVED for you
pg redis-cli --host 10.10.0.158 --port 6379 -- -c set foo bar
```

`-c` matters for writes as much as reads: without it a `set` aimed at a
non-owner returns `MOVED` and is **not** applied, so the key never exists. Put
`-c` on the write, and any single node serves the whole keyspace.

You can also skip the env var and pass the password with redis-cli's own `-a`
— it is a connection option too, so it goes in the same passthrough slot,
*before* the command word:

```bash
pg redis-cli --host 10.10.0.158 --port 6379 -- -a <the group password> -c get foo
```

Two caveats distinguish it from `REDISCLI_AUTH`: redis-cli prints a security
warning on every invocation (`-a`/`-u` on a command line is visible to other
users via `ps`), and placing the flag after the command word (`get foo -a …`)
fails with "wrong number of arguments" — it would be passed as a command
argument. For anything beyond a one-off command, prefer the env var; they are
otherwise equivalent for authentication.

The cluster bus is a **second port per member**: always `client port + 10000`
(Redis's own fixed rule, e.g. `6379`→`16379`) — it is not drawn from
`redis_start_port`, not stored in `pg.yaml`, and `pg addon list` shows it
alongside the token:

```
    Role:        cluster member of "app" (127.0.0.1:6381)
    Cluster:     app (bus 16381)
```

Topology is self-healing: each node's `--cluster-config-file nodes.conf` lives
in its own `--dir /data` bind mount, so `pg addon stop`/`start` (or a host
reboot) brings the whole cluster back with no re-create step — same
start-only model as autostart for everything else here.

### Cross-host members: `--advertise-host`

A single-host cluster dials peers at `127.0.0.1` and works with no extra flags.
Redis's cluster bus does **not** support NAT or port remapping, so members on
different hosts must announce the address peers can actually reach:

```bash
# on 10.10.0.158 — the first member decides the group's one password; read it back:
pg addon install redis --name n1 --cluster app --advertise-host 10.10.0.158
PW=$(pg addon password redis --name n1)
# on 10.10.0.159 and 10.10.0.160 — carry that same password across:
pg addon install redis --name n2 --cluster app --advertise-host 10.10.0.159 --password "$PW"
pg addon install redis --name n3 --cluster app --advertise-host 10.10.0.160 --password "$PW"
# assemble once (from any host that reaches all three) — the operands are each
# member's advertised address + its port (one member per host here, each host's
# own pool starts at 6379, so all three are :6379):
pg redis-cli --name n1 -- --cluster create \
    10.10.0.158:6379 10.10.0.159:6379 10.10.0.160:6379 --cluster-replicas 0
```

`--advertise-host` adds `--cluster-announce-ip/-port/-bus-port` to the node's
argv, and `PeerAddr` (used to build the `--cluster create` operands above)
switches from `127.0.0.1` to that address. Two things to get right on your side:
open each member's **bus port (client+10000 — `16379` at the default `6379`)**
on the firewall between hosts — not just the client port — and every member of
the group must carry the flag, not only the ones being joined later.

The `--password "$PW"` on every host after the first is not decoration: the
shared-password inheritance only works **within one `pg.yaml`** — on a single
host, later members automatically adopt the group's password; across hosts, each
host has its own config file and generates its own secret. A cross-host cluster
must therefore pin one password explicitly on every host, read from the first
member with `pg addon password`.

(`--cluster create` authenticates every operand with that one password, so a
mismatch surfaces as a per-node auth error.)

> **Status:** cross-host clusters are verified end-to-end across three hosts
> (`--advertise-host` → `--cluster-announce-*`, cross-host `--cluster create`,
> MOVED redirects between hosts, and self-heal after a stop/start on each
> host). The single-host flow is additionally covered by the automated e2e
> (`test/addon/test_redis_cluster.sh`), which tests both shapes there: a
> three-master group and a 3-master × 1-follower group (which is how the
> missing-`--masterauth` reconnect storm was caught). The multi-host case is so
> far a manual procedure, not yet in the suite.

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
already explicit or live on the host. A [native-cluster](#native-cluster)
member is no different here — the cluster bus port is always Redis's own
`client+10000`, derived, never drawn from the pool.

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
      # cluster: app                     # set by --cluster: this is a native-cluster member
      # cluster_replicas: 1              # set by --cluster-replicas: followers per master for --cluster create (0 = masters-only; group-shared)
      # advertise_host: 10.10.0.158      # set by --advertise-host: peer-visible address (cross-host only)
      autostart: false                   # pg autostart enable --redis --name cache
```

Edits to `listen`, `port`, `password`, `maxmemory`/`maxmemory_policy`,
`aof`/`appendfsync`/`save`, `replica_host`/`replica_port`,
`cluster`/`cluster_replicas`/`advertise_host`, `image_tag`/`version`, or
`data_dir` take effect after the next `pg addon install redis --name cache
--force` (or via the matching flags). The replica and cluster roles are normally
set by the `--replica-of*` / `--cluster` (incl. `--cluster-replicas`) /
`--advertise-host` flags rather than hand-edited — see [Read
replicas](#read-replicas) and [Native cluster](#native-cluster).

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

`pg addon list` never prints the password. To reveal it deliberately, add
`--show-password` (it appends a `Password:` and a ready-to-paste `Raw DSN:`
line to each Redis instance — and a `Root password:` line to each object store),
or print just one instance's value bare on stdout for scripting:

```bash
pg addon password redis --name cache
export REDISCLI_AUTH="$(pg addon password redis --name cache)"
```

`pg addon password` takes the same `--name` as the other subcommands (defaulting
to the addon's own name) and a `--file` that writes the value mode 0600 instead
of to the terminal — prefer `--file` so the secret stays out of shell history
and scrollback.

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
  instance has a `requirepass`. Get it with `pg addon password redis --name
  cache`, or use `pg redis-cli`, which injects it.
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
  ([Read replicas](#read-replicas)) and native-cluster sharding is supported
  ([Native cluster](#native-cluster)), but the HA *control plane* is not:
  **Sentinel** is out of scope, and so is cluster **orchestration** — pgcli
  installs cluster members but never assembles, rescales, or rebalances them.
  A replica gives read scaling and a redundant copy of the data, not a
  self-healing pair — when the master dies you promote a replica yourself
  (`replicaof no one`, above). A single instance with no replica is a single
  point: it can be made *durable* on disk with `--aof`, but it cannot fail over
  to another node.
- **Native cluster: assembly and reshaping stay manual** — `--cluster` installs
  cluster-enabled nodes and `--cluster-replicas N` (echoed into the suggested
  command) lets each master have followers, so a follower auto-promotes when its
  master dies. What pgcli does NOT do is run `--cluster create`/`add-node`/
  resharding/`del-node` for you, and removing a member (`pg addon remove`) is a
  plain delete with no slot reassignment — shrink or reshape the cluster at the
  Redis layer first (move its slots off, forget the node), then remove it. Also
  note a masters-only group (`--cluster-replicas 0`) has no follower to promote:
  there a dead master takes its slot range down with it until you intervene.
- **Cross-host cluster (`--advertise-host`) is manual, not in the e2e suite** —
  the flag, the `--cluster-announce-*` argv it produces, and the
  firewall/bus-port rules are implemented, unit-tested, and verified by hand
  across three hosts (assembly, cross-host MOVED redirects, and self-heal all
  pass). The single-host flow (masters-only and 3-master × 1-follower) has an
  automated e2e; cross-host-with-followers has no automated e2e but relies on
  the same per-member `--masterauth` that makes followers link up on one host.
  See the status note in [Native cluster](#native-cluster).
- **No TLS** — Redis 7's native TLS exists in some builds but is not wired
  here; treat the network path as trusted and rely on `requirepass`, or keep
  the instance loopback-only (`--listen 127.0.0.1`). For a cross-host cluster
  that means the cluster bus traffic between members is unencrypted as well.
- **macOS is code-complete, untested** — the bridge path (published ports,
  `host.containers.internal` for `pg redis-cli`) mirrors the object stores; it
  has not yet been exercised on a Mac.
- **Default persistence is RDB granularity** — a crash loses writes since the
  last snapshot. Enable `--aof` when that gap matters (at `everysec` you still
  lose at most one second; `always` closes it); accept snapshots-only for
  cache/session data, or move durable data to PostgreSQL.
