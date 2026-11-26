---
title: "Valkey"
description: "Run Valkey as a pgcli addon — the Linux Foundation's BSD-3 fork of Redis, with its own major line 7/8/9, RDB/AOF persistence, read replicas and native cluster sharding"
weight: 50
---

[Valkey](https://valkey.io/) is the Linux Foundation's **BSD-3 licensed fork of
Redis 7.2.4** — same wire protocol, same `--requirepass`, same RDB/AOF files,
same replication and native-cluster protocols. pgcli ships it as its **own
top-level addon type**, not as "redis with another image": `pg addon install
valkey` gets its own config namespace (`addons.valkey`), its own image table
(`docker.io/valkey/valkey`), its own container prefix (`pgcli-valkey…`), its own
data directory (`addon/valkey/<name>/data`), its own port pool
(`valkey_start_port`, base **6380**) and its own command passthrough
(`pg valkey-cli`).

Feature parity with the [redis](../redis/) addon is deliberate: standalone
master, read replicas (`--replica-of`), native cluster members (`--cluster`),
RDB/AOF persistence, generated `requirepass`, list/logs/password, autostart, and
the same Ansible single + cluster layouts. Only the labels and binaries differ
(`valkey-server` / `valkey-cli`).

> **Valkey majors are their own line.** Valkey forked from Redis 7.2.4 but
> versions independently: majors **7, 8 and 9** (default **9**) do **not**
> correspond to Redis majors. The redis addon's table is never consulted — and
> a redis addon and a valkey addon are independent namespaces, so they can even
> share a name, with ports seeded apart so they do not collide.

> **Out of scope:** Sentinel (automatic failover) is not managed, the same as
> for redis. The *native cluster* is supported — `--cluster` turns instances
> into cluster-enabled members. With direct `pg addon install` commands, assemble
> them once yourself with `pg valkey-cli -- --cluster create`; the Ansible
> `addon_valkey_cluster` layout assembles the cluster after all members are up.
> Read replicas (`--replica-of`) are supported too.

## Parameters

One flag per knob, each mapping to a native `valkey-server` option — no config
file is generated, and one container per instance runs the plain upstream image:

| Flag / `pg.yaml` key | Default | Valkey option | Meaning |
|----------------------|---------|---------------|---------|
| `--version` / `version` | `9` | — (picks the image tag) | major to install (see [Version selection](#version-selection)) |
| `--image` / `image_tag` | resolved from the version table | — | override the tag verbatim |
| `--port` / `port` | auto from `valkey_start_port` (base **6380**) | `--port` | host port |
| `--listen` / `listen` | `0.0.0.0` | `--bind` | bind address (`127.0.0.1` to tighten) |
| `--password` / `password` | generated, 20 chars | `--requirepass` | auth; always required (a replica's must equal the master's) |
| `--maxmemory` / `maxmemory` | unset (no cap) | `--maxmemory` | dataset cap that turns eviction on |
| `--maxmemory-policy` / `maxmemory_policy` | `allkeys-lru` | `--maxmemory-policy` | eviction policy, only under a cap |
| `--aof` / `aof` | off | `--appendonly yes` | write-ahead log on top of the snapshot |
| `--appendfsync` / `appendfsync` | `everysec` (Valkey's own) | `--appendfsync` | AOF flush strength, only with `--aof` |
| `--save` / `save` | Valkey's default schedule | `--save` | RDB snapshot plan; `no` disables snapshots |
| `--io-threads` / `io_threads` | unset (server default `1`) | `--io-threads` | network-I/O threads (1-128); command execution stays single-threaded — **keep it ≤ 4**, gains past that are negligible |
| `--replica-of` / `replica_host`+`replica_port` | unset (master) | `--replicaof` + `--masterauth` | read-only replica of a *local* valkey master addon (host/port/password resolved from the config) |
| `--replica-of-host` / `replica_host` | — | `--replicaof` | master *address* for a cross-host replica (pair with `--replica-of-port` + `--password`) |
| `--replica-of-port` / `replica_port` | — | `--replicaof` | master port, paired with `--replica-of-host` |
| `--cluster` / `cluster` | unset (standalone) | `--cluster-enabled yes` + `--masterauth` | native-cluster member: non-empty is the group name — same value means same cluster, and every member shares the first member's password. Mutually exclusive with `--replica-of`/`--replica-of-host`. You assemble the cluster yourself — see [Native cluster](#native-cluster) |
| `--cluster-replicas` / `cluster_replicas` | `0` (masters-only) | — (echoed into the suggested `--cluster create` only) | per-master followers for the assemble step; a group property inherited from the first member like the password |
| `--advertise-host` / `advertise_host` | unset (`127.0.0.1`, single-host) | `--cluster-announce-ip`/`-port`/`-bus-port` | the address this member announces to peers — required for a cross-host cluster (the cluster bus has no NAT/port remapping) |
| `--data-dir` / `data_dir` | `<base-dir>/addon/valkey/<name>/data` | `--dir /data` (host dir bind-mounted at `/data`) | where the data lives |
| — | `autostart: false` | — | start on boot (`pg autostart enable --valkey`) |

How the knobs combine — identical rules to redis, enforced by the same shared
validators:

- **`requirepass` is always on** — an unauthenticated Valkey is not a reachable
  state.
- **Persistence defaults to RDB snapshots** — Valkey's own `save` schedule,
  plus a final snapshot on shutdown (the stop timeout gives it time to write)
  and a replay from `dump.rdb` at start.
- **`--aof` adds the write-ahead log** (`--appendonly yes`) alongside the
  snapshot; `--appendfsync` tunes its flush (`always` / `everysec` / `no`). The
  AOF is replayed before the snapshot when both are on.
- **`--save no` disables snapshots** — pair it with `--aof` for an AOF-only
  store; on its own it is a purely in-memory instance that loses its dataset on
  restart (the install warns).
- **`--maxmemory` implies eviction**, defaulting to `allkeys-lru`; pass
  `--maxmemory-policy noeviction` for a hard ceiling. `--appendfsync` requires
  `--aof`; both combinations are rejected before anything is pulled.
- **`--io-threads N` parallelizes network I/O only** — Valkey executes commands
  on the main thread; the extra threads just read/write sockets. The gain
  flattens quickly: 2-4 is the useful range, and **the recommendation is not to
  exceed 4**. The servers accept 1-128; `0` (the default) leaves the server's
  own value of 1 in place.
- **`--replica-of` / `--cluster` are mutually exclusive** — an instance is one
  role or the other. Group members share the first member's password and
  `--cluster-replicas`; an explicit mismatch is rejected up front.

## Version selection

`--version` picks a major; the Valkey table maps each to a pinned patch tag of
`docker.io/valkey/valkey`:

| Major | Default image tag | Select with |
|-------|-------------------|-------------|
| 7 | `docker.io/valkey/valkey:7.2.16` | `pg addon install valkey --name vcache --version 7` |
| 8 | `docker.io/valkey/valkey:8.1.12` | `pg addon install valkey --name vcache --version 8` |
| 9 | `docker.io/valkey/valkey:9.2.0` | `pg addon install valkey --name vcache` (9 is the default) |

Resolution rules:

- `--version 7|8|9` → the table's tag is stored in `image_tag`, the major in
  `version`;
- `--image <tag>` → your tag wins outright; the major is reverse-parsed from it
  for display when it is a valkey tag, otherwise empty (the tag is shown
  verbatim);
- neither → major `9`;
- an unknown `--version` → error listing the available valkey majors.

All majors can run side by side on one host — different tags, different ports
from the same `valkey_start_port` pool. Do **not** map Redis majors onto
Valkey: `redis` major 8 and `valkey` major 8 are unrelated version lines.

## Install

```bash
# default: major 9, instance name "valkey", port auto-assigned from 6380
pg addon install valkey

# a named cache instance on major 7 with a memory cap
pg addon install valkey --name vcache --version 7 --maxmemory 256mb

# loopback-only instead of the all-interfaces default
pg addon install valkey --name vcache --listen 127.0.0.1

# pin the password (stable across reinstalls) and a fixed port
pg addon install valkey --name sessions --password 'S3ssions!' --port 6380

# a read-only replica of the local "sessions" instance
pg addon install valkey --name sessions-ro --replica-of sessions
```

The install summary prints everything a client needs:

```
✓ valkey installed: "vcache"
  Container:  pgcli-valkey-default-vcache
  Version:    9
  Image:      docker.io/valkey/valkey:9.2.0
  Data:       ~/pg/addon/valkey/vcache/data
  Address:    0.0.0.0:6380
  Role:       master
  Persistence: rdb snapshots (default save schedule)

  Password:    <generated>

  Client:      pg valkey-cli ping
  Raw DSN:     redis://:<password>@0.0.0.0:6380/0
  NOTE: listening on every interface; the password above is the only gate.
        loopback-only: pg addon install valkey --name vcache --listen 127.0.0.1 --force
```

The DSN keeps the `redis://` scheme because that is what every Redis-protocol
client expects — Valkey speaks the same wire protocol.

> **Security model.** `listen` defaults to **`0.0.0.0`**; on Linux (host
> networking) the port is published on every interface and `requirepass` is the
> only gate. Pass `--listen 127.0.0.1` to keep an instance loopback-only.
> `pg addon list` and the logs never print the password; reveal it with `pg
> addon password valkey --name vcache` or `pg addon list --show-password`.

Re-running `install` is idempotent: a running container is left alone, a
stopped one is started. Add `--force` to apply changed
ports/listen/password/image/persistence (the container is recreated; the data
dir is untouched).

## Using Valkey

### `pg valkey-cli`

`pg valkey-cli` runs Valkey's own client from a throwaway container of the
addon's image — no host install, no password typing:

```bash
pg valkey-cli ping                               # PONG
pg valkey-cli set session:42 '{"user":"alice"}'  # ok — auth is injected
pg valkey-cli get session:42
pg valkey-cli --name vcache lrange queue 0 -1
```

The local target is `--name <addon>` or, when omitted, the first valkey addon
by name (with several installed, the chosen target is announced on stderr).
pgcli's own flags are parsed only before the command word; everything after is
forwarded to `valkey-cli` verbatim.

**Reaching a remote endpoint** — `--host`/`--port` override where the client
connects, without registering the remote as an addon:

```bash
pg valkey-cli --host 10.0.0.7 ping
pg valkey-cli --host 10.0.0.7 --port 6381 --name vcache info

# no local addon at all: default-major image, password via REDISCLI_AUTH
REDISCLI_AUTH=s3cret pg valkey-cli --host 10.0.0.7 dbsize
```

Use these instead of the client's own `-h`/`-p`: those sit *after* the command
word and would be read as command arguments, while pgcli injects the connection
flags itself. `VALKEYCLI_AUTH` and `REDISCLI_AUTH` are both accepted by `pg
valkey-cli`; if both are set, `VALKEYCLI_AUTH` wins. pgcli forwards that value
under both names for compatibility, and otherwise uses the configured password:

```bash
VALKEYCLI_AUTH=otherpass pg valkey-cli dbsize
REDISCLI_AUTH=otherpass pg valkey-cli dbsize
```

### Application connections

Any Redis-protocol client works; the DSN printed at install has the shape
`redis://:<password>@<host>:<port>/0`.

## Read replicas

`--replica-of` turns a new instance into a **read-only replica** of an existing
valkey master addon: it streams the master's writes and serves reads, while
writes to it are rejected with `READONLY`. This is read scaling and data
redundancy, **not** high availability — there is no Sentinel, so a dead master
is not promoted automatically.

```bash
pg addon install valkey --name vcache
pg addon install valkey --name vcache-ro --replica-of vcache
```

A local `--replica-of` resolves everything from `addons.valkey`: the replica
adopts the master's version (cross-major replication is unsupported — a
mismatch is a fast error), its password, and the endpoint
`127.0.0.1:<master-port>`. A replica can only follow another **valkey** addon
— the redis and valkey namespaces are independent, so `--replica-of` never
reaches across types. For a cross-host master supply the address and the
master's password explicitly:

```bash
pg addon install valkey --name vcache-r2 \
    --replica-of-host 10.0.0.7 --replica-of-port 6380 --password <master-password>
```

`pg addon list` and the install summary show a `Role:` line (`master`, or
`replica of <host>:<port> (read-only)`), and `pg valkey-cli --name vcache-ro
info replication` reports `role:replica` with `master_link_status:up` once the
initial RDB sync completes.

To promote a replica by hand (the manual failover pgcli leaves to you):

```bash
pg valkey-cli --name vcache-ro replicaof no one   # stop following the master
pg addon remove valkey --name vcache-ro          # keep the data, forget the role
pg addon install valkey --name vcache-ro         # reinstall as a plain master
```

## Native cluster

`--cluster` turns instances into members of a **Valkey native cluster** —
sharding by slot (16384 of them), not a read replica of one master. Direct
`pg addon install` commands create cluster-enabled nodes but do not assemble
them; the Ansible `addon_valkey_cluster` layout assembles the cluster after all
members are up.

```bash
# 1. install the members — minimum 3 masters; same token = same cluster
pg addon install valkey --name n1 --cluster app --maxmemory 10mb
pg addon install valkey --name n2 --cluster app --maxmemory 10mb
pg addon install valkey --name n3 --cluster app --maxmemory 10mb

# 2. assemble once, through pg valkey-cli's passthrough (pgcli never runs this)
pg valkey-cli --name n1 -- --cluster create 127.0.0.1:6380 127.0.0.1:6381 127.0.0.1:6382 --cluster-replicas 0
```

Members of one group share the first member's password automatically (a later
member with a different `--password` is rejected up front), and each node's
argv carries `--masterauth` so any node Valkey promotes to a follower can
authenticate to its master. `--cluster` cannot be combined with
`--replica-of`/`--replica-of-host` on one install.

The install summary prints the exact `--cluster create` command built from the
group's current members — copy-pasteable rather than reconstructed by hand:

```
-> NOTE: native-cluster member of "app" — it comes up cluster-enabled but INCOMPLETE
   until you assemble the group once (see the "Cluster:" line below). pgcli does not
   run --cluster create for you.
...
  Cluster:     "app" — 3 masters configured. Assemble once (pgcli does not run this):
               pg valkey-cli --name n3 -- --cluster create 127.0.0.1:6380 127.0.0.1:6381 127.0.0.1:6382 --cluster-replicas 0
               (bus port = client+10000; for cross-host peers open it on the firewall, and give each member --advertise-host)
```

For followers, install `3*(1+N)` members and pass `--cluster-replicas N` on the
first one (the rest inherit it); the suggested command ends with
`--cluster-replicas N`, and which members become followers is Valkey's decision
at `--cluster create`.

### After assembling

`pg valkey-cli` pointed at a local cluster member is automatically a
cluster-aware client — pgcli injects valkey-cli's own `-c` flag so `get`/`set`
follow `MOVED` redirects to the node that owns the slot:

```bash
pg valkey-cli --name n1 set foo bar      # might be served by n3 via MOVED
pg valkey-cli --name n2 get foo          # follows the redirect again
pg valkey-cli --name n1 -- --cluster check 127.0.0.1:6380   # admin subcommand, no -c
```

The last line is the exception: when the command is valkey-cli's own `--cluster
<subcommand>`, pgcli suppresses the `-c` injection.

When you reach the cluster **remotely** with `--host`/`--port` and no local
addon is configured, pgcli cannot know the endpoint is a cluster node, so it
injects nothing; forward `-c` yourself after the `--` passthrough:

```bash
export REDISCLI_AUTH=<the group password>
pg valkey-cli --host 10.10.0.158 --port 6380 -- -c get foo
pg valkey-cli --host 10.10.0.158 --port 6380 -- -a <the group password> -c set foo bar
```

If your clients cannot be made cluster-aware at all, put a
[Predixy proxy](../predixy/) in front: it speaks the same protocol, follows
redirects itself and exposes one plain `redis://` endpoint.

The cluster bus is a **second port per member**, always `client port + 10000`
(e.g. `6380`→`16380`) — derived, not stored, and not drawn from the pool.
Topology is self-healing: each node's `nodes.conf` lives in its own bind mount,
so `pg addon stop`/`start` brings the cluster back with no re-create step.

```bash
# health checks through the passthrough
pg valkey-cli --host <ip> --port <port> -- -c cluster info
pg valkey-cli -- --cluster check <ip>:<port>
```

### Cross-host members: `--advertise-host`

The cluster bus does **not** support NAT or port remapping, so members on
different hosts must announce an address peers can reach:

```bash
# on 10.10.0.158 — the first member decides the group's one password; read it back:
pg addon install valkey --name n1 --cluster app --advertise-host 10.10.0.158
PW=$(pg addon password valkey --name n1)
# on 10.10.0.159 and 10.10.0.160 — carry that same password across:
pg addon install valkey --name n2 --cluster app --advertise-host 10.10.0.159 --password "$PW"
pg addon install valkey --name n3 --cluster app --advertise-host 10.10.0.160 --password "$PW"
# assemble once from any host that reaches all three:
pg valkey-cli --name n1 -- --cluster create \
    10.10.0.158:6380 10.10.0.159:6380 10.10.0.160:6380 --cluster-replicas 0
```

Open each member's **bus port (client+10000 — `16380` at the default
`6380`)** on the firewall between hosts. Password inheritance only works within
one `pg.yaml`; across hosts, every member must use the same password. Set it
explicitly on each host (as above), or otherwise ensure each install receives
that same value.

> **Status:** the single-host cluster flow is covered by the automated e2e
> (`test/addon/test_valkey_cluster.sh`); the cross-host procedure reuses the
> redis implementation and is verified by hand, like redis's.

## Ports

Each instance takes **one port** from Valkey's own pool, `valkey_start_port`
(default **6380**) — separate from the redis pool (`redis_start_port`, 6379)
and every other addon's pool. Assignment also seeds in the ports already
assigned to `addons.redis`, so a valkey instance will not auto-assign a port a
redis instance in the same config already holds:

```bash
pg addon install valkey --name vcache    # 6380
pg addon install valkey --name sessions  # 6381
```

`--port` fixes an instance to a specific port; auto-assignment skips anything
already explicit or live on the host. A cluster member is no different — the
bus port is always `client+10000`.

## Configuration

Instances live under the top-level `addons.valkey` map in `pg.yaml`:

```yaml
namespace: default
valkey_start_port: 6380    # Valkey's own pool, separate from redis_start_port
addons:
  valkey:
    vcache:
      container_name: pgcli-valkey-default-vcache
      name: vcache
      version: "9"                       # major as selected/stored
      image_tag: docker.io/valkey/valkey:9.2.0
      # data_dir: /srv/valkey-vcache      # omit for <base-dir>/addon/valkey/vcache/data
      listen: 0.0.0.0                    # default (all interfaces); 127.0.0.1 to tighten
      port: 6380
      password: <generated>              # written on first install
      # maxmemory: 256mb                 # optional cap
      # maxmemory_policy: noeviction     # only with a cap; default allkeys-lru
      # aof: true                        # write-ahead log on top of snapshots
      # appendfsync: always              # only with aof; default everysec
      # save: "900 1 300 10"             # RDB schedule; "no" disables snapshots
      # io_threads: 4                    # network I/O threads (1-128; keep ≤ 4)
      # replica_host: 127.0.0.1          # set by --replica-of: this is a read replica
      # replica_port: 6380               # the master's port
      # cluster: app                     # set by --cluster: this is a native-cluster member
      # cluster_replicas: 1              # followers per master for --cluster create
      # advertise_host: 10.10.0.158      # peer-visible address (cross-host only)
      autostart: false                   # pg autostart enable --valkey --name vcache
```

Edits to `listen`, `port`, `password`, `maxmemory`/`maxmemory_policy`,
`aof`/`appendfsync`/`save`, `io_threads`, `replica_host`/`replica_port`,
`cluster`/`cluster_replicas`/`advertise_host`, `image_tag`/`version`, or
`data_dir` take effect after the next `pg addon install valkey --name vcache
--force` (or via the matching flags).

### List

```bash
pg addon list
```

```
Infra add-ons (valkey):
  valkey (name: vcache)
    Status:      running
    Version:     9
    Address:     0.0.0.0:6380
    Role:        master
    Auth:        on (requirepass)
    Maxmemory:   256mb (allkeys-lru)
    Persistence: rdb + aof (default save schedule, appendonly yes)
    Data:        ~/pg/addon/valkey/vcache/data
    Image:       docker.io/valkey/valkey:9.2.0
    Container:   pgcli-valkey-default-vcache
    Client:      pg valkey-cli --name vcache ping
```

`pg addon list` never prints the password. Add `--show-password` to reveal it,
or print one instance's value bare on stdout for scripting:

```bash
pg addon password valkey --name vcache
export REDISCLI_AUTH="$(pg addon password valkey --name vcache)"
```

`pg addon password` takes the same `--name` (defaulting to "valkey") and a
`--file` that writes the value mode 0600 instead of to the terminal.

## Start and stop

```bash
pg addon start valkey --name vcache
pg addon stop  valkey --name vcache
```

`stop` sends a graceful SIGTERM — with the stop timeout Valkey writes its final
RDB before exiting. `start` only starts an existing container (and self-heals
an improper state by recreating it from the config).

## Auto-start on Boot

```bash
pg autostart enable --valkey --name vcache
```

Valkey autostart is **start-only** (install the instance first) and replays the
RDB/AOF from its data dir. Boot-time `pg start --autostart` starts valkey
right after redis, before proxies such as Predixy that front them.

## Remove

```bash
pg addon remove valkey --name vcache            # container + config entry; data kept
pg addon remove valkey --name vcache --clean-data  # also delete the data dir
```

Without `--clean-data` the host data dir (and its `dump.rdb`) stays in place —
reinstalling the same name revives the dataset. `--clean-data` deletes it,
falling back through `podman unshare` when rootless podman's subordinate uid
makes the files undeletable by the host user.

## Logs

```bash
pg logs addon valkey --name vcache        # last 50 lines
pg logs addon valkey --name vcache -f     # follow
```

## Troubleshooting

- **`NOAUTH Authentication required` from a raw client** — expected: every
  instance has a `requirepass`. Get it with `pg addon password valkey --name
  vcache`, or use `pg valkey-cli`, which injects it.
- **Image not found at install** — all three majors are plain
  `docker.io/valkey/valkey` images pulled on demand; on an air-gapped host,
  `podman load` the tar first (`valkey_7.2.16.tar` / `valkey_8.1.12.tar` /
  `valkey_9.2.0.tar`, see `docs/images.md`) and the pull is skipped.
- **A hand-edited `version` that is not in the table** — `install` errors and
  suggests `--image` rather than silently rewriting your value.
- **Port already live** — auto-assignment scans host listeners and skips them;
  pin `--port` when you need a stable number.
- **A replica rejects writes (`READONLY`)** — that is the role, not a fault:
  send writes to the master (see [Read replicas](#read-replicas)).
- **`--replica-of` cannot find the master** — the value is a name in
  `addons.valkey`, not in `addons.redis`; a valkey replica can only follow a
  valkey master.
- **`maxmemory` evicting more than you wanted** — pass
  `--maxmemory-policy noeviction` for a hard ceiling, or omit `--maxmemory`.
- **AOF losing the newest writes on a hard crash** — `everysec` (the default)
  can lose up to one second; use `--appendfsync always`. A graceful `pg stop`
  always flushes.

## Known limitations

- **No automatic failover** — read replicas and native-cluster sharding are
  supported, but there is no HA control plane: **Sentinel** is out of scope.
  The Ansible `addon_valkey_cluster` layout assembles the initial cluster but
  does not automatically rescale or rebalance it. A replica gives read scaling
  and a redundant copy, not a self-healing pair.
- **Native cluster: direct installs and reshaping stay manual** — direct
  `pg addon install` commands create cluster-enabled nodes but do not run
  `--cluster create`. The Ansible `addon_valkey_cluster` layout assembles the
  initial cluster automatically; `add-node`/resharding/`del-node` remain manual.
  Removing a member is a plain delete with no slot reassignment.
- **Cross-host cluster (`--advertise-host`) is manual, not in the e2e suite** —
  same status as redis: implemented, unit-tested and verified by hand; the
  single-host flow has the automated e2e.
- **No TLS** — treat the network path as trusted and rely on `requirepass`, or
  keep the instance loopback-only (`--listen 127.0.0.1`). Cross-host cluster
  bus traffic is unencrypted as well.
- **macOS is code-complete, untested** — the bridge path mirrors the object
  stores and has not yet been exercised on a Mac.
- **Default persistence is RDB granularity** — a crash loses writes since the
  last snapshot. Enable `--aof` when that gap matters.
- **A separate add-on, not a redis migration** — redis and valkey instances are
  independent namespaces with separate pools and commands; pgcli does not move
  an existing redis add-on to valkey for you. Install a valkey addon, point the
  application at it, and retire the redis one when done.
