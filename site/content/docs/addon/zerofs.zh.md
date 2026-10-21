---
title: "ZeroFS"
description: "以 pgcli 插件方式挂载加密、自维护的 S3 文件系统（宿主二进制）。"
weight: 60
---

[ZeroFS](https://github.com/Barre/ZeroFS) 是把**一切关在桶里**的 S3 文件系
统：不像 JuiceFS 依赖外部元数据引擎，它自己在对象存储上跑一套 LSM 树，且每个
对象都用卷数据密钥加密——该密钥由你的**加密口令**包裹（wrap）。快照与一致性
由后台 compaction 负责——没有要备份的数据库、没有要养活的 Redis。它经本地
9P 一跳再以 FUSE 挂载，日常使用与普通文件系统无异；而桶本身就是这个卷完整的、
加密的副本。

和 [JuiceFS](../juicefs/) 一样，ZeroFS 是**宿主二进制** addon——没有容器。
`pg addon install zerofs` 下载官方 release 二进制（sha256 校验；`PATH` 上已有
`zerofs` 则复用），自己写出卷的 `config.toml`，并运行**两个宿主进程**：

- **server**（`zerofs run -c config.toml`）——掌管 S3 与加密侧，在 **loopback**
  端口上讲 9P（默认 5564）。9P 没有认证，所以 pgcli 硬拒非 loopback 的
  `--listen`；
- **client**（`zerofs mount 127.0.0.1:<port> <mount-point>`）——你实际使用的
  FUSE 挂载，server 重启时自动重连。

> **平台**：仅 Linux（宿主 FUSE；与 BeeGFS 同样拒绝 macOS）。挂载点是普通宿主
> 目录，不会传播进 PG 实例容器。

## 一切全显式

对象存储是一个**外部** S3 服务（云，或另一台机器上的 MinIO），在命令行上点名
——pgcli 不做同主机 addon 的自动识别或凭据继承：

```bash
pg addon install zerofs --name z1 \
    --s3-endpoint http://minio.example.com:9000 \
    --access-key AK --secret-key SK \
    --bucket zerofs-data
```

- `--s3-endpoint` 是裸的 `http(s)://host[:port]`（不带桶路径、不内嵌凭据）；
  pgcli 在起 server 前先探一次，不可达的主机快速失败，而不是陷进 server 的
  重试循环。
- `--access-key`/`--secret-key` 必填，且必须成对。
- 桶从不由 pgcli 创建——用 `pg mc` 包装命令自己供给；安装时会原样打印那两条
  命令。
- ZeroFS 会对请求签名，因此 `--s3-region` 有效（可选——zerofs 自身默认
  `us-east-1`，多数 MinIO 兼容存储接受默认值）。

挂载点完全由你指定——默认落在 pgcli 的 addon 目录下，用 `--mount-point` 指到
任意位置。目录要你亲手建好（`mkdir -p`），且运行 `pg` 的用户对其有读写权限——
pgcli 从不创建挂载点：

```bash
mkdir -p /mnt/cloudfs
pg addon install zerofs --name z1 \
    --s3-endpoint http://minio.example.com:9000 \
    --access-key AK --secret-key SK \
    --bucket zerofs-data --mount-point /mnt/cloudfs
```

目录不存在或不可写时，安装（以及之后的每次 `mount`）都会在下载和启动任何东
西之前失败，并给出逐字的 `mkdir -p` 补救命令。

两个后端都可以就用 pgcli 自己的服务——先用 pgcli 建好，再作为普通的"外部
URL"显式接线（安装代码从不自动查找它们；换成云桶或远端 Redis 也一模一样）：

```bash
# MinIO 做对象存储（pgcli minio addon——silo/rustfs/garage/seaweedfs 同理；
# 桶仍要手动建，pgcli 从不建；root 用户默认 admin，除非安装时 --root-user
# 另有指定；`pg addon password minio` 裸输出 secret key，可直接内联）：
pg addon install minio --name ms3
pg mc mb ms3/zerofs-data

pg addon install zerofs --name z1 \
    --s3-endpoint http://127.0.0.1:9000 --bucket zerofs-data \
    --access-key admin --secret-key "$(pg addon password minio --name ms3)"
```

```bash
# Redis 做 fence 后端（--conditional-put redis——给缺 conditional put 的存储
# 用；pgcli 的 MinIO 并不需要，这里只演示接法）。`pg addon password redis`
# 裸输出随机口令，手工拼进 URL，/N 选逻辑库：
pg addon install redis --name fence
pg addon password redis --name fence

pg addon install zerofs --name z2 \
    --s3-endpoint http://silo.example.com:9000 \
    --access-key AK --secret-key SK --bucket zerofs-fenced \
    --conditional-put redis --redis-dsn "redis://:PASS@127.0.0.1:6379/0"
```

以上示例中 `pg` 与存储同机，loopback（默认 `127.0.0.1`）即可。zerofs 客户端
在别的机器上时，把存储发布到网络、`--s3-endpoint` 指向它的地址——端点写在
`config.toml` 里，之后要改就 `--force` 重渲染：

```bash
pg addon install minio --name ms3 --listen 0.0.0.0   # 已装过则加 --force
# 在另一台机器上：
pg addon install zerofs --name z1 --s3-endpoint http://10.0.0.5:9000 …
```

（redis 默认就绑 `0.0.0.0`——口令即门禁。注意这里说的只是 S3/redis 后端：
zerofs 自己的 9P `--listen` 必须保持 loopback——9P 没有认证。）

## 口令即数据

> ⚠️ **🔑 `pg addon remove` 会销毁口令的唯一副本——动手前先存好。** `remove`
> 保留桶，但删除配置条目，而口令只活在那个条目和它的 `config.toml` 里。没有
> 原口令，被保留的桶就永远无法解读——不存在任何恢复途径。任何将来可能反悔的
> `remove` 之前：`pg addon password zerofs --name z1 > /某处安全的地方`（放在
> pgcli 目录之外）。

加密口令**包裹着桶的数据加密密钥（DEK）**。首次安装自动生成——随时取回：

```bash
pg addon password zerofs --name z1
```

丢了它，桶里的内容就无法恢复。这对 `pg addon remove` 尤其关键：它**刻意保留
桶**，但配置条目（连同口令的副本）没了——于是朴素的 reinstall 会生成*新*口
令，无法 unwrap *旧* DEK，server 起不来并报 `Invalid password or corrupted
key`。要重新接上保留的桶，把原口令传回去：

```bash
pg addon install zerofs --name z1 --encryption-password '<the stored one>'
```

（真要推倒重来：清空桶，或换一个 `--bucket`。）

## Fencing：conditional put

ZeroFS 用 S3 **conditional put**（`If-None-Match`）对并发写者做 fencing。
`--conditional-put` 三选一：

| 模式 | 含义 |
|---|---|
| `auto`（默认） | 用存储原生 `If-None-Match`——需要 MinIO RELEASE.2022-08+（pgcli 默认镜像满足）或真 S3 |
| `redis` | 改经 Redis fencing——给不支持 conditional-put 的存储（如 silo）；必须显式给 `--redis-dsn redis://…`（无同主机自动解析） |
| `none` | 不 fencing——只在绝无他人碰这个桶时才安全 |

若 server 因存储缺少 conditional-put 支持而起不来，安装会识别日志特征并提示你
改用 `--conditional-put redis --redis-dsn …` 重试。

`--redis-dsn` 就是一个普通的 `redis://` URL——DSN 会原样写进 `config.toml`，
任何 Redis 都行，包括 pgcli 自己的 redis addon（接法见上文示例；`pg addon
password redis` 裸输出随机口令，`/N` 选逻辑库）。TLS 端点与 pgcli 其他处一
致：`rediss://user:pass@redis-host:6380/1`。

9P 端口每个实例独占：bind 撞车的 zerofs server 会静默启动、什么都不监听，所
以 install 和 `start` 对已被占用的 `--port` 直接拒绝——多卷并存请各给一个端口。

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--name` | `zerofs` | 卷名（配置 map 的键） |
| `--s3-endpoint` | — | 必填。外部 S3 服务，形如 `http(s)://host[:port]` |
| `--s3-region` | zerofs 自身的 `us-east-1` | 端点的 SigV4 region |
| `--bucket` | `zerofs-<name>` | 桶——pgcli 不创建 |
| `--access-key` / `--secret-key` | — | 必填且成对。S3 凭据 |
| `--encryption-password` | 生成 | wrap 口令；重接保留的桶时传回原口令 |
| `--mount-point` | `<base>/addon/zerofs/<name>/mnt` | 挂载的宿主目录——pgcli 从不创建：先 `mkdir -p` 建好，且运行 `pg` 的用户可写 |
| `--cache-dir` | `<base>/addon/zerofs/<name>/cache` | 服务端磁盘缓存目录 |
| `--server-cache-gb` | 100 | 服务端缓存大小（GiB） |
| `--listen` | `127.0.0.1` | 9P 绑定地址——**必须 loopback**（9P 无认证） |
| `--port` | 5564 | 9P 端口（已有应答者时直接拒绝——每个 zerofs 实例一个） |
| `--conditional-put` | `auto` | `auto` / `redis` / `none`（见 Fencing） |
| `--redis-dsn` | — | Redis fence 后端，`--conditional-put redis` 时必填 |
| `--install-dir` | `/usr/local/bin` | 宿主二进制落点（不可写退到 `~/.local/bin`） |
| `--version` | 内置 pin（`2.3.5`） | 要下载的版本（摘要已 pin 在 pgcli 源码里） |
| `--no-mount` | 关 | 写好配置、起好 server，但不挂载 |
| `--force` | 关 | 重渲染 `config.toml` 并重启——**保留存的加密口令**（新口令无法 unwrap 既有数据） |

FUSE（`/dev/fuse` + `fusermount3`）只探测、不安装。

## 日常管理

```bash
pg addon zerofs status --name z1     # server 进程、挂载状态、文件位置、fence 模式
pg addon zerofs umount --name z1     # 拆掉 FUSE client
pg addon zerofs mount  --name z1     # 重新挂上（server 需在跑）
pg addon zerofs mount  --name z1 --mount-point /mnt/cloudfs
                                     # 自定义路径（先 mkdir -p）；未挂载时这
                                     # 样挂会顺带永久迁移该卷的挂载点
pg addon zerofs stop   --name z1     # 卸载 + 杀 server
pg addon zerofs start  --name z1
pg addon start/stop zerofs --name z1 # 顶层别名（start=server，stop=全停）
pg logs addon zerofs --name z1 -f                      # server 日志
pg logs addon zerofs --name z1 --service mount -f      # FUSE client 日志
pg addon remove zerofs --name z1     # 停 + 忘掉条目；桶和加密数据原地不动
                                     # 🔑 先存口令：pg addon password zerofs --name z1
pg addon remove zerofs --name z1 --clean-data   # 连宿主目录一起删
```

`config.toml`、server/client 日志与 pid 文件都在 `<base>/addon/zerofs/<name>/`
下；口令与 S3 密钥放在 0600 的 `config.toml` 里——同时，和 pgcli 所有 addon
凭据一样，也明文存在于你的 `pg.yaml`。

换挂载路径的做法与 [JuiceFS](../juicefs/) 完全一致：先 `umount`，用
`mkdir -p` 把新目录建好（可写——与安装契约相同），再带覆盖参数挂载；新路径
会持久化进 `pg.yaml`，之后的普通 `mount`（含 autostart）都用它：

```bash
pg addon zerofs umount --name z1
mkdir -p /srv/cloudfs
pg addon zerofs mount --name z1 --mount-point /srv/cloudfs
#   [i] mount point moved to /srv/cloudfs (saved)
# ✓ zerofs "z1" mounted at /srv/cloudfs
```

仍在挂载时尝试迁移会被拒绝并提示先 `umount`；FUSE 挂载点与 pgcli 的簿记完全
解耦（9P server 照常跑在自己的端口上，`config.toml`/日志/pid 始终在
`<base>/addon/zerofs/<name>/`），迁移从不触碰卷本身。

## 开机自启

```bash
pg autostart enable --zerofs --name z1
```

开机时 `pg start --autostart` 先起 9P server（等端口监听）再起 FUSE
client。后端是外部服务——只有 S3 端点重新可达时开机才能成功。不重渲染、不重
生成：既有的 `config.toml` 与存的口令原样复用。
