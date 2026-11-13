---
title: "Add-ons"
description: "Configure addon_<type>_<mode> groups — install and remove top-level or instance-scoped add-ons, and manage the shared etcd group"
weight: 40
---

Add-ons are declared as `addon_<group>_addons` lists in
`group_vars/addon_<type>_<mode>/main.yml`. `group_vars/all` reads the list of
the group named by `--limit` into `pgcli_addons`, so one run selects one
add-on group:

```bash
uv run ansible-playbook playbooks/apply.yaml \
  --limit addon_pgbouncer_single --tags addon
```

The repository ships one group per add-on type and installation form:
`addon_<type>_single` (one host) and `addon_<type>_cluster` (three or four
hosts). See [Add-on clusters](./addon-cluster/) for the cluster form. The
`ha_etcd` and S3 groups define `pgcli_addons` directly and keep working the
same way. `type` takes any add-on accepted by `pg addon install` — see the
[add-on reference](../../addon/) for the full list (pgbouncer, pgdog, etcd,
haproxy, minio/silo/rustfs, redis, nginx, prometheus, kafka, and so on).

## Entry fields

| Field | Meaning |
|-------|---------|
| `type` | Add-on type accepted by `pg addon install` (see the [add-on reference](../../addon/)) |
| `name` | Top-level add-on instance name (e.g. this etcd member) |
| `pg_name` | Instead of `name`: remote PgBouncer/PostgREST target name (`--pg-name`) |
| `dsn` | Remote PostgreSQL DSN (`--dsn`; pgbouncer, postgrest, pgexporter only) |
| `instance` | Instead of `name`: scope the add-on to a pgcli instance |
| `state` | `present` (default) or `absent` |
| `params` | Complete per-entry flag list; replaces `addon_<group>_params` for this entry |
| `args` | Per-entry flags appended verbatim after the resolved parameters |
| `conf` | Inline config content, overriding `addon_<group>_conf` (nginx, haproxy, privoxy, prometheus, alertmanager only) |
| `clean_data` | With `state: absent`, delete data where the add-on supports it (default `true`; set `false` to keep it) |
| `force` | With `state: present`, recreate with `pg addon install --force` |
| `autostart` | `true` (default) enables boot autostart, `false` disables it |

Every entry needs `type` and exactly one of `name`, `pg_name`, or `instance`;
the role asserts this before doing anything.

## Per-type parameters

Flags shared by every entry in one group can be declared once next to the list
as `addon_<group>_params`: a list of raw `pg addon install` tokens. Entries
that do not set their own `params` inherit this list.

An entry can set `params` to its own complete flag list. It replaces
`addon_<group>_params` for that entry, so different instances of the same type
can use different ports, data directories, memory limits, and so on. Per-entry
`args` is still appended after the resolved params.

```yaml
addon_redis_single_addons:
  - type: redis
    name: cache
    state: present
    params:
      - "--port"
      - "6379"
      - "--maxmemory"
      - "256mb"
  - type: redis
    name: session
    state: present
    params:
      - "--port"
      - "6380"
      - "--maxmemory"
      - "512mb"
      - "--listen"
      - "127.0.0.1"
```

Entry names must be unique per type; each instance is stored under
`addons.<type>.<name>` in the pgcli config. Because `params` replaces the shared
list, repeat any shared flags in each entry (or use YAML anchors). The variable
name is `addon_<group>_params`, where `<group>` is the selected inventory group
(for example `addon_redis_single_params` or `addon_clickhouse_keeper_cluster_params`).
Groups that define `pgcli_addons` directly fall back to `addon_<type>_params`.

## Inline config files

nginx, haproxy, privoxy, prometheus and alertmanager accept `--conf-file`. Put
the file content in `addon_<group>_conf` instead of maintaining a path on the
target:

```yaml
addon_haproxy_single_conf: |
  global
      maxconn 1000
  defaults
      mode tcp
      timeout connect 4s
      timeout client 30m
      timeout server 30m
  # ... frontend/backend ...

addon_haproxy_single_addons:
  - type: haproxy
    name: lb1
    state: present
```

A Privoxy config replaces the rendered template, so it must be complete — the
listener and stdout logging are not inherited — and it cannot be combined with
`--allow-network` / `--deny-network`:

```yaml
addon_privoxy_single_conf: |
  confdir /etc/privoxy
  logdir /var/log/privoxy
  logfile /dev/stdout
  listen-address 0.0.0.0:8118

  # Plain forward-socks5 resolves hostnames locally; forward-socks5t uses
  # remote DNS.
  forward-socks5 / 127.0.0.1:1080 .

  permit-access 127.0.0.1/32
  permit-access 10.0.0.0/8
  permit-access 172.16.0.0/12
  permit-access 192.168.0.0/16

addon_privoxy_single_addons:
  - type: privoxy
    name: privoxy1
    state: present
```

The role writes the content to
`<dir of pgcli_config_path>/addons/<type>-<name>.conf` (mode `0600`) and passes
`--conf-file`; `pg addon install` still validates it with `haproxy -c` /
`nginx -t`. A per-entry `conf` overrides the group-level value. Inline config
cannot be combined with an explicit `--conf-file` or with flags that render the
same file (nginx `--upstream`, prometheus `--scrape`/`--remote-write-url`,
alertmanager `--webhook-url`). Editing the content does not reinstall an
existing add-on: set `force: true` (or remove it first) to apply the change.

## Standalone vs cluster installation

`Installation` describes whether the add-on is installed standalone or as a
cluster — not whether several independent copies can run on different hosts:

- **Standalone**: only a single-host installation exists. Copies deployed on
  multiple hosts are independent and do not form a cluster.
- **Standalone + cluster**: installable either standalone or in cluster mode.
- **Cluster only**: only installable as a cluster (a quorum is required).

This repository ships one standalone group per type — `addon_<type>_single`,
one host each — and, for types that support it, one cluster group —
`addon_<type>_cluster`, three or four hosts. Run one group per invocation with
`--limit addon_<type>_<mode>`; [Add-on clusters](./addon-cluster/) covers the
cluster layouts. The cluster form of each [add-on page](../../addon/) documents
the product flags; the S3 and HA groups are covered in their own pages here.

| Add-on | Installation | Notes |
|--------|--------------|-------|
| pgbouncer | standalone | instance-scoped |
| etcd | standalone + cluster | `--cluster`; shared DCS for HA |
| pgdog | standalone + cluster | one backend vs sharded backends |
| haproxy | standalone | each proxy is independent, no cluster mode |
| minio | standalone + cluster | SNSD/SNMD vs MNMD |
| silo | standalone + cluster | SNSD/SNMD vs MNMD |
| rustfs | standalone + cluster | cluster mode is MNMD only (no MNSD) |
| garage | standalone | v1 supports one host only |
| seaweedfs | standalone + cluster | `weed mini` vs `weed server` |
| redis | standalone + cluster | standalone vs `--cluster` sharding |
| predixy | standalone | one proxy fronting one or more Redis backends |
| postgrest | standalone | |
| pgadmin | standalone | |
| grafana | standalone | |
| prometheus | standalone | |
| alertmanager | standalone | HA gossip disabled by pgcli |
| victoriametrics | standalone | |
| nodeexporter | standalone | |
| pgexporter | standalone | |
| documentdb | standalone | requires a local PG 18 instance |
| vmcluster | standalone + cluster | all roles on one host vs role split |
| vlcluster | standalone + cluster | all roles on one host vs role split |
| victorialogs | standalone | |
| vlagent | standalone | |
| vmalert | standalone | HA via repeated instances and `--external-label` |
| influxdb | standalone | no HA in v1 |
| influx2tsdb | standalone | |
| clickhouse | standalone + cluster | static replicated cluster with Keeper |
| clickhouse-keeper | cluster only | Raft quorum of 3 or 5 |
| kafka | standalone + cluster | KRaft controller quorum |
| rabbitmq | standalone + cluster | etcd peer discovery |
| rocketmq | standalone + cluster | standalone vs DLedger cluster |
| gitea | standalone | |
| beegfs | standalone | v1 is one filesystem per host |
| juicefs | standalone | host binary; external S3 + metadata |
| zerofs | standalone | host binary; external S3 |
| duckdb | standalone | host binary |
| nginx | standalone | one reverse proxy |
| privoxy | standalone | HTTP/HTTPS forward proxy; no TLS interception |

## Top-level add-on example

`group_vars/addon_nodeexporter_single/main.yml.example` declares a top-level
add-on in its group list:

```yaml
addon_nodeexporter_single_addons:
  - type: nodeexporter
    name: ne1
    state: present
```

`--limit addon_nodeexporter_single` makes `group_vars/all` read that list into
`pgcli_addons` for the single host in the group.

Groups that set `pgcli_addons` directly use the same entry format.
`group_vars/ha_etcd/main.yml.example` builds one etcd member per host:

```yaml
pgcli_addons:
  - type: etcd
    name: "etcd-{{ groups[pg_etcd_group].index(inventory_hostname) + 1 }}"
    state: present
    autostart: true
    args: "{{ ['--cluster', 'ha', '--advertise-host', _ha_etcd_advertise_host]
             + (['--join', _ha_etcd_bootstrap_endpoint]
                if inventory_hostname != (groups[pg_etcd_group] | first) else []) }}"
```

The first host in the group bootstraps the cluster; the rest pass `--join` to
a running member. The add-on play runs with `serial: 1`, so the bootstrap host
is always ready before the next host joins.

## Instance-scoped example

Use `instance` instead of `name` for add-ons that belong to a PostgreSQL
instance. `group_vars/addon_pgbouncer_single/main.yml.example` declares the
instance and the add-on in the same group:

```yaml
addon_pgbouncer_single_instances:
  - name: your_pg_ins01
    host: "{{ inventory_hostname }}"
    state: started

addon_pgbouncer_single_addons:
  - type: pgbouncer
    instance: your_pg_ins01
    state: present
```

Run both plays so the instance exists first:
`--limit addon_pgbouncer_single --tags instance,addon`. The role then runs
`pg addon install pgbouncer -i your_pg_ins01`.

## Remote PostgreSQL example

Set `dsn` to target a PostgreSQL server that pgcli does not manage. PgBouncer
and PostgREST also need `pg_name`, the name the remote target is stored under;
pgexporter keeps its `name`. The pgbouncer group ships the remote variant
behind `addon_pgbouncer_single_remote_dsn`: leave it empty to pool the local
`your_pg_ins01` instance, or set it to skip the instance and pool that server:

```yaml
addon_pgbouncer_single_addons:
  - type: pgbouncer
    pg_name: remote-proj01
    dsn: postgres://admin:secret@db.example.com:5432/proj01
    state: present
```

pgexporter is declared the same way in `addon_pgexporter_single_addons`, but
uses `name` instead of `pg_name`. A local (no `dsn`) entry can pin the
monitor-role password with `--monitor-password-file FILE` (or
`--monitor-password VALUE`) in `params`, so a standby's dial-as-is `dsn` entry
can reuse the same credential without a post-install fetch. The role sets
`no_log` on the tasks carrying `--monitor-password`.

These entries are top-level (`addons.<type>.<name>`), so `state: absent` and
autostart use `--pg-name` (remote pgbouncer/postgrest) or `--name` (pgexporter).
pgdog has no `--dsn` flag; pass `--backend NAME=HOST:PORT:DB` and
`--user NAME:PASSWORD` in `params` instead.

## Multiple entries

Every group list is a YAML list; each item is reconciled in order. The redis
example above declares two instances of the same type. Groups that set
`pgcli_addons` directly (for example `ha_etcd`) can also mix types in one list:

```yaml
pgcli_addons:
  - type: etcd
    name: etcd-1
    args: [...]
  - type: redis
    name: cache
    state: absent
```

## Install and remove

```bash
# Reconcile one standalone add-on group
uv run ansible-playbook playbooks/apply.yaml --limit addon_nginx_single --tags addon

# Reconcile one cluster group
uv run ansible-playbook playbooks/apply.yaml --limit addon_etcd_cluster --tags addon

# The shared etcd DCS group, which defines pgcli_addons directly
uv run ansible-playbook playbooks/apply.yaml --tags addon --limit ha_etcd
```

- `state: present` installs only when the name/instance is missing from the
  config, unless `force: true` is set. It also reconciles `autostart` with
  `pg autostart enable|disable --<type> ...`; leaving it unset enables boot
  autostart for the add-on.
- `state: absent` calls `pg addon remove <type> --name <name>`, `--pg-name
  <pg_name>` for remote pgbouncer/postgrest, or `-i <instance>`; data is
  deleted by default, and `clean_data: false` keeps it where the add-on
  supports that. The etcd remove path always deletes its data directory.
- Running `--tags addon` without an `addon_*` limit leaves `pgcli_addons`
  empty for the add-on groups, so the run is a no-op there; groups that define
  `pgcli_addons` directly (ha_etcd, S3) still reconcile.

## etcd lifecycle

The etcd entry's `state` controls the member lifecycle: `present` (the add-on
default) creates/reconciles the local member, `absent` removes it.

Set it to `absent` only **after** the Patroni clusters are gone: they use this
etcd as their DCS. With `serial: 1`, members are removed one host at a time;
etcd removal deletes the data directory as part of the command. To rebuild,
set it back to `present` and re-run `--tags addon` — the first host
bootstraps, the rest rejoin.
