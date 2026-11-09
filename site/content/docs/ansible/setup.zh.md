---
title: "安装与示例文件"
description: "把仓库自带的 *.example 文件变成可用的 Ansible inventory 与变量，并验证连通性"
weight: 10
---

仓库从不提交与环境相关的文件。所有实际配置都由签入的 `*.example` 复制成正式文件名
后再修改；正式副本都被 `ansible/.gitignore` 忽略。

## 前置条件

- 一台安装了 [uv](https://docs.astral.sh/uv/) 的 Linux 或 macOS 控制机。
- 能通过 SSH 访问所有被管理主机。示例使用 `ansible_user=root`；play 不做提权，
  因此请使用具备角色所需权限的账号（podman、`/usr/local/bin`、systemd）。
- 目标主机为受支持的 Linux 发行版；缺少 podman 时由 `pgcli` 角色安装。

## 准备运行环境

```bash
cd ansible
uv sync
```

`pyproject.toml` 固定了 Ansible 版本（`ansible-core 2.18`）；始终用 `uv run` 执行
playbook，确保使用固定的环境。

## 复制示例文件

| 示例文件（已提交） | 正式文件（gitignored） | 用途 |
|--------------------|------------------------|------|
| `ansible.cfg.example` | `ansible.cfg` | inventory 路径、roles 路径、facts 缓存、`var/ansible.log`、SSH pipelining |
| `hosts.ini.example` | `hosts.ini` | inventory：部署模式分组、S3 分组、HA 角色分组 |
| `group_vars/all/main.yml.example` | `group_vars/all/main.yml` | 安装源/版本、配置路径、namespace、实例默认值 |
| `group_vars/single/main.yml.example` | `group_vars/single/main.yml` | 单机实例模式 |
| `group_vars/samehost/main.yml.example` | `group_vars/samehost/main.yml` | 主库 + 同机副本模式 |
| `group_vars/crosshost/main.yml.example` | `group_vars/crosshost/main.yml` | 主库 + 跨机副本模式、S3 备份变量 |
| `group_vars/ha_etcd/main.yml.example` | `group_vars/ha_etcd/main.yml` | 共享 etcd DCS |
| `group_vars/ha_cluster/main.yml.example` | `group_vars/ha_cluster/main.yml` | Patroni 集群 + HAProxy |
| `group_vars/{minio,silo,rustfs}_s3/main.yml.example` | 对应的正式文件 | S3 兼容存储部署 |

```bash
cp ansible.cfg.example ansible.cfg
cp hosts.ini.example hosts.ini
cp group_vars/all/main.yml.example group_vars/all/main.yml
cp group_vars/single/main.yml.example group_vars/single/main.yml
# 其他用到的分组同理
```

密钥文件没有带真实值的示例。自行创建
`group_vars/<group>/secrets.yaml`（或 `host_vars/<host>/secrets.yaml`）；所有
`secrets.yaml` 和正式 `main.yml` 都不入库。

## Inventory：`hosts.ini`

主机按**部署模式**分组，而不是按环境分组。play 始终作用于 `all`，用
`--limit <group>` 选择本次要操作的模式。

```ini
[single]
db-01 ansible_host=192.0.2.10 private_ip=192.0.2.10 ansible_user=root ansible_port=22

[crosshost]
db-01 ansible_host=192.0.2.10 private_ip=192.0.2.10 ansible_user=root ansible_port=22
db-02 ansible_host=192.0.2.11 private_ip=192.0.2.11 ansible_user=root ansible_port=22

[single:vars]
ansible_group_priority=20
[crosshost:vars]
ansible_group_priority=20
```

- **一次只启用一种模式分组**；每种模式维护自己的 `pgcli_instances`。模式分组带
  `ansible_group_priority=20`，其变量优先于 `all` 的默认值。
- **顺序很重要。** 实例 play 使用 `serial: 1`；主库要列在依赖它的跨机副本主机之前。
- 节点间通信（复制、etcd、备份 SSH）用 `private_ip`；SSH 仍走 `ansible_host`，
  公有云上后者通常是 EIP。
- S3 部分在 `[s3]` 下只激活**一个**存储类型子组（`minio_s3`、`silo_s3` 或
  `rustfs_s3`）。
- `[ha_etcd]` 与 `[ha_cluster]` 是角色标记分组。etcd 首台负责 bootstrap；
  Patroni 成员名按 `[ha_cluster]` 顺序对应。

## 按业务类型选择分组

每种业务场景对应一个 inventory 分组和一个 `group_vars` 文件。**一次只启用一种
场景分组**；未使用的分组保留声明但置空，保证 play 的主机模式仍能解析。

| 业务类型 | Inventory 分组 | `group_vars` 文件 | 运行 |
|----------|----------------|-------------------|------|
| 单机实例，无副本 | `[single]` | `group_vars/single/main.yml` | `apply.yaml --limit single` |
| 主库 + 同机副本 | `[samehost]` | `group_vars/samehost/main.yml` | `apply.yaml --limit samehost` |
| 主库 + 跨机副本 | `[crosshost]` | `group_vars/crosshost/main.yml` | `apply.yaml --limit crosshost` |
| Patroni HA 集群 | `[ha_etcd]` + `[ha_cluster]` | `group_vars/ha_etcd/main.yml` + `group_vars/ha_cluster/main.yml` | `apply.yaml --tags addon,ha` |
| S3 兼容备份存储 | `[s3]` 下的 `[minio_s3]` / `[silo_s3]` / `[rustfs_s3]` 之一 | 对应的 `group_vars/<type>_s3/main.yml` | `deploy-s3.yaml` |

前三种是互斥的部署模式：各模式的主机经常重叠，而每种启用的模式都会提供自己的
`pgcli_instances` 列表，同时启用可能混合实例列表。HA 与 S3 是带独立分组和 play
选择的支撑部署；主机属于 HA 集群时，不要再把它列进已激活的模式分组。

## 全局变量：`group_vars/all/main.yml`

示例文件里每个字段都有注释。最重要的几块：

| 变量 | 含义 |
|------|------|
| `pgcli_install_version` | 安装的 release tag；留空表示最新版 |
| `pgcli_install_force` | 即使 `pg` 已存在也重跑安装脚本 |
| `pgcli_install_binary_url`、`pgcli_images_http_url` | 离线/隔离环境的安装源 |
| `pgcli_install_path` | 二进制路径，默认 `/usr/local/bin/pg` |
| `pgcli_namespace_path` | 主机默认 namespace 目录；留空用 `/root/.pgcli/pg.yaml` |
| `pgcli_namespaces` | namespace 名称 → `pg.yaml` 目录的映射 |
| `pgcli_config_defaults` | `pg config init` 的初始值：namespace、`base_dir`、`backup_dir`、端口段 |
| `pgcli_config_overrides` | 分组级 namespace 覆盖（各模式示例里设置） |
| `pgcli_instance_defaults` | 实例默认 `state`、`autostart`、`backup_target`、`password_length`、`extensions`、`clean_data` |
| `pgcli_instances` | 期望实例列表（通常放在各模式分组里） |
| `pgcli_addons` | 主机期望的插件列表 |

`pgcli_config_defaults` 只在首次 `pg config init` 时生效；之后修改不会重写已有的
`pg.yaml`。

## 首次运行

```bash
cd ansible
uv run ansible-inventory --graph   # 校验 hosts.ini 并打印分组结构
uv run ansible-playbook playbooks/ping.yaml
uv run ansible-playbook playbooks/apply.yaml --limit single
```

完整的 play 顺序与每个 tag 的作用见[剧本](./playbooks/)页面。

## Tips

仓库只跟踪 `*.example` 文件；正式的 `main.yml`、`hosts.ini`、`ansible.cfg`、
`secrets.yaml` 和 `var/` 都被 gitignore。如果你自己管理维护 Ansible 工程
（例如把配置提交到自己的仓库），推荐把密码、S3 secret key 和证书单独放进
`secrets.yaml`，而不是直接写在 `main.yml` 里。`group_vars/<group>/secrets.yaml`
和 `host_vars/<host>/secrets.yaml` 都会被自动加载，变量解析方式不变，同时密钥
文件不会进入版本库（本仓库的 `.gitignore` 已覆盖该路径）。
