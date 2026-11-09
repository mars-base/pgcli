---
title: "HA 集群"
description: "配置 ha_etcd 与 ha_cluster 示例文件 —— 共享 etcd DCS、Patroni 集群、HAProxy，以及基于 state 的销毁"
weight: 50
---

HA 部署涉及两个 inventory 分组和两个示例变量文件：

| 分组 | 文件 | 作用 |
|------|------|------|
| `[ha_etcd]` | `group_vars/ha_etcd/main.yml` | 共享 etcd DCS，每台主机一个成员 |
| `[ha_cluster]` | `group_vars/ha_cluster/main.yml` | Patroni 成员、按 namespace 的集群、HAProxy |

两类主机可以重合（同机布局）。etcd 的 bootstrap 主机必须排在分组首位；
插件 play 是串行的。

## etcd：`group_vars/ha_etcd/main.yml.example`

文件与分组名解耦：只需设置一次 `pg_etcd_group`，下面全部引用它。

```yaml
pg_etcd_group: ha_etcd

_ha_etcd_advertise_host: "{{ private_ip | default(ansible_host, true) }}"
_ha_etcd_client_endpoint: "{{ _ha_etcd_advertise_host }}:2379"
_ha_etcd_bootstrap_endpoint: "http://{{ hostvars[groups[pg_etcd_group] | first].private_ip | default(...) }}:2379"

pg_etcd_state: present   # present（默认）| absent

pgcli_addons:
  - type: etcd
    name: "etcd-{{ groups[pg_etcd_group].index(inventory_hostname) + 1 }}"
    state: "{{ pg_etcd_state }}"
    autostart: true
    args: "{{ ['--cluster', 'ha', '--advertise-host', _ha_etcd_advertise_host]
             + (['--join', _ha_etcd_bootstrap_endpoint]
                if inventory_hostname != (groups[pg_etcd_group] | first) else []) }}"
```

`_ha_etcd_client_endpoint` 被 `ha_cluster` 用来构造 Patroni 的 DCS 端点列表。
`pg_etcd_state: absent` 会销毁整个分组（必须先销毁 Patroni 集群）。

## Patroni：`group_vars/ha_cluster/main.yml.example`

主机默认配置选择一个 namespace，额外 namespace 声明自己的目录和端口段：

```yaml
pgcli_config_overrides:
  namespace: ha

pgcli_namespaces:
  - name: ha
    path: /root/.pgcli
  - name: ha2
    path: /root/.pgcli/ha2
    base_dir: /data/pg-ha2
    backup_dir: /mnt/backup/pg-ha2
    pg_start_port: 36432
    pg_ssh_port: 43201
```

指定提供 DCS 的 etcd 分组，并派生端点列表：

```yaml
pg_ha_etcd_group: ha_etcd
_ha_dcs_endpoints: >-
  {{ groups[pg_ha_etcd_group]
     | map('extract', hostvars, '_ha_etcd_client_endpoint') | list }}
```

共享 S3 备份仓库（secret key 放在 `secrets.yaml`）：

```yaml
pgcli_backup_s3_host: s3-01
pgcli_backup_s3_port: 19800
pgcli_backup_s3_bucket: pgbackup
pgcli_backup_s3_access_key: admin
```

每个 namespace 一个集群条目：

```yaml
pg_ha_clusters:
  - namespace: ha
    scope: pgcluster
    members: [ha1, ha2, ha3]
    host_port: 35532
    restapi_port: 8008
    dcs_endpoints: "{{ _ha_dcs_endpoints }}"
    state: started          # started | stopped | absent
    autostart: true
    extensions:
      - pg_stat_statements
```

| 字段 | 含义 |
|------|------|
| `namespace` | `pgcli_namespaces` 中的名称 |
| `scope` | Patroni scope；namespace 后缀保证各集群在 DCS 中互不干扰 |
| `members` | 按 `[ha_cluster]` 顺序，每台主机一个成员名 |
| `host_port`、`restapi_port` | PostgreSQL 与 Patroni REST 端口；每个集群必须错开 |
| `dcs_endpoints` | etcd 客户端端点 |
| `state` | `started`（默认）、`stopped`、`absent` |
| `clean_data` | 与 `absent` 搭配：删除成员数据目录 |
| `autostart` | 为成员启用开机自启 |
| `extensions` | 集群范围安装的扩展 |

不同 namespace 的端口段、数据目录、备份目录不能重叠。两个 namespace 使用相同
`scope` 名称是安全的：pgcli 会把 namespace 后缀写进 DCS scope 和容器名。

### HAProxy

```yaml
pg_ha_haproxy_host: "{{ groups['ha_cluster'] | first }}"
pg_ha_haproxy:
  enabled: true
  mode: split          # 读写/只读监听
  max_lag: 1MB
  listen: 0.0.0.0
  autostart: true      # 所有条目的默认值；可按 cluster 覆盖
  clusters:
    - namespace: ha
      name: lb-ha
      rw_port: 5000
      ro_port: 5001
      stats_port: 5002
      state: present    # present（默认）| absent
```

代理通过 `--node` 指向 `[ha_cluster]` 的全部成员；应运行在带有对应 namespace
配置的主机上。两个实例共享同一个主机网络，端口必须不同。`autostart` 会按实例
收敛 `pg autostart enable|disable --haproxy --name <name>`；未设置时默认 `false`。

## 运行

```bash
# 一次完成 etcd + Patroni + HAProxy
uv run ansible-playbook playbooks/apply.yaml --tags addon,ha

# 仅收敛集群与代理（etcd 必须已存在）
uv run ansible-playbook playbooks/apply.yaml --tags ha

# 仅 HAProxy
uv run ansible-playbook playbooks/apply.yaml --tags haproxy

# 只读的拓扑与代理状态
uv run ansible-playbook playbooks/ha-status.yaml
```

## 销毁顺序

三个 state 开关互相独立；按依赖顺序销毁：

1. HAProxy：设置 `pg_ha_haproxy.clusters[].state: absent`，运行
   `--tags haproxy`。
2. Patroni 集群：设置 `pg_ha_clusters[].state: absent`（要删除成员数据目录再加
   `clean_data: true`），运行 `--tags ha`。
3. etcd：设置 `pg_etcd_state: absent`，运行 `--tags addon`。

HA 相关 play 会在首台主机清理 DCS scope，并串行移除各主机的本地成员；后续主机
对 scope 是 no-op。`clean_data` 删除成员数据目录，但保留空的
`addon/patroni/<scope>` 目录。etcd 移除总会删除其数据目录。以上操作都不会删除
备份容器、pgBackRest 配置和 S3 备份。
