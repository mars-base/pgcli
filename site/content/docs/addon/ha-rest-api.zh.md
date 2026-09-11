---
title: "Patroni REST API"
description: "Patroni REST API 端点参考：健康检查、监控、集群管理"
weight: 47
---

Patroni 在每个成员上暴露一个 HTTP REST API，提供健康检查端点（供负载均衡器和
Kubernetes 探针使用）、监控数据（含 Prometheus 指标）以及集群管理操作。

> 参考：[Patroni 官方文档](https://patroni.readthedocs.io/en/latest/rest_api.html)、
> [Pigsty REST API](https://pigsty.cc/docs/patroni/rest_api/)。

## 查找 REST API

每个成员的 REST API 端口由 pgcli 自动分配，存储在 `pg.yaml` 的
`addons.patroni.<scope>.members.<name>.restapi_port` 下。端口也写入渲染后的
`patroni.yml` 的 `restapi.connect_address`。

```bash
# 查找 REST API 端口
pg ha status app          # 显示每个成员的 pg= 和 rest= 端口
```

### 认证

pgcli 在 REST API 上启用了 basic-auth（用户名 `postgres`，自动生成密码）。
所有请求需要 `-u <user>:<password>`：

```bash
curl -u postgres:<restapi-密码> http://<host>:<port>/health
```

密码与 `patroni.yml` 的 `restapi.authentication` 中使用的 `restapi_password`
相同。通过导出命令获取：

```bash
# 导出密码到文件，然后提取 restapi_password
pg ha passwords app --file app-passwd.yml
grep restapi_password app-passwd.yml
# restapi_password: <密码>
```

## 健康检查端点

所有健康检查端点响应 `GET` 请求。Patroni 返回描述节点状态的 JSON 文档，附带
HTTP 状态码。仅需状态码时可用 `HEAD` 或 `OPTIONS` 代替 `GET`（无响应体）。

### 仅主库端点（仅 leader 返回 200）

以下端点**仅**在节点为当前持有 leader 锁的 leader 时返回 HTTP `200`：

| 端点 | 说明 |
|------|------|
| `GET /` | 根路径 —— 主库健康检查 |
| `GET /primary` | `/` 的别名 |
| `GET /read-write` | `/` 的别名 |
| `GET /leader` | 类似 `/`，但不区分 primary 和 standby_leader |
| `GET /master` | `/leader` 的传统别名 |

```bash
# leader 返回 200，replica 返回 503
curl -s -u postgres:<pw> http://<leader-host>:<port>/primary -w "%{http_code}"
```

这些端点适用于**负载均衡器健康检查**，将写操作仅路由到当前主库。

### 仅副本端点（仅 replica 返回 200）

| 端点 | 说明 |
|------|------|
| `GET /replica` | 副本健康检查 —— 节点运行中、角色为 replica、且未设置 `noloadbalance` 标签时返回 200 |
| `GET /replica?replication_state=streaming` | 仅当副本正在流式复制（而非通过归档恢复追赶）时返回 200 |
| `GET /replica?lag=<max>` | 仅当复制延迟低于阈值时返回 200（字节或可读格式：`10MB`、`1GB`） |

```bash
# 仅流式副本
curl -s -u postgres:<pw> "http://<replica-host>:<port>/replica?replication_state=streaming"

# 延迟 < 1 MB 的副本
curl -s -u postgres:<pw> "http://<replica-host>:<port>/replica?lag=1048576"
```

### 只读端点（主库和副本均返回 200）

| 端点 | 说明 |
|------|------|
| `GET /read-only` | 任何运行中的节点（主库或副本） |
| `GET /synchronous` / `GET /sync` | 仅同步 standby |
| `GET /asynchronous` / `GET /async` | 仅异步 standby |
| `GET /read-only-sync` | 主库 + 同步 standby |
| `GET /read-only-quorum` | 主库 + quorum standby |
| `GET /quorum` | 仅 quorum standby |

### PostgreSQL 健康

| 端点 | 说明 |
|------|------|
| `GET /health` | PostgreSQL 运行中时返回 200（不论角色） |

## Kubernetes 探针

| 端点 | 说明 |
|------|------|
| `GET /liveness` | Patroni 心跳循环正常运行时返回 200。主库上次心跳超过 `ttl` 秒、或副本超过 `2*ttl` 秒时返回 503。轻量级——不执行 SQL。适用于 `livenessProbe`。 |
| `GET /readiness` | 节点为 leader 时返回 200；或 PostgreSQL 运行中、正在复制、且延迟在允许范围内时返回 200。接受 `?lag=<max>`（默认 `maximum_lag_on_failover`）和 `?mode=apply\|write`（默认 `apply`）。适用于 `readinessProbe`。 |

```yaml
# Kubernetes 探针示例
livenessProbe:
  httpGet:
    scheme: HTTP
    path: /liveness
    port: 8008          # REST API 端口
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

## 监控端点

### GET /patroni

以 JSON 返回详细的节点状态。Patroni 在 leader 竞选期间内部调用，也供监控系统使用：

```bash
curl -s -u postgres:<pw> http://<host>:<port>/patroni | jq .
```

响应字段：

| 字段 | 说明 |
|------|------|
| `state` | 节点状态：`running`、`stopped`、`starting` 等 |
| `role` | `primary` 或 `replica` |
| `server_version` | PostgreSQL 版本（整数） |
| `xlog.location` | 当前 WAL 位置（仅主库） |
| `xlog.received_location` | 从主库接收的 WAL（仅副本） |
| `xlog.replayed_location` | 已回放的 WAL（仅副本） |
| `timeline` | 当前时间线编号 |
| `replication` | 已连接的副本数组（仅主库） |
| `cluster_unlocked` | 未持有 leader 锁时为 `true` |
| `pause` | 自动故障切换暂停时为 `true` |
| `dcs_last_seen` | 上次成功联系 DCS 的 epoch 时间戳 |
| `patroni.version` | Patroni 版本 |
| `patroni.scope` | 集群 scope 名 |
| `patroni.name` | 成员名 |

### GET /cluster

返回完整的集群拓扑 —— 所有成员及其角色、状态和复制状态：

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

返回存储在 DCS 中的当前动态配置：

```bash
curl -s -u postgres:<pw> http://<host>:<port>/config | jq .
```

等同于 `pg ha edit-config <scope> --show`。

### GET /history

返回时间线历史。未发生时间线切换时（新集群）为空数组 `[]`。

### GET /metrics

以 **Prometheus 格式**返回监控数据，供 Prometheus 或兼容系统抓取：

```bash
curl -s -u postgres:<pw> http://<host>:<port>/metrics
```

关键指标：

| 指标 | 说明 |
|------|------|
| `patroni_primary` | 本节点为 leader 时为 1 |
| `patroni_replica` | 本节点为副本时为 1 |
| `patroni_postgres_running` | PostgreSQL 运行中时为 1 |
| `patroni_postgres_streaming` | PostgreSQL 正在流式复制时为 1（副本） |
| `patroni_xlog_location` | 当前 WAL 位置（仅主库） |
| `patroni_xlog_received_location` | 接收的 WAL（仅副本） |
| `patroni_xlog_replayed_location` | 回放的 WAL（仅副本） |
| `patroni_cluster_unlocked` | 未持有 leader 锁时为 1 |
| `patroni_is_paused` | 自动故障切换禁用时为 1 |
| `patroni_pending_restart` | 节点需要重启时为 1 |
| `patroni_postgres_timeline` | 当前时间线 |
| `patroni_dcs_last_seen` | 上次 DCS 联系的 epoch |
| `patroni_server_version` | PostgreSQL 版本 |

## 基于标签的过滤

健康检查端点接受查询参数，按成员 `patroni.yml` 的 `tags` 节中定义的自定义
标签过滤：

```bash
# 仅带 dc=us-east 标签的副本
curl -u postgres:<pw> "http://<host>:<port>/replica?dc=us-east"

# 仅带 region=primary 标签的 leader
curl -u postgres:<pw> "http://<host>:<port>/leader?region=primary"
```

## 实用模式

### 负载均衡器路由

使用 REST API 健康检查正确路由流量：

```
# 写操作仅路由到主库
upstream pg_primary {
    server node1:8008 max_fails=1 fail_timeout=10s;
    server node2:8009 max_fails=1 fail_timeout=10s;
    # 检查: GET /primary（仅 leader 返回 200）
}

# 读操作路由到副本
upstream pg_replica {
    server node1:8008 max_fails=1 fail_timeout=10s;
    server node2:8009 max_fails=1 fail_timeout=10s;
    # 检查: GET /replica（仅 replica 返回 200）
}
```

### curl 监控脚本

快速健康检查脚本：

```bash
#!/bin/bash
# 检查所有成员
for port in 8008 8009; do
  code=$(curl -s -u postgres:<pw> http://10.0.0.11:$port/health -o /dev/null -w "%{http_code}")
  echo "端口 $port: HTTP $code"
done
```

### Prometheus 抓取配置

```yaml
scrape_configs:
  - job_name: patroni
    basic_auth:
      username: postgres
      password: <restapi-密码>
    static_configs:
      - targets:
        - '10.0.0.11:8008'   # node1
        - '10.0.0.11:8009'   # node2
    metrics_path: /metrics
```
