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


`pg inspect` answers one question in a single command: **is this database healthy, and what should I fix first?** It samples the standard PostgreSQL statistics views once and produces a deterministic report — a 0-100 health score plus findings graded CRITICAL / WARNING / NOTE / GOOD, each with evidence, a remediation hint, and the caveats that keep a confident recommendation from being a confidently wrong one.

It is strictly read-only: every statement runs inside a `READ ONLY` transaction, so it never writes, never takes locks beyond a plain read, and is safe to run against production at any time.

## When to use it

- **Weekly / on-call checkups** — run it against every instance and read only what scores below 100; the report tells you whether anything regressed without staring at dashboards.
- **"The database feels slow"** — before diving into EXPLAIN plans, get the 30-second overview: cache-hit ratio, top statements by total time (with `pg_stat_statements`), bloat, and session pileups.
- **After installs and upgrades** — confirm nothing dangerous slipped into the settings (e.g. `fsync = off`) and autovacuum is actually keeping up.
- **Before capacity decisions** — see which indexes are never scanned (and how much space dropping them would reclaim) and whether connections are drifting toward `max_connections`.
- **Audit and handoff** — `--html` writes one self-contained file you can mail or archive; `--json` feeds scripts and monitoring agents.

## Problems it catches

| Problem | What you'd otherwise miss |
|---------|---------------------------|
| Transaction-ID wraparound approaching the freeze wall | A forced-downtime vacuum emergency |
| Connection saturation near `max_connections` | Failed logins under load |
| Low cache-hit ratio | Undersized `shared_buffers` / `effective_cache_size` |
| Unused indexes over 1 MiB | Wasted space and write amplification |
| Table bloat, never-vacuumed or autovacuum-disabled tables | Slowly degrading scans and vacuum storms |
| Heavy sequential scans / non-HOT updates on big tables | Missing indexes and hot update patterns |
| Long-running and idle-in-transaction sessions | Blocked cleanup and lock waits |
| Dangerous setting overrides off compiled-in defaults | Silent durability or tuning mistakes |

Every check degrades independently — a database missing `pg_stat_statements` still gets a full report, with one note explaining the empty section.

## Usage

```bash
# Local instance: terminal summary with the health score
pg inspect -i myinst

# Remote database (passwords are never shown in output)
pg inspect --dsn postgres://user:pass@host:5432/db

# The raw section tables: queries, indexes, vacuum/tables, settings, sessions
pg inspect -i myinst --full

# Self-contained HTML report, optionally opened in a browser
pg inspect -i myinst --html /tmp/report.html --open
```
