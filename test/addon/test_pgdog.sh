#!/usr/bin/env bash
# pgcli PgDog addon end-to-end test.
# Covers: flag validation (missing/bad --backend, --user), install of a
# single-backend proxy against a real PG instance, rendered pgdog.toml /
# users.toml (contents + 0600 perms), read/write THROUGH pgdog with a
# backend-side data check, the openmetrics endpoint, stop ✓ output, start,
# idempotent re-install, list, remove — and a sharded install (two databases,
# --sharded-table bigint routing) verifying rows spread across both shards.
#
# Isolated from the developer's real ~/.pgcli the same way test/e2e-test.sh
# is: throwaway config file + base dir, own --namespace, own PG/SSH port
# range. PgDog's port pool starts at the built-in 7432; autoAssignPorts probes
# live ports, so a coexisting pgdog instance is skipped, not clobbered.
#
# Usage:
#   bash test/addon/test_pgdog.sh                 # full test, cleans up
#   bash test/addon/test_pgdog.sh --skip-destroy  # keep everything after
#   PG_BINARY=/path/to/pg bash test/addon/test_pgdog.sh
#   PGCLI_NAMESPACE=x1 PGCLI_PG_START_PORT=38500 PGCLI_SSH_START_PORT=43500 bash test/addon/test_pgdog.sh
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-pgd-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-pgd-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-pgde2e}"
PG_START_PORT="${PGCLI_PG_START_PORT:-38500}"
PG_SSH_PORT="${PGCLI_SSH_START_PORT:-43500}"
INSTANCE="pgd-db"           # backing PostgreSQL instance
PROXY="proxy"               # single-backend pgdog
SHARD="shard"               # two-shard pgdog
SHARD_ROWS=50

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
run_fails() {  # negative test: must fail AND print $expect (last arg)
    local desc="$1" expect="$2"; shift 2
    TESTS=$((TESTS + 1))
    local out rc
    out=$("$@" 2>&1) && rc=0 || rc=$?
    if [ "$rc" -ne 0 ] && echo "$out" | grep -qF -e "$expect"; then
        pass "$desc"
    else
        fail "$desc (rc=$rc, want error containing '$expect')"
        echo "$out" | sed 's/^/      | /'
    fi
}

pg() { "$BINARY" -c "$CONFIG_FILE" "$@"; }

PSQL="$(command -v psql || true)"
psql_via() {  # <port> <db> <sql> — as admin through a proxy/listen port
    PGPASSWORD="$PW" "$PSQL" -h 127.0.0.1 -p "$1" -U admin -d "$2" \
        -v ON_ERROR_STOP=1 -t -A -c "$3"
}
sql_backend() {  # <dbname> <sql> — direct psql inside the PG container
    podman exec "pgcli-pg-$NAMESPACE-$INSTANCE" psql -t -A -U admin -d "$1" -c "$2"
}
create_shard_dbs() {
    sql_backend "$DB" "CREATE DATABASE pgd0" && sql_backend "$DB" "CREATE DATABASE pgd1"
}

# yaml_field <instance-name> <key> — value under a 4-space instance block.
yaml_field() {
    local name="$1" key="$2"
    awk -v n="    $name:" -v k="$key:" '
        $0 == n {f=1; next}
        f && /^    [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {gsub(/^[[:space:]]*[a-z_]+: /,""); print; exit}
    ' "$CONFIG_FILE"
}
# addon_field <pgdog-name> <key> — value under addons.pgdog.<name> (8-space key).
addon_field() {
    awk -v n="        $1:" -v k="$2:" '
        $0 == n {f=1; next}
        f && /^        [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {gsub(/^[[:space:]]*[a-z_]+: /,""); print; exit}
    ' "$CONFIG_FILE"
}

wait_pg() {
    local c="$1"
    for _ in $(seq 1 30); do
        podman exec "$c" pg_isready -U admin >/dev/null 2>&1 && return 0
        sleep 1
    done
    return 1
}
pgdog_up() {  # <name> — container running
    podman ps --filter "name=pgcli-pgdog-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-pgdog-$NAMESPACE-$1"
}
pgdog_down() { ! pgdog_up "$1"; }  # stopped (container may still exist)
pgdog_gone() {
    ! podman ps -a --filter "name=pgcli-pgdog-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove pgdog --name "$SHARD" 2>/dev/null || true
    pg addon remove pgdog --name "$PROXY" 2>/dev/null || true
    pg stop -i "$INSTANCE" 2>/dev/null || true
    pg destroy -i "$INSTANCE" --force 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli PgDog Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Ports:     PG $PG_START_PORT+ / SSH $PG_SSH_PORT+ (PgDog auto 7432+)"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }
    [ -n "$PSQL" ] || { red "host psql not found (needed to talk to the proxy)"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-pgdog-$NAMESPACE-" \
            --filter "name=pgcli-pg-$NAMESPACE-" --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    mkdir -p "$CONFIG_DIR" "$TEST_DIR"

    run_test "config init (isolated ns + port range)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" \
        --namespace "$NAMESPACE" --pg-start-port "$PG_START_PORT" \
        --pg-ssh-port "$PG_SSH_PORT" --add "$INSTANCE"

    # ---- Backing instance (config init --add registered it; start creates) ----
    section "Backing PostgreSQL instance"
    run_test "start instance" pg start -i "$INSTANCE"
    run_test "instance is ready" wait_pg "pgcli-pg-$NAMESPACE-$INSTANCE"
    DB="${INSTANCE}_db"
    PW="$(yaml_field "$INSTANCE" password)"
    PORT="$(yaml_field "$INSTANCE" host_port)"
    [ -n "$PW" ] && [ -n "$PORT" ] || { red "cannot read instance password/host_port from $CONFIG_FILE"; exit 1; }

    # ---- Validation (all rejected before any container/config is written) ----
    section "Install validation"
    run_fails "missing --backend rejected" "--backend is required" \
        pg addon install pgdog --name "$PROXY" --user "admin:x"
    run_fails "missing --user rejected" "--user is required" \
        pg addon install pgdog --name "$PROXY" --backend "app=127.0.0.1:$PORT:$DB"
    run_fails "malformed --backend rejected" "must be NAME=HOST:PORT:DBNAME" \
        pg addon install pgdog --name "$PROXY" --backend "nonsense" --user "admin:x"
    run_fails "malformed --user rejected" "must be NAME:PASSWORD" \
        pg addon install pgdog --name "$PROXY" --backend "app=127.0.0.1:$PORT:$DB" --user "justname"
    run_fails "user db unknown to backends rejected" "no --backend defines" \
        pg addon install pgdog --name "$PROXY" --backend "app=127.0.0.1:$PORT:$DB" --user "admin:x:nodb"

    # ---- Single-backend install ----
    section "Install pgdog (single backend)"
    run_test "install pgdog --name $PROXY" \
        pg addon install pgdog --name "$PROXY" \
            --backend "app=127.0.0.1:$PORT:$DB" --user "admin:$PW:app"
    PGD_PORT="$(addon_field "$PROXY" host_port)"
    MTC_PORT="$(addon_field "$PROXY" openmetrics_port)"
    if [ -n "$PGD_PORT" ]; then pass "host port assigned: $PGD_PORT (+ metrics $MTC_PORT)"
    else fail "could not read pgdog host_port from config"; fi
    run_test "default image is pgdogdev/pgdog" grep -q 'pgdogdev/pgdog' "$CONFIG_FILE"
    CONF_DIR="$TEST_DIR/addon/pgdog/$PROXY"
    run_test "pgdog.toml rendered"   test -f "$CONF_DIR/pgdog.toml"
    run_test "users.toml rendered"   test -f "$CONF_DIR/users.toml"
    run_test "users.toml mode 0600" \
        bash -c "[ \"\$(stat -c %a '$CONF_DIR/users.toml')\" = 600 ]"
    run_test "toml: pooler_mode/workers/pool_size" \
        bash -c "grep -q 'pooler_mode = \"transaction\"' '$CONF_DIR/pgdog.toml' && grep -q 'workers = 2' '$CONF_DIR/pgdog.toml' && grep -q 'default_pool_size = 10' '$CONF_DIR/pgdog.toml'"
    run_test "toml: backend entry (name/host/db/shard 0)" \
        bash -c "grep -q 'name = \"app\"' '$CONF_DIR/pgdog.toml' && grep -q 'database_name = \"$DB\"' '$CONF_DIR/pgdog.toml' && grep -q 'shard = 0' '$CONF_DIR/pgdog.toml'"
    run_test "container running" pgdog_up "$PROXY"

    # ---- Read/write through the proxy ----
    section "Read/write through PgDog (:$PGD_PORT)"
    run_test "SELECT 1 via proxy" psql_via "$PGD_PORT" app "SELECT 1"
    run_test "CREATE TABLE via proxy" psql_via "$PGD_PORT" app \
        "DROP TABLE IF EXISTS pgd_e2e; CREATE TABLE pgd_e2e(id bigint primary key, note text)"
    run_test "INSERT via proxy" psql_via "$PGD_PORT" app \
        "INSERT INTO pgd_e2e VALUES (1,'a'),(2,'b'),(3,'c')"
    CNT="$(psql_via "$PGD_PORT" app "SELECT count(*) FROM pgd_e2e")"
    if [ "$CNT" = "3" ]; then pass "count via proxy = 3"; else fail "count via proxy expected 3, got '$CNT'"; fi
    BCNT="$(sql_backend "$DB" "SELECT count(*) FROM pgd_e2e")"
    if [ "$BCNT" = "3" ]; then pass "backend confirms count = 3"
    else fail "backend count expected 3, got '$BCNT'"; fi
    run_test "UPDATE via proxy" psql_via "$PGD_PORT" app "UPDATE pgd_e2e SET note='z' WHERE id=1"
    NOTE="$(psql_via "$PGD_PORT" app "SELECT note FROM pgd_e2e WHERE id=1")"
    if [ "$NOTE" = "z" ]; then pass "read-back after update = 'z'"; else fail "read-back expected 'z', got '$NOTE'"; fi
    if command -v curl &>/dev/null; then
        run_test "openmetrics endpoint responds" \
            bash -c "curl -sf -o /dev/null http://127.0.0.1:$MTC_PORT/metrics"
    else
        yellow "  (curl missing, skipping openmetrics check)"
    fi

    # ---- list / stop / start ----
    section "List / stop / start"
    run_test "addon list shows $PROXY" \
        bash -c "'$BINARY' -c '$CONFIG_FILE' addon list 2>&1 | grep -q 'pgdog (name: $PROXY)'"
    STOP_OUT="$(pg addon stop pgdog --name "$PROXY" 2>&1)"
    echo "$STOP_OUT"
    if echo "$STOP_OUT" | grep -q 'stopped'; then pass "stop prints a confirmation"
    else fail "stop printed no confirmation"; fi
    run_test "container stopped" pgdog_down "$PROXY"
    STOP2="$(pg addon stop pgdog --name "$PROXY" 2>&1)"
    if echo "$STOP2" | grep -q 'is not running'; then pass "second stop reports not-running"
    else fail "second stop output unexpected: $STOP2"; fi
    run_test "start pgdog" pg addon start pgdog --name "$PROXY"
    run_test "container running again" pgdog_up "$PROXY"
    sleep 1
    CNT2="$(psql_via "$PGD_PORT" app "SELECT count(*) FROM pgd_e2e")"
    if [ "$CNT2" = "3" ]; then pass "read works after restart"
    else fail "read after restart expected 3, got '$CNT2'"; fi

    # ---- Idempotent re-install ----
    section "Re-install (idempotent)"
    run_test "install again" pg addon install pgdog --name "$PROXY" \
        --backend "app=127.0.0.1:$PORT:$DB" --user "admin:$PW:app"
    PGD_PORT2="$(addon_field "$PROXY" host_port)"
    if [ "$PGD_PORT" = "$PGD_PORT2" ]; then pass "port stable across re-install ($PGD_PORT)"
    else fail "port changed: $PGD_PORT -> $PGD_PORT2"; fi
    CNT3="$(psql_via "$PGD_PORT" app "SELECT count(*) FROM pgd_e2e")"
    if [ "$CNT3" = "3" ]; then pass "data readable after re-install"
    else fail "read after re-install expected 3, got '$CNT3'"; fi

    # ---- Sharded install ----
    section "Install pgdog (two shards + sharded-table)"
    run_test "create shard databases" create_shard_dbs
    run_test "install sharded pgdog" pg addon install pgdog --name "$SHARD" \
        --backend "app=127.0.0.1:$PORT:pgd0:0" \
        --backend "app=127.0.0.1:$PORT:pgd1:1" \
        --sharded-table "app:users:id:bigint" \
        --user "admin:$PW:app"
    SHD_PORT="$(addon_field "$SHARD" host_port)"
    [ -n "$SHD_PORT" ] && pass "sharded proxy port: $SHD_PORT" || fail "no sharded proxy port"
    run_test "shard 1 rendered" grep -q 'shard = 1' "$TEST_DIR/addon/pgdog/$SHARD/pgdog.toml"
    run_test "sharded_tables rendered" \
        grep -q "column = \"id\"" "$TEST_DIR/addon/pgdog/$SHARD/pgdog.toml"
    run_test "sharded container running" pgdog_up "$SHARD"
    run_test "create sharded table via proxy" psql_via "$SHD_PORT" app \
        "DROP TABLE IF EXISTS users; CREATE TABLE users(id bigint primary key, note text)"
    # Route one row per statement: PgDog broadcasts a multi-row INSERT
    # verbatim to every shard instead of splitting it (see
    # site/content/docs/addon/pgdog.md, "Multi-row INSERT").
    run_test "insert $SHARD_ROWS rows via proxy (one per statement)" bash -c "
        for i in \$(seq 1 $SHARD_ROWS); do
            PGPASSWORD='$PW' '$PSQL' -h 127.0.0.1 -p $SHD_PORT -U admin -d app \\
                -v ON_ERROR_STOP=1 -q -c \"INSERT INTO users VALUES (\$i,'row-\$i')\" || exit 1
        done
    "
    C0="$(sql_backend pgd0 "SELECT count(*) FROM users" || echo err)"
    C1="$(sql_backend pgd1 "SELECT count(*) FROM users" || echo err)"
    if [[ "$C0" =~ ^[0-9]+$ ]] && [[ "$C1" =~ ^[0-9]+$ ]] && [ "$((C0 + C1))" = "$SHARD_ROWS" ] \
        && [ "$C0" -gt 0 ] && [ "$C1" -gt 0 ]; then
        pass "rows split across shards: $C0 + $C1 = $SHARD_ROWS"
    else
        fail "shard distribution wrong: pgd0=$C0, pgd1=$C1 (want both >0, sum=$SHARD_ROWS)"
    fi

    # ---- Remove ----
    section "Remove"
    run_test "remove sharded pgdog" pg addon remove pgdog --name "$SHARD"
    run_test "sharded container gone" pgdog_gone "$SHARD"
    run_test "remove $PROXY" pg addon remove pgdog --name "$PROXY"
    run_test "container gone" pgdog_gone "$PROXY"
    run_test "config dir removed" bash -c "test ! -d '$CONF_DIR'"
    # Both entries gone → the addons.pgdog map is dropped from the yaml.
    run_test "yaml entries removed" bash -c "! grep -q 'pgdog:' '$CONFIG_FILE'"

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
