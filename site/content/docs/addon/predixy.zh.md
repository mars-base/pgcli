---
title: "Predixy"
description: "将 Predixy 作为 pgcli 插件运行——Redis 协议代理，把原生集群暴露成一个普通的 redis:// 端点，集群无感知的客户端无需处理 MOVED"
weight: 51
---

[Predixy](https://github.com/joyieldInc/predixy) 是一个高性能 Redis 协议代理。
pgcli 把它做成一个**独立的顶层插件**：它接住一个 [Redis 原生集群](../redis/#原生集群)
（3 个以上节点、16384 个 slot、`MOVED` 重定向），再把它交回应用侧——形式是
**一个普通的 `redis://` 端点**。客户端不需要懂集群：不加 `-c`、不处理重定向，
连上就能读写。

> **平台支持：** 与 HAProxy、Redis 一样，Predixy 插件需要 **Linux host 网络**
> 才能拨通后端节点；macOS 不支持。

## 工作原理

Predixy 在客户端端口上说 Redis 协议，并到 `--backend` 列出的每个集群节点保持
持久连接。请求按 slot 路由：如果落地的节点并不持有该 key 的 slot，Predixy 自己
跟随 `MOVED`、在正确的节点上重发命令——对客户端完全透明，客户端永远看不到重定向。

pgcli 为每个实例渲染一个配置文件
`<base-dir>/addon/predixy/<name>/predixy.conf`，并以**文件级**只读 bind mount
盖到镜像内置的 `/usr/local/predixy/conf/predixy.conf` 上。渲染文件里的
`Include license.conf` 相对该文件所在目录解析，因此镜像里那份签名过的 sibling
仍然可见——pgcli 从不分发也不管理 license（见[已知限制](#已知限制)）。

### 一把密码，两侧复用

代理夹在两方已认证者之间——它自己的客户端，以及它后面的集群——pgcli 用
**同一把密码覆盖两侧**：

- `Authority { Auth "<pw>" { Mode admin } }`——客户端必须用这把密码 `AUTH` 才能用代理；
- `ClusterServerPool { Password "<pw>" }`——Predixy 用它向集群节点认证。

之所以成立，是因为这把密码就是**被代理集群自己的 `requirepass`**：客户端本来就
认它，节点本来就接受它。直接从 redis 插件里取出来：

```bash
PW=$(pg addon password redis --name n1)
```

于是一把集群成员密码同时也守住代理端点——不必再同步第二个密钥。（想要一把独立
的客户端密码，那是另一种 Authority 形态，pgcli 不渲染；见
[已知限制](#已知限制)。）

## 安装

`--backend` 与 `--password` **都必填**。`--backend` 是集群的**完整节点列表**——
pgcli 从不从本机 redis 插件推导它，因为被代理的集群常常在别的机器上；拓扑只有
你知道，所以由你来列。可重复传该 flag，也可一次逗号分隔；重复节点会被拒绝。

```bash
# 单机集群（成员在 127.0.0.1:6379/6380/6381）
PW=$(pg addon password redis --name n1)
pg addon install predixy --name proxy \
  --backend 127.0.0.1:6379,127.0.0.1:6380,127.0.0.1:6381 \
  --password "$PW"

# 跨机集群——每台机器一个成员，都用默认端口
pg addon install predixy --name app-proxy \
  --backend 10.10.0.158:6379,10.10.0.159:6379,10.10.0.160:6379 \
  --password "$PW" --workers 2
```

摘要会打印客户端应当指向的那一个端点：

```
✓ predixy installed: "proxy"
  Container:    pgcli-predixy-default-proxy
  Image:        ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine
  Listen:       0.0.0.0:7617
  Workers:      2
  Backends:     3 (127.0.0.1:6379, 127.0.0.1:6380, 127.0.0.1:6381)
  Config:       ~/pg/addon/predixy/proxy/predixy.conf

  Password:     <那把密码>
  Raw DSN:      redis://:<password>@0.0.0.0:7617/0

  Client:       redis-cli -h 0.0.0.0 -p 7617 -a '<password>' ping
  The proxy absorbs MOVED: an ordinary redis client — no -c, no
  redirect handling — reads and writes the whole cluster key space
  through this one endpoint.
```

所有校验都在**拉取或创建任何东西之前**完成：缺 `--backend`、条目格式不对（无端口、
端口非数字或越界、空条目）、节点重复、缺 `--password`、密码里含 `"` 或换行
（Predixy 的引号字符串语法无法转义它们）、`--workers < 1`，都会快速失败并给出清晰提示。

重跑 `install` 与别处一样是幂等的：运行中的容器原样保留（只重写配置），停着的容器
被启动，`--force` 才会重建容器，从而让改动的 port/listen/password/backend 生效。
后端列表**每次整体替换**，所以集群扩容（再装 redis 成员、`--cluster add-node` …）
之后，用更新后的完整列表重跑同一条 install 即可。

### Workers

`--workers N` 设置 Predixy 的 `WorkerThreads`（默认 **1**）。每个 worker 是一个
忙轮询事件线程；在专用于代理的多核机器上，N 约等于你愿意投入的核心数，吞吐大致线性扩展。

## 使用代理

任何 Redis 客户端连到代理端口、带上集群密码，表现就如同在跟一个单节点 Redis 说话——
整片 keyspace 都通过这一个端点服务：

```bash
redis-cli -h 127.0.0.1 -p 7617 -a "$PW" set foo bar    # OK — 没有 MOVED，不用 -c
redis-cli -h 127.0.0.1 -p 7617 -a "$PW" get foo        # "bar"
```

（DSN 形态：`redis://:<password>@<listen>:<port>/0`。）

### 用 `pg redis-cli`

`pg redis-cli` 经 `--host`/`--port` 连到代理——没有指向 Predixy 实例的 `--name`
路径（`--name` 只解析 *redis* 插件）：

```bash
# 在代理自己所在的主机上
pg redis-cli --host 127.0.0.1 --port 7617 set foo bar
pg redis-cli --host 127.0.0.1 --port 7617 get foo

# 从没有本地 redis 插件的主机：默认大版本镜像，密码走环境变量
REDISCLI_AUTH=$PW pg redis-cli --host 10.10.0.158 --port 7617 ping
```

本机若配了 redis 插件，则复用它的镜像与密码（见 [redis.md](../redis/#使用-redis)
里的 `pg redis-cli`）。该插件恰好是集群成员时，pg redis-cli 照旧注入 `-c`——穿过
代理也无害，因为每一个 `MOVED` 都被代理自己吸收，客户端根本看不到。这正是代理的
意义所在：同样这些普通客户端命令，无论 `-c` 在不在，都能工作。

### 跨机

已端到端验证：代理装在一台机器上，前端是三主机集群
（`--backend 10.10.0.15x:6379,…`），而**另一台**机器上的普通无集群感知客户端穿过它连接——
slot 属于远端节点的 `SET` 被吸收并落盘，之后能从那个节点自己的端口读回。唯一的条件是可达：
代理所在主机必须能拨通每个 `--backend` 节点的客户端端口（集群总线端口是成员之间的，不是代理的）。

## 端口

每个实例从 Predixy 自己的端口池取一个端口，`predixy_start_port`（默认 **7617**，
即镜像 `EXPOSE` 的端口）——与 Redis 的 `redis_start_port` 池分开，所以代理和它前端的
集群永不冲突。`--port` 可固定实例端口；自动分配会跳过已被占用的。

bind 地址默认 `0.0.0.0`（AUTH 密码是唯一的门）；`--listen 127.0.0.1` 收紧为仅回环。

## 配置

实例位于 `pg.yaml` 顶层的 `addons.predixy` 映射下：

```yaml
namespace: default
predixy_start_port: 7617
addons:
  predixy:
    proxy:
      container_name: pgcli-predixy-default-proxy
      name: proxy
      image_tag: ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine
      listen: 0.0.0.0
      port: 7617
      workers: 2
      backend:
        - 127.0.0.1:6379
        - 127.0.0.1:6380
        - 127.0.0.1:6381
      password: <集群的 requirepass>            # 由 install 写入，从不自动生成
      autostart: false                        # pg autostart enable --predixy --name proxy
```

渲染出的 `predixy.conf` 用 Predixy 自己的语法承载同样信息；手改它会被下次 install 抹掉——
要调整请改 `pg.yaml`（或对应 flag）再重装。

### 列表

```bash
pg addon list
```

```
Infra add-ons (predixy):
  predixy (name: proxy)
    Status:      running
    Address:     0.0.0.0:7617
    Workers:     2
    Backends:    3 (127.0.0.1:6379, 127.0.0.1:6380, 127.0.0.1:6381)
    Client DSN:  redis://:<password>@0.0.0.0:7617/0
    Image:       ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine
    Container:   pgcli-predixy-default-proxy
    Config:      ~/pg/addon/predixy/proxy/predixy.conf
```

`pg addon list` 会隐去密码；`--show-password` 才显示，而
`pg addon password predixy --name proxy` 单独裸打印这一个值以供脚本使用。

## 启停

```bash
pg addon start predixy --name proxy
pg addon stop  predixy --name proxy
```

`start` 只启动已存在的容器（状态不当时从磁盘上已渲染的配置重建以自愈）；若该文件缺失它会
报错——重跑 `install` 生成。

## 开机自启

```bash
pg autostart enable --predixy --name proxy
```

开机是**仅启动**（先 install），且 boot service 把 Predixy 排在 Redis **之后**，
于是代理拨号时被代理成员已经在起来的路上。见[开机自启](/docs/autostart/)。

## 移除

```bash
pg addon remove predixy --name proxy
```

停止并移除容器、删除渲染出的配置目录、去掉 `pg.yaml` 条目。被代理的集群不受影响——
代理只是它的一个消费者。

## 日志

```bash
pg logs addon predixy --name proxy      # 最后 50 行
pg logs addon predixy --name proxy -f   # 持续跟踪
```

启动横幅会报告实际生效的构建信息（`Workers:2` 等）——这是确认 `--workers` 是否生效的地方。

## 已知限制

- **单一 admin Authority 用户**——渲染器只发一条
  `Auth "<pw>" { Mode admin }`：代理的所有客户端共用一把密码、拥有全部权限。
  按用户认证、只读用户、keyspace 限制都不在渲染范围内（Predixy 本身支持更多；
  pgcli 的能力面到此为止）。
- **仅集群池**——`ClusterServerPool` 是唯一的后端类型；不配置 Sentinel 或单机池。
- **license 烤在镜像里**——`ghcr.io/mars-base/pgcli/predixy:7.0.1-alpine` 内置的
  免费版 license 把客户端数上限设为 `ClientLimit 128`，并于 **2026-12-31** 到期；
  pgcli 从不分发或轮转它——由镜像构建负责。到期后重新 build 镜像即可（Makefile 目标
  已经会换上当前 license）。
- **客户端侧不做 TLS**（仅密码门；与 Redis 本身一样，把 `listen` 收回回环或放在可信网络）。
- **仅 Linux**——拨后端需要 host 网络；macOS 不支持（快速失败）。

## 故障排除

- **客户端收到 `ERR invalid password`**——客户端必须 `AUTH` 那把密码；用
  `pg addon password predixy --name proxy` 核对（它应当等于集群的密码，
  `pg addon password redis --name n1`）。
- **容器立即退出**——Predixy 会当场拒绝它的配置：单独一行的 `{`，或含 `"`/换行而破坏
  引号的密码。pgcli 在写文件前已校验这两项，出现这种情形说明有人手改过 `predixy.conf`——重装即可。
- **命令挂起或每个 key 都报错**——某个后端节点从代理所在主机不可达（地址写错、被防火墙拦）。
  `pg logs addon predixy` 会显示拨号失败；`--backend` 必须列**本主机**能拨通的节点。
- **集群变更后后端列表过期**——install 整体替换列表；增删成员后，用当前完整节点集重跑它。
