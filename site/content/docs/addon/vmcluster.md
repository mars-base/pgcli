---
title: "VMCluster"
description: "Run VictoriaMetrics cluster edition — three components (vminsert + vmselect + vmstorage) as managed containers with cross-host support and horizontal scaling."
weight: 58
---

`pg addon install vmcluster` runs [VictoriaMetrics cluster edition](https://docs.victoriametrics.com/cluster-victoriametrics/) — three components each in its own container from the official `docker.io/victoriametrics/{vminsert,vmselect,vmstorage}:v1.154.1-cluster` images. Unlike the single-node `victoriametrics` addon (one container, one port), this manages up to three containers with three separate images and five ports, supporting cross-host deployment and horizontal scaling.

**Architecture**:

| Component | Role | HTTP Port | Internal TCP |
|-----------|------|-----------|--------------|
| vminsert | Ingestion — shards writes across all vmstorage nodes by metric hash | 8480 | Dials vmstorage:InternalInsertPort |
| vmselect | Queries — merges results from all vmstorage nodes | 8481 | Dials vmstorage:InternalSelectPort |
| vmstorage | Data storage (sharded, replicable) | 8482 | Listens on InternalInsertPort + InternalSelectPort |

> **There is NO web login/password.** Like the single-node VictoriaMetrics, the
> cluster edition ships **no authentication at all**. Its HTTP API runs arbitrary
> MetricsQL (a superset of PromQL), so remote-exposed instances are guarded
> **only by the network boundary** — your firewall, or `--listen 127.0.0.1` for
> loopback. Treat it like an unauthenticated read/write console.

> **Platform**: Linux only. VMCluster requires host networking for the internal
> TCP ports between components. On macOS, installation is refused with a clear
> error.

## Deployment Models

### Model 1 — Converged single-host (default)

All three components on one host, analogous to BeeGFS v1:

```bash
pg addon install vmcluster --name vmc
# 1 host, 3 containers (vminsert + vmselect + vmstorage)
# --storage-node auto-derived as 127.0.0.1:<internalPorts>
```

### Model 2 — Cross-host distribution

```bash
# Host A — storage node 1
pg addon install vmcluster --name vmc --roles storage --listen 10.0.0.1

# Host B — storage node 2
pg addon install vmcluster --name vmc --roles storage --listen 10.0.0.2

# Host C — ingestion + query layer
pg addon install vmcluster --name vmc --roles insert,select \
  --storage-node 10.0.0.1:8483:8484 \
  --storage-node 10.0.0.2:8483:8484
```

### Model 3 — Horizontal scaling with replication

```bash
# replicationFactor=2 → each write goes to 2 vmstorage nodes
pg addon install vmcluster --name vmc --roles insert,select \
  --replication-factor 2 \
  --storage-node 10.0.0.1:8483:8484 \
  --storage-node 10.0.0.2:8483:8484
```

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `vmc` | Instance name (map key) |
| `--roles` | `insert,select,storage` | Comma-separated roles this host runs (subset for cross-host) |
| `--storage-node` | auto-derived | vmstorage address (repeatable; format `host:insertPort:selectPort`). Lists ALL vmstorage nodes for sharding/replication. Omit for converged single-host |
| `--insert-port` | auto (base 8480) | vminsert HTTP port |
| `--select-port` | auto (base 8481) | vmselect HTTP port |
| `--storage-port` | auto (base 8482) | vmstorage HTTP port |
| `--internal-insert-port` | auto (base 8483) | vmstorage TCP port for vminsert protocol |
| `--internal-select-port` | auto (base 8484) | vmstorage TCP port for vmselect protocol |
| `--listen` | `0.0.0.0` | Bind address — defaults to every interface; use `127.0.0.1` for loopback-only |
| `--retention-months` | `1` | Data retention in months (only for vmstorage role) |
| `--replication-factor` | `1` | How many vmstorage nodes each write goes to (vminsert flag) |
| `--insert-image` | `docker.io/victoriametrics/vminsert:v1.154.1-cluster` | vminsert image override |
| `--select-image` | `docker.io/victoriametrics/vmselect:v1.154.1-cluster` | vmselect image override |
| `--storage-image` | `docker.io/victoriametrics/vmstorage:v1.154.1-cluster` | vmstorage image override |
| `--insert-extra-arg` | — | Extra CLI flag for vminsert (repeatable) |
| `--select-extra-arg` | — | Extra CLI flag for vmselect (repeatable) |
| `--storage-extra-arg` | — | Extra CLI flag for vmstorage (repeatable) |
| `--data-dir` | auto | vmstorage data directory override |
| `--force` | false | Recreate even when running |

## Install

```bash
pg addon install vmcluster --name vmc
```

The command pulls the three images if needed, creates the containers in dependency order (storage → insert → select), and prints the summary:

```
✓ VMCluster installed: "vmc"
  Roles:       storage, insert, select (converged on this host)
  Containers:  pgcli-vmcluster-default-vmc-storage, pgcli-vmcluster-default-vmc-insert, pgcli-vmcluster-default-vmc-select
  Images:      docker.io/victoriametrics/vmstorage:v1.154.1-cluster, docker.io/victoriametrics/vminsert:v1.154.1-cluster, docker.io/victoriametrics/vmselect:v1.154.1-cluster
  Data:        /home/you/.pgcli/addon/vmcluster/vmc/data
  Retention:   1 month(s)
  Insert URL:  http://0.0.0.0:8480/ (vminsert — ingestion)
  Select URL:  http://0.0.0.0:8481/ (vmselect — queries)
  Storage URL: http://0.0.0.0:8482/ (vmstorage — admin/metrics)
  Internal:    TCP 8483 (vminsert), 8484 (vmselect)
```

VMCluster is ready to ingest and query immediately.

## Cluster URL Format

**Important**: The cluster edition uses different URL paths than the single-node VictoriaMetrics. All API endpoints require an `<accountID>` in the path (use `0` for the default account):

**Ingestion via vminsert**:
```bash
# Prometheus remote_write
curl -X POST -d 'test_metric{job="pgcli"} 42' \
  http://127.0.0.1:8480/insert/0/prometheus/api/v1/import/prometheus

# InfluxDB line protocol
curl -X POST -d 'cpu,host=vm01 value=42' \
  http://127.0.0.1:8480/insert/0/influx/write

# Graphite plaintext
echo "test.metric 42 $(date +%s)" | nc 127.0.0.1 8480
```

**Query via vmselect**:
```bash
# Prometheus query API
curl 'http://127.0.0.1:8481/select/0/prometheus/api/v1/query?query=test_metric'

# Range query
curl 'http://127.0.0.1:8481/select/0/prometheus/api/v1/query_range?query=test_metric&start=2024-01-01T00:00:00Z&end=2024-01-02T00:00:00Z&step=1h'

# Export
curl 'http://127.0.0.1:8481/select/0/prometheus/api/v1/export?match[]={__name__!=""}'
```

**Grafana datasource**: Point at vmselect with URL `http://<host>:8481/select/0/prometheus/`

**Prometheus remote_write**: Configure `remote_write` to `http://<host>:8480/insert/0/prometheus/api/v1/write`

## Ports

Each VMCluster instance owns **5 consecutive ports** from the `vmcluster_start_port` pool (default 8480):

- `+0` InsertPort (vminsert HTTP — ingestion endpoint)
- `+1` SelectPort (vmselect HTTP — query endpoint)
- `+2` StoragePort (vmstorage HTTP — admin/metrics endpoint)
- `+3` InternalInsertPort (vmstorage TCP — vminsert dials this)
- `+4` InternalSelectPort (vmstorage TCP — vmselect dials this)

The consecutive allocation matters because `--storage-node` references the internal TCP ports explicitly, so the block must stay together. `8480` shares no number with VictoriaMetrics (`8428`), Prometheus (`9090`), Grafana (`3000`), or the rest.

## Configuration

```yaml
addons:
  vmcluster:
    vmc:
      name: vmc
      container_prefix: pgcli-vmcluster-default-vmc
      insert_image_tag: docker.io/victoriametrics/vminsert:v1.154.1-cluster
      select_image_tag: docker.io/victoriametrics/vmselect:v1.154.1-cluster
      storage_image_tag: docker.io/victoriametrics/vmstorage:v1.154.1-cluster
      listen: 0.0.0.0
      insert_port: 8480
      select_port: 8481
      storage_port: 8482
      internal_insert_port: 8483
      internal_select_port: 8484
      roles:
        - storage
        - insert
        - select
      storage_nodes:
        - address: "10.0.0.1:8483"
          select_port: 8484
        - address: "10.0.0.2:8483"
          select_port: 8484
      replication_factor: 1
      retention: "1"
      autostart: false
```

There is **no config file** — everything is passed as CLI flags to the three binaries. `storage_nodes` is the cross-host topology; empty means auto-derived single-node.

## Adding storage nodes

VictoriaMetrics cluster has no dynamic discovery — the `-storageNode` list is baked into the vminsert/vmselect containers at startup. Adding a vmstorage node requires a **brief maintenance window** to recreate the access-layer containers (not a hot reload). Existing vmstorage data is unaffected.

> **Maintenance impact**: rebuilding vminsert/vmselect causes a **few-second write interruption**. Plan this during a low-traffic window if write continuity is critical. Historical data in vmstorage remains available throughout — only ingestion is briefly interrupted.

### Steps

1. **Create the new vmstorage** on the target host:

```bash
# Host C — new storage node
pg addon install vmcluster --name vmc --roles storage --listen 10.0.0.3
```

2. **Recreate vminsert + vmselect** on the access-layer host, appending the new `--storage-node`:

```bash
pg addon install vmcluster --name vmc --roles insert,select --force \
  --storage-node 10.0.0.1:8483:8484 \
  --storage-node 10.0.0.2:8483:8484 \
  --storage-node 10.0.0.3:8483:8484
```

`--force` drops and recreates the vminsert/vmselect containers. There is a **few-second write interruption**; existing vmstorage data is untouched (only the access-layer containers are replaced).

### Data distribution after adding a node

- **Old data** stays on the original vmstorage nodes (no automatic rebalance).
- **New writes** are re-sharded across all nodes (old + new) by metric hash.
- vmselect merges from all nodes, so queries see both old and new data seamlessly.

VictoriaMetrics cluster does **not** migrate existing data to a newly added node. For most monitoring workloads this is acceptable — time series are continuously written, and after a few hours the new node holds enough data for queries to be balanced. If even distribution is required, export and re-import the old data (`/api/v1/export` → `/api/v1/import`).

## Removing storage nodes

Reverse the process: recreate vminsert/vmselect **without** the departing node's `--storage-node`, then `pg addon remove vmcluster` on the departing host. Data on the removed vmstorage becomes inaccessible to queries — export it first if it needs to be preserved.

## Start and stop

```bash
pg addon stop vmcluster
pg addon start vmcluster
```

Containers run with podman's `unless-stopped` restart policy, so they come back automatically after a host reboot.

## Remove

```bash
pg addon remove vmcluster              # keeps vmstorage data — reinstall revives time series
pg addon remove vmcluster --clean-data # deletes vmstorage data too
```

Only vmstorage has persistent data; vminsert and vmselect are stateless and leave nothing behind.

## Logs

```bash
pg logs addon vmcluster                # vmstorage logs (default service)
pg logs addon vmcluster --service insert   # vminsert logs
pg logs addon vmcluster --service select   # vmselect logs
```

## Known limitations

- **No authentication.** The cluster edition's API runs arbitrary MetricsQL; the only protection for a `0.0.0.0` bind is the network boundary.
- **Linux only.** Host networking is required for the internal TCP ports between components.
- **No automatic load balancing.** For multiple vminsert/vmselect instances, put HAProxy or nginx in front manually.
- **No TLS.** Use the nginx addon for TLS termination if needed.
- pgcli does not validate `--extra-arg` values. A bad flag surfaces only in the container logs on startup.
