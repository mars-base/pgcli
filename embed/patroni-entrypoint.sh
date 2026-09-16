#!/bin/bash
# pgcli Patroni entrypoint: starts sshd for backup support, then launches
# Patroni as PID 1. Patroni owns the postmaster lifecycle; its config is
# bind-mounted read-only at /patroni/patroni.yml by pgcli.
#
# The container runs as postgres (uid 999) via USER directive. With
# --userns=keep-id, uid 999 maps to the host user's UID, giving access
# to bind-mounted data dirs owned by the host user. SSH host keys are
# generated at runtime in /tmp (writable by postgres) rather than at
# image build time in /etc/ssh (root-only).
set -e

# --- sshd setup in /tmp (writable by postgres uid) ---
SSHD_DIR="/tmp/pgcli-sshd"
mkdir -p "$SSHD_DIR"

# Generate host keys if absent. Each container gets unique keys.
for alg in rsa ed25519 ecdsa; do
    keyfile="$SSHD_DIR/ssh_host_${alg}_key"
    if [ ! -f "$keyfile" ]; then
        ssh-keygen -t "$alg" -f "$keyfile" -N '' -q
    fi
done

# Authorized keys directory.
AUTH_KEYS_DIR="$SSHD_DIR/authorized_keys"
mkdir -p "$AUTH_KEYS_DIR"

# Install the backup container's public key if provided via bind-mount.
if [ -f /run/pgcli/backup_id_rsa.pub ]; then
    cp /run/pgcli/backup_id_rsa.pub "$AUTH_KEYS_DIR/postgres"
    chmod 600 "$AUTH_KEYS_DIR/postgres"
fi

# Write a minimal sshd_config that uses our /tmp paths.
# StrictModes off: avoids permission checks on home dirs that fail when
# the container runs with --userns keep-id (host UID != 999 on disk).
cat > "$SSHD_DIR/sshd_config" <<SSHD_EOF
ListenAddress 0.0.0.0
Port ${PGCLI_SSH_PORT:-22}
HostKey $SSHD_DIR/ssh_host_rsa_key
HostKey $SSHD_DIR/ssh_host_ed25519_key
HostKey $SSHD_DIR/ssh_host_ecdsa_key
PidFile $SSHD_DIR/sshd.pid
AuthorizedKeysFile $AUTH_KEYS_DIR/%u
StrictModes no
PasswordAuthentication no
PubkeyAuthentication yes
PermitRootLogin no
UsePAM no
Subsystem sftp /usr/lib/openssh/sftp-server
SSHD_EOF

# Start sshd. If it fails, log and continue — Patroni is the primary workload.
/usr/sbin/sshd -f "$SSHD_DIR/sshd_config" 2>"$SSHD_DIR/sshd.log" || {
    echo "WARNING: sshd failed to start (see $SSHD_DIR/sshd.log)"
    cat "$SSHD_DIR/sshd.log" >&2
}

# Hand off to Patroni as PID 1.
exec patroni /patroni/patroni.yml
