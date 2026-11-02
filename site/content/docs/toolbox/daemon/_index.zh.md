---
title: "Daemon"
description: "在宿主机后台运行 webhook 接收端等长期命令，并可选地开机自启"
weight: 50
icon: fa-solid fa-server
aliases: ["/toolbox/daemon/"]
menus:
  main:
    identifier: docs-toolbox-daemon
    parent: docs-toolbox
    weight: 50
    params:
      icon: fa-solid fa-server
cascade:
  type: docs
  footer_style: slim
---

`pg daemon` 直接在 Linux 宿主机上登记并运行任意长期命令——脚本、FastAPI/uvicorn
webhook 接收端、同步 agent，或任何需要在后台常驻的程序。整个过程不涉及容器：进程
由 pgcli 管理，用 pid 文件跟踪，并可接入现有的开机自启服务。

登记与启动是两个独立步骤：`pg daemon add` 只记录命令，`pg daemon start` 才真正
启动。这样 `pg.yaml` 保持声明式，命令也可以先登记、稍后再运行。

目前仅支持 Linux。

## 工作原理

- 每个 daemon 在 `pg.yaml` 中有一条记录，并在 `<base>/daemon/<name>/` 下有独立的
  状态目录。
- `pg daemon add` 会把可执行文件解析为绝对路径，开机时不再依赖 `PATH`；参数按
  原样保存。
- `pg daemon start` 用 `setsid` 启动进程，把 stdout/stderr 追加到日志，并记录 pid。
  `pg` 命令退出后进程继续运行。
- 存活检查会比对 `/proc/<pid>/cmdline`，避免 pid 被复用时误判。
- 停止时向整个进程组发信号：先 `SIGTERM`，等待 `--timeout`，仍未退出则
  `SIGKILL`。

未指定 `--log-file` 时的状态布局：

| 路径 | 内容 |
|---|---|
| `<base>/daemon/<name>/daemon.log` | stdout 与 stderr（追加写，不轮转） |
| `<base>/daemon/<name>/daemon.pid` | `<pid>` 与记录的二进制路径 |

`<base>` 优先取 `pg.yaml` 中的 `base_dir`，否则使用平台默认目录（通常是
`~/.pgcli`）。

## 登记 daemon

daemon 名称与命令之间必须用 `--` 分隔。`--` 之前的参数配置 `pg daemon`，之后的
全部属于被运行的命令：

```bash
pg daemon add <name> [--dir DIR] [--log-file PATH] [--force] -- <command> [args...]
```

```bash
# 用独立 virtualenv 运行 uvicorn webhook
pg daemon add webhook --dir /opt/webhook -- \
  /opt/webhook/.venv/bin/uvicorn app:app --host 0.0.0.0 --port 18801

# 运行 shell 脚本
pg daemon add heartbeat --dir /opt/scripts -- /opt/scripts/heartbeat.sh

# 内联循环（引号由当前 shell 处理）
pg daemon add tick -- /bin/sh -c 'while true; do date; sleep 60; done'
```

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--dir` | 执行 `add` 时的当前目录 | 进程的工作目录；必须存在 |
| `--log-file` | `<base>/daemon/<name>/daemon.log` | 保存为绝对路径。**相对路径按 `--dir` 解析**；启动时自动创建父目录 |
| `--force` | false | 替换已存在的登记；要求 daemon 当前未运行 |

注意：

- 裸命令名（如 `uvicorn`）会在**登记时**从 `PATH` 查找并保存为绝对路径，
  `--dir` 不参与这个查找。包含 `/` 的命令——包括 `./run.sh`、`../bin/x`——
  在登记时按 `--dir` 解析，且必须存在、可执行。
- 命令参数原样保存，运行时以 `--dir` 为工作目录执行，因此参数里的相对路径会
  自然生效：

  ```bash
  pg daemon add webhook --dir /opt/webhook -- \
    ./.venv/bin/uvicorn app:app --port 18801
  # add 时 argv[0] 解析为 /opt/webhook/.venv/bin/uvicorn；
  # 运行时 "app:app" 由 uvicorn 在 /opt/webhook 下解析。
  ```

  裸命令名本身不会按 `--dir` 解析——可执行文件在 `--dir` 下时要写成 `./name`。
- **`--log-file`：相对路径按 `--dir` 解析**——与进程的工作目录一致。
  `--dir /opt/webhook --log-file logs/hook.log` 写入
  `/opt/webhook/logs/hook.log`；绝对路径则原样使用。
- **统一路径风格。** 命令、命令里的文件参数、`--log-file` 要么全部写绝对
  路径，要么全部写相对 `--dir` 的路径。混用容易搞不清某个路径到底相对哪个
  目录解析。

  ```bash
  # 全部相对 --dir
  pg daemon add webhook --dir /opt/webhook --log-file logs/webhook.log -- \
    ./.venv/bin/uvicorn app:app --port 18801

  # 全部绝对
  pg daemon add webhook --dir /opt/webhook --log-file /opt/webhook/logs/webhook.log -- \
    /opt/webhook/.venv/bin/uvicorn --app-dir /opt/webhook app:app --port 18801
  ```
- 推荐直接写解释器或 virtualenv 二进制的绝对路径。像 `#!/usr/bin/env python3`
  这样的 shebang 依赖进程继承的环境，而交互式 shell 与开机服务的环境并不相同。
- `--force` 会保留原有的 `autostart` 标记；如果 daemon 正在运行则拒绝替换——先
  执行 `pg daemon stop`。
- daemon 名称规则与 SQLite 数据库一致：
  `[A-Za-z0-9][A-Za-z0-9_-]{0,47}`。

## 启动与日常操作

```bash
pg daemon start webhook
pg daemon status webhook        # 或: pg daemon status（查看全部）
pg daemon list
pg daemon logs webhook -n 100
pg daemon logs webhook -f
pg daemon stop webhook
pg daemon restart webhook
pg daemon stop webhook --timeout 30s
```

- `start` 是幂等的。若记录的进程仍存活，只打印 pid，不做任何事。
- 启动后会等待约半秒。如果进程已经退出——解释器错误、import 失败、端口被占用
  ——命令会失败并打印日志末尾 20 行，便于立刻定位原因。
- `stop` 作用于整个进程组，包装脚本派生的 worker 不会残留。它**不会**改变
  `autostart` 标记，与实例的 `pg stop` 行为一致。
- `status` 显示运行状态、pid、命令、工作目录、日志路径和 autostart 标记；
  `list` 以表格形式每行输出一个 daemon。

## 移除

```bash
pg daemon remove webhook
pg daemon remove webhook --clean-log
```

`remove` 先停止进程，再从 `pg.yaml` 删除记录。若曾启用 autostart，会刷新启动
服务；没有剩余目标时自动卸载。`--clean-log` 还会删除
`<base>/daemon/<name>/`（默认日志与 pid 文件）。自定义 `--log-file` 不会被删除，
命令会给出提示。

## 开机自启

`pg daemon` 复用 pgcli 现有的启动服务：

```bash
pg daemon add webhook --dir /opt/webhook -- \
  /opt/webhook/.venv/bin/uvicorn app:app --host 0.0.0.0 --port 18801

pg autostart enable --daemon --name webhook
pg autostart status
```

`--name` 是必填项，daemon 没有默认名称。开机时 `pg start --autostart` 会在所有
实例与 addon **之后**启动 `autostart` 为 true 的 daemon，这样 webhook 或 agent
启动时，数据库、消息队列和反向代理后端都已就绪。

开机路径调用同一套幂等启动逻辑：已在运行的 daemon 不会被重复启动；若某个 daemon
立刻退出，会打印其日志末尾，其余 daemon 继续启动。

```bash
pg autostart disable --daemon --name webhook
```

需要提前规划的限制：

- 开机自启只启动一次，**不**监控或重启崩溃后的进程。需要崩溃恢复时，请在命令
  内部使用自己的 supervisor。
- 进程继承的是启动服务的环境，而不是交互式登录 shell 的环境。所需变量应写进
  命令自己的包装脚本或配置，不要依赖 shell profile。
- 如果 `pg daemon add` 与开机服务运行 pgcli 的用户不同，状态目录
  `<base>/daemon/<name>/` 必须对该用户可读写。

systemd/launchd 服务细节见 [开机自启](/docs/autostart/)。

## Webhook 接收端示例

一个完整的最小 FastAPI 接收端，把 Alertmanager 风格的 payload 追加写入日志：

```bash
mkdir -p /opt/webhook
python3 -m venv /opt/webhook/.venv
/opt/webhook/.venv/bin/pip install fastapi uvicorn
```

`/opt/webhook/app.py`：

```python
import datetime
import json

from fastapi import FastAPI, Request

app = FastAPI()


@app.post("/alertmanager")
async def receive(request: Request):
    payload = await request.json()
    line = json.dumps(
        {"received_at": datetime.datetime.now(datetime.UTC).isoformat(), "payload": payload},
        ensure_ascii=False,
    )
    with open("/opt/webhook/alerts.log", "a", encoding="utf-8") as f:
        f.write(line + "\n")
    return {"status": "ok"}
```

登记并启动：

```bash
pg daemon add webhook --dir /opt/webhook -- \
  /opt/webhook/.venv/bin/uvicorn app:app --host 0.0.0.0 --port 18801

pg daemon start webhook
pg daemon status webhook
```

发送测试告警：

```bash
curl -sS -X POST http://127.0.0.1:18801/alertmanager \
  -H 'Content-Type: application/json' \
  -d '{"status":"firing","alerts":[{"alertname":"TestAlert"}]}'
```

预期响应：

```json
{"status":"ok"}
```

分别检查接收端日志和应用 payload 日志：

```bash
pg daemon logs webhook -n 50
tail -n 1 /opt/webhook/alerts.log
```

## 故障排查

**`daemon ... exited immediately`** — `pg daemon start` 已经打印了日志末尾；执行
`pg daemon logs <name> -n 100` 查看更多上下文。常见原因是缺少 Python 依赖、
`--dir` 错误或端口已被占用。

**`command not found in PATH`** — 执行 `pg daemon add` 的 shell 看不到该裸命令。
改用绝对路径，或把二进制安装到当前环境可见的位置，再用 `--force` 重新登记。

**daemon 启动后读不到 shell 变量** — 开机时进程运行在 systemd user service 下，
环境与登录 shell 不同。把配置写进命令的脚本，而不是 `~/.bashrc`。

**`daemon "x" is running; stop it before --force`** — 先
`pg daemon stop x`，再重新登记。

## 限制

- 仅支持 Linux。
- 每个 daemon 对应一个进程；没有监督、退避或重启策略。
- 日志只追加、不轮转。长期运行的接收端可使用自定义 `--log-file` 并配合外部
  轮转工具，或在命令内部实现轮转。
- 停止基于进程组。如果 daemon 主动脱离进程组（例如自己调用 `setsid`），其子进程
  可能残留。
- 登记保存的是命令行而不是 shell。管道、`&&` 等 shell 语法必须显式交给
  `/bin/sh -c '...'` 执行。
