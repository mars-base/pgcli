---
title: "Garage"
description: "Run Garage (the Rust S3-compatible object store from Deuxfleurs) as a pgcli addon — single-node, plaintext HTTP, TLS terminated by fronting it with the nginx addon"
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
2. **Two data directories, not one.** `metadata_dir` (LMDB) and `data_dir`
   (blocks) are separate bind mounts.
3. **Four ports, from their own pool.** S3 API, node-to-node RPC, the `s3_web`
   static site, and the admin/metrics API — allocated together per instance
   from `garage_start_port` (default **3900**), a separate cursor from the
   shared minio/silo/rustfs pool.
4. **No native TLS — plaintext HTTP only.** pgBackRest's S3 repository forces
   HTTPS, so a Garage store meant to receive `archive-push` must be **fronted by
   the [nginx addon](../nginx/)** — see [TLS by fronting with
   nginx](#tls-by-fronting-with-nginx).

Like rustfs, Garage is a good successor route now that the open-source MinIO
repository is archived. v1 is **single-node** (`replication_factor = 1`,
`garage server --single-node`); multi-node and cross-zone layouts are not yet
managed by pgcli.

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
  same way and stored alongside. It never surfaces in the CLI.

All three land in `pg.yaml` (`addons.garage.<name>.*`) so a recreate is stable.
On first boot the container is passed `--single-node --default-bucket` plus the
`GARAGE_DEFAULT_ACCESS_KEY` / `GARAGE_DEFAULT_SECRET_KEY` /
`GARAGE_DEFAULT_BUCKET` env, so Garage itself creates the access key and one
bucket (named after the instance, owned by that key) — no post-install exec
needed. The key and bucket live in `metadata_dir`, so they survive container
recreation.

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
pg addon install garage --name store --root-user GK<...> --root-password <secret>
```

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
        listen 127.0.0.1:8081 ssl;      # the HTTPS port pgcli assigned
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
Garage's status API.

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
      replication_factor: 1           # v1 is single-node only; >1 is rejected
      autostart: false                # pg autostart enable --garage --name store
```

Edits to `listen`, ports, `root_user`, `root_password`, `image_tag`, `data_dir`,
or `config_file` take effect after the next `pg addon install garage --name
store --force`. A hand-set `replication_factor: 2` is rejected at install —
multi-node is out of scope for v1.

### List

```bash
pg addon list
```

```
Infra add-ons (garage):
  garage (name: store)
    Status:      running
    Listen:      127.0.0.1
    API port:    3900
    RPC/Web/Admin: 3901/3902/3903
    S3 URL:      http://127.0.0.1:3900
    Data:        ~/pg/addon/garage/store/data
    Metadata:    ~/pg/addon/garage/store/meta
    Access key:  GK<generated>
    Bucket:      store
    Image:       docker.io/dxflrs/garage:v2.4.1
    Container:   pgcli-garage-default-store
    NOTE:        plaintext HTTP only — front with the nginx addon for TLS
```

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
- **`replication_factor` must be 1.** v1 is single-node; a hand-edited
  `replication_factor: 2` is rejected at install. Multi-node Garage is not yet
  managed by pgcli.
