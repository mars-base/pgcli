---
title: "Pg_exporter"
description: "Run pg_exporter, Pigsty's declarative PostgreSQL metrics exporter, as a managed container — over pgcli instances or any remote PostgreSQL."
weight: 62
---

`pg addon install pgexporter` runs [pg_exporter](https://pigsty.cc/docs/pg_exporter/) — Pigsty's PostgreSQL metrics exporter — as a managed container from the official `docker.io/pgsty/pg_exporter` image. Where the [node_exporter](../nodeexporter/) addon describes the **machine**, pg_exporter describes a **database**: it connects to a PostgreSQL server and publishes ~460 `pg_*` metrics (activity, locks, replication, bloat, WAL, per-database stats…) at `/metrics`. Like Prometheus, VictoriaMetrics, Grafana and node_exporter it is a **top-level addon**: it is attached to no instance of its own — instead it *points at* a target it monitors.

Its reason to exist alongside node_exporter is **unified collection**: the target can be a pgcli-managed instance **or any reachable PostgreSQL server pgcli does not manage**. That is the whole point — one exporter shape collects metrics from databases inside and outside pgcli alike.

> **Three target modes, mutually exclusive — pass exactly one.** `--pg-name <inst>` monitors a **pgcli-managed** instance; its admin DSN is re-derived from `pg.yaml` on every start (so a rotated password or changed port never goes stale). For a server pgcli does **not** manage, the target is given as ONE URL — and which URL you pass decides whether pgcli touches the database:
> - `--admin-dsn postgres://…` — an **admin-privileged** URL. pgcli uses it once to create a low-privilege monitor role on the target, then **discards it**: only its credential-free address (host/port/database) is stored, and host/port/database for the exporter come from it. The exporter thereafter connects as the monitor role.
> - `--dsn postgres://…` — **dial-as-is**. The exporter connects with exactly this URL's own credentials and pgcli runs **no** SQL on the target. Use it when a suitable role already exists, or when pgcli must not touch the database at all.
>
> `--dsn` and `--admin-dsn` never appear together — there's no second URL to give, because the target's host/port/database come from whichever one you pass.

> **The exporter never dials as an admin** in the local and `--admin-dsn` modes: pgcli creates the **low-privilege monitor role** (a `pg_monitor` member, `CONNECTION LIMIT 5`) and hands the container *that* role's URL. To create it, pgcli needs admin rights **once**: for `--pg-name` the admin credentials are derived automatically; for `--admin-dsn` you supply an admin URL that runs the single `CREATE ROLE`/`GRANT` batch and is **never stored in `pg.yaml`**. The generated monitor password is printed once and retrievable via `pg addon password pgexporter`. In the dial-as-is `--dsn` mode pgcli creates nothing — it is your job (or a prior `--admin-dsn` install) that the role already exists.

> **There is NO authentication and no TLS on the exporter endpoint**, and pg_exporter also serves **admin endpoints** (`/reload`, `/explain`, `/stat`) on the same port — anyone who can reach it can scrape metrics *and* trigger reloads. Because a Prometheus scraping it is usually on another host, `Listen` defaults to `0.0.0.0`; the only protection is the network boundary. Restrict it to your monitoring network, or `--listen 127.0.0.1` for a same-host Prometheus.

> **Platform**: Linux is the tested platform (host networking, so the exporter binds the host port directly and can dial a local instance's `127.0.0.1` target). On macOS the container runs on the pgcli-net bridge and **cannot reach a local instance's `127.0.0.1` DSN** — there, use `--dsn` pointed at a reachable address (the remote path works; the local path does not).

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `pgexporter` | Instance name (map key) |
| `--pg-name` | — | Monitor a **pgcli-managed** instance by name (admin DSN re-derived each start; mutually exclusive with `--dsn` and `--admin-dsn`) |
| `--admin-dsn` | — | Remote, **create-role** mode: an **admin-privileged** URL for a server pgcli does not manage. Used **once** to create the monitor role, then discarded — only its credential-free address (host/port/database) is stored, and **never** in a form carrying the admin password. The exporter connects as the monitor role. Mutually exclusive with `--dsn` and `--pg-name` |
| `--dsn` | — | Remote, **dial-as-is** mode: the exporter connects with THIS URL's own credentials and pgcli runs **no** SQL on the target — for a role that already exists or a database pgcli must not touch. Mutually exclusive with `--admin-dsn` and `--pg-name` |
| `--monitor-user` | `pgexporter` | Name of the low-privilege role pgcli creates (`pg_monitor` member; plain identifier only). Use a **distinct value per instance** when several exporters monitor the same target |
| `--image` | `docker.io/pgsty/pg_exporter:1.5.0` | Image override |
| `--port` | auto (base 9630) | Host port; auto-allocated from `pgexporter_start_port` |
| `--listen` | `0.0.0.0` | Bind address — every interface by default (cross-host Prometheus); use `127.0.0.1` for loopback-only |
| `--config-file` | — | Path to a **custom** pg_exporter collector YAML, mounted read-only at `/etc/pg_exporter.yml` (the binary's baked-in default set is used when unset) |
| `--extra-arg` | — | Extra CLI flag passed to the binary (repeatable); appended last so last-wins overrides earlier flags. E.g. `--extra-arg --auto-discovery=false` |
| `--force` | false | Recreate even when running — to apply changed port / listen / target / extra-args |

pg_exporter is **stateless** — it stores nothing, so there is no data directory. `pg addon remove` deletes only the container (the monitor role on the target is **kept**, since it may be shared or hand-managed); reinstalling always starts fresh.

## Install

Monitor a pgcli-managed instance (the monitor role is created automatically):

```bash
pg addon install pgexporter --pg-name demo
```

The command derives the local admin DSN, creates the monitor role on `demo`, pulls the image if needed, starts the container pointed at the *monitor* URL, and prints the scrape info:

```
✓ pg_exporter installed: "pgexporter"
  Container:  pgcli-pgexporter-pgexporter
  Image:      docker.io/pgsty/pg_exporter:1.5.0
  Metrics:    http://0.0.0.0:9630/metrics
  Target:     instance demo
  Monitor as: pgexporter (pg_monitor)

  Monitor password: pgx_3f9a…            # printed ONCE
  (printed once — retrieve it later with `pg addon password pgexporter --name pgexporter`)
```

Monitor a database **outside** pgcli's management, letting pgcli create the monitor role — pass an **admin URL alone**; the target's host/port/database come from it:

```bash
pg addon install pgexporter --name ext \
  --admin-dsn "postgres://admin:adminpw@db.example.com:5432/app"
```

The `--admin-dsn` runs the one-time `CREATE ROLE`/`GRANT pg_monitor` batch and is then **discarded** — only the credential-free address `postgres://db.example.com:5432/app` is kept in `pg.yaml`. Thereafter the exporter connects only as `pgexporter`, and `pg addon password pgexporter --name ext` returns that role's password.

If the monitor role already exists and you'd rather pgcli **not touch the target at all**, pass a `--dsn` with usable credentials instead (mutually exclusive with `--admin-dsn` — you give one URL, not two):

```bash
pg addon install pgexporter --name ext \
  --dsn "postgres://pgexporter:existingpw@db.example.com:5432/app"
```

Here pgcli runs no SQL: the exporter dials exactly this URL with its own credentials.

(To create the role by hand instead, the minimum is `CREATE ROLE pgexporter LOGIN CONNECTION LIMIT 5 PASSWORD '…'; GRANT pg_monitor TO pgexporter;` — plus `GRANT SELECT (oid) ON pg_subscription` on PostgreSQL 10–13.)

Verify the exporter is serving real numbers for the target:

```bash
curl -s http://127.0.0.1:9630/metrics | grep -E '^pg_(up|version_info)'
curl -s http://127.0.0.1:9630/up        # 200 == the target is reachable
```

`pg_up 1` means the monitor role connected. Cross-check `pg_version_info` against `pg exec demo -c "SHOW server_version"` — and confirm the role landed on the target with `pg exec demo -c "\du pgexporter"` (or query `pg_roles` / `pg_auth_members` for the `pg_monitor` grant). With the default `--auto-discovery`, metrics carry a `datname` label per database on the target.

### Repeated installs and one monitor user

The monitor-role SQL is **idempotent**, so re-running `install` never damages the target's role: a plain reinstall on a running, unchanged instance is a **no-op** (no SQL runs), and a `--force` reinstall `ALTER`s the existing role — its OID and grants survive and the `pg_monitor` membership is never duplicated.

What a `--force` / config-changed reinstall **does** do is rotate the generated password (that instance stays fine — the new password is stored and injected together — but it overwrites a password you may have set by hand or reused; `pg addon password pgexporter` retrieves the current one). Because every such install writes a fresh password onto the role, **two exporter instances sharing one target and the default `pgexporter` user collide**: installing the second rotates the shared role's password, and the first keeps its now-stale one — its open pool works, but the next reconnect fails and it drops to `pg_up 0`. When you run more than one exporter against the same target, give each its own role:

```bash
pg addon install pgexporter --name pe2 --pg-name demo --monitor-user pgexporter2
```

### Wire it into Prometheus

The exporter is only useful once something scrapes it. A pgcli [Prometheus](../prometheus/) instance does, via a one-line `--scrape` job:

```bash
pg addon install prometheus --name p1 --scrape pg=127.0.0.1:9630
# remote Prometheus → use the host's LAN address instead of 127.0.0.1:
pg addon install prometheus --name p1 --scrape pg=<host-ip>:9630
```

To add this exporter to a pgcli Prometheus that is **already running and already scraping other jobs**, use `--scrape-append` — it folds the `pg` job into the stored scrape list (merging same-named jobs, appending unseen ones) and SIGHUP-reloads, so it never wipes the existing targets:

```bash
pg addon install prometheus --name p1 --scrape-append pg=127.0.0.1:9630
```

(A plain `--scrape` **replaces** the whole job set, so re-running install with it would drop any other jobs p1 was scraping.) Then in Prometheus, `up{job="pg"}` is `1` and `count(pg_activity_count)` is positive. See the [Prometheus addon](../prometheus/) for the full `--scrape` / `--scrape-append` mechanics.

### Reuse the health probes

pg_exporter's `/primary` and `/replica` return 200 only when the target is in that role, so they double as **HAProxy-style liveness probes** for read/write routing — see the [HAProxy addon](../haproxy/) for wiring a pgcli HAProxy to point-check a PostgreSQL. `/up` (target reachable at all) and `/read` are the other probes.

Re-running `install` on a running instance is a **no-op** without `--force` (config fields you pass are saved; the container keeps serving). With `--force` the container is recreated — and the role SQL re-runs (rotating the monitor password) in `--pg-name` mode, or in `--admin-dsn` mode only when a fresh `--admin-dsn` is passed (without one, the stored monitor password is reused, since pgcli deliberately kept no admin rights). A `--dsn` install never runs SQL at all. Changing port / listen / extra-args needs `--force` (container recreation) — only the **collector config** is hot-reloadable (see below).

## Custom collectors (`--config-file`) and reload

pg_exporter ships a full default collector set **baked into the binary** and plans it at runtime by server version / role / extensions, so **no config file is needed to get useful metrics** — most installs omit `--config-file`. When you do want a custom set, point `--config-file` at a pg_exporter collector YAML; it is mounted read-only at `/etc/pg_exporter.yml` and selected via `PG_EXPORTER_CONFIG`. After editing the file on the host, hot-reload it in place with SIGHUP (no container restart):

```bash
pg addon install pgexporter --pg-name demo --config-file /srv/collectors.yml
# …edit /srv/collectors.yml, then:
pg addon pgexporter reload --name pgexporter
```

Reloading an instance started **without** `--config-file` is a harmless no-op — pgcli tells you so (the built-in default set has nothing on disk to re-read). URL / port / listen changes are **not** covered by SIGHUP; recreate with `install --force`.

## Custom flags (`--extra-arg`)

pg_exporter has a rich flag surface beyond the env-configured URL. Toggle features via `--extra-arg` (repeatable):

```bash
pg addon install pgexporter --pg-name demo \
  --extra-arg --auto-discovery=false \
  --extra-arg --tag=env=prod
```

Extra args are appended **last** to the command line, so they override pgcli's injected flags via last-wins. Common ones: `--auto-discovery=false` (stop adding a `datname` per database), `--exclude-database` (skip databases), `--tag` (add constant labels), `--namespace` (prefix every metric). After `--force`, confirm with `curl -s http://127.0.0.1:9630/metrics` that a `--auto-discovery=false` target reports only the `postgres` database's labels.

## Listening and exposure

`Listen` defaults to `0.0.0.0`: on Linux the container shares the host network namespace, so `/metrics` is reachable on every interface at the assigned port. This is deliberate — a Prometheus scraping it is usually on another machine. But there is **no authentication**, and pg_exporter also serves `/reload`, `/explain` and `/stat` on the same port, so anyone who can reach it can read config and trigger reloads, not just scrape. For a same-host Prometheus pass `--listen 127.0.0.1` (recreate with `--force`); otherwise restrict the port at the firewall. (Under the macOS bridge build, pgcli widens the bind to `0.0.0.0` automatically so the published port stays reachable through the podman VM.)

## Ports

Each pg_exporter instance owns a single port — the metrics endpoint — allocated from a pool with base `pgexporter_start_port` (default `9630`, pg_exporter's conventional port), incrementing for each additional instance and skipping ports taken by any managed container or `used_ports`. The allocation is saved to `pg.yaml`, so a reinstall never re-hands-out a port another addon holds. `9630` shares no number with node_exporter (`9100`), Prometheus (`9090`), VictoriaMetrics (`8428`), Grafana (`3000`) or Alertmanager (`9093`); an explicit `--port` still wins.

```bash
pg addon install pgexporter --name pe2 --monitor-user pgexporter2 --pg-name demo   # second instance: next free port (9631)
pg addon install pgexporter --name pe3 --port 9700 --monitor-user pgexporter3 --pg-name demo  # explicit port takes precedence
```

(Each extra instance on the same target needs its own `--monitor-user` — see *Repeated installs and one monitor user*.)

## Configuration

```yaml
addons:
  pgexporter:
    pgexporter:
      name: pgexporter
      image_tag: docker.io/pgsty/pg_exporter:1.5.0
      host_port: 9630
      listen: 0.0.0.0
      target_instance: demo              # local mode
      monitor_user: pgexporter
      monitor_dsn: postgres://pgexporter:…@127.0.0.1:5432/demo?sslmode=disable   # what the container dials
      monitor_password: "…"              # A SECRET — the generated monitor password
      autostart: false
    ext:
      name: ext
      host_port: 9631
      listen: 0.0.0.0
      admin_dsn: postgres://db.example.com:5432/app   # --admin-dsn mode: credential-FREE address only
      monitor_user: pgexporter
      monitor_dsn: postgres://pgexporter:…@db.example.com:5432/app
      monitor_password: "…"
```

The three target fields are mutually exclusive — set exactly one: `target_instance` (a pgcli-managed instance, DSN re-derived every start), `admin_dsn` (a remote server pgcli created a monitor role on — pgcli keeps only its credential-free address; the admin URL you passed is never persisted), or `dsn` (a remote server dialed as given, stored verbatim with its own credentials, and no role SQL ever runs). `monitor_dsn` / `monitor_password` are what pgcli generates and injects into the container (absent in `dsn` mode, where the exporter dials the `dsn` itself). `extra_args` is a list of flags appended last; `config_file` is an optional host path mounted read-only.

### List

```bash
pg addon list
```

```
Web add-ons (pgexporter):
  pgexporter (name: pgexporter)
    Status:      running
    Metrics:     http://0.0.0.0:9630/metrics
    Target:      instance demo
    Monitor as:  pgexporter (pg_monitor)
    Extra args:  --auto-discovery=false
    Image:       docker.io/pgsty/pg_exporter:1.5.0
    Container:   pgcli-pgexporter-pgexporter
```

The Target line is redacted in remote mode (the DSN's password never appears). The monitor password is not shown — retrieve it with `pg addon password pgexporter`.

### Password

```bash
pg addon password pgexporter          # print the stored monitor-role password
```

This is the password of the `pgexporter` role the exporter connects **with** — not a PostgreSQL admin password.

## Start and stop

Containers run with podman's `unless-stopped` restart policy, so they come back with podman after a host reboot. `start`/`stop` manage them explicitly:

```bash
pg addon stop pgexporter
pg addon start pgexporter
```

`start` re-derives the monitor DSN first (for a `--pg-name` target, so a rotated instance password or changed port takes effect on recreation).

## Auto-start on Boot

```bash
pg autostart enable --pgexporter --name pgexporter
```

This flags the instance in `pg.yaml` and installs/refreshes a boot service (a systemd `--user` unit on Linux, a LaunchAgent on macOS) that runs `pg start --autostart`. A boot-time start only brings the container up with the stored monitor credentials — it does **not** re-run the role-creation SQL (that happens at install). See [Autostart]({{< relref "../autostart" >}}) for the full mechanism, status and disabling.

## Remove

```bash
pg addon remove pgexporter   # deletes the container; there is no data to clean
```

pg_exporter keeps nothing on disk, so `--clean-data` has no effect beyond an informational note. The **monitor role created on the target is not dropped** — it may be shared or hand-managed. To remove it yourself, first revoke its per-database grant (a bare `DROP ROLE` fails while the `pg_subscription` column grant exists in any database it touched): `REVOKE SELECT (oid) ON pg_catalog.pg_subscription FROM "pgexporter";` in each monitored database, then `DROP ROLE "pgexporter";`.

## Logs

```bash
pg logs addon pgexporter            # show recent console output
pg logs addon pgexporter -f         # follow
pg logs addon pgexporter -n 200     # last 200 lines (also -i / --tail)
```

## Troubleshooting

- **`pg_up 0` / no `pg_*` metrics** — the monitor role can't connect. Check `/up` returns 200, that the target's `pg_hba.conf` admits the container's source address, and that the stored password matches the role (`pg addon password pgexporter`; if they diverged, reinstall to rotate it). On a `--dsn` target, confirm the DSN's host/port is reachable *from the exporter host* (a `127.0.0.1` DSN means the target is local to the exporter).
- **`pg_up` went 0 right after installing a *second* exporter on the same target** — both share the default `pgexporter` role, and the second install rotated its password, staling the first. Recreate the first (`install … --force`) to re-sync it, or give each instance its own `--monitor-user` (see *Repeated installs and one monitor user*).
- **Metrics present but thin/empty** — a missing `GRANT pg_monitor` yields a connected exporter with partial collectors. Reinstall (the role setup is idempotent) and confirm membership: `pg exec <inst> -c "SELECT r.rolname, m.rolname FROM pg_auth_members a JOIN pg_roles r ON r.oid=a.member JOIN pg_roles m ON m.oid=a.roleid WHERE r.rolname='pgexporter'"`.
- **Prometheus shows the target DOWN** — the exporter is running (`pg addon list`), but the scrape address/port don't match (`--scrape pg=<host>:<port>`), or a loopback `--listen 127.0.0.1` exporter is being scraped from another host.
- **`--dsn and --admin-dsn are mutually exclusive`** — you passed two remote URLs; give exactly one: `--admin-dsn` to have pgcli create the monitor role (its host/port/database become the target), or `--dsn` to dial a URL with credentials you already manage.
- **`has an admin_dsn target but no stored monitor password`** — a `--force` reinstall of an `--admin-dsn` instance after the secret was lost (never stored by pgcli). Re-pass `--admin-dsn postgres://…` so pgcli can create the role again.
- **A flag / discovery change didn't take effect** — `--extra-arg` and target/URL changes apply at container creation; reinstall with `--force`. Only the collector config (`--config-file`) reloads via SIGHUP.
- **macOS local mode serves nothing** — a bridge container can't reach the host's `127.0.0.1` PG; use `--dsn` at a reachable address on Mac.
- **Port already allocated** — free it with `pg addon remove` first, or pick another with `--port N`.

## Known limitations

- **No authentication, no TLS, and admin endpoints exposed.** pg_exporter serves plain-text `/metrics` plus `/reload`, `/explain`, `/stat`; the only protection for a `0.0.0.0` bind is the network boundary. Front with nginx or restrict by firewall if it must leave a trusted network.
- **The monitor DSN (with its password) is stored in `pg.yaml`.** That is the low-privilege `pg_monitor` role's credential, not an admin one; the `--admin-dsn` used to create it is never persisted. Protect `pg.yaml` accordingly.
- **The official image is `scratch`-based** — it carries no system CA bundle, so a TLS (`sslmode=require`) `--dsn` to an external server may fail certificate verification; supply trust via the image or connect over a trusted network.
- **Stateless** — no data dir, no retention; nothing survives beyond the running process. Reinstall always starts fresh, and the target-side monitor role is never auto-dropped.
- **macOS local-instance mode is not usable** (bridge container can't reach host loopback); remote `--dsn` mode works there. Linux is the tested platform.
- pgcli does not validate `--extra-arg` values; a bad flag surfaces only in the container logs on startup.
