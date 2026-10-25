---
title: ZFS 存储管理
description: 在 pgcli 中安装 ZFS 并管理 pool、dataset 和 snapshot
weight: 10
icon: fa-solid fa-hard-drive
aliases: ["/docs/zfs/", "/toolbox/zfs/"]
menus:
  main:
    identifier: docs-toolbox-zfs
    parent: docs-toolbox
    weight: 10
    params:
      icon: fa-solid fa-hard-drive
cascade:
  type: docs
  footer_style: slim
---

`pg zfs` 把一台 Linux 主机变成 ZFS 存储服务器：安装软件包、检查磁盘、创建带冗余的
pool，并管理 dataset 和 snapshot。它直接与内核交互，不读写 `pg.yaml`，也不依赖任何
PostgreSQL 实例。

命令按“资源 → 动作”组织：`pg zfs pool create`、`pg zfs dataset create`、
`pg zfs snapshot rollback`。没有封装的能力可通过 `pg zfs raw` 原样透传。

## 支持的系统

`pg zfs install` 从 `/etc/os-release` 识别发行版，并自动准备软件源后完成安装：

| 发行版 | ZFS 来源 | 实测版本 |
|---|---|---|
| Debian 12 (bookworm) | Debian `contrib`，DKMS | 2.1.11 |
| Debian 13 (trixie) | Debian `contrib`，DKMS | 2.3.9 |
| Ubuntu 24.04 (noble) | Ubuntu `universe` | 2.2.2，使用 Ubuntu 内核模块包 |
| Ubuntu 26.04 | Ubuntu `universe` | 2.4.1 |
| Fedora 44 | OpenZFS release RPM，DKMS | 2.4.4 |
| Rocky Linux 10 | OpenZFS release RPM + EPEL，DKMS | 2.2.11 |

这些是安装器测试时上游仓库提供的版本，实际安装到的版本由仓库决定；
`pg zfs status` 总会打印已安装的用户态与内核模块版本。

其他发行版只会被识别并给出手动安装指引，不会自动安装。仅支持 Linux。

## 安装

```bash
pg zfs install
```

命令零参数、幂等：

1. 检查权限（root，或可用的 `sudo -n`）。
2. 自动启用所需软件源（Debian `contrib`、Ubuntu `universe`、
   Fedora/Rocky 的 OpenZFS release 仓库）。
3. 安装 ZFS、内核头文件/模块和 ZED 守护进程。
4. 加载模块并把 ZFS 的 systemd 单元设为开机自启。
5. 打印用户态与内核模块版本。

在健康的机器上重复执行是 no-op。安装不提供 `--version`、`--backports`、
`--force` 等开关——需要特定 ZFS 版本时请用发行版包管理器升级，之后交由
`pg zfs` 管理。

随时查看状态：

```bash
pg zfs status
```

```
OS:            Debian GNU/Linux 12 (bookworm)
Family:        debian
ZFS:           installed
Userland:      zfs-2.1.11-1+deb12u1
Kernel module: 2.1.11-1+deb12u1 (loaded)
Kernel:        6.1.0-54-cloud-amd64
Privilege:     root
Pools:         none
Secure Boot:   disabled
```

## RAID 级别与 ZFS 的对应关系

ZFS 没有标准的 RAID2 / RAID3 级别，对应关系如下：

| `--type` | ZFS 布局 | 冗余 | 类比 |
|---|---|---|---|
| `stripe`（`raid0`） | 条带 vdev | 无 | RAID0 |
| `mirror`（`raid1`） | 镜像 | 1 份以上副本 | RAID1 |
| `raidz1` | 单校验 | 1 块盘 | RAID5 |
| `raidz2`（`raid2`） | 双校验 | 2 块盘 | RAID6 |
| `raidz3`（`raid3`） | 三校验 | 3 块盘 | 三校验 RAID |

为方便起见也接受 `raid0`/`raid1`/`raid2`/`raid3` 别名。

## 快速开始

```bash
# 1. 安装 ZFS
pg zfs install

# 2. 查看哪些磁盘可用
pg zfs disk list

# 3. 两块整盘 -> 自动创建 mirror
pg zfs pool create tank /dev/vdb /dev/vdc

# 4. 确认 pool 在线
pg zfs pool list

# 5. 为备份创建 dataset
pg zfs dataset create tank/backups

# 6. 升级前创建快照
pg zfs snapshot create tank/backups@before-upgrade

# 7. 升级失败时回滚
pg zfs snapshot rollback tank/backups@before-upgrade
```

pool 挂载在 `/tank`，dataset 挂载在 `/tank/backups`。回滚会丢弃快照之后的所有
改动，执行前需要确认。

### 复用旧 pool 的磁盘

ZFS 会在盘上保留 vdev 分区和标签，`export` 不会清除它们，因此安全校验会拒绝
这些盘。创建新 pool 前先销毁旧 pool 并清理残留：

```bash
# 1. 旧 pool 的数据不再需要时，先导入再销毁
pg zfs pool import fast
pg zfs pool destroy fast --force

# 2. 清除 ZFS 标签和分区表
pg zfs raw zpool labelclear -f /dev/vdd
pg zfs raw zpool labelclear -f /dev/vde
wipefs -a /dev/vdd /dev/vde

# 3. 创建新 pool
pg zfs pool create tank /dev/vdd /dev/vde
```

如果整盘 `labelclear` 报错，改清成员分区（`/dev/vdd1`、`/dev/vde1`）。旧 pool
已被销毁时跳过第 1 步。

## 磁盘安全

`pg zfs disk list` 会列出每块整盘的挂载点、ZFS 归属以及 pgcli 是否认为它可以安全
交给 ZFS 使用。

创建 pool 时拒绝：

- 根盘，或带有已挂载分区的磁盘；
- 分区、loop、ROM、CD-ROM 设备；
- 带有文件系统（`ext4`、`xfs`、swap、LVM、LUKS、mdraid 等）的磁盘；
- 已有 ZFS 标签的磁盘；
- 只读磁盘；
- 重复设备（包括通过不同路径指向的同一块盘）；
- 已被其他 pool 使用的磁盘。

`--force` 只跳过确认提示，**不会**映射为 ZFS 的 `-f`，也不绕过任何安全检查。

## 创建 pool

### 自动布局

位置参数的数量决定布局：

| 盘数 | 布局 |
|---|---|
| 1 | stripe（告警：无冗余） |
| 2 | mirror |
| 3 | raidz1 |
| 4 及以上 | raidz2 |

```bash
pg zfs pool create tank /dev/vdb /dev/vdc              # mirror
pg zfs pool create data /dev/vdb /dev/vdc /dev/vdd     # raidz1
pg zfs pool create bulk /dev/vdb /dev/vdc /dev/vdd /dev/vde   # raidz2
```

用 `--type` 覆盖：

```bash
pg zfs pool create fast /dev/vdb /dev/vdc /dev/vdd /dev/vde --type mirror
pg zfs pool create safe /dev/vdb /dev/vdc /dev/vdd /dev/vde /dev/vdf --type raidz3
```

同一 vdev 内请尽量使用容量一致的磁盘（性能也应相近）。ZFS 不会拒绝混合容量，
但 vdev 的可用容量由最小的成员盘决定，大盘多出的空间会被浪费。同一 pool 的不同
vdev 之间可以容量不同。

### 多个 vdev

使用可重复的 `--data` 分组，并显式给出 `--type`。每个分组是一个 vdev，pool 在
这些 vdev 之上做条带：

```bash
# 两个 mirror vdev（条带化镜像）
pg zfs pool create fast --type mirror \
  --data /dev/vdb,/dev/vdc --data /dev/vdd,/dev/vde

# 两个 raidz2 vdev
pg zfs pool create bulk --type raidz2 \
  --data /dev/vdb,/dev/vdc,/dev/vdd,/dev/vde \
  --data /dev/vdf,/dev/vdg,/dev/vdh,/dev/vdi
```

`--type` 作用于每个 `--data` 组内部，而不是 pool 整体：每个组建成该布局的一个
vdev，pool 在这些 vdev 之上条带。所有组共用同一个 `--type`；`pg zfs` 不支持
一次创建中混用 mirror、raidz2 等布局，混合拓扑需使用
`pg zfs raw zpool create`。

pool 容量为各 vdev 可用容量之和；冗余按 vdev 计算，任一 vdev 失去超出其容错能力
的磁盘都会导致整个 pool 不可用。

### SLOG、special、cache 与热备

```bash
pg zfs pool create tank /dev/vdb /dev/vdc \
  --type mirror \
  --special /dev/vdd,/dev/vde \
  --log /dev/vdf,/dev/vdg \
  --cache /dev/vdh \
  --spare /dev/vdi
```

`--special` 跟随 `--type`（这里是 mirror）。`--log` 两块及以上自动 mirror，单块为
stripe 并给出告警。`--cache` 与 `--spare` 接受独立的设备列表。

同一块盘只能出现在 data、special、log、cache、spare 中的一处。

建池后也可以用 `pool add` 追加热备盘：

```bash
pg zfs pool add tank --class spare /dev/vdi
```

### 默认属性

除非指定 `--no-defaults`：

| 对象 | 属性 | 值 |
|---|---|---|
| pool | `ashift` | `12` |
| pool | `autoexpand` | `on` |
| pool | `autotrim` | 所有数据盘均为非旋转盘且支持 discard 时为 `on` |
| 根 dataset | `compression` | `zstd` |
| 根 dataset | `atime` | `off` |
| 根 dataset | `xattr` | `sa` |
| 根 dataset | `acltype` | `posixacl` |
| 根 dataset | `dnodesize` | `auto` |

pool 属性用 `--prop/-p` 覆盖（对应 `zpool -o`），根 dataset 属性用
`--fs-prop/-P` 覆盖（对应 `zpool -O`）：

```bash
pg zfs pool create tank /dev/vdb /dev/vdc \
  --prop ashift=13 --fs-prop compression=lz4 --fs-prop atime=on
```

`-o` 是 pgcli 的全局输出参数，所以 pool/dataset 属性使用 `-p`/`-P`。在
`pg zfs raw` 内部，ZFS 原生参数照常可用。

## 管理 pool

```bash
pg zfs pool list
pg zfs pool status tank
pg zfs pool status --brief          # 只显示有问题的 pool
pg zfs pool scrub tank
pg zfs pool scrub tank --stop
```

扩容与修复：

```bash
# 增加一个 mirror vdev
pg zfs pool add tank /dev/vdd /dev/vde --type mirror

# 把 mirror 从 2 盘扩到 3 盘
pg zfs pool attach tank /dev/vdc /dev/vdd

# 再缩回 2 盘
pg zfs pool detach tank /dev/vdd

# 替换故障盘
pg zfs pool replace tank /dev/vdc /dev/vde

# 临时下线/上线
pg zfs pool offline tank /dev/vdc
pg zfs pool online tank /dev/vdc
```

`attach` 针对 mirror vdev。给 raidz vdev 增加磁盘（raidz expansion）需要
OpenZFS 2.3 及以上；Debian 12（2.1）和 Ubuntu 24.04（2.2）不支持。新版本上可用
`pg zfs raw zpool attach` 操作。

### 卸载与彻底删除 pool

`pg zfs pool export` 会卸载所有 dataset 并把 pool 从主机上分离，但数据仍完整
保存在成员盘上：

```bash
pg zfs pool export tank
```

已 export 的 pool 不会在开机时自动导入。重新接管时先列出可导入的 pool，再导入：

```bash
pg zfs pool import                  # 列出可导入的 pool
pg zfs pool import tank
pg zfs pool import tank --dir /dev/disk/by-id --no-mount
```

`pg zfs pool destroy` 会删除 pool、所有 dataset 和全部数据：

```bash
pg zfs pool destroy tank
```

`destroy` 只能作用于已导入的 pool。如果 pool 已 export，需要先导入再销毁：

```bash
pg zfs pool import tank
pg zfs pool destroy tank
```

不加 `--force` 时这两个命令都会要求确认；EOF 和默认答案均为“否”。非交互自动化
必须显式传 `--force`。`--force` 只跳过确认，绝不会映射为 ZFS 的 `-f`，也不绕过
任何安全校验。export 可逆，destroy 不可逆。

## 持久化与开机自动导入

pool 和 dataset 默认在重启后自动恢复，无需额外配置：

- `pg zfs install` 会启用 `zfs-load-module`、`zfs-import-cache`、`zfs-mount`
  和 `zfs-zed`（单元存在时）。
- `zpool create` 会把 pool 记录到 `/etc/zfs/zpool.cache`；开机时
  `zfs-import-cache` 据此导入。
- ZFS 不使用 `/etc/fstab`。每个 dataset 的 `mountpoint` 是 ZFS 属性，开机时
  由 `zfs-mount` 完成挂载。pool 的根 dataset 挂载在 `/<pool>`，子 dataset
  挂载在其下（例如 `/fast/data`）。
- pool 状态保存在成员盘上，异常断电或非正常关机后数据仍可导入。

检查配置：

```bash
systemctl is-enabled zfs-load-module zfs-import-cache zfs-mount zfs-zed
pg zfs raw zpool get cachefile fast
pg zfs raw zfs get mountpoint fast
```

重启后 `pg zfs pool list` 应能看到该 pool，`findmnt /fast` 应显示已挂载。

真实硬件上建议用 `/dev/disk/by-id/...` 等稳定路径创建 pool。`/dev/sdX`
名称可能在重启后变化，设备找不到时 pool 不会导入。把磁盘移到其他主机前先执行
`pg zfs pool export tank`，对方即可用 `pg zfs pool import tank` 接管。

## Dataset 与 snapshot

```bash
pg zfs dataset create tank/data
pg zfs dataset create tank/a/b/c --parents
pg zfs dataset create tank/logs --prop quota=50G --prop recordsize=128K

pg zfs dataset list
pg zfs dataset list tank/data --props quota,recordsize,compression

pg zfs dataset set tank/data compression=lz4 quota=100G

pg zfs snapshot create tank/data@snap1
pg zfs snapshot create tank/data@weekly --recursive
pg zfs snapshot list tank/data
pg zfs snapshot rollback tank/data@snap1
pg zfs snapshot destroy tank/data@snap1

pg zfs dataset clone tank/data@snap1 tank/clone --prop compression=off
pg zfs dataset destroy tank/clone
pg zfs dataset destroy tank/a --recursive
```

带子 dataset 的 dataset 必须加 `--recursive` 才能销毁。

### 回滚

`pg zfs snapshot rollback tank/data@snap1` 把 dataset 恢复到快照记录的状态，
之后的所有改动都会被丢弃。如果存在更新的快照，ZFS 会拒绝回滚；加 `--recursive`
会先删除这些更新的快照。依赖该快照的 clone 也会阻止回滚——先删除 clone，或用
`pg zfs raw zfs rollback -R`（会一并删除依赖的 clone）。非交互运行需加
`--force` 跳过确认。

```bash
# 升级前创建快照
pg zfs snapshot create tank/data@before-upgrade

# 回滚（需要确认）
pg zfs snapshot rollback tank/data@before-upgrade

# 非交互自动化
pg zfs snapshot rollback tank/data@before-upgrade --force

# 存在更新的快照：--recursive 会一并删除
pg zfs snapshot rollback tank/data@before-upgrade --recursive --force

# clone 阻止回滚：-R 会一并删除依赖的 clone
pg zfs raw zfs rollback -R tank/data@before-upgrade
```

### 克隆

`pg zfs dataset clone tank/data@snap1 tank/clone` 从快照创建一个新的可写
dataset。未改动的数据块按写时复制共享，不会复制整份数据。用 `--prop` 设置新
dataset 的属性：

```bash
# 从快照创建可写副本
pg zfs dataset clone tank/data@snap1 tank/clone

# 为副本设置属性
pg zfs dataset clone tank/data@snap1 tank/clone \
  --prop compression=off --prop mountpoint=/tank/clone

# 提升 clone 断开依赖，之后可删除原快照
pg zfs raw zfs promote tank/clone
pg zfs snapshot destroy tank/data@snap1
pg zfs dataset destroy tank/clone --force
```

clone 依赖的快照在 clone 存在期间不能销毁。需要删除旧快照时，先用
`pg zfs raw zfs promote tank/clone` 提升 clone，使其独立于原 dataset。

## raw 透传

```bash
pg zfs raw zpool status -v
pg zfs raw zpool set comment="backup pool" tank
pg zfs raw zfs create -o compression=off tank/scratch
pg zfs raw zpool create -f -O compression=gzip temp /dev/vdb
```

`raw` 绕过所有安全校验和确认，是未封装参数的逃生通道——请像以 root 身份直接运行
`zpool`/`zfs` 一样谨慎。二进制管道也可以工作，例如：

```bash
pg zfs snapshot create tank/data@send
pg zfs raw zfs send tank/data@send | pg zfs raw zfs receive tank/recv
```

## 权限模型

所有会调用 `zpool` 或 `zfs` 的命令都会先检查权限：

- root：直接执行；
- 非 root 且 `sudo -n` 可用：所有 ZFS 命令通过 `sudo -n` 执行；
- 否则立即失败：

  ```
  ZFS management requires root: run "sudo pg zfs ..." or configure passwordless sudo for this user
  ```

pgcli 从不弹出 sudo 密码提示。`pg zfs status` 和 `pg zfs disk list` 在无权限时仍会
输出静态信息，并把 pool 详情标记为不可用。

## 命令参考

| 命令 | 用途 |
|---|---|
| `pg zfs install` | 安装 ZFS（零参数、幂等） |
| `pg zfs status` | 发行版、软件包、模块与 pool 概览 |
| `pg zfs disk list [--all]` | 整盘、挂载、ZFS 归属与安全性 |
| `pg zfs pool create NAME DEVICE...` | 创建 pool |
| `pg zfs pool list` | 列出 pool |
| `pg zfs pool status [NAME] [--brief]` | 详细状态 |
| `pg zfs pool add NAME DEVICE...` | 增加 vdev |
| `pg zfs pool attach/detach NAME DEVICE [NEW]` | 扩大/缩小 mirror |
| `pg zfs pool replace NAME OLD NEW` | 替换磁盘 |
| `pg zfs pool offline/online NAME DEVICE` | 下线/上线磁盘 |
| `pg zfs pool export/import [NAME]` | 在系统间迁移 pool |
| `pg zfs pool scrub NAME [--stop]` | 校验数据完整性 |
| `pg zfs pool destroy NAME` | 销毁 pool 及全部数据 |
| `pg zfs dataset create/list/set/clone/destroy` | dataset 生命周期 |
| `pg zfs snapshot create/list/rollback/destroy` | snapshot 生命周期 |
| `pg zfs raw zpool/zfs ARGS...` | 绕过封装的透传 |

## 故障排查

**`pg zfs install` 找不到软件包。** 检查发行版归档源是否可达、`contrib`/`universe`
是否已启用。安装器只修改发行版官方归档源（绝不动第三方源），并在每个被修改的文件旁
留下 `*.pgcli.bak`。

**DKMS 构建失败。** 确认当前内核的头文件已安装（Debian/Ubuntu 为
`linux-headers-$(uname -r)`，Fedora/Rocky 为 `kernel-devel-$(uname -r)`）。安装器
会给出构建日志位置；它不会重启或更换内核。内存很小的 VM 请先加 swap。

**Secure Boot 已开启。** DKMS 模块需要签名。pgcli 只报告该情况，不会自动登记密钥；
请用 MOK 签名模块，或在测试机上关闭 Secure Boot。

**模块未加载。** 执行 `modprobe zfs` 后运行 `pg zfs status`。安装器会启用
`zfs-load-module`、`zfs-import-cache`、`zfs-mount` 和 `zfs-zed`，使 pool 开机
自动导入。

**磁盘因分区或旧标签被拒绝。** 如果确认磁盘空闲，清理残留：

```bash
pg zfs raw zpool labelclear -f /dev/vdb
wipefs -a /dev/vdb
```

在 OpenZFS 2.2 及更新版本上，若标签位于 ZFS 成员分区内，清理整盘会报
`failed to clear label for /dev/vdb`。此时改为清理该分区，再清分区表：

```bash
pg zfs raw zpool labelclear -f /dev/vdb1
wipefs -a /dev/vdb
```

绝不要对已导入 pool 的成员盘这样做——`pg zfs disk list` 会显示所属 pool，安全检查
也会拒绝这类磁盘。

**SELinux（Fedora/Rocky）。** ZFS 挂载点使用标准文件上下文。若服务无法访问某个
dataset，请查看 `ausearch`/`journalctl` 中的 AVC 拒绝记录，而不是全局关闭 SELinux。

**重启后 pool 没有自动导入。** 以 root 运行 `systemctl status zfs-import-cache`
和 `zpool import`。用 `/dev/sdX` 名称创建的 pool 会受设备重命名影响；真实硬件上
建议使用稳定路径（`/dev/disk/by-id/...`）创建 pool。
