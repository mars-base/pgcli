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
| HA — **Patroni** (`pg ha`) | ✅ incl. cross-host | ❌ Linux only |

Legend: ✅ supported · ✅ *note* supported with the stated caveat · ❌ not supported.

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
- **[Patroni](/docs/addon/ha/)** (`pg ha`) — **Linux only.** Members rely on
  rootless podman host networking and Linux-specific loopback rewriting to share
  a `0600` config and their data directories; the uid mapping a `podman machine`
  VM uses does not line up. The commands fail fast on macOS with a clear
  message. See [Patroni HA](/docs/addon/ha/).

## Confirming your platform

`pg start` prints the detected platform in its banner, so you can confirm which
path you are on:

```
=== pg start ===
Platform: linux        # or: macOS
```
