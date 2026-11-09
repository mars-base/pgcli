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
| Add-ons (etcd, pgbouncer, MinIO, …) | `pgcli_addons` | `pg_addon` |
| Patroni HA clusters and their etcd DCS | `pg_ha_clusters`, `pg_etcd_state` | `pg_ha` |
| HAProxy in front of HA clusters | `pg_ha_haproxy` | `pg_ha_haproxy` |
| Standalone S3-compatible stores for backups | `pgcli_s3_*` | `pg_s3` |

Every mutating task is guarded by the current pgcli config and container
state, so an unchanged host reports `ok` rather than `changed`.

Business scenarios are separated by inventory group and `group_vars` file:
standalone, same-host replica, cross-host replica, Patroni HA, and S3 storage.
Enable one scenario group at a time; [Setup](./setup/) has the mapping table.

## Repository layout

```text
ansible/
├── ansible.cfg.example      # copy to ansible.cfg
├── hosts.ini.example        # copy to hosts.ini (gitignored)
├── pyproject.toml           # pinned Ansible via uv
├── group_vars/
│   ├── all/main.yml.example        # global defaults: install, config, instance defaults
│   ├── single/ samehost/ crosshost/  # one deployment mode per group
│   ├── minio_s3/ silo_s3/ rustfs_s3/ # S3 store types
│   └── ha_etcd/ ha_cluster/          # shared DCS and Patroni members
├── host_vars/               # optional per-host overrides
├── playbooks/
│   ├── apply.yaml           # install + instances + add-ons + HA
│   ├── deploy-s3.yaml       # standalone S3 store
│   ├── ha-status.yaml       # read-only Patroni/HAProxy status
│   └── ping.yaml            # connectivity smoke test
└── roles/                   # pgcli, pg_instance, pg_addon, pg_ha, pg_ha_haproxy, pg_s3
```

## Pages

| Page | What it covers |
|------|----------------|
| [Quick deploy](./quick-deploy/) | Copy-paste commands: one host, one standalone instance, from clone to `pg list` |
| [Setup and example files](./setup/) | Which `*.example` file becomes which live file, `hosts.ini` groups, `group_vars` fields, first run |
| [Playbooks](./playbooks/) | `ping.yaml`, `apply.yaml`, `ha-status.yaml`, `deploy-s3.yaml`: tags, `--limit`, idempotency, lifecycle orders |
| [Instances and replicas](./instances/) | The `single`/`samehost`/`crosshost` example files and every `pgcli_instances` field |
| [Add-ons](./addons/) | `pgcli_addons` entries: install/remove, instance-scoped add-ons, etcd |
| [HA clusters](./ha/) | `ha_etcd`/`ha_cluster` example files, `pg_ha_clusters`, HAProxy, state-driven destroy |
| [S3 storage](./s3/) | Store-type example files, `deploy-s3.yaml` options, using the store as a backup target |
