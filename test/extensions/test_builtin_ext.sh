#!/bin/bash
# Builtin (contrib) Extensions Test Script
# Tests all 45 extensions already in the PostgreSQL base image — no apt install needed.
#
# Usage: ./test_builtin_ext.sh --ext <name>   # test single extension
#        ./test_builtin_ext.sh --all          # test all builtin extensions
#        ./test_builtin_ext.sh --list         # list available extensions
#
# Tests are run against the default instance (use -i to override).

set -euo pipefail

INSTANCE="default"
EXT_NAME=""
RUN_ALL=false

# ─── Color helpers ────────────────────────────────────────────────────
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

ok()   { echo -e "${GREEN}[PASS]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; }
info() { echo -e "${CYAN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }

# ─── pg wrapper ───────────────────────────────────────────────────────
pg_exec() { pg exec -i "$INSTANCE" "$@"; }
pg_scalar() { pg psql -i "$INSTANCE" -- -t -A -c "$@"; }

# ─── All 45 builtin extension names ──────────────────────────────────
ALL_EXTENSIONS=(
    amcheck
    autoinc
    bloom
    btree_gin
    btree_gist
    citext
    cube
    dblink
    dict_int
    dict_xsyn
    earthdistance
    file_fdw
    fuzzystrmatch
    hstore
    insert_username
    intagg
    intarray
    isn
    lo
    ltree
    moddatetime
    pageinspect
    pg_buffercache
    pg_freespacemap
    pg_logicalinspect
    pg_prewarm
    pg_stat_statements
    pg_surgery
    pg_trgm
    pg_visibility
    pg_walinspect
    pgcrypto
    pgrowlocks
    pgstattuple
    postgres_fdw
    refint
    seg
    sslinfo
    tablefunc
    tcn
    tsm_system_rows
    tsm_system_time
    unaccent
    "uuid-ossp"
    xml2
)

# ─── Test functions ───────────────────────────────────────────────────

test_amcheck() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS amcheck;"
    pg_exec "CREATE TABLE IF NOT EXISTS _btest (id serial PRIMARY KEY, val text);"
    pg_exec "INSERT INTO _btest (val) VALUES ('a'),('b'),('c');"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_index WHERE indrelid = '_btest'::regclass AND bt_index_check(indexrelid) IS NOT NULL;")
    if [[ -n "$result" ]]; then
        ok "amcheck: bt_index_check passed on $result index(es)"
    else
        fail "amcheck: bt_index_check returned nothing"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _btest;"
    pg_exec "DROP EXTENSION IF EXISTS amcheck;"
    ok "amcheck: cleanup done"
}

test_autoinc() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS autoinc;"
    pg_exec "CREATE TABLE IF NOT EXISTS _aitest (id int DEFAULT nextval('pg_class_oid_seq'::regclass), name text);"
    # autoinc provides create_autoinc_trigger — test it exists
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname = 'autoinc' AND pronamespace = 'public'::regnamespace;")
    if [[ "$result" -ge 1 ]]; then
        ok "autoinc: trigger function installed ($result functions)"
    else
        fail "autoinc: trigger function not found"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _aitest;"
    pg_exec "DROP EXTENSION IF EXISTS autoinc CASCADE;"
    ok "autoinc: cleanup done"
}

test_bloom() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS bloom;"
    pg_exec "CREATE TABLE IF NOT EXISTS _bloomtest (id serial, a int, b int, c int);"
    pg_exec "INSERT INTO _bloomtest (a, b, c) SELECT g%100, g%200, g%300 FROM generate_series(1,1000) g;"
    pg_exec "CREATE INDEX IF NOT EXISTS _bloomtest_idx ON _bloomtest USING bloom (a, b, c);"
    local result
    result=$(pg_scalar "SELECT count(*) FROM _bloomtest WHERE a = 42 AND b = 42;")
    if [[ -n "$result" ]]; then
        ok "bloom: bloom index query returned $result rows"
    else
        fail "bloom: query returned nothing"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _bloomtest;"
    pg_exec "DROP EXTENSION IF EXISTS bloom;"
    ok "bloom: cleanup done"
}

test_btree_gin() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS btree_gin;"
    pg_exec "CREATE TABLE IF NOT EXISTS _bgintest (id serial, val int);"
    pg_exec "INSERT INTO _bgintest (val) SELECT g FROM generate_series(1,100) g;"
    pg_exec "CREATE INDEX IF NOT EXISTS _bgintest_idx ON _bgintest USING gin (val);"
    local result
    result=$(pg_scalar "SELECT count(*) FROM _bgintest WHERE val = 42;")
    if [[ "$result" == "1" ]]; then
        ok "btree_gin: GIN index on int found 1 row"
    else
        fail "btree_gin: expected 1, got $result"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _bgintest;"
    pg_exec "DROP EXTENSION IF EXISTS btree_gin;"
    ok "btree_gin: cleanup done"
}

test_btree_gist() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS btree_gist;"
    pg_exec "CREATE TABLE IF NOT EXISTS _bgstest (id serial, val int);"
    pg_exec "INSERT INTO _bgstest (val) SELECT g FROM generate_series(1,100) g;"
    pg_exec "CREATE INDEX IF NOT EXISTS _bgstest_idx ON _bgstest USING gist (val);"
    local result
    result=$(pg_scalar "SELECT count(*) FROM _bgstest WHERE val = 42;")
    if [[ "$result" == "1" ]]; then
        ok "btree_gist: GiST index on int found 1 row"
    else
        fail "btree_gist: expected 1, got $result"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _bgstest;"
    pg_exec "DROP EXTENSION IF EXISTS btree_gist;"
    ok "btree_gist: cleanup done"
}

test_citext() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS citext;"
    pg_exec "CREATE TABLE IF NOT EXISTS _citest (id serial, name citext);"
    pg_exec "INSERT INTO _citest (name) VALUES ('Hello'),('World');"
    local result
    result=$(pg_scalar "SELECT name FROM _citest WHERE name = 'hello';")
    if [[ "$result" == "Hello" ]]; then
        ok "citext: case-insensitive match found 'Hello'"
    else
        fail "citext: expected Hello, got '$result'"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _citest;"
    pg_exec "DROP EXTENSION IF EXISTS citext;"
    ok "citext: cleanup done"
}

test_cube() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS cube;"
    local result
    result=$(pg_scalar "SELECT cube('(1,2,3)')::text;")
    if [[ "$result" == "(1, 2, 3)" ]]; then
        ok "cube: created cube (1,2,3)"
    else
        fail "cube: expected '(1, 2, 3)', got '$result'"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS cube;"
    ok "cube: cleanup done"
}

test_dblink() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS dblink;"
    # Just verify the extension functions exist
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname = 'dblink' AND pronamespace = 'public'::regnamespace;")
    if [[ "$result" -ge 1 ]]; then
        ok "dblink: dblink function installed ($result functions)"
    else
        fail "dblink: function not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS dblink;"
    ok "dblink: cleanup done"
}

test_dict_int() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS dict_int;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_ts_dict WHERE dictname = 'intdict';")
    if [[ "$result" -ge 1 ]]; then
        ok "dict_int: intdict text search dictionary installed"
    else
        fail "dict_int: dictionary not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS dict_int;"
    ok "dict_int: cleanup done"
}

test_dict_xsyn() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS dict_xsyn;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_ts_dict WHERE dictname = 'xsyn';")
    if [[ "$result" -ge 1 ]]; then
        ok "dict_xsyn: xsyn text search dictionary installed"
    else
        fail "dict_xsyn: dictionary not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS dict_xsyn;"
    ok "dict_xsyn: cleanup done"
}

test_earthdistance() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS cube;"
    pg_exec "CREATE EXTENSION IF NOT EXISTS earthdistance;"
    # Distance between two points on earth in miles
    local result
    result=$(pg_scalar "SELECT round(earth_distance(ll_to_earth(40.7128, -74.0060), ll_to_earth(51.5074, -0.1278))::numeric, 0);")
    if [[ -n "$result" && "$result" -gt 0 ]]; then
        ok "earthdistance: NYC to London = ${result} miles"
    else
        fail "earthdistance: invalid distance '$result'"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS earthdistance;"
    pg_exec "DROP EXTENSION IF EXISTS cube;"
    ok "earthdistance: cleanup done"
}

test_file_fdw() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS file_fdw;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_foreign_data_wrapper WHERE fdwname = 'file_fdw';")
    if [[ "$result" == "1" ]]; then
        ok "file_fdw: foreign data wrapper registered"
    else
        fail "file_fdw: FDW not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS file_fdw;"
    ok "file_fdw: cleanup done"
}

test_fuzzystrmatch() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS fuzzystrmatch;"
    local result
    result=$(pg_scalar "SELECT levenshtein('PostgreSQL', 'PostgresSQL');")
    if [[ "$result" == "1" ]]; then
        ok "fuzzystrmatch: levenshtein('PostgreSQL','PostgresSQL') = 1"
    else
        fail "fuzzystrmatch: expected 1, got $result"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS fuzzystrmatch;"
    ok "fuzzystrmatch: cleanup done"
}

test_hstore() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS hstore;"
    local result
    result=$(pg_scalar "SELECT 'a=>1,b=>2,c=>3'::hstore -> 'b';")
    if [[ "$result" == "2" ]]; then
        ok "hstore: key 'b' => 2"
    else
        fail "hstore: expected 2, got '$result'"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS hstore;"
    ok "hstore: cleanup done"
}

test_insert_username() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS insert_username;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname = 'insert_username' AND pronamespace = 'public'::regnamespace;")
    if [[ "$result" -ge 1 ]]; then
        ok "insert_username: trigger function installed ($result functions)"
    else
        fail "insert_username: function not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS insert_username CASCADE;"
    ok "insert_username: cleanup done"
}

test_intagg() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS intagg;"
    # intagg provides int_array_aggregate (aggregate) and int_array_enum (function)
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname IN ('int_array_aggregate','int_array_enum') AND pronamespace = 'public'::regnamespace;")
    if [[ "$result" -ge 2 ]]; then
        ok "intagg: aggregate and enum functions installed ($result)"
    else
        fail "intagg: functions not found ($result)"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS intagg;"
    ok "intagg: cleanup done"
}

test_intarray() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS intarray;"
    local result
    result=$(pg_scalar "SELECT uniq('{1,2,2,3,3,4}'::int[])::text;")
    if [[ "$result" == "{1,2,3,4}" ]]; then
        ok "intarray: uniq('{1,2,2,3,3,4}') = {1,2,3,4}"
    else
        fail "intarray: expected {1,2,3,4}, got $result"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS intarray;"
    ok "intarray: cleanup done"
}

test_isn() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS isn;"
    local result
    result=$(pg_scalar "SELECT is_valid('978-0-306-40615-7'::isbn13)::text;")
    if [[ "$result" == "t" || "$result" == "true" ]]; then
        ok "isn: ISBN-13 validation works"
    else
        fail "isn: expected true, got '$result'"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS isn;"
    ok "isn: cleanup done"
}

test_lo() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS lo;"
    pg_exec "CREATE TABLE IF NOT EXISTS _lotest (id serial, data lo);"
    # Just verify the type exists and table was created
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_type WHERE typname = 'lo';")
    if [[ "$result" -ge 1 ]]; then
        ok "lo: large object type registered"
    else
        fail "lo: type not found"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _lotest;"
    pg_exec "DROP EXTENSION IF EXISTS lo;"
    ok "lo: cleanup done"
}

test_ltree() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS ltree;"
    local result
    result=$(pg_scalar "SELECT 'Top.Science'::ltree @> 'Top.Science.Astronomy'::ltree;")
    if [[ "$result" == "t" || "$result" == "true" ]]; then
        ok "ltree: 'Top.Science' is ancestor of 'Top.Science.Astronomy'"
    else
        fail "ltree: expected true, got '$result'"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS ltree;"
    ok "ltree: cleanup done"
}

test_moddatetime() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS moddatetime;"
    pg_exec "CREATE TABLE IF NOT EXISTS _mdtest (id serial, name text, updated_at timestamptz DEFAULT now());"
    pg_exec "CREATE TRIGGER _mdtest_mod BEFORE UPDATE ON _mdtest FOR EACH ROW EXECUTE FUNCTION moddatetime(updated_at);"
    pg_exec "INSERT INTO _mdtest (name) VALUES ('test');"
    pg_exec "SELECT pg_sleep(0.1);"
    pg_exec "UPDATE _mdtest SET name = 'modified';"
    local result
    result=$(pg_scalar "SELECT name FROM _mdtest WHERE id = 1;")
    if [[ "$result" == "modified" ]]; then
        ok "moddatetime: trigger updated row successfully"
    else
        fail "moddatetime: expected 'modified', got '$result'"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _mdtest;"
    pg_exec "DROP EXTENSION IF EXISTS moddatetime;"
    ok "moddatetime: cleanup done"
}

test_pageinspect() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pageinspect;"
    pg_exec "CREATE TABLE IF NOT EXISTS _pageinsp (id serial, val text);"
    pg_exec "INSERT INTO _pageinsp (val) SELECT 'row_' || g FROM generate_series(1,10) g;"
    local result
    result=$(pg_scalar "SELECT lp FROM heap_page_items(get_raw_page('_pageinsp', 0)) LIMIT 1;")
    if [[ -n "$result" ]]; then
        ok "pageinspect: heap_page_items returned line pointer $result"
    else
        fail "pageinspect: no line pointer found"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _pageinsp;"
    pg_exec "DROP EXTENSION IF EXISTS pageinspect;"
    ok "pageinspect: cleanup done"
}

test_pg_buffercache() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_buffercache;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_buffercache;")
    if [[ "$result" -ge 0 ]]; then
        ok "pg_buffercache: $result pages in buffer cache"
    else
        fail "pg_buffercache: query failed"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS pg_buffercache;"
    ok "pg_buffercache: cleanup done"
}

test_pg_freespacemap() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_freespacemap;"
    pg_exec "CREATE TABLE IF NOT EXISTS _fsmtest (id serial, val text);"
    pg_exec "INSERT INTO _fsmtest (val) SELECT 'data_' || g FROM generate_series(1,100) g;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_freespace('_fsmtest');")
    if [[ "$result" -ge 0 ]]; then
        ok "pg_freespacemap: $result free space pages for _fsmtest"
    else
        fail "pg_freespacemap: query failed"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _fsmtest;"
    pg_exec "DROP EXTENSION IF EXISTS pg_freespacemap;"
    ok "pg_freespacemap: cleanup done"
}

test_pg_logicalinspect() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_logicalinspect;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname LIKE 'pg_logical_%inspect%' OR proname = 'pg_get_logical_snapshot_meta';")
    if [[ "$result" -ge 1 ]]; then
        ok "pg_logicalinspect: inspect functions installed ($result)"
    else
        fail "pg_logicalinspect: functions not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS pg_logicalinspect;"
    ok "pg_logicalinspect: cleanup done"
}

test_pg_prewarm() {
    info "pg_prewarm requires shared_preload_libraries"
    pg extension install pg_prewarm -i "$INSTANCE" --auto-restart 2>&1 || true
    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_prewarm;"
    pg_exec "CREATE TABLE IF NOT EXISTS _prewarmtest (id serial, val text);"
    pg_exec "INSERT INTO _prewarmtest (val) SELECT 'data_' || g FROM generate_series(1,100) g;"
    local result
    result=$(pg_scalar "SELECT pg_prewarm('_prewarmtest');")
    if [[ -n "$result" ]]; then
        ok "pg_prewarm: prewarmed $result blocks"
    else
        fail "pg_prewarm: returned nothing"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _prewarmtest;"
    pg_exec "DROP EXTENSION IF EXISTS pg_prewarm;"
    ok "pg_prewarm: cleanup done"
}

test_pg_stat_statements() {
    info "pg_stat_statements requires shared_preload_libraries"
    pg extension install pg_stat_statements -i "$INSTANCE" --auto-restart 2>&1 || true
    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;"
    pg_exec "SELECT count(*) FROM pg_class;" > /dev/null
    pg_exec "SELECT 1+1;" > /dev/null
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_stat_statements WHERE calls > 0;")
    if [[ "$result" -gt 0 ]]; then
        ok "pg_stat_statements: $result queries tracked"
    else
        fail "pg_stat_statements: no queries tracked"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS pg_stat_statements;"
    ok "pg_stat_statements: cleanup done"
}

test_pg_surgery() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_surgery;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname = 'heap_force_freeze' AND pronamespace = 'public'::regnamespace;")
    if [[ "$result" -ge 1 ]]; then
        ok "pg_surgery: heap surgery functions installed ($result)"
    else
        fail "pg_surgery: functions not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS pg_surgery;"
    ok "pg_surgery: cleanup done"
}

test_pg_trgm() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_trgm;"
    local result
    result=$(pg_scalar "SELECT similarity('PostgreSQL', 'Postgres');")
    if [[ -n "$result" ]]; then
        ok "pg_trgm: similarity('PostgreSQL','Postgres') = $result"
    else
        fail "pg_trgm: similarity returned nothing"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS pg_trgm;"
    ok "pg_trgm: cleanup done"
}

test_pg_visibility() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_visibility;"
    pg_exec "CREATE TABLE IF NOT EXISTS _vistest (id serial, val text);"
    pg_exec "INSERT INTO _vistest (val) SELECT 'row_' || g FROM generate_series(1,10) g;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_visibility_map('_vistest');")
    if [[ "$result" -ge 0 ]]; then
        ok "pg_visibility: $result pages in visibility map"
    else
        fail "pg_visibility: query failed"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _vistest;"
    pg_exec "DROP EXTENSION IF EXISTS pg_visibility;"
    ok "pg_visibility: cleanup done"
}

test_pg_walinspect() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_walinspect;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_get_wal_stats();")
    if [[ "$result" -ge 0 ]]; then
        ok "pg_walinspect: $result WAL record types found"
    else
        fail "pg_walinspect: query failed"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS pg_walinspect;"
    ok "pg_walinspect: cleanup done"
}

test_pgcrypto() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pgcrypto;"
    local hash
    hash=$(pg_scalar "SELECT crypt('test_password', gen_salt('bf'));")
    local verify
    verify=$(pg_scalar "SELECT (crypt('test_password', '$hash') = '$hash');")
    if [[ "$verify" == "t" ]]; then
        ok "pgcrypto: bcrypt hash + verify passed"
    else
        fail "pgcrypto: bcrypt verify failed"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS pgcrypto;"
    ok "pgcrypto: cleanup done"
}

test_pgrowlocks() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pgrowlocks;"
    pg_exec "CREATE TABLE IF NOT EXISTS _locktest (id serial, val text);"
    pg_exec "INSERT INTO _locktest (val) VALUES ('a'),('b'),('c');"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pgrowlocks('_locktest');")
    if [[ "$result" -ge 0 ]]; then
        ok "pgrowlocks: checked $result locked rows"
    else
        fail "pgrowlocks: query failed"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _locktest;"
    pg_exec "DROP EXTENSION IF EXISTS pgrowlocks;"
    ok "pgrowlocks: cleanup done"
}

test_pgstattuple() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS pgstattuple;"
    pg_exec "DROP TABLE IF EXISTS _stattest;"
    pg_exec "CREATE TABLE _stattest (id serial, val text);"
    pg_exec "INSERT INTO _stattest (val) SELECT 'data_' || g FROM generate_series(1,100) g;"
    local result
    result=$(pg_scalar "SELECT tuple_count FROM pgstattuple('_stattest');")
    if [[ "$result" == "100" ]]; then
        ok "pgstattuple: 100 live tuples confirmed"
    else
        fail "pgstattuple: expected 100, got '$result'"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _stattest;"
    pg_exec "DROP EXTENSION IF EXISTS pgstattuple;"
    ok "pgstattuple: cleanup done"
}

test_postgres_fdw() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS postgres_fdw;"
    # Get current port for self-connection
    local port
    port=$(pg_scalar "SHOW port;")
    local dbname
    dbname=$(pg_scalar "SELECT current_database();")
    pg_exec "CREATE SERVER IF NOT EXISTS _self_fdw FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host '/var/run/postgresql', port '$port', dbname '$dbname');"
    pg_exec "CREATE USER MAPPING IF NOT EXISTS FOR CURRENT_USER SERVER _self_fdw;"
    pg_exec "CREATE FOREIGN TABLE IF NOT EXISTS _fdw_test (id int, val text) SERVER _self_fdw OPTIONS (table_name 'pg_class_oid_seq');" 2>/dev/null || true
    # Just verify the server was created
    local result
    result=$(pg_scalar "SELECT srvname FROM pg_foreign_server WHERE srvname = '_self_fdw';")
    if [[ "$result" == "_self_fdw" ]]; then
        ok "postgres_fdw: self-referencing server created"
    else
        fail "postgres_fdw: server not found"
        return 1
    fi
    pg_exec "DROP FOREIGN TABLE IF EXISTS _fdw_test;"
    pg_exec "DROP USER MAPPING IF EXISTS FOR CURRENT_USER SERVER _self_fdw;"
    pg_exec "DROP SERVER IF EXISTS _self_fdw CASCADE;"
    pg_exec "DROP EXTENSION IF EXISTS postgres_fdw;"
    ok "postgres_fdw: cleanup done"
}

test_refint() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS refint;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname = 'check_primary_key' AND pronamespace = 'public'::regnamespace;")
    if [[ "$result" -ge 1 ]]; then
        ok "refint: referential integrity trigger functions installed ($result)"
    else
        fail "refint: functions not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS refint CASCADE;"
    ok "refint: cleanup done"
}

test_seg() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS seg;"
    local result
    result=$(pg_scalar "SELECT '1.5 .. 2.5'::seg::text;")
    if [[ -n "$result" ]]; then
        ok "seg: created segment '$result'"
    else
        fail "seg: returned nothing"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS seg;"
    ok "seg: cleanup done"
}

test_sslinfo() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS sslinfo;"
    local result
    result=$(pg_scalar "SELECT ssl_is_used();")
    # Will be false for local connections, but function should work
    if [[ "$result" == "t" || "$result" == "f" ]]; then
        ok "sslinfo: ssl_is_used() = $result"
    else
        fail "sslinfo: unexpected result '$result'"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS sslinfo;"
    ok "sslinfo: cleanup done"
}

test_tablefunc() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS tablefunc;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname = 'crosstab' AND pronamespace = 'public'::regnamespace;")
    if [[ "$result" -ge 1 ]]; then
        ok "tablefunc: crosstab function installed ($result functions)"
    else
        fail "tablefunc: function not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS tablefunc;"
    ok "tablefunc: cleanup done"
}

test_tcn() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS tcn;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname = 'triggered_change_notification' AND pronamespace = 'public'::regnamespace;")
    if [[ "$result" -ge 1 ]]; then
        ok "tcn: triggered_change_notification function installed"
    else
        fail "tcn: function not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS tcn CASCADE;"
    ok "tcn: cleanup done"
}

test_tsm_system_rows() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS tsm_system_rows;"
    pg_exec "CREATE TABLE IF NOT EXISTS _tsmtest (id serial, val text);"
    pg_exec "INSERT INTO _tsmtest (val) SELECT 'row_' || g FROM generate_series(1,100) g;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM _tsmtest TABLESAMPLE system_rows(10);")
    if [[ "$result" -le 10 ]]; then
        ok "tsm_system_rows: sampled $result rows (requested 10)"
    else
        fail "tsm_system_rows: got $result rows, expected <= 10"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _tsmtest;"
    pg_exec "DROP EXTENSION IF EXISTS tsm_system_rows;"
    ok "tsm_system_rows: cleanup done"
}

test_tsm_system_time() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS tsm_system_time;"
    pg_exec "CREATE TABLE IF NOT EXISTS _tsmttest (id serial, val text);"
    pg_exec "INSERT INTO _tsmttest (val) SELECT 'row_' || g FROM generate_series(1,100) g;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM _tsmttest TABLESAMPLE system_time(100);")
    if [[ "$result" -ge 0 ]]; then
        ok "tsm_system_time: sampled $result rows within 100ms"
    else
        fail "tsm_system_time: query failed"
        return 1
    fi
    pg_exec "DROP TABLE IF EXISTS _tsmttest;"
    pg_exec "DROP EXTENSION IF EXISTS tsm_system_time;"
    ok "tsm_system_time: cleanup done"
}

test_unaccent() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS unaccent;"
    local result
    result=$(pg_scalar "SELECT unaccent('Crème Brûlée');")
    if [[ "$result" == "Creme Brulee" ]]; then
        ok "unaccent: 'Crème Brûlée' => 'Creme Brulee'"
    else
        fail "unaccent: expected 'Creme Brulee', got '$result'"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS unaccent;"
    ok "unaccent: cleanup done"
}

test_uuid_ossp() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS \"uuid-ossp\";"
    local result
    result=$(pg_scalar "SELECT uuid_generate_v4() IS NOT NULL;")
    if [[ "$result" == "t" ]]; then
        ok "uuid-ossp: uuid_generate_v4() works"
    else
        fail "uuid-ossp: uuid_generate_v4() returned null"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS \"uuid-ossp\";"
    ok "uuid-ossp: cleanup done"
}

test_xml2() {
    pg_exec "CREATE EXTENSION IF NOT EXISTS xml2;"
    local result
    result=$(pg_scalar "SELECT count(*) FROM pg_proc WHERE proname = 'xpath_table' AND pronamespace = 'public'::regnamespace;")
    if [[ "$result" -ge 1 ]]; then
        ok "xml2: XML parsing functions installed ($result)"
    else
        fail "xml2: functions not found"
        return 1
    fi
    pg_exec "DROP EXTENSION IF EXISTS xml2;"
    ok "xml2: cleanup done"
}

# ─── CLI ──────────────────────────────────────────────────────────────
list_extensions() {
    echo "Available builtin extensions (${#ALL_EXTENSIONS[@]}):"
    for ext in "${ALL_EXTENSIONS[@]}"; do
        echo "  $ext"
    done
}

run_test() {
    local name="$1"
    local func="test_${name//-/_}"
    # Replace hyphens in function names: uuid-ossp -> uuid_ossp
    func="test_$(echo "$name" | tr '-' '_')"

    if declare -f "$func" > /dev/null 2>&1; then
        echo ""
        echo "══════════════════════════════════════"
        echo " Testing: $name"
        echo "══════════════════════════════════════"
        if "$func"; then
            return 0
        else
            return 1
        fi
    else
        warn "No test function for: $name (expected: $func)"
        return 1
    fi
}

usage() {
    echo "Usage: $0 [options]"
    echo ""
    echo "Options:"
    echo "  --ext <name>   Test a single builtin extension"
    echo "  --all          Test all builtin extensions"
    echo "  --list         List available builtin extensions"
    echo "  -i <instance>  Target instance (default: $INSTANCE)"
    echo ""
    echo "Examples:"
    echo "  $0 --ext hstore"
    echo "  $0 --ext pg_stat_statements"
    echo "  $0 --ext uuid-ossp"
    echo "  $0 --all"
    echo "  $0 --all -i pg01"
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --ext)
            EXT_NAME="$2"
            shift 2
            ;;
        --all)
            RUN_ALL=true
            shift
            ;;
        --list)
            list_extensions
            exit 0
            ;;
        -i)
            INSTANCE="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

if [[ -z "$EXT_NAME" && "$RUN_ALL" == false ]]; then
    usage
    exit 1
fi

echo "Instance: $INSTANCE"

if [[ -n "$EXT_NAME" ]]; then
    run_test "$EXT_NAME"
    exit $?
fi

if [[ "$RUN_ALL" == true ]]; then
    passed=0
    failed=0
    skipped=0
    failed_list=""

    for ext in "${ALL_EXTENSIONS[@]}"; do
        if run_test "$ext"; then
            ((passed++))
        else
            ((failed++))
            failed_list="$failed_list  - $ext\n"
        fi
    done

    echo ""
    echo "══════════════════════════════════════"
    echo " Results: ${passed} passed, ${failed} failed, ${skipped} skipped"
    echo "══════════════════════════════════════"
    if [[ $failed -gt 0 ]]; then
        echo "Failed:"
        echo -e "$failed_list"
        exit 1
    fi
fi
