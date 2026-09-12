# MinIO client (mc), single static binary. The upstream release binaries are
# statically-linked stripped ELF — nothing to layer, so the base is scratch.
# The only runtime file it needs is a TLS CA bundle (mc talks to endpoints
# over HTTPS), copied from the build host's system trust store.
#
# `mc` and `ca-certificates.crt` are NOT in git: `make container-build-mc`
# downloads the per-arch release binary and stages it as `embed/mc` right
# before each build (sha256-verified), then removes them. ~30 MB per image.
FROM scratch

COPY mc /bin/mc
COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# mc resolves the user home via `getent`, which does not exist on scratch —
# pin HOME so it uses the config dir under /data instead. Mount a volume at
# /data to persist aliases/config across runs; without one the config is
# ephemeral (fine for `mc alias set ... && mc ls ...` in a single invocation).
ENV HOME=/data

ENTRYPOINT ["/bin/mc"]
