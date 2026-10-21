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
`PATH` 上已有 `juicefs` 则直接复用），面向一个**外部 S3 服务** format 卷，再在
宿主上完成挂载。

> **平台**：仅 Linux（与 BeeGFS 一样明确拒绝 macOS）。挂载点是一个普通**宿主**
> 目录——只对宿主进程可见，不会传播进 PG 实例容器（v1 不做 rshared 挂载传播；
> addon 管理的是本机的挂载，不是集群的）。

## 一切全显式

卷的两端都在命令行上点名——pgcli 不做同主机 addon 的自动识别或凭据继承：

- **对象存储**：一个外部 S3 服务（云，或另一台机器上的 MinIO），用
  `--s3-endpoint http(s)://host[:port]` 加 `--access-key`/`--secret-key` 给出。
  pgcli 在 format 前先探一次端点，不可达的主机会快速失败并提示 "not
  answering"，而不是卡死在 `juicefs format` 里。端点会在 format 时被烤进卷
  元数据——之后挂载只需要元数据 URL。
- **元数据引擎**：一个显式 URL——`--dsn`（别名 `--meta-dsn`），接受
  `postgres://`、`redis://` 或 `sqlite3://`。pgcli 从不创建或删除该数据库；
  PostgreSQL 时它必须已经存在。
- **桶**：从不由 pgcli 创建。用 `pg mc` 这个包装命令自己供给一次——安装时会
  原样打印出那两条命令：

```bash
pg mc alias set externals3 http://minio.example.com:9000 AK '***'
pg mc mb externals3/juicefs-data
```

```bash
pg addon install juicefs --name data \
    --s3-endpoint http://minio.example.com:9000 \
    --access-key AK --secret-key SK \
    --bucket juicefs-data \
    --dsn "sqlite3:///var/lib/juicefs/data.db"
```

换引擎也只是换 URL：

```bash
# PostgreSQL：库必须已存在（pgcli 从不创建或删除），juicefs 会在其中自建
# jfs_* 表。
--dsn "postgres://jfs:pass@meta-host:5432/jfs?sslmode=disable"

# Redis（+TLS）：/N 后缀选择逻辑库。
--dsn "redis://:pass@redis-host:6379/0"
--dsn "rediss://user:pass@redis-host:6380/1"
```

挂载点完全由你指定——默认落在 pgcli 的 addon 目录下，用 `--mount-point` 指到
任意位置。目录要你亲手建好（`mkdir -p`），且运行 `pg` 的用户对其有读写权限——
pgcli 从不创建挂载点：

```bash
mkdir -p /mnt/cloudfs
pg addon install juicefs --name data \
    --s3-endpoint https://s3.amazonaws.com \
    --access-key AK --secret-key SK \
    --dsn "postgres://jfs:pass@meta-host:5432/jfs?sslmode=disable" \
    --bucket juicefs-data --mount-point /mnt/cloudfs
```

三种引擎都可以就用 pgcli 自己的服务——先用 pgcli 建好，再作为普通的"外部
URL"显式接线（安装代码从不自动查找它们；换成托管数据库或云桶也一模一样）：

```bash
# PostgreSQL 元数据：先建实例、再建库（pgcli 的安装路径从不建/删库）。
# `pg status` 会打印 admin 连接 URL——把其中的 /postgres 换成新库名。
pg create meta-pg
pg exec meta-pg -c "CREATE DATABASE jfs"
pg status meta-pg

pg addon install juicefs --name data \
    --s3-endpoint http://minio-host:9000 --access-key AK --secret-key SK \
    --bucket juicefs-data \
    --dsn "postgres://admin:PASS@127.0.0.1:35432/jfs?sslmode=disable"
```

```bash
# Redis 元数据（pgcli redis addon；`pg addon password redis` 裸输出随机口令，
# 手工拼进 URL，/N 选逻辑库）：
pg addon install redis --name mred
pg addon password redis --name mred
pg addon install juicefs --name r1 \
    --s3-endpoint http://minio-host:9000 --access-key AK --secret-key SK \
    --bucket juicefs-r1 --dsn "redis://:PASS@127.0.0.1:6379/1"
```

```bash
# MinIO 做对象存储（pgcli minio addon——silo/rustfs/garage/seaweedfs 同理；
# 桶仍要手动建，pgcli 从不建；root 用户默认 admin，除非安装时 --root-user
# 另有指定；`pg addon password minio` 裸输出 secret key）：
pg addon install minio --name ms3
pg mc mb ms3/juicefs-data
pg addon install juicefs --name m1 \
    --s3-endpoint http://127.0.0.1:9000 --bucket juicefs-data \
    --access-key admin --secret-key "$(pg addon password minio --name ms3)" \
    --dsn "sqlite3:///var/lib/juicefs/m1.db"
```

以上示例中 `pg` 与存储同机，loopback（默认 `127.0.0.1`）即可。JuiceFS 客户
端在别的机器上时，把存储发布到网络、`--s3-endpoint` 指向它的地址——端点在
format 时被烤进卷元数据，所以从一开始就发布好：

```bash
pg addon install minio --name ms3 --listen 0.0.0.0   # 已装过则加 --force
# 在另一台机器上：
pg addon install juicefs --name m1 --s3-endpoint http://10.0.0.5:9000 …
```

（redis 示例同理：redis 默认就绑 `0.0.0.0`——口令即门禁。）

凭据和端点已经住在元数据里，所以同一个卷之后在任何有元数据 URL 的主机都能挂。

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--name` | `juicefs` | 卷名（配置 map 的键；format 时的 JuiceFS 卷名也是它） |
| `--dsn` / `--meta-dsn` | — | 必填。元数据引擎 URL：`postgres://`、`redis://` 或 `sqlite3://` |
| `--s3-endpoint` | — | 必填。外部 S3 服务，形如 `http(s)://host[:port]`（不带桶路径、不内嵌凭据） |
| `--access-key` / `--secret-key` | — | 必填且必须成对。外部 S3 的凭据 |
| `--bucket` | `juicefs-<name>` | 桶名——pgcli 不创建（见上） |
| `--mount-point` | `<base>/addon/juicefs/<name>/mnt` | 挂载的宿主目录——pgcli 从不创建：先 `mkdir -p` 建好，且运行 `pg` 的用户可写 |
| `--cache-dir` | `<base>/addon/juicefs/<name>/cache` | 本地数据缓存目录 |
| `--install-dir` | `/usr/local/bin` | 宿主二进制落点（不可写时退到 `~/.local/bin`——注意保持其在 `PATH` 上） |
| `--version` | 内置 pin（`1.4.1`） | 要下载的版本（其 sha256 已 pin 在 pgcli 源码里） |
| `--no-mount` | 关 | 一切就绪但不挂载，之后手动 `pg addon juicefs mount` |
| `--force` | 关 | 按命令行参数重建配置条目（元数据引擎里卷已存在时 format 依然跳过） |

FUSE 本身（`/dev/fuse` + `fusermount3`）只探测、不安装——缺 `fuse3` 包时安装
会失败并给出该执行的 apt 命令。

## 日常管理

```bash
pg addon juicefs status --name data      # 端点、桶、元数据 URL、二进制、挂载状态
pg addon juicefs umount --name data
pg addon juicefs mount --name data       # juicefs mount -d 自带守护
pg addon juicefs mount --name data --mount-point /mnt/cloudfs
                       # 挂到自定义路径（先 mkdir -p）；未挂载时这样挂会顺带
                       # 永久迁移该卷的挂载点
pg addon start juicefs --name data       # 顶层别名 = mount/umount
pg addon stop  juicefs --name data
pg logs addon juicefs --name data -f     # 追挂载日志
pg addon password juicefs --name data    # 取回存的 S3 secret key
pg addon remove juicefs --name data      # 卸载 + 忘掉的只是配置；桶、元数据、二进制都在
pg addon remove juicefs --name data --clean-data   # 连宿主目录一起删
```

`remove` 从不删 S3 数据、也从不碰元数据存储——弄丢 `--dsn` 指向的目标就等于
弄丢整个文件系统。挂载属主就是运行 `pg` 的用户。

### 换挂载路径

挂载点记在配置条目上；`mount` 接受 `--mount-point` 覆盖，把卷迁到新路径——
新路径会持久化进 `pg.yaml`，之后的普通 `mount`（含 autostart）都用它。旧的
挂载必须先卸掉，新目录同样要预先建好且可写（与安装契约一致）：

```bash
pg addon juicefs umount --name data
mkdir -p /srv/cloudfs
pg addon juicefs mount --name data --mount-point /srv/cloudfs
#   [i] mount point moved to /srv/cloudfs (saved)
# ✓ juicefs "data" mounted at /srv/cloudfs
```

仍在挂载时尝试迁移会被拒绝并提示先 `umount`；指到不存在的目录则报出
`mkdir -p` 补救命令。挂载点与 pgcli 自己的簿记（pid/日志/缓存都在
`<base>/addon/juicefs/<name>/`）完全解耦，迁移从不影响卷本身——换的只是同一
个桶加元数据在另一处的挂法。

## 开机自启

```bash
pg autostart enable --juicefs --name data
```

JuiceFS 是宿主二进制 addon，所以 `pg start --autostart` 在开机时做的是
**重新挂载**而不是起容器。后端是外部服务——只有 S3 端点重新可达时开机才能成
功。绝不重新 format。挂载以 boot 服务所运行的用户身份完成。
