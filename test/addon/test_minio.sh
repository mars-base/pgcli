#!/usr/bin/env bash
# pgcli MinIO addon end-to-end test.
# Follows site/content/docs/addon/minio.md and drives what it documents:
#   install (consecutive port pair from the pool, root password generated,
#   printed once, stored in pg.yaml), re-install against a live container
#   being a no-op that keeps the password, /minio/health/live, the full
#   `pg mc` client story (alias set validates credentials — good AND wrong;
#   mb / cp upload / ls / cp download byte-identical / du / find / rm), the
#   stateless MC_HOST_* alias form, addon list never printing the password,
#   stop ✓ output + start, --force recreate keeping the data directory, a
#   second instance getting the next port pair, and remove's two modes (data
#   kept by default vs --clean-data).
#
# `pg mc` aliases persist on the host at ~/.mc/config.json by design; the
# test registers only a uniquely-named alias and removes it in cleanup,
# touching nothing else in that file. Isolation otherwise matches
# test/e2e-test.sh: throwaway config file + base dir, own namespace, own
# minio_start_port pool (sed-injected; config init has no flag for it).
#
# Usage:
#   bash test/addon/test_minio.sh                 # full test, cleans up
#   bash test/addon/test_minio.sh --skip-destroy  # keep the store after
#   PG_BINARY=/usr/local/bin/pg bash test/addon/test_minio.sh
#   PGCLI_NAMESPACE=x1 PGCLI_MINIO_START_PORT=29100 bash test/addon/test_minio.sh
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-mn-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-mn-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-mne2e}"
MINIO_START_PORT="${PGCLI_MINIO_START_PORT:-29000}"
STORE="store"
STORE2="archive"
BUCKET="backups"
ALIAS="mne2e-store"          # unique — alias keys share the host's ~/.mc

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
run_grep() {  # output must contain $expect (fixed string)
    local desc="$1" expect="$2"; shift 2
    TESTS=$((TESTS + 1))
    local out
    if out=$("$@" 2>&1) && echo "$out" | grep -qF -e "$expect"; then
        pass "$desc"
    else
        fail "$desc (want '$expect')"
        echo "$out" | sed 's/^/      | /'
    fi
}
run_not_grep() {  # output must NOT contain $expect
    local desc="$1" expect="$2"; shift 2
    TESTS=$((TESTS + 1))
    local out rc
    out=$("$@" 2>&1) && rc=0 || rc=$?
    if echo "$out" | grep -qF -e "$expect"; then
        fail "$desc (must not contain '$expect')"
        echo "$out" | sed 's/^/      | /'
    else
        pass "$desc"
    fi
}
run_fails() {  # must exit non-zero; $expect (may be empty) must appear
    local desc="$1" expect="$2"; shift 2
    TESTS=$((TESTS + 1))
    local out rc
    out=$("$@" 2>&1) && rc=0 || rc=$?
    if [ "$rc" -ne 0 ] && { [ -z "$expect" ] || echo "$out" | grep -qF -e "$expect"; }; then
        pass "$desc"
    else
        fail "$desc (rc=$rc, want error${expect:+ containing '$expect'})"
        echo "$out" | sed 's/^/      | /'
    fi
}

pg()   { "$BINARY" -c "$CONFIG_FILE" "$@"; }
pgmc() { pg mc "$@"; }

# pgmc_env <MC_HOST_<name> value> <name> <mc args...> — stateless env alias.
pgmc_env() {
    local val="$1" name="$2"; shift 2
    env "MC_HOST_$name=$val" "$BINARY" -c "$CONFIG_FILE" mc "$@"
}

# addon_field <minio-name> <key> — value under addons.minio.<name> (8-space key).
addon_field() {
    awk -v n="        $1:" -v k="$2:" '
        $0 == n {f=1; next}
        f && /^        [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {gsub(/^[[:space:]]*[a-z_]+: /,""); print; exit}
    ' "$CONFIG_FILE"
}

mn_up() {
    podman ps --filter "name=pgcli-minio-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-minio-$NAMESPACE-$1"
}
mn_down() { ! mn_up "$1"; }
mn_gone() {
    ! podman ps -a --filter "name=pgcli-minio-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}
wait_live() {  # <api-port> — poll the documented health endpoint
    for _ in $(seq 1 30); do
        curl -sf -o /dev/null "http://127.0.0.1:$1/minio/health/live" && return 0
        sleep 1
    done
    return 1
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pgmc alias remove "$ALIAS" >/dev/null 2>&1 || true
    pg addon remove minio --name "$STORE2" --clean-data 2>/dev/null || true
    pg addon remove minio --name "$STORE"  --clean-data 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli MinIO Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Ports:     minio pool $MINIO_START_PORT+ (API first, console second)"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }
    command -v curl &>/dev/null || { red "curl is not installed (health checks)"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-minio-$NAMESPACE-" \
            --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    mkdir -p "$CONFIG_DIR" "$TEST_DIR"

    run_test "config init (isolated ns)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" --namespace "$NAMESPACE"
    sed -i "s/^minio_start_port:.*/minio_start_port: $MINIO_START_PORT/" "$CONFIG_FILE"
    run_grep "minio_start_port applied" "minio_start_port: $MINIO_START_PORT" \
        cat "$CONFIG_FILE"

    # ---- Install ----
    section "Install"
    TESTS=$((TESTS + 1))
    if INSTALL_OUT="$(pg addon install minio --name "$STORE" 2>&1)"; then
        pass "install minio --name $STORE"
    else
        fail "install minio --name $STORE"; echo "$INSTALL_OUT" | sed 's/^/      | /'
    fi
    echo "$INSTALL_OUT" | sed 's/^/      /'
    API="$(addon_field "$STORE" api_port)"
    CON="$(addon_field "$STORE" console_port)"
    PW="$(addon_field "$STORE" root_password)"
    if [ "$CON" = "$((API + 1))" ]; then pass "consecutive port pair: API $API / console $CON"
    else fail "ports not consecutive: $API / $CON"; fi
    run_grep "summary prints S3 API endpoint" "S3 API:       http://127.0.0.1:$API" \
        echo "$INSTALL_OUT"
    run_grep "summary prints Console URL" "Console:      http://127.0.0.1:$CON" \
        echo "$INSTALL_OUT"
    run_grep "root password printed in summary" "Root password: $PW" echo "$INSTALL_OUT"
    PW_COUNT=$(grep -cF "$PW" <<<"$INSTALL_OUT" || true)
    if [ "$PW_COUNT" = "1" ]; then pass "root password printed exactly once"
    else fail "root password printed $PW_COUNT times in install output (want 1)"; fi
    run_grep "default image is pgcli-minio" "ghcr.io/mars-base/pgcli/pgcli-minio" \
        grep -o 'image_tag: ghcr.io/mars-base/pgcli/pgcli-minio[^ ]*' "$CONFIG_FILE"
    run_grep "default listen is loopback" "listen: 127.0.0.1" \
        grep -o 'listen: 127.0.0.1' "$CONFIG_FILE"
    run_test "container running" mn_up "$STORE"
    run_test "data dir created" test -d "$TEST_DIR/addon/minio/$STORE/data"
    run_test "health endpoint live" wait_live "$API"

    # ---- Re-install against a live container: documented no-op ----
    section "Re-install (live container is a no-op)"
    run_grep "reinstall skips creation" "already running; skipping" \
        pg addon install minio --name "$STORE"
    PW2="$(addon_field "$STORE" root_password)"
    if [ "$PW" = "$PW2" ]; then pass "root password kept across reinstall"
    else fail "root password changed on reinstall"; fi

    # ---- pg mc ----
    section "pg mc (alias + the doc's Common commands)"
    run_fails "alias set with WRONG password rejected" "" \
        pgmc alias set "${ALIAS}bad" "http://127.0.0.1:$API" admin definitely-wrong
    run_test "rejected alias left no trace in ~/.mc" \
        bash -c "! grep -qF '${ALIAS}bad' \"\$HOME/.mc/config.json\" 2>/dev/null"
    run_test "alias set (validates against the endpoint)" \
        pgmc alias set "$ALIAS" "http://127.0.0.1:$API" admin "$PW"
    run_grep "alias list shows it" "$ALIAS" pgmc alias list
    run_grep "mb $BUCKET" "$BUCKET" pgmc mb "$ALIAS/$BUCKET"
    echo "hello from pgcli minio e2e $$" > "$TEST_DIR/upload.txt"

    run_grep "cp upload" "upload.txt" \
        pgmc cp "$TEST_DIR/upload.txt" "$ALIAS/$BUCKET/upload.txt"
    run_grep "ls bucket shows object" "upload.txt" pgmc ls "$ALIAS/$BUCKET"
    run_grep "ls store shows bucket"   "$BUCKET"    pgmc ls "$ALIAS"
    run_test "cp download" pgmc cp "$ALIAS/$BUCKET/upload.txt" "$TEST_DIR/upload.down"
    run_test "downloaded bytes identical" cmp -s "$TEST_DIR/upload.txt" "$TEST_DIR/upload.down"
    run_grep "du reports the object" "1 object" pgmc du "$ALIAS"
    run_grep "find by name pattern" "upload.txt" pgmc find "$ALIAS" -- --name '*.txt'
    run_grep "rm object" "upload.txt" pgmc rm "$ALIAS/$BUCKET/upload.txt"
    run_not_grep "object gone after rm" "upload.txt" pgmc ls "$ALIAS/$BUCKET"

    # ---- Stateless MC_HOST_* alias (documented) ----
    section "MC_HOST_* env alias (no config file)"
    run_grep "MC_HOST_storeenv lists buckets" "$BUCKET" \
        pgmc_env "http://admin:$PW@127.0.0.1:$API" storeenv ls storeenv

    # ---- list / stop / start ----
    section "List / stop / start"
    run_grep "addon list shows store" "minio (name: $STORE)" pg addon list
    run_not_grep "addon list never prints the password" "$PW" pg addon list

    TESTS=$((TESTS + 1))
    STOP_OUT="$(pg addon stop minio --name "$STORE" 2>&1)" || true
    echo "$STOP_OUT" | sed 's/^/      /'
    if echo "$STOP_OUT" | grep -qF 'stopped'; then pass "stop prints a confirmation"
    else fail "stop printed no confirmation"; fi
    run_test "container stopped" mn_down "$STORE"
    run_grep "reinstall starts a stopped container" "exists but is stopped; starting" \
        pg addon install minio --name "$STORE"
    run_test "running again" mn_up "$STORE"
    run_test "health after start" wait_live "$API"
    run_test "pg addon start is a no-op when running" pg addon start minio --name "$STORE"
    run_grep "bucket survived the restart cycle" "$BUCKET" pgmc ls "$ALIAS"

    # ---- --force recreate keeps the data directory ----
    section "Recreate with --force"
    run_grep "--force recreates the container" "Starting MinIO container" \
        pg addon install minio --name "$STORE" --force
    run_test "data dir intact after --force" test -d "$TEST_DIR/addon/minio/$STORE/data"
    run_test "healthy after --force" wait_live "$API"
    run_grep "bucket still there after --force" "$BUCKET" pgmc ls "$ALIAS"

    # ---- Second instance: next consecutive pair ----
    section "Second instance (port pool arithmetic)"
    run_test "install $STORE2" pg addon install minio --name "$STORE2"
    API2="$(addon_field "$STORE2" api_port)"
    CON2="$(addon_field "$STORE2" console_port)"
    if [ "$API2" = "$((CON + 1))" ] && [ "$CON2" = "$((CON + 2))" ]; then
        pass "pool assigned next pair: $API2 / $CON2"
    else
        fail "expected pair $((CON + 1))/$((CON + 2)), got $API2/$CON2"
    fi
    run_test "second store healthy" wait_live "$API2"

    # ---- Remove: data kept by default, --clean-data deletes ----
    section "Remove"
    run_test "remove $STORE2 keeps the data dir" pg addon remove minio --name "$STORE2"
    run_test "$STORE2 container gone" mn_gone "$STORE2"
    run_test "$STORE2 data kept" test -d "$TEST_DIR/addon/minio/$STORE2/data"
    run_test "reinstall $STORE2 (fresh, same default dir)" \
        pg addon install minio --name "$STORE2"
    run_test "remove $STORE2 --clean-data" pg addon remove minio --name "$STORE2" --clean-data
    run_test "$STORE2 data deleted" test ! -d "$TEST_DIR/addon/minio/$STORE2"

    run_test "remove $STORE keeps the data dir" pg addon remove minio --name "$STORE"
    run_test "$STORE container gone" mn_gone "$STORE"
    run_test "$STORE data kept at documented path" test -d "$TEST_DIR/addon/minio/$STORE/data"
    run_not_grep "yaml minio entries removed" "minio:" cat "$CONFIG_FILE"

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
