---
title: "SafeLine 雷池 WAF"
description: "以托管插件方式运行 SafeLine（雷池）社区版——七容器栈（内置 PostgreSQL 15、HTTPS 控制台、检测引擎、tengine 流量代理与支撑服务），专用静态 IP bridge 网络，仅 Linux rootful"
weight: 70
---

`pg addon install safeline` 以托管插件方式运行 **SafeLine（雷池）社区版**。它是**顶层插件**
（不代理某个 PostgreSQL 实例），每个实例是**七个容器组成的栈**：

- **postgres** —— 栈自带的 PostgreSQL 15.18，与 pgcli 实例的版本独立固定（上游尚未验证
  PostgreSQL 18，见 [chaitin/SafeLine#1250](https://github.com/chaitin/SafeLine/issues/1250)）
- **mgt** —— HTTPS 控制台，唯一对外发布的端口
- **detector** —— 攻击检测引擎
- **tengine** —— 流量反向代理，防护你在控制台配置的站点
- **luigi**、**fvm**、**chaos** —— 支撑服务

> **tengine 使用 host 网络。** 受防护站点的端口**直接绑定在宿主机上**（默认 80/443），
> 必须空闲。控制台本身是 HTTPS，使用自签证书。

**运行要求：** Linux、**rootful podman**、至少 2 GB 内存。栈把所有 bridge 容器固定在专用
`/24` 内的静态地址上，rootless podman 做不到；因此 macOS/Windows 与 rootless 主机上一律拒绝安装。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `safeline` | 实例名（map key、容器/网络前缀、子命令默认名） |
| `--version` | `9.4.7` | SafeLine 发行 tag：拉取 `docker.io/chaitin/safeline-{mgt,detector,tengine,luigi,fvm,chaos}:<version>` |
| `--postgres-version` | `15.18` | 内置 PostgreSQL 镜像 tag：拉取 `docker.io/chaitin/safeline-postgres:<version>` |
| `--subnet-prefix` | `172.22.222` | 栈专用 `/24` bridge 子网的前三段。每个栈必须使用独立子网；地址在网关之上固定分配：`.1` 网关、`.2` postgres、`.4` mgt、`.5` detector、`.7` luigi、`.8` fvm、`.10` chaos |
| `--port` | 自动（`9443`） | 控制台 HTTPS 宿主端口。`0` = 从 SafeLine 端口池自动分配（`safeline_start_port`，默认 `9443`） |
| `--listen` | `0.0.0.0` | 控制台绑定地址，与上游 compose 默认一致。设为 `127.0.0.1` 可让控制台不对外监听 |
| `--data-dir` | `<base_dir>/addon/safeline/<name>` | 栈数据根目录（内置数据库、控制台状态、nginx 配置、日志）。除 `base_dir` 相对路径外必须为绝对路径 |
| `--force` | false | 重建七个容器（保留数据目录），并轮换控制台管理员密码 |

`--image`、`--dsn`、`--pg-name`、`--password`、`--config-option`、`--config-file`、`--tls`、
`--tls-pem`、`--tls-cert`、`--tls-key`、`--advertise-host`、`--cluster`、`--peer`、`--join`
对 SafeLine 不适用，会被拒绝：栈自带 PostgreSQL，所有镜像由 `--version`/`--postgres-version` 决定。

## 安装

```bash
pg addon install safeline --name waf1
```

安装会按需拉取七个镜像、创建目录与专用 bridge 网络、生成内置数据库密码，按依赖顺序启动容器
（postgres → mgt 控制台 → detector → fvm → chaos → luigi → tengine），等待 PostgreSQL 与
控制台健康检查通过，从 `resetadmin` 引导输出中捕获控制台管理员密码，写入 `pg.yaml`，并打印摘要：

```text
✓ SafeLine WAF "waf1" installed
  Console:     https://127.0.0.1:9443 (self-signed certificate)
  Admin:       admin
  Password:    <generated> (also stored: `pg addon password safeline --name waf1`)
  Version:     SafeLine 9.4.7, PostgreSQL 15.18
  Subnet:      172.22.222.0/24 (gateway 172.22.222.1)
  Containers:  postgres, mgt, detector, luigi, fvm, chaos (bridge pgcli-safeline-waf1); tengine (host network)
  Data dir:    /home/you/.pgcli/addon/safeline/waf1
  Traffic:     tengine listens on host networking — add protected sites in the console;
               their entry ports default to 80/443 and must be free on this host
```

第二个实例需要独立子网与控制台端口（端口池自动递增）：

```bash
pg addon install safeline --name waf2 --subnet-prefix 172.22.223 --port 9444
```

不带任何变更参数重复安装是 no-op；若参数与已存值不同（版本、子网、端口、监听地址、数据目录），
不带 `--force` 会直接报错，而不是静默忽略。

## 控制台

打开 `https://<listen>:<port>`（默认 `https://<host>:9443`），用 `admin` 与生成的密码登录。
之后可用下面的命令取回密码：

```bash
pg addon password safeline --name waf1
```

密码来自 mgt 容器的 `resetadmin` 引导输出，而该命令**每次调用都会轮换管理员密码**。pgcli 只在
没有记录密码时运行它（安装时，或安装中途失败后第一次 `start` 时），以及 `--force` 重建时运行。
手动重置：

```bash
podman exec pgcli-safeline-waf1-mgt resetadmin
```

控制台使用自签证书提供 HTTPS，浏览器会给出常规警告。

## 防护站点与流量端口

受防护应用（站点）在**控制台里**配置，不通过 pgcli 参数。每个站点在宿主机上获得一个入口端口
（默认 80/443），由 host 网络的 **tengine** 容器监听。添加站点前先确认宿主机端口空闲
（`ss -tlnp`）。防火墙需放行控制台端口以及各站点的入口端口（默认 80、443）。

## 容器布局与网络

| 角色 | 网络 | 地址 | 用途 |
|---|---|---|---|
| `postgres` | bridge `pgcli-safeline-<name>` | `<subnet>.2` | 内置 PostgreSQL 15.18 |
| `mgt` | bridge | `<subnet>.4` | 控制台（容器端口 `1443`，发布到 `--port`） |
| `detector` | bridge | `<subnet>.5` | 检测引擎 |
| `luigi` | bridge | `<subnet>.7` | 控制台支撑服务 |
| `fvm` | bridge | `<subnet>.8` | 文件/版本服务 |
| `chaos` | bridge | `<subnet>.10` | 流量仿真服务 |
| `tengine` | host | — | 流量代理；站点端口在控制台配置 |

bridge 网络以 `--subnet <prefix>.0/24 --gateway <prefix>.1` 创建，并带有归属标签
`pgcli.safeline=<name>`；已存在但子网或归属不符的网络会直接报错。容器使用
`--restart unless-stopped`（保证 `pg addon stop` 语义）与 30 秒停止超时；bridge 容器 drop
`NET_RAW` 并设置 `no-new-privileges`，tengine 设置 `no-new-privileges` 并按上游提高打开文件数上限。

## 数据布局

所有内容都在 `--data-dir`（默认 `<base_dir>/addon/safeline/<name>/`）下：

```text
<data-dir>/
├── resources/
│   ├── postgres/data   # 内置 PostgreSQL 数据
│   ├── postgres/password  # 内置数据库口令（0600），重装时复用
│   ├── mgt/            # 控制台状态
│   ├── detector/ nginx/ chaos/ cache/ luigi/
│   └── sock/ run/      # 容器间共享的 unix socket
└── logs/
    ├── nginx/          # 控制台 + tengine 访问/错误日志
    └── detector/
```

容器以 root 运行，这些目录归 root 所有。请把整棵树当作不透明数据，需要保留控制台配置时整体备份。PostgreSQL 在首次初始化时把口令写死在数据目录里，因此 pgcli 会把它记录到 `resources/postgres/password`：`pg addon remove`（不带 `--clean-data`）之后再重新安装，就是靠它重新接上保留下来的数据。

## 生命周期、日志与开机自启

```bash
pg addon list                                   # security add-on 段：n/7 运行、控制台 URL
pg addon start safeline --name waf1             # 启动整栈（缺失容器会创建）
pg addon stop safeline --name waf1              # 反序停止
pg addon logs safeline --name waf1              # mgt 控制台日志（默认 service）
pg addon logs safeline --name waf1 --service tengine -f   # 流量代理日志
```

`--service` 可选 `postgres`、`mgt`、`detector`、`tengine`、`luigi`、`fvm`、`chaos`，默认
`mgt`。`start` 返回前会等待 PostgreSQL 与控制台健康检查就绪。

开机自启会按依赖顺序拉起整栈；若控制台密码从未被捕获，也会在这里补录：

```bash
pg autostart enable --safeline --name waf1
```

受防护站点及其流量端口仍由控制台配置，不受 autostart 影响。

## 卸载

```bash
pg addon remove safeline --name waf1             # 删容器与网络，保留数据
pg addon remove safeline --name waf1 --clean-data   # 同时删除 <data-dir>
```

专用 bridge 网络在无容器挂载后删除。不带 `--clean-data` 时保留数据目录（内置 PostgreSQL 数据与
控制台状态），重新安装可接续使用。

## 故障排查

- **报错“SafeLine requires rootful podman” / “SafeLine requires Linux”** —— 栈需要静态容器地址与
  host 网络端口绑定。请在 Linux 上以 root 运行 podman；rootless 与 macOS/Windows 会直接拒绝。
- **报错“network … already exists with subnet …”** —— 已有栈或其他残留网络占用了该子网或名称。
  换一个 `--subnet-prefix`/`--name` 安装，或先删除残留网络。
- **控制台健康检查不通过** —— 查看 `pg addon logs safeline --name <name> --service mgt` 与
  `--service postgres`。首次启动要跑 schema 迁移，可能需要一分钟。
- **站点端口被占用** —— tengine 直接在宿主机绑定受防护站点端口；先释放端口或改控制台里的入口端口。
- **密码丢失** —— 执行 `podman exec <prefix>-mgt resetadmin`，它会轮换密码并打印新密码。此时
  pgcli 里存的副本已过期——重新执行 `pg addon install safeline --name <name> --force`（重建七个
  容器、保留数据、重新捕获密码），或继续使用打印出来的密码。
- **镜像** —— SafeLine CE 镜像只发布 `linux/amd64`，请在 amd64 主机运行。

## 限制

- **仅 Linux + rootful podman**（静态 IP；host 网络流量代理）。
- pgcli 只发布控制台端口；受防护站点的流量端口在控制台管理。
- 控制台使用 SafeLine 自带自签证书，不参与 `--tls` 体系。
- 上游可选密钥（`MGT_INTERNAL_TOKEN`、`DETECTOR_AES_KEY`、`MGT_LICENSE_INTEGRITY_KEY`、
  `RIVER_*`）保持上游默认值，pgcli 不管理。
- SafeLine 的状态保存在 `--data-dir` 内的自带 PostgreSQL 中；pgcli 的 PITR/备份插件面向 pgcli
  实例，不会备份这个数据库。
