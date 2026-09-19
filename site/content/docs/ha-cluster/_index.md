---
title: "HA Cluster"
description: "Patroni high-availability clusters with pgcli — pg ha commands, dynamic configuration, REST API, extensions, cluster backup and restore, direct SQL"
weight: 45
icon: fa-solid fa-sitemap
menus:
  main:
    identifier: docs-ha-cluster
    parent: docs
    weight: 45
    params:
      icon: fa-solid fa-sitemap
cascade:
  type: docs
  footer_style: slim
---

[Patroni](https://patroni.readthedocs.io) is the de-facto standard for PostgreSQL
high availability: it owns each postmaster's lifecycle, streams replication
between members, and performs **automatic failover** when the leader is lost.
pgcli exposes Patroni as its own top-level command — `pg ha` — a distinct mode
rather than a plain `pg` instance or an addon installed with `pg addon install`.

> **Note on placement.** Patroni pages used to live under
> [Addons](../addon/). They are listed here as a standalone **HA Cluster**
> section because `pg ha` is not installed via the addon system — the addon
> index only *mentions* it for discovery. The etcd DCS and the HAProxy
> load balancer remain addon pages ([etcd](../addon/etcd/),
> [HAProxy](../addon/haproxy/)); this section links to them where relevant.

## Pages

| Page | What it covers |
|------|----------------|
| [Patroni HA](./ha/) | The `pg ha` command set: create, status, switchover/failover, pause, cross-host members, passwords, namespace & DCS layout |
| [Dynamic Configuration](./ha-dynamic/) | `pg ha edit-config` / `pg ha ctl` — the DCS-backed runtime config, and why it never recreates a container |
| [REST API](./ha-rest-api/) | The per-member Patroni REST API: health checks, leader redirects, what HAProxy probes |
| [Extensions in HA](./ha-extensions/) | Installing/removing PostgreSQL extensions across a cluster (rolling shared_preload_libraries changes) |
| [Cluster Backup](./ha-backup/) | `pg backup setup` + `pg ha snapshot` — stanza, WAL archiving to S3, cross-host trust |
| [Cluster Restore](./ha-restore/) | `pg ha restore` — cluster PITR via the custom-bootstrap mechanism, the leader-locality pre-check, the post-restore re-baseline |
| [Exec / psql](./ha-exec/) | `pg ha exec` / `pg ha psql` — run SQL against the leader or any member directly, no dsn, no container exec |
| [Example: HA + self-CA MinIO](./ha-example-minio/) | A verified end-to-end walkthrough: a cluster backed up to a MinIO serving a bring-your-own cert, in a fully isolated second environment |
| [Generating a Certificate](./ha-cert/) | `pg cert` — self-signed cert with DNS and IP SANs for any TLS you need without a CA (dev/test servers, an internal endpoint, or a MinIO serving a bring-your-own cert): its flags, and how the single self-signed leaf acts as its own trust anchor |

## The typical path

```bash
# one host: bootstrap a cluster (on an installed etcd addon member)
pg ha create app --member node1 --etcd m1
pg ha create app --member node2 --etcd m1

# other hosts register their members with the same password set
pg ha passwords app --file app-passwd.yml
ssh other-host
pg ha create app --member node3 --advertise-host 10.0.0.12 \
    --etcd-endpoints 10.0.0.9:2379 --passwords-file app-passwd.yml

# run SQL directly — the leader is resolved from the DCS
pg ha exec app "SELECT version()"
pg ha psql app

# back it up and be able to go back in time
pg backup setup --s3-endpoint ...
pg ha snapshot create app --type full
pg ha restore app --time "2026-08-26 15:30:00+00"
```

## Related

- **Addons**: [etcd](../addon/etcd/) (the DCS), [HAProxy](../addon/haproxy/)
  (a stable client endpoint with optional read/write split),
  [MinIO](../addon/minio/) (the S3 repo the backups live in)
- **[Restore → Patroni cluster](../restore/#patroni-cluster)** for PITR basics shared with single-instance restores
- **[Failover: Replica Promotion](../failover/)** for the non-Patroni, single-replica promotion path (`pg replica`)
- **[Namespace Isolation](../namespace/)** — scopes, stanzas, and container names are namespace-prefixed
