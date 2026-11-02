# mongosh client only — no MongoDB server. The binary comes from the official
# mongosh release tarball (Node SEA) and is downloaded, sha256-verified,
# stripped and staged as embed/mongosh by `make container-build-mongosh`; it is
# never committed. The crypt shared object is intentionally not copied: all
# pgcli commands connect with SCRAM and never use client-side field encryption.
#
# The entrypoint exists so pgcli can pass the connection string (which contains
# the password) as a NAME-ONLY environment variable instead of an argv value;
# see mongosh-entrypoint.sh.
FROM debian:13-slim

ENV DEBIAN_FRONTEND=noninteractive \
    HOME=/home/mongosh

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 1000 --user-group \
               --home-dir /home/mongosh --shell /usr/sbin/nologin mongosh \
    && mkdir -p /home/mongosh/.mongodb/mongosh

COPY mongosh /usr/local/bin/mongosh
COPY mongosh-entrypoint.sh /usr/local/bin/pgcli-mongosh-entrypoint
COPY mongosh-config.json /home/mongosh/.mongodb/mongosh/config

RUN chmod 0755 /usr/local/bin/mongosh /usr/local/bin/pgcli-mongosh-entrypoint \
    && chown -R mongosh:mongosh /home/mongosh

USER mongosh

ENTRYPOINT ["/usr/local/bin/pgcli-mongosh-entrypoint"]
