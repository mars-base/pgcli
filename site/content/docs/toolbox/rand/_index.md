---
title: "Random Strings"
description: "Generate cryptographically secure passwords, tokens and secrets with a configurable length and character set"
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

`pg rand` generates random strings for passwords, API tokens, session secrets
and similar values. Characters come from `crypto/rand`; output is raw, one
string per line, with no labels or extra formatting, so it can be captured
directly in shell scripts.

```bash
pg rand
```

The default is a 24-character alphanumeric string (`a-z A-Z 0-9`), about 143
bits of entropy.

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `-l, --length N` | `24` | Characters per string (1–4096) |
| `-n, --count N` | `1` | Number of strings to print, one per line (1–1000) |
| `--charset NAME` | `alnum` | Preset character set (see below) |
| `--extra-chars CHARS` | empty | Extra printable ASCII characters appended to the preset |

## Character sets

| Name | Characters | Use for |
|------|------------|---------|
| `alnum` | `a-z A-Z 0-9` | Passwords and tokens accepted by the widest range of systems (default) |
| `alpha` | `a-z A-Z` | Secrets that must not contain digits |
| `lower` | `a-z` | Case-insensitive identifiers |
| `upper` | `A-Z` | Case-insensitive identifiers |
| `digits` | `0-9` | Numeric PINs and codes |
| `hex` | `0-9 a-f` | API keys, checksums, config values |
| `base64url` | `a-z A-Z 0-9 - _` | URL, cookie and JWT-safe tokens |
| `password` | `alnum` plus `!@#$%^&*()-_=+[]{}<>?:;,.~` | Passwords that must satisfy symbol requirements |

`--extra-chars` adds your own characters to any preset. Duplicates are ignored,
so every character keeps the same probability:

```bash
pg rand -l 32 --extra-chars '#%'
```

## Examples

```bash
# Single 24-character alphanumeric string
pg rand

# 32 characters with letters, digits and symbols
pg rand -l 32 --charset password

# Five 48-character hex keys
pg rand -n 5 -l 48 --charset hex

# URL-safe token assigned to a variable
TOKEN=$(pg rand -l 48 --charset base64url)

# Password for a SQL statement (quote it; symbols are shell metacharacters)
pg rand -l 32 --charset password
```

When a generated password contains symbols, always quote it in shell commands
and SQL. For `ALTER ROLE ... PASSWORD`, pass it as a quoted string literal or
use a parameterized client.

## Security notes

- Randomness comes from `crypto/rand`, suitable for secrets; it is not
  reproducible and has no seed flag.
- Entropy per character is `log2(len(charset))`: about 5.95 bits for `alnum`,
  6.46 for `password`, and 4 for `hex`. The 24-character default is roughly 143
  bits with `alnum`.
- Output is printed to stdout only; `pg rand` does not read or write `pg.yaml`
  and does not require a database instance.
