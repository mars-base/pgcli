---
title: "BeeGFS"
description: "Run a BeeGFS parallel filesystem as a managed addon — pgcli's first multi-container service (mgmtd + meta + storage)."
weight: 58
---

`pg addon install beegfs` runs [BeeGFS](https://www.beegfs.io/) — the
scale-out parallel filesystem — as a managed addon. Unlike every other addon in
pgcli (one container each), a BeeGFS *filesystem* is a **triad of containers**
that come up in dependency order:

```
pgcli-beegfs-<name>-mgmtd     management daemon — the node/target registry
pgcli-beegfs-<name>-meta      metadata service   — one meta target
pgcli-beegfs-<name>-storage   storage service    — one storage target
```

This is v1: a **converged single-host filesystem** — all three services on one
host, one config entry (`addons.beegfs.<name>`), one shared connection-auth
secret, brought up and torn down as a unit. It is a real, mountable BeeGFS
backend; it is not yet the multi-host HPC cluster (that is v2, see [Known
limitations](#known-limitations)).

> **Platform**: Linux only. BeeGFS daemons run on **host networking** (BeeGFS
> speaks its own TCP/UDP protocol, not HTTP, so there is no container
> port-mapping to hide behind). macOS has no host networking under podman, so
> `pg addon install beegfs` is refused there with a clear error.

> **The client is out of scope, on purpose.** Mounting a BeeGFS filesystem
> needs the `beegfs-client` **kernel module** (DKMS). Kernel modules cannot be
> containerised, so pgcli neither installs nor mounts it — it manages the three
> *server* containers only. `pg addon beegfs mount-hint` prints the exact
> host-side config a machine with the `beegfs-client` + `beegfs-utils` packages
> needs. See [Mounting a client](#mounting-a-client).

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `beegfs` | Filesystem name (map key); the three containers are `<prefix>-{mgmtd,meta,storage}` |
| `--image` | `8.4.0` | Version suffix — the manager builds `ghcr.io/thinkparq/beegfs-{mgmtd,meta,storage}:<version>` from it (all three share one tag) |
| `--data-dir` | `<base>/addon/beegfs/<name>` | **One** dir for the whole filesystem; its services use `<dir>/{mgmtd,meta,storage}` |
| `--listen` | `0.0.0.0` | The address clients self-reference as `sysMgmtdHost`; with host networking the daemons actually bind every interface |
| `--password` | generated | The connection-auth secret (`ConnAuth`) shared by all three daemons and every client; generated on first install if unset |
| `--beegfs-mgmtd-port` | `8008` | Move mgmtd's BeeMsg port (the *only* movable meta/storage registration target); `0` = stock |
| `--beegfs-mgmtd-grpc-port` | `8010` | Move mgmtd's gRPC port (what the `beegfs` CLI dials); `0` = stock |
| `--force` | — | Recreate all three containers even if the cluster is already running |

There is deliberately **no `--port` for meta or storage**: see [Ports](#ports).

## Install

```bash
pg addon install beegfs              # default filesystem named "beegfs"
pg addon install beegfs --name fs1
pg addon install beegfs --listen 10.0.0.5   # advertise a reachable mgmtd address
```

The install is fully non-interactive. It pulls the three images up front (so a
mid-cluster network failure never leaves a half-registered filesystem), then
brings the cluster up **in order**: mgmtd first, meta next (blocked until it
logs `Registration and management info download complete` — that line proves
both "mgmtd is serving" and "this service joined"), then storage the same way.
Each container's first boot runs a `beegfs-setup-<service>` step (driven by a
`beegfs_setup_1` env var the image's entrypoint consumes) that initialises its
target directory; the marker is idempotent, so a recreated container against a
surviving data dir reuses the existing target.

Re-running `install` is safe and **cluster-granular**:

- all three up → skip (use `--force` to recreate, or `pg addon beegfs check`);
- all three down and present → just start the stored cluster (no pull, no
  recreate);
- anything else (partial, or a fresh install) → `EnsureCluster` recreates the
  whole set in order.

The install summary lists the three container names, the image, mgmtd's two
ports, the three data dirs, the conn-auth state, and a pointer to
`pg addon beegfs mount-hint`. If `--listen` is the wildcard `0.0.0.0` it prints
an exposure NOTE (a parallel filesystem with TLS off is reachable from any
machine that holds the conn-auth secret).

## The data directories and persistence

One filesystem owns **three** host directories, bind-mounted each into its own
container at `/data/<role>`:

```
<base>/addon/beegfs/<name>/mgmtd     mgmtd.sqlite — the node/target registry
<base>/addon/beegfs/<name>/meta      the metadata target
<base>/addon/beegfs/<name>/storage   the storage target (the actual file data)
```

`--data-dir <dir>` lays all three out as `<dir>/{mgmtd,meta,storage}` in one
flag — operators never address the roles separately. The three images run as
**root** and own their data dirs, so unlike Gitea (uid 1000) or Grafana
(uid 472) there is **no pre-`chown` dance** — the host dirs are writable as-is.

All data survives container removal: `pg addon remove beegfs` without
`--clean-data` keeps the three dirs, and reinstalling the same name **revives**
the filesystem (the install prints a NOTE when it detects an existing
`mgmtd.sqlite`). Revival keeps whatever registry and connection auth the store
already had — a new `--password` only reaches freshly-initialised daemons, not
a surviving SQLite registry.

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

The three daemons and every client share one **connection-authentication
secret** (`ConnAuth`, delivered as the `CONN_AUTH_FILE_DATA` env var that the
image's init writes to `/etc/beegfs/conn.auth`). It is generated on first
install (20 random chars), printed once, and stored in `pg.yaml` — retrieve it
with `pg addon password beegfs`. Like every pgcli secret it is never defaulted
into the config by `ApplyDefaults`.

**TLS is disabled on mgmtd** (`--tls-disable true`, the explicit v1 decision).
This means only clients that pass `--tls-disable` too can join, and it is a
real exposure consideration: a wildcard `--listen 0.0.0.0` with TLS off leaves
ports 8008/8010/8005/8003 open to any machine on the network that holds the
conn-auth secret. For anything not on a private network, either bind loopback
(`--listen 127.0.0.1`) or firewall those four ports.

**Licensing**: a BeeGFS license is a system-specific registration you obtain
from ThinkParQ — **pgcli never generates or injects one** (same principle as
Gitea's crypto secrets). An unlicensed 8.4 management daemon logs a quota
warning at registration time but the filesystem still functions for
community-scale use. Mirroring, pools, and quota are enterprise features that
need a license and are outside pgcli's v1 wiring.

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
3. /etc/beegfs/beegfs-mounts.conf (one mount per line):
     /mnt/beegfs fs1@<mgmtd-host>:/ _sysName=<client-name>
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

`mount-hint` is pure local rendering — it never touches podman, since the client
is out of pgcli's management. When `--listen` is the wildcard, it prints
`<mgmtd-host-ip>` as a placeholder (0.0.0.0 is not dial-able) rather than a
value clients cannot connect to.

## Health check

```bash
pg addon beegfs check             # default name
pg addon beegfs check --name fs1
```

Reports the aggregate cluster state (`running` / `stopped` /
`partial (mgmtd,meta)`) and, per service, whether its container is up and
whether it logged its readiness marker — mgmtd `Waiting for shutdown signal`
(serving), meta/storage `Registration and management info download complete`
(joined). None of the three images ships `beegfs-ctl`/`check-servers`, so the
daemons' own logs are the authoritative *in-container* signal; for a mounted
client's view use the `beegfs` gRPC CLI above. `check` also works on a stopped
cluster (it reports state, it does not require running).

## Retrieving the connection-auth secret

```bash
pg addon password beegfs             # the "beegfs" filesystem
pg addon password beegfs --name fs1
```

`pg addon list` shows only a presence marker (`Auth: none` / `set
(connection-auth)`) and prints the secret itself only with `--show-password`, so
it never leaks to a casual listing.

## List

```bash
pg addon list
```

The BeeGFS block ("Infra add-ons (beegfs)") prints one entry per filesystem
with its aggregate cluster state, mgmtd's two ports, the fixed meta/storage
ports, conn-auth presence, image, the three data dirs, and the three container
names. When podman is unavailable it still lists the configured entries (state
shown as `n/a`).

## Start and stop

Containers run with podman's `unless-stopped` restart policy, so they come back
with the podman service after a host reboot. `start`/`stop` manage the whole
cluster explicitly, respecting dependency order:

```bash
pg addon stop beegfs     # storage → meta → mgmtd (reverse order, targets stay GOOD)
pg addon start beegfs    # mgmtd → meta → storage
```

## Auto-start on Boot

```bash
pg autostart enable --beegfs --name fs1
```

BeeGFS is the **only cluster-granular autostart**: `pg start --autostart` brings
up the whole filesystem (mgmtd → meta → storage in order), not one container.
Because the images run as root and own their dirs, an autostart needs no host
privileges beyond podman itself (no chown step like Gitea's). The client on
each mounting host re-attaches on its own via `beegfs-mounts.conf` — pgcli
never manages that. See [Autostart]({{< relref "../autostart" >}}) for the full
mechanism, status, and disabling.

## Remove

```bash
pg addon remove beegfs                # keeps the three data dirs — reinstall revives the filesystem
pg addon remove beegfs --clean-data   # deletes all three dirs too (the data is gone)
```

Remove tears down in reverse order (storage → meta → mgmtd). `--clean-data`
refuses to delete a directory that is still a mount point. **A client mountpoint
pointing at this host must be unmounted either way** — pgcli cannot reach the
client (it is not pgcli's), so unmount it on the client host first.

## Logs

Three containers, so `pg logs addon beegfs` takes `--service` to pick one
(default `mgmtd`):

```bash
pg logs addon beegfs                       # mgmtd (default)
pg logs addon beegfs --service meta -f     # follow the metadata daemon
pg logs addon beegfs --service storage -n 200
```

`--service` is only valid for `beegfs` (a value like `meta` against another
addon type is rejected).

## Troubleshooting

- **`install beegfs --name fs2` refuses with "a BeeGFS filesystem already
  exists on this host"** — this is v1's one-filesystem-per-host rule, not a
  bug: meta (8005) and storage (8003) cannot move their listen ports in 8.4, so
  a second filesystem on the same host could never bind them. Use a separate
  host (that is v2's cross-host direction), or `pg addon remove` the first one.
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
- **Clone/mount to `localhost` fails from another machine** — a wildcard
  `--listen 0.0.0.0` renders `sysMgmtdHost` as a placeholder in `mount-hint`;
  install with `--listen <reachable-ip>` (with `--force`) so clients get a real
  mgmtd address.

## Known limitations

- **Linux only.** Host networking is mandatory and podman provides it on Linux
  only; installation is refused on macOS. No bridge fallback exists because
  BeeGFS is not HTTP.
- **One filesystem per host, fixed ports.** v1 pins the stock ports
  (8008/8010/8005/8003); meta and storage have no movable listen port in 8.4,
  so a single host holds a single filesystem.
- **The client is never installed or mounted by pgcli.** It is a kernel module
  (DKMS) that cannot be containerised; pgcli manages only the three server
  containers and *documents* the mount via `mount-hint`.
- **TLS off, no license wiring.** mgmtd runs `--tls-disable true`; the `beegfs`
  gRPC CLI and clients must match. Licenses, mirroring, pools, and quota are
  not pgcli's concern — enterprise features need a ThinkParQ registration
  obtained out of band.
- **v1 is single-host (converged).** The real multi-host HPC cluster — meta and
  storage spread across hosts with SSH orchestration like Patroni's — is the
  planned **v2** and is not implemented here.
