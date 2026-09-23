#!/usr/bin/env bash
# pgcli PostgREST addon end-to-end test.
# Tests the full PostgREST lifecycle against a real backing PG instance:
#   config init (isolated namespace + port range), create/start instance, do the
#   DATABASE-SIDE setup pgcli deliberately does NOT manage (anonymous role,
#   exposed schema, GRANTs, NOTIFY reload), install PostgREST in LOCAL mode,
#   read through the REST API (OpenAPI root + table rows), list/status,
#   start/stop, --db-pool/--force env via podman inspect, re-install
#   idempotency, remote mode (--dsn), and remove.
#
# It never touches the developer's real ~/.pgcli: a throwaway config file,
# base dir, --namespace and PG/SSH port range keep containers and ports from
# colliding with other pgcli configs on the same host. The PostgREST port pool
# starts at the built-in default (3500); autoAssignPorts probes live ports and
# skips any already in use, so a coexisting service is not clobbered.
#
# Usage:
#   bash test/addon/test_postgrest.sh                 # full test, cleans up
#   bash test/addon/test_postgrest.sh --skip-destroy  # keep containers after
#   PG_BINARY=/usr/local/bin/pg bash test/addon/test_postgrest.sh
#   PGCLI_NAMESPACE=prstce2e bash test/addon/test_postgrest.sh
#   PGCLI_PG_START_PORT=38400 PGCLI_SSH_START_PORT=43400 bash test/addon/test_postgrest.sh
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-prst-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-prst-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-prste2e}"
PG_START_PORT="${PGCLI_PG_START_PORT:-38400}"
PG_SSH_PORT="${PGCLI_SSH_START_PORT:-43400}"
INSTANCE="prst-db"          # backing PostgreSQL instance
LOCAL_PRST="local"          # postgrest installed in local (-i) mode
REMOTE_PRST="remote"        # postgrest installed in remote (--dsn) mode
API_SCHEMA="rest"           # exposed schema (PGRST_DB_SCHEMAS)
ANON_ROLE="anonymous"       # PostgREST's default db-anon-role, SET ROLE'd per request
TABLE="widgets"             # table the REST test reads

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
# instance block named <name>. yaml.v3 emits two nesting levels at 4- and
# 8-space indent; instance blocks start at 4 spaces.
yaml_field() {
    local name="$1" key="$2" file="$3"
    awk -v n="    $name:" -v k="$key:" '
        $0 == n {f=1; next}
        f && /^    [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {gsub(/^[[:space:]]*[a-z_]+: /,""); print; exit}
    ' "$file"
}

# host_port / password of the backing instance.
pg_port()     { yaml_field "$1" host_port "$CONFIG_FILE"; }
pg_password() { yaml_field "$1" password  "$CONFIG_FILE"; }

# prst_port <local|remote> — the PostgREST listen host_port from the config.
# Local mode nests instances.<inst>.addons.postgrest (12-space key, 16-space
# fields); remote nests addons.postgrest.<name> (8-space key, 12-space fields).
prst_port() {
    local which="$1"
    local file="$CONFIG_FILE"
    if [ "$which" = local ]; then
        awk -v p="            postgrest:" '
            $0 == p {f=1; next}
            f && /^            [^ ]/ {f=0}
            f && /host_port:/ {gsub(/[^0-9]/,""); print; exit}
        ' "$file"
    else
        awk -v n="        $REMOTE_PRST:" '
            $0 == n {f=1; next}
            f && /^        [^ ]/ {f=0}
            f && /host_port:/ {gsub(/[^0-9]/,""); print; exit}
        ' "$file"
    fi
}

# psql_on <instance> <sql> — run SQL directly on the backing PG container.
psql_on() {
    local inst="$1"; shift
    podman exec "pgcli-pg-$NAMESPACE-$inst" psql -U admin -d "${inst}_db" \
        -v ON_ERROR_STOP=1 -t -A -c "$*"
}

# wait_http <port> [retries] — poll the REST root until it answers (schema load
# after boot is async, so the first requests can 503 until PostgREST connects).
wait_http() {
    local port="$1" tries="${2:-30}"
    for _ in $(seq 1 "$tries"); do
        if curl -sf "http://127.0.0.1:$port/" >/dev/null 2>&1; then return 0; fi
        sleep 1
    done
    return 1
}

# wait_pg <instance> — poll pg_isready inside the PG container.
wait_pg() {
    local c="pgcli-pg-$NAMESPACE-$1"
    for _ in $(seq 1 30); do
        podman exec "$c" pg_isready -U admin >/dev/null 2>&1 && return 0
        sleep 1
    done
    return 1
}

# poll_table <port> <fragment> [tries] — curl the widgets endpoint until the
# given JSON fragment appears. PostgREST introspects the schema asynchronously
# (and again after a `NOTIFY pgrst,'reload schema'`), so a single curl right
# after the HTTP root comes up can still 404 on the table.
poll_table() {
    local port="$1" frag="$2" tries="${3:-20}"
    for _ in $(seq 1 "$tries"); do
        if curl -sf "http://127.0.0.1:$port/$TABLE" 2>/dev/null | grep -q "$frag"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove postgrest --pg-name "$REMOTE_PRST" 2>/dev/null || true
    pg addon remove postgrest -i "$INSTANCE" 2>/dev/null || true
    pg stop -i "$INSTANCE" 2>/dev/null || true
    pg destroy -i "$INSTANCE" --force 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli PostgREST Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Ports:     PG $PG_START_PORT+ / SSH $PG_SSH_PORT+ (PostgREST auto 3500+)"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }
    command -v curl   &>/dev/null || { red "curl is not installed"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-postgrest-$NAMESPACE-" \
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
    section "Backing PostgreSQL instance"
    run_test "start instance" pg start -i "$INSTANCE"
    run_test "instance is ready" wait_pg "$INSTANCE"
    DB="${INSTANCE}_db"
    PW="$(pg_password "$INSTANCE")"

    # ---- Database-side setup (pgcli does NOT manage this; the test does it
    # to make a real read possible: anon role, exposed schema, GRANTs, NOTIFY).
    # The instance is freshly created above, so a plain CREATE ROLE is enough. ----
    section "Database-side setup (authenticator role + schema + GRANTs)"
    run_test "create anon role (NOINHERIT LOGIN)" \
        psql_on "$INSTANCE" "CREATE ROLE $ANON_ROLE NOINHERIT LOGIN PASSWORD 'pw'"
    run_test "create exposed schema $API_SCHEMA" \
        psql_on "$INSTANCE" "CREATE SCHEMA $API_SCHEMA"
    run_test "create table + rows" \
        psql_on "$INSTANCE" "CREATE TABLE $API_SCHEMA.$TABLE(id int primary key, name text); INSERT INTO $API_SCHEMA.$TABLE VALUES (1,'bolt'),(2,'nut')"
    run_test "grant usage + select to anon role" \
        psql_on "$INSTANCE" "GRANT USAGE ON SCHEMA $API_SCHEMA TO $ANON_ROLE; GRANT SELECT ON $API_SCHEMA.$TABLE TO $ANON_ROLE"

    # ---- Local-mode install ----
    section "Install postgrest (local -i mode, --schema $API_SCHEMA)"
    run_test "install postgrest -i $INSTANCE --db-pool 3 --schema $API_SCHEMA --anon-role $ANON_ROLE" \
        pg addon install postgrest -i "$INSTANCE" --db-pool 3 --schema "$API_SCHEMA" --anon-role "$ANON_ROLE"
    LOCAL_PORT="$(prst_port local)"
    if [ -n "$LOCAL_PORT" ]; then pass "postgrest host port assigned: $LOCAL_PORT"
    else fail "could not read postgrest host_port from config"; fi
    run_test "postgrest container running" \
        bash -c "podman ps --filter name=pgcli-postgrest-$NAMESPACE-$INSTANCE --filter status=running --format '{{.Names}}' | grep -q ."
    run_test "REST root answers (OpenAPI)" wait_http "$LOCAL_PORT"
    run_test "root returns OpenAPI JSON" \
        bash -c "curl -sf 'http://127.0.0.1:$LOCAL_PORT/' | grep -q '\"openapi\"'"

    # Tell PostgREST to reload so the freshly-created table is visible, then read.
    psql_on "$INSTANCE" "NOTIFY pgrst, 'reload schema'" || true
    run_test "GET /$TABLE returns rows through the API" \
        poll_table "$LOCAL_PORT" 'bolt'
    run_test "row filter ?id=eq.1 returns bolt" \
        bash -c "curl -sf 'http://127.0.0.1:$LOCAL_PORT/$TABLE?id=eq.1' | grep -q '\"name\":\"bolt\"'"

    # ---- env mapping via podman inspect ----
    section "Env mapping (podman inspect)"
    run_test "PGRST_DB_URI is set" \
        bash -c "podman inspect pgcli-postgrest-$NAMESPACE-$INSTANCE --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -q '^PGRST_DB_URI=postgres://'"
    run_test "PGRST_DB_POOL=3 (from --db-pool)" \
        bash -c "podman inspect pgcli-postgrest-$NAMESPACE-$INSTANCE --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -q '^PGRST_DB_POOL=3$'"
    run_test "PGRST_DB_SCHEMAS=$API_SCHEMA (from --schema)" \
        bash -c "podman inspect pgcli-postgrest-$NAMESPACE-$INSTANCE --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -q '^PGRST_DB_SCHEMAS=$API_SCHEMA$'"
    run_test "PGRST_DB_ANON_ROLE=$ANON_ROLE (from --anon-role)" \
        bash -c "podman inspect pgcli-postgrest-$NAMESPACE-$INSTANCE --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -q '^PGRST_DB_ANON_ROLE=$ANON_ROLE$'"

    # ---- list ----
    section "List"
    run_test "addon list shows local postgrest" \
        bash -c "'$BINARY' -c '$CONFIG_FILE' addon list 2>&1 | grep -q '$INSTANCE'"

    # ---- stop / start ----
    section "Stop / start"
    STOP_OUT="$(pg addon stop postgrest -i "$INSTANCE" 2>&1)"
    echo "$STOP_OUT"
    if echo "$STOP_OUT" | grep -q 'stopped'; then pass "stop prints a confirmation"
    else fail "stop printed no confirmation"; fi
    run_test "container stopped" \
        bash -c "! podman ps --filter name=pgcli-postgrest-$NAMESPACE-$INSTANCE --filter status=running --format '{{.Names}}' | grep -q ."
    run_test "start postgrest" pg addon start postgrest -i "$INSTANCE"
    run_test "container running again" \
        bash -c "podman ps --filter name=pgcli-postgrest-$NAMESPACE-$INSTANCE --filter status=running --format '{{.Names}}' | grep -q ."
    run_test "read works after restart" \
        poll_table "$LOCAL_PORT" 'bolt'

    # ---- Idempotent re-install (port stable, container reused) ----
    section "Re-install (idempotent)"
    run_test "install again (reuse port, no error)" \
        pg addon install postgrest -i "$INSTANCE" --db-pool 3 --schema "$API_SCHEMA" --anon-role "$ANON_ROLE"
    LOCAL_PORT2="$(prst_port local)"
    if [ "$LOCAL_PORT" = "$LOCAL_PORT2" ]; then pass "port stable across re-install ($LOCAL_PORT)"
    else fail "port changed on re-install: $LOCAL_PORT -> $LOCAL_PORT2"; fi

    # ---- Remote-mode install (--dsn) ----
    section "Install postgrest (--dsn remote mode)"
    DSN="postgres://admin:$PW@127.0.0.1:$(pg_port "$INSTANCE")/$DB"
    run_test "install postgrest --dsn --pg-name $REMOTE_PRST --schema $API_SCHEMA --anon-role $ANON_ROLE" \
        pg addon install postgrest --dsn "$DSN" --pg-name "$REMOTE_PRST" --schema "$API_SCHEMA" --anon-role "$ANON_ROLE"
    REMOTE_PORT="$(prst_port remote)"
    if [ -n "$REMOTE_PORT" ]; then pass "remote postgrest host port: $REMOTE_PORT"
    else fail "could not read remote postgrest host_port"; fi
    run_test "remote container running" \
        bash -c "podman ps --filter name=pgcli-postgrest-$NAMESPACE-$REMOTE_PRST --filter status=running --format '{{.Names}}' | grep -q ."
    run_test "remote REST reads same data" \
        bash -c "wait_http '$REMOTE_PORT' 20 && curl -sf 'http://127.0.0.1:$REMOTE_PORT/$TABLE' | grep -q 'bolt'"

    # ---- remove ----
    section "Remove"
    run_test "remove remote postgrest" pg addon remove postgrest --pg-name "$REMOTE_PRST"
    run_test "remote container gone" \
        bash -c "! podman ps -a --filter name=pgcli-postgrest-$NAMESPACE-$REMOTE_PRST --format '{{.Names}}' | grep -q ."
    run_test "remove local postgrest" pg addon remove postgrest -i "$INSTANCE"
    run_test "local container gone" \
        bash -c "! podman ps -a --filter name=pgcli-postgrest-$NAMESPACE-$INSTANCE --format '{{.Names}}' | grep -q ."

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
