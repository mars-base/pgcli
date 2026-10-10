---
title: "pgAdmin 4"
description: "Run pgAdmin 4 as a pgcli addon — the official PostgreSQL web administration UI, one container from the upstream dpage/pgadmin4 image, with an auto-generated web login and optional pre-seeded servers"
weight: 52
---

[pgAdmin 4](https://www.pgadmin.org/) is PostgreSQL's official web
administration UI — the browser console for browsing schemas, running SQL, and
watching servers. pgcli runs it as a **standalone, top-level addon** with the
same CLI surface as the other web/infra addons (install / start / stop / logs /
autostart / remove), managed entirely through `pg.yaml`.

It is the fleet's one *UI* addon, which shapes its design:

- **A web app, not a database front.** pgAdmin dials the servers *you* register
  in its own UI; it does not front one specific PostgreSQL instance the way
  [PostgREST](../postgrest/) or [PgBouncer](../pgbouncer/) do. That is why it is
  a top-level-only addon (`addons.pgadmin.<name>`) with no per-instance sidecar.
- **Its own login.** The web UI needs an email + password to sign in. That is
  **not** a PostgreSQL password — it is pgAdmin's own account, auto-generated on
  first install and stored in `pg.yaml`. Retrieve it later with
  `pg addon password pgadmin`.
- **Persistent session store.** pgAdmin keeps its config, saved servers, and
  browse history in a sqlite DB under a bind-mounted data dir. It survives
  `pg addon remove` (without `--clean-data`) and is revived on reinstall.
- **Optional one-time seed.** `--dsn` or `--pg-name <instance>` pre-registers
  one server in the UI's `servers.json` at install, so the browser opens with
  that server already listed. It is a convenience, not a runtime coupling —
  pgAdmin never blocks on it, and you can add/remove servers in the UI after.

> **Out of scope:** pgAdmin serves plain HTTP here — no TLS termination is wired
> in (put a reverse proxy in front if you need HTTPS). The seed `servers.json`
> **cannot** carry the target server's password (pgAdmin refuses to import one);
> you are prompted for it on first connect in the UI. See
> [Seeding a server](#seeding-a-server).

## Parameters

One container from the upstream `docker.io/dpage/pgadmin4` image (pull-only —
pgcli never builds it), configured entirely through `-e` env vars the entrypoint
reads. The table lists every knob and its default:

| Flag / `pg.yaml` key | Default | Env var | Meaning |
|----------------------|---------|---------|---------|
| `--image` / `image_tag` | `docker.io/dpage/pgadmin4:9.18` | — | override the tag verbatim |
| `--port` / `host_port` | auto from `pgadmin_start_port` (base **5050**) | `PGADMIN_LISTEN_PORT` | web host port |
| `--listen` / `listen` | `127.0.0.1` | `PGADMIN_LISTEN_ADDRESS` | bind address (`0.0.0.0` to expose) |
| `--email` / `email` | `admin@pgcli.lan` | `PGADMIN_DEFAULT_EMAIL` | web login account (validated — see [Troubleshooting](#troubleshooting)) |
| `--password` / `password` | generated, 20 chars | `PGADMIN_DEFAULT_PASSWORD` | web login password — **not** a PG password |
| `--dsn` / `dsn` | unset | — (renders `servers.json`) | one server to pre-register, given as a URI |
| `--pg-name` | unset | — (renders `servers.json`) | resolve that one server from a locally-managed instance instead |
| `--data-dir` / `data_dir` | `<base-dir>/addon/pgadmin/<name>/data` | (`/var/lib/pgadmin`) | where the session store lives |
| — | `autostart: false` | — | start on boot (`pg autostart enable --pgadmin`) |

How the knobs combine:

- **`email` + `password` only take effect on a fresh data dir.** pgAdmin reads
  `PGADMIN_DEFAULT_*` while it *initialises* `pgadmin4.db`; once that file
  exists the stored account is authoritative and the env vars are ignored. A
  reinstall that revives a kept dir therefore prints a password that is inert
  against it (the install says so) — to actually reset the login, remove with
  `--clean-data` first, or pin `--password` to the original when reviving.
- **`--dsn` and `--pg-name` are mutually exclusive** — both mean "seed this one
  server", one by URI and the other by resolving a local instance's endpoint.
  `--name` is the addon's own key and has nothing to do with `--pg-name`.
- **`listen` defaults to loopback** — the web UI is a login gate, so it stays on
  `127.0.0.1` unless you pass `--listen 0.0.0.0` to reach it from another host
  (the summary then warns that the login is the only gate).
- **`email` must survive the image's validator** — the entrypoint checks it with
  `email_validator` and rejects reserved TLDs (`.local`, `.invalid`, `.test`)
  outright, crash-looping on one. The default `admin@pgcli.lan` is chosen to
  pass; pick your own on the same basis (`.lan`, `.internal`-style names you
  control are fine — a real, deliverable address is not required, the
  deliverability check is off).

## Privileges

pgAdmin is the one addon worth spelling out here, because the naive reading
("the container writes to a bind mount as uid 5050, so pgcli must pre-chown the
host dir") is **wrong**, and pgcli deliberately does none of that ownership
machinery rustfs needs.

The `dpage/pgadmin4` entrypoint starts as **container root**, `chown`s
`/var/lib/pgadmin` to its own `pgadmin` user (uid/gid **5050**), then `su-exec`s
gunicorn down to it. So pgcli passes `--user 0` and gets out of the way:

- **No host-side chown.** pgcli never runs `chown`/`podman unshare chown` on
  your data dir. The dir is created `0755` by the install and the image fixes
  ownership from inside.
- **Rootless stays unprivileged.** Under rootless podman, `--user 0` is
  *mapped* — container root is your own host uid, and the 5050 the dir ends up
  owned by is the mapped subordinate uid. Nothing on the host runs as real root.
- **Teardown goes through `removeHostDir`.** Precisely because those files are
  mapped-uid, a plain host `rm -rf` of the data dir hits `EACCES` under
  rootless. `pg addon remove pgadmin --clean-data` falls back through
  `podman unshare rm` to reclaim them — the same path redis/rustfs use. (The
  e2e proves this both ways: the control `rm` must *fail* while `--clean-data`
  succeeds.)

The e2e asserts `.Config.User == 0` on the container and `5050:0` on
`/var/lib/pgadmin` inside it — which together show the *image* dropped
privileges, not pgcli.

## Install

```bash
# default: instance name "pgadmin", port auto from 5050, loopback, admin@pgcli.lan
pg addon install pgadmin

# a named console with your own web-login email
pg addon install pgadmin --name console --email me@example.com

# pre-seed the UI with one locally-managed instance
pg addon install pgadmin --name dev --pg-name proj01

# pre-seed from an arbitrary remote DSN
pg addon install pgadmin --name remote \
    --dsn "postgres://readonly@10.0.0.7:5432/appdb"
```

The install summary prints the URL and the (generated) web login:

```
✓ pgAdmin installed: "console"
  Container:  pgcli-pgadmin-default-console
  Image:      docker.io/dpage/pgadmin4:9.18
  Data:       ~/pg/addon/pgadmin/console/data
  URL:        http://127.0.0.1:5050/
  Seeded:     proj01

  Login email:    me@example.com
  Login password: <generated>
  (Web login only — not a PostgreSQL password. Retrieve it later with `pg addon password pgadmin`.)

  Add servers to browse in the web UI itself, or reinstall with --dsn/--pg-name to pre-seed one.
```

The `Seeded:` line appears only when `--dsn`/`--pg-name` was passed. Re-running
`install` is idempotent: a running container is left alone, a stopped one is
started. To apply a changed port/listen/email/seed, add `--force` (the container
is recreated from the config; the data dir is untouched).

## Using the web UI

Point a browser at the `URL` from the summary (on the host that runs podman),
sign in with the login email + password, and pgAdmin's own server tree appears.
Add connections in the UI, or pre-register one at install with
[--dsn / --pg-name](#seeding-a-server).

If the host isn't where your browser is, either set `--listen 0.0.0.0` (the
login is then the only gate) or keep it loopback and SSH-tunnel the port:

```bash
ssh -L 5050:127.0.0.1:5050 user@host
# then browse http://127.0.0.1:5050/ locally
```

### Retrieving the web login

```bash
pg addon password pgadmin --name console          # print bare, for scripting
pg addon password pgadmin --name console --file ./pw.txt   # mode 0600 file
```

`pg addon list` shows the URL and login email but **never** the password unless
you pass `--show-password`.

## Seeding a server

`--dsn` and `--pg-name` both render a one-time `servers.json` that pgAdmin
imports on first boot, so the browser opens with that server already in its
tree. `--pg-name` resolves the endpoint from a locally-managed instance (its
host/port/database/user, read from `pg.yaml`); `--dsn` gives it as a URI:

```bash
pg addon install pgadmin --name dev --pg-name proj01
# or
pg addon install pgadmin --name dev --dsn "postgres://app@127.0.0.1:5432/appdb"
```

What the seed does and does not carry:

- **The target server's password is NOT stored** — pgAdmin cannot import one,
  so you type it on first connect in the UI. pgcli's install says so explicitly
  ("its password is NOT carried"). This is also why the seed is safe to
  bind-mount: `servers.json` holds host/port/database/user only.
- **It's a one-time convenience, not a coupling.** After install, the server
  lives in pgAdmin's own `pgadmin4.db`; add, rename, or delete it in the UI. The
  addon keeps running regardless of whether the seed target is reachable.
- **`PGADMIN_REPLACE_SERVERS_ON_STARTUP=True`** is set whenever a seed exists,
  so a `--force` reinstall that changes `--dsn`/`--pg-name` re-applies the seed
  declaratively instead of leaving a stale entry.

To drop the seed and stop the declarative re-load, reinstall with `--force` and
neither `--dsn` nor `--pg-name`.

## Ports

Each instance takes **one port** from pgAdmin's own pool, `pgadmin_start_port`
(default **5050**) — a separate cursor from Redis's, Predixy's, and the object
stores' pools, so a pgadmin, a redis, and a minio instance never collide:

```bash
pg addon install pgadmin --name console   # 5050
pg addon install pgadmin --name audit     # 5051
```

`--port` fixes an instance to a specific port; auto-assignment skips anything
already explicit or live on the host.

## Configuration

Instances live under the top-level `addons.pgadmin` map in `pg.yaml`:

```yaml
namespace: default
pgadmin_start_port: 5050     # pgAdmin's own pool
addons:
  pgadmin:
    console:
      container_name: pgcli-pgadmin-default-console
      name: console
      image_tag: docker.io/dpage/pgadmin4:9.18
      # data_dir: /srv/pgadmin         # omit for <base-dir>/addon/pgadmin/console/data
      listen: 127.0.0.1                # default; 0.0.0.0 to expose on the network
      host_port: 5050
      email: me@example.com            # web login account (PGADMIN_DEFAULT_EMAIL)
      password: <generated>            # web login password; written on first install
      # dsn: postgres://app@127.0.0.1:5432/appdb   # set by --dsn/--pg-name: the seed
      # server_name: app               # display name of the seeded server
      autostart: false                 # pg autostart enable --pgadmin --name console
```

Edits to `listen`, `host_port`, `email`, `password`, `image_tag`, `dsn`/
`server_name`, or `data_dir` take effect after the next
`pg addon install pgadmin --name console --force` (or via the matching flags).
Remember the [Privileges](#privileges) caveat: `email`/`password` only re-apply
to an **empty** data dir, so changing them against an existing store needs
`--clean-data` first (or accept that the running account is the original one).

### List

```bash
pg addon list
```

```
Web add-ons (pgadmin):
  pgadmin (name: console)
    Status:      running
    URL:         http://127.0.0.1:5050/
    Login email: me@example.com
    Seeded:      proj01
    Data:        ~/pg/addon/pgadmin/console/data
    Image:       docker.io/dpage/pgadmin4:9.18
    Container:   pgcli-pgadmin-default-console
```

`pg addon list` never prints the password. Reveal it deliberately with
`pg addon list --show-password` (appends a `Login password:` line) or bare with
`pg addon password pgadmin --name console` — and prefer `--file` so the secret
stays out of shell history and scrollback.

## Start and stop

```bash
pg addon start pgadmin --name console
pg addon stop  pgadmin --name console
```

`start` only starts an existing container (and self-heals an improper state by
recreating it from the config). The data dir is preserved across stop/start, so
pgAdmin's saved servers and browse history come back with it.

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring instances up after a host reboot:

```bash
pg autostart enable --pgadmin --name console
```

pgAdmin autostart is **start-only** (install the instance first). On boot the
entrypoint re-chowns `/var/lib/pgadmin` and replays `pgadmin4.db` from the data
dir, so the saved servers survive the reboot. pgAdmin is independent of the
PostgreSQL stack — it is a browser UI that dials its servers itself — so there
is no ordering constraint and boot-time `pg start --autostart` starts it
alongside the other non-database addons.

## Remove

```bash
pg addon remove pgadmin --name console               # container + config entry; data kept
pg addon remove pgadmin --name console --clean-data  # also delete the data dir
```

Without `--clean-data` the host data dir (and its `pgadmin4.db`) stays in place
— reinstalling the same name revives the saved servers and session store (but
see the [Privileges](#privileges) note: the revived login is the original one,
not a freshly generated password). `--clean-data` deletes it, falling back
through `podman unshare rm` when rootless podman's mapped uid makes pgAdmin's
files undeletable by the host user. An empty per-instance parent directory is
pruned; a custom `data_dir` parent is never touched. `servers.json` (if seeded)
is host-owned and removed with the instance directory either way.

## Logs

```bash
pg logs addon pgadmin --name console        # last 50 lines
pg logs addon pgadmin --name console -f     # follow
```

## Troubleshooting

- **Signed in with the printed password and it's rejected** — you reinstalled
  over a kept data dir. The login that works is the one pgAdmin stored in
  `pgadmin4.db` on the *first* init; the newly generated one is inert (install
  warns "reviving an existing pgAdmin data dir"). Use `--clean-data` to start
  fresh, or `pg addon password pgadmin` to confirm what's stored, or reinstall
  with `--password` pinned to the original.
- **"pgAdmin could not start" / container exits immediately (or crash-loops)**
  — the entrypoint needs both `PGADMIN_DEFAULT_EMAIL` and
  `PGADMIN_DEFAULT_PASSWORD` on a fresh dir; a hand-edited `pg.yaml` with an
  empty password and a wiped data dir has neither. Let install generate one, or
  pass `--password`. It also **validates the email** and rejects reserved TLDs
  (`.local`, `.invalid`, `.test`, …) with *"does not appear to be a valid email
  address"* — the container then exits and, under the restart policy, spins.
  That is exactly why the default is `admin@pgcli.lan` and not `.local`.
- **Can't reach the UI from another machine** — `listen` defaults to
  `127.0.0.1`. Either SSH-tunnel the port (see [Using the web
  UI](#using-the-web-ui)) or reinstall `--listen 0.0.0.0 --force` and accept
  that the web login is then the only gate.
- **The seeded server shows but won't connect** — expected: `servers.json`
  never carries the target's password ([Seeding a server](#seeding-a-server));
  enter it in the connect prompt. Confirm the host/port are reachable from
  where pgAdmin runs.
- **Image not found at install** — `docker.io/dpage/pgadmin4` is pulled on
  demand; on an air-gapped host, `podman load` the tar first (catalog and export
  steps in `docs/images.md` in the repo) and the pull is skipped.
- **First boot is slow to answer** — pgAdmin runs its sqlite migrations before
  gunicorn binds; the port answers 302 (to `/login`) only once that finishes. On
  a loaded host this can take tens of seconds.

## Known limitations

- **No TLS** — pgAdmin serves plain HTTP here. For HTTPS put a reverse proxy
  (with `PGADMIN_URL_SCHEME`/`PGADMIN_DISABLE_STATIC_FILE_SERVER` tuning) in
  front; pgcli does not wire it. The loopback default keeps the common case on
  `127.0.0.1`.
- **Seed cannot carry the target password** — a pgAdmin constraint, not a pgcli
  one: `servers.json` passwords are not importable. The connection prompt
  happens in the UI.
- **Login only applies to a fresh store** — see
  [Privileges](#privileges)/[Troubleshooting](#troubleshooting); there is no
  in-place password reset, only `--clean-data` + reinstall.
- **macOS is code-complete, untested** — the bridge path (published ports,
  `proxyBindHost` widening loopback to `0.0.0.0` on the bridge) mirrors redis;
  it has not yet been exercised on a Mac.
