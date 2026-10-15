---
title: "Alertmanager"
description: "Run Alertmanager, Prometheus's alert deduplication/grouping/routing frontend, as a managed container."
weight: 56
---

`pg addon install alertmanager` runs [Alertmanager](https://prometheus.io/docs/alerting/latest/alertmanager/) — the alert deduplication, grouping, routing and notification layer that sits downstream of Prometheus — as a managed container from the official `docker.io/prom/alertmanager` image. Like Prometheus and Grafana it is a top-level addon: it fronts no single PostgreSQL instance, so it is not attached to any one of them. Prometheus and Alertmanager are the natural pair — Prometheus fires alerts, Alertmanager decides who gets notified and how — and this addon is Prometheus's near-exact twin in shape (single container, host networking on Linux, one web port from its own pool, persistent data dir, autostart, logs, rendered config file + trailing CLI flags, SIGHUP reload, no web login).

> **This version does NOT wire the two together.** `pg addon install alertmanager` does
> not inject an `alerting:` section into any `prometheus.yml`, and `pg addon install
> prometheus` knows nothing about Alertmanager. The two run side by side; to actually
> route Prometheus alerts here, point Prometheus at this instance yourself (or use
> `--conf-file` on either side for a real routing tree). A future version may link them
> up.

> **There is NO web login/password — and the stakes are higher than Prometheus's.**
> Alertmanager ships **no authentication at all**, and its unauthenticated API is
> write-capable in a way Prometheus's read-mostly one is not: anyone who reaches the
> port can **create silences** — i.e. silently mute every notification your on-call
> expects, with no trace in Prometheus's own metrics. Its `0.0.0.0` default is
> therefore guarded **only by the network boundary**, and that boundary matters more
> here than anywhere else in the addon family. Treat exposing it as handing out a mute
> switch for your alerting, not like a password-protected dashboard.

> **Platform**: Linux is the tested platform. The design supports macOS (the image is
> multi-arch; the container runs under the podman machine VM on the `pgcli-net` bridge
> with the port published), but it has not been validated there yet.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `alertmanager` | Instance name (map key) |
| `--image` | `docker.io/prom/alertmanager:v0.34.1` | Image override |
| `--port` | auto (base 9093) | Host web port; auto-allocated from `alertmanager_start_port` |
| `--listen` | `0.0.0.0` | Bind address — defaults to every interface; use `127.0.0.1` for loopback-only (see the exposure warning above) |
| `--webhook-url` | — | Webhook URL to notify (repeatable). Each URL is rendered as one `webhook_configs` entry of the SAME `web.hook` receiver — multiple URLs fan out to all of them. Validated with `net/url` (parseable, http/https, non-empty host) **before** anything is written; a bad URL aborts the install touching nothing. Connectivity is NOT probed (see below). Mutually exclusive with `--conf-file` |
| `--conf-file` | — | Path to your own `alertmanager.yml` — skip rendering entirely (mutually exclusive with `--webhook-url`); see below |
| `--data-dir` | auto | Data directory override (default under the base dir) |
| `--force` | false | Recreate even when running — instead of hot-reloading a changed config, or to apply changed ports / listen |

The data directory holds Alertmanager's state: **silences and notification state**. It
survives `pg addon remove` (delete it with `--clean-data`), so reinstalling under the
same name revives them. The image runs as a baked `nobody` (uid/gid 65534) and creates
no writable directory itself, so pgcli chowns the data dir on the host before creating
the container — via `podman unshare chown 65534:65534` under rootless podman, or a
direct recursive `chown` when pgcli runs as root on a rootful host (exactly Prometheus's
and Grafana's mechanism, different id).

`--webhook-url` is **not** connectivity-probed, on purpose: unlike a Prometheus scrape
(`GET /metrics`, reachable at install time and answerable then), a webhook is a `POST`
that only fires when an alert actually routes through it — dialling it at install would
tell you nothing you could rely on. The only early gate is the URL's *shape*; whether
the endpoint exists, is reachable, and accepts the payload is discovered at alert time
(check `pg logs addon alertmanager` when notifications go missing).

## Install

```bash
pg addon install alertmanager
```

The command pulls the image if needed, renders an `alertmanager.yml`, creates the
container, and prints the URL, config path, and what the route renders to:

```
✓ Alertmanager installed: "alertmanager"
  Container:  pgcli-alertmanager-default-alertmanager
  Image:      docker.io/prom/alertmanager:v0.34.1
  Data:       /home/you/.pgcli/addon/alertmanager/alertmanager/data
  Config:     /home/you/.pgcli/addon/alertmanager/alertmanager/alertmanager.yml (rendered: no-op receiver)
  URL:        http://0.0.0.0:9093/
```

With no `--webhook-url` the rendered config is a single route pointing at a name-only
`no-op` receiver: a *legal*, fully-functional config — the web UI and API are live,
silences work, the HA cluster port is disabled — that simply **sends nothing**. This is
the "bring up the endpoint, decide routing later" default. To actually notify something,
add a webhook:

```bash
# one URL → the route's receiver becomes web.hook with one webhook_configs entry
pg addon install alertmanager --name am1 --webhook-url http://127.0.0.1:5001/

# several URLs → all of them land under ONE web.hook receiver (fan-out to each)
pg addon install alertmanager --name am1 \
  --webhook-url http://127.0.0.1:5001/ \
  --webhook-url https://hooks.example.com/alert?token=abc
```

Re-running install on a running instance **hot-reloads** it (SIGHUP) when the
rendered/copied config actually changed, and leaves it alone when it didn't;
`--force` recreates the container instead — to apply a changed `--port` / `--listen`
(which are container arguments, not config-file contents).

Re-running with a **different** `--webhook-url` set replaces the stored list wholesale
(passing none clears it back to no-op); the diff is what decides whether to reload.

## Custom config file (`--conf-file`)

Give pgcli your own `alertmanager.yml` and it skips rendering entirely:

```bash
pg addon install alertmanager --name am1 --conf-file /etc/alertmanager/my.yml
```

pgcli **copies** your file into its managed path
(`<base>/addon/alertmanager/<name>/alertmanager.yml`) and mounts **that copy**
read-only — never your original. This is deliberate: a single-file bind mount pins the
source inode, so an in-place edit of your original file would not propagate into the
running container. Because it's a copy, editing your own file and running
`pg addon alertmanager reload` re-copies it and signals Alertmanager to reread. pgcli
does not validate or rewrite the file's contents (same contract as the
nginx/haproxy/prometheus `--conf-file`). `--conf-file` is mutually exclusive with
`--webhook-url` (the flag that shapes the rendered file) — a real routing tree
(email/Slack/PagerDuty, sub-routes, inhibition) lives in your own file. Omit
`--conf-file` on a reinstall to go back to render mode.

The file is plain Alertmanager YAML — pgcli adds nothing to it and expects nothing from
it beyond "parses". A worked example with a sub-route tree, inhibition, and three
different receiver types:

```yaml
# Complete Alertmanager config for `pg addon install alertmanager
# --name am1 --conf-file /path/to/this.yml` — everything pgcli would
# render is replaced by what follows; only the container flags
# (--web.listen-address, --storage.path, --cluster.listen-address=)
# stay pgcli's job.
global:
  resolve_timeout: 5m
  smtp_smarthost: "smtp.example.com:587"
  smtp_from: "alertmanager@example.com"
  smtp_auth_username: "alertmanager@example.com"
  smtp_auth_password: "CHANGE-ME"
  slack_api_url: "https://hooks.slack.com/services/T00000/B00000/XXXXXXXX"

inhibit_rules:
  # a critical alert mutes warning/info noise from the SAME alert on the SAME instance
  - source_matchers: ['severity = critical']
    target_matchers: ['severity =~ warning|info']
    equal: ['alertname', 'instance']

route:
  receiver: default
  group_by: ['alertname']
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 4h
  routes:
    # critical goes to the on-call mailbox, and still falls through to default
    - matchers: ['severity = critical']
      receiver: oncall-email
      continue: true
    # storage team's alerts get their own Slack channel
    - matchers: ['team = storage']
      receiver: storage-slack
      group_by: ['alertname']
      group_wait: 10s

receivers:
  - name: default
    webhook_configs:
      - url: "http://127.0.0.1:5001/"

  - name: oncall-email
    email_configs:
      - to: "oncall@example.com"
        send_resolved: true

  - name: storage-slack
    slack_configs:
      - channel: "#storage-alerts"
        send_resolved: true
```

pgcli itself never validates this file (no `amtool check-config` gate at install — see
Known limitations), but the image ships `amtool`, so you can check the managed copy
inside the running container before trusting a reload (container name as printed by
`pg addon list`, namespace-scoped — `pgcli-alertmanager-default-am1` in the transcript
above):

```bash
pg addon install alertmanager --name am1 --conf-file /etc/alertmanager/my.yml
# edit /etc/alertmanager/my.yml ...
podman exec pgcli-alertmanager-default-am1 amtool check-config /etc/alertmanager/alertmanager.yml
pg addon alertmanager reload --name am1   # re-copies and SIGHUPs once the file is good
```

## Reload (hot config change)

```bash
pg addon alertmanager reload --name am1
```

This rewrites the managed `alertmanager.yml` (re-render in render mode, re-copy your
file in `--conf-file` mode) and sends **SIGHUP** to the running container, which makes
Alertmanager reread it in place — no container recreate, no dropped notifications.

Unlike Prometheus, **Alertmanager's `POST /-/reload` endpoint exists and cannot be
disabled** (there is no `--web.enable-lifecycle`-style gate on it). pgcli still reloads
via SIGHUP — not because the route is closed, but so it never has to depend on an
unauthenticated HTTP endpoint that is, by the image's design, always open. Anyone who
can reach the port can already hit that route directly; pgcli simply doesn't add to the
surface. If you reload a hand-written `--conf-file` whose config is rejected, Alertmanager
keeps its last good config and logs the error, so check `pg logs addon alertmanager`
afterwards. The instance must already be running (`pg addon start alertmanager`).

## Listening and exposure

Alertmanager's `Listen` defaults to `0.0.0.0`: the container shares the host's network
namespace, so the UI and API are reachable on every interface at the assigned port.
**There is no password behind it, and — unlike Prometheus — the API lets a caller
CREATE SILENCES**, i.e. mute every notification your on-call expects, and reload its
config. A loopback-only deployment is the safe default: pass `--listen 127.0.0.1`
(recreate with `--force`); `pg addon list` then shows `http://127.0.0.1:<port>/`.
Otherwise restrict access at the firewall, and treat an open `0.0.0.0` as a
reachable mute switch.

## Ports

Each Alertmanager instance owns a single port: the web UI/API. It is allocated from a
pool with base `alertmanager_start_port` (default `9093` — Alertmanager's conventional
web port), incrementing for every additional instance and skipping ports already taken
by any managed container or the `used_ports` list. The allocation is written into
`pg.yaml`, so a reinstall never re-hands-out a port another addon is using. `9093`
shares no number with Grafana (`3000`), pgAdmin (`5050`), Prometheus (`9090`), or nginx
(`8080`); an explicit `--port` still wins.

> **9094 — the HA gossip port — is never opened, because it's disabled by a flag.**
> Alertmanager's high-availability clustering listens on TCP+UDP `9094` by default and
> will keep trying to bootstrap peers that never arrive for a single instance. pgcli
> passes `--cluster.listen-address=` (empty) to switch it off entirely — a
> single-instance contract, enforced at every create, so the cluster port never opens
> and no UDP listener lingers. (This is only about the *cluster* use of `9094`; the
> number is not reserved, so the web-port pool is free to hand `9094` out as the
> second instance's *web* port — see the example below.) Clustering several
> pgcli-managed Alertmanagers into one gossip group is not wired up (see Known
> limitations).

```bash
pg addon install alertmanager --name am2          # second instance: next free port after 9093 (e.g. 9094 — the pool is plain numeric assignment; nothing reserves the disabled cluster port's number)
pg addon install alertmanager --name am3 --port 9200  # explicit port still takes precedence
```

## Configuration

```yaml
addons:
  alertmanager:
    am1:
      name: am1
      image_tag: docker.io/prom/alertmanager:v0.34.1
      host_port: 9093
      listen: 0.0.0.0
      webhook:
        - http://127.0.0.1:5001/
        - https://hooks.example.com/alert?token=abc
      autostart: false
      # conf_file: /etc/alertmanager/my.yml   # operator-file mode, skips rendering
      # data_dir: /srv/alertmanager-data      # optional, overrides the default location
```

The rendered `alertmanager.yml` carries a `global.resolve_timeout`, one root route
(`group_by: ['alertname']`, default group_wait/interval/repeat_interval) pointing at a
single receiver, and that receiver's `webhook_configs` — one entry per `webhook` URL
under `web.hook`, or a name-only `no-op` receiver when the list is empty. The listener
bind, storage path, and the disabled cluster address are **not** in that file — they
are passed to the `alertmanager` binary as CLI flags (`--web.listen-address`,
`--storage.path`, `--cluster.listen-address=`), which is why `--listen` remains
settable alongside `--conf-file`.

### List

```bash
pg addon list
```

```
Web add-ons (alertmanager):
  alertmanager (name: am1)
    Status:      running
    URL:         http://0.0.0.0:9093/
    Config:      /home/you/.pgcli/addon/alertmanager/am1/alertmanager.yml [rendered (2 webhook)]
    Data:        /home/you/.pgcli/addon/alertmanager/am1/data
    Image:       docker.io/prom/alertmanager:v0.34.1
    Container:   pgcli-alertmanager-am1
```

There are no credentials to reveal — Alertmanager has none — so `--show-password` shows
nothing extra for it, and `pg addon password alertmanager` resolves nothing.

## Start and stop

Containers run with podman's `unless-stopped` restart policy, so they come back
automatically with the podman service after a host reboot. `start`/`stop` manage them
explicitly:

```bash
pg addon stop alertmanager
pg addon start alertmanager
```

## Auto-start on Boot

To have an Alertmanager instance started automatically on host boot, enable it:

```bash
pg autostart enable --alertmanager --name am1
```

This flags the instance in `pg.yaml` and installs/refreshes a boot service (a systemd
`--user` unit on Linux, a LaunchAgent on macOS) that runs `pg start --autostart`. A
boot-time start only brings the container up — reading the managed `alertmanager.yml`
already on disk, and the data dir was already chowned at create time, so it needs no
host privileges beyond podman itself. In the boot order Alertmanager starts right after
Prometheus and before nginx — the natural companion to the collector above, even though
v1 does not wire the two together. See [Autostart]({{< relref "../autostart" >}}) for
the full mechanism, status and disabling.

## Remove

```bash
pg addon remove alertmanager              # keeps silences + notification state — they survive a reinstall
pg addon remove alertmanager --clean-data # deletes them too
```

Under rootless podman, `--clean-data` deletes the data dir through `podman unshare rm` —
Alertmanager writes as its container uid (65534), which maps to a subordinate uid the
host user cannot `rm` directly. `--conf-file` mode removes only pgcli's copy; your
original file wherever you put it is never touched.

## Logs

```bash
pg logs addon alertmanager            # show recent console output
pg logs addon alertmanager -f         # follow
pg logs addon alertmanager -n 200     # last 200 lines (also -i / --tail)
```

## Troubleshooting

- **`install` fails with "chowning Alertmanager data dir ... to 65534:65534"** — the
  image runs as `nobody` (uid/gid 65534) and never fixes ownership itself, so pgcli
  chowns the host data dir before creating the container: through `podman unshare chown`
  under **rootless** podman (where the mapped subordinate uid is the only correct
  target), or a direct recursive `chown` when pgcli itself runs as root on a **rootful**
  host. The manager shells out to the plain `podman` CLI, so this only fails outside
  those two cases — e.g. a non-root `pg` explicitly pointed at someone else's rootful
  daemon. Run the install as root, or switch to rootless podman; a manual
  `sudo chown -R 65534:65534 <data-dir>` alone does not clear it, since pgcli re-checks
  ownership on every install attempt.
- **No notifications arrive** — the most common cause is a `no-op` receiver: an install
  with no `--webhook-url` renders a config that never sends anything. Check the `Config:`
  line in `pg addon list` (it says `no-op receiver` vs `rendered (N webhooks)`). If a
  real webhook is configured, it is **not** connectivity-probed at install — reachability
  is only discovered when an alert fires, so check `pg logs addon alertmanager` for POST
  failures and confirm the endpoint accepts Alertmanager's webhook payload.
- **`--webhook-url` aborted with a parse error** — by design: every URL is validated
  (`net/url` parse, http/https scheme, non-empty host) **before** pg.yaml or any file is
  written, so a bad URL leaves the running instance and config untouched. Fix the URL and
  re-run.
- **A config reload didn't take effect** — `pg addon alertmanager reload` needs the
  container running; if the new `alertmanager.yml` is rejected (a route referencing an
  undefined receiver, bad YAML) Alertmanager keeps serving the last good config and logs
  the error. Check `pg logs addon alertmanager`, fix the file, reload again.
- **Port already allocated** — the assigned port is held in `pg.yaml` and skipped for
  later instances; free it with `pg addon remove` first, or pick another with `--port N`.
- **Remote API unreachable / unexpectedly reachable** — on the default `0.0.0.0` a
  failure to reach it is host firewall, not bind address; conversely, if you did *not*
  want it reachable (and remember: reachability means anyone can create silences),
  `--listen 127.0.0.1 --force`.

## Known limitations

- **Not wired to Prometheus.** This addon neither injects an `alerting:` section into a
  `prometheus.yml` nor reads one from Prometheus; the two are independent (see the note
  at the top). Real Prometheus→Alertmanager routing is left to you (`--conf-file`) or a
  future version.
- **No authentication** — and its consequences are sharper than Prometheus's. The API can
  create silences (mute on-call) and reload config, and `POST /-/reload` cannot be
  disabled in the image (pgcli just doesn't use it). The only protection for a `0.0.0.0`
  bind is the network boundary; do not expose it on an untrusted network.
- **Single instance / cluster disabled.** `--cluster.listen-address=` is always passed
  empty, so HA gossip (TCP+UDP 9094) never opens. Clustering several pgcli-managed
  Alertmanagers is not supported in v1.
- **Webhooks are not connectivity-probed** at install (only their URL *shape* is
  validated). Reachability and payload acceptance surface at alert time, in the logs.
- pgcli renders or copies `alertmanager.yml` but does not validate it (there is no
  `amtool check-config` gate at install). A bad rendered config is impossible, but a bad
  `--conf-file` surfaces only in the container logs on reload.
- The host-side ownership chown is automatic under rootless podman (`podman unshare
  chown`) and when pgcli runs as root on a rootful host (direct `chown`). A rootful
  daemon talked to by a non-root pgcli (only via an explicitly configured remote socket)
  has no automatic path.
- On macOS the container runs inside the podman machine; it has been validated on Linux
  only.
