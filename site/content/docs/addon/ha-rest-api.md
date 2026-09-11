---
title: "Patroni REST API"
description: "Patroni REST API endpoints for health checks, monitoring, and cluster management"
weight: 47
---

Patroni exposes an HTTP REST API on each member, providing health check endpoints
(for load balancers and Kubernetes probes), monitoring data (including Prometheus
metrics), and cluster management operations.

> Reference: [Patroni official docs](https://patroni.readthedocs.io/en/latest/rest_api.html),
> [Pigsty REST API](https://pigsty.cc/docs/patroni/rest_api/).

## Finding the REST API

Each member's REST API port is assigned automatically by pgcli and stored in
`pg.yaml` under `addons.patroni.<scope>.members.<name>.restapi_port`. The port
is also embedded in the rendered `patroni.yml` as `restapi.connect_address`.

```bash
# Find REST API ports
pg ha status app          # shows pg= and rest= ports for each member
```

### Authentication

pgcli enables basic-auth on the REST API (username `postgres`, auto-generated
password). All requests require `-u <user>:<password>`:

```bash
curl -u postgres:<restapi-password> http://<host>:<port>/health
```

The password is the same `restapi_password` used in `patroni.yml`'s
`restapi.authentication` section. Export it with:

```bash
# Export passwords, then extract the restapi_password
pg ha passwords app --file app-passwd.yml
grep restapi_password app-passwd.yml
# restapi_password: <password>
```

## Health Check Endpoints

All health check endpoints respond with `GET` requests. Patroni returns a JSON
document describing the node state, along with an HTTP status code. Use `HEAD`
or `OPTIONS` instead of `GET` when only the status code is needed (no response
body).

### Primary-only endpoints (200 only on leader)

These return HTTP `200` **only** when the node is the current leader holding
the leader lock:

| Endpoint | Description |
|----------|-------------|
| `GET /` | Root — primary health check |
| `GET /primary` | Alias for `/` |
| `GET /read-write` | Alias for `/` |
| `GET /leader` | Like `/` but doesn't distinguish primary vs standby_leader |
| `GET /master` | Legacy alias for `/leader` |

```bash
# Returns 200 on leader, 503 on replica
curl -s -u postgres:<pw> http://<leader-host>:<port>/primary -w "%{http_code}"
```

These endpoints are useful for **load balancer health checks** that should route
writes only to the current primary.

### Replica-only endpoints (200 only on replica)

| Endpoint | Description |
|----------|-------------|
| `GET /replica` | Replica health check — 200 when node is running, role is replica, and `noloadbalance` tag is not set |
| `GET /replica?replication_state=streaming` | Only 200 when replica is actively streaming (not still catching up via archive recovery) |
| `GET /replica?lag=<max>` | Only 200 when replication lag is below the threshold (bytes or human-readable: `10MB`, `1GB`) |

```bash
# Streaming replica only
curl -s -u postgres:<pw> "http://<replica-host>:<port>/replica?replication_state=streaming"

# Replica with lag < 1 MB
curl -s -u postgres:<pw> "http://<replica-host>:<port>/replica?lag=1048576"
```

### Read-only endpoints (200 on both primary and replica)

| Endpoint | Description |
|----------|-------------|
| `GET /read-only` | Any running node (primary or replica) |
| `GET /synchronous` / `GET /sync` | Sync standby only |
| `GET /asynchronous` / `GET /async` | Async standby only |
| `GET /read-only-sync` | Primary + sync standby |
| `GET /read-only-quorum` | Primary + quorum standby |
| `GET /quorum` | Quorum standby only |

### PostgreSQL health

| Endpoint | Description |
|----------|-------------|
| `GET /health` | 200 when PostgreSQL is running (regardless of role) |

## Kubernetes Probes

| Endpoint | Description |
|----------|-------------|
| `GET /liveness` | 200 if Patroni heartbeat loop is running. 503 if primary's last heartbeat exceeds `ttl` seconds, or replica exceeds `2*ttl`. Lightweight — no SQL queries. Suitable for `livenessProbe`. |
| `GET /readiness` | 200 when node is leader, or when PostgreSQL is running, replicating, and within the allowed lag. Accepts `?lag=<max>` (default: `maximum_lag_on_failover`) and `?mode=apply\|write` (default: `apply`). Suitable for `readinessProbe`. |

```yaml
# Example Kubernetes probes
livenessProbe:
  httpGet:
    scheme: HTTP
    path: /liveness
    port: 8008          # REST API port
  initialDelaySeconds: 3
  periodSeconds: 10
  timeoutSeconds: 5
  failureThreshold: 3

readinessProbe:
  httpGet:
    scheme: HTTP
    path: /readiness
    port: 8008
  initialDelaySeconds: 3
  periodSeconds: 10
  timeoutSeconds: 5
  failureThreshold: 3
```

## Monitoring Endpoints

### GET /patroni

Returns detailed node status as JSON. Used internally by Patroni during leader
election and also useful for monitoring:

```bash
curl -s -u postgres:<pw> http://<host>:<port>/patroni | jq .
```

Response fields:

| Field | Description |
|-------|-------------|
| `state` | Node state: `running`, `stopped`, `starting`, etc. |
| `role` | `primary` or `replica` |
| `server_version` | PostgreSQL version as integer |
| `xlog.location` | Current WAL position (primary only) |
| `xlog.received_location` | WAL received from primary (replica only) |
| `xlog.replayed_location` | WAL replayed (replica only) |
| `timeline` | Current timeline number |
| `replication` | Array of connected replicas (primary only) |
| `cluster_unlocked` | `true` if no leader lock is held |
| `pause` | `true` if auto-failover is paused |
| `dcs_last_seen` | Epoch timestamp of last successful DCS contact |
| `patroni.version` | Patroni version |
| `patroni.scope` | Cluster scope name |
| `patroni.name` | Member name |

### GET /cluster

Returns the full cluster topology — all members with their roles, states, and
replication status:

```bash
curl -s -u postgres:<pw> http://<host>:<port>/cluster | jq .
```

```json
{
  "members": [
    {
      "name": "node1",
      "role": "leader",
      "state": "running",
      "api_url": "http://10.0.0.11:8008/patroni",
      "host": "10.0.0.11",
      "port": 35532,
      "timeline": 1
    },
    {
      "name": "node2",
      "role": "replica",
      "state": "streaming",
      "host": "10.0.0.11",
      "port": 35533,
      "timeline": 1,
      "receive_lag": 0,
      "receive_lsn": "0/3000168",
      "replay_lag": 0,
      "replay_lsn": "0/3000168"
    }
  ],
  "scope": "app-default"
}
```

### GET /config

Returns the current dynamic configuration stored in the DCS:

```bash
curl -s -u postgres:<pw> http://<host>:<port>/config | jq .
```

This is equivalent to `pg ha edit-config <scope> --show`.

### GET /history

Returns the timeline history. Empty array `[]` when no timeline switch has
occurred (fresh cluster).

### GET /metrics

Returns monitoring data in **Prometheus exposition format**, suitable for
scraping by Prometheus or compatible systems:

```bash
curl -s -u postgres:<pw> http://<host>:<port>/metrics
```

Key metrics:

| Metric | Description |
|--------|-------------|
| `patroni_primary` | 1 if this node is the leader |
| `patroni_replica` | 1 if this node is a replica |
| `patroni_postgres_running` | 1 if PostgreSQL is running |
| `patroni_postgres_streaming` | 1 if PostgreSQL is streaming (replica) |
| `patroni_xlog_location` | Current WAL position (primary only) |
| `patroni_xlog_received_location` | WAL received (replica only) |
| `patroni_xlog_replayed_location` | WAL replayed (replica only) |
| `patroni_cluster_unlocked` | 1 if no leader lock |
| `patroni_is_paused` | 1 if auto-failover is disabled |
| `patroni_pending_restart` | 1 if node needs restart |
| `patroni_postgres_timeline` | Current timeline |
| `patroni_dcs_last_seen` | Epoch of last DCS contact |
| `patroni_server_version` | PostgreSQL version |

## Tag-based Filtering

Health check endpoints accept query parameters to filter by custom tags defined
in the member's `patroni.yml` `tags` section:

```bash
# Only replicas with tag dc=us-east
curl -u postgres:<pw> "http://<host>:<port>/replica?dc=us-east"

# Only leader with tag region=primary
curl -u postgres:<pw> "http://<host>:<port>/leader?region=primary"
```

## Practical Patterns

### Load balancer routing

Use REST API health checks to route traffic correctly:

```
# Route writes to primary only
upstream pg_primary {
    server node1:8008 max_fails=1 fail_timeout=10s;
    server node2:8009 max_fails=1 fail_timeout=10s;
    # Check: GET /primary (200 only on leader)
}

# Route reads to replicas
upstream pg_replica {
    server node1:8008 max_fails=1 fail_timeout=10s;
    server node2:8009 max_fails=1 fail_timeout=10s;
    # Check: GET /replica (200 only on replica)
}
```

### Monitoring with curl

Quick health check script:

```bash
#!/bin/bash
# Check all members
for port in 8008 8009; do
  code=$(curl -s -u postgres:<pw> http://10.0.0.11:$port/health -o /dev/null -w "%{http_code}")
  echo "Port $port: HTTP $code"
done
```

### Prometheus scrape config

```yaml
scrape_configs:
  - job_name: patroni
    basic_auth:
      username: postgres
      password: <restapi-password>
    static_configs:
      - targets:
        - '10.0.0.11:8008'   # node1
        - '10.0.0.11:8009'   # node2
    metrics_path: /metrics
```
