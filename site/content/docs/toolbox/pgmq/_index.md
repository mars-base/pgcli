---
title: "PGMQ Queues"
description: "Use the pgmq extension to build durable message queues inside PostgreSQL"
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

`pgmq` turns PostgreSQL into a durable message queue: no Redis, RabbitMQ or
background worker, just tables and SQL functions in the `pgmq` schema.
Producers send JSON messages, consumers read them with a visibility timeout,
and messages stay in the queue until they are explicitly deleted or archived.

## Install

pgcli installs the extension package from the Pigsty repository and runs
`CREATE EXTENSION IF NOT EXISTS pgmq` in the instance's default database:

```bash
pg extension install pgmq -i pg01
```

To use it from another database in the same instance:

```bash
pg psql pg01 -- -d appdb
CREATE EXTENSION IF NOT EXISTS pgmq;
```

Check the installed version:

```bash
pg exec pg01 -c "SELECT extversion FROM pg_extension WHERE extname = 'pgmq'"
```

## Quick start

```sql
-- Create a queue; running it again on an existing queue is a no-op
SELECT pgmq.create('jobs');

-- Send one message; returns its message id
SELECT pgmq.send('jobs', '{"task": "resize", "image_id": 42}');

-- Read up to 10 messages, invisible to other consumers for 60 seconds
SELECT msg_id, read_ct, message
FROM pgmq.read('jobs', 60, 10);

-- Acknowledge one message
SELECT pgmq.delete('jobs', 1);
```

Run the same statements through pgcli:

```bash
pg exec pg01 -c "SELECT pgmq.send('jobs', '{\"task\": \"resize\"}')"
pg psql pg01
```

## Queues on disk

Each queue is a table in the `pgmq` schema:

| Object | Purpose |
|---|---|
| `pgmq.q_<name>` | live messages |
| `pgmq.a_<name>` | archive populated by `pgmq.archive` |
| `pgmq.meta` | queue registry used by `list_queues` and `metrics` |

A live message row contains `msg_id`, `read_ct` (number of deliveries),
`enqueued_at`, `vt` (visible again at) and `message jsonb`. Recent versions add
`last_read_at` and `headers`.

Because messages are rows, `pgmq.send` participates in transactions: it commits
or rolls back with the surrounding statement. Queue traffic is also ordinary
table traffic — high send/delete rates bloat the table like any other.

## Sending messages

```sql
SELECT pgmq.send('jobs', '{"task": "resize"}');

-- Visible only after 30 seconds
SELECT pgmq.send('jobs', '{"task": "cleanup"}', 30);

-- Batch send: one statement, one atomic insert
SELECT pgmq.send_batch('jobs', ARRAY[
  '{"task": "a"}',
  '{"task": "b"}',
  '{"task": "c"}'
]::jsonb[]);
```

`send` returns the new `msg_id`; `send_batch` returns one id per message. Use
`send_batch` instead of looping over `send` — it is one round trip and the whole
batch is atomic.

Payloads must be valid JSON and are stored as `jsonb`: keys are reordered and
duplicates removed. Keep large blobs out of the queue.

Newer versions accept a `headers jsonb` argument for metadata such as a trace id
or a FIFO group key.

## Reading and acknowledging

```sql
SELECT msg_id, read_ct, enqueued_at, vt, message
FROM pgmq.read('jobs', 60, 10);
```

`read(queue, vt, qty)` returns up to `qty` visible messages and makes them
invisible for `vt` seconds. Reading does not remove anything, so each message
must be acknowledged:

```sql
SELECT pgmq.delete('jobs', 42);                -- one message
SELECT pgmq.delete('jobs', ARRAY[42, 43, 44]); -- batch
```

`pgmq.pop('jobs')` reads and deletes in one call. Use it only when losing an
in-flight message on a consumer crash is acceptable.

Avoid a tight polling loop in long-running consumers:

```sql
SELECT msg_id, message
FROM pgmq.read_with_poll('jobs', 60, 10, 5, 100);
```

This waits up to `max_poll_seconds` (5) for messages, checking every
`poll_interval_ms` (100) milliseconds, and returns as soon as a message arrives.

## Visibility timeout and retries

The visibility timeout is PGMQ's delivery model:

1. `read` sets `vt = now() + timeout` and increments `read_ct`.
2. The message stays invisible to other consumers until `vt`.
3. `delete` or `archive` removes it. If the consumer crashes or does not
   acknowledge in time, the message becomes visible again after `vt` and is
   delivered to another consumer.

Delivery is therefore **at-least-once** across failures. Choose `vt` longer than
the worst-case processing time, and make consumers idempotent. For long jobs,
extend the lease without re-reading:

```sql
SELECT pgmq.set_vt('jobs', 42, 300);            -- one message
SELECT pgmq.set_vt('jobs', ARRAY[42, 43], 300); -- batch
```

`read_ct` is a ready-made retry counter.

## Archive and dead letters

`delete` is permanent. `archive` moves a message to `pgmq.a_<queue>` for
retention, audit or replay:

```sql
SELECT pgmq.archive('jobs', 42);
SELECT pgmq.archive('jobs', ARRAY[42, 43]);
SELECT * FROM pgmq.a_jobs;
```

A simple dead-letter pattern is to archive messages that failed too many times:

```sql
-- Move messages that have been delivered 5 or more times to the archive
SELECT pgmq.archive('jobs', ARRAY(
  SELECT msg_id FROM pgmq.q_jobs WHERE read_ct >= 5
));
```

Without a rule like this, a poison message is redelivered forever.

## Monitoring

```sql
SELECT * FROM pgmq.list_queues();
SELECT * FROM pgmq.metrics('jobs');
SELECT * FROM pgmq.metrics_all();
```

`metrics` reports `queue_length`, `queue_visible_length`, the age of the newest
and oldest messages, and `total_messages`. Older extension versions return a
smaller set of columns.

The queue table can be queried directly:

```sql
SELECT count(*) FROM pgmq.q_jobs;

SELECT msg_id, read_ct, enqueued_at, vt, message
FROM pgmq.q_jobs
ORDER BY msg_id DESC
LIMIT 5;
```

Alert when `queue_length` keeps growing or `oldest_msg_age_sec` climbs: either
consumers are down or they cannot keep up.

## Maintenance

```sql
SELECT * FROM pgmq.list_queues();

SELECT pgmq.create_unlogged('scratch');                    -- no WAL, lost on crash
SELECT pgmq.create_partitioned('events', '10000', '100000');
SELECT pgmq.purge_queue('jobs');                           -- delete all messages, keep queue
SELECT pgmq.drop_queue('jobs');                            -- queue and archive tables
```

- `create_unlogged` trades crash safety for write speed. Use it only for data
  that can be regenerated.
- `create_partitioned` keeps a very high-throughput queue from bloating a single
  table; old partitions are dropped automatically.
- `purge_queue` empties a queue but keeps it; `drop_queue` removes it entirely.
- Queue names are limited to 47 characters.

With ordinary queues, heavy send/delete traffic produces dead tuples. Make sure
autovacuum keeps up, or archive messages instead of deleting them and consider
partitioned queues at high volume.

## Permissions

PGMQ functions run with the caller's privileges. An application role needs
access to the schema, the functions and the queue tables:

```sql
GRANT USAGE ON SCHEMA pgmq TO app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA pgmq TO app;
GRANT SELECT, INSERT, UPDATE, DELETE ON pgmq.q_jobs TO app;
GRANT SELECT, INSERT ON pgmq.a_jobs TO app;   -- only when archiving
```

Run `pgmq.create` as the queue owner; creating tables requires privileges the
application role usually should not have. New queues need their own grants, or
you can pre-configure default privileges.

## Function reference

| Function | Purpose |
|---|---|
| `pgmq.create(name)` | Create a queue |
| `pgmq.create_unlogged(name)` | Create an unlogged queue |
| `pgmq.create_partitioned(name, ...)` | Create a partitioned queue |
| `pgmq.send(name, msg [, delay])` | Send one message, returns `msg_id` |
| `pgmq.send_batch(name, msgs jsonb[])` | Send a batch atomically |
| `pgmq.read(name, vt, qty)` | Read and hide messages |
| `pgmq.read_with_poll(name, vt, qty, ...)` | Long-poll read |
| `pgmq.pop(name [, qty])` | Read and delete in one call |
| `pgmq.delete(name, id)` / `pgmq.delete(name, ids[])` | Acknowledge and remove |
| `pgmq.archive(name, id)` / `pgmq.archive(name, ids[])` | Move to the archive table |
| `pgmq.set_vt(name, id(s), seconds)` | Extend the visibility timeout |
| `pgmq.list_queues()` / `pgmq.metrics(name)` | Inspect queues |
| `pgmq.purge_queue(name)` | Delete all messages, keep the queue |
| `pgmq.drop_queue(name)` | Remove the queue and its archive |

## Troubleshooting

**`extension "pgmq" is not available`.** The extension package is not in the
instance image, or the extension has no build for this database. Install it with
`pg extension install pgmq -i <instance>` and create it in the target database.

**Messages are redelivered.** Processing took longer than `vt`, or the consumer
exited before `delete`. Increase `vt`, call `set_vt` during long jobs, and make
processing idempotent.

**Queue length never drops.** Consumers are failing or too slow. Inspect
`read_ct`, application logs and `metrics().oldest_msg_age_sec`. PGMQ retries
failed messages forever unless they are archived or deleted.

**Performance degrades over time.** Default queues are ordinary tables. Check
dead tuples and autovacuum, batch sends and reads, and consider
`create_partitioned` for sustained high throughput.

**`permission denied for schema pgmq`.** Grant `USAGE` on the schema and
`EXECUTE` on the functions to the application role, plus table privileges on
each queue it uses.
