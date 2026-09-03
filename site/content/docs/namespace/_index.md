---
title: "Namespace Isolation"
description: "Create isolated environments on a single host using namespaces"
weight: 15
icon: fa-solid fa-layer-group
menus:
  main:
    identifier: docs-namespace
    parent: docs
    weight: 15
    params:
      icon: fa-solid fa-layer-group
cascade:
  type: docs
  footer_style: slim
---

Namespaces allow you to create completely isolated environments on a single host, perfect for separating production, development, and testing environments without container name conflicts or port collisions.

## What is a Namespace?

A namespace is a prefix applied to all container names within a configuration file. This isolation mechanism ensures that:

- **Container names don't clash**: Each namespace gets its own container prefix
- **Port ranges are separate**: Each config file allocates ports from its own range
- **Backup containers are isolated**: Each namespace has its own pgBackRest container
- **Configuration files are independent**: Each environment uses a separate config file

## Use Case: Production and Development Environments

A common scenario is running production and development environments on the same server:

```bash
# Create production environment
pg config init \
  --namespace prod \
  --pg-start-port 35432 \
  --pg-ssh-port 42201 \
  --add app-db \
  -o ~/.pgcli-prod/pg.yaml

# Create development environment
pg config init \
  --namespace dev \
  --pg-start-port 38000 \
  --pg-ssh-port 43000 \
  --add app-db \
  -o ~/.pgcli-dev/pg.yaml
```

This creates two completely isolated environments:

| Environment | Config File | Container Prefix | PG Port Range | SSH Port Range |
|-------------|-------------|------------------|---------------|----------------|
| Production | `~/.pgcli-prod/pg.yaml` | `pgcli-pg-prod-*` | 35432+ | 42201+ |
| Development | `~/.pgcli-dev/pg.yaml` | `pgcli-pg-dev-*` | 38000+ | 43000+ |

## Managing Multiple Environments

Use the `-c` flag to specify which configuration file to use:

```bash
# Start production database
pg -c ~/.pgcli-prod/pg.yaml start -i app-db

# Start development database
pg -c ~/.pgcli-dev/pg.yaml start -i app-db

# List instances in production
pg -c ~/.pgcli-prod/pg.yaml list

# List instances in development
pg -c ~/.pgcli-dev/pg.yaml list
```

## How It Works

### Container Naming

With namespace `prod` and instance `app-db`:
- Instance container: `pgcli-pg-prod-app-db`
- Backup container: `pgcli-backup-prod`
- Network: `pgcli-net-prod` (if using separate networks)

Without namespace (or `--namespace ""`):
- Instance container: `pgcli-pg-default-app-db`
- Backup container: `pgcli-backup-default`

### Port Allocation

Each instance in a config file gets sequential ports:
- First instance: `pg_start_port` (e.g., 35432)
- Second instance: `pg_start_port + 1` (e.g., 35433)
- And so on...

Same for SSH ports used by pgBackRest.

### Configuration Persistence

The namespace and port ranges are saved in the config file:

```yaml
namespace: prod
pg_start_port: 35432
pg_ssh_port: 42201
```

## Best Practices

### 1. Always Use Explicit Namespaces

Never rely on the default namespace when running multiple configs on one host:

```bash
# Bad: both configs would use "default" namespace and clash
pg config init --add app -o ~/.pgcli-prod/pg.yaml
pg config init --add app -o ~/.pgcli-dev/pg.yaml  # Conflict!

# Good: explicit namespaces
pg config init --namespace prod --add app -o ~/.pgcli-prod/pg.yaml
pg config init --namespace dev --add app -o ~/.pgcli-dev/pg.yaml
```

### 2. Use Disjoint Port Ranges

Ensure port ranges don't overlap between configs:

```bash
# Production: 35432-35999, 42201-42999
pg config init --namespace prod \
  --pg-start-port 35432 \
  --pg-ssh-port 42201 \
  --add app -o ~/.pgcli-prod/pg.yaml

# Development: 38000-38999, 43000-43999
pg config init --namespace dev \
  --pg-start-port 38000 \
  --pg-ssh-port 43000 \
  --add app -o ~/.pgcli-dev/pg.yaml

# Testing: 40000-40999, 44000-44999
pg config init --namespace test \
  --pg-start-port 40000 \
  --pg-ssh-port 44000 \
  --add app -o ~/.pgcli-test/pg.yaml
```

Leave headroom for multiple instances within each environment.

### 3. Namespace is Baked into Container Names

The namespace is embedded in container names at creation time. Changing it later breaks the link:

```bash
# Create with namespace "prod"
pg -c ~/.pgcli-prod/pg.yaml start -i app-db
# Container: pgcli-pg-prod-app-db

# Edit config to change namespace to "production"
# This WON'T work - container name mismatch!

# Instead: destroy and recreate
pg -c ~/.pgcli-prod/pg.yaml destroy -i app-db --clean-data
pg -c ~/.pgcli-prod/pg.yaml create -i app-db
pg -c ~/.pgcli-prod/pg.yaml start -i app-db
```

### 4. Use Shell Aliases for Convenience

Create aliases to avoid typing `-c` repeatedly:

```bash
# Add to ~/.bashrc or ~/.zshrc
alias pg-prod='pg -c ~/.pgcli-prod/pg.yaml'
alias pg-dev='pg -c ~/.pgcli-dev/pg.yaml'
alias pg-test='pg -c ~/.pgcli-test/pg.yaml'

# Now use:
pg-prod start -i app-db
pg-dev list
pg-test destroy -i test-db --force
```

## Advanced: Multiple Environments with Replicas

You can even set up isolated replication environments:

```bash
# Production: primary + replica
pg -c ~/.pgcli-prod/pg.yaml create -i primary --base-dir /data/prod
pg -c ~/.pgcli-prod/pg.yaml replica create replica -i primary

# Development: separate primary + replica
pg -c ~/.pgcli-dev/pg.yaml create -i primary --base-dir /data/dev
pg -c ~/.pgcli-dev/pg.yaml replica create replica -i primary
```

Each environment maintains its own replication slots, backup stanzas, and data directories.

## Troubleshooting

### Container Name Conflicts

```
Error: container "pgcli-pg-default-app-db" already exists
```

**Cause**: Two configs using the same namespace.

**Solution**: Use different namespaces or destroy the conflicting instance first.

### Port Already in Use

```
Error: listen tcp 0.0.0.0:35432: bind: address already in use
```

**Cause**: Port ranges overlap between configs.

**Solution**: Use disjoint port ranges with sufficient spacing.

### Instance Not Found After Namespace Change

```
Error: instance "app-db" exists in config but container not found
```

**Cause**: Changed namespace in config file after creating instances.

**Solution**: Either destroy and recreate instances, or revert the namespace change.

## Summary

Namespaces provide complete isolation for multiple environments on a single host:

- **Separate configs**: Each environment gets its own `pg.yaml`
- **Distinct namespaces**: Prevents container name collisions
- **Disjoint ports**: Avoids port conflicts
- **Independent operation**: Each environment managed separately with `-c`

Perfect for running production, development, testing, and staging environments on the same server without interference.
