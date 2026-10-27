#!/usr/bin/env bash
# Verify that influx2tsdb retention policies become automatic TimescaleDB
# background jobs: job creation and config, actual chunk deletion, independence
# from the proxy process, removal when the default policy becomes INF, and job
# cleanup when the database is dropped. No pg_cron is required or used.
#
# A local add-on is stopped and restarted during the test; use
# --skip-proxy-stop to skip that phase.
#
# Usage:
#   test/addon/test_influx2tsdb_retention.sh [--name NAME] [--url URL] [--db DB]
#                                             [--instance PG_INSTANCE | --pg-dsn DSN]
#                                             [--keep] [--skip-proxy-stop]
#
# By default the URL/database and a local target instance are read from
# `pg addon influx2tsdb status --name <NAME>`.
#
# Examples:
#   test/addon/test_influx2tsdb_retention.sh
#   test/addon/test_influx2tsdb_retention.sh --instance tsdb
#   test/addon/test_influx2tsdb_retention.sh --pg-dsn 'postgres://u:p@host:5432/tsdb_db?sslmode=disable'
set -u

NAME=influx2tsdb
URL=
DB=
PG_INSTANCE=
PG_DSN=
KEEP=0
SKIP_PROXY_STOP=0
RUN=
PROXY_STOPPED=0
PASS=0
FAIL=0
TMPBODY=$(mktemp)
trap 'cleanup' EXIT

usage() {
  sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name) NAME=$2; shift 2 ;;
    --url) URL=$2; shift 2 ;;
    --db) DB=$2; shift 2 ;;
    --instance) PG_INSTANCE=$2; shift 2 ;;
    --pg-dsn) PG_DSN=$2; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --skip-proxy-stop) SKIP_PROXY_STOP=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

ok()   { PASS=$((PASS + 1)); echo "✓ $1"; }
bad()  { FAIL=$((FAIL + 1)); echo "✗ $1"; echo "  want: $2"; echo "  got:  $3"; }
assert_eq()       { if [[ "$3" == "$2" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
assert_contains() { if [[ "$2" == *"$3"* ]]; then ok "$1"; else bad "$1" "contains '$3'" "$2"; fi; }

query() { # query <db> <influxql>
  curl -sS -G "$URL/query" --data-urlencode "db=$1" --data-urlencode "q=$2"
}

write_points() { # write_points <db> <line-protocol>
  curl -sS -o "$TMPBODY" -w '%{http_code}' -XPOST "$URL/write?db=$1" --data-binary "$2"
}

pg_sql() { # pg_sql <sql>
  if [[ -n "$PG_DSN" ]]; then
    pg exec --dsn "$PG_DSN" "$1"
  else
    pg exec -i "$PG_INSTANCE" "$1"
  fi
}

pg_count() { # pg_count <sql returning one integer>
  pg_sql "$1" 2>&1 | awk '$1 ~ /^[0-9]+$/ {print $1; exit}'
}

pg_val() { # pg_val <sql returning one scalar>; prints the value cell
  pg_sql "$1" 2>&1 | awk 'NR > 2 && $1 != "" && $1 != "(" {print $1; exit}'
}

json_eval() {
  python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
    print(eval(sys.argv[1], {"d": d}))
except Exception:
    print("")
' "$1"
}

cleanup() {
  if [[ "$PROXY_STOPPED" -eq 1 ]]; then
    pg addon start influx2tsdb --name "$NAME" >/dev/null 2>&1 || true
  fi
  if [[ -n "$RUN" && -n "$URL" && -n "$DB" && "$KEEP" -eq 0 ]]; then
    query "$DB" "DROP DATABASE \"$RUN\"" >/dev/null 2>&1 || true
  fi
  rm -f "$TMPBODY"
}

# --- discover the endpoint and target --------------------------------------
if [[ -z "$URL" || -z "$DB" || ( -z "$PG_INSTANCE" && -z "$PG_DSN" ) ]]; then
  status=$(pg addon influx2tsdb status --name "$NAME" 2>&1) || { echo "$status" >&2; exit 1; }
  if [[ -z "$URL" ]]; then
    port=$(printf '%s\n' "$status" | sed -n '/^[[:space:]]*Endpoint:/{s/.*:\([0-9][0-9]*\).*/\1/p;q;}')
    [[ -n "$port" ]] || { echo "cannot parse the endpoint port; pass --url" >&2; exit 1; }
    URL="http://127.0.0.1:$port"
  fi
  if [[ -z "$DB" ]]; then
    DB=$(printf '%s\n' "$status" | sed -n 's/^[[:space:]]*InfluxDB database:[[:space:]]*//p' | head -n1)
    [[ -n "$DB" && "$DB" != "-" ]] || { echo "cannot determine the InfluxDB database; pass --db" >&2; exit 1; }
  fi
  if [[ -z "$PG_INSTANCE" && -z "$PG_DSN" ]]; then
    PG_INSTANCE=$(printf '%s\n' "$status" | sed -n 's/^[[:space:]]*Target:[[:space:]]*instance[[:space:]]\{1,\}//p' | head -n1)
    [[ -n "$PG_INSTANCE" ]] || {
      echo "cannot determine the local target instance; pass --instance or --pg-dsn" >&2
      exit 1
    }
  fi
fi

RUN="i2t_rp_$$_$(date +%s)"
SENSOR="rp_probe_$$_$(date +%s)"
NOW=$(date +%s%N)
OLD=$((NOW - 172800000000000)) # 48 hours ago

echo "=== influx2tsdb automatic-retention self-test ==="
echo "  Proxy:    $NAME"
echo "  Endpoint: $URL"
echo "  Database: $DB"
if [[ -n "$PG_DSN" ]]; then echo "  PG check: --pg-dsn"; else echo "  PG check: instance $PG_INSTANCE"; fi
echo "  Temp DB:  $RUN"
echo

# --- preflight ---------------------------------------------------------------
code=$(curl -sS -o /dev/null -w '%{http_code}' "$URL/ping")
assert_eq "ping returns 204" "204" "$code"
assert_eq "pg_cron is not installed (not needed)" "0" \
  "$(pg_count "SELECT count(*) FROM pg_extension WHERE extname = 'pg_cron'")"

# --- 1. create a database and a 1-day default policy ------------------------
body=$(query "$DB" "CREATE DATABASE \"$RUN\"")
assert_eq "CREATE DATABASE returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"
body=$(query "$DB" "CREATE RETENTION POLICY \"rp_1d\" ON \"$RUN\" DURATION 1d REPLICATION 1 DEFAULT")
assert_eq "CREATE RETENTION POLICY returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"

# Two points: one 48h old (expired under 1d) and one current. Writing creates
# the hypertable, which is what the proxy's sync targets.
code=$(write_points "$RUN" "$SENSOR,host=old value=1 $OLD
$SENSOR,host=new value=2 $NOW")
assert_eq "write old+new points returns 204" "204" "$code"

# Force an immediate sync (CREATE ran before the hypertable existed).
query "$RUN" "ALTER RETENTION POLICY \"rp_1d\" ON \"$RUN\" DURATION 1d REPLICATION 1 DEFAULT" >/dev/null

# --- 2. jobs were created automatically by TimescaleDB ----------------------
ret_jobs=$(pg_count "SELECT count(*) FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN' AND proc_name = 'policy_retention'")
assert_eq "retention job created automatically" "1" "$ret_jobs"
assert_eq "job drop_after matches the 1d policy" "24:00:00" \
  "$(pg_val "SELECT config->>'drop_after' FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN' AND proc_name = 'policy_retention'")"

comp_jobs=$(pg_count "SELECT count(*) FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN' AND proc_name = 'policy_compression'")
assert_eq "compression job created automatically" "1" "$comp_jobs"
assert_eq "compress_after = retention/4 (6 hours)" "06:00:00" \
  "$(pg_val "SELECT config->>'compress_after' FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN' AND proc_name = 'policy_compression'")"

assert_eq "both jobs carry a TimescaleDB schedule" "2" \
  "$(pg_count "SELECT count(*) FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN' AND schedule_interval IS NOT NULL")"

# --- 3. the job actually deletes expired chunks -----------------------------
rid=$(pg_count "SELECT job_id FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN' AND proc_name = 'policy_retention'")
if [[ "$rid" =~ ^[0-9]+$ ]]; then
  ok "retention job id is $rid"
else
  bad "retention job id is numeric" "an integer" "$rid"
fi
pg_sql "CALL run_job($rid)" >/dev/null 2>&1
assert_eq "expired point removed by the retention job" "1" \
  "$(pg_count "SELECT count(*) FROM \"$RUN\".\"$SENSOR\"")"
assert_eq "recent point survives" "1" \
  "$(pg_count "SELECT count(*) FROM \"$RUN\".\"$SENSOR\" WHERE host = 'new'")"
assert_eq "proxy sees the post-job row count" "1" \
  "$(query "$RUN" "SELECT count(\"value\") FROM \"$SENSOR\"" | json_eval 'd["results"][0]["series"][0]["values"][0][1]')"

# --- 4. retention runs without the proxy process ---------------------------
if [[ "$SKIP_PROXY_STOP" -eq 0 ]]; then
  pg addon stop influx2tsdb --name "$NAME" >/dev/null 2>&1 || true
  PROXY_STOPPED=1
  assert_eq "proxy stopped for the independence check" "000" \
    "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 "$URL/ping" 2>/dev/null || true)"

  # Insert an expired row straight into PostgreSQL, bypassing the proxy.
  pg_sql "INSERT INTO \"$RUN\".\"$SENSOR\" (time, host, value) VALUES (now() - interval '48 hours', 'old2', 3)" >/dev/null
  assert_eq "direct insert bypasses the proxy" "2" \
    "$(pg_count "SELECT count(*) FROM \"$RUN\".\"$SENSOR\"")"
  pg_sql "CALL run_job($rid)" >/dev/null 2>&1
  assert_eq "retention job deletes expired data while the proxy is down" "1" \
    "$(pg_count "SELECT count(*) FROM \"$RUN\".\"$SENSOR\"")"
  assert_eq "the directly inserted expired row is gone" "0" \
    "$(pg_count "SELECT count(*) FROM \"$RUN\".\"$SENSOR\" WHERE host = 'old2'")"

  pg addon start influx2tsdb --name "$NAME" >/dev/null 2>&1
  PROXY_STOPPED=0
  code=$(curl -sS -o /dev/null -w '%{http_code}' "$URL/ping")
  assert_eq "proxy restarted after the independence check" "204" "$code"
  assert_eq "data state is unchanged after restart" "1" \
    "$(query "$RUN" "SELECT count(\"value\") FROM \"$SENSOR\"" | json_eval 'd["results"][0]["series"][0]["values"][0][1]')"
else
  echo "- skipped proxy-stop independence check (--skip-proxy-stop)"
fi

# --- 5. switching the default policy to INF removes the retention job -------
body=$(query "$RUN" "CREATE RETENTION POLICY \"rp_inf\" ON \"$RUN\" DURATION INF REPLICATION 1 DEFAULT")
assert_eq "CREATE INF default policy returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"
assert_eq "retention job removed for the INF default" "0" \
  "$(pg_count "SELECT count(*) FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN' AND proc_name = 'policy_retention'")"
assert_eq "compression job kept for the INF default" "1" \
  "$(pg_count "SELECT count(*) FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN' AND proc_name = 'policy_compression'")"
assert_contains "compression falls back to 1 day for INF" \
  "$(pg_sql "SELECT config->>'compress_after' FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN' AND proc_name = 'policy_compression'")" "1 day"

# --- 6. dropping the database removes its jobs -----------------------------
body=$(query "$DB" "DROP DATABASE \"$RUN\"")
assert_eq "DROP DATABASE returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"
assert_eq "jobs removed with the database schema" "0" \
  "$(pg_count "SELECT count(*) FROM timescaledb_information.jobs WHERE hypertable_schema = '$RUN'")"
RUN=

echo
echo "=== result: $PASS passed, $FAIL failed ==="
[[ "$FAIL" -eq 0 ]]
