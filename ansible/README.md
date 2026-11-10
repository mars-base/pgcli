# pgcli-ansible

Ansible playbooks for deploying [pgcli](https://github.com/mars-base/pgcli)-managed
PostgreSQL instances, Patroni HA clusters and add-ons on most mainstream Linux
distributions.

This repository is the standalone subtree of the `ansible/` directory in
[mars-base/pgcli](https://github.com/mars-base/pgcli). The full documentation
lives at <https://pgcli.pages.dev/docs/ansible/>.

## Requirements

- Control node: Python 3.11+ and [uv](https://docs.astral.sh/uv/)
- Managed hosts: most mainstream Linux distributions, reachable over SSH (the
  examples connect as `root`)
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

## Layout

| Path | Description |
|---|---|
| `playbooks/` | Entry points: `apply.yaml`, `ping.yaml`, `resources.yaml`, `deploy-s3.yaml`, `ha-status.yaml` |
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
