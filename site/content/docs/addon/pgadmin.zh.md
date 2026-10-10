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
> HTTPS 就在前面架反向代理）。预置的 `servers.json` **不能**携带目标服务器的
> 密码（pgAdmin 拒绝导入），首次连接时在 UI 里输入。见
> [预置服务器](#预置服务器)。

## 参数

单容器直接跑上游 `docker.io/dpage/pgadmin4` 镜像（pull-only——pgcli 从不构建
它），全部经由 entrypoint 读取的 `-e` 环境变量配置。下表列出每个旋钮及其默认
值：

| 参数 / `pg.yaml` 键 | 默认值 | 环境变量 | 含义 |
|----------------------|---------|---------|---------|
| `--image` / `image_tag` | `docker.io/dpage/pgadmin4:9.18` | — | 直接指定镜像标签 |
| `--port` / `host_port` | 自动，取自 `pgadmin_start_port`（基址 **5050**） | `PGADMIN_LISTEN_PORT` | Web 宿主端口 |
| `--listen` / `listen` | `127.0.0.1` | `PGADMIN_LISTEN_ADDRESS` | 绑定地址（`0.0.0.0` 对外开放） |
| `--email` / `email` | `admin@pgcli.lan` | `PGADMIN_DEFAULT_EMAIL` | Web 登录账号（会经过镜像校验，见[排障](#排障)） |
| `--password` / `password` | 自动生成，20 字符 | `PGADMIN_DEFAULT_PASSWORD` | Web 登录密码——**不是** PG 密码 |
| `--dsn` / `dsn` | 不设 | —（渲染 `servers.json`） | 以 URI 预注册一个服务器 |
| `--pg-name` | 不设 | —（渲染 `servers.json`） | 改为从本机已管理实例解析该服务器 |
| `--data-dir` / `data_dir` | `<base-dir>/addon/pgadmin/<name>/data` | （挂载到 `/var/lib/pgadmin`） | 会话存储位置 |
| — | `autostart: false` | — | 开机自启（`pg autostart enable --pgadmin`） |

旋钮间的配合关系：

- **`email` + `password` 只在空数据目录上生效。** pgAdmin 只在*初始化*
  `pgadmin4.db` 期间读取 `PGADMIN_DEFAULT_*`；该文件一旦存在，库里的账号就是
  权威，环境变量被忽略。因此复活既有目录的重装会打印一个对它无效的密码（install
  会明说）——要真正重置登录，先 `--clean-data` 移除；复活时也可以把
  `--password` 钉成原值。
- **`--dsn` 与 `--pg-name` 互斥**——二者都表示"预置这个服务器"，一个给 URI，
  一个从本实例解析端点。`--name` 是插件自己的键，与 `--pg-name` 无关。
- **`listen` 默认 loopback**——Web UI 有登录门，所以除非传 `--listen 0.0.0.0`
  从别的主机访问（summary 会警告登录成了唯一防线），它保持 `127.0.0.1`。
- **`email` 必须过得了镜像的校验器**——entrypoint 用 `email_validator` 检查它，
  直接拒绝保留 TLD（`.local`、`.invalid`、`.test`），命中就崩溃重启。默认值
  `admin@pgcli.lan` 正是为通过校验而选；自己起名时同理（`.lan`、`.internal`
  这类由你掌控的名字都行——不要求是真实可投递的地址，投递检查是关掉的）。

## 权限模型

pgAdmin 是插件族里值得单独讲清楚的一个，因为直觉读法（"容器以 uid 5050 写
bind 挂载目录，所以 pgcli 必须预 chown 宿主目录"）是**错的**，而且 pgcli 刻意
没有做 rustfs 需要的那套属主机械。

`dpage/pgadmin4` 的 entrypoint 以**容器 root** 启动，把 `/var/lib/pgadmin`
`chown` 给它自己的 `pgadmin` 用户（uid/gid **5050**），然后 `su-exec` 降权运行
gunicorn。所以 pgcli 传 `--user 0` 然后让开路：

- **无宿主侧 chown。** pgcli 从不对你的数据目录跑
  `chown`/`podman unshare chown`。目录由 install 建成 `0755`，镜像自己在容器
  内修属主。
- **rootless 保持非特权。** rootless podman 下 `--user 0` 是*映射*的——容器
  root 就是你自己的宿主 uid，目录最终归属的 5050 是映射后的 subordinate uid。
  宿主上没有任何东西以真 root 运行。
- **清理走 `removeHostDir`。** 正因为那些文件是映射 uid 的，rootless 下普通
  宿主 `rm -rf` 数据目录会 `EACCES`。
  `pg addon remove pgadmin --clean-data` 经 `podman unshare rm` 兜底回收它们
  ——与 redis/rustfs 同源。（e2e 双向验证：对照组的 `rm` 必须*失败*，而
  `--clean-data` 必须成功。）

e2e 断言容器 `.Config.User == 0` 且容器内 `/var/lib/pgadmin` 是 `5050:0`——
两者合起来证明降权的是*镜像*，不是 pgcli。

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

  Login email:    me@example.com
  Login password: <generated>
  (Web login only — not a PostgreSQL password. Retrieve it later with `pg addon password pgadmin`.)

  Add servers to browse in the web UI itself, or reinstall with --dsn/--pg-name to pre-seed one.
```

`Seeded:` 行只在传了 `--dsn`/`--pg-name` 时出现。重复 `install` 是幂等的：运行
中的容器原样保留，已停的被启动。要让改动的端口/listen/email/预置生效，加
`--force`（容器按配置重建；数据目录不动）。

## 使用 Web UI

把浏览器指向 summary 里的 `URL`（在跑 podman 的那台主机上），用登录邮箱 + 密码
签名进入，pgAdmin 自己的服务器树就出现了。在 UI 里添加连接，或在安装时用
[--dsn / --pg-name](#预置服务器) 预注册一个。

如果主机不在你的浏览器旁边：要么设 `--listen 0.0.0.0`（登录成了唯一防线），要
么保持 loopback 并 SSH 隧道转发端口：

```bash
ssh -L 5050:127.0.0.1:5050 user@host
# 然后本地浏览 http://127.0.0.1:5050/
```

### 取回 Web 登录

```bash
pg addon password pgadmin --name console          # 裸打印，供脚本用
pg addon password pgadmin --name console --file ./pw.txt   # 写成 0600 文件
```

`pg addon list` 显示 URL 和登录邮箱，但不加 `--show-password` **绝不**打印密码。

## 预置服务器

`--dsn` 与 `--pg-name` 都渲染一份一次性的 `servers.json`，pgAdmin 在首次启动
时导入，浏览器打开时那个服务器已在树里。`--pg-name` 从本机已管理实例解析端点
（其 host/port/database/user，读自 `pg.yaml`）；`--dsn` 以 URI 给出：

```bash
pg addon install pgadmin --name dev --pg-name proj01
# 或
pg addon install pgadmin --name dev --dsn "postgres://app@127.0.0.1:5432/appdb"
```

预置携带什么、不携带什么：

- **目标服务器的密码不被存储**——pgAdmin 无法导入密码，所以首次连接时你在 UI
  里输入。pgcli 的 install 明说这一点（"its password is NOT carried"）。这也
  是预置文件可以安全 bind 挂载的原因：`servers.json` 只有
  host/port/database/user。
- **它是一次性便利，不是耦合。** 安装后服务器活在 pgAdmin 自己的 `pgadmin4.db`
  里；在 UI 里增、删、改名都行。无论预置目标是否可达，插件照常运行。
- **`PGADMIN_REPLACE_SERVERS_ON_STARTUP=True`** 在有预置时被设置，所以改了
  `--dsn`/`--pg-name` 的 `--force` 重装会声明式地重新应用预置，而不是留下过期
  条目。

要去掉预置并停止声明式重载，用 `--force` 重装且不带 `--dsn`/`--pg-name`。

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
`pg addon install pgadmin --name console --force`（或对应 flags）后生效。记住
[权限模型](#权限模型)的告诫：`email`/`password` 只对**空**数据目录重新生效，所
以对既有存储改它们需要先 `--clean-data`（或接受生效账号仍是原账号）。

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
例即复活已存服务器与会话存储（但见[权限模型](#权限模型)的注：复活后可用的登录
是原密码，不是新生成的）。`--clean-data` 删除它，当 rootless podman 的映射 uid
使 pgAdmin 的文件对宿主用户不可删时，经 `podman unshare rm` 兜底。空的实例父目
录被修剪；自定义 `data_dir` 的父目录从不动。`servers.json`（若预置过）宿主所
有，两种移除都随实例目录一并删掉。

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
- **从别的机器打不开 UI** —— `listen` 默认 `127.0.0.1`。要么 SSH 隧道转发端口
  （见[使用 Web UI](#使用-web-ui)），要么 `--listen 0.0.0.0 --force` 重装并接
  受 Web 登录成为唯一防线。
- **预置的服务器显示但连不上** —— 预期行为：`servers.json` 从不携带目标密码
  （[预置服务器](#预置服务器)）；在连接对话框里输入。确认 host/port 从 pgAdmin
  所在处可达。
- **install 时找不到镜像** —— `docker.io/dpage/pgadmin4` 按需拉取；离线宿主先
  `podman load` 对应 tar（目录与导出步骤在仓库 `docs/images.md`），pull 会被跳
  过。
- **首次启动响应慢** —— pgAdmin 在 gunicorn 绑定之前先跑 sqlite 迁移；端口要
  等它完成才回 302（跳 `/login`）。负载高的宿主上这可能要几十秒。

## 已知限制

- **无 TLS** —— 这里 pgAdmin 提供明文 HTTP。要 HTTPS 就在前面架反向代理（配合
  `PGADMIN_URL_SCHEME`/`PGADMIN_DISABLE_STATIC_FILE_SERVER` 调参）；pgcli 不接
  这条线。loopback 默认让常见场景留在 `127.0.0.1`。
- **预置不能携带目标密码** —— 这是 pgAdmin 的限制、不是 pgcli 的：
  `servers.json` 密码不可导入。连接输入发生在 UI 里。
- **登录只对新存储生效** —— 见[权限模型](#权限模型)/[排障](#排障)；没有原地改
  密码的路，只有 `--clean-data` + 重装。
- **macOS 代码完备、未实测** —— bridge 路径（发布端口、bridge 下
  `proxyBindHost` 把 loopback 放宽到 `0.0.0.0`）与 redis 同构；尚未在 Mac 上演
  练过。
