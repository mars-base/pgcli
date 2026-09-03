---
title: 克隆
description: pgcli 克隆指南
weight: 20
icon: fa-solid fa-book
cascade:
  type: docs
  footer_style: slim
---


创建新实例，其数据从现有实例复制，直接流式传输——磁盘上无临时文件。

```bash
# 克隆默认实例
pg clone test02

# 克隆特定实例
pg clone test02 -i proj01

# 通过连接字符串克隆远程数据库
pg clone test02 --dsn postgres://user:pass@host:5432/db

# 新实例的自定义数据目录
pg clone test02 -i proj01 --base-dir /data/pg
```

## 发生了什么

1. **预检** — 在创建任何内容*之前*验证源：
   - 本地实例：容器必须正在运行
   - `--dsn`：认证的 `SELECT 1` 必须成功（捕获错误密码、不可达主机）
2. **创建** — 新实例条目添加到配置，带有随机密码、自己的容器名称、数据目录和自动分配的端口
3. **启动** — 新实例启动（与 `pg start` 相同的工作流）
4. **流式传输** — 源数据管道传输到目标，每秒显示一次实时传输进度

## 注释

- 源实例必须正在运行（或 `--dsn` 目标可达）；错误的源会立即失败且无副作用
- 新实例名称在配置中不能已存在
- `--dsn` 和 `--instance` 互斥：使用 `--dsn` 时连接字符串确定主机、端口和数据库
- 新实例获得新的随机密码——在克隆输出或 `pg status -i <name>` 中查找
- 仅逻辑复制（模式 + 数据）；对于大型数据库，物理方法可能更快
