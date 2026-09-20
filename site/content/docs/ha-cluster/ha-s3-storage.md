---
title: "S3 Storage High Availability"
description: "Making the MinIO/silo object store behind pgBackRest highly available: the two deployment modes pgcli exposes and why only those two, plus the ZFS layer that adds disk-level redundancy — a single-host raidz pool, per-node pools in a distributed cluster, and heterogeneous nodes"
weight: 55
---

A Patroni cluster survives a node loss; the S3 repository its WAL and backups
stream into must survive one too, or "high availability" quietly ends at the
backup path. The store is a [MinIO](../addon/minio/) or
[silo](../addon/silo/) addon (the two are interchangeable — silo is Pigsty's
MinIO fork with the same feature surface). Two fault domains matter, and they
are **orthogonal**:

| Fault | Who absorbs it |
|-------|----------------|
| A **disk** dies inside a node | the storage layer under MinIO's data directory (ZFS) |
| A **node** dies entirely | MinIO's own erasure coding (EC) across hosts |

This page records the shapes pgcli supports for combining the two, and why the
exposed mode list stops at two.

## The two deployment modes

MinIO classifies its layouts by node count and drive count per node
([SNSD / SNMD / MNSD](../addon/minio/#deployment-modes)). pgcli exposes two:

| Mode | Shape | Use it for |
|------|-------|------------|
| **SNSD** (single-node, single-drive) | one node, one data directory — the default | dev, test, demos — and, paired with ZFS below, any single-host deployment that needs disk redundancy |
| **MNSD** (multi-node, single-drive) | ≥ 4 nodes, one data directory per node | compact high-availability deployments |

### Why only two

The missing mode is **SNMD** (single-node, *multi*-drive) — and it is not a
policy choice. MinIO itself refuses a distributed endpoint list whose members
all resolve to the same host:

- a same-host list is rejected with `use path style endpoint for single node
  setup`;
- the entire `127.0.0.0/8` range is rejected with `resolves to localhost`.

So the SNMD container simply never comes up — pgcli cannot ship a deployment
shape upstream forbids. The capability SNMD would have provided (several
disks acting as one redundant volume on one host) is instead delegated one
layer down, to the filesystem: **ZFS gives MinIO a single drive path backed by
as many disks as you like, with real redundancy.** The division of labor is
clean — MinIO does node-level EC, ZFS does disk-level redundancy — and each
layer stays simple.

## ZFS: the flexible disk layer

The recipe is identical for both modes: build a zpool from the host's data
disks, create one dataset for the store, and point `--data-dir` at its mount
point. MinIO sees "one big reliable drive"; it never learns how many physical
disks are under it.

```bash
# one pool from the host's spare disks (mount point defaults to /minio-pool)
sudo zpool create minio-pool raidz1 /dev/sdb /dev/sdc /dev/sdd

# one dataset for the store: 1M recordsize suits object blobs, no atime churn
sudo zfs create -o recordsize=1M -o atime=off minio-pool/store

pg addon install minio --name store --data-dir /minio-pool/store   # or: silo
```

The pool must sit on separate devices from the root filesystem — which is also
what [MinIO's drive check](../addon/minio/#distributed--cluster-mode) and
pgcli's install-time advisory want (`drive is part of root drive, will not be
used`). A ZFS mount on its own disks satisfies that trivially.

### Layouts for a 4-disk host

With four disks under SNSD, the usual choice is:

| Layout | Usable | Survives | Character |
|--------|--------|----------|-----------|
| `raidz1` (like RAID5) | 3 × disk | 1 disk | the default: best capacity, single-parity |
| `raidz2` (like RAID6) | 2 × disk | 2 disks | safer on large disks (long resilvers), halves capacity |
| 2 × `mirror` (striped) | 2 × disk | 1 disk per mirror (2 if in different mirrors) | best small-write performance |

MinIO's own guidance to avoid RAID *underneath its EC mode* targets the
double-redundancy of RAID + cross-node EC. Under **SNSD** there is no EC —
ZFS is the only protection the data has — so the guidance does not apply, and
following it literally ("single drive, no ZFS") on a 4-disk host would mean
losing the whole store to one dead disk.

> **Co-locating with PostgreSQL:** ZFS caches aggressively (ARC, by default up
> to half of RAM). On a host that also runs the database, cap it — e.g.
> `options zfs:zfs_arc_max=8589934592` in `/etc/modprobe.d/zfs.conf` — so the
> two workloads do not fight over memory.

## HA with 4 hosts, each with several disks

This is the hybrid the layout table above points at: **MNSD across the hosts,
ZFS under each node's data directory**. Each node exposes exactly *one*
endpoint (its ZFS-backed `/data`); disk failures are healed by the local pool
and never reach MinIO; node failures are absorbed by EC quorum.

```bash
# on EACH of the 4 nodes: build the local pool (layout per node — see below)
sudo zpool create minio-pool raidz1 /dev/sdb /dev/sdc /dev/sdd
sudo zfs create -o recordsize=1M -o atime=off minio-pool/store

# node 1 (10.0.0.11):
pg addon install minio --name store \
  --listen 10.0.0.11 \
  --data-dir /minio-pool/store \
  --root-password '<shared-secret>' --tls \
  --endpoint http://10.0.0.11:9000/data \
  --endpoint http://10.0.0.12:9000/data \
  --endpoint http://10.0.0.20:9000/data \
  --endpoint http://10.0.0.21:9000/data

# nodes 2–4: same command, own --listen, identical endpoint list and password
```

What each layer then tolerates on a 4-node cluster:

| Event | Handled by | Effect |
|-------|-----------|--------|
| 1 disk in a node's raidz pool | ZFS (resilver) | invisible to MinIO, no quorum math |
| 1 node down | EC (writes need ⌈4/2⌉+1 = 3 of 4 online) | reads and writes continue |
| 2 nodes down | EC reads only (⌈4/2⌉ = 2 of 4) | readable, writes refused until a node returns |
| a disk *and* its node failing together | EC (3 of 4) | still safe — the surviving pools heal after the node returns |

Two properties make the scheme flexible:

- **Endpoint count = node count, not disk count.** A node with 2 disks and a
  node with 6 disks look identical to MinIO. The four hosts may run
  different layouts — `raidz1` ×4 disks here, 2-way `mirror` there, `raidz2`
  ×6 over there — heterogeneity costs nothing.
- **Pool capacities should be roughly aligned.** EC sets the usable capacity
  of the whole cluster to the *weakest member's* free space (3 × 12T pools +
  1 × 4T pool → you get 4T × striping, not 40T). If the disks genuinely
  differ, thin provisioning (`zvol`-based sparse datasets, or simply
  `zfs set refquota`) hides the mismatch from MinIO — the quota caps the big
  pools at the small one's size, and nothing wastes a rebuild.

## Choosing a shape

| Situation | Shape |
|-----------|-------|
| laptop / demo / CI | SNSD, default data dir — nothing to decide |
| one host with N ≥ 4 data disks, backups must survive a disk | SNSD + `raidz1` (or `raidz2` on big disks) |
| a few hosts, one data disk each, must survive a host | MNSD plain |
| hosts with several data disks each, must survive a disk *and* a host | MNSD + per-node ZFS (layouts may differ per node; keep capacities aligned) |

The store's TLS story is independent of the topology: whichever shape you
pick, `--tls` (or `pg cert`-minted BYO certificates) works the same — see
[Generating a Certificate](../ha-cert/).

## Related

- [Addons → MinIO](../addon/minio/) — install, `--tls`, distributed mode
  constraints (the three hard rules this page builds on)
- [Addons → Silo](../addon/silo/) — the drop-in fork; identical modes
- [Cluster Backup](../ha-backup/) — how the repo is wired into Patroni
  (`pg backup setup`)
- [Example: HA Cluster with a Self-CA MinIO](../ha-example-minio/) — an
  end-to-end verified walkthrough of the single-node variant
