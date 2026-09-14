#!/usr/bin/env bash
# pgcli MinIO addon — distributed-cluster end-to-end test (4 real hosts).
#
# Unlike every other addon test in this directory, this one cannot run on a
# single host: MinIO's own startup validation rejects same-host endpoint lists
# ("use path style endpoint for single node setup") and the 127.0.0.0/8
# loopback range ("resolves to localhost") outright, so a genuine distributed
# cluster needs four distinct, mutually-routable hosts. This script drives that
# real thing: it builds the pg binary from this checkout, pushes it to each
# node over SSH, then follows site/content/docs/addon/minio.md's
# "Distributed / Cluster Mode" section step by step on each host:
#   pg config init (own base dir, own namespace)
#   pg addon install minio --listen <that node's routable IP>
#     --data-dir <a disk mounted separately from the root filesystem>
#     --root-password <one generated secret, passed identically on every node>
#     --endpoint <n1> --endpoint <n2> --endpoint <n3> --endpoint <n4>
# then asserts, over SSH:
#   every node's pg.yaml carries the byte-identical endpoint list;
#   no node has a MINIO_SERVER_URL env (the exact bug that kept a real 4-node
#     cluster from forming — see internal/podman/minio.go);
#   all four /minio/health/cluster endpoints reach 200 (ring formed) and the
#     log shows "4 drives per set";
#   a 12MB upload shows up as four DIFFERENT shard digests across the four
#     data dirs (real erasure coding, not replication) and downloads back
#     byte-identical through a different peer;
#   stopping 2 of 4 nodes leaves reads working (read quorum ⌈N/2⌉=2) but
#     rejects writes (write quorum ⌈N/2⌉+1=3);
#   restarting the 2 downed nodes self-heals: cluster 200 again, object intact.
#
# Prerequisites (checked up front, never worked around):
#   - passwordless key-based SSH to each of the four IPs (root or sudo-capable);
#   - a data disk already formatted and mounted at MINIO_DATA_DIR on each node,
#     on a device DIFFERENT from the one holding / — MinIO refuses a drive that
#     shares the OS disk ("drive is part of root drive"). This script never
#     formats or mounts anything for you: that is destructive, host-specific
#     setup only an operator should confirm;
#   - podman installed on each node (pgcli's own prerequisite everywhere).
#
# Usage:
#   bash test/addon/test_minio_cluster.sh <ip1> <ip2> <ip3> <ip4>
#   SSH_USER=root SSH_PORT=22 bash test/addon/test_minio_cluster.sh 10.0.0.11 10.0.0.12 10.0.0.20 10.0.0.21
#   MINIO_DATA_DIR=/data/minio PGCLI_NAMESPACE=mnc bash test/addon/test_minio_cluster.sh <ips...>
#   bash test/addon/test_minio_cluster.sh --skip-destroy <ip1> <ip2> <ip3> <ip4>
#
#   SSH_USER (default root) SSH_PORT (default 22)
#   MINIO_DATA_DIR (default /data/minio; must be a separate mount on every node)
#   PGCLI_NAMESPACE (default mnc) PGCLI_MINIO_API_PORT (default 9000)
#   REMOTE_BASE_DIR (default /root/pgcli-mnc-e2e, wiped between runs)
#   PG_BINARY=/usr/local/bin/pg   # use a pre-built binary instead of building
set -euo pipefail

usage() {
    echo "usage: $0 [--skip-destroy] <ip1> <ip2> <ip3> <ip4>"
    exit 1
}

SKIP_DESTROY=false
if [ "${1:-}" = "--skip-destroy" ]; then
    SKIP_DESTROY=true
    shift
fi
[ "$#" -eq 4 ] || usage
NODES=("$@")

SSH_USER="${SSH_USER:-root}"
SSH_PORT="${SSH_PORT:-22}"
MINIO_DATA_DIR="${MINIO_DATA_DIR:-/data/minio}"
NAMESPACE="${PGCLI_NAMESPACE:-mnc}"
API_PORT="${PGCLI_MINIO_API_PORT:-9000}"
CONSOLE_PORT=$((API_PORT + 1))
REMOTE_BASE_DIR="${REMOTE_BASE_DIR:-/root/pgcli-mnc-e2e}"
CONFIG_FILE="$REMOTE_BASE_DIR/pg.yaml"
STORE="store"
BUCKET="ectest"
ROOT_USER="admin"

# Generated once here and passed identically to every node's install via
# --root-password — exactly the distributed-cluster flow from the docs: no
# yaml hand-copying between nodes. Never printed to stdout.
ROOT_PASSWORD="$(head -c 15 /dev/urandom | base64 | tr -d '/+=\n')"

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
    local out
    out=$("$@" 2>&1) || true
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

rssh() {  # rssh <ip> <remote command...>
    local ip="$1"; shift
    ssh -p "$SSH_PORT" -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
        "$SSH_USER@$ip" "$@"
}
push_binary() {  # push_binary <ip> <local-path>
    # Pipe the binary over ssh rather than scp: this sshd's SFTP subsystem
    # (scp's default transport) denies writes into /usr/local/bin even for
    # root, but a plain "cat > tmpfile" over the exec channel works. mv -f
    # on top then only needs the directory to be writable.
    local ip="$1" src="$2" tmp="/usr/local/bin/pg.tmp.$$"
    cat "$src" | rssh "$ip" "cat > '$tmp' && chmod +x '$tmp' && mv -f '$tmp' /usr/local/bin/pg"
}
rpg() {  # rpg <ip> <pg args...> — the node's pgcli against this test's config
    local ip="$1"; shift
    rssh "$ip" "/usr/local/bin/pg -c '$CONFIG_FILE' $*"
}
CONTAINER="pgcli-minio-$NAMESPACE-$STORE"

install_node() {  # install_node <ip>
    local ip="$1"
    rssh "$ip" "
        set -e
        rm -rf '$REMOTE_BASE_DIR'
        mkdir -p '$REMOTE_BASE_DIR'
        /usr/local/bin/pg -c '$CONFIG_FILE' config init \
            -o '$CONFIG_FILE' --base-dir '$REMOTE_BASE_DIR' --namespace '$NAMESPACE'
        /usr/local/bin/pg -c '$CONFIG_FILE' addon install minio \
            --name '$STORE' --force \
            --listen '$ip' \
            --api-port '$API_PORT' --console-port '$CONSOLE_PORT' \
            --data-dir '$MINIO_DATA_DIR' \
            --root-user '$ROOT_USER' \
            --root-password '$ROOT_PASSWORD' \
            --endpoint 'http://${NODES[0]}:$API_PORT$MINIO_DATA_DIR' \
            --endpoint 'http://${NODES[1]}:$API_PORT$MINIO_DATA_DIR' \
            --endpoint 'http://${NODES[2]}:$API_PORT$MINIO_DATA_DIR' \
            --endpoint 'http://${NODES[3]}:$API_PORT$MINIO_DATA_DIR' >/dev/null
    "
}

endpoint_field() {  # endpoint_field <ip> — the endpoints list from the node's yaml.
    # Indentation-agnostic: enter the block on any line ending in "endpoints:",
    # emit every more-indented "- <value>" line, and leave when a non-dash key
    # (a sibling field or the next top-level section) appears.
    rssh "$1" "awk '
        /^[[:space:]]*endpoints:[[:space:]]*\$/{f=1; next}
        f && /^[[:space:]]*[^-[:space:]]/{f=0}
        f && /^[[:space:]]*- /{sub(/^[[:space:]]*- /,\"\"); print}
    ' '$CONFIG_FILE'"
}
cmp_endpoints() {  # cmp_endpoints <ip> <reference>
    [ "$(endpoint_field "$1")" = "$2" ]
}

health_code() {  # health_code <ip> <live|cluster>
    rssh "$1" "curl -s -o /dev/null -w '%{http_code}' http://$1:$API_PORT/minio/health/$2"
}

wait_cluster_up() {  # wait_cluster_up <ip>
    local ip="$1"
    for _ in $(seq 1 30); do
        [ "$(health_code "$ip" cluster)" = "200" ] && return 0
        sleep 2
    done
    return 1
}

data_dir_separate() {  # data_dir_separate <ip> — MINIO_DATA_DIR's nearest existing
    # ancestor is NOT on the same device as / — mirrors pgcli's own
    # DataDirSharesRootDevice() check, so a not-yet-created subdir under a
    # correctly-mounted disk still passes (the container creates it).
    local ip="$1"
    rssh "$ip" "
        p='$MINIO_DATA_DIR'
        while [ ! -e \"\$p\" ] && [ \"\$p\" != / ]; do p=\$(dirname \"\$p\"); done
        [ \"\$(stat -c %d /)\" != \"\$(stat -c %d \"\$p\")\" ]
    "
}

shard_digests() {  # shard_digests <ip> — unique md5s of EC part files on that node
    rssh "$1" "find '$MINIO_DATA_DIR' -name 'part.*' -exec md5sum {} \;" | awk '{print $1}' | sort -u
}

upload_object() {  # upload_object — alias + mb + 12MB cp, all through node1
    rpg "${NODES[0]}" "mc alias set mnce2e 'http://${NODES[0]}:$API_PORT' '$ROOT_USER' '$ROOT_PASSWORD' >/dev/null" &&
    rpg "${NODES[0]}" "mc mb mnce2e/$BUCKET >/dev/null" &&
    rpg "${NODES[0]}" "mc cp /tmp/mnce2e-upload.bin mnce2e/$BUCKET/ec.bin >/dev/null"
}

write_check() {  # write_check — a fresh 1MB upload, expected to fail under degraded quorum
    rssh "${NODES[0]}" "dd if=/dev/urandom of=/tmp/mnce2e-write.bin bs=1M count=1 status=none"
    rpg "${NODES[0]}" "mc cp /tmp/mnce2e-write.bin mnce2e/$BUCKET/quorum-check.bin >/dev/null"
}

read_md5() {  # read_md5 — the ec.bin digest as seen from node1 right now
    rpg "${NODES[0]}" "mc cat mnce2e/$BUCKET/ec.bin 2>/dev/null | md5sum" | cut -d' ' -f1
}

count_distinct_shards() {  # count_distinct_shards — unique EC part digests across ALL nodes
    local ip
    for ip in "${NODES[@]}"; do shard_digests "$ip"; done | sort -u | wc -l
}

cleanup() {
    local ip
    for ip in "${NODES[@]}"; do
        # drop only the test's own aliases, never the whole ~/.mc config
        rssh "$ip" "
            /usr/local/bin/pg -c '$CONFIG_FILE' mc alias remove mnce2e >/dev/null 2>&1 || true
            /usr/local/bin/pg -c '$CONFIG_FILE' mc alias remove mnce2e-dn >/dev/null 2>&1 || true
            rm -rf /tmp/mnce2e-*.bin 2>/dev/null
        " || true
    done
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cluster removal (--skip-destroy)"; return
    fi
    section "Cleanup"
    for ip in "${NODES[@]}"; do
        rssh "$ip" "
            /usr/local/bin/pg -c '$CONFIG_FILE' addon remove minio --name '$STORE' --clean-data >/dev/null 2>&1 || true
            rm -rf '$REMOTE_BASE_DIR'
        " || true
    done
    [ -n "$LOCAL_BUILD_DIR" ] && rm -rf "$LOCAL_BUILD_DIR"
    green "  Cleanup done"
}
LOCAL_BUILD_DIR=""
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli MinIO Distributed-Cluster Test"
    echo "=========================================="
    echo "  Nodes:      ${NODES[*]}"
    echo "  SSH:        $SSH_USER@<node>:$SSH_PORT (key auth, BatchMode)"
    echo "  Data dir:   $MINIO_DATA_DIR (must be a separate device on every node)"
    echo "  Namespace:  $NAMESPACE"
    echo "  Ports:      API $API_PORT / console $CONSOLE_PORT"
    echo "=========================================="

    command -v ssh &>/dev/null || { red "ssh is not installed"; exit 1; }

    # ---- Build once, distribute the binary ----
    section "Build & distribute binary"
    if [ -n "${PG_BINARY:-}" ]; then
        LOCAL_PG="$PG_BINARY"
        pass "using pre-built binary: $LOCAL_PG"
    else
        command -v go &>/dev/null || { red "go not installed (or set PG_BINARY=...)"; exit 1; }
        LOCAL_BUILD_DIR="$(mktemp -d)"
        run_test "go build (this checkout)" go build -o "$LOCAL_BUILD_DIR/pg" .
        LOCAL_PG="$LOCAL_BUILD_DIR/pg"
    fi
    for ip in "${NODES[@]}"; do
        run_test "push binary to $ip" push_binary "$ip" "$LOCAL_PG"
    done

    # ---- Preconditions ----
    section "Preconditions"
    for ip in "${NODES[@]}"; do
        run_test "podman installed on $ip" rssh "$ip" "command -v podman >/dev/null"
        run_test "data dir $MINIO_DATA_DIR on a device distinct from / on $ip" data_dir_separate "$ip"
    done
    if [ "$FAILED" -gt 0 ]; then
        red "  Preconditions failed — fix the nodes above before continuing (see the header comment)."
        exit 1
    fi

    # ---- Install (docs' Distributed / Cluster Mode command, on every node) ----
    section "Install (4 nodes, same endpoint list + same --root-password)"
    for ip in "${NODES[@]}"; do
        run_test "install minio on $ip" install_node "$ip"
    done

    # ---- Config consistency ----
    section "Config consistency across nodes"
    REF="$(endpoint_field "${NODES[0]}")"
    TESTS=$((TESTS + 1))
    if [ "$(printf '%s\n' "$REF" | wc -l)" = "4" ]; then pass "endpoint list has 4 entries"
    else fail "endpoint list has $(printf '%s\n' "$REF" | wc -l) entries (want 4)"; fi
    for ip in "${NODES[@]}"; do
        run_test "endpoints byte-identical on $ip" cmp_endpoints "$ip" "$REF"
    done
    for ip in "${NODES[@]}"; do
        run_not_grep "no MINIO_SERVER_URL env on $ip" "MINIO_SERVER_URL" \
            rssh "$ip" "podman inspect '$CONTAINER' --format '{{range .Config.Env}}{{println .}}{{end}}'"
    done

    # ---- Cluster health ----
    section "Cluster health (ring formed)"
    for ip in "${NODES[@]}"; do
        run_test "cluster health reaches 200 on $ip" wait_cluster_up "$ip"
    done
    run_grep "EC layout: 4 drives per set (not single-node fold)" "4 drives per set" \
        rssh "${NODES[0]}" "podman logs '$CONTAINER' 2>&1"

    # ---- EC striping + round-trip through a different peer ----
    section "Erasure coding: striping + round-trip"
    rssh "${NODES[0]}" "dd if=/dev/urandom of=/tmp/mnce2e-upload.bin bs=1M count=12 status=none"
    ORIG_MD5="$(rssh "${NODES[0]}" "md5sum /tmp/mnce2e-upload.bin" | cut -d' ' -f1)"
    run_test "alias set + mb + 12MB upload via node1" upload_object

    ALL_SHARDS="$(count_distinct_shards)"
    TESTS=$((TESTS + 1))
    if [ "$ALL_SHARDS" = "4" ]; then pass "4 distinct shard digests across the 4 nodes (real EC striping, not replication)"
    else fail "expected 4 distinct shard digests, got $ALL_SHARDS"; fi

    rpg "${NODES[3]}" "mc alias set mnce2e-dn 'http://${NODES[3]}:$API_PORT' '$ROOT_USER' '$ROOT_PASSWORD' >/dev/null"
    DOWN_MD5="$(rpg "${NODES[3]}" "mc cp mnce2e-dn/$BUCKET/ec.bin /tmp/mnce2e-down.bin >/dev/null && md5sum /tmp/mnce2e-down.bin" | cut -d' ' -f1)"
    TESTS=$((TESTS + 1))
    if [ "$ORIG_MD5" = "$DOWN_MD5" ]; then pass "download through a different peer is byte-identical"
    else fail "round-trip mismatch: orig=$ORIG_MD5 down=$DOWN_MD5"; fi

    # ---- Quorum ----
    section "EC quorum (4 nodes: read ⌈N/2⌉=2, write ⌈N/2⌉+1=3)"
    for ip in "${NODES[2]}" "${NODES[3]}"; do
        rssh "$ip" "podman stop '$CONTAINER' >/dev/null"
    done
    sleep 5
    READ_MD5="$(read_md5)"
    TESTS=$((TESTS + 1))
    if [ "$READ_MD5" = "$ORIG_MD5" ]; then pass "read succeeds on node1 with 2 of 4 nodes down"
    else fail "read with 2 down returned '$READ_MD5', want '$ORIG_MD5'"; fi
    run_fails "write rejected on node1 with 2 of 4 nodes down" "" write_check

    # ---- Self-heal ----
    section "Self-heal (restart the 2 downed nodes)"
    for ip in "${NODES[2]}" "${NODES[3]}"; do
        rssh "$ip" "podman start '$CONTAINER' >/dev/null"
    done
    for ip in "${NODES[0]}" "${NODES[2]}"; do
        run_test "cluster health back to 200 on $ip" wait_cluster_up "$ip"
    done
    HEAL_MD5="$(rpg "${NODES[0]}" "mc cat mnce2e/$BUCKET/ec.bin 2>/dev/null | md5sum" | cut -d' ' -f1)"
    TESTS=$((TESTS + 1))
    if [ "$HEAL_MD5" = "$ORIG_MD5" ]; then pass "object intact after self-heal"
    else fail "after self-heal: got '$HEAL_MD5', want '$ORIG_MD5'"; fi

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
