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
releases (sha256-verified; a `juicefs` already on `PATH` is reused), asks a
pgcli **S3 addon** (`minio` — or `silo` — already installed) for the backend,
optionally creates its metadata database in a pgcli **PG instance** or reuses a
pgcli **redis** addon, formats the volume and mounts it on the host.

> **Platform**: Linux only (macOS refuses with a clear error, like BeeGFS).
> The mount is a plain **host** directory — visible to host processes only.
> It does NOT propagate into running PG instance containers (no rshared mount
> propagation in v1; the addon manages this host's mount, not a cluster's).

## Prerequisites

The bucket is **never created by pgcli** — object-storage provisioning stays in
your hands via the `pg mc` wrapper (the same manual flow as every other S3
addon). pgcli reads the S3 addon's config, probes its port, and prints the
exact `pg mc mb …` line to run when the bucket is missing. Minimal flow:

```bash
pg addon install minio --name ms3        # the S3 backend (already running)
pg mc mb ms3/juicefs-data                # create the volume's bucket by hand
pg addon install juicefs --name data --s3 minio --s3-name ms3 --bucket juicefs-data
```

Credentials default to the backend addon's root user/password (pgcli already
stores them; JuiceFS copies them into its metadata at format time, so later
mounts need no S3 credentials at all). `--access-key/--secret-key` override
them — e.g. with a volume-scoped service account you created via `pg mc`.

## Metadata engines (`--meta`)

| Engine | Flag | What pgcli does |
|---|---|---|
| `sqlite3` (default) | — | Metadata file under the addon dir; single host only |
| `redis` | `--redis-name cache` / `--redis-dsn redis://…` | Resolves a pgcli **redis** addon instance (must already be installed — pgcli won't auto-install it) to `redis://:<pw>@127.0.0.1:<port>/0` |
| `pg` | `--pg-name mydb` | Uses a pgcli **PG instance**; creates the `juicefs_<name>` metadata database if missing (idempotent — a reinstall reuses it), never drops it |
| `dsn` | `--meta-dsn <url>` | Passes any `postgres://` / `redis://` / `sqlite3://` URL straight through |

```bash
pg addon install juicefs --name data --meta pg --pg-name default --s3 minio --s3-name ms3
pg addon install juicefs --name r1 --meta redis --redis-name cache
pg addon install juicefs --name e1 --meta-dsn "postgres://u:p@other-host:5432/jfs?sslmode=disable"
```

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `juicefs` | Volume name (config map key; also the JuiceFS volume name at format time) |
| `--meta` | `sqlite3` | Metadata engine — `sqlite3`, `redis`, `pg` or `dsn` |
| `--meta-dsn` / `--dsn` | — | The metadata URL itself (implies `--meta dsn`) |
| `--redis-name` | `redis` | pgcli redis addon for `--meta redis` |
| `--pg-name` | — | pgcli PG instance for `--meta pg` |
| `--s3` | `minio` | Backend kind — `minio` or `silo` |
| `--s3-name` | = `--s3` | Which addon instance backs the store (must already be installed and running) |
| `--bucket` | `juicefs-<name>` | Bucket name — pgcli does NOT create it (see Prerequisites) |
| `--access-key` / `--secret-key` | backend root creds | Override the S3 credentials (give both or neither) |
| `--mount-point` | `<base>/addon/juicefs/<name>/mnt` | Host directory to mount |
| `--cache-dir` | `<base>/addon/juicefs/<name>/cache` | Local data cache dir |
| `--install-dir` | `/usr/local/bin` | Where the host binary goes (falls back to `~/.local/bin` when not writable — keep it on `PATH`) |
| `--version` | pinned (`1.4.1`) | Release to download (its sha256 is pinned in pgcli; pass a version whose digest pgcli knows) |
| `--no-mount` | off | Format and configure, leave it unmounted |
| `--force` | off | Rebuild the config entry from the flags (format is still skipped when the volume already exists in the metadata engine) |

FUSE itself (`/dev/fuse` + `fusermount3`) is **probed, never installed** — a
missing `fuse3` package fails the install with the apt line to run.

## Day-2

```bash
pg addon juicefs status --name data      # backend, meta engine, binary, mount state
pg addon juicefs umount --name data
pg addon juicefs mount --name data       # juicefs mount -d self-daemonizes
pg addon start juicefs --name data       # top-level aliases of mount/umount
pg addon stop  juicefs --name data
pg logs addon juicefs --name data -f     # tail the mount log
pg addon password juicefs --name data    # the stored S3 secret key
pg addon remove juicefs --name data      # unmount + forget; bucket, metadata, binary stay
pg addon remove juicefs --name data --clean-data   # also deletes the addon host dir
```

`remove` never deletes S3 data or a `--meta pg` database — for the latter it
prints the manual `DROP DATABASE` line instead. The mount owner is the user
running `pg`.

## Autostart

```bash
pg autostart enable --juicefs --name data
```

JuiceFS is a host-binary addon, so `pg start --autostart` **re-mounts** the
volume at boot instead of starting a container — after every object-storage
addon in the sequence, so the S3 endpoint is listening before the filesystem
reaches for it. It never re-formats. The mount comes up as the user the boot
service runs as.
