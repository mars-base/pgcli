---
title: 健康巡检
description: pgcli PostgreSQL 健康巡检报告指南
weight: 85
icon: fa-solid fa-stethoscope
cascade:
  type: docs
  footer_style: slim
---


`pg inspect` 用一条命令回答一个问题：**这个库健康吗，该先修什么？** 它对 PostgreSQL 标准统计视图做一次单快照采样，产出确定性报告——0-100 的健康分，加上按 CRITICAL / WARNING / NOTE / GOOD 分级的诊断条目，每条带证据、修复建议，以及防止"自信地给出错误建议"的限定说明。

它严格只读：每条语句都包在 `READ ONLY` 事务里执行，绝不写入、除普通读取外不加任何锁，可随时对生产库运行。

## 适用场景

- **每周 / 值班例行检查** —— 对所有实例跑一遍，只看分数不到 100 的；有没有退化一目了然，不用盯仪表盘。
- **"数据库感觉变慢了"** —— 在翻 EXPLAIN 计划之前，先拿到 30 秒全貌：缓存命中率、耗时 Top SQL（需 `pg_stat_statements`）、膨胀、会话堆积。
- **安装 / 升级之后** —— 确认没有危险参数混进配置（比如 `fsync = off`），autovacuum 确实跟得上。
- **容量决策之前** —— 看清哪些索引从未被扫描（删掉能回收多少空间）、连接数是否在逼近 `max_connections`。
- **审计与交接** —— `--html` 生成单个自包含文件，可直接发邮件或归档；`--json` 喂给脚本和监控 agent。

## 能提前发现的问题

| 问题 | 不巡检你会错过什么 |
|------|--------------------|
| 事务 ID 年龄逼近冻结墙 | 一次被迫停机的 vacuum 紧急事故 |
| 连接数逼近 `max_connections` | 高负载时登录失败 |
| 缓存命中率低 | `shared_buffers` / `effective_cache_size` 配小了 |
| 超过 1 MiB 的从未使用索引 | 空间浪费与写放大 |
| 表膨胀、从未 vacuum 或关了 autovacuum 的表 | 扫描越来越慢，vacuum 风暴 |
| 大表重顺序扫描 / 非 HOT 更新 | 缺索引和热点更新模式 |
| 长事务与 idle in transaction 会话 | 清理被阻塞、锁等待 |
| 偏离编译默认值的危险参数 override | 悄无声息的持久性或调优错误 |

各项检查独立降级——没装 `pg_stat_statements` 的库照样出完整报告，只多一条说明该板块为空的 NOTE。

## 用法

```bash
# 本地实例：带健康分的终端摘要
pg inspect -i myinst

# 远程数据库（输出中绝不显示密码）
pg inspect --dsn postgres://user:pass@host:5432/db

# 各板块原始表格：queries、indexes、vacuum/tables、settings、sessions
pg inspect -i myinst --full

# 自包含 HTML 报告，可选用浏览器打开
pg inspect -i myinst --html /tmp/report.html --open
```
