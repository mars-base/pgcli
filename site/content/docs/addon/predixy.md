---
title: "Predixy"
description: "Run Predixy as a pgcli addon — a Redis protocol proxy that exposes a native cluster as one plain redis:// endpoint, so cluster-unaware clients need no MOVED handling"
weight: 51
---

[Predixy](https://github.com/joyieldInc/predixy) is a high-performance Redis
protocol proxy. pgcli runs it as a **standalone, top-level addon** whose job is
to take a [Redis native cluster](../redis/#native-cluster) — 3+ nodes, 16384
slots, `MOVED` redirects — and hand it back to your applications as **one plain
`redis://` endpoint**. Clients do not need to be cluster-aware: no `-c`, no
redirect handling, just connect and run commands.

> **Platform support:** like HAProxy and Redis, the Predixy addon needs **Linux
> host networking** to dial its backend nodes; it is not supported on macOS.

## How It Works

Predixy speaks the Redis protocol on its client port and keeps persistent
connections to every cluster node listed in `--backend`. A client request is
routed by slot: if the node it lands on does not own the key's slot, Predixy
follows the `MOVED` itself and re-issues the command on the right node —
transparently, in-proxy. The client never sees a redirect.

pgcli renders one config file per instance,
`<base-dir>/addon/predixy/<name>/predixy.conf`, and bind-mounts it
(file-level, read-only) over the image's baked-in
`/usr/local/predixy/conf/predixy.conf`. The rendered file's
`Include license.conf` resolves relative to that directory, so the image's
signed license sibling stays visible — pgcli never ships or manages a license
(see [Known limitations](#known-limitations)).

### One password, two sides

The proxy sits between two authenticated parties — its own clients and the
cluster behind it — and pgcli uses **one password for both**:

- `Authority { Auth "<pw>" { Mode admin } }` — clients must `AUTH` this password
  to use the proxy;
- `ClusterServerPool { Password "<pw>" }` — Predixy authenticates to the cluster
  nodes with it.

That works because the password is *the proxied cluster's own `requirepass`*:
clients already know it, the nodes already accept it. Read it straight out of
the redis addon:

```bash
PW=$(pg addon password redis --name n1)
```

A single cluster-member password therefore gates the proxy endpoint too — no
second secret to keep in sync. (If you want a separate client-facing password,
that is a different Authority design pgcli does not render; see
[Known limitations](#known-limitations).)

## Install

`--backend` and `--password` are both **required**. `--backend` is the cluster's
**full node list** — pgcli never derives it from local redis addons, because the
cluster being fronted often lives on other hosts; you know its topology, so you
list it. Repeat the flag or comma-separate one run; duplicates are rejected.

```bash
# single-host cluster (members on 127.0.0.1:6379/6380/6381)
PW=$(pg addon password redis --name n1)
pg addon install predixy --name proxy \
  --backend 127.0.0.1:6379,127.0.0.1:6380,127.0.0.1:6381 \
  --password "$PW"

# cross-host cluster — one member per host at the default port
pg addon install predixy --name app-proxy \
  --backend 10.10.0.158:6379,10.10.0.159:6379,10.10.0.160:6379 \
  --password "$PW" --workers 2
```

The summary prints the one endpoint clients should be pointed at:

```
✓ predixy installed: "proxy"
  Container:    pgcli-predixy-default-proxy
  Image:        ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine
  Listen:       0.0.0.0:7617
  Workers:      2
  Backends:     3 (127.0.0.1:6379, 127.0.0.1:6380, 127.0.0.1:6381)
  Config:       ~/pg/addon/predixy/proxy/predixy.conf

  Password:     <the one password>
  Raw DSN:      redis://:<password>@0.0.0.0:7617/0

  Client:       redis-cli -h 0.0.0.0 -p 7617 -a '<password>' ping
  The proxy absorbs MOVED: an ordinary redis client — no -c, no
  redirect handling — reads and writes the whole cluster key space
  through this one endpoint.
```

Guards run **before anything is pulled or created**: a missing `--backend`, a
malformed entry (no port, non-numeric or out-of-range port, empty entry), a
duplicate node, a missing `--password`, a password containing `"` or a newline
(Predixy's quoted-string syntax has no escape for them), or `--workers < 1` all
fail fast with a clear message.

Re-running `install` is idempotent as elsewhere: a running container is left
alone (config rewritten), a stopped one is started, and `--force` recreates it
so a changed port/listen/password/backend set takes effect. The backend list is
**replaced wholesale** each time, so growing the cluster (install more redis
members, `--cluster add-node`, …) is followed by re-running the same install
with the updated full list.

### Workers

`--workers N` sets Predixy's `WorkerThreads` (default **1**). Each worker is a
busy-poll event thread; on a multi-core host dedicated to proxying, N ≈ the
number of cores you are willing to spend scales throughput roughly linearly.

## Using the proxy

Any Redis client connects to the proxy's port with the cluster's password and
behaves as if it were talking to a single standalone Redis — the whole keyspace
is served through the one endpoint:

```bash
redis-cli -h 127.0.0.1 -p 7617 -a "$PW" set foo bar    # OK — no MOVED, no -c
redis-cli -h 127.0.0.1 -p 7617 -a "$PW" get foo        # "bar"
```

(DSN shape: `redis://:<password>@<listen>:<port>/0`.)

### With `pg redis-cli`

`pg redis-cli` reaches the proxy through `--host`/`--port` — there is no
`--name` path to a Predixy instance (`--name` resolves *redis* addons only):

```bash
# on the proxy's own host
pg redis-cli --host 127.0.0.1 --port 7617 set foo bar
pg redis-cli --host 127.0.0.1 --port 7617 get foo

# from a host with no local redis addon: the default-major image, password via env
REDISCLI_AUTH=$PW pg redis-cli --host 10.10.0.158 --port 7617 ping
```

With a local redis addon configured, its image and password are reused (see
[redis.md](../redis/#using-redis) — `pg redis-cli`). If that addon happens to be
a cluster member, pg redis-cli injects its `-c` as usual — harmless through the
proxy, because the proxy absorbs every `MOVED` before the client could see one.
That is the point of the proxy: the same plain-client commands work whether or
not `-c` is in play.

### Cross-host

Verified end-to-end: a proxy on one host fronting a three-host cluster
(`--backend 10.10.0.15x:6379,…`), with plain cluster-unaware clients connecting
from a *different* host through it — `SET` whose slot belongs to a remote node
is absorbed and applied, and reads back from that node's own port. The only
requirement is reachability: the proxy host must be able to dial every
`--backend` node's client port (the cluster bus ports are between the members,
not the proxy).

## Ports

Each instance takes one port from Predixy's own pool, `predixy_start_port`
(default **7617**, the port the image `EXPOSE`s) — separate from Redis's
`redis_start_port` pool, so a proxy and the cluster it fronts never collide.
`--port` pins an instance; auto-assignment skips what is already taken.

The bind address defaults to `0.0.0.0` (the AUTH password is the only gate);
`--listen 127.0.0.1` tightens it to loopback.

## Configuration

Instances live under the top-level `addons.predixy` map in `pg.yaml`:

```yaml
namespace: default
predixy_start_port: 7617
addons:
  predixy:
    proxy:
      container_name: pgcli-predixy-default-proxy
      name: proxy
      image_tag: ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine
      listen: 0.0.0.0
      port: 7617
      workers: 2
      backend:
        - 127.0.0.1:6379
        - 127.0.0.1:6380
        - 127.0.0.1:6381
      password: <the cluster requirepass>   # written by install, never generated
      autostart: false                      # pg autostart enable --predixy --name proxy
```

The rendered `predixy.conf` carries the same information in Predixy's own
syntax; hand-editing it is wiped by the next install — edit `pg.yaml` (or the
flags) and re-install instead.

### List

```bash
pg addon list
```

```
Infra add-ons (predixy):
  predixy (name: proxy)
    Status:      running
    Address:     0.0.0.0:7617
    Workers:     2
    Backends:    3 (127.0.0.1:6379, 127.0.0.1:6380, 127.0.0.1:6381)
    Client DSN:  redis://:<password>@0.0.0.0:7617/0
    Image:       ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine
    Container:   pgcli-predixy-default-proxy
    Config:      ~/pg/addon/predixy/proxy/predixy.conf
```

`pg addon list` redacts the password; `--show-password` reveals it, and
`pg addon password predixy --name proxy` prints just that value bare for
scripting.

## Start and stop

```bash
pg addon start predixy --name proxy
pg addon stop  predixy --name proxy
```

`start` only starts an existing container (self-healing an improper state by
recreating it from the rendered config on disk); it errors if that file is
missing — re-run `install` to regenerate it.

## Auto-start on Boot

```bash
pg autostart enable --predixy --name proxy
```

Boot is **start-only** (install first), and the boot service starts Predixy
**after** Redis, so the proxied members are already coming up when the proxy
dials them. See [Auto-start on Boot](/docs/autostart/).

## Remove

```bash
pg addon remove predixy --name proxy
```

Stops and removes the container, deletes the rendered config directory, and
drops the `pg.yaml` entry. The proxied cluster is untouched — the proxy is
merely a consumer of it.

## Logs

```bash
pg logs addon predixy --name proxy      # last 50 lines
pg logs addon predixy --name proxy -f   # follow
```

The startup banner reports the effective build (`Workers:2`, …) — the place to
confirm `--workers` landed.

## Known limitations

- **One admin Authority user** — the renderer emits a single
  `Auth "<pw>" { Mode admin }`: every client of the proxy shares one password
  with full access. Per-user auth, read-only users, and keyspace restrictions
  are not rendered (Predixy itself supports more; pgcli's surface stops here).
- **Cluster pools only** — `ClusterServerPool` is the only backend type;
  Sentinel or standalone pools are not configured.
- **License lives in the image** — the free-edition license baked into
  `ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine` caps clients at
  `ClientLimit 128` and expires **2026-12-31**; pgcli never ships or rotates it
  — the image build does. When it expires, rebuild the image (the Makefile
  target already swaps in the current license).
- **No TLS on the client side** in this addon (password-only gate; keep
  `listen` loopback or a trusted network, as with Redis itself).
- **Linux only** — host networking is required to dial the backends; macOS is
  not supported (fails fast).

## Troubleshooting

- **`ERR invalid password` from clients** — the client must `AUTH` the one
  password; check it against `pg addon password predixy --name proxy` (it
  should equal the cluster's, `pg addon password redis --name n1`).
- **Container exits at once** — Predixy rejects its config on the spot: a lone
  `{` on its own line, or a password with a `"`/newline that broke quoting.
  pgcli validates both before writing, so this points at a hand-edited
  `predixy.conf` — re-install.
- **Commands hang or every key errors** — a backend node is unreachable from
  the proxy host (wrong address, firewalled). `pg logs addon predixy` shows the
  dial failures; `--backend` must list nodes *this host* can dial.
- **Stale backend list after cluster changes** — install replaces the list
  wholesale; re-run it with the current full node set after adding or removing
  members.
