# influx2tsdb-proxy 1.3.2 — InfluxDB 1.x HTTP API (Line Protocol + InfluxQL)
# in front of a PostgreSQL database with TimescaleDB. Packaged from the
# upstream linux/amd64 release binary; Debian slim provides the glibc runtime
# the release asset links against (it is NOT a static Go binary), plus the CA
# bundle and tzdata.
#
# The staged binary is NOT in git: `make container-build-influx2tsdb`
# downloads and sha256-verifies the release asset, stages it right before the
# build, then removes it. Single-arch (amd64) — the addon targets Linux x86
# only.
FROM docker.io/library/debian:12-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/*

COPY influx2tsdb-proxy-linux-amd64 /usr/local/bin/influx2tsdb-proxy
RUN chmod 0755 /usr/local/bin/influx2tsdb-proxy

# pgcli passes -pg/-port/-pool (and optionally -db/-verbose) at run time. The
# upstream binary requires -pg, so the image has no useful standalone CMD.
ENTRYPOINT ["/usr/local/bin/influx2tsdb-proxy"]
