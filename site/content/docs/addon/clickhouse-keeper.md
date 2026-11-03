---
title: "ClickHouse Keeper"
description: "Run a standalone ClickHouse Keeper quorum as a top-level addon — the ZooKeeper-compatible coordination service behind ReplicatedMergeTree and ON CLUSTER DDL"
weight: 66
---

`pg addon install clickhouse-keeper` runs **ClickHouse Keeper** as its own managed container from the official `docker.io/clickhouse/clickhouse-keeper` image. Keeper is the ZooKeeper-compatible coordination service used by ClickHouse [replicated clusters](./clickhouse-cluster/): it stores the replicated-table metadata, the replication log and the `ON CLUSTER` DDL task queue.

Keeper is a **separate top-level addon**, never a process inside the ClickHouse container:

- Its config, data and log directories are independent of every ClickHouse instance (pgcli rejects overlapping paths).
- It has its own lifecycle: install, start, stop, remove and autostart are all addressed as `pg addon ... clickhouse-keeper`.
- `pg start` starts autostart-enabled Keeper members **before** ClickHouse members, so ClickHouse never boots against a Keeper quorum that is still down.

This addon is only useful with ClickHouse in cluster mode. A single-node ClickHouse does not need Keeper.

> **Production runs Keeper on dedicated servers.** The examples below co-locate Keeper
> with ClickHouse on a test lab's VMs. Production should use 3 (or 5) independent
> servers with separate disks and failure domains — see [Production notes](#production-notes).

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `clickhouse-keeper` | Instance name (map key; also the default subcommand name) |
| `--version` | `26.8.36.3` | Upstream image tag from `docker.io/clickhouse/clickhouse-keeper` |
| `--server-id` | — (required on fresh install) | This member's 1-based Raft server id; unique within the quorum |
| `--advertise-host` | — (required on fresh install) | Concrete non-loopback address Raft peers use for this member |
| `--peer ID@HOST[:RAFT_PORT]` | — | Another member; repeatable. List every **other** member on every host; this host is added automatically. Default Raft port `9234` |
| `--client-port` | `9181` | ZooKeeper-protocol client port (ClickHouse connects here) |
| `--raft-port` | `9234` | Raft peer port between Keeper members |
| `--listen` | `127.0.0.1` | Bind address. Multi-member installs require a non-loopback value (`0.0.0.0` or the advertised address) |
| `--data-dir` | `<base_dir>/addon/clickhouse-keeper/<name>/data` | Raft log and snapshot storage; absolute path |
| `--log-dir` | `<base_dir>/addon/clickhouse-keeper/<name>/logs` | `clickhouse-keeper.log` and `.err.log`; absolute path |
| `--config-dir` | `<base_dir>/addon/clickhouse-keeper/<name>/config` | Directory that receives `keeper_config.xml`; absolute path |
| `--force` | false | Rewrite the config and recreate the container, keeping data and identity |
| `--clean-data` | false (`remove`) | Also delete the instance dir plus any custom data/log dirs |

`--cluster` is rejected: Keeper membership is expressed entirely by `--server-id`, `--advertise-host` and `--peer`. `--image` does not apply either — use `--version`.

## Install a three-member quorum

Install one member on each host. Only the local identity and the omitted peer differ:

```bash
# host01
pg addon install clickhouse-keeper --name keeper \
  --server-id 1 --advertise-host 192.0.2.11 --listen 0.0.0.0 \
  --peer 2@192.0.2.12 --peer 3@192.0.2.13

# host02
pg addon install clickhouse-keeper --name keeper \
  --server-id 2 --advertise-host 192.0.2.12 --listen 0.0.0.0 \
  --peer 1@192.0.2.11 --peer 3@192.0.2.13

# host03
pg addon install clickhouse-keeper --name keeper \
  --server-id 3 --advertise-host 192.0.2.13 --listen 0.0.0.0 \
  --peer 1@192.0.2.11 --peer 2@192.0.2.12
```

The rendered config on each host contains the **complete** Raft membership (the two peers plus this member folded in from its own identity), sorted by server id. Installs can run in any order: a member whose peers are not up yet starts without listening on the client port and reports a warning instead of failing the install. It joins as soon as a quorum forms, so continue with the remaining members. `pg addon clickhouse-keeper status` warns but does not fail when quorum is not reached yet.

A fresh install requires explicit `--server-id` and `--advertise-host` — Keeper cannot derive either safely. A multi-member install also requires non-loopback `--listen`, because peers cannot reach `127.0.0.1`. Quorums smaller than three members are accepted for development with a warning; use **3 or 5** members in production.

The install summary:

```
✓ ClickHouse Keeper "keeper" installed
  Version:     26.8.36.3
  Image:       docker.io/clickhouse/clickhouse-keeper:26.8.36.3
  Container:   pgcli-clickhouse-keeper-keeper
  Server ID:   1
  Client:      0.0.0.0:9181
  Raft:        192.0.2.11:9234
  Members:     1@192.0.2.11:9234, 2@192.0.2.12:9234, 3@192.0.2.13:9234
  Data dir:    /home/you/.pgcli/addon/clickhouse-keeper/keeper/data
  Logs dir:    /home/you/.pgcli/addon/clickhouse-keeper/keeper/logs
  Config:      /home/you/.pgcli/addon/clickhouse-keeper/keeper/config/keeper_config.xml
```

## Quorum

Keeper replicates its state with Raft. A quorum is `floor(n/2)+1` members:

| Members | Quorum | Failures tolerated |
|---|---|---|
| 1 | 1 | 0 (development only) |
| 2 | 2 | 0 — either member's loss stops the quorum |
| 3 | 2 | **1** |
| 5 | 3 | 2 |

Three members is the smallest configuration that survives a host failure. Even numbers are not useful: four members still need three for quorum. Run 3 or 5.

- **One member down (3-member quorum).** ClickHouse keeps creating replicated tables and running `ON CLUSTER` DDL.
- **Two members down.** No quorum. Replication stops, new replicated tables and DDL fail, and existing replicated tables go read-only after the session timeout. Restore a member before writing.
- **Loss of the whole quorum.** Table data on ClickHouse disks is not deleted, but replication and DDL state cannot be rebuilt automatically. Back up the Keeper data directories along with ClickHouse data.

Keeper's own client port is intentionally unauthenticated, like ZooKeeper. Keep `9181` and `9234` on a trusted network.

## Status

```bash
pg addon clickhouse-keeper status --name keeper
```

The command reports the container state, the rendered membership and a local `mntr` probe:

```
clickhouse-keeper "keeper" — running (ruok ok)
  Version:    26.8.36.3
  Image:      docker.io/clickhouse/clickhouse-keeper:26.8.36.3
  Container:  pgcli-clickhouse-keeper-keeper
  Server ID:  1
  Client:     0.0.0.0:9181
  Raft:       192.0.2.11:9234
  Members:    1@192.0.2.11:9234, 2@192.0.2.12:9234, 3@192.0.2.13:9234
  Local state: leader
  Reachability:
    server 1  127.0.0.1:9181  reachable (leader)
    server 2  192.0.2.12:9181  reachable
    server 3  192.0.2.13:9181  reachable
  [OK] 3/3 client endpoints reachable (quorum 2; client reachability is not a Raft quorum proof)
```

The local member is probed over loopback, so it shows `127.0.0.1`; remote members are probed at their advertised addresses. Only the local member's Raft role is known (from `mntr`), so remote members have no role suffix. Reachability probes every member's **client** port with the `ruok` four-letter word; the local member also answers `mntr`, which provides `zk_server_state` (`leader` / `follower` / `standalone`). A reachable client port is not a quorum proof — the output says so explicitly. Combine `mntr` state with the reachable count to judge health.

## What pgcli creates

Everything for one instance lives under `<base_dir>/addon/clickhouse-keeper/<name>/`:

| Host path | Container path | Notes |
|---|---|---|
| `config/keeper_config.xml` | `/etc/pgcli-clickhouse-keeper/keeper_config.xml` | Read-only managed config |
| `data/` | `/var/lib/clickhouse-keeper` | Raft log under `coordination/log`, snapshots under `coordination/snapshots` |
| `logs/` | `/var/log/clickhouse-keeper` | `clickhouse-keeper.log` / `.err.log`, rotated at 100 MB × 3 |

All three paths can be overridden independently (`--config-dir` controls the directory that holds `keeper_config.xml`). pgcli validates at install time that the Keeper config/data/log paths do not overlap each other or any ClickHouse config/data/log/backup/secret path.

The generated XML sets:

```xml
<?xml version="1.0"?>
<clickhouse>
    <logger>
        <level>information</level>
        <log>/var/log/clickhouse-keeper/clickhouse-keeper.log</log>
        <errorlog>/var/log/clickhouse-keeper/clickhouse-keeper.err.log</errorlog>
        <size>100M</size>
        <count>3</count>
        <console>1</console>
    </logger>
    <listen_host>0.0.0.0</listen_host>
    <keeper_server>
        <tcp_port>9181</tcp_port>
        <server_id>1</server_id>
        <log_storage_path>/var/lib/clickhouse-keeper/coordination/log</log_storage_path>
        <snapshot_storage_path>/var/lib/clickhouse-keeper/coordination/snapshots</snapshot_storage_path>
        <coordination_settings>
            <operation_timeout_ms>10000</operation_timeout_ms>
            <session_timeout_ms>30000</session_timeout_ms>
            <raft_logs_level>information</raft_logs_level>
        </coordination_settings>
        <raft_configuration>
            <server><id>1</id><hostname>192.0.2.11</hostname><port>9234</port></server>
            <server><id>2</id><hostname>192.0.2.12</hostname><port>9234</port></server>
            <server><id>3</id><hostname>192.0.2.13</hostname><port>9234</port></server>
        </raft_configuration>
    </keeper_server>
</clickhouse>
```

The container uses `--network host`, `--restart unless-stopped`, a 30-second stop timeout and `nofile=262144`. The official entrypoint chowns the data and log mounts before dropping privileges. There is no secret: Keeper configuration contains no credentials.

## Lifecycle

```bash
pg addon start clickhouse-keeper --name keeper      # create if missing, wait briefly for local ruok; a pending quorum only warns
pg addon stop clickhouse-keeper --name keeper
pg addon clickhouse-keeper status --name keeper
pg addon clickhouse-keeper shell --name keeper      # bash inside the container
pg addon clickhouse-keeper config show --name keeper
pg logs addon clickhouse-keeper --name keeper -f
pg addon remove clickhouse-keeper --name keeper     # remove container + pg.yaml entry, keep data
pg addon remove clickhouse-keeper --name keeper --clean-data
```

`remove` prints a quorum warning and reminds you that the remaining members still reference the removed server id in their `raft_configuration`. v1 has no online membership change: replacing a member means reinstalling the affected members with updated peer lists. `--clean-data` deletes only the Keeper instance directory and any custom `--data-dir`/`--log-dir` outside it; ClickHouse data is never touched.

`config show` prints the rendered XML; Keeper has no `config set` in v1 and no password subcommand.

Autostart:

```bash
pg autostart enable --clickhouse-keeper --name keeper
pg autostart status
pg autostart disable --clickhouse-keeper --name keeper
```

`pg start` starts autostart-enabled Keeper members first, then ClickHouse members, then nginx.

## Production notes

- **Dedicated servers.** Run the quorum on 3 (or 5) servers that do not also run ClickHouse. Separate disks and failure domains matter more than CPU: a lost Keeper quorum makes the whole ClickHouse cluster read-only, so co-location couples unrelated failures.
- **Trusted network only.** No authentication, no TLS. Any client that can reach `9181` can read and modify coordination state.
- **Back up the data directory.** Keeper's data is small and changes frequently; a filesystem snapshot of `data/` on one healthy member is enough for recovery, but a consistent backup is easier with the quorum stopped or one member stopped at a time.
- **One Keeper per host.** v1 enforces one Keeper member per host.
- **Static membership.** No online add/remove/replace; plan the quorum size up front.
- **Resource use.** Keeper is lightweight (hundreds of MiB) but not free on a 2 GiB test VM; leave headroom for ClickHouse's `max_server_memory_usage`.

## Limits

- No authentication and no TLS.
- No online membership change; `--server-id`, `--advertise-host`, `--peer`, `--client-port` and `--raft-port` are immutable after install, even with `--force`.
- No `config set`; edit the generated XML only through a full reinstall.
- IP addresses only (no DNS names).
- One Keeper per host.
- The addon manages one container per config entry; multi-member setups are one config entry per host.

## CLI flag reference

### `pg addon install clickhouse-keeper`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `clickhouse-keeper` | Instance name; `[A-Za-z0-9][A-Za-z0-9_-]*`, max 48 chars |
| `--version TAG` | `26.8.36.3` | Upstream image tag from `docker.io/clickhouse/clickhouse-keeper` |
| `--server-id N` | required on fresh install | 1-based Raft server id, unique in the quorum |
| `--advertise-host IP` | required on fresh install | Concrete non-loopback address peers dial |
| `--peer ID@HOST[:RAFT_PORT]` | — | Repeatable; list every other member on every host |
| `--client-port N` | `9181` | ZooKeeper-protocol client port |
| `--raft-port N` | `9234` | Raft peer port |
| `--listen ADDR` | `127.0.0.1` | Bind address; non-loopback required with peers |
| `--data-dir DIR` | `<instance>/data` | Absolute path; mounted at `/var/lib/clickhouse-keeper` |
| `--log-dir DIR` | `<instance>/logs` | Absolute path; mounted at `/var/log/clickhouse-keeper` |
| `--config-dir DIR` | `<instance>/config` | Absolute path; receives `keeper_config.xml` |
| `--force` | false | Rewrite config and recreate the container; identity flags stay immutable |

Re-running `install` without `--force` compares every explicit flag with the stored value and reports drift instead of applying it.

### `pg addon clickhouse-keeper status` / `shell` / `config show`

| Argument/flag | Default | Description |
|---|---|---|
| `--name NAME` | `clickhouse-keeper` | Instance to operate on |

### Lifecycle, logs, autostart

| Command | Flags | Description |
|---|---|---|
| `pg addon start clickhouse-keeper` | `--name NAME` | Start (create if missing); wait briefly for local `ruok`, warning when the quorum is still pending |
| `pg addon stop clickhouse-keeper` | `--name NAME` | Stop the container, keep data |
| `pg addon remove clickhouse-keeper` | `--name NAME`, `--clean-data` | Remove container and config entry; `--clean-data` deletes the Keeper instance/data/log paths |
| `pg logs addon clickhouse-keeper` | `--name NAME`, `-n N` (default 50), `-f` | Container logs |
| `pg autostart enable\|disable` | `--clickhouse-keeper`, `--name NAME` | Toggle boot start for one member |

### Global flags

| Flag | Description |
|---|---|
| `-c, --config FILE` | Config file (default `~/.pgcli/pg.yaml`) |
| `-o, --output FILE` | Output config path (default the same file) |
| `-i, --instance NAME` | PostgreSQL instance selector — not used by Keeper; use `--name` instead |

## Verification

Verified on 2026-11-03 with `clickhouse/clickhouse-keeper:26.8.36.3`: the Keeper assertions in `test/addon/test_clickhouse_cluster.sh` passed on a three-member quorum, including leader/follower state, 2/3 quorum with one member stopped, and `ON CLUSTER` DDL while degraded.
