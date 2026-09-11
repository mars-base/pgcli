---
title: "HAProxy"
description: "以 pgcli 插件方式运行 HAProxy——Patroni 集群前的 TCP 负载均衡，支持读写一体（unified）与读写分离（split）两种模式"
weight: 48
---

[HAProxy](https://www.haproxy.org) 是 Patroni 官方文档推荐放在集群前面的负载
均衡器：客户端只连一个稳定地址，HAProxy 对各成员的 REST API 做健康检查，把写
请求路由到当前 leader（读写分离模式下，读请求路由到 replica）。pgcli 将其作为
**独立的顶层插件**运行——共享的路由基础设施，而非实例级 sidecar——镜像固定为
官方 `haproxy:3.2.23-alpine`。

> **平台支持：** HAProxy 插件目前**仅支持 Linux**。它通过主机网络访问 Patroni
> 成员，而 macOS 的 `podman machine` 不会把主机网络暴露给容器。macOS 上
> `NewHAProxyManager` 会快速失败并给出明确提示；`pg addon list` 仍可显示已安装
> 实例，但没有实时状态。

## 工作原理

路由方式遵循 [Patroni 官方 haproxy.cfg](https://github.com/patroni/patroni/blob/master/haproxy.cfg)
的模式。每个后端就是一个 Patroni 成员，每个成员有两个关键端口：

- **PostgreSQL 端口** —— HAProxy 实际转发连接的目标端口；
- **REST API 端口** —— HAProxy 发送 `httpchk` 健康检查的端口。

Patroni 的 REST API 对下列 GET 请求的应答（这些 GET 按设计无需认证）：

| 端点 | 返回 200 的节点 |
|------|----------------|
| `GET /` | 仅 leader |
| `GET /replica`（可加 `?lag=<上限>`） | 仅 replica，且延迟在限制之内 |

于是写监听器里唯一 `UP` 的服务器就是 leader，读监听器里唯一 `UP` 的就是各
replica——健康检查状态一变，故障切换自动生效。pgcli 把上述配置渲染到
`<base-dir>/addon/haproxy/<name>/haproxy.cfg`，并以只读方式挂载进容器。

### 两种模式

用 `--mode` 选择：

- **`unified`**（默认）——单个监听器把**所有**流量发给当前 leader。简单，
  每条连接都可读可写。leader 切换时，写监听器用 `on-marked-down
  shutdown-sessions` 掐断旧连接，让客户端重连到新 leader。
- **`split`** —— 读写分离：`_<name>_rw` 监听器把写请求发给 leader，另一个
  `_<name>_ro` 监听器把读请求轮询分发到各 replica（可按 lag 过滤）。客户端
  端口从 1 个变成 2 个。

### 统计页

每个实例还会得到一个 HTTP **stats** 监听器，可在浏览器里观察服务器
up/down 状态：`http://<listen>:<stats-port>/`。

## 安装

后端成员用两种方式之一提供（互斥）：

- **`--node NAME=HOST:PGPORT:RESTPORT`**（可重复）——显式列出；
- **`--ha <scope>`** —— 自动从 `pg ha` 管理的 Patroni scope 中推导所有**本机**
  成员，直接使用各成员的 PostgreSQL 与 REST API 端口。

```bash
# 读写一体：单端口全走 leader，后端从 scope "app" 自动推导
pg addon install haproxy --name lb --ha app

# 读写分离：rw + ro 两个监听器，读副本 lag 上限 1MB
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB

# 显式指定后端（成员在别的机器上，或 scope 不由 pgcli 管理）
pg addon install haproxy --name lb --mode split \
  --node node1=10.0.0.11:35532:8008 \
  --node node2=10.0.0.11:35533:8009 \
  --node node3=10.0.0.11:35534:8010
```

安装输出会报告分配到的端口和现成的连接串：

```
✓ haproxy installed: "lb"
  Container:    pgcli-haproxy-default-lb
  Image:        docker.io/library/haproxy:3.2.23-alpine
  Mode:         split
  Patroni:      app
  Backends:     3
  Read-write:   127.0.0.1:5000
  Read-only:    127.0.0.1:5001 (lag ≤ 1MB)
  Stats:        http://127.0.0.1:5002/
  Config:       ~/pg/addon/haproxy/lb/haproxy.cfg

  Connect via HAProxy:
    postgres://<user>@127.0.0.1:5000/<database>
```

### 之后追加成员

用 `pg ha create <scope> --member <m>` 扩容集群后，新成员还不在运行中的
HAProxy 配置里。**重跑同一条 install 命令**即可——它会重新推导完整后端列表并
重建容器：

```bash
pg ha create app --member node3 --etcd m1
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB
# Backends: 2 -> 3，haproxy.cfg 新增 server node3 127.0.0.1:35534 ... check port 8011
```

显式 `--node` 安装时，在同一条命令里多加一个 `--node` 再重跑即可——目标列表
每次整体替换，命令里写的就是最终全集。

## 端口

端口从主机级端口池自动分配：每个实例连续取 3 个空闲端口（rw，split 模式下再
加 ro，最后 stats），起始值为 `haproxy_start_port`（默认 **5000**）。也可以显式
指定：

```bash
pg addon install haproxy --name lb --ha app \
  --rw-port 6000 --ro-port 6001 --stats-port 6002
```

端口池会探测占用并记录所有 HAProxy 实例已分配的端口，因此**同一台机器上可以跑
多个实例**而不会冲突。`split` 实例占 3 个端口（rw / ro / stats），`unified`
实例占 2 个（rw / stats）：

```bash
pg addon install haproxy --name lb1 --mode split --ha app     # 5000 / 5001 / 5002
pg addon install haproxy --name lb2 --ha report              # 5003 / 5004
```

绑定地址默认 `127.0.0.1`；如需暴露到网络，用 `--listen 0.0.0.0`（或在
`pg.yaml` 里设 `listen`）。除非后端确有需要，请保持 loopback——这里 PostgreSQL
前面没有任何认证。

## 配置

全部配置位于 `pg.yaml` 顶层 `addons.haproxy` 映射中，以实例名为 key：

```yaml
namespace: default
haproxy_start_port: 5000
addons:
  haproxy:
    lb:
      mode: split            # unified | split
      ha_scope: app          # 后端来源的 scope（供 --ha 重新推导）
      listen: 127.0.0.1
      write_port: 5000
      read_port: 5001
      stats_port: 5002
      replica_max_lag: 1MB   # 仅 split 模式
      autostart: false       # pg autostart enable --haproxy --name lb
      image_tag: docker.io/library/haproxy:3.2.23-alpine
      targets:
        - { name: node1, host: 127.0.0.1, pg_port: 35532, rest_port: 8008 }
        - { name: node2, host: 127.0.0.1, pg_port: 35533, rest_port: 8009 }
```

`targets` 由 install 渲染并重新推导；`mode`、端口、`replica_max_lag`、
`image_tag` 可在这里手调，然后 `pg addon install haproxy --name lb ...` 生效。
渲染出的 `haproxy.cfg` **不含任何密钥**——只有主机、端口和健康检查 URI。

### 查看列表

```bash
pg addon list
```

```
Infra add-ons (haproxy):
  haproxy (name: lb)
    Status:      running
    Mode:        split
    Patroni:     app
    Read-write:  127.0.0.1:5000
    Read-only:   127.0.0.1:5001
    Stats:       http://127.0.0.1:5002/
    Backends:    3
      - node1        127.0.0.1:35532 (check :8009)
      - node2        127.0.0.1:35533 (check :8010)
      - node3        127.0.0.1:35534 (check :8011)
```

## 启动与停止

宿主机重启后，无需重新渲染配置即可拉起实例：

```bash
pg addon start haproxy --name lb
pg addon stop  haproxy --name lb
```

`install` 总是重建容器以保证配置生效；`start` 只启动已有容器（状态不当时会
用磁盘上的 `haproxy.cfg` 自愈重建）。

## 开机自启

容器带有 `--restart unless-stopped` 策略（应对崩溃，而非重启）。要在宿主机
重启后拉起实例：

```bash
pg autostart enable --haproxy --name lb
```

这会设置 `autostart: true` 并安装 / 刷新开机服务（见[开机自启](/docs/autostart/)）。
开机时是**仅启动**语义：它启动已存在的容器、读取磁盘上已有的 `haproxy.cfg`，
不会重新渲染配置——请先安装实例。开机服务会在 Patroni 成员**之后**启动
HAProxy，因此其后端也已在拉起中。`pg autostart status` 会列出每个目标的状态。

## 移除

```bash
pg addon remove haproxy --name lb
```

停止并删除容器，同时删除其配置目录。

## 日志

```bash
pg logs addon haproxy --name lb       # 最近 50 行
pg logs addon haproxy --name lb -f    # 持续跟踪
```

容器日志走 stdout（`log stdout format raw local0 info`），健康检查状态变化与
连接事件都会出现在这里。

## 故障排除

- **stats 页所有后端都 DOWN。** 健康检查打的是各成员的 REST API 端口。先确认
  其可达：`curl http://<host>:<rest_port>/` 在 leader 上返回 200、其余 503；
  `/replica` 正相反。返回 `000` 说明 REST API 挂了或端口写错。
- **`--ha` 找不到成员。** `--ha` 只推导 `pg ha` 在 `pg.yaml` 中管理的**本机**
  成员。远程成员或 pgcli 不管理的 scope，请用 `--node` 显式列出。
- **手动 `--rw-port` 撞端口。** 请选端口池（`haproxy_start_port` 起）之外的
  端口，否则会被自动分配器视为已占用。
- **macOS。** 暂不支持——HAProxy 需要主机网络访问 Patroni 成员，podman
  machine 虚拟机不提供该能力。
