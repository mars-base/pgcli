---
title: "Node_exporter"
description: "Run node_exporter, the official Prometheus host-metrics exporter, as a managed container."
weight: 61
---

`pg addon install nodeexporter` runs [node_exporter](https://github.com/prometheus/node_exporter) — the official Prometheus exporter that publishes the **host's** CPU, memory, disk, network and filesystem metrics at `/metrics` — as a managed container from the official `docker.io/prom/node-exporter` image. Like Prometheus, VictoriaMetrics and Grafana it is a top-level addon: it describes the machine, not one PostgreSQL instance, so it is attached to none of them. It is the natural companion to a pgcli [Prometheus](../prometheus/) addon — node_exporter exposes, Prometheus scrapes.

node_exporter shares VictoriaMetrics's shape — **single container, no config file** (everything is CLI flags after the image name), no hot reload, no authentication — but it has one requirement no other web addon does, described below.

> **The non-negotiable: host mounts.** A container reading its own `/proc`
> reports the CONTAINER's metrics, not the host's. To export the machine, pgcli
> hard-wires three **read-only** bind mounts — host `/proc` → `/host/proc`,
> `/sys` → `/host/sys`, `/` → `/rootfs` — and points the binary at them with
> `--path.procfs` / `--path.sysfs` / `--path.rootfs`. These are fixed in pgcli,
> not user-configurable; without them the addon would silently export the wrong
> numbers. They are mounted `:ro` only (never `:z`), so pgcli never relabels the
> host root filesystem.

> **There is NO login/password and no TLS.** node_exporter just serves `/metrics`
> in plain text. Because Prometheus scraping it is usually on a **different
> host**, `Listen` defaults to `0.0.0.0` — anyone who can reach the port can read
> detailed host facts (hardware, mounts, network). Restrict it to your monitoring
> network, or pass `--listen 127.0.0.1` for a same-host Prometheus.

> **Platform**: Linux is the tested platform (the container uses host
> networking, so the exporter binds the host port directly). The design supports
> macOS (bridge + published port), but it has not been validated there yet.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `nodeexporter` | Instance name (map key) |
| `--image` | `docker.io/prom/node-exporter:v1.12.1` | Image override |
| `--port` | auto (base 9100) | Host port; auto-allocated from `nodeexporter_start_port` |
| `--listen` | `0.0.0.0` | Bind address — every interface by default (cross-host Prometheus); use `127.0.0.1` for loopback-only |
| `--extra-arg` | — | Extra CLI flag passed to the binary (repeatable); appended last so last-wins overrides earlier flags. E.g. `--extra-arg --no-collector.cpufreq` |
| `--force` | false | Recreate even when running — to apply changed ports / listen / extra-args |

node_exporter is **stateless** — it stores nothing, so there is no data directory. `pg addon remove` deletes only the container; reinstalling always starts fresh.

## Install

```bash
pg addon install nodeexporter
```

The command pulls the image if needed, creates the container with the three host mounts, and prints the scrape URL:

```
✓ node_exporter installed: "nodeexporter"
  Container:  pgcli-nodeexporter-default-nodeexporter
  Image:      docker.io/prom/node-exporter:v1.12.1
  Metrics:    http://0.0.0.0:9100/metrics
  Host mounts: /proc, /sys, / (read-only) → /host/proc, /host/sys, /rootfs
```

Because the default bind is `0.0.0.0`, the summary also prints an exposure warning and the matching Prometheus command. Verify the exporter serves real host numbers:

```bash
curl -s http://127.0.0.1:9100/metrics | grep -E '^node_(memory_MemAvailable_bytes|load1|cpu_seconds_total)'
```

The values should track the host (`free -b`, `cat /proc/loadavg`) — if they track the container instead, a mount is missing (they cannot be, via pgcli, but a hand-run `node_exporter` often gets this wrong).

### Wire it into Prometheus

The exporter is only useful once something scrapes it. A pgcli Prometheus instance does, via a one-line `--scrape` job (no service discovery, no Prometheus config edits):

```bash
pg addon install prometheus --name p1 --scrape node-exporter=127.0.0.1:9100
# remote Prometheus → use the host's LAN address instead of 127.0.0.1:
pg addon install prometheus --name p1 --scrape node-exporter=<host-ip>:9100
```

To add this exporter to a pgcli Prometheus that is **already running and already
scraping other jobs**, use `--scrape-append` instead — it folds the `node-exporter`
job into the stored scrape list (merging same-named jobs, appending unseen ones)
and SIGHUP-reloads, so it never wipes the existing targets:

```bash
pg addon install prometheus --name p1 --scrape-append node-exporter=127.0.0.1:9100
```

(A plain `--scrape` **replaces** the whole job set, so re-running install with it
would drop any other jobs p1 was scraping.) Then in Prometheus,
`up{job="node-exporter"}` is `1` and `count(node_cpu_seconds_total)` is large. See
the [Prometheus addon](../prometheus/) for the full `--scrape` / `--scrape-append`
mechanics.

Re-running `install` on a running instance **recreates** it when the config changed (port, listen, extra-args), and leaves it alone otherwise; `--force` recreates even when unchanged. node_exporter has **no SIGHUP reload**, so every change requires container recreation.

## Custom flags (`--extra-arg`)

node_exporter has dozens of collector flags. Toggle individual collectors off/on, or add `--collector.*`/`--no-collector.*` switches, via `--extra-arg` (repeatable):

```bash
pg addon install nodeexporter --extra-arg --no-collector.cpufreq \
                              --extra-arg --collector.netdev.device-exclude='^veth.*'
```

Extra args are appended **last** to the command line, so they override pgcli's injected defaults via last-wins (including pgcli's own `--collector.filesystem.mount-points-exclude`, which drops the pseudo-filesystems pgcli just mounted so `node_filesystem_*` isn't all noise). There is no config file to edit.

## Listening and exposure

`Listen` defaults to `0.0.0.0`: on Linux the container shares the host network namespace, so `/metrics` is reachable on every interface at the assigned port. This is deliberate — a Prometheus scraping node_exporter is usually on another machine. But there is **no authentication**, and node_exporter exposes fine-grained host detail (mount table, network interfaces, hardware). For a same-host Prometheus, pass `--listen 127.0.0.1` (recreate with `--force`); otherwise restrict the port at the firewall. (Under the macOS bridge build, pgcli widens the bind to `0.0.0.0` automatically so the published port stays reachable through the podman VM.)

## Ports

Each node_exporter instance owns a single port — the metrics endpoint — allocated from a pool with base `nodeexporter_start_port` (default `9100`, node_exporter's conventional port), incrementing for each additional instance and skipping ports taken by any managed container or `used_ports`. The allocation is saved to `pg.yaml`, so a reinstall never re-hands-out a port another addon holds. `9100` shares no number with Prometheus (`9090`), VictoriaMetrics (`8428`), Grafana (`3000`), Alertmanager (`9093`), or nginx (`8080`); an explicit `--port` still wins.

```bash
pg addon install nodeexporter --name ne2            # second instance: next free port (9101)
pg addon install nodeexporter --name ne3 --port 9200  # explicit port takes precedence
```

## Configuration

```yaml
addons:
  nodeexporter:
    nodeexporter:
      name: nodeexporter
      image_tag: docker.io/prom/node-exporter:v1.12.1
      host_port: 9100
      listen: 0.0.0.0
      extra_args:
        - --no-collector.cpufreq
      autostart: false
```

There is **no config file** — everything is passed as CLI flags to the `node_exporter` binary, and the three host mounts are fixed by pgcli. `extra_args` is a list of flags appended last.

### List

```bash
pg addon list
```

```
Web add-ons (nodeexporter):
  nodeexporter (name: nodeexporter)
    Status:      running
    Metrics:     http://0.0.0.0:9100/metrics
    Extra args:  --no-collector.cpufreq
    Image:       docker.io/prom/node-exporter:v1.12.1
    Container:   pgcli-nodeexporter-default-nodeexporter
```

There are no credentials to reveal — node_exporter has none.

## Start and stop

Containers run with podman's `unless-stopped` restart policy, so they come back with podman after a host reboot. `start`/`stop` manage them explicitly:

```bash
pg addon stop nodeexporter
pg addon start nodeexporter
```

## Auto-start on Boot

To bring a node_exporter instance up on host boot:

```bash
pg autostart enable --nodeexporter --name nodeexporter
```

This flags the instance in `pg.yaml` and installs/refreshes a boot service (a systemd `--user` unit on Linux, a LaunchAgent on macOS) that runs `pg start --autostart`. A boot-time start only brings the container up — node_exporter is stateless, so there is nothing to prepare. See [Autostart]({{< relref "../autostart" >}}) for the full mechanism, status and disabling.

## Remove

```bash
pg addon remove nodeexporter   # deletes the container; there is no data to clean
```

node_exporter keeps nothing on disk, so `--clean-data` has no effect beyond an informational note.

## Logs

```bash
pg logs addon nodeexporter            # show recent console output
pg logs addon nodeexporter -f         # follow
pg logs addon nodeexporter -n 200     # last 200 lines (also -i / --tail)
```

## Troubleshooting

- **Metrics look like the container's, not the host's** — not possible via `pg addon install` (the mounts are hard-wired), but if you ever run `node_exporter` by hand you must pass the `/proc`/`/sys`/`/` read-only mounts and the matching `--path.*` flags. Confirm with `curl -s …/metrics | grep node_uname_info` and compare `node_memory_MemAvailable_bytes` to the host `free -b`.
- **Prometheus shows the target DOWN** — check the exporter is running (`pg addon list`), that the scrape target address/port match (`--scrape node-exporter=<host>:<port>`), and that a loopback `--listen 127.0.0.1` exporter isn't being scraped from another host.
- **A collector flag didn't take effect** — `--extra-arg` is applied at container creation. Reinstall with `--force` to recreate, then check `pg logs addon nodeexporter` for the flag on the startup line.
- **Port already allocated** — the assigned port is held in `pg.yaml` and skipped for later instances; free it with `pg addon remove` first, or pick another with `--port N`.

## Known limitations

- **No authentication, no TLS.** node_exporter serves plain-text `/metrics`; the only protection for a `0.0.0.0` bind is the network boundary. Front with nginx or restrict by firewall if it must leave a trusted network.
- **Stateless** — no data dir, no retention; nothing survives beyond the running process. Reinstall always starts fresh.
- **Rootful assumption.** The `-v /:/rootfs:ro` mount reads the whole host tree; under rootless podman some host paths may be unreadable. All target VMs run rootful podman, matching the rest of pgcli.
- pgcli does not validate `--extra-arg` values; a bad flag surfaces only in the container logs on startup.
- On macOS the container runs inside the podman machine with the port published; validated on Linux only.
