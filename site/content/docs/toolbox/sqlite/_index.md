---
title: "SQLite + Litestream"
description: "Manage host-local SQLite databases and replicate WAL changes to S3-compatible storage with Litestream"
weight: 30
icon: fa-solid fa-database
aliases: ["/toolbox/sqlite/"]
menus:
  main:
    identifier: docs-toolbox-sqlite
    parent: docs-toolbox
    weight: 30
    params:
      icon: fa-solid fa-database
cascade:
  type: docs
  footer_style: slim
---

`pg sqlite` manages SQLite databases directly on a Linux host and can replicate
their WAL changes to S3-compatible object storage with
[Litestream](https://litestream.io/). SQLite runs as a normal host process — there
is no container, no Go SQLite driver and no cgo. The `sqlite3` CLI executes all
SQL; Litestream runs as one detached process per database.

Replication is asynchronous (roughly one second of RPO with the default sync
interval). It is continuous backup, not high availability: there is still a
single writer, and a failover means restoring a backup somewhere else.

Only Linux is supported.

## How it works

- Every managed database has an entry in `pg.yaml` and a per-name state
  directory under `<base>/sqlite/<name>/`.
- `pg sqlite create` enables `journal_mode=WAL`, which is persistent in the
  database header.
- `pg sqlite replication enable` renders a dedicated `litestream.yml` (mode
  `0600`) and starts `litestream replicate` in the background. Litestream
  tails the WAL and uploads LTX objects to the configured bucket.
- S3 credentials are written **only** to that `litestream.yml`. They are never
  stored in `pg.yaml` and are never printed by any `pg sqlite` command.

## Install

```bash
pg sqlite install
```

The command is idempotent and installs two host binaries:

1. `sqlite3` from the distribution package manager if it is not already on
   `PATH` (apt-get, dnf, yum, pacman, zypper or apk). Only this step may need
   root; pgcli uses passwordless `sudo -n` and fails with a clear message
   instead of prompting for a password.
2. Litestream `0.5.17`, downloaded from GitHub and verified against the
   published SHA256 checksum. It goes to `/usr/local/bin` when writable,
   otherwise to `~/.local/bin`.

Useful flags:

```bash
pg sqlite install --install-dir /opt/bin      # choose where Litestream goes
pg sqlite install --force-litestream          # re-download a supported version
```

Litestream 0.3.x is not compatible with the generated configuration; the
installer ignores older binaries on `PATH` and installs 0.5.x.

## Create and manage databases

```bash
# Default path: <base>/sqlite/appdb/appdb.db
pg sqlite create appdb
pg sqlite create analytics --path /srv/data/analytics.db

pg sqlite list
pg sqlite info appdb
pg sqlite info appdb --integrity full

pg sqlite exec appdb "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"
pg sqlite exec appdb "INSERT INTO items (name) VALUES ('first')"

# Read SQL from stdin (no command-line length limit)
pg sqlite exec appdb - <<'SQL'
INSERT INTO items (name) VALUES ('second');
INSERT INTO items (name) VALUES ('third');
SQL

pg sqlite shell appdb
pg sqlite shell appdb -- -header -column
```

`create` never truncates an existing file: pointing it at an existing SQLite
database adopts the file, enables WAL and leaves the contents untouched.
`--path` must be absolute and only moves the database file — configuration,
logs, pid and local Litestream state always stay under
`<base>/sqlite/<name>/`.

`info` reads `journal_mode`, page size/count and freelist, then runs
`PRAGMA quick_check` by default. Use `--integrity full` for a complete
`integrity_check`, or `--integrity none` to skip it on large databases.

`remove` unregisters the database and stops replication but leaves the database
file in place. Add `--clean-data` to delete the database, WAL sidecars, local
Litestream state and logs (interactive confirmation; `--force` skips it).
Remote S3 backups are never deleted automatically.

## Enable S3 replication

The bucket must already exist — Litestream does not create buckets. Create it
with `mc mb`, the cloud console, or another S3 tool first.

### Local MinIO for testing

pgcli can host the object store itself with the MinIO addon:

```bash
# Install a named MinIO instance; the output prints the S3 API endpoint.
# The default endpoint is http://127.0.0.1:9000 when the port is free.
pg addon install minio --name sqlite-s3

# The root user defaults to "admin"; the password is generated at install.
S3_SECRET=$(pg addon password minio --name sqlite-s3)

# Register the endpoint with the bundled mc wrapper and create the bucket.
pg mc alias set local http://127.0.0.1:9000 admin "$S3_SECRET"
pg mc mb local/sqlite-backups
```

If port 9000 was already taken, use the endpoint reported by
`pg addon list` instead. Then point replication at the local store:

```bash
pg sqlite replication enable appdb \
  --s3-endpoint http://127.0.0.1:9000 \
  --s3-bucket sqlite-backups \
  --s3-access-key admin \
  --s3-secret-key "$S3_SECRET" \
  --s3-uri-style path
```

Any other MinIO or S3-compatible store uses the same flags with its own
endpoint and credentials:

```bash
pg sqlite replication enable appdb \
  --s3-endpoint http://127.0.0.1:9000 \
  --s3-bucket sqlite-backups \
  --s3-access-key minioadmin \
  --s3-secret-key minioadmin \
  --s3-uri-style path
```

AWS S3 with the default credential chain (no keys on the command line):

```bash
pg sqlite replication enable appdb \
  --s3-bucket my-org-sqlite-backups \
  --s3-region eu-west-1 \
  --s3-uri-style virtual-host
```

| Flag | Default | Notes |
|---|---|---|
| `--s3-bucket` | required | Must already exist |
| `--s3-endpoint` | AWS default | Complete `http(s)://host[:port]`, no path or credentials |
| `--s3-path` | `sqlite/<name>` | Object key prefix; a leading `/` is stripped |
| `--s3-region` | `us-east-1` | |
| `--s3-access-key` / `--s3-secret-key` | default chain | Provide both or neither |
| `--s3-uri-style` | `path` | `path` or `virtual-host` |
| `--s3-ca-file` | none | Passed to Litestream as `SSL_CERT_FILE` for private CAs |
| `--s3-skip-verify` | false | Disable TLS verification (self-signed endpoints) |
| `--sync-interval` | `1s` | WAL upload interval |
| `--snapshot-interval` | `24h` | Full snapshot interval |
| `--retention` | `24h` | Snapshot retention |
| `--no-start` | false | Write configuration without starting Litestream |
| `--force` | false | Replace an existing configuration |

`enable` verifies that the database is in WAL mode, resolves a 0.5.x
Litestream binary (or uses `--litestream-binary PATH`), writes `litestream.yml`
with mode `0600`, clears stale local Litestream state, starts the process and
checks that it survives startup. If it exits immediately, the last log line is
printed and the command fails.

Running `enable` again with identical non-secret settings while replication is
running is a no-op. Different settings are rejected unless `--force` is given;
`--force` stops the running process, rewrites the configuration, resets local
state and restarts.

### Where the credentials live

| Path | Contents | Mode |
|---|---|---|
| `<base>/pg.yaml` | name, path, binary/version, endpoint, bucket, prefix, region, URI style, CA file, skip-verify — **no keys** | 0644 |
| `<base>/sqlite/<name>/<name>.db` | database file | app default |
| `<base>/sqlite/<name>/litestream.yml` | full S3 configuration **including access/secret keys** | 0600 |
| `<base>/sqlite/<name>/litestream.log` | process log | 0644 |
| `<base>/sqlite/<name>/litestream.pid` | running pid | 0644 |
| `<base>/sqlite/<name>/.<db>-litestream/` | local LTX state | app default |

Back up or copy `litestream.yml` with the same care as any credential file.
`replication disable` removes it, so credentials must be supplied again on the
next `enable`.

## Operate replication

```bash
pg sqlite replication status appdb
pg sqlite replication status              # all databases
pg sqlite replication logs appdb -n 100
pg sqlite replication logs appdb -f
pg sqlite replication stop appdb          # final sync, up to 30s
pg sqlite replication start appdb
pg sqlite replication disable appdb       # stop + remove litestream.yml
```

`status` combines the process state with `litestream status -json` (local
transaction id and WAL size) and shows the newest error line from the log.
If the status query fails, the command still reports process state and exits
successfully.

### Check remote backups

`pg mc` wraps the bundled MinIO client; its flags must come after `--`. Using
the alias created in the Local MinIO example, list what Litestream has uploaded
under the configured `--s3-path`:

```bash
pg mc ls local/sqlite-backups/sqlite/appdb -- --recursive
```

A populated prefix looks like this:

```text
0000/0000000000000001-0000000000000001.ltx
0000/0000000000000002-0000000000000002.ltx
0001/0000000000000001-0000000000000002.ltx
0009/0000000000000001-0000000000000002.ltx
```

`0000/` holds per-transaction WAL frames; higher-numbered levels (`0001/`,
`0009/`, ...) are merged and compacted files. An empty prefix right after
`enable` only means no write has been synced yet — insert a row, wait at least
one `--sync-interval` (default `1s`), then list again. The local transaction id
shown by `replication status` should match the highest txid in these names.

`disable` keeps the database, local LTX state and logs, but removes the
replication block from `pg.yaml` and deletes `litestream.yml`. The remote
backups remain untouched.

## Auto-start on boot

Litestream runs as a detached host process, so it does not survive a reboot on
its own. Register the database with the pgcli boot service:

```bash
pg autostart enable --sqlite --name appdb
pg autostart status
```

`--name` is required (SQLite has no default database name). Replication must
already be enabled and its `litestream.yml` must still exist — the toggle checks
both and fails with the usual re-enable hint otherwise. At boot,
`pg start --autostart` restarts Litestream after the object-storage addons, so
a same-host MinIO/silo/rustfs/garage/SeaweedFS backend is listening first. The
boot path is start-only: it never rewrites `litestream.yml` and never touches
the database file.

```bash
pg autostart disable --sqlite --name appdb
```

`pg sqlite replication disable` clears this flag automatically. See
[Auto-start on Boot](/docs/autostart/) for the systemd/launchd service details.

## Restore

Litestream can restore the latest state or a point in time. Two modes are
available:

```bash
# Restore a copy to another path; replication keeps running
pg sqlite replication restore appdb --target /tmp/appdb-restored.db

# Point in time by RFC3339 timestamp (UTC with a `Z` suffix)
pg sqlite replication restore appdb --target /tmp/appdb-old.db \
  --timestamp 2026-01-01T12:00:00Z --integrity full

# Point in time by Litestream transaction id (hex; 0x prefix optional)
pg sqlite replication restore appdb --target /tmp/appdb-tx.db --txid 00000000000000ff

# Show what would be restored without writing anything
pg sqlite replication restore appdb --dry-run
```

`--timestamp` and `--txid` are mutually exclusive; omitting both restores the
latest state. `--timestamp` is an absolute instant: write it in **UTC with the
`Z` suffix** (for example `2026-01-01T12:00:00Z`) so the restore point does not
depend on the host timezone. `--target` cannot be combined with `--restart`,
and an existing target requires `--force`, which first moves the old target and
its `-wal`/`-shm` files into `<target>.pre-restore-<UTC>/`.

### Point-in-time recovery walkthrough

Capture a transaction boundary before making the change you want to undo:

```bash
# Current local transaction id (the `local: ... txid=...` line)
pg sqlite replication status appdb

# Or inspect the uploaded LTX ranges in S3
pg mc ls local/sqlite-backups/sqlite/appdb -- --recursive
```

LTX object names are `<min-txid>-<max-txid>.ltx`. A restore by `--txid` includes
the state up to and including that transaction. Record the txid (or an RFC3339
UTC timestamp taken safely before the change), then restore a copy and verify
it:

```bash
pg sqlite replication restore appdb \
  --target /tmp/appdb-pitr.db \
  --txid 0000000000000004 \
  --integrity full

sqlite3 -readonly /tmp/appdb-pitr.db \
  "SELECT COUNT(*) FROM items; PRAGMA journal_mode;"
```

The timestamp form follows the same flow:

```bash
pg sqlite replication restore appdb \
  --target /tmp/appdb-pitr-ts.db \
  --timestamp 2026-01-01T12:00:00Z \
  --integrity full
```

Use `--dry-run` first when unsure: it prints the transaction range that would
be used and writes nothing. Restoring to `--target` does not stop replication,
so traffic can continue while you inspect the copy. As a sanity check, restore
the latest state (no `--timestamp`/`--txid`) and confirm that newer rows are
present; only then promote the verified copy with an in-place restore.

### In-place restore

Without `--target`, the configured database is replaced:

```bash
pg sqlite replication restore appdb --force
```

The sequence is deliberately conservative:

1. Litestream is stopped (a final sync runs) and other applications must have
   been stopped manually first.
2. The current database and its WAL sidecars are moved to
   `<db>.pre-restore-<UTC>/` (confirmation unless `--force`).
3. The backup is restored into a temporary directory next to the database, so
   a failed restore never touches the preserved copy.
4. The restored database is checked and switched to WAL if necessary, then
   moved into place with an atomic rename on the same filesystem.
5. Local Litestream state is reset. If reset fails, the stale state directory
   is removed.
6. Replication stays stopped by default. `--restart` starts it again.

Restarting replication into the same `--s3-path` after a restore can mix the
restored timeline with the old remote data. Prefer re-enabling with a new
`--s3-path`, or make sure you understand the old timeline before using
`--restart`.

If the database file is lost entirely, in-place restore works without
`--force`; the per-database `litestream.yml` must still exist.

## WAL and application settings

Only `PRAGMA journal_mode=WAL` is persisted in the database file by the CLI.
Tune the following per connection from the application:

```sql
PRAGMA busy_timeout=5000;   -- wait for locks instead of failing immediately
PRAGMA synchronous=NORMAL;  -- recommended with WAL (durability trade-off)
PRAGMA wal_autocheckpoint=1000;
```

Keep in mind:

- SQLite in WAL mode requires a local filesystem. Do not put the database on
  NFS or SMB.
- There is still one writer at a time. Long write transactions block other
  writers; long read transactions prevent WAL checkpointing and can make the
  WAL grow without bound.
- Litestream replicates the WAL, not an application-consistent snapshot. Use
  transactions for multi-statement invariants.

## Troubleshooting

**`litestream exited during startup`** — run
`pg sqlite replication logs <name> -n 50`. The usual causes are a missing
bucket, wrong credentials or an endpoint that is not reachable from the host.

**`no Litestream 0.5.x binary found`** — run `pg sqlite install`. A 0.3.x
binary on `PATH` is reported but not used.

**TLS errors against MinIO/RustFS** — use the full `http://host:port` endpoint
for plain HTTP, or `--s3-ca-file` / `--s3-skip-verify` for self-signed HTTPS.

**`database ... uses journal_mode=delete`** — the database was not created by
`pg sqlite create`, or another tool changed the mode. Use
`PRAGMA journal_mode=WAL;` with no other connections open, then retry `enable`.

**`litestream.yml not found`** — `start`, `status` and `restore` require the
configuration written by `enable`; re-run `enable` (credentials are not stored
in `pg.yaml`).

## Limits

- Linux only; the macOS host-binary paths are not implemented.
- Litestream is asynchronous backup, not synchronous replication or HA. The
  default RPO is about one second, and a final sync on `stop`/`disable` can take
  up to 30 seconds.
- One Litestream process and one WAL writer per database.
- `--clean-data` and in-place restore are destructive; they prompt by default,
  and the preserved `.pre-restore-*` directories are left for manual cleanup.
