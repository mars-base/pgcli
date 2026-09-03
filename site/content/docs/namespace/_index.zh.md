---
title: "命名空间隔离"
description: "使用命名空间在单台主机上创建隔离的环境"
weight: 15
icon: fa-solid fa-layer-group
menus:
  main:
    identifier: docs-namespace
    parent: docs
    weight: 15
    params:
      icon: fa-solid fa-layer-group
cascade:
  type: docs
  footer_style: slim
---

命名空间允许你在单台主机上创建完全隔离的环境，非常适合将生产、开发和测试环境分开，避免容器名称冲突或端口冲突。

## 什么是命名空间？

命名空间是应用于配置文件中所有容器名称的前缀。这种隔离机制确保：

- **容器名称不冲突**：每个命名空间有自己的容器前缀
- **端口范围分离**：每个配置文件从自己的范围分配端口
- **备份容器隔离**：每个命名空间有自己的 pgBackRest 容器
- **配置文件独立**：每个环境使用单独的配置文件

## 使用场景：生产和开发环境

一个常见场景是在同一台服务器上运行生产和开发环境：

```bash
# 创建生产环境
pg config init \
  --namespace prod \
  --pg-start-port 35432 \
  --pg-ssh-port 42201 \
  --add app-db \
  -o ~/.pgcli-prod/pg.yaml

# 创建开发环境
pg config init \
  --namespace dev \
  --pg-start-port 38000 \
  --pg-ssh-port 43000 \
  --add app-db \
  -o ~/.pgcli-dev/pg.yaml
```

这会创建两个完全隔离的环境：

| 环境 | 配置文件 | 容器前缀 | PG 端口范围 | SSH 端口范围 |
|------|----------|----------|-------------|--------------|
| 生产 | `~/.pgcli-prod/pg.yaml` | `pgcli-pg-prod-*` | 35432+ | 42201+ |
| 开发 | `~/.pgcli-dev/pg.yaml` | `pgcli-pg-dev-*` | 38000+ | 43000+ |

## 管理多个环境

使用 `-c` 标志指定要使用的配置文件：

```bash
# 启动生产数据库
pg -c ~/.pgcli-prod/pg.yaml start -i app-db

# 启动开发数据库
pg -c ~/.pgcli-dev/pg.yaml start -i app-db

# 列出生产环境实例
pg -c ~/.pgcli-prod/pg.yaml list

# 列出开发环境实例
pg -c ~/.pgcli-dev/pg.yaml list
```

## 工作原理

### 容器命名

命名空间为 `prod`，实例为 `app-db`：
- 实例容器：`pgcli-pg-prod-app-db`
- 备份容器：`pgcli-backup-prod`
- 网络：`pgcli-net-prod`（如果使用独立网络）

没有命名空间（或 `--namespace ""`）：
- 实例容器：`pgcli-pg-default-app-db`
- 备份容器：`pgcli-backup-default`

### 端口分配

配置文件中的每个实例获得顺序端口：
- 第一个实例：`pg_start_port`（例如 35432）
- 第二个实例：`pg_start_port + 1`（例如 35433）
- 依此类推...

SSH 端口同理。

### 配置持久化

命名空间和端口范围保存在配置文件中：

```yaml
namespace: prod
pg_start_port: 35432
pg_ssh_port: 42201
```

## 最佳实践

### 1. 始终使用显式命名空间

在一台主机上运行多个配置时，永远不要依赖默认命名空间：

```bash
# 错误：两个配置都会使用 "default" 命名空间并冲突
pg config init --add app -o ~/.pgcli-prod/pg.yaml
pg config init --add app -o ~/.pgcli-dev/pg.yaml  # 冲突！

# 正确：显式命名空间
pg config init --namespace prod --add app -o ~/.pgcli-prod/pg.yaml
pg config init --namespace dev --add app -o ~/.pgcli-dev/pg.yaml
```

### 2. 使用不重叠的端口范围

确保配置之间的端口范围不重叠：

```bash
# 生产：35432-35999, 42201-42999
pg config init --namespace prod \
  --pg-start-port 35432 \
  --pg-ssh-port 42201 \
  --add app -o ~/.pgcli-prod/pg.yaml

# 开发：38000-38999, 43000-43999
pg config init --namespace dev \
  --pg-start-port 38000 \
  --pg-ssh-port 43000 \
  --add app -o ~/.pgcli-dev/pg.yaml

# 测试：40000-40999, 44000-44999
pg config init --namespace test \
  --pg-start-port 40000 \
  --pg-ssh-port 44000 \
  --add app -o ~/.pgcli-test/pg.yaml
```

为每个环境中的多个实例预留足够的端口空间。

### 3. 命名空间在创建时固化

命名空间在创建时嵌入容器名称。之后更改会破坏关联：

```bash
# 使用命名空间 "prod" 创建
pg -c ~/.pgcli-prod/pg.yaml start -i app-db
# 容器：pgcli-pg-prod-app-db

# 编辑配置将命名空间改为 "production"
# 这样不行 - 容器名称不匹配！

# 正确做法：销毁并重建
pg -c ~/.pgcli-prod/pg.yaml destroy -i app-db --clean-data
pg -c ~/.pgcli-prod/pg.yaml create -i app-db
pg -c ~/.pgcli-prod/pg.yaml start -i app-db
```

### 4. 使用 Shell 别名简化操作

创建别名避免重复输入 `-c`：

```bash
# 添加到 ~/.bashrc 或 ~/.zshrc
alias pg-prod='pg -c ~/.pgcli-prod/pg.yaml'
alias pg-dev='pg -c ~/.pgcli-dev/pg.yaml'
alias pg-test='pg -c ~/.pgcli-test/pg.yaml'

# 然后使用：
pg-prod start -i app-db
pg-dev list
pg-test destroy -i test-db --force
```

## 高级用法：带副本的多环境

你甚至可以设置隔离的复制环境：

```bash
# 生产：主实例 + 副本
pg -c ~/.pgcli-prod/pg.yaml create -i primary --base-dir /data/prod
pg -c ~/.pgcli-prod/pg.yaml replica create replica -i primary

# 开发：独立的主实例 + 副本
pg -c ~/.pgcli-dev/pg.yaml create -i primary --base-dir /data/dev
pg -c ~/.pgcli-dev/pg.yaml replica create replica -i primary
```

每个环境维护自己的复制槽、备份 stanza 和数据目录。

## 故障排除

### 容器名称冲突

```
Error: container "pgcli-pg-default-app-db" already exists
```

**原因**：两个配置使用了相同的命名空间。

**解决**：使用不同的命名空间，或先销毁冲突的实例。

### 端口已被占用

```
Error: listen tcp 0.0.0.0:35432: bind: address already in use
```

**原因**：配置之间的端口范围重叠。

**解决**：使用不重叠的端口范围，并预留足够间距。

### 更改命名空间后找不到实例

```
Error: instance "app-db" exists in config but container not found
```

**原因**：创建实例后更改了配置文件中的命名空间。

**解决**：销毁并重建实例，或恢复命名空间更改。

## 总结

命名空间为单台主机上的多环境提供完全隔离：

- **独立配置**：每个环境有自己的 `pg.yaml`
- **不同命名空间**：防止容器名称冲突
- **不重叠端口**：避免端口冲突
- **独立操作**：每个环境使用 `-c` 单独管理

非常适合在同一台服务器上运行生产、开发、测试和预发布环境，互不干扰。
