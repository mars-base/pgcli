---
title: "exec and psql"
description: "exec and psql guide for pgcli"
weight: 90
icon: fa-solid fa-terminal
menus:
  main:
    identifier: docs-exec-psql
    parent: docs
    weight: 90
    params:
      icon: fa-solid fa-terminal
cascade:
  type: docs
  footer_style: slim
---


Two ways to run SQL against an instance: `pg exec` for one-shot SQL or container commands, `pg psql` for interactive sessions.

## pg exec

### SQL mode (default)

Arguments without `--` are executed as SQL via psql, using the instance's configured user and database.

```bash
pg exec "SELECT version()"
pg exec -i proj01 "SELECT count(*) FROM users"
pg exec "CREATE TABLE test (id serial PRIMARY KEY, msg text)"
```

### Container command mode (after --)

Arguments after `--` are run directly inside the container (as root).

```bash
pg exec -- pg_isready
pg exec -- ls -la /var/lib/postgresql/data
pg exec -- bash -c "cat /var/lib/postgresql/data/postgresql.conf"
pg exec -- tail -f /var/log/postgresql/postgresql-*.log
```

### Remote database (--dsn)

Execute SQL against any database reachable via a connection string, using a temporary container. `--dsn` only supports SQL mode; container commands require a local instance.

```bash
pg exec --dsn postgres://user:pass@host:5432/db "SELECT count(*) FROM users"
```

## pg psql

### Interactive session

```bash
pg psql                          # default instance
pg psql -i proj01                # specific instance
```

Inside the shell you get full psql features: SQL with history and tab completion, meta-commands (`\dt`, `\du`, `\l`), and `\q` to quit.

### Non-interactive (scripts)

```bash
echo "SELECT version();" | pg psql        # SQL from stdin
pg psql -- -c "SHOW work_mem"             # single command
pg psql -- -d other_db                    # connect to different database
pg psql -- -U other_user                  # connect as different user
```

### Switch to postgres superuser

Some administrative tasks (e.g., creating certain extensions, modifying system-level settings) require postgres superuser privileges. Use `--` to pass psql arguments and switch user:

```bash
pg psql -i pg01 -- -U postgres -d postgres
```

**Recommended approach**: Use the instance default user (admin) for daily operations, and switch to postgres only when superuser privileges are needed. This is safer and more convenient than modifying config files or restarting containers.

Example scenarios:
```bash
# Create an extension that requires superuser privileges
pg psql -i pg01 -- -U postgres -d postgres -c "CREATE EXTENSION pg_cron"

# Configure cron.database_name (pg_cron specific parameter)
pg psql -i pg01 -- -U postgres -d postgres -c "ALTER SYSTEM SET cron.database_name = 'pg01_db'"

# View system-level configuration
pg psql -i pg01 -- -U postgres -d postgres -c "SHOW shared_preload_libraries"
```

### Remote database (--dsn)

```bash
pg psql --dsn postgres://user:pass@host:5432/db
```

## Rules

- `--dsn` and `--instance` are mutually exclusive: the connection string determines host, port and database, so `-i` is rejected to avoid silent misuse.
- With `--dsn`, the database is the path part of the URL: `postgres://user:pass@host:5432/mydb` connects to `mydb`. To use another database, change the path.
- With a local instance, `--` passes raw psql arguments (including `-d`/`-U`), overriding the instance defaults.
