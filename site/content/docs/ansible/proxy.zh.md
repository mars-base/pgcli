---
title: "出网代理"
description: "在隔离机房中通过正向代理完成安装、镜像拉取、运行时下载与扩展构建"
weight: 16
---

机房里的数据库主机通常没有直连公网的出口。在一台可联网的机器上运行 Privoxy
add-on，并把 playbook 指向它，安装、镜像拉取、运行时下载和扩展构建就共用这条
出网通道。

```text
受管主机 ── HTTP / CONNECT ──▶ Privoxy（联网主机）──▶ 公网
```

## 启动代理

在可联网主机上执行；同样的 pgcli 二进制和镜像集就够了，不需要额外安装软件：

```bash
pg addon install privoxy --name egress --allow-network 10.0.0.0/8
```

- Privoxy 默认监听 `0.0.0.0:8118`。`--allow-network` 是客户端白名单；未配置
  ACL 且监听非回环地址时，安装会打印开放代理警告。
- 同一个监听同时服务明文 HTTP 和 `CONNECT` 隧道的 HTTPS，因此一个 URL 就够。
- 在将要使用代理的主机上验证：

```bash
curl -x http://192.0.2.10:8118 -o /dev/null -w '%{http_code}\n' https://repo.pigsty.io/
```

ACL 语法、自定义 SOCKS 上游、自启动和日志见 [Privoxy](../addon/privoxy/)。

## 配置 Ansible

在 `group_vars/all/main.yml` 中设置两个变量（文件不存在时先从
`main.yml.example` 复制）：

```yaml
pgcli_proxy_url: "http://192.0.2.10:8118"
pgcli_no_proxy: "localhost,127.0.0.1,::1"
```

`pgcli_proxy_url` 会以 `HTTP_PROXY` / `HTTPS_PROXY`（大小写各一份）导出给部署
账号。留空表示直连：此时派生的 `pgcli_proxy_env` 为 `{}`，所有 play 的行为与
未启用时完全一致。可以用下面的命令确认求值结果：

```bash
cd ansible
uv run ansible <host> -m debug -a 'var=pgcli_proxy_env'
```

### 哪些步骤走代理

| 步骤 | 走代理的方式 |
|------|--------------|
| 安装 play | `scripts/install.sh`（apt、release 下载）以及所有 `get_url` |
| 镜像 | `podman pull`、镜像加载、add-on 镜像懒加载 |
| 运行时下载 | `pg addon install …`、`pg sqlite install` —— Go HTTP 客户端 |
| 扩展构建 | `pg extension install` 背后的 `podman build` |

### 哪些不走代理

- 运行中的服务：容器创建时带 `--http-proxy=false`，PostgreSQL、备份、
  pgbouncer、PostgREST 和 Gitea 都不会继承代理。
- Patroni 与备份的兜底镜像构建仍显式禁用代理。
- `offline-check.yml` 的局域网可达性探测不经过代理。

## NO_PROXY

playbook 会自动追加四个离线 URL（`pgcli_install_binary_url`、
`pgcli_podman_binary_url`、`pgcli_download_mirror`、
`pgcli_images_http_url`）的主机名，以及 inventory 中所有 `private_ip`。

其它内网服务请按**具体 IP 或域名**补充：Python（Ansible `get_url`/`uri`）和
apt 不支持 `no_proxy` 里的 CIDR，`10.0.0.0/8` 这类网段只对 curl 和 Go 客户端
有效。局域网镜像与出网代理可以同时使用——镜像主机会自动加入 `NO_PROXY`。

## 不通过 Ansible 手动执行

在 SSH 终端里先导出同样的变量，再执行 `pg`：

```bash
export HTTP_PROXY=http://192.0.2.10:8118
export HTTPS_PROXY=http://192.0.2.10:8118
export NO_PROXY=localhost,127.0.0.1,::1
```

`pg` 以及它调用的 `podman` 命令会继承这些变量。注意三点：

- `sudo` 默认会重置环境——用 `sudo -E`、`sudo env HTTP_PROXY=... pg ...`，
  或在 sudoers 中配置 `env_keep`。
- 导出只对当前 shell 及其子进程有效；`pg start --autostart` 等开机路径看不到。
- pgcli 创建的容器仍带 `--http-proxy=false`，代理不会进入长驻服务。

## 安全

优先使用 Privoxy 的客户端 ACL，而不是 `user:pass@` 形式的 URL：代理凭据会出现在
进程环境（`/proc/<pid>/environ`）和 Ansible 详细日志里。监听地址应限制在私网，
ACL 白名单尽量收窄。

## 验证整条链路

1. 在每台受管主机上，用上面的 `curl -x` 访问一个外网地址。
2. 对测试实例执行一次扩展安装：

   ```bash
   pg extension install hll -i <instance>
   ```

   构建需要访问 `repo.pigsty.io`、`apt.postgresql.org` 和 `deb.debian.org`。
   可查看 `pg logs addon privoxy --name egress`（`logfile /dev/stdout`），
   或在构建期间抓取 8118 端口的流量。
3. 确认重建后的容器环境是干净的：

   ```bash
   podman exec <container> env | grep -i proxy || echo clean
   ```

如果扩展构建没有走代理，先确认执行 `pg extension install` 的进程确实带有这些
变量（Ansible 会在安装 play 中设置）。内网请求意外走了代理，通常是 `NO_PROXY`
里缺少对应的具体 IP 或域名。
