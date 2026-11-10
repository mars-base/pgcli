---
title: "插件"
description: "配置 addon_<type>_<mode> 分组 —— 安装/移除顶层或实例级插件，并管理共享 etcd 分组"
weight: 40
---

插件在 `group_vars/addon_<type>_<mode>/main.yml` 中以 `addon_<group>_addons`
列表声明。`group_vars/all` 会把 `--limit` 所选分组的列表读入 `pgcli_addons`，
因此一次运行只选择一组插件：

```bash
uv run ansible-playbook playbooks/apply.yaml \
  --limit addon_pgbouncer_single --tags addon
```

仓库为每种插件类型和安装形态各提供一个分组：`addon_<type>_single`（1 台主机）
与 `addon_<type>_cluster`（3 或 4 台主机）。集群形态见
[插件集群](./addon-cluster/)。`ha_etcd` 与 S3 分组直接定义 `pgcli_addons`，
用法不变。`type` 接受 `pg addon install` 支持的所有插件——完整列表见
[插件参考](../../addon/)（pgbouncer、pgdog、etcd、haproxy、
minio/silo/rustfs、redis、nginx、prometheus、kafka 等）。

## 条目字段

| 字段 | 含义 |
|------|------|
| `type` | `pg addon install` 支持的插件类型（见[插件参考](../../addon/)） |
| `name` | 顶层插件实例名（例如本机的 etcd 成员） |
| `pg_name` | 与 `name` 二选一：远程 PgBouncer/PostgREST 的目标名（`--pg-name`） |
| `dsn` | 远程 PostgreSQL DSN（`--dsn`；仅 pgbouncer、postgrest、pgexporter） |
| `instance` | 与 `name` 二选一：把插件挂到某个 pgcli 实例上 |
| `state` | `present`（默认）或 `absent` |
| `params` | 条目级完整参数列表，覆盖该条目的 `addon_<group>_params` |
| `args` | 条目级额外参数，追加在最终参数之后 |
| `conf` | 内联配置文件内容，覆盖 `addon_<group>_conf`（仅 nginx、haproxy、prometheus、alertmanager） |
| `clean_data` | 与 `state: absent` 搭配，在插件支持时一并删除数据（默认 `true`，设为 `false` 保留数据） |
| `force` | 与 `state: present` 搭配，用 `pg addon install --force` 重建 |
| `autostart` | `true`（默认）启用开机自启，`false` 关闭 |

每个条目必须有 `type`，并且 `name`/`pg_name`/`instance` 恰好设置一个；角色会在执行前断言。

## 公共参数

同一分组内所有条目共用的参数可以在列表旁声明一次，变量名为
`addon_<group>_params`，类型是原始的 `pg addon install` 参数列表。未设置
`params` 的条目会继承这份列表。

条目也可以设置自己的 `params`，作为该实例完整的参数列表；它会**整体替换**
`addon_<group>_params`，因此同类型的多个实例可以使用不同端口、数据目录、内存上限等。
条目级 `args` 仍然在最终参数之后追加。

```yaml
addon_redis_single_addons:
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
（可以用 YAML anchor）。变量名是 `addon_<group>_params`，`<group>` 为 `--limit`
所选分组（例如 `addon_redis_single_params` 或
`addon_clickhouse_keeper_cluster_params`）。直接定义 `pgcli_addons` 的分组回退到
`addon_<type>_params`。

## 内联配置文件

nginx、haproxy、prometheus、alertmanager 支持 `--conf-file`。把文件内容放到
`addon_<group>_conf`，不用在目标机上另外维护路径：

```yaml
addon_haproxy_single_conf: |
  global
      maxconn 1000
  defaults
      mode tcp
      timeout connect 4s
      timeout client 30m
      timeout server 30m
  # ... frontend/backend ...

addon_haproxy_single_addons:
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

本仓库为每种类型各提供一个单机分组 `addon_<type>_single`（各 1 台主机），
支持的类型另有集群分组 `addon_<type>_cluster`（3 或 4 台主机）。每次用
`--limit addon_<type>_<mode>` 只运行一个分组；集群布局见
[插件集群](./addon-cluster/)。各[插件页面](../../addon/)记录集群模式的产品参数；
S3 与 HA 分组见本站的对应页面。

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

`group_vars/addon_nodeexporter_single/main.yml.example` 在分组列表中声明一个
顶层插件：

```yaml
addon_nodeexporter_single_addons:
  - type: nodeexporter
    name: ne1
    state: present
```

`--limit addon_nodeexporter_single` 会让 `group_vars/all` 把该列表读入分组的
`pgcli_addons`（该组只有一台主机）。

直接设置 `pgcli_addons` 的分组使用相同的条目格式。
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

属于某个 PostgreSQL 实例的插件改用 `instance` 而不是 `name`。
`group_vars/addon_pgbouncer_single/main.yml.example` 在同一个分组里同时声明
实例与插件：

```yaml
addon_pgbouncer_single_instances:
  - name: your_pg_ins01
    host: "{{ inventory_hostname }}"
    state: started

addon_pgbouncer_single_addons:
  - type: pgbouncer
    instance: your_pg_ins01
    state: present
```

用 `--limit addon_pgbouncer_single --tags instance,addon` 先创建实例再装插件。
角色最终执行 `pg addon install pgbouncer -i your_pg_ins01`。

## 远程 PostgreSQL 示例

设置 `dsn` 即可指向 pgcli 不管理的 PostgreSQL 服务器。PgBouncer 和 PostgREST
还需要 `pg_name`，即远程目标在配置中的名字；pgexporter 仍用 `name`。pgbouncer
分组把远程形态放在 `addon_pgbouncer_single_remote_dsn` 后面：留空时连接本地
`your_pg_ins01` 实例，设置后不再创建本地实例，改为连接该服务器：

```yaml
addon_pgbouncer_single_addons:
  - type: pgbouncer
    pg_name: remote-proj01
    dsn: postgres://admin:secret@db.example.com:5432/proj01
    state: present
```

pgexporter 同样声明在 `addon_pgexporter_single_addons` 中，但用 `name` 而不是
`pg_name`：

这些条目属于顶层（`addons.<type>.<name>`），因此 `state: absent` 和开机自启
分别走 `--pg-name`（远程 pgbouncer/postgrest）或 `--name`（pgexporter）。
pgdog 没有 `--dsn` 参数；请改用 `params` 传递
`--backend NAME=HOST:PORT:DB` 与 `--user NAME:PASSWORD`。

## 多个条目

每个分组列表都是 YAML 列表，按顺序逐个收敛。上面的 redis 示例就在一个分组里
声明了两个同类型实例。直接设置 `pgcli_addons` 的分组（例如 `ha_etcd`）还可以在
同一列表里混合类型：

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
# 收敛一个单机插件分组
uv run ansible-playbook playbooks/apply.yaml --limit addon_nginx_single --tags addon

# 收敛一个集群分组
uv run ansible-playbook playbooks/apply.yaml --limit addon_etcd_cluster --tags addon

# 共享 etcd DCS 分组（直接定义 pgcli_addons）
uv run ansible-playbook playbooks/apply.yaml --tags addon --limit ha_etcd
```

- `state: present`：仅当配置中缺少该 name/instance 时才安装；设置
  `force: true` 会强制重建。同时按 `autostart` 调用
  `pg autostart enable|disable --<type> ...` 收敛开机自启；未设置时默认开启。
- `state: absent`：执行 `pg addon remove <type> --name <name>`；远程
  pgbouncer/postgrest 用 `--pg-name <pg_name>`，实例级插件用 `-i
  <instance>`；默认一并删除数据，插件支持时可用 `clean_data: false` 保留。
  etcd 的移除路径总会删除其数据目录。
- 不带 `addon_*` limit 运行 `--tags addon` 时，插件组的 `pgcli_addons` 为空，
  这些主机是 no-op；直接定义 `pgcli_addons` 的分组（ha_etcd、S3）仍会收敛。

## etcd 生命周期

etcd 条目的 `state` 控制成员生命周期：`present`（插件默认值）创建/收敛本地
成员，`absent` 移除本地成员。

只有在 Patroni 集群销毁**之后**才能设为 `absent`：它们把该 etcd 当作 DCS。
由于 `serial: 1`，成员按主机逐台移除；etcd 移除命令本身会删除数据目录。重建时
改回 `present` 并重跑 `--tags addon`——首台 bootstrap，其余重新加入。
