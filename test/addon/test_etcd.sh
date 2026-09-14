#!/usr/bin/env bash
# pgcli etcd addon end-to-end test (two members).
# Drives the full cluster lifecycle and does all queries AND reads/writes
# through `pg etcdctl` (the temporary-container etcdctl helper) rather than
# exec'ing into members:
#   config init (isolated namespace + etcd port range), member m1
#   bootstrapping the cluster, member m2 joining it (auto-registered with the
#   running peer), two-node quorum via `member list` / `endpoint health`,
#   put/get/del round-trips visible on BOTH members (replication), an
#   ETCDCTL_ENDPOINTS override, stop ✓ output + degraded-health of one member
#   with quorum intact on the other, start + data recovery, idempotent
#   re-install, list, and per-member remove with cluster deregistration.
#
# Isolated like test/e2e-test.sh: throwaway config file + base dir, own
# --namespace, and an --etcd-start-port range far from the developer's real
# ~/.pgcli (whose members hold 2379/2380). autoAssignPorts also probes live
# ports, so even a colliding range would skip busy ports, not clobber them.
#
# Usage:
#   bash test/addon/test_etcd.sh                 # full test, cleans up
#   bash test/addon/test_etcd.sh --skip-destroy  # keep the cluster after
#   PG_BINARY=/usr/local/bin/pg bash test/addon/test_etcd.sh
#   PGCLI_NAMESPACE=x1 PGCLI_ETCD_START_PORT=24790 bash test/addon/test_etcd.sh
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-etcd-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-etcd-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-ete2e}"
ETCD_START_PORT="${PGCLI_ETCD_START_PORT:-24790}"
M1="m1"
M2="m2"
CLUSTER="e2ectl"

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
# run_grep <desc> <pattern> <cmd...> — succeed when output matches (fixed string)
run_grep() {
    local desc="$1" pat="$2"; shift 2
    TESTS=$((TESTS + 1))
    local out
    if out=$("$@" 2>&1) && echo "$out" | grep -qF -e "$pat"; then
        pass "$desc"
    else
        fail "$desc (want '$pat')"
        echo "$out" | sed 's/^/      | /'
    fi
}
# run_not_grep <desc> <pattern> <cmd...> — succeed when output does NOT match
run_not_grep() {
    local desc="$1" pat="$2"; shift 2
    TESTS=$((TESTS + 1))
    local out
    if out=$("$@" 2>&1) && ! echo "$out" | grep -qF -e "$pat"; then
        pass "$desc"
    else
        fail "$desc (must not contain '$pat')"
        echo "$out" | sed 's/^/      | /'
    fi
}

pg() { "$BINARY" -c "$CONFIG_FILE" "$@"; }

# ectl <member-endpoint-key> <etcdctl args...> — run `pg etcdctl` against one
# member's client URL read from the config (addons.etcd.<name>.client_port is
# not stored per member as a URL, so resolve the port from the yaml).
ectl() {
    local member="$1"; shift
    local port
    port="$(addon_field "$member" client_port)"
    ETCDCTL_ENDPOINTS="http://127.0.0.1:$port" pg etcdctl "$@"
}

# addon_field <etcd-name> <key> — value under addons.etcd.<name> (8-space key).
addon_field() {
    awk -v n="        $1:" -v k="$2:" '
        $0 == n {f=1; next}
        f && /^        [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {gsub(/^[[:space:]]*[a-z_]+: /,""); print; exit}
    ' "$CONFIG_FILE"
}

etcd_up() {  # <member> — container running
    podman ps --filter "name=pgcli-etcd-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-etcd-$NAMESPACE-$1"
}
etcd_down() { ! etcd_up "$1"; }
etcd_gone() {
    ! podman ps -a --filter "name=pgcli-etcd-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}
wait_healthy() {  # <member> — poll endpoint health for this member
    local port
    port="$(addon_field "$1" client_port)"
    for _ in $(seq 1 30); do
        if ETCDCTL_ENDPOINTS="http://127.0.0.1:$port" pg etcdctl endpoint health 2>&1 \
                | grep -q 'is healthy'; then
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
    pg addon remove etcd --name "$M2" 2>/dev/null || true
    pg addon remove etcd --name "$M1" 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli etcd Addon Test (2 members, pg etcdctl)"
    echo "=========================================="
    echo "  Binary:     $BINARY"
    echo "  Test dir:   $TEST_DIR"
    echo "  Config:     $CONFIG_FILE"
    echo "  Namespace:  $NAMESPACE"
    echo "  Etcd ports: $ETCD_START_PORT+ (client/peer per member)"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-etcd-$NAMESPACE-" \
            --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    mkdir -p "$CONFIG_DIR" "$TEST_DIR"

    # config init has no etcd-port flag; set the pool start in the yaml so
    # ports never touch the real ~/.pgcli etcd (2379/2380).
    run_test "config init (isolated ns)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" --namespace "$NAMESPACE"
    sed -i "s/^etcd_start_port:.*/etcd_start_port: $ETCD_START_PORT/" "$CONFIG_FILE"
    run_grep "etcd_start_port applied" "etcd_start_port: $ETCD_START_PORT" \
        cat "$CONFIG_FILE"

    # ---- First member bootstraps the cluster ----
    section "Bootstrap m1"
    run_test "install etcd --name $M1" \
        pg addon install etcd --name "$M1" --cluster "$CLUSTER"
    P1="$(addon_field "$M1" client_port)"
    PP1="$(addon_field "$M1" peer_port)"
    if [ -n "$P1" ] && [ -n "$PP1" ]; then pass "m1 ports assigned: client $P1 / peer $PP1"
    else fail "could not read m1 ports from config"; fi
    run_test "m1 container running" etcd_up "$M1"
    run_test "m1 default image is coreos/etcd" grep -q 'quay.io/coreos/etcd' "$CONFIG_FILE"
    run_test "m1 data dir created" \
        test -d "$TEST_DIR/addon/etcd/$M1/data"
    run_grep "m1 endpoint healthy" "is healthy" ectl "$M1" endpoint health

    # ---- Second member joins ----
    section "Join m2"
    run_grep "install etcd --name $M2 (registers with running peer)" \
        "Registering new member" \
        pg addon install etcd --name "$M2" --cluster "$CLUSTER"
    P2="$(addon_field "$M2" client_port)"
    if [ -n "$P2" ] && [ "$P1" != "$P2" ]; then pass "m2 client port distinct: $P2"
    else fail "m2 port bad: '$P2' (m1 was $P1)"; fi
    run_test "m2 container running" etcd_up "$M2"
    run_test "m2 endpoint healthy" wait_healthy "$M2"

    # Two-node cluster visible through pg etcdctl (member list is tabular:
    # ID, started, name, peer URLs, client URLs — match the bare name column).
    run_grep "member list shows m1"    "m1" ectl "$M1" member list
    run_grep "member list shows m2"    "m2" ectl "$M1" member list
    run_grep "member list via m2 too"  "m1" ectl "$M2" member list
    run_grep "cluster name in config" "$CLUSTER" \
        grep -o "cluster_name: $CLUSTER" "$CONFIG_FILE"

    # ---- Write/read round-trips via pg etcdctl ----
    section "etcdctl read/write"
    run_grep "put key" "OK"  ectl "$M1" put e2e/hello world
    run_grep "get key (same member)"  "world" ectl "$M1" get e2e/hello
    # Replicated to the other member — read the SAME key there.
    run_grep "get key (other member)" "world" ectl "$M2" get e2e/hello
    run_grep "put second key" "OK"  ectl "$M2" put e2e/foo bar
    run_grep "range get by prefix" "e2e/foo" ectl "$M1" get e2e/ -- --prefix
    run_grep "get --print-value-only" "bar" \
        ectl "$M1" get e2e/foo -- --print-value-only
    run_test "del key" ectl "$M1" del e2e/foo
    run_not_grep "deleted key is gone" "bar" ectl "$M2" get e2e/foo -- --print-value-only

    # ---- ETCDCTL_ENDPOINTS override (documented usage) ----
    section "Endpoint override"
    run_grep "explicit ETCDCTL_ENDPOINTS hits m2" "e2e/hello" \
        env "ETCDCTL_ENDPOINTS=http://127.0.0.1:$P2" pg etcdctl get e2e/hello
    run_test "default endpoint is first member (m1, no override)" \
        bash -c "'$BINARY' -c '$CONFIG_FILE' etcdctl get e2e/hello | grep -q world"

    # ---- stop / start ----
    section "Stop / start"
    STOP_OUT="$(pg addon stop etcd --name "$M2" 2>&1)"
    echo "$STOP_OUT"
    if echo "$STOP_OUT" | grep -q 'stopped'; then pass "stop prints a confirmation"
    else fail "stop printed no confirmation"; fi
    run_test "m2 container stopped" etcd_down "$M2"
    # A 2-member raft group needs both votes: with m2 down, m1 cannot commit
    # and reports unhealthy. Quorum returns as soon as m2 starts again.
    TESTS=$((TESTS + 1))
    HOUT="$(ectl "$M1" endpoint health 2>&1)" && rc=0 || rc=$?
    if [ "$rc" -ne 0 ] && echo "$HOUT" | grep -qF 'is unhealthy'; then
        pass "m1 unhealthy while m2 down (2-node quorum lost)"
    else
        fail "m1 should be unhealthy with m2 down (rc=$rc): $HOUT"
    fi
    run_test "start etcd $M2" pg addon start etcd --name "$M2"
    run_test "m2 running again" etcd_up "$M2"
    run_test "m2 healthy again" wait_healthy "$M2"
    run_grep "data survived restart" "world" ectl "$M2" get e2e/hello -- --print-value-only

    # ---- Idempotent re-install ----
    section "Re-install (idempotent)"
    run_test "reinstall m1 (reuse ports)" pg addon install etcd --name "$M1" --cluster "$CLUSTER"
    P1B="$(addon_field "$M1" client_port)"
    if [ "$P1" = "$P1B" ]; then pass "m1 client port stable across re-install ($P1)"
    else fail "m1 port changed: $P1 -> $P1B"; fi
    run_test "m1 healthy after re-install" wait_healthy "$M1"
    run_grep "cluster data readable after re-install" "world" ectl "$M1" get e2e/hello

    # ---- list ----
    section "List"
    run_grep "addon list shows m1" "etcd (name: $M1)" pg addon list
    run_grep "addon list shows m2" "etcd (name: $M2)" pg addon list
    run_grep "addon list reports running" "Status:      running" pg addon list

    # ---- Remove (deregisters from the running peer) ----
    section "Remove"
    run_test "remove m2" pg addon remove etcd --name "$M2"
    run_test "m2 container gone" etcd_gone "$M2"
    run_test "m2 data dir removed" bash -c "test ! -d '$TEST_DIR/addon/etcd/$M2'"
    # m1 is the surviving quorum and must no longer list m2.
    run_test "m1 healthy after m2 removal" wait_healthy "$M1"
    run_not_grep "member list drops m2" "m2" ectl "$M1" member list
    run_test "remove m1 (last member)" pg addon remove etcd --name "$M1"
    run_test "m1 container gone" etcd_gone "$M1"
    run_test "yaml etcd entries removed" bash -c "! grep -q 'etcd:' '$CONFIG_FILE'"

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
