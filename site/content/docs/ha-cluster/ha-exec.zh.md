---
title: "HA Exec / psql"
description: "用 pg ha exec 和 pg ha psql 对 Patroni HA 集群执行 SQL：从 DCS 零配置解析 leader、免手拼 dsn、免进成员容器，并可用 --member 指定只读副本或跨主机节点"
weight: 52
---

`pg ha exec` 和 `pg ha psql` 是对 Patroni 集群直接跑 SQL 的方式。它们是单实例
[`pg exec`](../../exec-psql/) / [`pg psql`](../../exec-psql/) 的集群对应物：你只给一个
scope，工具负责解析目标并连上去。不用手拼 `--dsn`、不用 `podman exec` 钻进成员容器，
而且因为走的是普通 TCP，**另一台主机**上的 leader 或副本和本机成员一样可达。

## 为什么不用 `pg exec --dsn` 或 `podman exec`

在这两条命令之前，想在集群上临时跑 SQL 只有两条别扭的路：

- **`pg exec --dsn postgres://postgres:<pass>@<leader>:<port>/db`** —— 结果是对的，但
  dsn 要人肉拼：从 `pg ha status` 读 leader 的 `connect_address`、从配置里抄超管密码、
  再填端口。而且写死的 dsn 在 leader 一切换就失效了。
- **`podman exec -it <member> psql ...`** —— 只能打到*本机*成员，别的主机上的 leader 根本
  够不着；而且你是钻进了一个本该被工具屏蔽掉的容器。

`pg ha exec`/`psql` 把这两点都抹平了：pgcli 从集群自身状态解析 leader，用存储的密码认证，
在一个一次性容器里跑 psql。你全程看不到它。

## 目标是怎么解析的

**DCS roster**（`patronictl list -f json`）是唯一能看到整个集群全貌的视图。每台主机的
`pg.yaml` 只登记*自己的*成员，所以本机配置连一个跑在别处、名叫什么的节点都看不到——但 DCS
知道每个成员以及它对外宣告的 `connect_address`。两条命令都走它：

- **默认（不带 `--member`）** → 当前 **leader**。即使 leadership 自你上次查看后已经迁移，
  你依然打在对的节点上——不存在过期的 dsn。
- **`--member <名字>`** → 那个指定成员，无论它在哪。指向副本可拿到**只读**视图
  （在 standby 上 `SELECT`，`pg_is_in_recovery()` 返回 `t`），或指向某个具体的远端节点。

认证用集群超管走 scram；`pg_hba`（`host all all all scram-sha-256`）接受任意来源，所以远端
成员就像复制流量本来就能跨主机到达一样，可达。

## 用法

```bash
# 对 leader 跑一次性 SQL
pg ha exec app "SELECT version()"
pg ha exec app "SELECT count(*) FROM pg_stat_activity"

# 对 leader 上的另一个库
pg ha exec app --database mydb "SELECT * FROM t LIMIT 5"

# 只读：打副本而不是 leader
pg ha exec app --member node2 "SELECT pg_is_in_recovery()"

# 对 leader 开交互式 psql
pg ha psql app

# 对指定成员（哪怕跨主机）开交互式 psql
pg ha psql app --member node3

# -- 之后的参数透传给 psql
pg ha psql app -- -c "SELECT 1"
pg ha psql app --database mydb -- -x
```

`pg ha exec` 把 SQL 作为尾随参数接收（所以通常整体引成一个字符串）；`pg ha psql` 开一个
交互式 shell，把 `--` 之后的内容原样转给 psql。`--database` 在两条命令上都选择目标库
（默认 `postgres`）。没有 `--user`——超管是 pgcli 唯一持有凭证的角色，所以连上去的就是它。

### 交互式与脚本化

`pg ha psql` 在你的 stdin 是 tty 时分配一个 TTY；不是 tty 时（管道喂脚本、在 CI 里跑）它会
关掉分页器，让会话跑完就退出而不是卡在 `less` 里。所以下面两种用法都符合预期：

```bash
pg ha psql app < migrations.sql      # 管道喂文件，无分页器，跑完即退
printf '\conninfo\n' | pg ha psql app   # 脚本化单行，输出流回终端
```

## 关于输出

`pg ha exec` 流式输出 psql 自己的格式——列头、对齐、行数、错误都直接打到终端，跟 `psql -c`
打印的一模一样。它不是机器可解析的转储；如果需要那种，自己接管道工具，或用
`pg ha psql app -- -c "..."` 加 psql 的 `--csv` 之类 flag。

## 定位

如果你要一个**独立于 pgcli、能扛故障切换的稳定客户端端点**，在集群前面放
[HAProxy](../addon/haproxy/)，通过它连。`pg ha exec`/`psql` 是给运维和脚本直接驱动集群用的，
不是应用前面连接池的替代品。

`pg ha status` / `pg ha ctl` 仍是查看集群状态、以及触达未被包装的 `patronictl` 命令的方式；
这两条命令专门负责跑 SQL。
