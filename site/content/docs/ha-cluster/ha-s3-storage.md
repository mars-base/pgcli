---
title: "S3 Storage High Availability"
description: "Making the MinIO/silo object store behind pgBackRest highly available: the four deployment modes pgcli exposes (SNSD/SNMD/MNSD/MNMD), how native multi-drive compares to a ZFS layer for disk redundancy, and the two paths for a distributed cluster whose nodes each hold several disks"
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
| A **disk** dies inside a node | the drive-level EC under SNMD/MNMD, or the storage layer under MinIO's data directory (ZFS) |
| A **node** dies entirely | MinIO's own erasure coding (EC) across hosts |

This page records the shapes pgcli supports for combining the two, and how to
pick among them.

## The deployment modes

MinIO classifies its layouts by node count and drive count per node
([SNSD / SNMD / MNSD / MNMD](../addon/minio/#deployment-modes)). pgcli exposes
all four:

| Mode | Shape | Use it for |
|------|-------|------------|
| **SNSD** (single-node, single-drive) | one node, one data directory — the default | dev, test, demos — and, paired with ZFS below, any single-host deployment that needs disk redundancy |
| **SNMD** (single-node, *multi*-drive) | one node, several drives — one `--drive` per drive | a single host with N ≥ 4 data disks that must survive a disk loss without a filesystem layer |
| **MNSD** (multi-node, single-drive) | ≥ 4 nodes, one data directory per node | compact high-availability deployments |
| **MNMD** (multi-node, *multi*-drive) | ≥ 2 nodes, several drives each — `--drive` plus the full `--endpoint` matrix | surviving a disk loss *and* a node loss without a filesystem layer |

SNMD is native: `pg addon install minio --drive /mnt/disk1 --drive ...`
(one flag per drive) starts one MinIO process that erasure-codes across the
drives. What it buys, measured on a live 4-drive set: 2 parity shards by
default, so it tolerates 2 drive failures; with 1 drive down reads *and*
writes continue, with 2 down reads still succeed but writes are refused (the
quorum boundary); usable capacity is about half the raw total; a drive that
returns is healed by MinIO itself. The same `--drive` works on
[silo](../addon/silo/).

MNMD is native too: keep `--drive` for this node's drives and add the whole
cluster's host×drive endpoint matrix with `--endpoint` — one URL per drive on
every node, each naming that node's `/data1../dataN` slot. Measured on a live
4-node × 4-drive set (both minio and silo): 16 drives online report **EC:4**
in a single erasure set of stripe size 16; losing one whole node (12/16)
keeps reads *and* writes working; losing a second node (8/16) refuses writes
and fails reads, and the set self-heals to 16/16 on restart. The full
walkthrough is [Addons → MinIO → Multi-Node Multi-Drive
(MNMD)](../addon/minio/#multi-node-multi-drive-mnmd).

### SNMD vs ZFS: two ways to survive a disk

Both protect a single host's data against disk loss; they differ in where the
redundancy lives and what that costs.

| | native **SNMD/MNMD** (`--drive`) | **ZFS** pool under `--data-dir` |
|---|---|---|
| quorum unit | the *drive* — MinIO counts drives as failure members | invisible to MinIO — one big drive, the pool absorbs disk loss |
| layout changeable later | fixed at install (the drive set is the EC set) | freely — swap disks, grow, migrate `raidz1`→`raidz2`; `--data-dir` never changes |
| usable capacity, 4 disks | ~half (2 data + 2 parity) | depends on vdev: `raidz2` = 2×disk, `raidz1` = 3×disk (more usable) |
| rebuild | MinIO heals a returned drive | ZFS resilvers locally |
| serves which modes | SNMD (single host) and MNMD (across hosts) | SNSD *and* MNSD (same recipe under each node) |
| heterogeneous nodes | every node must contribute the same drive count | a 2-disk node and a 6-disk node look identical (one endpoint each) |

The honest tradeoff: **native multi-drive is simpler** — one command, no
filesystem to provision, and it gets you disk redundancy with zero ZFS setup.
**ZFS is more flexible** — the layout stays changeable underneath, it saves
more capacity on the same disks (`raidz1` keeps 3 of 4 usable where SNMD's
EC:2 keeps 2 of 4), and it tolerates nodes that are not identical. So:

- single host, want disk redundancy without touching ZFS → **SNMD**
- single host, want the most usable capacity / a layout you can change later →
  **SNSD + ZFS**
- distributed cluster, disk *and* node failure → both paths are first-class
  now: **native MNMD**, or **MNSD + per-node ZFS** — see "HA with 4 hosts,
  each with several disks" below

Neither is wrong on a single box — SNMD's 50% capacity is the price of not
managing a pool, and ZFS's flexibility is the price of provisioning one. The
rest of this page documents the ZFS path and the MNMD alternative for
multi-host sets.

## ZFS: the flexible disk layer

The recipe is identical under SNSD and MNSD: build a zpool from the host's
data disks, create one dataset for the store, and point `--data-dir` at its
mount point. MinIO sees "one big reliable drive"; it never learns how many
physical disks are under it.

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

This is the hybrid the layout table above points at, and there are two
first-class ways to build it: **native MNMD** — MinIO erasure-codes across
every disk of every node — or **MNSD + a ZFS pool under each node's data
directory** — MinIO sees one drive per node and the pools absorb disk loss.
They fail differently, so the choice is real either way.

### Native MNMD

One matrix, no filesystem layer: each node passes its own drives with
`--drive` and every node carries the identical host×drive endpoint list.

```bash
# node 1 (10.0.0.11) — four data disks mounted; nodes 2–4: same command,
# own --drive paths, SAME 16-endpoint matrix, SAME --root-password:
pg addon install minio --name store --tls \
  --listen 0.0.0.0 \
  --drive /mnt/minio/disk1 --drive /mnt/minio/disk2 \
  --drive /mnt/minio/disk3 --drive /mnt/minio/disk4 \
  --root-password '<shared-secret>' \
  --endpoint https://10.0.0.11:9000/data1 --endpoint https://10.0.0.11:9000/data2 \
  --endpoint https://10.0.0.11:9000/data3 --endpoint https://10.0.0.11:9000/data4 \
  --endpoint https://10.0.0.12:9000/data1 --endpoint https://10.0.0.12:9000/data2 \
  --endpoint https://10.0.0.12:9000/data3 --endpoint https://10.0.0.12:9000/data4 \
  --endpoint https://10.0.0.20:9000/data1 --endpoint https://10.0.0.20:9000/data2 \
  --endpoint https://10.0.0.20:9000/data3 --endpoint https://10.0.0.20:9000/data4 \
  --endpoint https://10.0.0.21:9000/data1 --endpoint https://10.0.0.21:9000/data2 \
  --endpoint https://10.0.0.21:9000/data3 --endpoint https://10.0.0.21:9000/data4
```

Measured on a live 4-node × 4-drive set (the same on minio and silo): the
16-drive set reports **EC:4** in one erasure set of stripe size 16 — losing
4 drives of 16 stays healthy.

| Event | Online | Effect (measured) |
|-------|--------|-------------------|
| 1–4 drives lost | ≥ 12/16 | reads and writes continue; returned drives are healed by MinIO |
| 1 node down (its 4 drives) | 12/16 | reads *and* writes continue — a 64 MiB round-trip stayed byte-identical |
| 2 nodes down | 8/16 | writes refused (`Resource requested is unwritable`), reads fail too |
| nodes restarted | 16/16 | self-heals |

Usable capacity follows the parity ratio: EC:4 over 16 drives keeps **12/16**
of raw bytes. The costs: the layout is fixed at install (the matrix is the EC
set), every node must contribute the same number of drives, and pgcli cannot
detect a matrix typo on *another* node — keeping the N `pg.yaml` files
identical is the operator's job. With `--tls`, the grid needs one shared
certificate authority: seed the same CA material (or BYO `cert_file`/
`key_file`) into every node's certs dir before starting the rest — per-node
self-signed CAs make cross-node handshakes fail with `x509: certificate
signed by unknown authority`.

### MNSD + per-node ZFS

**MNSD across the hosts, ZFS under each node's data directory.** Each node
exposes exactly *one* endpoint (its ZFS-backed `/data`); disk failures are
healed by the local pool and never reach MinIO; node failures are absorbed by
EC quorum.

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

### Which of the two

| | native **MNMD** | **MNSD + per-node ZFS** |
|---|---|---|
| setup | one matrix command per node, no filesystem to provision | zpool + dataset on every node first, then one endpoint per node |
| what EC counts | drives — one node down is just 4 of 16 members lost | nodes — a disk failure never reaches MinIO at all |
| measured fault margin | 4-node × 4-drive set: OK to 12/16 drives (one full node), fails at 8/16 | 4 nodes: reads at 2/4, writes at 3/4 nodes |
| usable capacity | 12/16 of raw on that set (EC:4) — the parity is MinIO's choice for the stripe | local `raidz1` keeps 3/4 per pool, then EC halves across nodes |
| layout later | fixed at install — the matrix *is* the EC set | changeable — disks, vdevs, layouts move without touching MinIO |
| heterogeneous nodes | not possible: every node must contribute the same drive count | natural — each node is just one endpoint, any local layout |
| TLS | the grid needs one shared CA across nodes (seed the same CA material, or BYO certs) | same requirement as any MNSD cluster |

Pick **MNMD** when the nodes are identical, the disk plan is settled, and you
want disk *and* node redundancy from MinIO alone with nothing to provision
underneath. Pick **MNSD + ZFS** when nodes differ in disk count or size, the
layout may change later, or you want to reason about failures in whole nodes
instead of in drives.

## Choosing a shape

| Situation | Shape |
|-----------|-------|
| laptop / demo / CI | SNSD, default data dir — nothing to decide |
| one host with N ≥ 4 data disks, backups must survive a disk | SNMD (`--drive` × N) — zero filesystem setup; or SNSD + ZFS (`raidz2` on spinning disks, `raidz1` on fast SSDs) for more usable capacity and a changeable layout |
| a few hosts, one data disk each, must survive a host | MNSD plain |
| identical hosts with several data disks each, must survive a disk *and* a host | MNMD (`--drive` + full `--endpoint` matrix) — no filesystem layer; or MNSD + per-node ZFS, see "Which of the two" above |
| hosts whose disk counts/sizes differ | MNSD + per-node ZFS (layouts may differ per node; keep capacities aligned) — MNMD needs equal drive counts |

The store's TLS story is independent of the topology: whichever shape you
pick, `--tls` (or `pg cert`-minted BYO certificates) works the same — see
[Generating a Certificate](../ha-cert/).

## Related

- [Addons → MinIO](../addon/minio/) — install, `--tls`, distributed mode
  constraints (the hard rules this page builds on), and the
  [MNMD walkthrough](../addon/minio/#multi-node-multi-drive-mnmd)
- [Addons → Silo](../addon/silo/) — the drop-in fork; identical modes
- [Cluster Backup](../ha-backup/) — how the repo is wired into Patroni
  (`pg backup setup`)
- [Example: HA Cluster with a Self-CA MinIO](../ha-example-minio/) — an
  end-to-end verified walkthrough of the single-node variant
