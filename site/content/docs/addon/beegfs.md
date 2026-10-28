---
title: "BeeGFS"
description: "Run a BeeGFS parallel filesystem as a managed addon."
weight: 58
---

[BeeGFS](https://www.beegfs.io/) is a **parallel filesystem** that chose the
easy route. It was born in 2004 at Fraunhofer ITWM in Germany — a research
group that needed Lustre, found its deployment and operations too heavy, and
wrote its own instead, with the goal set from day one: not the biggest scale,
but the least human effort to deploy and run. In 2014 the core team spun out
as the commercial company ThinkParQ, the product was renamed BeeGFS, and it
grew from an institute-internal tool into the shared storage you find on many
European universities and regional supercomputers.

It solves the "many machines, one high-throughput dataset" problem: a local
filesystem lives on a single host, and NFS-style shares cap out quickly under
concurrent large-file IO from many clients. Like Lustre, BeeGFS is a parallel
filesystem — metadata separated from data, files striped across storage
servers, clients reading data blocks directly once they know where they
are — so aggregate bandwidth scales with the number of servers. But at every
point where it could be complicated, it chose simple: service roles may share
one machine, daemons self-register into the cluster when they start (no
hand-synced configuration), growing means adding a box, and plain Ethernet
works — InfiniBand is not mandatory. The trade: it does not chase extreme
scale or the deepest tuning control; what it buys is that a research group or
department cluster **without a dedicated storage team can be up within a day
or two**.

Typical fit: HPC clusters from a few up to a couple hundred nodes, the **data
supply layer for AI training** (GPUDirect Storage since 7.3 — storage-to-GPU
direct paths, so large training/inference jobs read shared datasets directly),
**post-production editing media in film and video** (many editors reading and
writing large concurrent streams without fighting for bandwidth), and
department-scale shared volumes of many large files. One-line selection rule:
national-lab scale, thousands of nodes, a storage team on staff → Lustre;
"usable today, no storage team" → BeeGFS.

> **Platform**: Linux only. BeeGFS daemons run on **host networking** (BeeGFS
> speaks its own TCP/UDP protocol, not HTTP, so there is no container
> port-mapping to hide behind). macOS has no host networking under podman, so
> `pg addon install beegfs` is refused there with a clear error.

> **The client is out of scope, on purpose.** Mounting a BeeGFS filesystem
> needs the `beegfs-client` **kernel module** (DKMS). Kernel modules cannot be
> containerised, so pgcli neither installs nor mounts it — it manages the
> *server* services only (three containers on a converged host, the configured
> subset on a cross-host member). `pg addon beegfs mount-hint` prints the exact
> host-side config a machine with the `beegfs-client` + `beegfs-utils` packages
> needs. See [Mounting a client](#mounting-a-client).

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `beegfs` | Filesystem name (map key); containers are `<prefix>-<role>` for each role this host runs |
| `--image` | `8.4.0` | Version suffix — the manager builds `ghcr.io/thinkparq/beegfs-<role>:<version>` from it (all roles share one tag) |
| `--data-dir` | `<base>/addon/beegfs/<name>` | **One** dir for the whole filesystem; its services use `<dir>/{mgmtd,meta,storage}` |
| `--listen` | `0.0.0.0` | The address clients self-reference as `sysMgmtdHost`; with host networking the daemons actually bind every interface |
| `--password` | generated (mgmtd hosts) | The connection-auth secret (`ConnAuth`) shared by all daemons and every client; generated on first install **only if this host runs mgmtd** — a join host must pass the cluster's existing value |
| `--roles` | all three | v2: which services THIS host runs — comma list of `mgmtd,meta,storage`; omitting it is the converged single-host filesystem |
| `--mgmtd-host` | — | v2: address of the mgmtd host this host's meta/storage register against (required when `--roles` excludes mgmtd) |
| `--node-id` | `1` | v2: this host's BeeGFS node ID (1..655); storage target IDs derive from it (`2` → `201`, `202`, …). Must be unique per service type across hosts |
| `--storage-target` | single dir | v2: one host dir per storage target — repeat for multiple disks (one target each) |
| `--license-file` | — | Path to a ThinkParQ license `.pem` to carry into the mgmtd container (stored in `pg.yaml`, injected as `BEEGFS_LICENSE_FILE_DATA`). **mgmtd host only** — pgcli never generates a license; `pg addon beegfs license` shows/hot-swaps it later |
| `--beegfs-mgmtd-port` | `8008` | Move mgmtd's BeeMsg port (the *only* movable meta/storage registration target); `0` = stock |
| `--beegfs-mgmtd-grpc-port` | `8010` | Move mgmtd's gRPC port (what the `beegfs` CLI dials); `0` = stock |
| `--force` | — | Recreate this host's containers even if they are already running |

There is deliberately **no `--port` for meta or storage**: see [Ports](#ports).

## Install

```bash
pg addon install beegfs              # default filesystem named "beegfs"
pg addon install beegfs --name fs1
pg addon install beegfs --listen 10.0.0.5   # advertise a reachable mgmtd address
```

The install is fully non-interactive. It pulls the images for the roles this
host runs up front (so a mid-cluster network failure never leaves a
half-registered filesystem), then brings them up **in dependency order**:
mgmtd first, then meta, then storage — a join host with `--roles storage` just
runs its own step. meta/storage block until they log
`Registration and management info download complete`, the line that proves both
"mgmtd is reachable" and "this service joined". Each container's first boot
runs a `beegfs-setup-<service>` step (driven by `beegfs_setup_N` env vars the
image's entrypoint consumes) that initialises its target directory; the marker
is idempotent, so a recreated container against a surviving data dir reuses the
existing target.

Re-running `install` is safe and **cluster-granular** (over this host's roles):

- all of them up → skip (use `--force` to recreate, or `pg addon beegfs check`);
- all of them down and present → just start the stored cluster (no pull, no
  recreate);
- anything else (partial, or a fresh install) → `EnsureCluster` recreates the
  set in order.

The install summary lists this host's roles and their container names, the
images, the mgmtd address and its two ports (marked remote on a join host), the
node ID and data dirs, the conn-auth state, and a pointer to
`pg addon beegfs mount-hint`. If `--listen` is the wildcard `0.0.0.0` it prints
an exposure NOTE (a parallel filesystem with TLS off is reachable from any
machine that holds the conn-auth secret).

## Cross-host clusters (v2)

A BeeGFS filesystem scales by adding **hosts**, not by hand-syncing config:
each service daemon starts with `sysMgmtdHost=<mgmtd address>` plus the shared
conn-auth secret and **self-registers into the cluster — mgmtd is the
registry**. pgcli mirrors that model exactly: there is no central deploy
command and **pgcli never SSHes anywhere**. Every host runs its own
`pg addon install beegfs --roles <subset>` with its own local `pg.yaml`, and
BeeGFS does the rest — the same declarative, per-host pattern Patroni cross-host
members use (where etcd, not pgcli, aggregates topology; here the role is
mgmtd's own SQLite registry).

Three hosts, one filesystem:

```bash
# vm01 — management + metadata on this host; conn-auth generated here:
pg addon install beegfs --name fs1 --roles mgmtd,meta --listen 192.0.2.10

# render the verbatim join commands (secret included) from vm01:
pg addon beegfs join-hint --name fs1

# vm02 — two data disks, one storage target each:
pg addon install beegfs --name fs1 --roles storage --mgmtd-host 192.0.2.10 \
    --node-id 2 --password <conn-auth> \
    --storage-target /mnt/d1 --storage-target /mnt/d2

# vm03 — another storage node (or --roles meta to move metadata off vm01):
pg addon install beegfs --name fs1 --roles storage --mgmtd-host 192.0.2.10 \
    --node-id 3 --password <conn-auth> --storage-target /mnt/d3
```

Clients mount from vm01's address as usual — cross-host changes nothing about
the kernel-module client.

Rules and boundaries, honestly stated:

- **`--mgmtd-host` is required** whenever `--roles` excludes mgmtd (there is
  no localhost to fall back to), and the join **must carry the cluster's
  existing conn-auth** — pgcli never generates a secret for a member of an
  existing cluster. A wrong secret fails the install in seconds with a pointer
  to `pg addon password beegfs`, not after a registration timeout.
- **`--node-id` must be unique per service type across hosts** (node IDs are
  per-type namespaces: `meta_1` and `storage_1` coexist). It seeds the storage
  target IDs: node 2's targets are 201, 202, … (target IDs derive as
  `node_id*100 + ordinal`, capped at node_id 655).
- Each `pg.yaml` entry stores **only what its own host runs**; `check`,
  `start`/`stop`, `list` and autostart are all per-host views. The cluster's
  full membership lives in mgmtd (`beegfs ... node list`).
- **mgmtd remains a single point**: if it dies, existing client IO keeps
  flowing (clients talk to meta/storage directly) but registration and admin
  stop. A redundant management pair is an enterprise-license feature, not
  pgcli wiring. The **license rides on the mgmtd host alone** — join hosts
  neither store nor need a PEM (`join-hint` says so), since mgmtd verifies the
  certificate for the whole filesystem.
- There is still **one filesystem per host** — cross-host is the scaling axis,
  not second-filesystems-per-host (the fixed 8005/8003 ports make those
  collide).

## The data directories and persistence

A converged host owns **three** host directories, each bind-mounted into its own
container at `/data/<role>`:

```
<base>/addon/beegfs/<name>/mgmtd     mgmtd.sqlite — the node/target registry
<base>/addon/beegfs/<name>/meta      the metadata target
<base>/addon/beegfs/<name>/storage   the storage target (the actual file data)
```

`--data-dir <dir>` lays all three out as `<dir>/{mgmtd,meta,storage}` in one
flag — operators never address the roles separately. On a cross-host member
only its own role's dir is used. **Multiple storage targets** (the
`--storage-target` repeat) each get their own host dir mounted at
`/data/storage/<targetID>` inside the single storage container; a single target
keeps the plain `/data/storage` path so a v1 store still revives unchanged
(a target directory records its own path internally, so revival must mount the
same path). The three images run as
**root** and own their data dirs, so unlike Gitea (uid 1000) or Grafana
(uid 472) there is **no pre-`chown` dance** — the host dirs are writable as-is.

All data survives container removal: `pg addon remove beegfs` without
`--clean-data` keeps the dirs, and reinstalling the same name **revives**
the filesystem (the install prints a NOTE when it detects an existing
`mgmtd.sqlite`). Revival keeps whatever registry and connection auth the store
already had — a new `--password` only reaches freshly-initialised daemons, not
a surviving SQLite registry.

## Failure handling: striping for performance, ZFS for reliability

BeeGFS's cross-node striping is a **performance** mechanism, not a redundancy
one: in the default RAID0 layout, losing ANY one storage target loses the
chunks it held — and on a striped file that means the file is corrupt.
Node-level data redundancy is buddy mirroring, an enterprise-license feature.
So the community edition splits the two concerns, and the storage side needs a
local-failure scheme underneath it:

- **Across nodes: striping for throughput.** Each file's chunks spread over
  many targets on many hosts; aggregate bandwidth scales with the number of
  servers. This is what `--num-targets`/`--chunk-size` tune. It buys speed,
  never safety.
- **Within each storage server: a resilient volume under every target.** The
  recommended baseline is **ZFS**, because a BeeGFS storage target is "just a
  directory" and ZFS turns that directory into a self-healing store for free —
  checksums against silent corruption, copy-on-write, snapshots, compression.
  The daemon cannot tell the difference, so this layer needs **zero pgcli or
  BeeGFS involvement**: build the pool, make a dataset per target, and point
  `--storage-target` at it.
    ```bash
    # two disks → a mirror vdev (ZFS's RAID1; capacity = one disk, survives
    # either disk dying). raidz1/2 need 3+/4+ disks.
    zpool create bgtank mirror /dev/disk1 /dev/disk2
    zfs create -o recordsize=1M -o compression=lz4 bgtank/t1
    # ...then:  pg addon install beegfs --roles storage ... \
    #              --storage-target /bgtank/t1
    ```
  (mdadm RAID1/5/6 works too, but ZFS's checksumming catches the silent bit
  rot a BeeGFS target would otherwise serve straight to clients.) One target
  per dataset keeps a disk fault entirely out of the BeeGFS target path.
- **Whole-node loss is still not survivable** (community edition): a dead
  server takes its storage targets down with it — local ZFS/RAID survives a
  *disk*, not a *burnt-out machine*. Mitigations without a license: keep
  `meta`/`mgmtd` services duplicated across hosts (free — service redundancy,
  so the filesystem stays mounted and admin keeps working), snapshot the
  metadata, and treat storage data as rebuild-from-upstream where the workload
  allows (re-generable training datasets — the honest community trade-off). A
  temporary full-feature certificate (free, time-limited, one per system) or a
  paid enterprise license is the only way to get real cross-node mirroring.
- **Metadata deserves the same care**: losing the meta target makes the whole
  tree unreadable regardless of data redundancy — keep its dir on a local
  mirror too (it is small). pgcli takes no BeeGFS backups, so protecting the
  mgmtd registry and meta target is host-level work: a ZFS snapshot of the
  volume holding the role dirs, or a quiesced file copy while the daemons are
  stopped.

## Ports

The four BeeGFS listener ports are the **stock protocol ports**, not a pool:

```
mgmtd   8008  BeeMsg — meta/storage register here   (movable: --beemsg-port)
mgmtd   8010  gRPC   — the `beegfs` CLI dials here  (movable: --grpc-port)
meta    8005  metadata service                      (FIXED)
storage 8003  storage service                       (FIXED)
```

Verified on a live host: BeeGFS 8.4's **meta and storage daemons accept no
listen-port override** (`connPortsList`/`connTcpPort` are rejected as invalid,
and `connMetaPort` is accepted but breaks registration). Only mgmtd's two ports
can move, and moving mgmtd's BeeMsg port re-targets meta/storage registration
automatically (the manager emits `connMgmtdPort=<port>` into both). The direct
consequence: **exactly one BeeGFS filesystem can exist per host** — a second
one could never bind 8005/8003 regardless of what pgcli allocated, so
`EnsureCluster` guards it out with an explicit error, and the CLI rejects a
hand-pinned non-default `meta_port`/`storage_port` in `pg.yaml` with the real
reason. There is therefore **no `beegfs_start_port` pool** — unlike every other
addon, BeeGFS ports are fixed, not allocated.

## Connection auth and TLS

Every daemon in the filesystem and every client share one
**connection-authentication secret** (`ConnAuth`, delivered as the
`CONN_AUTH_FILE_DATA` env var that the image's init writes to
`/etc/beegfs/conn.auth`). It is generated on first install (20 random chars)
**only on a host that runs mgmtd**, printed once, and stored in `pg.yaml` —
retrieve it with `pg addon password beegfs`. A cross-host join host must be
given that existing value via `--password` (see
[Cross-host clusters](#cross-host-clusters-v2)); pgcli never generates a secret
for a member of an existing cluster. Like every pgcli secret it is never
defaulted into the config by `ApplyDefaults`.

**TLS is disabled on mgmtd** (`--tls-disable true`, the explicit v1 decision).
This means only clients that pass `--tls-disable` too can join, and it is a
real exposure consideration: a wildcard `--listen 0.0.0.0` with TLS off leaves
ports 8008/8010/8005/8003 open to any machine on the network that holds the
conn-auth secret. For anything not on a private network, either bind loopback
(`--listen 127.0.0.1`) or firewall those four ports.

**Licensing**: as of BeeGFS 8 a license is expected on every deployment.
**pgcli never generates a license** (same principle as Gitea's crypto secrets)
— but it **carries** one you obtained: the PEM content is stored in `pg.yaml`
(so it survives and travels with your config backups) and injected into the
mgmtd container as `BEEGFS_LICENSE_FILE_DATA`, which the official image's init
writes to `/etc/beegfs/license.pem` and mgmtd's start.sh passes as
`--license-cert-file`. Only the mgmtd host may carry it: its image is the only
one shipping the license library, and its daemon verifies the certificate for
the whole filesystem (the install refuses `--license-file` on a join host).

What a license costs, by tier (all three self-serve via ThinkParQ's portal):

| Tier | Price | What it does |
|---|---|---|
| **community** | free | Lifts the unlicensed limits (8.4's five-client mount cap, quota warnings); has a capacity ceiling and does NOT unlock enterprise features |
| **temporary** | free trial | Unlocks everything for a limited period; since 8.4 one system can hold only one |
| **enterprise** | paid | Storage pools, quotas, mirroring (buddy groups), the HA management pair |

> **Mirroring (buddy groups) is an ENTERPRISE feature — it needs a paid
> license.** Without one, mgmtd refuses every mirroring request (`beegfs
> mirror create` → "no license loaded" — verified on a live cluster). The free
> community license does NOT unlock it; only a paid enterprise license (or a
> temporary full-feature trial certificate) does. Data redundancy in the
> community edition is limited to running multiple meta/mgmtd daemons — that
> is service redundancy, not mirrored storage targets.

Obtaining a PEM is out-of-band and takes a minute for a community license:
`beegfs license --get` prints a **system-bound URL** → open it in a browser →
download the `.pem`. Then hand it to pgcli — at install time
(`--license-file license.pem`) or later with `pg addon beegfs license --set`
(see [License status and hot-swap](#license-status-and-hot-swap)).

The 8.4 realities of running **unlicensed**: mgmtd logs a quota warning,
every `beegfs` command nags, client mounts are capped at **five** (a sixth
mount is rejected), and enterprise features are refused outright (e.g.
`beegfs mirror create` fails with "no license loaded"). Any valid license —
even the free community one — lifts the warnings and the mount cap.

## Mounting a client

The client is a host **kernel module** — pgcli does not install it. To mount a
filesystem from a Debian/Ubuntu host, add ThinkParQ's apt repository and install
the client packages yourself. For 8.4 on Debian 12 (bookworm) — the exact
commands, verified on a test host:

```bash
# 1. the BeeGFS signing key (fingerprint 0683 AF2B 9AC5 BC7D 59B2 D623 40CE 4E24 3CD5 0804)
wget https://www.beegfs.io/release/beegfs_8.4/gpg/GPG-KEY-beegfs -O /etc/apt/trusted.gpg.d/beegfs.asc
# 2. the repo file (bookworm = Debian 12; use beegfs-trixie.list for Debian 13,
#    beegfs-jammy/noble.list for Ubuntu, …)
wget https://www.beegfs.io/release/beegfs_8.4/dists/beegfs-bookworm.list -O /etc/apt/sources.list.d/beegfs.list
apt update
# 3. client + tools; the module is DKMS-built against the kernel headers
apt install beegfs-client beegfs-utils beegfs-tools linux-headers-$(uname -r)
```

On RHEL-family hosts the same repo lives under
`https://www.beegfs.io/release/beegfs_8.4/dists/` as an `.repo` file
(`rpm --import …/GPG-KEY-beegfs` + `wget …/beegfs-rhel9.repo -O
/etc/yum.repos.d/beegfs.repo`). Always take the URLs for *your* BeeGFS version
and distribution from the [ThinkParQ download
page](https://www.beegfs.io/download/) — pinning the server to 8.4 means clients
should mount from the matching 8.4 repo.

`beegfs-client` depends on `build-essential` and recommends kernel headers —
DKMS compiles the `beegfs` module against `linux-headers-$(uname -r)` at install
time, so it needs the running kernel's headers present and will rebuild on every
kernel update.

Then write the config `pg addon beegfs mount-hint` prints — it renders the three
files verbatim:

```bash
pg addon beegfs mount-hint --name fs1
```

```
1. /etc/beegfs/conn.auth (mode 0600) — the SAME secret as the servers
2. /etc/beegfs/beegfs-client.conf — set in the main section:
     sysMgmtdHost=<mgmtd-host>          # + connMgmtdPort=<port> only if mgmtd's BeeMsg port was moved
3. /etc/beegfs/beegfs-mounts.conf (one mount per line, fields:
   <mountpoint> <client-conf> <fstype>):
     /mnt/beegfs /etc/beegfs/beegfs-client.conf beegfs
```

> **conn.auth gotcha:** the client's connection-auth file must end in a
> newline. `echo <secret> > /etc/beegfs/conn.auth` works; `printf <secret> > …`
> (no trailing newline) is rejected with `tried to authenticate stream with
> wrong secret` — the trailing byte is part of what both sides compare.

Then mount and verify:

```bash
systemctl restart beegfs-client            # reads beegfs-mounts.conf and mounts
df -h /mnt/beegfs
echo hi > /mnt/beegfs/t && cat /mnt/beegfs/t   # end-to-end write/read
```

Because the cluster runs with TLS disabled, every `beegfs` CLI call needs the
disabled-TLS form pointed at the gRPC port; `node list` shows all four node
types (management, meta, storage, and this client once mounted):

```bash
beegfs --tls-disable --mgmtd-addr=<mgmtd-host>:8010 node list
```

Common client-side operations (all verified against 8.4 — the `beegfs` CLI is
the 8.x rewrite; 7.x `beegfs-*-ctl` incantations do not apply):

```bash
B="beegfs --tls-disable --mgmtd-addr=<mgmtd-host>:8010"

$B node list                          # every node: mg / meta / storage / client
$B target list                        # storage targets, their node and state
$B entry info /mnt/beegfs/big.bin     # one file's stripe pattern and the
                                      # targets its chunks landed on
$B entry set /mnt/beegfs/data --num-targets 3 --chunk-size 4Mi
                                      # new files under data/ stripe across 3
                                      # targets, 4 MiB chunks (dirs set the
                                      # pattern for what is created after)
$B entry set /mnt/beegfs/data --num-targets 0   # back to the cluster default
$B health check                       # cluster-wide health report
$B stats server                       # live IO counters per server (8.x stats)
```

`mount-hint` is pure local rendering — it never touches podman, since the client
is out of pgcli's management. It renders the mgmtd address from the config
entry: on a cross-host member that is the stored `--mgmtd-host`; when the local
`--listen` is the wildcard, it prints `<mgmtd-host-ip>` as a placeholder
(0.0.0.0 is not dial-able) rather than a value clients cannot connect to.

## Health check

```bash
pg addon beegfs check             # default name
pg addon beegfs check --name fs1
```

Reports the aggregate state of **this host's roles** (`running` / `stopped` /
`partial (mgmtd,meta)`) and, per service, whether its container is up and
whether it logged its readiness marker — mgmtd `Waiting for shutdown signal`
(serving), meta/storage `Registration and management info download complete`
(joined). None of the images ships `beegfs-ctl`/`check-servers`, so the
daemons' own logs are the authoritative *in-container* signal; the cluster-wide
membership lives in mgmtd — for the whole picture use the `beegfs` gRPC CLI
above (`node list`), and for a stopped cluster `check` still works (it reports
state, it does not require running).

## Retrieving the connection-auth secret

```bash
pg addon password beegfs             # the "beegfs" filesystem
pg addon password beegfs --name fs1
```

`pg addon list` shows only a presence marker (`Auth: none` / `set
(connection-auth)`) and prints the secret itself only with `--show-password`, so
it never leaks to a casual listing.

## License status and hot-swap

```bash
pg addon beegfs license                     # what does mgmtd think of the cert?
pg addon beegfs license --name fs1 --set /path/license.pem   # load/replace, reloads mgmtd
pg addon beegfs license --name fs1 --clear                    # drop it again
```

Run on the **mgmtd host** (the license lives there — see
[Connection auth and TLS](#connection-auth-and-tls)). Without flags it reports
both sides: whether `pg.yaml` carries a PEM, and what mgmtd itself logged when
loading it — `Successfully loaded license certificate` for a valid one, or the
verification errors when mgmtd rejects a bad/expired certificate. pgcli only
shape-checks the PEM (BEGIN/END block); the real verification is always
mgmtd's.

`--set <pem>` reads the file (shape-checked for a BEGIN/END PEM block — the
cryptographic verification is mgmtd's, not pgcli's), stores the content in
`pg.yaml`, and **recreates the mgmtd container** so the new env lands (podman
env only exists from creation). meta/storage and every client re-register on
their own — the swap is a few seconds and IO-safe. `--clear` removes the PEM
and reloads unlicensed. `--set` and `--clear` are mutually exclusive; passing
`--license-file` at install is the same load without the recreate (the container
is being created anyway).

## List

```bash
pg addon list
```

The BeeGFS block ("Infra add-ons (beegfs)") prints one entry per filesystem
with its aggregate state over this host's roles, the role list, the mgmtd
address and its two ports (marked remote on a join host), the fixed
meta/storage ports, conn-auth presence, image, the data dirs of the roles this
host runs (one line per storage target), and the container names. When podman
is unavailable it still lists the configured entries (state shown as `n/a`).

## Start and stop

Containers run with podman's `unless-stopped` restart policy, so they come back
with the podman service after a host reboot. `start`/`stop` manage **this host's
roles** explicitly, respecting dependency order:

```bash
pg addon stop beegfs     # storage → meta → mgmtd (reverse order, targets stay GOOD)
pg addon start beegfs    # mgmtd → meta → storage
```

A cross-host member runs only its own subset (a `--roles storage` host starts
and stops just storage).

## Auto-start on Boot

```bash
pg autostart enable --beegfs --name fs1
```

BeeGFS is the **only cluster-granular autostart**: `pg start --autostart` brings
up the filesystem as this host runs it (mgmtd → meta → storage in order, or the
configured subset), not one container.
Because the images run as root and own their dirs, an autostart needs no host
privileges beyond podman itself (no chown step like Gitea's). The client on
each mounting host re-attaches on its own via `beegfs-mounts.conf` — pgcli
never manages that. See [Autostart]({{< relref "../autostart" >}}) for the full
mechanism, status, and disabling.

## Remove

```bash
pg addon remove beegfs                # keeps the data dirs — reinstall revives the filesystem
pg addon remove beegfs --clean-data   # deletes them too (the data is gone)
```

Remove tears down this host's roles in reverse order (storage → meta → mgmtd). `--clean-data`
refuses to delete a directory that is still a mount point. **A client mountpoint
pointing at this host must be unmounted either way** — pgcli cannot reach the
client (it is not pgcli's), so unmount it on the client host first.

## Logs

A filesystem spans up to three containers, so `pg logs addon beegfs` takes
`--service` to pick one (default `mgmtd`):

```bash
pg logs addon beegfs                       # mgmtd (default)
pg logs addon beegfs --service meta -f     # follow the metadata daemon
pg logs addon beegfs --service storage -n 200
```

`--service` is only valid for `beegfs` (a value like `meta` against another
addon type is rejected).

## Troubleshooting

- **`install beegfs --name fs2` refuses with "a BeeGFS filesystem already
  exists on this host"** — this is the one-filesystem-per-host rule, not a
  bug: meta (8005) and storage (8003) cannot move their listen ports in 8.4, so
  a second filesystem on the same host could never bind them. Grow by adding
  roles on ANOTHER host (`pg addon beegfs join-hint` renders the commands), or
  `pg addon remove` the first one.
- **meta/storage stuck "not registered" in `check`** — mgmtd must be serving
  first; if mgmtd's container died, both block on registration until
  `beegfsSetupTimeout` (60 s) fails the install. Check
  `pg logs addon beegfs --service mgmtd` for why mgmtd exited (port already in
  use on the host, or an un-writable data dir).
- **`install` refuses on the `meta_port`/`storage_port` config check** — a
  non-default value was hand-edited into `pg.yaml`. The daemons cannot bind
  those, so remove the pinned values and let them fall back to 8005/8003.
- **Client cannot mount / `beegfs` CLI says TLS error** — the cluster runs with
  mgmtd TLS disabled; every client and every `beegfs` CLI call needs
  `--tls-disable` too, and `/etc/beegfs/conn.auth` must be the *exact* `ConnAuth`
  from `pg addon password beegfs`.
- **A join host fails fast with "wrong secret"** — the `--password` passed to
  its install is not the cluster's connection-auth value (BeeGFS logs
  `tried to authenticate stream with wrong secret`). The install aborts in
  seconds rather than timing out; re-run with the value from
  `pg addon password beegfs` on the mgmtd host.
- **`pg addon beegfs license` prints a verification error from mgmtd** — the
  stored PEM is not valid for *this* system (certificates are system-bound —
  request one with `beegfs license --get` on the mgmtd host) or has expired.
  The container still runs and community functionality is unaffected; mgmtd
  simply behaves as unlicensed. `--clear` (or `--set` with the right PEM)
  fixes the config side.
- **Clone/mount to `localhost` fails from another machine** — a wildcard
  `--listen 0.0.0.0` renders `sysMgmtdHost` as a placeholder in `mount-hint`;
  install with `--listen <reachable-ip>` (with `--force`) so clients get a real
  mgmtd address.

## Known limitations

- **Linux only.** Host networking is mandatory and podman provides it on Linux
  only; installation is refused on macOS. No bridge fallback exists because
  BeeGFS is not HTTP.
- **One filesystem per host, fixed ports.** pgcli pins the stock ports
  (8008/8010/8005/8003); meta and storage have no movable listen port in 8.4,
  so a single host holds a single filesystem. Scaling out is cross-host
  (`--roles`), not a second filesystem.
- **The client is never installed or mounted by pgcli.** It is a kernel module
  (DKMS) that cannot be containerised; pgcli manages only the server
  containers and *documents* the mount via `mount-hint`.
- **TLS off; licenses are carried, not issued.** mgmtd runs `--tls-disable
  true`; the `beegfs` gRPC CLI and clients must match. pgcli never generates a
  license and never handles renewal — it carries a ThinkParQ PEM you obtained
  out of band (`beegfs license --get` prints the system-bound URL) to the
  mgmtd container, and `pg addon beegfs license` shows/hot-swaps it. Mirroring,
  pools, and quota stay enterprise-license features you still have to buy; an
  unlicensed 8.4 caps client mounts at five.
- **Cross-host deployment is per-host and manual by design (v2).** Each host
  runs its own `pg addon install beegfs --roles ...`; pgcli never SSHes and has
  no "deploy the whole cluster from one box" command. Growing means running one
  command on the new host (the conn-auth secret is carried by hand — see
  `join-hint`), and every host's `pg.yaml` describes only its own slice.
- **mgmtd is a single point.** The management daemon is the cluster's registry;
  if it stops, new registrations and admin operations stop (existing mounted
  clients keep doing IO against meta/storage). A redundant management pair is a
  BeeGFS *enterprise-license* feature and is outside pgcli's v1/v2 wiring.
