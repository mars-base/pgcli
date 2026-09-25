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
| `ghcr.io/mars-base/pgcli/pgcli-rustfs` | `1.0.0` | rustfs（Rust 实现的 S3 存储），建立在 `docker.io/rustfs/rustfs:1.0.0` 之上的 wrapper | `DefaultRustfsImageTag` |
| `docker.io/edoburu/pgbouncer` | `v1.25.2-p0` | PostgreSQL 连接池 | `DefaultPgBouncerImageTag` |
| `docker.io/postgrest/postgrest` | `v16.3` | PostgREST（REST API over PostgreSQL） | `DefaultPostgrestImageTag` |
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

podman save -o "$TARGET/postgrest_v16.3.tar" \
  docker.io/postgrest/postgrest:v16.3

podman save -o "$TARGET/pgbouncer_v1.25.2-p0.tar" \
  docker.io/edoburu/pgbouncer:v1.25.2-p0

podman save -o "$TARGET/haproxy_3.2.23-alpine.tar" \
  docker.io/library/haproxy:3.2.23-alpine

podman save -o "$TARGET/etcd_v3.5.30.tar" \
  quay.io/coreos/etcd:v3.5.30

podman save -o "$TARGET/pgdog_v0.1.57.tar" \
  ghcr.io/pgdogdev/pgdog:v0.1.57
```

### rustfs（双架构 manifest 不能直接 save）

`pgcli-rustfs:1.0.0` 在 ghcr 上是一个 **manifest list（amd64 + arm64）**，而
`podman save` 的 `docker-archive` 格式不接受 manifest list。离线导出只需要目标
主机那一个架构。`podman save` 写进 tar 的 RepoTags 就是所传的引用名，所以要
把**单架构镜像 retag 成规范名**再保存，目标主机 load 后才直接得到
`ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0`：

```bash
# 按平台 pull：本地 :1.0.0 变为单架构（amd64）镜像，不再是 manifest
podman pull --platform linux/amd64 ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
podman save --format docker-archive \
  -o "$TARGET/pgcli-rustfs_1.0.0.tar" \
  ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0
```

若目标主机是 arm64，把 `linux/amd64` 换成 `linux/arm64`。若本机刚跑过
`make container-build-rustfs`（本地 `:1.0.0` 是双架构 manifest），等价做法是
把其产出的单架构 tag 覆盖到规范名再 save——`podman tag …:1.0.0-amd64
…:1.0.0 && podman save …:1.0.0`，之后用 `make container-build-rustfs` 恢复
本地 manifest。

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
- **pgcli-rustfs**：建立在 `docker.io/rustfs/rustfs:1.0.0`（首个 GA）之上的
  pgcli wrapper（pull-only），tag 跟随被 pin 的上游版本；ghcr 上为双架构
  manifest list
- **pgbouncer** / **postgrest** / **haproxy** / **etcd** / **pgdog**：上游官方或社区维护的稳定版本

---

**最后更新**：2026-09-25（v2.1.5）
