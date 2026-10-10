#!/usr/bin/env bash
# pgcli pgAdmin 4 addon end-to-end test.
#
# pgAdmin is a web UI, so the assertions are HTTP-shaped rather than SQL-shaped,
# and the script concentrates on what is UNIQUE to it:
#
#   1. THE IMAGE SELF-DROPS PRIVILEGES — pgcli passes `--user 0` and never chowns
#      on the host; the container's own entrypoint chowns /var/lib/pgadmin to
#      5050 and su-execs gunicorn down. Proven by the in-container owner being
#      5050 while the HOST-side data dir is still owned by the test user (or the
#      rootless mapping), i.e. pgcli authored no host chown.
#   2. THE WEB LOGIN CREDENTIALS — always generated, always passed as
#      PGADMIN_DEFAULT_EMAIL / PGADMIN_DEFAULT_PASSWORD, retrievable later with
#      `pg addon password pgadmin`, and never a PostgreSQL password.
#   3. THE LOGIN PAGE IS UP — / answers 302 to the login page and /login answers
#      200, with PGADMIN_LISTEN_PORT pinned to the assigned host port (the image
#      would otherwise bind 80/8080).
#   4. servers.json SEEDING (--dsn / --pg-name) — a one-time install-time seed.
#      servers.json itself never carries a password (pgAdmin cannot import
#      one); the DSN's password goes into a companion pgpass file referenced
#      from it via ConnectionParameters.passfile, so the seeded server connects
#      without a prompt. Mounted read-only with
#      PGADMIN_REPLACE_SERVERS_ON_STARTUP=True so a re-point is declarative.
#   5. DATA PERSISTENCE + --clean-data — the config/session DB survives remove,
#      and under rootless the mapped-uid files are reclaimed only through pgcli's
#      `podman unshare rm` fallback (proved by a control rm that must FAIL).
#
# Usage:
#   bash test/addon/test_pgadmin.sh                  # full test, cleans up
#   bash test/addon/test_pgadmin.sh --skip-destroy    # keep everything after
#   PG_BINARY=/path/to/pg bash test/addon/test_pgadmin.sh
#   PGCLI_NAMESPACE=pae2e PGCLI_PGADMIN_START_PORT=38500 bash test/addon/test_pgadmin.sh
#
# Needs docker.io access (or a pre-loaded dpage/pgadmin4 image) — it is pull-only,
# pgcli never builds it. Run on Linux; the macOS bridge path is the same code as
# redis's and is covered by the unit tests.
set -euo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
TEST_DIR="${PGCLI_TEST_DIR:-/tmp/pgcli-pgadmin-e2e}"
CONFIG_DIR="${PGCLI_CONFIG_DIR:-/tmp/pgcli-pgadmin-e2e-config}"
CONFIG_FILE="$CONFIG_DIR/pg.yaml"
NAMESPACE="${PGCLI_NAMESPACE:-pae2e}"
PGADMIN_START_PORT="${PGCLI_PGADMIN_START_PORT:-38500}"
PG_START_PORT="${PGCLI_PG_START_PORT:-38400}"
PG_SSH_PORT="${PGCLI_SSH_START_PORT:-43400}"

UI="ui"                 # the plain instance (no seed)
SEED="seeded"          # installed with --dsn (remote-style seed)
INSTANCE="pgadmin-db"  # backing PG instance, for the --pg-name seed
# A throwaway fixture DSN: host/port/db only, and the password below exists
# solely to prove it lands in the pgpass file but NOT in servers.json.
FAKE_DSN="postgres://seeduser:seedpw@127.0.0.1:55999/seeddb"
EMAIL_OVERRIDE="dba@example.com"

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

# pgadmin_field <name> <key> — value under addons.pgadmin.<name> (8-space key).
# One layer of quotes is stripped: yaml.v3 quotes scalars that would otherwise
# parse as another type.
pgadmin_field() {
    awk -v n="        $1:" -v k="$2:" '
        $0 == n {f=1; next}
        f && /^        [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {
            gsub(/^[[:space:]]*[a-z_]+: /,""); gsub(/^"|"$/,""); print; exit
        }
    ' "$CONFIG_FILE"
}

# inst_field <name> <key> — value under the top-level instances.<name> block
# (4-space instance key). Same quote-stripping as pgadmin_field.
inst_field() {
    awk -v n="    $1:" -v k="$2:" '
        $0 == n {f=1; next}
        f && /^    [^ ]/ {f=0}
        f && $0 ~ ("^[[:space:]]*" k) {
            gsub(/^[[:space:]]*[a-z_]+: /,""); gsub(/^"|"$/,""); print; exit
        }
    ' "$CONFIG_FILE"
}

container_of() { pgadmin_field "$1" container_name; }

pa_up() {
    podman ps --filter "name=pgcli-pgadmin-$NAMESPACE-$1" --filter status=running \
        --format '{{.Names}}' | grep -q "pgcli-pgadmin-$NAMESPACE-$1"
}
pa_gone() {
    ! podman ps -a --filter "name=pgcli-pgadmin-$NAMESPACE-$1" --format '{{.Names}}' | grep -q .
}

# env_of <name> — the container's recorded environment, one KEY=VALUE per line.
env_of() {
    podman inspect "pgcli-pgadmin-$NAMESPACE-$1" \
        --format '{{range .Config.Env}}{{println .}}{{end}}'
}
# Capture before matching: `env_of | grep -q` is a pipefail trap — grep exits on
# the first match, podman takes SIGPIPE, and the pipeline reports failure even
# though the pattern was there (it looked like pgcli not passing the env).
env_has() { local out; out="$(env_of "$1")"; [[ "$out" == *"$2"* ]]; }
env_lacks() { local out; out="$(env_of "$1")"; [[ "$out" != *"$2"* ]]; }

# mounts_of <name> — the container's bind mount sources and destinations.
mounts_of() {
    podman inspect "pgcli-pgadmin-$NAMESPACE-$1" \
        --format '{{range .Mounts}}{{println .Source .Destination}}{{end}}'
}
# read_only_mount <name> <destination> — that mount exists AND is read-only.
# Podman's `.Mounts` JSON spells the flag `"RW":false` (capital RW), so match
# it exactly; the per-mount objects are split one per line first so the RW flag
# is tested against the right destination, not any other mount on the row.
read_only_mount() {
    podman inspect "pgcli-pgadmin-$NAMESPACE-$1" --format '{{json .Mounts}}' \
        | tr '{' '\n' | grep -F "\"Destination\":\"$2\"" | grep -q '"RW":false'
}

data_dir_of() { echo "$TEST_DIR/addon/pgadmin/$1/data"; }
servers_json_of() { echo "$TEST_DIR/addon/pgadmin/$1/servers.json"; }
pgpass_of() { echo "$TEST_DIR/addon/pgadmin/$1/pgpass"; }

# http_code <port> [path] [flag...] — status code of a request (no -f: pgAdmin
# answers 3xx/4xx deliberately).
http_code() {
    local port="$1" path="${2:-/}"; shift 2 || true
    curl -s -o /dev/null -w '%{http_code}' "$@" "http://127.0.0.1:$port$path" 2>/dev/null
}

# wait_http <port> [tries] — poll until the web UI answers anything at all
# (first boot runs the sqlite migrations before gunicorn binds).
wait_http() {
    local port="$1" tries="${2:-60}" code
    for _ in $(seq 1 "$tries"); do
        code="$(http_code "$port" / || true)"
        case "$code" in
            2*|3*|4*) return 0 ;;
        esac
        sleep 1
    done
    return 1
}

# redirect_to <port> — the Location of the / response (pgAdmin bounces
# unauthenticated visitors to the login page).
redirect_to() {
    curl -s -o /dev/null -D - "http://127.0.0.1:$1/" 2>/dev/null \
        | tr -d '\r' | awk -F': ' '/^[Ll]ocation:/{print $2; exit}'
}

# owner_in <name> <path> — uid:gid of a path as seen INSIDE the container.
owner_in() {
    podman exec "pgcli-pgadmin-$NAMESPACE-$1" stat -c '%u:%g' "$2" 2>/dev/null
}

# readable_in <name> <path> — the (mapped) container user can read the file.
readable_in() {
    podman exec "pgcli-pgadmin-$NAMESPACE-$1" test -r "$2" 2>/dev/null
}

# mode_in <name> <path> — permission bits of a path as seen INSIDE the container.
mode_in() {
    podman exec "pgcli-pgadmin-$NAMESPACE-$1" stat -c '%a' "$2" 2>/dev/null
}

# seed_connects <name> <host> <port> <user> <db> — libpq inside the pgAdmin
# container authenticates through the mounted passfile with NO password in the
# command: the seeded server must open without the first-connect prompt. Two
# details make this a real proof rather than a file check:
#   - it runs as uid 5050 (the entrypoint chowns the passfile to that uid and
#     gunicorn runs as it), because libpq IGNORES a passfile not owned by the
#     effective user;
#   - it uses the image's own Python + psycopg, i.e. exactly the stack pgAdmin
#     connects with (the image ships no psql).
seed_connects() {
    podman exec --user 5050 "pgcli-pgadmin-$NAMESPACE-$1" /venv/bin/python -c "
import psycopg
conn = psycopg.connect('host=$2 port=$3 user=$4 dbname=$5 passfile=/var/lib/pgadmin/pgpass')
print(conn.execute('select 1').fetchone()[0])
" 2>/dev/null | grep -q '^1$'
}

# restart_count <name> — how often podman has restarted the container. pgAdmin's
# entrypoint EXITs on invalid env (e.g. an email with a reserved TLD), so under
# the unless-stopped policy a bad config shows up as a climbing count — a far
# more direct signal than waiting on HTTP and timing out.
restart_count() {
    podman inspect "pgcli-pgadmin-$NAMESPACE-$1" --format '{{.RestartCount}}' 2>/dev/null
}

# pgadmin_db_present <name> — pgAdmin's config/session DB exists in the bind
# mount. Rootless writes it as the mapped uid, so try the host view first and
# fall back through the user namespace.
pgadmin_db_present() {
    local d="$(data_dir_of "$1")"
    test -f "$d/pgadmin4.db" || podman unshare test -f "$d/pgadmin4.db" 2>/dev/null
}

# seed_field <name> <json-key> — read one field of the seeded servers.json.
seed_field() {
    local file="$(servers_json_of "$1")"
    [ -f "$file" ] || return 1
    awk -v k="\"$2\":" 'index($0,k){sub(/^[^:]*: */,""); gsub(/[",]/,""); print; exit}' "$file"
}

# Helpers referenced BY NAME inside `bash -c "..."` run_test bodies execute in a
# child shell, so they must be exported — and `export -f` errors (under set -e,
# aborting the script) for anything not yet defined, so this list sits after
# every one of them. NAMESPACE/TEST_DIR are read by the exported helpers too.
export -f wait_http http_code redirect_to owner_in readable_in env_of env_has \
    mounts_of pa_up pgadmin_db_present seed_field data_dir_of servers_json_of \
    restart_count mode_in pgpass_of seed_connects
export NAMESPACE TEST_DIR

cleanup() {
    if [ "$SKIP_DESTROY" = true ]; then
        yellow "Skipping cleanup (--skip-destroy)"; return
    fi
    section "Cleanup"
    pg addon remove pgadmin --name "$SEED"   --clean-data 2>/dev/null || true
    pg addon remove pgadmin --name "$UI"     --clean-data 2>/dev/null || true
    pg stop -i "$INSTANCE" 2>/dev/null || true
    pg destroy -i "$INSTANCE" --force 2>/dev/null || true
    rm -rf "$CONFIG_DIR"
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    green "  Cleanup done"
}
trap cleanup EXIT

main() {
    echo "=========================================="
    echo "  pgcli pgAdmin Addon Test"
    echo "=========================================="
    echo "  Binary:    $BINARY"
    echo "  Test dir:  $TEST_DIR"
    echo "  Config:    $CONFIG_FILE"
    echo "  Namespace: $NAMESPACE"
    echo "  Ports:     pgadmin pool $PGADMIN_START_PORT+ / PG $PG_START_PORT+ / SSH $PG_SSH_PORT+"
    echo "=========================================="

    [ -x "$BINARY" ] || { red "Binary not found: $BINARY (run 'make build' first)"; exit 1; }
    command -v podman &>/dev/null || { red "podman is not installed"; exit 1; }
    command -v curl   &>/dev/null || { red "curl is not installed"; exit 1; }

    # ---- Setup ----
    section "Setup"
    rm -rf "$CONFIG_DIR"
    for c in $(podman ps -a --filter "name=pgcli-pgadmin-$NAMESPACE-" \
                    --format "{{.Names}}" 2>/dev/null); do
        podman rm -f "$c" 2>/dev/null || true
    done
    [ -d "$TEST_DIR" ] && { podman unshare rm -rf "$TEST_DIR" 2>/dev/null || rm -rf "$TEST_DIR" 2>/dev/null || true; }
    mkdir -p "$CONFIG_DIR" "$TEST_DIR"

    run_test "config init (isolated ns + port ranges)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$TEST_DIR" \
        --namespace "$NAMESPACE" --pg-start-port "$PG_START_PORT" \
        --pg-ssh-port "$PG_SSH_PORT" --add "$INSTANCE"
    sed -i "s/^pgadmin_start_port:.*/pgadmin_start_port: $PGADMIN_START_PORT/" "$CONFIG_FILE"
    run_grep "pgadmin_start_port applied" "pgadmin_start_port: $PGADMIN_START_PORT" \
        cat "$CONFIG_FILE"

    # ---- Install ----
    section "Install pgadmin --name $UI"
    TESTS=$((TESTS + 1))
    if INSTALL_OUT="$(pg addon install pgadmin --name "$UI" --email "$EMAIL_OVERRIDE" 2>&1)"; then
        pass "install pgadmin --name $UI --email $EMAIL_OVERRIDE"
    else
        fail "install pgadmin --name $UI"; echo "$INSTALL_OUT" | sed 's/^/      | /'
    fi
    echo "$INSTALL_OUT" | sed 's/^/      /'

    PORT="$(pgadmin_field "$UI" host_port)"
    PW="$(pgadmin_field "$UI" password)"
    TAG="$(pgadmin_field "$UI" image_tag)"
    CONTAINER="$(pgadmin_field "$UI" container_name)"
    LISTEN="$(pgadmin_field "$UI" listen)"

    run_test "container running" pa_up "$UI"
    # No crash-loop: pgAdmin's entrypoint exits on invalid env (e.g. a reserved
    # TLD in the email) and the restart policy would silently spin it.
    run_test "container has not restarted (env accepted on first boot)" \
        bash -c "[ \"\$(restart_count '$UI')\" = '0' ]"
    run_test "port drawn from the pgadmin pool" test "$PORT" -ge "$PGADMIN_START_PORT"
    run_test "loopback by default" test "$LISTEN" = "127.0.0.1"
    run_test "upstream image pinned" \
        test "$TAG" = "docker.io/dpage/pgadmin4:9.18"
    run_test "container name follows the namespace convention" \
        test "$CONTAINER" = "pgcli-pgadmin-$NAMESPACE-$UI"
    run_test "login password generated (20 chars)" test "${#PW}" -ge 16
    run_grep "summary prints the URL" "http://127.0.0.1:$PORT/" echo "$INSTALL_OUT"
    run_grep "summary prints the login email" "Login email:    $EMAIL_OVERRIDE" echo "$INSTALL_OUT"
    run_grep "summary prints the login password" "Login password: $PW" echo "$INSTALL_OUT"
    run_grep "summary flags it as a WEB login, not a PG password" \
        "Web login only" echo "$INSTALL_OUT"
    run_grep "summary points at pg addon password for retrieval" \
        "pg addon password pgadmin" echo "$INSTALL_OUT"
    # No seed given: nothing about servers.json may be claimed.
    run_not_grep "no seed line without --dsn/--pg-name" "Seeded:" echo "$INSTALL_OUT"

    # ---- Web login credentials reach the container ----
    section "Env mapping (podman inspect)"
    run_test "PGADMIN_DEFAULT_EMAIL from --email" \
        env_has "$UI" "PGADMIN_DEFAULT_EMAIL=$EMAIL_OVERRIDE"
    run_test "PGADMIN_DEFAULT_PASSWORD is the generated one" \
        env_has "$UI" "PGADMIN_DEFAULT_PASSWORD=$PW"
    run_test "PGADMIN_LISTEN_PORT pinned to the assigned port" \
        env_has "$UI" "PGADMIN_LISTEN_PORT=$PORT"
    run_test "PGADMIN_LISTEN_ADDRESS is the loopback bind" \
        env_has "$UI" "PGADMIN_LISTEN_ADDRESS=127.0.0.1"
    run_test "the container's postfix is disabled" \
        env_has "$UI" "PGADMIN_DISABLE_POSTFIX=1"
    run_test "no seed env without a DSN" \
        env_lacks "$UI" "PGADMIN_REPLACE_SERVERS_ON_STARTUP="
    run_test "loopback bind is not widened on Linux host networking" \
        env_lacks "$UI" "PGADMIN_LISTEN_ADDRESS=0.0.0.0"

    # ---- Ownership: the IMAGE drops privileges, pgcli never chowns ----
    section "Privileges (image self-drop, not a host chown)"
    run_test "the container runs as root so the entrypoint can chown + su-exec" \
        bash -c "[ \"\$(podman inspect 'pgcli-pgadmin-$NAMESPACE-$UI' --format '{{.Config.User}}')\" = '0' ]"
    run_test "data dir created on the host" test -d "$(data_dir_of "$UI")"
    # gunicorn has to be up (and its chown done) before this reads meaningfully,
    # so wait on HTTP first.
    run_test "web UI answers" wait_http "$PORT"
    run_test "the data dir is pgadmin's own 5050:0 inside the container" \
        bash -c "[ \"\$(owner_in '$UI' /var/lib/pgadmin)\" = '5050:0' ]"
    # The honest reading: the HOST dir is still owned by whoever ran the test (or
    # its rootless mapping), i.e. pgcli authored no host-side chown. On a rootful
    # host it is the test invoker either way, so only assert the non-root case.
    if [ "$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null)" = "true" ]; then
        TESTS=$((TESTS + 1))
        HOSTOWN="$(stat -c '%u' "$(data_dir_of "$UI")" 2>/dev/null || echo unknown)"
        if [ "$HOSTOWN" = "$(id -u)" ]; then
            pass "host data dir is still owned by the invoking uid $HOSTOWN (no pgcli host chown)"
        else
            fail "host data dir owner is $HOSTOWN, want the invoking uid $(id -u)"
        fi
    else
        yellow "  (rootful podman — skipping the host-side ownership control)"
    fi

    # ---- HTTP surface ----
    section "Web UI"
    run_test "/ redirects (pgAdmin bounces to the login page)" \
        bash -c "case \$(http_code '$PORT' /) in 3*) exit 0;; *) exit 1;; esac"
    run_test "the redirect targets the login page" \
        bash -c "redirect_to '$PORT' | grep -qi 'login'"
    run_test "/login answers 200" bash -c "[ \$(http_code '$PORT' /login) = 200 ]"
    run_test "/login serves the sign-in form" \
        bash -c "curl -sf 'http://127.0.0.1:$PORT/login' | grep -qi 'password'"
    # (No assertion that --email appears in the login HTML: /login is a React
    # shell — the served body is just `<div id="root">`, so pgAdmin's own
    # configured account is not visible to curl. Its presence is proven by the
    # PGADMIN_DEFAULT_EMAIL env check above.)
    run_test "pgAdmin initialised its session DB in the bind mount" \
        pgadmin_db_present "$UI"
    run_test "the data dir bind mount is the pgAdmin store" \
        bash -c "mounts_of '$UI' | grep -q ' /var/lib/pgadmin\$'"

    # ---- password retrieval surface ----
    section "pg addon password pgadmin"
    TESTS=$((TESTS + 1))
    if [ "$(pg addon password pgadmin --name "$UI" 2>/dev/null)" = "$PW" ]; then
        pass "addon password pgadmin prints the stored web login password"
    else
        fail "addon password pgadmin printed '$(pg addon password pgadmin --name "$UI" 2>/dev/null)', want the stored one"
    fi
    PWFILE="$CONFIG_DIR/pw.txt"
    run_test "password --file writes it out" \
        pg addon password pgadmin --name "$UI" --file "$PWFILE"
    TESTS=$((TESTS + 1))
    if [ "$(tr -d '\n' < "$PWFILE")" = "$PW" ]; then
        pass "--file content is the stored password"
    else
        fail "--file content mismatch: got '$(cat "$PWFILE")'"
    fi
    run_test "--file is mode 0600" \
        bash -c "[ \"\$(stat -c '%a' '$PWFILE')\" = '600' ]"
    rm -f "$PWFILE"


    # ---- list ----
    section "List"
    run_grep "addon list shows the pgadmin instance" "$UI" pg addon list
    run_grep "addon list prints the URL" "http://127.0.0.1:$PORT/" pg addon list
    run_grep "addon list prints the login email" "$EMAIL_OVERRIDE" pg addon list
    run_test "addon list --show-password prints the login password" \
        bash -c "'$BINARY' -c '$CONFIG_FILE' addon list --show-password 2>&1 | grep -qF '$PW'"
    run_test "addon list without --show-password hides it" \
        bash -c "! '$BINARY' -c '$CONFIG_FILE' addon list 2>&1 | grep -qF '$PW'"

    # ---- stop / start ----
    section "Stop / start"
    STOP_OUT="$(pg addon stop pgadmin --name "$UI" 2>&1)"
    echo "$STOP_OUT" | sed 's/^/      /'
    run_grep "stop prints a confirmation" "stopped" echo "$STOP_OUT"
    run_test "container stopped" bash -c "! pa_up '$UI'"
    run_test "start pgadmin" pg addon start pgadmin --name "$UI"
    run_test "container running again" pa_up "$UI"
    run_test "UI answers after restart" wait_http "$PORT"

    # ---- Idempotent re-install (port + password stable, container reused) ----
    section "Re-install (idempotent)"
    UID_BEFORE="$(podman inspect "pgcli-pgadmin-$NAMESPACE-$UI" --format '{{.Id}}')"
    run_test "install again (reuse, no error)" \
        pg addon install pgadmin --name "$UI"
    PORT2="$(pgadmin_field "$UI" host_port)"
    PW2="$(pgadmin_field "$UI" password)"
    if [ "$PORT" = "$PORT2" ]; then pass "port stable across re-install ($PORT)"
    else fail "port changed on re-install: $PORT -> $PORT2"; fi
    # The stored password must survive too: pgAdmin only applies
    # PGADMIN_DEFAULT_PASSWORD while initialising an EMPTY data dir, so a
    # regenerated value would silently mismatch the pgadmin4.db left behind.
    if [ "$PW" = "$PW2" ]; then pass "login password stable across re-install"
    else fail "login password changed on re-install (would not apply to the kept data dir)"; fi
    if [ "$UID_BEFORE" = "$(podman inspect "pgcli-pgadmin-$NAMESPACE-$UI" --format '{{.Id}}')" ]; then
        pass "container reused, not recreated"
    else
        fail "container was recreated without --force"
    fi

    # ---- --force recreates and applies a changed email ----
    section "Re-install --force (changed listen/email takes effect)"
    run_test "install --force --email dba2@example.com" \
        pg addon install pgadmin --name "$UI" --email dba2@example.com --force
    run_test "the new email reached the container" \
        env_has "$UI" "PGADMIN_DEFAULT_EMAIL=dba2@example.com"
    run_test "container recreated" \
        bash -c "[ \"\$(podman inspect 'pgcli-pgadmin-$NAMESPACE-$UI' --format '{{.Id}}')\" != '$UID_BEFORE' ]"

    # ---- servers.json seed via --dsn (remote-style) ----
    section "Seed via --dsn"
    run_fails "--dsn and --pg-name are mutually exclusive" "mutually exclusive" \
        pg addon install pgadmin --name badseed --dsn "$FAKE_DSN" --pg-name "$INSTANCE"
    run_fails "unknown --pg-name is refused" "not found" \
        pg addon install pgadmin --name badseed --pg-name nonexistent

    SEED_OUT=""
    TESTS=$((TESTS + 1))
    if SEED_OUT="$(pg addon install pgadmin --name "$SEED" --dsn "$FAKE_DSN" 2>&1)"; then
        pass "install pgadmin --name $SEED --dsn <uri>"
    else
        fail "install pgadmin --name $SEED --dsn"; echo "$SEED_OUT" | sed 's/^/      | /'
    fi
    echo "$SEED_OUT" | sed 's/^/      /'
    SEED_PORT="$(pgadmin_field "$SEED" host_port)"
    run_test "seeded container running" pa_up "$SEED"
    # The seeded instance is the DEFAULT-email path — the one that crash-loops if
    # pgcli picks a reserved TLD. RestartCount 0 is the direct proof the default
    # email was accepted (the seeded UI-answers test below would only time out).
    run_test "seeded container has not restarted (default email accepted)" \
        bash -c "[ \"\$(restart_count '$SEED')\" = '0' ]"
    run_test "seeded instance got its own port" \
        test "$SEED_PORT" != "$PORT"
    run_grep "summary reports the seed" "Seeded:" echo "$SEED_OUT"
    run_not_grep "summary never echoes the seed DSN" "seedpw" echo "$SEED_OUT"

    run_test "servers.json written on the host" test -f "$(servers_json_of "$SEED")"
    run_test "the seeded server has the DSN host" \
        bash -c "[ \"\$(seed_field '$SEED' Host)\" = '127.0.0.1' ]"
    run_test "the seeded server has the DSN port" \
        bash -c "[ \"\$(seed_field '$SEED' Port)\" = '55999' ]"
    run_test "the seeded server has the DSN database" \
        bash -c "[ \"\$(seed_field '$SEED' MaintenanceDB)\" = 'seeddb' ]"
    run_test "the seeded server has the DSN user" \
        bash -c "[ \"\$(seed_field '$SEED' Username)\" = 'seeduser' ]"
    # The load-bearing one: pgAdmin cannot import passwords, so the DSN's must be
    # dropped — this file is bind-mounted into a container.
    run_test "servers.json carries NO password" \
        bash -c "! grep -q 'seedpw' '$(servers_json_of "$SEED")'"
    run_test "sslmode is registered" \
        bash -c "grep -q '\"sslmode\": \"prefer\"' '$(servers_json_of "$SEED")'"
    run_test "servers.json is mounted into the container" \
        bash -c "mounts_of '$SEED' | grep -q ' /pgadmin4/servers.json\$'"
    run_test "the mount is read-only" \
        read_only_mount "$SEED" /pgadmin4/servers.json
    run_test "the seeded file is readable by the container's 5050 user" \
        readable_in "$SEED" /pgadmin4/servers.json
    run_test "REPLACE_SERVERS is set so the seed is declarative" \
        env_has "$SEED" "PGADMIN_REPLACE_SERVERS_ON_STARTUP=True"
    run_test "seeded UI answers" wait_http "$SEED_PORT"
    run_test "seeded UI is up without re-rendering anything" \
        bash -c "podman exec 'pgcli-pgadmin-$NAMESPACE-$SEED' grep -q seeddb /pgadmin4/servers.json"

    # ---- the seed password goes into a pgpass file, not servers.json ----
    # servers.json cannot carry a password ("Password fields cannot be imported
    # or exported"); the mechanism that removes the first-connect prompt is the
    # server's ConnectionParameters.passfile pointing at a libpq pgpass file.
    section "Seed password via pgpass"
    run_grep "summary reports the password is pre-configured" "password pre-configured" \
        echo "$SEED_OUT"
    run_test "pgpass written on the host" test -f "$(pgpass_of "$SEED")"
    run_test "pgpass holds the DSN password in pgpass format" \
        bash -c "grep -qF '127.0.0.1:55999:*:seeduser:seedpw' '$(pgpass_of "$SEED")'"
    run_test "servers.json references the passfile" \
        bash -c "grep -q '\"passfile\": \"/var/lib/pgadmin/pgpass\"' '$(servers_json_of "$SEED")'"
    run_test "pgpass is mounted into the container" \
        bash -c "mounts_of '$SEED' | grep -q ' /var/lib/pgadmin/pgpass\$'"
    # Deliberately writable: the entrypoint's chown -R must reach it (libpq
    # ignores a passfile not owned by the connecting uid), and chown on a
    # read-only mount fails with EROFS.
    run_test "the pgpass mount is writable (entrypoint chowns it)" \
        bash -c "! read_only_mount '$SEED' /var/lib/pgadmin/pgpass"
    run_test "the in-container pgpass is owned by the pgadmin uid" \
        bash -c "[ \"\$(owner_in '$SEED' /var/lib/pgadmin/pgpass | cut -d: -f1)\" = '5050' ]"
    run_test "the in-container pgpass is mode 600 (libpq rejects looser)" \
        bash -c "[ \"\$(mode_in '$SEED' /var/lib/pgadmin/pgpass)\" = '600' ]"
    run_test "no PGPASSFILE env (passfile is a per-server parameter)" \
        env_lacks "$SEED" "PGPASSFILE="

    # ---- the --pg-name seed path against a real backing instance ----
    section "Seed via --pg-name (backing instance)"
    run_test "start the backing instance" pg start -i "$INSTANCE"
    INST_PORT="$(inst_field "$INSTANCE" host_port)"
    INST_PW="$(inst_field "$INSTANCE" password)"
    run_test "install pgadmin --pg-name $INSTANCE" \
        pg addon install pgadmin --name pgnameseed --pg-name "$INSTANCE"
    run_test "the instance's host became the seeded host" \
        bash -c "grep -q '\"Host\": \"127.0.0.1\"' '$(servers_json_of pgnameseed)'"
    run_test "the instance's port became the seeded port" \
        bash -c "grep -q '\"Port\": $INST_PORT' '$(servers_json_of pgnameseed)'"
    run_test "the instance name became the display name" \
        bash -c "grep -q '\"Name\": \"$INSTANCE\"' '$(servers_json_of pgnameseed)'"
    TESTS=$((TESTS + 1))
    if [ -n "$INST_PW" ] && grep -qF "$INST_PW" "$(servers_json_of pgnameseed)"; then
        fail "the instance password was carried into servers.json"
    else
        pass "the instance password was NOT carried into servers.json"
    fi
    # ...but it must be in the pgpass file — that is what makes the seeded
    # server open without a password prompt.
    TESTS=$((TESTS + 1))
    if [ -n "$INST_PW" ] && grep -qF "$INST_PW" "$(pgpass_of pgnameseed)"; then
        pass "the instance password landed in the pgpass file"
    else
        fail "the instance password is missing from the pgpass file"
    fi
    run_grep "the NOTE explains the password is pre-configured" "password is pre-configured" \
        pg addon install pgadmin --name pgnameseed --pg-name "$INSTANCE" --force
    # The headline proof: libpq inside the pgAdmin container authenticates
    # through the mounted passfile with no password anywhere in the command.
    run_test "seeded server connects with NO password prompt (passfile)" \
        seed_connects pgnameseed 127.0.0.1 "$INST_PORT" admin "$(inst_field "$INSTANCE" database)"
    run_test "remove pgnameseed --clean-data" pg addon remove pgadmin --name pgnameseed --clean-data
    run_test "pgnameseed servers.json gone" test ! -f "$(servers_json_of pgnameseed)"
    run_test "pgnameseed pgpass gone (no stale plaintext secret)" test ! -f "$(pgpass_of pgnameseed)"

    # ---- teardown of the seeded instance ----
    section "Remove the seed instance"
    run_test "remove $SEED" pg addon remove pgadmin --name "$SEED" --clean-data
    run_test "$SEED container gone" pa_gone "$SEED"
    run_test "$SEED data deleted" test ! -d "$(data_dir_of "$SEED")"
    run_test "$SEED servers.json deleted" test ! -f "$(servers_json_of "$SEED")"
    run_test "$SEED pgpass deleted" test ! -f "$(pgpass_of "$SEED")"
    run_test "$SEED parent dir pruned" test ! -d "$TEST_DIR/addon/pgadmin/$SEED"

    # ---- Remove keeps data by default; --clean-data deletes ----
    section "Remove / --clean-data"
    run_test "remove $UI keeps the data dir" pg addon remove pgadmin --name "$UI"
    run_test "$UI container gone" pa_gone "$UI"
    run_test "$UI data kept" test -d "$(data_dir_of "$UI")"
    run_test "$UI config DB survived (sqlite store still there)" \
        pgadmin_db_present "$UI"
    run_not_grep "yaml entry removed" "$UI:" cat "$CONFIG_FILE"

    # Reinstall after a KEEPING remove revives the data dir. Honest expectation:
    # the remove dropped the config entry, so install regenerates a login
    # password — and because pgAdmin only applies PGADMIN_DEFAULT_PASSWORD to an
    # EMPTY data dir, the regenerated password is NOT the working login (the old
    # account in the kept pgadmin4.db still is). install must say so, and the
    # operator's fix is --clean-data for a fresh store (or pin --password to the
    # original when reinstalling).
    REVIVE_PW="$PW"
    TESTS=$((TESTS + 1))
    if REINSTALL_OUT="$(pg addon install pgadmin --name "$UI" 2>&1)"; then
        pass "reinstall $UI revives the kept data dir"
    else
        fail "reinstall $UI"; echo "$REINSTALL_OUT" | sed 's/^/      | /'
    fi
    echo "$REINSTALL_OUT" | sed 's/^/      /'
    run_test "reinstated container running" pa_up "$UI"
    NEW_PW="$(pgadmin_field "$UI" password)"
    if [ "$NEW_PW" = "$REVIVE_PW" ]; then
        fail "reinstall after a keeping-remove reused the config password — expected a regenerated one (the config entry was deleted by remove)"
    else
        pass "reinstall regenerated the login password (config entry was gone)"
    fi
    run_grep "the revive NOTE flags that the printed password is inert on the kept store" \
        "reviving an existing pgAdmin data dir" echo "$REINSTALL_OUT"
    run_test "the revived UI still answers" wait_http "$(pgadmin_field "$UI" host_port)"

    # A reinstall with --password pinned to the ORIGINAL login is the operator's
    # supported way to keep the stored config aligned with the revived account —
    # exercise the pin path (pgAdmin itself still ignores it against a non-empty
    # dir, but pgcli must persist exactly what was asked, not regenerate).
    run_test "reinstall --force --password pins the stored value" \
        pg addon install pgadmin --name "$UI" --force --password "$REVIVE_PW"
    if [ "$(pgadmin_field "$UI" password)" = "$REVIVE_PW" ]; then
        pass "--password is stored verbatim across --force"
    else
        fail "--password was not persisted: got '$(pgadmin_field "$UI" password)'"
    fi

    # The rootless headline: pgAdmin writes its sqlite store as the container's
    # MAPPED 5050 uid, which the host user cannot delete — so the control rm must
    # fail while pgcli's --clean-data succeeds through `podman unshare rm`.
    RD="$(data_dir_of "$UI")"
    pg addon stop pgadmin --name "$UI" >/dev/null 2>&1 || true
    if [ "$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null)" = "true" ]; then
        TESTS=$((TESTS + 1))
        if rm -rf "$RD" 2>/dev/null && [ ! -d "$RD" ]; then
            fail "control rm -rf SUCCEEDED (mapped-uid pgadmin4.db should be undeletable by the host user)"
        else
            pass "control rm -rf FAILED as expected (mapped-uid files undeletable by host user)"
        fi
    else
        yellow "  (rootful podman — skipping the undeletable-file control)"
    fi
    run_test "remove $UI --clean-data" pg addon remove pgadmin --name "$UI" --clean-data
    run_test "$UI data deleted by clean-data" test ! -d "$RD"
    run_test "$UI parent dir pruned" test ! -d "$TEST_DIR/addon/pgadmin/$UI"
    run_not_grep "yaml pgadmin section gone" "pgadmin:" cat "$CONFIG_FILE"

    # ---- Summary ----
    echo ""
    echo "=========================================="
    if [ "$FAILED" -eq 0 ]; then green "  ALL $TESTS TESTS PASSED"
    else red "  $FAILED/$TESTS TESTS FAILED"; fi
    echo "=========================================="
    [ "$FAILED" -eq 0 ] || exit 1
}

main
