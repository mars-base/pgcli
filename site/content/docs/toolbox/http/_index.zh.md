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
复制按钮；文件本身支持 `Range` 与 `HEAD` 请求。

默认监听 `127.0.0.1:18080` 并服务当前目录。服务器是脱离终端的独立进程，关闭
终端不会影响它；同一主机可以同时运行多个服务器。

```bash
pg http start
pg http status
pg http restart <NAME>
pg http stop
```

## 启动服务器

```bash
pg http start [--name NAME] [--port 18080] [--listen 127.0.0.1] \
  [--dir .] [--tls] [--tls-cert FILE --tls-key FILE]
```

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `http-<请求端口>` | `status`/`stop` 使用的名字。若已有同名服务器在运行，会自动追加 `-2`/`-3` |
| `--port` | `18080` | 端口被占用时自动尝试下一个空闲端口（最多 100 个） |
| `--listen` | `127.0.0.1` | 填 `0.0.0.0` 可暴露到网络 |
| `--dir` | `.` | 要服务的目录；启动时解析为绝对路径，且必须存在 |
| `--tls` | 关闭 | 使用自动生成的自签名证书提供 HTTPS |
| `--tls-cert` / `--tls-key` | — | 改用现有证书/私钥对（隐含 `--tls`） |

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
NAME                    PID      STATUS    URL                                      DIR
http-18080              412345   running   http://127.0.0.1:18080/                  /srv/share
http-18081              412410   running   https://0.0.0.0:18081/                   /srv/docs
```

进程已退出的状态文件会标记为 `stale`；下次 `stop` 时自动清理。

重启会先按 `stop` 的方式优雅停止，再用状态文件里记录的名称、监听地址、目录、
TLS 证书与端口重新拉起 —— 不需要重复传 `start` 的参数。原端口若已被占用，会
自动尝试下一个空闲端口；自动生成的证书在临近过期或不再覆盖监听地址时重新签发：

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

## 目录列表

- 目录排在文件之前，然后按名称不区分大小写排序。目录链接带尾部斜杠；面包屑和
  `..` 链接（根目录除外）用于向上导航。
- **不会**自动服务 `index.html` —— 目录始终渲染列表。
- 仅允许 `GET` 与 `HEAD`，其它方法返回 `405`。
- `/__pgcli_health` 返回 `200 ok`，供就绪探测使用，并会屏蔽同名的真实文件。
- 符号链接会被跟随，包括指向所服务目录之外的链接。

每个文件行都有一个 **copy** 按钮。点击后复制该文件的绝对 URL
（`http(s)://host:port/path`），并在按钮旁显示 `copyed` 约 1.5 秒。Clipboard API
只在安全上下文可用，因此通过普通 HTTP 访问时页面会回退到隐藏 textarea +
`document.execCommand('copy')`；两条路径在仍支持 execCommand 的浏览器中均可工作。

## 状态与生命周期

| 路径 | 内容 |
|---|---|
| `<base>/http/servers/<name>.json` | 名称、pid、端口、监听地址、目录、TLS 路径、日志路径 |
| `<base>/http/servers/<name>.log` | 服务器 stdout/stderr |
| `<base>/http/tls/<name>/cert.pem`、`key.pem` | 自动生成的证书对（`--tls`） |

`<base>` 取 `pg.yaml` 中的 `base_dir`（配置存在时），否则用平台默认目录（通常
为 `~/.pgcli`）。`pg http` 无需配置文件也能工作。

服务器以 `setsid` 启动，关闭终端不会停止它们。它们**不会**开机自启；重启主机后
需要再次执行 `pg http start`。`status` 通过 `/proc/<pid>/cmdline` 校验进程，
因此被复用的 PID 不会被误认为运行中的服务器。

## 安全与限制

- **没有认证。** 任何能访问该端口的人都能读取所服务目录树中的全部文件。默认
  只监听回环是唯一防线；仅在受信网络中才使用 `0.0.0.0`。
- **`--tls` 只加密、不认证客户端** —— 它不是访问控制手段。
- 符号链接可能暴露 `--dir` 之外的路径；把所服务的目录视为你愿意共享内容的边界。
- 每个服务器服务一棵目录树；需要共享多个目录时启动多个服务器。
- 日志只追加，pgcli 不做轮转。
