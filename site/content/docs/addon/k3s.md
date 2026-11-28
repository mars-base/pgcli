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
| `--cluster-cidr` | `10.42.0.0/16` | Server only: Pod network range (`--cluster-cidr`); a comma-separated IPv4/IPv6 pair selects dual-stack, which must be set at cluster creation; every server must use the same value |
| `--service-cidr` | `10.43.0.0/16` | Server only: Service VIP range (`--service-cidr`); same dual-stack and all-servers-identical rules |
| `--kube-proxy-arg` | | Custom flag for the embedded kube-proxy, e.g. `proxy-mode=nftables` (Kubernetes 1.31+, repeatable; kube-proxy runs on every node) |
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

## kube-proxy and nftables

`--kube-proxy-arg` forwards custom flags to the embedded kube-proxy. On
Kubernetes 1.31+ (this build runs v1.37)
`--kube-proxy-arg proxy-mode=nftables` moves the Service and NodePort rules
from the KUBE-* iptables chains to native `ip`/`ip6` `kube-proxy` nftables
tables.
flannel and the bundled CNI plugins keep programming the iptables frontend; on
Debian 12 that frontend is nftables-backed (`iptables-nft`), so every rule
still lands in nf_tables, while hosts whose alternatives point at
`iptables-legacy` stay legacy.

Switching the proxy mode on a running cluster is disruptive (set it on
install, or recreate the node with `--force`), and NodePorts on 127.0.0.1 are
not supported in nftables mode before Kubernetes 1.38 (KEP-6032).

## Single-node install

**The SQLite single-node install is for local testing and development.** Production deployments should use the HA cluster layout below (embedded etcd, at least three servers).

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

**Expansion: agents can join, servers cannot.**

- **Agents can join** a SQLite single-node cluster at any time with `--role agent --server https://<server>:6443` and the same token:

  ```bash
  # On the agent host, with the same token file as the server:
  pg addon install k3s --name k3s --role agent \
    --server https://10.0.0.11:6443 \
    --token-file /tmp/k3s-token
  ```

  Then confirm the node is `Ready` from the server:

  ```bash
  pg k3s kubectl --name k3s -- get nodes -o wide
  ```
- **Servers cannot join** a SQLite-backed cluster — multiple servers require embedded etcd. The upstream-supported conversion is to restart the existing server with `--cluster-init`; K3s converts the datastore to embedded etcd, after which further servers join exactly as in the HA layout. With pgcli that restart is `--force` with the new flags (the container is recreated and the data directory is kept). If growth is uncertain, install the first server in the HA shape from the start (`--cluster-init`; the Ansible group always does).

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

Wait for the first server to answer `/readyz` before starting the joiners. List every server address in `--tls-san` so the API certificate is valid whichever server clients hit; agent addresses are not needed — an agent serves neither the API nor etcd. `pg k3s kubectl --name k3s -- get nodes` shows `control-plane` for servers and `<none>` for agents (K3s writes the role label a few seconds after the node registers).

**Pod and Service CIDRs.** Set `--cluster-cidr` / `--service-cidr` only when the defaults (`10.42.0.0/16` and `10.43.0.0/16`) collide with existing networks. Every server must pass the same values — a server started with different ranges fails to join — and they are fixed at cluster creation. For dual-stack, pass a comma-separated IPv4/IPv6 pair on the bootstrap server, e.g. `--cluster-cidr 10.42.0.0/16,2001:db8:42::/56 --service-cidr 10.43.0.0/16,2001:db8:43::/112`.

The Ansible group `addon_k3s_cluster` always builds the cluster mode (embedded etcd) and never the SQLite standalone form: the bootstrap server gets `--cluster-init` even when the group lists a single host, which is then a one-member etcd cluster and grows by adding hosts; only the CLI standalone example above (no `--cluster-init`) uses SQLite. List one or more hosts, declare each role in `addon_k3s_roles` (`server` or `agent`; hosts left out are agents — group order does not decide the role), and name the server that bootstraps embedded etcd with `addon_k3s_cluster_init_server` (empty picks the first server, and it does not have to be the group's first host). The `k3s_shared` role distributes the token and the optional CA, and the play is `serial: 1`; joining nodes started before the bootstrap server retry until its API answers, and hosts added to the group join on the next run — see [Deploying add-ons with Ansible](../ansible/addons.md) and the [add-on cluster chapter](../ansible/addon-cluster.md).

**NATed peer networks.** etcd 3.7 checks a joining peer's certificate against the source address it observes. When peers see each other through NAT (a host that SNATs VM-to-VM traffic, for example), the join fails with `tls: "<observed address>" does not match any of the DNSNames`. The Ansible group handles this: every server's `--tls-san` gets the server IPs, and `addon_k3s_extra_tls_sans` adds any further addresses — put the SNAT source there, e.g. `addon_k3s_extra_tls_sans: ["10.0.0.254"]`. A direct L3 network — the usual bare-metal deployment — needs nothing beyond the server IPs. With plain `pg addon install`, pass the observed address as an extra `--tls-san` yourself.

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

The Ansible role can generate and distribute this CA for the initial server: set `addon_k3s_ca_days: 36500` in `group_vars/addon_k3s_cluster/main.yml` (0 keeps the K3s default; a custom lifetime must be greater than 10 days).

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

`pg k3s` drives the local node containers through `podman exec`; nothing has to be installed on the host.

```bash
pg k3s kubectl get nodes                          # the only configured node
pg k3s kubectl -- get nodes -o wide
pg k3s kubectl -- get pods -n kube-system
pg k3s kubectl -- apply -f - < manifest.yaml
pg k3s kubectl --name k3s get pods                # several nodes: pick one
pg k3s kubectl --kubeconfig kubeconfig.yaml -- get nodes   # remote cluster
```

`kubectl` and `kubeconfig` are server-only; an agent is rejected. With several nodes configured, `--name` selects the server. `stdin` is attached, so `kubectl apply -f -` works. kubectl runs inside the node container, which cannot see host paths — pipe manifests through stdin (`apply -f - < file.yaml`) instead of passing a host `-f` path.

```bash
pg k3s kubeconfig --name k3s > kubeconfig.yaml       # stdout
pg k3s kubeconfig --name k3s --output ~/.kube/config # mode 0600
```

When the server was installed with `--advertise-address`, the loopback API URL (`https://127.0.0.1:6443`) in the kubeconfig is rewritten to that address so the file works from other hosts. From a host that is not a K3s node, pass that kubeconfig to `--kubeconfig`: pg runs kubectl from a temporary container of the K3s image (pgcli's default tag, pulled into Podman on demand) against that file. The container bypasses the host's proxy settings and talks to the API server directly, so any host with Podman can operate the cluster without installing a kubectl package.

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

## Removing a node

Removing an agent:

```bash
# On any healthy server: stop scheduling and drop the Node object.
pg k3s kubectl --name k3s -- drain node4 --ignore-daemonsets --delete-emptydir-data
pg k3s kubectl --name k3s -- delete node node4
# On the removed host: delete the container and its data.
pg addon remove k3s --name k3s --clean-data
```

Removing a server, from a remaining server:

```bash
# 1. Evict workloads while the departing node is still healthy.
pg k3s kubectl --name k3s -- drain node3 --ignore-daemonsets --delete-emptydir-data

# 2. Remove the etcd member while every member is still running: the change is
#    a raft entry and needs the current membership's quorum, which an
#    even-sized cluster (two servers, for example) loses as soon as one member
#    is down. A member left in the list keeps counting toward quorum even
#    after its server is gone. K3s turns on client-certificate auth for
#    embedded etcd, so pass the certificates K3s wrote into the data directory.
TLS=/data/pg/addon/k3s/k3s/data/server/tls/etcd   # <data-dir>/server/tls/etcd
export ETCDCTL_ENDPOINTS=https://127.0.0.1:2379
pg etcdctl --ca $TLS/server-ca.crt \
  --cert $TLS/server-client.crt --key $TLS/server-client.key member list
pg etcdctl --ca $TLS/server-ca.crt \
  --cert $TLS/server-client.crt --key $TLS/server-client.key member remove <member-id>

# 3. Drop the Node object.
pg k3s kubectl --name k3s -- delete node node3
```

Then run `pg addon remove k3s --name k3s --clean-data` on the departing host.
`--clean-data` matters: role and membership live in the data directory, so a
leftover directory makes a later reinstall join with the old identity. In an
Ansible-managed cluster also drop the host from the group's inventory and
`addon_k3s_roles` as described in the
[add-on cluster chapter](../ansible/addon-cluster.md).

## Limitations

- **One node per host.** The fixed host-network ports (6443/2379/2380/10250/8472) make a second node impossible; use more hosts for more nodes.
- **Role and membership are part of the data directory.** Changing `role` or joining a different cluster requires removing the node with `--clean-data` and reinstalling. The one upstream-supported in-place change is converting a SQLite single-node server to embedded etcd: restart it with `--cluster-init` (`--force` recreates the container and keeps the data), after which more servers can join.
- **Traefik/ServiceLB bind ports 80 and 443 on the host.** Disable them with `--disable traefik` (ServiceLB keeps serving LoadBalancer services) or `--disable servicelb` when nginx/safeline/other web add-ons use those ports.
- **Token and CA rotation are manual.** pgcli does not rotate them; use `k3s certificate rotate-ca` or reinstall from a clean data directory.
- **No daemon integration.** Nodes rely on `--restart unless-stopped` for self-healing and on `pg start --autostart` for boot order, like the other add-ons.

## Metrics server

K3s bundles the **metrics-server** and starts it by default; the add-on
deploys nothing extra. It serves the resource metrics API (`metrics.k8s.io`),
which backs `kubectl top` and horizontal pod autoscaling. Metrics are scraped
from every kubelet, so servers and agents are covered alike.

```bash
pg k3s kubectl --name k3s -- get pods -n kube-system -l k8s-app=metrics-server
pg k3s kubectl --name k3s -- top nodes
pg k3s kubectl --name k3s -- top pods -n kube-system
```

`top` needs the pod running and the aggregated API available
(`pg k3s kubectl --name k3s -- get apiservice v1beta1.metrics.k8s.io`); right
after cluster creation it can take about a minute before the first samples
exist. Disable the component with `--disable metrics-server` (Ansible: add
`metrics-server` to `addon_k3s_disable`) when HPA is not needed or another
metrics stack is in use — `kubectl top` stops working afterwards.

## Traefik ingress

The complete manifests are in the repository:
`misc/k3s/hi-server-deployment.yaml` (the Deployment) and
`misc/k3s/hi-server-ingress.yaml` (the Service and Ingress), both in the
`default` namespace.

```bash
pg k3s kubectl --name k3s -- apply -f - < misc/k3s/hi-server-deployment.yaml
pg k3s kubectl --name k3s -- apply -f - < misc/k3s/hi-server-ingress.yaml
curl http://<any-node-ip>/hi/world
```

K3s also bundles **Traefik v3** as the default ingress controller and starts it
with the cluster, so in-cluster Services can be published without any extra
add-on. Traefik runs as a single-replica Deployment in `kube-system`, and
ServiceLB publishes its `LoadBalancer` Service on ports 80/443 of **every**
node — whichever node the Traefik pod itself runs on.

```bash
pg k3s kubectl --name k3s -- -n kube-system get deploy,svc traefik
pg k3s kubectl --name k3s -- -n kube-system get pods -l app.kubernetes.io/name=traefik
```

Routes come from standard `Ingress` objects (with `ingressClassName: traefik`)
or from the Traefik CRDs the bundle installs (`IngressRoute`, `Middleware`,
`TLSOption`, ...). A minimal Service plus Ingress publishes `/hi`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: hi-server
  namespace: default
spec:
  selector:
    app: hi-server
  ports:
    - name: http
      port: 8080
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: hi-server
  namespace: default
spec:
  ingressClassName: traefik
  rules:
    - http:
        paths:
          - path: /hi
            pathType: Prefix
            backend:
              service:
                name: hi-server
                port:
                  name: http
```

A rule without `host` matches every hostname — the quickest way to try a route
by node IP. Pin routes to a name with `host: app.example.com` (optionally plus
`path`) to split several apps across the same entry points. The same route as a
Traefik CRD:

```yaml
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: hi-server
  namespace: default
spec:
  entryPoints: [web]
  routes:
    - match: Host(`hi.example.com`) && PathPrefix(`/hi`)
      kind: Rule
      services:
        - name: hi-server
          port: 8080
```

To pin the route to a hostname and terminate TLS, add `host` rules and a
`kubernetes.io/tls` secret. Without a CA at hand, `pg cert` mints a self-signed
pair whose SANs cover the hostname (the leaf is its own trust anchor — add the
`.crt` to the clients that should trust it):

```bash
pg cert --host hi.example.com \
  --cert-file hi.example.com.crt --key-file hi.example.com.key
```

Then create the secret from those files with `pg k3s kubectl`. kubectl runs
inside the node container and cannot read host paths, so embed the files as
base64 data in a manifest piped through stdin:

```bash
pg k3s kubectl --name k3s -- apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: hi-server-tls
  namespace: default
type: kubernetes.io/tls
data:
  tls.crt: $(base64 -w0 hi.example.com.crt)
  tls.key: $(base64 -w0 hi.example.com.key)
EOF
```

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: hi-server-tls
  namespace: default
spec:
  ingressClassName: traefik
  tls:
    - hosts: [hi.example.com]
      secretName: hi-server-tls   # omit secretName for Traefik's self-signed default
  rules:
    - host: hi.example.com
      http:
        paths:
          - path: /hi
            pathType: Prefix
            backend:
              service:
                name: hi-server
                port:
                  name: http
```

The domain must resolve to a node IP. If you are using your own domain, add A
records pointing it at the cluster's agent nodes **before going live** (one A
record per agent; ServiceLB publishes Traefik on every node, so multiple
records give round-robin and redundancy). This variant is also in the
repository as `misc/k3s/hi-server-ingress-tls.yaml`; as a separate object it
coexists with the plain Ingress, and Traefik prefers the longer Host+Path rule
for `hi.example.com` while other hostnames keep matching the plain rule. HTTP
is not redirected to HTTPS by
default; opt in with a redirect middleware and an annotation on the Ingress:

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: redirect-https
  namespace: default
spec:
  redirectScheme:
    scheme: https
    permanent: true
```

The annotation goes on the Ingress itself: Traefik attaches that Ingress's
routers to both **web (80)** and **websecure (443)** by default, so requests
arriving on 80 get a 301 to https while requests already on 443 pass through.
The annotation is **commented out** here (port 80 stays directly reachable);
uncomment it to enforce the redirect:

```yaml
metadata:
  annotations:
    # Uncomment to 301 port-80 requests to HTTPS (this Ingress's routers listen
    # on both 80 and 443).
    # traefik.ingress.kubernetes.io/router.middlewares: default-redirect-https@kubernetescrd
```

Test with `--resolve` before the DNS record is in place:

```bash
curl -k --resolve hi.example.com:443:10.0.0.11 https://hi.example.com/hi/world
```

The dashboard is enabled in the bundled controller but not published through a
Service; reach it with a port-forward:

```bash
pg k3s kubectl --name k3s -- -n kube-system port-forward deploy/traefik 8080:8080
# open http://127.0.0.1:8080/dashboard/
```

Disabling the bundle (`--disable traefik`, Ansible `addon_k3s_disable: [traefik]`)
frees 80/443 but makes `Ingress` objects inert — publish Services with
`NodePort` or ServiceLB instead.
