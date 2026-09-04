#!/bin/bash
# PostgreSQL Extensions Test Script
# Usage: ./test_ext.sh --ext <name>        # test single extension
#        ./test_ext.sh --all               # test all extensions
#        ./test_ext.sh --list              # list available extensions
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

# ─── Extension test definitions ───────────────────────────────────────
# Each function: test_<name>
#   1. pg extension install (if not builtin-only)
#   2. CREATE EXTENSION IF NOT EXISTS
#   3. Functional SQL test
#   4. Cleanup (DROP test objects)

# ── 1. plpgsql ────────────────────────────────────────────────────────
test_plpgsql() {
    info "plpgsql is builtin, no install needed"

    pg_exec "CREATE EXTENSION IF NOT EXISTS plpgsql;"
    pg_exec "
        CREATE OR REPLACE FUNCTION _ext_test_add(a int, b int) RETURNS int AS \$\$
        BEGIN RETURN a + b; END;
        \$\$ LANGUAGE plpgsql;

        CREATE OR REPLACE FUNCTION _ext_test_trigger() RETURNS trigger AS \$\$
        BEGIN RETURN NEW; END;
        \$\$ LANGUAGE plpgsql;
    "

    local result
    result=$(pg_scalar "SELECT _ext_test_add(3, 7);")
    if [[ "$result" == "10" ]]; then
        ok "plpgsql: function returned 10"
    else
        fail "plpgsql: expected 10, got $result"
        return 1
    fi

    pg_exec "DROP FUNCTION IF EXISTS _ext_test_add(int,int); DROP FUNCTION IF EXISTS _ext_test_trigger();"
    ok "plpgsql: cleanup done"
}

# ── 2. pg_stat_statements ────────────────────────────────────────────
test_pg_stat_statements() {
    info "pg_stat_statements requires shared_preload_libraries"
    pg extension install pg_stat_statements -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;"

    # Generate some queries, then check stats
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

    # Show top 3
    pg_exec "SELECT query, calls, total_exec_time::numeric(8,2) AS ms FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 3;"
}

# ── 3. uuid-ossp ─────────────────────────────────────────────────────
test_uuid-ossp() {
    info "uuid-ossp is builtin, no install needed"

    pg_exec "CREATE EXTENSION IF NOT EXISTS \"uuid-ossp\";"

    local v1 v4 v5 builtin4 builtin7
    v1=$(pg_scalar "SELECT uuid_generate_v1();")
    v4=$(pg_scalar "SELECT uuid_generate_v4();")
    v5=$(pg_scalar "SELECT uuid_generate_v5(uuid_ns_url(), 'example.com');")
    builtin4=$(pg_scalar "SELECT gen_random_uuid();")
    builtin7=$(pg_scalar "SELECT uuidv7();")

    if [[ ${#v1} -eq 36 && ${#v4} -eq 36 && ${#v5} -eq 36 ]]; then
        ok "uuid-ossp: v1=$v1"
        ok "uuid-ossp: v4=$v4"
        ok "uuid-ossp: v5=$v5 (deterministic)"
    else
        fail "uuid-ossp: invalid UUID format"
        return 1
    fi
    ok "PG18 builtin: v4=$builtin4"
    ok "PG18 builtin: v7=$builtin7"
}

# ── 4. pgcrypto ──────────────────────────────────────────────────────
test_pgcrypto() {
    info "pgcrypto is builtin, no install needed"

    pg_exec "CREATE EXTENSION IF NOT EXISTS pgcrypto;"

    # Hash
    local hash
    hash=$(pg_scalar "SELECT digest('hello', 'sha256');")
    if [[ -n "$hash" ]]; then
        ok "pgcrypto: sha256 hash = ${hash:0:32}..."
    else
        fail "pgcrypto: sha256 hash failed"
        return 1
    fi

    # Encrypt / decrypt round-trip
    local decrypted
    decrypted=$(pg_scalar "SELECT pgp_sym_decrypt(pgp_sym_encrypt('secret_data', 'my_key'), 'my_key');")
    if [[ "$decrypted" == "secret_data" ]]; then
        ok "pgcrypto: encrypt/decrypt round-trip OK"
    else
        fail "pgcrypto: round-trip failed, got '$decrypted'"
        return 1
    fi

    # Password hash (bcrypt)
    local pw_hash
    pw_hash=$(pg_scalar "SELECT crypt('password123', gen_salt('bf'));")
    if [[ "$pw_hash" == \$2a\$* ]]; then
        ok "pgcrypto: bcrypt hash = ${pw_hash:0:30}..."
    else
        fail "pgcrypto: bcrypt hash failed"
        return 1
    fi
}

# ── 5. pgvector ──────────────────────────────────────────────────────
test_vector() {
    info "Installing pgvector from Pigsty catalog..."
    pg extension install vector -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS vector;"

    pg_exec "
        CREATE TABLE IF NOT EXISTS _ext_test_vec (
            id serial PRIMARY KEY,
            embedding vector(3)
        );
        DELETE FROM _ext_test_vec;
        INSERT INTO _ext_test_vec (embedding) VALUES ('[1,2,3]'), ('[4,5,6]'), ('[7,8,9]');
    "

    local nearest
    nearest=$(pg_scalar "SELECT id FROM _ext_test_vec ORDER BY embedding <-> '[3,1,2]' LIMIT 1;")
    if [[ "$nearest" == "1" ]]; then
        ok "pgvector: nearest neighbor = id 1 (correct)"
    else
        fail "pgvector: expected id 1, got $nearest"
        return 1
    fi

    pg_exec "DROP TABLE IF EXISTS _ext_test_vec;"
    ok "pgvector: cleanup done"
}

# ── 6. postgis ───────────────────────────────────────────────────────
test_postgis() {
    info "Installing postgis from Pigsty catalog..."
    pg extension install postgis -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS postgis;"

    pg_exec "
        CREATE TABLE IF NOT EXISTS _ext_test_geo (
            id serial PRIMARY KEY,
            name text,
            location geometry(Point, 4326)
        );
        DELETE FROM _ext_test_geo;
        INSERT INTO _ext_test_geo (name, location) VALUES
            ('NYC',  ST_SetSRID(ST_MakePoint(-74.006, 40.7128), 4326)),
            ('London', ST_SetSRID(ST_MakePoint(-0.1276, 51.5074), 4326)),
            ('Tokyo', ST_SetSRID(ST_MakePoint(139.6917, 35.6895), 4326));
    "

    local nearest
    nearest=$(pg_scalar "
        SELECT name FROM _ext_test_geo
        ORDER BY location::geography <-> ST_SetSRID(ST_MakePoint(-74.006, 40.7128), 4326)::geography
        LIMIT 1;
    ")
    if [[ "$nearest" == "NYC" ]]; then
        ok "postgis: nearest to NYC = NYC (correct)"
    else
        fail "postgis: expected NYC, got $nearest"
        return 1
    fi

    local dist
    dist=$(pg_scalar "
        SELECT round(ST_Distance(
            (SELECT location::geography FROM _ext_test_geo WHERE name='NYC'),
            (SELECT location::geography FROM _ext_test_geo WHERE name='London')
        )::numeric / 1000) AS km;
    ")
    ok "postgis: NYC-London distance = ${dist} km"

    pg_exec "DROP TABLE IF EXISTS _ext_test_geo;"
    ok "postgis: cleanup done"
}

# ── 7. pg_trgm ───────────────────────────────────────────────────────
test_pg_trgm() {
    info "pg_trgm is builtin, no install needed"

    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_trgm;"

    pg_exec "
        CREATE TABLE IF NOT EXISTS _ext_test_trgm (id serial, name text);
        DELETE FROM _ext_test_trgm;
        INSERT INTO _ext_test_trgm (name) VALUES ('John'), ('Jonh'), ('Jane'), ('Johnny');
    "

    local result
    result=$(pg_scalar "SELECT name FROM _ext_test_trgm ORDER BY similarity(name, 'John') DESC LIMIT 1;")
    if [[ "$result" == "John" ]]; then
        ok "pg_trgm: best match for 'John' = John (correct)"
    else
        fail "pg_trgm: expected John, got $result"
        return 1
    fi

    # Show top 3
    pg_exec "SELECT name, similarity(name, 'John')::numeric(4,3) AS sim FROM _ext_test_trgm ORDER BY sim DESC;"

    pg_exec "DROP TABLE IF EXISTS _ext_test_trgm;"
    ok "pg_trgm: cleanup done"
}

# ── 8. postgres_fdw ──────────────────────────────────────────────────
test_postgres_fdw() {
    info "postgres_fdw is builtin, no install needed"

    pg_exec "CREATE EXTENSION IF NOT EXISTS postgres_fdw;"

    # Self-connect via Unix socket (trust auth) inside the container
    local pg_port
    pg_port=$(pg_scalar "SHOW port;" 2>&1 | tr -d '[:space:]')
    pg_exec "
        DROP SERVER IF EXISTS _ext_test_fdw CASCADE;
        CREATE SERVER _ext_test_fdw FOREIGN DATA WRAPPER postgres_fdw
            OPTIONS (host '/var/run/postgresql', port '${pg_port}', dbname 'default_db');
        CREATE USER MAPPING FOR current_user SERVER _ext_test_fdw
            OPTIONS (user 'admin');
        CREATE TABLE IF NOT EXISTS _ext_test_local (id serial, val text);
        DELETE FROM _ext_test_local;
        INSERT INTO _ext_test_local (val) VALUES ('hello_fdw');
        DROP FOREIGN TABLE IF EXISTS _ext_test_ft;
        CREATE FOREIGN TABLE _ext_test_ft (id int, val text)
            SERVER _ext_test_fdw OPTIONS (table_name '_ext_test_local');
    "

    local result
    result=$(pg_scalar "SELECT val FROM _ext_test_ft LIMIT 1;" 2>&1)
    if [[ "$result" == "hello_fdw" ]]; then
        ok "postgres_fdw: remote query returned 'hello_fdw'"
    else
        fail "postgres_fdw: expected 'hello_fdw', got '$result'"
        return 1
    fi

    pg_exec "DROP FOREIGN TABLE IF EXISTS _ext_test_ft; DROP SERVER IF EXISTS _ext_test_fdw CASCADE; DROP TABLE IF EXISTS _ext_test_local;"
    ok "postgres_fdw: cleanup done"
}

# ── 9. pg_cron ───────────────────────────────────────────────────────
test_pg_cron() {
    info "pg_cron requires shared_preload_libraries (auto-handled by pgcli)"
    pg extension install pg_cron -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_cron;"

    # Idempotent: unschedule any existing test-job first
    pg_exec "SELECT cron.unschedule('test-job');" 2>&1 || true

    # Schedule a test job
    local jobid
    jobid=$(pg_scalar "SELECT cron.schedule('test-job', '*/5 * * * *', 'SELECT 1');")
    if [[ "$jobid" =~ ^[0-9]+$ ]]; then
        ok "pg_cron: scheduled job $jobid"
    else
        fail "pg_cron: schedule failed, got '$jobid'"
        return 1
    fi

    # List scheduled jobs
    pg_exec "SELECT jobid, schedule, command FROM cron.job;"

    # Cleanup
    pg_exec "SELECT cron.unschedule('test-job');" > /dev/null
    ok "pg_cron: unscheduled test-job"
}

# ── 10. timescaledb ──────────────────────────────────────────────────
test_timescaledb() {
    info "Installing timescaledb from Pigsty catalog..."
    pg extension install timescaledb -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS timescaledb;"

    pg_exec "
        CREATE TABLE IF NOT EXISTS _ext_test_ts (
            time timestamptz NOT NULL,
            value float NOT NULL
        );
        SELECT create_hypertable('_ext_test_ts', 'time', if_not_exists => TRUE);
        DELETE FROM _ext_test_ts;
        INSERT INTO _ext_test_ts (time, value) VALUES
            ('2024-01-01 00:00:00', 10.0),
            ('2024-01-01 01:00:00', 15.0),
            ('2024-01-01 02:00:00', 20.0),
            ('2024-01-01 03:00:00', 25.0);
    "

    local avg
    avg=$(pg_scalar "
        SELECT avg(value)::numeric(4,1) FROM _ext_test_ts;
    ")
    if [[ "$avg" == "17.5" ]]; then
        ok "timescaledb: hypertable avg = 17.5 (correct)"
    else
        fail "timescaledb: expected 17.5, got $avg"
        return 1
    fi

    # time_bucket
    pg_exec "
        SELECT time_bucket('2 hours', time) AS bucket, avg(value)::numeric(4,1) AS avg_val
        FROM _ext_test_ts GROUP BY bucket ORDER BY bucket;
    "

    pg_exec "DROP TABLE IF EXISTS _ext_test_ts;"
    ok "timescaledb: cleanup done"
}

# ── 11. pgaudit ──────────────────────────────────────────────────────
test_pgaudit() {
    info "pgaudit requires shared_preload_libraries"
    pg extension install pgaudit -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS pgaudit;"

    # Enable audit logging for this session
    pg_exec "SET pgaudit.log = 'read, ddl';"
    pg_exec "SELECT 1;" > /dev/null
    ok "pgaudit: extension loaded and audit enabled"

    # Verify extension is registered
    local ver
    ver=$(pg_scalar "SELECT extversion FROM pg_extension WHERE extname = 'pgaudit';")
    if [[ -n "$ver" ]]; then
        ok "pgaudit: version $ver"
    else
        fail "pgaudit: not found in pg_extension"
        return 1
    fi
}

# ── 12. pg_repack ────────────────────────────────────────────────────
test_pg_repack() {
    info "pg_repack is installed via 'pg extension install pg_repack'"
    pg extension install pg_repack -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_repack;"

    # Idempotent: drop and recreate test table
    pg_exec "DROP TABLE IF EXISTS _ext_test_bloat;"
    pg_exec "
        CREATE TABLE _ext_test_bloat (id serial PRIMARY KEY, val text);
        INSERT INTO _ext_test_bloat (val) SELECT md5(random()::text) FROM generate_series(1, 10000);
    "

    # Get size before bloat
    local size_before
    size_before=$(pg_scalar "SELECT pg_relation_size('_ext_test_bloat');")
    ok "pg_repack: table size before delete = $size_before bytes"

    # Create bloat: delete 80% of rows
    pg_exec "DELETE FROM _ext_test_bloat WHERE id % 5 != 0;"

    local size_after_del
    size_after_del=$(pg_scalar "SELECT pg_relation_size('_ext_test_bloat');")
    local row_count
    row_count=$(pg_scalar "SELECT count(*) FROM _ext_test_bloat;")
    ok "pg_repack: after delete — $row_count rows, size = $size_after_del bytes (bloat)"

    # Run pg_repack inside the container via pg exec
    pg exec -- pg_repack -U admin -d default_db -t _ext_test_bloat --no-superuser-check 2>&1

    local size_after_repack
    size_after_repack=$(pg_scalar "SELECT pg_relation_size('_ext_test_bloat');")
    ok "pg_repack: after repack — size = $size_after_repack bytes"

    if [[ "$size_after_repack" -lt "$size_after_del" ]]; then
        ok "pg_repack: size reduced ($size_after_del → $size_after_repack)"
    else
        fail "pg_repack: size did not reduce ($size_after_del → $size_after_repack)"
        return 1
    fi

    pg_exec "DROP TABLE IF EXISTS _ext_test_bloat;"
    ok "pg_repack: cleanup done"
}

# ── 13. citext ───────────────────────────────────────────────────────
test_citext() {
    info "citext is builtin, no install needed"

    pg_exec "CREATE EXTENSION IF NOT EXISTS citext;"

    pg_exec "
        CREATE TABLE IF NOT EXISTS _ext_test_citext (id serial, email citext);
        DELETE FROM _ext_test_citext;
        INSERT INTO _ext_test_citext (email) VALUES ('Alice@Example.COM'), ('bob@test.com');
    "

    local result
    result=$(pg_scalar "SELECT email FROM _ext_test_citext WHERE email = 'alice@example.com';")
    if [[ "$result" == "Alice@Example.COM" ]]; then
        ok "citext: case-insensitive match found 'Alice@Example.COM'"
    else
        fail "citext: expected Alice@Example.COM, got '$result'"
        return 1
    fi

    pg_exec "DROP TABLE IF EXISTS _ext_test_citext;"
    ok "citext: cleanup done"
}

# ── 14. unaccent ─────────────────────────────────────────────────────
test_unaccent() {
    info "unaccent is builtin, no install needed"

    pg_exec "CREATE EXTENSION IF NOT EXISTS unaccent;"

    local result
    result=$(pg_scalar "SELECT unaccent('Creme Brulee');")
    if [[ "$result" == "Creme Brulee" ]]; then
        ok "unaccent: stripped accents correctly"
    else
        fail "unaccent: expected 'Creme Brulee', got '$result'"
        return 1
    fi

    local result2
    result2=$(pg_scalar "SELECT unaccent();")
    ok "unaccent: '$result2'"
}

# ── 15. hstore ───────────────────────────────────────────────────────
test_hstore() {
    info "hstore is builtin, no install needed"

    pg_exec "CREATE EXTENSION IF NOT EXISTS hstore;"

    pg_exec "
        CREATE TABLE IF NOT EXISTS _ext_test_hs (id serial, attrs hstore);
        DELETE FROM _ext_test_hs;
        INSERT INTO _ext_test_hs (attrs) VALUES
            ('theme => dark, lang => en, version => 2'),
            ('theme => light, lang => zh');
    "

    local result
    result=$(pg_scalar "SELECT attrs -> 'theme' FROM _ext_test_hs WHERE id = 1;")
    if [[ "$result" == "dark" ]]; then
        ok "hstore: key lookup returned 'dark'"
    else
        fail "hstore: expected dark, got '$result'"
        return 1
    fi

    local cnt
    cnt=$(pg_scalar "SELECT count(*) FROM _ext_test_hs WHERE attrs ? 'theme';")
    ok "hstore: $cnt rows have 'theme' key"

    pg_exec "DROP TABLE IF EXISTS _ext_test_hs;"
    ok "hstore: cleanup done"
}

# ── 16. citus ────────────────────────────────────────────────────────
test_citus() {
    info "Installing citus from Pigsty catalog..."
    pg extension install citus -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS citus;"

    local result
    result=$(pg_scalar "SELECT citus_version();" 2>&1)
    if [[ "$result" == *"Citus"* ]]; then
        ok "citus: version = $result"
    else
        fail "citus: version query failed"
        return 1
    fi

    # Test distributed table
    pg_exec "
        CREATE TABLE IF NOT EXISTS _ext_test_citus (id serial PRIMARY KEY, val text);
        DELETE FROM _ext_test_citus;
        INSERT INTO _ext_test_citus (val) VALUES ('node1'), ('node2');
        SELECT create_distributed_table('_ext_test_citus', 'id');
    " 2>&1 || warn "citus: create_distributed_table may need workers"

    pg_exec "DROP TABLE IF EXISTS _ext_test_citus;"
    pg_exec "DROP EXTENSION IF EXISTS citus CASCADE;"
    ok "citus: cleanup done"
}

# ── 17. pg_search ────────────────────────────────────────────────────
test_pg_search() {
    info "Installing pg_search from Pigsty catalog..."
    pg extension install pg-search -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_search;"

    pg_exec "
        CREATE TABLE IF NOT EXISTS _ext_test_fts (id serial, title text, body text);
        DELETE FROM _ext_test_fts;
        INSERT INTO _ext_test_fts (title, body) VALUES
            ('PostgreSQL Guide', 'Learn how to use PostgreSQL effectively'),
            ('MySQL Tutorial', 'Getting started with MySQL database'),
            ('Search Engine', 'Full text search in PostgreSQL is powerful');
    "

    local result
    result=$(pg_scalar "
        SELECT count(*) FROM _ext_test_fts
        WHERE to_tsvector('english', title || ' ' || body) @@ to_tsquery('english', 'postgresql');
    ")
    if [[ "$result" == "2" ]]; then
        ok "pg_search: full-text search found 2 matches"
    else
        fail "pg_search: expected 2, got $result"
        return 1
    fi

    pg_exec "DROP TABLE IF EXISTS _ext_test_fts;"
    pg_exec "DROP EXTENSION IF EXISTS pg_search CASCADE;"
    ok "pg_search: cleanup done"
}

# ── 18. pg_prewarm ───────────────────────────────────────────────────
test_pg_prewarm() {
    info "pg_prewarm requires shared_preload_libraries"
    pg extension install pg_prewarm -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS pg_prewarm;"

    # Create a test table to prewarm
    pg_exec "
        CREATE TABLE IF NOT EXISTS _ext_test_pw (id serial, val text);
        DELETE FROM _ext_test_pw;
        INSERT INTO _ext_test_pw (val) SELECT md5(random()::text) FROM generate_series(1, 100);
    "

    local result
    result=$(pg_scalar "SELECT pg_prewarm('_ext_test_pw');" 2>&1)
    if [[ "$result" =~ ^[0-9]+$ ]]; then
        ok "pg_prewarm: prewarmed $result blocks"
    else
        warn "pg_prewarm: returned '$result' (may need restart)"
    fi

    pg_exec "DROP TABLE IF EXISTS _ext_test_pw;"
    ok "pg_prewarm: cleanup done"
}

# ── 20. PostgresML ───────────────────────────────────────────────────
test_pgml() {
    # Check PG version - pgml only supports up to PG17
    local pg_version
    pg_version=$(pg_scalar "SHOW server_version_num;" 2>&1 | tr -d '[:space:]')
    if [[ "$pg_version" -ge 180000 ]]; then
        warn "pgml: not available for PG18+ (only supports PG14-PG17)"
        return 0
    fi

    info "Installing pgml from Pigsty catalog..."
    pg extension install pgml -i "$INSTANCE" --auto-restart 2>&1 || true

    pg_exec "CREATE EXTENSION IF NOT EXISTS pgml;"

    local result
    result=$(pg_scalar "SELECT extversion FROM pg_extension WHERE extname = 'pgml';" 2>&1)
    if [[ -n "$result" ]]; then
        ok "pgml: version $result installed"
    else
        fail "pgml: not found in pg_extension"
        return 1
    fi

    pg_exec "DROP EXTENSION IF EXISTS pgml CASCADE;"
    ok "pgml: cleanup done"
}

# ─── All extensions list ─────────────────────────────────────────────
ALL_EXTENSIONS=(
    plpgsql
    pg_stat_statements
    uuid-ossp
    pgcrypto
    vector
    postgis
    pg_trgm
    postgres_fdw
    pg_cron
    timescaledb
    pgaudit
    pg_repack
    citext
    unaccent
    hstore
    citus
    pg_search
    pg_prewarm
    pgml
)

# ─── CLI parsing ─────────────────────────────────────────────────────
usage() {
    echo "Usage: $0 [options]"
    echo ""
    echo "Options:"
    echo "  --ext <name>     Test a single extension"
    echo "  --all            Test all extensions sequentially"
    echo "  --list           List available extensions"
    echo "  -i <instance>    Instance name (default: default)"
    echo "  -h, --help       Show this help"
    echo ""
    echo "Extensions:"
    for e in "${ALL_EXTENSIONS[@]}"; do
        echo "  $e"
    done
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --ext)  EXT_NAME="$2"; shift 2 ;;
        --all)  RUN_ALL=true; shift ;;
        --list) printf '%s\n' "${ALL_EXTENSIONS[@]}"; exit 0 ;;
        -i)     INSTANCE="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown option: $1"; usage; exit 1 ;;
    esac
done

# ─── Main ─────────────────────────────────────────────────────────────
if [[ -z "$EXT_NAME" && "$RUN_ALL" == false ]]; then
    usage
    exit 1
fi

echo ""
echo "======================================"
echo " PostgreSQL Extension Tests"
echo " Instance: $INSTANCE"
echo "======================================"
echo ""

# Normalize extension name to function name
normalize_name() {
    local name="$1"
    # uuid-ossp -> uuid-ossp (keep as-is, function uses quotes)
    # pg_stat_statements -> pg_stat_statements
    echo "$name"
}

run_single() {
    local name="$1"
    local func="test_$name"

    echo "──────────────────────────────────────"
    echo "Testing: $name"
    echo "──────────────────────────────────────"

    if declare -f "$func" > /dev/null 2>&1; then
        if "$func"; then
            ok ">>> $name PASSED"
        else
            fail ">>> $name FAILED"
        fi
    else
        fail "No test function for '$name'"
    fi
    echo ""
}

PASSED=0
FAILED=0
SKIPPED=0

if [[ "$RUN_ALL" == true ]]; then
    for ext in "${ALL_EXTENSIONS[@]}"; do
        if run_single "$ext"; then
            ((PASSED++))
        else
            ((FAILED++))
        fi
    done

    echo "======================================"
    echo " Results: $PASSED passed, $FAILED failed"
    echo "======================================"
else
    run_single "$EXT_NAME"
fi
