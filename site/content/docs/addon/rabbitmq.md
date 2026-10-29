---
title: "RabbitMQ"
description: "Run a single-node RabbitMQ broker as a managed addon — AMQP plus the management web UI/API, optional TLS (generated CA or bring your own certificate), and queue management (list/declare/inspect, publish/get, bind/unbind, purge/delete)"
weight: 66
---

`pg addon install rabbitmq` runs **RabbitMQ 4.4.0** as a managed container from the official `docker.io/library/rabbitmq` image (the `-management` variant, which bundles the web UI and HTTP API). Like Kafka and ClickHouse, RabbitMQ is a **top-level addon**: pgcli renders `rabbitmq.conf`, owns the administrator account, bind-mounts the data directory and runs the container on the host network.

The addon is deliberately **single-node**. It does not configure clustering, federation, shovel, MQTT or STOMP; advanced administration (vhosts, users, permissions, policies and exchanges) is done through the management UI or `pg addon rabbitmq shell` + `rabbitmqctl`. Common queue operations are wrapped by `pg addon rabbitmq queue` (see [Queues and messages](#queues-and-messages)).

> **Authentication is always on.** AMQP and the management UI require the administrator account pgcli creates (default `admin`, generated 20-character password). `--listen` defaults to `127.0.0.1`. When you expose it on a network, treat the password as the security boundary: TLS encrypts traffic but does **not** authenticate clients (`verify_none`, `fail_if_no_peer_cert = false`).

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `rabbitmq` | Instance name (map key; also the default subcommand name) |
| `--version` | `4.4.0` | Upstream image tag, pulled directly as `docker.io/library/rabbitmq:<version>-management` |
| `--admin-user` | `admin` | Administrator account created on first boot and reconciled on every start |
| `--admin-password` | generated (20 chars) | Administrator password; stored in `pg.yaml` and in a `0600` secret file |
| `--data-dir` | `<base_dir>/addon/rabbitmq/<name>/data` | Data directory bind-mounted at `/var/lib/rabbitmq`; must be an absolute path |
| `--listen` | `127.0.0.1` | Bind address shared by AMQP, AMQPS (when `--tls`) and the management listeners. `0.0.0.0` exposes all enabled listeners |
| `--amqp-port` | auto (base `5672`) | Plaintext AMQP port; `0` = auto-assign from `rabbitmq_start_port` |
| `--management-port` | auto (base `15672`) | Management HTTP UI/API port; `0` = auto-assign from `rabbitmq_management_start_port` |
| `--tls` | false | Add AMQPS and management HTTPS next to the plaintext listeners, using a certificate from pgcli's self-signed CA |
| `--tls-cert FILE` | — | Bring-your-own server certificate (PEM chain). Must be passed together with `--tls-key`; implies `--tls`. Must cover `--listen` unless the listen address is `0.0.0.0` |
| `--tls-key FILE` | — | Bring-your-own private key (PKCS#8/PKCS#1/SEC1), paired with `--tls-cert` |
| `--tls-port` | auto (base `5671`) | AMQPS port; `0` = auto-assign from `rabbitmq_tls_start_port`; requires `--tls` |
| `--config-option K=V` | — | Extra `rabbitmq.conf` setting (`heartbeat=60`, `log.file.level=debug`); repeatable; **replaces** the stored override map |
| `--force` | false | Re-render the config and recreate the container, keeping data and the stored password |

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

Open `http://<listen>:<management-port>` and sign in with the admin account. The same endpoint serves the HTTP API:

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

EPMD and distribution are infrastructure ports, not client endpoints. Every instance runs with `RABBITMQ_NODENAME=rabbit@localhost`, `ERL_EPMD_ADDRESS=127.0.0.1` and its own EPMD/distribution ports, and `rabbitmq.conf` pins `distribution.listener.interface = 127.0.0.1` — Erlang distribution is never exposed on the network, and multiple instances coexist on one host without colliding with a host Erlang installation.

The `rabbitmq_prometheus` plugin is **not enabled**. pgcli mounts its own `enabled_plugins` file containing only `rabbitmq_management`, so every instance behaves the same and none opens a second unauthenticated metrics listener.

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
- `declare` creates a **durable classic** queue by default. RabbitMQ 4.x removed
  transient (non-durable, non-exclusive) queues, so `--durable=false` is
  rejected before the request is sent. `--type quorum` declares a quorum queue
  (it must be durable and cannot be auto-delete). Repeatable
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

`install`, `start` and `--force` wait up to **90 seconds** for `rabbitmq-diagnostics -q ping` to answer. After readiness pgcli reconciles the administrator account with `rabbitmqctl change_password` / `add_user` / `set_user_tags administrator` / `set_permissions`.

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
| `config/enabled_plugins` | `/etc/rabbitmq/enabled_plugins` (read-only) | Always `[rabbitmq_management].` |
| `config/tls/` | `/etc/rabbitmq/tls` (read-only, only with `--tls`) | `ca.crt` (0644), `public.crt` (chain, 0644), `private.key` (0600, chowned to uid 999); `ca.key` stays host-side and is not mounted |
| `data/` | `/var/lib/rabbitmq` | Mnesia, message store, definitions and logs |
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

With `--tls` the file additionally carries `listeners.ssl.default`, the `ssl_options.*` block and the `management.ssl.*` block. The administrator password is **not** written to the file: the container receives it through `RABBITMQ_DEFAULT_USER` / `RABBITMQ_DEFAULT_PASS`, and pgcli reconciles it with `rabbitmqctl` on every start.

The container runs with `--network host`, `--restart unless-stopped` and a 30-second stop timeout. It runs as the image's **uid/gid 999 (`rabbitmq`)**; pgcli hands only the TLS private key to that uid (through `podman unshare` on rootless, `os.Chown` when running as root). The data directory is chowned by the image entrypoint itself.

## Configuration

Extra `rabbitmq.conf` entries can be set at install time:

```bash
pg addon install rabbitmq --name rmq --force \
  --config-option heartbeat=60 \
  --config-option log.file.level=debug
```

`--config-option` **replaces** the stored override map (it does not merge). Overrides are persisted in `pg.yaml` under `addons.rabbitmq.<name>.settings` and re-rendered on every start. Keys may contain letters, digits, dots, underscores and hyphens; values must not contain NUL, CR or LF.

pgcli-managed keys are rejected rather than silently overridden: `listeners.*`, `ssl_options.*`, `management.*`, `prometheus.*`, `default_user`, `default_pass`, `default_user_tags.*`, `default_permissions.*`, `loopback_users.*` and `distribution.listener.*`.

## Password and account semantics

- `RABBITMQ_DEFAULT_USER` / `RABBITMQ_DEFAULT_PASS` only **seed an empty Mnesia database** on first boot. That is why pgcli reconciles the account on every start with `rabbitmqctl change_password` (falling back to `add_user` when the user does not exist), then sets the `administrator` tag and full `.* / .* / .*` permissions on vhost `/`.
- Changing `--admin-user` on an existing instance creates/reconciles the new account but **does not delete the previous one**. Remove stale accounts in the management UI or with `rabbitmqctl delete_user`.
- Changing `--admin-password` (or reviving a stored secret) is applied on the next `install --force` or `start`.
- Passwords are stored both in `pg.yaml` and in the `0600` secret file; `pg addon password rabbitmq --name NAME` prints the stored value.

## Production notes

- **Single node only.** There is no clustering, mirrored/classic HA queue replication, federation, shovel, MQTT or STOMP in this addon. One stopped container means the broker is down.
- **No Prometheus plugin.** `enabled_plugins` is pinned to management only. Scrape metrics through the management API or enable a plugin manually in the container if you really need it — but note that every instance would then want the same default metrics port.
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
| `--admin-password PASS` | generated | Administrator password (no NUL/CR/LF) |
| `--data-dir DIR` | `<base_dir>/addon/rabbitmq/<name>/data` | Absolute host data dir bind-mounted at `/var/lib/rabbitmq` |
| `--listen ADDR` | `127.0.0.1` | Bind address for AMQP, AMQPS, management HTTP and management HTTPS |
| `--amqp-port N` | auto (base `5672`) | Plaintext AMQP port; `0` = auto-assign |
| `--management-port N` | auto (base `15672`) | Management HTTP port; `0` = auto-assign |
| `--tls` | false | Add AMQPS and management HTTPS; plaintext listeners stay enabled |
| `--tls-cert FILE` | — | BYO certificate chain (PEM); must be paired with `--tls-key`; implies `--tls` |
| `--tls-key FILE` | — | BYO private key; must be paired with `--tls-cert` |
| `--tls-port N` | auto (base `5671`) | AMQPS port; `0` = auto-assign; requires `--tls` |
| `--config-option K=V` | — | Extra `rabbitmq.conf` setting; repeatable; replaces the stored override map |
| `--force` | false | Re-render config and recreate the container, keeping data and password |

### `pg addon rabbitmq status` / `shell`

| Argument/flag | Default | Description |
|---|---|---|
| `--name NAME` | `rabbitmq` | Instance to operate on |

### `pg addon rabbitmq queue ...`

Every queue subcommand accepts `--name NAME` (default `rabbitmq`) and
`--vhost VHOST` (default `/`).

| Command | Argument/flag | Default | Description |
|---|---|---|---|
| `list` | — | — | Show queues with message and consumer counts |
| `declare QUEUE` | `--durable` | true | Declare a durable queue (`false` is rejected by RabbitMQ 4.x) |
| | `--auto-delete` | false | Delete the queue when its last consumer disconnects |
| | `--type TYPE` | `classic` | `classic` or `quorum` |
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
