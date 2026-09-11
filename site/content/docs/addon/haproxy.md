---
title: "HAProxy"
description: "Run HAProxy as a pgcli addon — a TCP load balancer in front of a Patroni cluster, in unified (read-write) or split (read/write separation) mode"
weight: 48
---

[HAProxy](https://www.haproxy.org) is the load balancer the Patroni docs
recommend putting in front of a cluster: clients connect to one stable address,
HAProxy health-checks each member's REST API, and routes writes to the current
leader (and, in read/write-split mode, reads to the replicas). pgcli runs it as
a **standalone, top-level addon** — shared routing infrastructure, not a
per-instance sidecar — pinned to the official `haproxy:3.2.23-alpine` image.

> **Platform support:** the HAProxy addon is **Linux-only** for now. It reaches
> Patroni members over the host network, which the macOS `podman machine` does
> not expose to containers. On macOS `NewHAProxyManager` fails fast with a
> clear message; `pg addon list` still shows installed instances without live
> status.

## How It Works

Routing follows the [official Patroni `haproxy.cfg`](https://github.com/patroni/patroni/blob/master/haproxy.cfg)
pattern. Each backend is a Patroni member; two ports matter per member:

- the **PostgreSQL port** — the TCP port HAProxy actually forwards connections to;
- the **REST API port** — where HAProxy sends its `httpchk` health check.

Patroni's REST API answers these GETs (unauthenticated by design):

| Endpoint | Returns 200 on |
|----------|----------------|
| `GET /` | the leader only |
| `GET /replica` (optionally `?lag=<max>`) | replicas only, and only if their lag is within the limit |

So the leader is the sole `UP` server for a write listener, and the replicas are
the sole `UP` servers for a read listener — failover is picked up automatically
as the health checks flip. pgcli renders this into
`<base-dir>/addon/haproxy/<name>/haproxy.cfg` and bind-mounts it read-only into
the container.

### Two modes

The mode is chosen with `--mode`:

- **`unified`** (default) — a single listener sends **all** traffic to the
  current leader. Simple; every connection can read and write. On leader change
  the write listener uses `on-marked-down shutdown-sessions` to drop stale
  connections so clients reconnect to the new leader.
- **`split`** — read/write separation: a `_<name>_rw` listener routes writes to
  the leader, and a separate `_<name>_ro` listener spreads reads across the
  replicas (round-robin, lag-filtered). Two client ports instead of one.

### Stats page

Every instance also gets an HTTP **stats** listener so you can watch server
up/down state in a browser: `http://<listen>:<stats-port>/`.

## Install

Backend members are supplied one of two ways (mutually exclusive):

- **`--node NAME=HOST:PGPORT:RESTPORT`** (repeatable) — list them explicitly;
- **`--ha <scope>`** — auto-derive every **local** member of a Patroni scope
  that `pg ha` manages, using each member's PostgreSQL and REST API ports.

What each `--node` field means — for `node1=10.0.0.11:35532:8008`:

| Field | Meaning |
|-------|---------|
| `node1` | member name — becomes the `server` name in `haproxy.cfg` and the label in the stats page |
| `10.0.0.11` | address where this member is reachable from this host (the member's `advertise-host`, or `127.0.0.1` on the same host) |
| `35532` | the member's PostgreSQL port that HAProxy forwards connections to |
| `8008` | the member's Patroni REST API port, used for the health check |

With `--ha`, all four come from `pg.yaml`: the Patroni member name, its
`advertise-host` (default `127.0.0.1`), and its `host_port` / `restapi_port`.

```bash
# unified: one port, everything to the leader, backends auto-derived from scope "app"
pg addon install haproxy --name lb --ha app

# split: rw + ro listeners, reads capped at 1MB replica lag
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB

# explicit backends (e.g. members on other hosts, or a scope pgcli doesn't manage)
pg addon install haproxy --name lb --mode split \
  --node node1=10.0.0.11:35532:8008 \
  --node node2=10.0.0.11:35533:8009 \
  --node node3=10.0.0.11:35534:8010
```

The output reports the assigned ports and a ready-to-use connect URL:

```
✓ haproxy installed: "lb"
  Container:    pgcli-haproxy-default-lb
  Image:        docker.io/library/haproxy:3.2.23-alpine
  Mode:         split
  Patroni:      app
  Backends:     3
  Read-write:   127.0.0.1:5000
  Read-only:    127.0.0.1:5001 (lag ≤ 1MB)
  Stats:        http://127.0.0.1:5002/
  Config:       ~/pg/addon/haproxy/lb/haproxy.cfg

  Connect via HAProxy:
    postgres://<user>@127.0.0.1:5000/<database>
```

### Adding or removing members later

The backend list follows the scope's **local** members recorded in `pg.yaml`, so
any topology change — or removal — is picked up by re-running the **same**
install command: it re-derives the full backend list and recreates the
container.

Added with `pg ha create <scope> --member <m>`, the new member is not in the
running HAProxy config yet:

```bash
pg ha create app --member node3 --etcd m1
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB
# Backends: 2 -> 3, and haproxy.cfg gains: server node3 127.0.0.1:35534 ... check port 8011
```

Symmetrically, after `pg ha remove <scope> --member <m>` the removed member
would otherwise linger in the config as a stale backend — re-running install
drops it:

```bash
pg ha remove app --member node3
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB
# Backends: 3 -> 2, and the server node3 line is gone
```

For an explicit `--node` install there is no auto-derivation: edit the `--node`
list itself (add or drop a spec) and re-run — the target list is replaced
wholesale each time, so the command always reflects the full set you want.

### Cross-host clusters

`--ha` derives backends from **this host's** `pg.yaml`, which lists only the
members this host's `pg ha create` manages. A member living on another host was
never a backend here — adding or removing one *there* requires no re-sync on
*this* host (and leaves nothing stale). To front the whole cluster through one
HAProxy, use an explicit `--node` list, with each member's advertised host:

```bash
pg addon install haproxy --name lb --mode split \
  --node node1=10.0.0.11:35532:8008 \
  --node node2=10.0.0.12:35532:8008 \
  --node node3=10.0.0.13:35532:8008
# later: add one more --node spec (or drop one) and re-run — the list replaces wholesale
```

Caveat: an instance installed with `--ha` on one host routes writes only to a
*local* leader — if failover promotes a remote member, the rw listener has no
UP server until the leader moves back. For cross-host clusters where the leader
can move, prefer the explicit all-members `--node` list (or run one HAProxy per
host and front it with a higher-level VIP/DNS).

## Ports

Ports are auto-assigned from a per-host pool, three consecutive free ports per
instance (rw, then ro if split, then stats), starting at `haproxy_start_port`
(default **5000**). Override any of them explicitly:

```bash
pg addon install haproxy --name lb --ha app \
  --rw-port 6000 --ro-port 6001 --stats-port 6002
```

Because the pool scans for free ports and tracks what every HAProxy instance
has already taken, you can run **many instances on one machine** without
collisions. A `split` instance consumes 3 ports (rw / ro / stats), a `unified`
instance 2 (rw / stats):

```bash
pg addon install haproxy --name lb1 --mode split --ha app     # 5000 / 5001 / 5002
pg addon install haproxy --name lb2 --ha report              # 5003 / 5004
```

The bind address defaults to `127.0.0.1`; set `--listen 0.0.0.0` — or the
`listen` key in `pg.yaml` — to expose the listeners on the network. Keep it on
loopback unless the backends require otherwise; there is no authentication in
front of PostgreSQL here.

## Configuration

Everything lives under the top-level `addons.haproxy` map in `pg.yaml`, keyed by
instance name:

```yaml
namespace: default
haproxy_start_port: 5000
addons:
  haproxy:
    lb:
      mode: split            # unified | split
      ha_scope: app          # scope the backends came from (for --ha re-derivation)
      listen: 127.0.0.1
      write_port: 5000
      read_port: 5001
      stats_port: 5002
      replica_max_lag: 1MB   # split mode only
      autostart: false       # pg autostart enable --haproxy --name lb
      image_tag: docker.io/library/haproxy:3.2.23-alpine
      targets:
        - { name: node1, host: 127.0.0.1, pg_port: 35532, rest_port: 8008 }
        - { name: node2, host: 127.0.0.1, pg_port: 35533, rest_port: 8009 }
```

`targets` is rendered from and re-derived by install; edit `mode`, ports,
`replica_max_lag`, or `image_tag` here for fine control, then
`pg addon install haproxy --name lb ...` to apply. The rendered `haproxy.cfg`
carries **no secrets** — only hosts, ports, and health-check URIs.

### List

```bash
pg addon list
```

```
Infra add-ons (haproxy):
  haproxy (name: lb)
    Status:      running
    Mode:        split
    Patroni:     app
    Read-write:  127.0.0.1:5000
    Read-only:   127.0.0.1:5001
    Stats:       http://127.0.0.1:5002/
    Backends:    3
      - node1        127.0.0.1:35532 (check :8009)
      - node2        127.0.0.1:35533 (check :8010)
      - node3        127.0.0.1:35534 (check :8011)
```

## Start and stop

After a host reboot, bring an instance back without re-rendering config:

```bash
pg addon start haproxy --name lb
pg addon stop  haproxy --name lb
```

`install` always recreates the container so config changes take effect;
`start` only starts an existing one (and self-heals an improper state by
recreating from the `haproxy.cfg` on disk).

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring an instance up after a host reboot:

```bash
pg autostart enable --haproxy --name lb
```

This sets `autostart: true` and installs/refreshes the boot service (see
[Auto-start on Boot](/docs/autostart/)). Boot is **start-only**: it starts the
existing container reading the `haproxy.cfg` already on disk, and never
re-renders config — install the instance first. The boot service starts
HAProxy **after** the Patroni members, so its backends are already coming up.
`pg autostart status` lists every target's state.

## Remove

```bash
pg addon remove haproxy --name lb
```

Stops and removes the container and deletes its config directory.

## Logs

```bash
pg logs addon haproxy --name lb       # last 50 lines
pg logs addon haproxy --name lb -f    # follow
```

The container logs to stdout (`log stdout format raw local0 info`), so health-check
transitions and connection events show up here.

## Troubleshooting

- **All backends DOWN in the stats page.** The health check hits each member's
  REST API port. Confirm it is reachable — `curl http://<host>:<rest_port>/`
  returns 200 on the leader, 503 elsewhere; `/replica` is the mirror image. A
  `000` means the REST API is down or the port is wrong.
- **`--ha` finds no members.** `--ha` only derives **local** members that
  `pg ha` manages in `pg.yaml`. For remote members, or a scope pgcli doesn't
  manage, list them with `--node`.
- **Port collision on manual `--rw-port`.** Pick a port outside the auto pool
  (`haproxy_start_port` and up) or the auto-assigner will treat it as taken.
- **macOS.** Not supported yet — HAProxy needs host networking to reach Patroni
  members, which the podman machine VM does not provide to containers.
