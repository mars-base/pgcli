---
title: "Daemon"
description: "Run long-lived host commands such as webhook receivers in the background and optionally start them on boot"
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

`pg daemon` registers and runs arbitrary long-lived commands directly on a Linux
host — a script, a FastAPI/uvicorn webhook receiver, a sync agent, or anything
else that should stay in the background. There is no container involved: the
process is owned by pgcli, tracked with a pid file and can be wired into the
existing boot service.

Registration and starting are separate steps: `pg daemon add` only records the
command, `pg daemon start` launches it. This keeps `pg.yaml` declarative and
lets a command be staged before it is first run.

Only Linux is supported.

## How it works

- Each daemon has an entry in `pg.yaml` and a state directory under
  `<base>/daemon/<name>/`.
- `pg daemon add` resolves the executable to an absolute path, so starting at
  boot does not depend on `PATH`. Arguments are stored verbatim.
- `pg daemon start` launches the process with `setsid` and records the pid; the
  process survives the `pg` command exiting. When rotation is enabled, a small
  supervisor process forwards stdout/stderr to the rotating log instead (see
  [Log rotation](#log-rotation)).
- Liveness is checked against `/proc/<pid>/cmdline`, so a recycled pid is not
  mistaken for the daemon.
- Stopping signals the whole process group: `SIGTERM`, wait up to
  `--timeout`, then `SIGKILL`.

State layout (when `--log-file` is not set):

| Path | Contents |
|---|---|
| `<base>/daemon/<name>/daemon.log` | stdout and stderr (append; rotated when configured) |
| `<base>/daemon/<name>/daemon.pid` | `<pid>` and the recorded binary path |
| `<base>/daemon/<name>/supervisor.log` | rotation supervisor diagnostics (only with rotation) |

`<base>` is `base_dir` from `pg.yaml` when set, otherwise the platform default
(usually `~/.pgcli`).

## Register a daemon

The daemon name and its command must be separated by `--`. Flags before `--`
configure `pg daemon`; everything after it belongs to the command:

```bash
pg daemon add <name> [--dir DIR] [--log-file PATH] \
  [--log-max-size SIZE] [--log-rotate-daily] [--log-keep N] \
  [--force] -- <command> [args...]
```

```bash
# A uvicorn webhook from its own virtualenv
pg daemon add webhook --dir /opt/webhook -- \
  /opt/webhook/.venv/bin/uvicorn app:app --host 0.0.0.0 --port 18801

# A shell script
pg daemon add heartbeat --dir /opt/scripts -- /opt/scripts/heartbeat.sh

# An inline loop (quoting belongs to your shell)
pg daemon add tick -- /bin/sh -c 'while true; do date; sleep 60; done'
```

| Flag | Default | Notes |
|---|---|---|
| `--dir` | current directory at `add` time | Working directory of the process; must exist |
| `--log-file` | `<base>/daemon/<name>/daemon.log` | Stored as an absolute path. **A relative value resolves against `--dir`**; parent dir is created on start |
| `--log-max-size` | disabled | Rotate when the log exceeds this size, e.g. `10M`, `1G` |
| `--log-rotate-daily` | false | Rotate at local midnight |
| `--log-keep` | 7 | Rotated files to retain; `0` keeps all. Requires one of the rotation triggers |
| `--force` | false | Replace an existing registration; the daemon must be stopped |

Notes:

- A bare command name (`uvicorn`) is looked up in `PATH` **at registration
  time** and stored as an absolute path; `--dir` is not part of that lookup.
  A command containing `/` — including `./run.sh` and `../bin/x` — is resolved
  against `--dir` at registration time, and must exist and be executable.
- Arguments are stored verbatim and interpreted at run time with the working
  directory set to `--dir`, so relative paths in arguments work as you would
  expect:

  ```bash
  pg daemon add webhook --dir /opt/webhook -- \
    ./.venv/bin/uvicorn app:app --port 18801
  # argv[0] becomes /opt/webhook/.venv/bin/uvicorn at add time;
  # "app:app" is resolved by uvicorn from /opt/webhook at run time.
  ```

  A relative path in a *bare* command name is not resolved this way — use
  `./name` when the executable lives in `--dir`.
- **`--log-file`: relative paths are resolved against `--dir`** — the same
  directory the process runs in. `--log-file logs/hook.log` with
  `--dir /opt/webhook` writes to `/opt/webhook/logs/hook.log`; an absolute
  value is used as-is.
- **Keep one path style.** Write every path — the command, its file arguments,
  and `--log-file` — either all as absolute paths, or all relative to `--dir`.
  Mixing the two makes it easy to lose track of which directory a given path
  resolves against.

  ```bash
  # all relative to --dir
  pg daemon add webhook --dir /opt/webhook --log-file logs/webhook.log -- \
    ./.venv/bin/uvicorn app:app --port 18801

  # all absolute
  pg daemon add webhook --dir /opt/webhook --log-file /opt/webhook/logs/webhook.log -- \
    /opt/webhook/.venv/bin/uvicorn --app-dir /opt/webhook app:app --port 18801
  ```
- Prefer the absolute path of an interpreter or virtualenv binary. A shebang
  such as `#!/usr/bin/env python3` depends on the environment the process
  inherits, which differs between an interactive shell and the boot service.
- `--force` keeps the existing `autostart` flag. It refuses to replace a
  daemon that is currently running — stop it first.
- Daemon names use the same rule as SQLite databases:
  `[A-Za-z0-9][A-Za-z0-9_-]{0,47}`.

## Start and operate

```bash
pg daemon start webhook
pg daemon status webhook        # or: pg daemon status (all daemons)
pg daemon list
pg daemon logs webhook -n 100
pg daemon logs webhook -f
pg daemon stop webhook
pg daemon restart webhook
pg daemon stop webhook --timeout 30s
```

- `start` is idempotent. If the recorded process is already alive it prints its
  pid and does nothing.
- After launching, `start` waits about half a second. If the process is already
  gone — bad interpreter, import error, port in use — it fails and prints the
  last 20 log lines, so the cause is visible immediately.
- `stop` affects the whole process group, so a wrapper script that spawned a
  worker does not leave the worker behind. It does **not** change the
  `autostart` flag, matching `pg stop` for instances.
- `status` shows state, pid, command, working directory, log path, rotation
  settings and autostart flag; `list` prints one table row per daemon.

## Log rotation

A long-running receiver should not grow its log without bound. Enable rotation
when the daemon is registered:

```bash
# rotate at 10 MB, keep the 7 most recent rotated files
pg daemon add webhook --dir /opt/webhook --log-max-size 10M --log-keep 7 -- \
  /opt/webhook/.venv/bin/uvicorn app:app --host 0.0.0.0 --port 18801

# rotate at local midnight, keep every rotated file
pg daemon add audit --log-rotate-daily --log-keep 0 -- /opt/bin/audit
```

- At least one trigger, `--log-max-size SIZE` or `--log-rotate-daily`, is
  required. Passing only `--log-keep` is an error.
- `SIZE` accepts `512K`, `10M`, `1G`, a byte count, and so on; it is rounded up
  to whole megabytes because that is the rotation library's granularity.
- `--log-keep N` retains the `N` newest rotated files. The default is `7`;
  `0` keeps all of them.
- Both triggers can be combined; whichever fires first rotates the file.

Rotated files live next to the active log and are named
`<base>-YYYY-MM-DDTHH-MM-SS.mmm<ext>`: `daemon.log` produces files such as
`daemon-2026-11-02T17-35-24.123.log`. The active log always keeps the configured
path, so `pg daemon logs` and external collectors can keep reading it.

Rotation is lazy. The size is checked on every write, so the file may slightly
exceed the limit before the next write triggers a rotation. A date boundary is
applied on the first write of the new day — an idle daemon does not rotate until
it writes again. If the daemon is restarted after midnight, the previous day's
file is moved aside before the first new line is appended. Timestamps use local
time.

With rotation enabled, pgcli keeps a small supervisor process
(`pg daemon __supervise <name>`) between the pid file and the command. It
forwards stdout/stderr and owns the log file; this is what allows rotation to
happen while no `pg` command is running. `pg daemon stop` still signals the
whole process group, so the supervisor and the command exit together.
Supervisor diagnostics are written to `<base>/daemon/<name>/supervisor.log`.

Two side effects of rotation:

- Newly created log files have mode `0600` (without rotation the log is created
  `0644`). Adjust permissions if another user must read it.
- `pg daemon logs <name> -f` switches to `tail -F`, so following continues in
  the new file after a rotation.

## Remove

```bash
pg daemon remove webhook
pg daemon remove webhook --clean-log
```

`remove` stops the process and deletes the entry from `pg.yaml`. If autostart
was enabled, the boot service is refreshed or removed when no targets remain.
`--clean-log` also deletes `<base>/daemon/<name>/` (the default log, rotated
backups, pid file and supervisor log). A custom `--log-file` is left untouched
and reported; its rotated siblings stay next to it as well.

## Auto-start on boot

`pg daemon` integrates with the existing pgcli boot service:

```bash
pg daemon add webhook --dir /opt/webhook -- \
  /opt/webhook/.venv/bin/uvicorn app:app --host 0.0.0.0 --port 18801

pg autostart enable --daemon --name webhook
pg autostart status
```

`--name` is required; there is no default daemon name. At boot,
`pg start --autostart` starts every registered daemon whose `autostart` flag is
true, **after** all instances and addons, so a webhook or agent can dial
databases, queues and reverse-proxy backends that are already up.

The boot path calls the same idempotent start logic: a daemon that is already
running is left alone, and one that exits immediately is reported with its log
tail while the rest of the boot pass continues.

```bash
pg autostart disable --daemon --name webhook
```

Limitations worth planning around:

- Autostart starts the process once. It does **not** supervise or restart it if
  it later crashes. Run the command under its own supervisor if you need
  crash recovery.
- The process inherits the environment of the boot service, not your
  interactive login shell. Put required variables in the command's own wrapper
  or configuration instead of relying on shell profiles.
- If pgcli itself runs as a different user under the boot service than during
  `pg daemon add`, the state directory (`<base>/daemon/<name>/`) must be
  readable and writable by that user.

See [Auto-start on Boot](/docs/autostart/) for the systemd/launchd service
details.

## Webhook receiver example

A complete, minimal FastAPI receiver that appends Alertmanager-style payloads
to a log file:

```bash
mkdir -p /opt/webhook
python3 -m venv /opt/webhook/.venv
/opt/webhook/.venv/bin/pip install fastapi uvicorn
```

`/opt/webhook/app.py`:

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

Register and run:

```bash
pg daemon add webhook --dir /opt/webhook --log-max-size 10M --log-keep 7 -- \
  /opt/webhook/.venv/bin/uvicorn app:app --host 0.0.0.0 --port 18801

pg daemon start webhook
pg daemon status webhook
```

Send a test alert:

```bash
curl -sS -X POST http://127.0.0.1:18801/alertmanager \
  -H 'Content-Type: application/json' \
  -d '{"status":"firing","alerts":[{"alertname":"TestAlert"}]}'
```

Expected response:

```json
{"status":"ok"}
```

Check both the receiver log and the application payload log:

```bash
pg daemon logs webhook -n 50
tail -n 1 /opt/webhook/alerts.log
```

## Troubleshooting

**`daemon ... exited immediately`** — `pg daemon start` already prints the log
tail. Run `pg daemon logs <name> -n 100` for more context. Common causes are a
missing Python dependency, a wrong `--dir`, or a port that is already in use.

**`command not found in PATH`** — the bare command name was not visible to the
shell that ran `pg daemon add`. Use an absolute path or install the binary
where the current environment can see it, then re-add with `--force`.

**The daemon starts but cannot see my shell variables** — at boot the process
runs under the systemd user service, which has a different environment. Export
configuration in the command's script rather than in `~/.bashrc`.

**`daemon "x" is running; stop it before --force`** — stop it with
`pg daemon stop x`, then re-add.

## Limits

- Linux only.
- One process per daemon entry; no supervision, backoff or restart policy.
- Logs are appended forever unless rotation is configured; use
  `--log-max-size` and/or `--log-rotate-daily` for long-running receivers.
- Stopping is process-group based. A daemon that deliberately escapes its
  process group (for example with its own `setsid`) may leave children behind.
- Registration captures the command line, not a shell. Shell syntax such as
  pipes or `&&` must be executed explicitly with `/bin/sh -c '...'`.
