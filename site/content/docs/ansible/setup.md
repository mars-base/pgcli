---
title: "Setup and example files"
description: "Turn the shipped *.example files into a working Ansible inventory and variable set, then verify connectivity"
weight: 10
---

The repository never commits environment-specific files. Everything is
prepared by copying a checked-in `*.example` file to its live name, then
editing it. The live copies are ignored by `ansible/.gitignore`.

## Prerequisites

- A Linux or macOS control node with [uv](https://docs.astral.sh/uv/).
- SSH access to every managed host. The examples use `ansible_user=root`;
  the plays do not escalate privileges, so use an account with the rights the
  roles need (podman, `/usr/local/bin`, systemd).
- Target hosts run a supported Linux distribution; podman is installed by the
  `pgcli` role when missing.

## Prepare the environment

```bash
cd ansible
uv sync
```

`pyproject.toml` pins Ansible (`ansible-core 2.18`); always run playbooks
through `uv run` so the pinned environment is used.

## Copy the example files

| Example (committed) | Live file (gitignored) | Purpose |
|---------------------|------------------------|---------|
| `ansible.cfg.example` | `ansible.cfg` | Inventory path, roles path, fact cache, `var/ansible.log`, SSH pipelining |
| `hosts.ini.example` | `hosts.ini` | Inventory: deployment-mode groups, S3 groups, HA role groups |
| `group_vars/all/main.yml.example` | `group_vars/all/main.yml` | Install source/version, config paths, namespaces, instance defaults |
| `group_vars/single/main.yml.example` | `group_vars/single/main.yml` | Standalone instance mode |
| `group_vars/samehost/main.yml.example` | `group_vars/samehost/main.yml` | Primary + same-host replica mode |
| `group_vars/crosshost/main.yml.example` | `group_vars/crosshost/main.yml` | Primary + cross-host replica mode, S3 backup vars |
| `group_vars/ha_etcd/main.yml.example` | `group_vars/ha_etcd/main.yml` | Shared etcd DCS |
| `group_vars/ha_cluster/main.yml.example` | `group_vars/ha_cluster/main.yml` | Patroni clusters + HAProxy |
| `group_vars/{minio,silo,rustfs}_s3/main.yml.example` | matching live file | S3-compatible store deployment |

```bash
cp ansible.cfg.example ansible.cfg
cp hosts.ini.example hosts.ini
cp group_vars/all/main.yml.example group_vars/all/main.yml
cp group_vars/single/main.yml.example group_vars/single/main.yml
# ...and the same for every group you use
```

Secrets have no `.example` counterpart with real values. Create
`group_vars/<group>/secrets.yaml` (or `host_vars/<host>/secrets.yaml`)
yourself; every `secrets.yaml` and every live `main.yml` is gitignored.

## Inventory: `hosts.ini`

Hosts are grouped by **deployment mode**, not by environment. The plays target
`all`, and `--limit <group>` selects the mode to exercise.

```ini
[single]
db-01 ansible_host=192.0.2.10 private_ip=192.0.2.10 ansible_user=root ansible_port=22

[crosshost]
db-01 ansible_host=192.0.2.10 private_ip=192.0.2.10 ansible_user=root ansible_port=22
db-02 ansible_host=192.0.2.11 private_ip=192.0.2.11 ansible_user=root ansible_port=22

[single:vars]
ansible_group_priority=20
[crosshost:vars]
ansible_group_priority=20
```

- Enable **one mode group at a time**; each mode owns its own
  `pgcli_instances` list. The mode groups carry `ansible_group_priority=20`
  so their variables win over the `all` defaults.
- **Order matters.** The instance play runs with `serial: 1`; list the primary
  before hosts that create replicas from it.
- Set `private_ip` for node-to-node traffic (replication, etcd, backup SSH).
  `ansible_host` remains the SSH address; on public clouds it is often the EIP.
- The S3 section activates exactly **one** store-type child under `[s3]`
  (`minio_s3`, `silo_s3` or `rustfs_s3`).
- `[ha_etcd]` and `[ha_cluster]` are role marker groups. The first etcd host
  bootstraps the DCS; Patroni member names follow `[ha_cluster]` order.

## Choose the group by business type

Each business scenario has its own inventory group and its own `group_vars`
file. Enable **one scenario group at a time**; keep the unused groups declared
but empty so the play patterns still resolve.

| Business type | Inventory group | `group_vars` file | Run |
|---------------|-----------------|-------------------|-----|
| Standalone instance, no replicas | `[single]` | `group_vars/single/main.yml` | `apply.yaml --limit single` |
| Primary + same-host replica | `[samehost]` | `group_vars/samehost/main.yml` | `apply.yaml --limit samehost` |
| Primary + cross-host replica | `[crosshost]` | `group_vars/crosshost/main.yml` | `apply.yaml --limit crosshost` |
| Patroni HA cluster | `[ha_etcd]` + `[ha_cluster]` | `group_vars/ha_etcd/main.yml` + `group_vars/ha_cluster/main.yml` | `apply.yaml --tags addon,ha` |
| S3-compatible backup store | one of `[minio_s3]` / `[silo_s3]` / `[rustfs_s3]` under `[s3]` | matching `group_vars/<type>_s3/main.yml` | `deploy-s3.yaml` |

The first three are mutually exclusive deployment modes: their hosts often
overlap, and each enabled mode supplies its own `pgcli_instances` list, so
enabling two at once can mix them. HA and S3 are supporting deployments with
their own groups and play selection; when a host is part of an HA cluster, do
not also list it in an active mode group.

## Global variables: `group_vars/all/main.yml`

The example file is commented field by field. The blocks that matter most:

| Variable | Meaning |
|----------|---------|
| `pgcli_install_version` | Release tag to install; empty means latest |
| `pgcli_install_force` | Re-run the installer even when `pg` exists |
| `pgcli_install_binary_url`, `pgcli_images_http_url` | Offline/air-gapped install source |
| `pgcli_install_path` | Binary path, default `/usr/local/bin/pg` |
| `pgcli_namespace_path` | Host-default namespace directory; empty uses `/root/.pgcli/pg.yaml` |
| `pgcli_namespaces` | Named namespace → `pg.yaml` directory map |
| `pgcli_config_defaults` | `pg config init` values: namespace, `base_dir`, `backup_dir`, port ranges |
| `pgcli_config_overrides` | Per-group namespace override (set by each mode example) |
| `pgcli_instance_defaults` | Default `state`, `autostart`, `backup_target`, `password_length`, `extensions`, `clean_data` |
| `pgcli_instances` | Desired instances (normally set per mode group) |
| `pgcli_addons` | Desired add-ons for the host |

`pgcli_config_defaults` is consumed only on the first `pg config init`; editing
it later does not rewrite an existing `pg.yaml`.

## First run

```bash
cd ansible
uv run ansible-inventory --list >/dev/null   # validates hosts.ini
uv run ansible-playbook playbooks/ping.yaml
uv run ansible-playbook playbooks/apply.yaml --limit single
```

The full play directory (and what each tag runs) is on the
[Playbooks](./playbooks/) page.

## What is safe to commit

`ansible.cfg`, `hosts.ini`, `group_vars/*/main.yml`, `group_vars/*/secrets.yaml`,
`host_vars/*/secrets.yaml`, `var/` and `.venv/` are all gitignored. Only the
`*.example` files are tracked. Keep real passwords, S3 secret keys and
certificates in `secrets.yaml`, never in a committed example.
