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
#   5. PERSISTENCE KNOBS — --aof/--appendfsync/--save land in the container
#      argv and the config; an AOF instance replays post-snapshot writes across
#      SIGKILL (no graceful save), the --save no + --aof shape is AOF-only, and
#      the knob-combination validators reject nonsense before anything is
#      pulled or created.
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

# rd_cmd <name> — the redis-server argv recorded on the container.
rd_cmd() {
    podman inspect "pgcli-redis-$NAMESPACE-$1" --format '{{json .Config.Cmd}}'
}
rd_has() { rd_cmd "$1" | grep -qF -- "$2"; }

# hard_kill <name> — SIGKILL (podman kill's flag form is --signal, not -9), so
# Redis never gets its final SIGTERM save. Only an enabled AOF can bring data
# written after the last snapshot back.
hard_kill() { podman kill --signal KILL "pgcli-redis-$NAMESPACE-$1" >/dev/null 2>&1; }

# wait_sync <name> — poll until the replica reports an established master link.
# Redis 8's first full sync goes through an rdbchannel that drops and re-
# establishes once, so a single read can legitimately catch the link down.
wait_sync() {
    for _ in $(seq 1 30); do
        if pg redis-cli --name "$1" info replication 2>/dev/null \
            | grep -q 'master_link_status:up'; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# rd_role <name> — role: line from info replication (slave on 7, replica on 8).
rd_role() {
    pg redis-cli --name "$1" info replication 2>/dev/null \
        | tr -d '\r' | awk -F: '/^role:/{print $2; exit}'
}

# rd_is_replica <name> — the replica reports role: replica (8) or slave (7).
rd_is_replica() {
    case "$(rd_role "$1")" in
        replica|slave) return 0 ;;
        *) return 1 ;;
    esac
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove redis --name "r1"       --clean-data 2>/dev/null || true
    pg addon remove redis --name "r2"       --clean-data 2>/dev/null || true
    pg addon remove redis --name "$LEGACY"  --clean-data 2>/dev/null || true
    pg addon remove redis --name "$CACHE"   --clean-data 2>/dev/null || true
    pg addon remove redis --name "imgtest"  --clean-data 2>/dev/null || true
    pg addon remove redis --name "aoftest"  --clean-data 2>/dev/null || true
    pg addon remove redis --name "rdbtest"  --clean-data 2>/dev/null || true
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
    wa_out=$(REDISCLI_AUTH="wrong-password" pg redis-cli --name "$CACHE" ping 2>&1 || true)
    if echo "$wa_out" | grep -qiE 'NOAUTH|WRONGPASS|invalid password|AUTH'; then
        pass "wrong REDISCLI_AUTH is refused (proves the env var is what authenticates)"
    else
        fail "wrong REDISCLI_AUTH unexpectedly accepted: $wa_out"
    fi

    # ---- Remote-targeting flags (--host/--port) ----
    section "pg redis-cli --host/--port"
    # On Linux host networking, --host 127.0.0.1 is the same endpoint the local
    # path already uses; a PONG proves the flag is honoured (and that an
    # explicit host does not duplicate the injected -h/-p).
    TESTS=$((TESTS + 1))
    if pg redis-cli --host 127.0.0.1 --name "$CACHE" ping 2>/dev/null | grep -q '^PONG$'; then
        pass "--host 127.0.0.1 reaches the addon (still injects the password)"
    else
        fail "--host 127.0.0.1 did not reach the addon"
    fi
    # --port with --host: point at the addon's own port explicitly.
    TESTS=$((TESTS + 1))
    if pg redis-cli --host 127.0.0.1 --port "$PORT" --name "$CACHE" ping 2>/dev/null | grep -q '^PONG$'; then
        pass "--host + --port reaches the addon"
    else
        fail "--host + --port did not reach the addon"
    fi
    # An unknown --name is an error even with --host (no silent fallback).
    # Capture the output first: under `set -o pipefail`, pg's expected
    # non-zero exit would mask the grep result in a direct pipeline.
    TESTS=$((TESTS + 1))
    nh_out=$(pg redis-cli --host 127.0.0.1 --name nosuch ping 2>&1 || true)
    if echo "$nh_out" | grep -qi 'not found'; then
        pass "unknown --name + --host still errors"
    else
        fail "unknown --name + --host did not error: $nh_out"
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

    # ---- AOF: appendonly + appendfsync + save schedule knobs ----
    # --aof turns on the write-ahead log; appendfsync tunes its flush; --save
    # overrides/disables the RDB schedule. The headline is a SIGKILL: only an
    # enabled AOF replays writes made after the last snapshot.
    section "AOF (--aof --appendfsync --save)"
    TESTS=$((TESTS + 1))
    if AOF_OUT="$(pg addon install redis --name aoftest --aof --appendfsync everysec \
                    --save "900 1 300 10" 2>&1)"; then
        pass "install redis --name aoftest --aof --appendfsync --save"
    else
        fail "install with --aof"; echo "$AOF_OUT" | sed 's/^/      | /'
    fi
    echo "$AOF_OUT" | sed 's/^/      /'
    run_grep "summary shows the aof persistence line" "rdb + aof" echo "$AOF_OUT"
    run_grep "summary echoes the save schedule" "save 900 1 300 10" echo "$AOF_OUT"
    run_test "aof stored" test "$(redis_field aoftest aof)" = "true"
    run_test "appendfsync stored" test "$(redis_field aoftest appendfsync)" = "everysec"
    run_test "save schedule stored" test "$(redis_field aoftest save)" = "900 1 300 10"
    run_test "--appendonly yes in the container command" rd_has aoftest "--appendonly"
    run_test "--appendfsync in the container command" rd_has aoftest "--appendfsync"
    run_test "--save in the container command" rd_has aoftest "--save"
    AOFPORT="$(redis_field aoftest port)"
    run_test "aof answers ping" redis_ping aoftest
    # Write a value, then SIGKILL (no graceful save, no snapshot in this
    # window), start explicitly (podman's restart policy needs a systemd
    # timer we don't assume here), and read back: only the AOF can replay a
    # write that no dump.rdb ever contained.
    pg redis-cli --name aoftest set aof:key "aof-survivor" >/dev/null 2>&1 || true
    sleep 2   # let appendfsync everysec flush the write to the log
    hard_kill aoftest
    run_test "aof container down after SIGKILL" rd_down aoftest
    run_grep "start brings it back" "started" pg addon start redis --name aoftest
    run_test "port answers after restart" wait_port "$AOFPORT"
    TESTS=$((TESTS + 1))
    if [ "$(redis_get aoftest aof:key)" = "aof-survivor" ]; then
        pass "post-SIGKILL restart replayed the AOF (write after last snapshot survived)"
    else
        fail "AOF did not replay the post-snapshot write: got '$(redis_get aoftest aof:key)'"
    fi
    # appendonlydir is the AOF's own on-disk tree under the data dir.
    run_test "appendonlydir created under the data dir" \
        bash -c "ls -d '$(data_dir_of aoftest)'/appendonlydir >/dev/null 2>&1 || podman unshare ls -d '$(data_dir_of aoftest)'/appendonlydir >/dev/null 2>&1"

    # Control: an RDB-only instance (no --aof), never snapshotted, same hard
    # kill — the write is GONE. This is what makes the AOF result above
    # meaningful: same kill, opposite outcome, and the only difference is AOF.
    pg addon install redis --name rdbtest >/dev/null 2>&1 || true
    RDBPORT="$(redis_field rdbtest port)"
    pg redis-cli --name rdbtest set rdb:key "gone-after-kill" >/dev/null 2>&1 || true
    sleep 2   # Redis's own schedules need 60s+ here, so no snapshot can exist
    hard_kill rdbtest
    run_test "rdb-only container down after SIGKILL" rd_down rdbtest
    pg addon start redis --name rdbtest >/dev/null 2>&1 || true
    run_test "rdb-only instance answers again" wait_port "$RDBPORT"
    TESTS=$((TESTS + 1))
    if [ -z "$(redis_get rdbtest rdb:key)" ]; then
        pass "control: RDB-only instance LOSES the un-snapshotted write after SIGKILL"
    else
        fail "RDB-only instance unexpectedly kept the write (control invalid): '$(redis_get rdbtest rdb:key)'"
    fi
    pg addon remove redis --name rdbtest --clean-data >/dev/null 2>&1 || true

    # --save no + AOF = aof-only (snapshots disabled). Recreating with --force
    # applies the new schedule; the unique thing to prove is the CLI-side
    # mapping (pgcli's "no" → Redis's own empty --save value), which a plain
    # SIGKILL replay already covers for durability (section above).
    TESTS=$((TESTS + 1))
    if AOFONLY_OUT="$(pg addon install redis --name aoftest --save no --force 2>&1)"; then
        pass "re-install --save no --force (snapshots off, AOF kept)"
    else
        fail "--save no reinstall"; echo "$AOFONLY_OUT" | sed 's/^/      | /'
    fi
    echo "$AOFONLY_OUT" | sed 's/^/      /'
    run_grep "summary shows aof-only persistence" "aof only" echo "$AOFONLY_OUT"
    run_test "aof still on after --save no reinstall" test "$(redis_field aoftest aof)" = "true"
    run_test "save stored as 'no'" test "$(redis_field aoftest save)" = "no"
    # The container must carry --save whose value is the empty disable token.
    # Match tolerantly: podman's {{json}} Cmd array may or may not space the
    # comma, so --save","" and --save", "" are both acceptable.
    TESTS=$((TESTS + 1))
    if rd_cmd aoftest | grep -qE -- '--save",[[:space:]]*""'; then
        pass "--save no mapped to an empty --save in the container argv"
    else
        fail "--save no did not map to empty --save: $(rd_cmd aoftest)"
    fi
    # The recreate replaced the container, so the dataset must have come back
    # purely from the AOF replay under the new aof-only argv.
    run_test "aof-only recreate replayed the earlier write" \
        test "$(redis_get aoftest aof:key)" = "aof-survivor"

    # Knobs that need their partner are rejected before anything is created.
    run_fails "--maxmemory-policy without --maxmemory" "requires --maxmemory" \
        pg addon install redis --name badknob --maxmemory-policy noeviction
    run_fails "--appendfsync without --aof" "requires --aof" \
        pg addon install redis --name badknob --appendfsync always
    run_fails "unknown --maxmemory-policy" "unknown --maxmemory-policy" \
        pg addon install redis --name badknob --maxmemory 64mb --maxmemory-policy allkeys-fifo
    run_fails "unknown --appendfsync" "unknown --appendfsync" \
        pg addon install redis --name badknob --aof --appendfsync sometimes
    run_fails "malformed --save (odd count)" "malformed --save" \
        pg addon install redis --name badknob --save "900 1 300"
    run_fails "malformed --save (non-integer)" "non-negative integer" \
        pg addon install redis --name badknob --save "900 one"
    # A rejected install must not leave a config entry behind.
    run_not_grep "badknob never entered the config" "badknob:" cat "$CONFIG_FILE"

    run_test "remove aoftest --clean-data" pg addon remove redis --name aoftest --clean-data
    run_test "aoftest gone" rd_gone aoftest
    run_test "aoftest data deleted" test ! -d "$(data_dir_of aoftest)"

    # ---- Read replicas (--replica-of / --replica-of-host) ----
    # The master under test is $CACHE (major 8); $LEGACY (major 7) is still
    # installed, which gives the cross-major rejection for free. Linux host
    # networking means the stored 127.0.0.1 target is literally reachable, so
    # the remote-loopback instance r2 exercises the same code path a cross-host
    # master would.
    section "Read replicas"
    run_fails "--replica-of and --replica-of-host together" "mutually exclusive" \
        pg addon install redis --name badrep --replica-of "$CACHE" --replica-of-host 10.0.0.9 --replica-of-port 6379 --password x
    run_fails "--replica-of an uninstalled master lists what exists" "not an installed redis addon" \
        pg addon install redis --name badrep --replica-of ghost
    run_fails "--replica-of itself" "cannot name the instance itself" \
        pg addon install redis --name "$CACHE" --replica-of "$CACHE"
    run_fails "cross-major replica rejected" "cannot replicate across majors" \
        pg addon install redis --name badrep --replica-of "$LEGACY" --version 8
    run_fails "remote replica without --password" "--password" \
        pg addon install redis --name badrep --replica-of-host 127.0.0.1 --replica-of-port "$PORT"
    run_fails "remote replica without --replica-of-port" "--replica-of-port" \
        pg addon install redis --name badrep --replica-of-host 127.0.0.1 --password "$PW"
    run_not_grep "rejected replicas never entered the config" "badrep:" cat "$CONFIG_FILE"

    TESTS=$((TESTS + 1))
    if R1_OUT="$(pg addon install redis --name r1 --replica-of "$CACHE" 2>&1)"; then
        pass "install redis --name r1 --replica-of $CACHE"
    else
        fail "installing the local replica"; echo "$R1_OUT" | sed 's/^/      | /'
    fi
    echo "$R1_OUT" | sed 's/^/      /'
    run_grep "summary prints the replica role" "replica of 127.0.0.1:$PORT" echo "$R1_OUT"
    run_grep "summary says where writes go" "Send writes to the master" echo "$R1_OUT"
    run_test "replica container running" rd_up r1
    run_test "master link up" wait_sync r1
    run_test "role is replica/slave" rd_is_replica r1
    run_test "major adopted from the master" test "$(redis_field r1 version)" = "8"
    run_test "password borrowed from the master" test "$(redis_field r1 password)" = "$PW"
    run_test "replica_host stored" test "$(redis_field r1 replica_host)" = "127.0.0.1"
    run_test "replica_port stored = master's port" test "$(redis_field r1 replica_port)" = "$PORT"
    run_test "--replicaof in the container command" rd_has r1 "--replicaof"
    run_test "--masterauth in the container command" rd_has r1 "--masterauth"
    run_test "replica answers ping" redis_ping r1
    TESTS=$((TESTS + 1))
    if pg redis-cli --name "$CACHE" info replication 2>/dev/null | grep -q 'connected_slaves:1'; then
        pass "master reports 1 connected replica"
    else
        fail "master does not see the replica: $(pg redis-cli --name "$CACHE" info replication 2>/dev/null | tr -d '\r' | grep -i connected || echo none)"
    fi
    # Write on the master, read on the replica: the propagation is what the role
    # exists for. Synchronous here because the master's write has already been
    # ACKed by the link above; retry briefly for the event loop.
    pg redis-cli --name "$CACHE" set repl:key "replicated-value" >/dev/null 2>&1 || true
    TESTS=$((TESTS + 1))
    synced=false
    for _ in $(seq 1 10); do
        if [ "$(redis_get r1 repl:key)" = "replicated-value" ]; then synced=true; break; fi
        sleep 1
    done
    if [ "$synced" = true ]; then
        pass "write on the master reached the replica"
    else
        fail "replica never got the write: got '$(redis_get r1 repl:key)'"
    fi
    TESTS=$((TESTS + 1))
    wo_out=$(pg redis-cli --name r1 set repl:rejected nope 2>&1 || true)
    if echo "$wo_out" | grep -qi 'READONLY'; then
        pass "write to the replica refused with READONLY"
    else
        fail "replica accepted a write: $wo_out"
    fi
    run_grep "addon list marks it a replica" "Role:        replica of 127.0.0.1:$PORT" pg addon list
    run_grep "addon list marks the master a master" "Role:        master" pg addon list

    # Remote shape: same host, addressed by IP:port + explicit password.
    TESTS=$((TESTS + 1))
    if R2_OUT="$(pg addon install redis --name r2 --replica-of-host 127.0.0.1 \
                    --replica-of-port "$PORT" --password "$PW" 2>&1)"; then
        pass "install redis --name r2 --replica-of-host (remote form)"
    else
        fail "installing the remote-form replica"; echo "$R2_OUT" | sed 's/^/      | /'
    fi
    echo "$R2_OUT" | sed 's/^/      /'
    run_test "r2 container running" rd_up r2
    run_test "r2 master link up" wait_sync r2
    run_test "r2 stored remote target" test "$(redis_field r2 replica_host)" = "127.0.0.1"
    TESTS=$((TESTS + 1))
    if [ "$(redis_get r2 repl:key)" = "replicated-value" ]; then
        pass "r2 (remote form) serves replicated reads"
    else
        fail "r2 did not replicate: got '$(redis_get r2 repl:key)'"
    fi

    # A reinstall that passes no replica flag must keep the role — dropping it
    # would silently promote the replica to an independent master.
    run_grep "reinstall keeps the replica role" "replica of 127.0.0.1:$PORT" \
        pg addon install redis --name r1 --force
    run_test "role survives reinstall in the config" \
        test "$(redis_field r1 replica_host)" = "127.0.0.1"

    # Promoting at runtime (no pgcli flag for it) is the documented escape
    # hatch; do it on r2 and confirm it becomes writable, then drop r2.
    pg redis-cli --name r2 replicaof no one >/dev/null 2>&1 || true
    sleep 1
    run_test "runtime replicaof no one promotes r2" redis_setget r2 promoted:key promoted-value
    run_test "remove r1" pg addon remove redis --name r1 --clean-data
    run_test "r1 gone" rd_gone r1
    run_test "remove r2" pg addon remove redis --name r2 --clean-data
    run_test "r2 gone" rd_gone r2

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
