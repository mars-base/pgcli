---
title: "HTTP 文件服务器"
description: "通过 HTTP(S) 共享目录，提供 autoindex 风格的文件列表和每行复制按钮"
weight: 55
icon: fa-solid fa-share-nodes
aliases: ["/toolbox/http/"]
menus:
  main:
    identifier: docs-toolbox-http
    parent: docs-toolbox
    weight: 55
    params:
      icon: fa-solid fa-share-nodes
cascade:
  type: docs
  footer_style: slim
---

`pg http` 直接在宿主机上启动一个轻量静态文件服务器（不使用容器），用于在不同
机器之间快速共享文件。目录会渲染成 nginx autoindex 风格的列表，每个文件行都带
复制按钮；文件本身支持 `Range` 与 `HEAD` 请求。服务器默认只读：以
`--allow-delete` 启动时每个文件行会多出删除按钮，`--allow-upload` 会在列表页
提供上传表单，`--allow-dir` 提供新建目录表单；`--admin-ip` 可把这三种写模式
限制到指定来源 IP。

默认监听 `127.0.0.1:18080` 并服务当前目录。服务器是脱离终端的独立进程，关闭
终端不会影响它；同一主机可以同时运行多个服务器。

```bash
pg http start
pg http status
pg http restart <NAME>
pg http stop
pg autostart enable --http --name <NAME>
```

## 启动服务器

```bash
pg http start [--name NAME] [--port 18080] [--listen 127.0.0.1] \
  [--dir .] [--tls] [--tls-cert FILE --tls-key FILE] \
  [--allow-delete] [--allow-upload] [--allow-dir] [--max-upload SIZE] \
  [--admin-ip IP[,IP...]]
```

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `http-<请求端口>` | `status`/`stop` 使用的名字。若已有同名服务器在运行，会自动追加 `-2`/`-3` |
| `--port` | `18080` | 端口被占用时自动尝试下一个空闲端口（最多 100 个） |
| `--listen` | `127.0.0.1` | 填 `0.0.0.0` 可暴露到网络 |
| `--dir` | `.` | 要服务的目录；启动时解析为绝对路径，且必须存在 |
| `--tls` | 关闭 | 使用自动生成的自签名证书提供 HTTPS |
| `--tls-cert` / `--tls-key` | — | 改用现有证书/私钥对（隐含 `--tls`） |
| `--allow-delete` | 关闭 | 在每个文件行提供删除按钮并接受 HTTP `DELETE`。没有认证，见「安全与限制」 |
| `--allow-upload` | 关闭 | 在列表页提供上传表单并接受 multipart `POST`，文件写入当前浏览的目录。没有认证，见「安全与限制」 |
| `--allow-dir` | 关闭 | 在列表页提供新建目录表单，按相对路径创建目录。没有认证，见「安全与限制」 |
| `--max-upload` | `1G` | 单个上传请求（所有文件合计）的大小上限，支持 `512M`、`2G` 或字节数 |
| `--admin-ip` | 空 | 允许使用写模式的来源 IP，逗号分隔；空表示不限制来源。其它来源只能看到只读列表，写请求返回 `403` |

`--name` 允许字母、数字、`.`、`_` 和 `-`。

输出示例：

```
✓ HTTP server started: http-18080
  URL:  http://127.0.0.1:18080/
  Dir:  /srv/share
  PID:  412345
  Log:  /root/.pgcli/http/servers/http-18080.log

  Stop with: pg http stop http-18080
```

使用 `--listen 0.0.0.0` 时打印的 URL 会包含 `0.0.0.0`；客户端应改用主机的实际
地址访问。

### 多实例与端口自动递增

每次 `pg http start` 都是一个独立服务器。请求的端口被占用时，pgcli 会提示并绑定
下一个空闲端口：

```bash
pg http start --dir /srv/share          # 18080
pg http start --dir /srv/docs           # 18081，同一主机
```

```
  [!] port 18080 is in use, using 18081
```

名字与仍在运行的服务器冲突时同样处理，自动追加数字后缀。

### TLS

`--tls` 使用与 `pg cert` 相同的代码生成自签名证书：

- 存放于 `<base>/http/tls/<name>/cert.pem`（0644）与 `key.pem`（0600）。
- SAN 覆盖回环地址与给定的 `--listen` 地址；监听通配地址（`0.0.0.0`/`::`）时
  覆盖本机所有网卡地址。
- 证书剩余有效期超过 30 天且仍覆盖监听地址时复用，否则重新生成。

证书本身就是信任锚（没有单独的 CA），客户端用 `--cacert` 验证：

```bash
curl --cacert ~/.pgcli/http/tls/http-18080/cert.pem \
  https://127.0.0.1:18080/
```

要使用 `pg cert` 或其它工具签发的证书，传入 `--tls-cert` 与 `--tls-key`。pgcli
会校验证书与私钥匹配、处于有效期内，并直接引用（不复制）。请确保证书 SAN 包含
客户端会使用的所有地址。

## 查看、重启与停止

```bash
pg http status
```

```
NAME                     PID      STATUS    AUTOSTART DELETE  UPLOAD  MKDIR   ADMIN             URL                                      DIR
http-18080               412345   running   -         -       -       -       all               http://127.0.0.1:18080/                  /srv/share
http-18081               412410   running   enabled   enabled enabled enabled 192.0.2.10,192.0.2.11 https://0.0.0.0:18081/              /srv/docs
```

`AUTOSTART` 表示该服务器是否已记录到 `pg.yaml` 用于开机自启（见下文）；
`DELETE`、`UPLOAD`、`MKDIR` 分别表示它以哪些写模式启动，`ADMIN` 列出允许使用
这些模式的来源 IP（不限制时为 `all`）。`stop` 对启用自启的服务器会额外打印
提示，因为它们会在下次开机时重新启动。

进程已退出的状态文件会标记为 `stale`；下次 `stop` 时自动清理。

重启会先按 `stop` 的方式优雅停止，再用状态文件里记录的名称、监听地址、目录、
TLS 证书、写模式（删除/上传/建目录）、上传大小上限、admin IP 限制和端口重新
拉起 —— 不需要重复传 `start` 的参数。原端口若已被占用，会自动尝试下一个空闲端口；自动生成的证书在临近过期或
不再覆盖监听地址时重新签发：

```bash
pg http restart http-18080        # 重启一个
pg http restart http-18080 docs   # 重启多个
pg http restart                   # 不带名字 = 全部重启
pg http restart --all
```

```bash
pg http stop http-18080           # 停止一个
pg http stop http-18080 docs      # 停止多个
pg http stop                      # 不带名字 = 全部停止
pg http stop --all
```

停止时先发 `SIGTERM`，最多等待 `--timeout`（默认 `5s`，与服务端优雅退出的窗口
一致），仍未退出则 `SIGKILL`。状态文件会被删除；日志与证书保留在磁盘上。
`restart` 同样接受 `--timeout` 与 `--all`。

## 目录列表

- 目录排在文件之前，然后按名称不区分大小写排序。目录链接带尾部斜杠；面包屑和
  `..` 链接（根目录除外）用于向上导航。
- **不会**自动服务 `index.html` —— 目录始终渲染列表。
- 仅允许 `GET` 与 `HEAD`，其它方法返回 `405`。启用 `--allow-delete` 后文件还
  允许 `DELETE`；启用 `--allow-upload` 或 `--allow-dir` 后目录允许 `POST`。
  `405` 响应在 `Allow` 头中列出当前实际放行的方法组合。
- `/__pgcli_health` 返回 `200 ok`，供就绪探测使用，并会屏蔽同名的真实文件；
  它永远不可删除，也不能被上传覆盖，或在根目录下创建同名目录。
- 符号链接会被跟随，包括指向所服务目录之外的链接。指向目录的符号链接按目录
  展示，`DELETE` 会像普通目录一样拒绝它。

每个文件行都有一个 **copy** 按钮。点击后复制该文件的绝对 URL
（`http(s)://host:port/path`），并在按钮旁显示 `copyed` 约 1.5 秒。Clipboard API
只在安全上下文可用，因此通过普通 HTTP 访问时页面会回退到隐藏 textarea +
`document.execCommand('copy')`；两条路径在仍支持 execCommand 的浏览器中均可工作。

### 删除文件

启用 `--allow-delete` 后，每个文件行的 copy 按钮右侧会多出一个 **delete**
按钮。第一次点击会把它变成红色的 `confirm?` 并保持 3 秒；再次点击会发送
`DELETE /path`，从页面移除该行并删除文件。未在 3 秒内确认则自动恢复为
`delete`。

- 目录（包括空目录）永远不会被删除，对目录 `DELETE` 返回 `405`。
- 删除符号链接只解除链接本身，不影响它指向的文件；通过符号链接目录解析到
  `--dir` 之外的路径会被拒绝（`403`）。
- 删除成功返回 `204 No Content`，文件不存在返回 `404`。

`pg http restart` 会保留状态文件中记录的删除/上传/建目录模式。需要改变时，
先 `stop` 再用相应参数重新 `start`。

### 上传文件

启用 `--allow-upload` 后，列表表格上方会出现上传表单，可一次选择多个文件。
上传目标就是页面标题 `Directory listing for /<路径>` 显示的目录——在哪个目录
浏览，文件就落在哪个目录；不存在的子路径不会被自动创建。

- `curl -F files=@a.txt -F files=@b.txt http://host:port/sub/` 等价于在 `/sub/`
  页面上传。
- 单个请求（所有文件合计）默认上限 1 GiB，可用 `--max-upload` 调整，超出返回
  `413`，响应中带上当前上限。页面在提交前会合计所选文件大小，超出上限时直接
  拒绝、不发请求；服务端仍会对任何客户端强制执行该上限。
- 同名文件采用**原子覆盖**：先写入同目录的隐藏临时文件，完成后 rename 到目标
  名，中断不会损坏原文件。目标是目录（含指向目录的符号链接）时返回 `409`。
- 文件名只保留基础名（浏览器带来的路径会被丢弃）；以 `.pgcli-upload-` 开头的
  名字，以及根目录下的 `__pgcli_health`，会被拒绝（`400`）。
- 写入始终被限制在 `--dir` 之内：经过指向外部的符号链接目录会返回 `403`。
- 页面通过 `XMLHttpRequest` 提交，传输过程中在表单下方显示进度条（百分比和
  已发送/总字节）；成功后自动刷新列表，失败时恢复按钮并提示原因。浏览器禁用
  JS 时退化为普通表单，完成后以 `303` 回到当前目录。多个文件中途失败时，已
  写入的文件保留。

### 创建目录

启用 `--allow-dir` 后，列表表格上方会出现新建目录表单。输入相对路径（如
`images/v2`），目录会创建在当前浏览的目录下，缺失的中间目录一并创建。

- 同一操作也接受普通表单：`curl -d 'dir=images/v2' http://host:port/sub/`。
- 目标已存在（文件或目录）时返回 `409`，不会静默成功。
- 绝对路径、任何 `..` 组件、空路径返回 `400`；根目录下的 `__pgcli_health`
  为保留名，同样返回 `400`。
- 创建成功后页面自动刷新；无 JS 时以 `303` 回到当前目录。

### 按来源 IP 限制写操作

`--admin-ip` 限定哪些来源可以使用写模式。可以只填一个地址，也可以填逗号分隔的
列表；IPv4 与 IPv6 都支持，逗号两侧的空格会被忽略：

```bash
pg http start --allow-upload --allow-dir --allow-delete \
  --admin-ip 192.0.2.10,192.0.2.11,2001:db8::7 --dir /srv/share
```

- 参数为空（默认）时**不限制来源**：完全由写模式参数决定，和以前一致。
- 一旦配置，其它来源仍然可以正常浏览，但页面是只读的——没有上传表单、没有
  新建目录表单、没有删除按钮，页脚也不会出现 `delete enabled`/`upload
  enabled`/`mkdir enabled`——并且其 `POST` 与 `DELETE` 一律返回 `403`。读请求
  不受此参数影响。
- 判断依据是直接 TCP 连接的来源地址（`RemoteAddr`），不信任
  `X-Forwarded-For`。如果前面有反向代理，所有请求看起来都来自代理地址，此时
  列表必须包含代理地址，按客户端区分就失效了。
- 列表中有非法条目时 `pg http start` 直接报错，不会静默丢弃。`pg http status`
  的 `ADMIN` 列会显示该列表。
- 这是粗粒度的网络限制，不是认证：地址可能通过 NAT 被共享，能合法使用列表中
  地址的人都可以写入。

## 状态与生命周期

| 路径 | 内容 |
|---|---|
| `<base>/http/servers/<name>.json` | 名称、pid、端口、监听地址、目录、TLS 路径、写模式（删除/上传/建目录）、上传大小上限、admin IP、日志路径 |
| `<base>/http/servers/<name>.log` | 服务器 stdout/stderr |
| `<base>/http/tls/<name>/cert.pem`、`key.pem` | 自动生成的证书对（`--tls`） |

`<base>` 取 `pg.yaml` 中的 `base_dir`（配置存在时），否则用平台默认目录（通常
为 `~/.pgcli`）。`pg http` 无需配置文件也能工作。

服务器以 `setsid` 启动，关闭终端不会停止它们。默认**不会**开机自启；重启主机后
需要再次执行 `pg http start`，或启用下面的自动启动。

### 状态是如何得出的

`pg http start` 会 fork 出隐藏子命令 `pg http serve` 并等待它完成端口绑定。子进程
把实际的 `{listen, port}` 写入短生命周期的 `<name>.ready` 文件；父进程随后写
`<name>.json`（先写临时文件再 rename，即使崩溃也不会留下半截状态）。

`pg http status` 不会连接服务器，只读取这些 `*.json`，跳过损坏文件；只有当记录的
PID 仍然存在、且（Linux 上）`/proc/<pid>/cmdline` 仍包含 `http`、`serve` 和服务器名
时才判定为 `running`——因此被复用的 PID 不会被误认为存活。URL 与目录来自状态文件，
`AUTOSTART` 来自 `pg.yaml`。

## 开机自启

服务器可以随主机启动自动拉起。先启动一次，再按名称启用自启：

```bash
pg http start --name docs --dir /srv/docs --port 18080
pg autostart enable --http --name docs
```

启用时会把服务器的目录、监听地址、端口、TLS 材料、写模式（删除/上传/建目录）、
上传大小上限和 admin IP 列表快照到 `pg.yaml` 顶层的 `http` 段；开机服务据此
重新创建服务器。
若记录的端口在开机时已被占用，会尝试下一个空闲端口。之后用同名 `pg http
start`/`pg http restart` 会刷新这份快照。

```bash
pg autostart status                        # 显示 "http docs   enabled"
pg autostart disable --http --name docs    # 删除快照；不会停止正在运行的服务器
```

开机服务本身的说明见[开机自启](/docs/autostart/)。配置中没有 `http` 条目时，
`pg http` 的行为与上文完全一致：服务器只脱离终端，不跨重启存活。

## 安全与限制

- **没有认证。** 任何能访问该端口的人都能读取所服务目录树中的全部文件。默认
  只监听回环是唯一防线；仅在受信网络中才使用 `0.0.0.0`。
- **写模式（`--allow-delete`、`--allow-upload`、`--allow-dir`）都不带认证** ——
  任何能访问该端口的人都可以删除、上传文件，或在 `--dir` 下创建目录。写操作不会
  逃逸所服务目录树，`POST` 还会校验浏览器 `Origin` 与请求 `Host` 一致以阻止
  跨站表单提交，但仍应像上文一样保持回环监听。启用任一写参数且监听地址不是
  回环时，`pg http start` 会打印警告。`--admin-ip` 可以把写操作收窄到列出的
  来源地址，并对其它来源隐藏写 UI，但它仍然不是认证——见上文「按来源 IP 限制
  写操作」。
- **`--tls` 只加密、不认证客户端** —— 它不是访问控制手段。
- 符号链接可能暴露 `--dir` 之外的路径；把所服务的目录视为你愿意共享内容的边界。
- 每个服务器服务一棵目录树；需要共享多个目录时启动多个服务器。
- 日志只追加，pgcli 不做轮转。
