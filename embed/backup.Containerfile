FROM docker.io/library/debian:13-slim

# Add PostgreSQL APT repository to get the same pgbackrest version as postgres:18
RUN apt-get update && apt-get install -y curl ca-certificates gnupg \
    && curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | \
       gpg --dearmor -o /usr/share/keyrings/pgdg.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/pgdg.gpg] http://apt.postgresql.org/pub/repos/apt trixie-pgdg main" \
       > /etc/apt/sources.list.d/pgdg.list \
    && apt-get update && apt-get install -y pgbackrest openssh-client \
    && rm -rf /var/lib/apt/lists/* \
    # Ensure postgres user/group has uid/gid 999. debian:13-slim ships
    # postgres with uid/gid ~101; systemd dep pulls in systemd-journal at
    # gid 999 which conflicts. Force both to 999 so subuid maps match the
    # PG container (postgres:18 uses uid 999).
    && groupdel systemd-journal 2>/dev/null || true \
    && (getent group postgres >/dev/null || groupadd -g 999 postgres) \
    && [ "$(getent group postgres | cut -d: -f3)" = "999" ] || groupmod -g 999 postgres 2>/dev/null || true \
    && (getent passwd postgres >/dev/null || useradd -u 999 -g 999 -m -d /home/postgres -s /bin/bash postgres) \
    && [ "$(id -u postgres 2>/dev/null || echo 0)" = "999" ] || usermod -u 999 -g 999 -d /home/postgres -s /bin/bash postgres 2>/dev/null || true \
    && mkdir -p /home/postgres/.ssh /var/lib/pgbackrest /var/log/pgbackrest /etc/pgbackrest \
    && chown -R postgres:postgres /home/postgres /var/lib/pgbackrest /var/log/pgbackrest /etc/pgbackrest \
    && chmod 700 /home/postgres/.ssh

VOLUME ["/var/lib/pgbackrest", "/var/log/pgbackrest"]

# pgbackrest.conf is mounted at runtime: -v <path>:/etc/pgbackrest/pgbackrest.conf:ro
# WAL volumes are mounted as: -v <wal_vol>:/wal/<instance>:ro

# Run as the postgres user (uid 999) so repo files are owned by the same host
# uid as the PG container's postgres (rootless podman subuid maps container
# 999 -> host 100998 in both images), eliminating the need for sudo/root or
# repo chmod relaxation.
USER postgres

ENTRYPOINT ["sleep", "infinity"]
