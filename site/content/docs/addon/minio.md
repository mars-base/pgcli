---
title: "MinIO"
description: "Run MinIO as a pgcli addon — single-node S3-compatible object storage with web console, typically as a shared pgBackRest backup repository"
weight: 49
---

[MinIO](https://min.io) is S3-compatible object storage. pgcli runs it as a
**standalone, top-level addon** — shared infrastructure, not a per-instance
sidecar — in **single-node** mode, with the web console included. The intended
use is a backup repository every host can reach (e.g. a pgBackRest `repo1-type=s3`
target for a Patroni cluster), but it is a general-purpose object store.

> **Platform support:** the MinIO addon is **Linux (amd64) only** for now. It
> serves over host networking, which the macOS `podman machine` does not expose
> to containers, and the public image is built from the upstream amd64 binary.
> Elsewhere the manager fails fast with a clear message; `pg addon list` still
> shows configured instances without live status.

## How It Works

One container, one directory: `minio server /data --address <listen>:<api-port>
--console-address <listen>:<console-port>`, where `/data` is a bind mount of the
instance's host data directory (default `<base-dir>/addon/minio/<name>/data`).
Everything MinIO stores lives there, so it outlives the container.

The image is the public pre-built tag
`ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226` — the upstream `.deb`'s
static binary on Alpine, because MinIO's official image dropped the bundled web
console. `pg addon install` pulls it; pgcli never builds the image at run time.

Credentials are handled like Patroni's:

- `root_user` defaults to `admin`;
- `root_password` is **generated on first install** and stored in `pg.yaml`
  (`addons.minio.<name>.root_password`) and **printed once** in the install
  summary for convenience.

The container runs `--ulimit nofile=1048576:1048576` and `--stop-timeout 60` as
MinIO's deployment docs recommend. Note that root credentials passed as `-e`
are visible in `podman inspect` — the same exposure as any hand-run container;
fine for a rootless single-host deployment, which is what this is.

## Install

```bash
# default instance name "minio", ports from the pool (base 9000), loopback bind
pg addon install minio

# a named instance with an explicit data directory
pg addon install minio --name store --data-dir /srv/minio

# fixed ports and a different root user
pg addon install minio --name store --api-port 9000 --console-port 9001 --root-user admin

# expose the store on the network instead of loopback only
pg addon install minio --name store --listen 0.0.0.0
```

The output reports endpoints and the root credentials:

```
✓ minio installed: "store"
  Container:    pgcli-minio-default-store
  Image:        ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226
  Data:         ~/pg/addon/minio/store/data
  S3 API:       http://127.0.0.1:9000
  Console:      http://127.0.0.1:9001

  Root user:     admin
  Root password: <generated>
                 (also stored in ~/.pgcli/pg.yaml, addons.minio.store.root_password)
```

Sign in to the console at the `Console:` URL with the printed root user and
password. Point S3 clients (including pgBackRest) at the `S3 API:` URL.

Re-running install against a **live** instance is a no-op: the container is not
recreated (a stopped one is simply started, with a notice), the flags are
merged into the stored config, and the existing root password is kept. Pass
`--force` to recreate the container so changed ports, listen address, or
credentials take effect — without losing the data directory.

> **Bind address:** the default `127.0.0.1` keeps the store local. `--listen
> 0.0.0.0` (or the `listen` key in `pg.yaml`) exposes it on the network. Anyone
> who can reach the port can then attempt the root credentials, so only do this
> behind a firewall or a TLS-terminating proxy.

## Using the mc Client

MinIO speaks S3, so any S3 client works. A ready-made `mc` lives in the
pre-built tag `ghcr.io/mars-base/pgcli/pgcli-mc:20250813083541` — the static
upstream binary on `scratch` plus a CA bundle (~29 MB, multi-arch). Set it once:

```bash
MC=ghcr.io/mars-base/pgcli/pgcli-mc:20250813083541
```

### One command at a time (`MC_HOST_*`)

`mc` accepts an alias defined entirely through the environment —
`MC_HOST_<name>=<scheme>://<user>:<password>@<host>:<port>` — so each
invocation is stateless: no config file, no volume, and the password stays out
of the command line (and your shell history):

```bash
PASS=$(yq '.addons.minio.store.root_password' ~/.pgcli/pg.yaml)

podman run --rm --network host \
  -e MC_HOST_store="http://admin:${PASS}@127.0.0.1:9000" \
  "$MC" mb store/backups

podman run --rm --network host \
  -e MC_HOST_store="http://admin:${PASS}@127.0.0.1:9000" \
  "$MC" ls store
```

### Many commands (a persistent alias)

To avoid repeating `-e` on every run, `alias set` once and mount the config
directory on **every** run. The image pins `HOME=/data`, so the mount is what
`mc` reads and writes:

```bash
mkdir -p ~/.mc-config
podman run --rm -v ~/.mc-config:/data --network host "$MC" \
  alias set store http://127.0.0.1:9000 admin \
  "$(yq '.addons.minio.store.root_password' ~/.pgcli/pg.yaml)"

podman run --rm -v ~/.mc-config:/data --network host "$MC" ls store
podman run --rm -v ~/.mc-config:/data --network host "$MC" cp ./dump.pglz store/backups/
```

Without the `-v`, `alias set` still succeeds — but the container exits with the
alias, and the next run cannot see it.

> **`--network host` on Linux:** MinIO serves on the host network, and
> `127.0.0.1` inside a bridge-networked container is the container itself, not
> the host. If you bind the store to a routable address with `--listen`, point
> the alias at that address instead and drop `--network host`.

### Common commands

| Command | Purpose |
|-------|---------|
| `mc ls store` / `mc ls store/backups` | list buckets / objects |
| `mc mb store/backups` | create a bucket |
| `mc cp ./file store/backups/` | upload (add `--recursive` for a tree) |
| `mc cp store/backups/file ./` | download |
| `mc find store --name '*.pglz'` | search objects by name |
| `mc du store` | size per bucket |
| `mc rm store/backups/file` | delete one object |

The same root credentials work for any S3 SDK — including pgBackRest against a
`repo1-type=s3` repository.

## Ports

Each instance takes **two consecutive ports** from one pool, starting at
`minio_start_port` (default **9000**): the S3 API first, the console second.
Like every other auto-assigned pool, it skips ports in use and the explicit
ports of sibling instances:

```bash
pg addon install minio --name store    # 9000 / 9001
pg addon install minio --name archive  # 9002 / 9003
```

## Configuration

Instances live under the top-level `addons.minio` map in `pg.yaml`, keyed by
instance name:

```yaml
namespace: default
minio_start_port: 9000
addons:
  minio:
    store:
      container_name: pgcli-minio-default-store
      name: store
      image_tag: ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226
      # data_dir: /srv/minio     # omit for <base-dir>/addon/minio/store/data
      listen: 127.0.0.1
      api_port: 9000
      console_port: 9001
      root_user: admin
      root_password: <generated>   # written on first install
      autostart: false             # pg autostart enable --minio --name store
```

Edits to `listen`, ports, `root_user`, `root_password`, `image_tag`, or
`data_dir` take effect after the next `pg addon install minio --name store
--force` — the plain install skips a still-present container, `--force`
recreates it (the data directory is never touched).

### List

```bash
pg addon list
```

```
Infra add-ons (minio):
  minio (name: store)
    Status:      running
    Listen:      127.0.0.1
    API port:    9000
    Console port: 9001
    Console URL: http://127.0.0.1:9001/
    Data:        ~/pg/addon/minio/store/data
    Root user:   admin
    Image:       ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226
    Container:   pgcli-minio-default-store
```

`pg addon list` never prints the password — read it from `pg.yaml`.

## Start and stop

After a host reboot, bring an instance back without re-applying config:

```bash
pg addon start minio --name store
pg addon stop  minio --name store
```

`install` skips a still-present container (starting it if stopped); `start` only
starts an existing one (and self-heals an improper state by recreating it from
the config).

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring instances up after a host reboot:

```bash
pg autostart enable --minio --name store
```

This sets `autostart: true` and installs/refreshes the boot service (see
[Auto-start on Boot](/docs/autostart/)). Boot is **start-only**. MinIO is
independent of the PostgreSQL stack, so it is started last; there is no
ordering constraint. `pg autostart status` lists every target's state.

## Remove

```bash
pg addon remove minio --name store            # container gone, data kept
pg addon remove minio --name store --clean-data   # also delete the data directory
```

The data directory **is the object storage** — losing it means losing every
bucket in it — so `remove` keeps it by default and says where it is.
`--clean-data` deletes it (and prunes the now-empty default-layout parent
directory; a `data_dir` override and its parent are never touched).

## Logs

```bash
pg logs addon minio --name store      # last 50 lines
pg logs addon minio --name store -f   # follow
```

MinIO logs to stdout: startup lines (`API:`/`Console:` addresses, `Documentation:`)
and request errors. An `API: http://...` block confirms the listeners came up.

## Troubleshooting

- **`pulling minio image ... : ...` on install.** The public tag
  `ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226` is not reachable — check
  network/registry access, or pre-pull it with `podman pull`.
- **Port collision on manual `--api-port`.** Pick ports outside the auto pool
  (`minio_start_port` and up) or the auto-assigner will treat them as taken;
  also remember MinIO needs a consecutive pair.
- **`pg addon start` after host reboot does nothing / fails.** Check
  `pg logs addon minio --name store -f` — most often the data directory was
  deleted (with `--clean-data` or by hand) and MinIO refuses to start on an
  empty dir it previously formatted, or the bind port moved.
- **Console reachable but S3 clients time out.** The `MINIO_SERVER_URL` is
  built from `listen` + API port; if you serve on `127.0.0.1` but access from
  another host, clients get redirected to the loopback URL. Set `listen` to the
  address clients can actually reach.
- **macOS / arm.** Not supported yet — see Platform support above.
