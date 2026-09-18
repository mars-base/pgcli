---
title: "示例：HA 集群 + 自签 CA 的 MinIO"
description: "完整且实测通过的端到端流程：单 member 的 Patroni 集群，备份到一台对外提供自带（自签）域名证书的 MinIO 插件——全程运行在一个完全隔离的 pgcli 环境中"
weight: 53
---

这是一页**实操示例**，每一步都真实跑通并验证过：创建 Patroni HA 集群，架一台
对外服务**自带域名证书**的 MinIO 插件（这里用我们自己造的自签 CA——与公共
CA、企业 CA 证书同一种形态，只是没去买），并把集群的 pgBackRest 备份与 WAL
归档指向它。下面每条命令都是实跑过的命令；只有主机名、端口、密码和证书材料
做了泛化处理。

有两个观念让这页值得细读而非略读：

- **存储端自带 TLS 证书。** MinIO 插件不必只服务 pgcli 生成的自签证书对——
  `--tls-cert` / `--tls-key` 可以让它服务你已有的证书。信任签发 CA 的客户端
  无需任何额外材料；私有（这里是自签）CA 则把该 CA 经 `--s3-ca-file` 交给备
  份栈——与用生成证书时完全一样。见
  [MinIO → 使用自带证书](../addon/minio/#使用自带证书--tls-cert----tls-key)。
- **用第二个配置文件做隔离。** `backup.repo.s3` 是一份 pgcli 环境里**全局唯
  一**的仓库，被所有 stanza 共享。把它改指向一个新 store，会连带把该主机上所
  有既有集群的备份目的地一起悄悄改掉。在跑着生产环境的机器上试新东西的正确
  姿势是**独立配置文件**——自己的 `base_dir`、`namespace` 和端口段。本示例就
  是这么做的。

## 我们搭建的环境

| 部件 | 取值 | 说明 |
|------|------|------|
| 配置 | `~/.pgcli-app1/pg.yaml` | 与生产的 `~/.pgcli/pg.yaml` 分开 |
| 数据根目录 | `/home/fish/bucket/pgcli-data-app1` | 完全不碰生产数据目录 |
| 命名空间 | `app1` | 给所有容器名加前缀——`pgcli-minio-app1-store1`、`pgcli-patroni-app1-app1-nodea`、`pgcli-backup-app1` |
| DCS | 复用生产环境运行中的 etcd `m1`（`127.0.0.1:2379`） | 早于本示例就在生产配置里创建——见[前置](#前置--etcd-dcs-是怎么来的)；此处以*外部*端点复用，集群成员按 scope 归组，新 scope 天然隔离 |
| MinIO | `store1`，端口 9010/9011，监听 `0.0.0.0` | 自带自签证书，SAN 为 `minio1.test,127.0.0.1,<主机IP>` |
| Patroni | scope `app1`、成员 `nodea`、PG `<主机IP>:35632` | 单成员，即 Leader |
| 备份容器 | `pgcli-backup-app1` | 与生产的 `pgcli-backup-default` 并存 |

## 前置 —— etcd DCS 是怎么来的

示例复用了这台主机上早已为生产集群服务的那套 etcd。若是从零开始，第一块要
装的也是这个插件，一次一个成员——单成员就是一个健康的单节点 etcd 集群，跑
HA 开发/测试绰绰有余：

```bash
pg addon install etcd --name m1
# pgcli-etcd-default-m1，client 127.0.0.1:2379，peer 2380，cluster "pgcli-etcd"
```

（如果要把这个 DCS 交给其他主机的成员加入，安装时就给它一个可达地址——
`--advertise-host <主机IP>`——让它的 peer/client URL 播报该 IP 而非回环；同
法 `--name m2`/`--name m3` 就能扩成真正的 3 节点组。本示例都不需要：集群与
它同机，拨的是 `127.0.0.1:2379`。）

## 第 0 步 —— 一张域名证书

任何 PEM 叶证书 + 密钥都行。演示用 CFSSL 的 `generate_cert` 造了一张自签的
（有效期 1 年，SAN 覆盖客户端将要拨号的名字与 IP）：

```bash
mkdir -p ~/.pgcli-app1/certs && cd ~/.pgcli-app1/certs
generate_cert -host "minio1.test,127.0.0.1,<主机IP>" -duration 8760h \
    # 产出 cert.pem + key.pem
mv cert.pem store1.crt && mv key.pem store1.key && chmod 600 store1.key

openssl x509 -in store1.crt -noout -subject -issuer -ext subjectAltName -dates
# subject=O = Acme Co
# issuer=O = Acme Co                     <- 自签：叶证书与根证书就是同一张
# DNS:minio1.test, IP Address:127.0.0.1, IP Address:<主机IP>
```

若是**公共 CA**（或企业 CA）证书，这一步就只是"文件你本来就有"——把 flag 指
向你的 `fullchain.pem`（先叶、后中间证书）和私钥即可，客户端侧反而更简单：锚
定在受信 CA 的链**根本不需要** `--s3-ca-file`。

## 第 1 步 —— 隔离环境

```bash
mkdir -p ~/.pgcli-app1
pg config init -o ~/.pgcli-app1/pg.yaml \
    --base-dir /home/fish/bucket/pgcli-data-app1 \
    --namespace app1 \
    --pg-start-port 36500 --pg-ssh-port 43500
```

`--namespace` 让这份配置创建的所有容器名都与众不同，于是同一台 podman 上可以
并排跑两套环境。后续每条命令都要带 `-c`——**本页此后的每个 `pg` 都指
`pg -c ~/.pgcli-app1/pg.yaml`**。

> **端口段。** 默认端口池（etcd 2379、MinIO 9000、Patroni 35532……）与生产配
> 置的起始值相同，而 host 网络容器的回环端口会被 podman 发布出来——所以处处
> 显式给端口、两套环境互不重叠，别让它们自动分配到同一批数字。

## 第 2 步 —— 服务自带证书的 MinIO

```bash
pg addon install minio --name store1 \
    --api-port 9010 --console-port 9011 --listen 0.0.0.0 \
    --tls-cert ~/.pgcli-app1/certs/store1.crt \
    --tls-key  ~/.pgcli-app1/certs/store1.key
```

```
-> MinIO BYO cert: CN="" issuer="" valid 2026-09-18 → 2027-09-18
  [OK] TLS certs (BYO: /home/…/.pgcli-app1/certs/store1.crt, valid 2026-09-18 → 2027-09-18)
         self-signed: clients pin the issuing CA (pg backup setup --s3-ca-file <ca.pem>)
  [OK] MinIO container started
```

安装期校验会把证书与密钥配对（不匹配、已过期、CA 当叶用、仅限非服务端用途的
证书都在此处直接失败），打印有效期；又因为是自签证书，提示客户端将把这张证书
钉为信任锚。这里用 `--listen 0.0.0.0` 而非回环：备份容器经 host 网络用主机 IP
访问存储，而该 IP 就在 SAN 里。快速证明它服务的是**你的**证书：

```bash
curl -s --cacert ~/.pgcli-app1/certs/store1.crt \
     https://127.0.0.1:9010/minio/health/live -o /dev/null -w "%{http_code}\n"
# 200 —— 且 curl 校验的链正是我们给的那张证书
```

建仓库桶（pgBackRest 不会代建）：

```bash
pg mc alias set store1 https://127.0.0.1:9010 admin '<root密码>' -- --insecure
pg mc mb store1/pgbackrest
```

## 第 3 步 —— Patroni 集群

```bash
pg ha create app1 --member nodea \
    --etcd-endpoints 127.0.0.1:2379 \
    --host-port 35632 --restapi-port 8028 \
    --advertise-host <主机IP>
```

```
!  No S3 backup repo configured on this host (backup.repo.s3): the member will
   be created WITHOUT WAL archiving ... Configure a repo and run `pg backup
   setup` to wire archiving in (it recreates members as needed).
  [OK] Patroni member nodea started
✓ Patroni member "nodea" installed in scope "app1"
  scope (DCS):  app1-app1        # 命名空间限定："app1-" + scope "app1"
  pg:           <主机IP>:35632
```

那条警告是设计好的操作顺序，不是问题：创建时尚无仓库，成员先不开归档上线；
下一步的 `backup setup` 配好仓库后会**重建成员**并带上归档。注意 DCS 里的
scope 是 `app1-app1`（命名空间前缀 + scope）——给 `pg ha` 命令传的都是*裸*
scope（`app1`），pgcli 自己换算限定形式；限定名也让 stanza 与同一 bucket 上
别的命名空间可能跑的 `app1` 互不冲突。

塞点值得备份的数据（顺带验证客户端 scram 认证链路）：

```bash
pg ha exec app1 "CREATE TABLE IF NOT EXISTS byo_probe(id serial primary key,
    note text, ts timestamptz default now());
    INSERT INTO byo_probe(note) SELECT 'row-'||g FROM generate_series(1,500) g;"
pg ha exec app1 "SELECT count(*) FROM byo_probe"   # 500
```

## 第 4 步 —— 把备份栈指向自带证书的 store

```bash
pg backup setup \
    --s3-endpoint <主机IP>:9010 --s3-bucket pgbackrest \
    --s3-access-key admin --s3-secret-key '<root密码>' \
    --s3-ca-file ~/.pgcli-app1/certs/store1.crt
```

`--s3-ca-file` 直接把**我们自己的证书**当信任锚——自签证书的叶里就含其根，
所以传给 `--tls-cert` 的那个文件*就是* CA 文件。pgBackRest 把 PEM bundle 原
样交给校验器、不检查内容，因此公共 CA 证书可以完全不传这个 flag，私有 CA 把
链传在这里即可。随后这次运行会验穿整条链路并重接成员：

```
  [OK] <主机IP>:9010 reachable
  [OK] repo CA published to 1 cluster registry/registries (from …/store1.crt)
  [OK] repository reachable                      # 一次经 TLS 的 repo-ls：凭据 + CA + 桶全通
  [stale] app1/nodea: mounts the backup-side pgbackrest.conf (pg1-host breaks archive-push)
-> Pausing cluster app1 (no auto-failover during recreate)
  -> Recreating member nodea...                  # 此次带上了 archive-push
  [OK] app1: archive-ready after recreating 1 member(s)
  [OK] stanza pgcli_app1-app1 created
  [OK] check pgcli_app1-app1
```

"repo CA published to the cluster registry" 这行是 etcd 分发通道：本 scope 的
*其他*每个成员（或未来新加的，比如第二台主机）都从 DCS 自动取到 CA，无需手工
scp、无需额外 flag。

## 第 5 步 —— 备份，并证明数据真的到了

```bash
pg ha snapshot create app1 --type full
#   Name:    20260918-091519F
#   Type:    full
pg ha snapshot list app1                          # pgBackRest 在仓库里能看到这份备份
```

独立证据是对象确实躺在桶里——而写入它们的是由**我们的**证书终结的 TLS 会话：

```bash
pg mc ls store1/pgbackrest -- --recursive
# …/backup/pgcli_app1-app1/20260918-091519F/backup.manifest
# …/backup/pgcli_app1-app1/20260918-091519F/pg_data/base/…  (.zst 文件)
# …/archive/pgcli_app1-app1/18-1/0000000200000000/000000020000000000000005-….zst
# …/archive/pgcli_app1-app1/18-1/0000000200000000/000000020000000000000005.00000028.backup
# …/archive/pgcli_app1-app1/archive.info
```

`archive/` 是持续的 **WAL 归档**（重建后的成员跑的 `archive-push`），`backup/`
是全量快照。确认归档器健康：

```bash
pg ha exec app1 "SELECT archived_count, failed_count, last_archived_wal
                 FROM pg_stat_archiver"
```

一个来自实跑的细枝末节，知道可免追鬼：`failed_count` 是 `1`，失败对象为
`00000002.history`——时间线历史文件，**一秒后**归档成功。它撞上了
`backup setup` 暂停并重建成员的那一瞬窗口。setup/重建期间出现一个孤立的近期
失败会自愈；同一个 WAL 名字让计数*持续增长*才是真正的仓库故障。

## 这个示例证明了什么

- 服务**自带**证书的 MinIO 插件可以作为 pgBackRest 仓库端到端工作：
  stanza-create、`check`、全量备份与持续 WAL 归档，全部走以所给证书校验的
  TLS。
- 自签不是次等路径：同一个 `--s3-ca-file` 槽位装得下它（叶即根）、公共 CA 的
  链、内部 CA 的 bundle——传输与信任管道完全相同。
- **`backup.repo.s3` 每份配置文件只有一个全局仓库。** 在生产环境旁边做实验，
  不要去改指它——init 第二份配置（`pg config init --namespace … --base-dir
  …`），把整个实验关在里面。命名空间限定的容器名、DCS scope 与 stanza 让两套
  环境彼此不可见。

## 拆除

示例创建的一切都在 app1 配置名下，拆除也按该配置逆序进行（数据目录显式删
除）：

```bash
pg -c ~/.pgcli-app1/pg.yaml ha remove app1 --scope-all --clean-data
pg -c ~/.pgcli-app1/pg.yaml backup remove --clean-data
pg -c ~/.pgcli-app1/pg.yaml addon remove minio --name store1 --clean-data
rm -rf ~/.pgcli-app1 /home/fish/bucket/pgcli-data-app1
```

要用 `--scope-all`（而不是 `--member`）：它会 `patronictl remove` 整个
scope 的 DCS 键并清空 pgcli 成员注册表；逐个 member 删除会把这些残留留在
共享 etcd 里。

etcd `m1` 属于生产环境——上面所有操作都碰不到它，这正是隔离的用意。

## 相关

- [Patroni 集群备份](../ha-backup/) —— 备份机制的深入讲解
- [MinIO](../addon/minio/) —— 插件本体及其
  [自带证书](../addon/minio/#使用自带证书--tls-cert----tls-key)一节
- [命名空间隔离](../../namespace/) —— `--namespace` 如何限定名称
- [Patroni HA](../ha/) —— `pg ha` 命令集
