---
title: "Observability deployment"
description: "Manage many machines with one declarative Ansible scenario: node and PostgreSQL exporters, Prometheus, alert rules, Alertmanager, and a pg daemon webhook receiver"
weight: 35
---

The `observable` scenario turns a fleet of machines into a monitored data
center: every member runs `node_exporter`, PostgreSQL hosts also run
`pg_exporter`, and one host runs Prometheus, Alertmanager, Grafana, and the
alert webhook. Instances, add-ons, and the webhook process are declared in
group variables and converged by one `apply.yaml` run. Adding machines means
adding inventory entries and re-running the playbook — no per-host commands.

The shipped reference is `group_vars/observable/main.yml.example`;
`group_vars/observable/README.md` contains the full Grafana API walkthrough.

## Reference layout

| Host | Role |
|------|------|
| `db-01` | stand-alone instance `pg01`, `nodeexporter`, `pgexporter` |
| `db-02` | primary `pg02`, `nodeexporter`, `pgexporter` |
| `db-03` | cross-host read-only replica `pg_primary_ro1`, `nodeexporter`, `pgexporter` |
| `obs-01` | `nodeexporter`, `prometheus`, `alertmanager`, `grafana`, `alertmanager-webhook` daemon |

For a larger fleet the layout is the same: list every machine in
`[observable]`, add its PostgreSQL instances to `pgcli_instances`, and the
scrape targets are derived from the inventory. All playbook commands run from
the `ansible/` directory.

## Inventory for many machines

```ini
[all]
db-01 ansible_host=192.0.2.11 private_ip=192.0.2.11
db-02 ansible_host=192.0.2.12 private_ip=192.0.2.12
db-03 ansible_host=192.0.2.13 private_ip=192.0.2.13
db-04 ansible_host=192.0.2.15 private_ip=192.0.2.15
obs-01 ansible_host=192.0.2.14 private_ip=192.0.2.14

[all:vars]
ansible_user=root
ansible_port=22

# Every monitored machine belongs to this group. node_exporter is installed on
# all of them; the observability host is included too.
[observable]
db-01
db-02
db-03
db-04
obs-01

[observable:vars]
ansible_group_priority=20

# Optional subgroups for batched rollouts and environment labels. They do not
# replace [observable]: group_vars/observable still applies to these hosts.
[rack-a]
db-01
db-02
[rack-b]
db-03
db-04
obs-01
```

`private_ip` is the address used for node-to-node scraping and replication.
`ansible_host` is only the SSH target. Keeping both lets the monitoring traffic
stay on the private network.

### Custom group names

`observable` is only the shipped example name. For a real site, copy the group
variable file under a name that matches your inventory group:

```bash
cp group_vars/observable/main.yml.example group_vars/site-a/main.yml
cp group_vars/observable/main.yml.example group_vars/site-b/main.yml
```

Then declare the same group names in `hosts.ini`:

```ini
[site-a]
db-01
db-02
obs-01

[site-b]
db-10
db-11
obs-02

[site-a:vars]
ansible_group_priority=20
[site-b:vars]
ansible_group_priority=20
```

Run `--limit site-a` or `--limit site-b`. Group variables follow membership, so
each site gets its own `pgcli_instances`, `pgcli_addons`, and optionally
`pgcli_daemons`; hosts shared by two scenario groups should be avoided, because
the lists do not merge — the winning group is decided by Ansible's group
precedence. Do not name a scenario group with an `addon_` prefix; those names
are reserved for the `addon_<type>_<single|cluster>` selector. Keep the example
files' `host:` values in sync with the hostnames used in the new group, replace
`groups['observable']` in `_observable_node_exporter_targets` with the new
group name, and create `group_vars/<group>/secrets.yaml` with
`pgexporter_monitor_password` (gitignored, as described below).

## Targets and labels

The example derives both scrape jobs from the inventory:

```yaml
# Hosts running node_exporter: every member of [observable].
_observable_node_exporter_targets: >-
  {{ groups['observable'] | map('extract', hostvars, 'private_ip')
     | map('regex_replace', '$', ':9100') | join(',') }}

# Hosts running pg_exporter: one per host that carries a pgcli instance.
_observable_pg_exporter_targets: >-
  {{ pgcli_instances | map(attribute='host') | unique
     | map('extract', hostvars, 'private_ip')
     | map('regex_replace', '$', ':9630') | join(',') }}
```

Both are passed to Prometheus as `--scrape <job>=<targets>` and regenerated on
every apply, so a machine added to `[observable]` (or an instance added to
`pgcli_instances`) is picked up automatically. This is static scrape
configuration, not service discovery: Prometheus is told exact
`host:port` targets.

To attach labels such as the environment or rack, pass `KEY=VALUE` tokens after
the target list. The flag accepts one `--scrape` per label set; a compact way
to build one spec per group:

```yaml
_observable_node_scrapes: >-
  {{ (['--scrape'] | product([
       'node-exporter=' ~ (groups['rack-a'] | map('extract', hostvars, 'private_ip')
                            | map('regex_replace', '$', ':9100') | join(',')) ~ ' rack=rack-a',
       'node-exporter=' ~ (groups['rack-b'] | map('extract', hostvars, 'private_ip')
                            | map('regex_replace', '$', ':9100') | join(',')) ~ ' rack=rack-b'
     ])) | list | flatten }}
```

Then place `_observable_node_scrapes` into the add-on's `params` instead of the
single `--scrape` entry. The same labels are available to alert rules as
`$labels.rack`.

`nodeexporter` has no `host` key in its add-on entry, so it installs on every
member. If a machine should be monitored without running PostgreSQL, put it in
`[observable]` only; it contributes a node_exporter target and no pg target.

## Group variables and secrets

```bash
cp group_vars/observable/main.yml.example group_vars/observable/main.yml
```

The scenario sets `pgcli_deploy_rootless: true` and `pgcli_deploy_user: pgcli`,
so pgcli, podman, containers, and the webhook daemon all run as the unprivileged
`pgcli` account. Derived paths follow `pgcli_deploy_home`.

Instances for the whole fleet are declared once in this group's
`pgcli_instances` list (name, host, optional replica settings). Each host
reconciles only the entries whose `host` matches. Use `host_vars/<host>.yml`
for per-machine overrides rather than copying the group file.

All PostgreSQL exporters share one low-privilege monitor role. On primaries and
stand-alone instances `pgexporter` creates the role (`pg_monitor`,
`CONNECTION LIMIT 5`) with the password pinned from
`group_vars/observable/secrets.yaml` (gitignored, never committed):

```yaml
pgexporter_monitor_password: "<choose a strong value>"
```

A physical standby cannot run the role-creation SQL. Streaming replication
carries the role created on the primary, and the standby's exporter dials the
local port with `--dsn` using the same password. Keep the default role name
`pgexporter`; a renamed role would have to be created on the primary and
replicated again.

## Deploy in batches

```bash
cd ansible
uv sync
uv run ansible-playbook playbooks/apply.yaml --limit observable
```

The instance and add-on plays run `serial: 1`, so large groups are provisioned
one host at a time. For a multi-rack rollout, select a subgroup per window —
`--limit rack-a` still applies `group_vars/observable` because group variables
follow membership, not the limit:

```bash
uv run ansible-playbook playbooks/apply.yaml --limit rack-a
# verify Prometheus targets, then:
uv run ansible-playbook playbooks/apply.yaml --limit rack-b
```

Partial runs by tag:

```bash
uv run ansible-playbook playbooks/apply.yaml --limit observable --tags instance  # instances
uv run ansible-playbook playbooks/apply.yaml --limit observable --tags addon     # exporters + stack
uv run ansible-playbook playbooks/apply.yaml --limit observable --tags daemon    # webhook receiver
```

The playbook order is install → instances → daemons → add-ons. Every role
compares declared state with `pg config show` and container state, so unchanged
hosts report `ok`. `force: true` on the Prometheus and Alertmanager entries
recreates those containers so changed parameters (`--scrape`,
`--webhook-url`, rules) take effect; without it an installed add-on keeps its
original flags. Recreating Prometheus is intentional here because its scrape
targets are inventory-derived and change whenever the fleet changes.

## Prometheus capacity

A single Prometheus add-on scrapes the whole fleet. Size it deliberately:

- `--retention 15d` is the default; raise it (for example `30d`) or set an
  explicit `--data-dir` on a large disk for long local history.
- `--scrape-interval 15s` is the default; on a large fleet, `30s` or `60s`
  cuts samples proportionally.
- The local TSDB is single-instance; the add-on does not cluster. For longer
  retention or cross-site aggregation, add
  `--remote-write-url` pointing at a VictoriaMetrics add-on and keep the local
  retention short. The write queue replays after a remote outage.
- Prometheus has no authentication. Keep `--listen=127.0.0.1` as in the
  example and let Grafana proxy queries, or firewall the port.

## Alerting rules and routing

Prometheus loads `misc/prometheus/postgres.rules.yml` through the `rule_files`
mapping on the Prometheus entry. The `pg_addon` role copies the file into
pgcli's managed rules directory and passes `--rule-file` at install time. Rule
content lives in a real file so Prometheus's own `{{ $labels.* }}` templating
is never re-rendered by Ansible. The rules cover PostgreSQL (`pg_up`,
replication lag, paused replay, connection saturation, idle-in-transaction,
deadlocks, archiver failures), node targets (scrape failures, CPU and memory
pressure, disk space, read-only filesystems), and Alertmanager config reloads.

Firing alerts go to Alertmanager, whose only receiver is the webhook managed by
`pg daemon`:

| Item | Value |
|------|-------|
| Script | `misc/webhook/alertmanager_webhook.py` (copied to the daemon dir) |
| Command | `/usr/bin/python3 <daemon dir>/alertmanager_webhook.py --listen 127.0.0.1:18801` |
| Log | `<daemon dir>/logs/alertmanager-webhook.log` |
| Rotation | 10 MB, keep 7 |
| Autostart | enabled |

The receiver prints one JSON line per alert and suppresses HTTP access logs, so
the daemon log contains only alert messages. Inspect it as the deployment
account on the observability host:

```bash
runuser -u pgcli -- /usr/local/bin/pg -c /home/pgcli/.pgcli/pg.yaml \
  daemon status alertmanager-webhook
runuser -u pgcli -- /usr/local/bin/pg -c /home/pgcli/.pgcli/pg.yaml \
  daemon logs alertmanager-webhook -n 50
runuser -u pgcli -- /usr/local/bin/pg -c /home/pgcli/.pgcli/pg.yaml \
  autostart status
```

`pg autostart status` shows `daemon alertmanager-webhook enabled`; at boot,
`pg start --autostart` starts instances first, then add-ons, then daemons.

The rendered Alertmanager config has one route and one receiver. For
severity/team routing, give the add-on entry a full `conf` (written 0600 and
passed as `--conf-file`; keep secrets in the gitignored live file):

```yaml
  - type: alertmanager
    name: alertmanager
    host: obs-01
    state: present
    force: true
    params:
      - "--listen=127.0.0.1"
    conf: |
      route:
        receiver: default
        group_by: [alertname, env]
        routes:
          - matchers: ['severity="critical"']
            receiver: oncall
      receivers:
        - name: default
          webhook_configs:
            - url: http://127.0.0.1:18801/alertmanager
        - name: oncall
          webhook_configs:
            - url: http://127.0.0.1:18801/alertmanager
```

The bundled receiver accepts `POST /alertmanager` and `GET /health`; to
distinguish receiver names, extend `deliver()` or point `oncall` at an external
API. The Alertmanager add-on runs a single instance and disables cluster
gossip, so it is not HA — keep its data directory (silences survive container
recreation) and restart the add-on if the host is lost.

To forward alerts to a third-party API, implement the HTTP call in `deliver()`
inside `misc/webhook/alertmanager_webhook.py`. Return `False` to answer 503 and
let Alertmanager retry. Keep tokens out of the repository: read them from a
0600 file owned by the deployment account. Alertmanager groups notifications,
so deduplicate on `alert["fingerprint"]` when the target API is not idempotent.
Rerunning `--tags daemon` rewrites the script and restarts the daemon when its
content changed.

## Verify

Run the loopback probes on the observability host:

```bash
curl -sS http://127.0.0.1:9090/api/v1/targets \
  | jq -r '.data.activeTargets[] | "\(.labels.job)\t\(.labels.instance)\t\(.health)"'
curl -sS http://127.0.0.1:9090/api/v1/targets \
  | jq '[.data.activeTargets[] | select(.health != "up")] | length'
curl -sS http://127.0.0.1:9090/api/v1/rules \
  | jq -r '.data.groups[].rules[] | "\(.name)\t\(.health)"'
curl -sf -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9093/-/ready
curl -sS http://127.0.0.1:18801/health
```

Every target should report `up` (the count of non-`up` targets is `0`), every
rule `ok`, Alertmanager `200`, and the webhook `{"status":"ok"}`. Send a
synthetic alert through Alertmanager; it appears in the daemon log after the
default `group_wait` (about 30 seconds):

```bash
curl -sS -X POST http://127.0.0.1:9093/api/v2/alerts \
  -H 'Content-Type: application/json' \
  -d '[{"labels":{"alertname":"WebhookTest","severity":"warning"},
        "annotations":{"summary":"test"}}]'
```

Send the same alert with an `endsAt` in the past to resolve it.

## Grafana

Grafana generates its admin password; retrieve it without writing it to the
Ansible log:

```bash
GF_PASSWORD=$(ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml \
  -e HOSTS=obs-01 | sed -n 's/^ *Login password: //p')
GRAFANA=http://obs-01:3000
```

Create the Prometheus data source (the URL is loopback because both containers
use host networking), then import the two dashboards:

```bash
curl -sS -u "admin:$GF_PASSWORD" \
  -H 'Content-Type: application/json' \
  -X POST "$GRAFANA/api/datasources" \
  --data '{
    "name": "Prometheus",
    "type": "prometheus",
    "url": "http://127.0.0.1:9090",
    "access": "proxy",
    "isDefault": true,
    "editable": true,
    "jsonData": {"httpMethod": "POST"}
  }'

curl -sS -o /tmp/node-exporter-full.json \
  https://grafana.com/api/dashboards/1860/revisions/latest/download
jq --arg ds Prometheus '
  .templating.list |= map(
    if .name == "ds_prometheus"
    then .current = {selected: true, text: $ds, value: $ds}
    else . end)
  | {dashboard: ., overwrite: true, folderId: 0}
' /tmp/node-exporter-full.json > /tmp/node-exporter-import.json
curl -sS -u "admin:$GF_PASSWORD" -H 'Content-Type: application/json' \
  -X POST "$GRAFANA/api/dashboards/db" --data @/tmp/node-exporter-import.json

jq -n --slurpfile d group_vars/observable/postgresql-pgexporter.json \
  '{dashboard: $d[0], overwrite: true, folderId: 0}' > /tmp/pg-dashboard-import.json
curl -sS -u "admin:$GF_PASSWORD" -H 'Content-Type: application/json' \
  -X POST "$GRAFANA/api/dashboards/db" --data @/tmp/pg-dashboard-import.json
```

The repository dashboard is named **Pgcli PostgreSQL** and has the fixed UID
`pgcli-pgexporter`, so the second import updates it in place. The Node Exporter
Full dashboard keeps the UID from its JSON. A repeat data source POST returns
HTTP 409; the existing data source is then reused. The **Instance** variable
defaults to the first PostgreSQL target; select `All` or add more instances
from the picker when managing many databases.

## Security notes

- Keep Prometheus, Alertmanager, and the webhook on loopback. Alertmanager's
  silence API is as sensitive as a notification channel.
- `pgexporter` on port 9630 is unauthenticated; restrict it to the monitoring
  network with the host firewall.
- The monitor password, Alertmanager routing secrets, and any third-party API
  token stay outside the repository (`secrets.yaml` is gitignored; the webhook
  reads its token from a 0600 file).
- A machine that is removed from `[observable]` also disappears from the
  Prometheus scrape list on the next apply; its `nodeexporter` container is
  removed only if you run an explicit remove/absent state for it.
