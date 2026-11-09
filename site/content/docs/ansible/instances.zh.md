---
title: "实例与副本"
description: "配置 single、samehost、crosshost 示例文件 —— pgcli_instances 的全部字段、namespace 与 state 语义"
weight: 30
---

PostgreSQL 实例在 `pgcli_instances` 中声明，通常放在某个部署模式分组的
`group_vars/<mode>/main.yml` 里。`apply.yaml --tags instance`（以 `serial: 1`
运行）会在每台主机上收敛 `host:` 与它匹配的条目。

## 三个模式示例

每个示例文件都很短，按要验证的模式复制对应的那个即可。

### `group_vars/single/main.yml.example`

单机主库：

```yaml
pgcli_config_overrides:
  namespace: dev

pgcli_instances:
  - name: pg_single
    host: db-01
    state: started
    autostart: true
```

### `group_vars/samehost/main.yml.example`

主库与同机只读副本：

```yaml
pgcli_instances:
  - name: pg01
    host: db-01
    state: started
    autostart: true

  - name: pg01_ro1
    host: db-01
    replica_of: pg01
    state: started
    autostart: true
```

同机副本会继承主库的 namespace；只有显式设置了 namespace 时才需要在副本上也写。

### `group_vars/crosshost/main.yml.example`

副本位于另一台主机，需要同时指定主库实例和它所在的主机：

```yaml
pgcli_instances:
  - name: pg01
    host: db-01
    namespace: dev
    state: started
    autostart: true

  - name: pg01_ro1
    host: db-02
    replica_of: pg01
    replica_of_host: db-01
    namespace: dev
    state: started
    autostart: true
```

主库所在主机必须排在 `[crosshost]` 前面：play 是串行的，副本需要主库先运行。
角色会先在主库侧准备复制槽与 `pg_hba`，再用 `--primary-dsn` 创建副本，主库密码
从该主机的 `pg.yaml` 读取。

## `pgcli_instances` 字段

| 字段 | 适用对象 | 含义 |
|------|----------|------|
| `name` | 全部 | 必填的实例名 |
| `host` | 全部 | 拥有该实例的 inventory 主机 |
| `namespace` | 全部 | `pgcli_namespaces` 中的名称；省略表示主机默认 |
| `state` | 全部 | `present`、`started`（默认）、`stopped`、`absent` |
| `autostart` | 全部 | 开机自启（`pg autostart enable`） |
| `backup_target` | 主库 | `local`（默认）或 `s3`；创建时固定 |
| `password_length` | 主库 | 生成的 admin 密码长度（默认 16） |
| `extensions` | 主库 | 扩展列表；设置后会整体替换默认列表 |
| `clean_data` | 全部 | 仅与 `state: absent` 搭配；同时删除数据/WAL 目录和 stanza |
| `replica_of` | 副本 | 主库实例名 |
| `replica_of_host` | 副本 | 跨机主库所在的 inventory 主机 |

副本条目只接受 `name`、`replica_of`、`replica_of_host`、`namespace`、`state`、
`autostart`、`clean_data`；密码、数据库、镜像、扩展和 PITR 设置都继承自主库。

默认值来自 `group_vars/all/main.yml` 的 `pgcli_instance_defaults`：

```yaml
pgcli_instance_defaults:
  state: started
  autostart: false
  backup_target: local
  password_length: 16
  extensions: []
  clean_data: false
```

## state 语义

- `present`：创建实例，但不改变其运行状态。
- `started`：必要时创建并启动。
- `stopped`：必要时创建并停止。
- `absent`：删除容器和配置项；数据/WAL 目录与 pgBackRest stanza 默认保留，只有
  `clean_data: true` 才会一并删除。因此**彻底销毁需要同时设置**
  `state: absent` 与 `clean_data: true`；单独设置 `clean_data` 不生效。

## namespace

`pgcli_namespaces` 把选择器名称映射到存放 `pg.yaml` 的目录：

```yaml
pgcli_namespaces:
  - name: dev
    path: /root/.pgcli
  - name: prod
    path: /root/.pgcli/prod
```

实例通过 `namespace: <name>` 选择；省略则使用主机默认
（`pgcli_namespace_path` / `pgcli_config_path`）。带独立 `base_dir` 的额外条目由
`pgcli` 角色自动初始化；没有 `base_dir` 的条目需要手动
`pg config init -o`。

## 运行

```bash
# 完整模式（必要时安装 pgcli）
uv run ansible-playbook playbooks/apply.yaml --limit samehost

# 仅在已准备好的主机上收敛实例
uv run ansible-playbook playbooks/apply.yaml --limit crosshost --tags instance
```

重复运行是幂等的；已处于目标状态的实例报告 `ok`。
