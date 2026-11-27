#!/usr/bin/env bash
# pgcli K3s addon — single-node end-to-end test.
#
# Drives one standalone server through its whole lifecycle on this host:
#   isolated config init, pre-install validation errors (missing token,
#   agent without --server, cluster-init/server conflict, malformed mirror
#   spec), install as a rootful Podman container on the host network with a
#   pre-generated token file, an advertise address and a containerd mirror,
#   rendered registries.yaml, /readyz via `pg k3s kubectl`, node listing,
#   kubeconfig export (rewritten to the advertise address, mode 0600),
#   `pg logs addon k3s`, `pg addon list`, a namespace surviving
#   stop/start and a --force recreate, autostart enable/status/disable,
#   and remove --clean-data (node data + generated registries.yaml gone,
#   the user's token file kept).
#
# The node-level checks never pull Pod images (K3s loads its packaged
# system images from the node image). Set PGCLI_K3S_POD_TEST=1 to also
# deploy a busybox Pod, which needs docker.io reachability (or a mirror).
#
# Requires Linux with ROOTFUL podman (K3s refuses rootless podman) and the
# rancher/k3s image locally or reachable from docker.io.
#
# Usage:
#   bash test/addon/test_k3s.sh                 # full test, cleans up
#   bash test/addon/test_k3s.sh --skip-destroy  # keep the node after
#   PG_BINARY=/usr/local/bin/pg bash test/addon/test_k3s.sh
#   PGCLI_NAMESPACE=k3e2e PGCLI_K3S_POD_TEST=1 bash test/addon/test_k3s.sh
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-k3s-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-k3s-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-k3e2e}"
NODE="k3s"
CONTAINER="pgcli-k3s-$NAMESPACE-$NODE"
# Loopback-equivalent upstream endpoint: exercises the registries.yaml
# rendering and the mirror mount without changing where pulls actually go.
MIRROR_REGISTRY="docker.io"
MIRROR_ENDPOINT="https://registry-1.docker.io"
POD_TEST="${PGCLI_K3S_POD_TEST:-0}"

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
# run_fail <desc> <expected-substring> <cmd...> — succeed when the command
# fails with an error containing the expected substring.
run_fail() {
    local desc="$1" expect="$2"; shift 2
    TESTS=$((TESTS + 1))
    local out
    out=$("$@" 2>&1) && rc=0 || rc=$?
    if [ "${rc:-0}" -ne 0 ] && echo "$out" | grep -qF -e "$expect"; then
        pass "$desc"
    else
        fail "$desc (want failure containing '$expect', rc=${rc:-0})"
        echo "$out" | sed 's/^/      | /'
    fi
}

pg() { "$BINARY" -c "$CONFIG_FILE" "$@"; }

container_up() {
    podman ps --filter "name=^$CONTAINER$" --filter status=running \
        --format '{{.Names}}' | grep -qx "$CONTAINER"
}
container_gone() {
    ! podman ps -a --filter "name=^$CONTAINER$" --format '{{.Names}}' | grep -qx "$CONTAINER"
}
container_id() { podman inspect --format '{{.Id}}' "$CONTAINER" 2>/dev/null; }

wait_ready() {  # poll /readyz through `pg k3s kubectl`
    for _ in $(seq 1 60); do
        if pg k3s kubectl --name "$NODE" -- get --raw=/readyz 2>/dev/null | grep -qx 'ok'; then
            return 0
        fi
        sleep 3
    done
    return 1
}

# K3s applies the control-plane role label asynchronously, a moment after the
# node registers, so poll for it instead of asserting right after /readyz.
wait_control_plane() {
    for _ in $(seq 1 20); do
        if kube get nodes 2>/dev/null | grep -q 'control-plane'; then
            return 0
        fi
        sleep 3
    done
    return 1
}

kube() { pg k3s kubectl --name "$NODE" -- "$@"; }

install_node() {
    pg addon install k3s --name "$NODE" --role server \
        --token-file "$TEST_DIR/k3s-token" \
        --advertise-address "$ADVERTISE_IP" \
        --registry-mirror "$MIRROR_REGISTRY=$MIRROR_ENDPOINT" "$@"
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove k3s --name "$NODE" --clean-data 2>/dev/null || true
    pg autostart disable --k3s --name "$NODE" 2>/dev/null || true
    podman rm -f "$CONTAINER" >/dev/null 2>&1 || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && rm -rf "$TEST_DIR"
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli K3s Addon Test (single-node server)"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Container: $CONTAINER"
    echo "  Pod test:  $POD_TEST (PGCLI_K3S_POD_TEST=1 to enable)"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }
    command -v openssl &>/dev/null || { red "openssl is not installed"; exit 1; }

    if [ "$(podman info --format '{{.Host.Security.Rootless}}')" = "true" ]; then
        yellow "Skipping: podman is rootless and K3s requires rootful podman."
        exit 0
    fi
    ADVERTISE_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
    [ -n "$ADVERTISE_IP" ] || ADVERTISE_IP="127.0.0.1"

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-k3s-$NAMESPACE-" \
            --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done
    [ -d "$TEST_DIR" ] && rm -rf "$TEST_DIR"
    mkdir -p "$CONFIG_DIR" "$TEST_DIR"
    # config init prompts when the base dir is non-empty; init first, then
    # drop the token file into it.
    run_test "config init (isolated ns)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" --namespace "$NAMESPACE"
    openssl rand -hex 32 > "$TEST_DIR/k3s-token"
    chmod 600 "$TEST_DIR/k3s-token"

    # ---- Pre-install validation (config untouched on failure) ----
    section "Validation"
    run_fail "missing --token-file rejected" "token_file is required" \
        pg addon install k3s --name "$NODE" --role server
    run_fail "agent without --server rejected" "agent nodes must set server_url" \
        pg addon install k3s --name "$NODE" --role agent --token-file "$TEST_DIR/k3s-token"
    run_fail "cluster-init + server rejected" "mutually exclusive" \
        pg addon install k3s --name "$NODE" --role server --token-file "$TEST_DIR/k3s-token" \
            --cluster-init --server https://10.0.0.1:6443
    run_fail "malformed --registry-mirror rejected" "expected REGISTRY=ENDPOINT" \
        pg addon install k3s --name "$NODE" --role server --token-file "$TEST_DIR/k3s-token" \
            --registry-mirror docker.io
    run_fail "dsn rejected (local infra add-on)" "does not accept --dsn" \
        pg addon install k3s --name "$NODE" --role server --token-file "$TEST_DIR/k3s-token" \
            --dsn postgres://user:pass@db.example.com:5432/db

    # ---- Install ----
    section "Install (standalone server, SQLite)"
    TESTS=$((TESTS + 1))
    INSTALL_OUT="$(install_node 2>&1)" && INSTALL_RC=0 || INSTALL_RC=$?
    if [ "$INSTALL_RC" -eq 0 ]; then pass "install k3s --name $NODE"
    else
        fail "install k3s --name $NODE (rc=$INSTALL_RC)"
        echo "$INSTALL_OUT" | sed 's/^/      | /'
    fi
    check_install() {  # <desc> <fixed-pattern>
        TESTS=$((TESTS + 1))
        if echo "$INSTALL_OUT" | grep -qF -e "$2"; then pass "$1"
        else
            fail "$1 (want '$2')"
            echo "$INSTALL_OUT" | sed 's/^/      | /'
        fi
    }
    check_install "reports a SQLite standalone server" "Datastore:   SQLite (standalone server)"
    check_install "reports the container" "Container:   $CONTAINER"
    check_install "reports the token file" "Token file:  $TEST_DIR/k3s-token"
    check_install "reports the mirror" "$MIRROR_REGISTRY -> $MIRROR_ENDPOINT"
    run_test "container running" container_up
    run_test "container is privileged" \
        bash -c "[ \"\$(podman inspect --format '{{.HostConfig.Privileged}}' '$CONTAINER')\" = true ]"
    run_test "container uses the host network" \
        bash -c "[ \"\$(podman inspect --format '{{.HostConfig.NetworkMode}}' '$CONTAINER')\" = host ]"
    run_test "data dir created" test -d "$TEST_DIR/addon/k3s/$NODE/data"
    run_grep "registries.yaml rendered with the mirror endpoint" "$MIRROR_ENDPOINT" \
        cat "$TEST_DIR/addon/k3s/$NODE/registries.yaml"

    # ---- Readiness and kubectl ----
    section "Readiness"
    run_test "node becomes ready (/readyz)" wait_ready
    run_grep "get nodes shows the node Ready" "$(hostname)" kube get nodes
    run_test "node gets the control-plane role" wait_control_plane
    run_grep "kubectl flags work after --" "INTERNAL-IP" kube get nodes -o wide
    run_grep "kubectl without -- works for bare verbs" "$(hostname)" \
        pg k3s kubectl --name "$NODE" get nodes

    # ---- kubeconfig ----
    section "Kubeconfig"
    run_grep "kubeconfig written" "mode 0600" \
        pg k3s kubeconfig --name "$NODE" --output "$TEST_DIR/kubeconfig.yaml"
    run_test "kubeconfig file mode is 0600" \
        bash -c "[ \"\$(stat -c %a '$TEST_DIR/kubeconfig.yaml')\" = 600 ]"
    run_grep "kubeconfig points at the advertise address" "server: https://$ADVERTISE_IP:6443" \
        cat "$TEST_DIR/kubeconfig.yaml"
    run_grep "kubeconfig on stdout" "apiVersion: v1" \
        pg k3s kubeconfig --name "$NODE"

    # ---- Logs and list ----
    section "Logs and list"
    run_grep "logs stream the node output" "k3s" pg logs addon k3s --name "$NODE"
    run_grep "list shows the node" "k3s (name: $NODE)" pg addon list
    run_grep "list shows the server role" "Role:        server" pg addon list
    run_grep "list shows the sqlite datastore" "Datastore:   sqlite" pg addon list
    run_grep "list shows the node running" "Status:      running" pg addon list

    # ---- Stop / start preserves data ----
    section "Stop / start"
    run_grep "stop prints a confirmation" "stopped" pg addon stop k3s --name "$NODE"
    run_test "container stopped" bash -c "! podman ps --filter 'name=^$CONTAINER$' --filter status=running --format '{{.Names}}' | grep -qx '$CONTAINER'"
    run_test "start" pg addon start k3s --name "$NODE"
    run_test "node ready again" wait_ready
    run_test "create a namespace for the persistence check" kube create namespace demo
    run_grep "namespace survives a restart" "demo" kube get namespace demo

    # ---- Force recreate preserves host data ----
    section "Force recreate"
    OLD_ID="$(container_id)"
    run_grep "force recreate announces the container" "Recreating k3s server" install_node --force
    NEW_ID="$(container_id)"
    if [ -n "$OLD_ID" ] && [ -n "$NEW_ID" ] && [ "$OLD_ID" != "$NEW_ID" ]; then
        pass "container was recreated ($OLD_ID -> $NEW_ID)"
    else
        fail "container id unchanged or missing (old=$OLD_ID new=$NEW_ID)"
    fi
    run_test "node ready after recreate" wait_ready
    run_grep "data survived the recreate" "demo" kube get namespace demo

    # ---- Idempotent re-install ----
    section "Re-install (idempotent)"
    run_test "reinstall without --force" install_node
    if [ "$(container_id)" = "$NEW_ID" ]; then
        pass "container kept across a plain re-install"
    else
        fail "plain re-install replaced the container"
    fi

    # ---- Autostart ----
    section "Autostart"
    run_grep "autostart enable" "Auto-start enabled for k3s node \"$NODE\"" \
        pg autostart enable --k3s --name "$NODE"
    run_grep "autostart status shows on" "k3s $NODE" pg autostart status
    run_grep "autostart disable" "Auto-start disabled for k3s node \"$NODE\"" \
        pg autostart disable --k3s --name "$NODE"

    # ---- Optional Pod test ----
    if [ "$POD_TEST" = "1" ]; then
        section "Pod (needs docker.io reachability)"
        kube apply -f - <<'MANIFEST'
apiVersion: v1
kind: Pod
metadata:
  name: k3s-e2e
spec:
  restartPolicy: Never
  containers:
    - name: pause
      image: busybox:1.36
      command: ["sleep", "60"]
MANIFEST
        for _ in $(seq 1 30); do
            if kube get pod k3s-e2e 2>/dev/null | grep -q " Running "; then
                break
            fi
            sleep 3
        done
        run_grep "busybox pod reaches Running" " Running " kube get pod k3s-e2e
        kube delete pod k3s-e2e --ignore-not-found >/dev/null 2>&1 || true
    fi

    # ---- Remove ----
    section "Remove"
    run_test "remove with --clean-data" pg addon remove k3s --name "$NODE" --clean-data
    run_test "container gone" container_gone
    run_test "data dir removed" bash -c "test ! -d '$TEST_DIR/addon/k3s/$NODE'"
    run_test "generated registries.yaml removed" \
        bash -c "test ! -f '$TEST_DIR/addon/k3s/$NODE/registries.yaml'"
    run_test "user token file kept" test -f "$TEST_DIR/k3s-token"
    run_test "yaml k3s entries removed" bash -c "! grep -q 'k3s:' '$CONFIG_FILE'"

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main