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
- SSH access to every managed host. The examples use `ansible_user=root`.
  Rootless deployments keep the SSH account separate from the unprivileged
  deployment account; the SSH account still needs sudo because host
  preparation always runs through `become`.
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
| `group_vars/instance_addon_mixed/main.yml.example` | `group_vars/instance_addon_mixed/main.yml` | One instance plus add-ons in the same run |
| `group_vars/ha_etcd/main.yml.example` | `group_vars/ha_etcd/main.yml` | Shared etcd DCS |
| `group_vars/ha_cluster/main.yml.example` | `group_vars/ha_cluster/main.yml` | Patroni clusters + HAProxy |
| `group_vars/{minio,silo,rustfs}_s3/main.yml.example` | matching live file | S3-compatible store deployment |
| `group_vars/addon_<type>_single/main.yml.example` | matching live file | One standalone add-on type (1 host) |
| `group_vars/addon_<type>_cluster/main.yml.example` | matching live file | One multi-host add-on cluster (3–4 hosts) |
| `group_vars/rootless/main.yml.example` | `group_vars/rootless/main.yml` | Non-root deployment account for hosts in `[rootless]` |

```bash
cp ansible.cfg.example ansible.cfg
cp hosts.ini.example hosts.ini
cp group_vars/all/main.yml.example group_vars/all/main.yml
cp group_vars/single/main.yml.example group_vars/single/main.yml
cp group_vars/addon_nginx_single/main.yml.example group_vars/addon_nginx_single/main.yml
# ...and the same for every group you use
```

Secrets have no `.example` counterpart with real values. Create
`group_vars/<group>/secrets.yaml` (or `host_vars/<host>/secrets.yaml`)
yourself; every `secrets.yaml` and every live `main.yml` is gitignored.

## Inventory: `hosts.ini`

Hosts are defined once in `[all]` with their connection variables. Every other
group selects them by bare name, and `--limit <group>` chooses the scenario:

```ini
[all]
db-01 ansible_host=192.0.2.10 private_ip=192.0.2.10
db-02 ansible_host=192.0.2.11 private_ip=192.0.2.11
db-03 ansible_host=192.0.2.12 private_ip=192.0.2.12
db-04 ansible_host=192.0.2.13 private_ip=192.0.2.13

[all:vars]
ansible_user=root
ansible_port=22

[single]
db-01

[crosshost]
db-01
db-02

[single:vars]
ansible_group_priority=20
[crosshost:vars]
ansible_group_priority=20

[addon_pgbouncer_single]
db-01

[addon_etcd_cluster]
db-01
db-02
db-03
```

- Enable **one mode group at a time**; each mode owns its own
  `pgcli_instances` list. The mode groups carry `ansible_group_priority=20`
  so their variables win over the `all` defaults.
- **Order matters.** The instance play runs with `serial: 1`; list the primary
  before hosts that create replicas from it. Add-on groups use the same rule:
  the first host bootstraps a quorum.
- Set `private_ip` for node-to-node traffic (replication, etcd, backup SSH).
  `ansible_host` remains the SSH address; on public clouds it is often the EIP.
- The S3 section activates exactly **one** store-type child under `[s3]`
  (`minio_s3`, `silo_s3` or `rustfs_s3`).
- `[ha_etcd]` and `[ha_cluster]` are role marker groups. The first etcd host
  bootstraps the DCS; Patroni member names follow `[ha_cluster]` order.
- Add-on groups are named `addon_<type>_single` (one host) and
  `addon_<type>_cluster` (three or four hosts). Declare them all at once —
  their variables are uniquely named and `--limit` picks one group per run.

## Rootless deployment

By default every host runs pgcli and podman as `root`. To run them as an
unprivileged account, add the host to `[rootless]` and enable it in
`group_vars/rootless/main.yml`:

```ini
[rootless]
db-01
```

```yaml
# group_vars/rootless/main.yml
pgcli_deploy_rootless: true
pgcli_deploy_user: pgcli
```

`ansible_user` in `hosts.ini` is only the SSH connection account and must have
sudo. `pgcli_deploy_user` is the account that owns pgcli's state and runs
rootless podman; the two may be different accounts. It is created automatically
when missing, with a nologin shell, locked password, subordinate UID/GID ranges
and systemd lingering. Packages, the podman launcher and host preparation
(uidmap, `/etc/containers/policy.json`, userns, SELinux) still run through
`become` as the SSH account. The `pg` binary stays in `/usr/local/bin/pg`.

Rootless containers use host networking. Binding ports below
`net.ipv4.ip_unprivileged_port_start` (1024 by default) would fail, so the play
persists `net.ipv4.ip_unprivileged_port_start=0` in
`/etc/sysctl.d/99-pgcli-rootless.conf` and writes `/etc/sudoers.d/pgcli-deploy`
granting the deployment account exactly one command:
`sysctl -w net.ipv4.ip_unprivileged_port_start=0`. It gets no other sudo rights.
When the account already has passwordless sudo (`sudo -n true` succeeds as that
account), the rule is redundant and the file is skipped.

Config, image cache, data and backups move under the account's home:

| Rootful (default) | Rootless |
|-------------------|----------|
| `/root/.pgcli/pg.yaml` | `~/.pgcli/pg.yaml` |
| `/var/cache/pgcli/images` | `~/.cache/pgcli/images` |
| `/data/pg` | `~/pg/data` |
| `/mnt/backup/pg` | `~/pg/backup` |

Hosts outside `[rootless]` keep the rootful layout. Rootless autostart uses
systemd user units; the play enables lingering so they start at boot. Run with
`--limit rootless` to deploy only those hosts. Rootless podman keeps its own
per-user image store, so images are not shared with the rootful store on the
same host.

## Choose the group by business type

Each business scenario has its own inventory group and its own `group_vars`
file. Enable **one scenario group at a time**; keep the unused groups declared
but empty so the play patterns still resolve.

| Business type | Inventory group | `group_vars` file | Run |
|---------------|-----------------|-------------------|-----|
| Standalone instance, no replicas | `[single]` | `group_vars/single/main.yml` | `apply.yaml --limit single` |
| Primary + same-host replica | `[samehost]` | `group_vars/samehost/main.yml` | `apply.yaml --limit samehost` |
| Primary + cross-host replica | `[crosshost]` | `group_vars/crosshost/main.yml` | `apply.yaml --limit crosshost` |
| Standalone instance + add-ons | `[instance_addon_mixed]` | `group_vars/instance_addon_mixed/main.yml` | `apply.yaml --limit instance_addon_mixed` |
| Patroni HA cluster | `[ha_etcd]` + `[ha_cluster]` | `group_vars/ha_etcd/main.yml` + `group_vars/ha_cluster/main.yml` | `apply.yaml --tags addon,ha` |
| S3-compatible backup store | one of `[minio_s3]` / `[silo_s3]` / `[rustfs_s3]` under `[s3]` | matching `group_vars/<type>_s3/main.yml` | `deploy-s3.yaml` |
| Standalone add-on (one type) | `[addon_<type>_single]` | `group_vars/addon_<type>_single/main.yml` | `apply.yaml --limit addon_<type>_single --tags addon` |
| Multi-host add-on cluster | `[addon_<type>_cluster]` | `group_vars/addon_<type>_cluster/main.yml` | `apply.yaml --limit addon_<type>_cluster --tags addon` |

The four scenario groups (`single`, `samehost`, `crosshost`,
`instance_addon_mixed`) are mutually exclusive deployment modes: their hosts
often overlap, and each enabled group supplies its own `pgcli_instances` list,
so enabling two at once can mix them. HA and S3 are supporting deployments
with their own groups and play selection; when a host is part of an HA
cluster, do not also list it in an active scenario group.

For a normal deployment, declare the instances and add-ons together in the
scenario group's file and run `apply.yaml --limit <scenario>`. The
`addon_<type>_<mode>` groups are for isolated add-on testing: their variable
files are not merged into a scenario group automatically, so copy the entries
(and their `params`) into the scenario group when you want them there.

Add-on groups can all stay declared at once because every variable is uniquely
named and `--limit` selects one group. Keep the scenario and HA groups disabled
while running add-on groups — their group-level `pgcli_instances` and
`pgcli_addons` override the list selected by the add-on `--limit`.

## Global variables: `group_vars/all/main.yml`

The example file is commented field by field. The blocks that matter most:

| Variable | Meaning |
|----------|---------|
| `pgcli_install_version` | Release tag to install; empty means latest |
| `pgcli_install_force` | Re-run the installer even when `pg` exists |
| `pgcli_install_binary_url`, `pgcli_images_http_url`, `pgcli_download_mirror` | Offline/air-gapped install source (the last covers juicefs/zerofs/duckdb binaries and DocumentDB DEBs) |
| `pgcli_install_path` | Binary path, default `/usr/local/bin/pg` |
| `pgcli_deploy_rootless`, `pgcli_deploy_user` | Non-root deployment for `[rootless]` hosts (see [Rootless deployment](#rootless-deployment)); defaults stay in `all` |
| `pgcli_namespace_path` | Host-default namespace directory; empty uses `<deployment home>/.pgcli/pg.yaml` (`/root` rootful, the account's home rootless) |
| `pgcli_namespaces` | Named namespace → `pg.yaml` directory map |
| `pgcli_config_defaults` | `pg config init` values: namespace, `base_dir`, `backup_dir`, port ranges |
| `pgcli_config_overrides` | Per-group namespace override (set by each mode example) |
| `pgcli_instance_defaults` | Default `state`, `autostart`, `backup_target`, `password_length`, `extensions`, `clean_data` |
| `pgcli_instances` | Desired instances (normally set per mode group; add-on groups declare their own, selected by `--limit`) |
| `pgcli_addons` | The selected add-on group's list (`addon_<kind>_addons`) |
| `_addon_limit`, `_addon_group`, `_addon_type`, `_addon_mode` | Add-on selector derived from `--limit addon_<type>_<single\|cluster>` |

`pgcli_config_defaults` is consumed only on the first `pg config init`; editing
it later does not rewrite an existing `pg.yaml`.

## First run

```bash
cd ansible
uv run ansible-inventory --graph   # validates hosts.ini and prints the groups
uv run ansible-playbook playbooks/ping.yaml
uv run ansible-playbook playbooks/apply.yaml --limit single
```

The full play directory (and what each tag runs) is on the
[Playbooks](./playbooks/) page.

## Tips

The repository tracks only the `*.example` files; live `main.yml`, `hosts.ini`,
`ansible.cfg`, `secrets.yaml` and `var/` are gitignored. If you maintain your
own Ansible project (for example a fork you commit to), keep passwords, S3
secret keys and certificates in a separate `secrets.yaml` rather than inline in
`main.yml`. Both `group_vars/<group>/secrets.yaml` and
`host_vars/<host>/secrets.yaml` are loaded automatically, so the variables
resolve the same way while the secret file stays out of version control
(the repository's `.gitignore` already covers this layout).
