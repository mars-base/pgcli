---
title: "自签证书"
description: "用 pg cert 签发自签 TLS 证书，并让 store 插件加载它"
weight: 55
icon: fa-solid fa-certificate
menus:
  main:
    identifier: docs-self-cert
    parent: docs
    weight: 55
    params:
      icon: fa-solid fa-certificate
cascade:
  type: docs
  footer_style: slim
---

## 为什么要自签

三个以 TLS 对外提供 S3 的插件——[`minio`](../addon/minio/)、
[`silo`](../addon/silo/)、[`rustfs`](../addon/rustfs/)——可以用两种方式加载证书：

- **自动生成**（`--tls`）：pgcli 替你签一张自签 CA + leaf，托管证书目录，重启时
  刷新。零配置，SAN 固定。
- **自带证书（BYO）**（`--tls-cert` / `--tls-key`）：你提供这一对。公 CA 签发的证书、
  你已有的通配符证书、需要自定义 SAN 的证书，都走这条。

`pg cert` 就是为第二条准备的签发工具：它生成一张自签证书，SAN 恰好覆盖你需要的
主机名和 IP，让你不必有真正的 CA 也能体验 BYO TLS——而且它只写出一对**独立文件，
不注册进 pgcli**：文件落到你指定的路径，`pg.yaml` 不动，也不会拉起任何容器。你得自己
用 `--tls-cert`/`--tls-key` 把它交给 store。

## 快速上手

```bash
# 放证书对的目录（任何可写路径都行；别放数据目录里）。pgcli 的 base dir 本就在
# ~/.pgcli 下：
mkdir -p ~/.pgcli/certs

# 一张对「一个域名 + 两个 IP」有效的 leaf，ECDSA P-256、825 天（均默认）：
pg cert --host "rustfs.test,127.0.0.1,10.0.0.9" \
  --cert-file ~/.pgcli/certs/rustfs.crt \
  --key-file  ~/.pgcli/certs/rustfs.key

# 交给 store 加载（三个之一都行），写全路径：
pg addon install rustfs --name store \
  --tls-cert ~/.pgcli/certs/rustfs.crt \
  --tls-key  ~/.pgcli/certs/rustfs.key
```

`pg cert` 只打印它写入的 SAN，别的什么都不做——它是一个纯粹的文件生成器，不是
插件命令。

## 参数

| 参数 | 默认 | 含义 |
|------|---------|---------|
| `--host` | `127.0.0.1` | 逗号分隔的 DNS 名和/或 IP，编进 SAN（可重复）。每项自动判定是 IP 还是域名，所以 `"rustfs.test,10.0.0.9"` 无需特殊写法；`*.wild.test` 作为通配符 DNS 项有效。 |
| `--cert-file` | `cert.pem` | 写证书 PEM 的路径。 |
| `--key-file` | `key.pem` | 写私钥 PEM（PKCS8，权限 `0600`）的路径。 |
| `--valid-duration` | `825` 天 | 证书有效期，例如 `--valid-duration 8760h` 是一年。 |
| `--ecdsa` | `P-256` | 曲线：`P-224`/`P-256`/`P-384`/`P-521`。置为 `""` 关闭 ECDSA（配 `--rsa` 用）。 |
| `--rsa` | *（关）* | RSA 位数（如 `2048`、`4096`）；仅在你确实需要 RSA 而非默认 ECDSA 时设。 |
| `--ca` | `false` | 让证书成为自己的 CA（`CA:TRUE`、`keyCertSign`）——用于你想要一个私有根去签更多证书，**不是** `--tls-cert` 的常见用法。 |

同一个生成器也编译成独立二进制供 `pg` 之外使用——`make gencert` → `bin/gencert`，
参数完全一致、只是单横线形式（`-host`、`-cert-file` …）。两者背后是同一个包，产出
的证书逐字节同类。

## `pg cert` 产出什么

**一张自签 leaf，不是链。** `--cert-file` 里的 PEM 恰好只有一个 `CERTIFICATE` 块——
证书自己签自己（`CA:FALSE`、`serverAuth` EKU、你的 SAN）。刻意没有
leaf+中间+根 的捆绑：它上面没有签发 CA，也就无链可拼。

这也是为什么 `--ca` 的产物**不该**喂给 `--tls-cert`：`pg addon install … --tls-cert`
会跑 `ValidateBYOCert`，它检查第一张证书并**拒绝一张 CA**。`--ca` 证书是你拿去签
别人的信任锚，不是用来对外 serve 的服务器证书。

## 把它当作客户端的信任锚

因为结果自签，把同一个 `.crt` 作为 CA 文件交给每个 TLS 客户端——被 serve 的 leaf
就是它自己的信任锚。对 pgBackRest 的 S3 仓库，这意味着把 `--s3-ca-file` 直接指向
`pg cert` 的输出：

```bash
pg backup setup --s3-endpoint <store-host>:<api-port> \
  --s3-ca-file ~/.pgcli/certs/rustfs.crt
```

这不是 pgBackRest 勉强容忍的变通：OpenSSL 把你交给它的任何证书都当信任锚（不要求
`CA:TRUE`），pgBackRest 的 S3 TLS 路径就是这套机制。直接验证过：对自签的
`CA:FALSE` leaf 跑 `openssl verify -CAfile <cert> <cert>` 返回 `OK`。

若要把证书放到拨号的那台机器上而不搬文件，用
[`pg backup fetch-ca`](../backup/#s3-对象存储仓库)——它直接从实时 TLS 握手里抽出
签发证书。

## 交给 store 加载

三个 S3 store 各自把你的这一对挂到其二进制要求的文件名下，所以**你的文件名随意**——
`pg cert --cert-file anything.crt` 对三个都成立：

```bash
# MinIO / silo 读 public.crt + private.key：
pg addon install minio --name store --tls-cert anything.crt --tls-key anything.key
pg addon install silo  --name store --tls-cert anything.crt --tls-key anything.key

# rustfs 读 rustfs_cert.pem + rustfs_key.pem：
pg addon install rustfs --name store --tls-cert anything.crt --tls-key anything.key
```

pgcli 从不重新属主、也不写你的源文件；在 rustfs 的情形下这一对以只读挂载、再拷进
容器自己的证书目录（见
[rustfs → 权限与属主](../addon/rustfs/#权限与属主)）。

## 在 rustfs 上实测过

真机安装验证：rustfs serve 的 `pg cert` leaf 经实时 TLS 握手确认（握手实际呈现的
证书 subject == issuer，不只看 `pg.yaml` 记了什么），容器以 HTTPS 起来且健康，
`pg mc` 对它完成 `alias set` → `mb` → put → `ls` → get 全链路，取回的对象与上传时
逐字节一致。

## 续期一张 BYO 证书

单文件 bind 挂载会钉住宿主源 inode，所以就地替换文件对运行中的容器无效。要续期，
重新铸一对（或覆盖文件），再重建：

```bash
pg cert --host "rustfs.test,127.0.0.1" \
  --cert-file ~/.pgcli/certs/rustfs.crt --key-file ~/.pgcli/certs/rustfs.key
pg addon install rustfs --name store \
  --tls-cert ~/.pgcli/certs/rustfs.crt \
  --tls-key  ~/.pgcli/certs/rustfs.key --force
```

**关掉 BYO**（退回自动生成，或纯 HTTP）：从 `pg.yaml` 里删掉该插件的
`cert_file`/`key_file` 行，再 `--force` 重建。

## 另见

- [`minio`](../addon/minio/) · [`silo`](../addon/silo/) ·
  [`rustfs`](../addon/rustfs/)——每个都有「自带证书」小节
- [备份 → S3 对象存储仓库](../backup/#s3-对象存储仓库)——客户端侧的
  `--s3-ca-file` / `fetch-ca`
