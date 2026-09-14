---
title: "MinIO"
description: "以 pgcli 插件方式运行 MinIO——单机 S3 兼容对象存储（含 Web 控制台）"
weight: 49
---

[MinIO](https://min.io) 是 S3 兼容的对象存储。pgcli 将其作为**独立的顶层插件**
运行——共享基础设施，而非实例级 sidecar——带 Web 控制台。默认采用**单机
（single-node）**模式，也支持跨主机的**分布式集群模式**（见下文
[分布式 / 集群模式](#分布式--集群模式)）。两种模式下它都是通用对象存储。

> **平台支持：** MinIO 插件**两个平台都支持**。Linux 上通过主机网络提供服务；
> macOS 上加入 `pgcli-net` bridge 网络并发布两个端口，Mac 用
> `127.0.0.1:<port>` 即可访问 API 与控制台，与其他插件一致。公开镜像为双架构
> （amd64 + arm64），任意主机架构均可运行。[`pg mc`
> 客户端](#使用-mc-客户端)同样两个平台可用。

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
- `root_password` **首次 install 时自动生成**（也可以用 `--root-password`
  显式指定），存入 `pg.yaml`
  （`addons.minio.<name>.root_password`），并在安装摘要中**打印一次**方便记录。

二者就是 MinIO root 的 **access key / secret key**——所有 S3 客户端（包括
`pg mc alias set <名称> <URL> <root_user> <root_password>`）所说的 access key
与 secret key 就是它们。

容器按 MinIO 官方部署建议带上 `--ulimit nofile=1048576:1048576` 和
`--stop-timeout 60`；macOS 上 `podman machine` 虚拟机的 RLIMIT_NOFILE 上限更
低，因此该平台改用 `65536`——单机 dev/test 足够。注意 root 凭据通过 `-e` 传
入，会出现在 `podman inspect` 里——与手工运行容器的暴露程度相同；对 rootless
单机部署（本插件的定位）可以接受。

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
```

用打印出的 root 用户与密码登录 `Console:` 地址的控制台。S3 客户端
（包括 pgBackRest）指向 `S3 API:` 地址即可，也可以在终端用下文的
[`pg mc`](#使用-mc-客户端)直接操作。

对**已存在**的实例重复执行 install 是无损的：不会重建容器（处于停止状态的会
直接启动并给出提示），命令行参数会合并进已存配置，**已有的** root 密码保持
不变。要让改过的端口、监听地址或凭据生效，加 `--force` 重建容器（数据目录不受
影响）。

> **绑定地址：** 默认 `127.0.0.1`，存储仅本机可见。`--listen 0.0.0.0`（或
> `pg.yaml` 里的 `listen` 键）会把它暴露到网络上——能访问该端口的任何人都可尝试
> root 凭据，因此只应在防火墙后或 TLS 终结代理之后这样暴露。

## 分布式 / 集群模式

也支持 MinIO 的纠删码（EC）集群模式。它要求**至少 4 个互不相同的
`host:port` 端点**，并且与其他插件不同，**没有中心协调者**：每台主机各自跑
一份 pgcli、各自一份 `pg.yaml`，而每一份 `pg.yaml` 都完整写入*同一份*端点
列表和*同一套* root 凭据。pgcli 只负责拉起本机对应的那一个容器——跨主机的
组环握手由 MinIO 自己完成。

端点列表就是模式开关：为空即单机（行为不变），非空则以
`minio server <ep1> <ep2> ...` 进入分布式模式。

每个端点的形式是 `http://<host>:<port><路径>`。`host:port` 是节点之间互相
访问、完成组环握手的地址。尾部那段 `<路径>` **不是** HTTP 路由——客户端永远
看不到它——它是 **export path（导出路径）**，即该节点把自己那一份纠删码分片
数据存放在**容器内**的哪个目录。pgcli 总是把 `--data-dir` 挂载到容器的 `/data`，
所以这段路径必须是 `/data` 本身，或它下面的一个子目录。（如果选了子目录，
比如 `/data/minio`，MinIO 会在挂载卷**内部**创建这一层目录——也就是说宿主上
会多出一层你从未传给 `--data-dir` 的嵌套：`--data-dir /data/minio` 搭配
endpoint `.../data/minio`，数据实际落在宿主 `/data/minio/minio`，而不是
`/data/minio`。）四个端点里这段路径字符串要保持一致。

```bash
# 节点 1（10.0.0.11），宿主上独立数据盘已挂载到 /data/minio：
pg addon install minio --name store \
  --listen 10.0.0.11 \
  --data-dir /data/minio \
  --root-password '<共享密码>' \
  --endpoint http://10.0.0.11:9000/data/minio \
  --endpoint http://10.0.0.12:9000/data/minio \
  --endpoint http://10.0.0.20:9000/data/minio \
  --endpoint http://10.0.0.21:9000/data/minio

# 节点 2-4：同样的命令、各自的 --listen、完全相同的 --endpoint 列表，以及
# 完全相同的 --root-password 值。
```

`--root-password` 是可选的：不传的话首次 install 会自动生成一个（打印一次、
存入 `pg.yaml`）。集群模式下每台传同一个值，就能保证全集群凭据一致，不需要
任何手工复制。

安装摘要会列出成员并提醒一致性要求：

```
✓ minio installed: "store"
  ...
  Distributed mode: 4 endpoints
    - http://10.0.0.11:9000/data/minio
    - http://10.0.0.12:9000/data/minio
    - http://10.0.0.20:9000/data/minio
    - http://10.0.0.21:9000/data/minio
  NOTE: every node's pg.yaml must carry the identical endpoint list AND
  identical root credentials, or the cluster will not form.
```

集群模式有三条硬性约束，都是实测踩出来的：

- **端点必须是可路由、互不相同的主机。** 同一主机会折叠成"单机多盘"并被
  拒绝（`use path style endpoint for single node setup`）；`127.0.0.0/8`
  回环段直接被拒（`resolves to localhost`）。请用各节点真实的局域网地址。
- **数据目录必须位于与根文件系统不同的磁盘上。** MinIO 拒绝与系统盘同设备
  的驱动器（`drive is part of root drive, will not be used`）。pgcli 会用
  数据目录与 `/` 的 `stat` 做检测，在 `--data-dir` 落在根设备时于 install
  时给出警告——警告是提示性的、不阻断；请把 `--data-dir` 指向独立挂载的
  数据盘。
- **集群模式下不下发 `MINIO_SERVER_URL`。** 它在各节点本就不同（各自宣告
  自己的 IP），而 MinIO 要求所有 `MINIO_*` 环境变量逐节点字节一致，否则每台
  都会卡在 `Waiting for at least 1 remote servers with valid configuration`。
  地址由端点列表决定。单机模式仍按 `listen` 设置 `MINIO_SERVER_URL`。

**Quorum（EC）：** 写需要 `⌈N/2⌉+1` 个节点在线，读需要 `⌈N/2⌉`。因此 4 节点
集群在挂掉 2 台时仍可读、但拒绝写；重启下线的节点后集群自愈。

> **仅跨主机。** 这是真正的分布式部署——每台主机由你自己跑 pgcli。pgcli 不会
> 在节点间 SSH、也不做成员注册；保持 N 份 `pg.yaml` 一致是运维的职责。

## 使用 mc 客户端

`pg mc` 在一次性容器里运行 MinIO 自家的 `mc` 客户端——不用本地安装，也不用
手敲 `podman run`：

```bash
# 按平台选 URL —— 见下文"各平台的端点"：
pg mc alias set store http://127.0.0.1:9000 admin <密码>    # Linux，本机 addon
pg mc alias set store http://host.containers.internal:9000 admin <密码>  # macOS，本机 addon
pg mc alias set store http://10.0.5.7:9000 admin <密码>     # 两者皆可，远端存储
pg mc mb store/backups
pg mc ls store
pg mc cp ./dump.pglz store/backups/
```

别名持久化保存在宿主的 `~/.mc/config.json`——这是 `mc` 自己的默认路径，
Linux 和 macOS 都一样——`alias set` 一次，之后每次 `pg mc`（以及 Linux 上
原生安装的 `mc`）都能看到同一批别名。`pg mc alias list` 读回列表，
`pg mc alias remove` 删除单个别名。`alias set` 会先拿凭据向端点校验，通过才
写文件——密码打错不会在配置里留下一个用不了的别名。`pg mc` 底层做的事只是把
你的 `~/.mc` 挂载进容器，然后运行 `ghcr.io/mars-base/pgcli/pgcli-mc`（静态上
游二进制 + `scratch`，首次使用时自动拉取）；没有别的魔法。

`cp` / `mirror` / `diff` 的本地文件参数同样可用：`pg mc` 会把每个本地路径按
realpath 解析，并把该绝对路径原样挂载进容器，因此

```bash
pg mc cp ./dump.pglz store/backups/        # 从当前目录上传
pg mc cp store/backups/dump.pglz ./        # 下载到当前目录
pg mc mirror ./repo store/backups/         # 或镜像整棵目录树
```

所见即所得。下载到尚不存在的路径（新文件名、或还没建的 `./newdir/`）也没问
题——挂载的是它最近的已存在父目录，mc 写出的文件会落回宿主。macOS 上路径必须
位于家目录之下——那是 `podman machine` 虚拟机唯一共享的目录树；家目录之外的
路径 `pg mc` 会明确提示并跳过。

任何 `pg` 自己解析器会拒绝的 `mc` 标志都要放到 `--` 之后，与 `pg etcdctl` 的
约定完全一致。`mc` 的长标志（`--all`、`--json`、`--recursive`、`--force`、
`--newer-than` 等）pg 一个都不认识，因此全部放 `--` 之后：

```bash
pg mc ls store -- --all
pg mc cp ./dir store/backups/ -- --recursive
pg mc rm store/old -- --recursive --force
```

放在 `--` 之前会在 `pg` 这一层就被拦下，报错形如 `unknown flag: --recursive`
——看到它就说明分隔符漏了。

原生 mc 有个与本地挂载相关的注意点值得知道：在文件类命令（`cp` / `mirror` /
`diff`）里，首段不是已知别名的参数一律按本地路径处理。所以别名拼错时不会报
错，而是安静地上传/下载到当前目录下一个以拼错名字命名的本地目录——`pg mc`
只是忠实挂载了 mc 判定要读的路径，这是原生行为而非 `pg mc` 的怪癖。执行文件
操作前先用 `pg mc alias list` 确认别名存在。

`MC_HOST_<name>` 环境变量形式的别名——`mc` 的无状态用法，不碰配置文件——同样
适用于 `pg mc`，因为该变量会被转发进容器：

```bash
MC_HOST_store="http://admin:<密码>@127.0.0.1:9000" pg mc ls store
```

> **各平台的端点：** Linux 上 `pg mc` 走主机网络，`127.0.0.1` 别名可直达本机
> addon 实例。macOS 上容器位于 bridge 网络，`127.0.0.1` 是容器自己的回环——
> 指向 Mac 本机 addon 的别名请用
> `http://admin:<密码>@host.containers.internal:9000`，远端存储则用其可路由地
> 址。`pg mc` 本身在两个平台上行为一致。
>
> Mac 浏览器访问控制台走的是宿主机侧的 `127.0.0.1:<console-port>`（插件已发布
> 该端口），这与 `pg mc` 容器能不能用 `127.0.0.1` 无关——那只是宿主机自己的回环。

### 常用命令

| 命令 | 用途 |
|------|------|
| `pg mc ls store` / `pg mc ls store/backups` | 列出桶 / 对象 |
| `pg mc mb store/backups` | 创建桶 |
| `pg mc cp ./file store/backups/` | 上传（目录树用 `pg mc mirror ./dir store/backups/`，或给 `cp` 加 `-- --recursive`） |
| `pg mc cp store/backups/file ./` | 下载 |
| `pg mc find store -- --name '*.pglz'` | 按名字搜索对象 |
| `pg mc du store` | 统计各桶大小 |
| `pg mc rm store/backups/file` | 删除单个对象 |
| `pg mc rm store/prefix -- --recursive` | 批量删除对象 |
| `pg mc rb store/bucket -- --force` | 连对象一起删桶 |

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
      # endpoints:                 # 省略即单机；见"分布式 / 集群模式"
      #   - http://10.0.0.11:9000/data/minio
      #   - http://10.0.0.12:9000/data/minio
      #   - http://10.0.0.20:9000/data/minio
      #   - http://10.0.0.21:9000/data/minio
```

修改 `listen`、端口、`root_user`、`root_password`、`image_tag`、`data_dir` 或
`endpoints` 后，执行 `pg addon install minio --name store --force` 即生效——普通
install 会跳过已存在的容器，`--force` 会重建它（数据目录永不受影响）。

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
- **控制台能打开，但 S3 客户端超时。** 单机模式下 `MINIO_SERVER_URL` 由
  `listen` + API 端口拼成；若绑在 `127.0.0.1` 却从其他主机访问，客户端会被
  重定向到回环地址。把 `listen` 设为客户端真正可达的地址。（集群模式完全
  不下发 `MINIO_SERVER_URL`——见[分布式 / 集群模式](#分布式--集群模式)。）
- **集群卡在 `Waiting for at least 1 remote servers with valid
  configuration`。** 节点之间配置不一致。逐台用 `podman inspect
  pgcli-minio-<ns>-<name> --format '{{json .Config.Env}}'` 核对每份
  `pg.yaml` 的 `endpoints` 列表与 `root_password` 是否字节级一致，并确认
  没有残留的每节点 `MINIO_SERVER_URL`。
- **macOS。** 已支持：插件加入 `pgcli-net` bridge 并发布两个端口，Mac 用
  `127.0.0.1:<port>` 即可访问（容器内部绑 `0.0.0.0`，`MINIO_SERVER_URL` 宣告
  Mac 使用的回环地址）。改过端口或凭据后用 `--force` 重建容器。用 `pg mc` 时
  注意容器内 `127.0.0.1` 别名不可用——见上文端点说明。
