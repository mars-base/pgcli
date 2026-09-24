#!/bin/sh
# pgcli-rustfs entrypoint wrapper.
#
# The upstream rustfs image bakes `User=rustfs` (uid/gid 10001) and runs
# /entrypoint.sh as that user. That is the whole reason a host cannot prepare a
# bind-mounted data dir for it from outside under rootless podman (the host user
# has no claim on uid 10001). We take the fix INSIDE the container instead:
# this wrapper runs as root (see `USER root` in the Containerfile), chowns the
# dirs the rustfs process will need to its own uid 10001, then drops to
# `rustfs` and hands off to the UNCHANGED upstream /entrypoint.sh so all of its
# argument normalization, RUSTFS_VOLUMES brace expansion, mkdirs, and the
# final `exec /usr/bin/rustfs ...` still happen exactly as upstream ships them.
#
# Net effect for pgcli: NO host-side ownership layer at all. The bind mounts
# come up writable by 10001 regardless of rootless vs rootful, and the final
# rustfs process is still unprivileged.
set -eu

RUSTFS_UID=10001
RUSTFS_GID=10001

# 1) Data dirs. rustfs erasure-codes across its volume roots; under pgcli those
#    are bind-mounted at /data (SNSD) or /data/rustfsN (SNMD/MNMD). Chown /data
#    and its immediate children — NOT recursive, mirroring upstream's deliberate
#    "non-recursive to avoid large disk overhead" chown. A freshly mounted dir
#    owned by 10001 is enough: rustfs (running as 10001) then creates everything
#    below it itself. The dirs are pgcli-owned but 0755, so re-owning them does
#    not lock pgcli out (it never writes them directly; `--clean-data` reclaims
#    them through `podman unshare rm`).
for p in /data /data/*; do
    [ -e "$p" ] || continue
    chown "$RUSTFS_UID:$RUSTFS_GID" "$p" 2>/dev/null || true
done

# 2) TLS. rustfs reads its key + cert as uid 10001, but pgcli generated them in a
#    0700 dir it owns and MUST keep owning — chowning that dir to 10001 would
#    lock pgcli out of its own certs on the next refresh / `pg cert` / fetch-ca.
#    So we do NOT re-own it. Instead, as container-root (whoever owns the bind,
#    root reads it), COPY the two required files into a fresh container-local dir
#    owned by 10001 and point RUSTFS_TLS_PATH there. pgcli mounts the source read
#    only at /opt/rustfs/certs-src: for generated mode that dir is pgcli's cert
#    dir (rustfsSyncGeneratedCerts already placed rustfs_cert.pem/rustfs_key.pem
#    in it), for BYO mode the two operator files are mounted at those same names.
#    Either way the copy is uniform and no host file is ever mutated.
TLS_SRC=/opt/rustfs/certs-src
TLS_DST="${RUSTFS_TLS_PATH:-}"
if [ -n "${TLS_DST:-}" ] && [ "$TLS_DST" != "$TLS_SRC" ] && [ -d "$TLS_SRC" ]; then
    mkdir -p "$TLS_DST"
    cp -f "$TLS_SRC/rustfs_cert.pem" "$TLS_DST/rustfs_cert.pem" 2>/dev/null || true
    cp -f "$TLS_SRC/rustfs_key.pem"  "$TLS_DST/rustfs_key.pem"  2>/dev/null || true
    chown "$RUSTFS_UID:$RUSTFS_GID" "$TLS_DST/rustfs_cert.pem" "$TLS_DST/rustfs_key.pem" 2>/dev/null || true
    [ -f "$TLS_DST/rustfs_key.pem" ] && chmod 0600 "$TLS_DST/rustfs_key.pem" 2>/dev/null || true
fi

# 3) Drop to the rustfs user and exec the real entrypoint, forwarding our args.
#    `su -s /bin/sh rustfs -c '...' sh "$@"` runs the command via rustfs's shell
#    with positional params rebound, so "$@" passes through untouched.
exec su -s /bin/sh rustfs -c 'exec /entrypoint.sh "$@"' sh "$@"
