---
title: "JuiceFS"
description: "Mount an S3-backed POSIX filesystem as a managed addon (host binary)."
weight: 59
---

[JuiceFS](https://juicefs.com/) puts a real POSIX filesystem on top of object
storage: file data is split into chunks and stored in an S3 bucket, while a
separate **metadata engine** (SQLite, Redis, PostgreSQL…) keeps the namespace,
file attributes and chunk maps. You get an ordinary directory you can `ls`,
`cat` and `grep` — every write eventually lands as immutable objects in the
bucket. It solves the "I want a filesystem, but the bytes should live in S3"
problem: snapshots are cheap (objects are immutable), capacity is the bucket's,
and the same bucket can be re-mounted from any host that knows the metadata
URL.

As a pgcli addon, JuiceFS is pgcli's first **host-binary** addon: there is no
container. `pg addon install juicefs` downloads the official binary from GitHub
releases (sha256-verified; a `juicefs` already on `PATH` is reused), formats
the volume against an **external S3 service** and mounts it on the host.

> **Platform**: Linux only (macOS refuses with a clear error, like BeeGFS).
> The mount is a plain **host** directory — visible to host processes only.
> It does NOT propagate into running PG instance containers (no rshared mount
> propagation in v1; the addon manages this host's mount, not a cluster's).

## Everything is explicit

Both sides of the volume are named on the command line — pgcli never
auto-detects or inherits from same-host addons:

- **Object store**: an EXTERNAL S3 service (cloud, or a MinIO on another
  machine) given as `--s3-endpoint http(s)://host[:port]` plus
  `--access-key`/`--secret-key`. pgcli probes the endpoint before formatting,
  so an unreachable host fails fast with "not answering" instead of hanging
  inside `juicefs format`. The endpoint is baked into the volume metadata at
  format time — later mounts need only the metadata URL.
- **Metadata engine**: an explicit URL — `--dsn` (alias `--meta-dsn`)
  accepting `postgres://`, `redis://` or `sqlite3://`. pgcli never creates or
  drops the database; for PostgreSQL it must already exist.
- **Bucket**: never created by pgcli. Provision it yourself (once) with the
  `pg mc` wrapper — the install prints the exact two lines:

```bash
pg mc alias set externals3 http://minio.example.com:9000 AK '***'
pg mc mb externals3/juicefs-data
```

```bash
pg addon install juicefs --name data \
    --s3-endpoint http://minio.example.com:9000 \
    --access-key AK --secret-key SK \
    --bucket juicefs-data \
    --dsn "sqlite3:///var/lib/juicefs/data.db"
```

Any engine works the same way — pick the URL:

```bash
# PostgreSQL: the database must already exist (pgcli never creates or drops
# it); juicefs builds its own jfs_* tables inside.
--dsn "postgres://jfs:pass@meta-host:5432/jfs?sslmode=disable"

# Redis (+TLS): the /N suffix selects the logical database.
--dsn "redis://:pass@redis-host:6379/0"
--dsn "rediss://user:pass@redis-host:6380/1"
```

The mount point is yours to choose — by default it lives under pgcli's addon
dir; point it anywhere with `--mount-point`. Create the directory yourself
(`mkdir -p`) at a path the user running `pg` has read-write access to:

```bash
mkdir -p /mnt/cloudfs
pg addon install juicefs --name data \
    --s3-endpoint https://s3.amazonaws.com \
    --access-key AK --secret-key SK \
    --dsn "postgres://jfs:pass@meta-host:5432/jfs?sslmode=disable" \
    --bucket juicefs-data --mount-point /mnt/cloudfs
```

The three engines can all be pgcli's own services — created with pgcli, then
wired in explicitly as ordinary external URLs (the install never looks them
up by itself; it could equally well point at a managed DB or a cloud bucket):

```bash
# PostgreSQL metadata: create the instance, then the database (pgcli's
# install path never creates or drops it). `pg status` prints the admin
# connection URL — swap its /postgres path for the new database.
pg create meta-pg
pg exec meta-pg -c "CREATE DATABASE jfs"
pg status meta-pg

pg addon install juicefs --name data \
    --s3-endpoint http://minio-host:9000 --access-key AK --secret-key SK \
    --bucket juicefs-data \
    --dsn "postgres://admin:PASS@127.0.0.1:35432/jfs?sslmode=disable"
```

```bash
# Redis metadata (a pgcli redis addon; `pg addon password redis` returns the
# generated password bare — paste it in, and /N picks the logical database):
pg addon install redis --name mred
pg addon password redis --name mred
pg addon install juicefs --name r1 \
    --s3-endpoint http://minio-host:9000 --access-key AK --secret-key SK \
    --bucket juicefs-r1 --dsn "redis://:PASS@127.0.0.1:6379/1"
```

```bash
# MinIO as the object store (a pgcli minio addon — or silo/rustfs/garage/
# seaweedfs; create the bucket by hand, pgcli never does; the root user is
# admin unless --root-user said otherwise, and `pg addon password minio`
# returns the secret key bare):
pg addon install minio --name ms3
pg mc mb ms3/juicefs-data
pg addon install juicefs --name m1 \
    --s3-endpoint http://127.0.0.1:9000 --bucket juicefs-data \
    --access-key admin --secret-key "$(pg addon password minio --name ms3)" \
    --dsn "sqlite3:///var/lib/juicefs/m1.db"
```

These examples run `pg` on the same machine as the store, so loopback
(default `127.0.0.1`) is enough. When the JuiceFS client lives on another
host, publish the store on the network and point `--s3-endpoint` at its
address — the endpoint is baked into the volume at format time, so publish it
from the start:

```bash
pg addon install minio --name ms3 --listen 0.0.0.0   # --force if already installed
# from the other host:
pg addon install juicefs --name m1 --s3-endpoint http://10.0.0.5:9000 …
```

(The same note applies to the redis example: redis already binds `0.0.0.0`
by default — its password is the gate.)

The same volume can then be mounted from any host that has the metadata URL —
the credentials and endpoint already live inside it.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `juicefs` | Volume name (config map key; also the JuiceFS volume name at format time) |
| `--dsn` / `--meta-dsn` | — | REQUIRED. Metadata-engine URL: `postgres://`, `redis://` or `sqlite3://` |
| `--s3-endpoint` | — | REQUIRED. External S3 service as `http(s)://host[:port]` (no bucket path, no embedded credentials) |
| `--access-key` / `--secret-key` | — | REQUIRED together. S3 credentials for the external service |
| `--bucket` | `juicefs-<name>` | Bucket name — pgcli does NOT create it (see above) |
| `--mount-point` | `<base>/addon/juicefs/<name>/mnt` | Host directory to mount — pgcli never creates it: `mkdir -p` it beforehand, writable for the user running `pg` |
| `--cache-dir` | `<base>/addon/juicefs/<name>/cache` | Local data cache dir |
| `--install-dir` | `/usr/local/bin` | Where the host binary goes (falls back to `~/.local/bin` when not writable — keep it on `PATH`) |
| `--version` | pinned (`1.4.1`) | Release to download (its sha256 is pinned in pgcli; pass a version whose digest pgcli knows) |
| `--no-mount` | off | Format and configure, leave it unmounted |
| `--force` | off | Rebuild the config entry from the flags (format is still skipped when the volume already exists in the metadata engine) |

FUSE itself (`/dev/fuse` + `fusermount3`) is **probed, never installed** — a
missing `fuse3` package fails the install with the apt line to run.

## Daily operations

```bash
pg addon juicefs status --name data      # endpoint, bucket, meta URL, binary, mount state
pg addon juicefs umount --name data
pg addon juicefs mount --name data       # juicefs mount -d self-daemonizes
pg addon juicefs mount --name data --mount-point /mnt/cloudfs
                       # mount at a custom path (mkdir -p it first); while
                       # unmounted this also PERMANENTLY relocates the volume
pg addon start juicefs --name data       # top-level aliases of mount/umount
pg addon stop  juicefs --name data
pg logs addon juicefs --name data -f     # tail the mount log
pg addon password juicefs --name data    # the stored S3 secret key
pg addon remove juicefs --name data      # unmount + forget; bucket, metadata, binary stay
pg addon remove juicefs --name data --clean-data   # also deletes the addon host dir
```

`remove` never deletes S3 data or the metadata store — losing the `--dsn`
target means losing the filesystem. The mount owner is the user running `pg`.

### Moving to a different mount point

The mount point is recorded on the config entry, and `mount` accepts a
`--mount-point` override that relocates the volume — the new path is persisted
to `pg.yaml`, so later plain `mount`s (and autostart) use it. The old mount
must be down first, and the new directory must exist and be writable (same
contract as install):

```bash
pg addon juicefs umount --name data
mkdir -p /srv/cloudfs
pg addon juicefs mount --name data --mount-point /srv/cloudfs
#   [i] mount point moved to /srv/cloudfs (saved)
# ✓ juicefs "data" mounted at /srv/cloudfs
```

Attempting the move while still mounted refuses with an "umount first" hint;
pointing it at a missing directory fails with the `mkdir -p` remedy. The mount
point is fully decoupled from pgcli's bookkeeping (pid/log/cache stay under
`<base>/addon/juicefs/<name>/`), so relocation never disturbs the volume
itself — it is the same bucket and metadata re-attached elsewhere.

## Autostart

```bash
pg autostart enable --juicefs --name data
```

JuiceFS is a host-binary addon, so `pg start --autostart` **re-mounts** the
volume at boot instead of starting a container. The backend is external — boot
succeeds only when the S3 endpoint is reachable again. It never re-formats. The
mount comes up as the user the boot service runs as.
