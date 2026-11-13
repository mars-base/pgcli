---
title: "Privoxy"
description: "Run Privoxy as a pgcli addon — an HTTP/HTTPS forward proxy with client ACLs, automatic port assignment, and no TLS interception"
weight: 54
---

[Privoxy](https://www.privoxy.org/) is a non-caching forward proxy: plain HTTP
requests are forwarded directly, HTTPS requests are tunnelled with `CONNECT`.
It never intercepts TLS, so pgcli manages **no CA and no certificates** — the
client's own trust store keeps working. pgcli runs it as a **standalone,
top-level addon** with the usual lifecycle (install / start / stop / logs /
autostart / remove) managed through `pg.yaml`.

Typical uses:

- a shared egress proxy for hosts and containers that have no direct route
  (or need a single, auditable outbound address),
- a `HTTP_PROXY` / `HTTPS_PROXY` endpoint for package managers, `curl`, and
  applications inside the fleet,
- a network-level gate: only clients in `--allow-network` may use it.

> **Platform support:** Linux uses host networking; macOS uses the pgcli-net
> bridge with the port published. The image is multi-arch (amd64 + arm64).

## How It Works

pgcli renders a complete Privoxy `config` from your flags and bind-mounts it
read-only at `/etc/privoxy/config`. Logs go to the container's stdout
(`logfile /dev/stdout`), visible with `pg logs addon privoxy`. The image's own
entrypoint is bypassed (`--entrypoint /usr/sbin/privoxy`) because it rewrites
the config file in place, which would fail on a read-only mount.

The rendered defaults are deliberately minimal and safe: caching is off,
remote toggles and CGI editing are disabled, request-blocking is not enforced,
and the proxy does not forward proxy authentication headers. No actions files
are loaded, so it is a pure forwarder — use `--conf-file` when you need
filtering.

### Port assignment

Each instance gets its own port. The first one starts at
`privoxy_start_port` (default **8118**); additional instances increment from
there, and any port already listening on the host (`ss -tlnH`) is skipped, so
installing several proxies on one machine never collides with each other or
with existing services:

```bash
pg addon install privoxy --name p1     # 8118
pg addon install privoxy --name p2     # 8119
pg addon install privoxy --name p3     # 8120, unless a host service owns it
```

Pin a port explicitly with `--port N` (a port already pinned to another
privoxy instance is rejected; a port owned by a non-pgcli process shows up as
a container start error).

### Client ACLs

ACLs use Privoxy's native syntax (`host`, `addr[/mask][:port]`, and the
optional two-part form `client-spec destination-spec`). Two rules of Privoxy's
own semantics matter:

1. Once any `permit-access` / `deny-access` line exists, the **default is
   deny**.
2. The **last matching line wins**.

pgcli therefore renders permits before denies:

| Flags | Rendered result | Meaning |
|-------|-----------------|---------|
| `--allow-network A,B` | `permit-access A`, `permit-access B` | whitelist — everyone else is denied |
| `--deny-network C` | `permit-access 0.0.0.0/0`, `deny-access C` | blacklist — everyone allowed except C |
| `--allow-network A --deny-network C` | permits first, then denies | allow a range, deny a narrower address inside it |

Both flags are repeatable **and** accept comma-separated lists; all forms are
merged (`--allow-network 10.0.0.0/8,192.168.0.0/16 --allow-network 172.16.0.0/12`
is one three-entry whitelist). Passing either flag replaces the stored list on
reinstall; passing neither clears it. With no ACL and a non-loopback listener
the install prints an **open proxy** warning — anyone who can reach the port
can proxy through it. Bind loopback (`--listen 127.0.0.1`) or add an ACL unless
the port is already firewalled.

For an IPv6 listener the catch-all permit is `[::]/0` instead of `0.0.0.0/0`.

### Custom configuration

`--conf-file FILE` copies a full Privoxy config into the addon directory
instead of rendering one. It is mutually exclusive with the ACL flags; the
file owns `listen-address` (pgcli's `--port` is then used only in the summary
and for macOS port publishing). Use this for actions/filter files, upstream
SOCKS forwarding, or any directive the template does not expose.

## Commands

```bash
# install (port auto-assigned from 8118)
pg addon install privoxy --name p1

# whitelist two private ranges (repeatable and/or comma-separated)
pg addon install privoxy --name p1 --allow-network 10.0.0.0/8,192.168.0.0/16

# blacklist one client, keep the rest open
pg addon install privoxy --name p2 --deny-network 10.0.0.66/32

# explicit port and loopback bind
pg addon install privoxy --name p3 --port 8120 --listen 127.0.0.1

# custom config
pg addon install privoxy --name p4 --conf-file ./privoxy.conf

# lifecycle
pg addon list
pg addon stop privoxy --name p1
pg addon start privoxy --name p1
pg logs addon privoxy --name p1 -n 50
pg addon remove privoxy --name p1
```

Re-running install re-renders the config and recreates the container only when
the rendered file changed (or when `--force` is set).

## Using the proxy

```bash
# HTTP: forwarded directly
curl -x http://10.0.0.5:8118 http://example.com/

# HTTPS: CONNECT tunnel; the client still validates example.com's own cert
curl -x http://10.0.0.5:8118 https://example.com/

# environment variables for most tools
export HTTP_PROXY=http://10.0.0.5:8118
export HTTPS_PROXY=http://10.0.0.5:8118
export NO_PROXY=localhost,127.0.0.1,10.0.0.0/8
```

A `curl -v` against an HTTPS URL shows `CONNECT example.com:443` followed by a
normal TLS handshake with the origin — no certificate is replaced.

## Autostart

```bash
pg autostart enable --privoxy --name p1
pg autostart status
pg autostart disable --privoxy --name p1
```

At boot, `pg start --autostart` starts the container from the config already
on disk, recreating it if it was removed. Install the instance first.

## Notes

- **No TLS interception** by design: no MITM CA, no certificate distribution.
  Clients validate the origin's real certificate through the tunnel.
- **No caching and no content filtering** in template mode; add actions files
  through `--conf-file` if you need filtering.
- **No authentication**: Privoxy's ACLs are address-based. Protect the port
  with `--allow-network`, a loopback bind, or a firewall.
- The config directory is `<base_dir>/addon/privoxy/<name>/`; `remove` deletes
  it, and there is no other state.
- Default image: `ghcr.io/mars-base/pgcli/pgcli-privoxy:4.2.0` (official
  amd64 `.deb`, arm64 built from the official source tarball).
