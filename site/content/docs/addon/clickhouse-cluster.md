---
title: "ClickHouse Cluster"
description: "Run a replicated ClickHouse cluster across hosts — 2 shards × 2 replicas, ReplicatedMergeTree, Distributed tables and ON CLUSTER DDL — coordinated by a standalone ClickHouse Keeper addon"
weight: 65
---

`pg addon install clickhouse --cluster ...` turns the [ClickHouse addon](./clickhouse/) into **one member of a static multi-host cluster**. Each host runs its own ClickHouse container and keeps its own `pg.yaml`; pgcli never SSHes between hosts and never creates cluster members for you. You install the same cluster on every member and pass the shared topology explicitly.

Coordinated replication is provided by **[ClickHouse Keeper](./clickhouse-keeper/)** — a separate top-level addon that must be installed and healthy **before** the ClickHouse members. Keeper is not embedded in the ClickHouse container.

> **Production uses three dedicated Keeper servers.** The four-VM layout below is a
> test topology that co-locates Keeper with ClickHouse to save machines. In
> production, run the Keeper quorum on its own 3 (or 5) servers with separate
> disks and failure domains — see [Production notes](#production-notes).

## Topology

A cluster is defined by a name plus a set of `shard` / `replica` positions. This guide uses the reference test layout:

| Host | IP | Keeper | ClickHouse |
|---|---|---|---|
| host01 | `192.0.2.11` | server id 1 | shard 1 / replica 1 |
| host02 | `192.0.2.12` | server id 2 | shard 1 / replica 2 |
| host03 | `192.0.2.13` | server id 3 | shard 2 / replica 1 |
| host04 | `192.0.2.14` | — | shard 2 / replica 2 |

Substitute your own addresses; the `--peer` lists below are built from this table.

### Shards and replicas

- A **shard** is a horizontal partition: different shards hold different rows.
- A **replica** is a full copy of a shard, kept in sync through Keeper. Every replica in a shard accepts reads and writes; replication is asynchronous.
- With **2 shards × 2 replicas**, every row exists twice and one ClickHouse host per shard can fail without losing data or write availability.
- v1 requires every shard to have the **same replica count**, numbered `1..N` without gaps, and every member must be listed on every host. Membership is static: there is no add/remove-node command.

v1 supports concrete IPv4/IPv6 addresses only (no DNS names), one ClickHouse instance per host, and one Keeper per host.

## Before you start

1. **A running Keeper quorum.** Follow [ClickHouse Keeper](./clickhouse-keeper/) and install three members. `pg addon clickhouse-keeper status` should show 3/3 client endpoints reachable.
2. **The same admin password on every member.** Every ClickHouse member must use the identical `--admin-password`; pgcli cannot read or synchronize it across hosts. A fresh cluster install therefore **requires the flag explicitly** — the generated-password path is disabled in cluster mode.
3. **Free ports.** HTTP `8123`, native TCP `9000`, interserver `9009`; Keeper uses client `9181` and Raft `9234`.
4. **The pgcli binary on every host** and a config file per host (default `~/.pgcli/pg.yaml`). Use the same `--name` everywhere; namespaces still isolate unrelated deployments on the same machine.

## Install

The reference Keeper commands (details on the [Keeper page](./clickhouse-keeper/)):

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

Then install one ClickHouse member per host. The full command for host01:

```bash
pg addon install clickhouse --name clickhouse \
  --cluster chcluster \
  --shard 1 --replica 1 \
  --advertise-host 192.0.2.11 --listen 0.0.0.0 \
  --admin-password "$CH_ADMIN_PASSWORD" \
  --peer 1/2@192.0.2.12:9000 \
  --peer 2/1@192.0.2.13:9000 \
  --peer 2/2@192.0.2.14:9000 \
  --keeper 192.0.2.11:9181 \
  --keeper 192.0.2.12:9181 \
  --keeper 192.0.2.13:9181 \
  --interserver-port 9009
```

The other three members differ only in the local identity and in which peer they omit:

| Host | `--shard` | `--replica` | `--advertise-host` | `--peer` entries (the other three) |
|---|---|---|---|---|
| host01 | 1 | 1 | `192.0.2.11` | `1/2@192.0.2.12:9000`, `2/1@192.0.2.13:9000`, `2/2@192.0.2.14:9000` |
| host02 | 1 | 2 | `192.0.2.12` | `1/1@192.0.2.11:9000`, `2/1@192.0.2.13:9000`, `2/2@192.0.2.14:9000` |
| host03 | 2 | 1 | `192.0.2.13` | `1/1@192.0.2.11:9000`, `1/2@192.0.2.12:9000`, `2/2@192.0.2.14:9000` |
| host04 | 2 | 2 | `192.0.2.14` | `1/1@192.0.2.11:9000`, `1/2@192.0.2.12:9000`, `2/1@192.0.2.13:9000` |

The shares that must be identical on every host are `--cluster`, the three `--keeper` endpoints, `--interserver-port` and `--admin-password`.

`--peer` lists **only the other members** — this host is folded in from `--shard`, `--replica`, `--advertise-host` and its TCP port. The `--keeper` list, on the other hand, is the **complete** Keeper client endpoint list on every host (Keeper has no self-folding for ClickHouse). The default Keeper client port is `9181`, so `--keeper 192.0.2.11` is accepted; write the port explicitly when it is not the default.

A fresh cluster install requires explicit `--cluster`, `--shard`, `--replica`, `--advertise-host`, at least one `--peer`, at least one `--keeper`, `--listen` (non-loopback, either `0.0.0.0` or this node's advertised address) and `--admin-password`. A missing piece is reported as an error listing every missing flag.

## What cluster mode renders

Standard server config still lives in `config.d/zz-pgcli.xml`. Cluster mode adds a second managed file, `config.d/zz-pgcli-cluster.xml`:

```xml
<?xml version="1.0"?>
<clickhouse>
    <remote_servers>
        <chcluster>
            <shard>
                <internal_replication>true</internal_replication>
                <replica>
                    <host>192.0.2.11</host>
                    <port>9000</port>
                    <user>admin</user>
                    <password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>
                </replica>
                <replica>
                    <host>192.0.2.12</host>
                    <port>9000</port>
                    <user>admin</user>
                    <password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>
                </replica>
            </shard>
            <shard>
                <internal_replication>true</internal_replication>
                <replica>
                    <host>192.0.2.13</host>
                    <port>9000</port>
                    <user>admin</user>
                    <password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>
                </replica>
                <replica>
                    <host>192.0.2.14</host>
                    <port>9000</port>
                    <user>admin</user>
                    <password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>
                </replica>
            </shard>
        </chcluster>
    </remote_servers>
    <macros>
        <cluster>chcluster</cluster>
        <shard>1</shard>
        <replica>1</replica>
    </macros>
    <zookeeper>
        <node>
            <host>192.0.2.11</host>
            <port>9181</port>
        </node>
        <node>
            <host>192.0.2.12</host>
            <port>9181</port>
        </node>
        <node>
            <host>192.0.2.13</host>
            <port>9181</port>
        </node>
        <session_timeout_ms>30000</session_timeout_ms>
        <operation_timeout_ms>10000</operation_timeout_ms>
    </zookeeper>
    <distributed_ddl>
        <path>/clickhouse/chcluster/task_queue/ddl</path>
    </distributed_ddl>
    <interserver_http_host>192.0.2.11</interserver_http_host>
    <interserver_http_port>9009</interserver_http_port>
    <default_replica_path>/clickhouse/chcluster/tables/{uuid}/{shard}</default_replica_path>
    <default_replica_name>{replica}</default_replica_name>
</clickhouse>
```

- `remote_servers` declares the full topology under the cluster name; `internal_replication=true` makes a write to a replicated table replicate through Keeper instead of the Distributed engine writing to every replica itself. Each replica entry also carries the admin user so cross-shard `Distributed` queries authenticate on the remote member — the image's entrypoint removes the password-less `default` user when a dedicated admin account is configured. The password is referenced as `from_env`; the container entrypoint wrapper exports it from the mounted secret at startup, so the 0644 cluster file contains only the variable name, never the password.
- `macros` fill `{shard}` and `{replica}` in `ReplicatedMergeTree` paths on this node.
- `zookeeper` points at the Keeper client endpoints (Keeper speaks the ZooKeeper protocol).
- `distributed_ddl` places `ON CLUSTER` DDL tasks under a cluster-specific path.
- `interserver_http_host` / `_port` is the replica-to-replica catch-up endpoint.
- `default_replica_path` / `default_replica_name` let `ReplicatedMergeTree` with no explicit arguments derive its Keeper path from the macros.

The cluster file contains no listeners, logger or backup settings — those stay in the base file. Its only user entry is the `from_env` credential reference above. In cluster mode, `config set` rejects keys that would contradict the managed topology (`remote_servers`, `macros`, `zookeeper`, `distributed_ddl`, `default_replica_path`, `default_replica_name`). A single-node instance may still set an external `zookeeper`/`macros` block of its own.

The cluster file is removed automatically if an instance is reinstalled as a single node (for example after `remove` without `--clean-data`), so a stale `remote_servers` block cannot leak into standalone mode.

## Verify

```bash
pg addon clickhouse cluster status --name clickhouse
```

The command queries `system.clusters` and `system.replicas` on the local member, compares the topology with `pg.yaml` and probes every Keeper client endpoint:

```
clickhouse "clickhouse" — cluster "chcluster", this node shard 1 / replica 1
  Cluster config: /home/you/.pgcli/addon/clickhouse/clickhouse/config.d/zz-pgcli-cluster.xml
  system.clusters: 4 member(s), expected 4
    shard 1 replica 1  192.0.2.11:9000  local
    shard 1 replica 2  192.0.2.12:9000
    shard 2 replica 1  192.0.2.13:9000
    shard 2 replica 2  192.0.2.14:9000
  system.replicas: 1 replicated table(s)
    demo.events replica 1 — ok, delay 0s, queue 0
  [OK] Keeper client endpoints reachable: 3/3 (quorum 2; reachability is not a Raft quorum proof)
```

It exits non-zero if `system.clusters` does not match the stored topology or a replica is read-only. Keeper reachability is reported separately: a TCP-reachable client port is **not** proof of a Raft quorum.

Direct SQL equivalents:

```bash
pg addon clickhouse query --name clickhouse \
  "SELECT cluster, shard_num, replica_num, host_name, port, is_local FROM system.clusters WHERE cluster = 'chcluster'"

pg addon clickhouse query --name clickhouse \
  "SELECT database, table, replica_name, is_readonly, absolute_delay, queue_size FROM system.replicas"

pg addon clickhouse query --name clickhouse \
  "SELECT count() FROM system.zookeeper WHERE path = '/clickhouse/chcluster'"
```

## Create replicated tables

Create the database and tables **once** with `ON CLUSTER`; ClickHouse expands the statement onto every member. Because `default_replica_path` / `default_replica_name` are rendered, `ReplicatedMergeTree` needs no explicit path argument:

```sql
CREATE DATABASE IF NOT EXISTS demo ON CLUSTER chcluster;

CREATE TABLE demo.events ON CLUSTER chcluster
(
    id   UInt64,
    ts   DateTime,
    name String
)
ENGINE = ReplicatedMergeTree
ORDER BY id;

CREATE TABLE demo.events_dist ON CLUSTER chcluster
AS demo.events
ENGINE = Distributed(chcluster, demo, events, rand());
```

Run these one statement at a time through `pg addon clickhouse query` (multi-statement mode is not enabled), or pipe a file through `shell` with `clickhouse-client --multiquery`.

Local table (`demo.events`) and distributed table (`demo.events_dist`) have different jobs:

- `demo.events` holds this shard's rows and replicates them to the sibling replica.
- `demo.events_dist` is a routing view: it fans writes across shards and merges reads from **all** shards. It stores no data.

Insert locally and verify replication on the sibling:

```bash
# On host01: insert into shard 1
pg addon clickhouse query --name clickhouse \
  "INSERT INTO demo.events VALUES (1, now(), 'host01')"

# On host02: the sibling replica sees it
pg addon clickhouse query --name clickhouse \
  "SELECT id, name FROM demo.events ORDER BY id"

# Read across both shards through the Distributed table (run on any member)
pg addon clickhouse query --name clickhouse \
  "SELECT id, name FROM demo.events_dist ORDER BY id"
```

If you prefer explicit paths (for example to give each table its own Keeper layout), pass them yourself and keep the macros in their placeholders:

```sql
ENGINE = ReplicatedMergeTree('/clickhouse/chcluster/tables/{shard}/events', '{replica}')
```

`{shard}` and `{replica}` come from the rendered `macros`, so the expression expands differently on each member.

## Failure behavior

- **One ClickHouse replica stops.** Its sibling in the same shard keeps serving reads and writes. When it returns, it catches up through Keeper and the interserver endpoint; check `system.replicas.queue_size` until it drains.
- **Both replicas of a shard stop.** That shard's data is unavailable, but other shards keep working. Distributed reads return partial results / errors depending on the engine settings; do not treat the cluster as available.
- **One Keeper of three stops.** A quorum of 2/3 remains; `ON CLUSTER` DDL and replication continue.
- **Two Keepers of three stop.** There is no quorum. New replicated tables and DDL fail, and existing replicated tables go read-only once the ClickHouse-to-Keeper session expires (30 s in the rendered config). Treat Keeper quorum loss as an outage and restore a member before writing.

Keeper data is small but critical: it holds the replication log and table metadata. Losing the whole Keeper quorum does **not** lose the table data on disk, but the replication state cannot be reconstructed automatically.

## Operations

- **Identity is immutable.** After install, `--force` rejects changes to `--cluster`, `--shard`, `--replica`, `--advertise-host`, `--peer`, `--keeper`, `--interserver-port` and (in cluster mode) `--tcp-port`. Every other member has this node baked into its configuration, so changing one node's identity requires a coordinated rebuild.
- **Growth is manual.** Adding members requires a rolling rebuild; see [Horizontal scaling](#horizontal-scaling). v1 does not automate rebalancing or shard splitting.
- **`backup` / `restore` are per-node.** They operate on the local instance's local tables. Backing up one replica does not back up the other shards. Replicated tables restored into a cluster need their Keeper paths considered.
- **`config set` affects only the local member and restarts it by default.** A cluster-wide change must be repeated on every host; the CLI does not distribute configuration. Pass `--no-restart` to write the value without restarting, so it applies on the next start. Roll members one at a time; never restart both replicas of a shard together.
- **`user` / `grant` are per-node** unless issued with `ON CLUSTER`. Users and grants created by the CLI run against the local member only.
- **Autostart order.** `pg start` starts autostart-enabled Keeper members first, then ClickHouse members, then nginx. Enable both with `pg autostart enable --clickhouse-keeper --name keeper` and `pg autostart enable --clickhouse --name clickhouse`.

## Setting memory

ClickHouse has no cluster-wide memory limit. Memory is enforced per server process, so every member needs its own configuration — there is no command that pushes a value to the whole cluster.

### Server memory

Set `max_server_memory_usage` at install time with `--config-option`:

```bash
pg addon install clickhouse --name clickhouse \
  --cluster chcluster --shard 1 --replica 1 \
  --advertise-host 192.0.2.11 --listen 0.0.0.0 \
  --admin-password "$CH_ADMIN_PASSWORD" \
  --peer 1/2@192.0.2.12:9000 \
  --peer 2/1@192.0.2.13:9000 \
  --peer 2/2@192.0.2.14:9000 \
  --keeper 192.0.2.11:9181 \
  --keeper 192.0.2.12:9181 \
  --keeper 192.0.2.13:9181 \
  --interserver-port 9009 \
  --config-option max_server_memory_usage=2147483648
```

On an existing member, use `config set` (it restarts the local container by default; `--no-restart` defers the change to the next start):

```bash
# Run on every member separately
pg addon clickhouse config set max_server_memory_usage=2147483648 --name clickhouse
```

Roll members one at a time: wait until `system.replicas` shows `is_readonly=0` with an empty queue before moving on, and never restart both replicas of a shard together.

The default `0` keeps ClickHouse's ratio rule (`max_server_memory_usage_to_ram_ratio`, 0.9 of host RAM). That is risky on shared hosts — always set an absolute value explicitly when Keeper or other services share the machine.

### Query memory

`max_memory_usage`, `max_bytes_before_external_group_by` and `max_bytes_before_external_sort` are **user/profile settings**, not server XML, so `config set` does not apply them. Define them on a user with `ON CLUSTER`, or set them per session:

```sql
CREATE USER app IDENTIFIED WITH sha256_password BY '...'
SETTINGS max_memory_usage = 5000000000,
         max_bytes_before_external_group_by = 2000000000
ON CLUSTER chcluster;
```

```sql
SET max_memory_usage = 5000000000;
```

Clients can also pass them per request (HTTP query parameters, or the driver's settings map). A query-level value cannot exceed the server-wide `max_server_memory_usage`.

### Memory across a Distributed query

Each participating shard executes its local part and is bounded by its own `max_server_memory_usage`; the initiating node additionally holds memory for result merging and aggregation. There is no shared cluster memory pool. Size for **per-node peak × concurrency**, and leave headroom for the initiating node and for Keeper. See [Memory limits](./clickhouse/#memory-limits) for the general ClickHouse guidance.

## Setting CPU

pgcli applies no container-level CPU quota: it never passes `--cpus` or `--cpuset-cpus` to podman, so a container may use every host core. ClickHouse also parallelizes queries across the available cores by default — **saturating the host's cores is the default behavior**.

### Query threads

`max_threads` (default: available cores) and `max_insert_threads` are user/session/request settings, not server XML, so `config set` does not apply them. Define them on a user with `ON CLUSTER`, or set them per session:

```sql
CREATE USER app IDENTIFIED WITH sha256_password BY '...'
SETTINGS max_threads = 4, max_insert_threads = 2
ON CLUSTER chcluster;
```

```sql
SET max_threads = 4;
```

Clients can also pass them per request (HTTP query parameters, or the driver's settings map).

### Server concurrency

`max_concurrent_queries` and `background_pool_size` are server XML settings. Run `config set` on every member separately (it restarts the local container by default; `--no-restart` defers the change):

```bash
# Run on every member separately
pg addon clickhouse config set max_concurrent_queries=20 background_pool_size=8 --name clickhouse
```

### Cluster semantics

Each node is independent — there is no cluster-wide CPU quota. A Distributed query starts threads on every participating shard, and the initiating node also pays the merge cost. Size for **per-node peak × concurrency**, and tune it together with `max_server_memory_usage`. Hard cgroup isolation is outside pgcli's scope: any `--cpus` you add to the container manually disappears when pgcli recreates it.

## Horizontal scaling

v1 membership is static: there is no online add-node command. Scaling means installing the new members first, then rewriting every existing member's `remote_servers` block in a rolling rebuild.

- Every shard keeps the same replica count, numbered consecutively from `1`. Adding one replica means adding it to **every** shard.
- `--peer` is part of the immutable identity, so an existing member cannot be changed in place with `--force`. Remove it **without** `--clean-data` and reinstall under the same `--name` with the extended peer list. The default data directory is reused and replicated tables reconnect to their Keeper paths. If the original install used a custom `--data-dir`, pass the same path again.
- Keep `--cluster`, `--admin-password`, `--interserver-port` and `--tcp-port` unchanged. Keeper membership does not change unless you are scaling Keeper separately.
- Roll one member at a time, never take both replicas of a shard down together, wait until `system.replicas` shows `is_readonly=0` and an empty queue, and pause `ON CLUSTER` DDL during the rollout.

### Add replicas

Add one replica to every shard. Going from 2 shards × 2 replicas to 2 × 3 uses two new hosts:

| Host | `--shard` | `--replica` | `--advertise-host` |
|---|---|---|---|
| host05 | 1 | 3 | `192.0.2.15` |
| host06 | 2 | 3 | `192.0.2.16` |

Install each new member with the full six-member peer list, listing every other member and never the node itself:

```bash
# host05 — shard 1 / replica 3
pg addon install clickhouse --name clickhouse \
  --cluster chcluster --shard 1 --replica 3 \
  --advertise-host 192.0.2.15 --listen 0.0.0.0 \
  --admin-password "$CH_ADMIN_PASSWORD" \
  --peer 1/1@192.0.2.11:9000 --peer 1/2@192.0.2.12:9000 \
  --peer 2/1@192.0.2.13:9000 --peer 2/2@192.0.2.14:9000 \
  --peer 2/3@192.0.2.16:9000 \
  --keeper 192.0.2.11:9181 --keeper 192.0.2.12:9181 \
  --keeper 192.0.2.13:9181 \
  --interserver-port 9009
```

host06 uses `--shard 2 --replica 3 --advertise-host 192.0.2.16` with the matching peer list.

Then roll the four existing members. On each host, remove without `--clean-data` and reinstall with the same identity and the extended peer list:

```bash
# On host01; repeat on host02–host04 with their own identity
pg addon remove clickhouse --name clickhouse     # keeps data, config and logs
pg addon install clickhouse --name clickhouse \
  --cluster chcluster --shard 1 --replica 1 \
  --advertise-host 192.0.2.11 --listen 0.0.0.0 \
  --admin-password "$CH_ADMIN_PASSWORD" \
  --peer 1/2@192.0.2.12:9000 --peer 2/1@192.0.2.13:9000 \
  --peer 2/2@192.0.2.14:9000 \
  --peer 1/3@192.0.2.15:9000 --peer 2/3@192.0.2.16:9000 \
  --keeper 192.0.2.11:9181 --keeper 192.0.2.12:9181 \
  --keeper 192.0.2.13:9181 \
  --interserver-port 9009
```

New replicas fetch existing parts from their shard siblings automatically — there is no manual data rebalance. Verify with `pg addon clickhouse cluster status --name clickhouse`: `system.clusters` should list six members (2 shards × 3 replicas) and `system.replicas` should be healthy.

### Add shards

A new shard starts empty and needs the same replica count as the existing shards. Going from 2 × 2 to 3 × 2 uses two new hosts:

| Host | `--shard` | `--replica` | `--advertise-host` |
|---|---|---|---|
| host05 | 3 | 1 | `192.0.2.15` |
| host06 | 3 | 2 | `192.0.2.16` |

Install them the same way; host05's command differs from the replica example only in identity and peer list:

```bash
# host05 — shard 3 / replica 1
pg addon install clickhouse --name clickhouse \
  --cluster chcluster --shard 3 --replica 1 \
  --advertise-host 192.0.2.15 --listen 0.0.0.0 \
  --admin-password "$CH_ADMIN_PASSWORD" \
  --peer 1/1@192.0.2.11:9000 --peer 1/2@192.0.2.12:9000 \
  --peer 2/1@192.0.2.13:9000 --peer 2/2@192.0.2.14:9000 \
  --peer 3/2@192.0.2.16:9000 \
  --keeper 192.0.2.11:9181 --keeper 192.0.2.12:9181 \
  --keeper 192.0.2.13:9181 \
  --interserver-port 9009
```

host06 uses `--shard 3 --replica 2 --advertise-host 192.0.2.16`. Then roll host01–host04 exactly as above, replacing the two new peer entries with `3/1@192.0.2.15:9000` and `3/2@192.0.2.16:9000`.

Historical rows are **not** redistributed: the Distributed table only routes new writes to the new shard. If existing data must move, plan a reshard separately (for example `clickhouse-copier`, or a new table populated with `INSERT … SELECT` or dual writes); pgcli v1 does not automate this. Verify `system.clusters` lists six members (3 shards × 2 replicas).

## Production notes

- **Run Keeper on dedicated servers.** The co-located layout in this guide exists to fit a four-VM test lab. Production should use **three (or five) independent Keeper servers** with separate disks, power and failure domains. Keeper and ClickHouse compete for the same RAM and I/O, and co-location turns a ClickHouse failure into a quorum risk.
- **Trusted network only.** Keeper has no authentication and no TLS; ClickHouse interserver traffic is plaintext too. Do not expose Keeper ports (`9181`, `9234`) or the interserver port beyond a trusted network.
- **No TLS.** The HTTP and native listeners are plaintext. Front the HTTP port with the [nginx addon](./nginx/) when remote clients need TLS; never expose the native or interserver ports to untrusted networks.
- **Asynchronous replication.** A write acknowledged by one replica is not yet on the sibling. Do not treat a successful insert as durable on two nodes; use `system.replicas` for health and `SYSTEM SYNC REPLICA` when a test must wait for catch-up.
- **Shared admin password.** Every member must use the same `--admin-password`; pgcli cannot enforce this across hosts. A mismatch shows up as inconsistent client access, not as an install error.
- **Memory.** Small VMs need an explicit `max_server_memory_usage` on each ClickHouse member, and Keeper needs headroom too. Pass `--config-option max_server_memory_usage=2147483648` at install time, or set it later with `pg addon clickhouse config set`; there is no cluster-wide push, so repeat it on every member. The 2 GiB test VMs use a few hundred MiB per ClickHouse container; see [Memory limits](./clickhouse/#memory-limits).
- **Upgrades.** Upgrade one replica at a time (stop, `install --force --version <new>`, start), wait for replication to catch up, then move to the sibling. Never run a newer on-disk format under an older server.

## Limits

- Static membership: no online add/remove, no automatic rebalance, no shard splitting.
- Uniform topology only: all shards must have the same replica count.
- Concrete IP addresses only; no DNS names.
- One ClickHouse instance and one Keeper per host.
- No cross-host orchestration: every command addresses this host's `pg.yaml`.
- No TLS, no Keeper authentication, no mixed-version guarantees.
- Replication is asynchronous.

## CLI flag reference

Shared ClickHouse flags (`--name`, `--version`, `--data-dir`, ports, users, config, backup, lifecycle) are documented on the [ClickHouse page](./clickhouse/#cli-flag-reference). The cluster-specific additions:

### `pg addon install clickhouse --cluster`

| Flag | Default | Description |
|---|---|---|
| `--cluster NAME` | — (required) | Cluster name; also becomes the `remote_servers` element, macros entry and DDL path component. `[A-Za-z_][A-Za-z0-9_-]*`, max 48 chars |
| `--shard N` | — (required) | This member's 1-based shard number |
| `--replica N` | — (required) | This member's 1-based replica number within the shard |
| `--advertise-host IP` | — (required) | Concrete non-loopback address peers and clients use for this member |
| `--peer S/R@HOST:PORT` | — (required, repeatable) | Another member as `shard/replica@host:tcp_port`. List the **other** members on every host; this host is added automatically |
| `--keeper HOST[:PORT]` | — (required, repeatable) | Keeper client endpoint. Pass the **full** shared list on every host; default port `9181` |
| `--interserver-port N` | `9009` | Replica catch-up port; must match across the cluster and is immutable after install |
| `--listen ADDR` | required in cluster mode | Must be `0.0.0.0` or this node's `--advertise-host`; loopback is rejected |
| `--admin-password PW` | required in cluster mode | Must be identical on every member; the generated-password path is disabled |
| `--tcp-port N` | auto (base `9000`) | Native port; used by every peer's `--peer` entry and immutable after install |

Cluster identity flags are immutable with `--force`: `--cluster`, `--shard`, `--replica`, `--advertise-host`, `--peer`, `--keeper`, `--interserver-port`, and `--tcp-port` in cluster mode. Re-running `install` without `--force` compares every explicit flag with the stored value and reports drift instead of silently applying it.

### `pg addon clickhouse cluster status`

| Argument/flag | Default | Description |
|---|---|---|
| `--name NAME` | `clickhouse` | Cluster member to inspect; must be running |

The command fails on a single-node instance, when `system.clusters` disagrees with the stored topology, or when a replicated table is read-only. Keeper reachability is informational.

## Verification

Verified on 2026-11-03 with `clickhouse/clickhouse-server:26.8.36.3` on a four-host topology matching the table above: all 103 assertions in `test/addon/test_clickhouse_cluster.sh` passed, covering replicated writes, cross-shard `Distributed` reads and writes, one Keeper member down, one ClickHouse replica down and catch-up, identity immutability and path isolation.
