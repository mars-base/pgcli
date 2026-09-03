---
title: "Backup"
description: "Backup guide for pgcli"
weight: 20
icon: fa-solid fa-shield-halved
menus:
  main:
    identifier: docs-backup
    parent: docs
    weight: 20
    params:
      icon: fa-solid fa-shield-halved
cascade:
  type: docs
  footer_style: slim
---


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
