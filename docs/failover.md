# Failover: Replica Promotion

Promote a replica to become the new primary when the current primary fails. pgcli provides a 3-step manual failover workflow — each step runs on its respective host, no auto-detection of same-host vs cross-host topology.

## Overview

```
                    ┌─────────────┐
                    │   primary   │  ← crashes / becomes unavailable
                    │  (pg01)     │
                    └──────┬──────┘
                           │ WAL streaming
               ┌───────────┼───────────┐
               ▼           ▼           ▼
          ┌─────────┐ ┌─────────┐ ┌─────────┐
          │  ro1    │ │  ro2    │ │  ro3    │
          │ replica │ │ replica │ │ replica │
          └─────────┘ └─────────┘ └─────────┘
```

After failover (promote ro1 → new primary):

```
                    ┌─────────────┐
                    │   ro1       │  ← new primary (promoted)
                    │  (primary)  │
                    └──────┬──────┘
                           │ WAL streaming
               ┌───────────┼───────────┐
               ▼           ▼           ▼
          ┌─────────┐ ┌─────────┐ ┌─────────┐
          │  ro2    │ │  ro3    │ │  pg01   │
          │ replica │ │ replica │ │ replica │  ← demoted old primary
          └─────────┘ └─────────┘ └─────────┘
```

## 3-Step Failover

Each step is an independent command. Run them **in order**, on their respective hosts.

```bash
# Step 1: On the replica being promoted
pg replica promote ro1

# Step 2: On the old primary host (when it recovers)
pg replica drop ro1 -i pg01

# Step 3: On each remaining replica host
pg replica repoint ro2 --primary-dsn "postgres://admin:<pw>@<new-primary-ip>:<port>/<db>" --primary-name ro1
pg replica repoint ro3 --primary-dsn "postgres://admin:<pw>@<new-primary-ip>:<port>/<db>" --primary-name ro1
```

### Step 1: `pg replica promote <name>`

Run on the host of the replica being promoted to primary.

```bash
pg replica promote ro1
```

**What happens:**

1. Validates the instance is a replica (`ReplicaOf` is set) and the container is running
2. Calls `pg_promote()` (PostgreSQL 12+ native promotion — no container restart)
3. Waits for recovery to end (usually sub-second)
4. Cleans up `primary_conninfo` from `postgresql.auto.conf` via `ALTER SYSTEM RESET`
5. Updates config: clears `ReplicaOf` and `PrimaryDSN`, enables `PITR`
6. Prints next-step instructions

**After promotion, run `pg start`** to initialize the pgBackRest stanza and enable WAL archiving:

```bash
pg start -i ro1
```

This triggers:
- pgBackRest stanza creation
- `archive_mode` / `archive_command` configuration
- PostgreSQL restart to apply postmaster-level parameters

**Idempotent:** If the replica is already promoted (e.g. from a manual `pg_ctl promote`), the command skips to config update.

### Step 2: `pg replica drop <name> -i <old-primary>`

Run on the **old primary host** to clean up the replication slot. Skip this step if the old primary is permanently lost.

```bash
pg replica drop ro1 -i pg01
```

This drops the physical replication slot `pgcli_r_ro1` on the old primary. Without cleanup, the slot would hold WAL indefinitely until the primary runs out of disk space.

**When to run:**
- Old primary recovered and is running → run this command
- Old primary is permanently lost → skip (slot is gone with the server)
- Plan to demote old primary to replica → skip (Step 3 handles cleanup via destroy + rebuild)

### Step 3: `pg replica repoint <name> --primary-dsn <dsn> --primary-name <name>`

Run on **each remaining replica host** to re-point it to the new primary.

```bash
pg replica repoint ro2 \
  --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@10.241.21.97:35439/pg01_db" \
  --primary-name ro1
```

**What happens:**

1. Queries the new primary's extensions via DSN (`pg_extension` catalog)
2. If non-builtin extensions exist (e.g. pg_cron, timescaledb), builds a local `-ext` image with matching packages
3. Stops the old replica container and destroys its data directory
4. Creates a replication slot on the new primary via DSN
5. Updates config: `ReplicaOf`, `PrimaryDSN`, `ImageTag`, `Extensions`, disables `PITR`
6. Re-initializes via `pg_basebackup -R` from the new primary
7. Starts the replica container in standby mode

**Why destroy + rebuild instead of ALTER SYSTEM SET?**

After promotion, the new primary advances to a new timeline. Other replicas on the old timeline cannot simply change `primary_conninfo` — PostgreSQL rejects the connection with:

```
FATAL: requested starting point on timeline 1 is not in this server's history
```

The only safe approach is a full `pg_basebackup` from the new primary.

### Getting the Primary DSN

Get the new primary's connection string from `pg status` on the promoted replica's host:

```bash
pg status -i ro1
# Connection: postgres://admin:fbcQx9uIzvTO6dVJ@127.0.0.1:35439/pg01_db
```

Replace `127.0.0.1` with the new primary host's IP reachable from the replica host (e.g. `10.241.21.97`).

## Demoting the Old Primary

When the old primary recovers, you can rejoin it as a replica of the new primary using the same `repoint` command:

```bash
# On the old primary host
pg replica repoint pg01 \
  --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@10.241.21.97:35439/pg01_db" \
  --primary-name ro1
```

This works even though `pg01` was a primary (no `ReplicaOf` set). The command:

1. Stops pg01 and destroys its data (including old PITR stanza)
2. Creates a replication slot for pg01 on the new primary
3. Sets `ReplicaOf = "ro1"`, `PITR.Enabled = false`
4. Re-initializes from the new primary via `pg_basebackup`

After repoint, pg01 streams WAL from the new primary as a read-only replica — no WAL archiving, no backups.

## Extension Sync

When a replica is repointed to a new primary, pgcli automatically synchronizes extensions:

1. **Query** — Connects to the new primary via DSN and queries `pg_extension` for installed extensions
2. **Filter** — Identifies non-builtin extensions (those requiring external packages, e.g. pg_cron, pgmq, timescaledb)
3. **Build** — If non-builtin extensions exist, builds a local `-ext` image:
   - If a local `-ext` image already exists, installs missing packages on top (reuses Pigsty repo — fast)
   - If no `-ext` image exists, builds from the base image with Pigsty repo setup
   - `apt-get install` is idempotent — installing an already-present package is a no-op
4. **Apply** — On replica start, `ApplyExtensions` writes `shared_preload_libraries` to `postgresql.conf`
5. **Skip CREATE EXTENSION** — Replicas are read-only; extensions are replicated from the primary via `pg_basebackup` + WAL streaming

This ensures the replica container has the required shared libraries (e.g. `pg_cron`) that are referenced in `postgresql.auto.conf`.

## Same-Host vs Cross-Host

pgcli does **not** auto-detect topology. You choose where to run each command:

| Scenario | Step 1 | Step 2 | Step 3 |
|----------|--------|--------|--------|
| All on one host | `pg replica promote ro1` | `pg replica drop ro1 -i pg01` | `pg replica repoint ro2 --primary-dsn "postgres://...@127.0.0.1:..." --primary-name ro1` |
| Primary + replicas split across hosts | On replica host | On old primary host | On each replica host with the new primary's network IP |
| Mixed | On the respective host | On old primary host | On each replica host |

The `--primary-dsn` must use an IP/hostname reachable from the host where `repoint` runs.

## Complete Example

```bash
# ── Initial setup: pg01 (primary) + ro1, ro2 (replicas) on same host ──

$ pg list
NAME    ROLE      PRIMARY   STATUS
pg01    primary   -         Up 2 hours
ro1     replica   pg01      Up 1 hour
ro2     replica   pg01      Up 1 hour

# ── pg01 crashes ──

# Step 1: Promote ro1
$ pg replica promote ro1
  [OK] pg_promote() signaled
  [OK] recovery ended, instance is now read-write
  [OK] primary_conninfo removed from postgresql.auto.conf
✓ Replica "ro1" promoted to primary

$ pg start -i ro1           # enable PITR + WAL archiving

# Step 2: Clean up on old primary (skip if pg01 is permanently lost)
$ pg replica drop ro1 -i pg01
  [OK] replication slot "pgcli_r_ro1" removed from primary "pg01"

# Step 3: Re-point ro2 to new primary
$ pg replica repoint ro2 \
    --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@127.0.0.1:35437/pg01_db" \
    --primary-name ro1
  [OK] extension image built with pg_cron, pgmq, timescaledb
  [OK] replication slot "pgcli_r_ro2" created on new primary
  [OK] config updated (ReplicaOf = "ro1")
✓ Replica "ro2" re-pointed to "ro1"

# ── pg01 recovers, demote to replica ──
$ pg replica repoint pg01 \
    --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@127.0.0.1:35437/pg01_db" \
    --primary-name ro1
  [OK] backup stanza removed: pgcli_pg01
  [OK] replication slot "pgcli_r_pg01" created on new primary
  [OK] config updated (ReplicaOf = "ro1", PITR disabled)
✓ Replica "pg01" re-pointed to "ro1"

# ── Final state ──
$ pg list
NAME    ROLE      PRIMARY   STATUS
pg01    replica   ro1       Up 30 seconds    # demoted
ro1     primary   -         Up 10 minutes    # new primary
ro2     replica   ro1       Up 5 minutes
```

## Cross-Host Example

```bash
# ── Setup: ra3 (primary, host A) + ra2 (replica, host A) + ro2 (replica, host B) ──

# ra3 crashes. On host A, promote ra2:
$ pg replica promote ra2
$ pg start -i ra2

# On host B (10.241.20.147), re-point ro2 to new primary ra2 (host A = 10.241.21.97):
$ pg replica repoint ro2 \
    --primary-dsn "postgres://admin:fbcQx9uIzvTO6dVJ@10.241.21.97:35438/pg01_db" \
    --primary-name ra2
-> New primary has 3 non-builtin extension(s): pg_cron, pgmq, timescaledb
-> Extension image already has all required packages
  [OK] replication slot "pgcli_r_ro2" created on new primary
  [OK] config updated (ReplicaOf = "ra2", image = ...-ext)
✓ Replica "ro2" re-pointed to "ra2"

# Verify cross-host replication:
$ pg exec -i ra2 "INSERT INTO test(msg) VALUES ('after failover')"
$ pg exec -i ro2 "SELECT * FROM test ORDER BY id DESC LIMIT 1"
   msg: after failover
```

## Notes

- **pg_promote()** — PostgreSQL 12+ native function, no container restart required. The instance exits recovery in-place and becomes read-write immediately
- **Timeline divergence** — After promotion, the new primary is on a new timeline. Other replicas cannot be re-pointed with `ALTER SYSTEM SET primary_conninfo` — they must be rebuilt via `pg_basebackup`
- **PITR on promoted replica** — After promotion, run `pg start` to create the pgBackRest stanza and enable WAL archiving. The promoted replica has no prior backup history
- **Replication slots** — The old primary's slot for the promoted replica becomes stale after promotion. `pg replica drop` cleans it up. If the old primary is demoted to a replica, `repoint` destroys the old data and the stale slot is no longer referenced
- **Extensions** — Replica containers inherit `shared_preload_libraries` from the primary via `postgresql.auto.conf`. The repoint command ensures the local image has the required extension packages before rebuilding the replica
- **CREATE EXTENSION skipped** — Replicas are read-only; `pg_basebackup` copies the extension metadata from the primary, so `CREATE EXTENSION` is not needed (and would fail with "cannot execute CREATE EXTENSION in a read-only transaction")
