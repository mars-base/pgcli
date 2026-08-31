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

## Cross-network replicas

The same-host flow above assumes primary and replica share one server (one podman daemon, one network). For a replica on *another host*, pgcli runs one command on each side — no SSH, the only cross-machine information is what you pass as parameters:

```bash
# ---- on the PRIMARY host: prepare the primary (run first) ----
pg replica create ro1 -i pg01 --replica-host 10.241.20.100

# ---- on the REPLICA host: copy data and start the replica (run second) ----
pg replica create ro1 --primary-dsn "postgres://admin:<password>@10.241.20.50:35432/pg01_db" --primary-name pg01
```

### Getting the primary DSN

If the primary is a pgcli-managed instance, get its connection info with `pg status`:

```bash
pg status -i pg01
# ...
#   Connection: postgres://admin:fbcQx9uIzvTO6dVJ@127.0.0.1:35432/pg01_db
```

Then replace `127.0.0.1` with the primary host's IP as seen from the replica host (e.g. `10.241.20.50`) — the user, password and database are used as-is. Note the primary host must accept TCP connections on that port from the replica host (firewall/security group).

What each side does:

- **Primary side** (`--replica-host <ip|hostname>`): only *prepares* the primary — nothing is created locally. It appends a `host replication all <addr>` entry to `pg_hba.conf` and creates the physical slot `pgcli_r_<name>`, then prints the exact `--primary-dsn` command to run on the replica host. IPs get a `/32` (`/128` for IPv6) mask; hostnames are written as-is. An IP already inside a managed RFC1918 range (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) is skipped as redundant. Idempotent — re-running adds no duplicate lines.
- **Replica side** (`--primary-dsn`): first verifies the slot exists on the primary (validates connectivity *and* ordering — running before the primary side fails with an actionable message and no side effects), then registers the instance, runs `pg_basebackup` from the DSN over the network (host networking), and starts the standby.

The replica side runs only on the replica host: user, database and password of the replica instance come from the DSN (physical replication copies `pg_authid`, so the local password must equal the primary's). The primary name is given with `--primary-name` and recorded as `replica_of`; it does not have to exist in the replica host's config, and `-i` is not used for the remote primary — if given, it keeps its strict meaning and must reference a real local instance.

Destroy is symmetric, one command per host — **in this order**:

```bash
# 1. on the REPLICA host: removes container + config, never touches the remote slot
pg destroy -i ro1

# 2. on the PRIMARY host: drop the slot (no-op if already gone); the pg_hba
#    entry is kept, matching same-host destroy behavior
pg replica drop ro1 -i pg01
```

Step 1 must run before step 2: PostgreSQL refuses to drop a slot that is still being streamed (`replication slot is active`), so the replica must be destroyed first to close its streaming connection. `replica drop` is idempotent — re-running when the slot is already gone succeeds as a no-op.

### Non-pgcli primary

The primary does not have to be managed by pgcli — the replica side works against any PostgreSQL server, as long as the primary side has been prepared manually (the slot check only verifies the slot exists, not who created it):

1. Allow replication from the replica host in `pg_hba.conf`, then reload (`SELECT pg_reload_conf()`):
   ```
   host replication <replica user> <replica ip>/32 scram-sha-256
   ```
2. Create the physical slot with the exact name `pgcli_r_<replica-name>` (the replica side checks this name):
   ```sql
   SELECT pg_create_physical_replication_slot('pgcli_r_ro1');
   ```
   Requires `wal_level = replica` (or `logical`) and a user with `REPLICATION` privilege — the DSN user.

Then the replica-side command is unchanged:

```bash
pg replica create ro1 --primary-dsn "postgres://<user>:<pass>@<primary ip>:5432/<db>" --primary-name pg01
```

On destroy, there is no pgcli on the primary side — after `pg destroy -i ro1`, drop the slot manually:

```sql
SELECT pg_drop_replication_slot('pgcli_r_ro1');
```

If a base backup fails (e.g. network hiccup), destroy the replica and re-run the replica-side command — the slot and hba entry on the primary side remain valid.

## Notes

- **Read-only** — the replica rejects all writes (`cannot execute INSERT in a read-only transaction`). To make it writable you would promote it (`pg_ctl promote`), which is not exposed as a pgcli command yet
- **Same data, same password** — physical replication is a byte-for-byte copy of the primary, including `pg_authid`. The replica's admin password and database name are therefore identical to the primary's; only container name, port and data directory differ. With `--dsn`-style connections use the replica's port
- **PITR disabled on replicas** — a standby archives nothing and is not registered with the pgBackRest backup container; backups run on the primary
- **Primary must be running** — both for initial creation (`pg_basebackup`) and for continuous streaming; if the primary restarts, the replica reconnects automatically (slot shows `active`)
- **Lag display** — `replica list` lag (`now() - pg_last_xact_replay_timestamp()`) grows while the primary is idle; it drops back to zero on the next replicated transaction. This is expected idle behavior, not drift
- **Idempotent start** — repeated `pg start -i ro1` skips the base backup when the data directory is already initialized
