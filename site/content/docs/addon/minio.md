---
title: "MinIO"
description: "Run MinIO as a pgcli addon — single-node or distributed S3-compatible object storage with web console"
weight: 49
---

[MinIO](https://min.io) is S3-compatible object storage. pgcli runs it as a
**standalone, top-level addon** — shared infrastructure, not a per-instance
sidecar — with the web console included. It defaults to **single-node** mode and
supports a genuinely distributed **cluster mode** across hosts
([see below](#distributed--cluster-mode)). Either way it is a general-purpose
object store.

> **Platform support:** the MinIO addon works on **both platforms**. Linux
> serves over host networking; macOS joins the `pgcli-net` bridge with its two
> ports published, so the Mac reaches the API and console on `127.0.0.1:<port>`
> like any other addon. The public image is dual-arch (amd64 + arm64), so any
> host architecture works. [`pg mc`](#using-the-mc-client), the client, works on
> both platforms too.

## How It Works

One container, one directory: `minio server /data --address <listen>:<api-port>
--console-address <listen>:<console-port>`, where `/data` is a bind mount of the
instance's host data directory (default `<base-dir>/addon/minio/<name>/data`).
Everything MinIO stores lives there, so it outlives the container.

The image is the public pre-built tag
`ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226` — the upstream static
binaries (dual-arch amd64 + arm64, from the minio/minio GitHub releases) on
Alpine, because MinIO's official image dropped the bundled web console. `pg
addon install` pulls it; pgcli never builds the image at run time.

Credentials are handled like Patroni's:

- `root_user` defaults to `admin`;
- `root_password` is **generated on first install** (or supplied explicitly via
  `--root-password`) and stored in `pg.yaml`
  (`addons.minio.<name>.root_password`) and **printed once** in the install
  summary for convenience.

They are exactly MinIO's root **access key / secret key** — what every S3
client (and `pg mc alias set <name> <url> <root_user> <root_password>`) calls
the access key and secret key.

The container runs `--ulimit nofile=1048576:1048576` and `--stop-timeout 60` as
MinIO's deployment docs recommend. On macOS the `podman machine` VM caps
`RLIMIT_NOFILE` lower, so the value used there is `65536` — plenty for a
single-host dev/test store. Note that root credentials passed as `-e` are
visible in `podman inspect` — the same exposure as any hand-run container; fine
for a rootless single-host deployment, which is what this is.

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

# HTTPS via pgcli's self-signed CA (the prerequisite for a pgBackRest S3 repo)
pg addon install minio --name store --tls
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
```

Sign in to the console at the `Console:` URL with the printed root user and
password. Point S3 clients (including pgBackRest) at the `S3 API:` URL, or
drive them from the terminal with [`pg mc`](#using-the-mc-client) below.

Re-running install against a **live** instance is a no-op: the container is not
recreated (a stopped one is simply started, with a notice), the flags are
merged into the stored config, and the existing root password is kept. Pass
`--force` to recreate the container so changed ports, listen address, endpoint
list, or credentials take effect — without losing the data directory.

> **Bind address:** the default `127.0.0.1` keeps the store local. `--listen
> 0.0.0.0` (or the `listen` key in `pg.yaml`) exposes it on the network. Anyone
> who can reach the port can then attempt the root credentials, so only do this
> behind a firewall or with TLS (`--tls`, below).

### TLS (`--tls`)

`--tls` makes MinIO serve HTTPS. pgcli generates a self-signed CA and a leaf
cert with the stdlib — SANs cover the loopback names, `localhost`, and every NIC
IP of the host — into `<base_dir>/tls/minio/<name>/`: `public.crt` /
`private.key` for MinIO's `--certs-dir`, and `ca.crt` for distribution. The cert
dir is mounted read-only and the endpoint URL becomes `https://`.

Why you need it: **pgBackRest forces HTTPS for S3 repositories** (plaintext is
an upstream-rejected option), so a MinIO meant to receive Patroni `archive-push`
must speak TLS. Point `backup.repo.s3.ca_file` at `ca.crt` and pgBackRest
connects with full certificate verification — see
[Backup → S3 object storage repository](../../backup/#s3-object-storage-repository).

`pg mc` adds `--insecure` automatically when a command targets a TLS store via
a loopback alias (mc persists no CA trust per alias; same-host loopback makes
this acceptable). External `https://` endpoints keep full verification.
Toggling `--tls` on an existing instance needs `--force` to take effect.

## Distributed / Cluster Mode

MinIO's erasure-coded (EC) cluster mode is available too. It requires **at
least four distinct `host:port` endpoints** and, unlike the other addons,
**no central coordinator**: every node runs its own pgcli with its own
`pg.yaml`, and every one of those `pg.yaml` files carries the *same* full
endpoint list and the *same* root credentials. pgcli only ever starts the
container for the node it is running on — MinIO itself does the handshake to
form the ring across hosts.

The endpoint list is the mode switch: an empty list is single-node (unchanged),
a non-empty list starts `minio server <ep1> <ep2> ...` in distributed mode.

Each endpoint is `http://<host>:<port><path>`. The `host:port` is how the nodes
reach each other to form the ring. The trailing `<path>` is **not** an HTTP
route — clients never see it — it is the **export path**, the directory *inside
the container* where that node keeps its own slice of the erasure-coded data.
pgcli always bind-mounts the `--data-dir` at `/data`, so the path must start at
`/data` — the simplest choice is literally `/data`. Keep the same path string
across all four endpoints.

Note the endpoint path is a *container* path, independent of the host layout:
if `/data` is a shared disk and you want to keep other things on it, point
`--data-dir` at a subdirectory of it — `--data-dir /data/minio` stores this
cluster's data under the host's `/data/minio` while the endpoints stay
`http://<host>:9000/data` (the endpoint cannot name that subdirectory; it sees
the mount root). Only if you deliberately extend into the volume with a path
below `/data` (e.g. `/data/mystore`) does the data land one level deeper on the
host: `<data-dir>/mystore` — avoid pairing `--data-dir /data/minio` with
endpoint `.../data/minio`, which nests as `/data/minio/minio`.

```bash
# on node 1 (10.0.0.11), with a dedicated data disk mounted at /data:
pg addon install minio --name store \
  --listen 10.0.0.11 \
  --data-dir /data \
  --root-password '<shared-secret>' \
  --endpoint http://10.0.0.11:9000/data \
  --endpoint http://10.0.0.12:9000/data \
  --endpoint http://10.0.0.20:9000/data \
  --endpoint http://10.0.0.21:9000/data

# nodes 2-4: same command, own --listen, and the SAME --endpoint list AND the
# SAME --root-password value.
```

`--root-password` is optional: omit it and the first install generates one
(printed once, stored in `pg.yaml`). In cluster mode, pass the same value on
every node so the stored credential is identical across the cluster without
any manual copy.

The install summary lists the members and reminds you of the consistency
requirement:

```
✓ minio installed: "store"
  ...
  Distributed mode: 4 endpoints
    - http://10.0.0.11:9000/data
    - http://10.0.0.12:9000/data
    - http://10.0.0.20:9000/data
    - http://10.0.0.21:9000/data
  NOTE: every node's pg.yaml must carry the identical endpoint list AND
  identical root credentials, or the cluster will not form.
```

Three things are strict in cluster mode, each learned the hard way:

- **Endpoints must be routable, distinct hosts.** Same-host folds into
  single-node-multi-drive and is rejected (`use path style endpoint for
  single node setup`); the `127.0.0.0/8` loopback range is rejected outright
  (`resolves to localhost`). Use each node's real LAN address.
- **The data directory must be on a disk separate from the root filesystem.**
  MinIO refuses a drive that shares the OS disk (`drive is part of root
  drive, will not be used`). pgcli detects this with a `stat` of the data dir
  versus `/` and warns at install time when `--data-dir` is on the root device
  — the warning is advisory; point `--data-dir` at a mounted data disk.
- **`MINIO_SERVER_URL` is not set in cluster mode.** It would differ per node
  (each advertises its own IP), and MinIO requires every `MINIO_*` env var to
  be byte-identical across nodes, so a per-node `MINIO_SERVER_URL` makes each
  node loop on `Waiting for at least 1 remote servers with valid
  configuration`. The endpoint list defines the addresses instead. Single-node
  still sets `MINIO_SERVER_URL` from `listen`.

**Quorum (EC):** writes need `⌈N/2⌉+1` nodes up, reads need `⌈N/2⌉`. A 4-node
set therefore keeps serving reads with 2 nodes down but rejects writes; restart
the downed nodes and the cluster self-heals.

> **Cross-host only.** This is a genuinely distributed deployment — you run
> pgcli on each host yourself. pgcli does not SSH between nodes or register
> members; keeping the N `pg.yaml` files consistent is the operator's job.

## Using the mc Client

`pg mc` runs MinIO's own `mc` client from a throwaway container — no local
install, no manual `podman run` invocation:

```bash
# pick the URL for your platform — see "Endpoints by platform" below:
pg mc alias set store http://127.0.0.1:9000 admin <password>    # Linux, local addon
pg mc alias set store http://host.containers.internal:9000 admin <password>  # macOS, local addon
pg mc alias set store http://10.0.5.7:9000 admin <password>     # either, remote store
pg mc mb store/backups
pg mc ls store
pg mc cp ./dump.pglz store/backups/
```

Aliases persist on the host at `~/.mc/config.json` — mc's own default path,
the same on Linux and macOS — so `alias set` once and every later `pg mc`
invocation (and a native `mc` install, on Linux) sees the same aliases.
`pg mc alias list` reads them back and `pg mc alias remove` drops one.
`alias set` validates the credentials against the endpoint before writing, so a
wrong password leaves `~/.mc/config.json` untouched rather than storing a dead
alias. Under the hood `pg mc` mounts your `~/.mc` into the container and runs
`ghcr.io/mars-base/pgcli/pgcli-mc` (the static upstream binary on `scratch`,
pulled on first use); nothing else about it is magic.

Local file operands of `cp` / `mirror` / `diff` work too: `pg mc` resolves each
one (like `realpath`) and mounts that exact absolute path into the container, so

```bash
pg mc cp ./dump.pglz store/backups/        # upload a file from the cwd
pg mc cp store/backups/dump.pglz ./        # download into the cwd
pg mc mirror ./repo store/backups/         # or mirror a whole tree
```

just does what it looks like. A download target that does not exist yet (a new
filename, or a `./newdir/` not created) is handled too — its nearest existing
parent directory is what gets mounted, so the file mc writes lands on the host.
On macOS the path must live under your home directory — that is the only tree
the `podman machine` VM shares; `pg mc` says so and skips any mount outside it.

Any `mc` flag that `pg`'s own flag parser would reject goes after `--`, exactly
like `pg etcdctl` — this covers essentially every `mc` long flag, since none are
registered on `pg`: `--all`, `--json`, `--recursive`, `--force`, `--newer-than`,
and so on. Put them all after `--`:

```bash
pg mc ls store -- --all
pg mc cp ./dir store/backups/ -- --recursive
pg mc rm store/old -- --recursive --force
```

Passing one before `--` fails in `pg`, not in `mc`, with a message like
`unknown flag: --recursive` — a giveaway that the separator is missing.

One caveat from native mc is worth knowing, because it interacts with the local
mounts above: in a file command (`cp` / `mirror` / `diff`) an operand whose
first segment is not a known alias is treated as a local path. A typo in an
alias name therefore uploads or downloads to a local directory named after the
typo instead of erroring — `pg mc` faithfully mounts what mc decides to read, so
the behaviour is native, not a pg mc quirk. Confirm the alias with `pg mc alias
list` before a file operation.

`MC_HOST_<name>` environment aliases — mc's stateless form, no config file
touched — work through `pg mc` too, since the variable is forwarded into the
container:

```bash
MC_HOST_store="http://admin:<password>@127.0.0.1:9000" pg mc ls store
```

> **Endpoints by platform:** on Linux `pg mc` runs on the host network, so a
> `127.0.0.1` alias reaches a local addon instance directly. On macOS the
> container sits on the bridge network, where `127.0.0.1` is the container's
> own loopback — point an alias at a local Mac addon via
> `http://admin:<password>@host.containers.internal:9000`, or at a remote store
> via its routable address. `pg mc` itself works identically on both.
>
> A Mac browser reaches the console on `127.0.0.1:<console-port>` (the addon
> publishes it); that is host-side and unrelated to what the `pg mc` container
> can address.

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
      # endpoints:                 # omit for single-node; see "Distributed / Cluster Mode"
      #   - http://10.0.0.11:9000/data
      #   - http://10.0.0.12:9000/data
      #   - http://10.0.0.20:9000/data
      #   - http://10.0.0.21:9000/data
```

Edits to `listen`, ports, `root_user`, `root_password`, `image_tag`, `data_dir`,
or `endpoints` take effect after the next `pg addon install minio --name store
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
  address clients can actually reach. (Cluster mode does not set
  `MINIO_SERVER_URL` at all — see [Distributed / Cluster
  Mode](#distributed--cluster-mode).)
- **Cluster stuck on `Waiting for at least 1 remote servers with valid
  configuration`.** The nodes disagree. Check that every `pg.yaml` has the
  byte-identical `endpoints` list and `root_password` (`podman inspect
  pgcli-minio-<ns>-<name> --format '{{json .Config.Env}}'` on each node), and
  that no per-node `MINIO_SERVER_URL` crept back in.
- **macOS.** Supported: the addon serves on the `pgcli-net` bridge with both
  ports published, so the Mac's `127.0.0.1:<port>` reaches them (the container
  binds `0.0.0.0` internally and `MINIO_SERVER_URL` advertises the loopback the
  Mac uses). After changing ports or credentials, `--force` recreate the
  container. For `pg mc`, the container cannot use a `127.0.0.1` alias — see
  the endpoint note above.
