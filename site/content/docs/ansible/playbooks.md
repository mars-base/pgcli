---
title: "Playbooks"
description: "How to run apply.yaml, ping.yaml, resources.yaml, deploy-s3.yaml, ha-status.yaml and passwords.yaml — play order, tags, limits, idempotency and lifecycle orders"
weight: 20
---

All commands run from the `ansible/` directory through the pinned `uv`
environment. Without `--limit`, `apply.yaml` targets every host in the
inventory; in practice you select a deployment-mode group or one add-on group
with `--limit addon_<type>_<single|cluster>`.

```bash
cd ansible
uv run ansible-playbook playbooks/apply.yaml
```

## `ping.yaml`

SSH and Python connectivity smoke test for every inventory host:

```bash
uv run ansible-playbook playbooks/ping.yaml
uv run ansible-playbook playbooks/ping.yaml --limit crosshost
```

## `apply.yaml`

The generic play. It does not contain mode-specific data: it reconciles
whatever the selected hosts declare in `group_vars/`.

| # | Play | Hosts | Serial | Tag |
|---|------|-------|--------|-----|
| 1 | Validate the add-on selection | `all` | – | `always` |
| 2 | Install and configure pgcli | `all` | – | `pgcli` |
| 3 | PostgreSQL instances | `all` | 1 | `instance` |
| 4 | Add-ons | `all` | 1 | `addon` |
| 5 | Assemble the Redis cluster | `all` | – | `addon` |
| 6 | Patroni HA members | `ha_cluster` | 1 | `ha` |
| 7 | Patroni HA backup convergence | `ha_cluster` | – | `ha` |
| 8 | Patroni HA proxy | proxy host | – | `ha`, `haproxy` |

`serial: 1` is deliberate: the first host installs the add-ons or bootstraps
etcd, then the next host joins; cross-host replicas need their primary to
exist first.

Add-ons are selected with `--limit addon_<type>_<single|cluster>`;
`group_vars/all` reads that group's `addon_<kind>_addons` list into
`pgcli_addons`. A limit that starts with `addon_` but is not one exact
existing group with a mode suffix fails the validation play, and a group name
that does not exist fails while Ansible matches the limit. Without such a
limit the list is empty and the add-on play is a no-op.

The Redis assembly play runs after all members exist (the add-on play is
serial). It only acts when `--limit addon_redis_cluster` selects present
entries: it waits for every member, creates the cluster once if needed, and
waits until all 16384 slots are assigned. Reruns skip the create step.

```bash
# Full stack for one deployment mode
uv run ansible-playbook playbooks/apply.yaml --limit crosshost

# Only reconcile instances (pgcli must already be installed/configured)
uv run ansible-playbook playbooks/apply.yaml --limit single --tags instance

# Fresh host: install and create in one pass
uv run ansible-playbook playbooks/apply.yaml --limit single --tags pgcli,instance

# Add-ons only, one group (set its entries to absent to remove them)
uv run ansible-playbook playbooks/apply.yaml --limit addon_etcd_cluster --tags addon

# HA members + converge; HAProxy has its own tag
uv run ansible-playbook playbooks/apply.yaml --tags ha
uv run ansible-playbook playbooks/apply.yaml --tags haproxy

# Single host
uv run ansible-playbook playbooks/apply.yaml --limit db-01 --tags instance
```

Tags do not pull in their prerequisites. A first HA deployment needs the etcd
add-on play (`pgcli,addon,ha` or a full run) before `--tags ha` can succeed.
The HA plays only run on hosts present in `[ha_cluster]`; the add-on play
reconciles etcd on `[ha_etcd]` hosts.

## `deploy-s3.yaml`

Deploys a standalone S3-compatible store (`minio`, `silo` or `rustfs`) used as
the pgBackRest `s3` backup target. The store type comes from the active
store-type group under `[s3]`; `pgcli_s3_type` is optional and must agree.

```bash
# Whole active store group
uv run ansible-playbook playbooks/deploy-s3.yaml

# Pick the store type explicitly
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_group=minio_s3

# Deploy only a subset of nodes (single node via a one-host pattern)
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_nodes=db-01
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_nodes=db-01,db-02

# Force container recreation
uv run ansible-playbook playbooks/deploy-s3.yaml -e pgcli_s3_force=true
```

Layout (single/multi-node, drives) and TLS options are on the
[S3 storage](./s3/) page.

## `ha-status.yaml`

Read-only. Prints the live Patroni topology (role, state, LSN, lag) and the
HAProxy instances in front of each cluster. It runs once on the first selected
host; any member can read the shared DCS view.

```bash
uv run ansible-playbook playbooks/ha-status.yaml
uv run ansible-playbook playbooks/ha-status.yaml -l ha_cluster
uv run ansible-playbook playbooks/ha-status.yaml -l db-02
```

## `resources.yaml`

Read-only. Reports CPU, load, memory, swap, filesystems and the top memory
processes, plus the pgcli version and, for every namespace in
`pgcli_namespaces`, its instances, add-ons and Patroni status. Hosts without
the pgcli binary at `pgcli_install_path` are skipped with a short message.

```bash
uv run ansible-playbook playbooks/resources.yaml
uv run ansible-playbook playbooks/resources.yaml -e HOSTS=db-01
uv run ansible-playbook playbooks/resources.yaml -l addon_etcd_single
```

## `passwords.yaml`

Read-only. Prints the stored credentials for the selected hosts:

- the admin DSN of each instance, read from `pg status -i`;
  `127.0.0.1`/`localhost` in the DSN is rewritten to the host's `private_ip`
  (falling back to `ansible_host`) so the DSN can be used from another machine;
- each Patroni cluster's stored password set (`pg ha passwords <scope>`);
- the add-on credentials (`pg addon list --show-password`).

Every namespace in `pgcli_namespaces` is reported separately, using that
namespace's own config.

The output is plaintext credentials, so the playbook **refuses to run unless
Ansible logging is disabled**. Pass `ANSIBLE_LOG_PATH=/dev/null` explicitly:

```bash
ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml
ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml -e HOSTS=db-01
ANSIBLE_LOG_PATH=/dev/null uv run ansible-playbook playbooks/passwords.yaml -e HOSTS=ha_cluster
```

Do not redirect its stdout to a world-readable file; write any copy to a
`0600` file or a password manager.

## Idempotency

Re-running any playbook is safe. Every state-changing task checks the current
pgcli config and container state, so an unchanged host reports `ok`. The
exception is `pgcli_install_force: true`, which re-runs the installer on every
pass by design.

## Lifecycle switches

| Resource | Declaration | Default | `absent` effect |
|----------|-------------|---------|-----------------|
| Instance | `pgcli_instances[].state` | `started` | Removes container + config; with `clean_data: true` also deletes data/WAL dirs and the pgBackRest stanza |
| Add-on | `pgcli_addons[].state` | `present` | Removes the add-on container + config entry; `clean_data` applies where the add-on supports it |
| HA cluster | `pg_ha_clusters[].state` | `started` | Removes local members and clears the DCS scope; `clean_data: true` deletes member data dirs (empty `addon/patroni/<scope>` dir remains) |
| HAProxy | `pg_ha_haproxy.clusters[].state` | `present` | Removes the proxy instance from the proxy host and prunes its config |

Destroy order for HA: HAProxy → Patroni clusters → etcd. etcd is an add-on
entry (`pgcli_addons[].state`); its removal always deletes the data dir.
Backup containers, pgBackRest configuration and S3 backups are not removed by
the HA plays.
S3 stores are deploy-only in this playbook; remove one with
`pg addon remove <type> --name <name> --clean-data` on the node.

## Logs

`ansible.cfg` writes the run log to `var/ansible.log` and caches facts under
`var/facts/`. Both are gitignored, as are all live `main.yml` and
`secrets.yaml` files. `passwords.yaml` overrides the log with
`ANSIBLE_LOG_PATH=/dev/null` because its output contains plaintext
credentials.
