---
title: "Nginx"
description: "Run nginx as a pgcli addon — an HTTP reverse proxy in front of web services (pgAdmin, PostgREST, etc.), with path-based routing and optional TLS termination"
weight: 53
---

[nginx](https://nginx.org) is the HTTP reverse proxy that fronts multiple web
services — pgAdmin, PostgREST, even HAProxy stats — with path-based routing and
optional TLS termination. pgcli runs it as a **standalone, top-level addon** with
the same CLI surface as the other web/infra addons (install / start / stop / logs
/ autostart / remove), managed entirely through `pg.yaml`.

It is the fleet's HTTP *gateway*, which shapes its design:

- **A reverse proxy, not a load balancer.** Unlike [HAProxy](../haproxy/) — a
  TCP load balancer in front of Patroni — nginx is an HTTP reverse proxy that
  routes by URL path. Each `location` block forwards to one upstream (pgAdmin,
  PostgREST, etc.), so clients reach multiple services through a single port.
- **File-driven configuration.** pgcli renders `nginx.conf` from the backends
  you declare (or accepts a custom file via `--conf-file`) and bind-mounts it
  read-only into the official `nginx:1.27-alpine` image. TLS is optional: when
  enabled without a BYO cert, pgcli generates a self-signed leaf via `tlsca`.
- **Independent log mounting.** Each backend gets its own access and error log
  files (`access_<name>.log`, `error_<name>.log`) under a bind-mounted
  `/var/log/nginx/` directory, so per-service traffic is observable without
  parsing a shared log.
- **Nginx-specific subcommands.** Beyond the generic addon lifecycle
  (install/start/stop/remove), nginx exposes `test` (validate config syntax via
  `nginx -t` using a temporary container), `reload` (hot reload via
  `nginx -s reload`), and `exec` (run arbitrary commands inside the container).

> **Platform support:** nginx works on both **Linux** (host networking) and
> **macOS** (pgcli-net bridge with published ports). The upstream image is
> multi-arch (amd64 + arm64), so any host architecture works.

## How It Works

Routing is path-based: each `location` block forwards to one upstream. The
config is rendered from the backends you declare (or copied from a custom
file), bind-mounted read-only, and nginx runs in the foreground with
`nginx -g 'daemon off;'`.

### Two configuration modes

The mode is chosen with `--conf-file` (custom) or `--upstream` (template):

- **Template mode** (default) — pgcli renders `nginx.conf` from `--upstream`
  specs, `--pgadmin-name`, and `--postgrest-name` flags. Each upstream becomes
  an `upstream` block and a `location` block; TLS adds an HTTPS server block
  alongside the HTTP one. The template covers the common case: path-based
  routing to a handful of web services.
- **Custom mode** (`--conf-file`) — you provide the full `nginx.conf` path;
  pgcli copies it into the addon directory and manages only the container
  lifecycle. Use this when production configs grow beyond the template's reach
  (`limit_req_zone`, `resolver` + variable `proxy_pass`, WebSocket, SNI,
  multiple locations per backend, upstream keepalive, etc.). `--upstream` and
  `--conf-file` are mutually exclusive.

### Per-backend logs

Each backend gets its own log files in the bind-mounted `/var/log/nginx/`
directory:

- `access_<name>.log` — access log for that backend's `location` block
- `error_<name>.log` — error log for that backend's `location` block

The global `access.log` and `error.log` capture server-level events. The log
directory is created on the host at `<base-dir>/addon/nginx/<name>/log/` and
persists across container restarts.

### TLS termination

TLS is optional. When enabled without `--tls-cert`/`--tls-key`, pgcli generates
a self-signed leaf certificate via `tlsca.Generate` under
`<base-dir>/addon/nginx/<name>/tls/`. The HTTPS listener runs alongside the
HTTP one, so existing plain-text clients keep working. With BYO certs, the
given files are bind-mounted instead.

## Install

Backends are supplied one of three ways (mutually exclusive):

- **`--upstream name=<n>,path=<p>,backend=<h:p>`** (repeatable) — explicit
  upstream targets;
- **`--pgadmin-name <name>`** / **`--postgrest-name <name>`** — convenience
  flags that auto-resolve the named addon's endpoint and add it at `/admin` or
  `/api` respectively;
- **`--conf-file <path>`** — a custom `nginx.conf` file (skips template
  rendering entirely).

What each `--upstream` field means — for
`--upstream name=pgadmin,path=/admin,backend=127.0.0.1:5050`:

| Field | Meaning |
|-------|---------|
| `name` | upstream name in `nginx.conf` (defaults to a sanitized `path` if omitted) |
| `path` | location path prefix (e.g. `/admin` → pgAdmin, `/api` → PostgREST; defaults to `/`) |
| `backend` | host:port to proxy to (e.g. `127.0.0.1:5050`) |

```bash
# template mode: one upstream, explicit
pg addon install nginx --name proxy \
  --upstream name=pgadmin,path=/admin,backend=127.0.0.1:5050

# template mode: convenience flags auto-resolve pgAdmin and PostgREST endpoints
pg addon install nginx --name proxy \
  --pgadmin-name admin --postgrest-name api

# template mode with TLS (self-signed cert)
pg addon install nginx --name proxy \
  --pgadmin-name admin --tls

# custom mode: you provide the full config
pg addon install nginx --name custom \
  --conf-file /path/to/nginx.conf --http-port 8080
```

The install summary reports the assigned ports and the config path:

```
✓ nginx installed: "proxy"
  Container:  pgcli-nginx-default-proxy
  Image:      docker.io/library/nginx:1.27-alpine
  HTTP:       127.0.0.1:8080
  HTTPS:      (disabled)
  Backends:   2
    - pgadmin    /admin → 127.0.0.1:5050
    - postgrest  /api   → 127.0.0.1:3000
  Config:     ~/pg/addon/nginx/proxy/nginx.conf
  Logs:       ~/pg/addon/nginx/proxy/log/
```

With `--conf-file`, the summary says `Config: <path> (from <confFile>)` and
skips the `Backends:` section (pgcli does not parse the user's config).

### Rebuilding with `--force`

`pg addon install nginx` without `--force` is a no-op on a running container —
it leaves the live config alone and just makes sure the container is up. That
is exactly the wrong behaviour when you *meant* to change something (port,
listen, backends, TLS, `--conf-file`, `worker_connections`): the running
container is pinned to the config it was created with, so the edit looks
accepted but nothing changes until the next recreate.

`--force` is the knob that applies such edits. It stops the running container,
removes it, and creates a new one from the current config:

```bash
# change the bind address to expose the proxy on the network
pg addon install nginx --listen 0.0.0.0 --force

# add a new upstream
pg addon install nginx --pgadmin-name admin --postgrest-name api --force

# switch to a custom config
pg addon install nginx --conf-file /etc/nginx/custom.conf --force

# retag to a different upstream image
pg addon install nginx --image docker.io/library/nginx:1.28-alpine --force
```

## Nginx subcommands

Beyond the generic addon lifecycle (install/start/stop/remove), nginx exposes
three subcommands for day-to-day operations:

### test — validate config syntax

```bash
pg addon nginx test --name proxy
```

Runs `nginx -t` inside a **temporary container** (`podman run --rm`) with the
same mounts as the real one. Does **not** require the nginx container to be
running — it spins up a throwaway container, validates the config, and exits.
This is the safe way to test config changes before reloading:

```bash
# edit the config file (template or custom)
vim ~/pg/addon/nginx/proxy/nginx.conf

# validate syntax without affecting the running proxy
pg addon nginx test --name proxy
# nginx: the configuration file /etc/nginx/nginx.conf syntax is ok
# nginx: configuration file /etc/nginx/nginx.conf test is successful

# only then reload
pg addon nginx reload --name proxy
```

The temporary container mounts the same `nginx.conf` (and TLS cert directory
if TLS is enabled), so the validation is identical to what the real container
would see on startup.

### reload — hot reload configuration

```bash
pg addon nginx reload --name proxy
```

Sends `nginx -s reload` to the running container — a graceful restart that
picks up config changes without dropping in-flight connections. Requires the
container to be running (use `pg addon start nginx --name proxy` first if it
is stopped).

### exec — run commands inside the container

```bash
pg addon nginx exec --name proxy -- cat /etc/nginx/nginx.conf
pg addon nginx exec --name proxy -- nginx -V
pg addon nginx exec --name proxy -- ls -la /var/log/nginx/
```

Runs an arbitrary command inside the nginx container with stdio passthrough.
Requires the container to be running. The `--` separator is mandatory —
everything after it is passed verbatim to the container.

## Parameters

One container from the upstream `docker.io/library/nginx` image (pull-only —
pgcli never builds it), configured entirely through the rendered (or custom)
`nginx.conf` and the `podman run` flags. The table lists every knob and its
default:

| Flag / `pg.yaml` key | Default | Meaning |
|----------------------|---------|---------|
| `--image` / `image_tag` | `docker.io/library/nginx:1.27-alpine` | override the tag verbatim |
| `--http-port` / `http_port` | auto from `nginx_start_port` (base **8080**) | HTTP listener host port |
| `--https-port` / `https_port` | auto (next free port after HTTP) | HTTPS listener host port (TLS only) |
| `--listen` / `listen` | `127.0.0.1` | bind address (`0.0.0.0` to expose) |
| `--worker-connections` / `worker_connections` | `1024` | `worker_connections` in the `events` block (max simultaneous connections per worker) |
| `--tls` / `tls` | `false` | enable HTTPS listener alongside HTTP |
| `--tls-cert` / `tls_cert` | unset | BYO cert path (PEM leaf + chain); without it, pgcli generates a self-signed cert |
| `--tls-key` / `tls_key` | unset | BYO private key path (PEM) |
| `--upstream` | unset (repeatable) | `name=<n>,path=<p>,backend=<h:p>` — one upstream target |
| `--pgadmin-name` | unset | convenience: auto-add the named pgAdmin addon at `/admin` |
| `--postgrest-name` | unset | convenience: auto-add the named PostgREST addon at `/api` |
| `--conf-file` / `conf_file` | unset | path to a custom `nginx.conf` (mutually exclusive with `--upstream`/`--pgadmin-name`/`--postgrest-name`) |
| — | `autostart: false` | start on boot (`pg autostart enable --nginx`) |

How the knobs combine:

- **`--conf-file` is mutually exclusive with `--upstream`/`--pgadmin-name`/`--postgrest-name`.**
  Both mean "here is the config" — one by path, the other by declaring backends.
  The install rejects the combination.
- **`--tls` without `--tls-cert`/`--tls-key` generates a self-signed cert.**
  The cert lives under `<base-dir>/addon/nginx/<name>/tls/` and is bind-mounted
  at `/etc/nginx/certs`. The HTTPS listener runs alongside HTTP on the next
  free port.
- **`--tls-cert`/`--tls-key` without `--tls` is ignored.** The flags only take
  effect when TLS is enabled.
- **`--worker-connections` is ignored in `--conf-file` mode.** The custom
  config owns the `events` block; pgcli does not patch it.
- **`--listen` defaults to loopback** — the proxy stays on `127.0.0.1` unless
  you pass `--listen 0.0.0.0` to reach it from another host. On Linux the
  container shares the host network, so `0.0.0.0` answers on
  `<host-ip>:<port>`; on macOS the bridge publishes the port and
  `proxyBindHost` widens loopback to `0.0.0.0` on the bridge.

## Ports

Each instance takes **one or two ports** from nginx's own pool,
`nginx_start_port` (default **8080**) — a separate cursor from pgAdmin's,
Redis's, Predixy's, and the object stores' pools, so an nginx, a pgadmin, a
redis, and a minio instance never collide:

```bash
pg addon install nginx --name proxy                          # 8080 (HTTP only)
pg addon install nginx --name proxy-tls --tls                # 8081 (HTTP) + 8082 (HTTPS)
pg addon install nginx --name proxy2                         # 8083
```

`--http-port` and `--https-port` fix an instance to specific ports;
auto-assignment skips anything already explicit or live on the host. A TLS
instance consumes two ports (HTTP + HTTPS); a plain instance consumes one.

## Configuration

Instances live under the top-level `addons.nginx` map in `pg.yaml`:

```yaml
namespace: default
nginx_start_port: 8080     # nginx's own pool
addons:
  nginx:
    proxy:
      container_name: pgcli-nginx-default-proxy
      name: proxy
      image_tag: docker.io/library/nginx:1.27-alpine
      listen: 127.0.0.1
      http_port: 8080
      https_port: 0        # 0 = auto-assign (TLS only)
      worker_connections: 1024
      tls: false
      # tls_cert: /path/to/cert.pem   # BYO cert (tls: true required)
      # tls_key:  /path/to/key.pem
      # conf_file: /etc/nginx/custom.conf  # custom mode (skips backends)
      autostart: false     # pg autostart enable --nginx --name proxy
      backends:
        - { name: pgadmin,   path: /admin, backend: "127.0.0.1:5050" }
        - { name: postgrest, path: /api,   backend: "127.0.0.1:3000" }
```

Edits to `listen`, `http_port`, `https_port`, `worker_connections`, `tls`,
`tls_cert`/`tls_key`, `conf_file`, or `backends` take effect after the next
`pg addon install nginx --name proxy --force` (or via the matching flags).

### List

```bash
pg addon list
```

```
Web add-ons (nginx):
  nginx (name: proxy)
    Status:      running
    HTTP:        127.0.0.1:8080
    HTTPS:       (disabled)
    Backends:    2
      - pgadmin    /admin → 127.0.0.1:5050
      - postgrest  /api   → 127.0.0.1:3000
    Config:      ~/pg/addon/nginx/proxy/nginx.conf
    Logs:        ~/pg/addon/nginx/proxy/log/
    Image:       docker.io/library/nginx:1.27-alpine
    Container:   pgcli-nginx-default-proxy
```

## Start and stop

```bash
pg addon start nginx --name proxy
pg addon stop  nginx --name proxy
```

`start` only starts an existing container (and self-heals an improper state by
recreating it from the `nginx.conf` already on disk). The config and log
directories are preserved across stop/start.

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring instances up after a host reboot:

```bash
pg autostart enable --nginx --name proxy
```

nginx autostart is **start-only** (install the instance first). On boot it
reads the `nginx.conf` already on disk, so the proxy comes up with the last
known config. nginx is independent of the PostgreSQL stack — it is a reverse
proxy that dials its backends itself — so there is no ordering constraint and
boot-time `pg start --autostart` starts it alongside the other non-database
addons.

## Remove

```bash
pg addon remove nginx --name proxy
```

Stops and removes the container and deletes its config directory (including TLS
certs and logs). An empty per-instance parent directory is pruned.

## Logs

```bash
pg logs addon nginx --name proxy        # last 50 lines
pg logs addon nginx --name proxy -f     # follow
```

The container logs to stdout, so access and error events show up here. For
per-backend logs, look at the bind-mounted log directory:

```bash
ls ~/pg/addon/nginx/proxy/log/
# access.log  access_pgadmin.log  access_postgrest.log
# error.log   error_pgadmin.log   error_postgrest.log
```

## Troubleshooting

- **Config syntax error on startup.** The container exits and the restart
  policy spins it. Check with `pg addon nginx test --name proxy` (works even
  when the container is stopped) — it runs `nginx -t` in a temporary container
  and reports the exact line number.
- **`--conf-file` and `--upstream` together.** The install rejects the
  combination — pick one mode.
- **Can't reach the proxy from another machine.** `listen` defaults to
  `127.0.0.1`. Reinstall `--listen 0.0.0.0 --force` and accept that the proxy
  is then the only gate in front of the backends.
- **Image not found at install.** `docker.io/library/nginx` is pulled on
  demand; on an air-gapped host, `podman load` the tar first (catalog and
  export steps in `docs/images.md` in the repo) and the pull is skipped.
- **Reload fails with "container not running".** `pg addon nginx reload`
  requires the container to be up. Start it first with
  `pg addon start nginx --name proxy`.

## Known limitations

- **No load balancing or health checks.** nginx here is a reverse proxy
  (path-based routing to a single upstream per location), not a TCP load
  balancer. For load balancing in front of Patroni, use
  [HAProxy](../haproxy/).
- **Template is simple by design.** The rendered `nginx.conf` covers path-based
  routing to a handful of backends with optional TLS. Production configs that
  need `limit_req_zone`, `resolver` + variable `proxy_pass`, WebSocket, SNI,
  multiple locations per backend, or upstream keepalive should use
  `--conf-file` instead.
- **macOS is code-complete, lightly tested.** The bridge path (published ports,
  `proxyBindHost` widening loopback to `0.0.0.0` on the bridge) mirrors the
  other bridge addons; it has been exercised but not yet under load.
