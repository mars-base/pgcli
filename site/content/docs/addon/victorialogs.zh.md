---
title: "VictoriaLogs"
description: "以 pgcli 插件方式运行 VictoriaLogs——VictoriaMetrics 家族的日志数据库，单容器直接跑上游官方镜像"
weight: 59
---

`pg addon install victorialogs` 以受管容器方式运行 [VictoriaLogs](https://docs.victoriametrics.com/victorialogs/)——VictoriaMetrics 家族的高性能、低成本日志数据库——直接跑官方 `docker.io/victoriametrics/victoria-logs` 镜像。与 VictoriaMetrics 和 Prometheus 一样，它是**顶层插件**：不前置某个具体实例（它从多个来源摄入日志），也不依附于任何一个。VictoriaMetrics 存指标、VictoriaLogs 存日志、Grafana 同时可视化两者——三者生命周期形状完全一致（单容器、Linux 上宿主网络、各自端口池里的一个 web 端口、持久数据目录、重启策略）。

VictoriaLogs 在**配置方式**上是 [VictoriaMetrics 插件]({{< relref "../addon/victoriametrics" >}})的精确镜像：**完全不需要配置文件**——一切都作为**镜像名之后的 CLI flag** 传入（`-storageDataPath` / `-httpListenAddr` / `-retentionPeriod` / 自定义 flags 经 `--extra-arg`）。所以 pgcli 在创建容器时直接传这些 flags；无需渲染配置、无需 SIGHUP reload、无需 chown（镜像以 root 运行）。单容器在单个 HTTP 端口上提供一切：摄入（JSON Lines、Elasticsearch 兼容 bulk、syslog、OTLP）、LogsQL 查询与内置 UI。

> **没有 Web 登录/密码。** VictoriaLogs **完全不带鉴权**。它的 HTTP API 可摄入
> 并查询任意日志，昂贵查询也能把自身打满，所以远程暴露的实例**只靠网络边界**保护
> ——防火墙，或 `--listen 127.0.0.1` 收紧为仅回环。请把它当作一个无鉴权的读/写
> 控制台来对待，而非一个有密码保护的面板。

> **平台**：Linux 是已实测的平台。设计上也支持 macOS（镜像多架构；容器在
> `pgcli-net` bridge 上以发布端口的方式跑在 podman machine 里），但尚未在
> Mac 上验证过。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `vl` | 实例名（map 键） |
| `--image` | `docker.io/victoriametrics/victoria-logs:v1.53.0` | 覆盖镜像 |
| `--port` | 自动（基址 9428） | Web 宿主端口；自 `victorialogs_start_port` 自动分配 |
| `--listen` | `0.0.0.0` | 绑定地址——默认开放全部网卡；`127.0.0.1` 收紧为仅回环（见上方暴露警告） |
| `--retention` | 二进制默认 | 数据保留时长（`30d`、`3months`、`1y` 等），原样作为 `-retentionPeriod` 传入。省略则保持 victoria-logs 自身默认 |
| `--extra-arg` | — | 传给二进制的额外 CLI flag（可重复）；最后追加，故 last-wins 覆盖早期 flags。如 `--extra-arg -search.maxQueryDuration=60s` |
| `--data-dir` | 自动 | 覆盖数据目录（默认在 base 目录下） |
| `--force` | false | 即使正在运行也重建——用于应用改动的端口/监听/保留期/额外参数 |

数据目录是 VictoriaLogs 的日志存储。它跨 `pg addon remove` 存活（用 `--clean-data` 才删除），重装同名实例即复活已存储的日志。镜像以 **root** 运行（与 Prometheus 的 uid 65534 不同），故无需预先 chown 数据目录。

## 安装

```bash
pg addon install victorialogs
```

命令会按需拉取镜像、创建容器，并打印 URL 与保留期：

```
✓ VictoriaLogs installed: "vl"
  Container:  pgcli-victorialogs-default-vl
  Image:      docker.io/victoriametrics/victoria-logs:v1.53.0
  Data:       /home/you/.pgcli/addon/victorialogs/vl/data
  Retention:  binary default (--retention to change)
  URL:        http://0.0.0.0:9428/
```

VictoriaLogs 立即可摄入和查询。打开 `http://<host>:9428/select/vmui/` 进入内置 Web UI（根路径 `/` 只是可用端点索引页），或用 HTTP API 查询：

```bash
# 摄入一条 JSON Lines 日志（Content-Type 必须为 application/json——
# 缺失时 VictoriaLogs 返回 200 但会静默丢弃数据）
echo '{"_msg":"hello from pgcli","app":"demo"}' | \
  curl -s -X POST -H 'Content-Type: application/json' \
    http://127.0.0.1:9428/insert/jsonline --data-binary @-

# 用 LogsQL 查询
curl -s http://127.0.0.1:9428/select/logsql/query \
  --data-urlencode 'query=app:demo' --data-urlencode 'limit=10'
```

对运行中的实例重复 `install`：当配置**确实变了**（端口、监听、保留期、额外参数）时**重建**，没变则原样不动；`--force` 即使配置未变也重建容器。

VictoriaLogs **不**支持热配置重载，所以所有变更都需要重建容器——与 VictoriaMetrics 一致。这比 Prometheus 简单（无配置文件要渲染、无 reload 信号），但意味着改配置时会有短暂中断。

## Web UI

每个 VictoriaLogs 实例都自带 Web UI（VMUI），无需额外安装任何组件。打开：

```
http://<host>:<port>/select/vmui/
```

根路径（`/`）只是可用 HTTP 端点的索引页；UI 位于 `/select/vmui/`。

UI 是一个面向日志的 [LogsQL](https://docs.victoriametrics.com/victorialogs/logsql/) 控制台：

- **查询** —— 输入 LogsQL 查询（如 `app:demo`、`level:error`、`error app:demo-api`）并选择时间范围，执行后即时返回结果。
- **视图** —— 可在 `Group`（按 stream 字段分组）、`Table`（点击行展开全部字段）、`JSON`（`/select/logsql/query` 原始响应）与 `Live`（实时 tail 新到日志）之间切换。
- **探索** —— 字段与 stream 选择器会列出已存储的字段名/值（底层调用 `/select/logsql/field_names`、`field_values`、`facets` 等），无需预先了解 schema 即可构造查询。

例如，摄入一条日志后在 UI 里搜索它（查询 `app:demo`）：

```bash
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"_msg":"hello from pgcli","app":"demo"}' \
  http://127.0.0.1:9428/insert/jsonline
```

UI 与 HTTP API 共享同一端口，也共享同样的**无鉴权**态势：任何能连到端口的人都能查询、也能写入日志。按上文所述用 `--listen 127.0.0.1` 或防火墙限制访问。

## 日志写入：vlagent、HTTP API、采集器

pgcli 只负责运行 VictoriaLogs 后端——日志怎么进来由你决定。共有三种方式。

### vlagent（官方 agent）

[`vlagent`](https://docs.victoriametrics.com/victorialogs/vlagent/) 是 VictoriaLogs 的官方日志 agent——`vmagent` 的**日志**对应物（`vmagent` 只负责指标，不要用它写日志）。它在端口 `9429`（可用 `-httpListenAddr` 修改）上接受与 VictoriaLogs 相同的 HTTP 摄入协议，并通过 `-remoteWrite.url` 转发到 VictoriaLogs，该 URL **必须以 `/insert/native` 结尾**：

```bash
podman run -d --name vlagent --network host \
  -v /var/lib/vlagent:/var/lib/vlagent \
  docker.io/victoriametrics/vlagent:v1.53.0 \
  -remoteWrite.url=http://127.0.0.1:9428/insert/native \
  -tmpDataPath=/var/lib/vlagent
```

这条命令**不跟踪任何日志文件**：在没有 `-kubernetesCollector` 或 `-fileCollector.glob` 时，vlagent 只接收推到端口 `9429` 的 HTTP 日志并转发。要跟踪宿主文件，必须先把目录挂进容器，再用 glob 匹配：

```bash
podman run -d --name vlagent --network host \
  -v /var/lib/vlagent:/var/lib/vlagent \
  -v /var/log/nginx:/var/log/nginx:ro \
  docker.io/victoriametrics/vlagent:v1.53.0 \
  -remoteWrite.url=http://127.0.0.1:9428/insert/native \
  -tmpDataPath=/var/lib/vlagent \
  -fileCollector.glob='/var/log/nginx/*.log'
```

适合使用 vlagent 的场景：

- **复制 / 高可用** —— 为每个 VictoriaLogs 实例重复传 `-remoteWrite.url`。vlagent 会把每条日志复制到所有目标，并为每个 URL 维护独立磁盘缓冲（`-remoteWrite.tmpDataPath`，可用 `-remoteWrite.maxDiskUsagePerURL` 限制大小）；后端宕机期间采集的日志会在其恢复后重放。
- **Kubernetes Pod 日志** —— 以 DaemonSet 方式运行并加 `-kubernetesCollector`（或使用 `victoria-logs-collector` Helm chart）。它跟踪节点上的 `/var/log/pods` 与 `/var/log/containers`（需把宿主 `/var/log` 只读挂入；使用 Docker runtime 时还要挂 `/var/lib` 以解析 runtime 符号链接），自动附加容器元数据，并把读取位点持久化在 `-tmpDataPath` 下。
- **磁盘文件** —— `-fileCollector.glob='/var/log/nginx/*.log'`（可重复；glob 必须指向文件而非目录）。路径是在**容器内**解析的，所以要先挂载宿主目录。默认 `hostname` 与 `file` 成为 stream 字段，读取位点与缓冲位于 `-tmpDataPath` 下。

如果只有单个、网络稳定的 VictoriaLogs 实例，直接发送（见下）更简单——这是推荐的默认方式。

### HTTP API

VictoriaLogs 在 UI/查询 API 的同一端口上提供多个摄入端点：

| 端点 | 格式 | 说明 |
|---|---|---|
| `/insert/jsonline` | JSON Lines（ndjson） | 必须带 `Content-Type: application/json`；缺失时返回 200 但数据被静默丢弃 |
| `/insert/elasticsearch/_bulk` | Elasticsearch / OpenSearch bulk | Filebeat / Logstash / Fluent Bit 的 Elasticsearch output 指向这里 |
| `/insert/loki/api/v1/push` | Loki JSON push | Promtail / Grafana Alloy / Loki 客户端指向这里 |
| `/insert/opentelemetry/v1/logs` | OTLP/HTTP logs | OpenTelemetry Collector 的 exporter 指向这里 |
| syslog TCP / UDP / Unix | RFC3164 / RFC5424 | 经 `--extra-arg -syslog.listenAddr.tcp=:514`（或 `.udp` / `.unix`）启用并重建 |

所有 HTTP 摄入端点都接受同一组调优参数（query string 或 `VL-*` 请求头）：`_msg_field`（日志消息所在字段，如 `message`）、`_time_field`（如 `@timestamp`）、`_stream_fields`（用逗号分隔的 stream 字段）、`extra_fields`（`env=prod`）、`ignore_fields`、`decolorize_fields`，以及 `debug=1`（只记录 payload 而不入库）。当采集器输出的 schema 无法被自动识别时用它们做映射。

```bash
# JSON Lines，显式字段映射
curl -s -X POST -H 'Content-Type: application/json' \
  --data-binary @app.jsonl \
  'http://127.0.0.1:9428/insert/jsonline?_stream_fields=host,app&_time_field=@timestamp&_msg_field=message'
```

### 日志采集器

任何能使用上述格式之一的采集器都可以；pgcli 不安装也不配置它们。常用采集器及官方配置指南：

| 采集器 | 发往 |
|---|---|
| [Filebeat](https://docs.victoriametrics.com/victorialogs/data-ingestion/filebeat/) | Elasticsearch output → `/insert/elasticsearch/_bulk` |
| [Logstash](https://docs.victoriametrics.com/victorialogs/data-ingestion/logstash/) | Elasticsearch 或 HTTP output |
| [Fluent Bit](https://docs.victoriametrics.com/victorialogs/data-ingestion/fluentbit/) / [Fluentd](https://docs.victoriametrics.com/victorialogs/data-ingestion/fluentd/) | Elasticsearch 或 HTTP output |
| [Vector](https://docs.victoriametrics.com/victorialogs/data-ingestion/vector/) | `http` sink → `/insert/jsonline`，或 Elasticsearch sink |
| [Promtail](https://docs.victoriametrics.com/victorialogs/data-ingestion/promtail/) / Grafana Alloy | Loki push → `/insert/loki/api/v1/push` |
| [OpenTelemetry Collector](https://docs.victoriametrics.com/victorialogs/data-ingestion/opentelemetry/) | OTLP/HTTP exporter → `/insert/opentelemetry/v1/logs` |
| [Telegraf](https://docs.victoriametrics.com/victorialogs/data-ingestion/telegraf/) | `http` output → jsonline |
| [journald](https://docs.victoriametrics.com/victorialogs/data-ingestion/journald/) | Vector / Fluent Bit 的 journald source → jsonline，或用 vlagent |
| [rsyslog / syslog-ng](https://docs.victoriametrics.com/victorialogs/data-ingestion/syslog/) | syslog listener（`-syslog.listenAddr.*`） |
| [Datadog Agent](https://docs.victoriametrics.com/victorialogs/data-ingestion/datadog-agent/) | Datadog intake API |

采集器也可以不直接连 VictoriaLogs，而是指向 **vlagent**（端口 `9429`）——需要缓冲、复制或统一出口时更合适。

## 自定义参数（`--extra-arg`）

VictoriaLogs 接受许多 CLI flags 来调优缓存大小、查询限制、摄入速率等。经 `--extra-arg` 传入（可重复）：

```bash
pg addon install victorialogs --name vl1 \
  --extra-arg -search.maxQueryDuration=60s \
  --extra-arg -memory.allowedPercent=80
```

额外参数**最后**追加到容器的命令行，故它们经 last-wins 语义覆盖早期 flags。这让你在 pgcli 的一等旋钮之外调优 VictoriaLogs，无需编辑任何配置文件。验证 flags 已生效（二进制不会把 flags 打进日志）：

```bash
podman inspect pgcli-victorialogs-default-vl1 --format '{{json .Config.Cmd}}'
```

## 监听与暴露

VictoriaLogs 的 `Listen` 默认 `0.0.0.0`：容器共享宿主网络命名空间，所以 UI 与 API 在宿主每个网卡的分配端口上可达。这与 VictoriaMetrics 的默认一致（别的机器上的日志采集器和 Grafana 要能连它）——但**背后没有密码**。任何能连到端口的人都能执行任意 LogsQL、读你存储的日志。想要仅回环的部署，传 `--listen 127.0.0.1`（用 `--force` 重建）；`pg addon list` 会显示 `http://127.0.0.1:<port>/`。否则请在防火墙层面限制访问。

## 端口

每个 VictoriaLogs 实例占有**一个**端口：Web UI/API。它取自以 `victorialogs_start_port`（默认 `9428`——VictoriaLogs 惯例 HTTP 端口）为基址的端口池，每多一个实例递增，并跳过任何已被受管容器或 `used_ports` 列表占用的端口。分配结果写入 `pg.yaml`，所以重装绝不会把别的插件在用的端口再发一遍。`9428` 与 VictoriaMetrics（`8428`）、vmcluster（`8480-8484`）、Prometheus（`9090`）、Grafana（`3000`）、pgAdmin（`5050`）、nginx（`8080`）都不重号；显式 `--port` 仍优先。

```bash
pg addon install victorialogs --name vl2          # 第二个实例：下一个空闲端口（9428 被占则 9429）
pg addon install victorialogs --name vl3 --port 9500  # 显式端口仍优先
```

## 配置

```yaml
addons:
  victorialogs:
    vl1:
      name: vl1
      image_tag: docker.io/victoriametrics/victoria-logs:v1.53.0
      host_port: 9428
      listen: 0.0.0.0
      retention: "30d"
      extra_args:
        - -search.maxQueryDuration=60s
        - -memory.allowedPercent=80
      # data_dir: /srv/victorialogs-data      # 可选，覆盖默认位置
```

**没有配置文件**——一切都作为 CLI flags 传给 `victoria-logs` 二进制（`-storageDataPath`、`-httpListenAddr`、`-retentionPeriod`，加上任何 `extra_args`）。`retention` 存为时长字符串（省略 = 二进制默认），`extra_args` 是最后追加的 CLI flags 列表。

### 列出

```bash
pg addon list
```

```
Web add-ons (victorialogs):
  victorialogs (name: vl1)
    Status:      running
    URL:         http://0.0.0.0:9428/
    Retention:   30d
    Extra args:  -search.maxQueryDuration=60s -memory.allowedPercent=80
    Data:        /home/you/.pgcli/addon/victorialogs/vl1/data
    Image:       docker.io/victoriametrics/victoria-logs:v1.53.0
    Container:   pgcli-victorialogs-default-vl1
```

没有凭据可揭示——VictoriaLogs 本就没有——所以 `--show-password` 对它不会多显示任何东西。

## 启停

容器以 podman 的 `unless-stopped` 重启策略运行，宿主重启后随 podman 服务自动回来。`start`/`stop` 显式管理它们：

```bash
pg addon stop victorialogs
pg addon start victorialogs
```

## 移除

```bash
pg addon remove victorialogs              # 保留日志存储——已存储日志跨重装存活
pg addon remove victorialogs --clean-data # 连日志存储一起删
```

rootless podman 下，`--clean-data` 经 `podman unshare rm` 删除数据目录——VictoriaLogs 以 root（容器内 uid 0）写文件，它映射到宿主的 subordinate uid，宿主用户无法直接 `rm`。

## 排障

- **摄入返回 200 但查询为空** —— JSON Lines 端点要求 `Content-Type: application/json`；缺失时 VictoriaLogs 接受请求但会静默丢弃数据（`/metrics` 上的 `vl_rows_ingested_total{type="jsonline"}` 保持 0）。加上该 header，或改用会自动设置的采集客户端。
- **查询返回空数据** —— 确认 VictoriaLogs 在运行（`pg addon list` 显示 `running`）、健康端点有响应（`curl -s http://<host>:<port>/-/healthy`）、且你查的是正确的时间范围与 stream 过滤（`_stream:` 前缀、`_time:` 边界）。查 `podman logs <container>` 看摄入错误。
- **Install 失败于 "container already exists"** —— 容器名由 `--name` 和命名空间派生；用 `--force` 重建，或先 `pg addon remove` 清理一次不完整的 install。
- **额外参数没生效** —— `--extra-arg` 在容器创建时应用，而非运行时。用 `--force` reinstall 以新 flags 重建容器。查 `podman inspect <container> --format '{{json .Config.Cmd}}'` 确认 flags 在命令行上（二进制不会打印它们）。
- **端口已被分配** —— 已分配的端口记在 `pg.yaml` 里并为后续实例跳过；先 `pg addon remove` 释放，或用 `--port N` 另选一个。
- **远程 API 打不开 / 意外可访问** —— 默认 `0.0.0.0` 下打不开是宿主防火墙问题，不是绑定地址；反之若你**不**想被访问，用 `--listen 127.0.0.1 --force`。

## 已知限制

- v1 是裸日志后端：pgcli 不安装任何日志采集器——按上文「日志写入」一节使用 `vlagent`、直接 HTTP API 或第三方采集器；也不做告警（自行跑 `vlalert`）。它是你让 Grafana（VictoriaLogs 数据源）和采集器连的长期日志存储。
- **无鉴权。** VictoriaLogs 的 API 摄入并查询任意日志；`0.0.0.0` 绑定的唯一保护是网络边界。这与 VictoriaMetrics 同一态势——不要把它暴露到不可信网络。
- pgcli 不校验 `--extra-arg` 值（install 阶段没有 pre-flight 检查）。有错的 flag 只会在启动时从容器日志里暴露。
- macOS 上容器跑在 podman machine 里；目前只在 Linux 上验证过。
