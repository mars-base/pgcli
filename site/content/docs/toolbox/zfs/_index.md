---
title: "ZFS Storage"
description: "Install ZFS and manage pools, datasets and snapshots directly from pgcli"
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

`pg zfs` turns a Linux host into a ZFS storage server: install the packages, inspect
disks, create redundancy-aware pools, and manage datasets and snapshots. It talks
directly to the kernel — no state is written to `pg.yaml`, and it does not need a
PostgreSQL instance.

The command family is deliberately resource-oriented: `pg zfs pool create`,
`pg zfs dataset create`, `pg zfs snapshot rollback`. Anything not wrapped is
reachable through `pg zfs raw`.

## Supported systems

`pg zfs install` detects the distribution from `/etc/os-release` and installs
everything needed, including package repositories:

| Distribution | ZFS source | Version tested |
|---|---|---|
| Debian 12 (bookworm) | Debian `contrib`, DKMS | 2.1.11 |
| Debian 13 (trixie) | Debian `contrib`, DKMS | 2.3.9 |
| Ubuntu 24.04 (noble) | Ubuntu `universe` | 2.2.2 from the Ubuntu kernel package |
| Ubuntu 26.04 | Ubuntu `universe` | 2.4.1 |
| Fedora 44 | OpenZFS release RPM, DKMS | 2.4.4 |
| Rocky Linux 10 | OpenZFS release RPM + EPEL, DKMS | 2.2.11 |

These are the versions the upstream repositories shipped when the installer was
tested; the repositories decide which release you get. `pg zfs status` always
prints the installed userland and kernel-module versions.

Other distributions are detected and given manual instructions rather than
installed automatically. Only Linux is supported.

## Install

```bash
pg zfs install
```

The command is zero-configuration and idempotent:

1. Verifies privileges (root, or working `sudo -n`).
2. Enables the required repository (`contrib` on Debian, `universe` on Ubuntu,
   the OpenZFS release repository on Fedora and Rocky).
3. Installs ZFS, kernel headers/module and the ZED daemon.
4. Loads the module and enables the ZFS systemd units for boot.
5. Prints userland and kernel-module versions.

Running it again on a healthy host is a no-op. There are no `--version`,
`--backports` or `--force` switches — use the distribution package manager when
you need a specific ZFS build, then let `pg zfs` manage it.

Verify the result at any time:

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

## RAID levels and ZFS

ZFS has no standard RAID2/RAID3 levels. The pool types map as follows:

| `--type` | ZFS layout | Redundancy | Analogy |
|---|---|---|---|
| `stripe` (`raid0`) | striped vdev | none | RAID0 |
| `mirror` (`raid1`) | mirror | 1+ copies | RAID1 |
| `raidz1` | single parity | 1 disk | RAID5 |
| `raidz2` (`raid2`) | double parity | 2 disks | RAID6 |
| `raidz3` (`raid3`) | triple parity | 3 disks | triple-parity RAID |

The aliases `raid0`/`raid1`/`raid2`/`raid3` are accepted for convenience.

## Quick start

```bash
# 1. Install ZFS
pg zfs install

# 2. See which disks are usable
pg zfs disk list

# 3. Two whole disks -> mirror automatically
pg zfs pool create tank /dev/vdb /dev/vdc

# 4. Confirm the pool is online
pg zfs pool list

# 5. A dataset for backups
pg zfs dataset create tank/backups

# 6. Snapshot before an upgrade
pg zfs snapshot create tank/backups@before-upgrade

# 7. Roll back if the upgrade went wrong
pg zfs snapshot rollback tank/backups@before-upgrade
```

The pool is mounted at `/tank`, the dataset at `/tank/backups`. Rollback
discards every change made after the snapshot and asks for confirmation.

### Reusing disks from an old pool

ZFS keeps its vdev partitions and labels on the disks, and `export` does not
remove them, so the safety check refuses such disks. Destroy the old pool and
clear the leftovers before creating a new one:

```bash
# 1. Import and destroy the old pool if its data is no longer needed
pg zfs pool import fast
pg zfs pool destroy fast --force

# 2. Clear the ZFS labels and the partition table
pg zfs raw zpool labelclear -f /dev/vdd
pg zfs raw zpool labelclear -f /dev/vde
wipefs -a /dev/vdd /dev/vde

# 3. Create the new pool
pg zfs pool create tank /dev/vdd /dev/vde
```

If `labelclear` fails on the whole disk, clear the member partitions instead
(`/dev/vdd1`, `/dev/vde1`). Skip step 1 when the old pool was already destroyed.

## Disk safety

`pg zfs disk list` reports every whole disk with its mountpoints, current ZFS
membership and whether pgcli considers it safe to hand to ZFS.

Pool creation refuses:

- the root disk, or a disk with a mounted partition;
- partitions, loop, ROM and CD-ROM devices;
- disks carrying a filesystem (`ext4`, `xfs`, swap, LVM, LUKS, mdraid, ...);
- disks with an existing ZFS label;
- read-only disks;
- duplicate devices, including duplicates reached through different paths;
- disks already used by another pool.

`--force` skips only the confirmation prompt. It is **never** passed to ZFS as
`-f`, and it does not bypass any safety check.

## Creating pools

### Automatic layouts

Positional devices select the layout:

| Disks | Layout |
|---|---|
| 1 | stripe (warning: no redundancy) |
| 2 | mirror |
| 3 | raidz1 |
| 4 or more | raidz2 |

```bash
pg zfs pool create tank /dev/vdb /dev/vdc              # mirror
pg zfs pool create data /dev/vdb /dev/vdc /dev/vdd     # raidz1
pg zfs pool create bulk /dev/vdb /dev/vdc /dev/vdd /dev/vde   # raidz2
```

Override with `--type`:

```bash
pg zfs pool create fast /dev/vdb /dev/vdc /dev/vdd /dev/vde --type mirror
pg zfs pool create safe /dev/vdb /dev/vdc /dev/vdd /dev/vde /dev/vdf --type raidz3
```

Use disks of the same capacity (and similar performance) within a vdev. ZFS
does not reject mixed sizes, but a vdev's usable capacity is limited by its
smallest member, so the extra space on larger disks is wasted. Different vdevs
in one pool may have different capacities.

### Multiple vdevs

Use repeatable `--data` groups together with an explicit `--type`. Each group is
one vdev; the pool stripes across them:

```bash
# Two mirror vdevs (striped mirrors)
pg zfs pool create fast --type mirror \
  --data /dev/vdb,/dev/vdc --data /dev/vdd,/dev/vde

# Two raidz2 vdevs
pg zfs pool create bulk --type raidz2 \
  --data /dev/vdb,/dev/vdc,/dev/vdd,/dev/vde \
  --data /dev/vdf,/dev/vdg,/dev/vdh,/dev/vdi
```

`--type` applies inside each `--data` group, not to the pool as a whole: every
group becomes one vdev of that layout, and the pool stripes across the vdevs.
All groups share the same `--type`; `pg zfs` cannot mix, for example, one mirror
vdev and one raidz2 vdev in a single create. Use `pg zfs raw zpool create` for a
mixed topology.

Capacity is the sum of the vdev capacities. Redundancy is per vdev: losing more
disks than one vdev can tolerate takes down the whole pool.

### SLOG, special, cache and spares

```bash
pg zfs pool create tank /dev/vdb /dev/vdc \
  --type mirror \
  --special /dev/vdd,/dev/vde \
  --log /dev/vdf,/dev/vdg \
  --cache /dev/vdh \
  --spare /dev/vdi
```

`--special` follows `--type` (a mirror here). `--log` with two or more devices is
a mirror; a single device is a stripe and is warned about. `--cache` and
`--spare` take independent device lists.

A device may appear only once across data, special, log, cache and spare.

Add a spare to an existing pool with `pool add`:

```bash
pg zfs pool add tank --class spare /dev/vdi
```

### Default properties

Unless `--no-defaults` is given:

| Object | Property | Value |
|---|---|---|
| pool | `ashift` | `12` |
| pool | `autoexpand` | `on` |
| pool | `autotrim` | `on` when every data disk is non-rotational and reports discard support |
| root dataset | `compression` | `zstd` |
| root dataset | `atime` | `off` |
| root dataset | `xattr` | `sa` |
| root dataset | `acltype` | `posixacl` |
| root dataset | `dnodesize` | `auto` |

Override pool properties with `--prop/-p` (maps to `zpool -o`) and root dataset
properties with `--fs-prop/-P` (maps to `zpool -O`):

```bash
pg zfs pool create tank /dev/vdb /dev/vdc \
  --prop ashift=13 --fs-prop compression=lz4 --fs-prop atime=on
```

`-o` is pgcli's global output flag, which is why the pool/dataset properties use
`-p`/`-P`. Inside `pg zfs raw` the native flags work normally.

## Managing pools

```bash
pg zfs pool list
pg zfs pool status tank
pg zfs pool status --brief          # only pools with problems
pg zfs pool scrub tank
pg zfs pool scrub tank --stop
```

Growth and repair:

```bash
# Add another mirror vdev
pg zfs pool add tank /dev/vdd /dev/vde --type mirror

# Attach /dev/vdd to the mirror holding /dev/vdc (2-way to 3-way)
pg zfs pool attach tank /dev/vdc /dev/vdd

# Shrink it back
pg zfs pool detach tank /dev/vdd

# Replace the failing /dev/vdc with the new /dev/vde
pg zfs pool replace tank /dev/vdc /dev/vde

# Temporarily take a disk out
pg zfs pool offline tank /dev/vdc
pg zfs pool online tank /dev/vdc
```

`attach` targets mirror vdevs. Adding disks to a raidz vdev (raidz expansion)
requires OpenZFS 2.3 or newer; Debian 12 (2.1) and Ubuntu 24.04 (2.2) cannot do
it. Use `pg zfs raw zpool attach` on a recent release if you need it.

### Unmounting and destroying a pool

`pg zfs pool export` unmounts every dataset and detaches the pool from the
host, but keeps all data on the member disks:

```bash
pg zfs pool export tank
```

An exported pool is not imported at boot. Attach it again by listing the
importable pools and importing it:

```bash
pg zfs pool import                  # list importable pools
pg zfs pool import tank
pg zfs pool import tank --dir /dev/disk/by-id --no-mount
```

`pg zfs pool destroy` deletes the pool, every dataset and all data:

```bash
pg zfs pool destroy tank
```

`destroy` only works on an imported pool. If the pool was exported, import it
first:

```bash
pg zfs pool import tank
pg zfs pool destroy tank
```

Without `--force` both commands ask for confirmation; EOF and the default
answer mean "no". Non-interactive automation must pass `--force`. `--force`
only skips the prompt — it is never passed to ZFS as `-f` and does not bypass
safety checks. Export is reversible; destroy is not.

## Persistence and boot

Pools and datasets survive reboots without any extra steps:

- `pg zfs install` enables `zfs-load-module`, `zfs-import-cache`, `zfs-mount`
  and `zfs-zed` (when the unit exists).
- `zpool create` records the pool in `/etc/zfs/zpool.cache`; on boot
  `zfs-import-cache` imports it.
- ZFS does not use `/etc/fstab`. Each dataset's `mountpoint` is a ZFS property,
  and `zfs-mount` mounts them at boot. A pool's root dataset mounts at
  `/<pool>`, and child datasets mount under it (for example `/fast/data`).
- The pool state lives on the member disks, so data survives a crash or power
  loss even if the host did not shut down cleanly.

Verify the setup:

```bash
systemctl is-enabled zfs-load-module zfs-import-cache zfs-mount zfs-zed
pg zfs raw zpool get cachefile fast
pg zfs raw zfs get mountpoint fast
```

After a reboot `pg zfs pool list` should list the pool and `findmnt /fast`
should show it mounted.

On real hardware, create pools from stable paths such as `/dev/disk/by-id/...`.
`/dev/sdX` names can change across boots, and a pool whose devices are missing
will not import. Run `pg zfs pool export tank` before moving disks to another
host; the destination can then adopt them with `pg zfs pool import tank`.

## Datasets and snapshots

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

A dataset with children requires `--recursive` to destroy. Rollback discards all
changes made after the snapshot.

### Rollback

`pg zfs snapshot rollback tank/data@snap1` returns the dataset to the state
captured by the snapshot; every change made after it is discarded. If newer
snapshots exist, ZFS refuses the rollback unless `--recursive` is given, which
deletes those newer snapshots. Clones that depend on the snapshot also block the
rollback — remove them first, or use `pg zfs raw zfs rollback -R` (which
destroys the dependent clones). Non-interactive runs need `--force` to skip the
confirmation prompt.

```bash
# Snapshot before an upgrade
pg zfs snapshot create tank/data@before-upgrade

# Roll back (asks for confirmation)
pg zfs snapshot rollback tank/data@before-upgrade

# Non-interactive automation
pg zfs snapshot rollback tank/data@before-upgrade --force

# Newer snapshots exist: --recursive also destroys them
pg zfs snapshot rollback tank/data@before-upgrade --recursive --force

# Clones block the rollback: -R also destroys dependent clones
pg zfs raw zfs rollback -R tank/data@before-upgrade
```

### Clone

`pg zfs dataset clone tank/data@snap1 tank/clone` creates a new, writable
dataset from the snapshot. Unchanged blocks are shared copy-on-write, so the
clone does not duplicate the full data. Pass `--prop` to set properties on the
new dataset:

```bash
# Create a writable copy of a snapshot
pg zfs dataset clone tank/data@snap1 tank/clone

# Set properties on the clone
pg zfs dataset clone tank/data@snap1 tank/clone \
  --prop compression=off --prop mountpoint=/tank/clone

# Detach the clone; the original snapshot can then be destroyed
pg zfs raw zfs promote tank/clone
pg zfs snapshot destroy tank/data@snap1
pg zfs dataset destroy tank/clone --force
```

While a clone depends on a snapshot, the snapshot cannot be destroyed. Promote
the clone (`pg zfs raw zfs promote tank/clone`) to make it independent of the
original dataset before removing the old snapshot.

## Raw passthrough

```bash
pg zfs raw zpool status -v
pg zfs raw zpool set comment="backup pool" tank
pg zfs raw zfs create -o compression=off tank/scratch
pg zfs raw zpool create -f -O compression=gzip temp /dev/vdb
```

`raw` bypasses every safety check and confirmation. It is the escape hatch for
options pgcli does not wrap — treat it exactly like running `zpool`/`zfs` as
root. Binary pipelines work too, for example:

```bash
pg zfs snapshot create tank/data@send
pg zfs raw zfs send tank/data@send | pg zfs raw zfs receive tank/recv
```

## Permissions

Every command that invokes `zpool` or `zfs` first checks privileges:

- root: runs directly;
- non-root with working `sudo -n`: all ZFS commands run through `sudo -n`;
- otherwise: fails immediately with

  ```
  ZFS management requires root: run "sudo pg zfs ..." or configure passwordless sudo for this user
  ```

pgcli never prompts for a sudo password. `pg zfs status` and `pg zfs disk list`
still show static information without privileges, marking pool details as
unavailable.

## Command reference

| Command | Purpose |
|---|---|
| `pg zfs install` | Install ZFS (zero-argument, idempotent) |
| `pg zfs status` | Distribution, package, module and pool overview |
| `pg zfs disk list [--all]` | Whole disks, mounts, ZFS membership, safety |
| `pg zfs pool create NAME DEVICE...` | Create a pool |
| `pg zfs pool list` | List pools |
| `pg zfs pool status [NAME] [--brief]` | Detailed status |
| `pg zfs pool add NAME DEVICE...` | Add vdevs |
| `pg zfs pool attach/detach NAME DEVICE [NEW]` | Grow/shrink a mirror |
| `pg zfs pool replace NAME OLD NEW` | Replace a disk |
| `pg zfs pool offline/online NAME DEVICE` | Take a disk out/return it |
| `pg zfs pool export/import [NAME]` | Move a pool between systems |
| `pg zfs pool scrub NAME [--stop]` | Verify data integrity |
| `pg zfs pool destroy NAME` | Destroy a pool and all data |
| `pg zfs dataset create/list/set/clone/destroy` | Dataset lifecycle |
| `pg zfs snapshot create/list/rollback/destroy` | Snapshot lifecycle |
| `pg zfs raw zpool/zfs ARGS...` | Unwrapped passthrough |

## Troubleshooting

**`pg zfs install` cannot find packages.** Check that the archive repository is
reachable and that `contrib`/`universe` is enabled. The installer edits only
distribution archive sources (never third-party repositories) and leaves a
`*.pgcli.bak` next to each modified file.

**DKMS build fails.** Confirm headers for the running kernel are installed
(`linux-headers-$(uname -r)` on Debian/Ubuntu, `kernel-devel-$(uname -r)` on
Fedora/Rocky). The installer reports the build log location; it never reboots or
changes the kernel. On very small VMs, add swap before installing.

**Secure Boot is enabled.** DKMS modules must be signed. pgcli reports the
condition but does not enroll keys; sign the module with your MOK key or disable
Secure Boot for the test host.

**Module is not loaded.** `modprobe zfs` then `pg zfs status`. The installer
enables `zfs-load-module`, `zfs-import-cache`, `zfs-mount` and `zfs-zed` so pools
import automatically on boot.

**A disk is rejected because it has partitions or an old label.** If it really
is free, clear the leftovers:

```bash
pg zfs raw zpool labelclear -f /dev/vdb
wipefs -a /dev/vdb
```

On OpenZFS 2.2 and newer, clearing the whole disk can fail with
`failed to clear label for /dev/vdb` when the label lives in the ZFS member
partition. In that case clear the partition instead, then wipe the table:

```bash
pg zfs raw zpool labelclear -f /dev/vdb1
wipefs -a /dev/vdb
```

Never do this to a disk that belongs to an imported pool — `pg zfs disk list`
shows the owning pool, and the safety check refuses such disks.

**SELinux (Fedora/Rocky).** ZFS mountpoints use standard file contexts. If a
service cannot access a dataset, check `ausearch`/`journalctl` for AVC denials
instead of disabling SELinux globally.

**Pools do not come back after reboot.** `systemctl status zfs-import-cache`
and `zpool import` as root. Pools created from `/dev/sdX` names follow device
renames; use stable paths (`/dev/disk/by-id/...`) when creating pools on real
hardware.
