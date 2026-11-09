---
title: "Add-ons"
description: "Configure pgcli_addons — install and remove top-level or instance-scoped add-ons, and manage the shared etcd group"
weight: 40
---

Add-ons are declared in `pgcli_addons` (empty by default in
`group_vars/all/main.yml`). A deployment-mode group adds its own list; the
`ha_etcd` group uses the same mechanism for the shared etcd DCS. `type` takes
any add-on accepted by `pg addon install` — see the
[add-on reference](../../addon/) for the full list (pgbouncer, pgdog, etcd,
haproxy, minio/silo/rustfs, redis, nginx, prometheus, kafka, and so on).

## Entry fields

| Field | Meaning |
|-------|---------|
| `type` | Add-on type accepted by `pg addon install` (see the [add-on reference](../../addon/)) |
| `name` | Top-level add-on instance name (e.g. this etcd member) |
| `instance` | Instead of `name`: scope the add-on to a pgcli instance |
| `state` | `present` (default) or `absent` |
| `params` | Complete per-entry flag list; replaces `addon_<type>_params` for this entry |
| `args` | Per-entry flags appended verbatim after the resolved parameters |
| `conf` | Inline config content, overriding `addon_<type>_conf` (nginx, haproxy, prometheus, alertmanager only) |
| `clean_data` | With `state: absent`, delete data where the add-on supports it (default `true`; set `false` to keep it) |
| `force` | With `state: present`, recreate with `pg addon install --force` |
| `autostart` | `true` (default) enables boot autostart, `false` disables it |

Every entry needs `type` and exactly one of `name` or `instance`; the role
asserts this before doing anything.

## Per-type parameters

Flags shared by every instance of one add-on type can be declared once next to
the list as `addon_<type>_params`: a list of raw `pg addon install` tokens.
Entries that do not set their own `params` inherit this list.

An entry can set `params` to its own complete flag list. It replaces
`addon_<type>_params` for that entry, so different instances of the same type
can use different ports, data directories, memory limits, and so on. Per-entry
`args` is still appended after the resolved params.

```yaml
addon_redis_addons:
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
name uses the type with `-` replaced by `_` (for example
`addon_clickhouse_keeper_params`).

## Inline config files

nginx, haproxy, prometheus and alertmanager accept `--conf-file`. Put the file
content in `addon_<type>_conf` instead of maintaining a path on the target:

```yaml
addon_haproxy_conf: |
  global
      maxconn 1000
  defaults
      mode tcp
      timeout connect 4s
      timeout client 30m
      timeout server 30m
  # ... frontend/backend ...

addon_haproxy_addons:
  - type: haproxy
    name: lb1
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

This repository's `group_vars` examples currently exercise the standalone
form; the cluster forms are documented on each [add-on page](../../addon/),
with the S3 and HA groups covered in their own pages here.

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

## Top-level add-on example

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
instance:

```yaml
pgcli_addons:
  - type: pgbouncer
    instance: pg01
    state: present
```

The role runs `pg addon install pgbouncer -i pg01`, so the instance must exist
in the same `pgcli_instances` reconcile (the add-on play runs after the
instance play in `apply.yaml`).

## Multiple add-ons

`pgcli_addons` is a list; each item is reconciled in order:

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
# Reconcile add-ons on every host that declares pgcli_addons
uv run ansible-playbook playbooks/apply.yaml --tags addon

# Only the etcd group
uv run ansible-playbook playbooks/apply.yaml --tags addon --limit ha_etcd
```

- `state: present` installs only when the name/instance is missing from the
  config, unless `force: true` is set. It also reconciles `autostart` with
  `pg autostart enable|disable --<type> ...`; leaving it unset enables boot
  autostart for the add-on.
- `state: absent` calls `pg addon remove <type> --name <name>` (or `-i
  <instance>`); data is deleted by default, and `clean_data: false` keeps it
  where the add-on supports that. The etcd remove path always deletes its data
  directory.
- A host with `pgcli_addons: []` is a no-op, so the play is safe to run with
  `--tags addon` on the whole inventory.

## etcd lifecycle

The etcd entry's `state` controls the member lifecycle: `present` (the add-on
default) creates/reconciles the local member, `absent` removes it.

Set it to `absent` only **after** the Patroni clusters are gone: they use this
etcd as their DCS. With `serial: 1`, members are removed one host at a time;
etcd removal deletes the data directory as part of the command. To rebuild,
set it back to `present` and re-run `--tags addon` — the first host
bootstraps, the rest rejoin.
