---
title: "Prometheus"
description: "以 pgcli 插件方式运行 Prometheus——Grafana 读取的指标采集后端，单容器直接跑上游官方镜像"
weight: 55
---

`pg addon install prometheus` 以受管容器方式运行 [Prometheus](https://prometheus.io/)——指标采集与告警后端——直接跑官方 `docker.io/prom/prometheus` 镜像。与 Grafana 一样，它是**顶层插件**：不前置某个具体实例（它抓很多端点），也不依附于任何一个。二者是天然搭档——Prometheus 负责采集与存储、Grafana 负责可视化——且生命周期形状完全一致（单容器、Linux 上宿主网络、各自端口池里的一个 web 端口、持久数据目录、开机自启、日志）。

Prometheus 与 Grafana 的分歧在**配置方式**。Grafana 只经环境变量（`GF_*`）；Prometheus 读一份配置**文件**（`prometheus.yml`），且把运行时调优作为**镜像名之后的 CLI flag** 传入（`--config.file` / `--web.listen-address` / `--storage.tsdb.path` / `--storage.tsdb.retention.time`）。所以 pgcli 渲染 `prometheus.yml`、只读 bind mount 进容器，并让 `--config.file` 指向容器内该路径。

> **没有 Web 登录/密码。** 与 Grafana（其 `0.0.0.0` 默认由自动生成的强密码兜底）
> 不同，Prometheus **完全不带鉴权**。它的 HTTP API 可执行任意 PromQL，昂贵查询也
> 能把自身打满，所以远程暴露的实例**只靠网络边界**保护——防火墙，或
> `--listen 127.0.0.1` 收紧为仅回环。请把它当作一个无鉴权的读/写控制台来对待，而
> 非一个有密码保护的面板。

> **平台**：Linux 是已实测的平台。设计上也支持 macOS（镜像多架构；容器在
> `pgcli-net` bridge 上以发布端口的方式跑在 podman machine 里），但尚未在
> Mac 上验证过。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `prometheus` | 实例名（map 键） |
| `--image` | `docker.io/prom/prometheus:v3.15.0` | 覆盖镜像 |
| `--port` | 自动（基址 9090） | Web 宿主端口；自 `prometheus_start_port` 自动分配 |
| `--listen` | `0.0.0.0` | 绑定地址——默认开放全部网卡；`127.0.0.1` 收紧为仅回环（见上方暴露警告） |
| `--scrape` | —（自带自监控 job） | 抓取声明 `JOB=HOST:PORT[,HOST:PORT...] [KEY=VALUE...]`（可重复）——job 名必填，逗号分隔的多个 target 全部并入这一个 job，在渲染的 `prometheus.yml` 里成一个 `static_configs` job；target 列表之后的裸 `KEY=VALUE` token 成为**逐 target 标签**，作用于该 spec 的所有 target（带标签的 spec 含空格，整个值要用引号包成**一个** shell 参数）。同一 job 名跨 spec 折叠为一个 job；同一 node 被两个 spec 提到时标签合并、只抓**一次**，而标签集不同的 node 拆成同 job 下多个 `static_configs` 块（即 `nodename=api1` 这类按节点打标签的原生形状）。声明的是**完整** job 集合（整体替换已存列表）。目标会被宿主侧 `GET /metrics` 探测，探测不通仅告警 |
| `--scrape-append` | — | 同样的声明格式（标签同样支持），但把追加的 target **连同其标签**合并进已存的 `--scrape` 列表（同名 job 并入、新名字排到末尾）而非替换。每个被追加的目标都会探测，任一拉不到数据即**中止 install**——什么都不写。与 `--scrape`、`--conf-file` 互斥 |
| `--scrape-interval` | `15s` | 渲染配置里的全局 `scrape_interval`；与 `--conf-file` 互斥 |
| `--retention` | `15d` | TSDB 保留期（`--storage.tsdb.retention.time`）；时长如 `15d`、`6mo`、`2y`。是 CLI flag，故与 `--conf-file` 可并存 |
| `--conf-file` | — | 你自己的 `prometheus.yml` 路径——完全跳过渲染（与 `--scrape`/`--scrape-append`/`--scrape-interval`/`--alertmanager-url`/`--remote-write-url`/`--remote-write-queue` 互斥）；见下 |
| `--alertmanager-url` | — | 本 Prometheus 转发已触发告警到的 Alertmanager 实例（可重复）：裸 `HOST[:PORT]`（不写端口则补 `9093`）或 `http(s)://HOST[:PORT][/path]`。渲染的配置会多出一段 `alerting:` 指向它，**并且**多出一个对应的 `alertmanager` 抓取 job（顺带盯住 Alertmanager 自己的 `/metrics`——不是另一个开关，二者一体）。全部条目先校验后落盘，且必须同一种 scheme（http/https 混用即中止）；**不**探测连通性。与 `--conf-file` 互斥；见下 |
| `--remote-write-url` | — | 把每个抓取到的样本推送一份到的远端存储 URL（可重复；完整 `http(s)://host:port/path` URL）。渲染的配置多出一段顶级 `remote_write:`。**本地 TSDB 仍按自己的 `--retention` 保留并可查**——远端是一份 WAL 兜底的副本，远端挂掉只是队列积压，恢复后自动重放。先校验（必须是带 host 的 http(s) URL）；**不**探测连通性。与 `--conf-file` 互斥；见"把样本推给 VictoriaMetrics"一节 |
| `--remote-write-queue` | — | 在**每个** `--remote-write-url` 条目下渲染一段 `queue_config:`（可重复 `KEY=VALUE`）：正整数 `capacity`、`max_shards`、`min_shards`、`max_samples_per_send`，以及时长 `batch_send_deadline`、`min_backoff`、`max_backoff`（如 `5s`、`1m30s`、`2y`）。VictoriaMetrics 建议高压下加大批量（`max_samples_per_send=10000 capacity=20000 max_shards=30`）。必须搭配 `--remote-write-url`；键与值先校验后落盘（未知键、非正整数、无单位时长都会中止）。与 `--remote-write-url` 同为渲染模式；差异化到单个远端的调优仍需 `--conf-file` |
| `--rule-file` | — | rule 文件路径（`*.rules.yml`，顶层是 `groups:`；可重复）——pgcli 把每个文件拷进托管 rules 目录（只取 basename；两个文件撞名即中止），把该目录只读挂到 `/etc/prometheus/rules`，渲染的 `prometheus.yml` 随即用 `rule_files` 加载这些拷贝。**与 `--conf-file` 可并存**：拷贝和挂载无论如何都会发生，你自己的文件决定要不要引用它们。文件不存在或为空即中止——什么都不写；见下 |
| `--data-dir` | 自动 | 覆盖数据目录（默认在 base 目录下） |
| `--force` | false | 即使正在运行也重建——用于不热加载改动的配置，或应用改动的端口/监听/保留期（`--rule-file` 从无到有一样会触发重建，见下） |

数据目录是 Prometheus 的 TSDB。它跨 `pg addon remove` 存活（用 `--clean-data` 才删除），重装同名实例即复活已采集的指标。镜像以固化的 `nobody`（uid/gid 65534）运行且不自建可写目录，所以 pgcli 在创建容器前先在宿主上把数据目录 chown 过去：rootless podman 下经 `podman unshare chown 65534:65534`，rootful 宿主且 pgcli 以 root 运行时直接递归 `chown`（与 Grafana 完全同构，只是 id 不同）。

## 安装

```bash
pg addon install prometheus
```

命令会按需拉取镜像、渲染一份 `prometheus.yml`、创建容器，并打印 URL、配置路径与保留期：

```
✓ Prometheus installed: "prometheus"
  Container:  pgcli-prometheus-default-prometheus
  Image:      docker.io/prom/prometheus:v3.15.0
  Data:       /home/you/.pgcli/addon/prometheus/prometheus/data
  Config:     /home/you/.pgcli/addon/prometheus/prometheus/prometheus.yml (rendered: 1 self job + 0 scrape job(s))
  Retention:  15d
  URL:        http://0.0.0.0:9090/
```

渲染的配置**始终**含一个抓取本服务自身 `/metrics` 的自监控 job，所以绝不会空转；`--scrape` 添加更多 job。在 Grafana 里把 `http://<host>:9090/` 加成 Prometheus 数据源，即可立刻查询。

```bash
# 每个 --scrape 声明一个 job；= 前的 job 名必填，
# 逗号分隔的多个 target 共用这一个 job（同构 exporter 舰队的自然形状）
pg addon install prometheus --name p1 \
  --scrape postgres-exporter=127.0.0.1:9187 \
  --scrape node=10.0.0.5:9100,10.0.0.6:9100
```

给 target 打标签（Ansible 渲染配置里那种 `nodename: api1`）就是在 target 列表后面
追加裸 `KEY=VALUE` token——每个带标签的 node 各一条 `--scrape`：

```bash
# 一个 windows job，三个 target，各带自己的节点名
# （带标签的 spec 含空格，要整体加引号作为**一个** shell 参数）
pg addon install prometheus --name p1 \
  --scrape "windows=10.0.0.1:9182 nodename=api1" \
  --scrape "windows=10.0.0.2:9182 nodename=api2" \
  --scrape "windows=10.0.0.3:9182 nodename=api3"
```

```yaml
# 渲染出的正是原生的按节点标签形状：一个 job_name，
# 每组不同的标签集一个带 labels 的 static_configs 块
  - job_name: windows
    static_configs:
      - targets: ["10.0.0.1:9182"]
        labels:
          nodename: "api1"
      - targets: ["10.0.0.2:9182"]
        labels:
          nodename: "api2"
      - targets: ["10.0.0.3:9182"]
        labels:
          nodename: "api3"
```

分组以 **target 为中心**，所以同一个 node 被提两次绝不会抓两遍：两条 spec 都提到
`10.0.0.5:9100` 时，它合并两边的标签集、只成为**一个** target
（`node=a:1 nodename=api1` + `node=a:1 env=prod` → `a:1` 同时带两个标签）。
唯一被拒绝的是矛盾——同一 node 的同一个标签键给了两个不同值，直接中止而不是
悄悄丢掉一个。标签值不能含空白（`device="eth0, eth1"` 请改用 `--conf-file`），
标签键必须满足 `[A-Za-z_][A-Za-z0-9_]*`；值以引号渲染，所以 `yes`、`2`
不会在 YAML 里改变类型。

带 `--scrape` 重复 install 会**整体替换**已存的 job 集合。想增量追加就用 `--scrape-append`——格式与 `--scrape` 完全一致（标签同理），它把每个被追加 target（连同其标签）并入同名已存 job、新 job 名排到末尾，然后热加载：

```bash
pg addon install prometheus --name p1 --scrape-append node=10.0.0.7:9100
# 已存的 node job 多出第三个 target，标签不动
pg addon install prometheus --name p1 --scrape-append "node=10.0.0.5:9100 env=prod"
# ……或者给一个已存 target 就地补一个标签（合并，仍然只抓一次）
```

两个 flag 的探测策略刻意不同：被**追加**的目标必须以 200 应答 `GET /metrics`，否则命令在写入任何东西之前就中止（pg.yaml 与运行中的容器分毫未动）；而 `--scrape` 声明完整集合时，探测不通只告警、install 照常——Prometheus 自己会把它标成 `DOWN`，且全量声明时常包含尚未就绪的端点。探测从**宿主**拨号——Linux（宿主网络）上与容器抓取处于同一命名空间，判定即容器所见；macOS bridge 下两者不同，故直接跳过探测。

抓取目标是**从 Prometheus 容器内拨号**的。Linux（宿主网络）上 `127.0.0.1:<port>` 直达宿主回环；macOS bridge 下它到的是容器自身命名空间，所以远程或 VM 内的目标要给一个可路由地址。

对运行中的实例重复 `install`：当渲染/拷贝的配置**确实变了**时**热加载**（SIGHUP），没变则原样不动；`--force` 则重建容器——用于应用改动的 `--port` / `--listen` / `--retention`（它们是容器参数，而非配置文件内容）。

## 把告警转发给 Alertmanager（`--alertmanager-url`）

让一个 Prometheus 指向一个或多个 pgcli 托管的 Alertmanager，把已触发的告警推给它们：

```bash
pg addon install alertmanager --name am1
pg addon install prometheus --name p1 \
  --scrape postgres-exporter=127.0.0.1:9187 \
  --alertmanager-url 127.0.0.1:9093
```

渲染出的 `prometheus.yml` 会因这一个 flag 多出**两处**——一段推送已触发告警的 `alerting:`，以及一个与之对应的 `alertmanager` 抓取 job（盯住每个 Alertmanager 自己的 `/metrics`）：

```yaml
alerting:
  alertmanagers:
    - static_configs:
        - targets: ["127.0.0.1:9093"]
...
  - job_name: alertmanager
    static_configs:
      - targets: ["127.0.0.1:9093"]
```

接受的形式是裸 `host[:port]`（不写端口则取 Alertmanager 惯用的 `9093`）或完整的 `http(s)://host[:port][/path]` URL；抓取目标那侧会剥掉 scheme 与 path，`https` 则在两段都带成 `scheme: https`（CA 文件等 TLS 旋钮超出渲染范围——那些要用 `--conf-file`）。所有条目**先于任何写入**完成校验，且必须共用一种 scheme（http 与 https 混用即中止），但**不**做连通性探测：Alertmanager 死没死由 Prometheus 自己跟踪，`pg addon prometheus reload` 也修不了网络问题。重跑 install 会**整体替换**已存的 URL 列表（它不像 `--scrape` 有追加形式）。

实机验证接线：

```bash
curl -s localhost:9090/api/v1/alertmanagers           # activeAlertmanagers 列出目标
curl -s 'localhost:9090/api/v1/targets?state=any' | grep -c alertmanager
```

## 把样本推给 VictoriaMetrics（`--remote-write-url`）

Prometheus 内置的 TSDB 是编译进二进制的，换不掉——把 VictoriaMetrics（或任何远端存储）垫在它后面的方式是 `remote_write`：每个抓取周期把每个样本的**一份副本**推给远端，而本地 TSDB 照常保留自己的 `--retention` 窗口、照常提供查询。pgcli 两端都已经能跑，所以这对组合就是两次 install：

```bash
# 长期存储——回环上的单机版 VictoriaMetrics，或 vmcluster 插件的 vminsert
# （http://<host>:8480/insert/0/prometheus/api/v1/write）
pg addon install victoriametrics --name vm --listen 127.0.0.1

# 采集后端把每个样本都推进它
pg addon install prometheus --name p1 --listen 127.0.0.1 \
  --remote-write-url http://127.0.0.1:8428/api/v1/write

# 同上，再带上 VictoriaMetrics 建议的高压批量调优
pg addon install prometheus --name p1 --listen 127.0.0.1 \
  --remote-write-url http://127.0.0.1:8428/api/v1/write \
  --remote-write-queue max_samples_per_send=10000 \
  --remote-write-queue capacity=20000 \
  --remote-write-queue max_shards=30
```

渲染出的 `prometheus.yml` 多出一段顶级 `remote_write:`——给了 `--remote-write-queue` 时，每个 URL 下再多一段 `queue_config:`：

```yaml
remote_write:
  - url: "http://127.0.0.1:8428/api/v1/write"
    queue_config:
      max_samples_per_send: 10000
      capacity: 20000
      max_shards: 30
```

重跑 install 会**整体替换**已存的 URL 列表（与 `--alertmanager-url` 一样没有追加形式），URL 变化靠**热加载（SIGHUP）**生效——不重建容器。条目必须是完整的 `http(s)://host[:port]/path` URL（与 Alertmanager 目标不同，没有补端口/补 scheme 的约定——path 本身就是写入端点）；先校验但**不**探测连通性。`--remote-write-queue` 走同一条路：重复传入即整体替换已存列表、先校验（只收白名单键——整数容量/分片与时长超时），且同一组 `KEY=VALUE` 会渲染进**每个**远端条目下，要按远端差异化调优仍需 `--conf-file`。没有 `--remote-write-url` 却单独传它会被中止——无处可调。

验证链路活着且在投递：

```bash
# remote-write 队列在排空（pending ≈ 0，samples_total 持续上涨）
curl -s 'localhost:9090/api/v1/query?query=prometheus_remote_storage_samples_pending'
# 样本确实落进了 VictoriaMetrics
curl -s 'localhost:8428/api/v1/query?query=prometheus_build_info'
```

把 Grafana 从 Prometheus 改指到 VictoriaMetrics（`http://<host>:8428/`，集群版则是 `http://<host>:8481/select/0/prometheus/`）之后，Prometheus 实例就退化成薄薄一层采集+转发，VictoriaMetrics 成为可查询的存储。已在 Prometheus TSDB 里的历史数据**不会**自动回填——一次性迁移用 `vmctl prometheus` 工具。

## 加载告警规则（`--rule-file`）

Prometheus 从 `rule_files:` 评估告警/记录规则。把 rule 文件交给 pgcli，它就变成托管拷贝：

```bash
pg addon install prometheus --name p1 --rule-file /srv/rules/alert.rules.yml
```

pgcli 把每个文件拷进 `<base>/addon/prometheus/<name>/rules/`，把**该目录**只读挂到 `/etc/prometheus/rules`，渲染的配置按文件名引用拷贝：

```yaml
rule_files:
  - "/etc/prometheus/rules/alert.rules.yml"
```

目录是扁平的，所以起作用的是 **basename**：两个源文件撞同一个 basename 会中止 install（拷贝会悄悄覆盖掉一个）。

改你的**原** rule 文件，一次普通的 reload 即可生效：`pg addon prometheus reload` 在发信号**之前**会把每个 `--rule-file` 重拷进托管目录，所以 Prometheus 原地重读新规则——不用 reinstall、不重启容器：

```bash
# 编辑 /srv/rules/alert.rules.yml ...
pg addon prometheus reload --name p1
curl -s localhost:9090/api/v1/rules    # 组内容已是改动后的；或看 pg logs addon prometheus
```

坏 rule 文件的遭遇与坏配置一样：Prometheus 沿用**上一份好规则集**并把解析错误打进日志，所以手改规则后 reload 完记得查日志（或 rules API）。这些拷贝是托管产物，与渲染出的 `prometheus.yml` 一样，`pg addon remove` 无论有没有 `--clean-data` 都会删掉它们。你的原文件绝不被碰。

`--rule-file` 与其他渲染旋钮的不同点在于：它**与 `--conf-file` 可并存**——pgcli 无论如何都会拷规则、加挂载，是否引用 `/etc/prometheus/rules` 由你自己的文件决定（在里面写 `rule_files: ["/etc/prometheus/rules/*.rules.yml"]` 就能匹配到你传入的一切）。唯一一个新增后无法热加载生效的 flag 也是它：从无到有一样会改变挂载集合，而 `/etc/prometheus/rules` 这个挂载只在有 rule 文件时存在，SIGHUP 加不了 volume，所以这一跃变（及其反向）会重建容器。

## 自定义配置文件（`--conf-file`）

把你自己的 `prometheus.yml` 交给 pgcli，它就完全跳过渲染：

```bash
pg addon install prometheus --name p1 --conf-file /etc/prometheus/my.yml
```

pgcli 把你的文件**拷贝**进托管路径（`<base>/addon/prometheus/<name>/prometheus.yml`），并只读挂载**这份拷贝**——绝不挂你的原文件。这是刻意为之：单文件 bind mount 锁死源 inode，原地改你的原文件不会传播进运行中的容器。正因它是拷贝，改完你的原文件后跑 `pg addon prometheus reload` 会重拷并通知 Prometheus 重读。pgcli 不校验、不改写文件内容（与 nginx/haproxy 的 `--conf-file` 同一约定）。`--conf-file` 与渲染旋钮 `--scrape`/`--scrape-append`/`--scrape-interval`/`--alertmanager-url`/`--remote-write-url`/`--remote-write-queue` 互斥（operator 文件自己掌管其 `alerting:` 与 `remote_write:` 段）；`--retention`、`--listen` 是 CLI flag，`--rule-file` 是文件拷贝而非渲染旋钮，三者与它并存仍有效。重装时省略 `--conf-file` 即回到渲染模式。

### 完整样例

一份接好 pgcli Alertmanager、并加载 pgcli 托管 rule 文件的真实运维配置：

```yaml
# /etc/prometheus/my.yml
# pg addon install prometheus --name p1 --conf-file /etc/prometheus/my.yml
global:
  scrape_interval: 15s
  scrape_timeout: 15s
  evaluation_interval: 1m

alerting:
  alertmanagers:
    - static_configs:
        - targets:
            - localhost:9093 # 指向默认基础端口 9093 上的 pgcli alertmanager

rule_files:
  - /etc/prometheus/rules/*.rules.yml # pgcli 的托管 rules 目录（--rule-file）

remote_write: []

scrape_configs:
  - job_name: prometheus
    static_configs:
      - targets:
          - localhost:9090 # 这个 Prometheus 自身
  - job_name: nodes
    static_configs:
      - targets:
          - 10.246.36.2:9100
        labels:
          nodename: lhcloud1
      - targets:
          - 10.246.36.3:9100
        labels:
          nodename: lhcloud2
      - targets:
          - 10.246.36.4:9100
        labels:
          nodename: lhcloud3
```

上面这种带标签的 `nodes` 形状，正是如今渲染模式会产出的——三条
`--scrape node=…:9100 nodename=…` 折叠成一个 `nodes` job、各带一个有标签的
`static_configs` 块；渲染器没有暴露的按 job 附加项（抓取超时、relabel 等）仍归手写文件管。

`localhost:9090`、`localhost:9093` 能通，是因为 Linux 上容器跑在**宿主网络**里——Prometheus 容器内的 `localhost` 就是宿主，两个 web 端口都在上面监听。这份配置里能看到 pgcli 只参与了一处：`--rule-file` 会把你的 rule 文件拷进 `<base>/addon/prometheus/<name>/rules/` 并把该目录挂到 `/etc/prometheus/rules`，所以上面的 `rule_files:` glob 才解析得到；而 `alerting:` 段完全由你自己写——`--conf-file` 模式下 pgcli 不往文件里加任何东西，也不派生 `alertmanager` 抓取 job，且 `--alertmanager-url` 作为渲染旋钮会被拒绝。手写文件别等 reload 后才从日志里发现错误，先自查一遍：

```bash
c=$(podman ps --filter name=pgcli-prometheus --format '{{.Names}}' | head -1)
podman exec "$c" promtool check config /etc/prometheus/prometheus.yml
```

检查的是 pgcli 挂载进容器的那份拷贝，所以在 install（或 reload）之后跑，而不是对着你的原文件路径跑。

## 热加载（配置热更新）

```bash
pg addon prometheus reload --name p1
```

它重写托管的 `prometheus.yml`（渲染模式重渲、`--conf-file` 模式重拷你的文件，两种模式下都会重拷 `--rule-file` 的拷贝），并向运行中的容器发 **SIGHUP**，让 Prometheus 原地重读——不重建容器、不中断抓取。它刻意用信号而非 HTTP `POST /-/reload` 端点，于是那条无鉴权重载路由永远不需要开启（不设 `--web.enable-lifecycle`）。若新配置有错，Prometheus 会沿用上一份好配置并打日志，所以对手写文件 reload 后请查 `pg logs addon prometheus`。实例须已在运行（`pg addon start prometheus`）。有一类变化是信号救不了的：`--rule-file` 从无到有一样会增删 `/etc/prometheus/rules` **挂载**，所以这一跃变要靠普通 `install` 重建容器来应用——非空集合*内部*的改动，reload 就够了。

## 监听与暴露

Prometheus 的 `Listen` 默认 `0.0.0.0`：容器共享宿主网络命名空间，所以 UI 与 API 在宿主每个网卡的分配端口上可达。这与 Grafana 的默认一致（别的机器上的 Grafana 要能连它）——但**背后没有密码**。任何能连到端口的人都能执行任意 PromQL、读你采集的指标。想要仅回环的部署，传 `--listen 127.0.0.1`（用 `--force` 重建）；`pg addon list` 会显示 `http://127.0.0.1:<port>/`。否则请在防火墙层面限制访问。

## 端口

每个 Prometheus 实例占有**一个**端口：Web UI/API。它取自以 `prometheus_start_port`（默认 `9090`——Prometheus 惯例端口）为基址的端口池，每多一个实例递增，并跳过任何已被受管容器或 `used_ports` 列表占用的端口。分配结果写入 `pg.yaml`，所以重装绝不会把别的插件在用的端口再发一遍。`9090` 与 Grafana（`3000`）、pgAdmin（`5050`）、nginx（`8080`）都不重号；显式 `--port` 仍优先。

```bash
pg addon install prometheus --name p2          # 第二个实例：下一个空闲端口（9090 被占则 9091）
pg addon install prometheus --name p3 --port 9100  # 显式端口仍优先
```

## 配置

```yaml
addons:
  prometheus:
    p1:
      name: p1
      image_tag: docker.io/prom/prometheus:v3.15.0
      host_port: 9090
      listen: 0.0.0.0
      retention: 15d
      scrape:
        - postgres-exporter=127.0.0.1:9187
        - node=10.0.0.5:9100,10.0.0.6:9100
        - node=10.0.0.5:9100 env=prod   # 同一 node 再次出现 → 一次抓取，标签取并集
        - windows=10.0.0.1:9182 nodename=api1   # 裸 KEY=VALUE token = 逐 target 标签
      scrape_interval: 15s
      autostart: false
      # alertmanager: [127.0.0.1:9093]            # 仅渲染模式：转发已触发告警 + 派生的抓取 job
      # remote_write: [http://127.0.0.1:8428/api/v1/write]  # 仅渲染模式：把样本推给长期存储（VictoriaMetrics）
      # remote_write_queue: [max_samples_per_send=10000, capacity=20000]  # 在上面每个远端条目下渲染 queue_config
      # rule_files: [/srv/rules/alert.rules.yml]  # 拷进 <name>/rules，挂到 /etc/prometheus/rules
      # conf_file: /etc/prometheus/my.yml   # 用户文件模式，跳过渲染
      # data_dir: /srv/prometheus-data      # 可选，覆盖默认位置
```

渲染的 `prometheus.yml` 承载全局 `scrape_interval`、始终存在的自监控 job、以及每个 `scrape` **JOB 名**一个抓取 job——该 job 各条 spec 列出的所有 target 折叠进去，再按合并后的标签集分桶，于是每一组不同的 `KEY=VALUE` 组合成为自己单独的 `- targets:/labels:` 块；`--alertmanager-url` 会加上 `alerting:` 段与随之而来的 `alertmanager` 抓取 job，`--remote-write-url` 会加上一段顶级 `remote_write:`（`--remote-write-queue` 在每个条目下填充 `queue_config:` 块），`--rule-file` 会加上 `rule_files:` 段。`retention` 与监听绑定**不在**那个文件里——它们作为 CLI flag 传给 `prometheus` 二进制（`--storage.tsdb.retention.time`、`--web.listen-address`），这正是它们能与 `--conf-file` 并存的原因。

### 列出

```bash
pg addon list
```

```
Web add-ons (prometheus):
  prometheus (name: p1)
    Status:      running
    URL:         http://0.0.0.0:9090/
    Config:      /home/you/.pgcli/addon/prometheus/p1/prometheus.yml [rendered (1 scrape job(s))]
    Retention:   15d
    Data:        /home/you/.pgcli/addon/prometheus/p1/data
    Image:       docker.io/prom/prometheus:v3.15.0
    Container:   pgcli-prometheus-p1
```

没有凭据可揭示——Prometheus 本就没有——所以 `--show-password` 对它不会多显示任何东西。

## 启停

容器以 podman 的 `unless-stopped` 重启策略运行，宿主重启后随 podman 服务自动回来。`start`/`stop` 显式管理它们：

```bash
pg addon stop prometheus
pg addon start prometheus
```

## 开机自启

要让 Prometheus 实例在宿主开机时自动启动，开启它：

```bash
pg autostart enable --prometheus --name p1
```

这会在 `pg.yaml` 里给实例打标记，并安装/刷新一个开机服务（Linux 上是 systemd `--user` unit，macOS 上是 LaunchAgent）来执行 `pg start --autostart`。开机启动只负责把容器拉起来——读磁盘上已存在的托管 `prometheus.yml`，且数据目录在创建时已 chown 过，除 podman 本身外不需要任何宿主特权。启动顺序上 Prometheus 排在 Grafana 之后、nginx 之前。完整机制、状态与关闭方式见 [开机自启]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove prometheus              # 保留 TSDB——已采集指标跨重装存活
pg addon remove prometheus --clean-data # 连 TSDB 一起删
```

rootless podman 下，`--clean-data` 经 `podman unshare rm` 删除数据目录——Prometheus 以容器内 uid（65534）写文件，它映射到宿主的 subordinate uid，宿主用户无法直接 `rm`。`--conf-file` 模式只删 pgcli 的拷贝；你的原文件在何处都不被触碰。`--rule-file` 的原文件同样幸存——被删的只有托管的 `rules/` 拷贝，且它们在每次 `remove` 时都消失（无论有无 `--clean-data`，与渲染出的 `prometheus.yml` 一样，没有值得复活的状态）。

## 日志

```bash
pg logs addon prometheus            # 最近的控制台输出
pg logs addon prometheus -f         # 跟踪
pg logs addon prometheus -n 200     # 最近 200 行（也支持 -i / --tail）
```

## 排障

- **`install` 失败于 "chowning Prometheus data dir ... to 65534:65534"** —— 镜像以 `nobody`（uid/gid 65534）运行且从不自己修复属主，所以 pgcli 在创建容器前先把宿主数据目录 chown 过去：**rootless** podman 下经 `podman unshare chown`（映射后的 subordinate uid 才是正确目标），**rootful** 宿主且 pgcli 自身以 root 运行时直接递归 `chown`。pgcli 是直接调用 `podman` CLI，所以只在这两种情形之外才会失败——例如非 root 的 `pg` 被显式指向别人的 rootful daemon。遇到时以 root 跑 install，或改用 rootless podman；单纯 `sudo chown -R 65534:65534 <data-dir>` 不能消除该错误，因为每次 install 都会重新校验属主。
- **某个抓取目标显示 `DOWN`** —— 确认 Prometheus 能连到它（它从容器内拨号；Linux 上 `127.0.0.1:<port>` 是宿主回环，但跨主机/VM 目标需要可路由地址，macOS bridge 上尤甚）。确认 exporter 在跑、路径是 `/metrics`。`pg logs addon prometheus` 会显示抓取失败。
- **`--scrape-append` 中止于 "returned no data"** —— 这是设计行为：被追加的目标必须以 200 应答宿主的 `GET /metrics`，否则任何东西都不写入（已存实例与 `pg.yaml` 分毫未动）。要么先把 exporter 拉起来，要么改用 `--scrape`（仅告警）声明完整 job 集合。macOS 上跳过该探测。
- **配置 reload 没生效** —— `pg addon prometheus reload` 需要容器在运行；若新的 `prometheus.yml` 有格式错误，Prometheus 会沿用上一份好配置并打日志。查 `pg logs addon prometheus`，改好文件，再 reload。
- **端口已被分配** —— 已分配的端口记在 `pg.yaml` 里并为后续实例跳过；先 `pg addon remove` 释放，或用 `--port N` 另选一个。
- **远程 API 打不开 / 意外可访问** —— 默认 `0.0.0.0` 下打不开是宿主防火墙问题，不是绑定地址；反之若你**不**想被访问，用 `--listen 127.0.0.1 --force`。

## 已知限制

- v1 是裸采集后端：不自带 PostgreSQL exporter（自己装好，再 `--scrape postgres-exporter=127.0.0.1:9187` 指向它）。但长期存储与告警都是接好的：`--remote-write-url` 把抓取到的样本推给 VictoriaMetrics 插件（单机版或 `vmcluster`），`--alertmanager-url` 把已触发告警转发给 pgcli 的 Alertmanager，`--rule-file` 加载告警规则（三者都是渲染模式；其余场景用 `--conf-file` 手写）。它是你让 Grafana 连的采集/存储/查询层。
- **无鉴权。** Prometheus 的 API 执行任意 PromQL；`0.0.0.0` 绑定的唯一保护是网络边界。这比 Grafana 有密码兜底的 `0.0.0.0` 是实质更弱的态势——不要把它暴露到不可信网络。
- pgcli 渲染或拷贝 `prometheus.yml`，但不校验它（install 阶段没有 `promtool check config` 闸门）。渲染出的配置不可能有错，但有错的 `--conf-file` 只会在 reload 时从容器日志里暴露。
- 宿主侧的属主 chown 在 rootless podman（`podman unshare chown`）下自动完成，pgcli 以 root 跑在 rootful 宿主上也自动完成（直接 `chown`）。非 root 的 pgcli 连接一个显式配置的远程 rootful daemon 时没有自动路径。
- macOS 上容器跑在 podman machine 里；目前只在 Linux 上验证过。
