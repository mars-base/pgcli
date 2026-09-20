---
title: "Silo"
description: "以 pgcli 插件方式运行 silo（Pigsty 的 MinIO 分支）——单机或跨主机分布式纠删码集群的 S3 兼容对象存储（含 Web 控制台）"
weight: 48
---

[silo](https://silo.pgsty.com) 是 Pigsty 社区维护的 MinIO 分支：S3 兼容对象
存储、内置 Web 控制台，并完整保留了 MinIO 的对外契约——S3 API、`MINIO_*`
环境变量、`server /data --address :9000 --console-address :9001` 命令行、
`--certs-dir` 目录布局、以及纠删码集群模式。pgcli 将其作为**独立的顶层插件**
运行，功能面与 [`minio` 插件](../minio/) 完全一致——install、TLS、BYO 证书、
分布式模式、logs、autostart 一一对应。如果你跟随 Pigsty 的发布节奏就选 silo；
两者可以在同一台主机并存（共用一个端口池，统一游标分配，不会冲突）。

> **平台支持：** silo 插件**两个平台都支持**。Linux 上通过主机网络提供服务；
> macOS 上加入 `pgcli-net` bridge 网络并发布两个端口，Mac 用
> `127.0.0.1:<port>` 即可访问 API 与控制台。公开镜像为双架构（amd64 +
> arm64）。两个客户端——[`pg mcli`](#使用-mcli-客户端)（silo 自带）与
> [`pg mc`](../minio/#使用-mc-客户端)（MinIO 的）——也都双平台可用，且都能
> 直连 silo 存储。

## 工作原理

一个容器、一个目录：`silo server /data --address <listen>:<api-port>
--console-address <listen>:<console-port>`，其中 `/data` 是实例主机数据目录
（默认 `<base-dir>/addon/silo/<name>/data`）的 bind mount。silo 存储的一切
都在这个目录里，因此它的生命周期长于容器。

镜像是公开的官方 tag `docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z`——
双架构、控制台内置、并且连 `mcli` 客户端一起打包——所以 `pg addon install
silo` 只负责拉取；pgcli 运行时从不构建镜像（`pgcli-minio` 是自建 tag，因为
MinIO 官方镜像移除了控制台，silo 没有这个问题）。pgcli 通过显式
`--entrypoint silo` 直接驱动 `silo` 二进制，绕开镜像自带的 entrypoint 包装
脚本，与驱动 MinIO 的方式一致。

凭据的处理方式与 Patroni 相同：

- `root_user` 默认 `admin`；
- `root_password` **首次 install 时自动生成**（也可以用 `--root-password`
  显式指定），存入 `pg.yaml`
  （`addons.silo.<name>.root_password`），并在安装摘要中**打印一次**方便记录。

二者就是 silo root 的 **access key / secret key**——经由 silo 从 MinIO 继承的
`MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` 环境变量传入——所有 S3 客户端（包括
`pg mcli alias set <名称> <URL> <root_user> <root_password>`）所说的 access
key 与 secret key 就是它们。

容器按 MinIO 官方部署建议（契约原样沿用）带上 `--ulimit
nofile=1048576:1048576` 和 `--stop-timeout 60`；macOS 上 `podman machine`
虚拟机的 RLIMIT_NOFILE 上限更低，因此该平台改用 `65536`——单机 dev/test
足够。root 凭据通过 `-e` 传入会出现在 `podman inspect` 里——与手工运行容器的
暴露程度相同；对 rootless 单机部署（本插件的定位）可以接受。

## 安装

```bash
# 默认实例名 "silo"，端口从共享池分配（基址 9000），只绑回环
pg addon install silo

# 命名实例 + 显式数据目录
pg addon install silo --name store --data-dir /srv/silo

# 固定端口、指定 root 用户
pg addon install silo --name store --api-port 9000 --console-port 9001 --root-user admin

# 绑定到所有网卡，暴露到网络
pg addon install silo --name store --listen 0.0.0.0

# 用 pgcli 自签 CA 提供 HTTPS（pgBackRest S3 仓库的前提）
pg addon install silo --name store --tls

# 用你已有的证书提供 HTTPS（公共 CA 或私有 CA 签发的域名证书）
pg addon install silo --name store --tls-cert /etc/ssl/silo.test.crt --tls-key /etc/ssl/silo.test.key
```

输出会报告端点与 root 凭据：

```
✓ silo installed: "store"
  Container:    pgcli-silo-default-store
  Image:        docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z
  Data:         ~/pg/addon/silo/store/data
  S3 API:       http://127.0.0.1:9000
  Console:      http://127.0.0.1:9001

  Root user:     admin
  Root password: <generated>
```

用打印出的 root 用户和密码登录 `Console:` URL 的控制台。S3 客户端（包括
pgBackRest）指向 `S3 API:` URL，或在终端里用下文的
[`pg mcli`](#使用-mcli-客户端) 操作。

对**存活**实例重复执行 install 是 no-op：容器不会被重建（已停止的会直接启动
并给出提示），命令行参数会合并进已存储的配置，root 密码保持不变。想改端口、
监听地址、endpoint 列表或凭据，用 `--force` 重建容器——数据目录不会丢。

> **绑定地址：** 默认 `127.0.0.1` 让存储保持本地可见。`--listen 0.0.0.0`
> （或 `pg.yaml` 里的 `listen` 键）会把它暴露到网络。届时任何能连到端口的
> 人都可以尝试 root 凭据，所以只在防火墙后面、或配合 TLS（下述 `--tls`）
> 使用。

### TLS（`--tls`）

`--tls` 让 silo 以 HTTPS 提供服务。pgcli 用标准库生成自签 CA 与叶子证书——
SAN 覆盖回环名、`localhost` 和主机的所有网卡 IP——写入
`<base_dir>/tls/silo/<name>/`：`public.crt` / `private.key` 供 silo 的
`--certs-dir` 使用，`ca.crt` 用于分发。证书目录以只读方式挂载，端点 URL
变为 `https://`。

为什么需要它：**pgBackRest 对 S3 仓库强制 HTTPS**（明文是上游明确拒绝的
选项），所以用于接收 Patroni `archive-push` 的 silo 必须讲 TLS。把
`backup.repo.s3.ca_file` 指向 `ca.crt`，pgBackRest 就会做完整证书验证——见
[备份 → S3 对象存储仓库](../../backup/#s3-object-storage-repository)。该页
关于 MinIO 仓库的一切对 silo 同样逐字适用：客户端契约完全相同。

关于存储与集群配对的一点：`backup.repo.s3` 每个配置文件只有一个全局仓库，
所以*第一个*接入的 TLS 存储（无论 minio 还是 silo）会接收该环境下所有
stanza。确实需要两个目的地时，请再维护一份配置（见
[备份 → 共享备份容器](../../backup/#shared-backup-container)，以及完整示例
[HA 集群 + 自签 CA MinIO](../../ha-cluster/ha-example-minio/)）。

`pg mcli` 在命令通过回环 alias 访问 TLS 存储时自动加 `--insecure`（mcli
不会按 alias 持久化 CA 信任；同机回环下这样可接受）。指向局域网 IP 的 alias
不算回环——自己补 `-- --insecure`，或正规配置 CA。外部 `https://` 端点保持
完整验证。对已有实例切换 `--tls` 需要 `--force` 才生效。

**有效期与其他主机的访问。** CA 与叶子证书都是长生命周期；pgcli 在
`--force` 时、或主机地址变化时重新签发叶子（silo 监视证书文件并热加载重新
签名的一对，部署因此无需重建即可拿到新链），所以把 `ca.crt` 交给客户端是
每个存储一次性的动作。TLS 客户端用**自己拨出的地址**校验叶子证书的 SAN——
客户端自身的地址无关紧要。远程主机因此把 `backup.repo.s3.endpoint` 指向存储
主机的某个 IP（回环名与所有网卡 IP、含虚拟网桥，都在 SAN 里；裸主机名不在
——除非设置 `MINIO_SERVER_URL`，其 host 会被加入）。

**把 CA 拿到远程主机上——不需要 scp。** 服务端证书以叶子+CA 链的形式提供，
另一台机器上的消费方可以直接从 TLS 握手中取回根证书：

```bash
pg backup fetch-ca <store-host>:9002
#   [OK] CA fetched from <store-host>:9002
#        saved:    ~/.pgcli/backup/repo-ca/ca-<store-host>-9002.crt
#        SHA-256:  c0f0…fe2e
pg backup setup --s3-ca-file ~/.pgcli/backup/repo-ca/ca-<store-host>-9002.crt
```

此取回是 trust-on-first-use——先与存储主机上 `sha256sum
~/.pgcli/tls/silo/<name>/ca.crt` 比对指纹再信任。随后一次 `setup` 会把 CA
重新发布进 Patroni 集群的 etcd 注册表，*其他*集群主机零手工步骤即可获得
（见[备份 → S3 对象存储仓库](../../backup/)）。

### 自带证书（`--tls-cert` / `--tls-key`）

`--tls` 永远只服务 pgcli 自签的一对。要服务你已有的证书——公共 CA 为真实
域名签发的、或你的私有 CA 签发的——改用 `--tls-cert <leaf(+chain).pem>
--tls-key <key.pem>`。它隐含 `--tls`（无需重复传），取代生成的证书：两个文件
以只读方式直接挂到 silo `--certs-dir` 要求的文件名上
（`/opt/silo/certs/public.crt`、`/opt/silo/certs/private.key`），pgcli 从不
复制或重签——私钥始终只在你放置的那一个位置。

```bash
pg addon install silo --name store \
  --tls-cert /etc/ssl/wildcard.example.com.crt \
  --tls-key  /etc/ssl/wildcard.example.com.key
```

BYO 模式的其余一切——install 校验什么、为什么续期必须 `--force`（单文件
挂载锁定源 inode）、客户端如何选取信任锚、以及用 `pg cert` 生成测试证书——
与 MinIO 插件完全相同，见
[MinIO → 使用自带证书](../minio/)。`pg cert` 示例
改写如下：

```bash
pg cert --host "silo.test,127.0.0.1,10.0.0.9" \
  --cert-file silo.crt --key-file silo.key
pg addon install silo --name store --tls-cert silo.crt --tls-key silo.key
```

**关闭 BYO。** 没有 off 开关——跨重跑的 config 合并是单向的，与 `--tls`
本身一致。要回到生成模式，删除 `pg.yaml` 里该插件下的
`cert_file`/`key_file`，再 `pg addon install silo --name store --force` 重建。

## 部署形态

silo/MinIO 对自身的部署布局有明确分类；本插件支持其中最常用的两种：

| 形态 | 结构 | 适用场景 |
|------|------|----------|
| **SNSD**（单机单盘） | 单节点、单个数据目录——不给 `--endpoint` 时的默认 | 开发、测试、演示 |
| **MNSD**（多机单盘） | 多节点、每节点一块数据盘——即下文的分布式模式 | 紧凑的高可用部署 |

`pg addon install silo` 开箱即是 SNSD。要得到 MNSD，传入集群的
endpoint 列表（至少四节点）——见下文[分布式 / 集群模式](#分布式--集群模式)。

MinIO 的第三种形态 **SNMD**（单机多盘）是有意不提供的：silo（与 MinIO 一样）
直接拒绝同主机的 endpoint 列表，所以单机上的磁盘冗余应放到更低一层——数据目
录之下。[S3 存储高可用方案](../../ha-cluster/ha-s3-storage/) 讲清楚了为什么只
有这两种形态，以及如何在两种形态下各垫一层 ZFS——包括"4 主机、每主机多块
盘"这个既能扛坏盘又能扛坏机的混合形态。

## 分布式 / 集群模式

silo 的纠删码（EC）集群模式与 MinIO 完全一致——命令形状相同，规则也相同。
它要求**至少四个互不相同的 `host:port` endpoint**，且与其他插件不同，**没有
中心协调者**：每个节点各自运行 pgcli 和各自的 `pg.yaml`，每一份 `pg.yaml`
携带*相同的*完整 endpoint 列表与*相同的* root 凭据。pgcli 只为本节点的容器
执行启动——组环的握手由 silo 自己跨主机完成。

```bash
# 节点 1（10.0.0.11），独立数据盘挂载于 /data：
pg addon install silo --name store \
  --listen 10.0.0.11 \
  --data-dir /data \
  --root-password '<shared-secret>' \
  --endpoint http://10.0.0.11:9000/data \
  --endpoint http://10.0.0.12:9000/data \
  --endpoint http://10.0.0.20:9000/data \
  --endpoint http://10.0.0.21:9000/data

# 节点 2-4：同样的命令，各自的 --listen，以及 SAME 的 endpoint 列表和 SAME
# 的 root 密码。
```

三条硬性规则（endpoint 必须可路由且互不相同、数据目录必须位于与根文件系统
分离的磁盘——否则 pgcli 在 install 时告警、集群模式下不设置逐节点的
`MINIO_SERVER_URL`）以及 quorum 计算，与
[MinIO → 分布式 / 集群模式](../minio/#分布式--集群模式)相同；silo 因为继承
了这些检查，执行得同样严格。

## 使用 mcli 客户端

`pg mcli` 从 pgsty/silo 镜像（经 `--entrypoint mcli` 选择——镜像默认
entrypoint 起的是服务端）在一次性容器里运行 silo 自带的 `mcli` 客户端——本机
零安装：

```bash
# 按平台选择 URL——见 mc 页面的"按平台区分的端点"：
pg mcli alias set store http://127.0.0.1:9000 admin <password>    # Linux，本机插件
pg mcli alias set store http://host.containers.internal:9000 admin <password>  # macOS，本机插件
pg mcli mb store/backups
pg mcli ls store
pg mcli cp ./dump.pglz store/backups/
```

mcli 与 mc 的 alias 契约相同，因此互通——对 silo 存储、MinIO 存储或任何 S3
端点都可用。alias 持久化在主机 `~/.mcli/config.json`，与 mc 的
`~/.mc/config.json` **相互独立**：`pg mc` 注册的 alias 对 `pg mcli` 不可见，
反之亦然（每个客户端各设一次，或使用下面的无状态形式，两者都识别）。

`alias set` 在写入前会对端点校验凭据，密码错误时配置文件保持不变。`cp` /
`mirror` / `diff` 的本地路径参数会按真实绝对路径解析并挂载，与 `pg mc` 行为
一致（macOS 上请保持在 home 目录内）。会被 `pg` 自身解析器拒绝的 `mcli`
参数放到 `--` 之后：

```bash
pg mcli ls store -- --all
MC_HOST_store="http://admin:<password>@127.0.0.1:9000" pg mcli ls store
```

两个客户端共用 MinIO 页面记录的命令表——
[常用命令](../minio/#常用命令)——把 `pg mc` 换成 `pg mcli` 即可。

## 端口

每个实例从**一个池**中取**两个连续端口**，起点为 `minio_start_port`（默认
**9000**）：先 S3 API，后控制台。**该池与 minio 插件共享**——minio 与 silo
实例在同一游标上分配，同机并存不冲突；先 minio 表（按名称），后 silo 表：

```bash
pg addon install minio --name store    # 9000 / 9001
pg addon install silo  --name lake     # 9002 / 9003
```

## 配置

实例位于 `pg.yaml` 顶层 `addons.silo` 映射，以实例名为键：

```yaml
namespace: default
minio_start_port: 9000       # 共享池：minio 与 silo 都从这里取
addons:
  silo:
    store:
      container_name: pgcli-silo-default-store
      name: store
      image_tag: docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z
      # data_dir: /srv/silo     # 省略则为 <base-dir>/addon/silo/store/data
      listen: 127.0.0.1
      api_port: 9000
      console_port: 9001
      root_user: admin
      root_password: <generated>   # 首次 install 时写入
      autostart: false             # pg autostart enable --silo --name store
      # tls: true                  # HTTPS（自签 CA，或下面的 BYO）
      # cert_file: /etc/ssl/silo.test.crt   # BYO 叶子(+链)，隐含 tls
      # key_file:  /etc/ssl/silo.test.key   # BYO 私钥，必须与 cert_file 配对
      # endpoints:                 # 省略为单机；见"分布式 / 集群模式"
      #   - http://10.0.0.11:9000/data
      #   - http://10.0.0.12:9000/data
      #   - http://10.0.0.20:9000/data
      #   - http://10.0.0.21:9000/data
```

对 `listen`、端口、`root_user`、`root_password`、`image_tag`、`data_dir`、
`tls`/`cert_file`/`key_file` 或 `endpoints` 的修改，在下一次
`pg addon install silo --name store --force` 后生效——普通 install 会跳过仍
存在的容器，`--force` 重建它（数据目录永不触碰）。

### 查看列表

```bash
pg addon list
```

```
Infra add-ons (silo):
  silo (name: store)
    Status:      running
    Listen:      127.0.0.1
    API port:    9000
    Console port: 9001
    Console URL: http://127.0.0.1:9001/
    Data:        ~/pg/addon/silo/store/data
    Root user:   admin
    Image:       docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z
    Container:   pgcli-silo-default-store
```

`pg addon list` 永不打印密码——从 `pg.yaml` 读取。

## 启停

主机重启后，不重新应用配置地拉回实例：

```bash
pg addon start silo --name store
pg addon stop  silo --name store
```

`install` 跳过仍存在的容器（已停止的则启动）；`start` 只启动已存在的容器
（状态异常时依配置重建自愈）。TLS 生成模式下 `start` 还会重新校验、必要时
重签叶子证书（silo 热加载挂载的证书）。

## 开机自启

容器带有 `--restart unless-stopped` 策略（管崩溃，不管重启）。要在主机重启
后拉起实例：

```bash
pg autostart enable --silo --name store
```

这会设置 `autostart: true` 并安装/刷新 boot service（见
[开机自启](/docs/autostart/)）。boot 是**只启动**的。silo 独立于 PostgreSQL
栈，因此排在最后启动；无顺序约束。`pg autostart status` 列出所有目标的状态。

## 删除

```bash
pg addon remove silo --name store            # 容器删除，数据保留
pg addon remove silo --name store --clean-data   # 同时删除数据目录
```

数据目录**就是对象存储**——丢了它等于丢掉其中所有 bucket——所以 `remove`
默认保留并打印其位置。`--clean-data` 删除它（并清理默认为空的默认布局父
目录；`data_dir` 覆盖路径及其父目录永不触碰）。

## 日志

```bash
pg logs addon silo --name store      # 最后 50 行
pg logs addon silo --name store -f   # 跟踪
```

silo 输出到 stdout：启动行（`API:`/`Console:` 地址、`Documentation:`）与请求
错误。`API: http://...` 块确认监听已就绪。

## 故障排查

- **install 报 `pulling silo image ... : ...`。** 公开 tag
  `docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z` 不可达——检查到
  docker.io 的网络/registry 访问，或用 `podman pull` 预先拉取。
- **手工 `--api-port` 撞端口。** 选择自动池（`minio_start_port` 及以上）
  之外的端口，否则自动分配器会视其为已占用；该池与 minio 插件共享，所以
  silo 实例也要避开 minio 实例的显式端口，反之亦然。每个存储都需要一对
  连续端口。
- **主机重启后 `pg addon start` 无效 / 失败。** 看
  `pg logs addon silo --name store -f`——多半是数据目录被删（`--clean-data`
  或手工），silo 拒绝在曾格式化过的空目录上启动；或绑定端口变了。
- **控制台可访问但 S3 客户端超时。** `MINIO_SERVER_URL` 由 `listen` + API
  端口构成；绑 `127.0.0.1` 却从其他主机访问时，客户端会被重定向到回环
  URL。把 `listen` 设成客户端真正可达的地址。（集群模式完全不设
  `MINIO_SERVER_URL`。）
- **用 `pg mc` 设的 alias 在 `pg mcli` 里看不到（或反之）。** 设计如此——
  mc 与 mcli 的配置文件相互独立（`~/.mc` vs `~/.mcli`）。每个客户端各设一
  次 alias，或用环境变量传 `MC_HOST_<name>`。
- **macOS。** 受支持：插件在 `pgcli-net` bridge 上服务并发布两个端口，Mac
  的 `127.0.0.1:<port>` 即可访问（容器内部绑 `0.0.0.0`，
  `MINIO_SERVER_URL` 声明 Mac 使用的回环地址）。改端口/凭据后 `--force`
  重建容器。`pg mcli` 的容器不能用 `127.0.0.1` alias——见 MinIO mc 页的
  端点说明。
