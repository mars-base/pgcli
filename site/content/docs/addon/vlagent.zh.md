---
title: "vlagent"
description: "以 pgcli 插件方式运行 vlagent——VictoriaLogs 官方日志 agent，跟踪任意宿主日志路径并转发给 VictoriaLogs"
weight: 59
---

`pg addon install vlagent` 以受管容器方式运行 [vlagent](https://docs.victoriametrics.com/victorialogs/vlagent/)——VictoriaLogs 家族的官方日志 agent、`vmagent` 的**日志**对应物——直接跑官方 `docker.io/victoriametrics/vlagent` 镜像。它是**顶层插件**：不前置某个具体 PostgreSQL 实例，是 [VictoriaLogs 插件]({{< relref "../addon/victorialogs" >}})的天然搭档——VictoriaLogs 存日志，vlagent 从文件或 HTTP 采集日志并转发给一个或多个 VictoriaLogs 实例。

与 VictoriaMetrics、VictoriaLogs 一样，**完全不需要配置文件**——一切都作为镜像名之后的 CLI flag 传入。pgcli 在其上增加文件采集接线：每个 `--log-path` 都以**只读方式挂载到容器内完全相同的绝对路径**，`--include`/`--exclude` 使用 vlagent 的**原生 glob 语法**（不是正则表达式）选择文件。

> **没有鉴权。** vlagent 的 HTTP 监听端口接受任何能连上的人推送的日志，而它转发的 VictoriaLogs（默认）也没有鉴权。远程暴露的实例**只靠网络边界**保护——用 `--listen 127.0.0.1` 收紧为仅回环，或在防火墙层面限制访问。

> **平台**：Linux 是已实测的平台。设计上也支持 macOS（镜像多架构；容器在
> `pgcli-net` bridge 上以发布端口的方式跑在 podman machine 里），但尚未在
> Mac 上验证过。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `va` | 实例名（map 键） |
| `--image` | `docker.io/victoriametrics/vlagent:v1.53.0` | 覆盖镜像 |
| `--port` | 自动（基址 9429） | HTTP 摄入宿主端口；自 `vlagent_start_port` 自动分配 |
| `--listen` | `0.0.0.0` | 绑定地址——默认开放全部网卡；`127.0.0.1` 收紧为仅回环 |
| `--remote-write-url` | **必填** | 转发的 VictoriaLogs 端点（可重复）。每个 URL **必须以 `/insert/native` 结尾** |
| `--log-path` | — | 要跟踪的宿主文件或目录（可重复）。以只读方式挂载到容器内相同绝对路径 |
| `--include` | 自动推导 | 选择文件的 glob（可重复；绝对容器路径）。默认目录为 `<dir>/**/*.log`，文件为自身 |
| `--exclude` | — | 排除文件的 glob（可重复；与 `--include` 按位置配对） |
| `--extra-arg` | — | 传给二进制的额外 CLI flag（可重复）；最后追加，故 last-wins 覆盖早期 flags |
| `--data-dir` | 自动 | 覆盖数据目录（默认在 base 目录下） |
| `--force` | false | 即使正在运行也重建——用于应用改动的端口/监听/URL/路径/glob |

数据目录存放 vlagent 的磁盘转发缓冲与文件采集**检查点**（读取位点）。它跨 `pg addon remove` 存活（用 `--clean-data` 才删除），所以重装同名实例会从采集停止的位置继续，而不是重发或跳过文件内容。镜像以 **root** 运行，无需预先 chown。

没有 `--password`：vlagent 没有本地账号概念，连接两端也都不鉴权。

## 安装

仅转发——在 `:9429` 接受 HTTP 推送并转发给本机 VictoriaLogs：

```bash
pg addon install vlagent \
  --remote-write-url http://127.0.0.1:9428/insert/native
```

跟踪一个宿主日志目录（最常见）：

```bash
pg addon install vlagent \
  --log-path /var/log/nginx \
  --remote-write-url http://127.0.0.1:9428/insert/native
```

不给 `--include` 时，该目录默认展开为 `/var/log/nginx/**/*.log`——即目录下任意深度的所有 `.log` 文件。上面的引号只是为了 shell 安全；pgcli 按字面存储 glob。

```bash
✓ vlagent installed: "va"
  Container:  pgcli-vlagent-default-va
  Image:      docker.io/victoriametrics/vlagent:v1.53.0
  Data:       /home/you/.pgcli/addon/vlagent/va/data
  Listen:     http://0.0.0.0:9429/ (HTTP ingest: jsonline, ES bulk, Loki, OTLP)
  Remote:     http://127.0.0.1:9428/insert/native
  Mount:      /var/log/nginx (dir, read-only, same path in the container)
  Include:    /var/log/nginx/**/*.log
  Checkpoint: file-checkpoints.json under /var/lib/vlagent
```

跟踪单个文件，或多个路径并显式指定 glob 与排除：

```bash
# 单个文件（默认 include 就是它自身）
pg addon install vlagent --name va-app \
  --log-path /var/log/myapp/app.log \
  --remote-write-url http://127.0.0.1:9428/insert/native

# 多个路径，每个路径各自 glob，并排除压缩归档
pg addon install vlagent --name va-all \
  --log-path /var/log/nginx --include '/var/log/nginx/*.log' --exclude '/var/log/nginx/*.gz' \
  --log-path /var/log/redis --include '/var/log/redis/**/*.log' \
  --remote-write-url http://127.0.0.1:9428/insert/native
```

每个 `--log-path` 变成一个只读 bind mount（`host:host:ro`），所以 pgcli 传给 vlagent 的 glob 就是宿主路径本身。路径必须是绝对路径且已存在。

对运行中的实例重复 `install`：配置**确实变了**（端口、监听、URL、日志路径、include/exclude、额外参数）时**重建**，没变则原样不动；`--force` 即使配置未变也重建。列表型 flag 会替换整个存储列表：要新增路径，请重传完整集合。

## 文件采集的工作方式

vlagent 的文件采集由 `-fileCollector.glob` 驱动，pgcli 的 flag 一一对应：

| pgcli 参数 | vlagent flag | 说明 |
|---|---|---|
| `--log-path` | （bind mount） | `-v <path>:<path>:ro`——容器必须能看见该文件 |
| `--include` | `-fileCollector.glob` | 可重复；必须匹配文件而非目录 |
| `--exclude` | `-fileCollector.excludeGlob` | 与第 N 个 include 按位置配对 |
| `--data-dir` | `-tmpDataPath` | 包含缓冲与 `file-checkpoints.json` |

每条采集到的日志额外带两个字段：`file`（来源路径）与 `hostname`。二者默认都是 **stream 字段**，所以每个文件在 VictoriaLogs 里是独立的 log stream——正是 LogsQL 过滤想要的效果。可用 `--extra-arg -fileCollector.streamFields=...`、`-fileCollector.fileField=...` 或 `-fileCollector.hostnameField=...` 调整。

采集器默认每 10 秒轮询新文件（`-fileCollector.refreshInterval`）。它支持 `create` 与 `nocreate` 两种日志轮转策略：轮转时先读完旧文件再切换；重启后会从保存位点继续读取轮转后的文件。检查点写在数据目录下。

### Glob 语法

这**不是正则表达式**——是 vlagent 自己的 glob 方言：

| 模式 | 匹配 |
|---|---|
| `*` | 任意字符序列，不跨路径分隔符 |
| `/**/` | 零个或多个目录（递归） |
| `?` | 任意单个字符，不跨路径分隔符 |
| `[abc]`、`[a-z]` | 字符类；前缀 `^` 或 `!` 表示取反 |
| `{a,b,c}` | 备选——匹配逗号分隔项中的任意一个 |
| `\c` | 转义字符字面量 |

示例：

```bash
--include '/var/log/nginx/*.log'              # 下一层
--include '/var/log/nginx/**/*.log'           # 任意深度
--include '/var/log/nginx/{access,error}.log' # 备选
--include '/var/log/nginx/access.log.[0-9]'   # 轮转文件
```

`*` **不**匹配隐藏文件（以 `.` 开头）；需要时请显式写出开头的点（`.*.log`）。

### Exclude 按位置配对

vlagent 把第 N 个 `--exclude` 与第 N 个 `--include` 配对，与它自己的 flag 语义一致。空字符串 `''` 是占位符，表示“这个位置不排除”：

```bash
--include '/var/log/*/*.log' --exclude '/var/log/nginx/*' \
--include '/var/log/nginx/*.log' --exclude '' \
```

第一对采集除 nginx 外的所有日志，第二对再单独采集 nginx。没有位置配对的话，nginx 的排除会把第二对也一起排除掉。

pgcli 会输出到最后一个非空 exclude 为止；末尾的空占位符直接丢弃。如果只改 `--log-path` 而不传 `--include`，会按新路径重新计算默认 glob，并裁剪超出新配对数量的已存 exclude（想覆盖就在同一次运行里显式传 `--exclude`）。

## 复制与高可用

为每个 VictoriaLogs 实例重复传 `--remote-write-url`：

```bash
pg addon install vlagent --name va-ha \
  --log-path /var/log/myapp \
  --remote-write-url http://10.0.0.1:9428/insert/native \
  --remote-write-url http://10.0.0.2:9428/insert/native
```

vlagent 把每条日志复制到所有目标，为每个 URL 维护独立磁盘缓冲，并在后端恢复后重放缓冲日志。可用 `--extra-arg -remoteWrite.maxDiskUsagePerURL=...` 限制每个缓冲的大小。

## 其他 HTTP 摄入

即使没有 `--log-path`，vlagent 也是一个有用的 HTTP 前门：它在监听端口上接受与 VictoriaLogs 相同的协议并转发，因此采集器可以推给 vlagent 而不是直连 VictoriaLogs：

| 端点 | 格式 |
|---|---|
| `/insert/jsonline` | JSON Lines（要求 `Content-Type: application/json`） |
| `/insert/elasticsearch/_bulk` | Elasticsearch / OpenSearch bulk |
| `/insert/loki/api/v1/push` | Loki JSON push |
| `/insert/opentelemetry/v1/logs` | OTLP/HTTP logs |

Kubernetes 节点日志：用 `--extra-arg -kubernetesCollector`，并通过 `--log-path` 挂入节点 `/var/log`（Docker runtime 再加 `/var/lib`）。完整协议与采集器列表见 [VictoriaLogs 日志写入一节]({{< relref "../addon/victorialogs" >}})。

## 端口

每个 vlagent 实例占**一个**端口：HTTP 摄入监听。它取自以 `vlagent_start_port`（默认 `9429`——vlagent 惯例端口）为基址的端口池，每多一个实例递增，并跳过任何已被受管容器或 `used_ports` 列表占用的端口。该池与 VictoriaLogs（`9428`）分开，所以新增 VictoriaLogs 实例绝不会抢走 vlagent 的端口。显式 `--port` 仍优先。

```bash
pg addon install vlagent --name va2 \
  --remote-write-url http://127.0.0.1:9428/insert/native   # 下一个空闲端口（9429 被占则 9430）
```

## 配置

```yaml
addons:
  vlagent:
    va:
      name: va
      image_tag: docker.io/victoriametrics/vlagent:v1.53.0
      host_port: 9429
      listen: 0.0.0.0
      remote_write_urls:
        - http://127.0.0.1:9428/insert/native
      log_paths:
        - /var/log/nginx
      includes:
        - /var/log/nginx/**/*.log
      excludes:
        - /var/log/nginx/*.gz
      extra_args:
        - -fileCollector.refreshInterval=5s
      # data_dir: /srv/vlagent-data        # 可选，覆盖默认位置
```

一切都作为 CLI flags 传给 `vlagent` 二进制：`remote_write_urls` 变成重复的 `-remoteWrite.url`，`includes` 变成 `-fileCollector.glob`，`excludes` 变成 `-fileCollector.excludeGlob`，`log_paths` 变成只读 bind mount，`data_dir` 变成 `-tmpDataPath`。

### 列出

```bash
pg addon list
```

```
Log agents (vlagent):
  vlagent (name: va)
    Status:      running
    Listen:      http://0.0.0.0:9429/
    Remote:      http://127.0.0.1:9428/insert/native
    Log paths:   /var/log/nginx
    Includes:    /var/log/nginx/**/*.log
    Excludes:    /var/log/nginx/*.gz
    Data:        /home/you/.pgcli/addon/vlagent/va/data
    Image:       docker.io/victoriametrics/vlagent:v1.53.0
    Container:   pgcli-vlagent-default-va
```

## 启停

```bash
pg addon stop vlagent
pg addon start vlagent
```

容器以 podman 的 `unless-stopped` 重启策略运行。文件读取位点会检查点写入数据目录，所以 stop/start（或宿主重启）会从原位置继续，不重复、不丢文件内容。

### 开机自启

```bash
pg autostart enable --vlagent --name va
```

这会在 `pg.yaml` 里给实例打标记，并安装/刷新一个开机服务（Linux 上是 systemd `--user` unit，macOS 上是 LaunchAgent）来执行 `pg start --autostart`。开机启动只负责把容器拉起来——带文件检查点与转发缓冲的数据目录在 install 时已创建，agent 会从采集停止的位置继续。启动顺序上 vlagent 排在 **VictoriaLogs 之后**，所以同机的 remote-write 目标已经先监听。关闭用 `pg autostart disable --vlagent --name va`；完整机制见 [开机自启]({{< relref "../autostart" >}})。

## 移除

```bash
pg addon remove vlagent              # 保留缓冲与文件检查点
pg addon remove vlagent --clean-data # 连它们一起删
```

移除 agent **不会**删除任何被跟踪的日志文件——它们以只读方式挂载，从未被修改。

## 排障

- **VictoriaLogs 里没有文件日志** —— 确认容器在运行（`pg addon list`）、glob 能匹配真实文件（`--include` 必须匹配文件而非目录）、且路径在容器内可见。`podman inspect pgcli-vlagent-<ns>-<name> --format '{{json .Config.Cmd}}'` 可查看确切 flags；`podman logs <container>` 查看采集器错误。
- **部分文件被跳过** —— `*` 不匹配隐藏文件，轮转名如 `app.log.1` 也不匹配 `*.log`。请加显式模式（如 `--include '/var/log/myapp/app.log.[0-9]'`）。exclude 按位置配对：第一对里过宽的排除会连带影响后面的配对。
- **写入的 stream 不对** —— `file` 与 `hostname` 默认是 stream 字段；用 `--extra-arg -fileCollector.streamFields=...` 覆盖。
- **额外参数没生效** —— `--extra-arg` 在容器创建时应用。重装（配置变化会自动重建）或用 `--force`；用 `podman inspect <container> --format '{{json .Config.Cmd}}'` 确认。
- **容器读不到日志（SELinux 主机）** —— pgcli 对日志路径只用 `:ro`、刻意不用 `:z`，因为给 `/var/log` 打标签会改动宿主。在 enforcing 的 SELinux 系统上请自行按工作负载打标签（如共享的 `container_log_t` 标签）或使用匹配的 security profile。
- **远程写入失败** —— URL 必须以 `/insert/native` 结尾；检查 VictoriaLogs 健康（`curl -s http://<vl>:9428/-/healthy`）以及主机间防火墙。可达后缓冲日志会自动重放。

## 已知限制

- **不支持正则表达式。** vlagent 只支持 glob（`*`、`/**/`、`?`、`[a-z]`、`{a,b}`）。复杂文件名规则需要放宽 include，再在查询侧用 LogsQL 过滤。
- 摄入监听与远程写入两侧都**没有鉴权**；用 `--listen 127.0.0.1` 或防火墙保护。
- pgcli 不校验 `--extra-arg` 的值；错误的 flag 只会在启动时从容器日志里暴露。
- macOS 代码完备但未实测；Linux 是已验证平台。
