#!/usr/bin/env bash
# pgcli vmalert addon end-to-end test.
#
# vmalert is a rule-evaluation engine, so the assertions are about the full
# chain rather than a single HTTP surface:
#
#   1. DEFAULT WIRING — datasource points at VictoriaLogs (9428 by default; this
#      test overrides the port) and the notifier at Alertmanager, with rules and
#      templates bind-mounted READ-ONLY at the documented container paths.
#   2. A VLOGS ALERT ACTUALLY FIRES — a marker line is ingested into the real
#      VictoriaLogs instance, a matching `type: vlogs` rule is evaluated, and
#      the alert must appear in BOTH vmalert's /api/v1/alerts and Alertmanager's
#      /api/v2/alerts. This proves the datasource and notifier flags work, not
#      just that the process started.
#   3. RELOAD — appending a second group and running `pg addon vmalert reload`
#      must change the live Groups/Rules counts reported by `status`.
#   4. IDEMPOTENT REINSTALL — identical flags print "config unchanged" and keep
#      the same container ID; a changed flag recreates it.
#   5. RULES ARE NEVER OVERWRITTEN — --force recreates the container but the
#      operator's rule file keeps its hash.
#   6. REMOVE BOUNDARIES — remove keeps explicit rules/templates dirs by
#      default, --clean-data still never deletes an EXPLICIT dir, and only a
#      pgcli-managed dir is deleted by --clean-data.
#
# Usage:
#   bash test/addon/test_vmalert.sh
#   bash test/addon/test_vmalert.sh --skip-destroy
#   PG_BINARY=/path/to/pg bash test/addon/test_vmalert.sh
#
# Needs docker.io access (or pre-loaded victoria-logs / alertmanager / vmalert
# images). Run on Linux; the macOS bridge path is covered by unit tests.
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-vmalert-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-vmalert-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-vme2e}"
VL_PORT="${PGCLI_VL_PORT:-39428}"
AM_PORT="${PGCLI_AM_PORT:-39093}"
VMALERT_PORT="${PGCLI_VMALERT_PORT:-38880}"
PG_START_PORT="${PGCLI_PG_START_PORT:-38400}"
PG_SSH_PORT="${PGCLI_SSH_START_PORT:-43400}"

VL_NAME="vl-e2e"
AM_NAME="am-e2e"
VM_NAME="vm-e2e"
MANAGED_NAME="vm-managed"

RULES_DIR="$TEST_DIR/vmalert-rules"
TEMPLATES_DIR="$TEST_DIR/vmalert-templates"
MANAGED_RULES_DIR="$TEST_DIR/addon/vmalert/$MANAGED_NAME/rules"
MANAGED_TEMPLATES_DIR="$TEST_DIR/addon/vmalert/$MANAGED_NAME/templates"
MARKER="vmalert-e2e-marker"

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
    local out
    if out=$("$@" 2>&1) && echo "$out" | grep -qF -e "$expect"; then
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

container_of_vm() { echo "pgcli-vmalert-$NAMESPACE-$1"; }

vm_up() {
    podman ps --filter "name=$(container_of_vm "$1")" --filter status=running \
        --format '{{.Names}}' | grep -q "$(container_of_vm "$1")"
}
vm_gone() {
    ! podman ps -a --filter "name=$(container_of_vm "$1")" --format '{{.Names}}' | grep -q .
}
vm_id() { podman inspect --format '{{.Id}}' "$(container_of_vm "$1")" 2>/dev/null; }
vm_cmd() { podman inspect --format '{{json .Config.Cmd}}' "$(container_of_vm "$1")" 2>/dev/null; }

mount_ro() {  # mount_ro <name> <container-destination>
    podman inspect "$(container_of_vm "$1")" --format '{{json .Mounts}}' \
        | tr '{' '\n' | grep -F "\"Destination\":\"$2\"" | grep -q '"RW":false'
}

http_code() {
    local port="$1" path="${2:-/}"
    curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port$path" 2>/dev/null
}
wait_http() {  # wait_http <port> <path> [tries]
    local port="$1" path="$2" tries="${3:-60}" code
    for _ in $(seq 1 "$tries"); do
        code="$(http_code "$port" "$path" || true)"
        case "$code" in 2*|3*) return 0 ;; esac
        sleep 1
    done
    return 1
}

vmalert_alerts() { curl -sf "http://127.0.0.1:$VMALERT_PORT/api/v1/alerts" 2>/dev/null; }
vmalert_rules()  { curl -sf "http://127.0.0.1:$VMALERT_PORT/api/v1/rules" 2>/dev/null; }
am_alerts()      { curl -sf "http://127.0.0.1:$AM_PORT/api/v2/alerts" 2>/dev/null; }

alertmarker_firing() {
    local body
    body="$(vmalert_alerts)" || return 1
    echo "$body" | grep -q "$MARKER" \
        && echo "$body" | grep -Eq '"state"[[:space:]]*:[[:space:]]*"firing"'
}
alertmarker_in_am() {
    local body
    body="$(am_alerts)" || return 1
    # AM's payload carries alertname/annotations, not the rule expression, so
    # match the alert name rather than the raw LogsQL marker.
    echo "$body" | grep -q "VmalertE2EMarker"
}
wait_until() {  # wait_until <tries> <cmd...>
    local tries="$1"; shift
    for _ in $(seq 1 "$tries"); do
        if "$@" >/dev/null 2>&1; then return 0; fi
        sleep 1
    done
    return 1
}

status_field() {  # status_field <name> <Groups|Rules>
    pg addon vmalert status --name "$1" 2>/dev/null \
        | awk -F': *' -v k="$2" '$0 ~ "^  "k":" {print $2; exit}'
}
status_groups() { status_field "$1" Groups; }
status_rules()  { status_field "$1" Rules; }
groups_are()    { [ "$(status_groups "$1")" = "$2" ]; }
rules_start()   { case "$(status_rules "$1")" in "$2"*) return 0 ;; *) return 1 ;; esac; }

write_rules_file() {
    cat > "$RULES_DIR/alert.rules.yaml" <<'YAML'
groups:
  - name: e2e
    type: vlogs
    interval: 5s
    rules:
      - alert: VmalertE2EMarker
        expr: '_time:5m | "vmalert-e2e-marker" | count() as cnt | filter cnt:>0'
        for: 0s
        labels:
          severity: critical
        annotations:
          summary: 'vmalert e2e marker seen'
YAML
}

append_second_group() {
    cat >> "$RULES_DIR/alert.rules.yaml" <<'YAML'
  - name: e2e-second
    type: vlogs
    interval: 5s
    rules:
      - alert: VmalertE2ENeverFires
        expr: '_time:5m | "vmalert-e2e-no-such-marker" | count() as cnt | filter cnt:>0'
        for: 0s
        labels:
          severity: info
YAML
}

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove vmalert --name "$MANAGED_NAME" --clean-data 2>/dev/null || true
    pg addon remove vmalert --name "$VM_NAME" --clean-data 2>/dev/null || true
    pg addon remove alertmanager --name "$AM_NAME" --clean-data 2>/dev/null || true
    pg addon remove victorialogs --name "$VL_NAME" --clean-data 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli vmalert Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Ports:     VL $VL_PORT / AM $AM_PORT / vmalert $VMALERT_PORT (PG $PG_START_PORT+, SSH $PG_SSH_PORT+)"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }
    command -v curl   &>/dev/null || { red "curl is not installed"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-vmalert-$NAMESPACE-" --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    mkdir -p "$CONFIG_DIR" "$TEST_DIR"

    run_test "config init (isolated ns + port ranges)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" \
        --namespace "$NAMESPACE" --pg-start-port "$PG_START_PORT" \
        --pg-ssh-port "$PG_SSH_PORT" --add e2e
    mkdir -p "$RULES_DIR" "$TEMPLATES_DIR"
    sed -i "s/^victorialogs_start_port:.*/victorialogs_start_port: $VL_PORT/" "$CONFIG_FILE"
    sed -i "s/^alertmanager_start_port:.*/alertmanager_start_port: $AM_PORT/" "$CONFIG_FILE"
    sed -i "s/^vmalert_start_port:.*/vmalert_start_port: $VMALERT_PORT/" "$CONFIG_FILE"
    run_grep "vmalert_start_port applied" "vmalert_start_port: $VMALERT_PORT" cat "$CONFIG_FILE"

    # ---- Dependencies ----
    section "VictoriaLogs + Alertmanager"
    run_test "install victorialogs on $VL_PORT" pg addon install victorialogs \
        --name "$VL_NAME" --port "$VL_PORT" --listen 127.0.0.1
    run_test "VictoriaLogs answers /health" wait_http "$VL_PORT" /health 60
    run_test "install alertmanager on $AM_PORT" pg addon install alertmanager \
        --name "$AM_NAME" --port "$AM_PORT" --listen 127.0.0.1
    run_test "Alertmanager answers /-/ready" wait_http "$AM_PORT" /-/ready 60

    # ---- Install ----
    section "Install vmalert --name $VM_NAME"
    TESTS=$((TESTS + 1))
    if INSTALL_OUT="$(pg addon install vmalert --name "$VM_NAME" \
            --port "$VMALERT_PORT" --listen 127.0.0.1 \
            --datasource-url "http://127.0.0.1:$VL_PORT" \
            --notifier-url "http://127.0.0.1:$AM_PORT" \
            --rules-dir "$RULES_DIR" --templates-dir "$TEMPLATES_DIR" 2>&1)"; then
        pass "install vmalert (explicit ports + dirs)"
    else
        fail "install vmalert"
        echo "$INSTALL_OUT" | sed 's/^/      | /'
    fi
    echo "$INSTALL_OUT" | grep -E '✓ vmalert installed|Datasource|Notifier|Rules dir|Templates dir' | sed 's/^/      /' || true

    run_test "container is running" vm_up "$VM_NAME"
    run_test "seed rules file exists" test -f "$RULES_DIR/alert.rules.yaml"
    run_test "seed template exists"   test -f "$TEMPLATES_DIR/default.tpl"
    run_test "rules dir mounted read-only"     mount_ro "$VM_NAME" /etc/vmalert/rules
    run_test "templates dir mounted read-only" mount_ro "$VM_NAME" /etc/vmalert/templates
    run_grep "datasource flag wired"  "-datasource.url=http://127.0.0.1:$VL_PORT" vm_cmd "$VM_NAME"
    run_grep "notifier flag wired"    "-notifier.url=http://127.0.0.1:$AM_PORT"    vm_cmd "$VM_NAME"
    run_grep "rules glob wired"       "-rule=/etc/vmalert/rules/*.yaml"            vm_cmd "$VM_NAME"
    run_grep "templates glob wired"   "-rule.templates=/etc/vmalert/templates/*.tpl" vm_cmd "$VM_NAME"
    run_grep "listen addr wired"      "-httpListenAddr=127.0.0.1:$VMALERT_PORT"    vm_cmd "$VM_NAME"
    run_test "vmalert answers /-/ready" wait_http "$VMALERT_PORT" /-/ready 60
    run_grep "rules API reports success" '"status":"success"' vmalert_rules

    # ---- Fire a real vlogs alert ----
    section "Vlogs rule -> firing -> Alertmanager"
    # VictoriaLogs' /insert/jsonline silently DROPS the payload (still HTTP 200)
    # unless the stream+json content type is set — the marker must really land.
    curl -sf -X POST "http://127.0.0.1:$VL_PORT/insert/jsonline" \
        -H 'Content-Type: application/stream+json' \
        --data-binary "{\"_msg\":\"$MARKER\",\"_time\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}" \
        >/dev/null
    write_rules_file
    run_test "reload after writing the rule" pg addon vmalert reload --name "$VM_NAME"
    run_test "vmalert reports the alert firing (<=60s)" wait_until 60 alertmarker_firing
    run_test "Alertmanager received the alert (<=60s)"  wait_until 60 alertmarker_in_am
    run_test "status shows 1 group / 1 rule" groups_are "$VM_NAME" 1
    run_test "status rule count is 1"        rules_start "$VM_NAME" "1 "

    # ---- Reload picks up a new group ----
    section "Reload picks up a second group"
    append_second_group
    run_test "reload after appending a group" pg addon vmalert reload --name "$VM_NAME"
    run_test "status reaches 2 groups (<=30s)" wait_until 30 groups_are "$VM_NAME" 2
    run_test "status rule count is 2"           rules_start "$VM_NAME" "2 "

    # ---- Idempotent reinstall ----
    section "Idempotent reinstall and config change"
    ID_BEFORE="$(vm_id "$VM_NAME")"
    TESTS=$((TESTS + 1))
    if SAME_OUT="$(pg addon install vmalert --name "$VM_NAME" \
            --port "$VMALERT_PORT" --listen 127.0.0.1 \
            --datasource-url "http://127.0.0.1:$VL_PORT" \
            --notifier-url "http://127.0.0.1:$AM_PORT" \
            --rules-dir "$RULES_DIR" --templates-dir "$TEMPLATES_DIR" 2>&1)" \
            && echo "$SAME_OUT" | grep -qF "config unchanged"; then
        pass "identical reinstall is a no-op (config unchanged)"
    else
        fail "identical reinstall should print 'config unchanged'"
        echo "$SAME_OUT" | sed 's/^/      | /'
    fi
    run_test "container ID unchanged on no-op" test "$ID_BEFORE" = "$(vm_id "$VM_NAME")"

    run_test "reinstall with --external-label recreates" pg addon install vmalert \
        --name "$VM_NAME" --port "$VMALERT_PORT" --listen 127.0.0.1 \
        --datasource-url "http://127.0.0.1:$VL_PORT" \
        --notifier-url "http://127.0.0.1:$AM_PORT" \
        --rules-dir "$RULES_DIR" --templates-dir "$TEMPLATES_DIR" \
        --external-label env=e2e
    run_test "container ID changed after config change" test "$ID_BEFORE" != "$(vm_id "$VM_NAME")"
    run_grep "external label flag wired" "-external.label=env=e2e" vm_cmd "$VM_NAME"

    RULES_HASH="$(sha256sum "$RULES_DIR/alert.rules.yaml" | awk '{print $1}')"
    run_test "--force recreates the container" pg addon install vmalert \
        --name "$VM_NAME" --port "$VMALERT_PORT" --listen 127.0.0.1 \
        --datasource-url "http://127.0.0.1:$VL_PORT" \
        --notifier-url "http://127.0.0.1:$AM_PORT" \
        --rules-dir "$RULES_DIR" --templates-dir "$TEMPLATES_DIR" \
        --external-label env=e2e --force
    run_test "user rules file untouched by --force" \
        test "$RULES_HASH" = "$(sha256sum "$RULES_DIR/alert.rules.yaml" | awk '{print $1}')"

    # ---- Remove boundaries ----
    section "Remove boundaries"
    run_test "remove keeps explicit rules/templates dirs" pg addon remove vmalert --name "$VM_NAME"
    run_test "explicit rules dir still exists"     test -f "$RULES_DIR/alert.rules.yaml"
    run_test "explicit templates dir still exists" test -f "$TEMPLATES_DIR/default.tpl"
    run_test "container removed" vm_gone "$VM_NAME"

    run_test "reinstall for --clean-data boundary" pg addon install vmalert \
        --name "$VM_NAME" --port "$VMALERT_PORT" --listen 127.0.0.1 \
        --datasource-url "http://127.0.0.1:$VL_PORT" \
        --notifier-url "http://127.0.0.1:$AM_PORT" \
        --rules-dir "$RULES_DIR" --templates-dir "$TEMPLATES_DIR"
    run_grep "--clean-data refuses explicit dirs" "kept (user-managed)" \
        pg addon remove vmalert --name "$VM_NAME" --clean-data
    run_test "explicit rules dir survives --clean-data"     test -f "$RULES_DIR/alert.rules.yaml"
    run_test "explicit templates dir survives --clean-data" test -f "$TEMPLATES_DIR/default.tpl"

    # ---- Managed-dir cleanup ----
    section "Managed directory --clean-data"
    run_test "install managed-dir instance" pg addon install vmalert \
        --name "$MANAGED_NAME" --listen 127.0.0.1 \
        --datasource-url "http://127.0.0.1:$VL_PORT" \
        --notifier-url "http://127.0.0.1:$AM_PORT"
    run_test "managed rules dir seeded"     test -f "$MANAGED_RULES_DIR/alert.rules.yaml"
    run_test "managed templates dir seeded" test -f "$MANAGED_TEMPLATES_DIR/default.tpl"
    run_test "remove --clean-data deletes managed instance" \
        pg addon remove vmalert --name "$MANAGED_NAME" --clean-data
    run_test "managed rules dir deleted"     test ! -e "$MANAGED_RULES_DIR"
    run_test "managed templates dir deleted" test ! -e "$MANAGED_TEMPLATES_DIR"

    # ---- Summary ----
    echo ""
    echo "=========================================="
    echo "  Results: $((TESTS - FAILED))/$TESTS passed"
    echo "=========================================="
    [ "$FAILED" -eq 0 ]
}

main "$@"
