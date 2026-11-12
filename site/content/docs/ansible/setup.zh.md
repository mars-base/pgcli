---
title: "安装与示例文件"
description: "把仓库自带的 *.example 文件变成可用的 Ansible inventory 与变量，并验证连通性"
weight: 10
---

仓库从不提交与环境相关的文件。所有实际配置都由签入的 `*.example` 复制成正式文件名
后再修改；正式副本都被 `ansible/.gitignore` 忽略。

## 前置条件

- 一台安装了 [uv](https://docs.astral.sh/uv/) 的 Linux 或 macOS 控制机。
- 能通过 SSH 访问所有被管理主机。示例使用 `ansible_user=root`；rootless 部署把
  SSH 连接账号与非 root 部署账号分开，连接账号仍需 sudo，因为主机初始化始终
  通过 `become` 执行。
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
| `group_vars/instance_addon_mixed/main.yml.example` | `group_vars/instance_addon_mixed/main.yml` | 一次运行同时创建实例与插件 |
| `group_vars/ha_etcd/main.yml.example` | `group_vars/ha_etcd/main.yml` | 共享 etcd DCS |
| `group_vars/ha_cluster/main.yml.example` | `group_vars/ha_cluster/main.yml` | Patroni 集群 + HAProxy |
| `group_vars/{minio,silo,rustfs}_s3/main.yml.example` | 对应的正式文件 | S3 兼容存储部署 |
| `group_vars/addon_<type>_single/main.yml.example` | 对应的正式文件 | 一种单机插件（1 台主机） |
| `group_vars/addon_<type>_cluster/main.yml.example` | 对应的正式文件 | 一种多主机插件集群（3–4 台主机） |
| `group_vars/rootless/main.yml.example` | `group_vars/rootless/main.yml` | `[rootless]` 主机的非 root 部署账号 |
| `group_vars/rootless_ha/main.yml.example` | `group_vars/rootless_ha/main.yml` | 非 root Patroni HA 场景（`[ha_cluster]` 的子组） |

```bash
cp ansible.cfg.example ansible.cfg
cp hosts.ini.example hosts.ini
cp group_vars/all/main.yml.example group_vars/all/main.yml
cp group_vars/single/main.yml.example group_vars/single/main.yml
cp group_vars/addon_nginx_single/main.yml.example group_vars/addon_nginx_single/main.yml
# 其他用到的分组同理
```

密钥文件没有带真实值的示例。自行创建
`group_vars/<group>/secrets.yaml`（或 `host_vars/<host>/secrets.yaml`）；所有
`secrets.yaml` 和正式 `main.yml` 都不入库。

## Inventory：`hosts.ini`

主机在 `[all]` 中定义一次（含连接变量），其他分组只写裸主机名；
`--limit <group>` 选择本次要操作的场景：

```ini
[all]
db-01 ansible_host=192.0.2.10 private_ip=192.0.2.10
db-02 ansible_host=192.0.2.11 private_ip=192.0.2.11
db-03 ansible_host=192.0.2.12 private_ip=192.0.2.12
db-04 ansible_host=192.0.2.13 private_ip=192.0.2.13

[all:vars]
ansible_user=root
ansible_port=22

[single]
db-01

[crosshost]
db-01
db-02

[single:vars]
ansible_group_priority=20
[crosshost:vars]
ansible_group_priority=20

[addon_pgbouncer_single]
db-01

[addon_etcd_cluster]
db-01
db-02
db-03
```

- **一次只启用一种模式分组**；每种模式维护自己的 `pgcli_instances`。模式分组带
  `ansible_group_priority=20`，其变量优先于 `all` 的默认值。
- **顺序很重要。** 实例 play 使用 `serial: 1`；主库要列在依赖它的跨机副本主机之前。
  插件分组同理：首台主机负责 bootstrap。
- 节点间通信（复制、etcd、备份 SSH）用 `private_ip`；SSH 仍走 `ansible_host`，
  公有云上后者通常是 EIP。
- S3 部分在 `[s3]` 下只激活**一个**存储类型子组（`minio_s3`、`silo_s3` 或
  `rustfs_s3`）。
- `[ha_etcd]` 与 `[ha_cluster]` 是角色标记分组。etcd 首台负责 bootstrap；
  Patroni 成员名按 `[ha_cluster]` 顺序对应。
- 插件分组命名为 `addon_<type>_single`（1 台）与 `addon_<type>_cluster`
  （3 或 4 台）。可以同时声明全部插件分组——变量名互不冲突，每次用 `--limit`
  选择一个。

## Rootless 部署

默认所有主机都以 `root` 运行 pgcli 与 podman。要以非 root 账号运行，把主机加入
`[rootless]`，并在 `group_vars/rootless/main.yml` 中启用：

```ini
[rootless]
db-01
```

```yaml
# group_vars/rootless/main.yml
pgcli_deploy_rootless: true
pgcli_deploy_user: pgcli
```

`hosts.ini` 里的 `ansible_user` 只是 SSH 连接账号，需具备 sudo 权限。
`pgcli_deploy_user` 才是拥有 pgcli 状态并运行 rootless podman 的账号，两者可以
不同；账号不存在时会自动创建（nologin shell、锁定密码、subuid/subgid 区间、
systemd lingering）。软件包、podman launcher 与主机初始化（uidmap、
`/etc/containers/policy.json`、userns、SELinux）仍以 SSH 账号通过 `become`
执行。`pg` 二进制仍位于 `/usr/local/bin/pg`。

rootless 容器使用 host 网络。绑定低于 `net.ipv4.ip_unprivileged_port_start`
（默认 1024）的端口会失败，因此 play 会把
`net.ipv4.ip_unprivileged_port_start=0` 持久化到
`/etc/sysctl.d/99-pgcli-rootless.conf`，并写入 `/etc/sudoers.d/pgcli-deploy`，
只授权部署账号执行一条命令：
`sysctl -w net.ipv4.ip_unprivileged_port_start=0`。除此之外没有任何 sudo 权限。
若该账号本身已有免密 sudo（以该账号执行 `sudo -n true` 成功），则规则冗余，
跳过该文件。

配置、镜像缓存、数据与备份会移到该账号的 home 下：

| Rootful（默认） | Rootless |
|-----------------|----------|
| `/root/.pgcli/pg.yaml` | `~/.pgcli/pg.yaml` |
| `/var/cache/pgcli/images` | `~/.cache/pgcli/images` |
| `/data/pg` | `~/pg/data` |
| `/mnt/backup/pg` | `~/pg/backup` |

`[rootless]` 之外的主机保持 rootful 布局。rootless 自启动使用 systemd user
unit；play 会启用 lingering，保证开机启动。用 `--limit rootless` 只部署这些主机。
rootless podman 使用独立的按用户镜像存储，不与该主机上的 rootful 存储共享。

非 root 的 Patroni 部署：新增 `[rootless_ha]` 分组，并在 `hosts.ini` 里通过
`[ha_cluster:children]` 把它设为 `[ha_cluster]` 的子组。子组继承
`group_vars/ha_cluster` 的 HA 设置，`group_vars/rootless_ha/main.yml` 只覆盖
namespace、集群和 HAProxy 列表，把它们替换为一个位于账号 home 下的集群。在同主机
上启用共址 etcd DCS（`[ha_etcd]` 条目设为 `state: present`），然后运行
`apply.yaml --limit rootless_ha`。etcd、Patroni 成员、S3 备份初始化与 HAProxy
全部以部署账号运行。若 pgbackup bucket 与其他集群共用，请为本场景的 pgBackRest
仓库指定独立的 `pgcli_backup_s3_path`。

## 按业务类型选择分组

每种业务场景对应一个 inventory 分组和一个 `group_vars` 文件。**一次只启用一种
场景分组**；未使用的分组保留声明但置空，保证 play 的主机模式仍能解析。

| 业务类型 | Inventory 分组 | `group_vars` 文件 | 运行 |
|----------|----------------|-------------------|------|
| 单机实例，无副本 | `[single]` | `group_vars/single/main.yml` | `apply.yaml --limit single` |
| 主库 + 同机副本 | `[samehost]` | `group_vars/samehost/main.yml` | `apply.yaml --limit samehost` |
| 主库 + 跨机副本 | `[crosshost]` | `group_vars/crosshost/main.yml` | `apply.yaml --limit crosshost` |
| 单机实例 + 插件 | `[instance_addon_mixed]` | `group_vars/instance_addon_mixed/main.yml` | `apply.yaml --limit instance_addon_mixed` |
| 机房监控 + 告警 | `[observable]`（或自定义分组，复制同名示例文件） | `group_vars/observable/main.yml` | `apply.yaml --limit observable` |
| Patroni HA 集群 | `[ha_etcd]` + `[ha_cluster]` | `group_vars/ha_etcd/main.yml` + `group_vars/ha_cluster/main.yml` | `apply.yaml --tags addon,ha` |
| 非 root Patroni HA 集群 | `[rootless_ha]`（`[ha_cluster]` 的子组）+ `[ha_etcd]` | `group_vars/rootless_ha/main.yml` + `group_vars/ha_etcd/main.yml` | `apply.yaml --limit rootless_ha` |
| S3 兼容备份存储 | `[s3]` 下的 `[minio_s3]` / `[silo_s3]` / `[rustfs_s3]` 之一 | 对应的 `group_vars/<type>_s3/main.yml` | `deploy-s3.yaml` |
| 单机插件（一种类型） | `[addon_<type>_single]` | `group_vars/addon_<type>_single/main.yml` | `apply.yaml --limit addon_<type>_single --tags addon` |
| 多主机插件集群 | `[addon_<type>_cluster]` | `group_vars/addon_<type>_cluster/main.yml` | `apply.yaml --limit addon_<type>_cluster --tags addon` |

四种场景分组（`single`、`samehost`、`crosshost`、`instance_addon_mixed`）是互斥的
部署模式：各组主机经常重叠，而每种启用的分组都会提供自己的 `pgcli_instances`
列表，同时启用可能混合实例列表。HA 与 S3 是带独立分组和 play 选择的支撑部署；
主机属于 HA 集群时，不要再把它列进已激活的场景分组。

正常部署时，把实例和插件一起声明在场景分组的文件里，跑
`apply.yaml --limit <scenario>` 即可。`addon_<type>_<mode>` 分组只用于单独测试
插件：它们的变量文件不会自动合并进场景分组，需要时把条目（连同 `params`）复制
到场景分组中。

插件分组可以同时声明全部类型，因为每组变量名互不冲突，由 `--limit` 选择一个。
运行插件分组时仍要保持场景分组和 HA 分组关闭——它们组级的
`pgcli_instances` / `pgcli_addons` 会覆盖插件 `--limit` 选中的列表。

## 全局变量：`group_vars/all/main.yml`

示例文件里每个字段都有注释。最重要的几块：

| 变量 | 含义 |
|------|------|
| `pgcli_install_version` | 安装的 release tag；留空表示最新版 |
| `pgcli_install_force` | 即使 `pg` 已存在也重跑安装脚本 |
| `pgcli_install_binary_url`、`pgcli_images_http_url`、`pgcli_download_mirror` | 离线/隔离环境的安装源（后者供 juicefs/zerofs/duckdb 二进制与 DocumentDB DEB 使用）；详见[离线与隔离环境安装](./offline/) |
| `pgcli_sqlite_install` | 在安装 play 中执行 `pg sqlite install`（sqlite3 与固定版本 Litestream；不是 add-on 类型） |
| `pgcli_install_path` | 二进制路径，默认 `/usr/local/bin/pg` |
| `pgcli_deploy_rootless`、`pgcli_deploy_user` | `[rootless]` 主机的非 root 部署（见 [Rootless 部署](#rootless-部署)）；`all` 只保留默认值 |
| `pgcli_namespace_path` | 主机默认 namespace 目录；留空用 `<部署账号 home>/.pgcli/pg.yaml`（rootful 为 `/root`，rootless 为账号 home） |
| `pgcli_namespaces` | namespace 名称 → `pg.yaml` 目录的映射 |
| `pgcli_config_defaults` | `pg config init` 的初始值：namespace、`base_dir`、`backup_dir`、端口段 |
| `pgcli_config_overrides` | 分组级 namespace 覆盖（各模式示例里设置） |
| `pgcli_instance_defaults` | 实例默认 `state`、`autostart`、`backup_target`、`password_length`、`extensions`、`clean_data` |
| `pgcli_instances` | 期望实例列表（通常放在各模式分组；插件分组也可声明，由 `--limit` 选中） |
| `pgcli_daemons` | 由 `pg daemon` 管理的主机长驻进程（webhook 接收端等）；不写 `host` 时在所有主机运行 |
| `pgcli_addons` | `--limit` 所选插件分组的列表（`addon_<kind>_addons`） |
| `_addon_limit`、`_addon_group`、`_addon_type`、`_addon_mode` | 由 `--limit addon_<type>_<single\|cluster>` 推导的插件选择器 |

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
