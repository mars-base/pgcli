---
title: "vmalert"
description: "Run vmalert, the VictoriaMetrics rule-evaluation engine, as a managed container: evaluate alerting/recording rules against VictoriaLogs or VictoriaMetrics and notify Alertmanager"
weight: 59
---

`pg addon install vmalert` runs [vmalert](https://docs.victoriametrics.com/vmalert/) — the rule-evaluation engine of the VictoriaMetrics family — as a managed container from the official `docker.io/victoriametrics/vmalert` image. It is a top-level addon: it evaluates alerting (and recording) rules and forwards firing alerts to one or more Alertmanager-compatible notifiers. It is the missing link between the [VictoriaLogs addon]({{< relref "../addon/victorialogs" >}}) / [VictoriaMetrics addon]({{< relref "../addon/victoriametrics" >}}) and the [Alertmanager addon]({{< relref "../addon/alertmanager" >}}).

**One instance evaluates against exactly one datasource.** Logs and metrics cannot be mixed in the same vmalert process: the default datasource is VictoriaLogs (`http://127.0.0.1:9428`), and a metrics rule set must pass `--datasource-url http://127.0.0.1:8428` (or another MetricsQL/PromQL-compatible endpoint) explicitly.

Rules and templates live in **host directories bind-mounted read-only** into the container. There is no rendered config file: edit the YAML directly, then run `pg addon vmalert reload`.

> **There is NO authentication and NO TLS.** The rules/alerts API and `GET /-/reload` are open to anyone who can reach the port. `--listen` defaults to `0.0.0.0` (the Prometheus-stack convention); use `--listen 127.0.0.1` on an untrusted network or restrict access at the firewall.

> **Platform**: Linux is the tested platform. The design supports macOS (the image is multi-arch; the container runs under the podman machine VM on the `pgcli-net` bridge with the port published), but it has not been validated there yet. On macOS the container's `127.0.0.1` is its own loopback, so the default datasource/notifier URLs must point at `host.containers.internal` (or a container name) instead.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `vmalert` | Instance name (map key) |
| `--image` | `docker.io/victoriametrics/vmalert:v1.133.0` | Image override |
| `--port` | auto (base 8880) | Host HTTP/API port; auto-allocated from `vmalert_start_port` |
| `--listen` | `0.0.0.0` | Bind address — defaults to every interface; use `127.0.0.1` for loopback-only |
| `--datasource-url` | `http://127.0.0.1:9428` | The ONE backend rules query (VictoriaLogs by default; `http://127.0.0.1:8428` for VictoriaMetrics) |
| `--notifier-url` | `http://127.0.0.1:9093` | Alertmanager-compatible notifier(s), repeatable. Passing the flag replaces the stored list |
| `--rules-dir` | `<base_dir>/addon/vmalert/<name>/rules` | Host rules directory, mounted read-only at `/etc/vmalert/rules` |
| `--templates-dir` | `<base_dir>/addon/vmalert/<name>/templates` | Host templates directory, mounted read-only at `/etc/vmalert/templates` |
| `--remote-write-url` | — | Remote-write base URL(s) for recording rules and alert-state persistence, repeatable |
| `--remote-read-url` | — | Remote-read base URL(s) restoring alert state after a restart, repeatable |
| `--external-label` | — | Extra `NAME=VALUE` label on every generated alert/series (repeatable; use for HA replica labels) |
| `--external-url` | — | Externally reachable base URL used in UI links |
| `--extra-arg` | — | Extra CLI flag passed to the binary (repeatable); appended last so last-wins overrides earlier flags |
| `--force` | false | Recreate even when running — to apply changed ports / listen / URLs / labels |

`--data-dir` and `--conf-file` are **rejected** for vmalert: it keeps no local state and has no config file.

## Rules and templates

The rules directory is seeded once with `alert.rules.yaml` and the templates directory with `default.tpl`; pgcli **never overwrites a file you have edited**. Both directories are bind-mounted read-only, so the operator owns the files.

Loading uses the container-side globs:

```text
-rule=/etc/vmalert/rules/*.yaml
-rule.templates=/etc/vmalert/templates/*.tpl
```

Only top-level `*.yaml` and `*.tpl` files match. **A `*.yml` file is silently ignored** — rename it to `.yaml`. New files added to the directory are picked up on reload without recreating the container. Subdirectories are not loaded.

A VictoriaLogs rule group (the reference layout):

```yaml
groups:
  - name: victoria-logs
    type: vlogs
    interval: 1m
    rules:
      - alert: found-999-log
        expr: '_time:5m | 999 | count() as cnt | filter cnt:>0'
        labels:
          severity: critical
        annotations:
          description: 'Found 999 logs in the last 5 minutes'
```

A metrics rule group (requires `--datasource-url http://127.0.0.1:8428`):

```yaml
groups:
  - name: metrics
    interval: 30s
    rules:
      - alert: instance-down
        expr: up == 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: 'Instance {{ $labels.instance }} is down'
```

## Install

Log alerting with the defaults (VictoriaLogs datasource, Alertmanager notifier):

```bash
pg addon install vmalert
```

Metrics alerting — point it at VictoriaMetrics instead:

```bash
pg addon install vmalert --name vmalert-metrics \
  --datasource-url http://127.0.0.1:8428
```

Custom directories, several notifiers, and an HA replica label:

```bash
pg addon install vmalert \
  --rules-dir /srv/vmalert/rules \
  --templates-dir /srv/vmalert/templates \
  --notifier-url http://127.0.0.1:9093 \
  --notifier-url http://127.0.0.1:9094 \
  --external-label replica=a \
  --listen 127.0.0.1
```

The install summary prints the container, image, URL, datasource, notifiers, rules/templates directories and any remote write/read or external labels.

## Reload

```bash
pg addon vmalert reload --name vmalert
```

Reload sends `GET /-/reload` to the running instance. It is **asynchronous**: a 2xx response only means the signal was accepted. Malformed YAML is reported in the container log while the previous rules keep running, so confirm the result with:

```bash
pg addon vmalert status --name vmalert
pg logs addon vmalert --name vmalert
```

`status` prints the stored config plus the live `Groups`, `Rules`, `firing`/`pending` counts and each group's name/rule count/file, read from `/api/v1/rules`. A stopped instance prints `stopped` without querying the API.

## Alert state across restarts

These two flags are not the rule-evaluation datasource — that is always `--datasource-url`. They persist vmalert's own output: recording-rule series and the `ALERTS` / `ALERTS_FOR_STATE` state series.

- Recording rules only (persist their output series): `--remote-write-url` alone.
- Alert state across restarts: both flags, pointing at the **same** store.
- Pure log alerting where a state reset on restart is acceptable: neither.

vmalert keeps alert state in memory. To restore it after a **restart**, configure **both** `--remote-write-url` and `--remote-read-url` (for example against the VictoriaMetrics addon):

```bash
pg addon install vmalert \
  --remote-write-url http://127.0.0.1:8428 \
  --remote-read-url http://127.0.0.1:8428
```

Pass **base URLs**: vmalert appends `/api/v1/write` for remote write and `/api/v1/query` for remote read itself, so `http://127.0.0.1:8428/api/v1/write` would become `/api/v1/write/api/v1/write` and 404. This differs from the Prometheus addon, whose `--remote-write-url` takes the full path.

Both are required, and the targets must be compatible. A hot reload never triggers a state restore; configuring only one of the pair does not either. `--remote-write-url` alone is still useful for recording-rule results.

## HA

Run one vmalert per replica with a distinct external label so Alertmanager can deduplicate identical alerts:

```bash
pg addon install vmalert --name vmalert-a --external-label replica=a
pg addon install vmalert --name vmalert-b --external-label replica=b
```

There is no leader election — every replica evaluates the same rules and sends alerts; Alertmanager deduplicates by label set. `--external-url` only shapes UI links.

## Lifecycle, logs, autostart

```bash
pg addon start vmalert --name vmalert
pg addon stop vmalert --name vmalert
pg addon remove vmalert --name vmalert              # rules/templates kept
pg addon remove vmalert --name vmalert --clean-data # delete pgcli-managed dirs too
pg logs addon vmalert --name vmalert
pg autostart enable --vmalert --name vmalert
```

Re-running install recreates a running container when the effective config changed (or with `--force`), and is a no-op when nothing changed. Rules and templates are never modified by install, start, stop, or `--force`.

`remove` keeps the rules and templates directories by default. `--clean-data` deletes **only pgcli-managed directories**; a user-supplied `--rules-dir`/`--templates-dir` is never deleted (the paths are printed). A directory that is a mount point is skipped with a warning.

At boot, `pg start --autostart` starts vmalert after VictoriaLogs, VictoriaMetrics and Alertmanager, so the default datasource and notifier are already listening.

## Limitations

- One datasource per instance — logs and metrics cannot be mixed.
- No basic auth, TLS, or any authentication on the API or `/-/reload`.
- No leader election (HA relies on external labels + Alertmanager deduplication).
- No local alert-state persistence; use remote write + remote read.
- Only `*.yaml` / `*.tpl` at the top level are loaded; `*.yml` is ignored.
- The vmalert release cadence differs from the VictoriaMetrics main version; upgrade with `--image`.

## Verification

End-to-end tested on Linux (debian12-01) on 2026-11-03 with `docker.io/victoriametrics/vmalert:v1.133.0` via `test/addon/test_vmalert.sh` (48 assertions): the default VictoriaLogs datasource fires a real vlogs alert that reaches Alertmanager, reload picks up a new group, an identical reinstall is a no-op while a changed flag recreates the container, `--force` leaves the operator's rules untouched, and remove/`--clean-data` respect managed-vs-explicit directories.
