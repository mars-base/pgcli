#!/usr/bin/env bash
# pgcli ClickHouse addon — replicated multi-host cluster end-to-end test.
#
# Runs a real 4-host deployment:
#   nodes 1-3: one ClickHouse Keeper member each (Raft quorum 2/3)
#   nodes 1-4: one ClickHouse member each, 2 shards x 2 replicas
#
# The script builds this checkout's pg binary, pushes it to every node over
# SSH, installs the Keeper quorum first and the ClickHouse members second
# (exactly the documented order), then asserts over SSH:
#   - Keeper: 3/3 client endpoints reachable, one leader + two followers
#   - ClickHouse: system.clusters reports 4 members, 2x2, local row marked
#   - zz-pgcli-cluster.xml: 2 shards, 4 replicas, internal_replication=true,
#     3 Keeper nodes, interserver host/port, macros and DDL path
#   - Keeper config/data/log paths are separate from the ClickHouse paths
#   - ReplicatedMergeTree: a local insert on one replica reaches its sibling
#   - Distributed: writes fan out, reads merge both shards
#   - Keeper failure: with 1 of 3 down (2/3 quorum) ON CLUSTER DDL still works
#   - ClickHouse failure: with shard 2 replica 2 down, replica 1 keeps
#     reading/writing and the downed replica catches up after restart
#   - identity immutability: flag drift is rejected, --force cannot change
#     cluster/shard/replica or Keeper server-id
#   - cleanup: Keeper --clean-data leaves the ClickHouse data directory intact
#
# Prerequisites (checked up front, never worked around):
#   - passwordless key-based SSH to all four IPs (root or sudo-capable)
#   - podman installed on every node (pgcli's own prerequisite everywhere)
#   - ports 8123/9000/9009/9181/9234 free on every node; the script removes
#     only its own namespace's leftover containers and its own base directory,
#     but does not stop unrelated services
#   - enough RAM for ClickHouse; members are capped at 768 MiB through
#     --config-option max_server_memory_usage (override with PGCLI_CH_MAX_MEMORY)
#
# Usage:
#   bash test/addon/test_clickhouse_cluster.sh <ip1> <ip2> <ip3> <ip4>
#   SSH_USER=root SSH_PORT=22 bash test/addon/test_clickhouse_cluster.sh 10.0.0.11 10.0.0.12 10.0.0.13 10.0.0.14
#   bash test/addon/test_clickhouse_cluster.sh --skip-destroy <ip1> <ip2> <ip3> <ip4>
#
#   SSH_USER (default root) SSH_PORT (default 22)
#   PGCLI_NAMESPACE (default che2e)
#   REMOTE_BASE_DIR (default /root/pgcli-che-e2e, wiped between runs)
#   PGCLI_CH_HTTP_PORT / PGCLI_CH_TCP_PORT / PGCLI_CH_INTERSERVER_PORT
#     (default 8123 / 9000 / 9009)
#   PGCLI_KEEPER_CLIENT_PORT / PGCLI_KEEPER_RAFT_PORT (default 9181 / 9234)
#   PGCLI_CH_MAX_MEMORY (default 805306368 = 768 MiB)
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
NAMESPACE="${PGCLI_NAMESPACE:-che2e}"
CH_NAME="clickhouse"
KEEPER_NAME="keeper"
CLUSTER="chcluster"
REMOTE_BASE_DIR="${REMOTE_BASE_DIR:-/root/pgcli-che-e2e}"
CONFIG_FILE="$REMOTE_BASE_DIR/pg.yaml"
HTTP_PORT="${PGCLI_CH_HTTP_PORT:-8123}"
TCP_PORT="${PGCLI_CH_TCP_PORT:-9000}"
INTERSERVER_PORT="${PGCLI_CH_INTERSERVER_PORT:-9009}"
KEEPER_CLIENT_PORT="${PGCLI_KEEPER_CLIENT_PORT:-9181}"
KEEPER_RAFT_PORT="${PGCLI_KEEPER_RAFT_PORT:-9234}"
MEMORY_LIMIT="${PGCLI_CH_MAX_MEMORY:-805306368}"

# Reference topology: shard 1 / replica 1-2 on nodes 1-2, shard 2 / replica 1-2
# on nodes 3-4. Nodes 1-3 also run Keeper (test co-location; production runs
# Keeper on dedicated servers).
SHARDS=(1 1 2 2)
REPLICAS=(1 2 1 2)

# Generated once and passed identically to every ClickHouse member, exactly
# like a real cluster deployment. Stripped to alphanumerics so it survives the
# remote shell quoting without special handling. Never printed by this script.
ADMIN_PASSWORD="$(head -c 15 /dev/urandom | base64 | tr -d '/+=\n')"

KEEPER_DIR="$REMOTE_BASE_DIR/addon/clickhouse-keeper/$KEEPER_NAME"
CH_DATA_DIR="$REMOTE_BASE_DIR/addon/clickhouse/$CH_NAME/data"
CH_CLUSTER_XML="$REMOTE_BASE_DIR/addon/clickhouse/$CH_NAME/config.d/zz-pgcli-cluster.xml"

red()    { printf '\033[0;31m%s\033[0m\n' "$*"; }
green()  { printf '\033[0;32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[0;33m%s\033[0m\n' "$*"; }
pass()   { green "  [PASS] $*"; }
fail()   { red "  [FAIL] $*"; FAILED=$((FAILED + 1)); }
section(){ echo ""; yellow "=== $* ==="; }

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
    local ip="$1" src="$2" tmp="/usr/local/bin/pg.tmp.$$"
    cat "$src" | rssh "$ip" "cat > '$tmp' && chmod +x '$tmp' && mv -f '$tmp' /usr/local/bin/pg"
}
rpg() {  # rpg <ip> <pg args...> — the node's pgcli against this test's config
    local ip="$1"; shift
    rssh "$ip" "/usr/local/bin/pg -c '$CONFIG_FILE' $*"
}
rquery() {  # rquery <ip> <sql> — SQL is piped on stdin, never re-quoted
    local ip="$1" sql="$2"
    printf '%s\n' "$sql" | rssh "$ip" \
        "/usr/local/bin/pg -c '$CONFIG_FILE' addon clickhouse query --name '$CH_NAME' --file -"
}

keeper_install_args() {  # keeper_install_args <index 0-2>
    local idx="$1" args j
    args="--name $KEEPER_NAME --server-id $((idx + 1)) --advertise-host ${NODES[$idx]}"
    args="$args --listen 0.0.0.0 --client-port $KEEPER_CLIENT_PORT --raft-port $KEEPER_RAFT_PORT"
    for j in 0 1 2; do
        [ "$j" -eq "$idx" ] && continue
        args="$args --peer $((j + 1))@${NODES[$j]}"
    done
    echo "$args"
}

ch_install_args() {  # ch_install_args <index 0-3>
    local idx="$1" args j k
    args="--name $CH_NAME --cluster $CLUSTER --shard ${SHARDS[$idx]} --replica ${REPLICAS[$idx]}"
    args="$args --advertise-host ${NODES[$idx]} --listen 0.0.0.0"
    args="$args --http-port $HTTP_PORT --tcp-port $TCP_PORT --interserver-port $INTERSERVER_PORT"
    args="$args --admin-password $ADMIN_PASSWORD"
    args="$args --config-option max_server_memory_usage=$MEMORY_LIMIT"
    for j in 0 1 2 3; do
        [ "$j" -eq "$idx" ] && continue
        args="$args --peer ${SHARDS[$j]}/${REPLICAS[$j]}@${NODES[$j]}:$TCP_PORT"
    done
    for k in 0 1 2; do
        args="$args --keeper ${NODES[$k]}:$KEEPER_CLIENT_PORT"
    done
    echo "$args"
}

init_node() {  # wipe this test's state and initialise a config on one node
    local ip="$1"
    rssh "$ip" "
        set -e
        for c in 'pgcli-clickhouse-$NAMESPACE-$CH_NAME' \
                 'pgcli-clickhouse-keeper-$NAMESPACE-$KEEPER_NAME'; do
            podman rm -f \"\$c\" >/dev/null 2>&1 || true
            if podman ps -a --format '{{.Names}}' | grep -qx \"\$c\"; then
                echo \"cannot remove stale container \$c\" >&2
                exit 1
            fi
        done
        rm -rf '$REMOTE_BASE_DIR'
        mkdir -p '$REMOTE_BASE_DIR'
        /usr/local/bin/pg -c '$CONFIG_FILE' config init \
            -o '$CONFIG_FILE' --base-dir '$REMOTE_BASE_DIR' --namespace '$NAMESPACE'
    " >/dev/null
}

ports_free() {  # no daemon may already listen on the ports this test needs
    rssh "$1" "bash -s" <<REMOTE
set -e
command -v ss >/dev/null
for p in $HTTP_PORT $TCP_PORT $INTERSERVER_PORT $KEEPER_CLIENT_PORT $KEEPER_RAFT_PORT; do
    if ss -ltnH 2>/dev/null | awk '{print \$4}' | grep -qE "[:.]\$p\$"; then
        echo "port \$p is already in use"
        exit 1
    fi
done
REMOTE
}

install_keeper() {  # install_keeper <index 0-2>
    local idx="$1"
    rpg "${NODES[$idx]}" "addon install clickhouse-keeper $(keeper_install_args "$idx")" >/dev/null
}
install_ch() {  # install_ch <index 0-3>
    local idx="$1"
    rpg "${NODES[$idx]}" "addon install clickhouse $(ch_install_args "$idx")" >/dev/null
}

keeper_status() { rpg "$1" "addon clickhouse-keeper status --name $KEEPER_NAME"; }
keeper_quorum_ok() { keeper_status "$1" | grep -qF "3/3 client endpoints reachable"; }
wait_keeper_quorum() {
    local ip="$1" _
    for _ in $(seq 1 30); do keeper_quorum_ok "$ip" && return 0; sleep 2; done
    return 1
}
keeper_local_state() {
    keeper_status "$1" | sed -n 's/^  Local state: *//p'
}
keeper_roles_ok() {  # exactly one leader and two followers across nodes 1-3
    local i st leaders=0 followers=0
    for i in 0 1 2; do
        st=$(keeper_local_state "${NODES[$i]}")
        case "$st" in
            leader)   leaders=$((leaders + 1)) ;;
            follower) followers=$((followers + 1)) ;;
        esac
    done
    [ "$leaders" -eq 1 ] && [ "$followers" -eq 2 ]
}
wait_keeper_roles() {
    local _
    for _ in $(seq 1 15); do keeper_roles_ok && return 0; sleep 1; done
    return 1
}
keeper_two_of_three() {
    keeper_status "$1" | grep -qF "2/3 client endpoints reachable"
}

ch_cluster_status() { rpg "$1" "addon clickhouse cluster status --name $CH_NAME"; }
ch_cluster_ok() {  # 4 members + Keeper quorum reachable, from any node
    local out
    out=$(ch_cluster_status "$1") || return 1
    printf '%s\n' "$out" | grep -qF "system.clusters: 4 member(s), expected 4" &&
        printf '%s\n' "$out" | grep -qF "Keeper client endpoints reachable: 3/3"
}
wait_ch_cluster() {
    local ip="$1" _
    for _ in $(seq 1 30); do ch_cluster_ok "$ip" && return 0; sleep 2; done
    return 1
}
ch_running() {
    rpg "$1" "addon clickhouse status --name $CH_NAME" 2>/dev/null |
        grep -qF "running (ping ok)"
}
wait_ch_running() {
    local ip="$1" _
    for _ in $(seq 1 30); do ch_running "$ip" && return 0; sleep 2; done
    return 1
}

local_count() {
    rquery "$1" "SELECT count() FROM demo.events FORMAT TSV" | tr -d '[:space:]'
}
dist_count() {
    rquery "$1" "SELECT count() FROM demo.events_dist FORMAT TSV" | tr -d '[:space:]'
}
count_is() { [ "$(local_count "$1")" = "$2" ]; }
dcount_is() { [ "$(dist_count "$1")" = "$2" ]; }
sync_replica() { rquery "$1" "SYSTEM SYNC REPLICA demo.events" >/dev/null; }

cluster_xml() {
    rssh "$1" "cat '$CH_CLUSTER_XML'"
}
status_field() {  # status_field <ip> <subcommand string> <label>
    rpg "$1" "$2" | sed -n "s/^  $3:[[:space:]]*//p"
}
not_nested() {  # not_nested <path-a> <path-b>
    case "$2/" in "$1/"*) return 1 ;; esac
    case "$1/" in "$2/"*) return 1 ;; esac
    return 0
}

LOCAL_BUILD_DIR=""
CLEANED=false
cleanup() {
    local ip
    if [ "$CLEANED" = true ]; then return; fi
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping teardown (--skip-destroy); inspect the nodes directly"
        return
    fi
    section "Cleanup (trap)"
    for ip in "${NODES[@]}"; do
        rpg "$ip" "addon remove clickhouse --name $CH_NAME --clean-data" >/dev/null 2>&1 || true
    done
    for ip in "${NODES[@]}"; do
        rpg "$ip" "addon remove clickhouse-keeper --name $KEEPER_NAME --clean-data" >/dev/null 2>&1 || true
        rssh "$ip" "rm -rf '$REMOTE_BASE_DIR'" >/dev/null 2>&1 || true
    done
    if [ -n "$LOCAL_BUILD_DIR" ]; then rm -rf "$LOCAL_BUILD_DIR"; fi
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli ClickHouse 2x2 cluster test"
    echo "=========================================="
    echo "  Nodes:      ${NODES[*]}"
    echo "  SSH:        $SSH_USER@<node>:$SSH_PORT (key auth, BatchMode)"
    echo "  Keeper:     nodes 1-3, client $KEEPER_CLIENT_PORT / raft $KEEPER_RAFT_PORT"
    echo "  ClickHouse: all 4, 2 shards x 2 replicas, TCP $TCP_PORT, interserver $INTERSERVER_PORT"
    echo "  Namespace:  $NAMESPACE"
    echo "  Memory cap: $MEMORY_LIMIT bytes per ClickHouse member"
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
        run_test "ss (iproute2) installed on $ip" rssh "$ip" "command -v ss >/dev/null"
    done
    if [ "$FAILED" -gt 0 ]; then
        red "  Preconditions failed — fix the nodes above before continuing."
        exit 1
    fi

    section "Reset test state and initialise configs"
    for ip in "${NODES[@]}"; do
        run_test "reset + config init on $ip" init_node "$ip"
    done

    section "Ports free"
    for ip in "${NODES[@]}"; do
        run_test "ports free on $ip" ports_free "$ip"
    done
    if [ "$FAILED" -gt 0 ]; then
        red "  Ports are occupied by something outside this test's namespace."
        exit 1
    fi

    # ---- Keeper quorum first ----
    section "Install ClickHouse Keeper (nodes 1-3)"
    for i in 0 1 2; do
        run_test "install Keeper member $((i + 1)) on ${NODES[$i]}" install_keeper "$i"
    done
    run_test "3/3 Keeper client endpoints reachable from node1" wait_keeper_quorum "${NODES[0]}"
    run_test "one leader + two followers" wait_keeper_roles
    for i in 0 1 2; do
        run_grep "node $((i + 1)) reports its own state" "Local state:" keeper_status "${NODES[$i]}"
    done

    # ---- ClickHouse members ----
    section "Install ClickHouse (all 4, 2 shards x 2 replicas)"
    for i in 0 1 2 3; do
        run_test "install shard ${SHARDS[$i]} / replica ${REPLICAS[$i]} on ${NODES[$i]}" install_ch "$i"
    done

    section "Cluster membership"
    for i in 0 1 2 3; do
        run_test "cluster status on node $((i + 1))" wait_ch_cluster "${NODES[$i]}"
    done
    for i in 0 1 2 3; do
        run_grep "node $((i + 1)) sees itself as shard ${SHARDS[$i]} replica ${REPLICAS[$i]}" \
            "shard ${SHARDS[$i]} replica ${REPLICAS[$i]}  ${NODES[$i]}:$TCP_PORT  local" \
            ch_cluster_status "${NODES[$i]}"
    done
    run_grep "node4 (no local Keeper) reaches the 3-member quorum" \
        "Keeper client endpoints reachable: 3/3" ch_cluster_status "${NODES[3]}"

    # ---- Rendered cluster XML ----
    section "Rendered cluster config"
    XML="$(cluster_xml "${NODES[0]}")"
    xml_count() { printf '%s\n' "$XML" | grep -cE "$1" || true; }
    xml_has() { printf '%s\n' "$XML" | grep -qF -- "$1"; }
    run_test "2 shard elements" test "$(xml_count '^[[:space:]]*<shard>$')" = "2"
    run_test "4 replica elements" test "$(xml_count '^[[:space:]]*<replica>$')" = "4"
    run_test "replicas authenticate as the admin user" \
        test "$(printf '%s\n' "$XML" | grep -cF '<user>admin</user>')" = "4"
    run_test "replica passwords come from the entrypoint environment" \
        test "$(printf '%s\n' "$XML" | grep -cF '<password from_env="PGCLI_CLICKHOUSE_ADMIN_PASSWORD"></password>')" = "4"
    run_test "cluster config does not contain the admin password" \
        env E2E_ADMIN_PASSWORD="$ADMIN_PASSWORD" E2E_XML="$XML" \
        bash -c '! printf "%s\n" "$E2E_XML" | grep -qF -- "$E2E_ADMIN_PASSWORD"'
    run_test "3 Keeper node elements" test "$(xml_count '^[[:space:]]*<node>')" = "3"
    run_test "4 ClickHouse replica endpoints use the TCP port" \
        test "$(printf '%s\n' "$XML" | grep -cF "<port>$TCP_PORT</port>")" = "4"
    run_test "3 Keeper endpoints use the client port" \
        test "$(printf '%s\n' "$XML" | grep -cF "<port>$KEEPER_CLIENT_PORT</port>")" = "3"
    run_test "internal_replication=true on every shard" \
        test "$(printf '%s\n' "$XML" | grep -cF '<internal_replication>true</internal_replication>')" = "2"
    run_test "interserver host is this node" \
        xml_has "<interserver_http_host>${NODES[0]}</interserver_http_host>"
    run_test "interserver port rendered" \
        xml_has "<interserver_http_port>$INTERSERVER_PORT</interserver_http_port>"
    run_test "cluster DDL path rendered" \
        xml_has "<path>/clickhouse/$CLUSTER/task_queue/ddl</path>"
    run_test "macros carry shard/replica" \
        xml_has "<shard>${SHARDS[0]}</shard>"

    # ---- Path isolation ----
    section "Keeper / ClickHouse path isolation"
    CH_DATA_FIELD="$(status_field "${NODES[0]}" "addon clickhouse status --name $CH_NAME" "Data dir")"
    KP_DATA_FIELD="$(status_field "${NODES[0]}" "addon clickhouse-keeper status --name $KEEPER_NAME" "Data dir")"
    run_test "ClickHouse data dir is the addon path" \
        test "$CH_DATA_FIELD" = "$CH_DATA_DIR"
    run_test "Keeper data dir is the Keeper addon path" \
        test "$KP_DATA_FIELD" = "$KEEPER_DIR/data"
    run_test "Keeper and ClickHouse data dirs do not nest" \
        not_nested "$CH_DATA_FIELD" "$KP_DATA_FIELD"

    # ---- Identity immutability ----
    section "Identity immutability"
    run_grep "ClickHouse reinstall without --force is a no-op" "already installed" \
        rpg "${NODES[0]}" "addon install clickhouse $(ch_install_args 0)"
    run_fails "ClickHouse flag drift without --force is rejected" "differs from the stored value" \
        rpg "${NODES[0]}" "addon install clickhouse --name $CH_NAME --shard 2"
    run_fails "ClickHouse --shard cannot change even with --force" "changing --shard is not supported" \
        rpg "${NODES[0]}" "addon install clickhouse --name $CH_NAME --shard 2 --force"
    run_grep "Keeper reinstall without --force is a no-op" "already installed" \
        rpg "${NODES[0]}" "addon install clickhouse-keeper $(keeper_install_args 0)"
    run_fails "Keeper --server-id cannot change even with --force" "changing --server-id is not supported" \
        rpg "${NODES[0]}" "addon install clickhouse-keeper --name $KEEPER_NAME --server-id 9 --force"

    # ---- Replication ----
    section "ReplicatedMergeTree + Distributed"
    run_test "create database ON CLUSTER" rquery "${NODES[0]}" \
        "CREATE DATABASE IF NOT EXISTS demo ON CLUSTER $CLUSTER"
    run_test "create local ReplicatedMergeTree ON CLUSTER" rquery "${NODES[0]}" \
        "CREATE TABLE demo.events ON CLUSTER $CLUSTER (id UInt64, ts DateTime, name String) ENGINE = ReplicatedMergeTree ORDER BY id"
    run_test "create Distributed table ON CLUSTER" rquery "${NODES[0]}" \
        "CREATE TABLE demo.events_dist ON CLUSTER $CLUSTER AS demo.events ENGINE = Distributed($CLUSTER, demo, events, rand())"

    run_test "insert shard 1 row on node1" rquery "${NODES[0]}" \
        "INSERT INTO demo.events VALUES (1, now(), 'node1')"
    run_test "insert shard 2 row on node3" rquery "${NODES[2]}" \
        "INSERT INTO demo.events VALUES (2, now(), 'node3')"
    run_test "sync sibling replica on node2" sync_replica "${NODES[1]}"
    run_test "sync sibling replica on node4" sync_replica "${NODES[3]}"
    run_test "node2 sees the shard 1 row" count_is "${NODES[1]}" 1
    run_test "node4 sees the shard 2 row" count_is "${NODES[3]}" 1
    run_test "Distributed reads merge both shards" dcount_is "${NODES[0]}" 2

    run_test "insert through the Distributed table" rquery "${NODES[0]}" \
        "INSERT INTO demo.events_dist VALUES (3, now(), 'distributed')"
    for i in 0 1 2 3; do
        run_test "sync replica on node $((i + 1))" sync_replica "${NODES[$i]}"
    done
    run_test "Distributed count is 3" dcount_is "${NODES[0]}" 3
    run_grep "cluster status reports the replicated table" "system.replicas: 1 replicated table(s)" \
        ch_cluster_status "${NODES[0]}"
    run_grep "replica state is healthy" "demo.events replica" ch_cluster_status "${NODES[0]}"

    # ---- Keeper failure: 2/3 quorum keeps DDL alive ----
    section "Keeper failure (1 of 3 down)"
    run_test "stop Keeper on node2" rpg "${NODES[1]}" \
        "addon stop clickhouse-keeper --name $KEEPER_NAME"
    run_test "node1 sees 2/3 Keeper endpoints" keeper_two_of_three "${NODES[0]}"
    run_test "ON CLUSTER DDL still works at 2/3" rquery "${NODES[0]}" \
        "CREATE TABLE demo.quorum_check ON CLUSTER $CLUSTER (id UInt64) ENGINE = ReplicatedMergeTree ORDER BY id"
    run_test "drop the quorum-check table" rquery "${NODES[0]}" \
        "DROP TABLE demo.quorum_check ON CLUSTER $CLUSTER"
    run_test "restart Keeper on node2" rpg "${NODES[1]}" \
        "addon start clickhouse-keeper --name $KEEPER_NAME"
    run_test "3/3 Keeper endpoints back" wait_keeper_quorum "${NODES[0]}"

    # ---- ClickHouse failure: sibling replica keeps serving ----
    section "ClickHouse failure (shard 2 replica 2 down)"
    BEFORE3="$(local_count "${NODES[2]}")"
    run_test "stop ClickHouse on node4" rpg "${NODES[3]}" \
        "addon stop clickhouse --name $CH_NAME"
    run_test "shard 2 replica 1 still accepts writes" rquery "${NODES[2]}" \
        "INSERT INTO demo.events VALUES (4, now(), 'node3-after-failure')"
    run_test "shard 2 replica 1 row count grew by one" \
        test "$(local_count "${NODES[2]}")" = "$((BEFORE3 + 1))"
    run_test "Distributed reads survive node4 down" dcount_is "${NODES[0]}" 4
    run_test "restart ClickHouse on node4" rpg "${NODES[3]}" \
        "addon start clickhouse --name $CH_NAME"
    run_test "node4 is running again" wait_ch_running "${NODES[3]}"
    run_test "node4 syncs the missed row" sync_replica "${NODES[3]}"
    run_test "node4 replica caught up with node3" \
        test "$(local_count "${NODES[3]}")" = "$(local_count "${NODES[2]}")"
    run_test "Distributed count is 4 after recovery" dcount_is "${NODES[0]}" 4

    # ---- Cleanup order + isolation ----
    if [ "$SKIP_DESTROY" = false ]; then
        section "Cleanup order: Keeper first, ClickHouse data preserved"
        run_test "remove Keeper on node1 with --clean-data" rpg "${NODES[0]}" \
            "addon remove clickhouse-keeper --name $KEEPER_NAME --clean-data"
        run_test "Keeper instance dir is gone" rssh "${NODES[0]}" "test ! -e '$KEEPER_DIR'"
        run_test "ClickHouse data dir still exists" rssh "${NODES[0]}" "test -d '$CH_DATA_DIR'"
        for i in 0 1 2 3; do
            run_test "remove ClickHouse on node $((i + 1))" rpg "${NODES[$i]}" \
                "addon remove clickhouse --name $CH_NAME --clean-data"
        done
        for ip in "${NODES[@]}"; do
            rssh "$ip" "rm -rf '$REMOTE_BASE_DIR'" || true
        done
        if [ -n "$LOCAL_BUILD_DIR" ]; then rm -rf "$LOCAL_BUILD_DIR"; fi
        CLEANED=true
    fi

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
