# Observability scenario (`[observable]`)

Four hosts mixing PostgreSQL topologies with a minimal Prometheus/Grafana
monitoring stack. The live variables are in `main.yml` (gitignored);
`main.yml.example` is the tracked template.

| Host | Role |
|------|------|
| `debian12-01` | stand-alone instance `pg01`, `nodeexporter`, `pgexporter` |
| `debian12-02` | primary `pg02`, `nodeexporter`, `pgexporter` |
| `debian12-03` | cross-host read-only replica `pg_primary_ro1`, `nodeexporter`, `pgexporter` |
| `debian12-04` | `nodeexporter`, `prometheus`, `alertmanager`, `grafana`, `alertmanager-webhook` daemon |

`pgcli_addons` is declared at group level here rather than in the `addon_*`
groups, so one run deploys the whole scenario:

```bash
cd ansible
uv run ansible-playbook playbooks/apply.yaml --limit observable
```

The webhook receiver is a host process managed by `pg daemon`, not an add-on.
Deploy or refresh only that part with the `daemon` tag:

```bash
cd ansible
uv run ansible-playbook playbooks/apply.yaml --limit observable --tags daemon
```

`nodeexporter` has no `host`, so it runs on every member. `pgexporter` runs on
the three database hosts and binds `0.0.0.0:9630` so the Prometheus on
`debian12-04` can scrape it; it has no authentication and also exposes its admin
endpoints, so keep port 9630 on the monitoring network.

The local `--pg-name` mode creates a low-privilege `pgexporter` role
(`pg_monitor`) on `pg01` and `pg02`, pinning its password with
`--monitor-password` so no post-install fetch is needed. `pg_primary_ro1` is a
physical standby and cannot execute that role-creation SQL, but streaming
replication carries the role created for `pg02` to the standby, so its exporter
uses `--dsn` (dial as-is) with the same role and password. The value is not in
the repo; put it in the gitignored `group_vars/observable/secrets.yaml` before
the first run:

```yaml
pgexporter_monitor_password: "<choose a strong value>"
```

The same value is used by both local-mode installs and the standby's `--dsn`
entry, so one `apply` creates all three. An exporter already installed with an
auto-generated password keeps it until recreated — add `force: true` to that
entry once (or reinstall it) to adopt the pinned value.

Prometheus listens on loopback because it has no web authentication; Grafana is
the exposed UI (`--listen=0.0.0.0`, port 3000). The `force: true` on the
Prometheus entry makes a re-run recreate the container so edits to `--scrape`
take effect — an already-installed add-on ignores changed params otherwise.

## Endpoints

| Component | Endpoint |
|-----------|----------|
| node_exporter | `http://<host>:9100/metrics` |
| pg_exporter | `http://<db-host>:9630/metrics` |
| Prometheus | `http://<prometheus-host>:9090` (loopback only) |
| Alertmanager | `http://<prometheus-host>:9093` (loopback only) |
| Alertmanager webhook | `http://127.0.0.1:18801/health` (loopback only) |
| Grafana | `http://<grafana-host>:3000` |

Confirm both scrape jobs are `up` (run on the Prometheus host, since it binds
loopback):

```bash
curl -sS http://127.0.0.1:9090/api/v1/targets \
  | jq -r '.data.activeTargets[] | "\(.labels.job)\t\(.labels.instance)\t\(.health)"'
```

The job names are `node-exporter` (four hosts) and `pg` (three hosts). For a
per-instance check directly on a database host:

```bash
curl -s http://127.0.0.1:9630/up        # 200 when the target is reachable
curl -s http://127.0.0.1:9630/metrics | grep '^pg_up'   # pg_up 1
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9630/replica
```

The last probe returns `200` on the standby and `404`/non-200 on a primary.

## Alerts and the webhook receiver

Prometheus loads `ansible/misc/prometheus/postgres.rules.yml` through the
`rule_files` mapping on the Prometheus add-on entry. The `pg_addon` role copies
it into pgcli's managed rules directory and passes `--rule-file` during
install. The file contains PostgreSQL, node, and Alertmanager self-monitoring
rules. Prometheus forwards firing alerts to the loopback-only Alertmanager; its
only receiver is a webhook.

The receiver is a stdlib-only Python HTTP server started by `pg daemon` on
`debian12-04`:

| Item | Value |
|------|-------|
| Working dir / script | `/home/pgcli/alertmanager-webhook/alertmanager_webhook.py` |
| Listen | `127.0.0.1:18801` (`POST /alertmanager`, `GET /health`) |
| Log | `/home/pgcli/alertmanager-webhook/logs/alertmanager-webhook.log` |
| Rotation | 10 MB, keep 7 |
| Autostart | enabled (`pg autostart enable --daemon --name alertmanager-webhook`) |

It prints one JSON line per alert and nothing else (access logs are
suppressed). Check it as the deployment account:

```bash
ssh debian12-04 'runuser -u pgcli -- /usr/local/bin/pg \
  -c /home/pgcli/.pgcli/pg.yaml daemon status alertmanager-webhook'
ssh debian12-04 'runuser -u pgcli -- /usr/local/bin/pg \
  -c /home/pgcli/.pgcli/pg.yaml daemon logs alertmanager-webhook -n 50'
ssh debian12-04 'runuser -u pgcli -- /usr/local/bin/pg \
  -c /home/pgcli/.pgcli/pg.yaml autostart status'
```

Send a synthetic alert through Alertmanager (it appears in the daemon log after
the default `group_wait` of 30 seconds):

```bash
ssh debian12-04 'curl -sS -X POST http://127.0.0.1:9093/api/v2/alerts \
  -H "Content-Type: application/json" \
  -d "[{\"labels\":{\"alertname\":\"WebhookTest\",\"severity\":\"warning\"},\"annotations\":{\"summary\":\"test\"}}]"'
```

To forward alerts to a custom third-party API, implement the HTTP call in
`deliver()` inside `ansible/misc/webhook/alertmanager_webhook.py`; the function
docstring shows the `urllib.request` pattern and retry semantics. Keep tokens
out of the repository (read them from a 0600 file owned by `pgcli`). Rerunning
the `--tags daemon` command above rewrites the file and restarts the daemon
when its content changed.

## Grafana API

pgcli does not provision Grafana data sources, so the Prometheus data source and
the host dashboard are created through the HTTP API once, after the add-on is
installed. The admin password is generated by pgcli; never commit it. The
snippets below assume `jq` is installed on the control node and are run from the
`ansible/` directory.

Retrieve it with the credentials playbook, which runs
`pg addon list --show-password` as the deployment account — no manual `runuser`
needed. The playbook refuses to run without `ANSIBLE_LOG_PATH=/dev/null` so the
plaintext credentials are not persisted in `var/ansible.log`:

```bash
cd ansible
ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml \
  -e HOSTS=<grafana-host>

GF_PASSWORD=$(ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml \
  -e HOSTS=<grafana-host> | sed -n 's/^ *Login password: //p')
GRAFANA=http://<grafana-host>:3000
```

### 1. Create the Prometheus data source

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
```

The URL is loopback because Prometheus binds `127.0.0.1` and both containers use
host networking. Grafana generates the data source UID unless the payload sends
one; step 2 therefore resolves it by name instead of hardcoding it. A repeat
call returns HTTP 409, after which the existing data source is simply reused.

### 2. Look up the UID and check health

```bash
DS_UID=$(curl -sS -u "admin:$GF_PASSWORD" \
  "$GRAFANA/api/datasources/name/Prometheus" | jq -r .uid)

curl -sS -u "admin:$GF_PASSWORD" \
  "$GRAFANA/api/datasources/uid/$DS_UID/health"
```

The health response is `{"status":"OK", ...}` when Grafana can reach Prometheus.

### 3. Import the Node Exporter Full dashboard

Grafana dashboard [1860](https://grafana.com/grafana/dashboards/1860) covers the
standard host metrics for a node_exporter target. Its `ds_prometheus` template
variable is normally bound by the web import dialog; `POST /api/dashboards/db`
does not prompt, so set `current` explicitly before posting:

```bash
curl -sS -o /tmp/node-exporter-full.json \
  https://grafana.com/api/dashboards/1860/revisions/latest/download

jq --arg ds Prometheus '
  .templating.list |= map(
    if .name == "ds_prometheus"
    then .current = {selected: true, text: $ds, value: $ds}
    else . end)
  | {dashboard: ., overwrite: true, folderId: 0}
' /tmp/node-exporter-full.json > /tmp/node-exporter-import.json

curl -sS -u "admin:$GF_PASSWORD" \
  -H 'Content-Type: application/json' \
  -X POST "$GRAFANA/api/dashboards/db" \
  --data @/tmp/node-exporter-import.json \
  | tee /tmp/node-exporter-import-response.json \
  | jq -r '"uid=\(.uid) url=\(.url)"'
```

The response carries the dashboard `uid` and `url`; keep the response file if
later commands need them. The UID comes from the imported JSON when it has one
(dashboard 1860 does), and is generated by Grafana otherwise. With a UID in the
JSON, `overwrite: true` updates the same dashboard in place; without one, each
import creates a new dashboard.

### 4. Import the PostgreSQL dashboard

`group_vars/observable/postgresql-pgexporter.json` is the pgcli-maintained
**Pgcli PostgreSQL** dashboard for the `pg` scrape job. Its UID is
`pgcli-pgexporter`, so `overwrite: true` updates the same dashboard on every
import:

```bash
jq -n --slurpfile d group_vars/observable/postgresql-pgexporter.json \
  '{dashboard: $d[0], overwrite: true, folderId: 0}' \
  > /tmp/pg-dashboard-import.json

curl -sS -u "admin:$GF_PASSWORD" \
  -H 'Content-Type: application/json' \
  -X POST "$GRAFANA/api/dashboards/db" \
  --data @/tmp/pg-dashboard-import.json \
  | jq -r '"uid=\(.uid) url=\(.url)"'
```

Run from `ansible/`. The dashboard's `datasource` variable is left unset, so
Grafana binds it to the default data source — the Prometheus created in step 1.
If the instance has more than one Prometheus data source, choose the right one
in the dashboard's data source picker after import.

The **Instance** variable defaults to the first PostgreSQL target rather than
`All`; **Database** lists only the databases of the selected instance
(`label_values(pg_db_xact_commit{instance=~"$instance"}, datname)`). Both allow
multiple selections.

### 5. Verify through Grafana

```bash
curl -sS -u "admin:$GF_PASSWORD" \
  "$GRAFANA/api/datasources/proxy/uid/$DS_UID/api/v1/query?query=up"
```

A working data source returns one series per scrape target — both the
`node-exporter` and `pg` jobs — with `"value":[...,"1"]`. Use
`query=pg_up` to check only the PostgreSQL targets.

## UIDs are deployment data

Do not copy UIDs between environments:

- The data source UID is Grafana-generated unless the create payload pins one.
  Resolve it by name (`/api/datasources/name/Prometheus`) as in step 2.
- The dashboard UID comes from the imported JSON, or is generated when the JSON
  has none. Capture it from the import response as in step 3.

Deleting and re-creating a resource, or importing a dashboard JSON without a
UID, changes these values; the commands above always resolve them dynamically.
