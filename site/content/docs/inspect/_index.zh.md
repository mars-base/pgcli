---
title: 健康巡检
description: pgcli PostgreSQL 健康巡检报告指南
weight: 85
icon: fa-solid fa-stethoscope
cascade:
  type: docs
  footer_style: slim
---


`pg inspect` 对 PostgreSQL 标准统计视图做一次单快照采样，产出一份确定性的健康报告：0-100 的健康分，加上按 CRITICAL / WARNING / NOTE / GOOD 分级的诊断条目，并可输出 queries、indexes、vacuum/tables、settings、sessions 各板块的原始表格。整体设计借鉴自开源项目 [pgbot](https://github.com/pgrundev/pgbot)（Apache-2.0）。

所有操作均为只读——每条语句都包在 `READ ONLY` 事务里执行。使用能读统计视图的监控角色（`pg_monitor`）即可。

## 基本用法

```bash
# 巡检本地实例（摘要 + 健康分）
pg inspect -i myinst

# 也可以用连接串巡检远程数据库
pg inspect --dsn postgres://user:pass@host:5432/db

# 打印各板块原始表格，而不只是摘要
pg inspect -i myinst --full

# 整个报告以 JSON 输出（供脚本 / agent 使用）
pg inspect -i myinst --json | jq '.findings | length'

# 写一份自包含 HTML 报告；只有加 --open 才尝试打开浏览器
pg inspect -i myinst --html /tmp/report.html
pg inspect -i myinst --html /tmp/report.html --open
```

## 参数

| 参数 | 默认 | 说明 |
|------|------|------|
| `--dsn` | — | 巡检远程数据库而不是本地实例 |
| `--full` | 关 | 在终端打印各板块原始表格 |
| `--json` | 关 | 整个报告以 JSON 输出（不再打印终端摘要） |
| `--html <path>` | — | 写一份自包含 HTML 报告（零外部请求） |
| `--open` | 关 | 配合 `--html`，尝试用浏览器打开报告（需要图形环境） |
| `--interval` | `1s` | 计算速率所用的两次计数器采样之间的间隔 |
| `--top` | `20` | `pg_stat_statements` 取多少行 |

`--open` 必须搭配 `--html`；没有报告文件就无从打开。

## 检查哪些内容

- **健康分** —— 满分 100 起扣：CRITICAL 每个扣 10、WARNING 扣 3、NOTE 扣 1。
- **事务 ID 回卷** —— xid / multixid 年龄对比冻结墙。
- **连接数** —— 相对 `max_connections` 的饱和度。
- **缓存命中率** —— 采样窗口内缓冲池命中 vs 物理读。
- **回滚比例与死锁** —— 来自 `pg_stat_database` 的双采样。
- **未使用索引** —— 零扫描、超过 1 MiB，且排除主键、约束支撑、unique、部分、表达式和副本标识索引（带限定说明而非直接建议删除）。
- **膨胀 / 顺序扫描 / 热更新** —— 每表的死元组比例、顺序扫描占比、非 HOT 更新负载。
- **Vacuum 与 analyze 卫生** —— 从未 vacuum/analyze 的表、按表关闭的 autovacuum。
- **会话** —— 长事务、idle in transaction，来自 `pg_stat_activity`。
- **参数设置** —— 危险调优值（如 `fsync = off`）以及偏离编译默认值的 override。

每个板块独立降级：没装 `pg_stat_statements` 的库照样出完整报告，只多一条 NOTE 解释 queries 板块为空。要启用它，请用扩展管理命令正确安装（软件包 + `shared_preload_libraries`，自动重启）：

```bash
pg extension install pg_stat_statements -i myinst
```

## HTML 报告

`--html` 生成的文件完全自包含：内联 CSS、内容全部转义、无 JavaScript、无外部请求——可以放心拷到任何地方离线打开。诊断条目按严重度分组，带证据行、修复建议和限定说明；每个板块渲染成可排序表格。

## JSON 契约

`--json` 以 snake_case 键输出报告：`server`、`version`、`target`、`interval_s`、`health`、`limits`、`rates`、`queries`、`indexes`、`tables`、`settings`、`activity`，以及 `findings`（每条含 `id`、`severity`、`title`、`detail`、`remediation`、`evidence`、`impact_score`、`caveats`）。

## 安全说明

- 所有诊断都跑在 `BEGIN TRANSACTION READ ONLY` 里——写入不可能成功。
- `--dsn` 目标在所有输出中都会脱敏（只显示 host:port/db，绝不显示密码）。
- queries 板块读取 `pg_stat_statements`，它可能暴露该实例上所有库的查询形态——用可信任能看到这些的角色执行 inspect，或者干脆不装该扩展。

## 致谢

collect → findings → report 的三层流水线、规则阈值和报告结构借鉴自 [pgbot](https://github.com/pgrundev/pgbot)（Apache-2.0），在此按其思想重新实现，不依赖其运行时组件。
