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
（postgres → mgt 控制台 → detector → fvm → chaos → luigi → tengine），等待 PostgreSQL、控制台
健康检查与 tengine 控制 socket 就绪，从 `resetadmin` 引导输出中捕获控制台管理员密码，写入
`pg.yaml`，并打印摘要：

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

安装还会生成内部 detector 管理令牌（`detector_mgmt_token`，写入 `pg.yaml`）并传给 detector、
fvm、luigi 三个容器。detector 镜像默认对其管理 API 开启鉴权，控制台通过该 API 发布站点规则——
没有共享令牌时建站会失败。旧版 pgcli 安装的栈会在下一次 `pg addon start` 时自动迁移：生成令牌并
重建这些容器。

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
需要主动重置时：

```bash
pg addon password safeline --name waf1 --reset
```

该命令重新执行引导，把新的 `admin_password` 写回 `pg.yaml` 并打印出来，之后
`pg addon password safeline --name waf1` 读到的就是新密码。执行时栈必须处于运行状态。

控制台使用自签证书提供 HTTPS，浏览器会给出常规警告。

## 防护站点与流量端口

受防护应用（站点）在**控制台里**配置，也可以用 `pg addon safeline site`（见下）脚本化
管理。每个站点在宿主机上获得一个入口端口
（默认 80/443），由 host 网络的 **tengine** 容器监听。添加站点前先确认宿主机端口空闲
（`ss -tlnp`）。防火墙需放行控制台端口以及各站点的入口端口（默认 80、443）。

站点的上游可以填 IP，也可以填主机名（例如负载均衡域名）。tengine 在**加载配置时**从宿主机 DNS
解析主机名，因此负载均衡的 DNS 记录变化后，执行 `pg addon reload safeline --name waf1`
重新校验并平滑重载 tengine 配置——新地址即刻生效，无需重启整栈，也不会中断在途请求。

Ansible 部署可以在 `type: safeline` 条目的 `sites` 字段里声明站点（每次运行自动创建
缺失站点、更新变更的上游，证书按控制机文件路径上传），见
[Ansible 插件章节](../ansible/addons.md#safeline-站点)。

### 通过 CLI 管理站点

`pg addon safeline site ...` 用存储的管理员密码调用控制台自己的 Web API——与浏览器相同的加密
登录和 `/api/open/site` 接口——因此无需打开 UI 即可脚本化建站。栈必须处于运行状态。

> **部署在特定账号下？** 这些命令读取执行者账号的 `~/.pgcli/pg.yaml`，用其他账号
> （例如 `pgcli`）安装的栈在这里找不到。用 `-c` 指定那个账号的配置，例如
> `pg -c /home/pgcli/.pgcli/pg.yaml addon safeline site cc show --name safeline1 --site-id 4`；
> `site mode`、`site cc`、`site protect` 与 `site bot` 用法相同。

> **社区版限制：一个站点只能有一个上游。** pgcli 安装的控制台没有 Pro 授权，站点只能代理
> 单个上游。添加第二个上游（也就是多服务器负载均衡）会被控制台以 `license required` 拒绝；
> 在控制台激活 Pro 授权后可用。

```bash
pg addon safeline site list --name waf1
pg addon safeline site create --name waf1 \
    --port 443_ssl --domain www.example.com \
    --upstream http://10.0.0.5:8080 \
    --comment "web tier" \
    --cert fullchain.pem --key privkey.pem \
    --health-check --health-check-interval 30 --health-check-path /healthz
pg addon safeline site update --name waf1 --site-id 3 --health-check=false
pg addon safeline site health --name waf1
pg addon safeline site mode --name waf1 --site-id 3 dryrun
pg addon safeline site cc show --name waf1 --site-id 3
pg addon safeline site cc waiting-room --name waf1 --site-id 3 --enabled
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --type req --count 200
pg addon safeline site protect show --name waf1 --site-id 3
pg addon safeline site protect mode balance --name waf1
pg addon safeline site bot show --name waf1 --site-id 3
pg addon safeline site bot enable --name waf1 --site-id 3
pg addon safeline site bot condition --name waf1 --site-id 3 --where 'uri:prefix=/admin'
pg addon safeline site delete --name waf1 --site-id 3

pg addon safeline cert list --name waf1
pg addon safeline cert create --name waf1 --cert fullchain.pem --key privkey.pem
pg addon safeline cert update --name waf1 --cert-id 2 --cert fullchain.pem --key privkey.pem
pg addon safeline cert delete --name waf1 --cert-id 2
```

- `--port` 是宿主机入口端口；端口加 `_ssl` 后缀表示在该端口终结 TLS。`--cert`/`--key`
  上传 PEM 证书（叶子证书或完整链）并一步绑定，`--cert-id` 绑定控制台里已有证书，都不传则
  使用控制台的默认证书。`--domain` 默认 `*`（匹配任意 Host 头）。
- `--upstream` 填 `http://` 或 `https://` 绝对 URL；`--balance` 可选 `roundrobin`（默认）、
  `leastconn`、`iphash`、`backup`。`site update` 用 `--upstream` 整体替换上游列表，
  或用 `--add-upstream` / `--remove-upstream` 增量增删。pgcli 安装的控制台运行的是
  **社区版**，每个站点只允许 **一个上游**——第二个上游会被控制台以 `license required`
  拒绝，多上游负载均衡需要 Pro 授权。
- `--health-check` 按指定路径每 10–3600 秒对每个上游做 HTTP 健康检查。`site update`
  只改你传入的配置（上游、健康检查与/或绑定证书），其他站点配置保持控制台里的值。
- `site health` 探测选定站点（默认全部）的每个上游，输出 `healthy` 或控制台返回的错误；
  只要有上游不健康就以非零码退出，可直接用作监控/脚本门禁。`site list` 展示 `update`、
  `delete`、`health`、`mode` 通过 `--site-id` 使用的站点 id。
- `site mode` 在三种运行模式间切换一个或多个站点，见下方的「站点运行模式」。
- `site cc` 查看与修改 CC 防护的两项配置（等候室与频率限制），见下方的「CC 防护」。
- `site protect` 查看与修改攻击防护背后的语义分析模块，见下方的「语义分析（攻击防护）」。
- `site bot` 查看与修改站点的 BOT 防护（人机验证），见下方的「BOT 防护」；社区版只运行
  无感知验证，匹配条件不包含仅有 Pro 的 `geo_*`/`asn_*` 操作符。
- `cert list` 展示已存证书的覆盖域名、到期时间和使用它的站点。证书被站点的 TLS 入口端口
  占用时控制台拒绝删除，先删除站点或换绑证书。

HTTP 明文站点不需要证书——只要入口端口和上游：

```bash
pg addon safeline site create --name waf1 \
    --port 80 --domain www.example.com \
    --upstream http://10.0.0.5:8080
```

tengine 直接在宿主机绑定 80 端口，因此该端口必须空闲（用 `ss -tlnp` 确认）。

给已创建的站点换上游——整体替换成单个上游任何版本都可，增删上游与多上游列表需要 Pro 授权：

```bash
# 增加一台服务器，保留现有上游（Pro）
pg addon safeline site update --name waf1 --site-id 3 --add-upstream http://10.0.0.7:8080

# 摘除一台服务器（Pro，且站点已有多个上游时才需要）
pg addon safeline site update --name waf1 --site-id 3 --remove-upstream http://10.0.0.5:8080

# 整体替换上游列表：一个 URL 任何版本都可，多个需要 Pro
pg addon safeline site update --name waf1 --site-id 3 \
    --upstream http://10.0.0.6:8080 --upstream http://10.0.0.7:8080
```

给 TLS 站点绑定证书——可以先用 `cert create` 上传、再按 id 绑定，也可以一步上传并绑定：

```bash
# 先上传，之后按 id 复用到多个站点（id 由命令打印，也可用 cert list 查看）
pg addon safeline cert create --name waf1 --cert fullchain.pem --key privkey.pem
pg addon safeline site create --name waf1 --port 443_ssl --domain www.example.com \
    --upstream http://10.0.0.5:8080 --cert-id 2

# 一步上传并绑定（建站失败会自动回滚已上传的证书）
pg addon safeline site create --name waf1 --port 443_ssl --domain www.example.com \
    --upstream http://10.0.0.5:8080 --cert fullchain.pem --key privkey.pem
```

证书续期分两种情况：

```bash
# 同一张证书续期（临近过期）：在原有 id 上原地替换 PEM——所有绑定该证书的站点
# 自动换用新证书，无需逐个改动，也无需 `pg addon reload`
pg addon safeline cert update --name waf1 --cert-id 2 --cert new-fullchain.pem --key new-privkey.pem

# 站点改用另一张证书：先存好新证书（也可复用 cert list 里的 id），再按 id 换绑
pg addon safeline cert create --name waf1 --cert other-fullchain.pem --key other-privkey.pem  # -> id 5
pg addon safeline site update --name waf1 --site-id 3 --cert-id 5
```

自定义错误页以及带权重/备份角色的上游仍在控制台 UI 中配置。

### 站点运行模式

每个站点运行在三种模式之一，与控制台的「防护模式 / 观察模式 / 维护模式」一一对应。
`pg addon safeline site list` 的 `MODE` 列显示当前模式，`pg addon safeline site mode`
负责切换：

| 模式 | 控制台名称 | 行为 |
|---|---|---|
| `defense` | 防护模式 | 默认模式；发现攻击后自动拦截并记录攻击事件 |
| `dryrun` | 观察模式 | 只记录命中的攻击事件，不拦截任何请求 |
| `offline` | 维护模式 | 返回维护页面，任何人都无法访问站点 |

模式名是 `site mode` 唯一的位置参数，站点 id 用 `--site-id` 指定（可重复或逗号分隔），
一次调用可以切换多个站点。变更立即下发到 tengine，与控制台里切换模式完全等价。

```bash
# 查看当前模式（MODE 列）
pg addon safeline site list --name waf1

# 单个站点切到观察模式
pg addon safeline site mode --name waf1 --site-id 3 dryrun

# 多个站点一次切回防护模式
pg addon safeline site mode --name waf1 --site-id 3 --site-id 4 defense
```

### CC 防护（等候室与频率限制）

`pg addon safeline site cc` 查看与修改控制台里站点 CC 防护（CC 防护）的两项配置。
配置在站点处于防护模式（defense）时生效。

```bash
# 两项配置一览：等候室、规则作用域与生效规则
pg addon safeline site cc show --name waf1 --site-id 3
```

输出展示等候室、站点频率限制是自有规则还是跟随全局，以及生效规则：

```text
Site 3 (www.example.com) CC protection
  Waiting room:  enabled (max concurrent 5, session timeout 3m)
  Rate limits:   global (3 rules)
TYPE    ENABLED  PERIOD  COUNT  ACTION        BLOCK  CONDITIONS
req     yes      10s     100    challenge_v1  60m    -
attack  no       60s     10     block         30m    -
error   no       10s     10     block         30m    status_code eq 403,404
```

等候室限制同时访问的并发用户数——查看、启用、禁用：

```bash
pg addon safeline site cc waiting-room --name waf1 --site-id 3
pg addon safeline site cc waiting-room --name waf1 --site-id 3 --enabled
pg addon safeline site cc waiting-room --name waf1 --site-id 3 --enabled=false
```

它的两个阈值（`--max-concurrent` 1–5000、`--session-timeout` 1–30 分钟）属于
Pro 功能：社区版控制台只保存 `--enabled`，阈值保持默认（5 并发、3 分钟）。命令写入后
会重新读取等候室，把控制台没有应用的阈值作为错误报出，不会把被静默忽略的修改当成成功。

> **不适合用于生产环境。** 等候室是面向浏览器的工具：排队页依赖 JavaScript 与 Cookie，
> API、CLI 等不跟随排队的客户端会一直停在排队状态；中途放弃的会话会占住槽位直到会话
> 超时；更糟的是，攻击者只需用 curl 这类脚本并发发起少量请求就能占满槽位（并保持到
> 超时），正常浏览器访客反而全部被挡在排队页外——这是一条可低成本利用的拒绝服务途径。
> 社区版固定的 5 并发阈值对真实流量也不够用。生产防护请改用下方的频率限制规则，
> 非浏览器客户端调用的接口优先用 `--action block`。

每个站点有三条频率限制规则——`req`（窗口内请求数）、`attack`（窗口内攻击命中数）与
`error`（匹配规则条件的响应数）——默认跟随全局规则集：

```bash
# 查看生效规则，可用 --type 只看一条
pg addon safeline site cc rate-limit --name waf1 --site-id 3
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --type req

# 修改规则（窗口 10-600 秒，次数 >= 1，封禁 1-1440 分钟，
# action 取 block / challenge_v1 / challenge_v2）
pg addon safeline site cc rate-limit --name waf1 --site-id 3 \
    --type req --enabled --period 10 --count 200 --action block --block-min 30

# 修改所有跟随站点共用的全局规则（控制台中的 0 号站点）
pg addon safeline site cc rate-limit --name waf1 --global --type attack --count 20

# 让单个站点使用自己的副本（从全局规则播种，所有规则默认禁用），
# 再切回全局
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --use-global=false
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --use-global
```

跟随全局规则的站点不能单独修改规则：命令会说明这一点，并提示用 `--global`
修改共用规则、或用 `--use-global=false` 先建立站点自己的副本。副本里所有规则初始为
禁用；把 `--use-global=false` 与 `--type` 和规则参数组合，可在同一次调用里启用并配置
其中一条。

一次调用完成的典型加固配置——让站点使用自己的规则（从全局规则播种），10 秒内允许
200 次请求，超限封禁 30 分钟：

```bash
pg addon safeline site cc rate-limit --name waf1 --site-id 3 --use-global=false \
    --type req --enabled --period 10 --count 200 --action block --block-min 30
```

### BOT 防护（人机验证）

`pg addon safeline site bot` 查看与修改控制台里与 CC 防护并列的 BOT 防护。它用
**无感知验证**（invisible verification）挑战选中的访客：请求会收到一段 JavaScript
挑战，浏览器无交互即可通过，无法执行 JavaScript 的客户端则失败。每个站点有自己的
开关与匹配条件；站点处于防护模式（defense）时生效。

```bash
# 查看是否开启、验证等级、有效期与匹配条件
pg addon safeline site bot show --name waf1 --site-id 3

# 开启 / 关闭
pg addon safeline site bot enable --name waf1 --site-id 3
pg addon safeline site bot disable --name waf1 --site-id 3

# 只挑战匹配条件的访客，或恢复为挑战全部访客
pg addon safeline site bot condition --name waf1 --site-id 3 \
  --where 'uri:prefix=/admin && method:eq=POST'
pg addon safeline site bot condition --name waf1 --site-id 3 --all-visitors
```

`show` 输出开关、验证等级、通过验证后的有效期以及挑战哪些访客：

```text
Site 3 (www.example.com) BOT protection (人机验证)
  Status:    enabled
  Level:     invisible verification
  Validity:  1h
  Match:     only visitors matching the conditions below
GROUP  FIELD   OP      VALUE
1      uri     prefix  /admin
1      method  eq      POST
```

从未配置过 BOT 防护的站点显示控制台默认值（关闭、无感知验证、1 小时、无条件）。
`enable` 保留已存的有效期与匹配条件；没有任何条件的站点会挑战所有访客。`condition`
对应控制台的「仅针对特定条件启用人机验证」选项中的「满足条件时认证」模式（另一种
「满足条件时跳过认证」未实现）。**每次调用 `bot condition` 都会整体覆盖已有的
匹配条件**：命令传入的规则就是最终保存的全部条件——旧规则不会保留或合并，仍需要的
规则请一并传入；`--all-visitors` 清空全部条件，恢复为对所有访问都进行人机验证。

每条 `--where` 是一个条件组，组之间 OR；组内用 `&&`
连接必须同时满足的条件（AND），`|` 分隔单个条件的多个值；一个条件写作
`field:operator=value`：

```bash
pg addon safeline site bot condition --name waf1 --site-id 3 \
  --where 'uri:prefix=/admin && method:eq=POST' \
  --where 'src_ip:cidr=10.0.0.0/8|192.168.0.0/16'
```

一个典型的配置——挑战脚本客户端、无 User-Agent 的请求与管理路径，其余访客直接放行：

```bash
pg addon safeline site bot enable --name waf1 --site-id 3 \
  --where 'req_header/User-Agent:has=curl' \
  --where 'req_header/User-Agent:has=python-requests' \
  --where 'req_header/User-Agent:has=wget' \
  --where 'req_header/User-Agent:not_exist' \
  --where 'uri:prefix=/admin'
```

命中的客户端会收到无感知验证页（HTTP 状态码 468）：浏览器执行其中的 JavaScript
后无交互通过，并在有效期内保持验证状态；无法执行 JavaScript 的脚本客户端则持续
停留在验证页。

`field:operator=value` 中的匹配目标（field）与各自可用的操作符：

| 匹配目标 | 书写 | 操作符 | 说明 |
|---|---|---|---|
| 源 IP | `src_ip` | `eq` `not_eq` `match` `cidr` `not_cidr` `in` `not_in` `geo_eq` `geo_not_eq` `asn_eq` `asn_not_eq` | 如 `src_ip:cidr=10.0.0.0/8`；`geo_*`/`asn_*` 需 Pro，`in`/`not_in` 选 IP 组——CLI 均拒绝，见下 |
| URI | `uri` | `eq` `not_eq` `has` `not_has` `prefix` `re` | 含查询参数；如 `uri:prefix=/admin` |
| 不含查询参数的 URI | `uri_no_query` | `eq` `not_eq` `has` `not_has` `prefix` `re` | 如 `uri_no_query:eq=/api/users` |
| Host | `host` | `eq` `not_eq` `match` `re` `has` `not_has` | 如 `host:eq=www.example.com` |
| 请求头 | `req_header/<header 名>` | `eq` `not_eq` `has` `not_has` `re` `exist` `not_exist` | 必须带 header 名；如 `req_header/User-Agent:has=curl` |
| 原始请求头 | `req_raw_header` | `re` `has` `not_has` | 如 `req_raw_header:has=python-requests` |
| 原始响应头 | `res_raw_header` | `re` `has` `not_has` | 如 `res_raw_header:has=Set-Cookie` |
| 请求体 | `req_body` | `re` `has` `not_has` | 如 `req_body:has=<script` |
| 响应体 | `res_body` | `re` `has` `not_has` | 如 `res_body:has=stack trace` |
| GET 参数 | `get_param/<参数名>` | `eq` `not_eq` `has` `not_has` `re` `exist` `not_exist` | 必须带参数名；如 `get_param/q:exist` |
| POST 参数 | `post_param/<参数名>` | `eq` `not_eq` `has` `not_has` `re` `exist` `not_exist` | 必须带参数名；如 `post_param/username:eq=admin` |
| 请求方法 | `method` | `eq` `not_eq` | 如 `method:eq=POST` |
| 站点 | `website` | `eq` `not_eq` | 值为站点 id；如 `website:eq=3` |
| JA4 指纹 | `ja4` | `ja4_in` `eq` `not_eq` | `ja4_in` 选已记录指纹；如 `ja4:eq=t13d1516h2_8daaf6152771_02713d6af862` |

Header 与参数类字段按名字定位（如 `req_header/User-Agent:has=curl`）；`exist`/`not_exist`
不带值（可省略 `=`）。控制台的部分选项在此不可用：`src_ip` 上的 `geo_*`、`asn_*` 需要
Pro 许可，`src_ip` 的 `in`/`not_in` 与 `ja4_in` 选取的是控制台管理的对象（IP 组、
已记录的 JA4 指纹）——这些请在控制台配置。

控制台只在防护开启时可修改条件，因此 `condition` 对关闭的站点直接报错，并提示用
`enable --where ...` 一次完成开启与条件设置。滑动验证（slide）、修改有效期与请求防重放
（replay）属于 Pro 功能：社区版固定为无感知验证 + 1 小时有效期，命令也只写入这组值。
每次修改后命令会重新读取站点，控制台没有应用的变化会作为错误报出。关闭会保留已存配置，
再次开启即恢复。

### 语义分析（攻击防护）

`pg addon safeline site protect` 查看与修改控制台「攻击防护」背后的语义分析
检测模块。14 个检测模块——SQL 注入、XSS、文件上传、命令注入、Java/PHP 反序列化、
SSTI、SSRF 等——各自运行在四档之一：

| 档位 | 控制台名称 | 行为 |
|---|---|---|
| `disable` | 禁用 | 不检测 |
| `observe` | 仅观察 | 只记录风险请求，不拦截（接口值为 `dry_run`） |
| `balance` | 平衡防护 | 拦截明显攻击、误报低（控制台默认档） |
| `strict` | 高强度防护 | 拦截更多请求，误报率相对更高 |

站点默认跟随全局档位；与 CC 频率限制相同，站点也可以拥有自己的副本。

```bash
# 全局档位（所有没有自有副本的站点都跟随它）
pg addon safeline site protect show --name waf1

# 某个站点的生效档位，并注明是全局还是站点自有
pg addon safeline site protect show --name waf1 --site-id 3

# 一次把 14 个模块全部设为某一档（全局）
pg addon safeline site protect mode balance --name waf1

# 给某个站点建立自有副本并设为高强度防护
pg addon safeline site protect mode strict --name waf1 --site-id 3

# 删除副本，重新跟随全局
pg addon safeline site protect use-global --name waf1 --site-id 3
```

```text
Global attack protection (semantic analysis): all 14 modules at balance
MODULE                LEVEL
m_sqli                balance
m_xss                 balance
m_file_upload         balance
...
```

批量配置不会触碰控制台页面未展示的三个模块：扫描器检测（`m_scanner`）、响应检测
（`m_response`）与加强规则模块（`m_rule`）。单模块档位仍在控制台 UI 中配置。
变更立即下发到检测引擎，在站点处于防护模式（defense）时生效。

### 威胁情报共享（威胁情报共享计划）

控制台「通用设置 → 威胁情报共享计划」下有三个开关，把本机检测到的数据上报给
长亭的威胁情报服务器：

| 开关 | 控制台名称 | 共享内容 |
|---|---|---|
| `malicious-ips` | IP 情报共享计划（共享恶意 IP 信息） | 本机检测到的恶意 IP |
| `threat-intel` | 共享威胁情报 | 公网攻击 IP 与关键攻击日志（脱敏加密后上报） |
| `fingerprint` | 浏览器指纹信息共享计划（共享浏览器指纹信息） | 浏览器指纹 |

`pg addon safeline telemetry` 查看与切换这三个开关：

```bash
# 查看三个开关
pg addon safeline telemetry show --name waf1

# 全部关闭——私有化部署的常见选择
pg addon safeline telemetry disable --name waf1

# 全部重新开启
pg addon safeline telemetry enable --name waf1
```

```text
Threat-intelligence sharing on "waf1":
SWITCH         STATE     CONSOLE SETTING
malicious-ips  disabled  IP 情报共享计划（共享恶意 IP 信息）
threat-intel   disabled  共享威胁情报
fingerprint    disabled  浏览器指纹信息共享计划
```

这些开关只控制对外共享与恶意 IP 情报组：全部关闭时内置检测模块照常工作。命令会与
控制台对账，只写入状态不同的开关。

### 源 IP 获取方式（SourceIP）

控制台「通用设置 → 源 IP 获取方式」决定 tengine 把请求归属到哪个地址——日志、
攻击事件记录、按 IP 匹配的 ACL/频率限制/封禁都基于这个地址。
`pg addon safeline source-ip` 读写同一份代理配置：

| 模式 | 存储值 | 来源地址 |
|---|---|---|
| `socket` | `socket_ip`（默认） | TCP 连接的对端地址 |
| `xff-first` | `xff_first` | X-Forwarded-For 最右侧一项（上一级代理） |
| `xff-second` | `xff_second` | 倒数第 2 项 |
| `xff-third` | `xff_third` | 倒数第 3 项 |
| `xff-dynamic` | `xff_dynamic` | 由 `--xff-position from-end\|from-start` 与 `--xff-index N`（1–100）指定的位置 |
| `header` | `http_header` | `--header` 指定 HTTP 头中的地址 |
| `proxy-protocol` | `proxy_protocol` | PROXY Protocol 前导中的客户端地址，仅接受 `--trust` 列出的来源 |

```bash
# 查看当前设置
pg addon safeline source-ip show --name waf1

# 前置一层可信反向代理，由它追加 X-Forwarded-For
pg addon safeline source-ip set --name waf1 --mode xff-first

# 改用自定义 HTTP 头
pg addon safeline source-ip set --name waf1 --mode header --header X-Real-IP

# 只信任负载均衡器的 PROXY Protocol
pg addon safeline source-ip set --name waf1 --mode proxy-protocol \
    --trust 10.0.0.0/8,192.168.0.0/16

# 从前往后数第 2 项
pg addon safeline source-ip set --name waf1 --mode xff-dynamic \
    --xff-position from-start --xff-index 2
```

不带 `--site-id` 时命令操作全局设置，所有未单独配置的站点默认继承全局值。
带 `--site-id` 时针对单个站点自己的记录，`use-global --site-id <id>` 让站点重新
继承全局值。社区版（CE）对按站点的代理设置返回 `license required`，只能改全局设置。

命令只写入 `ip_source`/`ip_value` 两项，其余代理字段保持原样；写入前与生效值
对账（跟随全局的站点对比全局设置），无差异时跳过写请求，写入后回读控制台校验。

警告：信任 HTTP 头或 PROXY Protocol 后，能直连监听端口的客户端可以伪造源地址，
按 IP 匹配的 ACL、频率限制和封禁会作用在伪造值上。仅在监听端口只接收可控代理
转发的流量时启用。

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

## 备份与恢复

逻辑备份直接连栈的内置数据库（口令文件仅 root 可读）。示例（实例 `safeline1`，默认数据目录）：

```bash
pw=$(cat /data/pg/addon/safeline/safeline1/resources/postgres/password)

# 备份 / 恢复（.sql 纯 SQL、.dump custom，加 .gz 压缩；--clean 先删已有对象）
pg export --dsn "postgres://safeline-ce:$pw@<subnet-prefix>.2:5432/safeline-ce?sslmode=disable" -o safeline.sql
pg import --dsn "postgres://safeline-ce:$pw@<subnet-prefix>.2:5432/safeline-ce?sslmode=disable" -i safeline.sql
```

`<subnet-prefix>` 换成该实例的 `--subnet-prefix`（默认 `172.22.222`），最后一段 `.2`
是内置数据库在栈内 /24 中的固定地址。

整目录冷备：停栈后拷贝整个 `<data-dir>`（含控制台状态），恢复时放回原位再
`pg addon start`。

`pg export` / `pg import` 的完整参数（压缩级别、stdin/stdout 管道等）见
[数据导入导出](/docs/import-export/)。

## 生命周期、日志与开机自启

```bash
pg addon list                                   # security add-on 段：n/7 运行、控制台 URL
pg addon start safeline --name waf1             # 启动整栈（缺失容器会创建）
pg addon stop safeline --name waf1              # 反序停止
pg addon logs safeline --name waf1              # mgt 控制台日志（默认 service）
pg addon logs safeline --name waf1 --service tengine -f   # 流量代理日志
pg addon reload safeline --name waf1            # nginx -t + 平滑重载 tengine（重新解析站点上游 DNS）
```

`--service` 可选 `postgres`、`mgt`、`detector`、`tengine`、`luigi`、`fvm`、`chaos`，默认
`mgt`。`start` 返回前会等待 PostgreSQL 与控制台健康检查就绪，tengine 重启时还会等待其控制 socket。

开机自启会按依赖顺序拉起整栈；若控制台密码从未被捕获，也会在这里补录：

```bash
pg autostart enable --safeline --name waf1
```

受防护站点及其流量端口由控制台或 `pg addon safeline site` 配置，不受 autostart 影响。

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
- **密码丢失** —— 执行 `pg addon password safeline --name <name> --reset`，它会轮换控制台密码、
  更新存储的 `admin_password` 并打印新密码（要求栈处于运行状态）。
- **镜像** —— SafeLine CE 镜像只发布 `linux/amd64`，请在 amd64 主机运行。

## 限制

- **仅 Linux + rootful podman**（静态 IP；host 网络流量代理）。
- **社区版每站点单上游。** 内置控制台没有 Pro 授权，站点只能代理一个上游：`--add-upstream`、
  多 URL 的 `--upstream`、带权重/备份角色的上游与负载均衡都会被控制台以 `license required`
  拒绝；在控制台激活 Pro 授权后可用。
- **BOT 防护只覆盖无感知验证。** 滑动验证、修改有效期与请求防重放属于 Pro 功能，社区版
  固定无感知验证 + 1 小时有效期。`src_ip` 上的 `geo_*`/`asn_*` 需要 Pro 许可，`in`/`not_in`
  与 `ja4_in` 指向控制台管理的对象——`site bot condition` 都会拒绝。身份认证与动态防护
  不由 CLI 管理，请在控制台配置。
- pgcli 只发布控制台端口；受防护站点的流量端口由 tengine 绑定，经控制台或 `pg addon safeline site` 创建。
- 控制台使用 SafeLine 自带自签证书，不参与 `--tls` 体系。
- pgcli 生成并管理内部 detector 管理令牌；其余上游可选密钥（`MGT_INTERNAL_TOKEN`、
  `DETECTOR_AES_KEY`、`MGT_LICENSE_INTEGRITY_KEY`、`RIVER_*`）保持上游默认值。
- SafeLine 的状态保存在 `--data-dir` 内的自带 PostgreSQL 中；pgcli 的 PITR/备份插件面向 pgcli
  实例，不会备份这个数据库。
