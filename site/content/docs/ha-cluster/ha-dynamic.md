---
title: "Patroni Dynamic Configuration"
description: "Reference for all dynamically configurable parameters stored in the DCS"
weight: 46
---

These parameters are stored in the DCS (etcd) under `/service/<scope>/config` and
applied to **every** member of the cluster. Modify them with `pg ha edit-config`:

```bash
pg ha edit-config app -- -s loop_wait=5
pg ha edit-config app -- -s 'postgresql.parameters.max_connections=200'
pg ha edit-config app --show          # view current config
```

> Reference: [Patroni official docs](https://patroni.readthedocs.io/en/latest/dynamic_configuration.html),
> [Pigsty dynamic config](https://pigsty.cc/docs/patroni/config/dynamic/).

> **`bootstrap.dcs` is one-time.** The `bootstrap.dcs` block in `patroni.yml`
> only takes effect when the **first** member of a scope bootstraps the cluster.
> Once Patroni writes the config into the DCS, subsequent changes to
> `bootstrap.dcs` in the YAML file are **completely ignored** — even on
> re-install (`pg ha create`). Re-running `pg ha create` does **not** re-read
> `bootstrap.dcs` — Patroni sees the existing `config` key in the DCS and uses
> it directly.

| Method | Description |
|--------|-------------|
| `pg ha edit-config app -- -s key=value` | **Recommended** — pgcli's standard way |
| `pg ha ctl app -- edit-config` | Passthrough to patronictl, same effect |
| Patroni REST API (`PATCH /config`) | Requires access to a member's REST API port |

## Core timing parameters

| Parameter | Default | Min | Description |
|-----------|---------|-----|-------------|
| `loop_wait` | `10` | 1 | Seconds the main loop sleeps between iterations (lock renewal, DCS updates, state refresh) |
| `ttl` | `30` | 20 | Leader lock TTL in seconds. Effectively the wait time before auto-failover triggers |
| `retry_timeout` | `10` | 3 | DCS and PostgreSQL operation retry timeout. If the DCS or network is down for less than this, Patroni does **not** demote the leader |

> **Constraint when changing `loop_wait`, `retry_timeout`, or `ttl`:**
> ```
> loop_wait + 2 * retry_timeout <= ttl
> ```

## Failover parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `maximum_lag_on_failover` | `1048576` | Maximum replication lag (bytes) for a replica to be eligible for leader promotion |
| `maximum_lag_on_syncnode` | `-1` | Maximum lag (bytes) before a synchronous standby is replaced by a healthy async one. When ≤ 0, Patroni does not replace unhealthy sync standbys. Set high enough to avoid frequent replacement during high-transaction workloads |
| `max_timelines_history` | `0` | Max timeline history entries kept in the DCS. 0 = keep all |
| `primary_start_timeout` | `300` | Seconds the leader has to recover from a failure before failover triggers. 0 = immediate failover on crash detection (may lose transactions with async replication). Max failover time = `loop_wait + primary_start_timeout + loop_wait`; with 0, just `loop_wait` |
| `primary_stop_timeout` | — | Seconds to wait when stopping PostgreSQL (only effective with `synchronous_mode`). If the stop exceeds this timeout, Patroni sends SIGKILL to the postmaster. ≤ 0 or unset = no effect |
| `failover_timeout` | `0` | Seconds to wait before triggering failover after the leader is lost. 0 = immediate |

## Replication mode

| Parameter | Default | Description |
|-----------|---------|-------------|
| `synchronous_mode` | `false` | Enable synchronous replication (`off` / `on` / `quorum`). The leader manages `synchronous_standby_names`; only the last-known leader or a sync standby may run for leader. Guarantees zero data loss at the cost of write unavailability when durability cannot be assured |
| `synchronous_mode_strict` | `false` | When no sync standby is available, refuse to disable sync replication — blocks all client writes to the leader |
| `synchronous_node_count` | `1` | Number of synchronous standby nodes. Dynamically adjusted as members join/leave. Clamped to the number of eligible nodes |
| `failsafe_mode` | `false` | Enable [DCS failsafe mode](https://patroni.readthedocs.io/en/latest/dcs_failsafe_mode.html): when the DCS is unreachable, the leader keeps running instead of demoting itself |

## PostgreSQL settings

| Parameter | Default | Description |
|-----------|---------|-------------|
| `postgresql.use_pg_rewind` | `false` | Use `pg_rewind` to rejoin a failed leader (faster than full `pg_basebackup`). Requires data page checksums (`--data-checksums` at initdb) or `wal_log_hints=on` |
| `postgresql.use_slots` | `true` | Use replication slots (prevents WAL loss when a replica disconnects). Default on PostgreSQL 9.4+ |
| `postgresql.pg_hba` | — | Rules for generating `pg_hba.conf`. Ignored if the PostgreSQL `hba_file` parameter is set to a non-default value |
| `postgresql.pg_ident` | — | Rules for generating `pg_ident.conf`. Ignored if `ident_file` is non-default |
| `postgresql.parameters` | — | PostgreSQL GUCs as key-value pairs, e.g. `{max_connections: 100, wal_level: "replica", wal_log_hints: "on"}`. Many are required for replication to work |
| `postgresql.recovery_conf` | — | Additional `recovery.conf` entries for standby configuration (PG12+ handled transparently) |

Set PostgreSQL parameters via the `postgresql.parameters` key:

```bash
pg ha edit-config app -- -s 'postgresql.parameters.max_connections=200'
pg ha edit-config app -- -s 'postgresql.parameters.work_mem=64MB'
pg ha edit-config app -- -s 'postgresql.parameters.wal_log_hints=on'
```

## Standby cluster

If defined, the cluster bootstraps as a **standby cluster** that streams from a
remote primary.

| Parameter | Description |
|-----------|-------------|
| `standby_cluster.host` | Remote primary address |
| `standby_cluster.port` | Remote primary port |
| `standby_cluster.primary_slot_name` | Slot name for replication (optional, defaults to member name) |
| `standby_cluster.create_replica_methods` | Ordered list of methods to bootstrap the standby leader from the remote primary |
| `standby_cluster.restore_command` | WAL restore command |
| `standby_cluster.archive_cleanup_command` | Archive cleanup command for the standby leader |
| `standby_cluster.recovery_min_apply_delay` | Delay before applying WAL records |

## Replication slots

| Parameter | Default | Description |
|-----------|---------|-------------|
| `member_slots_ttl` | `30min` | How long a replica's physical replication slot is retained after it shuts down. 0 = delete immediately when the member key expires from the DCS. Only effective on PostgreSQL 11+ |
| `slots` | — | Permanent replication slots (hash map). Preserved across switchover/failover. Physical slots on PG11+ are created on all nodes and advanced every `loop_wait` seconds. Logical slots are copied from primary to replicas via restart, then advanced every `loop_wait` seconds. Requires `use_slots: true` |
| `ignore_slots` | — | Slots managed externally that Patroni should not touch (list of attribute sets). Any subset match causes the slot to be ignored |

### Permanent slots example

```yaml
slots:
  permanent_physical_slot:
    type: physical
  permanent_logical_slot:
    type: logical
    database: my_db
    plugin: pgoutput

ignore_slots:
  - name: externally_managed_slot
    type: physical
```

### Node-pinned physical slots

For a fixed cluster topology, define a permanent physical slot per node to
prevent slot deletion during temporary outages:

```yaml
slots:
  node1:
    type: physical
  node2:
    type: physical
  node3:
    type: physical
```

> **Warning:** Permanent replication slots are synced from the
> **primary**/**standby_leader** to replicas only. Applications should use them
> on the leader node. Using permanent slots on replicas causes unbounded
> `pg_wal` growth across the cluster. Exception: physical slots matching a
> Patroni member name (created and maintained by Patroni) are synced across all
> nodes for inter-node replication.

## Viewing the current configuration

```bash
pg ha edit-config app --show
```

Or read directly from etcd:

```bash
ETCDCTL_ENDPOINTS=http://10.0.0.1:2379 pg etcdctl get /service/app-default/config -- --prefix
```
