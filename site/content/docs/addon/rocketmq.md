---
title: "RocketMQ"
description: "Run Apache RocketMQ 5.x as a managed addon — three containers (NameServer + broker + proxy), standalone or a multi-host controller-mode cluster with automatic broker failover, topic and message helpers, rendered configs and host networking"
weight: 67
---

`pg addon install rocketmq` runs **Apache RocketMQ 5.x** as a managed addon from the official `docker.io/apache/rocketmq` image. Like Kafka, ClickHouse and RabbitMQ it is a **top-level addon** that does not front a PostgreSQL instance.

Each instance is **three containers on the host network**: a **NameServer**, a **broker** and a **proxy**. The same shape is used in both modes, so ports, commands, logs and lifecycle behave identically:

- **Standalone** — one NameServer with no controller, one async master broker with no replicas, and a proxy. This is the quickest way to send and receive messages. The 4.x master/slave and DLedger broker deployment modes are **not** used.
- **Multi-host cluster** — every NameServer embeds a **DLedger (Raft) controller**, the brokers run in **controller mode**, and one broker is elected master while another takes over automatically when it fails. The proxy is the client entry point. This is RocketMQ 5.x's auto-failover deployment mode.

> **There is no authentication and no TLS.** RocketMQ listens on host networking, and cluster ports are fixed. Anyone who can reach the NameServer, broker or proxy ports can read and write every topic. Run it only on a trusted private network or behind a firewall that restricts `9876`, `9877`, `10911`, `10912`, `8080` and `8081`. There is also no web UI and no ACL in this addon.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `rocketmq` | Instance name (map key; also the default subcommand name) |
| `--version` | `5.5.0` | Upstream image tag, pulled as `docker.io/apache/rocketmq:<version>`. Only 5.x releases are accepted |
| `--data-dir` | `<base_dir>/addon/rocketmq/<name>/data` | Instance data root: the broker store is `<dir>/broker` and, in cluster mode, the controller store is `<dir>/controller`. Must be absolute except for `base_dir`-relative paths |
| `--advertise-host` | `127.0.0.1` standalone; required cluster | Host advertised to clients and peers. Cluster mode requires a concrete non-loopback **IPv4** address; hostnames, IPv6 and `0.0.0.0` are rejected. Set it to the host's LAN IP for standalone instances that remote clients use |
| `--cluster NAME` | — | Switch on multi-host cluster mode. Every host must use the same name; it becomes the DLedger controller group and the broker cluster/group name. Cluster identity is immutable after install |
| `--controller-id ID` | — | This node's controller member id (`n0`, `n1`, …). Required on a fresh cluster install; must be unique in the group and match `[A-Za-z][A-Za-z0-9]{0,31}`. Rejected in standalone mode |
| `--peer ID@HOST` | — | Another controller member. Repeat once per **other** node, with a bare non-loopback IPv4 host and no port (the controller port is fixed at `9877`). Pass the full list of the other members on every host; the local peer is added automatically. Rejected in standalone mode |
| `--namesrv-port` | auto (`9876`) standalone | NameServer host port. `0` = auto-assign from the RocketMQ pool. Standalone only; cluster members are fixed at `9876` |
| `--broker-port` | auto (`10911`) standalone | Broker listen port; HA is `broker_port + 1` and the VIP port is `broker_port - 2`. Standalone only; cluster members are fixed at `10911` |
| `--proxy-port` | auto (`8080`) standalone | Proxy remoting port. Standalone only; cluster members are fixed at `8080` |
| `--grpc-port` | auto (`8081`) standalone | Proxy gRPC port for 5.x SDKs. Standalone only; cluster members are fixed at `8081` |
| `--controller-port` | — | Embedded controller port. Not accepted in standalone mode; cluster members are fixed at `9877` |
| `--heap` | `512m` | Broker JVM heap, applied as `JAVA_OPT_EXT=-Xms<size> -Xmx<size>` |
| `--namesrv-heap` | `256m` | NameServer JVM heap |
| `--proxy-heap` | `256m` | Proxy JVM heap |
| `--config-option K=V` | — | Extra `broker.conf` setting (`flushDiskType=SYNC_FLUSH`, `brokerPermission=4`); repeatable and **replaces** the stored override list. pgcli-managed keys are rejected (identity, listeners, store paths, controller settings, replication settings, `autoCreateTopicEnable`) |
| `--force` | false | Re-render the three configs and recreate the three containers, keeping the broker store and controller store. Cluster identity and fixed ports cannot be changed even with `--force` |

`--image`, `--listen`, `--tls`, `--tls-pem`, `--tls-cert`, `--tls-key`, `--node-id`, `--cluster-id` and `--join` do not apply to RocketMQ and are rejected. Use `--version` to select the image.

## Install a standalone instance

```bash
pg addon install rocketmq --name rmq
```

The install pulls the image if needed, renders the configs, creates the directories, starts `namesrv → broker → proxy`, and waits for the NameServer, broker registration and both proxy listeners:

```text
✓ RocketMQ "rmq" installed
  Version:     5.5.0
  Image:       docker.io/apache/rocketmq:5.5.0
  Containers:  pgcli-rocketmq-rmq-namesrv, pgcli-rocketmq-rmq-broker, pgcli-rocketmq-rmq-proxy
  Namesrv:     127.0.0.1:9876
  Broker:      127.0.0.1:10911 (HA 10912, VIP 10909)
  Proxy:       127.0.0.1:8080 (remoting), 127.0.0.1:8081 (gRPC)
  Mode:        single-node (no controller, no replicas)
  JVM heap:    broker 512m, namesrv 256m, proxy 256m
  Data dir:    /home/you/.pgcli/addon/rocketmq/rmq/data
  Config:      /home/you/.pgcli/addon/rocketmq/rmq/config
```

To let clients on other hosts connect, advertise the host's LAN address (the proxy config and broker both use it):

```bash
pg addon install rocketmq --name rmq --advertise-host 10.0.0.11
```

A second standalone instance takes the next free port from each pool; it does not reuse the first instance's ports:

```bash
pg addon install rocketmq --name rmq2 --data-dir /srv/rocketmq-data
```

On a small VM, lower all three heaps to keep the three JVMs inside memory:

```bash
pg addon install rocketmq --name rmq \
  --heap 384m --namesrv-heap 192m --proxy-heap 192m
```

Re-running `install` without `--force` is a no-op that reports the current state. With `--force`, pgcli rewrites `namesrv.conf`, `broker.conf` and `rmq-proxy.json`, recreates the three containers and keeps the broker and controller stores.

## Multi-host cluster

Cluster mode uses RocketMQ 5.x's **controller mode**: the controller is embedded in every NameServer as a DLedger (Raft) group, and controller-mode brokers elect a master and fail over automatically. Every host runs its own `pg addon install rocketmq`; pgcli never SSHes to other hosts.

Requirements:

- The same `--cluster` name on every host.
- A unique `--controller-id` per host (`n0`, `n1`, `n2`, …).
- That host's `--advertise-host` as a concrete non-loopback IPv4 address.
- `--peer ID@HOST` entries for the **other** controller members. Pass the full peer set on every host; the local peer is added automatically.
- The fixed cluster ports free on every host: `9876`, `9877`, `10911`, `10912`, `8080` and `8081`. One clustered RocketMQ instance per host.
- **At least 3 members for fault tolerance.** The DLedger quorum is the majority, so 2 members tolerate no failure; pgcli prints a warning for a 2-member cluster.

Install the members one host at a time. For three hosts `10.0.0.11`, `10.0.0.12` and `10.0.0.13`:

```bash
# on 10.0.0.11 (controller n0)
pg addon install rocketmq --name rmq --cluster prod --controller-id n0 \
  --advertise-host 10.0.0.11 \
  --peer n1@10.0.0.12 --peer n2@10.0.0.13

# on 10.0.0.12 (controller n1)
pg addon install rocketmq --name rmq --cluster prod --controller-id n1 \
  --advertise-host 10.0.0.12 \
  --peer n0@10.0.0.11 --peer n2@10.0.0.13

# on 10.0.0.13 (controller n2)
pg addon install rocketmq --name rmq --cluster prod --controller-id n2 \
  --advertise-host 10.0.0.13 \
  --peer n0@10.0.0.11 --peer n1@10.0.0.12
```

The first two installs are expected to finish with a warning such as:

```text
[!] RocketMQ broker is running, but it has not registered with the controller yet
(broker not listed in clusterList) — install the remaining nodes with the same
--cluster and --peer values ...
```

This is normal: the controller quorum only forms once a majority of NameServers is up. The install still exits successfully and leaves the containers running. After the third member is installed, the controller elects the broker master and the broker registers.

Check every member:

```bash
pg addon rocketmq status --name rmq
pg addon rocketmq cluster status --name rmq
```

`status` shows the three container states, the controller id, the full peer list and — when the cluster is reachable — how many brokers are registered and how many are masters. `cluster status` prints the raw `clusterList` table; the controller's `BID` column (labelled `BrokerId` in older clients) is `0` for the current master and non-zero for replicas:

```text
rocketmq "rmq" — cluster prod: 3 broker(s), 1 master(s)
#Cluster Name  #Broker Name  #BID  #Addr             #Version  #InTPS(LOAD)  #OutTPS(LOAD)
prod           prod          0     10.0.0.13:10911   V5_5_0    0.00(0,0ms)   0.00(0,0ms)
prod           prod          1     10.0.0.11:10911   V5_5_0    0.00(0,0ms)   0.00(0,0ms)
prod           prod          2     10.0.0.12:10911   V5_5_0    0.00(0,0ms)   0.00(0,0ms)
```

The column layout and row order are produced by `mqadmin` and may differ slightly between 5.x releases; pgcli only relies on the cluster name and broker id columns.

### Failover

When the master broker stops, the controller elects another in-sync replica after a heartbeat timeout. Clients that connect through the proxy or NameServer retry automatically. To watch a failover:

```bash
# find the master (BID 0) in cluster status, then on that host:
pg addon stop rocketmq --name rmq

# on any member, poll until another BID 0 appears:
pg addon rocketmq cluster status --name rmq

# start the old broker again; it rejoins as a replica:
pg addon start rocketmq --name rmq
```

`enableElectUncleanMaster=false` is rendered, so a replica that is not in the in-sync state set is not elected. If the controller quorum itself loses its majority (for example two of three NameServers are down), no master can be elected and writes stop until the quorum returns. Stopping or removing a member does **not** rewrite the peer lists on the remaining hosts; pgcli never contacts the other nodes.

## Ports

| Port | Role | Standalone | Cluster |
|---|---|---|---|
| `9876` | NameServer client/RPC | auto, base `9876` | fixed |
| `9877` | Embedded DLedger controller | not used | fixed |
| `10911` | Broker listen | auto, base `10911` | fixed |
| `10912` | Broker HA replication | `broker_port + 1` | fixed |
| `10909` | Broker VIP | `broker_port - 2` | fixed |
| `8080` | Proxy remoting | auto, base `8080` | fixed |
| `8081` | Proxy gRPC | auto, base `8081` | fixed |

Standalone instances can coexist on one host because every port is drawn from a pool and reserved together with the derived HA/VIP ports. In cluster mode all of these ports are fixed because the controller membership and broker addresses are baked into every node's config.

## Clients

The proxy is the single client entry point in both modes and runs in `CLUSTER` mode:

- **5.x gRPC SDK** (the recommended 5.x client): connect to `<advertise-host>:8081`.
- **Remoting SDK** (4.x-style clients): connect to `<advertise-host>:8080`, or directly to the broker on `<advertise-host>:10911`.
- **NameServer addresses** (`9876`, controller `9877`) are internal cluster plumbing. Do not expose them to remote clients.

A standalone instance defaults to `127.0.0.1`, so a client on another host needs an install with `--advertise-host` set to the host's reachable IP. In cluster mode every member requires a non-loopback IPv4 `--advertise-host` and the proxy on each host advertises that host.

## What pgcli creates

For instance `rmq` with the default base directory:

```text
~/.pgcli/addon/rocketmq/rmq/
├── config/
│   ├── namesrv.conf
│   ├── broker.conf
│   └── rmq-proxy.json
├── data/
│   ├── broker/        # broker store: commitlog, consumequeue, index, ...
│   └── controller/    # cluster mode only: DLedger controller state
└── logs/
    ├── namesrv/
    ├── broker/
    └── proxy/
```

Containers (default names; a configured namespace inserts a suffix after `pgcli-rocketmq`):

- `pgcli-rocketmq-rmq-namesrv`
- `pgcli-rocketmq-rmq-broker`
- `pgcli-rocketmq-rmq-proxy`

All three use `--network host`, `--restart unless-stopped` and a 30-second stop timeout. Mounts:

- `config/` → `/etc/rocketmq` (read-only)
- `data/broker/` → `/home/rocketmq/store`
- `data/controller/` → `/home/rocketmq/DledgerController` (cluster mode, NameServer container only)
- `logs/<role>/` → `/home/rocketmq/logs`

The official image runs as **uid/gid 3000**. pgcli creates the writable directories and hands them to that user through `podman unshare chown` for rootless Podman, or `os.Chown` when running as root. Config files are `0644` and the config directory is mounted read-only.

The rendered `namesrv.conf` contains the listener and, in cluster mode, `enableControllerInNamesrv=true`, the DLedger group/peers/self id, the controller store path, `enableElectUncleanMaster=false` and `notifyBrokerRoleChanged=true`. `broker.conf` contains the broker cluster/group name, listen and HA ports, store paths, the advertised IP and the controller/NameServer addresses. The proxy JSON contains `proxyMode=CLUSTER`, the proxy cluster name, the NameServer address list and the gRPC/remoting ports; `remotingAccessAddr` and `localServeAddr` deliberately carry a bare host because RocketMQ appends the remoting port itself. `--config-option` values are appended to `broker.conf` only.

## Topics and messages

Topic auto-creation is enabled (`autoCreateTopicEnable=true`) for compatibility, but explicit creation is recommended so queue counts are deterministic:

```bash
pg addon rocketmq topic create surge --queues 3 --name rmq
pg addon rocketmq topic list --name rmq
pg addon rocketmq topic describe surge --name rmq
```

`topic create` creates the topic across the cluster (`-c <cluster-or-name>`) with `--queues` read and write queues (default 3). `topic list` hides RocketMQ's internal topics (`%RETRY%…`, `%DLQ%…`, `TBW102`, `SELF_TEST_TOPIC`, transaction/schedule/trace topics, the 5.x `rmq_sys_*` / `RMQ_SYS_*` topics, `*_REPLY_TOPIC` and the topic named after the cluster); pass `--system` to show everything. `topic describe` prints the per-broker queue and offset table from `topicStatus`.

Send a message and look it up by key:

```bash
pg addon rocketmq send surge "hello" --key k1 --tags demo --name rmq

pg addon rocketmq shell --service namesrv --name rmq -- \
  sh mqadmin queryMsgByKey -t surge -k k1 -n 127.0.0.1:9876
```

`send` prints the `sendMessage` result, including the message id, send status and queue. Deleting a topic removes all of its messages and asks for confirmation unless `--yes` is passed:

```bash
pg addon rocketmq topic delete surge --name rmq --yes
```

## Delivery and durability

**Standalone.** The broker is the only node (`brokerId=0`, `brokerRole=ASYNC_MASTER`) with no replicas. Messages are written to the broker store under `data/broker`, so a graceful stop/start keeps them. The rendered disk flush policy is RocketMQ's default **ASYNC_FLUSH**; a host power loss can therefore lose the most recent writes. For stronger durability, render `SYNC_FLUSH` (at the cost of throughput):

```bash
pg addon install rocketmq --name rmq --force \
  --config-option flushDiskType=SYNC_FLUSH
```

**Cluster.** Brokers replicate in controller mode. pgcli renders `allAckInSyncStateSet=true` and `minInSyncReplicas=2` for 3 or more members (`1` for a 2-member cluster), so a produce succeeds only when every in-sync replica has accepted the message. Disk flush is still ASYNC by default; use `flushDiskType=SYNC_FLUSH` when a power loss must not lose acknowledged writes across all replicas.

Replication protects against a **broker** failure while the controller quorum is healthy. It does not make the controller store disposable: `data/controller` holds the DLedger state and epoch files. Deleting it, wiping a member, or reinstalling with `--clean-data` destroys that member's controller state and can break the quorum. A member that is removed and rejoined must start with a fresh data directory and the same cluster/controller/peer identity.

`pg addon remove rocketmq --clean-data` deletes the instance directory (configs, broker store, controller store and logs) and a distinct custom data directory. Without `--clean-data`, the data and config directories are kept.

## Configuration, upgrades and immutability

- **Mutable with `--force`:** version, JVM heaps, broker.conf overrides and, for standalone instances, the listener ports. `--config-option` replaces the stored override map, so pass every override again on a forced reinstall.
- **Immutable even with `--force`:** in cluster mode `--cluster`, `--controller-id`, `--advertise-host`, `--peer` and the fixed ports. These values are embedded in every peer's configuration; changing them in place is rejected.
- **Topology changes require a rebuild:** standalone ↔ cluster is not an in-place change. Remove the instance with `--clean-data` and install it again in the desired mode.
- **Custom data directory:** pointing `--data-dir` at a new path makes the instance use a new, empty store; pgcli does not migrate existing data.

Managed `broker.conf` keys (rejected in `--config-option`) include `brokerClusterName`, `brokerName`, `brokerId`, `brokerRole`, `brokerIP1`/`brokerIP2`, `listenPort`, `haListenPort`, `namesrvAddr`, `enableControllerMode`, `controllerAddr`, every `controller*` key, `storePathRootDir`, `storePathCommitLog`, `storePathEpochFile`, `autoCreateTopicEnable`, `allAckInSyncStateSet`, `minInSyncReplicas`, `enableControllerInNamesrv`, `enableElectUncleanMaster` and `notifyBrokerRoleChanged`.

## Lifecycle, logs and autostart

```bash
pg addon start rocketmq --name rmq
pg addon stop rocketmq --name rmq
pg addon remove rocketmq --name rmq --clean-data

pg logs addon rocketmq --name rmq --service broker -f
pg logs addon rocketmq --name rmq --service namesrv
pg logs addon rocketmq --name rmq --service proxy
```

Stopping walks the containers in reverse (`proxy → broker → namesrv`); starting walks them forward (`namesrv → broker → proxy`). In cluster mode, start does not wait for the full controller quorum: when a majority is not yet reachable the broker stays in its waiting state and the command still succeeds. Check `pg addon rocketmq status` and `cluster status` after the other members are up.

Autostart is available like the other top-level addons:

```bash
pg autostart enable --rocketmq --name rmq
pg autostart status
```

It re-renders the three configs and starts the containers in order. For a cluster member, autostart does not wait for the controller quorum, so members can boot in any order. `pg addon start` (the global autostart starter) behaves the same way.

## Limitations

- **No authentication, TLS or ACL.** Restrict the network; the proxy and NameServer are open to anyone who can reach them.
- **No web UI.** Use `pg addon rocketmq status`, `cluster status`, topic commands and the logs.
- **No online scaling or rolling upgrade.** Cluster membership and fixed ports are immutable; member changes require a planned rebuild. The controller store must not be deleted while the member participates in a quorum.
- **2-member clusters have no fault tolerance.** Use 3 or more members for production.
- **Linux only.** The addon relies on host networking and the official image's `uid/gid 3000`.
- **No message migration, consumer-group management or broker/storage rebalancing.** Use the standard RocketMQ administration tools through `pg addon rocketmq shell`.
