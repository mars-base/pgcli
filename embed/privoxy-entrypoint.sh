#!/bin/sh
set -eu

CONF=/etc/privoxy/config
LISTEN="${PRIVOXY_LISTEN_ADDRESS:-0.0.0.0:8118}"

mkdir -p /var/log/privoxy /var/lib/privoxy/certs /var/lib/privoxy/ca
chown -R privoxy:nogroup /var/log/privoxy /var/lib/privoxy

sed -i '/^listen-address/d' "$CONF"
printf '\nlisten-address %s\n' "$LISTEN" >>"$CONF"

if grep -q '^logfile ' "$CONF"; then
    sed -i 's|^logfile .*|logfile /dev/stdout|' "$CONF"
else
    printf '\nlogfile /dev/stdout\n' >>"$CONF"
fi

if [ -n "${PRIVOXY_PERMIT_ACCESS:-}" ]; then
    for net in $PRIVOXY_PERMIT_ACCESS; do
        printf 'permit-access %s\n' "$net" >>"$CONF"
    done
fi

exec /usr/sbin/privoxy --no-daemon --user privoxy "$CONF"
