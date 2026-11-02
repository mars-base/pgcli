#!/bin/sh
# pgcli passes the connection string (including the client password) in the
# environment and asks podman for it by NAME only, so the secret never appears
# in podman's argv or `podman inspect`'s Config.Cmd. mongosh itself does not
# read MONGODB_URI, so expand the variable here and pass it as the db address.
if [ -n "${PGCLI_DOCDB_URI:-}" ]; then
    exec mongosh "$@" "$PGCLI_DOCDB_URI"
fi
exec mongosh "$@"
