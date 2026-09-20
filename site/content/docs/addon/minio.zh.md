---
title: "MinIO"
description: "以 pgcli 插件方式运行 MinIO——单机或跨主机分布式纠删码集群的 S3 兼容对象存储（含 Web 控制台）"
weight: 49
---

[MinIO](https://min.io) 是 S3 兼容的对象存储。pgcli 将其作为**独立的顶层插件**
运行——共享基础设施，而非实例级 sidecar——带 Web 控制台。默认采用**单机
（single-node）**模式，也支持跨主机的**分布式集群模式**（见下文
[分布式 / 集群模式](#分布式--集群模式)）。两种模式下它都是通用对象存储。
[silo](../silo/)——Pigsty 的 MinIO 分支，保持线路兼容——作为同级插件提供，功能面
完全一致；两者共用一个端口池，可在同一主机并存。

> **平台支持：** MinIO 插件**两个平台都支持**。Linux 上通过主机网络提供服务；
> macOS 上加入 `pgcli-net` bridge 网络并发布两个端口，Mac 用
> `127.0.0.1:<port>` 即可访问 API 与控制台，与其他插件一致。公开镜像为双架构
> （amd64 + arm64），任意主机架构均可运行。[`pg mc`
> 客户端](#使用-mc-客户端)同样两个平台可用。

## 工作原理

一个容器、一个目录：`minio server /data --address <listen>:<api-port>
--console-address <listen>:<console-port>`，其中 `/data` 是实例主机数据目录
（默认 `<base-dir>/addon/minio/<name>/data`）的 bind mount。MinIO 存储的一切
都在这个目录里，因此它的生命周期长于容器。

镜像是公开的预构建 tag `ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226`——
上游静态二进制（双架构 amd64 + arm64，来自 minio/minio 的 GitHub release）跑在
Alpine 上，因为 MinIO 官方镜像移除了内置的 Web 控制台。`pg addon install`
只负责拉取；pgcli 运行时从不构建镜像。

凭据的处理方式与 Patroni 相同：

- `root_user` 默认 `admin`；
- `root_password` **首次 install 时自动生成**（也可以用 `--root-password`
  显式指定），存入 `pg.yaml`
  （`addons.minio.<name>.root_password`），并在安装摘要中**打印一次**方便记录。

二者就是 MinIO root 的 **access key / secret key**——所有 S3 客户端（包括
`pg mc alias set <名称> <URL> <root_user> <root_password>`）所说的 access key
与 secret key 就是它们。

容器按 MinIO 官方部署建议带上 `--ulimit nofile=1048576:1048576` 和
`--stop-timeout 60`；macOS 上 `podman machine` 虚拟机的 RLIMIT_NOFILE 上限更
低，因此该平台改用 `65536`——单机 dev/test 足够。注意 root 凭据通过 `-e` 传
入，会出现在 `podman inspect` 里——与手工运行容器的暴露程度相同；对 rootless
单机部署（本插件的定位）可以接受。

## 安装

```bash
# 默认实例名 "minio"，端口从池中分配（基址 9000），只绑回环
pg addon install minio

# 命名实例 + 显式数据目录
pg addon install minio --name store --data-dir /srv/minio

# 固定端口、指定 root 用户
pg addon install minio --name store --api-port 9000 --console-port 9001 --root-user admin

# 绑定到所有网卡，暴露到网络
pg addon install minio --name store --listen 0.0.0.0

# HTTPS（pgBackRest S3 仓库的前提）：pgcli 自签 CA + 原生 TLS
pg addon install minio --name store --tls

# 用你自己已有的证书对外提供 HTTPS（公共 CA 或私有 CA 签发的域证书）
pg addon install minio --name store --tls-cert /etc/ssl/minio.test.crt --tls-key /etc/ssl/minio.test.key
```

输出会报告端点与 root 凭据：

```
✓ minio installed: "store"
  Container:    pgcli-minio-default-store
  Image:        ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226
  Data:         ~/pg/addon/minio/store/data
  S3 API:       http://127.0.0.1:9000
  Console:      http://127.0.0.1:9001

  Root user:     admin
  Root password: <generated>
```

用打印出的 root 用户与密码登录 `Console:` 地址的控制台。S3 客户端
（包括 pgBackRest）指向 `S3 API:` 地址即可，也可以在终端用下文的
[`pg mc`](#使用-mc-客户端)直接操作。

对**已存在**的实例重复执行 install 是无损的：不会重建容器（处于停止状态的会
直接启动并给出提示），命令行参数会合并进已存配置，**已有的** root 密码保持
不变。要让改过的端口、监听地址或凭据生效，加 `--force` 重建容器（数据目录不受
影响）。

> **绑定地址：** 默认 `127.0.0.1`，存储仅本机可见。`--listen 0.0.0.0`（或
> `pg.yaml` 里的 `listen` 键）会把它暴露到网络上——能访问该端口的任何人都可尝试
> root 凭据，因此只应在防火墙后或 TLS 终结代理之后这样暴露。

### TLS（`--tls`）

`--tls` 让 MinIO 以 HTTPS 提供服务：pgcli 用标准库生成一把自签 CA 和一张叶证
书（SAN 覆盖回环名、`localhost` 与本机所有网卡 IP），落在
`<base_dir>/tls/minio/<name>/`（`public.crt` / `private.key` 供 MinIO
`--certs-dir` 用，`ca.crt` 供分发），随后把证书目录只读挂进容器。端点 URL 随
之变为 `https://`。

之所以需要它：**pgBackRest 对 S3 仓库强制 HTTPS**（明文被上游明确拒绝），所以
一个要接收 Patroni `archive-push` 的 MinIO 必须说 TLS。把 `ca.crt` 路径填进
`backup.repo.s3.ca_file`，pgBackRest 就会以完整证书校验连接它——见
[备份 → S3 对象存储仓库](../../backup/#s3-对象存储仓库)。

关于"store 与集群怎么配对"的一点说明：`backup.repo.s3` 是一份配置文件里唯一
的全局仓库，所以最先接上的那台 TLS MinIO 会收到该环境里的**所有** stanza——
再装一台 MinIO 并不会让集群们多出一个可按集群选择的 store。确需两个目的地
时，跑第二份配置（见[备份 → 共享备份容器](../../backup/#共享备份容器)，以及完
整示例[示例：HA 集群 + 自签 CA 的 MinIO](../../ha-cluster/ha-example-minio/)）。

`pg mc` 在命令通过回环别名访问 TLS 存储时会自动附加 `--insecure`（`mc` 无法
按别名持久化 CA 信任；同机 `127.0.0.1` 回环可接受）。指向局域网 IP 的别名不算
回环——需自己加 `-- --insecure`，或正确配置 CA 信任。外部 `https://` 端点仍
做完整校验。给已存在的实例开关 `--tls` 需要 `--force` 重建容器才生效。

**有效期与跨主机访问。** CA 与叶证书均有效期 100 年（公共 CA 需遵守的 825 天
上限是浏览器策略，Go 校验器对私有根 CA 并不强制）。pgcli 会在 `--force` 时、
或主机地址变化时重签叶证书——因此对每个存储，分发 `ca.crt` 只需做一次。TLS
客户端校验的是**它所拨号的目标地址**是否匹配叶证书的 SAN，客户端自身地址从不
出现在任何证书里。所以远端主机（虚拟机里的 Patroni
成员、另一台机器上的备份容器）把 `backup.repo.s3.endpoint` 指向存储主机的
某个 IP 即可（回环名与本机所有网卡 IP——含虚拟网桥——都在 SAN 里；裸主机名
不在，除非设置了 `MINIO_SERVER_URL`，其 host 会被追加进 SAN）。

**远端主机取 CA——无需 scp。** 服务端证书以"叶 + CA"链形式下发，所以另一台机器
上的消费方可以直接从 TLS 握手里把根证书拉回来：

```bash
pg backup fetch-ca <存储主机>:9002
#   [OK] CA fetched from <存储主机>:9002
#        saved:    ~/.pgcli/backup/repo-ca/ca-<存储主机>-9002.crt
#        SHA-256:  c0f0…fe2e
pg backup setup --s3-ca-file ~/.pgcli/backup/repo-ca/ca-<存储主机>-9002.crt
```

这次拉取本质是"首次使用即信任"（TOFU）——在你拿到 CA 之前，没有任何东西能
验证它——所以信任前请把打印出的 SHA-256 与存储主机上
`sha256sum ~/.pgcli/tls/minio/<name>/ca.crt` 的结果对拍（如同核对 SSH 主机指
纹）。之后一次 `setup` 会把 CA 发布进 Patroni 集群的 etcd 注册表，集群里其余
主机自动拉取、零手工步骤（见[备份 → S3 对象存储仓库](../../backup/#s3-对象存储仓库)）。手工拷
贝 `ca.crt` 仍是可用的兜底路径，也是存储主机跑的是"链下发"之前的旧 pgcli 时的
唯一选择——`fetch-ca` 的报错会明确指出这一点，在存储主机上重跑
`pg addon install minio --tls` 即可升级（证书热重载，无需重启）。pgBackRest
stanza 从不要求 MinIO 与它同机。

### 使用自带证书（`--tls-cert` / `--tls-key`）

`--tls` 只会下发 pgcli 自签的那对证书。如果你想对外提供自己已有的证书——某
个公共 CA 为真实域名签发的，或你的私有 CA 签发的——改用
`--tls-cert <leaf(+链).pem> --tls-key <key.pem>`。它与 `--tls`（会被隐式打开，
无需重复传）配合，取代生成的证书：两个文件被只读挂载到 MinIO `--certs-dir`
要求的文件名上（`/opt/minio/certs/public.crt`、`/opt/minio/certs/private.key`），
pgcli 从不复制或重签它们——私钥只留在你放置的那一个地方。

```bash
pg addon install minio --name store \
  --tls-cert /etc/ssl/wildcard.hi.163.com.crt \
  --tls-key  /etc/ssl/wildcard.hi.163.com.key
```

**安装时校验什么、不校验什么。** `--tls-cert`/`--tls-key` 会在任何容器启动前，
通过 Go 自身的 TLS 加载器配对校验，所以密钥与证书不匹配、证书其实只是一张
CA、证书已过期、或证书被限定用于服务端以外的用途——都会在安装时直接失败，而
不会延后成一个反复崩溃重启的容器。证书 SAN 是否覆盖所配置的 `--listen` 地址
也会被检查，但只作告警——域证书常常是通过 DNS 或负载均衡器背后的名字被访问
的，与它运行在哪台主机无关，所以这里的不匹配是提示、不是致命错误。

**续期。** 替换证书/密钥文件后重建容器：

```bash
pg addon install minio --name store --tls-cert <new.crt> --tls-key <new.key> --force
```

重建是必需、而非可选的：文件以只读方式 bind 进容器后，运行中的 MinIO 不会
感知被替换的证书——既不会感知"写新文件再覆盖旧文件"（那会换掉单文件挂载所钉
住的 inode），甚至连原地重写同一个文件也不感知。已实测：改写宿主文件后，容器
仍继续下发旧的证书对，直到 `--force` 重建为止。pgcli 从不重签自带证书——证书
生命周期由运维方掌握——所以没有可依赖的自动刷新。

**客户端。** 用受公共 CA 签发的证书时，S3 客户端（`mc` 系、`aws` CLI、
pgBackRest 经 `repo*-s3-ca-file`）完全不需要额外信任材料——信任链本就锚定在
它们已信任的 CA 里。其余证书则把信任锚交给客户端的
`backup.repo.s3.ca_file` / `--s3-ca-file`：私有 CA 证书交签发 CA（或完整的
叶+中间证书链）；自签证书既然自身即锚，`pg cert` 造的那张就直接交被服务的
`.crt` 本身。从没见过这个文件的远端主机，可以不用 scp，直接从 TLS 握手里自动
把它拉回来——`fetch-ca` 会识别自签叶证书并替你保存：

```bash
pg backup fetch-ca <存储主机>:9010
#   [OK] CA fetched from <存储主机>:9010
#        saved:    ~/.pgcli/backup/repo-ca/ca-<存储主机>-9010.crt
#        SHA-256:  d4df…81e2
pg backup setup --s3-ca-file ~/.pgcli/backup/repo-ca/ca-<存储主机>-9010.crt
```

请把打印的 SHA-256 与存储主机上对你交给 `--tls-cert` 的那个 `.crt` 跑
`sha256sum` 的结果对拍（首次使用即信任，与上面取回生成 CA 的用法同理）。公共
CA 证书则仍然用不上这条命令——没有需要去取回、且尚未被信任的东西。

**关闭自带证书模式。** 没有 `--tls-cert=`/关闭 这类 flag——跨重跑的配置合并是
单向的，与 `--tls` 本身的行为一致。要回到生成证书模式，从 `pg.yaml` 里该实例
下删掉 `cert_file`/`key_file`，再用 `pg addon install minio --name store --force`
重建即可。

### 生成测试证书：`pg cert`

尝试自带证书模式不必先有一套真的 CA——`pg cert` 签发一张自签证书，SAN 覆盖你
要求的任意域名 / IP 组合：

```bash
# 一张同时对一个主机名和两个 IP 生效的叶证书，ECDSA P-256（默认），
# 825 天有效期（默认）——这正是多数自带证书场景要的形态：
pg cert --host "minio.test,127.0.0.1,10.0.0.9" \
  --cert-file minio.crt --key-file minio.key

# 然后对外提供它：
pg addon install minio --name store --tls-cert minio.crt --tls-key minio.key
```

它产出的任何东西都不碰 `pg.yaml`、也不碰容器——只是往你指定的路径写两个 PEM
文件并打印 SAN。flag 一览：

| flag | 默认值 | 含义 |
|------|--------|------|
| `--host` | `127.0.0.1` | 逗号分隔的域名和/或 IP，编码进 SAN（可重复传）。条目会自动识别是域名还是 IP，`"minio.test,10.0.0.9"` 不需要特殊语法；`*.wild.test` 会作为通配符域名条目。 |
| `--cert-file` | `cert.pem` | 写出证书 PEM 的路径。 |
| `--key-file` | `key.pem` | 写出私钥 PEM（PKCS8）的路径。 |
| `--valid-duration` | 825 天（`19800h`） | 证书有效期，例如 `--valid-duration 8760h` 是一年。 |
| `--ecdsa` | `P-256` | 曲线：`P-224`/`P-256`/`P-384`/`P-521`。置为空字符串则不生成 ECDSA 密钥（配合 `--rsa` 用）。 |
| `--rsa` | *（关）* | RSA 密钥位数（如 `2048`、`4096`）；只在明确需要 RSA 而非默认 ECDSA 密钥时才设。 |
| `--ca` | `false` | 让这张证书自成一个 CA（`CA:TRUE`、`keyCertSign`）——用于你想拿它当私有根再去签别的证书，而非常规的 `--tls-cert` 用法。 |

同一套签发逻辑也编成了一个可脱离 `pg` 使用的独立二进制：`make gencert` →
`bin/gencert`，flag 完全相同、只是用单横线形式（`-host`、`-cert-file`……）。两
者背后是同一个 `internal/certgen` 包，产出的证书类型逐字节一致。

**`pg cert` 写出的是单张自签叶证书，不是一条链。** `--cert-file` 里的 PEM 恰好
只有一块 `CERTIFICATE`——这张证书自我签名（`IsCA: false`、`serverAuth` EKU、
你要求的 SAN）。它有意不是"叶+中间+根"的打包链：它上面没有签发 CA，所以没有
东西可以拼进链；`ValidateBYOCert` 只看文件里的第一块证书，并拒绝 CA 证书
（`leaf.IsCA`）——这也正是 `--ca` 产出的那张证书**不能**拿去喂 `--tls-cert`
的原因：它是信任锚本身，不是服务端证书。

既然产出是自签证书，在客户端侧就把生成的 `minio.crt` 当作 pgcli 自己为
`--tls` 生成的那张 `ca.crt` 同样使用——被下发的那张叶证书*本身*就是自己的
信任锚，所以 `backup.repo.s3.ca_file` / `pg backup setup --s3-ca-file` 直接
指向同一个文件即可（见
[备份 → S3 对象存储仓库](../../backup/#s3-对象存储仓库)）。这不是 pgBackRest
勉强容忍的旁门做法：OpenSSL 的信任库把通过 `-CAfile`/`SSL_CTX` 交给它的任何
证书都当作锚点，并不要求 `CA:TRUE`，而 pgBackRest 的 S3 TLS 路径（curl 走
OpenSSL）用的正是这一套机制——直接实测过：对 `pg cert` 产出的自签、`CA:FALSE`
叶证书跑 `openssl verify -CAfile <证书> <证书>`，返回 `OK`。

## 部署形态

MinIO/silo 对自身的部署布局有明确分类；本插件支持其中最常用的两种：

| 形态 | 结构 | 适用场景 |
|------|------|----------|
| **SNSD**（单机单盘） | 单节点、单个数据目录——不给 `--endpoint` 时的默认 | 开发、测试、演示 |
| **MNSD**（多机单盘） | 多节点、每节点一块数据盘——即下文的分布式模式 | 紧凑的高可用部署 |

`pg addon install minio` 开箱即是 SNSD。要得到 MNSD，传入集群的
endpoint 列表（至少四节点）——见下文[分布式 / 集群模式](#分布式--集群模式)。

MinIO 其实还有多盘的形态（**SNMD**——单机多盘，以及分布式集群里每台主机挂多
块盘）；pgcli 没有把它们做进插件：`--endpoint` 每节点一个主机、`--data-dir`
是单个目录。这是有意收的范围——磁盘冗余该放在数据目录之下，由 ZFS 来做，更
灵活，而且同时服务两种形态。[S3 存储高可用方案](../../ha-cluster/ha-s3-storage/)
讲清了这份取舍的理由和 ZFS 配方，包括"4 主机、每主机多块盘"这个既能扛坏盘
又能扛坏机的混合形态。

## 分布式 / 集群模式

也支持 MinIO 的纠删码（EC）集群模式。它要求**至少 4 个互不相同的
`host:port` 端点**，并且与其他插件不同，**没有中心协调者**：每台主机各自跑
一份 pgcli、各自一份 `pg.yaml`，而每一份 `pg.yaml` 都完整写入*同一份*端点
列表和*同一套* root 凭据。pgcli 只负责拉起本机对应的那一个容器——跨主机的
组环握手由 MinIO 自己完成。

端点列表就是模式开关：为空即单机（行为不变），非空则以
`minio server <ep1> <ep2> ...` 进入分布式模式。

每个端点的形式是 `http://<host>:<port><路径>`。`host:port` 是节点之间互相
访问、完成组环握手的地址。尾部那段 `<路径>` **不是** HTTP 路由——客户端永远
看不到它——它是 **export path（导出路径）**，即该节点把自己那一份纠删码分片
数据存放在**容器内**的哪个目录。pgcli 总是把 `--data-dir` 挂载到容器的
`/data`，所以这段路径必须从 `/data` 开始——最简单就直接写 `/data`。四个端点里
这段路径字符串要保持一致。

注意端点里的路径是**容器内**路径，与宿主目录结构无关：如果 `/data` 是一块
共享盘、还想在上面放别的东西，把 `--data-dir` 指到它的子目录即可——
`--data-dir /data/minio` 让本集群的数据落在宿主的 `/data/minio`，而端点仍然
写 `http://<host>:9000/data`（端点无法指名这个子目录，它看到的是挂载根）。
只有当你有意往卷内部再扩展一层（比如 `/data/mystore`）时，数据才会在宿主上
多落一层到 `<data-dir>/mystore`——避免把 `--data-dir /data/minio` 和端点
`.../data/minio` 搭配使用，那会嵌套成 `/data/minio/minio`。

```bash
# 节点 1（10.0.0.11），宿主上独立数据盘已挂载到 /data：
pg addon install minio --name store \
  --listen 10.0.0.11 \
  --data-dir /data \
  --root-password '<共享密码>' \
  --endpoint http://10.0.0.11:9000/data \
  --endpoint http://10.0.0.12:9000/data \
  --endpoint http://10.0.0.20:9000/data \
  --endpoint http://10.0.0.21:9000/data

# 节点 2-4：同样的命令、各自的 --listen、完全相同的 --endpoint 列表，以及
# 完全相同的 --root-password 值。
```

`--root-password` 是可选的：不传的话首次 install 会自动生成一个（打印一次、
存入 `pg.yaml`）。集群模式下每台传同一个值，就能保证全集群凭据一致，不需要
任何手工复制。

安装摘要会列出成员并提醒一致性要求：

```
✓ minio installed: "store"
  ...
  Distributed mode: 4 endpoints
    - http://10.0.0.11:9000/data
    - http://10.0.0.12:9000/data
    - http://10.0.0.20:9000/data
    - http://10.0.0.21:9000/data
  NOTE: every node's pg.yaml must carry the identical endpoint list AND
  identical root credentials, or the cluster will not form.
```

集群模式有三条硬性约束，都是实测踩出来的：

- **端点必须是可路由、互不相同的主机。** 同一主机会折叠成"单机多盘"并被
  拒绝（`use path style endpoint for single node setup`）；`127.0.0.0/8`
  回环段直接被拒（`resolves to localhost`）。请用各节点真实的局域网地址。
- **数据目录必须位于与根文件系统不同的磁盘上。** MinIO 拒绝与系统盘同设备
  的驱动器（`drive is part of root drive, will not be used`）。pgcli 会用
  数据目录与 `/` 的 `stat` 做检测，在 `--data-dir` 落在根设备时于 install
  时给出警告——警告是提示性的、不阻断；请把 `--data-dir` 指向独立挂载的
  数据盘。
- **集群模式下不下发 `MINIO_SERVER_URL`。** 它在各节点本就不同（各自宣告
  自己的 IP），而 MinIO 要求所有 `MINIO_*` 环境变量逐节点字节一致，否则每台
  都会卡在 `Waiting for at least 1 remote servers with valid configuration`。
  地址由端点列表决定。单机模式仍按 `listen` 设置 `MINIO_SERVER_URL`。

**Quorum（EC）：** 写需要 `⌈N/2⌉+1` 个节点在线，读需要 `⌈N/2⌉`。因此 4 节点
集群在挂掉 2 台时仍可读、但拒绝写；重启下线的节点后集群自愈。

> **仅跨主机。** 这是真正的分布式部署——每台主机由你自己跑 pgcli。pgcli 不会
> 在节点间 SSH、也不做成员注册；保持 N 份 `pg.yaml` 一致是运维的职责。

## 使用 mc 客户端

`pg mc` 在一次性容器里运行 MinIO 自家的 `mc` 客户端——不用本地安装，也不用
手敲 `podman run`：

```bash
# 按平台选 URL —— 见下文"各平台的端点"：
pg mc alias set store http://127.0.0.1:9000 admin <密码>    # Linux，本机 addon
pg mc alias set store http://host.containers.internal:9000 admin <密码>  # macOS，本机 addon
pg mc alias set store http://10.0.5.7:9000 admin <密码>     # 两者皆可，远端存储
pg mc mb store/backups
pg mc ls store
pg mc cp ./dump.pglz store/backups/
```

别名持久化保存在宿主的 `~/.mc/config.json`——这是 `mc` 自己的默认路径，
Linux 和 macOS 都一样——`alias set` 一次，之后每次 `pg mc`（以及 Linux 上
原生安装的 `mc`）都能看到同一批别名。`pg mc alias list` 读回列表，
`pg mc alias remove` 删除单个别名。`alias set` 会先拿凭据向端点校验，通过才
写文件——密码打错不会在配置里留下一个用不了的别名。`pg mc` 底层做的事只是把
你的 `~/.mc` 挂载进容器，然后运行 `ghcr.io/mars-base/pgcli/pgcli-mc`（静态上
游二进制 + `scratch`，首次使用时自动拉取）；没有别的魔法。

`cp` / `mirror` / `diff` 的本地文件参数同样可用：`pg mc` 会把每个本地路径按
realpath 解析，并把该绝对路径原样挂载进容器，因此

```bash
pg mc cp ./dump.pglz store/backups/        # 从当前目录上传
pg mc cp store/backups/dump.pglz ./        # 下载到当前目录
pg mc mirror ./repo store/backups/         # 或镜像整棵目录树
```

所见即所得。下载到尚不存在的路径（新文件名、或还没建的 `./newdir/`）也没问
题——挂载的是它最近的已存在父目录，mc 写出的文件会落回宿主。macOS 上路径必须
位于家目录之下——那是 `podman machine` 虚拟机唯一共享的目录树；家目录之外的
路径 `pg mc` 会明确提示并跳过。

任何 `pg` 自己解析器会拒绝的 `mc` 标志都要放到 `--` 之后，与 `pg etcdctl` 的
约定完全一致。`mc` 的长标志（`--all`、`--json`、`--recursive`、`--force`、
`--newer-than` 等）pg 一个都不认识，因此全部放 `--` 之后：

```bash
pg mc ls store -- --all
pg mc cp ./dir store/backups/ -- --recursive
pg mc rm store/old -- --recursive --force
```

放在 `--` 之前会在 `pg` 这一层就被拦下，报错形如 `unknown flag: --recursive`
——看到它就说明分隔符漏了。

原生 mc 有个与本地挂载相关的注意点值得知道：在文件类命令（`cp` / `mirror` /
`diff`）里，首段不是已知别名的参数一律按本地路径处理。所以别名拼错时不会报
错，而是安静地上传/下载到当前目录下一个以拼错名字命名的本地目录——`pg mc`
只是忠实挂载了 mc 判定要读的路径，这是原生行为而非 `pg mc` 的怪癖。执行文件
操作前先用 `pg mc alias list` 确认别名存在。

`MC_HOST_<name>` 环境变量形式的别名——`mc` 的无状态用法，不碰配置文件——同样
适用于 `pg mc`，因为该变量会被转发进容器：

```bash
MC_HOST_store="http://admin:<密码>@127.0.0.1:9000" pg mc ls store
```

> **各平台的端点：** Linux 上 `pg mc` 走主机网络，`127.0.0.1` 别名可直达本机
> addon 实例。macOS 上容器位于 bridge 网络，`127.0.0.1` 是容器自己的回环——
> 指向 Mac 本机 addon 的别名请用
> `http://admin:<密码>@host.containers.internal:9000`，远端存储则用其可路由地
> 址。`pg mc` 本身在两个平台上行为一致。
>
> Mac 浏览器访问控制台走的是宿主机侧的 `127.0.0.1:<console-port>`（插件已发布
> 该端口），这与 `pg mc` 容器能不能用 `127.0.0.1` 无关——那只是宿主机自己的回环。

### 常用命令

| 命令 | 用途 |
|------|------|
| `pg mc ls store` / `pg mc ls store/backups` | 列出桶 / 对象 |
| `pg mc mb store/backups` | 创建桶 |
| `pg mc cp ./file store/backups/` | 上传（目录树用 `pg mc mirror ./dir store/backups/`，或给 `cp` 加 `-- --recursive`） |
| `pg mc cp store/backups/file ./` | 下载 |
| `pg mc find store -- --name '*.pglz'` | 按名字搜索对象 |
| `pg mc du store` | 统计各桶大小 |
| `pg mc rm store/backups/file` | 删除单个对象 |
| `pg mc rm store/prefix -- --recursive` | 批量删除对象 |
| `pg mc rb store/bucket -- --force` | 连对象一起删桶 |

同一套 root 凭据适用于任何 S3 SDK——包括 pgBackRest 的 `repo1-type=s3`
仓库（见[备份](/docs/backup/)、[恢复](/docs/restore/)）。

## 端口

每个实例从同一个端口池取**两个连续端口**，基址为 `minio_start_port`（默认
**9000**）：先 S3 API、后控制台。与其他自动分配池一样，会跳过已占用端口与同级
实例的显式端口：

```bash
pg addon install minio --name store    # 9000 / 9001
pg addon install minio --name archive  # 9002 / 9003
```

## 配置

实例存放在 `pg.yaml` 顶层 `addons.minio` 映射中，以实例名为键：

```yaml
namespace: default
minio_start_port: 9000
addons:
  minio:
    store:
      container_name: pgcli-minio-default-store
      name: store
      image_tag: ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226
      # data_dir: /srv/minio     # 省略则为 <base-dir>/addon/minio/store/data
      listen: 127.0.0.1
      api_port: 9000
      console_port: 9001
      root_user: admin
      root_password: <generated>   # 首次 install 时写入
      autostart: false             # pg autostart enable --minio --name store
      # tls: true                  # 以 HTTPS 提供服务（自签 CA，或下面的自带证书）
      # cert_file: /etc/ssl/minio.test.crt   # 自带叶证书(+链)，隐含 tls；见"使用自带证书"
      # key_file:  /etc/ssl/minio.test.key   # 自带私钥，须与 cert_file 配对
      # endpoints:                 # 省略即单机；见"分布式 / 集群模式"
      #   - http://10.0.0.11:9000/data
      #   - http://10.0.0.12:9000/data
      #   - http://10.0.0.20:9000/data
      #   - http://10.0.0.21:9000/data
```

修改 `listen`、端口、`root_user`、`root_password`、`image_tag`、`data_dir`、
`tls`/`cert_file`/`key_file` 或 `endpoints` 后，执行
`pg addon install minio --name store --force` 即生效——普通 install 会跳过已存在
的容器，`--force` 会重建它（数据目录永不受影响）。

### 查看列表

```bash
pg addon list
```

```
Infra add-ons (minio):
  minio (name: store)
    Status:      running
    Listen:      127.0.0.1
    API port:    9000
    Console port: 9001
    Console URL: http://127.0.0.1:9001/
    Data:        ~/pg/addon/minio/store/data
    Root user:   admin
    Image:       ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226
    Container:   pgcli-minio-default-store
```

`pg addon list` 从不打印密码——请到 `pg.yaml` 里查看。

## 启动与停止

主机重启后，不重新下发配置即可拉起实例：

```bash
pg addon start minio --name store
pg addon stop  minio --name store
```

`install` 会跳过已存在的容器（处于停止状态的直接启动）；`start` 只启动已有
容器（状态异常时依据配置自动重建）。

## 开机自启

容器带 `--restart unless-stopped` 策略（管崩溃、不管重启）。主机重启后自动拉起
实例：

```bash
pg autostart enable --minio --name store
```

这会置 `autostart: true` 并安装/刷新 boot service（见
[开机自启](/docs/autostart/)）。开机路径是**只启动**语义。MinIO 与 PostgreSQL
栈相互独立，因此排在最后启动，无顺序依赖。`pg autostart status` 列出所有目标
的状态。

## 移除

```bash
pg addon remove minio --name store               # 只删容器，数据保留
pg addon remove minio --name store --clean-data  # 连数据目录一起删除
```

数据目录**就是对象存储本身**——丢了它等于丢掉里面所有 bucket——因此 `remove`
默认保留它并打印其位置。`--clean-data` 会删除它（并清理默认布局下已清空的父
目录；显式 `data_dir` 及其父目录永不受影响）。

## 日志

```bash
pg logs addon minio --name store      # 最近 50 行
pg logs addon minio --name store -f   # 持续跟踪
```

MinIO 日志走 stdout：启动行（`API:`/`Console:` 地址、`Documentation:`）与请求
错误。看到 `API: http://...` 块即确认监听已就绪。

## 故障排除

- **install 报 `pulling minio image ... : ...`。** 公开 tag
  `ghcr.io/mars-base/pgcli/pgcli-minio:20250422221226` 拉不到——检查网络/registry
  可达性，或先 `podman pull` 手动拉取。
- **手动 `--api-port` 撞端口。** 请选端口池（`minio_start_port` 起）之外的端口，
  否则会被自动分配器视为已占用；另注意 MinIO 需要的是一对连续端口。
- **主机重启后 `pg addon start` 无效果/失败。** 看
  `pg logs addon minio --name store -f`——多半是数据目录被删了（`--clean-data`
  或人为），MinIO 拒绝在曾格式化过的空目录上启动；或者绑定端口变了。
- **控制台能打开，但 S3 客户端超时。** 单机模式下 `MINIO_SERVER_URL` 由
  `listen` + API 端口拼成；若绑在 `127.0.0.1` 却从其他主机访问，客户端会被
  重定向到回环地址。把 `listen` 设为客户端真正可达的地址。（集群模式完全
  不下发 `MINIO_SERVER_URL`——见[分布式 / 集群模式](#分布式--集群模式)。）
- **集群卡在 `Waiting for at least 1 remote servers with valid
  configuration`。** 节点之间配置不一致。逐台用 `podman inspect
  pgcli-minio-<ns>-<name> --format '{{json .Config.Env}}'` 核对每份
  `pg.yaml` 的 `endpoints` 列表与 `root_password` 是否字节级一致，并确认
  没有残留的每节点 `MINIO_SERVER_URL`。
- **macOS。** 已支持：插件加入 `pgcli-net` bridge 并发布两个端口，Mac 用
  `127.0.0.1:<port>` 即可访问（容器内部绑 `0.0.0.0`，`MINIO_SERVER_URL` 宣告
  Mac 使用的回环地址）。改过端口或凭据后用 `--force` 重建容器。用 `pg mc` 时
  注意容器内 `127.0.0.1` 别名不可用——见上文端点说明。
