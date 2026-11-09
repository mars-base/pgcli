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
| `args` | List of flags appended verbatim to `pg addon install` |
| `clean_data` | With `state: absent`, also delete data where the add-on supports it |
| `force` | With `state: present`, recreate with `pg addon install --force` |
| `autostart` | `true` (default) enables boot autostart, `false` disables it |

Every entry needs `type` and exactly one of `name` or `instance`; the role
asserts this before doing anything.

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
pg_etcd_state: present

pgcli_addons:
  - type: etcd
    name: "etcd-{{ groups[pg_etcd_group].index(inventory_hostname) + 1 }}"
    state: "{{ pg_etcd_state }}"
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
  <instance>`); add `clean_data: true` for the add-ons that keep data by
  default. The etcd remove path always deletes its data directory.
- A host with `pgcli_addons: []` is a no-op, so the play is safe to run with
  `--tags addon` on the whole inventory.

## etcd lifecycle

`pg_etcd_state` is the group-level switch:

```yaml
pg_etcd_state: present   # create/reconcile the local member (default)
# pg_etcd_state: absent  # remove the local member
```

Set it to `absent` only **after** the Patroni clusters are gone: they use this
etcd as their DCS. With `serial: 1`, members are removed one host at a time;
etcd removal deletes the data directory as part of the command. To rebuild,
set it back to `present` and re-run `--tags addon` — the first host
bootstraps, the rest rejoin.
