---
title: "SafeLine WAF"
description: "Run SafeLine (雷池) Community Edition as a managed addon — a seven-container stack (bundled PostgreSQL 15, HTTPS console, detector, tengine traffic proxy and support services) on a dedicated static-IP bridge network, Linux rootful only"
weight: 70
---

`pg addon install safeline` runs **SafeLine (雷池) Community Edition** as a managed addon. It is a **top-level addon** (it does not front a PostgreSQL instance) and it is a **seven-container stack per instance**:

- **postgres** — the stack's own bundled PostgreSQL 15.18, pinned separately from pgcli instances (upstream has not validated PostgreSQL 18 yet, see [chaitin/SafeLine#1250](https://github.com/chaitin/SafeLine/issues/1250))
- **mgt** — the HTTPS console, the only published port
- **detector** — the attack-detection engine
- **tengine** — the traffic reverse proxy, protecting the sites you configure in the console
- **luigi**, **fvm**, **chaos** — supporting services

> **tengine uses host networking.** The ports of the sites you protect are bound **directly on the host** (80/443 by default) and must be free. The console itself is HTTPS with a self-signed certificate.

**Requirements:** Linux, **rootful podman**, at least 2 GB of RAM. The stack pins every bridge container to a fixed address inside a dedicated `/24`, which rootless podman cannot do; the addon refuses to install on macOS/Windows or rootless hosts.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `safeline` | Instance name (map key, container/network prefix and default subcommand name) |
| `--version` | `9.4.7` | SafeLine release tag: pulls `docker.io/chaitin/safeline-{mgt,detector,tengine,luigi,fvm,chaos}:<version>` |
| `--postgres-version` | `15.18` | Bundled PostgreSQL tag: pulls `docker.io/chaitin/safeline-postgres:<version>` |
| `--subnet-prefix` | `172.22.222` | First three octets of the stack's private `/24` bridge subnet. Each stack needs its own subnet; addresses are fixed above the gateway: `.1` gateway, `.2` postgres, `.4` mgt, `.5` detector, `.7` luigi, `.8` fvm, `.10` chaos |
| `--port` | auto (`9443`) | Console HTTPS host port. `0` = auto-assign from the SafeLine pool (`safeline_start_port`, default `9443`) |
| `--listen` | `0.0.0.0` | Console bind address, matching the upstream compose default. Set `127.0.0.1` to keep the console off the network |
| `--data-dir` | `<base_dir>/addon/safeline/<name>` | Stack data root (bundled database, console state, nginx config, logs). Must be absolute except for `base_dir`-relative paths |
| `--force` | false | Recreate the seven containers, keeping the data directory, and rotate the console admin password |

`--image`, `--dsn`, `--pg-name`, `--password`, `--config-option`, `--config-file`, `--tls`, `--tls-pem`, `--tls-cert`, `--tls-key`, `--advertise-host`, `--cluster`, `--peer` and `--join` do not apply to SafeLine and are rejected: the stack bundles its own PostgreSQL and every image is pinned by `--version`/`--postgres-version`.

## Install

```bash
pg addon install safeline --name waf1
```

The install pulls the seven images if needed, creates the directories and the dedicated bridge network, generates the bundled-database password, starts the containers in dependency order (postgres → mgt console → detector → fvm → chaos → luigi → tengine), waits for PostgreSQL, the console health endpoint and tengine's control socket, captures the console admin password from the `resetadmin` bootstrap, saves `pg.yaml`, and prints a summary:

```text
✓ SafeLine WAF "waf1" installed
  Console:     https://127.0.0.1:9443 (self-signed certificate)
  Admin:       admin
  Password:    <generated> (also stored: `pg addon password safeline --name waf1`)
  Version:     SafeLine 9.4.7, PostgreSQL 15.18
  Subnet:      172.22.222.0/24 (gateway 172.22.222.1)
  Containers:  postgres, mgt, detector, luigi, fvm, chaos (bridge pgcli-safeline-waf1); tengine (host network)
  Data dir:    /home/you/.pgcli/addon/safeline/waf1
  Traffic:     tengine listens on host networking — add protected sites in the console;
               their entry ports default to 80/443 and must be free on this host
```

The install also generates the internal detector management token (`detector_mgmt_token` in `pg.yaml`) and passes it to the detector, fvm and luigi containers. The detector image authenticates management-API calls by default, and the console publishes site rules through that API — without the shared token, creating a site fails. A stack installed by an older pgcli is migrated on its next `pg addon start`, which generates the token and recreates those containers.

A second instance needs its own subnet and console port (the pool advances automatically):

```bash
pg addon install safeline --name waf2 --subnet-prefix 172.22.223 --port 9444
```

Re-running the same install with no changed flags is a no-op; changing any stored value (version, subnet, port, listen, data dir) without `--force` is refused instead of silently ignored.

## Console

Open `https://<listen>:<port>` (default `https://<host>:9443`) and log in as `admin` with the generated password. Retrieve it later with:

```bash
pg addon password safeline --name waf1
```

The password is captured from the mgt container's `resetadmin` bootstrap, which **rotates the admin password on every call**. pgcli runs it only when no password is recorded (install, or a later `start` if the install failed short) and on `--force` re-installs. To rotate it yourself:

```bash
pg addon password safeline --name waf1 --reset
```

The command reruns the bootstrap, saves the new `admin_password` back to `pg.yaml` and prints it, so `pg addon password safeline --name waf1` keeps returning the current password. The stack must be running.

The console serves HTTPS with a self-signed certificate; browsers show the usual warning.

## Protected sites and traffic ports

Applications (protected sites) are configured **in the console** or, for scripting, through `pg addon safeline site` (below). Each site gets an entry port on the host — 80/443 by default — served by the host-networked **tengine** container. Before adding a site, make sure the port is free on the host (`ss -tlnp`). In the firewall, open the console port and each site's entry port (80 and 443 by default).

A site's upstream can be an IP or a host name (for example a load-balancer domain). tengine resolves host names from the host's DNS **when its config loads**, so after the load balancer's DNS records change, run `pg addon reload safeline --name waf1` to re-test and gracefully reload the tengine config — the new addresses take effect without restarting the stack or dropping in-flight requests.

Ansible deployments can declare sites in the `sites` field of a `type: safeline` entry (each run creates missing sites and updates changed upstreams; certificates are uploaded from controller file paths) — see the [Ansible add-ons chapter](../ansible/addons.md#safeline-sites).

### Managing sites from the CLI

`pg addon safeline site ...` drives the console's own web API with the stored admin password — the same encrypted login and `/api/open/site` calls the browser makes — so sites can be scripted without the UI. The stack must be running.

> **Community Edition limit: one upstream per site.** The console pgcli installs runs without a Pro license, so a site can proxy to a single upstream. Adding a second upstream — and therefore any load balancing across servers — is refused by the console with `license required`; activate a Pro license in the console to use it.

```bash
pg addon safeline site list --name waf1
pg addon safeline site create --name waf1 \
    --port 443_ssl --domain www.example.com \
    --upstream http://10.0.0.5:8080 \
    --comment "web tier" \
    --cert fullchain.pem --key privkey.pem \
    --health-check --health-check-interval 30 --health-check-path /healthz
pg addon safeline site update --name waf1 --site-id 3 --health-check=false
pg addon safeline site health --name waf1
pg addon safeline site delete --name waf1 --site-id 3

pg addon safeline cert list --name waf1
pg addon safeline cert create --name waf1 --cert fullchain.pem --key privkey.pem
pg addon safeline cert update --name waf1 --cert-id 2 --cert fullchain.pem --key privkey.pem
pg addon safeline cert delete --name waf1 --cert-id 2
```

- `--port` values are the host entry ports; suffix a port with `_ssl` to terminate TLS on it. `--cert`/`--key` upload a PEM certificate (leaf or full chain) and bind it in one step, `--cert-id` binds a certificate already stored in the console, and without either the console's default certificate is used. `--domain` defaults to `*` (any Host header).
- `--upstream` takes absolute `http://` or `https://` URLs; `--balance` is `roundrobin` (default), `leastconn`, `iphash` or `backup`. `site update` replaces the whole list with `--upstream`, or changes it incrementally with `--add-upstream` / `--remove-upstream`. The bundled console runs as the Community Edition, which permits **one upstream per site** — a second one is refused with `license required`; load balancing across several upstreams needs a Pro license.
- `--health-check` probes every upstream over HTTP every 10–3600 seconds at the given path. `site update` changes only the settings you pass (upstreams, health check and/or the bound certificate), leaving everything else at its console values.
- `site health` probes the selected sites (all of them by default) and prints `healthy` or the console's error per upstream; it exits non-zero when any upstream is unhealthy, so it works as a monitoring gate. `site list` shows the site ids that `update`, `delete` and `health` take via `--site-id`.
- `cert list` shows stored certificates with their covered domains, expiry and the sites using them. The console refuses to delete a certificate while a site's TLS entry port uses it, so delete or repoint the site first.

A plain HTTP site needs no certificate — just an entry port and an upstream:

```bash
pg addon safeline site create --name waf1 \
    --port 80 --domain www.example.com \
    --upstream http://10.0.0.5:8080
```

tengine binds port 80 directly on the host, so it must be free (see `ss -tlnp`).

To change an existing site's upstreams — replacing the list with a single upstream works on any edition, while adding/removing servers and multi-upstream lists need a Pro license:

```bash
# add a server, keeping the current ones (Pro)
pg addon safeline site update --name waf1 --site-id 3 --add-upstream http://10.0.0.7:8080

# drop a server (Pro, once more than one is configured)
pg addon safeline site update --name waf1 --site-id 3 --remove-upstream http://10.0.0.5:8080

# replace the whole list: one URL works on any edition, several need Pro
pg addon safeline site update --name waf1 --site-id 3 \
    --upstream http://10.0.0.6:8080 --upstream http://10.0.0.7:8080
```

Binding a certificate to a TLS site — either upload it once and bind by id, or upload and bind in one step:

```bash
# upload once, reuse by id (the id is printed and shown by `cert list`)
pg addon safeline cert create --name waf1 --cert fullchain.pem --key privkey.pem
pg addon safeline site create --name waf1 --port 443_ssl --domain www.example.com \
    --upstream http://10.0.0.5:8080 --cert-id 2

# upload and bind in one step (the certificate is rolled back if site creation fails)
pg addon safeline site create --name waf1 --port 443_ssl --domain www.example.com \
    --upstream http://10.0.0.5:8080 --cert fullchain.pem --key privkey.pem
```

Renewing is two different operations, depending on what changed:

```bash
# same certificate renewed (expiry approaching): replace the PEM in place under
# the existing id — every site bound to it serves the new certificate without
# being touched and without a reload
pg addon safeline cert update --name waf1 --cert-id 2 --cert new-fullchain.pem --key new-privkey.pem

# the site should serve a different certificate altogether: store the new one
# (or reuse an id from `cert list`) and rebind the site
pg addon safeline cert create --name waf1 --cert other-fullchain.pem --key other-privkey.pem  # -> id 5
pg addon safeline site update --name waf1 --site-id 3 --cert-id 5
```

Custom error pages, ACL/rate-limit options and upstream weights/backup roles stay in the console UI.

## Container layout and networking

| Role | Network | Address | Purpose |
|---|---|---|---|
| `postgres` | bridge `pgcli-safeline-<name>` | `<subnet>.2` | Bundled PostgreSQL 15.18 |
| `mgt` | bridge | `<subnet>.4` | Console (container port `1443`, published on `--port`) |
| `detector` | bridge | `<subnet>.5` | Detection engine |
| `luigi` | bridge | `<subnet>.7` | Console support service |
| `fvm` | bridge | `<subnet>.8` | File/version service |
| `chaos` | bridge | `<subnet>.10` | Traffic simulation service |
| `tengine` | host | — | Traffic proxy; site ports configured in the console |

The bridge network is created with `--subnet <prefix>.0/24 --gateway <prefix>.1` and owned by the instance (label `pgcli.safeline=<name>`); an existing network with another subnet or owner is an error. Containers use `--restart unless-stopped` (so `pg addon stop` sticks) and a 30-second stop timeout; bridge containers drop `NET_RAW` and set `no-new-privileges`, tengine sets `no-new-privileges` and raises its open-file ulimit as upstream does.

## Data layout

Everything lives under `--data-dir` (`<base_dir>/addon/safeline/<name>/` by default):

```text
<data-dir>/
├── resources/
│   ├── postgres/data   # bundled PostgreSQL data
│   ├── postgres/password  # bundled DB password (0600), reused on re-install
│   ├── mgt/            # console state
│   ├── detector/ nginx/ chaos/ cache/ luigi/
│   └── sock/ run/      # unix sockets shared between containers
└── logs/
    ├── nginx/          # console + tengine access/error logs
    └── detector/
```

The containers run as root inside the container, so these directories are root-owned. Treat the tree as opaque and back it up as a whole if you need to preserve console configuration. PostgreSQL bakes its password into the cluster on first init, so pgcli records it in `resources/postgres/password`; that is what lets `pg addon remove` (without `--clean-data`) followed by a re-install reattach to the surviving data.

## Lifecycle, logs and autostart

```bash
pg addon list                                   # security add-on section: n/7 running, console URL
pg addon start safeline --name waf1             # start the stack (creates missing containers)
pg addon stop safeline --name waf1              # stop in reverse order
pg addon logs safeline --name waf1              # mgt console log (default service)
pg addon logs safeline --name waf1 --service tengine -f   # traffic proxy log
pg addon reload safeline --name waf1            # nginx -t + graceful tengine reload (re-resolves site upstream DNS)
```

`--service` accepts `postgres`, `mgt`, `detector`, `tengine`, `luigi`, `fvm` or `chaos`; the default is `mgt`. `start` waits for PostgreSQL and the console health endpoint — and for tengine's control socket when tengine (re)starts — before returning.

Autostart starts the whole stack on boot in dependency order, and recovers the console password if it was never captured:

```bash
pg autostart enable --safeline --name waf1
```

The protected sites and their traffic ports are configured in the console or with `pg addon safeline site`, not through autostart.

## Uninstall

```bash
pg addon remove safeline --name waf1             # remove containers and the network, keep data
pg addon remove safeline --name waf1 --clean-data   # also delete <data-dir>
```

The dedicated bridge network is removed once no containers are attached. Without `--clean-data` the data directory (bundled PostgreSQL data and console state) is kept so a re-install can resume.

## Troubleshooting

- **“SafeLine requires rootful podman” / “SafeLine requires Linux”** — the stack needs static container addresses and host-network port binding. Run podman as root on Linux; rootless and macOS/Windows are refused up front.
- **“network … already exists with subnet …”** — another stack or a stale network owns that subnet or name. Install with a different `--subnet-prefix`/`--name`, or remove the stale network first.
- **Console does not become healthy** — check `pg addon logs safeline --name <name> --service mgt` and `--service postgres`. The first boot runs schema migrations and can take a minute.
- **Site ports busy** — tengine binds protected sites directly on the host; free the port or pick another entry port in the console.
- **Password lost** — run `pg addon password safeline --name <name> --reset`; it rotates the console password, saves it to the stored `admin_password` and prints the new one (the stack must be running).
- **Images** — the SafeLine CE images are published for `linux/amd64` only; the stack runs on amd64 hosts.

## Limitations

- **Linux + rootful podman only** (static IPs; host-network traffic proxy).
- **One upstream per site (Community Edition).** The bundled console runs without a Pro license, so a site proxies to a single upstream: `--add-upstream`, a multi-URL `--upstream`, weighted/backup upstreams and load balancing are refused with `license required`. Activate a Pro license in the console to use them.
- Only the console port is published by pgcli; per-site traffic ports are bound by tengine and created through the console or `pg addon safeline site`.
- The console uses SafeLine's own self-signed certificate; no `--tls` integration.
- pgcli generates and manages the internal detector management token; the other optional upstream secrets (`MGT_INTERNAL_TOKEN`, `DETECTOR_AES_KEY`, `MGT_LICENSE_INTEGRITY_KEY`, `RIVER_*`) are left at their upstream defaults.
- SafeLine keeps its state in the bundled PostgreSQL inside `--data-dir`; pgcli's PITR/backup add-ons target pgcli instances and do not back this database up.
