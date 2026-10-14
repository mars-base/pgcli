---
title: "SeaweedFS"
description: '以 pgcli 插件方式运行 SeaweedFS（Go 语言、Apache-2.0 的 S3 兼容对象存储）——单节点 "weed mini"，或跨主机 "weed server" 集群（filer 元数据放在共享 PostgreSQL 里），原生 S3 TLS，全集群共用同一对凭据'
weight: 46
---

[SeaweedFS](https://github.com/seaweedfs/seaweedfs) 是一个 Go 语言、Apache-2.0
的 S3 兼容对象存储，其标志性特征是**以 volume 粒度管理磁盘**：数据被切成固定大
小的 *volume*（`.vol` 文件），由 master 在各 volume 服务器之间放置，从而获得
O(1) 读取和廉价的小文件存储。pgcli 将其作为**独立的顶层插件**运行，CLI 表面与
[`garage`](../garage/)、[`minio`](../minio/)、[`silo`](../silo/)、
[`rustfs`](../rustfs/) 一致（install、list、logs、autostart、password、
`pg mc`），但其运行时模型在四个方面与四者都不同，这也决定了本页的写法：

1. **一个进程，全部角色。** 单节点跑 `weed mini`——master、volume 服务器、
   filer、S3 网关四合一（官方认可的单机生产形态）。集群成员跑
   `weed server -filer -s3`，每台主机一个，同样是全部角色一起——绝不拆成四个
   容器。
2. **三个渲染出的文件，其余全走 argv。** pgcli 在实例目录下渲染 `s3.json`
   （S3 身份）以及集群场景下的 `filer.toml`（PostgreSQL 元数据存储），以只读方
   式 bind 挂载；其它配置都是 `weed` 的命令行 flag。
3. **集群元数据放在共享 PostgreSQL 里。** 集群每个节点都对外提供 filer + S3，
   所以 filer 命名空间必须是一个数据库，而不是各节点各自的 leveldb2——pgcli 让
   所有成员指向同一个 `--dsn` PostgreSQL。这正落在 pgcli 的主场：对象存储可以
   紧挨着（甚至就在）一个 pgcli 管理的实例旁边。
4. **原生 S3 TLS**——`--tls`（自签）或 `--tls-cert`/`--tls-key`（自带证书，例如
   [`pg cert`](../self-cert/) 的产物）——是 MinIO 的姿态，不是 Garage 的
   nginx 前置。开启 TLS 后，池化的 S3 端口**只服务 HTTPS**；明文监听器完全不
   绑定。

> **平台支持：** 已在 Linux（host 网络）上验证。macOS 的 bridge 路径代码完整、
> 遵循与其它对象存储一致的形态，但尚未在真机上跑过。

## 工作原理

一个实例一个容器，来自上游
`docker.io/chrislusf/seaweedfs:4.48_large_disk` 镜像（仅 pull）。pgcli 覆盖镜像
自带的 `ENTRYPOINT`，直接运行 `/usr/bin/weed -logtostderr=true <subcommand> …`：
官方 entrypoint 每次启动都会 `chown -R /data` 再降权到 uid 1000——在大目录上正
是我们想避开的"静默变慢"形态；而不带 `-logtostderr` 的话 weed 会把日志写进容器
内的文件，`pg logs` 根本看不到。

主机侧状态存放在 `<base-dir>/addon/seaweedfs/<name>/` 下：

- `data/` → `/data`（volume，单机时还有 filer 的 leveldb2 元数据）——多盘模式下
  被每盘一个 bind 挂载（`/data1`…`/dataN`）整体取代，见[多盘](#多盘)，
- `s3.json` → `/etc/seaweedfs/s3.json:ro`（S3 身份；宿主侧权限 0600），
- `filer.toml` → `/etc/seaweedfs/filer.toml:ro`（仅集群 / `--dsn`；渲染元数据
  DSN 的 `[postgres2]` 段），
- 开启 TLS 时，`cert.pem`/`key.pem` 挂到 `/etc/seaweedfs/` 下。

凭据是一对 **access key / secret key**，渲染进 `s3.json`（SeaweedFS 的静态身份
机制——`AWS_*` env 优先级最低、会被本地 store 覆盖，pgcli 从不用它）：

- **`root_user`** 是 access key id——**单节点**安装时首次自动生成
  （`SW` + 28 位 hex），或由 `--root-user` 指定。
- **`root_password`** 是 secret key（64 位 hex），自动生成或由 `--root-password`
  指定。
- 在 `--cluster` 下**两者都是必填，且每个节点必须完全一致**——见[多节点集
  群](#多节点集群)。

身份渲染时带裸 `Admin` action——在 SeaweedFS 的权限模型里它无条件短路成全权
限。所有值都落进 `pg.yaml`（`addons.seaweedfs.<name>.*`），重建才稳定。安装还会
渲染出一个桶——以实例名命名（`--bucket` 可覆盖）：单节点由 `weed mini -bucket`
启动时创建；集群在该节点健康门变绿后，通过 S3 API 用 `mc mb --ignore-existing`
播种。

## 安装

```bash
# 默认实例名 "seaweedfs"，四个端口取自 seaweedfs 池，loopback 绑定
pg addon install seaweedfs

# 命名实例 + 显式数据目录
pg addon install seaweedfs --name store --data-dir /srv/seaweedfs

# 把单节点的 volume 铺到多块独立挂载的盘上
pg addon install seaweedfs --name store --drive /mnt/sw-disk1 --drive /mnt/sw-disk2

# 固定 master 端口（volume/filer/s3 顺取后三个；gRPC 孪生口 +10000）
pg addon install seaweedfs --name store --port 8400

# 把存储暴露到网络，而不是仅 loopback
pg addon install seaweedfs --name store --listen 0.0.0.0

# 自带 access key / secret key（seaweedfs 下限：key ≥ 8，secret ≥ 16 字符）
pg addon install seaweedfs --name store --root-user SW<...> --root-password <secret>

# 原生 TLS（自签）——S3 口变 HTTPS-only
pg addon install seaweedfs --name store --tls

# 加入跨主机集群（见下文多节点集群）——凭据在这里是强制的
pg addon install seaweedfs --name store --cluster c1 --advertise-host 10.0.0.11 \
  --replication 001 --root-user SW<...> --root-password <secret> \
  --dsn "postgres://admin:<pw>@10.0.0.11:35432/seaweed_store" \
  --peer 10.0.0.11:8400 --peer 10.0.0.12:8400 --peer 10.0.0.14:8400
```

单节点输出的摘要报告端点和铸造的凭据：

```
  [OK] cluster leader: 127.0.0.1:8400.18400

✓ seaweedfs installed: "store"
  Container:     pgcli-seaweedfs-default-store
  Image:         docker.io/chrislusf/seaweedfs:4.48_large_disk
  Data:          /root/.pgcli/addon/seaweedfs/store/data
  S3 endpoint:   http://127.0.0.1:8403
  Ports:         master=8400 volume=8401 filer=8402 s3=8403 (gRPC twins +10000)
  Bucket:        store

  Access key:    SW<generated>
  Secret key:    <generated>
  Try it:        pg mc alias set store http://127.0.0.1:8403 SW<...> <secret> && pg mc ls store
```

安装以**健康门**收尾：pgcli 轮询 master 的 JSON `/cluster/status` API 最多 30 秒
才宣布成功——一个 detach + `--restart unless-stopped` 的容器否则会把错误的 argv
吞成一次崩溃循环、却显示安装成功。只有健康门绿了才进行桶播种。

对**存活**实例重跑 install 是 no-op：容器不重建（停止的会被启动并给提示），
flag 合并进存的配置，已有凭据保留。传 `--force` 才会重建容器，让改动的端口 /
监听地址 / 凭据生效——数据目录和元数据库都不会丢。

> **绑定地址：** 默认 `127.0.0.1` 保持存储本地；`--listen 0.0.0.0`（或
> `pg.yaml` 里的 `listen` 键）把它暴露到网络。**集群成员无论 `--listen` 是什么
> 都绑 `0.0.0.0`**——peer 必须能连到它的 gRPC 孪生口（`port + 10000`）做 Raft——
> 而 `-ip` 仍固定注册为那一个地址供 peer 拨号。

> **`-ip` 从不留给自动探测。** weed 会把它的 volume 注册到它自己猜出来的地址
> 下；filer/S3 的内部数据面随后去拨这个猜测地址，在一个"看起来健康"的容器上
> 上传就会 500。pgcli 永远显式传 `-ip`（单机取 `--listen` 值，集群取
> `--advertise-host`）——在一台测试主机上吃过这个坑才确认的。

### TLS

SeaweedFS 在 S3 口原生服务 HTTPS；没有 nginx 前置，也没有 web-console 那一路
需要代理。两种形态，同 minio/rustfs：

```bash
# 自签：pgcli 在实例目录下生成 cert+key（30 天叶子证书，SAN 覆盖
# bind/advertise 地址、127.0.0.1 和 localhost）
pg addon install seaweedfs --name store --tls

# 自带证书：例如 `pg cert` 签发的证书对（CA 证书会被拒绝）
pg addon install seaweedfs --name store --tls-cert fullchain.pem --tls-key privkey.pem
```

TLS 在底层改了什么（已实测）：明文监听器**关掉**（`-s3.port=0`——该池端口直接
拒绝 TCP 连接），同一个池化 S3 端口变成 `-s3.port.https`。所以安装摘要里打印的
端点是 `https://…:8403`，不是一个新端口。`pg mc` 照常指向它即可——loopback
别名会自动带 `--insecure`；真实 CA 或生成的 CA 则要交给 S3 客户端做校验。

## 多盘

单节点可以用多块盘：`weed` 自己的 `-dir` 本就是一个逗号列表
（`dir[,dir]…`），由**同一个 volume 服务器**把 volume 铺在这些目录上；pgcli 把
它暴露成可重复的 `--drive`——每块盘一个宿主目录，各自挂到自己的槽位：

```bash
# 每个 --drive 都应是一块独立挂载的盘；drives 完全取代 --data-dir（两者同时给是错误）
pg addon install seaweedfs --name store \
  --drive /mnt/sw-disk1 --drive /mnt/sw-disk2 --drive /mnt/sw-disk3
```

摘要和 `pg addon list` 会打印映射，取代单独那行 `Data:`：

```
  Drive 1:       /mnt/sw-disk1 -> /data1
  Drive 2:       /mnt/sw-disk2 -> /data2
  Drive 3:       /mnt/sw-disk3 -> /data3
```

容器因此以 `-dir=/data1,/data2,/data3` 运行（已实测：volume 服务器的 `/status`
把每个宿主目录报成独立的 `DiskStatuses` 条目、各自记账；`remove --clean-data`
也会逐个遍历 drives，并拒绝删除仍是活动挂载点的那一个）。

两点语义值得知道：

- **这是放置，不是 RAID。** 是一个 volume 服务器把 `.vol` 文件分散到自己的目录
  ——OS 层面既没有条带也没有镜像，各盘之间也**没有**纠删码（集群副本是另一件
  正交的事，见[多节点集群](#多节点集群)）。一块盘消失，它上面的 volume 就没了；
  这是否造成数据丢失取决于 placement code，而不是本地布局。
- **设备检查是建议性的。** `install` 会对任何与宿主根文件系统同设备的 drive 发
  出警告——多盘只有配独立挂载的盘才有意义。与 MinIO（会在启动时直接拒绝这种
  盘）不同，weed 照样会用它们，所以 pgcli 只警告、不阻断。请先自行格式化并挂
  载每块盘；pgcli 从不做分区或格式化。

drives 是**逐节点**的开关，并且与集群正交组合：在每台成员的 install 命令上照常
传 `--drive`，每个节点就在共享集群背后跑自己的多盘 volume 服务器（已在三主机
`weed server` 集群上实测——每台各挂两个 drive：每台进程都显示 `-dir=/data1,/data2`
且各报两个独立 `DiskStatuses` 条目；同一个 8MB volume 的 001 副本一份落在某台的
实体盘上、另一份落在副本台的**另一块** drive 上，即各节点的 volume 服务器在自己的
目录之间选择，而放置决策在节点之间进行）。各节点不需要相同的 drive 数量；每台只
看得到自己 `-dir` 列表里的目录。stop/start 与 `--force` 重建会像处理其它配置变更
一样处理 drive 集合——挂载与 `-dir` 都从 `pg.yaml` 重建。

## 多节点集群

集群是 **MinIO-MNMD 形态，不是 Garage 的 bootstrap/join**：没有"第一个节点"的
概念、没有携带集群密钥的 join 命令、也没有 layout 要 assign。你在**每台主机上
跑同一条 install 命令**——Raft 成员资格就是每个节点都传的完整 `--peer` 列表，
quorum 未齐就位的节点只是等待（其健康门报 warn 而非失败；补齐 quorum 的那次
安装完成桶播种）。

与"更大的单节点"相比有两点不同，而且这两点**天然要求全部一致**——每台成员的
命令都要原样携带：

1. **全集群一对凭据。** 每个网关只针对自己*那份*渲染出的 `s3.json` 认证；
   SeaweedFS 没有独立 IAM server 就没有共享凭据后端（超出 pgcli v1 范围），所以
   逐节点自动生成会让三个网关各自接受一份不同的身份。pgcli 因此**在
   `--cluster` 下拒绝自动生成**：每台节点传同一对 `--root-user` 和
   `--root-password`。先跑完第一个节点的 install，从打印的摘要里复制这对值，
   原样带到其余节点（pgcli 从不远程执行任何东西）。只传一半（两个里只给一
   个）是错误；把一个单节点配置加 `--cluster` 重装同样是错误。
2. **共享 filer 元数据库。** `--dsn postgres://user:pass@host:port/db` 是必填，
   且必须**从每台主机都可拨通**——所以用宿主发布端口 / LAN 地址，绝不要容器内
   部的 `127.0.0.1:5432`——数据库本身也必须已经存在：pgcli 只探测可达性，从不
   创建（也从不删除）它不拥有的数据库。元数据*表*不需要任何准备：weed 的
   `postgres2` store 在首次使用时自动在其内建 `.system`、`filemeta` 和每桶一张
   表（已实测）。`filer.toml` 里渲染的是 `sslmode=disable`，所以能连通 +
   `pg_hba` 放行就够了。

### 三主机实操

先在一个所有节点都能到的地方准备元数据库（这里用主机 1 上一个 pgcli 管理的实
例，宿主端口 35432）：

```bash
pg exec -i demo "CREATE DATABASE seaweed_store"
```

然后在**每台**主机上跑同一条命令，只换这台主机自己的 `--advertise-host`（下面的
key 是占位符——所有节点用同一对值）：

```bash
# on 10.0.0.11 — 打印出的摘要会交给你用于其它主机的命令
pg addon install seaweedfs --name store --cluster c1 \
  --advertise-host 10.0.0.11 --replication 001 \
  --root-user SW<...> --root-password <secret> \
  --dsn "postgres://admin:<pw>@10.0.0.11:35432/seaweed_store" \
  --peer 10.0.0.11:8400 --peer 10.0.0.12:8400 --peer 10.0.0.14:8400

# on 10.0.0.12 — 除 --advertise-host 外完全相同
# on 10.0.0.14 — 除 --advertise-host 外完全相同
```

想让每台成员跑自己的多盘 volume 服务器，就往同一条命令上再加 `--drive`
（`--drive /mnt/sw-disk1 --drive /mnt/sw-disk2`——见[多盘](#多盘)）；它是逐节
点的 flag，各台可以带不同的 drive 集合。已在三主机集群上实测。

摘要末尾会带一段可复制的、正好是这个形态的块，用于其余主机（凭据和 DSN 从本
节点配置里填好）。

集群还在拼装中时，先装的节点会说什么，都是*正常*的：

- 主机 1 起来时就能看到 leader（3 节点列表下，落单 master 立刻赢下自己的选举
  ——这是实测到的行为），但桶播种可能以 connection refused 失败：集群的 S3 侧
  还不可服务。pgcli 打印 `[!] bucket seeding incomplete … re-run this install
  command once the cluster has quorum`，并且**退出码 0**。
- 主机 2（或 3）补齐 quorum；同样幂等的播种在那里成功（`Bucket created
  successfully`）。事后重跑主机 1 的命令会安静复检，`mb --ignore-existing` 保持
  no-op。

三台都装完后，任意成员展示整个集群：

```
$ pg weed --name store --shell "cluster.status"

cluster:
	id:       topo
	status:   unlocked
	nodes:    3
	topology: 1 DC, 3 disks on 1 rack
…
```

### 经任意节点读写

因为凭据共享、filer 命名空间是一个数据库，**任意网关都服务任意对象**：经主机
2 的 S3 口写入、经主机 3 用同一把 key 读回——在三主机测试集群上端到端验证，包
括故意用错 secret 时返回 `The request signature we calculated does not match`
（它不是每节点各自的本地信任库）。`--replication 001` 给每个 volume 存一份远端
副本（`volume.list` 显示 `ReplicaPlacement:001`，每个 volume 落在两个数据节点
上）。

停掉一个成员，集群仍然服务（Raft quorum 是多数；3 个 master 时 2 个继续选举和
写入）：

```bash
# on host 3
pg addon stop seaweedfs --name store
# on host 1 — 经主机 2 的网关读取仍然成功
pg mc cat store2/store/roundtrip.bin | sha256sum
```

`pg addon start` 让节点重新加入；它的 volume 以同一个 `-ip` 重新注册。**成员变
更不被编排**：`--peer` 是静态 Raft 配置——增删成员意味着重建集群（v1 局限，见
[局限](#局限)）。

### 取集群的 AK/SK

这对凭据按设计存在于**每台**节点的 `pg.yaml` 里（是你带过去的），所以不像
garage 的 CRDT key——其 secret 恰好只在一台主机上——
`pg addon password seaweedfs --name store` 在任意成员上都能用。如果打印出的
secret 在所有主机上都丢了，就挑一对新值，在每台节点上用 `--force` 重装（渲染出
的 `s3.json` 会被重写；数据和元数据不受影响）。

## 用 mc 客户端

`pg mc` 从一次性容器里跑 MinIO 的 `mc`——无需本地安装——像对其它存储一样说
SeaweedFS 的 S3 API。把 **access key** 当 user、**secret key** 当 password 用：

```bash
pg mc alias set store http://127.0.0.1:8403 SW<...> <secret>   # Linux，本地
pg mc mb store/backups
pg mc cp ./dump.pglz store/backups/
pg mc ls store
```

别名持久化在 `~/.mc/config.json`；`pg` 的解析器会误吞的 flag 放到 `--` 之后
（`pg mc rm store/scratch -- --force`）。macOS 上把别名指到
`host.containers.internal:<s3-port>` 而不是 `127.0.0.1`。开 TLS 就用 `https://`
端点（loopback 别名自动 `--insecure`）。

## 用 `pg weed` 运维

`pg weed` 在一个**运行中**成员的容器里跑 SeaweedFS 自己的 `weed` 控制工具——替代
手写 `podman exec pgcli-seaweedfs-<ns>-<name> …`。`--name` 选实例（默认：按名第
一个）；pgcli 自己的 flag 必须放在子命令词之前，`weed` 的 flag 原样透传。两种
形态：

```bash
pg weed <args>...           # 原样：容器内 /usr/bin/weed <args>
pg weed --shell <line>...   # 每一行喂进 `weed shell`，已通过 loopback 预先
                            # 接好本实例的 master 和 filer
```

`weed shell` 菜单是 day-2 的表面（命令名已对 4.48 校验——shell 的 help 列得更
多）：

```bash
pg weed --shell "cluster.status"                     # 拓扑、节点、volume
pg weed --shell "volume.list"                        # 每个 volume、它的副本、大小
pg weed --shell "s3.bucket.list"                     # filer 视角下的桶
pg weed --shell "fs.ls /"                            # filer 命名空间，根
pg weed --name store --shell "volume.list" "s3.bucket.list"   # 一次多行
pg weed flag.lookup 0,1,2                            # 原样子命令
pg weed shell                                        # 交互式（需要 TTY）
```

`weed` 类 CLI 可能打印完结果还带着打开的 gRPC channel 不退出；pgcli 在其输出安
静 3 秒后（总上限 10 分钟）把这种命令切断，于是挂起暴露成一次响亮的超时——重跑
即可。

## 端口

每实例从 SeaweedFS 自己的池里取**四个连号端口**，起于 `seaweedfs_start_port`
（默认 **8400**）：master、volume、filer、s3——并且每个角色还开一个
**`http 端口 + 10000` 的 gRPC 孪生口**（8400 → 18400，……），pgcli 也把这些在
池里预留了。所以第二个实例落在 8404-8407 / 18404-18407，一个 seaweedfs、一个
garage、一个 minio 实例永不冲突（各自独立池：8400 / 3900 / 9000）：

```bash
pg addon install seaweedfs --name store   # 8400-8403 + 18400-18403
pg addon install seaweedfs --name lake    # 8404-8407 + 18404-18407
```

`--port` 只设 master 锚点；其余随之。四个里只有 S3 口是客户端端点；master 8400
还服务 `/cluster/status` JSON API 和运维 UI 页面（admin UI 和 webdav 在 argv 里
被关掉，iceberg/lance 旁路监听器钉到 0，所以 weed 不开额外的洞）。

## 配置

实例位于 `pg.yaml` 顶层的 `addons.seaweedfs` 映射：

```yaml
namespace: default
seaweedfs_start_port: 8400    # seaweedfs 自己的池 (master/volume/filer/s3, 各 4 连号, gRPC 孪生口预留)
addons:
  seaweedfs:
    store:
      container_name: pgcli-seaweedfs-default-store
      name: store
      image_tag: docker.io/chrislusf/seaweedfs:4.48_large_disk
      # data_dir: /srv/seaweedfs    # 省略则 <base-dir>/addon/seaweedfs/store/data
      # --- 多盘（取代 data_dir；与其互斥）---
      # drives:                      # 每盘一个宿主目录，挂到 /data1../dataN，
      #   - /mnt/sw-disk1            #   并把它们逗号拼接后交给 -dir
      #   - /mnt/sw-disk2
      listen: 127.0.0.1
      port: 8400                    # master 锚点；volume/filer/s3 = 后三个
      volume_port: 8401
      filer_port: 8402
      s3_port: 8403
      root_user: SW<generated>         # s3.json access key
      root_password: <generated>       # s3.json secret key
      bucket: store                    # 首次启动播种（默认 = name）
      volume_size_mb: 1024             # -master.volumeSizeLimitMB（weed 自带的 128 太小）
      autostart: false                 # pg autostart enable --seaweedfs --name store
      # --- 多节点集群键（单节点省略）---
      # cluster: c1                    # 非空 ⇒ 集群模式（weed server）
      # advertise_host: 10.0.0.11  # peer 拨号的 -ip；集群必填
      # replication: "001"             # placement 码 XYZ（X 远端副本数,
      #                                #   Y 机架数, Z 数据中心数），集群内不可变
      # peers:                         # 全量 Raft 列表，每台都写
      #   - 10.0.0.11:8400
      #   - 10.0.0.12:8400
      #   - 10.0.0.14:8400
      # filer_dsn: postgres://admin:<pw>@10.0.0.11:35432/seaweed_store  # 集群必填
      # --- TLS ---
      # tls: true                      # 自签（或 --tls-cert/--tls-key 自带）
      # cert_file: …/cert.pem
      # key_file: …/key.pem
```

对 `listen`、端口、`root_user`、`root_password`、`image_tag`、`data_dir`、
`drives`、`filer_dsn` 或 TLS 材料的改动，在下一次
`pg addon install seaweedfs --name store --force` 后生效。集群里的这次
force-recreate 必须在**每台**成员上跑（每台主机各持有自己的 `pg.yaml` 和容器）。

### 列出

```bash
pg addon list
```

```
Infra add-ons (seaweedfs):
  seaweedfs (name: store)
    Status:      running
    Cluster:     c1 (replication 001, 3 peers configured)
    Peers:       10.0.0.11:8400,10.0.0.12:8400,10.0.0.14:8400
    Leader:      10.0.0.11:8400.18400
    Filer store: postgres 10.0.0.11:35432/seaweed_store
    Listen:      127.0.0.1
    Ports:       master=8400 volume=8401 filer=8402 s3=8403
    S3 URL:      http://127.0.0.1:8403
    Data:        /root/.pgcli/addon/seaweedfs/store/data
    Access key:  SW<generated>
    Bucket:      store
    Image:       docker.io/chrislusf/seaweedfs:4.48_large_disk
    Container:   pgcli-seaweedfs-default-store
```

集群成员多出 `Cluster` / `Peers` / `Leader` / `Filer store` 四行；`Leader:` 是一
次实时的 `/cluster/status` 读（尽力而为——成员没跑就省略，集群还在拼装、尚无
leader 时打印成 `(none yet — still electing / no quorum)`）。单节点成员这四行都
没有。多盘实例则以 `Drives` 块（每个 drive 一行 `- /dataN <- 宿主路径`）取代单
独那行 `Data:`——见[多盘](#多盘)。开 TLS 时末尾追加一行 `TLS: native S3 HTTPS on 8403 (cleartext listener
off)`。`pg addon list` 从不打印 secret key；要显式暴露用
`pg addon list --show-password`（追加一行 `Secret key:`），或
`pg addon password seaweedfs --name store`（`--file <path>` 以 0600 权限写入）。
`Filer store` 行是 `host:port/db`，密码永远隐藏。

## 启停

```bash
pg addon start seaweedfs --name store
pg addon stop  seaweedfs --name store
```

`install` 跳过还在的容器（停止的会被启动）；`start` 只启动已存在的容器——并且
先重新渲染 `s3.json`/`filer.toml`，所以对存的 config 手工编辑会在重启后生效。

## 开机自启

容器带 `--restart unless-stopped` 策略（崩溃，而非重启）。要在主机重启后拉起实
例：

```bash
pg autostart enable --seaweedfs --name store
```

自启是**只启动**——渲染文件和容器都已存在。一个比 peers 先起的集群成员只是像任
何一次冷启动一样等 quorum。SeaweedFS 在上游镜像里以 root 运行，所以开机启动除了
运行 podman 本身不需要额外主机特权。

## 移除

```bash
pg addon remove seaweedfs --name store               # 容器没了，数据留着
pg addon remove seaweedfs --name store --clean-data  # 连数据目录 + 渲染文件一起删
```

数据目录**就是对象存储**——丢了它等于丢了里面所有 volume——所以 `remove` 默认
保留它。`--clean-data` 删除 data 目录、`s3.json`，以及存在时的 `filer.toml` /
TLS 目录（每个都要先 stat——缺失的可选项什么都不打印），并拒绝碰任何还是活动
挂载点的东西。**filer 元数据库永不删除**，在任何主机上、任何时候都是：移除时会
打印

```
  [i] The filer metadata database (10.0.0.11:35432/seaweed_store) was KEPT — pgcli never drops databases; drop it yourself if the data is disposable.
```

于是你清楚真要清干净时该 `DROP DATABASE` 什么（每台成员的 remove 都打同样的提
示；数据库比它们所有容器都活得久）。

## 日志

```bash
pg logs addon seaweedfs --name store      # 最后 50 行
pg logs addon seaweedfs --name store -f   # 跟随
```

## 局限

v1 有意**不做**的事：

- **集群成员变更**（增/删/替换节点）不被编排——`--peer` 是静态全列表；改它意味
  着重建集群。
- **EC 纠删码、分层（tiering）、cloud-sync（向其它 S3 复制）、FUSE 挂载，以及
  独立 IAM server**（动态 `s3.accesskey.*` 用户）都不被管理——全部仍是手工
  `pg weed` 的领地，尤其 IAM 是 SeaweedFS 本身动态共享凭据的唯一途径，这里超出
  范围。
- **凭据轮换是一次带 `--force` 在每台成员上的重装**；pgcli 从不就地编辑一个活的
  `s3.json`。
- **只播种一个桶**（实例名）；后续桶就是普通 `pg mc mb` 的事——它们自动落进同
  一个 filer 数据库。
- `filer.toml` 里的 DSN 渲染 **`sslmode=disable`**；如果你的 PostgreSQL 在这一
  条上强制 TLS，pgcli 的渲染不适用（自带 TLS 的 `--dsn` 不是 v1 表面）。

## 故障排查

- **`--cluster` 安装报 `requires explicit shared credentials`。** 这是设计上预
  期的——用显式的 `--root-user`/`--root-password` 对跑第一个节点，并把同样的值
  带到每个其余主机（安装摘要会打印填好它们的现成命令）。单节点安装可以省略它
  们、拿到自动生成的 key。
- **`requires a shared filer metadata database: pass --dsn …`。** 没有 `--dsn`
  的集群无法在各网关间维持一个命名空间。预建好数据库，传一个**主机可达**的
  URI（发布端口 / LAN 地址——容器内的 `127.0.0.1:5432` 不行）。
- **`[!] no leader yet — normal while the cluster is still assembling`。** 健康
  门的 quorum 警告，不是失败：把其余成员的 install 跑完（或等它们起来后重跑这条），
  它就绿了。
- **`[!] bucket seeding incomplete … re-run this install command`。** 晚一步的同
  一种姿态：这个节点拿到了 leader 但它的网关还不可服务。最后一台加入后重跑同一
  条 install；`mb --ignore-existing` 让它在桶已落地时是一个干净的 no-op。
- **单机健康外观下上传 500。** 历史上就是缺 `-ip` 的签名（volume 注册了一个自动
  猜的地址，数据面拨不通）；pgcli 永远传 `-ip`，若你还看到，检查有没有被手工改
  过的 argv 或一个很旧的二进制。
- **`mc: … you already own it`。** 对一个已存在的桶跑了裸 `mc mb`；pgcli 自己的
  播种用 `--ignore-existing`，所以只会从手工 `mc` 里看到这个——加上那个 flag。
- **错 secret 的困惑：`The request signature we calculated does not match`。** 这
  对值不匹配该节点渲染的 `s3.json`——在集群里，这意味着各节点装的时候用了*不同*
  的凭据；用同一对值 + `--force` 在每台重跑 install。
- **install 时 `pulling seaweedfs image … ` 失败。**
  `docker.io/chrislusf/seaweedfs:4.48_large_disk` 不可达——检查到 Docker Hub 的
  网络，或先 `podman pull` 一把。
