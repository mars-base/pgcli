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

## The password is the data

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

ZeroFS fences concurrent writers with S3 **conditional puts** (`If-None-Match`).
`--conditional-put` selects how:

| Mode | Meaning |
|---|---|
| `auto` (default) | Use the store's native `If-None-Match` — needs MinIO RELEASE.2022-08+ (pgcli's default image qualifies) or real S3 |
| `redis` | Fence through a Redis instead — for stores lacking conditional-put (**silo** currently); `--redis-name <pgcli redis addon>` or `--redis-dsn redis://…` |
| `none` | No fencing — safe only when nothing else ever touches the bucket |

If a server start fails on a store that lacks conditional-put, the install
recognises the log signature and tells you to retry with
`--conditional-put redis`.

## Prerequisites

Same manual-`pg mc` contract as JuiceFS: pgcli **never creates buckets**. It
reads the S3 addon's config, probes its port, and prints the exact
`pg mc mb …` line if the bucket is missing:

```bash
pg addon install minio --name ms3
pg mc mb ms3/zerofs-data
pg addon install zerofs --name z1 --s3 minio --s3-name ms3 --bucket zerofs-data
```

S3 credentials default to the backend addon's root user/password;
`--access-key/--secret-key` override them (give both or neither).

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `zerofs` | Volume name (config map key) |
| `--s3` | `minio` | Backend kind — `minio` or `silo` |
| `--s3-name` | = `--s3` | Which addon instance backs the store (installed + running) |
| `--bucket` | `zerofs-<name>` | Bucket — pgcli does NOT create it |
| `--access-key` / `--secret-key` | backend root creds | S3 credential override |
| `--encryption-password` | generated | Wrap password; pass the stored one to re-attach a kept bucket |
| `--mount-point` | `<base>/addon/zerofs/<name>/mnt` | Host directory to mount |
| `--cache-dir` | `<base>/addon/zerofs/<name>/cache` | Server-side disk cache dir |
| `--server-cache-gb` | 100 | Server cache size (GiB) |
| `--listen` | `127.0.0.1` | 9P bind address — **must be loopback** (no auth in 9P) |
| `--port` | 5564 | 9P port (one per zerofs instance — enforced) |
| `--conditional-put` | `auto` | `auto` / `redis` / `none` (see Fencing) |
| `--redis-name` / `--redis-dsn` | `redis` | Redis fence backend for `--conditional-put redis` |
| `--install-dir` | `/usr/local/bin` | Where the host binary goes (falls back to `~/.local/bin`) |
| `--version` | pinned (`2.3.5`) | Release to download (digest pinned in pgcli) |
| `--no-mount` | off | Write config + start server, leave it unmounted |
| `--force` | off | Re-render `config.toml` and restart — **keeps the stored encryption password** (a fresh one could not unwrap existing data) |

FUSE (`/dev/fuse` + `fusermount3`) is probed, never installed.

## Day-2

```bash
pg addon zerofs status --name z1     # server process, mount state, files, fence mode
pg addon zerofs umount --name z1     # detach the FUSE client
pg addon zerofs mount  --name z1     # attach it again (server must run)
pg addon zerofs stop   --name z1     # unmount + kill server
pg addon zerofs start  --name z1
pg addon start/stop zerofs --name z1 # top-level aliases (start=server, stop=full)
pg logs addon zerofs --name z1 -f                      # the server log
pg logs addon zerofs --name z1 --service mount -f      # the FUSE client log
pg addon remove zerofs --name z1     # stop + forget; bucket + encrypted data stay
pg addon remove zerofs --name z1 --clean-data   # also deletes the addon host dir
```

The `config.toml`, server/client logs and pid files live under
`<base>/addon/zerofs/<name>/`; the secrets (encryption password + S3 keys)
are kept in the 0600 `config.toml` and — like every pgcli addon credential —
in plain view in your `pg.yaml`.

## Autostart

```bash
pg autostart enable --zerofs --name z1
```

At boot, `pg start --autostart` brings up the 9P server (waiting for the port
to listen) and then the FUSE client — after every object-storage addon in the
sequence, so the S3 backend answers before the filesystem reaches for it.
Nothing is re-rendered or regenerated: the existing `config.toml` and stored
password are reused verbatim.
