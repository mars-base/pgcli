---
title: "Instances and replicas"
description: "Configure the single, samehost and crosshost example files — every pgcli_instances field, namespaces and state semantics"
weight: 30
---

PostgreSQL instances are declared in `pgcli_instances`, normally in one
deployment-mode group's `group_vars/<mode>/main.yml`. `apply.yaml --tags
instance` (run with `serial: 1`) reconciles each host's entries whose `host:`
matches that host.

## The three mode examples

Each example file is small; copy the one matching the mode you exercise.

### `group_vars/single/main.yml.example`

One stand-alone primary:

```yaml
pgcli_config_overrides:
  namespace: dev

pgcli_instances:
  - name: pg_single
    host: db-01
    state: started
    autostart: true
```

### `group_vars/samehost/main.yml.example`

Primary and a read-only replica on the same host:

```yaml
pgcli_instances:
  - name: pg01
    host: db-01
    state: started
    autostart: true

  - name: pg01_ro1
    host: db-01
    replica_of: pg01
    state: started
    autostart: true
```

A same-host replica inherits its primary's namespace; give it the same
`namespace` only if you set one explicitly.

### `group_vars/crosshost/main.yml.example`

The replica lives on another host and names both the primary instance and its
host:

```yaml
pgcli_instances:
  - name: pg01
    host: db-01
    namespace: dev
    state: started
    autostart: true

  - name: pg01_ro1
    host: db-02
    replica_of: pg01
    replica_of_host: db-01
    namespace: dev
    state: started
    autostart: true
```

The primary's host must come first in `[crosshost]`: the play is serial, and
the replica needs its primary running. The role prepares the primary side
(replication slot + `pg_hba`) before creating the replica with `--primary-dsn`,
reading the primary password from that host's `pg.yaml`.

## `pgcli_instances` fields

| Field | Applies to | Meaning |
|-------|------------|---------|
| `name` | all | Required instance name |
| `host` | all | Inventory host that owns the instance |
| `namespace` | all | Name from `pgcli_namespaces`; omit for the host default |
| `state` | all | `present`, `started` (default), `stopped`, `absent` |
| `autostart` | all | Enable the instance at boot (`pg autostart enable`) |
| `backup_target` | primary | `local` (default) or `s3`; fixed at creation time |
| `password_length` | primary | Generated admin password length (default 16) |
| `extensions` | primary | Extension list; setting it replaces the default list |
| `clean_data` | all | Only with `state: absent`; also deletes data/WAL dirs and the stanza |
| `replica_of` | replica | Name of the primary |
| `replica_of_host` | replica | Inventory host of a cross-host primary |

Replica entries accept only `name`, `replica_of`, `replica_of_host`,
`namespace`, `state`, `autostart` and `clean_data`. Password, database, image,
extension and PITR settings are inherited from the primary.

Defaults come from `pgcli_instance_defaults` in `group_vars/all/main.yml`:

```yaml
pgcli_instance_defaults:
  state: started
  autostart: false
  backup_target: local
  password_length: 16
  extensions: []
  clean_data: false
```

## State semantics

- `present` creates the instance without changing its run state.
- `started` creates (if needed) and starts it.
- `stopped` creates (if needed) and stops it.
- `absent` removes the container and the config entry. The data and WAL
  directories and the pgBackRest stanza are kept unless `clean_data: true`.
  A full destroy therefore needs **both**: `state: absent` and
  `clean_data: true`; `clean_data` alone does nothing.

## Namespaces

`pgcli_namespaces` maps a selector name to a directory holding `pg.yaml`:

```yaml
pgcli_namespaces:
  - name: dev
    path: /root/.pgcli
  - name: prod
    path: /root/.pgcli/prod
```

Instances select one with `namespace: <name>`; omitted means the host default
(`pgcli_namespace_path` / `pgcli_config_path`). Extra entries with their own
`base_dir` are initialized automatically by the `pgcli` role; entries without
`base_dir` need a manual `pg config init -o`.

## Run it

```bash
# Full mode (installs pgcli if needed)
uv run ansible-playbook playbooks/apply.yaml --limit samehost

# Instances only on an already prepared host
uv run ansible-playbook playbooks/apply.yaml --limit crosshost --tags instance
```

Re-running is idempotent; instances already in the desired state report `ok`.
