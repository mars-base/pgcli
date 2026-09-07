---
title: 开机自启
description: 配置 pgcli 在主机重启后自动启动 PostgreSQL 实例和服务
weight: 60
icon: fa-solid fa-power-off
---

pgcli 可以在主机重启后自动启动 PostgreSQL 实例、备份容器和 PgBouncer 服务。此功能使用系统服务管理器：
- **Linux**: systemd 用户单元
- **macOS**: launchd LaunchAgents

## 工作原理

启用开机自启后，pgcli 会创建一个系统服务，在开机时（或用户登录时）运行 `pg start --autostart`。`--autostart` 标志仅启动配置中标记为 `autostart: true` 的实例和服务。

开机自启是**配置驱动**的：如果你对某个实例执行了 `pg stop`（或 `pg stop --all`），它仍然会在下次开机时自动启动。要阻止自动启动，请使用 `pg autostart disable`。

**默认行为**：备份容器在新配置中默认 `autostart: true`。实例和 PgBouncer 默认 `autostart: false`，需要手动启用。

## 启用开机自启

### 单个实例

```bash
pg autostart enable -i <实例名>
```

示例：
```bash
pg autostart enable -i default
```

这会在实例配置中添加 `autostart: true` 并创建/更新开机服务。

### 备份容器

```bash
pg autostart enable --backup
```

启用共享 pgBackRest 备份容器的开机自启。

### PgBouncer

启用与特定实例关联的 PgBouncer 的开机自启：
```bash
pg autostart enable --pgbouncer -i <实例名>
```

或为远程 PgBouncer 启用：
```bash
pg autostart enable --pgbouncer --pg-name <远程名称>
```

## 禁用开机自启

```bash
pg autostart disable -i <实例名>
pg autostart disable --backup
pg autostart disable --pgbouncer -i <实例名>
pg autostart disable --pgbouncer --pg-name <远程名称>
```

当所有开机自启目标都被禁用后，开机服务会被自动移除。

## 查看状态

```bash
pg autostart status
```

显示：
- 哪些实例和服务已启用开机自启
- 开机服务单元名称和状态
- Linger 状态（Linux）—— rootless podman 在开机时启动服务所需

## 平台特定行为

### Linux (systemd)

开机服务以 systemd 用户单元创建：`pgcli-autostart-<hash>.service`

**重要**：Rootless podman 需要 `loginctl enable-linger` 才能在开机时（用户登录前）启动容器。pgcli 会自动尝试此操作，但如果失败会打印提示。

没有 linger，服务会在用户登录时启动，而不是在系统开机时。

#### Systemd Unit 配置

pgcli 生成的 systemd unit 包含以下关键配置：

```ini
[Service]
Type=oneshot
Delegate=yes
RemainAfterExit=yes
```

- **Delegate=yes** - 将 cgroup 控制权委托给服务进程，允许 rootless podman 正确管理容器的 cgroup 层级
- **RemainAfterExit=yes** - 服务执行完成后保持 `active` 状态，而不是立即变为 `inactive`
- **Type=oneshot** - 一次性执行 `pg start --autostart`，然后退出

#### Cgroup 隔离与自动回退

**问题**：当容器由 systemd 用户单元启动时，从 SSH 会话或其他上下文访问容器时可能会遇到 cgroup 权限错误：

```
Error: crun: writing file `/sys/fs/cgroup/.../cgroup.procs`: Permission denied: OCI permission denied
```

**原因**：systemd 用户单元启动的容器位于 `user@1000.service/app.slice` cgroup 子树，而 SSH 会话位于 `session-N.scope`，两者在不同的 cgroup 作用域。

**解决方案**：pgcli 会自动检测 cgroup 权限错误，并通过 `systemd-run --user --scope` 重新执行命令。这会创建一个临时 scope，使其能够加入容器的 cgroup 子树。

**受影响的命令**（自动回退）：
- `pg exec` - 执行 SQL 或容器命令
- `pg psql` - 交互式 psql 会话
- `pg status` - 检查容器状态
- `pg backup` - 备份操作
- `pg extension` - 扩展管理
- 所有其他 `podman exec` 操作

**不受影响的命令**：
- `pg start` - 启动容器（使用 systemd-run 本身）
- `pg stop` - 停止容器
- `pg restart` - 重启容器

#### 查看服务状态

`pg autostart status` 现在会显示详细的 systemctl 信息：

```bash
pg autostart status
```

输出包括：
- 所有开机自启目标的启用状态
- systemd unit 名称、加载状态、活跃状态
- 服务日志和最近执行记录
- Linger 状态

示例输出：
```
=== Auto-start targets ===
  instance default         enabled
  backup                 enabled

=== Boot service ===
  Unit:      pgcli-autostart-554d14ed
  Installed: yes
  Enabled:   enabled
  Running:   active
  Linger:    yes

=== Service status ===
     Loaded: loaded (/home/user/.config/systemd/user/pgcli-autostart-554d14ed.service; enabled)
     Active: active (exited) since Mon 2026-09-07 14:30:00 CST; 2h ago
    Process: 1234 pg -c /home/user/.config/pgcli/pg.yaml start --autostart (code=exited, status=0/SUCCESS)
   Main PID: 1234 (code=exited, status=0/SUCCESS)
```

### macOS (launchd)

开机服务以 LaunchAgent 创建：`com.pgcli.autostart-<hash>.plist`

**注意**：macOS LaunchAgents 在用户登录时运行，而不是在系统开机时。这是 macOS 的限制 —— rootless podman 无法在用户登录前启动服务。

## 配置文件

`autostart: true` 标志会出现在你的 pgcli 配置文件中：

```yaml
instances:
  default:
    # ... 其他设置 ...
    autostart: true

backup:
  # ... 其他设置 ...
  autostart: true

# 对于 PgBouncer
addons:
  pgbouncer:
    default:
      # ... 其他设置 ...
      autostart: true
```

## 开机服务行为

开机服务运行 `pg start --autostart`，它会：
1. 启动所有 `autostart: true` 的实例
2. 如果 `backup.autostart: true`，启动备份容器
3. 启动 `autostart: true` 的 PgBouncer 服务

如果没有配置任何开机自启目标，服务会正常退出（无错误）。

## 故障排查

### 服务启动失败

查看开机服务日志：

**Linux (systemd)**：
```bash
journalctl --user -u pgcli-autostart-<hash>.service
```

**macOS (launchd)**：
```bash
cat ~/Library/Logs/pgcli-autostart-<hash>.log
```

### Linux: "XDG_RUNTIME_DIR is not set"

此错误表示 systemd 用户单元不可用。确保你有正确的用户会话：
- SSH 会话默认支持 systemd --user
- 桌面环境中的终端会话支持 systemd --user
- Cron 任务和其他非交互式上下文不支持 systemd --user

### Linux: 开机自启仅在登录后生效

运行 `loginctl enable-linger <你的用户名>` 以允许 rootless podman 在开机时启动服务。

### macOS: 开机自启仅在登录后生效

这是预期行为。由于 rootless podman 的限制，macOS LaunchAgents 无法在用户登录前运行。

## 示例

为完整的生产环境启用开机自启：

```bash
# 启用 default 实例
pg autostart enable -i default

# 启用备份容器
pg autostart enable --backup

# 为 default 实例启用 PgBouncer
pg autostart enable --pgbouncer -i default

# 检查状态
pg autostart status
```

下次重启后，这三个服务都会自动启动。
