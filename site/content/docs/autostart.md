---
title: Auto-start on Boot
description: Configure pgcli to automatically start PostgreSQL instances and services after a host reboot
weight: 60
icon: fa-solid fa-power-off
---

pgcli can automatically start PostgreSQL instances, backup containers, and PgBouncer services after a host reboot. This feature uses system service managers:
- **Linux**: systemd user units
- **macOS**: launchd LaunchAgents

## How It Works

When you enable auto-start, pgcli creates a system service that runs `pg start --autostart` at boot time (or user login). The `--autostart` flag starts only the instances and services marked with `autostart: true` in your configuration.

Auto-start is **config-driven**: if you run `pg stop` on an instance, it will still be started automatically at the next boot. To prevent auto-start, use `pg autostart disable`.

## Enable Auto-start

### For a Single Instance

```bash
pg autostart enable -i <instance-name>
```

Example:
```bash
pg autostart enable -i default
```

This adds `autostart: true` to the instance configuration and creates/updates the boot service.

### For the Backup Container

```bash
pg autostart enable --backup
```

Enables auto-start for the shared pgBackRest backup container.

### For PgBouncer

Enable auto-start for a PgBouncer associated with a specific instance:
```bash
pg autostart enable --pgbouncer -i <instance-name>
```

Or for a remote PgBouncer:
```bash
pg autostart enable --pgbouncer --pg-name <remote-name>
```

## Disable Auto-start

```bash
pg autostart disable -i <instance-name>
pg autostart disable --backup
pg autostart disable --pgbouncer -i <instance-name>
pg autostart disable --pgbouncer --pg-name <remote-name>
```

When all auto-start targets are disabled, the boot service is automatically removed.

## Check Status

```bash
pg autostart status
```

Shows:
- Which instances and services have auto-start enabled
- The boot service unit name and state
- Linger status (Linux) — required for rootless podman to start services at boot time

## Platform-Specific Behavior

### Linux (systemd)

The boot service is created as a systemd user unit: `pgcli-autostart-<hash>.service`

**Important**: Rootless podman requires `loginctl enable-linger` to start containers at boot time (before the user logs in). pgcli attempts this automatically but will print a hint if it fails.

Without linger, the service starts at user login instead of at system boot.

### macOS (launchd)

The boot service is created as a LaunchAgent: `com.pgcli.autostart-<hash>.plist`

**Note**: macOS LaunchAgents run at user login, not at system boot. This is a macOS limitation — rootless podman cannot start services before the user logs in.

## Configuration File

The `autostart: true` flag appears in your pgcli configuration file:

```yaml
instances:
  default:
    # ... other settings ...
    autostart: true

backup:
  # ... other settings ...
  autostart: true

# For PgBouncer
addons:
  pgbouncer:
    default:
      # ... other settings ...
      autostart: true
```

## Boot Service Behavior

The boot service runs `pg start --autostart`, which:
1. Starts all instances with `autostart: true`
2. Starts the backup container if `backup.autostart: true`
3. Starts PgBouncer services with `autostart: true`

If no auto-start targets are configured, the service exits successfully (no error).

## Troubleshooting

### Service Failed to Start

Check the boot service logs:

**Linux (systemd)**:
```bash
journalctl --user -u pgcli-autostart-<hash>.service
```

**macOS (launchd)**:
```bash
cat ~/Library/Logs/pgcli-autostart-<hash>.log
```

### Linux: "XDG_RUNTIME_DIR is not set"

This error occurs when systemd user units are not available. Ensure you have a proper user session:
- SSH sessions should support systemd --user by default
- Terminal sessions in desktop environments support systemd --user
- Cron jobs and other non-interactive contexts do not support systemd --user

### Linux: Auto-start Only Works After Login

Run `loginctl enable-linger <your-username>` to allow rootless podman to start services at boot time.

### macOS: Auto-start Only Works After Login

This is expected behavior. macOS LaunchAgents cannot run before user login due to rootless podman constraints.

## Examples

Enable auto-start for a complete production setup:

```bash
# Enable the default instance
pg autostart enable -i default

# Enable the backup container
pg autostart enable --backup

# Enable PgBouncer for the default instance
pg autostart enable --pgbouncer -i default

# Check the status
pg autostart status
```

After the next reboot, all three services will start automatically.
