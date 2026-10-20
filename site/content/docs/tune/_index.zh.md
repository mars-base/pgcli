---
title: 配置调优
description: pgcli 实例与 Patroni 集群的 pgtune 风格 postgresql.conf 调优
weight: 86
icon: fa-solid fa-sliders
cascade:
  type: docs
  footer_style: slim
---

`pg tune` 把一台机器的硬件规格和一类工作负载，换算成一套自洽的 `postgresql.conf`
参数——算法就是 [le0pard/pgtune](https://pgtune.leopard.in.ua/)（MIT）那一套，移植进
pgcli 并做成目标感知。它同时服务 pgcli 纳管的两种部署：普通实例（写入
`postgresql.conf` 的托管调优块）和 Patroni 高可用集群（通过 `edit-config` 写入 DCS，
由 Patroni 滚动下发到每个成员）。

默认只**打印建议**，什么都不改。`--apply` 才落盘，`--revert` 移除已应用的调优。

## 适用场景

- **新实例装完之后** —— 容器默认值是"安全"而非"快"。一条 `pg tune -i <name> --apply`
  就能把内存划分、WAL 尺寸、planner 代价常量调到契合这台硬件。
- **升 / 降配之后** —— RAM 和 CPU 变了，重跑同一条命令，派生值随之更新。
- **`pg inspect` 报参数可疑时** —— 巡检报告会指出 `shared_buffers` 偏小 / 缓存命中率
  偏低，`pg tune` 就是那条修复路径。

## 输入

硬件规格默认探测命令所在的宿主机（Linux 从 `/proc/meminfo` 取总内存，CPU 数取自
运行时）。覆盖项：

| 参数 | 含义 |
|------|------|
| `--type` | 工作负载：`web`、`oltp`、`dw`、`desktop`、`mixed`（默认） |
| `--hd` | 存储类型：`ssd`（默认）、`hdd`、`san`、`nvme` |
| `--ram` | 总内存，如 `16G`——共享主机探测不准时用它覆盖 |
| `--cpus` | CPU 数——跨主机 Patroni 集群必须显式传，按最小成员规格算 |
| `--conns` | 覆盖 max_connections（默认按负载预设 200/300/40/20/100） |
| `--db-size` | 库大小 vs RAM，如 `200G`（`<RAM`→装得下，`≤4×RAM`→中等，否则→远大于） |

工作负载一句话选型：应用侧多为短查询 → `web`/`oltp`；分析型、大扫描 → `dw`；与库同机
的单用户应用 → `desktop`；拿不准 → `mixed`。

目标服务器本身会被探测 `server_version_num` 和每个参数的当前值，所以表格是 CURRENT 与
RECOMMENDED 并排；版本门控参数（`jit`、`io_method` 等）只在目标版本存在时才出现。

## 应用

### 普通实例（`-i <name>`）

```bash
pg tune -i myinst                       # 只出建议表
pg tune -i myinst --apply               # 写入 pgcli 托管块
pg tune -i myinst --revert              # 再次移除已应用的调优
```

`--apply` 把参数记录进实例配置，并写入 `postgresql.conf` 的托管调优块。该块在每次
`pg start` 都会重新施加，因此调优**能挺过重启和容器重建**。凡是用 `ALTER SYSTEM` 设过
的（即存在于 `postgresql.auto.conf`）会被跳过并计数——auto.conf 永远优先。

postmaster 级参数（`shared_buffers`、`wal_buffers`、`max_connections` 等）需要重启：
交互式下会 y/N 询问；非交互式下 pgcli 只打印 `pg stop && pg start` 提示，绝不擅自重启
数据库。其余改动直接 config reload 立即生效。

### Patroni 集群（`--ha <scope>`）

```bash
pg tune --ha app                        # 从 leader 读规格，出建议表
pg tune --ha app --apply --auto-restart # 写入 DCS，Patroni 滚动下发
pg tune --ha app --revert --auto-restart
```

这里参数经 `patronictl edit-config` 进入集群 DCS 配置——在 Patroni 的世界里这是改
postgresql.conf 值的唯一正当途径。随后 Patroni 在每个成员上施加，遇 postmaster 级改动
**滚动重启**。因为这有破坏性，`--apply` 和 `--revert` 都会先确认；`--auto-restart`
跳过确认（脚本场景）。你拒绝确认时，DCS 里什么都不会写。

建议表按 pgcli 运行所在主机的规格计算——跨主机集群成员规格可能不同，所以要传
`--ram/--cpus` 对齐最小成员。

## 校验与 `--apply-conf`

```bash
pg tune -i myinst --apply-conf /tmp/tune.conf              # 把建议存成文件…
$EDITOR /tmp/tune.conf                                     # …手工编辑…
pg tune -i myinst --apply --apply-conf /tmp/tune.conf      # …再以该文件为源应用
```

`--apply-conf` 指定一个 `key = value` 文件（postgresql.conf 风格：`#` 注释、值可带引号和
单位）。不加 `--apply` 时**写出**该文件（首行头注释记录输入，便于复现）；加 `--apply`
时**读取**该文件，以它为参数来源。

**每条 apply 路径都先校验。** 每个参数都对照目标的 `pg_settings` 检查：未知参数名
（如 PG15 上的 `io_method`）、非法 bool、超出枚举集合、整数/浮点越出 `[min_val, max_val]`
——逐条列出后整体拒绝。一批调优里只写进去一半是最坏结果。

## `--dsn`：只出建议

```bash
pg tune --dsn postgres://user:pass@host:5432/db
```

远程目标只探测、只建议；`--apply/--revert` 会被拒绝——拿一套启发式去改别人的库，这个
决定 pgcli 不替你做。

## `--web-tune`：交互式站点

```bash
pg tune --web-tune tune.html --open
```

写出单个自包含 HTML——完整的计算器，每个输入都有表单（版本、OS、负载、RAM+单位、
CPU、连接数、存储、库大小），实时重算，postgresql.conf / ALTER SYSTEM 输出带一键复制。
无网络、无依赖；宿主机 RAM/CPU/OS 会预填表单。页面内的计算器是 Go 算法的 JS 移植；
页面自带 Go 现算的 golden 样例，加载时自检，两套实现一旦对不上就显示红条警示。

## 数值怎么来的

公式严格照 le0pard/pgtune 复刻（内部以 kB 计算，上游取整处也取整）：

- `shared_buffers` = RAM/4（desktop /16）；`effective_cache_size` = 3/4 RAM。
- `maintenance_work_mem` = RAM/16（dw /8），封顶 8 GB（Windows ≤PG17 封顶 2047 MB）。
- `work_mem` = (RAM − shared_buffers) / ((max_connections + max_worker_processes) × 3)，
  再按负载缩放（dw/mixed ½、desktop ⅙）与库大小缩放（装得下 ×1.3、远大于 ×0.9），下限 4 MB。
- `wal_buffers` = shared_buffers 的 3%，夹在 [32 kB, 16 MB]。
- `random_page_cost`：hdd 及 dw-on-SSD 取 4（并带上上游那条"会选到灾难性索引扫描"警告），
  其余 1.1；`effective_io_concurrency` 按存储类型（仅 Linux）。
- 并行度按 CPU 数（<4 核不出并行块）；`autovacuum_max_workers` 在 ≥16/≥32 核才加；
  版本门控：`jit=off` ≥12、`wal_compression` on/lz4 ≥10/≥15、`io_method`/`io_workers` ≥18、
  desktop 专属 `wal_level=minimal` + `max_wal_senders=0`。

逐参数注意事项（lz4/liburing 编译开关、DW 代价警告）在表内就近打印；离谱的机器规格
（<256 MB、>100 GB RAM）给全局警告。致谢：算法来自
[le0pard/pgtune](https://github.com/le0pard/pgtune)，"先建议后应用"的交互思路来自
timescaledb-tune。
