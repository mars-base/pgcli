#!/usr/bin/env bash
# pgcli Redis addon — native-CLUSTER end-to-end test (single host, 3 masters).
#
# The cluster feature is deliberately a TWO-STAGE, passthrough-assembly design:
# pgcli installs cluster-enabled nodes (--cluster <token>, --advertise-host for
# cross-host) but NEVER runs `--cluster create` itself. You assemble the group
# once with `pg redis-cli -- --cluster create ...`. This script drives exactly
# that flow end-to-end on one host (so it is the single-host slice of the
# feature; cross-host --advertise-host is verified by hand VM01<->VM02, not here
# — the same reason test_minio_cluster.sh is a separate file from test_minio.sh,
# and why we do not append these cases to the 38KB test_redis.sh):
#
#   1. INSTALL N MASTERS — each --cluster app --maxmemory 10mb; assert each
#      container carries --cluster-enabled, each pg.yaml entry carries
#      cluster: app, and ALL share ONE password (the load-bearing invariant:
#      --cluster create authenticates every operand node with one password).
#   2. ASSEMBLE — `pg redis-cli --name n1 -- --cluster create ...` covers all
#      16384 slots; cluster_state:ok. Proves the passthrough injects the shared
#      password via REDISCLI_AUTH (no -a typed) and that --cluster ignores the
#      -h/-p pg redis-cli always injects.
#   3. REDIRECTS — a -c client (auto-injected for cluster nodes) follows MOVED:
#      the same key round-trips through every master regardless of the owner.
#   4. SELF-HEAL — stop+start all three; the cluster reforms from each node's
#      nodes.conf with NO re-create (pgcli start is start-only, like etcd).
#   5. GUARDS — --cluster + --replica-of is rejected; a later member whose
#      --password disagrees with the group is rejected and leaves no config.
#
# Usage:
#   bash test/addon/test_redis_cluster.sh                 # full test, cleans up
#   bash test/addon/test_redis_cluster.sh --skip-destroy   # keep the cluster
#   PG_BINARY=/path/to/pg bash test/addon/test_redis_cluster.sh
#   PGCLI_NAMESPACE=x1 PGCLI_REDIS_START_PORT=36479 bash test/addon/test_redis_cluster.sh
#
# Requires the redis 8 image locally (podman load from the tar distribution) or
# outbound docker.io access — pg pulls on demand otherwise.
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-rce2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-rce2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-rce2e}"
REDIS_START_PORT="${PGCLI_REDIS_START_PORT:-36479}"
CLUSTER="app"
N1="n1"; N2="n2"; N3="n3"
# Second scenario: one cluster WITH per-master followers (--cluster-replicas 1).
CLUSTER2="app2"
# 3 masters + 3 followers = 6 nodes. Redis assigns roles at create time; the
# pgcli names here are intentionally role-agnostic (all installed alike).
M1="m1"; M2="m2"; M3="m3"; F1="f1"; F2="f2"; F3="f3"

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

pg() { "$BINARY" -c "$CONFIG_FILE" "$@"; }

# addon_field <name> <key> — value under addons.redis.<name> (8-space key).
addon_field() {
    awk -v n="        $1:" -v k="$2:" '
        $0 == n {f=1; next}
        f && /^        [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {
            gsub(/^[[:space:]]*[a-z_]+: /,""); gsub(/^"|"$/,""); print; exit
        }
    ' "$CONFIG_FILE"
}
redis_field() { addon_field "$1" "$2"; }

rd_up() {
    podman ps --filter "name=pgcli-redis-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-redis-$NAMESPACE-$1"
}
rd_gone() {
    ! podman ps -a --filter "name=pgcli-redis-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}
rd_cmd() { podman inspect "pgcli-redis-$NAMESPACE-$1" --format '{{json .Config.Cmd}}'; }
rd_has() { rd_cmd "$1" | grep -qF -- "$2"; }
# rd_has_pair <name> <flag> <value> — the argv is a JSON array, so a two-slot
# flag/value pair ("--cluster-enabled","yes") never appears as one space-joined
# substring; match across the real separator instead (same trick test_redis.sh
# uses for the empty-valued --save).
rd_has_pair() { rd_cmd "$1" | grep -qE -- "\"$2\",[[:space:]]*\"$3\""; }

# cluster_state <name> — cluster_state line via a -c (cluster) client.
cluster_state() {
    pg redis-cli --name "$1" cluster info 2>/dev/null | tr -d '\r' \
        | awk -F: '/^cluster_state:/{print $2; exit}'
}
wait_cluster_ok() {
    for _ in $(seq 1 30); do
        if [ "$(cluster_state "$1")" = "ok" ]; then return 0; fi
        sleep 1
    done
    return 1
}
# wait_link_up <follower-name> — a cluster follower must reach
# master_link_status:up. Without --masterauth it stays :down forever and spins
# in a reconnect storm (the exact bug this guards against).
wait_link_up() {
    for _ in $(seq 1 20); do
        if pg redis-cli --name "$1" info replication 2>/dev/null | tr -d '\r' \
                | grep -q '^master_link_status:up'; then return 0; fi
        sleep 1
    done
    return 1
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    for c in "$N1" "$N2" "$N3" "$M1" "$M2" "$M3" "$F1" "$F2" "$F3" badrep badpw; do
        pg addon remove redis --name "$c" --clean-data >/dev/null 2>&1 || true
    done
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli Redis Native-Cluster Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Cluster:   $CLUSTER (3 masters)"
    echo "  Ports:     redis pool $REDIS_START_PORT+ (bus = client+10000)"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-redis-$NAMESPACE-" \
                    --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    mkdir -p "$CONFIG_DIR" "$TEST_DIR"
    run_test "config init (isolated ns)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" --namespace "$NAMESPACE"
    sed -i "s/^redis_start_port:.*/redis_start_port: $REDIS_START_PORT/" "$CONFIG_FILE"

    # ---- Install the three masters (NOT yet assembled) ----
    section "Install 3 cluster masters (--cluster $CLUSTER, --maxmemory 10mb)"
    for n in "$N1" "$N2" "$N3"; do
        run_test "install redis --name $n --cluster $CLUSTER" \
            pg addon install redis --name "$n" --cluster "$CLUSTER" --maxmemory 10mb
    done

    P1="$(redis_field "$N1" port)"; P2="$(redis_field "$N2" port)"; P3="$(redis_field "$N3" port)"
    PW1="$(redis_field "$N1" password)"; PW2="$(redis_field "$N2" password)"; PW3="$(redis_field "$N3" password)"

    run_test "three distinct ports from one pool" \
        test "$P1" != "$P2" -a "$P2" != "$P3" -a "$P1" != "$P3"
    run_test "all up" rd_up "$N1" ; run_test "n2 up" rd_up "$N2" ; run_test "n3 up" rd_up "$N3"

    # Cluster role is persisted and reaches the argv (cluster-enabled) on every
    # member — this is what `pg addon start` rebuilds from config later.
    for n in "$N1" "$N2" "$N3"; do
        run_grep "$n config records cluster: $CLUSTER" "cluster: $CLUSTER" \
            awk -v nn="        $n:" 'f&&/^[[:space:]]+cluster:/{print;exit} $0==nn{f=1}' "$CONFIG_FILE"
        run_test "$n container is --cluster-enabled" rd_has_pair "$n" --cluster-enabled yes
        run_test "$n keeps nodes.conf under --dir" rd_has "$n" "--cluster-config-file"
        run_test "$n caps memory at 10mb" rd_has_pair "$n" --maxmemory 10mb
        # Regression: cluster members must carry --masterauth even when
        # masters-only. Redis may promote any of them to a follower at create
        # time, and a follower with no masterauth can never auth to its master —
        # it spins in a reconnect storm (the CPU bug this guards). Password is
        # shared, so masterauth == the group password PW1.
        run_test "$n container carries --masterauth" rd_has_pair "$n" --masterauth "$PW1"
    done

    # The load-bearing invariant: one shared password across the whole group, so
    # a single REDISCLI_AUTH authenticates every node during --cluster create.
    run_test "all three share one password (inherited from the first member)" \
        test "$PW1" = "$PW2" -a "$PW2" = "$PW3" -a -n "$PW1"

    # ---- Assemble ONCE, through the pg redis-cli passthrough (no -a typed) ----
    section "Assemble: pg redis-cli -- --cluster create (passthrough)"
    CREATE_OUT="$(pg redis-cli --name "$N1" -- --cluster create \
        "127.0.0.1:$P1" "127.0.0.1:$P2" "127.0.0.1:$P3" \
        --cluster-replicas 0 --cluster-yes 2>&1)" || true
    echo "$CREATE_OUT" | sed 's/^/      | /' | tail -12
    run_grep "create covered all 16384 slots" "All 16384 slots covered" \
        printf '%s' "$CREATE_OUT"
    run_test "cluster_state:ok after assemble" wait_cluster_ok "$N1"

    # ---- MOVED redirects: a -c client follows to the owner from any master ----
    section "Redirects: -c client reaches the slot owner from every node"
    TESTKEY="rce2e:key"; TESTVAL="clustered"
    run_test "set via n1 (pg redis-cli auto-injects -c)" \
        sh -c "\"$BINARY\" -c \"$CONFIG_FILE\" redis-cli --name $N1 set $TESTKEY $TESTVAL | grep -q OK"
    for n in "$N2" "$N3"; do
        TESTS=$((TESTS + 1))
        if [ "$("$BINARY" -c "$CONFIG_FILE" redis-cli --name "$n" get "$TESTKEY" 2>/dev/null)" = "$TESTVAL" ]; then
            pass "get from $n returns the value (MOVED followed)"
        else
            fail "get from $n did not return '$TESTVAL'"
        fi
    done

    # ---- Self-heal: stop+start all three, no re-create (nodes.conf) ----
    section "Self-heal across stop/start (nodes.conf, start-only)"
    for n in "$N1" "$N2" "$N3"; do pg addon stop redis --name "$n" >/dev/null 2>&1 || true; done
    for n in "$N1" "$N2" "$N3"; do pg addon start redis --name "$n" >/dev/null 2>&1 || true; done
    run_test "cluster reforms without any re-create" wait_cluster_ok "$N1"
    TESTS=$((TESTS + 1))
    if [ "$("$BINARY" -c "$CONFIG_FILE" redis-cli --name "$N2" get "$TESTKEY" 2>/dev/null)" = "$TESTVAL" ]; then
        pass "the written key survived the stop/start cycle"
    else
        fail "key lost across restart (nodes.conf/RDB not reloaded)"
    fi

    # ---- Guards ----
    section "Guards (validation before any pull/create)"
    run_fails "--cluster + --replica-of is rejected" "mutually exclusive" \
        pg addon install redis --name badrep --cluster "$CLUSTER" --replica-of "$N1"
    run_not_grep "rejected member never entered the config" "badrep:" cat "$CONFIG_FILE"
    run_fails "a later member with a mismatched --password is rejected" "share the group" \
        pg addon install redis --name badpw --cluster "$CLUSTER" --password definitely-not-the-group-password
    run_not_grep "mismatched-password member never entered the config" "badpw:" cat "$CONFIG_FILE"
    # A re-pinned matching password is legal — the operator echoing back the
    # group's own secret must not be treated as a mismatch.
    run_test "re-adding n2 with the SAME password is accepted" \
        pg addon install redis --name "$N2" --cluster "$CLUSTER" --password "$PW1"

    # ---- pg addon list surfaces the cluster ----
    section "pg addon list shows the cluster"
    run_grep "list marks each node a cluster member" "cluster member of \"$CLUSTER\"" \
        pg addon list
    run_grep "list shows the Cluster: line with the bus port" "Cluster:     $CLUSTER (bus" \
        pg addon list

    # ==================================================================
    # SCENARIO 2 — a cluster WITH per-master followers (--cluster-replicas 1)
    #
    # 6 nodes, assembled with --cluster-replicas 1, so Redis elects 3 masters +
    # 3 followers. This is the regression that motivated the whole scenario: a
    # follower must carry --masterauth or it can never authenticate to its
    # master and spins in a reconnect storm (master_link_status stays :down,
    # CPU pinned). pgcli installs every node identically — the follower role is
    # Redis's decision at create time — so we never name members "replica".
    # ==================================================================
    section "Scenario 2: 3 masters x 1 follower (--cluster-replicas 1)"

    # Free memory first: tear the masters-only cluster down so the 6-node cluster
    # (2 GB VM) does not run alongside the 3-node one.
    for n in "$N1" "$N2" "$N3"; do
        pg addon remove redis --name "$n" --clean-data >/dev/null 2>&1 || true
    done

    # First member pins the group's per-master replica count; the rest inherit it.
    run_test "install m1 --cluster $CLUSTER2 --cluster-replicas 1" \
        pg addon install redis --name "$M1" --cluster "$CLUSTER2" --cluster-replicas 1 --maxmemory 10mb
    for n in "$M2" "$M3" "$F1" "$F2" "$F3"; do
        run_test "install $n --cluster $CLUSTER2 (inherits replicas)" \
            pg addon install redis --name "$n" --cluster "$CLUSTER2" --maxmemory 10mb
    done

    # The group's ClusterReplicas is persisted (first member set it, rest inherit).
    run_grep "m1 config records cluster_replicas: 1" "cluster_replicas: 1" \
        awk -v nn="        $M1:" 'f&&/^[[:space:]]+cluster_replicas:/{print;exit} $0==nn{f=1}' "$CONFIG_FILE"
    run_grep "f3 config INHERITED cluster_replicas: 1" "cluster_replicas: 1" \
        awk -v nn="        $F3:" 'f&&/^[[:space:]]+cluster_replicas:/{print;exit} $0==nn{f=1}' "$CONFIG_FILE"

    # Summary counts NODES and echoes the replica hint (never "6 masters").
    S2_LAST="$(pg addon install redis --name "$F3" --cluster "$CLUSTER2" --maxmemory 10mb 2>&1)" || true
    run_grep "summary counts 6 nodes configured" "6 nodes configured" printf '%s' "$S2_LAST"
    run_not_grep "summary does not mislabel all 6 as masters" "6 masters configured" printf '%s' "$S2_LAST"
    run_grep "summary echoes --cluster-replicas 1 into the create cmd" "--cluster-replicas 1" printf '%s' "$S2_LAST"

    # Every one of the 6 members carries --masterauth (the regression guard).
    S2_PW="$(redis_field "$M1" password)"
    for n in "$M1" "$M2" "$M3" "$F1" "$F2" "$F3"; do
        run_test "$n container carries --masterauth" rd_has_pair "$n" --masterauth "$S2_PW"
    done

    # ---- Assemble the 6-node group with --cluster-replicas 1 ----
    section "Assemble scenario 2: --cluster create ... --cluster-replicas 1"
    MP1="$(redis_field "$M1" port)"; MP2="$(redis_field "$M2" port)"; MP3="$(redis_field "$M3" port)"
    FP1="$(redis_field "$F1" port)"; FP2="$(redis_field "$F2" port)"; FP3="$(redis_field "$F3" port)"
    CREATE2_OUT="$(pg redis-cli --name "$M1" -- --cluster create \
        "127.0.0.1:$MP1" "127.0.0.1:$MP2" "127.0.0.1:$MP3" \
        "127.0.0.1:$FP1" "127.0.0.1:$FP2" "127.0.0.1:$FP3" \
        --cluster-replicas 1 --cluster-yes 2>&1)" || true
    echo "$CREATE2_OUT" | sed 's/^/      | /' | tail -14
    run_grep "create (replicas) covered all 16384 slots" "All 16384 slots covered" \
        printf '%s' "$CREATE2_OUT"
    run_test "cluster_state:ok after replica assemble" wait_cluster_ok "$M1"

    # ---- The point of the scenario: followers actually link to their masters ----
    section "Followers link up (the --masterauth reconnect-storm regression)"
    TESTS=$((TESTS + 1))
    slaves=0; linked=0
    for n in "$M1" "$M2" "$M3" "$F1" "$F2" "$F3"; do
        role="$(pg redis-cli --name "$n" info replication 2>/dev/null | tr -d '\r' \
                | awk -F: '/^role:/{print $2; exit}')"
        if [ "$role" = "slave" ]; then
            slaves=$((slaves + 1))
            if wait_link_up "$n"; then linked=$((linked + 1)); fi
        fi
    done
    if [ "$slaves" -eq 3 ] && [ "$linked" -eq 3 ]; then
        pass "all 3 followers reached master_link_status:up (auth via --masterauth)"
    else
        fail "expected 3 followers all linked; got slaves=$slaves linked=$linked"
    fi

    # ---- Data replicates: a follower is caught up to its master ----
    TESTS=$((TESTS + 1))
    pg redis-cli --name "$M1" set rce2e:k2 replicated >/dev/null 2>&1 || true
    sleep 1
    # The -c client reads via the master; assert the value round-trips across the
    # cluster (the follower link-up above is the real auth regression guard).
    got="$(pg redis-cli --name "$M2" get rce2e:k2 2>/dev/null)"
    if [ "$got" = "replicated" ]; then
        pass "written key readable through the replicated cluster"
    else
        fail "key read returned '$got', want 'replicated'"
    fi

    # ---- Self-heal the replicated cluster: stop+start all 6, NO re-create ----
    section "Scenario 2 self-heal across stop/start (followers re-link)"
    for n in "$M1" "$M2" "$M3" "$F1" "$F2" "$F3"; do pg addon stop redis --name "$n" >/dev/null 2>&1 || true; done
    for n in "$M1" "$M2" "$M3" "$F1" "$F2" "$F3"; do pg addon start redis --name "$n" >/dev/null 2>&1 || true; done
    run_test "replicated cluster reforms without re-create" wait_cluster_ok "$M1"
    TESTS=$((TESTS + 1))
    linked2=0
    for n in "$M1" "$M2" "$M3" "$F1" "$F2" "$F3"; do
        pg redis-cli --name "$n" info replication 2>/dev/null | tr -d '\r' \
            | grep -q '^role:slave' || continue
        if wait_link_up "$n"; then linked2=$((linked2 + 1)); fi
    done
    if [ "$linked2" -ge 1 ]; then
        pass "followers re-linked after restart ($linked2 up)"
    else
        fail "no follower re-linked after restart (nodes.conf did not restore roles/auth)"
    fi

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
