#!/usr/bin/env bash
# Exercise the common time-series operations through an influx2tsdb proxy:
# Line Protocol writes, tag/time filtering, aggregates, GROUP BY, metadata
# SHOW commands, DELETE / DROP MEASUREMENT, database lifecycle and retention
# policy CRUD. With --instance it also checks the TimescaleDB side.
#
# Usage:
#   test/addon/test_influx2tsdb.sh [--name NAME] [--url URL] [--db DB]
#                                  [--instance PG_INSTANCE] [--keep]
#
# By default the URL and database are read from
# `pg addon influx2tsdb status --name <NAME>`.
#
# Examples:
#   # local target, metadata discovered from pg addon status
#   test/addon/test_influx2tsdb.sh --instance tsdb
#
#   # explicit endpoint (e.g. remote/proxied proxy)
#   test/addon/test_influx2tsdb.sh --url http://127.0.0.1:8087 --db tsdb_db
#
#   # remote PostgreSQL target
#   pg addon install influx2tsdb --dsn 'postgres://user:pass@db:5432/metrics?sslmode=disable' --database metrics
#   test/addon/test_influx2tsdb.sh --name influx2tsdb --db metrics
set -u

NAME=influx2tsdb
URL=
DB=
PG_INSTANCE=
KEEP=0
PREFIX=i2t_selftest
# Unique per run: the proxy caches schemas in memory, so re-creating a
# measurement right after DROP MEASUREMENT would reuse stale cache state
# without re-inserting its metadata rows.
RUN_ID="$$_$(date +%s)"
TMPDB="${PREFIX}_db_${RUN_ID}"
PASS=0
FAIL=0
TMPBODY=$(mktemp)
trap 'rm -f "$TMPBODY"' EXIT

usage() {
  sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name) NAME=$2; shift 2 ;;
    --url) URL=$2; shift 2 ;;
    --db) DB=$2; shift 2 ;;
    --instance) PG_INSTANCE=$2; shift 2 ;;
    --keep) KEEP=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

ok()   { PASS=$((PASS + 1)); echo "✓ $1"; }
bad()  { FAIL=$((FAIL + 1)); echo "✗ $1"; echo "  want: $2"; echo "  got:  $3"; }
assert_eq()           { if [[ "$3" == "$2" ]]; then ok "$1"; else bad "$1" "$2" "$3"; fi; }
assert_contains()     { if [[ "$2" == *"$3"* ]]; then ok "$1"; else bad "$1" "contains '$3'" "$2"; fi; }
assert_not_contains() { if [[ "$2" != *"$3"* ]]; then ok "$1"; else bad "$1" "does not contain '$3'" "$2"; fi; }

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

query() { # query <db> <influxql>
  curl -sS -G "$URL/query" --data-urlencode "db=$1" --data-urlencode "q=$2"
}

write_points() { # write_points <db> <line-protocol>; sets HTTP_CODE / HTTP_BODY
  HTTP_CODE=$(curl -sS -o "$TMPBODY" -w '%{http_code}' -XPOST "$URL/write?db=$1" --data-binary "$2")
  HTTP_BODY=$(cat "$TMPBODY")
}

# --- discover endpoint when not supplied ------------------------------------
if [[ -z "$URL" || -z "$DB" ]]; then
  if ! command -v pg >/dev/null 2>&1; then
    echo "pg not found and --url/--db not fully supplied" >&2
    exit 2
  fi
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
fi

SENSOR="${PREFIX}_sensor_${RUN_ID}"
BASIC="${PREFIX}_basic_${RUN_ID}"

echo "=== influx2tsdb self-test ==="
echo "  Proxy:    $NAME"
echo "  Endpoint: $URL"
echo "  Database: $DB"
if [[ -n "$PG_INSTANCE" ]]; then echo "  PG check: instance $PG_INSTANCE"; fi
echo

# --- preflight --------------------------------------------------------------
code=$(curl -sS -o /dev/null -w '%{http_code}' "$URL/ping")
assert_eq "ping returns 204" "204" "$code"

# --- 1. single write and read back -----------------------------------------
T0=1700000000000000000
write_points "$DB" "$BASIC,host=web01 value=1i $T0"
assert_eq "single write returns 204" "204" "$HTTP_CODE"
body=$(query "$DB" "SELECT * FROM \"$BASIC\"")
assert_contains "SELECT returns the measurement" "$body" "\"name\":\"$BASIC\""
assert_contains "SELECT returns the tag"         "$body" "\"web01\""
assert_contains "SELECT returns the field"       "$body" ",1]"

# --- 2. batch write with multiple tags and field types ---------------------
# Fixed timestamps (ns, InfluxDB default precision), one minute apart.
T1=$T0
T2=$((T0 + 60000000000))
T3=$((T0 + 120000000000))
T4=$((T0 + 180000000000))
BATCH="$SENSOR,host=a value=10.5,ok=true,state=\"up\" $T1
$SENSOR,host=a value=20.5,ok=true,state=\"up\" $T2
$SENSOR,host=b value=30.5,ok=false,state=\"down\" $T3
$SENSOR,host=b value=40.5,ok=false,state=\"down\" $T4"
write_points "$DB" "$BATCH"
assert_eq "batch write returns 204" "204" "$HTTP_CODE"

count=$(query "$DB" "SELECT count(\"value\") FROM \"$SENSOR\"" | json_eval 'd["results"][0]["series"][0]["values"][0][1]')
assert_eq "count = 4" "4" "$count"

# --- 3. time-range filtering (InfluxQL timestamp literals) -----------------
body=$(query "$DB" "SELECT * FROM \"$SENSOR\" WHERE time >= '2023-11-14 22:13:20' AND time <= '2023-11-14 22:15:20'")
assert_contains "time range includes T3"     "$body" "30.5"
assert_not_contains "time range excludes T4" "$body" "40.5"

# --- 4. tag filtering -------------------------------------------------------
count=$(query "$DB" "SELECT count(\"value\") FROM \"$SENSOR\" WHERE \"host\" = 'b'" | json_eval 'd["results"][0]["series"][0]["values"][0][1]')
assert_eq "tag filter host=b count = 2" "2" "$count"

# --- 5. aggregates ----------------------------------------------------------
for pair in "mean:25.5" "sum:102" "min:10.5" "max:40.5"; do
  fn=${pair%%:*}; want=${pair##*:}
  got=$(query "$DB" "SELECT $fn(\"value\") FROM \"$SENSOR\"" | json_eval 'd["results"][0]["series"][0]["values"][0][1]')
  assert_eq "$fn(value) = $want" "$want" "$got"
done

# --- 6. GROUP BY time and tag ----------------------------------------------
rows=$(query "$DB" "SELECT mean(\"value\") FROM \"$SENSOR\" WHERE time >= '2023-11-14 22:13:20' AND time <= '2023-11-14 22:16:20' GROUP BY time(1m), \"host\" fill(none)" \
  | json_eval 'sum(len(s["values"]) for s in d["results"][0]["series"])')
assert_eq "GROUP BY time(1m),host yields 4 buckets" "4" "$rows"

body=$(query "$DB" "SELECT mean(\"value\") FROM \"$SENSOR\" GROUP BY \"host\"")
assert_contains "GROUP BY host: a mean 15.5" "$body" "15.5"
assert_contains "GROUP BY host: b mean 35.5" "$body" "35.5"

body=$(query "$DB" "SELECT last(\"value\") FROM \"$SENSOR\" GROUP BY \"host\"")
assert_contains "last() returns host b's latest value" "$body" "40.5"
body=$(query "$DB" "SELECT first(\"value\") FROM \"$SENSOR\" GROUP BY \"host\"")
assert_contains "first() returns host a's earliest value" "$body" "10.5"

# --- 7. SHOW metadata commands ---------------------------------------------
body=$(query "$DB" "SHOW MEASUREMENTS")
assert_contains "SHOW MEASUREMENTS lists the sensor" "$body" "$SENSOR"
body=$(query "$DB" "SHOW TAG KEYS FROM \"$SENSOR\"")
assert_contains "SHOW TAG KEYS lists host" "$body" "host"
body=$(query "$DB" "SHOW TAG VALUES FROM \"$SENSOR\" WITH KEY = \"host\"")
assert_contains "SHOW TAG VALUES lists a" "$body" "\"a\""
assert_contains "SHOW TAG VALUES lists b" "$body" "\"b\""
body=$(query "$DB" "SHOW FIELD KEYS FROM \"$SENSOR\"")
assert_contains "SHOW FIELD KEYS lists value"  "$body" "value"
assert_contains "SHOW FIELD KEYS reports float" "$body" "float"
assert_contains "SHOW FIELD KEYS lists ok"     "$body" "ok"
assert_contains "SHOW FIELD KEYS lists state"  "$body" "state"

# --- 8. PostgreSQL / TimescaleDB side (optional) ---------------------------
if [[ -n "$PG_INSTANCE" ]]; then
  got=$(pg exec -i "$PG_INSTANCE" "SELECT count(*) FROM \"$DB\".\"$SENSOR\"" 2>&1 | awk '$1 ~ /^[0-9]+$/ {print $1; exit}')
  assert_eq "PostgreSQL table row count = 4" "4" "$got"
  got=$(pg exec -i "$PG_INSTANCE" "SELECT count(*) FROM timescaledb_information.hypertables WHERE hypertable_schema='$DB' AND hypertable_name='$SENSOR'" 2>&1 | awk '$1 ~ /^[0-9]+$/ {print $1; exit}')
  assert_eq "measurement is a TimescaleDB hypertable" "1" "$got"
fi

# --- 9. DELETE and DROP MEASUREMENT ---------------------------------------
body=$(query "$DB" "DELETE FROM \"$SENSOR\" WHERE \"host\" = 'a'")
assert_eq "DELETE returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"
count=$(query "$DB" "SELECT count(\"value\") FROM \"$SENSOR\"" | json_eval 'd["results"][0]["series"][0]["values"][0][1]')
assert_eq "DELETE removed host=a points" "2" "$count"

query "$DB" "DROP MEASUREMENT \"$SENSOR\"" >/dev/null
body=$(query "$DB" "SHOW MEASUREMENTS")
assert_not_contains "DROP MEASUREMENT removes the sensor" "$body" "$SENSOR"

# --- 10. database lifecycle and retention-policy CRUD ---------------------
body=$(query "$DB" "CREATE DATABASE \"$TMPDB\"")
assert_eq "CREATE DATABASE returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"
body=$(query "$DB" "SHOW DATABASES")
assert_contains "SHOW DATABASES lists the new database" "$body" "$TMPDB"

write_points "$TMPDB" "tmp_measure,host=x value=7i $T0"
assert_eq "write into the new database returns 204" "204" "$HTTP_CODE"
count=$(query "$TMPDB" "SELECT count(\"value\") FROM \"tmp_measure\"" | json_eval 'd["results"][0]["series"][0]["values"][0][1]')
assert_eq "read back from the new database count = 1" "1" "$count"

body=$(query "$TMPDB" "CREATE RETENTION POLICY \"rp_1d\" ON \"$TMPDB\" DURATION 1d REPLICATION 1")
assert_eq "CREATE RETENTION POLICY returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"
body=$(query "$TMPDB" "SHOW RETENTION POLICIES ON \"$TMPDB\"")
assert_contains "SHOW RETENTION POLICIES lists rp_1d" "$body" "rp_1d"
assert_contains "rp_1d duration is 24h"              "$body" "24h"
body=$(query "$TMPDB" "ALTER RETENTION POLICY \"rp_1d\" ON \"$TMPDB\" DURATION 2d REPLICATION 1")
assert_eq "ALTER RETENTION POLICY returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"
body=$(query "$TMPDB" "SHOW RETENTION POLICIES ON \"$TMPDB\"")
assert_contains "ALTER changed rp_1d duration to 48h" "$body" "48h"
body=$(query "$TMPDB" "DROP RETENTION POLICY \"rp_1d\" ON \"$TMPDB\"")
assert_eq "DROP RETENTION POLICY returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"
body=$(query "$TMPDB" "SHOW RETENTION POLICIES ON \"$TMPDB\"")
assert_not_contains "DROP RETENTION POLICY removes rp_1d" "$body" "rp_1d"

body=$(query "$DB" "DROP DATABASE \"$TMPDB\"")
assert_eq "DROP DATABASE returns no error" "" "$(printf '%s' "$body" | json_eval 'd["results"][0].get("error", "")')"
body=$(query "$DB" "SHOW DATABASES")
assert_not_contains "DROP DATABASE removes the database" "$body" "$TMPDB"

# --- 11. invalid write is rejected ----------------------------------------
write_points "$DB" "this is not line protocol"
assert_eq "malformed line protocol returns 400" "400" "$HTTP_CODE"

# --- cleanup ----------------------------------------------------------------
if [[ "$KEEP" -eq 0 ]]; then
  query "$DB" "DROP MEASUREMENT \"$BASIC\"" >/dev/null
  query "$DB" "DROP MEASUREMENT \"$SENSOR\"" >/dev/null
  query "$DB" "DROP DATABASE \"$TMPDB\"" >/dev/null
  echo
  echo "test measurements/database removed (use --keep to retain them)"
else
  echo
  echo "kept test data: $BASIC, $SENSOR, $TMPDB"
fi

echo
echo "=== result: $PASS passed, $FAIL failed ==="
[[ "$FAIL" -eq 0 ]]
