---
title: "Health Inspection"
description: "PostgreSQL health inspection report guide for pgcli"
weight: 85
icon: fa-solid fa-stethoscope
menus:
  main:
    identifier: docs-inspect
    parent: docs
    weight: 85
    params:
      icon: fa-solid fa-stethoscope
cascade:
  type: docs
  footer_style: slim
---


`pg inspect` samples the standard PostgreSQL statistics views once and produces a deterministic health report: a 0-100 score plus findings graded CRITICAL / WARNING / NOTE / GOOD, with raw tables for queries, indexes, vacuum/tables, settings and sessions. The design is adapted from the open-source [pgbot](https://github.com/pgrundev/pgbot) project (Apache-2.0).

Everything is read-only — each statement runs inside a `READ ONLY` transaction. A monitoring role with access to the stats views (`pg_monitor`) is enough.

## Basic usage

```bash
# Inspect a local instance (summary + health score)
pg inspect -i myinst

# Also inspect a remote database via a connection string
pg inspect --dsn postgres://user:pass@host:5432/db

# Print the raw section tables, not just the summary
pg inspect -i myinst --full

# Emit the whole report as JSON (for scripts / agents)
pg inspect -i myinst --json | jq '.findings | length'

# Write a self-contained HTML report and (only with --open) launch a browser
pg inspect -i myinst --html /tmp/report.html
pg inspect -i myinst --html /tmp/report.html --open
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dsn` | — | Inspect a remote database instead of a local instance |
| `--full` | off | Print the raw section tables in the terminal |
| `--json` | off | Emit the whole report as JSON (no terminal summary) |
| `--html <path>` | — | Write a self-contained HTML report (no external requests) |
| `--open` | off | After `--html`, try to open the report in a browser (needs a GUI) |
| `--interval` | `1s` | Gap between the two counter samples used for rates |
| `--top` | `20` | Number of `pg_stat_statements` rows to consider |

`--open` requires `--html`; without a report file there is nothing to open.

## What gets checked

- **Health score** — starts at 100; each CRITICAL costs 10, WARNING 3, NOTE 1.
- **Wraparound** — transaction-ID / multixact-ID age against the freeze wall.
- **Connections** — saturation against `max_connections`.
- **Cache hit ratio** — buffer-cache hits vs reads over the sample window.
- **Rollback ratio & deadlocks** — from double-sampled `pg_stat_database`.
- **Unused indexes** — zero scans, over 1 MiB, excluding primary keys, constraint-backing, unique, partial, expression and replica-identity indexes (with caveats instead).
- **Bloat / seq scans / hot updates** — dead-tuple ratio, sequential-scan dominance, non-HOT update load per table.
- **Vacuum & analyze hygiene** — never-vacuumed/analyzed tables, autovacuum disabled per table.
- **Sessions** — long transactions, idle-in-transaction, from `pg_stat_activity`.
- **Settings** — dangerous tuning values (e.g. `fsync = off`) and overrides off compiled-in defaults.

Every section degrades independently: a database where `pg_stat_statements` is absent still gets a full report, with one NOTE explaining the empty queries section. To enable it, install the extension properly (the package plus `shared_preload_libraries`, with an automatic restart):

```bash
pg extension install pg_stat_statements -i myinst
```

## HTML report

The `--html` file is fully self-contained: inline CSS, escaped content, no JavaScript and no external requests — safe to copy anywhere and open offline. Findings are grouped by severity with evidence lines, remediation hints and caveats; each section renders as a sortable table.

## JSON contract

`--json` emits the report as snake_case keys: `server`, `version`, `target`, `interval_s`, `health`, `limits`, `rates`, `queries`, `indexes`, `tables`, `settings`, `activity`, and `findings` (each with `id`, `severity`, `title`, `detail`, `remediation`, `evidence`, `impact_score`, `caveats`).

## Security notes

- All diagnostics run in `BEGIN TRANSACTION READ ONLY` — a write can never succeed.
- `--dsn` targets are redacted in every output (host:port/db only, never the password).
- The queries section reads `pg_stat_statements`, which may expose query shapes from all databases on the instance — run inspect with a role you trust to see that, or leave the extension out.

## Credits

The collection → findings → report pipeline, the rule thresholds and the report structure are adapted from [pgbot](https://github.com/pgrundev/pgbot) (Apache-2.0), reimplemented here without its runtime dependencies.
