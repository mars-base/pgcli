FROM docker.io/library/postgres:18

# Patroni for PostgreSQL HA, co-located with the PostgreSQL binaries in the
# same image (Patroni drives pg_ctl/initdb/pg_rewind/pg_basebackup in place).
# psycopg3 is required, not optional: Patroni refuses to start without a
# PostgreSQL adapter even for `patroni --version`. The aws extra is omitted:
# pgcli's DCS layer is etcd (the etcd addon) or an external endpoint, never
# S3/DynamoDB. vim is present because an interactive `patronictl edit-config`
# needs an EDITOR inside the container.
#
# pgbackrest + openssh-server enable the pgcli backup container to SSH into
# this member and run backups (same architecture as the plain PG image).
# S3/MinIO data transfer goes directly from the remote pgBackRest process,
# SSH is only the control channel.
#
# Built with the host proxy disabled (`--http-proxy=false`) — a stale proxy
# env makes the pip install 403 against PyPI. Version pinned for reproducibility.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
         python3 python3-pip vim-tiny \
         pgbackrest=2.59.1* openssh-server \
    && rm -rf /var/lib/apt/lists/* \
    && pip3 install --no-cache-dir --break-system-packages "patroni[psycopg3,etcd3]==4.1.5" \
    && mkdir -p /var/run/sshd /home/postgres/.ssh /var/log/pgbackrest \
    && chown -R postgres:postgres /home/postgres/.ssh \
    && chmod 700 /home/postgres/.ssh \
    && chown postgres:postgres /var/log/pgbackrest

# Patroni must resolve the PostgreSQL client tools itself: same-image guarantee.
RUN patroni --version \
    && for b in pg_ctl postgres pg_rewind pg_basebackup initdb; do \
         command -v "$b" >/dev/null || { echo "MISS $b"; exit 1; }; \
       done

# Entrypoint generates SSH host keys at runtime in /tmp (writable by postgres),
# so the container works with --userns=keep-id. No build-time key generation
# or /etc/ssh writes (root-only in the image).
COPY patroni-entrypoint.sh /usr/local/bin/patroni-entrypoint.sh
RUN chmod +x /usr/local/bin/patroni-entrypoint.sh

USER postgres
ENTRYPOINT ["patroni-entrypoint.sh"]
