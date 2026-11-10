---
title: "Quick deploy"
description: "Copy-paste commands to deploy one standalone PostgreSQL instance on one host"
weight: 5
---

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

`apply.yaml` installs pgcli, initializes the host config, and creates
`pg_single`; re-running it is idempotent.

For more deployment examples, see `hosts.ini.example` and the `main.yml.example`
file under the matching `group_vars/<group>/` directory.
