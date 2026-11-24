#!/usr/bin/env bash
# pgcli SafeLine WAF addon end-to-end test.
#
# SafeLine is pgcli's first seven-container top-level addon, and it violates the
# "one container on the host network" house style on purpose (upstream compose
# contract). This script therefore concentrates on what is UNIQUE to it:
#
#   1. ROOTFUL LINUX ONLY — the stack pins six containers to static addresses
#      inside a dedicated /24 and runs tengine on host networking. On a rootless
#      podman the install must be refused with a clear message (that refusal is
#      asserted and the rest of the script is skipped); on Linux rootful the
#      static IPs, subnet, gateway and owner label are all asserted through
#      `podman inspect` / `podman network inspect`.
#   2. THE SEVEN-ROLE STACK — postgres, mgt, detector, fvm, chaos, luigi and
#      tengine are created and started in dependency order, every bridge role
#      has its fixed address (.2/.4/.5/.8/.10/.7), tengine is host-networked,
#      and only mgt publishes a port (container 1443 -> --port).
#   3. THE CONSOLE — the HTTPS health endpoint answers 200 on the published
#      port, the admin credentials come from the mgt container's `resetadmin`
#      bootstrap (captured at install, retrievable via `pg addon password
#      safeline`, equal to what the config stores, and NOT the bundled
#      PostgreSQL password), and the bundled database is reachable with the
#      generated DSN inside the postgres container.
#   4. IDEMPOTENCY AND DRIFT — re-running the same install is a no-op (mgt
#      container id unchanged), changing a stored flag without --force is
#      refused, and --force recreates all seven containers while KEEPING the
#      bundled PostgreSQL data (PG_VERSION still there) and rotating the
#      console password.
#   5. LIFECYCLE — stop takes the stack down (console unreachable) and start
#      brings it back WITHOUT re-running resetadmin (the stored password
#      stays); `--service` selects a role for logs; remove keeps the data
#      directory by default (reinstall resumes on it) and --clean-data drops
#      it, always removing the dedicated network.
#   6. FLAG CONTRACT — flags owned by other addons (--password, --image, --dsn,
#      ...) are rejected up front, and subnet/port conflicts and malformed
#      values fail during validation, before any image is pulled.
#   7. TRAFFIC — the console web API is driven the way the UI drives it (AES
#      encrypted admin login with the csrf token, POST /api/open/site), local
#      upstream HTTP servers on this host and a public HTTPS upstream
#      (www.baidu.com) are proxied through tengine on the new sites' entry
#      ports, and a hostname upstream picks up its new address only after
#      `pg addon reload`.
#
# Usage:
#   bash test/addon/test_safeline.sh                   # full test, cleans up
#   bash test/addon/test_safeline.sh --skip-destroy     # keep everything after
#   PG_BINARY=/path/to/pg bash test/addon/test_safeline.sh
#   PGCLI_NAMESPACE=sfe2e PGCLI_SAFELINE_CONSOLE_PORT=39543 \
#     PGCLI_SAFELINE_SUBNET=172.29.229 bash test/addon/test_safeline.sh
#
# Needs docker.io access (or the seven chaitin images pre-loaded with `podman
# load`) — pgcli only pulls, it never builds them. Requires rootful podman on
# Linux; on a rootless host the script asserts the refusal and exits.
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-safeline-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-safeline-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
LOG_DIR="${PGCLI_LOG_DIR:-/tmp/pgcli-safeline-e2e-logs}"
NAMESPACE="${PGCLI_NAMESPACE:-sfe2e}"
STACK="waf1"
CONSOLE_PORT="${PGCLI_SAFELINE_CONSOLE_PORT:-39543}"
SUBNET="${PGCLI_SAFELINE_SUBNET:-172.29.229}"

PREFIX="pgcli-safeline-$NAMESPACE-$STACK"
CONTAINER_PORT=1443
# Start order, as the manager uses it (stop/remove walk it backwards).
ROLES="postgres mgt detector fvm chaos luigi tengine"
DATA_DIR="$TEST_DIR/addon/safeline/$STACK"

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
run_not_grep() {
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

# contains <haystack> <fixed needle> — plain substring test for captured output.
contains() { [[ "$1" == *"$2"* ]]; }

# sf_field <name> <key> — value under addons.safeline.<name> (8-space map key,
# yaml.v3's 4-space indentation). One layer of quotes is stripped.
sf_field() {
    awk -v n="        $1:" -v k="$2:" '
        $0 == n {f=1; next}
        f && /^        [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {
            gsub(/^[[:space:]]*[a-z_]+: /,""); gsub(/^"|"$/,""); print; exit
        }
    ' "$CONFIG_FILE"
}

cname() { echo "$PREFIX-$1"; }
cid_of() { podman inspect "$(cname "$1")" --format '{{.Id}}' 2>/dev/null; }
# podman 4.9's sqlite backend occasionally answers a read with a stale or empty
# (rc=0) result while containers are churning, so state assertions poll for a
# few seconds before declaring a miss. Diagnostics from the last attempt are
# printed only when the deadline expires.
eventually() {  # eventually <func> [args...]
    # podman 4.9's sqlite backend can serve stale or empty reads for a few
    # seconds right after container churn, so give the retries room to outlast
    # it; a healthy stack answers on the first attempt.
    local deadline=$((SECONDS + 10)) err
    while :; do
        err="$("$@" 2>&1)" && return 0
        [ "$SECONDS" -ge "$deadline" ] && { [ -n "$err" ] && echo "$err" >&2; return 1; }
        sleep 0.3
    done
}
# podman 4.9's sqlite backend occasionally fails a list right after container
# churn; retry once and surface the raw error instead of calling a role down.
ps_list() {  # ps_list [ps|-a...] — running (default) or all container names
    local out rc
    for _ in 1 2; do
        out="$(podman "$@" --format '{{.Names}}' 2>&1)" && { printf '%s\n' "$out"; return 0; }
        rc=$?
        sleep 0.5
    done
    echo "podman $* failed (rc=$rc): $out" >&2
    return 1
}
is_running() {
    local out
    out="$(ps_list ps)" || return 1
    grep -qx "$(cname "$1")" <<<"$out" ||
        { echo "not running: $1 (listed: $(echo "$out" | tr '\n' ' '))" >&2; return 1; }
}
is_absent() {
    local out
    out="$(ps_list ps -a)" || return 1
    ! grep -qx "$(cname "$1")" <<<"$out"
}
all_running() { local r; for r in $ROLES; do is_running "$r" || return 1; done; }
none_running() { local r; for r in $ROLES; do is_running "$r" && return 1; done; return 0; }
all_absent()  { local r; for r in $ROLES; do is_absent  "$r" || return 1; done; }
ip_of() { podman inspect "$(cname "$1")" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null; }
# podman records "bridge" (the driver) in HostConfig.NetworkMode for --network
# <name>:ip=…, so attachment must be read from NetworkSettings.Networks. A
# failed inspect is retried once and its raw output kept for the diagnostic,
# since the sqlite backend occasionally errors or reads empty after churn.
net_attached() {
    local out
    for _ in 1 2; do
        out="$(podman inspect "$(cname "$1")" --format '{{range $k,$v := .NetworkSettings.Networks}}{{println $k}}{{end}}' 2>&1)" && break
        sleep 0.5
    done
    grep -qx "$PREFIX" <<<"$out" || { echo "not attached: $1 (networks: $(echo "$out" | tr '\n' ' '))" >&2; return 1; }
}
bridge_roles_attached() { local r; for r in postgres mgt detector luigi fvm chaos; do net_attached "$r" || { echo "not attached: $r" >&2; return 1; }; done; }
restart_policy_of() { podman inspect "$(cname "$1")" --format '{{.HostConfig.RestartPolicy.Name}}' 2>/dev/null; }
env_of() { podman inspect "$(cname "$1")" --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null; }
# Capture before matching: `env_of | grep -q` is a pipefail trap.
env_has() { local out; out="$(env_of "$1")"; [[ "$out" == *"$2"* ]]; }
net_exists() { podman network ls --format '{{.Name}}' 2>/dev/null | grep -qx "$PREFIX"; }
net_has_subnet() { [ "$(podman network inspect "$PREFIX" --format '{{range .Subnets}}{{.Subnet}}{{end}}' 2>/dev/null)" = "$1" ]; }
net_has_gateway() { [ "$(podman network inspect "$PREFIX" --format '{{range .Subnets}}{{.Gateway}}{{end}}' 2>/dev/null)" = "$1" ]; }
net_has_label() { [ "$(podman network inspect "$PREFIX" --format '{{index .Labels "pgcli.safeline"}}' 2>/dev/null)" = "$1" ]; }
health_code() { curl -k -s -o /dev/null -w '%{http_code}' "https://127.0.0.1:$CONSOLE_PORT/api/open/health" 2>/dev/null; }
health_ok() { [ "$(health_code)" = "200" ]; }
health_down() { ! curl -k -s -f -o /dev/null "https://127.0.0.1:$CONSOLE_PORT/api/open/health" 2>/dev/null; }
mgt_port_published() { podman port "$(cname mgt)" "$CONTAINER_PORT" 2>/dev/null | grep -q ":$CONSOLE_PORT$"; }
db_ok() { podman exec "$(cname postgres)" psql -U safeline-ce -d safeline-ce -tAc 'select 1' 2>/dev/null | grep -qx '1'; }
# addon reload wraps `nginx -t && nginx -s reload` in tengine; the stack has to
# stay healthy and running afterwards.
reload_ok() {
    local out
    out="$(pg addon reload safeline --name "$STACK" 2>&1)" || { echo "$out" >&2; return 1; }
    [[ "$out" == *"tengine reloaded"* ]] && eventually all_running && health_ok
}
pg_data_present() { test -f "$DATA_DIR/resources/postgres/data/PG_VERSION"; }
admin_pw_matches_config() {
    local stored printed
    stored="$(sf_field "$STACK" admin_password)"
    printed="$(pg addon password safeline --name "$STACK" 2>/dev/null)"
    [ -n "$stored" ] && [ "$stored" = "$printed" ]
}
admin_pw_differs_from_db() {
    local a b
    a="$(sf_field "$STACK" admin_password)"
    b="$(sf_field "$STACK" postgres_password)"
    [ -n "$a" ] && [ -n "$b" ] && [ "$a" != "$b" ]
}
# --reset reruns resetadmin: the printed password must differ from the stored
# one, and pg.yaml's admin_password must move along with it.
admin_reset_rotates() {
    local old new stored
    old="$(pg addon password safeline --name "$STACK" 2>/dev/null)" || return 1
    new="$(pg addon password safeline --name "$STACK" --reset 2>/dev/null)" || return 1
    [ -n "$new" ] && [ "$new" != "$old" ] || return 1
    stored="$(pg addon password safeline --name "$STACK" 2>/dev/null)" || return 1
    [ "$stored" = "$new" ] && admin_pw_matches_config
}
# create_order_ok <install output> — the six "container ... created" lines (and
# tengine's) appear in role order.
create_order_ok() {
    local out="$1" prev=0 n r
    for r in $ROLES; do
        n="$(echo "$out" | grep -n "SafeLine $r container" | head -1 | cut -d: -f1)"
        [ -n "$n" ] || return 1
        [ "$n" -gt "$prev" ] || return 1
        prev="$n"
    done
    return 0
}
addon_list_ok() {
    local out
    out="$(pg addon list 2>&1)" || return 1
    contains "$out" "Security add-ons (safeline):" &&
        contains "$out" "7/7 containers running" &&
        contains "$out" "https://127.0.0.1:$CONSOLE_PORT"
}
logs_nonempty() { [ -n "$(pg logs addon safeline --name "$STACK" "$@" 2>&1)" ]; }
autostart_safeline() {  # autostart_safeline <enable|disable> <true|false>
    pg autostart "$1" --safeline --name "$STACK" >/dev/null 2>&1 || return 1
    local v; v="$(sf_field "$STACK" autostart)"
    # autostart is omitempty: a disabled flag is absent from the config.
    [ -z "$v" ] && v=false
    [ "$v" = "$2" ]
}
# The detector image enables DETECTOR_MGMT_AUTH by default and only accepts
# callers that present the shared token fvm/luigi read from
# DETECTOR_MGMT_TOKEN, so all three roles must carry the stored value.
detector_token_wired() {
    local tok
    tok="$(sf_field "$STACK" detector_mgmt_token)"
    [ -n "$tok" ] || return 1
    env_has detector "DETECTOR_MGMT_TOKEN=$tok" &&
        env_has fvm "DETECTOR_MGMT_TOKEN=$tok" &&
        env_has luigi "DETECTOR_MGMT_TOKEN=$tok" &&
        ! env_has mgt "DETECTOR_MGMT_TOKEN=" &&
        ! env_has tengine "DETECTOR_MGMT_TOKEN="
}

# --- console web API fixtures -------------------------------------------
API_BASE="https://127.0.0.1:$CONSOLE_PORT/api"
UPSTREAM_PORT=""
UPSTREAM_PIDS=""

# json_get <dotted.path> — read stdin as JSON and print the value (empty for a
# missing path or null).
json_get() {
    python3 -c '
import sys, json
d = json.load(sys.stdin)
for k in sys.argv[1].split("."):
    d = d.get(k) if isinstance(d, dict) else None
print("" if d is None else d)' "$1"
}

# console_token logs in exactly like the console web UI: fetch the AES key and
# a csrf token, AES-CBC encrypt the admin password with a random salt whose hex
# string doubles as the IV, then exchange the base64(salt + ciphertext) blob
# for a JWT.
console_token() {
    local pw key csrf salt keyhex ivhex enc
    pw="$(pg addon password safeline --name "$STACK" 2>/dev/null)" || return 1
    key="$(curl -sk "$API_BASE/open/system/key" | json_get data)" || return 1
    csrf="$(curl -sk "$API_BASE/open/auth/csrf" | json_get data.csrf_token)" || return 1
    [ -n "$pw" ] && [ -n "$key" ] && [ -n "$csrf" ] || return 1
    salt="$(openssl rand -hex 8)"
    keyhex="$(printf '%s' "$key" | od -An -tx1 | tr -d ' \n')"
    ivhex="$(printf '%s' "$salt" | od -An -tx1 | tr -d ' \n')"
    enc="$( { printf '%s' "$salt"; printf '%s' "$pw" | openssl enc -aes-128-cbc -K "$keyhex" -iv "$ivhex" -nosalt; } | base64 -w0)"
    curl -sk -X POST "$API_BASE/open/auth/login" -H 'Content-Type: application/json' \
        --data "{\"username\":\"admin\",\"password\":\"$enc\",\"csrf_token\":\"$csrf\"}" | json_get data.jwt
}

# site_create <token> <entry-port> <upstream-url> [server-name] — POST the same
# payload the console's site form sends; echoes the new site id. server-name
# defaults to "*"; a real upstream vhost needs the site to own its domain,
# otherwise the original Host header (entry port) reaches the upstream. On
# failure the full API response (including msg) goes to stderr.
site_create() {
    local token="$1" port="$2" upstream="$3" name="${4:-*}" body resp id
    body="$(printf '{"server_names":["%s"],"ports":["%s"],"upstreams":["%s"],"comment":"e2e-traffic","email":"","cert_id":0,"load_balance":{"balance_type":1},"health_check":false,"index":"","static_default":1,"type":0}' "$name" "$port" "$upstream")"
    resp="$(curl -sk -X POST "$API_BASE/open/site" -H 'Content-Type: application/json' \
        -H "Authorization: Bearer $token" --data "$body")" || { echo "create request failed" >&2; return 1; }
    id="$(printf '%s' "$resp" | json_get data)"
    [[ "$id" =~ ^[0-9]+$ ]] || { echo "create failed: $resp" >&2; return 1; }
    echo "$id"
}

site_delete() {  # site_delete <token> <id>
    curl -sk -X DELETE "$API_BASE/open/site" -H 'Content-Type: application/json' \
        -H "Authorization: Bearer $1" --data "{\"ids\":[$2]}" >/dev/null || true
}

# free_port picks a host port that is free right now.
free_port() {
    local p
    for _ in $(seq 1 50); do
        p="$(shuf -i 20000-29999 -n 1)"
        ss -tln 2>/dev/null | grep -q ":$p " || { echo "$p"; return 0; }
    done
    return 1
}

# tengine_hosts_add <address> <name> — rewrite the name's entry in the tengine
# container's /etc/hosts. It is a bind mount, so the file is overwritten in
# place (sed -i's rename fails with EBUSY).
tengine_hosts_add() {
    podman exec "$(cname tengine)" sh -c \
        "grep -v '$2' /etc/hosts > /tmp/hosts.new; echo '$1 $2' >> /tmp/hosts.new; cat /tmp/hosts.new > /etc/hosts; rm -f /tmp/hosts.new; grep -qx '$1 $2' /etc/hosts"
}

# wait_body <url> <marker> [host] — poll until the response body contains
# marker, sending a Host header when one is given.
wait_body() {
    local url="$1" marker="$2" host="${3:-}" deadline=$((SECONDS + 30))
    while :; do
        if [ -n "$host" ]; then
            [[ "$(curl -s --max-time 3 -H "Host: $host" "$url" 2>/dev/null)" == *"$marker"* ]] && return 0
        else
            [[ "$(curl -s --max-time 3 "$url" 2>/dev/null)" == *"$marker"* ]] && return 0
        fi
        [ "$SECONDS" -ge "$deadline" ] && return 1
        sleep 1
    done
}

stop_upstreams() {
    if [ -n "$UPSTREAM_PIDS" ]; then
        kill $UPSTREAM_PIDS 2>/dev/null || true
    fi
    if [ -n "$UPSTREAM_PORT" ]; then
        pkill -f "http.server $UPSTREAM_PORT" 2>/dev/null || true
    fi
    UPSTREAM_PIDS=""
    UPSTREAM_PORT=""
    return 0
}

cleanup() {
    stop_upstreams
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove safeline --name "$STACK" --clean-data 2>/dev/null || true
    for c in $(podman ps -a --format '{{.Names}}' 2>/dev/null | grep "^$PREFIX-" || true); do
        podman rm -f "$c" 2>/dev/null || true
    done
    podman network rm "$PREFIX" 2>/dev/null || true
    rm -rf "$CONFIG_DIR" "$LOG_DIR"
    rm -rf "$TEST_DIR" 2>/dev/null || true
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli SafeLine WAF Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Stack:     $STACK (prefix $PREFIX)"
    echo "  Console:   https://127.0.0.1:$CONSOLE_PORT"
    echo "  Subnet:    $SUBNET.0/24"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }
    command -v curl   &>/dev/null || { red "curl is not installed"; exit 1; }
    command -v python3 &>/dev/null || { red "python3 is not installed (console API login + upstream servers)"; exit 1; }
    command -v openssl &>/dev/null || { red "openssl is not installed (console API password encryption)"; exit 1; }
    [ "$(uname -s)" = "Linux" ] || { red "SafeLine requires Linux"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR" "$LOG_DIR"
    mkdir -p "$CONFIG_DIR" "$LOG_DIR"
    for c in $(podman ps -a --format '{{.Names}}' 2>/dev/null | grep "^$PREFIX-" || true); do
        podman rm -f "$c" 2>/dev/null || true
    done
    podman network rm "$PREFIX" 2>/dev/null || true
    rm -rf "$TEST_DIR" 2>/dev/null || true
    mkdir -p "$TEST_DIR"
    run_test "config init (isolated ns + base dir)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" --namespace "$NAMESPACE"

    # ---- Rootless refusal ----
    ROOTLESS="$(podman info --format '{{.Host.Security.Rootless}}')"
    if [ "$ROOTLESS" = "true" ]; then
        section "Rootless refusal"
        run_fails "install refused on rootless podman" "requires rootful podman" \
            pg addon install safeline --name "$STACK" --port "$CONSOLE_PORT"
        echo ""
        echo "=========================================="
        echo "  Results: $((TESTS - FAILED))/$TESTS passed"
        echo "  Rootless podman: stack refused as designed; nothing else to run."
        echo "=========================================="
        [ "$FAILED" -eq 0 ] || exit 1
        return 0
    fi

    # ---- Install ----
    section "Install safeline --name $STACK"
    if INSTALL_OUT="$(pg addon install safeline --name "$STACK" \
            --port "$CONSOLE_PORT" --subnet-prefix "$SUBNET" 2>&1 | tee "$LOG_DIR/install.log")"; then
        pass "install safeline --name $STACK"
    else
        fail "install safeline --name $STACK"; echo "$INSTALL_OUT" | sed 's/^/      | /'
        return 1
    fi
    echo "$INSTALL_OUT" | sed 's/^/      /'

    run_test "install summary names the instance" contains "$INSTALL_OUT" 'SafeLine WAF "waf1" installed'
    run_test "install summary prints the console URL" contains "$INSTALL_OUT" "Console:     https://127.0.0.1:$CONSOLE_PORT"
    run_test "install summary prints the captured admin password" contains "$INSTALL_OUT" "Password:    "
    run_test "install summary states the component versions" contains "$INSTALL_OUT" "SafeLine 9.4.7, PostgreSQL 15.18"
    run_test "install summary states subnet and gateway" contains "$INSTALL_OUT" "Subnet:      $SUBNET.0/24 (gateway $SUBNET.1)"
    run_test "containers created in dependency order" create_order_ok "$INSTALL_OUT"

    run_test "all seven containers running" eventually all_running
    run_test "config container_prefix is namespaced" test "$(sf_field "$STACK" container_prefix)" = "$PREFIX"
    run_test "config network_name matches the prefix" test "$(sf_field "$STACK" network_name)" = "$PREFIX"
    run_test "config subnet_prefix stored" test "$(sf_field "$STACK" subnet_prefix)" = "$SUBNET"
    run_test "config console_port stored" test "$(sf_field "$STACK" console_port)" = "$CONSOLE_PORT"
    run_test "config listen defaults to 0.0.0.0" test "$(sf_field "$STACK" listen)" = "0.0.0.0"
    run_test "config data_dir is <base>/addon/safeline/<name>" test "$(sf_field "$STACK" data_dir)" = "$DATA_DIR"

    for r in $ROLES; do
        run_test "$r restart policy is unless-stopped" test "$(restart_policy_of "$r")" = "unless-stopped"
    done

    # ---- Static network contract ----
    section "Static-IP bridge network"
    run_test "network exists" net_exists
    run_test "network subnet is $SUBNET.0/24" net_has_subnet "$SUBNET.0/24"
    run_test "network gateway is $SUBNET.1" net_has_gateway "$SUBNET.1"
    run_test "network carries the pgcli.safeline owner label" net_has_label "$STACK"
    run_test "postgres on static address $SUBNET.2" test "$(ip_of postgres)" = "$SUBNET.2"
    run_test "mgt on static address $SUBNET.4" test "$(ip_of mgt)" = "$SUBNET.4"
    run_test "detector on static address $SUBNET.5" test "$(ip_of detector)" = "$SUBNET.5"
    run_test "luigi on static address $SUBNET.7" test "$(ip_of luigi)" = "$SUBNET.7"
    run_test "fvm on static address $SUBNET.8" test "$(ip_of fvm)" = "$SUBNET.8"
    run_test "chaos on static address $SUBNET.10" test "$(ip_of chaos)" = "$SUBNET.10"
    run_test "bridge roles are attached to the stack network" eventually bridge_roles_attached
    run_test "tengine uses host networking" test "$(podman inspect "$(cname tengine)" --format '{{.HostConfig.NetworkMode}}' 2>/dev/null)" = "host"
    run_test "only mgt publishes 1443 -> $CONSOLE_PORT" mgt_port_published
    run_test "mgt carries the bundled PostgreSQL DSN" env_has mgt "MGT_PG=postgres://safeline-ce:"
    run_test "mgt DSN targets the postgres static address" env_has mgt "@$SUBNET.2/safeline-ce?sslmode=disable"
    run_test "mgt sets SUBNET_PREFIX" env_has mgt "SUBNET_PREFIX=$SUBNET"
    run_test "tengine pulls site config from mgt's static address" env_has tengine "TCD_MGT_API=https://$SUBNET.4:1443/api/open/publish/server"
    run_test "tengine points at the detector static address" env_has tengine "SNSERVER_ADDR=$SUBNET.5:8000"

    # ---- Console and credentials ----
    section "Console and credentials"
    run_test "HTTPS health endpoint answers 200" health_ok
    run_test "bundled PostgreSQL accepts queries" db_ok
    run_test "admin user is admin" test "$(sf_field "$STACK" admin_user)" = "admin"
    run_test "admin password captured from resetadmin" admin_pw_matches_config
    run_test "bundled PostgreSQL password never doubles as the console one" admin_pw_differs_from_db
    run_test "addon password --reset rotates and stores the new password" admin_reset_rotates
    run_test "addon list shows 7/7 running" addon_list_ok
    run_test "addon reload hot-reloads tengine (nginx -t + reload)" reload_ok
    run_test "detector, fvm and luigi share the stored management token" detector_token_wired

    # ---- Protected site traffic ----
    section "Protected site traffic"
    # Drive the console the way its web UI does: log in through the API, create
    # a site, and let tengine proxy the upstream. Two upstream servers answer on
    # the same port from different addresses so the hostname site can be moved
    # by rewriting tengine's /etc/hosts, which is what a DNS change looks like
    # to the resolver nginx uses.
    HOST_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}')"
    [ -n "$HOST_IP" ] || HOST_IP="$(hostname -I | awk '{print $1}')"
    UPSTREAM_PORT="$(free_port)" || { red "no free port for the upstream servers"; return 1; }
    SITE_PORT="$(free_port)"
    while [ "$SITE_PORT" = "$UPSTREAM_PORT" ]; do SITE_PORT="$(free_port)"; done
    DNS_SITE_PORT="$(free_port)"
    while [ "$DNS_SITE_PORT" = "$UPSTREAM_PORT" ] || [ "$DNS_SITE_PORT" = "$SITE_PORT" ]; do DNS_SITE_PORT="$(free_port)"; done

    mkdir -p "$TEST_DIR/upstream-a" "$TEST_DIR/upstream-b"
    printf 'e2e-upstream-a\n' > "$TEST_DIR/upstream-a/index.html"
    printf 'e2e-upstream-b\n' > "$TEST_DIR/upstream-b/index.html"
    ( cd "$TEST_DIR/upstream-a" && exec python3 -m http.server "$UPSTREAM_PORT" --bind "$HOST_IP" ) >"$LOG_DIR/upstream-a.log" 2>&1 &
    UPSTREAM_PIDS="$!"
    ( cd "$TEST_DIR/upstream-b" && exec python3 -m http.server "$UPSTREAM_PORT" --bind 127.0.0.2 ) >"$LOG_DIR/upstream-b.log" 2>&1 &
    UPSTREAM_PIDS="$UPSTREAM_PIDS $!"

    TOKEN=""
    login_ok() { local t; t="$(console_token)"; [ -n "$t" ] || return 1; TOKEN="$t"; }
    run_test "console login via the web API (AES password + csrf)" login_ok
    run_test "upstream servers answer on $HOST_IP:$UPSTREAM_PORT and 127.0.0.2:$UPSTREAM_PORT" \
        bash -c "[[ \"\$(curl -s --max-time 3 http://$HOST_IP:$UPSTREAM_PORT/)\" == *e2e-upstream-a* && \"\$(curl -s --max-time 3 http://127.0.0.2:$UPSTREAM_PORT/)\" == *e2e-upstream-b* ]]"

    SITE_ID=""
    create_ip_site() {
        local id; id="$(site_create "$TOKEN" "$SITE_PORT" "http://$HOST_IP:$UPSTREAM_PORT")"
        [[ "$id" =~ ^[0-9]+$ ]] || { echo "create failed: $id" >&2; return 1; }
        SITE_ID="$id"
    }
    run_test "site with an IP upstream created through the console API" create_ip_site
    run_test "tengine proxies the new site on its entry port" wait_body "http://127.0.0.1:$SITE_PORT/" "e2e-upstream-a"

    DNS_SITE_ID=""
    create_dns_site() {
        tengine_hosts_add "$HOST_IP" sfe2e-upstream.test || return 1
        local id; id="$(site_create "$TOKEN" "$DNS_SITE_PORT" "http://sfe2e-upstream.test:$UPSTREAM_PORT")"
        [[ "$id" =~ ^[0-9]+$ ]] || { echo "create failed: $id" >&2; return 1; }
        DNS_SITE_ID="$id"
    }
    run_test "site with a hostname upstream created through the console API" create_dns_site
    run_test "hostname upstream resolves inside tengine" wait_body "http://127.0.0.1:$DNS_SITE_PORT/" "e2e-upstream-a"

    # A real public domain over TLS, the shape operators use for external
    # sites. The site must own www.baidu.com: the upstream vhost rejects
    # requests whose Host header is the bare entry port.
    BAIDU_SITE_PORT="$(free_port)"
    while [ "$BAIDU_SITE_PORT" = "$UPSTREAM_PORT" ] || [ "$BAIDU_SITE_PORT" = "$SITE_PORT" ] || [ "$BAIDU_SITE_PORT" = "$DNS_SITE_PORT" ]; do
        BAIDU_SITE_PORT="$(free_port)"
    done
    BAIDU_SITE_ID=""
    if curl -sk -o /dev/null --max-time 8 https://www.baidu.com; then
        create_baidu_site() {
            local id; id="$(site_create "$TOKEN" "$BAIDU_SITE_PORT" "https://www.baidu.com" www.baidu.com)" || return 1
            BAIDU_SITE_ID="$id"
        }
        run_test "site with a public HTTPS upstream created through the console API" create_baidu_site
        run_test "tengine proxies www.baidu.com over TLS on its entry port" \
            wait_body "http://127.0.0.1:$BAIDU_SITE_PORT/" "百度一下" www.baidu.com
    else
        yellow "  [SKIP] public HTTPS upstream (www.baidu.com unreachable from this host)"
    fi

    dns_move_cached() {
        tengine_hosts_add 127.0.0.2 sfe2e-upstream.test || return 1
        # nginx resolves upstream host names when its config loads, so the old
        # address keeps serving until the config is reloaded.
        [[ "$(curl -s --max-time 5 "http://127.0.0.1:$DNS_SITE_PORT/")" == *e2e-upstream-a* ]]
    }
    run_test "moved hostname keeps the old upstream until reload" dns_move_cached
    dns_reload() {
        local out
        out="$(pg addon reload safeline --name "$STACK" 2>&1)" || { echo "$out" >&2; return 1; }
        [[ "$out" == *"tengine reloaded"* ]] || { echo "$out" >&2; return 1; }
        wait_body "http://127.0.0.1:$DNS_SITE_PORT/" "e2e-upstream-b"
    }
    run_test "addon reload re-resolves the hostname to the new address" dns_reload

    site_delete "$TOKEN" "$SITE_ID"
    site_delete "$TOKEN" "$DNS_SITE_ID"
    if [ -n "$BAIDU_SITE_ID" ]; then
        site_delete "$TOKEN" "$BAIDU_SITE_ID"
    fi
    sites_gone() {
        ! curl -s --max-time 2 "http://127.0.0.1:$SITE_PORT/" | grep -q e2e-upstream-a &&
            ! curl -s --max-time 2 "http://127.0.0.1:$DNS_SITE_PORT/" | grep -q e2e-upstream-b &&
            ! curl -s --max-time 2 -H "Host: www.baidu.com" "http://127.0.0.1:$BAIDU_SITE_PORT/" | grep -q 百度一下
    }
    run_test "deleted sites stop being served" eventually sites_gone
    stop_upstreams

    # ---- Idempotency and drift ----
    section "Idempotency and drift"
    MGTCID_BEFORE="$(cid_of mgt)"
    if NOOP_OUT="$(pg addon install safeline --name "$STACK" \
            --port "$CONSOLE_PORT" --subnet-prefix "$SUBNET" 2>&1)"; then
        run_test "same-flags re-install is a no-op" contains "$NOOP_OUT" "already installed"
    else
        fail "same-flags re-install"; echo "$NOOP_OUT" | sed 's/^/      | /'
    fi
    run_test "no-op left the mgt container untouched" test "$(cid_of mgt)" = "$MGTCID_BEFORE"
    run_fails "stored --port drift is refused without --force" "--force" \
        pg addon install safeline --name "$STACK" --port "$((CONSOLE_PORT + 1))"
    run_fails "stored --version drift is refused without --force" "differs from the stored value" \
        pg addon install safeline --name "$STACK" --version 9.4.8
    run_fails "duplicate console port is rejected" "already used by" \
        pg addon install safeline --name waf2 --subnet-prefix 172.29.230 --port "$CONSOLE_PORT"
    run_fails "duplicate subnet is rejected" "needs its own subnet" \
        pg addon install safeline --name waf2 --subnet-prefix "$SUBNET" --port "$((CONSOLE_PORT + 1))"
    run_fails "malformed --subnet-prefix is rejected" "invalid --subnet-prefix" \
        pg addon install safeline --name waf2 --subnet-prefix 172.29
    run_fails "reload rejects addons without a reloadable proxy" "only supported for safeline" \
        pg addon reload redis --name cache
    run_fails "flags owned by other addons are rejected" "not supported for SafeLine" \
        pg addon install safeline --name waf2 --password secret

    # ---- --force rebuild ----
    section "Force rebuild"
    PW_BEFORE_FORCE="$(sf_field "$STACK" admin_password)"
    if FORCE_OUT="$(pg addon install safeline --name "$STACK" --force \
            --port "$CONSOLE_PORT" --subnet-prefix "$SUBNET" 2>&1 | tee "$LOG_DIR/force.log")"; then
        pass "install --force recreates the stack"
    else
        fail "install --force"; echo "$FORCE_OUT" | sed 's/^/      | /'
        return 1
    fi
    echo "$FORCE_OUT" | sed 's/^/      /'
    run_test "force rebuilt all seven containers" eventually all_running
    run_test "force replaced the mgt container" test "$(cid_of mgt)" != "$MGTCID_BEFORE"
    run_test "force keeps the detector management token" detector_token_wired
    run_test "force kept the bundled PostgreSQL data" pg_data_present
    run_test "force rotated the console password" test "$(sf_field "$STACK" admin_password)" != "$PW_BEFORE_FORCE"
    run_test "rotated password is what addon password prints" admin_pw_matches_config
    run_test "console healthy after the rebuild" health_ok

    # ---- Stop / start ----
    section "Stop and start"
    PW_BEFORE_STOP="$(sf_field "$STACK" admin_password)"
    run_grep "stop takes the whole stack down" "stopped" pg addon stop safeline --name "$STACK"
    run_test "no role is running after stop" eventually none_running
    run_test "console is unreachable after stop" health_down
    run_fails "reload fails while the stack is stopped" "reloading tengine" \
        pg addon reload safeline --name "$STACK"
    run_grep "start brings the stack back" "started" pg addon start safeline --name "$STACK"
    run_test "all seven roles running after start" eventually all_running
    run_test "console healthy after start" health_ok
    run_test "start did not re-run resetadmin" test "$(sf_field "$STACK" admin_password)" = "$PW_BEFORE_STOP"

    # ---- Logs ----
    section "Logs"
    run_test "default log stream (mgt) is non-empty" logs_nonempty
    run_test "--service tengine is non-empty" logs_nonempty --service tengine
    run_fails "unknown --service role is refused" "must be one of" \
        pg logs addon safeline --name "$STACK" --service nope

    # ---- Autostart flag ----
    section "Autostart flag"
    # backup.autostart is true in every default config, so the flag must be
    # checked inside the safeline block rather than by a whole-file grep.
    run_test "autostart enable --safeline sets the flag" autostart_safeline enable true
    run_test "autostart disable --safeline clears the flag" autostart_safeline disable false

    # ---- Remove keeping data, then reinstall ----
    section "Remove (keep data) and reinstall"
    run_grep "remove keeps the data directory by default" "kept" \
        pg addon remove safeline --name "$STACK"
    run_test "all containers gone" eventually all_absent
    run_test "dedicated network removed" bash -c "! podman network ls --format '{{.Name}}' | grep -qx '$PREFIX'"
    run_test "data directory survived the remove" pg_data_present
    run_test "config entry removed" bash -c "! grep -q '^        $STACK:' '$CONFIG_FILE'"
    run_fails "addon password no longer resolves" "" pg addon password safeline --name "$STACK"

    section "Reinstall on the kept data"
    if REINSTALL_OUT="$(pg addon install safeline --name "$STACK" \
            --port "$CONSOLE_PORT" --subnet-prefix "$SUBNET" 2>&1 | tee "$LOG_DIR/reinstall.log")"; then
        pass "reinstall resumes on the kept data"
    else
        fail "reinstall"; echo "$REINSTALL_OUT" | sed 's/^/      | /'
        return 1
    fi
    run_test "reinstall brought all seven containers up" eventually all_running
    run_test "reinstall wired the detector management token" detector_token_wired
    run_test "console healthy after reinstall" health_ok
    run_test "reinstall captured a console password" admin_pw_matches_config

    # ---- Remove with --clean-data ----
    section "Remove --clean-data"
    run_grep "clean-data remove reports the cleaned dir" "cleaned" \
        pg addon remove safeline --name "$STACK" --clean-data
    run_test "all containers gone again" eventually all_absent
    run_test "network gone again" bash -c "! podman network ls --format '{{.Name}}' | grep -qx '$PREFIX'"
    run_test "data directory deleted" test ! -e "$DATA_DIR"

    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then
        green "  Results: $TESTS/$TESTS passed"
    else
        red "  Results: $((TESTS - FAILED))/$TESTS passed, $FAILED failed"
    fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ]
}

main "$@"
