---
title: "etcd"
description: "Run a standalone etcd cluster as a pgcli addon for HA / DCS use"
weight: 30
---

etcd is a distributed key-value store. pgcli can run one or more etcd members
as a **standalone, top-level addon** — shared infrastructure rather than a
per-instance sidecar. This is useful as the DCS (Distributed Concurrent Store)
backing a PostgreSQL HA stack, or as a general config/lock service.

> **Security caveat:** pgcli-managed etcd members currently run **without**
> TLS/CA certificates and **without** authentication/RBAC — any client that can
> reach a client port has full read/write access. Plan a deployment around
> network isolation (loopback binds by default; keep advertised ports inside a
> trusted network). CA/TLS and auth/RBAC support is on the roadmap for a later
> release.

Members are managed one `pg addon install etcd` at a time: the first member
bootstraps a cluster, and later members join the same named cluster on the fly
— from the same host or from another machine.

## How It Works

- **Shared infrastructure:** etcd lives in the top-level `addons.etcd` map in
  `pg.yaml`, keyed by member name — not under any single instance.
- **Host network, configurable bind:** each member runs with `--network host`.
  By default its client/peer URLs bind to `127.0.0.1` — etcd ships without
  authentication, so loopback-only exposure is the intended posture for a
  single-host cluster. For cross-host clusters each member advertises a
  reachable address (`--advertise-host`) while **also** listening on loopback,
  so local `etcdctl` and remote peers both work.
- **Dynamic membership:** the first member starts with
  `--initial-cluster-state new`; each subsequent member is registered with
  `etcdctl member add` against a running peer, then started with
  `--initial-cluster-state existing`. pgcli does this automatically — same-host
  via a member container, cross-host via a temporary etcdctl container.
- **Cluster identity:** members sharing the same `--cluster` value join the
  same etcd cluster (it maps to etcd's `--initial-cluster-token`). The default
  is `pgcli-etcd`.
- **Baked-in tuning:** every member launches with periodic compaction
  (`--auto-compaction-mode periodic`, `--auto-compaction-retention 24h`) and an
  8 GiB backend quota (`--quota-backend-bytes 8589934592`) — sane defaults for
  a small HA metadata store.

## Deployment Topologies

pgcli supports two cluster shapes. Both use the same install commands — the
difference is whether members share a host.

### Single host — all members on one machine (testing / dev)

The classic layout for a dev box or CI: every member listens on loopback with a
different auto-assigned port. No `--advertise-host` needed (default
`127.0.0.1`), no firewall changes, nothing reachable from outside the host.

```bash
pg addon install etcd --name m1              # 127.0.0.1:2379/2380
pg addon install etcd --name m2              # 127.0.0.1:2381/2382
pg addon install etcd --name m3              # 127.0.0.1:2383/2384
```

All three belong to cluster `pgcli-etcd`; m1 bootstraps, m2/m3 join on the fly.
This gives you real 3-node raft semantics but zero host isolation — the whole
cluster dies with one machine, which is exactly why it is a *testing* topology.

### Cross host — one member per machine (production HA)

The production shape: spread members across machines (ideally 3 or 5, an odd
count — see Topology & Quorum). Each member advertises its host's LAN address;
the first member **must** bootstrap with `--advertise-host` so remote peers can
dial it. Ports may repeat on every host since each binds its own interface.

```bash
# host A (10.0.0.1) — bootstrap
pg addon install etcd --name m1 --cluster prod \
  --advertise-host 10.0.0.1 --client-port 2379 --peer-port 2380

# host B (10.0.0.2) — join
pg addon install etcd --name m2 --cluster prod \
  --advertise-host 10.0.0.2 --client-port 2379 --peer-port 2380 \
  --join http://10.0.0.1:2379

# host C (10.0.0.3) — join
pg addon install etcd --name m3 --cluster prod \
  --advertise-host 10.0.0.3 --client-port 2379 --peer-port 2380 \
  --join http://10.0.0.1:2379
```

Requirements: every member's `--advertise-host` is a mutual-LAN-reachable
address, the firewall on each host lets the other members through on **every
member's client and peer ports** (bidirectional peer traffic — e.g. open
2379-2386/tcp for a 4-member cluster on default ports), and all members share
the same `--cluster` token. Each host survives losing any one member; the
cluster only needs a majority of *machines*.

| | Single host | Cross host |
|---|---|---|
| Use for | dev, CI, functional testing | production HA |
| `--advertise-host` | omit (loopback default) | required, every member |
| `--join` | not used | every member after the first |
| Ports | unique per member on one host | may repeat, one host each |
| Firewall | none (loopback only) | open every member's client+peer ports, both ways |
| Survives machine loss | no | yes (with quorum) |

## Commands

### Create the first member

```bash
pg addon install etcd --name m1
```

```
-> Bootstrapping etcd cluster...
  [OK] etcd container started

✓ etcd installed: "m1"
  Container:    pgcli-etcd-m1
  Cluster:      pgcli-etcd
  Data dir:     ~/.pgcli/addon/etcd/m1/data
  Client port:  2379
  Peer port:    2380
  Advertise:    127.0.0.1

  Client URL: http://127.0.0.1:2379
  Connect (etcdctl): ETCDCTL_ENDPOINTS=http://127.0.0.1:2379
```

The client port starts at **2379** and the peer port takes the next free port
(**2380**), auto-allocated from `etcd_start_port`.

### Add members to the same cluster

With `m1` running, installing another member of the same `--cluster` joins it:

```bash
pg addon install etcd --name m2
pg addon install etcd --name m3
```

pgcli finds a running peer to act as coordinator, registers `m2`/`m3` via
`etcdctl member add`, and starts them with `--initial-cluster-state existing`.
Ports continue to auto-assign without collisions (`m2` → 2381/2382, `m3` →
2383/2384). A freshly-grown cluster briefly lacks quorum during leader
re-election; pgcli retries the membership operations automatically, so you do
not need to wait between installs.

Use a different `--cluster` to keep members in a separate etcd cluster.

### Join a member on another machine

Members on different hosts form one cluster too. Each cross-host member must
advertise an address the *other* members can reach, set with
`--advertise-host`. On the machine that already hosts a member, bootstrap as
usual, passing its LAN address:

```bash
# host A (10.0.0.1)
pg addon install etcd --name m1 --cluster prod \
  --advertise-host 10.0.0.1 --client-port 2379 --peer-port 2380
```

Then on the new host, point `--join` at any existing member's client endpoint.
pgcli registers the member through that endpoint (from a temporary etcdctl
container — no local member container or image needed; the image is pulled on
demand), takes the authoritative `ETCD_INITIAL_CLUSTER` from the response, and
starts the member:

```bash
# host B (10.0.0.2)
pg addon install etcd --name m2 --cluster prod \
  --advertise-host 10.0.0.2 --client-port 2379 --peer-port 2380 \
  --join http://10.0.0.1:2379
```

```
-> Registering member with the cluster at http://10.0.0.1:2379...
-> Starting etcd container (joining cluster)...
  [OK] etcd container started
```

Notes for cross-host clusters:

- **Every** member needs a reachable `--advertise-host` — including the first.
  Its advertised peer URL propagates into the cluster's membership list, so a
  first member started on the default `127.0.0.1` can never be joined from
  another host. If you plan a cross-host cluster, pass the LAN address at
  bootstrap time.
- `--cluster` is required with `--join` and must match the remote cluster's
  token — membership is checked by that cluster, not by the local config.
- `--advertise-host` is required with `--join`; without it the other members
  could register a peer URL they can't dial.
- Ports may repeat across hosts (`2379/2380` on both) since each host binds
  its own interfaces.
- A member's client/peer ports must be reachable between hosts — **open your
  firewall for every member's ports, in both directions.** Peers dial each
  other's *peer* ports bidirectionally, and clients (and `--join`/`pg etcdctl`)
  reach the *client* ports, so allow both. Auto-assignment means the exact
  numbers vary per member — read them from the install summary or
  `pg addon list` (or pin them with `--client-port`/`--peer-port`) and open
  that range, e.g. `2379-2386/tcp` for a 4-member cluster on default ports.
  Skipping this is the usual cause of a member hanging with `etcdserver: no
  leader` or a `--join` that times out.
- Re-running the same `--join` install for an already-registered name fails
  with a clear error; deregister first via
  `pg etcdctl member remove <hex-id>` against the cluster.

### Inspect with `pg etcdctl`

`pg etcdctl` runs etcd's client from a short-lived container — no need to
exec into a member (and it works even on a host with no member
installed, pulling the image on demand). The target comes from
`ETCDCTL_ENDPOINTS`, falling back to the first configured member:

```bash
export ETCDCTL_ENDPOINTS=http://10.0.0.2:2379
pg etcdctl member list
pg etcdctl endpoint health
pg etcdctl endpoint status -- -w table
```

etcdctl flags that pg's own parser would reject (`-w table`, `--hex`, …) go
after `--`.

### List

```bash
pg addon list
```

etcd members appear under the **Infra add-ons (etcd)** section:

```
Infra add-ons (etcd):
  etcd (name: m1)
    Status:      running
    Cluster:     pgcli-etcd
    Client URL:  http://127.0.0.1:2379
    Client port: 2379
    Peer port:   2380
    Image:       quay.io/coreos/etcd:v3.5.30
    Container:   pgcli-etcd-m1
```

### Remove a member

```bash
pg addon remove etcd --name m2
```

Removal first **deregisters** the member from the cluster (resolving its hex
member ID — etcd v3.5 `member remove` takes an ID, not a name), then deletes
the container and the member's data directory. Remaining members stay healthy
as long as quorum holds.

**Cross-host members must be deregistered separately.** The automatic
deregistration above only works when another running member of the *same
cluster* is present in the local `pg.yaml` — pgcli has no view of members that
live on other hosts. So on a cross-host cluster, removing a member from the
host that runs it deletes the container and data but **leaves its entry in the
cluster's membership list** (a "stale" member) — the surviving members keep
trying to peer with the now-deleted host.

Remove it in two steps instead:

```bash
# 1. on the host that runs the member — stop and clean up local state
pg addon remove etcd --name m4

# 2. from any surviving host's member, deregister it from the cluster
export ETCDCTL_ENDPOINTS=http://10.0.0.1:2379   # a surviving member's client URL
pg etcdctl member list                            # find m4's hex ID
pg etcdctl member remove <hex-id>                 # e.g. 5c7048c8f7521ec7
```

`pg etcdctl` needs a reachable peer to talk to — point `ETCDCTL_ENDPOINTS` at
a member of that cluster that is **still running** (on a host that still has a
running member, this falls back automatically; otherwise set it explicitly),
then remove by ID (see [Inspect with `pg etcdctl`](#inspect-with-pg-etcdctl)).
Verify with `pg etcdctl member list` afterward: the removed name should be gone.

## Parameters

| Flag | Description | Default |
|------|-------------|---------|
| `--name` | Member name, and the `pg.yaml` config key | `etcd` |
| `--cluster` | Cluster identity (`--initial-cluster-token`); same value = same cluster | `pgcli-etcd` |
| `--client-port` | Client host port (0 = auto-assign from `etcd_start_port`) | auto |
| `--peer-port` | Peer host port (0 = auto-assign, next free port) | auto |
| `--image` | etcd image tag | `quay.io/coreos/etcd:v3.5.30` |
| `--data-dir` | Data dir **root** — absolute, or relative to `base_dir`; each member uses `<root>/<name>/data` | `<base_dir>/addon/etcd` |
| `--advertise-host` | Host advertised in this member's peer/client URLs (empty = `127.0.0.1` for single-host; set a LAN IP or FQDN for cross-host) | `127.0.0.1` |
| `--join` | Client endpoint of an existing member to join cross-host, e.g. `http://10.0.0.1:2379` (implies `--initial-cluster-state existing`; requires `--advertise-host` and `--cluster`) | — |

Container name follows the namespace convention:
`pgcli-etcd-<namespace>-<name>` (namespace omitted when unset).

## Data Directory Layout

Data is always laid out as `<root>/<name>/data`, so multiple members on one
host never share a directory:

```
<base_dir>/addon/etcd/
├── m1/data/     # member m1
├── m2/data/     # member m2
└── m3/data/     # member m3
```

`--data-dir` sets the root. A relative value resolves against the config's
`base_dir`; the resolved root is persisted, so the config stays portable:

```bash
# base_dir: /data/pgcli  →  root /data/pgcli/etcd, member d1 → /data/pgcli/etcd/d1/data
pg addon install etcd --name d1 --data-dir ./etcd

# absolute root
pg addon install etcd --name d2 --data-dir /mnt/ssd/etcd
```

`pg addon remove` deletes only that member's `<name>/data` dir and leaves the
shared root in place.

## Connecting

Use `pg etcdctl`, which runs etcd's **v3** client from a short-lived container
against a member's client URL — the target comes from `ETCDCTL_ENDPOINTS`,
falling back to the first configured member. etcdctl's native flags go after
`--`:

```bash
pg etcdctl member list
pg etcdctl endpoint status -- -w table
pg etcdctl endpoint health
pg etcdctl put foo bar
pg etcdctl get foo -- --hex
```

`pg etcdctl` always speaks the v3 API (`ETCDCTL_API=3`, etcdctl's default); v2
is not supported.

## Topology & Quorum

etcd requires a quorum (majority) of members to accept writes:

| Members | Quorum | Tolerates failures |
|---------|--------|--------------------|
| 1 | 1 | 0 |
| 3 | 2 | 1 |
| 5 | 3 | 2 |

- Use an **odd** number of members — 3 or 5 for production HA.
- Removing members can drop the cluster below quorum (e.g. 3 → 1 surviving);
  it will then be unable to commit writes. Keep a majority running.

## Configuration

After install, `pg.yaml` records each member under the top-level `addons.etcd`:

```yaml
addons:
  etcd:
    m1:
      container_name: pgcli-etcd-m1
      name: m1
      cluster_name: pgcli-etcd
      image_tag: quay.io/coreos/etcd:v3.5.30
      data_dir: /home/user/.pgcli/addon/etcd
      client_port: 2379
      peer_port: 2380
      autostart: true
    m2:
      container_name: pgcli-etcd-m2
      name: m2
      cluster_name: pgcli-etcd
      client_port: 2381
      peer_port: 2382
```

Port base is configurable via top-level `etcd_start_port` (default 2379). A
cross-host member records the address it advertises:

```yaml
addons:
  etcd:
    m1:
      name: m1
      cluster_name: prod
      advertise_host: 10.0.0.1
      client_port: 2379
      peer_port: 2380
```

## Auto-start on Boot

Containers also carry a `--restart unless-stopped` policy, which a per-container
conmon monitor enforces even without a podman daemon — it covers crashes but
**not** host reboots. To bring members up after a reboot, enable autostart
per member:

```bash
pg autostart enable --etcd --name m1
pg autostart enable --etcd --name m2
pg autostart enable --etcd --name m3
```

This sets `autostart: true` on the member in `pg.yaml` and installs/refreshes
the boot service (see [Auto-start on Boot](/docs/autostart/)). Boot is
**start-only**: it starts the member's existing container and never re-runs
`member add` — an initialized member reloads its cluster from disk and rejoins.
On a cross-host cluster, run the command on each host for that host's
member(s). `pg autostart status` lists every member's state.

## Notes

- **Single-node vs cluster:** `--name m1` alone gives a one-member cluster;
  install more members with the same `--cluster` to grow it.
- **Production-ready defaults:** every member already launches with periodic
  compaction and an 8 GiB backend quota (see How It Works), suitable for a
  production DCS without further tuning.
- **Reinstall is idempotent:** re-running `pg addon install etcd --name <m>`
  on a running member recreates its container with updated flags; a member
  already registered in the cluster is not re-added.
