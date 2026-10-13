---
title: "Garage"
description: "Run Garage (the Rust S3-compatible object store from Deuxfleurs) as a pgcli addon — single-node or multi-node cross-host cluster, plaintext HTTP, TLS terminated by fronting it with the nginx addon"
weight: 45
---

[Garage](https://garagehq.deuxfleurs.fr) is a Rust, AGPLv3 reimplementation of
S3-compatible object storage, built by
[Deuxfleurs](https://deuxfleurs.fr) for small self-hosted and geo-distributed
clusters. pgcli runs it as a **standalone, top-level addon** with the same CLI
surface as [`minio`](../minio/), [`silo`](../silo/) and
[`rustfs`](../rustfs/) (install, list, logs, autostart, password), but its
runtime model diverges from all three in four ways that shape this page:

1. **A rendered TOML config file, not argv/env.** Garage reads
   `/etc/garage.toml`. pgcli renders it under the instance directory and
   bind-mounts it read-only; the MinIO/silo/rustfs addons pass argv or env.
2. **Two data directories, not one — and no drive pool behind them.**
   `metadata_dir` (LMDB) and `data_dir` (blocks) are separate bind mounts; one
   `data_dir` per node, with multi-disk aggregation left to host LVM/RAID
   (MinIO and rustfs instead take a list of drives per node) — see [Capacity
   and sizing](#capacity-and-sizing).
3. **Four ports, from their own pool.** S3 API, node-to-node RPC, the `s3_web`
   static site, and the admin/metrics API — allocated together per instance
   from `garage_start_port` (default **3900**), a separate cursor from the
   shared minio/silo/rustfs pool.
4. **No native TLS — plaintext HTTP only.** pgBackRest's S3 repository forces
   HTTPS, so a Garage store meant to receive `archive-push` must be **fronted by
   the [nginx addon](../nginx/)** — see [TLS by fronting with
   nginx](#tls-by-fronting-with-nginx).

Like rustfs, Garage is a good successor route now that the open-source MinIO
repository is archived. pgcli runs it **single-node** by default
(`replication_factor = 1`, `garage server --single-node`) and, with a handful of
cluster flags, as a **multi-node cross-host cluster** — see [Multi-node
cluster](#multi-node-cluster).

> **Platform support:** tested on Linux (host networking). The macOS bridge path
> publishes all four ports and is code-complete, following the same shape as the
> other object stores, but has not been exercised on a Mac yet.

## How It Works

One container per instance. pgcli pulls the **upstream**
`docker.io/dxflrs/garage` image directly (pull-only) — unlike rustfs there is no
wrapper image to build, because Garage runs as root inside its own container and
ships no uid-drop, so there is no host-side ownership dance to absorb.

The data lives in two bind-mounted host directories that outlive the container:

- `<base-dir>/addon/garage/<name>/data` → `/var/lib/garage/data` (blocks),
- `<base-dir>/addon/garage/<name>/meta` → `/var/lib/garage/meta` (LMDB),

plus the rendered `<base-dir>/addon/garage/<name>/garage.toml` →
`/etc/garage.toml`, mounted **read-only** (Garage never rewrites its own config).

Credentials are handled like the other stores, but Garage's are an **access key /
secret key pair** rather than a root user / password:

- **`root_user`** is the access key id, **generated on first install** with
  Garage's own `GK`-prefixed shape (`GK` + 32 hex chars, what
  `openssl rand -hex 16` yields in the quick-start), or supplied via
  `--root-user`.
- **`root_password`** is the secret key, **generated on first install**
  (64 hex chars), or supplied via `--root-password`.
- A third secret, `rpc_secret` (the cluster's RPC signing key), is generated the
  same way and stored alongside. On a single-node instance it never surfaces in
  the CLI; on a cluster **bootstrap** it is printed once — together with the join
  command — for the operator to carry to the next host (pgcli never executes
  anything remotely).

All three land in `pg.yaml` (`addons.garage.<name>.*`) so a recreate is stable.
On a **single-node** first boot the container is passed `--single-node
--default-bucket` plus the `GARAGE_DEFAULT_ACCESS_KEY` /
`GARAGE_DEFAULT_SECRET_KEY` / `GARAGE_DEFAULT_BUCKET` env, so Garage itself
creates the access key and one bucket (named after the instance, owned by that
key) — no post-install exec needed. The key and bucket live in `metadata_dir`, so
they survive container recreation. A **cluster** member starts bare (no
`--single-node`/`--default-bucket`, no `GARAGE_DEFAULT_*` env); its key and bucket
are seeded explicitly by the install that completes quorum — see [Multi-node
cluster](#multi-node-cluster).

## Install

```bash
# default instance name "garage", four ports from the garage pool, loopback bind
pg addon install garage

# a named instance with an explicit data directory
pg addon install garage --name store --data-dir /srv/garage

# a fixed S3 API port (RPC/web/admin take the next three free ports)
pg addon install garage --name store --api-port 3900

# expose the store on the network instead of loopback only
pg addon install garage --name store --listen 0.0.0.0

# supply your own access key / secret key instead of generating them
#   Garage's own limits apply: access key >= 8 chars, secret key >= 16 chars
pg addon install garage --name store --root-user GK<...> --root-password <secret>

# join a cross-host cluster (see Multi-node cluster below)
pg addon install garage --name store --cluster c1 --advertise-host 10.241.20.147 \
  --zone z1 --capacity 15 --replication-factor 3 \
  [--peer <id@addr> --rpc-secret <64hex>]   # a joiner carries both
```

> **Supplying your own keys:** these two flags only take effect on a
> **single-node** install — a cluster member never reads them, because its key
> comes from `garage key create` at the quorum-completing install. The values go
> to Garage verbatim and **Garage** is what enforces them: an access key id
> shorter than 8 characters, or a secret key shorter than 16, is rejected when
> the container first boots (`Invalid default access key: … should be at least
> N characters long`) — and because the container starts detached, `install`
> itself reports success while the node crash-loops behind `--restart
> unless-stopped`. Check `podman logs` (or just let `pg mc alias set` fail) if a
> custom-key install comes up unable to serve. The generated shapes
> (`GK` + 32 hex / 64 hex) clear both bounds with room; `mc` additionally
> requires an access key of `[A-Za-z0-9]`, so stay alphanumeric.

The output reports endpoints and the seeded credentials:

```
✓ garage installed: "store"
  Container:     pgcli-garage-default-store
  Image:         docker.io/dxflrs/garage:v2.4.1
  Data:          ~/pg/addon/garage/store/data
  Metadata:      ~/pg/addon/garage/store/meta
  Config:        ~/pg/addon/garage/store/garage.toml
  S3 API:        http://127.0.0.1:3900
  RPC/Web/Admin: 3901/3902/3903

  Access key:    GK<generated>
  Secret key:    <generated>
  Bucket:        store (created on first boot, owned by the access key)

  NOTE: Garage serves plaintext HTTP only. pgBackRest requires HTTPS — front it
        with the nginx addon via --conf-file (template --upstream mode strips
        the port from Host and breaks S3 signing; see docs/addon/garage):
          backend 127.0.0.1:3900, listen ssl, proxy_set_header Host $http_host
  Try it:        pg mc alias set store http://127.0.0.1:3900 GK<...> <secret> && pg mc ls store
```

Re-running install against a **live** instance is a no-op: the container is not
recreated (a stopped one is simply started, with a notice), the flags are merged
into the stored config, and the existing credentials are kept. Pass `--force` to
recreate the container so changed ports, listen address, or credentials take
effect — without losing the two data directories.

> **Bind address:** the default `127.0.0.1` keeps the store local. `--listen
> 0.0.0.0` (or the `listen` key in `pg.yaml`) exposes it on the network. Because
> Garage serves **plaintext HTTP**, anyone who can reach the port can sniff the
> credentials in transit — only bind it publicly behind a firewall, or in
> practice, in front of the [nginx TLS front](#tls-by-fronting-with-nginx).

> **`--tls` is rejected for garage.** Garage has no TLS listener to enable;
> passing `--tls`/`--tls-cert`/`--tls-key` errors out and points you at the nginx
> front rather than silently ignoring the flag.

### TLS by fronting with nginx

Because pgcli does not couple the two addons, terminating TLS in front of a
Garage store is an explicit two-step: install garage, then front it with nginx.

> **Use `--conf-file`, not the `--upstream` template.** S3 requests are signed
> with SigV4 over the `Host` header **including the port**. The template mode
> forwards `Host $host`, which nginx renders *without* the port — Garage then
> re-derives a different canonical request and every write fails with `Invalid
> signature` (verified: `pg mc mb` through a template front is refused; through
> the `--conf-file` front below it round-trips). The custom config also lets you
> set `client_max_body_size 0;` (the 1 MiB nginx default otherwise rejects any
> real object upload) and long proxy timeouts.

```bash
pg addon install garage --name store

# front.conf — path-style S3 over TLS, host+port preserved for signing:
cat > front.conf <<'EOF'
worker_processes auto;
pid /tmp/nginx.pid;
events { worker_connections 1024; }
http {
    access_log /var/log/nginx/access.log;
    error_log  /var/log/nginx/error.log;
    server {
        listen 0.0.0.0:8081 ssl;          # 0.0.0.0 so remote clients can reach it; the HTTPS port pgcli assigned
        ssl_certificate     /etc/nginx/certs/public.crt;
        ssl_certificate_key /etc/nginx/certs/private.key;
        client_max_body_size 0;          # unlimited PUT
        proxy_read_timeout   3600s;
        proxy_send_timeout   3600s;
        location / {
            proxy_pass http://127.0.0.1:3900;   # the garage S3 API port
            proxy_set_header Host $http_host;   # keep the port — SigV4
            proxy_request_buffering off;
            proxy_http_version 1.1;
        }
    }
}
EOF

pg addon install nginx --name gtls --tls --conf-file front.conf --https-port 8081
```

`--tls` makes pgcli generate the self-signed CA + leaf (`public.crt` /
`private.key`, mounted at `/etc/nginx/certs`) and stands up the HTTPS listener.
Point S3 clients at `https://<host>:8081` and hand consumers the front's CA at
`<base-dir>/addon/nginx/gtls/tls/ca.crt`.

> **Bind the front `0.0.0.0`, leave Garage on loopback.** The `listen` line above
> binds all interfaces so a remote host (e.g. a Patroni node pushing archives)
> can reach the TLS endpoint — TLS is terminated *at nginx*, so this is safe in
> a way that exposing Garage's plaintext port is not. The `proxy_pass` still
> targets `127.0.0.1:<garage-api-port>`: the nginx container runs on the host
> network, so the loopback Garage bind is reachable from it, and the plaintext
> leg never leaves the machine. For a front that should stay local, bind
> `listen 127.0.0.1:<port> ssl;` instead.

> **Why the `listen` and `proxy_pass` lines are pinned by hand.** In `--conf-file`
> mode pgcli mounts your config verbatim, so the ports in it must match the ones
> the two addons were assigned — read the garage `S3 API:` port from its install
> output and the nginx `HTTPS:` port from `pg addon list`. (The `$http_host` and
> `client_max_body_size` lines are the two that a template front gets wrong for
> S3.)

Getting that CA onto a remote Patroni host without a manual scp is what
`pg backup fetch-ca` is for — it dials the TLS front and pulls the root straight
out of the handshake:

```bash
pg backup fetch-ca <front-host>:<https-port>
#   [OK] CA fetched from <front-host>:<https-port>
#        saved:    ~/.pgcli/backup/repo-ca/ca-<front-host>-<https-port>.crt
#        SHA-256:  c0f0…fe2e
pg backup setup --s3-ca-file ~/.pgcli/backup/repo-ca/ca-<front-host>-<https-port>.crt
```

The fetch is trust-on-first-use — compare the printed SHA-256 against the front
host's `sha256sum ~/.pgcli/addon/nginx/gtls/tls/ca.crt` before trusting it.

#### Fronting a cluster: one node, or the local nodes

A cluster does **not** need all members behind the front. S3 replication is the
data layer's job: a request to *any* member is complete, and that member
replicates it to `replication_factor` nodes itself. So the single-node front
above works for a cluster too — point `proxy_pass` at one member's S3 port.

Front more members only to remove that member from the **access** single point of
failure. Keep the upstream to the nodes reachable on the front's host (typically
the local site — see [Geo-distributed
topology](#geo-distributed-topology)), and let each site run its own front:

```nginx
http {
    upstream garage_s3 {
        server 10.241.20.147:3904;   # member A, this site
        server 10.241.20.148:3900;   # member B, this site
        # no remote member: it stays a data replica, off the request path
    }
    server {
        listen 0.0.0.0:8081 ssl;
        ssl_certificate     /etc/nginx/certs/public.crt;
        ssl_certificate_key /etc/nginx/certs/private.key;
        client_max_body_size 0;
        proxy_read_timeout   3600s;
        proxy_send_timeout   3600s;
        location / {
            proxy_pass http://garage_s3;
            proxy_set_header Host $http_host;   # keep the port — SigV4
            proxy_request_buffering off;
            proxy_http_version 1.1;
        }
    }
}
```

Two constraints this raises that the single-backend example does not:

- **The members' S3 API must leave loopback.** Cluster mode only widens the RPC
  bind automatically; `--listen` still defaults to `127.0.0.1`, so a remote
  front cannot reach another host's member. Install the fronted members with
  `--listen 0.0.0.0` (behind a firewall — the front is what speaks TLS), or
  accept that only same-host members are valid upstream entries.
- **Ports differ per host** — same gotcha as the RPC port: each member's S3 API
  port comes from its own install output / `pg addon list` (`S3 URL:`), not
  necessarily 3900.

This stays a `--conf-file` config: the `--upstream` template's fault for S3 is
unchanged — `Host $host` without the port breaks every SigV4 write, and there is
no `client_max_body_size 0` (see the note above). With several upstream servers
the default round-robin is fine; add `max_fails`/`fail_timeout` to taste.
(Verified on a 3-node cluster: `pg mc` round-trips through such a front, and
stopping one upstream member leaves the front serving through the other.)

### Wiring pgBackRest to a fronted Garage store

Once you have a TLS endpoint, a Garage-backed S3 repository is the same as a
MinIO one — the S3 contract is identical (path-style, `repo1-s3-uri-style=path`):

```bash
pg backup setup \
  --s3-endpoint <front-host>:<https-port> \
  --s3-bucket store \
  --s3-access-key GK<...> \
  --s3-secret-key <secret> \
  --s3-ca-file <front>/addon/nginx/gtls/tls/ca.crt
```

See [Backup → S3 object storage
repository](../../backup/#s3-object-storage-repository) for the full flow.
`pg addon password garage --name store` prints the secret key for scripting.

## Multi-node cluster

Garage's real strength is the cross-host cluster: nodes in different zones,
CRDT-replicated layout, quorum reads/writes. pgcli drives this with the **same
`pg addon install garage`** command — no new entry point — adding a handful of
cluster flags. A cluster is assembled **one host at a time** (the Patroni model):
pgcli never executes anything on a remote host. The first host *bootstraps*,
prints a ready-made join command carrying the shared `rpc_secret` and this node's
`id@addr`, and you run that command (edited for the next host) yourself.

### Roles

`garageInstallRole` picks the role from the flags you pass:

| Role | When | What install does |
|------|------|-------------------|
| **single** | no `--cluster` | the v1 path, byte-for-byte (`--single-node --default-bucket`) |
| **bootstrap** | `--cluster` set, no `--peer` | generates `rpc_secret`, starts a bare `garage server`, stages its own layout role, prints the join command |
| **join** | `--cluster` + `--peer <id@addr>` | carries `--rpc-secret`, connects to that peer, stages its own role, applies when it completes quorum |

### Three-host walkthrough

**Host 1 — bootstrap.** Give it a `--zone`, a `--capacity`, and the eventual
`--replication-factor`. Because only one node exists so far, Garage refuses to
*apply* the layout (it needs `replication_factor` nodes with positive capacity),
and pgcli turns that into a warning, not an error — the staged role is
cluster-replicated and survives until the last host applies it:

```bash
# on 10.241.20.147
pg addon install garage --name store --cluster c1 \
  --advertise-host 10.241.20.147 --zone z1 --capacity 15 --replication-factor 3
```

The summary prints the secret and a copy-paste join template:

```
  Cluster:       c1 (role: bootstrapping cluster, replication factor 3)
  Advertise:     10.241.20.147:3905
  Zone/Capacity: z1 / 15 GiB
  Node ID:       1c0597cc…200aeb@10.241.20.147:3905
  [i] layout staged but not applied yet — Garage needs replication-factor assigned nodes to commit (later joins will).

  To add ANOTHER member, run on the next host (carrying --rpc-secret yourself;
  pgcli never executes remotely). --advertise-host is that host's own peer-
  reachable IP; pick a --zone per failure domain and keep --replication-factor:
    pg addon install garage --name store --cluster c1 \
      --advertise-host <next-host-ip> --rpc-secret <64hex> \
      --peer 1c0597cc…200aeb@10.241.20.147:3905 --zone <zone> --capacity <GiB> --replication-factor 3
```

**Host 2 — join.** Copy the printed command, change `--advertise-host` to this
host's IP and `--zone` to a distinct zone:

```bash
# on 10.241.20.148
pg addon install garage --name store --cluster c1 \
  --advertise-host 10.241.20.148 --rpc-secret <64hex> \
  --peer 1c0597cc…200aeb@10.241.20.147:3905 --zone z2 --capacity 15 --replication-factor 3
```

Two nodes are assigned now, still fewer than `rf = 3`, so it warns again. Peering
is **transitive**: host 2 only needs one `--peer` (host 1); host 3 can point at
either existing node.

**Host 3 — the install that completes quorum.** The third host sees the full
staged role set (assign is cluster-replicated), so its `layout apply` commits
version 1 — and **this** is the install that seeds the access key and bucket:

```bash
# on 10.241.20.153
pg addon install garage --name store --cluster c1 \
  --advertise-host 10.241.20.153 --rpc-secret <64hex> \
  --peer 5b370435…1c1c9187@10.241.20.148:3901 --zone z3 --capacity 15 --replication-factor 3
```

```
  Access key:    GKc57063…6147b0
  Secret key:    <printed once, here>
  Bucket:        store (cluster key + bucket seeded on this install)
```

Every install runs the seeding step idempotently, so the **last** host to join
is naturally the one that seeds — no back-fill on host 1 needed. The key name is
the instance name (`store`), the bucket too, and the key is granted read/write/
owner on it.

### Seeding and the secret

In a cluster the single-node `--default-bucket` / `GARAGE_DEFAULT_*` fast path is
unavailable, so pgcli seeds explicitly with `key create` + `bucket create` +
`bucket allow`. Two consequences:

- **`garage key create` prints the secret exactly once** and is *not* idempotent
  (a re-run mints a *second* key with the same name). pgcli checks `key list`
  first and only creates when absent. Whichever host performs the seeding writes
  the access/secret pair into **its own** `pg.yaml`; the other hosts hold only the
  public key id.
- **`pg addon password garage` on a non-seeding host** has no secret to print and
  says so — the secret lives in the `pg.yaml` of the host whose install completed
  quorum. Reveal it there, or run `garage key create` yourself to mint a fresh
  key on any node.

### Version conflicts and re-runs

`layout apply --version N` requires exactly `current + 1` — Garage's optimistic
lock. If two hosts install concurrently, one apply fails; pgcli reports it as
"layout changed concurrently — re-run the same install command". `assign` is
idempotent and re-staging is harmless, so the retry just re-reads the version and
applies. A re-run against a live member is otherwise a no-op (container already
running → `skipping creation`; peering already done → treated as success).

### Zone and replication-factor advice

- `--replication-factor` must be ≤ the number of nodes you will join; a cluster
  cannot seed until `rf` nodes hold a positive-capacity role.
- Give each host a **distinct `--zone`** to survive whole-failure-domain loss;
  Garage keeps replica copies in different zones when it can.
- `--capacity` is GiB and is what Garage carves out for blocks; a node with
  capacity 0 is treated as not-yet-data-bearing.
- `--advertise-host` is required for every member — it becomes `rpc_public_addr`,
  the address peers dial. Without it a cluster member is unreachable. (A cluster
  member whose `--listen` is loopback still widens its **RPC** bind to
  `0.0.0.0`; only the S3/web/admin ports stay on loopback.)

### Capacity and sizing

**Garage has no drive pool.** MinIO and rustfs both take a list of drives: a
node declares N block devices, stripes data across them, and survives individual
drive loss on its own. Garage's unit of redundancy is the **node**, not the
drive — each member gets exactly one `data_dir` (plus a separate LMDB
`metadata_dir`), and every block of a replicated object is placed on a
*different node* (preferring a different *zone*). If you want multi-disk
aggregation, do it below Garage: LVM or host RAID presents one big filesystem
and Garage's `data_dir` sits on it. Disk failure is thus the host layer's
problem; node failure is Garage's. **MinIO/rustfs manage drives; Garage manages
nodes.**

`--capacity` is the number you hand that model: GiB this node may store
(`layout assign -c <N>GiB`). Two properties worth internalising:

- It is a **placement weight, not a quota**. Garage never refuses a write
  because capacity was reached — the real free space is what `garage status`
  reports as `DataAvail`. Setting `--capacity 100` on a 20 GiB filesystem
  would just let Garage fill (and then break) the disk. Keep it at (or under)
  the usable size of the backing filesystem.
- Changing it is a **layout change**: re-run the same `pg addon install` line
  with the new `--capacity` (assign is idempotent, the apply bumps the layout
  version), and Garage rebalances blocks onto the enlarged space. Growing the
  backing filesystem first is what actually buys room.

**Usable capacity** is the sum of all node capacities divided by the
replication factor — every object is kept in `rf` copies:

| Nodes | Per node (`--capacity`) | rf | Raw total | Usable |
|-------|------------------------|----|-----------|--------|
| 3 | 15 GiB | 3 | 45 GiB | **15 GiB** |
| 4 | 15 GiB | 3 | 60 GiB | **20 GiB** |
| 5 | 15 GiB | 3 | 75 GiB | **25 GiB** |

General rule: `usable = Σ capacity ÷ rf`. The same math is why `rf = 2` halves
the storage overhead at the cost of surviving only one node loss; `rf = 3`
costs 3× but a write commits once **2 of 3** copies are acked, so a 3-node
`rf = 3` cluster tolerates 1 node down for writes, a 5-node one tolerates
2 (any 2 of the remaining members can ack). Adding a 4th or 5th node therefore
buys both fault tolerance *and* usable space, unlike simply mirroring.

### Quorum behaviour on a stopped node

Stop any one member and the other two keep serving reads and writes (2/3 quorum
with `rf = 3`):

```bash
# on host 3
pg addon stop garage --name store
# on host 1 — still reads and writes
pg mc cat c1/store/v.txt
```

`pg addon start` rejoins the node under the **same** `Node ID` (the keypair lives
in `metadata_dir`, not the container), and `garage status` shows it healthy
again. Start/stop/remove/autostart/logs always manage only the **local**
container — the Patroni semantic; there is no remote orchestration.

### Geo-distributed topology: 2 local + 1 remote

The production shape Garage was designed for: two members in your own site (one
per rack/zone, `z1` + `z2`) and a third member in a **remote site** (`z3`). It
fits the write-latency maths well: an `rf = 3` write commits on a **2/3
quorum**, so the two local replicas acknowledging is enough — the remote copy
follows asynchronously and a slow (or briefly down) WAN link never slows a
write. The nginx TLS front points at the two local members only (see [Fronting a
cluster](#fronting-a-cluster-one-node-or-the-local-nodes)); the remote member
carries data, not request traffic, and typically runs its own front for local
read repair / disaster use.

Two honest caveats about what the remote member **is not**:

- **It is a replica, not a backup.** CRDT replication syncs everything,
  *including deletions* — a mistaken `mc rb --force` or a pgBackRest
  `expire` propagates to the remote site. For delete-immune copies you need
  Garage's own bucket replication between *separate* clusters (a Garage CLI
  feature, manual here: `garage bucket replication`); the 2+1 cluster gives you
  node- and site-level survivability plus a manual rescue copy, nothing more.
- **A whole-site outage does not fail over automatically.** If both local members
  die together, the lone remote node is below quorum (1 < 2) and the cluster is
  unwritable. The data is intact on it, and Garage documents a manual
  recovery (re-committing a reduced layout from surviving nodes), but pgcli does
  not orchestrate that — it is a `garage layout` operation for an operator who
  has decided the site is really gone.

So the accurate framing is: **2 local + 1 remote = HA with site-failure rescue**,
writes stay local-speed, and "backup" in the delete-immune sense remains
pgBackRest's (or replication's) job.

### Getting the cluster's AK/SK

The seeded access key is cluster-wide (any member validates it), but its
**secret exists on exactly one host** — whichever install completed quorum
printed it once and stored it in that host's `pg.yaml`:

```bash
# on the seeding host only:
pg addon password garage --name store              # print the secret key
pg addon password garage --name store --file ./ak  # mode 0600, for shipping
pg addon list --show-password                      # access key + secret together
```

- Any host can show the **public id** (`pg addon list` → `Access key:`); on
  non-seeding hosts `pg addon password` errors and names the host that holds the
  secret — the message is precise, the secret is simply not there to give.
- Lost secret, or want credentials of your own? Mint a fresh key on **any**
  member and grant it the bucket — keys are CRDT state, so the new pair works
  cluster-wide immediately:

  ```bash
  pg garage --name store key create app-key
  pg garage --name store bucket allow store \
    --key GK<new-id> --read --write --owner
  ```

  This is the recommended day-2 practice: applications and pgBackRest get their
  own keys (independent rotation/revocation), the seeded one stays the break-glass
  admin credential. `pg garage` is the general passthrough for Garage's own
  control CLI — see [Administering via `pg garage`](#administering-via-pg-garage).

> **v2.0 does not** remove a node from the layout (`layout remove` + data
> migration), revert a committed layout, or orchestrate multi-zone failover.
> Those stay manual garage CLI operations — run them via
> [`pg garage`](#administering-via-pg-garage).

## Using the mc Client

`pg mc` runs MinIO's own `mc` client from a throwaway container — no local
install, no manual `podman run` — and speaks Garage's S3 API like any other
store. Use the **access key** in place of a user and the **secret key** in place
of a password:

```bash
# pick the URL for your platform — see "Endpoints by platform" below:
pg mc alias set store http://127.0.0.1:3900 GK<...> <secret>       # Linux, local addon
pg mc alias set store http://host.containers.internal:3900 GK<...> <secret>  # macOS, local addon
pg mc alias set store http://10.0.5.7:3900 GK<...> <secret>        # either, remote store
pg mc mb store/backups
pg mc ls store                       # the seeded "store" bucket is already here
pg mc cp ./dump.pglz store/backups/
```

Aliases persist on the host at `~/.mc/config.json` — mc's own default path, the
same on Linux and macOS — so `alias set` once and every later `pg mc` invocation
(and a native `mc` on Linux) sees the same aliases. `pg mc alias list` reads them
back and `pg mc alias remove` drops one. `alias set` validates the credentials
against the endpoint before writing, so a wrong key leaves `~/.mc/config.json`
untouched. Under the hood `pg mc` mounts your `~/.mc` into the container and runs
`ghcr.io/mars-base/pgcli/pgcli-mc` (the static upstream binary on `scratch`);
nothing else about it is magic.

Any `mc` flag that `pg`'s own flag parser would reject goes after `--`, exactly
like the other stores — `--recursive`, `--force`, `--all`, `--json` and so on:

```bash
pg mc cp ./dir store/backups/ -- --recursive
pg mc rb store/scratch -- --force
```

> **Endpoints by platform:** on Linux `pg mc` runs on the host network, so a
> `127.0.0.1` alias reaches a local addon instance directly. On macOS the
> container sits on the bridge network, where `127.0.0.1` is the container's own
> loopback — point an alias at a local Mac addon via
> `http://GK<...>:<secret>@host.containers.internal:3900`, or at a remote store
> via its routable address. To go over TLS, set the alias to the nginx front's
> `https://` endpoint instead (and `-- --insecure` for a self-signed leaf).

### Common commands

| Command | Purpose |
|-------|---------|
| `pg mc ls store` / `pg mc ls store/backups` | list buckets / objects |
| `pg mc mb store/backups` | create a bucket |
| `pg mc cp ./file store/backups/` | upload (for a tree: `pg mc mirror ./dir store/backups/`, or add `-- --recursive` to `cp`) |
| `pg mc cp store/backups/file ./` | download |
| `pg mc find store -- --name '*.pglz'` | search objects by name |
| `pg mc du store` | size per bucket |
| `pg mc rm store/backups/file` | delete one object |
| `pg mc rm store/prefix -- --recursive` | delete many objects |
| `pg mc rb store/bucket -- --force` | remove a bucket, objects and all |

The same access/secret key pair works for any S3 SDK — including pgBackRest
against a `repo1-type=s3` repository (through the nginx TLS front).

## Administering via `pg garage`

`pg garage` forwards any Garage control-CLI subcommand into the instance's
running container — the replacement for hand-written
`podman exec pgcli-garage-<ns>-<name> /garage …`. `--name` selects the instance
(default: the first by name); everything after the subcommand word reaches
`/garage` verbatim, so Garage's own flags need no `--` separator. It works the
same on a single-node instance and on any cluster member; on a cluster, ask any
**running** member — status, layout and keys are cluster-replicated CRDT state.

### Health and status

```bash
pg garage --name store health            # exit code 0 iff the cluster is healthy
pg garage --name store status            # per node: zone, capacity, DataAvail, version
pg garage --name store layout show       # committed roles + staged changes + layout version
pg garage --name store stats             # per-partition replication detail
curl -s http://127.0.0.1:3903/health     # the admin-port liveness line
```

`health` is the monitoring-friendly one: it prints a short summary (known /
connected / storage nodes, partition quorum) and **sets the exit code**, so a
cron or check script can use `pg garage --name store health > /dev/null`
directly. `status` is the wide table — watch the `DataAvail` column, that is the
real free space (see [Capacity and sizing](#capacity-and-sizing)).

### Keys and buckets

```bash
pg garage --name store key list                    # who can access the store
pg garage --name store key create app-key          # secret printed once
pg garage --name store bucket allow store --key GK... --read --write
pg garage --name store bucket deny  store --key GK... --write
pg garage --name store bucket list
pg garage --name store bucket info store           # size, object count, per-key grants
pg garage --name store key delete GK...            # revoke an app key
```

Mint-then-allow is the day-2 credential pattern: applications get their own key,
the seeded one stays break-glass (see
[Getting the cluster's AK/SK](#getting-the-clusters-aksk)).

### Layout changes (cluster)

Adding a node is `pg addon install` (it assigns and applies automatically); the
raw CLI is for auditing and the manual edge cases pgcli deliberately does not
orchestrate:

```bash
pg garage --name store layout history              # which version when, by which node
pg garage --name store layout show                 # any STAGED ROLE CHANGES pending?
pg garage --name store node connect <id@addr>      # (re-)peer a node
```

`layout remove`, `layout revert` and multi-zone failover remain manual garage
operations — the v2.0 limits blockquote at the end of
[Getting the cluster's AK/SK](#getting-the-clusters-aksk) lists them.

### Maintenance

```bash
pg garage --name store bucket cleanup-incomplete-uploads   # abort stale multipart uploads
pg garage --name store key delete-expired                  # sweep expired keys
pg garage --name store bucket inspect-object store/key    # which nodes hold this object's blocks
```

## Ports

Each instance takes **four consecutive ports** from Garage's own pool, starting
at `garage_start_port` (default **3900**): the S3 API first, then RPC, `s3_web`,
and admin/metrics. The pool is **separate from the shared minio/silo/rustfs
pool** (that one starts at `minio_start_port`, default 9000), so a garage, a
minio, and a rustfs instance never collide:

```bash
pg addon install garage --name store    # 3900 / 3901 / 3902 / 3903
pg addon install garage --name lake     # 3904 / 3905 / 3906 / 3907
pg addon install minio  --name archive  # 9000 / 9001  (shared pool)
```

Only the S3 API port is a knob (`--api-port`); the other three are assigned the
next free ports after it. The RPC port is node-to-node traffic and, for a
single-node instance, stays on loopback; the admin/metrics port (`3903`) serves
Garage's status API. A **cluster** member's RPC port is what peers dial
(`rpc_public_addr`), so pgcli widens its RPC bind to `0.0.0.0` even when
`--listen` is loopback — the channel is signed with `rpc_secret`. Note the port
number also rides inside `--advertise-host`/`--peer`, so on a host that already
runs another garage instance the cluster's RPC may land on a different port than
3901 (e.g. `10.241.20.147:3905`).

### No web console — administering via CLI and the admin API

Unlike MinIO (which ships a bundled web Console on a second port), **Garage has
no built-in web UI**. Its management surface is three things:

- **The `garage` CLI, via `pg garage`** — the practical way to inspect and change
  state. `pg garage` runs Garage's own control CLI inside the instance's running
  container (it talks to the local RPC port), so you never `podman exec` by hand:

  ```bash
  pg garage --name store status         # nodes, zones, capacity, DataAvail
  pg garage --name store layout show    # committed layout + version
  pg garage --name store bucket list
  pg garage --name store key list
  ```

  Everything after `--name` is forwarded to `/garage` verbatim, so Garage's own
  flags (`--read --write`, `-z`, `-c`) need no `--` separator — and they go
  **after** the subcommand word, since pgcli only reads its own flags before it.
  With a single instance installed, `--name` can be dropped; with several, omitting
  it targets the first by name (announced on stderr). This needs a **running**
  member — unlike `pg mc`/`pg etcdctl`, which spin up a throwaway client container,
  `pg garage` execs the node's own CLI. See
  [Administering via `pg garage`](#administering-via-pg-garage) for the full
  operations menu.

- **The admin API** on the admin port (`3903`) — a JSON API. From Garage v2 the
  endpoints are `/v2/<Operation>` (e.g. `/v2/GetClusterHealth`) and **require a
  Bearer token** in the `Authorization` header; a bare `curl` gets `403
  AccessDenied`. Only a few endpoints are open without a token:

  ```bash
  curl -s http://127.0.0.1:3903/health          # "Garage is fully operational"
  curl -s http://127.0.0.1:3903/metrics         # Prometheus exposition
  curl -s http://127.0.0.1:3903/v2/GetClusterHealth \
    -H "Authorization: Bearer <admin-token>"   # needs a token
  ```

- **`s3_web`** on the web port (`3902`) — this is **not** a console. It only
  serves static-website content for buckets that have website hosting enabled
  (via per-bucket vhost); its root returns `404`.

Community web UIs exist (e.g. dashboards built on the admin API), but they are
not part of Garage and not managed by pgcli; wiring one up means minting an
admin API token and pointing the third-party app at the admin port. The `web_port`
(`3902`) that pgcli assigns is reserved for bucket website hosting, so a third-
party console should talk to `3903` (admin API) or use [`pg garage`](#administering-via-pg-garage)
for the CLI — not to `3902`.

## Configuration

Instances live under the top-level `addons.garage` map in `pg.yaml`:

```yaml
namespace: default
garage_start_port: 3900    # garage's own pool (API/RPC/web/admin, four each)
addons:
  garage:
    store:
      container_name: pgcli-garage-default-store
      name: store
      image_tag: docker.io/dxflrs/garage:v2.4.1
      # data_dir: /srv/garage      # omit for <base-dir>/addon/garage/store/data
      #                             # (meta dir and garage.toml live beside it)
      # config_file: /etc/garage.toml   # BYO config: mounted verbatim, never rewritten
      listen: 127.0.0.1
      api_port: 3900
      rpc_port: 3901
      web_port: 3902
      admin_port: 3903
      root_user: GK<generated>        # access key id, written on first install
      root_password: <generated>      # secret key, written on first install
      rpc_secret: <generated>         # cluster RPC signing key, written on first install
      region: garage                  # s3_region
      replication_factor: 1           # single-node must be 1; a cluster sets it >1
      autostart: false                # pg autostart enable --garage --name store
      # --- multi-node cluster keys (omit for single-node) ---
      # cluster: c1                   # non-empty ⇒ cluster mode; groups members
      # advertise_host: 10.241.20.147 # rpc_public_addr host; required in cluster
      # zone: z1                      # layout assign -z (failure domain)
      # capacity: 15                  # layout assign -c, GiB; 0 ⇒ no auto-assign
      # peers:                        # known id@addr, each `garage node connect`ed
      #   - 1c0597cc…200aeb@10.241.20.147:3905
      # node_id: 1c0597cc…200aeb@10.241.20.147:3905  # read back after first boot
```

`replication_factor` defaults to 1. Setting it `> 1` **without** a `cluster` is
rejected at install — a lone node cannot meet a multi-node quorum. The cluster
keys above are written by the cluster install path; you can also set them by hand
and re-install. Zone and capacity are layout state, not TOML, so they appear only
here.

Edits to `listen`, ports, `root_user`, `root_password`, `image_tag`, `data_dir`,
or `config_file` take effect after the next `pg addon install garage --name
store --force`.

### List

```bash
pg addon list
```

```
Infra add-ons (garage):
  garage (name: store)
    Status:      running
    Cluster:     c1 (rf 3)
    Advertise:   10.241.20.147:3905
    Zone/Cap:    z1 / 15 GiB
    Node ID:     1c0597cc…200aeb@10.241.20.147:3905
    Nodes:       3 assigned (healthy)
    Listen:      127.0.0.1
    API port:    3904
    RPC/Web/Admin: 3905/3906/3907
    S3 URL:      http://127.0.0.1:3904
    Data:        ~/pg/addon/garage/store/data
    Metadata:    ~/pg/addon/garage/store/meta
    Access key:  GK<generated>
    Bucket:      store
    Image:       docker.io/dxflrs/garage:v2.4.1
    Container:   pgcli-garage-default-store
    NOTE:        plaintext HTTP only — front with the nginx addon for TLS
```

Cluster members gain the `Cluster` / `Advertise` / `Zone/Cap` / `Node ID` /
`Nodes` lines; `Nodes:` is a live `garage status` read (best-effort — it is
omitted when the member is unreachable). On a cluster the `Access key` on the
hosts that did **not** perform the seeding is the public id only (no secret); see
[Seeding and the secret](#seeding-and-the-secret).

`pg addon list` never prints the secret key. Reveal it deliberately with
`pg addon list --show-password` (appends a `Secret key:` line), or print just the
secret for scripting: `pg addon password garage --name store` (the secret key,
paired with the `Access key:` above; `--file <path>` writes it mode 0600).

## Start and stop

```bash
pg addon start garage --name store
pg addon stop  garage --name store
```

`install` skips a still-present container (starting it if stopped); `start` only
starts an existing one (and self-heals an improper state by recreating it from
the config, re-rendering `garage.toml`).

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring instances up after a host reboot:

```bash
pg autostart enable --garage --name store
```

garage autostart re-renders `garage.toml` and starts the existing container.
Garage runs as root in the upstream image, so a boot-time start needs no special
host privileges beyond running podman itself. Boot is **start-only**; garage is
independent of the PostgreSQL stack, so it is started alongside the other object
stores.

## Remove

```bash
pg addon remove garage --name store            # container gone, data kept
pg addon remove garage --name store --clean-data   # also delete the two dirs + config
```

The data directories **are the object storage** — losing them means losing every
bucket in them — so `remove` keeps them by default. `--clean-data` deletes the
data dir, the meta dir, and the pgcli-rendered `garage.toml` (a `config_file` you
supplied yourself is never touched), and prunes the now-empty default-layout
parent directory. Unlike rustfs, Garage writes as root and pgcli runs podman as
root on the managed hosts, so plain removal suffices — no `podman unshare`
fallback.

## Logs

```bash
pg logs addon garage --name store      # last 50 lines
pg logs addon garage --name store -f   # follow
```

## Troubleshooting

- **`pulling garage image ... : ...` on install.** The tag
  `docker.io/dxflrs/garage:v2.4.1` is not reachable — check network access to
  Docker Hub, or pre-pull it with `podman pull`.
- **`pg mc alias set` fails with a connection error.** The default `--listen
  127.0.0.1` binds the store loopback-only; on macOS, use
  `host.containers.internal`, not `127.0.0.1`. Confirm the live API port with
  `pg addon list`.
- **Writes through the nginx front fail with `Invalid signature`.** The front
  rewrote `Host` without the port; use `proxy_set_header Host $http_host;` (the
  [conf-file recipe](#tls-by-fronting-with-nginx) does) — the `--upstream`
  template mode hard-codes `$host` and does not fit S3.
- **PUTs through the nginx front fail at ~1 MiB.** nginx defaults
  `client_max_body_size` to 1m; the conf-file recipe sets it to `0` (unlimited).
- **pgBackRest refuses the endpoint (HTTP / cert).** pgBackRest forces HTTPS for
  S3 repos. Front Garage with the nginx addon (`--tls`) and point
  `backup.repo.s3.ca_file` at the front's `ca.crt`.
- **`replication_factor` must be 1 on a single-node instance.** A hand-edited
  `replication_factor: 2` without a `cluster` is rejected at install — a lone
  node cannot meet a multi-node quorum. For multi-node, set `cluster` (or install
  with `--cluster`); see [Multi-node cluster](#multi-node-cluster).
