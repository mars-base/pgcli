---
title: "Configuration Tuning"
description: "Hardware-aware postgresql.conf tuning for pgcli instances and Patroni clusters"
weight: 86
icon: fa-solid fa-sliders
menus:
  main:
    identifier: docs-tune
    parent: docs
    weight: 86
    params:
      icon: fa-solid fa-sliders
cascade:
  type: docs
  footer_style: slim
---

`pg tune` turns a machine's hardware specs and a workload class into a coherent set
of `postgresql.conf` parameters. It works against both kinds of deployment pgcli
manages: plain instances (written through the managed tuning block in
`postgresql.conf`) and Patroni HA clusters (written to the DCS via `edit-config`,
which rolls the change out to every member).

By default it **only prints a recommendation** — nothing is touched. `--apply`
writes it; `--revert` removes a previously applied tune.

## When to use it

- **A new instance after install** — the container defaults are safe, not fast. One
  `pg tune -i <name> --apply` gets the memory partition, WAL sizing and planner
  cost constants into the right ballpark for the hardware.
- **After an up/down-size** — RAM and CPU counts changed; re-run the same command
  and the derived values follow.
- **When `pg inspect` flags settings** — the inspection report can point at
  undersized `shared_buffers` / low cache-hit ratios; `pg tune` is the fix-it path.

## Inputs

The specs default to a probe of the host the command runs on (total RAM from
`/proc/meminfo` on Linux, CPU count from the runtime). Overrides:

| Flag | Meaning |
|------|---------|
| `--type` | workload class: `web`, `oltp`, `dw`, `desktop`, `mixed` (default) |
| `--hd` | storage class: `ssd` (default), `hdd`, `san`, `nvme` |
| `--ram` | total memory, e.g. `16G` — use on shared hosts where the probe over- or under-reports |
| `--cpus` | CPU count — mandatory for cross-host Patroni clusters; compute against the smallest member |
| `--conns` | max_connections override (default: workload preset 200/300/40/20/100) |
| `--db-size` | database size vs RAM, e.g. `200G` (`<RAM` → fits, `≤4×RAM` → mid, else larger) |

Workload choice in one line: mostly short queries from an app → `web`/`oltp`;
analytics and big scans → `dw`; a single-user app co-located with the DB →
`desktop`; unsure → `mixed`.

The target server itself is probed for `server_version_num` and for every
parameter's current value, so the table shows CURRENT vs RECOMMENDED side by side
and version-gated parameters (`jit`, `io_method`, …) only appear where they exist.

## Applying

### Managed instances (`-i <name>`)

```bash
pg tune -i myinst                       # recommendation table only
pg tune -i myinst --apply               # write it into pgcli's managed block
pg tune -i myinst --revert              # remove the applied tune again
```

`--apply` records the parameters in the instance's config and writes them into
the managed tuning block of `postgresql.conf`. The block is re-applied on every
`pg start`, so the tuning **survives restarts and reinstalls of the container**.
Parameters set via `ALTER SYSTEM` (i.e. present in `postgresql.auto.conf`) are
skipped with a count — auto.conf always wins.

Changes to postmaster-level parameters (`shared_buffers`, `wal_buffers`,
`max_connections`, …) need a restart: interactively you're asked y/N;
non-interactively pgcli only prints the `pg stop && pg start` hint and never
bounces the database on its own. Everything else is applied with a config
reload immediately.

### Patroni clusters (`--ha <scope>`)

```bash
pg tune --ha app                        # read specs from the leader, print table
pg tune --ha app --apply --auto-restart # write to the DCS; Patroni rolls it out
pg tune --ha app --revert --auto-restart
```

Here the parameters go into the cluster DCS config via
`patronictl edit-config` — the only sanctioned way to change postgresql.conf
values in a Patroni world. Patroni then applies them on every member, **rolling
restarting** for postmaster-level changes. Because that is disruptive, both
`--apply` and `--revert` ask for confirmation first; `--auto-restart` skips the
prompt (for scripts). If you decline, nothing is written to the DCS.

The recommendation table is computed from the host pgcli runs on — on a
cross-host cluster the members may differ, so pass `--ram/--cpus` matching the
smallest member.

## Validation and `--apply-conf`

```bash
pg tune -i myinst --apply-conf /tmp/tune.conf              # save the recommendation…
$EDITOR /tmp/tune.conf                                     # …hand-edit it…
pg tune -i myinst --apply --apply-conf /tmp/tune.conf      # …apply the file instead
```

`--apply-conf` names a `key = value` file (postgresql.conf style: `#` comments,
quoted values, units allowed). Without `--apply` the file is *written* (the
header line records the inputs so the result is reproducible); with `--apply` the
file is *read* and becomes the parameter source.

**Every apply path validates first.** Each pair is checked against the target's
`pg_settings`: unknown names (e.g. `io_method` on PG 15), non-boolean bools,
values outside an enum's set, and integers/reals out of `[min_val, max_val]` are
listed per-parameter and the apply aborts wholesale — a partial write is the
worst outcome for a tuning batch.

## `--dsn`: recommendations only

```bash
pg tune --dsn postgres://user:pass@host:5432/db
```

Remote targets are probe-and-suggest only; `--apply/--revert` are refused, since
writing a stranger's server from a tuning heuristic is a decision pgcli will not
make for you.

## `--web-tune`: the interactive site

```bash
pg tune --web-tune                  # start server on http://127.0.0.1:10864
pg tune --web-tune --listen :8080   # custom port
pg tune --web-tune --open           # start server and open browser
```

Starts an HTTP server that serves the interactive tuning calculator. The page
has a form for every input (version, OS, workload, RAM+unit, CPUs, connections,
storage, db size), live recompute, and postgresql.conf / ALTER SYSTEM output with
a copy button. No network access, no dependencies; the host's RAM/CPU/OS prefill
the form. The embedded calculator is a JS port of the Go algorithm; the page
ships Go-computed golden fixtures and self-checks them on load, showing a red
banner if the two implementations ever disagree.

Press Ctrl+C (or send SIGTERM) to stop the server gracefully.

## How the numbers are derived

The formulas are faithful and deterministic (internal unit kB, flooring at each
derivation step):

- `shared_buffers` = RAM/4 (desktop: /16); `effective_cache_size` = 3/4 RAM.
- `maintenance_work_mem` = RAM/16 (dw: /8), capped at 8 GB (2047 MB on Windows ≤PG17).
- `work_mem` = (RAM − shared_buffers) / ((max_connections + max_worker_processes) × 3),
  then scaled by workload (dw/mixed ½, desktop ⅙) and db-size (fits ×1.3, larger ×0.9), floor 4 MB.
- `wal_buffers` = 3% of shared_buffers, clamped to [32 kB, 16 MB].
- `random_page_cost` 4 for hdd and for dw-on-SSD (with a warning about
  catastrophic index scans), 1.1 otherwise; `effective_io_concurrency` by storage class (Linux only).
- Parallelism from CPU count (omitted below 4 cores); `autovacuum_max_workers` at ≥16/≥32 cores;
  version gates: `jit=off` ≥12, `wal_compression` on/lz4 ≥10/≥15, `io_method`/`io_workers` ≥18
  (`io_method` is always `worker` — `io_uring` additionally needs a `--with-liburing` build
  and kernel support, and a mismatch makes the server fail to start, so it is not auto-recommended),
  desktop-only `wal_level=minimal` + `max_wal_senders=0`.

Per-parameter caveats (lz4 compile flag, the DW cost warning) are
printed inline in the table; absurd machine sizes (<256 MB, >100 GB RAM) get a
global warning.
