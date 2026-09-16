---
title: "Backup"
description: "Backup guide for pgcli"
weight: 20
icon: fa-solid fa-shield-halved
menus:
  main:
    identifier: docs-backup
    parent: docs
    weight: 20
    params:
      icon: fa-solid fa-shield-halved
cascade:
  type: docs
  footer_style: slim
---


## Snapshots

```bash
# Create snapshot (full backup)
pg snapshot create -i proj01

# Create differential backup (recommended)
pg snapshot create --type diff -i proj01

# Stream backup container logs during snapshot
pg snapshot create --tail-logs -i proj01

# List snapshots
pg snapshot list -i proj01

# Limit the number of snapshots displayed
pg snapshot list --limit 5 -i proj01

# Delete snapshot
pg snapshot delete 20260826-073712F -i proj01
```

**Snapshot types:**
- `full` — Complete backup (default, self-contained)
- `diff` — Changes since last full backup
- `incr` — Changes since last backup

## Shared Backup Container

All instances share a single pgbackrest container; each instance gets its own stanza in the repository.

```bash
# Initialize the shared pgbackrest container (build image, create dirs, generate config)
pg backup setup

# Use a custom base directory for backup data and logs
pg backup setup --base-dir /mnt/backup

# Start / stop the backup container
pg backup start
pg backup stop

# Show backup container status
pg backup status
```

Backup infrastructure (network, image, directories, config, container) is prepared automatically on `pg start`; run `pg backup setup` manually to reinitialize, e.g. after changing the base directory.

## S3 Object Storage Repository

Patroni cluster backups and WAL archiving can push to any S3-compatible store
(MinIO, AWS S3, ...). Configure it under `backup.repo.s3` in `pg.yaml`:

```yaml
backup:
  repo:
    s3:
      endpoint: 10.0.0.9:9000     # host:port, no scheme
      bucket: pgbackrest
      access_key: admin
      secret_key: <password>       # stored in pg.yaml like every pgcli secret
      ca_file: /home/you/.pgcli/tls/minio/store/ca.crt   # required for a private CA
```

Then re-run setup — it performs the archiving orchestration:

```bash
pg backup setup
```

- **HTTPS is mandatory.** pgBackRest refuses plaintext S3 (upstream won't
  implement it), so the endpoint must serve TLS. pgcli's own MinIO does via
  `pg addon install minio --tls` — pgcli generates a self-signed CA whose
  `ca.crt` path you put in `ca_file` above. Publicly-caught endpoints (real
  AWS S3) just omit `ca_file`; if you truly cannot supply a CA, `verify_tls:
  false` is the escape hatch (no certificate verification).
- **Cross-host HA needs no key/CA hand-copy.** For a Patroni cluster spread
  over several hosts, `ca_file` and the backup SSH public key only have to be
  set up on *one* host: `pg backup setup` publishes the CA to the cluster's
  etcd registry and a joiner with `ca_file` empty pulls it; each host's backup
  public key is likewise published on `pg ha create` and merged into every
  member's authorized_keys by `setup`. Only public material (the CA
  certificate, SSH public keys) enters the registry — never private keys or
  the S3 secret_key. See the HA doc, "Backing up a cross-host leader".
- **Archiving is automated.** When setup finds a Patroni member whose archiving
  config is stale, it pauses the cluster, recreates members replicas-first
  (the recreate restarts PostgreSQL, which postmaster-level `archive_mode`
  needs), then resumes. Each member's patroni.yml gets the same
  `archive_command` — one stanza per cluster, members listed as `pg*-host` so
  pgBackRest finds the primary itself — and WAL streams to S3 from then on.
- **Blast radius.** The S3 repo applies only to Patroni cluster stanzas;
  `pg`-managed regular instances keep backing up to the local repo, unchanged.

One-shot flag equivalent (`secret_key` is best edited into pg.yaml so it never
lands in shell history):

```bash
pg backup setup --s3-endpoint 10.0.0.9:9000 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-ca-file ~/.pgcli/tls/minio/store/ca.crt
```

