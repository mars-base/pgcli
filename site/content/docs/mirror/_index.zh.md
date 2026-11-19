---
title: "配置 Mirror"
description: "为 docker.io、ghcr.io、quay.io 配置 podman registry mirror"
weight: 11
icon: fa-solid fa-network-wired
menus:
  main:
    identifier: docs-mirror
    parent: docs
    weight: 11
    params:
      icon: fa-solid fa-network-wired
cascade:
  type: docs
  footer_style: slim
---

在无法访问 `docker.io`、`ghcr.io` 或 `quay.io` 的网络中，可以让 podman 通过
registry mirror 拉取镜像。pgcli 用单个 drop-in 文件管理这三个 registry，既可在
安装时配置，也可在安装后随时修改。

## 安装时配置

`--docker-mirror HOST` 是 `docker.io` 的简写；需要配置多个 registry 时，重复
使用 `--registry-mirror REGISTRY=HOST`：

```bash
# docker.io 简写
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash -s -- --docker-mirror docker.m.daocloud.io

# 一个或多个 registry
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash -s -- \
  --registry-mirror ghcr.io=ghcr.m.daocloud.io \
  --registry-mirror quay.io=quay.m.daocloud.io
```

两种参数写入同一个 drop-in 文件（仅 Linux）：

- root 或具备 passwordless sudo：`/etc/containers/registries.conf.d/99-pgcli-registry-mirror.conf`
- 其他情况：`~/.config/containers/registries.conf.d/99-pgcli-registry-mirror.conf`

没有配置 mirror 的 registry 保持直连。Podman 会先尝试 mirror，mirror 中没有
对应镜像时回退到上游 registry。

## 安装后管理

```bash
pg mirror set docker.m.daocloud.io        # docker.io 简写
pg mirror set ghcr.io=ghcr.m.daocloud.io \
              quay.io=quay.m.daocloud.io  # 同时配置多个 registry
pg mirror show                            # 查看每个 registry 的生效 mirror
pg mirror clean                           # 删除全部 pgcli 管理的配置
pg mirror clean ghcr.io                   # 只删除指定 registry
```

每次 `set` 都会重写托管文件（即使值未变化），并保留本次未提及的 registry 的
mirror。旧的 `99-pgcli-docker-mirror.conf` 会在下次 `set` 时自动迁移并删除。

`show` 会列出每个已配置 registry 及其生效来源。`clean` 不带参数时删除全部托管
文件；带 registry 参数时，只从 system 和 user 两个作用域中删除指定的 mirror。

## 文件优先级

同一 registry 的用户级配置会覆盖 system 级配置。`set` 写入 system 文件时，只
删除调用账号用户级配置中与 system 冲突的 registry 条目，其余用户级条目保留。
如果 system 文件存在但当前账号没有 passwordless sudo，`clean` 会在删除任何文件
之前停止，并提示运行 `sudo pg mirror clean`。

## HOST 格式

`HOST` 支持主机名、`host:port`、`[IPv6]`、`[IPv6]:port`。该命令不探测
连通性、不读写 `pg.yaml`，且仅支持 Linux。配置对后续 pull 生效，运行中的容器
不受影响。
