# Replica (read-only standby)

Create a read-only physical replica of an existing instance. The replica continuously streams WAL from its primary via PostgreSQL physical replication and serves read-only queries — useful for read/write splitting, reporting, or as a warm standby.

```bash
# Create a replica of the default instance
pg replica create ro1

# Create a replica of a specific instance
pg replica create ro1 -i proj01

# List replicas and replication lag
pg replica list
```

## What happens

1. **Pre-flight check** — the primary must be running (verified *before* any config is written; a stopped primary fails with no side effects)
2. **Register** — a new instance entry is added with the primary's database name and password (see Notes), PITR disabled, and `replica_of` set to the primary
3. **Replication setup** — on the primary:
   - `pg_hba.conf` gains `host replication` entries for loopback and RFC1918 ranges (idempotent)
   - a physical replication slot `pgcli_r_<name>` is created, reserving WAL so the replica can never fall behind WAL recycling
4. **Base backup** — `pg_basebackup -R` copies the primary's data directory into the replica's data dir, writing `primary_conninfo` (with password) and `standby.signal` so the replica boots in standby mode
5. **Start** — the replica container starts and streams WAL continuously

## Verify

```bash
# Read-only standby?
pg exec -i ro1 "SELECT pg_is_in_recovery()"      # t

# Writes are rejected
pg exec -i ro1 "INSERT INTO t VALUES (1)"         # read-only transaction error

# Streaming is live
pg exec -i ro1 "SELECT pg_is_in_recovery(), now() - pg_last_xact_replay_timestamp()"

# Slot is active on the primary
pg exec -i primary "SELECT slot_name, active FROM pg_replication_slots"

# Overview
pg list                              # ROLE/PRIMARY columns
pg replica list                      # NAME/PRIMARY/STATUS/LAG
pg status -i ro1                     # Role: standby (replica of ...)
```

## Destroy

```bash
pg destroy -i ro1 --clean-data --force
```

Destroy also drops the `pgcli_r_<name>` replication slot on the primary, so WAL is not held forever on the replica's behalf (best-effort: if the primary is stopped, the slot is left for later manual cleanup).

## Notes

- **Read-only** — the replica rejects all writes (`cannot execute INSERT in a read-only transaction`). To make it writable you would promote it (`pg_ctl promote`), which is not exposed as a pgcli command yet
- **Same data, same password** — physical replication is a byte-for-byte copy of the primary, including `pg_authid`. The replica's admin password and database name are therefore identical to the primary's; only container name, port and data directory differ. With `--dsn`-style connections use the replica's port
- **PITR disabled on replicas** — a standby archives nothing and is not registered with the pgBackRest backup container; backups run on the primary
- **Primary must be running** — both for initial creation (`pg_basebackup`) and for continuous streaming; if the primary restarts, the replica reconnects automatically (slot shows `active`)
- **Lag display** — `replica list` lag (`now() - pg_last_xact_replay_timestamp()`) grows while the primary is idle; it drops back to zero on the next replicated transaction. This is expected idle behavior, not drift
- **Idempotent start** — repeated `pg start -i ro1` skips the base backup when the data directory is already initialized
