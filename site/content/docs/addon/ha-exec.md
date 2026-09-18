---
title: "HA Exec / psql"
description: "Run SQL against a Patroni HA cluster with pg ha exec and pg ha psql: zero-config leader resolution from the DCS, no hand-assembled dsn, no entering a member container, and read-only or cross-host targets via --member"
weight: 52
---

`pg ha exec` and `pg ha psql` are the direct way to run SQL against a Patroni
cluster. They are the cluster-side twins of the single-instance
[`pg exec`](../../exec-psql/) and [`pg psql`](../../exec-psql/): you name
a scope, they resolve the target and connect. No `--dsn` to hand-assemble, no
member container to `podman exec` into, and — because the connection is plain TCP
— a leader or replica on **another host** is just as reachable as a local one.

## Why not `pg exec --dsn` or `podman exec`

Before these commands, ad-hoc SQL on a cluster meant one of two awkward paths:

- **`pg exec --dsn postgres://postgres:<pass>@<leader>:<port>/db`** — correct, but
  you assemble it by hand: read the leader's `connect_address` out of
  `pg ha status`, copy the superuser password out of the config, plug in the port.
  A fixed dsn also goes stale the moment the leader moves.
- **`podman exec -it <member> psql ...`** — reaches only a *local* member, so a
  leader on another host is out of reach; and you are inside a container the tool
  is meant to abstract away.

`pg ha exec`/`psql` fold both away: pgcli resolves the leader from the cluster's
own state, authenticates with the stored password, and runs psql in a short-lived
container. You never see it.

## How the target is resolved

The **DCS roster** (`patronictl list -f json`) is the only membership view that
spans the whole cluster. Each host's `pg.yaml` registers *only its own* members,
so this host's config cannot even see a node running elsewhere — but the DCS
knows every member and its advertised `connect_address`. Both commands go through
it:

- **Default (no `--member`)** → the current **leader**. If leadership has moved
  since you last looked, you still hit the right node — there is no stale dsn.
- **`--member <name>`** → that named member, wherever it lives. Aim it at a
  replica for a **read-only** query (`SELECT` off a standby, `pg_is_in_recovery()`
  returns `t`), or at a specific remote node.

Authentication is the cluster's superuser over scram; `pg_hba`
(`host all all all scram-sha-256`) accepts any source, so a remote member is
reachable across hosts exactly the way replication traffic already is.

## Usage

```bash
# One-shot SQL against the leader
pg ha exec app "SELECT version()"
pg ha exec app "SELECT count(*) FROM pg_stat_activity"

# A different database on the leader
pg ha exec app --database mydb "SELECT * FROM t LIMIT 5"

# Read-only: target a replica instead of the leader
pg ha exec app --member node2 "SELECT pg_is_in_recovery()"

# Interactive psql against the leader
pg ha psql app

# Interactive against a specific (even cross-host) member
pg ha psql app --member node3

# Pass extra psql arguments after --
pg ha psql app -- -c "SELECT 1"
pg ha psql app --database mydb -- -x
```

`pg ha exec` takes the SQL as trailing words (so you normally quote one string);
`pg ha psql` opens an interactive shell and forwards anything after `--` straight
to psql. `--database` selects the target database on both (default `postgres`).
There is no `--user` — the managed superuser is the only role pgcli holds
credentials for, so that is what connects.

### Interactive vs. scripted

`pg ha psql` allocates a TTY when your stdin is one; when it is not (piping a
script, running under CI) it turns the pager off so the session terminates
instead of blocking in `less`. That makes both of these behave as expected:

```bash
pg ha psql app < migrations.sql      # pipe a file, no pager, exits when done
printf '\conninfo\n' | pg ha psql app   # scripted one-liner, output streams back
```

## Reading the output

`pg ha exec` streams psql's own formatting — column headers, alignment, row
counts, and errors go straight to your terminal, exactly as `psql -c` would print
them. It is not a machine-parseable dump; if you need one, pipe through your own
tooling or use `pg ha exec app "..." --csv`-style psql flags via `pg ha psql app
-- -c "..."`.

## Where these fit

For a **stable client endpoint** that survives failover independently of pgcli,
put [HAProxy](../haproxy/) in front of the cluster and connect through it.
`pg ha exec`/`psql` are for operators and scripts driving a cluster directly,
not a substitute for a pooler in front of an application.

The `pg ha status` / `pg ha ctl` verbs remain the way to inspect cluster state and
reach unwrapped `patronictl` commands; these two are specifically about running
SQL.
