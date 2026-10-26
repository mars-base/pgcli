---
title: "PGMQ 消息队列"
description: "用 pgmq 扩展在 PostgreSQL 内部实现持久化消息队列"
weight: 20
icon: fa-solid fa-inbox
menus:
  main:
    identifier: docs-toolbox-pgmq
    parent: docs-toolbox
    weight: 20
    params:
      icon: fa-solid fa-inbox
cascade:
  type: docs
  footer_style: slim
---

`pgmq` 把 PostgreSQL 变成持久化消息队列：不需要 Redis、RabbitMQ 或后台进程，
只有 `pgmq` schema 下的一组表和 SQL 函数。生产者发送 JSON 消息，消费者按可见性
超时读取，消息在被显式删除或归档之前一直保留在队列里。

## 安装

pgcli 从 Pigsty 仓库安装扩展包，并在实例的默认数据库执行
`CREATE EXTENSION IF NOT EXISTS pgmq`：

```bash
pg extension install pgmq -i pg01
```

同一实例的其他数据库需要单独创建扩展：

```bash
pg psql pg01 -- -d appdb
CREATE EXTENSION IF NOT EXISTS pgmq;
```

查看扩展版本：

```bash
pg exec pg01 -c "SELECT extversion FROM pg_extension WHERE extname = 'pgmq'"
```

## 快速开始

```sql
-- 创建队列；对已存在的队列重复执行不会报错
SELECT pgmq.create('jobs');

-- 发送一条消息，返回消息 id
SELECT pgmq.send('jobs', '{"task": "resize", "image_id": 42}');

-- 读取最多 10 条，60 秒内对其他消费者不可见
SELECT msg_id, read_ct, message
FROM pgmq.read('jobs', 60, 10);

-- 确认一条消息
SELECT pgmq.delete('jobs', 1);
```

通过 pgcli 执行同样的语句：

```bash
pg exec pg01 -c "SELECT pgmq.send('jobs', '{\"task\": \"resize\"}')"
pg psql pg01
```

## 队列在磁盘上的形态

每个队列都是 `pgmq` schema 里的一张表：

| 对象 | 用途 |
|---|---|
| `pgmq.q_<name>` | 在队消息 |
| `pgmq.a_<name>` | `pgmq.archive` 写入的归档表 |
| `pgmq.meta` | `list_queues` 和 `metrics` 使用的队列表册 |

一条在队消息包含 `msg_id`、`read_ct`（投递次数）、`enqueued_at`、`vt`
（再次可见时间）和 `message jsonb`。较新版本还增加了 `last_read_at` 和
`headers`。

消息就是表里的行，因此 `pgmq.send` 参与事务：随所在语句一起提交或回滚。
队列流量也是普通表流量——高频 send/delete 会像其他高写入表一样产生膨胀。

## 发送消息

```sql
SELECT pgmq.send('jobs', '{"task": "resize"}');

-- 30 秒后才可被读取
SELECT pgmq.send('jobs', '{"task": "cleanup"}', 30);

-- 批量发送：一条语句、一次原子写入
SELECT pgmq.send_batch('jobs', ARRAY[
  '{"task": "a"}',
  '{"task": "b"}',
  '{"task": "c"}'
]::jsonb[]);
```

`send` 返回新的 `msg_id`，`send_batch` 为每条消息各返回一个 id。批量场景应使用
`send_batch`，而不是循环调用 `send`——它只有一次网络往返，且整批原子写入。

消息必须是合法 JSON，存储类型为 `jsonb`：键会重排、重复键会被去掉。不要把大块
二进制内容放进队列。

较新版本支持 `headers jsonb` 参数，可携带 trace id、FIFO 分组键等元数据。

## 读取与确认

```sql
SELECT msg_id, read_ct, enqueued_at, vt, message
FROM pgmq.read('jobs', 60, 10);
```

`read(queue, vt, qty)` 最多返回 `qty` 条可见消息，并在 `vt` 秒内让它们对其他
消费者不可见。读取不会删除消息，每条都必须显式确认：

```sql
SELECT pgmq.delete('jobs', 42);                -- 单条
SELECT pgmq.delete('jobs', ARRAY[42, 43, 44]); -- 批量
```

`pgmq.pop('jobs')` 读取并删除一步完成；只有在消费者崩溃时丢一条在途消息可以接受时
才使用它。

长期运行的消费者不要用紧凑轮询：

```sql
SELECT msg_id, message
FROM pgmq.read_with_poll('jobs', 60, 10, 5, 100);
```

它最多等待 `max_poll_seconds`（5 秒），每 `poll_interval_ms`（100 毫秒）检查一次，
有消息到达就立即返回。

## 可见性超时与重试

可见性超时是 PGMQ 的投递模型：

1. `read` 把 `vt` 设为 `now() + timeout`，并递增 `read_ct`；
2. 在 `vt` 之前，其他消费者看不到这条消息；
3. `delete` 或 `archive` 将其移除。若消费者崩溃或没有及时确认，消息在 `vt` 到期后
   重新可见，并被投递给另一个消费者。

因此跨故障场景下投递语义是 **at-least-once（至少一次）**：`vt` 要大于最坏处理
耗时，消费者要保证幂等。长任务可以在不重新读取的情况下续期：

```sql
SELECT pgmq.set_vt('jobs', 42, 300);            -- 单条
SELECT pgmq.set_vt('jobs', ARRAY[42, 43], 300); -- 批量
```

`read_ct` 可以直接当重试计数器使用。

## 归档与死信

`delete` 是永久删除。`archive` 把消息移到 `pgmq.a_<queue>`，便于保留、审计或
重放：

```sql
SELECT pgmq.archive('jobs', 42);
SELECT pgmq.archive('jobs', ARRAY[42, 43]);
SELECT * FROM pgmq.a_jobs;
```

一个简单的死信模式是归档重试次数过多的消息：

```sql
-- 把已投递 5 次及以上的消息移入归档表
SELECT pgmq.archive('jobs', ARRAY(
  SELECT msg_id FROM pgmq.q_jobs WHERE read_ct >= 5
));
```

没有这类规则时，毒消息会被无限重投。

## 监控

```sql
SELECT * FROM pgmq.list_queues();
SELECT * FROM pgmq.metrics('jobs');
SELECT * FROM pgmq.metrics_all();
```

`metrics` 返回 `queue_length`、`queue_visible_length`、最新/最老消息的年龄以及
`total_messages`。较旧的扩展版本返回的列更少。

也可以直接查询队列表：

```sql
SELECT count(*) FROM pgmq.q_jobs;

SELECT msg_id, read_ct, enqueued_at, vt, message
FROM pgmq.q_jobs
ORDER BY msg_id DESC
LIMIT 5;
```

`queue_length` 持续增长或 `oldest_msg_age_sec` 不断变大时应当告警：要么消费者
挂了，要么消费速度跟不上。

## 维护

```sql
SELECT * FROM pgmq.list_queues();

SELECT pgmq.create_unlogged('scratch');                    -- 不写 WAL，崩溃后丢失
SELECT pgmq.create_partitioned('events', '10000', '100000');
SELECT pgmq.purge_queue('jobs');                           -- 清空消息，保留队列
SELECT pgmq.drop_queue('jobs');                            -- 删除队列和归档表
```

- `create_unlogged` 用崩溃安全性换写入速度，只用于可重新生成的数据。
- `create_partitioned` 避免超高吞吐队列把单表撑大；旧分区会自动丢弃。
- `purge_queue` 清空但保留队列；`drop_queue` 彻底删除。
- 队列名最长 47 个字符。

普通队列在高频 send/delete 下会产生 dead tuple。确认 autovacuum 能跟上；写入量
很大时优先考虑归档代替删除，或使用分区队列。

## 权限

PGMQ 函数以调用者的权限执行。应用角色需要 schema、函数和队列表的访问权限：

```sql
GRANT USAGE ON SCHEMA pgmq TO app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA pgmq TO app;
GRANT SELECT, INSERT, UPDATE, DELETE ON pgmq.q_jobs TO app;
GRANT SELECT, INSERT ON pgmq.a_jobs TO app;   -- 仅归档时需要
```

`pgmq.create` 应以队列所有者身份执行；建表权限通常不应授予应用角色。新建队列需要
单独授权，或预先配置 default privileges。

## 函数参考

| 函数 | 用途 |
|---|---|
| `pgmq.create(name)` | 创建队列 |
| `pgmq.create_unlogged(name)` | 创建 unlogged 队列 |
| `pgmq.create_partitioned(name, ...)` | 创建分区队列 |
| `pgmq.send(name, msg [, delay])` | 发送单条消息，返回 `msg_id` |
| `pgmq.send_batch(name, msgs jsonb[])` | 原子批量发送 |
| `pgmq.read(name, vt, qty)` | 读取并隐藏消息 |
| `pgmq.read_with_poll(name, vt, qty, ...)` | 长轮询读取 |
| `pgmq.pop(name [, qty])` | 读取并删除 |
| `pgmq.delete(name, id)` / `pgmq.delete(name, ids[])` | 确认并移除消息 |
| `pgmq.archive(name, id)` / `pgmq.archive(name, ids[])` | 移入归档表 |
| `pgmq.set_vt(name, id(s), seconds)` | 延长可见性超时 |
| `pgmq.list_queues()` / `pgmq.metrics(name)` | 查看队列状态 |
| `pgmq.purge_queue(name)` | 删除全部消息，保留队列 |
| `pgmq.drop_queue(name)` | 删除队列及其归档表 |

## 故障排查

**`extension "pgmq" is not available`。** 实例镜像里没有扩展包，或该数据库没有
对应构建。执行 `pg extension install pgmq -i <instance>`，并在目标数据库中创建
扩展。

**消息被重复投递。** 处理时间超过 `vt`，或消费者在 `delete` 前退出。增大 `vt`、
在长任务中调用 `set_vt`，并保证处理逻辑幂等。

**队列长度不下降。** 消费者失败或太慢。检查 `read_ct`、应用日志和
`metrics().oldest_msg_age_sec`。PGMQ 会无限重投失败消息，除非归档或删除它们。

**性能随时间下降。** 普通队列就是普通表。检查 dead tuple 与 autovacuum，批量
发送和读取；持续高吞吐场景考虑 `create_partitioned`。

**`permission denied for schema pgmq`。** 给应用角色授予 schema 的 `USAGE`、
函数的 `EXECUTE`，以及每个队列表的相应表权限。
