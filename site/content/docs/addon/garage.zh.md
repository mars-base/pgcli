---
title: "Garage"
description: "以 pgcli 插件方式运行 Garage（Deuxfleurs 的 Rust S3 兼容对象存储）——单节点、明文 HTTP，TLS 由 nginx 插件前置来终结"
weight: 45
---

[Garage](https://garagehq.deuxfleurs.fr) 是 [Deuxfleurs](https://deuxfleurs.fr)
出品的 Rust、AGPLv3 S3 兼容对象存储重实现，为小规模自建和异地分布式集群设
计。pgcli 将其作为**独立的顶层插件**运行，CLI 表面与
[`minio`](../minio/)、[`silo`](../silo/)、[`rustfs`](../rustfs/) 一致
（install、list、logs、autostart、password），但其运行时模型在四个方面与三者
都不同，这也决定了本页的写法：

1. **渲染 TOML 配置文件，而不是 argv/env。** Garage 从 `/etc/garage.toml` 读
   配置。pgcli 在实例目录下渲染该文件并以只读方式 bind 挂载；minio/silo/rustfs
   插件则传 argv 或 env。
2. **两个数据目录，而不是一个。** `metadata_dir`（LMDB）与 `data_dir`（数据
   块）是各自独立的 bind 挂载。
3. **四个端口，来自自己的池。** S3 API、节点间 RPC、`s3_web` 静态站点、
   admin/metrics API——每实例一起从 `garage_start_port`（默认 **3900**）分配，
   与共享的 minio/silo/rustfs 池是两条独立游标。
4. **无原生 TLS——仅明文 HTTP。** pgBackRest 的 S3 仓库强制 HTTPS，因此一个要
   接收 `archive-push` 的 Garage 存储必须**由
   [nginx 插件](../nginx/)前置**——见[用 nginx 前置实现
   TLS](#用-nginx-前置实现-tls)。

和 rustfs 一样，在开源 MinIO 仓库已归档之后，Garage 是一条很好的替代路线。v1
仅支持**单节点**（`replication_factor = 1`、`garage server --single-node`）；
pgcli 尚未管理多节点与跨 zone 布局。

> **平台支持：** 已在 Linux（host 网络）上验证。macOS 的 bridge 路径会发布全
> 部四个端口、代码完整，遵循与其它对象存储一致的形态，但尚未在真机上跑过。

## 工作原理

一个实例一个容器。pgcli 直接拉取**上游** `docker.io/dxflrs/garage` 镜像
（仅 pull）——不像 rustfs 需要自建 wrapper 镜像，因为 Garage 在自己的容器里以
root 运行、且不带 uid-drop，所以不存在主机侧属主处理要消化。

数据存放在两个长于容器生命周期的 bind 挂载主机目录里：

- `<base-dir>/addon/garage/<name>/data` → `/var/lib/garage/data`（数据块），
- `<base-dir>/addon/garage/<name>/meta` → `/var/lib/garage/meta`（LMDB），

外加渲染出的 `<base-dir>/addon/garage/<name>/garage.toml` →
`/etc/garage.toml`，以**只读**方式挂载（Garage 从不改写自身配置）。

凭据的处理与其它存储类似，但 Garage 用的是 **access key / secret key 对**，而不
是 root user / password：

- **`root_user`** 是 access key id，**首次安装时生成**，带 Garage 自家的 `GK`
  前缀形态（`GK` + 32 位十六进制，即上游 quick-start 里
  `openssl rand -hex 16` 产出的形状），或用 `--root-user` 指定。
- **`root_password`** 是 secret key，**首次安装时生成**（64 位十六进制），或用
  `--root-password` 指定。
- 第三个秘密 `rpc_secret`（集群 RPC 签名密钥）以同样方式生成并一并存储，从不
  在 CLI 表面出现。

三者都落进 `pg.yaml`（`addons.garage.<name>.*`），因此重建容器是稳定的。首次启
动时容器被传入 `--single-node --default-bucket` 以及
`GARAGE_DEFAULT_ACCESS_KEY` / `GARAGE_DEFAULT_SECRET_KEY` /
`GARAGE_DEFAULT_BUCKET` env，于是 Garage 自己创建这个 access key 和一个桶（以实
例名命名、归属该 key）——无需安装后再 exec。key 与桶都存在 `metadata_dir` 里，
因此能挺过容器重建。

## 安装

```bash
# 默认实例名 "garage"，四个端口取自 garage 池，回环绑定
pg addon install garage

# 指定名字与显式数据目录的实例
pg addon install garage --name store --data-dir /srv/garage

# 固定 S3 API 端口（rpc/web/admin 取随后三个空闲端口）
pg addon install garage --name store --api-port 3900

# 把存储暴露到网络而非仅回环
pg addon install garage --name store --listen 0.0.0.0

# 自带 access key / secret key，而不是自动生成
pg addon install garage --name store --root-user GK<...> --root-password <secret>
```

输出会报告端点和播种的凭据：

```
✓ garage installed: "store"
  Container:     pgcli-garage-default-store
  Image:         docker.io/dxflrs/garage:v2.4.1
  Data:          ~/pg/addon/garage/store/data
  Metadata:      ~/pg/addon/garage/store/meta
  Config:        ~/pg/addon/garage/store/garage.toml
  S3 API:        http://127.0.0.1:3900
  RPC/Web/Admin: 3901/3902/3903

  Access key:    GK<generated>
  Secret key:    <generated>
  Bucket:        store (created on first boot, owned by the access key)

  NOTE: Garage serves plaintext HTTP only. pgBackRest requires HTTPS — front it
        with the nginx addon via --conf-file (template --upstream mode strips
        the port from Host and breaks S3 signing; see docs/addon/garage):
          backend 127.0.0.1:3900, listen ssl, proxy_set_header Host $http_host
  Try it:        pg mc alias set store http://127.0.0.1:3900 GK<...> <secret> && pg mc ls store
```

对**存活**实例重跑 install 是 no-op：容器不会被重建（处于停止的会被启动，并给出
提示），flag 会合并进已存配置，已有凭据保持不变。传 `--force` 会重建容器，从而让
改动过的端口、监听地址或凭据生效——但不丢两个数据目录。

> **监听地址：** 默认 `127.0.0.1` 让存储留在本地。`--listen 0.0.0.0`（或
> `pg.yaml` 里的 `listen` 键）把它暴露到网络。由于 Garage 服务的是**明文
> HTTP**，任何能触达端口的人都能在传输中嗅到凭据——所以只在防火墙后公开绑定，
> 实际做法则是放在[nginx TLS 前置](#用-nginx-前置实现-tls)之后。

> **`--tls` 对 garage 会被拒绝。** Garage 没有可启用的 TLS 监听器；传
> `--tls`/`--tls-cert`/`--tls-key` 会直接报错并把你指向 nginx 前置，而不是默默
> 忽略这个 flag。

### 用 nginx 前置实现 TLS

由于 pgcli 不把两个插件耦合在一起，在一个 Garage 存储前面终结 TLS 是显式的两步：
先装 garage，再用 nginx 前置它。

> **用 `--conf-file`，不要用 `--upstream` 模板。** S3 请求用 SigV4 对**含端口
> 的** `Host` 头签名。模板模式转发 `Host $host`，而 nginx 渲染它时**不带**端口
> ——于是 Garage 重新算出的 canonical request 不同，每个写操作都以 `Invalid
> signature` 失败（已实测：经模板前置 `pg mc mb` 被拒；经下面 `--conf-file` 前置
> 则完整往返）。自定义配置还能设 `client_max_body_size 0;`（nginx 默认的 1 MiB
> 上限会拒掉任何真实对象上传）以及很长的代理超时。

```bash
pg addon install garage --name store

# front.conf —— 经 TLS 的 path-style S3，保留 host+port 以供签名：
cat > front.conf <<'EOF'
worker_processes auto;
pid /tmp/nginx.pid;
events { worker_connections 1024; }
http {
    access_log /var/log/nginx/access.log;
    error_log  /var/log/nginx/error.log;
    server {
        listen 127.0.0.1:8081 ssl;      # pgcli 分配给前置的 HTTPS 端口
        ssl_certificate     /etc/nginx/certs/public.crt;
        ssl_certificate_key /etc/nginx/certs/private.key;
        client_max_body_size 0;          # PUT 不限大小
        proxy_read_timeout   3600s;
        proxy_send_timeout   3600s;
        location / {
            proxy_pass http://127.0.0.1:3900;   # garage 的 S3 API 端口
            proxy_set_header Host $http_host;   # 保留端口 —— SigV4 需要
            proxy_request_buffering off;
            proxy_http_version 1.1;
        }
    }
}
EOF

pg addon install nginx --name gtls --tls --conf-file front.conf --https-port 8081
```

`--tls` 让 pgcli 生成自签 CA + leaf（`public.crt` / `private.key`，挂在
`/etc/nginx/certs`），并起一个 HTTPS 监听器。把 S3 客户端指向
`https://<host>:8081`，并把前置的 CA `<base-dir>/addon/nginx/gtls/tls/ca.crt`
交给消费方。

> **为什么 `listen` 与 `proxy_pass` 两行要手写死。** `--conf-file` 模式下 pgcli
> 原样挂载你的配置，所以里面的端口必须与两个插件被分配到的端口对上——garage 的
> `S3 API:` 端口取自它的安装输出，nginx 的 `HTTPS:` 端口取自 `pg addon list`。
> （`$http_host` 和 `client_max_body_size` 两行，正是模板前置对 S3 会搞错的地
> 方。）

把那个 CA 弄到远程 Patroni 主机上而无需手动 scp，正是 `pg backup fetch-ca` 的用
武之地——它拨通 TLS 前置，直接从握手里取出根证书：

```bash
pg backup fetch-ca <front-host>:<https-port>
#   [OK] CA fetched from <front-host>:<https-port>
#        saved:    ~/.pgcli/backup/repo-ca/ca-<front-host>-<https-port>.crt
#        SHA-256:  c0f0…fe2e
pg backup setup --s3-ca-file ~/.pgcli/backup/repo-ca/ca-<front-host>-<https-port>.crt
```

这个拉取是 trust-on-first-use——在信任之前，把打印出的 SHA-256 与前置主机上的
`sha256sum ~/.pgcli/addon/nginx/gtls/tls/ca.crt` 对一遍。

### 把 pgBackRest 接到前置后的 Garage 存储

一旦有了 TLS 端点，一个 Garage 支撑的 S3 仓库与 MinIO 的完全一样——S3 契约相
同（path-style、`repo1-s3-uri-style=path`）：

```bash
pg backup setup \
  --s3-endpoint <front-host>:<https-port> \
  --s3-bucket store \
  --s3-access-key GK<...> \
  --s3-secret-key <secret> \
  --s3-ca-file <front>/addon/nginx/gtls/tls/ca.crt
```

完整流程见[备份 → S3 对象存储仓
库](../../backup/#s3-对象存储仓库)。`pg addon password garage --name store` 会
为脚本打印 secret key。

## 使用 mc 客户端

`pg mc` 从一个临时容器里跑 MinIO 自家的 `mc` 客户端——无需本地安装、无需手动
`podman run`——并像对其它存储一样讲 Garage 的 S3 API。用 **access key** 顶替
user、**secret key** 顶替 password：

```bash
# 按平台选 URL——见下面"按平台的端点"：
pg mc alias set store http://127.0.0.1:3900 GK<...> <secret>       # Linux，本地插件
pg mc alias set store http://host.containers.internal:3900 GK<...> <secret>  # macOS，本地插件
pg mc alias set store http://10.0.5.7:3900 GK<...> <secret>        # 二者皆可，远程存储
pg mc mb store/backups
pg mc ls store                       # 播种的 "store" 桶已经在这里
pg mc cp ./dump.pglz store/backups/
```

alias 持久化在主机上的 `~/.mc/config.json`——mc 自己的默认路径，Linux 和 macOS
上一致——所以 `alias set` 一次，之后每次 `pg mc` 调用（以及 Linux 上的原生
`mc`）都看到同样的 alias。`pg mc alias list` 读回，`pg mc alias remove` 删
除。`alias set` 在写入前先对端点校验凭据，所以写错的 key 不会污染
`~/.mc/config.json`。底层上 `pg mc` 把你的 `~/.mc` 挂进容器并跑
`ghcr.io/mars-base/pgcli/pgcli-mc`（`scratch` 上的静态上游二进制）；此外没有任何
魔法。

任何会被 `pg` 自身 flag 解析器拒绝的 `mc` flag 都放在 `--` 之后，与其它存储完全
一样——`--recursive`、`--force`、`--all`、`--json` 等等：

```bash
pg mc cp ./dir store/backups/ -- --recursive
pg mc rb store/scratch -- --force
```

> **按平台的端点：** 在 Linux 上 `pg mc` 跑在 host 网络里，所以 `127.0.0.1` 的
> alias 能直达本地插件实例。在 macOS 上容器坐在 bridge 网络里，那里的
> `127.0.0.1` 是容器自己的回环——把本地 Mac 插件的 alias 指向
> `http://GK<...>:<secret>@host.containers.internal:3900`，或把远程存储指向其可
> 路由地址。要走 TLS，就把 alias 设成 nginx 前置的 `https://` 端点（自签 leaf
> 再加 `-- --insecure`）。

### 常用命令

| 命令 | 用途 |
|-------|---------|
| `pg mc ls store` / `pg mc ls store/backups` | 列桶 / 列对象 |
| `pg mc mb store/backups` | 建桶 |
| `pg mc cp ./file store/backups/` | 上传（整棵树：`pg mc mirror ./dir store/backups/`，或给 `cp` 加 `-- --recursive`） |
| `pg mc cp store/backups/file ./` | 下载 |
| `pg mc find store -- --name '*.pglz'` | 按名字搜对象 |
| `pg mc du store` | 每桶用量 |
| `pg mc rm store/backups/file` | 删单个对象 |
| `pg mc rm store/prefix -- --recursive` | 批量删对象 |
| `pg mc rb store/bucket -- --force` | 连对象一起删桶 |

同一对 access/secret key 适用于任何 S3 SDK——包括对 `repo1-type=s3` 仓库的
pgBackRest（经 nginx TLS 前置）。

## 端口

每个实例从 Garage 自己的池取**四个连续端口**，起点是 `garage_start_port`（默认
**3900**）：先是 S3 API，然后 RPC、`s3_web`、admin/metrics。这个池**与共享的
minio/silo/rustfs 池（那个从 `minio_start_port`、默认 9000 起）分开**，所以一个
garage、一个 minio、一个 rustfs 实例永不冲突：

```bash
pg addon install garage --name store    # 3900 / 3901 / 3902 / 3903
pg addon install garage --name lake     # 3904 / 3905 / 3906 / 3907
pg addon install minio  --name archive  # 9000 / 9001  （共享池）
```

只有 S3 API 端口是旋钮（`--api-port`）；另外三个被分配其后的空闲端口。RPC 端口
是节点间流量，对单节点实例会留在回环上；admin/metrics 端口（`3903`）服务 Garage
的状态 API。

## 配置

实例位于 `pg.yaml` 顶层的 `addons.garage` 映射里：

```yaml
namespace: default
garage_start_port: 3900    # garage 自己的池（API/RPC/web/admin，每个占四个）
addons:
  garage:
    store:
      container_name: pgcli-garage-default-store
      name: store
      image_tag: docker.io/dxflrs/garage:v2.4.1
      # data_dir: /srv/garage      # 省略则为 <base-dir>/addon/garage/store/data
      #                             # （meta 目录和 garage.toml 与它同级）
      # config_file: /etc/garage.toml   # 自带配置：原样挂载，永不改写
      listen: 127.0.0.1
      api_port: 3900
      rpc_port: 3901
      web_port: 3902
      admin_port: 3903
      root_user: GK<generated>        # access key id，首次安装时写入
      root_password: <generated>      # secret key，首次安装时写入
      rpc_secret: <generated>         # 集群 RPC 签名密钥，首次安装时写入
      region: garage                  # s3_region
      replication_factor: 1           # v1 仅单节点；>1 会被拒绝
      autostart: false                # pg autostart enable --garage --name store
```

对 `listen`、端口、`root_user`、`root_password`、`image_tag`、`data_dir` 或
`config_file` 的修改，在下一次 `pg addon install garage --name store --force` 后
生效。手写的 `replication_factor: 2` 会在安装时被拒绝——多节点不在 v1 范围内。

### 列表

```bash
pg addon list
```

```
Infra add-ons (garage):
  garage (name: store)
    Status:      running
    Listen:      127.0.0.1
    API port:    3900
    RPC/Web/Admin: 3901/3902/3903
    S3 URL:      http://127.0.0.1:3900
    Data:        ~/pg/addon/garage/store/data
    Metadata:    ~/pg/addon/garage/store/meta
    Access key:  GK<generated>
    Bucket:      store
    Image:       docker.io/dxflrs/garage:v2.4.1
    Container:   pgcli-garage-default-store
    NOTE:        plaintext HTTP only — front with the nginx addon for TLS
```

`pg addon list` 从不打印 secret key。要刻意揭示它用
`pg addon list --show-password`（追加一行 `Secret key:`），或为脚本只打印这个秘
密：`pg addon password garage --name store`（secret key，与上面 `Access key:` 配
对；`--file <path>` 以 0600 写出）。

## 启停

```bash
pg addon start garage --name store
pg addon stop  garage --name store
```

`install` 跳过仍存在的容器（停止的会被启动）；`start` 只启动已存在的容器（并通
过从配置重建来自愈一个处于 improper 状态的容器，同时重新渲染
`garage.toml`）。

## 开机自启

容器带 `--restart unless-stopped` 策略（管崩溃，不管重启）。要在主机重启后拉起实
例：

```bash
pg autostart enable --garage --name store
```

garage 自启会重新渲染 `garage.toml` 并启动已存在的容器。Garage 在上游镜像里以
root 运行，所以开机启动除运行 podman 本身外不需要特殊主机权限。开机是**仅启
动**；garage 独立于 PostgreSQL 栈，所以与其它对象存储一起被启动。

## 移除

```bash
pg addon remove garage --name store            # 容器消失，数据保留
pg addon remove garage --name store --clean-data   # 同时删除两个目录 + 配置
```

两个数据目录**就是对象存储**——丢了它们等于丢了里面每个桶——所以 `remove` 默认
保留。`--clean-data` 删除数据目录、meta 目录和 pgcli 渲染的 `garage.toml`（你自
带 `config_file` 指向的文件从不被碰），并清理已空的默认布局父目录。与 rustfs 不
同，Garage 以 root 写、pgcli 在受管主机上以 root 跑 podman，所以普通删除即可——
没有 `podman unshare` 兜底。

## 日志

```bash
pg logs addon garage --name store      # 最后 50 行
pg logs addon garage --name store -f   # 跟随
```

## 故障排除

- **install 时 `pulling garage image ... : ...`。** tag
  `docker.io/dxflrs/garage:v2.4.1` 不可达——检查到 Docker Hub 的网络，或先用
  `podman pull` 预拉。
- **`pg mc alias set` 报连接错误。** 默认 `--listen 127.0.0.1` 把存储绑在回环
  上；在 macOS 上用 `host.containers.internal` 而非 `127.0.0.1`。用
  `pg addon list` 确认实时 API 端口。
- **经 nginx 前置的写操作报 `Invalid signature`。** 前置把 `Host` 重写成不带端口
  的形式；用 `proxy_set_header Host $http_host;`（[conf-file
  配方](#用-nginx-前置实现-tls)就是这么写的）——`--upstream` 模板模式写死
  `$host`，不适合 S3。
- **经 nginx 前置的 PUT 在 ~1 MiB 处失败。** nginx 默认
  `client_max_body_size` 为 1m；conf-file 配方把它设成 `0`（不限）。
- **pgBackRest 拒绝端点（HTTP / 证书）。** pgBackRest 对 S3 仓库强制 HTTPS。用
  nginx 插件（`--tls`）前置 Garage，并把 `backup.repo.s3.ca_file` 指向前置的
  `ca.crt`。
- **`replication_factor` 必须为 1。** v1 是单节点；手改的
  `replication_factor: 2` 会在安装时被拒。pgcli 尚未管理多节点 Garage。
