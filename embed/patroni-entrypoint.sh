#!/bin/bash
# pgcli Patroni entrypoint: Patroni is PID 1 and owns the postmaster
# lifecycle. Its config is bind-mounted read-only at /patroni/patroni.yml by
# pgcli; everything below the mount is Patroni's to manage.
set -e

exec patroni /patroni/patroni.yml
