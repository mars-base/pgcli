---
title: "Kafka"
description: "Run Apache Kafka 4.x brokers in KRaft mode (no ZooKeeper) as a managed addon — single-node or a multi-host static controller quorum, with topic management, rendered server.properties and host networking"
weight: 65
---

`pg addon install kafka` runs **Apache Kafka 4.4.0** as a managed container from the official `docker.io/apache/kafka` multi-arch image. Kafka 4.x is **KRaft-only**: the metadata controller is built into the broker process (`process.roles=broker,controller`, combined mode) and **ZooKeeper is not installed or used**.

Like ClickHouse, Kafka is a **top-level addon** — it does not front a PostgreSQL instance. pgcli renders `server.properties`, pins a KRaft cluster id, bind-mounts the data directory and runs the container on the host network. A single command installs a **single combined node**; adding `--cluster`, `--node-id` and `--peer` installs one node of a **multi-host static controller quorum**, with every host running its own `pg addon install kafka` (pgcli never SSHes). See [Multi-host cluster](#multi-host-cluster).

> **There is no authentication.** Kafka listens with PLAINTEXT by default, and `--listen` defaults to `127.0.0.1`. TLS is opt-in (`--tls`) and encrypts traffic, but it does **not** authenticate clients (`ssl.client.auth=none`), and the plaintext listener stays enabled for compatibility. Expose it only on a trusted private network or behind a firewall/proxy. Anyone who can reach either listener can read and write every topic.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `kafka` | Instance name (map key; also the default subcommand name) |
| `--version` | `4.4.0` | Upstream image tag, pulled directly from `docker.io/apache/kafka` |
| `--data-dir` | `<base_dir>/addon/kafka/<name>/data` | Data directory bind-mounted at `/var/lib/kafka/data`; must be an absolute path |
| `--listen` | `127.0.0.1` | Bind address for the PLAINTEXT, SSL (when `--tls`) and CONTROLLER listeners. `0.0.0.0` exposes all enabled listeners on the network; in cluster mode it must be `0.0.0.0` or the advertised address |
| `--advertise-host` | `127.0.0.1` | Host clients are told to use. Must be a concrete IP (never `0.0.0.0`); in single-node mode it is also the controller voter address. In cluster mode it is this node's controller voter address too, so it must be a concrete non-loopback IP and is required on a fresh cluster install |
| `--broker-port` | auto (base `9092`) | Host broker port; `0` = auto-assign from `kafka_broker_start_port` |
| `--controller-port` | auto (base `19093`) | Host KRaft controller port; `0` = auto-assign from `kafka_controller_start_port`. Cluster installs must pass it explicitly: every peer embeds it in its voter list |
| `--cluster NAME` | — | Join a multi-host cluster with this name. Every host must use the same name; the KRaft cluster id is derived from it deterministically |
| `--node-id N` | `1` single-node | This node's 1-based KRaft node id. Required on a fresh cluster install, unique within the cluster; rejected in single-node mode |
| `--peer ID@HOST:PORT` | — | Another controller voter. Repeat once per **other** node (this node is folded in automatically); pass the full peer list on every host. The host must be an IP and the port must match that node's `--controller-port` |
| `--tls` | false | Add an SSL listener next to the plaintext broker, using a certificate from pgcli's self-signed CA. The PLAINTEXT listener stays enabled |
| `--tls-pem` | — | Serve TLS with this combined PEM (certificate chain + private key) instead of the generated certificate. Implies `--tls`; the file is validated and copied to `config/tls/server.pem`. PKCS#8, PKCS#1 and SEC1 keys are accepted. In cluster mode the certificate must cover this node's `--advertise-host` |
| `--tls-port` | auto (base `9093`) | Host SSL listener port; `0` = auto-assign from `kafka_tls_start_port`; requires `--tls` |
| `--cluster-id` | derived (cluster) / generated | In cluster mode the id is derived from `--cluster` and an explicit value must equal it; in single-node mode it adopts an explicit 22-character KRaft cluster id, else adopts `meta.properties` or generates one |
| `--config-option K=V` | — | Extra `server.properties` setting (`num.partitions=3`, `log.retention.hours=168`); repeatable; replaces the stored override map |
| `--force` | false | Re-render `server.properties` and recreate the container; keeps data, the stored cluster id and node id; required to add or remove TLS. Cluster identity fields (cluster, node id, cluster id, peers, advertise host, controller port) cannot be changed even with `--force` |

`--image` does not apply to Kafka — use `--version`.

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

## Multi-host cluster

A cluster is a **static KRaft quorum**: every node carries the complete `controller.quorum.voters` list, and membership cannot be changed online. Install one node per host with the same `--cluster` name, a unique `--node-id`, this host's `--advertise-host`, an explicit `--controller-port`, and a `--peer` entry for every **other** node. Run the command on each host yourself; pgcli does not fan out over SSH.

For three hosts `192.0.2.11`, `192.0.2.12` and `192.0.2.13`:

```bash
# node 1 on 192.0.2.11
pg addon install kafka --name prod --cluster prod --node-id 1 \
  --listen 0.0.0.0 --advertise-host 192.0.2.11 \
  --broker-port 9092 --controller-port 19093 \
  --peer 2@192.0.2.12:19093 --peer 3@192.0.2.13:19093

# node 2 on 192.0.2.12
pg addon install kafka --name prod --cluster prod --node-id 2 \
  --listen 0.0.0.0 --advertise-host 192.0.2.12 \
  --broker-port 9092 --controller-port 19093 \
  --peer 1@192.0.2.11:19093 --peer 3@192.0.2.13:19093

# node 3 on 192.0.2.13
pg addon install kafka --name prod --cluster prod --node-id 3 \
  --listen 0.0.0.0 --advertise-host 192.0.2.13 \
  --broker-port 9092 --controller-port 19093 \
  --peer 1@192.0.2.11:19093 --peer 2@192.0.2.12:19093
```

How the identity is assembled:

- **Cluster ID.** Derived from `--cluster` with a deterministic hash, so every host computes the same value without passing `--cluster-id`. If you pass `--cluster-id` it must equal the derived value.
- **Voters.** `ID@HOST:PORT` entries are sorted by node id; this node's entry comes from `--node-id`, `--advertise-host` and `--controller-port`, and peers supply the rest. Two voters may not share a controller endpoint.
- **Controller port.** It is part of every peer's voter list, so a fresh cluster install requires an explicit `--controller-port` (default and recommended value: `19093`). Auto-assignment could pick a different port on another host.
- **Ports and addresses.** All nodes may use the same broker/controller ports because they are different hosts. `--listen` must be `0.0.0.0` (or this node's advertised address); loopback is rejected.

The first two installs print a warning that the controller quorum is not ready yet — that is expected until the last voter is installed. The readiness wait is 15 seconds in cluster mode instead of 90. Once all nodes are up:

```bash
pg addon kafka cluster status --name prod    # leader + 3 voters
pg addon kafka status --name prod            # cluster, node x of N, voter, voters, RF/minISR
pg addon list                                # shows "cluster prod" and the voter list
```

Topic defaults follow the quorum size: `--replication-factor 0` means `min(3, voters)` (3 for three nodes), with `min.insync.replicas = min(2, RF)`. A **two-voter cluster is allowed but has zero fault tolerance** — losing either node loses the metadata quorum — so pgcli prints a warning. Use three or another odd number for HA.

Cluster identity is immutable after install, even with `--force`: `--cluster`, `--node-id`, `--cluster-id`, the `--peer` set, `--advertise-host` and `--controller-port` are all rejected if they change. Ports, TLS, version, `--data-dir` and `--config-option` remain mutable with `--force`. To change the topology, remove the node (on that host) and reinstall; there is no online controller add/remove in this addon.

## Ports

| Endpoint | Default | Protocol | Pool |
|---|---|---|---|
| Broker | `9092` | Kafka client protocol (PLAINTEXT) | `kafka_broker_start_port`, default `9092` |
| Controller | `19093` | KRaft metadata quorum (PLAINTEXT) | `kafka_controller_start_port`, default `19093` |
| SSL (TLS) | `9093` (when `--tls`) | Kafka client protocol over TLS | `kafka_tls_start_port`, default `9093` |

All three pools share one "already assigned" set with each other and pre-mark explicit ports from the other streaming addons (Prometheus, ClickHouse, InfluxDB, influx2tsdb and the object stores), so neither side can steal the other's port. Explicit Alertmanager ports are also pre-marked by the Kafka allocator (and Kafka's ports by the Alertmanager pool), because both default near `9093`. Broker, controller and TLS ports must differ and must be unique across all Kafka instances. Ports already listening on the host are skipped during auto-assignment.

When `--listen` is a wildcard, pgcli's own topic/health commands dial `127.0.0.1`; the printed local controller endpoint follows the same rule. In single-node mode the static voter is `AdvertiseHost:ControllerPort`, so if you set a LAN `--advertise-host` you must also set `--listen` to `0.0.0.0` (or that same LAN address) or the controller cannot reach itself.

In cluster mode the controller port is not just a local bind — it is the address every other host writes into its `controller.quorum.voters`. Use the same `--controller-port` on every node (unless you deliberately give each node a different one, in which case every `--peer` entry must carry the correct port for that node) and make sure the port is free on all hosts before you start. A mismatch means the voter lists differ and the quorum never forms. The broker port is advertised per node, so it may differ between hosts, but the same `9092` everywhere keeps client bootstrap lists simple.

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

### Cluster TLS

Every node issues its **own** CA and leaf, so a client that may connect to any broker needs all of them in one trust file:

```bash
# On each host, copy out that node's CA, then concatenate them on the client.
for v in vm01 vm02 vm03; do
  ssh $v 'cat ~/.pgcli/addon/kafka/prod/config/tls/ca.crt'
done > kafka-ca-bundle.pem
```

The SSL listener is per node: enable `--tls` on **all** nodes for a uniform SSL bootstrap list. If only some nodes have TLS, those nodes advertise an `SSL://...` endpoint while the others advertise only `PLAINTEXT://...`; clients using `security.protocol=SSL` can still reach the TLS nodes, but the plaintext-only nodes remain plaintext. Enable TLS one node at a time (`--tls --force`) so the controller quorum keeps two of three voters.

`--tls` only adds a client-facing SSL listener. The **CONTROLLER** quorum and inter-broker traffic stay on `PLAINTEXT`, so enabling or disabling TLS never breaks quorum.

### Bring your own certificate (`pg cert`)

Kafka's PEM keystore reads **one** file containing the certificate chain followed by the private key. `pg cert` writes the two parts separately, so merge them before installing:

```bash
# 1. Issue the server certificate. Include every address clients dial,
#    especially --advertise-host.
pg cert --host 127.0.0.1,192.0.2.11 \
  --cert-file kafka.crt --key-file kafka.key

# 2. Merge certificate (chain) + private key into one PEM.
cat kafka.crt kafka.key > kafka-server.pem

# 3. Install with the merged file — --tls is implied.
pg addon install kafka --name k1 --tls-pem ./kafka-server.pem
```

With an intermediate chain, put the leaf first: `cat leaf.crt intermediate.crt key.key > kafka-server.pem`.

At install time pgcli checks that the file contains a certificate and exactly one unencrypted key, that the key matches the certificate, that the certificate is currently valid, allows server authentication and is not a CA certificate. It also checks the SANs against `--advertise-host`: in cluster mode a certificate that does not cover this node's advertised IP is a **hard error** (the other voters and clients reach this node by that address); in single-node mode it is only a warning. PKCS#8 (`BEGIN PRIVATE KEY`, what `pg cert` writes), PKCS#1 (`BEGIN RSA PRIVATE KEY`) and SEC1 (`BEGIN EC PRIVATE KEY`) keys are accepted; encrypted keys are rejected.

pgcli copies the merged file to `config/tls/server.pem` (mode `0600`, owned by uid 1000) and does **not** generate `ca.crt` or `public.crt`/`private.key`. The source file is left untouched. Automatic renewal does not apply: replace the source PEM and re-run the install with `--force` to put the new certificate into a fresh container.

For a self-signed `pg cert` certificate, the certificate file itself is what clients trust — use `kafka.crt` wherever the examples below say `ca.crt`. If the certificate is signed by a public or private CA, point clients at that CA chain instead, and include any intermediate certificates in `kafka-server.pem`.

In a cluster, issue a **separate** certificate per node that covers that node's advertised IP, and pass it only to that node's install:

```bash
# On node 1, whose --advertise-host is 192.0.2.11:
pg cert --host 192.0.2.11 --cert-file kafka-node1.crt --key-file kafka-node1.key
cat kafka-node1.crt kafka-node1.key > kafka-node1.pem
pg addon install kafka --name prod --cluster prod --node-id 1 --force ... --tls-pem ./kafka-node1.pem

# Clients trust every node: concatenate the per-node certificates (or CAs).
cat kafka-node1.crt kafka-node2.crt kafka-node3.crt > kafka-ca-bundle.pem
```

If a node's certificate does not cover its `--advertise-host`, the cluster install is refused before the container is recreated.

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

For a three-node cluster (`node 2 of prod` shown; `--listen 0.0.0.0`):

```properties
process.roles=broker,controller
node.id=2
listeners=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:19093
advertised.listeners=PLAINTEXT://192.0.2.12:9092
inter.broker.listener.name=PLAINTEXT
controller.listener.names=CONTROLLER
listener.security.protocol.map=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
controller.quorum.voters=1@192.0.2.11:19093,2@192.0.2.12:19093,3@192.0.2.13:19093
log.dirs=/var/lib/kafka/data
offsets.topic.replication.factor=3
transaction.state.log.replication.factor=3
transaction.state.log.min.isr=2
default.replication.factor=3
min.insync.replicas=2
```

The voter list is identical on all three hosts and sorted by node id; this node's `node.id` is the only line that differs. Replication factors are `min(3, voters)` and ISR is `min(2, RF)`.

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

- `--partitions` defaults to the broker's `num.partitions` (upstream default `1`). `--replication-factor 0` (the default) means "the instance's RF" — `1` on a single node, `min(3, voters)` in a cluster (so `3` on a three-node cluster).
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

In a cluster, every broker advertises its own host, so a client can bootstrap from any node and is redirected to the partition leader. Producers and consumers may run on different hosts:

```bash
pg addon kafka topic create e2e --partitions 3 --replication-factor 3 --name prod

# Produce on node 1 through the local broker:
pg addon kafka shell --name prod -- bash -lc \
  'printf "alpha\nbeta\n" | /opt/kafka/bin/kafka-console-producer.sh --bootstrap-server 192.0.2.11:9092 --topic e2e'

# Consume on node 3 from node 2's broker:
pg addon kafka shell --name prod -- bash -lc \
  '/opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server 192.0.2.12:9092 --topic e2e --from-beginning --max-messages 2'
```

### Testing from a host with kcat

[kcat](https://github.com/edenhill/kcat) is a small native producer/consumer
that is convenient for smoke tests from outside the container (`apt install
kcat` on Debian/Ubuntu, `brew install kcat` on macOS). Bootstrap from any node —
the client follows the advertised leaders. The examples use the `e2e` topic
created above (3 partitions, RF=3):

```bash
# PLAINTEXT
printf 'alpha\nbeta\n' | kcat -P -b 192.0.2.11:9092 -t e2e
kcat -C -q -b 192.0.2.12:9092 -t e2e -o beginning -e -c 2   # first two messages
kcat -C -q -b 192.0.2.13:9092 -t e2e -o -1 -e -c 1         # the last message
```

For TLS, point kcat at the CA bundle from [Cluster TLS](#cluster-tls) and
select the SSL protocol (the TLS listener is on port `9093`):

```bash
printf 'gamma\n' | kcat -P -b 192.0.2.11:9093 -t e2e \
  -X security.protocol=SSL -X ssl.ca.location=kafka-ca-bundle.pem
kcat -C -q -b 192.0.2.12:9093 -t e2e -o -1 -e -c 1 \
  -X security.protocol=SSL -X ssl.ca.location=kafka-ca-bundle.pem
```

- `-o beginning` / `-o -1` start at the first / last message; `-e` exits at
  EOF and `-c N` stops after N messages; `-q` prints only message bodies.
- With `-b`, use the advertised broker address; the client is redirected to the
  partition leader, so there is no per-partition configuration.
- These examples do **not** use a consumer group: kcat reads the partitions
  directly and commits no offsets, so nothing appears in
  `pg addon kafka group list`. Add `-G NAME` to consume as a group (topics
  become positional arguments) — see [Consumer groups](#consumer-groups).

## Consumer groups

`kafka-consumer-groups.sh` is wrapped for the two everyday queries:

```bash
pg addon kafka group list --name k1
pg addon kafka group describe demo-group --name k1
```

- `list` prints the group ids that have committed offsets on this cluster.
- `describe` prints the group state, its members and per-partition
  `CURRENT-OFFSET` / `LOG-END-OFFSET` / `LAG`, plus the consumer id and host.
  A lag that keeps growing means the consumers are not keeping up.
- A group only appears after its first offset commit; `group.id` is set by the
  client, not by pgcli. To create one for a smoke test, consume with `--group`:

```bash
pg addon kafka shell --name prod -- bash -lc \
  '/opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server 192.0.2.11:9092 --topic e2e --group demo-group --from-beginning --max-messages 2'
pg addon kafka group list --name prod
pg addon kafka group describe demo-group --name prod
```

From a host, the same thing with kcat uses `-G`; in group mode topics are
positional arguments, not `-t`:

```bash
kcat -C -q -G demo-group -b 192.0.2.11:9092 \
  -X auto.offset.reset=earliest -c 2 e2e
pg addon kafka group list --name prod
pg addon kafka group describe demo-group --name prod
```

The `console-consumer` and kcat examples in the sections above omit the group
on purpose (read-and-exit smoke tests), so they never create one.

- The instance must be running for both commands. A consumer that stops without
  committing leaves the group behind in the `Empty` state; its offsets and lag
  are still visible until the offsets expire.

## Status and quorum

```bash
pg addon kafka status --name k1          # container state, endpoints, ids, paths
pg addon kafka cluster status --name k1  # KRaft metadata quorum leader and voters
pg logs addon kafka --name k1 -f         # broker logs (stdout/stderr)
```

`cluster status` runs `kafka-metadata-quorum.sh --bootstrap-controller <local controller> describe --status`; on a healthy single node it prints the cluster id, a leader and one voter. In a cluster it reports the leader and every voter with its log lag and whether it is caught up — run it on any node:

```
ClusterId:              <derived id>
LeaderId:               1
CurrentVoters:          [{"id": 1, ...}, {"id": 2, ...}, {"id": 3, ...}]
```

`N` voters tolerate `(N-1)/2` stopped nodes — three nodes tolerate one, two nodes tolerate none. Stopping a majority loses the metadata quorum: brokers keep their existing partitions readable/writable only as far as `min.insync.replicas` allows, but no leader election or metadata change is possible until quorum returns.

`install` and `start` wait up to 90 seconds for `kafka-broker-api-versions.sh` to answer on the local broker endpoint (15 seconds in cluster mode, where the full quorum only forms after the last voter is installed). A broker that never becomes ready fails the command and prints the last log lines; data is preserved.

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

In a cluster, `remove` only affects **this host** — pgcli never contacts the other nodes, and their static voter lists still reference this node. `pg addon remove` prints the quorum impact first. With three voters, removing (or stopping) one leaves the cluster available; removing a second loses quorum. Re-adding the node later requires the **same** `--cluster`, `--node-id` and `--peer` list; after `--clean-data` the data dir is empty and the broker formats it again with the same derived cluster id.

Auto-start after reboot is also per host — enable it on every node:

```bash
pg autostart enable --kafka --name prod   # run on each host
pg autostart status
pg autostart disable --kafka --name prod
```

Boot start re-renders `server.properties` first and is idempotent (`pg start` also brings up autostart-enabled Kafka instances).

## Production notes

- **Static quorum, no online resize.** `--cluster` installs one node of a fixed KRaft quorum. There is no online controller add/remove: to grow or shrink the cluster you must reinstall nodes with a new voter list. Use three (or another odd number ≥ 3) voters for HA; two voters are allowed but tolerate no failure.
- **Single node is still the default.** Without `--cluster` there is no replication and no HA: a stopped broker means no reads or writes for that instance.
- **KRaft combined mode.** Broker and controller share one process. This is fine for development and small deployments; the upstream project recommends isolated controller/broker roles for larger critical clusters, which this addon does not provide.
- **No authentication; TLS is transport-only.** The plaintext listener is always enabled; `--tls` adds an encrypted listener but `ssl.client.auth=none` means clients are not authenticated. Anyone who can reach either port can read and write every topic. In cluster mode the CONTROLLER quorum and inter-broker traffic are also PLAINTEXT — `--tls` only covers client connections. Keep the default loopback bind, or expose the cluster only on a trusted private network. Put real authentication (SASL/mTLS) in front of Kafka with your own proxy/gateway if needed.
- **No ZooKeeper.** Kafka 4.x is KRaft-only; there is no ZooKeeper container to manage, and `zookeeper.connect` is rejected as a managed key.
- **Copies are not backups.** Topic replication protects availability, not against accidental deletion or corruption. Stop the instance and copy the data dir for an offline backup of one node; there is no backup/restore command in this release, and an offline copy of a single cluster node is not a complete cluster backup.
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
| `--listen ADDR` | `127.0.0.1` | Bind address for the broker, SSL (when enabled) and controller listeners; cluster mode requires `0.0.0.0` or the advertised address |
| `--advertise-host IP` | `127.0.0.1` | Advertised broker host (and this node's controller voter host); never a wildcard; required explicitly on a fresh cluster install |
| `--broker-port N` | auto (base `9092`) | Broker host port; `0` = auto-assign |
| `--controller-port N` | auto (base `19093`) | Controller host port; `0` = auto-assign; must be explicit on a fresh cluster install because every peer embeds it |
| `--cluster NAME` | — | Multi-host cluster name; identical on every host; derives the KRaft cluster id |
| `--node-id N` | `1` single-node | 1-based node id, unique within the cluster; required on a fresh cluster install; rejected in single-node mode |
| `--peer ID@HOST:PORT` | — | Another node's controller voter; repeat for every other node; same peer list on every host |
| `--tls` | false | Add an SSL listener; the plaintext listener stays enabled |
| `--tls-pem FILE` | — | Combined PEM (chain + key) for the SSL listener; implies `--tls`; relative paths are resolved to absolute; must cover `--advertise-host` in cluster mode |
| `--tls-port N` | auto (base `9093`) | SSL listener host port; `0` = auto-assign; requires `--tls` |
| `--cluster-id ID` | derived (cluster) / generated | KRaft cluster id; in cluster mode it must equal the id derived from `--cluster` |
| `--config-option K=V` | — | Extra `server.properties` setting; repeatable; replaces the stored override map |
| `--force` | false | Re-render config and recreate the container, keeping data and ids; required to add or remove `--tls`; cannot change cluster identity fields |

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
| `--replication-factor N` | `0` = instance RF (`1` single-node, `min(3, voters)` in a cluster) | `create` only: replication factor |
| `--config K=V` | — | `create` only: topic config; repeatable |
| `--exclude-internal` | false | `list` only: hide Kafka's internal topics |
| `--yes` | false | `delete` only: skip the confirmation prompt |

### `pg addon kafka group list|describe`

| Argument/flag | Default | Description |
|---|---|---|
| `GROUP` | — | Consumer group id (`describe` only, positional) |
| `--name NAME` | `kafka` | Instance whose local broker is queried |

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
