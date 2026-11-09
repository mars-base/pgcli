---
title: "插件集群"
description: "用 addon_cluster 分组部署跨主机插件集群——按类型的配置段落、共享密钥与 TLS 材料、法定人数布局与生命周期"
weight: 45
---

`addon_cluster` 把一种支持集群的插件部署到多台主机上。它是
[插件](./addons/) 页的多主机版本：条目字段（`type`、`name`、`state`、
`params`、`args`、`clean_data`、`force`、`autostart`）完全相同，但主机列表、
peer 参数和共享密钥由 inventory 推导，不需要逐台手写。

这个分组刻意不是“一次部署全部”的分组。参考环境的 VM 资源有限，因此每次只收敛
一种插件类型，所有命令都用 `-e addon_type=<type>` 选择。

## Inventory 与变量

`hosts.ini` 声明四台主机。MinIO/silo 的 MNSD 至少需要四个 endpoint；
etcd、Kafka、Keeper、RabbitMQ、RocketMQ 用前三台组成法定人数：

```ini
[addon_cluster]
node1 ansible_host=192.0.2.10 private_ip=192.0.2.10 ansible_user=root ansible_port=22
node2 ansible_host=192.0.2.11 private_ip=192.0.2.11 ansible_user=root ansible_port=22
node3 ansible_host=192.0.2.12 private_ip=192.0.2.12 ansible_user=root ansible_port=22
node4 ansible_host=192.0.2.13 private_ip=192.0.2.13 ansible_user=root ansible_port=22
```

`private_ip` 是必需的：peer 参数、宣告地址和共享证书的 SAN 都用它。主机顺序很
重要——插件 play 以 `serial: 1` 按 inventory 顺序执行，首台主机先完成 bootstrap，
其余成员再加入。

复制模板并修改：

```bash
cd ansible
cp group_vars/addon_cluster/main.yml.example group_vars/addon_cluster/main.yml
```

`main.yml` 已被 gitignore；环境相关的值（SeaweedFS 元数据 DSN、显式密码等）都放在
这里。文件按插件类型分成独立的 `===== <type> =====` 段落：打开某个段落就能看到该
插件需要的全部配置。

每个段落以它指向的 inventory 分组开头：

```yaml
_addon_cluster_etcd_group: addon_cluster
_addon_cluster_etcd_hosts: "{{ groups[_addon_cluster_etcd_group] }}"
```

分组是按插件独立指定的：把 `_addon_cluster_<type>_group` 指向另一个 inventory
分组，就能让该类型跑在另一组主机上（更大的 etcd 法定人数、专用的 Kafka 分组等）。
共享材料的 pre-task 和证书 SAN 列表解析同一个变量；没有该段落的类型回退到
`addon_cluster`。变量名中的类型把 `-` 换成 `_`：`clickhouse-keeper` 对应
`_addon_cluster_clickhouse_keeper_group` 和
`addon_clickhouse_keeper_addons`。

每个段落以 `addon_<type>_addons` 列表结尾。条目字段见[插件](./addons/)页；
`state` 直接写在条目上：

- `present`：安装成员，已存在则保持。
- `absent`：移除成员。除非条目设置 `clean_data: false`，否则一并删除数据。

只用前三台主机的拓扑用表达式代替字面值：

```yaml
state: "{{ 'present' if inventory_hostname in _addon_cluster_etcd_hosts[:3] else 'absent' }}"
```

这样第四台无需修改配置就会收敛为 `absent`。

运行时选择器属于 playbook 管道，不是插件配置：

```yaml
_addon_kind: "{{ addon_type | default('') | replace('-', '_') }}"
pgcli_addons: "{{ lookup('vars', 'addon_' ~ _addon_kind ~ '_addons', default=[]) }}"
```

`-e addon_type=etcd` 会把 `pgcli_addons` 展开为 `addon_etcd_addons`；不传
`addon_type` 时得到空列表，play 什么也不做。

## 共享材料

多成员插件需要统一的口令，按需还需要统一的 TLS 证书。
`roles/addon_cluster_shared` 在控制机（运行 `ansible-playbook` 的机器）上生成一次，
再随每个成员运行 `pg_addon` 之前下发：

| 材料 | 位置 |
|------|------|
| 随机口令（`minio_root_password`、`silo_root_password`、`rustfs_root_password`、`seaweedfs_root_password`、`redis_password`、`clickhouse_admin_password`） | 控制机 `var/addon_cluster/shared/` |
| 共享 CA（`ca.crt`、`ca.key`）、服务器密钥/CSR/证书、链证书与合并 PEM | 同一目录；CA 私钥不会离开控制机 |
| RabbitMQ Erlang cookie 与管理密码 | 由 `pg addon rabbitmq secret generate` 生成并写入 etcd |

成员上的文件位于 `/data/pg/addon/shared/`（`ca.crt`、`server-chain.crt`、
`server.key`、`server.pem`；私钥类文件权限 `0600`）。服务器证书覆盖该插件
inventory 分组内所有主机的 `private_ip`。

运行期间，口令会通过 `set_fact` 注入对应的 `_addon_cluster_<type>_*` 变量，因此
配置模板里保持为空即可：

```yaml
_addon_cluster_minio_root_user: admin
_addon_cluster_minio_root_password: ""
```

类型支持 TLS 时（minio、silo、rustfs、seaweedfs、kafka、rabbitmq），把该插件的
`_tls` 开关设为 `true` 即可使用共享证书。安装摘要会打印 `https://` endpoint；
对自签名 CA，`pg mc` 会自动加 `--insecure`。

RabbitMQ 是例外：cookie 和管理密码在每个成员上必须一致，因此角色把它们发布到
etcd 的 `/pgcli/rabbitmq/clusters/<name>/secrets/`，每个成员用
`--secrets-from-etcd` 读取。这要求 etcd 集群已运行，请先部署 etcd 段落。生成命令
在控制机上执行，所以控制机需要有 `pgcli_install_path` 指向的 `pg` 二进制，并且能
访问 `_addon_cluster_rabbitmq_etcd_endpoints` 中的第一个 endpoint。

## 内置布局

| 类型 | 成员 | 端口 | 说明 |
|------|------|------|------|
| `etcd` | 前三台 | 2379 client，2380 peer | 首台 bootstrap，其余 `--join` |
| `minio` | 4 | 9100 API，9101 console | MNSD；每台贡献自己的 `/data` |
| `silo` | 4 | 9200 API，9201 console | MNSD |
| `rustfs` | 4 | 9300 API，9301 console | MNMD；每台一块盘。裸 SigV4 请求必须带 `x-amz-content-sha256` 头 |
| `seaweedfs` | 前三台 | 8400 master，8401 volume，8402 filer，8403 S3 | master 必须为奇数；filer 元数据库需要先在 PostgreSQL 中创建 |
| `redis` | 4 | 6379（集群总线 16379） | 只安装四个 cluster-enabled 成员；之后一次性组建 slot |
| `clickhouse-keeper` | 前三台 | 9181 client，9234 Raft | 3 成员 Raft 法定人数；`--server-id` = 主机下标 + 1 |
| `clickhouse` | 4 | 8123 HTTP，9000 native，9009 interserver | 基于上面的 Keeper 组成 2 shard × 2 replica；先部署 Keeper |
| `kafka` | 前三台 | 9092 broker，19093 controller | KRaft 法定人数；512 MiB 堆适配参考 VM |
| `rabbitmq` | 前三台 | 5672 AMQP，15672 management | 依赖 etcd；共享 cookie/密码来自 etcd |
| `rocketmq` | 前三台 | 9876 namesrv，10911 broker，8080 proxy，8081 gRPC | 小主机使用缩减后的堆 |
| `vmcluster` | 4 | 8480 insert，8481 select，8482 storage，8483/8484 internal | 首台承担 storage+insert+select，其余仅 storage |
| `vlcluster` | 4 | 9430 insert，9431 select，9432 storage | 同样的访问/存储拆分；存储分片但不复制 |

SeaweedFS 段落只把前三台放进 `_addon_cluster_seaweedfs_members`，第四台收敛为
`state: absent`。ClickHouse Keeper、Kafka、RabbitMQ、RocketMQ 段落也使用同样的
`[:3]` 写法。MinIO、silo、RustFS、Redis、vmcluster、vlcluster 使用全部四台。

## 创建、验证、销毁

一次只跑一种类型：

```bash
cd ansible
uv run ansible-playbook playbooks/apply.yaml --limit addon_cluster \
  --tags addon -e addon_type=etcd
```

play 使用 `serial: 1` 并按 inventory 顺序执行。对于法定人数型插件，首台必须先就绪，
其余成员才能加入，所以不要在两次运行之间调整主机顺序。

### 验证

除特别说明外，命令在某个成员上执行。

| 类型 | 命令 |
|------|------|
| `etcd` | `pg etcdctl endpoint health`、`pg etcdctl member list` |
| `minio` / `silo` / `rustfs` / `seaweedfs` | 用 `pg mc` 访问 API 端口，再从另一成员读回对象（见下） |
| `redis` | 组建 slot 后执行 `pg redis-cli -- --cluster check 192.0.2.10:6379` |
| `clickhouse-keeper` | `pg addon clickhouse-keeper status --name keeper-cluster` |
| `clickhouse` | `pg addon clickhouse cluster status --name clickhouse-cluster` |
| `kafka` | `pg addon kafka cluster status --name kafka-cluster`；`pg addon kafka topic list --name kafka-cluster` |
| `rabbitmq` | `pg addon rabbitmq cluster status --name rabbitmq-cluster` |
| `rocketmq` | `pg addon rocketmq cluster status --name rocketmq-cluster` |
| `vmcluster` | 写入一条样本后通过 vmselect 查询（见下） |
| `vlcluster` | 写入一条样本后通过 vlselect 查询（见下） |

跨节点 S3 读取（每个成员使用同一个口令，口令在控制机生成）：

```bash
PW=$(cat var/addon_cluster/shared/minio_root_password)
pg mc alias set n1 http://192.0.2.10:9100 admin "$PW"
pg mc mb n1/demo
pg mc cp /etc/hostname n1/demo/
pg mc alias set n2 http://192.0.2.11:9100 admin "$PW"
pg mc cat n2/demo/hostname
```

silo 用 9200，rustfs 用 9300，seaweedfs 用 8403，并换成对应的口令文件。Redis 在
四个成员都起来后一次性组建 slot：

```bash
pg redis-cli -- --cluster create \
  192.0.2.10:6379 192.0.2.11:6379 192.0.2.12:6379 192.0.2.13:6379 \
  --cluster-replicas 0
pg redis-cli -- --cluster check 192.0.2.10:6379
```

VictoriaMetrics 集群：

```bash
curl -s -X POST http://192.0.2.10:8480/insert/0/prometheus/api/v1/import/prometheus \
  --data-binary 'demo_metric{host="node1"} 42'
curl -s 'http://192.0.2.10:8481/select/0/prometheus/api/v1/query?query=demo_metric'
```

vminsert 还在逐个连接 vmstorage 节点时，第一次查询可能返回空结果；等几秒重试。

VictoriaLogs 集群：

```bash
curl -s -X POST http://192.0.2.10:9430/insert/jsonline \
  -H 'Content-Type: application/json' \
  -d '{"_msg":"hello","app":"demo"}'
curl -s 'http://192.0.2.10:9431/select/logsql/query?query=app:demo&limit=10'
```

必须带 `Content-Type: application/json`。缺少该头时 endpoint 可能返回 `200`，但
payload 被静默丢弃。

### 销毁

把该段落的 `state` 改成 `absent`，重跑同一条命令。除非条目设置
`clean_data: false`，数据随成员一起删除；对 SeaweedFS 还包括渲染出的
`s3.json` 和 `filer.toml`。

使用 `state` 表达式的段落（etcd、seaweedfs、keeper、kafka、rabbitmq、rocketmq）
把表达式换成字面值 `absent`，或者新增一个变量来选择状态。`serial: 1` 会逐台移除
法定人数成员。

依赖顺序：

- **etcd** 是 RabbitMQ 的协调后端，也是 HA 的 DCS。先把 RabbitMQ（以及任何
  Patroni 集群）移除，再移除 etcd。
- **clickhouse-keeper** 要比 ClickHouse 成员活得久。先移除 ClickHouse，再移除
  Keeper。
- 控制机 `var/addon_cluster/shared/` 里的材料在销毁/重建周期中保留。要轮换口令，
  在所有成员都处于 `absent` 时删除该目录；已存在的成员会继续使用旧口令，必须重建。
  RabbitMQ 的密钥存放在 etcd 中，随 etcd 数据一起消失，下次运行 RabbitMQ 时重新
  生成。

移除插件不会删除成员上的共享 TLS 文件，重建时可以直接复用。
