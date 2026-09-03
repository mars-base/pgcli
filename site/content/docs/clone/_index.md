---
title: "Clone"
description: "Clone guide for pgcli"
weight: 70
icon: fa-solid fa-clone
menus:
  main:
    identifier: docs-clone
    parent: docs
    weight: 70
    params:
      icon: fa-solid fa-clone
cascade:
  type: docs
  footer_style: slim
---


Create a new instance whose data is copied from an existing one, streamed directly — no temp file on disk.

```bash
# Clone the default instance
pg clone test02

# Clone a specific instance
pg clone test02 -i proj01

# Clone a remote database via connection string
pg clone test02 --dsn postgres://user:pass@host:5432/db

# Custom data directory for the new instance
pg clone test02 -i proj01 --base-dir /data/pg
```

## What happens

1. **Pre-flight check** — the source is verified *before* anything is created:
   - Local instance: container must be running
   - `--dsn`: an authenticated `SELECT 1` must succeed (catches wrong password, unreachable host)
2. **Create** — a new instance entry is added to the config with a random password, its own container name, data directory and auto-assigned port
3. **Start** — the new instance is started (same workflow as `pg start`)
4. **Stream** — source data is piped to the target with live transfer progress shown once per second

## Notes

- The source instance must be running (or the `--dsn` target reachable); a bad source fails immediately with no side effects
- The new instance name must not already exist in config
- `--dsn` and `--instance` are mutually exclusive: with `--dsn` the connection string determines host, port and database
- The new instance gets a fresh random password — find it in the clone output or `pg status -i <name>`
- Logical copy only (schema + data); for large databases a physical approach may be faster
