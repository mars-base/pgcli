---
title: 恢复
description: pgcli 时间点恢复（PITR）指南
weight: 30
icon: fa-solid fa-clock-rotate-left
cascade:
  type: docs
  footer_style: slim
---


## 时间点恢复（PITR）

恢复到首次备份后的任意时间点。

```bash
# 恢复（只读，提交前检查）
pg restore --time "2026-08-26 15:30:00+00"

# 预览将要恢复的内容而不执行（试运行）
pg restore --time "2026-08-26 15:30:00+00" --dry-run

# 恢复期间流式输出恢复容器日志
pg restore --time "2026-08-26 15:30:00+00" --tail-logs

# 如果需要可以尝试不同的时间
pg restore --time "2026-08-26 15:25:00+00"

# 提升为读写（切换时间线）
pg restore --time "2026-08-26 15:30:00+00" --promote

# 跳过确认
pg restore --time "2026-08-26 15:30:00+00" --promote --force
```

**时间格式：**
- `2026-08-26 15:30:00+08:00` — 带时区偏移
- `2026-08-26 15:30:00+08` — 仅时区小时
- `2026-08-26 15:30:00Z` — UTC
- `2026-08-26 15:30:00` — 假定为 UTC

**恢复工作流：** 停止 → 恢复 → 启动 → WAL 重放到目标时间

**注意：** `--promote` 之后，在进行进一步的 PITR 之前需要创建新的完整快照。
