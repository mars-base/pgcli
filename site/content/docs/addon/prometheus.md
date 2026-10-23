---
title: "Prometheus"
description: "Run Prometheus, the metrics collection backend Grafana reads from, as a managed container."
weight: 55
---

`pg addon install prometheus` runs [Prometheus](https://prometheus.io/) — the metrics collection and alerting backend — as a managed container from the official `docker.io/prom/prometheus` image. Like Grafana it is a top-level addon: it fronts no single PostgreSQL instance (it scrapes many endpoints), so it is not attached to any one of them. The two are natural partners — Prometheus scrapes and stores, Grafana visualises — and share the same lifecycle shape (single container, host networking on Linux, one web port from its own pool, persistent data dir, autostart, logs).

Prometheus diverges from Grafana in **how it is configured**. Grafana is env-only (`GF_*`); Prometheus reads a config **file** (`prometheus.yml`) and takes its runtime tuning as **CLI flags passed after the image name** (`--config.file` / `--web.listen-address` / `--storage.tsdb.path` / `--storage.tsdb.retention.time`). So pgcli renders `prometheus.yml`, bind-mounts it read-only, and points `--config.file` at the in-container path.

> **There is NO web login/password.** Unlike Grafana (whose `0.0.0.0` default is
> guarded by a generated strong password), Prometheus ships **no authentication at
> all**. Its HTTP API runs arbitrary PromQL and can be loaded by expensive queries,
> so a remote-exposed instance is guarded **only by the network boundary** — your
> firewall, or `--listen 127.0.0.1` for loopback. Treat exposing it like exposing
> an unauthenticated read/write console, not like a password-protected dashboard.

> **Platform**: Linux is the tested platform. The design supports macOS (the
> image is multi-arch; the container runs under the podman machine VM on the
> `pgcli-net` bridge with the port published), but it has not been validated
> there yet.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `prometheus` | Instance name (map key) |
| `--image` | `docker.io/prom/prometheus:v3.15.0` | Image override |
| `--port` | auto (base 9090) | Host web port; auto-allocated from `prometheus_start_port` |
| `--listen` | `0.0.0.0` | Bind address — defaults to every interface; use `127.0.0.1` for loopback-only (see the exposure warning above) |
| `--scrape` | — (self job always present) | Scrape spec `JOB=HOST:PORT[,HOST:PORT...] [KEY=VALUE...]` (repeatable) — the job name is required, every comma-separated target joins that one `static_configs` job in the generated `prometheus.yml`, and bare `KEY=VALUE` tokens after the target list become **per-target labels** applied to that spec's targets. Declares the FULL job set (replaces the stored list). Same job name across specs folds into one job; a node named by two specs merges both label sets and is scraped **once**, while nodes carrying different label sets split into separate `static_configs` blocks (Prometheus's native shape for `nodename=api1`-style per-node labels). Targets are probed with `GET /metrics` from the host; unreachable ones only warn |
| `--scrape-append` | — | Same spec form (labels included), but folds the appended jobs' targets — and their labels — into the stored `--scrape` list (same-named job merges, new names append) instead of replacing it. Every appended target is probed and the install ABORTS — writing nothing — if any returns no data. Mutually exclusive with `--scrape` and `--conf-file` |
| `--scrape-interval` | `15s` | Global `scrape_interval` in the rendered config; mutually exclusive with `--conf-file` |
| `--retention` | `15d` | TSDB retention (`--storage.tsdb.retention.time`); a duration like `15d`, `6mo`, `2y`. A CLI flag, so it stays valid alongside `--conf-file` |
| `--conf-file` | — | Path to your own `prometheus.yml` — skip rendering entirely (mutually exclusive with `--scrape`/`--scrape-append`/`--scrape-interval`/`--alertmanager-url`/`--remote-write-url`/`--remote-write-queue`); see below |
| `--alertmanager-url` | — | Alertmanager instance(s) to forward fired alerts to (repeatable): bare `HOST[:PORT]` (portless gets `9093`) or `http(s)://HOST[:PORT][/path]`. The rendered config gains an `alerting:` section pushing there **and** a matching `alertmanager` scrape job (so Alertmanager's own `/metrics` is watched — not a separate opt-in). All entries validated up front and must share one scheme (mixing http/https aborts); connectivity is **not** probed. Mutually exclusive with `--conf-file`; see below |
| `--remote-write-url` | — | Remote-write URL(s) to push scraped samples to a long-term store (repeatable; a full `http(s)://host:port/path` URL). The rendered config gains a top-level `remote_write:` section. The local TSDB keeps its own `--retention` regardless — the remote is a WAL-backed copy, so a dead remote only stalls the queue and replays once it returns. Validated up front (must parse as http(s) with a host); connectivity is **not** probed. Mutually exclusive with `--conf-file`; see "Pushing samples to VictoriaMetrics" below |
| `--remote-write-queue` | — | A `queue_config` tuning applied under **every** `--remote-write-url` entry (repeatable `KEY=VALUE`): positive integers `capacity`, `max_shards`, `min_shards`, `max_samples_per_send`, and durations `batch_send_deadline`, `min_backoff`, `max_backoff` (e.g. `5s`, `1m30s`, `2y`). VictoriaMetrics suggests bigger batches under load (`max_samples_per_send=10000 capacity=20000 max_shards=30`). Requires `--remote-write-url`; keys and values are validated up front (unknown keys, non-positive integers, and unitless durations abort before anything is written). Render-mode only, like `--remote-write-url`; per-remote tuning still needs `--conf-file` |
| `--rule-file` | — | Path to a rule file (`*.rules.yml` with a top-level `groups:`; repeatable) — pgcli copies each into its managed rules dir (basename only; two files sharing a basename abort), mounts that dir read-only at `/etc/prometheus/rules`, and the rendered config loads the copies via `rule_files`. **Valid alongside `--conf-file`**: the copy and mount happen either way, your own file decides whether to reference them. A missing/empty file aborts, writing nothing; see below |
| `--data-dir` | auto | Data directory override (default under the base dir) |
| `--force` | false | Recreate even when running — instead of hot-reloading a changed config, or to apply changed ports / listen / retention (install also recreates on its own when `--rule-file` goes from empty to non-empty or back, since that changes the container's mount set) |

The data directory is Prometheus's TSDB. It survives `pg addon remove` (delete it with `--clean-data`), so reinstalling under the same name revives collected metrics. The image runs as a baked `nobody` (uid/gid 65534) and creates no writable directory itself, so pgcli chowns the data dir on the host before creating the container — via `podman unshare chown 65534:65534` under rootless podman, or a direct recursive `chown` when pgcli runs as root on a rootful host (exactly Grafana's mechanism, different id).

## Install

```bash
pg addon install prometheus
```

The command pulls the image if needed, renders a `prometheus.yml`, creates the container, and prints the URL, config path, and retention:

```
✓ Prometheus installed: "prometheus"
  Container:  pgcli-prometheus-default-prometheus
  Image:      docker.io/prom/prometheus:v3.15.0
  Data:       /home/you/.pgcli/addon/prometheus/prometheus/data
  Config:     /home/you/.pgcli/addon/prometheus/prometheus/prometheus.yml (rendered: 1 self job + 0 scrape job(s))
  Retention:  15d
  URL:        http://0.0.0.0:9090/
```

The rendered config **always** contains a self-monitoring job that scrapes this server's own `/metrics`, so it is never idle; `--scrape` adds more jobs. Point Grafana at `http://<host>:9090/` as a Prometheus datasource and you can query immediately.

```bash
# each --scrape spec declares a job; the name before = is required, and several
# comma-separated targets share that one job (the shape for a fleet of exporters)
pg addon install prometheus --name p1 \
  --scrape postgres-exporter=127.0.0.1:9187 \
  --scrape node=10.0.0.5:9100,10.0.0.6:9100
```

Per-target labels (the `nodename: api1` your Ansible-rendered config carries) are
bare `KEY=VALUE` tokens after the target list — one spec per labelled node:

```bash
# one windows job, three targets, each labelled with its node name
pg addon install prometheus --name p1 \
  --scrape windows=10.0.0.1:9182 nodename=api1 \
  --scrape windows=10.0.0.2:9182 nodename=api2 \
  --scrape windows=10.0.0.3:9182 nodename=api3
```

```yaml
# renders exactly the native per-node shape: one job_name, one labelled
# static_configs block per distinct label set
  - job_name: windows
    static_configs:
      - targets: ["10.0.0.1:9182"]
        labels:
          nodename: "api1"
      - targets: ["10.0.0.2:9182"]
        labels:
          nodename: "api2"
      - targets: ["10.0.0.3:9182"]
        labels:
          nodename: "api3"
```

Grouping is **target-centric**, so naming the same node twice never scrapes it
twice: two specs carrying the same `10.0.0.5:9100` merge into ONE target with the
union of their labels (`node=a:1 nodename=api1` + `node=a:1 env=prod` → `a:1`
carrying both). The only rejection is a contradiction — the same node labelled
with the same key to two different values aborts instead of silently dropping
one. Label values may not contain whitespace (a `device="eth0, eth1"` needs
`--conf-file`), and label keys must be `[A-Za-z_][A-Za-z0-9_]*`; values are
emitted quoted, so `yes` or `2` cannot retype under YAML.

Re-running install with `--scrape` **replaces** the whole stored job set. To add to it instead, use `--scrape-append` — same spec form, labels included; it folds each appended target (and its labels) into the stored job of the same name and appends unseen names, then hot-reloads:

```bash
pg addon install prometheus --name p1 --scrape-append node=10.0.0.7:9100
# the stored node job grows a third target; its labels are untouched
pg addon install prometheus --name p1 --scrape-append node=10.0.0.5:9100 env=prod
# ...or an existing target gains a label in place (merged, still one scrape)
```

The two flags probe differently, on purpose: a freshly **appended** target must answer `GET /metrics` with 200 or the command aborts before writing anything (pg.yaml and the running container untouched); with plain `--scrape` an unreachable target is only a warning and the install proceeds — Prometheus will surface it as `DOWN`, and a full-list declaration is often pointed at endpoints that are legitimately not up yet. The probe dials from the host — on Linux (host network) that is the same namespace the container scrapes from; on the macOS bridge it differs, so the probe is skipped there.

Scrape targets are dialled **from the Prometheus container**. On Linux (host network) a `127.0.0.1:<port>` target reaches the host's loopback directly; on the macOS bridge it reaches the container's own namespace instead, so a remote or VM-resident target should be given a routable address.

Re-running `install` on a running instance **hot-reloads** it (SIGHUP) when the rendered/copied config actually changed, and leaves it alone when it didn't; `--force` recreates the container instead — to apply a changed `--port` / `--listen` / `--retention` (which are container arguments, not config-file contents).

## Forwarding alerts to Alertmanager (`--alertmanager-url`)

Point a Prometheus instance at one or more pgcli-managed Alertmanagers and it will push fired alerts to them:

```bash
pg addon install alertmanager --name am1
pg addon install prometheus --name p1 \
  --scrape postgres-exporter=127.0.0.1:9187 \
  --alertmanager-url 127.0.0.1:9093
```

The rendered `prometheus.yml` then gains **two** things from the one flag — an `alerting:` section that pushes fired alerts, and a matching `alertmanager` scrape job that watches each instance's own `/metrics`:

```yaml
alerting:
  alertmanagers:
    - static_configs:
        - targets: ["127.0.0.1:9093"]
...
  - job_name: alertmanager
    static_configs:
      - targets: ["127.0.0.1:9093"]
```

Accepted forms are bare `host[:port]` (a portless entry gets Alertmanager's conventional `9093`) or a full `http(s)://host[:port][/path]` URL; the scheme and path are stripped for the scrape target, and `https` is carried through as `scheme: https` on both sections (TLS knobs like CA files are out of render scope — they need `--conf-file`). Entries are validated **before** anything is written and must all share one scheme (http and https mixed together abort), but they are **not** connectivity-probed: Prometheus tracks dead alertmanagers on its own, and `pg addon prometheus reload` can't fix a network problem anyway. A re-run replaces the stored URL list wholesale (there is no append form for it, unlike `--scrape`).

Verify the wiring live:

```bash
curl -s localhost:9090/api/v1/alertmanagers           # activeAlertmanagers lists the target
curl -s 'localhost:9090/api/v1/targets?state=any' | grep -c alertmanager
```

## Pushing samples to VictoriaMetrics (`--remote-write-url`)

Prometheus's built-in TSDB is compiled in and cannot be swapped out — the way to put VictoriaMetrics (or any remote store) behind it is `remote_write`: each scrape cycle pushes a **copy** of every sample to the remote, while the local TSDB keeps its own `--retention` window and keeps serving queries regardless. pgcli already runs both ends, so the pair is two installs:

```bash
# long-term store — single-node VictoriaMetrics on the loopback, or the
# vmcluster addon's vminsert (http://<host>:8480/insert/0/prometheus/api/v1/write)
pg addon install victoriametrics --name vm --listen 127.0.0.1

# the scrape backend pushes every sample into it
pg addon install prometheus --name p1 --listen 127.0.0.1 \
  --remote-write-url http://127.0.0.1:8428/api/v1/write

# same, with VictoriaMetrics's suggested batching under load
pg addon install prometheus --name p1 --listen 127.0.0.1 \
  --remote-write-url http://127.0.0.1:8428/api/v1/write \
  --remote-write-queue max_samples_per_send=10000 \
  --remote-write-queue capacity=20000 \
  --remote-write-queue max_shards=30
```

The rendered `prometheus.yml` gains a top-level `remote_write:` section — with a `queue_config:` block under each URL when `--remote-write-queue` is set:

```yaml
remote_write:
  - url: "http://127.0.0.1:8428/api/v1/write"
    queue_config:
      max_samples_per_send: 10000
      capacity: 20000
      max_shards: 30
```

A re-run replaces the stored URL list wholesale (no append form, like `--alertmanager-url`), and a changed URL is applied by **hot reload (SIGHUP)** — no container recreate. Entries must be full `http(s)://host[:port]/path` URLs (unlike Alertmanager targets there is no portless/scheme default — the path *is* the write endpoint); they are validated up front but **not** connectivity-probed. `--remote-write-queue` rides the same path: a repeated list replaces the stored one, it is validated up front (whitelisted keys only — integer sizes/shards and duration timeouts), and the same `KEY=VALUE` set is emitted under **every** remote, so differentiated per-remote tuning still drops to `--conf-file`. Passing it without any `--remote-write-url` aborts — there is nothing to tune under.

Verify the link is alive and delivering:

```bash
# the remote-write queue is draining (pending ~0, samples_total rising)
curl -s 'localhost:9090/api/v1/query?query=prometheus_remote_storage_samples_pending'
# the sample actually landed in VictoriaMetrics
curl -s 'localhost:8428/api/v1/query?query=prometheus_build_info'
```

Once Grafana is pointed at VictoriaMetrics instead of Prometheus (`http://<host>:8428/`, or `http://<host>:8481/select/0/prometheus/` for the cluster edition), the Prometheus instance is a thin scrape-and-forward layer and VictoriaMetrics is the queryable store. Historical data already in the Prometheus TSDB does **not** backfill automatically — use the `vmctl prometheus` tool for a one-time migration.

## Loading alerting rules (`--rule-file`)

Prometheus evaluates alerting/recording rules from `rule_files:`. Give pgcli the rule file and it becomes a managed copy:

```bash
pg addon install prometheus --name p1 --rule-file /srv/rules/alert.rules.yml
```

pgcli copies each file into `<base>/addon/prometheus/<name>/rules/`, bind-mounts **that directory** read-only at `/etc/prometheus/rules`, and the rendered config references the copy by name:

```yaml
rule_files:
  - "/etc/prometheus/rules/alert.rules.yml"
```

A flat directory means the **basename** is what matters: two source files sharing one basename abort the install (the copy would silently clobber one).

Editing your **original** rule file takes effect with a plain reload: `pg addon prometheus reload` re-copies every `--rule-file` into the managed dir *before* signalling, so Prometheus rereads the new rules in place — no reinstall, no container restart:

```bash
# edit /srv/rules/alert.rules.yml ...
pg addon prometheus reload --name p1
curl -s localhost:9090/api/v1/rules    # groups reflect the edit; or watch pg logs addon prometheus
```

A bad rule file is rejected the same way a bad config is: Prometheus keeps serving the **previous rule set** and logs the parse error, so check the log (or the rules API) after reloading hand-edited rules. The copies are managed artifacts, deleted by `pg addon remove` regardless of `--clean-data`, exactly like the rendered `prometheus.yml`. Your originals are never touched.

`--rule-file` differs from the other render knobs in that it **stays valid alongside `--conf-file`**: pgcli copies the rules and adds the mount either way, and your own file decides whether to reference `/etc/prometheus/rules` (a `rule_files: ["/etc/prometheus/rules/*.rules.yml"]` glob in it picks up whatever you passed). The one flag whose addition cannot be hot-reloaded is this one going from empty to non-empty (or back): the `/etc/prometheus/rules` mount exists only while there are rule files, and a SIGHUP cannot add a volume, so that transition recreates the container.

## Custom config file (`--conf-file`)

Give pgcli your own `prometheus.yml` and it skips rendering entirely:

```bash
pg addon install prometheus --name p1 --conf-file /etc/prometheus/my.yml
```

pgcli **copies** your file into its managed path (`<base>/addon/prometheus/<name>/prometheus.yml`) and mounts **that copy** read-only — never your original. This is deliberate: a single-file bind mount pins the source inode, so an in-place edit of your original file would not propagate into the running container. Because it's a copy, editing your own file and running `pg addon prometheus reload` re-copies it and signals Prometheus to reread. pgcli does not validate or rewrite the file's contents (same contract as the nginx/haproxy `--conf-file`). `--conf-file` is mutually exclusive with the render knobs `--scrape`/`--scrape-append`/`--scrape-interval`/`--alertmanager-url`/`--remote-write-url`/`--remote-write-queue` (an operator file owns its `alerting:` and `remote_write:` sections); `--retention`, `--listen` and `--rule-file` are not render knobs and stay valid alongside it. Omit `--conf-file` on a reinstall to go back to render mode.

### Worked example

A real operator file wired to a pgcli Alertmanager and loading pgcli-managed rule files:

```yaml
# /etc/prometheus/my.yml
# pg addon install prometheus --name p1 --conf-file /etc/prometheus/my.yml
global:
  scrape_interval: 15s
  scrape_timeout: 15s
  evaluation_interval: 1m

alerting:
  alertmanagers:
    - static_configs:
        - targets:
            - localhost:9093 # a pgcli alertmanager on its default base port

rule_files:
  - /etc/prometheus/rules/*.rules.yml # pgcli's managed rules dir (--rule-file)

remote_write: []

scrape_configs:
  - job_name: prometheus
    static_configs:
      - targets:
          - localhost:9090 # this Prometheus itself
  - job_name: nodes
    static_configs:
      - targets:
          - 10.246.36.2:9100
        labels:
          nodename: lhcloud1
      - targets:
          - 10.246.36.3:9100
        labels:
          nodename: lhcloud2
      - targets:
          - 10.246.36.4:9100
        labels:
          nodename: lhcloud3
```

That labelled `nodes` shape is exactly what render mode now emits — the three
`--scrape node=…:9100 nodename=…` specs fold into one `nodes` job carrying one
labelled `static_configs` block each; the hand-written file remains the way to
add per-job extras (scrape timeouts, relabels) the renderer does not expose.

`localhost:9090` and `localhost:9093` work because Linux containers run on the **host network** — `localhost` inside Prometheus is the host, where both web ports listen. The two pieces pgcli contributes are visible in the file: `--rule-file` copies your rule files into `<base>/addon/prometheus/<name>/rules/` and bind-mounts that directory at `/etc/prometheus/rules`, which is why the `rule_files:` glob above resolves; the `alerting:` block, on the other hand, is yours to write — in `--conf-file` mode pgcli adds nothing to the file, does not derive the `alertmanager` scrape job either, and rejects `--alertmanager-url` as a render knob. Check a hand-written file before relying on `pg addon prometheus reload` to surface errors in the log:

```bash
c=$(podman ps --filter name=pgcli-prometheus --format '{{.Names}}' | head -1)
podman exec "$c" promtool check config /etc/prometheus/prometheus.yml
```

That inspects the copy pgcli mounted, so run it after an install (or a reload) rather than against your original path.


## Reload (hot config change)

```bash
pg addon prometheus reload --name p1
```

This rewrites the managed `prometheus.yml` (re-render in render mode, re-copy your file in `--conf-file` mode, re-copy the `--rule-file` copies in both) and sends **SIGHUP** to the running container, which makes Prometheus reread it in place — no container recreate, no dropped scrapes. It uses a signal rather than the HTTP `POST /-/reload` endpoint on purpose, so the unauthenticated reload route never has to be enabled (`--web.enable-lifecycle` is not set). Prometheus keeps serving on its last good config and logs an error if the new one is malformed, so check `pg logs addon prometheus` after reloading a hand-written file. The instance must already be running (`pg addon start prometheus`). One change a signal cannot apply: `--rule-file` going from empty to non-empty (or back) adds/removes the `/etc/prometheus/rules` **mount**, so a plain `install` recreates the container for that transition — reload is enough for edits *within* a non-empty rule set.

## Listening and exposure

Prometheus's `Listen` defaults to `0.0.0.0`: the container shares the host's network namespace, so the UI and API are reachable on every interface at the assigned port. This matches Grafana's default for the same practical reason (Grafana on another host needs to reach it) — but **there is no password behind it**. Anyone who can reach the port can run arbitrary PromQL and read your collected metrics. For a loopback-only deployment pass `--listen 127.0.0.1` (recreate with `--force`); `pg addon list` then shows `http://127.0.0.1:<port>/`. Otherwise, restrict access at the firewall.

## Ports

Each Prometheus instance owns a single port: the web UI/API. It is allocated from a pool with base `prometheus_start_port` (default `9090` — Prometheus's conventional web port), incrementing for every additional instance and skipping ports already taken by any managed container or the `used_ports` list. The allocation is written into `pg.yaml`, so a reinstall never re-hands-out a port another addon is using. `9090` shares no number with Grafana (`3000`), pgAdmin (`5050`), or nginx (`8080`); an explicit `--port` still wins.

```bash
pg addon install prometheus --name p2          # second instance: next free port (9091 if 9090 taken)
pg addon install prometheus --name p3 --port 9100  # explicit port still takes precedence
```

## Configuration

```yaml
addons:
  prometheus:
    p1:
      name: p1
      image_tag: docker.io/prom/prometheus:v3.15.0
      host_port: 9090
      listen: 0.0.0.0
      retention: 15d
      scrape:
        - postgres-exporter=127.0.0.1:9187
        - node=10.0.0.5:9100,10.0.0.6:9100
        - node=10.0.0.5:9100 env=prod   # same node again → ONE scrape carrying the union of labels
        - windows=10.0.0.1:9182 nodename=api1   # bare KEY=VALUE tokens = per-target labels
      scrape_interval: 15s
      autostart: false
      # alertmanager: [127.0.0.1:9093]        # render-mode only: forward fired alerts + a derived scrape job
      # remote_write: [http://127.0.0.1:8428/api/v1/write]  # render-mode only: push samples to a long-term store (VictoriaMetrics)
      # remote_write_queue: [max_samples_per_send=10000, capacity=20000]  # queue_config under every remote above
      # rule_files: [/srv/rules/alert.rules.yml]  # copied into <name>/rules, mounted at /etc/prometheus/rules
      # conf_file: /etc/prometheus/my.yml   # operator-file mode, skips rendering
      # data_dir: /srv/prometheus-data      # optional, overrides the default location
```

The rendered `prometheus.yml` carries the global `scrape_interval`, the always-present self job, and one `static_configs` job per `scrape` JOB — every target a job's specs list folds into that job, bucketed by merged label set so each distinct `KEY=VALUE` combination becomes its own `- targets:/labels:` block; `--alertmanager-url` adds the `alerting:` section plus its `alertmanager` scrape job, `--remote-write-url` adds a top-level `remote_write:` section (`--remote-write-queue` fills each entry with a `queue_config:` block), and `--rule-file` adds a `rule_files:` section. `retention` and the listener bind are **not** in that file — they are passed to the `prometheus` binary as CLI flags (`--storage.tsdb.retention.time`, `--web.listen-address`), which is why they remain settable alongside `--conf-file`.

### List

```bash
pg addon list
```

```
Web add-ons (prometheus):
  prometheus (name: p1)
    Status:      running
    URL:         http://0.0.0.0:9090/
    Config:      /home/you/.pgcli/addon/prometheus/p1/prometheus.yml [rendered (1 scrape job(s))]
    Retention:   15d
    Data:        /home/you/.pgcli/addon/prometheus/p1/data
    Image:       docker.io/prom/prometheus:v3.15.0
    Container:   pgcli-prometheus-p1
```

There are no credentials to reveal — Prometheus has none — so `--show-password` shows nothing extra for it.

## Start and stop

Containers run with podman's `unless-stopped` restart policy, so they come back automatically with the podman service after a host reboot. `start`/`stop` manage them explicitly:

```bash
pg addon stop prometheus
pg addon start prometheus
```

## Auto-start on Boot

To have a Prometheus instance started automatically on host boot, enable it:

```bash
pg autostart enable --prometheus --name p1
```

This flags the instance in `pg.yaml` and installs/refreshes a boot service (a systemd `--user` unit on Linux, a LaunchAgent on macOS) that runs `pg start --autostart`. A boot-time start only brings the container up — reading the managed `prometheus.yml` already on disk, and the data dir was already chowned at create time, so it needs no host privileges beyond podman itself. Prometheus starts after Grafana and before nginx in the boot order. See [Autostart]({{< relref "../autostart" >}}) for the full mechanism, status and disabling.

## Remove

```bash
pg addon remove prometheus              # keeps the TSDB — collected metrics survive a reinstall
pg addon remove prometheus --clean-data # deletes the TSDB too
```

Under rootless podman, `--clean-data` deletes the data dir through `podman unshare rm` — Prometheus writes as its container uid (65534), which maps to a subordinate uid the host user cannot `rm` directly. `--conf-file` mode removes only pgcli's copy; your original file wherever you put it is never touched. `--rule-file` originals likewise survive — only the managed `rules/` copies go away, and they go away on every `remove`, with or without `--clean-data` (like the rendered `prometheus.yml`, they carry no state worth reviving).

## Logs

```bash
pg logs addon prometheus            # show recent console output
pg logs addon prometheus -f         # follow
pg logs addon prometheus -n 200     # last 200 lines (also -i / --tail)
```

## Troubleshooting

- **`install` fails with "chowning Prometheus data dir ... to 65534:65534"** — the image runs as `nobody` (uid/gid 65534) and never fixes ownership itself, so pgcli chowns the host data dir before creating the container: through `podman unshare chown` under **rootless** podman (where the mapped subordinate uid is the only correct target), or a direct recursive `chown` when pgcli itself runs as root on a **rootful** host. The manager shells out to the plain `podman` CLI, so this only fails outside those two cases — e.g. a non-root `pg` explicitly pointed at someone else's rootful daemon. Run the install as root, or switch to rootless podman; a manual `sudo chown -R 65534:65534 <data-dir>` alone does not clear it, since pgcli re-checks ownership on every install attempt.
- **A scrape target shows `DOWN`** — confirm Prometheus can reach it (it dials from the container; on Linux `127.0.0.1:<port>` is the host loopback, but a cross-host/VM target needs a routable address, especially on the macOS bridge). Check the exporter is up and the path is `/metrics`. `pg logs addon prometheus` shows scrape failures.
- **`--scrape-append` aborted with "returned no data"** — by design: an appended target must answer `GET /metrics` with 200 from the host before anything is written (the existing instance and `pg.yaml` stay untouched). Bring the exporter up, or use plain `--scrape` (which only warns) when declaring the full job set. The probe is skipped on macOS.
- **A config reload didn't take effect** — `pg addon prometheus reload` needs the container running; if the new `prometheus.yml` is malformed Prometheus keeps serving the last good config and logs the error. Check `pg logs addon prometheus`, fix the file, reload again.
- **Port already allocated** — the assigned port is held in `pg.yaml` and skipped for later instances; free it with `pg addon remove` first, or pick another with `--port N`.
- **Remote API unreachable / unexpectedly reachable** — on the default `0.0.0.0` a failure to reach it is host firewall, not bind address; conversely, if you did *not* want it reachable, `--listen 127.0.0.1 --force`.

## Known limitations

- v1 is a bare collection backend: it ships no PostgreSQL exporter (install one yourself and point `--scrape postgres-exporter=127.0.0.1:9187` at it). Long-term storage and alerting are wired, though: `--remote-write-url` pushes scraped samples to a VictoriaMetrics addon (single-node or `vmcluster`), `--alertmanager-url` forwards fired alerts to a pgcli Alertmanager, and `--rule-file` loads alerting rules (all render-mode; `--conf-file` covers the rest by hand). It is the scrape/store/query layer you point Grafana at.
- **No authentication.** Prometheus's API runs arbitrary PromQL; the only protection for a `0.0.0.0` bind is the network boundary. This is a materially weaker posture than Grafana's password-guarded `0.0.0.0` — do not expose it on an untrusted network.
- pgcli renders or copies `prometheus.yml` but does not validate it (there is no `promtool check config` gate at install). A bad rendered config is impossible, but a bad `--conf-file` surfaces only in the container logs on reload.
- The host-side ownership chown is automatic under rootless podman (`podman unshare chown`) and when pgcli runs as root on a rootful host (direct `chown`). A rootful daemon talked to by a non-root pgcli (only via an explicitly configured remote socket) has no automatic path.
- On macOS the container runs inside the podman machine; it has been validated on Linux only.
