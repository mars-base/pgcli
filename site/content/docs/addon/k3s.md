---
title: "K3s"
description: "Run K3s (lightweight Kubernetes) as a managed addon — a single-node or HA server/agent cluster in rootful Podman containers on the host network, with custom CA, containerd registry mirrors and a kubectl/kubeconfig passthrough; Linux rootful only"
weight: 71
---

`pg addon install k3s` runs a **K3s** node as a managed addon. It is a **top-level infra addon** (it does not front a PostgreSQL instance): each host runs at most one node, in a **rootful Podman container on the host network** with `--privileged --userns=host --cgroupns=host`, the same shape as K3s's official container install. Both roles are supported, so you can build a single-node cluster or a multi-host HA cluster (embedded etcd) with `server`/`agent` members.

**Requirements:** Linux with **rootful podman**. The node container needs host networking, privileged mode and the host cgroup namespace, which rootless podman and other platforms cannot provide; pgcli refuses to install otherwise. Host-network ports must be free:

| Role | TCP | UDP |
|---|---|---|
| `server` | 6443 (API), 2379/2380 (etcd), 10250 (kubelet) | 8472 (VXLAN) |
| `agent` | 10250 (kubelet) | 8472 (VXLAN) |

Ports 80/443 are only warned about: K3s's bundled Traefik and ServiceLB claim them after startup, which conflicts with `nginx`/`safeline` on the same host — disable them with `--disable traefik`/`--disable servicelb` if that is intended.

One node per host: the API, etcd and kubelet ports are fixed, so `pg.yaml` allows a single `addons.k3s` entry and a second one is rejected.

## Parameters

| Flag | Default | Description |
|---|---|---|
| `--name` | `k3s` | Node name (map key, container suffix and the default `--name` of `pg k3s ...`) |
| `--version` | `v1.37.2-k3s1` | K3s release tag; pulls `docker.io/rancher/k3s:<tag>`. Upstream writes the release as `v1.37.2+k3s1` — Docker tags use `-`, and a `+` is rejected |
| `--image` | | Full image reference override (takes precedence over `--version`; mutually exclusive with it) |
| `--role` | `server` | `server` or `agent` |
| `--cluster-init` | false | Server only: bootstrap the first server of an HA cluster with the **embedded etcd** datastore (a standalone server omits it and uses SQLite) |
| `--server` | | Join an existing cluster: `https://<first-server>:6443`. Mutually exclusive with `--cluster-init`; required for agents |
| `--token-file` | | **Required on every node**: host file holding the cluster token, mounted read-only at `/run/k3s-token` |
| `--node-name` | OS hostname | Kubernetes node name (`--node-name`); must be unique in a cluster |
| `--advertise-address` | | Server only: address peers and clients use to reach this node; also rewritten into the exported kubeconfig |
| `--tls-san` | | Server only: extra TLS SAN for the API certificate (repeatable) |
| `--disable` | | Server only: bundled component to disable, e.g. `traefik`, `servicelb`, `metrics-server` (repeatable) |
| `--ca-dir` | | Custom CA directory, **initial server only** and **before its first start**; see [Custom CA](#custom-ca) |
| `--registry-mirror` | | containerd registry mirror `REGISTRY=ENDPOINT` (repeatable, e.g. `docker.io=https://<id>.mirror.aliyuncs.com`); mutually exclusive with `--registries-file` |
| `--registries-file` | | Complete `registries.yaml` to mount (may include auth/TLS); mutually exclusive with `--registry-mirror` |
| `--data-dir` | `<base_dir>/addon/k3s/<name>/data` | Host directory for the K3s datastore (`/var/lib/rancher/k3s`) |
| `--kubelet-dir` | `<base_dir>/addon/k3s/<name>/kubelet` | Host directory for the kubelet root (`/var/lib/kubelet`) |
| `--cni-dir` | `<base_dir>/addon/k3s/<name>/cni` | Host directory for CNI state (`/var/lib/cni`) |
| `--log-dir` | `<base_dir>/addon/k3s/<name>/log` | Host directory mounted at `/var/log` |
| `--log-file` | | Also write the K3s server/agent log to this host file (`--log /var/log/k3s.log --alsologtostderr`) |
| `--container-log-file` | | Pin Podman's `k8s-file` container log to this host file (default: Podman's own log storage) |
| `--force` | false | Recreate the node container from the current config, keeping host data; required to apply changed flags or a changed registry mirror |

`--dsn` and `--pg-name` do not apply (K3s runs on the host network) and are rejected.

The controller mounts are:

| Host path | Container path |
|---|---|
| `--data-dir` | `/var/lib/rancher/k3s` |
| `--kubelet-dir` | `/var/lib/kubelet` |
| `--cni-dir` | `/var/lib/cni` |
| `--log-dir` | `/var/log` |
| `--log-file` | `/var/log/k3s.log` |
| `--token-file` | `/run/k3s-token` (read-only) |
| generated/user `registries.yaml` | `/run/k3s-registries.yaml` (read-only) |

## Single-node install

```bash
# Every node needs a pre-generated token: K3s would otherwise mint its
# own per-cluster token on first boot and other members could not join.
openssl rand -hex 32 > /tmp/k3s-token
chmod 600 /tmp/k3s-token

pg addon install k3s --name k3s --role server \
  --token-file /tmp/k3s-token \
  --advertise-address 10.0.0.11
```

The install pulls the image if needed, creates the directories, renders `registries.yaml` when mirrors are configured, starts the container and saves `pg.yaml`. It does **not** block until the cluster is ready — K3s bootstraps in the background:

```bash
pg k3s kubectl --name k3s -- get --raw=/readyz
pg k3s kubectl --name k3s -- get nodes -o wide
```

> **kubectl flags go after `--`.** pg's own parser handles the arguments first: `-o` is pg's global config-output flag and `-n` is unknown to it. Anything after `--` reaches kubectl untouched.

## HA cluster

An HA cluster is the first server with `--cluster-init` (embedded etcd), the other servers joining with `--server`, and agents joining the same API. Every node needs the same token.

```bash
# 1. On the controller: one token, copied to every node (mode 0600).
openssl rand -hex 32 > /tmp/k3s-token && chmod 600 /tmp/k3s-token

# 2. First server: bootstraps the embedded-etcd datastore.
pg addon install k3s --name k3s --role server --cluster-init \
  --token-file /tmp/k3s-token \
  --advertise-address 10.0.0.11 \
  --tls-san 10.0.0.11 --tls-san 10.0.0.12 --tls-san 10.0.0.13

# 3. Other servers: join the first one (no --cluster-init).
pg addon install k3s --name k3s --role server \
  --server https://10.0.0.11:6443 \
  --token-file /tmp/k3s-token \
  --advertise-address 10.0.0.12 \
  --tls-san 10.0.0.11 --tls-san 10.0.0.12 --tls-san 10.0.0.13

# 4. Agents: join the same API.
pg addon install k3s --name k3s --role agent \
  --server https://10.0.0.11:6443 \
  --token-file /tmp/k3s-token
```

Wait for the first server to answer `/readyz` before starting the joiners. List every server address in `--tls-san` so the API certificate is valid whichever server clients hit. `pg k3s kubectl --name k3s -- get nodes` shows `control-plane` for servers and `<none>` for agents (K3s writes the role label a few seconds after the node registers).

The Ansible add-on groups build this layout for you (3 servers + 1 agent by default, `serial: 1`, token and optional CA distributed by the `k3s_shared` role) — see [Deploying add-ons with Ansible](../ansible/addons.md) and the [add-on cluster chapter](../ansible/addon-cluster.md). The single group (`addon_k3s_single`) takes one or more hosts: the first becomes a `--cluster-init` server (its embedded-etcd datastore can take more servers later) and the rest join it as agents; the CLI standalone example above keeps SQLite.

**NATed peer networks.** etcd 3.7 checks a joining peer's certificate against the source address it observes. When peers see each other through NAT (a host that SNATs VM-to-VM traffic, for example), the join fails with `tls: "<observed address>" does not match any of the DNSNames`. Add that observed source address to every server's `--tls-san` — or set `_addon_cluster_k3s_extra_tls_sans: ["<address>"]` in the cluster group — so it lands in the etcd peer certificates too.

## Custom CA

K3s ships a **10-year self-signed CA** that is never rotated automatically, and there is no flag to change its lifetime. To use a longer-lived CA, generate the complete set with the K3s utility script ([`contrib/util/generate-custom-ca-certs.sh`](https://github.com/k3s-io/k3s/blob/master/contrib/util/generate-custom-ca-certs.sh)) and pass it with `--ca-dir`:

```bash
DATA_DIR=/tmp/k3s-ca DAYS=36500 bash generate-custom-ca-certs.sh
find /tmp/k3s-ca -name '*.crt' -o -name '*.key'   # inspect the layout
```

The directory must contain the full set K3s expects:

```text
server-ca.crt            server-ca.key
client-ca.crt            client-ca.key
request-header-ca.crt    request-header-ca.key
etcd/peer-ca.crt         etcd/peer-ca.key
etcd/server-ca.crt       etcd/server-ca.key
service.key
```

pgcli copies them into `<data-dir>/server/tls` (keys with mode 0600) **before the initial server's first start** and refuses partial sets. It is valid only on the standalone or `--cluster-init` server; joining servers and agents learn the CA from the cluster. On repeat installs the existing CA is kept when it matches the source; a different CA is refused — rotate it with `k3s certificate rotate-ca` or clear the data directory (the datastore already holds the original trust material).

The Ansible role can generate and distribute this CA for the initial server: set `_addon_cluster_k3s_ca_days: 36500` in `group_vars/addon_k3s_cluster/main.yml` (0 keeps the K3s default).

## Registry mirrors and air-gap

containerd reads its mirror configuration once at node startup. Two mutually exclusive forms are supported:

```bash
# Rendered form: pgcli writes <base>/addon/k3s/<name>/registries.yaml (0600).
pg addon install k3s ... \
  --registry-mirror docker.io=https://<id>.mirror.aliyuncs.com

# Complete form: your own registries.yaml, including auth/TLS sections.
pg addon install k3s ... --registries-file /etc/k3s/registries.yaml
```

The file is mounted read-only at `/run/k3s-registries.yaml` and passed as `--private-registry`. Applying a changed mirror requires recreating the container, which keeps the host data:

```bash
pg addon install k3s ... --registry-mirror docker.io=https://new-mirror.example.com --force
```

Mirrors affect **Pod images only** — the K3s node image itself is pulled by Podman (configure that separately with `pg mirror`, or preload the tarball with Ansible's offline images). A truly air-gapped cluster additionally needs the K3s **airgap-images tarball** (pause, coredns, traefik, metrics-server, local-path-provisioner, ...) placed where containerd can import it.

## kubectl and kubeconfig

`pg k3s` drives the local node containers through `podman exec`; nothing has to be installed on the host and no remote cluster is involved.

```bash
pg k3s kubectl get nodes                          # the only configured node
pg k3s kubectl -- get nodes -o wide
pg k3s kubectl -- get pods -n kube-system
pg k3s kubectl -- apply -f manifest.yaml
pg k3s kubectl --name k3s get pods                # several nodes: pick one
```

`kubectl` and `kubeconfig` are server-only; an agent is rejected. With several nodes configured, `--name` selects the server. `stdin` is attached, so `kubectl apply -f -` works.

```bash
pg k3s kubeconfig --name k3s > kubeconfig.yaml       # stdout
pg k3s kubeconfig --name k3s --output ~/.kube/config # mode 0600
```

When the server was installed with `--advertise-address`, the loopback API URL (`https://127.0.0.1:6443`) in the kubeconfig is rewritten to that address so the file works from other hosts. Remote clusters use an exported kubeconfig with any kubectl — `pg k3s` is local-only.

## Lifecycle

```bash
pg addon list                          # "Infra add-ons (k3s):" section
pg addon start k3s --name k3s          # start the container (recreates if missing)
pg addon stop k3s --name k3s           # stop, keep data
pg logs addon k3s --name k3s -f        # container logs
pg addon remove k3s --name k3s         # remove the container and config entry
pg addon remove k3s --name k3s --clean-data   # also delete data/kubelet/cni/log
```

`--clean-data` deletes the data, kubelet, CNI and log directories and the pgcli-rendered `registries.yaml`; user-supplied token/CA/registries files are never deleted.

Autostart uses the existing container start path:

```bash
pg autostart enable --k3s --name k3s
pg autostart list
pg start                # starts autostart K3s nodes: cluster-init server, joining servers, standalone servers, agents
```

`pg start` starts the node containers in that order; it does not wait for cluster readiness. On a multi-host cluster, run `pg start` on the initial server's host first.

## Limitations

- **One node per host.** The fixed host-network ports (6443/2379/2380/10250/8472) make a second node impossible; use more hosts for more nodes.
- **Role and membership are part of the data directory.** Changing `role`, `--cluster-init` or `--server` requires removing the node with `--clean-data` and reinstalling; `--force` alone keeps the old datastore.
- **Traefik/ServiceLB bind ports 80 and 443 on the host.** Disable them with `--disable traefik` (ServiceLB keeps serving LoadBalancer services) or `--disable servicelb` when nginx/safeline/other web add-ons use those ports.
- **Token and CA rotation are manual.** pgcli does not rotate them; use `k3s certificate rotate-ca` or reinstall from a clean data directory.
- **No daemon integration.** Nodes rely on `--restart unless-stopped` for self-healing and on `pg start --autostart` for boot order, like the other add-ons.