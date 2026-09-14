#!/usr/bin/env bash
# pgcli PgBouncer addon end-to-end test.
# Tests the full PgBouncer lifecycle against a real backing PG instance:
#   config init (isolated namespace + port range), create/start instance,
#   install pgbouncer in LOCAL mode, read/write THROUGH the pooler and verify
#   the data landed on the backend, list/status, start/stop (with the ✓ output
#   added for this release), re-install idempotency, REMOVE mode (--dsn /
#   remote), and remove.
#
# It never touches the developer's real ~/.pgcli: a throwaway config file,
# base dir, --namespace and PG/SSH port range keep containers and ports from
# colliding with other pgcli configs on the same host. The PgBouncer port pool
# starts at the built-in default (56432); autoAssignPorts probes live ports and
# skips any already in use, so a coexisting pooler is not clobbered.
#
# Usage:
#   bash test/addon/test_pgbouncer.sh                 # full test, cleans up
#   bash test/addon/test_pgbouncer.sh --skip-destroy  # keep containers after
#   PG_BINARY=/usr/local/bin/pg bash test/addon/test_pgbouncer.sh
#   PGCLI_NAMESPACE=pgbce2e bash test/addon/test_pgbouncer.sh
#   PGCLI_PG_START_PORT=38400 PGCLI_SSH_START_PORT=43400 bash test/addon/test_pgbouncer.sh
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-pgb-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-pgb-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-pgbe2e}"
PG_START_PORT="${PGCLI_PG_START_PORT:-38400}"
PG_SSH_PORT="${PGCLI_SSH_START_PORT:-43400}"
INSTANCE="pgb-db"          # backing PostgreSQL instance
LOCAL_PGB="local"          # pgbouncer installed in local (-i) mode
REMOTE_PGB="remote"        # pgbouncer installed in remote (--dsn) mode

SKIP_DESTROY=false
if [ "${1:-}" = "--skip-destroy" ]; then
    SKIP_DESTROY=true
fi

red()   { printf '\033[0;31m%s\033[0m\n' "$*"; }
green() { printf '\033[0;32m%s\033[0m\n' "$*"; }
yellow(){ printf '\033[0;33m%s\033[0m\n' "$*"; }
pass() { green "  [PASS] $*"; }
fail() { red "  [FAIL] $*"; FAILED=$((FAILED + 1)); }
section() { echo ""; yellow "=== $* ==="; }

FAILED=0
TESTS=0
run_test() {
    local desc="$1"; shift
    TESTS=$((TESTS + 1))
    if "$@"; then pass "$desc"; else fail "$desc"; fi
}

# pg wraps the binary with the throwaway config file.
pg() { "$BINARY" -c "$CONFIG_FILE" "$@"; }

# yaml_field <name> <key> <file> — first value for <key> under the 4-space
# instance/addon block named <name>. yaml.v3 emits two nesting levels at
# 4- and 8-space indent; instance blocks start at 4 spaces.
yaml_field() {
    local name="$1" key="$2" file="$3"
    awk -v n="    $name:" -v k="$key:" '
        $0 == n {f=1; next}
        f && /^    [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {gsub(/^[[:space:]]*[a-z_]+: /,""); print; exit}
    ' "$file"
}

# host_port_of <instance> — the backing PG host port from the config.
pg_port() { yaml_field "$1" host_port "$CONFIG_FILE"; }
pg_password() { yaml_field "$1" password "$CONFIG_FILE"; }

# pgb_port <local|remote> — the PgBouncer listen host_port from the config.
# Local mode nests instances.<inst>.addons.pgbouncer (12-space key, 16-space
# fields); remote nests addons.pgbouncer.<name> (8-space key, 12-space fields).
pgb_port() {
    local which="$1"
    local file="$CONFIG_FILE"
    if [ "$which" = local ]; then
        awk -v pgbname="            pgbouncer:" '
            $0 == pgbname {f=1; next}
            f && /^            [^ ]/ {f=0}
            f && /host_port:/ {gsub(/[^0-9]/,""); print; exit}
        ' "$file"
    else
        awk -v n="        $REMOTE_PGB:" '
            $0 == n {f=1; next}
            f && /^        [^ ]/ {f=0}
            f && /host_port:/ {gsub(/[^0-9]/,""); print; exit}
        ' "$file"
    fi
}

# psql_via_pgb <port> <db> <user> <pass> <sql> — run SQL through the pooler
# using a host psql. Falls back to the container's psql if none on PATH.
PSQL="$(command -v psql || echo '')"
psql_via_pgb() {
    local port="$1" db="$2" user="$3" pw="$4" sql="$5"
    if [ -n "$PSQL" ]; then
        PGPASSWORD="$pw" "$PSQL" -h 127.0.0.1 -p "$port" -U "$user" -d "$db" \
            -v ON_ERROR_STOP=1 -t -A -c "$sql"
    else
        podman exec "pgcli-pgbouncer-$NAMESPACE-$6" psql -h 127.0.0.1 -p "$port" \
            -U "$user" -d "$db" -t -A -c "$sql"
    fi
}

# wait_ready <container> [retries] — poll pg_isready inside a PG container.
wait_pg() {
    local c="$1"
    for _ in $(seq 1 30); do
        podman exec "$c" pg_isready -U admin >/dev/null 2>&1 && return 0
        sleep 1
    done
    return 1
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove pgbouncer --pg-name "$REMOTE_PGB" 2>/dev/null || true
    pg addon remove pgbouncer -i "$INSTANCE" 2>/dev/null || true
    pg stop -i "$INSTANCE" 2>/dev/null || true
    pg destroy -i "$INSTANCE" --force 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli PgBouncer Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Ports:     PG $PG_START_PORT+ / SSH $PG_SSH_PORT+ (PgBouncer auto 56432+)"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-pgbouncer-$NAMESPACE-" \
            --filter "name=pgcli-pg-$NAMESPACE-" --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    mkdir -p "$CONFIG_DIR" "$TEST_DIR"

    run_test "config init (isolated ns + port range)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" \
        --namespace "$NAMESPACE" --pg-start-port "$PG_START_PORT" \
        --pg-ssh-port "$PG_SSH_PORT" --add "$INSTANCE"

    # ---- Backing instance ----
    # config init --add already registered $INSTANCE; `start` creates the
    # container on first run (same flow as test/e2e-test.sh).
    section "Backing PostgreSQL instance"
    run_test "start instance"  pg start -i "$INSTANCE"
    run_test "instance is ready" wait_pg "pgcli-pg-$NAMESPACE-$INSTANCE"
    DB="${INSTANCE}_db"
    PW="$(pg_password "$INSTANCE")"

    # ---- Local-mode install ----
    section "Install pgbouncer (local -i mode)"
    run_test "install pgbouncer -i $INSTANCE" pg addon install pgbouncer -i "$INSTANCE"
    run_test "config file written" \
        test -f "$TEST_DIR/addon/pgbouncer/$INSTANCE/pgbouncer.ini"
    LOCAL_PORT="$(pgb_port local)"
    if [ -n "$LOCAL_PORT" ]; then pass "pgbouncer host port assigned: $LOCAL_PORT"
    else fail "could not read pgbouncer host_port from config"; fi
    run_test "pgbouncer container running" \
        bash -c "podman ps --filter name=pgcli-pgbouncer-$NAMESPACE- --format '{{.Names}}' | grep -q 'pgcli-pgbouncer-$NAMESPACE-$INSTANCE'"

    # ---- Read/write THROUGH the pooler ----
    section "Read/write through PgBouncer ($LOCAL_PORT)"
    run_test "SELECT via pooler" \
        psql_via_pgb "$LOCAL_PORT" "$DB" admin "$PW" "SELECT 1" "$INSTANCE"
    run_test "CREATE TABLE via pooler" \
        psql_via_pgb "$LOCAL_PORT" "$DB" admin "$PW" \
        "DROP TABLE IF EXISTS pgb_e2e; CREATE TABLE pgb_e2e(id int primary key, note text)" "$INSTANCE"
    run_test "INSERT via pooler" \
        psql_via_pgb "$LOCAL_PORT" "$DB" admin "$PW" \
        "INSERT INTO pgb_e2e VALUES (1,'a'),(2,'b'),(3,'c')" "$INSTANCE"
    CNT="$(psql_via_pgb "$LOCAL_PORT" "$DB" admin "$PW" "SELECT count(*) FROM pgb_e2e" "$INSTANCE")"
    if [ "$CNT" = "3" ]; then pass "count via pooler = 3"; else fail "count via pooler expected 3, got '$CNT'"; fi

    # Verify the write reached the BACKEND directly (not just the pooler cache).
    BCNT="$(podman exec "pgcli-pg-$NAMESPACE-$INSTANCE" psql -t -A -U admin -d "$DB" \
            -c "SELECT count(*) FROM pgb_e2e")"
    if [ "$BCNT" = "3" ]; then pass "backend confirms count = 3 (data landed on PG)"
    else fail "backend count expected 3, got '$BCNT'"; fi

    # UPDATE + read-back through pooler.
    run_test "UPDATE via pooler" \
        psql_via_pgb "$LOCAL_PORT" "$DB" admin "$PW" "UPDATE pgb_e2e SET note='z' WHERE id=1" "$INSTANCE"
    NOTE="$(psql_via_pgb "$LOCAL_PORT" "$DB" admin "$PW" "SELECT note FROM pgb_e2e WHERE id=1" "$INSTANCE")"
    if [ "$NOTE" = "z" ]; then pass "read-back after update = 'z'"; else fail "read-back expected 'z', got '$NOTE'"; fi

    # PgBouncer logs must show client login + pooled server connection.
    run_test "pooler logged a client login" \
        bash -c "podman logs pgcli-pgbouncer-$NAMESPACE-$INSTANCE 2>&1 | grep -qi 'login attempt'"
    run_test "pooler opened a server connection to backend" \
        bash -c "podman logs pgcli-pgbouncer-$NAMESPACE-$INSTANCE 2>&1 | grep -qi 'new connection to server'"

    # ---- list / status ----
    section "List / status"
    run_test "addon list shows local pgbouncer" \
        bash -c "'$BINARY' -c '$CONFIG_FILE' addon list 2>&1 | grep -q '$INSTANCE'"

    # ---- stop / start (the ✓ output added this release) ----
    section "Stop / start"
    STOP_OUT="$(pg addon stop pgbouncer -i "$INSTANCE" 2>&1)"
    echo "$STOP_OUT"
    if echo "$STOP_OUT" | grep -q 'stopped'; then pass "stop prints a confirmation"
    else fail "stop printed no confirmation"; fi
    run_test "container stopped" \
        bash -c "! podman ps --filter name=pgcli-pgbouncer-$NAMESPACE-$INSTANCE --filter status=running --format '{{.Names}}' | grep -q ."
    run_test "start pgbouncer" pg addon start pgbouncer -i "$INSTANCE"
    run_test "container running again" \
        bash -c "podman ps --filter name=pgcli-pgbouncer-$NAMESPACE-$INSTANCE --filter status=running --format '{{.Names}}' | grep -q ."
    sleep 1
    run_test "read works after restart" \
        psql_via_pgb "$LOCAL_PORT" "$DB" admin "$PW" "SELECT count(*) FROM pgb_e2e" "$INSTANCE"

    # ---- Idempotent re-install ----
    section "Re-install (idempotent)"
    run_test "install again (reuse port, no error)" pg addon install pgbouncer -i "$INSTANCE"
    LOCAL_PORT2="$(pgb_port local)"
    if [ "$LOCAL_PORT" = "$LOCAL_PORT2" ]; then pass "port stable across re-install ($LOCAL_PORT)"
    else fail "port changed on re-install: $LOCAL_PORT -> $LOCAL_PORT2"; fi

    # ---- Remote-mode install (--dsn) ----
    section "Install pgbouncer (--dsn remote mode)"
    DSN="postgres://admin:$PW@127.0.0.1:$(pg_port "$INSTANCE")/$DB"
    run_test "install pgbouncer --dsn --pg-name $REMOTE_PGB" \
        pg addon install pgbouncer --dsn "$DSN" --pg-name "$REMOTE_PGB"
    REMOTE_PORT="$(pgb_port remote)"
    if [ -n "$REMOTE_PORT" ]; then pass "remote pgbouncer host port: $REMOTE_PORT"
    else fail "could not read remote pgbouncer host_port"; fi
    run_test "remote container running" \
        bash -c "podman ps --filter name=pgcli-pgbouncer-$NAMESPACE-$REMOTE_PGB --filter status=running --format '{{.Names}}' | grep -q ."
    # remote container name differs; use it for the host-psql fallback path only.
    RCNT="$(psql_via_pgb "$REMOTE_PORT" "$DB" admin "$PW" "SELECT count(*) FROM pgb_e2e" "$REMOTE_PGB")"
    if [ "$RCNT" = "3" ]; then pass "remote pooler reads same data (count = 3)"
    else fail "remote pooler count expected 3, got '$RCNT'"; fi

    # ---- remove ----
    section "Remove"
    run_test "remove remote pgbouncer" pg addon remove pgbouncer --pg-name "$REMOTE_PGB"
    run_test "remote container gone" \
        bash -c "! podman ps -a --filter name=pgcli-pgbouncer-$NAMESPACE-$REMOTE_PGB --format '{{.Names}}' | grep -q ."
    run_test "remove local pgbouncer" pg addon remove pgbouncer -i "$INSTANCE"
    run_test "local container gone" \
        bash -c "! podman ps -a --filter name=pgcli-pgbouncer-$NAMESPACE-$INSTANCE --format '{{.Names}}' | grep -q ."

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
