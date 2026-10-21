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

## 口令即数据

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
| `redis` | 改经 Redis fencing——给不支持 conditional-put 的存储（**silo** 现状如此）；`--redis-name <pgcli redis addon>` 或 `--redis-dsn redis://…` |
| `none` | 不 fencing——只在绝无他人碰这个桶时才安全 |

若 server 因存储缺少 conditional-put 支持而起不来，安装会识别日志特征并提示你
改用 `--conditional-put redis` 重试。

## 前置条件

与 JuiceFS 相同的「手动 `pg mc`」约定：pgcli **从不建桶**。它读 S3 addon 配
置、探端口，桶不存在时打印该执行的 `pg mc mb …`：

```bash
pg addon install minio --name ms3
pg mc mb ms3/zerofs-data
pg addon install zerofs --name z1 --s3 minio --s3-name ms3 --bucket zerofs-data
```

S3 凭据默认沿用后端 addon 的 root 用户/密码；`--access-key/--secret-key` 覆
盖（要么都给，要么都不给）。

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--name` | `zerofs` | 卷名（配置 map 的键） |
| `--s3` | `minio` | 后端类型——`minio` 或 `silo` |
| `--s3-name` | = `--s3` | 哪个 addon 实例做后端（已装且在跑） |
| `--bucket` | `zerofs-<name>` | 桶——pgcli 不创建 |
| `--access-key` / `--secret-key` | 后端 root 凭据 | S3 凭据覆盖 |
| `--encryption-password` | 生成 | wrap 口令；重接保留的桶时传回原口令 |
| `--mount-point` | `<base>/addon/zerofs/<name>/mnt` | 挂载的宿主目录 |
| `--cache-dir` | `<base>/addon/zerofs/<name>/cache` | 服务端磁盘缓存目录 |
| `--server-cache-gb` | 100 | 服务端缓存大小（GiB） |
| `--listen` | `127.0.0.1` | 9P 绑定地址——**必须 loopback**（9P 无认证） |
| `--port` | 5564 | 9P 端口（每个 zerofs 实例一个——强制校验） |
| `--conditional-put` | `auto` | `auto` / `redis` / `none`（见 Fencing） |
| `--redis-name` / `--redis-dsn` | `redis` | `--conditional-put redis` 的 Redis 后端 |
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
pg addon zerofs stop   --name z1     # 卸载 + 杀 server
pg addon zerofs start  --name z1
pg addon start/stop zerofs --name z1 # 顶层别名（start=server，stop=全停）
pg logs addon zerofs --name z1 -f                      # server 日志
pg logs addon zerofs --name z1 --service mount -f      # FUSE client 日志
pg addon remove zerofs --name z1     # 停 + 忘掉条目；桶和加密数据原地不动
pg addon remove zerofs --name z1 --clean-data   # 连宿主目录一起删
```

`config.toml`、server/client 日志与 pid 文件都在 `<base>/addon/zerofs/<name>/`
下；口令与 S3 密钥放在 0600 的 `config.toml` 里——同时，和 pgcli 所有 addon
凭据一样，也明文存在于你的 `pg.yaml`。

## 开机自启

```bash
pg autostart enable --zerofs --name z1
```

开机时 `pg start --autostart` 先起 9P server（等端口监听）再起 FUSE
client——排在所有对象存储 addon 之后，S3 先应答、文件系统再去够它。不重渲染、
不重生成：既有的 `config.toml` 与存的口令原样复用。
