---
title: 平台支持
description: pgcli 各组件与功能在 Linux、macOS 上的支持情况
weight: 8
icon: fa-solid fa-desktop
menus:
  main:
    identifier: docs-platform
    parent: docs
    weight: 8
    params:
      icon: fa-solid fa-desktop
---

pgcli 在 Linux 和 macOS 上都是驱动 [Podman](https://podman.io) 容器运行的。
两者的根本区别在于**容器如何获得网络**：

- **Linux** 原生运行 Podman，容器共享宿主网络栈（`--network host`），彼此通过
  `127.0.0.1` 互访。这是零开销路径，也是**唯一**支持**跨主机**拓扑的路径。
- **macOS** 把 Podman 跑在 `podman machine` 虚拟机里。此处的 host 网络绑定的是
  *虚拟机*的回环，Mac 看不到；于是 pgcli 改为加入共享 bridge 网络
  （`pgcli-net`）并**发布**各端口 —— Mac 通过 gvproxy 用 `127.0.0.1:<port>`
  访问，容器之间则通过 bridge 上的**容器名**互访。这适用于**单主机**
  （开发/测试），不是跨主机 HA 路径。

下表是当前支持矩阵。"macOS（单主机）"表示单机完全可用；那些必须向其它机器
广播可路由地址、或依赖 Linux 专属容器内部机制的功能，仍是 **仅 Linux**。

## 支持矩阵

| 组件 / 功能 | Linux | macOS |
|------------|:-----:|:-----:|
| 实例生命周期 —— `pg create` / `start` / `stop` / `restart` / `destroy` / `status` | ✅ | ✅ 单主机 |
| `pg psql` / `pg exec` | ✅ | ✅ |
| SQL 命令参考（`pg exec`、管理查询） | ✅ | ✅ |
| 日志 —— `pg logs` | ✅ | ✅ |
| 命名空间隔离（`pg.yaml` 的 `namespace`） | ✅ | ✅ |
| 数据导入 / 导出 —— `pg import` / `pg export` | ✅ | ✅ |
| 克隆 —— `pg clone`（流式 `pg_dump \| pg_restore`） | ✅ | ✅ |
| 扩展 —— `pg extension install` / `list` | ✅ | ✅ |
| 备份 / 恢复 —— `pg backup` / `pg restore`（pgBackRest） | ✅ | ✅ 单主机 |
| 物理副本 —— `pg replica` | ✅ | ✅ 单主机 |
| 故障切换 —— `pg failover`（副本提升） | ✅ | ✅ 单主机 |
| 开机自启 —— `pg autostart` | ✅ systemd user unit | ✅ launchd（登录后） |
| 插件 —— **PgBouncer** | ✅ 含跨主机连接池 | ✅ 单主机 dev/test |
| 插件 —— **PgDog** | ✅ | ✅ 单主机 dev/test |
| 插件 —— **etcd** | ✅ 含跨主机集群 | ❌ 仅 Linux |
| 插件 —— **HAProxy** | ✅ | ❌ 仅 Linux |
| 插件 —— **MinIO** | ✅ amd64 + arm64 | ❌ 仅 Linux |
| 客户端 —— **`pg mc`**（MinIO 客户端） | ✅ host 网络 | ✅ bridge，仅远端端点（见下） |
| 高可用 —— **Patroni**（`pg ha`） | ✅ 含跨主机 | ❌ 仅 Linux |

图例：✅ 支持 · ✅ *备注* 支持但有所述限制 · ❌ 不支持。

## 实例类功能

所有构建在受管 PostgreSQL 实例之上的功能在两个平台都可用 —— 生命周期命令、
`pg psql` / `pg exec`、日志、命名空间、导入/导出、克隆、扩展、备份/恢复
（pgBackRest）、物理副本、故障切换。macOS 的 bridge 路径（发布端口 + 容器名
DNS）最早由实例、副本/备份路径验证；代理插件复用的正是这一套。

macOS 上这些都是**单主机**：跨主机副本、跨多机的连接池属于 Linux 的
`--network host` + LAN 地址能力。

### 开机自启

两个平台都支持，但机制不同：

- **Linux** —— systemd 的 **user** unit。
- **macOS** —— launchd 的 **LaunchAgent**。LaunchAgent 在**用户登录**时运行，而
  非系统启动时 —— 因为 rootless podman 无法在有人登录前启动。请预期容器在你
  登录后才拉起。见[开机自启](/docs/autostart/)。

## 插件

各组件的网络模型不同：

- **[PgBouncer](/docs/addon/pgbouncer/)** 与 **[PgDog](/docs/addon/pgdog/)** ——
  纯代理，macOS 上支持单主机 dev/test。macOS 下它们加入 `pgcli-net` 并发布端
  口；客户端连接仍是 `127.0.0.1:<port>`。后端地址的注意事项（remote/后端主机
  必须是**从 Mac 可达**的地址，而不是 `127.0.0.1`）见各自页面的"平台支持"一
  节。
- **[etcd](/docs/addon/etcd/)** —— **仅 Linux**。成员以 host 网络运行，并把
  client/peer URL 作为**永久的集群状态**写进 raft 成员列表；要移植 macOS 的
  bridge + 容器名模型就得重写这份状态，在完成该设计之前保持仅 Linux。
- **[Patroni](/docs/addon/ha/)**（`pg ha`）—— **仅 Linux**。成员依赖 rootless
  podman 的 host 网络与 Linux 专属的回环地址改写来共享 `0600` 配置与数据目录；
  `podman machine` 虚拟机的 uid 映射与之并不吻合。这些命令在 macOS 上会快速失
  败并给出清晰提示。见 [Patroni 高可用](/docs/addon/ha/)。
- **[HAProxy](/docs/addon/haproxy/)** —— **仅 Linux**。它通过主机网络代理
  Patroni 成员，而 podman machine 虚拟机不提供该能力；manager 在 macOS 上快速
  失败。
- **[MinIO](/docs/addon/minio/)** —— **仅 Linux**。单机对象存储，经
  主机网络提供服务（与 HAProxy 同样的 macOS 限制）；公开镜像为双架构
  （amd64 + arm64）。
- **[`pg mc`](/docs/addon/minio/#使用-mc-客户端)**（MinIO 客户端）——
  **两个平台都可用**，与插件本身不同。它只是跑在一次性容器里的客户端，
  所以 macOS 上可以通过 bridge 网络访问远端或局域网的 MinIO/S3 端点
  （别名请指向可路由地址，而不是 `127.0.0.1`）；别名在两个平台上都持久化
  在 `~/.mc/config.json`。

## 确认当前平台

`pg start` 会在横幅里打印检测到的平台，便于确认当前走的是哪条路径：

```
=== pg start ===
Platform: linux        # 或：macOS
```
