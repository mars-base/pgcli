---
title: "Silo"
description: "Run silo (Pigsty's MinIO fork) as a pgcli addon — single-node or distributed S3-compatible object storage with web console"
weight: 48
---

[silo](https://silo.pgsty.com) is Pigsty's maintained fork of MinIO: S3-compatible
object storage with the web console bundled in, keeping MinIO's wire contract
end to end — the S3 API, the `MINIO_*` environment variables, the
`server /data --address :9000 --console-address :9001` command line, the
`--certs-dir` layout, and erasure-coded cluster mode. pgcli runs it as a
**standalone, top-level addon** with exactly the same surface as the
[`minio` addon](../minio/) — install, TLS, BYO certs, distributed mode, logs,
autostart. Pick silo over MinIO if you follow Pigsty's releases; the two can
coexist on one host (they share one port pool, assigned without collision).

> **Platform support:** the silo addon works on **both platforms**. Linux
> serves over host networking; macOS joins the `pgcli-net` bridge with its two
> ports published, so the Mac reaches the API and console on `127.0.0.1:<port>`
> like any other addon. The public image is dual-arch (amd64 + arm64). Both
> clients — [`pg mcli`](#using-the-mcli-client) (silo's own) and
> [`pg mc`](../minio/#using-the-mc-client) (MinIO's) — work on both platforms
> and against a silo store alike.

## How It Works

One container, one directory: `silo server /data --address <listen>:<api-port>
--console-address <listen>:<console-port>`, where `/data` is a bind mount of the
instance's host data directory (default `<base-dir>/addon/silo/<name>/data`).
Everything silo stores lives there, so it outlives the container.

The image is the public upstream tag `docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z`
— dual-arch, console included, and it ships the `mcli` client too — so `pg
addon install silo` just pulls it; pgcli never builds the image at run time
(unlike `pgcli-minio`, which is a self-maintained tag because MinIO's official
image dropped the console). pgcli drives the `silo` binary directly via an
explicit `--entrypoint silo`, bypassing the image's entrypoint wrapper, exactly
as it does for MinIO.

Credentials are handled like Patroni's:

- `root_user` defaults to `admin`;
- `root_password` is **generated on first install** (or supplied explicitly via
  `--root-password`) and stored in `pg.yaml`
  (`addons.silo.<name>.root_password`) and **printed once** in the install
  summary for convenience.

They are silo's root **access key / secret key** — passed through the
`MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` env vars silo inherits from MinIO —
what every S3 client (and `pg mcli alias set <name> <url> <root_user>
<root_password>`) calls the access key and secret key.

The container runs `--ulimit nofile=1048576:1048576` and `--stop-timeout 60` as
MinIO's deployment docs recommend (the contract carries over). On macOS the
`podman machine` VM caps `RLIMIT_NOFILE` lower, so the value used there is
`65536` — plenty for a single-host dev/test store. Root credentials passed as
`-e` are visible in `podman inspect` — the same exposure as any hand-run
container; fine for a rootless single-host deployment, which is what this is.

## Install

```bash
# default instance name "silo", ports from the shared pool (base 9000), loopback bind
pg addon install silo

# a named instance with an explicit data directory
pg addon install silo --name store --data-dir /srv/silo

# fixed ports and a different root user
pg addon install silo --name store --api-port 9000 --console-port 9001 --root-user admin

# expose the store on the network instead of loopback only
pg addon install silo --name store --listen 0.0.0.0

# HTTPS via pgcli's self-signed CA (the prerequisite for a pgBackRest S3 repo)
pg addon install silo --name store --tls

# HTTPS with a certificate you already have (public-CA or private-CA domain cert)
pg addon install silo --name store --tls-cert /etc/ssl/silo.test.crt --tls-key /etc/ssl/silo.test.key
```

The output reports endpoints and the root credentials:

```
✓ silo installed: "store"
  Container:    pgcli-silo-default-store
  Image:        docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z
  Data:         ~/pg/addon/silo/store/data
  S3 API:       http://127.0.0.1:9000
  Console:      http://127.0.0.1:9001

  Root user:     admin
  Root password: <generated>
```

Sign in to the console at the `Console:` URL with the printed root user and
password. Point S3 clients (including pgBackRest) at the `S3 API:` URL, or
drive them from the terminal with [`pg mcli`](#using-the-mcli-client) below.

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

`--tls` makes silo serve HTTPS. pgcli generates a self-signed CA and a leaf
cert with the stdlib — SANs cover the loopback names, `localhost`, and every NIC
IP of the host — into `<base_dir>/tls/silo/<name>/`: `public.crt` /
`private.key` for silo's `--certs-dir`, and `ca.crt` for distribution. The
cert dir is mounted read-only and the endpoint URL becomes `https://`.

Why you need it: **pgBackRest forces HTTPS for S3 repositories** (plaintext is
an upstream-rejected option), so a silo meant to receive Patroni `archive-push`
must speak TLS. Point `backup.repo.s3.ca_file` at `ca.crt` and pgBackRest
connects with full certificate verification — see
[Backup → S3 object storage repository](../../backup/#s3-object-storage-repository).
Everything that page says about a MinIO repo applies verbatim to a silo one:
the client contract is identical.

One note on pairing stores with clusters: `backup.repo.s3` is a single, global
repo per config file, so the *first* TLS store you wire up — minio or silo —
receives every stanza in that environment. If you truly need two destinations,
run a second config (see [Backup → Shared Backup
Container](../../backup/#shared-backup-container), and the worked
[Example: HA Cluster with a Self-CA MinIO](../../ha-cluster/ha-example-minio/)).

`pg mcli` adds `--insecure` automatically when a command targets a TLS store
via a loopback alias (mcli persists no CA trust per alias; same-host loopback
makes this acceptable). An alias to a LAN-IP endpoint is not loopback — append
`-- --insecure` yourself, or set up the CA properly. External `https://`
endpoints keep full verification. Toggling `--tls` on an existing instance
needs `--force` to take effect.

**Lifetimes and access from other hosts.** CA and leaf are both long-lived;
pgcli re-signs the leaf on `--force`, or whenever a host address changes (silo
watches its cert files and hot-reloads a re-signed pair, which is how a
deployment picks up the new chain without a recreate), so handing `ca.crt` to
clients is a one-time act per store. A TLS client validates the address it
*dials* against the leaf's SANs — the client's own address never matters.
Remote hosts therefore point `backup.repo.s3.endpoint` at one of the store
host's IPs (loopback names and every NIC IP, virtual bridges included, are in
the SAN; raw hostnames are not — unless you set `MINIO_SERVER_URL`, whose host
is added).

**Getting the CA onto a remote host — no scp needed.** The server cert is
served as a leaf+CA chain, so a consumer on another machine can pull the root
straight out of a TLS handshake:

```bash
pg backup fetch-ca <store-host>:9002
#   [OK] CA fetched from <store-host>:9002
#        saved:    ~/.pgcli/backup/repo-ca/ca-<store-host>-9002.crt
#        SHA-256:  c0f0…fe2e
pg backup setup --s3-ca-file ~/.pgcli/backup/repo-ca/ca-<store-host>-9002.crt
```

The fetch is trust-on-first-use — compare the printed SHA-256 against the
store host's `sha256sum ~/.pgcli/tls/silo/<name>/ca.crt` before trusting it.
One `setup` then republishes the CA into the Patroni cluster's etcd registry,
so every *other* cluster host gets it with no manual step at all (see
[Backup → S3 object storage repository](../../backup/)).

### Bring your own certificate (`--tls-cert` / `--tls-key`)

`--tls` only ever serves pgcli's own self-signed pair. To serve a certificate
you already hold — one signed by a public CA for a real domain, or one issued
by your private CA — pass `--tls-cert <leaf(+chain).pem> --tls-key <key.pem>`
instead. Together with `--tls` (which it implies, so you don't need to also
pass it), this replaces the generated certs: the two files are mounted
read-only straight at the names silo's `--certs-dir` requires
(`/opt/silo/certs/public.crt`, `/opt/silo/certs/private.key`), and pgcli never
copies or re-signs them — the private key stays in exactly the one place you
put it.

```bash
pg addon install silo --name store \
  --tls-cert /etc/ssl/wildcard.example.com.crt \
  --tls-key  /etc/ssl/wildcard.example.com.key
```

Everything else about BYO mode — what install checks, why renewal needs
`--force` (a single-file mount pins the source inode), how clients pick their
trust anchor, and `pg cert` as a test-cert mint — is identical to the MinIO
addon; see [MinIO → Bring your own certificate](../minio/). The
`pg cert` example, adapted:

```bash
pg cert --host "silo.test,127.0.0.1,10.0.0.9" \
  --cert-file silo.crt --key-file silo.key
pg addon install silo --name store --tls-cert silo.crt --tls-key silo.key
```

**Turning BYO off.** There is no off flag — the config merge across re-runs is
one-way, matching how `--tls` itself works. To go back to generated certs,
remove `cert_file`/`key_file` under this addon in `pg.yaml` and recreate with
`pg addon install silo --name store --force`.

## Deployment modes

silo/MinIO classifies its layouts; the addon supports all four:

| Mode | Shape | Use it for |
|------|-------|------------|
| **SNSD** (single-node, single-drive) | one node, one data directory — the default when no `--endpoint` or `--drive` is given | dev, test, demos |
| **SNMD** (single-node, multi-drive) | one node, several drives — `--drive` per drive, see [SNMD](#single-node-multi-drive-snmd) below | surviving a disk loss on a single host without a filesystem layer |
| **MNSD** (multi-node, single-drive) | several nodes, one data disk per node — the distributed mode below | compact high-availability deployments |
| **MNMD** (multi-node, multi-drive) | several nodes, several drives each — `--drive` for this node's drives + the full `--endpoint` matrix | surviving a disk loss *and* a node loss, without a filesystem layer |

SNSD is what `pg addon install silo` gives you out of the box. To get
MNSD, pass the cluster's endpoint list (at least four nodes) — see
[Distributed / Cluster Mode](#distributed--cluster-mode) below.

MNMD combines both flags: give this node's drives with `--drive` (as under
SNMD) and the *whole cluster's* host×drive endpoint matrix with `--endpoint`
(one URL per drive on every node) — see
[Multi-Node Multi-Drive (MNMD)](#multi-node-multi-drive-mnmd) below. Disk
redundancy under SNSD/MNSD is also available the other way — a ZFS pool
under `--data-dir` — which keeps the layout changeable underneath and can be
more space-efficient. [S3 Storage High
Availability](../../ha-cluster/ha-s3-storage/) compares native MNMD with the
ZFS approach for the 4-hosts-each-with-several-disks hybrid that survives
both a disk and a node.

## Single-Node Multi-Drive (SNMD)

One silo process, several host directories, erasure-coded across them. Pass
`--drive` once per drive instead of `--data-dir`:

```bash
pg addon install silo --name store \
  --drive /mnt/minio/disk1 --drive /mnt/minio/disk2 \
  --drive /mnt/minio/disk3 --drive /mnt/minio/disk4 --tls
```

Each `--drive` is a host directory on its own device; drive *N* is
bind-mounted at container path `/dataN` and the server starts as
`silo server /data1 /data2 ... /dataN`. `--drive` is mutually exclusive with
`--data-dir` — multi-drive mode takes its data locations from `--drive` only.
Combining `--drive` with `--endpoint` is MNMD, the multi-node version of this
mode — see [Multi-Node Multi-Drive (MNMD)](#multi-node-multi-drive-mnmd)
below. Like every other silo/MinIO drive, one that shares the host's root
device is rejected at startup; pgcli lists the offending drives and warns at
install time.

**What EC buys you** (measured on a live 4-drive silo set): a 4-drive set
defaults to 2 parity shards — it tolerates 2 drive failures. With 1 drive
down, reads and writes both continue; with 2 down, reads still succeed and
writes are refused — that is the quorum boundary, the same arithmetic MNSD
uses, just over drives instead of nodes. Usable capacity is roughly half the
raw total. A drive that returns is healed by silo itself; pgcli does not need
to do anything. The parity default scales with drive count — only the 4-drive
shape is tested here.

`pg addon remove silo --name store --clean-data` deletes each drive
directory — but refuses any drive that is still a mount point, so an
accidental `--clean-data` can never `rm -rf` through a live mount into the
disk below. Unmount first if the data below is really disposable.

## Distributed / Cluster Mode

silo's erasure-coded (EC) cluster mode works exactly like MinIO's — same
command shape, same rules. It requires **at least four distinct `host:port`
endpoints** and, unlike the other addons, **no central coordinator**: every node
runs its own pgcli with its own `pg.yaml`, and every one of those `pg.yaml`
files carries the *same* full endpoint list and the *same* root credentials.
pgcli only ever starts the container for the node it is running on — silo
itself does the handshake to form the ring across hosts.

```bash
# on node 1 (10.0.0.11), with a dedicated data disk mounted at /data:
pg addon install silo --name store \
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

The three strict rules (routable distinct hosts, a data directory on a disk
separate from root — pgcli warns when it isn't — and no per-node
`MINIO_SERVER_URL` in cluster mode) and the quorum arithmetic are the same as
[MinIO → Distributed / Cluster Mode](../minio/#distributed--cluster-mode);
silo enforces the same checks because it inherited them.

## Multi-Node Multi-Drive (MNMD)

MNMD works exactly like it does under MinIO: give this node's drives with
`--drive` and the *whole cluster's* host×drive endpoint matrix with
`--endpoint`, one URL per drive on every node. Each endpoint must address one
of that node's `/data1../dataN` drive slots, the matrix must be a multiple of
the per-node drive count and must name at least one remote node, and a `--tls`
node's endpoints must all be `https://` — pgcli checks all four before starting
the container. See
[MinIO → Multi-Node Multi-Drive (MNMD)](../minio/#multi-node-multi-drive-mnmd)
for the full walkthrough.

On a live 4-node × 4-drive silo set (16 × 2 GiB) the measured shape matches
MinIO's: **16 drives online, EC:4** in a single erasure set of stripe size 16;
losing **one whole node** (12/16 online) keeps reads *and* writes working, a
64 MiB round-trip byte-identical; losing a **second node** (8/16 online)
refuses writes (`Resource requested is unwritable`) and fails reads too, and
the set self-heals back to 16/16 once the nodes restart. EC:4 over 16 drives
keeps 12/16 of raw bytes.

## Using the mcli Client

`pg mcli` runs silo's own `mcli` client from the pgsty/silo image in a
throwaway container (selected via `--entrypoint mcli` — the image's default
entrypoint runs the server) — no local install:

```bash
# pick the URL for your platform — see "Endpoints by platform" in the mc page:
pg mcli alias set store http://127.0.0.1:9000 admin <password>    # Linux, local addon
pg mcli alias set store http://host.containers.internal:9000 admin <password>  # macOS, local addon
pg mcli mb store/backups
pg mcli ls store
pg mcli cp ./dump.pglz store/backups/
```

mcli speaks the same alias contract as mc, so it interoperates freely —
against a silo store, a MinIO store, or any S3 endpoint. pgcli points both
clients at **one** file: aliases persist on the host at `~/.mc/config.json`
(mc's native default path), so an alias registered with `pg mc` is visible to
`pg mcli` and vice versa — set it once, not once per client. (The stateless
`MC_HOST_<name>` form works for both too.) A native mcli install left to its
own defaults reads `~/.mcli/config.json` instead, so it will not see the
shared file unless `MC_CONFIG_DIR` is pointed at it.

`alias set` validates the credentials against the endpoint before writing, so a
wrong password leaves the config untouched. Local file operands of `cp` /
`mirror` / `diff` are resolved and mounted at their real absolute paths, exactly
as `pg mc` does (on macOS, keep them under your home directory). Any `mcli`
flag that `pg`'s own parser would reject goes after `--`:

```bash
pg mcli ls store -- --all
MC_HOST_store="http://admin:<password>@127.0.0.1:9000" pg mcli ls store
```

Both clients share the same command table as documented on the MinIO
page — [Common commands](../minio/#common-commands) — with `pg mcli` in place
of `pg mc`.

## Ports

Each instance takes **two consecutive ports** from one pool, starting at
`minio_start_port` (default **9000**): the S3 API first, the console second.
**The pool is shared with the minio addon** — minio and silo instances are
assigned from one cursor, so they coexist on a host without collision,
minio instances first (by name), then silo:

```bash
pg addon install minio --name store    # 9000 / 9001
pg addon install silo  --name lake     # 9002 / 9003
```

## Configuration

Instances live under the top-level `addons.silo` map in `pg.yaml`, keyed by
instance name:

```yaml
namespace: default
minio_start_port: 9000       # shared pool: minio AND silo draw from this
addons:
  silo:
    store:
      container_name: pgcli-silo-default-store
      name: store
      image_tag: docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z
      # data_dir: /srv/silo     # omit for <base-dir>/addon/silo/store/data
      # drives:                  # multi-drive (SNMD/MNMD): one host dir per drive,
      #   - /mnt/minio/disk1     # each mounted at its own /dataN — see SNMD/MNMD below
      #   - /mnt/minio/disk2
      listen: 127.0.0.1
      api_port: 9000
      console_port: 9001
      root_user: admin
      root_password: <generated>   # written on first install
      autostart: false             # pg autostart enable --silo --name store
      # tls: true                  # serve HTTPS (self-signed CA, or BYO below)
      # cert_file: /etc/ssl/silo.test.crt   # BYO leaf(+chain), implies tls
      # key_file:  /etc/ssl/silo.test.key   # BYO private key, must pair with cert_file
      # endpoints:                 # omit for single-node; see "Distributed / Cluster Mode"
      #   - http://10.0.0.11:9000/data
      #   - http://10.0.0.12:9000/data
      #   - http://10.0.0.20:9000/data
      #   - http://10.0.0.21:9000/data
      #   (with `drives` also set this is MNMD — one /dataN endpoint per drive on
      #    every node, and every node's list identical; see "Multi-Node Multi-Drive")
```

Edits to `listen`, ports, `root_user`, `root_password`, `image_tag`, `data_dir`,
`tls`/`cert_file`/`key_file`, or `endpoints` take effect after the next
`pg addon install silo --name store --force` — the plain install skips a
still-present container, `--force` recreates it (the data directory is never
touched).

### List

```bash
pg addon list
```

```
Infra add-ons (silo):
  silo (name: store)
    Status:      running
    Listen:      127.0.0.1
    API port:    9000
    Console port: 9001
    Console URL: http://127.0.0.1:9001/
    Data:        ~/pg/addon/silo/store/data
    Root user:   admin
    Image:       docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z
    Container:   pgcli-silo-default-store
```

`pg addon list` never prints the password — read it from `pg.yaml`.

## Start and stop

After a host reboot, bring an instance back without re-applying config:

```bash
pg addon start silo --name store
pg addon stop  silo --name store
```

`install` skips a still-present container (starting it if stopped); `start` only
starts an existing one (and self-heals an improper state by recreating it from
the config). In TLS generated mode `start` also re-validates and, if needed,
re-signs the leaf (silo hot-reloads its mounted cert).

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring instances up after a host reboot:

```bash
pg autostart enable --silo --name store
```

This sets `autostart: true` and installs/refreshes the boot service (see
[Auto-start on Boot](/docs/autostart/)). Boot is **start-only**. silo is
independent of the PostgreSQL stack, so it is started last; there is no
ordering constraint. `pg autostart status` lists every target's state.

## Remove

```bash
pg addon remove silo --name store            # container gone, data kept
pg addon remove silo --name store --clean-data   # also delete the data directory
```

The data directory **is the object storage** — losing it means losing every
bucket in it — so `remove` keeps it by default and says where it is.
`--clean-data` deletes it (and prunes the now-empty default-layout parent
directory; a `data_dir` override and its parent are never touched).

## Logs

```bash
pg logs addon silo --name store      # last 50 lines
pg logs addon silo --name store -f   # follow
```

silo logs to stdout: startup lines (`API:`/`Console:` addresses,
`Documentation:`) and request errors. An `API: http://...` block confirms the
listeners came up.

## Troubleshooting

- **`pulling silo image ... : ...` on install.** The public tag
  `docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z` is not reachable — check
  network/registry access to docker.io, or pre-pull it with `podman pull`.
- **Port collision on manual `--api-port`.** Pick ports outside the auto pool
  (`minio_start_port` and up) or the auto-assigner will treat them as taken;
  the pool is shared with the minio addon, so a silo instance must also dodge
  the minio instances' explicit ports and vice versa. Remember each store needs
  a consecutive pair.
- **`pg addon start` after host reboot does nothing / fails.** Check
  `pg logs addon silo --name store -f` — most often the data directory was
  deleted (with `--clean-data` or by hand) and silo refuses to start on an
  empty dir it previously formatted, or the bind port moved.
- **Console reachable but S3 clients time out.** The `MINIO_SERVER_URL` is
  built from `listen` + API port; if you serve on `127.0.0.1` but access from
  another host, clients get redirected to the loopback URL. Set `listen` to the
  address clients can actually reach. (Cluster mode does not set
  `MINIO_SERVER_URL` at all.)
- **An alias set with `pg mc` isn't visible to `pg mcli` (or vice versa).**
  They share `~/.mc/config.json` by design, so this means the alias really
  isn't there — check `pg mc alias list` (same file), or pass
  `MC_HOST_<name>` in the environment. A *native* mcli reads `~/.mcli`
  instead and won't see the shared file unless `MC_CONFIG_DIR` points there.
- **macOS.** Supported: the addon serves on the `pgcli-net` bridge with both
  ports published, so the Mac's `127.0.0.1:<port>` reaches them (the container
  binds `0.0.0.0` internally and `MINIO_SERVER_URL` advertises the loopback the
  Mac uses). After changing ports or credentials, `--force` recreate the
  container. For `pg mcli`, the container cannot use a `127.0.0.1` alias — see
  the endpoint note on the MinIO mc page.
