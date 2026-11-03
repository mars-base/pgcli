---
title: "vmalert"
description: "以 pgcli 插件方式运行 vmalert——VictoriaMetrics 规则求值引擎，对 VictoriaLogs 或 VictoriaMetrics 求值告警/记录规则并通知 Alertmanager"
weight: 59
---

`pg addon install vmalert` 以受管容器方式运行 [vmalert](https://docs.victoriametrics.com/vmalert/)——VictoriaMetrics 家族的规则求值引擎——直接跑官方 `docker.io/victoriametrics/vmalert` 镜像。它是**顶层插件**：对告警（及记录）规则求值，并把已触发的告警转发给一个或多个 Alertmanager 兼容的 notifier，是 [VictoriaLogs 插件]({{< relref "../addon/victorialogs" >}}) / [VictoriaMetrics 插件]({{< relref "../addon/victoriametrics" >}}) 与 [Alertmanager 插件]({{< relref "../addon/alertmanager" >}}) 之间的缺失环节。

**一个实例只对一个 datasource 求值。** 日志与指标不能在同一个 vmalert 进程里混用：默认 datasource 是 VictoriaLogs（`http://127.0.0.1:9428`），指标规则集必须显式传 `--datasource-url http://127.0.0.1:8428`（或其他兼容 MetricsQL/PromQL 的端点）。

规则与模板存放在**宿主目录并只读挂载**进容器。没有渲染出来的配置文件：直接编辑 YAML，然后执行 `pg addon vmalert reload`。

> **没有鉴权，也没有 TLS。** 规则/告警 API 与 `GET /-/reload` 对任何能连上端口的人开放。`--listen` 默认 `0.0.0.0`（Prometheus 体系的惯例）；不可信网络用 `--listen 127.0.0.1`，或在防火墙层面限制访问。

> **平台**：Linux 是已实测的平台。设计上也支持 macOS（镜像多架构；容器在
> `pgcli-net` bridge 上以发布端口的方式跑在 podman machine 里），但尚未在
> Mac 上验证过。macOS 下容器内的 `127.0.0.1` 是容器自己的回环，默认
> datasource/notifier URL 必须改成 `host.containers.internal`（或容器名）。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `vmalert` | 实例名（map 键） |
| `--image` | `docker.io/victoriametrics/vmalert:v1.133.0` | 覆盖镜像 |
| `--port` | 自动（基址 8880） | HTTP/API 宿主端口；自 `vmalert_start_port` 自动分配 |
| `--listen` | `0.0.0.0` | 绑定地址——默认开放全部网卡；`127.0.0.1` 收紧为仅回环 |
| `--datasource-url` | `http://127.0.0.1:9428` | **唯一**的规则查询后端（默认 VictoriaLogs；指标场景用 `http://127.0.0.1:8428`） |
| `--notifier-url` | `http://127.0.0.1:9093` | Alertmanager 兼容 notifier（可重复）。传该 flag 会整体替换已存列表 |
| `--rules-dir` | `<base_dir>/addon/vmalert/<name>/rules` | 宿主规则目录，只读挂载到 `/etc/vmalert/rules` |
| `--templates-dir` | `<base_dir>/addon/vmalert/<name>/templates` | 宿主模板目录，只读挂载到 `/etc/vmalert/templates` |
| `--remote-write-url` | — | 记录规则结果与告警状态持久化的 remote-write **base URL**（可重复） |
| `--remote-read-url` | — | 重启后恢复告警状态的 remote-read **base URL**（可重复） |
| `--external-label` | — | 附加到每条生成告警/序列的 `NAME=VALUE` 标签（可重复；HA 副本标签用） |
| `--external-url` | — | UI 链接使用的外部可达基 URL |
| `--extra-arg` | — | 传给二进制的额外 CLI flag（可重复）；最后追加，故 last-wins 覆盖早期 flags |
| `--force` | false | 即使正在运行也重建——用于应用改动的端口/监听/URL/标签 |

vmalert **拒绝** `--data-dir` 与 `--conf-file`：它没有本地状态，也没有配置文件。

## 规则与模板

规则目录首次安装时写入 `alert.rules.yaml`，模板目录写入 `default.tpl`；pgcli **绝不覆盖你编辑过的文件**。两个目录都以只读方式挂载，文件归运维所有。

容器内加载使用 glob：

```text
-rule=/etc/vmalert/rules/*.yaml
-rule.templates=/etc/vmalert/templates/*.tpl
```

只匹配顶层 `*.yaml` 与 `*.tpl` 文件。**`*.yml` 文件会被静默忽略**——请改名为 `.yaml`。新加入目录的文件在 reload 后生效，无需重建容器；子目录不会被加载。

VictoriaLogs 规则组（参考 role 的布局）：

```yaml
groups:
  - name: victoria-logs
    type: vlogs
    interval: 1m
    rules:
      - alert: found-999-log
        expr: '_time:5m | 999 | count() as cnt | filter cnt:>0'
        labels:
          severity: critical
        annotations:
          description: 'Found 999 logs in the last 5 minutes'
```

指标规则组（需要 `--datasource-url http://127.0.0.1:8428`）：

```yaml
groups:
  - name: metrics
    interval: 30s
    rules:
      - alert: instance-down
        expr: up == 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: 'Instance {{ $labels.instance }} is down'
```

## 安装

使用默认值的日志告警（VictoriaLogs datasource、Alertmanager notifier）：

```bash
pg addon install vmalert
```

指标告警——改指向 VictoriaMetrics：

```bash
pg addon install vmalert --name vmalert-metrics \
  --datasource-url http://127.0.0.1:8428
```

自定义目录、多个 notifier 与 HA 副本标签：

```bash
pg addon install vmalert \
  --rules-dir /srv/vmalert/rules \
  --templates-dir /srv/vmalert/templates \
  --notifier-url http://127.0.0.1:9093 \
  --notifier-url http://127.0.0.1:9094 \
  --external-label replica=a \
  --listen 127.0.0.1
```

安装摘要会打印容器、镜像、URL、datasource、notifier、规则/模板目录，以及已配置的 remote write/read 与外部标签。

## 重载

```bash
pg addon vmalert reload --name vmalert
```

Reload 向运行中的实例发送 `GET /-/reload`。它是**异步**的：2xx 只表示信号已被接受。非法 YAML 会记录到容器日志，旧规则继续运行，因此要确认结果：

```bash
pg addon vmalert status --name vmalert
pg logs addon vmalert --name vmalert
```

`status` 打印已存配置，并从 `/api/v1/rules` 读取实时的 `Groups`、`Rules`、`firing`/`pending` 计数以及每个组的名称/规则数/文件。停机的实例只打印 `stopped`，不会查询 API。

## 重启后的告警状态

这两个参数不是规则求值的 datasource——那始终是 `--datasource-url`。它们持久化的是 vmalert 自己的输出：recording rules 的序列，以及 `ALERTS` / `ALERTS_FOR_STATE` 状态序列。

- 只做记录规则（持久化其输出序列）：单配 `--remote-write-url`。
- 重启后恢复告警状态：两个都配，且指向**同一个**存储。
- 纯日志告警、可接受重启后状态重置：两个都不配。

vmalert 的告警状态保存在内存中。要在**重启**后恢复，必须**同时**配置 `--remote-write-url` 与 `--remote-read-url`（例如都指向 VictoriaMetrics 插件）：

```bash
pg addon install vmalert \
  --remote-write-url http://127.0.0.1:8428 \
  --remote-read-url http://127.0.0.1:8428
```

传 **base URL**：vmalert 自己追加——remote write 追加 `/api/v1/write`，remote read 追加 `/api/v1/query`，所以 `http://127.0.0.1:8428/api/v1/write` 会变成 `/api/v1/write/api/v1/write` 并 404。这与 Prometheus 插件的 `--remote-write-url`（完整路径）语义不同。

两者都必需，且目标存储要兼容。热重载不会触发状态恢复；只配其中一个也不会。仅 `--remote-write-url` 仍可用于记录规则结果。

## HA

每个副本各跑一个 vmalert，并用不同的外部标签让 Alertmanager 去重：

```bash
pg addon install vmalert --name vmalert-a --external-label replica=a
pg addon install vmalert --name vmalert-b --external-label replica=b
```

没有 leader election——每个副本对同一套规则求值并发送告警，由 Alertmanager 按标签集去重。`--external-url` 只影响 UI 链接。

## 生命周期、日志与自启动

```bash
pg addon start vmalert --name vmalert
pg addon stop vmalert --name vmalert
pg addon remove vmalert --name vmalert              # 保留规则/模板
pg addon remove vmalert --name vmalert --clean-data # 连同 pgcli 托管目录一起删除
pg logs addon vmalert --name vmalert
pg autostart enable --vmalert --name vmalert
```

有效配置发生变化（或带 `--force`）时，重装会重建正在运行的容器；配置未变化则是 no-op。install/start/stop/`--force` 都不会修改规则与模板文件。

`remove` 默认保留规则与模板目录。`--clean-data` **只删除 pgcli 托管目录**；用户自带的 `--rules-dir`/`--templates-dir` 永不删除（会打印路径）。是挂载点的目录会跳过并告警。

开机时，`pg start --autostart` 会在 VictoriaLogs、VictoriaMetrics 与 Alertmanager 之后启动 vmalert，确保默认 datasource 与 notifier 已在监听。

## 限制

- 每个实例只有一个 datasource——日志与指标不能混用。
- API 与 `/-/reload` 没有 basic auth、TLS 或任何鉴权。
- 没有 leader election（HA 依赖外部标签 + Alertmanager 去重）。
- 没有本地告警状态持久化；需要 remote write + remote read。
- 只加载顶层 `*.yaml` / `*.tpl`；`*.yml` 被忽略。
- vmalert 的发布节奏与 VictoriaMetrics 主版本不同；升级用 `--image`。

## 验证记录

2026-11-03 在 Linux（debian12-01）上使用 `docker.io/victoriametrics/vmalert:v1.133.0`，
通过 `test/addon/test_vmalert.sh` 完成端到端验证（48 项断言）：默认 VictoriaLogs datasource
产生真实 vlogs 告警并送达 Alertmanager、reload 后新增 group 生效、相同参数重装为 no-op、
配置变更会重建容器、`--force` 不会覆盖用户规则，以及 `remove`/`--clean-data` 对托管目录与
显式目录的清理边界。
