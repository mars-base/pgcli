---
title: "VictoriaLogs"
description: "Run VictoriaLogs, the log database of the VictoriaMetrics family, as a managed container."
weight: 59
---

`pg addon install victorialogs` runs [VictoriaLogs](https://docs.victoriametrics.com/victorialogs/) — a fast, cost-effective log database from the VictoriaMetrics family — as a managed container from the official `docker.io/victoriametrics/victoria-logs` image. Like VictoriaMetrics and Prometheus it is a top-level addon: it fronts no single PostgreSQL instance (it ingests logs from many sources), so it is not attached to any one of them. VictoriaMetrics keeps metrics, VictoriaLogs keeps logs, Grafana visualises both — and all three share the same lifecycle shape (single container, host networking on Linux, one web port from its own pool, persistent data dir, restart policy).

VictoriaLogs is the exact mirror of the [VictoriaMetrics addon]({{< relref "../addon/victoriametrics" >}}) in **how it is configured**: it needs **no config file at all** — everything is passed as **CLI flags after the image name** (`-storageDataPath` / `-httpListenAddr` / `-retentionPeriod` / custom flags via `--extra-arg`). So pgcli passes these flags directly when creating the container; there is no config to render, no SIGHUP reload, no chown needed (the image runs as root). One container serves everything on a single HTTP port: ingestion (JSON Lines, Elasticsearch-compatible bulk, syslog, OTLP), LogsQL queries, and the built-in UI.

> **There is NO web login/password.** VictoriaLogs ships **no authentication at
> all**. Its HTTP API can ingest and query arbitrary logs and be loaded by
> expensive LogsQL queries, so a remote-exposed instance is guarded **only by
> the network boundary** — your firewall, or `--listen 127.0.0.1` for loopback.
> Treat exposing it like exposing an unauthenticated read/write console, not
> like a password-protected dashboard.

> **Platform**: Linux is the tested platform. The design supports macOS (the
> image is multi-arch; the container runs under the podman machine VM on the
> `pgcli-net` bridge with the port published), but it has not been validated
> there yet.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `vl` | Instance name (map key) |
| `--image` | `docker.io/victoriametrics/victoria-logs:v1.53.0` | Image override |
| `--port` | auto (base 9428) | Host web port; auto-allocated from `victorialogs_start_port` |
| `--listen` | `0.0.0.0` | Bind address — defaults to every interface; use `127.0.0.1` for loopback-only (see the exposure warning above) |
| `--retention` | binary default | Data retention as a duration (`30d`, `3months`, `1y`, ...), passed verbatim as `-retentionPeriod`. Omitting it keeps the victoria-logs default |
| `--extra-arg` | — | Extra CLI flag passed to the binary (repeatable); appended last so last-wins overrides earlier flags. E.g. `--extra-arg -search.maxQueryDuration=60s` |
| `--data-dir` | auto | Data directory override (default under the base dir) |
| `--force` | false | Recreate even when running — to apply changed ports / listen / retention / extra-args |

The data directory holds VictoriaLogs's log storage. It survives `pg addon remove` (delete it with `--clean-data`), so reinstalling under the same name revives stored logs. The image runs as **root** (unlike Prometheus's uid 65534), so no pre-chown of the data dir is needed.

## Install

```bash
pg addon install victorialogs
```

The command pulls the image if needed, creates the container, and prints the URL and retention:

```
✓ VictoriaLogs installed: "vl"
  Container:  pgcli-victorialogs-default-vl
  Image:      docker.io/victoriametrics/victoria-logs:v1.53.0
  Data:       /home/you/.pgcli/addon/victorialogs/vl/data
  Retention:  binary default (--retention to change)
  URL:        http://0.0.0.0:9428/
```

VictoriaLogs is ready to ingest and query immediately. Open `http://<host>:9428/` for the built-in UI, or query via the HTTP API:

```bash
# ingest a JSON Lines log entry (the JSON content type is REQUIRED —
# without it VictoriaLogs answers 200 but silently drops the payload)
echo '{"_msg":"hello from pgcli","app":"demo"}' | \
  curl -s -X POST -H 'Content-Type: application/json' \
    http://127.0.0.1:9428/insert/jsonline --data-binary @-

# query with LogsQL
curl -s http://127.0.0.1:9428/select/logsql/query \
  --data-urlencode 'query=app:demo' --data-urlencode 'limit=10'
```

Re-running `install` on a running instance **recreates** it when the config actually changed (ports, listen, retention, extra-args), and leaves it alone when it didn't; `--force` recreates the container even when config is unchanged.

VictoriaLogs does **not** support hot config reload, so all changes require container recreation — same as VictoriaMetrics. This is simpler than Prometheus (no config file to render, no reload signal), but means brief interruption when changing settings.

## Custom flags (`--extra-arg`)

VictoriaLogs accepts many CLI flags for tuning cache sizes, query limits, ingestion rates, and more. Pass them via `--extra-arg` (repeatable):

```bash
pg addon install victorialogs --name vl1 \
  --extra-arg -search.maxQueryDuration=60s \
  --extra-arg -memory.allowedPercent=80
```

Extra args are appended **last** to the container's command line, so they override earlier flags via last-wins semantics. This lets you tune VictoriaLogs beyond pgcli's first-class knobs without editing any config file. Verify the flags took effect (the binary does not print them in its logs):

```bash
podman inspect pgcli-victorialogs-default-vl1 --format '{{json .Config.Cmd}}'
```

## Listening and exposure

VictoriaLogs's `Listen` defaults to `0.0.0.0`: the container shares the host's network namespace, so the UI and API are reachable on every interface at the assigned port. This matches VictoriaMetrics's default for the same practical reason (log shippers and Grafana on other hosts need to reach it) — but **there is no password behind it**. Anyone who can reach the port can run arbitrary LogsQL and read your stored logs. For a loopback-only deployment pass `--listen 127.0.0.1` (recreate with `--force`); `pg addon list` then shows `http://127.0.0.1:<port>/`. Otherwise, restrict access at the firewall.

## Ports

Each VictoriaLogs instance owns a single port: the web UI/API. It is allocated from a pool with base `victorialogs_start_port` (default `9428` — VictoriaLogs's conventional HTTP port), incrementing for every additional instance and skipping ports already taken by any managed container or the `used_ports` list. The allocation is written into `pg.yaml`, so a reinstall never re-hands-out a port another addon is using. `9428` shares no number with VictoriaMetrics (`8428`), vmcluster (`8480-8484`), Prometheus (`9090`), Grafana (`3000`), pgAdmin (`5050`), or nginx (`8080`); an explicit `--port` still wins.

```bash
pg addon install victorialogs --name vl2          # second instance: next free port (9429 if 9428 taken)
pg addon install victorialogs --name vl3 --port 9500  # explicit port still takes precedence
```

## Configuration

```yaml
addons:
  victorialogs:
    vl1:
      name: vl1
      image_tag: docker.io/victoriametrics/victoria-logs:v1.53.0
      host_port: 9428
      listen: 0.0.0.0
      retention: "30d"
      extra_args:
        - -search.maxQueryDuration=60s
        - -memory.allowedPercent=80
      # data_dir: /srv/victorialogs-data      # optional, overrides the default location
```

There is **no config file** — everything is passed as CLI flags to the `victoria-logs` binary (`-storageDataPath`, `-httpListenAddr`, `-retentionPeriod`, plus any `extra_args`). `retention` is stored as a duration string (omitted = binary default), and `extra_args` is a list of CLI flags appended last.

### List

```bash
pg addon list
```

```
Web add-ons (victorialogs):
  victorialogs (name: vl1)
    Status:      running
    URL:         http://0.0.0.0:9428/
    Retention:   30d
    Extra args:  -search.maxQueryDuration=60s -memory.allowedPercent=80
    Data:        /home/you/.pgcli/addon/victorialogs/vl1/data
    Image:       docker.io/victoriametrics/victoria-logs:v1.53.0
    Container:   pgcli-victorialogs-default-vl1
```

There are no credentials to reveal — VictoriaLogs has none — so `--show-password` shows nothing extra for it.

## Start and stop

Containers run with podman's `unless-stopped` restart policy, so they come back automatically with the podman service after a host reboot. `start`/`stop` manage them explicitly:

```bash
pg addon stop victorialogs
pg addon start victorialogs
```

## Remove

```bash
pg addon remove victorialogs              # keeps the log storage — stored logs survive a reinstall
pg addon remove victorialogs --clean-data # deletes the log storage too
```

Under rootless podman, `--clean-data` deletes the data dir through `podman unshare rm` — VictoriaLogs writes as root (container uid 0), which maps to a subordinate uid the host user cannot `rm` directly.

## Troubleshooting

- **Ingestion answers 200 but queries return nothing** — the JSON Lines endpoint requires `Content-Type: application/json`; without it VictoriaLogs accepts the request and silently drops the payload (`vl_rows_ingested_total{type="jsonline"}` on `/metrics` stays 0). Add the header, or use an ingestion client that sets it.
- **A query returns no data** — confirm VictoriaLogs is running (`pg addon list` shows `running`), the health endpoint responds (`curl -s http://<host>:<port>/-/healthy`), and you're querying the correct time range and stream filters (`_stream:` prefix, `_time:` bounds). Check `podman logs <container>` for ingestion errors.
- **Install fails with "container already exists"** — the container name is derived from `--name` and the namespace; use `--force` to recreate, or `pg addon remove` first to clean up a partial install.
- **Extra args didn't take effect** — `--extra-arg` is applied at container creation time, not runtime. Reinstall with `--force` to recreate the container with the new flags. Check `podman inspect <container> --format '{{json .Config.Cmd}}'` to confirm the flags are on the command line (the binary does not log them).
- **Port already allocated** — the assigned port is held in `pg.yaml` and skipped for later instances; free it with `pg addon remove` first, or pick another with `--port N`.
- **Remote API unreachable / unexpectedly reachable** — on the default `0.0.0.0` a failure to reach it is host firewall, not bind address; conversely, if you did *not* want it reachable, `--listen 127.0.0.1 --force`.

## Known limitations

- v1 is a bare log backend: pgcli does not install any log shipper (point Vector, Promtail, Filebeat, syslog or journald at it separately — VictoriaLogs accepts all of them over HTTP/TCP) and no alerting (use `vlalert` separately). It is the long-term log store you point Grafana (VictoriaLogs datasource) and your shippers at.
- **No authentication.** VictoriaLogs's API ingests and queries arbitrary logs; the only protection for a `0.0.0.0` bind is the network boundary. This is the same posture as VictoriaMetrics — do not expose it on an untrusted network.
- pgcli does not validate `--extra-arg` values (there is no pre-flight check at install). A bad flag surfaces only in the container logs on startup.
- On macOS the container runs inside the podman machine; it has been validated on Linux only.
