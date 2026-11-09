---
title: "Add-ons"
description: "Configure pgcli_addons — install and remove top-level or instance-scoped add-ons, and manage the shared etcd group"
weight: 40
---

Add-ons are declared in `pgcli_addons` (empty by default in
`group_vars/all/main.yml`). A deployment-mode group adds its own list; the
`ha_etcd` group uses the same mechanism for the shared etcd DCS.

## Entry fields

| Field | Meaning |
|-------|---------|
| `type` | Add-on type, e.g. `etcd`, `haproxy`, `pgbouncer`, `minio` |
| `name` | Top-level add-on instance name (e.g. this etcd member) |
| `instance` | Instead of `name`: scope the add-on to a pgcli instance |
| `state` | `present` (default) or `absent` |
| `args` | List of flags appended verbatim to `pg addon install` |
| `clean_data` | With `state: absent`, also delete data where the add-on supports it |
| `force` | With `state: present`, recreate with `pg addon install --force` |
| `autostart` | `true` enables boot autostart, `false` (default) disables it |

Every entry needs `type` and exactly one of `name` or `instance`; the role
asserts this before doing anything.

## Top-level add-on example

`group_vars/ha_etcd/main.yml.example` builds one etcd member per host:

```yaml
pg_etcd_state: present

pgcli_addons:
  - type: etcd
    name: "etcd-{{ groups[pg_etcd_group].index(inventory_hostname) + 1 }}"
    state: "{{ pg_etcd_state }}"
    autostart: true
    args: "{{ ['--cluster', 'ha', '--advertise-host', _ha_etcd_advertise_host]
             + (['--join', _ha_etcd_bootstrap_endpoint]
                if inventory_hostname != (groups[pg_etcd_group] | first) else []) }}"
```

The first host in the group bootstraps the cluster; the rest pass `--join` to
a running member. The add-on play runs with `serial: 1`, so the bootstrap host
is always ready before the next host joins.

## Instance-scoped example

Use `instance` instead of `name` for add-ons that belong to a PostgreSQL
instance:

```yaml
pgcli_addons:
  - type: pgbouncer
    instance: pg01
    state: present
```

The role runs `pg addon install pgbouncer -i pg01`, so the instance must exist
in the same `pgcli_instances` reconcile (the add-on play runs after the
instance play in `apply.yaml`).

## Multiple add-ons

`pgcli_addons` is a list; each item is reconciled in order:

```yaml
pgcli_addons:
  - type: etcd
    name: etcd-1
    args: [...]
  - type: semaphore
    name: ui
    state: absent
```

## Install and remove

```bash
# Reconcile add-ons on every host that declares pgcli_addons
uv run ansible-playbook playbooks/apply.yaml --tags addon

# Only the etcd group
uv run ansible-playbook playbooks/apply.yaml --tags addon --limit ha_etcd
```

- `state: present` installs only when the name/instance is missing from the
  config, unless `force: true` is set. It also reconciles `autostart` with
  `pg autostart enable|disable --<type> ...`; leaving it unset disables boot
  autostart for the add-on.
- `state: absent` calls `pg addon remove <type> --name <name>` (or `-i
  <instance>`); add `clean_data: true` for the add-ons that keep data by
  default. The etcd remove path always deletes its data directory.
- A host with `pgcli_addons: []` is a no-op, so the play is safe to run with
  `--tags addon` on the whole inventory.

## etcd lifecycle

`pg_etcd_state` is the group-level switch:

```yaml
pg_etcd_state: present   # create/reconcile the local member (default)
# pg_etcd_state: absent  # remove the local member
```

Set it to `absent` only **after** the Patroni clusters are gone: they use this
etcd as their DCS. With `serial: 1`, members are removed one host at a time;
etcd removal deletes the data directory as part of the command. To rebuild,
set it back to `present` and re-run `--tags addon` — the first host
bootstraps, the rest rejoin.
