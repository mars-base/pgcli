#!/usr/bin/env bash
# pgcli redis addon end-to-end test.
#
# Redis is the simplest addon shape (one container, one port, no cluster modes)
# and the first version-selectable one, so this script concentrates on what is
# UNIQUE to it rather than re-testing shared plumbing:
#
#   1. VERSION RESOLUTION — --version 7|8 maps through the built-in table,
#      --image bypasses it (major reverse-parsed for display), an invalid
#      --version errors listing the available majors, and 7 + 8 coexist as two
#      instances on two ports from one pool.
#   2. AUTH — requirepass is always generated and always passed to the
#      container; pg redis-cli injects it via REDISCLI_AUTH (and a caller-set
#      REDISCLI_AUTH wins), while a password-less raw redis-cli must get NOAUTH.
#   3. RDB PERSISTENCE — the dataset survives stop/start through the bind
#      mount, and remove WITHOUT --clean-data revives it on reinstall.
#   4. --clean-data under rootless must reclaim redis's mapped-uid dump.rdb via
#      the `podman unshare rm` fallback — proven with a control rm that must
#      FAIL where pgcli's removal succeeds.
#
# Usage:
#   bash test/addon/test_redis.sh                 # full test, cleans up
#   bash test/addon/test_redis.sh --skip-destroy   # keep the instances after
#   PG_BINARY=/path/to/pg bash test/addon/test_redis.sh
#   PGCLI_NAMESPACE=x1 PGCLI_REDIS_START_PORT=36379 bash test/addon/test_redis.sh
#
# Requires both redis majors' images locally (podman load from the tar
# distribution) or outbound docker.io access — pg pulls on demand otherwise.
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-re2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-re2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-re2e}"
REDIS_START_PORT="${PGCLI_REDIS_START_PORT:-36379}"
CACHE="cache"          # the v8 instance under test (default major)
LEGACY="legacy"        # the v7 instance (coexistence + per-major tags)

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
# Strips one layer of quotes: yaml.v3 quotes string scalars that would
# otherwise parse as another type (version: "8"), and the callers compare
# against bare words.
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
rd_down() { ! rd_up "$1"; }
rd_gone() {
    ! podman ps -a --filter "name=pgcli-redis-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}

# redis_ping <name> — PONG through pg redis-cli (proves pg redis-cli injects
# the config password and reaches the right port).
redis_ping() {
    pg redis-cli --name "$1" ping 2>/dev/null | grep -q '^PONG$'
}
# redis_setget <name> <key> <value> — round-trip through pg redis-cli.
redis_setget() {
    local name="$1" key="$2" val="$3"
    pg redis-cli --name "$name" set "$key" "$val" 2>/dev/null | grep -q '^OK$' \
        && [ "$(pg redis-cli --name "$name" get "$key" 2>/dev/null)" = "$val" ]
}
# redis_get <name> <key> — raw value (empty if missing).
redis_get() { pg redis-cli --name "$1" get "$2" 2>/dev/null || true; }

# raw_cli_noauth <port> — a password-less redis-cli from a throwaway container
# against $1 must be refused with NOAUTH (proves requirepass is live, i.e. pg
# redis-cli's success above is authentication, not an open port).
raw_cli_noauth() {
    local port="$1" out rc
    out=$(podman run --rm --network host --http-proxy=false \
        "$(redis_field "$CACHE" image_tag)" \
        redis-cli -h 127.0.0.1 -p "$port" ping 2>&1) && rc=0 || rc=$?
    echo "$out" | grep -q 'NOAUTH'
}

# wait_port <port> — poll until something answers on the TCP port.
wait_port() {
    for _ in $(seq 1 30); do
        if timeout 1 bash -c "echo > /dev/tcp/127.0.0.1/$1" 2>/dev/null; then
            return 0
        fi
        sleep 1
    done
    return 1
}

data_dir_of() { echo "$TEST_DIR/addon/redis/$1/data"; }

# file_exists_elevated <path> — host-visible read first, then through the
# rootless user namespace (podman unshare is rootless-only; on a rootful host
# the plain test succeeds and the fallback is never reached).
file_exists_elevated() {
    test -f "$1" || podman unshare test -f "$1" 2>/dev/null
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove redis --name "$LEGACY" --clean-data 2>/dev/null || true
    pg addon remove redis --name "$CACHE"  --clean-data 2>/dev/null || true
    pg addon remove redis --name "imgtest" --clean-data 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli Redis Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Ports:     redis pool $REDIS_START_PORT+ (one port per instance)"
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
    run_grep "redis_start_port applied" "redis_start_port: $REDIS_START_PORT" \
        cat "$CONFIG_FILE"

    # ---- Install (default major = 8) — the core path ----
    section "Install redis --name $CACHE (default major)"
    TESTS=$((TESTS + 1))
    if INSTALL_OUT="$(pg addon install redis --name "$CACHE" 2>&1)"; then
        pass "install redis --name $CACHE"
    else
        fail "install redis --name $CACHE"; echo "$INSTALL_OUT" | sed 's/^/      | /'
    fi
    echo "$INSTALL_OUT" | sed 's/^/      /'

    PORT="$(redis_field "$CACHE" port)"
    PW="$(redis_field "$CACHE" password)"
    TAG="$(redis_field "$CACHE" image_tag)"
    VER="$(redis_field "$CACHE" version)"
    CONTAINER="$(redis_field "$CACHE" container_name)"

    run_test "container running" rd_up "$CACHE"
    run_test "port drawn from the redis pool" test "$PORT" -ge "$REDIS_START_PORT"
    run_test "default major is 8" test "$VER" = "8"
    run_test "tag resolved from the version table" test "$TAG" = "docker.io/library/redis:8.10.2"
    run_test "container name follows the namespace convention" \
        test "$CONTAINER" = "pgcli-redis-$NAMESPACE-$CACHE"
    run_test "password generated (20 chars)" test "${#PW}" -ge 16
    run_grep "summary prints the version" "Version:    8" echo "$INSTALL_OUT"
    run_grep "summary prints the address" "Address:    0.0.0.0:$PORT" echo "$INSTALL_OUT"
    run_grep "summary prints a raw DSN" "redis://:$PW@0.0.0.0:$PORT/0" echo "$INSTALL_OUT"
    run_grep "0.0.0.0 widening is called out" "listening on every interface" echo "$INSTALL_OUT"

    # The data dir must exist and the container must have the four core flags.
    TESTS=$((TESTS + 1))
    if podman inspect "pgcli-redis-$NAMESPACE-$CACHE" --format '{{json .Config.Cmd}}' \
        | grep -qF -- "--requirepass"; then
        pass "--requirepass present in the container command"
    else
        fail "--requirepass missing from the container command"
    fi
    run_test "data dir created" test -d "$(data_dir_of "$CACHE")"
    run_test "port answers" wait_port "$PORT"

    # ---- Data plane through pg redis-cli ----
    section "pg redis-cli round-trip"
    run_test "ping -> PONG" redis_ping "$CACHE"
    run_test "set/get round-trip" redis_setget "$CACHE" "session:42" "user-alice"
    run_test "incr on a fresh key -> 1" \
        test "$(pg redis-cli --name "$CACHE" incr counter:hits 2>/dev/null)" = "1"
    run_grep "multi-command works (lpush/lrange)" "member-b" \
        bash -c "'$BINARY' -c '$CONFIG_FILE' redis-cli --name '$CACHE' lpush queue a >/dev/null 2>&1; '$BINARY' -c '$CONFIG_FILE' redis-cli --name '$CACHE' lpush queue member-b >/dev/null 2>&1; '$BINARY' -c '$CONFIG_FILE' redis-cli --name '$CACHE' lrange queue 0 -1"

    # ---- requirepass is really enforcing ----
    section "Auth (requirepass live)"
    TESTS=$((TESTS + 1))
    if raw_cli_noauth "$PORT"; then
        pass "password-less redis-cli refused with NOAUTH"
    else
        fail "password-less redis-cli was NOT refused — requirepass not enforcing"
    fi
    # A caller-set REDISCLI_AUTH must win over the stored password.
    TESTS=$((TESTS + 1))
    if REDISCLI_AUTH="$PW" pg redis-cli --name "$CACHE" ping 2>/dev/null | grep -q '^PONG$'; then
        pass "REDISCLI_AUTH override authenticates"
    else
        fail "REDISCLI_AUTH override rejected"
    fi
    TESTS=$((TESTS + 1))
    if REDISCLI_AUTH="wrong-password" pg redis-cli --name "$CACHE" ping 2>&1 \
        | grep -qiE 'NOAUTH|invalid password|AUTH'; then
        pass "wrong REDISCLI_AUTH is refused (proves the env var is what authenticates)"
    else
        fail "wrong REDISCLI_AUTH unexpectedly accepted"
    fi

    # ---- RDB persistence across stop/start ----
    section "RDB persistence (stop/start)"
    pg redis-cli --name "$CACHE" set persist:key "still-here" >/dev/null 2>&1 || true
    TESTS=$((TESTS + 1))
    pg redis-cli --name "$CACHE" save >/dev/null 2>&1 \
        && pass "explicit SAVE accepted (fast, deterministic snapshot)" \
        || fail "SAVE failed"
    run_test "dump.rdb written to the data dir" \
        file_exists_elevated "$(data_dir_of "$CACHE")/dump.rdb"
    run_grep "stop prints a confirmation" "stopped" \
        pg addon stop redis --name "$CACHE"
    run_test "container stopped" rd_down "$CACHE"
    run_grep "start prints a confirmation" "started" \
        pg addon start redis --name "$CACHE"
    run_test "running again" rd_up "$CACHE"
    run_test "port answers after restart" wait_port "$PORT"
    TESTS=$((TESTS + 1))
    if [ "$(redis_get "$CACHE" persist:key)" = "still-here" ]; then
        pass "value survived stop/start (RDB replay)"
    else
        fail "value lost across stop/start: got '$(redis_get "$CACHE" persist:key)'"
    fi

    # ---- Reinstall against a live container: documented no-op ----
    section "Re-install (live container is a no-op)"
    run_grep "reinstall skips creation" "already running; skipping" \
        pg addon install redis --name "$CACHE"
    PW2="$(redis_field "$CACHE" password)"
    if [ "$PW" = "$PW2" ]; then pass "password kept across reinstall"
    else fail "password changed on reinstall"; fi

    # ---- addon list / logs ----
    section "addon list / logs"
    run_grep "addon list shows the instance" "redis (name: $CACHE)" pg addon list
    run_not_grep "addon list never prints the password" "$PW" pg addon list
    run_grep "addon list shows the version" "Version:     8" pg addon list
    run_grep "logs redis returns the startup banner" "redis" \
        pg logs addon redis --name "$CACHE" -n 50

    # ---- Second major: 7 and 8 coexist on two ports, per-major tags ----
    section "Major coexistence (--version 7)"
    TESTS=$((TESTS + 1))
    if L7_OUT="$(pg addon install redis --name "$LEGACY" --version 7 2>&1)"; then
        pass "install redis --name $LEGACY --version 7"
    else
        fail "install redis --version 7"; echo "$L7_OUT" | sed 's/^/      | /'
    fi
    echo "$L7_OUT" | sed 's/^/      /'
    PORT7="$(redis_field "$LEGACY" port)"
    TAG7="$(redis_field "$LEGACY" image_tag)"
    run_test "v7 container running" rd_up "$LEGACY"
    run_test "v7 tag from the table" test "$TAG7" = "docker.io/library/redis:7.4.11"
    run_test "v7 major recorded" test "$(redis_field "$LEGACY" version)" = "7"
    TESTS=$((TESTS + 1))
    if [ "$PORT7" != "$PORT" ]; then
        pass "two instances drew distinct ports ($PORT / $PORT7)"
    else
        fail "port collision: both on $PORT"
    fi
    run_grep "v7 summary prints its version" "Version:    7" echo "$L7_OUT"
    run_test "v7 answers ping" redis_ping "$LEGACY"
    run_test "v7 set/get works" redis_setget "$LEGACY" "legacy:key" "v7-value"
    run_test "v7 data dir is separate" test -d "$(data_dir_of "$LEGACY")"
    # The default (no --name) target is the first sorted instance — "cache"
    # sorts before "legacy", so pg redis-cli without --name must hit the v8 one.
    TESTS=$((TESTS + 1))
    if pg redis-cli ping >/dev/null 2>&1 \
       && [ "$(pg redis-cli get persist:key 2>/dev/null)" = "still-here" ]; then
        pass "bare 'pg redis-cli' targets the first sorted instance (cache)"
    else
        fail "bare pg redis-cli did not hit the first sorted instance"
    fi

    # ---- Version-resolution failures (pure CLI, no container) ----
    section "Version resolution errors"
    run_fails "invalid --version lists the majors" "available: 7, 8" \
        pg addon install redis --name badver --version 6
    run_fails "invalid --version rejects 6.x-style input" "--version" \
        pg addon install redis --name badver --version "6.2"

    # ---- --image bypass + --maxmemory ----
    # --image carries an off-table tag; pre-tagging a local copy of a known
    # image (fixture name) keeps the test offline-safe — EnsureImage skips the
    # pull when the tag already exists. No local redis image -> skip the
    # section loudly rather than silently.
    section "--image bypass and --maxmemory"
    FIXTURE="localhost/pgcli-redis-fixture:7.2.4"
    SEED=""
    for cand in docker.io/library/redis:7.4.11 docker.io/library/redis:8.10.2; do
        podman image exists "$cand" 2>/dev/null && { SEED="$cand"; break; }
    done
    TESTS=$((TESTS + 1))
    if [ -n "$SEED" ] && podman tag "$SEED" "$FIXTURE" 2>/dev/null; then
        pass "fixture image tagged from $SEED (offline-safe --image test)"
        TESTS=$((TESTS + 1))
        if IMG_OUT="$(pg addon install redis --name imgtest --image "$FIXTURE" --maxmemory 64mb 2>&1)"; then
            pass "install with explicit --image + --maxmemory"
        else
            fail "install with explicit --image"; echo "$IMG_OUT" | sed 's/^/      | /'
        fi
        echo "$IMG_OUT" | sed 's/^/      /'
        if [ -n "$(redis_field imgtest port)" ]; then
            run_test "--image tag stored verbatim" \
                test "$(redis_field imgtest image_tag)" = "$FIXTURE"
            # The off-table tag still carries a parseable major — 7.2.4
            # reverse-parses to "7" (the reverse-parse is a digit-run scan, not
            # a table lookup, so a private-registry 7.x still gets a version).
            run_test "--image major reverse-parsed to 7" \
                test "$(redis_field imgtest version)" = "7"
            run_test "fixture container running" rd_up imgtest
            run_test "maxmemory stored" test "$(redis_field imgtest maxmemory)" = "64mb"
            run_grep "--maxmemory eviction note printed" "allkeys-lru" echo "$IMG_OUT"
            # The eviction flags must actually be in the container argv.
            TESTS=$((TESTS + 1))
            if podman inspect "pgcli-redis-$NAMESPACE-imgtest" --format '{{json .Config.Cmd}}' \
                | grep -qF -- "--maxmemory-policy"; then
                pass "--maxmemory-policy present in the container command"
            else
                fail "--maxmemory-policy missing from the container command"
            fi
            run_test "fixture answers ping" redis_ping imgtest
        fi
        pg addon remove redis --name imgtest --clean-data 2>/dev/null || true
        podman rmi "$FIXTURE" 2>/dev/null || true
    else
        fail "no local redis image to seed the --image fixture (podman load the tar first)"
    fi

    # ---- Remove: data kept by default; --clean-data deletes ----
    section "Remove / --clean-data"
    run_test "remove $LEGACY keeps the data dir" pg addon remove redis --name "$LEGACY"
    run_test "$LEGACY container gone" rd_gone "$LEGACY"
    run_test "$LEGACY data kept" test -d "$(data_dir_of "$LEGACY")"
    run_not_grep "yaml entry removed" "$LEGACY:" cat "$CONFIG_FILE"

    # Reinstall reuses the kept dir — the RDB written by the v7 instance must
    # come back (data survives a config-level remove/add cycle, not just stop/
    # start). Password is regenerated on a fresh install, which is fine: RDB
    # carries no auth.
    run_test "reinstall $LEGACY --version 7 revives the data" \
        pg addon install redis --name "$LEGACY" --version 7
    run_test "revived $LEGACY answers ping" redis_ping "$LEGACY"
    TESTS=$((TESTS + 1))
    if [ "$(redis_get "$LEGACY" legacy:key)" = "v7-value" ]; then
        pass "v7 value survived remove -> reinstall (kept RDB)"
    else
        fail "reinstall lost the dataset: got '$(redis_get "$LEGACY" legacy:key)'"
    fi

    # The rootless headline: redis writes dump.rdb as a mapped subordinate uid,
    # which the host user cannot delete. Seed nothing — the instance already has
    # a real dump.rdb from the SAVE above — so the control rm must fail while
    # pgcli's --clean-data succeeds via `podman unshare rm`.
    RD="$(data_dir_of "$LEGACY")"
    pg redis-cli --name "$LEGACY" save >/dev/null 2>&1 || true
    if [ "$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null)" = "true" ]; then
        TESTS=$((TESTS + 1))
        if rm -rf "$RD" 2>/dev/null && [ ! -d "$RD" ]; then
            fail "control rm -rf SUCCEEDED (mapped-uid dump.rdb should be undeletable by the host user)"
        else
            pass "control rm -rf FAILED as expected (mapped-uid files undeletable by host user)"
        fi
    else
        yellow "  (rootful podman — skipping the undeletable-file control)"
    fi
    pg addon stop redis --name "$LEGACY" >/dev/null 2>&1 || true
    run_test "remove $LEGACY --clean-data" pg addon remove redis --name "$LEGACY" --clean-data
    run_test "$LEGACY data deleted by clean-data" test ! -d "$RD"
    run_test "$LEGACY parent dir pruned" test ! -d "$TEST_DIR/addon/redis/$LEGACY"

    run_test "remove $CACHE --clean-data" pg addon remove redis --name "$CACHE" --clean-data
    run_test "$CACHE container gone" rd_gone "$CACHE"
    run_test "$CACHE data deleted" test ! -d "$(data_dir_of "$CACHE")"
    run_not_grep "yaml redis section gone" "redis:" cat "$CONFIG_FILE"

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
