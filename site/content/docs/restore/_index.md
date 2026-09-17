---
title: "Restore"
description: "Point-in-Time Recovery (PITR) guide for pgcli"
weight: 30
icon: fa-solid fa-clock-rotate-left
menus:
  main:
    identifier: docs-restore
    parent: docs
    weight: 30
    params:
      icon: fa-solid fa-clock-rotate-left
cascade:
  type: docs
  footer_style: slim
---


## Point-in-Time Recovery (PITR)

Restore to any point in time after the first backup.

```bash
# Restore (read-only, inspect before committing)
pg restore --time "2026-08-26 15:30:00+00"

# Preview what would be restored without executing (dry run)
pg restore --time "2026-08-26 15:30:00+00" --dry-run

# Stream restore container logs during recovery
pg restore --time "2026-08-26 15:30:00+00" --tail-logs

# Try different time if needed
pg restore --time "2026-08-26 15:25:00+00"

# Promote to read-write (switches timeline)
pg restore --time "2026-08-26 15:30:00+00" --promote

# Skip confirmation
pg restore --time "2026-08-26 15:30:00+00" --promote --force
```

**Time formats:**
- `2026-08-26 15:30:00+08:00` — with timezone offset
- `2026-08-26 15:30:00+08` — timezone hour only
- `2026-08-26 15:30:00Z` — UTC
- `2026-08-26 15:30:00` — assumed UTC

**Recovery workflow:** Stop → Restore → Start → WAL replay to target time

**Note:** After `--promote`, create a new full snapshot before further PITR.

## Patroni cluster

A Patroni HA cluster restores with `pg ha restore`, the cluster-side twin of
the single-instance command above. It takes the same `--time` (all formats), and
supports `--dry-run`, `--tail-logs`, and `--force`.

```bash
# Preview the recovery plan without touching the cluster
pg ha restore app --time "2026-08-26 15:30:00+00" --dry-run

# Restore, streaming the bootstrap member's recovery logs
pg ha restore app --time "2026-08-26 15:30:00+00" --tail-logs

# Choose which local member carries the bootstrap (default: first local member)
pg ha restore app --time "2026-08-26 15:30:00+00" --member node1

# Skip the confirmation prompt
pg ha restore app --time "2026-08-26 15:30:00+00" --force
```

**How it works:** the cluster's DCS identity is removed, then one **local**
member re-bootstraps its (emptied) data directory from the pgBackRest repo at
the target time via Patroni's custom-bootstrap method. That member comes up on a
**new timeline** and promotes itself to the writable leader; the other members —
this host's and cross-host ones alike — rejoin it through the DCS. This needs a
stanza with WAL archiving, provisioned by `pg backup setup` with an S3 repo (see
the [HA backup addon](../addon/ha-backup)).

**Differences from single-instance restore:**

- **It always promotes.** There is no read-only "pause, inspect, retry another
  time" two-step — the cluster comes up writable at the target. Use `--dry-run`
  to confirm the target before executing.
- **Leader-locality pre-check.** Before touching anything, `pg ha restore` reads the
  current leader from the DCS and checks whether it is one of this host's local
  members. On a real run it **refuses** if the leader is not local (a remote
  member, or a cross-host one absent from this host's view); `--dry-run` only
  warns. The reason: the destructive path clears the DCS identity and re-bootstraps
  a local member, so a leader this host cannot stop would race the bootstrap to
  re-claim the DCS on the **old timeline**. Move leadership local first
  (`pg ha switchover`/`failover`) or run on the leader's host. An empty/unreadable
  leader does not block the run (it covers the first bootstrap).
- **No `--promote` flag** — promotion is automatic.

**Recovery workflow:** Pre-flight (leader must be a local member) → Stop local
members → remove DCS → wipe + bootstrap one member from the repo → it promotes to
leader on a new timeline → remaining members rejoin as replicas.

**After the restore:** take a fresh full snapshot to re-baseline the new
timeline — `pg ha snapshot create app --type full`. Because the data directory
was rebuilt, the system-id changes, so the first snapshot may fail with
`[051] system-id ... do not match stanza`; fix it non-destructively with
`pg backup stanza-upgrade pgcli_app-<ns>` (see the Backup doc). If a replica
does not rejoin on its own, rebuild it with
`pg ha ctl app -- reinit app-<ns> <member> --force`.
