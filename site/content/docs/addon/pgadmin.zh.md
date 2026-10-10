---
title: "pgAdmin 4"
description: "以 pgcli 插件方式运行 pgAdmin 4——PostgreSQL 官方 Web 管理界面，单容器直接跑上游 dpage/pgadmin4 镜像，自动生成 Web 登录账号，可选预置服务器"
weight: 52
---

[pgAdmin 4](https://www.pgadmin.org/) 是 PostgreSQL 的官方 Web 管理界面——在
浏览器里浏览 schema、执行 SQL、观察服务器状态。pgcli 将其作为**独立的顶层插
件**运行，CLI 表面与其他 web/infra 插件一致（install / start / stop / logs /
autostart / remove），全部通过 `pg.yaml` 管理。

它是插件族里唯一的 *UI* 插件，这一点决定了它的设计：

- **它是 Web 应用，不是数据库前置。** pgAdmin 连接哪些服务器由*你自己*在其 UI
  里注册决定；它不像 [PostgREST](../postgrest/) 或 [PgBouncer](../pgbouncer/)
  那样专职前置某一个实例。这就是它做成顶层-only 插件
  （`addons.pgadmin.<name>`）、没有实例 sidecar 的原因。
- **它有自己的登录。** Web UI 需要邮箱 + 密码登录。这**不是** PostgreSQL 密码
  ——是 pgAdmin 自己的账号，首次安装时自动生成并存入 `pg.yaml`。之后用
  `pg addon password pgadmin` 取回。
- **持久化的会话存储。** pgAdmin 把配置、已保存的服务器、浏览历史存在 bind
  挂载数据目录里的 sqlite 库中。它跨 `pg addon remove`（不带 `--clean-data`）
  存活，重装同名实例即复活。
- **可选的一次性预置。** `--dsn` 或 `--pg-name <实例名>` 在 install 时向 UI 的
  `servers.json` 预注册一个服务器，让浏览器打开时列表里已有它。这只是一个便利
  ——不是运行期耦合：pgAdmin 不会因它阻塞，之后可在 UI 里增删。

> **不在范围内：** 这里 pgAdmin 只提供明文 HTTP——没有接入 TLS 终止（需要
> HTTPS 就在前面架反向代理）。`servers.json` 本身不能携带目标服务器的密码
> （pgAdmin 拒绝导入），但当预置 DSN *带了*密码时，pgcli 通过 pgAdmin 的 pgpass
> 支持预配置它，预置服务器连接时**无需再输入**——见[预置服务器](#预置服务器)。

## 参数

单容器直接跑上游 `docker.io/dpage/pgadmin4` 镜像（pull-only——pgcli 从不构建
它），全部经由 entrypoint 读取的 `-e` 环境变量配置。下表列出每个旋钮及其默认
值：

| 参数 / `pg.yaml` 键 | 默认值 | 环境变量 | 含义 |
|----------------------|---------|---------|---------|
| `--image` / `image_tag` | `docker.io/dpage/pgadmin4:9.18` | — | 直接指定镜像标签 |
| `--port` / `host_port` | 自动，取自 `pgadmin_start_port`（基址 **5050**） | `PGADMIN_LISTEN_PORT` | Web 宿主端口 |
| `--listen` / `listen` | `127.0.0.1` | `PGADMIN_LISTEN_ADDRESS` | 绑定地址（`0.0.0.0` 对外开放，见[监听与暴露](#监听与暴露)） |
| `--email` / `email` | `admin@pgcli.lan` | `PGADMIN_DEFAULT_EMAIL` | Web 登录账号（会经过镜像校验，见[排障](#排障)） |
| `--password` / `password` | 自动生成，20 字符 | `PGADMIN_DEFAULT_PASSWORD` | Web 登录密码——**不是** PG 密码 |
| `--dsn` / `dsn` | 不设 | —（渲染 `servers.json` + pgpass） | 以 URI 预注册一个服务器——带密码时同时预配置（host 是从容器内拨号的，见[预置的 host 与 listen 地址](#预置的-host-与-listen-地址)） |
| `--pg-name` | 不设 | —（渲染 `servers.json` + pgpass） | 改为从本机已管理实例解析该服务器 |
| `--data-dir` / `data_dir` | `<base-dir>/addon/pgadmin/<name>/data` | （挂载到 `/var/lib/pgadmin`） | 会话存储位置 |
| — | `autostart: false` | — | 开机自启（`pg autostart enable --pgadmin`） |

旋钮间的配合关系：

- **`email` + `password` 只在空数据目录上生效。** pgAdmin 只在*初始化*
  `pgadmin4.db` 期间读取 `PGADMIN_DEFAULT_*`；该文件一旦存在，库里的账号就是
  权威，环境变量被忽略。因此复活既有目录的重装会打印一个对它无效的密码（install
  会明说）——要真正重置登录，先 `--clean-data` 移除；复活时也可以把
  `--password` 钉成原值。
- **`--dsn` 与 `--pg-name` 互斥**——二者都表示"预置这个服务器"，一个给 URI，
  一个从本实例解析端点。`--name` 是插件自己的键，与 `--pg-name` 无关。预置里的
  host 是 **pgAdmin** 的连接出发地，不是你浏览器的位置——见[预置的 host 与
  listen 地址](#预置的-host-与-listen-地址)。
- **`listen` 默认 loopback**——Web UI 有登录门，所以除非传 `--listen 0.0.0.0`
  从别的主机访问（summary 会警告登录成了唯一防线），它保持 `127.0.0.1`。完整
  情形见[监听与暴露](#监听与暴露)。
- **`email` 必须过得了镜像的校验器**——entrypoint 用 `email_validator` 检查它，
  直接拒绝保留 TLD（`.local`、`.invalid`、`.test`），命中就崩溃重启。默认值
  `admin@pgcli.lan` 正是为通过校验而选；自己起名时同理（`.lan`、`.internal`
  这类由你掌控的名字都行——不要求是真实可投递的地址，投递检查是关掉的）。

## 安装

```bash
# 默认：实例名 "pgadmin"，端口自 5050 自动分配，loopback，admin@pgcli.lan
pg addon install pgadmin

# 命名 console，用你自己的 Web 登录邮箱
pg addon install pgadmin --name console --email me@example.com

# 用本机已管理的一个实例预置 UI
pg addon install pgadmin --name dev --pg-name proj01

# 用任意远程 DSN 预置
pg addon install pgadmin --name remote \
    --dsn "postgres://readonly@10.0.0.7:5432/appdb"
```

install summary 打印 URL 与（自动生成的）Web 登录：

```
✓ pgAdmin installed: "console"
  Container:  pgcli-pgadmin-default-console
  Image:      docker.io/dpage/pgadmin4:9.18
  Data:       ~/pg/addon/pgadmin/console/data
  URL:        http://127.0.0.1:5050/
  Seeded:     proj01
  Seed auth:  password pre-configured (pgpass) — no prompt on first connect

  Login email:    me@example.com
  Login password: <generated>
  (Web login only — not a PostgreSQL password. Retrieve it later with `pg addon password pgadmin`.)

  Add servers to browse in the web UI itself, or reinstall with --dsn/--pg-name to pre-seed one.
```

`Seeded:` / `Seed auth:` 行只在传了 `--dsn`/`--pg-name` 时出现。当 DSN 携带密
码时 `Seed auth:` 显示 `password pre-configured (pgpass)`；不带密码时显示
`no password in the DSN — first connect will prompt`。重复 `install` 是幂等的：
运行中的容器原样保留，已停的被启动。要让改动的端口/listen/email/预置生效，加
`--force`（容器按配置重建；数据目录不动）。

### 用 `--force` 重建

不带 `--force` 的 `pg addon install pgadmin` 对运行中的容器是空操作——它不动
现有配置，只确认容器在跑。当你*正要*修改某些东西（端口、listen、email、镜像、
预置 DSN、`--pg-name` 目标、`data_dir`）时，这恰好是错误的行为：运行中的容器
钉在创建它时的配置上，编辑看似被接受，但直到下次重建前什么都不会改变。

`--force` 就是应用这些改动的旋钮。它停止运行中的容器、移除它，并按当前配置创建
一个新的；数据目录**不动**，已存服务器和浏览历史在新容器里回来：

```bash
# 把绑定地址改成暴露到网络
pg addon install pgadmin --listen 0.0.0.0 --force

# 把预置服务器换成另一个实例
pg addon install pgadmin --pg-name proj02 --force

# 彻底去掉预置（不带 --dsn / --pg-name + --force）
pg addon install pgadmin --force

# 切到另一个上游镜像标签
pg addon install pgadmin --image docker.io/dpage/pgadmin4:9.19 --force
```

两个值得说清的坑：

- **`email` / `password` 仍然只对空数据目录重新生效。** pgAdmin 只在*初始化*
  `pgadmin4.db` 期间读取 `PGADMIN_DEFAULT_*`；该文件一旦存在，库里的账号就是
  权威，环境变量被忽略。单 `--force` 不会重置 Web 登录——要真正重置账号，得配合
  `--clean-data`（先把存储清掉）。
- **运行中的容器会被重建，不是原样保留。** 如果浏览器正开着 UI，`--force` 会杀
  掉会话；下一次页面加载会重新认证新容器。

## 使用 Web UI

把浏览器指向 summary 里的 `URL`（在跑 podman 的那台主机上），用登录邮箱 + 密码
签名进入，pgAdmin 自己的服务器树就出现了。在 UI 里添加连接，或在安装时用
[--dsn / --pg-name](#预置服务器) 预注册一个。

如果主机不在你的浏览器旁边：设 `--listen 0.0.0.0`（登录成了唯一防线）：

### 监听与暴露

`--listen`（配置键 `listen`）是 pgAdmin HTTP 服务绑定的地址，默认 loopback。
三种姿态如下——都要配 `--force`，因为容器需重建才会采纳改动：

```bash
# 仅 loopback（默认）——只有跑 podman 的那台主机能访问
pg addon install pgadmin --name console --listen 127.0.0.1 --force

# 全部网卡——其他主机可访问；登录成了唯一防线
pg addon install pgadmin --name console --listen 0.0.0.0 --force

# 暴露过之后再收回 loopback
pg addon install pgadmin --name console --listen 127.0.0.1 --force
```

- **Linux** 上容器共享宿主网络，所以 `--listen 0.0.0.0` 会在
  `<宿主IP>:<端口>` 应答，`--listen 127.0.0.1` 只在 `127.0.0.1:<端口>`。放宽到
  `0.0.0.0` 意味着 pgAdmin 之外只剩邮箱/密码登录这一道门——install 会警告这一
  点——因此除非网络可信，保持 loopback。
- **macOS** 上情况相反：bridge 网络在 podman machine 内部总是绑 `0.0.0.0`，
  pgcli *发布*端口，所以 `listen` 在那里实际上是恒被放宽的，你从 Mac 经
  gvproxy 用 `127.0.0.1:<端口>` 访问 UI。见[平台支持](/docs/platform/)。

### 取回 Web 登录

```bash
pg addon password pgadmin --name console          # 裸打印，供脚本用
pg addon password pgadmin --name console --file ./pw.txt   # 写成 0600 文件
```

`pg addon list` 显示 URL 和登录邮箱，但不加 `--show-password` **绝不**打印密码。

## 预置服务器

`--dsn` 与 `--pg-name` 都渲染一份一次性的 `servers.json`，pgAdmin 在首次启动
时导入，浏览器打开时那个服务器已在树里。`--pg-name` 从本机已管理实例解析端点
（其 host/port/database/user **及密码**，读自 `pg.yaml`）；`--dsn` 以 URI 给出：

```bash
pg addon install pgadmin --name dev --pg-name proj01
# 或
pg addon install pgadmin --name dev --dsn "postgres://app:secret@127.0.0.1:5432/appdb"
```

预置携带什么、不携带什么：

- **目标服务器的密码在 DSN 携带时被预配置。** `servers.json` 本身不能持有密码
  （pgAdmin 拒绝导入），所以 pgcli 在旁边写一个 libpq `pgpass` 文件，并通过
  `ConnectionParameters.passfile` 把预置条目指向它——这是 pgAdmin 9.18 导入/导
  出服务器文档中正式记录的绝对路径机制。浏览器里的连接完全跳过密码输入。不带
  密码的 DSN（如 `postgres://app@host/db`）只渲染 `servers.json`，pgAdmin 在首
  次连接时照常提示输入。
- **pgpass 文件以明文在磁盘上持有密码。** 它位于
  `<base-dir>/addon/pgadmin/<name>/pgpass`，mode 0600，只有宿主用户和容器内的
  uid 5050 可读——但它*确实*在磁盘上，不像 `servers.json` 从不持有机密。权衡与
  所有使用 `.pgpass` 的工具相同：免密连接以磁盘上的一个文件为代价。移除 pgAdmin
  实例（无论是否带 `--clean-data`）会把 pgpass 与 `servers.json` 一并删除——明文
  机密不会比实例活得更久。
- **它是一次性便利，不是耦合。** 安装后服务器活在 pgAdmin 自己的 `pgadmin4.db`
  里；在 UI 里增、删、改名都行。无论预置目标是否可达，插件照常运行。
- **`PGADMIN_REPLACE_SERVERS_ON_STARTUP=True`** 在有预置时被设置，所以改了
  `--dsn`/`--pg-name` 的 `--force` 重装会声明式地重新应用预置，而不是留下过期
  条目。

要去掉预置并停止声明式重载，用 `--force` 重装且不带 `--dsn`/`--pg-name`。

### 预置的 host 与 listen 地址

这两个旋钮指向不同的机器，却常被混为一谈。`--listen` 决定**谁能访问 pgAdmin
的 Web UI**；预置里的 host 决定**pgAdmin 从哪里拨数据库**。拨号是服务端动作——
由 pgAdmin 容器发起，不是你的浏览器——所以用 `--pg-name` 预置本机管理的实例
时，记录 `127.0.0.1` 是**正确**的：pgAdmin 与那个 PostgreSQL 共享 podman 宿主
的回环。把 UI 暴露到网络**并不**要求预置的 host 对浏览器可路由：

```bash
# pgAdmin 在 vm01 上前置 vm01 自己的实例，但 UI 供其他主机访问
pg addon install pgadmin --listen 0.0.0.0 --pg-name demo --force
# servers.json -> Host: 127.0.0.1, Port: <demo 的 pg 端口>（容器内拨号）

# 改为预置另一台主机上的服务器——该 host 必须从 pgAdmin 容器可达，
# 因为发起连接的是容器
pg addon install pgadmin --listen 0.0.0.0 \
    --dsn "postgres://app@10.241.20.148:35432/appdb" --force
```

反过来的坑：用 `--dsn` 预置一个*远程* host，再从别处用浏览器访问那个
pgAdmin——连接依然源自 pgAdmin 容器内部，所以那个 host 必须**从容器的网络**
可达，而不是从你的笔记本可达。

## 端口

每个实例从 pgAdmin 自己的端口池 `pgadmin_start_port`（默认 **5050**）取**一个
端口**——与 Redis、Predixy、对象存储的池各自独立游标，所以一个 pgadmin、一个
redis、一个 minio 实例永不撞车：

```bash
pg addon install pgadmin --name console   # 5050
pg addon install pgadmin --name audit     # 5051
```

`--port` 可把实例钉在特定端口；自动分配会跳过任何已显式指定或宿主上已占用的
端口。

## 配置

实例位于 `pg.yaml` 的顶层 `addons.pgadmin` map：

```yaml
namespace: default
pgadmin_start_port: 5050     # pgAdmin 自己的端口池
addons:
  pgadmin:
    console:
      container_name: pgcli-pgadmin-default-console
      name: console
      image_tag: docker.io/dpage/pgadmin4:9.18
      # data_dir: /srv/pgadmin         # 省略则为 <base-dir>/addon/pgadmin/console/data
      listen: 127.0.0.1                # 默认；0.0.0.0 对网络开放
      host_port: 5050
      email: me@example.com            # Web 登录账号（PGADMIN_DEFAULT_EMAIL）
      password: <generated>            # Web 登录密码；首次 install 时写入
      # dsn: postgres://app@127.0.0.1:5432/appdb   # 由 --dsn/--pg-name 设置：预置
      # server_name: app               # 预置服务器的显示名
      autostart: false                 # pg autostart enable --pgadmin --name console
```

对 `listen`、`host_port`、`email`、`password`、`image_tag`、`dsn`/
`server_name`、`data_dir` 的修改，在下一次
`pg addon install pgadmin --name console --force`（或对应 flags）后生效。记住：
`email`/`password` 只对**空**数据目录重新生效，所以对既有存储改它们需要先
`--clean-data`（或接受生效账号仍是原账号）。

### 列出

```bash
pg addon list
```

```
Web add-ons (pgadmin):
  pgadmin (name: console)
    Status:      running
    URL:         http://127.0.0.1:5050/
    Login email: me@example.com
    Seeded:      proj01
    Data:        ~/pg/addon/pgadmin/console/data
    Image:       docker.io/dpage/pgadmin4:9.18
    Container:   pgcli-pgadmin-default-console
```

`pg addon list` 从不打印密码。要刻意揭示，加 `--show-password`（每实例追加一行
`Login password:`），或用 `pg addon password pgadmin --name console` 裸打印——
并且优先用 `--file`，让机密留在文件里而不是 shell 历史与回滚缓冲。

## 启停

```bash
pg addon start pgadmin --name console
pg addon stop  pgadmin --name console
```

`start` 只启动已存在的容器（并以按配置重建来自愈不当状态）。数据目录跨
stop/start 保留，pgAdmin 的已存服务器与浏览历史随之回来。

## 开机自启

容器带 `--restart unless-stopped` 策略（管崩溃，不管重启）。要在宿主重启后拉起
实例：

```bash
pg autostart enable --pgadmin --name console
```

pgAdmin 自启是**只启动**（先 install）。开机时 entrypoint 重新 chown
`/var/lib/pgadmin`，并从数据目录重放 `pgadmin4.db`，已存服务器随之复活。pgAdmin
独立于 PostgreSQL 栈——它是自己拨号的浏览器 UI——所以没有排序约束，开机
`pg start --autostart` 把它与其他非数据库插件一起启动。

## 移除

```bash
pg addon remove pgadmin --name console               # 容器 + 配置条目；数据保留
pg addon remove pgadmin --name console --clean-data  # 同时删除数据目录
```

不带 `--clean-data` 时宿主数据目录（连同 `pgadmin4.db`）原地保留——重装同名实
例即复活已存服务器与会话存储（复活后可用的登录是原密码，不是新生成的）。
`--clean-data` 删除它，当 rootless podman 的映射 uid 使 pgAdmin 的文件对宿主用
户不可删时，经 `podman unshare rm` 兜底。空的实例父目录被修剪；自定义
`data_dir` 的父目录从不动。`servers.json`（若预置过）宿主所有，两种移除都随实
例目录一并删掉。

## 日志

```bash
pg logs addon pgadmin --name console        # 最近 50 行
pg logs addon pgadmin --name console -f     # 跟踪
```

## 排障

- **用打印的密码登录被拒** —— 你在保留的数据目录上重装了。能用的登录是
  pgAdmin 在*首次*初始化时写进 `pgadmin4.db` 的那个；新生成的密码无效（install
  会警告 "reviving an existing pgAdmin data dir"）。用 `--clean-data` 从零开
  始，或用 `pg addon password pgadmin` 确认存的是什么，或重装时把 `--password`
  钉成原值。
- **"pgAdmin could not start" / 容器秒退（或崩溃循环）** —— 空目录上 entrypoint
  需要 `PGADMIN_DEFAULT_EMAIL` 和 `PGADMIN_DEFAULT_PASSWORD` 都在；手改过
  `pg.yaml`、密码为空且数据目录被清过，就两个都没有。让 install 生成，或传
  `--password`。它还会**校验邮箱**并拒绝保留 TLD（`.local`、`.invalid`、
  `.test`），报 *"does not appear to be a valid email address"*——容器随即退出，
  在重启策略下反复自旋。这正是默认值取 `admin@pgcli.lan` 而非 `.local` 的原因。
- **从别的机器打不开 UI** —— `listen` 默认 `127.0.0.1`。`--listen 0.0.0.0 --force`
  重装并接受 Web 登录成为唯一防线。
- **预置的服务器显示但连不上** —— 若预置 DSN 不带密码，pgAdmin 在首次连接时
  提示输入（[预置服务器](#预置服务器)）。若 DSN *带了*密码却仍然提示，说明
  pgpass 文件失效了——最常见原因是容器启动时 entrypoint 的 `chown` 没跑（例如
  直接传了 `--user 5050` 而不是 `--user 0`，导致文件属主是宿主用户，libpq 静默
  丢弃它）。确认 host/port 从 pgAdmin 所在处可达。
- **install 时找不到镜像** —— `docker.io/dpage/pgadmin4` 按需拉取；离线宿主先
  `podman load` 对应 tar（目录与导出步骤在仓库 `docs/images.md`），pull 会被跳
  过。
- **首次启动响应慢** —— pgAdmin 在 gunicorn 绑定之前先跑 sqlite 迁移；端口要
  等它完成才回 302（跳 `/login`）。负载高的宿主上这可能要几十秒。

## 已知限制

- **无 TLS** —— 这里 pgAdmin 提供明文 HTTP。要 HTTPS 就在前面架反向代理（配合
  `PGADMIN_URL_SCHEME`/`PGADMIN_DISABLE_STATIC_FILE_SERVER` 调参）；pgcli 不接
  这条线。loopback 默认让常见场景留在 `127.0.0.1`。
- **预置密码以明文存于磁盘** —— `servers.json` 旁边的 pgpass 文件是 mode 0600
  但没有加密；移除实例时一并清理。没有办法预配置密码而不落盘文件（pgAdmin 没有
  KMS/keyring 集成）。
- **登录只对新存储生效** —— pgAdmin 只在*初始化* `pgadmin4.db` 期间读取
  `PGADMIN_DEFAULT_*`；该文件一旦存在，库里的账号就是权威。没有原地改密码的路，
  只有 `--clean-data` + 重装。
- **macOS 代码完备、未实测** —— bridge 路径（发布端口、bridge 下
  `proxyBindHost` 把 loopback 放宽到 `0.0.0.0`）与 redis 同构；尚未在 Mac 上演
  练过。
