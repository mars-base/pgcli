---
title: "插件"
description: "配置 pgcli_addons —— 安装/移除顶层或实例级插件，并管理共享 etcd 分组"
weight: 40
---

插件在 `pgcli_addons` 中声明（`group_vars/all/main.yml` 默认为空列表）。各部署
模式分组添加自己的列表；`ha_etcd` 分组用同一机制管理共享 etcd DCS。

## 条目字段

| 字段 | 含义 |
|------|------|
| `type` | 插件类型，如 `etcd`、`haproxy`、`pgbouncer`、`minio` |
| `name` | 顶层插件实例名（例如本机的 etcd 成员） |
| `instance` | 与 `name` 二选一：把插件挂到某个 pgcli 实例上 |
| `state` | `present`（默认）或 `absent` |
| `args` | 原样追加到 `pg addon install` 的参数列表 |
| `clean_data` | 与 `state: absent` 搭配，在插件支持时一并删除数据 |
| `force` | 与 `state: present` 搭配，用 `pg addon install --force` 重建 |

每个条目必须有 `type`，并且 `name`/`instance` 恰好设置一个；角色会在执行前断言。

## 顶层插件示例

`group_vars/ha_etcd/main.yml.example` 为每台主机生成一个 etcd 成员：

```yaml
pg_etcd_state: present

pgcli_addons:
  - type: etcd
    name: "etcd-{{ groups[pg_etcd_group].index(inventory_hostname) + 1 }}"
    state: "{{ pg_etcd_state }}"
    args: "{{ ['--cluster', 'ha', '--advertise-host', _ha_etcd_advertise_host]
             + (['--join', _ha_etcd_bootstrap_endpoint]
                if inventory_hostname != (groups[pg_etcd_group] | first) else []) }}"
```

分组中的首台主机负责 bootstrap，其余主机通过 `--join` 加入一个运行中的成员。
插件 play 使用 `serial: 1`，因此下一台加入前 bootstrap 主机一定已就绪。

## 实例级插件示例

属于某个 PostgreSQL 实例的插件改用 `instance` 而不是 `name`：

```yaml
pgcli_addons:
  - type: pgbouncer
    instance: pg01
    state: present
```

角色执行 `pg addon install pgbouncer -i pg01`，因此该实例必须已在同一轮
`pgcli_instances` 中收敛（`apply.yaml` 中插件 play 位于实例 play 之后）。

## 多个插件

`pgcli_addons` 是列表，按顺序逐个收敛：

```yaml
pgcli_addons:
  - type: etcd
    name: etcd-1
    args: [...]
  - type: semaphore
    name: ui
    state: absent
```

## 安装与移除

```bash
# 在所有声明了 pgcli_addons 的主机上收敛
uv run ansible-playbook playbooks/apply.yaml --tags addon

# 只操作 etcd 分组
uv run ansible-playbook playbooks/apply.yaml --tags addon --limit ha_etcd
```

- `state: present`：仅当配置中缺少该 name/instance 时才安装；设置
  `force: true` 会强制重建。
- `state: absent`：执行 `pg addon remove <type> --name <name>`（或 `-i
  <instance>`）；对默认保留数据的插件可加 `clean_data: true`。etcd 的移除路径
  总会删除其数据目录。
- 未声明插件的 `pgcli_addons: []` 主机是 no-op，所以对整个 inventory 执行
  `--tags addon` 是安全的。

## etcd 生命周期

`pg_etcd_state` 是分组级开关：

```yaml
pg_etcd_state: present   # 创建/收敛本地成员（默认）
# pg_etcd_state: absent  # 移除本地成员
```

只有在 Patroni 集群销毁**之后**才能设为 `absent`：它们把该 etcd 当作 DCS。
由于 `serial: 1`，成员按主机逐台移除；etcd 移除命令本身会删除数据目录。重建时
改回 `present` 并重跑 `--tags addon`——首台 bootstrap，其余重新加入。
