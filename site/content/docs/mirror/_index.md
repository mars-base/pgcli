---
title: "Config Mirror"
description: "Configure podman registry mirrors for docker.io, ghcr.io, and quay.io"
weight: 11
icon: fa-solid fa-network-wired
menus:
  main:
    identifier: docs-mirror
    parent: docs
    weight: 11
    params:
      icon: fa-solid fa-network-wired
cascade:
  type: docs
  footer_style: slim
---

On networks where `docker.io`, `ghcr.io`, or `quay.io` is unreachable, podman
can pull images through a registry mirror. pgcli manages a single drop-in file
covering all three registries and can configure it both at install time and
after installation.

## Configure at install time

Use `--docker-mirror HOST` as a `docker.io` shorthand, and repeat
`--registry-mirror REGISTRY=HOST` for one or more registries:

```bash
# docker.io shorthand
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash -s -- --docker-mirror docker.m.daocloud.io

# one or more registries
curl -fsSL https://raw.githubusercontent.com/mars-base/pgcli/main/scripts/install.sh | bash -s -- \
  --registry-mirror ghcr.io=ghcr.m.daocloud.io \
  --registry-mirror quay.io=quay.m.daocloud.io
```

Both flags write the same drop-in file (Linux only):

- root or passwordless sudo: `/etc/containers/registries.conf.d/99-pgcli-registry-mirror.conf`
- otherwise: `~/.config/containers/registries.conf.d/99-pgcli-registry-mirror.conf`

Registries without a mirror stay direct. Podman tries the mirror first and
falls back to the upstream registry when the mirror does not carry an image.

## Manage after installation

```bash
pg mirror set docker.m.daocloud.io        # docker.io shorthand
pg mirror set ghcr.io=ghcr.m.daocloud.io \
              quay.io=quay.m.daocloud.io  # multiple registries
pg mirror show                            # effective mirror per registry
pg mirror clean                           # remove all managed config
pg mirror clean ghcr.io                   # remove selected registries only
```

Every `set` rewrites the managed file, even when the value is unchanged, and
keeps mirrors for registries not mentioned in that invocation. The old
`99-pgcli-docker-mirror.conf` file is migrated automatically and removed by the
next `set`.

`show` reports each configured registry with its effective source. `clean`
without arguments removes all managed files; with registry arguments it removes
only those mirrors from both the system and user scopes.

## File precedence

A user-level file overrides the system file for the same registry. When `set`
writes the system file, it removes only the invoking account's user-level
entries for registries now configured at system scope; unrelated user entries
are kept. If a system file exists and passwordless sudo is unavailable, `clean`
stops without deleting anything and asks you to run `sudo pg mirror clean`.

## Host format

`HOST` accepts a hostname, `host:port`, `[IPv6]`, or `[IPv6]:port`. The command
does not probe reachability and does not read `pg.yaml`. It is Linux-only, and
configuration applies to new pulls; running containers are unaffected.
