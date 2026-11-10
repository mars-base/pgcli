---
title: "快速部署"
description: "用可直接粘贴的命令在一台主机上部署一个单机 PostgreSQL 实例"
weight: 5
---

替换 `myproj`、`db-01` 和 SSH 地址后，在控制节点直接执行：

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

`apply.yaml` 会安装 pgcli、初始化主机配置并创建 `pg_single`，可重复执行。

更多部署样例参考 `hosts.ini.example` 和对应 `group_vars/<分组>/` 目录下的
`main.yml.example` 文件。
