---
title: "RabbitMQ"
description: "Run a RabbitMQ broker as a managed addon — single node or a multi-host cluster formed by etcd peer discovery, AMQP plus the management web UI/API, optional TLS (generated CA or bring your own certificate), and queue management (list/declare/inspect, publish/get, bind/unbind, purge/delete)"
weight: 66
---

`pg addon install rabbitmq` runs **RabbitMQ 4.4.0** as a managed container from the official `docker.io/library/rabbitmq` image (the `-management` variant, which bundles the web UI and HTTP API). Like Kafka and ClickHouse, RabbitMQ is a **top-level addon**: pgcli renders `rabbitmq.conf`, owns the administrator account, bind-mounts the data directory and runs the container on the host network.

The addon runs **either a single node or one member of a multi-host cluster**. Clustering uses **etcd peer discovery** (`rabbitmq_peer_discovery_etcd`): you install one member per host with the same `--cluster` name and the list of etcd endpoints, and the members find each other on first boot — no static peer list and no manual `join_cluster`. Federation, shovel, MQTT and STOMP are not configured; advanced administration (vhosts, users, permissions, policies and exchanges) is done through the management UI or `pg addon rabbitmq shell` + `rabbitmqctl`. Common queue operations are wrapped by `pg addon rabbitmq queue` (see [Queues and messages](#queues-and-messages)). In cluster mode `declare` creates **quorum** queues by default (see [Clustering](#clustering)).

> **Authentication is always on.** AMQP and the management UI require the administrator account pgcli creates (default `admin`, generated 20-character password). `--listen` defaults to `127.0.0.1`. When you expose it on a network, treat the password as the security boundary: TLS encrypts traffic but does **not** authenticate clients (`verify_none`, `fail_if_no_peer_cert = false`).

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `rabbitmq` | Instance name (map key; also the default subcommand name) |
| `--version` | `4.4.0` | Upstream image tag, pulled directly as `docker.io/library/rabbitmq:<version>-management` |
| `--admin-user` | `admin` | Administrator account created on first boot and reconciled on every start |
| `--admin-password` | generated (20 chars) | Administrator password; stored in `pg.yaml` and in a `0600` secret file. Rejected in cluster mode — use `--secrets-from-etcd` or `--admin-password-file` |
| `--admin-password-file FILE` | — | Read the administrator password from `FILE` (one line). Required for the first install of a cluster member unless `--secrets-from-etcd` is used; also accepted for standalone instances |
| `--data-dir` | `<base_dir>/addon/rabbitmq/<name>/data` | Data directory bind-mounted at `/var/lib/rabbitmq`; must be an absolute path |
| `--listen` | `127.0.0.1` standalone, `0.0.0.0` cluster | Bind address shared by AMQP, AMQPS (when `--tls`) and the management listeners. `0.0.0.0` exposes all enabled listeners. Must not be loopback in cluster mode |
| `--cluster NAME` | — | Join/create the RabbitMQ cluster `NAME`. Empty = standalone. All members must use the same name |
| `--node-name NODE@HOST` | — | This member's Erlang node name (for example `rabbit@192.0.2.11`). Required for the first install of a cluster member; must be unique. An IP host needs no name resolution; a hostname must resolve on every member |
| `--etcd m1,m2` | — | Local etcd addon member name(s) to use as discovery endpoints. Mutually exclusive with `--etcd-endpoints` (which wins) |
| `--etcd-endpoints host:port,...` | — | External etcd endpoints for discovery; repeatable or comma-separated. Plain HTTP only; `http://` is accepted and stripped |
| `--erlang-cookie-file FILE` | — | Read the shared Erlang cookie from `FILE`. Required for the first install of a cluster member unless `--secrets-from-etcd` is used; the value is stored and cannot change afterwards |
| `--secrets-from-etcd` | false | Read both shared secrets from the etcd keys written by `pg addon rabbitmq secret generate` (recommended; no file copying between hosts). Cannot be combined with `--erlang-cookie-file` or `--admin-password-file` |
| `--amqp-port` | auto (base `5672`) | Plaintext AMQP port; `0` = auto-assign from `rabbitmq_start_port` |
| `--management-port` | auto (base `15672`) | Management HTTP UI/API port; `0` = auto-assign from `rabbitmq_management_start_port` |
| `--tls` | false | Add AMQPS and management HTTPS next to the plaintext listeners, using a certificate from pgcli's self-signed CA |
| `--tls-cert FILE` | — | Bring-your-own server certificate (PEM chain). Must be passed together with `--tls-key`; implies `--tls`. Must cover `--listen` unless the listen address is `0.0.0.0` |
| `--tls-key FILE` | — | Bring-your-own private key (PKCS#8/PKCS#1/SEC1), paired with `--tls-cert` |
| `--tls-port` | auto (base `5671`) | AMQPS port; `0` = auto-assign from `rabbitmq_tls_start_port`; requires `--tls` |
| `--config-option K=V` | — | Extra `rabbitmq.conf` setting (`heartbeat=60`, `log.file.level=debug`); repeatable; **replaces** the stored override map |
| `--force` | false | Re-render the config and recreate the container, keeping data and the stored password. In cluster mode `--cluster`, `--node-name` and the Erlang cookie are immutable; `--etcd-endpoints` may change |

`--image` does not apply to RabbitMQ — use `--version`. `--version` accepts any safe upstream tag, but the tag must exist in the registry; the image reference is always `docker.io/library/rabbitmq:<version>-management`.

## Install

```bash
pg addon install rabbitmq --name rmq
```

```
✓ RabbitMQ "rmq" installed
  Version:    4.4.0
  Image:      docker.io/library/rabbitmq:4.4.0-management
  Container:  pgcli-rabbitmq-default-rmq
  AMQP:       amqp://127.0.0.1:5672
  Management: http://127.0.0.1:15672
  Admin:      admin
  Password:   <generated>
  Data dir:   /home/you/.pgcli/addon/rabbitmq/rmq/data
  Config:     /home/you/.pgcli/addon/rabbitmq/rmq/config/rabbitmq.conf
```

A second instance takes the next free port from every pool (for example AMQP `5673`, management `15673`, EPMD `4370`, distribution `25673`):

```bash
pg addon install rabbitmq --name rmq2 --data-dir /srv/rabbitmq-data --listen 0.0.0.0
```

Re-running `install` without `--force` is a no-op that reports the current state. With `--force`, pgcli re-renders `rabbitmq.conf`, rewrites `enabled_plugins`, recreates the container and re-applies the administrator credentials; the data directory and password are kept.

The password is printed once at install time and can be read back at any time:

```bash
pg addon password rabbitmq --name rmq
```

## Management UI and API

The management web UI is served on the management port:

```text
standalone: http://127.0.0.1:15672      (default --listen 127.0.0.1)
cluster:    http://<member-ip>:15672    (any member, e.g. http://192.0.2.11:15672)
```

Run `pg addon rabbitmq status --name rmq` to print the exact `Management:` URL for the instance — it follows `--listen` and `--management-port`. Open that URL and sign in with the admin account; retrieve the password with `pg addon password rabbitmq --name rmq`. In cluster mode every member serves the same UI, and **Overview → Nodes** shows the whole cluster.

The same endpoint serves the HTTP API:

```bash
pw="$(pg addon password rabbitmq --name rmq)"
curl -fsS -u "admin:$pw" http://127.0.0.1:15672/api/overview
```

The UI is the supported path for everything the CLI does not wrap: vhosts, users, permissions, policies and exchange definitions. Common queue operations are available as `pg addon rabbitmq queue ...` commands (see [Queues and messages](#queues-and-messages)).

## Ports

| Endpoint | Default | Pool |
|---|---|---|
| AMQP (plaintext) | `5672` | `rabbitmq_start_port`, default `5672` |
| AMQPS (TLS) | `5671` (when `--tls`) | `rabbitmq_tls_start_port`, default `5671` |
| Management HTTP UI/API | `15672` | `rabbitmq_management_start_port`, default `15672` |
| Management HTTPS | `15671` (when `--tls`) | `rabbitmq_management_tls_start_port`, default `15671` |
| Erlang EPMD | `4369` | `rabbitmq_epmd_start_port`, default `4369` |
| Erlang distribution | `25672` | `rabbitmq_dist_start_port`, default `25672` |

Each pool is allocated independently: instances are sorted by name, an explicit port is never overwritten, and a port already listening on the host is skipped. Ports must be unique across all RabbitMQ instances.

EPMD and distribution are infrastructure ports, not client endpoints. A standalone instance runs with `RABBITMQ_NODENAME=rabbit@localhost`, `ERL_EPMD_ADDRESS=127.0.0.1` and its own EPMD/distribution ports, and `rabbitmq.conf` pins `distribution.listener.interface = 127.0.0.1` — Erlang distribution is never exposed on the network, and multiple standalone instances coexist on one host without colliding with a host Erlang installation.

A **cluster member is different**: every member uses the fixed EPMD port `4369` and distribution port `25672`, and distribution listens on `--listen` (default `0.0.0.0`). As a consequence, **at most one clustered RabbitMQ instance can run per host**, and `4369`, `25672` must be reachable from the other members (see [Clustering](#clustering)).

The `rabbitmq_prometheus` plugin is **not enabled**. pgcli mounts its own `enabled_plugins` file containing only `rabbitmq_management` (`rabbitmq_management` and `rabbitmq_peer_discovery_etcd` in cluster mode), so every instance behaves the same and none opens a second unauthenticated metrics listener.

## Clustering

A cluster is formed by **etcd peer discovery**, not by a static peer list. Each host runs one member with the same `--cluster` name and an etcd endpoint list; on first boot the member registers itself in etcd and joins whichever nodes are already registered.

Prerequisites:

- **etcd 3.4+** reachable from every member on plain HTTP. A single-member etcd is enough to test, but a production discovery store should itself be redundant. pgcli does not manage TLS or authentication for this etcd connection in this release — keep it on a trusted network. You can run it as the pgcli etcd addon (`pg addon install etcd ...`) or use an external etcd with `--etcd-endpoints`.
- **Unique node names.** `--node-name rabbit@host` must be unique in the cluster. The host part may be a hostname or an IPv4 address; loopback and IPv6 are rejected. With an **IP** the members connect directly and need no name resolution at all — recommended when there is no internal DNS. With a **hostname** every member must resolve it (DNS or `/etc/hosts`): etcd discovery registers only `rabbit@host`, it does not provide addresses. FQDNs and IPs make RabbitMQ use Erlang long names automatically.
- **Shared secrets.** All members need the **same Erlang cookie** and the **same administrator password**. Generate them once with `pg addon rabbitmq secret generate`; it writes local `0600` files and publishes their contents to etcd, and every member then installs with `--secrets-from-etcd`. pgcli stores both values in `pg.yaml` and the `0600` secret file, so `start` and autostart do not need etcd or the original files. The older `--erlang-cookie-file` / `--admin-password-file` pair still works when you prefer to distribute files yourself. These are cluster-level secrets: AMQP clients need neither.
- **Same version.** Use the same `--version` on every member.
- **Same cluster name.** A different `--cluster` value creates a separate cluster sharing the etcd store; pgcli isolates them with `cluster_formation.etcd.cluster_name`.

On the management host, generate and publish the shared secrets once:

```bash
pg addon rabbitmq secret generate --cluster pgcli-lab \
  --etcd-endpoints 192.0.2.10:2379
```

The values are never printed. The command writes `/tmp/rmq.cookie` and `/tmp/rmq.admin` (mode `0600`) and stores the same contents at these etcd keys (not files):

```text
/pgcli/rabbitmq/clusters/pgcli-lab/secrets/erlang_cookie
/pgcli/rabbitmq/clusters/pgcli-lab/secrets/admin_password
```

The two local files have distinct purposes:

| File | Purpose |
|---|---|
| `/tmp/rmq.cookie` | The shared **Erlang cookie**. Cluster nodes use it only for the Erlang distribution handshake between members (EPMD `4369`, distribution `25672`). AMQP and management clients never use it. |
| `/tmp/rmq.admin` | The shared **broker administrator password**, used to create/reconcile the `admin` account. It is an installation input, not a client-side credential: applications should connect with their own RabbitMQ users. To sign in as the administrator interactively, read the stored value with `pg addon password rabbitmq --name mq` instead of keeping the file. |

Both files are **bootstrap/distribution artifacts**. Each member stores the values in `pg.yaml` and its own `0600` secret file during installation, and `start`/autostart never read the `/tmp` files, so they can be deleted once every member is installed. When you distribute secrets manually instead of using `--secrets-from-etcd`, these are the files you copy to each host (contents must be identical) and pass as `--erlang-cookie-file` / `--admin-password-file`; the paths may differ per host. Never generate a new Erlang cookie after members are installed — it is immutable and members with mismatched cookies cannot join.

Install three members from the member hosts (only `--node-name` differs; using each host's IP avoids `/etc/hosts`/DNS):

```bash
# on 192.0.2.11
pg addon install rabbitmq --name mq --cluster pgcli-lab \
  --node-name rabbit@192.0.2.11 \
  --etcd-endpoints 192.0.2.10:2379 \
  --secrets-from-etcd

# on 192.0.2.12
pg addon install rabbitmq --name mq --cluster pgcli-lab \
  --node-name rabbit@192.0.2.12 \
  --etcd-endpoints 192.0.2.10:2379 \
  --secrets-from-etcd

# on 192.0.2.13
pg addon install rabbitmq --name mq --cluster pgcli-lab \
  --node-name rabbit@192.0.2.13 \
  --etcd-endpoints 192.0.2.10:2379 \
  --secrets-from-etcd
```

Every member reads the same values, so no file copying is required. If you prefer manual distribution, skip `secret generate` and pass the two `--*-file` flags instead.

Etcd stores these values as **plaintext**. Anyone who can reach the etcd client port can read the cluster credentials, so restrict port `2379` to the member hosts and the management host. Do not put the secrets in a shared etcd that other tenants can read.

The first member starts a cluster of one and prints a hint; the others discover it through etcd and join automatically. Discovery runs only while Mnesia is empty — an already-joined member restarts by contacting its last-seen peers.

Discovery keys live under

```text
/rabbitmq/discovery/rabbitmq/clusters/<cluster-name>/nodes/<node-name>
```

so several clusters can share one etcd as long as their `--cluster` names differ.

### Queue semantics

- In cluster mode `pg addon rabbitmq queue declare` defaults to `--type quorum`. Quorum queues are replicated on every member (the default quorum size is 3, capped by the number of members) and are the only replicated queue type in RabbitMQ 4.x.
- `--type classic` still works but **classic queues are not replicated**: a classic queue lives on the node where it was declared and is unavailable if that node is down. Use classic only for non-critical or transient workloads.
- `--durable=false` and quorum + `--auto-delete` remain rejected (see [Queues and messages](#queues-and-messages)).
- Every node can serve clients and the management API shows the whole cluster, so clients may connect to any member. For production, configure several endpoints or put a TCP load balancer in front.

### Inspect and retire members

```bash
pg addon rabbitmq cluster status --name mq
pg addon rabbitmq cluster forget rabbit@192.0.2.13 --name mq --yes
```

`cluster status` prints `rabbitmqctl cluster_status`, including every running node. `cluster forget` removes a **stopped, permanently retired** member from the cluster metadata; it refuses to forget the local node. After forgetting a member, rebalance quorum queues so their replicas are spread over the remaining nodes:

```bash
pg addon rabbitmq shell --name mq
rabbitmq-queues rebalance quorum
```

A member that is only temporarily down needs no action: restart it and it rejoins from its last-seen peers. Do not call `cluster forget` while the node is still meant to come back — the old node will not be able to rejoin and must be wiped (or forgotten from the other side and re-added with a fresh data directory).

### Cluster constraints

- `--cluster`, `--node-name` and the Erlang cookie are **immutable**. Re-running `install --force` with any of them changed fails; `--etcd-endpoints` can be updated.
- A standalone instance cannot be converted to a cluster member (or vice versa): remove it with `--clean-data` and install again. First-time cluster installation also requires an empty data directory.
- `--advertise-host`, `--node-id`, `--peer` and `--join` do not apply to RabbitMQ; the node identity is `--node-name`. Plaintext `--admin-password` is rejected in cluster mode.
- The administrator password is a cluster-wide credential. All members must store the same value. To rotate it, publish a new one with `pg addon rabbitmq secret generate --force --keep-cookie ...` (or use `--admin-password-file ...`) and rerun `install --force --secrets-from-etcd` (or `--admin-password-file ...`) on **every** member. Never regenerate the Erlang cookie after members are installed: it is immutable and a changed value makes `install --force` fail.
- The etcd secret keys under `/pgcli/rabbitmq/clusters/<name>/secrets/` are **plaintext**. Restrict the etcd client port to the member and management hosts. These secrets are for the cluster, not for application clients; AMQP clients need neither the cookie nor the administrator password (create separate RabbitMQ users for applications).
- Cluster interconnect traffic (Erlang distribution on `25672`) is **not encrypted**. `--tls` protects client listeners only. Keep `4369` and `25672` on a private network, and allow them only between members.

## TLS

TLS is **additive**: `--tls` keeps the plaintext AMQP and management listeners and adds AMQPS plus management HTTPS.

```bash
pg addon install rabbitmq --name rmq --tls
pg addon install rabbitmq --name rmq --tls --tls-port 5671
```

`install` and `status` then print both sets of endpoints and the CA path:

```
  AMQP:       amqp://127.0.0.1:5672
  Management: http://127.0.0.1:15672
  AMQPS:      amqps://127.0.0.1:5671
  Mgmt TLS:   https://127.0.0.1:15671
  CA cert:    /home/you/.pgcli/addon/rabbitmq/rmq/config/tls/ca.crt
```

The generated certificate is issued by pgcli's self-signed CA (the same helper MinIO/silo/rustfs and Kafka use) and covers `localhost`, `127.0.0.1`, `::1`, every host NIC address and a concrete `--listen` address. Clients must trust `config/tls/ca.crt`:

```bash
openssl s_client -connect 127.0.0.1:5671 \
  -CAfile "$HOME/.pgcli/addon/rabbitmq/rmq/config/tls/ca.crt" \
  -verify_ip 127.0.0.1 -verify_return_error </dev/null

pw="$(pg addon password rabbitmq --name rmq)"
curl -fsS --cacert "$HOME/.pgcli/addon/rabbitmq/rmq/config/tls/ca.crt" \
  -u "admin:$pw" https://127.0.0.1:15671/api/overview
```

AMQP URL with TLS (e.g. for Python/pika or Go clients):

```text
amqps://admin:<password>@127.0.0.1:5671/
```

- TLS is **transport-only**. `ssl_options.verify = verify_none` and `fail_if_no_peer_cert = false` mean clients do not present certificates; anyone who can reach an enabled port and knows the password still gets in. The listen address and password remain the security boundary.
- The leaf certificate is regenerated when it is within 30 days of expiry (on install, start or `--force`). The CA is kept, so clients do not need a new trust file. This applies only to pgcli-generated certificates.
- `--tls-port` without `--tls` is rejected. Disable TLS with `--tls=false --force`; the generated files stay under `config/tls/` and are reused if TLS is re-enabled.

### Bring your own certificate (`pg cert`)

```bash
pg cert --host localhost,127.0.0.1,192.0.2.10 \
  --cert-file rmq.crt --key-file rmq.key

pg addon install rabbitmq --name rmq \
  --tls-cert rmq.crt --tls-key rmq.key
```

`--tls-cert` and `--tls-key` must be supplied together and imply `--tls`. Before any image pull or container work, pgcli validates that:

- the certificate parses and is currently valid;
- it allows server authentication and is not a CA certificate;
- the private key matches the certificate and is unencrypted;
- the certificate covers a concrete `--listen` address (a mismatch is a hard error; use `--listen 0.0.0.0` or add the address as a SAN).

The files are copied into the instance's `config/tls/` as `public.crt` and `private.key`, and the certificate copy also serves as `ca.crt`. A self-signed `pg cert` leaf is its own trust anchor, so clients can trust `rmq.crt` directly. If the certificate is signed by another CA, trust that CA's chain instead: `ca.crt` contains only what you supplied, which may not include the root.

With BYO TLS the source files are left untouched and pgcli does not renew the certificate — replace the source files and re-run `install --force` (or `--tls=false --force` to go back to plaintext).

## Queues and messages

`queue list`, `purge` and `delete` run `rabbitmqctl` inside the container and do
not need the management HTTP password. The other queue commands — `declare`,
`info`, `publish`, `get`, `bind`, `unbind`, `bindings` — use the management HTTP
API with the stored administrator credentials; the instance must be running and
the management port must be reachable from the host.

```bash
# declare and inspect
pg addon rabbitmq queue list --name rmq
pg addon rabbitmq queue declare orders --name rmq
pg addon rabbitmq queue info orders --name rmq
pg addon rabbitmq queue info orders --name rmq --json

# publish and consume
pg addon rabbitmq queue publish orders --payload '{"id":1}' --name rmq
echo '{"id":2}' | pg addon rabbitmq queue publish orders --payload-file - --name rmq
pg addon rabbitmq queue get orders --count 10             # peek: messages stay queued
pg addon rabbitmq queue get orders --count 10 --consume   # ack: remove them
pg addon rabbitmq queue get orders --json

# bindings (a real exchange is required; the default exchange cannot be bound)
pg addon rabbitmq queue bind orders --exchange amq.topic --routing-key 'order.*' --name rmq
pg addon rabbitmq queue bindings orders --name rmq
pg addon rabbitmq queue unbind orders --exchange amq.topic --routing-key 'order.*' --name rmq

# cleanup
pg addon rabbitmq queue purge orders --name rmq --yes
pg addon rabbitmq queue delete orders --name rmq --yes
pg addon rabbitmq queue delete orders --name rmq --if-empty --if-unused
```

- `list` prints `name`, `type`, `messages`, `ready`, `unacked` and `consumers`;
  an empty vhost prints `no queues`.
- `declare` creates a **durable classic** queue by default on a standalone instance, and a **durable quorum** queue by default on a cluster member (see [Clustering](#clustering)). RabbitMQ 4.x removed
  transient (non-durable, non-exclusive) queues, so `--durable=false` is
  rejected before the request is sent. `--type classic` or `--type quorum`
  overrides the default explicitly; quorum queues must be durable and cannot be
  auto-delete. Repeatable
  `--argument KEY=VALUE` sets queue arguments; values that parse as JSON keep
  their JSON type (`--argument x-max-priority=10`).
- **Message retention.** `--message-ttl 30m` (or a number of milliseconds) sets
  `x-message-ttl`: a message that waits in the queue longer is dropped, or
  dead-lettered when the queue has `x-dead-letter-exchange`. A single message
  can carry its own TTL with `--property 'expiration="60000"'`.
- **Automatic deletion.** `--auto-delete` deletes the queue when its last
  consumer disconnects. `--expires 24h` sets `x-expires`: the queue itself —
  including any messages still in it — is deleted once it has been unused for
  that long. A queue counts as used only when it has a consumer, is redeclared,
  or is accessed with `basic.get`; **publishing alone does not reset the
  timer**. The value must be positive (`0` is rejected by RabbitMQ). Both can
  also be set with `--argument`.
- `info` shows type, state, durable, auto-delete, message counters, consumers,
  memory, policy, arguments and node. `--json` prints the raw management API
  document. Counters are updated by RabbitMQ's stats collector, so they can lag
  a few seconds behind publish/get.
- `publish` sends one message. Exactly one of `--payload` or `--payload-file`
  is required (use `-` to read stdin). `--exchange` defaults to `amq.default`
  (the default exchange, which routes to the queue named by the routing key);
  `--routing-key` defaults to the queue name. Repeatable
  `--property KEY=VALUE` sets message properties, e.g.
  `--property delivery_mode=2 --property 'content_type="application/json"'`.
  An unroutable message prints a warning but is not an error.
- `get` peeks by default (`ack_requeue_true`); `--consume` acknowledges and
  removes the returned messages (`ack_requeue_false`). `--count` defaults to 1;
  `--json` prints the raw API response.
- `bind` / `unbind` require `--exchange` and refuse `amq.default` (the default
  exchange is bound implicitly). `--routing-key` defaults to the queue name.
  Repeatable `--argument` supports argument-based (headers) bindings. `unbind`
  without `--argument` removes every binding from that exchange with the given
  routing key; with `--argument` it removes only the exactly matching binding.
- `bindings` lists a queue's bindings and their arguments (`--json` for the raw
  document).
- `purge` removes every **message** but keeps the queue; `delete` removes the
  queue itself. Both ask for confirmation unless `--yes` is given.
  `--if-empty` / `--if-unused` (delete only) forward RabbitMQ's conditional
  checks: the delete fails instead of removing a non-empty queue or one with
  consumers.
- Every queue command accepts `--name` and `--vhost` (default `/`) and requires
  the instance to be running.

### Retention examples

A queue declared without retention flags keeps messages **indefinitely**:
there is no default TTL. Messages leave the queue only when they are consumed,
purged, dead-lettered, or the queue itself is deleted. Concrete limits:

```bash
# Queue-level TTL: every message expires 24 hours after it was published
pg addon rabbitmq queue declare orders --message-ttl 24h --name rmq

# Per-message TTL: this message expires 60 seconds after publishing
# (`expiration` is a string property in milliseconds)
pg addon rabbitmq queue publish orders --payload '{"id":1}' \
  --property 'expiration="60000"' --name rmq

# Idle queue deletion (not a message TTL): the queue itself and its
# messages are removed after 1 hour without consumers, redeclare or basic.get
pg addon rabbitmq queue declare scratch --expires 1h --name rmq

# Capacity cap instead of a time limit: keep at most 10000 messages
pg addon rabbitmq queue declare backlog --argument x-max-length=10000 --name rmq

# Route expired messages to a dead-letter exchange instead of dropping them
pg addon rabbitmq queue declare dlq-demo --message-ttl 24h \
  --argument x-dead-letter-exchange=dlx --name rmq
```

`--message-ttl` and `--expires` only apply when the queue is first declared.
To change retention on an existing queue without deleting it, use a policy:

```bash
pg addon rabbitmq shell --name rmq
rabbitmqctl set_policy orders-ttl '^orders$' \
  '{"message-ttl":86400000,"max-length":10000}' --apply-to queues
rabbitmqctl list_policies
```

Policies match queue names by regular expression, apply to new and existing
queues, and take precedence over the equivalent `x-*` queue arguments. A policy
can also be created in the management UI under **Admin → Policies**.

The same retention limits can be applied without redeclaring a queue through a
RabbitMQ **policy** (`message-ttl`, `expires`, `max-length`,
`max-length-bytes`); pgcli does not wrap policies yet — use the management UI or
`rabbitmqctl set_policy`.

`pg addon rabbitmq shell` opens `/bin/bash` in the container; `rabbitmqctl` and
`rabbitmq-diagnostics` are available there for everything the CLI and API do not
cover.

## Status and lifecycle

```bash
pg addon rabbitmq status --name rmq       # state, endpoints, TLS, admin, EPMD/dist, paths
pg addon start rabbitmq --name rmq        # start (create if missing), wait for readiness
pg addon stop rabbitmq --name rmq
pg addon remove rabbitmq --name rmq       # remove container + pg.yaml entry, keep data/config/secrets
pg addon remove rabbitmq --name rmq --clean-data   # also delete the instance dir and a custom data dir
pg logs addon rabbitmq --name rmq -f
```

`install`, `start` and `--force` wait up to **90 seconds** for `rabbitmqctl -q await_startup` on a standalone instance. A cluster member waits up to **180 seconds**, which also covers the initial peer-discovery and Mnesia bootstrap; each attempt is capped at 10 seconds because with peers present the command can succeed silently and still never exit. If the idle watchdog ends such an attempt, pgcli probes the AMQP port instead — the listener opens at the end of boot, so a reachable port means ready. After readiness pgcli reconciles the administrator account with `rabbitmqctl change_password` / `add_user` / `set_user_tags administrator` / `set_permissions`.

In cluster mode `status` additionally prints the cluster name, the Erlang node name and the etcd endpoints; `pg addon rabbitmq cluster status` shows the live view from `rabbitmqctl cluster_status`.

Without `--clean-data`, `remove` keeps the data dir, the generated config and the `0600` password secret. Reinstalling under the same name reuses that password, so clients keep working after a remove/reinstall cycle.

Auto-start after reboot:

```bash
pg autostart enable --rabbitmq --name rmq
pg autostart status
pg autostart disable --rabbitmq --name rmq
```

Boot start re-renders `rabbitmq.conf`, starts the container and re-applies the admin credentials. `pg start` also brings up autostart-enabled RabbitMQ instances.

## What pgcli creates

Everything for one instance lives under `<base_dir>/addon/rabbitmq/<name>/`:

| Host path | Container path | Notes |
|---|---|---|
| `config/rabbitmq.conf` | `/etc/rabbitmq/rabbitmq.conf` (read-only) | Rendered listeners, management, admin tags/permissions and distribution settings |
| `config/enabled_plugins` | `/etc/rabbitmq/enabled_plugins` (read-only) | `[rabbitmq_management].` standalone, `[rabbitmq_management,rabbitmq_peer_discovery_etcd].` in cluster mode |
| `config/tls/` | `/etc/rabbitmq/tls` (read-only, only with `--tls`) | `ca.crt` (0644), `public.crt` (chain, 0644), `private.key` (0600, chowned to uid 999); `ca.key` stays host-side and is not mounted |
| `data/` | `/var/lib/rabbitmq` | Mnesia, message store, definitions and logs; cluster mode adds `.erlang.cookie` (0400, chowned to uid 999) |
| `secrets/admin_password` | not mounted | `0600`, reused by reinstall |

The generated `rabbitmq.conf` for a plaintext instance:

```ini
listeners.tcp.default = 127.0.0.1:5672

management.tcp.ip = 127.0.0.1
management.tcp.port = 15672

default_user_tags.administrator = true
default_permissions.configure = .*
default_permissions.read = .*
default_permissions.write = .*

distribution.listener.interface = 127.0.0.1
distribution.listener.port_range.min = 25672
distribution.listener.port_range.max = 25672
```

With `--tls` the file additionally carries `listeners.ssl.default`, the `ssl_options.*` block and the `management.ssl.*` block. A cluster member instead binds to `0.0.0.0` (or the explicit `--listen` address) and appends the discovery block:

```ini
cluster_formation.peer_discovery_backend = etcd
cluster_formation.etcd.endpoints.1 = 192.0.2.10:2379
cluster_formation.etcd.cluster_name = pgcli-lab
```

No secret is written into `rabbitmq.conf`. The administrator password travels in the container environment (`RABBITMQ_DEFAULT_USER` / `RABBITMQ_DEFAULT_PASS`, which only seed an empty Mnesia; pgcli reconciles it with `rabbitmqctl` on every start), as do `RABBITMQ_NODENAME` and `RABBITMQ_USE_LONGNAME`. In cluster mode pgcli additionally writes the shared Erlang cookie to `data/.erlang.cookie` **before** the container starts (mode `0400`, owned by uid 999): the official 4.4 image ignores `RABBITMQ_ERLANG_COOKIE`, and a node left to generate its own random cookie cannot complete the distribution handshake with its peers.

The container runs with `--network host`, `--restart unless-stopped` and a 30-second stop timeout. It runs as the image's **uid/gid 999 (`rabbitmq`)**; pgcli hands only the TLS private key and the cluster cookie file to that uid (through `podman unshare` on rootless, `os.Chown` when running as root). The data directory is chowned by the image entrypoint itself.

## Configuration

Extra `rabbitmq.conf` entries can be set at install time:

```bash
pg addon install rabbitmq --name rmq --force \
  --config-option heartbeat=60 \
  --config-option log.file.level=debug
```

`--config-option` **replaces** the stored override map (it does not merge). Overrides are persisted in `pg.yaml` under `addons.rabbitmq.<name>.settings` and re-rendered on every start. Keys may contain letters, digits, dots, underscores and hyphens; values must not contain NUL, CR or LF.

pgcli-managed keys are rejected rather than silently overridden: `listeners.*`, `ssl_options.*`, `management.*`, `prometheus.*`, `cluster_formation.*`, `default_user`, `default_pass`, `default_user_tags.*`, `default_permissions.*`, `loopback_users.*` and `distribution.listener.*`.

## Password and account semantics

- `RABBITMQ_DEFAULT_USER` / `RABBITMQ_DEFAULT_PASS` only **seed an empty Mnesia database** on first boot. That is why pgcli reconciles the account on every start with `rabbitmqctl change_password` (falling back to `add_user` when the user does not exist), then sets the `administrator` tag and full `.* / .* / .*` permissions on vhost `/`.
- Changing `--admin-user` on an existing instance creates/reconciles the new account but **does not delete the previous one**. Remove stale accounts in the management UI or with `rabbitmqctl delete_user`.
- Changing `--admin-password` (or reviving a stored secret) is applied on the next `install --force` or `start`. In a cluster the password is shared: every member must store the same value, so rotate it on all members.
- In cluster mode the shared secrets come either from `--secrets-from-etcd` (recommended; keys written by `pg addon rabbitmq secret generate`) or from `--admin-password-file` plus `--erlang-cookie-file`. Plaintext `--admin-password` is rejected. Both values are then persisted in `pg.yaml` and the `0600` secret file.
- Passwords are stored both in `pg.yaml` and in the `0600` secret file; `pg addon password rabbitmq --name NAME` prints the stored value.

## Production notes

- **Single node or one cluster member.** A standalone instance has no HA: one stopped container means the broker is down. In cluster mode availability comes from **quorum queues** replicated across members; classic queues are not replicated and disappear with their node. Federation, shovel, MQTT and STOMP are not configured.
- **Cluster networking.** Open `4369` (EPMD) and `25672` (Erlang distribution) only between members on a private network. Distribution traffic is not encrypted even with `--tls`; TLS covers client listeners only. One clustered instance per host.
- **Discovery is plaintext.** etcd endpoints are contacted over plain HTTP without authentication. Anyone who can reach etcd can register a node in the cluster or read the shared secret keys written by `pg addon rabbitmq secret generate`; isolate it on a trusted network. etcd TLS/auth is not supported in this release.
- **No Prometheus plugin.** `enabled_plugins` is pinned to management (plus etcd discovery in cluster mode). Scrape metrics through the management API or enable a plugin manually in the container if you really need it — but note that every instance would then want the same default metrics port.
- **TLS is not client authentication.** Use `--listen 127.0.0.1` or a private network/firewall in front of the broker. The management UI is a full administrative surface; do not expose it casually.
- **TLS is additive.** Plaintext `amqp://` and `http://` stay enabled when `--tls` is on; clients that need encryption must select the `amqps://` / `https://` endpoints explicitly.
- **Upgrades.** `--force --version <new>` pulls a new tag and recreates the container on the same data dir. RabbitMQ does not guarantee downgrading data written by a newer version — take an offline copy before upgrading. `--version` must be a real upstream tag.
- **Backups.** Copy the data directory while the container is stopped, or use the management API's definitions export for users/vhosts/policies. There is no `pg addon rabbitmq backup` command in this release.
- **Data ownership.** Data files are owned by uid/gid 999. Use `--clean-data` instead of deleting the directory by hand; pgcli handles the permission fallback through `podman unshare`.
- **Platform.** Linux (host networking); the official image is multi-arch (amd64/arm64). On a rootful host, run pgcli as root so it can hand the TLS private key to uid 999; on rootless it uses `podman unshare`.

## CLI flag reference

### `pg addon install rabbitmq`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `rabbitmq` | Instance name; `[A-Za-z0-9][A-Za-z0-9_-]*`, max 48 chars |
| `--version TAG` | `4.4.0` | Upstream `docker.io/library/rabbitmq` tag; the `-management` suffix is added automatically |
| `--admin-user USER` | `admin` | Administrator account; `[A-Za-z0-9_.-]{1,64}` |
| `--admin-password PASS` | generated | Administrator password (no NUL/CR/LF); rejected in cluster mode |
| `--admin-password-file FILE` | — | Read the password from `FILE`; required for the first install of a cluster member unless `--secrets-from-etcd` is used |
| `--data-dir DIR` | `<base_dir>/addon/rabbitmq/<name>/data` | Absolute host data dir bind-mounted at `/var/lib/rabbitmq` |
| `--listen ADDR` | `127.0.0.1` (`0.0.0.0` in cluster mode) | Bind address for AMQP, AMQPS, management HTTP and management HTTPS; must not be loopback in cluster mode |
| `--cluster NAME` | — | Cluster name; empty = standalone |
| `--node-name NODE@HOST` | — | Erlang node name for this cluster member; required on first install |
| `--etcd M1,M2` | — | Local etcd addon members used for discovery |
| `--etcd-endpoints HOST:PORT,...` | — | External etcd endpoints used for discovery; takes precedence over `--etcd` |
| `--erlang-cookie-file FILE` | — | Read the shared Erlang cookie from `FILE`; required on first install of a cluster member unless `--secrets-from-etcd` is used |
| `--secrets-from-etcd` | false | Read both shared secrets from the etcd keys written by `secret generate`; excludes the two `--*-file` flags |
| `--amqp-port N` | auto (base `5672`) | Plaintext AMQP port; `0` = auto-assign |
| `--management-port N` | auto (base `15672`) | Management HTTP port; `0` = auto-assign |
| `--tls` | false | Add AMQPS and management HTTPS; plaintext listeners stay enabled |
| `--tls-cert FILE` | — | BYO certificate chain (PEM); must be paired with `--tls-key`; implies `--tls` |
| `--tls-key FILE` | — | BYO private key; must be paired with `--tls-cert` |
| `--tls-port N` | auto (base `5671`) | AMQPS port; `0` = auto-assign; requires `--tls` |
| `--config-option K=V` | — | Extra `rabbitmq.conf` setting; repeatable; replaces the stored override map |
| `--force` | false | Re-render config and recreate the container, keeping data and password; cluster/node/cookie are immutable |

### `pg addon rabbitmq status` / `shell` / `cluster ...`

| Argument/flag | Default | Description |
|---|---|---|
| `status --name NAME` | `rabbitmq` | Show state, endpoints, TLS, admin, EPMD/dist, paths; cluster name/node/etcd when clustered |
| `shell --name NAME` | `rabbitmq` | Open `/bin/bash` in the container |
| `cluster status --name NAME` | `rabbitmq` | Print `rabbitmqctl cluster_status` |
| `cluster forget NODE --name NAME [--yes]` | — | Forget a stopped, retired member; refuses the local node and asks for confirmation unless `--yes` |

### `pg addon rabbitmq secret generate`

| Flag | Default | Description |
|---|---|---|
| `--cluster NAME` | — | Cluster whose secrets are published (required) |
| `--etcd M1,M2` | — | Local etcd addon members holding the secrets |
| `--etcd-endpoints HOST:PORT,...` | — | External etcd endpoints holding the secrets; takes precedence over `--etcd` |
| `--cookie-file FILE` | `/tmp/rmq.cookie` | Local copy of the Erlang cookie (`0600`); used by cluster nodes only, clients do not need it |
| `--admin-password-file FILE` | `/tmp/rmq.admin` | Local copy of the administrator password (`0600`); an installation input, not a client credential |
| `--force` | false | Overwrite existing files and etcd keys |
| `--keep-cookie` | false | With `--force`: keep the existing Erlang cookie and rotate only the admin password (used after members are installed) |

### `pg addon rabbitmq queue ...`

Every queue subcommand accepts `--name NAME` (default `rabbitmq`) and
`--vhost VHOST` (default `/`).

| Command | Argument/flag | Default | Description |
|---|---|---|---|
| `list` | — | — | Show queues with message and consumer counts |
| `declare QUEUE` | `--durable` | true | Declare a durable queue (`false` is rejected by RabbitMQ 4.x) |
| | `--auto-delete` | false | Delete the queue when its last consumer disconnects |
| | `--type TYPE` | `classic` standalone, `quorum` cluster | `classic` or `quorum` |
| | `--message-ttl DURATION` | — | Set `x-message-ttl` (e.g. `30m`, `24h`, or milliseconds) |
| | `--expires DURATION` | — | Set `x-expires`: delete the queue after this much idle time (must be > 0) |
| | `--argument K=V` | — | Extra queue argument; repeatable; JSON values keep their type |
| `info QUEUE` | `--json` | false | Print the raw management API document |
| `publish QUEUE` | `--payload TEXT` | — | Message body (mutually exclusive with `--payload-file`) |
| | `--payload-file FILE` | — | Read the body from a file; `-` reads stdin |
| | `--exchange NAME` | `amq.default` | Target exchange |
| | `--routing-key KEY` | queue name | Routing key |
| | `--property K=V` | — | Message property; repeatable; JSON values keep their type |
| `get QUEUE` | `--count N` | 1 | Maximum messages to fetch |
| | `--consume` | false | Ack and remove the messages (default: peek and requeue) |
| | `--json` | false | Print the raw management API response |
| `bind QUEUE` | `--exchange NAME` | — | Source exchange (required; `amq.default` rejected) |
| | `--routing-key KEY` | queue name | Routing key |
| | `--argument K=V` | — | Binding argument; repeatable |
| `unbind QUEUE` | `--exchange NAME` | — | Source exchange (required) |
| | `--routing-key KEY` | queue name | Routing key |
| | `--argument K=V` | — | Remove only the exactly matching binding; omit to remove all matches |
| `bindings QUEUE` | `--json` | false | Print the raw management API document |
| `purge QUEUE` | `--yes` | false | Skip the confirmation prompt |
| `delete QUEUE` | `--yes` | false | Skip the confirmation prompt |
| | `--if-empty` | false | Fail instead of deleting a queue with messages |
| | `--if-unused` | false | Fail instead of deleting a queue with consumers |

### Lifecycle, logs, autostart

| Command | Flags | Description |
|---|---|---|
| `pg addon start rabbitmq` | `--name NAME` | Start (create if missing), wait for readiness, reconcile the admin account |
| `pg addon stop rabbitmq` | `--name NAME` | Stop the container, keep data |
| `pg addon remove rabbitmq` | `--name NAME`, `--clean-data` | Remove container and config entry; `--clean-data` also deletes the instance dir and a custom data dir |
| `pg logs addon rabbitmq` | `--name NAME`, `-n N` (default 50), `-f` | Container logs |
| `pg autostart enable\|disable` | `--rabbitmq`, `--name NAME` | Toggle boot start for one instance |
| `pg addon password rabbitmq` | `--name NAME` | Print the stored administrator password |

### Global flags

| Flag | Description |
|---|---|
| `-c, --config FILE` | Config file (default `~/.pgcli/pg.yaml`) |
| `-o, --output FILE` | Output config path (default the same file) |
| `-i, --instance NAME` | PostgreSQL instance selector — not used by RabbitMQ; use `--name` instead |
