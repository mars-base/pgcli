---
title: "Etcd"
description: "以 pgcli addon 形式运行独立的 etcd 集群，用于 HA / DCS 场景"
weight: 30
---

etcd 是一个分布式键值存储。pgcli 可以将一个或多个 etcd 成员作为**独立的顶层
addon** 运行 —— 它是共享基础设施，而非绑定某个实例的 sidecar。常见用途是为
PostgreSQL 高可用栈充当 DCS（Distributed Concurrent Store），或作为通用的配置 /
锁服务。

> **安全提示：** pgcli 管理的 etcd 成员**默认不启用 TLS 与认证** —— 任何能连到
> client 端口的客户端都拥有完整读写权限，因此默认仅绑定回环、并把端口限制在受信
> 网络内。传入 `--tls` 可加密 client 监听器并对 raft peer 做双向认证，传入
> `--auth` 则要求用户名/密码。目前仍没有多用户 / RBAC 管理：`--auth` 只创建一个
> 拥有 `root` 角色的用户。详见 [TLS 与认证](#tls-与认证)。

成员通过一次次 `pg addon install etcd` 管理：第一个成员引导（bootstrap）集群，
后续成员动态加入同名的集群 —— 可以在同一台机器，也可以跨机器。

## 工作原理

- **共享基础设施：** etcd 存放在 `pg.yaml` 顶层的 `addons.etcd` map 中，以成员名
  为 key，不隶属于任何单个实例。
- **host 网络 + 可配置监听：** 每个成员以 `--network host` 运行。默认情况下
  client/peer URL 绑定到 `127.0.0.1` —— 未启用 `--tls`/`--auth` 时任何可达的
  客户端都拥有完整权限，因此单机集群默认只回环暴露。跨机集群中每个成员通过
  `--advertise-host` 广播一个对端可达的地址，同时**仍然**监听回环，本机 etcdctl
  与远端 peer 都能访问。
- **可选 TLS 与认证：** `--tls` 同时覆盖两个接口（client HTTPS + raft peer
  mTLS），`--auth` 增加一个 root 角色的用户名/密码。两者都是集群级属性，在
  bootstrap 时确定 —— 见 [TLS 与认证](#tls-与认证)。
- **动态成员管理：** 第一个成员以 `--initial-cluster-state new` 启动；每个后续成员
  先对一个运行中的对等成员执行 `etcdctl member add` 注册，再以
  `--initial-cluster-state existing` 启动。pgcli 自动完成这些步骤 —— 同机走成员
  容器，跨机走临时 etcdctl 容器。
- **集群身份：** `--cluster` 值相同的成员加入同一个 etcd 集群（对应 etcd 的
  `--initial-cluster-token`）。默认值为 `pgcli-etcd`。
- **内置调优：** 每个成员启动时都带周期性压缩（`--auto-compaction-mode periodic`、
  `--auto-compaction-retention 24h`）以及 8 GiB 后端配额
  （`--quota-backend-bytes 8589934592`）—— 对于小型 HA 元数据存储是合理的默认值。

## 部署形态

pgcli 支持两种集群形态，使用的安装命令完全相同 —— 区别只在于成员是否共用一台
主机。

### 单机 —— 所有成员在同一台机器（测试 / 开发）

开发机或 CI 的经典布局：每个成员用不同的自动分配端口监听回环。无需
`--advertise-host`（默认 `127.0.0.1`），无需改防火墙，主机之外什么都访问不到。

```bash
pg addon install etcd --name m1              # 127.0.0.1:2379/2380
pg addon install etcd --name m2              # 127.0.0.1:2381/2382
pg addon install etcd --name m3              # 127.0.0.1:2383/2384
```

三个成员同属 `pgcli-etcd` 集群；m1 引导，m2/m3 动态加入。这能体验真正的 3 节点
raft 语义，但没有任何主机隔离 —— 整集群随一台机器一起消失，正因如此它只是
*测试*形态。

### 跨主机 —— 每台机器一个成员（生产 HA）

生产形态：把成员分散到不同机器（最好 3 或 5 个奇数个 —— 见拓扑与 Quorum）。
每个成员广播自己主机的 LAN 地址；第一个成员 bootstrap 时**必须**带
`--advertise-host`，远端 peer 才拨得通。端口可以在各主机上重复，因为每台只绑
自己的网卡。

```bash
# 主机 A (10.0.0.1) — 引导
pg addon install etcd --name m1 --cluster prod \
  --advertise-host 10.0.0.1 --client-port 2379 --peer-port 2380

# 主机 B (10.0.0.2) — 加入
pg addon install etcd --name m2 --cluster prod \
  --advertise-host 10.0.0.2 --client-port 2379 --peer-port 2380 \
  --join http://10.0.0.1:2379

# 主机 C (10.0.0.3) — 加入
pg addon install etcd --name m3 --cluster prod \
  --advertise-host 10.0.0.3 --client-port 2379 --peer-port 2380 \
  --join http://10.0.0.1:2379
```

要求：每个成员的 `--advertise-host` 互相可达（同一 LAN），每台主机的防火墙为
**所有成员的 client 与 peer 端口**向其它成员放行（peer 是双向通信 —— 例如 4 成员
默认端口集群放行 2379-2386/tcp），且所有成员使用相同的 `--cluster` token。任意
一台机器宕机后其余主机照常工作；集群只需要保住*机器*的多数派。

| | 单机 | 跨主机 |
|---|---|---|
| 适用 | 开发、CI、功能测试 | 生产 HA |
| `--advertise-host` | 省略（回环默认） | 必填，且每个成员都要 |
| `--join` | 不用 | 除第一个外每个成员都要 |
| 端口 | 同主机内每个成员互不相同 | 可重复，各主机绑各自网卡 |
| 防火墙 | 无需（仅回环） | 每个成员的 client+peer 端口，双向放行 |
| 能否扛单机故障 | 否 | 能（quorum 成立时） |

## TLS 与认证

`--tls` 与 `--auth` 相互独立、均为可选，且都是**在 bootstrap 时确定的集群级
属性**。同一集群的所有成员必须对两者取值一致：

- `--tls` 把 client 监听器变成 HTTPS、raft peer 监听器变成 mTLS。每个成员都持有
  由同一个集群 CA 签发的 leaf 证书，peer 之间用该 CA 互相校验证书。
- `--auth` 启用 etcd 的认证存储，创建一个（默认名为 `root`）拥有 `root` 角色的
  用户，然后打开认证开关。

不启用 `--tls` 时 `--auth` 仍然可用，但密码会以明文经过网络（pgcli 会打印警告）。
启用 `--tls` 后 client 流量被加密且服务端经过认证，客户端通过用户名/密码认证。

### 单机

```bash
pg addon install etcd --name m1 --tls --auth
```

首次安装时若省略 `--password`，pgcli 会生成一个密码并只打印一次：

```
  TLS:          on
  Cluster CA:   ~/.pgcli/tls/etcd/pgcli-etcd/ca.crt
  Auth:         on
  Auth user:    root
  Auth password: ********  (generated once; stored in pg.yaml)
  Retrieve later with: pg addon password etcd --name m1
```

之后可用 `pg addon password etcd --name m1` 取回。同一主机上的其它成员会自动
继承这两个设置（以及凭据）：

```bash
pg addon install etcd --name m2
```

想自己指定密码可传 `--password`（用户名用 `--root-user`）；这两个参数任一出现都
隐含 `--auth`。用新的 `--password` 重新 install 会轮换 root 用户的密码。

### 跨主机集群

第一个成员在本地生成 CA；加入的主机会自动从集群取回，无需手工复制证书文件：

```bash
# 主机 A (10.0.0.1) — 引导
pg addon install etcd --name m1 --cluster prod \
  --advertise-host 10.0.0.1 --tls --auth --password '<password>'

# 主机 B (10.0.0.2) — 通过 HTTPS 加入；自动取回 CA
pg addon install etcd --name m2 --cluster prod \
  --advertise-host 10.0.0.2 --join https://10.0.0.1:2379 \
  --tls --auth --password '<password>'
```

- `--join` 的 scheme 必须匹配：启用 `--tls` 用 `https://`，否则用 `http://`。
- 远端集群已启用认证时**必须**传 `--password`（用户名不是 `root` 时还要传
  `--root-user`）——取 CA 这一步本身就要用该凭据认证。缺少凭据会给出明确错误。
- 取 CA 采用首次使用即信任（TOFU）：仅该次交互跳过证书校验，之后所有流量都用
  取回的 CA 验证。

`--join` 能在本地没有 CA 的主机上自动取回 CA，前提是集群 CA 由 pgcli 生成。若
使用手动分发的 CA 或 BYO 证书，请自行把 CA 复制到每台主机（见下文）。

### 手动分发 CA（`pg cert --ca`）

也可以在安装前用 `pg cert --ca` 生成 CA，并分发到每台主机。CA 对必须落在
`<base_dir>/tls/etcd/<cluster>/` 下（`<base_dir>` 默认为 `~/.pgcli`）：

```bash
# 直接生成到目标目录（在任一主机上执行一次）
mkdir -p ~/.pgcli/tls/etcd/prod
pg cert --ca --host localhost,127.0.0.1 \
  --cert-file ~/.pgcli/tls/etcd/prod/ca.crt \
  --key-file  ~/.pgcli/tls/etcd/prod/ca.key

# 复制到其它主机的同一路径
scp ~/.pgcli/tls/etcd/prod/ca.crt ~/.pgcli/tls/etcd/prod/ca.key \
  hostB:~/.pgcli/tls/etcd/prod/
```

pgcli 从该目录读取 CA 对（私钥保持 0600）并原样复用；每个成员再获得由它签发的
leaf。**包括 bootstrap 主机在内**，每台主机都要在安装前放好同一对文件。此模式
**不会**把 CA 写入 etcd —— 由你自行分发。

当前版本 `pg cert --ca` 生成的 CA **不带扩展密钥用途（EKU）限制**，因此既能签发
服务端证书也能签发客户端证书（raft peer 会把成员证书作为客户端证书校验）。旧版
pgcli 生成的是仅 serverAuth 的 CA，peer mTLS 会拒绝这类证书链；现在 pgcli 会在
安装时提前拒绝并提示重新生成。

### 自带成员证书（BYO）

`--tls-cert` / `--tls-key` 使用外部签发的 leaf 证书（隐含 `--tls`）；可选的
`--tls-ca` 指定客户端校验用的 CA，自签证书时默认就是该证书本身。leaf 必须带
`clientAuth` EKU（或完全不设 EKU），因为 raft peer 会把它当客户端证书校验。BYO
材料不会被写入 etcd，CA 也不会被发布 —— 请自行分发。

### 哪些内容会被发布，以及风险

在自动生成证书模式（只传 `--tls`、没有 BYO 证书对）下，第一个成员会把集群 CA
对写入 etcd：`/_pgcli/pki/etcd/ca.crt` 与 `/_pgcli/pki/etcd/ca.key`，供其它主机
`--join` 时取回：

- 启用 `--auth` 之前，任何能连到 client 端口的人都能读到 CA **私钥**（发布发生在
  打开认证之前，因为发布动作本身不需要凭据）。启用认证后读取需要凭据。
- 拿到该私钥的人可以签发此集群信任的任意证书。请把 client 端口放在私有网络并
  对 2379 做防火墙限制；或者改用手动分发 `pg cert --ca` 的 CA / BYO 证书，完全
  避免写入 etcd。

只要发布了自动生成的 CA，安装摘要就会打印上述警告。

### 不兼容的消费方

Patroni 的 DCS 配置与 RabbitMQ 的 etcd peer discovery 仍以**明文 HTTP 且不带
凭据**访问。对它们使用的集群启用 `--tls` 或 `--auth` 会让它们失联。若 pgcli 在
本地配置中发现这类消费方（Patroni 集群的 etcd members/endpoints 匹配该成员，或
RabbitMQ 用它做 discovery），安装时会打印 `[!]` 警告。在这些集成支持 TLS/认证
之前，请让此类 etcd 集群保持明文并依赖网络隔离。

### 之后修改 TLS/认证

TLS 在成员生命周期内不可更改：其广播的 peer URL scheme 是集群成员记录的一部分。
切换已初始化成员的 TLS，或在同一集群混用 TLS/非 TLS 成员，都会被拒绝 —— 请销毁
并重建集群：

```bash
pg addon remove etcd --name m1 --clean-data
```

认证同样是集群级属性。要在已有明文集群上启用认证，先用 `--auth --password ...`
重新安装 bootstrapping 成员，再用 `--auth` 重新安装其余成员，让它们记录凭据
（同机成员自动继承，跨机成员必须传 `--password`）。一旦启用，停用认证同样需要
重建集群。

## 命令

### 创建第一个成员

```bash
pg addon install etcd --name m1
```

```
-> Bootstrapping etcd cluster...
  [OK] etcd container started

✓ etcd installed: "m1"
  Container:    pgcli-etcd-m1
  Cluster:      pgcli-etcd
  Data dir:     ~/.pgcli/addon/etcd/m1/data
  Client port:  2379
  Peer port:    2380
  Advertise:    127.0.0.1

  Client URL: http://127.0.0.1:2379
  Connect (etcdctl): ETCDCTL_ENDPOINTS=http://127.0.0.1:2379
```

client 端口从 **2379** 起分配，peer 端口取下一个空闲端口（**2380**），由
`etcd_start_port` 自动分配。

### 往同一集群添加成员

在 `m1` 运行时，安装相同 `--cluster` 的其它成员即加入该集群：

```bash
pg addon install etcd --name m2
pg addon install etcd --name m3
```

pgcli 会找到一个运行中的对等成员作为协调者，通过 `etcdctl member add` 注册
`m2`/`m3`，再以 `--initial-cluster-state existing` 启动它们。端口继续无冲突地自动
分配（`m2` → 2381/2382，`m3` → 2383/2384）。刚扩容的集群在 leader 重选期间会短暂
失去 quorum；pgcli 会自动重试成员操作，无需在安装之间手动等待。

使用不同的 `--cluster` 可让成员归属另一个独立的 etcd 集群。

### 跨机器添加成员

不同主机上的成员也能组成同一个集群。每个跨机成员都必须广播一个**对端可达**的
地址，用 `--advertise-host` 指定。在已有成员的主机上，bootstrap 时传入本机 LAN
地址：

```bash
# 主机 A (10.0.0.1)
pg addon install etcd --name m1 --cluster prod \
  --advertise-host 10.0.0.1 --client-port 2379 --peer-port 2380
```

然后在新主机上，把 `--join` 指向任一现有成员的 client endpoint。pgcli 通过该
endpoint 完成注册（借助临时 etcdctl 容器 —— 本机无需已有成员容器或镜像，镜像会
按需拉取），取响应中的权威 `ETCD_INITIAL_CLUSTER`，再启动成员：

```bash
# 主机 B (10.0.0.2)
pg addon install etcd --name m2 --cluster prod \
  --advertise-host 10.0.0.2 --client-port 2379 --peer-port 2380 \
  --join http://10.0.0.1:2379
```

```
-> Registering member with the cluster at http://10.0.0.1:2379...
-> Starting etcd container (joining cluster)...
  [OK] etcd container started
```

跨机集群注意事项：

- **每个**成员都需要可达的 `--advertise-host`，第一个也不例外。它广播的 peer
  URL 会进入集群成员列表，用默认 `127.0.0.1` 启动的首个成员永远无法被其它主机
  加入。计划跨机组网时，bootstrap 时就要传 LAN 地址。
- `--join` 必须搭配 `--cluster`，且取值要与远端集群的 token 一致 —— 成员注册
  由远端集群校验，而非本地配置。
- `--join` 必须搭配 `--advertise-host`；否则其它成员会注册一个拨不通的 peer URL。
- 端口可以跨主机重复（两台机器都用 `2379/2380`），因为各主机只绑定自己的网卡。
- 成员间 client/peer 端口必须互通 —— **为每个成员的端口、双向放行防火墙**。peer
  之间是双向互连（都拨对方的 peer 端口），client（以及 `--join`/`pg etcdctl`）需要
  能访问 client 端口。自动分配的端口每个成员不同，从安装输出或 `pg addon list` 里
  读取实际端口（或用 `--client-port`/`--peer-port` 固定），按范围放行 —— 例如 4
  成员默认端口集群放行 `2379-2386/tcp`。漏配防火墙正是成员长时间
  `etcdserver: no leader`、或 `--join` 超时的常见原因。
- 对已注册的名字重复执行相同的 `--join` 安装会直接报错；先用
  `pg etcdctl member remove <hex-id>` 从集群注销。

### 用 `pg etcdctl` 查看

`pg etcdctl` 从短生命周期容器里运行 etcd 官方客户端 —— 无需钻进
成员容器（本机一个成员都没有时也能用，镜像按需拉取）。目标端点取自
`ETCDCTL_ENDPOINTS`，未设置时回退到配置中的第一个成员：

```bash
export ETCDCTL_ENDPOINTS=http://10.0.0.2:2379
pg etcdctl member list
pg etcdctl endpoint health
pg etcdctl endpoint status -- -w table
```

pg 自身参数解析器不接受的 etcdctl 参数（`-w table`、`--hex` 等）放在 `--` 之后。

### 查看

```bash
pg addon list
```

etcd 成员出现在 **Infra add-ons (etcd)** 段：

```
Infra add-ons (etcd):
  etcd (name: m1)
    Status:      running
    Cluster:     pgcli-etcd
    Client URL:  http://127.0.0.1:2379
    TLS:         off
    Auth:        off
    Client port: 2379
    Peer port:   2380
    Image:       quay.io/coreos/etcd:v3.5.30
    Container:   pgcli-etcd-m1
```

### 启动与停止

宿主机重启或手动停掉后,无需重跑 install 即可把成员拉回来 —— 不会重新执行
`member add`,复用已有配置与磁盘数据:

```bash
pg addon start etcd --name m1
pg addon stop etcd --name m1
```

`start` 可重复执行:已在运行的成员是 no-op;容器被删掉的成员会以
`--initial-cluster-state existing` 从其数据重建。跨机集群启动某成员前,请确认
其对端可达(quorum 成立)。

### 移除成员

```bash
pg addon remove etcd --name m2
```

移除时会先从集群中**注销**该成员（先解析其十六进制 member ID —— etcd v3.5 的
`member remove` 接受 ID 而非名字），再删除容器和该成员的数据目录。只要 quorum
仍成立，其余成员保持健康。

**跨主机成员需要单独注销。** 上面这一步的自动注销，只在本地 `pg.yaml` 里存在
同一集群的另一个运行中成员时才会生效 —— pgcli 看不到住在其他主机上的成员。因
此对于跨主机集群，在运行该成员的那台主机上执行移除，只会删掉容器和数据，**集
群的成员列表里仍会留下它**（一个"残留"成员）—— 存活的成员会一直尝试和已经删
除的这台主机建立 peer 连接。

正确的做法是分两步：

```bash
# 1. 在运行该成员的主机上 —— 停止并清理本地状态
pg addon remove etcd --name m4

# 2. 从任意一台仍存活的成员所在主机，把它从集群里注销
export ETCDCTL_ENDPOINTS=http://10.0.0.1:2379   # 指向一个存活成员的 client URL
pg etcdctl member list                            # 找到 m4 的十六进制 ID
pg etcdctl member remove <hex-id>                 # 如 5c7048c8f7521ec7
```

`pg etcdctl` 需要一个可达的 peer 来通信 —— 把 `ETCDCTL_ENDPOINTS` 指向该集群
中**仍在运行**的成员（在仍有本机运行成员的主机上执行时会自动回退到它；否则就
显式设置），再按 ID 移除（见[用 `pg etcdctl` 查看](#用-pg-etcdctl-查看)）。
事后用 `pg etcdctl member list` 复核：被移除的名字应该已经不在了。

## 参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--name` | 成员名，同时作为 `pg.yaml` 里的配置 key | `etcd` |
| `--cluster` | 集群身份（`--initial-cluster-token`），取值相同即同集群 | `pgcli-etcd` |
| `--client-port` | client 主机端口（0 = 从 `etcd_start_port` 自动分配） | auto |
| `--peer-port` | peer 主机端口（0 = 自动分配，取下一个空闲端口） | auto |
| `--image` | etcd 镜像 tag | `quay.io/coreos/etcd:v3.5.30` |
| `--data-dir` | 数据目录**根**——绝对路径，或相对 `base_dir`；每个成员用 `<root>/<name>/data` | `<base_dir>/addon/etcd` |
| `--advertise-host` | 本成员 peer/client URL 中广播的主机（留空 = `127.0.0.1` 单机；跨机填 LAN IP 或 FQDN） | `127.0.0.1` |
| `--join` | 要跨机加入的现有成员 client endpoint，如 `https://10.0.0.1:2379`（隐含 `--initial-cluster-state existing`；需搭配 `--advertise-host` 与 `--cluster`；scheme 必须与 `--tls` 一致） | — |
| `--tls` | 两个接口都启用 TLS —— client HTTPS、raft peer mTLS；证书生成在 `<base_dir>/tls/etcd/<cluster>/` 下 | 关闭 |
| `--tls-cert` / `--tls-key` | 自带成员证书与私钥（隐含 `--tls`；leaf 需带 `clientAuth` EKU 或不设 EKU） | — |
| `--tls-ca` | 配合 `--tls-cert`/`--tls-key` 时客户端校验用的 CA（默认用证书本身） | — |
| `--auth` | 启用用户名/密码认证，创建单个 root 角色用户 | 关闭 |
| `--root-user` | 认证用户名（隐含 `--auth`） | `root` |
| `--password` | 认证密码（隐含 `--auth`）；首次安装省略时自动生成 | 自动生成 |

容器名遵循命名空间约定：`pgcli-etcd-<namespace>-<name>`（未设 namespace 时省略）。

## 数据目录布局

数据始终按 `<root>/<name>/data` 布局，因此同机多成员绝不共用目录：

```
<base_dir>/addon/etcd/
├── m1/data/     # 成员 m1
├── m2/data/     # 成员 m2
└── m3/data/     # 成员 m3
```

`--data-dir` 指定根目录。相对值按配置的 `base_dir` 解析；解析后的根会被持久化，
保证配置文件可移植：

```bash
# base_dir: /data/pgcli  →  根为 /data/pgcli/etcd，成员 d1 → /data/pgcli/etcd/d1/data
pg addon install etcd --name d1 --data-dir ./etcd

# 绝对路径根
pg addon install etcd --name d2 --data-dir /mnt/ssd/etcd
```

`pg addon remove` 只删除该成员的 `<name>/data` 目录，保留共享根。

## 连接

用 `pg etcdctl` —— 它在短生命周期容器里运行 etcd 的 **v3** 客户端，指向某个
成员的 client URL。目标端点取自 `ETCDCTL_ENDPOINTS`，未设置时回退到配置中的
第一个成员。etcdctl 的原生参数放在 `--` 之后：

```bash
pg etcdctl member list
pg etcdctl endpoint status -- -w table
pg etcdctl endpoint health
pg etcdctl put foo bar
pg etcdctl get foo -- --hex
```

`pg etcdctl` 始终使用 v3 API（`ETCDCTL_API=3`，etcdctl 的默认值）；不支持 v2。

当回退成员启用了 TLS 和/或认证时，`pg etcdctl` 会自动注入：通过
`ETCDCTL_CACERT` 传入该成员的 CA，通过 `ETCDCTL_USER` 传入凭据（密码不会出现在
`ps` 里）：

```bash
pg etcdctl endpoint health
pg etcdctl put foo bar
```

若 `ETCDCTL_ENDPOINTS` 指向另一个集群，可在 `--` 之后覆盖注入值：

```bash
ETCDCTL_ENDPOINTS=https://10.0.0.9:2379 pg etcdctl \
  member list -- --cacert /path/to/other-ca.crt --user root:password
```

## 拓扑与 Quorum

etcd 需要成员多数派（quorum）才能接受写入：

| 成员数 | Quorum | 可容忍故障数 |
|--------|--------|--------------|
| 1 | 1 | 0 |
| 3 | 2 | 1 |
| 5 | 3 | 2 |

- 使用**奇数**个成员 —— 生产 HA 推荐 3 或 5。
- 移除成员可能让集群跌破 quorum（例如 3 台只剩 1 台存活），此时无法提交写入。请始终保持多数在线。

## 配置

安装后，`pg.yaml` 在顶层 `addons.etcd` 下记录每个成员：

```yaml
addons:
  etcd:
    m1:
      container_name: pgcli-etcd-m1
      name: m1
      cluster_name: pgcli-etcd
      image_tag: quay.io/coreos/etcd:v3.5.30
      data_dir: /home/user/.pgcli/addon/etcd
      client_port: 2379
      peer_port: 2380
      autostart: true
    m2:
      container_name: pgcli-etcd-m2
      name: m2
      cluster_name: pgcli-etcd
      client_port: 2381
      peer_port: 2382
```

启用 TLS/认证的成员还会记录集群级设置：

```yaml
addons:
  etcd:
    m1:
      name: m1
      cluster_name: prod
      advertise_host: 10.0.0.1
      client_port: 2379
      peer_port: 2380
      tls: true
      auth: true
      auth_user: root
      auth_password: example-password
```

`auth_password` 以明文保存（与其它 addon secret 一致），`pg addon password etcd
--name m1` 读回的就是它。BYO 成员还会记录 `tls_cert_file`、`tls_key_file` 与
`tls_ca_file`；自动生成证书模式下这些字段留空，路径由集群名推导到
`<base_dir>/tls/etcd/<cluster>/` 下。

端口起始值可通过顶层 `etcd_start_port` 配置（默认 2379）。

## 开机自启

容器还带有 `--restart unless-stopped` 策略，由每容器的 conmon 监控进程执行
（无需 podman 守护进程）——它能应对进程崩溃，但**不覆盖**主机重启。要让成员
在重启后自动拉起，按成员启用 autostart：

```bash
pg autostart enable --etcd --name m1
pg autostart enable --etcd --name m2
pg autostart enable --etcd --name m3
```

这会把成员的 `autostart: true` 写入 `pg.yaml`，并安装/刷新开机服务（见
[开机自启](/docs/autostart/)）。开机时**只做启动**：拉起成员已存在的容器，
绝不重新执行 `member add`——已初始化的成员从磁盘加载集群状态即可重新加入。
跨主机集群时，在各自主机上为该主机的成员执行命令。`pg autostart status`
可查看所有成员的状态。

## 说明

- **单成员 vs 集群：** 仅 `--name m1` 得到单成员集群；用相同 `--cluster` 再装更多
  成员即可扩容。
- **生产可用：** 每个成员默认已带周期性压缩与 8 GiB 后端配额等调优参数（见上文
  「工作原理」），可直接用于生产环境的 DCS 场景。
- **重复安装是幂等的：** 对运行中的成员再次执行 `pg addon install etcd --name <m>`
  会以更新后的参数重建其容器；已在集群中注册的成员不会被重复 add。
- **TLS/认证在 bootstrap 时决定：** 跨机集群请从第一个成员起就带 `--tls
  --auth`；已初始化成员只有销毁重建才能切换。
- **手动 CA 与 BYO 证书不写入 etcd：** 只有 pgcli 自动生成的 CA 才会发布供
  `--join` 取回 —— 手动分发或 BYO 材料请自行复制。
