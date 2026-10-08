#!/usr/bin/env bash
# pgcli Predixy addon — Redis-cluster-proxy end-to-end test (single host).
#
# Predixy fronts a native redis cluster as ONE plain redis:// endpoint, so a
# cluster-UNAWARE client can talk to it with no -c and no MOVED handling. This
# script proves exactly that, on a real 3-master cluster built with the redis
# addon's --cluster flow (same two-stage assembly as test_redis_cluster.sh):
#
#   1. BUILD THE BACKEND CLUSTER — 3 masters, --cluster app, assembled once via
#      `pg redis-cli -- --cluster create`. Reused verbatim from
#      test_redis_cluster.sh's scenario 1; this script is a consumer of that
#      feature, not a re-test of it.
#   2. GUARDS — every flag-shape error (--backend missing/bad-host:port/dup,
#      --password missing/bad-char, --workers<0) must fail BEFORE any image pull
#      or container create, and must leave no trace in pg.yaml.
#   3. INSTALL — asserts the rendered predixy.conf carries every structural line
#      (Bind/WorkerThreads/Authority+ClusterServerPool with the same password
#      twice/one "+ host:port" line per backend, Include license.conf) and that
#      the container is running on the port the pool assigned.
#   4. THE PROXY ACTUALLY PROXIES — a raw, cluster-UNAWARE redis-cli (no -c
#      anywhere) pointed at the proxy port writes a key whose slot belongs to a
#      DIFFERENT master than the seed node that would refuse it: proven by
#      first finding such a key the hard way (a direct, no--c write to one
#      master must get "-MOVED ..."), then showing the same write via the proxy
#      returns plain "OK", and reading it back directly from the node the MOVED
#      line named as the true owner.
#   5. CLIENT AUTH — the proxy's own Authority gate: no/wrong password is
#      refused; the cluster's own requirepass is what clients must use.
#   6. LIST / --show-password, stop/start self-heal, remove (stateless: no data
#      dir), and re-install after remove (fresh-container path, image already
#      local) round out the lifecycle.
#
# Usage:
#   bash test/addon/test_predixy.sh                  # full test, cleans up
#   bash test/addon/test_predixy.sh --skip-destroy   # keep everything running
#   PG_BINARY=/path/to/pg bash test/addon/test_predixy.sh
#   PGCLI_NAMESPACE=x1 PGCLI_REDIS_START_PORT=36479 PGCLI_PREDIXY_START_PORT=36717 bash test/addon/test_predixy.sh
#
# Requires the redis 8 image AND the predixy image locally (podman load from the
# tar distribution, see docs/images.md) or outbound ghcr.io/docker.io access —
# pg pulls on demand otherwise.
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-pxe2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-pxe2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-pxe2e}"
REDIS_START_PORT="${PGCLI_REDIS_START_PORT:-36479}"
PREDIXY_START_PORT="${PGCLI_PREDIXY_START_PORT:-36717}"
CLUSTER="app"
N1="n1"; N2="n2"; N3="n3"
PX="proxy"

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
# all_up — the two-and-three-container helpers below are checked via run_test,
# which invokes its argument directly (no subshell), so these must be plain
# functions, not "sh -c" strings (a child sh would never see rd_up/raw_cli/etc).
all_up() { rd_up "$N1" && rd_up "$N2" && rd_up "$N3"; }
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

# addon_field <name> <key> — value under addons.<redis|predixy>.<name> (the name
# key is always at 8-space indent for a top-level addon map, for either addon).
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
predixy_field() { addon_field "$1" "$2"; }

rd_up() {
    podman ps --filter "name=pgcli-redis-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-redis-$NAMESPACE-$1"
}
px_up() {
    podman ps --filter "name=pgcli-predixy-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-predixy-$NAMESPACE-$1"
}
px_gone() {
    ! podman ps -a --filter "name=pgcli-predixy-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}

# cluster_state <name> — via a -c (cluster) client, same as test_redis_cluster.sh.
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

# raw_cli <port> <password-or-empty> <args...> — a throwaway, deliberately
# cluster-UNAWARE redis-cli (no -c, never) from the redis image pgcli already
# pulled, pointed straight at $1. $2="" means no auth attempt at all. This is the
# whole point of the addon: the client side is a plain, ordinary redis-cli.
# It swallows redis-cli's exit code on purpose: every caller inspects the
# OUTPUT, and a refused auth or refused connection is the expected result there
# (under pipefail/set -e a raw exit 1 would fail the enclosing pipeline or
# assignment long before the grep decides).
raw_cli() {
    local port="$1" pw="$2"; shift 2
    local -a auth=()
    [ -n "$pw" ] && auth=(-a "$pw")
    podman run --rm --network host --http-proxy=false \
        "$(redis_field "$N1" image_tag)" \
        redis-cli -h 127.0.0.1 -p "$port" --no-auth-warning "${auth[@]}" "$@" 2>&1 || true
}
raw_cli_ok() { raw_cli "$@" | grep -q '^OK$'; }
# raw_get <port> <pw> <key> — value at $3 via the proxy, or empty.
raw_get() { local port="$1" pw="$2" key="$3"; raw_cli "$port" "$pw" get "$key" | tr -d '\r'; }
# px_get_eq <key> <expected> — the px:simple-style round-trip check as a plain
# function (so run_test can call it directly, not via sh -c).
px_get_eq() {
    local key="$1" want="$2"
    [ "$(raw_get "$PXPORT" "$PW" "$key")" = "$want" ]
}

# probe_moved <name> <keys...> — writes each key directly to ONE master with a
# cluster-UNAWARE client (no -c), so a foreign-slot key is refused, not
# followed: redis-cli renders the refusal as "MOVED <slot> <host:port>" (the
# "-MOVED" spelling is the raw RESP wire form; the client reflows it). On the
# first key $1 does NOT own, sets the globals MOVED_LINE (the refusal),
# PROBE_KEY and MOVED_TO (the owner host:port) and returns 0 — deleting the
# owned keys it probed along the way. Returns 1 (globals left empty) if none was
# foreign: a ~0.4% coin flip across 7 keys and 3 masters, and the caller treats
# silence as a failure, never a pass. Globals, not stdout, because calling this
# through $(...) would set them in a subshell where the main script cannot see.
MOVED_LINE=""; PROBE_KEY=""; MOVED_TO=""
probe_moved() {
    local n="$1"; shift
    for key in "$@"; do
        local out
        out="$(raw_cli "$(redis_field "$n" port)" "$(redis_field "$n" password)" set "$key" probe-v)"
        if echo "$out" | grep -q 'MOVED'; then
            MOVED_LINE="$out"
            PROBE_KEY="$key"
            # The token two past "MOVED" is the owner "host:port" (MOVED <slot> <host:port>).
            MOVED_TO="$(echo "$out" | awk '/MOVED/{for(i=1;i<=NF;i++) if($i=="MOVED"){print $(i+2); exit}}')"
            return 0
        fi
        # OWNED here: a successful direct set means $n serves this key, so it is
        # not a useful probe. Delete it and keep looking.
        raw_cli "$(redis_field "$n" port)" "$(redis_field "$n" password)" del "$key" >/dev/null
    done
    return 1
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove predixy --name "$PX" --clean-data >/dev/null 2>&1 || true
    for c in "$N1" "$N2" "$N3"; do
        pg addon remove redis --name "$c" --clean-data >/dev/null 2>&1 || true
    done
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli Predixy Proxy Test"
    echo "=========================================="
    echo "  Binary:       $BINARY"
    echo "  Test dir:     $TEST_DIR"
    echo "  Config:       $CONFIG_FILE"
    echo "  Namespace:    $NAMESPACE"
    echo "  Cluster:      $CLUSTER (3 masters, redis pool $REDIS_START_PORT+)"
    echo "  Predixy pool: $PREDIXY_START_PORT+"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-redis-$NAMESPACE-" --format "{{.Names}}" 2>/dev/null; \
               podman ps -a --filter "name=pgcli-predixy-$NAMESPACE-" --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    mkdir -p "$CONFIG_DIR" "$TEST_DIR"
    run_test "config init (isolated ns)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" --namespace "$NAMESPACE"
    sed -i "s/^redis_start_port:.*/redis_start_port: $REDIS_START_PORT/" "$CONFIG_FILE"
    sed -i "s/^predixy_start_port:.*/predixy_start_port: $PREDIXY_START_PORT/" "$CONFIG_FILE"

    # ---- Backend cluster: 3 masters, assembled once (reuses redis --cluster) ----
    section "Backend: install 3 cluster masters (--cluster $CLUSTER)"
    for n in "$N1" "$N2" "$N3"; do
        run_test "install redis --name $n --cluster $CLUSTER" \
            pg addon install redis --name "$n" --cluster "$CLUSTER" --maxmemory 10mb
    done
    P1="$(redis_field "$N1" port)"; P2="$(redis_field "$N2" port)"; P3="$(redis_field "$N3" port)"
    run_test "all three masters up" all_up
    section "Backend: pg redis-cli -- --cluster create (passthrough assembly)"
    CREATE_OUT="$(pg redis-cli --name "$N1" -- --cluster create \
        "127.0.0.1:$P1" "127.0.0.1:$P2" "127.0.0.1:$P3" \
        --cluster-replicas 0 --cluster-yes 2>&1)" || true
    echo "$CREATE_OUT" | sed 's/^/      | /' | tail -8
    run_grep "create covered all 16384 slots" "All 16384 slots covered" printf '%s' "$CREATE_OUT"
    run_test "cluster_state:ok before predixy exists" wait_cluster_ok "$N1"

    PW="$(pg addon password redis --name "$N1")"
    run_test "cluster password is non-empty (predixy will reuse it verbatim)" test -n "$PW"

    # ---- Guards: every bad flag shape must fail before any pull/create ----
    section "Guards (validation before image pull or container create)"
    run_fails "--backend missing entirely" "is required" \
        pg addon install predixy --name "$PX" --password "$PW"
    run_not_grep "a guarded install left no predixy config" "$PX:" cat "$CONFIG_FILE"
    run_test "guarded install left no container" px_gone "$PX"
    run_fails "--backend not host:port" "is not host:port" \
        pg addon install predixy --name "$PX" --backend 127.0.0.1 --password "$PW"
    run_fails "--backend bad port" "invalid port" \
        pg addon install predixy --name "$PX" --backend "127.0.0.1:notaport" --password "$PW"
    run_fails "--backend duplicate node" "listed twice" \
        pg addon install predixy --name "$PX" --backend "127.0.0.1:$P1,127.0.0.1:$P1" --password "$PW"
    run_fails "--password missing" "is required" \
        pg addon install predixy --name "$PX" --backend "127.0.0.1:$P1,127.0.0.1:$P2,127.0.0.1:$P3"
    run_fails "--password with a double quote (Predixy cannot express it)" "must not contain a double quote" \
        pg addon install predixy --name "$PX" --backend "127.0.0.1:$P1,127.0.0.1:$P2,127.0.0.1:$P3" --password 'p"w'
    run_fails "--workers < 0" "must be >= 1" \
        pg addon install predixy --name "$PX" --backend "127.0.0.1:$P1,127.0.0.1:$P2,127.0.0.1:$P3" --password "$PW" --workers -1
    run_not_grep "every guard above left no predixy config" "$PX:" cat "$CONFIG_FILE"
    run_test "every guard above left no container" px_gone "$PX"

    # ---- Install ----
    section "Install predixy --name $PX --backend <all 3 nodes> --password <cluster pw> --workers 2"
    INSTALL_OUT="$(pg addon install predixy --name "$PX" \
        --backend "127.0.0.1:$P1,127.0.0.1:$P2,127.0.0.1:$P3" --password "$PW" --workers 2 2>&1)"
    echo "$INSTALL_OUT" | sed 's/^/      | /'
    run_grep "summary reports the install" "predixy installed" printf '%s' "$INSTALL_OUT"
    run_grep "summary counts 3 backends" "Backends:     3 (127.0.0.1:$P1, 127.0.0.1:$P2, 127.0.0.1:$P3)" printf '%s' "$INSTALL_OUT"
    run_grep "summary prints the raw DSN" "redis://:$PW@0.0.0.0:" printf '%s' "$INSTALL_OUT"

    PXPORT="$(predixy_field "$PX" port)"
    run_test "predixy container is running" px_up "$PX"
    run_test "predixy listened port came from its own pool" test "$PXPORT" -ge "$PREDIXY_START_PORT"
    run_test "predixy port does not collide with a backend port" \
        test "$PXPORT" != "$P1" -a "$PXPORT" != "$P2" -a "$PXPORT" != "$P3"

    # ---- The rendered predixy.conf, checked line-by-line ----
    section "Rendered predixy.conf structure"
    CONF="$TEST_DIR/addon/predixy/$PX/predixy.conf"
    run_test "config file exists at the documented path" test -f "$CONF"
    for line in \
        "Bind 0.0.0.0:$PXPORT" \
        "WorkerThreads 2" \
        "Include license.conf" \
        "Authority {" \
        "    Auth \"$PW\" {" \
        "        Mode admin" \
        "ClusterServerPool {" \
        "    Password \"$PW\"" \
        "    Servers {" \
        "        + 127.0.0.1:$P1" \
        "        + 127.0.0.1:$P2" \
        "        + 127.0.0.1:$P3" \
    ; do
        run_grep "cfg contains '$line'" "$line" cat "$CONF"
    done
    # Braces must never sit alone on a line (Predixy's parser rejects that —
    # see RenderPredixyCfg's doc comment).
    TESTS=$((TESTS + 1))
    if awk '/^[[:space:]]*\{[[:space:]]*$/{found=1} END{exit !found}' "$CONF"; then
        fail "cfg has a brace alone on a line (Predixy cannot parse this)"
    else
        pass "no brace sits alone on its own line"
    fi

    # ---- The proxy actually proxies: a plain client, no -c, absorbs MOVED ----
    section "Transparent proxying: cluster-unaware client (no -c anywhere)"
    # First prove the cluster itself WOULD refuse this client: write probe keys
    # straight to one master (again no -c) until one is foreign-slot — that
    # direct attempt must come back as "MOVED <slot> <owner>", which is exactly
    # what a naive client cannot act on. probe_moved leaves PROBE_KEY/MOVED_TO
    # set for the checks below.
    PROBE_KEYS=(px:alpha px:bravo px:charlie px:delta px:echo px:foxtrot px:golf)
    probe_moved "$N1" "${PROBE_KEYS[@]}" || true
    run_test "a direct, cluster-unaware write gets refused by the cluster (control test)" \
        test -n "$MOVED_LINE"
    if [ -n "$MOVED_LINE" ]; then
        echo "      | direct-to-$N1 refused: $MOVED_LINE"
        run_grep "MOVED names a real owner host:port" "127.0.0.1:" printf '%s' "$MOVED_TO"
        run_test "the same write via the proxy is accepted, not refused" \
            raw_cli_ok "$PXPORT" "$PW" set "$PROBE_KEY" proxied-value
        # Read it back by talking DIRECTLY to the owner the MOVED line named —
        # proving the proxy routed to the CORRECT shard, not just any node.
        # (Single-host cluster, so raw_cli's hardcoded 127.0.0.1 is the owner.)
        OWNER_PORT="${MOVED_TO##*:}"
        TESTS=$((TESTS + 1))
        got="$(raw_cli "$OWNER_PORT" "$PW" get "$PROBE_KEY" | tr -d '\r')"
        if [ "$got" = "proxied-value" ]; then
            pass "$PROBE_KEY landed on its rightful owner $MOVED_TO (proxy absorbed MOVED, not faked it)"
        else
            fail "$PROBE_KEY not readable from owner $MOVED_TO (got '$got')"
        fi
    fi

    # An ordinary key, no MOVED involved, still round-trips through the proxy.
    run_test "plain set through the proxy" raw_cli_ok "$PXPORT" "$PW" set px:simple hello
    run_test "plain get through the proxy returns the same value" \
        px_get_eq px:simple hello

    # ---- Client auth is the proxy's own Authority gate ----
    section "Client auth: the proxy's Authority.Auth gate"
    TESTS=$((TESTS + 1))
    if raw_cli "$PXPORT" "" ping 2>&1 | grep -qi 'NOAUTH\|invalid password\|Authentication'; then
        pass "no client password is refused by the proxy"
    else
        fail "no client password was NOT refused (proxy is wide open?)"
        raw_cli "$PXPORT" "" ping | sed 's/^/      | /'
    fi
    TESTS=$((TESTS + 1))
    if raw_cli "$PXPORT" "wrong-password" ping 2>&1 | grep -qi 'invalid password\|NOAUTH\|Authentication'; then
        pass "a wrong client password is refused"
    else
        fail "a wrong client password was NOT refused"
        raw_cli "$PXPORT" "wrong-password" ping | sed 's/^/      | /'
    fi
    run_test "the cluster's own requirepass is what the proxy accepts from clients" \
        raw_cli_ok "$PXPORT" "$PW" set px:auth-checks ok

    # ---- pg addon list ----
    section "pg addon list surfaces predixy"
    run_grep "list shows the predixy section" "Infra add-ons (predixy):" pg addon list
    run_grep "list marks it running" "Status:      running" pg addon list
    run_grep "list shows workers" "Workers:     2" pg addon list
    run_grep "list shows the 3 backends" "Backends:    3" pg addon list
    run_grep "Client DSN line keeps the password redacted" "redis://:<password>@0.0.0.0:$PXPORT/0" pg addon list
    run_not_grep "the real password is hidden without --show-password" "Password:    $PW" pg addon list
    run_grep "--show-password reveals it" "Password:    $PW" pg addon list --show-password

    # ---- stop / start self-heal (start-only, reads predixy.conf off disk) ----
    section "stop / start self-heal"
    run_test "stop predixy" pg addon stop predixy --name "$PX"
    TESTS=$((TESTS + 1))
    if px_up "$PX"; then fail "container still up after stop"; else pass "container stopped"; fi
    # Give the listener a moment to come fully down (podman stop returns as the
    # process exits; the socket can linger a beat longer). The `if` wrapper is
    # load-bearing: under set -e a bare `cmd && break` kills the script the
    # first time cmd fails, which is exactly the not-yet-closed case.
    for _ in $(seq 1 10); do
        if raw_cli "$PXPORT" "$PW" ping 2>&1 | grep -qi 'could not connect\|connection refused'; then
            break
        fi
        sleep 0.5
    done
    TESTS=$((TESTS + 1))
    STOP_PING="$(raw_cli "$PXPORT" "$PW" ping 2>&1)"
    if echo "$STOP_PING" | grep -qi 'could not connect\|connection refused'; then
        pass "the proxy's port really is closed while stopped"
    else
        fail "the proxy's port still answered while stopped"
        echo "      | unexpected answer: $STOP_PING"
    fi
    run_test "start predixy again" pg addon start predixy --name "$PX"
    run_test "container is back up" px_up "$PX"
    run_test "a pre-existing key survived the stop/start cycle" px_get_eq px:simple hello

    # ---- re-install while running: reuse semantics, no --force ----
    section "Re-install (reuse semantics) and --force"
    REUSE_OUT="$(pg addon install predixy --name "$PX" --backend "127.0.0.1:$P1,127.0.0.1:$P2,127.0.0.1:$P3" --password "$PW" --workers 2 2>&1)"
    run_grep "a running container is left alone, not recreated" "already running; config rewritten" printf '%s' "$REUSE_OUT"
    run_grep "re-install reports 'already present', not a fresh install" "predixy already present" printf '%s' "$REUSE_OUT"
    FORCE_OUT="$(pg addon install predixy --name "$PX" --backend "127.0.0.1:$P1,127.0.0.1:$P2,127.0.0.1:$P3" --password "$PW" --workers 3 --force 2>&1)"
    run_grep "--force recreates it (here, to apply a changed --workers)" "✓ predixy installed" printf '%s' "$FORCE_OUT"
    run_grep "the new workers value reaches the rendered config" "WorkerThreads 3" cat "$CONF"
    run_test "container is running after --force recreate" px_up "$PX"
    # Restore workers=2 for the checks below that assert it.
    pg addon install predixy --name "$PX" --backend "127.0.0.1:$P1,127.0.0.1:$P2,127.0.0.1:$P3" --password "$PW" --workers 2 --force >/dev/null 2>&1

    # ---- logs passthrough ----
    run_grep "pg logs addon predixy works" "" pg logs addon predixy --name "$PX"

    # ---- remove ----
    section "Remove (predixy is stateless: --clean-data is documented as a no-op)"
    run_test "remove --clean-data succeeds" pg addon remove predixy --name "$PX" --clean-data
    run_test "container is gone" px_gone "$PX"
    run_test "rendered config dir is gone" test ! -e "$TEST_DIR/addon/predixy/$PX"
    run_not_grep "config entry is gone from pg.yaml" "$PX:" cat "$CONFIG_FILE"

    # ---- re-install after a clean remove: fresh-container path, image already local ----
    section "Re-install after remove (fresh-container path, no pull needed)"
    run_test "install succeeds again" \
        pg addon install predixy --name "$PX" --backend "127.0.0.1:$P1,127.0.0.1:$P2,127.0.0.1:$P3" --password "$PW" --workers 2
    run_test "container is up again" px_up "$PX"
    run_test "the old cluster key is still reachable through the freshly built proxy" px_get_eq px:simple hello

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
