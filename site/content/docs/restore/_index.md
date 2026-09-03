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
