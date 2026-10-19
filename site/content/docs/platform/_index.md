---
title: Platform Support
description: Which pgcli components and features work on Linux vs macOS
weight: 8
icon: fa-solid fa-desktop
menus:
  main:
    identifier: docs-platform
    parent: docs
    weight: 8
    params:
      icon: fa-solid fa-desktop
---

pgcli drives [Podman](https://podman.io) containers on both Linux and macOS.
The difference is **how containers get networking**:

- **Linux** runs Podman natively, so containers share the host network stack
  (`--network host`) and reach each other over `127.0.0.1`. This is the
  zero-overhead path and the only one that supports **cross-host** topologies.
- **macOS** runs Podman inside a `podman machine` VM. Host networking there
  binds to the *VM's* loopback, which the Mac cannot see, so pgcli instead
  joins a shared bridge network (`pgcli-net`) and **publishes** each port — the
  Mac reaches it on `127.0.0.1:<port>` via gvproxy, and containers reach each
  other by **container name** over the bridge. This works for a **single host**
  (dev/test); it is not the cross-host HA path.

The table below is the current support matrix. "macOS (single-host)" means
fully usable for one machine; features that must advertise a routable address
across machines, or that depend on Linux-only container internals, stay
**Linux only**.

## Support matrix

| Component / feature | Linux | macOS |
|---------------------|:-----:|:-----:|
| Instance lifecycle — `pg create` / `start` / `stop` / `restart` / `destroy` / `status` | ✅ | ✅ single-host |
| `pg psql` / `pg exec` | ✅ | ✅ |
| SQL command reference (`pg exec`, admin queries) | ✅ | ✅ |
| Logs — `pg logs` | ✅ | ✅ |
| Namespace isolation (`pg.yaml` `namespace`) | ✅ | ✅ |
| Data import / export — `pg import` / `pg export` | ✅ | ✅ |
| Clone — `pg clone` (streamed `pg_dump \| pg_restore`) | ✅ | ✅ |
| Extensions — `pg extension install` / `list` | ✅ | ✅ |
| Backup / restore — `pg backup` / `pg restore` (pgBackRest) | ✅ | ✅ single-host |
| Physical replica — `pg replica` | ✅ | ✅ single-host |
| Failover — `pg failover` (replica promotion) | ✅ | ✅ single-host |
| Auto-start on boot — `pg autostart` | ✅ systemd user unit | ✅ launchd (after login) |
| Addon — **PgBouncer** | ✅ incl. cross-host pools | ✅ single-host dev/test |
| Addon — **PgDog** | ✅ | ✅ single-host dev/test |
| Addon — **etcd** | ✅ incl. cross-host clusters | ❌ Linux only |
| Addon — **HAProxy** | ✅ | ❌ Linux only |
| Addon — **MinIO** | ✅ host network | ✅ bridge, published ports |
| Addon — **silo** | ✅ host network | ✅ bridge, published ports |
| Addon — **rustfs** | ✅ host network | ⚠️ code-complete bridge path, untested — treated as Linux only |
| Addon — **Redis** | ✅ host network | ⚠️ code-complete bridge path, untested — treated as Linux only |
| Addon — **Predixy** | ✅ host network (single- and cross-host clusters) | ❌ Linux only |
| Addon — **Garage** | ✅ host network | ⚠️ code-complete bridge path, untested — treated as Linux only |
| Addon — **SeaweedFS** | ✅ host network | ⚠️ code-complete bridge path, untested — treated as Linux only |
| Addon — **pgAdmin 4** | ✅ host network | ⚠️ code-complete bridge path, untested — treated as Linux only |
| Addon — **Grafana** | ✅ host network | ⚠️ code-complete bridge path, untested — treated as Linux only |
| Addon — **Prometheus** | ✅ host network | ⚠️ code-complete bridge path, untested — treated as Linux only |
| Addon — **Alertmanager** | ✅ host network | ⚠️ code-complete bridge path, untested — treated as Linux only |
| Addon — **Gitea** | ✅ host network | ⚠️ code-complete bridge path, untested — treated as Linux only |
| Addon — **nginx** | ✅ host network | ✅ bridge, published ports (lightly tested) |
| Client — **`pg mc`** (MinIO client) | ✅ host network | ✅ bridge (see below) |
| HA — **Patroni** (`pg ha`) | ✅ incl. cross-host | ❌ Linux only |

Legend: ✅ supported · ✅ *note* supported with the stated caveat · ⚠️ present but unverified on that platform · ❌ not supported.

## Instance features

Everything built on the managed PostgreSQL instance works on both platforms —
lifecycle commands, `pg psql` / `pg exec`, logs, namespaces, import/export,
clone, extensions, backup/restore (pgBackRest), physical replicas, and failover.
The macOS bridge path (published ports + container-name DNS) was proven by the
instance and replica/backup paths first; the proxy addons reuse it.

On macOS these are **single-host**: cross-host replicas and pools that span
machines are a Linux `--network host` + LAN-addresses feature.

### Auto-start on boot

Both platforms are supported, via different services:

- **Linux** — a systemd **user** unit.
- **macOS** — a launchd **LaunchAgent**. LaunchAgents run at **user login**, not
  at system boot, because rootless podman cannot start before someone logs in.
  Expect containers to come up after you log in, not before. See
  [Auto-start on Boot](/docs/autostart/).

## Addons

Addon networking differs per component:

- **[PgBouncer](/docs/addon/pgbouncer/)** and **[PgDog](/docs/addon/pgdog/)** —
  pure proxies, supported on macOS for single-host dev/test. On macOS they join
  `pgcli-net` and publish their ports. Client connections are still
  `127.0.0.1:<port>`. See each page's *Platform support* section for the
  backend-address caveat (a remote/backend host must be reachable **from the
  Mac**, not `127.0.0.1`).
- **[etcd](/docs/addon/etcd/)** — **Linux only.** Members run on host
  networking and advertise their client/peer URLs into the raft membership list
  as **permanent cluster state**; retrofitting the macOS bridge + container-name
  model would rewrite that state. Kept Linux-only until that is designed in.
- **[Patroni](/docs/ha-cluster/ha/)** (`pg ha`) — **Linux only.** Members rely on
  podman host networking; both root and rootless podman are supported. The
  `podman machine` VM's uid mapping does not align with host networking.
  The commands fail fast on macOS with a clear message. See [Patroni HA](/docs/ha-cluster/ha/).
- **[HAProxy](/docs/addon/haproxy/)** — **Linux only.** It fronts Patroni
  members over host networking, which the podman machine VM does not provide to
  containers; the manager fails fast on macOS.
- **[MinIO](/docs/addon/minio/)** — **both platforms.** Object storage,
  standalone or as a distributed erasure-coded cluster across hosts. On Linux it
  serves over host networking; on macOS it joins
  `pgcli-net` with its API and console ports published, so the Mac reaches both
  on `127.0.0.1:<port>` (the same path the proxy addons use). The public image
  is dual-arch (amd64 + arm64).
- **[silo](/docs/addon/silo/)** — **both platforms.** Pigsty's MinIO fork, so
  it shares MinIO's networking story exactly: host networking on Linux, the
  `pgcli-net` bridge with published ports on macOS. The public image is
  dual-arch (amd64 + arm64).
- **[rustfs](/docs/addon/rustfs/)** — **Linux only, in practice.** pgcli runs a
  custom wrapper image (`ghcr.io/mars-base/pgcli/pgcli-rustfs`) whose entrypoint
  chowns the bind-mounted data/cert directories to rustfs's fixed uid 10001
  from inside the container, before dropping to the unprivileged user. The
  wrapper image is built dual-arch, and the Go manager has the same macOS bridge
  path (published ports) as MinIO/silo, so macOS is expected to work — but this
  addon's e2e coverage has only run on Linux, so treat it as Linux only until
  it is exercised on a Mac.
- **[Redis](/docs/addon/redis/)** — **Linux, tested; macOS expected.** A plain
  upstream `docker.io/library/redis` image with no wrapper and no fixed container
  uid, so it needs none of rustfs's ownership machinery. On Linux it serves over
  host networking (the port lands on every interface or just loopback, per
  `listen`); on macOS it joins `pgcli-net` with the port published and
  `pg redis-cli` dials `host.containers.internal` — the same bridge path the
  object stores use, code-complete but not yet exercised on a Mac, so the e2e
  coverage is Linux-only for now.
- **[Predixy](/docs/addon/predixy/)** — **Linux only.** Like HAProxy, it needs
  host networking to dial its backend cluster nodes (single- and cross-host,
  both verified). The wrapper image is amd64-only — upstream ships an amd64
  binary — so an arm64 host needs `--platform linux/amd64` emulation, and the
  macOS podman machine path does not exist.
- **[pgAdmin 4](/docs/addon/pgadmin/)** — **Linux, tested; macOS expected.** A
  plain upstream `docker.io/dpage/pgadmin4` image with no wrapper — the image's
  own entrypoint chowns `/var/lib/pgadmin` to uid 5050 and drops privileges, so
  pgcli passes `--user 0` and never chowns on the host (unlike rustfs). On Linux
  it serves over host networking on `pgadmin_start_port` (loopback by default);
  on macOS it joins `pgcli-net` with the port published and its `listen` widened
  to `0.0.0.0` — the same bridge path redis/the object stores use, code-complete
  but not yet exercised on a Mac, so the e2e coverage is Linux-only for now.
- **[Grafana](/docs/addon/grafana/)** — **Linux, tested; macOS expected.** A
  plain upstream `docker.io/grafana/grafana` image with no wrapper. The image
  bakes `USER 472` and its entrypoint does no ownership work at all, so pgcli
  chowns the host data dir to `472:0` itself before creating the container —
  via `podman unshare chown` under rootless podman, or a direct recursive
  `chown` when pgcli runs as root on a rootful host. On Linux it serves
  over host networking on `grafana_start_port` (on every interface by default);
  on macOS it joins `pgcli-net` with the port published and its `listen` widened
  to `0.0.0.0` — the same bridge path pgAdmin/redis use, code-complete but not
  yet exercised on a Mac, so the e2e coverage is Linux-only for now.
- **[Prometheus](/docs/addon/prometheus/)** — **Linux, tested; macOS expected.**
  A plain upstream `docker.io/prom/prometheus` image with no wrapper, the same
  ownership shape as Grafana (baked `USER nobody`, uid/gid `65534`, no entrypoint
  fixup) — pgcli pre-chowns the host TSDB dir via `podman unshare chown
  65534:65534` under rootless podman, or a direct recursive `chown` when pgcli
  runs as root on a rootful host. Unlike Grafana it is configured by a
  bind-mounted `prometheus.yml` (rendered, or copied from `--conf-file`) plus CLI
  flags after the image name, not env vars. On Linux it serves over host
  networking on `prometheus_start_port` (on every interface by default) — with
  **no authentication** behind that default, so unlike Grafana's
  password-guarded `0.0.0.0`, this exposure is guarded only by the network
  boundary. On macOS it joins `pgcli-net` with the port published — the same
  bridge path Grafana uses, code-complete but not yet exercised on a Mac, so the
  e2e coverage is Linux-only for now.
- **[Alertmanager](/docs/addon/alertmanager/)** — **Linux; macOS code-complete,
  untested.** Prometheus's downstream alert router and its near-exact twin: a
  plain upstream `docker.io/prom/alertmanager` image, the same ownership shape
  (baked `USER nobody`, uid/gid `65534`, no entrypoint fixup — pgcli pre-chowns
  the host state dir), configured by a bind-mounted `alertmanager.yml` (rendered,
  or copied from `--conf-file`) plus CLI flags after the image name. Two
  Alertmanager-specific facts: `--cluster.listen-address=` is always passed empty
  so a single instance never opens the HA gossip port (TCP+UDP `9094`), and unlike
  Prometheus the unauthenticated `POST /-/reload` route **cannot** be disabled in
  the image — pgcli reloads via SIGHUP so it never depends on that route, but the
  route stays open regardless. That sits on top of a sharper exposure than
  Prometheus's: `Listen` defaults to `0.0.0.0` with **no authentication**, and
  reaching the port lets anyone **create silences** — silently muting every
  notification the on-call expects. On macOS it joins `pgcli-net` with the port
  published — the same bridge path, code-complete but not yet exercised on a Mac.
- **[Gitea](/docs/addon/gitea/)** — **Linux; macOS code-complete, untested.** The
  self-hosted Git service runs from the plain upstream `docker.io/gitea/gitea`
  image (rootful build — the rootless variant's data layout is incompatible and
  pgcli never selects it). Ownership is Grafana's shape with a different number:
  the daemon drops to the baked `git` user, uid/gid `1000` (pinned via
  `USER_UID`/`USER_GID` so a host pre-chown cannot drift), no `--user` flag;
  pgcli pre-chowns the host `/data` dir the same way. Config is env-only
  (`GITEA__*` folded into the persisted `app.ini` on every boot) — which is
  exactly why `SECRET_KEY`/`INTERNAL_TOKEN` are never injected: a regenerated
  secret would silently re-key a surviving database on reinstall. The install is
  no-wizard (`INSTALL_LOCK`) and the admin is created afterwards through the
  in-container CLI. Two optional faces beyond the single-port norm: `--dsn`
  moves the metadata to an external PostgreSQL (never created or dropped by
  pgcli), and git-over-ssh rides the image's OpenSSH `sshd`, which runs on
  *every* instance (the s6 service is unconditional). `--ssh-port N` makes that
  sshd the live clone endpoint on host port N; off by default it is just parked
  on a pool port — but it must be parked somewhere, because on the host network
  an sshd left on 22 would fight the host sshd forever.
  `Listen` defaults to `0.0.0.0` (a Git host is cloned from other machines),
  guarded by the generated admin password and closed self-registration. On
  macOS it joins `pgcli-net` with the web port — and, when SSH is enabled, the
  SSH port — published; code-complete but not yet exercised on a Mac.
- **[BeeGFS](/docs/addon/beegfs/)** — **Linux only; macOS installation is
  refused.** The first multi-container addon: one config entry is one
  converged single-host *filesystem* — three containers (`mgmtd` + `meta` +
  `storage`) started in dependency order and managed as a unit. It is the only
  addon with **no bridge path and no bridge fallback**: BeeGFS speaks its own
  TCP/UDP protocol (not HTTP), so the daemons run on `--network host` and bind
  their stock protocol ports directly — and because 8.4's meta (8005) and
  storage (8003) have no listen-port override at all, exactly one filesystem can
  exist per host (guarded, not pooled). No macOS equivalent exists to bridge
  to: without host networking there is no correct BeeGFS topology, so
  `pg addon install beegfs` fails fast on a Mac rather than half-working.
  Ownership is the *inverse* of Gitea/Grafana: the images run as root and own
  their bind-mounted dirs, so pgcli never chowns anything. The client side is a
  kernel module (DKMS) that cannot be containerised — pgcli deliberately never
  installs or mounts it and instead prints the exact client config via
  `pg addon beegfs mount-hint`.
- **[nginx](/docs/addon/nginx/)** — **both platforms (macOS lightly
  tested).** The HTTP reverse proxy joins `pgcli-net` on macOS with its ports
  published — the same bridge path the object stores use; it has been exercised
  there but not under load. The upstream image is multi-arch (amd64 + arm64).
- **[`pg mc`](/docs/addon/minio/#using-the-mc-client)** (MinIO client) — **both
  platforms.** It runs the `mc` binary from a throwaway container, with your
  `~/.mc/config.json` mounted as its own default config path and local file
  operands (for `cp`/`mirror`/`diff`) mounted at their real paths — on macOS
  that means under the home directory, the only tree the podman machine shares.
  On Linux it uses host networking, so a `127.0.0.1` alias reaches a local
  addon instance; on macOS the container sits on the bridge, where a
  `127.0.0.1` alias is the container's own loopback — point aliases at
  `host.containers.internal:<port>` for a local Mac addon, or a routable
  address for a remote store.

## Confirming your platform

`pg start` prints the detected platform in its banner, so you can confirm which
path you are on:

```
=== pg start ===
Platform: linux        # or: macOS
```
