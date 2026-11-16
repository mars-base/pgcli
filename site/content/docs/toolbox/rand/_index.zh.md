---
title: "随机字符串"
description: "生成密码、token 等密码学安全的随机字符串，支持自定义长度和字符集"
weight: 60
icon: fa-solid fa-dice
aliases: ["/toolbox/rand/"]
menus:
  main:
    identifier: docs-toolbox-rand
    parent: docs-toolbox
    weight: 60
    params:
      icon: fa-solid fa-dice
cascade:
  type: docs
  footer_style: slim
---

`pg rand` 用于生成密码、API token、会话密钥等随机字符串。字符来自
`crypto/rand`；输出为裸字符串，每行一条，不带标签或额外格式，可以直接被
shell 脚本捕获。

```bash
pg rand
```

默认生成 24 位字母数字字符串（`a-z A-Z 0-9`），熵约 143 bit。

## 参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-l, --length N` | `24` | 每条字符串的长度（1–4096） |
| `-n, --count N` | `1` | 输出条数，每行一条（1–1000） |
| `--charset NAME` | `alnum` | 字符集预设（见下表） |
| `--extra-chars CHARS` | 空 | 追加到预设字符集的额外可打印 ASCII 字符 |

## 字符集

| 名称 | 字符 | 适用场景 |
|------|------|----------|
| `alnum` | `a-z A-Z 0-9` | 兼容性最好的密码和 token（默认） |
| `alpha` | `a-z A-Z` | 不能包含数字的密钥 |
| `lower` | `a-z` | 大小写不敏感的标识符 |
| `upper` | `A-Z` | 大小写不敏感的标识符 |
| `digits` | `0-9` | 数字 PIN 和验证码 |
| `hex` | `0-9 a-f` | API key、校验和、配置值 |
| `base64url` | `a-z A-Z 0-9 - _` | 可安全用于 URL、Cookie 和 JWT 的 token |
| `password` | `alnum` 加 `!@#$%^&*()-_=+[]{}<>?:;,.~` | 要求包含符号的密码 |

`--extra-chars` 可以在任意预设上追加自定义字符。重复字符会被忽略，保证每个
字符出现概率一致：

```bash
pg rand -l 32 --extra-chars '#%'
```

## 示例

```bash
# 单条 24 位字母数字字符串
pg rand

# 32 位字母、数字和符号
pg rand -l 32 --charset password

# 5 条 48 位 hex key
pg rand -n 5 -l 48 --charset hex

# 生成 URL 安全 token 并赋给变量
TOKEN=$(pg rand -l 48 --charset base64url)

# 用于 SQL 的密码（符号属于 shell 元字符，注意加引号）
pg rand -l 32 --charset password
```

生成的密码含符号时，在 shell 命令和 SQL 中务必加引号。用于
`ALTER ROLE ... PASSWORD` 时应作为带引号的字符串字面量传入，或改用支持参数的
客户端。

## 24 位够用吗？

对大多数场景够用。默认 `alnum` 字符集有 62 个字符，每个字符携带
`log2(62) ≈ 5.95` bit，24 位约 **143 bit**，高于 API token 和会话密钥常用的
128-bit 标准，也远高于人工选择密码的熵。即使尝试速率极高，24 位字符串仍然
无法被暴力穷举。

两种情况下建议改用其他参数：

- **`hex`**：每个字符只有 4 bit，24 位仅 96 bit。需要 128-bit token 时用
  `-l 32`。
- **长期有效的高价值密钥**（如根凭证、加密主密钥）：用 `-l 32` 增加余量，
  代价可以忽略。

如果系统强制要求“同时包含大写、小写、数字和符号”，改用
`--charset password`（24 位约 155 bit），而不是默认的 `alnum`。长度只是一个
因素：服务端的限流和密码哈希策略至少同样重要。

## 安全说明

- 随机源为 `crypto/rand`，适用于密钥场景；结果不可复现，也没有 seed 参数。
- 每字符熵为 `log2(字符集大小)`：`alnum` 约 5.95 bit，`password` 约 6.46 bit，
  `hex` 为 4 bit。默认 24 位 `alnum` 约 143 bit。
- 输出只写到 stdout；`pg rand` 不读写 `pg.yaml`，也不依赖数据库实例。
