# InfluxDB 1.13.1 (single-node OSS), packaged from the official linux/amd64
# release tarball: influxd (server), influx (CLI) and influx_inspect (offline
# line-protocol export). All three are static-pie Go binaries; Alpine provides
# only the CA bundle, tzdata and a shell for `podman exec`.
#
# The staged files are NOT in git: `make container-build-influxdb` downloads
# and sha256-verifies the official tarball, stages the binaries right before
# the build, then removes them. Single-arch (amd64) — the addon targets
# Linux x86 only.
FROM docker.io/library/alpine:3.22

RUN apk add --no-cache ca-certificates tzdata

COPY influxd influx influx_inspect /usr/local/bin/
COPY influxdb.conf /etc/influxdb/influxdb.conf

RUN mkdir -p /var/lib/influxdb

EXPOSE 8086 8088
VOLUME ["/var/lib/influxdb"]

# pgcli supplies `-config <mounted influxdb.conf>` at run time; the copied
# upstream file is a ready-to-run default for standalone `podman run`.
ENTRYPOINT ["/usr/local/bin/influxd"]
CMD ["-config", "/etc/influxdb/influxdb.conf"]
