---
title: "HTTP File Server"
description: "Share a directory over HTTP(S) with an autoindex-style listing and a copy button for every file"
weight: 55
icon: fa-solid fa-share-nodes
aliases: ["/toolbox/http/"]
menus:
  main:
    identifier: docs-toolbox-http
    parent: docs-toolbox
    weight: 55
    params:
      icon: fa-solid fa-share-nodes
cascade:
  type: docs
  footer_style: slim
---

`pg http` starts a small static file server directly on the host — no container
— for sharing files between machines. Directories render an nginx-style
autoindex page with a copy button next to every file, and files are served with
`Range`/`HEAD` support. The server is read-only by default; `--allow-delete`
adds a delete button per file, `--allow-upload` adds an upload form that writes
into the listing's directory, and `--allow-dir` adds a "new folder" form.
`--admin-ip` optionally restricts all three write modes to a list of source IPs,
and `--auth` / `--auth-env` put the whole site behind HTTP Basic Auth.

The default is `127.0.0.1:18080` serving the current directory. Servers run as
detached processes, so they survive the terminal that started them; several
servers can run at once.

```bash
pg http start
pg http status
pg http restart <NAME>
pg http stop
pg autostart enable --http --name <NAME>
```

## Start a server

```bash
pg http start [--name NAME] [--port 18080] [--listen 127.0.0.1] \
  [--dir .] [--tls] [--tls-cert FILE --tls-key FILE] \
  [--allow-delete] [--allow-upload] [--allow-dir] [--max-upload SIZE] \
  [--admin-ip IP[,IP...]] [--auth USER:PASSWORD]... [--auth-env VAR]...
```

| Flag | Default | Notes |
|---|---|---|
| `--name` | `http-<requested-port>` | Name used by `status`/`stop`. If a live server already has it, `-2`/`-3` is appended |
| `--port` | `18080` | If the port is busy, the next free one is tried automatically (up to 100) |
| `--listen` | `127.0.0.1` | Use `0.0.0.0` to expose the server on the network |
| `--dir` | `.` | Directory to serve; resolved to an absolute path at start and must exist |
| `--tls` | off | Serve HTTPS with a generated self-signed certificate |
| `--tls-cert` / `--tls-key` | — | Use an existing certificate/key pair instead (implies `--tls`) |
| `--allow-delete` | off | Add a delete button per file and accept HTTP `DELETE`. Combine with `--auth` — see Security |
| `--allow-upload` | off | Add an upload form and accept multipart `POST` into the directory being listed. Combine with `--auth` — see Security |
| `--allow-dir` | off | Add a "new folder" form that creates directories from a relative path. Combine with `--auth` — see Security |
| `--max-upload` | `1G` | Maximum size of one upload request, all files combined (`512M`, `2G`, or a byte count) |
| `--admin-ip` | empty | Comma-separated source IPs allowed to use the write modes. Empty means no source restriction. Other sources get a read-only listing and `403` on writes |
| `--auth` | empty | Require HTTP Basic Auth. `USER:PASSWORD`, repeat the flag for several users; the password may contain `:` |
| `--auth-env` | empty | Read `USER:PASSWORD` from this environment variable, repeatable. Useful to keep passwords out of shell history |

`--name` accepts letters, digits, `.`, `_` and `-`.

Output:

```
✓ HTTP server started: http-18080
  URL:  http://127.0.0.1:18080/
  Dir:  /srv/share
  PID:  412345
  Log:  /root/.pgcli/http/servers/http-18080.log

  Stop with: pg http stop http-18080
```

With `--listen 0.0.0.0` the printed URL contains `0.0.0.0`; clients should use
the host's actual address instead.

### Multiple servers and port auto-increment

Every `pg http start` is an independent server. If the requested port is taken,
pgcli reports it and binds the next free port:

```bash
pg http start --dir /srv/share          # 18080
pg http start --dir /srv/docs           # 18081, same host
```

```
  [!] port 18080 is in use, using 18081
```

A name collision with a still-running server is resolved the same way, by
appending a numeric suffix.

### TLS

`--tls` generates a self-signed certificate with the same code as `pg cert`:

- Stored under `<base>/http/tls/<name>/cert.pem` (0644) and `key.pem` (0600).
- SANs cover loopback plus the given `--listen` address; for a wildcard
  listener (`0.0.0.0`/`::`) they cover every local interface address.
- The pair is reused while it is valid for more than 30 days and still covers
  the listener, and regenerated otherwise.

The certificate is its own trust anchor (no separate CA), so clients verify it
with `--cacert`:

```bash
curl --cacert ~/.pgcli/http/tls/http-18080/cert.pem \
  https://127.0.0.1:18080/
```

To use a certificate from `pg cert` or another tool, pass `--tls-cert` and
`--tls-key`. The pair is validated for matching key, validity window, and is
used in place (not copied). Include every address clients will use in the
certificate's SANs.

### Basic Auth

`--auth USER:PASSWORD` requires HTTP Basic Auth for the **whole site**:
listings, downloads and the write modes all answer `401` with a
`WWW-Authenticate` challenge until valid credentials arrive, so browsers show
their native login prompt. Repeat the flag for several users:

```bash
pg http start --auth alice:s3cret --auth bob:pa:ssword --dir /srv/share
```

To keep a password out of shell history and `ps` output, put it in an
environment variable and name the variable with `--auth-env` (the value has the
same `USER:PASSWORD` form; an unset or empty variable is an error):

```bash
export PG_HTTP_AUTH='carol:pw'
pg http start --auth alice:s3cret --auth-env PG_HTTP_AUTH --dir /srv/share
```

- The first `:` separates the user name from the password; the password itself
  may contain `:`. Empty user names or passwords and duplicate users (whether
  from `--auth` or `--auth-env`) are rejected at start.
- Passwords are turned into bcrypt hashes when the server starts. Only the
  hashes are written to the state file and to `pg.yaml`, and only hashes are
  passed to the detached `pg http serve` child — the plain password never
  appears in the state or the process list. Passwords are limited to the
  bcrypt maximum of 72 bytes.
- `/__pgcli_health` stays reachable **without** credentials so readiness probes
  keep working.
- `restart` and autostart reuse the recorded hashes, so they need neither the
  original flags nor the environment variable. To change a password, stop the
  server and start it again with the new `--auth`/`--auth-env` values.
- Basic Auth is cleartext without TLS: over plain HTTP anyone on the path can
  read the credentials, and `pg http start` prints a warning when `--auth` is
  used on a non-loopback listener without `--tls`. Combine the two when the
  server leaves the host.
- Browsers cache Basic credentials per origin; after a password change they
  only re-prompt once the server rejects the cached credentials, which a
  browser restart forces. There is no logout.

## Inspect, restart and stop

```bash
pg http status
```

```
NAME                     PID      STATUS    AUTOSTART DELETE  UPLOAD  MKDIR   AUTH                 ADMIN             URL                                      DIR
http-18080               412345   running   -         -       -       -       -                    all               http://127.0.0.1:18080/                  /srv/share
http-18081               412410   running   enabled   enabled enabled enabled alice,bob            192.0.2.10,192.0.2.11 https://0.0.0.0:18081/              /srv/docs
```

`AUTOSTART` shows whether the server is recorded in `pg.yaml` for boot
autostart (see below); `DELETE`, `UPLOAD` and `MKDIR` show which write modes
it was started with, `AUTH` lists the Basic Auth users it accepts (`-` when the
site is open), and `ADMIN` lists the source IPs allowed to use the write modes
(`all` when unrestricted). `stop` prints a reminder for autostart servers,
since they come back at the next boot.

A state file whose process is gone is marked `stale`; the next `stop` removes
it.

Restart stops the server gracefully like `stop`, then starts it again from the
name, listen address, directory, TLS certificate, write modes
(delete/upload/mkdir), upload size limit, admin-IP restriction, Basic Auth
users (bcrypt hashes) and port recorded in its state file —
no need to repeat the `start` flags. If the original port has been taken in the meantime, the
next free port is used; a generated certificate is re-signed when it is close
to expiry or no longer covers the listen address:

```bash
pg http restart http-18080        # one server
pg http restart http-18080 docs   # several
pg http restart                   # no names = every server
pg http restart --all
```

```bash
pg http stop http-18080           # one server
pg http stop http-18080 docs      # several
pg http stop                      # no names = every server
pg http stop --all
```

Stop sends `SIGTERM` and waits up to `--timeout` (default `5s`, matching the
server's graceful-shutdown window) before `SIGKILL`. State files are removed;
the log and certificate stay on disk. `restart` accepts the same `--timeout`
and `--all` flags.

## Directory listings

- Directories are listed before files, then case-insensitively by name.
  Directory links carry a trailing slash; breadcrumbs and a `..` link (except
  at the root) navigate upward.
- An `index.html` is **not** served automatically — a directory always
  renders its listing.
- Only `GET` and `HEAD` are allowed by default; other methods get `405`.
  `--allow-delete` allows `DELETE` for files, and `--allow-upload` or
  `--allow-dir` allows `POST` on directories. The `Allow` header on a `405`
  lists the method combination actually enabled.
- `/__pgcli_health` returns `200 ok` for readiness probes and shadows a real
  file of that name. It never requires Basic Auth credentials. It is never
  deletable, cannot be overwritten by an upload, and its name cannot be used
  for a root-level directory.
- Symlinks are followed, including ones pointing outside the served directory.
  A symlink to a directory is listed as a directory, and `DELETE` refuses it
  like any other directory.

Every file row has a **copy** button. Clicking it copies the file's absolute
URL (`http(s)://host:port/path`) and shows `copyed` next to the button for
about 2 seconds. The Clipboard API is only available in secure contexts, so
over plain HTTP the page falls back to a hidden textarea and
`document.execCommand('copy')`; both paths work in browsers that still support
execCommand.

### Deleting files

With `--allow-delete`, every file row also has a **delete** button to the right
of copy. The first click turns it into `confirm?` (red) for three seconds; a
second click sends `DELETE /path`, removes the row from the page, and unlinks
the file. The button reverts to `delete` if not confirmed in time.

- Directories are never deleted, including empty ones; `DELETE` on a directory
  returns `405`.
- A symlink is unlinked itself — its target file is untouched. A path that
  resolves outside `--dir` through a symlinked directory is rejected with
  `403`.
- A successful delete returns `204 No Content`; a missing file returns `404`.

`pg http restart` preserves the delete/upload/mkdir modes recorded in the state
file. To change them, stop the server and start it again with the flags you
want.

### Uploading files

With `--allow-upload`, every listing gets an upload form above the table and
accepts multiple files at once. Files land in the directory shown in the
`Directory listing for <path>` heading — whichever directory you are browsing
is the destination, and missing subpaths are not created for you.

- `curl -F files=@a.txt -F files=@b.txt http://host:port/sub/` is the
  equivalent of uploading on the `/sub/` page.
- One request (all files combined) is capped at 1 GiB by default and adjustable
  with `--max-upload`; larger requests get `413` with the configured limit in
  the message. The page adds up the selected file sizes before submitting and
  refuses an over-limit selection without sending any request.
- An existing file is **atomically overwritten**: the content is streamed to a
  hidden temporary file in the same directory and renamed over the target, so
  an interrupted upload never corrupts the old file. A directory target
  (including a symlink to one) returns `409`.
- Only the base name is kept (any path sent by the browser is dropped); names
  starting with `.pgcli-upload-`, and `__pgcli_health` at the root, are
  refused with `400`.
- Writes are confined to `--dir`; a symlinked directory pointing outside the
  served tree returns `403`.
- The page submits via `XMLHttpRequest` and shows a progress bar (percentage
  plus bytes sent of the total) below the form while the transfer runs; on
  success the listing reloads automatically, on failure the button is restored
  and the reason is shown. Without JavaScript the plain form still works and a
  `303` returns to the listing. If a multi-file upload fails midway, files
  already written are kept.

### Creating directories

With `--allow-dir`, every listing gets a "new folder" form. Enter a relative
path such as `images/v2`; it is created under the directory being browsed,
along with any missing intermediate directories.

- The same operation works without JavaScript:
  `curl -d 'dir=images/v2' http://host:port/sub/`.
- An existing target (file or directory) returns `409` rather than silently
  succeeding.
- Absolute paths, any `..` component, and empty paths return `400`;
  `__pgcli_health` is reserved at the root and also returns `400`.
- The page reloads automatically on success; without JavaScript a `303`
  returns to the listing.

### Restricting writes by source IP

`--admin-ip` limits who may use the write modes. Pass one address or a
comma-separated list; IPv4 and IPv6 are both accepted and surrounding spaces
are ignored:

```bash
pg http start --allow-upload --allow-dir --allow-delete \
  --admin-ip 192.0.2.10,192.0.2.11,2001:db8::7 --dir /srv/share
```

- With an empty value (the default) there is **no source restriction**: the
  write-mode flags alone decide, exactly as before.
- Once set, every other source still gets the normal read-only listing — no
  upload form, no "new folder" form, no delete buttons, and no `delete
  enabled`/`upload enabled`/`mkdir enabled` footer — and receives `403` for
  `POST` and `DELETE`. Reads are never restricted by this flag.
- The check uses the source address of the direct TCP connection
  (`RemoteAddr`); `X-Forwarded-For` is not trusted. Behind a reverse proxy
  every request appears to come from the proxy's address, so the list must
  contain that address, and the per-client restriction does not work.
- An invalid entry makes `pg http start` fail with an error rather than
  silently dropping it. `pg http status` shows the list in the `ADMIN` column.
- This is a coarse network restriction, not authentication: addresses can be
  shared through NAT, and anyone who can legitimately reach from a listed
  address can write.

## State and lifecycle

| Path | Contents |
|---|---|
| `<base>/http/servers/<name>.json` | name, pid, port, listen address, dir, TLS paths, write modes (delete/upload/mkdir), upload size limit, admin IPs, Basic Auth users with bcrypt hashes, log path |
| `<base>/http/servers/<name>.log` | server stdout/stderr |
| `<base>/http/tls/<name>/cert.pem`, `key.pem` | generated certificate pair (with `--tls`) |

`<base>` is `base_dir` from `pg.yaml` when the config exists, otherwise the
platform default (usually `~/.pgcli`). `pg http` works without a config file.

The state file is written with mode `0600` because it carries the Basic Auth
hashes.

Servers are launched with `setsid`, so closing the terminal does not stop them.
They are **not** started at boot by default; after a reboot, either run
`pg http start` again or enable autostart (see below).

### How status is determined

`pg http start` forks the hidden `pg http serve` child and waits for it to bind
the socket. The child writes the actual `{listen, port}` to a short-lived
`<name>.ready` file; the parent then records `<name>.json` (written to a temp
file and renamed, so a crash cannot leave a half-written state).

`pg http status` never connects to the server. It reads the `*.json` files,
skips broken ones, and treats an entry as `running` only when the recorded PID
still exists and — on Linux — its `/proc/<pid>/cmdline` still contains `http`,
`serve` and the server name, so a recycled PID is not mistaken for a server.
The URL and directory come from the state file; `AUTOSTART` comes from
`pg.yaml`.

## Autostart

A server can be relaunched automatically at boot. Start it once first, then
enable autostart for its name:

```bash
pg http start --name docs --dir /srv/docs --port 18080
pg autostart enable --http --name docs
```

The enable step snapshots the server's directory, listen address, port, TLS
material, write modes (delete/upload/mkdir), upload size limit, admin IP list
and Basic Auth users (bcrypt hashes) into the top-level `http` section of
`pg.yaml`; the boot service then re-creates the server from those settings. If
the recorded port is taken at boot, the next free port is used. A later
`pg http start`/`pg http restart` of the same name refreshes the snapshot.

```bash
pg autostart status                        # shows "http docs   enabled"
pg autostart disable --http --name docs    # removes the snapshot; does not stop the server
```

See [Auto-start on Boot](/docs/autostart/) for the boot service itself. Without
an `http` entry in the config, `pg http` behaves exactly as described above:
servers survive the terminal but not a reboot.

## Security and limits

- **No authentication by default.** Without `--auth`/`--auth-env`, anyone who
  can reach the port can read every file in the served tree. The default
  loopback listener is the only guard; expose `0.0.0.0` only on a trusted
  network or turn on Basic Auth.
- **Basic Auth is a full-site gate, not a strong access control.** It protects
  listings, downloads and writes alike, and `pg http start` warns when it is
  used without `--tls` on a non-loopback listener: over plain HTTP the
  credentials travel in clear text. The bcrypt hashes in the state file and in
  `pg.yaml` are not plain passwords but are still credential material — treat
  those files as sensitive.
- **The write modes (`--allow-delete`, `--allow-upload`, `--allow-dir`) add no
  separate authentication** — anyone who can pass Basic Auth (or reach the
  port when auth is off) can delete or upload files and create directories
  under `--dir`. Writes never escape the served tree, and `POST` additionally
  requires the browser `Origin` to match the request `Host` to block
  cross-site form submissions, but keep the loopback listener for the same
  reason as above. `pg http start` prints a warning when any write flag is on
  without `--auth` and the listener is not loopback. `--admin-ip` narrows
  writes to the listed source addresses and hides the write UI from everyone
  else, but it is not authentication — see Restricting writes by source IP
  above.
- **`--tls` encrypts but does not authenticate clients** — it is not an access
  control mechanism, though it is what keeps Basic Auth credentials from
  traveling in clear text.
- Symlinks can expose paths outside `--dir`; treat the served directory as the
  boundary of what you are willing to share.
- One directory tree per server; start several servers to share several trees.
- Logs are appended and not rotated by pgcli.
