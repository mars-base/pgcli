---
title: "HAProxy"
description: "以 pgcli 插件方式运行 HAProxy——Patroni 集群前的 TCP 负载均衡（读写一体或读写分离），或用 --conf-file 提供自己的 haproxy.cfg 实现任意原生代理"
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

### 两种路由模式

用 `--mode` 选择：

- **`unified`**（默认）——单个监听器把**所有**流量发给当前 leader。简单，
  每条连接都可读可写。leader 切换时，写监听器用 `on-marked-down
  shutdown-sessions` 掐断旧连接，让客户端重连到新 leader。
- **`split`** —— 读写分离：`_<name>_rw` 监听器把写请求发给 leader，另一个
  `_<name>_ro` 监听器把读请求轮询分发到各 replica（可按 lag 过滤）。客户端
  端口从 1 个变成 2 个。

### 两种配置模式

在上面两种路由模式之上，还有一个*谁来写 `haproxy.cfg`* 的选择：

- **渲染模式**（默认）——pgcli 根据 `--node`/`--ha` 加 `--mode`、端口和
  `--max-lag` 生成配置，每次安装都会重写。就是上面说的 Patroni 读写代理。
- **自定义模式（`--conf-file <path>`）**——你提供完整的 `haproxy.cfg`，pgcli
  原样运行它，用于**任意**原生 HAProxy 代理场景（普通 TCP 中继、HTTP
  frontend、任意数量的 `frontend`/`backend`/`listen` 段——HAProxy 能做的都
  行），不再限于 Patroni 集群。pgcli 把文件复制到自己的配置目录、以只读方式
  挂载该副本，（重）启容器前先用 `haproxy -c` 校验，且永不改写、不删除你的
  原文件。改完文件后，`pg addon haproxy reload` 会重新校验并**热加载**运行中的
  实例、不断流（仅 conf-file 实例——见[热加载](#热加载)）。所有监听器都来自
  文件本身；`--mode`、`--rw-port`/`--ro-port`/`--stats-port`、`--max-lag`、
  `--listen` 与 `--node`/`--ha` 在这里没有意义，会被拒绝或忽略——见
  [自定义配置文件](#自定义配置文件)。

### 统计页

每个实例还会得到一个 HTTP **stats** 监听器，可在浏览器里观察服务器
up/down 状态：`http://<listen>:<stats-port>/`。

## 安装

后端成员用两种方式之一提供（互斥），或者用 `--conf-file` 完全绕过这套生成逻辑
（见[自定义配置文件](#自定义配置文件)）：

- **`--node NAME=HOST:PGPORT:RESTPORT`**（可重复）——显式列出；
- **`--ha <scope>`** —— 自动从 `pg ha` 管理的 Patroni scope 中推导所有**本机**
  成员，直接使用各成员的 PostgreSQL 与 REST API 端口。

`--node` 各字段的含义，以 `node1=10.0.0.11:35532:8008` 为例：

| 字段 | 含义 |
|------|------|
| `node1` | 成员名——成为 `haproxy.cfg` 中的 `server` 名，也是 stats 页里的标签 |
| `10.0.0.11` | 本机可访问到该成员的地址（成员的 `advertise-host`；同机可写 `127.0.0.1`） |
| `35532` | 成员的 PostgreSQL 端口——HAProxy 实际转发连接的目标 |
| `8008` | 成员的 Patroni REST API 端口——健康检查打这个端口 |

用 `--ha` 时这四个值都来自 `pg.yaml`：Patroni 成员名、其 `advertise-host`
（默认 `127.0.0.1`）、以及 `host_port` / `restapi_port`。

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

### 之后增删成员

后端列表跟随 `pg.yaml` 中该 scope 的**本机**成员，因此任何拓扑变化——增员或
移除——都通过**重跑同一条 install 命令**同步：它重新推导完整后端列表并重建
容器。

用 `pg ha create <scope> --member <m>` 扩容后，新成员还不在运行中的 HAProxy
配置里：

```bash
pg ha create app --member node3 --etcd m1
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB
# Backends: 2 -> 3，haproxy.cfg 新增 server node3 127.0.0.1:35534 ... check port 8011
```

对称地，`pg ha remove <scope> --member <m>` 之后，被移除的成员会作为过期后端
残留在配置里——重跑 install 即可将其剔除：

```bash
pg ha remove app --member node3
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB
# Backends: 3 -> 2，server node3 一行消失
```

显式 `--node` 安装时没有自动推导：直接改 `--node` 列表本身（增加或删除某个
spec）再重跑——目标列表每次整体替换，命令里写的就是最终全集。

### 跨主机集群

`--ha` 只从**本机**的 `pg.yaml` 推导后端，而它只记录本机 `pg ha create` 管理的
成员。位于**其他主机**的成员从来就不是本机的后端——在**那边**增删成员，本机
**无需**重新同步（也不会有残留）。若想用一个 HAProxy 前置整个集群，请改用显式
的 `--node` 列表，逐个填上各成员的 advertise 主机地址：

```bash
pg addon install haproxy --name lb --mode split \
  --node node1=10.0.0.11:35532:8008 \
  --node node2=10.0.0.12:35532:8008 \
  --node node3=10.0.0.13:35532:8008
# 之后：再加（或删）一个 --node 再重跑——列表整体替换
```

注意：在单台主机上用 `--ha` 安装的实例，写流量只会路由到**本机**的 leader——
一旦故障切换把 leader 提到了远程成员，rw 监听器就没有 UP 的服务器了，直到
leader 迁回来。对于 leader 可能漂移的跨主机集群，优先用显式的、列出全部成员的
`--node` 列表（或每台主机各跑一个 HAProxy，再用上层的 VIP / DNS 前置它）。

## 自定义配置文件

上面的渲染模式是 pgcli 对 Patroni 路由的一种固定写法。用 `--conf-file` 就完全
跳过生成，直接跑你自己的 `haproxy.cfg`——任意原生 HAProxy：普通 TCP 中继、
HTTP frontend、任意数量的 `frontend`/`backend`/`listen` 段，upstream 软件支持
什么就能做什么。pgcli 只保留容器生命周期（拉取、创建、启停、remove、autostart）
和配置**校验门**：

```bash
pg addon install haproxy --name custom --conf-file /root/haproxy/lb.cfg
```

`--conf-file` 与 `--node`/`--ha` 互斥（同时给出会以报错拒绝安装），文件必须存在
且非空。一个把原始 TCP 中继和不相干 HTTP 监听器混在一起的典型文件：

```
global
    maxconn 1000
    log stdout format raw local0 info

defaults
    log global
    mode tcp
    timeout client 30m
    timeout connect 4s
    timeout server 30m

# 到某个 PostgreSQL 实例的普通 TCP 中继（pgcli 在这里不推导任何东西）
frontend pg_relay
    bind 10.0.0.11:7000
    default_backend pg

backend pg
    server demo 127.0.0.1:35432 check

# 一个用于不相干用途的 HTTP 模式监听器
listen webstats
    mode http
    bind 127.0.0.1:7001
    http-request return status 200 content-type text/plain lf-string "haproxy custom proxy alive\n"
```

安装摘要随后报告自定义模式，而不再报告端口（没有端口可报——它们在你的文件里）：

```
✓ haproxy installed: "custom"
  Container:    pgcli-haproxy-default-custom
  Image:        docker.io/library/haproxy:3.2.23-alpine
  Mode:         custom (user config — all listeners come from the file)
  Config:       /root/.pgcli/addon/haproxy/custom/haproxy.cfg (from /root/haproxy/lb.cfg)
```

这个模式的具体行为，逐条说明（以下每一条都是真实主机上安装实测的结果）：

- **你的文件是唯一事实来源；跑起来的是 pgcli 的副本。** install 把它复制到
  `<base-dir>/addon/haproxy/<name>/haproxy.cfg`，只读挂载这份副本。你的原文件
  永不重写，`pg addon remove` 也只删 pgcli 这份副本——你的文件留在原地。
- **改配置靠 `reload`（无损）或重跑 install（有损）。** 改完文件后，要么
  `pg addon haproxy reload` 把它热应用到运行中的容器、不断流，要么重跑同一条
  install 重建容器。两者都会先把文件重新复制一份并用 `haproxy -c` 校验。渲染
  模式的旋钮在这里改了没用——`--mode`、`--rw-port`/`--ro-port`/`--stats-port`、
  `--max-lag`、`--listen` 描述的是生成器，不是你的文件。见[热加载](#热加载)。
- **动容器之前全部经过校验。** 每次 install 都先把挂载的副本扔进一次性容器跑
  `haproxy -c`（仅检查）。坏文件会以 HAProxy 自己的诊断信息让 install 失败、
  退出码非零，并**回滚到上一份好配置**——运行中的容器和磁盘上的配置原样不动，
  之后的重启或 `pg addon start` 不可能复活一份被拒绝的配置。（warning——比如
  缺 timeout——放行；只有真正的 error 才拒。）
- **端口由你自己选。** 端口池跳过 conf-file 实例：既不分配，也不把你文件里的
  端口登记为已用。监听端口请自己规划，避开渲染模式实例自动分配的端口和主机上
  其他服务——撞了的话 HAProxy 启动时以 `cannot bind socket` 失败。
- **容器仍走 host 网络**，与渲染模式一致。绑 `127.0.0.1` 的 frontend 只有主机
  本机能访问；要让别的容器/主机进来，绑主机的地址（上面 `pg_relay` 就是）。
- **代理的健康状况由你的配置负责。** `pg logs <name>` 显示 HAProxy 自己的
  stdout（前提是文件里写了 log stdout，如示例）；stats 页只可能是你自己写的
  `stats` 段——pgcli 不会替你加一个。
- 切回渲染模式只需一条不带 `--conf-file` 的 install：重新给 `--node`/`--ha`，
  实例就回到渲染模式（磁盘上的 conf-file 副本会被生成的配置覆盖）。

### 示例：带健康检查的 HTTP 负载均衡

conf-file 实例就是一个完整的 HAProxy，所以 Patroni 那套并非它唯一能做的事——
下面是一个对多台 app 服务器做 HTTP 轮询、并带主动健康检查的配置（就是这份配置
实跑过，`pg logs` 里观察到了 DOWN/UP 行为）：

```
global
    maxconn 4096
    log stdout format raw local0 info

defaults
    log global
    mode http
    retries 2
    timeout client 30m
    timeout connect 4s
    timeout server 30m
    timeout http-request 30s

frontend http_in
    bind 127.0.0.1:8180
    default_backend http_pool

backend http_pool
    # 在各 app 服务器间轮询；用 GET / 做健康检查
    option httpchk GET /
    http-check expect status 200
    default-server inter 3s fall 3 rise 2
    server app1 127.0.0.1:8081 check
    server app2 127.0.0.1:8181 check
    # 再加：server app3 127.0.0.1:<port> check
```

每部分的作用，以及实测观察到的现象：

- **多条 `server` 行**——HAProxy 在新连接间轮询分发到各 `UP` 服务器（每台加一
  行；列表顺序即初始轮转顺序）。
- **`option httpchk GET /` + `http-check expect status 200`**——健康探测是
  HTTP `GET /`，只有 200 算 UP。方法和服务实际应答的要一致：对 `HEAD` 返
  `405`、对 `GET` 返 `200` 的服务，得用 `GET` 形式，而不是默认的 `HEAD /`。
- **`default-server inter 3s fall 3 rise 2`**——每 3s 探测一次，连续 3 次失败标
  DOWN，连续 2 次成功恢复。
- 每台 server 上的 **`check`** 为该 server 开启探测。

某后端不可达时，其探测失败，`fall 3` 后 HAProxy 把它从轮转中摘掉并记日志——
`pg logs <name>` 里看到：

```
Server http_pool/app2 is DOWN, reason: Layer4 connection problem, info: "Connection refused", check duration: 0ms. 1 active and 0 backup servers left. ...
```

而所有前端流量仍稳定返回 200——只流向存活的 `app1`。等 `app2` 的端口重新应答，
过了 `rise 2` 它就自动加回轮转，**无需 reload**。要增删 `server` *行*，改完
文件跑 `pg addon haproxy reload`（见[热加载](#热加载)）：它先用 `haproxy -c`
校验，再热换 worker、不断开在途连接。后端成员的增减仍以走上面的健康检查那条
最干净（你**停掉**的 server 无需任何 reload 就被摘出轮转）；reload 是给你改动
`server` *列表*本身用的。

### 并发调优（线程数）

HAProxy 的模型是**单进程、多线程**——没有 nginx 那种 `worker_processes`。在
conf-file 模式下，你在自己文件的 `global` 段里调它（渲染模式不设这些）：

- **`nbthread N`**——worker 线程数。对镜像验证过：`haproxy -c` 接受
  `nbthread 4`。
- **不设置时的默认**——在多处理器主机上，HAProxy 会自动按 CPU 数铺开线程（其
  原话：*"Threads will automatically be used on multi-processor machines if
  available"*），所以只有要钉死具体数量时才需要 `nbthread`。
- **`cpu-map auto:1-N 0-M`**——可选，把线程 1…N 绑定到 CPU 核 0…M（已验证）。
- **`nbproc` 已不存在。** 自 HAProxy 2.5 起它被直接拒绝（`haproxy -c` 报
  *"nbproc is not supported any more since HAProxy 2.5"*）。用线程，不用多进程。

```
global
    nbthread 4
    cpu-map auto:1-4 0-3
    maxconn 4096
    log stdout format raw local0 info
```

改它意味着编辑文件后跑 `pg addon haproxy reload`——master 重新读配置、按新的
线程数起一个新 worker（实测：`nbthread` 2 → 4 热应用，在途连接不断），因此
不像重跑 install 那样掐流量。这是你文件里的配置项，不是 pgcli 的 flag。

### 热加载

`--conf-file` 实例支持无损热加载配置——HAProxy 版的 `pg addon nginx reload`：

```bash
pg addon haproxy reload --name custom
```

官方镜像本就以 **master-worker 模式**（`-W`）运行 HAProxy：pid 1 是 master，
子进程是 worker。`reload` 给 master 发 `SIGUSR2`，于是 master 用磁盘上的配置
起一个*新* worker、把监听套接字交接过去，再让老 worker 处理完在途连接后才退出
——**不断流**，不同于 `install` 那次有损的容器重建。（这是实测过的行为，不是
纸上推演：一个运行中的实例的 worker 被整段换掉，期间始终返回 200。）

在发任何信号之前，`reload` 做的正是 install 那道校验门——它**不是**一个盲目
的信号：

1. 把你的 `--conf-file` 重新（原样）复制到挂载的 `haproxy.cfg`；
2. 把副本扔进一次性容器跑 `haproxy -c`（仅检查）；
3. **只有校验通过**才发 `SIGUSR2`。校验不过的文件会带着 HAProxy 自己的诊断
   信息中止 reload（退出码非零）、**把上一份好配置写回磁盘**、运行中的容器
   原样不动——所以一份被拒的编辑，之后的重启也绝不会把它捡起来。

前提与范围：

- **仅 `--conf-file` 实例。** 渲染实例的配置每次 install 都从 `--node`/`--ha`
  重建、不接受手改，因此它的更新路径仍是 `pg addon install haproxy`。对它跑
  `reload` 会报这条提示并拒绝。
- **容器必须在运行。** reload 打的是活的 master；实例停着的话先
  `pg addon start haproxy --name custom`（一次全新的 install 也会把改好的文件
  带起来）。
- `reload` 是给**就地改运行中配置**用的。仅仅是维护时临时下线某后端仍完全不需
  reload——健康检查会把它摘出轮转；reload 是改 `server` 列表 / `global` 旋钮
  本身时用的。

同样的热加载也可以通过 **install 的 `--reload` 参数**触发：对一个正在运行的
conf-file 实例，install 会校验并热加载而非重建容器，一条命令既（重）配置又应用
、不断流：

```bash
pg addon install haproxy --name custom --conf-file /root/haproxy/lb.cfg --reload
```

不加 `--reload` 时，install 保持默认的容器重建行为（有损）。对已停止或渲染模式
的实例加 `--reload` 只打印一条提示、按正常 install 走。

## 读写分离使用示例

用 `--mode split` 安装后，会暴露两个客户端端口：

| 端口 | 流量 | 后端 |
|------|------|------|
| `write_port`（默认 5000） | 读写 | 仅 leader |
| `read_port`（默认 5001） | 只读 | 各 replica（轮询，lag ≤ `max-lag`） |
| `stats_port`（默认 5002） | HTTP 统计页 | — |

```bash
# 写（始终打到当前 leader）
psql "host=127.0.0.1 port=5000 user=postgres dbname=postgres"

# 读（负载均衡到各 replica）
psql "host=127.0.0.1 port=5001 user=postgres dbname=postgres"
```

故障切换后 HAProxy 通过健康检查自动识别新 leader —— 无需改连接串。浏览器打开
`http://127.0.0.1:5002/` 可实时查看后端状态。

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

conf-file 实例（见[自定义配置文件](#自定义配置文件)）不消耗端口池：
池既不给它分配，也不登记它文件里的端口。

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
      # conf_file: /root/haproxy/lb.cfg   # 自定义模式（见上文；跳过 targets/mode/端口）
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

conf-file 实例显示的是来源文件，而不是端口和后端：

```
  haproxy (name: custom)
    Status:      running
    Mode:        custom (user config)
    Config:      /root/haproxy/lb.cfg
```

## 启动与停止

宿主机重启后，无需重新渲染配置即可拉起实例：

```bash
pg addon start haproxy --name lb
pg addon stop  haproxy --name lb
```

`install` 总是重建容器以保证配置生效；`start` 只启动已有容器（状态不当时会
用磁盘上的 `haproxy.cfg` 自愈重建）。对 `--conf-file` 实例，
`pg addon haproxy reload` 能把改好的文件热应用到*运行中*的容器、免去这次重建
——见[热加载](#热加载)。

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
- **conf-file 安装没过 `haproxy -c`，但旧配置还在跑。** 这是设计行为：被拒的
  副本会回滚，容器不动。按 HAProxy 报错指出的行修文件，再重装。若容器确实起来
  后又死了（`cannot bind socket`、配置引用的文件缺失），看 `pg logs <name>`。
- **reload 报"容器未在运行"。** reload 打的是活的 master，实例必须先起来——
  `pg addon start haproxy --name <name>`（或一次全新的 install，也会把改好的
  文件带起来）。
- **reload 因 `haproxy -c` 报错而拒绝文件。** 与 install 同一道门：reload 中止、
  上一份好配置写回、什么都没重载——按 HAProxy 指出的行修好再 reload。
- **macOS。** 暂不支持——HAProxy 需要主机网络访问 Patroni 成员，podman
  machine 虚拟机不提供该能力。
