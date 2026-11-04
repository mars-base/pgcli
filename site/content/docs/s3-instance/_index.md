---
title: "Instance with S3"
description: "Back up regular pgcli instances straight to the shared S3 repository — pg create --backup-target s3, no dual-write, promote-time selection"
weight: 25
icon: fa-solid fa-cloud-arrow-up
menus:
  main:
    identifier: docs-s3-instance
    parent: docs
    weight: 25
    params:
      icon: fa-solid fa-cloud-arrow-up
cascade:
  type: docs
  footer_style: slim
---

A regular (non-Patroni) instance archives WAL and takes its pgBackRest backups
to **local disk** by default. This page covers the opt-in alternative: send
both straight to the environment's shared **S3 repository**
(`backup.repo.s3` — MinIO, silo, rustfs, garage, AWS S3, ...), chosen once at
`pg create` time.

HA clusters behave differently and are not the subject here: a Patroni stanza
uses the S3 repo automatically whenever one is configured — see
[Backup → S3 Object Storage Repository](../backup/#s3-object-storage-repository)
and [HA Cluster → S3 storage](../ha-cluster/ha-s3-storage/).

## Prerequisite: the global S3 repo

`backup-target: s3` instances do not carry their own endpoint or credentials —
they reference the single host-level `backup.repo.s3` block in `pg.yaml`. The
S3 store normally lives on **its own machine** (e.g. a dedicated
`pg addon install minio` host), separate from the hosts running the pgcli
instances — the instance host only needs network reach to the endpoint plus
the store's TLS trust anchor. Store host `192.0.2.9`, instance host
`192.0.2.10`:

```bash
# On the store host: a TLS-serving MinIO (pgBackRest refuses plaintext S3)
pg addon install minio --tls
```

```bash
# On the instance host: pull the trust anchor with one TLS handshake — no scp
pg backup fetch-ca 192.0.2.9:9000      # prints the CA path + SHA-256 fingerprint
```

Cross-check the fingerprint against the store host (it's trust-on-first-use),
then register the repo — a complete block is endpoint + bucket + access_key +
secret_key:

```bash
pg backup setup --s3-endpoint 192.0.2.9:9000 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-secret-key '<secret>' \
    --s3-ca-file <path printed by fetch-ca>
```

(A passing shell with history expansion or shared logs may prefer editing
`backup.repo.s3.secret_key` into `pg.yaml` instead of the flag.) The rest of
the mechanics are identical to the Patroni case —
[Backup → S3 Object Storage Repository](../backup/#s3-object-storage-repository)
covers HTTPS-mandatory, `verify_tls`, and publicly-caught endpoints (real AWS
S3 just omit `--s3-ca-file`). The S3 block must be **complete** — an
incomplete repo is rejected at `pg create`, `pg start`, and `pg backup setup`,
never silently downgraded to local. And it is **per host**: on a multi-host
setup, each host that runs instances or promotes replicas configures its own
`backup.repo.s3` the same way (cross-host HA clusters instead distribute the
CA and SSH public keys via etcd — see the Backup doc's "Cross-host HA" bullet).

## Create an instance on S3

```bash
pg create -i proj01 --backup-target s3
```

`--backup-target` accepts `local` (default) and `s3`. The choice is recorded
per instance (`instances.<name>.pitr.backup_target: s3`) and survives reloads;
a local instance simply omits the key.

What the flag changes underneath: the instance's stanza in the shared backup
container gets a per-stanza `repo1-type=s3` override (endpoint, bucket,
region, path, credentials, CA), and so does the hostless conf inside the PG
container that `archive-push` runs from. The `[global]` repo stays local, so
the two target kinds never interfere.

**No dual-write.** An s3-target stanza writes **only** to S3 — backups and WAL
never touch the local repository, and no copy is kept on either side. If you
later aim the instance at a different repo, nothing migrates: the stanza starts
a fresh backup chain from zero, and old local backups simply age out.

**The target is chosen at create time.** There is no switch for a running
instance — to move one, create a new instance with the desired target and
migrate data as usual.

## Local and S3 instances coexist

One environment, one backup container, mixed targets:

```bash
pg create -i local01              # local repo (default)
pg create -i cloud01 --backup-target s3
```

```bash
pg backup list-stanza
# pgcli_local01   ← [global] repo1-path, local disk
# pgcli_cloud01   ← per-stanza repo1-type=s3 override
```

Retention, compression, and the shared stanza lifecycle (`stanza-create`,
`stanza-upgrade`, expiry) work identically for both — only the repository
bytes land in a different place. `pg backup setup`'s connectivity probe
prefers a stanza that really uses S3 whenever the repo is configured.

## Replicas: no backups until promoted

A replica — same-host or cross-host — gets **no** backup configuration: PITR
stays disabled while it streams from its primary, and archiving remains the
primary's job (the replica's WAL arrives via streaming replication, and PITR
on the primary covers it).

`pg replica promote <name>` decides the new primary's backup target
automatically — **before** calling `pg_promote()`:

| Situation | Result |
|-----------|--------|
| This host has a complete `backup.repo.s3` | promoted, backup target = **s3** |
| No S3 repo (same-host or cross-host replica) | promoted, backup target = **local** |
| `backup.repo.s3` present but incomplete | promotion **aborted** (fix the block, then re-run) |

So a cross-host replica with no S3 configured on this host behaves exactly
like a same-host one: it promotes to a **local**-backed primary and starts a
fresh backup chain here. Be aware that this new chain is unrelated to the old
primary's repository — the WAL that preceded the promotion lives in the *old*
host's repo, so you cannot PITR back through the failover point from this one.
If you want the promoted instance to continue on S3 instead, configure the repo
on this host first (the same `pg backup fetch-ca` +
`pg backup setup --s3-*` pair from the prerequisite section — the store is the
same remote machine), then promote and it resolves to `s3`:

```bash
pg backup setup --s3-endpoint 192.0.2.9:9000 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-secret-key '<secret>' \
    --s3-ca-file <path printed by fetch-ca>
pg replica promote ra2
```

After a successful promotion, stanza creation, `archive_command` wiring and
the PostgreSQL restart proceed exactly as the [failover doc](../failover/)
describes — with the target the table above resolved.

## Restore and PITR

Nothing to change: `pg restore --time ...` on an s3-target instance reads
backups and WAL from S3 through the same stanza, and pgBackRest fetches via
the per-stanza override. The temporary restore container gets the CA mount
automatically when `ca_file` is set. Private-CA stores work because the CA
certificate is bind-mounted read-only to `/etc/pgbackrest/ca.crt` in both the
PG container (archive-push) and the restore container — `pg` adds the mount
when creating containers, and `pg start` **recreates** an older PG container
that predates the mount (data lives in volumes, so the stop-rm-create is
lossless).

## See also

- [Backup](../backup/) — shared backup container, snapshots, the S3 repo block
- [Restore](../restore/) — PITR workflow and time formats
- [Failover](../failover/) — `pg replica promote` step by step
- [HA Cluster → S3 storage](../ha-cluster/ha-s3-storage/) — the Patroni equivalent
