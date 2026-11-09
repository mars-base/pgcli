---
title: "插件"
description: "配置 pgcli_addons —— 安装/移除顶层或实例级插件，并管理共享 etcd 分组"
weight: 40
---

插件在 `pgcli_addons` 中声明（`group_vars/all/main.yml` 默认为空列表）。各部署
模式分组添加自己的列表；`ha_etcd` 分组用同一机制管理共享 etcd DCS。`type` 接受
`pg addon install` 支持的所有插件——完整列表见[插件参考](../../addon/)
（pgbouncer、pgdog、etcd、haproxy、minio/silo/rustfs、redis、nginx、
prometheus、kafka 等）。

## 条目字段

| 字段 | 含义 |
|------|------|
| `type` | `pg addon install` 支持的插件类型（见[插件参考](../../addon/)） |
| `name` | 顶层插件实例名（例如本机的 etcd 成员） |
| `instance` | 与 `name` 二选一：把插件挂到某个 pgcli 实例上 |
| `state` | `present`（默认）或 `absent` |
| `params` | 条目级完整参数列表，覆盖该条目的 `addon_<type>_params` |
| `args` | 条目级额外参数，追加在最终参数之后 |
| `conf` | 内联配置文件内容，覆盖 `addon_<type>_conf`（仅 nginx、haproxy、prometheus、alertmanager） |
| `clean_data` | 与 `state: absent` 搭配，在插件支持时一并删除数据（默认 `true`，设为 `false` 保留数据） |
| `force` | 与 `state: present` 搭配，用 `pg addon install --force` 重建 |
| `autostart` | `true`（默认）启用开机自启，`false` 关闭 |

每个条目必须有 `type`，并且 `name`/`instance` 恰好设置一个；角色会在执行前断言。

## 公共参数

同一种 add-on 的所有实例共用的参数可以在列表旁声明一次，变量名为
`addon_<type>_params`，类型是原始的 `pg addon install` 参数列表。未设置 `params`
的条目会继承这份列表。

条目也可以设置自己的 `params`，作为该实例完整的参数列表；它会**整体替换**
`addon_<type>_params`，因此同类型的多个实例可以使用不同端口、数据目录、内存上限等。
条目级 `args` 仍然在最终参数之后追加。

```yaml
addon_redis_addons:
  - type: redis
    name: cache
    state: present
    params:
      - "--port"
      - "6379"
      - "--maxmemory"
      - "256mb"
  - type: redis
    name: session
    state: present
    params:
      - "--port"
      - "6380"
      - "--maxmemory"
      - "512mb"
      - "--listen"
      - "127.0.0.1"
```

同一类型下条目名必须唯一；每个实例在 pgcli 配置中存放在
`addons.<type>.<name>`。因为 `params` 是整体替换，公共 flag 需要在每个条目里重复
（可以用 YAML anchor）。变量名中的类型会把 `-` 换成 `_`（例如
`addon_clickhouse_keeper_params`）。

## 内联配置文件

nginx、haproxy、prometheus、alertmanager 支持 `--conf-file`。把文件内容放到
`addon_<type>_conf`，不用在目标机上另外维护路径：

```yaml
addon_haproxy_conf: |
  global
      maxconn 1000
  defaults
      mode tcp
      timeout connect 4s
      timeout client 30m
      timeout server 30m
  # ... frontend/backend ...

addon_haproxy_addons:
  - type: haproxy
    name: lb1
    state: present
```

角色会把内容写到 `<pgcli_config_path 所在目录>/addons/<type>-<name>.conf`
（权限 `0600`）并传入 `--conf-file`；`pg addon install` 仍会用 `haproxy -c` /
`nginx -t` 校验。条目级 `conf` 会覆盖分组变量。内联配置不能与显式 `--conf-file`
或渲染同一文件的 flag 同时使用（nginx `--upstream`、prometheus
`--scrape`/`--remote-write-url`、alertmanager `--webhook-url`）。修改内容不会自动
重装已存在的插件：设置 `force: true`（或先移除）才会应用。

## 单机安装与集群安装

`安装形态` 指的是该插件以单机方式安装，还是以集群方式安装，而不是它能不能在多台
主机上各装一份：

- **单机**：只支持单机安装；多台主机上部署的多个实例彼此独立，不组成集群。
- **单机 + 集群**：既可以单机安装，也可以按集群模式安装。
- **仅集群**：只能以集群方式安装（需要法定人数）。

本仓库 `group_vars` 的示例覆盖两种形态：`addon_single` 对应单机安装，
[插件集群](./addon-cluster/) 对应多主机的 `addon_cluster` 分组。各[插件
页面](../../addon/)记录集群模式的产品参数；S3 与 HA 分组见本站的对应页面。

| 插件 | 安装形态 | 说明 |
|------|----------|------|
| pgbouncer | 单机 | 实例级 |
| etcd | 单机 + 集群 | `--cluster`；HA 的共享 DCS |
| pgdog | 单机 + 集群 | 单后端 vs 分片后端 |
| haproxy | 单机 | 每个代理独立，不组成集群 |
| minio | 单机 + 集群 | SNSD/SNMD vs MNMD |
| silo | 单机 + 集群 | SNSD/SNMD vs MNMD |
| rustfs | 单机 + 集群 | 集群仅 MNMD（不支持 MNSD） |
| garage | 单机 | v1 只支持单机 |
| seaweedfs | 单机 + 集群 | `weed mini` vs `weed server` |
| redis | 单机 + 集群 | 单机 vs `--cluster` 分片 |
| predixy | 单机 | 一个代理可接一个或多个 Redis 后端 |
| postgrest | 单机 | |
| pgadmin | 单机 | |
| grafana | 单机 | |
| prometheus | 单机 | |
| alertmanager | 单机 | pgcli 关闭了 HA gossip |
| victoriametrics | 单机 | |
| nodeexporter | 单机 | |
| pgexporter | 单机 | |
| documentdb | 单机 | 需要本机 PG 18 实例 |
| vmcluster | 单机 + 集群 | 全角色收敛单机 vs 按角色拆分 |
| vlcluster | 单机 + 集群 | 全角色收敛单机 vs 按角色拆分 |
| victorialogs | 单机 | |
| vlagent | 单机 | |
| vmalert | 单机 | 多副本 HA 需 `--external-label` |
| influxdb | 单机 | v1 无 HA |
| influx2tsdb | 单机 | |
| clickhouse | 单机 + 集群 | 配合 Keeper 的静态复制集群 |
| clickhouse-keeper | 仅集群 | 3 或 5 成员 Raft 法定人数 |
| kafka | 单机 + 集群 | KRaft controller 法定人数 |
| rabbitmq | 单机 + 集群 | etcd 节点发现 |
| rocketmq | 单机 + 集群 | standalone vs DLedger 集群 |
| gitea | 单机 | |
| beegfs | 单机 | v1 每台主机一个文件系统 |
| juicefs | 单机 | 宿主二进制；外部 S3 + 元数据 |
| zerofs | 单机 | 宿主二进制；外部 S3 |
| duckdb | 单机 | 宿主二进制 |
| nginx | 单机 | 单个反向代理 |

## 顶层插件示例

`group_vars/ha_etcd/main.yml.example` 为每台主机生成一个 etcd 成员：

```yaml
pgcli_addons:
  - type: etcd
    name: "etcd-{{ groups[pg_etcd_group].index(inventory_hostname) + 1 }}"
    state: present
    autostart: true
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
  - type: redis
    name: cache
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
  `force: true` 会强制重建。同时按 `autostart` 调用
  `pg autostart enable|disable --<type> ...` 收敛开机自启；未设置时默认开启。
- `state: absent`：执行 `pg addon remove <type> --name <name>`（或 `-i
  <instance>`）；默认一并删除数据，插件支持时可用 `clean_data: false` 保留。
  etcd 的移除路径总会删除其数据目录。
- 未声明插件的 `pgcli_addons: []` 主机是 no-op，所以对整个 inventory 执行
  `--tags addon` 是安全的。

## etcd 生命周期

etcd 条目的 `state` 控制成员生命周期：`present`（插件默认值）创建/收敛本地
成员，`absent` 移除本地成员。

只有在 Patroni 集群销毁**之后**才能设为 `absent`：它们把该 etcd 当作 DCS。
由于 `serial: 1`，成员按主机逐台移除；etcd 移除命令本身会删除数据目录。重建时
改回 `present` 并重跑 `--tags addon`——首台 bootstrap，其余重新加入。
