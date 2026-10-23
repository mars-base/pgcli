# pgcli 容器镜像清单

pgcli 使用的容器镜像及其默认 tag。

## 核心组件

| 镜像 | Tag | 用途 | 配置常量 |
|------|-----|------|----------|
| `ghcr.io/mars-base/pgcli/pgcli-pg` | `18-2.58.0` | PostgreSQL 实例（单节点或 replica） | `DefaultPgImageTag` |
| `ghcr.io/mars-base/pgcli/pgcli-patroni` | `18-4.1.5` | Patroni HA 集群成员 | `DefaultPatroniImageTag` |
| `ghcr.io/mars-base/pgcli/pgcli-backup` | `2.58.0` | pgBackRest 备份仓库 | `DefaultBackupImageTag` |

## 插件 / 附加组件

| 镜像 | Tag | 用途 | 配置常量 |
|------|-----|------|----------|
| `ghcr.io/mars-base/pgcli/pgcli-minio` | `20250422221226` | MinIO S3 兼容存储 | `DefaultMinioImageTag` |
| `ghcr.io/mars-base/pgcli/pgcli-mc` | `20250813083541` | MinIO 客户端 (`mc`) | `DefaultMCImageTag` |
| `docker.io/pgsty/silo` | `RELEASE.2026-09-16T00-00-00Z` | silo（Pigsty 的 MinIO 分支）S3 存储 + `mcli` 客户端 | `DefaultSiloImageTag` |
| `ghcr.io/mars-base/pgcli/pgcli-rustfs` | `1.0.0` | rustfs（Rust 实现的 S3 存储），建立在 `docker.io/rustfs/rustfs:1.0.0` 之上的 wrapper | `DefaultRustfsImageTag` |
| `docker.io/edoburu/pgbouncer` | `v1.25.2-p0` | PostgreSQL 连接池 | `DefaultPgBouncerImageTag` |
| `docker.io/postgrest/postgrest` | `v16.3` | PostgREST（REST API over PostgreSQL） | `DefaultPostgrestImageTag` |
| `docker.io/library/haproxy` | `3.2.23-alpine` | HTTP/TCP 负载均衡 | `DefaultHAProxyImageTag` |
| `quay.io/coreos/etcd` | `v3.5.30` | 分布式键值存储（Patroni DCS） | `config.go:1953`（default 内联，`etcdctl.go:69` 亦引用） |
| `ghcr.io/pgdogdev/pgdog` | `v0.1.57` | PostgreSQL 代理 / 连接池 | `config.go:1967` |
| `docker.io/library/redis` | `7.4.11` | Redis 大版本 7（`--version 7`），纯上游镜像无 wrapper | `redisMajorImages["7"]` |
| `docker.io/library/redis` | `8.10.2` | Redis 大版本 8（默认，`--version 8`），纯上游镜像无 wrapper | `redisMajorImages["8"]` |
| `ghcr.io/mars-base/pgcli/predixy` | `7.0.1-alpine` | Predixy（Redis 集群代理），建立在上游 free edition 二进制之上的 wrapper，单架构 amd64 | `DefaultPredixyImageTag` |
| `docker.io/dpage/pgadmin4` | `9.18` | pgAdmin 4（PostgreSQL 官方 Web 管理界面），纯上游镜像无 wrapper，双架构 manifest list | `DefaultPgAdminImageTag` |
| `docker.io/library/nginx` | `1.27-alpine` | nginx HTTP 反向代理，纯上游镜像无 wrapper，双架构 manifest list | `DefaultNginxImageTag` |
| `docker.io/dxflrs/garage` | `v2.4.1` | Garage（Deuxfleurs 的 Rust S3 存储，面向自建/异地集群），纯上游镜像无 wrapper，`FROM scratch` 无 shell | `DefaultGarageImageTag` |
| `docker.io/chrislusf/seaweedfs` | `4.48_large_disk` | SeaweedFS（Go 对象存储，volume + placement-code 复制模型），纯上游镜像无 wrapper，`_large_disk` 变体 | `DefaultSeaweedfsImageTag` |
| `docker.io/grafana/grafana` | `13.2.4` | Grafana（开源可观测性面板），纯上游官方镜像无 wrapper；自 12.4.0 起 `grafana-oss` 仓库冻结，OSS 构建就发在这个仓库里；默认 Alpine base，双架构 manifest list | `config.go:578`（`DefaultGrafanaImageTag`） |
| `docker.io/prom/prometheus` | `v3.15.0` | Prometheus（指标采集后端，Grafana 的数据源），纯上游官方镜像无 wrapper；无后缀 tag 即 busybox 变体（带 shell），双架构 manifest list | `config.go:591`（`DefaultPrometheusImageTag`） |
| `docker.io/prom/alertmanager` | `v0.34.1` | Alertmanager（Prometheus 下游的告警去重/分组/路由端），纯上游官方镜像无 wrapper；无后缀 tag 即 busybox 变体（带 `amtool`），多架构 manifest list | `config.go:603`（`DefaultAlertmanagerImageTag`） |
| `docker.io/gitea/gitea` | `28.1.0` | Gitea（自托管 Git 服务），纯上游官方镜像无 wrapper；pin 的是 **rootful** 构建（`-rootless` 变体数据布局互不兼容，pgcli 永不选用）；多架构 manifest list（amd64 + arm64 + riscv64） | `config.go:619`（`DefaultGiteaImageTag`） |
| `ghcr.io/thinkparq/beegfs-mgmtd` | `8.4.0` | BeeGFS 管理/注册服务（三容器之一），官方 ThinkParQ 镜像无 wrapper；root 运行、host 网络 | `BeeGFSImageRepo` + `DefaultBeeGFSVersion`（`config.go:629/635`） |
| `ghcr.io/thinkparq/beegfs-meta` | `8.4.0` | BeeGFS 元数据服务（三容器之二），固定监听 8005 | 同上 |
| `ghcr.io/thinkparq/beegfs-storage` | `8.4.0` | BeeGFS 数据目标（三容器之三），固定监听 8003 | 同上 |
| `docker.io/victoriametrics/victoria-metrics` | `v1.154.1` | VictoriaMetrics（高性能 Prometheus 兼容时序数据库），纯上游官方镜像无 wrapper；单架构 amd64，以 root 运行 | `DefaultVictoriaMetricsImageTag` |
| `docker.io/victoriametrics/vminsert` | `v1.154.1-cluster` | VMCluster 摄入端（集群版三组件之一），纯上游官方镜像无 wrapper；单架构 amd64，以 root 运行 | `DefaultVMInsertImageTag` |
| `docker.io/victoriametrics/vmselect` | `v1.154.1-cluster` | VMCluster 查询端（集群版三组件之二），纯上游官方镜像无 wrapper；单架构 amd64，以 root 运行 | `DefaultVMSelectImageTag` |
| `docker.io/victoriametrics/vmstorage` | `v1.154.1-cluster` | VMCluster 数据存储（集群版三组件之三），纯上游官方镜像无 wrapper；单架构 amd64，以 root 运行 | `DefaultVMStorageImageTag` |
| `docker.io/victoriametrics/victoria-logs` | `v1.53.0` | VictoriaLogs（VictoriaMetrics 家族的日志数据库），纯上游官方镜像无 wrapper；多架构，以 root 运行 | `DefaultVictoriaLogsImageTag` |
| `docker.io/victoriametrics/vlagent` | `v1.53.0` | vlagent（VictoriaLogs 官方日志 agent），纯上游官方镜像无 wrapper；多架构，以 root 运行；文件采集由 pgcli 的 `--log-path`/`--include`/`--exclude` 接线 | `DefaultVlagentImageTag` |

## 镜像导出

**推荐一步式**：`make container-images-export` 把下面全部清单镜像（amd64）
`podman pull --platform linux/amd64` + `podman save` 到项目内 `images/` 目录
（已 gitignore，tar 不进 git），供离线 VM 同步 load。手工等价命令如下。

```bash
# 导出到指定目录（默认约定为项目 images/，已在 .gitignore）
TARGET=./images
mkdir -p "$TARGET"

podman save -o "$TARGET/pgcli-pg_18-2.58.0.tar" \
  ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0

podman save -o "$TARGET/pgcli-patroni_18-4.1.5.tar" \
  ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5

podman save -o "$TARGET/pgcli-backup_2.58.0.tar" \
  ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0

podman save -o "$TARGET/pgcli-minio_20250422221226.tar" \
  ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226

podman save -o "$TARGET/pgcli-mc_20250813083541.tar" \
  ghcr.io/mars-base/pgcli/pgcli-mc:20250813083541

podman save -o "$TARGET/silo_RELEASE.2026-09-16T00-00-00Z.tar" \
  docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z

podman save -o "$TARGET/postgrest_v16.3.tar" \
  docker.io/postgrest/postgrest:v16.3

podman save -o "$TARGET/pgbouncer_v1.25.2-p0.tar" \
  docker.io/edoburu/pgbouncer:v1.25.2-p0

podman save -o "$TARGET/haproxy_3.2.23-alpine.tar" \
  docker.io/library/haproxy:3.2.23-alpine

podman save -o "$TARGET/etcd_v3.5.30.tar" \
  quay.io/coreos/etcd:v3.5.30

podman save -o "$TARGET/pgdog_v0.1.57.tar" \
  ghcr.io/pgdogdev/pgdog:v0.1.57

# Redis 上游镜像与 rustfs 一样是双架构 manifest list，
# 先按目标平台 pull 成单架构再 save（见下面的 rustfs 小节）
podman pull --platform linux/amd64 docker.io/library/redis:7.4.11
podman save -o "$TARGET/redis_7.4.11.tar" docker.io/library/redis:7.4.11
podman pull --platform linux/amd64 docker.io/library/redis:8.10.2
podman save -o "$TARGET/redis_8.10.2.tar" docker.io/library/redis:8.10.2

# predixy 镜像本身就是单架构 amd64（上游只发 amd64 二进制），直接 save
podman pull ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine
podman save -o "$TARGET/predixy_7.0.1-alpine.tar" \
  ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine

# pgAdmin 上游镜像与 redis 一样是双架构 manifest list，先按 amd64 pull 再 save
podman pull --platform linux/amd64 docker.io/dpage/pgadmin4:9.18
podman save -o "$TARGET/pgadmin4_9.18.tar" docker.io/dpage/pgadmin4:9.18

# nginx 上游镜像与 redis 一样是双架构 manifest list，先按 amd64 pull 再 save
podman pull --platform linux/amd64 docker.io/library/nginx:1.27-alpine
podman save -o "$TARGET/nginx_1.27-alpine.tar" docker.io/library/nginx:1.27-alpine

# grafana 上游镜像同样是双架构 manifest list，先按 amd64 pull 再 save
podman pull --platform linux/amd64 docker.io/grafana/grafana:13.2.4
podman save -o "$TARGET/grafana_13.2.4.tar" docker.io/grafana/grafana:13.2.4

# prometheus 上游镜像同样是双架构 manifest list，先按 amd64 pull 再 save
podman pull --platform linux/amd64 docker.io/prom/prometheus:v3.15.0
podman save -o "$TARGET/prometheus_v3.15.0.tar" docker.io/prom/prometheus:v3.15.0

# alertmanager 上游镜像同样是多架构 manifest list，先按 amd64 pull 再 save
podman pull --platform linux/amd64 docker.io/prom/alertmanager:v0.34.1
podman save -o "$TARGET/alertmanager_v0.34.1.tar" docker.io/prom/alertmanager:v0.34.1

# gitea 上游镜像同样是多架构 manifest list，先按 amd64 pull 再 save
podman pull --platform linux/amd64 docker.io/gitea/gitea:28.1.0
podman save -o "$TARGET/gitea_28.1.0.tar" docker.io/gitea/gitea:28.1.0

# beegfs 三个服务端镜像（单文件系统 = mgmtd + meta + storage 三容器）
for svc in mgmtd meta storage; do
  podman pull --platform linux/amd64 ghcr.io/thinkparq/beegfs-${svc}:8.4.0
  podman save -o "$TARGET/beegfs-${svc}_8.4.0.tar" ghcr.io/thinkparq/beegfs-${svc}:8.4.0
done

# victoriametrics 上游镜像是单架构 amd64（root 运行，无 uid chown），直接 save
podman pull --platform linux/amd64 docker.io/victoriametrics/victoria-metrics:v1.154.1
podman save -o "$TARGET/victoriametrics_v1.154.1.tar" \
  docker.io/victoriametrics/victoria-metrics:v1.154.1

# vmcluster 三个集群版镜像（单架构 amd64，root 运行），直接 save
for svc in vminsert vmselect vmstorage; do
  podman pull --platform linux/amd64 docker.io/victoriametrics/${svc}:v1.154.1-cluster
  podman save -o "$TARGET/${svc}_v1.154.1-cluster.tar" \
    docker.io/victoriametrics/${svc}:v1.154.1-cluster
done

# victorialogs 上游镜像（多架构，root 运行）；离线导出只要 amd64
podman pull --platform linux/amd64 docker.io/victoriametrics/victoria-logs:v1.53.0
podman save -o "$TARGET/victoria-logs_v1.53.0.tar" \
  docker.io/victoriametrics/victoria-logs:v1.53.0

# vlagent 上游镜像（多架构，root 运行）；离线导出只要 amd64
podman pull --platform linux/amd64 docker.io/victoriametrics/vlagent:v1.53.0
podman save -o "$TARGET/vlagent_v1.53.0.tar" \
  docker.io/victoriametrics/vlagent:v1.53.0
```

### rustfs（双架构 manifest 不能直接 save）

`pgcli-rustfs:1.0.0` 在 ghcr 上是一个 **manifest list（amd64 + arm64）**，而
`podman save` 的 `docker-archive` 格式不接受 manifest list。离线导出只需要目标
主机那一个架构。`podman save` 写进 tar 的 RepoTags 就是所传的引用名，所以要
把**单架构镜像 retag 成规范名**再保存，目标主机 load 后才直接得到
`ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0`：

```bash
# 按平台 pull：本地 :1.0.0 变为单架构（amd64）镜像，不再是 manifest
podman pull --platform linux/amd64 ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
podman save --format docker-archive \
  -o "$TARGET/pgcli-rustfs_1.0.0.tar" \
  ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
```

若目标主机是 arm64，把 `linux/amd64` 换成 `linux/arm64`。若本机刚跑过
`make container-build-rustfs`（本地 `:1.0.0` 是双架构 manifest），等价做法是
把其产出的单架构 tag 覆盖到规范名再 save——`podman tag …:1.0.0-amd64
…:1.0.0 && podman save …:1.0.0`，之后用 `make container-build-rustfs` 恢复
本地 manifest。（`make container-images-export` 已内置「先按 amd64 pull 再
save」，对 rustfs/redis/pgadmin4 均自动处理。）

## 镜像加载（离线环境）

在目标主机上加载导出的镜像：

```bash
for img in /path/to/images/*.tar; do
  podman load -i "$img"
done
```

## 版本说明

- **pgcli-pg** / **pgcli-patroni**：PostgreSQL 18 + pgBackRest 2.58.0
- **pgcli-backup**：独立备份容器（pgBackRest 2.58.0）
- **pgcli-minio** / **pgcli-mc**：MinIO 固定版本，避免上游不兼容变更
- **silo**：Pigsty 官方发布的公开镜像（pull-only），版本随 Pigsty 发布节奏
- **pgcli-rustfs**：建立在 `docker.io/rustfs/rustfs:1.0.0`（首个 GA）之上的
  pgcli wrapper（pull-only），tag 跟随被 pin 的上游版本；ghcr 上为双架构
  manifest list
- **redis**：纯上游 `docker.io/library/redis`，无 wrapper；两个大版本各自
  pin 一个补丁 tag（`redisMajorImages` 映射表，默认大版本 8），tag 跟随各大
  版本的最新补丁、与 `internal/config/config.go` 的映射表一起升级
- **predixy**：pgcli 自建 wrapper（`ghcr.io/mars-base/pgcli/predixy`），把上游
  joyieldInc/predixy 的 **free edition** amd64 glibc 二进制放到 alpine:3.22 +
  gcompat + libgcc 上跑；上游只发 amd64 二进制，所以这是**单架构镜像**（不是
  manifest list，可直接 `podman save`）。`make container-build-predixy` 在构建
  前下载 release 包、并用当前的 `license2026.conf` 覆盖包内那份（自带 license
  已过期，free 二进制拒绝启动），因此**镜像内置 license 的有效期随构建时间滚动**
  （现镜像为 `ClientLimit 128`、2026-12-31 到期）；pgcli 本身不分发也不管理
  license，到期就重新 build + push
- **pgadmin4**：纯上游 `docker.io/dpage/pgadmin4`，无 wrapper（镜像自己的
  entrypoint 把 `/var/lib/pgadmin` chown 给 uid 5050 后降权，所以 pgcli 不需要
  rustfs 那套属主机制）；pin 到发布时的 `latest`（当前 `9.18`，双架构 manifest
  list，amd64 + arm64——Apple Silicon 的 podman machine 可直接跑）；`9` 这个
  major tag 与 `snapshot`（nightly）都存在，pgcli 一律不用不稳定的滚动 tag
- **nginx**：纯上游 `docker.io/library/nginx`，无 wrapper；基于 Alpine Linux 减小体积；
  pin 到 `1.27-alpine`（双架构 manifest list，amd64 + arm64——Apple Silicon 的
  podman machine 可直接跑）
- **garage**：纯上游 `docker.io/dxflrs/garage`，无 wrapper（不同于 rustfs——
  Garage 容器内以 root 跑，bind mount 属主天然可写，不需要 rustfs 那套
  uid-10001 属主机制）；pin 到 stable 线 `v2.4.1`；镜像 `FROM scratch`、无
  shell，`/garage` 二进制既是 daemon 也是控制 CLI；配置是渲染的 TOML
  文件而非 env，只提供明文 HTTP 无原生 TLS（需要 HTTPS 用 nginx 前置）
- **seaweedfs**：纯上游 `docker.io/chrislusf/seaweedfs`，无 wrapper；pin 到
  `4.48_large_disk`——`_large_disk` 变体以 `-tags large_disk` 构建（更高的
  每 volume fd/inode 上限）；Alpine base、以 root 跑，`/usr/bin/weed` 既是
  daemon（mini/server）也是控制 CLI（shell），自带原生 S3 TLS
- **grafana**：纯上游官方 `docker.io/grafana/grafana`，无 wrapper。注意自
  release 12.4.0 起 `grafana/grafana-oss` 仓库**已冻结**——OSS 构建就发布在
  `grafana/grafana` 里（两仓库镜像内容相同），pgcli 拉的就是它；无后缀 tag 即
  Alpine base（官方推荐变体），`-ubuntu`/`-distroless`/`-slim` 变体存在但 pgcli
  从不选用；pin 到 stable 线 `13.2.4`（2026-10-13 在 Docker Hub 核实），双架构
  manifest list。与 pgadmin 不同：镜像固化 `USER 472`（root 组 gid 0）、
  `run.sh` 既不 chown 也不降权，所以 pgcli 在创建容器前于宿主 chown 数据目录属主
  （rootless 下经 `podman unshare chown 472:0`，rootful+root 下直接 `chown`）
- **prometheus**：纯上游官方 `docker.io/prom/prometheus`，无 wrapper。无后缀 tag
  即 **busybox 变体**（镜像内带 shell，官方推荐用于普通部署；另有
  `-alpine`/`-ubuntu`/`glibc` 变体，pgcli 从不选用）；pin 到 stable 线
  `v3.15.0`（2026-10-14 在 Docker Hub 核实，`latest` 即指向此 tag），双架构
  manifest list。与 Grafana 同构的属主形态：镜像固化 `USER nobody`
  （uid/gid `65534`），entrypoint 不做任何属主管理，所以 pgcli 同样在创建容器前
  于宿主 chown 数据目录（rootless 下经 `podman unshare chown 65534:65534`，
  rootful+root 下直接 `chown`）。与 Grafana 不同的是配置方式：靠一份 bind mount
  的 `prometheus.yml`（渲染或从 `--conf-file` 拷贝）加镜像名之后的 CLI flag，而不
  是 env；且**完全没有鉴权**——`0.0.0.0` 默认背后没有密码兜底，暴露面只靠网络边界
- **alertmanager**：纯上游官方 `docker.io/prom/alertmanager`，无 wrapper，是上面
  prometheus 插件的下游同构体。无后缀 tag 即 **busybox 变体**（镜像内带 shell 与
  `amtool`；另有 `-ubuntu`/`docker-alpine` 变体，pgcli 从不选用）；pin 到 stable 线
  `v0.34.1`（多架构 manifest list）。属主形态与 prometheus/grafana 一致：固化
  `USER nobody`（uid/gid `65534`）、entrypoint 不做属主管理，pgcli 创建容器前在宿主
  chown 数据目录。两处差异：镜像名之后固定传 `--cluster.listen-address=`（空串）以彻底
  关闭单实例永不用、且默认会占 TCP+UDP `9094` 的 HA gossip；且无鉴权的 `POST /-/reload`
  **在镜像里关不掉**（不像 prometheus 的 `--web.enable-lifecycle` 可不设即隐去）——
  pgcli 走 SIGHUP、不依赖该端点，但路由照样敞开，故暴露面比 prometheus 更危险（可达即
  可创建 silence 静音 oncall）
- **gitea**：纯上游官方 `docker.io/gitea/gitea`，无 wrapper。注意版本线：Gitea 于
  2026 年底把历史 `1.` 前缀去掉，原 1.28.0 即 `28.0.0`，pin 的是当前 stable
  `28.1.0`（2026-10-15 在 Docker Hub 核实），多架构 manifest list（amd64 + arm64 +
  riscv64）。镜像有 **rootful / rootless 两套构建**，二者数据布局互不兼容、选定后不可
  切换（`-rootless` 是 tag 后缀）；pgcli 一律 pin rootful（无后缀）并预 chown 宿主数据
  目录。rootful 构建以 `git` 用户跑，默认 uid/gid **1000**（经 `USER_UID`/`USER_GID`
  重新传入以钉死，宿主预 chown 无从漂移）——与 grafana 的 uid 472、prometheus 的
  65534 同属"镜像固化用户、entrypoint 不修复既有挂载属主"的形态，所以 pgcli 在创建容器
  前于宿主 chown（rootless 下 `podman unshare chown 1000:1000`，rootful+root 下直接
  `chown`）。配置只经 env（`GITEA__*` 每次启动折叠进持久化的 app.ini），且
  `SECRET_KEY`/`INTERNAL_TOKEN` 刻意**不注入**（否则重装会静默重设幸存数据库的密钥）
- **beegfs**：纯上游官方 ThinkParQ 镜像（`ghcr.io/thinkparq/beegfs-{mgmtd,meta,storage}`），
  无 wrapper，三个镜像共用版本 pin `8.4.0`（`BeeGFSImageRepo` + `DefaultBeeGFSVersion`
  拼出引用名，一个"文件系统"= 三容器）。与仓库里所有其他 addon 不同的两点：镜像内以
  **root** 运行（宿主数据目录无需预 chown）；以 **host 网络**运行（BeeGFS 是自己的
  TCP 协议、非 HTTP，不存在容器端口映射可分配——故没有端口池，meta/storage 在 8.4
  根本没有可移动的监听口，每宿主仅一个文件系统）。没有 client 镜像：客户端是内核模块
  （DKMS），无法容器化，pgcli 不安装
- **juicefs** / **zerofs**：**没有镜像**——这两个是宿主二进制 addon，不走本清单。
  `pg addon install` 从各自 GitHub releases 下载官方 tarball（sha256 pin 在
  `internal/config/config.go`：juicefs `DefaultJuicefsVersion` + `JuicefsSHA256`，
  zerofs `DefaultZerofsVersion` + `ZerofsTarballSHA256`），落到宿主 `PATH`（默认
  `/usr/local/bin`），`PATH` 上已有同名二进制则直接复用。离线环境无需预置任何
  tar——把二进制预先放到目标主机的 `PATH` 上即可绕过下载
- **victoriametrics**：纯上游官方 `docker.io/victoriametrics/victoria-metrics`，
  无 wrapper。镜像以 **root** 运行（与 prometheus/grafana 的 uid 65534/472 不同，
  无需宿主预 chown 数据目录）；单架构 amd64（上游只发 amd64 二进制）。pin 到
  stable 线 `v1.154.1`（2026-10-19 发布的最新稳定版）。与 Prometheus 一样**完全没有
  鉴权**——`0.0.0.0` 默认背后没有密码兜底，暴露面只靠网络边界；不同的是**完全不需要
  配置文件**——一切都作为镜像名之后的 CLI flag 传入（`-storageDataPath` /
  `-httpListenAddr` / `-retentionPeriod` / `--extra-arg` 自定义 flags），故
  没有 SIGHUP reload，所有变更需重建容器
- **vmcluster**：纯上游官方 `docker.io/victoriametrics/{vminsert,vmselect,vmstorage}`，
  无 wrapper，三个镜像共用版本 pin `v1.154.1-cluster`（`-cluster` 后缀区分于单机版）。
  与单机版相同的运维形态：以 **root** 运行（宿主数据目录无需预 chown）、**完全不需要
  配置文件**（一切经 CLI flag）、**完全没有鉴权**。三个组件各跑一个容器——vminsert
  （摄入端，按 metric hash 分片写入所有 vmstorage）、vmselect（查询端，从所有
  vmstorage 合并结果）、vmstorage（数据存储，可多节点分片+副本）。只有 vmstorage 有
  持久数据目录；vminsert/vmselect 是 stateless。支持跨主机部署（`--roles` 选组件子集、
  `--storage-node` 列出所有 vmstorage 地址）和水平扩展（多 vmstorage 节点做数据分片，
  `--replication-factor` 做写入副本）
- **victorialogs**：纯上游官方 `docker.io/victoriametrics/victoria-logs`，无 wrapper。
  VictoriaMetrics 家族的**日志**数据库，运维形态与单机版 victoriametrics 完全一致：
  以 **root** 运行（宿主数据目录无需预 chown）、**完全不需要配置文件**（一切经 CLI
  flag：`-storageDataPath` / `-httpListenAddr` / `-retentionPeriod`）、**完全没有鉴权**
  （`0.0.0.0` 默认背后没有密码兜底，暴露面只靠网络边界）。单容器单 HTTP 口（默认
  9428）同时提供摄入、LogsQL 查询与内置 UI。与 victoriametrics 的差异只在 retention：
  victoriametrics 的 `-retentionPeriod` 按**月**计且 pgcli 默认 `"1"`；victoria-logs
  接受完整时长语法（`30d`、`3months`、`1y`），pgcli **不设默认**——`--retention` 省略
  时整个 flag 不下发，让镜像自身的默认生效。上游镜像为多架构（amd64/arm/arm64/ppc64le）。
  **vlcluster** 插件复用**同一镜像**跑三个角色容器（`vlinsert` / `vlselect` /
  `vlstorage`，仅靠 flag 区分），无需额外镜像
- **vlagent**：纯上游官方 `docker.io/victoriametrics/vlagent`，无 wrapper。VictoriaLogs
  的官方日志 agent（`vmagent` 的日志对应物），与 victoria-logs 同一发布节奏、同一版本号。
  以 **root** 运行、**完全不需要配置文件**（一切经 CLI flag）、**完全没有鉴权**。pgcli
  在其上接线文件采集：每个 `--log-path` 只读挂载到容器内相同绝对路径，`--include`/
  `--exclude` 用 vlagent 原生 glob（不是正则）选文件，`--remote-write-url`（必填、以
  `/insert/native` 结尾）指向 VictoriaLogs，可重复以做复制。单 HTTP 端口（默认 9429）。
  上游镜像为多架构（amd64/arm64/ppc64le 等）
- **pgbouncer** / **postgrest** / **haproxy** / **etcd** / **pgdog**：上游官方或社区维护的稳定版本

---

**最后更新**：2026-10-23（新增 vlagent 镜像；同日新增 victorialogs 镜像；此前新增 vmcluster 三镜像于 2026-10-22；
此前新增 victoriametrics 镜像；
此前新增 beegfs 三镜像于 2026-10-19，gitea 于 2026-10-16
合入时补录，alertmanager 于 2026-10-15
合入时补录，grafana 与 prometheus 于 2026-10-14 合入时补录，再往前 garage 与
seaweedfs 分别于 2026-10-12 / 2026-10-14 合入时补录）
