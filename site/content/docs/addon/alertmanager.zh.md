---
title: "Alertmanager"
description: "以 pgcli 插件方式运行 Alertmanager——Prometheus 的告警去重/分组/路由端，单容器直接跑上游官方镜像"
weight: 56
---

`pg addon install alertmanager` 以受管容器方式运行 [Alertmanager](https://prometheus.io/docs/alerting/latest/alertmanager/)——位于 Prometheus 下游的告警去重、分组、路由与通知层——直接跑官方 `docker.io/prom/alertmanager` 镜像。与 Prometheus、Grafana 一样，它是**顶层插件**：不前置某个具体实例，也不依附于任何一个。Prometheus 与 Alertmanager 是天然一对——Prometheus 触发告警，Alertmanager 决定通知谁、怎么通知——而本插件在形状上是 Prometheus 的近乎完全同构体（单容器、Linux 上宿主网络、各自端口池里的一个 web 端口、持久数据目录、开机自启、日志、渲染配置文件 + 镜像名之后的 CLI flag、SIGHUP 热加载、无 Web 登录）。

> **这一版不把两者衔接起来。** `pg addon install alertmanager` 不会往任何
> `prometheus.yml` 里注入 `alerting:` 段，`pg addon install prometheus` 也不知道
> Alertmanager 的存在。两者各自独立运行；要让 Prometheus 的告警真正路由到这里，需要
> 你自己把 Prometheus 指过来（或任一侧用 `--conf-file` 手写完整路由树）。未来版本可能
> 会把两者接起来。

> **没有 Web 登录/密码——而且比 Prometheus 的赌注更大。** Alertmanager **完全不
> 带鉴权**，且它的无鉴权 API 的可写性比 Prometheus 那个以读为主的 API 更危险：任何能
> 连到端口的人都能**创建 silence**——即悄悄压掉 oncall 期待的每一条通知，且不会在
> Prometheus 自己的指标里留下任何痕迹。它的 `0.0.0.0` 默认因此**只靠网络边界**保护，
> 而这条边界在这里比插件家族里任何其他地方都更重要。请把它当作把一套告警体系的静音
> 开关交出去来对待，而非一个有密码保护的面板。

> **平台**：Linux 是已实测的平台。设计上也支持 macOS（镜像多架构；容器在
> `pgcli-net` bridge 上以发布端口的方式跑在 podman machine 里），但尚未在 Mac 上验证
> 过。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `alertmanager` | 实例名（map 键） |
| `--image` | `docker.io/prom/alertmanager:v0.34.1` | 覆盖镜像 |
| `--port` | 自动（基址 9093） | Web 宿主端口；自 `alertmanager_start_port` 自动分配 |
| `--listen` | `0.0.0.0` | 绑定地址——默认开放全部网卡；`127.0.0.1` 收紧为仅回环（见上方暴露警告） |
| `--webhook-url` | — | 要通知的 webhook URL（可重复）。每个 URL 都被渲染成**同一个** `web.hook` receiver 下的一条 `webhook_configs`——多个 URL 会扇出通知到每一个。写入任何东西之前先用 `net/url` 校验（可解析、http/https、Host 非空）；格式错误的 URL 会让 install 直接中止，不留任何痕迹。**不探测连通性**（见下）。与 `--conf-file` 互斥 |
| `--conf-file` | — | 你自己的 `alertmanager.yml` 路径——完全跳过渲染（与 `--webhook-url` 互斥）；见下 |
| `--data-dir` | 自动 | 覆盖数据目录（默认在 base 目录下） |
| `--force` | false | 即使正在运行也重建——用于不热加载改动的配置，或应用改动的端口/监听 |

数据目录承载 Alertmanager 的状态：**silence 与通知状态**。它跨 `pg addon remove` 存活
（用 `--clean-data` 才删除），重装同名实例即复活它们。镜像以固化的 `nobody`（uid/gid
65534）运行且不自建可写目录，所以 pgcli 在创建容器前先在宿主上把数据目录 chown 过
去：rootless podman 下经 `podman unshare chown 65534:65534`，rootful 宿主且 pgcli 以
root 运行时直接递归 `chown`（与 Prometheus、Grafana 完全同构，只是 id 不同）。

`--webhook-url` **不**做连通性探测，这是刻意为之：不像 Prometheus 的抓取（`GET
/metrics`，install 时即可达并可应答），webhook 只在告警真正路由过去时才发的 `POST`
——install 时拨它一下也告诉不了你什么可靠信息。唯一的提前闸门是 URL 的*形状*；端点
是否存在、是否可达、能否接受这个 payload，只能在告警实际触发时才知道（通知没到就查
`pg logs addon alertmanager`）。

## 安装

```bash
pg addon install alertmanager
```

命令会按需拉取镜像、渲染一份 `alertmanager.yml`、创建容器，并打印 URL、配置路径以及
路由渲染成什么：

```
✓ Alertmanager installed: "alertmanager"
  Container:  pgcli-alertmanager-default-alertmanager
  Image:      docker.io/prom/alertmanager:v0.34.1
  Data:       /home/you/.pgcli/addon/alertmanager/alertmanager/data
  Config:     /home/you/.pgcli/addon/alertmanager/alertmanager/alertmanager.yml (rendered: no-op receiver)
  URL:        http://0.0.0.0:9093/
```

没有 `--webhook-url` 时，渲染的配置是一条指向仅有名字、`no-op` receiver 的单路由：一
份*合法*、功能完整的配置——Web UI 与 API 在线、silence 可用、HA 集群端口已禁用——只是
**什么都不发**。这是"先把端点拉起来，路由之后再定"的默认形态。要真正发出通知，加一个
webhook：

```bash
# 一个 URL → 路由的 receiver 变成带一条 webhook_configs 的 web.hook
pg addon install alertmanager --name am1 --webhook-url http://127.0.0.1:5001/

# 多个 URL → 全部落在同一个 web.hook receiver 下（扇出到每一个）
pg addon install alertmanager --name am1 \
  --webhook-url http://127.0.0.1:5001/ \
  --webhook-url https://hooks.example.com/alert?token=abc
```

对运行中的实例重复 install：当渲染/拷贝的配置**确实变了**时**热加载**（SIGHUP），没变
则原样不动；`--force` 则重建容器——用于应用改动的 `--port` / `--listen`（它们是容器参
数，而非配置文件内容）。

带**不同**的 `--webhook-url` 集合重复 install 会整体替换已存列表（一个都不传则清空回
no-op）；正是这个 diff 决定要不要 reload。

## 自定义配置文件（`--conf-file`）

把你自己的 `alertmanager.yml` 交给 pgcli，它就完全跳过渲染：

```bash
pg addon install alertmanager --name am1 --conf-file /etc/alertmanager/my.yml
```

pgcli 把你的文件**拷贝**进托管路径（`<base>/addon/alertmanager/<name>/alertmanager.yml`），
并只读挂载**这份拷贝**——绝不挂你的原文件。这是刻意为之：单文件 bind mount 锁死源
inode，原地改你的原文件不会传播进运行中的容器。正因它是拷贝，改完你的原文件后跑
`pg addon alertmanager reload` 会重拷并通知 Alertmanager 重读。pgcli 不校验、不改写文
件内容（与 nginx/haproxy/prometheus 的 `--conf-file` 同一约定）。`--conf-file` 与
`--webhook-url`（塑造渲染文件的那个 flag）互斥——真正的路由树（email/Slack/PagerDuty、
子路由、inhibition）活在你自己的文件里。重装时省略 `--conf-file` 即回到渲染模式。

这份文件就是普通的 Alertmanager YAML——pgcli 不往里加东西，除了"能解析"之外对它也没
任何要求。下面是一份完整示例，含子路由树、inhibition，以及三种不同类型的 receiver：

```yaml
# 一份完整的 Alertmanager 配置，配合 `pg addon install alertmanager
# --name am1 --conf-file /path/to/this.yml` 使用——pgcli 本来会渲染的那
# 份被下面这些完全替代；只有容器 flag
# （--web.listen-address、--storage.path、--cluster.listen-address=）
# 仍归 pgcli 管。
global:
  resolve_timeout: 5m
  smtp_smarthost: "smtp.example.com:587"
  smtp_from: "alertmanager@example.com"
  smtp_auth_username: "alertmanager@example.com"
  smtp_auth_password: "CHANGE-ME"
  slack_api_url: "https://hooks.slack.com/services/T00000/B00000/XXXXXXXX"

inhibit_rules:
  # 同一实例上同一告警的 critical，会抑制它的 warning/info 噪音
  - source_matchers: ['severity = critical']
    target_matchers: ['severity =~ warning|info']
    equal: ['alertname', 'instance']

route:
  receiver: default
  group_by: ['alertname']
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 4h
  routes:
    # critical 发到 oncall 邮箱，且仍会继续向下匹配到 default
    - matchers: ['severity = critical']
      receiver: oncall-email
      continue: true
    # storage 团队的告警走自己的 Slack channel
    - matchers: ['team = storage']
      receiver: storage-slack
      group_by: ['alertname']
      group_wait: 10s

receivers:
  - name: default
    webhook_configs:
      - url: "http://127.0.0.1:5001/"

  - name: oncall-email
    email_configs:
      - to: "oncall@example.com"
        send_resolved: true

  - name: storage-slack
    slack_configs:
      - channel: "#storage-alerts"
        send_resolved: true
```

pgcli 本身不校验这份文件（install 阶段没有 `amtool check-config` 闸门——见已知限
制），但镜像自带 `amtool`，所以你可以在信任一次 reload 之前，进正在运行的容器里检查
托管的那份拷贝（容器名以 `pg addon list` 打印的为准，带命名空间前缀——上面那段实录里
是 `pgcli-alertmanager-default-am1`）：

```bash
pg addon install alertmanager --name am1 --conf-file /etc/alertmanager/my.yml
# 编辑 /etc/alertmanager/my.yml ...
podman exec pgcli-alertmanager-default-am1 amtool check-config /etc/alertmanager/alertmanager.yml
pg addon alertmanager reload --name am1   # 文件没问题了再重拷 + SIGHUP
```

## 热加载（配置热更新）

```bash
pg addon alertmanager reload --name am1
```

它重写托管的 `alertmanager.yml`（渲染模式重渲、`--conf-file` 模式重拷你的文件），并向
运行中的容器发 **SIGHUP**，让 Alertmanager 原地重读——不重建容器、不中断通知。

与 Prometheus 不同，**Alertmanager 的 `POST /-/reload` 端点是存在的，且无法关闭**（没
有类似 `--web.enable-lifecycle` 的开关）。pgcli 仍然用 SIGHUP 重载——不是因为那条路由
被关掉了，而是为了永远不需要依赖一条按镜像设计就一直敞开的无鉴权 HTTP 端点。任何能打
到端口的人本就能直接打这条路由；pgcli 只是不往这个面上再加东西。若你 reload 一份被拒
绝的手写 `--conf-file`，Alertmanager 会沿用上一份好配置并打日志，所以之后请查
`pg logs addon alertmanager`。实例须已在运行（`pg addon start alertmanager`）。

## 监听与暴露

Alertmanager 的 `Listen` 默认 `0.0.0.0`：容器共享宿主网络命名空间，所以 UI 与 API 在
宿主每个网卡的分配端口上可达。**背后没有密码，而且——与 Prometheus 不同——这个 API
允许调用方创建 SILENCE**，即压掉 oncall 期待的每一条通知，还能重载自身配置。仅回环
部署才是安全的默认：传 `--listen 127.0.0.1`（用 `--force` 重建）；`pg addon list` 会
显示 `http://127.0.0.1:<port>/`。否则请在防火墙层面限制访问，并把敞开的 `0.0.0.0` 当
作一个可被触达的静音开关来对待。

## 端口

每个 Alertmanager 实例占有**一个**端口：Web UI/API。它取自以 `alertmanager_start_port`
（默认 `9093`——Alertmanager 惯例端口）为基址的端口池，每多一个实例递增，并跳过任何
已被受管容器或 `used_ports` 列表占用的端口。分配结果写入 `pg.yaml`，所以重装绝不会把
别的插件在用的端口再发一遍。`9093` 与 Grafana（`3000`）、pgAdmin（`5050`）、
Prometheus（`9090`）、nginx（`8080`）都不重号；显式 `--port` 仍优先。

> **9094——HA gossip 端口——永不被开启，因为它被一个 flag 关掉了。**
> Alertmanager 的高可用集群默认在 TCP+UDP `9094` 上监听，且对一个单实例而言会不停尝
> 试引导永远不会到来的 peer。pgcli 传 `--cluster.listen-address=`（空串）彻底关掉它
> ——这是单实例约定，每次创建都强制执行，于是集群端口永远不开、也没有 UDP 监听器残
> 留。（这只关乎 `9094` 的*集群*用途；这个数字并未被预留，所以 web 端口池照样可以
> 把 `9094` 作为第二个实例的 *web* 端口分配出去——见下面的示例。）把多个 pgcli 托管的
> Alertmanager 集群成一个 gossip 组并未接通（见已知限制）。

```bash
pg addon install alertmanager --name am2          # 第二个实例：9093 之后的下一个空闲端口（比如 9094——端口池是纯数字分配，被禁用的集群端口号并没有被预留）
pg addon install alertmanager --name am3 --port 9200  # 显式端口仍优先
```

## 配置

```yaml
addons:
  alertmanager:
    am1:
      name: am1
      image_tag: docker.io/prom/alertmanager:v0.34.1
      host_port: 9093
      listen: 0.0.0.0
      webhook:
        - http://127.0.0.1:5001/
        - https://hooks.example.com/alert?token=abc
      autostart: false
      # conf_file: /etc/alertmanager/my.yml   # 用户文件模式，跳过渲染
      # data_dir: /srv/alertmanager-data      # 可选，覆盖默认位置
```

渲染的 `alertmanager.yml` 承载一个 `global.resolve_timeout`、一条根路由
（`group_by: ['alertname']`，默认 group_wait/interval/repeat_interval）指向单个
receiver，以及这个 receiver 的 `webhook_configs`——`web.hook` 下一个 `webhook` URL 一
条，列表为空时则是仅有名字的 `no-op` receiver。监听绑定、存储路径、被禁用的集群地址
**不在**那个文件里——它们作为 CLI flag 传给 `alertmanager` 二进制
（`--web.listen-address`、`--storage.path`、`--cluster.listen-address=`），这正是
`--listen` 能与 `--conf-file` 并存的原因。

### 列出

```bash
pg addon list
```

```
Web add-ons (alertmanager):
  alertmanager (name: am1)
    Status:      running
    URL:         http://0.0.0.0:9093/
    Config:      /home/you/.pgcli/addon/alertmanager/am1/alertmanager.yml [rendered (2 webhook)]
    Data:        /home/you/.pgcli/addon/alertmanager/am1/data
    Image:       docker.io/prom/alertmanager:v0.34.1
    Container:   pgcli-alertmanager-am1
```

没有凭据可揭示——Alertmanager 本就没有——所以 `--show-password` 对它不会多显示任何东
西，`pg addon password alertmanager` 也解析不出任何东西。

## 启停

容器以 podman 的 `unless-stopped` 重启策略运行，宿主重启后随 podman 服务自动回来。
`start`/`stop` 显式管理它们：

```bash
pg addon stop alertmanager
pg addon start alertmanager
```

## 开机自启

要让 Alertmanager 实例在宿主开机时自动启动，开启它：

```bash
pg autostart enable --alertmanager --name am1
```

这会在 `pg.yaml` 里给实例打标记，并安装/刷新一个开机服务（Linux 上是 systemd `--user`
unit，macOS 上是 LaunchAgent）来执行 `pg start --autostart`。开机启动只负责把容器拉起
来——读磁盘上已存在的托管 `alertmanager.yml`，且数据目录在创建时已 chown 过，除 podman
本身外不需要任何宿主特权。启动顺序上 Alertmanager 排在 Prometheus 之后、nginx 之前——
是上面那个采集器天然的搭档，即使 v1 并未把两者接通。完整机制、状态与关闭方式见 [开机
自启]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove alertmanager              # 保留 silence 与通知状态——它们跨重装存活
pg addon remove alertmanager --clean-data # 连它们一起删
```

rootless podman 下，`--clean-data` 经 `podman unshare rm` 删除数据目录——Alertmanager
以容器内 uid（65534）写文件，它映射到宿主的 subordinate uid，宿主用户无法直接 `rm`。
`--conf-file` 模式只删 pgcli 的拷贝；你的原文件在何处都不被触碰。

## 日志

```bash
pg logs addon alertmanager            # 最近的控制台输出
pg logs addon alertmanager -f         # 跟踪
pg logs addon alertmanager -n 200     # 最近 200 行（也支持 -i / --tail）
```

## 排障

- **`install` 失败于 "chowning Alertmanager data dir ... to 65534:65534"** —— 镜像以
  `nobody`（uid/gid 65534）运行且从不自己修复属主，所以 pgcli 在创建容器前先把宿主数
  据目录 chown 过去：**rootless** podman 下经 `podman unshare chown`（映射后的
  subordinate uid 才是正确目标），**rootful** 宿主且 pgcli 自身以 root 运行时直接递归
  `chown`。pgcli 是直接调用 `podman` CLI，所以只在这两种情形之外才会失败——例如非
  root 的 `pg` 被显式指向别人的 rootful daemon。遇到时以 root 跑 install，或改用
  rootless podman；单纯 `sudo chown -R 65534:65534 <data-dir>` 不能消除该错误，因为每
  次 install 都会重新校验属主。
- **收不到任何通知** —— 最常见的原因是 `no-op` receiver：一次不带 `--webhook-url` 的
  install 渲染的配置永远什么都不发。查 `pg addon list` 里的 `Config:` 行（它写的是
  `no-op receiver` 还是 `rendered (N webhook)`）。若确实配了真实 webhook，它在 install
  时**不**做连通性探测——可达性只有告警真正触发时才知道，所以要查
  `pg logs addon alertmanager` 里的 POST 失败，并确认端点能接受 Alertmanager 的 webhook
  payload。
- **`--webhook-url` 中止于解析错误** —— 这是设计行为：每个 URL 都在 pg.yaml 或任何文
  件被写入**之前**完成校验（`net/url` 解析、http/https scheme、Host 非空），所以格式错
  误的 URL 不会动到运行中的实例与配置。改对 URL 再重跑。
- **配置 reload 没生效** —— `pg addon alertmanager reload` 需要容器在运行；若新的
  `alertmanager.yml` 被拒绝（路由引用了未定义的 receiver、YAML 格式错误），
  Alertmanager 会沿用上一份好配置并打日志。查 `pg logs addon alertmanager`，改好文件，
  再 reload。
- **端口已被分配** —— 已分配的端口记在 `pg.yaml` 里并为后续实例跳过；先 `pg addon
  remove` 释放，或用 `--port N` 另选一个。
- **远程 API 打不开 / 意外可访问** —— 默认 `0.0.0.0` 下打不开是宿主防火墙问题，不是绑
  定地址；反之若你**不**想被访问（记住：可触达即意味着任何人能创建 silence），用
  `--listen 127.0.0.1 --force`。

## 已知限制

- **未与 Prometheus 接通。** 本插件既不往 `prometheus.yml` 注入 `alerting:` 段，也不从
  Prometheus 读取任何信息；两者互相独立（见开头那段说明）。真正的
  Prometheus→Alertmanager 路由留给你自己（`--conf-file`）或未来版本。
- **无鉴权**——且其后果比 Prometheus 更尖锐。API 能创建 silence（静音 oncall）、能重载
  配置，且 `POST /-/reload` 在镜像里无法关闭（pgcli 只是不去用它）。`0.0.0.0` 绑定的
  唯一保护是网络边界；不要把它暴露到不可信网络。
- **单实例 / 集群已禁用。** `--cluster.listen-address=` 始终以空串传入，所以 HA gossip
  （TCP+UDP 9094）永不打开。把多个 pgcli 托管的 Alertmanager 集群起来在 v1 里不支持。
- **Webhook 在 install 时不做连通性探测**（只校验 URL *形状*）。可达性与 payload 是否
  被接受，在告警触发时才浮现于日志。
- pgcli 渲染或拷贝 `alertmanager.yml`，但不校验它（install 阶段没有 `amtool
  check-config` 闸门）。渲染出的配置不可能有错，但有错的 `--conf-file` 只会在 reload 时
  从容器日志里暴露。
- 宿主侧的属主 chown 在 rootless podman（`podman unshare chown`）下自动完成，pgcli 以
  root 跑在 rootful 宿主上也自动完成（直接 `chown`）。非 root 的 pgcli 连接一个显式配
  置的远程 rootful daemon 时没有自动路径。
- macOS 上容器跑在 podman machine 里；目前只在 Linux 上验证过。
