FROM docker.io/library/postgres:18

# Patroni for PostgreSQL HA, co-located with the PostgreSQL binaries in the
# same image (Patroni drives pg_ctl/initdb/pg_rewind/pg_basebackup in place).
# psycopg3 is required, not optional: Patroni refuses to start without a
# PostgreSQL adapter even for `patroni --version`. The aws extra is omitted:
# pgcli's DCS layer is etcd (the etcd addon) or an external endpoint, never
# S3/DynamoDB. vim is present because an interactive `patronictl edit-config`
# needs an EDITOR inside the container.
#
# Built with the host proxy disabled (`--http-proxy=false`) — a stale proxy
# env makes the pip install 403 against PyPI. Version pinned for reproducibility.
RUN apt-get update \
    && apt-get install -y --no-install-recommends python3 python3-pip vim-tiny \
    && rm -rf /var/lib/apt/lists/* \
    && pip3 install --no-cache-dir --break-system-packages "patroni[psycopg3,etcd3]==4.1.5"

# Patroni must resolve the PostgreSQL client tools itself: same-image guarantee.
RUN patroni --version \
    && for b in pg_ctl postgres pg_rewind pg_basebackup initdb; do \
         command -v "$b" >/dev/null || { echo "MISS $b"; exit 1; }; \
       done

# Patroni owns the postmaster lifecycle. PID 1 is the Patroni daemon, which
# reads its config from a bind-mounted file (mounted read-only at runtime);
# pgcli does NOT use docker-entrypoint-initdb.d here — Patroni runs initdb
# itself, so the admin/default_db convention of the plain PG image does not
# apply. Run as the postgres user (uid 999): rootless podman maps it onto the
# host's unprivileged subuid so the bind-mounted data dir is writable.
COPY patroni-entrypoint.sh /usr/local/bin/patroni-entrypoint.sh
RUN chmod +x /usr/local/bin/patroni-entrypoint.sh

USER postgres
ENTRYPOINT ["patroni-entrypoint.sh"]
