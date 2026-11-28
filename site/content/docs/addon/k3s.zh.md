---
title: "K3s"
description: "以托管插件方式运行 K3s（轻量 Kubernetes）——单机或 HA server/agent 集群，rootful Podman 容器 + host 网络，支持自定义 CA、containerd registry mirror 与 kubectl/kubeconfig 透传；仅 Linux rootful"
weight: 71
---

`pg addon install k3s` 以托管插件方式运行一个 **K3s** 节点。它是**顶层基础设施插件**
（不代理某个 PostgreSQL 实例）：每台主机最多一个节点，跑在 **rootful Podman 容器**里，
使用 host 网络与 `--privileged --userns=host --cgroupns=host`，与 K3s 官方容器安装形态一致。
两种角色都支持，因此既能建单机集群，也能用 `server`/`agent` 成员组建多主机 HA 集群
（嵌入式 etcd）。

**运行要求：** Linux + **rootful podman**。节点容器需要 host 网络、privileged 模式与
host cgroup namespace，rootless podman 与其他平台都做不到；不满足时 pgcli 直接拒绝安装。
host 网络端口必须空闲：

| 角色 | TCP | UDP |
|---|---|---|
| `server` | 6443（API）、2379/2380（etcd）、10250（kubelet） | 8472（VXLAN） |
| `agent` | 10250（kubelet） | 8472（VXLAN） |

80/443 只在被占用时给出警告：K3s 自带的 Traefik 与 ServiceLB 启动后会占用它们，与同机的
`nginx`/`safeline` 冲突——若确实如此，用 `--disable traefik`/`--disable servicelb` 关掉。

每台主机只能有一个节点：API、etcd、kubelet 端口固定，因此 `pg.yaml` 只允许一个
`addons.k3s` 条目，第二个直接报错。

## 参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--name` | `k3s` | 节点名（map key、容器名后缀、`pg k3s ...` 默认 `--name`） |
| `--version` | `v1.37.2-k3s1` | K3s 发行 tag，拉取 `docker.io/rancher/k3s:<tag>`。上游写作 `v1.37.2+k3s1`——Docker tag 用 `-`，含 `+` 会被拒绝 |
| `--image` | | 完整镜像引用覆盖（优先于 `--version`，两者互斥） |
| `--role` | `server` | `server` 或 `agent` |
| `--cluster-init` | false | 仅 server：用**嵌入式 etcd** 引导 HA 集群的首台 server（单机 server 不传它，用 SQLite） |
| `--server` | | 加入已有集群：`https://<first-server>:6443`。与 `--cluster-init` 互斥；agent 必填 |
| `--token-file` | | **每个节点必填**：存放集群 token 的宿主文件，只读挂载到 `/run/k3s-token` |
| `--node-name` | 宿主 hostname | Kubernetes 节点名（`--node-name`）；集群内必须唯一 |
| `--advertise-address` | | 仅 server：对端与客户端访问本节点的地址；也会写进导出的 kubeconfig |
| `--tls-san` | | 仅 server：API 证书的额外 TLS SAN（可重复） |
| `--disable` | | 仅 server：关闭自带组件，如 `traefik`、`servicelb`、`metrics-server`（可重复） |
| `--cluster-cidr` | `10.42.0.0/16` | 仅 server：Pod 网段（`--cluster-cidr`）；逗号分隔的 IPv4/IPv6 对表示双栈，必须在建集群时设置；所有 server 必须一致 |
| `--service-cidr` | `10.43.0.0/16` | 仅 server：Service VIP 网段（`--service-cidr`）；双栈与所有 server 一致的要求同上 |
| `--kube-proxy-arg` | | 传给内嵌 kube-proxy 的自定义参数，如 `proxy-mode=nftables`（K8s 1.31+，可重复；kube-proxy 在每个节点都运行） |
| `--ca-dir` | | 自定义 CA 目录，**仅初始 server** 且**仅在其首启之前**；见[自定义 CA](#自定义-ca) |
| `--registry-mirror` | | containerd registry mirror `REGISTRY=ENDPOINT`（可重复，如 `docker.io=https://<id>.mirror.aliyuncs.com`）；与 `--registries-file` 互斥 |
| `--registries-file` | | 完整 `registries.yaml`（可含 auth/TLS）；与 `--registry-mirror` 互斥 |
| `--data-dir` | `<base_dir>/addon/k3s/<name>/data` | K3s 数据目录（容器内 `/var/lib/rancher/k3s`） |
| `--kubelet-dir` | `<base_dir>/addon/k3s/<name>/kubelet` | kubelet 根目录（容器内 `/var/lib/kubelet`） |
| `--cni-dir` | `<base_dir>/addon/k3s/<name>/cni` | CNI 状态目录（容器内 `/var/lib/cni`） |
| `--log-dir` | `<base_dir>/addon/k3s/<name>/log` | 挂载到容器 `/var/log` 的宿主目录 |
| `--log-file` | | 把 K3s server/agent 日志另写到该宿主文件（`--log /var/log/k3s.log --alsologtostderr`） |
| `--container-log-file` | | 把 Podman 的 `k8s-file` 容器日志固定到该宿主文件（默认用 Podman 自己的日志存储） |
| `--force` | false | 按当前配置重建节点容器、保留宿主数据；修改参数或 registry mirror 后必须用它生效 |

`--dsn` 与 `--pg-name` 不适用（K3s 直接用 host 网络），传入会被拒绝。

宿主挂载关系：

| 宿主路径 | 容器路径 |
|---|---|
| `--data-dir` | `/var/lib/rancher/k3s` |
| `--kubelet-dir` | `/var/lib/kubelet` |
| `--cni-dir` | `/var/lib/cni` |
| `--log-dir` | `/var/log` |
| `--log-file` | `/var/log/k3s.log` |
| `--token-file` | `/run/k3s-token`（只读） |
| 生成/用户的 `registries.yaml` | `/run/k3s-registries.yaml`（只读） |

## kube-proxy 与 nftables

`--kube-proxy-arg` 把自定义参数透传给内嵌 kube-proxy。Kubernetes 1.31+（本版本为
v1.37）可用 `--kube-proxy-arg proxy-mode=nftables` 把 Service/NodePort 规则从
KUBE-* iptables 链改为原生 `ip`/`ip6` `kube-proxy` nft 表。flannel 和内置 CNI 插件仍调用
iptables 前端；Debian 12 上该前端由 nftables 提供（`iptables-nft`），规则最终仍写入
nf_tables；只有 alternatives 指向 `iptables-legacy` 的主机才是真 legacy。

在运行中的集群上切换 proxy mode 是有损操作（应在安装时指定，或用 `--force` 重建
节点），且 Kubernetes 1.38 之前的 nftables 模式不支持 127.0.0.1 上的 NodePort
（KEP-6032）。

## 单机安装

**SQLite 单机安装仅用于本地测试与开发。** 生产环境应使用下面的 HA 集群形态（嵌入式 etcd，至少三台 server）。

```bash
# 每个节点都需要预生成的 token：否则 K3s 会在首启时自造一个
# 集群 token，其他成员无法加入。
openssl rand -hex 32 > /tmp/k3s-token
chmod 600 /tmp/k3s-token

pg addon install k3s --name k3s --role server \
  --token-file /tmp/k3s-token \
  --advertise-address 10.0.0.11
```

安装会按需拉取镜像、创建目录、在配置了 mirror 时渲染 `registries.yaml`、启动容器并保存
`pg.yaml`。它**不会阻塞等到集群就绪**——K3s 在后台引导：

```bash
pg k3s kubectl --name k3s -- get --raw=/readyz
pg k3s kubectl --name k3s -- get nodes -o wide
```

> **kubectl 的 flag 要放在 `--` 之后。** 参数先经过 pg 自己的解析：`-o` 是 pg 的全局
> config 输出参数，`-n` 它不认识。`--` 之后的参数原样交给 kubectl。

扩容：**agent 可以加入，server 不能**。

- **agent 可加入**：SQLite 单机 server 之后可以随时添加 agent——以 `--role agent
  --server https://<server>:6443` 加入同一个 token 即可：

  ```bash
  # 在 agent 主机上，token 文件与 server 相同：
  pg addon install k3s --name k3s --role agent \
    --server https://10.0.0.11:6443 \
    --token-file /tmp/k3s-token
  ```

  加入后在 server 上确认节点已 `Ready`：

  ```bash
  pg k3s kubectl --name k3s -- get nodes -o wide
  ```
- **server 不可加入**：多 server 需要嵌入式 etcd，无法直接加入 SQLite 单机。K3s 官方
  支持把现有 SQLite 单机原地转换为嵌入式 etcd：以 `--cluster-init` 重启该 server 即可
  （在 pgcli 里用 `--force` 以新参数重建容器、保留数据），之后就能按 HA 集群的方式
  继续加入 server。不确定之后是否会扩容时，直接按 HA 集群形态安装首台 server
  （带 `--cluster-init`；Ansible 组始终如此）。

## HA 集群

HA 集群 = 首台 server 传 `--cluster-init`（嵌入式 etcd），其余 server 用 `--server` 加入，
agent 也加入同一个 API。所有节点必须使用同一个 token。

```bash
# 1. 在控制机生成一个 token，复制到每个节点（权限 0600）。
openssl rand -hex 32 > /tmp/k3s-token && chmod 600 /tmp/k3s-token

# 2. 首台 server：引导嵌入式 etcd 数据存储。
pg addon install k3s --name k3s --role server --cluster-init \
  --token-file /tmp/k3s-token \
  --advertise-address 10.0.0.11 \
  --tls-san 10.0.0.11 --tls-san 10.0.0.12 --tls-san 10.0.0.13

# 3. 其余 server：加入首台（不传 --cluster-init）。
pg addon install k3s --name k3s --role server \
  --server https://10.0.0.11:6443 \
  --token-file /tmp/k3s-token \
  --advertise-address 10.0.0.12 \
  --tls-san 10.0.0.11 --tls-san 10.0.0.12 --tls-san 10.0.0.13

# 4. agent：加入同一个 API。
pg addon install k3s --name k3s --role agent \
  --server https://10.0.0.11:6443 \
  --token-file /tmp/k3s-token
```

等首台 server 的 `/readyz` 通过后再启动加入者。`--tls-san` 列出所有 server 地址，客户端
无论打到哪台 server，API 证书都有效；agent 地址不需要——agent 既不提供 API 也不运行
etcd。`pg k3s kubectl --name k3s -- get nodes` 中 server 显示
`control-plane`，agent 显示 `<none>`（role 标签在节点注册后几秒才写入）。

**Pod / Service 网段。** 只有当默认网段（`10.42.0.0/16` 与 `10.43.0.0/16`）与现有网络
冲突时才需要改 `--cluster-cidr` / `--service-cidr`。所有 server 必须传相同的值——网段
不一致的 server 无法加入——且只在建集群时生效。双栈在初始 server 上传逗号分隔的
IPv4/IPv6 对，例如
`--cluster-cidr 10.42.0.0/16,2001:db8:42::/56 --service-cidr 10.43.0.0/16,2001:db8:43::/112`。

Ansible 的组 `addon_k3s_cluster` 始终组建**集群模式**（嵌入式 etcd），不会部署 SQLite
单机：初始 server 总是带 `--cluster-init`，即使组里只列一台主机——那是一个单成员 etcd
集群，加主机即扩容；只有上面的命令行单机示例（不传 `--cluster-init`）才使用 SQLite。
在组里列出任意数量主机，用 `addon_k3s_roles` 显式声明每台的 `server`/`agent` 角色
（未列出的默认 agent，角色与主机顺序无关），再用 `addon_k3s_cluster_init_server`
指定唯一一台以 `--cluster-init` 引导嵌入式 etcd 的 server（留空取角色表里的第一个
server，它不必是组内第一台主机）。token 与可选 CA 由 `k3s_shared` role 分发，play 为
`serial: 1`；先于初始 server 启动的加入节点会持续重试，直到其 API 可用，之后往组里
加主机重跑剧本即自动加入——见 [用 Ansible 部署插件](../ansible/addons.md) 与
[插件集群章节](../ansible/addon-cluster.md)。

**NAT 网络下的 peer 校验。** etcd 3.7 会用观测到的源地址校验加入节点的证书。节点间流量
经过 NAT（例如实验室宿主机对 VM 互访做 SNAT）时，join 会报
`tls: "<观测到的地址>" does not match any of the DNSNames`。Ansible 分组里每台 server
的 `--tls-san` 包含 server 的 IP，其余地址通过 `addon_k3s_extra_tls_sans` 显式追加——
把 SNAT 源地址写进去，例如 `addon_k3s_extra_tls_sans: ["10.0.0.254"]`。直连的三层网络
（通常的实体服务器部署）除 server IP 外不需要任何额外 SAN。
直接用 `pg addon install` 时需自行把观测到的源地址作为额外的 `--tls-san` 传入。

## 自定义 CA

K3s 自带 **10 年自签 CA**，且不会自动轮换，也没有参数能改年限。要使用更长寿命的 CA，先用
K3s 官方脚本 [`contrib/util/generate-custom-ca-certs.sh`](https://github.com/k3s-io/k3s/blob/master/contrib/util/generate-custom-ca-certs.sh)
生成完整集合，再用 `--ca-dir` 传入：

```bash
DATA_DIR=/tmp/k3s-ca DAYS=36500 bash generate-custom-ca-certs.sh
find /tmp/k3s-ca -name '*.crt' -o -name '*.key'   # 查看目录结构
```

目录必须包含 K3s 要求的完整集合：

```text
server-ca.crt            server-ca.key
client-ca.crt            client-ca.key
request-header-ca.crt    request-header-ca.key
etcd/peer-ca.crt         etcd/peer-ca.key
etcd/server-ca.crt       etcd/server-ca.key
service.key
```

pgcli 会在**初始 server 首启之前**把它们复制到 `<data-dir>/server/tls`（key 权限 0600），
文件不全直接报错。它只对单机 server 或 `--cluster-init` server 有效；加入的 server 与 agent
从集群获得 CA。重复安装时，源 CA 与现有文件一致则跳过；不一致会拒绝——请用
`k3s certificate rotate-ca` 轮换，或清空数据目录重装（数据存储里已经写入了原始信任材料）。

Ansible role 可以为初始 server 生成并分发这套 CA：在
`group_vars/addon_k3s_cluster/main.yml` 里设 `addon_k3s_ca_days: 36500`
（0 表示沿用 K3s 默认；自定义年限须大于 10 天）。

## Registry mirror 与 air-gap

containerd 只在节点启动时读取一次 mirror 配置。支持两种互斥形式：

```bash
# 渲染形式：pgcli 写 <base>/addon/k3s/<name>/registries.yaml（0600）。
pg addon install k3s ... \
  --registry-mirror docker.io=https://<id>.mirror.aliyuncs.com

# 完整形式：你自己的 registries.yaml，可含 auth/TLS 段。
pg addon install k3s ... --registries-file /etc/k3s/registries.yaml
```

文件只读挂载到 `/run/k3s-registries.yaml`，并以 `--private-registry` 传给 K3s。修改 mirror
需要重建容器（保留宿主数据）才能生效：

```bash
pg addon install k3s ... --registry-mirror docker.io=https://new-mirror.example.com --force
```

mirror 只影响 **Pod 镜像**——K3s 节点镜像本身由 Podman 拉取（用 `pg mirror` 单独配置，
或用 Ansible 的离线镜像预载 tarball）。真正 air-gap 的集群还需要 K3s 的
**airgap-images tarball**（pause、coredns、traefik、metrics-server、
local-path-provisioner 等），放到 containerd 能导入的位置。

## kubectl 与 kubeconfig

`pg k3s` 通过 `podman exec` 驱动本机的节点容器；宿主无需安装任何东西。

```bash
pg k3s kubectl get nodes                          # 唯一配置的节点
pg k3s kubectl -- get nodes -o wide
pg k3s kubectl -- get pods -n kube-system
pg k3s kubectl -- apply -f - < manifest.yaml
pg k3s kubectl --name k3s get pods                # 多个节点：指定其一
pg k3s kubectl --kubeconfig kubeconfig.yaml -- get nodes   # 远程集群
```

`kubectl` 与 `kubeconfig` 仅限 server；agent 会被拒绝。配置了多个节点时必须用 `--name`
选择 server。stdin 已接上，因此 `kubectl apply -f -` 可用。kubectl 在节点容器内执行，
看不到宿主路径——清单要通过 stdin 管道（`apply -f - < file.yaml`），不能直接传宿主的
`-f` 路径。

```bash
pg k3s kubeconfig --name k3s > kubeconfig.yaml       # 输出到 stdout
pg k3s kubeconfig --name k3s --output ~/.kube/config # 权限 0600
```

server 安装时若给了 `--advertise-address`，kubeconfig 里的 loopback API 地址
（`https://127.0.0.1:6443`）会被改写成该地址，文件可直接在其他主机使用。在非 K3s
节点的主机上把导出的 kubeconfig 传给 `--kubeconfig`：pg 用 K3s 镜像（pgcli 默认 tag，
缺失时自动拉取到 Podman）起一个一次性容器执行 kubectl；容器不走宿主代理、直连 API
server，因此只要主机有 Podman，无需安装 kubectl 软件包即可操作任意集群。

## 生命周期

```bash
pg addon list                          # "Infra add-ons (k3s):" 段
pg addon start k3s --name k3s          # 启动容器（不存在则重建）
pg addon stop k3s --name k3s           # 停止，保留数据
pg logs addon k3s --name k3s -f        # 容器日志
pg addon remove k3s --name k3s         # 删除容器与配置条目
pg addon remove k3s --name k3s --clean-data   # 同时删除 data/kubelet/cni/log
```

`--clean-data` 删除 data、kubelet、CNI、log 四个目录以及 pgcli 渲染的 `registries.yaml`；
用户提供的 token/CA/registries 原文件永不删除。

autostart 走已有的容器启动路径：

```bash
pg autostart enable --k3s --name k3s
pg autostart list
pg start                # 按顺序启动 autostart 的 K3s 节点：cluster-init server、加入的 server、单机 server、agent
```

`pg start` 按该顺序启动节点容器，但不等待集群就绪。多主机集群请先在初始 server 所在主机上
执行 `pg start`。

## 移除节点

移除 agent：

```bash
# 在任一健康的 server 上：封锁调度并删除 Node 对象。
pg k3s kubectl --name k3s -- drain node4 --ignore-daemonsets --delete-emptydir-data
pg k3s kubectl --name k3s -- delete node node4
# 在被移除的主机上：删除容器与数据。
pg addon remove k3s --name k3s --clean-data
```

移除 server，在存活的 server 上操作：

```bash
# 1. 趁被移除节点还健康时先驱逐工作负载。
pg k3s kubectl --name k3s -- drain node3 --ignore-daemonsets --delete-emptydir-data

# 2. 趁所有成员都在运行时摘掉它的 etcd 成员：成员移除是一条 raft 记录，需要
#    当前成员的法定人数，而偶数成员（比如两台 server）在少一台时就已失去法定
#    人数。滞留在成员列表里的节点即使主机下线也仍计入法定人数。K3s 的内嵌
#    etcd 启用了客户端证书认证，需带上 K3s 写入数据目录的证书。
TLS=/data/pg/addon/k3s/k3s/data/server/tls/etcd   # <data-dir>/server/tls/etcd
export ETCDCTL_ENDPOINTS=https://127.0.0.1:2379
pg etcdctl --ca $TLS/server-ca.crt \
  --cert $TLS/server-client.crt --key $TLS/server-client.key member list
pg etcdctl --ca $TLS/server-ca.crt \
  --cert $TLS/server-client.crt --key $TLS/server-client.key member remove <member-id>

# 3. 删除 Node 对象。
pg k3s kubectl --name k3s -- delete node node3
```

然后在被移除的主机上执行 `pg addon remove k3s --name k3s --clean-data`。
`--clean-data` 必不可少：角色和成员关系都保存在数据目录里，残留目录会让之后的重装
以旧身份加入。由 Ansible 管理的集群还要按
[add-on 集群章节](../ansible/addon-cluster.md) 的说明把主机从组 inventory 和
`addon_k3s_roles` 中删掉。

## 限制

- **每台主机一个节点。** host 网络端口固定（6443/2379/2380/10250/8472），无法跑第二个节点；
  更多节点请加主机。
- **角色与成员关系属于数据目录。** 修改 `role` 或改投其他集群需要 `--clean-data` 删除
  节点后重装。唯一受上游支持的原地变更，是把 SQLite 单机 server 转换为嵌入式 etcd：
  以 `--cluster-init` 重启（`--force` 重建容器、保留数据），之后即可加入更多 server。
- **Traefik/ServiceLB 会占用宿主 80/443。** 当 nginx/safeline/其他 Web 插件占用这些端口时，
  用 `--disable traefik`（保留 ServiceLB 服务 LoadBalancer）或 `--disable servicelb`。
- **token 与 CA 轮换需要手动。** pgcli 不自动轮换；用 `k3s certificate rotate-ca`，或从干净
  数据目录重装。
- **不接入 pg daemon。** 与其他插件一样，节点依靠 `--restart unless-stopped` 自愈、依靠
  `pg start --autostart` 控制开机顺序。

## metrics-server

K3s **内置** metrics-server 且默认启用，本插件不需要额外部署。它提供 resource
metrics API（`metrics.k8s.io`），是 `kubectl top` 与 HPA 的数据来源；指标由每个
节点的 kubelet 采集，server 和 agent 都被覆盖。

```bash
pg k3s kubectl --name k3s -- get pods -n kube-system -l k8s-app=metrics-server
pg k3s kubectl --name k3s -- top nodes
pg k3s kubectl --name k3s -- top pods -n kube-system
```

`top` 需要 Pod 处于 Running 且聚合 API 可用
（`pg k3s kubectl --name k3s -- get apiservice v1beta1.metrics.k8s.io`）；集群刚
建好时可能要等一分钟左右才有首批采样数据。不需要 HPA 或已有其他监控栈时可用
`--disable metrics-server` 关闭（Ansible：在 `addon_k3s_disable` 里加
`metrics-server`），关闭后 `kubectl top` 不可用。

## Traefik（Ingress）

完整示例在仓库里：`misc/k3s/hi-server-deployment.yaml`（Deployment）与
`misc/k3s/hi-server-ingress.yaml`（Service 与 Ingress），都部署在 `default`
namespace。

```bash
pg k3s kubectl --name k3s -- apply -f - < misc/k3s/hi-server-deployment.yaml
pg k3s kubectl --name k3s -- apply -f - < misc/k3s/hi-server-ingress.yaml
curl http://<任一节点 IP>/hi/world
```

K3s 还**内置 Traefik v3** 作为默认 Ingress 控制器，随集群一起启动，无需额外插件
即可对外发布集群内 Service。Traefik 以单副本 Deployment 跑在 `kube-system`，它的
`LoadBalancer` Service 由 ServiceLB 发布到**每台节点**的 80/443 端口——与 Traefik
Pod 本身调度在哪台节点无关。

```bash
pg k3s kubectl --name k3s -- -n kube-system get deploy,svc traefik
pg k3s kubectl --name k3s -- -n kube-system get pods -l app.kubernetes.io/name=traefik
```

路由来自标准 `Ingress` 对象（设置 `ingressClassName: traefik`），或内置的 Traefik
CRD（`IngressRoute`、`Middleware`、`TLSOption` 等）。最小的 Service + Ingress
示例把 `/hi` 转发到 8080 端口的负载：

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

不写 `host` 的规则匹配所有主机名，按节点 IP 直接访问最省事；用
`host: app.example.com`（可再加 `path`）把路由固定到域名，让多个应用共享同一组
入口。等价的 IngressRoute CRD 写法：

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

把路由固定到域名并启用 TLS：增加 `host` 规则和 `kubernetes.io/tls` Secret。没有现成
CA 时用 `pg cert` 生成一张自签叶证书（SAN 覆盖域名；叶证书本身就是信任锚，需要信任
它的客户端导入该 `.crt` 即可）：

```bash
pg cert --host hi.example.com \
  --cert-file hi.example.com.crt --key-file hi.example.com.key
```

再用 `pg k3s kubectl` 从这两个文件创建 Secret。kubectl 在节点容器内执行，看不到宿主
文件，因此把证书内容以 base64 内嵌进清单、经 stdin 传入：

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
      secretName: hi-server-tls   # 省略 secretName 则用 Traefik 自签默认证书
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

域名需解析到任一节点 IP。该变体同样放在仓库的
`misc/k3s/hi-server-ingress-tls.yaml`；它是独立对象，与普通 Ingress 共存——
`hi.example.com` 由更长的 Host+Path 规则优先匹配，其他主机名仍走普通规则。
HTTP 默认不跳转 HTTPS，需要跳转时加重定向 Middleware
并在 Ingress 上加注解：

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

注解挂在 Ingress 上即可：Traefik 给该 Ingress 生成的路由默认同时监听 **web（80）** 与
**websecure（443）**，所以 80 上收到请求时返回 301 跳到 https，443 上已经加密则原样
放行。这里默认**注释掉**注解（80 保持可直连），需要强制跳转时取消注释：

```yaml
metadata:
  annotations:
    # 开启后，80 上的请求会 301 跳到 HTTPS（该 Ingress 的路由同时监听 80/443）。
    # traefik.ingress.kubernetes.io/router.middlewares: default-redirect-https@kubernetescrd
```

DNS 还没就绪时用 `--resolve` 验证：

```bash
curl -k --resolve hi.example.com:443:10.0.0.11 https://hi.example.com/hi/world
```

dashboard 在内置控制器里已启用，但没有发布成 Service，可用 port-forward 访问：

```bash
pg k3s kubectl --name k3s -- -n kube-system port-forward deploy/traefik 8080:8080
# 浏览器打开 http://127.0.0.1:8080/dashboard/
```

关闭内置组件（`--disable traefik`，Ansible 里 `addon_k3s_disable: [traefik]`）会
释放 80/443，但 `Ingress` 对象随之失效——改用 `NodePort` 或 ServiceLB 发布服务。
