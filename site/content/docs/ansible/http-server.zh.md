---
title: "HTTP 文件服务器"
description: "用 pg_http 角色与 deploy-http.yaml 声明式管理 pg http 文件服务器：目录、写模式、TLS、Basic Auth、autostart 与离线二进制分发"
weight: 18
---

`playbooks/deploy-http.yaml` 把 [pg http](/docs/toolbox/http/) 文件服务器纳入
Ansible 管理。`pg http` 是宿主机进程（不占容器、没有 add-on 类型），因此这里用
独立的 `pg_http` 角色和独立 playbook，不经过 `pg_addon` / `apply.yaml`，与
`deploy-s3.yaml` 的组织方式一致。

角色逐条协调 `pgcli_http_servers` 中声明的服务器：按名字比对运行中的规格，有差异
就 `pg http stop` + `pg http start`；创建被服务目录；仅在配置
`sync_binaries: true` 时复制离线二进制；并按需要注册开机自启。默认不复制二进制。
重复运行会收敛到声明状态。

## 配置

inventory 里准备 `[http]` 组（`hosts.ini`，gitignored），例如：

```ini
[http]
node-http ansible_host=192.0.2.11 ansible_user=root
```

服务器列表放在 `group_vars/http/main.yml`（gitignored，从同目录的 `.example`
复制）。所有字段都是可选的，未写时使用括号中的默认值：

| 字段 | 对应参数 | 说明 |
|------|----------|------|
| `name` | `--name`（必填） | 服务器名，同时是状态文件名 |
| `host` | — | 只在该 inventory 主机上协调；省略则在所有目标主机上 |
| `state` | — | `started`（默认）/ `stopped` / `absent` |
| `autostart` | — | 注册开机自启（默认 `true`，仅 `state: started` 时允许） |
| `dir` | `--dir` | 被服务目录（默认 `/srv/offline-images`），不存在时创建并归属部署账号 |
| `listen` | `--listen` | 监听地址（默认 `127.0.0.1`） |
| `port` | `--port` | 监听端口（默认 `18080`） |
| `allow_upload` | `--allow-upload` | 上传表单（默认 `false`） |
| `allow_dir` | `--allow-dir` | 新建目录表单（默认 `false`） |
| `allow_delete` | `--allow-delete` | 每文件删除按钮（默认 `false`） |
| `max_upload` | `--max-upload` | 单次上传上限（默认 `1G`，如 `512M`） |
| `admin_ips` | `--admin-ip` | 写模式的来源地址白名单（列表，逗号拼接后传入） |
| `tls` | `--tls` | 用自签名证书启用 HTTPS（证书在 pgcli base dir 下自动生成与续期） |
| `tls_cert` | `--tls-cert` | 目标主机上的 PEM 证书路径；隐含启用 TLS |
| `tls_key` | `--tls-key` | 与 `tls_cert` 成对的私钥路径 |
| `auth` | `--auth` | Basic Auth 账号，每项 `USER:PASSWORD`（列表） |
| `auth_env` | `--auth-env` | 控制器上的环境变量名列表，值为 `USER:PASSWORD` |
| `auth_file` | `--auth-file` | 目标主机上的 htpasswd 文件列表（bcrypt 条目） |
| `force` | — | 规格一致也强制重启（默认 `false`） |
| `sync_binaries` | — | 是否把离线二进制复制进 `dir`（默认 `false`） |

## 示例

最简：把离线目录通过 HTTP 暴露到局域网，并注册开机自启。

```yaml
pgcli_http_servers:
  - name: images
    dir: /srv/offline-images
    listen: 0.0.0.0
    port: 18080
    autostart: true
```

写模式限制到指定来源，并调整上传上限：

```yaml
  - name: share
    dir: /srv/share
    listen: 0.0.0.0
    port: 18081
    allow_upload: true
    allow_dir: true
    allow_delete: true
    max_upload: 512M
    admin_ips:
      - 192.0.2.20
      - 192.0.2.21
```

TLS 两种写法（`tls: true` 用自动生成的自签证书；证书/私钥路径指目标主机）：

```yaml
  - name: images
    tls: true
  - name: files
    tls_cert: /etc/pgcli-http/files.crt
    tls_key: /etc/pgcli-http/files.key
```

Basic Auth 三种来源可以组合，但同一个用户名只能出现在一处：

```yaml
  - name: images
    auth:
      - "alice:s3cret"          # 明文写在 gitignored 的 group_vars 里
    auth_env:
      - PGCLI_HTTP_AUTH         # 控制器环境变量，值形如 bob:hunter2
    auth_file:
      - /home/pgcli/pg/data/http/htpasswd
```

`auth_env` 在控制器上 `lookup('env', ...)` 取值，只把变量名作为 `--auth-env`
参数传给目标主机；带明文 `auth` 或 `auth_env` 时启动任务整体 `no_log`，默认输出
与摘要里都不出现口令。

htpasswd 文件用 pgcli 自己维护（增量添加，保留其它账号）：

```bash
printf '%s\n' "$PASSWORD" | pg http htpasswd alice
# 默认写入 <base_dir>/http/htpasswd；也可 --file 指定
```

## 二进制同步

默认不复制二进制。若要分发，在对应 server 上设置 `sync_binaries: true`；角色会先把
控制端的 `bin/pg` 刷新到 `images/pg`，然后只把 `pgcli_http_binaries`（默认 `pg` 与
`podman-launcher-linux_amd64`）复制进该 server 的 `dir`。**镜像 tar 包不在这里搬运**，
请先用 `scripts/export-images.sh` 导出到该目录（见[离线镜像服务器](./image-server/)）。

启用二进制复制时，控制端必须先 `make build`，且 `images/` 下要有其余二进制；否则
playbook 会在修改任何主机前失败。可以在 `group_vars/http/main.yml` 里覆盖：

```yaml
pgcli_http_binaries: [pg, podman-launcher-linux_amd64]
pgcli_http_default_dir: /srv/offline-images
```

服务器主机不运行容器，所以安装阶段默认跳过容器镜像：不检查离线镜像服务器、
不加载镜像，安装脚本也带 `--skip-pull`。这在上游分发机（本机没有 pg、也不需要
本地镜像）上省掉一轮大文件传输。如果该主机同时还要承载其它容器的镜像，用
`-e pgcli_load_images=true -e pgcli_install_skip_pull=false` 覆盖。

## 运行

```bash
cd ansible

# 安装/更新 pgcli 并协调 [http] 组上的服务器
uv run ansible-playbook playbooks/deploy-http.yaml --limit http

# 指定主机
uv run ansible-playbook playbooks/deploy-http.yaml -e HOSTS=node-http

# 只跑协调阶段（跳过安装角色）
uv run ansible-playbook playbooks/deploy-http.yaml --limit http --skip-tags pgcli
```

playbook 分两个阶段：`pgcli` 标签负责安装与主机配置，`http` 标签只协调服务器。
服务以部署账号运行（rootless 场景即 `pgcli` 用户）；`/srv` 下的被服务目录由 root
创建后 chown 给部署账号。

## 幂等与生命周期

- 规格比对覆盖 `dir`、`listen`、`port`、三个写模式开关、`max_upload`、`admin_ips`、
  TLS、证书/私钥路径以及认证用户名集合与密码/哈希。任何一项变化都会重启服务器。
- 重启前先 `pg http stop`，顺带清理 stale 状态文件，避免 `pg http start` 产生
  `images-2` 这样的新名字。
- 端口被占用时 `pg http` 会自动顺延到后续端口；角色在启动后核对状态文件，若实际
  端口与声明不符直接失败，避免消费者使用错误的 URL。
- 健康检查请求 `http(s)://<listen>:<port>/__pgcli_health`（TLS 下跳过证书校验），
  该端点不需要认证。
- `autostart: true` 时用 `pg autostart enable --http --name` 把运行规格快照进
  `pg.yaml`；之后每次重启都会刷新快照。`state: stopped` 或 `autostart: false`
  时删除快照。
- `state: absent` 先停服务再删除状态文件。

## 注意

- 认证按用户名和密码比对：新增/删除账号、修改已有账号的密码都会触发重启。内联
  `auth` 与 `auth_env` 的密码在控制端对运行规格里的 bcrypt 哈希做校验；
  `auth_file` 条目按哈希逐条比对，所以在目标机用 `pg http htpasswd`、
  `htpasswd -B` 改过的账号下次运行就会识别。运行中的进程缓存启动时读到的哈希，
  因此角色一旦发现差异就 stop + start。
- `auth_file` 只在启动时读取一次，手工改文件后要么等下次 playbook 运行，要么
  显式 `force: true` 强制重启。
- 手工 `pg http start` 起过同名服务器也能被接管：规格一致就保持不动，不一致会被
  按声明重启（手工加的、未在配置里声明的参数会丢失）。
- 不配置认证时站点对所有人开放；`0.0.0.0` 只应在可信网段使用，或配合
  `admin_ips` / 防火墙。`--tls` 只加密流量，不认证客户端。
- 安装阶段有一个"初始化宿主机"任务每次都会报 `changed`（安装脚本的既有行为），
  协调阶段在状态一致时应为 `changed=0`。
