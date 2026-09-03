---
title: 副本（只读备用）
description: pgcli 副本（只读备用）指南
weight: 20
icon: fa-solid fa-book
cascade:
  type: docs
  footer_style: slim
---


创建现有实例的只读物理副本。副本通过 PostgreSQL 物理复制从其主实例持续流式传输 WAL，并提供只读查询服务——适用于读写分离、报告或作为热备。

```bash
# 创建默认实例的副本
pg replica create ro1

# 创建特定实例的副本
pg replica create ro1 -i proj01

# 列出副本和复制延迟
pg replica list
```

## 发生了什么

1. **预检** — 主实例必须正在运行（在任何配置写入*之前*验证；停止的主实例会失败但没有副作用）
2. **注册** — 添加新实例条目，使用主实例的数据库名称和密码（参见注释），禁用 PITR，`replica_of` 设置为主实例
3. **复制设置** — 在主实例上：
   - `pg_hba.conf` 为回环地址和 RFC1918 范围添加 `host replication` 条目（幂等）
   - 创建物理复制槽 `pgcli_r_<name>`，保留 WAL 以确保副本不会落后于 WAL 回收
4. **基础备份** — `pg_basebackup -R` 将主实例的数据目录复制到副本的数据目录，写入 `primary_conninfo`（带密码）和 `standby.signal`，使副本以备用模式启动
5. **启动** — 副本容器启动并持续流式传输 WAL

## 验证

```bash
# 只读备用？
pg exec -i ro1 "SELECT pg_is_in_recovery()"      # t

# 写入被拒绝
pg exec -i ro1 "INSERT INTO t VALUES (1)"         # 只读事务错误

# 流式传输活跃
pg exec -i ro1 "SELECT pg_is_in_recovery(), now() - pg_last_xact_replay_timestamp()"

# 主实例上的槽是活跃的
pg exec -i primary "SELECT slot_name, active FROM pg_replication_slots"

# 概览
pg list                              # ROLE/PRIMARY 列
pg replica list                      # NAME/PRIMARY/STATUS/LAG
pg status -i ro1                     # Role: standby (replica of ...)
```

## 销毁

销毁副本是两步操作：

```bash
# 步骤 1：销毁副本实例（移除容器 + 数据 + 配置条目）
pg destroy -i ro1 --clean-data --force

# 步骤 2：在主实例上删除复制槽（如果已不存在则为无操作）
pg replica drop ro1 -i <primary>
```

步骤 1 必须在步骤 2 之前运行：PostgreSQL 拒绝删除仍在流式传输的槽（`replication slot is active`），因此必须先销毁副本以关闭其流式连接。

> **注意：** 如果主实例是本地 pgcli 管理的实例（同一主机），可以跳过步骤 2 — `destroy` 会自动清理槽。当从副本主机无法访问主实例时需要步骤 2。

## 跨网络副本

上述同主机流程假设主实例和副本共享一个服务器（一个 podman 守护进程，一个网络）。对于在*另一台主机*上的副本，pgcli 在每一侧运行一个命令——无需 SSH，唯一的跨机器信息就是你作为参数传递的内容：

```bash
# ---- 在主实例主机上：准备主实例（首先运行）----
pg replica create ro1 -i pg01 --replica-host 10.241.20.100

# ---- 在副本主机上：复制数据并启动副本（其次运行）----
pg replica create ro1 --primary-dsn "postgres://admin:<password>@10.241.20.50:35432/pg01_db" --primary-name pg01
```

### 获取主实例 DSN

如果主实例是 pgcli 管理的实例，使用 `pg status` 获取其连接信息：

```bash
pg status -i pg01
# ...
#   Connection: postgres://admin:fbcQx9uIzvTO6dVJ@127.0.0.1:35432/pg01_db
```

然后将 `127.0.0.1` 替换为从副本主机看到的主实例主机 IP（例如 `10.241.20.50`）——用户、密码和数据库按原样使用。注意主实例主机必须接受从副本主机到该端口的 TCP 连接（防火墙/安全组）。

每一侧的作用：

- **主实例侧**（`--replica-host <ip|hostname>`）：仅*准备*主实例——本地不创建任何内容。它将 `host replication all <addr>` 条目追加到 `pg_hba.conf` 并创建物理槽 `pgcli_r_<name>`，然后打印要在副本主机上运行的确切 `--primary-dsn` 命令。IP 地址获得 `/32`（IPv6 为 `/128`）掩码；主机名按原样写入。已在托管 RFC1918 范围（`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`）内的 IP 会被跳过去重。幂等——重新运行不会添加重复行。
- **副本侧**（`--primary-dsn`）：首先验证主实例上存在槽（验证连接性和顺序——在主实例侧之前运行会失败并带有可操作的错误消息且没有副作用），然后注册实例，通过网络从 DSN 运行 `pg_basebackup`（主机网络），并启动备用实例。

副本侧仅在副本主机上运行：副本实例的用户、数据库和密码来自 DSN（物理复制复制 `pg_authid`，因此本地密码必须等于主实例的密码）。主实例名称使用 `--primary-name` 给出并记录为 `replica_of`；它不必存在于副本主机的配置中，`-i` 不用于远程主实例——如果给出，它保持其严格含义并且必须引用真实的本地实例。

销毁是对称的，每个主机一个命令——**按此顺序**：

```bash
# 1. 在副本主机上：移除容器 + 配置，还通过 DSN 删除远程主实例上的槽
#    （如果主实例可达则自动）
pg destroy -i ro1

# 2. 在主实例主机上（仅当步骤 1 的 DSN 连接失败时）：
#    手动删除槽
pg replica drop ro1 -i pg01
```

步骤 1 必须在步骤 2 之前运行：PostgreSQL 拒绝删除仍在流式传输的槽（`replication slot is active`），因此必须先销毁副本以关闭其流式连接。

**自动槽清理：** 当跨主机副本设置了 `PrimaryDSN` 时，`destroy` 会自动尝试通过 DSN 删除远程主实例上的复制槽。如果主实例可达，则无需手动步骤 2。`replica drop` 是幂等的——当槽已不存在时重新运行会作为无操作成功。

### 非 pgcli 主实例

主实例不必由 pgcli 管理——只要主实例侧已手动准备（槽检查仅验证槽是否存在，不验证谁创建了它），副本侧可以对任何 PostgreSQL 服务器工作：

1. 在 `pg_hba.conf` 中允许从副本主机复制，然后重新加载（`SELECT pg_reload_conf()`）：
   ```
   host replication <replica user> <replica ip>/32 scram-sha-256
   ```
2. 使用确切名称 `pgcli_r_<replica-name>` 创建物理槽（副本侧检查此名称）：
   ```sql
   SELECT pg_create_physical_replication_slot('pgcli_r_ro1');
   ```
   需要 `wal_level = replica`（或 `logical`）和具有 `REPLICATION` 权限的用户——DSN 用户。

然后副本侧命令不变：

```bash
pg replica create ro1 --primary-dsn "postgres://<user>:<pass>@<primary ip>:5432/<db>" --primary-name pg01
```

销毁时，主实例侧没有 pgcli——在 `pg destroy -i ro1` 之后，手动删除槽：

```sql
SELECT pg_drop_replication_slot('pgcli_r_ro1');
```

如果基础备份失败（例如网络中断），销毁副本并重新运行副本侧命令——主实例侧的槽和 hba 条目保持有效。

## 注释

- **只读** — 副本拒绝所有写入（`cannot execute INSERT in a read-only transaction`）。要使其可写你需要提升它（`pg_ctl promote`），这尚未作为 pgcli 命令暴露
- **相同数据，相同密码** — 物理复制是主实例的逐字节复制，包括 `pg_authid`。因此副本的管理员密码和数据库名称与主实例相同；只有容器名称、端口和数据目录不同。使用 `--dsn` 风格的连接时使用副本的端口
- **副本上禁用 PITR** — 备用实例不归档任何内容，也不在 pgBackRest 备份容器中注册；备份在主实例上运行
- **主实例必须正在运行** — 初始创建（`pg_basebackup`）和持续流式传输都需要；如果主实例重启，副本会自动重新连接（槽显示为 `active`）
- **延迟显示** — `replica list` 延迟（`now() - pg_last_xact_replay_timestamp()`）在主实例空闲时会增长；它会在下一个复制的事务时降回零。这是预期的空闲行为，不是漂移
- **幂等启动** — 重复的 `pg start -i ro1` 在数据目录已初始化时跳过基础备份
