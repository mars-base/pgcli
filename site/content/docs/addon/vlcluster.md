---
title: "VLCluster"
description: "Run VictoriaLogs cluster edition — vlinsert + vlselect + vlstorage, all from one image, as managed containers with cross-host sharding."
weight: 60
---

`pg addon install vlcluster` runs [VictoriaLogs cluster edition](https://docs.victoriametrics.com/victorialogs/cluster/) — the horizontally scalable log database. Unlike the single-node `victorialogs` addon (one container, one port), this manages up to three containers from **the same image** (`docker.io/victoriametrics/victoria-logs:v1.53.0`) and three HTTP ports per host. The role is selected entirely by flags.

**Architecture**:

| Component | Role | HTTP Port | Talks to |
|-----------|------|-----------|----------|
| vlinsert | Ingestion — shards writes across all vlstorage nodes by stream hash | 9430 | Dials every `vlstorage:StoragePort` |
| vlselect | Queries — fans out LogsQL and merges results | 9431 | Dials every `vlstorage:StoragePort` |
| vlstorage | Data storage (sharded, **not replicated**) | 9432 | Serves `/internal/insert` + `/internal/select/*` on that one port |

vlinsert and vlselect are stateless and dial the **same single port** on each vlstorage node; there are no separate internal TCP ports like VMCluster's `host:insertPort:selectPort`. A storage node address is therefore just `host:storagePort`.

> **There is NO web login/password.** Like single-node VictoriaLogs, the cluster edition ships **no authentication at all**. Its HTTP API can ingest, query, and delete arbitrary logs, so a remote-exposed instance is guarded **only by the network boundary** — your firewall, or `--listen 127.0.0.1` for loopback. Treat it as an unauthenticated read/write console.

> **Platform**: Linux only. VLCluster requires host networking for the ports that vlinsert/vlselect dial on every vlstorage node. On macOS, installation is refused with a clear error.

> **No replication.** vlinsert shards but does not copy: every stream lives on exactly one vlstorage node. Losing any vlstorage makes queries return HTTP 502 until it comes back (ingestion keeps working, buffered by vlagent or the client). Plan capacity and backup accordingly — this is an upstream design property, not a pgcli limitation.

## Deployment Models

### Model 1 — Converged single-host (default)

All three components on one host:

```bash
pg addon install vlcluster --name vlc --listen 127.0.0.1
# 1 host, 3 containers (vlstorage + vlinsert + vlselect)
# --storage-node auto-derived as 127.0.0.1:<StoragePort>
```

### Model 2 — Cross-host sharding

Same `--roles` + `--storage-node` model as VMCluster: each host runs its own `pg addon install vlcluster`, and pgcli never SSHes.

```bash
# Host A — storage node 1
pg addon install vlcluster --name vlc --roles storage --listen 10.0.0.1

# Host B — storage node 2
pg addon install vlcluster --name vlc --roles storage --listen 10.0.0.2

# Host C — ingestion + query layer
pg addon install vlcluster --name vlc --roles insert,select --listen 10.0.0.3 \
  --storage-node 10.0.0.1:9432 \
  --storage-node 10.0.0.2:9432
```

An access-only host (`--roles insert` and/or `--roles select`) **must** pass `--storage-node` — install aborts otherwise, because the auto-derived loopback address would create an isolated node that silently joins nothing.

All hosts must allocate the same port block for the instance name so the addresses match. The default base is 9430 (see [Ports](#ports)); if you change `vlcluster_start_port` on one host, set it identically on the others.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `vlc` | Instance name (map key) |
| `--roles` | `insert,select,storage` | Comma-separated roles this host runs (subset for cross-host) |
| `--storage-node` | auto-derived | vlstorage address (repeatable; format `host:storagePort`, e.g. `10.0.0.1:9432`). Lists ALL vlstorage nodes for sharding. Omit on a converged host |
| `--insert-port` | auto (base 9430) | vlinsert HTTP port |
| `--select-port` | auto (base 9431) | vlselect HTTP port |
| `--storage-port` | auto (base 9432) | vlstorage HTTP port |
| `--listen` | `0.0.0.0` | Bind address — use `127.0.0.1` for loopback-only |
| `--retention` | binary default | vlstorage retention, e.g. `30d` (only when this host runs storage) |
| `--image` | `docker.io/victoriametrics/victoria-logs:v1.53.0` | Image override (one image for all roles) |
| `--insert-extra-arg` | — | Extra CLI flag for vlinsert (repeatable, appended last) |
| `--select-extra-arg` | — | Extra CLI flag for vlselect (repeatable, appended last) |
| `--storage-extra-arg` | — | Extra CLI flag for vlstorage (repeatable, appended last) |
| `--data-dir` | auto | vlstorage data directory override |
| `--force` | false | Recreate even when running and unchanged |

`--retention` is deliberately not defaulted: empty means the victoria-logs binary default. Unlike vmcluster's `--retention-months`, VictoriaLogs parses durations such as `30d`, `3months`, or `1y`.

## Install

```bash
pg addon install vlcluster --name vlc --listen 127.0.0.1
```

The command pulls the image if needed, creates the containers in dependency order (storage → insert → select), and prints the summary:

```
✓ VLCluster installed: "vlc"
  Roles:       storage, insert, select (converged on this host)
  Containers:  pgcli-vlcluster-default-vlc-storage, ...
  Image:       docker.io/victoriametrics/victoria-logs:v1.53.0
  Data:        /home/you/.pgcli/addon/vlcluster/vlc/data
  Retention:   binary default (--retention to change)
  Insert URL:  http://127.0.0.1:9430/ (vlinsert — ingestion)
  Select URL:  http://127.0.0.1:9431/ (vlselect — LogsQL queries)
  Storage URL: http://127.0.0.1:9432/ (vlstorage — internal APIs + admin)
```

Reinstalling with the same flags is a no-op (`unchanged`); changed flags recreate only what is needed. A role removed from `--roles` has its stale container deleted.

## URL Format

The cluster edition uses the **same API paths** as single-node VictoriaLogs — no account ID, no extra prefix:

**Ingestion via vlinsert**:
```bash
curl -X POST http://127.0.0.1:9430/insert/jsonline \
  -H 'Content-Type: application/json' \
  -d '{"_msg":"hello from vlcluster","app":"demo"}'
```

**Query via vlselect**:
```bash
curl 'http://127.0.0.1:9431/select/logsql/query?query=app:demo&limit=10'
```

vlselect also serves the built-in UI at `http://<host>:9431/select/vmui/`. vlagent forwards to `http://<vlinsert>:9430/insert/native`.

The vlstorage port (`9432`) serves `/internal/insert` and `/internal/select/*` for the other two roles; its own LogsQL endpoints query **only that node's local data**, which is useful for per-node debugging but not for cluster-wide queries.

## Ports

Each VLCluster instance owns **3 consecutive ports** from the `vlcluster_start_port` pool (default 9430):

- `+0` InsertPort (vlinsert HTTP — ingestion endpoint)
- `+1` SelectPort (vlselect HTTP — query endpoint)
- `+2` StoragePort (vlstorage HTTP — internal insert/select APIs)

All three are allocated even on hosts that run only a subset, so a storage node's address is predictable across hosts. The base sits just above single-node VictoriaLogs (9428) and vlagent (9429); the three pools are mutually aware and never hand out the same port.

## Configuration

```yaml
addons:
  vlcluster:
    vlc:
      name: vlc
      container_prefix: pgcli-vlcluster-default-vlc
      image_tag: docker.io/victoriametrics/victoria-logs:v1.53.0
      listen: 0.0.0.0
      insert_port: 9430
      select_port: 9431
      storage_port: 9432
      roles:
        - storage
        - insert
        - select
      storage_nodes:
        - "10.0.0.1:9432"
        - "10.0.0.2:9432"
      retention: 30d
      autostart: false
```

There is **no config file** inside the containers — everything is passed as CLI flags. `storage_nodes` is the cross-host topology; empty means auto-derived single-node.

## Adding storage nodes

vlinsert/vlselect bake the `-storageNode` list at startup, so adding a node requires recreating the access-layer containers (a brief ingestion pause; existing data is untouched):

```bash
# New storage host
pg addon install vlcluster --name vlc --roles storage --listen 10.0.0.3

# Access-layer hosts: reinstall with the full node list
pg addon install vlcluster --name vlc --roles insert,select --listen 10.0.0.3 \
  --storage-node 10.0.0.1:9432 \
  --storage-node 10.0.0.2:9432 \
  --storage-node 10.0.0.3:9432
```

**No automatic rebalance**: old data stays on the original nodes; only new writes are spread across the enlarged set. vlselect merges all nodes, so queries see both. To rebalance older data, wait for retention to expire it or manually copy per-day partitions (upstream offers no rebalance tool).

## Start and stop

```bash
pg addon stop vlcluster
pg addon start vlcluster
```

Containers run with podman's `unless-stopped` restart policy and come back after a host reboot. Stop goes in reverse dependency order (select → insert → storage).

### Autostart on boot

```bash
pg autostart enable --vlcluster --name vlc
```

Boot is cluster-granular: each host starts only the roles it runs, in dependency order (storage → insert → select), starting existing containers and recreating only what is missing. Run it on every host in a cross-host deployment. See [Autostart]({{< relref "../autostart" >}}) for the full mechanism.

## Remove

```bash
pg addon remove vlcluster              # keeps vlstorage data — reinstall revives stored logs
pg addon remove vlcluster --clean-data # deletes the data directory too
```

Removal deletes all three role containers on this host, even orphans left behind by a role change. On an access-only host this does not touch remote storage nodes.

## Logs

```bash
pg logs addon vlcluster                # vlstorage logs (default service)
pg logs addon vlcluster --service insert   # vlinsert logs
pg logs addon vlcluster --service select   # vlselect logs
```

A role this host does not run is rejected with a clear error.

## Known limitations

- **No replication.** One copy per stream; any vlstorage outage makes queries return 502. Ingestion continues and can be buffered upstream (e.g. vlagent's disk queue). Cross-cluster HA needs multiple vlagent `--remote-write-url` targets and a routing layer such as vmauth — out of scope here.
- **No automatic rebalance.** Adding a storage node only affects new data.
- **No authentication.** The HTTP API ingests and queries arbitrary logs; a `0.0.0.0` bind is protected only by the network boundary.
- **Linux only.** Host networking is required.
- **No TLS.** Terminate TLS in front (nginx) or use a private network.
- **Per-host status.** `pg addon list` reports only the roles configured on the current host; it does not probe remote storage nodes.
- pgcli does not validate `--*-extra-arg` values. A bad flag surfaces only in the container logs on startup.
