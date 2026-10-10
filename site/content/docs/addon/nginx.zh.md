---
title: "Nginx"
description: "以 pgcli 插件方式运行 nginx——Web 服务（pgAdmin、PostgREST 等）前的 HTTP 反向代理，支持基于路径的路由和可选 TLS 终止"
weight: 53
---

[nginx](https://nginx.org) 是 HTTP 反向代理，用于前置多个 Web 服务——pgAdmin、
PostgREST 甚至 HAProxy stats——通过基于路径的路由和可选 TLS 终止。pgcli 将其
作为**独立的顶层插件**运行，CLI 表面与其他 web/infra 插件一致（install / start
/ stop / logs / autostart / remove），全部通过 `pg.yaml` 管理。

它是插件族里的 HTTP *网关*，这一点决定了它的设计：

- **反向代理，不是负载均衡器。** 与 [HAProxy](../haproxy/)——前置 Patroni 的 TCP
  负载均衡器——不同，nginx 是 HTTP 反向代理，按 URL 路径路由。每个 `location` 块
  转发到一个 upstream（pgAdmin、PostgREST 等），客户端通过单一端口访问多个服务。
- **文件驱动配置。** pgcli 根据你声明的 backends 渲染 `nginx.conf`（或通过
  `--conf-file` 接受自定义文件），以只读方式挂载到官方 `nginx:1.27-alpine`
  镜像中。TLS 可选：未提供 BYO 证书时，pgcli 通过 `tlsca` 生成自签名叶子证书。
- **独立日志挂载。** 每个 backend 有自己的 access 和 error 日志文件
  （`access_<name>.log`、`error_<name>.log`），位于挂载的 `/var/log/nginx/`
  目录下，因此无需解析共享日志即可观察每个服务的流量。
- **nginx 专属子命令。** 除了通用插件生命周期（install/start/stop/remove），
  nginx 还暴露 `test`（通过临时容器运行 `nginx -t` 验证配置语法）、`reload`
  （通过 `nginx -s reload` 热加载）和 `exec`（在容器内运行任意命令）。

> **平台支持：** nginx 在 **Linux**（主机网络）和 **macOS**（pgcli-net 桥接网络
> + 端口发布）上均可工作。上游镜像为多架构（amd64 + arm64），任何主机架构均可。

## 工作原理

路由基于路径：每个 `location` 块转发到一个 upstream。配置根据你声明的 backends
渲染（或从自定义文件复制），以只读方式挂载，nginx 在前台运行
`nginx -g 'daemon off;'`。

### 两种配置模式

通过 `--conf-file`（自定义）或 `--upstream`（模板）选择：

- **模板模式**（默认）——pgcli 根据 `--upstream` 规格、`--pgadmin-name` 和
  `--postgrest-name` 标志渲染 `nginx.conf`。每个 upstream 成为 `upstream` 块和
  `location` 块；TLS 在 HTTP 服务器块旁添加 HTTPS 服务器块。模板覆盖常见场景：
  基于路径的路由到若干 Web 服务。
- **自定义模式**（`--conf-file`）——你提供完整 `nginx.conf` 路径；pgcli 复制到
  插件目录，仅管理容器生命周期。当生产配置超出模板能力时使用（`limit_req_zone`、
  `resolver` + 变量 `proxy_pass`、WebSocket、SNI、每个 backend 多个 location、
  upstream keepalive 等）。`--upstream` 和 `--conf-file` 互斥。

### 按 backend 独立日志

每个 backend 在挂载的 `/var/log/nginx/` 目录下有自己的日志文件：

- `access_<name>.log` —— 该 backend 的 `location` 块的 access 日志
- `error_<name>.log` —— 该 backend 的 `location` 块的 error 日志

全局 `access.log` 和 `error.log` 捕获服务器级事件。日志目录在主机上创建于
`<base-dir>/addon/nginx/<name>/log/`，跨容器重启持久化。

### TLS 终止

TLS 可选。未提供 `--tls-cert`/`--tls-key` 时，pgcli 通过 `tlsca.Generate` 在
`<base-dir>/addon/nginx/<name>/tls/` 下生成自签名叶子证书。HTTPS 监听器与 HTTP
监听器并行运行，现有明文客户端继续工作。提供 BYO 证书时，改为挂载指定文件。

## 安装

backends 通过三种方式之一提供（互斥）：

- **`--upstream name=<n>,path=<p>,backend=<h:p>`**（可重复）——显式 upstream 目标；
- **`--pgadmin-name <name>`** / **`--postgrest-name <name>`** —— 便利标志，自动
  解析指定插件的端点并分别添加到 `/admin` 或 `/api`；
- **`--conf-file <path>`** —— 自定义 `nginx.conf` 文件（完全跳过模板渲染）。

每个 `--upstream` 字段的含义——以
`--upstream name=pgadmin,path=/admin,backend=127.0.0.1:5050` 为例：

| 字段 | 含义 |
|------|------|
| `name` | `nginx.conf` 中的 upstream 名称（省略时默认为清理后的 `path`） |
| `path` | location 路径前缀（例如 `/admin` → pgAdmin、`/api` → PostgREST；默认为 `/`） |
| `backend` | 要代理到的 host:port（例如 `127.0.0.1:5050`） |

```bash
# 模板模式：一个 upstream，显式指定
pg addon install nginx --name proxy \
  --upstream name=pgadmin,path=/admin,backend=127.0.0.1:5050

# 模板模式：便利标志自动解析 pgAdmin 和 PostgREST 端点
pg addon install nginx --name proxy \
  --pgadmin-name admin --postgrest-name api

# 模板模式 + TLS（自签名证书）
pg addon install nginx --name proxy \
  --pgadmin-name admin --tls

# 自定义模式：你提供完整配置
pg addon install nginx --name custom \
  --conf-file /path/to/nginx.conf --http-port 8080
```

安装摘要报告分配的端口和配置路径：

```
✓ nginx installed: "proxy"
  Container:  pgcli-nginx-default-proxy
  Image:      docker.io/library/nginx:1.27-alpine
  HTTP:       127.0.0.1:8080
  HTTPS:      (disabled)
  Backends:   2
    - pgadmin    /admin → 127.0.0.1:5050
    - postgrest  /api   → 127.0.0.1:3000
  Config:     ~/pg/addon/nginx/proxy/nginx.conf
  Logs:       ~/pg/addon/nginx/proxy/log/
```

使用 `--conf-file` 时，摘要显示 `Config: <path> (from <confFile>)` 并跳过
`Backends:` 部分（pgcli 不解析用户的配置）。

### 用 `--force` 重建

不带 `--force` 的 `pg addon install nginx` 对运行中的容器是空操作——它不动现有
配置，只确认容器在跑。当你*正要*修改某些东西（端口、listen、backends、TLS、
`--conf-file`、`worker_connections`）时，这恰好是错误的行为：运行中的容器钉在
创建它时的配置上，编辑看似被接受，但直到下次重建前什么都不会改变。

`--force` 就是应用这些改动的旋钮。它停止运行中的容器、移除它，并按当前配置创建
一个新的：

```bash
# 把绑定地址改成暴露到网络
pg addon install nginx --listen 0.0.0.0 --force

# 添加新 upstream
pg addon install nginx --pgadmin-name admin --postgrest-name api --force

# 切换到自定义配置
pg addon install nginx --conf-file /etc/nginx/custom.conf --force

# 切到另一个上游镜像标签
pg addon install nginx --image docker.io/library/nginx:1.28-alpine --force
```

## nginx 子命令

除了通用插件生命周期（install/start/stop/remove），nginx 还暴露三个日常运维子
命令：

### test —— 验证配置语法

```bash
pg addon nginx test --name proxy
```

在**临时容器**（`podman run --rm`）内运行 `nginx -t`，挂载与真实容器相同的卷。
**不**需要 nginx 容器在运行中——它启动一次性容器、验证配置、退出。这是在 reload
前测试配置改动的安全方式：

```bash
# 编辑配置文件（模板或自定义）
vim ~/pg/addon/nginx/proxy/nginx.conf

# 验证语法，不影响运行中的代理
pg addon nginx test --name proxy
# nginx: the configuration file /etc/nginx/nginx.conf syntax is ok
# nginx: configuration file /etc/nginx/nginx.conf test is successful

# 然后才 reload
pg addon nginx reload --name proxy
```

临时容器挂载相同的 `nginx.conf`（以及启用 TLS 时的证书目录），因此验证与真实
容器启动时看到的完全一致。

### reload —— 热加载配置

```bash
pg addon nginx reload --name proxy
```

向运行中的容器发送 `nginx -s reload`——优雅重启，采纳配置改动而不丢弃进行中的
连接。需要容器在运行中（若已停止，先用 `pg addon start nginx --name proxy`）。

### exec —— 在容器内运行命令

```bash
pg addon nginx exec --name proxy -- cat /etc/nginx/nginx.conf
pg addon nginx exec --name proxy -- nginx -V
pg addon nginx exec --name proxy -- ls -la /var/log/nginx/
```

在 nginx 容器内运行任意命令，stdio 透传。需要容器在运行中。`--` 分隔符是必需的
——之后的所有内容原样传给容器。

## 参数

单容器直接跑上游 `docker.io/library/nginx` 镜像（pull-only——pgcli 从不构建它），
全部通过渲染的（或自定义的）`nginx.conf` 和 `podman run` 标志配置。下表列出每个
旋钮及其默认值：

| 参数 / `pg.yaml` 键 | 默认值 | 含义 |
|----------------------|---------|---------|
| `--image` / `image_tag` | `docker.io/library/nginx:1.27-alpine` | 直接指定镜像标签 |
| `--http-port` / `http_port` | 自动，取自 `nginx_start_port`（基址 **8080**） | HTTP 监听器宿主端口 |
| `--https-port` / `https_port` | 自动（HTTP 之后下一个空闲端口） | HTTPS 监听器宿主端口（仅 TLS） |
| `--listen` / `listen` | `127.0.0.1` | 绑定地址（`0.0.0.0` 对外开放） |
| `--worker-connections` / `worker_connections` | `1024` | `events` 块中的 `worker_connections`（每个 worker 的最大并发连接数） |
| `--tls` / `tls` | `false` | 在 HTTP 监听器旁启用 HTTPS 监听器 |
| `--tls-cert` / `tls_cert` | 不设 | BYO 证书路径（PEM 叶子 + 链）；不提供时 pgcli 生成自签名证书 |
| `--tls-key` / `tls_key` | 不设 | BYO 私钥路径（PEM） |
| `--upstream` | 不设（可重复） | `name=<n>,path=<p>,backend=<h:p>` —— 一个 upstream 目标 |
| `--pgadmin-name` | 不设 | 便利：自动把指定 pgAdmin 插件添加到 `/admin` |
| `--postgrest-name` | 不设 | 便利：自动把指定 PostgREST 插件添加到 `/api` |
| `--conf-file` / `conf_file` | 不设 | 自定义 `nginx.conf` 路径（与 `--upstream`/`--pgadmin-name`/`--postgrest-name` 互斥） |
| — | `autostart: false` | 开机自启（`pg autostart enable --nginx`） |

旋钮间的配合关系：

- **`--conf-file` 与 `--upstream`/`--pgadmin-name`/`--postgrest-name` 互斥。**
  二者都表示"这是配置"——一个给路径，一个声明 backends。安装拒绝组合。
- **`--tls` 不提供 `--tls-cert`/`--tls-key` 时生成自签名证书。** 证书位于
  `<base-dir>/addon/nginx/<name>/tls/`，挂载到 `/etc/nginx/certs`。HTTPS
  监听器在下一个空闲端口上与 HTTP 并行运行。
- **`--tls-cert`/`--tls-key` 不提供 `--tls` 时被忽略。** 这些标志仅在启用 TLS
  时生效。
- **`--worker-connections` 在 `--conf-file` 模式下被忽略。** 自定义配置拥有
  `events` 块；pgcli 不修改它。
- **`--listen` 默认 loopback** —— 代理保持 `127.0.0.1`，除非传
  `--listen 0.0.0.0` 从别的主机访问。Linux 上容器共享主机网络，所以
  `0.0.0.0` 在 `<host-ip>:<port>` 应答；macOS 上桥接发布端口，
  `proxyBindHost` 把 loopback 放宽到 `0.0.0.0`。

## 端口

每个实例从 nginx 自己的端口池 `nginx_start_port`（默认 **8080**）取**一到两个
端口**——与 pgAdmin、Redis、Predixy、对象存储的池各自独立游标，所以一个 nginx、
一个 pgadmin、一个 redis、一个 minio 实例永不撞车：

```bash
pg addon install nginx --name proxy                          # 8080（仅 HTTP）
pg addon install nginx --name proxy-tls --tls                # 8081（HTTP）+ 8082（HTTPS）
pg addon install nginx --name proxy2                         # 8083
```

`--http-port` 和 `--https-port` 可把实例钉在特定端口；自动分配会跳过任何已显式
指定或宿主上已占用的端口。TLS 实例消耗两个端口（HTTP + HTTPS）；普通实例消耗
一个。

## 配置

实例位于 `pg.yaml` 的顶层 `addons.nginx` map：

```yaml
namespace: default
nginx_start_port: 8080     # nginx 自己的端口池
addons:
  nginx:
    proxy:
      container_name: pgcli-nginx-default-proxy
      name: proxy
      image_tag: docker.io/library/nginx:1.27-alpine
      listen: 127.0.0.1
      http_port: 8080
      https_port: 0        # 0 = 自动分配（仅 TLS）
      worker_connections: 1024
      tls: false
      # tls_cert: /path/to/cert.pem   # BYO 证书（需 tls: true）
      # tls_key:  /path/to/key.pem
      # conf_file: /etc/nginx/custom.conf  # 自定义模式（跳过 backends）
      autostart: false     # pg autostart enable --nginx --name proxy
      backends:
        - { name: pgadmin,   path: /admin, backend: "127.0.0.1:5050" }
        - { name: postgrest, path: /api,   backend: "127.0.0.1:3000" }
```

对 `listen`、`http_port`、`https_port`、`worker_connections`、`tls`、
`tls_cert`/`tls_key`、`conf_file`、`backends` 的修改，在下一次
`pg addon install nginx --name proxy --force`（或对应 flags）后生效。

### 列出

```bash
pg addon list
```

```
Web add-ons (nginx):
  nginx (name: proxy)
    Status:      running
    HTTP:        127.0.0.1:8080
    HTTPS:       (disabled)
    Backends:    2
      - pgadmin    /admin → 127.0.0.1:5050
      - postgrest  /api   → 127.0.0.1:3000
    Config:      ~/pg/addon/nginx/proxy/nginx.conf
    Logs:        ~/pg/addon/nginx/proxy/log/
    Image:       docker.io/library/nginx:1.27-alpine
    Container:   pgcli-nginx-default-proxy
```

## 启停

```bash
pg addon start nginx --name proxy
pg addon stop  nginx --name proxy
```

`start` 只启动已存在的容器（并以磁盘上已有的 `nginx.conf` 重建来自愈不当状态）。
配置和日志目录跨 stop/start 保留。

## 开机自启

容器带 `--restart unless-stopped` 策略（管崩溃，不管重启）。要在宿主重启后拉起
实例：

```bash
pg autostart enable --nginx --name proxy
```

nginx 自启是**只启动**（先 install）。开机时读取磁盘上已有的 `nginx.conf`，所以
代理以最后已知配置启动。nginx 独立于 PostgreSQL 栈——它是自己拨号的反向代理
——所以没有排序约束，开机 `pg start --autostart` 把它与其他非数据库插件一起启动。

## 移除

```bash
pg addon remove nginx --name proxy
```

停止并删除容器，同时删除其配置目录（包括 TLS 证书和日志）。空的实例父目录被
修剪。

## 日志

```bash
pg logs addon nginx --name proxy        # 最近 50 行
pg logs addon nginx --name proxy -f     # 跟踪
```

容器日志走 stdout，所以 access 和 error 事件出现在这里。按 backend 查看日志，
看挂载的日志目录：

```bash
ls ~/pg/addon/nginx/proxy/log/
# access.log  access_pgadmin.log  access_postgrest.log
# error.log   error_pgadmin.log   error_postgrest.log
```

## 排障

- **启动时配置语法错误。** 容器退出，重启策略反复自旋。用
  `pg addon nginx test --name proxy` 检查（容器停了也能用）——它在临时容器里跑
  `nginx -t`，报告确切行号。
- **`--conf-file` 与 `--upstream` 一起用。** 安装拒绝组合——选一种模式。
- **从别的机器打不开代理。** `listen` 默认 `127.0.0.1`。`--listen 0.0.0.0 --force`
  重装并接受代理成为 backends 前的唯一防线。
- **install 时找不到镜像。** `docker.io/library/nginx` 按需拉取；离线宿主先
  `podman load` 对应 tar（目录与导出步骤在仓库 `docs/images.md`），pull 会被跳过。
- **Reload 失败"container not running"。** `pg addon nginx reload` 需要容器在
  运行中。先用 `pg addon start nginx --name proxy` 启动。

## 已知限制

- **无负载均衡或健康检查。** 这里的 nginx 是反向代理（基于路径的路由，每个
  location 单个 upstream），不是 TCP 负载均衡器。要在 Patroni 前做负载均衡，用
  [HAProxy](../haproxy/)。
- **模板刻意保持简单。** 渲染出的 `nginx.conf` 覆盖基于路径的路由到若干
  backends，可选 TLS。生产配置需要 `limit_req_zone`、`resolver` + 变量
  `proxy_pass`、WebSocket、SNI、每个 backend 多个 location 或 upstream keepalive
  时，应改用 `--conf-file`。
- **macOS 代码完备、轻度测试。** 桥接路径（发布端口、`proxyBindHost` 把 loopback
  放宽到 `0.0.0.0`）与其他桥接插件同构；已演练但未在负载下测试。
