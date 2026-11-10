# pgcli-ansible

Ansible playbooks for deploying [pgcli](https://github.com/mars-base/pgcli)-managed
PostgreSQL instances, Patroni HA clusters and add-ons on Debian hosts.

This repository is the standalone subtree of the `ansible/` directory in
[mars-base/pgcli](https://github.com/mars-base/pgcli). The full documentation
lives at <https://pgcli.pages.dev/docs/ansible/>.

## Requirements

- Control node: Python 3.11+ and [uv](https://docs.astral.sh/uv/)
- Managed hosts: Debian 12, reachable over SSH (the examples connect as `root`)
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
