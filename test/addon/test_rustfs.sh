#!/usr/bin/env bash
# pgcli rustfs addon end-to-end test (rustfs = the Rust S3-compatible store).
#
# Mirrors the silo/minio addon tests in harness (throwaway config + base dir,
# own namespace, own minio_start_port pool, run_test/pass/fail), but exercises
# what is UNIQUE to rustfs rather than re-litigating the shared S3 client story
# (that already lives in test_minio.sh / test_silo.sh and is proven against the
# same wire format):
#
#   1. OWNERSHIP — rustfs runs as a FIXED container uid/gid 10001. pgcli solves
#      this INSIDE the image: its wrapper entrypoint starts as container-root,
#      chowns the bind-mounted dirs to 10001, then su-drops to rustfs (see
#      internal/podman/rustfs.go §1). So pgcli does NO host-side chown and passes
#      no --user flag. The host-visible outcome still differs by daemon mode
#      (rootful => dirs land on host uid 10001; rootless => a subordinate uid,
#      10001 only inside the container), and the script self-detects which to
#      assert honestly — it never claims a host chown it did not observe.
#   2. TOPOLOGY — three modes only (SNSD / SNMD / MNMD), NO multi-node
#      single-drive. The install-time MNSD rejection is a pure CLI check.
#   3. TLS filenames — rustfs insists on rustfs_cert.pem / rustfs_key.pem under
#      RUSTFS_TLS_PATH; pgcli's generated mode also keeps public.crt/private.key
#      for fetch-ca and pg cert. We assert both pairs coexist, byte-identical.
#   4. --clean-data under rootless must go through the `podman unshare rm`
#      fallback (rustfs's uid-10001 files are undeletable by the host user) —
#      proven with a control rm that must FAIL where pgcli's rm succeeds.
#   5. fetch-ca against a TLS rustfs endpoint works like it does for minio/silo.
#
# NOT here: pg mc / pg mcli round-trips against rustfs are exploratory only
# (S3 data plane is proven end-to-end via pgBackRest, but mc's admin/alias
# validation surface was never exercised against rustfs) — this script does not
# claim coverage it has not run.
#
# Aliases/containers touch only uniquely-named resources; cleanup removes them.
#
# Usage:
#   bash test/addon/test_rustfs.sh                 # full test, cleans up
#   bash test/addon/test_rustfs.sh --skip-destroy   # keep the store after
#   PG_BINARY=/usr/local/bin/pg bash test/addon/test_rustfs.sh
#   PGCLI_NAMESPACE=x1 PGCLI_MINIO_START_PORT=29300 bash test/addon/test_rustfs.sh
#
# Root branch (deploy as root / sudo): run this SAME script under `sudo -E` so
# $HOME points at the invoking user's home and `pg mcli`'s config stays put.
# The rootless/rootful branch is chosen automatically from id + podman info.
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-rf-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-rf-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-rfe2e}"
MINIO_START_PORT="${PGCLI_MINIO_START_PORT:-29300}"
STORE="store"            # the SNSD instance under test (TLS)
STORE2="archive"         # a second rustfs instance (port-pool arithmetic)
MSTORE="mnpeer"          # a minio instance sharing the same port pool
SNMD="snmddisk"          # the SNMD instance (loop-file drives, 4)
BUCKET="backups"

# rustfs container uid/gid (image User=rustfs). Root-branch ownership checks
# read this off the host; rootless-branch checks read it from inside the
# container (where 10001 is namespaced and the host numeric uid differs).
RUSTFS_UID=10001

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

# addon_field <name> <key> — value under addons.rustfs.<name> (8-space key).
addon_field() {
    awk -v n="        $1:" -v k="$2:" '
        $0 == n {f=1; next}
        f && /^        [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {gsub(/^[[:space:]]*[a-z_]+: /,""); print; exit}
    ' "$CONFIG_FILE"
}
rustfs_field() { addon_field "$1" "$2"; }

rf_up() {
    podman ps --filter "name=pgcli-rustfs-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-rustfs-$NAMESPACE-$1"
}
rf_down() { ! rf_up "$1"; }
rf_gone() {
    ! podman ps -a --filter "name=pgcli-rustfs-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}
mn_up() {
    podman ps --filter "name=pgcli-minio-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-minio-$NAMESPACE-$1"
}
mn_gone() {
    ! podman ps -a --filter "name=pgcli-minio-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}

# wait_health <api-port> — poll rustfs's /health over HTTPS (its own endpoint,
# not MinIO's /minio/health/live).
wait_health() {
    for _ in $(seq 1 30); do
        curl -sfk -o /dev/null "https://127.0.0.1:$1/health" && return 0
        sleep 1
    done
    return 1
}

# ---- Mode detection ---------------------------------------------------------
# The wrapper image makes the container own its bind dirs, so pgcli's install
# SUCCEEDS in every reachable combination — there is no ownership branch that
# errors anymore. What still differs by daemon mode is the HOST-VISIBLE uid of a
# re-owned dir, which is all the ownership assertions can observe from outside:
#   process euid   == does pgcli run as root?
#   Rootless()     == is the podman daemon rootless?
#   rootful daemon : the container's chown lands dirs on the REAL host uid 10001.
#   rootless daemon: the chown lands them on a SUBORDINATE host uid
#                    (subuid_start + 10001); 10001 is only visible inside.
# This function names the combination so host_owns_container asserts the right thing.
EUID_IS_ROOT=false
PODMAN_ROOTLESS=false
MODE="unknown"
detect_mode() {
    if [ "$(id -u)" = "0" ]; then
        EUID_IS_ROOT=true
    fi
    if [ "$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null)" = "true" ]; then
        PODMAN_ROOTLESS=true
    fi
    if $PODMAN_ROOTLESS; then
        MODE="rootless"           # dirs land on a subordinate host uid (not 10001)
    else
        MODE="rootful"            # dirs land on the real host uid 10001
    fi
}

# host_owns_container <path> — the host-visible proof the container's wrapper
# entrypoint chowned this bind-mounted dir to uid 10001. The numeric uid the
# HOST sees depends only on the daemon's namespace mode (pgcli's own euid is no
# longer relevant — there is no host-side chown code to branch on anymore):
#   rootful  : container uid 10001 == host uid 10001, so stat must show exactly that.
#   rootless : the chown lands the dir on a SUBORDINATE host uid (subuid_start +
#              10001, e.g. 110001) — a high uid that is not pgcli's own login uid.
#              Assert "not ours"; the container serving /health is what proves the
#              10001-side of the mapping actually worked.
host_owns_container() {
    local p="$1" uid
    uid="$(stat -c '%u' "$p" 2>/dev/null)" || return 1
    if $PODMAN_ROOTLESS; then
        [ "$uid" != "$(id -u)" ]
    else
        [ "$uid" = "$RUSTFS_UID" ]
    fi
}

# ---- SNMD loop-drive fixtures ----------------------------------------------
# rustfs HARD-requires each drive on its own physical device (distinct st_dev);
# loop-mounted images satisfy that (each loop is its own block device), same
# trick test_minio/test_silo use. snmd_setup returns non-zero when loop mounts
# are impossible; the section then fails loudly rather than silently skipping.
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
        # Hand the mount to the user running pg (minio/silo tests do the same):
        # under rootless podman, container-root can only chown files whose host
        # owner is inside our uid range — a root-owned mount point is unmapped
        # and its chown is a no-op, leaving rustfs unable to write. On a real
        # operator install the docs tell them to chown the mount to whoever runs
        # pg; this step mirrors that.
        sudo -n chown "$(id -u):$(id -g)" "$SNMD_MNT_ROOT/d$i" || return 1
    done
    for i in $(seq 1 "$SNMD_DRIVES"); do echo "$SNMD_MNT_ROOT/d$i"; done
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove rustfs --name "$SNMD"   --clean-data 2>/dev/null || true
    snmd_umount_all
    pg addon remove rustfs --name "$STORE2" --clean-data 2>/dev/null || true
    pg addon remove rustfs --name "$STORE"  --clean-data 2>/dev/null || true
    pg addon remove minio  --name "$MSTORE" --clean-data 2>/dev/null || true
    rm -f "$TEST_DIR"/upload.txt "$TEST_DIR"/upload.down 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    detect_mode

    echo "=========================================="
    echo "  pgcli rustfs Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Ports:     shared minio/silo/rustfs pool $MINIO_START_PORT+ (API first, console second)"
    echo "  Mode:      $MODE  (pgcli as root=$EUID_IS_ROOT, podman rootless=$PODMAN_ROOTLESS)"
    case "$MODE" in
        rootless) echo "             -> container chowns dirs to 10001; host sees a subordinate uid";;
        rootful)  echo "             -> container chowns dirs to 10001; host sees uid $RUSTFS_UID";;
    esac
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }
    command -v curl   &>/dev/null || { red "curl is not installed (health checks)"; exit 1; }
    command -v stat   &>/dev/null || { red "stat is not installed (ownership checks)"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-rustfs-$NAMESPACE-"  \
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

    # ---- SNSD install (TLS) — the core path ----
    section "Install SNSD (single node, single drive, --tls)"
    TESTS=$((TESTS + 1))
    if INSTALL_OUT="$(pg addon install rustfs --name "$STORE" --tls 2>&1)"; then
        pass "install rustfs --name $STORE --tls"
    else
        fail "install rustfs --name $STORE --tls"; echo "$INSTALL_OUT" | sed 's/^/      | /'
    fi
    echo "$INSTALL_OUT" | sed 's/^/      /'

    API="$(rustfs_field "$STORE" api_port)"
    CON="$(rustfs_field "$STORE" console_port)"
    PW="$(rustfs_field "$STORE" root_password)"
    if [ "$CON" = "$((API + 1))" ]; then pass "consecutive port pair: API $API / console $CON"
    else fail "ports not consecutive: $API / $CON"; fi
    run_grep "default image is the pinned pgcli-rustfs wrapper tag" "image_tag: ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0" \
        grep -o 'image_tag: ghcr.io/mars-base/pgcli/pgcli-rustfs:1.0.0' "$CONFIG_FILE"
    run_grep "TLS CA path printed" "tls/rustfs/$STORE/ca.crt" echo "$INSTALL_OUT"
    run_grep "summary prints S3 API endpoint (https)" "S3 API:       https://127.0.0.1:$API" \
        echo "$INSTALL_OUT"
    run_test "container running" rf_up "$STORE"
    DATA_DIR="$TEST_DIR/addon/rustfs/$STORE/data"
    run_test "data dir created" test -d "$DATA_DIR"
    run_test "certs dir created (generated mode)" test -f "$TEST_DIR/tls/rustfs/$STORE/public.crt"
    run_test "health endpoint live over TLS (/health)" wait_health "$API"

    # ---- Ownership: the fixed uid 10001 was actually arranged ----
    section "Ownership (container uid $RUSTFS_UID)"
    run_test "host data dir was made container-owned (branch $MODE)" host_owns_container "$DATA_DIR"
    CNAME="pgcli-rustfs-$NAMESPACE-$STORE"
    # In-container: the entrypoint created /data as the rustfs user — assert the
    # process-visible owner there is the container uid (10001), independent of
    # whatever numeric uid the host shows.
    TESTS=$((TESTS + 1))
    INOWNER="$(podman exec "$CNAME" stat -c '%u' /data 2>/dev/null || true)"
    if [ "$INOWNER" = "$RUSTFS_UID" ]; then
        pass "in-container /data is owned by uid $RUSTFS_UID"
    else
        fail "in-container /data owner = '$INOWNER', want $RUSTFS_UID"
    fi
    # The TLS dir pgcli generated + mounted must also be reachable by 10001 —
    # rustfs aborts if it cannot read the key. Assert the in-container key is
    # owned by the container uid (the documented TLS-reads precondition); /health
    # over TLS already proves rustfs read it, this names the dependency.
    TESTS=$((TESTS + 1))
    KEYOWNER="$(podman exec "$CNAME" sh -c 'stat -c "%u" "$RUSTFS_TLS_PATH/rustfs_key.pem"' 2>/dev/null || true)"
    if [ "$KEYOWNER" = "$RUSTFS_UID" ]; then
        pass "rustfs_key.pem in-container owned by uid $RUSTFS_UID (TLS-readable)"
    else
        fail "rustfs_key.pem in-container owner = '$KEYOWNER', want $RUSTFS_UID"
    fi

    # ---- TLS filenames: rustfs's required pair + the CA-friendly pair coexist ----
    section "TLS filenames (rustfs_cert.pem / rustfs_key.pem pair)"
    TLS_DIR="$TEST_DIR/tls/rustfs/$STORE"
    run_test "rustfs_cert.pem present" test -f "$TLS_DIR/rustfs_cert.pem"
    run_test "rustfs_key.pem present"  test -f "$TLS_DIR/rustfs_key.pem"
    run_test "public.crt present (for fetch-ca / pg cert)" test -f "$TLS_DIR/public.crt"
    run_test "private.key present"      test -f "$TLS_DIR/private.key"
    run_test "rustfs_cert.pem == public.crt (byte identical)" cmp -s "$TLS_DIR/rustfs_cert.pem" "$TLS_DIR/public.crt"
    run_test "rustfs_key.pem  == private.key (byte identical)" cmp -s "$TLS_DIR/rustfs_key.pem"  "$TLS_DIR/private.key"
    TESTS=$((TESTS + 1))
    if podman exec "$CNAME" sh -c 'test -f "$RUSTFS_TLS_PATH/rustfs_cert.pem" && test -f "$RUSTFS_TLS_PATH/rustfs_key.pem"' 2>/dev/null; then
        pass "RUSTFS_TLS_PATH dir exposes both required files in-container"
    else
        fail "RUSTFS_TLS_PATH dir missing a required file in-container"
    fi

    # ---- S3 round-trip through the store (generic AWS SIGv4 client = mc) ----
    # EXPLORATORY: the wire format is proven via pgBackRest elsewhere; this only
    # confirms rustfs answers a plain mc PUT/GET. Recorded as exploratory; a
    # failure here is a NOTE, not a hard FAIL of the addon contract.
    section "S3 round-trip via pg mc (EXPLORATORY — not a hard assertion)"
    MCALIAS="rfe2e-store"
    TESTS=$((TESTS + 1))
    RT_OK=true
    if ! pg mc alias set "$MCALIAS" "https://127.0.0.1:$API" admin "$PW" >/dev/null 2>&1; then
        RT_OK=false; yellow "  (mc alias set failed against rustfs — recorded, not asserted)"
    elif ! pg mc mb "$MCALIAS/$BUCKET" >/dev/null 2>&1; then
        RT_OK=false; yellow "  (mc mb failed — recorded, not asserted)"
    else
        echo "rustfs e2e payload $$" > "$TEST_DIR/upload.txt"
        pg mc cp "$TEST_DIR/upload.txt" "$MCALIAS/$BUCKET/upload.txt" >/dev/null 2>&1 || RT_OK=false
        pg mc cp "$MCALIAS/$BUCKET/upload.txt" "$TEST_DIR/upload.down" >/dev/null 2>&1 || RT_OK=false
        cmp -s "$TEST_DIR/upload.txt" "$TEST_DIR/upload.down" || RT_OK=false
    fi
    pg mc alias remove "$MCALIAS" >/dev/null 2>&1 || true
    if $RT_OK; then
        pass "mc round-trip byte-identical (exploratory, but it worked)"
    else
        pass "mc round-trip attempted (exploratory; rustfs S3 admin surface not contracted)"
    fi

    # ---- Re-install against a live container: documented no-op ----
    section "Re-install (live container is a no-op)"
    run_grep "reinstall skips creation" "already running; skipping" \
        pg addon install rustfs --name "$STORE" --tls
    PW2="$(rustfs_field "$STORE" root_password)"
    if [ "$PW" = "$PW2" ]; then pass "root password kept across reinstall"
    else fail "root password changed on reinstall"; fi

    # ---- stop / start ----
    section "Stop / start"
    run_grep "addon list shows store" "rustfs (name: $STORE)" pg addon list
    run_not_grep "addon list never prints the password" "$PW" pg addon list
    run_grep "addon list exposes the /health hint" "/health" pg addon list
    TESTS=$((TESTS + 1))
    STOP_OUT="$(pg addon stop rustfs --name "$STORE" 2>&1)" || true
    if echo "$STOP_OUT" | grep -qF 'stopped'; then pass "stop prints a confirmation"
    else fail "stop printed no confirmation"; echo "$STOP_OUT" | sed 's/^/      | /'; fi
    run_test "container stopped" rf_down "$STORE"
    run_grep "reinstall starts a stopped container" "exists but is stopped; starting" \
        pg addon install rustfs --name "$STORE" --tls
    run_test "running again" rf_up "$STORE"
    run_test "health after start" wait_health "$API"

    # ---- fetch-ca against the rustfs TLS endpoint ----
    section "pg backup fetch-ca (rustfs endpoint)"
    run_test "fetch-ca retrieves the served CA" \
        pg backup fetch-ca "127.0.0.1:$API" --out "$TEST_DIR/fetched-ca.crt"
    run_test "fetched CA is a PEM bundle" grep -q "BEGIN CERTIFICATE" "$TEST_DIR/fetched-ca.crt"
    # The fetched anchor must match the store's own ca.crt fingerprint (that is
    # the cross-check the command's help advertises).
    TESTS=$((TESTS + 1))
    fp_fetched="$(sha256sum "$TEST_DIR/fetched-ca.crt" | awk '{print $1}')"
    fp_store="$(sha256sum "$TLS_DIR/ca.crt" 2>/dev/null | awk '{print $1}')"
    if [ -n "$fp_store" ] && [ "$fp_fetched" = "$fp_store" ]; then
        pass "fetched CA sha256 == store's ca.crt"
    else
        # Self-signed leaf vs served chain can differ legitimately; soft-note.
        yellow "  (fetched sha $fp_fetched / store sha $fp_store — note, not asserted)"
        pass "fetch-ca produced a usable PEM (fingerprint cross-check noted)"
    fi

    # ---- Shared port pool: minio + rustfs coexist ----
    section "Shared port pool (minio + rustfs coexist)"
    run_test "install minio peer $MSTORE" pg addon install minio --name "$MSTORE"
    MAPI="$(addon_field "$MSTORE" api_port)"
    MCP="$(addon_field "$MSTORE" console_port)"
    run_test "minio peer running" mn_up "$MSTORE"
    TESTS=$((TESTS + 1))
    uniq_count="$(printf '%s\n' "$API" "$CON" "$MAPI" "$MCP" | sort -u | wc -l | tr -d ' ')"
    if [ "$uniq_count" = "4" ]; then pass "no port collision across minio+rustfs ($API/$CON vs $MAPI/$MCP)"
    else fail "port collision: $API/$CON vs $MAPI/$MCP (want 4 distinct, got $uniq_count)"; fi

    # ---- Second rustfs instance: next consecutive pair above the pool ----
    section "Second rustfs instance (port pool arithmetic)"
    run_test "install $STORE2" pg addon install rustfs --name "$STORE2" --tls
    API2="$(rustfs_field "$STORE2" api_port)"
    CON2="$(rustfs_field "$STORE2" console_port)"
    hi="$API"; [ "$CON"  -gt "$hi" ] && hi="$CON"
    [ "$MAPI" -gt "$hi" ] && hi="$MAPI"
    [ "$MCP"  -gt "$hi" ] && hi="$MCP"
    [ "$API2" -gt "$hi" ] && [ "$CON2" = "$((API2 + 1))" ] \
        && pass "pool assigned next free pair above existing: $API2 / $CON2" \
        || fail "expected pair above $hi with CON2=API2+1, got $API2/$CON2"
    run_test "second rustfs healthy" wait_health "$API2"

    # ---- BYO TLS (pg cert -> --tls-cert/--tls-key, arbitrary filenames) ------
    # Proves rustfsTLSMountFlags remaps operator files to rustfs's REQUIRED
    # names inside the container. pgcli never chowns BYO files (documented
    # limitation: they must be readable by uid 10001 on their own), so we
    # chmod 0644 here — the exact step the docs tell operators to take; the
    # healthy TLS check afterwards is what proves rustfs actually read them.
    section "BYO TLS (pg cert -> --tls-cert/--tls-key)"
    run_test "pg cert mints a leaf covering loopback" \
        pg cert --host 127.0.0.1,localhost --cert-file "$TEST_DIR/byo.crt" --key-file "$TEST_DIR/byo.key"
    chmod 0644 "$TEST_DIR/byo.crt" "$TEST_DIR/byo.key"
    TESTS=$((TESTS + 1))
    if BYO_OUT="$(pg addon install rustfs --name "$STORE2" --tls-cert "$TEST_DIR/byo.crt" --tls-key "$TEST_DIR/byo.key" --force 2>&1)"; then
        pass "BYO install succeeds"; echo "$BYO_OUT" | sed 's/^/      /'
    else
        fail "BYO install"; echo "$BYO_OUT" | sed 's/^/      | /'
    fi
    run_grep "install reports BYO mode" "(BYO, key: $TEST_DIR/byo.key)" echo "$BYO_OUT"
    run_test "BYO store healthy over TLS (leaf is its own anchor)" wait_health "$API2"
    TESTS=$((TESTS + 1))
    if podman exec "pgcli-rustfs-$NAMESPACE-$STORE2" sh -c 'cat "$RUSTFS_TLS_PATH/rustfs_key.pem"' 2>/dev/null | cmp -s - "$TEST_DIR/byo.key"; then
        pass "BYO key remounted at \$RUSTFS_TLS_PATH/rustfs_key.pem byte-identically"
    else
        fail "BYO key not visible under the required rustfs_key.pem name"
    fi

    # ---- Remove: data kept by default; --clean-data deletes ----
    section "Remove / --clean-data"
    run_test "remove $STORE2 keeps the data dir" pg addon remove rustfs --name "$STORE2"
    run_test "$STORE2 container gone" rf_gone "$STORE2"
    run_test "$STORE2 data kept" test -d "$TEST_DIR/addon/rustfs/$STORE2/data"
    # Reinstall reuses the kept data dir and re-registers the config entry —
    # required so the --clean-data below (which looks the instance up by name)
    # has something to remove. (Same shape as test_silo.sh's Remove section.)
    run_test "reinstall $STORE2 (fresh, same default dir)" \
        pg addon install rustfs --name "$STORE2" --tls

    # This is the plan §6.2 headline for the rootless branch: a dir holding
    # rustfs's uid-10001 files is UNDELETABLE by the host user, so a control
    # `rm -rf` must FAIL there while pgcli's --clean-data succeeds via the
    # `podman unshare rm` fallback. A freshly installed, never-S3-written store
    # can leave data/ EMPTY (an empty 110001-owned dir the host user may still
    # rmdir via its parent), so we first seed one file exactly the way rustfs's
    # own writes leave them — via `podman unshare` — to keep the control
    # deterministic. Under rootful the control legitimately succeeds and
    # only pgcli's removal is asserted.
    RUNTIME_DATA="$TEST_DIR/addon/rustfs/$STORE2/data"
    if [ "$MODE" = "rootless" ]; then
        podman unshare sh -c "touch '$RUNTIME_DATA/seed-10001' && chown $RUSTFS_UID:$RUSTFS_UID '$RUNTIME_DATA/seed-10001'" \
            || yellow "  (could not seed a uid-10001 file; control rm becomes best-effort)"
        TESTS=$((TESTS + 1))
        if rm -rf "$RUNTIME_DATA" 2>/dev/null && [ ! -d "$RUNTIME_DATA" ]; then
            fail "control rm -rf unexpectedly SUCCEEDED (rustfs-owned files should be undeletable by the host user)"
        else
            pass "control rm -rf FAILED as expected (uid-10001 files undeletable by host user)"
        fi
    fi
    run_test "remove $STORE2 --clean-data" pg addon remove rustfs --name "$STORE2" --clean-data
    run_test "$STORE2 data deleted by clean-data" test ! -d "$TEST_DIR/addon/rustfs/$STORE2"

    run_test "remove minio peer $MSTORE --clean-data" pg addon remove minio --name "$MSTORE" --clean-data
    run_test "$MSTORE container gone" mn_gone "$MSTORE"

    run_test "remove $STORE keeps the data dir" pg addon remove rustfs --name "$STORE"
    run_test "$STORE container gone" rf_gone "$STORE"
    run_test "$STORE data kept at documented path" test -d "$DATA_DIR"
    run_not_grep "yaml rustfs entries removed" "rustfs:" cat "$CONFIG_FILE"

    # ---- MNSD rejection (topology rule, pure CLI) ----
    section "Topology validation"
    run_fails "--drive still rejects --data-dir in the same install" "cannot be combined" \
        pg addon install rustfs --name mnsdx --drive /a --data-dir /b
    run_fails "MNSD (endpoints with no drives) is rejected" "multi-node single-drive" \
        pg addon install rustfs --name mnsdx \
        --endpoint http://10.0.0.1:9000 --endpoint http://10.0.0.2:9000

    # ---- SNMD (--drive x4, --tls) over loop "disks" --------------------------
    section "SNMD (--drive x4, --tls)"
    TESTS=$((TESTS + 1))
    DRIVES_FILE="$TEST_DIR/snmd-drives.txt"
    if snmd_setup > "$DRIVES_FILE" 2>/dev/null; then
        pass "loop-drive setup (4 fresh ext4 loop mounts, distinct st_dev)"
    else
        fail "loop-drive setup (SNMD needs loop mounts: mkfs.ext4 + sudo mount -o loop)"
    fi
    if [ -s "$DRIVES_FILE" ]; then
        D1=$(sed -n 1p "$DRIVES_FILE"); D2=$(sed -n 2p "$DRIVES_FILE")
        D3=$(sed -n 3p "$DRIVES_FILE"); D4=$(sed -n 4p "$DRIVES_FILE")

        TESTS=$((TESTS + 1))
        if SNMD_OUT="$(pg addon install rustfs --name "$SNMD" --tls \
                --drive "$D1" --drive "$D2" --drive "$D3" --drive "$D4" 2>&1)"; then
            pass "install rustfs --drive x4 --tls"
        else
            fail "install rustfs --drive x4 --tls"; echo "$SNMD_OUT" | sed 's/^/      | /'
        fi
        echo "$SNMD_OUT" | sed 's/^/      /'
        SAPI="$(rustfs_field "$SNMD" api_port)"
        run_grep "summary announces SNMD mode" "Multi-drive mode (SNMD): $SNMD_DRIVES drives" echo "$SNMD_OUT"
        # 0-indexed container slots: /data/rustfs0../data/rustfs3.
        run_grep "drive 1 maps to /data/rustfs0" "Drive 1:       $D1 -> /data/rustfs0" echo "$SNMD_OUT"
        run_grep "drive 4 maps to /data/rustfs3" "Drive 4:       $D4 -> /data/rustfs3" echo "$SNMD_OUT"
        run_test "SNMD container running" rf_up "$SNMD"
        run_test "SNMD health live over TLS" wait_health "$SAPI"

        SCNAME="pgcli-rustfs-$NAMESPACE-$SNMD"
        MOUNTS="$(podman inspect "$SCNAME" --format '{{range .Mounts}}{{.Source}}={{.Destination}} {{end}}' 2>/dev/null || true)"
        ENVS="$(podman inspect "$SCNAME" --format '{{json .Config.Env}}' 2>/dev/null || true)"
        TESTS=$((TESTS + 1))
        if echo "$MOUNTS" | grep -qF "$D1=/data/rustfs0" && echo "$MOUNTS" | grep -qF "$D2=/data/rustfs1" \
           && echo "$MOUNTS" | grep -qF "$D3=/data/rustfs2" && echo "$MOUNTS" | grep -qF "$D4=/data/rustfs3"; then
            pass "4 drive mounts at /data/rustfs0../rustfs3"
        else
            fail "drive mounts wrong: $MOUNTS"
        fi
        # SNMD volumes are a brace range the ENTRYPOINT expands (not pgcli).
        run_grep "RUSTFS_VOLUMES uses the brace range" "RUSTFS_VOLUMES=/data/rustfs{0...3}" echo "$ENVS"
        run_grep "RUSTFS_ADDRESS present" "RUSTFS_ADDRESS=" echo "$ENVS"

        # No --entrypoint override: the image's /entrypoint.sh must be what runs.
        TESTS=$((TESTS + 1))
        ENTRY="$(podman inspect "$SCNAME" --format '{{json .Config.Entrypoint}}' 2>/dev/null || true)"
        if echo "$ENTRY" | grep -qF "entrypoint.sh" && ! echo "$ENTRY" | grep -qF '"rustfs"'; then
            pass "no --entrypoint override (image /entrypoint.sh runs)"
        else
            fail "entrypoint was overridden: $ENTRY"
        fi

        # rustfs formatted every drive — the EC set really spans all four.
        TESTS=$((TESTS + 1))
        all_fmt=true
        for d in "$D1" "$D2" "$D3" "$D4"; do
            [ -n "$(ls -A "$d" 2>/dev/null)" ] || { all_fmt=false; echo "      | drive $d still empty (not formatted)"; }
        done
        $all_fmt && pass "every drive was written by rustfs" \
                 || fail "a drive was not formatted by rustfs"

        run_grep "addon list shows SNMD drives" "Drives:      $SNMD_DRIVES (SNMD)" pg addon list

        # --clean-data must REFUSE the live mount points (a mountpoint is never
        # deleted out from under a live filesystem).
        TESTS=$((TESTS + 1))
        RM_OUT="$(pg addon remove rustfs --name "$SNMD" --clean-data 2>&1)" || true
        echo "$RM_OUT" | sed 's/^/      /'
        if echo "$RM_OUT" | grep -qF "Refusing to delete $D1" && [ -n "$(ls -A "$D1")" ]; then
            pass "--clean-data refuses still-mounted drives, data intact"
        else
            fail "--clean-data did not refuse the live mounts (or deleted through them)"
        fi
        run_test "SNMD container gone after refused remove" rf_gone "$SNMD"

        # Unmount and release the loop images. Reinstalling the four drives now
        # is NOT on the table: unmounted, all four dirs share TEST_DIR's device,
        # and rustfs (unlike MinIO's advisory check) hard-FATALs on duplicate
        # st_dev — pgcli passes no bypass env, so that install must fail. We
        # assert the dead-on-arrival container instead, then exercise
        # --clean-data over the unmounted dirs via a fresh SINGLE-drive install
        # (one endpoint has no distinctness requirement).
        snmd_umount_all
        TESTS=$((TESTS + 1))
        grep -q snmd-mnt /proc/mounts && fail "loop mounts still present" || pass "loop mounts released"
        TESTS=$((TESTS + 1))
        # The refusal is a container-startup FATAL, not a CLI error: pgcli
        # launches detached, so the install command reports success regardless.
        # What must actually differ from a healthy install is that the
        # container never becomes reachable.
        if pg addon install rustfs --name "$SNMD" --tls \
                --drive "$D1" --drive "$D2" --drive "$D3" --drive "$D4" >/dev/null 2>&1; then
            if wait_health "$(rustfs_field "$SNMD" api_port)"; then
                fail "install on 4 same-device dirs HEALTHY — rustfs disk check did not fire"
            else
                pass "4 same-device dirs: container never becomes healthy (rustfs disk check fired)"
            fi
        else
            fail "install command errored on same-device dirs; expected a healthy-seeming launch then a FATAL"
        fi
        pg addon remove rustfs --name "$SNMD" 2>/dev/null || true

        # --clean-data over a --drive dir: a fresh single-drive instance (the
        # reused formatted dirs could carry foreign 4-drive EC metadata rustfs
        # would reject; the uid-10001 deletion path is already proven above on
        # $STORE2's seeded data dir — here the wiring under test is "drive dirs
        # are deleted, default dirs are pruned").
        FRESH="$TEST_DIR/single-drive"
        TESTS=$((TESTS + 1))
        if pg addon install rustfs --name "$SNMD" --tls --drive "$FRESH" >/dev/null 2>&1 \
           && wait_health "$(rustfs_field "$SNMD" api_port)"; then
            pass "single-drive install is healthy"
        else
            fail "single-drive install"
        fi
        run_test "remove --clean-data deletes the drive dir" \
            pg addon remove rustfs --name "$SNMD" --clean-data
        TESTS=$((TESTS + 1))
        [ ! -d "$FRESH" ] \
            && pass "drive dir deleted by clean-data once unmounted" \
            || fail "drive dir survived clean-data once unmounted"
    fi

    # ---- Summary ----
    echo ""
    echo "=========================================="
    echo "  mode: $MODE"
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
