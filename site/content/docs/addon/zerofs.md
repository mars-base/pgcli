---
title: "ZeroFS"
description: "Mount an encrypted, self-maintaining S3 filesystem as a managed addon (host binary)."
weight: 60
---

[ZeroFS](https://github.com/Barre/ZeroFS) is an S3-backed filesystem that
keeps **everything inside the bucket**: instead of JuiceFS's external metadata
engine, it runs its own LSM tree directly on object storage, and every object
is encrypted with a per-volume data key wrapped by your **encryption
password**. Snapshots and consistency are handled by background compaction —
there is no database to back up, no Redis to keep alive. You lose neither
property than a normal filesystem gives you (it mounts via FUSE through a
local 9P hop), and the bucket alone is a complete, encrypted copy of the
volume.

Like [JuiceFS](../juicefs/), ZeroFS is a **host-binary** addon — no container.
`pg addon install zerofs` downloads the official release binary
(sha256-verified; a `zerofs` already on `PATH` is reused), writes the volume's
`config.toml` itself, and runs **two host processes**:

- the **server** (`zerofs run -c config.toml`) — owns the S3 + encryption side
  and speaks 9P on a **loopback** port (default 5564). 9P has no
  authentication, so pgcli hard-rejects a non-loopback `--listen`;
- the **client** (`zerofs mount 127.0.0.1:<port> <mount-point>`) — the FUSE
  mount you actually use, auto-reconnecting if the server bounces.

> **Platform**: Linux only (host FUSE; macOS refuses, like BeeGFS). The mount
> is a plain host directory — it does not propagate into PG instance containers.

## Everything is explicit

The object store is an EXTERNAL S3 service (cloud, or a MinIO on another
machine), named on the command line — pgcli never auto-detects or inherits a
same-host addon's credentials:

```bash
pg addon install zerofs --name z1 \
    --s3-endpoint http://minio.example.com:9000 \
    --access-key AK --secret-key SK \
    --bucket zerofs-data
```

- `--s3-endpoint` is a bare `http(s)://host[:port]` (no bucket path, no
  embedded credentials); pgcli probes it before starting the server, so an
  unreachable host fails fast instead of hanging the server's retry loop.
- `--access-key`/`--secret-key` are required, always together.
- The bucket is never created by pgcli — provision it yourself with the
  `pg mc` wrapper; the install prints the exact two lines.
- ZeroFS signs requests, so `--s3-region` is honoured (optional — zerofs
  itself defaults to `us-east-1`; most MinIO-compatible stores accept the
  default).

The mount point is yours to choose — by default it lives under pgcli's addon
dir; point it anywhere with `--mount-point`. Create the directory yourself
(`mkdir -p`) at a path the user running `pg` has read-write access to — pgcli
never creates it:

```bash
mkdir -p /mnt/cloudfs
pg addon install zerofs --name z1 \
    --s3-endpoint http://minio.example.com:9000 \
    --access-key AK --secret-key SK \
    --bucket zerofs-data --mount-point /mnt/cloudfs
```

A missing or unwritable directory fails the install (and every later `mount`)
with the exact `mkdir -p` remedy before anything is downloaded or started.

Both backends can be pgcli's own services — created with pgcli, then wired in
explicitly as ordinary external URLs (the install never looks them up by
itself; it could equally well point at a cloud bucket or a remote Redis):

```bash
# MinIO as the object store (a pgcli minio addon — or silo/rustfs/garage/
# seaweedfs; create the bucket by hand, pgcli never does; the root user is
# admin unless --root-user said otherwise, and `pg addon password minio`
# returns the secret key bare — safe to inline):
pg addon install minio --name ms3
pg mc mb ms3/zerofs-data

pg addon install zerofs --name z1 \
    --s3-endpoint http://127.0.0.1:9000 --bucket zerofs-data \
    --access-key admin --secret-key "$(pg addon password minio --name ms3)"
```

```bash
# Redis as the fence backend (--conditional-put redis — for stores lacking
# conditional put; pgcli's MinIO needs none, this shows the wiring). `pg
# addon password redis` returns the generated password bare, paste it into
# the URL, /N picks the logical database:
pg addon install redis --name fence
pg addon password redis --name fence

pg addon install zerofs --name z2 \
    --s3-endpoint http://silo.example.com:9000 \
    --access-key AK --secret-key SK --bucket zerofs-fenced \
    --conditional-put redis --redis-dsn "redis://:PASS@127.0.0.1:6379/0"
```

These examples run `pg` on the same machine as the store, so loopback
(default `127.0.0.1`) is enough. When the zerofs client lives on another
host, publish the store on the network and point `--s3-endpoint` at its
address — the endpoint goes into `config.toml`, so re-render with `--force`
if you change it later:

```bash
pg addon install minio --name ms3 --listen 0.0.0.0   # --force if already installed
# from the other host:
pg addon install zerofs --name z1 --s3-endpoint http://10.0.0.5:9000 …
```

(Redis already binds `0.0.0.0` by default — its password is the gate. Note
this is about the S3/redis backends only: zerofs's OWN 9P `--listen` must
stay loopback — 9P has no authentication.)

## The password is the data

> ⚠️ **🔑 `pg addon remove` DESTROYS your only copy of the password — save it
> first.** `remove` keeps the bucket but deletes the config entry, and the
> password lives only in that entry and its `config.toml`. Without the original
> password the kept bucket is permanently unreadable — no recovery path exists.
> Before any `remove` you might want to undo later:
> `pg addon password zerofs --name z1 > /somewhere/safe` (outside pgcli's dirs).

The encryption password **wraps the bucket's data-encryption key (DEK)**. It is
generated on first install — retrieve it any time with:

```bash
pg addon password zerofs --name z1
```

Lose it and the bucket's contents are unrecoverable. This also matters on
`pg addon remove`, which deliberately **keeps the bucket**: the config entry
(and its copy of the password) is gone, so a plain re-install generates a
*new* password that cannot unwrap the *old* DEK — the server fails to start
with `Invalid password or corrupted key`. Re-attach the kept bucket by passing
the original password back in:

```bash
pg addon install zerofs --name z1 --encryption-password '<the stored one>'
```

(To truly start over: empty the bucket, or pick a new `--bucket`.)

## Fencing: conditional put

ZeroFS fences concurrent writers with S3 **conditional puts**
(`If-None-Match`). `--conditional-put` selects how:

| Mode | Meaning |
|---|---|
| `auto` (default) | Use the store's native `If-None-Match` — needs MinIO RELEASE.2022-08+ or real S3 |
| `redis` | Fence through Redis instead — for stores lacking conditional-put (e.g. silo); requires an explicit `--redis-dsn redis://…` (no same-host addon lookup) |
| `none` | No fencing — safe only when nothing else ever touches the bucket |

If a server start fails on a store that lacks conditional-put, the install
recognises the log signature and tells you to retry with
`--conditional-put redis --redis-dsn …`.

`--redis-dsn` is a plain `redis://` URL — the DSN is written verbatim into
`config.toml`, and any Redis works, including a pgcli redis addon (see the
wiring example above; `pg addon password redis` returns its generated
password bare, `/N` picks the logical database). TLS endpoints use the same
URL shape as everywhere else in pgcli: `rediss://user:pass@redis-host:6380/1`.

One 9P port per instance: a zerofs server that loses the port bind starts
silently and listens on nothing, so install and `start` refuse an already-
answering `--port` outright — run several volumes by giving each its own port.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `zerofs` | Volume name (config map key) |
| `--s3-endpoint` | — | REQUIRED. External S3 service as `http(s)://host[:port]` |
| `--s3-region` | zerofs's own `us-east-1` | SigV4 region for the endpoint |
| `--bucket` | `zerofs-<name>` | Bucket — pgcli does NOT create it |
| `--access-key` / `--secret-key` | — | REQUIRED together. S3 credentials |
| `--encryption-password` | generated | Wrap password; pass the stored one to re-attach a kept bucket |
| `--mount-point` | `<base>/addon/zerofs/<name>/mnt` | Host directory to mount — pgcli never creates it: `mkdir -p` it beforehand, writable for the user running `pg` |
| `--cache-dir` | `<base>/addon/zerofs/<name>/cache` | Server-side disk cache dir |
| `--server-cache-gb` | 100 | Server cache size (GiB) |
| `--listen` | `127.0.0.1` | 9P bind address — **must be loopback** (no auth in 9P) |
| `--port` | 5564 | 9P port (refused when already answering — one per zerofs instance) |
| `--conditional-put` | `auto` | `auto` / `redis` / `none` (see Fencing) |
| `--redis-dsn` | — | Redis fence backend, REQUIRED with `--conditional-put redis` |
| `--install-dir` | `/usr/local/bin` | Where the host binary goes (falls back to `~/.local/bin`) |
| `--version` | pinned (`2.3.5`) | Release to download (digest pinned in pgcli) |
| `--no-mount` | off | Write config + start server, leave it unmounted |
| `--force` | off | Re-render `config.toml` and restart — **keeps the stored encryption password** (a fresh one could not unwrap existing data) |

FUSE (`/dev/fuse` + `fusermount3`) is probed, never installed.

## Daily operations

```bash
pg addon zerofs status --name z1     # server process, mount state, files, fence mode
pg addon zerofs umount --name z1     # detach the FUSE client
pg addon zerofs mount  --name z1     # attach it again (server must run)
pg addon zerofs mount  --name z1 --mount-point /mnt/cloudfs
                                     # custom path (mkdir -p it first); while
                                     # unmounted this relocates the volume too
pg addon zerofs stop   --name z1     # unmount + kill server
pg addon zerofs start  --name z1
pg addon start/stop zerofs --name z1 # top-level aliases (start=server, stop=full)
pg logs addon zerofs --name z1 -f                      # the server log
pg logs addon zerofs --name z1 --service mount -f      # the FUSE client log
pg addon remove zerofs --name z1     # stop + forget; bucket + encrypted data stay
                                     # 🔑 save the password FIRST: pg addon password zerofs --name z1
pg addon remove zerofs --name z1 --clean-data   # also deletes the addon host dir
```

The `config.toml`, server/client logs and pid files live under
`<base>/addon/zerofs/<name>/`; the secrets (encryption password + S3 keys)
are kept in the 0600 `config.toml` and — like every pgcli addon credential —
in plain view in your `pg.yaml`.

Relocating the mount point works exactly like [JuiceFS](../juicefs/):
`umount`, `mkdir -p` the new directory (writable — same contract as
install), then mount with the override; the new path is persisted to
`pg.yaml`, so plain `mount`s and autostart use it from then on:

```bash
pg addon zerofs umount --name z1
mkdir -p /srv/cloudfs
pg addon zerofs mount --name z1 --mount-point /srv/cloudfs
#   [i] mount point moved to /srv/cloudfs (saved)
# ✓ zerofs "z1" mounted at /srv/cloudfs
```

Attempting the move while still mounted refuses with an "umount first"
hint; the FUSE mount point is fully decoupled from pgcli's bookkeeping (the
9P server keeps running on its port, `config.toml`/logs/pid stay under
`<base>/addon/zerofs/<name>/`), so the move never touches the volume.

## Autostart

```bash
pg autostart enable --zerofs --name z1
```

At boot, `pg start --autostart` brings up the 9P server (waiting for the port
to listen) and then the FUSE client. The backend is external — boot succeeds
only when the S3 endpoint is reachable again. Nothing is re-rendered or
regenerated: the existing `config.toml` and stored password are reused
verbatim.
