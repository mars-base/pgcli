# rustfs, wrapped so pgcli needs no host-side uid juggling.
#
# The upstream image runs rustfs as a FIXED non-root user (rustfs, uid/gid
# 10001) baked via `User=rustfs`. That uid is the reason a host user under
# rootless podman cannot pre-own a bind-mounted data dir for it (no claim on
# 10001), which forced a whole ownership layer onto pgcli. This image removes
# the problem at its source: it re-declares `USER root` so our wrapper entrypoint
# runs as container-root, chowns the bind-mounted volume roots to 10001, then
# `su`-drops to rustfs and execs the UNMODIFIED upstream /entrypoint.sh. rustfs
# still runs unprivileged; pgcli just runs the container and never touches host
# ownership. TLS material is handled the same root-only way but WITHOUT touching
# the host: the wrapper copies the (read-only-mounted) cert/key into a fresh
# container-local 10001-owned dir, so pgcli keeps owning its own cert dir.
#
# Pull-only, like the other pgcli add-on images: pgcli never builds this.
# `make container-build-rustfs && container-push-rustfs` publishes it to the
# public ghcr.io/mars-base/pgcli registry; the rustfs base image is multi-arch
# so the build is a straight dual-arch manifest with no binary staging.
#
# The wrapper inherits the base image's ENV (RUSTFS_VOLUMES, etc.) and CMD, so
# pgcli passes everything the same way it would for docker.io/rustfs/rustfs.
ARG RUSTFS_BASE=docker.io/rustfs/rustfs:1.0.0
FROM ${RUSTFS_BASE}

# Root before the COPY/chmod: the base bakes `User=rustfs`, and a non-root build
# step cannot chmod a root-owned file. Re-declared again after the COPY for
# clarity of intent (this image starts as root — see the top comment).
USER root

COPY rustfs-entrypoint.sh /pgcli-rustfs-entrypoint.sh
RUN chmod 0755 /pgcli-rustfs-entrypoint.sh

# Root at container start is deliberate and ONLY for the chown+drop step; the
# exec'd rustfs process runs as uid 10001 (see the wrapper).
ENTRYPOINT ["/pgcli-rustfs-entrypoint.sh"]
