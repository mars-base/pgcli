# Single-node MinIO with the web console. Built from the upstream static
# binaries (minio/minio GitHub releases, per-arch, sha256-verified by the
# Makefile) on Alpine because MinIO's official image dropped the bundled
# console. Dual-arch: see `make container-build-minio`.
#
# `minio` (the staged binary, ~118 MB) is NOT in git: `make container-build-minio`
# downloads it right before building each arch and deletes it after. It is
# COPY'd from this build context (embed/). Runtime is pull-only from the public
# ghcr tag; pgcli never builds this image.
FROM docker.io/library/alpine:3.22

RUN apk add --no-cache ca-certificates

COPY minio /usr/local/bin/minio

EXPOSE 9000 9001

# pgcli supplies `server /data --address ... --console-address ...` at run time.
ENTRYPOINT ["/usr/local/bin/minio"]
