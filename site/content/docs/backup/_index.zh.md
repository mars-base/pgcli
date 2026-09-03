---
title: 备份
description: pgcli 备份指南
weight: 20
icon: fa-solid fa-clock-rotate-left
cascade:
  type: docs
  footer_style: slim
---


## 快照

```bash
# 创建快照（完整备份）
pg snapshot create -i proj01

# 创建差异备份（推荐）
pg snapshot create --type diff -i proj01

# 快照期间流式输出备份容器日志
pg snapshot create --tail-logs -i proj01

# 列出快照
pg snapshot list -i proj01

# 限制显示的快照数量
pg snapshot list --limit 5 -i proj01

# 删除快照
pg snapshot delete 20260826-073712F -i proj01
```

**快照类型：**
- `full` — 完整备份（默认，自包含）
- `diff` — 自上次完整备份以来的更改
- `incr` — 自上次备份以来的更改

## 共享备份容器

所有实例共享单个 pgbackrest 容器；每个实例在存储库中都有自己的 stanza。

```bash
# 初始化共享 pgbackrest 容器（构建镜像、创建目录、生成配置）
pg backup setup

# 使用自定义基础目录存储备份数据和日志
pg backup setup --base-dir /mnt/backup

# 启动 / 停止备份容器
pg backup start
pg backup stop

# 显示备份容器状态
pg backup status
```

备份基础设施（网络、镜像、目录、配置、容器）在 `pg start` 时自动准备；手动运行 `pg backup setup` 重新初始化，例如更改基础目录之后。
