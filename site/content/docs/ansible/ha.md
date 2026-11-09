---
title: "HA clusters"
description: "Configure the ha_etcd and ha_cluster example files — shared etcd DCS, Patroni clusters, HAProxy, and state-driven destroy"
weight: 50
---

The HA deployment spans two inventory groups and two example variable files:

| Group | File | Role |
|-------|------|------|
| `[ha_etcd]` | `group_vars/ha_etcd/main.yml` | Shared etcd DCS, one member per host |
| `[ha_cluster]` | `group_vars/ha_cluster/main.yml` | Patroni members, per-namespace clusters, HAProxy |

The same hosts may appear in both groups (co-located layout). List the etcd
bootstrap host first; the add-on play is serial.

## etcd: `group_vars/ha_etcd/main.yml.example`

The file is group-name agnostic: set `pg_etcd_group` once and everything below
follows it.

```yaml
pg_etcd_group: ha_etcd

_ha_etcd_advertise_host: "{{ private_ip | default(ansible_host, true) }}"
_ha_etcd_client_endpoint: "{{ _ha_etcd_advertise_host }}:2379"
_ha_etcd_bootstrap_endpoint: "http://{{ hostvars[groups[pg_etcd_group] | first].private_ip | default(...) }}:2379"

pg_etcd_state: present   # present (default) | absent

pgcli_addons:
  - type: etcd
    name: "etcd-{{ groups[pg_etcd_group].index(inventory_hostname) + 1 }}"
    state: "{{ pg_etcd_state }}"
    autostart: true
    args: "{{ ['--cluster', 'ha', '--advertise-host', _ha_etcd_advertise_host]
             + (['--join', _ha_etcd_bootstrap_endpoint]
                if inventory_hostname != (groups[pg_etcd_group] | first) else []) }}"
```

`_ha_etcd_client_endpoint` is consumed by `ha_cluster` to build the Patroni DCS
endpoint list. `pg_etcd_state: absent` destroys the group (remove the Patroni
clusters first).

## Patroni: `group_vars/ha_cluster/main.yml.example`

The host default config gets a namespace, and extra namespaces are declared
with their own directories and port ranges:

```yaml
pgcli_config_overrides:
  namespace: ha

pgcli_namespaces:
  - name: ha
    path: /root/.pgcli
  - name: ha2
    path: /root/.pgcli/ha2
    base_dir: /data/pg-ha2
    backup_dir: /mnt/backup/pg-ha2
    pg_start_port: 36432
    pg_ssh_port: 43201
```

Which etcd group supplies the DCS, and the derived endpoint list:

```yaml
pg_ha_etcd_group: ha_etcd
_ha_dcs_endpoints: >-
  {{ groups[pg_ha_etcd_group]
     | map('extract', hostvars, '_ha_etcd_client_endpoint') | list }}
```

Shared S3 backup repository (secret key lives in `secrets.yaml`):

```yaml
pgcli_backup_s3_host: s3-01
pgcli_backup_s3_port: 19800
pgcli_backup_s3_bucket: pgbackup
pgcli_backup_s3_access_key: admin
```

One cluster entry per namespace:

```yaml
pg_ha_clusters:
  - namespace: ha
    scope: pgcluster
    members: [ha1, ha2, ha3]
    host_port: 35532
    restapi_port: 8008
    dcs_endpoints: "{{ _ha_dcs_endpoints }}"
    state: started          # started | stopped | absent
    autostart: true
    extensions:
      - pg_stat_statements
```

| Field | Meaning |
|-------|---------|
| `namespace` | Namespace from `pgcli_namespaces` |
| `scope` | Patroni scope; the namespace suffix keeps clusters apart in DCS |
| `members` | One name per `[ha_cluster]` host, in inventory order |
| `host_port`, `restapi_port` | PostgreSQL and Patroni REST ports; disjoint per cluster |
| `dcs_endpoints` | etcd client endpoints |
| `state` | `started` (default), `stopped`, `absent` |
| `clean_data` | With `absent`: delete member data dirs |
| `autostart` | Enable boot autostart for the members |
| `extensions` | Extensions installed cluster-wide |

Port ranges, data dirs and backup dirs must not overlap between namespaces. The
same `scope` name in two namespaces is safe: pgcli bakes the namespace suffix
into the DCS scope and container names.

### HAProxy

```yaml
pg_ha_haproxy_host: "{{ groups['ha_cluster'] | first }}"
pg_ha_haproxy:
  enabled: true
  mode: split          # rw/ro listeners
  max_lag: 1MB
  listen: 0.0.0.0
  clusters:
    - namespace: ha
      name: lb-ha
      rw_port: 5000
      ro_port: 5001
      stats_port: 5002
      state: present    # present (default) | absent
```

The proxy targets every `[ha_cluster]` member via `--node`; run it on a host
that carries the namespace config. Both instances share one host network, so
their ports must differ.

## Run it

```bash
# etcd + Patroni + HAProxy in one pass
uv run ansible-playbook playbooks/apply.yaml --tags addon,ha

# Reconcile the clusters and proxies only (etcd must already exist)
uv run ansible-playbook playbooks/apply.yaml --tags ha

# HAProxy only
uv run ansible-playbook playbooks/apply.yaml --tags haproxy

# Read-only topology and proxy status
uv run ansible-playbook playbooks/ha-status.yaml
```

## Destroy order

The state switches are independent; destroy in dependency order:

1. HAProxy: set `pg_ha_haproxy.clusters[].state: absent`, run `--tags haproxy`.
2. Patroni clusters: set `pg_ha_clusters[].state: absent` (add `clean_data:
   true` to delete member data dirs), run `--tags ha`.
3. etcd: set `pg_etcd_state: absent`, run `--tags addon`.

The HA plays clear the DCS scope on the first host and remove each host's local
members serially; later hosts are a no-op for the scope. `clean_data` deletes
the member data dirs but leaves the empty `addon/patroni/<scope>` directory.
etcd removal always deletes its data dir. Backup containers, the pgBackRest
configuration and S3 backups are left intact by all of the above.
