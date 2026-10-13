---
title: "Garage"
description: "以 pgcli 插件方式运行 Garage（Deuxfleurs 的 Rust S3 兼容对象存储）——单节点或多节点跨主机集群，明文 HTTP，TLS 由 nginx 插件前置来终结"
weight: 45
---

[Garage](https://garagehq.deuxfleurs.fr) 是 [Deuxfleurs](https://deuxfleurs.fr)
出品的 Rust、AGPLv3 S3 兼容对象存储重实现，为小规模自建和异地分布式集群设
计。pgcli 将其作为**独立的顶层插件**运行，CLI 表面与
[`minio`](../minio/)、[`silo`](../silo/)、[`rustfs`](../rustfs/) 一致
（install、list、logs、autostart、password），但其运行时模型在四个方面与三者
都不同，这也决定了本页的写法：

1. **渲染 TOML 配置文件，而不是 argv/env。** Garage 从 `/etc/garage.toml` 读
   配置。pgcli 在实例目录下渲染该文件并以只读方式 bind 挂载；minio/silo/rustfs
   插件则传 argv 或 env。
2. **两个数据目录，而不是一个——且背后没有磁盘池。** `metadata_dir`（LMDB）与
   `data_dir`（数据块）是各自独立的 bind 挂载；每个节点只有一个 `data_dir`，多
   盘聚合交给主机 LVM/RAID（MinIO 与 rustfs 则是每节点声明一串盘）——见[容量与
   尺寸计算](#容量与尺寸计算)。
3. **四个端口，来自自己的池。** S3 API、节点间 RPC、`s3_web` 静态站点、
   admin/metrics API——每实例一起从 `garage_start_port`（默认 **3900**）分配，
   与共享的 minio/silo/rustfs 池是两条独立游标。
4. **无原生 TLS——仅明文 HTTP。** pgBackRest 的 S3 仓库强制 HTTPS，因此一个要
   接收 `archive-push` 的 Garage 存储必须**由
   [nginx 插件](../nginx/)前置**——见[用 nginx 前置实现
   TLS](#用-nginx-前置实现-tls)。

和 rustfs 一样，在开源 MinIO 仓库已归档之后，Garage 是一条很好的替代路线。pgcli
默认按**单节点**运行（`replication_factor = 1`、`garage server --single-node`），
再加上几个集群 flag 即可跑成**多节点跨主机集群**——见[多节点集
群](#多节点集群)。

> **平台支持：** 已在 Linux（host 网络）上验证。macOS 的 bridge 路径会发布全
> 部四个端口、代码完整，遵循与其它对象存储一致的形态，但尚未在真机上跑过。

## 工作原理

一个实例一个容器。pgcli 直接拉取**上游** `docker.io/dxflrs/garage` 镜像
（仅 pull）——不像 rustfs 需要自建 wrapper 镜像，因为 Garage 在自己的容器里以
root 运行、且不带 uid-drop，所以不存在主机侧属主处理要消化。

数据存放在两个长于容器生命周期的 bind 挂载主机目录里：

- `<base-dir>/addon/garage/<name>/data` → `/var/lib/garage/data`（数据块），
- `<base-dir>/addon/garage/<name>/meta` → `/var/lib/garage/meta`（LMDB），

外加渲染出的 `<base-dir>/addon/garage/<name>/garage.toml` →
`/etc/garage.toml`，以**只读**方式挂载（Garage 从不改写自身配置）。

凭据的处理与其它存储类似，但 Garage 用的是 **access key / secret key 对**，而不
是 root user / password：

- **`root_user`** 是 access key id，**首次安装时生成**，带 Garage 自家的 `GK`
  前缀形态（`GK` + 32 位十六进制，即上游 quick-start 里
  `openssl rand -hex 16` 产出的形状），或用 `--root-user` 指定。
- **`root_password`** 是 secret key，**首次安装时生成**（64 位十六进制），或用
  `--root-password` 指定。
- 第三个秘密 `rpc_secret`（集群 RPC 签名密钥）以同样方式生成并一并存储。单节点
  下它从不出现在 CLI 表面；集群 bootstrap 会把**它连同加入命令一起打印一次**，
  由操作员搬运到下一台主机（pgcli 从不在远端执行任何东西）。

三者都落进 `pg.yaml`（`addons.garage.<name>.*`），因此重建容器是稳定的。**单节
点**首次启动时容器被传入 `--single-node --default-bucket` 以及
`GARAGE_DEFAULT_ACCESS_KEY` / `GARAGE_DEFAULT_SECRET_KEY` /
`GARAGE_DEFAULT_BUCKET` env，于是 Garage 自己创建这个 access key 和一个桶（以实
例名命名、归属该 key）——无需安装后再 exec。key 与桶都存在 `metadata_dir` 里，
因此能挺过容器重建。集群成员不带这些 flag 启动，key 与桶改由安装尾部显式播种
（见下文）。

## 安装

```bash
# 默认实例名 "garage"，四个端口取自 garage 池，回环绑定
pg addon install garage

# 指定名字与显式数据目录的实例
pg addon install garage --name store --data-dir /srv/garage

# 固定 S3 API 端口（rpc/web/admin 取随后三个空闲端口）
pg addon install garage --name store --api-port 3900

# 把存储暴露到网络而非仅回环
pg addon install garage --name store --listen 0.0.0.0

# 自带 access key / secret key，而不是自动生成
#   受 Garage 自家限制：access key ≥ 8 字符，secret key ≥ 16 字符
pg addon install garage --name store --root-user GK<...> --root-password <secret>

# 加入一个跨主机集群（见下文"多节点集群"）
pg addon install garage --name store --cluster c1 --advertise-host 10.241.20.147 \
  --zone z1 --capacity 15 --replication-factor 3 \
  [--peer <id@addr> --rpc-secret <64hex>]   # joiner 两个都要带
```

> **自带凭据：** 这两个 flag 只在**单节点**安装时生效——集群成员从不读它们，
> 其 key 来自 quorum 达成那次安装的 `garage key create`。取值原样交给
> Garage，由 **Garage** 执行限制：access key id 短于 8 字符、或 secret key 短于
> 16 字符，会在容器首启时被拒（`Invalid default access key: … should be at
> least N characters long`）——又因容器是后台起的，`install` 本身报成功、节点却
> 在 `--restart unless-stopped` 下后台崩溃重启。若一次自带 key 的安装起来后无
> 法服务，查 `podman logs`（或干脆让 `pg mc alias set` 报错）。生成出的形状
> （`GK` + 32 hex / 64 hex）远远越过这两个下界；`mc` 另外要求 access key 为
> `[A-Za-z0-9]`，所以保持字母数字。（pgcli 现在也会在安装前就按同样下界拦一道，
> 见下。）

输出会报告端点和播种的凭据：

```
✓ garage installed: "store"
  Container:     pgcli-garage-default-store
  Image:         docker.io/dxflrs/garage:v2.4.1
  Data:          ~/pg/addon/garage/store/data
  Metadata:      ~/pg/addon/garage/store/meta
  Config:        ~/pg/addon/garage/store/garage.toml
  S3 API:        http://127.0.0.1:3900
  RPC/Web/Admin: 3901/3902/3903

  Access key:    GK<generated>
  Secret key:    <generated>
  Bucket:        store (created on first boot, owned by the access key)

  NOTE: Garage serves plaintext HTTP only. pgBackRest requires HTTPS — front it
        with the nginx addon via --conf-file (template --upstream mode strips
        the port from Host and breaks S3 signing; see docs/addon/garage):
          backend 127.0.0.1:3900, listen ssl, proxy_set_header Host $http_host
  Try it:        pg mc alias set store http://127.0.0.1:3900 GK<...> <secret> && pg mc ls store
```

对**存活**实例重跑 install 是 no-op：容器不会被重建（处于停止的会被启动，并给出
提示），flag 会合并进已存配置，已有凭据保持不变。传 `--force` 会重建容器，从而让
改动过的端口、监听地址或凭据生效——但不丢两个数据目录。

> **监听地址：** 默认 `127.0.0.1` 让存储留在本地。`--listen 0.0.0.0`（或
> `pg.yaml` 里的 `listen` 键）把它暴露到网络。由于 Garage 服务的是**明文
> HTTP**，任何能触达端口的人都能在传输中嗅到凭据——所以只在防火墙后公开绑定，
> 实际做法则是放在[nginx TLS 前置](#用-nginx-前置实现-tls)之后。

> **`--tls` 对 garage 会被拒绝。** Garage 没有可启用的 TLS 监听器；传
> `--tls`/`--tls-cert`/`--tls-key` 会直接报错并把你指向 nginx 前置，而不是默默
> 忽略这个 flag。

### 用 nginx 前置实现 TLS

由于 pgcli 不把两个插件耦合在一起，在一个 Garage 存储前面终结 TLS 是显式的两步：
先装 garage，再用 nginx 前置它。

> **用 `--conf-file`，不要用 `--upstream` 模板。** S3 请求用 SigV4 对**含端口
> 的** `Host` 头签名。模板模式转发 `Host $host`，而 nginx 渲染它时**不带**端口
> ——于是 Garage 重新算出的 canonical request 不同，每个写操作都以 `Invalid
> signature` 失败（已实测：经模板前置 `pg mc mb` 被拒；经下面 `--conf-file` 前置
> 则完整往返）。自定义配置还能设 `client_max_body_size 0;`（nginx 默认的 1 MiB
> 上限会拒掉任何真实对象上传）以及很长的代理超时。

```bash
pg addon install garage --name store

# front.conf —— 经 TLS 的 path-style S3，保留 host+port 以供签名：
cat > front.conf <<'EOF'
worker_processes auto;
pid /tmp/nginx.pid;
events { worker_connections 1024; }
http {
    access_log /var/log/nginx/access.log;
    error_log  /var/log/nginx/error.log;
    server {
        listen 0.0.0.0:8081 ssl;          # 0.0.0.0 让远端客户端也能访问；pgcli 分配给前置的 HTTPS 端口
        ssl_certificate     /etc/nginx/certs/public.crt;
        ssl_certificate_key /etc/nginx/certs/private.key;
        client_max_body_size 0;          # PUT 不限大小
        proxy_read_timeout   3600s;
        proxy_send_timeout   3600s;
        location / {
            proxy_pass http://127.0.0.1:3900;   # garage 的 S3 API 端口
            proxy_set_header Host $http_host;   # 保留端口 —— SigV4 需要
            proxy_request_buffering off;
            proxy_http_version 1.1;
        }
    }
}
EOF

pg addon install nginx --name gtls --tls --conf-file front.conf --https-port 8081
```

`--tls` 让 pgcli 生成自签 CA + leaf（`public.crt` / `private.key`，挂在
`/etc/nginx/certs`），并起一个 HTTPS 监听器。把 S3 客户端指向
`https://<host>:8081`，并把前置的 CA `<base-dir>/addon/nginx/gtls/tls/ca.crt`
交给消费方。

> **前置绑 `0.0.0.0`，Garage 留在回环。** 上面那条 `listen` 绑所有网卡，让远端
> 主机（如要推归档的 Patroni 节点）能访问这个 TLS 端点——TLS 是在 *nginx 处*
> 终结的，所以这样做是安全的，而直接暴露 Garage 的明文端口则不是。`proxy_pass`
> 仍指向 `127.0.0.1:<garage-api-port>`：nginx 容器跑在 host 网络上，从它那里能达
> 到 Garage 的回环绑定，明文那一跳从不出机器。若前置只想本机可用，改回
> `listen 127.0.0.1:<port> ssl;`。

> **为什么 `listen` 与 `proxy_pass` 两行要手写死。** `--conf-file` 模式下 pgcli
> 原样挂载你的配置，所以里面的端口必须与两个插件被分配到的端口对上——garage 的
> `S3 API:` 端口取自它的安装输出，nginx 的 `HTTPS:` 端口取自 `pg addon list`。
> （`$http_host` 和 `client_max_body_size` 两行，正是模板前置对 S3 会搞错的地
> 方。）

把那个 CA 弄到远程 Patroni 主机上而无需手动 scp，正是 `pg backup fetch-ca` 的用
武之地——它拨通 TLS 前置，直接从握手里取出根证书：

```bash
pg backup fetch-ca <front-host>:<https-port>
#   [OK] CA fetched from <front-host>:<https-port>
#        saved:    ~/.pgcli/backup/repo-ca/ca-<front-host>-<https-port>.crt
#        SHA-256:  c0f0…fe2e
pg backup setup --s3-ca-file ~/.pgcli/backup/repo-ca/ca-<front-host>-<https-port>.crt
```

这个拉取是 trust-on-first-use——在信任之前，把打印出的 SHA-256 与前置主机上的
`sha256sum ~/.pgcli/addon/nginx/gtls/tls/ca.crt` 对一遍。

#### 前置一个集群：一台，还是本地几台

集群**并不**需要把所有成员都挂到前置后面。S3 的复制是数据层的事：打到*任意*一
个成员的请求都是完整的，该成员自己会按 `replication_factor` 复制。所以上面那个
单后端前置对集群同样成立——把 `proxy_pass` 指到一个成员的 S3 端口即可。

只有当你想把"被前置的那台成员"从**接入层**单点里拿掉时，才多挂几台。upstream
里只保留前置所在主机可达的成员（通常是本机房——见[异地分布拓
扑](#异地分布拓扑2-本地-1-远端)），每个机房各自跑自己的前置：

```nginx
http {
    upstream garage_s3 {
        server 10.241.20.147:3904;   # 成员 A，本机房
        server 10.241.20.148:3900;   # 成员 B，本机房
        # 不放远端成员：它是数据副本，不进请求路径
    }
    server {
        listen 0.0.0.0:8081 ssl;
        ssl_certificate     /etc/nginx/certs/public.crt;
        ssl_certificate_key /etc/nginx/certs/private.key;
        client_max_body_size 0;
        proxy_read_timeout   3600s;
        proxy_send_timeout   3600s;
        location / {
            proxy_pass http://garage_s3;
            proxy_set_header Host $http_host;   # 保留端口 —— SigV4 需要
            proxy_request_buffering off;
            proxy_http_version 1.1;
        }
    }
}
```

相比单后端示例，这带来两个新约束：

- **成员的 S3 API 必须离开回环。** 集群模式只会自动放宽 RPC bind；`--listen`
  默认仍是 `127.0.0.1`，远端前置够不到别的主机的成员。被前置的成员要用
  `--listen 0.0.0.0` 安装（放在防火墙后——讲 TLS 的是前置），或者接受"upstream
  里只有同主机成员可用"。
- **各主机端口号不同**——和 RPC 端口是同一个坑：每个成员的 S3 API 端口取自它自己
  的安装输出 / `pg addon list`（`S3 URL:`），未必是 3900。

这仍然是 `--conf-file` 配置：`--upstream` 模板对 S3 的毛病不变——`Host $host`
不带端口会让每个 SigV4 写失败，而且它没有 `client_max_body_size 0`（见上面的注
意）。多 upstream 服务器时默认的轮询就够用；`max_fails`/`fail_timeout` 按需调。
（已在 3 节点集群实测：`pg mc` 经这样一套前置完整往返；停掉 upstream 中的一台
成员后，前置经另一台照常服务。）

### 把 pgBackRest 接到前置后的 Garage 存储

一旦有了 TLS 端点，一个 Garage 支撑的 S3 仓库与 MinIO 的完全一样——S3 契约相
同（path-style、`repo1-s3-uri-style=path`）：

```bash
pg backup setup \
  --s3-endpoint <front-host>:<https-port> \
  --s3-bucket store \
  --s3-access-key GK<...> \
  --s3-secret-key <secret> \
  --s3-ca-file <front>/addon/nginx/gtls/tls/ca.crt
```

完整流程见[备份 → S3 对象存储仓
库](../../backup/#s3-对象存储仓库)。`pg addon password garage --name store` 会
为脚本打印 secret key。

## 多节点集群

Garage 的真正价值在于跨主机集群：不同 zone 的节点、CRDT 复制的 layout、quorum
读写。pgcli 用**同一条 `pg addon install garage`** 驱动它——没有新入口——只是多
了几个集群 flag。集群是**逐台主机**组装的（Patroni 模式）：pgcli 从不在远端主机
上执行任何东西。第一台*bootstrap*，打印一条现成的加入命令（内含共享的
`rpc_secret` 和本节点的 `id@addr`），你把这条命令（为下一台改好参数）自己在下一
台上执行。

### 角色

安装角色由你传的 flag 决定：

| 角色 | 何时 | install 做什么 |
|------|------|-------------------|
| **single** | 没有 `--cluster` | v1 路径，逐字节不变（`--single-node --default-bucket`） |
| **bootstrap** | 有 `--cluster`、无 `--peer` | 生成 `rpc_secret`，起裸 `garage server`，staged 本机 layout 角色，打印加入命令 |
| **join** | `--cluster` + `--peer <id@addr>` | 带 `--rpc-secret`，connect 该 peer，staged 本机角色，凑齐 quorum 的那次 apply 并提交 |

### 三主机走一遍

**主机 1——bootstrap。** 给它 `--zone`、`--capacity` 和最终的
`--replication-factor`。此时集群里只有一个节点，Garage 会*拒绝 apply* layout
（它需要 `replication_factor` 个有正容量的节点），pgcli 把这变成警告而不是错误
——staged 角色是集群复制的，会一直等到最后一台提交：

```bash
# 在 10.241.20.147 上
pg addon install garage --name store --cluster c1 \
  --advertise-host 10.241.20.147 --zone z1 --capacity 15 --replication-factor 3
```

摘要会打印 secret 和一份可复制的加入模板：

```
  Cluster:       c1 (role: bootstrapping cluster, replication factor 3)
  Advertise:     10.241.20.147:3905
  Zone/Capacity: z1 / 15 GiB
  Node ID:       1c0597cc…200aeb@10.241.20.147:3905
  [i] layout staged but not applied yet — Garage needs replication-factor assigned nodes to commit (later joins will).

  To add ANOTHER member, run on the next host (carrying --rpc-secret yourself;
  pgcli never executes remotely). ...
    pg addon install garage --name store --cluster c1 \
      --advertise-host <next-host-ip> --rpc-secret <64hex> \
      --peer 1c0597cc…200aeb@10.241.20.147:3905 --zone <zone> --capacity <GiB> --replication-factor 3
```

**主机 2——join。** 复制打印出的命令，把 `--advertise-host` 改成本机 IP、
`--zone` 换一个：

```bash
# 在 10.241.20.148 上
pg addon install garage --name store --cluster c1 \
  --advertise-host 10.241.20.148 --rpc-secret <64hex> \
  --peer 1c0597cc…200aeb@10.241.20.147:3905 --zone z2 --capacity 15 --replication-factor 3
```

现在有两个节点 assigned，仍少于 `rf = 3`，所以再次警告。peer 连接是**可传递**
的：主机 2 只需要一个 `--peer`（主机 1）；主机 3 指向任意一台已存在的节点都行。

**主机 3——凑齐 quorum 的那次安装。** 第三台能看到完整的 staged 角色集（assign
是集群复制的），于是它的 `layout apply` 把版本 1 提交——并且**就是这一次**安装
完成 access key 和 bucket 的播种：

```bash
# 在 10.241.20.153 上
pg addon install garage --name store --cluster c1 \
  --advertise-host 10.241.20.153 --rpc-secret <64hex> \
  --peer 5b370435…1c1c9187@10.241.20.148:3901 --zone z3 --capacity 15 --replication-factor 3
```

```
  Access key:    GKc57063…6147b0
  Secret key:    <printed once, here>
  Bucket:        store (cluster key + bucket seeded on this install)
```

每台安装都幂等地跑播种步骤，所以**最后**加入的那台天然就是执行播种的——不需要
主机 1 回填。key 名就是实例名（`store`），桶同名，key 被授予该桶的
read/write/owner。

### 播种与 secret

集群里单节点的 `--default-bucket` / `GARAGE_DEFAULT_*` 快速通道不可用，所以
pgcli 用 `key create` + `bucket create` + `bucket allow` 显式播种。两个推论：

- **`garage key create` 的 secret 只打印一次**，且*不*幂等（重跑会铸造第二个同名
  key）。pgcli 因此先查 `key list`，缺席才创建。执行播种的那台主机把
  access/secret 对写进**它自己**的 `pg.yaml`；其它主机只持有公开的 key id。
- **非播种主机上的 `pg addon password garage`** 无 secret 可打印，会明确告知——
  secret 在完成 quorum 的那台主机的 `pg.yaml` 里。去那台取回，或自己
  `garage key create` 铸一把新 key。

### 版本冲突与重跑

`layout apply --version N` 要求恰好等于 `当前版本 + 1`——Garage 的乐观锁。两台
主机并发安装时，慢的一台 apply 失败；pgcli 报"layout changed concurrently —
re-run the same install command"。assign 幂等、重新 stage 无害，所以重跑只是重新
读版本再 apply。对已在线成员重跑安装其它步骤都是 no-op（容器在跑→
`skipping creation`；已 connect→视为成功）。

### zone 与 replication-factor 建议

- `--replication-factor` 必须 ≤ 你打算加入的节点数；在 `rf` 个节点拿到正容量
  角色之前，集群无法播种。
- 每台主机给一个**互不相同的 `--zone`**，以承受整个故障域丢失；Garage 会尽量把
  副本放进不同 zone。
- `--capacity` 单位 GiB，是 Garage 划给数据块的额度；capacity 为 0 的节点被视为
  不承载数据。
- `--advertise-host` 对每个成员都是必需的——它成为 `rpc_public_addr`，peer 拨号
  的地址。缺了它，集群成员不可达。（`--listen` 为回环的集群成员仍会把 **RPC**
  bind 放宽到 `0.0.0.0`；只有 S3/web/admin 端口留在回环上。）

### 容量与尺寸计算

**Garage 没有磁盘池的概念。** MinIO 和 rustfs 都是接收一串盘：一个节点声明 N 块
设备、把数据条带化铺在这些盘上、并自行承受单盘损坏。Garage 的冗余单位是**节点**
而不是盘——每个成员恰好只有一个 `data_dir`（外加独立的 LMDB `metadata_dir`），
同一对象的每一块副本都会被放到*不同节点*上（尽量放到不同 *zone*）。要多盘聚合，
就在 Garage 之下做：用 LVM 或主机 RAID 呈现一个大文件系统，Garage 的
`data_dir` 落在它上面。于是磁盘故障是主机那一层的事，节点故障才是 Garage 的
事。**MinIO/rustfs 管盘，Garage 管节点。**

`--capacity` 就是交给这个模型的那个数：本节点可存放的 GiB 数
（`layout assign -c <N>GiB`）。有两个性质必须内化：

- 它是**放置权重，不是配额**。容量用满时 Garage 也从不拒写——真正的剩余空间是
  `garage status` 里 `DataAvail` 报的值。在一块 20 GiB 的文件系统上设
  `--capacity 100`，只会让 Garage 把盘填满（然后写坏）。把它保持在后端文件系统
  可用大小以内（或更低）。
- 改它是一次 **layout 变更**：用新的 `--capacity` 重跑同一条
  `pg addon install`（assign 幂等，apply 会把 layout 版本加一），Garage 就把
  数据块重新均衡到扩出来的空间上。先把后端文件系统扩开，才是真正买到容量。

**可用容量**是所有节点容量之和除以副本因子——每个对象都保留 `rf` 份：

| 节点数 | 单节点（`--capacity`） | rf | 原始总量 | 可用 |
|--------|----------------------|----|---------|------|
| 3 | 15 GiB | 3 | 45 GiB | **15 GiB** |
| 4 | 15 GiB | 3 | 60 GiB | **20 GiB** |
| 5 | 15 GiB | 3 | 75 GiB | **25 GiB** |

一般规则：`可用 = Σ容量 ÷ rf`。同一套数学也解释了为什么 `rf = 2` 把存储开销降一
半、代价是只能容忍一台节点丢失；`rf = 3` 是 3 倍开销，但一次写入只要有 **3 份中
的 2 份** ack 就提交，所以 3 节点的 `rf = 3` 集群写入时可容忍 1 台宕，5 节点的可
容忍 2 台（剩余成员里任意 2 台能 ack）。因此加第 4、第 5 台同时买到容错*和*可用
容量，这跟单纯镜像不一样。

### 停一台后的 quorum 行为

停掉任意一台，其余两台照常读写（`rf = 3` 时的 2/3 quorum）：

```bash
# 主机 3 上
pg addon stop garage --name store
# 主机 1 上——照常读写
pg mc cat c1/store/v.txt
```

`pg addon start` 会以**同一个** `Node ID` 重新入集群（密钥对在
`metadata_dir` 里，不属于容器），`garage status` 重新显示它健康。
start/stop/remove/autostart/logs 永远只管**本机**容器——Patroni 语义；没有远程编
排。

### 异地分布拓扑：2 本地 + 1 远端

Garage 生来为此的生产形态：本机房放两个成员（各占一个机架/zone，`z1` +
`z2`），第三个成员放在**远端机房**（`z3`）。这个形状和写延迟的数学很合拍：
`rf = 3` 的写只需 **2/3 quorum** 提交——本地两副本 ack 即算完成，远端副本异步
跟进，WAN 链路慢、甚至临时断开都不拖慢写入。nginx TLS 前置只指向两个本地成员
（见[前置一个集群](#前置一个集群一台还是本地几台)）；远端成员只承载数据、不进
请求路径，通常自己也起一个前置，供本地读修复/灾难时使用。

对远端成员"**不是什么**"，有两个诚实的注脚：

- **它是副本，不是备份。** CRDT 复制同步一切，*包括删除*——误操作的
  `mc rb --force` 或 pgBackRest `expire` 都会传播到远端机房。要拿"免疫误删"的
  副本得用 Garage 自己的跨集群 bucket replication（Garage CLI 功能，本文档不代
  管：`garage bucket replication`）；2+1 集群给你的是节点级与机房级的存活能力加
  一份人工抢救副本，仅此而已。
- **整机房故障不会自动切换。** 本地两台一起挂，只剩远台一台低于 quorum（1 <
  2），集群不可写。数据在那台节点上是完好的，Garage 也有文档化的人工恢复流程
  （从存活节点重新 commit 一个缩减 layout 拉起集群），但 pgcli 不编排这一步——
  那是"操作员确认机房真的没了"之后手敲 `garage layout` 的事。

所以准确的定位是：**2 本地 + 1 远端 = 带机房级抢救副本的 HA**，写入保持本地速
度；"备份"（免疫误删义）仍然是 pgBackRest（或跨集群 replication）的职责。

### 拿到集群的 AK/SK

播种的 access key 是集群级凭据（任何成员都能验证），但它的 **secret 只存在于一
台主机**——凑齐 quorum 的那次安装打印过一次，并写进了那台主机的 `pg.yaml`：

```bash
# 只能在种子主机上：
pg addon password garage --name store              # 打印 secret key
pg addon password garage --name store --file ./ak  # 0600 写文件，便于搬运
pg addon list --show-password                      # access key + secret 一起看
```

- 任何主机都能看**公开 id**（`pg addon list` 的 `Access key:` 行）；非种子主机上
  `pg addon password` 会报错并点名持有 secret 的主机——信息准确，secret 就是不
  在那台机器上。
- Secret 丢了，或想要一套自己的凭据？在**任意**成员上铸一把新 key 并授权桶——
  key 是 CRDT 状态，新凭据即刻全集群可用：

  ```bash
  pg garage --name store key create app-key
  pg garage --name store bucket allow store \
    --key GK<新id> --read --write --owner
  ```

  这是推荐的 day-2 姿势：应用和 pgBackRest 各用各的 key（独立轮换/吊销），播种
  那把留作 break-glass 管理员凭据。`pg garage` 是 Garage 自家控制面 CLI 的通用
  透传入口——完整运维菜单见[用 `pg garage` 做管理](#用-pg-garage-做管理)。

> **v2.0 不做**：把节点从 layout 移除（`layout remove` + 数据迁移）、revert 已提
> 交的 layout、多 zone 故障切换编排。这些仍是手敲 garage CLI 的事——用
> [`pg garage`](#用-pg-garage-做管理) 跑。

## 使用 mc 客户端


`pg mc` 从一个临时容器里跑 MinIO 自家的 `mc` 客户端——无需本地安装、无需手动
`podman run`——并像对其它存储一样讲 Garage 的 S3 API。用 **access key** 顶替
user、**secret key** 顶替 password：

```bash
# 按平台选 URL——见下面"按平台的端点"：
pg mc alias set store http://127.0.0.1:3900 GK<...> <secret>       # Linux，本地插件
pg mc alias set store http://host.containers.internal:3900 GK<...> <secret>  # macOS，本地插件
pg mc alias set store http://10.0.5.7:3900 GK<...> <secret>        # 二者皆可，远程存储
pg mc mb store/backups
pg mc ls store                       # 播种的 "store" 桶已经在这里
pg mc cp ./dump.pglz store/backups/
```

alias 持久化在主机上的 `~/.mc/config.json`——mc 自己的默认路径，Linux 和 macOS
上一致——所以 `alias set` 一次，之后每次 `pg mc` 调用（以及 Linux 上的原生
`mc`）都看到同样的 alias。`pg mc alias list` 读回，`pg mc alias remove` 删
除。`alias set` 在写入前先对端点校验凭据，所以写错的 key 不会污染
`~/.mc/config.json`。底层上 `pg mc` 把你的 `~/.mc` 挂进容器并跑
`ghcr.io/mars-base/pgcli/pgcli-mc`（`scratch` 上的静态上游二进制）；此外没有任何
魔法。

任何会被 `pg` 自身 flag 解析器拒绝的 `mc` flag 都放在 `--` 之后，与其它存储完全
一样——`--recursive`、`--force`、`--all`、`--json` 等等：

```bash
pg mc cp ./dir store/backups/ -- --recursive
pg mc rb store/scratch -- --force
```

> **按平台的端点：** 在 Linux 上 `pg mc` 跑在 host 网络里，所以 `127.0.0.1` 的
> alias 能直达本地插件实例。在 macOS 上容器坐在 bridge 网络里，那里的
> `127.0.0.1` 是容器自己的回环——把本地 Mac 插件的 alias 指向
> `http://GK<...>:<secret>@host.containers.internal:3900`，或把远程存储指向其可
> 路由地址。要走 TLS，就把 alias 设成 nginx 前置的 `https://` 端点（自签 leaf
> 再加 `-- --insecure`）。

### 常用命令

| 命令 | 用途 |
|-------|---------|
| `pg mc ls store` / `pg mc ls store/backups` | 列桶 / 列对象 |
| `pg mc mb store/backups` | 建桶 |
| `pg mc cp ./file store/backups/` | 上传（整棵树：`pg mc mirror ./dir store/backups/`，或给 `cp` 加 `-- --recursive`） |
| `pg mc cp store/backups/file ./` | 下载 |
| `pg mc find store -- --name '*.pglz'` | 按名字搜对象 |
| `pg mc du store` | 每桶用量 |
| `pg mc rm store/backups/file` | 删单个对象 |
| `pg mc rm store/prefix -- --recursive` | 批量删对象 |
| `pg mc rb store/bucket -- --force` | 连对象一起删桶 |

同一对 access/secret key 适用于任何 S3 SDK——包括对 `repo1-type=s3` 仓库的
pgBackRest（经 nginx TLS 前置）。

## 用 `pg garage` 做管理

`pg garage` 把 Garage 自家控制面 CLI 的任意子命令转发进实例的运行中容器——手写
`podman exec pgcli-garage-<ns>-<name> /garage …` 的替代品。`--name` 选实例
（默认按名字排第一的那个）；子命令词之后的一切逐字到达 `/garage`，所以 Garage
自家的 flag 不需要 `--` 分隔。单节点实例和集群成员上用法完全一致；集群里问任意
**运行中**的成员即可——status、layout、key 都是 CRDT 复制状态，全集群一致。

### 健康与状态

```bash
pg garage --name store health            # 集群健康时退出码才是 0
pg garage --name store status            # 每节点：zone、容量、DataAvail、版本
pg garage --name store layout show       # 已提交角色 + 暂存变更 + layout 版本
pg garage --name store stats             # 逐分区的复制明细
curl -s http://127.0.0.1:3903/health     # admin 端口的存活检查行
```

`health` 是对监控最友好的一条：打印简短汇总（已知/已连/存储节点数、分区
quorum），并且**会设置退出码**，所以 cron 或巡检脚本可以直接用
`pg garage --name store health > /dev/null`。`status` 是宽表——盯住
`DataAvail` 列，那才是真实剩余空间（见[容量与尺寸计算](#容量与尺寸计算)）。

### key 与 bucket

```bash
pg garage --name store key list                    # 谁能访问这个存储
pg garage --name store key create app-key          # secret 只打印一次
pg garage --name store bucket allow store --key GK... --read --write
pg garage --name store bucket deny  store --key GK... --write
pg garage --name store bucket list
pg garage --name store bucket info store           # 大小、对象数、逐 key 授权
pg garage --name store key delete GK...            # 吊销一把应用 key
```

先铸 key 再 allow，就是 day-2 凭据姿势：应用各用各的 key，播种那把留作
break-glass（见[拿到集群的 AK/SK](#拿到集群的-aksk)）。

### layout 变更（集群）

加节点走 `pg addon install`（assign/apply 自动完成）；裸 CLI 用于审计，以及
pgcli 刻意不编排的手动边缘场景：

```bash
pg garage --name store layout history              # 哪个版本、何时、由哪台提交
pg garage --name store layout show                 # 有没有 STAGED ROLE CHANGES 悬着
pg garage --name store node connect <id@addr>      # （重新）peer 一个节点
```

`layout remove`、`layout revert` 与多 zone 故障切换仍是手动 garage 操作——
v2.0 限制清单在[拿到集群的 AK/SK](#拿到集群的-aksk)一节末尾。

### 日常维护

```bash
pg garage --name store bucket cleanup-incomplete-uploads   # 中止陈旧的 multipart 上传
pg garage --name store key delete-expired                  # 清理过期 key
pg garage --name store bucket inspect-object store/key     # 这个对象的块都在哪些节点
```

## 端口

每个实例从 Garage 自己的池取**四个连续端口**，起点是 `garage_start_port`（默认
**3900**）：先是 S3 API，然后 RPC、`s3_web`、admin/metrics。这个池**与共享的
minio/silo/rustfs 池（那个从 `minio_start_port`、默认 9000 起）分开**，所以一个
garage、一个 minio、一个 rustfs 实例永不冲突：

```bash
pg addon install garage --name store    # 3900 / 3901 / 3902 / 3903
pg addon install garage --name lake     # 3904 / 3905 / 3906 / 3907
pg addon install minio  --name archive  # 9000 / 9001  （共享池）
```

只有 S3 API 端口是旋钮（`--api-port`）；另外三个被分配其后的空闲端口。RPC 端口
是节点间流量，对单节点实例会留在回环上；admin/metrics 端口（`3903`）服务 Garage
的状态 API。**集群**成员的 RPC 端口是 peer 拨号的目标（`rpc_public_addr`），所以
即便 `--listen` 为回环，pgcli 也会把它的 RPC bind 放宽到 `0.0.0.0`——这条通道由
`rpc_secret` 签名。注意端口号也随 `--advertise-host`/`--peer` 一起走，所以在一台
已跑着另一个 garage 实例的主机上，集群的 RPC 可能落在不同于 3901 的端口（例如
`10.241.20.147:3905`）。

### 没有 Web 控制台——用 CLI 与 admin API 管理

与 MinIO（自带跑在第二个端口上的 Web Console）不同，**Garage 没有内置
Web UI**。它的管理面是三样东西：

- **`garage` CLI，通过 `pg garage`**——检视与改动状态的实际手段。`pg garage`
  把命令跑进实例的运行中容器（它经由本机 RPC 端口通信），从此不用手敲
  `podman exec`：

  ```bash
  pg garage --name store status         # 节点、zone、容量、DataAvail
  pg garage --name store layout show    # 已提交的 layout + 版本
  pg garage --name store bucket list
  pg garage --name store key list
  ```

  `--name` 之后的内容逐字转发给 `/garage`，所以 Garage 自家的 flag
  （`--read --write`、`-z`、`-c`）不需要 `--` 分隔——且必须放在子命令词**之后**，
  因为 pgcli 只读它前面的自有 flag。只装了一个实例时 `--name` 可省；装了多个
  时，省略则命中按名字排第一的那个（stderr 会播报）。它需要一个**运行中**的成
  员——不同于 `pg mc`/`pg etcdctl` 会拉起临时客户端容器，`pg garage` 是在节点
  自己容器里 exec。完整运维菜单见[用 `pg garage` 做管理](#用-pg-garage-做管理)。

- **admin API**，在 admin 端口（`3903`）——一个 JSON API。从 Garage v2 起端点形如
  `/v2/<Operation>`（如 `/v2/GetClusterHealth`），且**必须在 `Authorization` 头里
  带 Bearer token**；裸 `curl` 会拿到 `403 AccessDenied`。只有少数端点无需 token：

  ```bash
  curl -s http://127.0.0.1:3903/health          # "Garage is fully operational"
  curl -s http://127.0.0.1:3903/metrics         # Prometheus 指标
  curl -s http://127.0.0.1:3903/v2/GetClusterHealth \
    -H "Authorization: Bearer <admin-token>"   # 需要 token
  ```

- **`s3_web`**，在 web 端口（`3902`）——这**不是**控制台。它只为开启了网站托管的
  bucket 提供静态站点内容（按 bucket 的 vhost）；访问根路径返回 `404`。

社区有第三方 Web UI（基于 admin API 构建的看板等），但它们不属于 Garage、也不由
pgcli 管理；接一个这样的界面意味着先签发一个 admin API token，再把它指向 admin
端口。pgcli 分配的 `web_port`（`3902`）是留给 bucket 网站托管的，所以第三方控制台
应该对接 `3903`（admin API）或用 [`pg garage`](#用-pg-garage-做管理)——而不是 `3902`。

## 配置

实例位于 `pg.yaml` 顶层的 `addons.garage` 映射里：

```yaml
namespace: default
garage_start_port: 3900    # garage 自己的池（API/RPC/web/admin，每个占四个）
addons:
  garage:
    store:
      container_name: pgcli-garage-default-store
      name: store
      image_tag: docker.io/dxflrs/garage:v2.4.1
      # data_dir: /srv/garage      # 省略则为 <base-dir>/addon/garage/store/data
      #                             # （meta 目录和 garage.toml 与它同级）
      # config_file: /etc/garage.toml   # 自带配置：原样挂载，永不改写
      listen: 127.0.0.1
      api_port: 3900
      rpc_port: 3901
      web_port: 3902
      admin_port: 3903
      root_user: GK<generated>        # access key id，首次安装时写入
      root_password: <generated>      # secret key，首次安装时写入
      rpc_secret: <generated>         # 集群 RPC 签名密钥，首次安装时写入
      region: garage                  # s3_region
      replication_factor: 1           # 单节点必须为 1；集群才设 >1
      autostart: false                # pg autostart enable --garage --name store
      # --- 多节点集群键（单节点请全部省略）---
      # cluster: c1                   # 非空即集群模式；把成员归到一组
      # advertise_host: 10.241.20.147 # rpc_public_addr 主机；集群模式必填
      # zone: z1                      # layout assign -z（故障域）
      # capacity: 15                  # layout assign -c，单位 GiB；0 表示不自动 assign
      # peers:                        # 已知 id@addr，逐个 `garage node connect`
      #   - 1c0597cc…200aeb@10.241.20.147:3905
      # node_id: 1c0597cc…200aeb@10.241.20.147:3905  # 首启后读回并缓存
```

`replication_factor` 默认 1。**没有** `cluster` 却把它设 `> 1` 会在安装时被拒绝
——单节点凑不出多节点 quorum。上面的集群键由集群安装路径写入；也可手写后重装。
zone 与 capacity 是 layout 状态、不进 TOML，所以只出现在这里。

对 `listen`、端口、`root_user`、`root_password`、`image_tag`、`data_dir` 或
`config_file` 的修改，在下一次 `pg addon install garage --name store --force` 后
生效。

### 列表

```bash
pg addon list
```

```
Infra add-ons (garage):
  garage (name: store)
    Status:      running
    Cluster:     c1 (rf 3)
    Advertise:   10.241.20.147:3905
    Zone/Cap:    z1 / 15 GiB
    Node ID:     1c0597cc…200aeb@10.241.20.147:3905
    Nodes:       3 assigned (healthy)
    Listen:      127.0.0.1
    API port:    3904
    RPC/Web/Admin: 3905/3906/3907
    S3 URL:      http://127.0.0.1:3904
    Data:        ~/pg/addon/garage/store/data
    Metadata:    ~/pg/addon/garage/store/meta
    Access key:  GK<generated>
    Bucket:      store
    Image:       docker.io/dxflrs/garage:v2.4.1
    Container:   pgcli-garage-default-store
    NOTE:        plaintext HTTP only — front with the nginx addon for TLS
```

集群成员多出 `Cluster` / `Advertise` / `Zone/Cap` / `Node ID` / `Nodes` 几行；
`Nodes:` 是一次实时的 `garage status` 读取（尽力而为——成员不可达时省略）。集群
里，未执行播种的那几台主机上 `Access key` 只有公开 id（没有 secret）；见[播种与
secret](#播种与-secret)。

`pg addon list` 从不打印 secret key。要刻意揭示它用
`pg addon list --show-password`（追加一行 `Secret key:`），或为脚本只打印这个秘
密：`pg addon password garage --name store`（secret key，与上面 `Access key:` 配
对；`--file <path>` 以 0600 写出）。

## 启停

```bash
pg addon start garage --name store
pg addon stop  garage --name store
```

`install` 跳过仍存在的容器（停止的会被启动）；`start` 只启动已存在的容器（并通
过从配置重建来自愈一个处于 improper 状态的容器，同时重新渲染
`garage.toml`）。

## 开机自启

容器带 `--restart unless-stopped` 策略（管崩溃，不管重启）。要在主机重启后拉起实
例：

```bash
pg autostart enable --garage --name store
```

garage 自启会重新渲染 `garage.toml` 并启动已存在的容器。Garage 在上游镜像里以
root 运行，所以开机启动除运行 podman 本身外不需要特殊主机权限。开机是**仅启
动**；garage 独立于 PostgreSQL 栈，所以与其它对象存储一起被启动。

## 移除

```bash
pg addon remove garage --name store            # 容器消失，数据保留
pg addon remove garage --name store --clean-data   # 同时删除两个目录 + 配置
```

两个数据目录**就是对象存储**——丢了它们等于丢了里面每个桶——所以 `remove` 默认
保留。`--clean-data` 删除数据目录、meta 目录和 pgcli 渲染的 `garage.toml`（你自
带 `config_file` 指向的文件从不被碰），并清理已空的默认布局父目录。与 rustfs 不
同，Garage 以 root 写、pgcli 在受管主机上以 root 跑 podman，所以普通删除即可——
没有 `podman unshare` 兜底。

## 日志

```bash
pg logs addon garage --name store      # 最后 50 行
pg logs addon garage --name store -f   # 跟随
```

## 故障排除

- **install 时 `pulling garage image ... : ...`。** tag
  `docker.io/dxflrs/garage:v2.4.1` 不可达——检查到 Docker Hub 的网络，或先用
  `podman pull` 预拉。
- **`pg mc alias set` 报连接错误。** 默认 `--listen 127.0.0.1` 把存储绑在回环
  上；在 macOS 上用 `host.containers.internal` 而非 `127.0.0.1`。用
  `pg addon list` 确认实时 API 端口。
- **经 nginx 前置的写操作报 `Invalid signature`。** 前置把 `Host` 重写成不带端口
  的形式；用 `proxy_set_header Host $http_host;`（[conf-file
  配方](#用-nginx-前置实现-tls)就是这么写的）——`--upstream` 模板模式写死
  `$host`，不适合 S3。
- **经 nginx 前置的 PUT 在 ~1 MiB 处失败。** nginx 默认
  `client_max_body_size` 为 1m；conf-file 配方把它设成 `0`（不限）。
- **pgBackRest 拒绝端点（HTTP / 证书）。** pgBackRest 对 S3 仓库强制 HTTPS。用
  nginx 插件（`--tls`）前置 Garage，并把 `backup.repo.s3.ca_file` 指向前置的
  `ca.crt`。
- **单节点的 `replication_factor` 必须为 1。** 没有 `cluster` 却手改
  `replication_factor: 2` 会在安装时被拒——单节点凑不出多节点 quorum。要跑多节点
  就设 `cluster`（或用 `--cluster` 安装），见[多节点集群](#多节点集群)。
