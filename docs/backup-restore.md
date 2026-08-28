# Backup and Restore

## Snapshots

```bash
# Create snapshot (full backup)
pg snapshot create -i proj01

# Create differential backup (recommended)
pg snapshot create --type diff -i proj01

# Stream backup container logs during snapshot
pg snapshot create --tail-logs -i proj01

# List snapshots
pg snapshot list -i proj01

# Limit the number of snapshots displayed
pg snapshot list --limit 5 -i proj01

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

## Shared Backup Container

All instances share a single pgbackrest container; each instance gets its own stanza in the repository.

```bash
# Initialize the shared pgbackrest container (build image, create dirs, generate config)
pg backup setup

# Use a custom base directory for backup data and logs
pg backup setup --base-dir /mnt/backup

# Start / stop the backup container
pg backup start
pg backup stop

# Show backup container status
pg backup status
```

Backup infrastructure (network, image, directories, config, container) is prepared automatically on `pg start`; run `pg backup setup` manually to reinitialize, e.g. after changing the base directory.

