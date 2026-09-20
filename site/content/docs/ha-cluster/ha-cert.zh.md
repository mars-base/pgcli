---
title: "生成证书 —— pg cert"
description: "pg cert 签发带域名与 IP SAN 的自签证书，服务于任何不想走 CA 又需要 TLS 的场合——开发测试服务器、内部端点、对外提供自带 TLS 证书的 MinIO。flag 一览，写出的内容（单张自签叶证书，不是一条链），以及它如何充当自己的信任锚"
weight: 54
---

`pg cert` 签发一张**自签证书**——SubjectAltNames 覆盖你要求的任意域名 / IP
组合——适用于任何不想走 CA 又需要 TLS 的场合：开发或测试服务器、内部端点、
客户端归你可控的服务。它写出的就是一张普通的 TLS 服务端叶证书，哪里都能用。

本文档树里演练的用例是 pgBackRest 的 S3 路径：Patroni 集群的备份要推给一个
S3 存储（通常是 MinIO 或 silo），而它必须对外提供 **TLS**——pgBackRest 明确
拒绝明文 S3——pgcli 的 MinIO/silo 插件都支持**自带证书**（`pg addon install minio --tls-cert
... --tls-key ...`）。想快速拿到一张证书来走通这条路，最省事的办法就是
`pg cert`：

```bash
pg cert --host "minio.test,127.0.0.1,10.0.0.9" \
  --cert-file minio.crt --key-file minio.key
```

它不往 pgcli 里注册任何东西——不碰 `pg.yaml`、不起容器——只是把两个文件写到
你指定的路径并打印 SAN。服务侧见
[MinIO → 使用自带证书](../addon/minio/#使用自带证书--tls-cert----tls-key)，端到端跑法见
[示例：HA 集群 + 自签 CA 的 MinIO](../ha-example-minio/)。

## 产出什么

**单张自签叶证书**，不是一条链：

- `--cert-file` 里的 PEM 恰好只有一块 `CERTIFICATE`——这张证书自我签名；
- `CA:FALSE`、扩展密钥用途 `serverAuth`——一张规规矩矩的 TLS 服务端叶证书；
- SAN 自动分流：`--host` 里任何能解析成 IP 的条目落进 IP SAN，其余进 DNS
  SAN，所以 `"minio.test,10.0.0.9"` 不需要特殊语法（`*.wild.test` 会作为
  通配符域名条目）。

因为是自签，**这张叶证书就是它自己的信任锚**——把同一张 `.crt` 交给任何允许
指定 CA 文件/证书串的 TLS 客户端即可（curl `--cacert`、浏览器导入、应用的
`SSL_CERT_FILE`……）。具体到 pgBackRest 的 S3 路径，就是把 `backup.repo.s3.ca_file`
/ `pg backup setup --s3-ca-file minio.crt` 指向它。从没见过这个文件的远端主机
也不必 scp：`pg backup fetch-ca <endpoint>` 能识别 TLS 握手里的自签叶证书，并
直接把它作为锚保存下来。这不是 pgBackRest 勉强容忍的旁门做法：OpenSSL 把交给
它信任库的任何证书都当作锚点，并不要求 `CA:TRUE`，而 pgBackRest 的 S3 路径
（curl 走 OpenSSL）用的正是这一套机制——直接实测过：
`openssl verify -CAfile minio.crt minio.crt` 返回 `OK`。

换成真正的**公共 CA / 私有 CA** 证书，这套自锚机制就都不需要了：锚定在受信
CA 的链根本不需要 `ca_file`，私有 CA 的签发证书你本来就拿在手里。

## flag 一览

| flag | 默认值 | 含义 |
|------|--------|------|
| `--host` | `127.0.0.1` | 逗号分隔的域名和/或 IP，编码进 SAN（可重复传）。条目自动识别是域名还是 IP；`*.wild.test` 会作为通配符域名条目。 |
| `--cert-file` | `cert.pem` | 写出证书 PEM 的路径。 |
| `--key-file` | `key.pem` | 写出私钥 PEM（PKCS8，权限 `0600`）的路径。 |
| `--valid-duration` | 825 天（`19800h`） | 证书有效期，例如 `--valid-duration 8760h` 是一年。 |
| `--ecdsa` | `P-256` | 曲线：`P-224`/`P-256`/`P-384`/`P-521`。置为空字符串则不生成 ECDSA 密钥（改配 `--rsa`）。 |
| `--rsa` | *（关）* | RSA 密钥位数（如 `2048`、`4096`）；只在明确需要 RSA 而非默认 ECDSA 密钥时才设。 |
| `--ca` | `false` | 让这张证书自成一个 CA（`CA:TRUE`、`keyCertSign`）而非服务端叶——见下节。 |

两种密钥类型都不选（`--rsa 0 --ecdsa ""`）是错误：必须有且只有一种产出密钥。

## `--ca` 模式不是给 `--tls-cert` 用的

`--ca` 让证书成为自己的 Certificate Authority——用于你想要一把私有根再去签
别的证书。它**不是**拿来喂 MinIO `--tls-cert` 的东西：`ValidateBYOCert` 会检
查文件里的第一块证书并拒绝 CA 证书——CA 的私钥是签名密钥，不是服务端密钥。
默认（不带 `--ca`）产出的才是你要的服务端叶证书。

## 独立二进制：`gencert`

同一套签发逻辑也编成了一个可脱离 `pg` 使用的独立程序：

```bash
make gencert          # -> bin/gencert
bin/gencert -host "minio.test,10.0.0.9" -cert-file minio.crt -key-file minio.key
```

flag 完全相同，只是单横线形式（`-host`、`-cert-file`……）。两者背后是同一个
`internal/certgen` 包，产出的证书类型一致——`pg cert` 只是让你留在 CLI 里的那
条路径。

## 示例：一台主机上的完整自带证书链路

```bash
# 1. 造一张 MinIO 将要对外提供的证书（SAN 覆盖客户端拨号用的名字与 IP）
mkdir -p ~/.pgcli/certs
pg cert --host "minio.test,127.0.0.1,<主机IP>" --valid-duration 8760h \
  --cert-file ~/.pgcli/certs/store.crt --key-file ~/.pgcli/certs/store.key
chmod 600 ~/.pgcli/certs/store.key

# 2. MinIO 以 HTTPS 提供它
pg addon install minio --name store --listen 0.0.0.0 \
  --tls-cert ~/.pgcli/certs/store.crt --tls-key ~/.pgcli/certs/store.key

# 3. 集群的备份指向它；被提供的那张叶证书本身就是 CA 文件
pg backup setup --s3-endpoint <主机IP>:9000 --s3-bucket pgbackrest \
  --s3-access-key admin --s3-ca-file ~/.pgcli/certs/store.crt
```

第 3 步直接引用你刚造好的 `.crt`，因为本机已经持有它。**从没见过这个文件的
远端主机**则用一次握手把它取回来，无需 scp——`fetch-ca` 能认出 MinIO 提供的
自签叶证书并把它作为锚保存：

```bash
# 在另一台主机上，替代把 ~/.pgcli/certs/store.crt 拷过去：
pg backup fetch-ca <主机IP>:9000
#   [OK] CA fetched from <主机IP>:9000
#        saved:    <base-dir>/backup/repo-ca/ca-<主机IP>-9000.crt
#        SHA-256:  d4df…81e2   （与存储主机的 sha256sum 对拍）
pg backup setup --s3-ca-file <base-dir>/backup/repo-ca/ca-<主机IP>-9000.crt
```

## 相关

- [Silo](../addon/silo/) —— Pigsty 的 MinIO 分支，自带证书形态相同
- [MinIO](../addon/minio/) —— 插件本体，及其
  [使用自带证书](../addon/minio/#使用自带证书--tls-cert----tls-key)
  与[生成测试证书](../addon/minio/#生成测试证书pg-cert)两节
- [示例：HA 集群 + 自签 CA 的 MinIO](../ha-example-minio/) —— 整条链路在一个
  完全隔离的环境里端到端跑通
- [Patroni 集群备份](../ha-backup/) —— 消费这张证书的 S3 仓库与 WAL 归档
- [Patroni HA](../ha/) —— `pg ha` 命令集
