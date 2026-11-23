---
title: "HTTP File Server"
description: "Manage pg http file servers declaratively with the pg_http role and deploy-http.yaml: directories, write modes, TLS, Basic Auth, autostart, and offline binary distribution"
weight: 18
---

`playbooks/deploy-http.yaml` brings [pg http](/docs/toolbox/http/) file servers
under Ansible management. `pg http` is a host process (no container, no add-on
type), so this uses a standalone `pg_http` role and a standalone playbook
instead of `pg_addon` / `apply.yaml`, mirroring how `deploy-s3.yaml` is
organized.

The role reconciles each server declared in `pgcli_http_servers`: it compares
the running spec by name and runs `pg http stop` + `pg http start` when
anything differs, creates the served directory, optionally copies the offline
binaries into it (`sync_binaries: true`), and registers boot autostart as needed.
Binary copying is disabled by default. Re-running converges to the declared state.

## Configuration

Prepare an `[http]` group in the inventory (`hosts.ini`, gitignored), for
example:

```ini
[http]
node-http ansible_host=192.0.2.11 ansible_user=root
```

The server list lives in `group_vars/http/main.yml` (gitignored; copy it from
the `.example` file next to it). Every field is optional; defaults are shown in
parentheses:

| Field | pg http flag | Description |
|-------|--------------|-------------|
| `name` | `--name` (required) | Server name, also the state file name |
| `host` | — | Reconcile only on this inventory host; omitted means all target hosts |
| `state` | — | `started` (default) / `stopped` / `absent` |
| `autostart` | — | Register boot autostart (default `true`; only allowed with `state: started`) |
| `dir` | `--dir` | Served directory (default `/srv/offline-images`) |
| `dir_owner` | — | Directory owner (default deployment account); applies to the directory only |
| `dir_group` | — | Directory group (default deployment account); applies to the directory only |
| `dir_mode` | — | Directory permissions (default `0755`); applies to the directory only |
| `listen` | `--listen` | Listen address (default `127.0.0.1`) |
| `port` | `--port` | Listen port (default `18080`) |
| `allow_upload` | `--allow-upload` | Upload form (default `false`) |
| `allow_dir` | `--allow-dir` | Create-directory form (default `false`) |
| `allow_delete` | `--allow-delete` | Per-file delete button (default `false`) |
| `max_upload` | `--max-upload` | Per-upload limit (default `1G`, e.g. `512M`) |
| `admin_ips` | `--admin-ip` | Source-address allowlist for write modes (list, joined with commas) |
| `tls` | `--tls` | HTTPS with a self-signed certificate generated and renewed under the pgcli base dir |
| `tls_cert` | `--tls-cert` | PEM certificate path on the target host; implies TLS |
| `tls_key` | `--tls-key` | Private key path paired with `tls_cert` |
| `auth` | `--auth` | Basic Auth entries, each `USER:PASSWORD` (list) |
| `auth_env` | `--auth-env` | Names of controller environment variables holding `USER:PASSWORD` |
| `auth_file` | `--auth-file` | htpasswd files on the target host (bcrypt entries) |
| `force` | — | Restart even when the spec matches (default `false`) |
| `sync_binaries` | — | Copy the offline binaries into `dir` (default `false`) |

## Examples

A directory writable by a scheduled job running as `http-writer`:

```yaml
pgcli_http_servers:
  - name: review
    dir: /data/www/lh3-review
    dir_owner: http-writer
    dir_group: http-writer
    dir_mode: '0755'
    listen: 0.0.0.0
    port: 11235
```

Minimal: expose the offline directory over HTTP on the LAN and register boot
autostart.

```yaml
pgcli_http_servers:
  - name: images
    dir: /srv/offline-images
    listen: 0.0.0.0
    port: 18080
    autostart: true
```

Write modes restricted to specific sources, with a larger upload limit:

```yaml
  - name: share
    dir: /srv/share
    listen: 0.0.0.0
    port: 18081
    allow_upload: true
    allow_dir: true
    allow_delete: true
    max_upload: 512M
    admin_ips:
      - 192.0.2.20
      - 192.0.2.21
```

TLS, two ways (`tls: true` uses the auto-generated self-signed certificate;
certificate and key paths refer to the target host):

```yaml
  - name: images
    tls: true
  - name: files
    tls_cert: /etc/pgcli-http/files.crt
    tls_key: /etc/pgcli-http/files.key
```

The three Basic Auth sources can be combined, but a username may appear in only
one of them:

```yaml
  - name: images
    auth:
      - "alice:s3cret"          # plaintext in the gitignored group_vars
    auth_env:
      - PGCLI_HTTP_AUTH         # controller env var, value like bob:hunter2
    auth_file:
      - /home/pgcli/pg/data/http/htpasswd
```

`auth_env` values are read on the controller with `lookup('env', ...)`; only
the variable names are passed to the target host as `--auth-env`. When
plaintext `auth` or `auth_env` entries are present the start task runs with
`no_log`, so passwords do not appear in the default output or the recap.

Maintain htpasswd files with pgcli itself (incremental upsert, other accounts
preserved):

```bash
printf '%s\n' "$PASSWORD" | pg http htpasswd alice
# Writes to <base_dir>/http/htpasswd by default; --file overrides the path
```

## Binary sync

Binary copying is disabled by default. Set `sync_binaries: true` on a server
to refresh the controller's `bin/pg` into `images/pg`, then copy only
`pgcli_http_binaries` (default `pg` and `podman-launcher-linux_amd64`) into its
`dir`. **Image tarballs are not copied here**; export them into that directory
first with `scripts/export-images.sh` (see [Offline image server](./image-server/)).

The controller therefore needs a `make build` first, and the other binaries
must already exist under `images/` when binary copying is enabled; otherwise the
playbook fails before any host is modified. Override the variables in
`group_vars/http/main.yml`:

```yaml
pgcli_http_binaries: [pg, podman-launcher-linux_amd64]
pgcli_http_default_dir: /srv/offline-images
```

A file-server host runs no containers, so the install stage skips container
images by default: the offline image server is not checked, no images are
loaded, and the installer runs with `--skip-pull`. That avoids a large
transfer on a distribution host that has no pg binary yet and needs no local
images. If the host must also carry images for other containers, override
with `-e pgcli_load_images=true -e pgcli_install_skip_pull=false`.

## Running

```bash
cd ansible

# Install/update pgcli and reconcile the servers on the [http] group
uv run ansible-playbook playbooks/deploy-http.yaml --limit http

# A specific host
uv run ansible-playbook playbooks/deploy-http.yaml -e HOSTS=node-http

# Reconcile only (skip the install role)
uv run ansible-playbook playbooks/deploy-http.yaml --limit http --skip-tags pgcli
```

The playbook has two stages: the `pgcli` tag installs and configures the host,
the `http` tag only reconciles servers. Servers run as the deployment account
(`pgcli` in rootless setups); served directories under `/srv` are created by
root and chowned to that account.

## Idempotency and lifecycle

- Spec comparison covers `dir`, `listen`, `port`, the three write-mode flags,
  `max_upload`, `admin_ips`, TLS, the certificate/key paths, and the auth
  usernames with their passwords/hashes. Any change restarts the server.
- The role runs `pg http stop` first so stale state files are cleaned up and
  `pg http start` cannot create an `images-2` style duplicate.
- When the port is taken, `pg http` silently increments it; the role re-reads
  the state file after starting and fails if the bound port differs from the
  declared one, so consumers never get a wrong URL.
- The health probe hits `http(s)://<listen>:<port>/__pgcli_health` (skipping
  certificate validation under TLS); the endpoint needs no authentication.
- With `autostart: true` the role snapshots the running spec into `pg.yaml` via
  `pg autostart enable --http --name`, refreshing it on every restart.
  `state: stopped` or `autostart: false` removes the snapshot.
- `state: absent` stops the server and deletes the state file.

## Caveats

- Auth is compared by username and password: adding or removing accounts, and
  changing the password of an existing one, restarts the server. Inline `auth`
  and `auth_env` passwords are verified on the controller against the bcrypt
  hashes in the running spec; `auth_file` entries are compared hash-for-hash,
  so accounts edited on the target with `pg http htpasswd` or `htpasswd -B`
  are picked up on the next run. A running process caches the hashes it read
  at start-up, which is why the role stops and starts it on a difference.
- `auth_file` is read only at start-up: a manual edit takes effect after the
  next playbook run (or an explicit `force: true` restart).
- A server started by hand with `pg http start` is adopted: it is left alone
  when the spec matches, and restarted with the declared flags when it does not
  (manual flags missing from the configuration are lost).
- Without auth the site is open to everyone; use `0.0.0.0` only on trusted
  networks, combined with `admin_ips` or a firewall. `--tls` encrypts traffic
  but does not authenticate clients.
- During the install stage one "initialize the host" task always reports
  `changed` (existing install-script behavior); the reconcile stage should be
  `changed=0` when the state matches.
