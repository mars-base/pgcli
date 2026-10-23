---
title: "vlagent"
description: "Run vlagent, the official VictoriaLogs log agent, as a managed container: track any host log path and forward to VictoriaLogs"
weight: 59
---

`pg addon install vlagent` runs [vlagent](https://docs.victoriametrics.com/victorialogs/vlagent/) — the official log agent of the VictoriaLogs family and the **log** counterpart of `vmagent` — as a managed container from the official `docker.io/victoriametrics/vlagent` image. It is a top-level addon (it fronts no single PostgreSQL instance) and the natural partner of the [VictoriaLogs addon]({{< relref "../addon/victorialogs" >}}): VictoriaLogs stores logs, vlagent collects them from files or over HTTP and forwards them to one or more VictoriaLogs instances.

Like VictoriaMetrics and VictoriaLogs there is **no config file at all** — everything is passed as CLI flags after the image name. pgcli adds the file-collector wiring on top: every `--log-path` is bind-mounted **read-only at the same absolute path** inside the container, and `--include`/`--exclude` select files using vlagent's **native glob syntax** (not regular expressions).

> **There is NO authentication.** vlagent's HTTP listener accepts logs from anyone who can reach the port, and it forwards to VictoriaLogs instances that (by default) have no auth either. A remote-exposed instance is guarded **only by the network boundary** — use `--listen 127.0.0.1` for loopback or restrict access at the firewall.

> **Platform**: Linux is the tested platform. The design supports macOS (the image is multi-arch; the container runs under the podman machine VM on the `pgcli-net` bridge with the port published), but it has not been validated there yet.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `va` | Instance name (map key) |
| `--image` | `docker.io/victoriametrics/vlagent:v1.53.0` | Image override |
| `--port` | auto (base 9429) | Host HTTP ingest port; auto-allocated from `vlagent_start_port` |
| `--listen` | `0.0.0.0` | Bind address — defaults to every interface; use `127.0.0.1` for loopback-only |
| `--remote-write-url` | **required** | VictoriaLogs endpoint(s) to forward to (repeatable). Every URL **must end with `/insert/native`** |
| `--log-path` | — | Host file or directory to track (repeatable). Mounted read-only at the same absolute path inside the container |
| `--include` | derived | Glob pattern(s) selecting files (repeatable; absolute container paths). Defaults to `<dir>/**/*.log` per directory and the file itself per file |
| `--exclude` | — | Glob pattern(s) excluding files (repeatable; positionally paired with `--include`) |
| `--extra-arg` | — | Extra CLI flag passed to the binary (repeatable); appended last so last-wins overrides earlier flags |
| `--data-dir` | auto | Data directory override (default under the base dir) |
| `--force` | false | Recreate even when running — to apply changed ports / listen / URLs / paths / globs |

The data directory holds vlagent's on-disk remote-write buffers and the file-collector checkpoints (its read positions). It survives `pg addon remove` (delete it with `--clean-data`), so reinstalling under the same name resumes exactly where collection stopped instead of re-sending or skipping file contents. The image runs as **root**, so no pre-chown is needed.

There is no `--password`: vlagent has no concept of a local account, and neither side of the connection authenticates.

## Install

Relay-only — accept HTTP pushes on `:9429` and forward them to a local VictoriaLogs:

```bash
pg addon install vlagent \
  --remote-write-url http://127.0.0.1:9428/insert/native
```

Track a directory of host log files (the common case):

```bash
pg addon install vlagent \
  --log-path /var/log/nginx \
  --remote-write-url http://127.0.0.1:9428/insert/native
```

With no `--include`, the directory defaults to `/var/log/nginx/**/*.log` — every `.log` file at any depth below it. The flag is quoted here only for shell safety; pgcli stores the glob literally.

```bash
✓ vlagent installed: "va"
  Container:  pgcli-vlagent-default-va
  Image:      docker.io/victoriametrics/vlagent:v1.53.0
  Data:       /home/you/.pgcli/addon/vlagent/va/data
  Listen:     http://0.0.0.0:9429/ (HTTP ingest: jsonline, ES bulk, Loki, OTLP)
  Remote:     http://127.0.0.1:9428/insert/native
  Mount:      /var/log/nginx (dir, read-only, same path in the container)
  Include:    /var/log/nginx/**/*.log
  Checkpoint: file-checkpoints.json under /var/lib/vlagent
```

Track a single file, or several paths with explicit globs and excludes:

```bash
# one file (default include is the file itself)
pg addon install vlagent --name va-app \
  --log-path /var/log/myapp/app.log \
  --remote-write-url http://127.0.0.1:9428/insert/native

# several paths, glob per path, excluding compressed archives
pg addon install vlagent --name va-all \
  --log-path /var/log/nginx --include '/var/log/nginx/*.log' --exclude '/var/log/nginx/*.gz' \
  --log-path /var/log/redis --include '/var/log/redis/**/*.log' \
  --remote-write-url http://127.0.0.1:9428/insert/native
```

Every `--log-path` becomes one read-only bind mount (`host:host:ro`), so the globs pgcli passes to vlagent are literally the host paths. Paths must be absolute and must already exist.

Re-running `install` on a running instance **recreates** it when the config actually changed (port, listen, URLs, log paths, includes/excludes, extra-args), and leaves it alone when it didn't; `--force` recreates the container even when config is unchanged. List-valued flags replace the stored list: to add a path, repeat the whole set.

## How file collection works

vlagent's file collector is driven by `-fileCollector.glob`. pgcli maps its flags one-to-one:

| pgcli flag | vlagent flag | Notes |
|---|---|---|
| `--log-path` | (bind mount) | `-v <path>:<path>:ro` — required so the container can see the file |
| `--include` | `-fileCollector.glob` | Repeatable; must match files, not directories |
| `--exclude` | `-fileCollector.excludeGlob` | Positionally paired with the Nth include |
| `--data-dir` | `-tmpDataPath` | Contains buffers and `file-checkpoints.json` |

Each collected line becomes a log entry with two extra fields: `file` (the source path) and `hostname`. Both are **stream fields** by default, so each file is its own log stream in VictoriaLogs — exactly what you want for LogsQL filtering. Tune them with `--extra-arg -fileCollector.streamFields=...`, `-fileCollector.fileField=...`, or `-fileCollector.hostnameField=...`.

The collector polls for new files every 10 seconds (`-fileCollector.refreshInterval`). It supports the `create` and `nocreate` rotation strategies: on rotation it finishes the old file before switching, and it resumes rotated files from the saved position after a restart. Checkpoints are written under the data dir.

### Glob syntax

This is **not a regular expression** — it is vlagent's own glob dialect:

| Pattern | Matches |
|---|---|
| `*` | Any sequence of characters except path separators |
| `/**/` | Zero or more directories (recursive) |
| `?` | Any single character except a path separator |
| `[abc]`, `[a-z]` | Character class; prefix with `^` or `!` to negate |
| `{a,b,c}` | Alternatives — any one of the comma-separated terms |
| `\c` | Escaped character literal |

Examples:

```bash
--include '/var/log/nginx/*.log'              # one level below
--include '/var/log/nginx/**/*.log'           # any depth
--include '/var/log/nginx/{access,error}.log' # alternatives
--include '/var/log/nginx/access.log.[0-9]'   # rotated files
```

`*` does **not** match hidden files (names starting with `.`); name the leading dot explicitly (`.*.log`) if you need them.

### Excludes are positional

vlagent pairs the Nth `--exclude` with the Nth `--include`, like its own flags. An empty string `''` is a placeholder meaning "no exclude at this position":

```bash
--include '/var/log/*/*.log' --exclude '/var/log/nginx/*' \
--include '/var/log/nginx/*.log' --exclude '' \
```

This collects everything except nginx first, then nginx in the second pair. Without the positional pairing, the nginx exclusion would also silence the second pair.

pgcli emits every exclude entry up to the last non-empty one; trailing empty placeholders are simply dropped. If you change `--log-path` without passing `--include`, the default globs are recomputed for the new paths and stored excludes beyond the new pair count are trimmed (pass `--exclude` explicitly in the same run to override).

## Replication and high availability

Repeat `--remote-write-url` for every VictoriaLogs instance:

```bash
pg addon install vlagent --name va-ha \
  --log-path /var/log/myapp \
  --remote-write-url http://10.0.0.1:9428/insert/native \
  --remote-write-url http://10.0.0.2:9428/insert/native
```

vlagent copies every log to all targets, keeps an independent disk buffer per URL, and replays buffered logs when a backend returns. Cap each buffer with `--extra-arg -remoteWrite.maxDiskUsagePerURL=...`.

## Other ingestion over HTTP

Even without `--log-path`, vlagent is a useful HTTP front door: it accepts the same protocols as VictoriaLogs on its listen port and forwards them, so collectors can push into vlagent instead of directly into VictoriaLogs:

| Endpoint | Format |
|---|---|
| `/insert/jsonline` | JSON Lines (requires `Content-Type: application/json`) |
| `/insert/elasticsearch/_bulk` | Elasticsearch / OpenSearch bulk |
| `/insert/loki/api/v1/push` | Loki JSON push |
| `/insert/opentelemetry/v1/logs` | OTLP/HTTP logs |

For Kubernetes node logs, pass `--extra-arg -kubernetesCollector` and mount the node's `/var/log` (plus `/var/lib` with the Docker runtime) via `--log-path`. See the [VictoriaLogs ingestion section]({{< relref "../addon/victorialogs" >}}) for the full protocol and collector table.

## Ports

Each vlagent instance owns a single port: the HTTP ingest listener. It is allocated from a pool with base `vlagent_start_port` (default `9429` — vlagent's conventional port), incrementing for every additional instance and skipping ports already taken by any managed container or the `used_ports` list. The pool is separate from VictoriaLogs (`9428`), so adding a VictoriaLogs instance can never steal a vlagent port. An explicit `--port` still wins.

```bash
pg addon install vlagent --name va2 \
  --remote-write-url http://127.0.0.1:9428/insert/native   # next free port (9430 when 9429 is taken)
```

## Configuration

```yaml
addons:
  vlagent:
    va:
      name: va
      image_tag: docker.io/victoriametrics/vlagent:v1.53.0
      host_port: 9429
      listen: 0.0.0.0
      remote_write_urls:
        - http://127.0.0.1:9428/insert/native
      log_paths:
        - /var/log/nginx
      includes:
        - /var/log/nginx/**/*.log
      excludes:
        - /var/log/nginx/*.gz
      extra_args:
        - -fileCollector.refreshInterval=5s
      # data_dir: /srv/vlagent-data        # optional, overrides the default location
```

Everything is passed as CLI flags to the `vlagent` binary: `remote_write_urls` becomes repeated `-remoteWrite.url`, `includes` become `-fileCollector.glob`, `excludes` become `-fileCollector.excludeGlob`, `log_paths` become read-only bind mounts, and `data_dir` becomes `-tmpDataPath`.

### List

```bash
pg addon list
```

```
Log agents (vlagent):
  vlagent (name: va)
    Status:      running
    Listen:      http://0.0.0.0:9429/
    Remote:      http://127.0.0.1:9428/insert/native
    Log paths:   /var/log/nginx
    Includes:    /var/log/nginx/**/*.log
    Excludes:    /var/log/nginx/*.gz
    Data:        /home/you/.pgcli/addon/vlagent/va/data
    Image:       docker.io/victoriametrics/vlagent:v1.53.0
    Container:   pgcli-vlagent-default-va
```

## Start and stop

```bash
pg addon stop vlagent
pg addon start vlagent
```

The container runs with podman's `unless-stopped` restart policy. File read positions are checkpointed to the data dir, so a stop/start (or host reboot) resumes without duplicating or losing file content.

### Autostart on boot

```bash
pg autostart enable --vlagent --name va
```

This flags the instance in `pg.yaml` and installs/refreshes a boot service (a systemd `--user` unit on Linux, a LaunchAgent on macOS) that runs `pg start --autostart`. A boot-time start only brings the container up — the data dir with the file checkpoints and remote-write buffers already exists, so the agent resumes where it stopped. vlagent starts **after VictoriaLogs** in the boot sequence, so a same-host remote-write target is already listening. Disable with `pg autostart disable --vlagent --name va`; see [Autostart]({{< relref "../autostart" >}}) for the full mechanism.

## Remove

```bash
pg addon remove vlagent              # keeps buffers and file checkpoints
pg addon remove vlagent --clean-data # deletes them too
```

Removing the agent **does not** remove any log files it tracked — they are mounted read-only and never modified.

## Troubleshooting

- **No file logs in VictoriaLogs** — confirm the container is running (`pg addon list`), the globs match real files (`--include` must match files, not directories), and the paths are visible inside the container. `podman inspect pgcli-vlagent-<ns>-<name> --format '{{json .Config.Cmd}}'` shows the exact flags; `podman logs <container>` shows collector errors.
- **Some files are skipped** — `*` does not match hidden files, and rotated names like `app.log.1` do not match `*.log`. Add explicit patterns (e.g. `--include '/var/log/myapp/app.log.[0-9]'`). Excludes are positional: a broad first-pair exclude can hide files from a later pair.
- **Data lands with the wrong stream** — `file` and `hostname` are stream fields by default; override with `--extra-arg -fileCollector.streamFields=...`.
- **Extra args didn't take effect** — `--extra-arg` is applied at container creation time. Reinstall (config changes recreate automatically) or use `--force`; verify with `podman inspect <container> --format '{{json .Config.Cmd}}'`.
- **Container cannot read the logs (SELinux hosts)** — pgcli mounts log paths with `:ro` only, deliberately not `:z`, because relabeling `/var/log` would modify the host. On enforcing-SELinux systems pass workload-appropriate labeling yourself (e.g. a shared `container_log_t` label or `--extra-arg`-independent podman policy) or run the container with a matching security profile.
- **Remote write fails** — the URL must end in `/insert/native`; check VictoriaLogs health (`curl -s http://<vl>:9428/-/healthy`) and whether a firewall separates the hosts. Buffered logs replay once it is reachable again.

## Known limitations

- **No regular expressions.** vlagent matches globs only (`*`, `/**/`, `?`, `[a-z]`, `{a,b}`). Complex file-name rules need a broader include plus a LogsQL filter on the query side.
- **No authentication** on the ingest listener or the remote-write side; guard it with `--listen 127.0.0.1` or a firewall.
- pgcli does not validate `--extra-arg` values; a bad flag surfaces in the container logs at startup.
- macOS is code-complete but untested; Linux is the validated platform.
