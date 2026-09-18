---
title: "Patroni Cluster Backup"
description: "pgBackRest backups for a Patroni HA cluster: backup setup, S3 repository and WAL archiving, pg ha snapshot operations, and reinit when a replica's LSN is stuck"
weight: 50
---

This page walks the **complete procedure for backing up a Patroni HA cluster**:
getting the backup infrastructure up with `pg backup setup`, taking
full/incr/diff backups with `pg ha snapshot`, and one operational pitfall — when a
replica's `Replay LSN` stalls and `pg ha status` shows members disagreeing on LSN,
how to pull it back with `patronictl reinit`. Command and mechanism details live in
[Patroni HA](../ha/#backing-up-a-cross-host-leader) and [Backup](../../backup/#s3-object-storage-repository);
this page strings them into a path you can run end to end.

## Model: one stanza per cluster, the backup container finds the primary

A Patroni cluster (scope) maps to **one** pgBackRest stanza: `pgcli_<scope>`
(matching the namespace-qualified DCS scope, so two pgcli namespaces sharing one S3
bucket never collide). The stanza is **cluster-wide** — the backup-side config lists
every member as a `pg*-host`, and pgBackRest SSH-probes them, locates the current
primary itself, and keeps following it across failovers **with no config change**.
Therefore:

- There is **no per-member backup**. `full` / `incr` / `diff` all operate on the one
  cluster stanza; pgBackRest snapshots whatever host is the leader at that moment.
- Backups run from the shared **backup container**, reaching the primary over SSH.
  Cross-host members are reachable too (trust is wired up automatically by `setup`).
- `pg ha snapshot` commands **never need you to name the leader** — pass the scope;
  pgBackRest resolves the primary.

## Prerequisite: `pg backup setup`

One idempotent run brings up the whole backup path and gives the cluster archiving:

```bash
# simplest (backup data lands under the default base-dir)
pg backup setup

# S3 object repository (the prerequisite for archiving to MinIO / AWS S3).
# When the store is on another host and you have no ca.crt yet, pull it with
# one TLS handshake first — no scp:
pg backup fetch-ca 10.0.0.9:9000          # prints a SHA-256 fingerprint to cross-check
pg backup setup --s3-endpoint 10.0.0.9:9000 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-ca-file ~/.pgcli/backup/repo-ca/ca-10.0.0.9-9000.crt
```

What setup does (when an S3 repo is configured, it first runs an **endpoint
preflight** — a plain TCP dial that fails the run immediately on a mistyped host
or a store that is down, rather than after the image pull / container start
buries the real cause):

1. Build/pull the pgbackrest image, create the network and dirs, generate the
   backup-container `pgbackrest.conf` and the member-local archive view
   `pgbackrest-archive.conf`.
2. Start the shared backup container, then **verify repository connectivity**: a
   `pgbackrest repo-ls` proves the whole stack (TLS, credentials, bucket) and
   maps a failure to an actionable hint — a cert error points at
   `pg backup fetch-ca`, access-denied at the credentials, a refused/timeout
   connection at the endpoint.
3. **Wire up cross-host backup trust**: each host publishes its backup **public key**
   into the cluster's etcd registry at `pg ha create`; `setup` merges every member's
   key into one per-cluster `authorized_keys` bind-mounted into each member container.
   sshd re-reads that file on every login and the merge rewrites it in place, so
   **hosts that join later are trusted without a restart**. The S3 repo CA follows the
   same path: a host with `ca_file` publishes it into the registry, joiners with it
   empty pull it. (Details: [HA → Backing up a cross-host leader](../ha/#backing-up-a-cross-host-leader).)
4. **Enable WAL archiving**: when a member's archive config is stale, pause the
   cluster, recreate stale members replicas-first / leader last (a recreate is exactly
   the postmaster restart `archive_mode` needs), resume, and render the same
   `archive_command` into every member's `patroni.yml`.
5. Run `stanza-create` + `check` for every cluster stanza.

> **HTTPS is mandatory.** pgBackRest rejects plaintext S3. A MinIO meant to receive
> cluster archives must serve TLS — install it with `pg addon install minio --tls`
> (pgcli's self-signed CA; point `--s3-ca-file` at its `ca.crt`). When the MinIO is on
> another machine, pull its CA with one TLS handshake — `pg backup fetch-ca
> <store-host>:<port>` — no scp needed (see [MinIO → Getting the CA onto a remote
> host](../addon/minio/#tls---tls)).

Confirm after setup:

```bash
pg backup status
```

Once the backup container is `Up` and the stanzas are ready, start backing up.

> **Create members *after* setup, on every host.** A member's archiving is fixed
> at creation time: the container only mounts `pgbackrest-archive.conf` if that
> file already exists, and the rendered `patroni.yml` only carries the archive
> GUCs if an S3 repo is configured. A member `pg ha create`d before `pg backup
> setup` on its host therefore starts **without WAL archiving** — `pg
> ha snapshot`/`pg ha restore` cannot cover it until a later `setup` flags it
> stale and recreates it inside a pause window. `pg ha create` warns about this
> up front; run `pg backup setup` first on each host for a backup-ready create.

## Snapshot operations: `pg ha snapshot`

These act on a cluster (scope), running pgBackRest against the current primary from
the shared backup container. This is the Patroni-cluster counterpart of `pg snapshot`
(single instance).

```bash
# full backup (default --type full); --tail-logs streams pgBackRest output
pg ha snapshot create app --tail-logs

# incremental (changes since the last backup)
pg ha snapshot create app --type incr

# differential (changes since the last full)
pg ha snapshot create app --type diff

# list every snapshot for the cluster (Start / Stop / Name / Type)
pg ha snapshot list app
pg ha snapshot list app --limit 5

# delete a specific snapshot (label from list) — prompts unless --force
pg ha snapshot delete app 20260916-140131F_20260917-015154I
pg ha snapshot delete app <label> --force
```

Notes:

- Before each command, `pg ha snapshot` **re-renders** the backup-container
  `pgbackrest.conf` from the current topology, so member adds/removes and port changes
  are reflected in the stanza immediately — the file is bind-mounted, no recreate.
- **You can only delete a non-unique full**: pgBackRest keeps at least one full, so
  deleting the only one is refused (take a new full first).
- create connects to whatever host is leader at the time; after a failover it still
  succeeds — that is the point of the cluster-wide stanza.

## Pitfall: a stuck replica LSN → `patronictl reinit`

**Symptom.** In `pg ha status app` one replica's `Replay LSN` trails its own
`Receive LSN` (or visibly differs from another replica) and **never converges** over
time. Note Patroni's `Lag` column is relative to the leader, so every number moves
when the leader's LSN advances; to spot a real stall compare a replica's own recv vs
replay, or watch whether its Replay LSN stays frozen across checks.

**Confirm.** Three signals point to the same conclusion — the replica's WAL replay is
at a hole and streaming has actually stopped:

```bash
# 1) leader has no replication clients, slots inactive
pg exec --dsn "postgres://<user>@<leader_host>:<port>/postgres" \
  "SELECT client_addr, state FROM pg_stat_replication"
pg exec --dsn "postgres://<user>@<leader_host>:<port>/postgres" \
  "SELECT slot_name, active, confirmed_flush_lsn FROM pg_replication_slots"

# 2) the stuck replica has no walreceiver
pg exec --dsn "postgres://<user>@<replica_host>:<port>/postgres" \
  "SELECT status FROM pg_stat_wal_receiver"

# 3) the replica container log loops the same two lines (pg logs ha fetches by
#    scope + member name; -f follows, -n takes more lines):
pg logs ha <scope> -m <member> -n 50 | grep -iE "prev-link|waiting for WAL"
#   LOG: record with incorrect prev-link ... at 0/20000060
#   LOG: waiting for WAL to become available at 0/20000078
```

`record with incorrect prev-link` + a repeating `waiting for WAL to become available`
means the WAL stream has a hole/discontinuity at a segment boundary and the startup
process is dead-waiting for the next segment. Patroni often keeps logging
"no action. I am ... a secondary, and following a leader" each cycle — it believes it
is following while the postmaster's walreceiver has actually stopped, so it **will not
self-heal**. This is a replication runtime issue, unrelated to backups.

**Fix: reinit the replica.** Have Patroni take a fresh `pg_basebackup` from the leader
and restart replication. This **destructively rebuilds that replica's data directory**
(the leader and other replicas are untouched), so Patroni requires `--force`:

```bash
# CLUSTER_NAME is the namespace-qualified scope (the name after "Cluster:" in pg ha status)
pg ha ctl <scope> -- reinit <scope>-<ns-suffix> <member> --force
# example (namespace default -> -default suffix):
pg ha ctl app -- reinit app-default node1 --force
```

**`-f` does not work**: `patronictl reinit` only accepts the full `--force` (unlike
`switchover`/`failover`'s `--yes`). `Success: reinitialize for member node1` means it
was dispatched.

**Verify recovery.** reinit wipes and rebuilds the data dir, then basebackups + replays.
Poll until it is `recv == replay` and `status=streaming`:

```bash
pg exec --dsn "postgres://<user>@<replica_host>:<port>/postgres" \
  "SELECT pg_last_wal_receive_lsn() recv, pg_last_wal_replay_lsn() replay,
          (SELECT status FROM pg_stat_wal_receiver) wr"
#   expected: recv == replay, wr = streaming

pg ha status app    # the replica's Replay LSN catches up, Lag converges
```

After reinit completes, run `pg ha snapshot create app --type full` once to realign the
snapshot baseline with the healthy topology.
