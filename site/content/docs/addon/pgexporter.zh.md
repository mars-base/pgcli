---
title: "Pg_exporter"
description: "以 pgcli 插件方式运行 pg_exporter——Pigsty 的声明式 PostgreSQL 指标导出器，可监控 pgcli 纳管实例或任意远程 PostgreSQL"
weight: 62
---

`pg addon install pgexporter` 以受管容器方式运行 [pg_exporter](https://pigsty.cc/docs/pg_exporter/)——Pigsty 的 PostgreSQL 指标导出器——直接跑官方 `docker.io/pgsty/pg_exporter` 镜像。[node_exporter](../nodeexporter/) 插件描述的是**机器**，而 pg_exporter 描述的是一个**数据库**：它连上某个 PostgreSQL 服务，在 `/metrics` 上发布约 460 个 `pg_*` 指标（活动、锁、复制、膨胀、WAL、按库统计……）。与 Prometheus、VictoriaMetrics、Grafana、node_exporter 一样，它是**顶层插件**：本身不依附任何实例——而是*指向*它监控的目标。

它与 node_exporter 并存的理由是**统一采集**：目标既可以是 pgcli 纳管的实例，**也可以是任意可达、pgcli 并不托管的 PostgreSQL 服务**。这正是它的全部意义——一种导出器形状，同时采集 pgcli 内外的数据库指标。

> **三种目标模式，互斥——只传其一。** `--pg-name <inst>` 监控一个 **pgcli 纳管**的实例；其 admin DSN 每次启动都从 `pg.yaml` 现推（故轮换过的密码或改动的端口永不会失效）。对 pgcli **不托管**的服务器，目标只用**一个** URL 给出——而你传哪个 URL 决定 pgcli 是否碰这个库：
> - `--admin-dsn postgres://…`——一个 **admin 权限** URL。pgcli 用它一次性在目标上建一个低权限监控角色，随后**丢弃它**：只保留其不含凭据的地址（host/port/database），导出器的 host/port/database 就取自它。此后导出器只以监控角色连库。
> - `--dsn postgres://…`——**原样拨号**。导出器就用这个 URL 自带的凭据连接，pgcli 在目标上**绝不执行任何** SQL。适合角色已存在、或根本不想让 pgcli 碰库的场景。
>
> `--dsn` 与 `--admin-dsn` 从不同时出现——不需要第二个 URL，因为目标的 host/port/database 都来自你传的那一个。

> **本地与 `--admin-dsn` 两种模式下，导出器从不用 admin 身份连库**：pgcli 创建一个**低权限监控角色**（`pg_monitor` 成员、`CONNECTION LIMIT 5`），并把*该*角色的 URL 交给容器。要建这个角色，pgcli 需要**一次** admin 权限：`--pg-name` 时 admin 凭据自动派生；`--admin-dsn` 时你给出一个 admin URL，它只用于那一批 `CREATE ROLE`/`GRANT` SQL、**绝不落进 `pg.yaml`**。生成的监控密码只打印一次，之后用 `pg addon password pgexporter` 取回。在原样拨号的 `--dsn` 模式下 pgcli 什么都不建——角色已存在是你（或某次先前的 `--admin-dsn` 安装）的事。

> **导出器端点没有鉴权、没有 TLS**，且 pg_exporter 在同一端口上还服务**管理端点**（`/reload`、`/explain`、`/stat`）——任何能连到端口的人既能抓指标、也能触发重载。因为抓取它的 Prometheus 通常在另一台主机，`Listen` 默认 `0.0.0.0`，唯一保护是网络边界。请把它限制在监控网段内，或对同机 Prometheus 传 `--listen 127.0.0.1`。

> **平台**：Linux 是已实测平台（宿主网络，故导出器直接绑宿主端口、并能拨本地实例的 `127.0.0.1` 目标）。在 macOS 上容器跑在 pgcli-net bridge 里，**够不到本地实例的 `127.0.0.1` DSN**——那里用远程模式（`--dsn` 或 `--admin-dsn`）指向一个可达地址（远程路径可用、本地路径不可用）。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `pgexporter` | 实例名（map 键） |
| `--pg-name` | — | 按名监控一个 **pgcli 纳管**实例（admin DSN 每次启动现推；与 `--dsn`、`--admin-dsn` 互斥） |
| `--admin-dsn` | — | 远程**建角色**模式：一个 **admin 权限** URL。pgcli 用它**一次性**建监控角色后即**丢弃**——只保留其不含凭据的地址（host/port/database），绝不含 admin 密码；此后导出器以监控角色连接。与 `--dsn`、`--pg-name` 互斥 |
| `--dsn` | — | 远程**原样拨号**模式：导出器就用这个 URL 自带的凭据连接，pgcli **绝不**在目标上跑 SQL——适合角色已存在、或不想让 pgcli 碰库。与 `--admin-dsn`、`--pg-name` 互斥 |
| `--monitor-user` | `pgexporter` | pgcli 在目标上创建的低权限角色名（`pg_monitor` 成员；仅限普通标识符）。多个 exporter 监控同一目标时，每个实例传**不同的值** |
| `--image` | `docker.io/pgsty/pg_exporter:1.5.0` | 覆盖镜像 |
| `--port` | 自动（基址 9630） | 宿主端口；自 `pgexporter_start_port` 自动分配 |
| `--listen` | `0.0.0.0` | 绑定地址——默认开放全部网卡（跨主机 Prometheus）；`127.0.0.1` 收紧为仅回环 |
| `--config-file` | — | **自定义** pg_exporter 采集器 YAML 的路径，只读挂到 `/etc/pg_exporter.yml`（不设则用二进制内建的默认采集器集） |
| `--extra-arg` | — | 传给二进制的额外 CLI flag（可重复）；最后追加，故 last-wins 覆盖早期 flags。如 `--extra-arg --auto-discovery=false` |
| `--force` | false | 即使正在运行也重建——用于应用改动的端口/监听/目标/额外参数 |

pg_exporter **无状态**——它不落盘，故没有数据目录。`pg addon remove` 只删容器（目标上建的监控角色**会保留**，因为它可能被共享或手工管理）；重装总是从头开始。

## 安装

监控一个 pgcli 纳管实例（监控角色自动创建）：

```bash
pg addon install pgexporter --pg-name demo
```

命令派生本地 admin DSN、在 `demo` 上建监控角色、按需拉取镜像、用*监控* URL 起容器，并打印抓取信息：

```
✓ pg_exporter installed: "pgexporter"
  Container:  pgcli-pgexporter-pgexporter
  Image:      docker.io/pgsty/pg_exporter:1.5.0
  Metrics:    http://0.0.0.0:9630/metrics
  Target:     instance demo
  Monitor as: pgexporter (pg_monitor)

  Monitor password: pgx_3f9a…            # 只打印一次
  (printed once — retrieve it later with `pg addon password pgexporter --name pgexporter`)
```

监控一个 pgcli **不托管**的库、并让 pgcli 建监控角色——**只**给一个 admin URL；目标的 host/port/database 就取自它：

```bash
pg addon install pgexporter --name ext \
  --admin-dsn "postgres://admin:adminpw@db.example.com:5432/app"
```

`--admin-dsn` 跑一次性的 `CREATE ROLE`/`GRANT pg_monitor` 后即被**丢弃**——`pg.yaml` 里只留下不含凭据的地址 `postgres://db.example.com:5432/app`。此后导出器只以 `pgexporter` 连，`pg addon password pgexporter --name ext` 取回该角色密码。

若监控角色已存在、你宁可 pgcli **完全不碰**目标，就改传一个带可用凭据的 `--dsn`（与 `--admin-dsn` 互斥——只给一个 URL，不是两个）：

```bash
pg addon install pgexporter --name ext \
  --dsn "postgres://pgexporter:existingpw@db.example.com:5432/app"
```

这里 pgcli 不跑任何 SQL：导出器就用这个 URL 及其自带凭据连接。

（要手工建角色，最小集是 `CREATE ROLE pgexporter LOGIN CONNECTION LIMIT 5 PASSWORD '…'; GRANT pg_monitor TO pgexporter;`——在 PostgreSQL 10–13 上再加 `GRANT SELECT (oid) ON pg_subscription`。）

验证导出器给的是目标的真实数字：

```bash
curl -s http://127.0.0.1:9630/metrics | grep -E '^pg_(up|version_info)'
curl -s http://127.0.0.1:9630/up        # 200 == 目标可达
```

`pg_up 1` 表示监控角色连上了。把 `pg_version_info` 与 `pg exec demo -c "SHOW server_version"` 对照校验——并确认角色已落到目标上：`pg exec demo -c "\du pgexporter"`（或查 `pg_roles` / `pg_auth_members` 里的 `pg_monitor` 成员关系）。默认带 `--auto-discovery` 时，指标会按目标上的每个库带一个 `datname` 标签。

### 重复安装与共享的监控用户

建监控角色的 SQL 是**幂等**的，所以重复 `install` 绝不会损坏目标上的角色：对一个在跑、配置未变的实例做普通重装是**空操作**（根本不执行 SQL）；`--force` 重装则对已存在的角色做 `ALTER`——其 OID 与授权都保留，`pg_monitor` 成员关系也绝不重复。

`--force` / 配置变化的重装**会**做一件事：轮换生成的密码（该实例自身没问题——新密码与注入是同步的——但它会覆盖你手工设过或复用过的密码；用 `pg addon password pgexporter` 取回当前的）。正因为每次这样的安装都会给角色写入一个新密码，**两个 exporter 实例共享同一目标、又都用默认的 `pgexporter` 用户就会冲突**：装第二个时轮换了这个共享角色的密码，第一个手里那份就此过期——它已建立的连接池还能用，但下一次重连会认证失败、掉到 `pg_up 0`。要对同一目标跑多个 exporter 时，给每个实例配一个自己的角色：

```bash
pg addon install pgexporter --name pe2 --pg-name demo --monitor-user pgexporter2
```

### 接入 Prometheus

导出器只有被抓取才有意义。pgcli 的 [Prometheus](../prometheus/) 插件靠一行 `--scrape` job 就能抓它：

```bash
pg addon install prometheus --name p1 --scrape pg=127.0.0.1:9630
# 远程 Prometheus → 用宿主的局域网地址而非 127.0.0.1：
pg addon install prometheus --name p1 --scrape pg=<host-ip>:9630
```

要把这个导出器加进**一个已经在跑、已经在抓别的 job** 的 pgcli Prometheus，用 `--scrape-append`——它把 `pg` job 折叠进已存的抓取列表（同名 job 合并、新名字追加）并 SIGHUP 热重载，绝不会清掉现有 target：

```bash
pg addon install prometheus --name p1 --scrape-append pg=127.0.0.1:9630
```

（普通的 `--scrape` 会**整体替换** job 集，所以用重跑 install 会丢掉 p1 原本在抓的其他 job。）之后在 Prometheus 里，`up{job="pg"}` 为 `1`、`count(pg_activity_count)` 为正数。`--scrape` / `--scrape-append` 的完整机制见 [Prometheus 插件](../prometheus/)。

### 复用健康探针

pg_exporter 的 `/primary` 与 `/replica` 只在目标处于该角色时返回 200，故它们可兼作 **HAProxy 式存活探针**用于读写路由——把 pgcli 的 HAProxy 接上某个 PostgreSQL 做指向探见的接线见 [HAProxy 插件](../haproxy/)。`/up`（目标是否可达）与 `/read` 是另外两个探针。

对运行中的实例重复 `install`：不带 `--force` 时是**空操作**（你传的字段会存下，但容器照旧服务）。`--force` 则重建容器——`--pg-name` 模式总会重跑幂等建角色（轮换密码），`--admin-dsn` 模式只有再传一个新鲜的 `--admin-dsn` 才重跑（否则沿用已存的监控密码，因为 pgcli 刻意没保留 admin 权限），`--dsn` 模式从不跑 SQL。改端口 / 监听 / 额外参数需要 `--force`（重建容器）——只有**采集器配置**可热重载（见下）。

## 自定义采集器（`--config-file`）与重载

pg_exporter 把一整套默认采集器**内建在二进制里**，并在运行时按服务版本/角色/扩展来规划，所以**要拿到有用的指标不需要任何配置文件**——大多数安装都不带 `--config-file`。当你确实要自定义采集器集时，把 `--config-file` 指向一份 pg_exporter 采集器 YAML；它被只读挂到 `/etc/pg_exporter.yml` 并经 `PG_EXPORTER_CONFIG` 选中。在宿主上编辑该文件后，用 SIGHUP 就地热重载（不重启容器）：

```bash
pg addon install pgexporter --pg-name demo --config-file /srv/collectors.yml
# …编辑 /srv/collectors.yml，然后：
pg addon pgexporter reload --name pgexporter
```

对**没带** `--config-file` 安装的实例做 reload 是无害的空操作——pgcli 会明说（内建默认集在磁盘上没有可重读的东西）。URL / 端口 / 监听改动**不**被 SIGHUP 覆盖；用 `install --force` 重建。

## 自定义参数（`--extra-arg`）

pg_exporter 在 env 配置的 URL 之外还有丰富的 flag 面。经 `--extra-arg`（可重复）开关特性：

```bash
pg addon install pgexporter --pg-name demo \
  --extra-arg --auto-discovery=false \
  --extra-arg --tag=env=prod
```

额外参数**最后**追加到命令行，故经 last-wins 覆盖 pgcli 注入的 flags。常见的有：`--auto-discovery=false`（不再按库加 `datname`）、`--exclude-database`（跳过某些库）、`--tag`（加常量标签）、`--namespace`（给每个指标加前缀）。`--force` 之后，用 `curl -s http://127.0.0.1:9630/metrics` 确认一个 `--auto-discovery=false` 的目标只报 `postgres` 库的标签。

## 监听与暴露

`Listen` 默认 `0.0.0.0`：在 Linux 上容器共享宿主网络命名空间，所以 `/metrics` 在宿主每个网卡的分配端口上可达。这是刻意的——抓取它的 Prometheus 通常在另一台机器。但**没有鉴权**，且 pg_exporter 在同一端口上还服务 `/reload`、`/explain`、`/stat`，故任何能连到端口的人不仅能抓指标、还能读配置和触发重载。对同机 Prometheus 传 `--listen 127.0.0.1`（用 `--force` 重建）；否则请在防火墙层面限制端口。（在 macOS bridge 构建下，pgcli 会自动把绑定放宽到 `0.0.0.0`，好让发布的端口经 podman VM 保持可达。）

## 端口

每个 pg_exporter 实例占有一个端口——指标端点——取自以 `pgexporter_start_port`（默认 `9630`，pg_exporter 惯例端口）为基址的端口池，每多一个实例递增，并跳过任何被受管容器或 `used_ports` 占用的端口。分配结果写入 `pg.yaml`，故重装绝不会把别的插件在用的端口再发一遍。`9630` 与 node_exporter（`9100`）、Prometheus（`9090`）、VictoriaMetrics（`8428`）、Grafana（`3000`）、Alertmanager（`9093`）都不重号；显式 `--port` 仍优先。

```bash
pg addon install pgexporter --name pe2 --monitor-user pgexporter2 --pg-name demo   # 第二个实例：下一个空闲端口（9631）
pg addon install pgexporter --name pe3 --port 9700 --monitor-user pgexporter3 --pg-name demo  # 显式端口仍优先
```

（同一目标上每多一个实例都需要自己的 `--monitor-user`——见「重复安装与共享的监控用户」。）

## 配置

```yaml
addons:
  pgexporter:
    pgexporter:
      name: pgexporter
      image_tag: docker.io/pgsty/pg_exporter:1.5.0
      host_port: 9630
      listen: 0.0.0.0
      target_instance: demo              # 本地模式
      monitor_user: pgexporter
      monitor_dsn: postgres://pgexporter:…@127.0.0.1:5432/demo?sslmode=disable   # 容器实际拨的
      monitor_password: "…"              # 一个 secret——生成的监控密码
      autostart: false
    ext:
      name: ext
      host_port: 9631
      listen: 0.0.0.0
      admin_dsn: postgres://db.example.com:5432/app   # --admin-dsn 模式：只存不含凭据的地址
      monitor_user: pgexporter
      monitor_dsn: postgres://pgexporter:…@db.example.com:5432/app
      monitor_password: "…"
```

三个目标字段互斥——只设其一：`target_instance`（pgcli 纳管实例，DSN 每次启动现推）、`admin_dsn`（pgcli 在其上建了监控角色的远程服务器——pgcli 只保留其不含凭据的地址，你传的 admin URL 绝不持久化）、或 `dsn`（原样拨号的远程服务器，连同自带凭据原样存，且从不跑建角色 SQL）。`monitor_dsn` / `monitor_password` 是 pgcli 生成并注入容器的（`dsn` 模式下没有，此时导出器直接拨 `dsn` 本身）。`extra_args` 是最后追加的 flag 列表；`config_file` 是可选的、只读挂载的宿主路径。

### 列出

```bash
pg addon list
```

```
Web add-ons (pgexporter):
  pgexporter (name: pgexporter)
    Status:      running
    Metrics:     http://0.0.0.0:9630/metrics
    Target:      instance demo
    Monitor as:  pgexporter (pg_monitor)
    Extra args:  --auto-discovery=false
    Image:       docker.io/pgsty/pg_exporter:1.5.0
    Container:   pgcli-pgexporter-pgexporter
```

远程模式下 Target 行会被遮盖（DSN 里的密码绝不出现）。监控密码不显示——用 `pg addon password pgexporter` 取回。

### 密码

```bash
pg addon password pgexporter          # 打印已存的监控角色密码
```

这是导出器连接**所用**的 `pgexporter` 角色密码——不是某个 PostgreSQL admin 密码。

## 启停

容器以 podman 的 `unless-stopped` 重启策略运行，宿主重启后随 podman 服务自动回来。`start`/`stop` 显式管理它们：

```bash
pg addon stop pgexporter
pg addon start pgexporter
```

`start` 会先重新派生监控 DSN（对 `--pg-name` 目标，故轮换过的实例密码或改动的端口在重建时生效）。

## 开机自启

```bash
pg autostart enable --pgexporter --name pgexporter
```

这会在 `pg.yaml` 里给实例打标记，并安装/刷新一个开机服务（Linux 上是 systemd `--user` unit，macOS 上是 LaunchAgent）来执行 `pg start --autostart`。开机启动只负责用已存的监控凭据把容器拉起来——它**不**重跑建角色的 SQL（那只在 install 时做）。完整机制、状态与关闭方式见 [开机自启]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove pgexporter   # 删容器；没有数据要清理
```

pg_exporter 不在磁盘上留存任何东西，故 `--clean-data` 除了给一条提示外没有任何效果。目标上建的**监控角色不会被删掉**——它可能被共享或手工管理。要自行移除，需先撤销它的按库授权（只要它在某个库拿到过 `pg_subscription` 的列授权，裸 `DROP ROLE` 就会失败）：在每个被监控的库里执行 `REVOKE SELECT (oid) ON pg_catalog.pg_subscription FROM "pgexporter";`，之后再 `DROP ROLE "pgexporter";`。

## 日志

```bash
pg logs addon pgexporter            # 最近的控制台输出
pg logs addon pgexporter -f         # 跟踪
pg logs addon pgexporter -n 200     # 最近 200 行（也支持 -i / --tail）
```

## 排障

- **`pg_up 0` / 没有 `pg_*` 指标** —— 监控角色连不上。确认 `/up` 返回 200、目标的 `pg_hba.conf` 放行容器的来源地址、已存密码与角色一致（`pg addon password pgexporter`；若已偏离，重装来轮换它）。对 `--dsn` 目标，确认该 DSN 的 host/port *从导出器所在主机*可达（一个 `127.0.0.1` 的 DSN 意味着目标在导出器本机）。
- **刚装完同目标的*第二个* exporter，第一个就掉成 `pg_up 0`** —— 两者共用默认的 `pgexporter` 角色，第二个安装轮换掉了它的密码、让第一个手里那份过期。给第一个做 `install … --force` 重建即可重新同步，或给每个实例配各自的 `--monitor-user`（见「重复安装与共享的监控用户」）。
- **指标有但很稀疏/偏空** —— 漏了 `GRANT pg_monitor` 会得到一个已连上但采集器不全的导出器。重装（建角色是幂等的）并确认成员关系：`pg exec <inst> -c "SELECT r.rolname, m.rolname FROM pg_auth_members a JOIN pg_roles r ON r.oid=a.member JOIN pg_roles m ON m.oid=a.roleid WHERE r.rolname='pgexporter'"`。
- **Prometheus 显示 target DOWN** —— 导出器在跑（`pg addon list`），但抓取地址/端口对不上（`--scrape pg=<host>:<port>`），或一个 `--listen 127.0.0.1` 的导出器被别的机器抓。
- **`--dsn and --admin-dsn are mutually exclusive`（`--dsn` 与 `--admin-dsn` 互斥）** —— 你传了两个远程 URL；只给一个：`--admin-dsn` 让 pgcli 建监控角色（其 host/port/database 即成为目标），或 `--dsn` 用你自己管理的凭据原样拨号。
- **`has an admin_dsn target but no stored monitor password`** —— 在一个 `--admin-dsn` 实例上做 `--force` 重装、而已存的监控密码丢失（pgcli 从未存 admin 凭据）。重新传 `--admin-dsn postgres://…` 让 pgcli 再建一次角色。
- **某个 flag / discovery 改动没生效** —— `--extra-arg` 与目标/URL 改动在容器创建时应用；用 `--force` reinstall。只有采集器配置（`--config-file`）经 SIGHUP 重载。
- **macOS 本地模式什么都不出** —— bridge 容器够不到宿主的 `127.0.0.1` PG；在 Mac 上用指向可达地址的 `--dsn`。
- **端口已被分配** —— 先 `pg addon remove` 释放，或用 `--port N` 另选一个。

## 已知限制

- **无鉴权、无 TLS，且管理端点暴露。** pg_exporter 提供纯文本 `/metrics` 外加 `/reload`、`/explain`、`/stat`；`0.0.0.0` 绑定的唯一保护是网络边界。若要离开可信网络，请前置 nginx 或用防火墙限制。
- **监控 DSN（含其密码）存在 `pg.yaml` 里。** 那是低权限 `pg_monitor` 角色的凭据，不是 admin 凭据；用于建它的 `--admin-dsn` 绝不持久化。请相应地保护 `pg.yaml`。
- **官方镜像基于 `scratch`** —— 它不带系统 CA 集，故对一个外部服务器的 TLS（`sslmode=require`）`--dsn` 可能证书校验失败；请经镜像供给信任、或在可信网络上连接。
- **无状态** —— 没有数据目录、没有保留期；除运行中的进程外什么都不留存。重装总是从头开始，且目标侧的监控角色绝不自动删除。
- **macOS 本地实例模式不可用**（bridge 容器够不到宿主回环）；远程 `--dsn` 模式在 Mac 上可用。Linux 是已实测平台。
- pgcli 不校验 `--extra-arg` 值；有错的 flag 只会在启动时从容器日志里暴露。
