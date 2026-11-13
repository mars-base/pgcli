---
title: "Egress proxy"
description: "Route installs, downloads and extension builds through a forward proxy in an isolated data center"
weight: 16
---

In an isolated data center the database hosts have no direct route to the
internet. Run one Privoxy add-on on a host that does, and point the playbooks
at it — installs, image pulls, runtime downloads and extension builds then
share that egress.

```text
managed hosts ── HTTP / CONNECT ──▶ Privoxy (connected host) ──▶ internet
```

## Start the proxy

Run this on the host with internet access; the same pgcli binary and image set
is enough, no extra package:

```bash
pg addon install privoxy --name egress --allow-network 10.0.0.0/8
```

- Privoxy listens on `0.0.0.0:8118` by default. `--allow-network` is a client
  whitelist; with no ACL and a non-loopback listener the install prints an
  open-proxy warning.
- One listener serves HTTP directly and HTTPS through `CONNECT`, so a single
  URL covers both.
- Verify from a host that will use it:

```bash
curl -x http://192.0.2.10:8118 -o /dev/null -w '%{http_code}\n' https://repo.pigsty.io/
```

See [Privoxy](../addon/privoxy/) for ACL syntax, custom upstream SOCKS,
autostart and logs.

## Configure Ansible

Set two variables in `group_vars/all/main.yml` (copy it from
`main.yml.example` if it does not exist yet):

```yaml
pgcli_proxy_url: "http://192.0.2.10:8118"
pgcli_no_proxy: "localhost,127.0.0.1,::1"
```

`pgcli_proxy_url` is exported to the deployment account as `HTTP_PROXY` and
`HTTPS_PROXY` (uppercase and lowercase). Leaving it empty keeps direct
connections: the derived `pgcli_proxy_env` is then `{}` and every play behaves
as before. Check the evaluated result with:

```bash
cd ansible
uv run ansible <host> -m debug -a 'var=pgcli_proxy_env'
```

### What uses the proxy

| Step | How it reaches the proxy |
|------|--------------------------|
| Install play | `scripts/install.sh` (apt, release download) and every `get_url` |
| Images | `podman pull`, image loading, lazy add-on image loads |
| Runtime downloads | `pg addon install …`, `pg sqlite install` — Go HTTP client |
| Extension builds | `podman build` behind `pg extension install` |

### What does not

- Running services: containers are created with `--http-proxy=false`, so
  PostgreSQL, backup, pgbouncer, PostgREST and Gitea never inherit the proxy.
- Patroni and backup fallback image builds keep the proxy explicitly disabled.
- The LAN reachability checks in `offline-check.yml` bypass the proxy.

## NO_PROXY

The playbooks append the hosts of the four offline URLs
(`pgcli_install_binary_url`, `pgcli_podman_binary_url`,
`pgcli_download_mirror`, `pgcli_images_http_url`) and every inventory
`private_ip` to `pgcli_no_proxy`.

Add other internal endpoints by exact IP or hostname. Python (Ansible
`get_url`/`uri`) and apt do not understand CIDR entries in `no_proxy`; a range
such as `10.0.0.0/8` only helps curl and Go clients. Running a LAN mirror and
an egress proxy together is fine — the mirror hosts are added automatically.

## Using it without Ansible

In an SSH shell, export the same variables before running `pg`:

```bash
export HTTP_PROXY=http://192.0.2.10:8118
export HTTPS_PROXY=http://192.0.2.10:8118
export NO_PROXY=localhost,127.0.0.1,::1
```

`pg` and the `podman` commands it runs inherit them. Three caveats:

- `sudo` resets the environment by default — use `sudo -E`,
  `sudo env HTTP_PROXY=... pg ...`, or `env_keep` in sudoers.
- The exports cover only that shell and its children; `pg start --autostart`
  and other boot-time paths do not see them.
- Containers created by pgcli still get `--http-proxy=false`; the proxy is
  never passed into a long-running service.

## Security

Prefer Privoxy client ACLs to a `user:pass@` URL: credentials embedded in the
proxy URL are visible in process environments (`/proc/<pid>/environ`) and in
verbose Ansible logs. Keep the listener on the private network and the ACL
whitelist narrow.

## Verify the whole path

1. From each managed host, fetch an external URL through the proxy with
   `curl -x` as shown above.
2. Run one extension install against a test instance:

   ```bash
   pg extension install hll -i <instance>
   ```

   The build must reach `repo.pigsty.io`, `apt.postgresql.org` and
   `deb.debian.org`. Watch `pg logs addon privoxy --name egress`
   (`logfile /dev/stdout`) or capture port 8118 while it runs.
3. Confirm the recreated container is clean:

   ```bash
   podman exec <container> env | grep -i proxy || echo clean
   ```

If the extension build bypasses the proxy, check that the process running
`pg extension install` actually has the variables (Ansible sets them on the
install play). Internal requests that unexpectedly hit the proxy usually mean
the exact IP or hostname is missing from `NO_PROXY`.
