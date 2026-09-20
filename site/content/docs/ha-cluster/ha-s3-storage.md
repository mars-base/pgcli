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

MinIO and silo support more shapes than these — **SNMD** (single-node,
*multi*-drive) and multi-node with several drives per node are both real,
using MinIO's path-style endpoint syntax (a single node with
`/data{1...4}`, or a host×drive matrix). pgcli simply does not wire those
into the addon: `--endpoint` takes one routable host per node and `--data-dir`
is one directory, so the two modes above are what the CLI can express today.

That is a deliberate scope call, not a gap we consider worth closing. Disk
redundancy is a *filesystem* job, and ZFS does it better and more flexibly
than MinIO's own multi-drive mode could:

- **One mechanism serves both modes.** `raidz1` under SNSD protects a single
  host's store; the same recipe under each node of an MNSD cluster protects a
  distributed one. A MinIO SNMD/MNMD layout, by contrast, only ever applies to
  whichever node it was declared on.
- **The layout stays changeable underneath.** Swap two disks for a mirror,
  grow the pool, migrate to `raidz2` — `--data-dir` never changes and MinIO
  notices nothing. MinIO's drive set is fixed at install time.
- **Nodes may be heterogeneous.** A 2-disk node and a 6-disk node look
  identical to MinIO (one endpoint each). With MinIO-level multi-drive, every
  member has to describe its drives to the cluster.
- **Quorum math stays simple.** Writes need ⌈N/2⌉+1 *nodes*. When drives
  inside a node also count as failure members, the arithmetic of "what can
  this cluster survive" stops being legible.

So the division of labor is: **MinIO does node-level EC, ZFS does disk-level
redundancy** — and each layer stays simple.

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

### Layout by disk count

| Data disks | SNSD (ZFS is the only defense) | MNSD node (EC above absorbs node loss) |
|-----------|--------------------------------|------------------------------------------|
| 1 | no redundancy — fine for dev/test, a dead disk means re-seeding the repo | one big disk per node is the plain MNSD shape; no ZFS needed |
| 2 | `mirror` | `mirror` |
| 3 | `raidz1` (2D usable) | `raidz1` |
| 4 | `raidz2` on spinning disks; `raidz1` when SSD rebuilds are quick and the capacity matters; 2 × `mirror` for write-heavy stores | `raidz1` — one parity is enough to keep disk loss invisible to MinIO |
| 5–8 | `raidz2` ((N−2)D usable) | `raidz1`, or `raidz2` with large HDDs |
| > 8 | prefer two smaller `raidz2`/`raidz1` vdevs striped over the pool — a resilver across 12+ disks is a long exposure window | same: keep any single vdev ≤ ~8 disks |

The two columns differ in one line of reasoning: under SNSD ZFS must survive
the disk *and* whatever happens during its rebuild, so parity depth buys
safety outright; under MNSD, ZFS only has to keep a disk failure from
escalating into a node loss, so one parity plus quick local resilver is the
job — and beyond that, prefer more nodes over deeper local RAID.

For a 4-disk host under SNSD — the most common single-box question — the
usual choices are:

| Layout | Usable | Survives | Character |
|--------|--------|----------|-----------|
| `raidz2` (like RAID6) | 2 × disk | 2 disks | the safe default on spinning disks: resilvers on large drives are long, and raidz1's single parity does not survive one more loss during a rebuild |
| `raidz1` (like RAID5) | 3 × disk | 1 disk | the capacity pick for SSDs / small disks, where a resilver takes minutes, not hours |
| 2 × `mirror` (striped) | 2 × disk | 1 disk per mirror (2 if in different mirrors) | best small-write performance — wide raidz is the worst shape for it |

Under **SNSD** ZFS is the store's only line of defense, so the default leans
conservative: `raidz2` unless the disks are fast enough to rebuild quickly and
the capacity is worth the thinner margin.

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

**Why not stripe the per-node pool to reclaim capacity.** MinIO's "no RAID
under EC" advice is about capacity, and the arithmetic is real: a plain
4-node MNSD EC set already halves the raw total, so an unmirrored `raidz_none`
pool on each node (all disks striped, zero local redundancy) does squeeze out
roughly a third more cluster capacity than `raidz1` would. But EC counts a
failure in *nodes*, and a striped pool turns a single dead disk into a whole
offline node — one ordinary disk failure burns a slot of the budget EC set
aside for losing an entire machine, forcing a network-wide rebuild of that
node's whole pool and leaving zero margin until it finishes. The capacity is
only "free" because you quietly downgraded disk fault-tolerance to node
fault-tolerance. `raidz1` per node is the point where the two layers stop
stealing from each other: the pool absorbs disk loss invisibly, MinIO's EC
budget stays reserved for node loss. (If the reclaim-everything answer truly
fits, the shape that maximizes it is plain MNSD on one big disk per node —
no ZFS at all — not a striped pool that hides the same single-point risk one
layer down.)

## Choosing a shape

| Situation | Shape |
|-----------|-------|
| laptop / demo / CI | SNSD, default data dir — nothing to decide |
| one host with N ≥ 4 data disks, backups must survive a disk | SNSD + `raidz2` on spinning disks, `raidz1` on fast SSDs |
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
