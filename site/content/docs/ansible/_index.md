---
title: "Ansible Deployment"
description: "Manage pgcli resources declaratively with the bundled Ansible playbooks — instances, replicas, add-ons, Patroni HA clusters, and S3 stores"
weight: 12
icon: fa-solid fa-robot
menus:
  main:
    identifier: docs-ansible
    parent: docs
    weight: 12
    params:
      icon: fa-solid fa-robot
cascade:
  type: docs
  footer_style: slim
---

The `ansible/` directory in the pgcli repository turns the CLI into a
declarative deployment tool. Instead of running `pg create`, `pg addon
install` and `pg ha create` by hand on every host, you describe the desired
state in `group_vars/` and run one playbook. Re-running the playbook converges
the hosts to that state and is safe to repeat.

## What it manages

| Resource | Ansible input | Role |
|----------|---------------|------|
| `pg` binary, podman, host config | `pgcli_*` variables in `group_vars/all` | `pgcli` |
| PostgreSQL instances and read-only replicas | `pgcli_instances` | `pg_instance` |
| Host processes (webhook receivers, …) | `pgcli_daemons` | `pg_daemon` |
| Add-ons (etcd, pgbouncer, MinIO, …) | `pgcli_addons` | `pg_addon` |
| Patroni HA clusters and their etcd DCS | `pg_ha_clusters`, `pgcli_addons` (etcd) | `pg_ha`, `pg_addon` |
| HAProxy in front of HA clusters | `pg_ha_haproxy` | `pg_ha_haproxy` |
| Standalone S3-compatible stores for backups | `pgcli_s3_*` | `pg_s3` |

Every mutating task is guarded by the current pgcli config and container
state, so an unchanged host reports `ok` rather than `changed`.

Business scenarios are separated by inventory group and `group_vars` file:
standalone, same-host replica, cross-host replica, instance plus add-ons,
Patroni HA, S3 storage, and per-type add-on groups. Enable one scenario group
at a time; [Setup](./setup/) has the mapping table.

## Repository layout

```text
ansible/
├── ansible.cfg.example      # copy to ansible.cfg
├── hosts.ini.example        # copy to hosts.ini (gitignored)
├── pyproject.toml           # pinned Ansible via uv
├── group_vars/
│   ├── all/main.yml.example        # global defaults: install, config, instance defaults
│   ├── single/ samehost/ crosshost/  # one deployment mode per group
│   ├── observable/                   # monitoring + alerting scenario (exporters, Prometheus, Alertmanager)
│   ├── minio_s3/ silo_s3/ rustfs_s3/ # S3 store types
│   ├── addon_<type>_single/         # one group per standalone add-on
│   ├── addon_<type>_cluster/        # one group per multi-host add-on cluster
│   └── ha_etcd/ ha_cluster/          # shared DCS and Patroni members
├── host_vars/               # optional per-host overrides
├── misc/
│   ├── prometheus/          # Prometheus alert rule files
│   └── webhook/             # Alertmanager webhook receiver
├── playbooks/
│   ├── apply.yaml           # install + instances + daemons + add-ons + HA
│   ├── deploy-s3.yaml       # standalone S3 store
│   ├── ha-status.yaml       # read-only Patroni/HAProxy status
│   └── ping.yaml            # connectivity smoke test
└── roles/                   # pgcli, pg_instance, pg_daemon, pg_addon, addon_cluster_shared, pg_ha, pg_ha_haproxy, pg_s3
```

## Pages

| Page | What it covers |
|------|----------------|
| [Quick deploy](./quick-deploy/) | Copy-paste commands: one host, one standalone instance, from clone to `pg list` |
| [Setup and example files](./setup/) | Which `*.example` file becomes which live file, `hosts.ini` groups, `group_vars` fields, first run |
| [Offline and air-gapped installs](./offline/) | LAN HTTP server: image manifest and lazy loading, `pg`/podman binaries, runtime host binaries and DocumentDB DEBs |
| [Egress proxy](./proxy/) | Forward proxy for isolated data centers: Privoxy add-on, global variables, `NO_PROXY`, manual SSH usage, verification |
| [Playbooks](./playbooks/) | `ping.yaml`, `apply.yaml`, `resources.yaml`, `deploy-s3.yaml`, `ha-status.yaml`, `passwords.yaml`: tags, `--limit`, idempotency, lifecycle orders |
| [Instances and replicas](./instances/) | The `single`/`samehost`/`crosshost` example files and every `pgcli_instances` field |
| [Observability deployment](./observability/) | Monitoring and alerting for many machines: exporters, Prometheus, rules, Alertmanager, webhook daemon, Grafana |
| [Add-ons](./addons/) | `addon_<type>_<mode>` entries: install/remove, instance-scoped add-ons, etcd |
| [Add-on clusters](./addon-cluster/) | Multi-host add-on clusters: one inventory group per type, shared secrets/CA, quorum layouts, lifecycle |
| [HA clusters](./ha/) | `ha_etcd`/`ha_cluster` example files, `pg_ha_clusters`, HAProxy, state-driven destroy |
| [S3 storage](./s3/) | Store-type example files, `deploy-s3.yaml` options, using the store as a backup target |
