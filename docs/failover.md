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
6. Automatically initializes PITR:
   - pgBackRest stanza creation
   - `archive_mode` / `archive_command` configuration
   - PostgreSQL restart to apply postmaster-level parameters
7. Prints next-step instructions

**Idempotent:** If the replica is already promoted (e.g. from a manual `pg_ctl promote`), the command skips to config update.

### Step 2: `pg replica drop <name> -i <old-primary>`

Run on the **old primary host** to clean up the replication slot. This step is only needed in specific scenarios.

```bash
pg replica drop ro1 -i pg01
```

This drops the physical replication slot `pgcli_r_ro1` on the old primary. Without cleanup, the slot would hold WAL indefinitely until the primary runs out of disk space.

**When to run:**

| Scenario | Action | Why |
|----------|--------|-----|
| Old primary is permanently lost | **Skip** | Slot is gone with the server |
| Plan to demote old primary to replica | **Skip** | `repoint` destroys the data directory (including `pg_replslot/`), all slots are implicitly removed |
| Old primary recovered, keep running as independent primary | **Must run** | Slot holds WAL indefinitely; without cleanup the disk will eventually fill up |
| Old primary recovered but will be shut down | **Optional** | No harm in skipping if the instance will not run again |

> **Keep the old primary as-is?** If you want to preserve the old primary with its original data (e.g. for forensic analysis or as a read-only archive), you can simply leave it alone — do not run `drop` or `repoint` on it. The old primary keeps running as an independent instance with stale data. Just be aware that the replication slot for the promoted replica still exists and will accumulate WAL; you may want to drop just that specific slot (`pg replica drop ro1 -i pg01`) while leaving everything else untouched.

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
  [OK] pg_promote() signaled
  [OK] recovery ended
  [OK] PITR initialized (stanza + archive_mode)
✓ Replica "ra2" promoted to primary

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

## Cascading Replication

A replica can itself serve as a primary for downstream replicas, forming a cascading chain. This reduces load on the primary and enables hierarchical topologies.

```
primary (ra3)
  ├─→ replica (ra2) ← upstream for ra2_ro1
  │     └─→ replica (ra2_ro1)
  └─→ replica (pg01)
```

### How It Works

1. **Create a replica of a replica**: Use the replica as the `-i` target
   ```bash
   # ra2 is a replica of ra3, create ra2_ro1 as a replica of ra2
   pg replica create ra2_ro1 -i ra2
   ```

2. **WAL propagation**: 
   - ra2 streams WAL from ra3
   - ra2_ro1 streams WAL from ra2
   - Data flows: ra3 → ra2 → ra2_ro1

3. **Replication slots**: Each link maintains its own slot
   - ra3 has slot `pgcli_r_ra2`
   - ra2 has slot `pgcli_r_ra2_ro1`

### Benefits

- **Reduced primary load**: Only direct replicas connect to primary
- **Geographic distribution**: Primary → regional replica → local replicas
- **Network efficiency**: Local replicas can share a regional upstream

### Limitations

- **Increased latency**: Each hop adds replication delay
- **Cascading failures**: If ra2 fails, ra2_ro1 loses its upstream
- **Promotion complexity**: Promoting ra2_ro1 requires repointing it to a new primary

### Verify Cascading

```bash
# Check ra2's downstream replicas
pg exec -i ra2 "SELECT client_addr, state FROM pg_stat_replication"

# Check ra2_ro1's upstream
pg exec -i ra2_ro1 "SELECT conninfo FROM pg_stat_wal_receiver"
```

### Failover with Cascading

If ra2 (middle node) fails:

```bash
# Option 1: Re-point ra2_ro1 to ra3 directly
pg replica repoint ra2_ro1 \
  --primary-dsn "postgres://admin:password@ra3-host:5432/pg01_db" \
  --primary-name ra3

# Option 2: Wait for ra2 to recover (automatic once ra2 reconnects to ra3)
```

If ra3 (primary) fails and ra2 is promoted:

```bash
# Step 1: Promote ra2
pg replica promote ra2

# Step 2: ra2_ro1 automatically follows (it's already replicating from ra2)
# No action needed for ra2_ro1

# Step 3: Re-point other replicas to the new primary
pg replica repoint pg01 \
  --primary-dsn "postgres://admin:password@ra2-host:5432/pg01_db" \
  --primary-name ra2
```

## Notes

- **pg_promote()** — PostgreSQL 12+ native function, no container restart required. The instance exits recovery in-place and becomes read-write immediately
- **Timeline divergence** — After promotion, the new primary is on a new timeline. Other replicas cannot be re-pointed with `ALTER SYSTEM SET primary_conninfo` — they must be rebuilt via `pg_basebackup`
- **PITR on promoted replica** — After promotion, run `pg start` to create the pgBackRest stanza and enable WAL archiving. The promoted replica has no prior backup history
- **Replication slots** — The old primary's slot for the promoted replica becomes stale after promotion. `pg replica drop` cleans it up. If the old primary is demoted to a replica, `repoint` destroys the old data and the stale slot is no longer referenced
- **Extensions** — Replica containers inherit `shared_preload_libraries` from the primary via `postgresql.auto.conf`. The repoint command ensures the local image has the required extension packages before rebuilding the replica
- **CREATE EXTENSION skipped** — Replicas are read-only; `pg_basebackup` copies the extension metadata from the primary, so `CREATE EXTENSION` is not needed (and would fail with "cannot execute CREATE EXTENSION in a read-only transaction")
