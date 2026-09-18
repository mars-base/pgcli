---
title: "Example: HA Cluster with a Self-CA MinIO"
description: "A complete, verified end-to-end walkthrough: a single-member Patroni cluster backed up to a MinIO addon serving a bring-your-own (self-signed) domain certificate, run in a fully isolated pgcli environment"
weight: 53
---

This page is a **worked example**, run end to end and verified: create a Patroni
HA cluster, stand up a MinIO addon that serves a **bring-your-own domain
certificate** (a self-signed CA of our own making — the same shape as a
public-CA or corporate-CA cert, just without buying one), and point the cluster's
pgBackRest backups and WAL archiving at it. Every command below is a real one
from the run; only hostnames, ports, passwords, and the certificate material are
genericized.

Two ideas make the example worth reading rather than skimming:

- **BYO TLS on the store.** The MinIO addon does not have to serve pgcli's
  generated self-signed pair — `--tls-cert` / `--tls-key` let it serve a cert you
  already hold. A client that trusts the issuing CA needs no extra material; with
  a private (here: self-signed) CA you hand that CA to the backup stack via
  `--s3-ca-file`, exactly as with the generated one. See
  [MinIO → Bring your own certificate](../addon/minio/#bring-your-own-certificate---tls-cert----tls-key).
- **Isolation via a second config file.** `backup.repo.s3` is a *single, global*
  repository shared by every stanza in one pgcli environment. Repointing it at a
  new store would silently redirect the backups of every existing cluster on that
  host too. The clean way to try something new against a live estate is a
  **separate config file** with its own `base_dir`, `namespace`, and port ranges —
  which is what this example does.

## The environment we built

| Piece | Value | Notes |
|-------|-------|-------|
| Config | `~/.pgcli-app1/pg.yaml` | separate from the production `~/.pgcli/pg.yaml` |
| Base dir | `/home/fish/bucket/pgcli-data-app1` | nothing touches the production data dir |
| Namespace | `app1` | prefixes every container name — `pgcli-minio-app1-store1`, `pgcli-patroni-app1-app1-nodea`, `pgcli-backup-app1` |
| DCS | existing etcd `m1` at `127.0.0.1:2379` | created on the production config long before this example — see [Prerequisite](#prerequisite--the-etcd-dcs); reused here as an *external* endpoint, and cluster membership is keyed by scope, so a new scope is naturally isolated |
| MinIO | `store1`, ports 9010/9011, listen `0.0.0.0` | BYO self-signed cert, SANs `minio1.test,127.0.0.1,<host-ip>` |
| Patroni | scope `app1`, member `nodea`, PG `<host-ip>:35632` | single member; the leader by definition |
| Backup container | `pgcli-backup-app1` | coexists with the production `pgcli-backup-default` |

## Prerequisite — the etcd DCS

The example reuses the etcd that already serves this host's production
clusters. If you are starting from scratch, that first piece to install is the
same addon, one member at a time — a lone member is a healthy one-node etcd
cluster, plenty for HA dev/test:

```bash
pg addon install etcd --name m1
# pgcli-etcd-default-m1, client 127.0.0.1:2379, peer 2380, cluster "pgcli-etcd"
```

(For members other hosts will join as a DCS, give it a reachable address at
install time — `--advertise-host <host-ip>` — so its peer/client URLs announce
that IP instead of loopback; and grow it to a real 3-node ensemble with
`--name m2`/`--name m3` the same way. None of that matters for this example:
the cluster lives on the same host and dials `127.0.0.1:2379`.)

## Step 0 — a domain certificate

Any PEM leaf + key works. For the demo we minted a self-signed one with a CFSSL
`generate_cert` helper (duration 1 year, SANs covering the name and IPs clients
will dial):

```bash
mkdir -p ~/.pgcli-app1/certs && cd ~/.pgcli-app1/certs
generate_cert -host "minio1.test,127.0.0.1,<host-ip>" -duration 8760h \
    # writes cert.pem + key.pem
mv cert.pem store1.crt && mv key.pem store1.key && chmod 600 store1.key

openssl x509 -in store1.crt -noout -subject -issuer -ext subjectAltName -dates
# subject=O = Acme Co
# issuer=O = Acme Co                     <- self-signed: leaf and root are one and the same
# DNS:minio1.test, IP Address:127.0.0.1, IP Address:<host-ip>
```

With a **public-CA** (or corporate-CA) cert this step is simply "you already
have the files" — point the flags at your `fullchain.pem` (leaf first, then
intermediates) and key, and the client-side story gets *easier*: chains rooted
in a trusted CA need no `--s3-ca-file` at all.

## Step 1 — an isolated environment

```bash
mkdir -p ~/.pgcli-app1
pg config init -o ~/.pgcli-app1/pg.yaml \
    --base-dir /home/fish/bucket/pgcli-data-app1 \
    --namespace app1 \
    --pg-start-port 36500 --pg-ssh-port 43500
```

`--namespace` makes every container name this config creates distinct, so two
environments live side by side in one podman. The config file is passed with
`-c` on every subsequent command — from here on, every `pg` in this page means
`pg -c ~/.pgcli-app1/pg.yaml`.

> **Port ranges.** The default port pools (etcd 2379, MinIO 9000, Patroni
> 35532, …) start at the same values as the production config, and podman
> publishes the loopback ports of a host-networked container — so pass explicit,
> disjoint ports everywhere rather than letting both environments auto-assign the
> same numbers.

## Step 2 — MinIO with the BYO cert

```bash
pg addon install minio --name store1 \
    --api-port 9010 --console-port 9011 --listen 0.0.0.0 \
    --tls-cert ~/.pgcli-app1/certs/store1.crt \
    --tls-key  ~/.pgcli-app1/certs/store1.key
```

```
-> MinIO BYO cert: CN="" issuer="" valid 2026-09-18 → 2027-09-18
  [OK] TLS certs (BYO: /home/…/.pgcli-app1/certs/store1.crt, valid 2026-09-18 → 2027-09-18)
         self-signed: clients pin the issuing CA (pg backup setup --s3-ca-file <ca.pem>)
  [OK] MinIO container started
```

Install-time validation pairs the cert and key (a mismatched, expired,
CA-instead-of-leaf, or non-server-auth pair fails right here), prints the
validity window, and — because the cert is self-signed — tells you clients will
pin it as their trust anchor. `--listen 0.0.0.0` rather than loopback: the
backup container reaches the store over host networking via the host IP, and
that IP is one of the SANs. A quick proof it serves *your* certificate:

```bash
curl -s --cacert ~/.pgcli-app1/certs/store1.crt \
     https://127.0.0.1:9010/minio/health/live -o /dev/null -w "%{http_code}\n"
# 200  — and curl verified the chain against exactly the cert we supplied
```

Create the repository bucket (pgBackRest does not create it):

```bash
pg mc alias set store1 https://127.0.0.1:9010 admin '<root-password>' -- --insecure
pg mc mb store1/pgbackrest
```

## Step 3 — the Patroni cluster

```bash
pg ha create app1 --member nodea \
    --etcd-endpoints 127.0.0.1:2379 \
    --host-port 35632 --restapi-port 8028 \
    --advertise-host <host-ip>
```

```
!  No S3 backup repo configured on this host (backup.repo.s3): the member will
   be created WITHOUT WAL archiving ... Configure a repo and run `pg backup
   setup` to wire archiving in (it recreates members as needed).
  [OK] Patroni member nodea started
✓ Patroni member "nodea" installed in scope "app1"
  scope (DCS):  app1-app1        # namespace-qualified: "app1-" + scope "app1"
  pg:           <host-ip>:35632
```

The warning is the designed order of operations, not a problem: at create time
no repo exists yet, so the member comes up without archiving; the `backup
setup` in the next step configures the repo and **recreates the member** with
archiving enabled. Note the DCS scope is `app1-app1` (namespace prefix + scope)
— pass the *plain* scope (`app1`) to every `pg ha` command and pgcli resolves
the qualified form; it also keeps the stanza distinct from any `app1` another
namespace might run on the same bucket.

Seed something worth backing up (this also proves client auth over scram from
the host):

```bash
pg ha exec app1 "CREATE TABLE IF NOT EXISTS byo_probe(id serial primary key,
    note text, ts timestamptz default now());
    INSERT INTO byo_probe(note) SELECT 'row-'||g FROM generate_series(1,500) g;"
pg ha exec app1 "SELECT count(*) FROM byo_probe"   # 500
```

## Step 4 — point the backup stack at the BYO store

```bash
pg backup setup \
    --s3-endpoint <host-ip>:9010 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-secret-key '<root-password>' \
    --s3-ca-file ~/.pgcli-app1/certs/store1.crt
```

`--s3-ca-file` takes our *own* cert as the trust anchor — for a self-signed
cert the leaf embeds its own root, so the file you passed to `--tls-cert` *is*
the CA file. pgBackRest passes the bundle to its verifier with no content
inspection, so a public-CA cert needs no flag at all and a private-CA one just
gets its chain here. The run then proves the full stack and rewires the member:

```
  [OK] <host-ip>:9010 reachable
  [OK] repo CA published to 1 cluster registry/registries (from …/store1.crt)
  [OK] repository reachable                      # a repo-ls over TLS: creds + CA + bucket all good
  [stale] app1/nodea: mounts the backup-side pgbackrest.conf (pg1-host breaks archive-push)
-> Pausing cluster app1 (no auto-failover during recreate)
  -> Recreating member nodea...                  # now with archive-push enabled
  [OK] app1: archive-ready after recreating 1 member(s)
  [OK] stanza pgcli_app1-app1 created
  [OK] check pgcli_app1-app1
```

The "repo CA published to the cluster registry" line is the etcd-distribution
path: every *other* member of this scope (or future ones, like a second host)
picks the CA up from the DCS with no manual scp and no extra flags.

## Step 5 — back it up, and prove it landed

```bash
pg ha snapshot create app1 --type full
#   Name:    20260918-091519F
#   Type:    full
pg ha snapshot list app1                          # pgBackRest sees the backup in the repo
```

The independent proof is that the objects are physically in the bucket, written
over a TLS session terminated by **our** certificate:

```bash
pg mc ls store1/pgbackrest -- --recursive
# …/backup/pgcli_app1-app1/20260918-091519F/backup.manifest
# …/backup/pgcli_app1-app1/20260918-091519F/pg_data/base/…  (.zst files)
# …/archive/pgcli_app1-app1/18-1/0000000200000000/000000020000000000000005-….zst
# …/archive/pgcli_app1-app1/18-1/0000000200000000/000000020000000000000005.00000028.backup
# …/archive/pgcli_app1-app1/archive.info
```

`archive/` is continuous **WAL archiving** (the `archive-push` the recreated
member runs), `backup/` is the full snapshot. Confirm the archiver is healthy:

```bash
pg ha exec app1 "SELECT archived_count, failed_count, last_archived_wal
                 FROM pg_stat_archiver"
```

One subtlety from our run, worth knowing so you don't chase a ghost:
`failed_count` was `1`, for `00000002.history` — the timeline-history file,
archived one second *later* successfully. It raced the brief window in which
`backup setup` was pausing and recreating the member. A lone recent failure
during a setup/recreate is self-healing; a *growing* count with the same WAL
name is a real repo problem.

## What the example demonstrates

- A MinIO addon serving a **bring-your-own** certificate works as a pgBackRest
  repository end to end: stanza-create, `check`, full backup, and continuous
  WAL archiving all flow over TLS verified against the supplied cert.
- Self-signed is not a lesser path: the same `--s3-ca-file` slot carries it
  (leaf==root), a public CA's chain, or an internal CA bundle — the transport
  and the trust plumbing are identical.
- **`backup.repo.s3` is one global repo per config file.** To experiment
  beside a running estate, don't repoint it — init a second config
  (`pg config init --namespace … --base-dir …`) and run the whole experiment
  there. Namespace-qualified container names, DCS scopes, and stanzas keep the
  two environments from ever seeing each other.

## Teardown

Everything the example created lives under the app1 config, so teardown is
ordered through that same `-c` (data directories deleted explicitly):

```bash
pg -c ~/.pgcli-app1/pg.yaml ha remove app1 --member nodea --clean-data
pg -c ~/.pgcli-app1/pg.yaml backup remove --clean-data
pg -c ~/.pgcli-app1/pg.yaml addon remove minio --name store1 --clean-data
rm -rf ~/.pgcli-app1 /home/fish/bucket/pgcli-data-app1
```

The etcd `m1` belonged to the production environment — it is untouched by all
of the above, which is exactly the point of the isolation.

## Related

- [Patroni Cluster Backup](../ha-backup/) — the backup machinery in depth
- [MinIO](../addon/minio/) — the addon, and its
  [BYO certificate](../addon/minio/#bring-your-own-certificate---tls-cert----tls-key) section
- [Namespace Isolation](../../namespace/) — how `--namespace` scopes names
- [Patroni HA](../ha/) — the `pg ha` command set
