---
title: "Patroni HA"
description: "Run PostgreSQL high availability with Patroni as a pgcli HA mode — automatic failover, switchover, and a DCS-backed cluster"
weight: 45
---

[Patroni](https://patroni.readthedocs.io) is the de-facto standard for
PostgreSQL high availability: it manages each postmaster's lifecycle, streams
replication between members, and performs **automatic failover** when the
leader is lost. pgcli exposes Patroni as a distinct mode — `pg ha` — rather than
folding it into the plain `pg` instance path.

> **This is a separate mode, not an addon subcommand.** Patroni (not pgcli) owns
> the PostgreSQL process. `pg ha` members do **not** appear in `cfg.Instances`,
> share no instance lifecycle code, and are not created by `pg create`. The one
> thing it borrows from the addon system is the [etcd](./etcd/) DCS.

**Linux only**, like the [etcd](./etcd/) addon: Patroni members rely on
rootless podman host networking. On macOS the commands fail fast with a clear
message.

## Ownership boundary

The single most important thing to internalize is who owns what:

| Responsibility | Owner |
|----------------|-------|
| Patroni container (run/start/stop/rm), image, `patroni.yml`, port allocation, passwords, DCS wiring | **pgcli** |
| postmaster lifecycle, `initdb`, PostgreSQL config rendering, replication slots, **failover** | **Patroni** |
| switchover / failover / pause / edit-config | pgcli wrapping **patronictl** in short-lived containers |

pgcli never edits a running PostgreSQL's config directly, and never runs
`initdb` — Patroni does both. This is also why the plain PG image's
`docker-entrypoint-initdb.d` convention (the `admin` role / default database)
**does not apply** here: Patroni bootstraps its own cluster, so the role system
is `postgres` (superuser), `replicator`, and `rewind_user` instead.

### Container lifecycle ≠ safe PG restart

Because Patroni is PID 1 inside its container:

- **`pg ha create`** is *re-install* semantics (stop + recreate the container).
  Recreating the leader takes that node fully offline and **triggers a
  failover**. Changing a member's config by re-running `create` is fine; for
  dynamic settings prefer `pg ha edit-config`, which never touches the
  container.
- **`pg ha start` / `pg ha stop`** are raw container start/stop. Stopping the
  leader's container is exactly as disruptive as the node crashing — Patroni
  will fail over to a replica. For *planned* maintenance, `pg ha pause` first
  (it disables auto-failover), then stop the container.
- A **paused** cluster has **no automatic failover** until resumed.

## How It Works

`pg ha` runs one Patroni container per member. All members of a scope point at
the same **DCS** (a [etcd](./etcd/) cluster) which holds the cluster's dynamic
configuration and leader lock:

1. **The first `pg ha create` for a scope** bootstraps: Patroni runs `initdb`,
   wins the leader race, and becomes the leader.
2. **Every later member** automatically `pg_basebackup`s from the current leader
   and begins streaming — no flag distinguishes "add" from "join".
3. `pg ha` control commands (`switchover`, `pause`, …) run `patronictl` in an
   ephemeral container against the DCS, so they work even when no member
   container is running locally.

The DCS is the source of truth: Patroni re-renders each member's
`postgresql.conf`/`pg_hba.conf` from it every loop, so hand-edits on disk are
lost — use `pg ha edit-config` instead.

## Scope and the DCS layout

The `<scope>` in `pg ha create <scope>` **is the Patroni cluster name** — the
identity of one HA cluster. Every command that takes a scope (`status`,
`switchover`, `failover`, `pause`, `ctl`, …) names the same cluster; the first
`create` for a scope bootstraps it, later `create`s with the same scope add
members. (`--member` is the per-host node name *inside* the cluster — one
member per machine.)

In `pg.yaml` the cluster is keyed by that scope under `addons.patroni.<scope>`.

### What actually lands in etcd

Patroni stores everything under a fixed etcd namespace plus the scope:

- **namespace:** `/service/` — Patroni's top-level key, not changed by pgcli.
- **scope:** pgcli writes `PatroniScope(scope)` into `patroni.yml`, which is
  your scope **plus the pgcli namespace suffix**. Patroni has no namespace
  concept of its own (its etcd prefix is the raw scope), so pgcli bakes the
  suffix into the scope to keep two pgcli namespaces sharing one etcd from
  cross-talking.

So the full etcd prefix for a cluster is:

```
/service/<scope>[-<namespace>]/          # e.g. /service/app/  (no namespace)
                                         #      /service/app-prod/  (namespace: prod)
├── initialize       # bootstrap marker (written once)
├── leader           # current leader; value = member name
├── members/<member> # per-member registration (conn_url, api_url, state)
├── status           # cluster LSN / state
├── config           # dynamic config (pause lives here too)
├── history          # config revision history
├── failover         # manual failover request
└── sync             # synchronous-replication state
```

The keys below the prefix are Patroni's own DCS layout. To inspect them, point
`pg etcdctl` at a running etcd member of the same host/config:

```bash
ETCDCTL_ENDPOINTS=http://127.0.0.1:2379 pg etcdctl get /service/ -- --prefix --keys-only
```

Note the scope shown there carries the namespace suffix, even if you configured
the cluster with the bare name.

## Install

Prerequisite: a DCS. Either reuse local etcd addon members or point at an
external etcd.

```bash
# 1. a DCS — here, the etcd addon (see the etcd page for multi-node setups)
pg addon install etcd --name m1

# 2. the first member bootstraps the cluster (becomes leader)
pg ha create app --member node1 --etcd m1

# 3. further members join automatically as replicas
pg ha create app --member node2 --etcd m1
pg ha create app --member node3 --etcd m1

# 4. watch it
pg ha status app
```

`pg ha status app` renders `patronictl list` — exactly one `Leader` row and the
rest `Replica … streaming`:

```
+ Cluster: app (7683433951661608987) +-----------+----+-------------+-----+------------+-----+
| Member | Host            | Role    | State     | TL | Receive LSN | Lag | Replay LSN | Lag |
+--------+-----------------+---------+-----------+----+-------------+-----+------------+-----+
| node1  | 127.0.0.1:5432  | Leader  | running   |  1 |             |     |            |     |
| node2  | 127.0.0.1:5433  | Replica | streaming |  1 |   0/3000060 |   0 |  0/3000060 |   0 |
| node3  | 127.0.0.1:5434  | Replica | streaming |  1 |   0/3000060 |   0 |  0/3000060 |   0 |
+--------+-----------------+---------+-----------+----+-------------+-----+------------+-----+
```

With no argument, `pg ha status` rolls up every cluster: container state, member
count, and each member's `pg=`/`rest=` ports.

## Choosing a DCS

Patroni only needs a reachable etcd cluster, so there are two layouts:

- **Co-located** — run the etcd members as addons on the same hosts as the
  Patroni members and pass `--etcd m1,m2,m3`. Simplest: one machine per role,
  no extra hosts. Good for a starter 3-node HA cluster.
- **Dedicated DCS hosts** — run the etcd cluster on its own (virtual) machines
  and point every Patroni member at it with `--etcd-endpoints`. Each line below
  runs on a different machine (pgcli is per-host):

  ```bash
  # host E1 (10.0.0.20) — bootstrap the etcd cluster
  pg addon install etcd --name e1 --cluster prod \
      --advertise-host 10.0.0.20 --client-port 2379 --peer-port 2380

  # host E2 (10.0.0.21) — join
  pg addon install etcd --name e2 --cluster prod \
      --advertise-host 10.0.0.21 --client-port 2379 --peer-port 2380 \
      --join http://10.0.0.20:2379

  # host E3 (10.0.0.22) — join
  pg addon install etcd --name e3 --cluster prod \
      --advertise-host 10.0.0.22 --client-port 2379 --peer-port 2380 \
      --join http://10.0.0.20:2379

  # hosts A / B / C — one Patroni member each; same endpoint list everywhere
  pg ha create app --member node1 --advertise-host 10.0.0.11 \
      --etcd-endpoints 10.0.0.20:2379,10.0.0.21:2379,10.0.0.22:2379
  ```

  This is the more available topology: etcd's quorum survives losing a PG host,
  and re-imaging a database machine never takes the DCS down with it. The PG
  hosts only ever *talk to* the DCS; `--etcd-endpoints` lists all member client
  URLs, so Patroni falls through to the next endpoint if one etcd host is down
  (writes still need the etcd quorum itself — 2 of 3). Keep the endpoint list
  identical on every PG host.

  A single endpoint (`--etcd-endpoints 10.0.0.20:2379`) does work — that one
  etcd member serves the whole cluster, and the other two are hidden behind it.
  But it re-introduces a single point of failure: if E1 goes down, Patroni
  cannot reach the DCS *even though the etcd quorum is healthy*, and a leader
  that fails to renew its lock demotes itself. List every endpoint you actually
  have.

A dedicated odd-sized etcd cluster (3 or 5) is the production recommendation;
co-locating is fine for dev and small footprints.

## Commands

| Command | What it does |
|---------|--------------|
| `pg ha create <scope> --member <m> …` | Register + (re)install one member — **recreate = node offline** |
| `pg ha status [scope]` | All clusters, or one cluster's `patronictl list` |
| `pg ha switchover <scope>` | Planned leader change (patronictl confirms; `--yes` to script) |
| `pg ha failover <scope>` | Promote a replica now |
| `pg ha pause` / `resume <scope>` | Disable / re-enable automatic failover |
| `pg ha edit-config <scope> -- …` | View or patch the dynamic config in the DCS (never recreates a container) |
| `pg ha start` / `stop <scope> --member m \| --all` | Raw container start/stop (see lifecycle caveat) |
| `pg ha remove <scope> --member m \| --scope-all [--clean-data] [--force]` | Remove member(s); `--scope-all` also clears the DCS |
| `pg ha passwords <scope> [--file F]` | Export the stored password set (the `--passwords-file` format, for other hosts) |
| `pg ha ctl <scope> -- <patronictl args…>` | Passthrough to any `patronictl` command |

Flags after `--` reach `patronictl` verbatim (cobra strips the `--`), so
`pg ha ctl app -- show-config`, `pg ha ctl app -- topology`, and
`pg ha edit-config app -- -s synchronous_mode=true --force` all work. `edit-config
--show` is a convenience alias for `ctl … -- show-config`.

## Cross-host members

Each host runs its own pgcli managing **only that host's** members; the cluster
reassembles through the shared DCS, so two hosts' `pg.yaml` files each hold a
partial view of one `scope`.

```bash
# host A (10.0.0.11) — bootstrap (its own etcd, or an external DCS)
pg ha create app --member node1 --advertise-host 10.0.0.11 \
    --etcd-endpoints 10.0.0.9:2379,10.0.0.10:2379

# host A — export the generated password set for other hosts
pg ha passwords app --file app-passwd.yml

# host B (10.0.0.12) — join, sharing the SAME DCS and password set
pg ha create app --member node2 --advertise-host 10.0.0.12 \
    --etcd-endpoints 10.0.0.9:2379,10.0.0.10:2379 \
    --passwords-file app-passwd.yml
```

Cross-host checklist (all three must line up on every host):

1. **Ports** — auto-assignment is per-host, but `connect_address` is stored in the
   DCS cluster-wide. Pass `--host-port` / `--restapi-port` with the **same value
   on every host**, or replicas can't reach each other.
2. **Passwords** — Patroni's replication / rewind / REST-API auth is cluster-wide.
   Export the first host's generated set with `pg ha passwords app --file
   app-passwd.yml` and pass `--passwords-file app-passwd.yml` to every other
   `pg ha create`.
3. **`--advertise-host`** — required for cross-host members; it flips the
   listen address to `0.0.0.0` and puts a reachable IP in `connect_address`.
   Left empty, a member is loopback-only. **This applies to the first (bootstrap)
   member too**: it becomes the leader, and its loopback `connect_address` is
   what every later host would try to `pg_basebackup` from — cross-host joins
   fail forever once the cluster was bootstrapped with the default. If you may
   ever add a member on another host, pass your LAN IPv4 from the very first
   `create`.
4. **Firewall** — allow the two ports (PG + REST API) pairwise between members.

pgcli does not validate the peers' config; `pg ha status` shows each member's
`connect_address` so you can self-check reachability.

## Passwords

The first `pg ha create` for a scope generates four credentials — `superuser`,
`replication`, `rewind`, and the `restapi` basic-auth pair (`restapi_user` /
`restapi_password`) — and stores them in `pg.yaml` under the cluster. You never
pass passwords on the command line: the first member generates them, and
`pg ha passwords` exports the stored set:

```bash
pg ha passwords app --file app-passwd.yml   # the exact --passwords-file format, mode 0600
```

(Without `--file` the YAML goes to stdout — prefer `--file` so the secrets stay
out of shell history and scrollback.)

For full control (or when you'd rather author the file yourself) use the same
format with `--passwords-file`:

```yaml
# app-passwd.yml
superuser: <postgres superuser password>
replication: <replicator password>
rewind: <rewind_user password>
restapi_user: postgres          # optional, defaults to postgres
restapi_password: <REST API basic-auth password>
```

```bash
pg ha create app --member node1 --etcd m1 --passwords-file app-passwd.yml
```

`pg ha create` prints where each password came from (generated-and-stored vs. a
file path). The rendered `patroni.yml` is written mode `0600` because it embeds
all of them.

## Auto-start on Boot

Like the other infra addons, a Patroni member's container can be brought up
after a host reboot — but it only **starts the existing container**, reading the
`patroni.yml` already on disk; it never re-renders config or re-creates data.

```bash
pg autostart enable --ha --scope app --name node1
```

Members are toggled one at a time (`--ha --scope <scope> --name <member>`).
Start order relative to the DCS doesn't matter: Patroni retries until etcd
answers, then re-elects normally. See the [autostart](/docs/autostart/) page for
the boot-service mechanics.

## Logs

```bash
pg logs addon patroni --scope app --name node1          # last 50 lines
pg logs addon patroni --scope app --name node1 -f       # follow
```

Container name is `pgcli-patroni-<scope>-<member>` (namespace-prefixed when a
`namespace` is set).

## Connecting

Clients connect to the **leader's** PostgreSQL port. Find the leader with
`pg ha status app` (the `Leader` row's `Host` is its `connect_address`), then
point psql/pgcli at it. On a single host with default loopback-only members,
that is `127.0.0.1:<host_port>`.

```bash
psql "host=127.0.0.1 port=<leader_port> user=postgres dbname=postgres"
```

After a failover the leader changes, so a fixed connection string should be
avoided unless fronted by a pooler or the Patroni REST API's leader redirect.

## pg ha vs. pg replica — which to pick

pgcli has two ways to get a standby:

| | [`pg replica`](/docs/replica/) + [`pg failover`](/docs/failover/) | `pg ha` (Patroni) |
|---|---|---|
| Model | Manual: `pg replica` builds a standby, `pg failover` promotes it on demand | Automatic: Patroni keeps N members in sync and self-heals |
| Failover | A human runs `pg failover`; primary stays down until promoted | Patroni detects the loss and promotes a replica within ~30s |
| Ownership | pgcli drives postmaster (as with any instance) | Patroni drives postmaster; pgcli owns containers only |
| Best for | Simple read-scaling, single planned promotion, staying on `pg`-instance tooling | Zero-RTO availability requirements, unattended failover |

If you need a standby you control by hand, use `pg replica`. If you need the
cluster to survive a node crash without a human, use `pg ha`.

## Configuration

The rendered `patroni.yml` per member is derived entirely from `pg.yaml` under
`addons.patroni.<scope>`. A typical entry:

```yaml
addons:
  patroni:
    app:
      name: app
      etcd_members: [m1]                # or etcd_endpoints for an external DCS
      passwords:
        superuser: <superuser-password>
        replication: <replication-password>
        rewind: <rewind-password>
        restapi_user: postgres
        restapi_password: <restapi-password>
      members:
        node1:
          container_name: pgcli-patroni-app-node1
          image_tag: ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5
          host_port: 35590
          restapi_port: 39090
          data_dir: /home/you/.pgcli/addon/patroni/app/node1
          autostart: false
```

Ports come from two independent pools — `patroni_start_port` (default 35532) for
PostgreSQL and `patroni_restapi_start_port` (default 39060) for the REST API —
so Patroni members never collide with plain-instance or addon ports.

The DCS-scope key is the scope plus the namespace suffix (Patroni has no
namespace concept of its own, so the prefix is baked into the scope to keep two
pgcli namespaces sharing one etcd from cross-talking).

### Default `patroni.yml`

The file below is what pgcli renders and mounts read-only into each member's
container. Passwords are auto-generated at bootstrap time; `--passwords-file`
lets you supply your own set.

```yaml
scope: app-default
namespace: /service/
name: node1

etcd3:
    hosts: 10.0.0.11:2379       # from --etcd-endpoints or local etcd member
    protocol: http

restapi:
    listen: 0.0.0.0:8009
    connect_address: 10.0.0.11:8009
    authentication:
        username: postgres
        password: <auto-generated>

bootstrap:
    dcs:
        ttl: 30
        loop_wait: 10
        retry_timeout: 10
        maximum_lag_on_failover: 1048576
        postgresql:
            use_pg_rewind: true
            use_slots: true
            parameters:
                wal_level: replica
                hot_standby: "on"
    initdb:
        - encoding: UTF8
        - data-checksums
    pg_hba:
        - host all all all scram-sha-256
        - host replication all all scram-sha-256

postgresql:
    listen: 0.0.0.0:35532
    connect_address: 10.0.0.11:35532
    data_dir: /var/lib/postgresql/data
    bin_dir: /usr/lib/postgresql/18/bin
    pgpass: /patroni/.pgpass
    authentication:
        superuser:
            username: postgres
            password: <auto-generated>
        replication:
            username: replicator
            password: <auto-generated>
        rewind:
            username: rewind_user
            password: <auto-generated>
    parameters:
        unix_socket_directories: /var/lib/postgresql
```

Key points:

- **`scope`** = Patroni cluster name. Combined with `namespace`, it forms the etcd key prefix.
- **`etcd3.hosts`** = `host:port` only (no `http://` scheme). Multiple endpoints are comma-separated.
- **`bootstrap.dcs`** values are defaults only — they take effect on first bootstrap; afterwards use `pg ha edit-config`.
- **`postgresql.listen: 0.0.0.0`** is set when `--advertise-host` is used (cross-host). Without it, listen is `127.0.0.1`.
- **Passwords** are generated once and stored in `pg.yaml`; exported via `pg ha passwords`.

## Dynamic Configuration

Patroni's dynamic configuration lives in the DCS (etcd) under `/service/<scope>/config`.
Every member reads it on each loop (every `loop_wait` seconds) and applies
changes live — so `pg ha edit-config` is the right way to tune runtime parameters
without restarting containers.

> **`bootstrap.dcs` is one-time.** The `bootstrap.dcs` block in `patroni.yml`
> only takes effect when the **first** member of a scope runs `pg ha create`
> (i.e. the cluster bootstrap). Once Patroni writes the config into the DCS,
> subsequent changes to `bootstrap.dcs` in the YAML file are **completely
> ignored** — even on re-install (`pg ha create`). To change dynamic
> configuration after bootstrap, use `pg ha edit-config`.
>
> The common pitfall: re-running `pg ha create` does **not** re-read
> `bootstrap.dcs` from the YAML — Patroni sees the existing `config` key in the
> DCS and uses it directly.
>
> | Method | Description |
> |--------|-------------|
> | `pg ha edit-config app -- -s key=value` | **Recommended** — pgcli's standard way |
> | `pg ha ctl app -- edit-config` | Passthrough to patronictl, same effect |
> | Patroni REST API (`PATCH /config`) | Requires access to a member's REST API port |

**Persistence:** changes are written directly to etcd, not to container files.
Container restarts, `pg ha start`/`stop`, or even `pg ha create` (re-install)
do not lose these settings — new members automatically pick up the latest config
from the DCS.

### Viewing the current config

```bash
pg ha edit-config app --show
```

This is a convenience alias for `pg ha ctl app -- show-config`. The output is
the full JSON blob stored in `/service/<scope>/config`.

### Modifying parameters

```bash
# Set a single parameter
pg ha edit-config app -- -s loop_wait=5

# Set multiple parameters
pg ha edit-config app -- -s loop_wait=5 -s retry_timeout=3

# Apply without confirmation (useful in scripts)
pg ha edit-config app -- -s loop_wait=5 --force

# Interactive edit (opens $EDITOR with the current config)
pg ha edit-config app
```

After a change, all members apply it on their next loop (within `loop_wait`
seconds). No restart needed.

### Common tunable parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `loop_wait` | `10` | Seconds between leader-loop iterations (lock renewal, DCS updates) |
| `ttl` | `30` | Leader lock TTL. If the leader fails to renew within this window, replicas trigger failover |
| `retry_timeout` | `10` | Timeout for DCS/PostgreSQL operations. Must be `< ttl - loop_wait` to give the leader at least one retry chance |
| `maximum_lag_on_failover` | `1048576` | Maximum replication lag (bytes) for a replica to be eligible for promotion. Default 1 MB |
| `synchronous_mode` | `false` | Enable synchronous replication (zero data loss, higher latency) |
| `synchronous_node_count` | `1` | How many synchronous standby nodes (when `synchronous_mode=true`) |
| `use_pg_rewind` | `true` | Use `pg_rewind` to rejoin a failed leader (faster than full `pg_basebackup`) |
| `use_slots` | `true` | Use replication slots (prevent WAL loss when a replica disconnects) |
| `failover_timeout` | `0` | How long to wait before failover (0 = immediate when leader is lost) |

PostgreSQL runtime parameters can also be set under `postgresql.parameters`:

```bash
pg ha edit-config app -- -s 'postgresql.parameters.max_connections=200'
pg ha edit-config app -- -s 'postgresql.parameters.work_mem=64MB'
```

These trigger a PostgreSQL `reload` (or restart, depending on the parameter's
context). Check `pg_hba.conf` and `postgresql.conf` parameter documentation for
which settings require a restart.

### What not to edit

- **Do not edit `patroni.yml` on disk** — it is regenerated from `pg.yaml` on
  every `pg ha create`, and Patroni reads dynamic config from the DCS anyway.
- **Do not edit `postgresql.conf` directly** — Patroni overwrites it each loop
  from the DCS config.
- **Do not change `scope` or `namespace`** — these are baked into the DCS key
  at bootstrap time and cannot be changed without recreating the cluster.

> **Full parameter reference:** [Patroni Dynamic Configuration](./ha-dynamic/)
> covers every DCS-tunable parameter with defaults, constraints, and examples.

## Notes

- **Linux / rootless only.** Members run as the host user via
  `--userns=keep-id` so the rootless container can read the `0600` config and
  write its data dir.
- **`pg_hba.conf` is permissive by design** (`host all all all scram-sha-256`
  + a `replication` line). Rootless podman's pasta rewrites loopback sources,
  and tightening to a fixed allow-list is a planned future refinement — do not
  expose these ports to untrusted networks yet.
- **The DCS (etcd) has its own security caveats** — see the
  [etcd](./etcd/) page: pgcli-managed etcd runs without TLS or auth.
- **No `init.sh` / `docker-entrypoint-initdb.d`.** Patroni bootstraps the
  cluster itself, so the `admin`/default-db convention of plain instances does
  not exist here; use `postgres` (superuser) to connect and create roles.
- **`use_slots` / `use_pg_rewind`** are enabled: Patroni owns replication slots
  and can rejoin a crashed leader via `pg_rewind` instead of a full rebase.
