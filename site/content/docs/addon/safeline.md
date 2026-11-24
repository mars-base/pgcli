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

The install pulls the seven images if needed, creates the directories and the dedicated bridge network, generates the bundled-database password, starts the containers in dependency order (postgres → mgt console → detector → fvm → chaos → luigi → tengine), waits for PostgreSQL and the console health endpoint, captures the console admin password from the `resetadmin` bootstrap, saves `pg.yaml`, and prints a summary:

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

Applications (protected sites) are configured **in the console**, not with pgcli flags. Each site gets an entry port on the host — 80/443 by default — served by the host-networked **tengine** container. Before adding a site, make sure the port is free on the host (`ss -tlnp`). In the firewall, open the console port and each site's entry port (80 and 443 by default).

A site's upstream can be an IP or a host name (for example a load-balancer domain). tengine resolves host names from the host's DNS **when its config loads**, so after the load balancer's DNS records change, run `pg addon reload safeline --name waf1` to re-test and gracefully reload the tengine config — the new addresses take effect without restarting the stack or dropping in-flight requests.

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

`--service` accepts `postgres`, `mgt`, `detector`, `tengine`, `luigi`, `fvm` or `chaos`; the default is `mgt`. `start` waits for PostgreSQL and the console health endpoint before returning.

Autostart starts the whole stack on boot in dependency order, and recovers the console password if it was never captured:

```bash
pg autostart enable --safeline --name waf1
```

The protected sites and their traffic ports are still configured in the console, not through autostart.

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
- Only the console port is published by pgcli; the per-site traffic ports are managed in the console.
- The console uses SafeLine's own self-signed certificate; no `--tls` integration.
- The optional upstream secrets (`MGT_INTERNAL_TOKEN`, `DETECTOR_AES_KEY`, `MGT_LICENSE_INTEGRITY_KEY`, `RIVER_*`) are left at their upstream defaults; pgcli does not manage them.
- SafeLine keeps its state in the bundled PostgreSQL inside `--data-dir`; pgcli's PITR/backup add-ons target pgcli instances and do not back this database up.
