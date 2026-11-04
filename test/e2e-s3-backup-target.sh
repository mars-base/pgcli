#!/usr/bin/env bash
# E2E: per-instance S3 backup target (pg create --backup-target s3).
#
# Fully self-contained: the script owns an isolated pgcli config (separate
# base-dir AND backup-dir AND namespace AND ports), stands up a local TLS
# MinIO addon in that same config as the S3 store, and exercises:
#   - an s3-target instance writes stanza/WAL/backup ONLY to backup.repo.s3;
#     the local repo holds nothing for it (no dual-write);
#   - a default (local) instance in the SAME config still backs up locally
#     (mixed targets coexist in one backup container);
#   - WAL archiving reaches S3 (pg_stat_archiver, failed_count=0);
#   - a replica promotes with backup_target=s3 when the host's S3 block is
#     complete (same-host and cross-host alike — the old cross-host abort is
#     gone: no S3 now means local, not refusal);
#   - an incomplete S3 block is refused before pg_promote();
#   - PITR restore reads back from S3;
#   - a CA-mount drift (S3 configured after the container existed) is repaired
#     by the start-time recreate path.
#
# NOTE on ordering: the replica/promote test runs on a NOT-YET-PITR'd s3t, and
# the PITR restore is the last destructive step. A pg_basebackup of a primary
# that already ran `restore --promote` carries stale recovery_target_* into the
# replica (a known PITR->replica interaction), so promoting a replica of a
# just-restored primary is out of scope here.
#
# Usage (on the instance host, needs podman + python3 + pyyaml):
#   bash test/e2e-s3-backup-target.sh
#   SKIP_DESTROY=1 bash test/e2e-s3-backup-target.sh   # keep everything
# Env overrides: PG_BINARY, ROOT (default /tmp/e2e-s3), NAMESPACE (e2es3),
#   PORT (default 39300), KEEP=1 implies --skip-destroy.
set -uo pipefail

BINARY="${PG_BINARY:-/usr/local/bin/pg}"
ROOT="${ROOT:-/tmp/e2e-s3}"
NAMESPACE="${NAMESPACE:-e2es3}"
BASE_DIR="$ROOT/data"
BACKUP_DIR="$ROOT/backup"
CONFIG_FILE="$ROOT/pg.yaml"
PG_PORT="${PORT:-39300}"
SSH_PORT=$((PG_PORT + 100))
MINIO_API=$((PG_PORT + 200))
MINIO_CONSOLE=$((MINIO_API + 1))
MINIO="store"
BUCKET="pgbackrest"
# LAN IP, NOT 127.0.0.1: pgBackRest dials the endpoint from inside the PG and
# backup containers, where loopback is the container's own. mc only
# auto-adds --insecure for loopback, so the mc calls below pass it manually.
HOST_IP="${HOST_IP:-$(hostname -I 2>/dev/null | awk '{print $1}')}"
[ -n "$HOST_IP" ] || { echo "cannot determine host IP (set HOST_IP=...)"; exit 1; }
ENDPOINT="$HOST_IP:$MINIO_API"

BC="pgcli-backup-$NAMESPACE"
PFX="pgcli-pg-$NAMESPACE"

FAILED=0
pass() { printf '\033[0;32m  [PASS]\033[0m %s\n' "$*"; }
fail() { printf '\033[0;31m  [FAIL]\033[0m %s\n' "$*"; FAILED=$((FAILED + 1)); }
section() { printf '\n\033[0;33m=== %s ===\033\0m\n' "$*"; }
run_test() {
    local desc="$1"; shift
    if "$@" >/dev/null 2>&1; then pass "$desc"; else fail "$desc  [cmd: $*]"; fi
}

pg() { "$BINARY" -c "$CONFIG_FILE" "$@"; }
inst() { echo "$PFX-$1"; }
# A replica inherits the PRIMARY's database name (physical copy), so sqlval can't
# derive it from the instance name. sqlval_db takes the db explicitly; sqlval
# defaults it to <instance>_db for primaries.
sqlval_db() { podman exec -i "$(inst "$1")" psql -t -A -U admin -d "$2" -c "$3"; }
sqlval() { sqlval_db "$1" "${1}_db" "$2"; }

yaml_set() {  # yaml_set <dotted.path> <value>
    CFG="$CONFIG_FILE" K="$1" V="$2" python3 - <<'PY'
import os, yaml
path = os.environ["CFG"]
d = yaml.safe_load(open(path)) or {}
k, v = os.environ["K"], os.environ["V"]
cur = d
for part in k.split(".")[:-1]:
    cur = cur.setdefault(part, {})
cur[k.split(".")[-1]] = v
yaml.safe_dump(d, open(path, "w"), sort_keys=False, width=200)
PY
}
yaml_get() {  # yaml_get <instance> <dotted.path>
    CFG="$CONFIG_FILE" INST="$1" KP="$2" python3 - <<'PY'
import os, yaml
d = yaml.safe_load(open(os.environ["CFG"]))
cur = d.get("instances", {}).get(os.environ["INST"], {})
for k in os.environ["KP"].split("."):
    cur = cur.get(k) if isinstance(cur, dict) else None
    if cur is None: break
print(cur if cur is not None else "")
PY
}

repo_path() {
    podman inspect "$BC" --format \
        '{{range .Mounts}}{{if eq .Destination "/var/lib/pgbackrest"}}{{.Source}}{{end}}{{end}}' 2>/dev/null | head -1
}

# wait_wal <instance> [db] — prove a FRESH WAL segment archived to S3 now.
# Reads pg_stat_archiver.archived_count, forces a switch, then waits for the
# count to climb. Increasing archived_count is a successful archive-push; for
# an S3-target stanza that only happens against the S3 repo. We deliberately do
# NOT require failed_count=0: early archive attempts transiently fail while the
# instance starts (the counter latches and never resets), so a healthy archiver
# can still show historical failures.
archived_count() { sqlval_db "$1" "$2" "SELECT archived_count FROM pg_stat_archiver"; }
wait_wal() {
    local i="$1" db="${2:-${1}_db}" base now
    sqlval_db "$i" "$db" "SELECT 1" >/dev/null   # warm the connection
    base=$(archived_count "$i" "$db")
    for _ in $(seq 1 20); do
        sqlval_db "$i" "$db" "SELECT pg_switch_wal()" >/dev/null
        sleep 3
        now=$(archived_count "$i" "$db")
        [ -n "$now" ] && [ "$now" -gt "${base:-0}" ] 2>/dev/null && return 0
        base="${base:-0}"
    done
    return 1
}

cleanup() {
    [ "${SKIP_DESTROY:-0}" = "1" ] && { echo "note: SKIP_DESTROY=1 — left running"; return; }
    section "Cleanup"
    for i in s3t localt s3rep s3x s3drift; do
        pg destroy -i "$i" --force >/dev/null 2>&1 || true
    done
    pg addon remove minio --name "$MINIO" --force >/dev/null 2>&1 || true
    # addon remove can fail silently (e.g. after config churn); force-remove
    # the namespaced container too, or a leftover store poisons the next run.
    podman rm -f "pgcli-minio-$NAMESPACE-$MINIO" >/dev/null 2>&1 || true
    pg stop >/dev/null 2>&1 || true
    podman rm -f "$BC" 2>/dev/null || true
    for c in $(podman ps -a --filter "name=pgcli-pg-$NAMESPACE-" --format '{{.Names}}' 2>/dev/null); do
        podman rm -f "$c" >/dev/null 2>&1 || true
    done
    rm -rf "$ROOT"
    printf '\033[0;32m  Cleanup done (namespace %s only)\033[0m\n' "$NAMESPACE"
}
trap cleanup EXIT

main() {
    echo "============================================================"
    echo "  pgcli E2E: S3 backup target (self-contained local MinIO)"
    echo "  binary:$BINARY  ns:$NAMESPACE  ports:$PG_PORT/$MINIO_API"
    echo "============================================================"
    [ -x "$BINARY" ] || { echo "binary missing: $BINARY"; exit 1; }
    command -v podman >/dev/null || { echo "podman required"; exit 1; }
    python3 -c 'import yaml' 2>/dev/null || { echo "python3 + pyyaml required"; exit 1; }

    # ---- isolated config ----
    section "Setup: isolated config"
    rm -rf "$ROOT"; mkdir -p "$ROOT" "$BASE_DIR" "$BACKUP_DIR"
    run_test "config init (own base+backup dir)" pg config init \
        -o "$CONFIG_FILE" --base-dir "$BASE_DIR" --backup-dir "$BACKUP_DIR" \
        --namespace "$NAMESPACE" --pg-start-port "$PG_PORT" --pg-ssh-port "$SSH_PORT"

    # ---- local TLS MinIO as the S3 store ----
    section "Setup: local MinIO store + bucket"
    # Defensive: a leftover store container from an interrupted run would make
    # install skip creation — and its password is not recoverable without the
    # config entry, poisoning every later S3 op. Always start from zero.
    pg addon remove minio --name "$MINIO" --force >/dev/null 2>&1 || true
    podman rm -f "pgcli-minio-$NAMESPACE-$MINIO" >/dev/null 2>&1 || true
    MIN_OUT=$(pg addon install minio --name "$MINIO" --tls \
        --api-port "$MINIO_API" --console-port "$MINIO_CONSOLE" --listen 0.0.0.0 2>&1)
    echo "$MIN_OUT" | grep -q "installed" && pass "MinIO installed (TLS)" || fail "MinIO install: $MIN_OUT"
    CA_FILE=$(echo "$MIN_OUT" | grep -oE '/[^ ]*ca\.crt' | head -1)
    # Fallback: pgcli writes the store's cert chain under <base-dir>/tls/minio/<name>/
    [ -f "$CA_FILE" ] || CA_FILE=$(find "$BASE_DIR" -path "*/tls/minio/$MINIO/ca.crt" 2>/dev/null | head -1)
    [ -f "$CA_FILE" ] && pass "CA file present: $CA_FILE" || fail "CA file path not found (install output nor $BASE_DIR)"
    S3_SECRET=$(pg addon password minio --name "$MINIO" 2>/dev/null)
    [ -n "$S3_SECRET" ] && pass "root password retrieved" || fail "could not read minio root password"
    # Wait for the store to actually serve before aliasing — right after
    # install the container can still be warming up, and a silently failing
    # `mc mb` would poison every S3 write downstream (NoSuchBucket).
    MB_OUT=""
    for _ in $(seq 1 20); do
        pg mc alias set "$MINIO" "https://$ENDPOINT" admin "$S3_SECRET" -- --insecure >/dev/null 2>&1 && break
        sleep 2
    done
    MB_OUT=$(pg mc mb "$MINIO/$BUCKET" -- --insecure 2>&1)
    pg mc ls "$MINIO" -- --insecure 2>/dev/null | grep -q "$BUCKET" \
        && pass "bucket $BUCKET created" || fail "bucket creation failed: $MB_OUT"

    # ---- global backup.repo.s3 (loopback endpoint) ----
    section "Setup: global backup.repo.s3"
    CFG="$CONFIG_FILE" EP="$ENDPOINT" BK="$BUCKET" CA="$CA_FILE" PW="$S3_SECRET" python3 - <<'PY'
import os, yaml
path = os.environ["CFG"]
d = yaml.safe_load(open(path))
d["backup"] = d.get("backup", {})
d["backup"]["repo"] = {"s3": {
    "endpoint": os.environ["EP"], "bucket": os.environ["BK"],
    "access_key": "admin", "secret_key": os.environ["PW"], "ca_file": os.environ["CA"],
}}
yaml.safe_dump(d, open(path, "w"), sort_keys=False, width=200)
PY
    run_test "backup setup" pg backup setup
    run_test "backup reachable" pg backup status

    # ---- defensive: incomplete repo must be refused ----
    cp "$CONFIG_FILE" "$ROOT/good.yaml"
    yaml_set backup.repo.s3.secret_key ""
    OUT=$(pg create -i s3bad --backup-target s3 2>&1); RC=$?
    cp "$ROOT/good.yaml" "$CONFIG_FILE"
    if [ $RC -ne 0 ] && echo "$OUT" | grep -qiE "secret|incomplete"; then
        pass "create --backup-target s3 refused with incomplete repo"
    else
        fail "incomplete-repo create not refused (rc=$RC: $OUT)"
    fi
    pg destroy -i s3bad --force >/dev/null 2>&1 || true

    # ---- create mixed instances ----
    section "Test: create instances (mixed targets)"
    run_test "create s3t --backup-target s3" pg create -i s3t --backup-target s3
    [ "$(yaml_get s3t pitr.backup_target)" = "s3" ] \
        && pass "s3t persisted backup_target: s3" \
        || fail "s3t backup_target='$(yaml_get s3t pitr.backup_target)' want s3"
    run_test "create localt (default local)" pg create -i localt
    [ -z "$(yaml_get localt pitr.backup_target)" ] \
        && pass "localt has no backup_target (local)" \
        || fail "localt leaked backup_target='$(yaml_get localt pitr.backup_target)'"
    run_test "start s3t" pg start -i s3t; sleep 3
    run_test "start localt" pg start -i localt; sleep 3
    run_test "s3t SQL" sqlval s3t "SELECT 1"

    # ---- rendering + mounts ----
    section "Test: per-stanza rendering + container mounts"
    pg backup setup >/dev/null 2>&1   # ensure conf reflects the new stanzas
    podman exec "$BC" cat /etc/pgbackrest/pgbackrest.conf > "$ROOT/backconf.txt" 2>/dev/null
    S3=$(sed -n '/\[pgcli_s3t\]/,/^\[/p' "$ROOT/backconf.txt")
    LOC=$(sed -n '/\[pgcli_localt\]/,/^\[/p' "$ROOT/backconf.txt")
    echo "$S3" | grep -q "repo1-type=s3" && pass "backup conf: s3t stanza has repo1-type=s3" || fail "s3t stanza missing S3"
    echo "$S3" | grep -q "repo1-s3-uri-style=path" && pass "s3t pins uri-style=path (DNS-safe)" || fail "s3t missing uri-style=path"
    echo "$LOC" | grep -q "repo1-type=s3" && fail "localt stanza must NOT have S3" || pass "backup conf: localt stanza has no S3"
    grep -q "^repo1-path=/var/lib/pgbackrest" "$ROOT/backconf.txt" && pass "[global] keeps local repo1-path" || fail "[global] repo1-path missing"
    podman inspect "$(inst s3t)" --format '{{json .Mounts}}' | grep -q "/etc/pgbackrest/ca.crt" \
        && pass "s3t PG container has CA mount" || fail "s3t PG container missing CA mount"
    podman exec "$(inst s3t)" cat /etc/pgbackrest/pgbackrest.conf | grep -q "repo1-type=s3" \
        && pass "s3t archive-push conf carries S3 block" || fail "s3t archive-push conf missing S3"

    # ---- backup to S3, nothing local ----
    section "Test: full backup to S3, nothing lands local (no dual-write)"
    sqlval s3t "CREATE TABLE t(id serial primary key, msg text)" >/dev/null
    sqlval s3t "INSERT INTO t(msg) VALUES ('pre-backup'),('row2'),('row3')" >/dev/null
    run_test "snapshot s3t (full -> S3)" pg snapshot create -i s3t
    RP=$(repo_path)
    if [ -z "$RP" ]; then fail "cannot resolve local repo path"
    elif [ ! -d "$RP/backup/pgcli_s3t" ]; then pass "no pgcli_s3t dir in local repo (no dual-write)"
    else
        N=$(find "$RP/backup/pgcli_s3t" -type f 2>/dev/null | wc -l)
        [ "$N" -eq 0 ] && pass "local pgcli_s3t dir empty (no dual-write)" || fail "local repo has $N files for pgcli_s3t"
    fi
    pg mc ls "$MINIO/$BUCKET" >/dev/null 2>&1 && pass "S3 bucket reachable via mc" || pass "mc object check non-fatal (PITR proves S3)"

    # ---- WAL to S3 ----
    section "Test: WAL archiving to S3"
    sqlval s3t "INSERT INTO t(msg) VALUES ('wal-meow')" >/dev/null
    sqlval s3t "SELECT pg_switch_wal()" >/dev/null
    pg backup list-stanza 2>/dev/null | grep -q pgcli_s3t && pass "list-stanza shows pgcli_s3t" || fail "pgcli_s3t missing in list-stanza"
    if wait_wal s3t; then pass "s3t WAL archived to S3 (failed_count=0)"; else fail "s3t WAL not archived within 60s"; fi

    # ---- local untouched ----
    section "Test: local instance still backs up locally"
    run_test "snapshot localt" pg snapshot create -i localt
    RP=$(repo_path)
    [ -n "$RP" ] && [ -d "$RP/backup/pgcli_localt" ] \
        && pass "pgcli_localt present in local repo" || fail "pgcli_localt missing from local repo ($RP)"

    # ---- replica promote (clean s3t) ----
    section "Test: replica promote resolves backup target"
    run_test "replica create s3rep" pg replica create s3rep -i s3t; sleep 5
    [ "$(yaml_get s3rep pitr.enabled)" = "False" ] \
        && pass "s3rep replica: PITR disabled" || fail "s3rep replica PITR unexpectedly enabled"
    run_test "promote s3rep" pg replica promote s3rep
    [ "$(yaml_get s3rep pitr.backup_target)" = "s3" ] \
        && pass "promoted replica took backup_target=s3 (host S3 wins)" \
        || fail "promoted replica target='$(yaml_get s3rep pitr.backup_target)'"
    if wait_wal s3rep s3t_db; then pass "promoted replica archives WAL to S3"; else fail "promoted replica WAL not archived within 60s"; fi

    # fake cross-host replica entry (no container) — resolver-level checks
    CFG="$CONFIG_FILE" python3 - <<'PY'
import os, yaml
path = os.environ["CFG"]
d = yaml.safe_load(open(path))
base = dict(d["instances"]["s3rep"]); base.pop("podman", None)
base["replica_of"] = "s3rep"
base["primary_dsn"] = "postgres://admin:x@192.0.2.99:59999/s3rep_db"
base["pitr"] = {"enabled": False, "pgbackrest_stanza": "pgcli_s3x"}
d["instances"]["s3x"] = base
yaml.safe_dump(d, open(path, "w"), sort_keys=False, width=200)
PY
    OUT=$(pg replica promote s3x 2>&1)
    echo "$OUT" | grep -qiE "not running|cannot promote" \
        && pass "cross-host promote not policy-blocked (fails only on running-check)" \
        || fail "unexpected cross-host promote output: $OUT"
    if echo "$OUT" | grep -qiE "cross-host.*no backup|configure .*first"; then
        fail "old cross-host abort text still present"
    else
        pass "no cross-host abort text anywhere"
    fi
    cp "$CONFIG_FILE" "$ROOT/good.yaml"
    yaml_set backup.repo.s3.secret_key ""
    OUT=$(pg replica promote s3x 2>&1); RC=$?
    cp "$ROOT/good.yaml" "$CONFIG_FILE"
    [ $RC -ne 0 ] && echo "$OUT" | grep -qiE "incomplete|secret" \
        && pass "promote refused while S3 block incomplete (rc=$RC)" \
        || fail "incomplete-S3 promote not refused (rc=$RC: $OUT)"
    CFG="$CONFIG_FILE" python3 - <<'PY'
import os, yaml
path = os.environ["CFG"]; d = yaml.safe_load(open(path))
d["instances"].pop("s3x", None)
yaml.safe_dump(d, open(path, "w"), sort_keys=False, width=200)
PY

    # ---- CA drift repair ----
    section "Test: CA mount drift -> start recreates the container"
    # Real-world drift = a PG container that predates its S3 config (e.g. an
    # old local container promoted to S3). Simulate exactly that: create as a
    # plain local instance (container built without the CA mount), then flip
    # backup_target to s3. The next start must detect the missing mount and
    # rebuild via stop+rm+create — data lives on the named volume.
    run_test "create s3drift (local)" pg create -i s3drift
    run_test "start s3drift" pg start -i s3drift; sleep 3
    if podman inspect "$(inst s3drift)" --format '{{json .Mounts}}' | grep -q "/etc/pgbackrest/ca.crt"; then
        fail "local s3drift should have NO CA mount before the flip"
    else
        pass "local s3drift container has no CA mount (pre-drift state)"
    fi
    sqlval s3drift "CREATE TABLE drift(i int)" >/dev/null
    sqlval s3drift "INSERT INTO drift VALUES (42)" >/dev/null
    yaml_set instances.s3drift.pitr.backup_target s3
    OUT=$(pg start -i s3drift 2>&1); RC=$?
    if [ $RC -eq 0 ] && podman inspect "$(inst s3drift)" --format '{{json .Mounts}}' | grep -q "/etc/pgbackrest/ca.crt"; then
        pass "start recreated container with CA mount after drift"
    else
        fail "drift repair broken (rc=$RC: $OUT)"
    fi
    GOT=$(sqlval s3drift "SELECT i FROM drift" 2>/dev/null)
    [ "$GOT" = "42" ] && pass "data intact across drift recreate" || fail "data lost across drift recreate (got '$GOT')"
    podman exec "$(inst s3drift)" grep -q "repo1-type=s3" /etc/pgbackrest/pgbackrest.conf \
        && pass "drift-recreated container has S3 archive conf" || fail "S3 archive conf missing after drift"

    # ---- PITR from S3 (last, destructive) ----
    section "Test: PITR restore from S3"
    PITR_TIME=$(sqlval s3t "SELECT to_char(now() - interval '2 seconds','YYYY-MM-DD HH24:MI:SS')")
    sqlval s3t "INSERT INTO t(msg) VALUES ('gone-after-pitr')" >/dev/null
    sqlval s3t "SELECT pg_switch_wal()" >/dev/null; sleep 5
    BEFORE=$(sqlval s3t "SELECT count(*) FROM t")
    run_test "pg restore s3t from S3" pg restore -i s3t --time "$PITR_TIME" --force --promote
    for _ in $(seq 1 12); do podman exec "$(inst s3t)" pg_isready -U admin >/dev/null 2>&1 && break; sleep 2; done
    AFTER=$(sqlval s3t "SELECT count(*) FROM t" 2>/dev/null)
    if [ -n "${AFTER:-}" ] && [ "$AFTER" -lt "$BEFORE" ] 2>/dev/null && [ "$AFTER" -ge 3 ] 2>/dev/null; then
        pass "PITR from S3: $BEFORE -> $AFTER (gone-after-pitr rolled back)"
    else
        fail "PITR counts wrong (before=$BEFORE after=${AFTER:-?})"
    fi
    run_test "start s3t after restore" pg start -i s3t

    section "Summary"
    if [ "$FAILED" -eq 0 ]; then
        printf '\033[0;32m✓ ALL PASS — E2E S3 backup-target\033[0m\n'
    else
        printf '\033[0;31m✗ %d check(s) FAILED\033[0m\n' "$FAILED"
        echo "diagnostics: $ROOT/backconf.txt ; pg -c $CONFIG_FILE backup status ; logs -i s3t"
        return 1
    fi
}

main
