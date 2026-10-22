---
title: "VictoriaMetrics"
description: "以 pgcli 插件方式运行 VictoriaMetrics——高性能、兼容 Prometheus 的时序数据库，单容器直接跑上游官方镜像"
weight: 57
---

`pg addon install victoriametrics` 以受管容器方式运行 [VictoriaMetrics](https://victoriametrics.com/)——高性能、兼容 Prometheus 的时序数据库——直接跑官方 `docker.io/victoriametrics/victoria-metrics` 镜像。与 Prometheus 和 Grafana 一样，它是**顶层插件**：不前置某个具体实例（它从多个来源摄入指标），也不依附于任何一个。三者是天然搭档——Prometheus 负责采集、VictoriaMetrics 以优异的压缩率做长期存储、Grafana 负责可视化——且生命周期形状完全一致（单容器、Linux 上宿主网络、各自端口池里的一个 web 端口、持久数据目录、开机自启、日志）。

VictoriaMetrics 与 Prometheus 的分歧在**配置方式**。Prometheus 读一份配置**文件**（`prometheus.yml`）加 CLI flags；VictoriaMetrics **完全不需要配置文件**——一切都作为**镜像名之后的 CLI flag** 传入（`-storageDataPath` / `-httpListenAddr` / `-retentionPeriod` / 自定义 flags 经 `--extra-arg`）。所以 pgcli 在创建容器时直接传这些 flags；无需渲染配置、无需 SIGHUP reload、无需 chown（镜像以 root 运行）。

> **没有 Web 登录/密码。** 与 Grafana（其 `0.0.0.0` 默认由自动生成的强密码兜底）
> 不同，VictoriaMetrics **完全不带鉴权**。它的 HTTP API 可执行任意 MetricsQL
> （PromQL 的超集），昂贵查询也能把自身打满，所以远程暴露的实例**只靠网络边界**
> 保护——防火墙，或 `--listen 127.0.0.1` 收紧为仅回环。请把它当作一个无鉴权的读/写
> 控制台来对待，而非一个有密码保护的面板。

> **平台**：Linux 是已实测的平台。设计上也支持 macOS（镜像多架构；容器在
> `pgcli-net` bridge 上以发布端口的方式跑在 podman machine 里），但尚未在
> Mac 上验证过。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `vm` | 实例名（map 键） |
| `--image` | `docker.io/victoriametrics/victoria-metrics:v1.154.1` | 覆盖镜像 |
| `--port` | 自动（基址 8428） | Web 宿主端口；自 `victoriametrics_start_port` 自动分配 |
| `--listen` | `0.0.0.0` | 绑定地址——默认开放全部网卡；`127.0.0.1` 收紧为仅回环（见上方暴露警告） |
| `--retention-months` | `1` | 数据保留月数（`-retentionPeriod`）；如 `3` = 3 个月、`12` = 1 年 |
| `--extra-arg` | — | 传给二进制的额外 CLI flag（可重复）；最后追加，故 last-wins 覆盖早期 flags。如 `--extra-arg -search.maxUniqueTimeseries=1000000` |
| `--data-dir` | 自动 | 覆盖数据目录（默认在 base 目录下） |
| `--force` | false | 即使正在运行也重建——用于应用改动的端口/监听/保留期/额外参数（config 变化时 install 也会自行重建） |

数据目录是 VictoriaMetrics 的 TSDB。它跨 `pg addon remove` 存活（用 `--clean-data` 才删除），重装同名实例即复活已存储的时序数据。镜像以 **root** 运行（与 Prometheus 的 uid 65534 不同），故无需预先 chown 数据目录。

## 安装

```bash
pg addon install victoriametrics
```

命令会按需拉取镜像、创建容器，并打印 URL、保留期与任何额外参数：

```
✓ VictoriaMetrics installed: "vm"
  Container:  pgcli-victoriametrics-default-vm
  Image:      docker.io/victoriametrics/victoria-metrics:v1.154.1
  Data:       /home/you/.pgcli/addon/victoriametrics/vm/data
  Retention:  1 month(s)
  URL:        http://0.0.0.0:8428/
```

VictoriaMetrics 立即可摄入和查询。在 Grafana 里把 `http://<host>:8428/` 加成 Prometheus 数据源（VictoriaMetrics 兼容 Prometheus），即可用 PromQL 或 MetricsQL 查询。

```bash
# 自定义保留期和搜索限制
pg addon install victoriametrics --name vm1 \
  --retention-months 12 \
  --extra-arg -search.maxUniqueTimeseries=1000000
```

对运行中的实例重复 `install`：当配置**确实变了**（端口、监听、保留期、额外参数）时**重建**，没变则原样不动；`--force` 即使配置未变也重建容器。

VictoriaMetrics **不**支持热配置重载（SIGHUP），所以所有变更都需要重建容器。这比 Prometheus 简单（无配置文件要渲染、无 reload 信号），但意味着改配置时会有短暂中断。

## 自定义参数（`--extra-arg`）

VictoriaMetrics 接受许多 CLI flags 来调优缓存大小、查询限制、摄入速率等。经 `--extra-arg` 传入（可重复）：

```bash
pg addon install victoriametrics --name vm1 \
  --extra-arg -search.maxUniqueTimeseries=1000000 \
  --extra-arg -selfScrapeInterval=10s
```

额外参数**最后**追加到容器的命令行，故它们经 last-wins 语义覆盖早期 flags。这让你在 pgcli 的一等旋钮之外调优 VictoriaMetrics，无需编辑任何配置文件。

验证 flags 已生效：

```bash
podman logs pgcli-victoriametrics-default-vm1 | grep -E '(maxUniqueTimeseries|selfScrapeInterval)'
# 2026-10-22T...  -search.maxUniqueTimeseries="1000000"
# 2026-10-22T...  -selfScrapeInterval="10s"
```

## 监听与暴露

VictoriaMetrics 的 `Listen` 默认 `0.0.0.0`：容器共享宿主网络命名空间，所以 UI 与 API 在宿主每个网卡的分配端口上可达。这与 Prometheus 的默认一致（别的机器上的 Grafana 要能连它）——但**背后没有密码**。任何能连到端口的人都能执行任意 MetricsQL、读你存储的时序数据。想要仅回环的部署，传 `--listen 127.0.0.1`（用 `--force` 重建）；`pg addon list` 会显示 `http://127.0.0.1:<port>/`。否则请在防火墙层面限制访问。

## 端口

每个 VictoriaMetrics 实例占有**一个**端口：Web UI/API。它取自以 `victoriametrics_start_port`（默认 `8428`——VictoriaMetrics 惯例 HTTP 端口）为基址的端口池，每多一个实例递增，并跳过任何已被受管容器或 `used_ports` 列表占用的端口。分配结果写入 `pg.yaml`，所以重装绝不会把别的插件在用的端口再发一遍。`8428` 与 Prometheus（`9090`）、Grafana（`3000`）、pgAdmin（`5050`）、nginx（`8080`）都不重号；显式 `--port` 仍优先。

```bash
pg addon install victoriametrics --name vm2          # 第二个实例：下一个空闲端口（8428 被占则 8429）
pg addon install victoriametrics --name vm3 --port 8500  # 显式端口仍优先
```

## 配置

```yaml
addons:
  victoriametrics:
    vm1:
      name: vm1
      image_tag: docker.io/victoriametrics/victoria-metrics:v1.154.1
      host_port: 8428
      listen: 0.0.0.0
      retention: "1"
      extra_args:
        - -search.maxUniqueTimeseries=1000000
        - -selfScrapeInterval=10s
      autostart: false
      # data_dir: /srv/victoriametrics-data      # 可选，覆盖默认位置
```

**没有配置文件**——一切都作为 CLI flags 传给 `victoria-metrics` 二进制（`-storageDataPath`、`-httpListenAddr`、`-retentionPeriod`，加上任何 `extra_args`）。`retention` 存为字符串（月数），`extra_args` 是最后追加的 CLI flags 列表。

### 列出

```bash
pg addon list
```

```
Web add-ons (victoriametrics):
  victoriametrics (name: vm1)
    Status:      running
    URL:         http://0.0.0.0:8428/
    Retention:   1 month(s)
    Extra args:  -search.maxUniqueTimeseries=1000000 -selfScrapeInterval=10s
    Data:        /home/you/.pgcli/addon/victoriametrics/vm1/data
    Image:       docker.io/victoriametrics/victoria-metrics:v1.154.1
    Container:   pgcli-victoriametrics-default-vm1
```

没有凭据可揭示——VictoriaMetrics 本就没有——所以 `--show-password` 对它不会多显示任何东西。

## 启停

容器以 podman 的 `unless-stopped` 重启策略运行，宿主重启后随 podman 服务自动回来。`start`/`stop` 显式管理它们：

```bash
pg addon stop victoriametrics
pg addon start victoriametrics
```

## 开机自启

要让 VictoriaMetrics 实例在宿主开机时自动启动，开启它：

```bash
pg autostart enable --victoriametrics --name vm1
```

这会在 `pg.yaml` 里给实例打标记，并安装/刷新一个开机服务（Linux 上是 systemd `--user` unit，macOS 上是 LaunchAgent）来执行 `pg start --autostart`。开机启动只负责把容器拉起来——数据目录在 install 时已创建，除 podman 本身外不需要任何宿主特权。启动顺序上 VictoriaMetrics 排在 Prometheus 之后。完整机制、状态与关闭方式见 [开机自启]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove victoriametrics              # 保留 TSDB——已存储时序数据跨重装存活
pg addon remove victoriametrics --clean-data # 连 TSDB 一起删
```

rootless podman 下，`--clean-data` 经 `podman unshare rm` 删除数据目录——VictoriaMetrics 以 root（容器内 uid 0）写文件，它映射到宿主的 subordinate uid，宿主用户无法直接 `rm`。

## 日志

```bash
pg logs addon victoriametrics            # 最近的控制台输出
pg logs addon victoriametrics -f         # 跟踪
pg logs addon victoriametrics -n 200     # 最近 200 行（也支持 -i / --tail）
```

## 排障

- **查询返回空数据** —— 确认 VictoriaMetrics 在运行（`pg addon list` 显示 `running`）、健康端点有响应（`curl -s http://<host>:<port>/-/healthy` 输出 `VictoriaMetrics is Healthy.`）、且你查的是正确的时间范围（用 `query_range` 配显式 `start`/`end`/`step`）。查 `pg logs addon victoriametrics` 看摄入错误。
- **Install 失败于 "container already exists"** —— 容器名由 `--name` 和命名空间派生；用 `--force` 重建，或先 `pg addon remove` 清理一次不完整的 install。
- **额外参数没生效** —— `--extra-arg` 在容器创建时应用，而非运行时。用 `--force` reinstall 以新 flags 重建容器。查 `podman logs <container>` 确认 flags 出现在启动日志里。
- **端口已被分配** —— 已分配的端口记在 `pg.yaml` 里并为后续实例跳过；先 `pg addon remove` 释放，或用 `--port N` 另选一个。
- **远程 API 打不开 / 意外可访问** —— 默认 `0.0.0.0` 下打不开是宿主防火墙问题，不是绑定地址；反之若你**不**想被访问，用 `--listen 127.0.0.1 --force`。

## 已知限制

- v1 是裸存储后端：不自带抓取能力（用 `remote_write` 让 Prometheus 推给它，或单独跑 `vmagent`）、不做告警。它是你让 Prometheus 和 Grafana 连的长期存储层。
- **无鉴权。** VictoriaMetrics 的 API 执行任意 MetricsQL；`0.0.0.0` 绑定的唯一保护是网络边界。这与 Prometheus 同一态势——不要把它暴露到不可信网络。
- pgcli 不校验 `--extra-arg` 值（install 阶段没有 pre-flight 检查）。有错的 flag 只会在启动时从容器日志里暴露。
- macOS 上容器跑在 podman machine 里；目前只在 Linux 上验证过。
