#!/usr/bin/env bash
# pgcli K3s addon — multi-host cluster end-to-end test (3 servers + 1 agent).
#
# Builds this checkout's pg binary, pushes it to all four nodes over SSH and
# deploys the documented HA shape:
#   node 1: server --cluster-init (embedded etcd), custom 100-year CA via
#           --ca-dir, --tls-san for every server address
#   nodes 2-3: servers joining node 1
#   node 4: agent joining node 1
# Every node gets the same pre-generated token file and a containerd mirror;
# each server waits for /readyz before the next member installs, exactly like
# the Ansible play. The script then asserts:
#   - 4 nodes Ready: 3 control-plane servers + 1 agent, one Ready per line
#   - the custom CA survives into the cluster: node 1's server-ca.crt is
#     valid ~100 years and node 2 received the identical CA from the datastore
#   - token file mode 0600 and generated registries.yaml on every node
#   - kubeconfig export from a joining server (advertise rewrite, mode 0600)
#   - agent nodes refuse kubectl; servers accept it
#   - idempotent re-install keeps the container
#   - self-heal: restarting the agent container and stop/start of a server
#     both return the node to Ready without rebuilding the cluster
#   - remove --clean-data on every node (reverse order) removes containers,
#     data and generated registries.yaml but keeps the user token file
#
# Prerequisites (checked up front, never worked around):
#   - passwordless key-based SSH to all four IPs
#   - Linux with rootful podman on every node (K3s refuses rootless podman)
#   - openssl on every node (the vendored CA generator runs on node 1)
#   - the rancher/k3s image locally or reachable from docker.io
#   - ports 6443/10250/tcp and 8472/udp free on every node (a container-less
#     leftover flannel.1 holding 8472 is tolerated: the install removes it)
#   - the rancher/k3s image locally or reachable from docker.io
#
# Lab networking note: the VM host source-NATs VM-to-VM traffic, so peers see
# each other coming from NAT_SOURCE_IP. etcd 3.7 verifies a peer's client
# certificate against the observed source address, so that address must be in
# every server certificate — the script appends it as an extra --tls-san.
#
# Usage:
#   bash test/addon/test_k3s_cluster.sh <ip1> <ip2> <ip3> <ip4>
#   bash test/addon/test_k3s_cluster.sh --skip-destroy <ip1> <ip2> <ip3> <ip4>
#   SSH_USER=root SSH_PORT=22 bash test/addon/test_k3s_cluster.sh 192.0.2.10 192.0.2.11 192.0.2.12 192.0.2.13
#
#   SSH_USER (default root), SSH_PORT (default 22)
#   PGCLI_NAMESPACE (default k3ce2e)
#   REMOTE_BASE_DIR (default /root/pgcli-k3s-e2e, wiped between runs)
#   NAT_SOURCE_IP (empty by default; set it to the source address peers observe
#     when the network SNATs node-to-node traffic, e.g. the KVM host's bridge
#     address; empty disables the extra --tls-san)
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
NAMESPACE="${PGCLI_NAMESPACE:-k3ce2e}"
NODE="k3s"
CONTAINER="pgcli-k3s-$NAMESPACE-$NODE"
REMOTE_BASE_DIR="${REMOTE_BASE_DIR:-/root/pgcli-k3s-e2e}"
CONFIG_FILE="$REMOTE_BASE_DIR/pg.yaml"
TOKEN_FILE="$REMOTE_BASE_DIR/k3s-token"
CA_DIR="$REMOTE_BASE_DIR/k3s-ca"
CA_SCRIPT_REMOTE="$REMOTE_BASE_DIR/generate-custom-ca-certs.sh"
MIRROR="docker.io=https://registry-1.docker.io"
# When the network SNATs VM-to-VM traffic, peers see each other coming from
# the NAT source address. etcd 3.7 verifies a peer's client certificate
# against the observed source address, so that address must be in every
# server certificate — pass it as NAT_SOURCE_IP to append an extra --tls-san.
NAT_SOURCE_IP="${NAT_SOURCE_IP:-}"
# 3e9 seconds is ~95 years: proves the injected CA is the 100-year set, not
# the bundled 10-year default.
CA_CHECKEND=3000000000

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CA_SCRIPT_LOCAL="$REPO_ROOT/ansible/roles/k3s_shared/files/generate-custom-ca-certs.sh"

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
run_grep() {  # run_grep <desc> <pattern> <cmd...>
    local desc="$1" pat="$2"; shift 2
    TESTS=$((TESTS + 1))
    local out
    if out=$("$@" 2>&1) && echo "$out" | grep -qF -e "$pat"; then pass "$desc"
    else
        fail "$desc (want '$pat')"
        echo "$out" | sed 's/^/      | /'
    fi
}
run_fails() {  # run_fails <desc> <expected-substring> <cmd...>
    local desc="$1" expect="$2"; shift 2
    TESTS=$((TESTS + 1))
    local out rc
    out=$("$@" 2>&1) && rc=0 || rc=$?
    if [ "$rc" -ne 0 ] && echo "$out" | grep -qF -e "$expect"; then pass "$desc"
    else
        fail "$desc (want failure containing '$expect', rc=$rc)"
        echo "$out" | sed 's/^/      | /'
    fi
}

run_quiet() {  # run_quiet <desc> <cmd...> — like run_test, output shown only on failure
    local desc="$1"; shift
    TESTS=$((TESTS + 1))
    local out
    if out=$("$@" 2>&1); then pass "$desc"
    else
        fail "$desc"
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
push_file() {  # push_file <ip> <local-path> <remote-path> <mode>
    local ip="$1" src="$2" dst="$3" mode="${4:-0644}" tmp="$3.tmp.$$"
    cat "$src" | rssh "$ip" "cat > '$tmp' && chmod $mode '$tmp' && mv -f '$tmp' '$dst'"
}
rpg() {  # rpg <ip> <pg arguments...>
    local ip="$1"; shift
    rssh "$ip" "/usr/local/bin/pg -c '$CONFIG_FILE' $*"
}
run_remote_grep() {  # run_remote_grep <desc> <ip> <pattern> <pg args...>
    local desc="$1" ip="$2" pat="$3"; shift 3
    TESTS=$((TESTS + 1))
    local out
    if out=$(rpg "$ip" "$*" 2>&1) && echo "$out" | grep -qF -e "$pat"; then pass "$desc"
    else
        fail "$desc (want '$pat')"
        echo "$out" | sed 's/^/      | /'
    fi
}
run_remote_fails() {  # run_remote_fails <desc> <ip> <expected> <pg args...>
    local desc="$1" ip="$2" expect="$3"; shift 3
    TESTS=$((TESTS + 1))
    local out rc
    out=$(rpg "$ip" "$*" 2>&1) && rc=0 || rc=$?
    if [ "$rc" -ne 0 ] && echo "$out" | grep -qF -e "$expect"; then pass "$desc"
    else
        fail "$desc (want failure containing '$expect', rc=$rc)"
        echo "$out" | sed 's/^/      | /'
    fi
}
run_install() {  # run_install <desc> <ip> <install args>
    local desc="$1" ip="$2" args="$3"
    TESTS=$((TESTS + 1))
    local out
    if out=$(rpg "$ip" "addon install k3s $args" 2>&1); then pass "$desc"
    else
        fail "$desc"
        echo "$out" | sed 's/^/      | /'
    fi
}

hostname_of() { rssh "$1" hostname; }
is_rootful() { [ "$(rssh "$1" "podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null || true")" = false ]; }
file_mode() { rssh "$1" "stat -c %a '$2'" 2>/dev/null || true; }
container_absent() { ! rssh "$1" "podman ps -a --format '{{.Names}}' | grep -qx '$CONTAINER'"; }
ports_free() {  # ports_free <ip> — a container-less flannel.1 on 8472 is fine: install removes it
    local ip="$1"
    rssh "$ip" '
        ! ss -lntuH | grep -Eq ":(6443|2379|2380|10250)[[:space:]]" || exit 1
        if ss -lntuH | grep -Eq ":8472[[:space:]]" && ! ip link show flannel.1 >/dev/null 2>&1; then
            exit 1
        fi
        ! podman ps --filter status=running --format "{{.Names}}" | grep -q "^pgcli-k3s-"
    '
}

k3s_args() {  # k3s_args <index 0-3>
    local idx="$1" args j
    args="--name $NODE --token-file $TOKEN_FILE --registry-mirror $MIRROR"
    if [ "$idx" -lt 3 ]; then
        args="$args --role server --advertise-address ${NODES[$idx]}"
        for j in 0 1 2; do
            args="$args --tls-san ${NODES[$j]}"
        done
        if [ -n "$NAT_SOURCE_IP" ]; then
            args="$args --tls-san $NAT_SOURCE_IP"
        fi
    else
        args="$args --role agent"
    fi
    if [ "$idx" -eq 0 ]; then
        args="$args --cluster-init --ca-dir $CA_DIR/server/tls"
    else
        args="$args --server https://${NODES[0]}:6443"
    fi
    echo "$args"
}

wait_server_ready() {  # wait_server_ready <ip> — poll /readyz through pg k3s kubectl
    local ip="$1" _
    for _ in $(seq 1 100); do
        if rpg "$ip" "k3s kubectl --name $NODE -- get --raw=/readyz" 2>/dev/null \
                | grep -qx 'ok'; then
            return 0
        fi
        sleep 3
    done
    return 1
}

node_ready() {  # node_ready <server-ip> <node-hostname>
    rpg "$1" "k3s kubectl --name $NODE -- get node $2 --no-headers" 2>/dev/null \
        | grep -q ' Ready '
}

wait_node_ready() {  # wait_node_ready <server-ip> <node-hostname>
    local _ ip="$1" name="$2"
    for _ in $(seq 1 100); do
        node_ready "$ip" "$name" && return 0
        sleep 3
    done
    return 1
}

counts() {  # counts <server-ip> — "<total> <control-plane> <ready>" one per line
    local out
    out="$(rpg "$1" "k3s kubectl --name $NODE -- get nodes --no-headers" 2>/dev/null || true)"
    printf '%s\n' "$out" | grep -c . || true
    printf '%s\n' "$out" | grep -c 'control-plane' || true
    printf '%s\n' "$out" | grep -c ' Ready ' || true
}

LOCAL_BUILD_DIR=""
LOCAL_TOKEN_DIR=""
CLEANED=false
cleanup() {
    local ip
    if [ "$CLEANED" = true ]; then return; fi
    CLEANED=true
    [ -n "$LOCAL_BUILD_DIR" ] && rm -rf "$LOCAL_BUILD_DIR"
    [ -n "$LOCAL_TOKEN_DIR" ] && rm -rf "$LOCAL_TOKEN_DIR"
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping teardown (--skip-destroy); inspect the nodes directly"
        return
    fi
    section "Cleanup (trap)"
    for ip in "${NODES[@]}"; do
        rpg "$ip" "addon remove k3s --name $NODE --clean-data" >/dev/null 2>&1 || true
        rssh "$ip" "rm -rf '$REMOTE_BASE_DIR'" >/dev/null 2>&1 || true
    done
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli K3s cluster test (3 servers + 1 agent)"
    echo "=========================================="
    echo "  Nodes:        ${NODES[*]}"
    echo "  SSH:          $SSH_USER@<node>:$SSH_PORT (key auth, BatchMode)"
    echo "  Namespace:    $NAMESPACE"
    echo "  Container:    $CONTAINER"
    echo "  Base dir:     $REMOTE_BASE_DIR"
    echo "  Topology:     node1 --cluster-init (+custom CA), nodes 2-3 servers, node4 agent"
    echo "=========================================="

    command -v ssh &>/dev/null || { red "ssh is not installed"; exit 1; }
    command -v openssl &>/dev/null || { red "openssl is not installed (needed for the token and CA)"; exit 1; }
    [ -f "$CA_SCRIPT_LOCAL" ] || { red "CA generator missing: $CA_SCRIPT_LOCAL"; exit 1; }

    # ---- Build once, distribute the binary ----
    section "Build & distribute binary"
    if [ -n "${PG_BINARY:-}" ]; then
        LOCAL_PG="$PG_BINARY"
        pass "using pre-built binary: $LOCAL_PG"
    else
        command -v go &>/dev/null || { red "go not installed (or set PG_BINARY=...)"; exit 1; }
        LOCAL_BUILD_DIR="$(mktemp -d)"
        run_test "go build (this checkout)" go build -C "$REPO_ROOT" -o "$LOCAL_BUILD_DIR/pg" .
        LOCAL_PG="$LOCAL_BUILD_DIR/pg"
    fi
    for ip in "${NODES[@]}"; do
        run_test "push binary to $ip" push_binary "$ip" "$LOCAL_PG"
    done

    # ---- Preconditions ----
    section "Preconditions"
    for ip in "${NODES[@]}"; do
        run_test "podman installed on $ip" rssh "$ip" "command -v podman >/dev/null"
        run_test "openssl installed on $ip" rssh "$ip" "command -v openssl >/dev/null"
        run_test "podman is rootful on $ip" is_rootful "$ip"
    done
    if [ "$FAILED" -gt 0 ]; then
        red "  Preconditions failed — fix the nodes above before continuing."
        exit 1
    fi

    # ---- Reset this test's state and init configs ----
    section "Reset test state and init configs"
    for ip in "${NODES[@]}"; do
        run_quiet "reset + config init on $ip" rssh "$ip" "
            set -e
            podman rm -f '$CONTAINER' >/dev/null 2>&1 || true
            rm -rf '$REMOTE_BASE_DIR'
            mkdir -p '$REMOTE_BASE_DIR'
            /usr/local/bin/pg -c '$CONFIG_FILE' config init \
                -o '$CONFIG_FILE' --base-dir '$REMOTE_BASE_DIR' --namespace '$NAMESPACE'
        "
    done

    # ---- Port preconditions (after reset: a leftover container would hold them) ----
    section "Port preconditions"
    for ip in "${NODES[@]}"; do
        run_test "K3s host-network ports free on $ip" ports_free "$ip"
    done

    # ---- Shared material: token on every node, CA only on node 1 ----
    section "Shared token and custom CA"
    LOCAL_TOKEN_DIR="$(mktemp -d)"
    openssl rand -hex 32 > "$LOCAL_TOKEN_DIR/k3s-token"
    for ip in "${NODES[@]}"; do
        run_test "token pushed to $ip (0600)" push_file "$ip" "$LOCAL_TOKEN_DIR/k3s-token" "$TOKEN_FILE" 0600
        run_test "token mode 0600 on $ip" test "$(file_mode "$ip" "$TOKEN_FILE")" = 600
    done

    run_test "push the vendored CA generator to node1" \
        push_file "${NODES[0]}" "$CA_SCRIPT_LOCAL" "$CA_SCRIPT_REMOTE" 0755
    run_quiet "generate a 100-year CA on node1" rssh "${NODES[0]}" \
        "DATA_DIR='$CA_DIR' DAYS=36500 bash '$CA_SCRIPT_REMOTE'"
    run_test "CA set has server-ca.crt" \
        rssh "${NODES[0]}" "test -f '$CA_DIR/server/tls/server-ca.crt'"
    run_test "node1 is the only holder of the source CA dir" \
        test "$(rssh "${NODES[1]}" "test -d '$CA_DIR' && echo yes || echo no")" = no

    # ---- Initial server ----
    section "Install node1 (server --cluster-init, custom CA)"
    run_install "install node1" "${NODES[0]}" "$(k3s_args 0)"
    run_test "node1 API ready (/readyz)" wait_server_ready "${NODES[0]}"
    run_test "custom CA injected into the data dir" \
        rssh "${NODES[0]}" "test -f '$REMOTE_BASE_DIR/addon/k3s/$NODE/data/server/tls/server-ca.crt'"
    run_test "injected CA is valid ~100 years" \
        rssh "${NODES[0]}" "openssl x509 -in '$REMOTE_BASE_DIR/addon/k3s/$NODE/data/server/tls/server-ca.crt' -noout -checkend $CA_CHECKEND"
    run_remote_grep "node1 reports the cluster-init etcd datastore" "${NODES[0]}" \
        "Datastore:   etcd (cluster-init)" "addon list"

    CA1_SUM="$(rssh "${NODES[0]}" "sha256sum '$REMOTE_BASE_DIR/addon/k3s/$NODE/data/server/tls/server-ca.crt' | awk '{print \$1}'")"

    # ---- Joining servers ----
    section "Install nodes 2-3 (servers joining node1)"
    for idx in 1 2; do
        ip="${NODES[$idx]}"
        run_install "install node$((idx + 1))" "$ip" "$(k3s_args "$idx")"
        run_test "node$((idx + 1)) API ready (/readyz)" wait_server_ready "$ip"
        run_remote_grep "node$((idx + 1)) shows as a join" "$ip" "Joins:       https://${NODES[0]}:6443" "addon list"
    done
    run_test "node2 received the identical cluster CA" \
        test "$(rssh "${NODES[1]}" "sha256sum '$REMOTE_BASE_DIR/addon/k3s/$NODE/data/server/tls/server-ca.crt' | awk '{print \$1}'")" = "$CA1_SUM"

    # ---- Agent ----
    section "Install node4 (agent)"
    run_install "install node4" "${NODES[3]}" "$(k3s_args 3)"
    AGENT_HOSTNAME="$(hostname_of "${NODES[3]}")"
    run_test "agent node joins and becomes Ready" wait_node_ready "${NODES[0]}" "$AGENT_HOSTNAME"
    run_remote_grep "agent's local list reports the agent role" "${NODES[3]}" \
        "Role:        agent" "addon list"

    # ---- Topology ----
    section "Cluster topology"
    read -r TOTAL CP READY <<<"$(counts "${NODES[0]}" | paste -sd' ' -)"
    run_test "4 nodes registered ($TOTAL)" test "$TOTAL" -eq 4
    run_test "3 servers report control-plane ($CP)" test "$CP" -eq 3
    run_test "all 4 nodes Ready ($READY)" test "$READY" -eq 4
    run_remote_grep "kubectl wait succeeds from a joining server" "${NODES[2]}" \
        "condition met" "k3s kubectl --name $NODE -- wait --for=condition=Ready node --all --timeout=300s"
    run_remote_grep "every node lists on a joining server too" "${NODES[1]}" \
        "$AGENT_HOSTNAME" "k3s kubectl --name $NODE -- get nodes"

    # ---- Kubeconfig ----
    section "Kubeconfig"
    run_remote_grep "kubeconfig written on node3" "${NODES[2]}" "mode 0600" \
        "k3s kubeconfig --name $NODE --output $REMOTE_BASE_DIR/kubeconfig.yaml"
    run_test "kubeconfig mode 0600 on node3" \
        test "$(file_mode "${NODES[2]}" "$REMOTE_BASE_DIR/kubeconfig.yaml")" = 600
    run_test "kubeconfig points at node3's advertise address" \
        rssh "${NODES[2]}" "grep -qF 'server: https://${NODES[2]}:6443' '$REMOTE_BASE_DIR/kubeconfig.yaml'"

    # ---- Agent has no kubectl ----
    section "Role guard"
    run_remote_fails "agent refuses kubectl" "${NODES[3]}" "is an agent" \
        "k3s kubectl --name $NODE -- get nodes"

    # ---- Per-node files ----
    section "Per-node token and registries"
    for ip in "${NODES[@]}"; do
        run_test "registries.yaml rendered on $ip" \
            rssh "$ip" "grep -qF 'registry-1.docker.io' '$REMOTE_BASE_DIR/addon/k3s/$NODE/registries.yaml'"
    done

    # ---- Self-heal ----
    section "Self-heal"
    run_quiet "restart the agent container on node4" \
        rssh "${NODES[3]}" "podman restart '$CONTAINER'"
    run_test "agent Ready again after a container restart" \
        wait_node_ready "${NODES[0]}" "$AGENT_HOSTNAME"
    run_grep "stop node2" "stopped" rpg "${NODES[1]}" "addon stop k3s --name $NODE"
    run_test "start node2" rpg "${NODES[1]}" "addon start k3s --name $NODE"
    run_test "node2 API ready after stop/start" wait_server_ready "${NODES[1]}"
    NODE2_HOSTNAME="$(hostname_of "${NODES[1]}")"
    run_test "node2 Ready in the cluster after stop/start" \
        wait_node_ready "${NODES[0]}" "$NODE2_HOSTNAME"

    # ---- Idempotent re-install ----
    section "Re-install (idempotent)"
    ID_BEFORE="$(rssh "${NODES[0]}" "podman inspect --format '{{.Id}}' '$CONTAINER'")"
    run_install "reinstall node1 (same flags)" "${NODES[0]}" "$(k3s_args 0)"
    ID_AFTER="$(rssh "${NODES[0]}" "podman inspect --format '{{.Id}}' '$CONTAINER'")"
    if [ -n "$ID_BEFORE" ] && [ "$ID_BEFORE" = "$ID_AFTER" ]; then
        pass "node1 container kept across a plain re-install"
    else
        fail "node1 container replaced by a plain re-install"
    fi

    # ---- Remove (reverse order) ----
    section "Remove (reverse order, --clean-data)"
    for idx in 3 2 1 0; do
        ip="${NODES[$idx]}"
        run_grep "remove node$((idx + 1))" "removed" rpg "$ip" "addon remove k3s --name $NODE --clean-data"
        run_test "container gone on node$((idx + 1))" container_absent "$ip"
        run_test "data dir removed on node$((idx + 1))" \
            rssh "$ip" "test ! -d '$REMOTE_BASE_DIR/addon/k3s/$NODE/data'"
        run_test "generated registries.yaml removed on node$((idx + 1))" \
            rssh "$ip" "test ! -f '$REMOTE_BASE_DIR/addon/k3s/$NODE/registries.yaml'"
    done
    run_test "user token file kept on node1" rssh "${NODES[0]}" "test -f '$TOKEN_FILE'"
    run_test "yaml k3s entries removed on node1" \
        rssh "${NODES[0]}" "! grep -q 'k3s:' '$CONFIG_FILE'"

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main