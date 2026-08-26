#!/usr/bin/env bash
# pgcli end-to-end test script.
# Tests: create, start, status, backup, PITR, stop, destroy, multi-instance.
#
# Usage:
#   bash scripts/e2e-test.sh                 # full test
#   bash scripts/e2e-test.sh --skip-destroy  # keep containers after test
#   SKIP_PITR=1 bash scripts/e2e-test.sh     # skip PITR tests (faster)
set -euo pipefail

BINARY="${PG_BINARY:-./bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/home/fish/bucket/pgcli-data}"
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

pg() {
    "$BINARY" -c "$CONFIG_FILE" "$@"
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

    # Setup config
    section "Setup"
    mkdir -p "$CONFIG_DIR"
    mkdir -p "$TEST_DIR"

    cat > "$CONFIG_FILE" << EOF
base_dir: "$CONFIG_DIR"
network: pgcli-e2e-net

postgres:
  user: pgcli_user
  password: pgcli_pass
  database: pgcli_db

podman:
  image_tag: "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0"
  network: pgcli-e2e-net

pitr:
  enabled: true
  pgbackrest_stanza: pgcli_e2e

backup:
  container_name: pgcli-e2e-backup
  image_tag: "ghcr.io/mars-base/pgcli/pgcli-backup:2.58.0"
  data_dir: "$TEST_DIR/backup-data"
  log_dir: "$TEST_DIR/backup-log"
  retention_full: 2

logging:
  level: info

instances:
  $INSTANCE:
    postgres:
      port: 25432
      user: pgcli_user
      password: pgcli_pass
      database: pgcli_db
    podman:
      container_name: pgcli-pg-$INSTANCE
      host_port: 25432
      ssh_port: 32201
      data_dir: "$TEST_DIR/$INSTANCE"
      image_tag: "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0"
      network: pgcli-e2e-net
    pitr:
      enabled: true
      pgbackrest_stanza: pgcli_e2e_test
EOF

    pass "Config created"

    # Test 1: Create instance
    section "Test: Create Instance"
    run_test "Create instance" pg create "$INSTANCE" --base-dir "$TEST_DIR/$INSTANCE"

    # Test 2: Start instance
    section "Test: Start Instance"
    run_test "Start instance" pg start -i "$INSTANCE"

    # Test 3: Status check
    section "Test: Status"
    run_test "Status shows running" pg status -i "$INSTANCE"

    # Test 4: PG health check via psql
    section "Test: Database Connectivity"
    sleep 3  # extra wait for PG to be fully ready
    run_test "psql SELECT 1" pg exec -i "$INSTANCE" -- psql -U pgcli_user -d pgcli_db -tAc "SELECT 1"

    # Test 5: Create table and insert data
    section "Test: Data Operations"
    run_test "CREATE TABLE" pg exec -i "$INSTANCE" -- psql -U pgcli_user -d pgcli_db -c \
        "CREATE TABLE IF NOT EXISTS e2e_test (id serial PRIMARY KEY, msg text, ts timestamptz DEFAULT now())"
    run_test "INSERT data" pg exec -i "$INSTANCE" -- psql -U pgcli_user -d pgcli_db -c \
        "INSERT INTO e2e_test (msg) VALUES ('before-backup'), ('row-2'), ('row-3')"
    run_test "SELECT verify" pg exec -i "$INSTANCE" -- psql -U pgcli_user -d pgcli_db -tAc \
        "SELECT count(*) FROM e2e_test"

    # Test 6: Snapshot (backup)
    section "Test: Backup / Snapshot"
    if [ "$SKIP_PITR" != "1" ]; then
        run_test "Create snapshot" pg snapshot create -i "$INSTANCE" e2e-snap-1
        run_test "List snapshots" pg snapshot list -i "$INSTANCE"
    else
        yellow "  Skipping PITR/backup tests (SKIP_PITR=1)"
    fi

    # Test 7: Record timestamp, insert more data, then PITR
    section "Test: PITR Recovery"
    if [ "$SKIP_PITR" != "1" ]; then
        PITR_TIME=$(pg exec -i "$INSTANCE" -- psql -U pgcli_user -d pgcli_db -tAc "SELECT now()")
        PITR_TIME="$(echo "$PITR_TIME" | xargs)"
        sleep 5

        run_test "Insert post-snapshot data" pg exec -i "$INSTANCE" -- psql -U pgcli_user -d pgcli_db -c \
            "INSERT INTO e2e_test (msg) VALUES ('after-snapshot-should-be-gone')"

        run_test "PITR restore (dry-run)" pg restore -i "$INSTANCE" --time "$PITR_TIME" --dry-run
        run_test "PITR restore" pg restore -i "$INSTANCE" --time "$PITR_TIME" --force --tail-logs

        # After restore with pause, verify data then resume
        run_test "Verify pre-snapshot data exists" pg exec -i "$INSTANCE" -- psql -U pgcli_user -d pgcli_db -tAc \
            "SELECT count(*) FROM e2e_test WHERE msg = 'before-backup'"

        # Start instance to promote from paused recovery
        run_test "Start after PITR" pg start -i "$INSTANCE"
        sleep 3

        # Test 8: List command
        section "Test: List Instances"
        run_test "List instances" pg list
    else
        yellow "  Skipping PITR tests (SKIP_PITR=1)"
    fi

    # Test 9: Stop instance
    section "Test: Stop Instance"
    run_test "Stop instance" pg stop -i "$INSTANCE"

    # Test 10: Multi-instance — create a second instance
    section "Test: Multi-Instance Isolation"

    # Add second instance to config
    cat >> "$CONFIG_FILE" << EOF
  $INSTANCE2:
    postgres:
      port: 25433
      user: pgcli_user
      password: pgcli_pass
      database: pgcli_db
    podman:
      container_name: pgcli-pg-$INSTANCE2
      host_port: 25433
      ssh_port: 32202
      data_dir: "$TEST_DIR/$INSTANCE2"
      image_tag: "ghcr.io/mars-base/pgcli/pgcli-pg:18-2.58.0"
      network: pgcli-e2e-net
    pitr:
      enabled: false
EOF

    run_test "Create second instance" pg create "$INSTANCE2" --base-dir "$TEST_DIR/$INSTANCE2"
    run_test "Start second instance" pg start -i "$INSTANCE2"
    sleep 3
    run_test "Second instance psql" pg exec -i "$INSTANCE2" -- psql -U pgcli_user -d pgcli_db -tAc "SELECT 1"
    run_test "Stop second instance" pg stop -i "$INSTANCE2"

    # Test 11: Destroy
    section "Test: Destroy Instances"
    run_test "Destroy instance 2" pg destroy -i "$INSTANCE2" --force
    run_test "Destroy instance 1 with data" pg destroy -i "$INSTANCE" --force --clean

    # Summary
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
