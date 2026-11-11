# pgcli-ansible

Ansible playbooks for deploying [pgcli](https://github.com/mars-base/pgcli)-managed
PostgreSQL instances, Patroni HA clusters and add-ons on most mainstream Linux
distributions. With this repository you can declaratively orchestrate PostgreSQL
databases, extensions, and the surrounding database and storage infrastructure.

This repository is the standalone subtree of the `ansible/` directory in
[mars-base/pgcli](https://github.com/mars-base/pgcli). The full documentation
lives at <https://pgcli.pages.dev/docs/ansible/>.

## Requirements

- Control node: Python 3.11+ and [uv](https://docs.astral.sh/uv/)
- Managed hosts: most mainstream Linux distributions, reachable over SSH. The
  examples connect as `root`; any sudo-capable account works, and rootless
  deployments keep the SSH account separate from the unprivileged deployment
  account (see [Rootless deployment](#rootless-deployment)).
- The playbooks install pgcli and its podman dependency; no manual setup is
  needed on a fresh host

## Quick start

Replace `myproj`, `db-01` and the SSH address, then run on the control node:

```bash
myproj=xx
git clone --depth 1 https://github.com/mars-base/pgcli-ansible.git ansible-${myproj}
cd ansible-${myproj}
uv sync
cp ansible.cfg.example ansible.cfg

cat > hosts.ini <<'EOF'
[single]
db-01 ansible_host=192.0.2.10 private_ip=192.0.2.10 ansible_user=root ansible_port=22

[single:vars]
ansible_group_priority=20
EOF

cp group_vars/all/main.yml.example group_vars/all/main.yml
cat > group_vars/single/main.yml <<'EOF'
pgcli_config_overrides:
  namespace: dev

pgcli_instances:
  - name: pg_single
    host: db-01
    state: started
    autostart: true
EOF

uv run ansible-playbook playbooks/ping.yaml --limit single
uv run ansible-playbook playbooks/apply.yaml --limit single

uv run ansible single -m command -a 'pg list'
uv run ansible single -m command -a 'pg exec pg_single -c "SELECT version();"'
```

Re-running `apply.yaml` is idempotent.

## Rootless deployment

By default every host runs pgcli and podman as `root`. To run them as an
unprivileged account, put the host in the `[rootless]` inventory group and copy
`group_vars/rootless/main.yml.example` to `group_vars/rootless/main.yml`:

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
`become` as the SSH account.

The SSH account does not have to be root. Any account that can `become` root
works, including a dedicated operator account:

```ini
[all:vars]
ansible_user=ops
ansible_port=22
```

All plays run with `become: true`, so a sudo password is required unless the
account has `NOPASSWD`; pass it once with `-K` (`--ask-become-pass`) when
running a playbook. `ansible_become_password` can also be set in an encrypted
`host_vars` file, but do not commit it in plaintext.

The deployment account (`pgcli` above) does not need SSH access or a shell; the
plays switch to it with `become_user`.

Rootless containers use host networking. Binding ports below
`net.ipv4.ip_unprivileged_port_start` (1024 by default) would fail, so the play
persists `net.ipv4.ip_unprivileged_port_start=0` in
`/etc/sysctl.d/99-pgcli-rootless.conf` and writes `/etc/sudoers.d/pgcli-deploy`
granting the deployment account exactly one command:
`sysctl -w net.ipv4.ip_unprivileged_port_start=0`. It gets no other sudo rights.
When the account already has passwordless sudo (`sudo -n true` succeeds as that
account), the rule is redundant and the file is skipped.

Run with `--limit rootless`. Config, image cache, data and backups move under
the account's home (`~/.pgcli`, `~/.cache/pgcli/images`, `~/pg/data`,
`~/pg/backup`); `pg` stays in `/usr/local/bin/pg`. Hosts outside `[rootless]`
keep the rootful layout.

### Operating a rootless deployment

The config, podman storage and containers belong to the deployment account, so
`pg` must run as that account with its systemd user runtime. The account has a
nologin shell, so open a shell with `sh` rather than `sudo -i`:

```bash
uid=$(id -u pgcli)
sudo -u pgcli env HOME=/home/pgcli \
  XDG_RUNTIME_DIR=/run/user/$uid \
  DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus \
  sh -c 'cd && pg list'
```

The read-only playbooks resolve this environment automatically and are the
easiest way to inspect a rootless deployment from the control node:

```bash
uv run ansible-playbook playbooks/resources.yaml -e HOSTS=rootless
uv run ansible-playbook playbooks/ha-status.yaml -l rootless_ha
ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml -e HOSTS=rootless
```

### Rootless HA

For a non-root Patroni deployment, add a `[rootless_ha]` group and make it a
child of `[ha_cluster]`, then copy
`group_vars/rootless_ha/main.yml.example` to `group_vars/rootless_ha/main.yml`.
The child group turns on the unprivileged account and overrides the parent's
namespace, cluster and HAProxy lists with one home-based cluster. Enable the
co-located etcd DCS on the same hosts in `[ha_etcd]` (entries `state: present`)
and run `uv run ansible-playbook playbooks/apply.yaml --limit rootless_ha`.
etcd, the Patroni members, backups and HAProxy all run as the deployment
account.

## Playbooks

Run every playbook from the repository root with `uv run`.

| Playbook | What it does | Modifies hosts |
|---|---|---|
| `ping.yaml` | Verifies SSH/Ansible connectivity to every selected host | No |
| `apply.yaml` | Installs pgcli and reconciles instances, extensions, add-ons and HA clusters | Yes |
| `deploy-s3.yaml` | Deploys a standalone minio/silo/rustfs store as a pgBackRest S3 target | Yes |
| `ha-status.yaml` | Shows the live Patroni topology and HAProxy endpoints | No |
| `resources.yaml` | Shows host resources and, per namespace, instances, add-ons and HA status | No |
| `passwords.yaml` | Shows the stored credentials of instances, HA clusters and add-ons | No |

### ping.yaml

Checks that the control node can reach the selected hosts over SSH and run
Ansible modules. It changes nothing and is the quickest way to validate a new
inventory. Targets every host in the inventory, or a subset with `--limit`:

```bash
uv run ansible-playbook playbooks/ping.yaml
uv run ansible-playbook playbooks/ping.yaml --limit debian12-01
```

### apply.yaml

The main playbook. It performs the actual deployment: installs pgcli and its
images, initializes the host config, then reconciles `pgcli_instances`,
`pgcli_addons` and the HA cluster declarations for each selected host. Enable
exactly one scenario group at a time and select it with `--limit`:

```bash
uv run ansible-playbook playbooks/apply.yaml --limit single
uv run ansible-playbook playbooks/apply.yaml --limit instance_addon_mixed
uv run ansible-playbook playbooks/apply.yaml --limit ha_cluster
```

Tags allow running only part of it: `pgcli`, `instance`, `addon`, `ha`
(the validation play is tagged `always`). On hosts that already have pgcli
installed, skip the install play:

```bash
uv run ansible-playbook playbooks/apply.yaml --limit instance_addon_mixed --tags instance,addon
uv run ansible-playbook playbooks/apply.yaml --limit debian12-01 --tags instance
```

Add-ons are selected by their exact inventory group name — `--limit
addon_<type>_<single|cluster>` — not by a separate type variable:

```bash
uv run ansible-playbook playbooks/apply.yaml --limit addon_etcd_single --tags addon
uv run ansible-playbook playbooks/apply.yaml --limit addon_redis_cluster --tags addon
```

Wildcards and comma-separated limits are rejected by the validation play.
Set `state: absent` in the corresponding `group_vars` entry to remove
resources; `clean_data: true` also deletes the data directory.

### deploy-s3.yaml

Deploys one of the pgcli-managed object stores (minio, silo, rustfs) for the
`pgBackRest` `s3` backup target: it prepares the data drives, writes the TLS
material and starts the store containers. The store type comes from the active
`*_s3` inventory group (`minio_s3`, `silo_s3`, `rustfs_s3`); the layout is
derived from the selected node count and `pgcli_s3_drives`.

```bash
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_group=minio_s3
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_nodes=debian12-01
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_nodes=debian12-01,debian12-02,debian12-03,debian12-04
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_type=rustfs
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_force=true
```

`pgcli_s3_nodes` defaults to the whole `pgcli_s3_group` (the `[s3]` parent
unless overridden). `pgcli_s3_type` is optional when the group name already
encodes the type. See the `*_s3` `main.yml.example` files for drives, TLS and
endpoint options.

### ha-status.yaml

Targets the `ha_cluster` group (or any single member; all members share the
same DCS view) and prints the Patroni topology plus the HAProxy endpoints in
front of it. Read-only; it does not change cluster state:

```bash
uv run ansible-playbook playbooks/ha-status.yaml
uv run ansible-playbook playbooks/ha-status.yaml -l ha_cluster
uv run ansible-playbook playbooks/ha-status.yaml -l debian12-02
```

### resources.yaml

Reports CPU, load, memory, filesystems, the pgcli version, and per-namespace
instances, add-ons and HA status. Hosts without the pgcli binary at
`pgcli_install_path` are skipped. Read-only:

```bash
uv run ansible-playbook playbooks/resources.yaml
uv run ansible-playbook playbooks/resources.yaml -e HOSTS=debian12-01
uv run ansible-playbook playbooks/resources.yaml -l addon_nodeexporter_single
```

### passwords.yaml

Prints the admin DSN of every instance (read from `pg status -i`, with
`127.0.0.1`/`localhost` rewritten to the host's `private_ip`, falling back to
`ansible_host`), each HA cluster's stored password set (`pg ha passwords`),
and the add-on credentials (`pg addon list --show-password`). Read-only; it
only queries the existing state.

The output is plaintext credentials, so the playbook **refuses to run unless
Ansible logging is disabled**. Pass `ANSIBLE_LOG_PATH=/dev/null` explicitly:

```bash
ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml
ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml -e HOSTS=debian12-01
ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml -e HOSTS=ha_cluster
```

Do not redirect its stdout to a world-readable file; write any copy to a
`0600` file or a password manager.

## PostgreSQL extensions

The base image ships 45 built-in extensions (contrib) that only need
`CREATE EXTENSION`. pgcli can also build a derived image containing any of the
395 extensions from the Pigsty DEB catalog — `pg extension available` lists all
440 known names. Extensions are baked into the image layer, so they survive
container rebuilds and restarts.

Common choices: `pg_stat_statements`, `pg_cron`, `timescaledb`, `pgvector`,
`postgis`, `pgmq`, `pgcrypto`, `pg_trgm`, `postgres_fdw`, `pg_partman`.

Declare extensions per instance in `pgcli_instances`; the role installs any
missing ones with `pg extension install ... --auto-restart`:

```yaml
pgcli_instances:
  - name: pg_single
    host: db-01
    extensions:
      - pg_stat_statements
      - pgcrypto
      - pgvector
```

The default is `[pg_stat_statements]`; setting `extensions` replaces the list
entirely. The same operations are available directly through
`pg extension install|list|remove|available`. See the
[extensions guide](https://pgcli.pages.dev/docs/extensions/) and the
[built-in catalog](https://pgcli.pages.dev/docs/extensions/catalog/).

## Add-ons

pgcli manages 38 add-on types — PostgreSQL sidecars, storage systems,
observability stacks, messaging systems and supporting services. Most run as
managed host-network containers; JuiceFS, ZeroFS and DuckDB are installed as
host binaries.

| Category | Add-ons |
|---|---|
| Connection pooling and proxying | `pgbouncer`, `pgdog`, `predixy`, `haproxy`, `nginx` |
| Observability | `prometheus`, `alertmanager`, `grafana`, `nodeexporter`, `pgexporter`, `victoriametrics`, `vmalert`, `victorialogs`, `vlagent`, `vmcluster`, `vlcluster` |
| Object storage | `minio`, `silo`, `rustfs`, `garage`, `seaweedfs` |
| Filesystems | `beegfs`, `juicefs`, `zerofs` |
| Databases and analytics | `clickhouse`, `clickhouse-keeper`, `influxdb`, `influx2tsdb`, `documentdb`, `duckdb` |
| Messaging and coordination | `etcd`, `redis`, `kafka`, `rabbitmq`, `rocketmq` |
| Web and admin | `gitea`, `pgadmin`, `postgrest` |

Each add-on has an inventory group with a fixed topology: `addon_<type>_single`
(one host) or `addon_<type>_cluster` (multi-host; `etcd`, `minio`, `silo`,
`rustfs`, `seaweedfs`, `redis`, `clickhouse`, `clickhouse-keeper`, `kafka`,
`rabbitmq`, `rocketmq`, `vmcluster` and `vlcluster`). Declare entries in
`group_vars/addon_<type>_<mode>/main.yml` and apply only that group:

```yaml
addon_etcd_single_addons:
  - type: etcd
    state: present
    name: etcd1
```

```bash
uv run ansible-playbook playbooks/apply.yaml --limit addon_etcd_single --tags addon
```

Set `state: absent` to remove an add-on; add `clean_data: true` to delete its
data as well. Per-type flags go in the entry's `params` list (or the group's
`addon_<type>_<mode>_params` default); each `main.yml.example` documents the
common ones. See the [add-on reference](https://pgcli.pages.dev/docs/addon/)
and [deploying add-ons with Ansible](https://pgcli.pages.dev/docs/ansible/addons/).

To create a PostgreSQL instance and its add-ons in one run, use the mixed
scenario group: declare both `pgcli_instances` and `pgcli_addons` in
`group_vars/instance_addon_mixed/main.yml`, enable `[instance_addon_mixed]` in
the inventory, and run `apply.yaml --limit instance_addon_mixed`. The instance
play runs before the add-on play, so a local pgbouncer can pool an instance
created in the same run. Enable only one scenario group at a time.

## Layout

| Path | Description |
|---|---|
| `playbooks/` | Entry points: `apply.yaml`, `ping.yaml`, `resources.yaml`, `deploy-s3.yaml`, `ha-status.yaml`, `passwords.yaml` |
| `roles/` | `pgcli`, `pg_instance`, `pg_addon`, `pg_ha`, `pg_ha_haproxy`, `pg_s3`, `addon_cluster_shared` |
| `group_vars/` | Per-scenario and per-add-on settings; only `main.yml.example` files are tracked |
| `hosts.ini.example` | Inventory template (`hosts.ini` is gitignored) |
| `ansible.cfg.example` | Ansible config template (`ansible.cfg` is gitignored) |

Live inventory, `group_vars/*/main.yml`, `host_vars/*/secrets.yaml` and `var/`
are gitignored: keep credentials and generated TLS material out of the
repository. More deployment examples are in `hosts.ini.example` and the
`main.yml.example` files under `group_vars/`.

## License

Apache License 2.0, see [LICENSE](LICENSE).
