---
title: "Node_exporter"
description: "以 pgcli 插件方式运行 node_exporter——Prometheus 官方宿主机指标导出器，单容器直接跑上游官方镜像"
weight: 61
---

`pg addon install nodeexporter` 以受管容器方式运行 [node_exporter](https://github.com/prometheus/node_exporter)——Prometheus 官方的**宿主机**指标导出器，在 `/metrics` 上发布整台机器的 CPU、内存、磁盘、网络与文件系统指标——直接跑官方 `docker.io/prom/node-exporter` 镜像。与 Prometheus、VictoriaMetrics、Grafana 一样，它是**顶层插件**：它描述的是机器，不是某个 PostgreSQL 实例，故不依附于任何一个。它是 pgcli [Prometheus](../prometheus/) 插件的天然搭档——node_exporter 负责暴露、Prometheus 负责抓取。

node_exporter 与 VictoriaMetrics 形状相同——**单容器、无配置文件**（一切都作为镜像名之后的 CLI flag 传入）、无热重载、无鉴权——但它有一条别的 web 插件都没有的硬性要求，见下。

> **不可协商的一条：宿主挂载。** 一个容器若读自己的 `/proc`，报的是**容器**的指标，
> 不是宿主的。为导出整台机器，pgcli 写死三条**只读** bind 挂载——宿主 `/proc` →
> `/host/proc`、`/sys` → `/host/sys`、`/` → `/rootfs`——并用 `--path.procfs` /
> `--path.sysfs` / `--path.rootfs` 把二进制指向它们。这三个挂载固定在 pgcli 里、
> 不由用户配置；少了它们，addon 会静默导出错误的数字。它们只以 `:ro` 挂载（绝不
> 用 `:z`），所以 pgcli 绝不会给宿主根文件系统重 labeling。

> **没有登录/密码，也没有 TLS。** node_exporter 只用纯文本提供 `/metrics`。因为抓取
> 它的 Prometheus 通常在**另一台主机**上，`Listen` 默认 `0.0.0.0`——任何能连到端口的
> 人都能读到细粒度的宿主信息（硬件、挂载表、网络）。请把它限制在你的监控网段内，或
> 对同机 Prometheus 传 `--listen 127.0.0.1`。

> **平台**：Linux 是已实测的平台（容器用宿主网络，故导出器直接绑宿主端口）。设计上也
> 支持 macOS（bridge + 发布端口），但尚未在 Mac 上验证过。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `nodeexporter` | 实例名（map 键） |
| `--image` | `docker.io/prom/node-exporter:v1.12.1` | 覆盖镜像 |
| `--port` | 自动（基址 9100） | 宿主端口；自 `nodeexporter_start_port` 自动分配 |
| `--listen` | `0.0.0.0` | 绑定地址——默认开放全部网卡（跨主机 Prometheus）；`127.0.0.1` 收紧为仅回环 |
| `--extra-arg` | — | 传给二进制的额外 CLI flag（可重复）；最后追加，故 last-wins 覆盖早期 flags。如 `--extra-arg --no-collector.cpufreq` |
| `--force` | false | 即使正在运行也重建——用于应用改动的端口/监听/额外参数 |

node_exporter **无状态**——它不落盘，故没有数据目录。`pg addon remove` 只删容器；重装总是从头开始。

## 安装

```bash
pg addon install nodeexporter
```

命令会按需拉取镜像、带着三条宿主挂载创建容器，并打印抓取 URL：

```
✓ node_exporter installed: "nodeexporter"
  Container:  pgcli-nodeexporter-default-nodeexporter
  Image:      docker.io/prom/node-exporter:v1.12.1
  Metrics:    http://0.0.0.0:9100/metrics
  Host mounts: /proc, /sys, / (read-only) → /host/proc, /host/sys, /rootfs
```

因为默认绑定是 `0.0.0.0`，summary 还会打印一条暴露警告和对应的 Prometheus 命令。验证导出器给的是真实宿主数字：

```bash
curl -s http://127.0.0.1:9100/metrics | grep -E '^node_(memory_MemAvailable_bytes|load1|cpu_seconds_total)'
```

这些值应与宿主一致（`free -b`、`cat /proc/loadavg`）——若它们跟的是容器而非宿主，说明缺了挂载（经 pgcli 不可能缺，但手跑的 `node_exporter` 经常犯这个错）。

### 接入 Prometheus

导出器只有被抓取才有意义。pgcli 的 Prometheus 插件靠一行 `--scrape` job 就能抓它（无需服务发现、无需改 Prometheus 配置）：

```bash
pg addon install prometheus --name p1 --scrape node-exporter=127.0.0.1:9100
# 远程 Prometheus → 用宿主的局域网地址而非 127.0.0.1：
pg addon install prometheus --name p1 --scrape node-exporter=<host-ip>:9100
```

要把这个导出器加进**一个已经在跑、已经在抓别的 job** 的 pgcli Prometheus，用
`--scrape-append`——它把 `node-exporter` job 折叠进已存的抓取列表（同名 job 合并、
新名字追加）并 SIGHUP 热重载，绝不会清掉现有 target：

```bash
pg addon install prometheus --name p1 --scrape-append node-exporter=127.0.0.1:9100
```

（普通的 `--scrape` 会**整体替换** job 集，所以用重跑 install 会丢掉 p1 原本在抓的
其他 job。）之后在 Prometheus 里，`up{job="node-exporter"}` 为 `1`、
`count(node_cpu_seconds_total)` 很大。`--scrape` / `--scrape-append` 的完整机制见
[Prometheus 插件](../prometheus/)。

对运行中的实例重复 `install`：当配置**确实变了**（端口、监听、额外参数）时**重建**，没变则原样不动；`--force` 即使配置未变也重建。node_exporter **没有 SIGHUP reload**，所以每次改动都需要重建容器。

## 自定义参数（`--extra-arg`）

node_exporter 有几十个 collector flag。经 `--extra-arg`（可重复）逐个开关 collector，或加 `--collector.*`/`--no-collector.*`：

```bash
pg addon install nodeexporter --extra-arg --no-collector.cpufreq \
                              --extra-arg --collector.netdev.device-exclude='^veth.*'
```

额外参数**最后**追加到命令行，故经 last-wins 覆盖 pgcli 注入的默认值（包括 pgcli 自己的 `--collector.filesystem.mount-points-exclude`——它过滤掉 pgcli 刚挂上的那些伪文件系统，让 `node_filesystem_*` 不全是噪音）。没有配置文件要编辑。

## 监听与暴露

`Listen` 默认 `0.0.0.0`：在 Linux 上容器共享宿主网络命名空间，所以 `/metrics` 在宿主每个网卡的分配端口上可达。这是刻意的——抓取 node_exporter 的 Prometheus 通常在另一台机器。但**没有鉴权**，且 node_exporter 暴露细粒度宿主细节（挂载表、网络接口、硬件）。对同机 Prometheus，传 `--listen 127.0.0.1`（用 `--force` 重建）；否则请在防火墙层面限制端口。（在 macOS bridge 构建下，pgcli 会自动把绑定放宽到 `0.0.0.0`，好让发布的端口经 podman VM 保持可达。）

## 端口

每个 node_exporter 实例占有一个端口——指标端点——取自以 `nodeexporter_start_port`（默认 `9100`，node_exporter 惯例端口）为基址的端口池，每多一个实例递增，并跳过任何被受管容器或 `used_ports` 占用的端口。分配结果写入 `pg.yaml`，故重装绝不会把别的插件在用的端口再发一遍。`9100` 与 Prometheus（`9090`）、VictoriaMetrics（`8428`）、Grafana（`3000`）、Alertmanager（`9093`）、nginx（`8080`）都不重号；显式 `--port` 仍优先。

```bash
pg addon install nodeexporter --name ne2            # 第二个实例：下一个空闲端口（9101）
pg addon install nodeexporter --name ne3 --port 9200  # 显式端口仍优先
```

## 配置

```yaml
addons:
  nodeexporter:
    nodeexporter:
      name: nodeexporter
      image_tag: docker.io/prom/node-exporter:v1.12.1
      host_port: 9100
      listen: 0.0.0.0
      extra_args:
        - --no-collector.cpufreq
      autostart: false
```

**没有配置文件**——一切都作为 CLI flags 传给 `node_exporter` 二进制，三条宿主挂载由 pgcli 固定。`extra_args` 是最后追加的 flag 列表。

### 列出

```bash
pg addon list
```

```
Web add-ons (nodeexporter):
  nodeexporter (name: nodeexporter)
    Status:      running
    Metrics:     http://0.0.0.0:9100/metrics
    Extra args:  --no-collector.cpufreq
    Image:       docker.io/prom/node-exporter:v1.12.1
    Container:   pgcli-nodeexporter-default-nodeexporter
```

没有凭据可揭示——node_exporter 本就没有。

## 启停

容器以 podman 的 `unless-stopped` 重启策略运行，宿主重启后随 podman 服务自动回来。`start`/`stop` 显式管理它们：

```bash
pg addon stop nodeexporter
pg addon start nodeexporter
```

## 开机自启

要让 node_exporter 实例在宿主开机时自动启动：

```bash
pg autostart enable --nodeexporter --name nodeexporter
```

这会在 `pg.yaml` 里给实例打标记，并安装/刷新一个开机服务（Linux 上是 systemd `--user` unit，macOS 上是 LaunchAgent）来执行 `pg start --autostart`。开机启动只负责把容器拉起来——node_exporter 无状态，无需准备任何东西。完整机制、状态与关闭方式见 [开机自启]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove nodeexporter   # 删容器；没有数据要清理
```

node_exporter 不在磁盘上留存任何东西，故 `--clean-data` 除了给一条提示外没有任何效果。

## 日志

```bash
pg logs addon nodeexporter            # 最近的控制台输出
pg logs addon nodeexporter -f         # 跟踪
pg logs addon nodeexporter -n 200     # 最近 200 行（也支持 -i / --tail）
```

## 排障

- **指标像是容器的而非宿主的** —— 经 `pg addon install` 不可能（挂载写死了），但若你手跑 `node_exporter`，必须传 `/proc`/`/sys`/`/` 的只读挂载和对应的 `--path.*` flags。用 `curl -s …/metrics | grep node_uname_info` 并把 `node_memory_MemAvailable_bytes` 与宿主 `free -b` 对比来确认。
- **Prometheus 显示 target DOWN** —— 确认导出器在运行（`pg addon list`）、抓取目标的地址/端口对得上（`--scrape node-exporter=<host>:<port>`），且一个 `--listen 127.0.0.1` 的导出器没被别的机器抓。
- **某个 collector flag 没生效** —— `--extra-arg` 在容器创建时应用。用 `--force` reinstall 重建，再查 `pg logs addon nodeexporter` 看 flag 是否出现在启动行上。
- **端口已被分配** —— 已分配的端口记在 `pg.yaml` 里并为后续实例跳过；先 `pg addon remove` 释放，或用 `--port N` 另选一个。

## 已知限制

- **无鉴权、无 TLS。** node_exporter 提供纯文本 `/metrics`；`0.0.0.0` 绑定的唯一保护是网络边界。若要离开可信网络，请前置 nginx 或用防火墙限制。
- **无状态** —— 没有数据目录、没有保留期；除运行中的进程外什么都不留存。重装总是从头开始。
- **rootful 假设。** `-v /:/rootfs:ro` 挂载读整棵宿主目录树；rootless podman 下部分宿主路径可能读不到。所有目标 VM 都跑 rootful podman，与 pgcli 其余部分一致。
- pgcli 不校验 `--extra-arg` 值；有错的 flag 只会在启动时从容器日志里暴露。
- macOS 上容器跑在 podman machine 里、端口发布出来；目前只在 Linux 上验证过。
