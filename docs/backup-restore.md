# Backup and Restore

## Snapshots

```bash
# Create snapshot (full backup)
pg snapshot create -i proj01

# Create differential backup (recommended)
pg snapshot create --type diff -i proj01

# List snapshots
pg snapshot list -i proj01

# Delete snapshot
pg snapshot delete 20260826-073712F -i proj01
```

**Snapshot types:**
- `full` — Complete backup (default, self-contained)
- `diff` — Changes since last full backup
- `incr` — Changes since last backup

## Point-in-Time Recovery (PITR)

Restore to any point in time after the first backup.

```bash
# Restore (read-only, inspect before committing)
pg restore --time "2026-08-26 15:30:00+00"

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
