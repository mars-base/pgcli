---
title: "Ansible 部署"
description: "用仓库自带的 Ansible playbook 声明式管理 pgcli 资源 —— 实例、副本、插件、Patroni HA 集群与 S3 存储"
weight: 12
icon: fa-solid fa-robot
menus:
  main:
    identifier: docs-ansible
    parent: docs
    weight: 12
    params:
      icon: fa-solid fa-robot
cascade:
  type: docs
  footer_style: slim
---

pgcli 仓库的 `ansible/` 目录把 CLI 变成声明式部署工具。不必在每台主机上手动执行
`pg create`、`pg addon install` 和 `pg ha create`，只需在 `group_vars/` 里描述期望
状态，然后运行一个 playbook。重复运行会持续收敛到该状态，可以放心地反复执行。

## 管理哪些资源

| 资源 | Ansible 输入 | 角色 |
|------|--------------|------|
| `pg` 二进制、podman、主机配置 | `group_vars/all` 中的 `pgcli_*` 变量 | `pgcli` |
| PostgreSQL 实例与只读副本 | `pgcli_instances` | `pg_instance` |
| 主机长驻进程（webhook 等） | `pgcli_daemons` | `pg_daemon` |
| 插件（etcd、pgbouncer、MinIO 等） | `pgcli_addons` | `pg_addon` |
| Patroni HA 集群及其 etcd DCS | `pg_ha_clusters`、`pgcli_addons`（etcd） | `pg_ha`、`pg_addon` |
| HA 集群前置的 HAProxy | `pg_ha_haproxy` | `pg_ha_haproxy` |
| 备份用的独立 S3 兼容存储 | `pgcli_s3_*` | `pg_s3` |

所有变更任务都会先检查当前 pgcli 配置与容器状态，无变化的主机报告 `ok` 而不是
`changed`。

不同业务场景通过 inventory 分组和 `group_vars` 文件隔离：单机、同机副本、跨机
副本、实例与插件混合、Patroni HA、S3 存储，以及按类型拆分的插件分组。一次只
启用一种场景分组；对应关系见[安装与示例文件](./setup/)。

## 仓库结构

```text
ansible/
├── ansible.cfg.example      # 复制为 ansible.cfg
├── hosts.ini.example        # 复制为 hosts.ini（gitignored）
├── pyproject.toml           # 用 uv 固定 Ansible 版本
├── group_vars/
│   ├── all/main.yml.example        # 全局默认：安装、配置、实例默认值
│   ├── single/ samehost/ crosshost/  # 每种部署模式一个分组
│   ├── observable/                   # 机房监控 + 告警场景（含 exporter、Prometheus、Alertmanager）
│   ├── minio_s3/ silo_s3/ rustfs_s3/ # S3 存储类型
│   ├── addon_<type>_single/         # 每种单机插件一个分组
│   ├── addon_<type>_cluster/        # 每种多主机插件集群一个分组
│   └── ha_etcd/ ha_cluster/          # 共享 DCS 与 Patroni 成员
├── host_vars/               # 可选的单主机覆盖
├── misc/
│   ├── prometheus/          # Prometheus 告警规则文件
│   └── webhook/             # Alertmanager webhook 接收端
├── playbooks/
│   ├── apply.yaml           # 安装 + 实例 + daemon + 插件 + HA
│   ├── deploy-s3.yaml       # 独立 S3 存储
│   ├── ha-status.yaml       # 只读的 Patroni/HAProxy 状态
│   └── ping.yaml            # 连通性冒烟测试
└── roles/                   # pgcli、pg_instance、pg_daemon、pg_addon、addon_cluster_shared、pg_ha、pg_ha_haproxy、pg_s3
```

## 子页面

| 页面 | 内容 |
|------|------|
| [快速部署](./quick-deploy/) | 可直接粘贴的命令：一台主机、一个单机实例，从 clone 到 `pg list` |
| [安装与示例文件](./setup/) | 每个 `*.example` 对应哪个实际文件、`hosts.ini` 分组、`group_vars` 字段、首次运行 |
| [离线与隔离环境安装](./offline/) | 局域网 HTTP 服务器：镜像清单与懒加载、`pg`/podman 二进制、运行时主机二进制与 DocumentDB DEB |
| [出网代理](./proxy/) | 隔离机房的出网通道：Privoxy add-on、全局变量、`NO_PROXY`、手动 SSH 用法与验证 |
| [剧本](./playbooks/) | `ping.yaml`、`apply.yaml`、`resources.yaml`、`deploy-s3.yaml`、`ha-status.yaml`、`passwords.yaml`：tags、`--limit`、幂等、生命周期顺序 |
| [实例与副本](./instances/) | `single`/`samehost`/`crosshost` 示例文件与 `pgcli_instances` 全部字段 |
| [机房可观测部署](./observability/) | 大量机器的监控与告警：exporter、Prometheus、告警规则、Alertmanager、webhook daemon、Grafana |
| [插件](./addons/) | `addon_<type>_<mode>` 条目：安装/移除、实例级插件、etcd |
| [插件集群](./addon-cluster/) | 多主机插件集群：每种类型一个 inventory 分组、共享密钥/CA、法定人数布局、生命周期 |
| [HA 集群](./ha/) | `ha_etcd`/`ha_cluster` 示例文件、`pg_ha_clusters`、HAProxy、基于 state 的销毁 |
| [S3 存储](./s3/) | 存储类型示例文件、`deploy-s3.yaml` 选项、作为备份目标使用 |
