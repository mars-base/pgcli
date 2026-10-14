---
title: "SeaweedFS"
description: 'Run SeaweedFS (the Go, Apache-2.0 S3-compatible object store) as a pgcli addon — single-node "weed mini" or a cross-host "weed server" cluster with filer metadata in a shared PostgreSQL, native S3 TLS, and one shared credential pair across the cluster'
weight: 46
---

[SeaweedFS](https://github.com/seaweedfs/seaweedfs) is a Go, Apache-2.0
S3-compatible object store whose defining trait is **volume-granularity disk
management**: data is split into fixed-size *volumes* (`.vol` files) that the
master places across volumeservers, giving O(1) reads and cheap small-file
storage. pgcli runs it as a **standalone, top-level addon** with the same CLI
surface as [`garage`](../garage/), [`minio`](../minio/), [`silo`](../silo/) and
[`rustfs`](../rustfs/) (install, list, logs, autostart, password, `pg mc`), but
its runtime model diverges in four ways that shape this page:

1. **One process, all roles.** A single node runs `weed mini` — master,
   volume server, filer and S3 gateway in one process (the officially endorsed
   single-binary deployment). A cluster member runs `weed server -filer -s3`,
   one per host, all roles together — never four containers.
2. **Three rendered files, argv for everything else.** pgcli renders `s3.json`
   (the S3 identity) and, for a cluster, `filer.toml` (the PostgreSQL metadata
   store) under the instance directory and bind-mounts them read-only; the rest
   of the configuration is `weed` command-line flags.
3. **Cluster metadata lives in a shared PostgreSQL.** Every cluster node fronts
   filer + S3, so the filer namespace must be one database, not per-node
   leveldb2 — pgcli points all members at one `--dsn` PostgreSQL. This is the
   natural home ground of pgcli: the store can sit next to (or in) a pgcli-managed
   instance.
4. **Native S3 TLS** — `--tls` (self-signed) or `--tls-cert`/`--tls-key` (BYO,
   e.g. [`pg cert`](../self-cert/) output) — the MinIO posture, not Garage's
   nginx-front one. With TLS on, the pooled S3 port serves **HTTPS only**; the
   cleartext listener is not bound at all.

> **Platform support:** tested on Linux (host networking). The macOS bridge
> path is code-complete and follows the same shape as the other object stores,
> but has not been exercised on a Mac yet.

## How It Works

One container per instance, from the upstream
`docker.io/chrislusf/seaweedfs:4.48_large_disk` image (pull-only). pgcli
overrides the image's `ENTRYPOINT` and runs `/usr/bin/weed -logtostderr=true
<subcommand> …` directly: the stock entrypoint would `chown -R /data` and drop
to uid 1000 on every start — on a large data directory that is exactly the
silent slowdown shape pgcli avoids, and without `-logtostderr` weed logs to
files inside the container where `pg logs` cannot see them.

Host state lives under `<base-dir>/addon/seaweedfs/<name>/`:

- `data/` → `/data` (volumes and, single-node, the filer's leveldb2 metadata) —
  replaced by one bind mount per drive (`/data1`…`/dataN`) in multi-disk mode,
  see [Multi-disk](#multi-disk),
- `s3.json` → `/etc/seaweedfs/s3.json:ro` (the S3 identity; 0600 on the host),
- `filer.toml` → `/etc/seaweedfs/filer.toml:ro` (cluster / `--dsn` only; the
  `[postgres2]` section rendering the metadata DSN),
- `cert.pem`/`key.pem` → mounted under `/etc/seaweedfs/` when TLS is on.

Credentials are an **access key / secret key pair** rendered into `s3.json`
(SeaweedFS's static-identity mechanism — `AWS_*` env is lower priority and gets
overridden by the local store, so pgcli never uses it):

- **`root_user`** is the access key id — on a **single-node** install it is
  generated on first install (`SW` + 28 hex chars) or supplied via
  `--root-user`.
- **`root_password`** is the secret key (64 hex chars), generated or supplied
  via `--root-password`.
- Under `--cluster` **both are required and must be identical on every node** —
  see [Multi-node cluster](#multi-node-cluster).

The identity is rendered with the bare `Admin` action, which in SeaweedFS's
permission model short-circuits to full access. All values land in `pg.yaml`
(`addons.seaweedfs.<name>.*`) so a recreate is stable. The install also renders
one bucket — named after the instance (`--bucket` overrides): single-node `weed
mini -bucket` creates it at boot; a cluster seeds it through the S3 API with
`mc mb --ignore-existing` once the node's health gate is green.

## Install

```bash
# default instance name "seaweedfs", four ports from the seaweedfs pool, loopback bind
pg addon install seaweedfs

# a named instance with an explicit data directory
pg addon install seaweedfs --name store --data-dir /srv/seaweedfs

# spread one node's volumes across several separately-mounted disks
pg addon install seaweedfs --name store --drive /mnt/sw-disk1 --drive /mnt/sw-disk2

# a fixed master port (volume/filer/s3 take the next three; gRPC twins +10000)
pg addon install seaweedfs --name store --port 8400

# expose the store on the network instead of loopback only
pg addon install seaweedfs --name store --listen 0.0.0.0

# supply your own access key / secret key (seaweedfs limits: key >= 8, secret >= 16 chars)
pg addon install seaweedfs --name store --root-user SW<...> --root-password <secret>

# native TLS (self-signed) — the S3 port becomes HTTPS-only
pg addon install seaweedfs --name store --tls

# join a cross-host cluster (see Multi-node cluster below) — credentials are MANDATORY here
pg addon install seaweedfs --name store --cluster c1 --advertise-host 10.0.0.11 \
  --replication 001 --root-user SW<...> --root-password <secret> \
  --dsn "postgres://admin:<pw>@10.0.0.11:35432/seaweed_store" \
  --peer 10.0.0.11:8400 --peer 10.0.0.12:8400 --peer 10.0.0.14:8400
```

The single-node output reports endpoints and the minted credentials:

```
  [OK] cluster leader: 127.0.0.1:8400.18400

✓ seaweedfs installed: "store"
  Container:     pgcli-seaweedfs-default-store
  Image:         docker.io/chrislusf/seaweedfs:4.48_large_disk
  Data:          /root/.pgcli/addon/seaweedfs/store/data
  S3 endpoint:   http://127.0.0.1:8403
  Ports:         master=8400 volume=8401 filer=8402 s3=8403 (gRPC twins +10000)
  Bucket:        store

  Access key:    SW<generated>
  Secret key:    <generated>
  Try it:        pg mc alias set store http://127.0.0.1:8403 SW<...> <secret> && pg mc ls store
```

The install ends with a **health gate**: pgcli polls the master's JSON
`/cluster/status` API for up to 30 s before declaring success — a detached
container with `--restart unless-stopped` would otherwise swallow a bad argv
into a crash loop behind a green-looking install. Only after the gate is green
does the bucket seeding run.

Re-running install against a **live** instance is a no-op: the container is not
recreated (a stopped one is simply started, with a notice), flags are merged
into the stored config, and existing credentials are kept. Pass `--force` to
recreate the container so changed ports, listen address, or credentials take
effect — without losing the data directory or the metadata database.

> **Bind address:** the default `127.0.0.1` keeps the store local; `--listen
> 0.0.0.0` (or the `listen` key in `pg.yaml`) exposes it on the network. A
> **cluster member always binds `0.0.0.0`** regardless of `--listen` — peers
> must reach its gRPC twin (`port + 10000`) for Raft — while `-ip` still
> registers the single address peers dial.

> **`-ip` is never left to auto-detection.** weed registers its volumes under
> whatever address it guesses; the filer/S3 data path then dials that guess and
> uploads 500 on an otherwise healthy-looking container. pgcli passes `-ip`
> explicitly (`--listen` value single-node, `--advertise-host` in a cluster) —
> verified the hard way on a test host.

### TLS

SeaweedFS serves HTTPS natively on the S3 port; there is no nginx front and no
web-console leg to proxy. Two shapes, same as minio/rustfs:

```bash
# self-signed: pgcli generates cert+key under the instance dir (30-day leaf,
# SANs covering the bind/advertise address, 127.0.0.1 and localhost)
pg addon install seaweedfs --name store --tls

# bring your own: e.g. a `pg cert`-issued pair (a CA certificate is refused)
pg addon install seaweedfs --name store --tls-cert fullchain.pem --tls-key privkey.pem
```

What TLS changes under the hood (verified): the cleartext listener is **off**
(`-s3.port=0` — the pooled port simply refuses TCP connections), and the same
pooled S3 port becomes `-s3.port.https`. So the endpoint printed at install is
`https://…:8403`, not an extra port. Point `pg mc` at it as usual — the loopback
alias gets `--insecure` automatically; for a real or generated CA, pass its
`ca.crt` to your S3 client.

## Multi-disk

A single node can use several disks: `weed`'s own `-dir` is a comma list
(`dir[,dir]…`) that one volume server stores its volumes across, and pgcli
exposes it as a repeatable `--drive` — one host directory per disk, each
bind-mounted at its own slot:

```bash
# each --drive must be a separately mounted disk; the drives fully replace
# --data-dir (combining the two is an error)
pg addon install seaweedfs --name store \
  --drive /mnt/sw-disk1 --drive /mnt/sw-disk2 --drive /mnt/sw-disk3
```

The summary and `pg addon list` then show the mapping instead of a single
`Data:` line:

```
  Drive 1:       /mnt/sw-disk1 -> /data1
  Drive 2:       /mnt/sw-disk2 -> /data2
  Drive 3:       /mnt/sw-disk3 -> /data3
```

and the container runs with `-dir=/data1,/data2,/data3` (verified: the volume
server's `/status` reports each host directory as its own `DiskStatuses` entry
with independent disk accounting, and `remove --clean-data` walks the drives
one by one, refusing any that is still a live mount point).

Two semantics worth knowing:

- **Placement, not RAID.** This is one volume server spreading `.vol` files
  across its directories — no striping or mirroring at the OS level, and no
  erasure coding *between* the drives either (the cluster replication story is
  orthogonal — see [Multi-node cluster](#multi-node-cluster)). A drive that
  disappears takes the volumes on it; whether that loses data depends on the
  replication code, not on the local layout.
- **Advisory device check.** `install` warns about any drive sitting on the
  same device as the host root filesystem — multi-disk only pays off on
  separately mounted disks. Unlike MinIO (which refuses such a drive at
  startup), weed happily uses them, so pgcli warns and continues. Format and
  mount each disk first; pgcli never partitions or formats anything.

Drives are a **per-node knob**, and they compose with the cluster: pass
`--drive` on each member's install command and every node runs its own
multi-disk volume server behind the shared cluster (verified on a 3-host
`weed server` cluster where each member mounted two drives — every member's
process showed `-dir=/data1,/data2` with two independent `DiskStatuses`
entries, and the 001 replica of one 8 MB volume landed on the real disk of one
node and on the other drive of the replica node, i.e. each node's volume
server picked among its own dirs while placement chose across nodes). The
nodes do not need the same drive count; each account only sees its own `-dir`
list. Stop/start and `--force` recreate handle the drive set like any other
config change — the mounts and `-dir` are rebuilt from `pg.yaml`.

## Multi-node cluster

The cluster is **MinIO-MNMD shaped, not Garage's bootstrap/join**: there is no
first node, no join command with a carried cluster secret, no layout to
assign. You run the **same install command on every host** — Raft membership
is the full `--peer` list every node passes, and a node that starts before
quorum simply waits (its health gate warns instead of failing; the install that
completes quorum finishes the bucket seeding).

Two things differ from a bigger single node, and both are **shared by
definition** — every member's command carries them identically:

1. **One credential pair for the whole cluster.** Each gateway authenticates
   against only its *own* rendered `s3.json`; SeaweedFS has no
   shared-credential backend without a standalone IAM server (out of pgcli's v1
   scope), so per-node auto-generation would leave the three gateways
   accepting three different identities. pgcli therefore **refuses to
   auto-generate under `--cluster`**: pass the same `--root-user` and
   `--root-password` on every node. Run the first node's install with them,
   copy the pair from its printed summary, and carry it verbatim to the rest
   (pgcli never executes anything remotely). A half pair (only one of the two)
   is an error; so is re-running a single-node config's install with `--cluster`.
2. **The shared filer metadata database.** `--dsn postgres://user:pass@host:port/db`
   is required, must be dialable **from every host** — so use the host-published
   port / LAN address, never a container-local `127.0.0.1:5432` — and the
   database itself must already exist: pgcli only probes reachability, it never
   creates (or drops) databases it does not own. The metadata *tables* need no
   setup: weed's `postgres2` store creates `.system`, `filemeta` and one table
   per bucket inside it on first use (verified). `sslmode=disable` is rendered
   into `filer.toml`, so reachability + `pg_hba` is all that must line up.

### Three-host walkthrough

Prepare the metadata database once, anywhere all nodes can reach (here a
pgcli-managed instance on host 1, host port 35432):

```bash
pg exec -i demo "CREATE DATABASE seaweed_store"
```

Then, on **each** host, the same command with that host's own
`--advertise-host` (keys below are placeholders — use one pair everywhere):

```bash
# on 10.0.0.11 — the printed summary hands you the command for the others
pg addon install seaweedfs --name store --cluster c1 \
  --advertise-host 10.0.0.11 --replication 001 \
  --root-user SW<...> --root-password <secret> \
  --dsn "postgres://admin:<pw>@10.0.0.11:35432/seaweed_store" \
  --peer 10.0.0.11:8400 --peer 10.0.0.12:8400 --peer 10.0.0.14:8400

# on 10.0.0.12 — identical except --advertise-host
# on 10.0.0.14 — identical except --advertise-host
```

Add `--drive` to that same command to give each member its own multi-disk
volume server (`--drive /mnt/sw-disk1 --drive /mnt/sw-disk2` — see
[Multi-disk](#multi-disk)); it is a per-node flag, so hosts may carry
different drive sets. Verified on a 3-host cluster.

The summary ends with a copy-paste block for the remaining hosts with exactly
that shape (credentials and DSN filled in from this node's config).

What the first installs say while the cluster is still assembling is *normal*:

- Host 1 comes up with a leader already visible (a lone weed master with an
  odd peer count can still win its own election here — the observed behaviour
  with a 3-node list is host 1 leading immediately), but the bucket seed may
  fail with a connection refused: the cluster's S3 side is not serving yet.
  pgcli prints `[!] bucket seeding incomplete … re-run this install command
  once the cluster has quorum` and **exits 0**.
- Host 2 (or 3) completes quorum; the same idempotent seed succeeds there
  (`Bucket created successfully`). Re-running host 1's command afterwards
  re-checks quietly and `mb --ignore-existing` stays a no-op.

After all three, any member shows the whole cluster:

```
$ pg weed --name store --shell "cluster.status"

cluster:
	id:       topo
	status:   unlocked
	nodes:    3
	topology: 1 DC, 3 disks on 1 rack
…
```

### Reading and writing through any node

Because credentials are shared and the filer namespace is one database, **any
gateway serves any object**: write through host 2's S3 port and read back
through host 3's with the same key — verified end-to-end on a 3-host test
cluster, including a wrong-secret attempt coming back as
`The request signature we calculated does not match` (it is not a
per-node local trust store). `--replication 001` keeps one remote copy of every
volume (`volume.list` shows `ReplicaPlacement:001` and each volume on two
data nodes).

Stop one member and the cluster keeps serving (Raft quorum is a majority; with
3 masters, 2 keep electing and writing):

```bash
# on host 3
pg addon stop seaweedfs --name store
# on host 1 — reads still succeed via host 2's gateway
pg mc cat store2/store/roundtrip.bin | sha256sum
```

`pg addon start` rejoins the node; its volumes re-register under the same
`-ip`. **Changing cluster membership is not orchestrated**: the `--peer` list
is static Raft configuration — add or remove a member by rebuilding the
cluster (v1 limitation, see [Limitations](#limitations)).

### Getting the cluster's AK/SK

The pair is by design present in **every** node's `pg.yaml` (you carried it
there), so unlike garage's CRDT key — whose secret lives on exactly one host —
`pg addon password seaweedfs --name store` works on any member. If the printed
secret is lost everywhere, pick a new pair and re-install each node with
`--force` (the rendered `s3.json` is rewritten; data and metadata are
untouched).

## Using the mc Client

`pg mc` runs MinIO's `mc` from a throwaway container — no local install — and
speaks SeaweedFS's S3 API like any other store. Use the **access key** in place
of a user and the **secret key** in place of a password:

```bash
pg mc alias set store http://127.0.0.1:8403 SW<...> <secret>   # Linux, local
pg mc mb store/backups
pg mc cp ./dump.pglz store/backups/
pg mc ls store
```

Aliases persist in `~/.mc/config.json`; flags `pg`'s parser would claim go after
`--` (`pg mc rm store/scratch -- --force`). On macOS point the alias at
`host.containers.internal:<s3-port>` instead of `127.0.0.1`. Under TLS, use the
`https://` endpoint (loopback aliases are auto-`--insecure`).

## Administering via `pg weed`

`pg weed` runs SeaweedFS's own `weed` control tool inside a **running** member's
container — the replacement for hand-written `podman exec
pgcli-seaweedfs-<ns>-<name> …`. `--name` selects the instance (default: the
first by name); pgcli's own flags must come before the subcommand word, and
`weed`'s flags pass through untouched. Two shapes:

```bash
pg weed <args>...           # verbatim: /usr/bin/weed <args> in the container
pg weed --shell <line>...   # each line piped into `weed shell`, pre-wired to
                            # this instance's master and filer over loopback
```

The `weed shell` menu is the day-2 surface (command names verified against 4.48
— the shell help lists far more):

```bash
pg weed --shell "cluster.status"                     # topology, nodes, volumes
pg weed --shell "volume.list"                        # every volume, its replicas, sizes
pg weed --shell "s3.bucket.list"                     # buckets as seen by the filer
pg weed --shell "fs.ls /"                            # the filer namespace, root
pg weed --name store --shell "volume.list" "s3.bucket.list"   # several lines at once
pg weed flag.lookup 0,1,2                            # verbatim subcommand
pg weed shell                                        # interactive (needs a TTY)
```

`weed` CLIs can print their result and then linger with open gRPC channels;
pgcli cuts such a command off after its output has been quiet for 3 s (10-minute
overall cap), so a hang surfaces as a loud timeout instead — re-run it.

## Ports

Each instance takes **four consecutive ports** from SeaweedFS's own pool,
starting at `seaweedfs_start_port` (default **8400**): master, volume, filer,
s3 — and every role also opens a **gRPC twin at `http port + 10000`** (8400 →
18400, …), which pgcli reserves in the pool too. A second instance therefore
lands on 8404-8407 / 18404-18407, and a seaweedfs, a garage and a minio
instance never collide (separate pools: 8400 / 3900 / 9000):

```bash
pg addon install seaweedfs --name store   # 8400-8403 + 18400-18403
pg addon install seaweedfs --name lake    # 8404-8407 + 18404-18407
```

`--port` sets only the master anchor; the rest follow. Only the S3 port is a
client endpoint; master 8400 also serves the `/cluster/status` JSON API and the
operator UI pages (the admin UI and webdav are switched off in the argv, and
iceberg/lance side listeners are pinned to 0 so weed opens no extra holes).

## Configuration

Instances live under the top-level `addons.seaweedfs` map in `pg.yaml`:

```yaml
namespace: default
seaweedfs_start_port: 8400    # seaweedfs's own pool (master/volume/filer/s3, four each, gRPC twins reserved)
addons:
  seaweedfs:
    store:
      container_name: pgcli-seaweedfs-default-store
      name: store
      image_tag: docker.io/chrislusf/seaweedfs:4.48_large_disk
      # data_dir: /srv/seaweedfs    # omit for <base-dir>/addon/seaweedfs/store/data
      # --- multi-disk (replaces data_dir; mutually exclusive with it) ---
      # drives:                      # one host dir per disk, mounted /data1../dataN,
      #   - /mnt/sw-disk1            #   and -dir lists them comma-joined
      #   - /mnt/sw-disk2
      listen: 127.0.0.1
      port: 8400                    # master anchor; volume/filer/s3 = next three
      volume_port: 8401
      filer_port: 8402
      s3_port: 8403
      root_user: SW<generated>         # s3.json access key
      root_password: <generated>       # s3.json secret key
      bucket: store                    # seeded on first boot (default = name)
      volume_size_mb: 1024             # -master.volumeSizeLimitMB (weed's own default 128 is tiny)
      autostart: false                 # pg autostart enable --seaweedfs --name store
      # --- multi-node cluster keys (omit for single-node) ---
      # cluster: c1                    # non-empty ⇒ cluster mode (weed server)
      # advertise_host: 10.0.0.11  # the -ip peers dial; required in cluster
      # replication: "001"             # placement code XYZ (X remote copies,
      #                                #   Y racks, Z data centers), immutable in-cluster
      # peers:                         # FULL Raft list, written on every node
      #   - 10.0.0.11:8400
      #   - 10.0.0.12:8400
      #   - 10.0.0.14:8400
      # filer_dsn: postgres://admin:<pw>@10.0.0.11:35432/seaweed_store  # required in cluster
      # --- TLS ---
      # tls: true                      # self-signed (or via --tls-cert/--tls-key BYO)
      # cert_file: …/cert.pem
      # key_file: …/key.pem
```

Edits to `listen`, ports, `root_user`, `root_password`, `image_tag`,
`data_dir`, `drives`, `filer_dsn`, or the TLS material take effect after the
next `pg addon install seaweedfs --name store --force`. In a cluster the force-recreate
must be run on **every** member (each host owns its own `pg.yaml` and container).

### List

```bash
pg addon list
```

```
Infra add-ons (seaweedfs):
  seaweedfs (name: store)
    Status:      running
    Cluster:     c1 (replication 001, 3 peers configured)
    Peers:       10.0.0.11:8400,10.0.0.12:8400,10.0.0.14:8400
    Leader:      10.0.0.11:8400.18400
    Filer store: postgres 10.0.0.11:35432/seaweed_store
    Listen:      127.0.0.1
    Ports:       master=8400 volume=8401 filer=8402 s3=8403
    S3 URL:      http://127.0.0.1:8403
    Data:        /root/.pgcli/addon/seaweedfs/store/data
    Access key:  SW<generated>
    Bucket:      store
    Image:       docker.io/chrislusf/seaweedfs:4.48_large_disk
    Container:   pgcli-seaweedfs-default-store
```

Cluster members gain the `Cluster` / `Peers` / `Leader` / `Filer store` lines;
`Leader:` is a live `/cluster/status` read (best-effort — omitted when the
member is not running, and printed as `(none yet — still electing / no quorum)`
while a still-assembling cluster has no leader). Single-node members show none
of those four lines. A multi-disk instance shows the `Drives` block (with one
`- /dataN <- host` line per drive) in place of the single `Data:` line — see
[Multi-disk](#multi-disk). With TLS on, a final `TLS: native S3 HTTPS on 8403
(cleartext listener off)` line is appended.
`pg addon list` never prints the secret key; reveal it deliberately with
`pg addon list --show-password` (appends a `Secret key:` line), or
`pg addon password seaweedfs --name store` (`--file <path>` writes it mode
0600). The `Filer store` line is `host:port/db` with the password always
hidden.

## Start and stop

```bash
pg addon start seaweedfs --name store
pg addon stop  seaweedfs --name store
```

`install` skips a still-present container (starting it if stopped); `start` only
starts an existing one — and re-renders `s3.json`/`filer.toml` first, so
hand-edits to the stored config take effect on restart.

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring instances up after a host reboot:

```bash
pg autostart enable --seaweedfs --name store
```

Autostart is **start-only** — the rendered files and the container already
exist. A cluster member that boots before its peers just waits for quorum like
any other cold start. SeaweedFS runs as root in its upstream image, so a
boot-time start needs no special host privileges beyond running podman itself.

## Remove

```bash
pg addon remove seaweedfs --name store               # container gone, data kept
pg addon remove seaweedfs --name store --clean-data  # also delete data dir + rendered files
```

The data directory **is the object storage** — losing it means losing every
volume in it — so `remove` keeps it by default. `--clean-data` deletes the data
dir, `s3.json`, and the `filer.toml` / TLS directory when present (each only
after a stat — absent optionals print nothing), and refuses to touch anything
that is a live mountpoint. **The filer metadata database is never dropped**, on
any host, ever: the removal prints

```
  [i] The filer metadata database (10.0.0.11:35432/seaweed_store) was KEPT — pgcli never drops databases; drop it yourself if the data is disposable.
```

so you know exactly what to `DROP DATABASE` if the data is truly done with
(every member's remove prints the same notice; the database outlives all of them).

## Logs

```bash
pg logs addon seaweedfs --name store      # last 50 lines
pg logs addon seaweedfs --name store -f   # follow
```

## Limitations

What v1 deliberately does **not** do:

- **Cluster membership changes** (add/remove/replacement nodes) are not
  orchestrated — `--peer` is a static full list; changing it means rebuilding
  the cluster.
- **EC erasure coding, tiering, cloud-sync (replication to other S3s), FUSE
  mounts, and the standalone IAM server** (dynamic `s3.accesskey.*` users) are
  not managed — all remain manual `pg weed` territory, and IAM in particular is
  the only way SeaweedFS itself shares credentials dynamically, out of scope
  here.
- **Credential rotation is a re-install** with `--force` on every member; pgcli
  never edits a live `s3.json` in place.
- **One bucket is seeded** (instance name); further buckets are plain `pg mc mb`
  away — they land in the same filer database automatically.
- The `filer.toml` DSN renders **`sslmode=disable`**; if your PostgreSQL demands
  TLS on that leg, pgcli's render does not fit it (BYO `--dsn` with TLS is not a
  v1 surface).

## Troubleshooting

- **`requires explicit shared credentials` on a `--cluster` install.** Expected
  by design — run the first node with an explicit `--root-user`/`--root-password`
  pair and carry the same values to every remaining host (the install summary
  prints the ready-made command with them filled in). Single-node installs may
  omit them and get auto-generated keys.
- **`requires a shared filer metadata database: pass --dsn …`.** A cluster
  without `--dsn` cannot keep one namespace across gateways. Pre-create the
  database and pass a **host-reachable** URI (published port / LAN address — a
  container-local `127.0.0.1:5432` is not).
- **`[!] no leader yet — normal while the cluster is still assembling`.** The
  health gate's quorum warning, not a failure: finish the remaining members'
  installs (or re-run this one once they are up) and it goes green.
- **`[!] bucket seeding incomplete … re-run this install command`.** Same
  posture one step later: the node reached a leader but its gateway was not
  serving yet. Re-run the identical install after the last member joins; `mb
  --ignore-existing` makes it a clean no-op if the bucket already landed.
- **Uploads 500 on a healthy-looking single node.** Historically the missing
  `-ip` signature (volumes register an auto-guessed address the data path then
  cannot dial); pgcli always passes `-ip`, so if you see it, check for a
  hand-edited argv or a very old binary.
- **`mc: … you already own it`.** A bare `mc mb` against an existing bucket;
  pgcli's own seed uses `--ignore-existing`, so see this only from manual `mc`
  use — add the flag.
- **Wrong-secret confusion: `The request signature we calculated does not
  match`.** The pair does not match the node's rendered `s3.json` — in a
  cluster, that means the nodes were installed with *different* credentials;
  re-run the install with the same pair + `--force` everywhere.
- **`pulling seaweedfs image … ` fails on install.** `docker.io/chrislusf/seaweedfs:4.48_large_disk`
  unreachable — check Docker Hub access or `podman pull` it first.
