---
title: 管理
description: pgcli 管理指南
weight: 20
icon: fa-solid fa-book
cascade:
  type: docs
  footer_style: slim
---


## Shell 补全

为命令、标志和实例名称启用 tab 补全。

### Bash

```bash
# Linux
pg completion bash > /etc/bash_completion.d/pg

# macOS（使用 Homebrew bash-completion）
pg completion bash > $(brew --prefix)/etc/bash_completion.d/pg

# 或在当前会话中加载
source <(pg completion bash)
```

### Zsh

```bash
# 启用补全系统（一次）
echo "autoload -U compinit; compinit" >> ~/.zshrc

# 安装补全
pg completion zsh > "${fpath[1]}/_pg"
```

### Fish

```bash
pg completion fish > ~/.config/fish/completions/pg.fish
```

### PowerShell

```powershell
pg completion powershell > pg.ps1
# 从你的 PowerShell 配置文件加载
```

## PostgreSQL 配置

通过 `pg exec` 使用 `ALTER SYSTEM` 修改 PostgreSQL 运行时参数，然后重新加载：

```bash
# 更改参数
pg exec "ALTER SYSTEM SET work_mem = '256MB'"
pg exec "SELECT pg_reload_conf()"

# 针对特定实例
pg exec -i proj01 "ALTER SYSTEM SET effective_cache_size = '4GB'"
pg exec -i proj01 "SELECT pg_reload_conf()"
```

**注意：** 某些参数（例如 `shared_buffers`、`max_connections`）需要重启而不是重新加载。使用 `pg stop && pg start` 应用这些更改。

## 配置文件管理

检查或验证配置文件（默认为 `~/.pgcli/pg.yaml`，使用 `-c` 覆盖）。

```bash
# 显示当前配置（YAML）
pg config show

# 以 JSON 格式显示配置
pg config show --json

# 验证配置文件结构
pg config validate
```

Init 生成默认配置；`--add` 在同一文件中创建命名实例，`-o` 写入自定义路径：

```bash
pg config init --add default --base-dir /data/pg
pg config init --add proj01 --base-dir /data/pg -o ./my-pg.yaml
```

在一个主机上运行多个配置的隔离参数（参见[快速开始](quickstart.md)进行规划）：

```bash
pg config init --namespace t1 --pg-start-port 38000 --pg-ssh-port 43000 --add proj01 -o ~/.pgcli-t1/pg.yaml
```

| 参数 | 默认值 | 含义 |
|------|--------|------|
| `--namespace` | `default` | 容器名称前缀：`pgcli-pg-<namespace>-<instance>`，备份容器 `pgcli-backup-<namespace>`。传递 `--namespace ""` 保持旧式名称不带前缀 |
| `--pg-start-port` | `35432` | 分配范围中的第一个 PG 主机端口 |
| `--pg-ssh-port` | `42201` | 分配范围中的第一个 SSH 主机端口 |

所有三个都保存到配置文件中（`namespace`、`pg_start_port`、`pg_ssh_port`）；跨配置使用不重叠的端口范围，这样分配永远不会冲突。
