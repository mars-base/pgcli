#!/bin/bash
# pgcli PostgreSQL initialization script
# Executed on first PostgreSQL container startup, configures WAL archiving.
#
# Environment variables:
#   PGBACKREST_STANZA - pgbackrest stanza name (default: pgcli)

set -e

STANZA="${PGBACKREST_STANZA:-pgcli}"

# Create a 'postgres' superuser role so pgbackrest can connect via peer/Unix
# socket when running as the OS postgres user over SSH.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-'EOSQL'
    DO $$
    BEGIN
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'postgres') THEN
            CREATE ROLE postgres WITH LOGIN SUPERUSER;
        END IF;
    END
    $$;
EOSQL

cat >> "$PGDATA/postgresql.conf" << EOF

# === pgcli PITR configuration ===
wal_level = replica
archive_mode = on
# archive_command is set after stanza creation via ALTER SYSTEM by pg start.
# Do NOT set it here — the stanza does not exist yet during first-time initdb,
# and the archiver would accumulate failures for every WAL segment.
archive_timeout = 60
max_wal_senders = 10
EOF

# Create .pgpass for the postgres OS user so that pg_cron (and other
# background workers that connect via TCP as localhost) can authenticate
# without a password prompt. pg_cron uses libpq TCP connections to
# nodename:nodeport, which may bypass pg_hba trust rules depending on
# the libpq version and connection behavior.
echo "*:*:*:${POSTGRES_USER}:${POSTGRES_PASSWORD}" > /var/lib/postgresql/.pgpass
chmod 600 /var/lib/postgresql/.pgpass
chown postgres:postgres /var/lib/postgresql/.pgpass

echo "pgcli: WAL archive configuration written (stanza=${STANZA})"
