---
title: "rustfs"
description: "以 pgcli 插件方式运行 rustfs（Rust 实现的 S3 兼容对象存储）——单机或纠删码多盘/多节点，固定的容器 uid 由定制镜像在内部消化"
weight: 47
---

[rustfs](https://github.com/rustfs/rustfs) 是 Rust 重新实现的 S3 兼容对象存
储：同样的 S3 API、Web 控制台、纠删码多盘/多节点布局——但运行时模型与
MinIO/silo 完全不同。pgcli 将其作为**独立的顶层插件**运行，CLI 表面与
[`minio`](../minio/)、[`silo`](../silo/) 一致（install、TLS、BYO 证书、
drives、logs、autostart），共用同一个端口池；但有三点决定了本页的写法：

1. **固定的容器用户。** 上游 rustfs 在镜像里写死 `User=rustfs`（uid/gid
   10001）。pgcli 把这件事**完全放在自家镜像内部**处理——见下节
   [权限与属主](#权限与属主)——所以 rustfs 存储不需要任何主机侧属主处理。
2. **只有三种拓扑，没有多节点单盘。** rustfs 只支持 SNSD / SNMD / MNMD——
   见[部署模式](#部署模式)。
3. **自己的 TLS 文件名。** rustfs 从 `RUSTFS_TLS_PATH` 读取
   `rustfs_cert.pem` / `rustfs_key.pem`，而不是 MinIO 的 `public.crt` /
   `private.key`。

想要一个精简、Rust 原生的 S3 端点就选 rustfs；它可与 minio、silo 在同一台
主机并存（三者共用一个端口池）。

> **平台支持：** rustfs 插件**实际上是仅 Linux**。其运行时模型（固定容器
> uid、独立块设备上的盘、host 网络）是 Linux 容器路径，也只在 Linux 上做过验
> 证；macOS 的 bridge 路径代码完整、wrapper 镜像也是双架构，但尚未在真机上跑
> 过。

## 工作原理

一个实例一个容器。数据存放在 bind 挂载的主机目录里（默认
`<base-dir>/addon/rustfs/<name>/data`，或 `--drive` 的每个盘各自一个目录），
生命周期长于容器。与 minio/silo 不同，pgcli **不**直接驱动 `rustfs` 二进制
——它运行镜像自带的 `/entrypoint.sh`（经下面说的 wrapper 转发），因为正是这
个 entrypoint 会展开 `RUSTFS_VOLUMES` 里的多盘花括号范围、创建各盘目录、并
组装服务端 argv。

pgcli 拉取的镜像不是纯上游镜像——是
`ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0`，一个建立在
`docker.io/rustfs/rustfs:1.0.0`（首个 GA 版本）之上的精简 pgcli wrapper。
`pg addon install rustfs` 只负责 pull；pgcli 从不在运行时构建它。tag 跟随被
pin 的上游版本。

凭据处理方式与 minio/silo 相同：

- `root_user` 默认 `admin`（rustfs 对 access key 无最小长度要求，任意非空值
  都可以；`admin` 还顺带避开了对 `rustfsadmin` 的告警）；
- `root_password` **首次 install 时生成**（或用 `--root-password` 显式提
  供），存于 `pg.yaml`（`addons.rustfs.<name>.root_password`），并在 install
  摘要里**只打印一次**。

它们是 rustfs 的 root access key / secret key，通过 `RUSTFS_ACCESS_KEY` /
`RUSTFS_SECRET_KEY` 环境变量传入——即每个 S3 客户端（含 `pg mc alias set`）
口中的 access key 与 secret key。

## 权限与属主

这是 rustfs 与 minio/silo 真正不同的地方，而 pgcli 已把它收进镜像、在实际
使用中变得不可见。

上游 rustfs 以一个**固定的、不可配置的 uid/gid（10001）**运行。在 rootless
podman 下，主机用户对这个 uid 没有任何权限主张，所以让一个 bind 挂载的数据
目录对该进程可写的「朴素做法」是在主机侧做 `chown` 腾挪
（`podman unshare chown 10001:10001 …`）——脆弱，而且和操作者是否为 root 与
其说相关。pgcli 完全绕开了这一切：

- wrapper 镜像的 entrypoint 以**容器内 root** 启动，把它自己的 bind 挂载数据
  目录 `chown` 成 10001，然后 `su` 降到 `rustfs` 用户，再 `exec` **未做任何
  改动的上游** `/entrypoint.sh`。pgcli 不传 `--user` flag，也**完全不做主机
  侧的属主操作**。
- 两种 daemon 模式下的效果一致；只有**主机可见的**数字 uid 不同，因为那是
  podman 用户命名空间的属性，与 pgcli 无关：
  - **rootful** podman → 目录落在真实的主机 uid `10001`；
  - **rootless** podman → 落在*附属*（subordinate）主机 uid
    （`subuid_start + 10001`，例如 `110001`），`10001` 只在容器内可见。

两种情形都不需要操作者以 root 运行、也不需要手工 `chown`。唯一还值得知道的
约定是：当你自己挂载盘（`--drive`）时，挂载点应归运行 `pg` 的那个用户所有
——rootless podman 下 root 拥有的挂载点落在命名空间 uid 范围之外，容器内的
`chown` 无法认领它，跟任何 bind mount 一样。

**TLS 是拷贝，不重新属主。** rustfs 必须以 10001 身份读它的 key+cert，但
pgcli 生成的证书目录（0700、pgcli 拥有）**必须**继续由 pgcli 拥有，`pg cert`、
CA 刷新、`pg backup fetch-ca` 都要在它上面工作。所以 wrapper **不** `chown`
那个目录：它以**只读**方式挂载该目录，然后以容器内 root 身份把那两个必需文件
**拷贝**到一个新建的、由 10001 拥有的容器本地目录——`RUSTFS_TLS_PATH` 指向
那里。任何主机文件都不会被改动；BYO 路径走同一套拷贝机制，BYO 的 key/cert
同样只读挂载、按所需文件名被拷贝、也从不被 chown。

## 安装

```bash
# 默认实例名 "rustfs"，端口取自共享池，loopback 绑定
pg addon install rustfs

# 指定名字 + 显式数据目录
pg addon install rustfs --name store --data-dir /srv/rustfs

# 固定端口、换 root 用户
pg addon install rustfs --name store --api-port 9000 --console-port 9001 --root-user admin

# 让存储暴露在网络而不是仅本机
pg addon install rustfs --name store --listen 0.0.0.0

# 走 pgcli 自签 CA 的 HTTPS（作为 pgBackRest S3 仓库的前提）
pg addon install rustfs --name store --tls

# 用你已经有的证书（公网 CA 或私有 CA）
pg addon install rustfs --name store --tls-cert /etc/ssl/rustfs.test.crt --tls-key /etc/ssl/rustfs.test.key
```

输出会报告端点和 root 凭据：

```
✓ rustfs installed: "store"
  Container:    pgcli-rustfs-default-store
  Image:        ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
  Data:         ~/pg/addon/rustfs/store/data
  S3 API:       http://127.0.0.1:9000
  Console:      http://127.0.0.1:9001

  Root user:     admin
  Root password: <generated>
```

在 `Console:` URL 用打印出的 root 用户/密码登录控制台。把 S3 客户端（含
pgBackRest）指向 `S3 API:` URL，或用 [`pg mc`](../minio/#使用-mc-客户端) 直
接在终端操作。

对一个**在跑的**实例重跑 install 是 no-op：容器不会被重建（停着的会被启动并
有提示），flag 会合并进已存配置，已有的 root 密码保留。传 `--force` 才重建
容器，从而让改动的端口、监听地址、端点列表或凭据生效——数据目录不受影响。

> **绑定地址：** 默认 `127.0.0.1` 让存储只在本机可达。`--listen 0.0.0.0`
> （或 `pg.yaml` 里的 `listen` 键）会把它暴露到网络上。能访问该端口的任何人
> 都可尝试 root 凭据，所以要么放在防火墙后、要么开 TLS（`--tls`）。

### TLS（`--tls`）

`--tls` 让 rustfs 提供 HTTPS。pgcli 用标准库生成一个自签 CA 与一张叶子证书
——SAN 覆盖 loopback 名称、`localhost` 以及主机的每张网卡 IP——写入
`<base_dir>/tls/rustfs/<name>/`：`rustfs_cert.pem` / `rustfs_key.pem`（rustfs
要求的名字，同时另存一份 `public.crt` / `private.key` 便于 CA 工具复用）与用
于分发的 `ca.crt`。证书目录以只读挂载、并如[权限与属主](#权限与属主)所述被
拷进容器；端点 URL 变成 `https://`。

为什么需要它：**pgBackRest 对 S3 仓库强制 HTTPS**，所以一个要接收 Patroni
`archive-push` 的 rustfs 必须支持 TLS。`backup.repo.s3.ca_file` 指向
`ca.crt`，pgBackRest 就会带完整证书校验连接——见[备份 → S3 对象存储仓
库](../../backup/#s3-对象存储仓库)。该页关于 MinIO 仓库的一切同样适用于
rustfs：S3 契约完全一致（path-style、`repo1-s3-uri-style=path`）。

**把 CA 送到远端主机——无需 scp。** 服务端证书以 leaf+CA 链的形式被提供，
所以另一台机器上的消费方可以直接从一次 TLS 握手里抽出根证书：

```bash
pg backup fetch-ca <store-host>:9000
#   [OK] CA fetched from <store-host>:9000
#        saved:    ~/.pgcli/backup/repo-ca/ca-<store-host>-9000.crt
#        SHA-256:  c0f0…fe2e
pg backup setup --s3-ca-file ~/.pgcli/backup/repo-ca/ca-<store-host>-9000.crt
```

fetch 是 trust-on-first-use——信任前先把打印出的 SHA-256 与存储主机上的
`sha256sum ~/.pgcli/tls/rustfs/<name>/ca.crt` 比对一下。

### 自带证书（`--tls-cert` / `--tls-key`）

`--tls` 只会服务 pgcli 自家的自签证书对。若要服务你已经持有的证书，传
`--tls-cert <leaf(+chain).pem> --tls-key <key.pem>`。配合 `--tls`（隐含）这
会取代生成的证书：两个文件以只读挂载，并按 rustfs 要求的名字被拷进容器
（`/opt/rustfs/certs/rustfs_cert.pem`、`…/rustfs_key.pem`）；pgcli 既不重新
属主、也不写它们的源目录。

```bash
pg addon install rustfs --name store \
  --tls-cert /etc/ssl/wildcard.example.com.crt \
  --tls-key  /etc/ssl/wildcard.example.com.key
```

BYO 模式的其余一切——为什么续期需要 `--force`（单文件挂载会钉住宿主源
inode）、客户端如何挑选信任锚、`pg cert` 作为测试证书铸造——与 MinIO 插件完
全一致，见 [MinIO → 自带证书](../minio/)。**关掉 BYO：** 从 `pg.yaml` 里删
掉这个插件下的 `cert_file`/`key_file`，再 `pg addon install rustfs --name
store --force` 重建。

## 部署模式

rustfs 有**三**种布局——没有多节点单盘模式：

| 模式 | 形态 | 适用场景 |
|------|------|----------|
| **SNSD**（单节点单盘） | 一个节点、一个数据目录——不给 `--endpoint`/`--drive` 时的默认 | dev、test、demo |
| **SNMD**（单节点多盘） | 一个节点、多块盘——每块一个 `--drive`，见下文 [SNMD](#单节点多盘snmd) | 单主机上抵抗磁盘故障 |
| **MNMD**（多节点多盘） | 多节点、每节点多盘——本节点的 `--drive` + `--endpoint` 列表 | 同时抵抗磁盘故障与节点故障 |

rustfs 有意**没有 MNSD**（多节点单盘）。只给 `--endpoint` 不给 `--drive` 会在
install 期被拒：rustfs 自己派生每盘 volume 范围，所以一个分布式节点必须声明
自己有几块盘。

> **rustfs 严格要求物理盘互斥。** 在 SNMD/MNMD 下，每个 `--drive` 必须位于自
> 己的块设备上。若两块盘共享一个设备（同一 `st_dev`），rustfs 进程启动即
> `[FATAL]` 退出。pgcli 以分离方式启动容器，所以 `pg addon install` 仍返回
> 0，故障表现为一个永远变不健康的容器——若多盘 install 起不来，查
> `pg logs addon rustfs --name store`。（这比 MinIO/silo 更严，后者只告警。）

## 单节点多盘（SNMD）

一个 rustfs 进程、若干主机目录、在它们之间纠删码。用 `--drive` 一次一块盘替
代 `--data-dir`：

```bash
pg addon install rustfs --name store \
  --drive /mnt/rustfs/disk1 --drive /mnt/rustfs/disk2 \
  --drive /mnt/rustfs/disk3 --drive /mnt/rustfs/disk4 --tls
```

每个 `--drive` 是位于独立设备上的一个主机目录；第 *N* 块盘（0-indexed）被
bind 挂到容器路径 `/data/rustfsN`，`RUSTFS_VOLUMES` 被设为花括号范围
`/data/rustfs{0...3}`，由镜像的 `/entrypoint.sh` 展开成各个独立目录。
`--drive` 与 `--data-dir` 互斥；把 `--drive` 和 `--endpoint` 组合就是 MNMD。

`pg addon remove rustfs --name store --clean-data` 会删除每个 drive 目录
——但会拒删仍然处于挂载状态的盘，这样一次手滑的 `--clean-data` 绝不会穿过
活挂载点 `rm -rf` 到下面的真实磁盘。若确实要清数据，先卸载。

## 分布式 / 集群模式（MNMD）

每个节点跑各自的 pgcli 与各自的 `pg.yaml`；每份 `pg.yaml` 带**同一份**完整
的 endpoint 列表与**同一套** root 凭据。对 rustfs 而言，`--endpoint` 列表是
**每节点一条 `scheme://host:port`**（不带 path——rustfs 从 `--drive` 自己派生
`/data/rustfsN` volume 范围），每节点再各传各的盘：

```bash
# node 1（10.0.0.11），四块独立数据盘：
pg addon install rustfs --name store \
  --listen 10.0.0.11 \
  --root-password '<shared-secret>' \
  --drive /mnt/rustfs/d1 --drive /mnt/rustfs/d2 --drive /mnt/rustfs/d3 --drive /mnt/rustfs/d4 \
  --endpoint http://10.0.0.11:9000 --endpoint http://10.0.0.12:9000 \
  --endpoint http://10.0.0.20:9000 --endpoint http://10.0.0.21:9000

# node 2-4：同样的命令、自己的 --listen/--drive，外加相同的 --endpoint 列表
# 与相同的 --root-password 值。
```

跨主机 MNMD 代码完整、并在 install 期做校验；不过本页的 e2e 覆盖只在真实单
主机上跑了 SNSD 与 SNMD。上面的四节点环是按接线与 CLI 校验写出的，不是实测
集群——在你亲自跑过之前，请如此看待。

## 使用 mc 客户端

`pg mc` 在一次性容器里跑 MinIO 的 `mc` 客户端，对 rustfs 的 S3 API 与其它
存储一样可用：

```bash
pg mc alias set store http://127.0.0.1:9000 admin <password>    # Linux
pg mc mb store/backups
pg mc ls store
pg mc cp ./dump.pglz store/backups/
```

S3 数据面（PUT/GET）经 pgBackRest 与一次基础 `mc` 往返对 rustfs 已端到端证
实，其中包括一段完整的 PITR 流程——全量 base backup、WAL 的 `archive-push`、
以及一次 `--time` 恢复：从 rustfs 取回归档 WAL 回放并精确停在目标时间
（Linux，rootless podman 实测）。如实标注：*管理面* 的 `mc` 命令（alias set
校验、bucket policy、admin 系列）尚未对 rustfs 逐一验证。把它当作纯对象
put/get 用，理解某些 MinIO 专属的管理命令未必在 rustfs 上有对应。

## 端口

每个实例从 `minio_start_port`（默认 **9000**）起的同一个池里取**两个连续端
口**：先 S3 API、后 console。**这个池在 minio、silo、rustfs 之间共享**——三
者由同一个游标分配，故在同一主机并存不冲突（先按名字排 minio，再 silo，再
rustfs）：

```bash
pg addon install minio  --name store   # 9000 / 9001
pg addon install silo   --name lake    # 9002 / 9003
pg addon install rustfs --name archive # 9004 / 9005
```

## 配置

实例位于 `pg.yaml` 顶层的 `addons.rustfs` 映射里：

```yaml
namespace: default
minio_start_port: 9000       # 共享池：minio、silo 与 rustfs 都从这里取
addons:
  rustfs:
    store:
      container_name: pgcli-rustfs-default-store
      name: store
      image_tag: ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
      # data_dir: /srv/rustfs    # 省略则为 <base-dir>/addon/rustfs/store/data
      # drives:                  # 多盘（SNMD/MNMD）：每块一个主机目录，
      #   - /mnt/rustfs/d1       # 挂到 /data/rustfs0../rustfsN
      #   - /mnt/rustfs/d2
      listen: 127.0.0.1
      api_port: 9000
      console_port: 9001
      root_user: admin
      root_password: <generated>   # 首次 install 写入
      autostart: false             # pg autostart enable --rustfs --name store
      # tls: true                  # 提供 HTTPS（自签 CA，或下方的 BYO）
      # cert_file: /etc/ssl/rustfs.test.crt   # BYO leaf(+chain)，隐含 tls
      # key_file:  /etc/ssl/rustfs.test.key   # BYO 私钥，须与 cert_file 配对
      # endpoints:                 # 单节点省略；MNMD 每节点一条：
      #   - http://10.0.0.11:9000
      #   - http://10.0.0.12:9000
```

改动 `listen`、端口、`root_user`、`root_password`、`image_tag`、`data_dir`、
`tls`/`cert_file`/`key_file` 或 `endpoints`，需在下一次
`pg addon install rustfs --name store --force` 后生效。

### 列表

```bash
pg addon list
```

```
Infra add-ons (rustfs):
  rustfs (name: store)
    Status:      running
    Listen:      127.0.0.1
    API port:    9000
    Console port: 9001
    Console URL: http://127.0.0.1:9001/
    Data:        ~/pg/addon/rustfs/store/data
    Root user:   admin
    Image:       ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
    Container:   pgcli-rustfs-default-store
```

`pg addon list` 从不打印密码——去 `pg.yaml` 读。健康端点是 `GET /health`
（返回 `200`），不同于 MinIO 的 `/minio/health/live`。

## 启停

```bash
pg addon start rustfs --name store
pg addon stop  rustfs --name store
```

`install` 会跳过仍在的容器（停着的则启动）；`start` 只启动已存在的容器（并
在状态异常时从配置重建以自愈）。在 TLS 生成模式下 `start` 还会重新校验并按
需重签叶子证书。

## 开机自启

容器带 `--restart unless-stopped` 策略（崩溃重启，而非开机自启）。要在主机
重启后拉起实例：

```bash
pg autostart enable --rustfs --name store
```

rustfs 的固定容器 uid 在 pgcli 的 wrapper 镜像内部处理，所以开机时的启动除
了运行 podman 本身不需要任何特殊主机权限。开机是**只启动**；rustfs 独立于
PostgreSQL 栈，故最后启动。

## 移除

```bash
pg addon remove rustfs --name store            # 容器移除，数据保留
pg addon remove rustfs --name store --clean-data   # 同时删除数据目录
```

数据目录**就是对象存储**——丢了它就丢了里面所有 bucket——所以 `remove` 默
认保留它。`--clean-data` 才删除（并清理默认布局下已空的父目录）。在
rootless podman 下，对象归一个附属 uid 所有、普通 `rm` 无法 unlink，因此
`--clean-data` 会透明地回退到 `podman unshare rm` 来回收它们。

## 日志

```bash
pg logs addon rustfs --name store      # 最近 50 行
pg logs addon rustfs --name store -f   # 跟踪
```

## 故障排除

- **install 时 `pulling rustfs image ... : ...`。** wrapper tag
  `ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0` 不可达——检查到 ghcr.io 的网络
  访问，或用 `podman pull` 预拉。
- **多盘 install 起不来（永不健康）。** 两块盘共享一个物理设备时 rustfs
  `[FATAL]`。查 `pg logs addon rustfs --name store` 看磁盘检查是否触发，并确
  保每个 `--drive` 在独立设备上（独立磁盘或各自的 loop 挂载）。
- **rootless podman 写不进某个盘。** 该盘的挂载点归 root 所有、落在命名空间
  uid 范围之外，容器无法认领。把挂载点 `chown` 给运行 `pg` 的用户。
- **手工 `--api-port` 撞端口。** 池在 minio/silo/rustfs 之间共享，一个显式端
  口必须被三者的自动分配器都视为已占用。每个存储需要一对连续端口。
- **`--clean-data` 落了个目录没删。** rootless podman 下的附属 uid 目录树经
  `podman unshare rm` 回退删除；若该路径不可用，自己动手
  `podman unshare rm -rf <dir>`。
