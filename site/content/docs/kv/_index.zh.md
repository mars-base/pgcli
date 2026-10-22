---
title: KV 缓存
description: PostgreSQL KV 缓存函数，轻量级 Redis 替代方案
weight: 87
icon: fa-solid fa-database
cascade:
  type: docs
  footer_style: slim
---

`pg kv` 将一组 KV 缓存函数部署到 PostgreSQL 实例，将其转变为轻量级 Redis 替代方案，用于纯键值缓存。它不是完整的 Redis 替代——没有计数器、没有有序集合、没有发布/订阅——只有快速、原子化、基于 TTL 的键值存储，支持自动过期。

提供两个变体：**BYTEA**（二进制值，通用缓存）和 **JSONB**（结构化 JSON，支持字段级更新和包含查询）。两者可以一起或分别部署。

## 适用场景

- **会话存储** —— 存储用户会话、OAuth 令牌或 API 密钥，自动过期。当应用已经连接 PostgreSQL 时，比 Redis sidecar 更简单。
- **配置缓存** —— 缓存不频繁变更的应用配置或功能开关。JSONB 变体支持部分字段更新，无需重写整个对象。
- **限流** —— 按用户/IP 跟踪请求计数或时间戳。UPSERT 原子性防止并发写入时的竞态条件。
- **临时数据** —— 任务队列、锁令牌、一次性代码。UNLOGGED TABLE 意味着无 WAL 开销；对于临时缓存，崩溃丢数据可接受。
- **原型开发** —— 避免为副项目启动 Redis。一条命令（`pg kv -i myinst --database myapp`）即可获得可用缓存。

## 不适用场景

- **高吞吐**（持续 >500 QPS）—— 使用 Redis 或 Memcached。PostgreSQL 未针对每秒数百万个小 KV 操作优化。
- **有序集合、计数器、发布/订阅** —— 这些是 PostgreSQL 不复刻的 Redis 特性。使用 Redis 或应用层逻辑。
- **大值**（每个键 >1 MB）—— BYTEA 存储可行但效率低。使用对象存储（S3/MinIO）或文件系统。
- **需要持久化** —— UNLOGGED TABLE 意味着崩溃或非正常关机时数据丢失。如果缓存丢失不可接受，使用普通表或带 AOF 的 Redis。

## 用法

```bash
# 部署 BYTEA 和 JSONB 两个变体（默认）
pg kv -i myinst --database myapp

# 仅部署 BYTEA（通用二进制值）
pg kv -i myinst --database myapp --type bytea

# 仅部署 JSONB（结构化 JSON，支持字段更新）
pg kv -i myinst --database myapp --type json

# 通过 DSN 目标远程数据库
pg kv --dsn postgres://user:pass@host:5432/otherdb --database myapp

# 预览 SQL 不执行
pg kv -i myinst --database myapp --dry-run
```

`--database` 标志是必填的——pgcli 不会自动创建数据库，目标必须已存在。命令是幂等的：多次运行是安全的（函数使用 `CREATE OR REPLACE`，表使用 `IF NOT EXISTS`）。

## BYTEA 变体

通用二进制键值缓存。值存储为 `BYTEA`（任意二进制数据）。用于字符串、序列化对象或原始字节。

### 表

```sql
CREATE UNLOGGED TABLE kv_cache (
    key TEXT PRIMARY KEY,
    value BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
```

### 函数

| 函数 | 签名 | 返回值 | 描述 |
|------|------|--------|------|
| `kv_set` | `(p_key TEXT, p_value BYTEA, p_ttl_seconds INT DEFAULT 300)` | `VOID` | 以 UPSERT 原子性写入缓存。默认 TTL 300秒。 |
| `kv_set_with_result` | `(p_key TEXT, p_value BYTEA, p_ttl_seconds INT DEFAULT 300)` | `BOOLEAN` | 与 `kv_set` 相同，成功返回 `TRUE`，错误返回 `FALSE`。 |
| `kv_get` | `(p_key TEXT)` | `BYTEA` | 读取缓存（自动过滤过期）。未找到或已过期返回 `NULL`。 |
| `kv_get_text` | `(p_key TEXT)` | `TEXT` | 读取缓存并转换为文本（`convert_from` UTF-8）。未找到返回 `NULL`。 |
| `kv_del` | `(p_key TEXT)` | `VOID` | 删除缓存条目。 |
| `kv_cleanup_batch` | `(max_rows INT DEFAULT 1000)` | `INT` | 删除最多 `max_rows` 条过期条目。返回实际删除数量。 |

### 示例

```sql
-- 写入缓存条目（文本转换为 bytea）
SELECT kv_set('session:abc123', 'user_id=42'::bytea, 3600);

-- 以文本读取
SELECT kv_get_text('session:abc123');
-- 返回: user_id=42

-- 以原始 bytea 读取（十六进制编码）
SELECT kv_get('session:abc123');
-- 返回: \x757365725f69643d3432

-- 检查键是否存在（过期或缺失返回 NULL）
SELECT kv_get('session:xyz') IS NOT NULL;

-- 删除键
SELECT kv_del('session:abc123');

-- 清理过期条目（通过 cron 或应用定期运行）
SELECT kv_cleanup_batch(1000);
```

## JSONB 变体

结构化 JSON 缓存，支持字段级更新和包含查询。值存储为 `JSONB`，带 GIN 索引用于高效的 `@>` 查询。

### 表

```sql
CREATE UNLOGGED TABLE kv_cache_json (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_kv_cache_json_gin ON kv_cache_json USING GIN (value);
```

GIN 索引服务于用户编写的临时查询（`WHERE value @> '{"status":"active"}'`）。函数 API 使用主键查找，因此 `json_get`/`json_set` 不使用该索引。

### 函数

| 函数 | 签名 | 返回值 | 描述 |
|------|------|--------|------|
| `json_set` | `(p_key TEXT, p_value JSONB, p_ttl_seconds INT DEFAULT 300)` | `VOID` | 以 UPSERT 原子性写入 JSON 缓存。默认 TTL 300秒。 |
| `json_set_with_result` | `(p_key TEXT, p_value JSONB, p_ttl_seconds INT DEFAULT 300)` | `BOOLEAN` | 与 `json_set` 相同，成功返回 `TRUE`。 |
| `json_get` | `(p_key TEXT)` | `JSONB` | 读取整个 JSON 对象。未找到或已过期返回 `NULL`。 |
| `json_get_field` | `(p_key TEXT, p_field TEXT)` | `TEXT` | 以文本读取指定字段（`->>` 操作符）。键或字段缺失返回 `NULL`。 |
| `json_get_field_json` | `(p_key TEXT, p_field TEXT)` | `JSONB` | 以 JSONB 读取指定字段（`->` 操作符）。键或字段缺失返回 `NULL`。 |
| `json_contains` | `(p_key TEXT, p_query JSONB)` | `BOOLEAN` | 检查缓存的 JSON 是否包含查询子集（`@>` 操作符）。键缺失返回 `FALSE`。 |
| `json_update_field` | `(p_key TEXT, p_field TEXT, p_new_value TEXT, p_ttl_seconds INT DEFAULT NULL)` | `BOOLEAN` | 部分更新指定字段。智能类型转换（数字/布尔/数组/对象/字符串）。`p_ttl_seconds NULL` 保留原过期时间；非 NULL 重置它。`p_new_value SQL NULL` 返回 `FALSE`（防止意外字段删除）。键缺失返回 `FALSE`。 |
| `json_del` | `(p_key TEXT)` | `VOID` | 删除 JSON 缓存条目。 |
| `json_cleanup_batch` | `(max_rows INT DEFAULT 1000)` | `INT` | 删除最多 `max_rows` 条过期条目。返回实际删除数量。 |

### 示例

```sql
-- 写入 JSON 对象
SELECT json_set('user:42', '{"name":"Alice","age":30,"roles":["admin","user"]}'::jsonb, 3600);

-- 以文本读取字段
SELECT json_get_field('user:42', 'name');
-- 返回: Alice

-- 以 JSONB 读取字段（保留类型）
SELECT json_get_field_json('user:42', 'age');
-- 返回: 30（整数类型）

-- 读取整个对象
SELECT json_get('user:42');
-- 返回: {"age": 30, "name": "Alice", "roles": ["admin", "user"]}

-- 检查对象是否包含子集
SELECT json_contains('user:42', '{"age":30}'::jsonb);
-- 返回: t

-- 更新单个字段（保留其他字段，智能类型转换）
SELECT json_update_field('user:42', 'age', '31');
SELECT json_get_field('user:42', 'age');
-- 返回: 31（仍是整数，不是字符串）

-- 添加新字段
SELECT json_update_field('user:42', 'email', 'alice@example.com');

-- 更新字段并重置 TTL
SELECT json_update_field('user:42', 'age', '32', 7200);

-- 更新字段不改变过期时间
SELECT json_update_field('user:42', 'age', '33', NULL);

-- 删除键
SELECT json_del('user:42');

-- 清理过期条目
SELECT json_cleanup_batch(1000);
```

## 并发与原子性

两个变体都使用 `ON CONFLICT`（UPSERT）实现原子写入。对同一键的并发 `kv_set` 调用不会竞态——一个获胜，另一个覆盖。除单行外不持有锁。

读操作（`kv_get`、`json_get`）无副作用且永不阻塞。过期条目通过 `WHERE expires_at > NOW()` 过滤——它们不会物理删除，直到 `kv_cleanup_batch` 运行。

## 过期与清理

条目基于 TTL（默认 300 秒）过期。过期条目**不会自动删除**——它们保留在表中，直到调用 `kv_cleanup_batch` 或 `json_cleanup_batch`。

**推荐的清理策略：**

1. **应用层 cron** —— 每 5-10 分钟从应用或 cron 任务调用 `kv_cleanup_batch(1000)`。
2. **pg_cron 扩展** —— 如已安装，通过 `cron.schedule` 调度 `SELECT kv_cleanup_batch(1000)`。
3. **惰性清理** —— 每 N 次写入后调用清理（例如 `if (rand() < 0.01) kv_cleanup_batch(1000)`）。

`max_rows` 参数限制每次清理，防止长事务。每批 1000 行，即使有数百万过期条目，清理也只需 <10ms。

## 限制

- **无持久化** —— UNLOGGED TABLE 意味着崩溃时数据丢失。缓存可接受，主数据不可接受。
- **无复制** —— UNLOGGED TABLE 不被逻辑复制复制（它们被物理流复制复制，但仅表结构，非数据）。
- **无分区** —— 所有键在单表中。对于超大缓存（>1000万键），考虑按键前缀分片或使用 Redis。
- **无 TTL 索引** —— 过期条目未单独索引。清理使用普通 B 树索引扫描 `expires_at < NOW()`。

## 验证

部署后，验证函数是否存在：

```bash
# 检查函数数量（--type all 应为 15）
pg exec -i myinst -c "SELECT COUNT(*) FROM pg_proc WHERE proname LIKE 'kv_%' OR proname LIKE 'json_%'"

# 列出所有 KV 函数
pg exec -i myinst -c "SELECT proname FROM pg_proc WHERE proname LIKE 'kv_%' OR proname LIKE 'json_%' ORDER BY proname"
```

`--type all` 的预期输出：

```
         proname
-------------------------
 json_cleanup_batch
 json_contains
 json_del
 json_get
 json_get_field
 json_get_field_json
 json_set
 json_set_with_result
 json_update_field
 kv_cleanup_batch
 kv_del
 kv_get
 kv_get_text
 kv_set
 kv_set_with_result
(15 rows)
```

## 测试

`scripts/test-kv-cache.sh` 提供了测试脚本。覆盖所有 15 个函数（6 个 BYTEA + 9 个 JSONB），包括边缘情况（NULL 处理、TTL 保留、过期条目清理）。

```bash
# 对本地实例运行测试
bash scripts/test-kv-cache.sh myinst

# 对默认实例运行测试
bash scripts/test-kv-cache.sh
```

如果所有测试通过，脚本以代码 0 退出；如果有任何失败，以代码 1 退出。在 CI 或升级后使用它确认函数按预期工作。

## 卸载

没有 `pg kv remove` 命令。函数和表保留在数据库中，直到手动删除：

```sql
-- 移除 BYTEA 变体
DROP TABLE IF EXISTS kv_cache CASCADE;

-- 移除 JSONB 变体
DROP TABLE IF EXISTS kv_cache_json CASCADE;
```

`CASCADE` 子句自动删除依赖函数。
