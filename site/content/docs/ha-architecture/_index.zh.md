---
title: "HA 架构"
description: "同城 PostgreSQL 高可用与两地三中心异步灾备架构参考"
weight: 46
icon: fa-solid fa-diagram-project
menus:
  main:
    identifier: docs-ha-architecture
    parent: docs
    weight: 46
    params:
      icon: fa-solid fa-diagram-project
cascade:
  type: docs
  footer_style: slim
---

本文给出两种基于 PostgreSQL、Patroni、etcd DCS 与 HAProxy 的参考架构：同城高可用，以及以同城故障时异地接管为目标的两地三中心灾备。

> **术语说明：**本文所说的“同城双活”指两个同城故障域都部署数据库成员、正常情况下由 Patroni 管理集群服务；它不是双主写入。Patroni 集群同一时刻只有一个 leader 接受写入，其他成员作为副本。若要求多个节点同时写入，需要另外的多主数据库方案，不属于 Patroni HA。

## 架构一：同城双活（单主写入 HA）

两个同城中心各部署一个 PostgreSQL/Patroni 成员，第三个 PostgreSQL 成员可作为额外副本。etcd 使用 3 个成员，部署到独立主机并尽量分散故障域；不能只把 3 个成员都放在同一台机器或同一故障域。

| 位置 | PostgreSQL / Patroni | etcd DCS | 用途 |
|---|---:|---:|---|
| 同城中心 A | 1 个成员 | 1 个成员 | leader 或副本 |
| 同城中心 B | 1 个成员 | 1 个成员 | 副本或故障切换候选 |
| 独立主机 / 第三故障域 | 可选 1 个成员 | 1 个成员 | DCS 第三票；PG 可按容量与恢复目标决定 |
| **合计** | **至少 2 个成员** | **3 个成员** | etcd 多数派为 2 票 |

| 故障情况 | DCS 多数派 | 预期行为 |
|---|---|---|
| 任意一个 etcd 成员故障 | 保留 2/3 票 | DCS 仍可工作 |
| 一个 PG 成员故障 | 取决于剩余副本状态 | Patroni 可在 DCS 正常且副本具备提升条件时选出新 leader |
| 两个 DCS 成员故障 | 仅 1/3 票 | DCS 失去 quorum，不能自动安全选主 |
| 两中心间网络分区 | 由持有 2 票的一侧继续 | 少数派一侧无法续租 leader 锁；需避免客户端绕过入口直连旧 leader |

### 客户端入口

可在同城部署 HAProxy，为应用提供稳定的 PostgreSQL TCP 入口。HAProxy 检查 Patroni REST API，只把写连接转发给当前 leader。HAProxy 是代理，不是 L2 设备，也不负责跨网段漂移 VIP。

数据库主机处于不同网段时，可为代理使用各自网段的固定地址；若要冗余代理，可部署多个 HAProxy，并由应用/连接池配置主备地址，或通过 DNS/GSLB/负载均衡统一入口。不得将两个代理地址当成两个可同时写入的数据库端点。

## 架构二：两地三中心灾备

目标是**同城整体故障时异地接管**。同城两个中心部署 2 个 PostgreSQL/Patroni 成员与 2 个 etcd 成员；异地第三中心部署 1 个异步 PostgreSQL/Patroni 副本与 3 个 etcd 成员。异地的 3 个 etcd 应运行在独立主机上；若全部处于同一机柜，则该机柜是共同故障域。

| 地点 | 中心 | PostgreSQL / Patroni | etcd DCS | 作用 |
|---|---|---:|---:|---|
| 同城 | A | 1 个成员 | 1 个成员 | 生产集群成员 |
| 同城 | B | 1 个成员 | 1 个成员 | 生产集群成员 |
| 异地 | C | 1 个异步副本 | 3 个成员 | 同城整体故障时保留 DCS 多数派，提供灾备接管候选 |
| **合计** | **3 个中心** | **3 个成员** | **5 个成员** | etcd 多数派为 3 票 |

| 故障情况 | 剩余 DCS 票数 | 预期行为与限制 |
|---|---:|---|
| 同城 A 或 B 单中心故障 | 4/5 | DCS 保持 quorum；剩余 PG 成员按 Patroni 状态继续服务或切换 |
| 同城 A、B 整体故障 | 异地 3/5 | 异地保有 quorum；若异地 PG、Patroni 与网络正常且副本可提升，可自动成为 leader |
| 异地中心 C 整体故障 | 同城 2/5 | 同城失去 quorum；此布局有意优先保证同城故障时异地接管，不保证异地故障时同城自动选主 |
| 两地链路分区 | 异地 3/5、同城 2/5 | 异地是多数派侧；同城 Patroni 无法续租 leader 锁。必须验证客户端网络隔离与防脑裂措施 |

异步复制下，故障时异地副本可能尚未接收最新 WAL，自动提升存在数据丢失可能；实际 RPO 取决于故障时的复制延迟。DCS 有 quorum 是异地接管的必要条件，不等于保证副本一定可提升。应监控复制延迟，并按业务 RPO 决定是否需要额外的同步复制策略及其延迟/可用性代价。

### 两地入口与故障切换

同城和异地可各部署 HAProxy。HAProxy 负责在数据库成员间识别当前 leader 并转发连接；它本身不会让应用从同城入口切到异地入口。

| 组件 | 建议职责 |
|---|---|
| 同城 HAProxy | 正常情况下提供同城连接入口，并探测 Patroni leader |
| 异地 HAProxy | 提供异地连接入口；异地副本提升后转发到新 leader |
| 应用 / 连接池 | 将两个代理地址配置为主备并支持断线重连；不可并行写入两个代理 |
| 可选 DNS/GSLB/全局负载均衡 | 若应用不支持多入口，提供站点级流量切换；需评估健康检查、DNS 缓存和已有连接重建 |

两地网段不同，通常不需要让同一个 VIP 在数据库主机之间漂移。若必须保持单一 IP，应由网络团队提供跨网段路由发布（例如 BGP）或 SDN/负载均衡控制面切换；普通 Keepalived/VRRP 的 ARP 漂移只适用于同一二层广播域。

## 部署与演练检查

- 按实际故障域规划 etcd 成员；确认成员间 client/peer 网络互通，奇数成员和多数派设计符合目标故障场景。
- Patroni 的每个成员使用一致的 DCS endpoints、端口、密码与可达 advertise 地址；放通成员间 PostgreSQL 与 REST API 流量。
- 明确复制策略、可接受 RPO/RTO、异地提升策略和同城恢复后的重新加入/回切流程。
- 分别演练单 PG 故障、单 DCS 故障、同城整体断电、两地网络分区及异地恢复；验证数据库角色、入口健康检查、应用重连和数据一致性。
- HAProxy 入口不是备份。保留独立备份与 WAL 归档，覆盖误操作、逻辑损坏和超出复制保护范围的故障。

Ansible 的 Patroni、etcd 与 HAProxy 配置示例见 [Ansible HA 集群](../ansible/ha/)，命令与组件行为见 [Patroni HA](../ha-cluster/ha/) 和 [HAProxy](../addon/haproxy/) 文档。
