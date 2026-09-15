#!/bin/bash
# pgcli Patroni entrypoint: starts sshd for backup support, then launches
# Patroni as PID 1. Patroni owns the postmaster lifecycle; its config is
# bind-mounted read-only at /patroni/patroni.yml by pgcli.
set -e

# Generate SSH host keys if absent (first boot or ephemeral container).
for key in /etc/ssh/ssh_host_rsa_key /etc/ssh/ssh_host_ed25519_key; do
    if [[ ! -f "$key" ]]; then
        alg=${key##*_}
        alg=${alg%_key}
        ssh-keygen -t "${alg:-rsa}" -f "$key" -N '' -q
    fi
done

# Install the backup container's public key if provided via bind-mount.
# The key is mounted read-only at /run/pgcli/backup_id_rsa.pub so that it
# survives container recreation. We copy (rather than mounting directly) so
# sshd sees correct postgres ownership and permissions.
if [[ -f /run/pgcli/backup_id_rsa.pub ]]; then
    mkdir -p /etc/ssh/authorized_keys
    cp /run/pgcli/backup_id_rsa.pub /etc/ssh/authorized_keys/postgres
    chown postgres:postgres /etc/ssh/authorized_keys/postgres
    chmod 600 /etc/ssh/authorized_keys/postgres
fi

# Start sshd in background. Each Patroni member gets a unique PGCLI_SSH_PORT
# to avoid port collisions under host networking.
SSHD_OPTS=""
if [ -n "${PGCLI_SSH_PORT:-}" ]; then
    SSHD_OPTS="-p ${PGCLI_SSH_PORT}"
fi
/usr/sbin/sshd $SSHD_OPTS

# Hand off to Patroni as PID 1.
exec patroni /patroni/patroni.yml
