---
title: "SQLite + Litestream"
description: "在宿主机上管理 SQLite 数据库，并用 Litestream 将 WAL 变更复制到 S3 兼容对象存储"
weight: 30
icon: fa-solid fa-database
aliases: ["/toolbox/sqlite/"]
menus:
  main:
    identifier: docs-toolbox-sqlite
    parent: docs-toolbox
    weight: 30
    params:
      icon: fa-solid fa-database
cascade:
  type: docs
  footer_style: slim
---

`pg sqlite` 在 Linux 宿主机上管理 SQLite 数据库，并可选地用
[Litestream](https://litestream.io/) 把 WAL 变更持续复制到 S3 兼容对象存储。
SQLite 直接以宿主机进程运行：不使用容器、不引入 Go SQLite driver，也不依赖 cgo。
所有 SQL 都由 `sqlite3` 命令行执行；每个数据库对应一个独立的 Litestream 后台进程。

复制是异步的（默认 sync interval 下 RPO 约 1 秒）。它属于持续备份，不是高可用：
SQLite 仍然只有一个写入者，故障切换意味着要在别处恢复备份。

目前仅支持 Linux。

## 工作原理

- 每个受管数据库在 `pg.yaml` 中有一条记录，并在 `<base>/sqlite/<name>/` 下有独立
  的状态目录。
- `pg sqlite create` 会启用 `journal_mode=WAL`，该设置持久化在数据库文件头中。
- `pg sqlite replication enable` 生成专属的 `litestream.yml`（权限 `0600`），并在
  后台启动 `litestream replicate`。Litestream 跟随 WAL，把 LTX 对象上传到指定
  bucket。
- S3 凭据**只**写入该 `litestream.yml`，绝不会进入 `pg.yaml`，任何 `pg sqlite`
  命令也不会打印它。

## 安装

```bash
pg sqlite install
```

命令是幂等的，会安装两个宿主机二进制：

1. 如果 `sqlite3` 不在 `PATH` 上，则通过发行版包管理器安装（apt-get、dnf、yum、
   pacman、zypper 或 apk）。只有这一步可能需要 root；pgcli 使用免密
   `sudo -n`，不会交互式索要密码，失败时会给出明确提示。
2. Litestream `0.5.17`，从 GitHub 下载并校验官方 SHA256。可写时安装到
   `/usr/local/bin`，否则安装到 `~/.local/bin`。

常用参数：

```bash
pg sqlite install --install-dir /opt/bin      # 指定 Litestream 安装目录
pg sqlite install --force-litestream          # 强制重新下载受支持版本
```

Litestream 0.3.x 与生成的配置不兼容；安装器会忽略 `PATH` 上的旧版本，直接安装
0.5.x。

## 创建与管理数据库

```bash
# 默认路径：<base>/sqlite/appdb/appdb.db
pg sqlite create appdb
pg sqlite create analytics --path /srv/data/analytics.db

pg sqlite list
pg sqlite info appdb
pg sqlite info appdb --integrity full

pg sqlite exec appdb "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"
pg sqlite exec appdb "INSERT INTO items (name) VALUES ('first')"

# 从 stdin 读取 SQL（不受命令行长度限制）
pg sqlite exec appdb - <<'SQL'
INSERT INTO items (name) VALUES ('second');
INSERT INTO items (name) VALUES ('third');
SQL

pg sqlite shell appdb
pg sqlite shell appdb -- -header -column
```

`create` 不会截断已有文件：指向一个已存在的 SQLite 数据库时会接管该文件、启用
WAL，并保留原有内容。`--path` 必须是绝对路径，且只移动数据库文件——配置、日志、
pid 和 Litestream 本地状态始终留在 `<base>/sqlite/<name>/` 下。

`info` 读取 `journal_mode`、page size/count 和 freelist，默认执行
`PRAGMA quick_check`。大库可用 `--integrity none` 跳过检查，或
`--integrity full` 执行完整 `integrity_check`。

`remove` 只注销数据库并停止复制，数据库文件保留。加 `--clean-data` 才会删除
数据库、WAL 附属文件、Litestream 本地状态和日志（交互确认，`--force` 跳过）。
远端 S3 备份永远不会被自动删除。

## 启用 S3 复制

bucket 必须已经存在——Litestream 不会创建 bucket。请先用 `mc mb`、云控制台或
其他 S3 工具创建好。

### 本地 MinIO（测试用）

pgcli 可以直接用 MinIO addon 在宿主机上起一个对象存储：

```bash
# 安装名为 sqlite-s3 的 MinIO 实例；命令输出会打印 S3 API 端点。
# 端口空闲时默认端点为 http://127.0.0.1:9000。
pg addon install minio --name sqlite-s3

# root 用户默认为 admin，密码在安装时生成。
S3_SECRET=$(pg addon password minio --name sqlite-s3)

# 用内置的 mc 包装器注册端点并创建 bucket。
pg mc alias set local http://127.0.0.1:9000 admin "$S3_SECRET"
pg mc mb local/sqlite-backups
```

如果 9000 端口已被占用，请改用 `pg addon list` 中显示的实际端点。然后把复制指向
这个本地存储：

```bash
pg sqlite replication enable appdb \
  --s3-endpoint http://127.0.0.1:9000 \
  --s3-bucket sqlite-backups \
  --s3-access-key admin \
  --s3-secret-key "$S3_SECRET" \
  --s3-uri-style path
```

其他 MinIO 或 S3 兼容存储使用相同的 flags，替换成自己的端点和凭据：

```bash
pg sqlite replication enable appdb \
  --s3-endpoint http://127.0.0.1:9000 \
  --s3-bucket sqlite-backups \
  --s3-access-key minioadmin \
  --s3-secret-key minioadmin \
  --s3-uri-style path
```

AWS S3，使用默认凭据链（命令行不传 key）：

```bash
pg sqlite replication enable appdb \
  --s3-bucket my-org-sqlite-backups \
  --s3-region eu-west-1 \
  --s3-uri-style virtual-host
```

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--s3-bucket` | 必填 | 必须已存在 |
| `--s3-endpoint` | AWS 默认 | 完整 `http(s)://host[:port]`，不能带路径或凭据 |
| `--s3-path` | `sqlite/<name>` | 对象 key 前缀；前导 `/` 会被去掉 |
| `--s3-region` | `us-east-1` | |
| `--s3-access-key` / `--s3-secret-key` | 默认凭据链 | 必须同时提供或同时省略 |
| `--s3-uri-style` | `path` | `path` 或 `virtual-host` |
| `--s3-ca-file` | 无 | 通过 `SSL_CERT_FILE` 传给 Litestream，用于私有 CA |
| `--s3-skip-verify` | false | 关闭 TLS 校验（自签名端点） |
| `--sync-interval` | `1s` | WAL 上传间隔 |
| `--snapshot-interval` | `24h` | 全量快照间隔 |
| `--retention` | `24h` | 快照保留时间 |
| `--no-start` | false | 只写配置，不启动 Litestream |
| `--force` | false | 覆盖已有配置 |

`enable` 会先确认数据库处于 WAL 模式，解析可用的 0.5.x Litestream（或用
`--litestream-binary PATH` 指定），以 `0600` 写入 `litestream.yml`，清理本地
Litestream 旧状态，启动进程并检查它没有立即退出。若立即退出，会打印日志最后一行
并报错。

在复制运行中，用完全相同的非敏感配置再次执行 `enable` 是幂等的。配置不同则默认
拒绝；`--force` 会停止旧进程、重写配置、reset 本地状态并重启。

### 凭据存放位置

| 路径 | 内容 | 权限 |
|---|---|---|
| `<base>/pg.yaml` | 名称、路径、binary/version、endpoint、bucket、前缀、region、URI style、CA file、skip-verify —— **不含 key** | 0644 |
| `<base>/sqlite/<name>/<name>.db` | 数据库文件 | 应用默认 |
| `<base>/sqlite/<name>/litestream.yml` | 完整 S3 配置，**含 access/secret key** | 0600 |
| `<base>/sqlite/<name>/litestream.log` | 进程日志 | 0644 |
| `<base>/sqlite/<name>/litestream.pid` | 运行中的 pid | 0644 |
| `<base>/sqlite/<name>/.<db>-litestream/` | Litestream 本地 LTX 状态 | 应用默认 |

备份或复制 `litestream.yml` 时要像对待其他凭据文件一样谨慎。
`replication disable` 会删除它，因此下次 `enable` 需要重新提供凭据。

## 复制日常操作

```bash
pg sqlite replication status appdb
pg sqlite replication status              # 所有数据库
pg sqlite replication logs appdb -n 100
pg sqlite replication logs appdb -f
pg sqlite replication stop appdb          # 最终 sync，最长 30s
pg sqlite replication start appdb
pg sqlite replication disable appdb       # 停止并删除 litestream.yml
```

`status` 结合进程状态与 `litestream status -json`（本地事务 id 和 WAL 大小），
并显示日志中最近的错误行。即使状态查询失败，仍会展示进程状态并正常退出。

### 检查远端备份文件

`pg mc` 是内置 MinIO 客户端的包装器，mc 自己的 flags 必须写在 `--` 之后。使用
「本地 MinIO」示例中创建的 alias，可以列出 Litestream 上传到 `--s3-path` 下的对象：

```bash
pg mc ls local/sqlite-backups/sqlite/appdb -- --recursive
```

有数据时类似这样：

```text
0000/0000000000000001-0000000000000001.ltx
0000/0000000000000002-0000000000000002.ltx
0001/0000000000000001-0000000000000002.ltx
0009/0000000000000001-0000000000000002.ltx
```

`0000/` 存放逐事务的 WAL 帧；编号更大的层级（`0001/`、`0009/` 等）是合并、
压缩后的文件。刚 `enable` 完时前缀为空只说明还没有写入被同步——插入一行数据，
等待至少一个 `--sync-interval`（默认 `1s`）后再列一次。`replication status`
显示的本地事务 id 应与这些文件名中的最大 txid 一致。

`disable` 保留数据库、本地 LTX 状态和日志，只删除 `pg.yaml` 中的 replication
块并删除 `litestream.yml`。远端备份不受影响。

## 开机自启

Litestream 是独立的宿主机进程，不会在主机重启后自行恢复。把数据库注册到
pgcli 的开机服务：

```bash
pg autostart enable --sqlite --name appdb
pg autostart status
```

`--name` 必填（SQLite 没有默认数据库名）。复制必须已经启用且 `litestream.yml`
仍然存在——开关命令会检查这两点，否则报错并提示重新 enable。开机时
`pg start --autostart` 会在对象存储 addon 之后重启 Litestream，因此同机的
MinIO/silo/rustfs/garage/SeaweedFS 已经先开始监听。开机路径只做启动：绝不
重写 `litestream.yml`，也不修改数据库文件。

```bash
pg autostart disable --sqlite --name appdb
```

`pg sqlite replication disable` 会自动清除该标志。systemd/launchd 服务细节见
[开机自启](/docs/autostart/)。

## 恢复

Litestream 可以恢复最新状态或指定时间点。支持两种模式：

```bash
# 恢复到另一个路径；复制继续运行
pg sqlite replication restore appdb --target /tmp/appdb-restored.db

# 按 RFC3339 时间戳恢复（UTC，使用 Z 后缀）
pg sqlite replication restore appdb --target /tmp/appdb-old.db \
  --timestamp 2026-01-01T12:00:00Z --integrity full

# 按 Litestream 事务 id 恢复（十六进制，可带 0x 前缀）
pg sqlite replication restore appdb --target /tmp/appdb-tx.db --txid 00000000000000ff

# 只展示将要恢复的内容，不写任何文件
pg sqlite replication restore appdb --dry-run
```

`--timestamp` 与 `--txid` 互斥；都不传则恢复最新状态。`--timestamp` 表示绝对
时间点，请写成**带 `Z` 后缀的 UTC 时间**（如 `2026-01-01T12:00:00Z`），避免恢复
时间点受宿主机时区影响。`--target` 不能与 `--restart` 同用；目标已存在时必须加
`--force`，此时旧的 target 及其 `-wal`/`-shm` 会先被移入
`<target>.pre-restore-<UTC>/`。

### 时间点恢复演练

在做出想撤销的变更之前，先记录一个事务边界：

```bash
# 当前本地事务 id（输出中的 `local: ... txid=...` 行）
pg sqlite replication status appdb

# 或查看 S3 上已上传的 LTX 范围
pg mc ls local/sqlite-backups/sqlite/appdb -- --recursive
```

LTX 对象名为 `<min-txid>-<max-txid>.ltx`。按 `--txid` 恢复会包含该事务及之前
的状态。记录变更前的 txid（或一个安全早于变更的 RFC3339 UTC 时间戳），然后恢复
一份副本并验证：

```bash
pg sqlite replication restore appdb \
  --target /tmp/appdb-pitr.db \
  --txid 0000000000000004 \
  --integrity full

sqlite3 -readonly /tmp/appdb-pitr.db \
  "SELECT COUNT(*) FROM items; PRAGMA journal_mode;"
```

时间戳方式流程相同：

```bash
pg sqlite replication restore appdb \
  --target /tmp/appdb-pitr-ts.db \
  --timestamp 2026-01-01T12:00:00Z \
  --integrity full
```

不确定时先加 `--dry-run`：它只打印将要使用的事务范围，不写任何文件。恢复到
`--target` 不会停止复制，因此可以在检查副本的同时继续处理线上流量。建议再恢复
一次最新状态（不传 `--timestamp`/`--txid`），确认较新的数据存在；验证无误后，
再用原地恢复把副本正式替换上线。

### 原地恢复

不传 `--target` 时会替换配置中的数据库：

```bash
pg sqlite replication restore appdb --force
```

流程刻意保守：

1. 停止 Litestream（会执行最终 sync）；其他打开该数据库的应用必须提前自行停止。
2. 当前数据库及 WAL 附属文件移入 `<db>.pre-restore-<UTC>/`（不加 `--force` 时
   会先确认）。
3. 备份先恢复到数据库同目录下的临时目录，因此恢复失败也不会影响已经保留的旧副本。
4. 检查恢复出的数据库，必要时切回 WAL，然后通过同一文件系统内的原子 rename
   移动到最终位置。
5. 重置 Litestream 本地状态；若 reset 失败，则删除旧的本地状态目录。
6. 默认保持复制停止；`--restart` 会重新启动。

恢复后把复制重启到相同的 `--s3-path`，可能把恢复出的时间线与远端旧数据混在一起。
优先用新的 `--s3-path` 重新 enable；使用 `--restart` 前请确认理解旧时间线。

如果数据库文件完全丢失，原地恢复不需要 `--force`；但每个数据库的
`litestream.yml` 必须仍然存在。

## WAL 与应用侧设置

CLI 只把 `PRAGMA journal_mode=WAL` 持久化到数据库文件中。以下设置按连接在应用侧
调整：

```sql
PRAGMA busy_timeout=5000;   -- 等待锁而不是立即失败
PRAGMA synchronous=NORMAL;  -- WAL 下的推荐值（在持久性上有所取舍）
PRAGMA wal_autocheckpoint=1000;
```

注意事项：

- WAL 模式要求本地文件系统，不要把数据库放在 NFS 或 SMB 上。
- 同一时刻仍然只有一个写入者。长写事务会阻塞其他写入者；长读事务会阻止 WAL
  checkpoint，可能让 WAL 无限增长。
- Litestream 复制的是 WAL，不是应用一致快照。涉及多条语句的不变量请使用事务。

## 故障排查

**`litestream exited during startup`** —— 执行
`pg sqlite replication logs <name> -n 50`。常见原因是 bucket 不存在、凭据错误，
或宿主机无法访问 endpoint。

**`no Litestream 0.5.x binary found`** —— 执行 `pg sqlite install`。`PATH` 上的
0.3.x 会被报告但不会使用。

**连接 MinIO/RustFS 的 TLS 错误** —— 纯 HTTP 端点要写完整
`http://host:port`；自签名 HTTPS 用 `--s3-ca-file` 或 `--s3-skip-verify`。

**`database ... uses journal_mode=delete`** —— 数据库不是由 `pg sqlite create`
创建的，或被其他工具改过模式。在没有其他连接时执行
`PRAGMA journal_mode=WAL;`，再重试 `enable`。

**`litestream.yml not found`** —— `start`、`status` 和 `restore` 都依赖
`enable` 写出的配置；请重新执行 `enable`（凭据不在 `pg.yaml` 中）。

## 限制

- 仅支持 Linux；macOS 的 host-binary 路径未实现。
- Litestream 是异步备份，不是同步复制或高可用。默认 RPO 约 1 秒，
  `stop`/`disable` 时的最终 sync 最长可能耗时 30 秒。
- 每个数据库一个 Litestream 进程、一个 WAL 写入者。
- `--clean-data` 和原地恢复具有破坏性；默认会确认，保留的 `.pre-restore-*`
  目录需要手动清理。
