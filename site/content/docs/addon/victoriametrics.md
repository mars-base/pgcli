---
title: "VictoriaMetrics"
description: "Run VictoriaMetrics, a fast Prometheus-compatible time-series database, as a managed container."
weight: 57
---

`pg addon install victoriametrics` runs [VictoriaMetrics](https://victoriametrics.com/) — a fast, Prometheus-compatible time-series database — as a managed container from the official `docker.io/victoriametrics/victoria-metrics` image. Like Prometheus and Grafana it is a top-level addon: it fronts no single PostgreSQL instance (it ingests metrics from many sources), so it is not attached to any one of them. The three are natural partners — Prometheus scrapes, VictoriaMetrics stores long-term with superior compression, Grafana visualises — and share the same lifecycle shape (single container, host networking on Linux, one web port from its own pool, persistent data dir, autostart, logs).

VictoriaMetrics diverges from Prometheus in **how it is configured**. Prometheus reads a config **file** (`prometheus.yml`) plus CLI flags; VictoriaMetrics needs **no config file at all** — everything is passed as **CLI flags after the image name** (`-storageDataPath` / `-httpListenAddr` / `-retentionPeriod` / custom flags via `--extra-arg`). So pgcli passes these flags directly when creating the container; there is no config to render, no SIGHUP reload, no chown needed (the image runs as root).

> **There is NO web login/password.** Unlike Grafana (whose `0.0.0.0` default is
> guarded by a generated strong password), VictoriaMetrics ships **no authentication
> at all**. Its HTTP API runs arbitrary MetricsQL (a superset of PromQL) and can be
> loaded by expensive queries, so a remote-exposed instance is guarded **only by the
> network boundary** — your firewall, or `--listen 127.0.0.1` for loopback. Treat
> exposing it like exposing an unauthenticated read/write console, not like a
> password-protected dashboard.

> **Platform**: Linux is the tested platform. The design supports macOS (the
> image is multi-arch; the container runs under the podman machine VM on the
> `pgcli-net` bridge with the port published), but it has not been validated
> there yet.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `vm` | Instance name (map key) |
| `--image` | `docker.io/victoriametrics/victoria-metrics:v1.154.1` | Image override |
| `--port` | auto (base 8428) | Host web port; auto-allocated from `victoriametrics_start_port` |
| `--listen` | `0.0.0.0` | Bind address — defaults to every interface; use `127.0.0.1` for loopback-only (see the exposure warning above) |
| `--retention-months` | `1` | Data retention in months (`-retentionPeriod`); e.g. `3` = 3 months, `12` = 1 year |
| `--extra-arg` | — | Extra CLI flag passed to the binary (repeatable); appended last so last-wins overrides earlier flags. E.g. `--extra-arg -search.maxUniqueTimeseries=1000000` |
| `--data-dir` | auto | Data directory override (default under the base dir) |
| `--force` | false | Recreate even when running — to apply changed ports / listen / retention / extra-args (install also recreates on its own when config changes) |

The data directory holds VictoriaMetrics's TSDB. It survives `pg addon remove` (delete it with `--clean-data`), so reinstalling under the same name revives stored time series. The image runs as **root** (unlike Prometheus's uid 65534), so no pre-chown of the data dir is needed.

## Install

```bash
pg addon install victoriametrics
```

The command pulls the image if needed, creates the container, and prints the URL, retention, and any extra args:

```
✓ VictoriaMetrics installed: "vm"
  Container:  pgcli-victoriametrics-default-vm
  Image:      docker.io/victoriametrics/victoria-metrics:v1.154.1
  Data:       /home/you/.pgcli/addon/victoriametrics/vm/data
  Retention:  1 month(s)
  URL:        http://0.0.0.0:8428/
```

VictoriaMetrics is ready to ingest and query immediately. Point Grafana at `http://<host>:8428/` as a Prometheus datasource (VictoriaMetrics is Prometheus-compatible) and you can query using PromQL or MetricsQL.

```bash
# custom retention and search limits
pg addon install victoriametrics --name vm1 \
  --retention-months 12 \
  --extra-arg -search.maxUniqueTimeseries=1000000
```

Re-running `install` on a running instance **recreates** it when the config actually changed (ports, listen, retention, extra-args), and leaves it alone when it didn't; `--force` recreates the container even when config is unchanged.

VictoriaMetrics does **not** support hot config reload (SIGHUP), so all changes require container recreation. This is simpler than Prometheus (no config file to render, no reload signal), but means brief interruption when changing settings.

## Custom flags (`--extra-arg`)

VictoriaMetrics accepts many CLI flags for tuning cache sizes, query limits, ingestion rates, and more. Pass them via `--extra-arg` (repeatable):

```bash
pg addon install victoriametrics --name vm1 \
  --extra-arg -search.maxUniqueTimeseries=1000000 \
  --extra-arg -selfScrapeInterval=10s
```

Extra args are appended **last** to the container's command line, so they override earlier flags via last-wins semantics. This lets you tune VictoriaMetrics beyond pgcli's first-class knobs without editing any config file.

Verify the flags took effect:

```bash
podman logs pgcli-victoriametrics-default-vm1 | grep -E '(maxUniqueTimeseries|selfScrapeInterval)'
# 2026-10-22T...  -search.maxUniqueTimeseries="1000000"
# 2026-10-22T...  -selfScrapeInterval="10s"
```

## Listening and exposure

VictoriaMetrics's `Listen` defaults to `0.0.0.0`: the container shares the host's network namespace, so the UI and API are reachable on every interface at the assigned port. This matches Prometheus's default for the same practical reason (Grafana on another host needs to reach it) — but **there is no password behind it**. Anyone who can reach the port can run arbitrary MetricsQL and read your stored time series. For a loopback-only deployment pass `--listen 127.0.0.1` (recreate with `--force`); `pg addon list` then shows `http://127.0.0.1:<port>/`. Otherwise, restrict access at the firewall.

## Ports

Each VictoriaMetrics instance owns a single port: the web UI/API. It is allocated from a pool with base `victoriametrics_start_port` (default `8428` — VictoriaMetrics's conventional HTTP port), incrementing for every additional instance and skipping ports already taken by any managed container or the `used_ports` list. The allocation is written into `pg.yaml`, so a reinstall never re-hands-out a port another addon is using. `8428` shares no number with Prometheus (`9090`), Grafana (`3000`), pgAdmin (`5050`), or nginx (`8080`); an explicit `--port` still wins.

```bash
pg addon install victoriametrics --name vm2          # second instance: next free port (8429 if 8428 taken)
pg addon install victoriametrics --name vm3 --port 8500  # explicit port still takes precedence
```

## Configuration

```yaml
addons:
  victoriametrics:
    vm1:
      name: vm1
      image_tag: docker.io/victoriametrics/victoria-metrics:v1.154.1
      host_port: 8428
      listen: 0.0.0.0
      retention: "1"
      extra_args:
        - -search.maxUniqueTimeseries=1000000
        - -selfScrapeInterval=10s
      autostart: false
      # data_dir: /srv/victoriametrics-data      # optional, overrides the default location
```

There is **no config file** — everything is passed as CLI flags to the `victoria-metrics` binary (`-storageDataPath`, `-httpListenAddr`, `-retentionPeriod`, plus any `extra_args`). `retention` is stored as a string (months), and `extra_args` is a list of CLI flags appended last.

### List

```bash
pg addon list
```

```
Web add-ons (victoriametrics):
  victoriametrics (name: vm1)
    Status:      running
    URL:         http://0.0.0.0:8428/
    Retention:   1 month(s)
    Extra args:  -search.maxUniqueTimeseries=1000000 -selfScrapeInterval=10s
    Data:        /home/you/.pgcli/addon/victoriametrics/vm1/data
    Image:       docker.io/victoriametrics/victoria-metrics:v1.154.1
    Container:   pgcli-victoriametrics-default-vm1
```

There are no credentials to reveal — VictoriaMetrics has none — so `--show-password` shows nothing extra for it.

## Start and stop

Containers run with podman's `unless-stopped` restart policy, so they come back automatically with the podman service after a host reboot. `start`/`stop` manage them explicitly:

```bash
pg addon stop victoriametrics
pg addon start victoriametrics
```

## Auto-start on Boot

To have a VictoriaMetrics instance started automatically on host boot, enable it:

```bash
pg autostart enable --victoriametrics --name vm1
```

This flags the instance in `pg.yaml` and installs/refreshes a boot service (a systemd `--user` unit on Linux, a LaunchAgent on macOS) that runs `pg start --autostart`. A boot-time start only brings the container up — the data dir was already created at install time, so it needs no host privileges beyond podman itself. VictoriaMetrics starts after Prometheus in the boot order. See [Autostart]({{< relref "../autostart" >}}) for the full mechanism, status and disabling.

## Remove

```bash
pg addon remove victoriametrics              # keeps the TSDB — stored time series survive a reinstall
pg addon remove victoriametrics --clean-data # deletes the TSDB too
```

Under rootless podman, `--clean-data` deletes the data dir through `podman unshare rm` — VictoriaMetrics writes as root (container uid 0), which maps to a subordinate uid the host user cannot `rm` directly.

## Logs

```bash
pg logs addon victoriametrics            # show recent console output
pg logs addon victoriametrics -f         # follow
pg logs addon victoriametrics -n 200     # last 200 lines (also -i / --tail)
```

## Troubleshooting

- **A query returns no data** — confirm VictoriaMetrics is running (`pg addon list` shows `running`), the health endpoint responds (`curl -s http://<host>:<port>/-/healthy` prints `VictoriaMetrics is Healthy.`), and you're querying the correct time range (use `query_range` with explicit `start`/`end`/`step`). Check `pg logs addon victoriametrics` for ingestion errors.
- **Install fails with "container already exists"** — the container name is derived from `--name` and the namespace; use `--force` to recreate, or `pg addon remove` first to clean up a partial install.
- **Extra args didn't take effect** — `--extra-arg` is applied at container creation time, not runtime. Reinstall with `--force` to recreate the container with the new flags. Check `podman logs <container>` to confirm the flags appear in the startup log.
- **Port already allocated** — the assigned port is held in `pg.yaml` and skipped for later instances; free it with `pg addon remove` first, or pick another with `--port N`.
- **Remote API unreachable / unexpectedly reachable** — on the default `0.0.0.0` a failure to reach it is host firewall, not bind address; conversely, if you did *not* want it reachable, `--listen 127.0.0.1 --force`.

## Known limitations

- v1 is a bare storage backend: it ships no scrape capability (point a pgcli Prometheus instance at it with `pg addon install prometheus --remote-write-url http://<host>:8428/api/v1/write`, or use `vmagent` separately) and no alerting. It is the long-term storage layer you point Prometheus and Grafana at.
- **No authentication.** VictoriaMetrics's API runs arbitrary MetricsQL; the only protection for a `0.0.0.0` bind is the network boundary. This is the same posture as Prometheus — do not expose it on an untrusted network.
- pgcli does not validate `--extra-arg` values (there is no pre-flight check at install). A bad flag surfaces only in the container logs on startup.
- On macOS the container runs inside the podman machine; it has been validated on Linux only.
