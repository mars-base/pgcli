---
title: "Destroy"
description: "Destroy an instance and remove its configuration"
weight: 95
icon: fa-solid fa-trash
menus:
  main:
    identifier: docs-destroy
    parent: docs
    weight: 95
    params:
      icon: fa-solid fa-trash
---

Destroy stops and removes the container, then removes the instance from the configuration file. By default, host data directories are preserved. Use `--clean-data` to also remove data, WAL archives, and the pgBackRest repository stanza.

## Basic Usage

```bash
# Destroy instance (keeps data directory)
pg destroy -i proj01

# Destroy without confirmation prompt
pg destroy -i proj01 --force

# Destroy with data cleanup (fresh start)
pg destroy -i proj01 --clean-data

# Skip confirmation and clean all data
pg destroy -i proj01 --clean-data --force
```

## What Happens

When you run `pg destroy`:

1. **Stop container** — PostgreSQL is gracefully shut down
2. **Remove container** — The Podman container is deleted
3. **Remove configuration** — The instance entry is removed from `~/.pgcli/pg.yaml`
4. **Preserve data** (by default) — Host data directory at `--base-dir/<instance>` is kept

With `--clean-data`:

1. All of the above, plus:
2. **Remove host data** — The data directory is deleted
3. **Remove WAL archives** — Any WAL files on the host are removed
4. **Remove backup stanza** — The pgBackRest repository stanza for this instance is removed

## Recreating an Instance

After destroying, you can recreate the instance with a fresh start:

```bash
# Destroy and clean all data
pg destroy -i proj01 --clean-data

# Recreate with the same name
pg create -i proj01 --base-dir /data/pg

# Start the new instance
pg start -i proj01
```

**Important:** Without `--clean-data`, the old data directory is preserved. When you recreate the instance, PostgreSQL will use the existing data, and `init.sh` (which creates users and sets the admin password) does **not** run again. This can cause issues if:

- The data was created with a different user or password
- You changed the default user in configuration
- You want a completely fresh start

Use `--clean-data` when you need a clean slate.

## Confirmation Prompt

By default, `pg destroy` asks for confirmation before proceeding:

```bash
$ pg destroy -i proj01
!  This will destroy instance "proj01":
   - Container: pgcli-pg-default-proj01
   - Data dir: /data/pg/proj01 (preserved)

Continue? [y/N]:
```

Use `--force` to skip the prompt:

```bash
pg destroy -i proj01 --force
```

## Use Cases

### 1. Clean Restart After Configuration Change

If you changed the default user, password, or PostgreSQL version in the config, destroy and recreate:

```bash
# Update configuration
# Edit ~/.pgcli/pg.yaml to change postgres.user or image_tag

# Destroy with data cleanup
pg destroy -i proj01 --clean-data --force

# Recreate with new settings
pg create -i proj01 --base-dir /data/pg
pg start -i proj01
```

### 2. Remove Test Instance

After testing, remove an instance you no longer need:

```bash
pg destroy -i test-instance --force
```

### 3. Troubleshooting Corrupted State

If an instance is in a bad state (e.g., failed to start, corrupted data), destroy and recreate:

```bash
pg destroy -i broken-instance --clean-data --force
pg create -i broken-instance --base-dir /data/pg
pg start -i broken-instance
```

### 4. Free Up Resources

Destroy instances you're not actively using to free up:

- Podman containers (CPU and memory)
- Host disk space (with `--clean-data`)
- Configuration file entries

## Relationship with Replicas

When destroying a **replica**, the replication slot on the primary is **not** automatically removed. You need to clean it up separately:

```bash
# Step 1: Destroy the replica
pg destroy -i ro1 --force

# Step 2: On the primary, drop the replication slot
pg replica drop ro1 -i primary-instance
```

This two-step process ensures you don't accidentally lose the slot if you plan to recreate the replica later.

## Safety Considerations

- **Data Loss**: `--clean-data` permanently deletes all data, WAL, and backups for the instance. Use with caution.
- **No Undo**: Once destroyed, the instance cannot be recovered unless you have external backups.
- **Configuration Loss**: The instance entry is removed from the config file. If you need the configuration later, back it up first.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--force` | `false` | Skip confirmation prompt |
| `--clean-data` | `false` | Also remove host data, WAL archives, and backup stanza |
| `-i`, `--instance` | `default` | Instance name to destroy |

## Related Commands

- `pg create` — Create a new instance
- `pg start` — Start an instance
- `pg stop` — Stop an instance (container remains)
- `pg replica drop` — Remove replication slot from primary
