---
title: "剧本"
description: "apply.yaml、deploy-s3.yaml、ha-status.yaml、ping.yaml 的用法 —— play 顺序、tags、limit、幂等与生命周期顺序"
weight: 20
---

所有命令都在 `ansible/` 目录下通过固定的 `uv` 环境执行。不指定 `--limit` 时，
`apply.yaml` 作用于 inventory 中的全部主机；实际使用中通常选择某个部署模式分组，
或用 `--limit addon_<type>_<single|cluster>` 选择一个插件分组。

```bash
cd ansible
uv run ansible-playbook playbooks/apply.yaml
```

## `ping.yaml`

对所有 inventory 主机做 SSH 与 Python 连通性冒烟测试：

```bash
uv run ansible-playbook playbooks/ping.yaml
uv run ansible-playbook playbooks/ping.yaml --limit crosshost
```

## `apply.yaml`

通用 play，不包含任何模式数据：它只收敛被选中主机在 `group_vars/` 中声明的状态。

| # | Play | 主机 | serial | Tag |
|---|------|------|--------|-----|
| 1 | 校验插件选择 | `all` | – | `always` |
| 2 | 安装并配置 pgcli | `all` | – | `pgcli` |
| 3 | PostgreSQL 实例 | `all` | 1 | `instance` |
| 4 | 插件 | `all` | 1 | `addon` |
| 5 | 组建 Redis 集群 | `all` | – | `addon` |
| 6 | Patroni HA 成员 | `ha_cluster` | 1 | `ha` |
| 7 | Patroni HA 备份收敛 | `ha_cluster` | – | `ha` |
| 8 | Patroni HA 代理 | 代理主机 | – | `ha`、`haproxy` |

`serial: 1` 是刻意的：首台主机先安装插件或 bootstrap etcd，下一台再加入；
跨机副本也要求主库先存在。

插件用 `--limit addon_<type>_<single|cluster>` 选择；`group_vars/all` 会把该分组的
`addon_<kind>_addons` 列表读入 `pgcli_addons`。以 `addon_` 开头但不是“存在且带模式
后缀的精确分组名”的 limit 会在校验 play 失败；不存在的分组会在 Ansible 匹配
limit 的阶段报错。不带这类 limit 时列表为空，插件 play 是 no-op。

Redis 组建 play 在所有成员都存在之后运行（插件 play 是 serial）。只有
`--limit addon_redis_cluster` 且条目为 present 时才生效：等待所有成员应答，
必要时执行一次集群组建，然后等待全部 16384 个 slot 分配完成。重跑会跳过组建步骤。

```bash
# 完整部署某个模式
uv run ansible-playbook playbooks/apply.yaml --limit crosshost

# 只收敛实例（pgcli 必须已安装并初始化）
uv run ansible-playbook playbooks/apply.yaml --limit single --tags instance

# 全新主机：一次完成安装与创建
uv run ansible-playbook playbooks/apply.yaml --limit single --tags pgcli,instance

# 只操作插件的一个分组（把条目改成 absent 即可移除）
uv run ansible-playbook playbooks/apply.yaml --limit addon_etcd_cluster --tags addon

# HA 成员 + 收敛；HAProxy 有独立 tag
uv run ansible-playbook playbooks/apply.yaml --tags ha
uv run ansible-playbook playbooks/apply.yaml --tags haproxy

# 单台主机
uv run ansible-playbook playbooks/apply.yaml --limit db-01 --tags instance
```

tag 不会自动带上依赖。首次部署 HA 需要先跑 etcd 插件 play（`pgcli,addon,ha`
或完整运行），之后单独 `--tags ha` 才能成功。HA 相关 play 只作用于
`[ha_cluster]` 中的主机；插件 play 在 `[ha_etcd]` 主机上收敛 etcd。

## `deploy-s3.yaml`

部署独立的 S3 兼容存储（`minio`、`silo` 或 `rustfs`），作为 pgBackRest 的
`s3` 备份目标。存储类型取自 `[s3]` 下当前激活的类型分组；`pgcli_s3_type`
可选，若设置必须一致。

```bash
# 当前激活的存储分组全部节点
uv run ansible-playbook playbooks/deploy-s3.yaml

# 显式指定存储类型
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_group=minio_s3

# 只部署部分节点（单节点用单主机模式）
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_nodes=db-01
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_nodes=db-01,db-02

# 强制重建容器
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_force=true
```

单机/多机布局与 TLS 选项见 [S3 存储](./s3/)页面。

## `ha-status.yaml`

只读。打印每个集群的实时 Patroni 拓扑（角色、状态、LSN、延迟）以及前面的
HAProxy 实例。命令只在选中的首台主机上执行一次；任何成员都能读取共享 DCS 视图。

```bash
uv run ansible-playbook playbooks/ha-status.yaml
uv run ansible-playbook playbooks/ha-status.yaml -l ha_cluster
uv run ansible-playbook playbooks/ha-status.yaml -l db-02
```

## 幂等

任何 playbook 都可以安全重跑。每个变更任务都会先检查当前 pgcli 配置与容器状态，
无变化的主机报告 `ok`。唯一例外是 `pgcli_install_force: true`，它按设计每次都会
重跑安装脚本。

## 生命周期开关

| 资源 | 声明 | 默认 | `absent` 效果 |
|------|------|------|---------------|
| 实例 | `pgcli_instances[].state` | `started` | 删除容器与配置；`clean_data: true` 时同时删除数据/WAL 目录和 pgBackRest stanza |
| 插件 | `pgcli_addons[].state` | `present` | 删除插件容器与配置项；支持的插件可用 `clean_data` |
| HA 集群 | `pg_ha_clusters[].state` | `started` | 删除本地成员并清理 DCS scope；`clean_data: true` 删除成员数据目录（保留空的 `addon/patroni/<scope>` 目录） |
| HAProxy | `pg_ha_haproxy.clusters[].state` | `present` | 从代理主机移除实例并清理配置 |

HA 的销毁顺序：HAProxy → Patroni 集群 → etcd。etcd 是插件条目
（`pgcli_addons[].state`），移除时总会删除数据目录。HA 相关 play 不会删除
备份容器、pgBackRest 配置和 S3 备份。S3 存储在本 playbook 中只负责部署；销毁时在对应节点
执行 `pg addon remove <type> --name <name> --clean-data`。

## 日志

`ansible.cfg` 将运行日志写到 `var/ansible.log`，facts 缓存在 `var/facts/`。
两者都被 gitignore，连同所有正式的 `main.yml` 与 `secrets.yaml`。
