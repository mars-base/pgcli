# Predixy — a Redis protocol proxy (redis cluster / sentinel / standalone).
# Built from the upstream FREE-EDITION binary (joyieldInc/predixy GitHub
# releases) on Alpine. Upstream ships only an amd64 glibc binary, so this is a
# single-arch image (no manifest), unlike the minio/mc/rustfs targets.
#
# The glibc binary needs a musl compat layer on Alpine: gcompat provides the
# glibc loader/symbols, libgcc the unwinder (predixy is C++). Verified: the
# binary runs and serves a proxy round-trip on alpine:3.22 + gcompat + libgcc.
#
# `predixy` (the ~11 MB binary staged at embed/predixy) and `embed/predixy-conf/`
# are NOT in git: `make container-build-predixy` downloads the release tarball,
# extracts the binary and the conf/ dir right before the build, overwrites
# conf/license.conf with the current license2026.conf (the tarball's own
# license.conf is already EXPIRED — the free binary refuses to start with it),
# and deletes both after. The default predixy.conf Include's license.conf,
# auth.conf, try.conf, latency.conf — all present in the copied dir, so the
# image runs standalone with no host-mounted config.
#
# Runtime is pull-only from the public ghcr tag; pgcli never builds this image.
FROM docker.io/library/alpine:3.22

RUN apk add --no-cache gcompat libgcc

COPY predixy /usr/local/predixy/bin/predixy
COPY predixy-conf /usr/local/predixy/conf

EXPOSE 7617

# predixy is a foreground process; the conf is baked in and operators override
# it by bind-mounting their own /usr/local/predixy/conf/predixy.conf.
ENTRYPOINT ["/usr/local/predixy/bin/predixy", "/usr/local/predixy/conf/predixy.conf"]
