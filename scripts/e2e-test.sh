#!/usr/bin/env bash
# pgcli end-to-end test script.
# Tests: config init, create, start, status, exec SQL, backup, PITR,
#        export/import files, pipe streaming, clone, --dsn mode, backup
#        container, stop, destroy, multi-instance isolation.
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
    podman exec -i "pgcli-pg-$inst" psql -t -A -U admin -d "${inst}_db" -c "$@"
}

# dsn_of builds a connection string for instance <name> from the config file,
# for testing --dsn modes against local instances.
# yaml.v3 emits instance keys with 4-space indentation.
dsn_of() {
    local name="$1"
    local pass port
    pass=$(awk -v n="    $name:" '$0 == n {f=1; next} f && /^    [^ ]/ {f=0} f && /password:/ {print $2; exit}' "$CONFIG_FILE")
    port=$(awk -v n="    $name:" '$0 == n {f=1; next} f && /^    [^ ]/ {f=0} f && /host_port:/ {print $2; exit}' "$CONFIG_FILE")
    echo "postgres://admin:$pass@127.0.0.1:$port/${name}_db?sslmode=disable"
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"
        return
    fi
    section "Cleanup"
    pg stop -i "$INSTANCE" 2>/dev/null || true
    pg stop -i "$INSTANCE2" 2>/dev/null || true
    pg stop -i "e2e-clone" 2>/dev/null || true
    pg stop -i "e2e-dsnclone" 2>/dev/null || true
    pg destroy -i "$INSTANCE" --force 2>/dev/null || true
    pg destroy -i "$INSTANCE2" --force 2>/dev/null || true
    pg destroy -i "e2e-clone" --force 2>/dev/null || true
    pg destroy -i "e2e-dsnclone" --force 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    # Rootless containers create files with mapped UIDs — need podman unshare
    # to delete leftover data directories (same as setup).
    if [ -d "$TEST_DIR" ]; then
        podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true
    fi
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
        # Create full snapshot and capture the actual pgBackRest name
        CREATE_OUT=$(pg snapshot create -i "$INSTANCE" 2>&1)
        echo "$CREATE_OUT"
        FULL_SNAP=$(echo "$CREATE_OUT" | grep -oP 'Name: \K\S+')
        if [ -n "$FULL_SNAP" ]; then
            pass "Create snapshot: $FULL_SNAP"
        else
            fail "Create snapshot (could not capture name)"
            FULL_SNAP=""
        fi

        run_test "List snapshots" pg snapshot list -i "$INSTANCE"

        # Snapshot delete validation
        run_test "Delete non-existent snapshot fails" \
            bash -c 'out=$(pg snapshot delete nonexistent-snap -i "$INSTANCE" 2>&1); rc=$?; [ "$rc" -ne 0 ] && echo "$out" | grep -q "not found"'

        if [ -n "$FULL_SNAP" ]; then
            run_test "Delete only full backup fails" \
                bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' snapshot delete '$FULL_SNAP' -i '$INSTANCE' 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'only full backup'"
        fi

        # Create diff snapshot and delete it
        DIFF_OUT=$(pg snapshot create --type diff -i "$INSTANCE" 2>&1)
        echo "$DIFF_OUT"
        DIFF_SNAP=$(echo "$DIFF_OUT" | grep -oP 'Name: \K\S+')
        if [ -n "$DIFF_SNAP" ]; then
            run_test "Delete diff snapshot" pg snapshot delete "$DIFF_SNAP" -i "$INSTANCE"
        else
            fail "Create diff snapshot (could not capture name)"
        fi

        # Create incr snapshot and delete it
        INCR_OUT=$(pg snapshot create --type incr -i "$INSTANCE" 2>&1)
        echo "$INCR_OUT"
        INCR_SNAP=$(echo "$INCR_OUT" | grep -oP 'Name: \K\S+')
        if [ -n "$INCR_SNAP" ]; then
            run_test "Delete incr snapshot" pg snapshot delete "$INCR_SNAP" -i "$INSTANCE"
        else
            fail "Create incr snapshot (could not capture name)"
        fi

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

        # Wait for PostgreSQL to be ready after restore
        sleep 5
        for _i in $(seq 1 10); do
            if podman exec "pgcli-pg-$INSTANCE" pg_isready -U admin 2>/dev/null | grep -q "accepting"; then
                break
            fi
            sleep 2
        done

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

    # ---- Test 12: Export to Files ----
    section "Test: Export to Files"
    run_test "Start instance for export" pg start -i "$INSTANCE"
    sleep 3

    run_test "Export custom format" pg export -i "$INSTANCE" -o "$TEST_DIR/export.dump"
    if head -c 4 "$TEST_DIR/export.dump" 2>/dev/null | grep -q "PGDM"; then
        pass "Export file has pg_dump magic bytes"
    else
        fail "Export file missing PGDMP magic bytes"
    fi

    run_test "Export SQL format" pg export -i "$INSTANCE" -o "$TEST_DIR/export.sql"
    if grep -q "CREATE TABLE" "$TEST_DIR/export.sql" 2>/dev/null; then
        pass "SQL dump contains CREATE TABLE"
    else
        fail "SQL dump missing CREATE TABLE"
    fi

    run_test "Export gzip format" pg export -i "$INSTANCE" -o "$TEST_DIR/export.dump.gz"
    run_test "Gzip file is valid" gzip -t "$TEST_DIR/export.dump.gz"

    # ---- Test 13: Import from Files ----
    section "Test: Import from Files"
    run_test "Start second instance for import" pg start -i "$INSTANCE2"
    sleep 3

    run_test "Import custom format" pg import -i "$INSTANCE2" "$TEST_DIR/export.dump"
    COUNT=$(sqlval "$INSTANCE2" "SELECT count(*) FROM e2e_test")
    if [ "$COUNT" = "3" ]; then
        pass "Imported custom count = 3"
    else
        fail "Imported custom count expected 3, got '$COUNT'"
    fi

    sqlval "$INSTANCE2" "DROP TABLE IF EXISTS e2e_test"
    run_test "Import SQL format" pg import -i "$INSTANCE2" "$TEST_DIR/export.sql"
    COUNT=$(sqlval "$INSTANCE2" "SELECT count(*) FROM e2e_test")
    if [ "$COUNT" = "3" ]; then
        pass "Imported SQL count = 3"
    else
        fail "Imported SQL count expected 3, got '$COUNT'"
    fi

    sqlval "$INSTANCE2" "DROP TABLE IF EXISTS e2e_test"
    run_test "Import gzip format" pg import -i "$INSTANCE2" "$TEST_DIR/export.dump.gz"
    COUNT=$(sqlval "$INSTANCE2" "SELECT count(*) FROM e2e_test")
    if [ "$COUNT" = "3" ]; then
        pass "Imported gzip count = 3"
    else
        fail "Imported gzip count expected 3, got '$COUNT'"
    fi

    # ---- Test 14: Pipe Streaming ----
    section "Test: Pipe Streaming"
    run_test "Pipe export to import" bash -c "'$BINARY' -c '$CONFIG_FILE' export -i '$INSTANCE' | '$BINARY' -c '$CONFIG_FILE' import -i '$INSTANCE2' --clean"
    COUNT=$(sqlval "$INSTANCE2" "SELECT count(*) FROM e2e_test")
    if [ "$COUNT" = "3" ]; then
        pass "Pipe transfer count = 3"
    else
        fail "Pipe transfer count expected 3, got '$COUNT'"
    fi

    DSN1=$(dsn_of "$INSTANCE")
    DSN2=$(dsn_of "$INSTANCE2")
    run_test "Pipe export --dsn to import --dsn" bash -c "'$BINARY' -c '$CONFIG_FILE' export --dsn '$DSN1' | '$BINARY' -c '$CONFIG_FILE' import --dsn '$DSN2' --clean"
    COUNT=$(sqlval "$INSTANCE2" "SELECT count(*) FROM e2e_test")
    if [ "$COUNT" = "3" ]; then
        pass "DSN pipe transfer count = 3"
    else
        fail "DSN pipe transfer count expected 3, got '$COUNT'"
    fi

    # ---- Test 15: Clone ----
    section "Test: Clone"
    run_test "Clone target already exists fails" \
        bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' clone '$INSTANCE' -i '$INSTANCE' 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'already exists'"

    run_test "Stop second instance for clone test" pg stop -i "$INSTANCE2"

    run_test "Clone stopped source fails" \
        bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' clone e2e-bad -i '$INSTANCE2' 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'is not running'"

    run_test "Clone unknown source fails" \
        bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' clone e2e-bad2 -i nosuch-inst 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'not found in config'"

    run_test "Clone instance" pg clone e2e-clone -i "$INSTANCE"
    sleep 3
    COUNT=$(sqlval "e2e-clone" "SELECT count(*) FROM e2e_test")
    if [ "$COUNT" = "3" ]; then
        pass "Clone data count = 3"
    else
        fail "Clone data count expected 3, got '$COUNT'"
    fi

    run_test "Clone via --dsn" pg clone e2e-dsnclone --dsn "$DSN1"
    sleep 3
    COUNT=$(sqlval "e2e-dsnclone" "SELECT count(*) FROM e2e_test")
    if [ "$COUNT" = "3" ]; then
        pass "Clone --dsn data count = 3"
    else
        fail "Clone --dsn data count expected 3, got '$COUNT'"
    fi

    run_test "Clone --dsn with -i fails" \
        bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' clone e2e-x --dsn '$DSN1' -i '$INSTANCE' 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'mutually exclusive'"

    # ---- Test 16: --dsn Mode ----
    section "Test: --dsn Mode"
    run_test "exec --dsn SQL" pg exec --dsn "$DSN1" "SELECT 1"
    run_test "psql --dsn one-shot" pg psql --dsn "$DSN1" -- -c "SELECT 1"

    run_test "exec --dsn with -i fails" \
        bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' exec --dsn '$DSN1' -i '$INSTANCE' 'SELECT 1' 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'mutually exclusive'"

    run_test "psql --dsn with -i fails" \
        bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' psql --dsn '$DSN1' -i '$INSTANCE' -- -c 'SELECT 1' 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'mutually exclusive'"

    run_test "export --dsn with -i fails" \
        bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' export --dsn '$DSN1' -i '$INSTANCE' -o '$TEST_DIR/x.dump' 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'mutually exclusive'"

    run_test "import --dsn with -i fails" \
        bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' import --dsn '$DSN1' -i '$INSTANCE' '$TEST_DIR/export.dump' 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'mutually exclusive'"

    run_test "exec --dsn container command fails" \
        bash -c "out=\$('$BINARY' -c '$CONFIG_FILE' exec --dsn '$DSN1' -- -c 'whoami' 2>&1); rc=\$?; [ \"\$rc\" -ne 0 ] && echo \"\$out\" | grep -q 'only supports SQL mode'"

    run_test "export --dsn to file" pg export --dsn "$DSN1" -o "$TEST_DIR/dsn-export.dump"
    if head -c 4 "$TEST_DIR/dsn-export.dump" 2>/dev/null | grep -q "PGDM"; then
        pass "DSN export file has pg_dump magic bytes"
    else
        fail "DSN export file missing PGDMP magic bytes"
    fi

    run_test "Start second instance for import --dsn" pg start -i "$INSTANCE2"
    sleep 3
    run_test "import --dsn from file" pg import --dsn "$DSN2" "$TEST_DIR/dsn-export.dump" --clean
    COUNT=$(sqlval "$INSTANCE2" "SELECT count(*) FROM e2e_test")
    if [ "$COUNT" = "3" ]; then
        pass "DSN import count = 3"
    else
        fail "DSN import count expected 3, got '$COUNT'"
    fi

    # ---- Test 17: Backup Container ----
    section "Test: Backup Container"
    run_test "Backup setup" pg backup setup --base-dir "$TEST_DIR"
    run_test "Backup status shows container" bash -c "'$BINARY' -c '$CONFIG_FILE' backup status 2>&1 | grep -q pgcli-backup"
    run_test "Backup stop" pg backup stop
    run_test "Backup start" pg backup start
    run_test "Backup status running" bash -c "'$BINARY' -c '$CONFIG_FILE' backup status 2>&1 | grep -qE 'Status:.*Up'"

    # ---- Test 18: Destroy Instances ----
    section "Test: Destroy Instances"
    run_test "Destroy clone instance" pg destroy -i "e2e-clone" --force
    run_test "Destroy dsn clone instance" pg destroy -i "e2e-dsnclone" --force
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
