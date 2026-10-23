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

VictoriaLogs is ready to ingest and query immediately. Open `http://<host>:9428/select/vmui/` for the built-in web UI (the root path `/` is just an index of useful endpoints), or query via the HTTP API:

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

## Web UI

Every VictoriaLogs instance ships with a built-in web UI (VMUI) — there is no extra component to install. Open:

```
http://<host>:<port>/select/vmui/
```

The root path (`/`) is only an index of useful HTTP endpoints; the UI lives under `/select/vmui/`.

The UI is a LogsQL console for logs:

- **Query** — type a [LogsQL](https://docs.victoriametrics.com/victorialogs/logsql/) query (e.g. `app:demo`, `level:error`, `error app:demo-api`) and pick the time range; results update as you run the query.
- **Views** — switch between `Group` (rows grouped by stream fields), `Table` (click a row to expand every field), `JSON` (raw `/select/logsql/query` response), and `Live` (live tail of matching logs as they arrive).
- **Exploration** — field and stream pickers list the stored field names/values (backed by `/select/logsql/field_names`, `field_values`, `facets`, ...), so you can build queries without knowing the schema up front.

For example, ingest one line and search for it in the UI (`app:demo`):

```bash
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"_msg":"hello from pgcli","app":"demo"}' \
  http://127.0.0.1:9428/insert/jsonline
```

The UI shares the same port and the same **no-authentication** posture as the HTTP API: anyone who can reach the port can search and also ingest logs. Restrict it with `--listen 127.0.0.1` or a firewall, as described above.

## Ingestion: vlagent, HTTP APIs, collectors

pgcli runs the VictoriaLogs backend only — how logs reach it is up to you. There are three routes.

### vlagent (official agent)

[`vlagent`](https://docs.victoriametrics.com/victorialogs/vlagent/) is the official log agent for VictoriaLogs — the **log** counterpart of `vmagent` (`vmagent` ships metrics only; do not use it for logs). It accepts the same HTTP ingestion protocols as VictoriaLogs on port `9429` (change with `-httpListenAddr`) and forwards everything to VictoriaLogs via `-remoteWrite.url`, which **must end with `/insert/native`**:

```bash
podman run -d --name vlagent --network host \
  -v /var/lib/vlagent:/var/lib/vlagent \
  docker.io/victoriametrics/vlagent:v1.53.0 \
  -remoteWrite.url=http://127.0.0.1:9428/insert/native \
  -tmpDataPath=/var/lib/vlagent
```

This command tracks **no log files**: without `-kubernetesCollector` or `-fileCollector.glob`, vlagent only receives logs pushed to it over HTTP on port `9429` and forwards them. To tail files on the host they must be mounted into the container and matched by a glob:

```bash
podman run -d --name vlagent --network host \
  -v /var/lib/vlagent:/var/lib/vlagent \
  -v /var/log/nginx:/var/log/nginx:ro \
  docker.io/victoriametrics/vlagent:v1.53.0 \
  -remoteWrite.url=http://127.0.0.1:9428/insert/native \
  -tmpDataPath=/var/lib/vlagent \
  -fileCollector.glob='/var/log/nginx/*.log'
```

Use vlagent when you need:

- **Replication / HA** — repeat `-remoteWrite.url` for every VictoriaLogs instance. vlagent copies each log to all of them and keeps an independent on-disk buffer per URL (`-remoteWrite.tmpDataPath`, capped by `-remoteWrite.maxDiskUsagePerURL`); logs collected while a backend is down are replayed when it returns.
- **Kubernetes Pod logs** — run it as a DaemonSet with `-kubernetesCollector` (or use the `victoria-logs-collector` Helm chart). It follows the node's container logs under `/var/log/pods` and `/var/log/containers` (mount the host's `/var/log` read-only; with the Docker runtime also mount `/var/lib` so runtime symlinks resolve), adds container metadata, and checkpoints its read position under `-tmpDataPath`.
- **Files on disk** — `-fileCollector.glob='/var/log/nginx/*.log'` (repeatable; globs must point at files, not directories). The path is resolved **inside the container**, so mount the host directory first. By default `hostname` and `file` become stream fields, and reading checkpoints/buffers live under `-tmpDataPath`.

For a single, stable VictoriaLogs instance, sending logs directly (below) is simpler — that is the recommended default.

### HTTP APIs

VictoriaLogs exposes several ingestion endpoints on the same port as the UI and query API:

| Endpoint | Format | Notes |
|---|---|---|
| `/insert/jsonline` | JSON Lines (ndjson) | Requires `Content-Type: application/json`; without it the request returns 200 but the payload is silently dropped |
| `/insert/elasticsearch/_bulk` | Elasticsearch / OpenSearch bulk | Point Filebeat / Logstash / Fluent Bit Elasticsearch outputs here |
| `/insert/loki/api/v1/push` | Loki JSON push | Point Promtail / Grafana Alloy / Loki clients here |
| `/insert/opentelemetry/v1/logs` | OTLP/HTTP logs | Point an OpenTelemetry Collector exporter here |
| syslog TCP / UDP / Unix | RFC3164 / RFC5424 | Enable via `--extra-arg -syslog.listenAddr.tcp=:514` (or `.udp` / `.unix`), then recreate |

All HTTP ingestion endpoints accept the same tuning parameters, either as query-string args or `VL-*` headers: `_msg_field` (field holding the log message, e.g. `message`), `_time_field` (e.g. `@timestamp`), `_stream_fields` (comma-separated fields that define a log stream), `extra_fields` (`env=prod`), `ignore_fields`, `decolorize_fields`, and `debug=1` (log the payload instead of storing it). Use them when your collector emits a schema VictoriaLogs does not auto-detect.

```bash
# JSON Lines with explicit field mapping
curl -s -X POST -H 'Content-Type: application/json' \
  --data-binary @app.jsonl \
  'http://127.0.0.1:9428/insert/jsonline?_stream_fields=host,app&_time_field=@timestamp&_msg_field=message'
```

### Log collectors

Any collector that can speak one of the formats above works; pgcli installs and configures none of them. Commonly used ones, each with an official setup guide:

| Collector | Send to |
|---|---|
| [Filebeat](https://docs.victoriametrics.com/victorialogs/data-ingestion/filebeat/) | Elasticsearch output → `/insert/elasticsearch/_bulk` |
| [Logstash](https://docs.victoriametrics.com/victorialogs/data-ingestion/logstash/) | Elasticsearch or HTTP output |
| [Fluent Bit](https://docs.victoriametrics.com/victorialogs/data-ingestion/fluentbit/) / [Fluentd](https://docs.victoriametrics.com/victorialogs/data-ingestion/fluentd/) | Elasticsearch or HTTP output |
| [Vector](https://docs.victoriametrics.com/victorialogs/data-ingestion/vector/) | `http` sink → `/insert/jsonline`, or Elasticsearch sink |
| [Promtail](https://docs.victoriametrics.com/victorialogs/data-ingestion/promtail/) / Grafana Alloy | Loki push → `/insert/loki/api/v1/push` |
| [OpenTelemetry Collector](https://docs.victoriametrics.com/victorialogs/data-ingestion/opentelemetry/) | OTLP/HTTP exporter → `/insert/opentelemetry/v1/logs` |
| [Telegraf](https://docs.victoriametrics.com/victorialogs/data-ingestion/telegraf/) | `http` output → jsonline |
| [journald](https://docs.victoriametrics.com/victorialogs/data-ingestion/journald/) | Vector / Fluent Bit journald source → jsonline, or vlagent |
| [rsyslog / syslog-ng](https://docs.victoriametrics.com/victorialogs/data-ingestion/syslog/) | syslog listener (`-syslog.listenAddr.*`) |
| [Datadog Agent](https://docs.victoriametrics.com/victorialogs/data-ingestion/datadog-agent/) | Datadog intake API |

Collectors can also point at a **vlagent** instance (port `9429`) instead of VictoriaLogs directly — useful when you want buffering, replication, or a single egress point.

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

- v1 is a bare log backend: pgcli does not install any log shipper — use `vlagent`, a direct HTTP ingestion API, or a third-party collector as described in the Ingestion section above — and does no alerting (use `vlalert` separately). It is the long-term log store you point Grafana (VictoriaLogs datasource) and your shippers at.
- **No authentication.** VictoriaLogs's API ingests and queries arbitrary logs; the only protection for a `0.0.0.0` bind is the network boundary. This is the same posture as VictoriaMetrics — do not expose it on an untrusted network.
- pgcli does not validate `--extra-arg` values (there is no pre-flight check at install). A bad flag surfaces only in the container logs on startup.
- On macOS the container runs inside the podman machine; it has been validated on Linux only.
