---
title: "JuiceFS"
description: "以 pgcli 插件方式把 S3 后端 POSIX 文件系统挂载为本地目录（宿主二进制）。"
weight: 59
---

[JuiceFS](https://juicefs.com/) 把真正的 POSIX 文件系统铺在对象存储之上：文件
数据切块后存进 S3 桶，另由一个**元数据引擎**（SQLite、Redis、PostgreSQL……）
保存命名空间、文件属性与块映射。你拿到的是一个可以 `ls`、`cat`、`grep` 的普通
目录——每次写入最终都化作桶里不可变的对象。「想要文件系统、但字节应该住在
S3」就是它解决的问题：快照近乎免费（对象不可变）、容量就是桶的容量、任何知道
元数据 URL 的主机都能把同一个桶再挂起来。

作为 pgcli 插件，JuiceFS 是第一个**宿主二进制**形态的 addon：没有容器。
`pg addon install juicefs` 从 GitHub releases 下载官方二进制（sha256 校验；
`PATH` 上已有 `juicefs` 则直接复用），向后端的 pgcli **S3 addon**（已装好的
`minio`，或 `silo`）要连接信息，可以在 pgcli 的 **PG 实例**上顺手建元数据库、
或复用 pgcli 的 **redis** addon，然后 format 卷并完成挂载。

> **平台**：仅 Linux（与 BeeGFS 一样明确拒绝 macOS）。挂载点是一个普通**宿主**
> 目录——只对宿主进程可见，不会传播进 PG 实例容器（v1 不做 rshared 挂载传播；
> addon 管理的是本机的挂载，不是集群的）。

## 前置条件

桶**从不由 pgcli 创建**——对象存储的供给留在你手里，用 `pg mc` 这个包装命令
（和所有 S3 addon 一样的手动流程）。pgcli 只读 S3 addon 的配置、探它的端口，
桶不存在时打印出该执行的那条 `pg mc mb …`。最小流程：

```bash
pg addon install minio --name ms3        # S3 后端（保持运行）
pg mc mb ms3/juicefs-data                # 手工建卷用的桶
pg addon install juicefs --name data --s3 minio --s3-name ms3 --bucket juicefs-data
```

凭据默认沿用后端 addon 的 root 用户/密码（pgcli 本来就存着；JuiceFS 在
format 时把它们抄进元数据，此后再挂载完全不需要 S3 凭据）。
`--access-key/--secret-key` 可覆盖——比如换成你用 `pg mc` 建的按卷授权的服务
账号。

## 元数据引擎（`--meta`）

| 引擎 | 参数 | pgcli 的行为 |
|---|---|---|
| `sqlite3`（默认） | — | 元数据文件放在 addon 目录下；仅限单机 |
| `redis` | `--redis-name cache` / `--redis-dsn redis://…` | 解析成 pgcli **redis** addon 实例（必须已安装——pgcli 不会代装），得到 `redis://:<pw>@127.0.0.1:<port>/0` |
| `pg` | `--pg-name mydb` | 使用 pgcli 的 **PG 实例**；`juicefs_<name>` 元数据库不存在则创建（幂等——重装复用），永远不删除 |
| `dsn` | `--meta-dsn <url>` | 任何 `postgres://` / `redis://` / `sqlite3://` URL 原样直通 |

```bash
pg addon install juicefs --name data --meta pg --pg-name default --s3 minio --s3-name ms3
pg addon install juicefs --name r1 --meta redis --redis-name cache
pg addon install juicefs --name e1 --meta-dsn "postgres://u:p@other-host:5432/jfs?sslmode=disable"
```

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--name` | `juicefs` | 卷名（配置 map 的键；format 时的 JuiceFS 卷名也是它） |
| `--meta` | `sqlite3` | 元数据引擎——`sqlite3`、`redis`、`pg` 或 `dsn` |
| `--meta-dsn` / `--dsn` | — | 元数据 URL 本身（隐含 `--meta dsn`） |
| `--redis-name` | `redis` | `--meta redis` 使用的 pgcli redis addon |
| `--pg-name` | — | `--meta pg` 使用的 pgcli PG 实例 |
| `--s3` | `minio` | 后端类型——`minio` 或 `silo` |
| `--s3-name` | = `--s3` | 哪个 addon 实例做后端（必须已安装且在跑） |
| `--bucket` | `juicefs-<name>` | 桶名——pgcli 不创建（见前置条件） |
| `--access-key` / `--secret-key` | 后端 root 凭据 | 覆盖 S3 凭据（要么都给，要么都不给） |
| `--mount-point` | `<base>/addon/juicefs/<name>/mnt` | 挂载的宿主目录 |
| `--cache-dir` | `<base>/addon/juicefs/<name>/cache` | 本地数据缓存目录 |
| `--install-dir` | `/usr/local/bin` | 宿主二进制落点（不可写时退到 `~/.local/bin`——注意保持其在 `PATH` 上） |
| `--version` | 内置 pin（`1.4.1`） | 要下载的版本（其 sha256 已 pin 在 pgcli 源码里） |
| `--no-mount` | 关 | 一切就绪但不挂载，之后手动 `pg addon juicefs mount` |
| `--force` | 关 | 按命令行参数重建配置条目（元数据引擎里卷已存在时 format 依然跳过） |

FUSE 本身（`/dev/fuse` + `fusermount3`）只探测、不安装——缺 `fuse3` 包时安装
会失败并给出该执行的 apt 命令。

## 日常管理

```bash
pg addon juicefs status --name data      # 后端、元数据引擎、二进制、挂载状态
pg addon juicefs umount --name data
pg addon juicefs mount --name data       # juicefs mount -d 自带守护
pg addon start juicefs --name data       # 顶层别名 = mount/umount
pg addon stop  juicefs --name data
pg logs addon juicefs --name data -f     # 追挂载日志
pg addon password juicefs --name data    # 取回存的 S3 secret key
pg addon remove juicefs --name data      # 卸载 + 忘掉的只是配置；桶、元数据、二进制都在
pg addon remove juicefs --name data --clean-data   # 连宿主目录一起删
```

`remove` 从不删 S3 数据、也不删 `--meta pg` 建的库——后者只打印一条手动
`DROP DATABASE` 供你自行决定。挂载属主就是运行 `pg` 的用户。

## 开机自启

```bash
pg autostart enable --juicefs --name data
```

JuiceFS 是宿主二进制 addon，所以 `pg start --autostart` 在开机时做的是
**重新挂载**而不是起容器——排在所有对象存储 addon 之后，S3 端点先监听、文件
系统再去够它。绝不重新 format。挂载以 boot 服务所运行的用户身份完成。
