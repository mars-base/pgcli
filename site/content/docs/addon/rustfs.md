---
title: "rustfs"
description: "Run rustfs (the Rust S3-compatible object store) as a pgcli addon — single-node or erasure-coded, with the fixed container uid handled inside a purpose-built image"
weight: 47
---

[rustfs](https://github.com/rustfs/rustfs) is a Rust reimplementation of
S3-compatible object storage: the same S3 API, a web console, erasure-coded
multi-drive and multi-node layouts — and a completely different runtime model
from MinIO/silo. pgcli runs it as a **standalone, top-level addon** with the
same CLI surface as [`minio`](../minio/) and [`silo`](../silo/) (install, TLS,
BYO certs, drives, logs, autostart), sharing the one port pool, but with three
differences that shape this page:

1. **A fixed container user.** Upstream rustfs bakes `User=rustfs` (uid/gid
   10001) into the image. pgcli works around this entirely **inside its own
   wrapper image** — see [Privileges and ownership](#privileges-and-ownership)
   below — so a rustfs store needs no host-side ownership dance.
2. **Three topologies, no multi-node single-drive.** rustfs speaks SNSD / SNMD
   / MNMD only — see [Deployment modes](#deployment-modes).
3. **Its own TLS filenames.** rustfs reads `rustfs_cert.pem` / `rustfs_key.pem`
   from `RUSTFS_TLS_PATH`, not MinIO's `public.crt` / `private.key`.

Pick rustfs when you want a lean, Rust-native S3 endpoint; it can coexist with
minio and silo on one host (all three draw from the same port pool).

> **Platform support:** the rustfs addon is **Linux only, in practice**. The
> runtime model (a fixed container uid, drives on distinct block devices, host
> networking) is a Linux container story and is only exercised on Linux; the
> macOS bridge path is code-complete and the wrapper image is dual-arch, but it
> has not been tested on a Mac yet.

## How It Works

One container per instance. The data lives in a bind-mounted host directory
(default `<base-dir>/addon/rustfs/<name>/data`, or one directory per `--drive`),
so it outlives the container. Unlike the minio/silo addons, pgcli does **not**
drive the `rustfs` binary directly — it runs the image's own `/entrypoint.sh`
(via the wrapper below) because that entrypoint is what expands the multi-drive
brace range in `RUSTFS_VOLUMES`, creates the per-drive directories, and assembles
the server argv.

The image pgcli pulls is not the bare upstream one — it is
`ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0`, a thin pgcli wrapper built on top
of `docker.io/rustfs/rustfs:1.0.0` (the first GA release). `pg addon install
rustfs` just pulls the wrapper; pgcli never builds it at run time. The tag tracks
the pinned upstream version.

Credentials are handled like minio/silo:

- `root_user` defaults to `admin` (rustfs imposes no minimum key length;
  anything non-empty works, and `admin` sidesteps the `rustfsadmin` warning);
- `root_password` is **generated on first install** (or supplied via
  `--root-password`), stored in `pg.yaml` (`addons.rustfs.<name>.root_password`)
  and **printed once** in the install summary.

They are rustfs's root access key / secret key, passed via the
`RUSTFS_ACCESS_KEY` / `RUSTFS_SECRET_KEY` env vars — what every S3 client
(including `pg mc alias set`) calls the access key and secret key.

## Privileges and ownership

This is the one place rustfs genuinely differs from minio/silo, and pgcli has
absorbed it into the image so it is invisible in practice.

Upstream rustfs runs as a **fixed, non-configurable uid/gid (10001)**. Under
rootless podman a host user has no claim on that uid, so the naive way to make a
bind-mounted data directory writable by the process is a host-side `chown` dance
(`podman unshare chown 10001:10001 …`) — fragile, and it fights with the fact
that the operator may not be root. pgcli sidesteps all of it:

- The wrapper image's entrypoint starts **as container root**, `chown`s its own
  bind-mounted data directories to 10001, then `su`-drops to the `rustfs` user
  and `exec`s the **unmodified upstream** `/entrypoint.sh`. pgcli passes no
  `--user` flag and does **no host-side ownership work at all**.
- The result is the same on both daemon modes; only the *host-visible* numeric
  uid differs, because that is a property of podman's user namespace, not of
  pgcli:
  - **rootful** podman → the directories land on the real host uid `10001`;
  - **rootless** podman → they land on a *subordinate* host uid
    (`subuid_start + 10001`, e.g. `110001`), with `10001` only visible inside
    the container.

Neither case needs the operator to be root or to run `chown` by hand. The one
convention still worth knowing is that when you mount drives yourself
(`--drive`), the mount point should be owned by the user who runs `pg` — under
rootless podman a root-owned mount point is outside the namespace's uid range and
cannot be claimed by the in-container `chown`, exactly as with any bind mount.

**TLS is copied, never re-owned.** rustfs must read its key+cert as 10001, but
the cert directory pgcli generated (0700, pgcli-owned) has to *stay* pgcli-owned
so `pg cert`, CA refresh and `pg backup fetch-ca` keep working on it. So the
wrapper does not `chown` that directory: it mounts it **read-only** and, as
container root, **copies** the two required files into a fresh container-local
directory (owned by 10001) that `RUSTFS_TLS_PATH` points at. No host file is
ever mutated, and the BYO path uses the same copy mechanism, so BYO key/cert are
mounted read-only at the required names and never chowned either.

## Install

```bash
# default instance name "rustfs", ports from the shared pool, loopback bind
pg addon install rustfs

# a named instance with an explicit data directory
pg addon install rustfs --name store --data-dir /srv/rustfs

# fixed ports and a different root user
pg addon install rustfs --name store --api-port 9000 --console-port 9001 --root-user admin

# expose the store on the network instead of loopback only
pg addon install rustfs --name store --listen 0.0.0.0

# HTTPS via pgcli's self-signed CA (the prerequisite for a pgBackRest S3 repo)
pg addon install rustfs --name store --tls

# HTTPS with a certificate you already have (public-CA or private-CA cert)
pg addon install rustfs --name store --tls-cert /etc/ssl/rustfs.test.crt --tls-key /etc/ssl/rustfs.test.key
```

The output reports endpoints and the root credentials:

```
✓ rustfs installed: "store"
  Container:    pgcli-rustfs-default-store
  Image:        ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
  Data:         ~/pg/addon/rustfs/store/data
  S3 API:       http://127.0.0.1:9000
  Console:      http://127.0.0.1:9001

  Root user:     admin
  Root password: <generated>
```

Sign in to the console at the `Console:` URL with the printed root user and
password. Point S3 clients (including pgBackRest) at the `S3 API:` URL, or
drive them from the terminal with [`pg mc`](../minio/#using-the-mc-client).

Re-running install against a **live** instance is a no-op: the container is not
recreated (a stopped one is simply started, with a notice), the flags are merged
into the stored config, and the existing root password is kept. Pass `--force`
to recreate the container so changed ports, listen address, endpoint list, or
credentials take effect — without losing the data directory.

> **Bind address:** the default `127.0.0.1` keeps the store local. `--listen
> 0.0.0.0` (or the `listen` key in `pg.yaml`) exposes it on the network. Anyone
> who can reach the port can then attempt the root credentials, so only do this
> behind a firewall or with TLS (`--tls`).

### TLS (`--tls`)

`--tls` makes rustfs serve HTTPS. pgcli generates a self-signed CA and a leaf
cert with the stdlib — SANs cover the loopback names, `localhost`, and every NIC
IP of the host — into `<base_dir>/tls/rustfs/<name>/`: `rustfs_cert.pem` /
`rustfs_key.pem` (rustfs's required names, also mirrored as `public.crt` /
`private.key` for the CA-friendly tooling) and `ca.crt` for distribution. The
cert dir is mounted read-only and copied into the container as described in
[Privileges and ownership](#privileges-and-ownership); the endpoint URL becomes
`https://`.

Why you need it: **pgBackRest forces HTTPS for S3 repositories**, so a rustfs
meant to receive Patroni `archive-push` must speak TLS. Point
`backup.repo.s3.ca_file` at `ca.crt` and pgBackRest connects with full
certificate verification — see [Backup → S3 object storage
repository](../../backup/#s3-object-storage-repository). Everything that page
says about a MinIO repo applies to a rustfs one: the S3 contract is identical
(path-style, `repo1-s3-uri-style=path`).

**Getting the CA onto a remote host — no scp needed.** The server cert is served
as a leaf+CA chain, so a consumer on another machine can pull the root straight
out of a TLS handshake:

```bash
pg backup fetch-ca <store-host>:9000
#   [OK] CA fetched from <store-host>:9000
#        saved:    ~/.pgcli/backup/repo-ca/ca-<store-host>-9000.crt
#        SHA-256:  c0f0…fe2e
pg backup setup --s3-ca-file ~/.pgcli/backup/repo-ca/ca-<store-host>-9000.crt
```

The fetch is trust-on-first-use — compare the printed SHA-256 against the store
host's `sha256sum ~/.pgcli/tls/rustfs/<name>/ca.crt` before trusting it.

### Bring your own certificate (`--tls-cert` / `--tls-key`)

`--tls` only ever serves pgcli's own self-signed pair. To serve a certificate you
already hold, pass `--tls-cert <leaf(+chain).pem> --tls-key <key.pem>`. Together
with `--tls` (implied), this replaces the generated certs: the two files are
mounted read-only and copied into the container at the names rustfs requires
(`/opt/rustfs/certs/rustfs_cert.pem`, `…/rustfs_key.pem`); pgcli never re-owns
them and never writes to their source directory.

```bash
pg addon install rustfs --name store \
  --tls-cert /etc/ssl/wildcard.example.com.crt \
  --tls-key  /etc/ssl/wildcard.example.com.key
```

Everything else about BYO mode — why renewal needs `--force` (a single-file
mount pins the source inode), how clients pick their trust anchor, and `pg cert`
as a test-cert mint — is identical to the MinIO addon; see [MinIO → Bring your
own certificate](../minio/). **Turning BYO off:** remove
`cert_file`/`key_file` under this addon in `pg.yaml` and recreate with
`pg addon install rustfs --name store --force`.

## Deployment modes

rustfs has **three** layouts — there is no multi-node single-drive mode:

| Mode | Shape | Use it for |
|------|-------|------------|
| **SNSD** (single-node, single-drive) | one node, one data directory — the default when no `--endpoint` or `--drive` is given | dev, test, demos |
| **SNMD** (single-node, multi-drive) | one node, several drives — `--drive` per drive, see [SNMD](#single-node-multi-drive-snmd) | surviving a disk loss on a single host |
| **MNMD** (multi-node, multi-drive) | several nodes, several drives each — `--drive` for this node's drives + the `--endpoint` list | surviving a disk loss *and* a node loss |

There is deliberately **no MNSD** (multi-node single-drive) in rustfs. Passing
`--endpoint` without `--drive` is rejected at install time: rustfs derives its
per-drive volume range itself, so a distributed node must say how many drives it
has.

> **rustfs hard-requires distinct physical disks.** Under SNMD/MNMD, every
> `--drive` must sit on its own block device. If two drives share a device
> (`st_dev`), the rustfs process `[FATAL]`s at startup. pgcli launches detached,
> so `pg addon install` still exits 0 and the failure surfaces as a container
> that never becomes healthy — check `pg logs addon rustfs --name store` if a
> multi-drive install won't come up. (This is stricter than MinIO/silo, which
> only warn.)

## Single-Node Multi-Drive (SNMD)

One rustfs process, several host directories, erasure-coded across them. Pass
`--drive` once per drive instead of `--data-dir`:

```bash
pg addon install rustfs --name store \
  --drive /mnt/rustfs/disk1 --drive /mnt/rustfs/disk2 \
  --drive /mnt/rustfs/disk3 --drive /mnt/rustfs/disk4 --tls
```

Each `--drive` is a host directory on its own device; drive *N* (0-indexed) is
bind-mounted at container path `/data/rustfsN`, and `RUSTFS_VOLUMES` is set to
the brace range `/data/rustfs{0...3}` that the image's `/entrypoint.sh` expands
into the individual directories. `--drive` is mutually exclusive with
`--data-dir`; combining `--drive` with `--endpoint` is MNMD.

`pg addon remove rustfs --name store --clean-data` deletes each drive directory
— but refuses any drive that is still a mount point, so an accidental
`--clean-data` can never `rm -rf` through a live mount into the disk below.
Unmount first if the data below is really disposable.

## Distributed / Cluster Mode (MNMD)

Every node runs its own pgcli with its own `pg.yaml`; each `pg.yaml` carries the
*same* full endpoint list and the *same* root credentials. For rustfs the
`--endpoint` list is **one `scheme://host:port` per node** (no path — rustfs
derives the `/data/rustfsN` volume range from `--drive`), and each node passes
its own drives:

```bash
# on node 1 (10.0.0.11), four dedicated data disks:
pg addon install rustfs --name store \
  --listen 10.0.0.11 \
  --root-password '<shared-secret>' \
  --drive /mnt/rustfs/d1 --drive /mnt/rustfs/d2 --drive /mnt/rustfs/d3 --drive /mnt/rustfs/d4 \
  --endpoint http://10.0.0.11:9000 --endpoint http://10.0.0.12:9000 \
  --endpoint http://10.0.0.20:9000 --endpoint http://10.0.0.21:9000

# nodes 2-4: same command, own --listen/--drive, and the SAME --endpoint list
# AND the SAME --root-password value.
```

Cross-host MNMD is code-complete and validated at install time, but this page's
e2e coverage ran SNSD and SNMD on a real single host; the four-node ring above is
documented from the wiring and the CLI validation, not a measured cluster.
Treat it as such until you have run it.

## Using the mc Client

`pg mc` runs MinIO's `mc` client in a throwaway container and speaks rustfs's S3
API like any other store:

```bash
pg mc alias set store http://127.0.0.1:9000 admin <password>    # Linux
pg mc mb store/backups
pg mc ls store
pg mc cp ./dump.pglz store/backups/
```

The S3 data plane (PUT/GET) is proven end-to-end against rustfs via pgBackRest
and a basic `mc` round-trip, including a full PITR sequence — base backup,
`archive-push` of the WAL, and a `--time` restore that replayed the archived WAL
out of rustfs and stopped exactly at the target (Linux, rootless podman). Note
honestly: the *administrative* `mc` surface (alias-set validation, bucket
policies, admin commands) has not been exhaustively exercised against rustfs.
Use it for plain object put/get with the understanding that some
MinIO-specific admin commands may not map onto rustfs.

## Ports

Each instance takes **two consecutive ports** from one pool, starting at
`minio_start_port` (default **9000**): the S3 API first, the console second.
**The pool is shared across minio, silo, and rustfs** — all three are assigned
from one cursor, so they coexist on a host without collision (minio first by
name, then silo, then rustfs):

```bash
pg addon install minio  --name store   # 9000 / 9001
pg addon install silo   --name lake    # 9002 / 9003
pg addon install rustfs --name archive # 9004 / 9005
```

## Configuration

Instances live under the top-level `addons.rustfs` map in `pg.yaml`:

```yaml
namespace: default
minio_start_port: 9000       # shared pool: minio, silo AND rustfs draw from this
addons:
  rustfs:
    store:
      container_name: pgcli-rustfs-default-store
      name: store
      image_tag: ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
      # data_dir: /srv/rustfs    # omit for <base-dir>/addon/rustfs/store/data
      # drives:                  # multi-drive (SNMD/MNMD): one host dir per
      #   - /mnt/rustfs/d1       # drive, mounted at /data/rustfs0../rustfsN
      #   - /mnt/rustfs/d2
      listen: 127.0.0.1
      api_port: 9000
      console_port: 9001
      root_user: admin
      root_password: <generated>   # written on first install
      autostart: false             # pg autostart enable --rustfs --name store
      # tls: true                  # serve HTTPS (self-signed CA, or BYO below)
      # cert_file: /etc/ssl/rustfs.test.crt   # BYO leaf(+chain), implies tls
      # key_file:  /etc/ssl/rustfs.test.key   # BYO private key, must pair with cert_file
      # endpoints:                 # omit for single-node; one per node for MNMD:
      #   - http://10.0.0.11:9000
      #   - http://10.0.0.12:9000
```

Edits to `listen`, ports, `root_user`, `root_password`, `image_tag`, `data_dir`,
`tls`/`cert_file`/`key_file`, or `endpoints` take effect after the next
`pg addon install rustfs --name store --force`.

### List

```bash
pg addon list
```

```
Infra add-ons (rustfs):
  rustfs (name: store)
    Status:      running
    Listen:      127.0.0.1
    API port:    9000
    Console port: 9001
    Console URL: http://127.0.0.1:9001/
    Data:        ~/pg/addon/rustfs/store/data
    Root user:   admin
    Image:       ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
    Container:   pgcli-rustfs-default-store
```

`pg addon list` never prints the password — read it from `pg.yaml`. Health is on
`GET /health` (returns `200`), unlike MinIO's `/minio/health/live`.

## Start and stop

```bash
pg addon start rustfs --name store
pg addon stop  rustfs --name store
```

`install` skips a still-present container (starting it if stopped); `start` only
starts an existing one (and self-heals an improper state by recreating it from
the config). In TLS generated mode `start` also re-validates and, if needed,
re-signs the leaf.

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring instances up after a host reboot:

```bash
pg autostart enable --rustfs --name store
```

rustfs's fixed container uid is handled inside pgcli's wrapper image, so a
boot-time start needs no special host privileges beyond running podman itself.
Boot is **start-only**; rustfs is independent of the PostgreSQL stack, so it is
started last.

## Remove

```bash
pg addon remove rustfs --name store            # container gone, data kept
pg addon remove rustfs --name store --clean-data   # also delete the data directory
```

The data directory **is the object storage** — losing it means losing every
bucket in it — so `remove` keeps it by default. `--clean-data` deletes it (and
prunes the now-empty default-layout parent directory). Under rootless podman the
objects are owned by a subordinate uid that plain `rm` cannot unlink, so
`--clean-data` transparently falls back to `podman unshare rm` to reclaim them.

## Logs

```bash
pg logs addon rustfs --name store      # last 50 lines
pg logs addon rustfs --name store -f   # follow
```

## Troubleshooting

- **`pulling rustfs image ... : ...` on install.** The wrapper tag
  `ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0` is not reachable — check network
  access to ghcr.io, or pre-pull it with `podman pull`.
- **A multi-drive install won't become healthy.** rustfs `[FATAL]`s when two
  drives share a physical device. Check `pg logs addon rustfs --name store` for
  the disk check firing, and make sure each `--drive` is on its own device (a
  separate disk or its own loop mount).
- **Rootless podman can't write a drive.** A drive's mount point is owned by
  root, outside the namespace's uid range, so the container can't claim it.
  `chown` the mount point to the user who runs `pg`.
- **Port collision on manual `--api-port`.** The pool is shared across
  minio/silo/rustfs; an explicit port must be dodged by the auto-assigner for
  all three. Each store needs a consecutive pair.
- **`--clean-data` leaves a directory behind.** Under rootless podman a
  subordinate-uid tree is removed via the `podman unshare rm` fallback; if that
  path is unavailable, remove it yourself with `podman unshare rm -rf <dir>`.
