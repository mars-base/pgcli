# pgcli 容器镜像清单

pgcli 使用的容器镜像及其默认 tag。

## 核心组件

| 镜像 | Tag | 用途 | 配置常量 |
|------|-----|------|----------|
| `ghcr.io/mars-base/pgcli/pgcli-pg` | `18-2.58.0` | PostgreSQL 实例（单节点或 replica） | `DefaultPgImageTag` |
| `ghcr.io/mars-base/pgcli/pgcli-patroni` | `18-4.1.5` | Patroni HA 集群成员 | `DefaultPatroniImageTag` |
| `ghcr.io/mars-base/pgcli/pgcli-backup` | `2.58.0` | pgBackRest 备份仓库 | `DefaultBackupImageTag` |

## 插件 / 附加组件

| 镜像 | Tag | 用途 | 配置常量 |
|------|-----|------|----------|
| `ghcr.io/mars-base/pgcli/pgcli-minio` | `20250422221226` | MinIO S3 兼容存储 | `DefaultMinioImageTag` |
| `ghcr.io/mars-base/pgcli/pgcli-mc` | `20250813083541` | MinIO 客户端 (`mc`) | `DefaultMCImageTag` |
| `docker.io/pgsty/silo` | `RELEASE.2026-09-16T00-00-00Z` | silo（Pigsty 的 MinIO 分支）S3 存储 + `mcli` 客户端 | `DefaultSiloImageTag` |
| `docker.io/edoburu/pgbouncer` | `v1.25.2-p0` | PostgreSQL 连接池 | `DefaultPgBouncerImageTag` |
| `docker.io/library/haproxy` | `3.2.23-alpine` | HTTP/TCP 负载均衡 | `DefaultHAProxyImageTag` |
| `quay.io/coreos/etcd` | `v3.5.30` | 分布式键值存储（Patroni DCS） | `etcdctl.go:69` |
| `ghcr.io/pgdogdev/pgdog` | `v0.1.57` | PostgreSQL 代理 / 连接池 | `config.go:204` |

## 镜像导出

将所有镜像导出到本地存储（用于离线环境或备份）：

```bash
# 导出到指定目录
TARGET=/path/to/images
mkdir -p "$TARGET"

podman save -o "$TARGET/pgcli-pg_18-2.58.0.tar" \
  ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0

podman save -o "$TARGET/pgcli-patroni_18-4.1.5.tar" \
  ghcr.io/mars-base/pgcli/pgcli-patroni:18-4.1.5

podman save -o "$TARGET/pgcli-backup_2.58.0.tar" \
  ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0

podman save -o "$TARGET/pgcli-minio_20250422221226.tar" \
  ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226

podman save -o "$TARGET/pgcli-mc_20250813083541.tar" \
  ghcr.io/mars-base/pgcli/pgcli-mc:20250813083541

podman save -o "$TARGET/silo_RELEASE.2026-09-16T00-00-00Z.tar" \
  docker.io/pgsty/silo:RELEASE.2026-09-16T00-00-00Z

podman save -o "$TARGET/pgbouncer_v1.25.2-p0.tar" \
  docker.io/edoburu/pgbouncer:v1.25.2-p0

podman save -o "$TARGET/haproxy_3.2.23-alpine.tar" \
  docker.io/library/haproxy:3.2.23-alpine

podman save -o "$TARGET/etcd_v3.5.30.tar" \
  quay.io/coreos/etcd:v3.5.30

podman save -o "$TARGET/pgdog_v0.1.57.tar" \
  ghcr.io/pgdogdev/pgdog:v0.1.57
```

## 镜像加载（离线环境）

在目标主机上加载导出的镜像：

```bash
for img in /path/to/images/*.tar; do
  podman load -i "$img"
done
```

## 版本说明

- **pgcli-pg** / **pgcli-patroni**：PostgreSQL 18 + pgBackRest 2.58.0
- **pgcli-backup**：独立备份容器（pgBackRest 2.58.0）
- **pgcli-minio** / **pgcli-mc**：MinIO 固定版本，避免上游不兼容变更
- **silo**：Pigsty 官方发布的公开镜像（pull-only），版本随 Pigsty 发布节奏
- **pgbouncer** / **haproxy** / **etcd** / **pgdog**：上游官方或社区维护的稳定版本

---

**最后更新**：2026-09-20（v2.1.2）
