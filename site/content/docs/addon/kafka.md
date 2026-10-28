---
title: "Kafka"
description: "Run a single-node Apache Kafka 4.x broker in KRaft mode (no ZooKeeper) as a managed addon, with topic management, rendered server.properties and host networking"
weight: 65
---

`pg addon install kafka` runs **Apache Kafka 4.4.0** as a managed container from the official `docker.io/apache/kafka` multi-arch image. Kafka 4.x is **KRaft-only**: the metadata controller is built into the broker process (`process.roles=broker,controller`, combined mode) and **ZooKeeper is not installed or used**.

Like ClickHouse, Kafka is a **top-level addon** — it does not front a PostgreSQL instance. pgcli renders `server.properties`, pins a KRaft cluster id, bind-mounts the data directory and runs the container on the host network. This release supports a **single combined node**; the multi-host cluster flags are reserved and rejected for now (see [Production notes](#production-notes)).

> **There is no authentication.** Kafka listens with PLAINTEXT by default, and `--listen` defaults to `127.0.0.1`. TLS is opt-in (`--tls`) and encrypts traffic, but it does **not** authenticate clients (`ssl.client.auth=none`), and the plaintext listener stays enabled for compatibility. Expose it only on a trusted private network or behind a firewall/proxy. Anyone who can reach either listener can read and write every topic.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `kafka` | Instance name (map key; also the default subcommand name) |
| `--version` | `4.4.0` | Upstream image tag, pulled directly from `docker.io/apache/kafka` |
| `--data-dir` | `<base_dir>/addon/kafka/<name>/data` | Data directory bind-mounted at `/var/lib/kafka/data`; must be an absolute path |
| `--listen` | `127.0.0.1` | Bind address for the PLAINTEXT, SSL (when `--tls`) and CONTROLLER listeners. `0.0.0.0` exposes all enabled listeners on the network |
| `--advertise-host` | `127.0.0.1` | Host clients are told to use. Must be a concrete IP (never `0.0.0.0`); in single-node mode it is also the controller voter address |
| `--broker-port` | auto (base `9092`) | Host broker port; `0` = auto-assign from `kafka_broker_start_port` |
| `--controller-port` | auto (base `19093`) | Host KRaft controller port; `0` = auto-assign from `kafka_controller_start_port` |
| `--tls` | false | Add an SSL listener next to the plaintext broker, using a certificate from pgcli's self-signed CA. The PLAINTEXT listener stays enabled |
| `--tls-pem` | — | Serve TLS with this combined PEM (certificate chain + private key) instead of the generated certificate. Implies `--tls`; the file is validated and copied to `config/tls/server.pem`. PKCS#8, PKCS#1 and SEC1 keys are accepted |
| `--tls-port` | auto (base `9093`) | Host SSL listener port; `0` = auto-assign from `kafka_tls_start_port`; requires `--tls` |
| `--cluster-id` | generated | Adopt an explicit 22-character KRaft cluster id instead of generating one (or adopting `meta.properties`); also used on reinstall after `remove` |
| `--config-option K=V` | — | Extra `server.properties` setting (`num.partitions=3`, `log.retention.hours=168`); repeatable; replaces the stored override map |
| `--force` | false | Re-render `server.properties` and recreate the container; keeps data, the stored cluster id and node id; required to add or remove TLS |

`--image` does not apply to Kafka — use `--version`. `--cluster`, `--peer` and `--node-id` are not implemented yet: passing any of them fails with an explicit "not implemented yet" error.

The controller default is **19093**, not 9093, because 9093 belongs to the Alertmanager pool (the TLS pool starts at 9093 but skips ports owned by other addons). pgcli renders the topology keys itself; `--config-option` can only add settings outside the managed set (roles, node id, listeners, advertised listeners, protocol map, controller listeners/voters, `log.dirs`, the SSL keystore/client-auth keys, internal-topic replication factors, `default.replication.factor` and `min.insync.replicas` are rejected).

## Install

```bash
pg addon install kafka --name k1
```

```
✓ Kafka "k1" installed
  Version:     4.4.0
  Image:       docker.io/apache/kafka:4.4.0
  Container:   pgcli-kafka-k1
  Broker:      127.0.0.1:9092
  Controller:  127.0.0.1:19093
  Node ID:     1
  Cluster ID:  <22-character id>
  Data dir:    /home/you/.pgcli/addon/kafka/k1/data
  Config:      /home/you/.pgcli/addon/kafka/k1/config/server.properties
```

A second instance takes the next free broker/controller ports from their pools (for example `9093`/`19094`, unless those ports are in use):

```bash
pg addon install kafka --name k2 --data-dir /srv/kafka-data --listen 0.0.0.0 --advertise-host 192.0.2.10
```

Re-running `install` without `--force` is a no-op that reports the current state. `--force` re-renders the config and recreates the container, keeping the data dir, the stored cluster id and node id. Changing `--cluster-id` on an instance whose data dir is already formatted is refused — the data belongs to the original cluster id.

The image is pulled directly from Docker Hub; no pgcli-built image is involved. `--version` accepts any safe upstream tag, but the tag must exist in the registry — the pull fails otherwise.

## Ports

| Endpoint | Default | Protocol | Pool |
|---|---|---|---|
| Broker | `9092` | Kafka client protocol (PLAINTEXT) | `kafka_broker_start_port`, default `9092` |
| Controller | `19093` | KRaft metadata quorum (PLAINTEXT) | `kafka_controller_start_port`, default `19093` |
| SSL (TLS) | `9093` (when `--tls`) | Kafka client protocol over TLS | `kafka_tls_start_port`, default `9093` |

All three pools share one "already assigned" set with each other and pre-mark explicit ports from the other streaming addons (Prometheus, ClickHouse, InfluxDB, influx2tsdb and the object stores), so neither side can steal the other's port. Explicit Alertmanager ports are also pre-marked by the Kafka allocator (and Kafka's ports by the Alertmanager pool), because both default near `9093`. Broker, controller and TLS ports must differ and must be unique across all Kafka instances. Ports already listening on the host are skipped during auto-assignment.

When `--listen` is a wildcard, pgcli's own topic/health commands dial `127.0.0.1`; the printed local controller endpoint follows the same rule. In single-node mode the static voter is `AdvertiseHost:ControllerPort`, so if you set a LAN `--advertise-host` you must also set `--listen` to `0.0.0.0` (or that same LAN address) or the controller cannot reach itself.

## TLS

Enable the SSL listener with `--tls`:

```bash
pg addon install kafka --name k1 --tls                 # SSL on the first free port from 9093
pg addon install kafka --name k1 --tls --tls-port 9093
```

`install` and `status` then print both endpoints and the CA path:

```
  Broker:      127.0.0.1:9092
  TLS:         127.0.0.1:9093 (CA .../addon/kafka/k1/config/tls/ca.crt)
  Controller:  127.0.0.1:19093
```

The plaintext listener stays enabled, so existing clients keep working. TLS only takes effect on creation or with `--force`; `--tls-port` without `--tls` is rejected. Disable it again with `--tls=false --force` — the generated files stay under `config/tls/` and are reused if you re-enable TLS.

The certificate is issued by pgcli's self-signed CA (the same helper MinIO/silo/rustfs use) for `localhost`, every host NIC address and `--advertise-host`. Clients must trust `config/tls/ca.crt`.

### Bring your own certificate (`pg cert`)

Kafka's PEM keystore reads **one** file containing the certificate chain followed by the private key. `pg cert` writes the two parts separately, so merge them before installing:

```bash
# 1. Issue the server certificate. Include every address clients dial,
#    especially --advertise-host.
pg cert --host 127.0.0.1,10.241.20.147 \
  --cert-file kafka.crt --key-file kafka.key

# 2. Merge certificate (chain) + private key into one PEM.
cat kafka.crt kafka.key > kafka-server.pem

# 3. Install with the merged file — --tls is implied.
pg addon install kafka --name k1 --tls-pem ./kafka-server.pem
```

With an intermediate chain, put the leaf first: `cat leaf.crt intermediate.crt key.key > kafka-server.pem`.

At install time pgcli checks that the file contains a certificate and exactly one unencrypted key, that the key matches the certificate, that the certificate is currently valid, allows server authentication and is not a CA certificate. It also warns when `--advertise-host` is missing from the SANs — such a broker starts, but clients that verify the advertised hostname will fail. PKCS#8 (`BEGIN PRIVATE KEY`, what `pg cert` writes), PKCS#1 (`BEGIN RSA PRIVATE KEY`) and SEC1 (`BEGIN EC PRIVATE KEY`) keys are accepted; encrypted keys are rejected.

pgcli copies the merged file to `config/tls/server.pem` (mode `0600`, owned by uid 1000) and does **not** generate `ca.crt` or `public.crt`/`private.key`. The source file is left untouched. Automatic renewal does not apply: replace the source PEM and re-run the install with `--force` to put the new certificate into a fresh container.

For a self-signed `pg cert` certificate, the certificate file itself is what clients trust — use `kafka.crt` wherever the examples below say `ca.crt`. If the certificate is signed by a public or private CA, point clients at that CA chain instead, and include any intermediate certificates in `kafka-server.pem`.

To switch an instance back to pgcli's generated certificate, reinstall with `--tls --tls-pem '' --force`; an empty `--tls-pem` clears the stored path.

Java client (`client.properties`):

```properties
security.protocol=SSL
ssl.truststore.type=PEM
ssl.truststore.location=/path/to/addon/kafka/k1/config/tls/ca.crt
```

librdkafka-based clients:

```properties
security.protocol=SSL
ssl.ca.location=/path/to/addon/kafka/k1/config/tls/ca.crt
```

Verify the listener with OpenSSL:

```bash
openssl s_client -connect 127.0.0.1:9093 \
  -CAfile <base_dir>/addon/kafka/k1/config/tls/ca.crt -verify_return_error </dev/null
```

Produce and consume over TLS from inside the container (the TLS dir is mounted read-only with the config dir):

```bash
pg addon kafka shell --name k1 -- bash -lc \
  'printf "security.protocol=SSL\nssl.truststore.type=PEM\nssl.truststore.location=/mnt/shared/config/tls/ca.crt\n" > /tmp/client.properties
   printf "hello\n" | /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server 127.0.0.1:9093 --producer.config /tmp/client.properties --topic demo'

pg addon kafka shell --name k1 -- bash -lc \
  'printf "security.protocol=SSL\nssl.truststore.type=PEM\nssl.truststore.location=/mnt/shared/config/tls/ca.crt\n" > /tmp/client.properties
   /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server 127.0.0.1:9093 --consumer.config /tmp/client.properties --topic demo --from-beginning --max-messages 1'
```

- TLS encrypts the connection only: `ssl.client.auth=none`, so clients present no certificate and anyone who can reach the SSL port can still connect. The listen address remains the only security boundary.
- Connect by IP (or a name present in the certificate). A DNS name that is not a host NIC address is not in the SAN list and fails verification.
- The leaf certificate is regenerated when it is within 30 days of expiry (on install, start or `--force`). The CA is kept, so clients do not need a new trust file. This applies only to pgcli-generated certificates; with `--tls-pem` you own renewal.

## What pgcli creates

Everything for one instance lives under `<base_dir>/addon/kafka/<name>/`:

| Host path | Container path | Notes |
|---|---|---|
| `config/server.properties` | `/mnt/shared/config` (directory, read-only) | Generated topology; the image entrypoint merges it into the runtime config |
| `config/tls/` | `/mnt/shared/config/tls` (read-only) | Only with `--tls`: `server.pem` (cert chain + key, 0600, owned by uid 1000), plus `ca.crt` (0644), `public.crt` and `private.key` when pgcli generated the certificate. With `--tls-pem`, only `server.pem` is written — a normalized copy of your combined PEM |
| `data/` | `/var/lib/kafka/data` | Log segments, `meta.properties` and KRaft metadata |

The generated `server.properties` sets:

```properties
process.roles=broker,controller
node.id=1
listeners=PLAINTEXT://127.0.0.1:9092,CONTROLLER://127.0.0.1:19093
advertised.listeners=PLAINTEXT://127.0.0.1:9092
inter.broker.listener.name=PLAINTEXT
controller.listener.names=CONTROLLER
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
controller.quorum.voters=1@127.0.0.1:19093
log.dirs=/var/lib/kafka/data
```

With `--tls` the listener lines and security section become:

```properties
listeners=PLAINTEXT://127.0.0.1:9092,SSL://127.0.0.1:9093,CONTROLLER://127.0.0.1:19093
advertised.listeners=PLAINTEXT://127.0.0.1:9092,SSL://127.0.0.1:9093
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,SSL:SSL
ssl.keystore.type=PEM
ssl.keystore.location=/mnt/shared/config/tls/server.pem
ssl.client.auth=none
```

plus replication settings for the internal topics and auto-created topics: with one voter, `offsets.topic.replication.factor=1`, `transaction.state.log.replication.factor=1`, `transaction.state.log.min.isr=1`, the share-coordinator equivalents, `default.replication.factor=1` and `min.insync.replicas=1`. Overrides are appended after the managed block in sorted key order.

The container runs with `--network host`, `--restart unless-stopped` and a 30-second stop timeout, and is started with `-e CLUSTER_ID=<id>` so the image formats the data dir with exactly the stored id (the image would otherwise inject its own fixed default).

The official image runs as **uid/gid 1000 (`appuser`)**. Before the first boot pgcli hands the data directory to that id with a one-time `podman unshare chown -R 1000:1000`. Once `meta.properties` exists the storage is already owned correctly, so later starts and `--force` re-creations do not recurse over the log tree. With `--tls`, only the combined `config/tls/server.pem` is chowned to uid/gid 1000 (the PEM keystore is read as `appuser`); the config dir, CA and raw private key stay host-owned, and the directory remains traversable for the read-only mount.

## Topics

```bash
pg addon kafka topic create demo --name k1
pg addon kafka topic create events --partitions 3 --replication-factor 1 --name k1
pg addon kafka topic create logs --config retention.ms=604800000 --name k1
pg addon kafka topic list --name k1
pg addon kafka topic list --exclude-internal --name k1
pg addon kafka topic describe demo --name k1
pg addon kafka topic delete demo --name k1 --yes
```

- `--partitions` defaults to the broker's `num.partitions` (upstream default `1`). `--replication-factor 0` (the default) means "the instance's RF" — `1` on a single node.
- `--config KEY=VALUE` is repeatable and passes topic-level settings through to `kafka-topics.sh`.
- `delete` asks for confirmation unless `--yes` is given; deleting a topic deletes all of its messages.
- The instance must be running for every topic command.

Produce and consume with the Kafka scripts inside the container (the image keeps
them in `/opt/kafka/bin`, which is not on `PATH`):

```bash
pg addon kafka shell --name k1 -- bash -lc \
  'printf "hello\nworld\n" | /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server 127.0.0.1:9092 --topic demo'

pg addon kafka shell --name k1 -- bash -lc \
  '/opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server 127.0.0.1:9092 --topic demo --from-beginning --max-messages 2'
```

`--bootstrap-server` is what external clients use with the printed broker endpoint. For a host-side client against the default loopback listener, `127.0.0.1:9092` is enough.

## Status and quorum

```bash
pg addon kafka status --name k1          # container state, endpoints, ids, paths
pg addon kafka cluster status --name k1  # KRaft metadata quorum leader and voters
pg logs addon kafka --name k1 -f         # broker logs (stdout/stderr)
```

`cluster status` runs `kafka-metadata-quorum.sh --bootstrap-controller <local controller> describe --status`; on a healthy single node it prints the cluster id, a leader and one voter.

`install` and `start` wait up to 90 seconds for `kafka-broker-api-versions.sh` to answer on the local broker endpoint. A broker that never becomes ready fails the command and prints the last log lines; data is preserved.

## Configuration

Extra `server.properties` entries can be set at install time:

```bash
pg addon install kafka --name k1 \
  --config-option num.partitions=3 \
  --config-option log.retention.hours=168
```

`--config-option` **replaces** the stored override map (it does not merge). The overrides are persisted in `pg.yaml` under `addons.kafka.<name>.settings` and re-rendered on every start, so they survive restarts and container recreation. There is no separate `config set` command in this release; change an override by re-installing with `--force` and the complete map you want.

Keys may contain letters, digits, dots, underscores and hyphens. Values must not contain NUL, CR or LF. pgcli-managed keys are rejected rather than silently overridden.

## Lifecycle

```bash
pg addon start kafka --name k1            # start, create if missing, wait for readiness
pg addon stop kafka --name k1
pg addon remove kafka --name k1           # remove container + pg.yaml entry, keep data/config
pg addon remove kafka --name k1 --clean-data   # also delete the instance dir and a custom data dir
```

Without `--clean-data`, `remove` leaves `<instance dir>` (config + data) and a custom `--data-dir` on disk. Reinstalling under the same name **adopts the formatted storage**: the `cluster.id` and `node.id` in `meta.properties` are reused automatically instead of generating a new cluster. This is why a reinstall can revive the existing log.

`--clean-data` deletes the instance directory and a distinct custom data dir (files owned by uid 1000 are removed through `podman unshare`), but never removes the pulled image. Pointing an instance at a data dir that belongs to a different cluster id or node id is refused before the container is created.

Auto-start after reboot:

```bash
pg autostart enable --kafka --name k1
pg autostart status
pg autostart disable --kafka --name k1
```

Boot start re-renders `server.properties` first and is idempotent (`pg start` also brings up autostart-enabled Kafka instances).

## Production notes

- **Single node only in this release.** There is no replication and no HA: a stopped broker means no reads or writes. `--cluster`/`--peer`/`--node-id` are reserved for a future multi-host topology and currently return an explicit error.
- **KRaft combined mode.** Broker and controller share one process. This is fine for development and small deployments; the upstream project recommends isolated controller/broker roles for larger critical clusters, which this addon does not provide.
- **No authentication; TLS is transport-only.** The plaintext listener is always enabled; `--tls` adds an encrypted listener but `ssl.client.auth=none` means clients are not authenticated. Anyone who can reach either port can read and write every topic. Keep the default loopback bind, or expose it only on a trusted private network. Put real authentication (SASL/mTLS) in front of Kafka with your own proxy/gateway if needed.
- **No ZooKeeper.** Kafka 4.x is KRaft-only; there is no ZooKeeper container to manage, and `zookeeper.connect` is rejected as a managed key.
- **Copies are not backups.** Topic replication (not available on a single node) protects availability, not against accidental deletion or corruption. Stop the instance and copy the data dir for an offline backup; there is no backup/restore command in this release.
- **Upgrades.** `--force --version <new>` pulls a new tag and recreates the container on the same data dir. Kafka does not guarantee downgrading data written by a newer version — take an offline copy before upgrading. `--version` must be a real upstream tag.
- **Data ownership.** Data files are owned by uid/gid 1000. Use `--clean-data` instead of deleting the directory by hand; pgcli handles the permission fallback.
- **Ports.** Broker defaults to `9092`; the controller defaults to `19093` to avoid the Alertmanager default `9093`. Both are ordinary host ports under host networking.
- **Platform.** Linux (host networking); the official image is multi-arch (amd64/arm64).

## CLI flag reference

### `pg addon install kafka`

| Flag | Default | Description |
|---|---|---|
| `--name NAME` | `kafka` | Instance name; `[A-Za-z0-9][A-Za-z0-9_-]*`, max 48 chars |
| `--version TAG` | `4.4.0` | Upstream `docker.io/apache/kafka` tag |
| `--data-dir DIR` | `<base_dir>/addon/kafka/<name>/data` | Absolute host data dir bind-mounted at `/var/lib/kafka/data` |
| `--listen ADDR` | `127.0.0.1` | Bind address for the broker, SSL (when enabled) and controller listeners |
| `--advertise-host IP` | `127.0.0.1` | Advertised broker host (and single-node controller voter host); never a wildcard |
| `--broker-port N` | auto (base `9092`) | Broker host port; `0` = auto-assign |
| `--controller-port N` | auto (base `19093`) | Controller host port; `0` = auto-assign; must differ from the broker port |
| `--tls` | false | Add an SSL listener; the plaintext listener stays enabled |
| `--tls-pem FILE` | — | Combined PEM (chain + key) for the SSL listener; implies `--tls`; relative paths are resolved to absolute |
| `--tls-port N` | auto (base `9093`) | SSL listener host port; `0` = auto-assign; requires `--tls` |
| `--cluster-id ID` | generated / adopted | Explicit 22-character base64url KRaft cluster id |
| `--config-option K=V` | — | Extra `server.properties` setting; repeatable; replaces the stored override map |
| `--force` | false | Re-render config and recreate the container, keeping data and ids; required to add or remove `--tls` |

### `pg addon kafka status` / `shell`

| Argument/flag | Default | Description |
|---|---|---|
| `--name NAME` | `kafka` | Instance to operate on |
| `-- COMMAND...` | `/bin/bash` | `shell` only: run a command instead of an interactive shell |

### `pg addon kafka topic create|list|describe|delete`

| Argument/flag | Default | Description |
|---|---|---|
| `NAME` | — | Topic name (`create`/`describe`/`delete`, positional) |
| `--partitions N` | broker default (`1`) | `create` only: partition count |
| `--replication-factor N` | `0` = instance RF (`1`) | `create` only: replication factor |
| `--config K=V` | — | `create` only: topic config; repeatable |
| `--exclude-internal` | false | `list` only: hide Kafka's internal topics |
| `--yes` | false | `delete` only: skip the confirmation prompt |

### `pg addon kafka cluster status`

| Argument/flag | Default | Description |
|---|---|---|
| `--name NAME` | `kafka` | Instance whose local controller is queried |

### Lifecycle, logs, autostart

| Command | Flags | Description |
|---|---|---|
| `pg addon start kafka` | `--name NAME` | Start (create if missing), wait for readiness |
| `pg addon stop kafka` | `--name NAME` | Stop the container, keep data |
| `pg addon remove kafka` | `--name NAME`, `--clean-data` | Remove container and config entry; `--clean-data` also deletes the instance dir and a custom data dir |
| `pg logs addon kafka` | `--name NAME`, `-n N` (default 50), `-f` | Container logs |
| `pg autostart enable\|disable` | `--kafka`, `--name NAME` | Toggle boot start for one instance |

### Global flags

| Flag | Description |
|---|---|
| `-c, --config FILE` | Config file (default `~/.pgcli/pg.yaml`) |
| `-o, --output FILE` | Output config path (default the same file) |
| `-i, --instance NAME` | PostgreSQL instance selector — not used by Kafka; use `--name` instead |
