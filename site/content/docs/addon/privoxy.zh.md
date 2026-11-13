---
title: "Privoxy"
description: "以 pgcli 插件方式运行 Privoxy——HTTP/HTTPS 正向代理，支持客户端 ACL、端口自动分配，不做 TLS 中间人"
weight: 54
---

[Privoxy](https://www.privoxy.org/) 是非缓存正向代理：明文 HTTP 直接转发，
HTTPS 通过 `CONNECT` 隧道转发。它**不做 TLS 中间人**，因此 pgcli 不管理任何
CA 或证书——客户端原有的信任链继续生效。pgcli 将其作为**独立的顶层插件**运行，
生命周期（install / start / stop / logs / autostart / remove）与其他插件一致，
全部通过 `pg.yaml` 管理。

典型用途：

- 给没有直连出口的主机/容器提供统一出网代理（或统一的审计出口地址），
- 为包管理器、`curl` 和集群内应用提供 `HTTP_PROXY` / `HTTPS_PROXY` 端点，
- 做网络层面的访问闸门：只有 `--allow-network` 中的客户端能用。

> **平台支持：** Linux 使用 host 网络；macOS 使用 pgcli-net 桥接网络并发布端口。
> 镜像为多架构（amd64 + arm64）。

## 工作原理

pgcli 根据参数渲染完整的 Privoxy `config`，以只读方式挂载到
`/etc/privoxy/config`。日志走容器 stdout（`logfile /dev/stdout`），用
`pg logs addon privoxy` 查看。镜像自带的 entrypoint 会被绕过
（`--entrypoint /usr/sbin/privoxy`），因为它会就地改写配置文件，在只读挂载上
会失败。

渲染出的默认配置刻意保持最小且安全：关闭缓存、关闭远程 toggle 和 CGI 编辑、
不启用请求拦截、不转发代理认证头。模板不加载 actions 文件，因此是纯转发代理；
需要内容过滤时用 `--conf-file`。

### 端口分配

每个实例有独立端口。第一个从 `privoxy_start_port`（默认 **8118**）开始，
后续实例依次递增；宿主上已经被监听的端口（`ss -tlnH` 检测）会自动跳过，
所以同机安装多个代理不会互相冲突，也不会撞上已有服务：

```bash
pg addon install privoxy --name p1     # 8118
pg addon install privoxy --name p2     # 8119
pg addon install privoxy --name p3     # 8120（除非宿主服务占用了它）
```

用 `--port N` 显式指定端口（与另一个 privoxy 实例已占用的端口冲突会直接报错；
被非 pgcli 进程占用的端口会在容器启动时报错）。

### 客户端 ACL

ACL 使用 Privoxy 原生语法（`host`、`addr[/mask][:port]`，以及可选的
`客户端规则 目的地址规则` 两段式）。要特别注意 Privoxy 自身的两条语义：

1. 一旦出现任何 `permit-access` / `deny-access` 行，**默认就是拒绝**。
2. **最后一个匹配的规则生效**。

因此 pgcli 固定先渲染 permit、再渲染 deny：

| 参数 | 渲染结果 | 含义 |
|------|----------|------|
| `--allow-network A,B` | `permit-access A`、`permit-access B` | 白名单——其它客户端全部拒绝 |
| `--deny-network C` | `permit-access 0.0.0.0/0`、`deny-access C` | 黑名单——除 C 外全部允许 |
| `--allow-network A --deny-network C` | 先 permit 后 deny | 允许一个大网段，再拒绝其中更窄的地址 |

两个参数都可重复，**也支持逗号分隔**，代码里会合并成一个列表：
`--allow-network 10.0.0.0/8,192.168.0.0/16 --allow-network 172.16.0.0/12`
等于三条白名单；`--deny-network` 同样可以一次写多个地址。重装时传任一 ACL
参数会**替换**已存列表；两个都不传则清空。
没有任何 ACL 且监听非回环地址时，安装会打印**开放代理**警告——能连到端口的人
都能借它出网。除非端口已有防火墙保护，否则请绑定 `--listen 127.0.0.1` 或加 ACL。

```bash
# 黑名单多个客户端：可重复，也可逗号分隔（会合并）
pg addon install privoxy --name p2 \
  --deny-network 10.0.0.66/32,10.0.0.67/32 \
  --deny-network 192.0.2.10/32
```

IPv6 监听时，catch-all permit 使用 `[::]/0` 而不是 `0.0.0.0/0`。

### 自定义配置

`--conf-file FILE` 会复制一份完整的 Privoxy 配置到插件目录，而不是渲染模板。
它与 ACL 参数互斥；监听地址以文件里的 `listen-address` 为准（此时 `--port`
仅用于摘要输出和 macOS 端口发布）。需要上游 SOCKS 转发、镜像内自带的
actions/filter 文件，或模板未暴露的指令时使用它。

因为文件会**整体替换**渲染出的配置，所以必须写成完整配置，不会继承任何指令。
下面是一个完整示例：全部流量走本机 SOCKS5 上游，只允许私网客户端：

```conf
# privoxy.conf
confdir /etc/privoxy
logdir /var/log/privoxy
logfile /dev/stdout
listen-address 0.0.0.0:8118

# 上游：所有请求转发给 SOCKS5 代理。普通 forward-socks5 在本地解析域名；
# 需要由 SOCKS5 端解析（远程 DNS）时改用 forward-socks5t。
forward-socks5 / 127.0.0.1:1080 .

# 客户端 ACL：只要出现任何 ACL 行，默认即为拒绝，需列全允许的网段
permit-access 127.0.0.1/32
permit-access 10.0.0.0/8
permit-access 172.16.0.0/12
permit-access 192.168.0.0/16
```

```bash
pg addon install privoxy --name p4 --conf-file ./privoxy.conf
```

希望 `pg logs addon privoxy` 能看到请求日志时，保留 `logfile /dev/stdout`。
容器只把配置文件本身只读挂载到 `/etc/privoxy/config`，其它目录和文件不会挂载，
因此 `actionsfile` 只能引用镜像内已有的文件（自带的 `/etc/privoxy/*.action`）。

## 常用命令

```bash
# 安装（端口从 8118 自动分配）
pg addon install privoxy --name p1

# 白名单两个私网段（可重复，也可逗号分隔）
pg addon install privoxy --name p1 --allow-network 10.0.0.0/8,192.168.0.0/16

# 黑名单多个客户端（可重复，也可逗号分隔）
pg addon install privoxy --name p2 --deny-network 10.0.0.66/32,10.0.0.67/32 \
  --deny-network 192.0.2.10/32

# 显式端口 + 仅回环监听
pg addon install privoxy --name p3 --port 8120 --listen 127.0.0.1

# 自定义配置
pg addon install privoxy --name p4 --conf-file ./privoxy.conf

# 生命周期
pg addon list
pg addon stop privoxy --name p1
pg addon start privoxy --name p1
pg logs addon privoxy --name p1 -n 50
pg addon remove privoxy --name p1
```

重装会重新渲染配置，只有渲染结果变化（或带 `--force`）时才重建容器。

## 使用代理

```bash
# HTTP：直接转发
curl -x http://10.0.0.5:8118 http://example.com/

# HTTPS：CONNECT 隧道；客户端仍校验 example.com 自己的证书
curl -x http://10.0.0.5:8118 https://example.com/

# 大多数工具通过环境变量
export HTTP_PROXY=http://10.0.0.5:8118
export HTTPS_PROXY=http://10.0.0.5:8118
export NO_PROXY=localhost,127.0.0.1,10.0.0.0/8
```

Ansible 管理的集群只需在 `group_vars/all/main.yml` 设置 `pgcli_proxy_url`；
见[出网代理](../ansible/proxy/)。

对 HTTPS 地址执行 `curl -v` 会先看到 `CONNECT example.com:443`，随后是与源站
的正常 TLS 握手——没有任何证书被替换。

## 自启动

```bash
pg autostart enable --privoxy --name p1
pg autostart status
pg autostart disable --privoxy --name p1
```

开机的 `pg start --autostart` 会按磁盘上已有的配置启动容器；容器被删除时自动
重建。需先安装实例。

## 说明

- **不做 TLS 中间人**：没有 MITM CA，也不需要分发证书；客户端通过隧道校验
  源站的真实证书。
- **模板模式不做缓存和内容过滤**；需要过滤时通过 `--conf-file` 加载 actions 文件。
- **没有认证**：Privoxy 的 ACL 基于地址。请用 `--allow-network`、仅回环监听或
  防火墙保护端口。
- 配置目录为 `<base_dir>/addon/privoxy/<name>/`；`remove` 会删除它，除此之外
  没有其它状态。
- 默认镜像：`ghcr.io/mars-base/pgcli/pgcli-privoxy:4.2.0`（amd64 用官方 `.deb`，
  arm64 由官方源码包编译）。
