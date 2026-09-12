---
title: "MinIO"
description: "以 pgcli 插件方式运行 MinIO——单机 S3 兼容对象存储（含 Web 控制台），典型用途是共享的 pgBackRest 备份仓库"
weight: 49
---

[MinIO](https://min.io) 是 S3 兼容的对象存储。pgcli 将其作为**独立的顶层插件**
运行——共享基础设施，而非实例级 sidecar——采用**单机（single-node）**模式，带
Web 控制台。典型用途是所有主机都能访问的备份仓库（例如 Patroni 集群的
pgBackRest `repo1-type=s3` 目标），但它本身就是通用对象存储。

> **平台支持：** MinIO 插件**仅支持 Linux**。它通过主机网络提供服务，而 macOS
> 的 `podman machine` 不会把主机网络暴露给容器，因此在该平台上会快速失败并给出
> 明确提示。公开镜像为双架构（amd64 + arm64），任意 Linux 主机架构均可运行；
> macOS 上 `pg addon list` 仍可显示已配置实例，但没有实时状态。

## 工作原理

一个容器、一个目录：`minio server /data --address <listen>:<api-port>
--console-address <listen>:<console-port>`，其中 `/data` 是实例主机数据目录
（默认 `<base-dir>/addon/minio/<name>/data`）的 bind mount。MinIO 存储的一切
都在这个目录里，因此它的生命周期长于容器。

镜像是公开的预构建 tag `ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226`——
上游静态二进制（双架构 amd64 + arm64，来自 minio/minio 的 GitHub release）跑在
Alpine 上，因为 MinIO 官方镜像移除了内置的 Web 控制台。`pg addon install`
只负责拉取；pgcli 运行时从不构建镜像。

凭据的处理方式与 Patroni 相同：

- `root_user` 默认 `admin`；
- `root_password` **首次 install 时自动生成**，存入 `pg.yaml`
  （`addons.minio.<name>.root_password`），并在安装摘要中**打印一次**方便记录。

容器按 MinIO 官方部署建议带上 `--ulimit nofile=1048576:1048576` 和
`--stop-timeout 60`。注意 root 凭据通过 `-e` 传入，会出现在 `podman inspect`
里——与手工运行容器的暴露程度相同；对 rootless 单机部署（本插件的定位）可以
接受。

## 安装

```bash
# 默认实例名 "minio"，端口从池中分配（基址 9000），只绑回环
pg addon install minio

# 命名实例 + 显式数据目录
pg addon install minio --name store --data-dir /srv/minio

# 固定端口、指定 root 用户
pg addon install minio --name store --api-port 9000 --console-port 9001 --root-user admin

# 绑定到所有网卡，暴露到网络
pg addon install minio --name store --listen 0.0.0.0
```

输出会报告端点与 root 凭据：

```
✓ minio installed: "store"
  Container:    pgcli-minio-default-store
  Image:        ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226
  Data:         ~/pg/addon/minio/store/data
  S3 API:       http://127.0.0.1:9000
  Console:      http://127.0.0.1:9001

  Root user:     admin
  Root password: <generated>
                 (also stored in ~/.pgcli/pg.yaml, addons.minio.store.root_password)
```

用打印出的 root 用户与密码登录 `Console:` 地址的控制台。S3 客户端
（包括 pgBackRest）指向 `S3 API:` 地址即可。

对**已存在**的实例重复执行 install 是无损的：不会重建容器（处于停止状态的会
直接启动并给出提示），命令行参数会合并进已存配置，**已有的** root 密码保持
不变。要让改过的端口、监听地址或凭据生效，加 `--force` 重建容器（数据目录不受
影响）。

> **绑定地址：** 默认 `127.0.0.1`，存储仅本机可见。`--listen 0.0.0.0`（或
> `pg.yaml` 里的 `listen` 键）会把它暴露到网络上——能访问该端口的任何人都可尝试
> root 凭据，因此只应在防火墙后或 TLS 终结代理之后这样暴露。

## 使用 mc 客户端

`pg mc` 在一次性容器里运行 MinIO 自家的 `mc` 客户端——不用本地安装，也不用
手敲 `podman run`：

```bash
pg mc alias set store http://127.0.0.1:9000 admin \
  "$(yq '.addons.minio.store.root_password' ~/.pgcli/pg.yaml)"
pg mc mb store/backups
pg mc ls store
pg mc cp ./dump.pglz store/backups/
```

别名持久化保存在宿主的 `~/.mc/config.json`——这是 `mc` 自己的默认路径，
Linux 和 macOS 都一样——`alias set` 一次，之后每次 `pg mc`（以及 Linux 上
原生安装的 `mc`）都能看到同一批别名。`pg mc` 底层做的事只是把你的 `~/.mc`
挂载进容器，然后运行 `ghcr.io/mars-base/pgcli/pgcli-mc`（静态上游二进制 +
`scratch`，首次使用时自动拉取）；没有别的魔法。

任何会被 `pg` 自己的参数解析器拒绝的 `mc` 标志（`--all`、`--json` 等）放到
`--` 之后，与 `pg etcdctl` 的约定完全一致：

```bash
pg mc ls store -- --all
```

`MC_HOST_<name>` 环境变量形式的别名——`mc` 的无状态用法，不碰配置文件——同样
适用于 `pg mc`，因为该变量会被转发进容器：

```bash
PASS=$(yq '.addons.minio.store.root_password' ~/.pgcli/pg.yaml)
MC_HOST_store="http://admin:${PASS}@127.0.0.1:9000" pg mc ls store
```

> **平台：** MinIO **插件**本身仍仅支持 Linux (amd64)（见上文）；`pg mc`
> 只是一个客户端，macOS 上同样可用，指向远端或局域网端点即可。macOS 上别名
> 里的 `127.0.0.1` / `localhost` 是容器自己的回环，不是宿主机的；请改用可路由
> 地址（或 `host.containers.internal`）。Linux 上 `pg mc` 走主机网络，回环
> 别名可以直达本机 `pg addon install minio` 起的实例。

### 常用命令

| 命令 | 用途 |
|------|------|
| `pg mc ls store` / `pg mc ls store/backups` | 列出桶 / 对象 |
| `pg mc mb store/backups` | 创建桶 |
| `pg mc cp ./file store/backups/` | 上传（整个目录加 `--recursive`） |
| `pg mc cp store/backups/file ./` | 下载 |
| `pg mc find store -- --name '*.pglz'` | 按名字搜索对象 |
| `pg mc du store` | 统计各桶大小 |
| `pg mc rm store/backups/file` | 删除单个对象 |

同一套 root 凭据适用于任何 S3 SDK——包括 pgBackRest 的 `repo1-type=s3`
仓库（见[备份](/docs/backup/)、[恢复](/docs/restore/)）。

## 端口

每个实例从同一个端口池取**两个连续端口**，基址为 `minio_start_port`（默认
**9000**）：先 S3 API、后控制台。与其他自动分配池一样，会跳过已占用端口与同级
实例的显式端口：

```bash
pg addon install minio --name store    # 9000 / 9001
pg addon install minio --name archive  # 9002 / 9003
```

## 配置

实例存放在 `pg.yaml` 顶层 `addons.minio` 映射中，以实例名为键：

```yaml
namespace: default
minio_start_port: 9000
addons:
  minio:
    store:
      container_name: pgcli-minio-default-store
      name: store
      image_tag: ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226
      # data_dir: /srv/minio     # 省略则为 <base-dir>/addon/minio/store/data
      listen: 127.0.0.1
      api_port: 9000
      console_port: 9001
      root_user: admin
      root_password: <generated>   # 首次 install 时写入
      autostart: false             # pg autostart enable --minio --name store
```

修改 `listen`、端口、`root_user`、`root_password`、`image_tag` 或 `data_dir`
后，执行 `pg addon install minio --name store --force` 即生效——普通 install 会
跳过已存在的容器，`--force` 会重建它（数据目录永不受影响）。

### 查看列表

```bash
pg addon list
```

```
Infra add-ons (minio):
  minio (name: store)
    Status:      running
    Listen:      127.0.0.1
    API port:    9000
    Console port: 9001
    Console URL: http://127.0.0.1:9001/
    Data:        ~/pg/addon/minio/store/data
    Root user:   admin
    Image:       ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226
    Container:   pgcli-minio-default-store
```

`pg addon list` 从不打印密码——请到 `pg.yaml` 里查看。

## 启动与停止

主机重启后，不重新下发配置即可拉起实例：

```bash
pg addon start minio --name store
pg addon stop  minio --name store
```

`install` 会跳过已存在的容器（处于停止状态的直接启动）；`start` 只启动已有
容器（状态异常时依据配置自动重建）。

## 开机自启

容器带 `--restart unless-stopped` 策略（管崩溃、不管重启）。主机重启后自动拉起
实例：

```bash
pg autostart enable --minio --name store
```

这会置 `autostart: true` 并安装/刷新 boot service（见
[开机自启](/docs/autostart/)）。开机路径是**只启动**语义。MinIO 与 PostgreSQL
栈相互独立，因此排在最后启动，无顺序依赖。`pg autostart status` 列出所有目标
的状态。

## 移除

```bash
pg addon remove minio --name store               # 只删容器，数据保留
pg addon remove minio --name store --clean-data  # 连数据目录一起删除
```

数据目录**就是对象存储本身**——丢了它等于丢掉里面所有 bucket——因此 `remove`
默认保留它并打印其位置。`--clean-data` 会删除它（并清理默认布局下已清空的父
目录；显式 `data_dir` 及其父目录永不受影响）。

## 日志

```bash
pg logs addon minio --name store      # 最近 50 行
pg logs addon minio --name store -f   # 持续跟踪
```

MinIO 日志走 stdout：启动行（`API:`/`Console:` 地址、`Documentation:`）与请求
错误。看到 `API: http://...` 块即确认监听已就绪。

## 故障排除

- **install 报 `pulling minio image ... : ...`。** 公开 tag
  `ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226` 拉不到——检查网络/registry
  可达性，或先 `podman pull` 手动拉取。
- **手动 `--api-port` 撞端口。** 请选端口池（`minio_start_port` 起）之外的端口，
  否则会被自动分配器视为已占用；另注意 MinIO 需要的是一对连续端口。
- **主机重启后 `pg addon start` 无效果/失败。** 看
  `pg logs addon minio --name store -f`——多半是数据目录被删了（`--clean-data`
  或人为），MinIO 拒绝在曾格式化过的空目录上启动；或者绑定端口变了。
- **控制台能打开，但 S3 客户端超时。** `MINIO_SERVER_URL` 由 `listen` + API
  端口拼成；若绑在 `127.0.0.1` 却从其他主机访问，客户端会被重定向到回环地址。
  把 `listen` 设为客户端真正可达的地址。
- **macOS。** 暂不支持——该插件通过主机网络提供服务，而 `podman machine` 不会
  把它暴露给容器。Linux（amd64 与 arm64）均可用；见上文平台支持。
