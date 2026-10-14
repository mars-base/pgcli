---
title: "HAProxy"
description: "Run HAProxy as a pgcli addon — a TCP load balancer in front of a Patroni cluster (unified or read/write split), or your own haproxy.cfg via --conf-file for arbitrary native proxying"
weight: 48
---

[HAProxy](https://www.haproxy.org) is the load balancer the Patroni docs
recommend putting in front of a cluster: clients connect to one stable address,
HAProxy health-checks each member's REST API, and routes writes to the current
leader (and, in read/write-split mode, reads to the replicas). pgcli runs it as
a **standalone, top-level addon** — shared routing infrastructure, not a
per-instance sidecar — pinned to the official `haproxy:3.2.23-alpine` image.

> **Platform support:** the HAProxy addon is **Linux-only** for now. It reaches
> Patroni members over the host network, which the macOS `podman machine` does
> not expose to containers. On macOS `NewHAProxyManager` fails fast with a
> clear message; `pg addon list` still shows installed instances without live
> status.

## How It Works

Routing follows the [official Patroni `haproxy.cfg`](https://github.com/patroni/patroni/blob/master/haproxy.cfg)
pattern. Each backend is a Patroni member; two ports matter per member:

- the **PostgreSQL port** — the TCP port HAProxy actually forwards connections to;
- the **REST API port** — where HAProxy sends its `httpchk` health check.

Patroni's REST API answers these GETs (unauthenticated by design):

| Endpoint | Returns 200 on |
|----------|----------------|
| `GET /` | the leader only |
| `GET /replica` (optionally `?lag=<max>`) | replicas only, and only if their lag is within the limit |

So the leader is the sole `UP` server for a write listener, and the replicas are
the sole `UP` servers for a read listener — failover is picked up automatically
as the health checks flip. pgcli renders this into
`<base-dir>/addon/haproxy/<name>/haproxy.cfg` and bind-mounts it read-only into
the container.

### Two routing modes

The mode is chosen with `--mode`:

- **`unified`** (default) — a single listener sends **all** traffic to the
  current leader. Simple; every connection can read and write. On leader change
  the write listener uses `on-marked-down shutdown-sessions` to drop stale
  connections so clients reconnect to the new leader.
- **`split`** — read/write separation: a `_<name>_rw` listener routes writes to
  the leader, and a separate `_<name>_ro` listener spreads reads across the
  replicas (round-robin, lag-filtered). Two client ports instead of one.

### Two configuration modes

Above those two routing modes sits a choice of *who writes* `haproxy.cfg`:

- **Rendered** (default) — pgcli generates the config from `--node`/`--ha` plus
  `--mode`, ports and `--max-lag`, and rewrites it on every install. This is the
  Patroni read/write proxy described above.
- **Custom (`--conf-file <path>`)** — you provide the full `haproxy.cfg` and
  pgcli runs it verbatim, for **any** native HAProxy proxying (plain TCP
  relays, HTTP frontends, extra listeners — anything HAProxy does), not just a
  Patroni cluster. pgcli copies the file into its config directory, mounts that
  copy read-only, validates it with `haproxy -c` before (re)starting the
  container, and never rewrites or deletes your original. After an edit,
  `pg addon haproxy reload` re-validates the file and hot-reloads the running
  instance without dropping connections (conf-file instances only — see
  [Hot reload](#hot-reload)). All listeners come from the file; `--mode`,
  `--rw-port`/`--ro-port`/`--stats-port`, `--max-lag`, `--listen` and
  `--node`/`--ha` have no meaning there and are rejected or ignored — see
  [Custom config file](#custom-config-file).


### Stats page

Every instance also gets an HTTP **stats** listener so you can watch server
up/down state in a browser: `http://<listen>:<stats-port>/`.

## Install

Backend members are supplied one of two ways (mutually exclusive), or bypassed
entirely with `--conf-file` (see [Custom config file](#custom-config-file)):

- **`--node NAME=HOST:PGPORT:RESTPORT`** (repeatable) — list them explicitly;
- **`--ha <scope>`** — auto-derive every **local** member of a Patroni scope
  that `pg ha` manages, using each member's PostgreSQL and REST API ports.

What each `--node` field means — for `node1=10.0.0.11:35532:8008`:

| Field | Meaning |
|-------|---------|
| `node1` | member name — becomes the `server` name in `haproxy.cfg` and the label in the stats page |
| `10.0.0.11` | address where this member is reachable from this host (the member's `advertise-host`, or `127.0.0.1` on the same host) |
| `35532` | the member's PostgreSQL port that HAProxy forwards connections to |
| `8008` | the member's Patroni REST API port, used for the health check |

With `--ha`, all four come from `pg.yaml`: the Patroni member name, its
`advertise-host` (default `127.0.0.1`), and its `host_port` / `restapi_port`.

```bash
# unified: one port, everything to the leader, backends auto-derived from scope "app"
pg addon install haproxy --name lb --ha app

# split: rw + ro listeners, reads capped at 1MB replica lag
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB

# explicit backends (e.g. members on other hosts, or a scope pgcli doesn't manage)
pg addon install haproxy --name lb --mode split \
  --node node1=10.0.0.11:35532:8008 \
  --node node2=10.0.0.11:35533:8009 \
  --node node3=10.0.0.11:35534:8010
```

The output reports the assigned ports and a ready-to-use connect URL:

```
✓ haproxy installed: "lb"
  Container:    pgcli-haproxy-default-lb
  Image:        docker.io/library/haproxy:3.2.23-alpine
  Mode:         split
  Patroni:      app
  Backends:     3
  Read-write:   127.0.0.1:5000
  Read-only:    127.0.0.1:5001 (lag ≤ 1MB)
  Stats:        http://127.0.0.1:5002/
  Config:       ~/pg/addon/haproxy/lb/haproxy.cfg

  Connect via HAProxy:
    postgres://<user>@127.0.0.1:5000/<database>
```

### Adding or removing members later

The backend list follows the scope's **local** members recorded in `pg.yaml`, so
any topology change — or removal — is picked up by re-running the **same**
install command: it re-derives the full backend list and recreates the
container.

Added with `pg ha create <scope> --member <m>`, the new member is not in the
running HAProxy config yet:

```bash
pg ha create app --member node3 --etcd m1
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB
# Backends: 2 -> 3, and haproxy.cfg gains: server node3 127.0.0.1:35534 ... check port 8011
```

Symmetrically, after `pg ha remove <scope> --member <m>` the removed member
would otherwise linger in the config as a stale backend — re-running install
drops it:

```bash
pg ha remove app --member node3
pg addon install haproxy --name lb --mode split --ha app --max-lag 1MB
# Backends: 3 -> 2, and the server node3 line is gone
```

For an explicit `--node` install there is no auto-derivation: edit the `--node`
list itself (add or drop a spec) and re-run — the target list is replaced
wholesale each time, so the command always reflects the full set you want.

### Cross-host clusters

`--ha` derives backends from **this host's** `pg.yaml`, which lists only the
members this host's `pg ha create` manages. A member living on another host was
never a backend here — adding or removing one *there* requires no re-sync on
*this* host (and leaves nothing stale). To front the whole cluster through one
HAProxy, use an explicit `--node` list, with each member's advertised host:

```bash
pg addon install haproxy --name lb --mode split \
  --node node1=10.0.0.11:35532:8008 \
  --node node2=10.0.0.12:35532:8008 \
  --node node3=10.0.0.13:35532:8008
# later: add one more --node spec (or drop one) and re-run — the list replaces wholesale
```

Caveat: an instance installed with `--ha` on one host routes writes only to a
*local* leader — if failover promotes a remote member, the rw listener has no
UP server until the leader moves back. For cross-host clusters where the leader
can move, prefer the explicit all-members `--node` list (or run one HAProxy per
host and front it with a higher-level VIP/DNS).

## Custom config file

The rendered mode above is pgcli's opinion about Patroni routing. With
`--conf-file` you skip the render entirely and run your own `haproxy.cfg` —
arbitrary native HAProxy: plain TCP relays, HTTP frontends, any number of
`frontend`/`backend`/`listen` sections, anything the upstream software
supports. pgcli keeps only the container lifecycle (pull, create, start, stop,
remove, autostart) and the config **gate**:

```bash
pg addon install haproxy --name custom --conf-file /root/haproxy/lb.cfg
```

`--conf-file` is mutually exclusive with `--node`/`--ha` (the install is
rejected with an error), and the file must exist and be non-empty. A typical
file mixes a raw TCP relay with an unrelated HTTP listener:

```
global
    maxconn 1000
    log stdout format raw local0 info

defaults
    log global
    mode tcp
    timeout client 30m
    timeout connect 4s
    timeout server 30m

# plain TCP relay to a PostgreSQL instance (pgcli derives nothing here)
frontend pg_relay
    bind 10.0.0.11:7000
    default_backend pg

backend pg
    server demo 127.0.0.1:35432 check

# an HTTP-mode listener for something unrelated
listen webstats
    mode http
    bind 127.0.0.1:7001
    http-request return status 200 content-type text/plain lf-string "haproxy custom proxy alive\n"
```

The summary then reports the custom mode instead of ports (there are none to
report — they live in your file):

```
✓ haproxy installed: "custom"
  Container:    pgcli-haproxy-default-custom
  Image:        docker.io/library/haproxy:3.2.23-alpine
  Mode:         custom (user config — all listeners come from the file)
  Config:       /root/.pgcli/addon/haproxy/custom/haproxy.cfg (from /root/haproxy/lb.cfg)
```

How the mode behaves, point by point (all of this is what an install on a real
host does):

- **Your file is the source of truth; pgcli's copy is what runs.** Install
  copies it to `<base-dir>/addon/haproxy/<name>/haproxy.cfg` and bind-mounts
  that copy read-only. Your original is never rewritten, and `pg addon remove`
  deletes only pgcli's copy — your file stays where you put it.
- **Edits apply via `reload` (seamless) or re-install (lossy).** Change the
  file, then either `pg addon haproxy reload` to hot-apply it to the running
  container without dropping connections, or re-run the same install to
  recreate the container. Both first re-copy your file and validate it with
  `haproxy -c`. Editing the rendered-mode knobs does nothing here — `--mode`,
  `--rw-port`/`--ro-port`/`--stats-port`, `--max-lag` and `--listen` describe
  the generator, not your file. See [Hot reload](#hot-reload).
- **Everything is validated before the container is touched.** Each install
  runs the mounted copy through `haproxy -c` (check-only) in a throwaway
  container first. A bad file fails the install with HAProxy's own diagnostics,
  exits non-zero, and **restores the previous good copy** — a running container
  and the on-disk config are left exactly as they were, so a later reboot or
  `pg addon start` cannot resurrect a rejected config. (Warnings — missing
  timeouts and the like — pass; only real errors are refused.)
- **Ports are yours to choose.** The pool allocator skips conf-file instances;
  it neither assigns nor learns the ports in your file. Pick listener ports
  yourself and keep them clear of rendered-mode instances' auto-assigned ports
  and everything else on the host — HAProxy binds fail at start with
  `cannot bind socket` if you collide.
- **The container runs with host networking**, like the rendered mode. A
  frontend bound to `127.0.0.1` is reachable from the host itself; to let other
  containers/hosts in, bind the host's address (as `pg_relay` above does).
- **Health of the proxies is your config's business.** `pg logs <name>` shows
  HAProxy's own stdout (if your file logs there, as the sample does), and any
  stats page is whatever `stats` section you wrote — pgcli does not add one.
- Switching back is just an install without `--conf-file`: pass `--node`/`--ha`
  again and the instance returns to rendered mode (the conf-file copy on disk
  is overwritten by the generated config).

### Example: HTTP load balancer with health checks

A conf-file instance is a full HAProxy, so the Patroni pattern is not the only
thing it can do — here is an HTTP round-robin balancer over several app servers
with active health checks (this exact config was run and the DOWN/UP behaviour
observed in `pg logs`):

```
global
    maxconn 4096
    log stdout format raw local0 info

defaults
    log global
    mode http
    retries 2
    timeout client 30m
    timeout connect 4s
    timeout server 30m
    timeout http-request 30s

frontend http_in
    bind 127.0.0.1:8180
    default_backend http_pool

backend http_pool
    # round-robin across the app servers; health-checked via GET /
    option httpchk GET /
    http-check expect status 200
    default-server inter 3s fall 3 rise 2
    server app1 127.0.0.1:8081 check
    server app2 127.0.0.1:8181 check
    # add more: server app3 127.0.0.1:<port> check
```

What each piece does, and what was observed live:

- **Multiple `server` lines** — HAProxy round-robins new connections across the
  `UP` servers (add a line per backend; the list order is the starting
  rotation).
- **`option httpchk GET /` + `http-check expect status 200`** — the health probe
  is an HTTP `GET /` and only 200 counts as UP. Match the method and the
  expected code to what the app actually answers: a server that returns `405` to
  `HEAD` but `200` to `GET` needs the `GET` form, not the `HEAD /` default.
- **`default-server inter 3s fall 3 rise 2`** — probe every 3s, mark DOWN after
  3 consecutive failures, return to service after 2 successes.
- **`check` on each server** turns the probe on for that server.

When a backend is unreachable its probe fails and, after `fall 3`, HAProxy drops
it from rotation and logs it — `pg logs <name>` showed:

```
Server http_pool/app2 is DOWN, reason: Layer4 connection problem, info: "Connection refused", check duration: 0ms. 1 active and 0 backup servers left. ...
```

and all frontend traffic kept returning 200 by flowing only to the surviving
`app1`. Once `app2`'s port answers again it clears `rise 2` and rejoins the
rotation — no reload needed. To add or remove a `server` *line*, edit the file
and run `pg addon haproxy reload` (see [Hot reload](#hot-reload)): it
re-validates with `haproxy -c` and hot-swaps the worker without dropping
in-flight connections. Backend membership changes are cleanest still through the
health-check path above (a server you *stop* is pulled from rotation with no
reload at all); reload is for when you change the `server` *list* itself.

### Tuning concurrency (threads)

HAProxy's model is **one process, many threads** — there is no nginx-style
`worker_processes`. In conf-file mode you tune it from the `global` section of
your own file (the rendered mode does not set any of these):

- **`nbthread N`** — the number of worker threads. Validated against the image:
  `haproxy -c` accepts `nbthread 4`.
- **default when unset** — HAProxy spreads threads across the machine's CPUs
  automatically on a multi-processor host (its own wording: *"Threads will
  automatically be used on multi-processor machines if available"*), so you only
  need `nbthread` to pin a specific count.
- **`cpu-map auto:1-N 0-M`** — optionally bind thread 1…N to CPU cores 0…M
  (validated).
- **`nbproc` no longer exists.** Since HAProxy 2.5 it is refused outright
  (`haproxy -c` fails with *"nbproc is not supported any more since HAProxy 2.5"*).
  Use threads, not processes.

```
global
    nbthread 4
    cpu-map auto:1-4 0-3
    maxconn 4096
    log stdout format raw local0 info
```

Changing it means editing the file and running `pg addon haproxy reload` — the
master re-reads the config and starts a fresh worker with the new thread count
(verified: `nbthread` 2 → 4 applied with no dropped connections), so unlike a
re-install it doesn't cut in-flight traffic. This is a config knob in your file,
not a pgcli flag.

### Hot reload

`--conf-file` instances support seamless config reload — the HAProxy equivalent
of `pg addon nginx reload`:

```bash
pg addon haproxy reload --name custom
```

The official image already runs HAProxy in **master-worker mode** (`-W`): pid 1
is the master, a child is the worker. `reload` sends the master `SIGUSR2`, so it
starts a *new* worker from the config on disk, hands the listening sockets over,
and lets the old worker drain its in-flight connections before exiting —
**no dropped connections**, unlike the lossy container recreate that `install`
performs. (This is verified behaviour, not just theory: a running instance's
worker was swapped end-to-end while it kept serving 200s.)

Before it signals anything, `reload` does exactly what install's gate does — it
is **not** a blind signal:

1. re-copies your `--conf-file` to the mounted `haproxy.cfg` (verbatim);
2. runs the copy through `haproxy -c` (check-only) in a throwaway container;
3. **only if it validates** does it send `SIGUSR2`. A file that fails the check
   aborts the reload with HAProxy's own diagnostics (non-zero exit), **restores
   the previous good copy on disk**, and leaves the running container untouched
   — so a rejected edit can never be picked up by a later restart either.

Requirements and scope:

- **`--conf-file` instances only.** A rendered instance rebuilds its config from
  `--node`/`--ha` on every install and takes no hand-edits, so its update path
  stays `pg addon install haproxy`. Running `reload` on it is refused with that
  advice.
- **The container must be running.** `reload` signals the live master; if the
  instance is stopped, start it (`pg addon start haproxy --name custom`) — a
  fresh `install` also picks up the edited file.
- `reload` is for **changing the running config in place**. Turning a backend
  *off* for maintenance still needs no reload at all — the health check pulls it
  from rotation; reload is for editing the `server` list / global knobs
  themselves.

The same reload is available **on install** via `--reload`: for a running
conf-file instance it validates and hot-reloads instead of recreating the
container, so one command both (re)configures and applies without dropped
connections:

```bash
pg addon install haproxy --name custom --conf-file /root/haproxy/lb.cfg --reload
```

Without `--reload`, install keeps its default recreate-the-container behaviour
(lossy). `--reload` on a stopped or rendered instance is a no-op warning — it
just installs normally.

## Read/Write Split in Practice

After installing with `--mode split`, two client ports are exposed:

| Port | Traffic | Backend |
|------|---------|---------|
| `write_port` (default 5000) | Read + write | Leader only |
| `read_port` (default 5001) | Read only | Replicas (round-robin, lag ≤ `max-lag`) |
| `stats_port` (default 5002) | HTTP stats page | — |

```bash
# Write (always hits the current leader)
psql "host=127.0.0.1 port=5000 user=postgres dbname=postgres"

# Read (load-balanced across replicas)
psql "host=127.0.0.1 port=5001 user=postgres dbname=postgres"
```

After a failover, HAProxy picks up the new leader automatically via health
checks — no connection string change needed. A browser at
`http://127.0.0.1:5002/` shows real-time backend state.

## Ports

Ports are auto-assigned from a per-host pool, three consecutive free ports per
instance (rw, then ro if split, then stats), starting at `haproxy_start_port`
(default **5000**). Override any of them explicitly:

```bash
pg addon install haproxy --name lb --ha app \
  --rw-port 6000 --ro-port 6001 --stats-port 6002
```

Because the pool scans for free ports and tracks what every HAProxy instance
has already taken, you can run **many instances on one machine** without
collisions. A `split` instance consumes 3 ports (rw / ro / stats), a `unified`
instance 2 (rw / stats):

```bash
pg addon install haproxy --name lb1 --mode split --ha app     # 5000 / 5001 / 5002
pg addon install haproxy --name lb2 --ha report              # 5003 / 5004
```

The bind address defaults to `127.0.0.1`; set `--listen 0.0.0.0` — or the
`listen` key in `pg.yaml` — to expose the listeners on the network. Keep it on
loopback unless the backends require otherwise; there is no authentication in
front of PostgreSQL here.

## Configuration

Everything lives under the top-level `addons.haproxy` map in `pg.yaml`, keyed by
instance name:

```yaml
namespace: default
haproxy_start_port: 5000
addons:
  haproxy:
    lb:
      mode: split            # unified | split
      ha_scope: app          # scope the backends came from (for --ha re-derivation)
      listen: 127.0.0.1
      write_port: 5000
      read_port: 5001
      stats_port: 5002
      replica_max_lag: 1MB   # split mode only
      autostart: false       # pg autostart enable --haproxy --name lb
      image_tag: docker.io/library/haproxy:3.2.23-alpine
      # conf_file: /root/haproxy/lb.cfg   # custom mode (see above; skips targets/mode/ports)
      targets:
        - { name: node1, host: 127.0.0.1, pg_port: 35532, rest_port: 8008 }
        - { name: node2, host: 127.0.0.1, pg_port: 35533, rest_port: 8009 }
```

`targets` is rendered from and re-derived by install; edit `mode`, ports,
`replica_max_lag`, or `image_tag` here for fine control, then
`pg addon install haproxy --name lb ...` to apply. The rendered `haproxy.cfg`
carries **no secrets** — only hosts, ports, and health-check URIs.

### List

```bash
pg addon list
```

```
Infra add-ons (haproxy):
  haproxy (name: lb)
    Status:      running
    Mode:        split
    Patroni:     app
    Read-write:  127.0.0.1:5000
    Read-only:   127.0.0.1:5001
    Stats:       http://127.0.0.1:5002/
    Backends:    3
      - node1        127.0.0.1:35532 (check :8009)
      - node2        127.0.0.1:35533 (check :8010)
      - node3        127.0.0.1:35534 (check :8011)
```

A conf-file instance shows its source instead of ports and backends:

```
  haproxy (name: custom)
    Status:      running
    Mode:        custom (user config)
    Config:      /root/haproxy/lb.cfg
```

## Start and stop

After a host reboot, bring an instance back without re-rendering config:

```bash
pg addon start haproxy --name lb
pg addon stop  haproxy --name lb
```

`install` always recreates the container so config changes take effect;
`start` only starts an existing one (and self-heals an improper state by
recreating from the `haproxy.cfg` on disk). For a `--conf-file` instance,
`pg addon haproxy reload` applies an edited file to a *running* container
without that recreate — see [Hot reload](#hot-reload).

## Auto-start on Boot

Containers carry a `--restart unless-stopped` policy (crashes, not reboots). To
bring an instance up after a host reboot:

```bash
pg autostart enable --haproxy --name lb
```

This sets `autostart: true` and installs/refreshes the boot service (see
[Auto-start on Boot](/docs/autostart/)). Boot is **start-only**: it starts the
existing container reading the `haproxy.cfg` already on disk, and never
re-renders config — install the instance first. The boot service starts
HAProxy **after** the Patroni members, so its backends are already coming up.
`pg autostart status` lists every target's state.

## Remove

```bash
pg addon remove haproxy --name lb
```

Stops and removes the container and deletes its config directory.

## Logs

```bash
pg logs addon haproxy --name lb       # last 50 lines
pg logs addon haproxy --name lb -f    # follow
```

The container logs to stdout (`log stdout format raw local0 info`), so health-check
transitions and connection events show up here.

## Troubleshooting

- **All backends DOWN in the stats page.** The health check hits each member's
  REST API port. Confirm it is reachable — `curl http://<host>:<rest_port>/`
  returns 200 on the leader, 503 elsewhere; `/replica` is the mirror image. A
  `000` means the REST API is down or the port is wrong.
- **`--ha` finds no members.** `--ha` only derives **local** members that
  `pg ha` manages in `pg.yaml`. For remote members, or a scope pgcli doesn't
  manage, list them with `--node`.
- **Port collision on manual `--rw-port`.** Pick a port outside the auto pool
  (`haproxy_start_port` and up) or the auto-assigner will treat it as taken.
- **A conf-file install fails `haproxy -c` but the old config keeps running.**
  That is the designed behaviour: the rejected copy is rolled back and the
  container untouched. Fix the file (HAProxy's error names the line) and
  re-install. To see why a *started* container died anyway (`cannot bind
  socket`, missing files your config references), check `pg logs <name>`.
- **`reload` says "container is not running".** Reload signals the live master,
  so the instance must be up first — `pg addon start haproxy --name <name>` (or
  a fresh `pg addon install`, which also picks up the edited file).
- **`reload` rejects the file with `haproxy -c` errors.** Same gate as install:
  the reload is aborted, the previous good copy is restored, and nothing was
  reloaded — fix the line HAProxy names and reload again.
- **macOS.** Not supported yet — HAProxy needs host networking to reach Patroni
  members, which the podman machine VM does not provide to containers.
