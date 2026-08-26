#!/usr/bin/env bash
# pgcli end-to-end test script.
# Tests: config init, create, start, status, exec SQL, backup, PITR, stop,
#        destroy, multi-instance isolation.
#
# Usage:
#   bash scripts/e2e-test.sh                 # full test
#   bash scripts/e2e-test.sh --skip-destroy  # keep containers after test
#   SKIP_PITR=1 bash scripts/e2e-test.sh     # skip PITR tests (faster)
set -euo pipefail

BINARY="${PG_BINARY:-./bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-$HOME/bucket/pgcli-test}"
CONFIG_DIR="$HOME/.pgcli-e2e"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
INSTANCE="e2e-test"
INSTANCE2="e2e-test2"
SKIP_DESTROY=false
SKIP_PITR="${SKIP_PITR:-0}"

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
    if "$@"; then
        pass "$desc"
    else
        fail "$desc"
    fi
}

# pg wraps the binary with the test config file
pg() {
    "$BINARY" -c "$CONFIG_FILE" "$@"
}

# sql runs a SQL command via pg exec (auto-connects with instance user/database)
sql() {
    local inst="$1"; shift
    pg exec -i "$inst" "$@"
}

# sqlval runs a SQL query and returns only the raw value (no headers/footers).
sqlval() {
    local inst="$1"; shift
    podman exec -i "pgcli-pg-$inst" psql -t -A -U pgcli -d "${inst}_db" -c "$@"
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"
        return
    fi
    section "Cleanup"
    pg stop -i "$INSTANCE" 2>/dev/null || true
    pg stop -i "$INSTANCE2" 2>/dev/null || true
    pg destroy -i "$INSTANCE" --force 2>/dev/null || true
    pg destroy -i "$INSTANCE2" --force 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    rm -rf "$TEST_DIR/$INSTANCE" "$TEST_DIR/$INSTANCE2"
    rm -rf "$TEST_DIR/backup-data" "$TEST_DIR/backup-log"
    green "  Cleanup done"
}

trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli End-to-End Test"
    echo "=========================================="
    echo "  Binary:   $BINARY"
    echo "  Test dir: $TEST_DIR"
    echo "  Config:   $CONFIG_FILE"
    echo "=========================================="

    # Pre-flight checks
    if [ ! -x "$BINARY" ]; then
        red "Binary not found: $BINARY"
        echo "  Run 'make build' first."
        exit 1
    fi

    if ! command -v podman &>/dev/null; then
        red "podman is not installed"
        exit 1
    fi

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"

    # Remove leftover pgcli containers from previous runs before deleting dirs,
    # otherwise stale volume mounts (SSH keys, pgbackrest.conf) cause start failures.
    for c in $(podman ps -a --filter "name=pgcli-" --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done

    # Podman rootless creates files with mapped UIDs — need podman unshare to
    # delete leftover data directories from previous runs.
    if [ -d "$TEST_DIR" ]; then
        podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true
    fi
    mkdir -p "$CONFIG_DIR"
    mkdir -p "$TEST_DIR"

    # Test 1: config init with --base-dir
    run_test "config init" pg config init \
        -o "$CONFIG_FILE" \
        --base-dir "$TEST_DIR" \
        --add "$INSTANCE"

    # ---- Test 2: Create Instance ----
    section "Test: Create Instance"
    run_test "Create instance (second)" pg create -i "$INSTANCE2" --base-dir "$TEST_DIR"

    # ---- Test 3: Start Instance ----
    section "Test: Start Instance"
    run_test "Start instance" pg start -i "$INSTANCE"

    # ---- Test 4: Status ----
    section "Test: Status"
    run_test "Status shows running" pg status -i "$INSTANCE"

    # ---- Test 5: Database Connectivity ----
    section "Test: Database Connectivity"
    sleep 3  # extra wait for PG to be fully ready

    run_test "pg exec SQL: SELECT 1" sql "$INSTANCE" "SELECT 1"
    run_test "pg exec SQL: SELECT version()" sql "$INSTANCE" "SELECT version()"

    # ---- Test 6: Data Operations ----
    section "Test: Data Operations"

    run_test "CREATE TABLE" sql "$INSTANCE" \
        "CREATE TABLE IF NOT EXISTS e2e_test (id serial PRIMARY KEY, msg text, ts timestamptz DEFAULT now())"

    run_test "INSERT data" sql "$INSTANCE" \
        "INSERT INTO e2e_test (msg) VALUES ('before-backup'), ('row-2'), ('row-3')"

    COUNT=$(sqlval "$INSTANCE" "SELECT count(*) FROM e2e_test")
    if [ "$COUNT" = "3" ]; then
        pass "SELECT count(*) = 3"
    else
        fail "SELECT count(*) expected 3, got '$COUNT'"
    fi

    # ---- Test 7: Snapshot (backup) ----
    section "Test: Backup / Snapshot"
    if [ "$SKIP_PITR" != "1" ]; then
        run_test "Create snapshot" pg snapshot create -i "$INSTANCE" e2e-snap-1
        run_test "List snapshots" pg snapshot list -i "$INSTANCE"
    else
        yellow "  Skipping PITR/backup tests (SKIP_PITR=1)"
    fi

    # ---- Test 8: PITR Recovery ----
    section "Test: PITR Recovery"
    if [ "$SKIP_PITR" != "1" ]; then
        sleep 5
        # Capture PITR time 1 second in the past so it's safely within
        # already-archived WAL (avoids edge-case where now() equals the
        # last WAL timestamp).
        PITR_TIME=$(sqlval "$INSTANCE" "SELECT now() - interval '1 second'")

        run_test "Insert post-snapshot data" sql "$INSTANCE" \
            "INSERT INTO e2e_test (msg) VALUES ('after-snapshot-should-be-gone')"

        AFTER_COUNT=$(sqlval "$INSTANCE" "SELECT count(*) FROM e2e_test")
        if [ "$AFTER_COUNT" = "4" ]; then
            pass "Post-snapshot count = 4"
        else
            fail "Post-snapshot count expected 4, got '$AFTER_COUNT'"
        fi

        # Force WAL switch and wait for archiver to catch up before PITR.
        sql "$INSTANCE" "SELECT pg_switch_wal()" >/dev/null
        sleep 3

        run_test "PITR restore (dry-run)" pg restore -i "$INSTANCE" --time "$PITR_TIME" --dry-run
        run_test "PITR restore" pg restore -i "$INSTANCE" --time "$PITR_TIME" --force --tail-logs

        # After restore with pause, verify data then resume
        RESTORE_COUNT=$(sqlval "$INSTANCE" "SELECT count(*) FROM e2e_test")
        if [ "$RESTORE_COUNT" = "3" ]; then
            pass "PITR restored count = 3 (post-snapshot row gone)"
        else
            fail "PITR restored count expected 3, got '$RESTORE_COUNT'"
        fi

        # Start instance to promote from paused recovery
        run_test "Start after PITR" pg start -i "$INSTANCE"
        sleep 3
    else
        yellow "  Skipping PITR tests (SKIP_PITR=1)"
    fi

    # ---- Test 9: List Instances ----
    section "Test: List Instances"
    run_test "List instances" pg list

    # ---- Test 10: Stop Instance ----
    section "Test: Stop Instance"
    run_test "Stop instance" pg stop -i "$INSTANCE"

    # ---- Test 11: Multi-Instance Isolation ----
    section "Test: Multi-Instance Isolation"
    run_test "Start second instance" pg start -i "$INSTANCE2"
    sleep 3
    run_test "Second instance SQL" sql "$INSTANCE2" "SELECT 1"

    # Verify isolation: second instance should not have the first instance's table
    TABLE_CHECK=$(sqlval "$INSTANCE2" "SELECT count(*) FROM information_schema.tables WHERE table_name = 'e2e_test'")
    if [ "$TABLE_CHECK" = "0" ]; then
        pass "Instance isolation: e2e_test table not present in instance2"
    else
        fail "Instance isolation: e2e_test table leaked into instance2"
    fi

    run_test "Stop second instance" pg stop -i "$INSTANCE2"

    # ---- Test 12: Destroy Instances ----
    section "Test: Destroy Instances"
    run_test "Destroy instance 2" pg destroy -i "$INSTANCE2" --force
    run_test "Destroy instance 1 with data" pg destroy -i "$INSTANCE" --force --clean-data

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then
        green "  ALL $TESTS TESTS PASSED"
    else
        red "  $FAILED/$TESTS TESTS FAILED"
    fi
    echo "=========================================="

    [ "$FAILED" -eq 0 ] || exit 1
}

main
