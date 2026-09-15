---
title: "Extensions in HA Clusters"
description: "Install and manage PostgreSQL extensions in Patroni-managed HA clusters"
weight: 48
---

Installing PostgreSQL extensions in a Patroni-managed HA cluster is
fundamentally different from a single-node instance. This page explains why,
and walks through the `pg ha extension` command subtree that handles it.

## Why not `pg extension install`?

The single-node flow (`pg extension install <instance> <ext>`) works by:

1. Building a `-ext` derived image with Pigsty packages
2. Stopping and recreating the container from the new image
3. Editing `postgresql.conf` to set `shared_preload_libraries`
4. Running `CREATE EXTENSION` inside the container

In a Patroni cluster, **steps 2-4 all break**:

- **Patroni is PID 1.** Recreating a container takes that node offline. If it's
  the leader, Patroni triggers a failover — the cluster reshuffles while
  you're mid-install.
- **Patroni regenerates `postgresql.conf` every loop** from the DCS. Any
  direct edit to the file is overwritten within seconds. `shared_preload_libraries`
  must be set via `patronictl edit-config` (which writes to the DCS).
- **`CREATE EXTENSION` must run on the leader**, which may be on a remote host
  — not inside any local container.

`pg ha extension` orchestrates all of this correctly.

## How It Works

The install flow follows a specific order to avoid the pitfalls above:

```
1. Validate extension names (IsExtensionKnown)
2. Build -ext image with Pigsty packages
3. patronictl pause <scope> --wait            ← disable auto-failover
4. Recreate each member container (one at a time, wait for rejoin)
5. patronictl resume <scope> --wait           ← ⚠ must come BEFORE edit-config
6. patronictl edit-config (set shared_preload_libraries)
   → Patroni triggers a rolling restart (replicas first, leader last)
7. Wait for rolling restart to complete
8. CREATE EXTENSION on leader
9. Save extensions list to pg.yaml
```

The critical ordering is **resume before edit-config**: a paused Patroni cluster
does not apply `edit-config` changes. If you edit-config while paused, the
`shared_preload_libraries` update is silently lost.

### Builtin-only fast path

If all requested extensions are builtin (contrib extensions like `hstore`,
`uuid-ossp` that ship with PostgreSQL), steps 2-4 are skipped entirely — no
image build, no container recreate, no pause/resume. The flow goes straight to
`edit-config` + `CREATE EXTENSION`.

## Commands

### Install

```bash
pg ha extension install <scope> <extension>[,<extension>...] [flags]
```

Builds the `-ext` image, recreates all member containers (coordinated via
pause/resume), sets `shared_preload_libraries` via `patronictl edit-config`,
and runs `CREATE EXTENSION` on the leader.

Extensions are passed as a comma-separated list:

```bash
# Install a single extension
pg ha extension install app pg_stat_statements

# Install multiple extensions to a specific database
pg ha extension install app pg_cron,pg_stat_statements --database mydb

# Skip the rolling-restart confirmation prompt
pg ha extension install app pgvector --auto-restart
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--database` | `postgres` | Target database for `CREATE EXTENSION` |
| `--auto-restart` | `false` | Skip confirmation for the rolling restart triggered by `edit-config` |

### Remove

```bash
pg ha extension remove <scope> <extension>[,<extension>...] [flags]
```

Runs `DROP EXTENSION` on the leader, then updates `shared_preload_libraries`
via `patronictl edit-config` (triggers a rolling restart). Does **not** rebuild
the image or recreate containers — the `-ext` image only grows; disk reclamation
is rare and manual.

Extensions are passed as a comma-separated list:

```bash
pg ha extension remove app pg_cron
pg ha extension remove app pg_stat_statements,pg_cron --auto-restart
```

### List

```bash
pg ha extension list <scope>
```

Shows three views of the cluster's extensions:

- **Config (pg.yaml):** the `extensions` list stored in `pg.yaml`
- **DCS (preload):** the `shared_preload_libraries` value from `patronictl show-config`
- **Leader (installed):** actual extensions from `pg_extension` on the leader

```bash
$ pg ha extension list app
Cluster "app" extensions:

  Config (pg.yaml):  [pg_stat_statements pg_cron]
  DCS (preload):     shared_preload_libraries: pg_stat_statements,pg_cron
  Leader (installed): [pg_cron pg_stat_statements]
```

### Apply

```bash
pg ha extension apply <scope> [flags]
```

Manually triggers the second half of the install flow: resume the cluster,
run `patronictl edit-config`, and execute `CREATE EXTENSION` on the leader.

Used in **cross-host** clusters — after running `install` on each host (each
builds the image and recreates its local members), run `apply` once on any host
to complete the DCS update and extension creation.

```bash
pg ha extension apply app
pg ha extension apply app --database mydb --auto-restart
```

## Cross-Host Workflow

In a cross-host cluster, each host manages only its own members. The extension
installation workflow splits into two phases:

**Phase 1 — per host:** Run `pg ha extension install` on each host. Each host:
- Builds the `-ext` image locally
- Pauses the cluster
- Recreates its own local members from the new image
- Resumes the cluster

```bash
# host A
pg ha extension install app pg_stat_statements,pg_cron

# host B
pg ha extension install app pg_stat_statements,pg_cron
```

After the first host's `install`, the command detects that not all members are
local and prints instructions for the remaining hosts.

**Phase 2 — once, on any host:** Run `pg ha extension apply` to:
- Update `shared_preload_libraries` via `patronictl edit-config`
- Wait for the rolling restart
- Run `CREATE EXTENSION` on the leader

```bash
pg ha extension apply app --auto-restart
```

For single-host clusters (all members local), `install` automatically runs
the `apply` step — no separate command needed.

## The `extensions` Config Field

Extensions are tracked at the cluster level in `pg.yaml`, under the Patroni
cluster config:

```yaml
addons:
  patroni:
    app:
      name: app
      extensions:
        - pg_stat_statements
        - pg_cron
      passwords:
        superuser: ...
      members:
        node1: { ... }
        node2: { ... }
```

This list is the source of truth for what `pg ha extension list` reports and
what `apply` installs. Both `install` and `remove` update it automatically.

## `shared_preload_libraries` Ordering

Some extensions must appear at position 0 in `shared_preload_libraries`
(PostgreSQL fatals if they're not first). pgcli tracks this as a catalog
attribute (`PreloadFirst`) — currently set on:

- **`citus`** — distributed PostgreSQL, must be loaded before anything else
- **`timescaledb`** — time-series engine, same constraint

Additional extensions may require companion DCS parameters:

- **`pg_cron`** needs `cron.database_name`

`pg ha extension` handles all of this automatically:

- Extensions with `PreloadFirst` are always placed at the start of the CSV,
  regardless of input order
- When `pg_cron` is present, `cron.database_name` is set to the `--database`
  value (default `postgres`)
- When `pg_cron` is removed, `cron.database_name` is cleared

## Examples

### Install pg_stat_statements and pg_cron

```bash
pg ha extension install app pg_stat_statements,pg_cron --database mydb --auto-restart
```

This builds an `-ext` image, recreates all members (with pause/resume), sets
`shared_preload_libraries=pg_stat_statements,pg_cron` and
`cron.database_name=mydb` in the DCS, then creates both extensions in `mydb`.

### Install Citus (must be first in preload)

```bash
pg ha extension install app citus pg_stat_statements
# or equivalently:
pg ha extension install app citus,pg_stat_statements
```

Even though `citus` is listed second, the preload CSV is generated as
`citus,pg_stat_statements` — extensions with `PreloadFirst` are always
placed at position 0.

### Remove an extension

```bash
pg ha extension remove app pg_cron
```

Drops `pg_cron` from the leader, removes it from `shared_preload_libraries`,
and clears `cron.database_name`. Triggers a rolling restart.

### Check what's installed

```bash
pg ha extension list app
```

## Limitations

- **Image rebuild is additive.** Removing an extension does not rebuild the
  `-ext` image or shrink it. To reclaim disk, manually prune old images with
  `podman image prune`.
- **No per-member extension list.** Extensions are cluster-wide — all members
  share the same `-ext` image and the same `shared_preload_libraries`.
- **`CREATE EXTENSION` targets one database.** PostgreSQL extensions are
  per-database. To install in multiple databases, re-run with `--database`
  pointing at each one.
- **Cross-host requires manual coordination.** Each host must run `install`
  before `apply` is run once. pgcli does not SSH into remote hosts.

## See Also

- [Patroni HA](./ha/) — cluster setup, commands, and architecture
- [Extensions](/docs/extensions/) — single-node extension management and the Pigsty catalog
- [Patroni Dynamic Configuration](./ha-dynamic/) — DCS parameters reference
