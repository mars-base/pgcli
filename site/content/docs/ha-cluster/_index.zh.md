---
title: "HA 集群"
description: "用 pgcli 管理 Patroni 高可用集群 —— pg ha 命令集、动态配置、REST API、扩展、集群备份与恢复、直连 SQL"
weight: 45
icon: fa-solid fa-sitemap
menus:
  main:
    identifier: docs-ha-cluster
    parent: docs
    weight: 45
    params:
      icon: fa-solid fa-sitemap
cascade:
  type: docs
  footer_style: slim
---

[Patroni](https://patroni.readthedocs.io) 是 PostgreSQL 高可用的事实标准：它掌管每个
postmaster 的生命周期、在成员间流式复制、并在 leader 失联时执行**自动 failover**。pgcli
把 Patroni 作为独立的顶级命令 `pg ha` 暴露出来——它是一种独立的模式，既不是普通的 `pg`
实例，也不是用 `pg addon install` 安装的插件。

> **关于归属。** Patroni 相关页面原先挂在 [插件 Addons](../addon/) 下。现在它们独立成
> **HA 集群**这一节，因为 `pg ha` 并不通过插件系统安装——插件索引里提到它只是为了方便
> 检索。etcd DCS 与 HAProxy 负载均衡仍是插件页面（[etcd](../addon/etcd/)、
> [HAProxy](../addon/haproxy/)）；本节会在相关处链回去。

## 子页面

| 页面 | 内容 |
|------|------|
| [Patroni HA](./ha/) | `pg ha` 命令集：创建、status、switchover/failover、pause、跨主机成员、密码、namespace 与 DCS 布局 |
| [动态配置](./ha-dynamic/) | `pg ha edit-config` / `pg ha ctl` —— 存在 DCS 里的运行时配置，以及为什么它绝不重建容器 |
| [REST API](./ha-rest-api/) | 每个成员的 Patroni REST API：健康检查、leader 重定向、HAProxy 探测什么 |
| [HA 集群扩展](./ha-extensions/) | 在集群范围内安装/卸载 PostgreSQL 扩展（滚动修改 shared_preload_libraries） |
| [集群备份](./ha-backup/) | `pg backup setup` + `pg ha snapshot` —— stanza、WAL 归档到 S3、跨主机的备份 SSH 通道 |
| [集群恢复](./ha-restore/) | `pg ha restore` —— 走自定义 bootstrap 机制的集群 PITR、leader 本机性预检、恢复后的重新基线 |
| [Exec / psql](./ha-exec/) | `pg ha exec` / `pg ha psql` —— 直连 leader 或任意成员跑 SQL，免 dsn、免进容器 |
| [示例：HA 集群 + 自签 CA 的 MinIO](./ha-example-minio/) | 实测通过的端到端流程：集群备份到一台服务自带证书的 MinIO，全程在完全隔离的第二套环境里 |
| [生成证书](./ha-cert/) | `pg cert` —— 签发带域名与 IP SAN 的自签证书，服务于任何不想走 CA 又需要 TLS 的场合（开发测试服务器、内部端点，或服务自带证书的 MinIO）：flag 一览，以及单张自签叶证书如何充当自己的信任锚 |
| [S3 存储高可用](./ha-s3-storage/) | 让仓库本身（MinIO/silo）也容错：pgcli 暴露的 SNSD 与 MNSD 两种形态及为何只有这两种，加上在数据目录之下用 ZFS —— 单机 raidz、分布式集群每节点各自建池、异构节点 |

## 典型路径

```bash
# 一台主机：引导集群（基于已安装的 etcd 插件成员）
pg ha create app --member node1 --etcd m1
pg ha create app --member node2 --etcd m1

# 其它主机用同一套密码登记自己的成员
pg ha passwords app --file app-passwd.yml
ssh other-host
pg ha create app --member node3 --advertise-host 10.0.0.12 \
    --etcd-endpoints 10.0.0.9:2379 --passwords-file app-passwd.yml

# 直接跑 SQL —— leader 由 pgcli 从 DCS 解析
pg ha exec app "SELECT version()"
pg ha psql app

# 备份它，并且能回到过去
pg backup setup --s3-endpoint ...
pg ha snapshot create app --type full
pg ha restore app --time "2026-08-26 15:30:00+00"
```

## 相关

- **插件**：[etcd](../addon/etcd/)（DCS）、[HAProxy](../addon/haproxy/)
  （可扛故障切换的稳定客户端端点，可选读写分离）、[MinIO](../addon/minio/)（备份所在的 S3 仓库）
- **[恢复 → Patroni 集群](../restore/#patroni-集群)**：与单实例恢复共通的 PITR 基础
- **[Failover：副本提升](../failover/)**：非 Patroni 的单副本提升路径（`pg replica`）
- **[Namespace 隔离](../namespace/)** —— scope、stanza、容器名都带 namespace 前缀
