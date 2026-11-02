# DocumentDB gateway 1.0.0 — the official MongoDB wire-protocol gateway.
# Packaged from the upstream Ubuntu 24.04 .deb; Debian 13 slim provides the
# glibc 2.39+ runtime the daemon links against. The daemon is started directly
# (not via /usr/bin/documentdb-gateway), which bypasses the wrapper's
# systemd/runuser logic and the upstream all-in-one entrypoint telemetry.
#
# The staged .deb is NOT in git: `make container-build-documentdb-gateway`
# downloads and sha256-verifies the release asset, stages it right before the
# build, then removes it.
FROM docker.io/library/debian:13-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates libgcc-s1 libssl3t64 openssl \
 && rm -rf /var/lib/apt/lists/*

COPY documentdb-gateway.deb /tmp/documentdb-gateway.deb
RUN apt-get install -y /tmp/documentdb-gateway.deb \
 && rm -f /tmp/documentdb-gateway.deb \
 && (getent passwd documentdb-gateway >/dev/null \
     || useradd --system --user-group --home-dir /var/lib/documentdb-gateway \
                --shell /usr/sbin/nologin documentdb-gateway) \
 && mkdir -p /var/lib/documentdb-gateway/tls \
 && chown -R documentdb-gateway:documentdb-gateway /var/lib/documentdb-gateway

USER documentdb-gateway
WORKDIR /var/lib/documentdb-gateway

# The add-on mounts:
#   /var/run/postgresql                              — target instance socket (ro)
#   /etc/documentdb/gateway/pg-url                   — libpq URL file (ro)
#   /etc/documentdb/gateway/SetupConfiguration.json  — telemetry-off config (ro)
#   /var/lib/documentdb-gateway                      — persisted auto-generated TLS state
ENTRYPOINT ["/usr/lib/documentdb-gateway/documentdb-gateway-daemon"]
CMD ["run"]
