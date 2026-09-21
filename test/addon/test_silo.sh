#!/usr/bin/env bash
# pgcli silo addon end-to-end test (silo = Pigsty's MinIO fork).
# Mirrors test/addon/test_minio.sh, plus the silo-specific surface: the addon
# installs with --tls (HTTPS from pgcli's own CA), and BOTH clients work
# against it — `pg mcli` (silo's own client, from the pgsty/silo image) and
# `pg mc` (MinIO's client, proving the S3 contract is intact end to end). It
# also covers the shared port pool: a minio and a silo instance in one config
# must coexist without a port collision, and BYO TLS via `pg cert`.
#
# Aliases persist on the host at ~/.mc/config.json — pg mcli and pg mc share
# one file by design — so both clients see the same aliases. The test registers
# only uniquely-named aliases and removes them in cleanup, touching nothing
# else. Isolation otherwise matches test_minio.sh:
# throwaway config file + base dir, own namespace, own minio_start_port pool
# (sed-injected; the pool is shared with minio by design, hence a distinct
# range here).
#
# Usage:
#   bash test/addon/test_silo.sh                 # full test, cleans up
#   bash test/addon/test_silo.sh --skip-destroy  # keep the store after
#   PG_BINARY=/usr/local/bin/pg bash test/addon/test_silo.sh
#   PGCLI_NAMESPACE=x1 PGCLI_MINIO_START_PORT=29200 bash test/addon/test_silo.sh
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-sl-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-sl-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-sne2e}"
MINIO_START_PORT="${PGCLI_MINIO_START_PORT:-29200}"
STORE="store"           # the silo instance under test (TLS)
STORE2="archive"        # a second silo instance (port-pool arithmetic)
MSTORE="mnpeer"         # a minio instance sharing the same port pool
SNMD="snmddisk"         # the SNMD instance under test (loop-file drives)
BUCKET="backups"
ALIAS="sne2e-store"        # unique — alias keys share the host's ~/.mc
MCALIAS="sne2e-store-mc"   # the same store, reached via MinIO's mc client
SNMD_ALIAS="sne2e-snmd"    # the SNMD store, via mcli

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

pg()     { "$BINARY" -c "$CONFIG_FILE" "$@"; }
pgmcli() { pg mcli "$@"; }
pgmc()   { pg mc "$@"; }

# pgmcli_env <MC_HOST_<name> value> <name> <mcli args...> — stateless env alias.
pgmcli_env() {
    local val="$1" name="$2"; shift 2
    env "MC_HOST_$name=$val" "$BINARY" -c "$CONFIG_FILE" mcli "$@"
}

# addon_field <silo|minio-name> <key> — value under addons.<section>.<name>.
# Instance names are unique across the two tables in this test, so the awk
# block (same shape as test_minio.sh) can key on the 8-space name line alone.
addon_field() {
    awk -v n="        $1:" -v k="$2:" '
        $0 == n {f=1; next}
        f && /^        [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {gsub(/^[[:space:]]*[a-z_]+: /,""); print; exit}
    ' "$CONFIG_FILE"
}
silo_field()  { addon_field "$1" "$2"; }
minio_field() { addon_field "$1" "$2"; }

sl_up() {
    podman ps --filter "name=pgcli-silo-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-silo-$NAMESPACE-$1"
}
sl_down() { ! sl_up "$1"; }
sl_gone() {
    ! podman ps -a --filter "name=pgcli-silo-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}
mn_up() {
    podman ps --filter "name=pgcli-minio-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-minio-$NAMESPACE-$1"
}
mn_gone() {
    ! podman ps -a --filter "name=pgcli-minio-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}
wait_live() {  # <api-port> — poll the /minio/health/live endpoint over HTTPS
    for _ in $(seq 1 30); do
        curl -sfk -o /dev/null "https://127.0.0.1:$1/minio/health/live" && return 0
        sleep 1
    done
    return 1
}

# ---- SNMD loop-drive fixtures (same design as test_minio.sh) --------------
# 768M fresh ext4 loop images, one per "drive", each mounted at its own dir so
# every drive has a distinct st_dev (what silo/MinIO require). snmd_setup
# returns non-zero when loop mounts are impossible here; the section then fails
# loudly rather than skipping.
SNMD_DRIVES=4
SNMD_DISK_DIR="$TEST_DIR/snmd-disks"
SNMD_MNT_ROOT="$TEST_DIR/snmd-mnt"

snmd_umount_all() {
    for i in $(seq 1 "$SNMD_DRIVES"); do
        sudo -n umount "$SNMD_MNT_ROOT/d$i" 2>/dev/null || true
    done
    rm -f "$SNMD_DISK_DIR"/*.img 2>/dev/null || true
}

snmd_setup() {
    local mkfs=""
    for c in /sbin/mkfs.ext4 /usr/sbin/mkfs.ext4 mkfs.ext4; do
        command -v "$c" >/dev/null 2>&1 && { mkfs="$c"; break; }
    done
    [ -n "$mkfs" ] || return 1
    sudo -n true 2>/dev/null || return 1
    mkdir -p "$SNMD_DISK_DIR" "$SNMD_MNT_ROOT"
    for i in $(seq 1 "$SNMD_DRIVES"); do
        rm -f "$SNMD_DISK_DIR/d$i.img"
        fallocate -l 768M "$SNMD_DISK_DIR/d$i.img" || return 1
        "$mkfs" -q -F "$SNMD_DISK_DIR/d$i.img" || return 1
        mkdir -p "$SNMD_MNT_ROOT/d$i"
        sudo -n mount -o loop "$SNMD_DISK_DIR/d$i.img" "$SNMD_MNT_ROOT/d$i" || return 1
        sudo -n chown "$(id -u):$(id -g)" "$SNMD_MNT_ROOT/d$i" || return 1
    done
    for i in $(seq 1 "$SNMD_DRIVES"); do echo "$SNMD_MNT_ROOT/d$i"; done
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pgmcli alias remove "$ALIAS"  >/dev/null 2>&1 || true
    pgmcli alias remove "$SNMD_ALIAS" >/dev/null 2>&1 || true
    pgmc   alias remove "$MCALIAS" >/dev/null 2>&1 || true
    pg addon remove silo  --name "$SNMD"   --clean-data 2>/dev/null || true
    snmd_umount_all
    pg addon remove silo  --name "$STORE2"  --clean-data 2>/dev/null || true
    pg addon remove silo  --name "$STORE"   --clean-data 2>/dev/null || true
    pg addon remove minio --name "$MSTORE"  --clean-data 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli silo Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Ports:     shared minio/silo pool $MINIO_START_PORT+ (API first, console second)"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }
    command -v curl &>/dev/null || { red "curl is not installed (health checks)"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-silo-$NAMESPACE-"  \
                    --filter "name=pgcli-minio-$NAMESPACE-" \
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

    # ---- Install (TLS) ----
    section "Install (with --tls)"
    TESTS=$((TESTS + 1))
    if INSTALL_OUT="$(pg addon install silo --name "$STORE" --tls 2>&1)"; then
        pass "install silo --name $STORE --tls"
    else
        fail "install silo --name $STORE --tls"; echo "$INSTALL_OUT" | sed 's/^/      | /'
    fi
    echo "$INSTALL_OUT" | sed 's/^/      /'
    API="$(silo_field "$STORE" api_port)"
    CON="$(silo_field "$STORE" console_port)"
    PW="$(silo_field "$STORE" root_password)"
    if [ "$CON" = "$((API + 1))" ]; then pass "consecutive port pair: API $API / console $CON"
    else fail "ports not consecutive: $API / $CON"; fi
    run_grep "default image is pgsty/silo" "docker.io/pgsty/silo" \
        grep -o 'image_tag: docker.io/pgsty/silo[^ ]*' "$CONFIG_FILE"
    run_grep "TLS CA path printed" "tls/silo/$STORE/ca.crt" echo "$INSTALL_OUT"
    run_grep "summary prints S3 API endpoint (https)" "S3 API:       https://127.0.0.1:$API" \
        echo "$INSTALL_OUT"
    run_test "container running" sl_up "$STORE"
    run_test "data dir created" test -d "$TEST_DIR/addon/silo/$STORE/data"
    run_test "certs dir created (generated mode)" test -f "$TEST_DIR/tls/silo/$STORE/public.crt"
    run_test "health endpoint live over TLS" wait_live "$API"

    # ---- Re-install against a live container: documented no-op ----
    section "Re-install (live container is a no-op)"
    run_grep "reinstall skips creation" "already running; skipping" \
        pg addon install silo --name "$STORE" --tls
    PW2="$(silo_field "$STORE" root_password)"
    if [ "$PW" = "$PW2" ]; then pass "root password kept across reinstall"
    else fail "root password changed on reinstall"; fi

    # ---- pg mcli (silo's own client) ----
    section "pg mcli (alias + the doc's common commands)"
    run_fails "mcli alias set with WRONG password rejected" "" \
        pgmcli alias set "${ALIAS}bad" "https://127.0.0.1:$API" admin definitely-wrong
    run_test "rejected alias left no trace in ~/.mcli" \
        bash -c "! grep -qF '${ALIAS}bad' \"\$HOME/.mcli/config.json\" 2>/dev/null"
    run_test "mcli alias set (validates against the endpoint)" \
        pgmcli alias set "$ALIAS" "https://127.0.0.1:$API" admin "$PW"
    run_grep "mcli alias list shows it" "$ALIAS" pgmcli alias list
    run_grep "mcli mb $BUCKET" "$BUCKET" pgmcli mb "$ALIAS/$BUCKET"
    echo "hello from pgcli silo e2e $$" > "$TEST_DIR/upload.txt"
    run_grep "mcli cp upload" "upload.txt" \
        pgmcli cp "$TEST_DIR/upload.txt" "$ALIAS/$BUCKET/upload.txt"
    run_grep "mcli ls bucket shows object" "upload.txt" pgmcli ls "$ALIAS/$BUCKET"
    run_grep "mcli ls store shows bucket"   "$BUCKET"    pgmcli ls "$ALIAS"
    run_test "mcli cp download" pgmcli cp "$ALIAS/$BUCKET/upload.txt" "$TEST_DIR/upload.down"
    run_test "mcli downloaded bytes identical" cmp -s "$TEST_DIR/upload.txt" "$TEST_DIR/upload.down"
    run_grep "mcli rm object" "upload.txt" pgmcli rm "$ALIAS/$BUCKET/upload.txt"
    run_not_grep "mcli object gone after rm" "upload.txt" pgmcli ls "$ALIAS/$BUCKET"

    # ---- pg mc (MinIO's client) against the silo store: the S3 contract ----
    section "pg mc against silo (S3 compatibility)"
    # pg mcli and pg mc share one host config file (~/.mc) by design, so an
    # alias set via mcli must be visible to mc with no second `alias set`.
    run_grep "mc alias list sees the mcli-set alias" "$ALIAS" pgmc alias list
    run_test "mc alias set (minio client reaches the silo store)" \
        pgmc alias set "$MCALIAS" "https://127.0.0.1:$API" admin "$PW"
    run_grep "mc mb via the silo store" "s3-backup" pgmc mb "$MCALIAS/s3-backup"
    run_grep "mc ls sees the mcli-made bucket" "$BUCKET" pgmc ls "$MCALIAS"
    pgmcli alias remove "${ALIAS}bad" >/dev/null 2>&1 || true
    pgmc   alias remove "$MCALIAS"    >/dev/null 2>&1 || true

    # ---- Stateless MC_HOST_* alias (documented) ----
    section "MC_HOST_* env alias (no config file)"
    run_grep "MC_HOST_* lists buckets" "$BUCKET" \
        pgmcli_env "https://admin:$PW@127.0.0.1:$API" storeenv ls storeenv

    # ---- list / stop / start ----
    section "List / stop / start"
    run_grep "addon list shows store" "silo (name: $STORE)" pg addon list
    run_not_grep "addon list never prints the password" "$PW" pg addon list

    TESTS=$((TESTS + 1))
    STOP_OUT="$(pg addon stop silo --name "$STORE" 2>&1)" || true
    echo "$STOP_OUT" | sed 's/^/      /'
    if echo "$STOP_OUT" | grep -qF 'stopped'; then pass "stop prints a confirmation"
    else fail "stop printed no confirmation"; fi
    run_test "container stopped" sl_down "$STORE"
    run_grep "reinstall starts a stopped container" "exists but is stopped; starting" \
        pg addon install silo --name "$STORE" --tls
    run_test "running again" sl_up "$STORE"
    run_test "health after start" wait_live "$API"
    run_test "pg addon start is a no-op when running" pg addon start silo --name "$STORE"
    run_grep "bucket survived the restart cycle" "$BUCKET" pgmcli ls "$ALIAS"

    # ---- --force recreate keeps the data directory ----
    section "Recreate with --force"
    run_grep "--force recreates the container" "Starting silo container" \
        pg addon install silo --name "$STORE" --tls --force
    run_test "data dir intact after --force" test -d "$TEST_DIR/addon/silo/$STORE/data"
    run_test "healthy after --force" wait_live "$API"
    run_grep "bucket still there after --force" "$BUCKET" pgmcli ls "$ALIAS"

    # ---- Shared port pool: a minio instance alongside silo ----
    section "Shared port pool (minio + silo coexist)"
    run_test "install minio peer $MSTORE" pg addon install minio --name "$MSTORE"
    MAPI="$(minio_field "$MSTORE" api_port)"
    MCP="$(minio_field "$MSTORE" console_port)"
    run_test "minio peer running" mn_up "$MSTORE"
    # All four API/console ports across the two addons must be distinct.
    TESTS=$((TESTS + 1))
    uniq_count="$(printf '%s\n' "$API" "$CON" "$MAPI" "$MCP" | sort -u | wc -l | tr -d ' ')"
    if [ "$uniq_count" = "4" ]; then pass "no port collision across minio+silo ($API/$CON vs $MAPI/$MCP)"
    else fail "port collision: $API/$CON vs $MAPI/$MCP (want 4 distinct, got $uniq_count)"; fi

    # ---- Second silo instance: next consecutive pair ----
    section "Second silo instance (port pool arithmetic)"
    run_test "install $STORE2" pg addon install silo --name "$STORE2" --tls
    API2="$(silo_field "$STORE2" api_port)"
    CON2="$(silo_field "$STORE2" console_port)"
    # The pool cursor is shared across both addon tables, so the second silo
    # instance just gets the next free consecutive pair (>= current max).
    hi="$API"; [ "$CON" -gt "$hi" ] && hi="$CON"
    [ "$MAPI" -gt "$hi" ] && hi="$MAPI"
    [ "$MCP" -gt "$hi" ] && hi="$MCP"
    if [ "$API2" -gt "$hi" ] && [ "$CON2" = "$((API2 + 1))" ]; then
        pass "pool assigned next free pair above existing: $API2 / $CON2"
    else
        fail "expected pair above $hi with CON2=API2+1, got $API2/$CON2"
    fi
    run_test "second silo healthy" wait_live "$API2"

    # ---- BYO TLS via pg cert ----
    section "BYO TLS (pg cert -> --tls-cert/--tls-key)"
    run_test "pg cert mints a leaf covering loopback" \
        pg cert --host 127.0.0.1,localhost --cert-file "$TEST_DIR/byo.crt" --key-file "$TEST_DIR/byo.key"
    TESTS=$((TESTS + 1))
    if BYO_OUT="$(pg addon install silo --name "$STORE2" --tls-cert "$TEST_DIR/byo.crt" --tls-key "$TEST_DIR/byo.key" --force 2>&1)"; then
        pass "BYO install succeeds"
        echo "$BYO_OUT" | sed 's/^/      /'
    else
        fail "BYO install"; echo "$BYO_OUT" | sed 's/^/      | /'
    fi
    echo "$BYO_OUT" | grep -qF "TLS certs (BYO:" \
        && pass "install reports BYO mode" || fail "install did not report BYO mode"
    echo "$BYO_OUT" | grep -qF "$TEST_DIR/byo.crt" \
        && pass "summary prints the cert path" || fail "summary missing cert path"
    run_test "BYO store healthy over TLS (leaf is its own anchor)" wait_live "$API2"
    pgmcli alias remove "$ALIAS" >/dev/null 2>&1 || true

    # ---- Remove: data kept by default, --clean-data deletes ----
    section "Remove"
    run_test "remove $STORE2 keeps the data dir" pg addon remove silo --name "$STORE2"
    run_test "$STORE2 container gone" sl_gone "$STORE2"
    run_test "$STORE2 data kept" test -d "$TEST_DIR/addon/silo/$STORE2/data"
    run_test "reinstall $STORE2 (fresh, same default dir)" \
        pg addon install silo --name "$STORE2" --tls
    run_test "remove $STORE2 --clean-data" pg addon remove silo --name "$STORE2" --clean-data
    run_test "$STORE2 data deleted" test ! -d "$TEST_DIR/addon/silo/$STORE2"

    run_test "remove minio peer $MSTORE --clean-data" pg addon remove minio --name "$MSTORE" --clean-data
    run_test "$MSTORE container gone" mn_gone "$MSTORE"

    run_test "remove $STORE keeps the data dir" pg addon remove silo --name "$STORE"
    run_test "$STORE container gone" sl_gone "$STORE"
    run_test "$STORE data kept at documented path" test -d "$TEST_DIR/addon/silo/$STORE/data"
    run_not_grep "yaml silo entries removed" "silo:" cat "$CONFIG_FILE"

    # ---- SNMD: single-node multi-drive over four loop-mounted "disks" ------
    section "SNMD (--drive x4, --tls)"
    run_fails "--drive still rejects --data-dir in the same install" "cannot be combined" \
        pg addon install silo --name snmdx --drive /a --data-dir /b

    # ---- MNMD: --drive + --endpoint is now a legal combination ----
    # The mutual-exclusion error is gone; what remains is matrix validation,
    # which fires before any podman work, so these are pure CLI checks (no real
    # second host is exercised locally — that is the VM test's job).
    section "MNMD matrix validation (--drive + --endpoint)"
    run_fails "MNMD rejects a bare /data export path" "each endpoint must address one of those slots" \
        pg addon install silo --name mnmdx --drive /a --drive /b \
        --endpoint http://10.0.0.1:9000/data --endpoint http://10.0.0.1:9000/data \
        --endpoint http://10.0.0.2:9000/data --endpoint http://10.0.0.2:9000/data
    run_fails "MNMD rejects an endpoint count not divisible by drives/node" "not a multiple of" \
        pg addon install silo --name mnmdx --drive /a --drive /b \
        --endpoint http://10.0.0.1:9000/data1 --endpoint http://10.0.0.1:9000/data2 \
        --endpoint http://10.0.0.2:9000/data1
    run_fails "MNMD rejects a single-host fold" "at least one more node" \
        pg addon install silo --name mnmdx --drive /a --drive /b \
        --endpoint http://10.0.0.1:9000/data1 --endpoint http://10.0.0.1:9000/data2
    run_fails "MNMD rejects http:// endpoints on a --tls node" "plaintext http:// but this node serves TLS" \
        pg addon install silo --name mnmdx --tls --drive /a --drive /b \
        --endpoint http://10.0.0.1:9000/data1 --endpoint http://10.0.0.1:9000/data2 \
        --endpoint http://10.0.0.2:9000/data1 --endpoint http://10.0.0.2:9000/data2


    TESTS=$((TESTS + 1))
    DRIVES_FILE="$TEST_DIR/snmd-drives.txt"
    if snmd_setup > "$DRIVES_FILE"; then
        pass "loop-drive setup (4 fresh ext4 loop mounts)"
    else
        fail "loop-drive setup (SNMD section needs loop mounts: mkfs.ext4 + sudo mount -o loop)"
    fi
    if [ -s "$DRIVES_FILE" ]; then
        D1=$(sed -n 1p "$DRIVES_FILE"); D2=$(sed -n 2p "$DRIVES_FILE")
        D3=$(sed -n 3p "$DRIVES_FILE"); D4=$(sed -n 4p "$DRIVES_FILE")

        TESTS=$((TESTS + 1))
        if SNMD_OUT="$(pg addon install silo --name "$SNMD" --tls \
                --drive "$D1" --drive "$D2" --drive "$D3" --drive "$D4" 2>&1)"; then
            pass "install silo --drive x4 --tls"
        else
            fail "install silo --drive x4 --tls"; echo "$SNMD_OUT" | sed 's/^/      | /'
        fi
        echo "$SNMD_OUT" | sed 's/^/      /'
        SAPI="$(silo_field "$SNMD" api_port)"
        SPW="$(silo_field "$SNMD" root_password)"
        run_grep "summary announces SNMD mode" "Multi-drive mode (SNMD): $SNMD_DRIVES drives" echo "$SNMD_OUT"
        run_grep "summary maps drive 1 to /data1" "Drive 1:       $D1 -> /data1" echo "$SNMD_OUT"
        run_grep "summary maps drive 4 to /data4" "Drive 4:       $D4 -> /data4" echo "$SNMD_OUT"
        run_test "SNMD container running" sl_up "$SNMD"
        run_test "SNMD health endpoint live over TLS" wait_live "$SAPI"

        CNAME="pgcli-silo-$NAMESPACE-$SNMD"
        MOUNTS="$(podman inspect "$CNAME" --format '{{range .Mounts}}{{.Source}}={{.Destination}} {{end}}' 2>/dev/null || true)"
        CMD="$(podman inspect "$CNAME" --format '{{join .Config.Cmd " "}}' 2>/dev/null || true)"
        TESTS=$((TESTS + 1))
        if echo "$MOUNTS" | grep -qF "$D1=/data1" && echo "$MOUNTS" | grep -qF "$D2=/data2" \
           && echo "$MOUNTS" | grep -qF "$D3=/data3" && echo "$MOUNTS" | grep -qF "$D4=/data4"; then
            pass "4 drive mounts at /data1../data4"
        else
            fail "drive mounts wrong: $MOUNTS"
        fi
        run_grep "server argv is 'server /data1..4'" "server /data1 /data2 /data3 /data4" echo "$CMD"
        TESTS=$((TESTS + 1))
        podman inspect "$CNAME" --format '{{json .Config.Env}}' | grep -qF MINIO_SERVER_URL \
            && pass "MINIO_SERVER_URL present (single node)" \
            || fail "MINIO_SERVER_URL missing on an SNMD container"
        # silo formatted every drive — the EC set really spans all four.
        TESTS=$((TESTS + 1))
        all_fmt=true
        for d in "$D1" "$D2" "$D3" "$D4"; do
            [ -n "$(ls -A "$d" 2>/dev/null)" ] || { all_fmt=false; echo "      | drive $d still empty (not formatted)"; }
        done
        $all_fmt && pass "every drive was written by silo" \
                 || fail "a drive was not formatted by silo"

        run_test "mcli alias set against the SNMD store" \
            pgmcli alias set "$SNMD_ALIAS" "https://127.0.0.1:$SAPI" admin "$SPW"
        run_grep "mcli mb through SNMD store" "$BUCKET" pgmcli mb "$SNMD_ALIAS/$BUCKET"
        echo "snmd silo payload $$" > "$TEST_DIR/snmd.txt"
        run_grep "mcli cp upload through SNMD store" "snmd.txt" \
            pgmcli cp "$TEST_DIR/snmd.txt" "$SNMD_ALIAS/$BUCKET/snmd.txt"
        run_test "mcli cp download byte-identical" \
            bash -c "'$BINARY' -c '$CONFIG_FILE' mcli cp '$SNMD_ALIAS/$BUCKET/snmd.txt' '$TEST_DIR/snmd.down' 2>/dev/null && cmp -s '$TEST_DIR/snmd.txt' '$TEST_DIR/snmd.down'"

        run_grep "addon list shows SNMD drives" "Drives:      $SNMD_DRIVES (SNMD)" pg addon list
        run_grep "addon list maps the /data1 slot" "/data1 <- $D1" pg addon list

        # --clean-data must REFUSE the live mount points.
        TESTS=$((TESTS + 1))
        RM_OUT="$(pg addon remove silo --name "$SNMD" --clean-data 2>&1)" || true
        echo "$RM_OUT" | sed 's/^/      /'
        if echo "$RM_OUT" | grep -qF "Refusing to delete $D1" && [ -n "$(ls -A "$D1")" ]; then
            pass "--clean-data refuses still-mounted drives, data intact"
        else
            fail "--clean-data did not refuse the live mounts (or deleted through them)"
        fi
        run_test "SNMD container gone after refused remove" sl_gone "$SNMD"

        # Reinstall over the SAME formatted drives: config entry was deleted by
        # the refused remove, so re-read the fresh password; the bucket survives.
        TESTS=$((TESTS + 1))
        if RESNMD="$(pg addon install silo --name "$SNMD" --tls \
                --drive "$D1" --drive "$D2" --drive "$D3" --drive "$D4" 2>&1)" \
           && echo "$RESNMD" | grep -qF "Starting silo container"; then
            pass "reinstall on the same 4 drives recreates the container"
        else
            fail "reinstall on same drives"; echo "$RESNMD" | sed 's/^/      | /'
        fi
        SAPI="$(silo_field "$SNMD" api_port)"
        SPW="$(silo_field "$SNMD" root_password)"
        run_test "healthy after reinstall" wait_live "$SAPI"
        pgmcli alias set "$SNMD_ALIAS" "https://127.0.0.1:$SAPI" admin "$SPW" >/dev/null 2>&1 || true
        run_grep "bucket survived remove + recreate" "$BUCKET" pgmcli ls "$SNMD_ALIAS"

        run_test "stop SNMD" pg addon stop silo --name "$SNMD"
        run_test "SNMD stopped" sl_down "$SNMD"
        run_test "start SNMD" pg addon start silo --name "$SNMD"
        run_test "healthy after start" wait_live "$SAPI"
        run_grep "bucket survived the stop/start cycle" "$BUCKET" pgmcli ls "$SNMD_ALIAS"

        # Plain remove keeps every drive dir with its format.
        run_test "remove SNMD keeps the drive dirs" pg addon remove silo --name "$SNMD"
        run_test "SNMD container gone" sl_gone "$SNMD"
        TESTS=$((TESTS + 1))
        [ -n "$(ls -A "$D1")" ] && [ -n "$(ls -A "$D4")" ] \
            && pass "drive formats intact after plain remove" \
            || fail "a drive format vanished with the container"
        pgmcli alias remove "$SNMD_ALIAS" >/dev/null 2>&1 || true

        # Unmount, reinstall, then --clean-data over the (now plain) dirs
        # deletes them all.
        snmd_umount_all
        TESTS=$((TESTS + 1))
        grep -q snmd-mnt /proc/mounts && fail "loop mounts still present" || pass "loop mounts released"
        TESTS=$((TESTS + 1))
        if pg addon install silo --name "$SNMD" --tls \
               --drive "$D1" --drive "$D2" --drive "$D3" --drive "$D4" >/dev/null 2>&1; then
            pass "install over the unmounted (reused) drive dirs"
        else
            fail "install over the unmounted drive dirs"
        fi
        run_test "remove --clean-data deletes the drive dirs" \
            pg addon remove silo --name "$SNMD" --clean-data
        TESTS=$((TESTS + 1))
        [ ! -d "$D1" ] && [ ! -d "$D4" ] \
            && pass "all drive dirs deleted by clean-data" \
            || fail "drive dirs survived clean-data once unmounted"
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
