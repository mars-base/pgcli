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
`Range`/`HEAD` support.

The default is `127.0.0.1:18080` serving the current directory. Servers run as
detached processes, so they survive the terminal that started them; several
servers can run at once.

```bash
pg http start
pg http status
pg http stop
```

## Start a server

```bash
pg http start [--name NAME] [--port 18080] [--listen 127.0.0.1] \
  [--dir .] [--tls] [--tls-cert FILE --tls-key FILE]
```

| Flag | Default | Notes |
|---|---|---|
| `--name` | `http-<requested-port>` | Name used by `status`/`stop`. If a live server already has it, `-2`/`-3` is appended |
| `--port` | `18080` | If the port is busy, the next free one is tried automatically (up to 100) |
| `--listen` | `127.0.0.1` | Use `0.0.0.0` to expose the server on the network |
| `--dir` | `.` | Directory to serve; resolved to an absolute path at start and must exist |
| `--tls` | off | Serve HTTPS with a generated self-signed certificate |
| `--tls-cert` / `--tls-key` | — | Use an existing certificate/key pair instead (implies `--tls`) |

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

## Inspect and stop

```bash
pg http status
```

```
NAME                    PID      STATUS    URL                                      DIR
http-18080              412345   running   http://127.0.0.1:18080/                  /srv/share
http-18081              412410   running   https://0.0.0.0:18081/                   /srv/docs
```

A state file whose process is gone is marked `stale`; the next `stop` removes
it.

```bash
pg http stop http-18080           # one server
pg http stop http-18080 docs      # several
pg http stop                      # no names = every server
pg http stop --all
```

Stop sends `SIGTERM` and waits up to `--timeout` (default `5s`, matching the
server's graceful-shutdown window) before `SIGKILL`. State files are removed;
the log and certificate stay on disk.

## Directory listings

- Directories are listed before files, then case-insensitively by name.
  Directory links carry a trailing slash; breadcrumbs and a `..` link (except
  at the root) navigate upward.
- An `index.html` is **not** served automatically — a directory always
  renders its listing.
- Only `GET` and `HEAD` are allowed; other methods get `405`.
- `/__pgcli_health` returns `200 ok` for readiness probes and shadows a real
  file of that name.
- Symlinks are followed, including ones pointing outside the served directory.

Every file row has a **copy** button. Clicking it copies the file's absolute
URL (`http(s)://host:port/path`) and shows `copyed` next to the button for
about 1.5 seconds. The Clipboard API is only available in secure contexts, so
over plain HTTP the page falls back to a hidden textarea and
`document.execCommand('copy')`; both paths work in browsers that still support
execCommand.

## State and lifecycle

| Path | Contents |
|---|---|
| `<base>/http/servers/<name>.json` | name, pid, port, listen address, dir, TLS paths, log path |
| `<base>/http/servers/<name>.log` | server stdout/stderr |
| `<base>/http/tls/<name>/cert.pem`, `key.pem` | generated certificate pair (with `--tls`) |

`<base>` is `base_dir` from `pg.yaml` when the config exists, otherwise the
platform default (usually `~/.pgcli`). `pg http` works without a config file.

Servers are launched with `setsid`, so closing the terminal does not stop them.
They are **not** started at boot; after a reboot, run `pg http start` again.
`status` checks `/proc/<pid>/cmdline`, so a recycled PID is not mistaken for a
server.

## Security and limits

- **No authentication.** Anyone who can reach the port can read every file in
  the served tree. The default loopback listener is the only guard; expose
  `0.0.0.0` only on a trusted network.
- **`--tls` encrypts but does not authenticate clients** — it is not an access
  control mechanism.
- Symlinks can expose paths outside `--dir`; treat the served directory as the
  boundary of what you are willing to share.
- One directory tree per server; start several servers to share several trees.
- Logs are appended and not rotated by pgcli.
