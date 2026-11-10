---
title: "插件集群"
description: "用按类型拆分的 inventory 分组部署跨主机插件集群——共享密钥与 TLS 材料、法定人数布局与生命周期"
weight: 45
---

`addon_<type>_cluster` 这组 inventory 分组把一种支持集群的插件部署到多台主机上。
它们是[插件](./addons/)页的多主机版本：条目字段（`type`、`name`、`state`、
`params`、`args`、`clean_data`、`force`、`autostart`）完全相同，但主机列表、
peer 参数和共享密钥由 inventory 推导，不需要逐台手写。

这些分组刻意不是“一次部署全部”的分组。参考环境的 VM 资源有限，因此每次只收敛
一种插件类型，所有命令都用 `--limit addon_<type>_cluster` 选择分组。

## Inventory 与变量

`hosts.ini` 在 `[all]` 中只定义一次四台主机，再用裸主机名把它们选进每种插件的
分组。法定人数型插件用三台主机；MinIO/silo MNSD 等四成员布局用全部四台：

```ini
[all]
node1 ansible_host=192.0.2.10 private_ip=192.0.2.10
node2 ansible_host=192.0.2.11 private_ip=192.0.2.11
node3 ansible_host=192.0.2.12 private_ip=192.0.2.12
node4 ansible_host=192.0.2.13 private_ip=192.0.2.13

[all:vars]
ansible_user=root
ansible_port=22

[addon_etcd_cluster]
node1
node2
node3

[addon_minio_cluster]
node1
node2
node3
node4
```

`private_ip` 是必需的：peer 参数、宣告地址和共享证书的 SAN 都用它。主机顺序很
重要——插件 play 以 `serial: 1` 按 inventory 顺序执行，首台主机先完成 bootstrap，
其余成员再加入。

每个分组都有自己独立、自包含的 `group_vars` 目录。按需复制并修改：

```bash
cd ansible
cp group_vars/addon_etcd_cluster/main.yml.example \
   group_vars/addon_etcd_cluster/main.yml
```

`main.yml` 已被 gitignore；环境相关的值（SeaweedFS 元数据 DSN、显式密码等）都放在
这里。每个文件声明自己的拓扑变量和一个 `addon_<group>_addons` 列表，打开一个文件
就能看到该插件需要的全部配置：

```yaml
_addon_cluster_etcd_hosts: "{{ groups['addon_etcd_cluster'] }}"
_addon_cluster_etcd_index: "{{ _addon_cluster_etcd_hosts.index(inventory_hostname) | int }}"

addon_etcd_cluster_addons:
  - type: etcd
    state: present
    name: "etcd-{{ (_addon_cluster_etcd_index | int) + 1 }}"
```

分组名和变量名中的类型把 `-` 换成 `_`：
`addon_clickhouse_keeper_cluster_addons` 对应 clickhouse-keeper。

每个列表条目遵循[插件](./addons/)页记录的字段；`state` 直接写在条目上：

- `present`：安装成员，已存在则保持。
- `absent`：移除成员。除非条目设置 `clean_data: false`，否则一并删除数据。

运行时选择器属于 playbook 管道，不是插件配置
（`group_vars/all/main.yml`）：

```yaml
_addon_limit: "{{ ansible_limit | default('', true) }}"
_addon_group: >-
  {{ _addon_limit
     if (_addon_limit is match('^addon_[A-Za-z0-9_]+$') and _addon_limit in groups)
     else '' }}
_addon_kind: "{{ _addon_group | regex_replace('^addon_', '') }}"
_addon_type: "{{ _addon_kind | regex_replace('_(single|cluster)$', '') }}"
_addon_mode: >-
  {{ 'cluster' if _addon_kind is match('.*_cluster$')
     else ('single' if _addon_kind is match('.*_single$') else '') }}
pgcli_addons: >-
  {{ lookup('vars', 'addon_' ~ _addon_kind ~ '_addons', default=[])
     if _addon_group | length > 0 else [] }}
```

`--limit addon_etcd_cluster` 会在那三台主机上把 `pgcli_addons` 展开为
`addon_etcd_cluster_addons`；不传 add-on limit 时得到空列表，play 什么也不做。
`apply.yaml` 会拒绝通配符、逗号分隔的列表，以及缺少模式后缀的 `addon_<type>`。

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
`server.key`、`server.pem`；私钥类文件权限 `0600`）。服务器证书覆盖所选
`addon_<type>_cluster` 分组内所有主机的 `private_ip`。

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
| `redis` | 4 | 6379（集群总线 16379） | 安装四个 cluster-enabled 成员；随后由 `apply.yaml` 组建 slot 并等待集群健康 |
| `clickhouse-keeper` | 前三台 | 9181 client，9234 Raft | 3 成员 Raft 法定人数；`--server-id` = 主机下标 + 1 |
| `clickhouse` | 4 | 8123 HTTP，9000 native，9009 interserver | 基于上面的 Keeper 组成 2 shard × 2 replica；先部署 Keeper |
| `kafka` | 前三台 | 9092 broker，19093 controller | KRaft 法定人数；512 MiB 堆适配参考 VM |
| `rabbitmq` | 前三台 | 5672 AMQP，15672 management | 依赖 etcd；共享 cookie/密码来自 etcd |
| `rocketmq` | 前三台 | 9876 namesrv，10911 broker，8080 proxy，8081 gRPC | 小主机使用缩减后的堆 |
| `vmcluster` | 4 | 8480 insert，8481 select，8482 storage，8483/8484 internal | 首台承担 storage+insert+select，其余仅 storage |
| `vlcluster` | 4 | 9430 insert，9431 select，9432 storage | 同样的访问/存储拆分；存储分片但不复制 |

三节点布局（`addon_etcd_cluster`、`addon_seaweedfs_cluster`、
`addon_clickhouse_keeper_cluster`、`addon_kafka_cluster`、
`addon_rabbitmq_cluster`、`addon_rocketmq_cluster`）在 `hosts.ini` 中只列前三台
主机，因此组文件里不再需要 `[:3]` 切片或 `state` 表达式。四成员布局列出全部四台。
MinIO/silo MNSD 至少需要四个 endpoint；Redis、vmcluster、vlcluster 也使用全部四台。

## 创建、验证、销毁

一次只跑一种类型：

```bash
cd ansible
uv run ansible-playbook playbooks/apply.yaml --limit addon_etcd_cluster \
  --tags addon
```

play 使用 `serial: 1` 并按 inventory 顺序执行。对于法定人数型插件，首台必须先就绪，
其余成员才能加入，所以不要在两次运行之间调整主机顺序。

### 验证

除特别说明外，命令在某个成员上执行。

| 类型 | 命令 |
|------|------|
| `etcd` | `pg etcdctl endpoint health`、`pg etcdctl member list` |
| `minio` / `silo` / `rustfs` / `seaweedfs` | 用 `pg mc` 访问 API 端口，再从另一成员读回对象（见下） |
| `redis` | `pg redis-cli -- --cluster check 192.0.2.10:6379`（play 在组建后会自动做同等校验） |
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

silo 用 9200，rustfs 用 9300，seaweedfs 用 8403，并换成对应的口令文件。

Redis 不需要手工步骤：`apply.yaml` 会等所有成员应答，若集群尚未组建则执行一次
`--cluster create ... --cluster-yes`（`--cluster-replicas` 取自插件参数，默认
`0`），然后等待 `cluster_state:ok` 且全部 16384 个 slot 覆盖、已知节点数符合预期。
集群健康后重跑会跳过组建步骤。部分成员已加入但 slot 未覆盖的集群不会被自动重置：
play 会停止并提示手工重置成员后重跑。只用 `pg addon` 安装时，等价命令为：

```bash
pg redis-cli -- --cluster create \
  192.0.2.10:6379 192.0.2.11:6379 192.0.2.12:6379 192.0.2.13:6379 \
  --cluster-replicas 0 --cluster-yes
pg redis-cli -- --cluster check 192.0.2.10:6379
```

非交互运行必须带 `--cluster-yes`：否则 `redis-cli` 会等待确认输入，然后不应用
配置直接退出。

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

把组文件里每个条目的 `state` 改成 `absent`，重跑同一条命令。除非条目设置
`clean_data: false`，数据随成员一起删除；对 SeaweedFS 还包括渲染出的
`s3.json` 和 `filer.toml`。`serial: 1` 会逐台移除成员。

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
