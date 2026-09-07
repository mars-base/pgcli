---
title: "etcd"
description: "Run a standalone etcd cluster as a pgcli addon for HA / DCS use"
weight: 30
---

etcd is a distributed key-value store. pgcli can run one or more etcd members
as a **standalone, top-level addon** — shared infrastructure rather than a
per-instance sidecar. This is useful as the DCS (Distributed Concurrent Store)
backing a PostgreSQL HA stack, or as a general config/lock service.

Members are managed one `pg addon install etcd` at a time: the first member
bootstraps a cluster, and later members join the same named cluster on the fly.

## How It Works

- **Shared infrastructure:** etcd lives in the top-level `addons.etcd` map in
  `pg.yaml`, keyed by member name — not under any single instance.
- **Host network, loopback binds:** each member runs with `--network host` and
  binds its client/peer URLs to `127.0.0.1`. etcd ships without authentication,
  so loopback-only exposure is the intended posture.
- **Dynamic membership:** the first member starts with
  `--initial-cluster-state new`; each subsequent member is registered with
  `etcdctl member add` against a running peer, then started with
  `--initial-cluster-state existing`. pgcli does this automatically.
- **Cluster identity:** members sharing the same `--cluster` value join the
  same etcd cluster (it maps to etcd's `--initial-cluster-token`). The default
  is `pgcli-etcd`.
- **Baked-in tuning:** every member launches with periodic compaction
  (`--auto-compaction-mode periodic`, `--auto-compaction-retention 24h`) and an
  8 GiB backend quota (`--quota-backend-bytes 8589934592`) — sane defaults for
  a small HA metadata store.

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

## Parameters

| Flag | Description | Default |
|------|-------------|---------|
| `--name` | Member name, and the `pg.yaml` config key | `etcd` |
| `--cluster` | Cluster identity (`--initial-cluster-token`); same value = same cluster | `pgcli-etcd` |
| `--client-port` | Client host port (0 = auto-assign from `etcd_start_port`) | auto |
| `--peer-port` | Peer host port (0 = auto-assign, next free port) | auto |
| `--image` | etcd image tag | `quay.io/coreos/etcd:v3.5.30` |
| `--data-dir` | Data dir **root** — absolute, or relative to `base_dir`; each member uses `<root>/<name>/data` | `<base_dir>/addon/etcd` |

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

Point `etcdctl` (or any v3 client) at a member's client URL:

```bash
export ETCDCTL_ENDPOINTS=http://127.0.0.1:2379
etcdctl member list
etcdctl endpoint status -w table
etcdctl endpoint health
etcdctl put foo bar
etcdctl get foo
```

Inside a member container the tools are already present:

```bash
podman exec pgcli-etcd-m1 etcdctl member list
```

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
    m2:
      container_name: pgcli-etcd-m2
      name: m2
      cluster_name: pgcli-etcd
      client_port: 2381
      peer_port: 2382
```

Port base is configurable via top-level `etcd_start_port` (default 2379).

## Notes

- **Single-node vs cluster:** `--name m1` alone gives a one-member cluster;
  install more members with the same `--cluster` to grow it.
- **No auth:** these members run unauthenticated and bound to loopback. Do not
  expose the client port beyond the host without adding authentication.
- **Reinstall is idempotent:** re-running `pg addon install etcd --name <m>`
  on a running member recreates its container with updated flags; a member
  already registered in the cluster is not re-added.
